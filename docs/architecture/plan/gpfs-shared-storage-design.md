# GPFS 共享存储支持设计（本地卷与 GPFS 卷并存）

- **状态**：设计草案，未实施
- **日期**：2026-09-22，基于 `stage-01` 分支 `53dceb82`（文中行号均以此为准）
- **涉及**：clapi（`api/`）、节点脚本（`scripts/`）、cpgateway 代理白名单、前端（`web/`）
- **相关文档**：`storage-gpfs-ceph-integration-plan.md`、`gpfs-deployment-plan.md`，两者与本文的关系见 §13
- **执行顺序**：`local-multi-disk-storage-plan.md`（本地多盘存储池）**先于本文实施**。本文阶段 0 中与本地存储有关的部分由它的阶段 L0a / L0b 完成（`storage_pools`、`hyper_storage_pools` 和存储池接口也由它先建出来），本文在其基础上增加 `gpfs` 驱动，见 §14 阶段 0 的说明。两份文档对同一张表、同一个脚本的描述不一致时，**以本地方案为准**，本文只增加 GPFS 专用的部分（§3.3、§4.2、§6.2 已加注）

---

## 0. 摘要

- **存储池成为一等资源**：新增 `storage_pools` 表，类型为 `local` 或 `gpfs`。每个卷（系统盘、数据盘）都属于一个存储池，由存储池决定驱动。取消全局开关 `volume.driver` 和节点上按 `wds_address` 的分支。
- **一个 GPFS 类型的 CloudLand 存储池 = 一个独立 fileset 的入口目录**。数据放在哪种介质上，由 GPFS 放置规则决定；池的容量由 fileset 配额决定。CloudLand 不管理 GPFS 集群本身。
- **clapi 把每块盘的驱动和绝对路径下发给节点脚本**，脚本按单块盘执行，不再自己推算路径。
- **GPFS 卷可以挂给任何挂载了该存储池的节点上的虚拟机**。GPFS 系统盘从池内的镜像基础文件用 `mmclone` 秒级克隆，不整盘复制。
- **全部磁盘都在 GPFS 上的虚拟机，热迁移不复制磁盘**。源节点宕机时，可以在确认隔离后到其他节点恢复。
- **分四个阶段实施**：0 重构（只有 local，行为不变）→ 1 GPFS 数据卷 → 2 GPFS 系统盘与共享迁移 → 3 节点宕机恢复。

---

## 1. 背景与目标

### 1.1 现状

- **存储类型只有一个区域级的全局开关**，分两处：
  - clapi 读取 `volume.driver`（`services/volume.go:27`），没配置时为 `local`。没有任何部署方式会写 `[volume]` 配置段，所以现有环境全是 local。
  - 节点脚本按 `cloudrc.local` 里的 `wds_address` 是否为空来分支，约 20 个脚本有这种判断。
  - clapi 里调用 `GetVolumeDriver()` 的地方约 16 处。
- **卷记录没有存储类型字段**，路径的记法也不统一：
  - 本地数据卷的 `path` 是 `volume-<卷ID>.disk`。
  - 本地系统盘在库里同样记为 `volume-<卷ID>.disk`，但实际文件是 `$image_dir/inst-<实例ID>.disk`。脚本从不读系统盘的库内路径，而是按实例 ID 自己拼。
  - `model/volume.go:57-66` 注释里写的 `local://` 格式与实际不符。
- **本地存储的限制**：
  - 已落盘的卷只能挂给同一节点上的虚拟机。
  - 迁移要复制全部磁盘（`--copy-storage-all`），并且源节点必须在线。
  - 节点宕机后，上面的虚拟机在节点恢复之前无法恢复。

### 1.2 目标

1. 同一区域内本地卷与 GPFS 卷并存，创建卷或虚拟机时按存储池选择。
2. GPFS 卷可以挂给任何挂载了该存储池的节点上的虚拟机。
3. 系统盘可以放在 GPFS 上，创建时不整盘复制镜像。
4. 全部磁盘都在 GPFS 上的虚拟机，迁移时不复制磁盘。
5. 源节点宕机时，全部磁盘都在 GPFS 上的虚拟机可以在其他节点恢复。
6. 按存储池统计容量，创建时做容量准入。

### 1.3 非目标

- GPFS 集群本身的安装、扩容和升级。可参考 `gpfs-deployment-plan.md` §3 至 §8，但要先看本文 §13 列出的需要重新核对之处。
- Ceph。本文保留了驱动的扩展位，见 §12。
- 卷在存储池之间搬迁。第一版不做，后续可以做离线复制。
- 用 GPFS 策略做自动冷热分层。虚拟机磁盘不适合，原因见 §2.3。
- 按存储池分别计配额。cpgateway 仍然只有一个 `disk_gb`。
- 存量数据迁移。产品还没有上线，直接按目标方案改。

### 1.4 前置决策：WDS 的去留（待办 C1）

WDS 也是一种「非本地」驱动，它的分支和本设计要改的地方高度重合（全局开关、`pool_id` 字符串、`image_storages`、约 20 个脚本分支）。

**建议在阶段 0 之前决定放弃 WDS**。阶段 0 重构时可以直接删掉 WDS 分支，驱动抽象会干净得多。WDS 目前没有测试环境，保留它就意味着一大块改动无法验证。

如果决定保留，WDS 就作为 `storage_pools` 的一种驱动（`wds_vhost`）接入同一套抽象：WDS 的池 ID 放进存储池参数，`wds_vhost://` 路径格式保留。这样工作量大约增加三分之一，而且无法验证。本文以下内容按放弃 WDS 来写。

---

## 2. GPFS 概念与映射

### 2.1 用到的概念

| 概念 | 说明 | 与 CloudLand 的关系 |
|---|---|---|
| 文件系统 | 顶层单位，有挂载点、块大小、副本数，有唯一一份策略。存储池和 fileset 都属于某个文件系统。 | 一个 GPFS 文件系统里可以有多个 CloudLand 存储池 |
| 存储池（GPFS） | 物理层，按介质把磁盘分组，如 `system`、`ssd`、`hdd` | 由放置规则间接使用，CloudLand 不直接操作 |
| fileset | 文件系统里的一棵目录子树，通过入口目录链接到命名空间。**独立 fileset**有自己的 inode 空间，可以单独做快照和配额。 | **一个 CloudLand 存储池 = 一个独立 fileset** |
| 放置规则 | 创建文件时决定数据放进哪个物理池，可以按 fileset 匹配 | 管理员把每个 fileset 固定到一个物理池 |
| fileset 配额 | fileset 的容量和文件数上限 | 作为 CloudLand 存储池的容量 |
| `mmclone` | 文件级写时复制克隆，父文件变成只读 | 从镜像基础文件克隆系统盘 |

### 2.2 为什么选「独立 fileset」作为存储池

- **驱动实现简单**：fileset 有固定的入口目录，驱动只需要知道一个路径，不必调用 GPFS 的池管理命令。
- **介质固定**：一条 `SET POOL ... FOR FILESET` 规则就能把 fileset 固定到 SSD 或 HDD。用户选的存储池就是实际使用的介质。
- **容量可以单独统计**：fileset 配额提供按池的容量和用量（`mmlsquota -j`）。
- **克隆可用**：`mmclone` 要求父文件和克隆出来的文件在同一个 inode 空间里。镜像基础文件和系统盘都放在同一个独立 fileset 里，就满足这个条件（仍需实测，见 §16）。
- **跨 fileset 不能 `mv`**（会报 `EXDEV`）。这也是本设计不做「池间搬迁」的原因，真要搬就是完整的数据复制。

### 2.3 放置规则的使用约定

- 每个 CloudLand 用的 fileset 只配一条放置规则，把它固定到一个物理池。
- **不要用 `LIMIT` 做溢出**。溢出会让「SSD 池」在 SSD 满了以后悄悄把新盘写到 HDD，用户选的和实际拿到的不一致。空间不足应该由 CloudLand 的容量准入拒绝（§5.3）。
- **不要对虚拟机磁盘所在的 fileset 配迁移规则**。GPFS 按整个文件分层，一块虚拟机磁盘要么整块在 SSD，要么整块在 HDD；运行中的虚拟机一直在读写，访问时间永远是新的，按访问时间迁移没有意义，还会产生大量 I/O。

### 2.4 GPFS 管理员的准备工作（每个 CloudLand 存储池做一次）

文件系统层面（只做一次）：

- 所有计算节点都已加入 GPFS 集群，并**在相同路径**挂载这个文件系统。
- 开机自动挂载：`mmchfs <fs> -A yes`。
- 开启配额：`mmchfs <fs> -Q yes`。

每个 CloudLand 存储池：

```bash
# one independent fileset per CloudLand storage pool
mmcrfileset  fs1 cl_ssd --inode-space new --inode-limit 2000000
mmlinkfileset fs1 cl_ssd -J /gpfs/fs1/cl_ssd

# pin the fileset to one physical pool (part of the fs-wide policy file, first match wins)
#   RULE 'cl_ssd'  SET POOL 'ssd' FOR FILESET ('cl_ssd')
#   RULE 'cl_hdd'  SET POOL 'hdd' FOR FILESET ('cl_hdd')
#   RULE 'default' SET POOL 'hdd'
mmchpolicy fs1 policy.txt -I test && mmchpolicy fs1 policy.txt

# capacity of this CloudLand pool
mmsetquota fs1:cl_ssd --block 50T:50T
```

做完以后，在 CloudLand 管理界面登记这个存储池（§10.1），填入 `mount_path=/gpfs/fs1/cl_ssd`、`fs_name=fs1`、`fileset=cl_ssd`。

### 2.5 池内目录结构（由 CloudLand 创建）

```
/gpfs/fs1/cl_ssd/                  <- mount_path (fileset junction)
├── .cloudland-pool                <- marker file holding the pool UUID (guard against unmounted path, §5.1)
├── volumes/volume-<volID>.disk    <- boot and data disks (qcow2)
├── images/image-<imgID>-<prefix>.qcow2   <- per-pool image base, clone parent (§6.6)
├── nvram/inst-<instID>_VARS.fd    <- UEFI NVRAM of instances whose boot disk is in this pool
└── tmp/                           <- staging area for imports
```

---

## 3. 总体设计

### 3.1 核心原则

1. **存储类型是卷的属性，不是区域的属性**：卷 → 存储池 → 驱动。全局开关 `volume.driver` 删除。
2. **clapi 是路径的唯一来源**：建卷记录时就确定相对路径，下发命令时拼成绝对路径传给脚本。脚本不再按实例 ID 或卷 ID 自己拼路径，也不再用 `wds_address` 判断存储类型。
3. **按单块盘处理**：同一台虚拟机可以同时有本地盘和 GPFS 盘。凡是涉及磁盘的脚本（迁移、删除、救援等），都从 clapi 下发的磁盘清单里读每块盘的驱动和路径。
4. **共享盘永远不按路径模式删除**：只删 clapi 明确点名的文件，并且要在确认虚拟机已销毁之后（§9.3）。

### 3.2 组件分工

| 组件 | 职责 |
|---|---|
| clapi | 存储池记录、节点对存储池的可用性、为每块盘确定驱动和路径、按存储池可用性过滤调度候选节点、容量准入、处理回调 |
| cland | **不改**。候选节点列表由 clapi 拼进 `select=group-...:<hostid 列表>`，cland 只在列表里选节点（`cland/dispatcher.go:230-241`） |
| 节点脚本 | 按下发的每块盘执行操作；写入前做挂载保护；上报存储池可用性和容量 |
| cpgateway | 代理白名单增加存储池接口；配额不变 |
| 前端 | 选择存储池、显示存储池和类型；新增管理员的存储池页面 |

### 3.3 驱动与路径约定

驱动取值：`local`、`gpfs`（为 Ceph 预留 `ceph_rbd`）。

`volumes.path` 存**相对于存储池根目录的路径**，绝对路径 = 池根目录 + `/` + `path`。内置本地池的根目录固定为 `/opt/cloudland/cache`。

> 本地方案实施后还会有非内置的本地池（根目录 `/opt/cloudland/pools/<池UUID>`，池内布局与 gpfs 池相同，见本地方案 §2.3）。下表的「local 池」一列指内置本地池。

| 磁盘 | local 池（根目录 `/opt/cloudland/cache`） | gpfs 池（根目录 = `mount_path`） |
|---|---|---|
| 系统盘 | `instance/inst-<实例ID>.disk`（文件位置不变，只是如实入库） | `volumes/volume-<卷ID>.disk` |
| 数据盘 | `volume/volume-<卷ID>.disk` | `volumes/volume-<卷ID>.disk` |
| 镜像 | 节点本地缓存 `image/…`，不入库（与现在相同） | `images/image-<镜像ID>-<前缀>.qcow2`，记录在 `image_storages` |
| UEFI NVRAM | `instance/inst-<实例ID>_VARS.fd`，不入库 | `nvram/inst-<实例ID>_VARS.fd`，跟随系统盘放在池里 |
| 救援盘 | 永远在本地：`instance/inst-<实例ID>-rescue.disk` | 同左 |

其他约定：

- 删除 `launch_vm.sh`、`reinstall_vm.sh`、`rescue_vm.sh` 里「先找 `$volume_dir/inst-N.disk`」的回退逻辑。没有任何脚本会创建这个文件；而当它存在时，`launch_vm.sh` 不发 `create_volume` 回调，系统盘会一直停在 `pending`。
- **`volumes.hyper` 只对本地池有意义**，表示卷文件所在的节点。GPFS 卷的 `hyper` 恒为 0。

---

## 4. 数据模型

表结构由启动时的 AutoMigrate 创建，不写迁移代码。AutoMigrate 不会删除列，下文删掉的字段对应的列会留在库里，不影响使用。

### 4.1 `storage_pools`（新增）

```go
// StoragePool is where volume files live. Every volume belongs to exactly one pool.
type StoragePool struct {
	Model
	Name          string     `gorm:"type:varchar(64);uniqueIndex"`
	Driver        string     `gorm:"type:varchar(32)"`  // local | gpfs
	MountPath     string     `gorm:"type:varchar(256)"` // gpfs: fileset junction, identical on every node
	FsName        string     `gorm:"type:varchar(64)"`  // gpfs: file system device, used by mmlsquota
	Fileset       string     `gorm:"type:varchar(64)"`  // gpfs: independent fileset name
	FsType        string     `gorm:"type:varchar(16)"`  // expected statfs type of MountPath, "gpfs" (nfs only in test envs)
	Status        string     `gorm:"type:varchar(32)"`  // active | disabled
	IsDefault     bool       // no gorm default tag: false is meaningful
	OverRatio     float64    // max provisioned/capacity, qcow2 is thin
	CloneMode     string     `gorm:"type:varchar(16)"` // mmclone | copy
	CapacityBytes int64      // from the fileset quota, 0 = unknown
	UsedBytes     int64
	CapacityAt    *time.Time
	Description   string     `gorm:"type:varchar(256)"`
}
```

- **内置的本地池**：clapi 启动时如果不存在就自动创建，名为 `local`、`Driver=local`、`MountPath=/opt/cloudland/cache`。它不能删除，也不能改驱动和路径，保证「每个卷都有存储池」这个前提始终成立。
- **`IsDefault`** 全局最多一个，新建系统盘和数据盘时不指定存储池就用它；没有设置默认池时用内置本地池。这类零值有业务含义的布尔字段，不要加 GORM 的 `default` 标签（见 CLAUDE.md 的相关约定）。
- **`Status=disabled`**：不再接受新卷，已有卷照常使用。
- **创建后不能修改的字段**：`MountPath`、`FsName`、`Fileset`、`Driver`。只要池里有卷或镜像基础文件，就不允许修改。

### 4.2 `hyper_storage_pools`（新增）

记录每个节点对每个存储池的可用性，由节点上报（§5.2）：

```go
type HyperStoragePool struct {
	Model
	Hostid    int32     `gorm:"uniqueIndex:idx_hyper_pool"`
	PoolID    int64     `gorm:"uniqueIndex:idx_hyper_pool"`
	Status    string    `gorm:"type:varchar(32)"` // ready | unavailable
	Reason    string    `gorm:"type:varchar(256)"`
	CheckedAt time.Time
}
```

- 超过 15 分钟没有收到上报，按 `unavailable` 处理。
- 删除节点时同时删除该节点的记录。

> **以本地方案 §3.2 为准**：这张表由本地方案 L1 先建出来，本地池（包括内置池）同样有记录，并且多出布局、盘、容量等字段和 `creating`、`degraded`、`lost` 等状态。GPFS 池的记录只用其中的 `ready` / `unavailable`，其余字段留空。原先「本地池不需要这张表」的说法作废。删除节点时：这台节点上有本地池记录或本地卷时拒绝删除（本地方案 §5.9），GPFS 池的记录照旧随节点一起删除。

### 4.3 `volumes` 的变更

| 字段 | 变更 |
|---|---|
| `StoragePoolID int64`（带索引） | **新增**，不能为空 |
| `PoolID string` | **删除**（原来是 WDS 的池 ID） |
| `Path` | 改为池内相对路径（§3.3），**由 clapi 在建记录时写入**，回调不再回传路径 |
| `Hyper` | 语义收窄：只对本地池有意义 |
| `BaseImageStorageID int64` | **新增**：GPFS 系统盘克隆自哪个镜像基础文件（`image_storages.id`）；0 表示整盘复制、没有父文件。用于删除镜像基础文件时的引用计数（§6.6） |

模型方法：

- 删除 `GetVolumeDriver/GetVolumePath/GetVolumePoolID/GetOriginVolumeID`，以及 `parse` 系列函数。
- 新增 `Driver()`（取所属池的驱动）、`AbsPath()`、`IsShared()`。它们都依赖预加载的 `StoragePool`，调用方要么预加载，要么显式查询。

### 4.4 `image_storages` 的变更

现有的 `ImageStorage` 模型用来记录「一个镜像在一个存储池里有一份副本」，正好对应 GPFS 池内的镜像基础文件，所以改造后继续用：

| 字段 | 变更 |
|---|---|
| `PoolID string` | 改为 `StoragePoolID int64`，与 `ImageID` 组成唯一索引 |
| `VolumeID string` | 改为 `Path string`，即基础文件在池内的相对路径 |
| `Status` | 保留 `syncing/synced/error`，新增 `deleting`（镜像已删除，但还有系统盘引用它，§6.6） |

### 4.5 删除的配置与字段

- clapi 配置 `volume.driver`、`volume.default_wds_pool_id`，以及 WDS 的 QoS 默认值 `volume.default_iops_*` / `default_bps_*`。
- 字典分类 `storage_pool`（`model/dictionary.go:15`）及相关校验：`services/storage.go` 的 `InitStorages` 和从未被调用的 `CheckDefaultPool`，`services/backup.go:93-98`。
- `Image.StorageType`：只写不读（`services/image.go:112`）。
- 节点上 `cloudrc.local` 里的 `wds_*` 配置和 `cloudrc` 里的 WDS 辅助函数（按放弃 WDS 的前提）。

---

## 5. 节点侧：挂载保护、可用性与容量

### 5.1 挂载保护

**风险**：GPFS 没挂载时，`/gpfs/fs1/cl_ssd` 只是根文件系统上的一个空目录，或者干脆不存在。如果脚本照常 `mkdir -p` 再写文件，写入会「成功」地落在本地磁盘上；等 GPFS 挂载后这些文件又被遮住。这会造成数据错乱，而且没有任何报错。

**做法**：`cloudrc` 新增一个函数 `pool_guard <根目录> <池UUID> <期望的文件系统类型>`。所有写 GPFS 池的脚本在**第一次写入之前**都必须调用它，任一条件不满足就报错退出，不做任何写入：

1. `stat -f -c %T <根目录>` 的结果等于期望的类型（`gpfs`）。fileset 的入口目录不是挂载点，所以不能用 `mountpoint -q` 判断。
2. `<根目录>/.cloudland-pool` 文件存在，内容等于池的 UUID。
3. 脚本里**不对池根目录本身做 `mkdir -p`**，子目录在池初始化时一次性建好（§10.1）。

### 5.2 可用性上报

这里用「clapi 主动检查」，而不是让节点自己知道有哪些存储池：

- **触发时机**：clapi 后台任务每 5 分钟执行一轮；存储池创建或修改时立即执行一轮；节点状态从离线变成在线时，对这个节点执行一轮。
- **下发**：`toall=` 广播 `check_storage_pools.sh`，把全部 GPFS 池的 `[{uuid, mount_path, fs_type}]` 放在 stdin 里。
- **节点检查**：对每个池执行 `pool_guard`，再在 `tmp/` 里建一个临时文件然后删掉，验证可写。
- **回调**：每个池一行 `storage_pool_status.sh '<NODE_ID>' '<池UUID>' 'ready|unavailable' '<原因>'`，clapi 写入 `hyper_storage_pools`。
- 两台 clapi（HA）会各跑一次检查，upsert 是幂等的，不会出问题。

### 5.3 存储池容量与准入

**采集**：clapi 后台每 5 分钟，用 `select=<该池 ready 节点组>` 把 `storage_capacity_gpfs.sh` 派给其中一个节点：

- 用 `/usr/lpp/mmfs/bin/mmlsquota -j <fileset> <fs> -Y` 读取配额和用量。输出是机器可读格式，具体字段以实际版本为准。
- fileset 没设配额时，退回用 `df -B1 <mount_path>`，这时拿到的是整个文件系统的容量。
- 回调写入 `capacity_bytes / used_bytes / capacity_at`。
- 注意 `mm*` 命令默认不在 PATH 里，脚本要写全路径。

**准入**：创建卷、创建虚拟机（系统盘）、扩容时，clapi 检查两项：

- **已分配**：池内所有卷的 `size` 之和，加上本次新增的量，不超过 `capacity × over_ratio`。
- **实际用量**：`used / capacity` 低于 90%。

容量未知（`capacity_bytes=0`）时只记告警，不拦截。

**写满时的表现**：fileset 配额写满后，写操作返回 `EDQUOT`，而 QEMU 默认只在 `ENOSPC` 时暂停虚拟机。所以 GPFS 盘的 `<driver>` 要加 `error_policy='stop'`，写出错时暂停虚拟机，而不是把 I/O 错误交给客户机文件系统（客户机可能因此把文件系统改成只读）。这一点需要验证（§16）。

### 5.4 节点磁盘统计不能把 GPFS 算进去

`report_rc.sh:257` 用 `sudo du -s $mount_point` 统计 `$image_dir` 所在文件系统的用量，没加 `-x`。如果 GPFS 挂在同一个根文件系统下面（比如 `$image_dir` 在 `/`，GPFS 挂在 `/gpfs`），**每次心跳（1–20 秒一次）都会遍历整个 GPFS 命名空间**，节点磁盘统计失真，还会给 GPFS 带来持续的元数据压力。

阶段 0 必须改成 `du -x`。GPFS 的挂载点本来就不在 `$image_dir` 下，第 247 行 `du -s $image_dir` 不受影响。

---

## 6. 各操作流程

以下「下发」指 clapi 通过 `HyperExecute` 下发命令。「池节点组」指 `select=group-pool-<id>:<该池 ready 的节点 hostid>`，不带资源要求，由 cland 轮询挑选。

### 6.1 创建数据卷

| | local（现状保留） | gpfs |
|---|---|---|
| 建记录 | `path=volume/volume-<id>.disk`，`available`，**不落盘** | `path=volumes/volume-<id>.disk`，`pending` |
| 下发 | 无（首次挂载时在虚拟机所在节点创建） | 派给池节点组：`create_volume_gpfs.sh '<id>' '<绝对路径>' '<GB>' '<池UUID>'` |
| 节点 | — | `pool_guard`，然后 `qemu-img create -f qcow2 -o cluster_size=2M`（与本地数据卷相同） |
| 回调 | — | `create_volume_gpfs '<id>' 'available\|error' '<原因>'` |

GPFS 卷创建时就落盘，不像本地卷那样等到首次挂载：它不依赖任何节点，提前落盘还能尽早发现池不可用或空间不足的问题。

### 6.2 挂载与卸载

- **挂载前的校验**：
  - 本地卷：沿用现在的规则，已落盘的卷只能挂给同一节点上的虚拟机。
  - GPFS 卷：虚拟机所在节点对这个池必须是 `ready`，否则返回 400，说明哪个存储池在哪个节点上不可用。
- **下发**：`inter=<虚拟机所在节点>`，`attach_volume_<驱动>.sh '<实例ID>' '<卷ID>' '<绝对路径>' '<卷UUID>' '<大小>' '<池UUID>'`。参数顺序以本地方案 §5.2 为准：保留现有的前四个参数，大小与池 UUID 追加在后面。
- **脚本**：
  - `attach_volume_local.sh` 把 `vol_path=$volume_dir/$3` 改成直接使用传入的绝对路径。
  - `attach_volume_gpfs.sh` 与本地版共用 `cloudrc` 里的通用函数，额外先做 `pool_guard`；磁盘 XML 的 `<driver>` 用 `cache='none' error_policy='stop'`。
- **回调 `attach_volume`**（`rpcs/attach_volume.go:78-83`）：只有本地卷才写 `hyper`。
- **卸载**：`detach_volume_*.sh` 本来就不依赖路径（用的是保存下来的磁盘 XML），两种驱动行为一致。

### 6.3 扩容

- **已挂载**：两种驱动相同，都是 `inter=<虚拟机所在节点>`：
  - 虚拟机开着（运行、暂停、崩溃）时用 `virsh blockresize <域> <绝对路径> <N>G`。
  - 关机时用 `qemu-img resize`。
  - 这就是现在 `resize_volume_local.sh` 的逻辑，只是路径改为下发的值。
- **未挂载**：
  - local：沿用现在的做法，派给 `volume.Hyper`；还没落盘的只改记录。
  - gpfs：派给池节点组，用 `qemu-img resize`。
- 扩容前做容量准入（§5.3）。
- 失败时按实际容量回滚（`rpcs/resize_volume.go` 现有逻辑），两种驱动都适用。

### 6.4 删除卷

- **local**：沿用现在的做法，`inter=<volume.Hyper>`，还没落盘的只删记录。
- **gpfs**：派给池节点组，`clear_volume_gpfs.sh '<id>' '<绝对路径>' '<池UUID>'`：
  - 先 `pool_guard`，再 `rm -f`。
  - 只允许删除 `volumes/` 目录下、文件名与卷 ID 对应的文件，防止误删。
- 删除前的状态校验不变：必须是 `available`，不能处在忙碌状态，也不能在一致性组里。

### 6.5 创建虚拟机（系统盘在 GPFS 上）

**clapi**（`services/instance.go` 的 `Create`）：

1. 确定系统盘所在的存储池：用请求里的 `storage_pool`，没给就用默认池。池必须是 `active`，并通过容量准入。
2. 候选节点：`GetHyperGroup` 增加存储池过滤，取可用区内的活动节点和该池 ready 节点的交集。如果管理员指定了节点，该节点必须在交集里。
3. 系统盘记录：`storage_pool_id`，`path=volumes/volume-<卷ID>.disk`，状态 `pending`。
4. 查找 `image_storages(镜像, 池)` 记录，没有就创建（状态 `syncing`），并把基础文件路径和是否已就绪写进元数据。
5. `rcNeeded` 里的 `disk`：GPFS 系统盘不占节点磁盘，填 0；本地系统盘照旧（单位见附录 B 第 4 条）。
6. 元数据（stdin 里的 JSON）新增 `boot_disk` 字段，不再追加位置参数：

```json
"boot_disk": {
  "volume_id": 12,
  "driver": "gpfs",
  "pool": "<pool uuid>",
  "pool_root": "/gpfs/fs1/cl_ssd",
  "fs_type": "gpfs",
  "path": "/gpfs/fs1/cl_ssd/volumes/volume-12.disk",
  "size_gb": 40,
  "image_base": "/gpfs/fs1/cl_ssd/images/image-3-ab12cd34.qcow2",
  "image_storage_id": 7,
  "clone_mode": "mmclone",
  "nvram": "/gpfs/fs1/cl_ssd/nvram/inst-9_VARS.fd"
}
```

本地系统盘同样用这个结构：`driver=local`，`path=/opt/cloudland/cache/instance/inst-9.disk`，没有 `image_base`，`nvram` 在 `$image_dir` 下。这样 `launch_vm.sh` 里不再有按驱动推算路径的逻辑。

**节点**（`launch_vm.sh` 的 gpfs 分支）：

1. `pool_guard`。失败就回调 `create_volume_gpfs '<卷ID>' 'error' 'storage pool not available'`，实例进入 `error` 状态。
2. 如果基础文件不存在，按 §6.6 导入，完成后回调 `image_storage_status '<id>' 'synced'`。
3. 生成系统盘：
   - `clone_mode=mmclone`：`mmclone copy <基础文件> <系统盘路径>`，失败时自动退回整盘复制。
   - `clone_mode=copy`：`qemu-img convert -O qcow2 <基础文件> <系统盘路径>`。
4. 校验镜像的虚拟大小不超过规格，再 `qemu-img resize` 到规格大小。这一步与本地相同。
5. UEFI：NVRAM 从模板复制到 `boot_disk.nvram`。放在池里，迁移和节点宕机恢复后都还在。
6. 域定义 XML：系统盘的 `<driver>` 改用 `cache='none' error_policy='stop'`（本地系统盘保持现在的 `writeback`）。
7. 回调 `create_volume_gpfs '<卷ID>' 'attached' 'success'`。clapi 记录 `base_image_storage_id`（整盘复制时为 0）。
8. 第 3 步之后任何一步失败，都删掉刚生成的系统盘文件（路径唯一，不会误删别的文件）。

### 6.6 镜像基础文件（`image_storages`）

**导入**：第一次在某个池里用某个镜像创建虚拟机时导入（懒导入），并以原子方式发布，保证多个节点同时导入也不会出错：

```bash
# on the launching node, pool_guard already passed
tmp=$pool_root/tmp/image-$img-$(hostname)-$$.qcow2
ensure_image_cached "$img_name" "$url"                          # node-local cache, as today
qemu-img convert -O qcow2 "$image_cache/$img_name" "$tmp"
[ "$clone_mode" = mmclone ] && /usr/lpp/mmfs/bin/mmclone snap "$tmp"   # tmp becomes a read-only clone parent
ln "$tmp" "$base" 2>/dev/null   # atomic publish; fails if another node won the race
rm -f "$tmp"                    # winner drops the extra name, loser drops its copy
```

- 依靠 `ln` 的原子性：同一个路径只会有一个节点发布成功，输的一方用赢家发布的文件。
- 最坏情况是几个节点重复转换一次，浪费一些 I/O，但结果正确。这样不依赖 GPFS 上的 `flock` 是否在整个集群范围内生效。
- 对克隆父文件建硬链接能否正常工作需要实测（§16）。如果不行，就改为由 clapi 串行导入：`image_storages` 先置 `syncing`，派给一个节点导入，等回调 `synced` 以后再下发创建虚拟机的命令。
- 可选的预热：`PATCH /images/:id` 带上 `storage_pools: [...]`，clapi 把 `import_image_gpfs.sh` 派给池节点组，提前导入。这个接口原来用于 WDS 的池同步（`services/image.go:698-744`），改为这里的用途。

**删除**：

- 删除镜像时，对每个 GPFS 池里的基础文件，统计引用数：`volumes.base_image_storage_id` 等于它、且没有删除的卷有多少个。
  - 引用数为 0：派发 `clear_image_gpfs.sh` 删除基础文件，然后删掉这条记录。
  - 引用数不为 0：记录置 `deleting`。之后删除虚拟机、或重装虚拟机（换了基础文件）时再检查一次，引用归零就清理。
- 如果 GPFS 不允许删除还有子克隆的父文件，这个引用计数正好满足它的要求；如果允许，引用计数也能避免给还在用的父文件留下悬空状态。实际行为见 §16。

### 6.7 重装、救援、捕获镜像、调整规格

- **重装**（`reinstall_vm.sh`）：
  - 取消域定义之后，GPFS 系统盘先删掉旧文件，再按 §6.5 第 2 到 4 步从新镜像的基础文件克隆到同一路径。
  - clapi 更新 `base_image_storage_id`，并检查旧基础文件的引用数。
  - 本地系统盘照旧，只是改用下发的路径。
- **救援**（`rescue_vm.sh` / `end_rescue.sh`）：
  - 救援盘总是建在本地，救援虚拟机和原虚拟机在同一个节点上。
  - 原系统盘作为 `vdb` 挂进救援虚拟机，使用下发的绝对路径。这同时修复一个现有缺陷：本地模式下 `VOLUME_SOURCE` 从来没有被替换（附录 B 第 1 条）。
  - 重新挂载数据盘时，不再按 `wds_address` 判断用 `file=` 还是 `path=`，直接复用每块盘自己的 XML。
- **捕获镜像**（`async_job/capture_image.sh`）：
  - 源磁盘改用下发的系统盘绝对路径（现在写死了 `$cache_dir/instance/inst-N.disk`）。
  - 临时文件放到 `$cache_tmp_dir`，不要放在 `$image_dir`，否则会被 `report_rc.sh` 计入节点磁盘用量。
- **调整规格**（`resize_vm.sh`）：只改 CPU 和内存，不涉及磁盘，不用改。

### 6.8 删除虚拟机

- `clear_vm.sh` 改为从 stdin 接收磁盘清单（驱动、路径）：
  - 只删除**本地**系统盘和本地 NVRAM。
  - GPFS 盘一律不碰。
  - 现在第 53-54 行按 `inst-N.*` 通配符删除的写法要去掉。
- clapi 收到 `clear_vm` 回调，确认域已经销毁之后，再把 `clear_volume_gpfs.sh` 派给池节点组，删除 GPFS 系统盘，NVRAM 在池里时一并删除。
- **顺序很重要**：必须先确认虚拟机已销毁，再删共享盘。如果虚拟机所在节点离线，`clear_vm` 没法执行，GPFS 系统盘就先保留，直到节点恢复并清理完，或者按 §8 确认隔离。
- 如果后续那条删除命令失败，文件就会残留。阶段 4 做一个孤儿文件对账：定期列出池内的 `volumes/*.disk`，和数据库对比，只报告差异，不自动删除。

### 6.9 节点开机

- `report_rc.sh` 里的 `sync_instance` 在 `virsh start` 之前，要等待这台虚拟机所有 `<source file>` 指向池路径的磁盘都可以访问，也就是 GPFS 已经挂载好（最多等 5 分钟）。超时就不启动这台虚拟机，并上报错误。这和现在 WDS 等待卷设备就绪（第 212-218 行）是同一类处理。
- 阶段 3 还要进一步改掉「开机就把所有虚拟机拉起来」这个行为，见 §8.4。

---

## 7. 迁移

### 7.1 按磁盘组成决定迁移方式

| 磁盘组成 | 源节点在线，虚拟机运行中 | 源节点在线，虚拟机已关机 | 源节点离线（`force`） |
|---|---|---|---|
| 全部本地 | 现状：`--live --copy-storage-all`，复制全部磁盘 | 现状：先 scp 磁盘，再 `--offline` | 不支持（现状） |
| 本地与 GPFS 混合 | `--live --copy-storage-all --migrate-disks` **只列本地盘** | 只 scp 本地盘，再 `--offline` | 不支持 |
| 全部 GPFS | `--live`，不复制磁盘 | `--offline`，只转移域定义 | **阶段 3 起支持**，走 §8 的流程 |

各种组成都适用的规则：

- **目标节点**：对这台虚拟机用到的所有 GPFS 池都必须是 ready。自动选择目标时，候选节点按这个条件过滤；手动指定目标时，同样要校验。
- **调度时的磁盘需求**：按本地盘的总大小计算（系统盘和本地数据盘）。现在只算了系统盘，GPFS 盘不计入。
- **NVRAM**：在池里的不用复制；在本地的照旧 scp。
- **配置光盘（ISO）**：照旧在目标节点重新生成。

### 7.2 脚本改动

- **`source_migration.sh`**：
  - 磁盘清单改用 clapi 下发的卷 JSON，每块盘带上驱动和路径（`rpcs/migrate_vm.go:45-60` 增加这两个字段），不再从 `virsh domblklist` 里挑出所有文件型磁盘。
  - 只对本地盘做「目标上已存在同名文件就中止」的检查，以及预建空盘或 scp。**共享盘在目标节点上本来就存在，不排除的话，现在的逻辑每次都会中止迁移。**
  - 没有本地盘时，去掉 `--copy-storage-all`。
- **`finish_source_migration.sh`**：
  - 只删除清单里的本地盘（现在按 `$volume_dir/*`、`$image_dir/*` 前缀删）。
  - 「源节点上还定义着这台虚拟机就跳过清理」这条保护，改为对所有驱动都生效（现在只对 local 生效）。
- **`async_job/clear_target_migration.sh`**：只删除清单里的本地盘；池里的 NVRAM 不删。
- **`target_migration.sh`**：
  - 全部磁盘都在 GPFS 上时，不再返回 `not_supported 'cold migration requires shared storage'`。
  - 阶段 2 只支持源节点在线时的冷迁移（由源节点执行 `--offline`）；源节点离线的情况在阶段 3 处理。
- **`async_job/complete_migration.sh`**：刷新域定义 XML 的步骤，从「只对 local 做」改为对所有文件型驱动都做。

### 7.3 clapi 改动

- **`services/migration.go:58-76`**：把全局的 local 判断改成按每台虚拟机判断：
  - 有任何本地盘：不允许 `force`，源节点必须在线（与现在相同）。
  - 全部是 GPFS 盘：阶段 3 起允许 `force`。
- **`rpcs/migrate_vm.go:306-311`**：迁移完成时，只更新**本地池**卷的 `hyper`。现在会更新这台虚拟机的所有卷。
- **`services/hyper.go:525-541`**：维护模式批量迁移的逻辑不变，自动适用以上规则。

---

## 8. 节点宕机后的恢复（阶段 3）

### 8.1 前提

同时满足以下条件，才能把一台虚拟机从宕机节点恢复到别的节点：

- 这台虚拟机的**全部磁盘**都在共享存储池上。有本地盘的虚拟机不能恢复，因为本地盘随节点一起不可用。
- 源节点状态是离线（`status=10`），并且已经超过宽限期（建议 5 分钟，长于 cland 的 90 秒离线宽限）。
- **已确认隔离**（§8.2）。

### 8.2 隔离（fencing）

**风险**：cland 判定某个节点离线，可能只是管理网络断了。节点本身还活着，GPFS 的存储网络也正常，虚拟机还在往磁盘写数据。这时在别的节点再启动一份，就会有两个进程同时写同一块磁盘，数据会损坏。

GPFS 自带隔离机制：失联的节点会被集群驱逐，磁盘租约过期后，它就不能再写这个文件系统，要重新加入集群并重新挂载才行。据此：

- **自动判定**：把 `check_node_fenced.sh '<主机名>' '<fs>'` 派给一个池节点，检查源节点是否已经不在 `mmlsmount <fs> -L` 的挂载节点列表里。`mmgetstate -N <节点> -Y` 作为辅助参考。具体用哪个判据、GPFS 驱逐节点要多久，需要实测（§16）。
- **人工确认**：自动判定没通过时，管理员可以在确认节点已经断电之后（例如通过 SoftLayer 或 IPMI 关机），带上 `confirm_fenced=true` 强制执行。这个操作要记入审计。
- 如果测试环境用 NFS 模拟共享存储，NFS 没有这种隔离机制，只能人工确认。

### 8.3 流程

1. 管理员调用 `POST /hypers/:uuid/evacuate`（可以指定 `target_hyper` 和 `confirm_fenced`），或者对单台虚拟机调用 `POST /migrations {force: true}`。
2. clapi 检查 §8.1 的前提，为每台虚拟机建一条迁移记录，类型为 `evacuate`。
3. 选择目标节点：规则同 §7.1。
4. 目标节点以「使用已有磁盘」的方式执行 `launch_vm.sh`：
   - `boot_disk.existing=true`，跳过克隆和调整大小。
   - 按元数据里的 `volumes[]`（带路径）重新挂载数据盘。
   - NVRAM 用池里的那份。
5. 后续处理复用冷迁移的回调链：`LaunchVM` 以 `sync` 方式更新 `instances.hyper` 和网卡，同步浮动 IP，预写 VXLAN 转发条目（`prewarmTargetFdb`）。
6. 源节点那边的清理，推迟到它恢复时进行（§8.4）。

### 8.4 原节点恢复：阶段 3 的前提，必须先改

**现在的行为**：

- 节点开机后，`sync_instance`（`report_rc.sh:190-223`）会把 `$xml_dir` 下的**每一台**虚拟机都 `virsh start` 起来，并以本节点的身份回调 `launch_vm.sh '<id>' 'running' '<NODE_ID>' 'sync'`。
- clapi 处理 `sync` 时会把 `instances.hyper` 改写成这个节点；`inst_status` 也会按节点的上报修正 `hyper`。

**后果**：已经恢复到别处的虚拟机，在原节点上又被启动一份（两个进程同时写同一块磁盘）；数据库里它的归属也被抢回到原节点。

**要改成**：

1. `sync_instance` 不再直接启动虚拟机，而是上报本机上定义的所有域：`node_recovered.sh '<NODE_ID>' '<boot_id>' '<实例ID 列表>'`。
2. clapi 逐台对账：
   - 数据库里归属本节点、并且没有删除的：下发启动，之后照旧 `sync`。
   - 其他的：下发 `clear_stale_vm.sh '<实例ID>'`，取消域定义，删除 XML、ISO 和本地 NVRAM，**不碰任何共享盘**，也不修改数据库。
3. 处理 `inst_status` 和 `launch_vm sync` 回调时增加保护：如果实例有一条已完成的 `evacuate` 记录，而上报的正好是它的源节点，就不改写 `hyper`，只记告警，并触发第 2 步的清理。
4. 多加一层保护：如果实测证明 QEMU 的镜像锁在整个 GPFS 集群范围内都有效（§16），第二个进程打开磁盘时会直接失败。但这只能作为补充，不能替代上面的对账。

**这一节必须先实现并测试通过，才能开放节点宕机恢复功能。**

---

## 9. 一致性与安全

### 9.1 同一块盘只能有一个写入者

三层保护，由强到弱：

1. **clapi 的状态机**：卷只有处于 `available` 时才能挂载；一台虚拟机在任一时刻只归属一个节点；迁移和恢复都有明确的状态流转。
2. **QEMU 镜像锁**：QEMU 默认会对打开的镜像文件加 OFD 锁。GPFS 支持整个集群范围的 POSIX 字节范围锁，如果 OFD 锁也被集群范围执行，那么另一个节点再打开同一块盘就会失败。需要实测（§16）。
3. **可选：libvirt virtlockd**（`lock_manager = "lockd"`）。如果第 2 层实测无效，就启用它，把锁空间放在 GPFS 上。

### 9.2 缓存模式与出错策略

- GPFS 盘统一使用 `cache='none'`。libvirt 对共享存储上的磁盘做热迁移时，如果缓存模式不安全会拒绝迁移（除非加 `--unsafe`）。
- libvirt 的源码里把 GPFS 列为共享文件系统（`virfile.c`），但需要在节点现有的 libvirt 12.0 上实测确认。
- 出错策略用 `error_policy='stop'`（§5.3）。

### 9.3 删除共享盘的规则

- 只删除 clapi 明确点名的文件，不用通配符，也不按路径前缀删。
- 必须先确认虚拟机已经销毁（`clear_vm` 回调、迁移已完成，或者 §8.2 的隔离已确认）。
- 迁移的清理脚本、`clear_vm.sh`、`end_rescue.sh` 都只处理本地文件。

### 9.4 其他

- **权限**：libvirt 的动态属主（`dynamic_ownership`）会在虚拟机启动时把磁盘文件改成 `libvirt-qemu` 所有，关机后恢复。在共享文件系统上，它的行为和迁移时的属主处理都需要实测。
- **AppArmor**：Ubuntu 上 libvirt 会为每台虚拟机动态生成 AppArmor 规则，把磁盘路径加进去，GPFS 路径理论上可以正常使用，也需要实测。
- **`mm*` 命令**：需要 root 权限，并且不在 PATH 里。节点脚本经 cloudlet 以 `sudo -E bash` 执行，满足 root 条件，但脚本里要写全路径 `/usr/lpp/mmfs/bin/`。

---

## 10. 接口与前端

### 10.1 clapi REST 接口

| 接口 | 权限 | 说明 |
|---|---|---|
| `GET /storage_pools` | 所有成员 | 列出 `active` 的存储池。普通成员只看到 `id / name / driver / shared / is_default`；系统管理员看到全部字段，包括容量和已分配量 |
| `POST /storage_pools` | 系统管理员 | `{name, driver:"gpfs", mount_path, fs_name, fileset, over_ratio, clone_mode, is_default, description}`，详细规则见下方 |
| `PATCH /storage_pools/:id` | 系统管理员 | 可以修改 `name / status / is_default / over_ratio / clone_mode / description` |
| `DELETE /storage_pools/:id` | 系统管理员 | 池里没有卷、也没有镜像基础文件时才能删除。内置本地池不能删除 |
| `GET /storage_pools/:id/hypers` | 系统管理员 | 各节点对这个池的可用性和原因 |
| `POST /volumes` | 写权限 | 新增 `storage_pool: {id}`，不传就用默认池。删除原来的 `pool_id` |
| `POST /instances` | 写权限 | 新增 `storage_pool: {id}`，表示系统盘所在的池。删除原来的 `pool_id` |
| `PATCH /images/:id` | 系统管理员 | `storage_pools: [...]`，预热镜像基础文件（§6.6） |
| `POST /hypers/:uuid/evacuate` | 系统管理员 | 阶段 3，§8.3 |

`POST /storage_pools` 的规则：

- `mount_path` 必须是绝对路径，不能包含 `..`，不能位于 `/opt/cloudland` 下面，也不能和已有的池重复。
- 创建后，clapi 从可用区内任选一个在线节点，派发 `init_storage_pool_gpfs.sh`。节点确认路径所在的文件系统类型是 `gpfs`，并且目录为空（或者已经有 UUID 相同的标记文件）之后，建好子目录，写入 `.cloudland-pool` 标记文件。
- 然后执行一轮可用性检查（§5.2）。

返回结构的变化：

- `VolumeResponse` 增加 `storage_pool {id, name, driver, shared}`。已落盘的本地卷再增加 `hypervisor`，可见性规则与 `InstanceResponse.hypervisor` 相同。
- `InstanceResponse.volumes[]` 增加存储池名称。
- `GET /images/:id/storages` 返回镜像在各个池里的状态（这个接口已经存在，前端还没用过）。

### 10.2 cpgateway

- `proxy_routes.go` 的白名单加入以上路由：`GET /storage_pools` 所有成员可用，其余标记为系统管理员。
- 配额保持一个 `disk_gb`，本地盘和 GPFS 盘都计入。现在的记账和对账逻辑不用改。

### 10.3 前端

- **管理员的「存储池」页面**（放在 Administration 下）：
  - 列表：名称、类型标签、挂载路径、状态、容量条（已用、已分配、总量）、就绪节点数（x/y）。
  - 详情：各节点的可用性表格。
  - 创建和编辑弹窗。
- **创建云硬盘**：增加存储池下拉框，默认选中默认池；每个选项显示「本地 / 共享」标签和剩余容量。
- **创建虚拟机**：增加「系统盘存储池」下拉框。
- **云硬盘列表**：增加「存储池」列。**详情页**：显示存储池、类型、所在节点（仅本地卷）。
- **挂载弹窗**：
  - 已落盘的本地卷，只列出同一节点上的虚拟机。
  - GPFS 卷，把所在节点对这个池不可用的虚拟机置灰，并说明原因。
  - 现在那段固定的提示文字（`attachHint`）改成按卷的类型显示。
- **迁移弹窗**：列出每块盘是「复制」还是「共享，不复制」。只有全部是共享盘时才显示「源节点宕机恢复」相关选项（阶段 3）。
- 新增文案要在三种语言里都加上，并通过 `npm run i18n:check`。

### 10.4 审计

`audit_actions.go` 增加以下动作：`storage_pool.create`、`storage_pool.update`、`storage_pool.delete`、`hyper.evacuate`（带上 `confirm_fenced` 的值）。

---

## 11. 部署与配置

### 11.1 计算节点

- 安装 GPFS 客户端、加入集群、配置自动挂载，并且**所有节点使用相同的挂载路径**。这些都由存储管理员完成，CloudLand 的部署脚本不负责（GPFS 是需要许可证的软件包）。
- **内核版本要固定**：GPFS 的内核模块与内核版本强绑定，升级内核后要重新执行 `mmbuildgpl`。
- **操作系统支持要先确认**：work-01、work-02、work-03 目前是 Ubuntu 26.04，需要先查 Storage Scale 的支持矩阵（§16）。
- 节点开机时，要等 GPFS 挂载好再启动相关虚拟机，这由 §6.9 的等待逻辑保证，不依赖 systemd 的启动顺序。
- `deploy-compute-node.sh` 不用为 GPFS 改动。节点是否能用某个池，由 §5.2 的检查自动发现。

### 11.2 控制面

- clapi 不需要挂载 GPFS，所有 GPFS 操作都派到计算节点执行。
- 从配置模板和文档里删掉 `[volume]` 配置段。现在的部署方式本来也没有写这一段。

### 11.3 不要用旧文档里的做法

`gpfs-deployment-plan.md` §4.2 让你在 `cloudrc.local` 里覆盖 `volume_dir`、`image_dir`、`image_cache` 三个变量。**这样做不会生效**：`cloudrc` 在第 3 行先加载 `cloudrc.local`，第 11-14 行又把这三个变量赋成默认值，覆盖掉了。而且即使生效，也等于把整个节点的本地存储都搬到 GPFS 上，与本设计「按卷选择」的目标相反。

---

## 12. 扩展到 Ceph

本文的抽象对 Ceph RBD 同样适用，按卷的驱动、clapi 下发的磁盘清单、节点可用性、容量准入、迁移矩阵、宕机恢复流程都不用改。需要新写的只有这个驱动自己的实现：

| 方面 | Ceph RBD 的做法 |
|---|---|
| 磁盘 XML | `type='network'`，`<source protocol='rbd'>`，cephx 认证用 libvirt secret |
| 存储池 | 对应一个 Ceph pool，`path` 存 RBD 镜像名 |
| 镜像基础文件 | RBD 镜像 + 受保护的快照 |
| 系统盘 | `rbd clone` |
| 容量 | `ceph df` |
| 隔离 | `ceph osd blocklist add <client>` |
| 挂载保护 | 不需要（不是文件系统） |

---

## 13. 与旧文档的关系

- **`storage-gpfs-ceph-integration-plan.md`**：
  - 那份文档的方案是整个节点或区域切换一个 `storage_backend`，并建议初期只用单一后端，**被本文取代**。
  - 它的 Ceph 脚本清单和 XML 示例，在做 Ceph 时仍可参考。
- **`gpfs-deployment-plan.md`**：
  - 以下内容已经过时：§4.2 的覆盖方式（见 §11.3）；§5「local 完全不支持迁移」（本地存储热迁移 2026-09-15 起已经支持）；文中关于 SCI 的描述（已被 cland-go / cloudlet-go 取代）。
  - §3 GPFS 集群安装和 §8 运维命令仍可参考，但要重新核对操作系统版本（现在是 Ubuntu 26.04）和 Developer Edition 的许可条款。
  - §7 监控里 textfile exporter 的思路可以沿用。

---

## 14. 实施阶段

### 阶段 0：存储池抽象（只有 local，行为不变）

> **执行顺序调整**：本地多盘方案（`local-multi-disk-storage-plan.md`）先实施。它的阶段 L0a（数据盘部分）与 L0b（系统盘、救援、重装、捕获镜像、WDS 清理）合起来完成下面范围里除 GPFS 专用部分以外的全部内容；附录 B 第 3 条的 `du -x` 在 L0a 做，第 4 条（磁盘需求单位）因为会改变调度结果，在本地方案里也列为单独评估，不随阶段 0 顺带修。它的 L1 还会建好 `hyper_storage_pools`、存储池接口与 `pool_guard`，并给 `storage_pools` 增加 `Media`、`FallbackGroup` 字段。执行本文时，阶段 0 只需补上 GPFS 专用的字段（`FsName`、`Fileset`、`FsType`、`CloneMode`，以及 `volumes.BaseImageStorageID`）和 `image_storages` 的改造，再从阶段 1 开始。

**范围**：

- `storage_pools`（内置本地池）；`volumes.storage_pool_id`；如实记录路径（系统盘为 `instance/inst-N.disk`）；卷响应里增加 `storage_pool`。
- clapi 约 16 处 `GetVolumeDriver()` 改为按卷判断；命令里下发绝对路径。
- 系统盘相关的元数据增加 `boot_disk`；迁移、删除、救援用的卷 JSON 带上驱动和路径。
- 脚本里约 20 处 `wds_address` 分支改为按单块盘判断（放弃 WDS 的话直接删掉），去掉 `$volume_dir/inst-N.disk` 回退。
- 顺带修复附录 B 第 1 到 7 条。

**验收**：

- `test-items/` 回归全部通过：创建、挂载、扩容、卸载、删除、热迁移（带数据盘）、关机迁移、重装、救援（要能访问原盘）、捕获镜像、删除虚拟机。
- 数据库里每个卷的路径都与节点上的实际文件一致。

### 阶段 1：GPFS 存储池与数据卷

**范围**：

- 存储池的增删改查和初始化；挂载保护；可用性上报；容量采集和准入。
- `*_volume_gpfs.sh` 各脚本；挂载时的节点校验。
- 混合迁移：本地系统盘加 GPFS 数据盘，GPFS 盘不复制。
- 管理员的存储池页面；创建卷时选择存储池；列表和详情显示存储池。

**验收**：

- GPFS 卷先挂到 A 节点上的虚拟机写入数据，卸载后挂到 B 节点上的虚拟机，数据一致。
- 在线扩容和离线扩容都正常。
- 某个节点卸载 GPFS 后，该节点在 5 分钟内变为不可用；往这个节点上的虚拟机挂载卷会被拒绝。
- 删除标记文件后，任何写操作都失败，并且根文件系统上没有产生任何文件。
- 混合迁移后数据完整，GPFS 盘没有被复制（看迁移耗时和 `domjobinfo` 的数据量）。

### 阶段 2：GPFS 系统盘与共享迁移

**范围**：

- 镜像基础文件的导入、发布和引用计数；`mmclone` 克隆和整盘复制两种方式；NVRAM 放进池里。
- `cache='none'` 与 `error_policy='stop'`。
- 创建、重装、救援、捕获、删除虚拟机都支持 GPFS 系统盘。
- 全部为 GPFS 盘的虚拟机：热迁移不复制磁盘，冷迁移只转移定义。
- 创建虚拟机时选择系统盘存储池。

**验收**：

- 三个节点并发创建 10 台虚拟机，使用同一个在这个池里还没导入过的镜像：只发布一个基础文件，10 台都正常启动。
- `mmclone` 方式下，系统盘创建时间与镜像大小无关。
- 热迁移时间与磁盘大小无关，NVRAM 保持不变。
- 删除镜像时如果还有虚拟机在用，基础文件保留，最后一台虚拟机删除后被清理。

### 阶段 3：节点宕机后恢复

**范围**：

- 先做 §8.4：节点开机后与 clapi 对账，并给 `inst_status` 和 `sync` 回调加保护。
- 再做隔离判定和 `POST /hypers/:uuid/evacuate`。
- 前端的恢复入口。

**验收**：

- 断开一个节点的电源：确认隔离后，它上面的虚拟机在其他节点上启动，数据完整。
- 节点恢复后，不会再启动已经恢复到别处的虚拟机，虚拟机在数据库里的归属不变，残留的域定义被清理掉。
- 只断管理网络（存储网络正常）时，自动判定不通过，恢复被拒绝。

### 阶段 4：运维完善（可选）

- 孤儿文件对账（§6.8）；存储池容量告警和节点可用性告警。
- 按存储池分别计配额；存储池之间离线搬迁卷。

---

## 15. 测试方案

### 15.1 环境

- **真实 GPFS**：`mmclone`、fileset 配额、隔离、集群范围的锁和性能，都只能在真实的 GPFS 上验证。
  - 可以用 Storage Scale Developer Edition（免费，有容量上限，以 IBM 当前条款为准）。
  - 需要三个节点和用作 NSD 的空闲磁盘。
  - 如果 Storage Scale 不支持 Ubuntu 26.04，要另外准备 24.04 的节点。
- **用 NFS 验证通用流程**：阶段 0 到 2 的大部分流程都不依赖 GPFS 特有的功能，可以先在 NFS 上跑：池参数设 `fs_type=nfs`、`clone_mode=copy`，容量走 `df`。这样在拿到 GPFS 环境之前就能推进开发。**NFS 上的结果不能作为隔离和锁方面的结论。**
- **在线上环境做前端测试时**，照例在测试脚本里兜底拦截所有写请求（见 CLAUDE.md 的相关约定）。

### 15.2 性能基准

在同一个节点上，对比本地 qcow2、GPFS qcow2、GPFS raw 三种情况：

- fio：4k 随机读写（队列深度 1 和 32）、1M 顺序读写。
- 虚拟机从创建到能 SSH 登录的时间：`mmclone` 与整盘复制对比，镜像大小分别为 2GB 和 20GB。
- 热迁移耗时：本地复制与 GPFS 共享对比，磁盘分别为 20GB 和 200GB。

---

## 16. 风险与待验证项

| # | 事项 | 影响 | 验证方法 |
|---|---|---|---|
| 1 | Storage Scale 是否支持 Ubuntu 26.04 及其内核版本 | 会不会**卡住整个部署** | 查 IBM 支持矩阵；在测试节点上执行 `mmbuildgpl` |
| 2 | `mmclone`：父文件和克隆是否必须在同一个独立 fileset；对克隆父文件建硬链接能否发布；qcow2 克隆后能否 `qemu-img resize`；有子克隆时能否删除父文件 | 系统盘的实现方式（§6.6） | 在测试集群上手工验证 |
| 3 | QEMU 的 OFD 镜像锁在 GPFS 上是否集群范围有效 | 同一块盘只有一个写入者的第二层保护（§9.1） | 在 A、B 两个节点上启动使用同一块盘的虚拟机，看第二个能否打开 |
| 4 | libvirt 12.0 能否识别 GPFS 为共享文件系统；`cache='none'` 时热迁移是否报 unsafe | 共享迁移（§7） | 在测试环境里执行 `virsh migrate --live` |
| 5 | 动态属主和 AppArmor 在 GPFS 路径上是否正常 | 虚拟机能否启动 | 启动、迁移、关机后检查文件属主 |
| 6 | GPFS 驱逐节点所需的时间、`mmlsmount -L` 和 `mmgetstate` 的表现 | 隔离判定是否可靠（§8.2） | 分别做断电、只断管理网、只断存储网三种测试 |
| 7 | 配额写满时 `error_policy='stop'` 能否暂停虚拟机 | 写满时的表现（§5.3） | 把 fileset 配额设得很小，然后在虚拟机里写满 |
| 8 | qcow2 在 GPFS 上的性能；GPFS 块大小与 qcow2 簇大小（2M）如何搭配 | 用户体验 | §15.2 |
| 9 | GPFS 的许可证费用 | 能不能上线 | 商务评估 |
| 10 | WDS 的去留（C1） | 阶段 0 的范围 | 需要决策 |

---

## 附录 A：受影响的代码

### clapi（`api/src/`）

| 文件 | 改动 | 阶段 |
|---|---|---|
| `model/volume.go` | 增加 `StoragePoolID`、`BaseImageStorageID`；删除 `PoolID` 和路径解析函数；新增 `Driver()`、`AbsPath()`、`IsShared()` | 0 |
| `model/storage_pool.go`（新增） | `StoragePool`、`HyperStoragePool` | 0 / 1 |
| `model/image.go` | `ImageStorage` 改为用 `StoragePoolID` 和 `Path`；删除 `StorageType` | 0 / 2 |
| `services/volume.go` | Create、Update（挂载与卸载）、Delete、Resize、UpdateQos 改为按卷的驱动处理；容量准入；删除 `GetVolumeDriver` | 0 / 1 |
| `services/instance.go` | Create（选择系统盘存储池、按池过滤候选节点、`boot_disk`、`rcNeeded`）；Reinstall；Rescue；Delete（先 `clear_vm` 后删共享盘）；`GetMetadata` 的 `volumes[]` 带驱动和路径 | 0 / 2 |
| `services/storage.go` | 改写为存储池管理与镜像基础文件管理 | 1 / 2 |
| `services/image.go` | 镜像删除时的引用计数；`PATCH` 的预热；捕获镜像时下发系统盘路径 | 2 |
| `services/migration.go` | 按每台虚拟机的磁盘组成判断；过滤目标节点；磁盘需求只算本地盘 | 0 / 2 / 3 |
| `services/backup.go`、`services/consistency_group.go` | 放弃 WDS 的话随之删除，或者对非 WDS 卷直接拒绝 | 0 |
| `common/instance.go` | `GetHyperGroup` 增加存储池过滤 | 1 |
| `rpcs/create_volume.go` | 回调不再回传路径；新增 `create_volume_gpfs` | 0 / 1 |
| `rpcs/attach_volume.go` | 只有本地卷才写 `hyper` | 1 |
| `rpcs/migrate_vm.go` | 迁移完成时只更新本地卷的 `hyper`；卷 JSON 带驱动和路径 | 0 / 2 |
| `rpcs/clear_vm.go` | 之后删除共享系统盘 | 2 |
| `rpcs/capture_image.go` | 修复本地模式下的参数解析（附录 B 第 5 条） | 0 |
| `rpcs/launch_vm.go`、`rpcs/inst_status.go` | 宕机恢复后的归属保护 | 3 |
| `rpcs/` 新增 | `storage_pool_status`、`storage_capacity_gpfs`、`image_storage_status`、`node_recovered` | 1 / 2 / 3 |
| `apis/volume.go`、`apis/instance.go` | 请求中的 `storage_pool` 和响应字段 | 0 / 1 / 2 |
| `apis/storage_pool.go`（新增）、`apis/routes.go`、`apis/audit_actions.go` | 存储池接口与审计动作 | 1 |

### 节点脚本（`scripts/`）

| 文件 | 改动 | 阶段 |
|---|---|---|
| `cloudrc` | 通用文件盘函数；`pool_guard`；删除 WDS 辅助函数 | 0 / 1 |
| `kvm/launch_vm.sh` | 从 `boot_disk` 读取磁盘信息；gpfs 分支（克隆、NVRAM、缓存模式）；删除 `$volume_dir/inst-N.disk` 回退 | 0 / 2 |
| `kvm/reinstall_vm.sh`、`kvm/rescue_vm.sh`、`kvm/end_rescue.sh` | 使用下发的路径；救援时挂上原盘；`undefine --nvram` | 0 / 2 |
| `kvm/clear_vm.sh` | 按磁盘清单只删本地盘 | 0 |
| `kvm/source_migration.sh`、`kvm/target_migration.sh`、`kvm/finish_source_migration.sh`、`kvm/async_job/clear_target_migration.sh`、`kvm/async_job/complete_migration.sh` | 按单块盘处理（§7.2） | 0 / 2 |
| `kvm/async_job/capture_image.sh` | 源路径由 clapi 下发；临时文件放到 `$cache_tmp_dir` | 0 |
| `kvm/*_volume_local.sh` | 使用下发的绝对路径 | 0 |
| `kvm/*_volume_gpfs.sh`、`kvm/check_storage_pools.sh`、`kvm/storage_capacity_gpfs.sh`、`kvm/init_storage_pool_gpfs.sh`、`kvm/import_image_gpfs.sh`、`kvm/clear_image_gpfs.sh`（均为新增） | GPFS 驱动 | 1 / 2 |
| `kvm/report_rc.sh` | `du -x`；开机时等待磁盘可访问；开机对账 | 0 / 2 / 3 |
| `kvm/check_node_fenced.sh`、`kvm/clear_stale_vm.sh`（新增） | 宕机恢复 | 3 |
| `xml/template*_with_qa.xml`、`xml/volume.xml` | `<driver>` 的属性按驱动替换 | 2 |
| WDS 相关的 30 余个脚本、`xml/wds_*.xml` | 放弃 WDS 的话删除 | 0 |

### 其他

| 位置 | 改动 | 阶段 |
|---|---|---|
| `cpgateway/src/apis/proxy_routes.go` | 加入存储池路由 | 1 |
| `web/src/`：存储池页面、`VolumeList.vue`、`VolumeDetail.vue`、`VolumeActionModals.vue`、`CreateInstanceModal.vue`、迁移弹窗、`api/volumes.ts`、`api/instances.ts`、三种语言的语言包 | §10.3 | 1 / 2 / 3 |
| `deploy/`：`roles/wds`、`cloudrc.local` 模板里的 `wds_*`、`compute.env.example` | 放弃 WDS 的话删除 | 0 |

---

## 附录 B：调查中发现的现有缺陷

以下问题与本设计相关，建议在阶段 0 一并修复。除特别说明外，都是按代码核实的，没有在运行环境中复现。

| # | 位置 | 问题 |
|---|---|---|
| 1 | `scripts/kvm/rescue_vm.sh:159` | 本地模式下，原系统盘的 XML 只替换了 `VM_UNIX_SOCK/VOLUME_TARGET/VHOST_QUEUE_NUM`，**没有替换 `VOLUME_SOURCE`**，挂载的是字面路径 `VOLUME_SOURCE`，救援虚拟机因此访问不到原盘 |
| 2 | `scripts/kvm/clear_image.sh:15` | 变量名拼写错误（`$iname_name`），实际路径变成 `$image_cache/.<格式>`，本地镜像从来没被删除过 |
| 3 | `scripts/kvm/report_rc.sh:257` | `du -s $mount_point` 没加 `-x`，会统计到挂在下面的其他文件系统（§5.4）；第 252 行把 `qemu-img info` 输出的数字当作 GiB 解析，NVRAM 这类 KiB 级别的文件会被当成 528 GiB 之类的值 |
| 4 | `services/instance.go:276`、`services/migration.go:163` | 调度时磁盘需求按 `GB*1024*1024` 传递，而节点上报的是字节，**相差 1024 倍**，磁盘检查几乎永远通过。修正后调度会真的按磁盘拒绝，需要先核对各节点的 `disk_over_ratio` 和剩余空间 |
| 5 | `rpcs/capture_image.go:57-63` | 本地模式下，把回调的第 4 个参数（字面量 `success`）当作镜像大小做 `Atoi`，解析失败后直接返回。S3 模式由 `UploadCapture` 另外把镜像置为可用，所以表面上看不出问题 |
| 6 | `model/volume.go:57-66`、`rpcs/create_volume.go` | 注释里的 `local://` 格式与实际不符；本地系统盘在库里记为 `volume-<卷ID>.disk`，实际文件是 `$image_dir/inst-<实例ID>.disk` |
| 7 | `services/backup.go:526` | 按单个卷判断驱动时，本地卷得到的是空串（不是 `"local"`），会走进 WDS 分支。现在本地卷建不了备份，所以走不到这里 |
| 8 | `scripts/kvm/end_rescue.sh:13,22` | `virsh undefine` 没带 `--nvram`，UEFI 救援虚拟机的 NVRAM 文件会残留 |
| 9 | `scripts/kvm/launch_vm.sh:116-149` | 如果 `$volume_dir/inst-N.disk` 已经存在，脚本不发 `create_volume` 回调，系统盘会一直停在 `pending` |
| 10 | `cpgateway/src/services/quota.go:509-512` | 创建卷时按 `size` 预扣配额，没有乘以 `count`（clapi 接受 1 到 16）。与本设计无关，一并记在这里 |
