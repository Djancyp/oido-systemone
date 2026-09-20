# oido-systemone

Local, drop-in server for TypeSafe's **System One** API (`POST /v1/systemone`).
Runs a local GGUF model (MiniCPM5-2B or Qwen3.5-4B, Q4_K_M) in-process via [Kronk](https://github.com/ardanlabs/kronk) (llama.cpp).
No Docker, no external API: a single Go binary.

Every answer is **one forward pass, one token, softmax over option-letter logprobs**. No text is
generated. Port of `directScore()` from [TheoLeeCJ/Semif](https://github.com/TheoLeeCJ/Semif) `webgpu-demo/worker.js`.

## Requirements

- Go 1.27+ (see `go.mod`)
- `CGO_ENABLED=1` (default on Linux) and a C toolchain
- Internet on first run: downloads the llama.cpp libs and the chosen model (MiniCPM5-2B ~1.5 GB, Qwen3.5-4B ~2.7 GB; both by default) into `~/.kronk/`
- ~16k-token context window per slot costs RAM; lower `-ctx` on small machines
- Optional: [`air`](https://github.com/air-verse/air) for live reload (`go install github.com/air-verse/air@latest`)

## Run

```sh
go run .                    # listens on :8080, loads both models
MODEL=qwen3.5-4b go run .   # load only Qwen3.5-4B
air                      # rebuild + restart on change (see .air.toml)
go build -o oido-systemone . && ./oido-systemone -addr :9000 -slots 2
```

On start it prints the logo and where it runs (stderr), then the `listening` log line, which means it is ready:

```
  mmmm  mmmmm  mmmm    mmmm          mmmm    m               #    "
 m"  "m   #    #   "m m"  "m        #"   " mm#mm  m   m   mmm#  mmm     mmm
 #    #   #    #    # #    #        "#mmm    #    #   #  #" "#    #    #" "#
 #    #   #    #    # #    #            "#   #    #   #  #   #    #    #   #
  #mm#  mm#mm  #mmm"   #mm#         "mmm#"   "mm  "mm"#  "#m##  mm#mm  "#m#"

  System One API, local  ·  pid 1420271

  listen   http://localhost:8080  ·  http://myhost:8080  (all interfaces)
  docs     http://localhost:8080/docs
  models   oido-rlhf-minicpm5-2b (default), oido-rlhf-qwen3.5-4b
  runs on  vulkan  ·  go1.27.0  ·  linux/amd64  ·  host myhost
  auth     OFF (no API_KEY)
```

`runs on` shows the backend in use (`cpu`, `vulkan`, `cuda`, ...). `BANNER=false` turns the block off.

## Models

| `MODEL` | Source (pinned revision) | Size | Reported id |
|---------|--------------------------|------|-------------|
| `minicpm5-2b` (default) | `openbmb/MiniCPM5-2B-GGUF` Q4_K_M, the revision SemIf tested | ~1.5 GB | `oido-rlhf-minicpm5-2b` |
| `qwen3.5-4b` | `unsloth/Qwen3.5-4B-GGUF` Q4_K_M | ~2.7 GB | `oido-rlhf-qwen3.5-4b` |

All listed models load at startup and stay in RAM (weights + `-ctx` KV cache each), so a
single-model box should set `MODEL=minicpm5-2b`. Each model has its own `-slots` limit. Add a preset
in the `presets` map in `main.go`. Thinking is disabled per request (`enable_thinking: false`), else
the model opens with a reasoning block instead of an option letter.

```sh
curl -s localhost:8080/v1/systemone -H 'Content-Type: application/json' \
  -d '{"model":"oido-rlhf-qwen3.5-4b","state":"I was charged twice.","questions":{"billing":{"type":"noul","instructions":"Is this about billing?"}}}'
```

## Swagger (dev)

| URL | What |
|-----|------|
| `http://localhost:8080/docs` | Swagger UI ("Try it out", Authorize for Bearer key) |
| `http://localhost:8080/openapi.json` | OpenAPI 3.1 spec |

Swagger UI is loaded from the jsDelivr CDN, so `/docs` needs internet. The spec is embedded from
`openapi.json` (TypeSafe's published spec, `/v1/models` removed because it is not implemented).
`/docs` and `/openapi.json` are always on and never require the API key.

## Configuration

Flag wins over env; env wins over default.

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-model` | `MODEL` | `minicpm5-2b,qwen3.5-4b` | Comma-separated presets to load. First one answers `jev-latest` / `jev-preview` |
| `-addr` | `ADDR` | `:8080` | Listen address |
| `-slots` | `SLOTS` | `1` | Parallel forward passes. >1 re-prefills state per slot, slower on CPU |
| `-ctx` | `CONTEXT_WINDOW` | `16384` | Context window per slot, tokens |
| `-queue` | `MAX_QUEUE` | `64` | Max in-flight requests before answering `529` |
| | `API_KEY` | empty | Bearer key. Empty = no auth (use only on a trusted network) |
| | `RATE_LIMIT` | `0` (off) | Requests per second per client (API key, else remote IP). Over it: `429` + `Retry-After` |
| | `RATE_BURST` | `RATE_LIMIT` | Burst size per client |
| | `TLS_CERT`, `TLS_KEY` | empty | PEM files. Both set = serve HTTPS directly |
| | `SHUTDOWN_TIMEOUT` | `25s` | How long in-flight requests may drain on SIGTERM before they are cancelled |
| | `REQUEST_TIMEOUT` | `5m` | Whole-request deadline (Go duration, `0` = none). Exceeded: `504` |
| | `LOG_COLOR` | `auto` | ANSI colors in logs and banner. `auto` = on for a terminal, off when piped or `NO_COLOR` is set. `always` (e.g. `docker logs -t` viewers) / `never` |
| | `BANNER` | `true` | `false` hides the startup logo block |
| | `DOCS` | `true` | `false` hides `/docs` and `/openapi.json` |
| | `BOTH_ORDERS` | `true` | Average forward + reversed option order to cancel position bias. `false` = ~2x faster, more bias |
| | `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. **`debug` logs prompts and probabilities (request content): dev only** |

## GPU

No flag needed: at startup Kronk picks the llama.cpp build for the host and downloads it into
`~/.kronk/libraries/`. Preference order: CUDA (NVIDIA), then ROCm (AMD), then Vulkan, then CPU.
All model layers are offloaded to the GPU when one is found.

| Hardware | Needs on the host |
|----------|-------------------|
| NVIDIA | NVIDIA driver with visible CUDA devices. No CUDA devices visible: falls back to Vulkan or CPU |
| AMD | ROCm (`rocminfo` lists the GPU), or Vulkan via Mesa RADV (`vulkaninfo --summary` lists it; Debian/Ubuntu package `vulkan-tools`) |
| Intel / other | Vulkan driver (`vulkaninfo --summary` lists the GPU) |
| Apple Silicon | Nothing, Metal is automatic |
| Windows | CUDA, Vulkan or ROCm, same rules as Linux |

Check what was picked in the startup log:

```
select-host-runtime: preferred[vulkan] selected[vulkan] ...
download-libraries: ... processor[vulkan]
```

Override the choice with `KRONK_PROCESSOR` (`cpu`, `cuda`, `rocm`, `vulkan`, `metal`):

```sh
KRONK_PROCESSOR=cpu go run .       # force CPU
KRONK_PROCESSOR=vulkan go run .    # force Vulkan, e.g. when ROCm is installed but broken
```

`KRONK_LIB_PATH` points at your own llama.cpp build instead of the download. `KRONK_ARCH` and
`KRONK_OS` exist too, but are only needed when cross-selecting a build.

Memory notes:
- Every loaded model needs its weights plus a `-ctx` KV cache in GPU memory (VRAM). Both default models together need several GB; set `MODEL=minicpm5-2b` or lower `-ctx` on a small card.
- Integrated GPUs (e.g. AMD Radeon 780M) share system RAM, so "VRAM" is whatever the driver lets the GPU map.
- This server has no partial-offload option. It is all layers on the GPU or `KRONK_PROCESSOR=cpu`.

Tested here: AMD Radeon 780M (iGPU) on Linux picked Vulkan automatically; `KRONK_PROCESSOR=cpu` switched it to the CPU build. CUDA, ROCm and Metal are not tested here.

## Production

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
- Size RAM/VRAM for every loaded model (see GPU memory notes), or load one with `MODEL=`.
- `CGO_ENABLED=1`; the binary is dynamic and loads llama.cpp from `~/.kronk/libraries/` at runtime.

Not built in: tracing, and request-latency histograms (only sum and count).

## API

### `POST /v1/systemone`

Body: `{ "model", "state", "questions" }`

- `model`: picks the model. `oido-rlhf-minicpm5-2b`, `oido-rlhf-qwen3.5-4b`, or `jev-latest` / `jev-preview` for the default (first in `MODEL`). The response `model` field says which one answered. Unknown or not-loaded id: `422` listing valid ones
- `state`: string, object or array. The content every question refers to
- `questions`: object of `name -> question`, 1 to 64 questions, answered in parallel

Question types (`type` field):

| type | Fields | Answer |
|------|--------|--------|
| `noul` (yes/no) | `instructions`, optional `criteria: {true, false}` | `{type, noul}` probability of yes, 0..1 |
| `choice` | `instructions`, `criteria: {name: description, ...}` (1 to 255 options) | `{type, choice, probabilities, confidence}` |
| `score` | `instructions`, `criteria: [level0, level1, ...]` (1 to 20 levels) | `{type, score, legend, probabilities, confidence}`, score is the probability-weighted level |

`instructions` and criteria values may be strings, objects or arrays. Choice option order = key
order in the request (it fixes the letters the model sees).

Response: `{ "model", "answers": {name: answer}, "usage": {input_tokens, output_tokens} }`.
Every response carries an `x-typesafe-request-id` header.

### Example

```sh
curl -s localhost:8080/v1/systemone \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "jev-latest",
    "state": {"subject": "Duplicate charge", "message": "I was charged twice. Please help."},
    "questions": {
      "billing": {"type": "noul", "instructions": "Is this message about billing?"},
      "tone": {
        "type": "choice",
        "instructions": "What is the tone of this message?",
        "criteria": {"angry": "Hostile or furious", "neutral": "Calm", "worried": "Anxious"}
      },
      "urgency": {
        "type": "score",
        "instructions": "How urgent is this message?",
        "criteria": ["Not urgent", "Somewhat urgent", "Very urgent"]
      }
    }
  }' | jq
```

With `API_KEY` set add `-H "Authorization: Bearer $API_KEY"`.

### Errors

| Status | When |
|--------|------|
| `403` | `API_KEY` set, no `Authorization: Bearer` header |
| `401` | Wrong key |
| `413` | Body over 4 MiB |
| `422` | Validation (FastAPI-style `{"detail": [{loc, msg, type}]}`). Also input too long for the context window |
| `429` | Client over `RATE_LIMIT` |
| `504` | Request passed `REQUEST_TIMEOUT`, or one forward pass passed 2 min |
| `529` | More than `-queue` requests in flight. `Retry-After: 1` |
| `500` | Model failure |

Auth and server errors use `{"detail": {"error_type", "message"}}`.

## How scoring works

1. State goes in the system message, so all questions on one state share a prefilled KV prefix.
2. Options are labelled `A`, `B`, ... The model's first-token top-20 logprobs are read; `A` and ` A` masses are summed; softmax gives probabilities.
3. Unless `BOTH_ORDERS=false`, each question is also run with reversed option order and averaged.
4. Choices with more than 20 options use two stages: pick a group of 16, then pick within the top 3 groups. Probabilities multiply, so they still sum to 1.
5. `confidence` rescales the top probability so uniform = 0, certain = 1.

## Known limits

- Kronk has no `logit_bias`. An option letter outside the top-20 gets the 20th logprob: an upper bound, its probability is overstated, never understated.
- State size is limited by a byte heuristic (3 bytes/token, 3/4 of the window). Past it: `422 too_long`.
- `-slots` above 1 does not help much on CPU.
- `/v1/models` from TypeSafe's API is not implemented.

## Tests

```sh
go vet ./... && go test -race ./...
```

`api_test.go` checks responses against the JSON schemas in `openapi.json` (`santhosh-tekuri/jsonschema`).
`docs_test.go` checks the Swagger routes. Tests use a fake scorer: no model needed.

## Layout

| File | Purpose |
|------|---------|
| `main.go` | Flags, model install/load, Kronk scorer, HTTP server, request log |
| `api.go` | `/v1/systemone`: auth, validation, prompt, scoring, answers |
| `logcolor.go` | Colored slog handler (terminal only) |
| `docs.go` | `/docs` + `/openapi.json` |
| `metrics.go` | `/metrics` Prometheus exporter |
| `ratelimit.go` | Per-client token-bucket limiter |
| `openapi.json` | TypeSafe's published spec, also used by tests |
| `.air.toml` | Live reload config (`build/` output, gitignored) |
