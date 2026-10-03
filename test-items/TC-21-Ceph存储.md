# TC-21 Ceph 存储（CEPH）

优先级 **P1** · 部署 / 删除 Ceph 集群（装软件包、拉镜像、擦盘）、建池建卷、建测试云服务器；CEPH-09 用回环设备、CEPH-12 重启节点（破坏性，要用户同意）· 约 90 min（不含 CEPH-12）

2026-10-03 按 `docs/architecture/plan/shared-storage-design.md` 的 S3 实施后写的用例（设计 §16 S3 的验收）。**同日在 work-01 / 02 / 03（Ubuntu 24.04，Ceph 19.2.3）上跑过**（用户同意在 work-x 上装 Ceph，D3），三台的 `sdb` 被 GPFS 集群 `gpfs1` 占着，测试盘用回环盘；结果、修掉的问题与收尾时的环境见 `runs/2026-10-03-0778bb2a+Ceph.md`。运维说明见 `docs/deployment/10-ceph-storage.md`。

## 设计约定（判定预期的依据）

- **软件**：不上传安装包。各节点从发行版源装 `ceph-common qemu-block-extra`，守护进程节点再加 `cephadm lvm2` 并拉镜像；镜像默认 `quay.io/ceph/ceph:v<节点 ceph-common 的版本>`，比节点新的镜像安装步骤拒绝。一个集群的节点必须是同一个发行版（预检的 Done 钩子检查）
- **布局**（`CheckLayout`）：mon 奇数且 ≥ 3（测试布局 1）、mgr 1–2、管理节点 ≥ 1 且是 mon、OSD 节点每台至少一块盘、OSD 节点数 ≥ 副本数（3，测试布局 1）；`replicas` 只能是 3（测试布局 1）
- **fsid = 集群 UUID**；libvirt secret 的 UUID 也是集群 UUID；客户端用户 `cloudland`，托管集群上权限是不限池的 `profile rbd`（集群归 CloudLand）
- **密钥**：客户端密钥由配置步骤读出，加密存进 `storage_clusters.secrets`，并从这一步的运行结果里删掉；导入时填的密钥同样只进 `secrets`，`params` 里没有
- **池**：托管集群上 RBD 池 `cl_<池 UUID 前 8 位>`，有介质时放置规则 `cl-<介质>`，3 副本 `min_size 2`；池里有标记对象 `cloudland-pool`（`driver=ceph_rbd`、`pool_uuid=`）。导入的集群登记已有的池，只写 / 删标记对象
- **卷**：RBD 镜像 `volume-<卷 ID>`（`volumes.path` 就是这个名字）；磁盘 `type='network' protocol='rbd'`，`<config file='/etc/ceph/<集群>.conf'/>`，`cache='writeback' discard='unmap' error_policy='stop'`
- **容量**：池探测用客户端身份 `ceph df`，容量 = `stored + max_avail`，有配额时不超过配额。不设配额、放置规则相同的池共用容量（`CapacityGroup` = `ceph/<集群>/<规则>`），导入的池各自算

### ⭐ 最容易误判的几点

- **节点上同时有 `/etc/ceph/ceph.conf`**：cephadm 给管理节点写的，与 CloudLand 的客户端配置 `/etc/ceph/<集群 UUID>.conf` 是两回事；平台脚本只用后者和 `/var/lib/ceph/<fsid>/config/`
- **mon / mgr 的界面标签是英文**：Ceph 的术语，有意不翻
- **建 OSD 这一步慢**：每块盘约 1.8 分钟（拉起 ceph-volume 容器、建 LVM、起 OSD 容器），不是卡住；三台从零部署约 11.5 分钟
- **19.2 的 `ceph orch device ls` 可能一直是空的**：清单用 `--filter-for-batch`，只列可用的盘，系统盘、GPFS 盘、已有 OSD 的盘都不算；这不影响建 OSD（19.2 的 `daemon add osd` 不看清单）
- **池配额写满时云服务器不会暂停**：Ceph 把池标成 `POOL_FULL`，写入阻塞、不返回错误，云服务器一直是 running；池探测在 30–50 秒内判「不可用」
- **集群「健康」一栏现在一直是「未知」**：健康看护属于 S5
- **单节点测试布局的集群 `HEALTH_WARN`**：`POOL_NO_REDUNDANCY` 已经关掉，剩下的告警（如 `TOO_FEW_OSDS`）是一台主机的正常现象

## 测试环境

- 节点：work-01 / 02 / 03 = hostid 1 / 2 / 3（Ubuntu 24.04，Ceph 19.2.3），内网 10.191.202.40 / .29 / .13
- 磁盘：回环盘 `/var/lib/cl-s3test/osd1.img`（每台 16 GiB 稀疏文件，`cl-s3.sh setup` 建，同时在 `cloudrc.local` 设 `storage_allow_loop=true`），平台在上面先建 LVM；CEPH-09 用 work-02 的 `osd2.img`（`cl-s3.sh setup osd2 work-02`）。回环设备重启后不会自己挂回来，所以三台装了只用于测试环境的 `cl-s3test-loop.service`（`/usr/local/sbin/cl-s3test-loop.sh`，开机在 docker 之前把 `/var/lib/cl-s3test/*.img` 挂回回环设备并激活 `clceph-*` 卷组；目录不存在时不运行），换真盘后可以删掉
- 工具（work-01，都要先有 S2 的 `cl-env.sh`、`cl-s2-lib.sh`、`cl-s2-vol-lib.sh`）：`/root/cl-s3.sh {setup|create|watch <任务>|check|disks|cadm '<ceph 参数>'}`（集群名 `ceph1`，三台都是 mon、work-01 / 02 是 mgr、每台一个 OSD，OSD 内存 1 GiB）、`cl-s3-pool.sh {create <名> [配额 GB] [介质] [超分]|quota|delete|show}`、`cl-s3-vol.sh <池>`（CEPH-04/05/06，逐项 PASS / FAIL）、`cl-s3-unavail.sh <池>`（CEPH-08）、`cl-s3-quota.sh`（CEPH-07）、`cl-s3-disks.sh {add|remove}`（CEPH-09）、`cl-s3-import.sh {run|cleanup}`（CEPH-11，把 ceph1 当外部集群）
- 沙箱（本机 WSL，Ubuntu 26.04，Ceph 20.2.0）：`go test ./src/rpcs -run TestStorageCephWSL`（`CEPH_WSL_E2E=1`，见测试文件头的前提），会建一个单节点集群并在最后删掉；探路脚本 `gpfs-spike/ceph/spike1.sh`、`spike2.sh`
- PostgreSQL：`go test ./src/services -run TestStorageCeph`（`CLAPI_TEST_DB_URI`），覆盖每一步的输入、Done 钩子与收尾
- 界面：本机会话临时目录 `pw/stc-ui-ceph.js`（全部接口模拟，41 项）、`pw/stc-ui-ceph-live.js`（对 work-01 真实数据只读，17 项，`ADMIN_PW` 环境变量给密码）

## CEPH-01 预检与部署（界面向导）

1. 「新建集群」→「部署 Ceph」：类型卡片不是「后续版本」；没有「软件」这一步
2. 选三台节点：第一台默认「管理、mon、mgr、OSD」，其余「mon、OSD」；每台选一块盘
3. 参数：副本数显示 3（不可改，勾测试布局变 1）；镜像填非法值、集群网络不带前缀时「下一步」不可用
4. 预检全部通过 → 确认 → 开始部署

**预期**：11 步都成功；`ceph -s` 三个 mon 在法定人数、两个 mgr 里一个 active（只有一台 mgr 时一个）、3 个 OSD `up`；`ceph orch ls` 里 `osd.all-available-devices` 是 `<unmanaged>`、没有 prometheus / grafana / alertmanager / node-exporter；`ceph config get osd osd_memory_target_autotune` 是 false；每台 `authorized_keys` 里集群公钥只有一行且带 `from=`；每台 `/etc/ceph/<UUID>.conf`、`.client.cloudland.keyring`（600）、`virsh secret-list` 有集群 UUID；集群详情「集群标识」= 集群 UUID，磁盘标签显示 `osd.0..2`，没有「文件系统」标签、没有「重新均衡」

> WSL（单节点测试布局）：通过，见设计 §16 S3 实施记录
> work-x（2026-10-03）：通过（修了回归点 9、10 之后；从零部署 11 分 38 秒）

## CEPH-02 不一致的发行版、过新的镜像

1. 计划里混一台 26.04 节点（或在 PG 测试里模拟）：预检后任务失败「run different releases」
2. 参数里填比节点新的镜像（如节点 19.2.3、镜像 `v19.2.4` 之后的版本）：安装步骤失败「newer than the Ceph ... of this host」

> work-x：第 2 条通过（在 work-03 上直接跑安装步骤、镜像 `v20.2.0`）；第 1 条只在 PostgreSQL 测试里跑过（三台都是 24.04）

## CEPH-03 RBD 存储池

1. 集群上建池，介质 HDD、配额 0：任务两步（`create_pool`、`sync_pools`）成功
2. 再建一个配额 50 GB 的池；把配额改成 60 GB
3. 介质选集群没有的（SSD）：400

**预期**：`ceph osd pool ls detail` 有 `cl_<前缀>`，`size 3 min_size 2`、`crush_rule` 是 `cl-hdd`、application rbd；`rados -p <池> get cloudland-pool -` 两行；每台节点池「就绪」，容量接近 `ceph df` 的 `stored + max_avail`；配额池容量 = 配额，`ceph osd pool get-quota` 一致

> work-x：通过（建池 30 秒，容量 16.3 GB；改配额在 CEPH-07 里）

## CEPH-04 RBD 数据卷

建卷 → 挂到 work-01 的云服务器 → 写数据、卸下 → 挂到 work-02 的云服务器、读数据 → 在线扩容 → 离线扩容 → 删卷

**预期**：建卷几秒内可用，`rbd info <池>/volume-<id>` 存在；`virsh dumpxml` 里磁盘是 `network`/`rbd`、带 `config file` 与 `auth`；跨节点数据一致（MD5）；在线扩容虚拟机不重启、`lsblk` 立即是新容量；删卷后 `rbd ls` 里没有了

> WSL：建卷、挂到 TCG 虚拟机（真实 librbd）、`rbd status` 看到 1 个 watcher、在线扩容 1→2 GiB（`virsh domblkinfo` 与 `rbd info` 都是 2 GiB）、卸下后 watcher 归零、删卷，通过
> work-x：`cl-s3-vol.sh` 全部通过（work-01 → work-02 换挂、在线 2→3、离线 3→4 GiB）

## CEPH-05 开着的卷不能删

卷挂在运行中的云服务器上时，在数据库里把它改成 available 后删卷（模拟状态不一致），或从另一个客户端 `rbd map` 打开它

**预期**：删卷失败，原因里有 watcher 的地址；卷不丢

> work-x：通过（卷挂着时在 work-02 直接跑删卷脚本被拒）

## CEPH-06 带 RBD 数据盘的迁移

云服务器有一块 RBD 数据盘，热迁移到另一台节点

**预期**：迁移计划里这块盘「共享，不复制」；传输量只有系统盘 + 内存；迁移后数据一致；目标节点上磁盘仍指向同一个 RBD 镜像（libvirt secret 同一个 UUID）

> work-x：通过（s2-02 同时带 GPFS 盘与 RBD 盘迁到 work-03，10 秒）

## CEPH-07 池配额写满

配额 2 GB 的池里建 3 GB 的卷（超分比例 2），在虚拟机里写满

**预期**：写到配额时 I/O 阻塞或报错（记下实际表现，V8），云服务器按 `error_policy='stop'` 暂停或卡住；放开配额后恢复

> work-x：**写入阻塞、不报错**，云服务器保持 running；`POOL_FULL` 时 `stored` 比配额多约 8%；池探测 30–50 秒判不可用；配额改大后写入立即完成

## CEPH-08 池不可用

1. 在一台节点上把 `/etc/ceph/<UUID>.conf` 改名
2. 删掉池里的标记对象

**预期**：30–60 秒内该节点（或全部节点）池「不可用」并说明原因，往那台节点的云服务器挂卷 400；恢复后回到就绪

> work-x：通过（配置改名 20 秒、标记对象删掉 36 秒判不可用；恢复约 40 秒）

## CEPH-09 加盘、移除盘

1. 在 work-02 上加一块盘（回环盘时平台先建 LVM）
2. 移除这块盘

**预期**：加盘后新 OSD `up`，`osd metadata` 的 `devices` 是这块盘的内核名；移除时任务详情显示数据迁移进度，完成后 OSD 不在 `ceph osd ls`，盘被擦净、扫描为空闲，回环盘的 `clceph-*` 卷组没了

> work-x：通过（加盘约 2 分钟、移除 51 秒）

## CEPH-10 加节点、移除节点、离线移除

需要第 4 台节点（没有就 SKIP）：加节点（mon 以外的 OSD 节点）→ 移除节点；离线移除（断网后）

**预期**：加节点后 `ceph orch host ls` 有它，OSD `up`；移除后 `host drain` 完成、`host ls` 没有它、节点干净；离线移除时它的 OSD 被 purge，Ceph 用其余副本恢复

> work-x：SKIP（没有第 4 台；三台都是 mon，移除任一台被布局规则拒绝）

## CEPH-11 导入外部集群

被导入方：另一组节点上的 Ceph，或 WSL 沙箱里的单节点集群（网络可达时）

1. 在被导入方建客户端用户（`profile rbd pool=vms`）与池 `vms`
2. 向导「导入外部 Ceph」：填 fsid、mon 地址、用户、密钥；密钥框是密码框，确认页不显示密钥
3. 登记池 `vms`、建卷、挂载；取消登记；删除集群

**预期**：导入任务成功，节点上有客户端配置与 secret；`storage_clusters.params` 里没有密钥；登记后 `vms` 里有标记对象；同一个池再登记 400；删除只清节点上的客户端配置，被导入方没有变化

> work-x：通过（把 ceph1 当外部集群，池 `extpool`、用户 `client.ext`，`cl-s3-import.sh`）

## CEPH-12 节点整机重启（破坏性，要用户同意）

重启一台 OSD 节点，上面有挂着 RBD 盘的云服务器

**预期**：开机后 cephadm 的容器自动起来，OSD 回到 `up`；云服务器进待启动列表，池探测通过后启动；其余节点上的云服务器读写不中断（3 副本降级）

> work-x：通过（2026-10-03，修了回归点 13 后；两次真实重启 work-03，s2-03 挂 RBD 卷 rb-a、s2-01 上的 RBD 卷 rb-b 每 0.2 秒同步写一次）。开机约 2.5 分钟 SSH 恢复，池探测通过后 s2-03 立即启动（这时 osd.2 还没回来，集群靠其余两份副本照常可用），约 3 分 20 秒 `HEALTH_OK`；rb-a 数据完好；rb-b 写入 0 失败，关机那一刻卡 13.4 秒（第一次）→ 1.6 秒（修后），osd.2 回来时 peering 再卡 1–4 秒（Ceph 本身的行为）。开机时心跳会短暂报 `paused`，是 libvirt 的「启动中」，GPFS 盘的云服务器同样（S2 也有）
>
> ⚠️ 重启前回环盘要能开机挂回来（「测试环境」里的 `cl-s3test-loop.service`），否则 OSD 起不来

## CEPH-13 删除集群

集群上还有池时 409；删完池后删除（可选卸载软件包）

**预期**：每台节点没有 `/var/lib/ceph/<fsid>`、没有 ceph 容器、没有 `clceph-*` 卷组、没有客户端配置与 secret、`authorized_keys` 里没有集群那一行；盘被擦净、扫描为空闲；内存预留归零

> WSL：通过（单节点）
> work-x：通过（带卸载软件包 91 秒；中止的部署也能删，回归点 11）

## CEPH-14 界面

`pw/stc-ui-ceph.js`（接口模拟）：向导的 Ceph 部署与导入、集群详情、两种集群上的共享池弹窗，41 项

> 本机 5173：模拟 41/41、真实数据只读 17/17 通过

## 历史缺陷回归点汇总

| # | 问题 | 怎么验证 |
|---|---|---|
| 1 | cephadm 默认镜像标签 `:v20` 拉到 20.2.4，生成的密钥 20.2.0 的客户端读不出（`Malformed input`） | 镜像默认取节点 `ceph-common` 的版本；填更新的镜像安装步骤拒绝（CEPH-02） |
| 2 | Ubuntu 26.04 的 Rust coreutils `install -o 167` 报 `invalid user`，bootstrap 失败 | 安装步骤在没有 uid 167 时建 `ceph-ctr` 用户 |
| 3 | 刚 bootstrap 的主机没有设备清单，`daemon add osd` 报 `No devices found`；逻辑卷不在清单里报 `is not found on host` | 建 OSD 前刷新清单（不等，见第 10 条）；报这两种错时带 `--skip-validation` 重试 |
| 4 | 回环盘的卷组在重启后没激活，ceph-volume 找不到逻辑卷 | `backend_disk_path` 每次 `vgchange -ay` |
| 5 | `source_migration.sh` 先在文件盘列表里找计划里的每块盘，RBD 盘不是文件，迁移失败 | 共享盘在查找前跳过（CEPH-06） |
| 6 | 在线扩容用卷路径做 `virsh blockresize` 的参数，RBD 盘没有路径 | 改用磁盘的设备名（CEPH-04） |
| 7 | 开机时只按 `source/@file` 认池，RBD 盘的云服务器不会等池就绪 | `instance_pools` 认 `protocol='rbd'` 的磁盘（按池名与配置文件对应）（CEPH-12） |
| 8 | 配置步骤的运行结果里会带着客户端密钥（运行结果明文存库） | Done 钩子加密保存后从结果里删掉（PG 测试 `TestStorageCephDeployPG`） |
| 9 | Ubuntu 24.04 的 `cephadm` 19.2.3 包不依赖 `python3-jinja2`，bootstrap 一启动就 `No module named 'jinja2'` | 安装步骤给守护进程节点装 `python3-jinja2`（CEPH-01） |
| 10 | 建 OSD 前等设备清单永远等不到：19.2 的清单只列可用的盘，盘都在用时是空的 | 不等清单，只在 20.2 报「No devices found for host」「is not found on host」时带 `--skip-validation` 重试（CEPH-01、CEPH-09） |
| 11 | 中止的部署删不掉（「The cluster has no admin host」：删除时只选已是成员的管理节点） | 删除集群时管理节点不按状态过滤（PG 测试 `TestStorageCephAbortedDeletePG`，CEPH-13） |
| 12 | 20.2 的 `orch daemon add osd` 每次都把那块盘存成**受管**的 `osd.default` 规格（cephadm 里判断规格是否已存在时拿 `default` 去比 `osd.default`，永远不相等），按代码，这块盘移除、清掉之后后台会在上面重新建 OSD（没有实测） | 建完 OSD 后 `orch set-unmanaged osd.default`（19.2 没有这个规格，命令失败、无害）。20.2：WSL 端到端测试保留集群（`CEPH_WSL_KEEP=1`）后 `orch ls` 里 `osd.default` 是 `<unmanaged>`；19.2：CEPH-09 加盘、移除盘照常 |
| 13 | 节点关机时同一台机器上的 mon 与 OSD 被 systemd 一起停掉，OSD 报告下线的消息正好发给了本机这个正在停的 mon，丢了；集群要等心跳超时（约 13 秒）才标它 down，其余节点上云服务器的 I/O 卡这么久（cephadm 的单元不像 Ceph 软件包的单元那样把 OSD 排在 mon 之后） | 安装步骤给守护进程节点写 `/etc/systemd/system/ceph-<集群>@.service.d/cloudland-order.conf`（`After=ceph-<集群>@mon.%l.service`），删除集群时删掉。验证：`systemctl show -p After ceph-<集群>@osd.<编号>.service` 有本机 mon；`systemctl stop ceph-<集群>.target` 时 OSD 日志里 `prepare_to_stop` 两行相隔约 1 秒（没修时正好 5 秒，是等确认超时），osd.2 在 2.7 秒内被标 down（没修 17.6 秒）；WSL 端到端测试检查顺序与删除后不残留（CEPH-12） |
| 14 | 删 RBD 卷时 `rbd info` 的任何失败（超时、集群暂时连不上）都被当成「镜像不在」，脚本报删除成功，clapi 删掉卷记录，镜像留在池里占容量和配额（2026-10-03 代码审查） | 直接 `rbd rm`，只有 `No such file or directory`（镜像或池不在）才算已删；超时报「timed out」（WSL `gpfs-spike/stc-test4.sh` 第 4 组用替身 `rbd` 测各种返回） |
| 15 | 导入集群登记已有 RBD 池时按 `LIKE '%"ceph_pool":"名字"%'` 查重，名字里的 `_` 是通配符，`rbd_a` 会被当成已登记的 `rbdXa`（代码审查） | 用 `dbs.Contains` 转义（PG `TestStorageCephImportPG`：登记了 `vms` 后 `v_s` 不算重复） |
| 16 | 导入不占槽，导入的检查还在跑时就能删除（forget）集群，检查跑完又把客户端配置写回去（代码审查） | 有在跑或等重试的导入时删除返回 409（同上）；另见 TC-20 回归点 16–26 里两种存储共用的修复 |

## 清理

删除测试云服务器、卷、池、集群（CEPH-13）；回环盘的文件与 `storage_allow_loop`；被导入方上建的客户端用户与池
