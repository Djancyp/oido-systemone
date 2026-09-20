# syntax=docker/dockerfile:1

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/oido-systemone .

# glibc runtime: Kronk downloads llama.cpp builds at first start and dlopens them.
#   libgomp1, libstdc++6  llama.cpp CPU/Vulkan builds
#   mesa-vulkan-drivers, libvulkan1, vulkan-tools  Vulkan GPUs (AMD, Intel); vulkaninfo is how Kronk detects them
#   ca-certificates, curl  downloads from GitHub and Hugging Face; the healthcheck
FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl libgomp1 libstdc++6 libvulkan1 mesa-vulkan-drivers vulkan-tools \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --uid 1000 app \
 && install -d -o app -g app /home/app/.kronk
COPY --from=build /out/oido-systemone /usr/local/bin/oido-systemone

USER app
# Libs (~100 MB) and models (~4 GB) land in ~/.kronk: mount a volume here.
VOLUME /home/app/.kronk
# CPU by default. Without a GPU device Mesa still offers "vulkan" (llvmpipe, a software
# renderer, slower than the CPU build), so a GPU is opt-in: KRONK_PROCESSOR=vulkan.
ENV ADDR=:8080 KRONK_PROCESSOR=cpu
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10m --retries=3 \
  CMD curl -fsS http://localhost:8080/healthz || exit 1
ENTRYPOINT ["oido-systemone"]
