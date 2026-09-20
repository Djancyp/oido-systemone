package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// limiter is a per-client token bucket. The client is the API key when auth is
// on (a shared IP behind NAT is not one client), else the remote IP.
// ponytail: X-Forwarded-For is not trusted, so behind a proxy without API_KEY
// every client is the proxy; set API_KEY or rate limit at the proxy.
type limiter struct {
	rps   rate.Limit
	burst int
	mu    sync.Mutex
	seen  map[string]*client
}

type client struct {
	l    *rate.Limiter
	last time.Time
}

func newLimiter(rps float64, burst int) *limiter {
	return &limiter{rps: rate.Limit(rps), burst: max(burst, 1), seen: map[string]*client{}}
}

func clientID(r *http.Request) string {
	if k, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && k != "" {
		return "key:" + k
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

// allow reports whether r may proceed; idle clients are dropped as a side effect.
func (l *limiter) allow(r *http.Request) bool {
	id, now := clientID(r), time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, c := range l.seen {
		if now.Sub(c.last) > 10*time.Minute {
			delete(l.seen, k)
		}
	}
	c := l.seen[id]
	if c == nil {
		c = &client{l: rate.NewLimiter(l.rps, l.burst)}
		l.seen[id] = c
	}
	c.last = now
	return c.l.Allow()
}
