# Vartio

Guest 没有可达公网 IP。探针**只出站**连控制面，所有命令走这条 WSS 上的反向 RPC。控制面不拨 Guest、不扫内网、不走 VMware Tools。

产品名是 Vartio，二进制是 `vartio` 和 `vartio-agent`。下面这些是已经部署的探针认的契约，没有改：`/v1/probe/*`、Hello 签名 `probe-hub-hello/v1`、发布包签名前缀 `probe-agent|`、请求头 `X-Guest-*`。环境变量新名前缀是 `VARTIO_` / `VARTIO_AGENT_`，原来的 `PROBE_HUB_*` 和 `GUEST_*` 仍然有效。

## 两个入口

| 项目 | 启动 | 用途 |
|------|------|------|
| **vartio** | `make vartio` / `go run ./internal/cmd/hub` | 探针服务端。不连 ESXi、不读 `.env`。`127.0.0.1:8766` |
| **vartio-agent** | `make vartio-agent` / `go run ./internal/cmd/agent` | 探针客户端，出站连 hub |

协议在 [`internal/entities`](../../internal/entities)，服务端在 [`internal/hub`](../../internal/hub)。探针 HTTP 全部在 `/v1/probe/*`。

## 本机调试

```bash
# 终端 1
make probe-hub
# PROBE_HUB_LISTEN=127.0.0.1:8766  GUEST_TOKEN=dev

# 终端 2
cp agent/agent.env.example probe-agent.env
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
| `PROBE_HUB_INTERVAL` / `PROBE_HUB_RETENTION` | 采集间隔 / 原始样本保留（默认 `10s` / `168h`），事件保留 30 天 |
| `PROBE_HUB_RETENTION_5M` / `PROBE_HUB_RETENTION_1H` | 5 分钟 / 1 小时汇总的保留（默认 `336h`（且不短于原始样本）/ `8760h`） |
| `PROBE_HUB_AUTH` / `PROBE_HUB_ENROLL` | 接入认证，见下文「接入认证」 |

表：`probe_systems`（主机、标签、最新样本 jsonb，主键 `device_id`）、`probe_metrics`（`(device_id, ts)` 主键的时序样本）、`probe_events`（事件）。启动时自动建表，外键 `ON UPDATE CASCADE`，主机换键时历史跟着走。旧版以 `bios_uuid` 为主键的表不再自动迁移，需要清表重建。

降采样（hub ≥ 1.18.0）：主机与容器样本另外汇总进 `probe_metrics_5m` / `probe_metrics_1h` 和 `probe_container_metrics_5m` / `_1h`。均值列取平均，峰值列（`cpu_max`、`net_rx_max`、`net_tx_max`）取最大，流量、报文段、错误包取合计，所以从汇总表算出的曲线与直接聚合原始样本一致。hub 每分钟重算最近 70 分钟（幂等 upsert，当前桶随新样本补齐），启动时从上次汇总处补齐（升级后首次启动会把现有原始样本全部汇总一遍）。曲线步长 ≥ 1 小时读小时表，≥ 15 分钟或区间超出原始样本保留读 5 分钟表，步长取汇总粒度的整数倍；其余读原始样本。默认配置下 7 天曲线读 5 分钟表，30 天读小时表，原始样本只需保留 7 天。

监控 API（hub 独有）：

- `GET /v1/probe/monitor/config` — 公开，告诉前端是否需要登录
- `GET /v1/probe/monitor/systems` — 全部主机 + 最新样本
- `GET|PATCH|DELETE /v1/probe/monitor/systems/{id}` — 详情含最近 50 条事件；PATCH `{name?, tags?}`；DELETE 仅离线主机
- `GET /v1/probe/monitor/systems/{id}/events?category=lifecycle|change|audit&limit=100` — 事件（limit ≤ 500）
- `GET /v1/probe/monitor/systems/{id}/series?range=1h|6h|24h|7d|30d` — 列式曲线数据

`{id}` 是设备 ID；只有一台主机报这个 BIOS UUID 时也可以直接传 BIOS UUID。

### 网络指标

网络带宽卡片占满一行，标题旁可下拉指定网卡（默认全部物理网卡；分网卡曲线需要 agent 上报累计收发计数）。吞吐与包速率画在同一张图里：左轴吞吐（实线为桶内平均，虚线为桶内峰值），右轴包速率；底部汇总区间内的平均、P95（按降采样后的桶计算）、峰值、累计流量、平均包速率、错误包与丢包。另有网络连接数（TCP / UDP / 其他套接字堆积，图例给出当前、平均、峰值；需要 agent ≥ 1.15.0）与 TCP 连接状态（ESTABLISHED / TIME_WAIT / LISTEN）两张卡片；重传率照常采集入库，详情页不再单独画图；网卡错误/丢包只在区间内非零时出现。包、错误、TCP 数据需要 agent ≥ 1.14.0，旧样本对应字段为空，相应卡片自动隐藏。

| 指标 | Linux | Windows | macOS |
|------|-------|---------|-------|
| 包 / 错误 / 丢包 | `/proc/net/dev`（gopsutil） | `GetIfEntry2`，单播 + 非单播包 | gopsutil |
| TCP 连接数 | `/proc/net/tcp{,6}` | `GetExtendedTcpTable` | `netstat -an -f inet/inet6` |
| UDP / 其他套接字 | `/proc/net/udp{,6}`、`raw{,6}`、`icmp{,6}` | `GetExtendedUdpTable`；不统计原始套接字 | 同上（`udp*`、`icm*`；`tcp46`/`udp46` 只计一次） |
| 重传率 | `/proc/net/snmp` 的 `RetransSegs / OutSegs` | `GetTcpStatisticsEx` 的 `dwRetransSegs / (dwOutSegs + dwRetransSegs)`（`dwOutSegs` 不含重传） | `nettop` 的 `re-tx / bytes_out`（字节比例；非 root 读不到报文段计数） |

都是系统内核自带的累计计数器。hub 每次采样记下发送量与重传量的增量（`tcp_out_segs` / `tcp_retrans_segs`，series 接口返回桶内合计），需要时按 Σ重传 / Σ发送 加权计算；单次采样的 `retransPct` 在发送不足 200 段时为空。hub 与探针之间的往返延迟（WebSocket ping/pong，精确到 0.01 ms）记入 `latency_ms`，在详情页头部显示，不单独画图。网卡错误增加时记 `net.errors` 告警事件，每台主机每小时最多一条。

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
| `lifecycle` | `online` `offline`（含在线时长；hub 重启补记）`upgrade`（注明远程升级 / 自动更新）`upgrade.failed`（已回滚，warn）`reboot`（启动时间前移 > 2 分钟）`rekey` `conflict` `auth.reject`（凭据缺失或登记关闭，warn，10 分钟一条）`container.start/stop/unhealthy/healthy`（同一次采样合并成一条；stop、unhealthy 为 warn） |
| `change` | `remote`（来源 IP）`hostname` `os` `kernel` `cpu` `memory`（> 2%）`disk`（挂载点增删、容量 > 2%）`nic`（带 IPv4 的非容器网卡增删、IP/MAC 变化） |
| `audit` | `term.open/close`（含时长）`display.open/close` `exec`（命令行 + 退出码）`power.*` `proc.*` `service.*` `file.upload/download/mkdir/rename/delete/trash` `trash.restore/purge/empty` `meta`（改名称/标签）`agent.upgrade`（下发升级，`from → to`）`docker.logs` `docker.exec`（容器名）`credential.reset` |

每条事件都带 `ip`（当时探针的来源 IP）和 `version`（当时的 agent 版本）两个基础属性；审计事件另有 `actor`，是操作者 IP（有 `X-Forwarded-For` 取第一个）。只读操作（列目录、进程列表、文件预览 `preview=1`）和在到达 agent 前就被拒的请求（参数错误、缺 confirm、离线）不记；agent 侧失败记为 `level: warn`。

物理 Windows 机器的 SMBIOS UUID 若是占位值（全 0 / 全 F / `03000200-0400-0500-0006-000700080009`），探针改用注册表 `MachineGuid`，避免局域网多台机器撞 ID。

### 接入认证

hub ≥ 1.18.0 在共享的 `GUEST_TOKEN` 之上加了两层，旧探针不受影响：

- **hub 身份**：hub 首次启动生成 Ed25519 密钥（`probe-hub.key`，0600，`PROBE_HUB_KEY_FILE` 可改路径），公钥打在启动日志里，也可 `GET /v1/probe/hub/key` 查询。探针（≥ 1.18.0）在 Hello 里带随机 `nonce`，hub 回 `HelloOK.sig = Sign("probe-hub-hello/v1\n" + nonce + "\n" + 设备 ID + "\n" + 凭据)`。探针首次连上时记住 hub 公钥（TOFU），之后签名不对、换了公钥或不签名的 hub 一律拒绝并重连。`GUEST_HUB_KEY=<base64>[,<base64>…]` 预置可信公钥（多个用于轮换），`GUEST_HUB_VERIFY=off` 跳过校验
- **设备凭据**：用 `GUEST_TOKEN` 通过认证的新设备会收到一份专属凭据（hub 只存 SHA-256，`probe-hub.agents.json`，`PROBE_HUB_CREDENTIALS_FILE` 可改路径），此时状态为「待确认」。探针下次连接在 `X-Guest-Device` / `X-Guest-Credential` 头里出示后变为「已绑定」。已绑定的设备只能凭自己的凭据接入，单凭令牌冒用会被拒（`credential_required`，记 `auth.reject` 事件）。凭据本身就能通过认证，所以已绑定的探针在轮换 `GUEST_TOKEN` 后照样能连；新令牌只用于新设备登记

探针把公钥和凭据存在 `GUEST_HUB_STATE`；没设时，容器内放 `GUEST_STATE_DIR/hub.json`，否则放 exe 旁的 `probe-agent.hub.json`（目录不可写时放用户配置目录 `probe-agent/hub.json`）。按控制地址分开保存，0600。

| 操作 | 方法 |
|------|------|
| 探针丢了凭据（重装、换目录） | 详情页「接入凭据」点「重置」，或 `POST /v1/probe/sessions/{id}/credential/reset {"confirm":true}`（审计 `credential.reset`）；探针下次连接凭令牌重新登记 |
| 禁止新设备接入 | `PROBE_HUB_ENROLL=off`。没有凭据的旧探针（< 1.18.0）也会被拒，先把探针全部升级 |
| hub 换机 | 连同 `probe-hub.key`、`probe-hub.agents.json` 一起迁移；丢了 key，所有探针会拒绝新 hub，需要删掉探针侧的 hub.json 或设置 `GUEST_HUB_KEY` |
| 关闭 | `PROBE_HUB_AUTH=off`，退回只校验令牌 |

删除主机时同时删除它的凭据。签名能防假冒 hub，但明文 `ws://` 上的主动中间人仍可转发篡改帧，公网请用 `wss://`。

## REST

前缀 `/v1/probe/sessions/{id}`，`{id}` 是设备 ID（唯一时也可传 `biosUuid`）。

- `GET /v1/probe/sessions/{id}` — 在线摘要，不采集
- `GET .../health` — 当场 RPC
- `GET|POST .../procs` — 列表；`start|stop|kill` 要 `confirm`
- `GET|POST .../services` — 列表；`start|stop|restart` 要 `confirm`
- `POST .../exec` — `{command, args?, cwd?, shell, timeoutSec}`
- `POST .../term` → `/v1/probe/ws/term?id=&cols=&rows=`（2 分钟一次性）。每张票一个独立 PTY，同一台最多 8 个并发会话（监控页终端的多标签页）；agent 在 Hello 里声明 `caps: ["term.multi"]`（≥1.4.0），旧 agent 只支持单会话，新开会顶掉旧的
- `POST .../power` — `shutdown|reboot`，要 `confirm`。没有 `on`
- `GET|POST .../display` — GET 说明；POST 出 `/v1/probe/ws/display` 票。只读 JPEG 二进制帧，**无 input**。清晰度（agent ≥ 1.12.0）在 ws URL 上带 `&quality=auto|smooth|hd`：`smooth` 10fps / 最长边 1280 / 质量 60，`hd` 3fps / 2560 / 90，`auto`（默认）在 2/1280/55 … 10/1920/82 五档间按 2 秒窗口的发送耗时自动升降。多显示器（agent ≥ 1.11.0）：连上后 hub 先发文本帧 `{"type":"info","screens":[{index,name,x,y,width,height,primary}],"screen":0,"quality":"auto"}`（0 号恒为主屏）；浏览器发 `{"type":"screen","screen":N}` / `{"type":"quality","quality":"hd"}` 切换，hub 转成 `display.select` / `display.quality` 下发并回同类型消息，失败回 `{"type":"error","message":…}`；`screen` 为 `-1`（agent ≥ 1.18.4）时按各屏桌面坐标拼成一张，这次选择只在当前连接里有效，关闭后再开仍从主屏开始（agent ≥ 1.18.5）。每一路浏览器连接各自截取、各自选屏和清晰度（agent ≥ 1.18.6，同一台最多 4 路）；更早的探针只有一路，后打开的页面会把先打开的那路切走。旧 agent 不发列表，只看主屏。macOS ≥ 14 用 ScreenCaptureKit（运行时 `dlopen`），失败退回 `screencapture`。macOS 按 CoreGraphics 顺序用 `screencapture -D`，Windows 用 `EnumDisplayMonitors` + 每显示器 DPI 感知截取。无交互会话 `409 no_user_session`，不回退宿主机截图。Windows 服务在 Session 0 里看不到用户桌面，画面要在用户会话里跑探针（或另做用户态助手）
- `GET .../files?path=` — 列表；空 path 为暂存目录
- `GET .../files/content?path=` — 下载，上限 512MB，SHA-256
- `PUT .../files?path=` — 上传；覆盖要 `confirm`。相对路径落在暂存目录（Windows `C:\ProgramData\probe-agent\tmp`）

离线：`409 guest_offline`。bios 撞车：`409 guest_unbound`。

## 探针分发

```bash
make probe-agent-dist      # 全平台：windows/darwin/linux × amd64/arm64
make probe-agent-windows   # 只打 Windows
# 或：supplemental/scripts/probe-agent-dist.sh linux/amd64 windows/amd64
```

产物按版本、平台隔离（版本取 `probe.go` 的 `Version`），旧版本可并存回滚。打包后自动签名生成 `manifest.json`，`latest` 指向的版本就是 hub 对外提供的升级目标：

```
dist/probe-agent/
├── probe-agent.env                         # 模板（GUEST_CONTROL_URL、GUEST_TOKEN），自行维护，勿提交
├── latest -> 1.9.0                         # hub 提供的升级目标；改指向即可发布 / 撤回
└── 1.9.0/
    ├── manifest.json                       # 每个平台的 size、sha256、Ed25519 签名
    ├── probe-agent-1.9.0-windows-amd64/    # probe-agent.exe + probe-agent.env（CRLF）
    ├── probe-agent-1.9.0-windows-amd64.zip # 解压即得上面的同名目录
    ├── probe-agent-1.9.0-darwin-arm64/     # probe-agent + probe-agent.env
    └── …
```

模板路径可用 `AGENT_ENV=path/to/env` 覆盖；不存在时包里不带 env。可执行文件旁的 `probe-agent.env` 会被自动加载。Windows 上手动替换版本前先停掉旧进程/服务，否则 exe 被占用会复制失败（≥ 1.9.0 可以远程升级，见下文）。Windows 包是 GUI 子系统（`-H windowsgui`），双击不弹控制台；macOS 包带 cgo（菜单栏图标），只能在 Mac 上打。

## 运行方式

| 方式 | 安装 | 适用 | `runner` |
|------|------|------|----------|
| Windows 托盘（推荐） | `probe-agent.exe install` | 有人登录的桌面；实时画面可用 | `tray` |
| Windows 服务 | 管理员 `probe-agent.exe service install` | 无人登录的服务器；Session 0 看不到桌面，没有实时画面 | `service` |
| macOS 菜单栏（推荐） | `./probe-agent install`（不要 sudo） | 登录用户的 LaunchAgent | `launchd` |
| Linux | systemd unit | 服务器 | `systemd` |
| Docker 容器 | `supplemental/docker/agent/compose.agent.yml`（见下文「Docker」） | 只想用镜像部署的 Linux 主机 | `docker` |
| 终端前台 | 直接运行 | 调试 | `console` |

- **Windows 托盘**：`install` 写 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 的 `ProbeAgent`，并立即在后台启动；`uninstall` 删除该项并结束本机的托盘探针。托盘与服务二选一，装了一种再装另一种会被拒绝。直接双击 exe 也会进入托盘模式，但不会开机自启
- **macOS 菜单栏**：`install` 写 `~/Library/LaunchAgents/probe-agent.plist`（`RunAtLoad`、`KeepAlive`、仅 Aqua 会话）并 `launchctl bootstrap`。进程被结束会被 launchd 拉起，只有 `uninstall`（`bootout`）能停掉。不出现在程序坞。在终端里直接运行也会显示菜单栏图标，日志照常打到终端；root 或纯 SSH 登录时不显示。图标首次出现在系统图标（时钟、控制中心）左侧，避免被刘海挡住；按住 ⌘ 拖动可调整位置，重启和升级后保持（≥ 1.15.2，记录在 `~/Library/Preferences/probe-agent.plist`）
- `install` 时如果 exe 旁还没有 `probe-agent.env`，会把当前环境里的 `GUEST_*` 写进去（0600），因为登录自启拿不到终端里的环境变量
- 同一用户只允许一个实例：Windows 用命名互斥量 `Local\probe-agent`，并检查服务是否在跑；macOS 用 exe 旁的 `probe-agent.lock`（launchd 拉起的实例会等锁，而不是反复退出重启）。重复启动会提示"已在后台运行"后退出
- 菜单内容：连接状态（已连接时长 / 未连接原因 / 升级中）、Hub 地址、版本与设备 ID、打开监控页、查看日志、立即重连。**没有退出**，防止误关监控
- 日志：没有终端可写时（托盘、服务、launchd）写到 exe 旁的 `probe-agent.log`，超过 10MB 轮转为 `.log.1`；exe 目录不可写时落到用户缓存目录。`GUEST_LOG=stderr` 强制写 stderr，其他值视为日志路径
- `GUEST_TRAY=off` 关掉托盘 / 菜单栏图标，Windows 上这时仍然没有控制台，日志写文件

打进 `RV0000_BASE`。控制面 `LISTEN` 必须是 Guest **能出站到达** 的地址（常见 `PG-IKUAI-LAN` 上的内网 IP），不要用 `127.0.0.1`。

身份：Guest SMBIOS UUID 与 ESXi `config.uuid` / `biosUuid` 相同；本机可用 `GUEST_BIOS_UUID` 覆盖。probe-hub 内部按设备 ID（见上文）区分主机。

## Docker

agent ≥ 1.17.0 支持两件事：任何平台上的探针都能监控本机的 Docker / Podman 容器；探针本身也可以跑在容器里。hub 也提供镜像。

### 容器监控

探针直接调 Docker Engine API（不依赖 docker CLI）。引擎地址按顺序找：`DOCKER_HOST`（`unix://` 或不带 TLS 的 `tcp://`）、`/var/run/docker.sock`、`/run/podman/podman.sock`、`$XDG_RUNTIME_DIR/{docker.sock,podman/podman.sock}`、`~/.docker/run`、`~/.orbstack/run`、`~/.colima/default`、`~/.rd` 下的 `docker.sock`。Windows 不支持 named pipe，需要 `DOCKER_HOST=tcp://…`。

| `GUEST_DOCKER` | 能力 | 说明 |
|----------------|------|------|
| `ro`（默认） | `docker` | 容器列表、CPU / 内存 / 网络 / 磁盘 IO / 进程数、日志 |
| `rw` | `docker` `docker.exec` | 另外允许在容器里开终端 |
| `off` | — | 不访问引擎 |

不提供启动、停止、删除。

- 采集：每个采样周期 `GET /containers/json?all=1`，再对运行中的容器并发（8 路）取一次 `stats?stream=false&one-shot=true`，最多 200 个容器、100 次 stats；失败的容器当轮重试一次。Docker < 25 的 `one-shot` 不生效，每次 stats 要等约 1 秒，改为全部并发。agent 上报累计值和引擎的采样时刻，hub 算速率。CPU% 与 `docker stats` 一致（100% 为一个核心）；内存是 `usage − inactive_file`（cgroup v1 用 `total_inactive_file`，再退回 `cache`），不含可回收的页缓存
- 引擎差异（agent ≥ 1.18.0）：Podman 不给主机 CPU 时间，hub 按两次采样的引擎时间戳折算；Podman 列表不带健康状态，取不到时补一次 inspect。Windows 容器的 CPU 单位是 100ns、内存取 `PrivateWorkingSet`。hub 丢弃超出核数 110% 的 CPU 和超过 100 Gbit/s 的网络 / 磁盘速率（计数器回绕、容器重建），该次不画点
- `GUEST_DOCKER_EXCLUDE=buildx_*,*-tmp`：按容器名 glob 排除（逗号分隔），不采集、不列出，日志、终端、详情也拒绝
- 端口：列表接口里的映射合并成 `127.0.0.1:5432→5432/tcp, 8080→80/tcp`（IPv4 / IPv6 同一端口只显示一次，未发布的端口不显示）
- 镜像更新：每个镜像 6 小时查一次仓库，用 `HEAD /v2/<repo>/manifests/<tag>`（匿名 token）的 `Docker-Content-Digest` 对比本地 `RepoDigests`，有新版本时列表显示「可更新」。HEAD 不计入 Docker Hub 拉取限额；本地构建、按 digest 引用、需要登录的私有仓库会跳过。`GUEST_DOCKER_IMAGE_CHECK=off` 关闭
- 详情：能力 `docker.inspect`（agent ≥ 1.18.0）。`GET /v1/probe/sessions/{id}/docker/inspect?container=名称` 返回引擎的 inspect 文档，探针端先删掉 `Config.Env`（常含密码）；页面上点容器的「详情」查看
- 历史：表 `probe_container_metrics`，随 `PROBE_HUB_RETENTION` 清理，另有 5 分钟 / 1 小时汇总（见「监控页面」）。`GET /v1/probe/monitor/systems/{id}/containers/series?range=1h&top=8` 返回平均 CPU、平均内存各自前 N 名的并集（`top` 最大 50）
- 日志：`GET /v1/probe/sessions/{id}/docker/logs?container=名称&tail=500&since=unix` 拉快照（带时间戳，最多约 2MB）；页面上可选时间范围、行数、模糊过滤、下载。追踪走终端票据 `mode=logs`
- 编排：`GET /v1/probe/sessions/{id}/docker/compose?project=名称` 读 `com.docker.compose.project.config_files` 指向的 YAML。探针在容器里时，Docker Desktop（Mac）需要把 `$HOME` 挂进容器才能读到宿主机路径
- 终端：复用终端票据。`POST /v1/probe/sessions/{id}/term` 的请求体可带 `{"mode":"logs"|"exec", "container":"名称或 ID", "tail":500, "since":unix, "shell":"/bin/sh"}`，不带就是主机终端。`exec` 是 `docker exec` 进到容器内部（优先 bash，没有就 sh）。缺 `docker` 能力返回 `409`，exec 缺 `docker.exec` 返回 `403 agent_policy`
- exec 关闭：Docker 在客户端断开后不会结束 TTY exec 里的 shell。每个会话带环境变量 `PROBE_EXEC=<随机值>`，关闭时探针再起一个 exec，找出环境里带这个标记的进程，先 HUP 再 KILL

`docker.sock` 即使以只读方式挂载，也等同于宿主机 root。`ro` 只限制探针自己会调用的接口。

### 探针跑在容器里

```bash
make docker-agent
GUEST_CONTROL_URL=ws://hub:8766/v1/probe/connect GUEST_TOKEN=… \
  docker compose -f supplemental/docker/agent/compose.agent.yml up -d
```

compose 用 `pid: host` 和 `network_mode: host` 看到宿主机的进程、网卡和连接，把宿主机 `/` 只读挂到 `/hostfs`。Docker Desktop（Mac）的 `/` 绑定看不到 virtiofs 里的 `/Users`，所以再把 `$HOME` 只读挂进去，才能读到 Compose 编排文件。容器内的行为：

- 识别：`PROBE_AGENT_RUNNER=docker`（镜像里已设），或存在 `/.dockerenv`、`/run/.containerenv`。`runner` 为 `docker`
- `GUEST_HOST_ROOT=/hostfs`：磁盘用量按 `/hostfs/<挂载点>` 统计；`os-release`、`machine-id`、主机名从 `/hostfs/etc` 读。`GUEST_HOSTNAME` 可以覆盖主机名
- 设备 ID：容器里不用网卡 MAC（不共享宿主机网络时，每次重建容器都会变）。读不到宿主机的 `machine-id` 时，生成一个存在 `GUEST_STATE_DIR`（默认 `/var/lib/probe-agent`，compose 挂了数据卷）
- 终端、命令执行、关机默认在探针容器里运行。`GUEST_HOST_NS=1` 时用 `nsenter -t 1 -m -u -i -n -p` 进入宿主机命名空间，需要 `privileged: true`
- 镜像标签：`probe-agent:<版本>` 和 `:latest` 里的二进制就是该版本，容器内拒绝远程升级。`probe-agent:edge` 打开 `GUEST_UPGRADE` 和 `GUEST_AUTO_UPDATE`，跟着 hub 的签名包走；重建容器后回到 edge 镜像里打包的版本。compose 用 `PROBE_AGENT_TAG=edge PROBE_AGENT_TARGET=edge`
- 宿主机根目录默认是普通绑定挂载，探针启动后才挂上的磁盘看不到；加 `,rslave` 可以看到，但 Docker Desktop 的根目录不是共享挂载，不支持

### hub 镜像

```bash
make docker-hub
GUEST_TOKEN=… PROBE_HUB_ADMIN_TOKEN=… docker compose -f supplemental/docker/hub/compose.hub.yml up -d
```

带一个 PostgreSQL 存历史。工作目录 `/data`（数据卷，hub 签名密钥 `probe-hub.key` 和设备凭据也在这里，重建容器时不要丢），远程升级用的签名产物挂到 `/data/dist/probe-agent`（compose 里有注释示例）。多架构镜像：`make docker-push REGISTRY=…`（buildx，默认 `linux/amd64,linux/arm64`）。agent 镜像打 `agentVersion`、`latest`（两者都是锁定版本的 release）和 `edge`。

## 远程升级

agent ≥ 1.9.0 在 Hello 里声明 `caps: ["upgrade"]` 和 `runner`（见上文「运行方式」）。更旧的版本需要手动更新一次。

两种触发方式，都通过 HTTP 下载同一份签名产物：

- **hub 推送**：监控页详情页「升级到 X」、首页横幅「全部升级」，或下面的 API。hub 发 `agent.upgrade` RPC，agent 下载、校验、自检通过后先回复，1 秒后再替换并重启
- **agent 拉取**：`GUEST_AUTO_UPDATE=on` 时每 `GUEST_UPDATE_INTERVAL`（默认 `6h`，首次在 `min(间隔, 5m)` 的 0.5–1 倍之间随机）查一次 `GET /v1/probe/agent/latest`，有更新就走同一流程

流程：

1. 下载到 `probe-agent.new`，校验大小、SHA-256 和 Ed25519 签名。签名的消息是 `probe-agent|版本|os|arch|size|sha256`，公钥编译进 agent（`internal/release/release.pub`，可放多把用于轮换）。任何一步失败都不动原文件，hub 收到 `502 upgrade_failed`
2. 自检：执行 `probe-agent.new version`，输出的版本和平台必须对得上
3. 替换：`probe-agent` → `probe-agent.old`，`.new` → `probe-agent`，写 `probe-agent.upgrade.json`，再拉起看门狗（旧二进制 `upgrade-watch`）
   - Unix：`exec` 原地换成新版本，PID 不变
   - Windows 服务：看门狗停止并重新启动服务
   - Windows 托盘 / 前台进程：旧进程先移除托盘图标再退出，看门狗等它退出后启动新版本（前台进程用新控制台，托盘不带控制台）
4. 确认：新版本连上 hub、收到 HelloOK 后删掉状态文件。看门狗在试用期（`GUEST_UPGRADE_PROBATION`，默认 `2m`）内看到文件被删就退出。确认后（≥ 1.13.0）新版本删除 `probe-agent.old*`；Windows 上看门狗退出前文件被占用，会每 3 秒重试，最多约 1 分钟。不在试用期的正常启动也会顺带清理残留
5. 回滚：试用期过了还没确认，看门狗停掉新进程，`probe-agent` → `.bad`，`.old` → `probe-agent`，再按原方式启动旧版本。旧版本下次连上时上报结果，hub 记 `upgrade.failed`

看门狗日志在 `probe-agent.upgrade.log`。Unix 前台运行时，回滚后由看门狗重新启动的进程日志写到 `probe-agent.log`。`GUEST_UPGRADE=off` 拒绝一切远程升级。

终端前台运行也能升级，但重启或注销后不会自动启动，详情页会提示改用 `install`。

进度：agent（≥ 1.13.0）在 `agent.upgrade` 调用期间以同一 id 发 `stream: "upgrade"` 帧，`data` 为 `{stage, done, total}`（`download` 带已下载字节数、`verify`、`selftest`）。hub 按设备记录 `queued → download → verify → selftest → restart → done | failed | rolled_back`：调用返回成功记为 `restart`，新版本再次 hello 时按 `UpgradeReport` 记 `done` 或 `rolled_back`。同一设备未结束的升级会拒绝再次下发（`409 upgrade_busy`）。监控页用自绘弹窗确认，开始后每 0.7 秒轮询 `GET /v1/probe/upgrade` 画进度条；关闭弹窗只是停止观察，升级继续。旧 agent 不发进度帧，弹窗会从「下发中」直接跳到「重启中」。

### macOS 签名与录屏权限

macOS 把「录屏与系统录音」授权绑定在二进制的 designated requirement 上。ad-hoc 签名的要求就是本次构建的 cdhash，每次升级都会失效，系统再次弹窗，设置里还会残留旧条目。打包脚本对 darwin 产物用固定证书和固定标识符 `probe-agent` 签名，要求变成 `identifier "probe-agent" and certificate leaf = H"…"`，以后升级授权不会丢：

```bash
supplemental/scripts/probe-agent-macos-sign-setup.sh   # 只需一次：在 ~/.config/probe-agent/signing.keychain-db 生成自签名代码签名证书
```

证书放在独立 keychain，随机密码存在旁边的 `signing.pass`（0600），不碰登录 keychain；打包时临时加入 keychain 搜索列表，签完恢复。目录要备份，换证书意味着每台 Mac 再授权一次。没有证书时退回 ad-hoc 签名，脚本会告警。第一次换成证书签名的版本需要在设置里删掉旧的 `probe-agent` / `probe-agent.old` 条目，再给新版本授权一次。截屏失败且 `CGPreflightScreenCaptureAccess` 为假时，画面窗口会直接提示去开启权限。

### 发布

```bash
go run ./internal/cmd/release keygen   # 只需一次：私钥写到 ~/.config/probe-agent/release.key，公钥追加到 internal/release/release.pub
make probe-agent-dist               # 构建并签名（私钥不存在时只告警，产物没有签名，agent 会拒绝）
go run ./internal/cmd/release sign dist/probe-agent/1.9.0   # 单独重签
```

私钥不进仓库，请离线备份；丢失只能生成新密钥对，并让所有 agent 手动更新一次以信任新公钥。hub 从 `PROBE_HUB_AGENT_DIR`（默认 `./dist/probe-agent`，其次二进制旁的 `probe-agent/`）读取发布目录，不需要私钥。

### API

agent 用 `X-Guest-Token`：

- `GET /v1/probe/agent/latest?os=&arch=&current=` — `{version, artifact, url}`；已是最新时返回 204
- `GET /v1/probe/agent/download/{version}/{os}/{arch}` — 二进制

管理端（Bearer）：

- `GET /v1/probe/releases` — `{latest, versions: [{version, created, platforms}]}`
- `POST /v1/probe/sessions/{id}/upgrade` — `{version?, confirm: true}`，`version` 不传时用 latest。agent 太旧返回 `409 upgrade_unsupported`，版本相同返回 `409 already_current`
- `POST /v1/probe/upgrade` — `{version?, ids?, confirm: true}`；不传 `ids` 时选所有在线、可升级、且落后于目标版本的探针，并发 4 台，返回 `[{id, ok, from, to, message?}]`
- `GET /v1/probe/upgrade` — 最近 30 分钟的升级进度 `[{id, from, to, stage, done?, total?, message?, started, updated}]`，新的在前

## 通用原语与插件

agent ≥ 1.16.0 在 Hello 里声明 `caps` 和 `primitives`（原语 → 版本）。插件只通过这些原语操作探针所在的机器，hub 和 agent 都不额外监听端口：

| 原语 | 作用 | 说明 |
| --- | --- | --- |
| `tunnel.tcp` | 经探针的出站 WebSocket 连到那台机器上的 TCP 服务 | 只能由 hub 发起 |
| `fs.which` | 按路径或 PATH 查找程序 | 路径可写 `~`、`%VAR%`、`$VAR` |
| `fs.read` | 读小文件 | 默认 64KB，最多 1MB；不存在的文件和目录直接跳过 |
| `proc.spawn` | 脱离探针启动进程 | 参数只展开开头的 `~` 和 `%VAR%`；`GUEST_SPAWN=off` 时不声明 |
| `net.listeners` | 列出 TCP 监听端口和进程名 | macOS 用 `lsof`，其他系统用 gopsutil |

没声明的原语返回 `409 cap_missing`。

### TCP 隧道

- 帧：`tunnel.open` RPC（`{id, mode: "tcp", target}`）成功后，双向用 `stream: "tun"` 传字节（每帧最多 32KB），任一侧发 `tun.close` 关闭。每台最多 64 条隧道（`409 too_many_tunnels`；1.16.0 探针为 16）。插件开的隧道 15 分钟无流量自动关闭，端口转发的不关
- 目标白名单 `GUEST_TUNNEL_ALLOW`（在 agent 上配）：空 = 只允许回环；`off` = 关闭隧道并不再声明能力；否则逗号分隔的 `loopback`、CIDR、`ip:port`、`ip:*`。主机名先解析，按解析出的 IP 校验并拨号。被拒 `403 agent_policy`，连不上 `502 dial_failed`
- `GET /v1/probe/sessions/{id}/tunnels`（Bearer）— `{tunnels}`：活动隧道的目标、发起方（`plugin:<name>`、`forward:<id>`）、收发字节

### 端口转发

把远端机器上的 TCP 服务映射成 Hub 所在机器的本地端口。每个进来的连接新开一条隧道，HTTP、WebSocket、SSH、数据库等基于 TCP 的协议原样透传。规则保存在 Hub 上，关掉页面不受影响，多台机器可以同时转发。

- 入口：顶栏的「隧道转发」按钮管理全部机器；主机详情页的「隧道转发」按钮只管这台
- 本地端口全局唯一：与已有规则重复，或已被本机其他程序占用，都返回 `409 port_taken`。不填端口时自动分配
- 监听地址只能选 `127.0.0.1`（仅本机，默认）或 `0.0.0.0`（局域网）。选局域网时同网段的人不用登录 Hub 就能访问这个服务
- 添加时先经探针试连一次目标，探针离线、目标不在白名单或连不上都直接报错。之后探针掉线时规则保留，连接会被接受后立即断开，并记下最近一次失败原因
- 规则存在 `probe-hub.forwards.json`（工作目录下，`PROBE_HUB_FORWARDS_FILE` 可改），Hub 重启后恢复；端口被占用的规则会保留并标记监听失败

管理端（Bearer）：

- `GET /v1/probe/forwards[?agent=]` — `{forwards: [{id, agent, name, bind, port, target, note, online, listening, error?, lastError?, conns, total, rxBytes, txBytes}], binds}`
- `POST /v1/probe/forwards` — `{agent, target, port?, bind?, note?}`，返回 `201` 和规则。审计 `tunnel.forward`
- `DELETE /v1/probe/forwards/{id}` — 停止监听并断开正在用的连接，`204`。审计 `tunnel.unforward`

### 插件

插件用 Go 写，编译进 hub（`internal/hub/plugins`），通过 `Host` 接口使用上面的原语，并在清单里声明用到的原语和目标地址，越界 `403 plugin_forbidden`。

- 安装状态存在 `probe-hub.plugins.json`（工作目录下，`PROBE_HUB_PLUGINS_FILE` 可改）。文件不存在时按 `PROBE_HUB_PLUGINS`（逗号分隔）安装，默认都不装
- 未安装的插件，接口返回 `404 plugin_not_installed`，页面返回 404。卸载时立即关闭它开的隧道（包括正在用的 WebSocket）

管理端（Bearer）：

- `GET /v1/probe/plugins` — 所有插件的清单、是否已装、页面模块地址，以及每台在线探针是否满足所需原语
- `GET /v1/probe/plugins/{name}` — 单个插件
- `POST /v1/probe/plugins/{name}` / `DELETE /v1/probe/plugins/{name}` — 安装 / 卸载
- `* /v1/probe/plugins/{name}/...` — 插件自己的接口

页面：插件中心 `#/plugins` 里安装、卸载、打开；插件页 `#/plugins/{name}?agent=`；主机详情页的「插件」按钮在抽屉里打开。带页面的插件提供一个 ES 模块 `/plugins/{name}/index.js`，导出 `mount(el, ctx)`，可返回清理函数。`ctx` 有 `name`、`agent`、`embedded`（是否在抽屉里）、`params`、`api(path, opts)`（带上管理令牌，请求插件自己的接口）、`open(name, params)`（跳到另一个插件）。

### bitbrowser

调用探针所在机器上的比特浏览器本地 API，用到 `tunnel.tcp`。hub 上配 `BITBROWSER_ADDR`（默认 `127.0.0.1:54345`）和可选的 `BITBROWSER_API_KEY`（请求不带 `X-Api-Key` 时用它）。

- `GET .../bitbrowser/openapi.json` — OpenAPI 3.1，33 个接口；`x-confirm` 标记删除、全关、清 Cookie 等破坏性操作，`x-audit` 标记会记审计的操作
- `GET .../bitbrowser/operations` — 精简后的接口列表（必填字段、示例请求体）
- `GET .../bitbrowser/agents/{id}/status` — 本地 API 是否可达和延迟
- `POST .../bitbrowser/agents/{id}/call/{operationId}` — 按 operationId 调用，先校验必填字段（`400`）
- `* .../bitbrowser/agents/{id}/api/{path}` — 原样透传，状态码和响应体不改

破坏性操作要带 `X-Confirm: true` 或 `?confirm=true`，否则 `403`。审计 kind 为 `plugin.bitbrowser.<operationId>`，比特浏览器返回 `success: false` 时记为失败。打开窗口返回调试端口时，页面上可以直接转到 cdp 插件调试。

### cdp

发现、启动并调试探针所在机器上的 Chromium 系浏览器（Chrome、Edge、Chromium、Brave、Canary，以及比特浏览器等开了调试端口的窗口），用到 `tunnel.tcp`、`fs.which`、`fs.read`、`proc.spawn`、`net.listeners`，只允许连回环地址。

- `GET .../cdp/catalog` — 内置的浏览器目录（各系统的安装路径、默认用户目录）
- `GET .../cdp/agents/{id}/browsers[?all=1]` — `{installed, running, probed}`。按进程名挑出浏览器的监听端口（`all=1` 时探测全部回环端口），逐个请求 `/json/version`
- `POST .../cdp/agents/{id}/launch` — `{browser | path, port?, headless?, userDataDir?, url?, args?}`。默认用户目录 `~/.probe-cdp/<browser>`，端口不传时在 19222–19921 里随机选，等 `/json/version` 最多 20 秒；找不到浏览器 `404 browser_not_found`，超时 `504 launch_timeout`
- `GET|PUT|POST .../cdp/agents/{id}/ports/{port}/json[/...]` — 转发 CDP 的 HTTP 接口（`list`、`new?url`、`activate/{id}`、`close/{id}`），响应里的 `ws://127.0.0.1:port/devtools/...` 改写为经 hub 的地址
- `GET .../cdp/agents/{id}/ports/{port}/devtools/...` — WebSocket 转发，需 Bearer，Puppeteer / Playwright 等可以直接连。审计 `plugin.cdp.connect`
- `POST .../cdp/agents/{id}/ports/{port}/close` — 在浏览器级会话上发 `Browser.close`，浏览器正常退出（全部窗口关闭）。没有 `/json/version` 的端点（见下文 `wsOnly`）返回 `400`。审计 `plugin.cdp.close`
- `POST .../cdp/agents/{id}/ports/{port}/ticket` — `{path: "/devtools/..."}`，返回一次性地址 `/v1/probe/ws/plugins/cdp/{ticket}`，2 分钟内有效，给浏览器里不能带请求头的 WebSocket 用。这个前缀不校验管理令牌，用过或过期返回 `410`

转发时 `Host` 改成 `127.0.0.1:port`，并去掉 `Origin`、`Authorization`、`Cookie`，浏览器不需要加 `--remote-allow-origins`。

Chrome 144 起，在 `chrome://inspect` 里给默认用户目录开启的远程调试没有 `/json/*` 接口，只在用户目录下写 `DevToolsActivePort`（端口和 `/devtools/browser/<id>`）。插件会读这个文件，把端点标成 `wsOnly`，只能开浏览器会话；每次连接 Chrome 都会弹窗让用户允许。

页面提供端点列表、启动浏览器、页面与目标管理，以及在线调试台（常用命令、方法补全、事件流、截图预览）。
