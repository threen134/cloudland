# 运维功能行为说明（告警、控制台、部署与计算节点）

> 状态：**维护中**。2026-10-14 按代码与 `CLAUDE.md` 的约定整理（功能域对齐 04，`test-items/alignment/04-告警控制台部署与计算节点.md`），经负责人确认作为这几个功能域的设计依据。
>
> 这不是实施方案，只写对外可见的行为：接口、业务规则、拒绝条件、权限、与其他功能的关系。改变这些行为的代码改动要同时改这里（改动规则见 `../design-code-test-alignment.md` §3.4）。
>
> 相关的维护中设计：下发链路与节点准入 `sci-grpc-rewrite.md`；通知渠道、系统设置、计算节点终端的密码与限流 `cpgateway-go-migration-plan.md` §3.4、§13；链路追踪告警 `distributed-tracing-otel-tempo-plan.md`；节点宕机后的疏散、隔离与对账 `shared-storage-design.md` §11；云服务器监控曲线 `compute-behavior.md` §5.2。部署步骤以 `docs/deployment/` 为准。

## 1. 权限

clapi 只按网关转发来的身份判断（网关每次按数据库取角色，见 `cpgateway-go-migration-plan.md` §13）。网关白名单标为系统管理员的接口，clapi 里也自己检查一次。

| 操作 | 要求 |
|---|---|
| 告警事件列表、发送流水 | 本组织成员，只看本组织的事件 |
| VM 告警规则：列表、详情、查看渠道绑定 | 本组织只读及以上 |
| VM 告警规则：创建 | 本组织编辑及以上 |
| VM 告警规则：删除、启用 / 停用、关联 / 解绑云服务器、改渠道绑定 | 规则组所属组织的编辑及以上；别的组织的规则组按不存在处理（404） |
| 节点告警规则的增删改查、Prometheus 当前规则（`active-rules`）、强制重写映射（`sync-mappings`） | 系统管理员 |
| 图形 / 文本控制台 | 本组织编辑及以上，云服务器按组织过滤 |
| 计算节点终端 | 系统管理员，且系统设置 `HOST_CONSOLE_ENABLED` 打开 |
| 计算节点的一切接口（列表、详情、注册、改属性、维护、疏散、解除隔离、删除、主机监控曲线） | 系统管理员 |
| 任务列表、任务详情 | 本组织只读及以上，按组织过滤 |

## 2. 告警

### 2.1 三块功能与事件来源

- **节点告警规则**（`/node-alarm-rules`）、**VM 告警规则**（`/metrics/alarm/{cpu,memory,bw}/rules`，规则组 + 明细 + 关联的云服务器）、**告警事件**（`/alarm/events`）各自独立
- 事件不是接口创建的：Prometheus 按规则文件判定 → Alertmanager → clapi `POST /api/v1/alerts/process`。clapi 按 `fingerprint` 幂等写入，`labels.owner` 是 clapi 本地组织 ID；没有 owner 的（节点告警）归系统组织；第一次收到就是 resolved 的记成 resolved
- **回调要带共享令牌**：`Authorization: Bearer <ALERT_WEBHOOK_TOKEN>`。clapi 没配令牌时拒绝所有回调（不放行）；Docker 部署由 Alertmanager 容器启动时把令牌写进 `/data/webhook_token`，`alertmanager.yml` 用 `credentials_file` 引用；部署脚本在为空时生成，HA 两台同步。clapi 的 8255 对整个管理网可达，没有令牌时任何机器都能伪造任意组织的告警并经它的渠道发出去
- VPN 网关的告警（`VpnTunnelDown`、`VpnConnectionDown` 等）不经 Prometheus：clapi 直接写事件，推给网关所属组织启用的渠道（`vpn-gateway-plan.md` §9.6）
- 链路追踪告警带 `source=tracing`，路由到 `platform` 接收器，不进 clapi
- 事件保留 `ALARM_EVENT_RETENTION_DAYS` 天（默认 30），clapi 后台清理

### 2.2 规则文件是全有或全无

Prometheus 加载规则时只要有一个文件坏了就回滚整份规则集，此后所有告警规则更新都不生效（只在 prometheus 容器日志里报 `loading groups failed`）。所以：

- 规则配置的键必须和 `deploy/roles/monitor/templates/*.yml.j2` 的变量名一一对上（各类型的变量见 `CLAUDE.md`「告警」一节），错键 400
- 渲染后扫一遍未替换的 `{{ }}`，有就报错且不写文件（Go 模板关键字除外）
- 写进规则文件的用户输入只能是整数、枚举或服务端生成的值；VM 规则的 `region_id` 限 `[A-Za-z0-9._-]`、最长 64，不符 400（它写在双引号里，引号和换行能写坏 YAML、插入伪造的标签）
- Docker 部署由部署脚本把模板拷进 `volumes/prometheus/node_templates/`

### 2.3 节点告警规则

- 每种 `rule_type` 全局只有一条；`owner` 取调用方组织；可以以停用状态创建（`Enabled` 不带 gorm 默认值）
- 规则组和规则带 `owner` / `rule_group` 标签，事件归系统组织
- 节点告警规则不进组织动态（有意不加审计动作映射）

### 2.4 VM 告警规则

- 规则组名 `^[a-zA-Z][a-zA-Z0-9_]*$`；明细的 `name` 由界面填「组名_序号」，不参与生成规则
- 级别 critical / warning / info；带宽规则每条选方向 in / out，阈值 1–100
- **只能关联本组织的云服务器**：创建时的 `linkedvms` 与 `POST /metrics/alarm/link` 里有读不到的云服务器时整批 400（`INSTANCES_NOT_FOUND`），不写任何关联；解绑不查云服务器（已删除的也要能解绑）
- 停用的规则组不能再关联云服务器（400 `RULE_GROUP_DISABLED`）
- 删除规则组在同一个事务里软删明细、关联和组

### 2.5 云服务器与规则组的映射

- `matched_vms.json` 是 Prometheus `vm-group-metadata` 任务的 file_sd 目标，规则靠它找到要监控的云服务器；文件一空，所有 VM 告警同时静默失效
- 创建 / 关联 / 解绑时增量更新（不重载 Prometheus）；clapi 启动 30 秒后及每 10 分钟从数据库全量重建，内容不同才写，任一查询失败整轮不写
- `POST /metrics/alarm/sync-mappings` 只用于排障（系统管理员，强制重写）

### 2.6 启动时重建

clapi 启动后在后台重建 VM 告警规则与资源自动调整规则的文件，任一失败每分钟重试一轮，最多 10 轮。

### 2.7 通知渠道与发送流水

- 渠道在网关里管理、按 `org_uuid` 同步到区域（`cpgateway-go-migration-plan.md` §3.4、§13）；规则组只能绑本组织的渠道（403 `channel_not_owned`），渠道还没同步到本区域时 409
- 事件对上规则组才走该组绑定的渠道并产生发送流水；通知地址（渠道与规则里的地址）都经只连公网地址的客户端发出（A14）

### 2.8 资源自动调整（未启用）

`/metrics/adjust/*` 不在网关白名单，compose 里的 Alertmanager 也没有到 `/alerts/resource-adjustment` 的路由，界面和外部接口都用不了。clapi 里的代码与回调保留，回调同样要 §2.1 的令牌。要不要整套删掉另定。

## 3. 控制台

### 3.1 图形控制台（VNC）

- `POST /instances/:id/console`（缺省 `type: vnc`），token 2 小时有效、一次性（每台云服务器每种类型一条记录，再开会作废未用的）；consoleproxy 解析 token 时再查一次组织角色
- 自带 noVNC 页面 `/console/:id`；定义域时带随机 VNC 密码，运行中设不上密码的（存量无密码）拒绝打开
- 页面功能：修饰键锁定、Tab / Esc / Ctrl+Alt+Del、F1–F12、虚拟键盘、粘贴为键入（仅 ASCII 可打印字符）

### 3.2 文本控制台（串口）

- `POST /instances/:id/console {"type":"serial"}`；解析时下发 `start_serial_console.sh`，节点用 socat 在内网地址的 16000–16999 随机端口做一次性转发
- 节点 iptables 只放行控制面；60 秒无人连接关闭；每次解析带随机会话 ID，同一实例只保留最新会话
- 只对把串口作为登录终端的系统有效

### 3.3 计算节点终端

- `POST /hypers/:uuid/console`：系统管理员 + `HOST_CONSOLE_ENABLED`；节点状态 0 / 1 / 2 才能开
- 是否要密码由 `HOST_CONSOLE_REQUIRE_PASSWORD` 决定（默认要），改这一项本身要密码；密码校验与 5 次 / 10 分钟限流在网关
- token 2 分钟、一次性；解析时再查一次开关；节点上以 `env -i` 起 root shell，只记录终端输出，空闲 `HOST_CONSOLE_IDLE_MINUTES`（5–240，默认 15）断开

### 3.4 多窗口登录态

只在 token 的组织不是目标组织时才 `switch-org`；新 token 经 `BroadcastChannel` 发给同一用户的其他窗口，401 时用新 token 重试一次。

## 4. 部署

### 4.1 控制节点

- `deploy-control-node.sh` 以 root 执行，按 `COMPOSE_PROFILES` 部署；`GRPC_AUTH_TOKEN`、`CAPTURE_UPLOAD_SECRET`、`VPN_SECRET_KEY`、`ALERT_WEBHOOK_TOKEN` 为空时生成（`VPN_SECRET_KEY` 一旦有密文就不能丢）
- loki 3100、OTLP 4317（都无鉴权）与 cland 5006 只需对计算节点开放：没显式设置且 `MANAGEMENT_VIP` 在本机时，`TELEMETRY_LISTEN_IP` 设为 `MANAGEMENT_VIP`、`GRPC_LISTEN` 设为 `MANAGEMENT_VIP:5006`；不在本机时保持监听全部地址并提示
- 其他对外端口只有 80 / 443；监控栈、clapi 8255、alarm-rules-manager 8256 都绑 `INTERNAL_IP`
- `REPO_BRANCH` 优先级：显式环境变量 > `.env` > 已有仓库当前分支 > `staging`
- 所有部署脚本开头写 apt 配置 `DPkg::Lock::Timeout "600"`：新装的系统开机后 unattended-upgrades 占着 dpkg 锁，不等就直接失败

### 4.2 控制面 HA 与多区域

- `deploy-ha-node.sh`：MASTER / BACKUP 两台，keepalived 漂移 VIP；共享密钥（上面四个）由 MASTER 生成、BACKUP 同步。遥测端口要监听 VIP，保持监听全部地址，靠内网隔离挡住公网（绑 VIP 要开 `ip_nonlocal_bind`，没做）
- 多区域：只部署区域控制面（`COMPOSE_PROFILES=region`）并在中央控制面登记区域；组织、系统设置、通知渠道由网关全量推送

### 4.3 计算节点

- `POST /hypers` 建节点记录（状态 4 部署中）并返回 `deploy_command`；节点执行后编译 cloudlet-go、取回 cland 密钥、注册上线，cland 用 clapi 的节点清单校验
- 计算节点必须是经典 sudo；部署脚本装依赖（haproxy、keepalived、socat、strongSwan、WireGuard、FRR 等）、`cloudland-iptables.service`、把 bond 交给 NetworkManager
- 只有 4（部署中）/ 5（部署失败）能重试部署；离线（10）的节点要先删除再注册，换新 hostid（待办 B4）

## 5. 计算节点

### 5.1 状态

0 停用、1 可用、2 维护中、4 部署中、5 部署失败、10 离线（cland 报告失联，带 `offline_at`）。心跳不覆盖管理员设的 0 / 2；离线的节点回来时按 `offline_prior` 恢复 0 / 2，其余按节点上报。

### 5.2 启用 / 停用与属性

- `PATCH /hypers/:uuid`：`status` 只接受 0 / 1；可用区按 UUID；超分比例 ≥ 1；备注
- 按当前状态处理启用 / 停用：部署中、部署失败的节点拒绝（400，`ErrHypervisorInvalidState`）；离线的节点不改 status（疏散与隔离靠「10 + offline_at」判断），只记 `offline_prior`，节点回来后按它恢复；其余写入并下发 `update_hyper.sh`
- 改可用区会重启节点的 cloudlet

### 5.3 维护

`POST /hypers/:uuid/maintain`：只能从 0 / 1 进入，有迁入中的迁移时拒绝；置为 2 并按需批量迁走云服务器，迁不走的逐台给原因；全部失败时恢复原状态。

### 5.4 删除

有云服务器、是存储集群成员、有隔离记录时拒绝（400）；删除后下发 Shutdown，cloudlet 正常退出且不被 systemd 拉起。

### 5.5 疏散与隔离

见 `shared-storage-design.md` §11（`POST /hypers/:uuid/evacuate`、`/unfence`）。

### 5.6 列表、详情与监控

列表与详情给出状态、用量、磁盘、存储池、隔离与待补清理；主机 CPU / 内存曲线见 `compute-behavior.md` §5.2。

## 6. 任务列表

`GET /tasks`、`GET /tasks/:id`：按组织过滤的异步任务记录（来源 manual / scheduler / migration）；列表默认不含迁移任务，`source=migration` 才列出。

## 7. 已知限制

- 离线节点不能直接重新部署（待办 B4）
- HA 控制面的遥测端口监听全部地址（§4.2）
- 节点告警规则页没有绑定渠道的入口（待办 B17）
- 资源自动调整未启用（§2.8）

## 8. 回归用例对照

由 `test-items/scripts/check_route_coverage.py --update-design-maps` 按追踪表 `test-items/回归功能追踪.csv` 的「设计依据」列生成，不要手改。

<!-- regression-map:begin -->

> 由 `test-items/scripts/check_route_coverage.py --update-design-maps` 从回归追踪表（`test-items/回归功能追踪.csv` 的「设计依据」列）生成，**不要手改**：改追踪表后重新生成，CI 会检查两边是否一致。

| 章节 | 功能（功能域 / 大功能 / 子功能） | 回归用例 |
|---|---|---|
| §1 权限 | 计算 / 控制台 / token 与权限 | TC-09 CON-06 |
| §1 权限 | 控制面 / 告警 / 节点告警规则 | TC-10 ALM-02、ALM-03、ALM-03b、ALM-04 |
| §1 权限 | 控制面 / 告警 / VM 告警规则的权限 | TC-10 ALM-05；TC-10 ALM-07 |
| §1 权限 | 部署与计算节点 / 计算节点 / 列表与详情 | TC-27 HYP-01；TC-12 UI-01 |
| §2.1 三块功能与事件来源 | 控制面 / 告警 / 告警事件 | TC-10 ALM-06 |
| §2.1 三块功能与事件来源 | 控制面 / 告警 / 告警回调鉴权 | TC-10 ALM-06 |
| §2.1 三块功能与事件来源 | 控制面 / 告警 / 链路追踪告警 | TC-10 ALM-08 |
| §2.1 三块功能与事件来源 | 控制面 / 告警 / VPN 告警事件 | TC-10 ALM-12 |
| §2.2 规则文件是全有或全无 | 控制面 / 告警 / 规则文件全有或全无 | TC-10 ALM-00 |
| §2.2 规则文件是全有或全无 | 控制面 / 告警 / 规则变量名 | TC-10 ALM-01 |
| §2.2 规则文件是全有或全无 | 控制面 / 告警 / VM 告警规则的权限 | TC-10 ALM-05；TC-10 ALM-07 |
| §2.3 节点告警规则 | 控制面 / 告警 / 节点告警规则 | TC-10 ALM-02、ALM-03、ALM-03b、ALM-04 |
| §2.3 节点告警规则 | 控制面 / 告警 / 本地存储池告警 | TC-10 ALM-11 |
| §2.4 VM 告警规则 | 控制面 / 告警 / VM 告警规则 | TC-10 ALM-05 |
| §2.4 VM 告警规则 | 控制面 / 告警 / VM 告警规则的权限 | TC-10 ALM-05；TC-10 ALM-07 |
| §2.5 云服务器与规则组的映射 | 控制面 / 告警 / VM 规则映射对账 | TC-10 ALM-09 |
| §2.6 启动时重建 | 控制面 / 告警 / 启动时重建规则 | TC-10 ALM-10 |
| §2.7 通知渠道与发送流水 | 控制面 / 告警 / 通知渠道 | TC-10 ALM-07 |
| §2.7 通知渠道与发送流水 | 控制面 / 告警 / 节点告警绑定渠道（界面） | TC-10 ALM-07 |
| §2.8 资源自动调整（未启用） | 控制面 / 告警 / 告警回调鉴权 | TC-10 ALM-06 |
| §3.1 图形控制台（VNC） | 计算 / 控制台 / 图形控制台（VNC） | TC-09 CON-01 |
| §3.1 图形控制台（VNC） | 计算 / 控制台 / token 与权限 | TC-09 CON-06 |
| §3.2 文本控制台（串口） | 计算 / 控制台 / 文本控制台（串口） | TC-09 CON-02 |
| §3.2 文本控制台（串口） | 计算 / 控制台 / 依赖与环境 | TC-09 CON-04 |
| §3.3 计算节点终端 | 计算 / 控制台 / 计算节点终端 | TC-09 CON-03 |
| §3.4 多窗口登录态 | 计算 / 控制台 / 多窗口登录态 | TC-09 CON-07；TC-12 UI-07 |
| §4.1 控制节点 | 部署与计算节点 / 部署 / 一键部署控制节点 | TC-27 DEP-01 |
| §4.2 控制面 HA 与多区域 | 部署与计算节点 / 部署 / 控制面 HA | TC-27 DEP-04 |
| §4.2 控制面 HA 与多区域 | 部署与计算节点 / 部署 / 多区域 | TC-27 DEP-05 |
| §4.3 计算节点 | 计算 / 控制台 / 依赖与环境 | TC-09 CON-04 |
| §4.3 计算节点 | 部署与计算节点 / 部署 / 注册与部署计算节点 | TC-27 DEP-02 |
| §4.3 计算节点 | 部署与计算节点 / 部署 / 部署失败重试与重新注册 | TC-27 DEP-03 |
| §5.2 启用 / 停用与属性 | 部署与计算节点 / 计算节点 / 启用 / 停用与属性 | TC-27 HYP-02；TC-06 LB-07b；TC-18 |
| §5.3 维护 | 部署与计算节点 / 计算节点 / 维护模式 | TC-27 HYP-03；TC-18 PG-10、PG-18 |
| §5.4 删除 | 部署与计算节点 / 计算节点 / 删除计算节点 | TC-27 HYP-04；TC-16 STO-08 |
| §5.6 列表、详情与监控 | 部署与计算节点 / 计算节点 / 列表与详情 | TC-27 HYP-01；TC-12 UI-01 |
| §6 任务列表 | 部署与计算节点 / 计算节点 / 任务列表 | TC-27 HYP-06 |
| §7 已知限制 | 控制面 / 告警 / 节点告警绑定渠道（界面） | TC-10 ALM-07 |
| §7 已知限制 | 部署与计算节点 / 部署 / 部署失败重试与重新注册 | TC-27 DEP-03 |

<!-- regression-map:end -->
