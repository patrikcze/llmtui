# Embedded GGUF inference on Linux with NVIDIA CUDA

This is the detailed companion to [embedded.md](embedded.md) for running the
llmtui `embedded` provider on a Linux host with NVIDIA GPUs, including GPUs
passed through to a virtual machine. It covers building the exact pinned
llama.cpp revision with CUDA and shared libraries, pointing llmtui at it,
multi-GPU behaviour, and the failures encountered while validating this path.

> **Support status.** `llmtui runtime install` does **not** ship a CUDA
> runtime for any platform (see [What is packaged](#what-is-packaged)). Linux
> CUDA requires a **manually built** llama.cpp runtime selected through
> `library_path` / `YZMA_LIB`. The procedure below was **manually validated
> once** in a lab (see [Validation record](#validation-record)); it is not
> CI-tested and is not an NVIDIA/VMware-certified configuration. Treat version
> numbers here as a working reference, not minimum requirements.

## Contents

- [What is packaged vs. what you build](#what-is-packaged)
- [Reference environment (validated once)](#validation-record)
- [1. VMware ESXi passthrough preparation](#1-vmware-esxi-passthrough-preparation)
- [2. NVIDIA driver on the Linux guest](#2-nvidia-driver-on-the-linux-guest)
- [3. CUDA compiler toolkit](#3-cuda-compiler-toolkit)
- [4. Build the pinned llama.cpp runtime](#4-build-the-pinned-llamacpp-runtime)
- [5. Install and fix dynamic linking](#5-install-and-fix-dynamic-linking)
- [6. Configure llmtui](#6-configure-llmtui)
- [7. Multiple GPUs](#7-multiple-gpus)
- [8. Staged validation](#8-staged-validation)
- [9. Troubleshooting](#9-troubleshooting)
- [10. Headless / SSH image limitation](#10-headless--ssh-image-limitation)
- [11. Licensing notes](#11-licensing-notes)
- [12. Storage and operational notes](#12-storage-and-operational-notes)

<a id="what-is-packaged"></a>
## What is packaged vs. what you build

| Path | What it gives you | CUDA? |
| --- | --- | --- |
| Self-contained release archive (`lib/llmtui/runtime`) | CPU runtime for the platform | No |
| `llmtui runtime install` | The pinned CPU runtime, downloaded and hash-verified into the managed user-data directory | No |
| `llmtui runtime install --backend vulkan` | The pinned **Vulkan** pack (Linux amd64/arm64, Windows amd64 only) | No — Vulkan, not CUDA |
| `llmtui runtime install --backend cuda` | Nothing — **fails deliberately**; upstream publishes no pinned Linux CUDA asset | — |
| A llama.cpp runtime you compile with `-DGGML_CUDA=ON`, selected via `library_path` | CUDA offload across the visible NVIDIA GPUs | Yes |

`gpu_layers: -1` asks the runtime to offload every layer, but it can only use
a CUDA backend if the **loaded runtime directory actually contains a working
`libggml-cuda.so`**. With the packaged CPU runtime, `gpu_layers: -1` still runs
on the CPU.

`library_path` (and the `YZMA_LIB` environment variable, lower precedence) is a
**trusted administrator override**. Unlike a project-installed runtime it is
*not* checked against llmtui's embedded manifest — llmtui probes that the
expected `libllama` / `libggml*` files are present and warns on a version-stamp
mismatch, but it does not hash them. Point it only at a directory you built or
trust. See [security.md](security.md).

<a id="validation-record"></a>
## Reference environment (validated once)

The embedded provider was run successfully with a self-built CUDA runtime in
the following lab environment. **These are the versions that worked together,
not requirements.** Choose versions compatible with your OS, driver, CUDA
toolkit and the pinned llama.cpp revision.

| Component | Validated value |
| --- | --- |
| Hypervisor | VMware ESXi, GPUs assigned with VMDirectPath I/O (raw passthrough), no NVIDIA vGPU profile, no vGPU Manager |
| Guest OS | Rocky Linux 10.x, x86_64, EFI firmware, all guest memory reserved |
| GPU | NVIDIA A16 (Ampere, compute capability 8.6), ~15,356 MiB per GPU processor; **four** processors passed through |
| NVIDIA KMD driver | 610.57.04 (open DKMS kernel module) |
| CUDA user-mode / toolkit | 13.3 (compiler `nvcc` 13.3.73); host GCC 14.3.1 |
| llama.cpp | the tag pinned by the installed llmtui (`internal/runtime/pin.json`) |
| Model | `gpt-oss-20b` GGUF, `Q4_K_S` |

Observed: the llmtui process appeared on every selected GPU in `nvidia-smi`
with model memory divided across devices. An interactive `gpt-oss-20b` run on
two A16 processors produced roughly 34 tokens/second — an uncontrolled
observation, **not a benchmark or a guarantee**.

An NVIDIA A16 board carries **four independent 16 GB GPU processors**. Assigning
several of them to one VM yields several **distinct CUDA devices**, not one
large device: four processors provide roughly 60 GiB of *aggregate* usable
VRAM, not a contiguous 64 GB allocation. A tensor or buffer that cannot be
split still has to fit on a single processor.

<a id="1-vmware-esxi-passthrough-preparation"></a>
## 1. VMware ESXi passthrough preparation

This is a summary, not a vSphere manual. Consult Broadcom/VMware documentation
for your version.

**Host (ESXi):**

- Enable **VT-d / AMD IOMMU** (and **Above 4G Decoding** where the server
  firmware requires it) in the server BIOS/UEFI.
- Mark each GPU PCI function for passthrough (Host → Manage → Hardware → PCI
  Devices → Toggle passthrough). Reboot the host if prompted.
- ESXi itself needs **no NVIDIA driver** for raw passthrough. Never install
  ordinary Linux NVIDIA packages on ESXi. An **NVIDIA vGPU Manager** VIB is a
  different product and is required only for NVIDIA vGPU (not for this
  compute-only DirectPath setup).

**VM:**

- Power the VM off before adding PCI devices.
- Add each GPU function as its own **PCI device / DirectPath I/O** entry.
- Use **EFI** firmware. Secure Boot can stay enabled, but the in-guest NVIDIA
  kernel module then needs its signing key enrolled — see
  [step 2](#2-nvidia-driver-on-the-linux-guest).
- **Reserve all guest memory** (DirectPath requires it).
- For more than one GPU function, enable 64-bit MMIO in the VM's advanced
  configuration:

  ```text
  pciPassthru.use64bitMMIO = TRUE
  pciPassthru.64bitMMIOSizeGB = 128
  ```

  This value reserves **guest physical address space** for the GPU BARs. It
  does **not** consume that much VM RAM or GPU VRAM. Size it from what the VM
  firmware asks for and round **up** to the next power of two: in the reference
  environment two GPUs fit in 64 GB, while four GPUs reported a requirement of
  ~64.125 GiB and therefore needed 128 GB.

- DirectPath constraints apply: the GPU functions are exclusive to that VM,
  and snapshots, vMotion and Fault Tolerance are restricted or unavailable.
  Do not mix vGPU and passthrough on the same physical GPU.

Inside the guest, confirm the devices are visible before installing anything:

```bash
lspci -Dnnk -d 10de:
```

<a id="2-nvidia-driver-on-the-linux-guest"></a>
## 2. NVIDIA driver on the Linux guest

The **normal Linux NVIDIA driver** is installed **inside the guest** (not on
ESXi). `nvidia-smi` ships with that driver.

### Prerequisites (Rocky Linux 10 / RHEL 10 family)

```bash
sudo dnf install -y dnf-plugins-core kernel-devel-matched kernel-headers \
  gcc gcc-c++ make dkms elfutils-libelf-devel
sudo dnf config-manager --set-enabled crb
sudo dnf install -y epel-release
```

### NVIDIA repository and driver

Use NVIDIA's official CUDA repository for your exact Rocky/RHEL major version.
**Verify the current URL and package names against NVIDIA's documentation**
([driver install guide][nv-driver-guide], [CUDA repo install][nv-cuda-repo]) —
they change between releases. For a RHEL/Rocky 10 host the pattern is:

```bash
sudo dnf config-manager --add-repo \
  https://developer.download.nvidia.com/compute/cuda/repos/rhel10/x86_64/cuda-rhel10.repo
sudo dnf clean expire-cache
```

The reference environment used the **open** kernel module via DKMS. On current
repos that is the `nvidia-open` module stream / package set; confirm the name
with `dnf module list nvidia-driver` or `dnf search nvidia` before installing,
then, for a compute-only host, install the driver without the desktop/X
packages, e.g.:

```bash
sudo dnf module install -y nvidia-driver:open-dkms      # name may differ — verify first
```

### Secure Boot

If Secure Boot is enabled, an unsigned DKMS module is refused at boot:

```text
Loading of module with unavailable key is rejected
```

Two supported fixes — pick one according to your security policy:

1. **Disable Secure Boot** (keep EFI). Acceptable for an isolated lab when
   policy permits; do not assume it is acceptable in production.
2. **Keep Secure Boot, enroll the DKMS key (MOK).** DKMS generates a key on
   first build; enroll it and reboot to complete enrollment in the firmware
   prompt:

   ```bash
   sudo mokutil --import /var/lib/dkms/mok.pub     # path may be /var/lib/nvidia/ ... — check dkms output
   # set a one-time password, reboot, choose "Enroll MOK" in the blue screen
   sudo mokutil --list-enrolled | grep -i dkms
   ```

### Verify

```bash
nvidia-smi -L          # lists each GPU as a distinct device
nvidia-smi             # driver + CUDA (UMD) version, per-GPU memory
lsmod | grep nvidia
dkms status
```

`nvidia-smi -L` should list one line per passed-through GPU processor.

<a id="3-cuda-compiler-toolkit"></a>
## 3. CUDA compiler toolkit

`nvidia-smi` reporting a "CUDA Version" only means the **driver** supports up to
that CUDA runtime. It does **not** mean the CUDA **compiler** is installed.
Building llama.cpp with CUDA needs `nvcc`:

```bash
command -v nvcc || echo "nvcc not found"
nvcc --version
```

Install the **smallest** compiler/development set that satisfies the build,
not the full `cuda-toolkit` metapackage — that pulls in Nsight and other
tooling (several GB). The relevant pieces are the CUDA compiler and the CUDA
runtime/driver development headers, e.g. `cuda-nvcc-<ver>`,
`cuda-cudart-devel-<ver>`, `cuda-crt-<ver>` (names vary by CUDA release —
check `dnf search cuda- | grep -Ei 'nvcc|cudart|crt'`).

Select a specific toolkit when several are installed. The reference host had
`/usr/local/cuda-13.2` and `/usr/local/cuda-13.3` and chose 13.3:

```bash
export CUDA_HOME=/usr/local/cuda-13.3        # substitute your installed version
export PATH="$CUDA_HOME/bin:$PATH"
export CUDACXX="$CUDA_HOME/bin/nvcc"
export LD_LIBRARY_PATH="$CUDA_HOME/lib64${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
```

Making this permanent (`/etc/profile.d/cuda.sh`) is optional. Keep the CUDA
toolkit version compatible with both the installed driver and the host GCC.

<a id="4-build-the-pinned-llamacpp-runtime"></a>
## 4. Build the pinned llama.cpp runtime

**Build the exact revision llmtui pins — never `master`.** yzma and llama.cpp
share a narrow compatible window; a mismatched runtime fails at load with
symbol/ABI errors.

```bash
# The authoritative values live in internal/runtime/pin.json:
#   llama_tag  (e.g. b10549)      -> PIN below
#   llama_commit                  -> exact commit for that tag
#   compatible_range.min/max      -> the only builds yzma accepts
PIN=b10549          # <-- read this from internal/runtime/pin.json of YOUR llmtui

git clone https://github.com/ggml-org/llama.cpp.git /opt/build/llama.cpp-$PIN
cd /opt/build/llama.cpp-$PIN
git checkout "$PIN"
git submodule update --init --recursive
```

`llmtui version` prints the llmtui build; the matching pin is in that build's
`internal/runtime/pin.json`. `llmtui doctor` prints the tag llmtui expects
(`runtime <tag>: …`).

### Recommended: minimal embedded runtime

Builds the shared libraries llmtui loads and nothing else. Avoids the
unified-application/tools targets entirely (see the linking note below).

```bash
cmake -S . -B build-cuda \
  -DCMAKE_BUILD_TYPE=Release \
  -DBUILD_SHARED_LIBS=ON \
  -DGGML_CUDA=ON \
  -DGGML_NATIVE=ON \
  -DGGML_CUDA_FA_ALL_QUANTS=ON \
  -DLLAMA_BUILD_TESTS=OFF \
  -DLLAMA_BUILD_EXAMPLES=OFF \
  -DLLAMA_BUILD_TOOLS=OFF \
  -DLLAMA_BUILD_SERVER=OFF \
  -DLLAMA_BUILD_APP=OFF \
  -DLLAMA_CURL=OFF

cmake --build build-cuda -j"$(nproc)"
cmake --install build-cuda --prefix /opt/llmtui/llama-$PIN-cuda
```

Notes:

- `GGML_NATIVE=ON` tunes for this exact host CPU. Use `-DGGML_NATIVE=OFF`
  (optionally with `-DCMAKE_CUDA_ARCHITECTURES="86"` for the A16, or your
  card's compute capability) if the runtime must run on other CPUs/GPUs.
- If CMake cannot find the compiler, add
  `-DCMAKE_CUDA_COMPILER="$CUDACXX"`.
- `LLAMA_CURL` may not exist as an option on every revision; the `-D` for an
  unknown option is harmless.
- **Vision:** add `-DLLAMA_BUILD_MTMD=ON` to also produce `libmtmd.so`
  (needed only if you configure `mmproj_path`). If `libmtmd.so` is still
  absent after installing, also drop `-DLLAMA_BUILD_TOOLS=OFF`.

### Optional: full diagnostic build

Adds `llama-cli`, `llama-bench` and `llama-server` for isolating problems
outside llmtui. Keep the application/server/CLI toggles **consistent** —
enabling `LLAMA_BUILD_APP` while disabling the server/CLI produced a link
failure during validation:

```text
/usr/bin/ld: cannot find -lllama-server-impl
/usr/bin/ld: cannot find -lllama-cli-impl
```

So for a diagnostic build, enable them together (the standalone defaults):

```bash
cmake -S . -B build-cuda-full \
  -DCMAKE_BUILD_TYPE=Release \
  -DBUILD_SHARED_LIBS=ON \
  -DGGML_CUDA=ON \
  -DGGML_NATIVE=ON \
  -DGGML_CUDA_FA_ALL_QUANTS=ON \
  -DLLAMA_BUILD_MTMD=ON \
  -DLLAMA_CURL=OFF \
  -DLLAMA_BUILD_TESTS=OFF

cmake --build build-cuda-full -j"$(nproc)"
cmake --install build-cuda-full --prefix /opt/llmtui/llama-$PIN-cuda-full
```

The validated full build installed:

```text
libllama.so   libggml.so   libggml-base.so   libggml-cpu.so   libggml-cuda.so
libmtmd.so    llama-cli     llama-bench       llama-server
```

`libmtmd.so` is only consulted when a projector (`mmproj_path`) is configured;
text-only inference does not need it.

<a id="5-install-and-fix-dynamic-linking"></a>
## 5. Install and fix dynamic linking

CMake may install libraries into `lib` **or** `lib64` depending on the distro
and toolchain. The reference host used `lib64`. Discover the real directory:

```bash
find /opt/llmtui/llama-$PIN-cuda \
  \( -name 'libllama.so*' -o -name 'libggml-cuda.so*' \) -print
```

llmtui loads `libllama.so` from the directory you configure, but the operating
system's dynamic linker then has to resolve its siblings (`libggml.so`,
`libggml-base.so`, `libggml-cpu.so`, `libggml-cuda.so`). A freshly installed
tree is usually not in the linker cache, which surfaces as:

```text
libggml-cpu.so.0: cannot open shared object file: No such file or directory
```

Register the directory permanently:

```bash
echo '/opt/llmtui/llama-<PIN>-cuda/lib64' | \
  sudo tee /etc/ld.so.conf.d/llmtui-llama-<PIN>.conf     # use your real PIN and lib/lib64
sudo ldconfig
```

If the CUDA runtime libraries themselves are not already on the default search
path, register the selected CUDA `lib64` the same way:

```bash
echo '/usr/local/cuda-13.3/lib64' | \
  sudo tee /etc/ld.so.conf.d/cuda-13.3.conf
sudo ldconfig
```

Verify — the `not found` greps should print nothing:

```bash
ldconfig -p | grep -E 'libllama|libggml-cuda|libggml-cpu'
ldd /opt/llmtui/llama-<PIN>-cuda/lib64/libllama.so     | grep 'not found'
ldd /opt/llmtui/llama-<PIN>-cuda/lib64/libggml-cuda.so | grep 'not found'
```

A temporary `LD_LIBRARY_PATH=/opt/llmtui/llama-<PIN>-cuda/lib64` works for a
one-off test, but the `ld.so.conf.d` entry is the durable fix.

<a id="6-configure-llmtui"></a>
## 6. Configure llmtui

Text-only NVIDIA provider (`~/.config/llmtui/config.yaml`):

```yaml
providers:
  embedded_cuda:
    type: embedded
    model_path: "/models/gguf/model.gguf"
    library_path: "/opt/llmtui/llama-<PIN>-cuda/lib64"   # your real PIN and lib/lib64
    context_size: 8192
    gpu_layers: -1
    threads: 8
    batch_size: 512
    kv_cache_type: q8_0
    flash_attention: auto
    tool_format: auto
```

- Replace `<PIN>` and confirm `lib` vs `lib64`.
- `context_size: 8192` is a conservative starting point for validation, not a
  recommendation. Raise it after measuring VRAM headroom with `nvidia-smi`.
- `kv_cache_type: q8_0` roughly halves KV-cache memory for a small quality
  cost. It **requires flash attention**, so keep `flash_attention: auto` or
  `on` — `q8_0` with `flash_attention: off` is rejected before load.
- `tool_format: auto` is almost always right; set an explicit grammar only if
  model auto-detection misses.
- Per-model sampling/temperature belongs in the provider's `sampling:` block
  or a `model_profiles:` entry (see [embedded.md](embedded.md)).

Vision provider — only after confirming your build produced `libmtmd.so` and
you have a projector that matches the **exact** model family/revision:

```yaml
providers:
  embedded_vision_cuda:
    type: embedded
    model_path: "/models/gguf/model.gguf"
    mmproj_path: "/models/gguf/mmproj-model.gguf"        # must match model.gguf exactly
    library_path: "/opt/llmtui/llama-<PIN>-cuda/lib64"
    context_size: 8192
    gpu_layers: -1
    swa_full: false
    kv_cache_type: q8_0
    flash_attention: auto
    tool_format: auto
```

Start it:

```bash
llmtui doctor                                   # shows the resolved runtime tier + directory
llmtui chat --provider embedded_cuda
```

Changing `library_path` takes effect on the next start — llama.cpp is
initialised once per process and the runtime directory cannot be swapped while
running.

<a id="7-multiple-gpus"></a>
## 7. Multiple GPUs

**What llmtui exposes:** only `gpu_layers` (`-1` = offload all layers, mapped
internally to "every layer"; `0` = CPU only; a positive integer = that many
layers). llmtui does **not** expose `tensor_split`, `split_mode`, `main_gpu`,
or explicit device selection.

**What that means:**

- With a CUDA-enabled runtime, llama.cpp enumerates every visible CUDA device
  and applies its **own default** multi-GPU layer split. llmtui does not
  override it.
- Restrict which GPUs are used at launch time with the standard NVIDIA
  environment variable:

  ```bash
  CUDA_VISIBLE_DEVICES=0,1 llmtui chat --provider embedded_cuda
  ```

- VRAM is **per GPU**. Aggregate capacity across four A16 processors (~60 GiB)
  does **not** behave like one contiguous 64 GB device; an indivisible buffer
  or a single large layer must still fit on one processor.
- A model small enough to fit on one GPU may load entirely on that GPU and
  never demonstrate a balanced split.
- If the runtime logs a warning that a multi-GPU peer/communication library
  (e.g. NCCL) is absent, treat it as a **performance** note unless your build
  of the pinned revision proves otherwise — it is not automatically fatal.
- Always confirm real placement with `nvidia-smi` rather than assuming:

  ```bash
  watch -n 1 nvidia-smi
  nvidia-smi dmon           # per-GPU utilisation / power / memory, one line per sample
  ```

Inside the TUI, `/debug` shows a `native backends` line
(`N registered, M devices`) once a model is loaded — `M` is the number of
GGML backend devices the runtime actually registered (CPU + each CUDA GPU).

<a id="8-staged-validation"></a>
## 8. Staged validation

Work outward from the hardware so a failure localises itself.

1. **PCI** — `lspci -Dnnk -d 10de:` lists every GPU function with
   `Kernel driver in use: nvidia`.
2. **Driver** — `nvidia-smi -L` and `nvidia-smi` succeed and show every GPU.
3. **Compiler** — `nvcc --version` reports the expected CUDA version.
4. **llama.cpp CUDA build** — with the optional full build:

   ```bash
   /opt/llmtui/llama-<PIN>-cuda-full/bin/llama-cli --list-devices
   ```

   (see `llama-cli --help` for the current flag name) should list each CUDA
   device. Then run a tiny GGUF through it and watch `nvidia-smi`.
5. **Dynamic libraries** — the `ldd … | grep 'not found'` checks from
   [step 5](#5-install-and-fix-dynamic-linking) print nothing.
6. **llmtui resolution** — `llmtui doctor` shows the embedded provider
   resolving to your `library_path` directory.
7. **llmtui inference** — `llmtui chat --provider embedded_cuda`, send one
   short prompt, confirm tokens stream.
8. **GPU placement** — `watch -n 1 nvidia-smi` shows the `llmtui` process on
   the expected GPU(s) with model memory allocated.
9. **Scale up** — increase `context_size`, then model size, one change at a
   time, re-checking VRAM after each.

For repeatable numbers use `llama-bench` from the full build (verify its flags
with `llama-bench --help` at your pinned revision). Report
**prompt-processing** (`pp`) and **token-generation** (`tg`) separately — they
are different operations with different bottlenecks.

<a id="9-troubleshooting"></a>
## 9. Troubleshooting

| Symptom | Likely cause | Resolution |
| --- | --- | --- |
| `No CMAKE_CUDA_COMPILER could be found` | `nvcc` not installed or not on `PATH` | Install the CUDA compiler package; `export PATH="$CUDA_HOME/bin:$PATH"` or pass `-DCMAKE_CUDA_COMPILER="$CUDACXX"` |
| `Loading of module with unavailable key is rejected` | Secure Boot rejected the DKMS module | Disable Secure Boot where policy permits, or enroll the DKMS MOK with `mokutil` and reboot |
| `cannot find -lllama-server-impl` / `-lllama-cli-impl` at link | Inconsistent `LLAMA_BUILD_APP` / `LLAMA_BUILD_SERVER` / CLI toggles | Use the minimal build (app + tools + server all OFF) or the full build (all ON); do not mix |
| `libggml-cpu.so.0: cannot open shared object file` | Sibling libraries not in the linker cache | Add the install `lib`/`lib64` to `/etc/ld.so.conf.d/`, run `ldconfig`, re-check with `ldd` |
| VM firmware cannot allocate PCI MMIO / VM won't power on with GPUs | 64-bit MMIO window too small | Raise `pciPassthru.64bitMMIOSizeGB` to cover the firmware's reported requirement, rounded up |
| Inference runs but only CPU is busy | CPU runtime loaded, `libggml-cuda.so` missing/unloadable, or `gpu_layers: 0` | Check `library_path` points at the CUDA build, `libggml-cuda.so` is present and `ldd`-clean, `gpu_layers` is `-1`, and `/debug` shows CUDA devices |
| Only one GPU is used | Model fits on one GPU, `CUDA_VISIBLE_DEVICES` restricts it, or llama.cpp's default split placed it there | Inspect `CUDA_VISIBLE_DEVICES`, `llama-cli --list-devices`, and `nvidia-smi`; try a larger model/context |
| CUDA backend loads, model then OOMs | Weights + KV cache + compute buffers + projector exceed a single GPU's free VRAM | Lower `context_size`/`batch_size`, set `kv_cache_type: q8_0`, use a smaller/more-quantised GGUF, drop the projector while isolating |
| `NCCL not found` (or similar) warning | Optional multi-GPU comms library absent | Treat as a performance note unless proven fatal for your pinned revision |
| Vision init fails | `libmtmd.so` missing from the runtime dir, or projector/model mismatch | Rebuild with `-DLLAMA_BUILD_MTMD=ON`; use a projector published with that exact model |
| Symbol / ABI / `undefined symbol` errors at load | llama.cpp revision outside yzma's compatible range | Rebuild the exact `compatible_range` tag from `pin.json`; never mix versions |

`llmtui` quotes the decisive lines of llama.cpp's own log on a load or
context-init failure — read the error text first.

<a id="10-headless--ssh-image-limitation"></a>
## 10. Headless / SSH image limitation

`Ctrl+V` in the TUI reads an image from the clipboard **of the machine where
llmtui runs**. When llmtui runs over SSH on a headless server:

- It cannot reach the image on your local (Mac/Windows) clipboard — plain SSH
  forwards keyboard text, not binary clipboard image data.
- Installing `wl-paste` or `xclip` alone does not help: there is no graphical
  clipboard without a running display server. The error names `wl-paste` /
  `xclip`, but the underlying cause is usually no `DISPLAY` / `WAYLAND_DISPLAY`.

llmtui has **no file-path image attachment command** today (a `/attach`-style
command does not exist). Attaching an image by path is a possible future
enhancement, not a current feature.

Workaround for vision on a headless GPU host: run a llama.cpp `llama-server`
(from the full build) bound to loopback, forward it over SSH, and run llmtui
**locally** as an OpenAI-compatible client so it can use your local clipboard:

```bash
# on the GPU host
/opt/llmtui/llama-<PIN>-cuda-full/bin/llama-server \
  -m /models/gguf/model.gguf --mmproj /models/gguf/mmproj-model.gguf \
  --host 127.0.0.1 --port 8080 -ngl 999

# on your workstation
ssh -N -L 8080:127.0.0.1:8080 user@gpu-host.example.com
llmtui chat --provider openai_compatible --base-url http://127.0.0.1:8080/v1 --model model.gguf
```

Never bind an unauthenticated model API to a routable/corporate address. Keep
it on `127.0.0.1` and reach it through the SSH tunnel.

<a id="11-licensing-notes"></a>
## 11. Licensing notes

Not legal advice — confirm entitlement with NVIDIA, your reseller, or your
licensing team for any production use.

- **llama.cpp** is MIT-licensed; it is not vendored or compiled by llmtui.
- **yzma** is permissively licensed (see [`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md)).
- **NVIDIA driver, CUDA runtime and CUDA Toolkit** are official NVIDIA
  software but are **not** all open source; they are governed by NVIDIA's
  licence terms ([CUDA Toolkit EULA][nv-eula]).
- **Raw VMDirectPath I/O passthrough** is technically distinct from **NVIDIA
  vGPU**. Compute-only passthrough as validated here does not use or require
  the NVIDIA vGPU Manager on ESXi.
- **NVIDIA vGPU** (vGPU Manager, vGPU/vWS profiles, GPU partitioning/sharing,
  virtual workstation features, NVIDIA AI Enterprise) can introduce **separate
  licensing requirements** and a licence server. The absence of licence-server
  enforcement in a passthrough setup is **not** proof of entitlement for vGPU
  software.
- VMware/Broadcom licensing is separate again.

[nv-driver-guide]: https://docs.nvidia.com/datacenter/tesla/driver-installation-guide/
[nv-cuda-repo]: https://docs.nvidia.com/cuda/cuda-installation-guide-linux/
[nv-eula]: https://docs.nvidia.com/cuda/eula/index.html

<a id="12-storage-and-operational-notes"></a>
## 12. Storage and operational notes

- CUDA packages and the DNF cache can consume several GB under `/var`; a
  source build adds more. Do not let `/var` fill mid-install
  (`sudo dnf clean packages` clears cached RPMs after a failed transaction).
- A build under a small `/home` can fail for lack of space even when `/` has
  room — they are usually separate filesystems.
- Keep models on a dedicated, suitably sized filesystem (e.g. `/models`).
- Use **versioned** install prefixes (`/opt/llmtui/llama-<PIN>-cuda`) so a pin
  bump does not overwrite a known-good runtime, and keep some free LVM
  capacity for the next build.

## See also

- [embedded.md](embedded.md) — the canonical embedded-provider overview,
  configuration key reference, vision/tools/reasoning behaviour, and general
  troubleshooting.
- [architecture/embedded-local-inference.md](architecture/embedded-local-inference.md)
  — the design ADR and the 2026-09-04 Linux CUDA / multi-GPU validation
  addendum.
- [security.md](security.md) — the native-code trust boundary and the
  `library_path` override.
