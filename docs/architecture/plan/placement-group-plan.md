# 放置组设计（分散 / 集中）

- **状态**：**2026-09-27 已实施；2026-09-28 后端已部署到 work-01，三节点端到端 TC-18 两轮合计 20 条通过**（实施记录、与本文不同的做法、测试结果见附录 E）。2026-09-24 经一轮代码核对复查，改了 12 处（附录 C）；2026-09-27 第二轮复查，改了 15 处（附录 D）
- **日期**：2026-09-24，基于 `stage-01` 分支 `a6336e10`（文中行号以此为准；2026-09-27 核对时，文中引用的文件在 `0795dacc` 上都没变过）
- **涉及**：clapi（`api/`）、cpgateway（代理白名单）、前端（`web/`）。**不涉及** cland-go、cloudlet-go、节点脚本
- **范围**：spread（分散）与 pack（集中）两种策略，各带「严格 / 尽量」开关。partition（分区）不做，理由见 §1.3

---

## 0. 摘要

- 用户建一个**放置组**（策略 + 严格程度 + 可用区），创建云服务器时选它。之后这台云服务器在**创建、迁移、维护腾空**这三条会决定位置的路径上都受组的约束。
- **组内成员由 clapi 自己选节点，cland 只复核资源**。原因是 clapi 下发 `select=<候选组>` 后不知道 cland 挑了哪台，要等节点回调 `launch_vm` 才知道。镜像要下载，这可能是几分钟之后；这段时间里同组的下一台无从避让。所以组内成员一律由 clapi 算出单台节点，用 `select=group-zone-<z>:<节点>` 下发，cland 仍按 CPU / 内存 / 磁盘复核。本地存储池的系统盘（`bootHost`）已经在走这条路。
- **成员占用哪台节点**看三处：`instances.hyper`（已落地）、新增的 `instances.placement_hyper`（已选定、还在创建中）、进行中迁移的 `target_hyper`（迁移期间源和目标都算占用）。卡住的创建和迁移记录**继续算占用**，在组详情里标出来，由人处理（§2.2）。
- **并发靠锁放置组那一行**（`SELECT ... FOR UPDATE`），创建、迁移、删除组都先锁组再算。同一个组的放置决策串行，不同组互不影响。
- **资源视图滞后是分钟级的**：新虚拟机要等镜像下载、转换完、`virsh define` 之后才被节点的资源统计计入。所以放置时把**本组在途成员的需求**先计入，否则集中组的连续请求会把锚点节点压超（§3.3）。
- **平台做决策时不破坏严格约束**：放不下就报错（创建整批回滚，维护腾空把这台记为 `not_doing`）。只有系统管理员在迁移时显式带 `ignore_placement` 才能破坏。尽量约束放不下时放宽。例外只有一种：严格集中组整组迁移时，各成员的迁移是互相独立的异步流程，做不成事务，其中一台失败回滚时组会被拆开（§2.3）。
- **严格集中组整组迁移先整组检查**：第一台建迁移之前，就确认锚点接得住全部随行成员（规格可以各不相同，磁盘可以分在不同的池），有一台接不住就整组不动（§6.3）。
- **不自动纠正**：组「不合规」时（尽量约束放宽过、管理员破坏过、整组迁移部分失败）只在界面上标出来，平台不会自动迁移去修。
- **不加入组的云服务器行为不变**：不选放置组就走原来的代码路径。`bootHost` 拆成两步时，非成员的节点选择结果要和现在一致（§6.2）。例外是顺带修的两个既有问题，对所有云服务器生效：创建命令下发失败时实例置为 `error`，迁移被 cland 拒绝时恢复原状态而不是停在 `rollback`（附录 B 第 2、4 条）。
- 改动集中在 clapi 的 `services/`：一个纯函数的放置器（输入候选节点、占用、需求，输出节点），加上创建、迁移、迁移目标列表三处调用，以及 `rpcs/migrate_vm.go` 的一处回调。节点侧零改动。

---

## 1. 背景与目标

### 1.1 现状

决定云服务器落在哪台节点的只有这几条路径：

| 路径 | 位置 | 谁选节点 |
|---|---|---|
| 创建（系统盘在内置池） | `services/instance.go:173-183`、`:268` | cland：`select=` + `instanceHyperGroup` 算出的候选组（本可用区、活动、内置池占用 < 90%） |
| 创建（系统盘在其他本地池） | `instance.go:269-278`，`services/instance_placement.go:75` `bootHost` | clapi：可用空间最大的一台，下发单节点的 `select=` |
| 创建（管理员指定宿主机） | 内置池 `instance.go:279-286`；其他本地池 `bootHost` 的 `hyperID >= 0` 分支（`instance_placement.go:76-81`） | 管理员。内置池用 `inter=`，cland 不检查资源；其他本地池经 `admitLocked` 校验后下发单节点 `select=`，cland 复核 |
| 迁移（不指定目标） | `services/migration.go:155-174` | cland：`select=` + `migrationCandidates` |
| 迁移（指定目标） | `migration.go:150-154` | 管理员；`inter=` |
| 维护模式腾空 | `services/hyper.go:535-551` | 同迁移，逐台建迁移 |

其余操作（调整规格、重装、救援、开机补启动、节点重启恢复）都在原节点上做，不换节点。

cland 的调度器（`cland/scheduler.go:101` `GetBestNode`）是**全局一个计数器的轮询**，第一台资源够的就选中。镜像下载、负载均衡、迁移等所有 `select=` 共用这个计数器，所以"同一批依次落到不同节点"只是碰巧，没有保证。

还有一点决定了本方案的做法：**clapi 下发 `select=` 后不知道 cland 选了哪台**。cland 不回报；要等节点执行完 `launch_vm.sh` 回调，clapi 才写 `instances.hyper`（`rpcs/launch_vm.go:107-120`），此前一直是 -1。迁移不指定目标时也一样，要等 `target_prepared` 回调。

### 1.2 目标

1. 用户能建放置组，选策略（分散 / 集中）和严格程度，绑定一个可用区。
2. 创建云服务器（含批量，最多 16 台）时可以选放置组，平台按组的规则选节点。
3. 迁移和维护腾空也遵守组的规则；严格约束满足不了就拒绝，并给出原因。
4. 用户能看到组内成员是否满足规则，普通用户看不到节点名。
5. 并发创建、批量创建不会打破严格约束。

### 1.3 非目标

- **partition（分区）**：CloudLand 没有机架、电源这类故障域概念，分区只能等于"一组节点"。在几台到十几台节点的规模下，它比尽量分散多不出东西；它唯一独有的"告诉虚拟机自己在哪个分区"也没人提过需求。将来有了故障域标签再加，`policy` 是枚举，加一种不改表。
- **已有云服务器加入 / 退出放置组**：只在创建时加入，随删除退出。加入要校验现位置，放到以后（§12 第 4 条）。
- **自动纠正**：不合规时不自动迁移。
- **跨可用区的组**。
- **修改组的策略、严格程度、可用区**：建好后只能改名称和描述，要改就新建一个组。

---

## 2. 概念与规则

### 2.1 策略 × 严格程度

| | 严格（`strict=true`） | 尽量（`strict=false`） |
|---|---|---|
| **spread 分散** | 组内任意两台不在同一节点。没有空节点就报错 | 放到组内成员最少的节点；成员最少的有多台时，挑资源最宽裕的 |
| **pack 集中** | 放到组内成员所在的那台，那台放不下就报错。组还没有成员时，挑一台能放下这一整批的 | 优先放成员最多且放得下的节点，都放不下就挑资源最宽裕的一台 |

界面默认：spread 严格、pack 尽量。严格 pack 很容易被单台节点的容量卡住，一台满了整组就建不出来。

### 2.2 成员占用的节点

组 G 的占用表 `occ[节点] = 成员数`，由下面三类记录累加。一台成员可能同时占两台节点：

1. 成员的 `instances.hyper >= 0`：已经落在这台。
2. 成员的 `hyper = -1` 且 `status in (provisioning, deleting)`：取 `instances.placement_hyper`，表示已经选定、正在创建。
3. 成员进行中的迁移（`migrations.status in (in_progress, target_prepared, source_prepared)`、`target_hyper >= 0`）：目标节点也算占用。源节点仍由第 1 类占着，直到 completed 回调把 `hyper` 改成目标。不看 `updated_at`，理由见下面第一条注。

不算占用的：已软删的成员；`hyper = -1` 且状态不在上面两种里的（典型是 cland 拒绝后的 `error`，从来没落地）。已落地的成员删除中（`deleting` 且 `hyper >= 0`）仍按第 1 类占用，直到记录被删除。这样偏保守，但不会出错。

> **卡住的记录继续算占用**。两类都会卡住：
> - 第 2 类：下发失败（附录 B 第 4 条修掉最常见的这一种）；`launch_vm.sh` 在走到 `disk_fail` 之前 `die`，不回调（`launch_vm.sh:39`）；cloudlet 在命令排队时重启，命令丢失；创建中目标节点被删除（`HyperAdmin.Delete` 只看 `hyper`，不看 `placement_hyper`）。
> - 第 3 类：2026-09-16 那种停在 source_prepared 的迁移记录。虚拟机其实已经在目标节点运行，`instances.hyper` 却还是源节点，心跳也不会纠正（CLAUDE.md「迁移卡死后心跳不会纠正」）。
>
> 不能按时间放弃它们。初稿给第 3 类定过 24 小时时限，可过了 24 小时目标节点在占用表里就是空的，严格分散组可能把另一台成员放到那台真正运行着成员的节点上：约束被平台自己破坏了，而且没有任何提示。继续计入的代价是组可能一直报"没有空节点"，所以组详情要把这类成员标出来（§7.1 的 `stale_provisioning`、`stale_migration`），409 的消息也要写明被哪几台占着。卡住的创建由用户删掉那台实例即可释放；卡住的迁移本来就要手工恢复，恢复后自然释放。要不要改回"超过 24 小时不计"，见 §12 第 6 条。

> **`deleting` 只在最初几秒起作用**。创建中被删除时，删除命令用 `toall=` 广播（`instance.go:1254-1256`）。没有这台虚拟机的节点几秒内就执行完 `clear_vm.sh` 并回调，而 `ClearVM` 在第一条回调到达时就软删记录（`rpcs/clear_vm.go:199`）。真正在建的那台节点上 cloudlet 串行执行，要等创建做完才轮到删除。所以记录软删之后，那台虚拟机还会在节点上存在一段时间（建完随即被销毁），这段时间里它不再计入占用。影响只是新成员可能和一台即将被销毁的虚拟机短暂同机，可以接受。改用 `inter=<placement_hyper>` 删除，能让记录一直留到那台节点真正删完；但那台节点离线时删除会一直卡住，不值得。

> 为什么要第 2、3 类：创建和迁移都是异步的，镜像下载、磁盘复制可能持续几分钟。只看 `hyper` 的话，这段时间里同组的下一台看到的是一张"空"的占用表，并发请求会打破严格分散。

### 2.3 合规判定

按成员的**当前位置**（上面第 1、2 类，不含迁移目标）算，和 §2.2 用同一套判断：

- spread：没有任何一台节点上有 2 个及以上成员。
- pack：成员分布在不超过 1 台节点上。
- 没有位置的成员（`error` 且 `hyper = -1`）不参与判定。

组变得不合规有五种来源：

1. 尽量约束放宽过；
2. 管理员带 `ignore_placement` 迁移过；
3. 尽量组被管理员指定了宿主机；
4. **严格集中组整组迁移时部分成员没迁成**：§6.3 在第一台建迁移之前做整组检查，但之后各成员的迁移是互相独立的异步流程。某一台 `virsh migrate` 失败回滚，或者在第一台下发之后才被 cland 复核拒绝（期间锚点的资源被组外的创建占走），其他台照样迁完，组就分在了两台节点上。这做不成事务；
5. 心跳 `inst_status` 按虚拟机实际所在的节点改写了 `instances.hyper`。它反映的是真实位置，不是一次放置决策。

平台做放置决策时不会让严格组变得不合规；第 4 种是决策之后执行失败的结果。组详情的不合规说明里，能查到原因的就写出原因：成员最近一次迁移失败了（第 4 种），或者那次迁移带了 `ignore_placement`（第 2 种，记在迁移记录上，§4.3）。界面见 §8。

---

## 3. 关键设计决策

### 3.1 组内成员由 clapi 选节点

可选的做法有三种：

| 做法 | 问题 |
|---|---|
| A. clapi 从候选组里去掉不满足规则的节点，仍交给 cland 选 | cland 选了哪台，clapi 要等回调才知道。同一批的第 2 台、并发的另一个请求都看不到第 1 台的去向，严格分散必然会被打破。批量集中更做不到：N 台要落同一台，而候选组里有多台时 cland 会分开放 |
| B. 改 cland，让它支持组语义或回报选中的节点 | 要改 gRPC 协议、cland 调度器和 clapi 的下发链路；cland 没有数据库，重启即丢状态。为这个功能不值得 |
| **C. clapi 自己算出一台，下发单节点的 `select=`**（采用） | cland 没得选，只做资源复核。复核不过就按现有路径回调 `error=resource`，这台创建失败，**不会自动换一台重试** |

C 的先例是本地池系统盘的 `bootHost`（`instance_placement.go:75`）：同样是 clapi 选节点、下发单节点 `select=`、cland 复核。

C 的代价是**放弃了 cland"换一台"的能力**，实际影响很小。clapi 选节点时已经用 `resources` 表检查过资源；这张表由节点心跳经 `hyper_status.sh` 上报（`report_rc.sh:458-466`，心跳 1–20 秒一次，资源一变就报）。cland 的调度表来自同一份心跳数据（`report_rc.sh` 的首行），两边看到的基本一致，cland 复核很少能拦下 clapi 放行的请求。真被拦下时，用户看到的就是现在已有的"Resource is not enough"，删掉重建即可。要不要做自动重试见 §12 第 1 条。

**两边的资源视图都滞后，而且是分钟级的**：节点只统计已经 `virsh define` 的虚拟机（`report_rc.sh:410-416` 按 XML 累加 vCPU），而 `launch_vm.sh` 要先下载镜像、转换、调整大小，最后才 define（`launch_vm.sh:56-128`）。cloudlet 默认串行执行命令（`CLOUDLET_CONCURRENCY=1`），同一台节点上排队的创建还要再等前面的做完。这几分钟里，已下发的虚拟机在 clapi 和 cland 看来都不存在。所以真正的风险不是"cland 拒绝"，而是**超配两边都拦不住**。本方案在同组范围内补上这一点（§3.3），跨组的仍是既有问题（附录 B 第 1 条）。

### 3.2 并发：锁放置组那一行

- **创建**：在 `InstanceAdmin.Create` 的事务里，选节点之前先执行 `SELECT ... FROM placement_groups WHERE id = ? FOR UPDATE`。成员行（带 `placement_hyper`）在同一事务里写入。事务提交后才下发命令（`instance.go:107-115` 的 defer 本来就是这个顺序），所以下一个拿到锁的请求一定看得到它们。
- **迁移**：`createOne` 的事务（`migration.go:110`）里同样先锁组。维护腾空是逐台建迁移、每台一个事务，天然串行。
- **删除组**：同样先锁组，在同一事务里数成员、软删、改名。先数再删而不加锁的话，并发的创建可能在两步之间加进成员，留下指向已删除组的成员。
- **写法**：照 `services/loadbalancer.go:173` 锁路由器行的做法，`db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(group)`。GORM 会自动加 `deleted_at IS NULL`，组已被删时拿不到行，按组不存在报错。
- **锁顺序**：一律先锁放置组，再做存储池准入（`admitLocked` 会锁 `hyper_storage_pools` 行）。所有路径同一顺序，不会死锁。创建时要注意：内置池 + 管理员指定宿主机的 `admitLocked` 在循环之前（`instance.go:176`），锁组必须放在它前面。
- 不同组之间不互斥，没有全局锁。

### 3.3 资源视图与同批累加

`resources` 表不包含还没 define 的虚拟机（§3.1）。放置器在内存里维护 `pending[节点] = (CPU, 内存, 内置池磁盘)`，判断"放得下"时用"可用 − pending"。判断式和 cland 一致（`scheduler.go:81` `testResource`：需求不超过可用即通过）。

`pending` 有三个来源：

1. **初值：本组在途成员的需求**。§2.2 的第 2 类（还没落地的成员，按 `placement_hyper`）和第 3 类（迁移目标还没落地的，按 `target_hyper`），按各自的 CPU、内存和内置池磁盘累加。这两类记录算占用表时本来就要查，不增加查询。没有这一步的话，集中组隔一分钟再建一批，锚点节点看起来还是空的，会被压超。
2. **初值：刚落地、节点还没再上报资源的成员**。`launch_vm` 回调写入 `hyper` 后，成员就从第 2 类变成第 1 类，不再计入上一条；但 `resources` 要等下一次 `hyper_status` 才计入它（心跳 1–20 秒一次，且只在资源变化时上报，`report_rc.sh:458-466`）。所以第 1 类成员里，`instances.updated_at` 晚于所在节点 `resources.updated_at` 的也计入。这两列算占用表和构造 `HostSlot` 时本来就读，不增加查询。两种情况会多算：`instances.updated_at` 被别的更新刷新过；节点在回调之前就已经把它算进去了（`calc_resource` 按 XML 文件累加，XML 在 `virsh define` 之前就写好）。两种都只多算到下一次资源上报为止，偏保守。
3. **本次请求已经放下的成员**：每放一台就加上。

只计本组的在途成员：放置组的锁只保护本组，别的组和非组成员的在途创建 clapi 在它们落地前不知道位置（cland 选的），也无从计入。这是现有问题，cland 自己的表同样不扣减。放置组不让它更坏，也不在这里修（附录 B 第 1 条）。

系统盘在其他本地池时，池容量不走 `pending`：每台之后的 `admitLocked` + `reserve` 写在同一事务里，下一台的准入看得到（§5）。

### 3.4 候选节点沿用现有过滤，放置组只管最后一步

放置器的输入是**现有逻辑已经算好的候选节点**，它只负责"在这些里挑哪台"：

- 系统盘在内置池：`instanceHyperGroup` 的结果（活动、本可用区、内置池占用 < 90%）。
- 系统盘在其他本地池：`bootHost` 里通过 `admit` 的那些节点。
- 迁移：`migrationCandidates` 的 g1 和 g2（磁盘计划已经可行），各自按放置规则过滤后 g1 优先（§6.3）。严格集中组整组迁移时，还要去掉接不住其他随行成员的节点（§6.3 的整组检查）。
- 管理员指定宿主机或迁移目标：只有那一台，放置器只做校验。

这样存储池准入、节点状态、可用区这些规则不用在放置组里再写一遍。

### 3.5 组绑定可用区

组建立时选定可用区，成员必须建在这个可用区。迁移本来就不跨可用区（`migrationCandidates` 按 `instance.ZoneID` 过滤），所以之后也不会跑出去。

不绑定也能工作，分散跨可用区天然成立。但集中跨可用区不可能满足，界面上还得解释"为什么这个组有时能选、有时不能"。绑定最简单。

---

## 4. 数据模型

### 4.1 新表 `placement_groups`

```go
type PlacementGroup struct {
	Model
	Owner       int64  `gorm:"uniqueIndex:idx_owner_placement_group"` // organization ID
	Name        string `gorm:"uniqueIndex:idx_owner_placement_group;type:varchar(64)"`
	Description string `gorm:"type:varchar(256)"`
	Policy      string `gorm:"type:varchar(16)"` // spread | pack
	Strict      bool   // no default tag: false is a real choice and GORM would replace it with the column default
	ZoneID      int64
	Zone        *Zone `gorm:"foreignkey:ZoneID"`
}
```

- `Strict` **不能带 `gorm:"default:..."`**：false 有业务含义，GORM 会把显式的零值换成列默认值（CLAUDE.md「零值有业务含义的字段」）。
- 名称在组织内唯一。删除按既有约定先软删、再 `Unscoped` 改名释放名称（PET-1228，参照 `services/key.go:146`）。
- 每个组织（在每个区域）最多 50 个组，是 clapi 的常量，超出返回 409。不进 cpgateway 配额：组本身不占资源，而新增配额项要改十几处（CLAUDE.md「新增可计费资源要同时改」），不值得。

### 4.2 `instances` 加两列

```go
PlacementGroupID int64 `gorm:"index"` // 0: not in a group
PlacementHyper   int32 // host clapi chose for a member; only read while hyper is -1 and status is provisioning or deleting
```

- `PlacementHyper` 同样不带 default 标签。只对组成员有意义，非成员的值不读。
- 不在创建时直接写 `instances.hyper`：很多地方把 `hyper >= 0` 当作"已经落地"，包括心跳 `inst_status`、`GET /instances?hyper=`、维护腾空按 `hyper` 找虚拟机、迁移取源节点。提前写会让这些逻辑把还没建出来的虚拟机当成真的。
- 产品未上线，AutoMigrate 加列即可，不需要迁移脚本。

### 4.3 `migrations` 加两列

```go
IgnorePlacement bool   // no default tag, like IgnoreCapacity
PriorStatus     string `gorm:"type:varchar(32)"` // instance status before the migration, restored when cland rejects it
```

- `IgnorePlacement`：请求里的 `ignore_placement`，和 `IgnoreCapacity` 一样存下来。组详情据此说明不合规是哪次迁移造成的（§2.3 第 2 种）。
- `PriorStatus`：`createOne` 把实例改成 `migrating` 之前的状态。cland 复核拒绝时，回调把实例恢复成这个状态，而不是写 `rollback`（附录 B 第 2 条，这次一起修）。所有迁移都写，不只是组成员的。

---

## 5. 放置器

放置器是一个纯函数，不查库，方便表驱动测试：

```go
type HostSlot struct {
	Hostid        int32
	FreeCpu       int64
	FreeMemKiB    int64
	FreeDiskBytes int64 // room of the boot disk's pool on this host, see below
}

type Demand struct{ Cpu, MemKiB, DiskBytes int64 }

// Place picks one host for one instance. occ is the member count per host (§2.2); pending is the demand counted on
// top of the reported resources (§3.3); need is the demand of this instance; rest is the summed demand of every
// instance that still has to go with this one, this one included: the rest of a creation batch (need times the
// count left), or the members of a strict pack group moving together, whose flavors may differ (§6.3). It returns
// an error when a strict group can not be satisfied.
func Place(g *model.PlacementGroup, candidates []*HostSlot, occ map[int32]int, pending map[int32]Demand,
	need, rest Demand) (hostid int32, err error)
```

`fits(h, d)` 表示 h 的"可用 − pending"放得下需求 d（三维都不超过）。`rest` 用需求之和而不是台数：创建时同一批规格相同，乘以台数就行；迁移时随行成员的规格可以各不相同，只能求和。`score(h)` 用 cland 的公式 `(1−cpu/可用cpu)(1−mem/可用mem)(1−disk/可用disk)`，越大越宽裕；平局取 hostid 小的。

`HostSlot` 由调用方构造，磁盘这一维按系统盘所在的池取值：

| 系统盘所在 | `FreeDiskBytes` | `pending` 的磁盘初值 |
|---|---|---|
| 内置池 | `resources.disk`（与 cland 调度用的同一口径） | 本组在途成员里系统盘在内置池的那些 |
| 其他本地池 | 该池在这台节点上还能准入的量，按 `admit`（`services/storage_admission.go:134`）的规则算：`capacityLimit − allocatedBytes`；占用率 ≥ 90% 或池上有因写满暂停的虚拟机时为 0 | 0：在途成员的系统盘在创建时就写了预留，`allocatedBytes` 已经算进去了 |

这样严格集中建第一批时，`fits(h, rest)` 同时检查池放不放得下这一整批盘，不会挑中一台 CPU 和内存够、池却放不下的节点而整批回滚。`Place` 选出节点之后，调用方照旧 `admitLocked` + `reserve`，那才是池容量的权威检查。原来 `bootHost` 挑"池物理剩余空间（`AvailBytes`）最大"的节点，现在评分的磁盘项用可准入量，倾向基本一致。

迁移时 `HostSlot` 的磁盘这一维只取内置池（和 cland 的 `disk=` 口径一致），`need` 的磁盘项是 `builtinDiskGB`。其他本地池的盘由 `migrationCandidates` 逐台检查；严格集中组整组迁移时，再由 §6.3 的整组检查按池求和。

组成员的放置跳过没有 `resources` 行的节点（刚注册、还没上报过），与 cland 一致（`cland/scheduler.go:116-118`）。非成员的 `bootHost` 现在不跳过这类节点（`instance_placement.go:95` 只在有资源行时比较），这一点不改（§6.2）。

**spread**

```
fit = candidates where fits(h, need)
strict: pick = argmax score over { h in fit : occ[h] == 0 }
        none -> ErrPlacementGroupNoHost
soft:   m = min occ[h] over fit
        pick = argmax score over { h in fit : occ[h] == m }
        fit empty -> ErrNoQualifiedHypervisor (same as today)
```

**pack**

```
anchor = hosts holding the most members (empty when the group has none)
strict: if anchor not empty:
            pick = the anchor with the lowest hostid; it must be a candidate and fits(pick, rest), else ErrPlacementGroupHostFull
        else:
            pick = argmax score over { h : fits(h, rest) }; none -> ErrPlacementGroupHostFull
soft:   if every occ is 0:
            pick = argmax score over { h : fits(h, rest) }
            none fits the whole batch -> pick = argmax score over { h : fits(h, need) }  (the batch spills over)
        otherwise order { h : fits(h, need) } by (occ desc, score desc) and take the first
        none -> ErrNoQualifiedHypervisor
```

选中后，调用方 `occ[pick]++`、`pending[pick] += need`、`rest -= need`，再放下一台。

三点说明：

- **严格集中在组已不合规时**（成员分在几台上，只可能是管理员破坏过或整组迁移部分失败，§2.3）选成员最多的那台，不报错。这不会让情况更坏。
- **严格集中一律要求目标放得下剩下的整批**（`rest`），不管锚点是已有的还是新选的。否则前几台放进去、后几台放不下：创建是整批回滚、白选一轮；迁移则是前几台已经迁走、后几台留在原地，组被拆开。
- **尽量集中的一整批没有一台放得下时**，第一台落在最宽裕的节点，后面几台按"成员多的优先"跟上去，放不下再换一台。结果是尽量少地分在几台上。

---

## 6. 各操作流程

### 6.1 放置组本身

- **创建**：校验名称、策略枚举、可用区存在；组织内名称不重复；不超过 50 个。
- **修改**：只能改 `name`、`description`。请求里带了 `policy`、`strict` 或 `zone` 时返回 400，不静默忽略。
- **删除**：锁组（§3.2）后数成员。组内还有成员（包括删除中的）返回 409 `ErrPlacementGroupInUse`，提示先删成员；空组在同一事务里软删后改名。

### 6.2 创建云服务器

`POST /instances` 请求体加可选的 `placement_group: {id}`。`InstanceAdmin.Create`（`instance.go:91`）里的处理：

1. **取组**：组必须属于当前组织，系统管理员也一样（成员与组必须同组织）。组的可用区必须等于请求的可用区，否则 400。
2. **锁组**：在事务里锁组，读出占用表 `occ` 和 `pending` 的初值（§3.3）。锁组要放在现有的 `admitLocked`（`instance.go:176`）之前（§3.2）。严格集中组有迁移在途（§2.2 第 3 类非空）时返回 409"放置组正在迁移，请稍后再试"：锚点此刻在源和目标之间，新成员放哪边都可能和整组的去向相反。卡住的迁移同样算在途，要等管理员恢复（§2.2），消息里写明是哪一条迁移。
3. **算候选**：按 §3.4 算出候选节点，预加载各节点的 `Resource`，转成 `HostSlot`。
4. **逐台放置**：循环里每台依次：
   1. 调 `Place(...)` 选出节点，`rest` 是 `need` × 剩余台数（含这台）。
   2. 写 `instances.placement_group_id` 和 `placement_hyper`。
   3. 控制串改为 `"select=" + hyperGroupOf(zoneID, []int32{pick}) + " " + schedulerResources(...)`。

   两种系统盘位置的区别：

   - **其他本地池**：原来 `bootHost` 选节点的那一步由 `Place` 替代，**之后的 `admitLocked` 和 `reserve` 照旧**，只是节点来自放置器。实施时 `bootHost` 没有拆，原样留给非成员；组成员的候选由 `startCreationPlacement` 另算（附录 E），非成员的结果自然和现在一样（PG-15）。
   - **内置池**：原来整批共用一个候选组，现在每台单独一个单节点组。cland 仍按 `disk=` 复核内置池。
5. **失败即整批回滚**：任何一台 `Place` 失败就返回错误，事务回滚，**整批一台都不建**。现有的 defer 就是这样，命令只在提交后下发。严格约束满足不了时，消息写明占着节点的是哪几台成员，其中在途和卡住的（§2.2）单独点出来，否则用户数一数成员会觉得对不上。

**管理员指定宿主机**（`hypervisor`）时，候选只有这一台：

- 严格组按规则校验，不满足返回 400。严格分散 + 指定宿主机 + `count > 1` 必然不满足，直接 400。
- 尽量组放行。管理员的明确意图优先，组可能因此不合规。响应不带警告：`POST /instances` 返回的是云服务器数组，没有合适的位置放；指定宿主机本来就是有意为之，组详情会显示不合规。
- 控制串保持原样：内置池是 `inter=`（管理员指定时 cland 不复核是现状，不改）；其他本地池是 `bootHost` 经 `admitLocked` 校验后的单节点 `select=`（`instance.go:269-278`）。

不选放置组时，以上全部跳过，代码路径和现在一样。

### 6.3 迁移

`POST /migrations` 请求体加 `ignore_placement`（布尔，默认 false）。迁移接口本来就只允许系统管理员调用。`createOne`（`migration.go:104`）里只对组成员做下面的处理。

**不指定目标**（`tgtHyper = -1`）：

1. 锁组，读 `occ` 和 `pending` 初值，**扣掉迁移中的这台自己**（它在源节点的那一份）。
2. **候选**：`migrationCandidates` 返回的 g1（原池就放得下）和 g2（要换池）**先分别按放置规则过滤**，再按"g1 过滤后非空就用 g1，否则用 g2"取候选。顺序不能反：现在的代码是 g1 非空就只看 g1（`migration.go:157-162`），先定了 g1 再套规则的话，g1 的节点都已被本组成员占用、只有 g2 有空节点时，严格分散会误报"没有可用节点"。之后再按 `resources` 过滤。`IgnoreCapacity` 时也照样按 `resources` 过滤：它只跳过存储池的容量检查，cland 在选中的节点上照样复核 CPU、内存和内置池（初稿写的是「跳过这一步」，那样放置器会在一堆「无限大」的节点里挑编号最小的，满了也照选，附录 E.6）。
3. 用 `Place` 选出目标，之后**按指定了目标处理**：`migration.TargetHyper = 目标`，**并在同一事务里写回数据库**（`tx.Model(migration).Update("target_hyper", 目标)`），再调 `PlanMigrationTarget` 生成磁盘计划并准入。控制串用单节点 `select=`，不用 `inter=`，让 cland 复核 CPU 和内存。
   - 必须写回：迁移记录在 `migration.go:147` 已经按 -1 插入。只改内存，就是 2026-09-16"调度器自选目标的迁移永远卡在 source_prepared"那个 bug 的翻版；而且第 3 类占用读的是库里的 `target_hyper`，不写回的话，下一段说的"后建的迁移看得到先建的去向"也不成立。
4. 选不出来时：非批量直接返回错误；维护腾空（`batch=true`）记一条 `not_doing`，原因写"放置组 X（严格分散）没有可用的目标节点"。

这样成员的迁移在创建时就有确定的目标，§2.2 的第 3 类占用立即生效，维护腾空里后建的迁移看得到先建的去向。

**严格集中：只能整组一起走**。严格集中组的成员单独迁走，必然和留在源节点的其他成员分开。先定义**随行成员**：与这台在同一个源节点上、在同一次 `Create` 调用的实例列表里、并且能迁移的本组成员。"能迁移"指 `Create` 与 `createOne` 会放行的状态：running / shut_off / paused，且不是因存储写满而暂停（`migration.go:83-86`、`:107`）；本次调用里已经建了迁移的成员（状态已变成 migrating）也算。规则：

- **源节点上本组的成员全部是随行成员**：它们不参与定锚。组在别的节点上没有成员时，第一台选一台**接得住全部随行成员**的节点（见下面的整组检查）；后面的成员通过第 3 类占用看到这个锚点，跟过去。组在别的节点上已有成员（之前被拆开过，§2.3）时，锚点是成员最多的那台，同样要接得住全部随行成员。传给 `Place` 的 `rest` 是随行成员里还没建迁移的那些（含这台）的需求之和，每台按自己的规格和内置池磁盘算；已经建了迁移的那几台，需求在 `pending` 初值里（第 3 类）。
- **源节点上有本组成员不是随行成员**（没被一起请求、正在救援、处于 error、因存储写满暂停……）：本组在这个源节点上的成员一个都不迁。非批量返回 400："严格集中组 X 在节点 Y 上有成员不能一起迁移（成员 Z 状态为 rescuing）"；批量记 `not_doing`，原因相同。否则第一台迁走、剩下的留在原地，组就被平台自己拆开了。
- 维护腾空（`Maintain` 把源节点上的全部虚拟机放进同一次调用）和"管理员在一个请求里选中整组迁移"都走这条规则，不需要按源节点是否在维护中特判。

**整组检查**。每台随行成员的迁移在各自的 `createOne` 里建、建完立即下发（`migration.go:83-100`）。所以"锚点接不接得住"必须在第一台下发之前，对全部随行成员一起判断；不能等到每台自己的 `migrationCandidates` / `PlanMigrationTarget` 才发现，那时前几台已经在迁了。检查的内容：

1. **状态**：上面的随行成员规则。
2. **CPU、内存、内置池磁盘**：`Place` 的 `fits(T, rest)`，`rest` 按各台实际规格求和。
3. **其他本地池的磁盘**：对每台还没建迁移的随行成员跑 `planDisks(成员, T)`，必须成功（T 在它的 g1 或 g2 里）；再把各台计划按目标池求和，每个池用总量跑一次 `admit`（已建迁移的那几台，预留已经记在 `allocatedBytes` 里）。
4. 还没有锚点时，候选节点是"这台的 g1 / g2 按规则过滤"再去掉第 3 项不通过的节点，`Place` 在剩下的里面选。

实现：`Create` 把本次调用的实例 ID 集合传给 `createOne`，`createOne` 锁组之后对**全部还没建迁移的随行成员**做整组检查，而不只检查这一台。第一台的检查就覆盖了整组：有一台状态不对，或者锚点接不住哪一台，整组都不动。之后每台再检查一遍，是为了发现这期间的变化（例如组外的创建占走了锚点的资源）。这时前面几台已经发出，只能返回错误或记 `not_doing`，组被拆开，属于 §2.3 第 4 种。随行成员一般不超过十几台，每台对剩余成员各跑一次 `planDisks`，代价可以接受。

**指定目标**时，放置器只做校验：

- 严格组不满足返回 400，批量时记 `not_doing`。严格集中组还要通过上面的整组检查，目标就是锚点。
- 尽量组放行，`MigrationResponse` 上加 `placement_warning` 字段写明破坏了哪条规则。
- `ignore_placement = true` 跳过校验，包括整组检查。

`ignore_placement` 只对组成员有意义，非成员忽略。它不绕过容量检查，那是 `ignore_capacity` 的事。它和 `ignore_capacity` 一样存进迁移记录（§4.3）。

**cland 复核拒绝时**，把实例恢复成迁移前的状态（`migrations.prior_status`），迁移记录与阶段任务置为 failed，释放预留（附录 B 第 2 条）。实施时发现原先的判断错了：拒绝时 cland 回传的是原命令 `target_migration.sh`，而 rpcs 里**没有这个 handler**，回调被丢弃，迁移记录永远停在 `in_progress`、实例停在 `migrating`（`MigrateVM` 里写 `rollback` 的那段从来走不到）。现在加了 `target_migration` 的 handler（`rpcs/migrate_vm.go` 的 `TargetMigrationRefused`）。组成员的迁移改走单节点 `select=` 后被拒绝的次数会变多，而第 3 类占用不看时间，不修的话每次拒绝都会让目标节点被一条卡住的记录一直占着。

### 6.4 维护腾空

`HyperAdmin.Maintain`（`hyper.go:481`）本身不改。它把源节点上的全部虚拟机放进同一次 `migrationAdmin.Create(..., batch=true)` 调用，走 §6.3 的逻辑。结果里记为 `not_doing` 的就是被放置组拦下的：严格分散没有空节点，或者严格集中组没通过整组检查（有成员走不了，或者没有节点接得住整组）。管理员处理掉原因后重试，或者手工带 `ignore_placement` 迁移。

腾空时指定了统一目标（`targetHyper`）的情况：严格分散的成员如果目标上已有同组成员，这台记 `not_doing`。严格集中的成员全部去同一台；组在别的节点上还有成员、而目标又不是那台时，按 §6.3 记 `not_doing`。

### 6.5 其他操作

- **调整规格、重装、救援、开机补启动、节点重启恢复**：都在原节点做，不换位置，不查放置组。调整规格可能让严格集中组的那台节点更满，但那是容量问题，不是放置问题。
- **删除云服务器**：软删后自然不再计入占用，不需要额外处理。
- **删除节点**（`HyperAdmin.Delete`）：节点上还有实例（`hyper` 指向它）时拒绝删除（`hyper.go:579-586`），所以已落地的成员不会悬空。能悬空的只有第 2 类：创建中目标节点被删除，成员的 `placement_hyper` 指向一台不存在的节点，永远落不了地。它按卡住的创建处理（§2.2）：继续算占用，组详情标出，用户删掉即可。
- **节点被禁用或进入维护**：已有成员不动；新的放置按 `status = 1` 过滤，自然会避开它。严格集中组的锚点节点进入维护后，新成员建不出来（`ErrPlacementGroupHostFull`，消息写明锚点节点不可用），要等整组腾走或节点恢复。
- **解散组织**：clapi `services/org.go:475-488` 的 `resourceChecks` 加上 `PlacementGroup`，组织里还有放置组时拒绝解散，和密钥等其他资源一样。
- **删除可用区**：`services/zone.go:256` 旁边加一条检查，可用区里还有放置组时拒绝删除。否则组指向一个不存在的可用区，再也建不出成员。

---

## 7. 接口

### 7.1 clapi

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/placement_groups` | `offset`/`limit`/`query`/`order`，支持 `zone` 过滤；每项带 `member_count`、`host_count`、`compliant` |
| POST | `/placement_groups` | `{name, description, policy, strict, zone}`；`zone` 为名称，缺省为默认可用区 |
| GET | `/placement_groups/:id` | 在列表字段之外另带 `members` |
| PATCH | `/placement_groups/:id` | 只接受 `name`、`description`；带 `policy`、`strict`、`zone` 返回 400 |
| DELETE | `/placement_groups/:id` | 有成员时 409 |

响应示例：

```json
{
  "id": "…", "name": "db-ha", "description": "",
  "policy": "spread", "strict": true, "zone": "zone0",
  "member_count": 3, "host_count": 3, "compliant": true,
  "members": [
    {"id": "…", "hostname": "db-1", "status": "running", "host_slot": 1, "target_slot": 0, "hypervisor": "work-01",
     "stale_provisioning": false, "stale_migration": false, "migration_id": "…",
     "last_migration_failed": false, "ignored_placement": false}
  ]
}
```

- **成员项的其余字段**（实施时加的）：`target_slot` 是进行中迁移的目标节点编号（0 为没有）；`migration_id` 是进行中迁移的 UUID，只给系统管理员；`last_migration_failed`、`ignored_placement` 表示最近一次结束的迁移失败了 / 带了 `ignore_placement`，界面据此说明不合规的原因（§2.3 第 4、2 种）。
- **云服务器上的 `placement_group`** 除 `id`、`name` 外还带 `policy`、`strict`：迁移弹窗要据此决定是否显示「忽略放置组约束」。
- **`migration_targets` 的 `placement_blocked`**：只因严格组规则而不可用的目标为 true。勾选「忽略放置组约束」后，界面只放开这些目标，因为磁盘或容量不够的目标即使忽略规则也迁不过去。

- **PATCH 拒绝不可改的字段**：Gin 绑定 JSON 时会静默忽略结构体里没有的字段，所以 payload 要把 `policy`、`strict`、`zone` 也声明出来（指针类型），不为 nil 就返回 400。否则用户以为改了策略，实际什么都没变。
- **`host_slot`**：组内的节点编号。同一台节点同一个数字（1、2、3……，按 hostid 排序后编号）。普通用户靠它就能看出成员有没有分开，又不暴露节点名。`hypervisor` 只对系统管理员返回，和 `GET /instances/:id` 现在的做法一致（`GetHyperByHostid` 要求系统管理员）。
- **列表统计**：`member_count`、`host_count`、`compliant` 用一条按"组 + 节点"聚合的查询算出整页结果，不逐组查，避免 N+1。
- **卡住的记录**：详情的成员项加两个布尔字段（§2.2）。它们只用来提示，不影响占用，卡住的记录照样算占用：
  - `stale_provisioning`：`hyper = -1`、状态为 provisioning 或 deleting，且创建超过 1 小时。提示用户删掉这台重建。
  - `stale_migration`：有一条停在进行中状态、1 小时没有更新的迁移记录。复制磁盘期间 `migrate_progress` 每几秒就更新一次这条记录，其他阶段也是秒级，1 小时没动静就是卡住了。提示管理员按 CLAUDE.md「迁移卡死后心跳不会纠正」手工恢复。
- **云服务器接口**：
  - `GET /instances`、`GET /instances/:id` 加 `placement_group: {id, name}`，不在组里时省略。
  - `POST /instances` 加 `placement_group: {id}`。
- **迁移接口**：
  - `POST /migrations` 加 `ignore_placement`；响应 `MigrationResponse` 加 `placement_warning`（尽量组被破坏时才有值）。
  - `GET /instances/:id/migration_targets` 的每个目标：严格组不满足时 `usable=false`，`reason` 说明是哪个组、哪条规则；尽量组不满足时 `usable=true`，另带 `placement_warning`。
- **列表约定**（CLAUDE.md）：名称搜索用 `dbs.Contains`，排序只开放本表的真实列（`name`、`created_at`、`policy`）。
- **Swagger**：改完用 `make docs` 重新生成（`-dir` 已包含 `src/services`）。

### 7.2 cpgateway

- `src/apis/proxy_routes.go` 加上面 5 条，都不是系统管理员专属。
- 不加配额规则。`POST /instances` 的配额按"方法 + 路由模板"整条匹配，请求体多一个字段不影响。
- 删除区域的检查不用加：组只是区域 clapi 里的配置记录，不占资源。

### 7.3 审计与组织动态

- `apis/audit_actions.go` 的 `auditRoutes` 加三条：`POST /placement_groups` → `placement_group.create`，`PATCH /placement_groups/:id` → `placement_group.update`，`DELETE /placement_groups/:id` → `placement_group.delete`。资源名解析表加 `"placement_group": {"placement_groups", "name"}`。
- 文案键 `dashboard.overview.activityActions.placement_group.*` 和对应的 `activityActionsFailed` 三种语言都要加。
- 带 `ignore_placement` 的迁移本来就进审计（迁移创建已有审计动作），不另加。

### 7.4 错误码（1117xx，目前未占用）

| 码 | 名称 | HTTP | 场景 |
|---|---|---|---|
| 111701 | `ErrPlacementGroupNotFound` | 404 | 组不存在或属于别的组织 |
| 111702 | `ErrPlacementGroupExists` | 409 | 组织内重名 |
| 111703 | `ErrPlacementGroupInUse` | 409 | 删除时还有成员 |
| 111704 | `ErrPlacementGroupNoHost` | 409 | 严格分散没有空节点 |
| 111705 | `ErrPlacementGroupHostFull` | 409 | 严格集中的锚点节点放不下或不可用 |
| 111706 | `ErrPlacementGroupConflict` | 400 | 可用区不一致；指定宿主机或迁移目标违反严格约束；严格集中组有成员不能一起迁移 |
| 111707 | `ErrPlacementGroupLimit` | 409 | 超过 50 个 |
| 111708 | `ErrPlacementGroupBusy` | 409 | 严格集中组有迁移在途时新建成员 |

新码要加进 `common/error_codes.go` 的 `ToHTTPStatus`，否则按调用处传入的状态码返回；还要重新生成 `common/error_codes_str.go`（`go generate`，stringer，`make setup` 会安装）。消息要让用户知道怎么办，例如"严格分散组 db-ha 已有 3 台成员（其中 db-4 创建超过 1 小时仍未完成），可用区 zone0 只有 3 台可用节点"。消息里的节点数对普通用户不算额外泄露：用严格分散组逐台加成员，本来就能试出可用区有几台节点。

---

## 8. 前端

- **放置组列表** `/dashboard/placement-groups`（`PlacementGroupList.vue`）：用 `PageToolbar` + `DataTable` + `PaginationBar` + `useListQuery`，服务端分页、搜索、排序。
  - 列：名称 / ID、策略（分散 / 集中 + 严格 / 尽量标签）、可用区、成员数、分布（"3 台节点"）、状态、创建时间、操作。
  - 状态用 `StatusBadge`：合规为 success，不合规为 warning。
  - 操作：`Pencil` 编辑名称和描述，`Trash2` 删除；有成员时删除按钮禁用并提示原因。
- **创建弹窗**（`BaseModal form`）：
  - 字段：名称、描述、策略、严格开关、可用区。
  - 策略用两张单选卡片，各写一句说明和适用场景。
  - 切换策略时按 §2.1 重设严格开关的默认值，说明文字写清"放不下时会怎样"。
  - 可用区用 `zonesApi`，默认选默认可用区。
  - 报错放在底栏按钮旁（长表单弹窗的教训）。
- **详情页** `/dashboard/placement-groups/:id`：
  - 按 CLAUDE.md 的 `.title-bar` 结构。
  - `InfoRow` 列出策略、严格程度、可用区、成员数、分布。
  - 成员表：云服务器链接、状态、"节点 1 / 2 / 3"；系统管理员多一列节点名。
  - 不合规时顶部显示一条说明，例如"有 2 台成员在同一节点上，平台不会自动调整，可以迁移其中一台"。能查到原因的写出原因（§2.3）："成员 db-3 最近一次迁移失败，整组没有迁完"，或者"管理员迁移 db-2 时忽略了放置组约束"。
  - 成员有 `stale_migration` 时在那一行标出"迁移记录已卡住"，系统管理员可以点进迁移详情；有 `stale_provisioning` 时标出"创建超过 1 小时仍未完成，建议删除后重建"。
- **创建云服务器弹窗**（`components/instance/CreateInstanceModal.vue`）：
  - 可用区下面加"放置组"下拉，可选，按所选可用区过滤，可用区变了就清空。
  - 选中后显示一行说明，例如"严格分散：每台放在不同节点；本组已有 2 台成员"。
  - 批量数量超过组能满足的数量时，前端不预判，显示服务端返回的 409。
- **云服务器详情**：`InfoRow` 加"放置组"，链接到组详情。
- **迁移弹窗**（`MigrationList.vue`）：目标列表显示 `reason` 或 `placement_warning`。实例属于严格组时，系统管理员多一个"忽略放置组约束"复选框，勾选后标红提示。
- **侧边栏**：放在计算分组里，挨着云服务器。图标用 lucide `Boxes`，列表行、详情标题、空状态用同一个。
- **验证**：三种语言文案；`npm run typecheck`、`lint`、`i18n:check` 全部通过；按约定在本地 5173 上用 Playwright 验证，按 pathname 兜底拦截写请求。

---

## 9. 权限

- 组是组织资源：组织成员可读；创建、修改、删除要求组织写角色，只读成员 403。系统管理员能看到全部，沿用 `GetOrgFilter` 的做法，列表带组织信息。
- 创建云服务器时引用别的组织的组，按不存在处理，返回 404，不泄露组是否存在。
- `hypervisor`（节点名）只给系统管理员。`host_slot` 所有人可见，只反映组内成员之间是否同机。

---

## 10. 测试

### 10.1 单元测试

`Place` 用表驱动测试（`services/placement_test.go`）：

- **spread 严格**：有空节点时选最宽裕的；没有空节点报错；`pending` 让同批第二台避开第一台。
- **spread 尽量**：5 台放 3 个节点，得到 2/2/1；资源不够的节点跳过。
- **pack 严格**：有锚点时只选锚点；锚点放不下报错；没有成员时要求一台放得下整批；组已不合规时选成员最多的那台；`rest` 由不同规格求和（2 核 + 2 核 + 8 核按 12 核判断，锚点只剩 8 核时报错）。
- **pack 尽量**：锚点满了溢出到最宽裕的节点；组没有成员、又没有一台放得下整批时，第一台落在最宽裕的节点，后面的跟上去。
- **平局**：按 hostid 取小的。
- **`pending` 初值**：本组在途成员的需求压在锚点上时，集中组的新一批在尽量组里溢出、在严格组里报错；刚落地、所在节点 `resources.updated_at` 早于成员 `updated_at` 的成员也计入。
- **磁盘维度**：系统盘在其他本地池时，`fits(h, rest)` 按池的可准入量判断，池放不下整批的节点不会被选中。
- **没有资源行的节点**：组成员跳过；非成员的 `bootHost` 不跳过，选节点结果和现在一致。

占用表 `occ` 和 `pending` 初值的计算要在 PostgreSQL 上单独测，覆盖：三类来源；`error` 成员不占用；软删成员不占用；创建中被删除（`deleting` 且 `hyper = -1`）的仍占用；迁移中源和目标都占用；卡住的记录仍占用（创建超过 1 小时、迁移 1 小时没有更新），并分别带上 `stale_provisioning`、`stale_migration`。

整组检查要单独测：源节点上的成员全部在调用列表里且可迁移时放行；有一个没被请求、处于 rescuing / error、或因存储写满暂停时整组拒绝；本次调用里已经变成 migrating 的成员算随行；各台规格不同时按总和判断；有一台的盘在锚点上没有可用的池（锚点不在它的 g1 / g2 里），或者各台的盘加起来超过锚点上某个池的可准入量时，整组拒绝，一台迁移都不建。

另外三处既有逻辑的改动也要有测试：不指定目标的组成员迁移，建完后库里的 `target_hyper` 就是选中的节点；cland 拒绝迁移时（`error=resource` 回调）实例恢复成 `prior_status`；`executeCommandList` 下发失败时实例置为 `error`。

### 10.2 三节点端到端

在 work-01/02/03 上测，新增 `test-items/TC-18-放置组.md`：

| # | 场景 | 期望 |
|---|---|---|
| PG-01 | 严格分散，count=3 | 3 台落在 3 个不同节点 |
| PG-02 | 严格分散，count=4 | 409，一台都不建，配额不扣（网关在后端失败时回滚预扣） |
| PG-03 | 严格分散逐台建到第 4 台失败，删掉一台后再建 | 新的一台落在空出来的节点 |
| PG-04 | 尽量分散，count=5 | 分布 2/2/1 |
| PG-05 | 严格集中，count=3 | 落在同一节点；把那台的 CPU 用满后再建 → 409 |
| PG-06 | 尽量集中，锚点节点满了 | 溢出到别的节点，组显示不合规 |
| PG-07 | 两个 count=2 的请求同时打同一个严格分散组（3 个节点） | 一个成功、一个 409，不会出现 4 台 |
| PG-08 | 系统盘放 `local-hdd` + 严格分散 | 落在不同节点，`storage_reservations` 正确 |
| PG-09 | 迁移严格分散成员，不指定目标 | 目标避开其他成员；3 个成员占满 3 个节点时 400。系统盘在 `local-hdd`、而只有没建这个池的节点空着时（g2），仍能换池迁过去 |
| PG-10 | 维护腾空一台同时有严格分散成员和严格集中组的节点 | 集中组整组去同一台；分散成员有空节点就迁，没有就记 `not_doing` 且原因可读 |
| PG-11 | 严格集中成员单独迁移（同组其他成员留在源节点、不在请求里） | 400；一个请求里选中整组则成功；带 `ignore_placement` 单独迁移成功，组变为不合规 |
| PG-12 | `migration_targets` | 违反严格约束的节点 `usable=false` 且有原因 |
| PG-13 | 删除有成员的组 | 409；成员删完后可删，名称可以复用 |
| PG-14 | 权限 | 其他组织看不到这个组，引用返回 404；只读成员不能建组 |
| PG-15 | 回归：不选放置组的创建和迁移，系统盘分别放内置池和 `local-hdd` | 内置池：控制串仍是原来的多节点 `select=`；`local-hdd`：仍选池剩余空间大的节点、预留正确（`bootHost` 拆成两步后这条路径也被改到了） |
| PG-16 | 管理员指定宿主机，违反严格分散 | 400 |
| PG-17 | 删除组与往组里建云服务器并发 | 要么删除返回 409、成员建成；要么删除成功、创建返回 404。不会出现指向已删除组的成员 |
| PG-18 | 维护腾空时，严格集中组在源节点上有一台正在救援 | 本组在该节点上的成员全部 `not_doing`，原因写明是哪台、什么状态；组没有被拆开 |
| PG-19 | 严格集中组先建 2 台，镜像还在下载时再建 2 台，锚点节点只放得下 3 台 | 第二个请求 409，锚点不超配。尽量集中组则溢出到别的节点 |
| PG-20 | 严格集中组有迁移在途时往组里建云服务器 | 409 `ErrPlacementGroupBusy` |
| PG-21 | 严格集中组 3 台成员规格不同（2 核、2 核、8 核），整组迁移；其他节点只放得下 2 核 + 2 核 | 整组 400（批量时整组 `not_doing`），一台都不迁 |
| PG-22 | 同 PG-21，CPU 够，但把某台成员数据盘所在的 `local-hdd` 在其他节点上都临时置为维护 | 整组拒绝，一台都不迁；原因写明是哪台成员的哪块盘。测完恢复池状态 |
| PG-23 | 不指定目标迁移严格分散组的一台成员，建完立刻查库，再迁同组另一台 | `migrations.target_hyper` 已是选中的节点；第二台的目标避开它 |
| PG-24 | 在库里把一台成员的迁移记录改成 `source_prepared`、`updated_at` 为两小时前 | 仍算占用；详情标出 `stale_migration`；严格分散组的 409 消息点名这条迁移。测完恢复 |
| PG-25 | 用没缓存过的镜像建一台成员，下载中重启目标节点的 cloudlet（命令丢失，实例停在 provisioning），再把它的 `created_at` 改到两小时前 | 仍算占用、标出 `stale_provisioning`；删掉这台后节点释放 |
| PG-26 | 迁移一台不在组里的云服务器、不指定目标，其他节点的 `cpu_over_ratio` 调低到放不下它 | cland 拒绝；实例恢复成迁移前的状态而不是 `rollback`，迁移记录为 failed |
| PG-27 | 停掉 cland-go 容器后建一台云服务器，再启动 | 实例立即为 `error`，原因写明下发失败；不会停在 provisioning |

PG-07、PG-17 用两个并发的 `curl &`。PG-05、PG-06、PG-19、PG-21、PG-26 可以临时调小节点的 `cpu_over_ratio` 制造"满"，测完改回；PG-19、PG-25 用一个没有缓存过的镜像，让下载时间足够长。PG-27 会让控制面短暂不可用，只在测试环境做。

---

## 11. 实施步骤与改动清单

一次做完再部署，不分阶段上线：如果只做创建不做迁移，维护腾空会悄悄打破严格分散。建议的实施顺序：

1. **模型**：新增 `model/placement_group.go`；`model/instance.go` 加两列；`model/migration.go` 加 `IgnorePlacement`、`PriorStatus`（§4.3）。
2. **放置器**：新增 `services/placement.go`（`Place`（`rest Demand`）、占用表和 `pending` 初值的计算、`HostSlot` 构造、整组检查）和单元测试。
3. **放置组服务与接口**：新增 `services/placement_group.go`、`apis/placement_group.go`；改 `apis/routes.go` 和错误码（加 `ToHTTPStatus`，重新生成 `error_codes_str.go`）。解散组织与删除可用区的检查：`services/org.go`、`services/zone.go`。
4. **创建接入**：
   - `apis/instance.go`：payload 和响应。
   - `services/instance.go`：锁组（在 `admitLocked` 之前）、逐台 `Place`、改控制串；`executeCommandList` 下发失败时把那台实例置为 `error`（附录 B 第 4 条）。
   - `services/instance_placement.go`：组成员的候选与逐台放置（`startCreationPlacement`、`next`）；`bootHost` 不动，非成员的结果不变。
5. **迁移接入**：
   - `apis/migration.go`：`ignore_placement`，响应的 `placement_warning`。
   - `services/migration.go`：`Create` 把本次调用的实例 ID 集合传给 `createOne`；`createOne` 锁组、整组检查、先按规则过滤 g1 / g2 再选目标、`target_hyper` 写回数据库、写 `prior_status`。
   - `services/migration_plan.go`：`MigrationOptions.IgnorePlacement`，`MigrationTargets` 加原因。
   - `rpcs/migrate_vm.go`：新增 `target_migration` handler 处理 cland 的拒绝，把实例恢复成 `prior_status`（附录 B 第 2 条）。
6. **审计**：`apis/audit_actions.go`。
7. **cpgateway**：`src/apis/proxy_routes.go`。
8. **Swagger**：`make docs`。
9. **前端**：新增 `api/placementGroups.ts` 和两个页面；改路由、侧边栏、`CreateInstanceModal.vue`、`InstanceDetail.vue`、迁移弹窗、三种语言文案。
10. **测试**：跑单元测试 → 在 work-01 部署 clapi 和 cpgateway（节点侧无改动，不用同步脚本）→ 跑 TC-18。

节点脚本、cland-go、cloudlet-go 都不改。

---

## 12. 待决策

正文按「推荐」一列写，定下来后再改。

| # | 问题 | 推荐 | 备选 |
|---|---|---|---|
| 1 | cland 复核失败时怎么办（两边资源数据同源，很少发生，§3.1） | 这台失败，用户删掉重建，和现在一致 | clapi 收到 `error=resource` 后换一台自动重试：要在回调里重新锁组、重新下发，代码量翻倍 |
| 2 | 普通用户能否看到 `host_slot` | 能，它只反映组内成员是否同机 | 只给 `compliant`，不给逐台信息 |
| 3 | 组数上限 | clapi 常量 50 | 进 cpgateway 配额（要改十几处） |
| 4 | 已有云服务器能否加入 / 退出组 | 这一版不做 | 做：加入时校验现位置，严格组不满足就拒绝 |
| 5 | 尽量组被管理员指定宿主机或迁移目标打破时 | 放行；迁移响应带 `placement_warning`，创建不带 | 和严格组一样拒绝，要求带 `ignore_placement` |
| 6 | 卡住的迁移记录（§2.2 第 3 类）是否一直算占用 | 一直算，组详情标出 `stale_migration`，由管理员恢复；严格约束永远不会被静默破坏 | 超过 24 小时不算（初稿的做法）：组不会被一条卡住的记录一直挡住，但虚拟机其实已经在目标节点上时，严格分散组可能把另一台成员放上去，而且没有任何提示 |

---

## 附录 A：与公有云的对照

| | AWS EC2 | OpenStack | 本方案 |
|---|---|---|---|
| 分散 | spread（每个可用区最多 7 台，按机架） | anti-affinity / soft-anti-affinity | spread + 严格 / 尽量，按节点 |
| 集中 | cluster（同一个低延迟网络段，不是同一台物理机） | affinity / soft-affinity | pack + 严格 / 尽量，按节点（同一台物理机） |
| 分区 | partition（按机架分区，元数据告知分区号） | 无 | 不做 |
| 已有实例加入 | 实例停止后可以修改 | 只能在创建时 | 只能在创建时 |
| 违规之后 | 不自动纠正 | 不自动纠正 | 不自动纠正，界面标出 |

注意 AWS 的 cluster 不是"同一台物理机"，而是"同一个低延迟网络段"。本方案的 pack 是真正的同一节点，因为 CloudLand 没有机架拓扑。好处是同节点互访走本机网桥、不经 VXLAN；代价是一台节点宕机整组下线。这一点界面上要说清楚。

---

## 附录 B：动手时会撞上的既有问题

1. **在途的创建不计入资源，滞后是分钟级**：节点只统计已经 `virsh define` 的虚拟机（`report_rc.sh:410-416`），而 `launch_vm.sh` 要先下载镜像、转换，最后才 define（`launch_vm.sh:56-128`）；cloudlet 默认串行执行，排队的创建还要再等。这段时间里 clapi 的 `resources` 表和 cland 的调度表（`scheduler.go:101` 只读不写）都看不到这些虚拟机，接连的请求可能都通过检查、超配到同一台。现在的 `select=` 调度就有这个问题。放置组用 §3.3 的 `pending` 初值补上了**本组**的在途成员；别的组和非组成员的在途创建仍看不到，因为 clapi 在它们落地前不知道位置。彻底的修法是 cland 选中节点后临时扣减、下次心跳覆盖，另立项。
2. **cland 复核拒绝的迁移，回调没有人处理**（实施时更正，原先写的是「实例停在 `rollback` 最多 10 分钟」）：拒绝时 cland 按 `error=resource` 回传原命令 `target_migration.sh`，rpcs 按脚本名找 handler，而只注册了 `migrate_vm`，于是记一条 `no command target_migration found` 就丢掉了。迁移记录永远停在 `in_progress`，实例停在 `migrating`，迁移预留要等 24 小时过期；`MigrateVM` 里写 `rollback` 的分支从来走不到。原先不指定目标的迁移用多节点 `select=`，只有所有候选都不够时才拒绝，很少发生；放置组的迁移改成单节点 `select=` 后会常见得多。**这次一起修**：新增 `target_migration` handler，实例恢复成迁移前的状态（`migrations.prior_status`，§4.3）、迁移与阶段任务置为 failed、释放预留；cland 会重试回调，只处理还在 `in_progress` 的记录（§6.3）。
3. **管理员指定宿主机时 cland 完全不复核资源**（`inter=`）：这是现状，本方案不改。放置组只校验规则，不替管理员兜底容量。
4. **下发失败的创建永远停在 provisioning**（`instance.go:403-413`）：`executeCommandList` 里 `HyperExecute` 出错只记日志，实例 `hyper = -1`、状态 provisioning，之后不会再有任何回调。放置组让它变成第 2 类占用，一直占着 `placement_hyper`。**这次一起修**：出错时按 cland 拒绝时的做法处理（`rpcs/launch_vm.go` 的 `error=resource` 分支）：实例置为 `error`、原因写明下发失败，并释放系统盘的存储预留。有一种情况会误判：gRPC 超时、而 cland 其实已经下发了。这时之后的 `launch_vm` 回调会把状态改回 running 并写入 `hyper`（除迁移中外，回调不看当前状态），实例本身不受影响；只是这期间它不计入占用，严格组有极小的概率被破坏，接受。

---

## 附录 C：复查改掉的 12 处（2026-09-24）

初稿写完后按代码逐条核对了一遍，下面这些是改过的。其中 1、2、6 三条是初稿对代码行为的理解错了，另外几条是设计漏洞。

**结论没变的部分**：由 clapi 选节点、下发单节点 `select=`；锁放置组那一行；三类占用；严格约束平台不破坏。核对下来都站得住。调整规格、重装、救援都用 `inter=instance.Hyper`（`instance.go:354/545/692`），不会换节点；`InstanceAdmin.Create` 只有 `apis/instance.go:636` 一个调用方，不在外层事务里，命令确实在提交后才下发。

| # | 初稿写的 | 实际 | 改成 |
|---|---|---|---|
| 1 | cland 复核失败只可能是两次心跳之间资源被占走，窗口几秒 | 节点要到 `virsh define` 之后才统计新虚拟机，而 define 在镜像下载、转换之后（`launch_vm.sh:56-128`、`report_rc.sh:410-416`），窗口是分钟级；clapi 和 cland 的数据同源，都看不到在途的创建，真正的风险是超配两边都拦不住 | `pending` 初值计入本组在途成员（§3.3）；改正 §3.1；附录 B 第 1 条扩写；新增 PG-19 |
| 2 | 附录 B：迁移失败留下的 `rollback` 状态心跳永远不纠正 | 心跳每 600 秒全量重报一次（`report_rc.sh:95-98`），最多约 10 分钟就纠正 | 改写附录 B 第 2 条，降为低优先级 |
| 3 | 严格集中：源节点在维护中时，其他成员视为"会一起走" | `Create` 跳过非 running / shut_off / paused 的虚拟机（`migration.go:83-86`），`createOne` 拒绝因存储写满暂停的（`:107`）。源节点上有一个成员在救援，其他成员照样迁走，组被平台自己拆开 | 改成"随行成员"规则：源节点上本组成员全部在同一次调用里且可迁移才迁，否则整组不迁；不再按维护状态特判（§6.3）；新增 PG-18 |
| 4 | 删除组：有成员返回 409，空组软删 | 先数再删而不加锁，并发创建会留下指向已删除组的成员 | 删除也锁组，在同一事务里计数、软删（§3.2、§6.1）；新增 PG-17 |
| 5 | 迁移候选 = g1（没有就 g2），再套放置规则 | `migration.go:157-162` g1 非空就只看 g1；g1 全被本组占用、只有 g2 有空节点时严格分散会误报 | 先按规则分别过滤 g1、g2，再 g1 优先（§6.3）；PG-09 补一条 |
| 6 | `HostSlot` 只有内置池磁盘 | 系统盘在其他本地池时，严格集中建第一批可能挑中池放不下整批的节点，整批回滚 | 磁盘维度按系统盘所在池取值：其他池用 `admit` 规则算出的可准入量（§5） |
| 7 | 第 3 类占用：进行中的迁移目标 | 卡住的迁移记录（2026-09-16 那种）会永久占住目标节点 | 只计 24 小时内有更新的，详情标出 `stale_migration`（§2.2、§7.1）。第二轮改为一直计入，见附录 D 第 5 条 |
| 8 | 第 2 类占用：`status = provisioning` | 创建中被删除的虚拟机在节点上可能还在建（删除用 `toall=` 广播，`instance.go:1254-1256`） | 第 2 类加 `deleting`（§2.2）。第二轮发现它只在最初几秒起作用，见附录 D 第 6 条 |
| 9 | 没提解散组织、删除可用区 | `services/org.go:475-488` 的资源检查不含放置组；删除可用区后组再也建不出成员 | 两处都加检查（§6.5） |
| 10 | PATCH 带 `policy` 等返回 400 | Gin 静默忽略结构体里没有的字段 | payload 把这三个声明成指针（§7.1）；另外没有 `resources` 行的节点跳过，与 cland 一致（§5） |
| 11 | 管理员指定宿主机打破尽量组时，创建响应带 `placement_warning` | `POST /instances` 返回云服务器数组，没有合适的位置 | 创建不带警告；迁移响应 `MigrationResponse` 带（§6.2、§6.3） |
| 12 | 回归只测不选放置组时控制串不变 | `bootHost` 拆成两步后，非组成员 + 其他本地池的创建路径也被改到了 | PG-15 补系统盘放 `local-hdd` 的回归 |

复查时顺带补的一条规则：严格集中组有迁移在途时拒绝新建成员（§6.2，`ErrPlacementGroupBusy`，PG-20）。锚点此刻在源和目标之间，新成员放哪边都可能和整组的去向相反。

---

## 附录 D：第二轮复查改掉的 15 处（2026-09-27）

按 `0795dacc` 的代码再核对了一遍（文中引用的文件自 `a6336e10` 起都没变过，行号仍然准确）。其中 1–5 是会让平台自己破坏约束、或者让组被永久卡住的问题，6–15 是事实更正和细节。

**核对过、结论没变的部分**：单节点 `select=` 时 cland 只在这一台上复核，判断式是"需求 ≤ 可用"，节点未连接时同样回调 `error=resource`；创建和迁移的命令都在提交后才下发；数据库是默认的 READ COMMITTED，拿到组锁之后的查询看得到前一个事务写入的成员；负载均衡锁路由器行的先例、`resourceChecks`、删除可用区的检查位置都在；1117xx 没有被占用；批量上限是 16；迁移候选排除源节点。

| # | 之前写的 | 实际 | 改成 |
|---|---|---|---|
| 1 | 严格集中整组迁移：`fits(h, batchLeft)` 是"放得下 batchLeft 台这一台的 need"；随行成员只检查状态 | 迁移时各成员规格可以不同；其他本地池的盘要到各自的 `createOne` 才检查，而成员是逐台建迁移、逐台下发的（`migration.go:83-100`），后面的接不住时前面的已经在迁，组被拆开 | `batchLeft int` 改成 `rest Demand`（§5）；`createOne` 锁组后对全部还没建迁移的随行成员做整组检查，第一台就覆盖整组（§6.3）；PG-21、PG-22 |
| 2 | §2.3：严格组在平台自己的决策下不会变成不合规 | 整组迁移时各成员的迁移互相独立、异步执行，一台失败回滚，其他照样迁完 | §2.3 列为第 4 种来源，另加心跳纠正位置为第 5 种；界面说明原因（§8） |
| 3 | 第 2 类占用没有时限，也没提它会卡住 | 下发失败只记日志（`instance.go:403-413`）、`launch_vm.sh:39` `die` 不回调、cloudlet 重启丢命令、目标节点被删，实例都会一直停在 provisioning | 继续算占用，标 `stale_provisioning`（§2.2、§7.1），409 消息点名；下发失败置为 `error`（附录 B 第 4 条）；PG-25、PG-27 |
| 4 | 不指定目标的迁移：`migration.TargetHyper = 目标` | 迁移记录已按 -1 插入（`migration.go:147`），只改内存就是 2026-09-16 那个 bug 的翻版，第 3 类占用也读不到 | 在同一事务里写回数据库（§6.3）；PG-23 |
| 5 | 第 3 类只计 24 小时内有更新的迁移 | 已知的卡死形态里虚拟机其实已在目标节点运行，24 小时后目标节点看起来是空的，严格分散可能被静默破坏 | 一直算占用，标 `stale_migration`（阈值改为 1 小时，只做提示）；§12 第 6 条；PG-24 |
| 6 | 第 2 类计入 `deleting`，能挡住"节点上那台可能还在建" | `toall=` 时，其他节点几秒内就回调，`ClearVM` 在第一条回调到达时就软删记录（`rpcs/clear_vm.go:199`） | 保留 `deleting`，改正说明（§2.2 的注） |
| 7 | `pending` 只计还没落地的在途成员 | 落地后 `resources` 要等下一次 `hyper_status`（≤ 20 秒）才计入它 | `pending` 加第二个来源（§3.3） |
| 8 | 附录 B 第 2 条优先级低 | 单节点 `select=` 让 cland 拒绝变多，停在 `rollback` 的实例被 `Create` 静默跳过，严格集中组整组被挡 | 这次一起修，`migrations` 加 `prior_status`（§4.3、§6.3）；PG-26 |
| 9 | 管理员指定宿主机时控制串是 `inter=` | 只对内置池成立；其他本地池走 `bootHost` + 单节点 `select=`（`instance.go:269-278`） | 改 §1.1、§6.2 |
| 10 | "不加入组的行为完全不变"，同时"没有资源行的节点跳过" | 现在的 `bootHost` 不跳过这类节点（`instance_placement.go:95`），拆成两步时容易被一起改掉 | 只对组成员跳过，非成员的结果不变（§0、§5、§6.2） |
| 11 | 删除节点后，成员记录可能留下 | `HyperAdmin.Delete` 在节点上还有实例时拒绝（`hyper.go:579-586`） | 改 §6.5：只有第 2 类会悬空，按卡住的创建处理 |
| 12 | 尽量集中、组没有成员时：prefer `fits(h, batchLeft)` | 没写一台都放不下整批时怎么选 | 退回 `fits(h, need)` 里最宽裕的一台，后面的跟上去（§5） |
| 13 | 新错误码加进 `ToHTTPStatus` 即可 | 还要重新生成 stringer 的 `error_codes_str.go`；示例消息会把节点数告诉普通用户 | 加 `go generate`（§7.4、§11）；节点数用严格分散组本来就能试出来，保留 |
| 14 | 锁顺序：先锁组，再做存储池准入 | 内置池 + 管理员指定宿主机时，`admitLocked` 在循环之前执行（`instance.go:176`） | §3.2、§6.2 写明锁组要放在它前面 |
| 15 | `ignore_placement` 不落库 | 组详情说不清不合规是哪次迁移造成的 | 和 `ignore_capacity` 一样存进迁移记录（§4.3） |

---

## 附录 E：实施记录（2026-09-27）

按 §11 一次做完：clapi、cpgateway、前端、测试、Swagger。2026-09-28 提交 `0168d0e1`（后端）、`3b86224d`（前端）与本文所在的文档提交，未推送。部署只需重建 clapi、cpgateway（界面再加 nginx）；新表和新列由 AutoMigrate 建出，不用手工改库；节点侧、cland-go、cloudlet-go 不改。2026-09-28 部署了 work-01 的 clapi、cpgateway（nginx 没重建，界面在本机 5173 上看），端到端结果见 E.5。

### E.1 改动

- **clapi**：
  - 模型：`model/placement_group.go`（新）；`instances` 加 `placement_group_id`、`placement_hyper`；`migrations` 加 `ignore_placement`、`prior_status`。
  - 放置器：`services/placement.go` 放 `Place`、占用表与 `pending`、`explainPlacement`（报错时点名在途和卡住的成员）。
  - 迁移：`services/placement_migration.go`，内容是迁移时的放置、严格集中的整组检查，以及 `migration_targets` 的标注。
  - 创建：`services/instance_placement.go` 的 `startCreationPlacement`。
  - 放置组服务与接口：`services/placement_group.go`、`apis/placement_group.go`，另有路由、审计映射、错误码 1117xx（已重新生成 stringer）。
  - 解散组织、删除可用区时检查放置组。
- **附录 B 顺带修的两个既有问题**：
  - 下发失败的创建置为 `error`：`services/instance.go` 的 `launchNotSent`。
  - cland 拒绝迁移的回调：`rpcs/migrate_vm.go` 的 `TargetMigrationRefused`。
- **cpgateway**：`proxy_routes.go` 加 5 条。
- **前端**：
  - 新增：放置组列表 / 详情 / 编辑弹窗、`api/placementGroups.ts`、`utils/placementGroup.ts`。
  - 修改：路由与侧边栏（计算分组、`Boxes`）、创建云服务器弹窗、云服务器详情、迁移弹窗、操作动态、三种语言文案。

### E.2 与正文不同的做法

1. **`bootHost` 没有拆**：组成员的候选由 `startCreationPlacement` 另算，`bootHost` 原样留给非成员。这样非成员的行为不变，靠的是代码根本没动，而不是靠回归测试来证明（§6.2、§11）。
2. **附录 B 第 2 条原先的判断错了**。cland 拒绝时回传的是 `target_migration.sh`，rpcs 里没有这个 handler，迁移永远停在 `in_progress`。原文以为是「写成 rollback、最多 10 分钟」，实际更糟，现已加 handler（§6.3、附录 B）。
3. **接口多了几个字段**：
   - 成员的 `target_slot`、`migration_id`、`last_migration_failed`、`ignored_placement`；
   - 云服务器 `placement_group` 带 `policy`、`strict`；
   - 迁移目标的 `placement_blocked`（§7.1）。
4. **指定了目标的严格集中整组迁移，整组检查只查磁盘**，不查 CPU 和内存。原因是指定目标用 `inter=`，cland 不复核，不会中途一台一台被拒；而磁盘每台迁移都要准入，中途失败就会把组拆开（§6.3）。
5. **带 `ignore_placement`、又不指定目标时**，走原来的多节点 `select=`，由 cland 选节点。
6. **报错消息是英文**，和接口的其他消息一致。只在系统管理员才能调的接口（迁移、指定宿主机）里写节点名；普通用户的创建报错不写。
7. **删除可用区时有放置组**：返回 111703（409）。
8. **界面的迁移弹窗一次只迁一台**，所以严格集中组要整组迁移，只能走维护腾空，或者直接调接口一次传多台。（2026-09-28 改了：选中严格集中组的成员时，弹窗自动带上同一节点上的其他成员，见 E.6。）
9. **创建云服务器弹窗的报错挪到了底栏**（长表单弹窗的约定），这对这个弹窗的所有报错都生效，不只放置组的 409。

### E.3 测试

- **单元测试**（`services/placement_test.go`，9 个）：`Place` 各策略与平局、`pending`、规格不同时的 `rest` 求和、磁盘维度、`placementViolation`、合规判定、整组检查里哪些成员挡路。本机 `go test` 通过。
- **PostgreSQL 测试**（`services/placement_pg_test.go` 6 个、`rpcs/migrate_vm_pg_test.go` 1 个，设置 `CLAPI_TEST_DB_URI` 才跑）覆盖：
  - 三类占用、`error` 与软删成员不计、创建中被删除仍计、迁移源与目标都计、卡住标记、刚落地成员的 `pending`；
  - 按组 + 节点聚合的统计与 `host_slot`；删除组的 409 与名称复用；
  - 创建时逐台放置、整批放不下、管理员指定宿主机违规、严格集中迁移中拒绝新建、可用区不符；
  - 不指定目标的迁移把 `target_hyper` 写回数据库、`prior_status` 落库；严格集中单独一台被拒、整组选接得住的节点、没有就拒；
  - 下发失败置 `error` 并释放预留；`target_migration` 拒绝回调恢复状态、幂等。

  在 work-01 上起临时的 `postgres:14` 容器（只绑 127.0.0.1）、在 `golang:1.26-bookworm` 容器里连它跑，连跑两次都通过，跑完容器、卷和代码副本都已删除。在本机经 SSH 隧道跑不动：AutoMigrate 有上千条 SQL，每条都要跨洋往返一次。
- **全量**：api `go test ./...` 通过。cpgateway 的 `TestPythonParityFlows` 失败，但属于既有问题：在 `git archive HEAD` 出来的未改动代码上同样随机停在 169 或 206 行；其余通过。
- **前端**：`typecheck`、`i18n:check` 通过，`lint` 0 错误（3 条既有 warning）。本机 Playwright 对 5173 做了 34 项检查，放置组接口全部 mock、写请求一律拦截，全部通过。

### E.4 没做 / 没验证

- TC-18 跳过的 8 条：PG-10、PG-18（维护腾空会迁走节点上所有虚拟机，含保留环境）；PG-19、PG-25（cirros 已缓存，造不出下载窗口）；PG-22（池维护影响同池其他虚拟机）；PG-14（没有第二个组织的账号）；PG-03、PG-06（由单元测试和 PostgreSQL 测试覆盖）。
- 界面还是一次选一台；严格集中组的同节点成员由弹窗自动带上（E.6），其他情况要一次迁多台仍得调接口。
- 迁移详情页不显示 `ignore_placement` / `placement_warning`。
- 界面没按角色隐藏写操作按钮：只读成员点了由服务端返回 403，和其他页面一样。

### E.5 部署与端到端（2026-09-28）

- **部署**：
  - 先用 git blob 哈希逐文件比对，确认 work-01 的 `api/`、`cpgateway/`、`scripts/` 与本地 HEAD 一致，再只覆盖这次改动的 27 个文件（覆盖前的文件 `/root/pg-deploy-backup-20260928.tar`，清单 `/root/pg-deploy-files-20260928.txt`）。
  - 数据库先 dump：`/root/cloudland-db-before-pg-20260928.dump`、`/root/cpgateway-db-before-pg-20260928.dump`。
  - 重建 clapi、cpgateway。AutoMigrate 建出 `placement_groups` 表和新列，心跳正常，`rb-lb` 10/10。
- **结果**：TC-18 跑了 19 条，全部通过，8 条跳过（E.4），逐条证据见 `test-items/runs/2026-09-28-0795dacc+放置组.md`。几条关键的：
  - 组成员下发的是单节点 `select=`，不入组的仍是多节点；
  - 并发创建、删组与建成员并发都不会破坏约束；
  - 严格集中组整组迁移到同一台；规格不同、合起来放不下时一台都不迁；
  - 卡住的迁移记录继续占着目标节点；
  - cland 拒绝迁移后实例恢复原状态，这是附录 B 第 2 条修复在真实环境的验证；
  - cland 停 10 秒期间建的实例立即 `error`。
- **界面**：本机 5173 对真实接口只读检查 20/20 通过。
- **过程中改的**：三处报错文字（严格集中的原因重复、批量 / 迁移时「成员在 N 台节点上」容易误解、整组迁移被拒时句尾重复），改完重新部署 clapi 并逐条复查。
- **清理**：测试用的 21 台虚拟机、6 个组、3 个临时规格、子网与 VPC 已删除，三节点无残留。

### E.6 代码审查后的修复（2026-09-28）

审查报了 14 条，逐条核对都属实。修了 11 条，另有 1 条（第 13 条，可用空间算了两遍，收益太小）没修，2 条有意不修（最后一段）。

**必须修的 3 条**：
1. **批量迁移时成员失败，其余照样迁走**：`migrationCall` 原先只记成功的成员；维护腾空里某台严格集中成员的迁移失败后，后面成员的整组检查照样放行，组被拆开。现在失败的也记下（`call.failed`），整组检查遇到就拒绝。
2. **迁移路径没有「组迁移中」的拦截**：严格集中组有更早请求发起、还在进行的迁移时，组分在两台上，锚点取哪台说不准，可能正是这台自己的源节点，迁移命令就会发给它正在运行的节点。现在和创建路径一样返回 409（111708），另加「锚点不能是源节点」的保护。
3. **`ignore_capacity` 给了「无限大」的节点**：所有候选得分相同，只会挑编号最小的，而 cland 按真实资源复核，满了迁移就失败。现在一律用真实资源；`ignore_capacity` 只跳过存储池容量（§6.3 正文已改）。

**建议修的 3 条**：
- **迁移弹窗**：选中严格集中组的成员时，调组详情接口找出同节点的其他成员，一起放进 `instances`，弹窗写明「将同时迁移 X、Y」。成员状态不能迁时用红字提示。整组迁移时隐藏逐盘选池，因为接口只接受单台时逐盘指定。迁移目标上「其余成员须同一请求」的提示也不再重复显示。
- **系统盘放其他本地池、存储池准入失败时**，一台候选都没有就返回存储池的原始原因（原先被吞掉，报成放置组没有节点）。
- **`MigrateVM` 的参数个数检查**改为 `< 6`：原先是 `< 5`，却读了 `args[5]`。

**顺手清理的 5 条**：
- 放置组写入失败时只有唯一约束冲突才报 111702，其他按数据库错误报；
- 删掉管理员列表里逐组查组织、结果却没人用的循环（以及模型上的 `OwnerInfo`）；
- 组成员创建时不再重复计算内置池的候选组；
- 详情页的节点名一次查出（`GetHyperNames`）；
- `started` 改成布尔集合。

**有意不修的 2 条**：
- **管理员指定宿主机、节点刚断线时实例卡在 provisioning**：cland 回复 `error: node not connected` 时 `HyperExecute` 只告警，这是既有约定，改它影响所有调用方。卡住的成员会被标出，删掉即可释放节点。
- **用 `updated_at` 判断「刚落地」会多算**：§3.3 已写明接受这个取舍。

**验证**：
- 单元测试新增「本次调用里失败的成员」一例；PostgreSQL 测试新增三个场景（失败成员拦住整组、`ignore_capacity` 选有空的节点、组迁移中拒绝）和一个新测试（存储池原因透出）。8 个数据库测试连跑两次都通过，api 全量通过，前端三项检查通过。
- 重新部署 clapi 后，端到端重跑 PG-11、PG-21，并验证修复 2：组被拆开、`pg-k-2` 正从 3 号迁往 1 号时，再迁 1 号上的 `pg-k-1`。按原先的算法锚点会取 1 号，也就是它自己的源节点；现在返回 409、一条记录都没建。
- 界面在 5173 上真实提交了一次严格集中组的迁移：请求带上三台，三台一起到了 3 号，组仍合规。
- 测试资源都已清理。
