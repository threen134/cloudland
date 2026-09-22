# 本地多盘存储池设计（按宿主机配置磁盘，支持 SSD / HDD 混用）

- **状态**：设计草案，未实施
- **日期**：2026-09-22，基于 `stage-01` 分支 `53dceb82`（文中行号均以此为准）
- **涉及**：clapi（`api/`）、节点脚本（`scripts/`）、cpgateway 代理白名单、前端（`web/`）、部署脚本
- **相关文档**：`gpfs-shared-storage-design.md`（下称「GPFS 设计」）。本文沿用它定义的存储池模型、「clapi 是路径的唯一来源」和挂载保护，不另起一套模型
- **执行顺序**：**本文先实施，GPFS 设计后实施**。GPFS 设计阶段 0（存储池抽象）里本地存储需要的部分，由本文的阶段 L0 完成；GPFS 专用的部分留给 GPFS 设计。以后执行 GPFS 设计时，在本文的结果上增加 `gpfs` 驱动即可，不需要推倒重来

---

## 0. 摘要

- **两层结构**：「存储池」是区域级的、用户可以选择的资源（例如 `local-ssd`、`local-hdd`、GPFS 的 `gpfs-ssd`）；「节点本地存储」是某台宿主机上属于某个本地存储池的一个目录，由管理员按机器配置。每台机器可以有 0 个或多个本地存储池，盘的类型、数量、冗余方式各不相同。
- **同一台机器上，一个存储池只对应一个目录**。同类的多块盘用 LVM（可选 RAID1）合成一个目录；需要隔离或分等级时，建成不同的存储池。
- **目录路径在所有机器上相同**：`/opt/cloudland/pools/<池UUID>`。迁移时盘留在同一个池，路径就不变，可以沿用现在「目标机器同路径预建磁盘」的做法。
- **迁移时可以换池**：管理员可以逐盘指定目标机器上的另一个本地池（包括内置池）。目标机器没有原池时，自动替换成一个**介质相同**的池（可关闭）；没有同介质的池才要求管理员指定。**永远不跨介质自动替换**。换池时改写域定义、数据盘描述文件和 NVRAM 的路径。
- **管理员在计算节点详情页配置磁盘**：发现空闲盘、建池、给池加盘、删池。建池会格式化磁盘，有严格的检查和二次确认。
- **创建虚拟机时，管理员可以先选宿主机，再从这台机器上的池里选系统盘的位置**；普通用户只选池，由调度器挑机器。
- **容量准入按「这台机器上的这个池」计算**：首次挂载（落盘）、扩容、创建系统盘、迁移到目标池时检查；心跳上报每个池的容量和健康状态（含 RAID 降级）。
- **分六个阶段实施**：L0 存储池抽象（取自 GPFS 设计阶段 0 中与本地存储有关的部分）→ L1 本地池与数据卷 → L2 迁移时换池 → L3 加盘、RAID1、告警 → L4 系统盘与调度 → L5（可选）。GPFS 设计在本文之后实施，以 L0 的结果为基础。

---

## 1. 背景与目标

### 1.1 现状

- **本地卷只有一个目录**：`scripts/cloudrc:11` 写死了 `volume_dir=/opt/cloudland/cache/volume`，13 个节点脚本直接用它拼卷的路径；系统盘在 `$image_dir`（`/opt/cloudland/cache/instance`）。这两个目录都在根分区上。
- **节点上的盘没用上**：work-01、work-02、work-03 各有两块 2 TB 机械盘（Seagate ST2000NM0055）。操作系统、系统盘、数据卷全在 `sda4` 上；第二块盘 `sdb` 闲置（work-01 / work-03 挂在 `/disk1` 但为空，work-02 没有挂载）。
- **数据卷不计入容量**：心跳只统计 `$image_dir` 所在文件系统（`report_rc.sh:18`、`:247`、`:257`）。挂载和扩容本地卷时不检查剩余空间，而卷文件是 qcow2 精简置备，分配总量可以超过磁盘容量，写满时虚拟机出现 I/O 错误。
- **将来会有混用**：不同机器的盘类型（SSD / HDD）、数量可能都不一样。
- 上游的「多存储池」（PET-1283）只针对 WDS，把 WDS 的故障域按使用率轮选，**不涉及本地盘**，不能直接借用。

### 1.2 目标

1. 管理员能按机器配置本地磁盘：哪些盘、组成哪些存储池、用什么冗余方式。
2. 用户按存储池（等级）建卷，不需要知道机器和目录。
3. 本地卷只落在「虚拟机所在机器上、属于该存储池」的目录里；机器上没有这个池时明确拒绝，而不是悄悄落到根分区。
4. 按「机器 × 存储池」做容量准入，心跳上报容量和健康状态。
5. 迁移时每块盘可以留在同一个池，也可以由管理员换到目标机器上的另一个池；目标机器没有原池时，在同介质的池之间自动替换；自动校验目标机器是否具备所需的池和容量。
6. 管理员创建虚拟机时，可以指定宿主机和这台机器上的具体存储池。
7. 加盘在线完成，不停虚拟机。

### 1.3 非目标

- 共享存储（GPFS 设计负责）。
- 同一台机器上同一个存储池拆成多个目录（按盘隔离）。需要隔离时建成不同的存储池（§2.2）。
- 不换机器、只换池（同一台机器上的池间搬迁），以及本地盘与 GPFS 盘之间的转换。放在可选的阶段 L5，复用迁移换池的路径改写逻辑。跨机器迁移时换池属于本文范围（§7）。
- 按存储池分别计配额。cpgateway 仍然只有一个 `disk_gb`（与 GPFS 设计一致）。
- 自动冷热分层。

---

## 2. 概念与模型

### 2.1 两层结构

| 层 | 范围 | 谁来维护 | 例子 |
|---|---|---|---|
| **存储池**（`storage_pools`） | 区域级，用户可见、可选择 | 系统管理员定义 | `local`（内置）、`local-ssd`、`local-hdd`、`gpfs-ssd` |
| **节点本地存储**（`hyper_storage_pools` 的本地池记录） | 某台宿主机上的一个目录，属于一个本地存储池 | 系统管理员在该机器上配置 | work-02 上的 `local-ssd`：`nvme0n1`，单盘，1.8 TB |

示例：

```
存储池（区域级）    local（内置）          local-ssd                 local-hdd
work-01            /opt/cloudland/cache   —                         sdb+sdc，RAID1，1.8 TB
work-02            /opt/cloudland/cache   nvme0n1，单盘，1.8 TB      sdb，单盘，1.8 TB
work-03            /opt/cloudland/cache   nvme0n1+nvme1n1，线性     sdb，单盘，1.8 TB
```

- **内置的 `local` 池**（GPFS 设计 §4.1）保持不变：每台机器都有，根目录是 `/opt/cloudland/cache`，也就是现在的位置。已有的卷全部属于它，不需要迁移。
- **用户只选存储池**。一块 `local-ssd` 卷首次挂到 work-02 上的虚拟机时，落在 work-02 的 `local-ssd` 目录；挂到 work-01 上的虚拟机时，因为 work-01 没有 `local-ssd`，直接拒绝并说明原因。
- 本地卷落盘后就绑定在那台机器上（`volumes.hyper`），这和现在的规则一致。

### 2.2 为什么同一台机器上一个池只对应一个目录

- **同类的多块盘在存储层合并**（LVM，可选 RAID1），对 CloudLand 只呈现一个目录。放置逻辑不需要在同一台机器的多个目录之间选择，加盘也只是存储层的在线扩容（§4.3）。
- **需要分开的就建成不同的池**：例如 SSD 与 HDD 分成 `local-ssd` 和 `local-hdd`；如果确实要按盘隔离，可以建 `local-hdd-a`、`local-hdd-b`，由用户（或以后的放置策略）选择。
- 代价：同一台机器上同一个池内的盘共享故障域。线性拼接时坏一块盘，这个池里的全部卷都受影响，所以建池时要明确选择冗余方式（§4.2），界面上要把「无冗余」标注清楚。

### 2.3 路径约定

- 本地存储池（内置池除外）的根目录固定为 `/opt/cloudland/pools/<池UUID>`，**在每台有这个池的机器上都相同**。
  - 用 UUID 而不是名称：存储池可以改名，路径不能变。
  - 路径相同带来的好处：迁移时盘留在同一个池，目标机器就可以沿用现在「同路径预建磁盘」的逻辑（`source_migration.sh:64`、`:67`），域定义里的磁盘路径不用改写。换池时路径不同，改写方法见 §7.3。
- 池根目录是一个**独立挂载点**，结构与 GPFS 设计 §2.5 一致：

```
/opt/cloudland/pools/<pool-uuid>/     <- mount point of the host's LV
├── .cloudland-pool                   <- marker file: pool UUID + hostid (guard, §4.5)
├── volumes/volume-<volID>.disk       <- data disks and (phase L4) boot disks, qcow2
├── nvram/inst-<instID>_VARS.fd       <- UEFI NVRAM of instances whose boot disk is in this pool (L4)
└── tmp/
```

- `volumes.path` 存池内相对路径 `volumes/volume-<id>.disk`，绝对路径由 clapi 拼好下发（GPFS 设计 §3.3）。
- 在 `storage_pools` 里，本地池的 `MountPath` 就是这个统一的根目录，与 GPFS 池「每个节点挂载路径相同」的语义一致；区别在于内容不共享（§3.1 的 `Shared=false`）。

### 2.4 与 GPFS 设计的关系

| 方面 | GPFS 池 | 本地池（本文） |
|---|---|---|
| 存储池记录 | `storage_pools`，`Driver=gpfs` | `storage_pools`，`Driver=local` |
| 节点记录 | `hyper_storage_pools`：该节点能否访问共享池 | `hyper_storage_pools`：该节点上**这个池的目录**，带盘、布局、容量、健康状态（§3.2） |
| 卷与节点 | 不绑定，`hyper=0` | 落盘后绑定 `hyper` |
| 容量 | 池级（fileset 配额） | **机器 × 池**级 |
| 挂载保护 | fs 类型 + 标记文件 | 独立挂载点 + fs 类型 + 标记文件（含 hostid） |
| 迁移 | 全共享时不复制磁盘；始终留在原池 | 复制磁盘；默认留在同一个池，管理员可以逐盘换到目标机器上的另一个本地池（§7） |

GPFS 设计里的「按卷的驱动」「clapi 下发绝对路径」「按单块盘处理迁移清单」对本地池同样适用，本文不重复，只写本地池特有的部分。

### 2.5 配置流程总览

路径与盘分两步设置，在两个不同的地方：

**第 1 步：建存储池，路径在这时自动确定。**

- **在哪里**：管理员的「存储池」管理页（§8.3），新建一个本地存储池，例如 `local-ssd`，填名称、介质、超分比例。
- **什么时候**：新建的那一刻，clapi 把路径定为 `/opt/cloudland/pools/<池UUID>`（§2.3、§3.1）。管理员不能填写或修改；每台机器上这个池的路径都一样。
- 这一步只在数据库里登记，**不会碰任何机器、任何盘**。

**第 2 步：在某台机器上添加这个池，盘与池的对应在这时设置。**

- **在哪里**：计算节点详情页的「存储」标签页（§8.3）。
- **什么时候**：机器注册上线之后的任何时候，管理员逐台操作：
  1. 点「扫描」，列出这台机器的盘，以及每块盘是空闲、有旧数据、在用还是系统盘（§4.1）。
  2. 点「添加存储池」：选第 1 步建好的池（也可以在弹窗里就地新建），勾选这台机器上要用的盘，选布局（单盘 / 线性 / RAID1），输入机器名确认。

对应接口 `POST /hypers/:uuid/storage_pools`（§8.1），执行过程：

```
clapi: insert a hyper_storage_pools row (host, pool, layout, disk by-ids, status=creating)
  | dispatch to that host (inter=<hostid>)
  v
node create_local_pool.sh (§4.2):
  re-check disks -> mdadm RAID1 (if chosen) -> LVM vg cl_<first 8 of pool uuid> -> mkfs.xfs
  -> mount at /opt/cloudland/pools/<pool uuid>, add to fstab by file system UUID
  -> create volumes/ nvram/ tmp/, write marker .cloudland-pool (pool uuid + hostid)
  | callback local_pool_status
  v
clapi: status=ready, record capacity
```

**之后的维护**，都在同一个「存储」标签页上：给这台机器上的池加盘（§4.3）、删池（§4.4，池里没有卷时才允许）。

**对应关系存在哪里**：

| 位置 | 记录的内容 |
|---|---|
| clapi 数据库 `hyper_storage_pools`（§3.2） | 哪台机器、哪个池、用了哪几块盘（by-id）、布局、卷组名、容量、状态。界面上显示的就是这里 |
| 节点的 LVM / mdadm | 盘 → 阵列 → 卷组 → 逻辑卷，这是实际生效的组合 |
| 节点的 `/etc/fstab` | 逻辑卷的文件系统 UUID → 池的路径，开机自动挂载。按 UUID 而不是 `sdb` 这类名字，加盘、换盘后名字变了也不受影响 |
| 池根目录下的 `.cloudland-pool` | 池 UUID 和机器 ID，用来确认挂载上来的确实是本机的这个池（§4.5） |

**节点上没有专门的 CloudLand 配置文件**，不需要手工编辑 `cloudrc.local`。心跳时，节点扫描 `/opt/cloudland/pools/*/.cloudland-pool` 就知道自己有哪些池（§4.6）。

**配置完成之后的日常使用**：用户建卷时只选存储池（§5.1）；卷首次挂到某台机器上的虚拟机时，才落在那台机器上这个池的目录里（§5.2）。管理员创建虚拟机时，可以先选机器、再从这台机器上的池里选（§5.6）。

---

## 3. 数据模型

表结构由启动时的 AutoMigrate 创建，不写迁移代码。

**因为本文先于 GPFS 设计实施，`storage_pools` 和 `hyper_storage_pools` 两张表由本文创建**：L0 建 `storage_pools`（只有内置 `local` 池），L1 建 `hyper_storage_pools`。字段定义沿用 GPFS 设计 §4.1、§4.2，但只对 GPFS 有意义的字段（`FsName`、`Fileset`、`FsType`、`CloneMode`）先不加，等执行 GPFS 设计时再加，AutoMigrate 会自动补列。下面列出的是本文在这个基础上增加的字段。

### 3.1 `storage_pools`（结构见 GPFS 设计 §4.1）增加的字段

| 字段 | 说明 |
|---|---|
| `Media string` | `ssd` / `hdd` / `nvme` / 空。用于展示和选择；迁移时的同介质自动替换也依据它（§7.1）。为空时不参与自动替换 |
| `Shared`（不是数据库列） | 表里不存这一列，由 `Driver` 现算：`Driver=gpfs` 为 true，`local` 为 false（模型方法 `Shared()`，接口响应里输出为 `shared` 字段）。前端据此显示「共享 / 本地」。不单独存，是为了避免它和 `Driver` 互相矛盾；以后接入 Ceph 等共享驱动时只改这个方法 |

- 本地池（非内置）的 `MountPath` 在创建时自动设为 `/opt/cloudland/pools/<UUID>`，不能修改。`FsName`、`Fileset`、`CloneMode` 对本地池无意义。
- 池级的 `CapacityBytes / UsedBytes` 对本地池是所有机器的**合计**，只用于展示；准入按机器计算（§6）。
- **默认池**：建议保持内置的 `local`。如果把一个不是每台机器都有的本地池设为默认，没有这个池的机器上的虚拟机挂载新卷时会失败，界面要给出警告。

### 3.2 `hyper_storage_pools`（结构见 GPFS 设计 §4.2）扩展

GPFS 设计里这张表记录「节点能否访问共享池」。本文先把它建出来给本地池用，每条记录表示「这台机器上这个池的目录」，唯一索引 `(hostid, pool_id)` 保证一台机器上一个池只有一个目录（§2.2）。以后接入 GPFS 时，共享池的记录也放在这张表里，只是不用下面的本地字段。

```go
type HyperStoragePool struct {
	Model
	Hostid    int32     `gorm:"uniqueIndex:idx_hyper_pool"`
	PoolID    int64     `gorm:"uniqueIndex:idx_hyper_pool"`
	Status    string    `gorm:"type:varchar(32)"` // ready | degraded | unavailable | creating | removing | error
	Reason    string    `gorm:"type:varchar(256)"`
	CheckedAt time.Time
	// local pools only: the directory this host provides for the pool
	Layout        string `gorm:"type:varchar(16)"`  // single | linear | raid1
	Devices       string `gorm:"type:text"`         // JSON: [{by_id, serial, model, size_bytes, media}]
	VgName        string `gorm:"type:varchar(64)"`  // cl_<first 8 of pool uuid>
	CapacityBytes int64  // file system size
	UsedBytes     int64  // file system used
	CapacityAt    *time.Time
}
```

- `Status`：
  - `creating` / `removing`：建池、删池的命令已下发，等待回调。
  - `ready`：挂载保护通过，存储层健康。
  - `degraded`：RAID1 缺盘但仍可用。**照常接受新卷**，但要告警（§4.6）。
  - `unavailable`：没挂载、标记文件不对、LV 或阵列不可用，或者超过 15 分钟没有上报。**不接受新卷**。
  - `error`：建池失败，`Reason` 写明原因，管理员可以重试或删除这条记录。
- 已分配容量不入库，准入时按卷实时计算（§6）。

### 3.3 `hyper_disks`（新增）

节点磁盘发现的结果，供建池时选择：

```go
type HyperDisk struct {
	Model
	Hostid    int32  `gorm:"index"`
	ByID      string `gorm:"type:varchar(256)"` // /dev/disk/by-id/..., stable across reboots
	Name      string `gorm:"type:varchar(32)"`  // sdb, nvme0n1: display only, may change after reboot
	Serial    string `gorm:"type:varchar(128)"`
	Model     string `gorm:"type:varchar(128)"`
	SizeBytes int64
	Media     string `gorm:"type:varchar(16)"`  // ssd | hdd | nvme, from rotational / transport
	State     string `gorm:"type:varchar(32)"`  // free | dirty | in_use | system
	Detail    string `gorm:"type:varchar(256)"` // why not free: mounted at /, has ext4 signature, member of md0 ...
	PoolID    int64  // set when the disk belongs to one of our pools
	ScannedAt time.Time
}
```

- 每次扫描整体替换这台机器的记录。
- 盘的标识一律用 `/dev/disk/by-id/...`。`sdb` 这类名字在重启、加盘后可能变化，只用于显示。

### 3.4 `volumes`

沿用 GPFS 设计 §4.3（`storage_pool_id`、相对路径、`hyper` 只对本地池有意义），本文不再增加字段。

### 3.5 `migrations` 增加 `DiskPlan`

迁移会跨越好几个回调（目标准备、源端准备、完成、回滚），每一步都要用同一份「每块盘从哪里到哪里」的计划，所以在创建迁移时就把它存下来（§7.1）：

```go
// DiskPlan is decided when the migration is created and used unchanged by every later step (JSON):
// [{volume_id, device, booting, src_pool_id, src_path, dst_pool_id, dst_path, auto, reason}]
// auto: the target pool was picked by the same-media fallback rule (§7.1), reason says why
DiskPlan string `gorm:"type:text"`
```

- 只包含本地盘，GPFS 盘不在其中。
- `src_pool_id == dst_pool_id` 时 `src_path == dst_path`（§7.2）。
- 迁移详情接口返回它，界面上显示每块盘从哪个池迁到哪个池。

---

## 4. 节点侧

新增脚本都放在 `scripts/kvm/`，按惯例以 `|:-COMMAND-:|` 回调，多行结果用 base64 编码成一个参数。建池、加盘、删池这三个脚本用 `flock /var/lock/cloudland-storage.lock` 互斥，同一台机器上同时只能有一个存储变更。

### 4.1 发现磁盘：`scan_host_disks.sh`

- 由管理员在界面上点「扫描」触发（`inter=<hostid>`），不放进心跳。
- `lsblk -J -b -o NAME,PATH,TYPE,SIZE,ROTA,TRAN,MODEL,SERIAL,MOUNTPOINTS,FSTYPE,PKNAME`，再用 `/dev/disk/by-id` 找到每块盘的稳定名字。
- 分类：
  - `system`：承载 `/`、`/boot`、swap、`/opt/cloudland/cache` 的盘（顺着分区、md、LVM 往上追）。**永远不能选**。
  - `in_use`：已挂载、属于其他 md 阵列或 LVM 卷组，或者是本系统的存储池成员（这时带上 `PoolID`）。
  - `dirty`：没在用，但有分区表或文件系统签名（`wipefs -n` 能读到）。例如现在 work-01 的 `sdb1`（ext4，只有 `lost+found`）。界面提供「清除」操作（§4.2 第 1 步），同样需要二次确认。
  - `free`：整盘、没有任何签名。
- 只列 `TYPE=disk`，排除 loop、rom、可移动设备。
- 介质判断：`TRAN=nvme` 为 `nvme`；`ROTA=0` 为 `ssd`；其他为 `hdd`。管理员可以在建池时改成其他值。
- 回调 `host_disks '<NODE_ID>' '<base64 JSON>'`，clapi 整体替换 `hyper_disks`。

### 4.2 建池：`create_local_pool.sh`

参数：`<池UUID> <根目录> <布局> <by-id 设备...>`，`<根目录>` 必须等于 `/opt/cloudland/pools/<池UUID>`。

1. **再检查一遍每块盘**（不相信 clapi 的扫描结果，盘的状态可能已经变了）：整盘、不是 `system`、没挂载、没有 holder。有签名时，只有带 `--wipe` 参数（界面上选了「清除」）才执行 `wipefs -a`，否则报错退出。
2. **按布局组装**：
   - `single`：一块盘，直接作为 PV。
   - `linear`：多块盘，都作为 PV，拼接成一个 LV。**没有冗余**。
   - `raid1`：盘数必须是偶数，每两块用 `mdadm --create --level=1` 组成一个阵列，阵列作为 PV。写入 `/etc/mdadm/mdadm.conf` 并执行 `update-initramfs -u`，保证重启后阵列名稳定。
3. `vgcreate cl_<UUID 前 8 位>`，`lvcreate -l 100%FREE -n data`。
4. `mkfs.xfs`。选 xfs 是因为它对大文件、qcow2 的表现好，没有 ext4 单文件 16 TiB 的限制，并且支持在线扩容。
5. 写 fstab：`UUID=<fs uuid> <根目录> xfs defaults,nofail,x-systemd.device-timeout=30s 0 2`，然后 `mount`。`nofail` 保证坏盘时机器仍能开机，这时池为 `unavailable`（§4.5）。
6. 建 `volumes/`、`nvram/`、`tmp/`，写入 `.cloudland-pool`（内容为池 UUID 和 hostid），把属主设为 `cland`。
7. 回调 `local_pool_status '<NODE_ID>' '<池UUID>' 'ready|error' '<原因>' '<容量>' '<已用>'`。
8. 任何一步失败都**回滚已经做的步骤**（卸载、删 fstab 行、`vgremove`、`mdadm --stop` 和 `--zero-superblock`），再回调 `error`。已执行 `wipefs -a` 的盘无法恢复原签名，这一点要在确认框里写清楚。

### 4.3 加盘：`extend_local_pool.sh`

- `single` → 加一块盘后变成 `linear`（无冗余），或者再加一块做成 `raid1`：原盘和新盘组成新阵列需要搬数据，**第一版不支持**，界面上不提供这个选项。
- `linear`：`pvcreate` → `vgextend` → `lvextend -l +100%FREE -r`，在线完成。
- `raid1`：每次加两块，组成新阵列 → `pvcreate` → `vgextend` → `lvextend -r`，在线完成。
- 回调同 §4.2，带上新容量。

### 4.4 删池：`remove_local_pool.sh`

- clapi 先校验：这台机器上这个池里**没有任何卷**（`storage_pool_id=池 且 hyper=这台机器`，包括未删除的系统盘）。
- 节点：`umount` → 删 fstab 行 → `vgremove` → `mdadm --stop` 与 `--zero-superblock` → `wipefs -a` → 删 mdadm.conf 里对应的行。
- 脚本自己也要检查 `volumes/` 目录为空，不为空就拒绝，作为 clapi 校验之外的第二道保护。

### 4.5 挂载保护

扩展 GPFS 设计 §5.1 的 `pool_guard`。本地池的根目录是独立挂载点，所以多一项检查：

1. `mountpoint -q <根目录>`；
2. `stat -f -c %T <根目录>` 为 `xfs`；
3. `.cloudland-pool` 里的池 UUID 和 hostid 都匹配（防止把别的机器的盘误插过来后被当成本机的池）。

凡是写本地池的脚本（挂载落盘、扩容、迁移预建目标磁盘、系统盘创建），在第一次写入前都必须调用它。**不对池根目录做 `mkdir -p`**。

### 4.6 状态与容量上报

- 放进心跳 `report_rc.sh`，但**限频**：状态变化时立即上报，否则每 5 分钟上报一次（用时间戳文件节流，心跳本身是 1–20 秒一次）。
- 每个本地池：`pool_guard` 的结果、`df -B1 --output=size,used`、存储层健康：
  - `mdadm --detail --test` 退出码为 1 时是 `degraded`，为 2 时是 `unavailable`。
  - 还要检查 LV 是否处于激活状态。
- 回调 `local_pool_status`（与 §4.2 相同）。clapi 超过 15 分钟没收到就按 `unavailable` 处理（与 GPFS 设计 §4.2 一致）。
- 节点不需要预先知道自己有哪些池，直接扫描 `/opt/cloudland/pools/*/.cloudland-pool` 即可。
- `degraded` 和 `unavailable` 接入告警（节点告警规则里增加「本地存储池异常」类型），属于阶段 L3。

### 4.7 开机

- fstab 带 `nofail`，盘坏了机器照样能启动。
- `report_rc.sh` 里的 `sync_instance` 在 `virsh start` 之前，对这台虚拟机每一块指向 `/opt/cloudland/pools/` 的磁盘执行 `pool_guard`，最多等 5 分钟（与 GPFS 设计 §6.9 是同一套逻辑）；超时就不启动这台虚拟机，并上报原因。

---

## 5. 各操作流程

以下「本地池」都指非内置的本地存储池；内置 `local` 池的行为就是阶段 L0 完成后的行为（与现在一致，只是路径由 clapi 下发）。

### 5.1 创建数据卷

- 请求带 `storage_pool: {id}`；不传就用默认池。
- 与现在一样**不落盘**：建记录，`path=volumes/volume-<id>.disk`，状态 `available`。
- 区域内没有任何一台机器的这个池处于 `ready` 时，照样允许创建，但响应里带一条提示，界面上显示「当前没有可用的节点」。

### 5.2 首次挂载（落盘）

- 机器 = 虚拟机所在的机器（与现在相同）。
- clapi 校验：
  1. 这台机器上这个池的记录存在，且状态是 `ready` 或 `degraded`。否则返回 400，例如「work-01 没有存储池 local-ssd」或「work-02 上的存储池 local-ssd 当前不可用：未挂载」。
  2. 容量准入（§6）。
- 下发：`attach_volume_local.sh '<实例ID>' '<卷ID>' '<绝对路径>' '<卷UUID>' '<GB>' '<池UUID>'`：
  - `vol_path` 直接用传入的绝对路径（现在是 `$volume_dir/$3`，`attach_volume_local.sh:10`）。
  - 在 `qemu-img create` 之前执行 `pool_guard`（内置池跳过）。
- 回调照旧写 `hyper`（`rpcs/attach_volume.go:80-81`）。

### 5.3 已落盘卷的挂载、卸载

- 已落盘的卷只能挂给同一台机器上的虚拟机，这是现在的规则（`services/volume.go:447`），不变。
- 卸载不依赖路径，不变。

### 5.4 扩容

- 在卷所在的机器上做容量准入（增量部分，§6）。
- 路径由 clapi 下发。`resize_volume_local.sh` 在 `qemu-img` / `blockresize` 之前对本地池执行 `pool_guard`。
- 失败时按实际容量回滚，这是现有逻辑（`rpcs/resize_volume.go`）。

### 5.5 删除卷

- `inter=<volume.Hyper>`，路径由 clapi 下发，删除前执行 `pool_guard`；还没落盘的只删记录（与现在相同）。
- 机器上的池处于 `unavailable` 时，删除请求照样下发，节点上的 `pool_guard` 会失败并回调 `error`，卷保持原状。这样不会出现「数据库删了、文件还在盘上」的情况。

### 5.6 系统盘放在本地存储池（阶段 L4）

- `POST /instances` 的 `storage_pool` 可以指定本地池（GPFS 设计 §10.1 已经定义了这个参数）。
- **调度过滤**：`GetHyperGroup`（`common/instance.go:32`）的候选节点，取它与「该池在这台机器上 `ready` 且容量够放下系统盘」的交集；管理员指定了节点时，该节点必须在交集里。
- **管理员先选宿主机、再选池**：现在管理员已经可以在创建虚拟机时指定宿主机。选了宿主机之后，「系统盘存储池」下拉框只列出这台机器上可用的池（它的本地池、内置池，以及它能访问的 GPFS 池），每一项显示这台机器上的剩余容量，容量不够的置灰。因为同一台机器上一个池只有一个目录（§2.2），选定「机器 + 池」就唯一确定了系统盘的位置。后端对这个组合做同样的校验和容量准入。
- 没有指定宿主机（普通用户只能这样）时只选池，由调度器在具备这个池的机器中挑选。
- `rcNeeded` 里的 `disk`：系统盘不在 `$image_dir` 时填 0，由 clapi 按池做准入。否则 cland 会按根分区的剩余空间去判断，结果不对。
- 元数据里的 `boot_disk`（GPFS 设计 §6.5）：`driver=local`，`pool_root=/opt/cloudland/pools/<UUID>`，`path=.../volumes/volume-<id>.disk`，`nvram=.../nvram/inst-<id>_VARS.fd`。`launch_vm.sh` 先执行 `pool_guard`，再把镜像从本地缓存转换到这个路径。
- 重装、救援、捕获镜像都改用下发的系统盘路径，这部分在 GPFS 设计 §6.7 已经覆盖。

### 5.7 让虚拟机落在有所需存储池的机器上（阶段 L4，可选）

问题：数据卷首次挂载时才落盘，要是虚拟机所在的机器没有这个池，只能在挂载时失败。

- `POST /instances` 增加可选参数 `required_storage_pools: [{id}, ...]`，调度时只考虑具备这些池（`ready` 或 `degraded`）的机器。
- 界面：创建虚拟机时，如果系统盘选了某个本地池，数据盘默认也按这个池过滤；另外可以勾选「还需要 local-ssd」。
- 对已经存在的虚拟机，挂载弹窗把「所在机器没有这个池」的虚拟机置灰并说明原因（§8.3）。

---

## 6. 容量准入

对「机器 H 上的本地池 P」：

- **已分配** = 所有 `storage_pool_id=P`、`hyper=H`、未删除的卷的 `size` 之和，加上本次新增量（首次挂载加卷大小，扩容加增量，系统盘加规格大小）。要求 ≤ `CapacityBytes × OverRatio`。
- **实际用量**：`UsedBytes / CapacityBytes` < 90%。
- 容量未知（还没收到上报）时拒绝，因为本地池不存在「容量未知但能用」的情况：记录存在就一定上报过。这一点和 GPFS 设计（未知时只告警）不同。
- 扩容、首次挂载、创建系统盘在同一台机器、同一个池上可能并发：准入和写记录放在同一个事务里，并对 `hyper_storage_pools` 的这一行加 `SELECT ... FOR UPDATE`，避免两个请求同时通过。
- **内置 `local` 池**也补上同样的准入：容量取心跳里 `$image_dir` 所在文件系统（`report_rc.sh:18`），已分配按「该机器上属于内置池的卷」计算。这顺带修掉 §1.1 里「数据卷不计入容量」的问题。

---

## 7. 迁移

在 GPFS 设计 §7 的迁移矩阵基础上，虚拟机的每块本地盘（本地池和内置池里的盘）迁移时**可以留在同一个池，也可以由管理员逐盘换到目标机器上的另一个本地池**。迁移接口本来就只允许系统管理员调用（`services/migration.go:29`）。

### 7.1 迁移计划：每块盘的目标池

创建迁移时，clapi 为虚拟机的每块本地盘（系统盘、数据盘）确定目标池，形成「迁移计划」：

| 情况 | 目标池 |
|---|---|
| 请求里为这块盘指定了 `storage_pool` | 用指定的池。必须是本地池（包括内置 `local`），并且在目标机器上是 `ready` 或 `degraded` |
| 没有指定，目标机器上有同一个池 | 留在同一个池 |
| 没有指定，目标机器上没有同一个池，但有**介质相同**的池 | 自动替换为同介质的池（见下文「同介质自动替换」）；开关关闭时按下一行处理 |
| 没有指定，目标机器上既没有同一个池，也没有可替换的同介质池 | 返回 400，列出这块盘在目标机器上可选的池，要求管理员指定 |

**同介质自动替换**：

- **目的**：不同机器上的池名不同、但盘是同一类时（例如一台有 `local-hdd-a`，另一台只有 `local-hdd-b`，都是机械盘），自动选择目标和维护模式批量迁移不至于迁不动。
- **规则**：
  - 只在 `Media` 相同的池之间替换。**永远不跨介质自动替换**：SSD 换 HDD、HDD 换 SSD 都必须由管理员明确指定。
  - `Media` 为空的池不参与自动替换，既不会被换走，也不会被选作替换目标。所以管理员建池时要标好介质。
  - 内置 `local` 池没有介质标签，也不参与；它和其他池之间的转换只能手动指定。
  - 候选池必须是 `ready` 或 `degraded`，并通过容量准入；有多个时选剩余空间最多的。换到同一个目标池的多块盘合在一起算容量。
- **开关**：系统设置 `MIGRATION_SAME_MEDIA_POOL_FALLBACK`（布尔，默认开启）。按系统设置的现有约定，cpgateway 与 clapi 两边的默认值要保持一致。
- **可见性**：替换写入迁移计划，标记为自动选择并附上原因（例如「自动选择：原池 local-hdd-a 在目标机器上不存在，替换为同为 HDD 的 local-hdd-b」），迁移详情和审计都能看到。
- **管理员手动迁移时**：目标机器上没有原池的话，迁移弹窗按同一条规则预填一个建议的池，并标注「自动选择（同为 HDD）」，管理员可以改成别的。
- **以后开放普通用户自助迁移**时，沿用同一条规则；普通用户如果要自己选池，也只能在同介质的池之间选。

其他规则：

- **GPFS 盘不参与**：迁移时始终留在原来的共享池（GPFS 设计 §7）。本地盘也不能在迁移时换到 GPFS 池；本地和共享之间的转换属于池间搬迁（阶段 L5）。
- **容量准入按目标池汇总**（§6）：换到同一个目标池的多块盘合在一起检查。
- **自动选择目标机器**（包括维护模式的批量迁移，`services/hyper.go`）：
  - 只考虑「每块盘都能留在原池，或者能按上面的规则自动替换」的机器；**优先选不需要替换的机器**，其次才是需要替换的。
  - 跨介质的换池永远不会自动发生。
  - 批量迁移时，找不到合适目标机器的虚拟机被跳过，并在结果里列出原因，由管理员单独处理。
- **计划在创建迁移时写入 `migrations.disk_plan`**（§3.5）。之后的各个阶段（目标准备、源端复制、完成、回滚）都按这份计划执行，不重新计算，避免中途池的状态变化导致前后不一致。

### 7.2 路径

- 目标路径 = 目标池根目录 + 这类盘在池里的相对路径：
  - 本地池：`volumes/volume-<卷ID>.disk`；
  - 内置池：数据盘 `volume/volume-<卷ID>.disk`，系统盘 `instance/inst-<实例ID>.disk`（GPFS 设计 §3.3）。
- **留在同一个池时，源路径和目标路径相同**（§2.3），与现在的行为一致，不改写任何配置。
- **换池时路径不同**，按 §7.3 改写域定义、数据盘描述文件和 NVRAM 路径。

### 7.3 执行流程

`source_migration.sh` 从 clapi 下发的计划里读取每块盘的源路径和目标路径，替代现在从 `virsh domblklist` 里挑出所有文件型磁盘的做法（第 49 行）。步骤如下，换池和不换池的盘走同一套流程，只是不换池时第 2 到 4 步什么都不改：

1. **目标机器预建**：
   - 对每个目标池执行 `pool_guard`（内置池除外）；目标路径已存在就中止。
   - 运行中的虚拟机：按目标路径预建同样虚拟大小的空 qcow2（现在第 67 行）。关机的虚拟机：把磁盘 `scp` 到目标路径（现在第 64 行）。
   - **不再执行 `mkdir -p $(dirname $path)`**。如果目标池没有挂载，这一步会在根分区上建出目录并把磁盘写进去；本地池只写入池初始化时已经建好的 `volumes/` 目录。
2. **改写域定义**：在源机器上执行 `virsh dumpxml --security-info --migratable`，把换池的盘的 `<source file='源路径'/>` 替换成目标路径，得到新的配置文件。
   - 热迁移：在现在第 123 行的命令上增加 `--xml <新配置> --persistent-xml <新配置>`。libvirt 允许目标配置里的磁盘源路径与源端不同，前提是其余部分 ABI 兼容（需要实测，见 §13）。
   - 冷迁移：在现在第 118 行的命令上增加 `--persistent-xml <新配置>`。
   - 所有盘都留在原池时，不带这两个参数，命令与现在完全相同。
3. **改写数据盘的描述文件**：卸载数据盘时要用的 `$xml_dir/<实例>/disk-<卷ID>.xml` 里也写着磁盘路径。现在第 79 行把它们原样复制到目标机器；换池的盘要先替换路径再复制，否则迁移后卸载会找不到盘。
4. **NVRAM**（阶段 L4 起，系统盘在本地池时 NVRAM 放在池里）：系统盘换池时，NVRAM 一起复制到目标池的 `nvram/`，新配置里的 `<nvram>` 路径同样要改写。
5. **完成**：`async_job/complete_migration.sh:24` 在目标机器上重新保存域定义，保存下来的已经是新路径。clapi 收到 `completed` 回调后，在同一个事务里更新每块盘的 `storage_pool_id`、`path`、`hyper`（现在只更新 `hyper`，`rpcs/migrate_vm.go`）。
6. **源端清理**：`finish_source_migration.sh` 按计划删除源路径上的文件（现在按 `$volume_dir/*`、`$image_dir/*` 前缀删，第 32 行），并保留「源机器上还定义着这台虚拟机就跳过清理」的保护。
7. **回滚**：`async_job/clear_target_migration.sh` 按计划删除目标路径上预建的文件（现在第 48、51 行按前缀删）。源端的域定义和磁盘都没有改动过，不用处理。

### 7.4 其他规则

- **有本地盘的虚拟机仍然不允许 `force`**，源机器必须在线，与现在相同。
- **迁移进度**：换池不影响现在的进度上报，`domjobinfo` 统计的是全部复制的数据量。
- **同一台机器上换池（不换机器）不走迁移流程**，属于阶段 L5 的池间搬迁。它和本节共用「按计划改写路径 + 回调更新卷记录」这套逻辑，底层改用 `virsh blockcopy --pivot`（运行中）或 `qemu-img convert`（关机）。

---

## 8. 接口与前端

### 8.1 clapi 接口

| 接口 | 权限 | 说明 |
|---|---|---|
| `POST /storage_pools` | 系统管理员 | 这组存储池接口（增删改查，定义见 GPFS 设计 §10.1）由本文 L1 新建，先只支持 `driver:"local"`，参数为 `{name, driver, media, over_ratio, is_default, description}`；`mount_path` 自动生成，不接受传入。GPFS 设计以后在此基础上增加 `gpfs` |
| `GET /storage_pools` | 所有成员 | 本地池增加 `shared=false`、`media`、`host_count`（有这个池且可用的机器数）；系统管理员另外看到合计容量 |
| `GET /storage_pools/:id/hypers` | 系统管理员 | 本地池返回每台机器的布局、盘、容量、已分配、状态 |
| `POST /hypers/:uuid/disks/scan` | 系统管理员 | 下发 `scan_host_disks.sh`，异步 |
| `GET /hypers/:uuid/disks` | 系统管理员 | 最近一次扫描结果 |
| `GET /hypers/:uuid/storage_pools` | 系统管理员 | 这台机器上的所有本地池 |
| `POST /hypers/:uuid/storage_pools` | 系统管理员 | `{pool:{id}, layout, devices:[by_id...], wipe:bool, confirm:"<机器名>"}`。`confirm` 必须等于机器的 hostname，防止误操作 |
| `POST /hypers/:uuid/storage_pools/:pool_id/extend` | 系统管理员 | `{devices, wipe, confirm}` |
| `DELETE /hypers/:uuid/storage_pools/:pool_id` | 系统管理员 | 池里没有卷时才允许 |
| `POST /instances` | 写权限 | 阶段 L4 增加 `required_storage_pools`（§5.7）；管理员同时指定 `hypervisor` 和 `storage_pool` 时校验这台机器上有这个池（§5.6） |
| `GET /instances/:id/migration_targets` | 系统管理员 | 迁移弹窗用：列出候选目标机器，并为每台机器给出每块本地盘「能否留在原池」、按同介质规则建议的池、全部可选的目标池和剩余容量 |
| `POST /migrations` | 系统管理员（现状） | 增加 `disks: [{volume:{id}, storage_pool:{id}}]`，为指定的盘选择目标池；没有列出的盘留在原池，目标机器没有原池时按同介质规则自动替换（§7.1） |
| `GET /migrations/:id` | 系统管理员（现状） | 增加 `disk_plan` |

校验规则：

- 所选盘必须在最近一次扫描（24 小时内）里是 `free`，或者是 `dirty` 且 `wipe=true`。
- `raid1` 的盘数必须是偶数；`single` 只能选一块。
- 同一台机器的同一个池，只能有一条记录。

### 8.2 cpgateway

- `proxy_routes.go` 的白名单加入以上路由，除 `GET /storage_pools` 外都标记为系统管理员。
- 配额不变（仍然只有一个 `disk_gb`）。

### 8.3 前端

- **计算节点详情页新增「存储」标签页**：
  - 「磁盘」表格：名称、by-id、型号、容量、介质、状态（空闲 / 有旧数据 / 使用中 / 系统盘），以及所属的池。右上角有「扫描」按钮。
  - 「存储池」表格：池名称、介质标签、布局（「单盘」「线性（无冗余）」「RAID1」）、成员盘、容量条（已用、已分配、总量）、状态（降级用警告色）、卷数；操作包括「加盘」和「删除」。
  - **「添加存储池」弹窗**：选存储池（或就地新建一个）、勾选空闲的盘、选布局。选择「线性」时标出「任何一块盘损坏都会导致这个池里所有卷不可用」。最后一步要求输入机器名确认；选中了有旧数据的盘时，再额外列出这些盘会被清除。
- **「存储池」管理页**：由本文 L1 新建，布局按 GPFS 设计 §10.3，以后 GPFS 池直接加入同一个页面。本地池显示「本地」标签、`x/y` 台机器具备、合计容量；详情页列出每台机器的情况。
- **创建云硬盘**：存储池下拉框，每一项显示「本地 / 共享」和介质标签；本地池附带「仅限有此存储池的节点：work-02、work-03」。
- **挂载弹窗**：未落盘的本地池卷，把所在机器没有这个池、或池不可用的虚拟机置灰，并说明原因。现在固定的挂载提示（`attachHint`）改为按卷所在的池显示。
- **云硬盘列表和详情**：显示存储池和所在机器。
- **创建虚拟机**（阶段 L4）：系统盘存储池；管理员先选了宿主机时，只列出这台机器上的池和剩余容量（§5.6）；可选的「还需要这些存储池」。
- **迁移弹窗**（阶段 L2）：
  - 选了目标机器后，下方逐行列出每块本地盘：盘、大小、当前所在的池、目标池下拉框。
  - 目标池默认是「留在 <原池>」。目标机器上没有原池时，预填按同介质规则建议的池并标注「自动选择（同为 HDD）」；没有同介质的池时必须手动选择。每个选项显示它在目标机器上的剩余容量，放不下的置灰；跨介质的选项旁边标出介质变化（例如「HDD → SSD」）。
  - 有某块盘找不到任何可用目标池的机器，在目标机器列表里置灰，并说明是哪块盘。
  - GPFS 盘显示「共享，不复制」，不能选池。
- **迁移详情**：显示每块盘从哪个池迁到哪个池（`disk_plan`）。
- 新文案在三种语言里都要加，并通过 `npm run i18n:check`。

### 8.4 审计

`audit_actions.go` 增加：`hyper.disks_scan`、`hyper.storage_pool_create`、`hyper.storage_pool_extend`、`hyper.storage_pool_delete`。建池和加盘的审计记录里带上盘的 by-id，以及是否执行了清除。

迁移的审计动作不变；换池信息保存在迁移记录的 `disk_plan` 里，可以随时查到。

---

## 9. 安全与风险

| 风险 | 应对 |
|---|---|
| 格式化了错误的盘（系统盘、别人的盘、刚插上的有数据的盘） | 盘用 by-id 标识；节点脚本在执行前重新检查（§4.2 第 1 步）；`system` 类的盘永远不能选；有签名的盘必须显式 `wipe`；输入机器名确认；审计记录 |
| 池没挂载时卷写进了根分区 | `pool_guard` 检查挂载点、fs 类型、标记文件；脚本不对池根目录 `mkdir -p`；迁移预建也要先 `pool_guard`（§7） |
| 线性池坏一块盘，整个池里的卷都不可用 | 建池时明确选择布局，界面醒目提示；推荐 RAID1；池状态和告警（§4.6） |
| RAID1 降级没人发现 | 心跳上报 `degraded` 并告警；计算节点详情页显示降级 |
| 精简置备写满 | 容量准入（§6），加上 90% 实际用量的阈值；以后可以接容量告警 |
| 重启后盘名变了 | 一律用 by-id；fstab 用文件系统 UUID；mdadm.conf 加上 `update-initramfs` |
| 建池做到一半失败 | 回滚已完成的步骤并回调 `error`；记录保留 `error` 状态，管理员可以重试或删除 |
| 两个管理员同时对同一台机器建池 | 节点侧 `flock`；clapi 对 `(hostid, pool_id)` 唯一约束，并拒绝对 `creating` / `removing` 状态的记录再次操作 |
| 盘从一台机器拔下插到另一台 | 标记文件里带 hostid，不匹配时池为 `unavailable`，不会被当成本机的池使用 |
| 换池迁移时改写的配置有误，热迁移失败，或者目标机器上的定义不对 | 只改写 `<source file>` 和 `<nvram>` 两处；改写后先用 `virt-xml-validate` 校验，不通过就中止，不开始复制；验收覆盖热迁移、冷迁移、UEFI 虚拟机（阶段 L2） |
| 换池迁移后，数据盘的描述文件还是旧路径，卸载失败 | §7.3 第 3 步改写；验收时迁移后做一次卸载和重新挂载 |
| 迁移中途失败，目标池里留下预建的文件 | 回滚按计划清理（§7.3 第 7 步）；阶段 L5 的孤儿文件对账兜底 |
| 介质标签标错，自动替换把盘换到了性能不同的池 | 建池时介质按磁盘扫描结果预填（§4.1），管理员修改时要确认；介质为空的池不参与自动替换；每一次自动替换都写进迁移计划并标注原因，迁移详情里可以看到 |
| 创建迁移之后、执行之前，目标池的状态或容量变了 | 目标准备阶段对每个目标池再执行一次 `pool_guard`；容量不够时 `qemu-img create` 或 `scp` 失败，走回滚 |

---

## 10. 部署

- `deploy-compute-node.sh` 安装 `lvm2`、`mdadm`、`xfsprogs`（现在三台节点都已经有了，但部署脚本里没有显式安装，新节点不一定有），并创建 `/opt/cloudland/pools`。**不自动建池**，统一由管理员在界面上操作。
- 可选：部署时通过环境变量 `LOCAL_POOL_DISKS=<by-id,...>` 和 `LOCAL_POOL=<池名>`，在节点注册后由 clapi 自动下发建池命令，方便批量部署。放在阶段 L3 之后再做。
- **现有三台机器的落地**（阶段 L1 验收时做）：
  - work-01、work-03：`/disk1`（`sdb1`，ext4，只有 `lost+found`）先卸载并删掉 fstab 里的对应行，然后在界面上对 `sdb` 执行「清除 + 建池」。
  - work-02：`sdb1` 没有挂载，**先确认里面没有需要保留的数据**，再同样处理。
  - 三台机器都只有一块空闲盘，只能用 `single` 布局（无冗余）。建一个 `local-hdd` 池；等以后加了盘，再按需加盘或新建 RAID1 池。

---

## 11. 实施阶段

### 阶段 L0：存储池抽象（取自 GPFS 设计阶段 0）

本文先于 GPFS 设计实施，所以 GPFS 设计阶段 0 里本地存储需要的部分在这里完成，**后续所有阶段都依赖它**。完成后行为与现在一致，只有内置 `local` 一个池。

**前置决策**：WDS 的去留（待办 C1）。L0 要改约 20 处按 `wds_address` 分支的脚本和约 16 处 `GetVolumeDriver()`，GPFS 设计 §1.4 建议先决定放弃 WDS，这样可以直接删掉这些分支。这个决定原本排在 GPFS 阶段 0 之前，现在提前到 L0 之前。

**范围**（对应 GPFS 设计 §14 阶段 0，去掉 GPFS 专用的部分）：

- 建 `storage_pools`，启动时自动创建内置 `local` 池（字段见 §3 开头的说明）；`volumes.storage_pool_id`；如实记录路径（系统盘为 `instance/inst-N.disk`）；卷的响应里增加 `storage_pool`。
- clapi 里约 16 处 `GetVolumeDriver()` 改为按卷判断；下发命令时带上绝对路径。
- 系统盘相关的元数据增加 `boot_disk`；迁移、删除、救援用的卷 JSON 带上驱动和路径。
- 节点脚本约 20 处按 `wds_address` 的分支改为按单块盘判断（放弃 WDS 的话直接删除）；去掉 `$volume_dir/inst-N.disk` 的回退逻辑。
- 顺带修复 GPFS 设计附录 B 第 1 到 7 条。
- **`report_rc.sh:257` 的 `du -s $mount_point` 改为 `du -x`**：这里的 `$mount_point` 是根分区 `/`。本地池挂在 `/opt/cloudland/pools/*` 之后，不加 `-x` 就会把池里的数据算进根分区的用量，节点用于调度的磁盘统计会出错，每次心跳（1–20 秒一次）还要把池目录遍历一遍。GPFS 设计 §5.4 为 GPFS 提出了同样的修改，本地池同样需要。

**不包含**（留给 GPFS 设计）：`gpfs` 驱动及其脚本、`image_storages` 的改造、镜像基础文件与 `mmclone`、共享存储迁移、节点宕机恢复，以及 GPFS 专用的字段。

**验收**：

- `test-items/` 回归全部通过：创建、挂载、扩容、卸载、删除、热迁移（带数据盘）、关机迁移、重装、救援（要能访问原盘）、捕获镜像、删除虚拟机。
- 数据库里每个卷的路径都与节点上的实际文件一致。
- 在节点上另挂一个目录后，心跳上报的根分区用量与 `df` 一致，不包含那个目录里的数据。

### 阶段 L1：本地存储池与数据卷

**范围**：

- `storage_pools` 支持 `driver=local`；`hyper_storage_pools` 的本地字段；`hyper_disks`。
- 节点脚本：`scan_host_disks.sh`、`create_local_pool.sh`、`remove_local_pool.sh`、`pool_guard` 扩展、心跳里的状态和容量上报。
- 接口：扫描、查询磁盘、建池、删池（先不做加盘）。
- 数据卷：按池建卷、首次挂载的机器和池校验、容量准入（包括内置池）、扩容准入、删除。
- 迁移：只支持留在同一个池（目标机器必须有同一个池）；目标机器的池校验；预建磁盘前 `pool_guard`，不再 `mkdir -p`。
- 前端：计算节点详情的「存储」标签页（扫描、建池、删池）；建卷时选存储池；挂载弹窗置灰。

**验收**：

- 在 work-03 上用 `sdb` 建 `local-hdd`（single）。建一块 `local-hdd` 卷挂到 work-03 上的虚拟机，文件出现在 `/opt/cloudland/pools/<UUID>/volumes/`，根分区上没有这块盘。
- 挂到 work-01 上的虚拟机被拒绝，提示 work-01 没有这个池。
- work-01 也建好 `local-hdd` 后，把挂着这块卷的虚拟机从 work-03 热迁移到 work-01：数据完整，目标路径与源路径相同。
- work-01 的池还没建好时，迁移被拒绝并列出缺少的池。
- 手动卸载 work-03 的池目录：5 分钟内状态变为 `unavailable`；挂载、扩容、迁移预建都失败；根分区上没有产生任何文件。
- 容量准入：在一个很小的池上（可以用 loop 设备模拟），超过 `容量 × 超分比例` 的首次挂载和扩容被拒绝。
- 建池时选中系统盘，节点脚本拒绝执行；选中有签名的盘但没勾「清除」，拒绝执行。
- 重启 work-03：池自动挂载，池里有盘的虚拟机在挂载完成后正常启动。

### 阶段 L2：迁移时换池

**范围**：

- `migrations.disk_plan`（§3.5）；`POST /migrations` 的 `disks` 参数；`GET /instances/:id/migration_targets`。
- 同介质自动替换（§7.1）与系统设置 `MIGRATION_SAME_MEDIA_POOL_FALLBACK`；自动选目标时的优先顺序；维护模式批量迁移跳过并列出原因。
- `source_migration.sh`：按计划预建；改写域定义和数据盘描述文件；换池时带 `--xml` / `--persistent-xml`。`finish_source_migration.sh`、`async_job/clear_target_migration.sh` 按计划清理。
- `rpcs/migrate_vm.go`：完成时更新 `storage_pool_id`、`path`、`hyper`。
- 前端：迁移弹窗逐盘选择目标池；迁移详情显示计划。
- 这一阶段只处理数据盘，以及仍在内置池里的系统盘。系统盘放进本地池（包括它的 NVRAM）要等阶段 L4。

**验收**：

- 运行中的虚拟机，数据盘从 work-03 的 `local-hdd` 热迁移到 work-01 的内置 `local` 池，再反方向迁回：数据完整；目标文件在新池的路径下；源文件已删除；卷记录的池和路径已更新；迁移后卸载、重新挂载这块盘都正常。
- 同样的换池做一次关机迁移。
- 一块盘留在原池、另一块换池的混合情况。
- 目标机器没有原池、请求里也没有指定目标池时，创建迁移返回 400，并列出可选的池。
- 目标机器预建之后人为让迁移失败：回滚后目标池里没有残留文件，源端虚拟机正常运行。
- 同介质自动替换：work-01 上建 `local-hdd-b`（HDD）、不建 `local-hdd`，把 `local-hdd` 里的盘迁到 work-01 且不指定目标池，结果落在 `local-hdd-b`，迁移详情标注为自动选择；关闭 `MIGRATION_SAME_MEDIA_POOL_FALLBACK` 后同样的请求返回 400。
- 目标机器只有 `ssd` 介质的池时，`hdd` 盘不会被自动替换过去，创建迁移返回 400。
- 自动选择目标机器时，优先选能保留原池的机器；维护模式批量迁移中找不到目标的虚拟机被跳过并列出原因。

### 阶段 L3：加盘、RAID1、告警

**范围**：

- `extend_local_pool.sh` 与接口、界面；`raid1` 布局（建池与按对加盘）。
- 心跳上报 `degraded`，并接入节点告警。
- 部署脚本安装依赖、创建目录；可选的部署时自动建池。

**验收**：

- `linear` 池在线加盘：容量增加，池里运行中的虚拟机不受影响。
- 用两块盘（可以用 loop 设备）建 `raid1`，`mdadm --fail` 一块盘后：池显示降级并产生告警；池里的虚拟机继续读写；换盘重建完成后恢复为 `ready`。

### 阶段 L4：系统盘与调度

**范围**：

- 系统盘可以放在本地池；`GetHyperGroup` 按存储池过滤；`rcNeeded` 调整；NVRAM 放进池里。
- 重装、救援、捕获镜像、删除虚拟机使用系统盘的实际路径（GPFS 设计 §6.7、§6.8 已经覆盖）。
- `required_storage_pools`；创建虚拟机界面，包括管理员先选宿主机、再从这台机器上的池里选（§5.6）。
- 迁移时系统盘换池，NVRAM 一起迁移（§7.3 第 4 步）。

**验收**：

- 选择 `local-ssd` 作为系统盘池创建虚拟机，只会被调度到有这个池的机器上；系统盘和 NVRAM 都在池里。
- 管理员指定 work-01 后，系统盘存储池只列出 work-01 上的池；选一个 work-01 没有的池时，后端拒绝。
- 这台虚拟机的重装、救援、热迁移、删除全部正常，删除后池里没有残留文件。
- 一台 UEFI 虚拟机，系统盘从内置池热迁移到另一台机器的 `local-ssd`：正常启动，启动项和 Secure Boot 状态保持不变（NVRAM 迁移正确）。

### 阶段 L5（可选）

- 同一台机器上换池（池间搬迁）：复用 §7 的计划与路径改写，底层用 `virsh blockcopy --pivot` 或 `qemu-img convert`。本地盘与 GPFS 盘之间的转换也放在这里。
- 按存储池分别计配额（需要 cpgateway 增加配额项）。
- 容量告警、孤儿文件对账（与 GPFS 设计阶段 4 合并做）。

---

## 12. 测试环境

- 现在的三台机器都只有一块空闲的机械盘。功能测试可以都建成 `local-hdd`；**要测「混用」**，可以再建一个用 loop 设备做的池，把它标记为 `ssd`，行为上与真正的 SSD 一样（介质只是标签）。
- RAID1、加盘、容量准入的测试都用 loop 设备（`losetup` 建在 `/opt/cloudland/cache/tmp` 以外的位置），不去动 `sdb`。
- 迁移换池（阶段 L2）不需要额外的盘：在 `sdb` 上的 `local-hdd`、loop 设备做的 `local-ssd` 和内置 `local` 池之间来回迁即可。
- 回归：`test-items/` 里涉及卷与迁移的用例全部通过，内置池的行为保持不变。

---

## 13. 待决策

1. **WDS 的去留（待办 C1），要在 L0 之前决定**。执行顺序已定为本文先于 GPFS 设计，L0 承担了 GPFS 阶段 0 的抽象工作，其中改动最多的就是 WDS 分支（约 20 个脚本、约 16 处 `GetVolumeDriver()`）。放弃 WDS 的话直接删除这些分支；保留的话要把 WDS 也接入存储池抽象，工作量明显增加，而且没有环境验证。
2. **默认的布局与推荐做法**：建议界面默认选 `raid1`（盘数允许时），`linear` 要额外确认。现有三台机器只有一块空闲盘，只能 `single`，要接受「坏盘即丢卷」，靠备份兜底。
3. **默认池**：建议保持内置 `local`。如果希望新卷默认落在 `local-hdd`，要先保证每台机器都有这个池。
4. **是否需要按盘隔离**（同一台机器上同一个池有多个目录）：本文不支持，需要时建成多个池。如果确实需要自动在多个同类目录间分布，要另外设计放置策略。
5. **容量未知时是否拒绝**：本文选择拒绝（§6），因为本地池的记录一定来自上报；如果希望更宽松，可以改为只告警。
6. **迁移时的自动换池**：已定为「只在同介质的池之间自动替换，永远不跨介质」（§7.1），由系统设置 `MIGRATION_SAME_MEDIA_POOL_FALLBACK` 控制，默认开启。需要确认的是默认值：开启可以让维护模式迁移更顺利；关闭则任何换池都要人工确认。
7. **需要实测的一点**：热迁移时用 `--xml` / `--persistent-xml` 改写磁盘源路径，本文按 libvirt 文档判断是可行的，阶段 L2 开工前应先在 work-x 上用一台临时虚拟机手工验证（包括 UEFI 虚拟机）。

---

## 附录 A：受影响的代码

**L0** 的改动清单就是 GPFS 设计附录 A 里标为阶段 0 的条目，去掉 GPFS 专用的部分，再加上 `report_rc.sh:257` 改用 `du -x`，这里不重复列出。以下是 L1–L5 的改动。

### clapi（`api/src/`）

- `model/storage_pool.go`（L0 新建，结构按 GPFS 设计 §4.1、§4.2）：`StoragePool` 增加 `Media`；`HyperStoragePool`（L1 新建）增加本地字段；新增 `HyperDisk`。
- 存储池的 service 层与接口（L1 新建）：存储池的增删改查，先只支持 `driver=local`，以及本地池 `MountPath` 的生成。GPFS 设计阶段 1 在此基础上增加 `gpfs` 驱动。
- 新增 `services/hyper_storage.go`：扫描、建池、加盘、删池；按「机器 × 池」的容量准入（事务 + 行锁）。
- `services/volume.go`：首次挂载（约第 440–455 行）增加池和容量校验；扩容增加准入；删除时下发路径。
- `services/migration.go`：生成并保存迁移计划（`disk_plan`），包括同介质自动替换；目标池校验与按目标池汇总的容量准入；自动选目标时优先选能保留原池的机器；`POST /migrations` 的 `disks` 参数。
- 系统设置 `MIGRATION_SAME_MEDIA_POOL_FALLBACK`：cpgateway 的设置定义与校验、clapi 读取的镜像值（两边默认值一致），系统设置页面与三种语言的文案。
- `model/`：`Migration` 增加 `DiskPlan`。
- `rpcs/migrate_vm.go`：第 24 行的 `VolumeInfo` 增加源路径、目标路径、目标池；完成回调更新 `storage_pool_id`、`path`、`hyper`（现在只更新 `hyper`）。
- `apis/`：新增 `GET /instances/:id/migration_targets`；迁移详情返回 `disk_plan`。
- `common/instance.go:32` `GetHyperGroup`：阶段 L4 增加存储池过滤。
- `rpcs/`：新增 `host_disks`、`local_pool_status` 回调。
- `apis/`：`hyper.go` 增加磁盘和存储池子路由；`storage_pool.go` 支持本地池；`audit_actions.go` 增加动作。

### 节点脚本（`scripts/`）

- 新增：`kvm/scan_host_disks.sh`、`kvm/create_local_pool.sh`、`kvm/extend_local_pool.sh`（L3）、`kvm/remove_local_pool.sh`。
- `cloudrc`：`pool_guard` 增加挂载点和 hostid 检查；节流上报用的辅助函数。
- `kvm/report_rc.sh`：本地池的状态和容量上报（节流）；`sync_instance` 等待池挂载。
- `kvm/attach_volume_local.sh:10`、`kvm/resize_volume_local.sh`、`kvm/clear_volume_local.sh`：使用下发的路径，并执行 `pool_guard`。
- `kvm/source_migration.sh`：磁盘清单改读下发的计划（第 49 行）；预建前 `pool_guard`，不再 `mkdir -p`（第 64、67 行）；换池时改写并复制数据盘描述文件（第 79 行）；冷迁移和热迁移命令按需增加 `--persistent-xml` / `--xml`（第 118、123 行）。
- `kvm/finish_source_migration.sh:32`、`kvm/async_job/clear_target_migration.sh:48`、`:51`：按计划删除文件，不再按目录前缀删。
- `kvm/launch_vm.sh`（L4）：本地池系统盘。

### 其他

- `cpgateway/src/apis/proxy_routes.go`：新路由加入白名单。
- `deploy/docker/scripts/deploy-compute-node.sh`：安装 `lvm2`、`mdadm`、`xfsprogs`，创建 `/opt/cloudland/pools`。
- `web/`：计算节点详情的「存储」标签页，建卷、挂载、迁移、创建虚拟机的相关改动，三种语言的文案。
