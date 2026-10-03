# 共享存储设计：在 CloudLand 里部署与使用 GPFS、Ceph

- **状态**：S1、S2、S3 已实施并在 work-x 上验收（2026-10-03；S3 先在本机 WSL 沙箱里用单节点 Ceph 跑通，再在三台上用回环盘验收）；S4 起未做。各阶段的实施记录在 §16。都未提交（阶段 S0 的 GPFS 内核编译验证见 §2.2）。2026-10-01 做过一轮三路评审（代码事实核对、对抗式设计审查、内部一致性），正文已按结论修改，每条发现的处置见附录 H
- **日期**：2026-10-01，基于 `stage-01` 分支 `0778bb2a`（文中行号均以此为准）
- **涉及**：clapi（`api/`）、节点脚本（`scripts/`）、cpgateway 代理白名单、前端（`web/`）、部署脚本（`deploy/`）
- **相关文档**：
  - 本文由 `gpfs-shared-storage-design.md`（2026-09-22）改写而来并**取代**它。原文的定位是「CloudLand 只使用 GPFS，集群由存储管理员装」；2026-10-01 需求改为在 CloudLand 界面上部署、配置、创建 GPFS 与 Ceph 集群（§1.1），整体重写并改名。原文各章节的去向见附录 G
  - `local-multi-disk-storage-plan.md`（本地多盘存储池）已实施（L0a–L4），本文的存储池模型、磁盘扫描、`pool_guard`、待启动列表都建在它的基础上。与它的描述不一致时，**以已实施的代码为准**，其次是本地方案
- **优先级**：GPFS 高于 Ceph。两者没有冲突（§3），共用的框架一次做，然后先做 GPFS、再做 Ceph（§16）

---

## 0. 摘要

- **CloudLand 自己部署和管理存储集群**：系统管理员在界面上上传 GPFS 安装包、选节点和角色、选磁盘、填参数，CloudLand 经现有的命令通道（clapi → cland → cloudlet）在节点上完成安装、建集群、建文件系统（GPFS）或建池（Ceph）；之后的加节点、加盘、删节点、删集群也在界面上做。已有的外部集群可以「导入」，只用不管
- **四层模型**：存储集群（GPFS 集群 / Ceph 集群）→ 文件系统（只有 GPFS）→ CloudLand 存储池（GPFS 的一个独立 fileset，或 Ceph 的一个 RBD 池）→ 卷。存储池以下沿用原设计的思路：按卷选驱动、clapi 下发路径、节点可用性、容量准入、共享迁移、宕机恢复
- **新做的通用部分**：多步骤任务引擎（每步每节点的状态与日志、重试、中止，节点上有持久的作业记录，重启、丢回调、重复下发都能收敛）、节点角色与磁盘认领（用到盘的那一刻按稳定 ID 和序列号核对身份）、部署前预检、每个集群独立的 SSH 密钥、存储进程的内存预留；软件包仓库（安装包上传到 S3、许可证确认、节点按预签名地址下载）只有 GPFS 用，随 S2 做
- **通用接口**（§4.5）：GPFS、Ceph 和以后的共享存储都经同一套接口接入。集群一层是「存储后端」（每种存储一个 clapi 文件加一个节点钩子文件，类型专用的数据放进 JSON 列）；存储池一层是「驱动」（分文件型、块型两类，节点上每个操作一个通用脚本、每个驱动一个函数文件）。S1 的代码已按这个接口改好
- **GPFS 节点必须是 Ubuntu 24.04**：手上的安装包（6.0.0.2）对 work-x 的 26.04 / 7.0 内核**编译失败，65 个错误**（§2.2）。IBM 也只支持到 24.04。GPFS 的真实环境测试要先解决「哪来的 24.04 节点」（§18 决策 D1）；Ceph 在现有节点上没有障碍。开发阶段先用本机 WSL 做沙箱（§2.5），不占 work-x
- **测试环境的限制**：三台节点没有空闲磁盘（`sdb` 都被保留的 `local-hdd` 占着）、内存 30 GB、网络 2 Gbit/s。只够做功能测试（用文件做的回环盘），GPFS 纠删码版（ECE）的硬件门槛达不到，只能测副本模式（§2.3、§2.5）。2026-10-02 起 work-01 / 02 / 03 已重装为 Ubuntu 24.04（原有环境清空），`sdb` 上残留旧本地池的 LVM，擦除后可作测试盘（决策 D2）
- **从用户角度看完整流程**（管理员搭建、维护，普通用户使用）：GPFS 见 §19，Ceph 见 §20

---

## 1. 需求与范围

### 1.1 需求

2026-10-01 用户提出（原话）：

> 我要支持 ceph 与 gpfs；gpfs 的优先级比 ceph 高；如果 2 个不冲突可以一起实现。我本机 download 目录下面有 gpfs 安装包。我要深度集成这 2 个软件，就是在我的页面上能部署、配置、创建 gpfs 与 ceph 集群。

拆开来是四件事：

1. **部署**：在页面上把软件装到节点上（GPFS 用用户提供的安装包，Ceph 用发行版的包和官方容器镜像）
2. **配置**：节点角色、磁盘、网络、副本数、块大小等集群参数，在页面上选
3. **创建**：建集群、建文件系统（GPFS）或池（Ceph），再在上面建 CloudLand 的存储池，给云硬盘和云服务器用
4. **运维**：集群建好之后的增删改（加节点、加盘、删节点、删集群、看健康状态）。没有这一块，「深度集成」只集成了第一天

### 1.2 目标

1. 系统管理员在 CloudLand 界面上完成 GPFS 集群、Ceph 集群的部署、扩缩容和删除，过程中每一步、每台节点的进度和日志都能看到，失败可以重试
2. 已有的 GPFS 集群、Ceph 集群可以导入，只当存储用
3. 在集群上建 CloudLand 存储池；云硬盘、云服务器的系统盘可以放在上面
4. 共享存储池上的盘可以挂给任何能访问这个池的节点上的云服务器；全部磁盘都在共享池上的云服务器，迁移不复制磁盘，源节点宕机时可以在别的节点恢复（原设计的目标，保留）
5. 集群健康、容量进入 CloudLand 的监控和告警

### 1.3 非目标

- **GPFS 纠删码（ECE 的 `mmvdisk` 恢复组）放在后面**（阶段 S7）：硬件门槛高，测试环境达不到（§2.3）。第一版用副本模式，ECE 安装包的许可证覆盖副本模式（§2.1）
- **GPFS 的协议服务**（NFS / SMB / S3 / HDFS / AFM）、IBM 的图形界面（`gpfs.gui`）和 REST 接口（`gpfs.scaleapi`）、性能采集（zimon）：都不装。CloudLand 的界面替代图形界面，管理命令走节点脚本（§4.3）
- **Ceph 的 CephFS、RGW（对象存储）、iSCSI / NVMe-oF 网关**：都不做，只用 RBD
- **SAN 共享 LUN 做 GPFS 的 NSD**：第一版只用节点本地盘（无共享副本模式）。磁盘扫描现在会把多路径、FC、iSCSI 盘判为 `shared` 并拒绝（`scripts/kvm/storage_lib.sh:271` 的 `classify_disk`），以后要支持时再放开（阶段 S7）
- **不经 cloudlet 管理的节点**：CloudLand 只能经 cloudlet 在节点上执行命令。专用的存储节点也要按计算节点注册，再设为不调度云服务器（§6.3）
- **操作系统升级**：CloudLand 不负责把节点从 26.04 换成 24.04，也不升级内核（§7.7）
- 卷在存储池之间搬迁、按存储池分别计配额：与原设计相同，不做
- 存量数据迁移：产品没上线，直接按目标方案改

### 1.4 与原设计的差别

| 方面 | 原设计（`gpfs-shared-storage-design.md`） | 本文 |
|---|---|---|
| 集群由谁装 | 存储管理员，CloudLand 不管（原 §1.3、§11.1） | CloudLand 在界面上装，也可以导入外部集群 |
| 后端 | GPFS，Ceph 只留了扩展位（原 §12） | GPFS 与 Ceph 都做，Ceph RBD 有完整的驱动设计（§9.3） |
| 存储池怎么来 | 管理员先在 GPFS 上手工建 fileset、配放置规则和配额，再到 CloudLand 登记路径（原 §2.4） | 托管集群在集群详情页上建，CloudLand 生成 fileset、放置规则、配额（GPFS）或 RBD 池、CRUSH 规则、配额（Ceph）；外部集群仍按原方式登记 |
| 阶段 0（存储池抽象） | 待做 | 已由本地多盘方案完成（`storage_pools`、`hyper_storage_pools`、`hyper_disks`、`pool_guard`、待启动列表都已上线） |
| 新增 | — | 软件包仓库、任务引擎、节点角色与磁盘认领、预检、集群 SSH 密钥、内存预留（§6） |

---

## 2. 可行性与环境约束

### 2.1 手上的安装包

本机 `Downloads` 目录下有两个 IBM Storage Scale **纠删码版（Erasure Code Edition）** 的安装包：

| 文件 | 大小 | 版本 |
|---|---|---|
| `Storage_Scale_Erasure_Code-6.0.0.2-x86_64-Linux-install` | 1.70 GB | 6.0.0.2 |
| `Storage_Scale_Erasure_Code-5.2.3.8-x86_64-Linux-install` | 1.29 GB | 5.2.3.8 |

安装包的结构（按 6.0.0.2 核对）：

- 是一个自解压的 bash 脚本，第 680 行起是 tar.gz（脚本里 `PGM_BEGIN_TGZ=680`）。选项 `--dir <目录>`（默认 `/usr/lpp/mmfs/6.0.0.2`）、`--silent`（以静默方式运行许可证确认工具，即接受许可）、`--text-only`、`--manifest`（只打印清单）、`--remove`
- 许可证确认工具是一个 Java 程序，安装包自带 IBM Java（`ibm-java-x86_64-80`），节点上不用装 Java
- `manifest` 列出每个软件包和它的 md5，可以用来校验解出来的包
- Ubuntu 的包只有 **22.04 和 24.04** 两个目录（`gpfs_debs/ubuntu/ubuntu22`、`ubuntu24`），放的是 `gpfs.librdkafka`、性能采集、NFS（ganesha）、SMB 这些按发行版编译的包；核心包（`gpfs.base`、`gpfs.gpl`、`gpfs.gskit`、`gpfs.msg.en-us`、`gpfs.license.ec`、`gpfs.gnr*`、`gpfs.adv`、`gpfs.crypto`、`gpfs.compression`、`gpfs.docs`）是不分发行版的 amd64 包
- 还带了 `ansible-toolkit`（IBM 的安装工具）、`gpfs.gui`、`gpfs.scaleapi`（REST 接口）、`Public_Keys`（包签名公钥）

许可证：**ECE 包含数据管理版（Data Management Edition）的全部功能**，再加上 ECE 本身（[IBM ECE 手册](https://www.ibm.com/docs/en/SS8QUM_5.2.3/pdf/scale_ece.pdf)）。所以用这个包建副本模式的普通集群在功能上没有问题；**正式使用的授权（按容量或按盘计费）要和 IBM 确认**，这不是技术问题，列在 §18。

### 2.2 操作系统与内核（GPFS 的头号风险）

| | 现状 |
|---|---|
| work-01/02/03 | 2026-10-01 时是 Ubuntu 26.04.1、内核 `7.0.0-31-generic`；**2026-10-02 起重装为 Ubuntu 24.04.5、内核 `6.8.0-146-generic`**（GA 系列，GPFS 已在上面编译、加载通过，V1） |
| Storage Scale 支持的 Ubuntu | 按 IBM 公开资料是 22.04、24.04（24.04.4 配 6.0.1.1 时最低内核 6.8）；安装包里也只有这两个版本的目录（§2.1）。没有找到支持 26.04 的说明 |
| 只支持 Ubuntu 的默认 generic 内核 | HWE 等其他内核不支持（[FAQ](https://www.ibm.com/docs/en/STXKQY/pdf/gpfsclustersfaq.pdf)） |

GPFS 有内核模块（「可移植层」，`gpfs.gpl` 带源码，`mmbuildgpl` 在节点上编译），与内核版本强绑定。用户空间的包装在 26.04 上不是问题，问题在于这个模块能不能对 7.0 内核编译。

**验证结果（2026-10-01，阶段 S0）**：在本机 WSL（Ubuntu 26.04）里装了 6.0.0.2 的 `gpfs.base`、`gpfs.gpl`、`gpfs.gskit` 和 `linux-headers-7.0.0-31-generic`（与 work-x 完全相同的内核头文件），执行 `mmbuildgpl`：

1. 第一步就被拒：`Cannot parse kernel version 7.0.0-31-generic`。构建配置脚本（`/usr/lpp/mmfs/src/config/configure:867`）的正则只认主版本号 2–6 的内核
2. 在沙箱里放开这个检查后，又被「Ubuntu 上不支持为非当前运行的内核生成配置」拦下（只有 RHEL 允许），再放开
3. 真正编译：**65 个错误**，分布在 `include/gpl-linux/verdep.h`（26 个）、`kx.c`（17 个）、`mmap.c`（16 个）、`trcid.h`（5 个），另有 `mmpmem.c` 找不到头文件 `linux/pfn_t.h`。原因是 7.0 内核改了内核接口：`struct filename` 的 `refcnt` 改成了 `atomic_t`、去掉了 `uptr`，若干函数的参数个数变了，内存映射相关的结构变了，`pfn_t.h` 被删掉了

结论：**Storage Scale 6.0.0.2 在 Ubuntu 26.04 / 7.0 内核上装不起来**。不是绕开一两个版本检查的问题，而是 IBM 的内核模块源码还没适配 7.0 的接口；自己改这 65 处等于维护一份 IBM 文件系统内核模块的分支，即使编过也没有人能保证正确，不能用。5.2.3.8 更旧，没有单独试。（验证脚本与日志在本机 `Documents\gpfs-spike\`，WSL 里的 `/root/gpfs-spike/`；沙箱里改过的 `configure` 留有 `.orig` 备份。）

所以只有两条路：

1. **GPFS 节点用 Ubuntu 24.04（推荐）**：在支持范围内。CloudLand 本身支持 24.04（部署脚本、控制面镜像都按 24.04 / 26.04 写的），同一个区域里可以混用：24.04 的节点能用 GPFS 存储池，26.04 的节点用不了（可用性检查会把它们判为不可用，调度时自然排除，§9.2）
2. **等 IBM 支持 26.04**：时间不可控

CloudLand 在部署前做硬性预检（§6.5）：操作系统和内核不在支持范围内默认拒绝。系统管理员可以勾选「允许不受支持的系统（仅用于测试）」强制继续——这对 24.04 上比支持表新一点的内核有用，对 26.04 没用（编译那一步一定失败）。这个选择记入审计，并在集群详情页上一直显示警告。

**Ceph 没有这个问题**：Ubuntu 26.04 自带 Ceph 20.2.0（Tentacle），`cephadm`、`ceph-common` 都在官方源里；云服务器访问 RBD 走 QEMU 里的 librbd（用户态），节点上 `librbd1` 和 QEMU 的 RBD 块驱动（`qemu-block-extra` 里的 `block-rbd.so`）三台都**已经装好**，不需要内核模块。

### 2.3 GPFS 纠删码版（ECE）的硬件门槛

按 IBM 的 ECE 硬件要求（[最低硬件预检](https://www.ibm.com/docs/en/storage-scale-ece/5.2.2?topic=requirements-minimum-hardware-precheck)）：

| 要求 | ECE | work-x |
|---|---|---|
| 每个恢复组的服务器数 | 3–32 台，配置相同 | 3 台，相同 ✓ |
| 内存 | 64 GB 以上（每台 64 块盘以内） | 30 GB ✗ |
| 网络 | 至少 25 Gbit/s | 2 Gbit/s（bond） ✗ |
| CPU | 16 核以上 | 64 线程 ✓ |
| 盘 | 每台若干块同型号盘 | 每台 2 块 HDD，都在用 ✗ |

**ECE 在现有测试环境上跑不了**（何况它也要 24.04）。第一版用「无共享副本模式」：每台节点的本地盘作为它自己的 NSD，每台节点一个故障组，数据和元数据都存 2 或 3 份。这是 Storage Scale 的基本功能，三台节点就够。

### 2.4 Ceph

- **部署工具用 `cephadm`**（Ceph 官方的编排工具）：守护进程跑在容器里，节点上只装 `cephadm` 和 `ceph-common`。三台节点都已经装了 docker（cephadm 支持 docker 和 podman）
- **容器镜像**要能拉到（默认 `quay.io/ceph/ceph:v20.2.x`）。私有化环境要提供镜像仓库地址（§8.1）
- 三台节点刚好够 3 副本的最小形态：3 个 mon、2 个 mgr、每台若干 OSD、副本数 3。但坏一台节点后没有地方补第三份副本，要等它回来（§8.5），所以只适合测试；正式使用建议 OSD 节点不少于 4 台
- 内存：mon、mgr 各 1–2 GB，每个 OSD 默认按 4 GB 预算（`osd_memory_target`），在 30 GB 的节点上与云服务器混跑要调小并做内存预留（§6.7）

### 2.5 测试环境

**磁盘**：三台节点各有两块 1.8 TB 的 HDD：`sda` 是系统盘（根文件系统还剩 1.6–1.7 TB），`sdb` 整块被保留的测试存储池 `local-hdd` 占着（LVM，卷组没有剩余空间）。可选的做法：

| 做法 | 优点 | 缺点 |
|---|---|---|
| **在 `sda` 的根文件系统上建大文件，做成回环设备**（推荐先用） | 不动保留环境；每台可以做几块 100 GB 的盘 | 性能没有参考意义；开机后回环设备要先建好（加一个 systemd 单元）；GPFS 要加 `nsddevices` 用户出口才认回环设备；Ceph 不收回环设备，要在上面建 LVM 逻辑卷再交给它 |
| 删掉某台或全部节点上的 `local-hdd`，腾出 `sdb` | 真实磁盘，可以测性能 | `local-hdd` 是保留的测试环境，**要用户同意** |
| 新租带空盘的机器 | 不影响现有环境 | 要花钱，要用户决定 |

本地存储池的测试已经用过回环设备（`cloudrc.local` 的 `storage_allow_loop`），CloudLand 的磁盘扫描对它有现成的开关。

**GPFS 还要 24.04 的节点**（§2.2），现有三台都是 26.04：

| 做法 | 说明 |
|---|---|
| **新租 2–3 台 Ubuntu 24.04 的裸金属，按计算节点加入 work-01 的区域**（推荐） | 最接近真实形态，带空盘的话磁盘问题一起解决；要花钱 |
| 把 work-02、work-03 重装成 24.04 | 不花钱，但上面的保留环境（`ibmx` / `ibmr` / `aax` 的云服务器、`rb-be3`、VPN 网关的一端）全部要先迁走或重建，**要用户同意** |
| 在 CloudLand 里开 24.04 的云服务器当节点（嵌套虚拟化） | 不推荐：云服务器要作为计算节点接入 cland，没有浮动 IP 时出网限速约 1 Mbit/s（待办 C2），装 1.7 GB 的包就要几小时；公网地址只剩一个 |

Ceph 可以直接在 work-x 上用回环盘测，但**在 work-x 上安装任何东西都要用户决定**（CLAUDE.md：不直接部署）。

**本机 WSL 沙箱**（开发阶段用，不占 work-x，不需要用户决定）：本机的 WSL（Ubuntu 26.04，内核 6.6.114.1，16 核、31 GB 内存、900 GB 空闲）有 systemd、docker、回环设备和 device-mapper，可以：

- 起单机 Ceph（cephadm + docker，OSD 放在回环设备上的 LVM 逻辑卷），验证 §8 的部署步骤和 §18.2 里 Ceph 的大部分待验证项
- ~~为 WSL 内核编译 GPFS 的内核模块、起单节点 GPFS 集群~~ **试过，不行**（2026-10-01，V22）：模块能编译，用开了 BTF 重编的内核树拿到对得上的 `Module.symvers` 后版本校验也能过、`tracedev` 能加载，但加载 `mmfslinux` 时内核报 `jump_label: Fatal kernel bug, unexpected op at cxiUnlockAndPutPage`，WSL 的虚拟机随即崩溃重启（`%LOCALAPPDATA%\Temp\wsl-crashes` 有记录）。WSL 也没有嵌套虚拟化（没有 `/dev/kvm`），不能在里面再开 24.04 虚拟机。**GPFS 的一切验证只能等 24.04 节点**（D1）；GPFS 的节点脚本可以先写，命令行为照 IBM 文档与附录 C
- 直接执行节点脚本，把输出的回调行喂给 clapi 的 PostgreSQL 测试（本机 WSL 里已有 PostgreSQL 18），验证任务引擎（S1 已做：`api/src/rpcs/storage_task_wsl_test.go`）

WSL 没有 cloudlet，所以不能端到端（界面 → clapi → cland → 节点）；S1 的测试用一个假 cland 在 WSL 里按 cloudlet 的方式执行命令（每台节点一个串行队列、`bash -c` 执行、回调行交给 clapi 的解析器），覆盖到节点脚本为止，cloudlet 与 cland 本身要在真实节点上测。⚠️ WSL 的会话结束时会杀掉它留下的后台进程，模拟 cloudlet 时每条命令要放进自己的会话（`setsid --wait`）；没有任何 `wsl.exe` 会话时虚拟机也可能被停掉，在 WSL 里的 PostgreSQL 随之重启。

### 2.6 结论

- 技术上可以做。Ceph 在现有节点上没有障碍；**GPFS 只能跑在 24.04 节点上**，测试要先有 24.04 的机器
- 测试环境只够验证功能和流程，不够验证性能和 ECE
- 下面的设计不依赖「GPFS 节点在哪」：操作系统支持做成预检，同一区域混用 24.04 / 26.04 节点时，GPFS 存储池只在 24.04 节点上可用

---

## 3. GPFS 与 Ceph 能否一起做

### 3.1 逐项比对

| 方面 | GPFS | Ceph | 会不会冲突 |
|---|---|---|---|
| 磁盘 | NSD 直接用整块盘，盘上没有 `blkid` 认得的签名 | OSD 在盘上建 LVM（卷组 `ceph-*`，逻辑卷带 `ceph.osd_id` 标签） | **会**：同一块盘只能给一方。靠磁盘认领（§6.4）保证；还要让磁盘扫描认出 GPFS 的 NSD，否则它在扫描结果里是「空闲」，可能被拿去建本地池（§6.4） |
| 内核 | 自己的内核模块（mmfs26、mmfslinux、tracedev） | 不用内核模块（云服务器经 QEMU 的 librbd 访问，不用 krbd） | 不冲突 |
| 操作系统 | 只能 22.04 / 24.04（§2.2） | 24.04、26.04 都行 | 不冲突，但同时用两者的节点只能是 24.04 |
| 端口 | 1191（守护进程）；不装图形界面就没有别的固定端口 | mon 3300 / 6789，OSD / mgr 6800–7300，mgr 的 prometheus 9283，面板 8443 | 两者之间不冲突。**cephadm 默认还会装一套监控**（prometheus 9095、grafana 3000、alertmanager 9093、node-exporter 9100），和 CloudLand 自己的监控冲突（work-01 的 3000、9093 已被占用），所以 bootstrap 一律带 `--skip-monitoring-stack`（§8.2） |
| 时钟 | 要求节点时间同步 | mon 之间时差超过 0.05 秒就告警 | 不冲突，节点已有 ntpsec / chrony |
| SSH | 管理命令要求管理节点能免密 root 登录其他成员 | cephadm 要能从 mgr 所在主机免密 root 登录其他主机 | 不冲突，各用各的密钥（§6.6） |
| 容器 | 不用 | 用 docker / podman | 不冲突 |
| LVM | 不用 | 用 | 与本地存储池共用 LVM：本地池的脚本只碰自己打了 `cloudland_pool` 标签的卷组（`async_job/create_local_pool.sh:129`），不会动 `ceph-*`；反过来 cephadm 只用被明确交给它的盘（§8.2 关掉它的「自动占用所有空闲盘」） |
| 内存 | pagepool（默认 1 GiB）加守护进程约 1 GiB | 每个 OSD 2–4 GiB，mon / mgr 各 1–2 GiB；cephadm 默认开启 `osd_memory_target_autotune`，按主机内存的七成分给 OSD | 两者叠加后要从可调度内存里扣掉（§6.7）；Ceph 的自动调整必须关掉，否则预留是错的 |
| 根文件系统 | `/var/mmfs`、安装包缓存 | mon 的数据目录在 `/var/lib/ceph`，可用空间低于 30% 告警、低于 5% mon 自行停止 | 都和内置本地池共用根文件系统。内置池允许用到 90%，一台节点的本地云服务器把根文件系统写满，就可能让 mon 停掉、整个集群的 RBD 卡住。预检要检查（§6.5），mon 所在节点建议给 `/var/lib/ceph` 单独分区或调低内置池的上限 |
| 网络带宽 | 写入要同步到另一个副本所在的节点 | 写入要同步到另外两个副本 | 都和云服务器的 VXLAN 流量抢 2 Gbit/s，是性能问题，不是冲突 |
| 开机顺序 | GPFS 自动启动并挂载（`mmcrcluster -A`、`mmcrfs -A yes`） | 容器由 systemd 拉起 | 都靠待启动列表：池没就绪的云服务器先不启动（本地方案 §4.8，已实施） |

### 3.2 结论

- **没有冲突**，可以在同一批节点上同时部署。硬约束只有两条：磁盘不能共用；cephadm 不能装它自带的监控
- **约六成的工作是两者共用的**：软件包仓库、任务引擎、节点角色与磁盘认领、预检、SSH 密钥、内存预留、存储池上的通用规则（可用性检查、容量准入、迁移矩阵、宕机恢复）、界面框架
- 所以按「共用框架 → GPFS → Ceph」的顺序做（§16），共用部分只做一次。GPFS 在等 24.04 节点的时候，Ceph 可以先在现有节点上把共用框架验证掉

---

## 4. 总体设计

### 4.1 层次

```
存储集群 storage_clusters            GPFS 集群 / Ceph 集群；托管（CloudLand 部署）或外部（导入）
 ├─ 成员节点 storage_cluster_nodes    计算节点 + 角色（GPFS：管理、仲裁、NSD、客户端；Ceph：管理、mon、mgr、OSD、客户端）
 ├─ 磁盘 storage_cluster_disks        被集群认领的磁盘（GPFS 的 NSD / Ceph 的 OSD）
 ├─ 文件系统 storage_filesystems      只有 GPFS：一个集群可以有多个文件系统
 └─ CloudLand 存储池 storage_pools    GPFS：文件系统里的一个独立 fileset；Ceph：一个 RBD 池
     └─ 卷 volumes                    云硬盘、系统盘
```

- **存储集群是区域级资源**，只有系统管理员能看到和操作
- 一个区域里可以有多个集群，GPFS 和 Ceph 可以同时存在；一台节点可以同时是一个 GPFS 集群和一个 Ceph 集群的成员（磁盘不能共用，§6.4）
- **一台节点最多属于一个 GPFS 集群**（GPFS 的限制：要访问别的 GPFS 集群只能走多集群远程挂载，放在阶段 S7）
- **一台节点最多为一个托管 Ceph 集群运行守护进程**（mon、mgr、OSD）：两个集群的 mon 会争 3300 / 6789 端口，OSD 的端口段也会冲突。作为客户端可以同时用多个 Ceph 集群
- 普通成员只看得到存储池（名称、类型、是否共享），看不到集群

### 4.2 组件分工

| 组件 | 职责 |
|---|---|
| 前端 | 软件包、存储集群的列表 / 详情 / 创建向导、任务进度与日志；存储池页面增加「所属集群」 |
| cpgateway | 代理白名单加入新接口（除 `GET /storage_pools` 外都是系统管理员专用）；配额不变 |
| clapi | 集群、节点、磁盘、文件系统、存储池的记录；**任务编排器**（把一次操作拆成步骤，下发到节点，按回调推进，超时处理）；预检；凭据加密保存；容量采集与准入；集群健康的后台看护。两台 clapi（HA）时，后台循环用 PostgreSQL 的 `pg_try_advisory_lock` 选一台执行，另一台空转。类型相关的部分全部经存储后端与存储池驱动两个接口（§4.5） |
| cland | **不改**。按 `inter=` / `toall=` 转发命令。注意 `toall=` 的成员列表为空时会发给**所有**节点（`cland/dispatcher.go:243-254`），编排器下发前必须确认列表非空 |
| cloudlet | **不改**。每个节点默认串行执行命令（`CLOUDLET_CONCURRENCY=1`），所以所有耗时的步骤都用 `async_exec` 在后台跑，不占命令队列（§6.2） |
| 节点脚本 | 新目录 `scripts/kvm/storage/`：`stc_*.sh`（通用：预检、下载、密钥、磁盘擦除、日志）、`backends/<类型>.sh`（每种存储的钩子）、`drivers/<驱动>.sh`（每个存储池驱动的函数）、`gpfs_*.sh`、`ceph_*.sh`（各自的步骤）；存储池的通用操作脚本（`*_volume_shared.sh`、`import_image_shared.sh`）放在 `scripts/kvm/` 下，与 `*_volume_local.sh` 并列（§4.5.2）。脚本清单见附录 B |

### 4.3 管理命令走哪条路

**GPFS**：在节点上直接执行 `mm*` 命令（`mmcrcluster`、`mmcrnsd`、`mmcrfs`、`mmaddnode` 等），由节点脚本包装。没有采用的做法：

| 做法 | 为什么不用 |
|---|---|
| IBM 安装工具（安装包里的 `ansible-toolkit`，`spectrumscale` 命令） | 要在一台节点上装 Ansible 和它的 Python 依赖；进度只有整体日志，拆不成 CloudLand 的步骤；它自己维护一份集群定义文件，和 CloudLand 的数据库是两份事实 |
| 图形界面（`gpfs.gui`）与 REST 接口（`gpfs.scaleapi`） | 图形界面占 443 端口（控制节点上 nginx 已占用），还要 PostgreSQL 和 Java；REST 接口要先有集群才能用，建集群这一步还是得用命令。CloudLand 的界面就是用来替代它的 |

**Ceph**：用 `cephadm` 和 `ceph orch`。Ceph 官方只维护 cephadm 和 Rook 两种部署方式，自己装包配守护进程的做法已经不推荐；cephadm 自己处理守护进程的放置、升级、替换盘，CloudLand 只需要调用它的命令。

两者都需要「**管理节点**」执行集群级的命令：

- GPFS：建集群时指定的主、备两台管理节点（`adminMode=central`，只有它们能免密登录其他成员）
- Ceph：带 `_admin` 标签的主机（cephadm 会把 `client.admin` 的密钥环同步到这些主机），建集群时是执行 bootstrap 的那台，之后建议加到 2–3 台

编排器把集群级的步骤用 `inter=<管理节点>` 下发；一台管理节点离线时换另一台，都离线时集群级操作不可用（每台节点自己的步骤仍可执行）。

### 4.4 一次操作的路径

```
界面 → cpgateway → clapi：建任务（storage_tasks + 步骤）
                     │
                     ├─ 每台节点的步骤：inter=<hostid> 或 toall=<成员>，脚本 async_exec 后台执行
                     │      └─ 结束后经心跳带回 |:-COMMAND-:| storage_task_run.sh ...（1–20 秒内）
                     ├─ 集群级的步骤：inter=<管理节点>
                     └─ 长步骤运行中：编排器每 15 秒下发一次 stc_poll.sh（同步、很快），取回日志尾部和进度

clapi 收到回调 → 更新这一步这台节点的结果 → 本步全部成功则下发下一步；有失败则任务停在「失败」，等管理员重试或中止
```

### 4.5 通用接口：存储后端与存储池驱动

2026-10-02 定下：GPFS、Ceph 和以后要加的共享存储（NFS、CephFS、Lustre、SAN 等）都经同一套接口接入，框架代码里不出现 `if kind == "gpfs"` 或 `if driver == "ceph_rbd"` 这样的分支。分三层：

| 层 | 通用到什么程度 | 加一种存储要做的 |
|---|---|---|
| 用户侧 | 完全通用：普通成员只看到「存储池（共享或不共享）」，建卷、挂载、扩容、迁移的接口不分类型 | 不用改 |
| 集群后端（部署、扩缩容、健康、隔离、删除） | 框架通用：任务引擎、作业协议、磁盘认领、预检、集群 SSH 密钥、内存预留、任务页面。类型相关的部分经 `StorageBackend` 接口取 | clapi 一个后端文件、节点一个钩子文件，加上这种存储自己的步骤脚本 |
| 存储池驱动（建卷、挂载、系统盘、开机、迁移） | 分「文件型」「块型」两类，各有公共实现。clapi 经 `PoolDriver` 接口取，节点上每个操作一个通用脚本、每个驱动一个函数文件 | 文件型只补差异（怎么确认挂对了、怎么克隆）；块型写一个驱动文件 |

#### 4.5.1 集群后端 `StorageBackend`

clapi 里每种存储一个文件（`services/storage_backend_<类型>.go`），在 `init` 里注册；框架只经这个接口取类型相关的信息。方法在第一次用到的阶段加上：

| 方法 | 作用 | 阶段 |
|---|---|---|
| `Kind`、`Roles`、`DiskRole`、`DefaultRoles` | 类型名；角色（每种都有 `client`，即只使用存储的节点）；贡献磁盘的角色（不收磁盘的类型为空）；表单里给新选的节点建议的角色 | S1（已实现） |
| `Requirements` | 支持的系统与内核、节点之间的端口、数据目录与所需空间、是否要编译内核模块、是否要容器 | S1（已实现） |
| `ParseParams` | 解析并校验这种存储的参数（`storage_clusters.params`），不认识的键直接拒绝（拼错的参数否则会被悄悄忽略、用了默认值） | S1（已实现） |
| `CheckLayout` | 角色规则（§6.3） | S1（已实现） |
| `HostConflict` | 一台节点能否同时在同类型的另一个集群里（§4.1） | S1（已实现） |
| `ReserveMB` | 每台节点要为存储进程预留的内存（§6.7） | S1（已实现） |
| `Capabilities` | 支持哪些模式和操作：托管 / 外部、文件系统层、重新均衡、客户端、改角色、升级。接口和界面按它开放，不支持的操作返回 400、界面上不显示 | S2 |
| `Steps` | 部署、导入、加减节点与盘、建删池、删除集群等任务的步骤列表，步骤定义注册进任务引擎（§6.2） | S2 / S3 |
| `PoolDriver`、`PoolParams` | 这个集群上的存储池用哪个驱动；建池时这种存储的参数与校验 | S2 / S3 |
| `ClientSpec` | 客户端节点要的配置，写进共享池清单和探测进程的输入（§9.2） | S2 / S3 |
| `Health` | 健康检查脚本与结果解析（§14.1），统一成集群、节点、磁盘、容量四类 | S5 |
| `Fence`、`Unfence`、`FenceStatus` | 宕机恢复时的隔离与解除（§11.2） | S6 |
| `UpgradeSteps`、`RotateKeysSteps` | 升级、凭据轮换 | S6 |

**数据放在哪**：所有类型都有的字段留成表上的列；只有某一种存储才有的，放进三个 JSON 列，只由它自己的后端读写（§5.1–§5.3）：`params`（管理员选的）、`attrs`（后端发现或生成的，如 Ceph 的 mon 地址与 libvirt secret 编号、GPFS 的故障组号）、`secrets`（凭据，整体加密）。

**节点侧**：每种存储一个钩子文件 `scripts/kvm/storage/backends/<类型>.sh`，通用脚本用 `stc_lib.sh` 的 `backend_load <类型>` 引入。S1 已有 `backend_existing`（节点上是否已有不归 CloudLand 管的这种软件，预检用）；以后按需加 `backend_health`、`backend_fence` 等。每种存储自己的步骤脚本（`gpfs_*.sh`、`ceph_*.sh`）照旧独立，由后端的步骤列表引用。

**界面**：`GET /storage_backends` 返回每种类型的角色、贡献磁盘的角色、建议角色、支持的系统；预检弹窗（以及 S2 的创建向导）按它渲染，不在前端写死类型和角色。只有参数表单按类型各写一个组件（`components/storage/params/<类型>.vue`，S2）。类型名、角色名、说明文字没有翻译时直接显示原值，不显示键名。

#### 4.5.2 存储池驱动 `PoolDriver`

数据通路只有两类，以后的存储大多能归进其中一类：

| 类 | 卷是什么 | 现有 | 以后可能接的 |
|---|---|---|---|
| 文件型 | 共享文件系统上的 qcow2 文件，libvirt 的 `type='file'` 磁盘 | GPFS | NFS、CephFS、Lustre、GlusterFS |
| 块型 | 网络块设备，raw 格式 | Ceph RBD（QEMU 自带的协议） | iSCSI / FC SAN 的 LUN |

**clapi**：每个驱动实现 `PoolDriver`（S2 起）：

| 方法 | 作用 |
|---|---|
| `Name`、`Family`、`Format` | 驱动名（`storage_pools.driver`）、文件型还是块型、卷格式（qcow2 / raw） |
| `VolumeRef` | 卷在池里叫什么：文件型是池内相对路径，块型是镜像名（§5.6） |
| `DriverArgs` | 下发给节点的驱动参数：文件型是池根目录与文件系统类型；RBD 是池名、配置文件、客户端用户、libvirt secret 编号（§9.7 的 `boot_disk`） |
| `Capacity` | 池容量从哪来（§9.5 的口径） |
| `CloneModes` | 支持的系统盘克隆方式（§9.6） |

**节点**：每个操作一个通用脚本、每个驱动一个函数文件，而不是「操作 × 驱动」各写一个脚本。通用脚本是 `scripts/kvm/` 下的 `create_volume_shared.sh`、`attach_volume_shared.sh`、`detach_volume_shared.sh`、`resize_volume_shared.sh`、`delete_volume_shared.sh`、`import_image_shared.sh`，加上探测进程 `shared_pool_probe.sh` 和处理系统盘的 `launch_vm.sh` 等。它们从 stdin 的 JSON 里读出 `driver`，引入 `scripts/kvm/storage/drivers/<驱动>.sh`，再调用以下固定的函数：

| 函数 | 作用 |
|---|---|
| `drv_guard` | 写之前确认连的是对的池：文件型核对文件系统类型与标记文件，RBD 读标记对象（§9.1、§9.2） |
| `drv_probe` | 探测进程用：池可不可用、容量多少 |
| `drv_exists`、`drv_size` | 卷在不在、实际多大（建卷不覆盖已有的卷、扩容失败时回滚都要用） |
| `drv_create`、`drv_resize`、`drv_delete` | 建卷、离线扩容、删卷（只删点名的那一个，§12.3） |
| `drv_disk_xml` | 生成 libvirt 磁盘 XML（缓存模式、出错策略，§12.2） |
| `drv_users` | 谁还开着这块盘（RBD 的监听者；文件型返回空），删卷失败时写进原因 |
| `drv_import_image`、`drv_clone`、`drv_copy` | 导入镜像基础副本；从基础副本克隆或整盘复制出系统盘（§9.6、§9.7） |
| `drv_source_uri` | 给 `qemu-img convert` 用的源地址（捕获镜像） |

文件型的公共实现放在 `drivers/file.sh`（用 `qemu-img` 和文件操作，路径一律校验在池根以内）。`drivers/gpfs.sh` 引入它，只覆盖三个函数：`drv_guard`（`stat -f` 的类型是 `gpfs`）、`drv_import_image` 与 `drv_clone`（用 `mmclone`）。回调按操作命名（`create_volume_shared` 等），不按驱动命名，rpcs 里每个操作一个处理函数。

本地池（`driver=local`）的脚本与回调已实施并验证过，这一轮不并进来。它在接口里相当于「文件型、不共享」，以后要统一时再改。

#### 4.5.3 每种存储都要回答的问题

接口规定了怎么接进来；下面这些问题接口替不了，每加一种存储都要单独设计、写进本文对应的章节。答不上来的，不开放共享迁移和宕机恢复：

| 问题 | GPFS | Ceph RBD |
|---|---|---|
| 支持哪些系统和内核、要不要内核模块 | 22.04 / 24.04 的 GA 内核，要（§2.2） | 24.04 / 26.04，不要 |
| 磁盘扫描怎么认出它的盘 | GPT 分区类型；节点装了 GPFS 时盘头有数据的空白盘判 `unknown_member`（§6.4） | LVM 标签 `ceph.osd_id`（§6.4） |
| 开机时怎么判断存储已就绪 | 文件系统已挂上、`drv_guard` 通过（§9.9） | 能读到池的标记对象 |
| 写满时云服务器是什么表现 | 暂停（`error_policy='stop'`，V8） | 磁盘 I/O 卡住，集群到 95% 时全部池都卡住（§9.5） |
| 怎么保证同一块盘只有一个写入者 | 状态机，加 QEMU 文件锁或 virtlockd（§12.1） | 状态机，加黑名单（§12.1） |
| 宕机时怎么隔离、怎么解除 | `mmexpelnode`（§11.2） | `blocklist range`，有效期要设长 |
| libvirt 是否接受这种盘的热迁移 | `cache='none'`（V6） | `cache='writeback'`（V6） |
| 容量和用量从哪来 | fileset 配额或所在的 GPFS 存储池（§9.5） | 池配额或 `stored + max_avail` |
| 存储进程要多少内存、哪些端口、哪些目录 | §6.5、§6.7 | 同左 |
| 有哪些凭据 | 集群 SSH 私钥 | 集群 SSH 私钥、客户端密钥、镜像仓库口令（放在 `secrets`） |

#### 4.5.4 加一种新存储要做什么

以「导入外部 NFS 作为共享池」为例（只导入、不部署）：

1. **clapi**：`storage_backend_nfs.go` 注册后端：角色只有 `client`，不收磁盘；`Capabilities` 只开「外部」；`Steps` 只有导入（在节点上挂载导出、检查）和取消登记
2. **节点**：`backends/nfs.sh`（`backend_existing` 等钩子）；`drivers/nfs.sh` 引入 `file.sh`，`drv_guard` 核对文件系统类型为 `nfs4` 并检查标记文件，系统盘只支持整盘复制
3. **回答 §4.5.3**：NFS 自己没有隔离手段（撤销某个客户端的导出要靠 NFS 服务器那边），所以先不开放宕机恢复；写满时返回 `ENOSPC`，按本地盘的方式暂停
4. **界面**：三种语言加类型名与说明，写一个参数表单组件（导出地址、挂载选项）
5. **用例**：新增

不用改的：任务引擎、预检框架、磁盘认领、准入、迁移矩阵、待启动列表、用户侧的接口和页面。

---

## 5. 数据模型

表结构由启动时的 AutoMigrate 创建，不写迁移代码。零值有业务含义的字段（布尔开关、0 表示「不限」的数值）不加 GORM 的 `default` 标签。所有表都继承带软删除的 `Model`，所以**唯一约束一律建成只覆盖未删除行的部分唯一索引**（`dbs.AutoUpgrade` 里 `CREATE UNIQUE INDEX ... WHERE deleted_at IS NULL`，照中转网关的做法），否则删掉的集群、节点、盘会挡住同名重建或重新认领。

### 5.1 `storage_clusters`（新增）

```go
// StorageCluster is a GPFS or Ceph cluster in a region
type StorageCluster struct {
	Model
	Name       string `gorm:"type:varchar(64)"` // unique among live rows
	Kind       string `gorm:"type:varchar(16)"` // gpfs | ceph
	Mode       string `gorm:"type:varchar(16)"` // managed | external
	Layout     string `gorm:"type:varchar(16)"` // gpfs: replica | ece; ceph: empty
	Status     string `gorm:"type:varchar(16)"` // planning | deploying | ready | degraded | error | deleting
	Health     string `gorm:"type:varchar(16)"` // healthy | warning | error | unknown
	HealthInfo string `gorm:"type:text"`        // json summary for the detail page
	HealthAt   *time.Time
	Version    string `gorm:"type:varchar(32)"`
	PackageID  int64  // storage_packages row the software came from, 0 = none (ceph installs from the distribution)
	ClusterRef string `gorm:"type:varchar(128)"` // the storage software's own id: gpfs cluster name and id, ceph fsid
	PublicNet  string `gorm:"type:varchar(64)"`
	ClusterNet string `gorm:"type:varchar(64)"`
	Params     string `gorm:"type:text"` // json: what the admin chose, kind specific (StorageBackend.ParseParams)
	Attrs      string `gorm:"type:text"` // json: what the backend found out or generated, e.g. ceph mon addresses
	Secrets    string `gorm:"type:text"` // json of kind specific credentials, encrypted as a whole
	Unsupported bool  // deployed on an OS / kernel outside the support matrix (§6.5)
	ActiveTask     int64  // structural task holding the cluster (§6.2), 0 = none
	ActivePoolTask int64  // pool task (create / change / delete a CloudLand pool), 0 = none
	PendingClients string `gorm:"type:varchar(1024)"` // json: hosts waiting to join as clients (§6.3)
	AutoJoinZones  string `gorm:"type:varchar(256)"`  // json: zones whose new hosts join as clients, empty = off
	SSHPubKey      string `gorm:"type:text"` // cluster admin key (§6.6), every kind that manages its members over SSH
	SSHPrivKey     string `gorm:"type:text"` // encrypted
	Description    string `gorm:"type:varchar(256)"`
}
```

- **列只放所有类型都有的东西**（§4.5.1）。某一种存储才有的放进三个 JSON 列，只由它的后端读写：
  - `params`（管理员选的）：所有类型都有 `test`（单节点测试形态）；GPFS 有 `fs_name`、`block_size`、`data_replicas`、`pagepool_mib`；Ceph 有 `replicas`、`osd_memory_target_mib`，S3 再加 `image`（容器镜像）、`registry_user`。后端解析时拒绝不认识的键
  - `attrs`（后端发现或生成的）：Ceph 的 `client_user`（不带 `client.`，托管集群为 `cloudland`）、`mon_addrs`、`secret_uuid`（libvirt secret 编号，所有节点相同）
  - `secrets`（凭据，整个 JSON 加密）：Ceph 的 `client_key`、`registry_password`
  - 集群 SSH 密钥、软件包是框架的通用机制（§6.6、§6.1），留成列
- **`Status` 与 `Health` 分工**：`Status` 是 CloudLand 对集群的操作阶段（部署中、就绪、删除中、部署失败 `error`），只由任务改；`Health` 是存储软件自己报告的健康，只由健康看护（§14.1）改。`degraded` 不再作为 `Status` 的取值，「降级」看 `Health`，所以看护对所有 `ready` 的集群一直检查，降级后也能恢复
- **外部集群**：GPFS 只记文件系统和挂载点（节点上的 GPFS 由管理员装好，CloudLand 只检查和使用，§7.8）；Ceph 记 mon 地址、fsid、客户端用户名（不要求叫 `cloudland`）和密钥，CloudLand 负责在计算节点上配置客户端（§8.8）。所有 `ceph` / `rbd` / `rados` 命令和磁盘 XML 一律用 `attrs.client_user`，不写死 `cloudland`

### 5.2 `storage_cluster_nodes`（新增）

```go
type StorageClusterNode struct {
	Model
	ClusterID     int64  // (cluster_id, hostid) unique among live rows
	Hostid        int32
	Roles         string `gorm:"type:varchar(128)"` // comma separated, see below
	Attrs         string `gorm:"type:text"`         // json, kind specific: gpfs failure_group
	Status        string `gorm:"type:varchar(16)"` // joining | active | down | leaving | error
	State         string `gorm:"type:varchar(64)"` // what the storage software says: gpfs mmgetstate, ceph host status
	Reason        string `gorm:"type:varchar(512)"`
	CheckedAt     *time.Time
	ReservedMemMB int32 // memory held back from instance scheduling for the daemons on this node (§6.7)
}
```

角色：

| 类型 | 角色 | 说明 |
|---|---|---|
| GPFS | `admin` | 管理节点，持有集群 SSH 私钥，执行集群级命令；1–2 台，必须同时是 `quorum` |
| GPFS | `quorum` | 仲裁节点，奇数台（3、5、7；1 台只用于单节点测试形态），也作为管理器（`manager`） |
| GPFS | `nsd` | 贡献本地盘作为 NSD，并作为这些 NSD 的服务节点 |
| GPFS | `client` | 只挂载文件系统、不承担别的角色的成员。GPFS 的成员都挂载文件系统，`client` 只是「没有别的角色」的标记 |
| Ceph | `admin` | 带 `_admin` 标签，有 `client.admin` 密钥环；必须同时是 `mon`，第一台 `admin` 执行 bootstrap |
| Ceph | `mon` / `mgr` | 1、3、5 台 mon（1 台只用于单节点测试形态），1–2 台 mgr |
| Ceph | `osd` | 贡献磁盘作为 OSD |
| Ceph | `client` | 不跑任何守护进程、只配置了客户端的计算节点。所有带守护进程的成员也都配置客户端 |

角色表由各类型的后端声明（`StorageBackend.Roles`，§4.5.1），上表是 GPFS 与 Ceph 的。`client` 是所有类型共有的角色。

GPFS 的故障组号（`attrs.failure_group`）在集群内从 1 开始分配，不用 hostid：hostid 只增不复用，而 GPFS 对故障组号有上限。

节点删除（`DELETE /hypers/:uuid`）时，如果它还是任何存储集群的成员，拒绝，提示先从集群移除；节点永久坏掉、在线移除做不了时，用离线移除（§7.5、§8.5）。

### 5.3 `storage_cluster_disks`（新增）

```go
// StorageClusterDisk is a disk claimed by a storage cluster; the claim lives in the database and on the disk itself
type StorageClusterDisk struct {
	Model
	ClusterID int64
	Hostid    int32  // (hostid, disk_id) unique among live rows: a disk belongs to one cluster at a time
	DiskID    string `gorm:"type:varchar(256)"` // hyper_disks.disk_id, e.g. wwn-0x..., loop:<file>, dev:<name>
	Serial    string `gorm:"type:varchar(128)"` // identity checked on the host right before the disk is used (§6.4)
	WWN       string `gorm:"type:varchar(64)"`
	SizeBytes int64
	Role      string `gorm:"type:varchar(16)"` // the disk role of the kind (StorageBackend.DiskRole): nsd | osd
	Name      string `gorm:"type:varchar(64)"` // the storage software's name for it: gpfs NSD name, ceph osd.N
	Media     string `gorm:"type:varchar(16)"` // ssd | hdd | nvme
	FsID      int64  // file system the disk is in, for kinds with that layer (gpfs); 0 = none / free NSD
	Attrs     string `gorm:"type:text"`        // json, kind specific: gpfs usage (dataAndMetadata | metadataOnly | dataOnly | descOnly) and gpfs_pool (system | data)
	Status    string `gorm:"type:varchar(16)"` // claiming | active | draining | removing | failed
	Reason    string `gorm:"type:varchar(512)"`
}
```

`hyper_disks`（已有）增加两个状态 `gpfs_nsd`、`ceph_osd`，由磁盘扫描按盘上的特征和这张表判断（§6.4）。

### 5.4 `storage_filesystems`（新增，目前只有 GPFS）

集群与 CloudLand 存储池之间的可选一层，有这一层的类型才用（`Capabilities` 声明，§4.5.1）；以后的 CephFS 也可以用它。

```go
type StorageFilesystem struct {
	Model
	ClusterID     int64  // (cluster_id, name) unique among live rows
	Name          string `gorm:"type:varchar(32)"`  // GPFS device name, e.g. fs1
	MountPoint    string `gorm:"type:varchar(256)"` // same on every node, /gpfs/<name>
	BlockSize     string `gorm:"type:varchar(8)"`   // 4M by default
	DataReplicas  int32
	MetaReplicas  int32
	Status        string `gorm:"type:varchar(16)"` // creating | ready | error | deleting
	PolicyGen     int64  // generation of the placement policy CloudLand installed (§7.4)
	CapacityBytes int64
	FreeBytes     int64
	CapacityAt    *time.Time
}
```

Ceph 没有这一层：Ceph 池直接对应 CloudLand 存储池。

### 5.5 `storage_pools`（已有）的变更

| 字段 | 变更 |
|---|---|
| `Driver` | 新增取值 `gpfs`、`ceph_rbd`（现在只有 `local`，`model/storage_pool.go:16`） |
| `Status` | 共享池新增 `creating`（建池任务进行中，不能被选用）、`error`（建池失败）；任务成功后变 `active`。本地池不变 |
| `ClusterID int64` | **新增**，共享池所属的集群；本地池为 0 |
| `FilesystemID int64` | **新增**，GPFS 池所在的文件系统 |
| `DriverParams string` | **新增**，JSON，驱动专用的参数，只由驱动读写（§4.5.2）：GPFS 的 `fileset`（独立 fileset 名）、`gpfs_pool`（数据落在文件系统里的哪个 GPFS 存储池，`system` / `data`）；Ceph 的 `ceph_pool`（池名）、`crush_rule`（按介质：`cl-ssd`、`cl-hdd`） |
| `Replicas int32` | **新增**，Ceph 池的副本数（GPFS 的副本数在文件系统上） |
| `QuotaBytes int64` | **新增**，池的配额（GPFS 的 fileset 配额 / Ceph 的池配额），0 表示不设 |
| `CloneMode string` | **新增**，`clone`（GPFS 的 `mmclone` / Ceph 的 `rbd clone`）或 `copy` |
| `CapacityBytes`、`UsedBytes int64`、`CapacityAt *time.Time` | **新增**，共享池的池级容量（本地池按节点汇总，不用这三列），口径见 §9.5 |
| `MountPath` | GPFS 池为 fileset 的入口目录（`<挂载点>/<fileset>`）；Ceph 池为空 |

`Shared()`（`model/storage_pool.go:44`）已经是 `Driver != local`，不用改。`Root()` 对 Ceph 池没有意义，调用方按驱动区分（§9.1）。

共享池在 `hyper_storage_pools` 里**每台能访问它的节点一行**，只用 `ready` / `unavailable` 两个状态，由可用性检查写入（§9.2）。本地池的那些状态（`creating`、`extending`、`lost` 等）对共享池不适用；现有代码里把「节点上有非内置池的行」当作「节点上有本地池」的地方（如删除节点时的 `releaseStorage`，`services/hyper.go:628-641`）都要限定 `driver = local`（附录 A）。

### 5.6 `volumes`（已有）的变更

| 字段 | 变更 |
|---|---|
| `Path` | GPFS 池为池内相对路径 `volumes/volume-<卷ID>.disk`；Ceph 池为 RBD 镜像名 `volume-<卷ID>` |
| `Hyper` | 只对本地池有意义，共享池的卷恒为 0 |
| `Format` | GPFS 池为 `qcow2`；Ceph 池为 `raw`（RBD 镜像只能是 raw） |
| `BaseImageStorageID int64` | **新增**，系统盘克隆自哪个镜像基础副本（`image_storages.id`），0 表示整盘复制、没有父对象；用于删除镜像基础副本时的引用计数（§9.6） |

⚠️ **`hyper = 0` 现在的含义是「还没在任何节点上建出文件」**：删卷时只删记录、不通知节点（`services/volume.go:432`），扩容时只改数据库（`:613`、`:652`），挂载时走 `new` 模式新建文件（`:319`）。共享卷的 `hyper` 恒为 0，如果不改这些分支，删卷留下孤儿、扩容不生效、挂载会覆盖已有的卷。规则改为：**本地卷「已落盘」= `hyper > 0`；共享卷建卷成功（离开 `pending`）就已落盘**。所有读 `volumes.hyper` 的地方（含 rpcs 回调与迁移代码）都按这条改，清单在附录 A。

### 5.7 `image_storages`（新增）

原来的 `ImageStorage` 随 WDS 一起删掉了，这里重新建一张，用途是「一个镜像在一个共享池里有一份基础副本」：

```go
type ImageStorage struct {
	Model
	ImageID       int64  // (image_id, storage_pool_id) unique among live rows
	StoragePoolID int64
	Path          string `gorm:"type:varchar(256)"` // gpfs: images/image-<id>-<prefix>.qcow2; ceph: image-<id>-<prefix> (snapshot "base")
	Status        string `gorm:"type:varchar(16)"`  // syncing | synced | error | deleting
	Reason        string `gorm:"type:varchar(512)"`
}
```

`<前缀>` 沿用现有约定：镜像 UUID 的第一段（`services/s3.go` 的 `s3ObjectName`）。

### 5.8 `storage_packages`（新增，阶段 S2）

```go
// StoragePackage is an uploaded installer of storage software, kept in S3
type StoragePackage struct {
	Model
	Kind        string `gorm:"type:varchar(16)"`   // gpfs
	Edition     string `gorm:"type:varchar(32)"`   // erasure_code | data_management | standard | developer, from the license package
	Version     string `gorm:"type:varchar(32)"`   // 6.0.0.2
	FileName    string `gorm:"type:varchar(256)"`
	SizeBytes   int64
	SHA256      string `gorm:"type:varchar(64)"`   // computed after upload, shown so it can be compared with IBM's download page
	ObjectKey   string `gorm:"type:varchar(256)"`  // storage-packages/<uuid>/<file name>
	UploadID    string `gorm:"type:varchar(256)"`  // S3 multipart upload in progress
	PartsDone   int32                               // parts uploaded so far, in order
	SourceURL   string `gorm:"type:varchar(1024)"` // set when clapi downloads the package itself
	PayloadLine int32                               // line of the installer where its tar.gz starts (PGM_BEGIN_TGZ)
	Distros     string `gorm:"type:varchar(256)"`  // json: ["ubuntu22","ubuntu24",...]
	Manifest    string `gorm:"type:text"`          // json: package file name -> md5
	LicenseText string `gorm:"type:text"`          // json: language -> license text
	AcceptedBy  string `gorm:"type:varchar(64)"`   // user name snapshot, empty = not accepted
	AcceptedAt  *time.Time
	Status      string `gorm:"type:varchar(16)"` // uploading | verifying | ready | error
	Reason      string `gorm:"type:varchar(512)"`
}
```

Ceph 不需要上传：软件包来自发行版源，守护进程来自容器镜像（§8.1）。

### 5.9 任务：`storage_tasks`、`storage_task_steps`、`storage_task_runs`（新增）

```go
type StorageTask struct {
	Model
	ClusterID   int64  // 0 for a task not tied to a cluster (precheck, selftest)
	Kind        string `gorm:"type:varchar(32)"`
	Status      string `gorm:"type:varchar(16)"` // running | failed | aborting | succeeded | aborted
	Params      string `gorm:"type:text"`        // json, what the task was asked to do
	CurrentStep int32
	CreatorName string `gorm:"type:varchar(64)"` // user name snapshot (clapi cannot resolve user IDs)
	Message     string `gorm:"type:varchar(1024)"`
	FinishedAt  *time.Time
}

type StorageTaskStep struct {
	ID         int64  `gorm:"primaryKey"`
	TaskID     int64  // (task_id, seq) unique
	Seq        int32
	Name       string `gorm:"type:varchar(64)"`   // precheck | install | build_gpl | ssh_trust | create_cluster | ...
	Scope      string `gorm:"type:varchar(16)"`   // nodes | admin
	Hostids    string `gorm:"type:varchar(2048)"` // json: hosts of a nodes step, candidate hosts of an admin step
	Status     string `gorm:"type:varchar(16)"`   // pending | running | failed | succeeded
	TimeoutSec int32  // 0 = no limit
	StartedAt  *time.Time
	FinishedAt *time.Time
}

type StorageTaskRun struct {
	ID         int64  `gorm:"primaryKey"`
	StepID     int64  // (step_id, hostid, attempt) unique
	Hostid     int32
	Attempt    int32
	Status     string `gorm:"type:varchar(16)"` // dispatched | running | failed | succeeded
	Dispatches int32  // how many times the command was sent (§6.2, outbox)
	Progress   int32  // 0-100, optional
	Message    string `gorm:"type:varchar(1024)"`
	LogTail    string `gorm:"type:text"` // last 64 KiB of the node log
	Result     string `gorm:"type:text"` // json output consumed by later steps (host keys, device names, NSD names, OSD ids, ...)
	StartedAt  time.Time
	UpdatedAt  time.Time
	PolledAt   *time.Time
}
```

**任务种类**（`Kind`）：

| 种类 | 占用 | 说明 |
|---|---|---|
| `precheck` | 不占集群（`ClusterID = 0`） | 只做预检（§6.5） |
| `selftest` | 不占集群 | 只用于测试任务引擎：脚本按参数睡眠、失败、输出进度（§16 S1） |
| `deploy`、`import`、`add_nodes`、`remove_node`、`add_clients`、`remove_clients`、`add_disks`、`remove_disk`、`replace_disk`、`rebalance`、`create_fs`、`delete_fs`、`delete_cluster`、`upgrade`、`rotate_keys` | 结构槽（`active_task`） | 改动集群结构的操作，一个集群同一时间一个 |
| `create_pool`、`update_pool`、`delete_pool` | 池槽（`active_pool_task`） | 改 CloudLand 存储池的操作。只要集群是 `ready`，就不被长时间的结构任务（重新均衡、移除盘）挡住 |

不占集群的任务同样可以重试和中止，只是不检查也不占用任何槽；列表接口用 `cluster_id=0` 查。

现有的通用 `tasks` 表（`model/task.go:44-57`）现在只被迁移当作阶段记录用，但它有对外的 `/tasks` 接口（`apis/task.go`）；存储任务不写进这张表，免得混进那个列表。每节点状态的写法参考中转网关的 `TgwNodeState`（`model/transit_gateway.go:98-107`）。

### 5.10 凭据

集群 SSH 私钥、Ceph 客户端密钥、私有镜像仓库的口令，都用 `common/secret.go` 的 AES-256-GCM 加密后入库（与 VPN 的预共享密钥同一套）。集群 SSH 私钥是通用机制，单独一列；某种存储自己的凭据（Ceph 的客户端密钥与仓库口令）放在 `storage_clusters.secrets` 这个 JSON 里，整体加密一次（§5.1）。错误码 `ErrVpnSecretUnavailable` 已改名为 `ErrSecretUnavailable`（数值 132031 不变）。配置键 `vpn.secret_key`（环境变量 `VPN_SECRET_KEY`）**暂不改名**：改名要同步改每个部署环境的 `.env`，否则 VPN 凭据全部解不开，等决策 D7（§18.1）。将来改名时，HKDF 的两个固定参数（`"cloudland-vpn"`、`"vpn-secrets-v1"`，`common/secret.go:44`）**不能改**，否则旧密文作废；名字建议用 `CREDENTIAL_KEY` 这类不会和 `S3_SECRET_KEY`、`CPGATEWAY_SECRET_KEY`、`AUTH_SECRET_KEY` 混淆的。没配密钥时，新建托管集群和导入 Ceph 集群返回 503，与 VPN 相同。

---

## 6. 两种集群共用的机制

### 6.1 软件包仓库（阶段 S2）

只有 GPFS 用。流程：

1. **上传**：界面分片上传，每片 8 MiB（nginx 的 `/api/v1/` 限制请求体 10 MB，`deploy/docker/config/nginx/ssl.conf:55`；cpgateway 会把整个请求体读进内存，`cpgateway/src/apis/proxy.go:43`，8 MiB 在可接受范围内）。clapi 把每一片写成 S3 分段上传的一段（S3 要求除最后一段外每段不小于 5 MiB，最多 10000 段，单个包最大约 80 GB）
   - **只能按顺序传**：第 n 片只在已完成 n−1 片时接受（重传第 n−1 片覆盖原段，用于中断后重试）。`GET /storage_packages/:id` 返回 `parts_done`，界面断线后从下一片继续；合并时用 S3 的 `ListObjectParts` 取各段的 ETag，不在库里存
   - 删除软件包、或者上传 24 小时没有进展时，`AbortMultipartUpload` 清掉已传的段（MinIO 没配未完成上传的生命周期规则，不清会一直占空间）
   - 也可以填一个下载地址，由 clapi 自己拉取写入 S3，与镜像按地址导入的做法相同（`services/image.go:236-256`）
2. **校验**（clapi 后台）：从 S3 读出文件，算 SHA-256；读脚本头的 `PGM_BEGIN_TGZ` 与 `PROD_FILES`（支持的发行版来自后者的 `gpfs_debs/ubuntu/ubuntu22` 这类目录），从该行起解 tar 流，取出 `manifest`（软件包与 md5）和许可证文本（`LA_HOME/LA_<语言>`、`LI_<语言>`，UTF-16 编码，转成 UTF-8 存），从 `gpfs.license.*` 包名得出版本类型。只读不执行，不需要 Java。读一遍 1.7 GB 的压缩流约 1–2 分钟；clapi 在校验中途重启时，维护循环把卡在 `verifying` 超过 30 分钟的包重新校验
3. **接受许可证**：界面显示许可证文本，系统管理员勾选同意后才能用这个包部署。记录同意人和时间，进审计（`storage_package.accept_license`）
4. **节点安装**（部署任务的步骤，§7.2）：节点用预签名地址下载（`GenerateDownloadURL` 现在只接受镜像，`services/s3.go:188-198`，改成按对象键生成），校验 SHA-256，**不执行安装包脚本本身**，而是从第 `PayloadLine` 行起直接解 tar 流里的 `gpfs_debs`（与 clapi 的校验同一个做法；不必以 root 运行一个 1.7 GB 的自解压脚本，也不需要它自带的 Java），按 `manifest` 的 md5 校验要装的包，`apt-get install`。许可证的同意由第 3 步在 CloudLand 里完成并记录

**完整性**：上传时算出的 SHA-256 在界面上显示，供管理员和 IBM 下载页给出的值对照；包内各 deb 再按 `manifest` 的 md5 校验。安装包里带有 IBM 的签名公钥（`Public_Keys/`），能否用它校验 deb 的签名待验证（V19）。

**不启用 S3（`S3_ENDPOINT` 为空）时不能用托管 GPFS**，界面提示先配置对象存储；外部集群不受影响。

下载缓存放在节点的 `/opt/cloudland/cache/storage-pkg/<sha256>/`，用 `flock` 加 `.partial` 原子替换（照搬 `ensure_image_cached`，`scripts/cloudrc:339-375`）；安装完成后删除解出来的包，只保留安装包本身，以后加节点、重装时不必重新下载。

### 6.2 任务引擎

**一次操作 = 一个任务 = 一串步骤**，每一步在一组节点或一台管理节点上执行一个脚本。编排器在 `services/storage_task.go`。

#### 6.2.1 槽与并发

- 结构任务占集群的结构槽（`active_task`），池任务占池槽（`active_pool_task`），各自同一时间一个（§5.9）。新任务在事务里 `FOR UPDATE` 锁集群行后检查，槽被占就返回 409
- **失败的任务继续占着槽**，直到管理员重试或中止：半完成的集群上不该再跑别的结构操作
- 自动加入客户端（§6.3）不受 409 影响：请求先记进集群的 `pending_clients`，结构槽空出来时由后台循环发起 `add_clients` 任务
- 任务里一切状态变化都在锁住任务行的事务里做（建运行、收回调、超时判失败、重试、中止），所以两台 clapi、回调和后台循环同时推进同一个任务也不会冲突
- **后台循环只在一台 clapi 上跑**：每轮先 `pg_try_advisory_lock`，拿不到就空转。否则轮询和健康检查会对同一节点各发两遍

#### 6.2.2 下发（outbox）

- 推进时，在锁住任务行的事务里给这一步的每台节点建一条运行记录（状态 `dispatched`），**事务提交之后再下发命令**；下发失败的直接置失败
- clapi 恰好在提交和下发之间重启时，运行停在 `dispatched`。轮询会问节点（§6.2.4），节点答「没有这个作业」时，clapi **重新下发**同一条运行（`dispatches` 加一），超过 30 分钟仍不见作业才判失败。节点上作业的启动是幂等的（§6.2.3），重复下发不会跑出两份
- 命令格式：`/opt/cloudland/scripts/backend/storage/<脚本> '<运行ID>' <<'EOF' <JSON 输入> EOF`。输入由编排器在下发前从数据库和前面步骤的输出（`storage_task_runs.result`）生成，所以重试用的是最新的数据。命令经环境变量交给执行器，单个字符串上限 128 KiB，输入超过 120 KiB 时这条运行直接失败

#### 6.2.3 节点上的作业

所有步骤脚本共用 `scripts/kvm/storage/stc_lib.sh`：

- **作业目录**：`/opt/cloudland/run/storage/jobs/<运行ID>/`，在磁盘上、保留 7 天，里面有：`accepted`（收到命令的时间）、`pid`、`boot_id`（`/proc/sys/kernel/random/boot_id`）、`progress`、`exit`（退出码）、`callback`（最终的回调行）。日志在 `/opt/cloudland/log/storage/run-<运行ID>.log`，保留 90 天
- **启动是幂等的**：脚本先在前台（同步部分）检查作业目录：已有 `exit` → 只把 `callback` 再输出一次；有 `pid` 且进程还在、`boot_id` 没变 → 什么都不做（作业正在跑）；否则才用 `async_exec` 起后台作业。同步部分只做这几件很快的事，长活全在后台
- **同一集群一把锁**：后台作业先拿 `flock /var/lock/cloudland-storage-<集群UUID>.lock`，同一台节点上同一集群的作业不会并发（比如超时后重试时旧作业还在跑）。拿不到锁就等，等待计入步骤的超时
- **stdout 只用于回调**：`async_exec` 会把后台作业的 stdout 和 stderr 一起写进待发文件，心跳时整份发给 clapi（`report_rc.sh` 的 `sync_delayed_job`），所以作业一开始就把 stdout、stderr 都重定向到日志文件，回调行写到单独保存的描述符上
- **结束**：写 `exit` 和 `callback`，再把回调行输出：`|:-COMMAND-:| storage_task_run '<运行ID>' '<succeeded|failed>' '<进度>' '<base64 JSON>'`，JSON 里是消息、日志尾部（最多 64 KiB）和这一步的输出
- 每个步骤脚本都必须**可重入**：先检查目标状态是否已经达成（包已装、模块已编译、节点已在集群里、NSD 已存在），达成就直接报成功（附录 B 写了每个脚本的判断条件）。这样重试只需对失败的节点重跑，成功过的节点不重复执行

#### 6.2.4 结果送达与轮询

- 后台作业结束后，结果由下一次心跳带回（`report_rc.sh:400-406`，1–20 秒）。但 `.done` 文件在投递之前就被删掉，cland 转发失败最多重试 3 次，所以**心跳这条路可能丢结果**
- 补救靠轮询：后台循环每 15 秒检查一次，对超过 15 秒没有消息的运行下发 `stc_poll.sh '<运行ID>'`（同步命令，只读作业目录，毫秒级）。`stc_poll.sh` 是作业状态的权威来源，回答四种情况之一：
  - 已结束 → 把保存的 `callback` 再发一次（clapi 按运行记录去重）
  - 在跑 → `running`，带进度和日志尾部
  - 进程不在了而没有 `exit`，或者 `boot_id` 变了（节点重启过） → `failed`，原因「作业中断」
  - 作业目录不存在 → `missing`：命令还在节点的队列里排着，或者根本没到。clapi 重新下发（§6.2.2）
- 轮询命令会在节点的串行队列里排在别的长命令后面（`launch_vm.sh` 下载转换镜像、`source_migration.sh` 整个迁移都是同步执行的），所以**同一个运行有一条轮询还没回复时不再发新的**（2 分钟内），也不把「轮询没回来」当成失败
- 回调处理幂等：cland 转发回调失败会重试最多 3 次，同一个结果还可能经心跳和轮询各到一次。已终结的运行不再改；回调的节点必须是运行记录里的节点

#### 6.2.5 超时、重试、中止

- **超时**：每步有超时（安装 30 分钟、编译 15 分钟、建文件系统 60 分钟等；重新均衡、移除盘这类数据迁移不设上限，靠轮询的「作业中断」发现异常）。运行超时或者节点离线超过 5 分钟，这台节点的这一步置为失败，原因写明
- **重试**：`POST /storage_tasks/:id/retry` 从失败的那一步重新开始，只对失败的节点再下发（新的 `attempt`）。用到「上一步解析出的设备名」的步骤声明了 `RetryFrom`，重试时从解析那一步重新开始（§6.4）。旧作业如果还在跑（超时判失败但进程还在），新作业会等在同一把集群锁上，不会两份同时干活
- **中止**：`POST /storage_tasks/:id/abort` 先把任务置 `aborting`，对还在跑的运行下发 `stc_kill.sh '<运行ID>'`（杀掉作业的进程组），等这些运行都终结（或节点离线超过 5 分钟）后才置 `aborted` 并释放槽。不自动回滚：部分失败时很难做对，还会把排查现场清掉；半成品由管理员另行发起「删除集群」清理（删除集群能清理任何阶段的半成品，§7.6、§8.6）

#### 6.2.6 日志

- 回调带回最后 64 KiB，界面直接显示
- 完整日志（阶段 S5）：`GET /storage_tasks/:id/runs/:run/log` 先下发 `stc_upload_log.sh`，节点把日志文件上传到 clapi 的新接口 `/internal/storage_runs/:id/log`，clapi 存进 S3 后，接口再次调用时返回预签名的下载地址（界面先显示「正在取回」，轮询到地址为止）。令牌与捕获镜像同一套 HMAC 机制和上传密钥，但签名内容加用途前缀（`"log|<运行ID>|<过期时间>"`），日志令牌不能用来覆盖镜像
- 日志里不能出现凭据：脚本不回显密钥，`set -x` 只在不处理凭据的段落里用

### 6.3 节点与角色

- **只有已注册的计算节点能加入存储集群**（要经 cloudlet 下发命令）。专用的存储节点也按计算节点注册，然后在节点详情里设为「不调度云服务器」：用现有的禁用状态（`PATCH /hypers/:uuid` 改 status），禁用的节点不参与调度，但 cloudlet 照常执行命令
- 向导里列出区域内所有在线节点，显示操作系统、内核、内存、可用磁盘、是否已在别的集群里、是否在支持范围内（§6.5）
- **角色的校验**：
  - GPFS：仲裁节点奇数台；管理节点 1–2 台且必须是仲裁节点；**有盘的节点（故障组）至少 3 个**，只有 2 个时必须另加一块 `descOnly` 盘作第三个故障组：文件系统描述符的法定人数按故障组算，只有两个故障组时丢掉其中之一整个文件系统就卸载；数据、元数据副本数不超过故障组数，并且按 GPFS 存储池分别算（`system` 池里的故障组数要不少于元数据副本数）；全部成员都必须在 GPFS 支持的系统上（§2.2）
  - Ceph：mon 奇数台；mgr 1–2 台；`admin` 必须是 mon；OSD 所在主机数不少于副本数；托管集群的副本数只能是 3（或单节点测试形态的 1），不允许 2：Ceph 的 `size=2` 配 `min_size=1` 会单副本写入，配 `min_size=2` 坏一台就停写
  - 单节点测试形态（1 台仲裁 / 1 个 mon、副本 1）只在参数里显式选「测试」时允许，集群详情一直显示警告
- **自动加入客户端**：集群设置 `auto_join_zones`（可用区列表），这些可用区里新上线的节点记进 `pending_clients`，结构槽空出来时由后台循环发起 `add_clients` 任务（§6.2.1）；节点的系统不在支持范围内时不加，在集群详情页上列出

### 6.4 磁盘认领与身份核对

**规则**：一块盘在任一时刻只属于一方——本地存储池、一个 GPFS 集群（NSD）、一个 Ceph 集群（OSD），或者空闲。

两道保证：

1. **数据库**：`storage_cluster_disks` 里有记录的盘，不能再被别的集群或本地池选中；本地池建池时的选盘（`services/hyper_storage.go:395-428` 的 `pickDisks`）增加这项检查。保存扫描结果（`SaveScannedDisks`）时，登记过的盘一律按登记的角色显示为 `gpfs_nsd` / `ceph_osd`
2. **盘上的特征**（扫描时识别，防止数据库与实际不一致，`classify_disk`，`scripts/kvm/storage_lib.sh:271`）：
   - **Ceph OSD**：盘上是 LVM 物理卷，卷组名 `ceph-*`、逻辑卷带 `ceph.osd_id`、`ceph.cluster_fsid` 标签。现在判为 `unknown_member`（有 LVM 签名、读不出 CloudLand 的标签，`storage_lib.sh:296-299`），本地池已经会拒绝；改为识别成 `ceph_osd` 并带上编号和 fsid
   - **GPFS NSD**：旧格式（v1）的 NSD 盘上没有 `blkid` 认得的签名，现在会判为 `free`；新格式（v2）在盘上写 GPT 分区表、分区类型 GUID 为 GPFS 专用值，现在会判为 `dirty`。两种都危险：`dirty` 的盘勾选「擦除」就能拿去建本地池。识别办法按可靠程度：分区类型 GUID（v2，`lsblk -o PARTTYPE` 就能读）→ 节点装了 GPFS 时用 GPFS 自带的识别命令（`tspreparedisk -s`，输出格式待验证，V2）。**节点上只要装了 GPFS（`/usr/lpp/mmfs/bin/mmfsd` 存在），无法确认「不是 NSD」的空白盘一律判为 `unknown_member`，不判 `free`**（外部 GPFS 集群的 NSD 不在 CloudLand 的库里，只能这样兜底）
   - **实际的做法（2026-10-02 真实节点验收时改）**：上面「装了 GPFS 的空白盘一律 `unknown_member`」在集群建好之后会让成员节点上的**任何新盘都加不进集群**（加盘只收 `free` / `dirty`），所以按两条规则判：① 有 GPFS 分区类型（`37affc90-ef7d-4e96-91c3-2d7ae055b174`）的分区 → `unknown_member`「GPFS NSD」，不管装没装 GPFS（不再是能勾「擦除」的 `dirty`）；② 装了 GPFS、没有任何签名的盘，**前 4 MiB 不全为零**才判 `unknown_member`（旧格式 NSD 的描述符写在盘头），全零判 `free`。`tspreparedisk -s` 验证过不能用：它只列 `nsddevices` 出口给出的盘，而我们的出口只列本集群认领的盘。配套地，`wipe_disk` 擦完签名后把盘头、盘尾各 10 MiB 清零（下面「释放」一条原本就这么写，代码之前只做了 `wipefs`），这样我们自己释放的盘在 GPFS 节点上重新扫描是 `free`
- **认领时的检查**：盘必须是 `free`（或 `dirty` 且勾选了擦除）；扫描结果不能超过 24 小时（沿用 `hyper_storage.go:37`）；认领时记下盘的序列号、WWN、大小
- **用到盘的那一刻核对身份**：数据库里存的是稳定 ID（by-id 链接名如 `wwn-0x…`；回环设备是 `loop:<后端文件>`，找不到 by-id 时是 `dev:<名>`，`storage_lib.sh:143-163`）。**任何会写盘的脚本（建 NSD、建 OSD、擦盘）都在执行的那一刻、在那台节点上用 `disk_path_of`（`storage_lib.sh:166-181`）重新解析，并核对序列号、WWN、大小与认领时一致**，再复核盘上没有分区、文件系统、LVM、md 签名，没有被挂载，不是系统盘。不一致就失败，不碰盘。脚本只接受「稳定 ID + 期望的序列号」，不接受设备名
- GPFS 的 NSD 描述（stanza）要用内核设备名（`/dev/sdX`），所以由管理节点生成描述之前，`resolve_disks` 步骤在各 NSD 节点上解析并核对，`create_nsd` 声明 `RetryFrom: resolve_disks`（§6.2.5）。另外给每个集群写 `/var/mmfs/etc/nsddevices` 用户出口：**只列出本集群认领的盘**（按稳定 ID 解析）并返回 0，GPFS 永远看不到其他盘，也就不会把 NSD 建到本地池的盘上
- **释放**：从集群里移除盘（§7.5、§8.5）或删除集群后，盘被擦除（`wipefs -a`、清掉开头和结尾各 10 MiB、`sgdisk --zap-all`；同样先核对身份），记录删除，再触发一次扫描。擦除前在界面上列出盘的型号、序列号、大小，要求输入集群名确认

### 6.5 部署前预检

每个部署、加节点任务的第一步都是预检（`stc_precheck.sh`），结果按「通过 / 警告 / 不通过」逐项显示：

| 项目 | GPFS | Ceph | 不通过时 |
|---|---|---|---|
| 操作系统与内核在支持范围内 | 与安装包的发行版目录和支持矩阵比对（矩阵随 CloudLand 一起维护，由各类型后端的 `Requirements` 声明，`api/src/services/storage_backend_<类型>.go`；26.04 一律不通过，§2.2） | Ubuntu 24.04 / 26.04 | 不通过；可勾选「允许不受支持的系统（仅用于测试）」 |
| 运行中内核的头文件可装 | `linux-headers-$(uname -r)` | — | 不通过 |
| 安全启动 | 开着时自己编译的内核模块加载不了（`mokutil --sb-state`） | — | 不通过 |
| 时间同步 | `timedatectl` 显示已同步 | 同左 | 不通过 |
| 节点之间的内网连通 | 1191 端口与 SSH | 3300、6789、6800（代表 OSD 端口段）与 SSH | 不通过 |
| 磁盘 | 认领的盘空闲、身份可核对；同一文件系统内各故障组容量相近 | 同左，且不小于 5 GB | 不通过 / 警告 |
| 根文件系统 | `/var/mmfs` 所在分区至少 10 GB 空闲 | `/var/lib/ceph` 所在分区至少 30% 空闲（mon 节点） | 不通过 |
| 内存余量 | 可调度内存扣掉预留后仍大于 0（§6.7） | 同左 | 警告 |
| 软件包管理器 | dpkg 锁能拿到（等 2 分钟；开机后 unattended-upgrades 常常占着） | 同左 | 不通过 |
| 容器 | — | docker 或 podman 在运行 | 不通过 |
| 主机名 | 在集群内唯一，能解析 | 同左 | 不通过 |
| 已有安装 | 节点上已有 GPFS / Ceph 且没有本集群的成员标记 | 同左 | 不通过（要先清理，或改用导入） |

- **连通性怎么测**：软件装好之前这些端口上没有服务在听。用 TCP 连接测试：对方立刻回「连接被拒绝」说明网络通、只是没人听，算通过；超时说明被防火墙拦了，不通过。不临时起监听
- **成员标记**：加入集群的第一步（预检之后、装软件之前）就在节点上写 `/opt/cloudland/run/storage/<集群UUID>/member`，所以前面的步骤失败后重试或再加节点，不会把半装好的软件误判成外部安装
- 预检同时收集后面步骤要用的信息（SSH 主机公钥、内核版本、内网地址），存进这一步的输出。预检也可以单独发起（`POST /storage_clusters/precheck`，任务种类 `precheck`，不占集群），用来在选节点和磁盘时提前发现问题
- 安装步骤的 `apt-get` 一律带 `-o DPkg::Lock::Timeout=600`

### 6.6 集群的 SSH 密钥

GPFS 的管理命令和 cephadm 都要求能免密以 root 登录其他成员。**不用 CloudLand 自己的 cland 密钥**（那一对是全集群共用的，见待办 A1），每个集群单独一对：

- clapi 生成 ed25519 密钥对，私钥加密入库（§5.10）
- 公钥写进每个成员的 `/root/.ssh/authorized_keys`，**带 `from="<发起登录的节点的内网地址>"` 限制**和 `no-agent-forwarding,no-port-forwarding,no-X11-forwarding`；发起登录的节点变化时编排器重写这一行（行尾注释 `cloudland-storage-<集群UUID>` 用来定位）
- **GPFS**：发起登录的是管理节点，私钥只发给管理节点。预检时每个成员上报自己的 SSH 主机公钥，编排器给管理节点写一份这个集群专用的 `known_hosts`。建集群时用 CloudLand 的包装脚本作为远程命令（`mmcrcluster -r /opt/cloudland/scripts/kvm/storage/gpfs_rsh -R .../gpfs_rcp`），包装脚本给 `ssh` / `scp` 加上这个集群的私钥和 `known_hosts`（`StrictHostKeyChecking=yes`），root 的默认 SSH 配置不受影响。再设 `mmchconfig adminMode=central`，非管理节点之间不需要互相登录
- **Ceph**：cephadm 的 SSH 连接是从**当前活动的 mgr** 发起的（编排模块运行在 mgr 进程里），所以 `from=` 要列出所有 mgr 所在主机的地址，mgr 放置变化时更新。bootstrap 时用 `--ssh-private-key` / `--ssh-public-key` 传入这对密钥，cephadm 把它存在集群的配置库里。两个已知的不足：cephadm bootstrap 自己会往引导主机的 `authorized_keys` 写一行**不带 `from=`** 的公钥（编排器在 bootstrap 之后把那一行改写成带限制的，待验证，V17）；cephadm 不校验被管主机的 SSH 主机公钥，`known_hosts` 对它不起作用

普通成员被攻破，拿不到登录其他节点的能力；管理节点（或 Ceph 的 mgr 主机）被攻破则可以登录这个集群的所有成员（GPFS 和 cephadm 的设计决定的，无法避免）。**另外，A1 的共用 cland 密钥还在，任何节点被攻破本来就能登录所有节点**，所以在 A1 修好之前，每集群独立密钥并不增加实际的隔离，只是不让问题变得更糟，记在 §18。

下发私钥要经过 clapi → cland → cloudlet 的 gRPC，这条链路**没有 TLS**（待办 A6），与 VPN 凭据是同一个问题。

### 6.7 存储进程的内存预留

计算节点上报的可调度内存是「物理内存 − 系统预留（默认 1/4，`report_rc.sh:13-18`）」乘以超分比例再减去云服务器内存。GPFS 和 Ceph 的进程要从里面再扣掉：

| 进程 | 预留（默认） |
|---|---|
| GPFS（任何角色） | pagepool + 1 GiB；pagepool 默认 1 GiB，可在集群参数里调 |
| Ceph mon | 2 GiB |
| Ceph mgr | 1 GiB |
| Ceph OSD | `osd_memory_target` + 0.5 GiB；`osd_memory_target` 在集群参数里设，与云服务器混跑时默认 2 GiB（Ceph 默认 4 GiB） |
| Ceph 客户端（librbd） | 不预留，算在云服务器的 QEMU 进程里 |

- 编排器按节点的角色算出 `storage_cluster_nodes.reserved_mem_mb`，写到节点的 `/opt/cloudland/run/storage_reserved_memory`（多个集群相加，单位 KiB），`report_rc.sh` 读取后从总内存里减掉
- **Ceph 一律关掉 `osd_memory_target_autotune`**：cephadm 默认开着，会按主机内存的七成给 OSD 设置并覆盖全局的 `osd_memory_target`，预留就是错的
- ⚠️ `report_rc.sh:446-447` 有一行「算出的可用内存小于 `MemFree` 就改用 `MemFree`」。存储进程刚启动、还没把内存用起来时 `MemFree` 很大，预留会被这一行抵消，所以预留同样要从 `MemFree` 里减掉

### 6.8 节点上的文件

| 路径 | 内容 |
|---|---|
| `/opt/cloudland/cache/storage-pkg/<sha256>/` | 下载的安装包（§6.1） |
| `/opt/cloudland/run/storage/jobs/<运行ID>/` | 作业目录（§6.2.3），保留 7 天 |
| `/opt/cloudland/log/storage/run-<运行ID>.log` | 步骤日志，保留 90 天 |
| `/opt/cloudland/run/storage/<集群UUID>/` | 集群的运行时状态：成员标记 `member`（§6.5）、`known_hosts`、GPFS 包装脚本用的私钥（权限 600，只在管理节点上）、这个集群的共享池清单 `shared_pools.json`（§9.2，每集群一份，开机时合并读取）、探测状态（§9.2） |
| `/opt/cloudland/run/storage_reserved_memory` | 内存预留（§6.7） |
| `/etc/ceph/<集群UUID>.conf`、`/etc/ceph/<集群UUID>.client.<用户>.keyring` | Ceph 客户端配置（§8.4）。不用默认的 `/etc/ceph/ceph.conf`：一台节点可以是多个 Ceph 集群的客户端，而 cephadm 的管理节点自己要用默认文件。**配置文件里要写 `keyring = <密钥环路径>`**：librados 默认按集群名找 `/etc/ceph/ceph.client.<用户>.keyring`，找不到这个按 UUID 命名的密钥环 |
| `/var/lock/cloudland-storage-<集群UUID>.lock` | 同一集群的作业互斥（§6.2.3） |
| `/opt/cloudland/scripts/kvm/storage/backends/<类型>.sh`、`drivers/<驱动>.sh` | 每种存储的节点钩子、每个存储池驱动的函数（§4.5），随脚本一起分发 |

---

## 7. GPFS 集群

### 7.1 模式

| 模式 | 说明 | 阶段 |
|---|---|---|
| **托管 · 副本**（`managed` + `replica`） | 无共享：每台 NSD 节点的本地盘是它自己的 NSD，每台节点一个故障组（集群内从 1 开始编号，§5.2）。**至少 3 个故障组**（只有 2 台有盘时加一块 `descOnly` 盘作第三组，§6.3）。数据副本默认 2（可选 3），元数据副本在有 3 个以上故障组时为 3；最大副本数一律建成 3，以后可以加副本不必重建文件系统。设 `restripeOnDiskFailure=yes`，盘失效后 GPFS 自动在其余故障组补齐副本 | S2 |
| **外部**（`external`） | 集群由管理员装好，计算节点已经是它的成员并挂好了文件系统；CloudLand 只登记、检查和使用（§7.8） | S2 |
| 托管 · 纠删码（`managed` + `ece`） | ECE 的 `mmvdisk` 恢复组（§7.9） | S7 |

**NSD 节点重启后的盘**：无共享布局下，NSD 节点离线期间它的盘被标成 `down`，节点回来后不会自己恢复，要 `mmchdisk <fs> start`。健康看护（§14.1）发现节点已 `active`、它的盘仍 `down` 时自动执行一次（同一块盘 10 分钟内只试一次），否则滚动重启第二台节点时双副本的数据就不可用了。

### 7.2 部署步骤（任务 `deploy`）

| # | 步骤 | 范围 | 做什么 | 可重入的判断 |
|---|---|---|---|---|
| 1 | `precheck` | 全部成员 | §6.5；输出主机公钥、内网地址、内核版本 | 每次都跑 |
| 2 | `join` | 全部成员 | 写成员标记（§6.5）、`apt-mark hold` 内核元包（§7.7） | 标记已存在 |
| 3 | `fetch_package` | 全部成员 | 按预签名地址下载安装包、校验 SHA-256（§6.1） | 缓存目录里已有且校验通过 |
| 4 | `install` | 全部成员 | 从安装包的 tar 流里解出 `gpfs_debs`，按 `manifest` 的 md5 校验后 `apt-get install`：`gpfs.base`、`gpfs.gpl`、`gpfs.gskit`、`gpfs.msg.en-us`、`gpfs.license.<版本类型>`、`gpfs.docs`（ECE 模式另加 `gpfs.gnr*`）。不装 `gpfs.gui`、`gpfs.scaleapi`、`gpfs.java`、性能采集、协议服务（§1.3）；另装 `libaio1t64`、`ksh`、`iputils-arping`、编译工具与内核头文件。一律用 `apt-get install ./<包>.deb` 装，让 apt 补齐依赖：`gpfs.base` 依赖的 `iputils-arping` 在 24.04 上默认没有，只用 `dpkg -i` 会留下半装的包（V1 实测） | `dpkg -s gpfs.base` 的版本相同 |
| 5 | `build_gpl` | 全部成员 | `mmbuildgpl` | 运行中内核的模块已存在（`/lib/modules/$(uname -r)/extra/mmfs26.ko`） |
| 6 | `ssh_trust` | 全部成员 | 写 `authorized_keys`；管理节点另写私钥、`known_hosts`、`gpfs_rsh` / `gpfs_rcp`（§6.6） | 行已存在且内容相同 |
| 7 | `create_cluster` | 管理节点 | 生成节点描述文件（**一律用内网地址**，即 `hypers.host_ip`），`mmcrcluster -N <文件> -C <集群名> -r gpfs_rsh -R gpfs_rcp -A`；`mmchlicense server --accept -N <仲裁与 NSD 节点>`、`mmchlicense client --accept -N <其余>`；`mmchconfig adminMode=central,autoBuildGPL=yes,restripeOnDiskFailure=yes,pagepool=<值>` | `mmlscluster` 已是同名集群；节点已属于别的集群则失败 |
| 8 | `start` | 管理节点 | `mmstartup -a`，等到 `mmgetstate -a` 全部 `active`（最长 10 分钟） | 已全部 active |
| 9 | `resolve_disks` | NSD 节点 | 按稳定 ID 解析认领的盘并核对身份（§6.4）；写 `/var/mmfs/etc/nsddevices`，只列出本集群认领的盘 | 每次都跑 |
| 10 | `create_nsd` | 管理节点 | 按上一步的输出生成 NSD 描述（`device=/dev/<名>`、`nsd=cl<集群ID>h<hostid>d<序号>`、`servers=<内网地址>`、`failureGroup=<故障组号>`、`usage`、`pool`），`mmcrnsd -F`。`RetryFrom: resolve_disks` | `mmlsnsd` 里已有同名 NSD |
| 11 | `create_fs` | 管理节点 | `mmcrfs <名> -F <描述> -B <块大小> -m <M> -M 3 -r <R> -R 3 -T /gpfs/<名> -A yes -Q yes --inode-limit <值>`；有多种介质时装默认放置规则（§7.3）；`mmmount <名> -a`，等到所有成员都挂上（`mmlsmount -L`） | `mmlsfs <名>` 已存在 |
| 12 | `finish` | 全部成员 | 装集群监控的采集脚本（附录 E）、写内存预留，然后触发一次磁盘扫描 | 每次都跑 |

参数（向导里填，存在 `storage_clusters.params`）：集群名、第一个文件系统的名称（默认 `fs1`）、块大小（默认 4 MiB，可选 1 / 2 / 4 / 8 / 16 MiB；与 qcow2 的 2 MiB 簇如何搭配待验证，V15）、数据副本数（2 / 3）、pagepool（默认 1 GiB）、inode 上限、是否单节点测试形态。

挂载点固定为 `/gpfs/<文件系统名>`：所有节点相同，不在 `/opt/cloudland` 下面（池的根目录是挂载点下的 fileset 入口，§7.4）。

### 7.3 文件系统

- **介质**：认领磁盘时按介质分进文件系统里的 GPFS 存储池：只有一种介质时全部进 `system`（数据和元数据）；有 SSD 或 NVMe 也有 HDD 时，SSD / NVMe 进 `system`（数据和元数据），HDD 进 `data`（只放数据），并装一条默认放置规则 `RULE 'default' SET POOL 'data'`。CloudLand 存储池选介质时就是选 GPFS 存储池（§7.4，`storage_pools.driver_params` 的 `gpfs_pool`）
- **再建一个文件系统**：任务 `create_fs`，用空闲的 NSD 或新认领的盘（`resolve_disks` → `create_nsd` → `create_fs`）
- **删除文件系统**：上面没有 CloudLand 存储池时才能删（任务 `delete_fs`），`mmumount <名> -a` → `mmdelfs <名>`，NSD 变回空闲（可以再删 NSD、擦盘）
- **容量**：健康看护每分钟取一次 `mmdf <名> -Y`（§14.1），既有整个文件系统的、也有每个 GPFS 存储池的，写 `storage_filesystems` 与存储池的容量（口径见 §9.5）

### 7.4 CloudLand 存储池（独立 fileset）

托管集群在集群详情页的「存储池」标签里建（任务 `create_pool`，占池槽，管理节点执行）。池记录先以 `creating` 状态建出来（fileset 名要用池的 UUID），任务成功后变 `active`，失败变 `error`（可以删除重建）：

1. `mmcrfileset <fs> <fileset> --inode-space new --inode-limit <值>`，fileset 名由 CloudLand 生成（`cl_<池UUID 前 8 位>`）
2. `mmlinkfileset <fs> <fileset> -J /gpfs/<fs>/<fileset>`，这就是池的根目录（`storage_pools.mount_path`）
3. **放置规则由 CloudLand 整体生成**：每个池一条 `RULE '<fileset>' SET POOL '<system|data>' FOR FILESET ('<fileset>')`，最后一条默认规则；`mmchpolicy <fs> <文件> -I test` 校验通过后再正式装入，`storage_filesystems.policy_gen` 加一。托管文件系统上**不要手工改放置规则**，CloudLand 下次改池时会整体覆盖（界面提示）。不用 `LIMIT` 做溢出（SSD 满了会悄悄写到 HDD，用户选的和实际拿到的不一致，空间不足应该由准入拒绝，§9.5），不配迁移规则（GPFS 按整个文件分层，运行中的云服务器磁盘一直在读写，按访问时间迁移没有意义，还会产生大量 I/O）
4. 设了配额就 `mmsetquota <fs>:<fileset> --block <配额>:<配额>`
5. 建池内目录 `volumes/`、`images/`、`nvram/`、`tmp/`，写标记文件 `.cloudland-pool`（`driver=gpfs`、`pool_uuid=<UUID>` 两行，§9.2）
6. 对集群全部成员做一轮可用性检查（§9.2），写 `hyper_storage_pools`

- **改配额**：`PATCH /storage_pools/:id` 带 `quota_gb`（任务 `update_pool`），下发 `mmsetquota`
- **删池**（任务 `delete_pool`）：池里没有卷时才能删。引用数为 0 的镜像基础副本随池一起删除，不挡删池（懒导入留下的副本，否则池永远删不掉）。`mmunlinkfileset` → `mmdelfileset -f` → 重新生成放置规则
- **外部集群**：按原设计登记已有的 fileset 入口目录（填路径、文件系统名、fileset 名），CloudLand 只建池内目录和标记文件，不建 fileset、不改放置规则和配额

### 7.5 加节点、加盘、移除

| 操作 | 任务 | 步骤 | 前提 |
|---|---|---|---|
| 加节点 | `add_nodes` | 预检 → 加入（成员标记）→ 下载 → 安装 → 编译 → SSH 信任（管理节点的 `known_hosts` 加新主机）→ `mmaddnode -N` → `mmchlicense` → `mmstartup -N` → `mmmount all -N` → 完成；带盘的接着做「加盘」 | 节点系统在支持范围内、不在别的 GPFS 集群里 |
| 加盘 | `add_disks` | `resolve_disks` → `mmcrnsd` → `mmadddisk <fs> -F <描述>`（不带 `-r`） | — |
| 重新均衡 | `rebalance` | `mmrestripefs <fs> -b`，I/O 很重、可能跑几小时，单独发起；进度由轮询读它的输出 | 建议在业务低峰 |
| 移除盘 | `remove_disk` | `mmdeldisk <fs> <NSD>`（把数据迁走，可能很久）→ `mmdelnsd` → 擦盘（核对身份） | 剩余容量够放下已用数据；剩余故障组数不少于副本数且不少于 3 |
| 移除节点 | `remove_node` | 有盘先「移除盘」；仲裁节点先 `mmchnode --nonquorum`；`mmumount all -N` → `mmshutdown -N` → `mmdelnode -N` → 删 `authorized_keys` 那一行与成员标记；可选卸载软件包 | 这台节点上没有使用本集群存储池的云服务器（先迁走）；移除后仲裁节点仍是奇数且不少于 3（或明确降到 1 台的测试形态） |
| 离线移除 | `remove_node`（带 `offline`） | 节点已经永久坏了：它的 NSD `mmdeldisk <fs> <NSD> -p`（盘不可读时）→ `mmdelnsd -p` → `mmdelnode -N <节点>`（节点不可达时 GPFS 会提示，具体选项待验证，V21）。不在该节点上执行任何命令 | 节点离线超过 30 分钟；要求输入节点名确认 |
| 换坏盘 | `replace_disk` | 坏盘已经 `down`：`mmdeldisk <fs> <NSD> -p` 再加新盘 | — |

### 7.6 删除集群

前提：集群上没有 CloudLand 存储池。步骤：`mmumount all -a` → 每个文件系统 `mmdelfs` → `mmdelnsd` 全部 → `mmshutdown -a` → `mmdelnode -a` → 每台节点：删 `authorized_keys` 那一行、成员标记和集群目录、按确认擦盘（核对身份）、可选卸载软件包、内存预留清零、触发磁盘扫描。删除前要求输入集群名，并列出将被擦除的盘。

**半成品与离线节点**：删除任务从「集群现在实际是什么状态」出发，每一步都可重入（文件系统不存在就跳过 `mmdelfs`，节点不在集群里就跳过 `mmdelnode`），所以部署任何阶段失败、中止后都能用它清理。有节点离线时，集群级命令照常在在线的管理节点上做，离线节点用 §7.5 的离线移除；节点本地的清理（密钥、标记、擦盘）记为「待清理」，节点回来后由健康看护补做，删除任务本身不卡住。

### 7.7 升级与内核

- **GPFS 升级**（阶段 S6）：逐台进行。一台节点停 GPFS 之前，上面使用本集群存储池的云服务器要先迁走（全部磁盘在共享池上的不复制磁盘，很快；有本地盘的照常复制），复用现有维护模式的批量迁移。每台：迁走 → `mmshutdown -N` → 装新版本 → `mmbuildgpl` → `mmstartup -N` → 挂载 → 解除维护。全部完成后再由管理员确认执行 `mmchconfig release=LATEST` 和 `mmchfs <fs> -V full`（不可逆，单独一步）
- **内核**：`autoBuildGPL=yes` 让 GPFS 启动时发现没有对应模块就自动编译。但新内核必须在 IBM 的支持范围内，而 Ubuntu 的安全更新会带来新内核。**加入集群时对 GPFS 节点 `apt-mark hold` 内核元包**（`linux-image-generic`、`linux-headers-generic`），管理员查过支持矩阵后再手工升级；节点详情页在运行中的内核不在支持表里时显示警告。这样会让内核安全更新变成手工操作，是否接受列在 §18 决策 D5

### 7.8 外部 GPFS 集群

- **导入**（任务 `import`）：填集群名、文件系统名和挂载点、要使用它的节点（或可用区）。CloudLand 对这些节点下发检查（`mmfsd` 在运行、文件系统挂在登记的路径上、`stat -f` 类型是 `gpfs`），把通过的节点记为 `client`，状态随每轮检查更新
- **存储池**：按 §7.4 末尾的方式登记已有 fileset
- **健康与容量**：由一台在线的成员节点执行（没有管理节点）：挂载状态和 `df` 的容量；fileset 配额能读到时用 `mmlsquota -j`（非管理节点上能否执行，待验证，V3）
- CloudLand 不在外部集群上执行任何管理命令
- **删除**（取消登记）：池都删了才能删；只清理 CloudLand 在节点上写的东西（共享池清单、探测进程），不碰 GPFS 本身

### 7.9 纠删码模式（阶段 S7）

只在硬件满足 §2.3 时做。流程换成 `mmvdisk`：`mmvdisk nodeclass create` → `mmvdisk server configure` → `mmvdisk server recycle` → `mmvdisk recoverygroup create` → `mmvdisk vdiskset define --code 4+2p|8+3p|...` → `mmvdisk vdiskset create` → `mmvdisk filesystem create`。预检加上 IBM 提供的 ECE 就绪检查工具（操作系统、网络、存储三项）。测试环境做不了，不展开。

---

## 8. Ceph 集群

### 8.1 版本与镜像

- 节点上的 `cephadm`、`ceph-common` 来自发行版源（26.04 是 20.2.0；24.04 是 19.2 系列）。**一个集群只用一个版本**：部署时以管理节点上 `cephadm` 的版本为准，其他成员装同一主版本；24.04 与 26.04 混用、发行版源里没有同一版本时，用 Ceph 官方的软件源（`download.ceph.com`）装 `cephadm`、`ceph-common`，版本由集群参数固定
- 容器镜像默认 `quay.io/ceph/ceph:v<集群版本>`，可以改成私有仓库地址（私有化环境），仓库口令加密保存（§5.1）。Ubuntu 打包的 `cephadm` 与上游镜像能否直接搭配，待验证（V10）
- 节点上已有 docker，不另装 podman（cephadm 两者都支持）

### 8.2 部署步骤（任务 `deploy`）

| # | 步骤 | 范围 | 做什么 | 可重入的判断 |
|---|---|---|---|---|
| 1 | `precheck` | 全部成员 | §6.5 | 每次都跑 |
| 2 | `join` | 全部成员 | 写成员标记（§6.5） | 标记已存在 |
| 3 | `install` | 全部成员 | `apt-get install cephadm ceph-common lvm2`，版本固定 | 已装且版本相同 |
| 4 | `pull_image` | 全部成员 | `cephadm --image <镜像> pull`（有私有仓库时先 `cephadm registry-login`） | 本地已有该镜像 |
| 5 | `ssh_trust` | 全部成员 | 写 `authorized_keys`（`from=` 为 mgr 所在主机，§6.6）。放在 bootstrap 之前：bootstrap 自己要 SSH 到本机 | 行已存在且内容相同 |
| 6 | `bootstrap` | 第一台 `admin`（必须是 mon） | `cephadm --image <镜像> bootstrap --fsid <clapi 生成> --mon-ip <内网地址> [--cluster-network <网段>] --ssh-private-key <f> --ssh-public-key <f> --ssh-user root --skip-monitoring-stack --skip-dashboard --skip-firewalld`；单节点测试形态加 `--single-host-defaults`；之后把 bootstrap 自己写进 `authorized_keys` 的那一行改成带 `from=` 的（§6.6） | `/var/lib/ceph/<fsid>` 已存在且 `ceph -s` 能连上 |
| 7 | `add_hosts` | 管理节点 | `ceph orch host add <主机名> <内网地址> --labels <角色>` | `ceph orch host ls` 里已有 |
| 8 | `place_daemons` | 管理节点 | `ceph orch apply mon --placement=label:mon`、`ceph orch apply mgr --placement=label:mgr`；给 `admin` 加 `_admin` 标签；**`ceph orch apply osd --all-available-devices --unmanaged=true`**（关掉「自动占用所有空闲盘」，否则 cephadm 会把节点上的空闲盘全部吃掉，包括准备给本地池的）；等 mon 全部进入法定人数 | 放置已一致 |
| 9 | `configure` | 管理节点 | `public_network`、`cluster_network`、`osd_memory_target`、**`osd_memory_target_autotune=false`**（§6.7）、`osd_pool_default_size`、`mon_allow_pool_delete=false`、`auth_allow_insecure_global_id_reclaim=false`；启用 mgr 的 `prometheus` 模块并绑内网地址；`ceph auth get-or-create client.cloudland mon 'profile rbd'`（先不给任何池的权限，建池时加），输出密钥，clapi 加密存进 `client_key` | 每次都写（幂等） |
| 10 | `resolve_disks` | OSD 节点 | 同 §7.2（核对身份）；测试环境的回环设备先建成 LVM 逻辑卷（ceph-volume 不收回环设备） | 每次都跑 |
| 11 | `create_osds` | 管理节点 | 每块盘 `ceph orch daemon add osd <主机>:<设备>`，等 OSD `up`；介质与 Ceph 自动识别的设备类不同时 `ceph osd crush set-device-class` 改成认领时的介质；输出 OSD 编号。`RetryFrom: resolve_disks` | 该设备上已有 OSD（`ceph-volume lvm list` / `ceph osd tree`） |
| 12 | `client_setup` | 全部成员与客户端 | 见 §8.4 | 文件与 secret 已存在且一致 |
| 13 | `finish` | 全部成员 | 写内存预留；触发磁盘扫描。任务成功后由 clapi 把 mgr 的指标地址写进 Prometheus 的目标文件（§14.2，节点写不到控制面的文件） | 每次都跑 |

参数：集群名、镜像、公共网段（默认取成员内网地址所在网段）、复制网段（可选）、副本数（3；单节点测试形态为 1）、`osd_memory_target`（默认 2 GiB）。

### 8.3 CloudLand 存储池（RBD 池）

托管集群在集群详情页上建（任务 `create_pool`，占池槽，管理节点执行；池记录先以 `creating` 建出来，同 §7.4）：

1. 按介质准备 CRUSH 规则：`ceph osd crush rule create-replicated cl-<介质> default host <介质>`（已存在就跳过）；该介质的 OSD 所在主机数少于副本数时，接口直接拒绝
2. `ceph osd pool create <池名>`，`pg_autoscale_mode on`，`size 3`、`min_size 2`（单节点测试形态 `size 1`、`min_size 1`），`crush_rule cl-<介质>`；池名由 CloudLand 生成（`cl_<池UUID 前 8 位>`）
3. `ceph osd pool application enable <池名> rbd`、`rbd pool init <池名>`
4. 设了配额就 `ceph osd pool set-quota <池名> max_bytes <配额>`
5. 写标记对象：`rados -p <池名> put cloudland-pool <内容为池 UUID 的文件>`（§9.2 用它确认连的是对的池）
6. 更新客户端的权限，把新池加进去（`ceph auth caps client.<用户> mon 'profile rbd' osd 'profile rbd pool=<池1>, profile rbd pool=<池2>, ...' mgr 'profile rbd pool=...'`）
7. 对全部客户端做一轮可用性检查

- **删池**（任务 `delete_pool`）：没有卷时：引用数为 0 的镜像基础副本一起删除（`rbd snap unprotect` / `snap purge` / `rm`），临时 `mon_allow_pool_delete=true` → `ceph osd pool rm <池名> <池名> --yes-i-really-really-mean-it` → 改回 `false` → 从客户端权限里去掉
- **外部集群**：登记已有的池名，CloudLand 只写标记对象（客户端密钥要有写权限）

### 8.4 客户端

要使用 Ceph 存储池的每台计算节点（包括 OSD、mon 所在的节点）都要配置客户端（任务步骤 `client_setup`，也可以单独对新节点执行 `add_clients`）：

- `apt-get install ceph-common`；QEMU 的 RBD 驱动 `qemu-block-extra` 与 `librbd1` 两个系统版本都显式安装（26.04 节点上现在有它，是被 `qemu-system-x86` 的推荐依赖带进来的，不能依赖这一点）
- `/etc/ceph/<集群UUID>.conf`：`ceph config generate-minimal-conf` 的输出（fsid 与 mon 地址），**另加一行 `keyring = /etc/ceph/<集群UUID>.client.<用户>.keyring`**（§6.8）
- `/etc/ceph/<集群UUID>.client.<用户>.keyring`：权限 600，节点脚本以 root 执行 `rbd` 时用
- libvirt secret：所有节点用**同一个 UUID**（`storage_clusters.attrs` 的 `secret_uuid`，热迁移时目标节点才认得），`virsh secret-define` 后用 `virsh secret-set-value --file` 写入密钥（不用把密钥放在命令行参数里，避免出现在进程列表里）
- 移除客户端（任务 `remove_clients`）：节点上没有使用这个集群的云服务器时，删配置文件、密钥环和 libvirt secret

### 8.5 加主机、加盘、移除

| 操作 | 步骤 | 前提 |
|---|---|---|
| 加主机 | 预检 → 加入 → 安装 → 拉镜像 → SSH 信任 → `ceph orch host add` → 按角色放置 → 客户端配置；带盘的接着加 OSD | 这台主机没有为别的 Ceph 集群运行守护进程（§4.1） |
| 加盘 | `resolve_disks` → `ceph orch daemon add osd`；之后 Ceph 自动把一部分数据迁到新盘（见表后），不需要另发起重新均衡 | — |
| 移除 OSD | `ceph orch osd rm <编号> --zap`，等数据迁完（`ceph orch osd rm status` 轮询进度）→ 盘已被 `--zap` 清掉，再触发扫描 | `ceph osd ok-to-stop`；剩余 OSD 主机数不少于各池副本数；剩余容量够 |
| 换坏盘 | `ceph orch osd rm <编号> --replace`（保留编号）→ 新盘用同一编号加入 | — |
| 移除主机 | `ceph orch host drain <主机>`（迁走全部守护进程，包括 OSD）→ 等完成 → `ceph orch host rm <主机>` → 删客户端配置与 `authorized_keys` 那一行 | 移除后 mon 仍是奇数且不少于 3（先缩 mon）；主机上没有使用本集群存储池的云服务器（先迁走） |
| 离线移除主机 | 主机已经永久坏了：`ceph orch host rm <主机> --offline --force`，它的 OSD `ceph osd purge <编号> --yes-i-really-mean-it`，等数据在其余 OSD 上恢复 | 主机离线超过 30 分钟；要求输入主机名确认 |

**数据的重新分布与可用性**（Ceph 自身的行为，决定了上面各项操作和节点故障时的预期）：

- **加盘、移除盘之后，Ceph 自动重新分布数据**：OSD 加入或移出后，数据分布图（CRUSH）跟着变，一部分数据（PG）自动迁到新位置。所以 Ceph 没有 GPFS 那种要单独发起的「重新均衡」（§7.5），也不提供这个操作。迁移流量和云服务器的读写共用网络与磁盘，优先级由 OSD 的 mClock 调度决定（`osd_mclock_profile`，业务优先时设 `high_client_ops`）
- **3 副本（`size 3`、`min_size 2`）时的可用性**：一台节点离线，它上面的数据在别处还有两份，读写照常，集群显示降级；同时离线两台，有些数据只剩一份、低于 `min_size`，这部分数据的读写会卡住，直到有节点回来
- **只有 3 台 OSD 节点时，坏一台补不回副本**：分布规则按节点放副本（§8.3），没有第 4 台节点可以放第三份，集群一直降级，直到节点回来，或者离线移除后加上新节点；这期间再坏一台就卡住。节点有 4 台以上时，一台离线超过 10 分钟（`mon_osd_down_out_interval`，默认 600 秒）后，它的 OSD 被标记为移出，Ceph 在其余节点上补齐副本。正式使用建议 OSD 节点不少于 4 台
- **mon 的法定人数**：3 台 mon 允许 1 台不在，同时少 2 台集群就失去法定人数、停止服务；5 台 mon 允许 2 台不在
- **计划内维护**：单台节点重启（10 分钟以内）不用额外操作；要停更久时，先在管理节点执行 `ceph orch host maintenance enter <主机>`（停掉这台主机上的守护进程，并让它的 OSD 不被标记为移出，免得白白迁移一遍数据），完成后 `ceph orch host maintenance exit <主机>`（附录 F.4）

### 8.6 删除集群

前提：集群上没有 CloudLand 存储池。先在管理节点上 `ceph mgr module disable cephadm`（否则还活着的 mgr 会把守护进程重新部署到正在清理的主机上），再在每台主机执行 `cephadm rm-cluster --fsid <fsid> --zap-osds --force`，删客户端配置、libvirt secret、`authorized_keys` 那一行和成员标记，可选卸载软件包，内存预留清零，触发磁盘扫描。与 §7.6 相同：每一步可重入，能清理任何阶段的半成品；离线主机的本地清理等它回来后补做。

### 8.7 升级

`ceph orch upgrade start --image <新镜像>` 逐个守护进程滚动升级，`ceph orch upgrade status` 轮询进度（阶段 S6）。云服务器用的是节点上的 `librbd1`（QEMU 进程启动时加载），升级节点软件包后要重启或迁移云服务器才生效；Ceph 兼容旧客户端，不急。

### 8.8 外部 Ceph 集群

- **导入**（任务 `import`）：填 fsid、mon 地址、客户端用户名与密钥（密钥加密保存，§5.1）、要使用它的节点（或可用区）。客户端的权限要求：`mon 'profile rbd'`、`osd 'profile rbd pool=<要用的池>'`。CloudLand 对这些节点执行客户端配置（§8.4）
- **存储池**：登记已有的池名（§8.3 末尾）
- **健康**：由一台客户端节点用客户端身份执行 `ceph health -f json`（`profile rbd` 的 mon 权限能否执行，待验证，V14）
- `profile rbd` 本身包含把失联客户端加入黑名单的权限（librbd 打破排他锁时就靠它），所以外部集群同样支持节点宕机后的隔离（§11；能否执行 `blocklist range`，待验证，V14）
- **删除**（取消登记）：池都删了才能删；清理各节点上的客户端配置、密钥环与 libvirt secret，不碰 Ceph 本身

---

## 9. 存储池与数据通路

本章是原设计 §3、§5、§6 按两种驱动的重写。下文按驱动分开写的做法，在代码里一律落到驱动接口（§4.5.2）：clapi 经 `PoolDriver` 取驱动相关的信息，节点上每个操作一个通用脚本，引入 `drivers/<驱动>.sh` 后调用固定的函数。原则不变：

1. **存储类型是卷的属性**：卷 → 存储池 → 驱动
2. **clapi 是路径的唯一来源**：建卷记录时确定路径，下发命令时给出驱动、绝对路径（或 RBD 的池名与镜像名）和池参数，脚本不自己拼
3. **按单块盘处理**：同一台云服务器可以同时有本地盘、GPFS 盘、RBD 盘，涉及磁盘的脚本从 clapi 下发的磁盘清单里读每块盘的驱动
4. **共享盘永远不按路径模式删除**：只删 clapi 点名的那一个，并且要在确认云服务器已销毁之后（§12.3）

### 9.1 驱动与路径

| | 内置本地池 | 其他本地池 | GPFS 池 | Ceph RBD 池 |
|---|---|---|---|---|
| 根 | `/opt/cloudland/cache` | `/opt/cloudland/pools/<池UUID>` | `/gpfs/<fs>/<fileset>` | 无，按池名访问 |
| 系统盘 | `instance/inst-<实例ID>.disk` | `volumes/volume-<卷ID>.disk` | `volumes/volume-<卷ID>.disk` | `volume-<卷ID>` |
| 数据盘 | `volume/volume-<卷ID>.disk` | `volumes/volume-<卷ID>.disk` | `volumes/volume-<卷ID>.disk` | `volume-<卷ID>` |
| 格式 | qcow2 | qcow2 | qcow2 | raw |
| 什么时候落盘 | 首次挂载（数据盘） | 首次挂载 | 建卷时 | 建卷时 |
| 「已落盘」怎么判断 | `hyper > 0` | `hyper > 0` | 已离开 `pending`（§5.6） | 同左 |
| 镜像基础 | 节点本地缓存 | 节点本地缓存 | `images/image-<镜像ID>-<前缀>.qcow2` | `image-<镜像ID>-<前缀>@base` |
| UEFI NVRAM | `$image_dir` | `<池>/nvram/` | `<池>/nvram/`（跟着系统盘，迁移、恢复后都在） | 节点本地 `$image_dir`，迁移时照旧复制（§10） |
| 磁盘 XML | `type='file'`，`error_policy='enospace'` | 同左 | `type='file'`，`cache='none' error_policy='stop'` | `type='network'`、`protocol='rbd'`，cephx 走 libvirt secret，`cache='writeback' discard='unmap'` |
| 写入前的保护 | `pool_guard`（xfs、挂载点、hostid） | 同左 | `pool_guard`（文件系统类型 `gpfs`、标记文件） | 标记对象（§9.2） |
| `volumes.hyper` | 文件所在节点 | 文件所在节点 | 0 | 0 |

节点侧的改动：

- `pool_guard`（`scripts/kvm/storage_lib.sh:54-72`）现在只认 xfs、挂载点和带 `driver=local`、`hostid` 的标记文件，它留给本地池用。共享池的检查是驱动的 `drv_guard`（§4.5.2）：文件型的公共实现核对文件系统类型与标记文件（`driver=<驱动>`、`pool_uuid=<UUID>` 两行，不校验 hostid），GPFS 要求 `timeout 10 stat -f -c %T <根>` 等于 `gpfs`（fileset 入口不是挂载点，不能用 `mountpoint`）；RBD 读池的标记对象
- `pool_root`（`storage_lib.sh:20-28`）和 `pool_enter` 把池根写死成 `/opt/cloudland/pools/<UUID>`；GPFS 池的根要从这个集群的共享池清单（`shared_pools.json`，§9.2）读，或者由 clapi 在命令里给出
- 脚本里不对池根目录做 `mkdir -p`，子目录在建池时一次建好（§7.4）

### 9.2 节点可用性

照搬已实施的本地池探测做法（`pool_probe.sh`）：**每个池一个常驻的后台探测进程，心跳只读它写的状态文件**。原因是访问挂起的 GPFS 时进程会卡在不可中断状态，`timeout` 也杀不掉；如果检查是心跳或同步命令里做的，cloudlet 的串行队列和心跳都会被拖死，VPN 切换、负载均衡、中转网关的命令全排在它后面。

- **池清单下发**：clapi 在建池、改池、删池、节点加入集群时，以及每 5 分钟一轮（兜底丢掉的命令），对每个集群的成员和客户端（列表非空才发）下发 `sync_shared_pools.sh <集群UUID>`（同步、很快），stdin 是这个集群的全部池：`[{uuid, driver, root | ceph_pool, conf, user, fs_type}]`。节点存成 `/opt/cloudland/run/storage/<集群UUID>/shared_pools.json`（每集群一份，开机和待启动列表合并读取，§9.9），并确保每个池的探测进程在跑、清单里没有的池的探测进程停掉
- **探测**（`shared_pool_probe.sh <集群UUID> <池UUID>`，每 30 秒一次，结果写 `run/storage/<集群UUID>/probe-<池UUID>`）：
  - 探测进程调用驱动的 `drv_probe`（§4.5.2）：
  - 文件型（GPFS）：`drv_guard`，再在 `tmp/` 里建一个临时文件然后删掉，验证可写；同时读 `df` 的容量
  - Ceph：`timeout 15 rados --conf <conf> --id <用户> -p <池> get cloudland-pool -`，内容等于池 UUID（连错集群、池被删除重建都能发现）
  - 判活看 `/proc/<pid>/cmdline`（沿用 `probe_alive`，pid 文件跨重启保留，不能只 `kill -0`）
- **上报**：心跳的 `report_rc.sh` 读各探测状态，结果变化或距上次上报满 5 分钟时回调 `shared_pool_status '<NODE_ID>' '<base64 JSON>'`，每个池一项 `ready` / `unavailable` 加原因；状态文件超过 2 分钟没更新报 `unavailable`（原因「探测卡住」，通常就是存储挂起了）
- **过期**：clapi 维护循环把超过 15 分钟没有上报的共享池行置为 `unavailable`。这是一条单独的规则：现有的过期规则要求 `capacity_at` 不为空（`storage_callbacks.go:312-317`），而共享池的行不填这一列
- 不是集群成员或客户端的节点（例如 26.04 节点之于 GPFS 集群）根本没有这一行，等于不可用，调度和挂载时自然排除

### 9.3 Ceph RBD 驱动

下表各操作在节点上由通用脚本（`*_volume_shared.sh` 等）调用 `drivers/ceph_rbd.sh` 的函数完成（§4.5.2）。

**派给哪台节点**：不需要在云服务器所在节点执行的操作（建卷、未挂载时扩容、删卷、导入基础副本），由 clapi 从这个池 `ready` 的节点里挑一台（优先最近上报过的），用 `inter=` 下发，两种驱动共用一个函数（`pickPoolHost`）。不用 cland 的 `select=`：它会按 CPU / 内存挑，可能回 `error=resource`。

| 操作 | 在哪执行 | 做法 |
|---|---|---|
| 建卷 | `pickPoolHost` | `rbd create <池>/volume-<id> --size <MiB> --image-feature layering,exclusive-lock,object-map,fast-diff,deep-flatten`；回调 `create_volume_shared '<id>' 'available\|error' '<原因>'` |
| 挂载 | 云服务器所在节点（该节点对这个池必须可用，否则 400） | `attach_volume_shared.sh`：驱动的 `drv_disk_xml` 生成 network 磁盘 XML（附录 F.1），`virsh attach-device --live --config`；回调不写 `hyper` |
| 卸载 | 同上 | 与本地盘相同，用保存下来的磁盘 XML |
| 扩容 | 已挂载且云服务器开着：所在节点 `virsh blockresize`（QEMU 会调用 RBD 的扩容）；否则 `pickPoolHost` 执行 `rbd resize` | 失败时按实际大小回滚，沿用 `rpcs/resize_volume.go` |
| 删卷 | `pickPoolHost` | `rbd rm`。镜像还有监听者（有 QEMU 开着它）时 Ceph 拒绝删除，脚本把 `rbd status` 列出的监听者写进失败原因。这只是额外的检查：网络分区时监听会在约 30 秒后超时消失，真正的保证是 §12.3 的删除顺序 |
| 系统盘 | 云服务器将要启动的节点 | 基础副本已 `synced` 后 `rbd clone <池>/image-<id>-<前缀>@base <池>/volume-<id>`，再 `rbd resize` 到规格大小；`clone_mode=copy` 时 `rbd deep cp` |
| 捕获镜像 | 云服务器所在节点 | `qemu-img convert -f raw -O qcow2 'rbd:<池>/volume-<id>:id=<用户>:conf=<conf>' <临时文件>`，之后沿用现有的上传流程 |
| 救援 | 云服务器所在节点 | 救援盘照旧在本地；原系统盘用它自己的 network XML 挂成 `vdb` |

磁盘 XML 里的用户名用集群 `attrs` 里的 `client_user`（外部集群不一定叫 `cloudland`）。mon 地址：写死在 XML 里的话，mon 换了地址后，已运行的云服务器不受影响（librbd 会从集群学到新的 mon 表），但持久化的域定义里还是旧地址，下次启动可能连不上。优先用 libvirt 的 `<config file='/etc/ceph/<集群UUID>.conf'/>` 引用配置文件（libvirt 12 是否支持 RBD 磁盘的这个写法，待验证，V12）；不支持时，mon 变更后由编排器刷新所有相关域定义里的地址。

### 9.4 GPFS 驱动

GPFS 是文件型：下表的大部分操作是 `drivers/file.sh` 的公共实现，`drivers/gpfs.sh` 只覆盖 `drv_guard`、`drv_import_image`、`drv_clone`（§4.5.2）。

| 操作 | 在哪执行 | 做法 |
|---|---|---|
| 建卷 | `pickPoolHost` | `drv_guard`，`qemu-img create -f qcow2 -o cluster_size=2M <绝对路径> <大小>`（目标已存在则失败，不覆盖）；回调 `create_volume_shared`。建卷时就落盘：不依赖任何节点，还能尽早发现池不可用或空间不足 |
| 挂载 | 云服务器所在节点（必须可用） | `attach_volume_shared.sh`：`drv_guard`，磁盘 XML 用 `cache='none' error_policy='stop'`；回调不写 `hyper` |
| 卸载 | 同上 | 与本地盘相同 |
| 扩容 | 开着：`virsh blockresize`；关着或未挂载：`pickPoolHost` 执行 `qemu-img resize` | 与本地盘相同，只是路径由 clapi 下发 |
| 删卷 | `pickPoolHost` | `drv_guard`，只删 `volumes/` 下文件名与卷 ID 对应的那个文件；路径用 `realpath -m` 规范化后必须在池根内（沿用 `pool_enter` 的做法） |
| 系统盘 | 将要启动的节点 | 基础副本已 `synced` 后 `mmclone copy <基础副本> <系统盘>`，失败退回整盘复制（回调里报告实际用了哪种，§9.7）；`clone_mode=copy` 时 `qemu-img convert`；再 `qemu-img resize`；NVRAM 从模板复制到池里 |
| 捕获镜像、救援 | 云服务器所在节点 | 与本地盘相同，路径由 clapi 下发 |

`mm*` 命令默认不在 `PATH` 里，脚本一律写全路径 `/usr/lpp/mmfs/bin/`。

### 9.5 容量与准入

**容量口径**（健康看护每分钟采集，§14.1）：

| | 设了配额 | 没设配额 |
|---|---|---|
| GPFS 池 | fileset 配额（`mmlsquota -j`） | 池所在 **GPFS 存储池**（`system` / `data`）的容量（`mmdf -Y` 按存储池的部分），不是整个文件系统 |
| Ceph 池 | 池配额 | `stored + max_avail` |

**同一个 GPFS 存储池（或同一个 Ceph CRUSH 规则下）有几个不设配额的 CloudLand 池时，它们共享同一份容量**：准入时这几个池的已分配量合并计算，不能各自按整份容量 × `over_ratio` 分配。界面建池时建议设配额。

**准入**：建卷、建云服务器（系统盘）、扩容时，在事务里 `FOR UPDATE` 锁 `storage_pools` 那一行（共享容量时锁同组的所有池行，按 ID 排序加锁）：

- 已分配（这些池里所有卷的 `size` 之和）加本次新增，不超过 `容量 × over_ratio`
- 实际用量低于容量的 90%
- **容量未知时拒绝**（与现有的本地池一致，`storage_admission.go:142-147`）。Ceph 写满会阻塞整个集群，不能放过

现有的准入（`services/storage_admission.go:57-165`）按「机器 × 池」锁 `hyper_storage_pools` 的行、要求每台机器都有一行，这是本地池的口径；共享池改走上面这条池级的分支。

**调度**：系统盘放共享池时，候选节点 = 可用区内的活动节点 ∩ 该池可用的节点，交给 cland 的 `select=` 挑（CPU、内存照常检查；磁盘维度为 0，因为调度器的磁盘需求只算内置池，`services/instance_placement.go` 的 `schedulerResources`）。现在非内置池走的是 clapi 自选节点加预留（`services/instance.go:304-317` 调用 `instance_placement.go:210` 的 `bootHost`），那是给按机器计容量的本地池用的，共享池不需要。**放置组的路径**（`instance_placement.go:98-180` 的 `startCreationPlacement`）同样对非内置池按「机器 × 池」做准入，共享池要改成池级准入，候选节点同样与「该池可用的节点」取交集。指定了宿主机时，该节点必须在候选集合里。

**写满时的表现不同**，这一点要在界面和文档里说清楚：

| | GPFS | Ceph |
|---|---|---|
| 到配额 | 写操作返回 `EDQUOT`。QEMU 默认只对 `ENOSPC` 暂停云服务器，所以 GPFS 盘用 `error_policy='stop'`，出错就暂停，不把 I/O 错误交给客户机（是否生效待验证，V8） | 池被标成「配额已满」，**写操作阻塞**（librbd 一直等），云服务器表现为 I/O 卡住，而不是暂停 |
| 整体写满 | 文件系统满，同上 | 集群到 `full_ratio`（默认 95%）后**全集群所有池的写都阻塞**；85% 起是 `nearfull` |

Ceph 写满的后果更重，所以 Ceph 池默认 `over_ratio=1`（不超分），`nearfull` 与池用量 80% / 90% 都告警（§14）。本地池的「写满后自动暂停、空间回落后自动恢复」（本地方案 §6.1）只针对本地盘，共享盘导致的暂停不自动恢复。

### 9.6 镜像基础副本

**由 clapi 串行导入**：第一次在某个共享池里用某个镜像建云服务器时，clapi 建 `image_storages` 记录（`syncing`），用 `pickPoolHost` 挑一台节点，以后台作业导入（`import_image_shared.sh` 调用驱动的 `drv_import_image`，经 `async_exec`，不占串行队列），回调 `image_storage_status '<id>' 'synced|error' '<原因>'` 后，才下发创建云服务器的命令；同一镜像同一池同时只有一个导入，其余创建请求排在后面（实例停在 `pending`，回调到了再下发）。

不在 `launch_vm.sh` 里导入：它是同步执行的，导入 20 GB 镜像（Ceph 要写三份）会占住这台节点的串行队列好几分钟；几台节点同时抢着导入还会成倍增加 I/O。

导入的做法（节点上）：

- **GPFS**：本地缓存镜像（`ensure_image_cached`）→ `qemu-img convert -O qcow2` 到 `tmp/image-<镜像ID>-<作业>.qcow2` → `clone_mode=clone` 时 `mmclone snap`（变成只读的克隆父文件）→ `mv` 到正式路径（目标已存在则删掉自己的临时文件、视为成功）
- **Ceph**：本地缓存镜像 → `qemu-img convert -O raw` 到 `rbd:<池>/tmp-image-<镜像ID>-<作业>` → `rbd snap create ...@base` → `rbd snap protect`（克隆格式 v2 可用时不需要）→ `rbd rename` 到正式名（目标已存在则删掉自己的临时镜像）
- 作业中断留下的临时文件 / 临时 RBD 镜像由下一次导入（同一池）先清理：`tmp-image-*` 中没有对应进行中作业的一律删除（带受保护快照的先 `unprotect`、`snap purge`）

可选预热：`PATCH /images/:id` 带 `storage_pools: [...]`，提前导入。

**删除**：删除镜像时，对每个共享池里的基础副本统计引用数（`volumes.base_image_storage_id` 指向它且没删的卷）。为 0 就由 `pickPoolHost` 删除并删记录；不为 0 就置 `deleting`，之后删除或重装云服务器时再检查，归零再删。GPFS 是否允许删除还有子克隆的父文件不确定，Ceph 明确不允许（要先 `rbd flatten` 子镜像），引用计数对两者都适用。删池时引用数为 0 的副本随池删除（§7.4、§8.3）。

### 9.7 创建云服务器（系统盘在共享池）

clapi（`services/instance.go` 的 `Create`）：

1. 确定系统盘的池（请求里的 `storage_pool` 已有，没给就用默认池），池必须 `active` 并通过准入（§9.5）
2. 候选节点按 §9.5 过滤
3. 系统盘记录：`storage_pool_id`、路径（§9.1）、状态 `pending`
4. 基础副本：`image_storages(镜像, 池)` 不是 `synced` 时先按 §9.6 导入，导入完成后再继续下面一步（`clone_mode=copy` 的池也一样，节点从池内的基础副本整盘复制，不再各自下载）
5. 元数据（stdin 里的 JSON）的 `boot_disk` 带上驱动相关的全部参数，脚本不再推算：

```json
"boot_disk": {
  "volume_id": 12,
  "driver": "ceph_rbd",
  "pool": "<pool uuid>",
  "ceph_pool": "cl_1a2b3c4d",
  "image": "volume-12",
  "conf": "/etc/ceph/<cluster uuid>.conf",
  "user": "cloudland",
  "secret_uuid": "<libvirt secret uuid>",
  "size_gb": 40,
  "image_base": "image-3-ab12cd34@base",
  "image_storage_id": 7,
  "clone_mode": "clone",
  "existing": false
}
```

```json
"boot_disk": {
  "volume_id": 12,
  "driver": "gpfs",
  "pool": "<pool uuid>",
  "pool_root": "/gpfs/fs1/cl_1a2b3c4d",
  "fs_type": "gpfs",
  "path": "/gpfs/fs1/cl_1a2b3c4d/volumes/volume-12.disk",
  "size_gb": 40,
  "image_base": "/gpfs/fs1/cl_1a2b3c4d/images/image-3-ab12cd34.qcow2",
  "image_storage_id": 7,
  "clone_mode": "clone",
  "nvram": "/gpfs/fs1/cl_1a2b3c4d/nvram/inst-9_VARS.fd",
  "existing": false
}
```

本地系统盘同样用这个结构（`driver=local`，没有 `image_base`）。`existing=true` 只用于宕机恢复（§11.3）：跳过克隆和扩容，直接用已有的盘。

节点（`launch_vm.sh`）按驱动：可用性检查（失败就回调 `error`，实例进入 `error`）→ **目标已存在就失败**（`qemu-img convert` 会静默覆盖已存在的文件，不能让它发生；`existing=true` 时反过来要求目标存在）→ 克隆或复制 → 校验镜像虚拟大小不超过规格后扩到规格大小 → NVRAM → 定义并启动域 → 回调。回调 `create_volume_shared` 带上 `{"image_storage_id": 7, "cloned": true}`，clapi 据此写 `base_image_storage_id`（实际整盘复制了就是 0）。**克隆或复制开始之后**任何一步失败，删掉这次生成的系统盘（路径唯一，不会误删）；`existing=true` 时绝不删盘。

### 9.8 重装、救援、捕获、删除云服务器

- **删除云服务器**：`clear_vm.sh` 从 stdin 接收磁盘清单，**只删本地盘**和本地 NVRAM。clapi 收到 `clear_vm` 回调、确认域已经销毁后，再用 `pickPoolHost` 删除共享系统盘，GPFS 池里的 NVRAM（`<池>/nvram/inst-<ID>_VARS.fd`）一并删除，然后检查基础副本的引用数（§9.6）。云服务器所在节点离线时 `clear_vm` 没法执行，共享系统盘先保留，直到节点恢复并清理完，或者按 §11 确认隔离
- **重装**：取消域定义后，先删旧的共享系统盘，再按 §9.7 从新镜像的基础副本克隆到同一路径；clapi 更新 `base_image_storage_id` 并检查旧基础副本的引用数
- **救援**：救援盘永远在本地，原系统盘按它自己的磁盘 XML 挂进救援虚拟机
- **捕获镜像**：源磁盘由 clapi 下发（GPFS 是文件路径，Ceph 是 `rbd:` 地址），临时文件放 `$cache_tmp_dir`
- **调整规格**：只改 CPU 和内存，不涉及磁盘

### 9.9 节点开机

沿用本地方案的待启动列表（`$cache_dir/pending_start`，已实施）：磁盘所在的池没就绪的云服务器先不启动，之后每次心跳检查，池就绪了再启动并补发同步回调。

- **现在的判断对共享盘无效**：`report_rc.sh:302-311` 的 `instance_pools` 把不在 `/opt/cloudland/pools/` 下的磁盘路径一律算作内置池、视为就绪，而且只看 `source/@file`，RBD 这种网络磁盘直接跳过。要改成：路径在某个 GPFS 池根下的，归到那个池；`<source protocol='rbd' name='<池>/...'>` 的，按池名归到对应的 Ceph 池（都查合并后的 `shared_pools.json`）；池是否就绪读探测状态（§9.2）
- 所有判断都只读状态文件，**不能在心跳里等存储**（心跳被阻塞会让节点被判离线）

这一条随 S2 / S3 一起做：一挂上共享数据盘，节点开机就需要它。阶段 S6 还要改掉「开机就把所有云服务器拉起来」这个行为，见 §11.4。

---

## 10. 迁移

本地盘的迁移以本地方案 §7 与已实施的代码为准（迁移计划 `migrations.disk_plan`、目标端预检与预建由源端经 ssh 完成、关机迁移用 `rsync --sparse`）。共享盘的规则：**不复制、不预建、不清理**。

| 磁盘组成 | 源节点在线，云服务器运行中 | 源节点在线，云服务器已关机 | 源节点离线（`force`） |
|---|---|---|---|
| 全部本地 | 现状：`--live --copy-storage-all` | 现状：先复制磁盘，再 `--offline` | 不支持（现状） |
| 本地与共享混合 | `--live --copy-storage-all --migrate-disks` **只列本地盘** | 只复制本地盘，再 `--offline` | 不支持 |
| 全部共享（GPFS / Ceph） | `--live`，不复制磁盘 | `--offline`，只转移域定义 | **阶段 S6 起支持**，走 §11 |

- **目标节点**：对这台云服务器用到的所有共享池都必须可用；自动选目标时按这个条件过滤，手动指定时同样校验
- **迁移计划**（`services/migration_plan.go:92-140` 的 `planDisks`，现在默认每块盘都复制）：共享盘的动作为「保留」，不进复制列表、不占目标端的预留
- **NVRAM**：在 GPFS 池里的不用复制；在本地的（含 Ceph 云服务器的）照旧复制
- **脚本**：`source_migration.sh` 只对本地盘做「目标上已有同名文件就中止」的检查、预建和复制（**共享盘在目标上本来就在，不排除的话每次都会中止**），没有本地盘时去掉 `--copy-storage-all`；`finish_source_migration.sh`、`clear_target_migration.sh` 只删清单里的本地盘；`complete_migration.sh` 刷新域定义的步骤对所有驱动都做
- **clapi**：迁移完成时只更新本地池卷的 `hyper`。写库的是 `services/migration_plan.go:250-264` 的 `ApplyMigrationPlan`（由 `rpcs/migrate_vm.go:377` 调用），其中没有迁移计划时的兼容分支会把这台云服务器所有卷的 `hyper` 改成目标节点，同样要排除共享卷；有任何本地盘时不允许 `force`
- **阶段**：带共享数据盘的迁移（共享盘不复制）随 S2（GPFS）、S3（Ceph）各自一起做，不等 S4：一挂上共享数据盘，迁移就必须认得它，否则迁移会去复制一块在目标上本来就有的盘
- **缓存模式**：libvirt 对共享存储上的磁盘做热迁移时，缓存模式不安全会拒绝（除非 `--unsafe`）。GPFS 盘用 `cache='none'`；RBD 盘 libvirt 按网络磁盘处理，`writeback` 是否被接受待验证（V6）
- RBD 的排他锁在热迁移时由 librbd 在源、目标两个 QEMU 之间交接，这是 OpenStack 的常规用法

---

## 11. 节点宕机后的恢复（阶段 S6）

### 11.1 前提

同时满足才能把一台云服务器从宕机节点恢复到别的节点：

- 这台云服务器的**全部磁盘**都在共享池上（本地盘随节点一起不可用）；Ceph 云服务器的 NVRAM 在本地，恢复时从模板重建（UEFI 启动项丢失，靠默认的回退启动路径；对 Windows 等依赖启动项的系统要验证）
- 源节点离线（`status=10`）超过宽限期（建议 5 分钟，长于 cland 的 90 秒）
- **已确认隔离**（§11.2）

### 11.2 隔离（fencing）

**风险**：cland 判定节点离线，可能只是管理网断了；节点本身活着，存储网正常，云服务器还在写盘。这时在别处再起一份，两个进程同时写同一块盘，数据损坏。

| | GPFS | Ceph |
|---|---|---|
| 隔离手段 | 管理节点执行 `mmexpelnode -N <节点>`：被驱逐的节点在执行 `mmexpelnode -r` 之前不能重新加入，即使它的网络恢复、重启后 GPFS 自动启动（`-A`）也一样。**不依赖 GPFS 自己按租约驱逐**：那种驱逐在网络恢复后会自动重新加入并挂载 | `ceph osd blocklist range add <节点内网地址>/32 <很长的有效期>`（按地址把这台节点上的所有客户端加入黑名单，之后它的任何读写都被拒绝）。**有效期必须显式给一个很长的值**（如 10 年）：不给时默认 1 小时（`mon_osd_blocklist_default_expire`）就自动解除，旧写入者恢复写盘。`profile rbd` 的客户端身份有加黑名单的权限，外部集群同样能用（`range` 是否允许，待验证，V14） |
| 判定已隔离 | `mmexpelnode -L` 列出它，`mmlsmount <fs> -L` 里没有它 | 黑名单里有这个地址且有效期正确；`rbd status` 的监听者里不再有它 |
| 解除 | 节点回来、完成 §11.4 的对账（被恢复到别处的云服务器已在它上面 `virsh destroy` 并取消定义）后，才 `mmexpelnode -r -N <节点>` | 同样对账完成后 `ceph osd blocklist range rm` |

两种隔离都是主动、持久的，具体命令的效果在 S6 实测（§18.2）。自动执行隔离失败时（例如 GPFS 的管理节点也在故障的那一侧），管理员在确认节点已断电（例如经 SoftLayer 或 IPMI 关机）后可以带 `confirm_fenced=true` 强制执行，记入审计。

### 11.3 流程

1. 管理员调用 `POST /hypers/:uuid/evacuate`（可以指定 `target_hyper`、`confirm_fenced`），或者对单台云服务器 `POST /migrations {force: true}`
2. clapi 检查 §11.1，按驱动执行隔离（§11.2），为每台云服务器建一条 `evacuate` 类型的迁移记录
3. 目标节点以「使用已有磁盘」的方式执行 `launch_vm.sh`（`boot_disk.existing=true`，跳过克隆和扩容；按元数据重新挂数据盘）
4. 之后复用冷迁移的回调链：`LaunchVM` 以 `sync` 更新 `instances.hyper` 和网卡，同步浮动 IP，预写转发条目
5. 源节点的清理推迟到它恢复时（§11.4）

### 11.4 原节点恢复：必须先改

**现在的行为**：节点开机后 `report_rc.sh` 的 `sync_instance` 把本机定义的**每一台**云服务器都启动（池没就绪的进待启动列表，就绪后照样启动），并以本节点身份回调 `launch_vm.sh ... 'sync'`，clapi 会把 `instances.hyper` 改回这个节点。

**后果**：已经恢复到别处的云服务器在原节点上又起一份（两个进程写同一块盘），数据库里的归属也被抢回来。

**要改成**：

1. **不只是开机时**：cloudlet 每次向 cland 重新注册（开机、cloudlet 重启、断网后重连）都触发对账。只断管理网、节点没重启的情况下，旧的 QEMU 一直开着（写被黑名单或驱逐挡住），而 `report_rc.sh` 只上报相对 `old_inst_list` 有变化的云服务器，它永远不会被上报，只有对账能发现它
2. 节点上报本机定义的所有域：`node_recovered '<NODE_ID>' '<boot_id>' '<实例ID 列表>'`；开机时在对账结果回来之前不启动任何云服务器
3. clapi 逐台对账：数据库里归属本节点且没删的，下发启动（已在运行的不动）；其他的下发 `clear_stale_vm.sh`：`virsh destroy`（还开着的话）、取消域定义，删除 XML、ISO 和本地 NVRAM，**不碰任何共享盘**，不改数据库
4. `inst_status` 和 `launch_vm sync` 回调加保护：实例有一条已完成的 `evacuate` 记录、上报的正好是它的源节点、并且这个节点的对账还没完成时，不改 `hyper`，只记告警。对账完成后保护解除，以后合法地迁回这个节点不受影响
5. 清理完成后才解除隔离（§11.2）

**这一节先实现并测试通过，才能开放宕机恢复。**

---

## 12. 一致性与安全

### 12.1 同一块盘只有一个写入者

| 层 | GPFS | Ceph |
|---|---|---|
| clapi 状态机 | 卷只有 `available` 时能挂载；一台云服务器任一时刻只归属一个节点；迁移、恢复都有明确的状态流转 | 同左 |
| QEMU 镜像锁 | QEMU 对打开的镜像文件加 OFD 锁。GPFS 支持集群范围的 POSIX 字节范围锁，如果 OFD 锁也在集群范围生效，另一个节点再打开同一块盘就会失败（待验证，V5） | **没有**：RBD 的排他锁是协作式的，另一个客户端请求时锁会被交出去，挡不住两个写入者 |
| 兜底 | 第二层无效时启用 libvirt 的 virtlockd（锁空间放在 GPFS 上） | virtlockd 只管文件路径，管不了网络磁盘。**所以 Ceph 的恢复必须先加黑名单**（§11.2），这是硬性要求 |

### 12.2 缓存模式与出错策略

- GPFS 盘 `cache='none'`、`error_policy='stop'`
- RBD 盘 `cache='writeback'`（打开 librbd 的缓存）、`discard='unmap'`（客户机删除文件后空间能回收，精简配置才有意义）
- 本地盘不变（`error_policy='enospace'`）

### 12.3 删除共享盘的规则

- 只删 clapi 点名的那一个（文件名或镜像名与卷 ID 对应），不用通配符、不按前缀
- 必须先确认云服务器已销毁（`clear_vm` 回调、迁移已完成，或 §11.2 的隔离已确认）
- 迁移的清理脚本、`clear_vm.sh`、`end_rescue.sh` 都只处理本地文件
- RBD 镜像还有监听者时 `rbd rm` 会失败（§9.3），这是额外的检查；网络分区时监听会超时消失，不能当作保证

### 12.4 凭据与权限

- 集群 SSH 私钥只在 GPFS 管理节点上（Ceph 的在集群配置库里），`client.admin` 密钥环只在 Ceph 的 `_admin` 主机上；普通计算节点只有 CloudLand 的客户端身份（权限限于 CloudLand 的池）
- 下发凭据经过没有 TLS 的 gRPC（待办 A6），与 VPN 凭据同一个问题
- 节点脚本里的 `ceph` / `rbd` / `rados` 命令一律显式带 `--conf` 和 `--id`，不依赖默认的 `/etc/ceph/ceph.conf`（多个集群时会连错）
- libvirt 的动态属主会在启动时把 GPFS 上的磁盘文件改成 `libvirt-qemu` 所有，在共享文件系统上的表现、迁移时的属主处理待验证（V7）
- AppArmor：Ubuntu 的 libvirt 为每台云服务器动态生成规则，GPFS 路径会被加进去；RBD 盘时 QEMU 里的 librbd 可能要读 `/etc/ceph/` 下的配置、在 `/var/run/ceph/` 建管理套接字，可能被拦，要看 `journalctl -k | grep DENIED`（V7）

---

## 13. 接口、界面与审计

### 13.1 clapi 接口

除特别说明外都只给系统管理员。长操作一律返回 202 和任务 ID。

**软件包**（阶段 S2）

| 接口 | 说明 |
|---|---|
| `GET /storage_packages` | 列表（版本、版本类型、支持的发行版、大小、状态、许可证同意人与时间） |
| `POST /storage_packages` | 开始上传 `{kind, file_name, size_bytes}`，返回包 ID 与分片大小；或 `{kind, url}` 由 clapi 拉取 |
| `PUT /storage_packages/:id/parts/:n` | 上传第 n 片（二进制，8 MiB） |
| `POST /storage_packages/:id/complete` | 合并分片，开始校验 |
| `GET /storage_packages/:id` | 详情，含 `parts_done`（断点续传从下一片开始）、SHA-256、许可证文本、清单摘要 |
| `POST /storage_packages/:id/accept_license` | 接受许可证 |
| `DELETE /storage_packages/:id` | 删除（还有集群在用时拒绝）；上传中的同时清掉 S3 的分段 |

**集群**

| 接口 | 说明 |
|---|---|
| `GET /storage_backends` | 支持的存储类型：每种的角色、贡献磁盘的角色、建议角色、支持的系统、是否要内核模块或容器（§4.5.1）；界面按它渲染（S1 已实现） |
| `GET /storage_clusters`、`GET /storage_clusters/:id` | 列表、详情（节点、磁盘、文件系统、存储池摘要、健康、正在跑的任务）；节点与磁盘的类型专用属性在 `attrs` 里原样返回 |
| `POST /storage_clusters/precheck` | 只做预检：`{kind, nodes, disks, params, allow_unsupported}`，返回任务 ID（任务种类 `precheck`，不占集群）。`kind` 与角色由后端校验；`params` 是 JSON 对象，原样交给该类型的后端解析，不认识的键返回 400 |
| `POST /storage_clusters` | 新建托管集群：`{kind, name, layout, package_id \| image, nodes:[{hypervisor, roles}], disks:[{hypervisor, disk_id, media, wipe}], params, allow_unsupported}`；节点用 UUID 指定，与其他接口一致 |
| `POST /storage_clusters/import` | 导入外部集群（§7.8、§8.8） |
| `PATCH /storage_clusters/:id` | 名称、说明、自动加入客户端的可用区等不影响集群运行的设置 |
| `PATCH /storage_clusters/:id/nodes/:hypervisor` | 改角色（任务 `change_roles`）：GPFS 增减仲裁 / 管理节点（`mmchnode`），Ceph 改 mon / mgr / `_admin` 的放置（改标签后 `ceph orch apply`）；校验同 §6.3 |
| `DELETE /storage_clusters/:id` | `?confirm_name=&purge_packages=`（网关转发 DELETE 时丢掉请求体，确认放查询参数）；托管集群的盘一律擦除；外部集群是取消登记（§7.8、§8.8） |
| `POST /storage_clusters/:id/nodes`、`DELETE .../nodes/:hypervisor` | 加节点（可带盘）、移除节点（`?offline=true&confirm=<主机名>` 为离线移除，`purge_packages=true` 同时卸载软件，§7.5、§8.5） |
| `POST /storage_clusters/:id/disks`、`DELETE .../disks/:id`、`POST .../disks/:id/replace` | 加盘、移除盘、换盘 |
| `POST /storage_clusters/:id/filesystems`、`DELETE .../filesystems/:id` | 只有 GPFS |
| `POST /storage_clusters/:id/rebalance` | 只有 GPFS |
| `POST /storage_clusters/:id/clients`、`DELETE .../clients/:hypervisor` | 只有 Ceph：给计算节点配置、移除客户端 |
| `POST /storage_clusters/:id/upgrade`、`POST .../rotate_keys` | 升级、轮换集群 SSH 密钥与 Ceph 客户端密钥（阶段 S6） |
| `GET /storage_clusters/:id/metrics` | 监控曲线（§14），时段与步长的限制同 VPN 流量接口 |

**任务**

| 接口 | 说明 |
|---|---|
| `GET /storage_tasks?cluster_id=&status=` | 分页列表；`cluster_id=0` 列出不占集群的任务（预检、自测） |
| `GET /storage_tasks/:id` | 步骤、每台节点的结果与日志尾部 |
| `GET /storage_tasks/:id/runs/:run/log` | 完整日志（阶段 S5）：第一次调用让节点上传，返回 202；之后再调用，上传完成时返回下载地址（§6.2.6） |
| `POST /storage_tasks/:id/retry`、`POST /storage_tasks/:id/abort` | 重试、中止 |

**存储池**（已有的接口，`api/src/apis/routes.go:175-181`）

- `POST /storage_pools` 增加 `driver`（`gpfs` / `ceph_rbd`）、`cluster_id`；托管集群的 GPFS 池另带 `filesystem_id`、`media`、`quota_gb`、`inode_limit`，Ceph 池带 `media`、`replicas`、`quota_gb`；外部集群带已有的 `mount_path` + `fileset` 或 `ceph_pool`。共享池返回 202 和任务 ID
- `PATCH /storage_pools/:id` 增加 `quota_gb`
- `DELETE /storage_pools/:id` 对托管集群的池发起删除任务
- `GET /storage_pools/:id/hypers` 已有，共享池返回各节点的可用性
- 普通成员的 `GET /storage_pools` 多返回 `shared`（已有）与集群类型，不返回集群细节

**其他**：`POST /hypers/:uuid/evacuate`（阶段 S6，§11.3）；`GET /hypers/:uuid` 返回这台节点在各集群里的角色；`PATCH /images/:id` 增加 `storage_pools: [...]`，预热镜像基础副本（§9.6）。

### 13.2 cpgateway

- `proxy_routes.go` 白名单加入以上路由，全部标为系统管理员专用（已有的 `GET /storage_pools`、`GET /storage_pools/:id` 保持所有成员可用）
- 分片上传每片 8 MiB，在现有的 10 MB 请求体限制之内，不用改 nginx
- 配额不变：共享池上的盘照样计入 `disk_gb`

### 13.3 界面

侧边栏新建「存储」分组（现在「存储池」只是「管理」下的一个条目，`Layout.vue:420-422`，移进这个分组）：存储池、**存储集群**（新）、**软件包**（新，S2）。页面一律复用现有的基础组件（`BaseModal`、`DataTable`、`StatusBadge`、`PaginationBar`、`useListQuery`、`DetailTabs`、`InfoRow`、`CapacityBar`、`MonitoringPanel`），长表单弹窗的报错放在底栏按钮旁。

- **软件包页**：列表；上传弹窗（分片上传、进度条、断点续传，也可以填下载地址）；许可证弹窗（全文 + 「我已阅读并接受」）
- **存储集群列表**：名称、类型（GPFS / Ceph）、模式（托管 / 外部）、版本、状态、健康、节点数、磁盘数、容量条、存储池数、正在跑的任务
- **创建向导**（整页，不用弹窗，步骤太多）：
  1. 类型与模式：GPFS 副本 / Ceph / 导入外部 GPFS / 导入外部 Ceph（GPFS 纠删码灰显，标「后续版本」）
  2. 软件：GPFS 选一个已接受许可证的软件包（显示它支持的系统）；Ceph 填镜像地址与仓库口令
  3. 节点与角色：区域内的在线节点，每行显示系统 / 内核（不在支持范围内标红并说明原因）、内存余量、已在哪个集群；勾角色，实时校验（仲裁节点奇数、mon 奇数、副本数与节点数）
  4. 磁盘：每台节点的空闲盘（来自磁盘扫描，显示型号、大小、介质、扫描时间，超过 24 小时提示重新扫描）；`dirty` 的盘要单独勾选「擦除」
  5. 参数：§7.2、§8.2 列的参数，带默认值
  6. 预检：一键执行，逐节点逐项显示结果；有不通过的项不能继续，系统不在支持范围的那一项可以勾「允许不受支持的系统（仅用于测试）」
  7. 确认：汇总将占用的盘、每台节点将预留的内存，提交后跳到集群详情的任务页
- **集群详情**（标签页）：
  - 概览：状态、健康摘要、版本、参数；不受支持的系统、正在跑的任务以横幅显示
  - 节点：主机、角色、状态、存储软件报告的状态、预留内存；加节点、移除节点
  - 磁盘：主机、设备、NSD 名或 OSD 编号、介质、大小、所属文件系统或池、状态；加盘、移除、换盘
  - 文件系统（只有 GPFS）：容量、副本数、块大小；新建、删除、重新均衡
  - 存储池：这个集群上的 CloudLand 存储池（容量、配额、可用节点数 x/y）；新建、改配额、删除
  - 任务：历史任务列表；任务详情抽屉显示步骤时间线、每台节点的状态与进度、日志尾部（任务运行中每 3 秒静默刷新），重试 / 中止 / 下载完整日志（S5）
  - 监控：容量、读写吞吐与 IOPS（§14）
- **节点详情的存储标签**（已有）：增加「所属存储集群与角色」；磁盘表里被集群占用的盘显示集群名与 NSD / OSD
- **存储池页**（已有）：新建弹窗先选类型（本地 / GPFS / Ceph），共享类型再选集群和参数；列表增加类型与集群列
- **云硬盘挂载弹窗**：共享卷把所在节点对这个池不可用的云服务器置灰并说明原因
- **迁移弹窗**：逐盘列出「复制」或「共享，不复制」
- 新增文案三种语言都加，`npm run i18n:check` 通过

### 13.4 审计

`audit_actions.go` 的 `auditRoutes` 增加：`storage_package.upload`、`storage_package.accept_license`、`storage_package.delete`；`storage_cluster.precheck`（预检也会往节点下发命令）、`create`、`import`、`update`、`delete`、`change_roles`、`add_nodes`、`remove_node`、`add_disks`、`remove_disk`、`replace_disk`、`create_fs`、`delete_fs`、`rebalance`、`add_clients`、`remove_clients`、`upgrade`、`rotate_keys`；`storage_task.retry`、`abort`；`hyper.evacuate`（带 `confirm_fenced`）。存储池的动作已有。勾选「允许不受支持的系统」记在 `storage_cluster.create` / `precheck` 的审计里。

---

## 14. 监控与告警

### 14.1 健康看护（clapi 后台）

每个 `ready` 的集群（包括健康已经变差的、包括外部集群）每分钟一轮，只在拿到选主锁的那台 clapi 上跑（§6.2.1），以 `tracing.StartBackground` 起根 span。健康检查脚本以后台作业执行（`async_exec`，命令全部加 `timeout`），回调 `storage_health '<集群UUID>' '<base64 JSON>'`，结果写回集群的 `health` / `health_info`、节点的 `state`、磁盘的 `status`、文件系统与存储池的容量：

| | GPFS（`gpfs_health.sh`） | Ceph（`ceph_health.sh`） |
|---|---|---|
| 在哪执行 | 托管：一台在线的管理节点；外部：一台在线的成员 | 托管：一台在线的 `_admin` 主机；外部：一台在线的客户端 |
| 集群健康 | `mmhealth cluster show -Y` | `ceph health detail -f json` |
| 节点 | `mmgetstate -a -Y` | `ceph orch host ls -f json`、`ceph orch ps -f json` |
| 磁盘 | `mmlsdisk <fs> -Y`（`availability`、`status`） | `ceph osd tree -f json`（`up` / `in`） |
| 容量 | `mmdf <fs> -Y`（含每个 GPFS 存储池）；每个池 `mmlsquota -j <fileset> <fs> -Y` | `ceph df detail -f json` |

外部集群只做能做的那部分（GPFS 外部集群只有挂载状态和 `df`，§7.8）。

看护还顺带做两件修复：GPFS 的 NSD 节点回来后它的盘仍 `down` 时自动 `mmchdisk <fs> start`（§7.1）；离线时没做完的节点本地清理（§7.6、§8.6），节点回来后补做。

### 14.2 告警

**只有一条告警路径：由健康看护直接产生**，与 VPN 告警的做法相同（`services/vpn_notify.go`：clapi 写告警事件并经通知渠道发送，恢复时解除）。不另写 Prometheus 告警规则：两条路径覆盖同一批条件会重复告警，而且 GPFS 的指标里没有节点和盘的状态，用规则也写不出来。

| 告警 | 条件 | 级别 |
|---|---|---|
| `StorageClusterUnhealthy` | 健康变为 `warning` / `error`（持续 2 分钟） | 按健康级别 |
| `StorageNodeDown` | 成员节点在存储软件里不是 `active` / 不在线（持续 2 分钟） | warning |
| `StorageDiskDown` | NSD 不是 `up` / OSD 不是 `up`（持续 2 分钟） | warning |
| `StoragePoolUsageHigh` | 共享池用量（口径同 §9.5 的准入）≥ 80% / 90% | warning / critical |
| `CephNearFull` | Ceph 报 `nearfull` | critical |
| `StorageFsUnmounted` | 某个成员上 GPFS 文件系统没挂上（来自 §9.2 的探测） | critical |

阈值放系统设置（常规类），默认值如上。告警属于集群所在区域的系统组织，走系统组织的通知渠道。

### 14.3 指标与曲线

- **Ceph**：启用 mgr 的 `prometheus` 模块（端口 9283，只有活动的 mgr 输出数据）。clapi 在部署、mgr 放置变化后把所有 mgr 主机的地址写进 Prometheus 的目标文件 `/etc/prometheus/lists/ceph_targets.json`（与 `matched_vms.json` 同一种 file_sd 做法，Prometheus 自己重读，不用重载），Prometheus 配置加一个 `ceph-mgr` 采集任务
- **GPFS**：每个成员上一个 node_exporter textfile 采集脚本（附录 E），导出挂载状态、文件系统容量、组件健康。不装 IBM 的性能采集（zimon）
- 界面的监控标签由 clapi 查 Prometheus（`GET /storage_clusters/:id/metrics`），限制与 VPN 流量接口相同：先限定时段（跨度不超过 31 天）再运算，步长取整秒
- Grafana：Ceph 用官方面板；GPFS 用 CloudLand 自带的简单面板（挂载状态、容量、组件健康）。指标只用于看曲线，不用于告警

## 15. 部署与配置

### 15.1 控制面

- **托管 GPFS 需要 S3**（`minio` profile 或外部 S3），用来放安装包和完整日志
- 凭据沿用 `VPN_SECRET_KEY`（§5.10，改名待决策 D7）
- Prometheus 配置加 `ceph-mgr` 采集任务（file_sd，§14.2）
- 不新增容器

### 15.2 计算节点

- `deploy-compute-node.sh` 与 Ansible 的 hyper 角色**默认不装任何存储软件**：节点加入存储集群时由任务按需安装（§7.2、§8.2）。把 `qemu-block-extra`（QEMU 的 RBD 块驱动）显式加进两个版本的包列表：现在两处都没写，26.04 节点上有它只是因为 `qemu-system-x86` 的推荐依赖把它带了进来；`lvm2` 已在列表里
- **要用 GPFS 的节点装 Ubuntu 24.04**（§2.2）。部署脚本本来就支持 24.04
- GPFS 节点的内核元包被 `apt-mark hold`（§7.7）
- 节点之间的内网放行沿用现状（节点 INPUT 对私网地址全放行）；收紧防火墙的环境要放行 §3.1 列的端口
- cephadm 用节点上已有的 docker

### 15.3 文档

- 部署文档新增一页 `docs/deployment/09-shared-storage.md`（前提、节点系统要求、S3、上传安装包、建集群、排障），`index.md` 的快速导航加一条
- 使用指南新增 `docs/guide/storage-clusters.md`（概念、模式怎么选、向导各步、写满时的表现差异、限制）

---

## 16. 实施阶段

```
S0 可行性验证 ──▶ S1 共用框架 ──┬─▶ S2 GPFS（托管副本 + 外部 + 数据卷）──┬─▶ S4 系统盘与共享迁移 ──▶ S5 运维完善 ──▶ S6 升级与宕机恢复 ──▶ S7 ECE 等
                                └─▶ S3 Ceph（托管 + 外部 + 数据卷）──────┘
```

GPFS 优先：S2 先于 S3 开工。S2 的任何真实验证都要 24.04 的节点（决策 D1）——WSL 沙箱跑不了 GPFS（§2.5、V22）；节点没到位之前，GPFS 的节点脚本只能先写、不能执行，S3 可以并行，用它把 S1 的框架在真实节点上跑通。

### S0：可行性验证

**已做**（2026-10-01）：GPFS 6.0.0.2 对 26.04 / 7.0 内核编译失败（§2.2）；GPFS 模块在 WSL 内核上加载即让内核崩溃（V22）。

**本机 WSL 沙箱里能做的**（不占 work-x）：

- Ceph：cephadm 20.2 用 docker 带 `--skip-monitoring-stack` 起单节点，OSD 放在回环设备上的 LVM 逻辑卷；§18.2 里标 S0 的 Ceph 各项
- ~~GPFS：为 WSL 内核编译 GPFS 模块，能编过就起单节点集群~~ 不行（V22）。§18.2 里标「S0（WSL 沙箱）」的 GPFS 各项（V2、V3、V4）改到 24.04 节点上做

**要节点才能做的**（决策 D1、D3）：~~GPFS 在 24.04 上 `mmbuildgpl`~~ 已做（2026-10-02，V1 通过）；Ceph 在节点上从单节点扩到三节点。

结果回填 §18.2，结论改变设计的地方先改设计再开工。

### S1：共用框架

**范围**：

- 模型：§5.1–§5.4、§5.9（集群、节点、磁盘、文件系统、任务三张表）；共享池、卷、基础副本的模型变更随用到它们的阶段做
- 任务引擎（§6.2）：两个槽、outbox 下发与重发、节点作业目录与 `stc_lib.sh`、`stc_poll.sh`、`stc_kill.sh`、回调去重、超时与离线、重试（含 `RetryFrom`）、中止（`aborting` → `aborted`）、后台循环选主
- `selftest` 任务种类：脚本按参数睡眠、失败、输出进度、拿集群锁，用来在真实节点上验证引擎
- `precheck` 任务种类与 `stc_precheck.sh`（§6.5）
- 磁盘：身份核对库（按稳定 ID 解析 + 核对序列号 / WWN / 大小）；扫描识别 `ceph_osd`；装了 GPFS 的节点上未确认的空白盘判 `unknown_member`；登记过的盘按登记显示；`pickDisks` 检查集群认领（§6.4）
- 集群 SSH 密钥的生成与 `stc_ssh_trust.sh`（§6.6）
- 内存预留：`stc_mem_reserve.sh` 与 `report_rc.sh`（含 `MemFree` 那一行，§6.7）
- 错误码 `ErrVpnSecretUnavailable` 改名为 `ErrSecretUnavailable`（§5.10）
- 接口：预检、任务列表 / 详情 / 重试 / 中止、集群列表 / 详情（只读）；网关白名单；审计映射
- 界面：侧边栏「存储」分组、存储集群列表（S1 里只有空列表和「节点预检」入口）、预检弹窗、任务详情抽屉

**验收**：

- 引擎的 PostgreSQL 测试（照 `placement_pg_test.go` 的做法，用 `startFakeCland` 核对下发的控制串和命令，节点的回调用本机 WSL 里真实执行节点脚本得到的输出）：建任务、推进、重复回调、回调丢失后靠轮询收敛、`missing` 重发、超时、离线、重试只重跑失败的节点、`RetryFrom`、中止、两个槽互不阻塞、失败的任务占着结构槽
- 节点脚本在 WSL 里：作业启动幂等（同一运行下发两次只跑一份）、`boot_id` 变化判「作业中断」、同一集群的锁、只往回调描述符输出
- 对三台节点发起预检，逐项结果正确（26.04 节点的 GPFS 预检判为不支持）——要部署到 work-x，等用户同意
- 部署后用 `selftest` 在真实节点上验证：节点重启时正在跑的那一步在有限时间内判失败；cloudlet 重启、clapi 重启后任务继续；重试时旧作业还在跑不会重复执行；节点离线时 5 分钟内判失败

**实施记录**（2026-10-01，未提交、未部署）：

- 代码：模型 `model/storage_cluster.go`、`model/storage_task.go`；引擎 `services/storage_task.go`；预检、自检与读接口 `services/storage_cluster.go`；支持矩阵、端口、数据目录、内存预留与集群 SSH 密钥生成 `services/storage_support.go`（`newStorageSSHKey` 到 S2 建集群时才调用；类型相关的部分 2026-10-02 挪进了各类型的后端，见本节最后一条）；回调 `rpcs/storage_task.go`；接口 `apis/storage_cluster.go`（8 个，均为系统管理员）；网关白名单、审计映射；磁盘扫描与 `pickDisks` 的集群认领（`hyper_storage.go`）、删节点前检查集群成员（`hyper.go`）；节点脚本 `scripts/kvm/storage/stc_{lib,poll,kill,selftest,precheck,ssh_trust,mem_reserve}.sh`、`storage_lib.sh`（`ceph_osd`、`unknown_member`、`disk_identity`）、`report_rc.sh`（内存预留，含 `MemFree`）；界面：侧边栏「存储」分组（存储池移入）、`StorageClusters.vue`（集群 / 任务两个标签）、`StorageTaskDetail.vue`（任务详情做成页面而不是抽屉，与其他详情页一致；预检结果按节点逐项列出）、`StoragePrecheckModal.vue`、`StorageSelftestModal.vue`
- 与上文不同的地方：① 一个步骤的「当前运行」对管理节点步骤只看最后一次运行（重试可能换了管理节点，之前失败的那次不再算），否则重试成功后任务仍判失败；② 回调必须来自运行下发到的那台节点（原先回调不带节点时也接受）；③ 作业收到 `TERM` 以退出码 143 结束，避免 `async_exec` 的外壳把 `Terminated` 写进心跳输出；④ 自检多了 `fail_step`（只在第几步失败）；⑤ 集群返回里的 `active_task` / `active_pool_task` 是任务 UUID
- 测试：`services/storage_task_pg_test.go`（PostgreSQL，假 cland 记录下发的命令）：步骤顺序、重复与迟到的回调、他人节点的回调被拒、失败与只在失败节点上重试、`missing` 重发与重发上限、命令发不出去、输入超限、中止（含失败任务直接中止）、超时、离线、轮询（一次只一个、回答之后再问、久无回答再问）、管理节点步骤换节点重试、两个槽与 `RetryFrom`、收尾失败保留槽位、中止释放槽位、选主锁；`TestStoragePrecheckPG`：非管理员、角色规则、脏盘、旧扫描、已认领、客户端主机的盘、离线主机、预检输入（对端、端口、盘身份、数据目录、内存预留）、同名主机判失败。另把管理节点步骤的修复撤掉验证过测试会失败。`rpcs/storage_task_wsl_test.go`（`STORAGE_WSL_E2E=1`）：引擎下发的真实命令在 WSL 里按 cloudlet 的方式执行，回调经 clapi 的解析器回来——两步自检含失败与重试、长作业被轮询到进度、中止杀掉作业、对回环盘的真实预检。WSL 里的节点脚本测试 40 项（作业协议 18 项；预检、SSH 信任、内存预留、磁盘识别 22 项）。界面在本机 5173 上用 Playwright 检查（存储接口在浏览器里模拟，其余只放行 GET）：1440 / 1280 / 1024 无横向溢出、无页面错误，预检弹窗读到三台真实节点并给出默认角色
- ~~没做的验收：对三台节点的真实预检与部署后的 `selftest`~~ **2026-10-02 在重新部署的 work-x（24.04）上做了**（CloudLand 重新部署、本地改动叠加到 work-01，用户批准）：
  - 预检：对三台各跑一次 GPFS、一次 Ceph，全部项目通过（24.04.5、内核 6.8.0-146 匹配 GPFS 的 6.8 GA 规则，安全启动关闭，时间同步，节点间端口连通，sdb 空闲且身份可核对，空间与内存足够，Ceph 的 docker 在运行，没有外部安装）
  - 链路自检：失败后重试只在失败的那台重跑；任务中途重启 work-02 的 cloudlet，作业不受影响、任务完成；任务中途重启 clapi，任务照常完成；中止后三台的作业都被杀掉、没有残留；停掉 work-03 的 cloudlet，它的作业约 5 分钟后判「节点离线」失败，另两台正常完成
  - 发现并修掉一个引擎问题：一步的作业都成功、但跨节点检查（`Done`，如预检里两台同名、系统版本不受支持）判失败时，重试报「没有可重跑的节点」；改为这种情况在这一步的所有节点上重跑（`RetryStorageTask`，PostgreSQL 测试覆盖）
  - 还没做：节点整机重启时正在跑的那一步在有限时间内判失败（要重启节点，等用户同意）
- **改成通用接口（2026-10-02，未提交、未部署，§4.5）**：新增 `services/storage_backend.go`（接口与注册）、`storage_backend_gpfs.go`、`storage_backend_ceph.go`，原来散在 `storage_support.go` 和 `storage_cluster.go` 里的支持矩阵、端口、数据目录、角色表、角色规则、内存预留与「节点能否同时在两个集群」的判断都挪进各自的后端，框架里不再按类型分支；参数改为原样的 JSON，由后端解析并拒绝不认识的键（原来在接口层用 gin 的绑定规则校验）；模型去掉 Ceph 专用的 7 列（`Image`、`ClientUser`、`ClientKey`、`MonAddrs`、`SecretUUID`、`RegistryUser`、`RegistryPassword`）和 GPFS 专用的 3 列（`FailureGroup`、`Usage`、`GpfsPool`），改为 `params` / `attrs` / `secrets` 三个 JSON 列（S1 里这些列都还没用到）；新增 `GET /storage_backends`（网关白名单已加），预检弹窗的类型、角色、贡献磁盘的角色和建议角色都取自它；节点侧预检的「已有安装」检查挪进 `scripts/kvm/storage/backends/{gpfs,ceph}.sh`（`stc_lib.sh` 的 `backend_load` 引入）。测试：新增 `services/storage_backend_test.go`（注册表约束、参数校验、角色规则、节点冲突、内存预留，不要数据库），原有的 PostgreSQL 测试、WSL 端到端测试全部通过；节点脚本测试加了 3 项（GPFS 钩子认出沙箱里装的 GPFS、Ceph 钩子判为没有、不认识的类型判不通过），共 43 项通过；界面测试改为全部接口在浏览器里模拟（work-01 已重装），另模拟了一个前端没有任何文案的第三种类型 `nfs`（只有客户端角色、不收磁盘），弹窗照常工作、不显示键名，16 项通过

### S2：GPFS（托管副本、外部导入、存储池与数据卷）

**进度**（2026-10-02，未提交）：S2 的代码已全部写完并在本地测过（见本节末尾「S2 其余部分的实施记录」），2026-10-02 晚叠加到 work-01 / 02 / 03 做了真实节点验收（见本节最后「S2 真实节点验收」），整机重启（2026-10-03，待启动列表与 NSD 恢复）、界面上传与断网续传、界面从零部署、装包途中断网后重试也都已通过；只剩加节点 / 移除节点 / 离线移除（只有三台，只验了被拒绝）、导入外部集群、两种介质没测（都受环境限制）。最早的一批是：软件包仓库（模型、分片上传与校验、许可证、接口；真实安装包在 work-01 上经 nginx → cpgateway → clapi → MinIO 上传通过，1.7 GB 70 秒，服务端算出的 SHA-256、版本、版本类型、发行版、清单、许可证文本都对）、新建 / 删除托管集群的通用部分、GPFS 后端的部署与删除任务（12 步部署、2 步删除）和全部节点脚本（`stc_join`、`stc_fetch`、`gpfs_install`、`gpfs_build_gpl`、`gpfs_cluster`、`stc_resolve_disks`、`gpfs_nsd`、`gpfs_fs`、`stc_finish`、`stc_leave`，`backends/gpfs.sh` 的钩子），引擎加了按存储类型找任务定义（`storage_tasks.backend`）。PostgreSQL 测试覆盖部署与删除的每一步输入和落库。界面：软件包页（列表、分片上传与续传、从地址下载、许可证弹窗与接受），本机界面测试 16 项通过；创建向导、集群详情还没写。存储池、驱动、数据卷都还没写

**真实节点上的部署与删除**（2026-10-02，用户同意后执行）：经接口在 work-01 / 02 / 03（24.04.5、内核 6.8.0-146）上部署三节点副本集群 `gpfs1`，每台的 sdb 一个 NSD：

- 结果：集群 `gpfs1.work-01`（GPFS 给集群名加了主节点的域名后缀，`gpfs_cluster.sh` 按点号前一段比对）三台都是 quorum-manager、全部 active，远程命令用的是集群自己的 `gpfs_rsh` / `gpfs_rcp`；NSD `cl1h1d1`–`cl1h3d1`、故障组 1–3；`fs1` 元数据 3 副本、数据 2 副本、块 4 MiB，三台都挂在 `/gpfs/fs1`（5.5 TB）；work-02 写入的文件 work-03 读到；`adminMode=central`、`autoBuildGPL`、`restripeOnDiskFailure`、`pagepool 1024M` 生效；每台的 `nsddevices` 只列自己的 sdb，内核包已 hold，内存预留 2 GiB；磁盘扫描把 sdb 判为 `gpfs_nsd`，接口里集群 `ready`
- 各步耗时：预检 16 秒、加入 16 秒、下载 1.7 GB 23 秒、安装 60 秒、编译 30 秒、SSH 信任 13 秒、建集群 25 秒、启动 2 分钟（GPFS 自己有 93 秒的「安全恢复」等待）、核对磁盘 15 秒、建 NSD 17 秒、建文件系统并全部挂上 2 分钟、收尾 3 秒，合计约 9 分钟（S2 验收的目标是 30 分钟）
- 删除（带卸载软件包）约 1 分 20 秒；之后三台没有 GPFS 的包、目录、进程、模块和挂载，sdb 无签名、扫描为 free，集群密钥行已删、内核包已解除 hold、内存预留归零，接口里集群消失；确认名填错时拒绝
- 跑的过程中发现并修掉 4 个问题：① GPFS 把节点文件里的地址反查成主机名后用主机名登录，`known_hosts` 只写地址时 `Host key verification failed`，改为每行写「地址,主机名」；建集群一步重试时从 SSH 信任重来（`RetryFrom`）；② 建文件系统一步只传了 NSD 名字，脚本要的是完整描述（用途、故障组、存储池，`mmcrfs` 从描述里取这些）；③ `mmlsnsd -d <名字>` 对不存在的 NSD 也返回 0，NSD 被当成已存在而全部跳过，删除时同样会误判，改为按 `mmlsnsd -X` 的列表比对名字（`gpfs_nsd_exists`），并让建文件系统一步重试时从核对磁盘重来；④ 网关转发 DELETE 时丢掉请求体，删除集群的确认名改为查询参数（`?confirm_name=&purge_packages=`，与删本地池一致）

**范围**：§6.1 软件包仓库；GPFS 后端的其余方法与文件型池驱动（§4.5）；§7.1–§7.6、§7.8；§9 中 GPFS 驱动的数据卷部分（建卷、挂载、扩容、删卷、可用性检查的探测框架、容量与准入、§9.9 的开机待启动列表）；带 GPFS 数据盘的迁移（§10）；§5.5–§5.6 的模型变更与附录 A 里所有 `volumes.hyper` 的读取点；界面的软件包页、创建向导、集群详情的 GPFS 部分、存储池新建弹窗。

**验收**（24.04 节点）：

- 经界面上传 1.7 GB 的安装包，中途断网后能续传；清单、发行版、许可证文本解析正确；不接受许可证不能部署
- 在界面上从零部署三节点副本集群，30 分钟内完成，过程中每一步的进度和日志可见
- 在集群上建存储池，fileset、放置规则、配额与 CloudLand 的记录一致
- GPFS 卷挂到 A 节点上的云服务器写入数据，卸载后挂到 B 节点上的云服务器，数据一致；在线、离线扩容都正常；带 GPFS 数据盘的云服务器迁移时不复制这块盘
- 某节点卸载文件系统后 5 分钟内变为不可用，往它上面的云服务器挂卷被拒绝；删掉标记文件后任何写操作都失败，根文件系统上没有产生文件
- 节点重启：GPFS 自动挂载前，用 GPFS 盘的云服务器留在待启动列表里，挂上后自动启动；NSD 节点回来后它的盘被自动拉起
- 加节点、加盘、移除盘、移除节点、离线移除、删除集群：结束后节点上没有残留的进程、密钥、挂载，盘已擦除，磁盘扫描显示为空闲
- 装包途中断开一台节点的网络（`tc netem`）：任务失败停住，恢复网络后重试成功
- 导入外部集群：要另一组不归 CloudLand 管的节点（一台节点只能属于一个 GPFS 集群，不能用 CloudLand 部署的集群模拟），节点不够时这一项推迟

**S2 其余部分的实施记录**（2026-10-02，未提交、未部署）：

- **代码**：
  - clapi：`services/storage_pool_driver.go`（`PoolDriver`：`Name`、`Family`、`Format`、`VolumeRef`、`DriverArgs`、`CapacityGroup`，文件型公共实现与 GPFS 驱动，`pickPoolHost`）、`storage_pool_shared.go`（建池 / 改配额 / 删池的通用部分、共享池清单、`shared_pool_status` 的处理、每 5 分钟重发清单、维护规则）、`volume_shared.go`（共享卷的建、挂、扩、删）、`storage_cluster_ops.go`（加节点、加盘、移除盘、移除节点、重新均衡的通用部分）、`storage_cluster_import.go`（导入、取消登记）、`storage_backend_gpfs_pools.go`、`storage_backend_gpfs_ops.go`、`storage_backend_gpfs_import.go`；`storage_admission.go` 的 `admitShared`；`volume.go`、`migration_plan.go`、`storage_callbacks.go`、`instance.go` 的共享池分支；`rpcs/storage_shared.go`（`shared_pool_status`、`create_volume_shared`、`attach_volume_shared`）；接口：`POST /storage_pools` 带 `cluster` 和 `params`、`PATCH` 带 `quota_gb`（都返回 202 和任务），`POST /storage_clusters/import`、`/storage_clusters/:id/nodes`（POST、`DELETE .../:hypervisor?offline=&confirm=&purge_packages=`）、`/disks`（POST、`DELETE .../:disk_id`）、`/rebalance`；集群详情返回文件系统、存储池和计数，后端列表返回 `capabilities`；网关白名单与审计（`storage_cluster.import`、`add_nodes`、`remove_node`、`add_disks`、`remove_disk`、`rebalance`）
  - 节点：`scripts/kvm/storage/drivers/{file,gpfs}.sh`、`{create,attach,resize,delete}_volume_shared.sh`、`sync_shared_pools.sh`、`shared_pool_probe.sh`、`storage/{stc_pools,stc_release_disks,stc_forget,gpfs_pool,gpfs_import,gpfs_disks_up}.sh`，`gpfs_cluster.sh` 加 `add` / `remove`，`gpfs_fs.sh` 加 `add` / `remove` / `rebalance`，`storage_lib.sh` 的共享池函数，`report_rc.sh` 的 `shared_pool_report` 与 `instance_pools`，`source_migration.sh` / `finish_source_migration.sh` 认共享盘
  - 界面：创建向导整页 `StorageClusterCreate.vue`（类型与方式、软件包、节点与磁盘、参数、预检、确认；导入只有类型、节点、参数、确认）、集群详情 `StorageClusterDetail.vue`（概览、节点、磁盘、文件系统、存储池、任务，加节点 / 加盘 / 移除 / 离线移除 / 重新均衡 / 删除或取消登记）、`SharedPoolModal.vue`、`StorageExpandModal.vue`；从预检弹窗抽出 `StorageHostPicker.vue`（选节点、角色、盘，三处共用）和 `StoragePrecheckResults.vue`（任务详情里部署和加节点的预检步骤也显示逐项结果）；存储池页加共享池入口、集群列、配额，存储池详情显示集群、配额与池级容量；迁移弹窗和迁移详情把共享盘显示为「共享，不复制」
- **与上文不同的地方**：
  1. **共享池的容量来自探测进程的 `df`**，不是健康看护的 `mmlsquota` / `mmdf`（看护是 S5）：建池时 `gpfs_pool.sh` 给文件系统打开 `--filesetdf`，有配额的 fileset 在 `df` 里显示配额。没设配额时 `df` 显示整个文件系统，所以有两种介质、池又没设配额时容量会偏大（测试环境只有一种介质）
  2. 池的状态机：建池请求在同一个事务里插入 `creating` 的池记录、算出 fileset 和入口目录；任务失败时池停在 `creating`、任务占着池槽等重试或中止，中止后池变 `error`；`error` 的池走同一个删池任务删除（每一步都先查有没有）
  3. 共享池清单：任务里的 `sync_pools` 一步（新增的 `OnlineOnly`：只在开始时在线的节点上跑，离线节点由每 5 分钟一次的 `sync_shared_pools.sh` 补上）。删池时先撤清单、再删 fileset，免得节点去探测正在消失的目录
  4. 探测进程不常驻：心跳每 30 秒拉起一次 `shared_pool_probe.sh`，状态文件和本地池放在一起（`run/pools/<池UUID>.state`，另有 `.shared` 标记），所以待启动列表的 `pool_state_ok` 不用改；进程活着却 2 分钟没结果判「探测卡住」。每台节点一次回调报全部共享池
  5. 删共享卷由任一能访问池的节点执行，`clear_volume` 回调不再要求来自 `volumes.hyper`；共享卷建出来之前是 `pending`，10 分钟没回调变 `error`，`error` 的卷删除时照样经节点删文件（文件可能已经建了）
  6. 准入按池：`PoolDriver.CapacityGroup` 给出共用容量的一组池（GPFS 是「同一文件系统同一 GPFS 存储池、都没设配额」），这组池行按 id 加锁、已分配合并计算
  7. `StorageBackend.TaskPlan` 改为接收 `StorageTaskScope`（节点、盘、离线标志）：任务带来的节点和盘标 `joining` / `claiming`，带走的标 `leaving` / `removing`，后端据此排步骤；`InitialAttrs` 接收已有的节点和盘，扩容时接着编故障组号；NSD 名改为沿用已存的名字、新盘接着最大序号编，删过盘后不会改名；部署时把各节点的 SSH 主机公钥记进节点 `attrs`，之后加节点时管理节点的 `known_hosts` 用它
  8. 移除节点：有盘先 `mmdeldisk`（离线移除带 `-p`），仲裁节点先 `mmchnode --nonquorum`，再 `mmdelnode`，最后那台节点 `stc_leave`（离线移除没有这一步）。离线移除要求节点离线并输入主机名；移除后剩下的布局仍要通过后端的角色规则（仲裁奇数、至少 3 个故障组）
  9. 「NSD 节点回来后它的盘被自动拉起」原属健康看护（§14.1、S5），S2 先单独做了：任务循环每分钟给每个托管 GPFS 集群的一台管理节点发 `gpfs_disks_up.sh`（后端可选接口 `storageClusterMaintainer`）
  10. 导入的集群：`gpfs_import.sh` 只检查不改；池是登记已有目录（`register_pool`、`unregister_pool`，不执行任何 `mm` 命令，克隆方式 `copy`）；删除就是取消登记（`forget`）。没有另一组节点，导入**没有在真实节点上测过**
  11. 系统盘放共享池的请求直接拒绝（S4）；迁移时本地盘不能换到共享池、共享盘不能换池
- **没做**：第二个文件系统（`create_fs` / `delete_fs`）、改角色（`change_roles`）、两种介质的放置规则（代码有，测试环境只有 HDD，没在真实节点上验证）、按 GPFS 存储池算容量（S5）、健康看护与告警（S5，只做了拉起 `down` 的盘）、Ceph（S3）、系统盘（S4）
- **测试**：
  - PostgreSQL（`services/storage_pool_shared_pg_test.go`、`storage_gpfs_ops_pg_test.go`）：建池全过程（参数校验、介质、池槽、离线节点被跳过、各节点的检查结果写进 `hyper_storage_pools`）、心跳上报（非成员被忽略）、共享卷的建 / 挂 / 扩 / 卸 / 删与池级准入（超分、用量 95%、失败的卷照样占容量）、带共享数据盘的迁移计划（共享盘不复制、不预留、目标够不到就拒绝、`ApplyMigrationPlan` 不给共享卷写 `hyper`）、配额任务、删池（有卷拒绝、先撤清单再删 fileset）、建池失败中止后再删、维护规则；加盘、加节点（新主机的预检、全体 SSH 信任、`known_hosts` 带上旧成员记下的公钥、故障组 4）、移除盘（`nsddevices` 留下的盘、擦掉的盘）、移除节点（实例在用池时拒绝、唯一管理节点拒绝、离线移除要输入主机名、剩两个故障组拒绝）、重新均衡、每分钟的拉盘命令；导入、登记与取消登记目录、取消登记集群。原有的 PostgreSQL 测试、WSL 端到端测试全部通过
  - WSL（`gpfs-spike/stc-test3.sh`，31 项，tmpfs 冒充 GPFS 文件系统、`fs_type` 给 `tmpfs`）：清单的校验与写入、探测（缺标记、类型不对、就绪、卡住）、心跳只在变化或满 5 分钟时上报、建卷（不覆盖已有文件、路径必须是这个卷的、`..`、根目录 `/`、坏的驱动名）、离线与在线扩容（假 `virsh`）、缩小被拒并报实际大小、挂载生成的磁盘 XML（`cache='none' error_policy='stop'`）、删卷只删这个卷的文件、池没挂上时不在底下的根文件系统写任何东西、`stc_pools.sh` 当场探测、清单里没了的池停探测、待启动列表把共享盘归到对应的池、`register` 拒绝不在 GPFS 上的目录。原有 43 项照常通过
  - 界面（会话临时目录 `pw/stc-ui4.js`，全部接口在浏览器里模拟，39 项）：集群列表、向导部署全流程（类型卡片、Ceph 与纠删码标「后续版本」、默认角色、别的集群的 NSD 不可选、预检通过才能下一步、提交的内容）、向导导入全流程、集群详情各标签与操作（重新均衡、加盘只列成员、加节点只列非成员、离线移除要输入主机名、建共享池、改配额、删除要输入集群名）、存储池页的共享池行与详情；1440 / 1280 / 1024 无横向溢出，无页面错误
- **部署要点**：clapi 与 cpgateway 一起（白名单）；AutoMigrate 给 `storage_pools` 加列；三台节点同步 `scripts/kvm/` 的上述新脚本（`*_volume_shared.sh`、`sync_shared_pools.sh`、`shared_pool_probe.sh` 要可执行位）、`storage/` 下的新脚本与 `drivers/` 目录、`storage_lib.sh`、`report_rc.sh`、`source_migration.sh`、`finish_source_migration.sh`。真实节点上的验收要保留一个 GPFS 集群和几台测试云服务器（要用户同意）

**S2 真实节点验收**（2026-10-02 晚，用户同意后执行；work-01 / 02 / 03，24.04.5，内核 6.8.0-146）：

- **环境**：本地未提交的代码叠加到 work-01（91 个文件，备份 `/root/s2b-overlay-backup-20261002-2156.tar`，数据库 `/root/db-before-s2b-overlay-20261002-2156.sql`），重建 clapi、cpgateway；三台同步 37 个脚本（备份 `/root/s2b-scripts-backup-<日期>.tar`）。经接口重新部署 `gpfs1`（7 分钟，NSD `cl2h1d1`–`cl2h3d1`）；测试用 cirros 镜像、规格 `s2-tiny`、VPC `s2vpc`（内部子网 192.168.230.0/24）、三台云服务器 `s2-01`–`s2-03` 各在一台节点上，进云服务器经节点上 VPC 路由器 netns 里的 `sshpass`（三台节点为此装了 `sshpass`）。脚本在 work-01：`/root/cl-s2-{env,pool,vol1,unmount,disks}.sh`、`/root/cl-s2-vol-lib.sh`
- **存储池**：建池 30 秒（`create_pool` 20 秒 + `sync_pools` 10 秒）；fileset `cl_57480e44`、配额 50 GB（`mmlsquota` 52428800 KB）、标记文件、三台节点的池清单与探测状态都对，`df` 显示 50G（`--filesetdf`），三台节点行 `ready`；改配额 50→60 GB 的任务 20 秒，`mmlsquota` / `df` 立即是 60G，池记录的容量在下一次上报后更新；不设配额的池容量是整个文件系统（5.5 TB）；有卷的池删除返回 409，空池删除后 fileset 消失
- **数据卷**：建卷 2.2 秒（`pending` → `available`）；挂到 work-01 的 s2-01 写 32 MiB 随机数据，卸载后挂到 work-02 的 s2-02 读出 MD5 一致；磁盘 XML 是 `cache='none' error_policy='stop'`、源在 `/gpfs/fs1/cl_…/volumes/`；在线扩容 1→2 GB 虚拟机不重启、立即看到 2 GiB；离线扩容 2→3 GB 后挂到 work-03 的 s2-03，容量 3 GiB、数据一致；删卷 202，记录与文件都没了
- **迁移**：带这块 3 GB 共享盘把 s2-03 从 work-03 热迁移到 work-01，9 秒完成；计划里共享盘标 `shared`，传输总量 1.6 GB（本地系统盘 + 内存，不含共享盘），卷文件 inode 不变，虚拟机内每 0.2 秒一次的时间戳没有整秒缺口，数据一致
- **不可用**：work-02 上 `mmumount fs1` 后 31 秒节点行变 `unavailable`（原因「不在 gpfs 文件系统上」），池可用节点 2/3；往 s2-02 挂卷返回 400（122002「gp1 在 work-02 上不可用」）；这期间新建卷由别的节点落盘；work-02 根文件系统上的 `/gpfs/fs1` 一直是空目录；重新挂载 26 秒后恢复。把池的标记文件改名后 46 秒三台都 `unavailable`，建卷返回 400「没有在线节点能访问 gp1」、池里没有新文件，已挂盘的云服务器照常读写；改回 30 秒后恢复
- **集群操作**（每台只有一块数据盘，用 20 GB 回环文件当新盘，临时打开 `storage_allow_loop`，测完已清掉）：三块一起加盘 67 秒（NSD `cl2h1d2`–`cl2h3d2` 接着编号，故障组与节点对应）；重新均衡 2 分钟，新盘分到数据；移除盘每块约 70 秒（`mmdeldisk` 先迁数据，`nsddevices` 少一行，盘擦净后扫描为 `free`）；移除节点 work-03、移除 work-03 唯一的盘都被拒绝（法定节点会变偶数 / 失败组只剩 2 个），导入成员节点上的集群被拒绝（已在另一个 GPFS 集群里）。操作期间挂着 gv1 的云服务器数据一直完好
- **界面**（本机 5173 代理到 work-01 真实数据，只放行 GET，`pw/stc-ui-live.js`）：集群列表、集群详情五个标签、加盘 / 加节点弹窗、存储池列表与共享池弹窗、池详情、迁移详情、云硬盘列表、创建向导，1440 / 1024 共 40 项通过，无横向溢出、无页面错误
- **验收中发现并修掉的问题**：
  1. **装了 GPFS 的节点上新盘一律 `unknown_member`，加盘根本加不进去**（本地测试里盘的状态是直接写成 `free` 的）：按 §6.4「实际的做法」改 `classify_disk`、`wipe_disk`；同时补上 v2 NSD 的分区类型识别
  2. **重新均衡必然失败**：`gpfs_fs.sh` 的 `do_rebalance` 取错参数，把整段输入 JSON 当文件系统名传给 `mmrestripefs`；修好后对失败任务「重试」成功（顺带验证了重试）
  3. **集群列表、集群详情的「已分配」写死为 0**：集群汇总加 `allocated_bytes`（集群各池卷容量之和，与池级准入同一口径），集群详情的存储池标签逐池给出；文件系统一行不显示已分配
  4. 布局检查的错误消息里错误码重复（「Code 123021: Without work-03: Code 123021: …」），改为只取内层消息；池详情的「配额（GB）」标签与值「60 GB」单位重复
  5. **与 S2 无关的既有问题：配了 S3 时导入镜像永远停在「创建中」**——上传协程在建镜像的事务提交之前就去读镜像记录，读不到就放弃（`S3 upload: failed to load image N: record not found`），改为事务提交之后才启动上传协程（`services/image.go`）
- **整机重启 work-02**（2026-10-03 凌晨，用户同意后执行；s2-02 在 work-02 上、挂着 gp1 的 gv1；监控脚本 work-01 `/root/cl-s2-reboot-mon.sh`，记录 `/root/cl-s2-reboot.log`）：+50 秒 work-02 断开，+57 秒 clapi 判它离线；+197 秒 SSH 回来，此时 GPFS 还没起来，s2-02 没被启动；+213 秒第一次心跳把 s2-02 放进待启动列表（`shut_off` / `storage_pending`），这时 GPFS 已经由 `mmautoload` 拉起并挂上；+245 秒 work-02 上 gp1 的池行变 `ready`，同一轮 s2-02 从待启动列表启动；+253 秒 `running`；+285 秒 work-02 的 NSD 从 `recovering` 回到 `up`。虚拟机内 gv1 的数据 MD5 一致、容量 3 GiB。集群这期间保持 2/3 法定节点，work-01 / work-03 上的文件系统一直可用。**NSD 这次是 GPFS 自己的自动恢复拉起来的**（`restripeOnDiskFailure=yes` → `mmcommon recoverFailedDisk` → `tschdisk fs1 start -a`），不是我们的看护——见下一条
- **重启后发现并修掉的问题**：
  6. **每分钟拉起掉线 NSD 的看护从来没起过作用**：`gpfs_disks_up.sh` 用 `mmlsdisk <fs> -e -Y` 数掉线的盘，而 GPFS 不允许 `-e` 与 `-Y` 同时用（报错后输出为空），所以永远数出 0 块。改为 `mmlsdisk <fs> -Y` 按 `availability` 一列数 `down` / `unrecovered`（`recovering` 是正在拉、`suspended` 是管理员有意停的，都不算）。新增 WSL 测试 `gpfs-spike/stc-test4.sh`（9 项，假的 `mmlsdisk` 和真的一样拒绝 `-e -Y`，把旧写法放回去时 4 项失败）。真实集群上手工 `mmchdisk fs1 stop -d cl2h3d1` 后 10 秒看护就发出 `mmchdisk fs1 start -a`，90 秒后盘回到 `up`（GPFS 自动恢复不管管理员停掉的盘，所以这次确实是看护拉的）
  7. 重启后第一次心跳把池行的原因报成「probe stuck: no result for 2 minutes」（状态文件是重启前留下的），改为状态早于本次开机时报「not checked yet since the host started」（`report_rc.sh` 的 `shared_pool_report`，stc-test3 加了一项，共 32 项）
- **界面上传与界面部署**（2026-10-03，用户同意后执行；先删掉 gp1、gpfs1（卸载软件包）、已上传的安装包和三台节点上缓存的安装包，保证部署时真的重新下载；脚本在会话临时目录 `pw/stc-ui-{upload,deploy,retry}.js`、work-01 `/root/cl-s2-clean.sh`、`/root/cl-s2-netcut.sh`）：
  - **上传**：本机界面（5173 代理到 work-01）上传 6.0.0.2 安装包（1.7 GB，203 片），约 3 MB/s。传到第 51 片（25%）时让浏览器断网，4 秒后弹窗报错；10 秒后恢复，按钮变成「继续上传」、提示「从第 52 / 203 片之后接着传」（第 52 片在断网那一刻已经存进服务端，只是回应丢了），恢复后发的第一片是第 53 片、还是同一个包；全程 570 秒，服务端校验后「可用」，SHA-256 与原文件一致，版本 / 纠删码版 / ubuntu22·24 都识别对，许可证（约 2 万字）在界面上接受
  - **部署**：创建向导选这个包、三台节点（默认角色）各选 sdb，真实预检 12 秒通过，提交后跳到集群详情的任务标签。work-03 进入「下载软件包」这一步后，从 work-01 经公网地址在 work-03 上 `tc netem` 让 bond0 丢 100% 的包、7 分钟后自动恢复（看到 running 时它已下到 92%、在校验 SHA-256）：+43 秒平台判 work-03 离线，+303 秒这一步以「the host is offline」失败、任务停住；恢复后 work-03 重新上线，在界面任务详情页点「重试」，只重跑 work-03 的那次（8 秒，断网期间本地已经下完、校验通过，用了缓存），重试后 6.6 分钟部署完成，集群 `ready`、NSD `cl3h1d1`–`cl3h3d1`、三台挂载、跨节点读写正常
- **这一轮发现并修掉的问题**：
  8. **同一个上传弹窗里中断后再点「上传」会新建一个包、从头传**：续传只认页面列表里已有的未完成包，而列表在弹窗里失败时不会刷新，旧包要等 24 小时才清掉。改为弹窗记住自己正在传的包，同一个文件再提交时先向服务端取最新进度、从下一片接着传；关掉弹窗时刷新列表。断网时原来只显示 axios 的「Network Error」，改为「网络中断，已上传的部分保留着：网络恢复后点『继续上传』从断点接着传」。模拟测试 `pw/stc-ui-upload-mock.js`（4 项：第 3 片连断 3 次、只建一个包、恢复后从第 4 片接着传），原有的软件包页测试 16 项照常通过
  9. **节点下载软件包没有停滞超时**：`stc_fetch.sh` 的 `curl` 只有 `--retry`，网络断开时会挂在 TCP 重传上十几分钟，改为 `--connect-timeout 30 --speed-limit 1024 --speed-time 60`（与 `create_image.sh` 一致）
- **回归用例与补测**（2026-10-03）：S1、S2 的验收写成了回归用例 `test-items/TC-20-共享存储.md`（SHS-01–16，含 15 条历史缺陷回归点），执行记录 `test-items/runs/2026-10-03-0778bb2a+共享存储.md`；部署文档新增 `docs/deployment/09-shared-storage.md`，使用指南 `docs/guide/volumes.md` 重写（原内容还写着 WDS 时代的快照与 QoS）。在界面部署的新集群上按用例补测了 SHS-05–09、SHS-15，全部通过，又发现并修了两处：
  10. **共享池设为默认池后，所有不指定池的云服务器创建都被拒**：系统盘经 `Resolve(nil)` 取默认池，而默认池对数据盘、系统盘是同一个。新增 `StoragePoolAdmin.ResolveBoot`：没指定池、默认池又是共享池时，系统盘退回内置池（S4 之前），默认池对数据盘照常生效；PostgreSQL 测试在事务里改默认池再回滚
  11. 创建云服务器的「系统盘存储池」下拉列出共享池，选了必然 400：前端过滤掉共享池
- **没测**：加节点（没有第 4 台）、移除节点 / 离线移除（三台时被布局规则拒绝）；导入外部集群（要另一组不归 CloudLand 管的节点）；两种介质

### S3：Ceph（托管、外部导入、存储池与数据卷）

**范围**：§8；Ceph 后端的其余方法与 RBD 池驱动（§4.5）；§9 中 RBD 驱动的数据卷部分（同 S2，包括探测框架的 Ceph 检查与开机待启动列表）；带 RBD 数据盘的迁移（§10）；界面的 Ceph 部分。

**验收**（WSL 沙箱先过，再上 work-x，等用户同意）：

- 在界面上部署三节点集群；节点上没有 cephadm 自带的监控容器；节点上没被认领的空闲盘没有被 cephadm 占用；`osd_memory_target_autotune` 是关的
- 建 RBD 池、挂载、跨节点换挂、扩容、删卷，与 S2 相同的验收项
- 有云服务器开着某块盘时删卷被拒绝，原因里有占用者
- 池配额写满时的表现与 §9.5 一致
- 移除 OSD、移除主机、离线移除主机、删除集群后节点干净
- 导入外部集群：用 WSL 沙箱里的单节点 Ceph 作为被导入方（网络可达时），或另一组节点

**S3 实施记录**（2026-10-03，未提交；同日叠加到 work-x 做完真实节点验收，见本节最后「S3 真实节点验收」）：

- **代码**：
  - clapi：`services/storage_backend_ceph.go`（参数与导入参数、导入时把客户端密钥分进 `secrets`、`cephInfoOf`、`PoolSetup`）、`storage_backend_ceph_tasks.go`（部署 11 步、删除、导入、加节点 / 加盘 / 移除盘 / 移除节点的步骤与输入、Done 钩子、收尾）、`storage_backend_ceph_pools.go`（RBD 驱动 `rbdDriver`、池任务）；`storage_backend_gpfs_tasks.go` 的 SSH 信任输入改成可按角色给出（`storageTrustInputFrom`，Ceph 用管理节点与 mgr 节点）；`storage_cluster_import.go` 支持后端把导入参数里的凭据分出去（可选接口 `storageImportSecrets`）；集群接口加 `cluster_ref`
  - 节点：`storage/ceph_install.sh`、`ceph_cluster.sh`（bootstrap / add_hosts / configure / create_osds / remove_osds / remove_host / teardown）、`ceph_client.sh`（setup / import / remove）、`ceph_pool.sh`（create / quota / delete / register / unregister）、`drivers/ceph_rbd.sh`、`backends/ceph.sh`（`backend_existing`、`backend_disk_path`、`backend_disks_resolved`、`backend_leave`、`backend_forget`）；`stc_resolve_disks.sh` 调新钩子 `backend_disk_path`，`stc_forget.sh` 调 `backend_forget`；`source_migration.sh`、`resize_volume_shared.sh`、`report_rc.sh` 认 RBD 盘（下面第 10 条）
  - 界面：向导里 Ceph 的部署参数与导入参数（密钥是密码框，确认页打码）、按存储类型的节点与导入说明；共享池弹窗按类型（Ceph 没有 inode 上限，导入的 Ceph 集群填 RBD 池名）；集群概览的「集群标识」；三种语言的文案，步骤名 `create_pool` / `delete_pool` 改成不带 fileset 的说法
- **与上文（§8）不同的地方**：
  1. **版本**：每台节点装自己发行版的 `ceph-common`（24.04 是 19.2.3，26.04 是 20.2.0），**一个集群的节点必须是同一个发行版**（预检步骤的 Done 钩子比较 `os`，加节点时和已记录的节点比）；没有做「混用时改用 download.ceph.com」。容器镜像默认 `quay.io/ceph/ceph:v<节点 ceph-common 的版本>`，**比节点新的镜像安装步骤拒绝**：探路时 cephadm 打包的默认镜像 `:v20` 是 20.2.4，它生成的密钥（`AgD…`，新的密钥类型）20.2.0 的客户端读不出（`Malformed input`），所以镜像必须钉到节点客户端的版本（V10）
  2. 步骤合并：`pull_image` 并进 `install`（守护进程节点才拉镜像，只当客户端的节点只装 `ceph-common qemu-block-extra`），`place_daemons` 并进 `add_hosts`（先按标签应用 mon / mgr / crash 的放置，再加主机，等 mon 法定人数与 active mgr）。共 11 步
  3. bootstrap：加 `--orphan-initial-daemons`（不建 cephadm 默认的「5 个 mon、2 个 mgr」放置，由 `add_hosts` 按标签放）、`--skip-pull`、`--allow-fqdn-hostname`，`--output-dir` 放在 `/var/lib/ceph/<fsid>/bootstrap`（不写 `/etc/ceph`）。cephadm 往 `authorized_keys` 写的那一行公钥不带限制，与公钥文件内容完全相同，bootstrap 后按整行删掉，留下 `ssh_trust` 写的带 `from=` 的那行（V17）。管理命令一律用 cephadm 在 `_admin` 主机上维护的 `/var/lib/ceph/<fsid>/config/ceph.conf` 与 admin keyring（cephadm 仍会写 `/etc/ceph/ceph.conf`，平台不用它）
  4. **客户端权限**：托管集群上客户端用户 `client.cloudland` 是**不限池**的 `profile rbd`（集群归 CloudLand，上面的池都是 CloudLand 的），省掉了建池 / 删池时改权限这一步；导入的集群按对方建的权限。`configure` 没有启用 mgr 的 prometheus 模块（S5），没有设 `auth_allow_insecure_global_id_reclaim`；1 副本（测试布局）时打开 `mon_allow_pool_size_one`（不开时 `pool set size 1` 被拒）并关掉 `mon_warn_on_pool_no_redundancy`
  5. **客户端配置由平台直接写**（`fsid` 与 `mon_host`，加 `[client.<用户>] keyring = …`），不用 `generate-minimal-conf`（客户端节点没有管理员权限）；mon 地址取 mon map 里 v2 地址的 IP，客户端两种协议都试。导入时还装 `ceph-common qemu-block-extra`（没装的话），写完配置用客户端身份 `ceph fsid` 核对，对不上就删掉刚写的东西
  6. **磁盘 XML 用 `<config file='/etc/ceph/<集群>.conf'/>`**，不写 mon 列表，池参数里也不存 mon：mon 变了只要改各节点的配置文件（V12，libvirt 12 / QEMU 10.2 实测可用）。`auth` 写在 `source` 里，secret 用 UUID 引用
  7. **建 OSD**：刚加入的主机没有设备清单，`daemon add osd` 报 `No devices found`，所以先 `orch device ls --refresh` 并等到这台主机有清单；19.2 起 `daemon add osd` 按清单校验设备，逻辑卷不在清单里（`is not found on host`），这时带 `--skip-validation` 再试（V11）。回环盘由 `resolve_disks` 里的后端钩子先建成卷组 `clceph-<集群前 8 位>-<盘 ID 的哈希>` 上的逻辑卷 `osd`（每次都 `vgchange -ay`，重启后要重新激活），ceph-volume 收这个逻辑卷；`resolve_disks` 的结果里 `path` 是逻辑卷、`name` 仍是盘的内核名。OSD 编号按 `osd metadata` 的 `hostname` 与 `devices`（逻辑卷报的是底层的 `loop1`）找，所以可以重入。每块盘 2–3 分钟
  8. **容量**：池探测用客户端身份 `ceph df`（`profile rbd` 能执行，V14），容量 = `stored + max_avail`（`max_avail` 已经按副本数和最满的 OSD 算过），有配额时不超过配额。没设配额、放置规则相同的池共用容量（`CapacityGroup` = `ceph/<集群>/<规则>`）；导入的池各算各的。共用的池各自报「自己存的 + 还能存的」，同组的别的池有数据时容量偏小（偏保守）。集群一级的容量（列表的容量列）Ceph 没有，等 S5 的看护
  9. 删池：标记对象必须是这个池的、`rbd ls` 与 `rbd trash ls` 都为空才删；临时打开 `mon_allow_pool_delete`。基础副本的清理属于 S4
  10. 通用脚本里原来只认文件盘的三处：`source_migration.sh` 先在文件盘列表里找计划里的每块盘（RBD 盘不是文件，迁移直接失败）→ 共享盘在查找前跳过；`resize_volume_shared.sh` 在线扩容用卷路径做 `virsh blockresize` 的参数 → 改用 `disk-<卷>.xml` 里的设备名；`report_rc.sh` 的 `instance_pools` 只认 `source/@file` → 加上 `protocol='rbd'` 的盘（按 RBD 池名与配置文件对应到池；本机清单里没有的池按「不可用」，云服务器等着）
  11. Ubuntu 26.04 的 Rust 版 coreutils `install -o 167` 不认没有对应用户的数字 uid，cephadm 建守护进程目录时失败（`invalid user: '167'`）：安装步骤在没有 uid 167 的用户时建系统用户 `ceph-ctr`（24.04 不受影响）
  12. 删除集群：`teardown` 只在管理节点上停掉编排器（`mgr module disable cephadm`），每台节点的 `stc_leave` 里 `backend_leave` 做 `cephadm rm-cluster --force --zap-osds`、删 `/etc/ceph` 里属于这个集群的文件、客户端配置与 secret、回环盘的卷组，再擦盘
- **没做**：加 mon 节点后已有节点的客户端配置不更新 `mon_host`（旧 mon 在就能用，客户端会从 mon map 学到新的；要更新得重跑客户端配置）、换坏盘（`osd rm --replace`）、改角色、`add_clients` / `remove_clients` 单独的任务（加节点时选「客户端」角色即可）、mgr prometheus 与健康看护（S5）、blocklist（S6）、混用 24.04 与 26.04、私有镜像仓库的口令
- **测试**：
  - 探路（WSL，`gpfs-spike/ceph/spike1.sh`、`spike2.sh`）：上面第 1、3、4、7、8 条的结论都来自这里；WSL 的根挂载是 private，ceph-volume 容器要 `rslave`，测试前 `mount --make-rshared /`（真实节点默认 shared）
  - PostgreSQL（`services/storage_ceph_pg_test.go`）：部署每一步的输入（发行版不一致时预检失败、客户端节点只装客户端、SSH 信任的来源、bootstrap / 加主机 / 配置 / 建 OSD 的输入、客户端配置带解密后的密钥）、配置步骤把密钥加密保存并从运行结果里删掉、收尾后的集群 / 盘 / 节点；建池（参数校验、介质的副本数检查、规则名、驱动参数、清单内容）、RBD 卷的命令（`image` 而不是 `path`）、改配额、删池；加盘（回环盘的逻辑卷与内核名）、移除盘、离线移除客户端节点；删除；导入（密钥不进 `params`、mon 地址补端口、登记与重复登记、取消登记、forget 带存储类型）
  - WSL 端到端（`rpcs/storage_ceph_wsl_test.go`，`CEPH_WSL_E2E=1`）：经 clapi 的任务引擎在 WSL 里部署单节点 Ceph（测试布局，OSD 在 12 GiB 回环盘的逻辑卷上），建池、经共享卷脚本建卷，把卷挂到一个 TCG 虚拟机（真实 librbd、libvirt 12、QEMU 10.2）、看到 1 个 watcher、在线扩容到 2 GiB、打开着时删卷被拒、销毁虚拟机后删卷，删池，删除集群后节点上没有集群目录、容器、卷组、客户端配置、secret、信任行。部署 2 分 42 秒（镜像已在本地；第一次没缓存镜像时 4 分 38 秒），整个测试 248 秒；池容量 12.2 GB（12 GiB 的 OSD）
  - 界面（会话临时目录 `pw/stc-ui-ceph.js`，全部接口模拟，41 项）：Ceph 部署与导入的向导（默认角色、参数校验、测试布局、预检与提交内容、密钥打码）、Ceph 集群详情（没有文件系统标签与重新均衡、集群标识、OSD 名）、两种 Ceph 集群上的共享池弹窗
- **部署要点**：clapi（新的任务与驱动）；三台节点同步 `scripts/kvm/storage/` 的新脚本（`ceph_*.sh` 要可执行位）、`drivers/ceph_rbd.sh`、`backends/ceph.sh`、`stc_resolve_disks.sh`、`stc_forget.sh`、`source_migration.sh`、`resize_volume_shared.sh`、`report_rc.sh`；前端随 nginx。没有表结构变化

**S3 真实节点验收**（2026-10-03，用户同意在 work-x 上装 Ceph（D3），测试盘按建议用回环盘：三台的 `sdb` 被 `gpfs1` 占着；work-01 / 02 / 03，24.04.5，发行版的 Ceph 19.2.3）。用例 `test-items/TC-21-Ceph存储.md`，执行记录 `test-items/runs/2026-10-03-0778bb2a+Ceph.md`：

- **通过**：三台部署（3 个 mon、2 个 mgr、每台一个 OSD，从零到就绪 11 分 38 秒，其中建 3 个 OSD 5 分 23 秒）；不拉起监控容器、空闲盘不被占用、autotune 关闭、集群公钥只有带 `from=` 的那一行；RBD 池（30 秒，`cl-hdd` 规则、3 副本 `min_size 2`）；RBD 卷的建、跨节点换挂、在线与离线扩容；打开着时删卷被拒并说明 watcher；带 GPFS 盘与 RBD 盘的热迁移 10 秒、两块共享盘都不复制；池不可用（单台客户端配置缺失 20 秒、标记对象缺失 36 秒判出）；加盘约 2 分钟、移除盘 51 秒；把 ceph1 当外部集群导入、登记、使用、取消登记、forget；比节点新的镜像被拒；删除集群（带卸载软件包 91 秒，节点干净、盘擦净）；界面模拟 41 项、真实数据只读 17 项
- **池配额写满**（V8 的 Ceph 一半）：Ceph 把池标成 `POOL_FULL`（`stored` 比配额多约 8%，配额是滞后执行的），**来宾的写入阻塞、不返回错误**，云服务器保持 running，`error_policy` 不触发；30–50 秒内各节点的池探测（写探测对象超时）判「不可用」，挡住新建卷与挂载；配额改大后阻塞的写入立即完成。与 §9.5 的预期一致（Ceph 是 I/O 卡住、不是暂停），实测补充了两点：配额超出约 8% 才生效，探测会把写满的池判成不可用
- **修了 3 个问题**（第 9–11 条回归点）：
  9. Ubuntu 24.04 的 `cephadm` 19.2.3 包没有声明依赖 `python3-jinja2`，而 cephadm 要 import 它，bootstrap 一启动就失败（26.04 的沙箱碰巧装着）→ 安装步骤给守护进程节点装 `python3-jinja2`
  10. 19.2 的设备清单是 `ceph-volume inventory --filter-for-batch`，只列可用的盘；这三台的系统盘、GPFS 盘、回环盘都不可用，清单永远是空的，我写的「等这台主机有设备清单」永远等不到。19.2 的 `daemon add osd` 也根本不按清单校验（没有 `--skip-validation` 参数，20.2 才有）→ 不等清单，直接建；只在 20.2 报「No devices found for host」「is not found on host」时带 `--skip-validation` 重试
  11. 中止的部署删不掉：删除时只在「已是成员」的管理节点里选执行者，中止的部署里节点都还是 `joining` → 删除集群时不按状态过滤（与 GPFS 一样），PostgreSQL 测试 `TestStorageCephAbortedDeletePG`
- **回到 20.2 复测时又修了 1 条**（第 12 条回归点）：20.2 的 `orch daemon add osd` 每次都把那块盘存成**受管**的规格 `osd.default` 并立即应用（cephadm 判断规格是否已存在时拿 `default` 去比 `osd.default`，永远不相等，所以每次都覆盖），按代码这块盘移除、清掉之后后台会在上面重新建 OSD（没有实测）→ 建完 OSD 后 `orch set-unmanaged osd.default`；19.2 没有这个规格（`daemon add osd` 建的 OSD 归在不受管的 `osd` 下），命令失败、无害。验证：WSL 端到端测试（20.2）通过两次，保留集群那次 `osd.default` 是 `<unmanaged>`；work-x 上 CEPH-09 加盘、移除盘照常。复测中第一次失败**不是代码问题**：WSL 里残留了节点脚本测试 `stc-test2.sh` 建的假 OSD 卷（标签不全，没有 `ceph.type`），20.2 的 `ceph-volume lvm list` 遍历所有逻辑卷、碰到它就崩溃，cephadm 于是看不到刚建的 OSD、不部署守护进程；该测试已改为补全标签并在退出时删掉卷组、卸下回环设备。**在一台机器上同时跑 Ceph 与造假 Ceph 卷的测试要注意这一点**
- **整机重启（CEPH-12，用户另外同意后做的，修了第 13 条）**：真实重启 work-03（OSD + mon）两次，上面的 s2-03 挂 RBD 卷，work-01 上 s2-01 的 RBD 卷由来宾里的循环每 0.2 秒同步写一次。开机约 2.5 分钟 SSH 恢复，云服务器先进待启动列表，池探测通过后立即启动（这时本机的 OSD 还没回来，集群靠其余两份副本照常可用，所以不用等本机 OSD），约 3 分 20 秒 `HEALTH_OK`；数据完好，s2-01 的写入 0 失败
  13. **第一次重启时 s2-01 的写入在 work-03 关机那一刻卡了 13.4 秒**：systemd 关机时把一台机器上的 mon、OSD 一起停，osd.2 报告下线的消息发给了本机这个同时在停的 mon（16 毫秒之差），丢了；osd.2 等满 5 秒确认超时才停，集群靠心跳超时才把它标 down。Ceph 软件包的 systemd 单元把 OSD 排在 mon 之后正是为此，cephadm 写的单元没有 → 安装步骤给守护进程节点写 drop-in `ceph-<集群>@.service.d/cloudland-order.conf`（`After=ceph-<集群>@mon.%l.service`，模板级，以后加盘建的 OSD 自动生效；mon 单元读到这条自依赖时 systemd 丢掉并记一行警告），删除集群时删掉。用停 target（与关机同一套停止顺序）对比：卡顿 13.5 → 1.6 秒，osd.2 被标 down 17.6 → 2.7 秒；修后再真实重启一次也是 1.6 秒。OSD 回来时 PG 重新 peering 还会让写入卡 1–4 秒，是 Ceph 本身的行为，没处理。计划内的维护仍建议先进维护模式（§8.5，S5）
  - 开机过程中心跳会报几秒 `paused`：是 libvirt 启动域时的「启动中」（连 RBD 那几秒），没有 RBD 盘的云服务器也有，S2 的重启里同样出现过，不是故障；要不要在上报时把「启动中」和真正的暂停分开，留到 S5
- **没测**：CEPH-10 加节点 / 移除节点 / 离线移除（没有第 4 台，三台都是 mon、移除任一台被布局规则拒绝）；两种介质（只有 HDD）
- **保留的环境**：`ceph1`（uuid `8ca8cbee-d927-4db9-8ea1-55ed48c3cbd0`）与池 `rp1`，上面没有卷；回环文件在 `/var/lib/cl-s3test/`，三台 `cloudrc.local` 有 `storage_allow_loop=true`。回环设备由测试环境的 `cl-s3test-loop.service` 开机挂回（产品里回环盘只用于测试，所以没做进平台）

**S1–S3 代码审查后的修复**（2026-10-03，`/code-review` 报了 15 条，逐条核实都成立，已改，同日部署到 work-x 实测，未提交；回归点 TC-20 第 16–26 条、TC-21 第 14–16 条，执行记录 `test-items/runs/2026-10-03-0778bb2a+代码审查修复.md`）。定下的做法：

- **任务引擎**：收尾（`Finish`）在保存点里跑，失败时它写的部分整体回滚；全部步骤成功、只有收尾失败的任务，`Retry` 只重跑收尾，不必中止。步骤定义找不到（升级改了步骤名）时任务失败、不 panic
- **节点上的作业**：`run/storage/jobs/` 与 `log/storage/` 一律 700，因为步骤的输入、结果、回调里有集群 SSH 私钥和客户端密钥；不改作业的 umask（作业还写 `/etc/ceph/<集群>.conf`，QEMU 要读）
- **`authorized_keys`**：一律经 `stc_lib.sh` 的 `stc_keys_update`（全机一把锁、`mktemp`、awk 过滤、原子 `mv`），不要再「过滤 → 固定临时文件 → cat 回去」：不同集群的任务会同时在一台机器上改它，那行 cland 公钥也在这个文件里
- **删除集群**：GPFS 的 teardown 与 `backend_leave` 只动 CloudLand 自己建的（成员标记 + 集群名），预检就失败的部署里节点可能属于别人的 GPFS 集群；删除前的检查（池数量、在跑或等重试的导入）放进 `Prepare`、锁住集群行之后做，建池也锁住集群行后再核对状态
- **RBD 删除**：只有 `No such file or directory` 才算镜像已不在，超时、集群连不上都是失败
- **接口**：GPFS 文件系统名按 `gpfs_fs.sh` 的规则校验；PATCH 存储池的配额要单独改（带别的字段时改任何东西之前就 400）；共享卷的建卷回调要来自池里的节点、挂载回调要来自云服务器所在的节点；登记 RBD 池的查重转义 LIKE 通配符；池列表的集群与已分配量按页一次查出
- **界面**：存储池编辑弹窗里状态与默认只在改了时才发，池不是启用 / 停用时状态锁住并说明原因
- **测试**：PostgreSQL 的 `TestStorageTaskPG`、`TestSharedPoolPG`、`TestStorageCephImportPG`、`TestStorageBackendParams`、接口层新增 `TestStoragePoolPatchPG`；WSL 节点脚本新增 `gpfs-spike/stc-test4.sh`（20 项）；本机界面 `pw/stc-ui-review.js`（8 项）；`stc-test1/2/3`、两个 WSL 端到端测试、原有界面模拟测试都重跑通过
- **没改的**：本地卷的挂载回调同样不核对上报节点（早就如此，而且迁移中 `instances.hyper` 可能还没更新，按执行节点记卷所在才是对的），这次不动

### S4：系统盘与共享迁移（两种驱动）

**范围**：§5.6 的 `base_image_storage_id`、§5.7；§9.6–§9.8；§10 的全共享迁移；界面的系统盘存储池选择、迁移弹窗的逐盘说明。

**验收**：

- 三台节点并发创建 10 台云服务器，用同一个在这个池里还没导入过的镜像：只导入一次基础副本，10 台都正常启动；导入期间这些节点的命令队列不被占住
- 克隆方式下系统盘的创建时间与镜像大小无关
- 全部磁盘都在共享池上的云服务器，热迁移时间与磁盘大小无关；GPFS 池上的 NVRAM 不变
- 删除镜像时如果还有云服务器在用，基础副本保留，最后一台删除后被清理
- 重装、救援、捕获镜像、删除云服务器在两种驱动上都正常；删除云服务器时节点离线，共享系统盘保留到节点恢复

### S5：运维完善

**范围**：§14 的健康看护、告警、指标与界面监控；完整日志（§6.2.6）；GPFS 的重新均衡、换盘；Ceph 的换盘；改角色；新节点自动加为客户端；孤儿对象对账（共享池里有文件 / 镜像但数据库没有记录的，只报告不删除）。

**验收**：§14.2 的每种告警都能触发、恢复时解除；池配额写满时告警按时触发。

### S6：升级、凭据轮换、宕机恢复

**范围**：§7.7、§8.7 的滚动升级；客户端密钥与集群 SSH 密钥的轮换；§11 的宕机恢复（先做 §11.4）。

**验收**：

- 断开一台节点的电源，确认隔离后它上面的云服务器在其他节点启动、数据完整；节点恢复后不会再启动已在别处恢复的云服务器，归属不变，残留的域定义被清理，之后才解除隔离
- 只断管理网、存储网正常：节点上用 iptables 只拦到 cland 的 5006 端口（GPFS 和 Ceph 都走内网地址，在 bond0 上整体断网会把存储网一起断掉，测不出这种情况）；恢复后旧 QEMU 写不了盘，对账把它清掉
- 一小时后（Ceph 黑名单默认的有效期）旧节点仍然写不了盘

### S7：后续

GPFS 纠删码（§7.9）、多集群远程挂载（一台节点访问多个 GPFS 集群、对接已有的存储集群）、SAN 共享 LUN 做 NSD。

---

## 17. 测试方案

### 17.1 环境

见 §2.5 与决策 D1–D3。在决定之前能做的：S1 的全部单元与 PostgreSQL 测试、节点脚本在 WSL 里的测试（已做）；S3 的 Ceph 脚本在 WSL 沙箱里开发。S2 的 GPFS 脚本只能写、不能在 WSL 里执行（V22）。

### 17.2 用例

- 新建 `test-items/TC-20-存储集群.md`：集群生命周期（部署、导入、加减节点与盘、离线移除、删除）、任务重试与中止、预检、许可证、失败注入
- 扩充 `TC-16`（存储池）：共享池的建删、可用性、准入、写满
- 扩充 `TC-07`（迁移）：混合与全共享的迁移矩阵
- 失败注入的做法：安装中断网（`tc qdisc add dev bond0 root netem loss 100%`，不要用 iptables，它拦不住 ARP）；只断管理网（节点上 iptables 拦到 cland 5006 的出向连接）；管理节点在任务中途离线；节点在任务中途重启；删除回环设备模拟坏盘；写满配额

### 17.3 性能基准

在同一台节点上对比本地 qcow2、GPFS qcow2、RBD raw：fio 的 4k 随机读写（队列深度 1 和 32）与 1M 顺序读写；云服务器从创建到能登录的时间（克隆与整盘复制，镜像 2 GB 与 20 GB）；热迁移耗时（本地复制与共享，磁盘 20 GB 与 200 GB）。在回环设备和 2 Gbit/s 网络上测出来的数只能比相对快慢，不能当产品指标。

### 17.4 约定

- 在 work-x 上安装、部署任何东西前先征得用户同意（CLAUDE.md）
- 对线上环境的界面测试按惯例兜底拦截所有写请求
- 保留的测试资源（`rb-vpc`、`local-hdd` 等）不动；测试用的回环设备、集群在测完后按记录清理

---

## 18. 待决策、待验证与风险

### 18.1 待决策（要用户定）

| # | 事项 | 建议 |
|---|---|---|
| D1 | ~~**GPFS 测试用的 Ubuntu 24.04 节点从哪来**~~ **已解决**（2026-10-02）：用户选择把 work-01 / 02 / 03 全部重装为 24.04（原有环境不保留）；CloudLand 要在上面重新部署，部署由用户决定 | — |
| D2 | **测试磁盘**：回环设备、腾出某台的 `local-hdd`，还是新机器的空盘 | 先用回环设备做功能，性能测试等新机器 |
| D3 | **在 work-x 上装 Ceph 做 S3 的验证**（会装 `cephadm`、拉镜像、起容器、建回环盘），以及部署 S1 做真实节点验证 | 先在 WSL 沙箱里做完，再在一台上跑单节点 |
| D4 | GPFS 正式使用的授权：手上是 ECE 版本，计费方式（按容量还是按盘）与是否覆盖副本模式的用法 | 和 IBM 确认，不影响开发 |
| D5 | GPFS 节点 `apt-mark hold` 内核（内核安全更新变成手工操作） | hold，节点详情页提示有待升级的内核 |
| D6 | 托管 GPFS 要求启用 S3 | 接受（私有化部署本来就带 MinIO） |
| D7 | `VPN_SECRET_KEY` 改成通用的名字（它已经不只给 VPN 用）。改名要同步改每个部署环境的 `.env` | 改，名字用 `CREDENTIAL_KEY` 这类不会和 `S3_SECRET_KEY`、`CPGATEWAY_SECRET_KEY` 混淆的；单独一个提交，与本方案无关。在定之前沿用原名 |
| D8 | Ceph 镜像从 quay.io 直接拉，还是要求私有仓库 | 默认直连，允许填私有仓库 |

### 18.2 待验证

| # | 事项 | 影响 | 在哪验证 |
|---|---|---|---|
| V1 | GPFS 在 24.04 节点上 `mmbuildgpl` 成功、`autoBuildGPL` 在内核变化后自动编译。**前一半已通过**（2026-10-02，work-01）：6.0.0.2 在 Ubuntu 24.04.5、内核 `6.8.0-146-generic`（比 IBM 测过的 139 新）上 `mmbuildgpl` 29 秒编过；三个模块按依赖顺序加载、卸载正常，内核无报错，只有「树外模块、未签名」的提示（安全启动是关的）。测完已卸载干净，包与日志留在 work-01 `/root/gpfs-v1/`。还没测：`autoBuildGPL` 换内核后自动编译、守护进程起来以后的运行 | 能否部署 | S0（24.04 节点） |
| V2 | 回环设备经 `nsddevices` 用户出口做 NSD；`tspreparedisk -s` 列出本机 NSD 的格式；NSD v2 格式的 GPT 分区类型 GUID；没装 GPFS 时怎么认出 NSD | 测试环境、磁盘扫描（§6.4） | S2（24.04 节点；WSL 跑不了，V22） |
| V3 | `mmcrcluster -r/-R` 用包装脚本、`adminMode=central` 下非管理节点能执行哪些读命令（`mmlscluster`、`mmlsquota`）；`mmhealth` 的输出格式 | SSH 密钥方案（§6.6）、外部集群（§7.8）、监控（附录 E） | S2（24.04 节点） |
| V4 | `mmclone`：父文件与克隆必须在同一个独立 fileset；qcow2 克隆后能否 `qemu-img resize`；有子克隆时能否删父文件 | 系统盘（§9.6） | S2 / S4（24.04 节点） |
| V5 | QEMU 的 OFD 镜像锁在 GPFS 上是否集群范围有效 | 单写入者的第二层（§12.1） | S4 |
| V6 | libvirt 12 对 GPFS 盘（`cache='none'`）、RBD 盘（`cache='writeback'`）热迁移是否报不安全。**RBD 已验证**（2026-10-03，work-x，24.04 的 libvirt 10.0）：带 RBD 盘（同时带 GPFS 盘）的热迁移 10 秒完成，没有报不安全 | 共享迁移（§10） | S2 / S3 |
| V7 | 动态属主（含迁移时）与 AppArmor 在 GPFS 路径和 RBD 盘上是否正常。**RBD 已验证**（2026-10-03）：热挂、在线扩容、热迁移都正常；libvirt 的 AppArmor 抽象本来就放行 `/etc/ceph/*.conf`，不用另加规则 | 能否启动 | S2 / S3 |
| V8 | fileset 配额写满时 `error_policy='stop'` 能否暂停云服务器；Ceph 池配额写满时 I/O 阻塞的具体表现。**Ceph 一半已验证**（2026-10-03）：池到配额约 108% 时 `POOL_FULL`，来宾写入阻塞、云服务器保持 running，各节点的探测 30–50 秒判池不可用；配额改大后写入立即完成 | 写满时的表现（§9.5） | S2 / S3 |
| V9 | `mmexpelnode` 持久生效、`-r` 解除；GPFS 被驱逐后，QEMU 还开着文件时文件系统能否重新挂载 | 隔离（§11.2） | S6 |
| V10 | Ubuntu 打包的 `cephadm` 20.2 与上游镜像搭配；用 docker 而不是 podman。**已验证**（2026-10-03，WSL）：能搭配、docker 可用，但镜像要钉到节点 `ceph-common` 的版本——打包的 cephadm 默认拉 `:v20`（20.2.4），它生成的新类型密钥 20.2.0 的客户端读不出（`Malformed input`）。实现里镜像默认取节点版本、更新的拒绝（§16 S3 实施记录第 1 条） | 能否部署 | S0（WSL 沙箱） |
| V11 | ceph-volume 收回环设备上的 LVM 逻辑卷；`ceph orch daemon add osd` 接受的设备写法。**已验证**（2026-10-03，WSL）：收 `/dev/<vg>/<lv>`，但 20.2 的 `daemon add osd` 按设备清单校验，逻辑卷要带 `--skip-validation`；刚加入的主机要先刷新清单 | 测试环境、建 OSD（§8.2） | S0（WSL 沙箱） |
| V12 | libvirt 的 RBD 磁盘支持 `<config file=...>`。**已验证**（2026-10-03，WSL，libvirt 12.0 / QEMU 10.2.1，真实 librbd）：带 `config file` 与 secret 的磁盘能热挂、在线扩容；2026-10-03 在 24.04 节点（libvirt 10.0 / QEMU 8.2）上也确认了，热迁移同样正常 | mon 变更后的域定义（§9.3） | S3 |
| V13 | 带快照的 RBD 镜像能否 `rbd rename`；克隆格式 v2 是否可用 | 基础副本（§9.6） | S0（WSL 沙箱）/ S4 |
| V14 | `ceph osd blocklist range add` 的可用版本、有效期参数；`profile rbd` 的客户端能否执行它和 `ceph health`。**后一半已验证**（2026-10-03，WSL）：`profile rbd` 的客户端能执行 `ceph health`、`ceph -s`、`ceph df`、`ceph fsid`、`osd pool get-quota`、`osd pool ls detail`，能读写池里的对象。`blocklist range` 没测 | 隔离（§11.2）、外部集群（§8.8） | S0（WSL 沙箱）/ S6 |
| V15 | 块大小（GPFS 4 MiB vs qcow2 2 MiB 簇）与性能 | 默认参数 | S2 |
| V16 | Ceph 云服务器 NVRAM 从模板重建后 UEFI 系统能否启动（宕机恢复） | §11.1 | S6 |
| V17 | cephadm bootstrap 是否往 `authorized_keys` 写不带限制的一行、能否改写；关掉 `osd_memory_target_autotune` 后 OSD 的实际内存。**前一半已验证**：会写，内容与公钥文件完全相同（按整行比较），bootstrap 后删掉即可，带 `from=` 的那行照常工作。OSD 实际内存没测 | §6.6、§6.7 | S0（WSL 沙箱） |
| V18 | 删除集群前 `ceph mgr module disable cephadm` 是否足以阻止守护进程被重新部署。**是**（2026-10-03，三台各删两次）：teardown 5 秒，各节点 `rm-cluster --zap-osds` 之后没有容器被重新拉起，节点干净 | §8.6 | S3 |
| V19 | 能否用安装包里的 `Public_Keys` 校验 deb 的签名 | 完整性（§6.1） | S2 |
| V20 | 24.04 节点上 `qemu-block-extra` 带不带 RBD 驱动。**带**（2026-10-01 核对三台节点：`block-rbd.so` 在） | §8.4 | S3 |
| V21 | 节点永久离线时 `mmdelnode`、`mmdeldisk -p` 的确切用法 | 离线移除（§7.5） | S2 |
| V22 | ~~WSL 沙箱里能否为 WSL 内核编出并加载 GPFS 模块~~ **否**（2026-10-01）：编得出、版本校验过得了，加载 `mmfslinux` 时 `jump_label` 致命错误、内核崩溃；WSL 没有嵌套虚拟化 | 开发方式（§2.5）：GPFS 只能在 24.04 节点上验证 | S0（已做） |

### 18.3 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| IBM 迟迟不支持 26.04，而 CloudLand 其他功能在 26.04 上迭代 | GPFS 节点长期停在 24.04，两套系统都要测 | 部署脚本、节点脚本保持两个版本都能用；CI 里加 24.04 的检查 |
| 超融合（存储与云服务器在同一批节点、共用 2 Gbit/s） | 存储复制流量与业务流量互相影响，重新均衡、恢复时尤其明显 | 推荐独立的存储网络（参数里填复制网段）；文档写明 |
| 存储守护进程与内置本地池共用根文件系统（§3.1） | 本地云服务器写满根文件系统会让 mon 停掉、整个 Ceph 集群卡住 | 预检检查分区空间；mon 节点建议单独分区或调低内置池上限 |
| 管理节点 / mgr 主机被攻破即可登录整个集群（§6.6）；A1 的共用 cland 密钥还在 | 每集群独立密钥在 A1 修好之前不增加实际的隔离 | A1 定方案时一并考虑 |
| 下发链路没有 TLS（A6） | 集群私钥、Ceph 密钥在管理网上明文 | 等 A6 |
| Ceph 写满阻塞全集群写入（§9.5） | 所有 RBD 云服务器卡住 | 不超分、容量未知时拒绝、80/90% 告警、`nearfull` 告警 |
| 任务中途失败留下半成品 | 节点上残留软件、进程、盘上数据 | 步骤可重入 + 重试；删除集群任务能清理任何阶段的半成品，离线节点回来后补做 |
| 存储软件自身的运维深度（GPFS 的 `mmfsck`、Ceph 的 PG 修复等） | 界面覆盖不了所有排障场景 | 不追求全覆盖：界面做日常操作，深度排障在管理节点上手工做，附录 D、F 给出常用命令 |

---

## 19. GPFS 端到端使用流程（管理员与用户视角）

本章把前文 GPFS 相关的设计按「谁在什么时候做什么」串起来，给产品、测试和写使用指南时对照。每一步标出所在阶段（§16）：S1 已实施，S2 起未实施。细节以正文对应章节为准。

### 19.1 两类人看到的东西

| | 系统管理员 | 普通成员（组织里的用户） |
|---|---|---|
| 能看到 | 软件包、存储集群（节点、磁盘、文件系统、任务、健康）、所有存储池 | 只有存储池：名称、类型（本地 / GPFS）、是否共享（§4.1） |
| 能做 | 部署、扩缩容、建池、维护、删除集群 | 建云硬盘、挂载、扩容、删卷、选系统盘放在哪个池 |

前提（管理员负责）：

- 计算节点是 Ubuntu 24.04（或 22.04）、6.8 GA 内核；26.04 不能做 GPFS 节点，客户端也不行（§2.2）
- 控制面配了对象存储（S3 / MinIO），安装包要放在里面（§6.1）
- 节点已注册进 CloudLand，做过一次磁盘扫描（§6.4）

### 19.2 管理员：搭建

**1. 上传安装包**（S2，§6.1）：侧边栏「存储 → 软件包」，上传 IBM 的安装包（约 1.7 GB）。

- 分片上传，有进度条；断网后从断点接着传。也可以填下载地址，由平台自己拉取
- 平台后台校验：算出 SHA-256 显示出来，供与 IBM 下载页核对；识别版本类型和支持的系统（22.04 / 24.04）
- 弹出许可证全文，勾选「我已阅读并接受」后这个包才能用于部署；接受人和时间进审计

**2. 节点预检**（S1 已实施，§6.5）：「存储 → 存储集群 → 节点预检」，选节点、角色、磁盘，平台在每台节点上逐项检查并给出通过 / 警告 / 不通过，只检查、不安装：

- 系统与内核是否在支持范围内（26.04 判为不通过）
- 内核头文件能否安装、安全启动是否关闭
- 时间是否同步；节点之间 1191 端口与 SSH 是否连通
- 磁盘是否空闲、身份能否核对
- `/var/mmfs` 所在分区是否有 10 GB 空闲、内存余量
- 节点上是否已有不归 CloudLand 管的 GPFS

**3. 创建向导**（S2，§13.3，整页、7 步）：

1. **类型**：选「GPFS 副本」（纠删码版灰显，标「后续版本」），或「导入外部 GPFS」
2. **软件**：选一个已接受许可证的安装包
3. **节点与角色**：列出在线节点，不在支持范围内的标红并说明原因。每台勾选角色（§5.2）：
   - **管理**：1–2 台，执行集群命令，必须同时是仲裁节点
   - **仲裁**：奇数台，至少 3 台
   - **NSD**：贡献磁盘
   - **客户端**：只挂载，不承担其他角色

   向导实时校验：仲裁数为奇数；有盘的节点（故障组）至少 3 个（§6.3）
4. **磁盘**：每台 NSD 节点的空闲盘，显示型号、大小、介质、扫描时间；有旧数据的盘要单独勾选「擦除」
5. **参数**（都有默认值，§7.2）：集群名、文件系统名（`fs1`）、块大小（4 MiB）、数据副本数（2 或 3）、pagepool（1 GiB）
6. **预检**：一键执行；有不通过的项不能继续，只有「系统不在支持范围」一项可以勾「仅用于测试」放行
7. **确认**：汇总将占用的盘、每台节点将预留的内存（pagepool + 1 GiB，从云服务器可用内存里扣掉，§6.7）；提交后跳到任务页

**4. 看部署过程**（§6.2、§7.2）：任务页按步骤和节点逐格显示进度与日志，运行中每 3 秒刷新。步骤为：

预检 → 加入 → 下载安装包 → 安装 → 编译内核模块 → SSH 信任 → 建集群 → 启动 → 核对磁盘 → 建 NSD → 建文件系统并挂载到 `/gpfs/fs1` → 收尾

- 三节点目标 30 分钟内完成（§16 S2 验收）
- 失败时任务停在失败的那一步，并占着这个集群，防止在半成品上做别的操作。管理员可以：
  - **重试**：只在失败的节点上重跑，成功过的节点不重复执行
  - **中止**：先结束节点上还在跑的作业，再释放集群
- 没有自动回滚；半成品用「删除集群」清理，它能处理任何阶段留下的半成品（§7.6）

**5. 建 CloudLand 存储池**（S2，§7.4）：集群详情「存储池」标签 → 新建，选文件系统、介质（有 SSD 和 HDD 时分开放）、配额。

- 平台自动建独立 fileset、生成放置规则、设配额、建池内目录，再让每台节点检查一遍能否访问
- 池从「创建中」变为「可用」，列表显示「可用节点 x/y」
- 托管文件系统上不要手工改放置规则，平台下次改池时会整体覆盖

**导入外部集群**（S2，§7.8）：节点已是别人集群的成员、文件系统已挂好时，填文件系统名和挂载点，再登记已有的 fileset 路径。平台只检查和使用，不执行任何 GPFS 管理命令。

### 19.3 管理员：日常维护

| 场景 | 怎么做 | 平台做的事 |
|---|---|---|
| 看健康 | 集群详情的概览、节点、磁盘标签；告警事件页 | 每分钟检查集群、节点、磁盘、容量；异常时告警（集群不健康、节点掉线、盘掉线、池用量 ≥ 80% / 90%、某节点没挂上文件系统），恢复后自动解除（S5，§14） |
| 加节点 | 「节点 → 加节点」，可同时带盘 | 预检 → 安装 → 编译 → 加入集群 → 挂载（§7.5） |
| 加盘 | 「磁盘 → 加盘」 | 核对身份 → 建 NSD → 加进文件系统 |
| 重新均衡 | 「文件系统 → 重新均衡」，单独发起 | I/O 很重，可能跑几小时，建议在业务低峰 |
| 移除盘 / 节点 | 「移除」 | 先把数据迁走再擦盘；剩余故障组不少于 3、仲裁节点仍为奇数；节点上用这个池的云服务器要先迁走 |
| 节点永久坏了 | 「离线移除」，输入节点名确认 | 不在坏节点上执行任何命令，直接从集群里摘掉 |
| 换坏盘 | 「换盘」 | 摘掉坏盘、加入新盘 |
| 节点重启 | 无需操作 | GPFS 挂上之前，用 GPFS 盘的云服务器留在待启动列表，挂上后自动启动（§9.9）；NSD 节点回来后它的盘自动恢复上线（§7.1） |
| 改配额 / 删池 | 存储池标签 | 池里还有卷时不能删 |
| 建第二个文件系统 | 「文件系统 → 新建」 | 用空闲 NSD 或新认领的盘（§7.3） |
| 升级 GPFS / 内核 | 「升级」（S6，§7.7） | 逐台升级：先迁走云服务器，再停 GPFS、装新版、编译、启动；最后的格式升级不可逆，单独确认。加入集群时内核已锁定（D5），查过 IBM 支持表后再手工升级 |
| 节点宕机疏散 | 节点详情「疏散」（S6，§11） | 先确认旧节点已被隔离（GPFS 驱逐），再在别的节点用原来的盘启动它上面的云服务器（前提是盘全在共享池上）；旧节点回来先对账、清掉残留，才解除隔离 |
| 删除集群 | 输入集群名，列出将被擦除的盘 | 卸载 → 删文件系统 → 删 NSD → 停 GPFS → 擦盘 → 清理密钥、标记、内存预留；集群上还有存储池时不能删（§7.6） |

所有操作都进审计和操作动态（§13.4）。

### 19.4 普通用户：使用

1. **选池建云硬盘**（S2，§9.4、§9.5）：建卷时在存储池下拉里选 GPFS 池，看到的只是一个「共享」类型的池。建好就已实际分配空间，状态直接变为可用；占用组织的磁盘配额（`disk_gb`），与本地盘相同。池满或接近满（用量 ≥ 90%，或已分配量超出容量）时建卷直接被拒绝
2. **挂载到任意节点上的云服务器**（S2）：共享卷可以挂给任何能访问这个池的节点上的云服务器；挂载弹窗里所在节点访问不了这个池的云服务器置灰并说明原因（§13.3）。从 A 节点的云服务器卸载、再挂到 B 节点的云服务器，数据一致
3. **扩容**（S2）：云服务器开着也能扩，不用重启；虚拟机内的分区和文件系统仍要自己扩，扩容弹窗给出步骤
4. **删卷**：卷还挂着时不能删
5. **系统盘放在 GPFS 上**（S4，§9.6、§9.7）：建云服务器时系统盘选 GPFS 池。每个镜像在每个池里只导入一次基础副本，之后的系统盘从它克隆，创建时间与镜像大小无关；重装、救援、捕获镜像、删除照常可用
6. **写满时**（§9.5）：池到配额或写满时，用这块盘的云服务器被暂停，数据不损坏；空间腾出来后要手动恢复，不像本地盘那样自动恢复
7. **迁移**（§10）：迁移由管理员发起，用户感受到的是停机时间更短。GPFS 盘不复制；磁盘全在共享池上的云服务器热迁移，耗时与磁盘大小无关。迁移弹窗逐块盘写明「复制」或「共享，不复制」

用户不会接触集群、节点、NSD 这些概念；也没有 NFS / SMB / S3 等协议服务（§1.3）。

### 19.5 各阶段能用到什么

| 阶段 | 管理员 | 用户 |
|---|---|---|
| S1（已实施，未部署） | 节点预检、链路自检 | — |
| S2 | 软件包、部署、导入、扩缩容、删除、建池 | GPFS 云硬盘：建、挂、换挂、扩容、删；带 GPFS 数据盘的迁移 |
| S4 | 镜像预热 | 系统盘放 GPFS、秒级克隆、全共享热迁移 |
| S5 | 健康告警、监控曲线、完整日志、重新均衡、换盘、新节点自动加入 | — |
| S6 | 滚动升级、密钥轮换、宕机疏散 | 节点宕机后云服务器能在别处恢复 |
| S7 | 纠删码版（ECE）、多集群 | — |

---

## 20. Ceph 端到端使用流程（管理员与用户视角）

本章与 §19 对应，把前文 Ceph 相关的设计按「谁在什么时候做什么」串起来。每一步标出所在阶段（§16）：S1 已实施，S3 起未实施。S3 排在 S2 之后（GPFS 优先），但它不依赖 24.04 节点，可以和 S2 并行。细节以正文对应章节为准。

### 20.1 两类人看到的东西

| | 系统管理员 | 普通成员（组织里的用户） |
|---|---|---|
| 能看到 | 存储集群（节点、磁盘、存储池、客户端、任务、健康）、所有存储池 | 只有存储池：名称、类型（本地 / Ceph）、是否共享（§4.1、§13.1） |
| 能做 | 部署、扩缩容、配置客户端、建池、维护、删除集群 | 建云硬盘、挂载、扩容、删卷、选系统盘放在哪个池 |

前提（管理员负责）：

- 计算节点是 Ubuntu 24.04 或 26.04，两者都行；**一个集群只用一个 Ceph 版本**：24.04 的发行版源是 19.2 系列、26.04 是 20.2，两种系统混用时改用 Ceph 官方源固定版本（§8.1）
- 节点上有 docker（CloudLand 部署计算节点时已装），cephadm 用它跑守护进程
- 节点能拉到容器镜像（默认 `quay.io/ceph/ceph`），私有化环境要准备私有镜像仓库（§8.1、D8）
- 不需要对象存储（S3）：软件来自发行版源，守护进程来自容器镜像，没有要上传的安装包（§6.1）
- 控制面配了凭据加密密钥（现在沿用 `VPN_SECRET_KEY`），否则新建和导入 Ceph 集群返回 503（§5.10）
- 节点已注册进 CloudLand，做过一次磁盘扫描（§6.4）
- 一台节点最多为一个托管 Ceph 集群跑守护进程，作客户端则可以同时用多个集群（§4.1）
- mon 所在节点的根文件系统和内置本地池共用，本地云服务器写满根分区会让 mon 停掉、整个集群卡住，建议给 `/var/lib/ceph` 单独分区或调低内置池的上限（§3.1、§18.3）

### 20.2 管理员：搭建

**1. 准备镜像**（S3，§8.1）：不用上传任何东西。镜像默认 `quay.io/ceph/ceph:v<集群版本>`；用私有仓库时在向导里填地址和口令，口令加密保存。

**2. 节点预检**（S1 已实施，§6.5）：「存储 → 存储集群 → 节点预检」，选「Ceph」、节点、角色、磁盘，平台逐项检查并给出通过 / 警告 / 不通过，只检查、不安装：

- 系统是不是 Ubuntu 24.04 / 26.04
- 时间是否同步（mon 之间时差超过 0.05 秒 Ceph 就告警）
- 节点之间 3300、6789、6800（代表 OSD 的端口段）与 SSH 是否连通
- 磁盘是否空闲、身份能否核对、不小于 5 GB
- mon 节点上 `/var/lib/ceph` 所在分区是否有 30% 空闲、内存余量
- docker 或 podman 是否在运行、软件包管理器的锁能否拿到
- 节点上是否已有不归 CloudLand 管的 Ceph

**3. 创建向导**（S3，§13.3，整页、7 步）：

1. **类型**：选「Ceph」，或「导入外部 Ceph」
2. **软件**：镜像地址（带默认值），私有仓库时填口令
3. **节点与角色**：列出在线节点，显示系统、内存余量、已在哪个集群。每台勾选角色（§5.2）：
   - **管理**（带 `_admin` 标签）：有集群的管理密钥，执行集群命令；必须同时是 mon，第一台执行初始化（bootstrap）
   - **mon**：奇数台，3 台（或 5 台）
   - **mgr**：1–2 台
   - **OSD**：贡献磁盘
   - **客户端**：不跑任何守护进程，只配置访问 Ceph 的客户端

   向导实时校验：mon 为奇数；管理节点是 mon；有 OSD 的节点数不少于副本数（3）；节点没有在为别的托管 Ceph 集群跑守护进程（§6.3）
4. **磁盘**：每台 OSD 节点的空闲盘，显示型号、大小、介质、扫描时间；有旧数据的盘要单独勾选「擦除」。Ceph 自动识别的介质与认领时不同时，以认领时选的为准
5. **参数**（都有默认值，§8.2）：集群名、镜像、公共网段（默认取节点内网地址所在网段）、复制网段（可选，推荐独立的存储网络）、副本数（只能是 3，不允许 2）、每个 OSD 的内存上限 `osd_memory_target`（2 GiB）。单节点测试形态（1 个 mon、副本 1）要显式勾选，集群详情会一直显示警告
6. **预检**：一键执行；有不通过的项不能继续
7. **确认**：汇总将占用的盘、每台节点将预留的内存（mon 2 GiB、mgr 1 GiB、每个 OSD 为内存上限加 0.5 GiB，默认 2.5 GiB，从云服务器可用内存里扣掉，§6.7）；提交后跳到任务页

**4. 看部署过程**（§6.2、§8.2）：任务页与 GPFS 相同，按步骤和节点逐格显示进度与日志。步骤为：

预检 → 加入 → 安装（`cephadm`、`ceph-common`、`lvm2`）→ 拉镜像 → SSH 信任 → 初始化集群（第一台管理节点）→ 加入其他节点 → 放置 mon / mgr → 集群配置 → 核对磁盘 → 建 OSD → 配置客户端 → 收尾

平台替管理员处理掉的几个默认行为：

- 不装 cephadm 自带的监控和管理面板：它的端口和 CloudLand 的监控冲突（§3.1），Ceph 的指标由 CloudLand 的 Prometheus 采集（§14.3）
- 关掉 cephadm「自动占用所有空闲盘」：只有认领过的盘会变成 OSD，节点上别的空闲盘不受影响
- 关掉 OSD 内存自动调整：否则 OSD 会按主机内存的七成分内存，与云服务器争抢，预留也算不准
- 建一个只访问 RBD 的客户端身份（`client.cloudland`），计算节点只拿到它，拿不到集群管理密钥

失败时的处理与 GPFS 相同：任务停在失败的那一步并占着集群，可以**重试**（只在失败的节点上重跑）或**中止**；没有自动回滚，半成品用「删除集群」清理（§8.6）。

**5. 建 CloudLand 存储池**（S3，§8.3）：集群详情「存储池」标签 → 新建，选介质、配额。

- 平台按介质建分布规则（该介质的 OSD 所在节点数不少于副本数才能建，否则直接拒绝）、建 RBD 池（3 副本，坏一台节点仍可读写）、设配额、写一个标记对象，再把新池加进客户端身份的权限，让每台客户端检查一遍能否访问
- 池从「创建中」变为「可用」，列表显示「可用节点 x/y」
- **建议设配额**：同一介质下几个不设配额的池共用这一份容量，准入时合并计算（§9.5）；Ceph 池默认不超分

**6. 让其他计算节点也能用**（S3，§8.4）：集群成员自动配置客户端；不在集群里的计算节点要用这个池，在集群详情「客户端」标签里添加。平台在节点上装 `ceph-common` 和 QEMU 的 RBD 驱动，写集群配置和密钥，在 libvirt 里登记密钥（所有节点同一个编号，热迁移时目标节点才认得）。客户端不跑守护进程、不预留内存，24.04、26.04 都可以。也可以设「自动加入的可用区」，这些可用区里新上线的节点自动配置（§6.3）。

**导入外部集群**（S3，§8.8）：填 fsid、mon 地址、客户端用户名与密钥（权限至少 `mon 'profile rbd'`、`osd 'profile rbd pool=<要用的池>'`）、要使用它的节点，平台在这些节点上配置客户端；再登记已有的池名，平台只往池里写标记对象（客户端要有写权限）。不在外部集群上执行任何管理命令。

### 20.3 管理员：日常维护

| 场景 | 怎么做 | 平台做的事 |
|---|---|---|
| 看健康 | 集群详情的概览、节点、磁盘标签；告警事件页 | 每分钟在一台管理节点上检查集群健康、主机、OSD、容量；异常时告警（集群不健康、主机掉线、OSD 掉线、池用量 ≥ 80% / 90%、Ceph 报 `nearfull`），恢复后自动解除（S5，§14） |
| 加节点 | 「节点 → 加节点」，可同时带盘 | 预检 → 安装 → 拉镜像 → SSH 信任 → 加入编排 → 按角色放守护进程 → 配置客户端（§8.5） |
| 加盘 | 「磁盘 → 加盘」 | 核对身份 → 建 OSD；Ceph 自动把一部分数据迁到新盘，没有单独的「重新均衡」，迁移流量和业务共用网络与磁盘（§8.5） |
| 加 / 移除客户端 | 「客户端」标签 | 移除前提：节点上没有使用这个集群的云服务器（§8.4） |
| 移除盘 | 「移除」 | 先把数据迁走（显示进度）再擦盘；要求剩余 OSD 所在节点数不少于副本数、剩余容量够 |
| 换坏盘 | 「换盘」（S5） | 保留原来的 OSD 编号，新盘用同一编号加入 |
| 移除节点 | 「移除」 | 先迁走这台节点上的全部守护进程（含 OSD 的数据），再从集群摘掉、删客户端配置；mon 仍为奇数且不少于 3（先缩 mon）；节点上用这个集群的池的云服务器要先迁走 |
| 节点永久坏了 | 「离线移除」，输入节点名确认 | 节点离线超过 30 分钟才能做；不在坏节点上执行任何命令，从集群摘掉它和它的 OSD，Ceph 在其余 OSD 上恢复副本 |
| 改角色 | 节点标签里改 mon / mgr / 管理（S5） | 改放置后由 cephadm 增减守护进程；校验同建集群 |
| 节点重启 | 10 分钟以内无需操作；停更久先进 Ceph 的维护模式（§8.5） | 守护进程由 systemd 拉起；用 RBD 盘的云服务器在这个池可用之前留在待启动列表，可用后自动启动（§9.9）。3 副本下一台节点离线不影响读写，同时离线两台时部分数据的读写会卡住，直到有节点回来；只有 3 台 OSD 节点时，坏掉的那台回来之前集群一直降级（§8.5） |
| 改配额 / 删池 | 存储池标签 | 池里还有卷时不能删；没有被引用的镜像基础副本随池删除 |
| 升级 Ceph | 「升级」（S6，§8.7） | cephadm 逐个守护进程滚动升级，不用先迁走云服务器。云服务器用的是节点上的客户端库，节点软件包升级后要重启或迁移云服务器才用上新版本，Ceph 兼容旧客户端，不急 |
| 节点宕机疏散 | 节点详情「疏散」（S6，§11） | 先把宕机节点的地址加进 Ceph 黑名单（有效期设得很长，默认的 1 小时一到会自动解除），确认它再也写不了盘，再在别的节点用原来的盘启动它上面的云服务器（前提是盘全在共享池上）；旧节点回来先对账、清掉残留，才解除黑名单。RBD 的排他锁挡不住两个写入者，这一步不能省（§12.1） |
| 删除集群 | 输入集群名，列出将被擦除的盘 | 先停掉 cephadm 的编排（否则它会把守护进程重新部署回来）→ 每台节点删除集群和 OSD 数据 → 清理客户端配置、libvirt 里的密钥、SSH 密钥、标记、内存预留 → 重新扫描磁盘；集群上还有存储池时不能删（§8.6） |

所有操作都进审计和操作动态（§13.4）。

### 20.4 普通用户：使用

1. **选池建云硬盘**（S3，§9.3、§9.5）：建卷时在存储池下拉里选 Ceph 池，看到的只是一个「共享」类型的池。建卷时就在 Ceph 里建出这块盘，状态直接变为可用；盘是精简配置的，实际占用随写入增长，但组织的磁盘配额（`disk_gb`）和池的准入都按卷的大小算。池接近满（用量 ≥ 90%，或已分配量超出容量）时建卷直接被拒绝；Ceph 池默认不超分
2. **挂载到任意节点上的云服务器**（S3）：与 GPFS 相同，所在节点访问不了这个池的云服务器在挂载弹窗里置灰并说明原因；从 A 节点的云服务器卸载、再挂到 B 节点的云服务器，数据一致
3. **扩容**（S3）：云服务器开着也能扩，不用重启；虚拟机内的分区和文件系统仍要自己扩
4. **删卷**：卷还挂着时不能删；还有云服务器打开着这块盘时 Ceph 也会拒绝，失败原因里写明是谁在用
5. **空间回收**：虚拟机里删掉文件后执行 `fstrim`（或挂载时带 `discard`），空间会还给池（磁盘开了 `discard='unmap'`，§12.2）
6. **系统盘放在 Ceph 上**（S4，§9.6、§9.7）：建云服务器时系统盘选 Ceph 池。每个镜像在每个池里只导入一次基础副本，之后的系统盘从它克隆，创建时间与镜像大小无关；重装、救援、捕获镜像、删除照常可用
7. **写满时**（§9.5）：**与本地盘、GPFS 都不同**，云服务器不会被暂停，而是磁盘 I/O 卡住，看起来像虚拟机没有响应；空间腾出来（扩配额、加盘、删数据）后 I/O 接着进行。池到配额只卡这个池；整个集群用到 95% 时**所有池的写入都卡住**，所以 85%（`nearfull`）就告警（具体表现待验证，V8）
8. **迁移**（§10）：迁移由管理员发起。Ceph 盘不复制；磁盘全在共享池上的云服务器热迁移，耗时与磁盘大小无关。UEFI 云服务器的 NVRAM 在节点本地，迁移时照旧复制（很小）。迁移弹窗逐块盘写明「复制」或「共享，不复制」

用户不会接触集群、mon、OSD 这些概念；也没有 CephFS、对象存储（RGW）、iSCSI 等服务（§1.3）。

### 20.5 各阶段能用到什么

| 阶段 | 管理员 | 用户 |
|---|---|---|
| S1（已实施，未部署） | 节点预检、链路自检 | — |
| S3 | 部署、导入、加减节点、加减盘、离线移除、配置客户端、删除、建池 | Ceph 云硬盘：建、挂、换挂、扩容、删；带 Ceph 数据盘的迁移 |
| S4 | 镜像预热 | 系统盘放 Ceph、秒级克隆、全共享热迁移 |
| S5 | 健康告警、监控曲线、完整日志、换盘、改角色、新节点自动加为客户端 | — |
| S6 | 滚动升级、密钥轮换、宕机疏散 | 节点宕机后云服务器能在别处恢复 |

### 20.6 与 GPFS 流程的差别

| | GPFS（§19） | Ceph |
|---|---|---|
| 软件从哪来 | 上传 IBM 安装包，要 S3、要接受许可证 | 发行版源装 `cephadm` / `ceph-common`，守护进程用容器镜像；不要 S3 |
| 节点系统 | 只能 22.04 / 24.04 | 24.04、26.04 都行，一个集群一个版本 |
| 角色 | 管理、仲裁、NSD、客户端 | 管理、mon、mgr、OSD、客户端 |
| 客户端 | 也是集群成员：装 GPFS、编内核模块、预留 pagepool | 只装客户端和配置，不跑守护进程、不预留内存 |
| 池的下面 | 文件系统 → 独立 fileset | 直接是一个 RBD 池，没有文件系统这一层 |
| 卷 | qcow2 文件 | raw 格式的 RBD 镜像，精简配置 |
| 副本数 | 数据 2 或 3 | 只能 3 |
| 加盘之后 | 要单独发起重新均衡 | Ceph 自动重新分布 |
| 写满 | 云服务器被暂停 | 磁盘 I/O 卡住；集群到 95% 时所有池都卡；默认不超分 |
| 删卷 | 只靠删除顺序保证 | 另外还有：有人打开着时 Ceph 拒绝删除 |
| 宕机恢复的隔离 | GPFS 驱逐 | 黑名单，有效期要显式设长 |
| 防两个写入者 | QEMU 文件锁（待验证，V5）或 virtlockd | 只能靠黑名单 |
| UEFI NVRAM | 在池里，迁移、恢复后都在 | 在节点本地，迁移时复制；宕机恢复时从模板重建（V16） |

---

## 附录 A：受影响的代码

### clapi（`api/src/`）

| 文件 | 改动 | 阶段 |
|---|---|---|
| `model/storage_cluster.go`、`model/storage_task.go`（新增） | §5.1–§5.4、§5.9；类型专用的数据在 `params` / `attrs` / `secrets` 三个 JSON 列 | S1 |
| `model/storage_package.go`（新增） | §5.8 | S2 |
| `model/storage_pool.go` | `StoragePool` 加 §5.5 的列（驱动专用的放 `DriverParams`）与状态；`HyperDisk` 加 `gpfs_nsd` / `ceph_osd` 状态 | S1 / S2 |
| `model/volume.go`、`model/image_storage.go`（新增） | `BaseImageStorageID`；`ImageStorage` | S4 |
| `services/storage_task.go`（新增） | 任务引擎（§6.2） | S1 |
| `services/storage_cluster.go`（新增） | 集群的列表、详情、预检、各任务的步骤定义与参数生成 | S1–S6 |
| `services/storage_backend.go`（新增） | 存储后端接口与注册（§4.5.1） | S1 |
| `services/storage_backend_gpfs.go`、`services/storage_backend_ceph.go`（新增） | 两种存储的后端：角色规则、支持矩阵、参数、内存预留（S1）；任务步骤、池驱动与池参数、客户端（S2 / S3）；健康看护（S5）；隔离、升级（S6） | S1–S6 |
| `services/pool_driver.go`、`services/pool_driver_gpfs.go`、`services/pool_driver_rbd.go`（新增） | 存储池驱动接口与两个实现（§4.5.2） | S2 / S3 |
| `services/storage_package.go`（新增） | 分片上传、校验、许可证 | S2 |
| `services/storage_support.go`（新增） | 支持矩阵与数据目录的类型定义、集群 SSH 密钥生成 | S1 |
| `services/storage_pool.go` | 按驱动建池（共享池发任务）；`PoolScriptID` / `PoolRelPath` 等按驱动 | S2 / S3 |
| `services/storage_admission.go` | 共享池的池级准入（§9.5），容量未知拒绝 | S2 / S3 |
| `services/storage_callbacks.go` | 共享池行的过期规则（不依赖 `capacity_at`，§9.2） | S2 |
| `services/hyper_storage.go` | `pickDisks` 检查集群认领；`SaveScannedDisks` 按登记显示 | S1 |
| `services/hyper.go:628-641`（`releaseStorage`） | 删除节点时只把**本地**非内置池算作「节点上有本地池」；节点是存储集群成员时拒绝 | S1 / S2 |
| `services/volume.go`（`:319`、`:432-435`、`:606-616`、`:652` 等） | `hyper = 0` 的含义按驱动区分（§5.6）；共享卷的建、挂、扩、删按驱动下发到 `pickPoolHost` | S2 / S3 |
| `services/instance.go:304-317`、`services/instance_placement.go:98-180`（`startCreationPlacement`）、`:210`（`bootHost`） | 系统盘在共享池时的候选节点、池级准入与 `boot_disk`；放置组路径同样处理 | S4 |
| `services/migration_plan.go`（`planDisks`、`ApplyMigrationPlan` 及其兼容分支）、`services/migration.go` | 共享盘「保留」、不改共享卷的 `hyper`；目标节点过滤；`force` 的条件 | S2 / S3 / S6 |
| `services/image.go`、`services/s3.go` | 基础副本的导入、引用计数与预热；预签名地址按对象键生成 | S2 / S4 |
| `common/secret.go`、`common/error_codes.go` | 错误码改名 `ErrSecretUnavailable`；新的存储错误码 | S1 |
| `rpcs/storage_task.go`（新增） | `storage_task_run`（S1）、`storage_health`（S5） | S1 / S5 |
| `rpcs/` 其他新增 | `shared_pool_status`、`create_volume_shared` 等（按操作命名，不按驱动）、`image_storage_status`、`node_recovered` | S2–S6 |
| `rpcs/attach_volume.go:83-85` | 挂载成功的回调现在一律把 `hyper` 改成执行节点：共享卷不改 | S2 / S3 |
| `rpcs/create_volume.go`（`create_volume_local`） | 系统盘建好后写 `hyper=hostid`：共享卷不写 | S4 |
| `rpcs/migrate_vm.go`、`rpcs/clear_vm.go`、`rpcs/launch_vm.go`、`rpcs/inst_status.go` | 确认销毁后删共享盘与池里的 NVRAM；宕机恢复后的归属保护 | S4 / S6 |
| `apis/storage_cluster.go`、`apis/storage_task.go`（新增）、`apis/routes.go`、`apis/audit_actions.go` | §13.1、§13.4 | S1 起 |
| `apis/storage_package.go`（新增）、`apis/storage_pool.go` | 软件包接口；共享池参数 | S2 / S3 |
| `services/services.go` | 启动任务引擎的后台循环 | S1 |

### 节点脚本（`scripts/`）

| 文件 | 改动 | 阶段 |
|---|---|---|
| `kvm/storage/`（新目录） | 附录 B 的全部脚本 | S1–S6 |
| `kvm/storage_lib.sh` | 身份核对（`disk_identity`）；`classify_disk` 识别 `ceph_osd`、GPFS 分区类型的盘与装了 GPFS 时盘头有数据的空白盘判 `unknown_member`（S1，S2 验收时改为盘头全零判 `free`）；`wipe_disk` 清零两端各 10 MiB；`pool_root` / `pool_enter` 支持共享池的根（§9.1，S2）。共享池的写前检查是驱动的 `drv_guard`，不改 `pool_guard` | S1 / S2 |
| `kvm/async_job/scan_host_disks.sh` | 同上 | S1 |
| `kvm/report_rc.sh` | 内存预留（含 `MemFree` 那一行，S1）；`instance_pools`（`:302-311`）认得 GPFS 路径与 RBD 网络盘，读共享池探测状态（S2 / S3） | S1 / S2 / S3 |
| `kvm/*_volume_shared.sh`、`kvm/import_image_shared.sh`（新增） | 共享池的建、挂、卸、扩、删与镜像导入，按 stdin 里的驱动引入 `storage/drivers/<驱动>.sh`（§4.5.2） | S2 / S3 |
| `kvm/launch_vm.sh`、`kvm/reinstall_vm.sh`、`kvm/rescue_vm.sh`、`kvm/clear_vm.sh`、`kvm/async_job/capture_image.sh` | 按 `boot_disk` 的驱动处理系统盘 | S4 |
| `kvm/source_migration.sh`、`kvm/finish_source_migration.sh`、`kvm/async_job/clear_target_migration.sh`、`kvm/async_job/complete_migration.sh` | 共享盘不复制、不清理 | S2 / S3 |
| `kvm/node_recovered.sh`、`kvm/clear_stale_vm.sh`（新增） | 节点重新注册时的对账（§11.4） | S6 |
| `xml/` | 新增 RBD 磁盘模板 | S3 |

### 其他

| 位置 | 改动 | 阶段 |
|---|---|---|
| `cpgateway/src/apis/proxy_routes.go` | §13.2 | S1 起 |
| `web/src/`：`api/storageClusters.ts`（新增）；`views/dashboard/StorageClusters.vue`（新增）；`components/storage/`（预检弹窗、任务抽屉）；`router/index.ts`、`views/dashboard/Layout.vue`（「存储」分组）；三种语言包。类型、角色、建议角色取自 `GET /storage_backends`，不在前端写死（§4.5.1） | §13.3 | S1 |
| `web/src/`：`api/storagePackages.ts`、`views/dashboard/StoragePackages.vue`、`StorageClusterCreate.vue`、`StorageClusterDetail.vue`、`components/storage/params/<类型>.vue`（新增，每种存储一个参数表单）；`StoragePools.vue`、`StoragePoolDetail.vue`、`HostStorageTab.vue`、`VolumeActionModals.vue`、`CreateInstanceModal.vue`、迁移弹窗 | §13.3 | S2–S4 |
| `deploy/docker/config/prometheus/prometheus.yml` | `ceph-mgr` 采集任务 | S3 / S5 |
| `deploy/docker/scripts/deploy-compute-node.sh`、`deploy/roles/hyper/tasks/main.yml` | 显式安装 `qemu-block-extra` | S3 |
| `docs/` | §15.3 | 各阶段 |

---

## 附录 B：节点脚本清单

都在 `scripts/kvm/storage/`，共用 `stc_lib.sh`（作业目录、幂等启动、集群锁、回调，§6.2.3）。「后台」表示同步部分只登记作业、长活经 `async_exec` 执行；每个脚本先判断目标是否已经达成，达成就直接报成功（可重入）。

| 脚本 | 后台 | 做什么 | 已达成的判断 | 阶段 |
|---|---|---|---|---|
| `stc_lib.sh` | — | 作业目录、`boot_id`、集群锁、日志与回调描述符、身份核对的封装；`backend_load` 引入某种存储的钩子文件 | — | S1 |
| `backends/<类型>.sh` | — | 每种存储的节点钩子（§4.5.1）：`backend_existing`（S1，预检用）；以后按需加 `backend_health`、`backend_fence` 等 | — | S1 起 |
| `drivers/file.sh`、`drivers/gpfs.sh`、`drivers/ceph_rbd.sh` | — | 存储池驱动的函数（§4.5.2）：文件型的公共实现，GPFS 只覆盖检查与克隆 | — | S2 / S3 |
| `stc_poll.sh` | 否 | 回答一个运行的状态：已结束（重发结果）/ 在跑 / 中断 / 没有 | — | S1 |
| `stc_kill.sh` | 否 | 杀掉一个运行的进程组，记 `exit` | 进程已不在 | S1 |
| `stc_selftest.sh` | 是 | 按参数睡眠、失败、输出进度、拿集群锁 | — | S1 |
| `stc_precheck.sh` | 是 | §6.5 的各项；输出主机公钥、内网地址、内核版本 | — | S1 |
| `stc_ssh_trust.sh` | 是 | 写或删 `authorized_keys` 那一行；管理节点写私钥、`known_hosts` | 内容一致 | S1 |
| `stc_mem_reserve.sh` | 否 | 写 `/opt/cloudland/run/storage_reserved_memory` | 内容一致 | S1 |
| `stc_join.sh` | 是 | 写成员标记；GPFS 节点 hold 内核元包 | 标记已存在 | S2 / S3 |
| `stc_resolve_disks.sh` | 是 | 按稳定 ID 解析、核对身份、复核盘是空的（之前核对过的盘只核身份） | — | S2 / S3 |
| `stc_release_disks.sh` | 是 | 盘离开集群：先让存储软件只认留下的盘（GPFS 的 `nsddevices`），再核对身份后擦掉离开的盘 | 身份对不上的不擦 | S2 |
| `stc_finish.sh`、`stc_leave.sh` | 是 | 部署 / 加节点的收尾（内存预留、扫描）；离开集群的清理（存储软件、擦盘、密钥、内存预留、hold 的内核包、成员标记） | 每次都跑 / 没有的跳过 | S2 |
| `stc_pools.sh` | 是 | 写这个集群的共享池清单（同 `sync_shared_pools.sh`），新建池时当场探测一次并回报（§7.4） | — | S2 |
| `stc_forget.sh` | 是 | 取消登记导入的集群：只删清单和探测进程（§7.8） | 目录已不存在 | S2 |
| `stc_fetch.sh` | 是 | 按预签名地址下载、校验 SHA-256 | 缓存已有且校验通过 | S2 |
| `stc_upload_log.sh` | 是 | 把完整日志传给 clapi | — | S5 |
| `sync_shared_pools.sh` | 否 | 写共享池清单，停掉清单里已没有的池的探测进程（§9.2） | — | S2 / S3 |
| `shared_pool_probe.sh` | 后台 | 一个池探测一次就退出（驱动的 `drv_probe`），心跳每 30 秒拉起；参数先池后集群，`probe_alive` 与本地池共用 | — | S2 / S3 |
| `gpfs_install.sh` | 是 | 从安装包 tar 流解出 deb、校验 md5、`apt-get install` | `gpfs.base` 版本相同 | S2 |
| `gpfs_build_gpl.sh` | 是 | `mmbuildgpl` | 运行中内核的模块已存在 | S2 |
| `gpfs_rsh` / `gpfs_rcp` | — | 给 `ssh` / `scp` 加上集群私钥与 `known_hosts` 的包装 | — | S2 |
| `gpfs_cluster.sh <子命令>` | 是 | `create`、`start`、`teardown`、`add`（`mmaddnode`、许可证、启动、挂载）、`remove`（摘仲裁、停、`mmdelnode`；`offline` 时不碰那台节点）（管理节点执行）；`change_roles` 未做 | 按 `mmlscluster` / `mmgetstate` | S2 |
| `gpfs_nsd.sh` | 是 | `create`（`nsddevices` 用户出口由 `backends/gpfs.sh` 的 `backend_disks_resolved` 写） | 按 `mmlsnsd -X` | S2 |
| `gpfs_fs.sh <子命令>` | 是 | `create`、`add`（`mmadddisk`，不带 `-r`）、`remove`（`mmdeldisk`、`mmdelnsd`，节点已坏时 `-p`）、`rebalance`（`mmrestripefs -b`）；`delete` 未做（只能随集群删除） | 按 `mmlsfs`、`mmlsdisk` | S2 |
| `gpfs_pool.sh <子命令>` | 是 | `create`（`--filesetdf`、建 fileset、链接、两种介质时整体生成放置规则、配额、池内目录、标记文件）、`quota`、`delete`；导入的集群上 `register`、`unregister`（不执行任何 `mm` 命令） | 按 `mmlsfileset -Y` 比对名字 | S2 |
| `gpfs_import.sh` | 是 | 外部集群的节点检查（§7.8）：`mmfsd` 在跑、挂载点是 GPFS | — | S2 |
| `gpfs_disks_up.sh` | 后台 | 每分钟由 clapi 发给一台管理节点：所有节点都 active 后把 `down` 的 NSD 拉起来（`mmchdisk start -a`，10 分钟一次，§7.1） | 没有 `down` 的盘 | S2 |
| `gpfs_health.sh` | 是 | §14.1 | — | S5 |
| `gpfs_node_exporter.sh` | — | 附录 E 的采集脚本，由 systemd 定时器执行 | — | S5 |
| `ceph_install.sh` | 是 | `apt-get install cephadm ceph-common lvm2`；拉镜像 | 版本与镜像都已在 | S3 |
| `ceph_bootstrap.sh` | 是 | `cephadm bootstrap`、改写它加的 `authorized_keys` 行（§8.2） | `/var/lib/ceph/<fsid>` 已存在且能连上 | S3 |
| `ceph_orch.sh <子命令>` | 是 | `add_host`、`place`、`configure`、`add_osd`、`rm_osd`、`drain_host`、`rm_host`、`rm_host_offline`、`upgrade` | 按 `ceph orch` 的查询 | S3 |
| `ceph_pool.sh <子命令>` | 是 | `create`、`quota`、`delete`、`caps` | 按 `ceph osd pool ls` | S3 |
| `ceph_client.sh <子命令>` | 是 | `setup`、`remove`：配置文件（含 `keyring` 一行）、密钥环、libvirt secret | 内容一致 | S3 |
| `ceph_health.sh` | 是 | §14.1 | — | S5 |
| `ceph_rm_cluster.sh` | 是 | 停编排模块后 `cephadm rm-cluster --zap-osds` 与清理（§8.6） | `/var/lib/ceph/<fsid>` 已不存在 | S3 |

不在 `storage/` 目录里的：`scripts/kvm/*_volume_shared.sh`（S2 已有 `create`、`attach`、`resize`、`delete`；卸载沿用 `detach_volume_local.sh`）、`import_image_shared.sh`（与 `*_volume_local.sh` 并列；导入镜像基础副本是后台作业，正式路径已存在就算达成，§9.6）、`node_recovered.sh`、`clear_stale_vm.sh`（附录 A）。

---

## 附录 C：GPFS 命令对照

CloudLand 在各步骤里执行的命令（§7.2–§7.6），也是手工排障时的参考。`mm*` 命令都在 `/usr/lpp/mmfs/bin/`。

```bash
# install (every member), after the license was accepted in the UI: take the debs out of the
# installer's tar.gz payload instead of running the self-extracting script (§6.1)
tail -n +680 Storage_Scale_Erasure_Code-6.0.0.2-x86_64-Linux-install | tar -xz -C /tmp/gpfs gpfs_debs
apt-get install -y -o DPkg::Lock::Timeout=600 ./gpfs.base_*.deb ./gpfs.gpl_*.deb ./gpfs.gskit_*.deb \
    ./gpfs.msg.en-us_*.deb ./gpfs.license.ec_*.deb ./gpfs.docs_*.deb \
    libaio1t64 ksh build-essential linux-headers-$(uname -r)
mmbuildgpl

# create the cluster (primary admin node); node names are internal addresses
mmcrcluster -N nodes.list -C <name> -r /opt/cloudland/scripts/kvm/storage/gpfs_rsh \
    -R /opt/cloudland/scripts/kvm/storage/gpfs_rcp -A
mmchlicense server --accept -N <quorum and nsd nodes>
mmchlicense client --accept -N <other nodes>
mmchconfig adminMode=central,autoBuildGPL=yes,restripeOnDiskFailure=yes,pagepool=1G
mmstartup -a
mmgetstate -a

# NSDs: one failure group per node (shared-nothing replica layout), at least three failure groups
cat > nsd.stanza <<'EOF'
%nsd: device=/dev/sdb nsd=cl1h1d1 servers=10.191.202.40 usage=dataAndMetadata failureGroup=1 pool=system
%nsd: device=/dev/sdb nsd=cl1h2d1 servers=10.191.202.29 usage=dataAndMetadata failureGroup=2 pool=system
%nsd: device=/dev/sdb nsd=cl1h3d1 servers=10.191.202.13 usage=dataAndMetadata failureGroup=3 pool=system
EOF
mmcrnsd -F nsd.stanza

# file system: max replicas 3 so replicas can be raised later without recreating it
mmcrfs fs1 -F nsd.stanza -B 4M -m 3 -M 3 -r 2 -R 3 -T /gpfs/fs1 -A yes -Q yes --inode-limit 10000000
mmmount fs1 -a
mmlsmount fs1 -L

# one CloudLand storage pool = one independent fileset
mmcrfileset fs1 cl_1a2b3c4d --inode-space new --inode-limit 2000000
mmlinkfileset fs1 cl_1a2b3c4d -J /gpfs/fs1/cl_1a2b3c4d
mmchpolicy fs1 policy.txt -I test && mmchpolicy fs1 policy.txt
mmsetquota fs1:cl_1a2b3c4d --block 10T:10T

# /var/mmfs/etc/nsddevices lists only the disks claimed by the cluster, so GPFS never sees any
# other disk (loop devices of test environments are listed the same way):
#   echo "sdb generic"; return 0
```

测试环境用回环设备时，节点开机后要先把回环设备建好再启动 GPFS（一个排在 `gpfs.service` 之前的 systemd 单元），否则 NSD 找不到盘。

---

## 附录 D：GPFS 日常运维

界面覆盖日常操作（§7.5–§7.7）；下面是在管理节点上手工排障的常用命令。

```bash
mmgetstate -a                    # GPFS state of every node
mmhealth cluster show            # cluster health
mmlsdisk fs1 -e                  # disks that are not up and ready
mmdf fs1                         # file system capacity
mmlsquota -j cl_1a2b3c4d fs1     # quota and usage of one CloudLand pool
mmlsmount fs1 -L                 # which nodes have it mounted
mmumount fs1 -N <node>; mmmount fs1 -N <node>
mmshutdown -N <node>; mmstartup -N <node>
```

- **计算节点宕机**：按 §11 处理（阶段 S6 之后用 `POST /hypers/:uuid/evacuate`）。**不要**在确认原节点已被驱逐之前在别的节点 `virsh define` 再 `virsh start` 同一块盘的云服务器，那会两边同时写
- **节点恢复**：`mmstartup -N <节点>`、`mmmount all -N <节点>`；之后的开机对账见 §11.4
- **内核升级**：先迁走这台节点上用 GPFS 池的云服务器 → 解除内核元包的 hold → 确认新内核在支持矩阵里 → 升级并重启（`autoBuildGPL=yes` 会在启动时编译模块，失败时手工 `mmbuildgpl` 看报错）→ 重新 hold
- **NSD 服务节点故障**：副本数 ≥ 2 且其余故障组完好时文件系统照常可用；`mmlsdisk fs1 -e` 看哪些盘不可用。节点恢复后健康看护会自动 `mmchdisk fs1 start`（§7.1），手工排障时也是这一句，再按需 `mmrestripefs fs1 -r` 补齐副本
- 只有一台 NSD 节点或副本数为 1 时，它宕机整个文件系统就不可用、上面的云服务器全部卡住，**生产环境至少三台、双副本**

---

## 附录 E：集群监控的采集

### E.1 GPFS 成员节点（node_exporter textfile）

由 `finish` 步骤装到每个成员上，systemd 定时器每分钟执行（定时器单元带 `TimeoutSec`）。要检查的挂载点来自集群目录里的 `gpfs_mounts`（建文件系统时由编排器写入，§6.8）：只看 `/proc/mounts` 的话，没挂上的文件系统根本不会出现。本节点的挂载状态按 `stat -f` 判断（`mmlsmount | grep -c` 只要文件系统在任一节点挂着就算挂载，不对）；容量用 `df`。**没有在真实 GPFS 上运行过**，`mmhealth` 的输出格式以实际版本为准。这些指标只用于看曲线，告警由健康看护产生（§14.2）。

```bash
#!/bin/bash
# GPFS health of this node for the node_exporter textfile collector
OUTPUT=/var/lib/node_exporter/cloudland_gpfs.prom
TMP=$OUTPUT.tmp
: >$TMP
for mnt in $(cat /opt/cloudland/run/storage/*/gpfs_mounts 2>/dev/null | sort -u); do
    fs=$(basename "$mnt")
    mounted=0
    # a hung GPFS mount blocks stat; the timeout cannot kill a process stuck in D state, so the whole
    # collector runs from a timer with its own timeout and the exporter sees a stale file at worst
    [ "$(timeout 10 stat -f -c %T "$mnt" 2>/dev/null)" = "gpfs" ] && mounted=1
    echo "cloudland_gpfs_filesystem_mounted{fs=\"$fs\"} $mounted" >>$TMP
    if [ $mounted -eq 1 ]; then
        timeout 10 df -B1 --output=size,avail "$mnt" | awk -v fs="$fs" 'NR == 2 {
            print "cloudland_gpfs_filesystem_total_bytes{fs=\"" fs "\"} " $1
            print "cloudland_gpfs_filesystem_free_bytes{fs=\"" fs "\"} " $2 }' >>$TMP
    fi
done
timeout 20 /usr/lpp/mmfs/bin/mmhealth node show 2>/dev/null | awk 'NR > 1 && NF >= 2 {
    print "cloudland_gpfs_component_healthy{component=\"" $1 "\"} " ($2 == "HEALTHY" ? 1 : 0) }' >>$TMP
mv $TMP $OUTPUT
```

管理节点另外导出每个 fileset 的用量与配额（`mmlsquota -j ... -Y`），指标 `cloudland_gpfs_fileset_used_bytes`、`cloudland_gpfs_fileset_quota_bytes`，带 `pool_uuid` 标签。

### E.2 Ceph

mgr 的 `prometheus` 模块直接导出（`ceph_health_status`、`ceph_osd_up`、`ceph_osd_in`、`ceph_pool_stored`、`ceph_pool_max_avail`、`ceph_pool_quota_bytes`、读写的吞吐与 IOPS 等），由 Prometheus 按 `ceph_targets.json` 采集（§14.2）。

### E.3 告警

不写 Prometheus 告警规则，告警由健康看护直接产生（§14.2）。

---

## 附录 F：Ceph 命令与配置参考

### F.1 磁盘 XML 与 libvirt secret

```xml
<disk type='network' device='disk'>
  <driver name='qemu' type='raw' cache='writeback' discard='unmap'/>
  <source protocol='rbd' name='CEPH_POOL/volume-ID'>
    <host name='MON_ADDR_1' port='6789'/>
    <host name='MON_ADDR_2' port='6789'/>
    <host name='MON_ADDR_3' port='6789'/>
  </source>
  <auth username='CLIENT_USER'>
    <secret type='ceph' uuid='SECRET_UUID'/>
  </auth>
  <target dev='vdb' bus='virtio'/>
</disk>
```

```bash
# the same secret uuid on every node, so live migration finds it on the target
cat > secret.xml <<'EOF'
<secret ephemeral='no' private='yes'>
  <uuid>SECRET_UUID</uuid>
  <usage type='ceph'><name>client.CLIENT_USER CLUSTER_UUID</name></usage>
</secret>
EOF
virsh secret-define secret.xml
virsh secret-set-value --secret SECRET_UUID --file key.b64    # keep the key off the command line
```

### F.2 部署与池

```bash
cephadm --image quay.io/ceph/ceph:v20.2.0 bootstrap --fsid <uuid> --mon-ip <internal ip> \
    --ssh-private-key key --ssh-public-key key.pub --ssh-user root \
    --skip-monitoring-stack --skip-dashboard --skip-firewalld
ceph orch apply osd --all-available-devices --unmanaged=true   # never grab free disks on its own
ceph orch host add <hostname> <internal ip> --labels mon,mgr,osd
ceph orch apply mon --placement=label:mon
ceph orch apply mgr --placement=label:mgr
ceph orch daemon add osd <hostname>:/dev/sdb
ceph mgr module enable prometheus
ceph config set osd osd_memory_target_autotune false            # keep the memory reservation right (§6.7)
ceph config set osd osd_memory_target 2147483648

ceph osd crush rule create-replicated cl-hdd default host hdd
ceph osd pool create cl_1a2b3c4d
ceph osd pool set cl_1a2b3c4d size 3
ceph osd pool set cl_1a2b3c4d min_size 2
ceph osd pool set cl_1a2b3c4d crush_rule cl-hdd
ceph osd pool application enable cl_1a2b3c4d rbd
rbd pool init cl_1a2b3c4d
ceph osd pool set-quota cl_1a2b3c4d max_bytes $((10 * 1024**4))
ceph auth get-or-create client.cloudland mon 'profile rbd' osd 'profile rbd pool=cl_1a2b3c4d' \
    mgr 'profile rbd pool=cl_1a2b3c4d'
ceph config generate-minimal-conf    # plus "keyring = /etc/ceph/<cluster uuid>.client.<user>.keyring" (§6.8)
```

### F.3 驱动用到的 RBD 命令

```bash
opts="--conf /etc/ceph/<cluster uuid>.conf --id <client user>"
rbd $opts create cl_1a2b3c4d/volume-12 --size 40960 \
    --image-feature layering,exclusive-lock,object-map,fast-diff,deep-flatten
rbd $opts resize cl_1a2b3c4d/volume-12 --size 81920
rbd $opts status cl_1a2b3c4d/volume-12      # watchers, i.e. who has it open
rbd $opts rm cl_1a2b3c4d/volume-12
rbd $opts clone cl_1a2b3c4d/image-3-ab12cd34@base cl_1a2b3c4d/volume-13
ceph $opts osd blocklist range add 10.191.202.29/32 315360000   # fencing a dead node; give a long expiry, the default is one hour (§11.2)
```

### F.4 运维

```bash
ceph -s; ceph health detail
ceph osd tree; ceph osd df
ceph df detail
ceph orch ps; ceph orch host ls
ceph orch osd rm <id> --zap; ceph orch osd rm status
ceph orch host maintenance enter <host>; ceph orch host maintenance exit <host>   # planned downtime longer than ~10 minutes (§8.5)
ceph mgr module disable cephadm                         # before removing the cluster from its hosts (§8.6)
ceph orch upgrade start --image <image>; ceph orch upgrade status
```

参考：[Ceph 与 libvirt](https://docs.ceph.com/en/latest/rbd/libvirt/)、[cephadm](https://docs.ceph.com/en/latest/cephadm/)、[QEMU 块设备](https://qemu.readthedocs.io/en/latest/system/devices/block.html)。

---

## 附录 G：原文档的去向

本文取代 `gpfs-shared-storage-design.md`（2026-09-22），后者在 2026-10-01 已并入更早的两份文档 `storage-gpfs-ceph-integration-plan.md`、`gpfs-deployment-plan.md`（原文见 git 历史 `404a92c9`）。原 `gpfs-shared-storage-design.md` 各章节的去向：

| 原章节 | 去向 |
|---|---|
| §0 摘要、§1 背景与目标 | §0、§1 重写；「CloudLand 不管理 GPFS 集群」这条非目标**取消** |
| §1.4 WDS 的去留 | 删除：已决定放弃并已删除（2026-09-22） |
| §2 GPFS 概念与映射 | 「独立 fileset 作为存储池」的思路保留在 §7.4；放置规则的约定写进 §7.4；「管理员的准备工作」改为由 CloudLand 自动完成（托管集群），外部集群仍按原方式登记 |
| §3 总体设计 | §9 开头的四条原则、§9.1 的路径表（扩成四种池） |
| §4 数据模型 | §5；原 §4.1–§4.2 的表已由本地方案建出来，本文只加列；`image_storages` 改为新建（原表随 WDS 删了） |
| §5 挂载保护、可用性、容量 | §9.1 的 `pool_guard`、§9.2、§9.5 |
| §6 各操作流程 | §9.3、§9.4（按驱动）、§9.6–§9.9 |
| §7 迁移 | §10 |
| §8 节点宕机后的恢复 | §11（加了 Ceph 的黑名单隔离） |
| §9 一致性与安全 | §12 |
| §10 接口与前端 | §13 |
| §11 部署与配置 | §15；「部署脚本不管 GPFS」改为「默认不装，加入集群时按需装」 |
| §12 扩展到 Ceph | §8、§9.3 |
| §13 旧文档的去向 | 本附录 |
| §14 实施阶段 | §16；原阶段 0 已由本地方案完成，原阶段 1–3 分别并入 S2 / S3、S4、S6 |
| §15 测试方案 | §17 |
| §16 风险与待验证项 | §18.2；第 1 条（26.04 支持）已有结论（§2.2） |
| 附录 A 受影响的代码 | 附录 A 重写 |
| 附录 B 现有缺陷 | 删除：均由本地方案的 L0a / L0b 处理，实际处置见本地方案附录 B |
| 附录 C GPFS 集群规划与安装 | §7.2（自动化）与附录 C（命令对照） |
| 附录 D GPFS 日常运维 | 附录 D |
| 附录 E GPFS 集群监控 | §14 与附录 E |
| 附录 F Ceph RBD 参考 | §8、§9.3 与附录 F |


---

## 附录 H：评审处置记录（2026-10-01）

三路并行评审：代码事实核对（下表 F）、对抗式设计审查（A）、内部一致性（C）。「已改」指正文已按结论修改；「推迟」写明到哪个阶段；「不采纳」写明理由。

### H.1 代码事实核对

| # | 发现 | 处置 |
|---|---|---|
| F1 | Ceph OSD 盘现在被判为 `unknown_member`，不是 `in_use` | 已改（§6.4） |
| F2 | GPFS NSD v2 格式写 GPT，会被判为 `dirty`，同样危险 | 已改（§6.4），按分区类型 GUID 识别列入 V2 |
| F3 | 完整日志不能直接复用捕获镜像的上传接口 | 已改（§6.2.6）：新接口、令牌加用途前缀 |
| F4 | `bootHost` 的位置引用不准；放置组路径 `startCreationPlacement` 没覆盖 | 已改（§9.5、附录 A） |
| F5 | 迁移完成写库的是 `ApplyMigrationPlan`，其兼容分支会改所有卷的 `hyper` | 已改（§10、附录 A） |
| F6 | `create_local_pool.sh` 路径少了 `async_job/` | 已改（§3.1） |
| F7 | `DiskID` 存的是 by-id 链接名等，不是完整路径 | 已改（§6.4） |
| F8 | 通用 `tasks` 表有对外接口，存储任务不要写进去 | 已改（§5.9） |
| F9 | 改名时 HKDF 的固定参数不能改；`SECRET_KEY` 易混淆 | 已改（§5.10、D7） |
| F10 | 侧边栏没有「存储」分组 | 已改（§13.3） |
| F11 | `GET /storage_pools/:id` 也对成员开放 | 已改（§13.2） |
| F12 | `qemu-block-extra` 两个版本的包列表都没写 | 已改（§8.4、§15.2） |
| F13 | `rpcs/attach_volume.go` 回调会给共享卷写 `hyper` | 已改（附录 A，S2 / S3） |
| F14 | `rpcs/create_volume.go` 同上 | 已改（附录 A，S4） |
| F15 | `volume.go` 里 `hyper=0` 表示「未落盘」，共享卷会落进错误分支 | 已改（§5.6、§9.1、附录 A） |
| F16 | `releaseStorage` 会把共享池行当成本地池，所有客户端节点删不掉 | 已改（§5.5、附录 A） |
| F17 | `report_rc.sh` 的 `instance_pools` 认不出 GPFS 路径和 RBD 盘，开机保护无效 | 已改（§9.9、附录 A） |
| F18 | `pool_root` / `pool_enter` 写死本地池根 | 已改（§9.1、附录 A） |
| F19 | 容量未知时放行与现有规则相反 | 已改（§9.5）：拒绝 |
| F20 | 现有过期规则要求 `capacity_at`，管不到共享池 | 已改（§9.2）：单独一条规则 |
| F21 | 唯一索引与软删除冲突 | 已改（§5 开头，代码已用部分唯一索引） |
| F22 | 同步的轮询会排在长命令后面；HA 下双发 | 已改（§6.2.1、§6.2.4） |
| F23 | 未完成的分段上传要 Abort | 已改（§6.1） |
| F24 | 已有 `local_pool` 告警规则类型可扩展 | 不采纳：告警改由健康看护产生（§14.2），不走规则模板 |

### H.2 对抗式设计审查

| # | 发现 | 处置 |
|---|---|---|
| A-B1 | 任务引擎没有作业存活协议：重复执行、结果丢失、永远卡住、提交与下发之间的空档 | 已改（§6.2.2–§6.2.5）：持久作业目录与 `boot_id`、幂等启动、同集群锁、`stc_poll.sh` 为权威、outbox 重发、中止先杀作业再释放槽、选主 |
| A-B2 | 设备名过期，可能把 NSD 建到别的盘、擦错盘 | 已改（§6.4）：执行时按稳定 ID 解析并核对序列号 / WWN / 大小；`nsddevices` 只列出认领的盘；擦盘只接受稳定 ID + 序列号 |
| A-B3 | 隔离会自动失效（黑名单默认 1 小时；GPFS 租约驱逐后自动回来）；只断管理网时旧 QEMU 不会被发现 | 已改（§11.2、§11.4）：显式长有效期、一律 `mmexpelnode`、每次重新注册都对账、对账完成才解除隔离 |
| A-M1 | 共享卷沿用 `hyper=0` 会落进「未落盘」分支 | 已改（同 F15） |
| A-M2 | 两个故障组时描述符法定人数集中一侧；盘回来后要 `mmchdisk start`；故障组号不能用 hostid | 已改（§6.3、§7.1、§5.2） |
| A-M3 | 存储挂起会拖死心跳和命令队列；被驱逐后 QEMU 持有句柄可能无法重新挂载 | 前者已改（§9.2：每池常驻探测进程）；后者列为 V9，S6 设计恢复流程 |
| A-M4 | Ceph：自动内存调整、一台主机只能跑一个集群的守护进程、删除前停编排模块、密钥环找不到、两副本 | 已改（§4.1、§6.7、§6.8、§6.3、§8.2、§8.3、§8.6） |
| A-M5 | 永久宕掉的节点没有移除路径 | 已改（§7.5、§8.5、§7.6、§8.6）：离线移除；删除集群对离线节点补做清理 |
| A-M6 | 装了 GPFS 的节点上，外部集群的 NSD 会显示为空闲 | 已改（§6.4）：GPFS 分区类型的盘、盘头有数据的空白盘判 `unknown_member`（盘头全零的判 `free`，否则成员节点上的新盘加不进集群） |
| A-M7 | 基础副本在同步的 `launch_vm.sh` 里导入会占住队列；系统盘生成会覆盖已有文件 | 已改（§9.6、§9.7）：clapi 串行后台导入；目标已存在即失败；`existing=true` 绝不删盘 |
| A-M8 | 存储守护进程与内置池共用根文件系统 | 已改（§3.1、§6.5、§18.3） |
| A-M9 | 锁粒度太粗；自动加客户端被 409 丢弃；后台循环在两台 clapi 上重复执行 | 已改（§6.2.1、§5.9）：结构槽与池槽、`pending_clients` 排队、选主 |
| A-m1 | cephadm 自己写不带 `from=` 的 `authorized_keys`；不校验主机公钥；A1 仍在 | 已改（§6.6、§18.3），前者列为 V17 |
| A-m2 | 预检缺安全启动检查 | 已改（§6.5） |
| A-m3 | 安装会撞 dpkg 锁 | 已改（§6.5） |
| A-m4 | 以 root 执行 1.7 GB 自解压脚本；SHA-256 没有参照值 | 已改（§6.1）：直接解 tar 流；界面显示 SHA-256 供对照；签名校验列为 V19 |
| A-m5 | 分段上传、临时 RBD 镜像的残留清理 | 已改（§6.1、§9.6） |
| A-m6 | `rbd rm` 遇监听者拒绝在网络分区时不是防线 | 已改（§9.3、§12.3） |
| A-m7 | `SECRET_KEY` 改名与本方案无关、名字易混淆 | 已改（§5.10、D7）：本方案不改名 |
| A-m8 | 「只断管理网」与「用同一套集群模拟外部集群」两项验收不可测 | 已改（§16 S2、S6，§17.2） |
| A-S1 | S1 范围：软件包挪到 S2；补作业协议、身份核对、探测框架、`selftest` | 已改（§16 S1）；探测框架随第一个共享池驱动在 S2 / S3 做 |

### H.3 内部一致性

| # | 发现 | 处置 |
|---|---|---|
| C1 | 共享池清单按集群下发却写同一个文件 | 已改（§9.2、§6.8）：每集群一份 |
| C2 | 阶段范围矛盾：告警在 S3 验收、§9.9 不在任何阶段、RBD 数据盘迁移缺失 | 已改（§16） |
| C3 | 懒导入缺状态流转；克隆失败退回复制时 clapi 不知道 | 已改（§9.6、§9.7） |
| C4 | 外部 Ceph 缺客户端用户名与 mon 地址字段，命令写死 `cloudland` | 已改（§5.1、§9.3、附录 F） |
| C5 | 健康看护对外部集群、`degraded` 集群的处理与回调名缺失 | 已改（§5.1、§14.1） |
| C6 | 文档里的唯一索引与软删除冲突 | 已改（同 F21） |
| C7 | 共享池建池任务期间的状态 | 已改（§5.5、§7.4、§8.3）：`creating` / `error` |
| C8 | Ceph 的 `ssh_trust` 应在 bootstrap 之前 | 已改（§8.2） |
| C9 | 缺创建 `client.cloudland` 的步骤 | 已改（§8.2 `configure`） |
| C10 | 按 UUID 命名的密钥环找不到 | 已改（§6.8、§8.4） |
| C11 | 节点写不了控制面的 Prometheus 目标文件 | 已改（§8.2、§14.3） |
| C12 | 两条告警路径重复；GPFS 指标不全；用量口径不一 | 已改（§14.2）：只留健康看护一条 |
| C13 | 任务种类不全；不占集群的任务规则没写 | 已改（§5.9） |
| C14 | 重试拿到过期的设备名 | 已改（§6.2.5、`RetryFrom`） |
| C15 | 未定义的 `select=group-pool-...`；`select=` 可能回 `error=resource` | 已改（§9.3：`pickPoolHost` + `inter=`） |
| C16 | 调度没考虑放置组 | 已改（§9.5） |
| C17 | 不设配额的几个池各按整份容量分配；分层时容量口径 | 已改（§9.5） |
| C18 | NVRAM、基础副本的删除缺执行方；零引用副本挡住删池 | 已改（§9.6、§9.8、§7.4、§8.3） |
| C19 | 角色关系不清（Ceph `admin` 与 bootstrap、GPFS `client`） | 已改（§5.2） |
| C20 | 「版本固定」与两个系统版本冲突 | 已改（§8.1） |
| C21 | GPFS 自动启动会让隔离失效；归属保护没有期限 | 已改（§11.2、§11.4） |
| C22 | 预检的端口连通性怎么测；成员标记未定义 | 已改（§6.5） |
| C23 | 断点续传缺协议 | 已改（§6.1） |
| C24 | 正文的「要验证」与 V 项不对应 | 已改（§18.2，各节统一写「待验证」并对上 V 项） |
| C25 | `boot_disk` 缺 `existing`，GPFS 示例不全 | 已改（§9.7） |
| C26 | 接口缺口（改角色、升级、轮换、`PATCH /images`、逐盘擦除、删除外部集群） | 已改（§13.1） |
| C27 | `nvme` 映射；副本数按 GPFS 存储池校验；单节点形态 | 已改（§7.3、§6.3） |
| C28 | 磁盘 XML 在附录 F.1 不是 F.2 | 已改 |
| C29 | D5、D7 未决却已按决定写 | 已改：D7 改为不在本方案里改名；D5 仍按建议写，等用户定 |
| C30 | 字段缺失（仓库口令、自动加入开关、`gpfs_mounts` 无人写、`skipped` 未用） | 已改（§5.1、§5.9、附录 E）；`skipped` 从状态里删除 |
| C31 | 附录 A 的阶段与 S1 范围不一致 | 已改 |
| C32 | 「第 3 步之后」指代不清 | 已改（§9.7） |
| C33 | mon 台数 3 或 5 与允许 1 台不一致 | 已改（§5.2、§6.3） |
| C34 | 附录 B 漏了不在 `storage/` 目录的脚本 | 已改 |
| C35 | 回调名写法不统一 | 已改：回调名一律不带 `.sh`（与现有 `host_disks` 等一致） |
| C36 | 预检缺审计映射 | 已改（§13.4） |
| C37 | 「前缀」未定义 | 已改（§5.7） |
| C38 | 取日志是同步还是异步 | 已改（§6.2.6、§13.1） |
| C39 | CLAUDE.md 仍写旧文件名 | 不成立：CLAUDE.md 已更新 |
