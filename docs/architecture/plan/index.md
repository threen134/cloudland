# 设计文档索引

本目录有 37 份设计文档（不含本索引），写于不同时期，按状态分组编号 P01–P37（编号固定，状态变了也不改；新文档接着往后编），**可信度差别很大**。判断一个功能「应该怎样」时，只拿标为「维护中」的文档当依据；其余的只看来龙去脉，以代码和当前文档（[部署文档](../../deployment/index.md)、[使用指南](../../guide/introduction.md)）为准。

做法与背景见 [设计文档、实现与回归测试的统一](../design-code-test-alignment.md)。

> 状态说明
>
> - **维护中**：按当前实现写，改行为时要同步改它；回归用例可以拿它当依据
> - **历史**：一次性的实施方案或审查记录，已实施或已被别的方案取代，正文不再改
> - **作废**：方案依赖的东西已经不存在（如 SCI / C++ 主控、Python 网关），只作历史参考
>
> 索引建于 2026-10-14；三份 09-14 的文档（gRPC 下发、链路追踪、网关）同日复查后列为维护中，正文是方案原文，之后的变化集中写在文末一节。「历史 / 作废」是按文档内容与现有代码判断的，没有逐份核对实施细节。

## 维护中

| 编号 | 文档 | 内容 | 最后核对 |
|---|---|---|---|
| P01 | [shared-storage-design.md](shared-storage-design.md) | 共享存储：GPFS（副本 / 纠删码 / SAN）、Ceph RBD、共享系统盘、运维、宕机恢复、远程挂载 | 2026-10-07 全文复查，2026-10-14 按 work-04 实验更新 |
| P02 | [vpn-gateway-plan.md](vpn-gateway-plan.md) | VPN 网关：站点到站点、客户端、主备 / 双活、隧道高可用 | 2026-10-14 按代码修正 |
| P03 | [local-multi-disk-storage-plan.md](local-multi-disk-storage-plan.md) | 本地多盘存储池、本地卷、迁移换池 | 2026-10-14 按代码修正 |
| P04 | [vpc-transit-gateway-plan.md](vpc-transit-gateway-plan.md) | 中转网关（VPC 互联） | 2026-10-01 实施记录；没全文复查 |
| P05 | [placement-group-plan.md](placement-group-plan.md) | 放置组（分散 / 集中） | 2026-09-28 实施记录；只搜过 09-30 那批改动 |
| P06 | [sci-grpc-rewrite.md](sci-grpc-rewrite.md) | 控制面 → 计算节点的 gRPC 下发链路（cland-go / cloudlet-go） | 2026-10-14 复查，之后的变化写在 §13 |
| P07 | [distributed-tracing-otel-tempo-plan.md](distributed-tracing-otel-tempo-plan.md) | OpenTelemetry + Tempo 链路追踪 | 2026-10-14 复查，部署上的差异写在 §12 |
| P08 | [cpgateway-go-migration-plan.md](cpgateway-go-migration-plan.md) | 控制面网关（cpgateway） | 2026-10-14 复查，之后的变化写在 §13 |
| P09 | [compute-behavior.md](compute-behavior.md) | 计算资源行为说明：云服务器、迁移、云硬盘、镜像、规格与密钥（不是实施方案，只写对外行为） | 2026-10-14 功能域对齐 02 时按代码与 `CLAUDE.md` 整理 |
| P10 | [network-behavior.md](network-behavior.md) | 网络行为说明：VPC、子网、可用区、IP 组、安全组、弹性公网 IP、负载均衡 | 2026-10-14 功能域对齐 03 时按代码与 `CLAUDE.md` 整理 |
| P11 | [operations-behavior.md](operations-behavior.md) | 运维功能行为说明：告警、控制台、部署与计算节点、任务列表 | 2026-10-14 功能域对齐 04 时按代码与 `CLAUDE.md` 整理 |
| P12 | [admin-frontend-design.md](admin-frontend-design.md) | 系统管理前端：导航、三层权限、区域 / 可用区 / 计算节点 / 热迁移 / 节点告警各页调的接口，及其他仅管理员可见的入口 | 2026-10-15 按代码重写 |

## 历史

| 编号 | 文档 | 内容 | 现在以什么为准 |
|---|---|---|---|
| P13 | [alarm_plan_review.md](alarm_plan_review.md) | 告警方案的实现审查 | 告警在 2026-09-20 / 21 修复复查过，见 `CLAUDE.md`「告警」 |
| P14 | [review-feedback.md](review-feedback.md) | 用户告警通知方案的审查意见 | 同上 |
| P15 | [user-alarm-notification-plan.md](user-alarm-notification-plan.md) | 用户告警规则与通知推送 | 同上；通知渠道 2026-09-30 改为按组织 UUID 同步 |
| P16 | [vm-alarm-multi-level-support.md](vm-alarm-multi-level-support.md) | VM 告警规则多级阈值 | 同上 |
| P17 | [prometheus-auto-discovery-plan.md](prometheus-auto-discovery-plan.md) | Prometheus 自动发现计算节点 | 代码与 [`deploy/docker/config/`](https://github.com/threen134/cloudland/tree/stage-01/deploy/docker/config) |
| P18 | [consoleproxy-direct-expose-plan.md](consoleproxy-direct-expose-plan.md) | 多区域下的 consoleproxy 暴露方式 | 控制台 2026-09-17 改为自带页面，见 `CLAUDE.md`「VNC 控制台」「文本控制台」 |
| P19 | [dns-upstream-settings-plan.md](dns-upstream-settings-plan.md) | DNS 上游转发的界面配置 | 已实施（系统设置 `DNS_UPSTREAM`） |
| P20 | [internal-dns-plan.md](internal-dns-plan.md) | 控制面 dnsmasq 解析节点主机名 | 已实施，以代码为准 |
| P21 | [dynamic-system-settings.md](dynamic-system-settings.md) | 动态系统设置与通知渠道（Python 网关时期） | Go 网关的系统设置；Slack / 通用 Webhook 备用值 2026-09-17 已删除 |
| P22 | [image-repository-minio-plan.md](image-repository-minio-plan.md) | S3 兼容镜像仓库 | 已实施（`S3_ENDPOINT`），[部署文档 06](../../deployment/06-configuration.md) |
| P23 | [image-visibility-plan.md](image-visibility-plan.md) | 镜像公共 / 私有 | 已实施，以代码为准 |
| P24 | [private-subnet-type-plan.md](private-subnet-type-plan.md) | `private` 子网类型 | 已实施，以代码为准 |
| P25 | [private-vlan-interface-plan.md](private-vlan-interface-plan.md) | 私有网段走独立 VLAN 接口 | 已实施（`PRIVATE_VLAN_DEVICE`） |
| P26 | [org-quota-redesign-plan.md](org-quota-redesign-plan.md) | 组织级配额 | Go 网关的配额（7 项），见 `CLAUDE.md`「配额」 |
| P27 | [org-status-plan.md](org-status-plan.md) | 组织状态 | 以 cpgateway 代码为准 |
| P28 | [permission-redesign-plan.md](permission-redesign-plan.md) | 系统角色与组织角色的权限重构 | 以代码为准；权限缺口 2026-09-30 修过一批 |
| P29 | [permission-test-plan.md](permission-test-plan.md) | 权限重构的测试计划 | 回归用例 [`TC-11`](https://github.com/threen134/cloudland/blob/stage-01/test-items/TC-11-%E7%BB%84%E7%BB%87%E7%94%A8%E6%88%B7%E4%B8%8E%E9%85%8D%E9%A2%9D.md)、[`TC-13`](https://github.com/threen134/cloudland/blob/stage-01/test-items/TC-13-%E5%AE%89%E5%85%A8.md) |
| P30 | [unified-user-org-management-plan.md](unified-user-org-management-plan.md) | 用户与组织统一由网关管理 | 已实施；跨服务只按 UUID 对应，见 `CLAUDE.md`「身份与组织的跨服务传递」 |
| P31 | [keepalive.md](keepalive.md) | 区域心跳（Python 网关时期） | Go 网关的区域心跳 |
| P32 | [noble-dancing-dusk.md](noble-dancing-dusk.md) | 控制面高可用（keepalived + 两台控制节点） | 部署文档 [`03-ha-deployment.md`](../../deployment/03-ha-deployment.md) 与 [`deploy-ha-node.sh`](https://github.com/threen134/cloudland/blob/stage-01/deploy/docker/scripts/deploy-ha-node.sh) |
| P33 | [ssh-key-distribution-plan.md](ssh-key-distribution-plan.md) | cland SSH 密钥分发 | 已实施；集群共用一对密钥是待改的安全问题（`CLAUDE.md` 待办 A1） |

## 作废

| 编号 | 文档 | 内容 | 作废原因 |
|---|---|---|---|
| P34 | [node-lifecycle-architecture.md](node-lifecycle-architecture.md) | 节点生命周期（SCI 时期） | SCI 与 C++ 主控 2026-09-17 已删除；节点注册与准入见 [`sci-grpc-rewrite.md`](sci-grpc-rewrite.md) 与 `CLAUDE.md`「注册 / 准入」 |
| P35 | [node-lifecycle-management.md](node-lifecycle-management.md) | 去掉 host.list、API 驱动部署（SCI 时期） | 同上 |
| P36 | [docker-deploy-scenarios-plan.md](docker-deploy-scenarios-plan.md) | Docker 部署场景（含 C++ 主控） | 以 [`docs/deployment/`](../../deployment/index.md) 与 [`deploy/docker/`](https://github.com/threen134/cloudland/tree/stage-01/deploy/docker) 为准 |
| P37 | [implementation_plan.md](implementation_plan.md) | JSON 日志（含 C++ 服务、clbase） | 涉及的服务大多已不存在；日志约定见 `CLAUDE.md`「链路追踪约定」 |

## 没有设计文档的功能域

组织 / 用户 / 配额、审计与组织动态。这些功能域的回归用例以代码和 `CLAUDE.md` 的约定为准；需要时补一页简短的行为说明（见[对齐文档](../design-code-test-alignment.md) §3.1）。虚拟机、迁移、云硬盘、镜像、规格与密钥 2026-10-14 已有行为说明 [`compute-behavior.md`](compute-behavior.md)，网络与负载均衡有 [`network-behavior.md`](network-behavior.md)，告警、控制台、部署与计算节点有 [`operations-behavior.md`](operations-behavior.md)。
