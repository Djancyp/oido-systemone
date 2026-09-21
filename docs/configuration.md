# Configuration

## Models

| `MODEL` | Source (pinned revision) | Size | Reported id |
|---------|--------------------------|------|-------------|
| `minicpm5-2b` (default) | `openbmb/MiniCPM5-2B-GGUF` Q4_K_M, the revision SemIf tested | ~1.5 GB | `oido-rlhf-minicpm5-2b` |
| `qwen3.5-4b` | `unsloth/Qwen3.5-4B-GGUF` Q4_K_M | ~2.7 GB | `oido-rlhf-qwen3.5-4b` |
| `qwen3-4b` (opt-in) | `unsloth/Qwen3-4B-GGUF` Q4_K_M | ~2.5 GB | `oido-rlhf-qwen3-4b` |

`qwen3-4b` is not in the default list, to keep the default memory footprint: add it with
`MODEL=minicpm5-2b,qwen3.5-4b,qwen3-4b` (or `MODEL=qwen3-4b` alone). The first name in the list answers `jev-latest`.

All listed models load at startup and stay in RAM (weights + `-ctx` KV cache each), so a
single-model box should set `MODEL=minicpm5-2b`. Each model has its own `-slots` limit. Add a preset
in the `presets` map in `main.go`. Thinking is disabled per request (`enable_thinking: false`), else
the model opens with a reasoning block instead of an option letter.

```sh
curl -s localhost:8080/v1/systemone -H 'Content-Type: application/json' \
  -d '{"model":"oido-rlhf-qwen3.5-4b","state":"I was charged twice.","questions":{"billing":{"type":"noul","instructions":"Is this about billing?"}}}'
```

## Flags and environment

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
| | `SELF_CHECK` | `true` | Ask each model one obvious question at startup and exit if it fails or picks wrong (bad chat template, thinking on, no logprobs). `false` skips |
| | `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. **`debug` logs prompts and probabilities (request content): dev only** |
