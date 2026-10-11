# Vartio

探针 Hub 与 Agent。Agent 只出站连接 Hub，Hub 不连 ESXi。

| 二进制 | 入口 | 默认 |
|--------|------|------|
| vartio | `go run ./internal/cmd/hub` | `127.0.0.1:8766` |
| vartio-agent | `go run ./internal/cmd/agent` | 无入站端口 |
| vartio-release | `go run ./internal/cmd/release` | 给分发包签名 |

```bash
make vartio
make vartio-agent
```

环境变量新名前缀是 `VARTIO_`（Hub）和 `VARTIO_AGENT_`（Agent）。已经写在机器上的 `PROBE_HUB_*`、`GUEST_*` 仍然有效。状态文件同样：有 `probe-hub.key` 就继续用它，没有才新建 `vartio.key`。

线上协议没有改：`/v1/probe/*`、Hello 签名 `probe-hub-hello/v1`、发布包签名 `probe-agent|…`、请求头 `X-Guest-*`。已安装的 agent 用远程升级替换二进制；macOS 签名标识、LaunchAgent 标签和 Windows 服务名仍是 `probe-agent`，这样录屏授权和正在跑的服务不会断。

说明见 [supplemental/guides/guest-agent.md](supplemental/guides/guest-agent.md)。ESXi 虚拟化层探针尚未实施，约定见 [supplemental/guides/vmware-probe.md](supplemental/guides/vmware-probe.md)。

插件不编进 Hub。插件中心下载已签名的 `.vtp` 安装包（整包 SHA-256 加 Ed25519），校验通过后启动。官方插件是 `vartio-plugin-chrome-devtools`（插件名 `cdp`，适用于 Mac、Windows、Linux）和 `vartio-plugin-bitbrowser`（适用于 Mac、Windows）。已安装的记录仍在 `probe-hub.plugins.json` / `vartio.plugins.json`，目录是 `vartio-plugins/`（`VARTIO_PLUGIN_DIR` 可改）。官方签名公钥编进 Hub，私钥不入库。
