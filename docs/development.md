# Development

## Requirements

- Go 1.27+ (see `go.mod`)
- `CGO_ENABLED=1` (default on Linux) and a C toolchain
- Internet on first run: downloads the llama.cpp libs and the chosen models into `~/.kronk/`
- Optional: [`air`](https://github.com/air-verse/air) for live reload (`go install github.com/air-verse/air@latest`)

## Run

```sh
go run .                                  # listens on :8080, loads both models
MODEL=qwen3.5-4b go run .                 # load only Qwen3.5-4B
MODEL=minicpm5-2b,qwen3.5-4b,qwen3-4b go run .   # also load Qwen3-4B
air                                       # rebuild + restart on change (see .air.toml)
go build -o oido-systemone . && ./oido-systemone -addr :9000 -slots 2
```

## Tests

```sh
go vet ./... && go test -race ./...
```

`api_test.go` checks responses against the JSON schemas in `openapi.json` (`santhosh-tekuri/jsonschema`).
`docs_test.go` checks the Swagger routes. Tests use a fake scorer: no model needed.

## Layout

| File | Purpose |
|------|---------|
| `README.md`, `docs/` | Readme and reference docs |
| `LICENSE`, `CONTRIBUTING.md` | MIT license, contribution guide |
| `Dockerfile`, `docker-compose.yml`, `docker-compose.gpu.yml`, `.env.example` | Container image and production stack, see [Docker](docker.md) |
| `main.go` | Flags, model install/load, Kronk scorer, HTTP server, request log |
| `api.go` | `/v1/systemone`: auth, validation, prompt, scoring, answers |
| `logcolor.go` | Colored slog handler (terminal only) |
| `docs.go` | `/docs` + `/openapi.json` |
| `metrics.go` | `/metrics` Prometheus exporter |
| `ratelimit.go` | Per-client token-bucket limiter |
| `openapi.json` | TypeSafe's published spec, also used by tests |
| `.air.toml` | Live reload config (`build/` output, gitignored) |
