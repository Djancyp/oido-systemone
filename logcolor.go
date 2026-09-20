package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

// colorOn is set by initLogging. Off when piped, so log files stay plain.
var colorOn bool

// useColor: LOG_COLOR=always|never overrides; else on for a terminal unless NO_COLOR is set.
func useColor() bool {
	switch os.Getenv("LOG_COLOR") {
	case "always", "true":
		return true
	case "never", "false":
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// paint wraps s in an ANSI style when color is on.
func paint(style, s string) string {
	if !colorOn {
		return s
	}
	return style + s + reset
}

// colorHandler prints "15:04:05.000 LEVEL message key=value ..." with ANSI colors.
// ponytail: groups are not supported (nothing here uses them).
type colorHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Leveler
	attrs []slog.Attr
}

func (h *colorHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *colorHandler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), as...)
	return &c
}

func (h *colorHandler) WithGroup(string) slog.Handler { return h }

func (h *colorHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(dim + r.Time.Format("15:04:05.000") + reset + " ")
	lvl := r.Level.String()
	b.WriteString(levelStyle(r.Level) + lvl + reset + strings.Repeat(" ", max(0, 5-len(lvl))) + " ")
	b.WriteString(bold + r.Message + reset)
	add := func(a slog.Attr) {
		v := a.Value.Resolve().String()
		if v == "" || strings.ContainsAny(v, " =\"") {
			v = strconv.Quote(v)
		}
		b.WriteString(" " + dim + a.Key + "=" + reset)
		if st := valueStyle(a.Key, v); st != "" {
			v = st + v + reset
		}
		b.WriteString(v)
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	b.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func levelStyle(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return bold + red
	case l >= slog.LevelWarn:
		return bold + yellow
	case l >= slog.LevelInfo:
		return green
	}
	return dim
}

// valueStyle colors the values worth spotting: HTTP status by class, errors red.
func valueStyle(key, v string) string {
	switch key {
	case "err":
		return red
	case "status":
		switch code, _ := strconv.Atoi(v); {
		case code >= 500:
			return red
		case code >= 400:
			return yellow
		case code >= 300:
			return cyan
		case code >= 200:
			return green
		}
	}
	return ""
}
