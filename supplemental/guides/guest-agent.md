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
| `lifecycle` | `online` `offline`（含在线时长；hub 重启补记）`upgrade`（注明远程升级 / 自动更新）`upgrade.failed`（已回滚，warn）`reboot`（启动时间前移 > 2 分钟）`rekey` `conflict` |
| `change` | `remote`（来源 IP）`hostname` `os` `kernel` `cpu` `memory`（> 2%）`disk`（挂载点增删、容量 > 2%）`nic`（带 IPv4 的非容器网卡增删、IP/MAC 变化） |
| `audit` | `term.open/close`（含时长）`display.open/close` `exec`（命令行 + 退出码）`power.*` `proc.*` `service.*` `file.upload/download/mkdir/rename/delete/trash` `trash.restore/purge/empty` `meta`（改名称/标签）`agent.upgrade`（下发升级，`from → to`） |

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
- `GET|POST .../display` — GET 说明；POST 出 `/v1/probe/ws/display` 票。只读 JPEG 二进制帧，**无 input**。清晰度（agent ≥ 1.12.0）在 ws URL 上带 `&quality=auto|smooth|hd`：`smooth` 10fps / 最长边 1280 / 质量 60，`hd` 3fps / 2560 / 90，`auto`（默认）在 2/1280/55 … 10/1920/82 五档间按 2 秒窗口的发送耗时自动升降。多显示器（agent ≥ 1.11.0）：连上后 hub 先发文本帧 `{"type":"info","screens":[{index,name,x,y,width,height,primary}],"screen":0,"quality":"auto"}`（0 号恒为主屏）；浏览器发 `{"type":"screen","screen":N}` / `{"type":"quality","quality":"hd"}` 切换，hub 转成 `display.select` / `display.quality` 下发并回同类型消息，失败回 `{"type":"error","message":…}`；旧 agent 不发列表，只看主屏。macOS ≥ 14 用 ScreenCaptureKit（运行时 `dlopen`），失败退回 `screencapture`。macOS 按 CoreGraphics 顺序用 `screencapture -D`，Windows 用 `EnumDisplayMonitors` + 每显示器 DPI 感知截取。无交互会话 `409 no_user_session`，不回退宿主机截图。Windows 服务在 Session 0 里看不到用户桌面，画面要在用户会话里跑探针（或另做用户态助手）
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

产物按版本、平台隔离（版本取 `cmd/probe-agent/main.go` 的 `agentVersion`），旧版本可并存回滚。打包后自动签名生成 `manifest.json`，`latest` 指向的版本就是 hub 对外提供的升级目标：

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
| 终端前台 | 直接运行 | 调试 | `console` |

- **Windows 托盘**：`install` 写 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 的 `ProbeAgent`，并立即在后台启动；`uninstall` 删除该项并结束本机的托盘探针。托盘与服务二选一，装了一种再装另一种会被拒绝。直接双击 exe 也会进入托盘模式，但不会开机自启
- **macOS 菜单栏**：`install` 写 `~/Library/LaunchAgents/probe-agent.plist`（`RunAtLoad`、`KeepAlive`、仅 Aqua 会话）并 `launchctl bootstrap`。进程被结束会被 launchd 拉起，只有 `uninstall`（`bootout`）能停掉。不出现在程序坞。在终端里直接运行也会显示菜单栏图标，日志照常打到终端；root 或纯 SSH 登录时不显示
- `install` 时如果 exe 旁还没有 `probe-agent.env`，会把当前环境里的 `GUEST_*` 写进去（0600），因为登录自启拿不到终端里的环境变量
- 同一用户只允许一个实例：Windows 用命名互斥量 `Local\probe-agent`，并检查服务是否在跑；macOS 用 exe 旁的 `probe-agent.lock`（launchd 拉起的实例会等锁，而不是反复退出重启）。重复启动会提示"已在后台运行"后退出
- 菜单内容：连接状态（已连接时长 / 未连接原因 / 升级中）、Hub 地址、版本与设备 ID、打开监控页、查看日志、立即重连。**没有退出**，防止误关监控
- 日志：没有终端可写时（托盘、服务、launchd）写到 exe 旁的 `probe-agent.log`，超过 10MB 轮转为 `.log.1`；exe 目录不可写时落到用户缓存目录。`GUEST_LOG=stderr` 强制写 stderr，其他值视为日志路径
- `GUEST_TRAY=off` 关掉托盘 / 菜单栏图标，Windows 上这时仍然没有控制台，日志写文件

打进 `RV0000_BASE`。控制面 `LISTEN` 必须是 Guest **能出站到达** 的地址（常见 `PG-IKUAI-LAN` 上的内网 IP），不要用 `127.0.0.1`。

身份：Guest SMBIOS UUID = ESXi `config.uuid` / `biosUuid`，vm-server 用它把 VM 对到探针；本机可用 `GUEST_BIOS_UUID` 覆盖。probe-hub 内部按设备 ID（见上文）区分主机。

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
scripts/probe-agent-macos-sign-setup.sh   # 只需一次：在 ~/.config/probe-agent/signing.keychain-db 生成自签名代码签名证书
```

证书放在独立 keychain，随机密码存在旁边的 `signing.pass`（0600），不碰登录 keychain；打包时临时加入 keychain 搜索列表，签完恢复。目录要备份，换证书意味着每台 Mac 再授权一次。没有证书时退回 ad-hoc 签名，脚本会告警。第一次换成证书签名的版本需要在设置里删掉旧的 `probe-agent` / `probe-agent.old` 条目，再给新版本授权一次。截屏失败且 `CGPreflightScreenCaptureAccess` 为假时，画面窗口会直接提示去开启权限。

### 发布

```bash
go run ./cmd/probe-release keygen   # 只需一次：私钥写到 ~/.config/probe-agent/release.key，公钥追加到 internal/release/release.pub
make probe-agent-dist               # 构建并签名（私钥不存在时只告警，产物没有签名，agent 会拒绝）
go run ./cmd/probe-release sign dist/probe-agent/1.9.0   # 单独重签
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
