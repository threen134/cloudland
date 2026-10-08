# TC-26 GPFS 共享 LUN（SAN）与多集群远程挂载

优先级 **P2** · GPFS 的第三种布局（SAN 共享 LUN 做 NSD）与两个 GPFS 集群之间的远程挂载 · 约 120 min

2026-10-07 按 `docs/architecture/plan/shared-storage-design.md` §7.10、§7.11（S7 第二轮）写的用例。两项都在代码层面实现，**一条都还没在真实节点上执行**：
- SAN 要 FC / iSCSI / 多路径的共享 LUN（work-x 没有）。
- 远程挂载要两个平台部署的 GPFS 集群（一台节点只能属于一个 GPFS 集群，work-x 三台只够一个三节点集群，或一个两节点 + 一个单节点测试集群）。

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

## SAN-01 扫描与分类（未执行，E10）

1. 三台节点登录同一组 iSCSI LUN（每个 LUN 两条路径，`multipathd` 合成多路径设备），扫描磁盘

**预期**：
- 每台的磁盘列表有 `mpathX`（状态「疑似共享」，标识 `wwn-0x…` 在三台上相同）。
- 下层的 `sdX` 是「使用中」，说明「a path of multipath device mpathX」。

## SAN-02 参数与认领校验（未执行，E10）

1. `layout: san` 加一块本地盘
2. 一个 LUN 勾在 9 台节点下
3. `data_replicas: 2`
4. 另一个集群已认领的 LUN（在别的节点上认领的）

**预期**：都是 400（本地盘「no shared LUN」、「at most 8 servers」、「one copy」、「belongs to storage cluster」）

## SAN-03 部署（未执行，E10）

1. 向导选「GPFS 共享磁盘（SAN）」，LUN a 勾在三台下、LUN b 勾在两台下，部署

**预期**：
- 预检：每块 LUN 一项「shared LUN」通过（回归点 6）；勾了擦除的为警告。
- `resolve_disks`：每个 LUN 只有最小节点查空。
- `create_nsd`：两个 NSD（`cl<ID>s1` 服务器三台、第一台轮转到第二台；`cl<ID>s2` 两台），设备是第一台服务器上的 `/dev/mapper/…`。
- `create_fs`：故障组 1、数据和元数据各一份。
- 每台的 `/var/mmfs/etc/nsddevices` 列出 `dm-N dmm`。
- `mmlsnsd -X` 显示各台都看到两个 NSD；文件系统在所有节点挂上。

## SAN-04 加节点：已有 LUN 与新 LUN（未执行，E10）

1. 第四台节点登录 LUN a 和一个新 LUN c，加入集群（两块都勾上）；**第四台的 hostid 要比 LUN a 现有的服务器都小**（或先用一台 hostid 小的节点做这一步），在 LUN a 上勾「擦除」试一次再取消
2. 对 LUN a 直接调接口带 `wipe: true`

**预期**：
- 界面上 LUN a 不给「擦除」，显示「集群已在使用这个 LUN」；LUN c 可以擦除。
- `resolve_disks` 在新节点上：LUN a 带 `in_use`、只核对身份（日志「identity only」），不查空、不擦，即使它的 hostid 最小；LUN c 查空。
- `create_nsd` 只做 LUN c。
- `san_servers` 用 `mmchnsd` 把 LUN a 的服务器改成四台，文件系统不卸载、I/O 不中断。
- `add_disks` 只加 LUN c。
- 第 2 步：400「LUN … holds data of this cluster already: it can not be wiped」。

## SAN-05 移除节点（未执行，E10）

1. 移除 SAN-04 的节点

**预期**：
- `san_servers` 把它从 LUN a 拿掉。
- LUN c 只由它服务，`mmdeldisk`（数据先迁走）。
- `leave` 时 LUN a 不擦（标 `shared`），LUN c 擦。

## SAN-06 移除 LUN（未执行，E10）

1. 磁盘列表里移除 LUN b 在任一节点上的那一行

**预期**：两台上的记录都进入移除；`mmdeldisk` 一次；只有最小节点擦盘；两条记录都删掉

## SAN-07 删除集群（未执行，E10）

**预期**：每个 LUN 只擦一次（最小节点），其余节点 `leave` 时跳过

## RMT-01 建远程挂载（未执行，E11）

1. 集群 A 的「文件系统」标签「远程挂载到其他集群」：选 `fs1`、挂载方选 B
2. 挂载方已有同名文件系统时再建；选自己；选 Ceph 集群

**预期**：
- 第 1 步：跳到挂载方 B 的任务详情，四步依次在 A、B、A、B 的管理节点上执行。
- 两个密钥步骤之后，两边 `mmauth show .` 的 Cipher list 都是 `AUTHONLY`（原来没设的；管理员设过别的保留）。运行中改不了时步骤失败、提示可能要停掉整个集群的 GPFS——记下实际行为（E11 的一部分）。
- 结束后 B 的所有节点挂着 `/gpfs/fs1`（`mmlsmount fs1 -L` 列出两边的节点），两边的「远程挂载」列表都有这条（「由 B 挂载」/「挂载自 A」），状态「已挂载」。
- 第 2 步：400（同名或同挂载点、自己）；Ceph 集群不在下拉里。

## RMT-02 挂载方节点使用所属方的池（未执行，E11）

1. A 的 `fs1` 上有共享池 `p1`；等 5 分钟（或挂载完成时）
2. 在 B 的一台节点上用 `p1` 建卷挂给云服务器、用 `p1` 放系统盘建云服务器；把云服务器在 A、B 的节点之间热迁移

**预期**：
- 第 1 步：B 的节点上有 `run/storage/<A 的 uuid>/shared_pools.json` 和 `remote` 标记文件，`p1` 在 B 的节点上「可用」；B 的节点不生成 `cloudland_storage_<A 的 uuid>.prom`，A 的「节点」曲线只数 A 自己的成员。
- 第 2 步：建卷、系统盘克隆、热迁移都正常；路径两边相同（同挂载点）。

## RMT-03 挂载方节点宕机（未执行，E11）

1. B 的一台节点上有用 `p1` 的云服务器，断电这台

**预期**：
- 疏散时隔离发生在 B（它自己的集群）而不是 A。
- 被隔离后它不能再访问 `/gpfs/fs1`（核对 A 上 `mmlsmount` 不再列它）。
- 云服务器在别的节点启动、数据完好。

## RMT-04 拒绝（未执行，E11）

1. 删除集群 A；删除 A 的 `fs1`；B 的节点上的云服务器还用着 `p1` 的卷时取消挂载

**预期**：都拒绝（删集群「remote mount(s) … first」，删文件系统「Other clusters mount file system fs1」，取消 409）

## RMT-05 取消远程挂载（未执行，E11）

1. 迁走 / 删掉 B 上用 `p1` 的云服务器后，在 A 的列表里「取消远程挂载」

**预期**：
- 跳到 B 的任务详情：两个密钥步骤，`remote_unmount`（`forget_cluster: true`），`remote_revoke`（`forget_cluster: true`）。
- 结束后 B 的节点不再挂 `/gpfs/fs1`，`mmremotecluster show` 与 A 的 `mmauth show` 都没有对方。
- B 的节点上 `p1` 的行删除、收到空清单。

## RMT-07 挂载方集群的节点退出（未执行，E11）

1. 远程挂载就绪、B 的一台节点 H 上 `p1` 可用时，把 H 从 B 移除

**预期**：
- H 在 `p1` 上的记录随移除删掉（不再停在「可用」），之后建卷、疏散、迁移都不会选 H 放 `p1` 的盘。
- H 收到所属方 A 的空池清单，`run/storage/<A 的 uuid>/` 下的池清单变空、`p1` 的探测停止。

## RMT-06 同一对集群两个文件系统（未执行，E11）

1. A 的 `fs1`、`fs2` 都远程挂到 B，然后只取消 `fs1`

**预期**：取消 `fs1` 时 `forget_cluster: false`，B 仍认得 A、A 仍授权 B 的 `fs2`；`fs2` 照常

## 回归点

两项都没在真实环境跑过。沙箱里发现并修掉的：节点脚本在 `$( )` 里调 `stc_fail` 时失败消息丢失，改为直接调用、用全局变量返回结果。

2026-10-07 `/code-review` 修掉的（都没在真机上验证；设计 §16 S7「第二轮的代码审查修复」）：

1. **给已在用的 LUN 加一台 hostid 更小的服务器会擦掉正在用的 NSD**：查空 / 擦除的节点原来在所有服务这个 LUN 的节点里取最小的。现在只有新 LUN 才查空，在用 LUN 的新服务器只核对身份；对在用 LUN 请求擦除返回 400，界面上不给「擦除」（SAN-04 的第 1 步加一台 hostid 比现有服务器小的节点时核对）
2. **`stc_resolve_disks.sh` 不读 `in_use`**：新服务器加入已有 LUN 必然报「has data on it」（SAN-04 原来一定失败）
3. **远程挂载不设 cipherList**：平台部署的集群 cipherList 为空，按 IBM 流程两边都要设。`key` 步骤在没有设置时改为 AUTHONLY（RMT-01 核对两边 `mmauth show .` 的 Cipher list；在守护进程运行时能不能改是 E11 的一部分）
4. **挂载方集群的节点退出后，它在所属方池上的记录永远停在「可用」**：退出收尾一起删、并发空清单（RMT-07）
5. **挂载方节点用所属方的 uuid 报本机集群的指标**：远程挂载的池清单带 `remote` 标记，指标脚本跳过（RMT-02 核对所属方的「节点」曲线只算自己的成员）
6. **SAN 布局在预检就失败**（2026-10-07 设计复查）：预检输入里共享 LUN 的身份不带标记，节点的 `check_disks` 把 `shared` 判为不能用，SAN 的部署和带 LUN 的加节点一定停在预检。现在带 `shared: true`，节点只核对身份和没被挂载（多路径设备的某条路径判 `in_use`，不通过）；SAN-03 的预检全部通过即为修好。PostgreSQL `TestStorageGPFSSANPG`、WSL `stc-test16.sh` 已核对

## 清理

- SAN：删除集群；三台登出 iSCSI、删掉 target 上的 LUN；`multipath -F`
- 远程挂载：取消所有远程挂载后再删两个集群；恢复 gpfs1
