<!-- markdownlint-disable MD041 -->
<div align="center">

<pre>
  mmmm  mmmmm  mmmm    mmmm          mmmm    m               #    "
 m"  "m   #    #   "m m"  "m        #"   " mm#mm  m   m   mmm#  mmm     mmm
 #    #   #    #    # #    #        "#mmm    #    #   #  #" "#    #    #" "#
 #    #   #    #    # #    #            "#   #    #   #  #   #    #    #   #
  #mm#  mm#mm  #mmm"   #mm#         "mmm#"   "mm  "mm"#  "#m##  mm#mm  "#m#"
</pre>

  <h1>
  Self-Hosted TypeSafe System One API, on Local Models
  </h1>

[Quickstart](#quickstart) | [Docs](#documentation) | [API](docs/api.md) | [GPU](docs/gpu.md) | [Production](docs/production.md)

[![license](https://img.shields.io/github/license/Djancyp/oido-systemone)](./LICENSE)
[![go version](https://img.shields.io/github/go-mod/go-version/Djancyp/oido-systemone)](./go.mod)
[![last commit](https://img.shields.io/github/last-commit/Djancyp/oido-systemone)](https://github.com/Djancyp/oido-systemone/commits/main)

</div>

**oido-systemone** is a local, drop-in server for TypeSafe's **System One** API (`POST /v1/systemone`). It runs a GGUF model (MiniCPM5-2B or Qwen3.5-4B) in-process through [Kronk](https://github.com/ardanlabs/kronk) and llama.cpp, so answers never leave your machine. Every answer is one forward pass and one token: a softmax over option-letter logprobs. No text is generated.

- One Go binary. No Docker, no external API
  - GPU (CUDA, ROCm, Vulkan, Metal) picked automatically, CPU as fallback
- Yes/no (`noul`), `choice` and `score` questions, mixed in one request
- Pick the model per request, or use the `jev-latest` alias so TypeSafe SDKs work unchanged
- Swagger UI at `/docs`
- Production basics built in: API key, TLS, rate limiting, `/healthz`, Prometheus `/metrics`, graceful shutdown
- Port of `directScore()` from [TheoLeeCJ/Semif](https://github.com/TheoLeeCJ/Semif)

## Quickstart

> Needs Go 1.27+, a C toolchain (`CGO_ENABLED=1`) and internet on first run: the llama.cpp libs and models (about 1.5 GB and 2.7 GB) download into `~/.kronk/`. See [Development](docs/development.md).

Clone, build and start the server:

```shell
git clone git@github.com:Djancyp/oido-systemone.git
cd oido-systemone
go build -o oido-systemone . && ./oido-systemone
```

On start it prints the logo and where it runs, then a `listening` log line: that means it is ready.

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

Ask it something:

```shell
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
      }
    }
  }' | jq
```

Or open [http://localhost:8080/docs](http://localhost:8080/docs) and use "Try it out".

Load only one model to save memory, or force the CPU:

```shell
MODEL=minicpm5-2b ./oido-systemone
KRONK_PROCESSOR=cpu ./oido-systemone
```

For a real deployment set `API_KEY` and `DOCS=false`, and read [Production](docs/production.md).

## Documentation

- [**API**](docs/api.md): Request and response format, question types, errors, Swagger, how scoring works, known limits
- [**Configuration**](docs/configuration.md): Models and every flag and environment variable
- [**GPU**](docs/gpu.md): Backend selection, overrides, memory notes
- [**Production**](docs/production.md): Health, metrics, TLS, rate limits, shutdown, deploy checklist
- [**Development**](docs/development.md): Requirements, live reload, tests, code layout

## Support

[Open an issue](https://github.com/Djancyp/oido-systemone/issues/new) for bugs and feature requests.

## Contributing

Contributions are welcome. Before opening a pull request, run:

```shell
go vet ./... && go test -race ./...
```

## License

[MIT](LICENSE). The models are separate works with their own licenses: check the model cards for [MiniCPM5-2B](https://huggingface.co/openbmb/MiniCPM5-2B-GGUF) and [Qwen3.5-4B](https://huggingface.co/unsloth/Qwen3.5-4B-GGUF) before use.
