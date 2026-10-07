# TC-25 GPFS 纠删码布局（ECE）

优先级 **P1** · 纠删码布局的参数与布局校验、创建向导、部署（恢复组 / 纠删码卷 / 文件系统）、重试路径、坏盘、删除（含建到一半的恢复组）· 约 180 min（建恢复组最慢）

2026-10-07 按 `docs/architecture/plan/shared-storage-design.md` §7.9（S7 纠删码第一版）写的用例。纠删码要 IBM Storage Scale **纠删码版**安装包、每台至少 4 块盘（合计至少 12 块），work-x 每台只有一块数据盘，所以分两种跑法：

- **A. 试验虚拟机**（节点脚本按作业协议直接执行，不经 clapi / cland）：work-x 上三台 KVM 虚拟机 ece1–3（`/root/ece-spike-*.sh`），盘是 virtio-scsi 带 WWN 的稀疏盘或虚拟机里的 LIO 模拟盘。试验虚拟机在 VPC 里，连不到管理网上的 cland，注册不成 CloudLand 节点
- **B. 物理机端到端**（界面 → clapi → cland → cloudlet → 节点）：work-x 上每台用 LIO 把一块数据盘上的文件做成 5 块模拟盘。要先删掉 `gpfs1`（一台节点只能属于一个 GPFS 集群），**要用户同意**

clapi 侧另由 PostgreSQL 测试覆盖：`TestStorageGPFSECEDeployPG`（假 cland 走完 15 步与删除、核对每步输入、非纠删码版安装包被拒）。

**2026-10-07 两种跑法都跑过**（执行记录 `test-items/runs/2026-10-07-cb4c8250+ECE模拟盘.md`）：A（试验虚拟机）ECE-02（本机界面）、03、04、05、08 通过；B（物理机端到端，删了 gpfs1）ECE-03、05、06、07、08 通过、公网界面只读检查通过。共修了 8 个问题（回归点 1–9）。已知限制：非 IBM 硬件上 `ess_config_mismatch` 消不掉，集群健康一直 warning。

## 设计约定（判定预期的依据）

- **参数**：`layout: ece`，`ece_code`（默认 4+2p；4+3p、8+2p、8+3p、3WayReplication、4WayReplication），`block_size`（按纠删码限定，默认 4M，三副本 / 四副本默认 2M），`ece_set_size`（默认 80%），`pagepool_mib`（默认且至少 8192），`no_slot_map`（仅测试）。不接受 `data_replicas`、`test`；纠删码参数不能用在副本模式
- **布局**：NSD 主机就是恢复组服务器（3–32 台），只有它们能给盘；每台盘数相同、一种介质；合计至少 12 块且至少比纠删码宽度多 2 块
- **安装包**：只收纠删码版（`edition = erasure_code`），建集群时就拒绝其他版本（`ErrStoragePackageState`）；向导里其他版本置灰，标「纠删码布局要用纠删码版」
- **内存预留**：服务器 pagepool + 2 GiB，客户端 2 GiB
- **部署**：前 8 步同副本模式，之后 `resolve_disks`（不写 `nsddevices`）→ `ece_configure`（节点类 `cl<ID>_nc`、拓扑一致、`--recycle one`）→ `ece_slots`（实体机查槽位映射；`no_slot_map` 时把 `nsdRAIDStrictPdiskSlotLocation` 与 `nsdRAIDDiskCheckVWCE` 设成 0 并逐台重启）→ `ece_create_rg`（核对日志组数 = 服务器数 × 2 + 1，不够补跑 `--complete-log-format`）→ `ece_create_vs` → `ece_create_fs` → `finish`
- **收尾**：集群的盘按 WWN（不行再按服务器 + 设备）对上恢复组的物理盘，盘名改成物理盘名（`n001p001`）；`attrs` 记下 `node_class` / `recovery_group` / `vdisk_set` / `ece_code`
- **能力**：只有部署、删除、建存储池、轮换密钥；加减节点、加盘、移除盘、换盘、改角色、重新均衡、升级都关掉（界面不显示、接口 400）；GPFS 拉起 down NSD 的看护对纠删码集群跳过
- **健康**：`backend_health` 收到 `recovery_group` 时报每块物理盘的状态（`ok` → `up`），其他状态照报并告警
- **删除**：文件系统 → 纠删码卷（delete、undefine）→ 恢复组 → 取消服务器配置 → 删节点类 → 照副本模式删集群。恢复组普通删除失败时（建到一半），停掉节点类成员的 GPFS 再 `-p` 强制删

### ⭐ 最容易误判的几点

- **建恢复组要很久**：每台两个日志组、每个日志组一块 32 GiB 三副本的日志盘，机械盘上一两个小时，步骤超时 8 小时。进度看管理节点上的 `tscrvdisk` / `mmcrvdisk` 进程；`mmlspdisk`、`mmlsrecoverygroup` 在格式化期间会排队或失败，不是坏了
- **`mmvdisk` 可能推迟日志盘**（`Deferring recovery group log vdisk creation and format`）：脚本会补跑 `--complete-log-format`，日志里看到这一行是正常的
- **LIO 模拟盘必须 `emulate_write_cache=1`**：否则 GNR 诊断慢盘时发的 DPO 读被 LIO 拒绝，盘卡在诊断里，建恢复组挂住（来宾 / 节点内核日志 `Got CDB: 0x28 with DPO bit set`）。打开后 GNR 会把盘标成 `VWCE` 并迁出数据，除非 `nsdRAIDDiskCheckVWCE=no`（`no_slot_map` 时 `ece_slots` 设）
- **刚部署完健康就是 warning**：`ess_config_mismatch`（组件库 `mmlscomp` 为空；组件只能登记 IBM / Lenovo 型号，Supermicro + LIO 盘发现 0 个，消不掉也不能隐藏）。`gnr_rg_not_primary` 不应再出现（`ece_create_rg` 收尾时把根日志组挪到第一台服务器）
- **健康整体是 warning 不一定有事**：`mmhealth` 的 INFO_EXTERNAL 类历史事件（以前的服务器 panic、节点被驱逐、OOM 等）不会自己消失，看物理盘的状态（全部 `up`）判断恢复组本身；测试环境用 `mmhealth event hide` / `resolve` 清掉
- **`mmlspdisk` 段落里的 `server` 不是盘所在的节点**：scale-out 时是此刻服务恢复组的节点，盘在哪台看 `device`（`//ece1/dev/sdc`）
- **`-p` 强制删掉的恢复组不清盘**：这些盘上留着 pdisk 描述，再认领要勾「清除旧数据」

## 测试环境

- A：work-01 `/root/ece-spike-lib.sh`（`gx <n> <命令>` 进虚拟机、`hx` 进宿主机），`/root/ece-spike-12.sh`（擦盘 → 打开 LIO 写缓存 → 按作业协议跑 `gpfs_ece.sh` 各步 → 重试 → 健康 → 数据 → 拔盘 → 拆除），日志 `/root/ece-spike-12.log`；虚拟机里 `/root/ece-stc-run.sh <脚本> <run> <分钟>`（标准输入是步骤输入），CloudLand 的节点脚本装在 `/opt/cloudland/scripts`。三台虚拟机 4 核 12 GB，每台 5 块 256 GiB 的 LIO 盘（`/var/lib/lio/d1–5.img`，在一块 1.5T 的稀疏 virtio 盘上）
- B：每台 `targetcli`：`/backstores/fileio create lio<N> /var/lib/cl-ece/d<N>.img 256G`、`set attribute emulate_write_cache=1`、`/loopback create` 后为每块盘建 LUN；之后扫描磁盘应看到 5 块带 WWN（`wwn-0x6001405…`）的空闲 HDD
- 安装包：纠删码版 6.0.0.2（软件包仓库里已有，许可证已接受）；另需一个其他版本的包测 ECE-01 第 7 条（没有时用 PostgreSQL 测试）
- 沙箱：PostgreSQL `go test ./src/services -run 'TestGPFSECE|TestStorageGPFSECEDeployPG|TestStorageGPFSDeployPG'`（后一个核对副本模式收尾重跑后盘的 `fs_id`）；WSL `gpfs-spike/stc-test9.sh`（26 项，替身 `mm*` 命令：`slots` 的两个参数与按需重启、`create_rg` 补跑推迟的日志盘与两种 `device` 写法、`teardown_ece` 的兜底与纠删码卷、物理盘数核对、节点类读不到、安装包逐个核对、`nsddevices: false`）；本机界面 `pw/stc-ui-ece.js`（24 项，所有接口在浏览器里模拟，集群详情读 `layout_info`）

## ECE-01 参数与布局校验（不破坏）

逐条 `POST /storage_clusters`（或预检），每条只改一处：

1. 3 台 × 3 块盘（共 9 块）
2. 3 台，盘数 4 / 4 / 5
3. 一台的盘介质是 SSD、其余 HDD
4. `params` 带 `data_replicas: 2`，或 `test: true`
5. `ece_code: 3WayReplication` + `block_size: 4M`；`ece_code: 6+2p`
6. `pagepool_mib: 4096`
7. 安装包是数据管理版
8. 不给 `layout: ece`、只给 `ece_code`

**预期**：1–6、8 都是 400（`ErrStorageInvalidPlan`，消息说明哪条不满足）；7 是 400（`ErrStoragePackageState`，「needs an erasure code edition package」）；都不建集群、不占盘

## ECE-02 创建向导（界面，不破坏）

1. 「新建集群」→ 选「GPFS 纠删码（ECE）」
2. 软件步骤看安装包列表
3. 主机步骤选 3 台、每台 4 块盘；参数步骤改纠删码、块大小、pagepool，勾「不要求槽位映射」
4. 预检、确认

**预期**：只有纠删码版的包能选，其他版本置灰并标原因；主机步骤说明「NSD 节点就是恢复组的服务器」；参数页没有单机测试和数据副本数，纠删码换成 8+3p 时块大小可选 16M、换成三副本时块大小自动降到 2M，pagepool 小于 8192 不能下一步，勾槽位后显示黄字警告；预检与创建的 `params` 带 `layout: ece` 和填的值。本机 `pw/stc-ui-ece.js` 覆盖这些

## ECE-03 部署（A 或 B）

1. A：`/root/ece-spike-12.sh` 的 3–7 步；B：界面按 ECE-02 提交，盯任务详情
2. 部署完看集群详情、`mmvdisk recoverygroup list`、`mmvdisk vdiskset list`、`mmlsfs`、三台 `mmlsmount`

**预期**：`ece_configure` 拓扑三台「ECE 5 HDD」100/100；`ece_slots` 结果 `slot_check: off, write_cache_check: off`，三台 `mmdiag --config` 两个参数都是 0；`ece_create_rg` 结果带 15 块物理盘（名字、**盘所在服务器的地址**（n001 是 ece1 的、n002 是 ece2 的……）、设备 `sdX`、WWN），日志组 7 个；纠删码卷 4+2p；文件系统三台都挂上；B 里集群详情「布局：纠删码 · 4+2p」、恢复组 / 纠删码卷名字，磁盘标签里盘名是 `n00xp00y`，节点内存预留服务器 10 GiB

## ECE-04 重试路径（接 ECE-03）

1. 每一步按原输入再跑一次（A：`ece-spike-12.sh` 第 8 步；B：任务失败后「重试」会从 `resolve_disks` 重跑）

**预期**：都成功，什么都不重建：`configure` 跳过已配置的节点类、`slots` 不重启（守护进程里的值已是 0）、`create_rg` 发现恢复组已在且日志组数够、`create_vs` 输出「vdisk set … is created already」、`create_fs` 已存在就跳过

## ECE-05 健康与坏盘（接 ECE-03）

1. 健康钩子（A：`backend_health`；B：等健康看护一轮）
2. 写 1 GiB 随机数据，三台读 md5
3. 拔掉一块盘（LIO：`targetcli /loopback/<naa>/luns delete lun2`），2 分钟后再看健康、在别的节点读数据
4. 插回（`luns create /backstores/fileio/lio3 lun=2`），等恢复

**预期**：正常时 15 块全是 `up`、健康 ok；三台 md5 一致；拔盘后那块盘是 `diagnosing` / `missing` 等，健康 warning（B 里出存储告警），读数据 md5 不变；插回后几分钟内全部 `up`

## ECE-06 不支持的操作（B，不破坏）

1. 集群详情找「添加节点」「添加磁盘」「升级」按钮，磁盘行的「移除」「换盘」
2. 直接调 `POST /storage_clusters/:id/disks`、`/nodes`、`/upgrade`

**预期**：按钮都不显示，「轮换密钥」在；接口 400「gpfs clusters (ece) do not support this operation yet」

## ECE-07 存储池与云硬盘（B）

1. 在纠删码集群上建共享池，建云硬盘挂给云服务器，写数据；系统盘放在这个池里建一台云服务器

**预期**：与副本集群相同（fileset 带配额、卷可跨节点挂、系统盘克隆）

## ECE-08 删除（A 或 B）

1. A：`ece-spike-12.sh` 第 12 步（`teardown_ece`）；B：删除集群
2. 建到一半的恢复组：建恢复组那一步中止后删除（A 里是杀掉卡住的 `--complete-log-format` 再重启 GPFS 造出来的）

**预期**：文件系统、纠删码卷（先 delete 再 undefine，只定义未建的直接 undefine）、恢复组、节点类都删掉，`mmvdisk recoverygroup list` 为空，全程不停任何节点的 GPFS；恢复组上还有纠删码卷时直接失败（「still has vdisk sets」），不停服务器；建到一半的恢复组普通删除报 `Vdisk RG001ROOTLOGHOME not found`，兜底停掉服务器后 `-p` 删除成功，之后取消服务器配置、删节点类都成功；这些盘用 `wipe_disk` 擦过后能重新建恢复组

## 回归点

1. LIO 模拟盘上的 DPO 读被拒（`emulate_write_cache` 没开）→ 盘卡在诊断、`--complete-log-format` 挂住（2026-10-07）
2. 开了 `emulate_write_cache` 而 `nsdRAIDDiskCheckVWCE` 还是 1 → 盘被标 `VWCE` 并迁出数据（2026-10-07）
3. 建到一半的恢复组删不掉（`teardown_ece` 原来直接失败）（2026-10-07）
4. 非纠删码版安装包要到 `install` 步骤才失败（原来建集群时不检查）（2026-10-07）
5. `nsdRAIDDiskCheckVWCE` 写成 `=0` 被 `mmchconfig` 拒绝（它只收 yes / no），`ece_slots` 失败（2026-10-07）
6. 物理盘的服务器地址取了 `mmlspdisk` 的 `server`（服务恢复组的节点），15 块盘全成了 ece3；`device` 的 `//节点/dev/` 前缀没去掉（2026-10-07）
7. 按 `vdiskset list --vdisk-set X -Y` 找汇总段（只有不带参数的列表才有）：`create_vs` 重试报 already exists；拆除跳过纠删码卷、删恢复组失败后错误地走兜底停了服务器（2026-10-07）
8. 安装包带 `gpfs.gnr` 时没装 `lsscsi`，`mmdiscovercomp` 失败（`mmgssconfig discoverinfo` 找不到 `/usr/bin/lsscsi`）（2026-10-07 物理机）
9. 根日志组不在服务器列表第一台，三台报 `gnr_rg_not_primary`（2026-10-07 物理机）
10. 节点上留着同版本的 `gpfs.base`（删副本集群时没卸包）时纠删码的包不装：按每个包核对（2026-10-07 代码审查）
11. `resolve_disks` 收到 `nsddevices: false` 仍写设备出口（jq 的 `//` 把 false 当空）（2026-10-07 代码审查）
12. 恢复组读不全物理盘（`mmlspdisk` 失败或返回空）时步骤照样成功，到收尾才失败：现在步骤失败（2026-10-07 代码审查）
13. 读不到节点类成员时日志组总数算成 1，推迟的日志盘不补：现在按盘表达式的服务器数核对（2026-10-07 代码审查）
14. 进度监视残留的 `sleep` 占着集群锁，下一步多等最多 30 秒（2026-10-07 代码审查）
15. 磁盘拓扑与根日志组按固定列号读 `-Y` 输出：改为按表头取列（2026-10-07 代码审查）
16. 副本模式收尾重跑时盘的 `fs_id` 被写成 0（两种布局的收尾合并时顺带修，2026-10-07 代码审查）
