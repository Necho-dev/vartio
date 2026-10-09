# Guest 探针

Guest 没有可达公网 IP。探针**只出站**连控制面，所有命令走这条 WSS 上的反向 RPC。控制面不拨 Guest、不扫内网、不走 VMware Tools。

## 三个入口

| 项目 | 启动 | 用途 |
|------|------|------|
| **probe-hub** | `make probe-hub` / `go run ./cmd/probe-hub` | 探针服务端。不连 ESXi、不读 `.env`。`127.0.0.1:8766` |
| **probe-agent** | `make probe-agent` / `go run ./cmd/probe-agent` | 探针客户端，出站连 hub |
| **vm-server** | `make vm-server` / `go run ./cmd/vm-server` | ESXi 控制面。`0.0.0.0:8765`。也挂同一套 `/v1/probe/*` |

协议同一份：[`internal/guest`](../internal/guest/)。探针 HTTP 全部在 `/v1/probe/*`，不挂在 `/v1/vms` 下。

## 本机调试

```bash
# 终端 1
make probe-hub
# PROBE_HUB_LISTEN=127.0.0.1:8766  GUEST_TOKEN=dev

# 终端 2
cp cmd/probe-agent/agent.env.example probe-agent.env
make probe-agent
```

```bash
curl -sS http://127.0.0.1:8766/v1/probe/sessions
curl -sS http://127.0.0.1:8766/v1/probe/sessions/$BIOS/health
curl -sS -X POST http://127.0.0.1:8766/v1/probe/sessions/$BIOS/exec \
  -H 'Content-Type: application/json' \
  -d '{"command":"hostname"}'
curl -sS http://127.0.0.1:8766/v1/probe/sessions/$BIOS/files
curl -sS -X PUT "http://127.0.0.1:8766/v1/probe/sessions/$BIOS/files?path=note.txt&confirm=true" \
  --data-binary $'hello\n'
curl -sS -X POST http://127.0.0.1:8766/v1/probe/sessions/$BIOS/display
```

Guest 在 ESXi 网段、仍只测探针时：把 `GUEST_DEV_LISTEN` 改成该网段可达地址。不要给 Guest 配入站端口。

## 生产 REST

前缀 `/v1/probe/vms/{id}`。`GET /v1/vms` 只加 `guestAgent.online`，不向全部探针拉健康。旧路径 `/v1/vms/{id}/guest*`、`/v1/guest/*`、`/ws/guest-*` 已去掉。

- `GET /v1/probe/vms/{id}` — 在线摘要，不采集
- `GET .../health` — 当场 RPC
- `GET|POST .../procs` — 列表；`start|stop|kill` 要 `confirm`
- `GET|POST .../services` — 列表；`start|stop|restart` 要 `confirm`
- `POST .../exec` — `{command, args?, cwd?, shell, timeoutSec}`
- `POST .../term` → `/v1/probe/ws/term?id=`（2 分钟一次性）
- `POST .../power` — `shutdown|reboot`，要 `confirm`。没有 `on`
- `GET|POST .../display` — GET 说明；POST 出 `/v1/probe/ws/display` 票。只读 JPEG ~2fps、最长边 1920、质量 88，**无 input**。无交互会话 `409 no_user_session`，不回退宿主机截图。Windows 服务在 Session 0 里看不到用户桌面，画面要在用户会话里跑探针（或另做用户态助手）
- `GET .../files?path=` — 列表；空 path 为暂存目录
- `GET .../files/content?path=` — 下载，上限 512MB，SHA-256
- `PUT .../files?path=` — 上传；覆盖要 `confirm`。相对路径落在暂存目录（Windows `C:\ProgramData\probe-agent\tmp`）

离线：`409 guest_offline`。bios 撞车：`409 guest_unbound`。

MCP：`esxi_guest_get` / `health` / `procs` / `services` / `exec` / `power` / `display` / `files`。

## 探针分发

```bash
make probe-agent-windows
# dist/probe-agent.exe
```

exe 旁放 `probe-agent.env`（`GUEST_CONTROL_URL`、`GUEST_TOKEN`）。安装：

```
probe-agent.exe service install
```

打进 `RV0000_BASE`。控制面 `LISTEN` 必须是 Guest **能出站到达** 的地址（常见 `PG-IKUAI-LAN` 上的内网 IP），不要用 `127.0.0.1`。

身份：Guest SMBIOS UUID = ESXi `config.uuid` / `biosUuid`。本机可用 `GUEST_BIOS_UUID` 覆盖。
