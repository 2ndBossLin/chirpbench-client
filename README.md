# ChirpBench Local Client

Local benchmark client for **ChirpBench** — a public leaderboard for local LLM inference speed (prefill / decode across 9 context lengths, up to 256k tokens).

The client is a **single static binary** with the web UI embedded. No runtime dependencies: no Go toolchain, no shared libraries, no config files. Drop it in any directory and run it.

This repository is public **for external review**: the measurement logic, the exact upload payload, and the command-execution boundary are all readable here and can be checked against the deployed client.

A standalone methodology brief written for external review (what is measured, how cache contamination is neutralised, known limitations, open questions) is in [METHODOLOGY.md](METHODOLOGY.md).

---

## What it does

- Serves the test UI at `http://127.0.0.1:18099/`
- Reverse-proxies browser requests to a local or LAN inference server (`/__proxy`) — bypasses CORS and flushes every stream chunk so time-to-first-token is measured accurately
- Reads local hardware info and a machine fingerprint (`/__hw`)
- Runs start/stop commands for the "sweep" (parameter-tuning) mode (`/__exec`)
- Uploads finished runs to the leaderboard service

## Repository layout

```
client/            Go source — all client logic
  main.go          HTTP server, embedded page, /__proxy
  hw.go            hardware detection + machine_id fingerprint
  exec.go          start/stop command execution (with safety checks)
  exec_unix.go     child process detachment (Unix)
  exec_windows.go  child process detachment (Windows)
  build.sh         build script (embeds page + assets into the binary)
index.html         test UI (all front-end measurement + upload logic)
chart.umd.min.js   charting library
html2canvas.min.js result screenshots
chirpbench-icon.png / logo.jpg   icons and artwork
```

## Build

Go 1.22+, no CGO required:

```bash
cd client
./build.sh          # current platform only
./build.sh all      # Linux + Windows
```

Outputs land in `client/out/`:

- `chirpbench-client-linux-amd64`
- `chirpbench-client-windows-amd64.exe`

The page, icons and JS libraries are embedded via `//go:embed web`, so the binary needs no external files at runtime.

## Run

```bash
./chirpbench-client                  # http://127.0.0.1:18099/
./chirpbench-client -port 18100      # explicit port
./chirpbench-client -open=false      # do not open a browser
```

If 18099 is taken, the client automatically tries the next free port (up to 20 attempts).

## Internal endpoints

| Path | Purpose |
| --- | --- |
| `/` | embedded test UI |
| `/__proxy?u=<target-url>` | proxy to any http(s) inference server; request bodies are buffered so a reset upstream connection can be replayed |
| `/__hw` | GPU / CPU / RAM / chipset plus machine fingerprint |
| `/__exec` | start a set of start/stop commands |
| `/__exec/status`, `/__exec/log` | command status and log |
| `/__kill` | stop processes started by this client |

## Measurement integrity — the cache-busting prefix is not optional

Inference servers cache prompt prefixes (vLLM, llama.cpp and SGLang all ship this enabled by default). If the client sends byte-identical prompts, the second and later prefills are served from KV cache while the client still counts every token as computed — the reported prefill speed then comes out far too high.

Measured here on one machine, one model, one ladder, 16,384-token length, 2-way concurrency:

- client **without** cache busting: **4,590 tok/s**
- client **with** cache busting: **2,565 tok/s**

That is **+79%** from cache reuse alone, with no hardware change. The effect grows with context length, because every longer prompt contains the shorter one as an exact prefix.

So `index.html` does two things, and both are part of the measurement definition rather than an optimisation:

1. every request — each repeat pass, each concurrency request, each calibration probe — is prefixed with a fresh random `[s xxxxxxxx]` marker;
2. any pass whose prefill time is under 40% of the fastest pass at the same length is discarded as a cache hit.

Removing or weakening either one makes the numbers invalid: they stop measuring prompt-processing speed and start measuring how well the server caches. Leaderboard submissions are cross-checked by independent re-tests (5 different users; average prefill within 5%, average decode within 10%) and by a server-recomputed `result_hash`, so inflated runs are rejected — but a fork that drops the nonce will simply produce numbers that are not comparable with ChirpBench results.

## Measurement methodology

- Each context length is repeated multiple times (3 passes for rated tiers); **the median of a single pass is reported**, not the sum of passes
- Prompt-processing speed prefers the server-reported `timings.prompt_ms`; when absent, client time-to-first-byte is used with a sampled network latency subtracted
- Output is fixed at 512 tokens with `temperature 0` and a fixed corpus, so numbers stay comparable across machines
- Points where the server reports queueing (`queue > 200ms`) are discarded rather than counted as prefill

## What gets uploaded

Benchmark data only — no prompts, no files, no model weights:

`user_code` (internal account key), `model_name`, hardware fields (GPU / CPU / RAM / chipset — editable in the UI), `framework`, `quantization`, `run_command` (launch flags), per-length prefill/decode numbers, `repeat_count`, `concurrency`, `machine_id` (first 32 hex chars of `sha256(machine-id + board serial + product UUID + board model + CPU + GPU + OS)`; raw serials never leave the machine), `tier` / `probe3_pass` / `probe8_pass` (rating verdict), `schema_version`.

The leaderboard server always recomputes `result_hash`; a client-supplied hash is not trusted.

## Command execution boundary

`/__exec` is reachable only from the local client page and requires all of:

- target host is `127.0.0.1`
- the request carries `X-ChirpBench-Exec: 1`
- `Origin` matches the local port

Commands are typed in by the user in the sweep UI. The client generates none and accepts none from the network.

## Known limitations

- Windows and Linux only (macOS lacks fingerprint and process-detach support)
- In a two-machine setup (client on machine A, model on machine B) the detected hardware and fingerprint belong to machine A — hardware fields must be corrected by hand
- Not code-signed; Windows SmartScreen warns on first run

## License

GNU General Public License v3.0 — see [LICENSE](LICENSE).

Why GPLv3: the code is public so anyone can audit the measurement logic, the upload payload and the execution boundary; copyleft also keeps forks open, so nobody can repackage it as a closed-source client to farm the same leaderboard. If you need permissive terms (closed-source redistribution allowed), MIT can be swapped in.

---

# 中文说明

**ChirpBench 本地测速客户端**（赛博斗蛐蛐）：本地大模型推理速度的公开榜单客户端，按 9 个上下文长度档（最高 256k）测 prefill / decode。

单文件静态程序，网页界面编译在内嵌文件系统里，运行时不需要 Go 环境、任何动态库或配置文件，可以放在任意目录。

本仓库公开是为了**接受外部审查**：测量逻辑、上传字段、命令执行边界都能直接对照线上客户端核对。

面向外部审查的方法论说明（测什么、如何消除缓存污染、已知局限、待回答的问题）见 [METHODOLOGY.md](METHODOLOGY.md)。

## 它做什么

- 在 `http://127.0.0.1:18099/` 提供测试页面
- 反代浏览器到本机 / 局域网推理服务（`/__proxy`），绕开 CORS，并逐块 flush 保证首字延迟测得准
- 读取本机硬件信息与机器指纹（`/__hw`）
- 为"多轮调参"执行模型启停命令（`/__exec`）
- 测试完成后上传成绩到榜单服务

## 目录结构

`client/` 是全部 Go 源码（`main.go` 服务与反代、`hw.go` 硬件与指纹、`exec*.go` 命令执行、`build.sh` 构建脚本）；根目录是前端页面 `index.html`、图表库、截图库、图标与素材。

## 构建与运行

需要 Go 1.22+，无需 CGO，`cd client && ./build.sh all` 产出 Linux 与 Windows 两个版本到 `client/out/`。运行时 `./chirpbench-client`（默认 `127.0.0.1:18099`），`-port` 换端口，`-open=false` 不自动开浏览器；端口被占用会自动往后顺延，最多试 20 个。

## 测量可信度：随机前缀不是可选项

推理服务默认都会缓存 prompt 前缀（vLLM、llama.cpp、SGLang 都一样）。如果客户端每一遍发的内容完全相同，第二遍起服务端直接从 KV 缓存取，而客户端仍按全部 token 计算速度——报出来的 prefill 就会虚高。

同一台机器、同一个模型、同一条梯子、16384 档、2 并发实测：

- 不做缓存失效的客户端：**4590 tok/s**
- 做了缓存失效的客户端：**2565 tok/s**

硬件没变，只因为缓存复用就高出 **79%**。而且上下文越长差得越多，因为长 prompt 完整包含短 prompt 的前缀。

因此 `index.html` 做了两件事，它们属于测量口径本身，不是性能优化：

1. 每一个请求——每一遍、每个并发请求、每次校准探针——都带一个新的随机 `[s xxxxxxxx]` 前缀；
2. 同一长度下，某遍 prefill 耗时不到最快那遍 40% 的，判定为缓存命中并剔除。

去掉或削弱任何一条，成绩就失效：那时测的不再是 prefill 速度，而是服务端的缓存命中率。上传的成绩另有独立复测（5 位不同用户，平均 prefill 偏差 5%、平均 decode 偏差 10%）和由服务端重算的 `result_hash` 兜底，虚高的记录进不了榜——但删掉随机前缀的分叉版本，产出的数字与本榜不可比。

## 测量口径

每个长度重复多遍（评级档 3 遍），**展示单遍中位数**而不是多遍求和；prefill 优先取服务端 `timings.prompt_ms`，没有时用首字节时间并扣除网络延迟采样；输出固定 512 token、`temperature 0`、固定语料，保证跨机器可比；服务端上报排队（`queue > 200ms`）的档位直接剔除，不计入 prefill。

## 上传内容

只上传测试数据，不含提示词、文件或模型权重：账号内部键、模型名、硬件字段（可手改）、框架、量化、启动参数、各长度档速度、重复遍数、并发数、机器指纹（`sha256` 前 32 位，原始序列号不出本机）、评级判定与 `schema_version`。服务端始终自己重算 `result_hash`，不信客户端传来的哈希。

## 命令执行边界

`/__exec` 只在本地页面可用，且必须同时满足：目标为 `127.0.0.1`、带 `X-ChirpBench-Exec: 1` 头、`Origin` 与本机端口一致。命令由用户自己填写，客户端不生成、也不从网络接收命令。

## 已知限制

只支持 Windows / Linux；双机测试（客户端在 A 机、模型在 B 机）时硬件与指纹取的是 A 机，需手工改成被测机配置；未做代码签名，Windows 首次运行会有 SmartScreen 提示。

## 许可

GPL-3.0（见 [LICENSE](LICENSE)），衍生版本须继续开源，避免有人拿这份代码封装闭源客户端刷同一个榜单；如需更宽松可改 MIT。
