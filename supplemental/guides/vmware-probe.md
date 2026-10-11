# ESXi 探针（未实施）

vmkits 是独立的 ESXi 产品。Vartio 不连接 ESXi，也不引用 vmkits 的代码。需要虚拟化层指标时，由单独的 `vartio-agent-vmware` 调用 vmkits 的只读 HTTP，再按现有探针协议上报。Hub 的适配方式对齐 Docker：探针多一个能力，子对象挂在这台探针下面，速率由 Hub 计算。

## 三个角色

| | 产品 | 持有 | 和 Hub 的关系 |
|---|---|---|---|
| vmkits | ESXi 控制面（清单、QueryPerf、截图、电源、克隆、WebMKS） | ESXi 账号 | 不连接 Hub |
| `vartio-agent-vmware` | 跑在能访问 vmkits 的机器上的探针 | vmkits 地址和只读令牌 | 只出站上报 |
| Vartio Hub | 探针监控 | 探针上报的采样 | 不调用 ESXi，也不请求 vmkits |

一台 `vartio-agent-vmware` 代表整台 ESXi。它下面的虚拟机是子对象，相当于一台开启了 Docker 的主机下面挂着容器。虚拟机内部的进程、磁盘和画面仍由装进客户机的普通 `vartio-agent` 上报，那是另一台探针。

部署位置可以是该 ESXi 上的一台 Linux 虚拟机，也可以是任何能访问 vmkits 的机器。一台 ESXi 对应一个 vmkits 进程。

## 和 Docker 的对应

Docker 已经按这个形状接好了，实施时沿用同一条路径：

| Docker | VMware |
|---|---|
| 引擎是独立运行环境，探针调它的 API | vmkits 是独立产品，探针调它的只读 HTTP |
| 能力 `docker`（`internal/entities/proto.go` 的 `CapDocker`） | 能力 `vmware` |
| 指标里的 `Metrics.Docker`（`DockerInfo`） | 指标里增加 `Metrics.VMware`，带主机和虚拟机采样 |
| `agent/connect.go` 按配置追加能力 | `vartio-agent-vmware` 在 Hello 里带上 `vmware` |
| `internal/hub/systems/monitor.go` 的 `dockerSample` 用上一拍累计值算速率 | Hub 同样用上一拍计数器算速率，不在探针里算 |
| 页面上的容器列表、曲线、详情 | 这台探针的详情里增加虚拟机列表和虚拟化层曲线 |

探针二进制可以单独命名为 `vartio-agent-vmware`，协议仍是现在的 Hello 和指标上报。它不链接 govmomi，也不读取 `ESXI_URL` / `ESXI_PASSWORD`。

## 采集

探针按 Hub 的采样间隔调用 vmkits 已有的只读接口：

- `GET /v1/host`、`GET /v1/host/metrics`
- `GET /v1/vms`、`GET /v1/vms/{id}/metrics`

上报内容是计数器和瞬时值（CPU 使用、活跃内存、网卡收发、电源状态、名称、`biosUuid`、moref）。独立 ESXi 的 QueryPerf 实时窗口大约一小时、20 秒一个点；历史由 Hub 保存，vmkits 不另做一套时序库。

vmkits 需要给这个探针一个只读入口（主机、虚拟机清单和性能计数器）。写操作不在这组接口里。

## 身份

- 这台探针自己的主键仍是设备指纹。
- 子对象虚拟机用 vmkits 的 `biosUuid`（ESXi `config.uuid`）标识。
- 客户机里的普通 agent 继续用设备指纹做主键，BIOS UUID 只是属性。
- Hub 用 BIOS UUID 把「虚拟化层里的这台虚拟机」和「客户机里的那台探针」连起来，页面上可以互相跳转。两套采样不画进同一张图：QueryPerf 的活跃内存和客户机自己的内存用量不是同一个数。
- vmkits 的控制台截图留在 vmkits。客户机画面只来自 agent 的桌面采集。

## Hub 做的适配

- Hello 能力 `vmware`。
- 指标载荷里的主机摘要和虚拟机列表。
- 详情页：虚拟机列表、单台虚拟机的虚拟化层曲线、与客户机探针的跳转。
- 存储和降采样走现有的主机 / 容器指标同一类机制，单独的表或字段，不写进客户机那条 CPU、内存曲线。

Hub 不做的事：电源、克隆、数据存储、网卡修改、WebMKS。这些仍只在 vmkits 里操作。详情里可以放 vmkits 的链接。

## 实施顺序

1. 定 `Metrics.VMware` 和能力名 `vmware`，对照 `DockerInfo` / `CapDocker`。
2. `vartio-agent-vmware` 只通过 HTTP 读 vmkits，配置为地址和只读令牌。
3. Hub 收采样、算速率、落历史。
4. 详情页列出虚拟机，并用 BIOS UUID 链接到已接入的客户机探针。
