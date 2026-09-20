package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

// metrics is a tiny Prometheus text-format exporter: no dependency, and only
// what an operator needs (traffic, errors, latency sum, tokens, in-flight).
type metrics struct {
	mu       sync.Mutex
	requests map[string]int64    // "model|status" -> count
	tokens   map[string][2]int64 // model -> in, out
	ms       atomic.Int64        // total request time
	count    atomic.Int64
}

func newMetrics() *metrics {
	return &metrics{requests: map[string]int64{}, tokens: map[string][2]int64{}}
}

// observe records one finished /v1/systemone request.
func (m *metrics) observe(model string, status int, ms int64, in, out int) {
	m.mu.Lock()
	m.requests[model+"|"+strconv.Itoa(status)]++
	t := m.tokens[model]
	m.tokens[model] = [2]int64{t[0] + int64(in), t[1] + int64(out)}
	m.mu.Unlock()
	m.ms.Add(ms)
	m.count.Add(1)
}

func (m *metrics) write(w http.ResponseWriter, inflight int) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Fprintln(w, "# TYPE oido_requests_total counter")
	keys := make([]string, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var model, status string
		for i := range k {
			if k[i] == '|' {
				model, status = k[:i], k[i+1:]
			}
		}
		fmt.Fprintf(w, "oido_requests_total{model=%q,status=%q} %d\n", model, status, m.requests[k])
	}
	fmt.Fprintln(w, "# TYPE oido_tokens_total counter")
	models := make([]string, 0, len(m.tokens))
	for k := range m.tokens {
		models = append(models, k)
	}
	sort.Strings(models)
	for _, k := range models {
		fmt.Fprintf(w, "oido_tokens_total{model=%q,direction=\"input\"} %d\n", k, m.tokens[k][0])
		fmt.Fprintf(w, "oido_tokens_total{model=%q,direction=\"output\"} %d\n", k, m.tokens[k][1])
	}
	fmt.Fprintln(w, "# TYPE oido_request_duration_ms_sum counter")
	fmt.Fprintf(w, "oido_request_duration_ms_sum %d\n", m.ms.Load())
	fmt.Fprintln(w, "# TYPE oido_request_duration_ms_count counter")
	fmt.Fprintf(w, "oido_request_duration_ms_count %d\n", m.count.Load())
	fmt.Fprintln(w, "# TYPE oido_in_flight gauge")
	fmt.Fprintf(w, "oido_in_flight %d\n", inflight)
}
