# ChirpBench 本地客户端 / ChirpBench Local Client

ChirpBench（赛博斗蛐蛐）测速榜的本地测试客户端。单文件静态程序，内置网页界面，负责：

- 提供测试页面（浏览器访问 `http://127.0.0.1:18099/`）
- 反代浏览器到本机/局域网推理服务的请求（`/__proxy`，绕开 CORS、逐块 flush 以保证首字延迟测量准确）
- 读取本机硬件信息与机器指纹（`/__hw`）
- 执行"多轮调参"用的模型启停命令（`/__exec`）
- 测试完成后把成绩上传到榜单服务

本项目开源用于**接受外部审查**：测量逻辑、上传字段、命令执行边界都可以直接读代码核对。

## 目录结构

```
client/           Go 源码（全部逻辑）
  main.go         HTTP 服务、静态页面、/__proxy 反代
  hw.go           硬件识别 + machine_id 指纹
  exec.go         启停命令执行（含安全校验）
  exec_unix.go    子进程脱离（Unix）
  exec_windows.go 子进程脱离（Windows）
  build.sh        构建脚本（把页面与资源嵌进二进制）
index.html        测试页面（前端全部逻辑，含测量与上传）
chart.umd.min.js  图表库
html2canvas.min.js 结果截图
chirpbench-icon.png / logo.jpg  图标与素材
```

## 构建

需要 Go 1.22+，无需 CGO：

```bash
cd client
./build.sh          # 只构建当前平台
./build.sh all      # Linux + Windows
```

产物在 `client/out/`：

- `chirpbench-client-linux-amd64`
- `chirpbench-client-windows-amd64.exe`

构建是自包含的：页面、图标、JS 库全部通过 `//go:embed web` 打进二进制，运行时不需要任何外部文件。

## 运行

```bash
./chirpbench-client                  # 默认 http://127.0.0.1:18099/
./chirpbench-client -port 18100      # 指定端口
./chirpbench-client -open=false      # 不自动开浏览器
```

18099 被占用时会自动顺延到后面的空闲端口（最多试 20 个）。

## 内部接口

| 路径 | 作用 |
| --- | --- |
| `/` | 内嵌测试页面 |
| `/__proxy?u=<目标URL>` | 反代到任意 http(s) 推理服务；缓冲请求体以便上游断流时整条重发 |
| `/__hw` | 本机 GPU / CPU / 内存 / 主板 + 机器指纹 |
| `/__exec` | 启动一组启停命令 |
| `/__exec/status`、`/__exec/log` | 命令状态与日志 |
| `/__kill` | 停止本客户端启动的进程 |

## 测量口径

- 每档长度重复多遍（评级测试 3 遍），取单遍中位数展示，避免把多遍求和当成单次耗时
- prefill 优先采用服务端返回的 `timings.prompt_ms`；没有时用客户端首字节时间并扣除网络延迟采样
- 输出固定 512 token、temperature 0、固定语料，保证结果可比
- 排队（`queue > 200ms`）会剔除，避免把并发排队算进 prefill

## 数据上传

只上传测试数据，不上传任何提示词内容或本机文件：

`user_code`（账号内部键）、`model_name`、硬件字段（显卡/CPU/内存/主板，可手改）、`framework`、`quantization`、`run_command`（启动参数）、各长度档的 prefill/decode 数值、`repeat_count`、`concurrency`、`machine_id`（`sha256(主板序列号+machine-id+…)` 前 32 位，不上传原始序列号）、`tier`/`probe3_pass`/`probe8_pass`（评级判定）、`schema_version`。

榜单服务端会始终重算 `result_hash`，客户端传的哈希不被信任。

## 命令执行的安全边界

`/__exec` 只在本地客户端的页面里可用，且同时要求：

- 目标为 `127.0.0.1`
- 请求带自定义头 `X-ChirpBench-Exec: 1`
- `Origin` 必须与本机端口一致

命令由用户自己在"多轮调参"里填写，客户端不做任何命令生成或远程下发。

## 已知限制

- 只支持 Windows / Linux（macOS 未适配指纹与子进程脱离）
- 跨机测试时硬件字段与指纹取的是运行客户端这台机器，需要手工改成被测机配置
- 未做代码签名，Windows 首次运行会有 SmartScreen 提示

## 许可

暂未附加开源许可证，如有需要可另行指定。
