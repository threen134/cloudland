---
order: 100
---
# 部署共享存储（Ceph）

Ceph 是第二种共享存储：云硬盘是 Ceph 集群里的 RBD 镜像，能访问集群的任何一台计算节点上的云服务器都能挂载它，迁移云服务器时这块盘不用复制。本文讲如何在 CloudLand 界面上用 cephadm 部署 Ceph 集群、导入已有的 Ceph 集群、建 RBD 存储池，以及日常运维。概念、任务、共享池的用法与 GPFS 相同的部分见[部署共享存储（GPFS）](./09-shared-storage.md)，这里只写 Ceph 不同的地方。

设计细节见 `docs/architecture/plan/shared-storage-design.md`（§8；§16 S3 有实施记录）。

> **现在能做什么**（2026-10-04）：
>
> - **能**：部署三台及以上的 Ceph 集群（3 副本）；导入已有的 Ceph 集群；建 RBD 存储池（托管集群按介质建放置规则、可设配额；导入的集群登记已有的 RBD 池）；池里的**数据盘**（建、挂、在线扩容、删）与**系统盘**（S4：从池里的镜像副本 `image-<ID>-<前缀>@base` 克隆，克隆格式 v2）；带 RBD 盘的迁移（不复制）；加节点、加盘、移除盘、移除节点、离线移除
> - **也能**（S5）：健康检查与告警、监控曲线、任务的完整日志、换坏盘、改角色（挪 mon / mgr / 管理节点）、新节点自动加为客户端、孤儿对账
> - **还不能**：升级、宕机恢复（S6）。RBD 云服务器的 UEFI 变量放在所在节点上，迁移时照常复制
> - **验证情况**：先在本机 WSL 沙箱里用单节点集群跑通，2026-10-03 在三台 Ubuntu 24.04 节点（Ceph 19.2.3，OSD 在回环盘上）上验收：部署、建池、卷的跨节点挂载与扩容、带 RBD 盘的热迁移、池不可用、配额写满、加盘 / 移除盘、导入、删除、节点整机重启都通过；加节点 / 移除节点（只有三台）没测

---

## 与 GPFS 不同的地方

| | GPFS | Ceph |
|---|---|---|
| 软件 | 上传 IBM 安装包 | **不用上传**：节点从发行版源装 `ceph-common`、`cephadm`，守护进程以容器运行，镜像 `quay.io/ceph/ceph:v<版本>` |
| 节点系统 | Ubuntu 22.04 / 24.04 | Ubuntu 24.04（Ceph 19.2）/ 26.04（Ceph 20.2）。**一个集群的节点必须是同一个发行版**（各自装发行版自带的 Ceph，版本要一致），预检会拦下 |
| 角色 | 管理、仲裁、NSD、客户端 | 管理（`admin`，必须同时是 mon）、`mon`（奇数、至少 3）、`mgr`（1–2）、`OSD`（提供磁盘）、客户端 |
| 副本 | 数据 2 或 3 | 固定 **3 副本**，每份在不同节点（OSD 节点至少 3 台）；测试布局 1 副本 |
| 池 | 独立 fileset | RBD 池 `cl_<池 UUID 前 8 位>` |
| 卷 | qcow2 文件 | RBD 镜像 `volume-<卷 ID>`（raw） |
| 重新均衡 | 手动 | 没有这个操作：Ceph 在加盘、移除盘后自动重新分布数据 |

一台节点只能运行**一个** Ceph 集群的守护进程；作为客户端可以使用多个集群。

---

## 前置要求

| 项目 | 要求 |
|---|---|
| 节点系统 | Ubuntu 24.04 或 26.04，同一集群的节点同一个版本 |
| 容器 | 节点上有 docker（CloudLand 计算节点本来就装了） |
| 节点数 | mon 奇数且至少 3 台、mgr 1–2 台、OSD 节点至少 3 台；测试布局可以 1 台，只用于测试 |
| 磁盘 | 每台 OSD 节点至少一块空闲盘。ceph-volume 不收回环设备，测试用的回环盘由平台先建成 LVM 逻辑卷 |
| 网络 | 节点之间内网互通，端口 22、3300、6789、6800–7300；节点能访问 `quay.io` 拉镜像（或在参数里填私有仓库的镜像地址） |
| 空间 | mon 节点 `/var/lib/ceph` 所在文件系统至少 30% 空闲 |
| 内存 | 预留：mon 2 GiB、mgr 1 GiB、每个 OSD「内存目标 + 512 MiB」（默认内存目标 2 GiB） |
| 凭据 | 控制面配置了 `VPN_SECRET_KEY`（集群 SSH 密钥、客户端密钥都用它加密保存） |
| 时间同步 | chrony 或 ntpsec 在运行（cephadm 会检查） |

---

## 部署

界面「存储 → 存储集群」→「新建集群」→「部署 Ceph」：

| 步骤 | 内容 |
|---|---|
| 节点与磁盘 | 默认第一台是「管理 + mon + mgr + OSD」，其余「mon + OSD」；只当客户端的节点选「客户端」 |
| 参数 | 副本数（固定 3，测试布局 1）、OSD 内存目标（默认 2048 MiB）、容器镜像（留空 = 节点上 Ceph 的同版本镜像）、集群网络（OSD 之间复制用的网段，留空用公共网络）、私有镜像仓库（地址、用户、口令；口令加密保存，镜像要以仓库地址开头） |
| 预检 | 系统、容器运行时、时间同步、端口、`/var/lib/ceph` 空间、内存、磁盘、节点上是否已有别的 Ceph 集群（`existing`） |
| 确认 | 集群名，开始部署 |

**部署过程**（11 步）：预检 → 加入 → 安装（`ceph-common qemu-block-extra`，守护进程节点再加 `cephadm lvm2` 并拉镜像）→ SSH 信任 → 在第一台管理节点上 bootstrap（第一个 mon 与 mgr）→ 加主机（mon / mgr 按标签放置，等 mon 进入法定人数）→ 配置集群（副本数、OSD 内存、建客户端用户）→ 核对磁盘 → 建 OSD → 每台节点配置客户端 → 收尾。三台实测 11 分 38 秒（含装包与拉 2 GB 镜像 1 分 20 秒、bootstrap 2 分钟、加主机 1 分钟、建 3 个 OSD 5 分 20 秒）；单节点沙箱 2 分 42 秒。

部署后：

- **集群的 fsid 就是 CloudLand 里集群的 UUID**，在集群详情的「集群标识」里
- 节点上没有 cephadm 自带的监控容器（`--skip-monitoring-stack`），没有 dashboard
- OSD 只建在认领的盘上：`osd.all-available-devices` 服务是 `unmanaged`，节点上别的空闲盘不会被 cephadm 占用；20.2 的 `orch daemon add osd` 会另存一个 `osd.default` 规格，建完 OSD 后也设成 `unmanaged`，免得移除盘后 cephadm 在原盘上重建 OSD（19.2 没有这个规格）
- `osd_memory_target_autotune` 关闭，OSD 内存按参数固定（与节点上为它预留的内存一致）
- 守护进程节点上有 systemd drop-in `/etc/systemd/system/ceph-<fsid>@.service.d/cloudland-order.conf`，让本集群的 OSD 等守护进程排在本机 mon 之后：关机时 OSD 先停，它的下线通知送得到 mon。没有它时 OSD 的通知可能发给本机这个同时在停的 mon 而丢掉，集群要等心跳超时（约 13 秒）才发现，其余节点上云服务器的 I/O 卡这么久。`journalctl` 里 mon 单元的 `Dependency After=... is dropped` 警告是这条规则作用到 mon 自己身上，无害
- 同一个目录里还有 `cloudland-oom.conf`：mon、mgr、OSD 每次启动后，`ceph_oom_protect.sh` 把容器里的进程设为 `oom_score_adj -900`、给容器设 `memory.low`（mon 2 GiB、mgr 1 GiB、OSD 为 OSD 内存目标 + 512 MiB，与节点上为它们预留的内存一致）。节点内存耗尽时内核先杀云服务器，不杀 Ceph 守护进程。脚本经 `systemd-run` 在独立的临时单元里跑，守护进程的单元不等它。`system.slice` 的 `MemoryLow` 也会被设成本机守护进程之和（容器在 `system.slice` 下，上层没有保护时容器的 `memory.low` 不起作用），心跳每 5 分钟后台巡检一次、补上漏掉的（日志 `/opt/cloudland/log/storage/ceph_oom.log`）。核对要看真实进程，不看单元（单元的主进程只是 `docker run`）：`for c in mon mgr osd; do p=$(pgrep -x ceph-$c | head -1); echo "$c $(cat /proc/$p/oom_score_adj) $(cat /sys/fs/cgroup$(cut -d: -f3 /proc/$p/cgroup)/memory.low)"; done; cat /sys/fs/cgroup/system.slice/memory.low`，每个都应是 -900，最后一个数是前面三个之和（按本机部署的守护进程个数）；`journalctl` 里有 `oom_score_adj -900 (0 processes missed)` 一行。GPFS 不用处理：IBM 的 `gpfs.service` 已把 `mmfsd` 设为 -1000
- 每台节点有客户端配置 `/etc/ceph/<集群 UUID>.conf`（fsid 与 mon 地址）、密钥环 `/etc/ceph/<集群 UUID>.client.cloudland.keyring`（600），以及 libvirt secret（UUID 同样是集群 UUID，热迁移时目标节点认得）
- 管理节点上的管理员配置在 `/var/lib/ceph/<fsid>/config/`，排查时 `ceph --conf /var/lib/ceph/<fsid>/config/ceph.conf --keyring /var/lib/ceph/<fsid>/config/ceph.client.admin.keyring -s`（cephadm 另外也会写 `/etc/ceph/ceph.conf`）
- `authorized_keys` 里集群的 SSH 公钥只有一行，带 `from=`（管理节点与 mgr 节点）；cephadm bootstrap 自己写的那一行不带限制，部署时已删掉

**私有仓库与混合发行版**（2026-10-07 第二轮，未在真机验证）：填了私有仓库时每台节点先 `docker login` 再拉镜像，bootstrap 带仓库登录信息。跑守护进程的节点要是同一个发行版；纯客户端可以是另一个（比如 26.04 的客户端加入 24.04 的集群），但它的 `ceph-common` 不能比集群镜像旧。

**镜像版本**：容器镜像的 Ceph 不能比节点上的 `ceph-common` 新，否则新版本生成的密钥旧客户端读不出来（实测 20.2.4 的镜像配 20.2.0 的客户端，密钥报 `Malformed input`），安装步骤会拒绝。留空时用的就是节点上 Ceph 的同版本镜像；**不要用 `:v19`、`:v20` 这种浮动标签**。

---

## 导入外部 Ceph 集群

向导里选「导入外部 Ceph」，选要使用它的节点，填：

| 字段 | 说明 |
|---|---|
| fsid | 在集群上执行 `ceph fsid` |
| 监视器地址 | IP 或 IP:端口，逗号分开；不写端口时用 6789 |
| 客户端用户 | 不带 `client.` 前缀 |
| 客户端密钥 | `ceph auth get-key client.<用户>` 的输出，加密保存，提交后不再显示 |

客户端用户要在对方集群上先建好，权限：

```bash
ceph auth get-or-create client.cloudland mon 'profile rbd' osd 'profile rbd pool=<给 CloudLand 用的池>' mgr 'profile rbd pool=<同上>'
```

导入时平台在每台节点上装 `ceph-common`（没有的话）、写客户端配置与 libvirt secret，再用客户端身份 `ceph fsid` 核对集群，**不改动集群**。之后建存储池时填对方已有的 RBD 池名：平台在池里写一个标记对象 `cloudland-pool`（所以客户端要有这个池的写权限），删池只删标记对象，删除集群只清理节点上的客户端配置。

---

## 建 RBD 存储池

「存储 → 存储池」→「新建共享池」，或集群详情的「存储池」标签：

| 字段 | 托管集群 | 导入的集群 |
|---|---|---|
| 介质 | 按介质建放置规则 `cl-<介质>`（数据只放到这种介质的 OSD 上）；「不限」用默认规则。有这种介质 OSD 的节点少于副本数时拒绝 | — |
| 配额（GB） | `ceph osd pool set-quota`；0 = 不设，不设配额、介质相同的池共用这些 OSD 的容量 | 不能设 |
| RBD 池 | 平台生成 `cl_<池 UUID 前 8 位>` | 必填，对方已有的池名 |

建池（托管集群）：放置规则 → 建池（3 副本、`min_size 2`）→ 启用 rbd 应用、`rbd pool init` → 配额 → 写标记对象 → 把池清单发给所有节点、各节点检查一次。

**容量**：节点探测时用客户端身份 `ceph df`，池的容量 = 已存数据（`stored`）+ Ceph 说还能存的量（`max_avail`，已经按副本数和最满的 OSD 算过）；设了配额时不超过配额。

**写满时**：池到配额（实测要超出约 8% 才生效，Ceph 的配额是滞后执行的）或集群到 95% 时，Ceph 把池标成写满，**云服务器的磁盘写入卡住、不报错**，云服务器仍显示运行中；各节点的探测 30–50 秒内把池判为不可用，新建卷和挂载被拒。改大配额或腾出空间后，卡住的写入自动继续。所以 Ceph 池建议不超分（超分比例 1）

**删池**：池里还有卷时拒绝；托管集群上池里还有 RBD 镜像（包括回收站里的）也拒绝；标记对象不是这个池的不删。

---

## 使用

与 GPFS 共享池相同（[使用共享池](./09-shared-storage.md#使用共享池)），区别：

- 卷是 RBD 镜像 `volume-<卷 ID>`，在建卷时由任一能访问池的节点 `rbd create`
- 挂载的磁盘是 `type='network'`、`protocol='rbd'`，`cache='writeback'`、`discard='unmap'`；mon 地址取自磁盘里写的配置文件（`<config file='/etc/ceph/<集群>.conf'/>`），mon 变了不用改磁盘
- 在线扩容按设备名 `virsh blockresize`，虚拟机立即看到新容量
- 删卷前检查镜像还有没有人打开（`rbd status` 的 watchers），有就拒绝并说明是谁
- 池探测：标记对象对得上、能写一个探测对象（集群写满时写操作会卡住，20 秒写不进去就判不可用）

---

## 运维

| 操作 | Ceph 上怎么做 |
|---|---|
| 添加节点 | 预检、装软件、`ceph orch host add` 按角色打标签，选了盘的同时建 OSD，最后配置客户端 |
| 添加磁盘 | `ceph orch daemon add osd`；Ceph 自动把一部分数据迁到新盘 |
| 移除磁盘 | `ceph orch osd rm <编号> --zap`：先把数据迁走再删 OSD、清盘，进度在任务详情里（「moving the data off」）。开始前检查：每个存储池按它的介质，剩下有这种 OSD 的节点数不少于它的副本数；同一设备类里留下的 OSD 装得下要迁走的数据、不到 `nearfull_ratio`（默认 85%）。不满足就拒绝，免得数据永远迁不完 |
| 移除节点 | `ceph orch host drain`（迁走全部守护进程与数据）→ `ceph orch host rm`；只是客户端的节点不碰集群。开始前的检查同「移除磁盘」 |
| 离线移除 | 节点已永久坏了（离线满 30 分钟）：`ceph orch host rm --offline --force`，它的 OSD `purge`，Ceph 用其余副本恢复 |
| 换盘 | 只对健康检查报告不是 `up` 的 OSD 出现，新盘必须在同一台主机：`ceph osd out` → `ceph orch daemon rm osd.<编号> --force` → `ceph osd destroy <编号> --force --yes-i-really-mean-it`（保留编号），再在新盘上建 OSD（沿用原编号）并 `ceph osd in`，数据从其他副本回填。OSD 还是 `up` 时拒绝。不用 `ceph orch osd rm --replace`：它要等 `safe-to-destroy`，OSD 主机数等于副本数时降级的数据无处可去，永远等不到。坏盘不擦除（回环测试盘外面那层 `clceph-*` 卷组会被去掉） |
| 改角色 | 改 `mon` / `mgr` / `_admin` 标签，cephadm 按标签增减守护进程；mon 要保持奇数，mgr 1–2 台，管理节点必须是 mon。客户端节点第一次得到标签时先加为 cephadm 主机。mon 有变化时（加节点、改角色、移除节点都会），任务最后读出新的 mon 地址，重写所有客户端配置的 `mon_host` |
| 自动加为客户端 | 同 GPFS（[共享存储](./09-shared-storage.md#运维)）：所选可用区里的节点逐台以客户端加入（装 `ceph-common`、写客户端配置与 libvirt secret） |
| 删除集群 | 先停掉编排器（`ceph mgr module disable cephadm`），每台在线节点 `cephadm rm-cluster --zap-osds`，删客户端配置、libvirt secret、`authorized_keys` 那一行，擦盘。离线的节点回来后平台自动补做（同 GPFS，见[共享存储](./09-shared-storage.md#运维)） |
| 轮换密钥 | 可选集群 SSH 密钥与客户端密钥。SSH 密钥同 GPFS 的三轮，中间让编排器换钥匙：`config-key set mgr/cephadm/ssh_identity_key` 与 `…_pub` 一起写再 `ceph mgr fail`（mgr 切换一次，几秒），然后 `ceph cephadm check-host` 每台主机。**不要用 `ceph cephadm set-priv-key` / `set-pub-key` 换钥匙**：它们各自拿新的一半去配旧的另一半，校验不过就静默保留旧钥匙（退出码 0）。客户端密钥：`ceph auth get-or-create-pending client.cloudland` 生成新密钥，写到每台节点的 keyring 与 libvirt secret，**第一次被使用时 Ceph 就把它转正**，旧密钥此后不能建立新连接。正在运行的云服务器不受影响（已建立的会话续期不需要密钥），下次启动或迁移时用上新密钥 |
| 升级 | 各节点装发行版现有的 Ceph 版本（`ceph-common`、`cephadm`，不能比集群旧），cephadm 主机拉对应的官方镜像（集群用自己的镜像时要填新版本的镜像），再 `ceph orch upgrade start --image …` 逐个升级守护进程、跟到 `ceph versions` 只剩新版本。不用迁走云服务器；云服务器用的是节点上的 `librbd`，下次启动或迁移时用上新版本。所有守护进程已是这个版本时直接成功 |

**健康检查、告警、曲线**：做法与 GPFS 相同（[共享存储](./09-shared-storage.md#运维)）。检查用 `ceph health detail`、`ceph orch host ls`、`ceph osd tree`、`ceph df`，Ceph 报 `*_NEARFULL` / `*_FULL` 时立即发「Ceph 容量接近满」告警。曲线来自活动 mgr 的 `prometheus` 模块（端口 9283）：部署时打开，已有集群由健康检查第一次运行时打开；每个 mgr 的模块只监听所在节点的内网地址（`mgr/prometheus/<mgr 名>/server_addr`，部署时设、健康检查时补设，地址变了的 mgr 会重启或切换一次）；Prometheus 的 `storage_clusters` 任务从 clapi 取 mgr 主机列表（`/api/v1/prometheus/sd/storage`），升级控制面后要用新的 `prometheus.yml` 重建 prometheus 容器。只有活动的 mgr 有数据，备用 mgr 的目标是空的。导入的外部集群（2026-10-07 第二轮起）由它的客户端节点以客户端身份执行 `ceph -s` / `ceph df detail` 取容量、读写、OSD，客户端身份要能读 mon（`mon 'allow r'`）

**节点宕机后的恢复**：同 GPFS（[共享存储](./09-shared-storage.md#运维)），隔离是另一台管理节点把宕机节点的内网地址加进黑名单：`ceph osd blocklist range add <地址>/32 315360000`（有效期 10 年；不给有效期时默认 1 小时就自动解除，旧的写入者会恢复写盘），`ceph osd blocklist ls` 里是 `cidr:<地址>:0/32`（同时出现的带进程号、1 小时的那一条是新客户端打破旧锁时 Ceph 自己加的）。实测（TC-24 REC-05/06）：被隔离节点上的旧 QEMU 立即暂停在 I/O 错误上，70 分钟后恢复它仍然写不进去。节点回来、对账清掉旧副本后再过 5 分钟才自动删掉这条黑名单（同 GPFS）。RBD 的排他锁挡不住两个写入者，这一步不能省。系统盘在 RBD 上的 UEFI 云服务器，NVRAM 在原节点本地，疏散时从模板重建（启动项丢失，走默认启动路径）。导入的集群由 CloudLand 的客户端身份加黑名单（只能加、不能删），解除要它的管理员 `ceph osd blocklist range rm <地址>/32`，然后在节点详情点「已手工解除」

**只有 3 台 OSD 节点时坏一台补不回副本**：集群一直降级直到节点回来或换上新节点，这期间再坏一台，部分数据的读写会卡住。正式使用建议 OSD 节点不少于 4 台。计划内维护见设计文档 §8.5。

**重启一台 OSD 节点时**（三台实测）：开机约 2.5 分钟后节点回来，上面的云服务器先进待启动列表，池探测通过就启动，不用等本机的 OSD（集群靠其余副本照常可用）；约 3 分 20 秒 `HEALTH_OK`。其余节点上云服务器的写入不失败，关机那一刻卡约 1.6 秒，这台的 OSD 回来时 PG 重新 peering 再卡 1–4 秒。开机过程中云服务器会短暂显示「暂停」，是 libvirt 启动域时的中间状态

---

## 常见问题

- **安装步骤报「镜像比节点上的 Ceph 新」**：参数里填的镜像版本高于节点的 `ceph-common`。留空，或填同版本的镜像
- **预检报节点系统不一致**：一个集群的节点要都是 24.04 或都是 26.04
- **Ubuntu 26.04 上 bootstrap 报 `invalid user: '167'`**：26.04 的 Rust 版 coreutils 不认没有对应用户的数字 uid；安装步骤会建一个 uid 167 的 `ceph-ctr` 用户，手工部署时要自己建
- **Ubuntu 24.04 上 bootstrap 报 `No module named 'jinja2'`**：24.04 的 `cephadm` 包没有声明依赖 `python3-jinja2`；安装步骤会装上，手工部署时要自己装
- **`ceph orch device ls` 是空的**：19.2 的设备清单只列可用的盘（`--filter-for-batch`），系统盘、GPFS 盘、已有 OSD 的盘都不列，盘都在用时就是空的，不影响建 OSD
- **建 OSD 报 `No devices found for host` / `is not found on host`**（20.2）：20.2 按设备清单校验，刚加入的主机还没有清单、逻辑卷（回环盘）不在清单里，平台会带 `--skip-validation` 再试；19.2 不校验
- **OSD 建出来一直是 down、cephadm 日志里 `ceph-volume lvm list` 报 `KeyError: 'ceph.type'`**：节点上有带 `ceph.*` 标签但标签不全的逻辑卷（别的工具或测试留下的），ceph-volume 列逻辑卷时遍历所有卷、碰到它就崩溃，cephadm 于是看不到新 OSD、不部署守护进程。`lvs -o lv_name,vg_name,lv_tags` 找出来，确认不是在用的 OSD 后删掉，再「重试」任务（这条恢复路径没有实测过）
- **部署卡在某一步**：任务详情看日志末尾；节点上 `/opt/cloudland/log/storage/run-<run id>.log`；集群日志 `cephadm logs --fsid <fsid> --name mon.<主机名>`
- **池在某台节点上不可用**：`ceph --conf /etc/ceph/<集群>.conf --id cloudland health`、`rados --conf ... --id cloudland -p <池> get cloudland-pool -` 看标记对象；探测结果在 `/opt/cloudland/run/pools/<池 UUID>.state`
- **删卷被拒绝说镜像还被打开**：`rbd --conf ... --id cloudland status <池>/<镜像>` 看 watchers 是哪台主机的哪个进程
