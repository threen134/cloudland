# TC-26 GPFS 共享 LUN（SAN）与多集群远程挂载

优先级 **P2** · GPFS 的第三种布局（SAN 共享 LUN 做 NSD）与两个 GPFS 集群之间的远程挂载 · 约 120 min

2026-10-07 按 `docs/architecture/plan/shared-storage-design.md` §7.10、§7.11（S7 第二轮）写的用例。两项都在代码层面实现，当时没有真实环境：
- SAN 要 FC / iSCSI / 多路径的共享 LUN（work-x 没有）。
- 远程挂载要两个平台部署的 GPFS 集群（一台节点只能属于一个 GPFS 集群，work-x 三台只够一个三节点集群，或一个两节点 + 一个单节点测试集群）。

**2026-10-11 work-04 加入后在 work-01–04 上跑过**（执行记录 `runs/2026-10-10-b017569d+work-04实验.md`，环境见下面「测试环境」）：SAN-01–07 通过（SAN-02 只做了一部分），RMT-01、02、04、05、06 通过，RMT-03、RMT-07 SKIP（挂载方集群只有一台）；修的问题见回归点 7–14。

每条用例的标题后括号里是设计 §16 S7 的实验编号。

沙箱覆盖（都通过）：
- PostgreSQL `TestStorageGPFSSANPG`：部署、加带已有 LUN 与新 LUN 的节点、移除节点、移除 LUN、删除，核对 NSD 服务器列表、`mmchnsd`、只擦一次。
- PostgreSQL `TestStorageRemoteMountPG`：挂载四步的输入、池清单与上报、疏散时隔离在哪个集群、拒绝、取消挂载。
- 单元 `TestGPFSSANLayout` / `TestGPFSSANNamesAndWipes` / `TestGPFSSANChangePlans`。
- WSL `gpfs-spike/stc-test16.sh`（SAN 节点脚本，15 项，含 `in_use`）、`stc-test17.sh`（远程挂载节点脚本，17 项，含 cipherList）。
- 本机界面 `pw/stc-ui-s7.js`（SAN 向导、远程挂载的列表 / 建 / 删）。

## 设计约定（判定预期的依据）

**SAN（`layout: san`）**：
- 扫描列出多路径设备（`/dev/mapper/<名>`，状态 `shared`）；它的各条路径（`sdX`）是 `in_use`「多路径设备 X 的一条路径」，不能认领；FC / iSCSI / WWN 重复的盘也是 `shared`。
- 在每台要服务某个 LUN 的节点下勾选同一个 LUN（同一个标识）：每台一条记录，这些节点是它的 NSD 服务器（最多 8 台）。
- `san` 布局只收 `shared` 的盘，其他布局照旧拒绝；别的集群在任何节点上认领过的 LUN 拒绝。
- 一个 LUN 一个 NSD，名字 `cl<集群>s<n>`；服务器列表的第一台按 LUN 序号轮转；故障组 1；数据、元数据各一份（`data_replicas` 只能是 1）；`nsddevices` 把 `dm-N` 报成 `dmm`。
- 只有 LUN 的最小节点查空 / 擦除，其他节点只核身份；释放、离开时 LUN 还被别的节点服务就不擦，整个离开时只擦一次。
- 已有 LUN 多了或少了服务器用 `mmchnsd`（步骤 `san_servers`，在线）；只由离开的节点服务的 LUN 才 `mmdeldisk`。
- 移除盘 = 移除整个 LUN（各节点上的记录一起）。
- 不支持换盘、第二个文件系统。

**远程挂载**：
- 所属、挂载方都是平台部署的就绪 GPFS 集群，不能是同一个。
- 以原名、原挂载点挂载，挂载方不能已有同名或同挂载点的文件系统。
- 挂载方集群的任务 `remote_mount`：
  1. 所属方的密钥（`mmauth genkey new` + `commit`，已有不重做）。
  2. 挂载方的密钥。
  3. 所属方授权：`mmauth add` 或 `update`，再 `mmauth grant -f <fs> -a rw`。
  4. 挂载方挂载：`mmremotecluster add` 或 `update`（联系节点是所属方的仲裁节点，最多 8 个），`mmremotefs add <fs> -f <fs> -C <所属方> -T <挂载点> -A yes`，`mmmount -a`。
- 取消（`remote_unmount`）：
  - 挂载方 `mmumount -a`、`mmremotefs delete`，不再挂所属方的任何文件系统时 `mmremotecluster delete`。
  - 所属方 `mmauth deny`，不再给任何文件系统时 `mmauth delete`。
  - 挂载方节点上的云服务器还在用那个文件系统上的卷时拒绝。
- 所属方的池（在那个文件系统上的）发给挂载方的节点（所属方 uuid 下），它们的上报被接受。
- 挂载方节点疏散时在自己的集群里隔离。
- 有远程挂载的集群不能删，被远程挂载的文件系统不能删。

## 测试环境

- SAN：要一台 iSCSI target（或 FC 阵列）给三台节点同时导出 2–3 个 LUN，节点上装 `multipath-tools` 并能看到 `/dev/mapper/mpathX`。可以用一台不在集群里的机器跑 `targetcli` 导出文件做的 LUN 给三台节点（iSCSI 登录 + 多路径），**要用户同意**。
- 远程挂载：两个 GPFS 集群，例如 work-01 + work-02 两节点（仲裁 1 台，测试布局）与 work-03 单节点测试集群；**要先删 gpfs1 并重建，要用户同意**。
- ⚠️ **各节点的 iSCSI 发起端名字（`/etc/iscsi/initiatorname.iscsi`）必须唯一**：IBM 镜像四台相同。target 按发起端名字配 ACL，同名的发起端区分不开，各节点的会话会互相冲突。2026-10-10 改成了 `iqn.2026-10.local.cloudland:<主机名>`（原文件备份 `/root/initiatorname.iscsi.bak-20261010`），改后重启 `iscsid`。
- 2026-10-11 实际用的环境：
  - SAN：work-04 上的 LIO iSCSI target（`iqn.2026-10.local.cloudland.work-04:san`，`10.191.202.21` 的 3260、3261 两个门户形成两条路径，`/var/lib/cl-ece/san/` 下 20 GiB 文件做的 LUN a / b，SAN-04 时加 LUN c）。work-02、03、04 登录并装 `multipath-tools`，看到 `mpatha` / `mpathb`；SAN-04 时 work-01 也登录。集群 gsan。写入探测 work-03 `/root/w4-sanio.sh`。
  - 远程挂载：A = 三台的 gpfs1（work-01–03），B = work-04 的单节点测试集群 gpfsb，B 挂 A 的 `fs1`、`fs2`。

## SAN-01 扫描与分类（2026-10-11 work-x 通过，E10）

> 2026-10-11：先把各节点的 iSCSI 发起端名字改成唯一的（见「测试环境」）。三台 `mpatha` / `mpathb` 是 `shared`、标识相同；路径 sdX 是 `in_use`「a path of multipath device」。

1. 三台节点登录同一组 iSCSI LUN（每个 LUN 两条路径，`multipathd` 合成多路径设备），扫描磁盘

**预期**：
- 每台的磁盘列表有 `mpathX`（状态「疑似共享」，标识 `wwn-0x…` 在三台上相同）。
- 下层的 `sdX` 是「使用中」，说明「a path of multipath device mpathX」。

## SAN-02 参数与认领校验（2026-10-11 work-x 部分通过：第 2、4 步没测，E10）

> 2026-10-11：本地盘 400、`data_replicas: 2` 400。第 2 步要 9 台节点，第 4 步要另一个 SAN 集群认领过的 LUN，都没测（PG `TestStorageGPFSSANPG` 覆盖）。

1. `layout: san` 加一块本地盘
2. 一个 LUN 勾在 9 台节点下
3. `data_replicas: 2`
4. 另一个集群已认领的 LUN（在别的节点上认领的）

**预期**：都是 400（本地盘「no shared LUN」、「at most 8 servers」、「one copy」、「belongs to storage cluster」）

## SAN-03 部署（2026-10-11 work-x 通过，E10）

> 2026-10-11：第一次预检失败（回归点 8），修后部署成功；`create_cluster` 有一次偶发的 CCR 失败，重试即过。查空只在最小节点；`cl15s1` 服务器 work-03、04、02（轮转），`cl15s2` 服务器 work-03、04；`nsddevices` 报 `dmm`；故障组 1、各一份；三台都挂上。

1. 向导选「GPFS 共享磁盘（SAN）」，LUN a 勾在三台下、LUN b 勾在两台下，部署

**预期**：
- 预检：每块 LUN 一项「shared LUN」通过（回归点 6）；勾了擦除的为警告。
- `resolve_disks`：每个 LUN 只有最小节点查空。
- `create_nsd`：两个 NSD（`cl<ID>s1` 服务器三台、第一台轮转到第二台；`cl<ID>s2` 两台），设备是第一台服务器上的 `/dev/mapper/…`。
- `create_fs`：故障组 1、数据和元数据各一份。
- 每台的 `/var/mmfs/etc/nsddevices` 列出 `dm-N dmm`。
- `mmlsnsd -X` 显示各台都看到两个 NSD；文件系统在所有节点挂上。

## SAN-04 加节点：已有 LUN 与新 LUN（2026-10-11 work-x 通过，界面「不给擦除」没看，E10）

> 2026-10-11：加 work-01（hostid 最小）：LUN a「identity only」，只建 LUN c，`mmchnsd` 在线把 LUN a 改成四台服务器；LUN a 带 `wipe` 400；期间写入 363 次 0 失败。界面上 LUN a 不给「擦除」这条没在浏览器里看（本机 `pw/stc-ui-s7.js` 覆盖）。

1. 第四台节点登录 LUN a 和一个新 LUN c，加入集群（两块都勾上）；**第四台的 hostid 要比 LUN a 现有的服务器都小**（或先用一台 hostid 小的节点做这一步），在 LUN a 上勾「擦除」试一次再取消
2. 对 LUN a 直接调接口带 `wipe: true`

**预期**：
- 界面上 LUN a 不给「擦除」，显示「集群已在使用这个 LUN」；LUN c 可以擦除。
- `resolve_disks` 在新节点上：LUN a 带 `in_use`、只核对身份（日志「identity only」），不查空、不擦，即使它的 hostid 最小；LUN c 查空。
- `create_nsd` 只做 LUN c。
- `san_servers` 用 `mmchnsd` 把 LUN a 的服务器改成四台，文件系统不卸载、I/O 不中断。
- `add_disks` 只加 LUN c。
- 第 2 步：400「LUN … holds data of this cluster already: it can not be wiped」。

## SAN-05 移除节点（2026-10-11 work-x 通过，E10）

> 2026-10-11：第一次 LUN c 擦不掉（回归点 9），修后：`mmchnsd` 去掉 work-01，LUN c `mmdeldisk`；LUN a 不擦、LUN c 擦。

1. 移除 SAN-04 的节点

**预期**：
- `san_servers` 把它从 LUN a 拿掉。
- LUN c 只由它服务，`mmdeldisk`（数据先迁走）。
- `leave` 时 LUN a 不擦（标 `shared`），LUN c 擦。

## SAN-06 移除 LUN（2026-10-11 work-x 通过，E10）

> 2026-10-11：删一行两行都走，`mmdeldisk` 一次，只有 work-03 擦（盘上原有的 kpartx 映射由回归点 9 的修复处理）。

1. 磁盘列表里移除 LUN b 在任一节点上的那一行

**预期**：两台上的记录都进入移除；`mmdeldisk` 一次；只有最小节点擦盘；两条记录都删掉

## SAN-07 删除集群（2026-10-11 work-x 通过，E10）

**预期**：每个 LUN 只擦一次（最小节点），其余节点 `leave` 时跳过；没擦盘的节点也去掉本机按旧分区表建的 kpartx 映射（回归点 10）

> 2026-10-11：LUN a 只由 work-02 擦一次；整个 SAN 测试期间写入 62883 次 0 失败（9.5 小时）。当时没擦盘的节点上留着 kpartx 映射，2026-10-12 修了（回归点 10），没有 SAN 环境了，修复没在真机复测。

## SAN-08 SAN 集群上的其余能力（2026-10-14 按代码核对补，未执行，E10）

SAN 布局的能力（`LayoutCapabilities`）还有存储池、重新均衡、改角色、轮换密钥、升级与完成升级、远程挂载，不支持换盘与第二个文件系统；2026-10-11 只测了 SAN-01–07 的结构操作。要 SAN 环境（见「测试环境」，要用户同意）。

1. 在 SAN 集群上建共享池、建卷挂给云服务器、系统盘放在这个池里建一台云服务器，再热迁移到另一台服务这些 LUN 的节点
2. 服务 LUN a 的一台节点停掉 GPFS（或断电），另一台节点上的云服务器持续写
3. 加一个新 LUN 后「重新均衡」
4. 改角色（给一台加管理，或换一台仲裁，仲裁仍为奇数）；轮换集群 SSH 密钥
5. 直接调 `POST /storage_clusters/:id/disks/:disk_id/replace`、`POST /storage_clusters/:id/filesystems`
6. （有新版本安装包时）滚动升级与完成升级

**预期**：
- 第 1 步：与副本集群相同：fileset 带配额、卷跨节点挂、系统盘克隆、迁移不复制共享盘。
- 第 2 步：GPFS 把 LUN a 的 I/O 切到它的下一台 NSD 服务器（`mmlsnsd -X` 看服务器列表），写入只短暂停顿、不报错；那台节点离线满 5 分钟后照常疏散（`mmexpelnode` 隔离）。
- 第 3 步：`mmrestripefs -b` 成功。
- 第 4 步：与副本集群相同（TC-23 OPS-09、TC-24 ROT-01）。
- 第 5 步：400「gpfs clusters (san) do not support this operation yet」；界面里磁盘行没有「换盘」、文件系统标签没有「新建文件系统」。
- 第 6 步：与副本集群相同（TC-24 UPG-01 / 02），升级期间 LUN 由其他服务器继续服务。

## RMT-01 建远程挂载（2026-10-11 work-x 通过，E11）

> 2026-10-11（A = gpfs1，B = gpfsb）：挂自己、挂 Ceph 集群、不存在的文件系统都 400（选 Ceph 时报错的括号是空的，回归点 13）；四步在 A、B、A、B 上 63 秒；`mmlsmount fs1 -L` 列出 B 的节点；重复挂载 400。Cipher list 的结果执行记录里没有单独记。

1. 集群 A 的「文件系统」标签「远程挂载到其他集群」：选 `fs1`、挂载方选 B
2. 挂载方已有同名文件系统时再建；选自己；选 Ceph 集群

**预期**：
- 第 1 步：跳到挂载方 B 的任务详情，四步依次在 A、B、A、B 的管理节点上执行。
- 两个密钥步骤之后，两边 `mmauth show .` 的 Cipher list 都是 `AUTHONLY`（原来没设的；管理员设过别的保留）。运行中改不了时步骤失败、提示可能要停掉整个集群的 GPFS——记下实际行为（E11 的一部分）。
- 结束后 B 的所有节点挂着 `/gpfs/fs1`（`mmlsmount fs1 -L` 列出两边的节点），两边的「远程挂载」列表都有这条（「由 B 挂载」/「挂载自 A」），状态「已挂载」。
- 第 2 步：400（同名或同挂载点、自己）；Ceph 集群不在下拉里。

## RMT-02 挂载方节点使用所属方的池（2026-10-11 work-x 通过，E11）

> 2026-10-11：第一次 B 的节点上迟到的池清单把 A 的集群目录建回来，指标以 A 的名义多报了节点和 B 的 `fsb`（回归点 7），修后：gp1 在 work-04 上 ready；gp1 的卷挂到 work-04 的云服务器；gp1 系统盘的云服务器 work-04 ↔ work-01 热迁移各 10 秒、`shared: true`。

1. A 的 `fs1` 上有共享池 `p1`；等 5 分钟（或挂载完成时）
2. 在 B 的一台节点上用 `p1` 建卷挂给云服务器、用 `p1` 放系统盘建云服务器；把云服务器在 A、B 的节点之间热迁移

**预期**：
- 第 1 步：B 的节点上有 `run/storage/<A 的 uuid>/shared_pools.json` 和 `remote` 标记文件，`p1` 在 B 的节点上「可用」；B 的节点不生成 `cloudland_storage_<A 的 uuid>.prom`，A 的「节点」曲线只数 A 自己的成员。
- 第 2 步：建卷、系统盘克隆、热迁移都正常；路径两边相同（同挂载点）。

## RMT-03 挂载方节点宕机（2026-10-11 SKIP：挂载方集群只有一台，E11）

> 四台机器里 A 占了三台，B 只有 work-04 一台，没有别的管理节点执行隔离。要两个各 2 台以上的集群（等 gece 删掉后用四台各建一个两节点集群）。

1. B 的一台节点上有用 `p1` 的云服务器，断电这台

**预期**：
- 疏散时隔离发生在 B（它自己的集群）而不是 A。
- 被隔离后它不能再访问 `/gpfs/fs1`（核对 A 上 `mmlsmount` 不再列它）。
- 云服务器在别的节点启动、数据完好。

## RMT-04 拒绝（2026-10-11 work-x 通过，E11）

> 2026-10-11：删集群 A、删 `fs1` 时先撞上「还有池」的检查；删掉 gp2 后删 `fs2` 报「Other clusters mount file system fs2」；有云服务器在用时取消挂载 409。

1. 删除集群 A；删除 A 的 `fs1`；B 的节点上的云服务器还用着 `p1` 的卷时取消挂载

**预期**：都拒绝（删集群「remote mount(s) … first」，删文件系统「Other clusters mount file system fs1」，取消 409）

## RMT-05 取消远程挂载（2026-10-11 work-x 通过，E11）

> 2026-10-11：两边都没有对方（`mmremotecluster`、`mmauth`），列表空，B 上的池清单是 `[]`。

1. 迁走 / 删掉 B 上用 `p1` 的云服务器后，在 A 的列表里「取消远程挂载」

**预期**：
- 跳到 B 的任务详情：两个密钥步骤，`remote_unmount`（`forget_cluster: true`），`remote_revoke`（`forget_cluster: true`）。
- 结束后 B 的节点不再挂 `/gpfs/fs1`，`mmremotecluster show` 与 A 的 `mmauth show` 都没有对方。
- B 的节点上 `p1` 的行删除、收到空清单。

## RMT-07 挂载方集群的节点退出（2026-10-11 SKIP：同 RMT-03，E11）

> 挂载方集群只有一台，移除它就是删集群，没测。节点侧离开时清空远程池清单（回归点 11）在 TC-25 ECE-10 移除 work-04 时用预置的清单真机验证过；所属方池记录随移除删掉只有 PG `TestStorageRemoteMountPG` 覆盖。

1. 远程挂载就绪、B 的一台节点 H 上 `p1` 可用时，把 H 从 B 移除

**预期**：
- H 在 `p1` 上的记录随移除删掉（不再停在「可用」），之后建卷、疏散、迁移都不会选 H 放 `p1` 的盘。
- H 收到所属方 A 的空池清单，`run/storage/<A 的 uuid>/` 下的池清单变空、`p1` 的探测停止。

## RMT-06 同一对集群两个文件系统（2026-10-11 work-x 通过，E11）

> 2026-10-11：只取消 `fs1` 后 B 仍认得 A、只剩 `fs2`，A 只授权 `fs2`。

1. A 的 `fs1`、`fs2` 都远程挂到 B，然后只取消 `fs1`

**预期**：取消 `fs1` 时 `forget_cluster: false`，B 仍认得 A、A 仍授权 B 的 `fs2`；`fs2` 照常

## RMT-08 挂载之后的变化（2026-10-14 按代码核对补，未执行，E11）

池清单的周期同步（`SyncSharedPools`）也把所属方的池推给挂载方集群的成员（`syncRemotePoolLists`），所以挂载之后的变化不用重建挂载；2026-10-11 只测了挂载那一刻已有的池。

1. 挂载建好后，在所属方 A 的 `fs1` 上新建一个共享池 `p2`、改 `p1` 的配额
2. 挂载方 B 加一台节点（要第二台能进 B 的节点）
3. A 加一台客户端节点
4. 所属方或挂载方的一台节点重启后，看它的挂载

**预期**：
- 第 1 步：一个同步周期内（最多 5 分钟）B 的节点上 `p2` 出现并「可用」，`p1` 的容量跟着变；B 上建卷用 `p2` 正常。
- 第 2 步：新节点加入 B 后自动挂上远程的 `/gpfs/fs1`（`-A yes`），收到 A 的池清单（带 `remote` 标记），`p1` 在它上面可用。
- 第 3 步：A 的新节点是 GPFS 意义上的本地节点，不影响 B 的挂载（`mmremotecluster show` 的联系节点不变，只列仲裁节点）。
- 第 4 步：重启的节点开机后自动重新挂上远程文件系统，之后池探测 `ready`、云服务器从待启动列表启动。

## 回归点

沙箱里发现并修掉的：节点脚本在 `$( )` 里调 `stc_fail` 时失败消息丢失，改为直接调用、用全局变量返回结果。

2026-10-07 `/code-review` 修掉的（都没在真机上验证；设计 §16 S7「第二轮的代码审查修复」）：

1. **给已在用的 LUN 加一台 hostid 更小的服务器会擦掉正在用的 NSD**：查空 / 擦除的节点原来在所有服务这个 LUN 的节点里取最小的。现在只有新 LUN 才查空，在用 LUN 的新服务器只核对身份；对在用 LUN 请求擦除返回 400，界面上不给「擦除」（SAN-04 的第 1 步加一台 hostid 比现有服务器小的节点时核对）
2. **`stc_resolve_disks.sh` 不读 `in_use`**：新服务器加入已有 LUN 必然报「has data on it」（SAN-04 原来一定失败）
3. **远程挂载不设 cipherList**：平台部署的集群 cipherList 为空，按 IBM 流程两边都要设。`key` 步骤在没有设置时改为 AUTHONLY（RMT-01 核对两边 `mmauth show .` 的 Cipher list；在守护进程运行时能不能改是 E11 的一部分）
4. **挂载方集群的节点退出后，它在所属方池上的记录永远停在「可用」**：退出收尾一起删、并发空清单（RMT-07）
5. **挂载方节点用所属方的 uuid 报本机集群的指标**：远程挂载的池清单带 `remote` 标记，指标脚本跳过（RMT-02 核对所属方的「节点」曲线只算自己的成员）
6. **SAN 布局在预检就失败**（2026-10-07 设计复查）：预检输入里共享 LUN 的身份不带标记，节点的 `check_disks` 把 `shared` 判为不能用，SAN 的部署和带 LUN 的加节点一定停在预检。现在带 `shared: true`，节点只核对身份和没被挂载（多路径设备的某条路径判 `in_use`，不通过）；SAN-03 的预检全部通过即为修好。PostgreSQL `TestStorageGPFSSANPG`、WSL `stc-test16.sh` 已核对

> 1–6 的真机情况（2026-10-11）：1、2 在 SAN-04 通过（LUN a「identity only」、带 `wipe` 400）；6 在 SAN-03 通过（另修了回归点 8 之后）；3 的 Cipher list 没单独记；4 没测（RMT-07 SKIP）；5 不够，见回归点 7。

2026-10-11 / 12 work-04 实验与之后两轮代码审查修掉的（执行记录 `runs/2026-10-10-b017569d+work-04实验.md`，第 N 条 / R / S 是记录里的编号）：

7. **节点离开集群后，迟到的池清单把集群目录建回来**，指标脚本以所属方的名义上报（远程挂载后 2 分钟里 A 的节点数多了、多出 B 的 `fsb`）（第 8 条）。同 TC-20 回归点 37（池清单不再发给 `leaving` 的节点，`leave` 留 `.left-<uuid>` 标记）；RMT-02
8. **SAN 的多路径 LUN 预检必然失败**：`lsblk` 对 dm 设备不给 WWN，身份核对读到空（第 12 条）。`disk_identity` 退回 udev 的 `DM_WWN` / `ID_WWN`；SAN-03
9. **SAN 的 LUN 擦不掉**：NSD 有分区表，multipathd 用 kpartx 建的分区映射占着多路径设备，`wipefs` 报 busy（第 13 条）。`wipe_disk` 对 mpath 设备先 `kpartx -d`；SAN-05、SAN-06
10. **删 SAN 集群后，没擦盘的节点上留着按旧分区表建的 kpartx 映射**（2026-10-12）。`leave` 与 `release_disks` 遇到由别的节点擦的共享 LUN，核对身份后也去掉本机的分区映射（`storage_lib.sh` 的 `drop_partition_maps`，`wipe_disk` 共用）；离线移除的节点回来补清理时，`shared` 的行一律保留在清单里，否则被留下的节点认领着就走不到这一步（第二轮代码审查 S12）。没有 SAN 环境了，没在真机复测；SAN-07
11. **离线移除的节点回来后，经本集群远程挂载得到的别的集群的池清单没人清**，探测一直跑、clapi 一直记日志（第一轮代码审查 R2）。GPFS `backend_leave` 把本机所有带 `remote` 标记的清单置空（停探测）后删掉（一台主机只属于一个 GPFS 集群）；TC-25 ECE-10 用预置的清单真机验证；RMT-07
12. **「移除残留认领」原来对所有布局开放**：SAN 加节点中止后，加入中节点那几条「只核对身份」的 LUN 记录看起来就是残留认领，移除会擦掉别的节点正在用的 LUN（第二轮代码审查 S1）。只对纠删码开放（`storageStrayChecks`），SAN 照常走正规移除；同 TC-25 回归点 30，PG `TestStorageStrayClaimPG`
13. **远程挂载选 Ceph 集群的报错是「ceph clusters () do not support this operation yet」，括号是空的**（2026-10-12）。没有布局的类型不带括号（`storageUnsupportedOp`，单元 `TestStorageUnsupportedOp`）；RMT-01 第 2 步
14. **补清理的擦盘清单会误擦经另一台节点认领的共享 LUN**（第一轮代码审查 R4）。同 TC-20 回归点 44

## 清理

- SAN：删除集群；三台登出 iSCSI、删掉 target 上的 LUN；`multipath -F`
- 远程挂载：取消所有远程挂载后再删两个集群；恢复 gpfs1
