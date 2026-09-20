# Production

Built in:
- `GET /healthz` returns `200 ok`. The server only listens once every model is loaded, so it is both liveness and readiness. No key needed, not access-logged.
- `GET /metrics`: Prometheus text format. Needs the key when `API_KEY` is set (`authorization: {credentials: ...}` in the scrape config). Series: `oido_requests_total{model,status}`, `oido_tokens_total{model,direction}`, `oido_request_duration_ms_sum/_count`, `oido_in_flight`. `model="none"` = rejected before a model was chosen (auth, rate limit, overload).
- TLS: set `TLS_CERT` and `TLS_KEY`, or terminate at a proxy.
- Rate limit per client: `RATE_LIMIT` (req/s) and `RATE_BURST`. The client is the API key, else the remote IP. `X-Forwarded-For` is not trusted, so behind a proxy without `API_KEY` all clients look like the proxy: set `API_KEY` or limit at the proxy.
- Graceful shutdown on SIGINT/SIGTERM: in-flight requests drain for `SHUTDOWN_TIMEOUT` (25 s, fits Kubernetes' 30 s default), then are cancelled, and only then are models unloaded.
- HTTP timeouts (header 10 s, read 30 s, idle 2 min), 4 MiB body cap, per-request deadline (`REQUEST_TIMEOUT`), in-flight cap (`-queue`, `529` + `Retry-After`).
- A panic in the model layer fails that request with `500`; the process keeps running.
- Bearer auth with constant-time compare. Startup logs a warning when `API_KEY` is unset.
- Access log has metadata only (method, path, status, ms, request id). Request content is logged only at `LOG_LEVEL=debug`.
- Colored logs on a terminal (level, HTTP status by class, errors in red); plain `key=value` text when piped, so log files stay clean.
- One log format: everything, including Kronk's download/load messages, goes through `slog` on stderr.
- Offline start works when libs and models are already in `~/.kronk/`: tested in a network-less namespace, Kronk logs `no network available, using current version` and serves normally.

Deploy checklist:
- Set `API_KEY`, `DOCS=false`, keep `LOG_LEVEL=info`, set `RATE_LIMIT` if clients are not trusted.
- Startup can take minutes on first run (downloads). Use a generous startup probe, and pre-populate `~/.kronk/` for fast or air-gapped starts.
- If you raise `SHUTDOWN_TIMEOUT`, raise the container grace period above it.
- Size RAM/VRAM for every loaded model (see [GPU](gpu.md) memory notes), or load one with `MODEL=`.
- `CGO_ENABLED=1`; the binary is dynamic and loads llama.cpp from `~/.kronk/libraries/` at runtime.

Not built in: tracing, and request-latency histograms (only sum and count).
