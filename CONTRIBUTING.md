# Contributing

Thanks for helping. Bug reports, fixes, docs and new model presets are all welcome.

## Before you start

- **Bugs and small fixes:** open a pull request directly.
- **Bigger changes** (new endpoint, new question type, new dependency): [open an issue](https://github.com/Djancyp/oido-systemone/issues/new) first so we agree on the shape before you write code.
- **Security problems:** do not open a public issue. Use GitHub's private vulnerability reporting (Security tab → Report a vulnerability).

## Set up

You need Go 1.27+ and a C toolchain (`CGO_ENABLED=1`). No GPU and no model download are needed to develop or test.

```shell
git clone git@github.com:<you>/oido-systemone.git
cd oido-systemone
go test -race ./...
```

To run the real server you need internet on first start (llama.cpp libs and models, about 4 GB into `~/.kronk/`). See [Development](docs/development.md) for live reload with `air` and the code layout.

## The one rule: keep the wire contract

This server is a drop-in for TypeSafe's System One API. Request and response shapes, status codes, error bodies and the `x-typesafe-request-id` header must keep matching TypeSafe's published spec (`openapi.json`). `TestContractShapes` and the other tests in `api_test.go` check responses against that spec, so a change that breaks compatibility fails the tests. New paths (like `/healthz` and `/metrics`) are fine; changes to `/v1/systemone` are not, unless the spec changes.

## Check your change

CI runs exactly this. Run it locally first:

```shell
test -z "$(gofmt -l .)"                                   # formatting
go vet ./...
go test -race -count=1 ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

Tests use a fake scorer, so they run in about a second with no model. Changing scoring, prompts or model presets? Also smoke-test against a real model, since the fake cannot catch a bad prompt:

```shell
MODEL=minicpm5-2b go run .
curl -s localhost:8080/v1/systemone -H 'Content-Type: application/json' \
  -d '{"model":"jev-latest","state":"I was charged twice.","questions":{"billing":{"type":"noul","instructions":"Is this about billing?"}}}'
```

## Code style

- Match the surrounding code: naming, comment density, plain standard library first. Dependencies are kept few on purpose; ask in an issue before adding one.
- Add or update a test for behaviour you change. Use the fake scorer in `api_test.go`; one focused test beats a big suite.
- A deliberate shortcut with a known ceiling gets a `ponytail:` comment naming the ceiling and the upgrade path, for example `// ponytail: byte heuristic; exact fix = tokenize before scoring`.
- Errors that reach clients keep TypeSafe's shape: FastAPI-style `{"detail": [...]}` for validation, `{"detail": {"error_type", "message"}}` for the rest.
- Never log request content above `LOG_LEVEL=debug`. The access log stays metadata only.

## Common changes

**Add a model preset**

1. Add an entry to `presets` in `main.go`: a key, the id clients will send (`oido-rlhf-<name>`), and a Hugging Face URL **pinned to a commit hash**, not `main`.
2. Confirm the model answers with a single option letter as its first token (chat templates that open with a reasoning block need `enable_thinking: false`, already set) by running the smoke test above against it.
3. Add a row to the models table in [docs/configuration.md](docs/configuration.md) and mention its size.

**Add or change an environment variable or flag**

Update the table in [docs/configuration.md](docs/configuration.md) and, if it matters in production, [docs/production.md](docs/production.md). Add a `.env.example` line if the compose file uses it.

**Change the container setup**

`docker build -t oido-systemone .` must still pass (CI builds it). Update [docs/docker.md](docs/docker.md) when you change behaviour, and note what you tested (CPU, Vulkan, CUDA).

## Pull requests

- Branch from `main`, keep the PR to one change, and describe what and why.
- Write commit messages in the imperative, with a short subject line (`Add rate limit per API key`), and a body when the why is not obvious.
- Say what you tested. "Tested on AMD Vulkan" or "CPU only" helps reviewers; hardware we cannot test (CUDA, ROCm, Metal) is called out as untested in the docs, so keep it that way unless you ran it.
- CI must be green before review.

## License

By contributing you agree that your work is released under the [MIT License](LICENSE).
