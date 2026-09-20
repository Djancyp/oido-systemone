# GPU

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
