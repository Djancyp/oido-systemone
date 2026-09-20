# Docker

The image is a glibc runtime (`debian:trixie-slim`) around the Go binary. Kronk downloads llama.cpp and the models at first start, so keep `/home/app/.kronk` on a volume: about 4 GB with both models, and it survives image rebuilds.

```shell
docker build -t oido-systemone .
```

## docker run

CPU, one model (smallest and fastest to start):

```shell
docker run -d --name oido-systemone \
  -p 127.0.0.1:8080:8080 \
  -v oido-kronk:/home/app/.kronk \
  -e MODEL=minicpm5-2b \
  oido-systemone
```

Same with a key, both models and no Swagger, for anything reachable by others:

```shell
docker run -d --name oido-systemone \
  -p 127.0.0.1:8080:8080 \
  -v oido-kronk:/home/app/.kronk \
  -e API_KEY="$(openssl rand -hex 24)" \
  -e DOCS=false -e RATE_LIMIT=10 \
  --stop-timeout 30 \
  oido-systemone
```

AMD or Intel GPU (Vulkan). Pass the render device and its groups (`getent group render video` on the host):

```shell
docker run -d --name oido-systemone \
  -p 127.0.0.1:8080:8080 \
  -v oido-kronk:/home/app/.kronk \
  --device /dev/dri \
  --group-add "$(getent group render | cut -d: -f3)" \
  --group-add "$(getent group video | cut -d: -f3)" \
  -e KRONK_PROCESSOR=vulkan \
  oido-systemone
```

Check `runs on` in the startup banner: `docker logs oido-systemone`. The first start downloads the libs and models, so wait for `listening` (or `docker inspect -f '{{.State.Health.Status}}' oido-systemone` to say `healthy`).

Then:

```shell
curl -s localhost:8080/v1/systemone \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $API_KEY" \
  -d '{"model":"jev-latest","state":"I was charged twice.","questions":{"billing":{"type":"noul","instructions":"Is this about billing?"}}}'
```

NVIDIA (CUDA): needs the [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/) and `--gpus all -e KRONK_PROCESSOR=cuda`. Not tested here, and the Kronk CUDA build may need CUDA runtime libraries this image does not ship.

## Production with Docker Compose

`docker-compose.yml` is the production file: key required, Swagger off, rate limit on, loopback-only port, model volume, 30 s stop grace, capabilities dropped, log rotation, healthcheck with a 10 minute start period.

```shell
cp .env.example .env            # set API_KEY (openssl rand -hex 24), MODEL, PORT
docker compose up -d --build
docker compose logs -f
```

The stack refuses to start without `API_KEY`.

GPU (AMD / Intel, Vulkan): add the override. It maps `/dev/dri`, adds the render and video groups and sets `KRONK_PROCESSOR=vulkan`. Set `RENDER_GID` and `VIDEO_GID` in `.env` if your host differs from `992` and `44`.

```shell
docker compose -f docker-compose.yml -f docker-compose.gpu.yml up -d
```

Day two:

```shell
docker compose ps                                 # health: starting / healthy
curl -s -H "Authorization: Bearer $API_KEY" localhost:8080/metrics
git pull && docker compose up -d --build          # update; the model volume is kept
docker compose down                               # stop; add -v to also delete the models
```

Put a TLS reverse proxy (Caddy, Traefik, nginx) in front and keep `BIND=127.0.0.1`, or set `TLS_CERT` and `TLS_KEY` and mount the files. See [Production](production.md) for what each setting does.

## Notes

- **CPU is the image default** (`KRONK_PROCESSOR=cpu`). Without a GPU device, Mesa still offers a software Vulkan renderer (llvmpipe), so auto-detect would report `vulkan` while running on the CPU, slower than the real CPU build. A GPU is opt-in.
- **Use a separate volume, not your host `~/.kronk`.** Kronk stores absolute paths in its model index (`models/.index.yaml`). Mounting a host cache into the container rewrites those paths to `/home/app/.kronk`, and the host install then fails to find its models until they are put back.
- **Bind mount permissions:** the container user is uid 1000. A named volume just works; for a bind mount run `chown 1000:1000 <dir>`.
- **Tested here:** CPU and AMD Radeon 780M (Vulkan) with `docker run`, and with the compose file plus GPU override, on Docker 29. Auth, `/healthz`, `/metrics`, healthy status and a clean stop (exit 0, about 1 s idle) were checked. CUDA, ROCm and a first-start download inside the volume were not.
