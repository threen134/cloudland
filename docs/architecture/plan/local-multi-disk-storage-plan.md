# 本地多盘存储池设计（按宿主机配置磁盘，支持 SSD / HDD 混用）

- **状态**：**L0a–L4 已实施（2026-09-22，未提交）**，在 work-01/02/03 上用各自空闲的 `sdb` 做了端到端测试；WDS 已放弃并整体删除（§13 第 1 条）。实施中与本文不同的做法、发现的问题和测试结果见 **§14**。设计经三轮审查修订；第三轮同时做了代码事实核对、对抗式设计审查和内部一致性审查
- **日期**：2026-09-22，基于 `stage-01` 分支 `53dceb82`（文中行号均以此为准；之后只有 `apis/instance.go`、`services/instance.go` 各多了几行）
- **涉及**：clapi（`api/`）、节点脚本（`scripts/`）、cpgateway（代理白名单与配额）、前端（`web/`）、部署脚本与告警模板
- **相关文档**：`gpfs-shared-storage-design.md`（下称「GPFS 设计」）。本文沿用它的存储池模型和「clapi 是路径的唯一来源」这一原则
- **执行顺序**：**本文先实施，GPFS 设计后实施**。GPFS 设计阶段 0 里本地存储需要的部分由本文的 L0a / L0b 完成；两份文档对同一件事说法不同时以本文为准，GPFS 设计里已在相应位置加注

---

## 0. 摘要

- **两层结构**：「存储池」是区域级、用户可选的资源（如 `local-ssd`、`local-hdd`）；「节点存储池」是某台宿主机上属于某个存储池的一个目录，由管理员按机器配置。每台机器可以有 0 个或多个本地存储池，盘的类型、数量、冗余方式各不相同。
- **同一台机器上，一个存储池只对应一个目录**，路径在所有机器上相同：`/opt/cloudland/pools/<池UUID>`。同类的多块盘用 LVM（可选 RAID1）合成一个目录。
- **管理员在计算节点详情页配置磁盘**：扫描、建池、加盘、换盘（RAID1）、删池、维护；节点重新注册后「接管」原来的池；盘坏了「宣告丢失」（可以撤销）。
- **防误删**：扫描直接读盘上的 LVM 标签和 md 超级块来识别 CloudLand 的盘，读不出来的 LVM / md 成员一律不许清除；疑似共享 LUN 的盘不许选；建池要输入机器名。建池、加盘时后端检查介质是否一致，不一致要显式确认（§2.6）。
- **容量准入按「机器 × 池」计算**：在请求事务里锁住节点存储池记录再判断。首次挂载、扩容直接把卷计入已分配；迁移和系统盘（L4）用预留。迁移另在复制前按实际数据量预检目标空间。
- **精简置备的池快满时**（§6.1）：80% / 90% 预警；占用达到 90%、或池上有因空间不足暂停的虚拟机时，挡住新的分配；虚拟机里删掉的数据经 `discard` 还给池；真写满时虚拟机暂停（不写坏数据），界面显示原因，空间回落后自动恢复。
- **坏盘不拖垮节点**：池的探测、挂载重试、自动恢复都在每个池一个的后台进程里做，心跳只读结果；锁按池拆分，全部有等待上限。
- **迁移**：默认留在原池；管理员可以逐盘换到目标机器上的其他池；目标机器没有原池时，只在同一个「可互换组」里自动替换。迁移计划在目标机器确定之后、复制磁盘之前生成并准入。
- **开机不阻塞心跳**：池还没就绪的虚拟机进入待启动列表，就绪后再启动。
- **分阶段**：L0a（数据盘相关的存储池抽象）→ L1（本地池与数据卷）→ L2（迁移时换池）→ L3（加盘、换盘、RAID1、告警）→ L0b（系统盘相关的抽象）→ L4（系统盘放进池）→ L5（可选）。

---

## 1. 背景与目标

### 1.1 现状

- **本地卷只有一个目录**：`scripts/cloudrc:11` 写死了 `volume_dir=/opt/cloudland/cache/volume`，`scripts/kvm/` 下有 12 个脚本引用它。其中 4 个（`attach_vol.sh`、`detach_vol.sh`、`create_volume_from_image.sh`、`create_volume_local.sh`）没有任何地方下发，实际在用的是 8 个：多数直接拼卷的路径；`finish_source_migration.sh`、`clear_target_migration.sh` 把它当作清理时的路径前缀；`launch_vm.sh`、`reinstall_vm.sh`、`rescue_vm.sh` 用它找 `inst-N.disk`（回退逻辑）。系统盘在 `$image_dir`（`/opt/cloudland/cache/instance`）。两个目录都在根分区上。
- **库里的路径和实际文件对不上**：本地数据盘在库里记为 `volume-<id>.disk`（`services/volume.go:197`），脚本用 `$volume_dir/$3` 拼成完整路径；系统盘在库里也记成 `volume-<卷ID>.disk`（`launch_vm.sh:148` 回调 → `rpcs/create_volume.go:72-73`），实际文件却是 `$image_dir/inst-<实例ID>.disk`。系统盘的 `volumes.hyper` 创建时不写，只有迁移完成后才有值（`rpcs/migrate_vm.go:307`）。
- **节点上的盘没用上**：work-01、work-02、work-03 各有两块 2 TB 机械盘（Seagate ST2000NM0055）。操作系统、系统盘、数据卷全在 `sda4` 上；第二块盘 `sdb` 闲置：work-01 / work-03 的 `sdb1` 挂在 `/disk1`（ext4，空），work-02 的 `sdb1` 没有挂载。
- **磁盘容量没人管**：
  - 挂载和扩容本地卷时不检查剩余空间。卷文件是 qcow2 精简置备，分配总量可以超过磁盘容量，写满时虚拟机出现 I/O 错误。
  - 心跳（`report_rc.sh`）算节点可用磁盘时，虚拟大小只统计 `$image_dir/*`（第 248–256 行），数据卷不在其中；而且把 UEFI 虚拟机的 NVRAM 文件（528 KiB）也送进 `qemu-img info`，按 GiB 解析，**每台 UEFI 虚拟机凭空多算 528 GiB**。另外 `du -s`（KiB）与 `df -B 1`（字节）直接相减（第 257–258 行），调度时的磁盘需求单位也不对（附录 B 第 3、6、7 条）。结果是：没有 UEFI 虚拟机时 cland 的磁盘检查几乎总是通过，UEFI 虚拟机多了又会把节点判成磁盘不足。
  - clapi 从 `hyper_status` 回调（`report_rc.sh:309`）拿到的是乘过超分比例、减掉系统盘虚拟大小之后的值，不是文件系统的原始容量；超分比例本身也没有写进数据库（附录 B 第 10 条）。
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
7. 加盘、换盘在线完成，不停虚拟机。
8. 节点重新注册、盘坏、节点离线等情况有明确的处置流程，不会卡死，也不会误删数据。
9. 一块坏盘不会拖垮整台节点的心跳和命令执行。

### 1.3 非目标

- 共享存储（GPFS 设计负责）。
- 同一台机器上同一个存储池拆成多个目录。需要隔离时建成不同的存储池，并且不把它们放进同一个可互换组（§2.2）。
- 不换机器、只换池（同一台机器上的池间搬迁），以及本地盘与 GPFS 盘之间的转换：放在可选的 L5。跨机器迁移时换池属于本文范围（§7）。
- 按存储池分别计配额：第一版不做（可选的 L5），cpgateway 仍然只有一个 `disk_gb`。
- 本地卷的备份与复制。
- `volume.driver` 配置为 WDS 等其他驱动的部署：本地池功能只在 `volume.driver` 为空或 `local` 时启用（WDS 的去留见 §13）。

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
- CloudLand 只看池这一层：满的是池的文件系统，不存在「池里某一块盘满了而池没满」（线性拼接按区段分配，RAID1 两块互为镜像）。

### 2.3 路径与标记

- 非内置本地池的根目录固定为 `/opt/cloudland/pools/<池UUID>`，**在每台有这个池的机器上都相同**。用 UUID 而不是名称，存储池可以改名，路径不变。路径相同的好处是：迁移时盘留在同一个池，路径不变，域定义不用改写；换池时路径不同，改写方法见 §7.3。
- 池根目录是一个独立挂载点：

```
/opt/cloudland/pools/<pool-uuid>/     <- mount point of the host's LV
├── .cloudland-pool                   <- marker, see below
├── volumes/volume-<volID>.disk       <- data disks and (phase L4) boot disks, qcow2
├── nvram/inst-<instID>_VARS.fd       <- UEFI NVRAM of instances whose boot disk is in this pool (L4)
└── tmp/
```

- `volumes.path` 存池内相对路径，绝对路径由 clapi 拼好下发：
  - 非内置池：`volumes/volume-<卷ID>.disk`；
  - 内置池：数据盘 `volume/volume-<卷ID>.disk`，系统盘 `instance/inst-<实例ID>.disk`。现在库里两类都记成 `volume-<id>.disk`（§1.1），**L0a 一次性改写存量记录**（§3.4）。
- **标记文件** `.cloudland-pool`，key=value 格式：

```
driver=local
pool_uuid=<pool uuid>
hostid=<hostid>
```

GPFS 池的标记文件只有前两行（共享池没有归属节点），检查方法见 §4.6。

- **同样的归属信息也写在盘上**：卷组带 LVM 标签 `cloudland_pool=<池UUID>`、`cloudland_host=<hostid>`；RAID1 阵列的名字为 `cl_<池UUID前8位>_<序号>`。标记文件在池自己的文件系统里，没挂载时读不到；扫描和接管靠盘上的这两样（§4.2、§5.9）。阵列没组装时，阵列名也能从成员盘的超级块读出（`mdadm --examine`）。

### 2.4 与 GPFS 设计的关系

| 方面 | GPFS 池（以后） | 本地池（本文） |
|---|---|---|
| 存储池记录 | `storage_pools`，`Driver=gpfs` | `storage_pools`，`Driver=local` |
| 节点记录 | `hyper_storage_pools`：该节点能否访问共享池 | `hyper_storage_pools`：该节点上这个池的目录，带盘、布局、容量、健康状态 |
| 卷与节点 | 不绑定，`hyper=0` | 落盘后绑定 `hyper` |
| 容量 | 池级（fileset 配额），存在 `storage_pools` 上 | 机器 × 池级，存在节点记录上，池级合计查询时汇总 |
| 可用性上报 | clapi 广播检查脚本（GPFS §5.2） | 节点后台探测、心跳上报（§4.7）。两套机制写同一张表，各管各的驱动 |
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

对应接口 `POST /hypers/:uuid/storage_pools`（clapi 的节点路由参数是 `:uuid`，网关白名单里写作 `:hostid`，传的都是节点 UUID，§8.1），执行过程：

```
clapi: in one transaction, insert hyper_storage_pools row (host, pool, layout, disks, vg name,
       status=creating, last_op=create, deadline); dispatch after commit (inter=<hostid>, via async_exec)
  v
node create_local_pool.sh (§4.3):
  re-check disks -> wipe if asked -> mdadm RAID1 (if chosen) -> LVM vg (tagged pool uuid + hostid)
  -> mkfs.xfs -> mount at /opt/cloudland/pools/<pool uuid>, fstab by file system UUID
  -> create volumes/ nvram/ tmp/, write marker .cloudland-pool
  | callback local_pool_status <node> create <pool uuid> ready|degraded|error <reason> <details>
  v
clapi: status=ready or degraded, record capacity (creating rows past their deadline become error)
```

**之后的维护**都在同一个标签页：加盘（§4.4）、换盘（§4.9）、删池（§4.5）、维护（§5.11）、宣告丢失与恢复（§5.8）、接管（§5.9）。

**对应关系存在哪里**：

| 位置 | 记录的内容 |
|---|---|
| clapi `hyper_storage_pools` | 哪台机器、哪个池、哪几块盘（含 RAID1 配对）、布局、卷组名、容量、状态。界面显示的就是这里 |
| 节点的 LVM / mdadm | 盘 → 阵列 → 卷组 → 逻辑卷。卷组带标签 `cloudland_pool`、`cloudland_host`，阵列名 `cl_…` |
| 节点的 `/etc/fstab` | 文件系统 UUID → 池路径，开机自动挂载；也是节点认定「本机有哪些池」的依据（§4.7） |
| 池根目录下的 `.cloudland-pool` | 驱动、池 UUID、hostid |

节点上**没有专门的 CloudLand 配置文件**，不需要手工编辑 `cloudrc.local`。

### 2.6 分配盘时的检查（介质与容量）

建池、加盘、换盘时，除了 §8.1 的状态检查（空闲、是否清除、盘数），clapi 在下发前还做下面的检查。**检查在后端做**，界面只负责展示结果和收集确认，直接调接口也绕不过去。

| 检查 | 触发条件 | 处理 |
|---|---|---|
| 盘与池的介质 | 池的 `Media` 不为空，所选盘里有介质与它不同的 | 返回 400，列出这些盘和各自的介质；请求带 `allow_media_mismatch: true` 时放行 |
| 所选盘之间的介质 | 建池时所选盘的介质不全相同；加盘、换盘时新盘与池里已有成员的介质不同 | 同上，共用 `allow_media_mismatch` |
| RAID1 一对盘的容量 | 同一对的两块盘容量相差超过 1% | 不拦截。界面按扫描结果算出每对的实际可用容量（按小的那块算）和浪费的容量并提示，成功响应里也带上这些数值 |

- **介质不一致只要求确认、不绝对禁止**：自动识别不完全可靠，硬件 RAID 卡后面的 SSD 常被报成旋转盘，部分 SAN 盘、虚拟盘也一样。识别错了应当纠正（见下一条）；真要混用时（例如临时拿 SSD 顶替 HDD 池的坏盘）也有路可走。
- **以什么介质为准**：扫描得到的是自动识别值（§4.2）。管理员可以在扫描结果里手工修改某块盘的介质（`PATCH /hypers/:uuid/disks/:id`），之后的检查一律按修改后的「生效介质」比较。手工指定的值重新扫描不会丢（§3.3）；界面标明是「自动识别」还是「手工指定」，并显示自动识别的原值。
- **所选盘之间也要一致**：一块 SSD 和一块 HDD 组 RAID1，写入要等两块都完成，整个阵列被拖到机械盘的速度；线性拼接时卷落在哪块盘上由 LVM 分配，同一个池的性能忽快忽慢。池的 `Media` 为空时，这一条照样检查。
- **池里已有成员的介质**取 `hyper_storage_pools.Devices` 里记录的值（建池、加盘、换盘时写入的生效介质）。
- **RAID1 的配对**：按请求里 `disks` 的顺序每两块一对（第 1、2 块一对，第 3、4 块一对……），界面按对展示，管理员可以调整顺序。设 1% 的阈值是为了忽略同标称容量、不同批次的盘之间几 MB 的差别。
- **修改存储池的 `Media`**：已有机器上的成员盘与新标签不一致时只提示（列出这些机器），不拦截，已建好的池照常使用。
- **留痕**：放行了介质不一致的，审计记录里注明（§8.4）；节点存储池详情里给不一致的成员盘加标记，方便以后排查性能问题。

---

## 3. 数据模型

表结构由启动时的 AutoMigrate 创建，不写迁移代码。**`storage_pools`、`hyper_storage_pools` 由本文创建**：L0a 建 `storage_pools`（只有内置 `local` 池），L1 建其余的表。字段沿用 GPFS 设计 §4.1、§4.2 的定义；只对 GPFS 有意义的字段——`storage_pools` 的 `FsName`、`Fileset`、`FsType`、`CloneMode` 和池级的 `CapacityBytes`、`UsedBytes`、`CapacityAt`，以及 `volumes` 的 `BaseImageStorageID`——等执行 GPFS 设计时再加。

**删除一律用硬删除**（`Unscoped().Delete`）：这几张表带唯一索引，软删除的行仍占着唯一值，删了再建会冲突。

### 3.1 `storage_pools`

在 GPFS 设计 §4.1 的基础上：

| 字段 | 说明 |
|---|---|
| `Media string` | `ssd` / `hdd` / `nvme` / 空。用于展示，以及建池、加盘时的介质检查（§2.6），**不决定迁移行为** |
| `FallbackGroup string` | **可互换组**。组名相同、且都不为空的池，在迁移时可以自动互换（§7.1）。为空表示不参与。内置 `local` 池固定为空。字段与编辑在 L1，迁移时的自动替换在 L2 |
| `Shared`（不是数据库列） | 由 `Driver` 现算：`gpfs` 为 true，`local` 为 false。接口响应里输出 `shared` 字段，前端据此显示「共享 / 本地」 |

- 非内置本地池的 `MountPath` 创建时自动设为 `/opt/cloudland/pools/<UUID>`，不能修改。
- **`Status`**（`active` / `disabled`，GPFS §4.1）：`disabled` 的池不接受新建卷、首次挂载、迁入，已有的卷照常使用。
- 本地池的池级容量是各机器的合计，**查询时汇总**，不入库。
- **默认池**：建议保持内置 `local`。在 L4 之前，默认池只影响数据盘；把一个不是每台机器都有的池设为默认时，界面给出警告。
- **删除存储池**：还有任何卷（包括未落盘的），或还有任何本地池的节点记录时拒绝。GPFS 池的节点可用性记录随池一起删除。内置池不能删除。
- **普通成员能看到的字段**：GPFS §10.1 规定的那几项，再加 `media` 和「可用机器数」；只列出 `active` 的池。

### 3.2 `hyper_storage_pools`

每条记录表示「这台机器上这个池的目录」。唯一索引 `(hostid, pool_id)`。

```go
type HyperStoragePool struct {
	Model
	Hostid     int32      `gorm:"uniqueIndex:idx_hyper_pool"`
	PoolID     int64      `gorm:"uniqueIndex:idx_hyper_pool"`
	Status     string     `gorm:"type:varchar(32)"` // see the table below
	Reason     string     `gorm:"type:varchar(256)"`
	LastOp     string     `gorm:"type:varchar(16)"` // create | extend | replace | remove | adopt
	CheckedAt  time.Time  // last report from the node, also while lost (restore needs it, §5.8)
	DeadlineAt *time.Time // creating / extending / removing give up after this
	// local pools only
	Layout        string  `gorm:"type:varchar(16)"` // builtin | single | linear | raid1
	Devices       string  `gorm:"type:text"`        // JSON: [{id, serial, model, size_bytes, media, array, pair}]
	VgName        string  `gorm:"type:varchar(64)"` // cl_<first 8 of pool uuid>_<4 random hex>, unique per host
	CapacityBytes int64   // what CloudLand may use, see below
	UsedBytes     int64   // df used
	AvailBytes    int64   // df available (excludes blocks reserved for root)
	OwnBytes      int64   // builtin only: actual usage of CloudLand files
	OverRatio     float64 // builtin only: the node's disk_over_ratio, reported by the node
	CapacityAt    *time.Time
	SyncPercent   int    // RAID1 resync progress, 100 when in sync
	UsageReport   string `gorm:"type:text"` // JSON: largest files by actual usage, from the last on-demand scan (§6.1)
	UsageAt       *time.Time
}
```

**内置池记录**：
- 节点注册时创建，clapi 启动时再为缺少记录的节点补建，不等节点第一次上报。`Layout=builtin`。
- 还没收到上报（`CapacityAt` 为空）时按可用处理（§5.2、§6）。
- 删除节点时随节点一起删除，不参与「节点上还有池」的检查（§5.9）。

**容量口径**（全部为字节）：
- `CapacityBytes`：非内置池取池文件系统的大小；内置池取 `OwnBytes + AvailBytes`，即 CloudLand 最多能用到多少。根分区上的操作系统、控制面数据、日志不算进内置池的容量。
- **占用比例 = `UsedBytes / (UsedBytes + AvailBytes)`**，与 `df` 的 Use% 相同。这样自动扣掉了 ext4 为 root 保留的空间：QEMU 以 `libvirt-qemu` 身份运行，用不到这部分，按文件系统大小算的话，df 显示约 95% 时虚拟机就已经写满了。全文的 80% / 85% / 90% 都按这个比例。

**`Status` 的含义与允许的操作**（「可用」在全文都指 `ready` 或 `degraded`）：

| 状态 | 含义 | 接受新分配 | 允许的操作 |
|---|---|---|---|
| `creating` | 建池或接管的命令已下发，等待结果。超过 `DeadlineAt`（30 分钟）转为 `error` | 否 | — |
| `extending` | 加盘或换盘（L3）进行中，池照常可读写。失败或超时回到原来的状态，原因写进 `Reason` | 否 | — |
| `removing` | 删池进行中。超时转为 `error` | 否 | — |
| `ready` | 探测通过，存储层健康 | 是 | 全部 |
| `degraded` | RAID1 缺盘或正在同步，仍可读写 | 是（并告警） | 全部 |
| `unavailable` | 探测失败（没挂载、标记不对、逻辑卷或阵列不可用、探测卡住），或 15 分钟没有上报（`Reason=node_offline`） | 否 | 维护、宣告丢失、删池（池里没卷时） |
| `maintenance` | 管理员置为维护，节点不自动挂载（§5.11） | 否 | 退出维护、宣告丢失、删池（池里没卷时） |
| `lost` | 已宣告丢失（§5.8） | 否 | 恢复、强制删池（只卸载，不删文件、不擦盘） |
| `error` | 建池或删池失败（`LastOp` 说明是哪一个） | 否 | 建池失败：删除记录（节点已回滚），或重新提交建池（复用这一行）；删池失败：重试删池 |

### 3.3 `hyper_disks`

磁盘扫描的结果，供建池、加盘、换盘、接管时选择：

```go
type HyperDisk struct {
	Model
	Hostid        int32  `gorm:"uniqueIndex:idx_hyper_disk"`
	DiskID        string `gorm:"uniqueIndex:idx_hyper_disk;type:varchar(256)"` // stable id, see below
	Name          string `gorm:"type:varchar(32)"` // sdb, nvme0n1: display only
	Serial        string `gorm:"type:varchar(128)"`
	DiskModel     string `gorm:"column:model;type:varchar(128)"`
	SizeBytes     int64
	Transport     string `gorm:"type:varchar(16)"` // sata | sas | nvme | fc | iscsi | ...
	DetectedMedia string `gorm:"type:varchar(16)"` // ssd | hdd | nvme, as detected by the scan
	Media         string `gorm:"type:varchar(16)"` // effective media: DetectedMedia unless set by an admin
	MediaSource   string `gorm:"type:varchar(8)"`  // auto | manual
	State         string `gorm:"type:varchar(32)"` // free | dirty | in_use | system | cloudland_pool | unknown_member | shared
	Detail        string `gorm:"type:varchar(256)"`
	PoolUUID      string `gorm:"type:varchar(64)"` // in_use members of this host's pools, and cloudland_pool
	OwnerHostid   int32  // cloudland_pool: from the cloudland_host tag, 0 when unreadable
	ScannedAt     time.Time
}
```

- **稳定标识**按优先级取 `/dev/disk/by-id/` 下的：`wwn-*` > `nvme-eui.*` > `ata-*` / `scsi-*`（每块盘通常同时有这几个链接）。测试环境放行 loop 设备时用 `loop:<backing file>`（§12）。
- 每次扫描按 `(hostid, DiskID)` 更新记录，这次没扫到的盘硬删除。`MediaSource=manual` 的记录更新时保留 `Media`，只刷新 `DetectedMedia`；手工值清空（恢复自动识别）时 `Media` 改回 `DetectedMedia`。盘从扫描结果里消失后，它的手工值随记录一起删除，再出现时要重新指定。

### 3.4 `volumes`

| 字段 | 变更 |
|---|---|
| `StoragePoolID int64` | 新增（L0a）。启动时回填：值为 0 的行改为内置池的 ID（带条件的幂等 `UPDATE`，不算迁移代码） |
| `Path` | 池内相对路径，由 clapi 在建记录时写入。**L0a 启动时改写存量记录**：内置池里记成 `volume-<id>.disk` 的数据盘改为 `volume/volume-<id>.disk`，系统盘改为 `instance/inst-<实例ID>.disk`（按 `instance_id`，`booting=true`）。同样是带条件的幂等 `UPDATE` |
| `Hyper` | 卷文件所在节点。数据盘：首次挂载时**在请求事务里**写入（§5.2；现在是挂载回调里写）。系统盘：创建回调里写（L0a；现在创建时不写，只有迁移完成后才有值），L0a 同时用所属实例的 `hyper` 回填存量系统盘 |
| `Status` | 新增取值：`deleting`（§5.5）、`delete_failed`（删除超时，只允许再删）、`lost`（池已宣告丢失，§5.8）、`orphaned`（节点已删除、等待接管，§5.9） |
| `Reason` | 新增，记录失败或异常的原因 |

### 3.5 `storage_reservations`（新增）

容量预留，只用于**事先不能把卷直接计入**的两种情况：迁移（迁入的盘在完成前还属于源机器）和系统盘（L4，卷记录在创建回调里才落到目标机器）。首次挂载和扩容不用预留：前者在请求事务里就把卷的 `hyper` 写成目标机器，后者在请求事务里就把 `size` 改成新值，两者都直接计入已分配（§5.2、§5.4）。

```go
type StorageReservation struct {
	ID          int64 `gorm:"primaryKey"`
	Hostid      int32 `gorm:"index:idx_res_host_pool"`
	PoolID      int64 `gorm:"index:idx_res_host_pool"`
	VolumeID    int64
	MigrationID int64  // incoming disks of a migration, 0 otherwise
	Kind        string `gorm:"type:varchar(16)"` // boot | migration
	SizeGB      int32  // virtual size
	ExpiresAt   time.Time
}
```

- 过期时间：系统盘 30 分钟；迁移到迁移结束（完成或回滚时删除），另设 24 小时兜底。clapi 后台每 5 分钟清理过期的预留。
- **命令一律在事务提交之后下发**（沿用 `InstanceAdmin.Create` 先提交、再执行命令列表的做法），回调不会早于提交到达、看不到刚写入的预留或卷记录。

### 3.6 `migrations`

```go
// DiskPlan is fixed once the target host is known and then used unchanged by every later step (JSON):
// [{volume_id, device, booting, src_pool_id, src_path, dst_pool_id, dst_path, auto, reason}]
// plus, for a boot disk whose NVRAM lives in a pool (L4), {nvram: true, src_path, dst_path}
DiskPlan          string `gorm:"type:text"`
DiskRequests      string `gorm:"type:text"` // JSON: per-disk target pools asked for in the request
AllowPoolFallback bool
IgnoreCapacity    bool
```

- **生成时机**：指定了目标机器时，创建迁移时生成；不指定时，在 `target_prepared` 回调里、下发 `source_migration.sh` 之前生成（§7.1）。生成后不再重算。
- 生成计划要用到请求里的参数，所以 `DiskRequests`、`AllowPoolFallback`、`IgnoreCapacity` 随迁移记录保存。
- 计划只包含本地盘；`src_pool_id == dst_pool_id` 时 `src_path == dst_path`。精确的虚拟大小和簇大小不放进计划，由源端复制前自己读（§7.3）。
- 迁移详情接口返回计划，界面显示每块盘从哪个池迁到哪个池。

### 3.7 其他表

- **`hypers`**：hostid 的分配改为在**包括已删除记录**在内的范围里取最大值 + 1（`Unscoped`）。现在只看未删除的记录（`services/hyper.go:304`），被删的正好是编号最大的节点时，下一台注册的机器会拿到同一个号，挂在旧编号下等待接管的卷就会算到那台机器头上（附录 B 第 9 条）。
- **`instances.Reason`** 新增取值：`storage_full`（因存储空间不足暂停，§6.1）、`storage_pending`（开机时池未就绪、在待启动列表里，§4.8）。

---

## 4. 节点侧

- **脚本**放在 `scripts/kvm/`，以 `|:-COMMAND-:|` 回调。多行数据用 base64 编码成一个参数（节点回调里是第一次这样用；单行上限 1 MiB，`cloudlet/executor.go:36`，够用）。
- **池相关的回调统一格式**：`local_pool_status '<NODE_ID>' '<op>' '<池UUID|builtin>' '<status>' '<reason>' '<base64 JSON>'`。
  - `op` 为 `create` / `extend` / `replace` / `remove` / `adopt` / `report`。
  - JSON 里是容量（size、used、avail、own）、同步进度、`over_ratio`（内置池）、布局、成员盘与配对、卷组名；接管时另有 `volumes/` 下的文件清单。
  - 内置池的 UUID 节点并不知道，用固定的 `builtin` 表示。
  - clapi：命令结果（`op` 不为 `report`）要与记录的 `LastOp`、当前状态对得上才生效，重复回调幂等。`report` 只更新容量与健康信息，不改变 `creating` / `extending` / `removing` / `maintenance` / `lost` 记录的状态（`lost` 的记录仍更新 `CheckedAt` 和健康信息，§5.8 的「恢复」要用）。
- **锁**：
  - 每个池一把 `/var/lock/cloudland-pool-<uuid>.lock`（内置池为 `builtin`）。结构性操作（建池、加盘、换盘、删池、接管）拿排他锁；写池的操作（落盘、扩容、迁移预建与迁入占位、删卷、自动恢复）拿共享锁。
  - 另有一把只管「盘归谁」的 `/var/lock/cloudland-disks.lock`，扫描、建池、加盘、换盘时持有，防止两个操作选中同一块盘。
  - **所有 `flock` 都带 `-w`**（写池操作等 10 秒，结构性操作等 60 秒），拿不到就立即以失败回调。不能让一个卡住的操作堵住 cloudlet 的串行命令队列（默认 `CLOUDLET_CONCURRENCY=1`）。
- **长任务走 `async_exec`**：扫描、建池、加盘、换盘、删池、接管都可能要几十秒到几分钟，或者碰到坏盘卡住，不占用命令队列。
- **心跳不直接碰盘**：心跳脚本本身没有超时（cloudlet 的 `runReportScript` 只是 `cmd.Wait()`，30 秒的超时只作用于上报 RPC），而 `timeout` 杀不掉卡在 D 状态的进程，一次卡住就会让节点被判离线。所以池的探测、挂载重试、自动恢复都在后台探测进程里做（§4.7），心跳只读结果。

### 4.1 依赖

`lvm2`、`mdadm`、`xfsprogs`、`rsync`（关机迁移保留稀疏，§7.3）。现在三台节点都有，但部署脚本（`deploy-compute-node.sh:233-238`）和 Ansible 的 hyper 角色都没有显式安装，L1 起两处都加上。

### 4.2 扫描磁盘：`scan_host_disks.sh`

- 管理员点「扫描」触发（经 `async_exec`），不放进心跳。
- 数据来源：`lsblk -J -b -o NAME,PATH,TYPE,SIZE,ROTA,TRAN,MODEL,SERIAL,WWN,MOUNTPOINTS,FSTYPE,PKNAME`、`/proc/mdstat`。
- **判断归属不依赖本机的 LVM / md 视图**，直接读盘：
  - 带 `LVM2_member` 签名的盘或分区：用 `pvs --devicesfile "" --devices <设备> -o vg_name,vg_tags` 读卷组名和标签。这样可以绕过 LVM devices file：重装系统后，新的 `system.devices` 不含旧盘，按默认视图根本看不到它的卷组。
  - 带 `linux_raid_member` 签名的盘或分区：用 `mdadm --examine` 读阵列名，`cl_` 开头的就是 CloudLand 的阵列。
- **分类**（按顺序，先命中的为准）：
  - `system`：承载 `/`、`/boot`、swap、`/opt/cloudland/cache` 的盘（顺着分区、md、LVM 往上追）。**永远不能选**。
  - `shared`：传输类型为 fc / iscsi，或上层是多路径设备（mpath holder），或同一个 WWN 出现多次。这类盘可能是共享 LUN，本机视角看不出别的机器在不在用，**不能选**。将来接 GPFS 之前，还要加上 GPFS NSD 描述符的检测（libblkid 不认识它）。
  - `in_use`：已挂载（例如现在 work-01 / work-03 的 `sdb1` 挂在 `/disk1`），或者属于别的 md、LVM。本机正在用的 CloudLand 池（池在本机 fstab 里，且 `cloudland_host` 等于本机 `NODE_ID`）的成员也归在这里，带上 `PoolUUID`。
  - `cloudland_pool`：盘上有 CloudLand 的标签或阵列名，但不是本机正在用的池（不在本机 fstab 里，或 `cloudland_host` 不等于本机），例如节点重新注册之后、或从别的机器搬来的盘。带上 `PoolUUID` 和 `OwnerHostid`（取自 `cloudland_host` 标签；只能读到阵列名、读不到卷组标签时为 0）。**只能接管，不能在建池时清除**（§5.9）。
  - `unknown_member`：有 `LVM2_member` / `linux_raid_member` 签名，但读不出卷组标签或阵列名，例如 RAID1 只剩一块成员盘、卷组又在阵列里面。它可能是 CloudLand 的池，**不能清除**；确实要清除时，由管理员登录节点手工处理。
  - `dirty`：没在用，带分区表或非 LVM、非 md 的文件系统签名（`wipefs -n` 能读到），例如现在 work-02 的 `sdb1`。可以在建池时选择清除。
  - `free`：整盘、没有任何签名。
- 只列 `TYPE=disk`，排除 rom、可移动设备；loop 设备只在测试开关打开时列出（§12）。
- 介质（自动识别值，写入 `DetectedMedia`）：`TRAN=nvme` 为 `nvme`，`ROTA=0` 为 `ssd`，其他为 `hdd`。硬件 RAID 卡、部分 SAN 盘和虚拟盘会把 SSD 报成旋转盘，识别错时由管理员手工纠正（§2.6）。
- 分类逻辑写在 `cloudrc` 的一个函数里，建池、加盘、换盘时节点二次检查用的是同一个函数。
- 回调 `host_disks '<NODE_ID>' '<base64 JSON>'`。

### 4.3 建池：`create_local_pool.sh`

参数：`<池UUID> <布局> <卷组名> <hostid> [--wipe] [--destroy-pool <池UUID>...] <盘标识...>`。根目录由脚本按 UUID 拼出（先校验 UUID 格式），不接受路径参数；卷组名由 clapi 生成。

1. **再检查一遍每块盘**（不相信 clapi 的扫描结果，用 §4.2 的同一个函数）：只接受 `free`，或带 `--wipe` 的 `dirty`，或者带 `--destroy-pool` 且标签等于所列池 UUID 的 `cloudland_pool`（§5.9 末尾）。
2. **清除**（`--wipe` / `--destroy-pool`）：先对盘上每个分区 `wipefs -a`，再对整盘 `wipefs -a`，然后 `blockdev --rereadpt`，并确认内核里已经没有这块盘的分区。只擦整盘的话，分区表是擦掉了，分区里的文件系统签名却会留在逻辑卷的开头，后面的 `lvcreate` / `mkfs` 会拒绝。
3. **按布局组装**：
   - `single`：一块盘作为 PV。
   - `linear`：多块盘都作为 PV。**没有冗余**。
   - `raid1`：盘数为偶数，按传入顺序每两块 `mdadm --create --level=1 --name=cl_<池UUID前8位>_<序号> --run` 组成一个阵列（同一对容量不同时按小的那块算，clapi 事先已提示，§2.6），阵列作为 PV，写入 `/etc/mdadm/mdadm.conf`。**不用 `--assume-clean`**：阵列建好就能用，初次同步在后台进行（2 TB 机械盘要几个小时），期间池为 `degraded` 并上报进度。
4. `vgcreate --yes --addtag cloudland_pool=<池UUID> --addtag cloudland_host=<hostid> <卷组名>`，`lvcreate --yes -l 100%FREE -n data`。
5. `mkfs.xfs -f -K`（`-K` 不在建文件系统时对 SSD 整盘执行 discard）。选 xfs：大文件表现好，没有 ext4 单文件 16 TiB 的限制，支持在线扩容。
6. fstab：`UUID=<文件系统UUID> <根目录> xfs defaults,nofail,x-systemd.device-timeout=120s 0 2`，然后 `mount`。等待时间要比 mdadm 降级阵列开机时的强制启动（约 30 秒）长。
7. 建 `volumes/`、`nvram/`、`tmp/`，写标记文件（§2.3），属主设为 `cland`。
8. 回调 `op=create`，状态 `ready` 或 `degraded`，带容量、布局、成员盘与配对、卷组名。
9. 失败时回滚已做的步骤（卸载、删 fstab 行、`vgremove`、`mdadm --stop` 与 `--zero-superblock`），再回调 `error`。已清除的盘恢复不了原来的签名，确认框里要写清楚。

### 4.4 加盘：`extend_local_pool.sh`（L3）

- 记录置为 `extending`，池照常可读写。
- `linear`：`pvcreate` → `vgextend` → `lvextend -l +100%FREE -r`，在线完成。
- `raid1`：每次加两块，组成新阵列 → `pvcreate` → `vgextend` → `lvextend -r`，在线完成。
- `single` 加一块变成 `linear`（无冗余，界面提示）。「单盘加一块变成 RAID1」要搬数据，第一版不支持。
- 新盘的检查、清除与 §4.3 第 1、2 步相同；介质与容量检查见 §2.6。
- 回调 `op=extend`。失败时回到原来的状态，原因写进 `Reason`，**不转为 `error`**：`error` 允许「删除记录」，对一个有卷、正在使用的池很危险。

### 4.5 删池：`remove_local_pool.sh`

- **正常删除**：
  - clapi 在一个事务里锁住这条记录（与准入同一把行锁），校验池里没有卷（`storage_pool_id=池`、`hyper=这台机器`，含系统盘和 `deleting`、`delete_failed` 的卷）、没有未过期的预留、没有以它为目标的进行中迁移，并在同一个事务里改为 `removing`。要求输入机器名确认。
  - 节点：拿池的排他锁 → 检查 `volumes/` 为空（不为空就拒绝并列出里面的文件）→ `umount` → 删 fstab 行 → `vgremove --yes` → `mdadm --stop` / `--zero-superblock` → 各成员盘 `wipefs -a` → 删 mdadm.conf 对应行 → 回调 `op=remove`。
- **池里有残留文件**（删卷命令丢失、迁移残留等）：删池被拒绝并列出文件。管理员确认后用 `force=true`，节点删掉这些文件再继续，文件清单记进审计。
- **丢失的池**（`lost`）：见 §5.8。节点只做 `umount -l`、停用卷组、删 fstab 行，**不删文件、不擦盘**；clapi 不管节点结果都删掉记录。之后这些盘扫描为 `cloudland_pool` 或 `unknown_member`。

### 4.6 挂载保护：`pool_guard`

L1 在 `cloudrc` 里新建（GPFS 设计 §5.1 的同名函数属于 GPFS 阶段 1，本文先实施，由本文先写）。签名：`pool_guard <根目录> <池UUID> <期望的hostid>`；GPFS 以后增加驱动参数，按驱动选择检查项（GPFS 的 fileset 不是挂载点，不做 `mountpoint` 检查，标记文件也没有 hostid）。本地池的检查：

1. `mountpoint -q <根目录>`；
2. `stat -f -c %T <根目录>` 为 `xfs`；
3. 标记文件里的 `driver=local`、`pool_uuid`、`hostid` 都与参数一致。

**期望的 hostid 由调用方传入**（clapi 下发命令时带上），不从环境变量取，所以既能在本机执行，也能经 ssh 到别的机器上执行（迁移时源端检查目标，§7.3）。凡是写本地池的脚本，第一次写入前都必须调用它；内置池不调用。**不对池根目录做 `mkdir -p`**。

### 4.7 池的探测与上报

- **每个池一个后台探测进程** `pool_probe.sh <池UUID|builtin>`：
  - 心跳发现某个池没有正在运行的探测进程、且上次结果已超过 60 秒，就用 `setsid` 脱离启动一个，不等待它结束。
  - 结果写进 `$run_dir/pool-<uuid>.state`：状态、原因、容量、已用、可用、CloudLand 占用、阵列状态、同步进度、时间戳。
- **心跳只读状态文件**，不执行任何碰盘的命令。某个池的探测进程超过 5 分钟还没结束，心跳就把这个池报成 `unavailable`（原因「探测卡住」），并且不再为它启动新的探测，避免卡死的进程越积越多。
- **池的清单**：以 `/etc/fstab` 里挂载点在 `/opt/cloudland/pools/` 下的条目为准（标记文件在池自己的文件系统里，没挂载时读不到）；内置池固定存在。
- **探测内容**：
  - 已挂载：`pool_guard`（期望的 hostid 取本机 `NODE_ID`）、`df -B1`（大小、已用、可用）、阵列与逻辑卷状态。内置池另取 CloudLand 文件的实际占用（`du -sxB1 $image_dir $volume_dir`）和 `cloudrc` 里的 `disk_over_ratio`。
  - 没挂载：按退避间隔（1、2、5、10 分钟）尝试挂载一次。挂载前用 `flock -n -s` 拿池锁，拿不到（有结构性操作在进行）就跳过这一轮；有维护标记（§5.11）时不挂载，直接报 `maintenance`。
  - 阵列：`mdadm --detail --test` 退出码 1 为 `degraded`，2 为 `unavailable`；同步中取 `/proc/mdstat` 的进度。
  - `lost` 池（节点仍在 fstab 里保留条目时）也照常探测，健康信息供「恢复」参考（§5.8）。
  - 自动恢复因空间不足暂停的虚拟机（§6.1）也在这里做。
- **上报**：心跳读到状态变化时立即上报（实际占用跨过 80%、85%、90% 也算变化），否则 5 分钟一次（时间戳文件节流），回调 `op=report`。
- clapi：某条可用记录超过 15 分钟没收到上报，转为 `unavailable`，`Reason=node_offline`。这个原因要与存储本身的故障区分开，§5.8 的宣告丢失要用。
- **告警指标**（L3）：探测进程同时把池的状态和占用比例写进 node_exporter 的 textfile 目录（`cloudland_pool_status{pool="…"}`、`cloudland_pool_usage_ratio{pool="…"}`），供告警规则使用。Ubuntu 的 `prometheus-node-exporter` 默认开启 textfile 收集器，目录为 `/var/lib/prometheus/node-exporter`（§13 待确认）。

### 4.8 开机：待启动列表

现状：节点重启后，`report_rc.sh` 的 `sync_instance` 遍历 `$xml_dir` 下的所有虚拟机逐台 `virsh start`（第 207–221 行；重启前已关机的也会被拉起，这个行为本方案不改），不看结果都回报 `running`（第 219–220 行），循环结束后写入 boot_id 标记（第 222 行）。**不能在这里等池挂载**：`report_rc.sh` 就是心跳，每台虚拟机等几分钟，节点就会被 cland 判离线。这正是 WDS 等待设备 200 秒、节点离线约 7 分钟的旧问题（见 CLAUDE.md 负载均衡一节）。

改为：

- **开机后第一次同步**：
  - 先对所有域执行 `virsh autostart --disable`。`resize_vm.sh:35` 会给改过规格的虚拟机开启开机自启，开机时 libvirtd 直接把它拉起来，绕过挂载保护（附录 B 第 8 条）。
  - 磁盘所在的池不可用（读 §4.7 的状态文件）的虚拟机不启动，写进待启动列表 `$cache_dir/pending_start`。`inst_status` 把它报成 `pending_storage`，clapi 记为已关机、`Reason=storage_pending`。
- **之后每次心跳检查一遍列表**：
  - 域已经不存在，或者已经在运行（用户手工启动过）→ 移出列表。
  - 池可用了 → 在后台带超时执行 `virsh start`。成功则移出列表，并补发 `launch_vm.sh '<id>' 'running' '<NODE_ID>' 'sync'` 回调，让 clapi 重建网卡、安全组、转发条目、浮动 IP，与现在的开机同步一样；失败则留在列表里，原因写进 `Reason`，不再回报 `running`（附录 B 第 2 条）。
- **clapi 对虚拟机下发关机、删除、迁移、重装、救援时**，相应脚本先把它移出待启动列表（`cloudrc` 提供 `pending_start_remove`），避免它之后被自动拉起。
- boot_id 标记照旧写。
- 节点详情页列出这台机器上 `Reason=storage_pending` 的虚拟机。

### 4.9 换盘（RAID1，L3）

- RAID1 池的某块成员盘坏了、池为 `degraded` 时：`POST /hypers/:uuid/storage_pools/:pool_id/replace_disk {failed_disk, new_disk, wipe, allow_media_mismatch, confirm}`。
- 节点 `replace_local_pool_disk.sh`：新盘按 §4.3 第 1、2 步检查与清除 → `mdadm --manage <阵列> --remove <坏盘>`（已被内核踢出时跳过）→ `--add <新盘>` → 后台重建。重建期间池保持 `degraded` 并上报进度，完成后回到 `ready`。
- 记录置为 `extending`，回调 `op=replace`，`Devices` 里更新配对。新盘不能比阵列成员小（mdadm 会拒绝，clapi 事先检查）。
- `single`、`linear` 池没有冗余，坏盘换不了，只能宣告丢失（§5.8）。

---

## 5. 各操作流程

以下「本地池」都指非内置的本地存储池；内置池除挂载保护外行为相同。所有下发给节点的命令都在事务提交之后发出（§3.5）。

### 5.1 创建数据卷

- 请求带 `storage_pool: {id}`；不传就用默认池。池必须是 `active`。
- 与现在一样**不落盘**：建记录，`path` 按 §2.3 写好，状态 `available`，`hyper=0`。
- 区域内没有任何机器的这个池处于可用状态时，照样允许创建，响应里带提示，界面显示「当前没有可用的节点」。
- 未落盘的卷扩容只改记录（现状），不需要准入。

### 5.2 首次挂载（落盘）

1. **校验**：虚拟机的状态允许挂载（§5.10）；存储池为 `active`；虚拟机所在机器上这个池的记录存在且可用（内置池还没收到上报时视为可用）。否则返回 400，例如「work-01 没有存储池 local-ssd」「work-02 上的 local-ssd 当前不可用：未挂载」。
2. **准入并直接计入**：同一个事务里锁住这条节点存储池记录，按 §6 判断；通过后把卷的 `hyper` 写成这台机器。卷从这时起计入已分配，不需要预留。
3. **事务提交后下发** `attach_volume_local.sh '<实例ID>' '<卷ID>' '<绝对路径>' '<卷UUID>' '<GB>' '<池UUID>' '<hostid>' 'new'`（保留现有的前四个参数，其余追加在后面）：
   - 最后一个参数说明卷是否应当已经存在：`new` 时文件已存在就报错（不覆盖）；`existing` 时文件不存在就报错，**不再新建一块空盘**。现在 `attach_volume_local.sh:14-15` 见文件不存在就新建，删除回调丢失后再挂载，会静默得到一块空盘（附录 B 第 13 条）。
   - 拿池的共享锁；`pool_guard`（内置池跳过）；`new` 时按传入大小 `qemu-img create`；挂载失败时删掉本次新建的文件。
4. **回调**：成功时改为已挂载。失败时，如果是 `new`，把 `hyper` 清回 0。回调写入的节点取回调来源的 hostid，不取 `instance.Hyper`。
- **普通成员怎么知道哪些虚拟机能挂**：`GET /instances` 的每台虚拟机增加 `available_storage_pools`（它所在机器上可用的池的 UUID），挂载弹窗据此把不能挂的置灰并说明原因（§8.3）。

### 5.3 已落盘卷的挂载、卸载

- 已落盘的卷只能挂给同一台机器上的虚拟机（`services/volume.go:447`，不变），下发时带 `existing`。
- 卸载不依赖路径，不变。
- 两者都受 §5.10 的限制。

### 5.4 扩容

- clapi 先检查 §5.10 的限制和卷所在机器上这个池是否可用，不满足直接返回 400。不下发，是为了避免节点上 `pool_guard` 失败、读不到镜像大小而把卷置为 error。
- **准入不写预留**：扩容在请求事务里就把 `size` 改成新值（`services/volume.go:625-628`，现状），新值直接计入已分配；如果再加一条增量预留，增量就被算了两次。
- 下发时额外带上原容量；`resize_volume_local.sh` 拿共享锁、执行 `pool_guard`。失败时优先回报镜像的实际大小，读不到时回报 clapi 传来的原容量，clapi 按现有逻辑回滚大小并保持卷可用（`rpcs/resize_volume.go`）。

### 5.5 删除卷

现在的流程是先在数据库里软删，再异步下发 `clear_volume_local.sh`（`services/volume.go:520-539`），脚本只有一句 `rm -f`、没有回调；节点离线时 `HyperExecute` 只告警、命令被丢弃（`common/clients.go:132-139`），文件就残留下来。改为：

- **未落盘的卷**：直接删记录（现状）。
- **已落盘的卷**：
  1. clapi 检查 §5.10 的限制、卷所在机器在线、池可用，否则返回 400 并说明原因。
  2. 卷置为 `deleting`，提交后下发 `clear_volume_local.sh '<卷ID>' '<卷UUID>' '<绝对路径>' '<池UUID>' '<hostid>'`，接口返回 202。
  3. 节点：拿池的共享锁、`pool_guard`、删除文件，回调 `clear_volume '<卷ID>' 'deleted|error' '<原因>'`（新增回调与处理函数）。
  4. clapi 只在卷为 `deleting`、且回调来自 `volumes.hyper` 这台机器时处理（重复回调幂等）。`deleted`：软删记录；`error`：恢复为 `available`，原因写进 `Reason`。
  5. `deleting` 超过 30 分钟没有回调，改为 `delete_failed`：只允许再次删除，不允许挂载。文件可能其实已经删掉了，挂上去就是一块空盘；§5.2 的 `existing` 参数也会拦住这种情况。
- **配额**：网关把 `DELETE /volumes/:id` 返回的 202 当作成功，立即释放（§8.2）。节点端删除失败的少数情况，卷记录还在，由登录时的对账纠正。
- **`lost` 卷**：只删记录，同步完成（返回 200）。
- **`orphaned` 卷**：不能直接删，先接管，或者先「放弃」（§5.9）。

### 5.6 系统盘放在本地存储池（L4）

- `POST /instances` 的 `storage_pool` 可以指定本地池（L4 之前只接受内置池）。
- **由 clapi 选定宿主机**：
  - 候选机器 = 该池在这台机器上可用、按 §6 准入通过的机器。clapi 在一个事务里锁住候选的节点存储池记录，选剩余空间最多的一台，写 `boot` 预留。
  - 提交后下发 `select=group-…:<这一台> <rcNeeded>`。候选组只有这一台，cland 仍会按 CPU、内存检查它，资源不够时回调 `error=resource`，clapi 释放预留，换下一台候选重试（最多 3 台），都不行就失败。
  - 不交给 cland 在多台里选：用 `select=` 的话，clapi 下发前不知道 cland 会挑哪台，没法事先准入和预留。
- **批量创建**：每一台单独走上面的流程（现在只有第一台按指定节点下发，`services/instance.go:278`，附录 B 第 4 条）。
- **管理员先选宿主机、再选池**：选了宿主机之后，系统盘存储池下拉框只列出这台机器上可用的池和剩余容量，放不下的置灰；准入只针对这一台。指定宿主机要求系统管理员身份（已单独修复，附录 B 第 1 条）。
- `rcNeeded` 里的 `disk`：只计内置池里的系统盘（cland 按节点的 `disk_over_ratio` 判断）；系统盘在其他池时填 0，由 clapi 按池准入。
- 元数据里的 `boot_disk`（GPFS 设计 §6.5）：`driver=local`，`pool_root`、`path`、`nvram` 都在池里。`launch_vm.sh` 先 `pool_guard`，再把镜像从本地缓存转换到这个路径。创建回调写系统盘的 `hyper`，删除 `boot` 预留。
- 重装、救援、捕获镜像、删除虚拟机都使用下发的系统盘路径（L0b）。
- **系统盘所在的池被宣告丢失**：这台虚拟机只能删除（节点上 `destroy` / `undefine`，不删盘文件）。

### 5.7 让虚拟机落在有所需存储池的机器上（L4，可选）

- `POST /instances` 增加可选参数 `required_storage_pools: [{id}, ...]`，候选机器按「这些池都可用」收窄。

### 5.8 宣告丢失与恢复

- **进入**：节点存储池为 `unavailable` 或 `maintenance`，管理员点「宣告丢失」，输入机器名确认。
  - 原因是存储本身（探测失败、阵列失效）时，确认一次即可。
  - **原因是 `node_offline` 时**（节点离线，无法确认存储状态），还要再确认一次，并醒目提示「节点可能只是断网或关机，宣告丢失后池里的卷都将不可用」。
- **池里的卷**：已落盘在这个节点存储池里的卷全部置为 `lost`。`lost` 卷允许：
  - **只删记录**（不下发节点命令），释放配额；
  - **强制卸下**：只从域定义里删掉这块盘（`virsh detach-device --config`，运行中的虚拟机下次重启生效），不访问文件（`POST /volumes/:id/force_detach`）。
- **系统盘在丢失池里的虚拟机**（L4）：见 §5.6。
- **恢复**：`lost` 记录仍接收节点的上报（只更新健康信息，不改状态）。节点报告这个池又健康了，界面显示「节点报告该池已恢复」，管理员可以点「恢复」：
  - 记录回到 `ready`；
  - 池里的 `lost` 卷，文件还在的（探测进程列出的 `volumes/` 文件清单里有）恢复为 `available`，不在的保持 `lost`。
- **强制删除丢失的池**：只卸载，不删文件、不擦盘（§4.5）。
- 这条流程让「单盘池坏了」有出路：否则删卷时挂载保护失败、删池要求池里没卷、坏盘上 `umount` 也会失败，形成死锁。同时误判也能撤回。

### 5.9 节点重新注册与接管

背景：节点离线后无法直接重新部署，只能删除节点再注册（`HyperAdmin.Deploy` 只在 status 4/5 允许重试，`services/hyper.go:341`），注册后得到新的 hostid（work-02 就从 2 变成了 4），被删节点的 hostid 以后不再复用（§3.7）。重装系统时数据盘通常还在（实测 work-02 的 `sdb1` 在 OS reload 后仍然存在）。

**删除节点前的检查**（现在 `HyperAdmin.Delete` 只检查有没有实例，`services/hyper.go:569-576`）：
- 这台机器上还有非内置池的节点记录，或者有 `hyper=这台机器` 的卷时，拒绝删除，提示先迁走、删掉，或者宣告丢失。
- 内置池记录不参与这项检查，删除节点时一并删除。
- 管理员也可以选择**「保留池以便接管」**（`DELETE /hypers/:uuid?keep_pools=true`）：
  - 非内置池里的卷改为 `orphaned`，`hyper` 保持旧 hostid；
  - 内置池里的卷改为 `lost`，因为重装系统后根分区里的文件不会还在，界面上要先列出这些卷再确认；
  - 然后删除节点和它的节点存储池记录。

**接管**：新节点注册后扫描，带 CloudLand 标签或阵列名的盘显示为 `cloudland_pool`（§4.2）。管理员点「接管」：
1. **clapi 校验**：标签里的旧 hostid 不对应任何现存节点。仍然存在的话，说明这是另一台正在用它的机器，或者盘被克隆过，拒绝。
2. **节点执行** `adopt_local_pool.sh <池UUID> <新hostid>`：按阵列名组装 md，必要时 `vgimportdevices`（LVM devices file），`vgchange -ay`，挂载，改写标记文件和 `cloudland_host` 标签，重建 fstab 与 mdadm.conf。回调 `op=adopt`，带布局、成员盘与配对、卷组名、容量，以及 `volumes/` 下的文件清单。
3. **clapi 建节点存储池记录**。这个池里 `hyper=旧hostid`、状态为 `orphaned` 的卷：文件在清单里的，改为新 hostid、状态 `available`；不在清单里的改为 `lost`。删除节点时本来就要求没有实例，所以不存在「接管后仍挂着」的情况。

**放弃接管**：确定原来的盘不会再接回来时，管理员对某个池、某个旧 hostid 的 `orphaned` 卷执行「放弃」（`POST /storage_pools/:id/orphans/abandon {old_hostid, confirm}`），这些卷改为 `lost`，之后可以删记录。

**清除 CloudLand 池的盘**：`cloudland_pool` 盘不能用普通的「清除」。只有 clapi 确认这个池 UUID 在对应的旧 hostid 下已经没有 `orphaned` / `lost` 卷，才允许在建池请求里用 `destroy_pools: [<池UUID>]` 选中这些盘（要求输入池名和机器名，记审计），节点脚本也只对标签等于这些 UUID 的盘放行清除（§4.3）。

### 5.10 进行中操作的互斥

- 虚拟机处于 migrating、rescuing、reinstalling、resizing、provisioning、deleting 时，拒绝挂载、卸载、删除它的卷。现在挂载、卸载只拦「已暂停」和「救援中」（`services/volume.go:413`、`:437`，附录 B 第 11 条）。
  - 迁移期间新挂的卷不在 `--migrate-disks` 里，libvirt 会当它是共享存储，目标端打不开，迁移失败。
  - 切换后、完成前挂载，命令按 `instance.Hyper`（仍是源机器）下发，会在源池里留下不计入已分配的孤儿文件。
- 卷处于挂载中、卸载中、扩容中、删除中时，拒绝对它的其他操作（现状大部分已有）。
- 虚拟机因空间不足暂停（`Reason=storage_full`）时不允许迁移（§6.1）。
- 节点存储池处于 `creating` / `extending` / `removing` 时不接受新分配（§3.2 状态表）。

### 5.11 池的维护

- 管理员对某个节点存储池「进入维护」（`POST /hypers/:uuid/storage_pools/:pool_id/maintenance {enable: true}`，池里有运行中虚拟机的卷时提示）：clapi 置为 `maintenance`，下发命令在节点上写维护标记 `$run_dir/pool-<uuid>.maint`。探测进程见到标记就不再自动挂载，上报 `maintenance`。
- 用途：`xfs_repair`、手工检查、换线。管理员在节点上手工卸载之后，池不会在下一次心跳被挂回去。
- 「退出维护」删除标记，探测进程下一轮尝试挂载，并恢复正常上报。
- 维护期间池不接受新分配；可以宣告丢失，或在池里没卷时删池。

---

## 6. 容量准入

对「机器 H 上的池 P」：

- **已分配** = `storage_pool_id=P`、`hyper=H`、未软删的卷（含 `deleting`、`delete_failed`）的 `size` 之和，加上这条记录上未过期的预留（`boot`、`migration`）之和。
- **准入条件**（全部满足）：
  1. 已分配 + 本次新增 ≤ `CapacityBytes × 超分比例`；
  2. 占用比例（§3.2）< 90%；
  3. 池上没有因空间不足暂停的虚拟机（§6.1）。
- **超分比例**：非内置池取存储池的 `OverRatio`；内置池取节点随上报带来的 `disk_over_ratio`（存在这条记录的 `OverRatio` 上）。不用 `hypers.disk_over_rate`：心跳回调只在内存里给它赋值、不写数据库（`rpcs/hyper_status.go:103-119` 与 `:125-144`），数据库里的值只由 `PATCH /hypers` 写入，可能与节点实际使用的不一致（附录 B 第 10 条）。
- **并发**：准入在一个事务里完成——`SELECT ... FOR UPDATE` 锁住这条节点存储池记录，计算，写卷记录或预留，提交。删池、删除节点时的检查用同一把行锁。
- **容量未知**（还没收到上报）：非内置池拒绝（记录一定来自建池回调，不存在「未知但能用」）；**内置池放行并记日志**，避免部署 L1 后第一次上报之前所有挂载都被拒。
- **迁移有两道**（§7.3）：
  - clapi，目标机器确定时：每个目标池按迁入的虚拟大小汇总准入，写 `migration` 预留。
  - 节点，`source_migration.sh` 复制之前：按**实际数据量**预检目标空间——各盘实际占用之和 + 目标池上其他进行中的迁入量 ≤ 目标池可用空间 − 容量的 10%。在目标池的锁里检查，并写迁入占位文件（`$run_dir/incoming-<迁移ID>`，记录字节数），迁移结束或回滚时删除。
  - 只按虚拟大小准入不够：超分比例大于 1 时，目标池 70% 已用、剩 300 GB，迁入一块虚拟 1 TB、实际 600 GB 的盘也能通过，复制到一半写满，目标池上其他租户的虚拟机会一起暂停。

注意：qcow2 精简置备，预建的空盘只占几 KB，**空间不够并不会在预建时失败**，要到复制途中才报错。所以迁移前的两道检查都不能省。

### 6.1 池快满与写满

准入只管新的分配。卷是精简置备的 qcow2，已有的卷会随虚拟机写入继续变大，准入挡不住。下面按「预警 → 挡住新分配 → 回收 → 写满 → 腾空间」分层处理，除标明 L3 的以外都在 L1 完成。

**1. 预警**

- 现有的「计算节点」告警按 node_exporter 覆盖所有非 tmpfs / overlay 的文件系统（`compute-core-resources.yml.j2:27`，剩余空间低于 `disk_space_threshold`，默认 10%，持续 `disk_alert_duration` 分钟），池的挂载点自动包括在内。L1 要做的：确认 node_exporter 没有把 `/opt/cloudland/pools/` 排除；告警描述里加上 `mountpoint`（第 35 行现在只写 `device`，看不出是哪个池）。这条规则要管理员建了才有，「存储」标签页在没建时给出提示。
- L3 增加「本地存储池」告警类型，按 §4.7 的 textfile 指标对池状态（`degraded` / `unavailable`）和占用比例告警。
- 界面：容量条按占用比例在 80% 变黄、90% 变红；已分配超过总量时加「超分」标记。
- 上报：占用比例跨过 80%、85%、90% 时立即上报（§4.7）。

**2. 挡住新的分配**

- 数据卷：§6 的三个条件。
- 系统盘（L4 之前都在内置池）：创建虚拟机、自动选目标的迁移在取候选机器时，排除内置池占用达到 90% 的机器。
  - `GetHyperGroup`（`common/instance.go:32`）也被负载均衡的主备选点使用（`services/loadbalancer.go:162`、`rpcs/set_vrrp_ip.go:122`），负载均衡与磁盘无关。所以给它加一个用途参数，只在创建虚拟机和迁移时按磁盘过滤。
  - 管理员指定宿主机或目标机器时不经过它，按 §6 对指定的机器准入。
  - 迁移接口另有 `ignore_capacity`（系统管理员，记审计），用于所有机器都快满、又必须把虚拟机撤离某台机器的情况。
  - 所有候选都被排除时，报「没有可用的计算节点：内置存储空间不足」。L4 起系统盘按池准入，由 §5.6 覆盖。

**3. 回收：让虚拟机里删掉的数据还给池**

- 现在的磁盘 XML 没有 `discard`，虚拟机里删文件、执行 `fstrim` 都不会让 qcow2 文件变小，池的占用只增不减。
- 在实际使用的模板上给本地 qcow2 盘加 `discard='unmap'`：系统盘模板 `template_with_qa.xml`、`template_uefi_with_qa.xml`，数据盘模板 `volume.xml`。`template.xml`、`template_uefi.xml` 没有任何引用，L0a 删除。虚拟机里的 `fstrim`（Ubuntu 默认每周执行一次）、Windows 的「优化驱动器」（默认每周一次）会把空间还给池。
- **生效范围**：新建虚拟机的系统盘、此后新挂载的数据盘。**存量虚拟机不会自动带上**：重装是在现有域定义上修改（`reinstall_vm.sh:35-37`、`:210`），迁移原样带走域定义，都不按模板重建。提供可选的运维脚本 `enable_discard.sh <实例ID>`，给现有域定义补上 `discard` 并重新 `virsh define`，下次冷启动生效。
- **回收粒度**：数据卷按 `cluster_size=2M` 创建（`attach_volume_local.sh:15`），只有整块、对齐的 2 MiB 空闲区才能还给池，大量零散的小文件删除后回收有限。迁移预建目标盘时沿用源盘的簇大小（现在预建用默认的 64 KiB，§7.3）。

**4. 写满时**

- 磁盘 XML 显式写 `error_policy='enospace'`：空间不足时暂停虚拟机，其他 I/O 错误照常报给虚拟机。这就是 QEMU 的默认行为（`werror=enospc`），写明是为了不依赖默认值。暂停不会写坏数据，恢复后被卡住的写入会重做。
- **识别原因**：探测进程用 `virsh domblkerror <域>` 列出出错的盘和原因。**出错的盘都在本地池、原因都是空间不足（no space）时**，才判定为存储空间不足。不能只看「因 I/O 错误暂停」：GPFS 盘用 `error_policy='stop'`（GPFS 设计 §5.3），任何 I/O 错误都会暂停虚拟机，同时挂着本地盘和 GPFS 盘的虚拟机会被误判。
- **上报**：`inst_status` 把这类虚拟机报成 `paused_nospace`，clapi 记为「已暂停」、`Reason=storage_full`。同时把 `inst_status` 与上次列表的比较改为整行精确匹配（`grep -qxF`，现在是第 86 行的子串匹配）：否则 `paused_nospace` 变回 `paused` 不会上报，`5 …` 还会被旧列表里的 `15 …` 命中（附录 B 第 12 条）。
- **自动恢复**（在池的探测进程里做，不阻塞心跳）：
  - 条件：这台虚拟机出错的盘所在的池都满足「可用空间 ≥ min(容量的 15%, 50 GiB)」。设绝对值兜底，是因为大池按百分比要腾出几百 GB 才恢复，而暂停的虚拟机往往只差一点空间。
  - 每台每 5 分钟最多尝试一次；有进行中作业（`virsh domjobinfo` 不为 None）的跳过；`virsh resume` 加超时。
  - 恢复后照常经 `inst_status` 报 `running`，clapi 清掉 `Reason` 并记日志（不是经接口发起的操作，不进审计）。
  - 只恢复这一类暂停的；用户自己暂停的不碰。
- **手工恢复**：沿用现有的「恢复」操作（`PATCH /instances/:id` 的 `power_action=resume`），有审计。
- **池上有这类暂停的虚拟机时，拒绝这个池的新分配**（§6 条件 3）：否则管理员刚腾出的空间会先被新卷占掉，暂停的虚拟机迟迟恢复不了。
- **这类虚拟机不允许迁移**：暂停状态的域迁移后在目标端仍是暂停，而原因不再是空间不足，自动恢复识别不到（`source_migration.sh:144-145` 只在迁移前是运行状态时才在目标端恢复）。先腾出空间让它恢复，或者先关机再迁移。
- 界面：池的一行显示「N 台虚拟机因空间不足暂停」；虚拟机列表和详情在「已暂停」旁显示「存储空间不足」。

**5. 腾出空间**

- **统计占用**：节点存储池增加「统计占用」操作，下发 `pool_usage.sh <池UUID|builtin>`（经 `async_exec`，加超时）。
  - 脚本自己按池确定目录：非内置池为 `/opt/cloudland/pools/<UUID>/volumes`，内置池为 `$image_dir` 与 `$volume_dir`。只接受合法的 UUID 或 `builtin`，不接受路径参数。
  - 用 `stat` 列出每个文件的实际占用，回调 `pool_usage '<NODE_ID>' '<池UUID|builtin>' '<base64 JSON>'`。
  - clapi 把文件对应到卷、虚拟机和组织，存进 `UsageReport`，界面按实际占用列出前 20 个。
- **腾空间的手段**：在线加盘（§4.4，L3）；把其他虚拟机迁到别的机器（L1 同池，L2 可换池；因空间不足暂停的除外）；删除不用的卷；让虚拟机里执行 `fstrim`（带 `discard` 的盘才有效）。
- **内置池写满更危险**：它是根分区，写满会影响宿主机本身（日志、libvirt、cloudlet、镜像缓存下载）；all-in-one 的控制节点上，根分区还放着控制面的数据库和监控数据，界面上注明「与控制面共用」。有了非内置池以后，建议数据卷不再放进内置池（把默认池改成非内置池之前，先确认每台机器都有它，§3.1）。

---

## 7. 迁移

迁移接口只允许系统管理员调用（`services/migration.go:29`）。

### 7.1 目标机器与迁移计划

**每块本地盘的目标池**：

| 情况 | 目标池 |
|---|---|
| 请求里为这块盘指定了 `storage_pool` | 用指定的池，必须在目标机器上可用 |
| 没有指定，目标机器上有同一个池且可用 | 留在同一个池 |
| 没有指定，目标机器上没有原池，但有**同一个可互换组**的池（L2） | 自动替换（`allow_pool_fallback=false` 时按下一行处理） |
| 以上都不满足 | 失败，列出这块盘在目标机器上可选的池 |

- **可互换组**（`FallbackGroup`）：只在组名相同、且都不为空的池之间自动替换；多个候选时选剩余空间最多的。内置 `local` 池不参与。用可互换组而不是「介质相同」，是为了不和「用不同的池做隔离」冲突（§2.2）。
- **可见性**：自动替换写进计划并附上原因（例如「原池 local-hdd-a 在目标机器上不存在，替换为同组的 local-hdd-b」），迁移详情里可以看到。
- `allow_pool_fallback` 是请求参数，默认 true。迁移只有系统管理员能发起，不必做成系统设置。
- **逐盘指定目标池（`disks`）必须同时指定目标机器**：不指定目标时，无法确认所选的池在哪台机器上可用。
- 因空间不足暂停的虚拟机不允许迁移（§6.1）。

**计划与准入的时机**：

- **指定了目标机器**：创建迁移时就生成计划、准入、写预留，失败直接返回 400。
- **不指定目标机器**（包括维护模式批量迁移）：
  1. clapi 先算两组候选机器，按虚拟大小预筛（§6 的条件）：
     - G1：每块本地盘都能留在原池；
     - G2（L2）：每块盘都能留在原池，或按可互换组替换。
  2. G1 不为空就用 `select=group-...:<G1>` 下发 `target_migration.sh`，否则用 G2，都为空就失败。这样实现了「优先不换池」，又不需要 cland 支持优先级。
  3. `target_migration.sh` 只准备网络和元数据，不碰磁盘；它结束时回调 `target_prepared`（`rpcs/migrate_vm.go:388-447`）。
  4. 此时目标已确定，clapi 生成计划（换池时按可互换组在这台机器上选池）、准入、写预留；**通过后才下发 `source_migration.sh`**，并带上计划和目标机器的 hostid。不通过（例如刚好被别的请求占满）就走现有的回滚流程，由 `clear_target_migration.sh` 清理目标端已准备的网络资源。
- **磁盘的检查和预建留在 `source_migration.sh` 里，经 ssh 在目标上完成**（§7.3），不挪到 `target_migration.sh`：不指定目标时，`target_migration.sh` 运行时计划还不存在，它结束时发出的正是 `target_prepared`。`pool_guard` 接受「期望的 hostid」参数（§4.6），所以经 ssh 执行也能校验目标池的归属。

**维护模式批量迁移**：现在整批放在一个事务里，逐台下发，一台失败就整体回滚，而前面已经下发的命令收不回（`services/migration.go:35-40`、`:106-110`、`:190`）。改为先对所有虚拟机算候选组，没有候选的跳过（沿用现有的 `not_doing` 做法），其余再逐台下发，每台各自一个事务；`Maintain` 返回每台虚拟机的结果和原因（现在只返回一个 error，`services/hyper.go:534-541`）。

### 7.2 路径

- 目标路径 = 目标池根目录 + 这类盘在池里的相对路径（§2.3）。
- 留在同一个池时，源路径和目标路径相同，与现在的行为一致。
- 换池时路径不同，按 §7.3 改写。

### 7.3 执行流程

1. **`target_migration.sh`**：不变，只准备网络与元数据。
2. **clapi**：生成计划、准入、写预留（§7.1）；下发 `source_migration.sh` 时带上计划和目标机器的 hostid。
3. **`source_migration.sh` 按计划逐盘处理**（磁盘清单读计划，不再从 `virsh domblklist` 里挑出所有文件型磁盘，第 49 行）：
   - ssh 到目标执行 `pool_guard <目标池根目录> <目标池UUID> <目标hostid>`（内置池除外）；目标路径已存在就失败。
   - 空间预检（§6）：在目标池的锁里比较实际数据量与可用空间，写迁入占位文件。
   - 运行中的虚拟机：在目标上预建空 qcow2，虚拟大小取 `qemu-img info -U` 读出的精确字节数（现状，第 66 行），簇大小与源盘相同。
   - 关机的虚拟机：用 `rsync --sparse` 复制到计划里的目标路径。现在用 `scp`，会把稀疏文件的空洞全部写实，`discard` 回收出来的空间在目标端又被占满。
   - 不再 `mkdir -p $(dirname $path)`（第 64、67 行）：池没挂载时这一步会把磁盘写进根分区。池里的 `volumes/` 等子目录建池时已经建好。
4. **改写域定义**（L2，只在有盘换池时）：源端 `virsh dumpxml --security-info --migratable`，替换换池的盘的 `<source file>`，先用 `virt-xml-validate` 校验，通过后热迁移加 `--xml <新配置> --persistent-xml <新配置>`（第 123 行），冷迁移加 `--persistent-xml <新配置>`（第 118 行）。libvirt 允许目标配置里的磁盘源路径与源端不同（L2 开工前实测，§13）。
5. **数据盘的描述文件**（L2）：`$xml_dir/<实例>/disk-<卷ID>.xml` 里写着磁盘路径，现在原样复制到目标（第 79 行）；换池的盘先替换路径再复制。
6. **NVRAM**（L4）：放在池里的 NVRAM **不论是否换池都按计划复制和清理**，换池时第 4 步一并改写 NVRAM 路径。现在只复制 `$image_dir` 下的（第 81 行），目标端的模板也只放在 `$image_dir`（`target_migration.sh:37`）。
7. **完成**：`async_job/complete_migration.sh:24` 在目标上重新保存域定义。clapi 收到 `completed` 后，在一个事务里更新每块盘的 `storage_pool_id`、`path`、`hyper`（现在只更新 `hyper`，`rpcs/migrate_vm.go:306-311`），删除迁移预留；目标端的迁入占位文件随后删除。
8. **源端清理**：`finish_source_migration.sh` 按计划删除源路径的文件（现在按 `$volume_dir/*`、`$image_dir/*` 前缀删，第 31–33 行，**池里的文件不会被删**），保留「源机器上还定义着这台虚拟机就跳过清理」的保护（第 20–23 行）。
9. **回滚**：`async_job/clear_target_migration.sh` 按计划删除目标路径上预建的文件和 NVRAM（现在第 48 行按前缀删、第 51 行按固定文件名删 `inst-N.disk`、第 39 行删 `$image_dir` 下的 NVRAM），删除迁入占位文件；clapi 删除迁移预留。

阶段划分：第 1、2、3、7、8、9 步在 **L1**（同池迁移）就要做，否则同池迁移后源端池里残留文件，迁回原机器或失败后重试都会报「目标上已存在同名文件」。第 4、5 步是 **L2**（换池）。第 6 步是 **L4**。

### 7.4 其他规则

- 有本地盘的虚拟机仍然不允许 `force`，源机器必须在线。
- 迁移进度不受影响（`domjobinfo` 统计全部复制量）。
- 热迁移经 QEMU 的块复制把磁盘写到目标，零块在目标端是否仍保持稀疏要实测（§13）；如果不保持，预检按实际数据量估算的空间会偏少。
- 同一台机器上换池不走迁移流程，见 L5。

---

## 8. 接口与前端

### 8.1 clapi 接口

**约定**：
- **节点路由参数**：clapi 的节点路由是 `/hypers/:uuid`（`apis/routes.go:155-159`），新增的子路由沿用 `:uuid`，否则 gin 注册时与已有路由的通配名冲突，启动即 panic。网关白名单里同一路径写作 `/hypers/:hostid`（`cpgateway/src/apis/proxy_routes.go:138-143`），传的也是节点 UUID。clapi 先按 UUID 查出节点，再用它的 hostid 拼 `inter=`。审计键按 `:uuid` 写。
- 请求里表示存储池一律用 `storage_pool: {id}`（值为池的 UUID）。
- 新增的容量字段都以字节为单位，字段名带 `_bytes`。

**新增接口**：

| 接口 | 权限 | 说明 |
|---|---|---|
| `GET /storage_pools` | 所有成员 | 成员看到 GPFS §10.1 规定的字段，外加 `media`、可用机器数，只列 `active` 的池；系统管理员看到全部字段与合计容量 |
| `POST / PATCH / DELETE /storage_pools[/:id]` | 系统管理员 | 存储池增删改（L1 新建，先只支持 `driver:"local"`）。可改 `name`、`media`、`fallback_group`、`over_ratio`、`status`、`is_default`、`description`；`mount_path` 自动生成 |
| `GET /storage_pools/:id/hypers` | 系统管理员 | 每台机器的节点存储池记录（字段同 `GET /hypers/:uuid/storage_pools`） |
| `POST /storage_pools/:id/orphans/abandon` | 系统管理员 | 放弃接管 `{old_hostid, confirm}`（§5.9） |
| `POST /hypers/:uuid/disks/scan`、`GET /hypers/:uuid/disks` | 系统管理员 | 扫描与结果。结果含每块盘的全部字段；`cloudland_pool` 盘另附 clapi 里这个池在该旧 hostid 下的 `orphaned` 卷数（接管弹窗要用） |
| `PATCH /hypers/:uuid/disks/:id` | 系统管理员 | `{media:"ssd"}` 手工指定介质，`{media:""}` 恢复自动识别（§2.6）。`:id` 是 `hyper_disks` 的记录 ID（盘标识里可能有 `/`，不适合放进路径） |
| `GET /hypers/:uuid/storage_pools` | 系统管理员 | 这台机器上的节点存储池（含内置池）：状态、原因、`last_op`、布局、成员盘与配对、`capacity_bytes`、`used_bytes`、`avail_bytes`、`own_bytes`、`allocated_bytes`、`reserved_bytes`、`usage_ratio`、卷数、因空间不足暂停的虚拟机数、同步进度、是否在维护，以及最近一次上报的健康信息；另附原因为 `storage_pending` 的虚拟机列表（§4.8） |
| `POST /hypers/:uuid/storage_pools` | 系统管理员 | 建池 `{storage_pool, layout, disks:[id...], wipe, allow_media_mismatch, destroy_pools, confirm}`。对建池失败的 `error` 记录再次提交时复用这一行 |
| `POST /hypers/:uuid/storage_pools/:pool_id/extend` | 系统管理员 | 加盘 `{disks, wipe, allow_media_mismatch, confirm}`（L3） |
| `POST /hypers/:uuid/storage_pools/:pool_id/replace_disk` | 系统管理员 | 换盘 `{failed_disk, new_disk, wipe, allow_media_mismatch, confirm}`（L3，§4.9） |
| `POST /hypers/:uuid/storage_pools/:pool_id/maintenance` | 系统管理员 | `{enable}`（§5.11） |
| `POST /hypers/:uuid/storage_pools/:pool_id/lost` | 系统管理员 | 宣告丢失 `{confirm, node_offline_ack}`；原因为 `node_offline` 时必须带 `node_offline_ack=true`（§5.8） |
| `POST /hypers/:uuid/storage_pools/:pool_id/restore` | 系统管理员 | 把 `lost` 恢复为 `ready`（§5.8） |
| `POST /hypers/:uuid/storage_pools/adopt` | 系统管理员 | 接管 `{storage_pool, confirm}`（§5.9） |
| `DELETE /hypers/:uuid/storage_pools/:pool_id` | 系统管理员 | 删池 `{confirm}`。`force=true`：有残留文件时删掉文件后继续；丢失的池只卸载。建池失败的 `error` 记录直接删除记录（节点已回滚） |
| `POST /hypers/:uuid/storage_pools/:pool_id/usage`、`GET` 同一路径 | 系统管理员 | 触发「统计占用」与读取结果（§6.1） |
| `POST /volumes/:id/force_detach` | 卷所属组织的写权限 | `lost` 卷强制卸下（§5.8） |
| `GET /instances/:id/migration_targets` | 系统管理员 | 候选目标机器，以及每块本地盘能否留在原池、可替换的池、全部可选的池和剩余容量 |

**已有接口的变化**：

| 接口 | 变化 |
|---|---|
| `POST /volumes` | 增加 `storage_pool` |
| `GET /volumes`、`GET /volumes/:id` | 增加 `storage_pool`、`reason`；系统管理员另外看到所在机器 |
| `DELETE /volumes/:id` | 已落盘的卷返回 202，卷先进入 `deleting`（§5.5）；`lost` 卷同步删除记录（200）；`orphaned` 卷拒绝 |
| `GET /instances`、`GET /instances/:id` | 增加 `available_storage_pools`（§5.2）；`reason` 增加 `storage_full`、`storage_pending` |
| `POST /instances` | L4 增加系统盘的 `storage_pool` 与 `required_storage_pools`；指定 `hypervisor` 要求系统管理员（已单独修复，附录 B 第 1 条） |
| `POST /migrations` | 增加 `disks: [{volume:{id}, storage_pool:{id}}]`（必须同时指定目标机器）、`allow_pool_fallback`、`ignore_capacity` |
| `GET /migrations/:id` | 增加 `disk_plan` |
| `POST /hypers/:uuid/maintain` | 返回每台虚拟机的迁移结果 |
| `DELETE /hypers/:uuid` | 增加对节点存储池和卷的检查，以及 `keep_pools=true`（§5.9） |
| `GET /hypers`、`GET /hypers/:uuid` | 磁盘改为按这台机器的节点存储池（含内置池）汇总：`disk_total`、`disk_allocated`、`disk_used`（GB，与现有字段单位一致），`disk_max_usage_ratio`（各池里最高的占用比例，进度条按它上色），另加 `storage_pools[]`（每个池的名称、各项字节数、占用比例、状态）；原来来自 `hyper_status`、乘过超分的 `disk` 不再返回（§8.3） |

**建池、加盘、换盘所选的盘**：必须在 24 小时内的扫描结果里是 `free`，或者 `dirty` 且 `wipe=true`，或者 `cloudland_pool` 且其池 UUID 在 `destroy_pools` 里（§5.9）；`shared`、`unknown_member`、`system`、`in_use` 一律不行。`raid1` 的盘数为偶数，`single` 只能一块。介质与容量的检查见 §2.6，不一致时返回 400，响应里列出不一致的盘及其介质。

### 8.2 cpgateway

- `proxy_routes.go` 加入新增的路由（节点相关的沿用 `:hostid` 写法）：`GET /storage_pools` 与 `POST /volumes/:id/force_detach` 所有成员可用，其余新增路由标记为系统管理员。已有路由的标记不变（`POST /instances`、`/migrations` 等仍由 clapi 校验权限）。
- **配额**：`FinishQuota`（`services/quota.go:605-627`）现在只在 200 / 201 / 204 时结算，删除类规则要把 202 也当作成功，否则 `DELETE /volumes/:id` 改为异步后磁盘配额不会释放。节点端删除失败的少数情况由登录对账纠正（每个组织-区域一小时内最多对账一次）。
- 配额仍只有一个 `disk_gb`。

### 8.3 前端

- **计算节点详情页「存储」标签页**：
  - 「磁盘」表格：名称、标识、型号、容量、传输类型、介质（手工指定的加「手工」标记，悬停显示自动识别的原值；行内可修改）、状态（空闲 / 有旧数据 / 使用中 / 系统盘 / CloudLand 池（可接管）/ 未知成员 / 疑似共享）、所属的池。「扫描」按钮。
  - 「存储池」表格：池名称、介质、布局（「单盘」「线性（无冗余）」「RAID1」）、成员盘、容量条（已用 / 已分配 / 预留 / 总量）、状态（降级用警告色，同步中显示进度，维护中单独标注）、卷数、因空间不足暂停的虚拟机数。
    - 容量条按占用比例在 80% 变黄、90% 变红；已分配超过总量时加「超分」标记；没建「计算节点」告警规则时给出提示（§6.1）。
    - 操作：加盘、换盘（L3）、统计占用、维护、删除、宣告丢失、恢复（`lost` 且节点报告已恢复时）。
    - 内置池一行只能统计占用，其他操作不可用；控制节点上标注「与控制面共用」。
  - 「待启动的虚拟机」：开机后因池不可用而没有启动的虚拟机及原因（§4.8）。
  - 「添加存储池」弹窗：选池、勾盘、选布局；「线性」标出无冗余；每块盘显示生效介质，与池的标签或与其他所选盘不一致的标红，要勾选「确认使用介质不一致的盘」才能提交（提交时带 `allow_media_mismatch`）；`raid1` 按对显示，给出每对的可用容量，容量不一致时提示浪费多少；选中有旧数据的盘时单独列出将被清除的盘；最后输入机器名。「加盘」「换盘」弹窗同样处理。
  - 「接管」弹窗：列出 `cloudland_pool` 盘所属的池、原节点 ID、clapi 里等待接管的卷数，确认后接管。
  - 「删池」「宣告丢失」弹窗：输入机器名；宣告丢失时如果原因是节点离线，再确认一次并醒目提示（§5.8）。
- **磁盘容量的显示口径（全站统一）**：一律显示「原始容量 + 已分配」，不乘超分比例（超分只用于准入，§6）。
  - 「磁盘」表格：单块物理盘的原始大小。
  - 节点存储池的容量条：总量取 `CapacityBytes`（内置池为 CloudLand 最多能用到的量，§3.2），已用、已分配、预留，颜色按占用比例。
  - 存储池管理页：各机器的合计。
  - **计算节点列表的「磁盘」列**：这台机器所有节点存储池（含内置池）的合计，显示「已分配 / 总量」；进度条颜色按**最满的那个池**的占用比例（`disk_max_usage_ratio`），否则根分区 95% 加一个空的 2 TB 池会显示成 30% 左右，掩盖风险；悬停时按池列出明细。原来这一列显示的是 `hyper_status` 上报的值：只看根分区、乘过超分比例、可用值只减去系统盘的虚拟大小，建池之后完全反映不出新加的盘，而且与存储池的数字口径不同。
  - **计算节点详情「资源容量」卡片**：磁盘一行按池分行显示（池名、已分配 / 总量、状态），并链接到「存储」标签页。
  - `hyper_status` 里的磁盘值 clapi 不再展示，只有 cland 调度系统盘时用它自己那份（cland 的资源表不经过 clapi）。
- **「存储池」管理页**（L1 新建，GPFS 以后加入同一页面）：本地池显示「本地」标签、可互换组、`x/y` 台机器具备、合计容量；详情页列出每台机器；等待接管的卷可以「放弃」（§5.9）。
- **创建云硬盘**：存储池下拉框，显示「本地 / 共享」、介质；系统管理员另外看到具备这个池的机器，普通成员只看到可用机器数。
- **挂载弹窗**：未落盘的卷，按 `available_storage_pools` 把所在机器没有这个池或池不可用的虚拟机置灰并说明原因；处于迁移、救援等状态的虚拟机也置灰（§5.10）；挂载提示按池显示。
- **云硬盘列表、详情**：存储池、原因；系统管理员另外看到所在机器；`deleting`、`delete_failed`、`lost`、`orphaned` 状态的文案与操作（`lost` 只有「删除记录」「强制卸下」，`delete_failed` 只有「删除」，`orphaned` 没有操作并说明「等待接管」）。
- **虚拟机列表、详情**：`Reason=storage_full` 时，「已暂停」旁显示「存储空间不足」，悬停说明「空间回落后自动恢复」；`storage_pending` 时显示「等待存储就绪」。
- **创建虚拟机**（L4）：系统盘存储池；管理员选了宿主机时只列这台机器上的池；可选的「还需要这些存储池」。
- **迁移弹窗**（L2）：选目标机器后逐盘显示目标池，默认「留在原池」；目标机器没有原池时预填可互换组里的池并标注「自动选择」；每项显示剩余容量，放不下的置灰；有盘无处可去的机器置灰并说明是哪块盘。因空间不足暂停的虚拟机不能迁移并说明原因。**迁移详情**显示计划。
- **维护模式**：显示每台虚拟机的迁移结果。
- 三种语言的文案，通过 `npm run i18n:check`。

### 8.4 审计

`audit_actions.go` 增加（审计键按 clapi 的 `:uuid` 路由写）：

| 动作 | 记录的附加信息 |
|---|---|
| `storage_pool.create` / `update` / `delete` | — |
| `storage_pool.orphans_abandon` | 旧 hostid、卷数 |
| `hyper.disks_scan` | — |
| `hyper.disk_update` | 手工指定的介质 |
| `hyper.storage_pool_create` | 盘的标识、是否清除、`destroy_pools`、是否放行了介质不一致 |
| `hyper.storage_pool_extend` / `replace_disk` | 盘的标识、是否清除、是否放行了介质不一致 |
| `hyper.storage_pool_maintenance` | 进入还是退出 |
| `hyper.storage_pool_lost` | 原因、是否在节点离线时宣告 |
| `hyper.storage_pool_restore` | 恢复的卷数 |
| `hyper.storage_pool_adopt` | 旧 hostid、恢复的卷数、转为 lost 的卷数 |
| `hyper.storage_pool_delete` | 是否 `force`、删掉的残留文件清单 |
| `volume.force_detach` | — |

- `hyper.delete` 记录是否 `keep_pools`；迁移记录 `ignore_capacity`，换池信息在 `disk_plan` 里。
- **有意不加动作映射**：「统计占用」（`POST .../usage`，只读统计，仍进审计日志，但不进组织动态）。自动恢复不是经接口发起的，不进审计，只记日志（§6.1）。

---

## 9. 安全与风险

| 风险 | 应对 |
|---|---|
| 格式化了错误的盘 | 稳定标识；节点执行前按同一套规则重新检查；`system`、`shared`、`unknown_member` 永远不能清除；有签名的盘要显式 `wipe`；输入机器名；审计 |
| 重装系统后，CloudLand 的池被当成旧数据清除 | 扫描直接读 PV 上的标签（绕过 LVM devices file）和 md 超级块上的阵列名，识别为 `cloudland_pool`；读不出来的 LVM / md 成员归为 `unknown_member`，都不能用普通清除（§4.2、§5.9） |
| 共享 LUN 被当成空闲盘 | fc / iscsi、多路径、重复 WWN 的盘归为 `shared`，不能选；接 GPFS 前加 NSD 检测 |
| 坏盘拖垮节点（D 状态、心跳卡住、命令队列堵塞） | 探测在每个池的后台进程里做，心跳只读状态文件；探测卡住就报 `unavailable` 且不再堆积；挂载按退避重试；锁按池拆分且全部 `flock -w`；删除丢失的池用 `umount -l`、走异步 |
| 池没挂载时卷写进根分区 | `pool_guard`；不对池根目录 `mkdir -p`；迁移时源端经 ssh 在目标上先 `pool_guard` |
| 线性池或单盘池坏盘，卷全丢 | 建池时明确选择布局并醒目提示；推荐 RAID1；本地卷不支持备份，界面要说清；宣告丢失流程保证不死锁（§5.8） |
| 误宣告丢失，把完好的数据弄丢 | 节点离线时宣告要二次确认；`lost` 可以恢复；删除丢失的池只卸载、不删文件、不擦盘（§5.8） |
| 接管到错误的机器 | 要求旧 hostid 不对应任何现存节点；只改指在文件系统里确实找到文件的卷；hostid 不复用（§3.7、§5.9） |
| RAID1 降级没人发现 | 上报 `degraded` 与同步进度；L3 接入告警；换盘流程（§4.9） |
| 并发请求超分 | 事务内锁行准入；首次挂载、扩容直接计入，迁移、系统盘用预留（§6） |
| 精简置备写满 | 准入 + 90% 占用阈值；迁移按实际数据量预检；80% / 90% 预警；`discard` 回收；写满时暂停而不写坏，显示原因，空间回落后自动恢复；池上有这类暂停的虚拟机时挡住新分配；内置池快满的机器不再分到新虚拟机（§6、§6.1） |
| 开机时池没挂好 | fstab `nofail` + 更长的设备等待；探测进程按退避重试挂载；待启动列表，不阻塞心跳；开机时关闭所有域的自启（§4.8） |
| 心跳把管理员手工卸载的池挂回去 | 维护标记；挂载前拿池锁（§5.11、§4.7） |
| 盘名变化 | 稳定标识；fstab 用文件系统 UUID；mdadm.conf 与阵列名 |
| 盘从一台机器搬到另一台 | 卷组名带随机后缀，不会重名；`cloudland_host` 不符时为 `cloudland_pool`，需要接管 |
| 建池做到一半失败或节点离线 | 失败回滚；`creating` / `removing` 超时转 `error`，可以重试或删除记录；`extending` 失败回到原状态 |
| 删池时有写入 | 写操作拿池的共享锁，删池拿排他锁；clapi 在同一把行锁里检查卷、预留、进行中的迁移并置为 `removing` |
| 换池迁移改写配置出错 | 只改 `<source file>` 和 NVRAM；`virt-xml-validate` 校验；L2 验收覆盖热迁移、冷迁移和 UEFI 虚拟机 |
| 删卷命令或回调丢失 | 删卷改为回调确认；超时转 `delete_failed`，不许挂载；挂载时 `existing` 卷缺文件就报错，不静默新建空盘（§5.2、§5.5） |
| 回调丢失（cland 只重试 3 次，间隔 1、2 秒） | 各回调按状态机幂等处理；所有「等待回调」的状态都有超时；首次挂载在请求事务里写 `hyper`，不依赖回调 |
| 介质识别错误或混用 | 后端检查盘与池、盘与盘的介质，不一致要显式确认并记审计；识别错误可以手工纠正；RAID1 容量不一致给出提示（§2.6） |
| 节点命令参数被注入 | 节点脚本只接受 UUID、hostid、布局名等受限值，根目录由脚本自己拼出；所有参数照现有约定用 `ShellEscape` 包起来 |

---

## 10. 部署与现有环境

- `deploy-compute-node.sh` 与 Ansible 的 hyper 角色安装 `lvm2`、`mdadm`、`xfsprogs`、`rsync`，创建 `/opt/cloudland/pools`（L1）。**不自动建池**（部署时自动建池放在可选的 L5）。
- 部署脚本第 346 行 `chown -R cland:cland "$CLOUDLAND_DIR"` 会深入挂载在 `/opt/cloudland/pools/` 下的池（现在对 `cache/` 下的虚拟机磁盘也是如此）。GNU `chown` 没有 `--one-file-system` 选项，改为 `find "$CLOUDLAND_DIR" -xdev -exec chown cland:cland {} +`，或者只 chown 脚本自己建的目录（L1）。
- 本地池功能只在 `volume.driver` 为空或 `local` 时启用；配置了其他驱动时，clapi 不创建内置池、不做回填，存储池接口返回「未启用」（§1.3）。
- **现有三台机器**（L1 验收时）：
  - work-01、work-03：`sdb1` 挂在 `/disk1`，扫描会显示为「使用中」。先卸载并删掉 fstab 里的对应行，再在界面上「清除 + 建池」。
  - work-02：`sdb1` 没挂载，显示为「有旧数据」；**先确认里面没有需要保留的数据**。清除时按 §4.3 第 2 步先擦分区再擦整盘。
  - 每台只有一块空闲盘，只能用单盘布局（无冗余）。
  - **已按此执行**（2026-09-22）：三台的 `sdb` 都建成了池 `local-hdd`（单盘）并保留作为测试环境；work-01 / work-03 的 `/disk1` 那行 fstab 已注释掉（原文件备份在各自的 `/root/fstab.before-storage-test`）。

---

## 11. 实施阶段

> **L0a–L4 已于 2026-09-22 实施完成**（L5 未做）。各阶段的验收项哪些跑过、哪些没跑，见 §14.4 / §14.5；与本章不同的做法见 §14.2。

顺序：**L0a → L1 → L2 → L3 → L0b → L4 → L5**。L0 拆成两部分：数据盘相关的抽象先做（L0a），系统盘、救援、重装、捕获镜像和 WDS 清理（L0b）等到系统盘要放进池（L4）之前再做，这样 L1–L3 不必等 WDS 的去留决定。

### 阶段 L0a：存储池抽象（数据盘部分）

**范围**：
- `storage_pools`，启动时创建内置 `local` 池；`volumes.storage_pool_id` 回填。
- `volumes.path` 改写存量记录（数据盘加 `volume/` 前缀，系统盘改为 `instance/inst-<实例ID>.disk`）；系统盘的 `hyper` 在创建回调里写，并回填存量（§3.4）。
- 数据盘相关的脚本（`attach_volume_local.sh`、`resize_volume_local.sh`、`clear_volume_local.sh`）和迁移脚本改为使用 clapi 下发的路径；迁移、删除用的卷 JSON（`rpcs/migrate_vm.go:45-60` 等处）带上驱动和路径。
- `report_rc.sh:257` 改用 `du -x`（本地池挂在根文件系统下之后，不加 `-x` 每次心跳都要遍历池目录；即 GPFS 设计附录 B 第 3 条的前半）。
- 删除没有任何地方下发的脚本（`attach_vol.sh`、`detach_vol.sh`、`create_volume_from_image.sh`、`create_volume_local.sh`）和没有引用的模板（`template.xml`、`template_uefi.xml`）。
- 卷的响应里增加 `storage_pool`。
- `volume.driver` 不是 `local` 时不启用（§10）。

**不包含**：心跳和调度里的磁盘单位与 NVRAM 解析问题（附录 B 第 3、6、7 条）。三者修正后都会改变调度结果（有的让可用磁盘变少，有的变多），要先核对各节点的超分比例和剩余空间，单独评估后再做。clapi 对各池的准入用的是探测进程上报的原始 `df` 字节数（§4.7），不受这几个问题影响。

**验收**：
- `test-items/` 里卷与迁移相关的用例全部通过。
- 用脚本逐条核对：每个卷都有 `storage_pool_id`；每个已落盘卷的路径都与所在节点上的实际文件一致；每个系统盘都有 `hyper`。
- 卷的响应带 `storage_pool`。
- 挂载一个有数据的目录到 `/opt/cloudland/pools/` 下前后，心跳上报的磁盘数值不变、心跳耗时不增加。

### 阶段 L1：本地存储池与数据卷

**范围**：
- 部署：安装依赖（含 Ansible 的 hyper 角色）、创建目录、修正 `chown`。
- 数据模型与后台任务：`storage_pools` 增加 `Media`、`FallbackGroup`（只做字段与编辑）、`Status`；`hyper_storage_pools`（含内置池记录的创建与补建）、`hyper_disks`、`storage_reservations`；`volumes` 的新状态与 `Reason`；`migrations` 的 `DiskPlan` 等字段；hostid 不复用。后台任务：过期预留清理、15 分钟无上报转 `unavailable`、各状态超时。
- 存储池增删改接口与管理页；介质检查与手工指定介质（§2.6；RAID1 容量提示随 `raid1` 放在 L3）。
- 节点：`scan_host_disks.sh`（含直接读 PV 标签和 md 超级块、`shared` / `unknown_member` 分类）、`create_local_pool.sh`（`single`、`linear`）、`remove_local_pool.sh`、`adopt_local_pool.sh`、`pool_probe.sh`、`pool_usage.sh`、维护标记；`pool_guard`；池锁；心跳读状态文件上报；待启动列表、开机关闭自启、`virsh start` 结果判断与补发 sync（§4.8）。
- 数据卷：按池建卷；首次挂载的校验、准入与 `new` / `existing`；扩容准入；删卷改为 `deleting` + 回调 + `delete_failed`；进行中操作的互斥（§5.10）；宣告丢失与恢复、强制卸下；维护；删除节点前的检查、`keep_pools`、接管、放弃接管、`destroy_pools`。
- 池快满与写满（§6.1，告警类型除外）：告警描述带挂载点；容量条阈值与「超分」标记；跨阈值立即上报；`GetHyperGroup` 加用途参数并按内置池占用过滤；`ignore_capacity`；模板加 `discard='unmap'` 与 `error_policy='enospace'`；`domblkerror` 识别、`paused_nospace` 上报、`inst_status` 精确匹配；自动恢复；池上有这类暂停时挡住新分配；统计占用。
- 迁移：同池迁移；计划在 `target_prepared` 时生成；源端经 ssh 在目标上 `pool_guard`、按实际数据量预检、预建时沿用簇大小、关机迁移改用 `rsync --sparse`；按计划清理与回滚（§7.3 第 1、2、3、7、8、9 步）；目标准入与迁移预留；不指定目标时按 G1 收窄候选组；维护模式逐台结果；因空间不足暂停的虚拟机不许迁移。
- 网关：新增路由；`FinishQuota` 把 202 当作成功。
- 前端：计算节点「存储」标签页（扫描、建池、删池、维护、宣告丢失、恢复、接管、统计占用、待启动列表）；计算节点列表与详情的磁盘改为按池汇总（§8.3）；存储池管理页（含放弃接管）；建卷选存储池；挂载弹窗按 `available_storage_pools` 置灰；云硬盘、虚拟机的新状态与原因。

**验收**：
- **基本流程**：work-03 用 `sdb` 建 `local-hdd`（单盘）。`local-hdd` 卷挂到 work-03 上的虚拟机，文件出现在池目录，根分区上没有。挂到 work-01 上的虚拟机被拒绝，提示 work-01 没有这个池。
- **同池迁移**：work-01 也建好 `local-hdd` 后，把虚拟机热迁移到 work-01，**再迁回 work-03**：两次都成功，数据完整，源端池里没有残留文件。不指定目标机器的迁移也成功（走 G1）。一次关机迁移后，目标文件的实际占用与源端相近（稀疏保持）。
- **迁移失败**：人为让目标端预建失败，回滚后目标池无残留，**再次发起迁移成功**。work-01 没有这个池时，迁移被拒绝并列出缺少的池。目标池可用空间小于实际数据量时，复制前就失败并回滚。
- **池不可用**：进入维护后在节点上手工卸载 work-03 的池：之后的心跳不会把它挂回去，状态为 `maintenance`；挂载、扩容、删卷都被 clapi 直接拒绝；根分区上没有产生任何文件；退出维护后自动挂载并恢复可用。
- **坏盘不拖垮节点**：用 `dmsetup suspend` 让测试池的设备 I/O 卡住：心跳照常，节点不被判离线，这个池 5 分钟内变为 `unavailable`（探测卡住），其他池和虚拟机的操作不受影响；恢复设备后池恢复可用。
- **准入**：并发对同一个小池（测试开关下的 loop 设备，§12）发起多次首次挂载，总分配不超过准入上限；扩容超过上限被拒；池上有因空间不足暂停的虚拟机时，挂新卷被拒。
- **扫描安全**：建池时选中系统盘被拒绝；选中有签名的盘但没勾「清除」被拒绝；带分区的旧盘（work-02 的 `sdb`）清除后建池成功。模拟重装：把一个测试池的盘从 LVM devices file 里去掉，再停掉它的阵列并清空 mdadm.conf，扫描仍识别为 `cloudland_pool`（或 `unknown_member`），都不能清除。
- **介质检查**：把一块测试盘手工指定为 `ssd`，用它建 `local-hdd` 被拒绝并列出这块盘；勾选确认后成功，审计里注明放行了介质不一致；直接调接口、不带 `allow_media_mismatch` 同样被拒绝；两块介质不同的测试盘组线性池同样要确认；重新扫描后手工值仍在，恢复自动识别后回到原值。
- **删卷**：节点离线时 clapi 直接拒绝；正常删除后文件消失、记录删除、网关的磁盘用量立即减少；人为丢掉回调，30 分钟后变为 `delete_failed`，不能挂载，再次删除成功。
- **迁移中的互斥**：虚拟机迁移期间，挂载、卸载、删除它的卷都被拒绝。
- **删池**：正常删池成功；池里有残留文件时被拒绝并列出文件，`force` 后成功，文件清单进审计。
- **显示**：work-03 建好 `local-hdd` 后，计算节点列表的磁盘总量等于内置池与 `local-hdd` 的容量之和，进度条颜色跟随最满的池；悬停明细与「存储」标签页、存储池管理页的数字一致。
- **重启**：重启 work-03：池自动挂载；池里有盘的虚拟机正常启动，网络规则恢复；心跳没有中断。模拟池晚到（开机时池挂不上）：虚拟机进入待启动列表，界面显示「等待存储就绪」，挂上后下一次心跳自动启动并补发同步。人为让 `virsh start` 失败，虚拟机留在列表里并显示原因，不被报成运行中。改过规格的虚拟机开机时不会被 libvirtd 抢先拉起。
- **重新注册**：删除节点（选择保留池）→ 重新注册（hostid 不复用旧号）→ 扫描显示可接管 → 接管后卷恢复可用，挂载正常；文件缺失的卷变为 `lost`。
- **坏盘与恢复**：测试开关下的 loop 设备，拔掉 backing file → 宣告丢失 → 卷可以删记录、可以强制卸下 → 强制删池成功，盘上的数据没有被擦。另一次：宣告丢失后把设备接回来，节点报告恢复，点「恢复」后池和卷回到可用。
- **写满**（测试开关下几 GB 的小池，放两台虚拟机的数据盘，超分比例临时调大）：
  - 占用跨过 80%、90% 时界面立即变色，告警带池的挂载点；90% 后往这个池挂新卷被拒。
  - 在一台虚拟机里 `dd` 写满：它被暂停，界面显示「存储空间不足」；另一台继续写入后也被暂停；用户自己暂停的第三台不受影响；同时挂着内置池盘的虚拟机只有出错的是本地池盘时才被判为空间不足。
  - 「统计占用」列出这两块卷且排在最前。
  - 事先在池根目录用 `fallocate` 放一个占位文件；写满后删掉它，腾出的空间达到 §6.1 的恢复条件：下一次探测内两台自动恢复，写入完成，文件系统检查无错误。
  - 虚拟机里删文件并执行 `fstrim` 后，池的实际占用下降。
  - 暂停中的虚拟机发起迁移被拒绝。
- **内置池快满**：用 `fallocate` 把 work-03 内置池的占用临时推到 90% 以上：新建虚拟机不会调度到 work-03；负载均衡的主备选点不受影响；删掉文件后恢复。
- **chown**：重新执行部署脚本后，池目录里文件的属主和时间戳没有被改动。

### 阶段 L2：迁移时换池

**范围**：可互换组的自动替换；`POST /migrations` 的 `disks`、`allow_pool_fallback`；`GET /instances/:id/migration_targets`；G2 候选组；`target_prepared` 时按可互换组生成换池计划；域定义与数据盘描述文件的改写（§7.3 第 4、5 步）；迁移弹窗逐盘选池、迁移详情显示计划。

**开工前**：用一台临时虚拟机手工验证 `--xml` / `--persistent-xml` 改写磁盘路径的热迁移与冷迁移（包括 UEFI 虚拟机）。

**验收**：
- 数据盘从 work-03 的 `local-hdd` 热迁移到 work-01 的内置池，再迁回：数据完整；文件在新路径；源文件已删除；卷记录的池和路径已更新；迁移后卸载、再挂载正常。
- 同样的换池做一次关机迁移；一块留原池、一块换池的混合情况；一台 UEFI 虚拟机的数据盘换池迁移，启动项不变。
- 可互换组：work-01 只有与原池同组的 `local-hdd-b`，不指定目标池时落在 `local-hdd-b` 并标注自动选择；`allow_pool_fallback=false` 时失败；不同组的池不会被自动选中。
- 自动选目标时，有能保留原池的机器就不会选到需要换池的机器。
- 不指定目标机器时带 `disks` 参数被拒绝。
- 维护模式：一台虚拟机无处可迁时被跳过并返回原因，其余照常迁移。

### 阶段 L3：加盘、换盘、RAID1、告警

**范围**：`extend_local_pool.sh` 与接口、界面；`raid1` 布局（建池、按对加盘、RAID1 容量提示）；`replace_local_pool_disk.sh`（§4.9）；同步进度；textfile 指标与「本地存储池」告警类型（clapi 的 rule_type、Prometheus 规则模板、前端的规则类型与三种语言文案）。

**验收**：
- `linear` 池在线加盘，容量增加，池里运行中的虚拟机不受影响；`single` 池加一块变为 `linear`，界面提示无冗余；加盘失败时池回到原来的状态，不变成 `error`。
- 两块盘（测试开关下的 loop 设备）建 `raid1`，同步期间可读写、显示进度；两块容量不同的测试盘建 `raid1`，界面和响应给出实际可用容量与浪费的容量，建成后池的容量与提示一致。
- `mdadm --fail` 一块后显示降级并告警，虚拟机继续读写；用「换盘」换上新盘，重建完成后恢复 `ready`，告警解除。
- 池变为 `unavailable` 时告警。
- 往 `local-hdd` 加一块手工标成 `ssd` 的盘要确认。

### 阶段 L0b：存储池抽象（系统盘与其余部分）

在 L4 之前完成，**需要先决定 WDS 的去留（待办 C1）**。

**范围**（GPFS 设计 §14 阶段 0 中余下的本地部分）：
- 系统盘元数据 `boot_disk`。
- `launch_vm.sh`、`reinstall_vm.sh`、`rescue_vm.sh`、`end_rescue.sh`、`capture_image.sh`、`clear_vm.sh`、`clear_image.sh` 使用下发的路径；clapi 相应的 `services/instance.go`、`services/image.go`、`rpcs/capture_image.go` 同步修改。
- 去掉 `$volume_dir/inst-N.disk` 的回退。
- 约 20 个脚本中按 `wds_address` 的分支（放弃 WDS 就直接删除）。
- GPFS 设计附录 B 第 1、2、5、7、8、9 条。第 3 条的前半已在 L0a 做；第 3 条的后半和第 4 条即本文附录 B 第 7、6 条，单独评估；第 6 条的路径部分已在 L0a 做。

**验收**：`test-items/` 全部回归通过（创建、重装、救援要能访问原盘、结束救援后没有残留的 NVRAM、捕获镜像、删除虚拟机等）；数据库里所有卷的路径与实际文件一致。

### 阶段 L4：系统盘与调度

**范围**：
- 系统盘可以放在本地池：由 clapi 选定宿主机、写 `boot` 预留，下发只含这一台的候选组（§5.6）；批量创建的每一台都单独走这个流程；`rcNeeded` 调整。
- 管理员先选宿主机再选池；可选的 `required_storage_pools`。
- NVRAM 放进池，并在迁移中按计划复制和清理（§7.3 第 6 步，含 `target_migration.sh:37` 的 NVRAM 模板）；换池迁移扩展到系统盘。
- 系统盘所在的池被宣告丢失时的处理。

**验收**：
- 选 `local-ssd` 作系统盘池创建虚拟机，只会落在有这个池、放得下的机器上；并发创建多台时总分配不超过准入上限；批量创建 3 台，每台都经过准入；cland 因 CPU 不够回调 `error=resource` 时，换下一台候选成功。
- 管理员指定 work-01 后只列出 work-01 上的池。
- 这台虚拟机的重装、救援、热迁移、关机迁移（含系统盘换池）、删除都正常；NVRAM 在迁移后保持（启动项、Secure Boot 状态不变）；删除后池里没有残留文件。
- 系统盘所在的池宣告丢失后，这台虚拟机只能删除，删除后节点上没有残留的域定义。

### 阶段 L5（可选）

同一台机器上换池（`virsh blockcopy --pivot` 或 `qemu-img convert`，复用 §7 的计划与改写）；本地盘与 GPFS 盘之间的转换；按池计配额；部署时自动建池；孤儿文件对账。

---

## 12. 测试环境

- 三台机器都只有一块空闲机械盘，没有 SSD。RAID1、加盘、换盘、小容量准入、坏盘模拟、「混用」都需要更多的盘。
- **测试开关**：节点 `cloudrc.local` 里设 `storage_allow_loop=true`（仅测试环境）时，扫描会列出 loop 设备，标识为 `loop:<backing file>`，建池流程与真盘相同。生产环境不设这个开关。另一个办法是加载 `scsi_debug` 模块得到带 by-id 的模拟盘，更接近真盘，可以二选一。
- loop 设备的 backing file 放在 `/opt/cloudland/cache/tmp` 以外。
- **写满测试**用几 GB 的 loop 小池；占用用 `fallocate` 占位文件调节，不必真的写满几个 TB。
- **卡住的设备**：用 `dmsetup suspend` 挂起池的逻辑卷（或 loop 之上的 dm 设备），模拟坏盘让 I/O 卡在 D 状态；`dmsetup resume` 恢复。
- **重装后的扫描**：从 LVM devices file 里删掉测试盘、停掉阵列并清空 mdadm.conf，模拟重装系统后的样子。
- 真正的介质差异（SSD 与 HDD 的性能）测不了。介质检查的逻辑用「手工指定介质」构造混用场景来测（§2.6）；RAID1 容量不一致用两个大小不同的 backing file 构造。

---

## 13. 待决策与待验证

1. **WDS 的去留（待办 C1）**：~~在 L0b 之前决定~~ **已决定放弃**（2026-09-22）。WDS 分支、`*_wds*.sh`、备份与一致性组（只支持 WDS）、QoS、`tools/`、Ansible 的 `wds` 角色一并删除。
2. **单盘池的风险接受度**：本地卷不支持备份，现有三台机器只能建单盘池。要么接受「坏盘即丢卷」（并在界面上说清），要么等加盘后再对用户开放本地池。
3. **默认池**：建议保持内置 `local`。
4. **`allow_pool_fallback` 的默认值**：建议 true，维护模式迁移更顺利。
5. **自动恢复的条件**：建议「可用空间 ≥ min(容量的 15%, 50 GiB)」；也可以改成只提示、由管理员手工恢复。
6. ~~**磁盘单位与 NVRAM 解析的修正**（附录 B 第 3、6、7 条）的时机~~ **已于 2026-09-23 修复**：先在三台节点上算了修正前后的数值再改（总量从写死的 10 TB 变为 1830 GiB，可用分别 1830 / 1310 / 1800 GiB，所有规格仍可调度），见附录 B。
7. **需要实测**（实测结果见 §14.3）：
   - `virsh migrate --xml` / `--persistent-xml` 改写磁盘路径（L2 开工前）。
   - Ubuntu 24.04 / 26.04 上，重装系统后 LVM devices file 是否让旧卷组默认不可见、md 阵列会不会被自动组装；影响扫描安全和接管（L1）。
   - mdadm 降级阵列开机时的强制启动时间与 `x-systemd.device-timeout` 的配合（L3）。
   - 写满暂停与恢复（L1）：系统盘是 `cache='writeback'`，确认空间不足时同样是暂停而不是报错给虚拟机；`virsh domblkerror` 在 QEMU 10.2 / libvirt 12.0 上的输出格式；`discard='unmap'` 在 virtio 盘上对 Linux 和 Windows（virtio-win 驱动版本）是否生效，以及 2 MiB 簇下的实际回收比例。
   - 热迁移（QEMU 块复制）后目标盘是否保持稀疏；`rsync --sparse` 复制 qcow2 后的实际占用（L1）。
   - 节点上 node_exporter 的 textfile 收集器是否已开启、目录是否一致（L3）。

---

## 14. 实施记录（2026-09-22）

按 WDS 删除 → L0a → L1 → L2 → L3 → L0b → L4 的顺序一次做完，部署到 work-01（控制面 + 计算节点）、work-02、work-03 测试。L5 未做。

### 14.1 实施范围与未做的部分

- **未做**：L5 全部（同机换池、按池计配额、部署时自动建池、孤儿文件对账）；可选的 `enable_discard.sh`；附录 B 第 10 条里「把心跳上报的超分比例写进 `hypers` 表」那一行（准入按 §6 用的是上报值，不受影响）。
- **WDS 删除的连带范围**：除 WDS 分支与 `*_wds*.sh` 外，备份、一致性组（只支持 WDS）、卷 QoS、`tools/`、Ansible 的 `wds` 角色、相关接口与代理白名单、前端入口一并删除。
- `test-items/` 的全量回归没有跑，测试是本方案自己的用例（见 §14.4）。

### 14.2 与方案不同的做法

实施中改掉的地方，都是方案写的做法在这套环境上不成立：

1. **关机迁移换池不能靠 `--persistent-xml`**：libvirt 12.0 的 `virsh migrate --offline` 直接忽略 `--persistent-xml`，目标上定义出来的仍是源端的旧路径（虚拟机随后起不来）。改为源端复制完磁盘后，经 ssh 在目标上 `virsh define` 改写好的 XML。热迁移的 `--xml` 按方案可用。
2. **mdadm.conf 按阵列 UUID 维护**：这套环境上 `mdadm --examine --scan` 与 `--detail --brief` 的输出**不带 `name=`**（只有 `ARRAY /dev/md/cl_<8位>_N ... UUID=`）。接管时的阵列匹配改为认 `/dev/md/cl_<8位>_N`；mdadm.conf 的增删一律按阵列 UUID（`md_uuid` / `mdadm_conf_add` / `mdadm_conf_del`），按名字匹配删不掉，删池后会留下陈旧条目（work-03 上实际留下过，已手工清理）。
3. **删池的确认不能放在请求体里**：cpgateway 转发 DELETE 时丢掉请求体，`confirm` / `force` 改为查询参数（服务端仍兼容请求体）。
4. **hostid 不复用要算上更多来源**：`hypers` 是硬删除，方案里写的 `Unscoped` 不起作用。改为 `max(hypers.hostid, volumes.hyper, instances.hyper〔含软删〕, hyper_disks.owner_hostid) + 1`。
5. **回调的空参数要占位**：clapi 的回调参数解析会丢掉末尾的空参数，节点回调里的空值一律传 `-`（`local_pool_status`、`clear_volume`、挂载失败的回调）。
6. **已丢失的池要允许退出维护**：否则「维护中发现盘坏 → 宣告丢失」之后无法让探测重新挂载，也就无法「恢复」。退出维护对 `lost` 的池只清维护标志，状态仍是 `lost`。
7. **加盘后要重算布局**：单盘池加一块变成 `linear`，节点要在作业里重算并随回调写回 `layout`，否则池一直显示 `single`（界面会错误地认为有冗余或无变化）。
8. **卸载成功后要删掉磁盘描述文件**：否则迁移会把陈旧的描述文件复制到目标，目标按它定义出已经不存在的盘。
9. **node_exporter 的 textfile 目录要从进程参数取**：这套环境是 `/var/lib/node_exporter`，不是发行版默认值；取不到就不写指标（不报错）。
10. **新池的「已用」不是 0**：1.8 TB 盘上新建的 XFS 池 `df` 已用约 36 GB（约 2%，reflink / rmapbt 的元数据预留）。容量条和 80 / 90% 判断都基于 `df`，属正常现象。

### 14.3 §13 第 7 条「需要实测」的结果

| 待验证 | 结果 |
|---|---|
| `--xml` / `--persistent-xml` 改写磁盘路径 | 热迁移可用；关机迁移不可用，见 §14.2 第 1 条 |
| 重装系统后 LVM devices file 与 md 阵列的表现 | 用「去掉 devices file 条目 + 停阵列 + 清 mdadm.conf + 改 VG 标签」模拟：扫描仍按 PV 标签与 md 超级块识别为 CloudLand 的盘且不允许清除；接管按阵列 UUID 组装成功，一块曾被踢出的旧成员被正确排除 |
| mdadm 降级阵列开机时的强制启动 | **未测**（没有真实重启过带 RAID1 池的节点） |
| 写满暂停与恢复 | `dd` 写满后虚拟机暂停，`virsh domblkerror` 输出 `vdc: no space`，映射为 `paused` + `storage_full`；加盘后下一次探测自动恢复并写完。`discard='unmap'` 只验证了配置生效（Linux + `fstrim`），回收比例与 Windows 未测 |
| 稀疏是否保持 | 热迁移（QEMU 块复制）与关机迁移（`rsync -S`）后，目标文件的实际占用与源端一致 |
| node_exporter textfile 收集器 | 已开启，目录见 §14.2 第 9 条 |

### 14.4 测试覆盖

三台机器各用空闲的 `sdb` 建单盘池 `local-hdd`；RAID1、小容量、坏盘用测试开关下的 loop 设备构造。用例脚本在 work-01 `/root/st-*.sh`。

| 范围 | 覆盖的用例 |
|---|---|
| 池的配置 | 三台扫描、建池（单盘）、挂载与 fstab `nofail`、标记与 LVM 标签、删池；负面：系统盘、有数据未勾清除、介质不一致未确认、确认名不符、布局与盘数不符，均被拒 |
| 数据卷 | 建卷、首次挂载（2 MiB 簇）、写入、在线扩容、卸载后再挂载（`existing`）、跨机器挂载被拒、删除 202 与文件删除、文件缺失时不新建空盘、节点回报删除失败后卷恢复 |
| 迁移（L1 / L2） | 指定目标与调度选点的热迁移（预留建立与释放、稀疏保持、源端清理）、换池热迁移、换池关机迁移、可互换组自动替换、`migration_targets` 接口 |
| 加盘换盘（L3） | loop 上建 RAID1 与同步进度、在线加盘（第二个阵列，容量增长）、成员失效判定降级、换盘与重建回到 `ready`、mdadm.conf 全程跟随 |
| 写满（§6.1） | 小池写满后虚拟机暂停并显示原因、新分配与迁移被拒、加盘后自动恢复、「统计占用」把文件对上卷 / 虚拟机 / 组织 |
| 维护与丢失 | 维护中拒绝分配、手工卸载后探测不挂回、待启动列表上报 `storage_pending`、宣告丢失、强制卸下后虚拟机启动、退出维护后恢复、删池在有卷时被拒 |
| 节点增删与接管 | 有池有卷时拒绝删除节点、`keep_pools` 后卷变孤儿、放弃接管后变 `lost`、模拟重装后接管并恢复卷 |
| 系统盘（L4） | 系统盘落在指定池（boot 预留）、热迁移、救援（救援系统看到池里的原盘）、重装（原地重建）、删除后文件清理、UEFI 的 NVRAM 放在池的 `nvram/` 并随迁移复制、删除后清理 |
| 前端 | typecheck、lint、`i18n:check` 通过；中文界面截图核对 |

### 14.5 没有覆盖的验收项

- `dmsetup suspend` 模拟 I/O 卡住的「坏盘不拖垮节点」。
- ~~真实重启计算节点~~ **2026-09-23 已在 work-03 实测通过**：系统盘与数据盘都在 `local-hdd` 的虚拟机，开机 +12 秒池经 fstab 挂载（早于 cloudlet 的 +26 秒）、约 +43 秒 cloudlet 注册、+43～47 秒 4 台虚拟机全部起来、约 +55 秒首次心跳与池上报 `ready`，两块盘上的标记完好。仍未覆盖：「池晚到」触发待启动列表的**真实**开机路径（这次池挂得比 cloudlet 早，只用手工卸载与模拟晚到验证过）、降级 RAID1 的开机行为。
- 并发准入（只验证了单个请求下的上限与超分比例）。
- 内置池占用推到 90% 后新建虚拟机不落在该节点。
- 宿主机维护模式（`Maintain`）的逐台迁移结果；一块盘留原池、一块换池的混合迁移。
- 后台超时任务（15 分钟无上报转 `unavailable`、30 分钟 `deleting` 转 `delete_failed`）只跑了代码路径，没有真的等到超时。
- `fstrim` 之后池占用下降、重跑部署脚本后 `chown` 不动池内文件。
- `test-items/` 全量回归；前端的写操作没有在界面上实跑（只看了页面与截图）。

### 14.6 本次新发现的既有缺陷（未修，不属本方案范围）

- **从虚拟机捕获镜像在 S3 模式下上传必然失败**：`CLAPI_INTERNAL_URL` 为空时 clapi 默认拼成 `http://<clapi 主机名>:8255/api/v1`，而 clapi 的 8255 只提供 HTTPS，节点 `curl` 收到 400，镜像停在 `error`。work-01 上从来没有成功的 capture 记录，与本方案无关。测试时池里的系统盘被正确找到并完成了 `qemu-img convert`（回报了虚拟大小），失败只在上传这一步。修的时候要一并决定节点怎么信任 clapi 的自签证书。

---

### 14.7 第二轮 code review（2026-09-23）修掉的 12 条

全部核实成立后修复并部署三台验证（回归用例 TC-16 STO-12）。与本文设计相关的几条：

| 问题 | 与本文的关系 / 改法 |
|---|---|
| 心跳给 cland 的内置池总量用了文件系统大小 | §6 的准入口径是 `own + avail`（clapi `applyCapacity`），心跳改为读探测写在 `builtin.state` 里的 `own` 加当下 `df` 的 `avail`。work-01 的可分配量因此从 1830 降到 1609 GiB，差值就是 OS / docker 占的 |
| 探测进程的 pid 文件跨重启保留，pid 被复用后池永远报 `probe stuck` | §4.7 的判活加上 `/proc/<pid>/cmdline` 校验（`probe_alive`） |
| 待启动列表把「等池」和「`virsh start` 失败」混在一起 | §4.8 的本意是「失败的留在列表里并显示原因」：现在池就绪但起不来的上报 `start_failed`（clapi 映射 `shut_off` + 原因，界面「开机后启动失败」），libvirt 的报错存在节点 `$run_dir/start_failed-<id>`，每 5 分钟重试一次 |
| `tmp/.incoming-*` 占位不过期 | §7.3 的占位改为与 clapi 预留同样的 24 小时寿命：探测删除过期的，`prepare_migration_disks.sh` 求和时忽略。这是「减少孤儿文件」里的第 2 项，因为它污染的是容量判断而不只是留文件，作为例外先做了；其余孤儿文件处理仍按决定暂缓 |
| 系统盘放内置池 + 指定宿主机时只准入不预留 | §5.6：`inter=` 下发时 cland 不做检查，clapi 补 `boot` 预留，与其他池的分支一致 |
| 删池被节点拒绝后池卡在 `error` | 见 §14.2 之后的补充：节点回报 `refused`，clapi 恢复原状态（周期上报从不改写 `error` 的行） |
| 其余 | 改名清掉卷的 `reason`、`PATCH over_ratio: 0` 被吞、`old_hostid: 0` 被绑定拒绝、`poolByRef` 的 `LIKE` 未校验、`SetMaintenance` 先发命令后写库、`GET /instances` 与 `GET /storage_pools` 的 N+1 |
| 验证时顺带发现的既有问题 | `LaunchVM` 回调把控制字 `sync` 原样写进 `instances.reason`（每台经开机同步上报的虚拟机都带着它），改为存空串；`start_pending.sh` 的 flock 文件每台机器留一个，随 `pending_start_remove` 一并删 |

---

## 附录 A：受影响的代码

### clapi（`api/src/`）

- **`model/`**：
  - `storage_pool.go`（新建）：`StoragePool`（L0a；`Media`、`FallbackGroup` 在 L1）、`HyperStoragePool`、`HyperDisk`、`StorageReservation`（L1）。
  - `volume.go`：`StoragePoolID`（L0a）、`Reason` 与新状态（L1）。
  - `migration.go`：`DiskPlan`、`DiskRequests`、`AllowPoolFallback`、`IgnoreCapacity`（L1）。
  - `instance.go`：`Reason` 的新取值（L1）。
- **启动与后台任务**：
  - 启动时创建内置池、为每台节点补建内置池记录、回填卷的 `storage_pool_id` / `path` / 系统盘 `hyper`（L0a / L1）。
  - 后台任务（L1）：过期预留清理（5 分钟）、15 分钟无上报转 `unavailable`、`creating` / `extending` / `removing` 超时、`deleting` 超时转 `delete_failed`。与现有的定时任务（如 `services/audit.go` 的清理任务）一样在 clapi 启动时拉起。
- **`services/`**：
  - `storage_pool.go`（新建，L1）：存储池增删改、放弃接管。
  - `hyper_storage.go`（新建）：扫描结果处理与手工介质；建池、加盘、换盘（L3）、删池、维护、宣告丢失、恢复、接管；介质与容量检查（§2.6）；准入（事务 + 行锁）。
  - `volume.go`：
    - 建卷（`storage_pool`、路径，第 194–203 行）；
    - 首次挂载的准入、请求事务里写 `hyper`、`new` / `existing`（约第 440–455 行）；
    - 挂载与卸载的状态限制（第 413、437 行）；
    - 扩容不写预留（第 625–628 行）；
    - 删除改为 `deleting` + 202（第 520–539 行）；
    - 强制卸下。
  - `migration.go`：计划生成、G1 / G2 候选组（第 145–169 行）、准入与预留、新参数、拒绝因空间不足暂停的虚拟机、批量迁移逐台处理（第 35–40、106–110、190 行）。
  - `hyper.go`：hostid 分配改为 `Unscoped`（第 304 行）；注册时创建内置池记录；`Maintain` 返回逐台结果（第 534–541 行）；`Delete` 的检查与 `keep_pools`（第 569–576 行）。
  - `instance.go`：L4 的宿主机选择、`boot` 预留、批量创建（第 278 行）、`rcNeeded`（第 276 行）、丢失池的处理；L0b 的 `boot_disk` 与重装、救援、删除的路径。
  - `image.go`：L0b 捕获镜像的路径。
- **`common/instance.go:32` `GetHyperGroup`**：加用途参数，创建虚拟机与迁移时排除内置池占用达到 90% 的机器（L1）；按池收窄候选（L4）。
- **`rpcs/`**：
  - 新增：`host_disks`、`local_pool_status`、`clear_volume`、`pool_usage`（L1）。
  - `inst_status.go`：`paused_nospace`、`pending_storage` 映射为状态加 `Reason`，恢复运行后清掉 `Reason`（L1）。
  - `clear_vm.go`：`updateAttachedVolumes` 不改动 `lost` / `orphaned` 卷的状态（L1）。
  - `launch_vm.go` / `create_volume.go`：给系统盘写 `hyper`（L0a）；补发的 sync 回调沿用现有处理（L1）。
  - `attach_volume.go`：`hyper` 取回调来源节点，`new` 失败时清零（L1）。
  - `migrate_vm.go`：卷 JSON 带驱动与路径（第 45–60 行，L0a）；`target_prepared` 时生成计划与准入（第 388–447 行）；`completed` 时更新池与路径（第 306–311 行）；回滚时删除预留（L1）。
  - `capture_image.go`：L0b。
- **`apis/`**：`storage_pool.go`（新建）；`hyper.go` 的子路由与磁盘汇总（第 396–397 行）；`volume.go`（202、`force_detach`、`storage_pool`、`reason`）；`instance.go`（`available_storage_pools`；L4 的参数）；`migration.go`；`routes.go`；`audit_actions.go`。

### 节点脚本（`scripts/`）

- **新增**：`kvm/scan_host_disks.sh`、`kvm/create_local_pool.sh`、`kvm/remove_local_pool.sh`、`kvm/adopt_local_pool.sh`、`kvm/pool_probe.sh`、`kvm/pool_usage.sh`、`kvm/set_pool_maintenance.sh`（L1）；`kvm/extend_local_pool.sh`、`kvm/replace_local_pool_disk.sh`（L3）；`kvm/enable_discard.sh`（可选）。
- **`cloudrc`**：`pool_guard`；盘的分类函数（扫描与二次检查共用）；池锁的封装（`flock -w`）；`pending_start_remove`；`storage_allow_loop` 开关。
- **`kvm/report_rc.sh`**：`du -x`（第 257 行，L0a）；启动探测进程、读状态文件上报；待启动列表、开机关闭自启、`virsh start` 结果与补发 sync（第 207–222 行）；`inst_status`（第 68–100 行）改为 `grep -qxF`，上报 `paused_nospace`、`pending_storage`（L1）。
- **`xml/`**：`template_with_qa.xml`、`template_uefi_with_qa.xml`、`volume.xml` 的本地 qcow2 盘加 `discard='unmap' error_policy='enospace'`（L1）；删除 `template.xml`、`template_uefi.xml`（L0a）。
- **`kvm/attach_volume_local.sh`**（第 10、14–15 行）：使用下发的路径、池锁、`pool_guard`、`new` / `existing`、失败时删除新建的文件。**`kvm/resize_volume_local.sh`**、**`kvm/clear_volume_local.sh`**（加回调）：使用下发的路径、池锁、`pool_guard`。
- **`kvm/source_migration.sh`**：读计划（第 49 行）；经 ssh 在目标上 `pool_guard`、空间预检与迁入占位；预建沿用簇大小（第 58–67 行，去掉 `mkdir -p`）；关机迁移改用 `rsync --sparse`（第 64 行）；改写并复制数据盘描述文件（第 79 行，L2）；NVRAM（第 81 行，L4）；`--xml` / `--persistent-xml`（第 118、123 行，L2）。
- **`kvm/finish_source_migration.sh`**（第 31–33 行）、**`kvm/async_job/clear_target_migration.sh`**（第 39、48、51 行）：按计划清理，删除迁入占位。
- **关机、删除、迁移、重装、救援相关脚本**：调用 `pending_start_remove`（L1）。
- **删除**（L0a）：`kvm/attach_vol.sh`、`kvm/detach_vol.sh`、`kvm/create_volume_from_image.sh`、`kvm/create_volume_local.sh`。
- **L0b**：`kvm/launch_vm.sh`、`kvm/reinstall_vm.sh`、`kvm/rescue_vm.sh`、`kvm/end_rescue.sh`、`kvm/capture_image.sh`、`kvm/clear_vm.sh`、`kvm/clear_image.sh`、WDS 相关脚本。**L4**：`kvm/target_migration.sh:37` 的 NVRAM 模板。

### 其他

- **`cpgateway/`**：`src/apis/proxy_routes.go` 新增路由；`src/services/quota.go` 的 `FinishQuota` 把删除类请求的 202 当作成功。
- **部署**：`deploy/docker/scripts/deploy-compute-node.sh`（依赖、`/opt/cloudland/pools`、第 346 行的 `chown`）；Ansible 的 hyper 角色（依赖）。
- **告警**：`deploy/roles/monitor/templates/compute-core-resources.yml.j2` 的描述带 `mountpoint`（第 35 行，L1）；新增「本地存储池」告警的 `.j2` 模板与 clapi 的 rule_type（L3）。
- **`web/`**：计算节点「存储」标签页、计算节点列表与详情的磁盘、存储池管理页、建卷、挂载、云硬盘状态、虚拟机原因、迁移、维护模式、创建虚拟机（L4）、告警规则类型（L3），三种语言。

---

## 附录 B：审查中发现的现有缺陷

以下问题是审查本方案时发现的现有代码缺陷。各条注明处置方式：随本方案某个阶段修复、已单独修复，或需要单独评估。

| # | 缺陷 | 位置 | 处置 |
|---|---|---|---|
| 1 | 创建虚拟机时指定宿主机没有权限检查，普通成员直接调接口也能把虚拟机固定到某台节点 | `apis/instance.go:580-589`、`services/instance.go:132` | **已修复**（`4659d337`）：接口层在查节点之前检查，服务层再检查一次，非系统管理员返回 403 |
| 2 | 开机拉起虚拟机时不看 `virsh start` 的结果，一律回报 `running` | `report_rc.sh:219-220` | **已修复**（L1，§4.8） |
| 3 | 心跳把 `du -s`（KiB）和 `df -B 1`（字节）直接相减，根分区上非 CloudLand 文件的占用只扣了约千分之一 | `report_rc.sh:257-258` | **已修复**（2026-09-23，与第 6、7 条一起）：评估时发现问题比描述的更大——`calc_resource` 开头有一句上游 2024 年加的 `total_disk=10000000000000`，把 `df` 取到的真实容量覆盖成写死的 10 TB。现在总量 = 内置池文件系统大小 × 超分比例，可用 = 总量 − 内置池里所有 `*.disk` 的虚拟大小，单位全是字节，与 clapi 对其他池的准入公式一致；两次 `du` 一并删掉（其中一次每次心跳遍历整个根分区，work-02 上约 0.6 秒） |
| 4 | 批量创建只有第一台按指定节点下发，其余走调度 | `services/instance.go:278` | **已修复**（L4，§5.6） |
| 5 | 删除本地卷时节点离线，命令被丢弃，文件残留 | `common/clients.go:132-139`、`services/volume.go:520-539` | **已修复**（L1，§5.5）：改为 `deleting` + 回调，节点离线直接拒绝 |
| 6 | 调度时磁盘需求按 `GB*1024*1024` 传给 cland，而节点上报的是字节，相差 1024 倍（即 GPFS 设计附录 B 第 4 条） | `services/instance.go:276`、`services/migration.go:163` | **已修复**（2026-09-23）：需求改为字节，且只算**落在内置池的部分**——cland 这一维度代表的是内置池余量，放在其他池的系统盘由 clapi 选机器并准入，不能算到候选机器的内置池头上；迁移按实例在内置池里的全部磁盘（系统盘 + 数据盘）计算。实测：2000 GB 系统盘的规格（超过每台 1830 GiB 的可分配量）被 cland 以 `no node has sufficient resources` 拒绝、实例 `Resource is not enough`，修之前会被接受 |
| 7 | 心跳把 UEFI 虚拟机的 NVRAM 文件当 GiB 解析，每台多算 528 GiB，约 3 台就能让 2 TB 的节点被判为磁盘不足（即 GPFS 设计附录 B 第 3 条的后半） | `report_rc.sh:248-256` | **已修复**（2026-09-23）：只统计 `*.disk`，用 `qemu-img info --output=json` 取精确的字节数（文本输出的第三列不看单位，528 KiB 被当成 528 GiB）。修正前三台节点上都还没有 UEFI 虚拟机，这条尚未造成实际影响 |
| 8 | 调整规格后给虚拟机开启了开机自启，节点重启时 libvirtd 会直接拉起它 | `resize_vm.sh:35` | **已修复**（L1）：`resize_vm.sh` 不再开启自启，开机同步时关闭所有域的自启（§4.8） |
| 9 | hostid 取未删除节点里的最大值 + 1，被删的正好是编号最大的节点时，这个号会分给下一台注册的机器 | `services/hyper.go:304` | **已修复**（L1，§3.7）：`hypers` 是硬删除，`Unscoped` 无效，改为取 `hypers.hostid`、`volumes.hyper`、`instances.hyper`（含软删）、`hyper_disks.owner_hostid` 的最大值 + 1 |
| 10 | 心跳回调只在内存里给超分比例赋值，不写数据库；数据库里的值只由 `PATCH /hypers` 写入，可能与节点实际使用的不一致 | `rpcs/hyper_status.go:103-119`、`:125-144` | **部分修复**（L1）：内置池的准入已改用节点随上报带来的值（§6）；写进 `hypers` 表那一行仍未做 |
| 11 | 挂载、卸载只拦「已暂停」和「救援中」，迁移中也能挂载卸载 | `services/volume.go:413`、`:437` | **已修复**（L1，§5.10）：挂载、卸载、扩容一并拦迁移中、重装中、调整规格中等状态 |
| 12 | `inst_status` 按子串匹配旧列表，状态变化可能漏报，`5 …` 会被 `15 …` 命中 | `report_rc.sh:86` | **已修复**（L1）：改为 `grep -qxF` 整行匹配 |
| 13 | 挂载本地卷时文件不存在就新建一块空盘，已落盘的卷文件丢失后会静默变成空盘 | `attach_volume_local.sh:14-15` | **已修复**（L1，§5.2）：clapi 下发 `new` / `existing`，`existing` 时文件不在就报错 |
| 14 | 节点重启后会拉起所有定义过的虚拟机，包括重启前已经关机的 | `report_rc.sh:207-221` | 现有行为，本方案不改 |
| 15 | cland 转发回调只重试 3 次（间隔 1、2 秒），clapi 重启的窗口里回调会丢 | `cland/callback.go` | 本方案靠回调幂等与超时兜底，不改重试策略 |
| 16 | 4 个脚本、2 个模板没有任何地方使用 | 见 L0a | **已删除**（L0a） |
