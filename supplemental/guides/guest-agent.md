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

## 监控页面（probe-hub）

probe-hub 自带一个 Beszel 风格的监控页（`/`，embed 在二进制里）。hub 每 `PROBE_HUB_INTERVAL`（默认 10s）对在线探针发 `metrics` RPC，探针只回累计计数器与瞬时值，速率由 hub 算；历史写 PostgreSQL，按 `date_bin` 降采样出曲线。1.2 以前的探针没有 `metrics`，hub 退回 `health` 兜底。

配置放 `probe-hub.env`（当前目录或二进制旁，已 gitignore）：

| 变量 | 说明 |
|------|------|
| `PROBE_HUB_LISTEN` | 局域网测试用 `0.0.0.0:8766` |
| `PROBE_HUB_DSN` | `postgres://probe_hub:***@127.0.0.1:5432/probe_hub?sslmode=disable`；不设则只有实时视图、无历史 |
| `GUEST_TOKEN` | 探针接入令牌 |
| `PROBE_HUB_ADMIN_TOKEN` | 设置后页面与 `/v1/probe/sessions*`、`/v1/probe/monitor/*` 需 Bearer；`/v1/probe/connect` 与一次性 WS 票不受影响 |
| `PROBE_HUB_INTERVAL` / `PROBE_HUB_RETENTION` | 采集间隔 / 样本保留（默认 `10s` / `168h`），事件保留 30 天 |

表：`probe_systems`（主机、标签、最新样本 jsonb，主键 `device_id`）、`probe_metrics`（`(device_id, ts)` 主键的时序样本）、`probe_events`（事件）。启动时自动建表，外键 `ON UPDATE CASCADE`，主机换键时历史跟着走。旧版以 `bios_uuid` 为主键的表不再自动迁移，需要清表重建。

监控 API（hub 独有）：

- `GET /v1/probe/monitor/config` — 公开，告诉前端是否需要登录
- `GET /v1/probe/monitor/systems` — 全部主机 + 最新样本
- `GET|PATCH|DELETE /v1/probe/monitor/systems/{id}` — 详情含最近 50 条事件；PATCH `{name?, tags?}`；DELETE 仅离线主机
- `GET /v1/probe/monitor/systems/{id}/events?category=lifecycle|change|audit&limit=100` — 事件（limit ≤ 500）
- `GET /v1/probe/monitor/systems/{id}/series?range=1h|6h|24h|7d|30d` — 列式曲线数据

`{id}` 是设备 ID；只有一台主机报这个 BIOS UUID 时也可以直接传 BIOS UUID。

### 设备 ID（指纹）

agent ≥ 1.8.0 在 Hello 里带 `deviceId` = `sha256("pd1|os|系统安装ID|硬件UUID|主网卡MAC")` 前 10 字节（20 位 hex），以及三项各自的短摘要 `device: {mid, hw, mac}`（不上送原始 machine-id）。

| 输入 | Linux | Windows | macOS |
|------|-------|---------|-------|
| 系统安装 ID | `/etc/machine-id` | `MachineGuid` | `IOPlatformUUID` |
| 硬件 UUID | `/sys/class/dmi/id/product_uuid` | SMBIOS（占位值视为空） | `IOPlatformUUID` |
| 主网卡 MAC | 非回环、非本地管理、非桥接/隧道/宿主机虚拟网卡中最小的 MAC；Guest 自己的 VMware vNIC 算真网卡 | 同左 | 同左 |

不纳入 CPU / 磁盘 / 主机名：克隆机上完全相同，调 vCPU、vMotion、改名又会变。ESXi 克隆会重新生成 MAC，所以 BIOS UUID 和 machine-id 都撞了也能区分。`GUEST_DEVICE_ID` 可手动覆盖。

hub 侧：

- 会话按设备 ID 建立。同一 ID 已有会话且仍回 ping → 新连接被拒（Hello 回 `device_conflict`，记"指纹冲突"事件，10 分钟一条）；不回 ping 的半开连接直接替换
- 旧 agent（无 `deviceId`）继续用 BIOS UUID 作 ID
- 升级到 1.8 时，旧的 BIOS UUID 记录会迁移到新指纹；之后若只变了一项（比如换网卡），三项里两项仍对得上的离线记录会被继承。都记"标识迁移"事件。候选不唯一时不迁移
- BIOS UUID 被多台在线 agent 共用时，按 BIOS UUID 寻址返回 `409 guest_unbound`，按设备 ID 仍可用

### 事件

| 分类 | kind |
|------|------|
| `lifecycle` | `online` `offline`（含在线时长；hub 重启补记）`upgrade` `reboot`（启动时间前移 > 2 分钟）`rekey` `conflict` |
| `change` | `remote`（来源 IP）`hostname` `os` `kernel` `cpu` `memory`（> 2%）`disk`（挂载点增删、容量 > 2%）`nic`（带 IPv4 的非容器网卡增删、IP/MAC 变化） |
| `audit` | `term.open/close`（含时长）`display.open/close` `exec`（命令行 + 退出码）`power.*` `proc.*` `service.*` `file.upload/download/mkdir/rename/delete/trash` `trash.restore/purge/empty` `meta`（改名称/标签） |

每条事件都带 `ip`（当时探针的来源 IP）和 `version`（当时的 agent 版本）两个基础属性；审计事件另有 `actor`，是操作者 IP（有 `X-Forwarded-For` 取第一个）。只读操作（列目录、进程列表、文件预览 `preview=1`）和在到达 agent 前就被拒的请求（参数错误、缺 confirm、离线）不记；agent 侧失败记为 `level: warn`。

物理 Windows 机器的 SMBIOS UUID 若是占位值（全 0 / 全 F / `03000200-0400-0500-0006-000700080009`），探针改用注册表 `MachineGuid`，避免局域网多台机器撞 ID。

## 生产 REST

前缀 `/v1/probe/vms/{id}`。`GET /v1/vms` 只加 `guestAgent.online`，不向全部探针拉健康。旧路径 `/v1/vms/{id}/guest*`、`/v1/guest/*`、`/ws/guest-*` 已去掉。

- `GET /v1/probe/vms/{id}` — 在线摘要，不采集
- `GET .../health` — 当场 RPC
- `GET|POST .../procs` — 列表；`start|stop|kill` 要 `confirm`
- `GET|POST .../services` — 列表；`start|stop|restart` 要 `confirm`
- `POST .../exec` — `{command, args?, cwd?, shell, timeoutSec}`
- `POST .../term` → `/v1/probe/ws/term?id=&cols=&rows=`（2 分钟一次性）。每张票一个独立 PTY，同一台最多 8 个并发会话（监控页终端的多标签页）；agent 在 Hello 里声明 `caps: ["term.multi"]`（≥1.4.0），旧 agent 只支持单会话，新开会顶掉旧的
- `POST .../power` — `shutdown|reboot`，要 `confirm`。没有 `on`
- `GET|POST .../display` — GET 说明；POST 出 `/v1/probe/ws/display` 票。只读 JPEG ~2fps、最长边 1920、质量 88，**无 input**。无交互会话 `409 no_user_session`，不回退宿主机截图。Windows 服务在 Session 0 里看不到用户桌面，画面要在用户会话里跑探针（或另做用户态助手）
- `GET .../files?path=` — 列表；空 path 为暂存目录
- `GET .../files/content?path=` — 下载，上限 512MB，SHA-256
- `PUT .../files?path=` — 上传；覆盖要 `confirm`。相对路径落在暂存目录（Windows `C:\ProgramData\probe-agent\tmp`）

离线：`409 guest_offline`。bios 撞车：`409 guest_unbound`。

MCP：`esxi_guest_get` / `health` / `procs` / `services` / `exec` / `power` / `display` / `files`。

## 探针分发

```bash
make probe-agent-dist      # 全平台：windows/darwin/linux × amd64/arm64
make probe-agent-windows   # 只打 Windows
# 或：scripts/probe-agent-dist.sh linux/amd64 windows/amd64
```

产物按版本、平台隔离（版本取 `cmd/probe-agent/main.go` 的 `agentVersion`），升级时整目录替换、旧版本可并存回滚：

```
dist/probe-agent/
├── probe-agent.env                         # 模板（GUEST_CONTROL_URL、GUEST_TOKEN），自行维护，勿提交
├── latest -> 1.3.2
└── 1.3.2/
    ├── probe-agent-1.3.2-windows-amd64/    # probe-agent.exe + probe-agent.env（CRLF）
    ├── probe-agent-1.3.2-windows-amd64.zip # 解压即得上面的同名目录
    ├── probe-agent-1.3.2-darwin-arm64/     # probe-agent + probe-agent.env
    └── …
```

模板路径可用 `AGENT_ENV=path/to/env` 覆盖；不存在时包里不带 env。可执行文件旁的 `probe-agent.env` 会被自动加载。Windows 上替换版本前先停掉旧进程/服务，否则 exe 被占用会复制失败。安装：

```
probe-agent.exe service install
```

打进 `RV0000_BASE`。控制面 `LISTEN` 必须是 Guest **能出站到达** 的地址（常见 `PG-IKUAI-LAN` 上的内网 IP），不要用 `127.0.0.1`。

身份：Guest SMBIOS UUID = ESXi `config.uuid` / `biosUuid`，vm-server 用它把 VM 对到探针；本机可用 `GUEST_BIOS_UUID` 覆盖。probe-hub 内部按设备 ID（见上文）区分主机。
