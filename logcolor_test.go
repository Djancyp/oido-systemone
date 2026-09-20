package main

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestColorHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(&colorHandler{mu: &sync.Mutex{}, w: &buf, level: slog.LevelInfo})
	log.Info("request", "path", "/v1/systemone", "status", 200, "note", "two words")
	log.Error("boom", "status", 500, "err", "bad")
	log.Debug("hidden")
	out := buf.String()
	for _, want := range []string{"request", `path=`, `note=`, `"two words"`, green + "200", red + "500", red + "bad"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "hidden") {
		t.Error("debug line printed at info level")
	}
}
