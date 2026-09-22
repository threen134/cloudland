# 本地多盘存储池设计（按宿主机配置磁盘，支持 SSD / HDD 混用）

- **状态**：设计草案，未实施。2026-09-22 按两轮审查（代码核对 + 对抗式设计评审）修订
- **日期**：2026-09-22，基于 `stage-01` 分支 `53dceb82`（文中行号均以此为准）
- **涉及**：clapi（`api/`）、节点脚本（`scripts/`）、cpgateway 代理白名单、前端（`web/`）、部署脚本
- **相关文档**：`gpfs-shared-storage-design.md`（下称「GPFS 设计」）。本文沿用它的存储池模型和「clapi 是路径的唯一来源」这一原则
- **执行顺序**：**本文先实施，GPFS 设计后实施**。GPFS 设计阶段 0 里本地存储需要的部分由本文的 L0a / L0b 完成；GPFS 专用的部分留给 GPFS 设计。以后执行 GPFS 设计时，在本文的结果上增加 `gpfs` 驱动即可

---

## 0. 摘要

- **两层结构**：「存储池」是区域级、用户可选的资源（如 `local-ssd`、`local-hdd`）；「节点存储池」是某台宿主机上属于某个存储池的一个目录，由管理员按机器配置。每台机器可以有 0 个或多个本地存储池，盘的类型、数量、冗余方式各不相同。
- **同一台机器上，一个存储池只对应一个目录**，路径在所有机器上相同：`/opt/cloudland/pools/<池UUID>`。同类的多块盘用 LVM（可选 RAID1）合成一个目录。
- **管理员在计算节点详情页配置磁盘**：扫描、建池、加盘、删池；节点重新注册后「接管」原来的池；盘坏了「宣告池丢失」。建池会格式化磁盘，有多道防护，已属于 CloudLand 的盘不允许清除。建池、加盘时后端检查盘与池、所选盘之间的介质是否一致，不一致要显式确认；自动识别错了可以手工纠正（§2.6）。
- **容量准入按「机器 × 池」计算，并在请求时预留**：首次挂载、扩容、创建系统盘、迁移时先在事务里写预留，节点回调成功后转正、失败时释放。内置池也纳入准入。
- **迁移**：默认留在原池；管理员可以逐盘换到目标机器上的其他池。目标机器没有原池时，只在同一个「可互换组」的池之间自动替换，永远不跨组。不指定目标时，clapi 先把候选机器收窄到「能保留原池」的一组，再交给 cland 按资源挑选。
- **开机不阻塞心跳**：池还没挂好的虚拟机进入待启动列表，之后每次心跳检查一次，就绪了再启动。
- **分阶段**：L0a（数据盘相关的存储池抽象）→ L1（本地池与数据卷）→ L2（迁移时换池）→ L3（加盘、RAID1、告警）→ L0b（系统盘相关的抽象）→ L4（系统盘放进池）→ L5（可选）。

---

## 1. 背景与目标

### 1.1 现状

- **本地卷只有一个目录**：`scripts/cloudrc:11` 写死了 `volume_dir=/opt/cloudland/cache/volume`，`scripts/kvm/` 下 12 个脚本用它：多数直接拼卷的路径；`finish_source_migration.sh`、`clear_target_migration.sh` 把它当作清理时的路径前缀；`launch_vm.sh`、`reinstall_vm.sh`、`rescue_vm.sh` 用它找 `inst-N.disk`（回退逻辑）。系统盘在 `$image_dir`（`/opt/cloudland/cache/instance`）。两个目录都在根分区上。
- **节点上的盘没用上**：work-01、work-02、work-03 各有两块 2 TB 机械盘（Seagate ST2000NM0055）。操作系统、系统盘、数据卷全在 `sda4` 上；第二块盘 `sdb` 闲置：work-01 / work-03 的 `sdb1` 挂在 `/disk1`（ext4，空），work-02 的 `sdb1` 没有挂载。
- **数据卷的容量没人管**：
  - 挂载和扩容本地卷时不检查剩余空间，而卷文件是 qcow2 精简置备，分配总量可以超过磁盘容量，写满时虚拟机出现 I/O 错误。
  - 心跳（`report_rc.sh`）算节点可用磁盘时，虚拟大小只统计 `$image_dir/*`（第 248–255 行），数据卷不在其中；数据卷的实际占用只经第 257 行的 `du` 间接计入，而 `du -s` 的单位是 KiB、第 18 行 `df -B 1` 是字节，第 258 行直接相减，这部分占用只按约千分之一扣除。再加上调度时磁盘需求的单位也不对（附录 B 第 6 条），cland 的磁盘检查实际上几乎总是通过。
  - clapi 从 `hyper_status` 回调（`report_rc.sh:309`）拿到的是乘过超分比例、减掉系统盘虚拟大小之后的值，不是文件系统的原始容量。
- **将来会有混用**：不同机器的盘类型（SSD / HDD）、数量可能都不一样。
- **本地卷不支持备份**（`services/backup.go:154`），盘坏即丢数据，冗余方式要在建池时认真选。
- 上游的「多存储池」（PET-1283）只针对 WDS，**不涉及本地盘**，不能直接借用。

### 1.2 目标

1. 管理员能按机器配置本地磁盘：哪些盘、组成哪些存储池、用什么冗余方式。
2. 用户按存储池建卷，不需要知道机器和目录。
3. 本地卷只落在「虚拟机所在机器上、属于该存储池」的目录里；机器上没有这个池时明确拒绝，而不是悄悄落到根分区。
4. 按「机器 × 存储池」做容量准入，并发请求不会超分。
5. 迁移时每块盘可以留在原池，也可以由管理员换到目标机器上的其他池；目标机器没有原池时，在可互换的池之间自动替换。
6. 管理员创建虚拟机时，可以指定宿主机和这台机器上的具体存储池。
7. 加盘在线完成，不停虚拟机。
8. 节点重新注册、盘坏等情况有明确的处置流程，不会卡死，也不会误删数据。

### 1.3 非目标

- 共享存储（GPFS 设计负责）。
- 同一台机器上同一个存储池拆成多个目录。需要隔离时建成不同的存储池，并且不把它们放进同一个可互换组（§2.2）。
- 不换机器、只换池（同一台机器上的池间搬迁），以及本地盘与 GPFS 盘之间的转换，放在可选的阶段 L5。跨机器迁移时换池属于本文范围（§7）。
- 按存储池分别计配额。cpgateway 仍然只有一个 `disk_gb`。
- 本地卷的备份与复制。

---

## 2. 概念与模型

### 2.1 两层结构

| 层 | 范围 | 谁来维护 | 例子 |
|---|---|---|---|
| **存储池**（`storage_pools`） | 区域级，用户可见、可选择 | 系统管理员定义 | `local`（内置）、`local-ssd`、`local-hdd` |
| **节点存储池**（`hyper_storage_pools`） | 某台宿主机上属于某个存储池的一个目录 | 系统管理员在该机器上配置；内置池自动存在 | work-02 上的 `local-ssd`：`nvme0n1`，单盘，1.8 TB |

```
存储池（区域级）    local（内置）          local-ssd                 local-hdd
work-01            /opt/cloudland/cache   —                         sdb+sdc，RAID1，1.8 TB
work-02            /opt/cloudland/cache   nvme0n1，单盘，1.8 TB      sdb，单盘，1.8 TB
work-03            /opt/cloudland/cache   nvme0n1+nvme1n1，线性     sdb，单盘，1.8 TB
```

- **内置的 `local` 池**：每台机器都有，根目录 `/opt/cloudland/cache`，也就是现在的位置。已有的卷全部属于它，不需要搬数据。它在每台机器上也有一条节点存储池记录（§3.2），用来上报容量和做准入。
- **用户只选存储池**。一块 `local-ssd` 卷首次挂到 work-02 上的虚拟机时，落在 work-02 的 `local-ssd` 目录；挂到 work-01 上的虚拟机时，因为 work-01 没有 `local-ssd`，直接拒绝并说明原因。
- 本地卷落盘后就绑定在那台机器上（`volumes.hyper`），这和现在的规则一致。

### 2.2 同一台机器上一个池只对应一个目录

- **同类的多块盘在存储层合并**（LVM，可选 RAID1），对 CloudLand 只呈现一个目录。加盘只是存储层的在线扩容（§4.4）。
- **需要分开的就建成不同的池**：SSD 与 HDD 分成 `local-ssd` 和 `local-hdd`；需要按盘隔离时，建 `local-hdd-a`、`local-hdd-b`，并且**不要放进同一个可互换组**，否则迁移时会被自动互换（§7.1）。
- 代价：同一个节点存储池里的盘共享故障域。线性拼接时坏一块盘，这个池里的全部卷都受影响；本地卷又不支持备份，所以建池时要明确选择冗余方式，界面上把「无冗余」标注清楚。

### 2.3 路径约定

- 非内置本地池的根目录固定为 `/opt/cloudland/pools/<池UUID>`，**在每台有这个池的机器上都相同**。用 UUID 而不是名称，存储池可以改名，路径不变。
- 路径相同的好处：迁移时盘留在同一个池，路径不变，域定义不用改写。换池时路径不同，改写方法见 §7.3。
- 池根目录是一个独立挂载点：

```
/opt/cloudland/pools/<pool-uuid>/     <- mount point of the host's LV
├── .cloudland-pool                   <- marker: pool UUID + hostid (guard §4.6, adoption §5.9)
├── volumes/volume-<volID>.disk       <- data disks and (phase L4) boot disks, qcow2
├── nvram/inst-<instID>_VARS.fd       <- UEFI NVRAM of instances whose boot disk is in this pool (L4)
└── tmp/
```

- `volumes.path` 存池内相对路径（`volumes/volume-<id>.disk`），绝对路径由 clapi 拼好下发。内置池沿用现在的相对路径：数据盘 `volume/volume-<id>.disk`，系统盘 `instance/inst-<实例ID>.disk`。

### 2.4 与 GPFS 设计的关系

| 方面 | GPFS 池（以后） | 本地池（本文） |
|---|---|---|
| 存储池记录 | `storage_pools`，`Driver=gpfs` | `storage_pools`，`Driver=local` |
| 节点记录 | `hyper_storage_pools`：该节点能否访问共享池 | `hyper_storage_pools`：该节点上这个池的目录，带盘、布局、容量、健康状态 |
| 卷与节点 | 不绑定，`hyper=0` | 落盘后绑定 `hyper` |
| 容量 | 池级（fileset 配额） | 机器 × 池级 |
| 可用性上报 | clapi 广播检查脚本（GPFS §5.2） | 节点心跳里上报（§4.7）。两套机制写同一张表，各管各的驱动 |
| 迁移 | 全共享时不复制磁盘 | 复制磁盘；默认留在原池，可以换池 |

### 2.5 配置流程总览

**第 1 步：建存储池，路径在这时自动确定。**

- **在哪里**：管理员的「存储池」管理页（§8.3）。填名称、介质标签、可互换组、超分比例。
- **什么时候**：新建的那一刻，clapi 把路径定为 `/opt/cloudland/pools/<池UUID>`，不能修改。这一步只在数据库里登记，**不会碰任何机器、任何盘**。

**第 2 步：在某台机器上添加这个池，盘与池的对应在这时设置。**

- **在哪里**：计算节点详情页的「存储」标签页（§8.3）。
- **什么时候**：机器注册上线之后的任何时候，管理员逐台操作：
  1. 点「扫描」，列出这台机器的盘及状态（§4.2）。
  2. 点「添加存储池」：选第 1 步建好的池，勾选这台机器上的盘，选布局（单盘 / 线性 / RAID1），输入机器名确认。所选盘的介质与池的介质标签不一致、或所选盘之间介质不一致时，要显式确认（§2.6）。

对应接口 `POST /hypers/:hostid/storage_pools`，执行过程：

```
clapi: insert hyper_storage_pools row (host, pool, layout, disk ids, vg name, status=creating, deadline)
  | dispatch to that host (inter=<hostid>), the node runs it through async_exec
  v
node create_local_pool.sh (§4.3):
  re-check disks -> mdadm RAID1 (if chosen) -> LVM vg (tagged with the pool uuid) -> mkfs.xfs
  -> mount at /opt/cloudland/pools/<pool uuid>, fstab by file system UUID
  -> create volumes/ nvram/ tmp/, write marker .cloudland-pool (pool uuid + hostid)
  | callback local_pool_status ready|error
  v
clapi: status=ready, record capacity (creating rows past their deadline become error)
```

**之后的维护**都在同一个标签页：加盘（§4.4）、删池（§4.5）、宣告丢失（§5.8）、接管（§5.9）。

**对应关系存在哪里**：

| 位置 | 记录的内容 |
|---|---|
| clapi `hyper_storage_pools` | 哪台机器、哪个池、哪几块盘、布局、卷组名、容量、状态。界面显示的就是这里 |
| 节点的 LVM / mdadm | 盘 → 阵列 → 卷组 → 逻辑卷。卷组带 LVM 标签 `cloudland_pool=<池UUID>` |
| 节点的 `/etc/fstab` | 文件系统 UUID → 池路径，开机自动挂载 |
| 池根目录下的 `.cloudland-pool` | 池 UUID 和 hostid |

节点上**没有专门的 CloudLand 配置文件**，不需要手工编辑 `cloudrc.local`。

### 2.6 分配盘时的检查（介质与容量）

建池、加盘时，除了 §8.1 的状态检查（空闲、是否清除、盘数），clapi 在下发前还做下面的检查。**检查在后端做**，界面只负责展示结果和收集确认，直接调接口也绕不过去。

| 检查 | 触发条件 | 处理 |
|---|---|---|
| 盘与池的介质 | 池的 `Media` 不为空，所选盘里有介质与它不同的 | 返回 400，列出这些盘和各自的介质；请求带 `allow_media_mismatch: true` 时放行 |
| 所选盘之间的介质 | 建池时所选盘的介质不全相同；加盘时新盘与池里已有成员的介质不同 | 同上，共用 `allow_media_mismatch` |
| RAID1 一对盘的容量 | 同一对的两块盘容量相差超过 1% | 不拦截。响应和界面给出每对的实际可用容量（按小的那块算）和浪费的容量 |

- **介质不一致只要求确认、不绝对禁止**：自动识别不完全可靠，硬件 RAID 卡后面的 SSD 常被报成旋转盘，部分 SAN 盘、虚拟盘也一样。识别错了应当纠正（见下一条）；真要混用时（例如临时拿 SSD 顶替 HDD 池的坏盘）也有路可走。
- **以什么介质为准**：扫描得到的是自动识别值（§4.2）。管理员可以在扫描结果里手工修改某块盘的介质（`PATCH /hypers/:hostid/disks/:id`），之后的检查一律按修改后的「生效介质」比较。手工指定的值重新扫描不会丢（§3.3）；界面标明是「自动识别」还是「手工指定」，并显示自动识别的原值。
- **所选盘之间也要一致**：一块 SSD 和一块 HDD 组 RAID1，写入要等两块都完成，整个阵列被拖到机械盘的速度；线性拼接时卷落在哪块盘上由 LVM 分配，同一个池的性能忽快忽慢。池的 `Media` 为空时，这一条照样检查。
- **池里已有成员的介质**取 `hyper_storage_pools.Devices` 里记录的值（建池、加盘时写入的生效介质）。
- **RAID1 的配对**：按请求里 `disks` 的顺序每两块一对（第 1、2 块一对，第 3、4 块一对……），界面按对展示，管理员可以调整顺序。设 1% 的阈值是为了忽略同标称容量、不同批次的盘之间几 MB 的差别。
- **修改存储池的 `Media`**：已有机器上的成员盘与新标签不一致时只提示（列出这些机器），不拦截，已建好的池照常使用。
- **留痕**：放行了介质不一致的，审计记录里注明（§8.4）；节点存储池详情里给不一致的成员盘加标记，方便以后排查性能问题。

---

## 3. 数据模型

表结构由启动时的 AutoMigrate 创建，不写迁移代码。**`storage_pools`、`hyper_storage_pools` 由本文创建**：L0a 建 `storage_pools`（只有内置 `local` 池），L1 建 `hyper_storage_pools`。字段沿用 GPFS 设计 §4.1、§4.2 的定义，只对 GPFS 有意义的字段（`storage_pools` 的 `FsName`、`Fileset`、`FsType`、`CloneMode`，`volumes` 的 `BaseImageStorageID`）等执行 GPFS 设计时再加。

**删除一律用硬删除**（`Unscoped().Delete`）：这几张表带唯一索引，软删除的行仍占着唯一值，删了再建会冲突。

### 3.1 `storage_pools`

在 GPFS 设计 §4.1 的基础上：

| 字段 | 说明 |
|---|---|
| `Media string` | `ssd` / `hdd` / `nvme` / 空。用于展示，以及建池、加盘时的介质检查（§2.6），**不决定迁移行为** |
| `FallbackGroup string` | **可互换组**。组名相同、且都不为空的池，在迁移时可以自动互换（§7.1）。为空表示不参与自动互换。内置 `local` 池固定为空 |
| `Shared`（不是数据库列） | 由 `Driver` 现算：`gpfs` 为 true，`local` 为 false。接口响应里输出 `shared` 字段，前端据此显示「共享 / 本地」 |

- 非内置本地池的 `MountPath` 创建时自动设为 `/opt/cloudland/pools/<UUID>`，不能修改。
- 池级容量是各机器的合计，**查询时汇总**，不入库。
- **默认池**：建议保持内置 `local`。在 L4 之前，默认池只影响数据盘；把一个不是每台机器都有的池设为默认时，界面给出警告。
- **删除存储池**：还有任何卷（包括未落盘的）或任何节点存储池记录时拒绝。

### 3.2 `hyper_storage_pools`

每条记录表示「这台机器上这个池的目录」。唯一索引 `(hostid, pool_id)`。

```go
type HyperStoragePool struct {
	Model
	Hostid     int32      `gorm:"uniqueIndex:idx_hyper_pool"`
	PoolID     int64      `gorm:"uniqueIndex:idx_hyper_pool"`
	Status     string     `gorm:"type:varchar(32)"` // creating | ready | degraded | unavailable | removing | lost | error
	Reason     string     `gorm:"type:varchar(256)"`
	CheckedAt  time.Time
	DeadlineAt *time.Time // creating / removing / extending give up after this
	// local pools only
	Layout        string `gorm:"type:varchar(16)"` // builtin | single | linear | raid1
	Devices       string `gorm:"type:text"`        // JSON: [{id, serial, model, size_bytes, media}]
	VgName        string `gorm:"type:varchar(64)"` // cl_<first 8 of pool uuid>_<4 random hex>, unique per host
	CapacityBytes int64  // raw file system size
	UsedBytes     int64  // raw file system used
	CapacityAt    *time.Time
	SyncPercent   int    // RAID1 resync progress, 100 when in sync
}
```

- **内置池也有记录**：节点第一次上报时自动创建，`Layout=builtin`，容量为 `$image_dir` 所在文件系统的原始大小。
- `Status` 的含义与允许的操作：

| 状态 | 含义 | 接受新卷 | 允许的操作 |
|---|---|---|---|
| `creating` / `removing` | 命令已下发，等待回调。超过 `DeadlineAt`（30 分钟）转为 `error` | 否 | — |
| `ready` | 挂载保护通过，存储层健康 | 是 | 全部 |
| `degraded` | RAID1 缺盘或正在同步，仍可读写 | 是（并告警） | 全部 |
| `unavailable` | 没挂载、标记不对、LV 或阵列不可用，或 15 分钟没有上报 | 否 | 宣告丢失、删池（池里没卷时） |
| `lost` | 管理员确认数据已不可恢复（§5.8） | 否 | 只删记录、强制删池 |
| `error` | 建池或删池失败，`Reason` 写明原因 | 否 | 重试、删除记录 |

**「可用」在全文都指 `ready` 或 `degraded`**。

### 3.3 `hyper_disks`

磁盘扫描的结果，供建池、加盘、接管时选择：

```go
type HyperDisk struct {
	Model
	Hostid        int32  `gorm:"uniqueIndex:idx_hyper_disk"`
	DiskID        string `gorm:"uniqueIndex:idx_hyper_disk;type:varchar(256)"` // stable id, see below
	Name          string `gorm:"type:varchar(32)"`  // sdb, nvme0n1: display only
	Serial        string `gorm:"type:varchar(128)"`
	DiskModel     string `gorm:"column:model;type:varchar(128)"`
	SizeBytes     int64
	DetectedMedia string `gorm:"type:varchar(16)"` // ssd | hdd | nvme, as detected by the scan
	Media         string `gorm:"type:varchar(16)"` // effective media: DetectedMedia unless set by an admin
	MediaSource   string `gorm:"type:varchar(8)"`  // auto | manual
	State         string `gorm:"type:varchar(32)"` // free | dirty | in_use | system | cloudland_pool
	Detail       string `gorm:"type:varchar(256)"`
	PoolUUID     string `gorm:"type:varchar(64)"` // for in_use members of our pools and for cloudland_pool
	OwnerHostid  int32  // cloudland_pool: hostid written in its marker
	ScannedAt    time.Time
}
```

- **稳定标识**按优先级取 `/dev/disk/by-id/` 下的：`wwn-*` > `nvme-eui.*` > `ata-*` / `scsi-*`（每块盘通常同时有这几个链接）。测试环境放行 loop 设备时用 `loop:<backing file>`（§12）。
- 每次扫描按 `(hostid, DiskID)` 更新记录，这次没扫到的盘硬删除。`MediaSource=manual` 的记录更新时保留 `Media`，只刷新 `DetectedMedia`；手工值清空（恢复自动识别）时 `Media` 改回 `DetectedMedia`。盘从扫描结果里消失后，它的手工值随记录一起删除，再出现时要重新指定。

### 3.4 `volumes`

| 字段 | 变更 |
|---|---|
| `StoragePoolID int64` | 新增（L0a）。**启动时一次性回填**：值为 0 的行改为内置池的 ID（幂等的 `UPDATE`，不算迁移代码） |
| `Path` | 池内相对路径，由 clapi 在建记录时写入 |
| `Hyper` | 卷文件所在节点。数据盘在挂载回调里写（现状）；**系统盘在创建回调里写**（现在从不写，L0a 补上，否则准入和删池检查都会漏掉系统盘） |
| `Status` | 新增取值 `deleting`（§5.5）和 `lost`（§5.8） |

### 3.5 `storage_reservations`（新增）

容量预留。准入时在同一个事务里写入，节点回调成功后删除（此时卷的 `hyper` 已经写上），失败或过期时也删除：

```go
type StorageReservation struct {
	ID          int64 `gorm:"primaryKey"`
	Hostid      int32 `gorm:"index:idx_res_host_pool"`
	PoolID      int64 `gorm:"index:idx_res_host_pool"`
	VolumeID    int64
	MigrationID int64  // incoming disks of a migration, 0 otherwise
	Kind        string `gorm:"type:varchar(16)"` // attach | resize | boot | migration
	SizeGB      int32  // full size for attach/boot/migration, the increase for resize
	ExpiresAt   time.Time
}
```

- 过期时间：挂载、扩容、系统盘 30 分钟；迁移到迁移结束（完成或回滚时删除），另设 24 小时兜底。
- clapi 后台每 5 分钟清理过期的预留。

### 3.6 `migrations` 增加 `DiskPlan`

```go
// DiskPlan is fixed once the target host is known and then used unchanged by every later step (JSON):
// [{volume_id, device, booting, src_pool_id, src_path, dst_pool_id, dst_path, auto, reason}]
// plus, for a boot disk whose NVRAM lives in a pool (L4), {nvram: true, src_path, dst_path}
DiskPlan string `gorm:"type:text"`
```

- **生成时机**：管理员指定了目标机器时，创建迁移时生成；不指定时，在目标准备完成的回调（`target_prepared`）里生成（§7.1）。生成后不再重算。
- 只包含本地盘；`src_pool_id == dst_pool_id` 时 `src_path == dst_path`。
- 迁移详情接口返回它，界面显示每块盘从哪个池迁到哪个池。

---

## 4. 节点侧

- 新增脚本放在 `scripts/kvm/`，以 `|:-COMMAND-:|` 回调。扫描结果这类多行数据用 base64 编码成一个参数（节点回调里是第一次这样用；单行上限 1 MiB，`cloudlet/executor.go:36`，够用）。
- **锁**：建池、加盘、删池、接管拿 `flock -x /var/lock/cloudland-storage.lock`；落盘、扩容、迁移预建、删卷这些写池的操作拿共享锁 `flock -s`，保证删池时没有别的写入。
- **长任务走 `async_exec`**：建池、加盘、接管可能要几十秒到几分钟（`mdadm`、`mkfs`），直接执行会占住 cloudlet 的串行命令队列（默认 `CLOUDLET_CONCURRENCY=1`），这台机器上的其他操作全部排队。
- **池相关命令一律加超时**（`timeout 10 df ...` 等），坏盘上 `df`、`stat` 可能卡在 D 状态，不能拖住心跳。

### 4.1 依赖

`lvm2`、`mdadm`、`xfsprogs`。现在三台节点都有，但部署脚本（`deploy-compute-node.sh:233-238`）没有显式安装，L1 起加上。

### 4.2 扫描磁盘：`scan_host_disks.sh`

- 管理员点「扫描」触发（`inter=<hostid>`），不放进心跳。
- `lsblk -J -b -o NAME,PATH,TYPE,SIZE,ROTA,TRAN,MODEL,SERIAL,MOUNTPOINTS,FSTYPE,PKNAME`，加上 `pvs`、`vgs -o +vg_tags`、`/proc/mdstat`。
- 分类：
  - `system`：承载 `/`、`/boot`、swap、`/opt/cloudland/cache` 的盘（顺着分区、md、LVM 往上追）。**永远不能选**。
  - `in_use`：已挂载（例如现在 work-01 / work-03 的 `sdb1` 挂在 `/disk1`），或者属于别的 md、LVM；是本机已登记的存储池成员时带上 `PoolUUID`。
  - `cloudland_pool`：上面有带 `cloudland_pool=<UUID>` 标签的卷组，但**不属于本机已登记的池**，例如节点重新注册之后、或从别的机器搬来的盘。带上 `PoolUUID` 和标记文件里的 `OwnerHostid`。**不能清除，只能接管**（§5.9）。
  - `dirty`：没在用，但有分区表或文件系统签名（`wipefs -n` 能读到），例如现在 work-02 的 `sdb1`。可以在建池时选择「清除」。
  - `free`：整盘、没有任何签名。
- 只列 `TYPE=disk`，排除 rom、可移动设备；loop 设备只在测试开关打开时列出（§12）。
- 介质（自动识别值，写入 `DetectedMedia`）：`TRAN=nvme` 为 `nvme`，`ROTA=0` 为 `ssd`，其他为 `hdd`。硬件 RAID 卡、部分 SAN 盘和虚拟盘会把 SSD 报成旋转盘，识别错时由管理员手工纠正（§2.6）。
- 回调 `host_disks '<NODE_ID>' '<base64 JSON>'`。

### 4.3 建池：`create_local_pool.sh`

参数：`<池UUID> <根目录> <布局> <卷组名> [--wipe] <盘标识...>`。`<根目录>` 必须等于 `/opt/cloudland/pools/<池UUID>`，卷组名由 clapi 生成。

1. **再检查一遍每块盘**（不相信 clapi 的扫描结果）：整盘、不是 `system`、不是 `cloudland_pool`、没挂载、没有 holder。有签名时，只有带 `--wipe` 才执行 `wipefs -a`。
2. **按布局组装**：
   - `single`：一块盘作为 PV。
   - `linear`：多块盘都作为 PV。**没有冗余**。
   - `raid1`：盘数为偶数，每两块 `mdadm --create --level=1` 组成一个阵列（按传入顺序配对，同一对容量不同时阵列按小的那块算，clapi 事先已提示，§2.6），阵列作为 PV。写入 `/etc/mdadm/mdadm.conf`。**不用 `--assume-clean`**：阵列建好就能用，初次同步在后台进行（2 TB 机械盘要几个小时），期间池为 `degraded` 并上报进度。
3. `vgcreate --addtag cloudland_pool=<池UUID> <卷组名>`，`lvcreate -l 100%FREE -n data`。
4. `mkfs.xfs -K`（`-K` 不在建文件系统时对 SSD 整盘执行 discard）。选 xfs：大文件表现好，没有 ext4 单文件 16 TiB 的限制，支持在线扩容。
5. fstab：`UUID=<文件系统UUID> <根目录> xfs defaults,nofail,x-systemd.device-timeout=120s 0 2`，然后 `mount`。等待时间要比 mdadm 降级阵列的强制启动（约 30 秒）长。
6. 建 `volumes/`、`nvram/`、`tmp/`，写 `.cloudland-pool`（池 UUID、hostid），属主设为 `cland`。
7. 回调 `local_pool_status '<NODE_ID>' '<池UUID>' 'ready|degraded|error' '<原因>' '<容量>' '<已用>' '<同步进度>'`。
8. 失败时回滚已做的步骤（卸载、删 fstab 行、`vgremove`、`mdadm --stop` 与 `--zero-superblock`），再回调 `error`。已 `wipefs -a` 的盘恢复不了原签名，确认框里要写清楚。

### 4.4 加盘：`extend_local_pool.sh`

- `linear`：`pvcreate` → `vgextend` → `lvextend -l +100%FREE -r`，在线完成。
- `raid1`：每次加两块，组成新阵列 → `pvcreate` → `vgextend` → `lvextend -r`，在线完成。
- `single` 加一块变成 `linear`（无冗余）。「单盘加一块变成 RAID1」要搬数据，第一版不支持。
- 介质与容量检查见 §2.6：新盘与池的 `Media` 不一致、与池里已有成员不一致，都要显式确认；`raid1` 新加的一对容量不同时提示。
- 回调同 §4.3。

### 4.5 删池：`remove_local_pool.sh`

- **正常删除**：clapi 先校验这台机器上这个池里没有卷（`storage_pool_id=池 且 hyper=这台机器`，包括系统盘和「删除中」的卷）、没有未过期的预留、没有以它为目标的进行中迁移。节点：拿排他锁 → 检查 `volumes/` 为空，不为空就拒绝并列出里面的文件 → `umount` → 删 fstab 行 → `vgremove` → `mdadm --stop` / `--zero-superblock` → `wipefs -a` → 删 mdadm.conf 对应行。回调 `local_pool_status ... 'removed'`。
- **池里有残留文件**（删卷命令丢失、迁移残留等）：删池会被拒绝并列出文件。管理员确认后可以「强制删除」，节点删掉这些文件再继续。
- **强制删除丢失的池**（§5.8）：节点尽力清理，忽略 `umount`、`vgremove` 在坏盘上的报错；clapi 不管节点结果都删掉记录。

### 4.6 挂载保护：`pool_guard`

L1 在 `cloudrc` 里**新建**（GPFS 设计 §5.1 的同名函数属于 GPFS 阶段 1，本文先实施，所以由本文先写，GPFS 以后复用并增加它自己的检查）。本地池的检查：

1. `mountpoint -q <根目录>`；
2. `stat -f -c %T <根目录>` 为 `xfs`；
3. `.cloudland-pool` 里的池 UUID 与 hostid 都与本机一致（hostid 取 cloudlet 环境里的 `NODE_ID`）。

凡是写本地池的脚本，在第一次写入前都必须调用它，并且**必须在要写入的那台机器上本地执行**（经 ssh 执行时拿不到 `NODE_ID`，所以迁移时的目标端检查放在 `target_migration.sh` 里，§7.3）。**不对池根目录做 `mkdir -p`**。

### 4.7 状态与容量上报

- 放进心跳 `report_rc.sh`，**限频**：状态变化时立即上报，否则每 5 分钟一次（时间戳文件节流）。
- **节点要知道自己有哪些池，不能只靠标记文件**（标记文件在池自己的文件系统上，卸载后就看不见了）：
  - 以 `/etc/fstab` 里挂载点在 `/opt/cloudland/pools/` 下的条目为准；
  - 对每个条目：已挂载就执行 `pool_guard` 并取 `df -B1`、阵列与 LV 状态；**没挂载就尝试 `mount` 一次**（晚到的设备可以自动恢复），仍然失败就立即上报 `unavailable` 和原因。
- 阵列状态：`mdadm --detail --test` 退出码 1 为 `degraded`，2 为 `unavailable`；同步中取 `/proc/mdstat` 的进度。
- **内置池**：上报 `$image_dir` 所在文件系统的原始 `df -B1`（大小、已用），不做超分、不减虚拟大小。
- 回调 `local_pool_status`。clapi 忽略处于 `creating` / `removing` / `lost` 状态的记录收到的周期上报（只认对应命令的结果回调）；超过 15 分钟没收到上报的可用记录转为 `unavailable`。
- `degraded`、`unavailable` 接入告警（节点告警增加「本地存储池异常」类型），阶段 L3。

### 4.8 开机：待启动列表

现在 `report_rc.sh` 的 `sync_instance` 在节点重启后逐台 `virsh start`（第 219 行），循环结束后无条件写入 boot_id 标记（第 222 行），之后不再拉起。**不能在这里等池挂载**：`report_rc.sh` 就是心跳，cloudlet 会等它退出（`cloudlet/reporter.go`）。每台虚拟机等几分钟，节点会被 cland 判离线，这正是 WDS 等待设备 200 秒、节点离线约 7 分钟的旧问题（见 CLAUDE.md 负载均衡一节）。

改为：

- 开机后第一次同步时，磁盘所在的池没有通过挂载保护的虚拟机不启动，写进待启动列表（`$cache_dir/pending_start`），并上报原因。
- 之后每次心跳检查一遍列表：池可用了就启动并移出列表；**`virsh start` 失败就留在列表里并上报原因**，不再回报 `running`（现在第 219–220 行不管成败都回报 running，是现有缺陷，一并修掉）。
- boot_id 标记照旧写，待启动列表单独维护，两者互不影响。
- 管理员可以在节点详情页看到待启动列表和原因。

---

## 5. 各操作流程

以下「本地池」都指非内置的本地存储池；内置池除挂载保护外行为相同。

### 5.1 创建数据卷

- 请求带 `storage_pool: {id}`；不传就用默认池。
- 与现在一样**不落盘**：建记录，`path=volumes/volume-<id>.disk`，状态 `available`，`hyper=0`。
- 区域内没有任何机器的这个池处于可用状态时，照样允许创建，响应里带提示，界面显示「当前没有可用的节点」。
- 未落盘的卷扩容只改记录（现状），不需要准入。

### 5.2 首次挂载（落盘）

1. clapi 校验：虚拟机所在机器上这个池的记录存在且可用；否则返回 400，例如「work-01 没有存储池 local-ssd」或「work-02 上的 local-ssd 当前不可用：未挂载」。
2. **容量准入与预留**（§6）：在同一个事务里锁定这条节点存储池记录，计算已分配 + 预留 + 本次大小，通过后写入 `attach` 预留。
3. 下发 `attach_volume_local.sh '<实例ID>' '<卷ID>' '<绝对路径>' '<卷UUID>' '<GB>' '<池UUID>'`：使用传入的绝对路径（现在是 `$volume_dir/$3`，第 10 行）；拿共享锁；`qemu-img create` 之前执行 `pool_guard`（内置池跳过）。
4. 回调成功：写 `hyper`（现状，`rpcs/attach_volume.go:80-81`），删除预留。失败：删除预留。

### 5.3 已落盘卷的挂载、卸载

- 已落盘的卷只能挂给同一台机器上的虚拟机（`services/volume.go:447`，不变）。
- 卸载不依赖路径，不变。

### 5.4 扩容

- clapi 先检查卷所在机器上这个池可用，不可用直接返回 400（不下发，避免节点上 `pool_guard` 失败、读不到镜像大小而把卷置为 error）。
- 准入并写 `resize` 预留（增量部分）。
- 下发时额外带上原容量；`resize_volume_local.sh` 拿共享锁、执行 `pool_guard`；失败时优先回报镜像的实际大小，读不到时回报 clapi 传来的原容量，clapi 按现有逻辑回滚大小并保持卷可用（`rpcs/resize_volume.go`）。
- 回调后删除预留。

### 5.5 删除卷

现在的流程是先在数据库里软删，再异步下发 `clear_volume_local.sh`（`services/volume.go:520-539`），脚本只有一句 `rm -f`、没有回调；节点离线时 `HyperExecute` 只告警、命令被丢弃（`common/clients.go:132-139`），文件就残留下来。改为：

- **未落盘的卷**：直接删记录（现状）。
- **已落盘的卷**：
  1. clapi 检查卷所在机器在线、池可用，否则返回 400 并说明原因。
  2. 卷置为 `deleting`，下发 `clear_volume_local.sh '<卷ID>' '<卷UUID>' '<绝对路径>' '<池UUID>'`。
  3. 节点：拿共享锁、`pool_guard`、删除文件，回调 `clear_volume '<卷ID>' 'deleted|error' '<原因>'`（新增回调与处理函数）。
  4. `deleted`：软删记录，释放配额（现状的配额逻辑挪到这里）。`error`：恢复为 `available` 并记录原因。
  5. `deleting` 超过 30 分钟没有回调，恢复为 `available`，允许重试。
- **池已宣告丢失的卷**：只删记录（§5.8）。

### 5.6 系统盘放在本地存储池（阶段 L4）

- `POST /instances` 的 `storage_pool` 可以指定本地池（L4 之前只接受内置池）。
- **调度**：clapi 把候选机器收窄为「该池在这台机器上可用、且容量准入通过」的机器，拼进 `select=group-...:<hostid 列表>` 交给 cland 按资源挑选；批量创建的每一台都走这个收窄后的候选组（现在批量创建只有第一台按指定节点下发，`services/instance.go:278`）。系统盘的 `boot` 预留在目标确定、下发创建命令前写入。
- **管理员先选宿主机、再选池**：选了宿主机之后，系统盘存储池下拉框只列出这台机器上可用的池和剩余容量，放不下的置灰。选定「机器 + 池」就唯一确定了位置。**指定宿主机要求系统管理员身份**：原先后端没有这项检查，已单独修复（附录 B 第 1 条）。
- `rcNeeded` 里的 `disk`：只计内置池里的系统盘（cland 按节点的 `disk_over_ratio` 判断）；系统盘在其他池时填 0，由 clapi 按池准入。
- 元数据里的 `boot_disk`（GPFS 设计 §6.5）：`driver=local`，`pool_root`、`path`、`nvram` 都在池里。`launch_vm.sh` 先 `pool_guard`，再把镜像从本地缓存转换到这个路径。
- 重装、救援、捕获镜像、删除虚拟机都使用下发的系统盘路径（L0b）。

### 5.7 让虚拟机落在有所需存储池的机器上（阶段 L4，可选）

- `POST /instances` 增加可选参数 `required_storage_pools: [{id}, ...]`，候选机器按「这些池都可用」收窄。
- 对已经存在的虚拟机，挂载弹窗把「所在机器没有这个池」的虚拟机置灰并说明原因（§8.3）。

### 5.8 盘坏了：宣告池丢失

- **进入**：节点存储池为 `unavailable`，管理员确认数据无法恢复后，点「宣告丢失」，状态变为 `lost`。
- **池里的卷**：已落盘在这个节点存储池里的卷全部置为 `lost`。`lost` 卷允许：
  - **只删记录**（不下发节点命令），释放配额；
  - **强制从虚拟机上卸下**：只从域定义里删掉这块盘（`virsh detach-device --config`，运行中的虚拟机下次重启生效），不访问文件。
- **系统盘在丢失池里的虚拟机**：只能删除（节点上 `destroy` / `undefine`，不删盘文件）。
- **池本身**：可以强制删除（§4.5）。
- 这条流程让「单盘池坏了」有出路：否则删卷时挂载保护失败、删池要求池里没卷、坏盘上 `umount` 也会失败，形成死锁。

### 5.9 节点重新注册与接管

背景：节点离线后无法直接重新部署，只能删除节点再注册（`HyperAdmin.Deploy` 只在 status 4/5 允许重试，`services/hyper.go:341`），注册后得到新的 hostid（work-02 就从 2 变成了 4）。重装系统时数据盘通常还在（实测 work-02 的 `sdb1` 在 OS reload 后仍然存在）。

- **删除节点前的检查**（现在 `HyperAdmin.Delete` 只检查有没有实例，`services/hyper.go:569-576`）：这台机器上还有节点存储池记录，或有 `hyper=这台机器` 的卷时，拒绝删除，提示先迁走或宣告丢失。管理员也可以选择「保留池以便接管」：记录这台机器的池和卷的对应关系后再删除节点，这些卷置为 `lost`（可恢复），等待接管。
- **接管**：新节点注册后扫描，带 CloudLand 卷组标签的盘显示为 `cloudland_pool`（§4.2），不能清除。管理员点「接管」：
  1. 节点 `adopt_local_pool.sh <池UUID> <新hostid>`：激活卷组、挂载、改写标记文件里的 hostid、重建 fstab 和 mdadm.conf，回调 `local_pool_status`。
  2. clapi 为新 hostid 建节点存储池记录；把这个池里 `hyper=旧hostid` 的卷改为新 hostid，`lost` 恢复为 `available` 或 `attached`（按是否还挂在虚拟机上）。旧 hostid 从标记文件里读出。
- 需要验证：Ubuntu 26.04 的 LVM devices file（`/etc/lvm/devices/system.devices`）在重装系统后是否会让旧卷组默认不可见；如果是，接管脚本要先 `vgimportdevices`（§13）。

---

## 6. 容量准入

对「机器 H 上的池 P」：

- **已分配** = `storage_pool_id=P`、`hyper=H`、未删除（包括 `deleting`）的卷的 `size` 之和 + 这条记录上未过期的预留之和。
- **准入条件**：已分配 + 本次新增 ≤ `CapacityBytes × 超分比例`，并且 `UsedBytes / CapacityBytes` < 90%。
  - 非内置池的超分比例取存储池的 `OverRatio`。
  - **内置池取这台机器的 `disk_over_ratio`**（`hyper_status` 回调已把它存进 `hypers.disk_over_rate`，`rpcs/hyper_status.go:115-119`），与 cland 对系统盘的判断用同一个比例，避免两套阈值结论不一。
- **并发**：准入在一个事务里完成——`SELECT ... FOR UPDATE` 锁住这条节点存储池记录，计算，写入预留，提交。预留在节点回调前就计入，所以并发的另一个请求一定能看到。
- **容量未知**（还没收到上报）：非内置池拒绝（记录一定来自建池回调，不存在「未知但能用」）；**内置池放行并记日志**，避免部署 L1 后第一次上报之前所有挂载都被拒。
- **迁移**：目标机器上每个目标池按汇总后的迁入量准入，并写 `migration` 预留，迁移结束时删除。

注意：qcow2 精简置备，预建的空盘只占几 KB，**空间不够并不会在预建时失败**，要到复制途中才报错。所以迁移前的准入是唯一的防线，不能省。

---

## 7. 迁移

迁移接口只允许系统管理员调用（`services/migration.go:29`）。

### 7.1 目标机器与迁移计划

**每块本地盘的目标池**：

| 情况 | 目标池 |
|---|---|
| 请求里为这块盘指定了 `storage_pool` | 用指定的池，必须在目标机器上可用 |
| 没有指定，目标机器上有同一个池且可用 | 留在同一个池 |
| 没有指定，目标机器上没有原池，但有**同一个可互换组**的池 | 自动替换（请求参数 `allow_pool_fallback=false` 时按下一行处理） |
| 以上都不满足 | 失败，列出这块盘在目标机器上可选的池 |

- **可互换组**（`FallbackGroup`）：只在组名相同、且都不为空的池之间自动替换；多个候选时选剩余空间最多的。内置 `local` 池不参与。用可互换组而不是「介质相同」，是为了不和「用不同的池做隔离」冲突（§2.2）。
- **可见性**：自动替换写进计划并附上原因（例如「原池 local-hdd-a 在目标机器上不存在，替换为同组的 local-hdd-b」），迁移详情里可以看到。
- `allow_pool_fallback` 是请求参数，默认 true。迁移只有系统管理员能发起，不必做成系统设置。

**指定了目标机器**：创建迁移时就生成计划、做准入、写预留，失败直接返回 400。

**不指定目标机器**（包括维护模式批量迁移）：现在是下发 `select=<组> <资源需求>` 由 cland 挑选，clapi 要等 `target_prepared` 回调才知道目标（`services/migration.go:145-169`、`rpcs/migrate_vm.go:404-411`），cland 也不支持优先级。所以：

1. clapi 先算两组候选机器：
   - G1：每块本地盘都能留在原池，且按池汇总后容量够；
   - G2：每块盘都能留在原池或按可互换组替换，且容量够。
2. G1 不为空就用 `select=group-...:<G1>` 下发，否则用 G2，都为空就失败。这样既实现了「优先不换池」，又不需要 cland 支持优先级。
3. `target_prepared` 回调到达、目标确定后，clapi 生成计划、准入、写预留；此时准入失败（例如刚好被别的请求占满）就走现有的回滚流程。

**维护模式批量迁移**：现在整批放在一个事务里，逐台下发，一台失败就整体回滚，而前面已经下发的命令收不回（`services/migration.go:35-40`、`:106-110`、`:190`）。改为先对所有虚拟机算候选组，没有候选的跳过（沿用现有的 `not_doing` 做法），其余再逐台下发；`Maintain` 返回每台虚拟机的结果和原因（现在只返回一个 error，`services/hyper.go:534-541`）。

### 7.2 路径

- 目标路径 = 目标池根目录 + 这类盘在池里的相对路径（§2.3）。
- 留在同一个池时，源路径和目标路径相同，与现在的行为一致。
- 换池时路径不同，按 §7.3 改写。

### 7.3 执行流程

磁盘清单改为读取计划，不再从 `virsh domblklist` 里挑出所有文件型磁盘（`source_migration.sh:49`）。

1. **目标端检查与预建，放在 `target_migration.sh` 里**（它在目标机器本地运行，有 `NODE_ID`；现在的预建是源端经 ssh 做的，拿不到 `NODE_ID`，也做不了 hostid 检查）：对每个目标池执行 `pool_guard`（内置池除外）；目标路径已存在就失败。运行中的虚拟机，按计划预建同样虚拟大小的空 qcow2。
2. **源端**：关机的虚拟机把磁盘 `scp` 到计划里的目标路径（目标目录已由第 1 步确认）；**不再 `mkdir -p $(dirname $path)`**（`source_migration.sh:64`、`:67`），池没挂载时这一步会把磁盘写进根分区。
3. **改写域定义**（只在有盘换池时）：源端 `virsh dumpxml --security-info --migratable`，替换换池的盘的 `<source file>`（和 NVRAM 路径），先用 `virt-xml-validate` 校验，通过后热迁移加 `--xml <新配置> --persistent-xml <新配置>`（第 123 行），冷迁移加 `--persistent-xml <新配置>`（第 118 行）。libvirt 允许目标配置里的磁盘源路径与源端不同（L2 开工前实测，§13）。
4. **数据盘的描述文件**：`$xml_dir/<实例>/disk-<卷ID>.xml` 里写着磁盘路径，现在原样复制到目标（第 79 行）；换池的盘先替换路径再复制。
5. **NVRAM**：在池里的 NVRAM（L4）**不论是否换池都按计划复制和清理**；现在只复制 `$image_dir` 下的（`source_migration.sh:81`），目标端的模板也只放在 `$image_dir`（`target_migration.sh:37`）。
6. **完成**：`async_job/complete_migration.sh:24` 在目标上重新保存域定义。clapi 收到 `completed` 后，在一个事务里更新每块盘的 `storage_pool_id`、`path`、`hyper`（现在只更新 `hyper`，`rpcs/migrate_vm.go:306-311`），删除迁移预留。
7. **源端清理**：`finish_source_migration.sh` 按计划删除源路径的文件（现在按 `$volume_dir/*`、`$image_dir/*` 前缀删，第 31–33 行，**池里的文件不会被删**），保留「源机器上还定义着这台虚拟机就跳过清理」的保护（第 19–22 行）。
8. **回滚**：`async_job/clear_target_migration.sh` 按计划删除目标路径上预建的文件和 NVRAM（现在第 48 行按前缀删、第 51 行按固定文件名删 `inst-N.disk`、第 39 行删 `$image_dir` 下的 NVRAM），删除迁移预留。

第 1、2、6、7、8 步在 **L1**（同池迁移）就要做：否则同池迁移后源端池里残留文件，迁回原机器或失败后重试都会报「目标上已存在同名文件」。第 3、4 步是 **L2**（换池）。第 5 步是 **L4**。

### 7.4 其他规则

- 有本地盘的虚拟机仍然不允许 `force`，源机器必须在线。
- 迁移进度不受影响（`domjobinfo` 统计全部复制量）。
- 同一台机器上换池不走迁移流程，见 L5。

---

## 8. 接口与前端

### 8.1 clapi 接口

新增接口（节点相关路由沿用网关里已有的通配名 `:hostid`，否则 gin 注册时会冲突）：

| 接口 | 权限 | 说明 |
|---|---|---|
| `GET /storage_pools` | 所有成员 | 本地池返回 `shared=false`、`media`、`fallback_group`、可用机器数；系统管理员另外看到合计容量 |
| `POST / PATCH / DELETE /storage_pools[/:id]` | 系统管理员 | 存储池增删改（L1 新建，先只支持 `driver:"local"`，GPFS 设计以后增加 `gpfs`）。`mount_path` 自动生成 |
| `GET /storage_pools/:id/hypers` | 系统管理员 | 每台机器的布局、盘、容量、已分配、预留、状态 |
| `POST /hypers/:hostid/disks/scan`、`GET /hypers/:hostid/disks` | 系统管理员 | 扫描与结果 |
| `PATCH /hypers/:hostid/disks/:id` | 系统管理员 | `{media:"ssd"}` 手工指定介质，`{media:""}` 恢复自动识别（§2.6）。`:id` 是 `hyper_disks` 的记录 ID（盘标识里可能有 `/`，不适合放进路径） |
| `GET /hypers/:hostid/storage_pools` | 系统管理员 | 这台机器上的节点存储池（含内置池） |
| `POST /hypers/:hostid/storage_pools` | 系统管理员 | `{pool:{id}, layout, disks:[id...], wipe, allow_media_mismatch, confirm:"<机器名>"}` |
| `POST /hypers/:hostid/storage_pools/:pool_id/extend` | 系统管理员 | `{disks, wipe, allow_media_mismatch, confirm}` |
| `POST /hypers/:hostid/storage_pools/:pool_id/lost` | 系统管理员 | 宣告丢失，`{confirm}` |
| `POST /hypers/:hostid/storage_pools/adopt` | 系统管理员 | 接管，`{pool_uuid, confirm}` |
| `DELETE /hypers/:hostid/storage_pools/:pool_id` | 系统管理员 | `?force=true` 用于残留文件或丢失的池 |
| `GET /instances/:id/migration_targets` | 系统管理员 | 候选目标机器，以及每块本地盘能否留在原池、可替换的池、全部可选的池和剩余容量 |

已有接口的变化：

| 接口 | 变化 |
|---|---|
| `POST /volumes` | 增加 `storage_pool` |
| `DELETE /volumes/:id` | 已落盘的卷变为异步，返回 202，卷先进入 `deleting`（§5.5） |
| `POST /instances` | L4 增加系统盘的 `storage_pool` 与 `required_storage_pools`；指定 `hypervisor` 要求系统管理员（已单独修复，附录 B 第 1 条） |
| `POST /migrations` | 增加 `disks: [{volume:{id}, storage_pool:{id}}]`、`allow_pool_fallback` |
| `GET /migrations/:id` | 增加 `disk_plan` |
| `POST /hypers/:hostid/maintain` | 返回每台虚拟机的迁移结果 |
| `DELETE /hypers/:hostid` | 增加对节点存储池和卷的检查（§5.9） |

所选盘必须在 24 小时内的扫描结果里是 `free`，或 `dirty` 且 `wipe=true`；`cloudland_pool` 只能用于接管。`raid1` 的盘数为偶数，`single` 只能一块。介质与 RAID1 容量的检查见 §2.6：介质不一致时返回 400，响应里列出不一致的盘及其介质，界面据此展示；RAID1 容量不一致不拦截，界面按扫描结果里的容量自行算出每对的可用容量并提示，成功响应里也带上这些数值。

### 8.2 cpgateway

- `proxy_routes.go` 加入**新增**的路由：`GET /storage_pools` 所有成员可用，其余新增路由标记为系统管理员。已有路由的标记不变（`POST /instances`、`/migrations` 等仍由 clapi 校验权限）。
- 配额仍只有一个 `disk_gb`。删卷改为异步后，配额在删除回调成功时才释放。

### 8.3 前端

- **计算节点详情页「存储」标签页**：
  - 「磁盘」表格：名称、标识、型号、容量、介质（手工指定的加「手工」标记，悬停显示自动识别的原值；行内可修改）、状态（空闲 / 有旧数据 / 使用中 / 系统盘 / CloudLand 池（可接管））、所属的池。「扫描」按钮。
  - 「存储池」表格：池名称、介质、布局（「单盘」「线性（无冗余）」「RAID1」）、成员盘、容量条（已用 / 已分配 / 预留 / 总量）、状态（降级用警告色，同步中显示进度）、卷数；操作：加盘、删除、宣告丢失。内置池一行只读。
  - 「待启动的虚拟机」：开机后因池不可用而没有启动的虚拟机及原因（§4.8）。
  - 「添加存储池」弹窗：选池、勾盘、选布局；「线性」标出无冗余；每块盘显示生效介质，与池的标签或与其他所选盘不一致的标红，要勾选「确认使用介质不一致的盘」才能提交（提交时带 `allow_media_mismatch`）；`raid1` 按对显示，给出每对的可用容量，容量不一致时提示浪费多少；最后输入机器名；选中有旧数据的盘时单独列出将被清除的盘。「加盘」弹窗同样处理。
  - 「接管」弹窗：列出 `cloudland_pool` 盘所属的池、原节点 ID、里面的卷数，确认后接管。
- **「存储池」管理页**（L1 新建，GPFS 以后加入同一页面）：本地池显示「本地」标签、可互换组、`x/y` 台机器具备、合计容量；详情页列出每台机器。
- **创建云硬盘**：存储池下拉框，显示「本地 / 共享」、介质和具备这个池的机器。
- **挂载弹窗**：未落盘的卷，把所在机器没有这个池或池不可用的虚拟机置灰并说明原因；挂载提示按池显示。
- **云硬盘列表、详情**：存储池、所在机器；`deleting`、`lost` 状态的文案与操作（`lost` 只有「删除记录」「强制卸下」）。
- **创建虚拟机**（L4）：系统盘存储池；管理员选了宿主机时只列这台机器上的池；可选的「还需要这些存储池」。
- **迁移弹窗**（L2）：选目标机器后逐盘显示目标池，默认「留在原池」；目标机器没有原池时预填可互换组里的池并标注「自动选择」；每项显示剩余容量，放不下的置灰；有盘无处可去的机器置灰并说明是哪块盘。**迁移详情**显示计划。
- **维护模式**：显示每台虚拟机的迁移结果。
- 三种语言的文案，通过 `npm run i18n:check`。

### 8.4 审计

`audit_actions.go` 增加：`storage_pool.create`、`storage_pool.update`、`storage_pool.delete`、`hyper.disks_scan`、`hyper.disk_update`（手工指定介质）、`hyper.storage_pool_create`、`hyper.storage_pool_extend`、`hyper.storage_pool_delete`、`hyper.storage_pool_lost`、`hyper.storage_pool_adopt`。建池、加盘、接管的记录带上盘的标识、是否清除、是否放行了介质不一致。迁移的审计动作不变，换池信息在 `disk_plan` 里。

---

## 9. 安全与风险

| 风险 | 应对 |
|---|---|
| 格式化了错误的盘 | 稳定标识；节点执行前重新检查；`system`、`cloudland_pool` 永远不能清除；有签名的盘要显式 `wipe`；输入机器名；审计 |
| 节点重新注册后，管理员把原来的池当旧数据清除 | 扫描识别 CloudLand 卷组标签，归为 `cloudland_pool`，只能接管（§5.9） |
| 池没挂载时卷写进根分区 | `pool_guard`；不对池根目录 `mkdir -p`；迁移预建也在目标端先 `pool_guard` |
| 线性池或单盘池坏盘，卷全丢 | 建池时明确选择布局并醒目提示；推荐 RAID1；本地卷不支持备份，界面要说清；宣告丢失流程保证不死锁（§5.8） |
| RAID1 降级没人发现 | 心跳上报 `degraded` 与同步进度，接入告警 |
| 并发请求超分 | 事务内准入 + 预留（§6） |
| 精简置备写满 | 准入 + 90% 实际用量阈值；预建空盘不会暴露空间不足，迁移前准入不能省 |
| 开机时池没挂好 | fstab `nofail` + 更长的设备等待；心跳尝试挂载；待启动列表，不阻塞心跳（§4.8） |
| 坏盘让 `df` 卡住 | 池相关命令加超时 |
| 盘名变化 | 稳定标识；fstab 用文件系统 UUID；mdadm.conf |
| 盘从一台机器搬到另一台 | 卷组名带随机后缀，不会重名；标记文件的 hostid 不符时为 `cloudland_pool`，需要接管 |
| 建池做到一半失败或节点离线 | 失败回滚；`creating` / `removing` 超时转 `error`，可以重试或删除 |
| 删池时有写入 | 写操作拿共享锁，删池拿排他锁；clapi 检查卷、预留、进行中的迁移 |
| 换池迁移改写配置出错 | 只改 `<source file>` 和 NVRAM；`virt-xml-validate` 校验；L2 验收覆盖热迁移、冷迁移、UEFI |
| 删卷命令丢失留下残留文件 | 删卷改为回调确认（§5.5）；删池时列出残留文件，允许确认后强制删除 |
| 介质识别错误或混用，池的性能与标签不符 | 后端检查盘与池、盘与盘的介质，不一致要显式确认并记审计；识别错误可以手工纠正；RAID1 容量不一致给出提示（§2.6） |

---

## 10. 部署与现有环境

- `deploy-compute-node.sh` 安装 `lvm2`、`mdadm`、`xfsprogs`，创建 `/opt/cloudland/pools`（L1）。**不自动建池**。
- 部署脚本第 346 行 `chown -R cland:cland /opt/cloudland` 会深入挂载在 `/opt/cloudland/pools/` 下的池（现在对 `cache/` 下的虚拟机磁盘也是如此）。改为只处理它自己建的目录，或加 `--one-file-system` 类的限制（L1）。
- 可选：部署时通过环境变量指定盘和池，节点注册后由 clapi 自动下发建池命令（L3 之后）。
- **现有三台机器**（L1 验收时）：
  - work-01、work-03：`sdb1` 挂在 `/disk1`，扫描会显示为「使用中」。先卸载并删掉 fstab 里的对应行，再在界面上「清除 + 建池」。
  - work-02：`sdb1` 没挂载，显示为「有旧数据」；**先确认里面没有需要保留的数据**。
  - 每台只有一块空闲盘，只能用单盘布局（无冗余）。

---

## 11. 实施阶段

顺序：**L0a → L1 → L2 → L3 → L0b → L4 → L5**。L0 拆成两部分：数据盘相关的抽象先做（L0a），系统盘、救援、重装、捕获镜像和 WDS 清理（L0b）等到系统盘要放进池（L4）之前再做，这样 L1–L3 不必等 WDS 的去留决定。

### 阶段 L0a：存储池抽象（数据盘部分）

**范围**：

- `storage_pools`，启动时创建内置 `local` 池；`volumes.storage_pool_id` 与启动时回填。
- 数据盘相关的脚本（`attach_volume_local.sh`、`resize_volume_local.sh`、`clear_volume_local.sh`）和迁移脚本改为使用 clapi 下发的路径；迁移、删除用的卷 JSON 带上驱动和路径。
- 系统盘在创建回调里写 `volumes.hyper`，并如实记录路径 `instance/inst-N.disk`（脚本本身留到 L0b）。
- `report_rc.sh:257` 改用 `du -x`（本地池挂在根文件系统下之后，不加 `-x` 每次心跳都要遍历池目录；GPFS 设计附录 B 第 3 条也有这一项）。
- 卷的响应里增加 `storage_pool`。

**不包含**：心跳里 `du` / `df` 的单位修正和调度时磁盘需求的单位修正（附录 B 第 3、6 条）。前者修正后节点上报的可用磁盘会少掉根分区上非 CloudLand 文件的占用；后者修正后磁盘需求放大 1024 倍，cland 会真的开始按磁盘拒绝节点。两者都会改变调度结果，要先核对各节点的超分比例和剩余空间，单独评估后再做。clapi 对各池的准入用的是节点上报的原始 `df` 字节数（§4.7），不受这两个问题影响。

**验收**：`test-items/` 里卷与迁移相关的用例全部通过；数据库里每个卷的路径与节点上的实际文件一致；系统盘记录都有 `hyper`；挂载一个有数据的目录到 `/opt/cloudland/pools/` 下前后，心跳上报的磁盘数值不变、心跳耗时不增加。

### 阶段 L1：本地存储池与数据卷

**范围**：

- 部署脚本安装依赖、创建目录、修正 `chown -R`。
- 存储池增删改接口与管理页；`hyper_storage_pools`（含内置池记录）、`hyper_disks`、`storage_reservations`。
- 建池时的介质检查与手工指定介质（§2.6；RAID1 容量提示随 `raid1` 在 L3 做）。
- 节点：`scan_host_disks.sh`、`create_local_pool.sh`、`remove_local_pool.sh`、`adopt_local_pool.sh`；`pool_guard`（新建）；心跳上报（含内置池原始容量、按 fstab 发现池、自动尝试挂载）；待启动列表与 `virsh start` 回报修正（§4.8）。
- 数据卷：按池建卷；首次挂载的校验、准入与预留；扩容的预检、准入与预留；删卷改为 `deleting` + 回调；宣告丢失；删除节点前的检查与接管。
- 迁移：只支持留在原池；目标端 `pool_guard` 与预建挪到 `target_migration.sh`；按计划清理源端和回滚（§7.3 第 1、2、6、7、8 步）；目标准入与迁移预留；不指定目标时按 G1 收窄候选组。
- 前端：计算节点「存储」标签页（扫描、建池、删池、宣告丢失、接管、待启动列表）；建卷选存储池；挂载弹窗置灰；`deleting` / `lost` 状态。

**验收**：

- work-03 用 `sdb` 建 `local-hdd`（单盘）。`local-hdd` 卷挂到 work-03 上的虚拟机，文件出现在池目录，根分区上没有。
- 挂到 work-01 上的虚拟机被拒绝，提示 work-01 没有这个池。
- work-01 也建好 `local-hdd` 后，把虚拟机热迁移到 work-01，**再迁回 work-03**：两次都成功，数据完整，源端池里没有残留文件。
- 迁移中人为让目标端预建失败，回滚后目标池无残留；**再次发起迁移成功**。
- work-01 没有这个池时，迁移被拒绝并列出缺少的池。
- 卸载 work-03 的池（先让池里没有运行中的虚拟机）：一次上报周期内状态变为 `unavailable`（不必等 15 分钟）；挂载、扩容、删卷都被 clapi 直接拒绝；根分区上没有产生任何文件；重新挂载后恢复可用。
- 并发对同一个小池（测试开关下的 loop 设备，§12）发起多次首次挂载，总分配不超过准入上限。
- 建池时选中系统盘被拒绝；选中有签名的盘但没勾「清除」被拒绝；`cloudland_pool` 盘不能被清除。
- 介质检查：把一块测试盘手工指定为 `ssd`，用它建 `local-hdd` 被拒绝并列出这块盘；勾选确认后成功，审计里注明放行了介质不一致；直接调接口、不带 `allow_media_mismatch` 同样被拒绝；两块介质不同的测试盘组线性池同样要确认；重新扫描后手工值仍在，恢复自动识别后回到原值。
- 删卷：节点离线时 clapi 直接拒绝；正常删除后文件消失、记录删除、配额释放。
- 重启 work-03：池自动挂载；池里有盘的虚拟机正常启动；心跳没有中断。模拟池晚到（开机时池挂不上）：虚拟机进入待启动列表，挂上后下一次心跳自动启动。
- 模拟重新注册：删除节点（选择保留池）→ 重新注册 → 扫描显示可接管 → 接管后卷恢复可用，挂载正常。
- 模拟坏盘（测试开关下的 loop 设备，拔掉 backing file）：宣告丢失 → 卷可以删记录、可以强制卸下 → 强制删池成功。

### 阶段 L2：迁移时换池

**范围**：`FallbackGroup`；`POST /migrations` 的 `disks`、`allow_pool_fallback`；`GET /instances/:id/migration_targets`；G2 候选组与 `target_prepared` 时生成计划；域定义与数据盘描述文件的改写（§7.3 第 3、4 步）；维护模式批量迁移的预校验与逐台结果；迁移弹窗逐盘选池、迁移详情显示计划。

**开工前**：用一台临时虚拟机手工验证 `--xml` / `--persistent-xml` 改写磁盘路径的热迁移与冷迁移（包括 UEFI 虚拟机）。

**验收**：

- 数据盘从 work-03 的 `local-hdd` 热迁移到 work-01 的内置池，再迁回：数据完整；文件在新路径；源文件已删除；卷记录的池和路径已更新；迁移后卸载、再挂载正常。
- 同样的换池做一次关机迁移；一块留原池、一块换池的混合情况。
- 可互换组：work-01 只有与原池同组的 `local-hdd-b`，不指定目标池时落在 `local-hdd-b` 并标注自动选择；`allow_pool_fallback=false` 时失败。不同组的池不会被自动选中。
- 自动选目标时，有能保留原池的机器就不会选到需要换池的机器。
- 维护模式：一台虚拟机无处可迁时被跳过并返回原因，其余照常迁移。

### 阶段 L3：加盘、RAID1、告警

**范围**：`extend_local_pool.sh` 与接口、界面；`raid1` 布局（建池与按对加盘）；同步进度；`degraded` / `unavailable` 告警；可选的部署时自动建池。

**验收**：`linear` 池在线加盘，容量增加，池里运行中的虚拟机不受影响；两块盘（测试开关下的 loop 设备）建 `raid1`，同步期间可读写、显示进度；`mdadm --fail` 一块后显示降级并告警，虚拟机继续读写；换盘重建后恢复 `ready`。两块容量不同的测试盘建 `raid1`，界面和响应给出实际可用容量与浪费的容量，建成后池的容量与提示一致；往 `local-hdd` 加一块手工标成 `ssd` 的盘要确认。

### 阶段 L0b：存储池抽象（系统盘与其余部分）

在 L4 之前完成，**需要先决定 WDS 的去留（待办 C1）**。

**范围**（GPFS 设计 §14 阶段 0 中余下的本地部分）：系统盘元数据 `boot_disk`；`launch_vm.sh`、`reinstall_vm.sh`、`rescue_vm.sh`、`capture_image.sh`、`clear_vm.sh` 使用下发的路径；去掉 `$volume_dir/inst-N.disk` 回退；约 20 个脚本中按 `wds_address` 的分支（放弃 WDS 就直接删除）；GPFS 设计附录 B 第 1、2、5–7 条。

**验收**：`test-items/` 全部回归通过（创建、重装、救援要能访问原盘、捕获镜像、删除虚拟机等）；数据库里所有卷的路径与实际文件一致。

### 阶段 L4：系统盘与调度

**范围**：系统盘可以放在本地池；候选机器按池收窄（批量创建的每一台都适用）；`boot` 预留；`rcNeeded` 调整；NVRAM 放进池并在迁移中按计划复制和清理（§7.3 第 5 步）；管理员先选宿主机再选池；可选的 `required_storage_pools`；换池迁移扩展到系统盘。

**验收**：选 `local-ssd` 作系统盘池创建虚拟机，只会被调度到有这个池的机器上；管理员指定 work-01 后只列出 work-01 上的池；这台虚拟机的重装、救援、热迁移、关机迁移、删除都正常，NVRAM 在迁移后保持（启动项、Secure Boot 状态不变），删除后池里没有残留文件。

### 阶段 L5（可选）

同一台机器上换池（`virsh blockcopy --pivot` 或 `qemu-img convert`，复用 §7 的计划与改写）；本地盘与 GPFS 盘之间的转换；按池计配额；容量告警；孤儿文件对账。

---

## 12. 测试环境

- 三台机器都只有一块空闲机械盘，没有 SSD。RAID1、加盘、小容量准入、坏盘模拟、「混用」都需要更多的盘。
- **测试开关**：节点 `cloudrc.local` 里设 `storage_allow_loop=true`（仅测试环境）时，扫描会列出 loop 设备，标识为 `loop:<backing file>`，建池流程与真盘相同。生产环境不设这个开关。另一个办法是加载 `scsi_debug` 模块得到带 by-id 的模拟盘，更接近真盘，可以二选一。
- loop 设备的 backing file 放在 `/opt/cloudland/cache/tmp` 以外。
- 真正的介质差异（SSD 与 HDD 的性能）测不了。介质检查的逻辑可以用「手工指定介质」构造混用场景来测（§2.6）；RAID1 容量不一致用两个大小不同的 backing file 构造。

---

## 13. 待决策与待验证

1. **WDS 的去留（待办 C1）**：在 L0b 之前决定。放弃的话 L0b 直接删除 WDS 分支；保留的话要把 WDS 接入存储池抽象，工作量明显增加，而且没有环境验证。
2. **单盘池的风险接受度**：本地卷不支持备份，现有三台机器只能建单盘池。要么接受「坏盘即丢卷」（并在界面上说清），要么等加盘后再对用户开放本地池。
3. **默认池**：建议保持内置 `local`。
4. **`allow_pool_fallback` 的默认值**：建议 true，维护模式迁移更顺利。
5. **需要实测**：
   - `virsh migrate --xml` / `--persistent-xml` 改写磁盘路径（L2 开工前）；
   - Ubuntu 26.04 的 LVM devices file 是否会让重装系统后的旧卷组默认不可见（影响接管，L1）；
   - mdadm 降级阵列开机时的强制启动时间与 `x-systemd.device-timeout` 的配合（L3）。
6. **磁盘单位修正**（附录 B 第 3、6 条）的时机：会改变调度结果，需要单独评估。

---

## 附录 A：受影响的代码

### clapi（`api/src/`）

- `model/`：`storage_pool.go`（`StoragePool` 含 `Media`、`FallbackGroup`；`HyperStoragePool`；`HyperDisk` 含自动识别与生效介质；`StorageReservation`），`volume.go`（`StoragePoolID`，状态 `deleting` / `lost`），`migration.go`（`DiskPlan`）。
- 存储池的 service 层与接口（L1 新建）；`services/hyper_storage.go`：扫描（按盘标识更新、保留手工介质）、手工指定介质、建池与加盘前的介质与容量检查（§2.6）、建池、加盘、删池、宣告丢失、接管；准入与预留（事务 + 行锁），过期预留清理。
- `services/volume.go`：首次挂载（约第 440–455 行）、扩容、删除（第 520–539 行，改为 `deleting` + 回调）。
- `services/migration.go`：计划生成、候选组 G1 / G2（第 145–169 行）、目标准入与预留、`disks` 与 `allow_pool_fallback` 参数、批量迁移预校验（第 35–40、106–110、190 行）。
- `services/hyper.go`：`Maintain` 返回逐台结果（第 534–541 行）；`Delete` 检查节点存储池和卷（第 569–576 行）。
- `common/instance.go:32` `GetHyperGroup`：按池收窄候选（L4）；`services/instance.go:278` 批量创建（L4）。
- `rpcs/`：新增 `host_disks`、`local_pool_status`、`clear_volume`；`launch_vm.go` / `create_volume.go` 给系统盘写 `hyper`（L0a）；`attach_volume.go`、`resize_volume.go` 删除预留；`migrate_vm.go`：`target_prepared` 时生成计划、`completed` 时更新池与路径（第 306–311 行）、回滚时删除预留。
- `apis/`：`storage_pool.go`；`hyper.go` 的磁盘与存储池子路由；`volume.go` 删除返回 202；`migration.go`；`audit_actions.go`。

### 节点脚本（`scripts/`）

- 新增：`kvm/scan_host_disks.sh`、`kvm/create_local_pool.sh`、`kvm/extend_local_pool.sh`（L3）、`kvm/remove_local_pool.sh`、`kvm/adopt_local_pool.sh`。
- `cloudrc`：`pool_guard`（新建）；上报节流；池命令的超时封装。
- `kvm/report_rc.sh`：`du -x`（第 257 行，L0a）；本地池与内置池的上报；待启动列表与 `virsh start` 结果判断（第 214–222 行）。
- `kvm/attach_volume_local.sh:10`、`kvm/resize_volume_local.sh`、`kvm/clear_volume_local.sh`（加回调）：使用下发的路径、共享锁、`pool_guard`。
- `kvm/target_migration.sh`：目标端 `pool_guard` 与预建。
- `kvm/source_migration.sh`：读计划（第 49 行）；不再 `mkdir -p` 与预建（第 58–67 行）；改写并复制数据盘描述文件（第 79 行）；NVRAM（第 81 行）；`--xml` / `--persistent-xml`（第 118、123 行）。
- `kvm/finish_source_migration.sh`（第 31–33 行）、`kvm/async_job/clear_target_migration.sh`（第 39、48、51 行）：按计划清理。

### 其他

- `cpgateway/src/apis/proxy_routes.go`：新增路由。
- `deploy/docker/scripts/deploy-compute-node.sh`：依赖、`/opt/cloudland/pools`、第 346 行 `chown -R`。
- `web/`：计算节点「存储」标签页、存储池管理页、建卷、挂载、云硬盘状态、迁移、维护模式、创建虚拟机（L4），三种语言。

---

## 附录 B：审查中发现的现有缺陷（与本方案无关，可单独修）

1. **创建虚拟机时指定宿主机没有权限检查**：`apis/instance.go:580-589` 按 UUID 查节点、`services/instance.go:132` 只校验可用区，不校验系统管理员身份。界面只对管理员显示这个选项，但普通成员直接调接口也能把虚拟机固定到某台节点。**已修复**：接口层在查节点之前检查（避免借报错探测节点 UUID），服务层再检查一次，非系统管理员返回 403。
2. **开机拉起虚拟机时不看结果**：`report_rc.sh:219-220` 不管 `virsh start` 成败都回报 `running`（L1 随待启动列表一并修）。
3. **心跳磁盘统计单位混用**：`report_rc.sh:257-258` 把 `du -s`（KiB）和 `df -B 1`（字节）直接相减，根分区上非 CloudLand 文件的占用只扣了约千分之一。修正会改变调度结果，需评估。
4. **批量创建只有第一台按指定节点下发**：`services/instance.go:278` 的 `i == 0 && hyperID >= 0`，其余走调度。
5. **删除本地卷时节点离线，命令被丢弃、文件残留**：`common/clients.go:132-139` 对「节点离线」只告警并返回成功（L1 随删卷回调一并修）。
6. **调度时磁盘需求的单位不对**：`services/instance.go:276`、`services/migration.go:163` 按 `GB*1024*1024` 传给 cland，而节点上报的是字节，相差 1024 倍，磁盘检查几乎总是通过（即 GPFS 设计附录 B 第 4 条）。修正会改变调度结果，需评估。
