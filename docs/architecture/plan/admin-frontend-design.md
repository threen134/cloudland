---
order: 12
---
# P12 系统管理（Administration）前端设计

- **状态**：2026-10-15 按代码重写（`stage-01`，`14b01d66` 之后的工作区）。原文是 2026-03 的方案，写的是 Python 网关时期的设计，原文见 git `8ab38be4`；与原文的差别见 §9。
- **范围**：只有系统管理员看得到的界面——侧边栏「系统管理」「存储」两个分组，以及「设置」与「组织」入口。存储页面只列入口和接口，行为见 [P01 共享存储](shared-storage-design.md)与 [P03 本地多盘存储池](local-multi-disk-storage-plan.md)；告警规则的配置键见 [P11 运维功能行为说明](operations-behavior.md)。
- **以什么为准**：本文写的是界面上有什么、调哪个接口、谁能用；业务规则（能不能迁、何时拒绝）以后端代码与上面几份文档为准。

## 1. 导航结构

侧边栏在 `web/src/views/dashboard/Layout.vue`。只有 `auth.user.is_superuser` 为真时才渲染下面这些分组和入口：

```
权限管理
└── 组织                /dashboard/orgs           （同组里的「用户」所有人可见）
存储                     （整个分组仅系统管理员）
├── 存储池              /dashboard/storage-pools
└── 存储集群            /dashboard/storage-clusters   （标签：集群 / 任务 / 软件包）
系统管理                 （整个分组仅系统管理员）
├── 区域管理            /dashboard/regions
├── 可用区              /dashboard/zones
├── 计算节点            /dashboard/hypervisors
├── 热迁移              /dashboard/migrations
└── 节点告警            /dashboard/alarms
设置                    /dashboard/settings
```

「告警」分组（告警事件、VM 告警规则、通知渠道）对所有成员开放，不在本文范围内。

## 2. 权限控制

三层，每层都要有，前端只是第一道：

| 层         | 位置                                                  | 做法                                                                                                                                                                                                                                                                                                                      |
| ---------- | ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 路由守卫   | `web/src/router/index.ts`                           | 路由带 `meta: { requiresSuperAdmin: true }`，`beforeEach` 里 `auth.user?.is_superuser` 不为真时跳回 `/dashboard`                                                                                                                                                                                                  |
| 控制面网关 | `cpgateway/src/apis/routes.go`、`proxy_routes.go` | 区域接口挂在 `admin` 组（`ActiveUser()` + `Superuser()`）；转发到区域的接口在白名单里标 `Admin: true`（`/hypers*`、`/migrations*`、`/instances/:id/migration_targets`、`/node-alarm-rules*`、`/metrics/hypers/*`、存储的写接口等），非系统管理员 403。转发时角色从数据库取，不信 token 声明（待办 A13） |
| clapi      | `api/src/services/*.go`                             | 再查一次 `CheckSystemPermission()`。可用区的读接口白名单不标管理员（建云服务器要选可用区），建 / 改 / 删由 clapi 拦（`services/zone.go`）                                                                                                                                                                             |

带 `requiresSuperAdmin` 的路由：组织（列表 / 详情）、区域、可用区（列表 / 详情）、计算节点（列表 / 详情）、存储池（列表 / 详情）、存储集群（列表 / 新建 / 详情，软件包是列表页的一个标签）、存储任务详情、热迁移（列表 / 详情）、节点告警（列表 / 详情）、设置。

## 3. 区域管理

- **页面**：`RegionList.vue`（只有列表页，详情在弹窗里）
- **接口**：cpgateway 自己处理，不转发（`api/regions.ts`）

| 接口                                   | 界面                                                                                                                                                                                                                                      |
| -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /regions`                       | 列表：名称、描述、状态（可用 / 离线 / 维护中，由 `is_available` 与 `maintenance_mode` 推出）；服务端分页 + 搜索（`offset` / `limit` / `query`）。这个接口所有登录用户都能调，返回公开视图，区域切换器也用它（传 `limit=500`） |
| `GET /regions/{uuid}`                | 编辑弹窗打开时取管理员视图（含 `internal_endpoint`、`fail_count`）                                                                                                                                                                    |
| `POST /regions`                      | 创建弹窗：`name`（`^[a-z0-9][a-z0-9-]*[a-z0-9]$`）、`display_name`、`internal_endpoint`（http / https）、`internal_secret`（留空或 `auto-generate` 由后端生成）、`description`。`internal_secret` 只在创建响应里返回一次  |
| `PATCH /regions/{uuid}`              | 编辑：显示名、内部地址、描述、可用、维护模式；显式 `null` 表示清空。改为可用会触发一次全量同步（组织、系统设置、通知渠道）                                                                                                              |
| `DELETE /regions/{uuid}`             | 删除确认；区域里还有用量时网关拒绝                                                                                                                                                                                                        |
| `POST /regions/{uuid}/rotate-secret` | 轮换密钥，新密钥只显示一次。**不会同步到区域侧**：要手工改区域 `.env` 的 `CPGATEWAY_SECRET_KEY` 并重建 clapi（`TC-11` ORG-11）                                                                                                |

## 4. 可用区

- **页面**：`ZoneList.vue`、`ZoneDetail.vue`（路由参数是可用区名）
- **接口**：转发到 clapi（`api/zones.ts`）

| 接口                     | 界面                                                                                                                                          |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /zones`           | 列表：名称、类型（默认 / 普通，对应列 `default`）、备注；`useListQuery` 服务端分页搜索。「类型」列不开放排序（`default` 是 SQL 保留字） |
| `POST /zones`          | 创建：`name`、`default`、`remark`                                                                                                       |
| `GET /zones/{name}`    | 详情页，下方列出这个可用区的计算节点（取 `GET /hypers?limit=500` 后在浏览器里按 `zone_name` 过滤，节点超过 500 台会缺）                   |
| `PATCH /zones/{name}`  | 编辑：只能改 `default` 与 `remark`，名称不可改                                                                                            |
| `DELETE /zones/{name}` | 删除确认                                                                                                                                      |

## 5. 计算节点

- **页面**：`HypervisorList.vue`、`HypervisorDetail.vue`；详情页的组件 `components/monitoring/HostMonitoringCharts.vue`、`components/storage/HostStorageTab.vue`、`HostRecoveryCard.vue`、`HostEvacuateModal.vue`；终端 `/host-console/:uuid`（`SerialConsole.vue`，`composables/useHostConsole.ts`）
- **接口**：转发到 clapi（`api/hypervisors.ts`、`api/storagePools.ts` 的节点部分），全部要求系统管理员

### 5.1 状态

| 值 | 文案     | 说明                                                                                                                                            |
| -- | -------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| 0  | 已禁用   |                                                                                                                                                 |
| 1  | 活跃     | 只有这个状态能进入维护、接收迁入                                                                                                                |
| 2  | 维护中   | 列表上另显示「腾空中 · 剩 N 台」或「已腾空」（按节点上剩余的云服务器数，不按进行中的迁移数——迁移失败时云服务器留在原地）                     |
| 4  | 部署中   | 注册后、节点执行部署命令期间                                                                                                                    |
| 5  | 部署失败 | 4 / 5 时可以重新部署                                                                                                                            |
| 10 | 离线     | cland 判离线；详情页显示「离线」、出现「疏散」与恢复卡片。⚠️ 列表页的状态表没有 10，显示的是后端的 `status_name` 或「未知(10)」，详情页正常 |

维护状态由控制面单方面设置，心跳不会改回去，所以列表与详情都有「退出维护」（`PATCH` `status: 1`）。

### 5.2 列表页

- 列：名称（主机名 + 可复制的 UUID）、状态、CPU、内存、磁盘、可用区、操作。磁盘条按**最满的那个存储池**（`disk_max_usage_ratio`）着色，容量是所有池之和、不乘超分比例
- 服务端分页 + 搜索（`q`）
- 「部署节点」弹窗 → `POST /hypers`：`ip`、`hostname`、`network_device`、`vlan_device`、`private_vlan_device`、`dns_server`、`domain`、`zone_name`、`virt_type`。返回的 `deploy_command` 显示给管理员，在节点上以 root 执行
- 行操作菜单：
  - **编辑** → `PATCH /hypers/{uuid}`，只发改了的字段：状态（启用 / 禁用）、可用区（`zone_id` 是 UUID，不能清空）、CPU / 内存 / 磁盘超分比例、备注
  - **进入维护**（仅活跃）→ `POST /hypers/{uuid}/maintain`：`migrate`、`target_hyper`（-1 或省略由调度选）。返回每台云服务器的结果，迁不走的（`not_doing`）只提示数量，原因在详情页
  - **退出维护**（仅维护中）
  - **终端** → 见 §5.4
  - **删除** → `DELETE /hypers/{uuid}`。离线节点不能重新部署，只能删掉重新注册（待办 B4）

### 5.3 详情页

- 标题区操作：进入维护（活跃时）、**疏散**（离线时，`HostEvacuateModal` → `POST /hypers/{uuid}/evacuate`：`target_hyper`、`confirm_fenced`（已确认断电，不等 5 分钟宽限）、`instances`；结果列在概览里）、编辑、终端
- 标签页：
  - **概览**：资源与超分、这台节点上的云服务器（`GET /instances?hyper=<hostid>`）、各存储池的容量摘要；节点离线、有存储隔离（`fences`）或待补清理（`storage_cleanups`）时显示恢复卡片 `HostRecoveryCard`，可「解除隔离」或「只删记录」（`POST /hypers/{uuid}/unfence {forget}`）
  - **存储**（`HostStorageTab`）：磁盘清单（`GET /hypers/{uuid}/disks`，「扫描磁盘」`POST .../disks/scan` 异步约 10 秒，改介质 `PATCH .../disks/{id}`），本机存储池（建池、扩容、换盘、维护、声明丢失 / 恢复、接管、删除、统计占用，都在 `/hypers/{uuid}/storage_pools/...` 下）。磁盘清单是上一次扫描的快照，建删池后不自动重扫
  - **监控**：`POST /metrics/hypers/{cpu,memory}/his_data`（按主机名）

### 5.4 计算节点终端

- 菜单项按系统设置 `HOST_CONSOLE_ENABLED` 置灰（默认关），节点状态须为 0 / 1 / 2
- 新窗口打开 `/host-console/:uuid`，先不带密码请求 `POST /hypers/{uuid}/console`；网关返回「Password is required」才显示密码框（是否要密码由 `HOST_CONSOLE_REQUIRE_PASSWORD` 决定）。不带 `error_code` 的 403 / 429 当作密码错误或限流留在密码框，clapi 的 403（开关关闭等）显示错误页
- token 2 分钟、一次性；重新连接要重新输密码。链路与安全措施见 `CLAUDE.md`「计算节点终端」

## 6. 热迁移

- **页面**：`MigrationList.vue`（列表 + 创建弹窗）、`MigrationDetail.vue`
- **接口**：转发到 clapi（`api/migrations.ts`），全部要求系统管理员（2026-09-30 起，之前列表对所有成员开放，待办 A10）

| 接口                                      | 界面                                                                                                                                    |
| ----------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /migrations`                       | 列表：名称、云服务器、类型、状态、进度、发起人（`creater_name`，创建时快照）；服务端分页搜索；有进行中的迁移时每 5 秒静默刷新         |
| `GET /migrations/{id}`                  | 详情：源 / 目标节点、阶段列表（含失败原因 `message`）、进度条（已传输 / 总量）、磁盘计划 `disk_plan`、放置组警告；进行中每 3 秒刷新 |
| `GET /instances/{id}/migration_targets` | 创建弹窗选中一台云服务器后，列出可选目标节点、不可用原因、每块盘可选的目标池                                                            |
| `POST /migrations`                      | 创建，返回数组（每台云服务器一条）                                                                                                      |

创建弹窗的规则：

- **没有「热迁移 / 冷迁移」选择**：热 / 冷由后端按云服务器当前状态决定。`force` 只在源节点离线时由界面自动带上（等于疏散，要求盘在共享池；可勾「已确认断电」= `confirm_fenced`）。2026-09-21 删掉了把「冷迁移」映射成 `force=true` 的下拉
- 目标节点可选，省略由调度器选（记录里 `target_hyper` 先是 -1）
- 指定目标且只有一台云服务器时可以逐盘选目标池（`disks`）；`allow_pool_fallback` 默认开
- `ignore_capacity`、`ignore_placement` 只有系统管理员可勾，会进审计
- 选中**严格集中放置组**的成员时，自动带上同一节点上的其他成员一起提交（后端要求整组同一次请求迁）

## 7. 节点告警

- **页面**：`AlarmList.vue`、`AlarmDetail.vue`（`/dashboard/alarms/:id`，用 `GET /node-alarm-rules?uuid=` 取单条）
- **接口**：转发到 clapi（`api/alarms.ts`），全部要求系统管理员

| 接口                                | 界面                                                                                 |
| ----------------------------------- | ------------------------------------------------------------------------------------ |
| `GET /node-alarm-rules`           | 列表：名称、规则类型、描述、启用状态；按 `rule_type` 筛选                          |
| `POST /node-alarm-rules`          | 创建：选规则类型后按 `CONFIG_TEMPLATES` 预填配置；**每种类型全局只能有一条** |
| `PATCH /node-alarm-rules/{uuid}`  | 编辑名称、描述、配置、启用（规则类型不可改）                                         |
| `DELETE /node-alarm-rules/{uuid}` | 删除确认，同时删掉生成的 Prometheus 规则文件                                         |

规则类型（`RULE_TYPES`，文案在 i18n `dashboard.alarmRuleTypes.<值>`）：`node_available`、`control_node`、`compute_node`、`hypervisor_vcpu`、`packet_drop`、`ip_block`、`ipgroup_available_ip`、`local_pool`。各类型的配置键必须和告警模板变量名一致，清单见 `CLAUDE.md`「告警」与 [P11](operations-behavior.md)。

原来的「同步映射」按钮已删除（映射由 clapi 后台每 10 分钟对账）；接口 `POST /metrics/alarm/sync-mappings` 还在，只作排障用。节点告警规则页没有「绑定通知渠道」入口（待办 B17）。

## 8. 其他仅管理员可见的页面与元素

| 位置                                  | 内容                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 存储池 / 存储集群 / 软件包 / 存储任务 | `StoragePools.vue`、`StoragePoolDetail.vue`、`StorageClusters.vue`、`StorageClusterCreate.vue`（创建向导整页）、`StorageClusterDetail.vue`、`StorageTaskDetail.vue`；软件包是存储集群列表页的第三个标签（`?tab=packages`，组件 `components/storage/StoragePackageList.vue`；旧地址 `/dashboard/storage-packages` 重定向过去），创建向导的软件步骤和升级弹窗里可以就地上传安装包、接受许可证（`composables/useStoragePackageFlow.ts`），不必离开当前页面；接口 `/storage_pools*`、`/storage_clusters*`、`/storage_packages*`、`/storage_tasks*`、`/storage_backends`（`api/storagePools.ts`、`storageClusters.ts`、`storagePackages.ts`）。存储池的列表与详情所有成员都能读（建卷要选池），写接口与侧边栏入口只给系统管理员 |
| 组织                                  | `OrgList.vue`、`OrgDetail.vue`：组织列表、成员、配额                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| 设置                                  | `SystemSettings.vue`：系统设置按分组编辑，只提交改动的项；改 `HOST_CONSOLE_REQUIRE_PASSWORD` 要输入密码                                                                                                                                                                                                                                                                                                                                                                                 |
| 普通页面里的管理员元素                | 创建云服务器时可指定宿主机（`hypervisor`）；镜像的公共 / 私有与存储池副本；VPN、放置组等详情页的节点信息；操作动态里管理员专属资源类型的条目（`ActivityText.vue`）                                                                                                                                                                                                                                                                                                                      |

## 9. 与原方案（2026-03）的差别

| 原方案                                                           | 现在                                                                                              |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| 「Administration」分组 5 项                                      | 仍是 5 项（区域、可用区、计算节点、热迁移、节点告警），另有「存储」分组和「设置」「组织」入口     |
| 后端用 `get_current_superuser` 依赖项校验                      | Python 网关已换成 Go 的 cpgateway：`Superuser()` 中间件 + 白名单 `Admin` 标记，clapi 再查一次 |
| 迁移弹窗选「热 / 冷迁移」                                        | 没有这个选择，热 / 冷由后端定；`force` 只表示源节点离线时的疏散                                 |
| 节点告警页要重构为 `NodeAlarmRuleList.vue`、有「同步映射」按钮 | 页面仍是 `AlarmList.vue`，已能建 / 改 / 删；「同步映射」按钮删除                                |
| 节点告警没有 PATCH                                               | 有 `PATCH /node-alarm-rules/{uuid}`                                                             |
| 计算节点删除时「清理 SCI 注册」                                  | SCI 已删除；删节点时 clapi 检查存储集群成员，cloudlet 收到 Shutdown 后退出                        |
| 计算节点没有终端、存储、疏散                                     | 有终端（§5.4）、存储标签、离线疏散与隔离恢复（§5.3）                                            |
| 列表前端过滤                                                     | 区域、可用区、计算节点、迁移都是服务端分页 + 搜索                                                 |
| 国际化只有 en / zh                                               | 三种语言：`en.ts`、`zh.ts`、`zh-TW.ts`                                                      |
