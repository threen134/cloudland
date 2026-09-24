# VPN 网关设计（站点到站点 + 客户端到站点）

- **状态**：**V1 + V2 + V3 已实施并在 work-01/02/03 验证（2026-09-23 实施；2026-09-24 经代码复查、三次真实重启节点与产品调整又改了一轮；均未提交），V4 未做**。**正文已按实现更新到 2026-09-24**：实施中改掉的做法在原处写明「原设计为……，见 §14.x」，决策与踩坑过程保留在附录 C–F 与 §14（实施记录）。原状态：设计草案，经四轮复查修订：代码核对式复查（附录 C，13 处）、按「BGP 必做」重写（附录 D）、BGP 部分与既有章节的衔接复查（附录 E，12 处）、第三轮代码核对（附录 F，5 处按原稿实现 V1 即不成立）
- **日期**：2026-09-22，基于 `stage-01` 分支 `1c186c18`（文中行号以此为准；附录 F 的核对基于 `b99855f8`，`report_rc.sh` 的三行 LB 钩子现在是 448–450 行）
- **涉及**：clapi（`api/`）、节点脚本（`scripts/`）、cpgateway（代理白名单、配额）、前端（`web/`）、部署脚本
- **最接近的既有实现**：负载均衡（VRRP 主备 + 路由器 netns 内起进程 + 浮动 IP）。本文大量复用它的机制，差异见附录 A

---

## 0. 摘要

- **一个 VPC 最多一个 VPN 网关**，跑在 VPC 路由器的 netns 里（`router-N`），和负载均衡同构：主备两个节点、keepalived 持有公网浮动 IP、心跳守护进程、节点重启后由回调整体恢复。
- **两种接入共用同一个网关对象和同一个公网地址**，只是端口和数据面不同：
  - **站点到站点**：strongSwan / IKEv2（UDP 500、4500），必须用 IPsec 才能和机房现有设备（Cisco / 华为 / 飞塔 / 公有云 VPN）互通。
  - **客户端到站点**：WireGuard（UDP 51820），推荐理由与备选方案的取舍见 §2.1。模型上留了 `protocol` 字段，将来要加 IKEv2/EAP（零客户端安装）不用改表。
- **BGP 是必做项，路由方案 2026-09-23 确认采用「外部 eBGP + 内部汇总网段」**（§2.6 的方案 1，十个候选的对比在那一节）：**eBGP 只在持有浮动 IP 的那个节点上跑**（FRR 的 `zebra` + `bgpd`，会话建在隧道内的一对链路地址上），学到的明细前缀只存在于主节点；其余节点走用户声明的**汇总网段**静态路由 + 主节点上的 `blackhole` 兜底。不做每节点 iBGP——VRRP 子网里非主备节点只有 anycast 地址，**在节点之间根本不可路由**（命中本机 local 表直接回环），补齐它的代价和收益不成比例。
- **汇总网段一个值用在三处**：其余节点的静态路由、`nonat` 条目、**FRR 的入方向 prefix-list**。最后一条不是可选项——BGP 模式下 IPsec 的流量选择符必须放开成 `0.0.0.0/0`，内核策略不再过滤，**prefix-list 是唯一的边界**（§10）。
- **最难的一点是东西向回程路由**（§2.3）：VPC 网关是 anycast，虚拟机的回包只会进入**它自己所在节点**的 `router-N`，而隧道只在一个节点上。方案是在每个承载该 VPC 的节点的 `router-N` 上加一条指向「VPN 主节点 VRRP 子网地址」的静态路由，主备两端再用 keepalived 的 notify 脚本即时改写本地那一条。进程级切换零收敛，主节点整机宕机时靠心跳上报的主节点身份变化驱动 clapi 重发。设计估计收敛 1–20 秒；实测真实重启主节点时，跨 VPC 探测在第 8–23 秒中断，clapi 记录的主节点第 24 秒切换（§14.5）。
- **对端网段必须加进路由器 netns 的 `nonat` ipset**，否则去往对端的流量会被 `create_local_router.sh:92` 的 SNAT 规则改成 `169.x` 源地址。这一条同时也让 `create_local_router.sh:88` 放行隧道来的入站流量。
- **主节点的 `rp_filter` 必须是 loose，这是正常运行的前提，不是切换时才用到的细节**：只要虚拟机不在主节点上，它的包就从 `ns-<vrrp_vlan>` 进主节点、而源地址的路由在 `ns-<vni>`（§2.3）。`create_local_router.sh` 无条件显式设 2。
- **一个 VPC 一个 charon**：charon 的 pid 目录是编译期固定的，第二个实例会拒启。实施用 `unshare -m` 把网关目录下的 `run/` bind mount 到 `/run` 再启动（§2.2、§14.1）。
- **切主时 `vpn_notify.sh` 的第 0 步是恢复 fip 路由表的默认路由**（`set_route_table.sh`），漏了就是「切主后隧道永远建不起来」（§2.4）。
- **凭据不回读**：站点连接的 PSK 与网关自己的 WireGuard 私钥加密存库，任何 GET 接口都不返回；**客户端私钥根本不存**，平台生成时只在创建响应里返回一次，库里只留公钥。下发到节点一律走 `ShellEscape` + `<<'EOF'`。
- **隧道接口 MTU 按 1450 算，不是 1500**：`create_veth.sh:28` 把路由器 netns 侧的所有 veth（含公网口 `te-`）都设成 1450，外层封装的上限就是它。
- **客户端与远端站点之间禁止互通**（2026-09-24 产品决定）：网关两个节点的路由器 netns 在 `FORWARD` 最前面双向丢弃 `wg+` 与 `ipsec+` 之间的转发，推送路由和 BGP 怎么配都串不起来（§10、§14.7）。
- **网关可以整体停用**（2026-09-24）：停掉隧道、BGP 与 WireGuard，保留主备、公网 IP 与全部配置，启用后按最新配置恢复（§5.6、§14.8）。
- **分阶段**：V1（共用骨架 + 站点到站点 + BGP）→ V2（客户端到站点）→ V3（状态、告警、配额、审计补全）→ V4（可选：IKEv2/EAP 客户端接入、证书认证的站点连接）。
- **必须三节点测**：两个节点测不出回程路由的错误，和迁移那次「两节点测不出来，三节点以上会导错流量」是同一类陷阱。

---

## 1. 背景与目标

### 1.1 现状

平台目前没有任何 VPN 能力。用户要从机房或办公网访问 VPC 里的虚拟机，只能给每台虚拟机绑公网浮动 IP，再用安全组限制源地址。缺点是：公网 IP 消耗、每台机器都暴露在公网、访问的是公网地址而不是内网地址、没有加密。

可以直接复用的既有机制：

| 机制 | 位置 | 说明 |
|---|---|---|
| VPC 路由器 netns | `scripts/kvm/create_local_router.sh` | 每个 VPC 每个节点一个 `router-N`，INPUT 默认 DROP，有 `nonat` ipset 和 SNAT 规则，已开 `ip_forward` |
| VRRP 主备 | `api/src/services/loadbalancer.go:113` `CreateVrrpInstance` | 按 zone 调度出两个节点，在 VPC 的 VRRP 子网里各分配一个地址和 MAC |
| keepalived 持浮动 IP | `scripts/kvm/create_keepalived_conf.sh` | 两端都以 `state BACKUP` + `nopreempt` 启动，按角色给优先级；浮动 IP 挂在 `te-<router>-<vlan>` |
| 浮动 IP 的外网口与策略路由 | `scripts/kvm/create_lb_floating.sh` | `ext-`/`te-` veth、`fip-<vlan>` 路由表、进出方向限速 |
| 进程守护 | `scripts/kvm/check_lb_process.sh`，`report_rc.sh:217` | 每次心跳拉起异常退出的进程，与配置脚本用 `flock $lb_lock_file` 互斥 |
| 节点重启恢复 | `api/src/rpcs/recover_loadbalancer.go`，`report_rc.sh:206` | 按 `boot_id` 触发整体重建，重建时先 `set_vrrp_ip.sh` 再 `sendFdbRules` |
| 只由持有浮动 IP 的节点上报 | `scripts/kvm/report_lb_health.sh` | 变化或满 5 分钟才回调，避免刷爆 clapi |
| 静态转发条目下发 | `api/src/rpcs/launch_vm.go:39` `sendFdbRules` | `toall=group-fdb-<排除>:<h1>,<h2>` 定向下发到承载该 VPC 的节点集合 |

### 1.2 目标

1. 用户能在 VPC 上开一个 VPN 网关，得到一个公网地址。
2. **站点到站点**：配置对端设备的公网地址、认证方式、本端/对端网段，建立 IPsec 隧道；两侧的内网地址互相可达，**双向都能主动发起**。
3. **BGP**：对端新增或撤销网段时，我方自动学到，用户不需要回控制台改配置。
4. **客户端到站点**：用户为每个人（或每台设备）创建一个客户端，下载配置文件（或扫二维码）即可从笔记本连进 VPC 内网；虚拟机看到的是客户端的真实地址，云上也能主动访问客户端。
5. 主备高可用：一个节点挂掉，隧道在秒级重建；平台侧的路由自动收敛。
6. 网关的状态、每条隧道的状态和流量在界面上看得到。
7. 节点重启、路由器重建、虚拟机迁移之后自动恢复，不需要人工干预。

### 1.3 非目标

- **经典网络（无 VPC）不支持**。VPN 网关只能建在 VPC 上。
- **一个 VPC 多个 VPN 网关**：第一版限制为一个（§2.5 的 VRID 问题和回程路由都会因为多个网关复杂化）。
- **OSPF / IS-IS**：只做 BGP（BGP 是必做项，见 §2.6）。
- **每节点 iBGP**：BGP 学到的明细前缀只存在于主节点，其余节点走用户声明的汇总网段。理由和将来的升级路径见 §2.6。
- **VPC 到 VPC 的内部互联**：虽然可以用两个 VPN 网关互建站点连接来实现（测试时就这么做），但这不是解决 VPC 互联的正确做法，将来应该有专门的对等连接功能。
- **SSL-VPN（OpenVPN）的 TCP/443 穿透**：见 §2.1 的取舍，列在 §13 待决策。
- **按流量计费 / 带宽限速**：浮动 IP 本身的限速（`create_lb_floating.sh` 的 tc 规则）照常生效，不再单独为 VPN 做。
- **IPv6**。

---

## 2. 关键设计决策

### 2.1 协议选型

先说结论：**站点到站点用 strongSwan（IKEv2），客户端到站点用 WireGuard**。

**站点到站点只能是 IPsec**，没有第二个选项：用户的对端是机房里已有的防火墙或路由器，那些设备只会 IKEv1/IKEv2。用 IKEv2，IKEv1 不做（老设备要接入时再议）。

**客户端到站点有三种选择**：

| | WireGuard | IKEv2 + EAP-MSCHAPv2 | OpenVPN（SSL-VPN） |
|---|---|---|---|
| 客户端安装 | 要装（官方 App，全平台都有） | **不用装**（Win/mac/iOS/Android 系统自带） | 要装 |
| 服务端要素 | 一对密钥 | **要一套 CA + 服务端证书**，客户端得先导入 CA | 要一套 CA |
| 用户凭据 | 每客户端一对密钥 | 用户名 / 密码（要有用户库） | 证书或用户名密码 |
| 穿透能力 | UDP 51820，UDP 被封就不通 | UDP 500/4500，同左 | 可走 **TCP 443**，穿透最好 |
| 主备切换 | **几乎无感**（无连接状态，下一个握手即恢复） | 要重建 IKE SA | 要重建 TLS 会话 |
| 平台实现量 | **最小**（内核态，无守护进程，配置就是一组公钥 + AllowedIPs） | 大（CA 管理、证书签发与吊销、EAP 用户库、证书下发） | 中（CA 管理、配置文件生成、一个守护进程） |
| 保留客户端真实源地址 | 是（AllowedIPs 即路由） | 是（地址池） | 是（地址池） |

第一版选 WireGuard，因为它的实现量只有另外两个的一小半（不需要引入 CA 体系），主备切换几乎无感，而且和站点到站点的 strongSwan 互不干扰（各自一个 UDP 端口，共用一个公网地址）。

代价说清楚：**必须装客户端**，而且**没有用户名密码**——「给某人开通」等于「生成一份配置发给他」，「禁用某人」等于「删掉他的公钥」。如果业务上要求零安装（比如给外部人员临时开通）或者要求 UDP 被封时也能连，那就得上 IKEv2/EAP 或 OpenVPN，工作量和影响见 §12 的 V4。这一条列在 §13 待决策第 1 条。

模型里留 `vpn_client_configs.protocol`（`wireguard` / `ikev2`），网关上留 `client_protocol`，将来加第二种协议时表结构不用动。

### 2.2 运行位置：VPC 路由器 netns

和负载均衡一样，跑在 `router-N` 里，不做成独立的虚拟机网关。理由：

- 路由器 netns 已经是 VPC 所有子网的网关，隧道流量进来直接就在正确的三层位置上，不需要额外的网络接入。
- 主备、浮动 IP、进程守护、重启恢复这一整套已经有了（LB 用了一年多，节点真实重启验证过多次）。
- 做成虚拟机的话要占用户配额、要维护一个网关镜像、升级要重建虚拟机，而且虚拟机上的浮动内网地址会撞上安全组的反欺骗规则。

netns 对这两种数据面都没有问题：XFRM 的 state / policy 是按网络命名空间隔离的；WireGuard 接口在 netns 内 `ip link add` 出来，加密 socket 也在该 netns。

**strongSwan 的多实例隔离是这一节的前提，而且现成的办法不够用（2026-09-23 核对）**：charon 默认读 `/etc/strongswan.conf`、`/etc/swanctl/swanctl.conf`，这两个能用环境变量 `STRONGSWAN_CONF` 和 `swanctl --file` 改；vici socket 能用 `charon.plugins.vici.socket` 改；**但 pid 文件（`<piddir>/charon.pid`）的目录是编译期 `--with-piddir` 固定的，`strongswan.conf` 里没有任何选项能改它**（官方文档只有 `${piddir}` 这个替换变量，没有 `charon.pid_file` 之类的键）。第二个 charon 启动时看到 pid 文件里的进程还活着就直接退出（`charon already running`），所有 netns 共用同一个文件系统，所以「一个 VPC 一个 charon」按原稿根本起不了第二个。官方 netns 文档的做法是重新编译把 piddir 放到 `/etc` 下、再靠 `ip netns exec` 的 `/etc/netns/<name>/` 绑定挂载，对我们不现实。两个可行的绕法，**开工前先实测其一（§13 待验证第 1 条）**：

1. **私有挂载命名空间**：`ip netns exec router-N unshare -m sh -c 'mount --bind <vpn 目录>/run /var/run && exec charon'`（Ubuntu 的 `/var/run` 是 `/run` 的符号链接，bind 的目标要按包实际编译的 piddir 来，`strings /usr/lib/ipsec/charon | grep charon.pid` 可查）。charon 看到的 `/var/run/charon.pid` 与 `charon.vici` 实际落在 `<vpn 目录>/run/`，外面的 `swanctl` 用 `--uri unix://<vpn 目录>/run/charon.vici` 访问。`unshare -m` 只影响这个进程树，不动 netns 与宿主机。
2. **改用 `charon-systemd` 二进制**（包 `charon-systemd`）：它不写 pid 文件、同样认 `STRONGSWAN_CONF`，脱离 systemd 直接启动也能跑（`NOTIFY_SOCKET` 未设时 `sd_notify` 是空操作，收 SIGTERM 正常退出），日志默认走 journal，要改成 `charon-systemd.filelog`。要实测的是它在没有 systemd 看管时的守护与退出行为。

不管选哪个，`swanctl` 调用一律带 `--uri unix://<自己的 vici socket>`，配置一律用 `--file <自己的 swanctl.conf>`。

WireGuard 是内核态的，没有守护进程，天然隔离。

### 2.3 东西向回程路由（本设计的核心难点）

**问题**。VPC 网关是 anycast：每个节点的 `router-N` 都持有同一个网关 IP 和按 hostid 生成的网关 MAC（`cloudrc` 的 `subnet_gw_mac`）。虚拟机发往非本 VPC 地址的包，一定进入**它自己所在节点**的 `router-N`。而 IPsec / WireGuard 隧道只存在于一个节点上。

举例：客户端 `10.8.0.5` 访问虚拟机 `192.168.62.5`（在 work-03）。

- 去程没问题：隧道在 work-01 终结，包进 `router-N@work-01`，从 `ns-<vni>` 出去，经 VXLAN 到 work-03 的网桥，进虚拟机。
- 回程断了：虚拟机回包给 `10.8.0.5`，网关是 work-03 本地的 `router-N`。那里没有隧道，也没有到 `10.8.0.0/24` 的路由，于是走默认路由经 `ti-N` 到 `router-0`，被 SNAT 成 `169.x` 丢进公网。

**不采用的做法：在网关上 SNAT**。把隧道来的流量 SNAT 成主节点在 VRRP 子网里的地址（haproxy 就是这么绕开这个问题的：`source` 用本端 VRRP 地址，虚拟机的回包自然回到那台机器）。这样回程不需要任何额外路由。但代价是虚拟机看不到真实来源地址（安全组、审计、应用白名单全部失效），而且**云上无法主动发起**到对端 / 客户端——站点到站点必须双向，所以这条路直接堵死。客户端到站点单独用 SNAT 也不值得，会让两种接入的行为不一致。

**采用的做法：每个节点一条静态路由 + 两跳兜底**。

1. **VRRP 子网是现成的节点间三层通道**。VPC 的 VRRP 子网（`Router.VrrpSubnetID`，`192.168.196.0/24`）里，主备各有一个**固定不漂移**的地址（MASTER / BACKUP 两个 `Interface` 记录）。`sendFdbRules` 会把 VRRP 网卡的转发条目和邻居表项下发到**所有承载该 VPC 的节点**，`add_fwrule.sh` 在收到条目、发现 `ns-<vrrp_vlan>` 不存在时会把它建出来。所以任何一个承载该 VPC 的节点，都能在二层直达主备两个 VRRP 地址。

2. **所有节点（含主备自己）装一条路由**：

   ```
   ip netns exec router-N ip route replace <对端网段> via <VPN 主节点的 VRRP 地址> dev ns-<vrrp_vlan>
   ```

   **「对端网段」是一个派生集合**（§3.4），三个来源：

   | 来源 | 内容 | 变化时机 |
   |---|---|---|
   | `connection_static` | 静态连接的 `remote_cidrs` | 用户改配置 |
   | `connection_summary` | **BGP 连接的汇总网段** `remote_summary_cidrs`（§2.6） | 用户改配置 |
   | `client_pool` | 客户端地址池 `client_cidr` | 用户改配置 |

   三个来源**都只随 API 变化**，不随 BGP 运行时的收敛变化——这是让 §2.6 的设计成立的关键，下面会讲为什么。由 clapi 用 `toall=group-fdb-...` 的方式下发给承载该 VPC 的节点集合（复用 `sendFdbRules` 里那段构造 hyper 集合的代码）。

3. **主备两端自己改写本地那一条**，由 keepalived 的 notify 脚本在切换瞬间执行，不经过 clapi：

   - `notify_master`：
     - 静态连接、客户端池：`ip route replace <网段> dev <ipsec-K / wg-K>`（走隧道）
     - **BGP 连接：`ip route replace blackhole <汇总网段>`**。明细路由由 bgpd 装进来，比汇总更长，自然胜出；汇总本身做兜底，防止某条前缀被对端撤销之后流量顺着默认路由漏到公网去（§2.6）
   - `notify_backup`：一律 `ip route replace <网段> via <对端 VRRP 地址> dev ns-<vrrp_vlan>`（BGP 连接的汇总也是，因为这时本机没有明细）
   - `notify_fault`：一律 `blackhole`——本端 VRRP 网卡坏了、收不到对端通告，对端是不是主无从得知，两端同时 FAULT 时互指会成环（§2.4）

   于是正常的主备切换（keepalived 被杀、进程重启、手工切换）**收敛为零**：其他节点的 nexthop 仍指向原主节点，原主节点此刻已经把自己的那条改成了指向新主节点，多一跳而已。

   ⚠️ **主备两个节点上，这几条路由只有 `vpn_notify.sh` 一个归属**。建 XFRM / WireGuard 接口的脚本**不许**顺手装 `dev <隧道接口>` 的路由——备节点上一旦被抢先装成指向本地 wg 接口，流量就被吸进一个没有任何握手的接口里，直接黑洞，而且现象是「切换后好一阵子不通、重启一下进程又好了」，极难排查。接口建好后由 `vpn_notify.sh` 按当前角色装一次（脚本启动时先读 `ip addr` 判断本节点有没有浮动 IP，避免 keepalived 还没触发过 notify 时路由是空的）。

   ⚠️ **`vpn_notify.sh` 是 keepalived 的子进程，不是 cloudlet 执行的命令**，它的 stdout 不走 `|:-COMMAND-:|` 回调协议，写在里面的回调不会有任何人收到。主节点身份的变化只能靠心跳上报（第 4 点）。

4. **主节点整机宕机**时第 3 步不成立（那台机器没了，nexthop 不可达）。这时靠心跳：`report_vpn_status.sh` 只在持有浮动 IP 的节点上报，clapi 发现网关的主节点变了，就把第 2 步的路由重发给其余节点。收敛时间 = 心跳间隔（1–20 秒随机）+ 下发。这段时间里隧道本身也在重建，用户感知是一次性的。

5. **对端网段必须加进 `nonat`**（每个节点的 `router-N` 都要加）：

   ```
   ip netns exec router-N ipset add nonat <对端网段>
   ```

   否则 `create_local_router.sh:92` 的 `POSTROUTING -m set --match-set nonat src -m set ! --match-set nonat dst -j SNAT --to-source $local_ip` 会把 VPC 去往对端的流量源地址改成 `169.x`；浮动 IP 的 SNAT 规则（`create_floating.sh` 的 `-s $int_ip -m set ! --match-set nonat dst`）同理。加进 `nonat` 还有一个副作用是 `create_local_router.sh:88` 的 `INPUT -m set --match-set nonat src -j ACCEPT` 会放行隧道来的入站流量（比如 ping 网关），这是期望的。

   **BGP 连接放汇总网段就够**：`nonat` 是 `hash:net`，存 `10.0.0.0/8` 时查 `10.1.2.3` 是命中的。所以学到的明细前缀不需要、也不应该进 ipset——否则每次 BGP 收敛都要去改一遍每个节点的 ipset，这正是 §2.6 要避免的事。

#### 谁拥有哪条路由（实现时先对照这张表）

复查里连续两轮都出现「同一条路由有两个归属」的错误，所以把它显式列出来。**改动任何下发逻辑之前先对照这张表。**

| 节点角色 | 网段类型 | 谁写 | 装成什么 |
|---|---|---|---|
| 其余节点 | 全部 | **clapi**（`set_vpn_route.sh <网关ID> <网段> via <主节点 VRRP 地址>`） | `via <主节点 VRRP 地址> dev ns-<vrrp_vlan>` |
| 主备两节点 | 全部 | **clapi 只写 `tunnel_routes` 文件，不装路由**；`vpn_notify.sh` 按当前角色装 | 见下四行 |
| └ 主（持 VIP） | 静态连接 | `vpn_notify.sh` | `dev ipsec-<if_id>` |
| └ 主（持 VIP） | BGP 连接的汇总 | `vpn_notify.sh` | **`blackhole`**（明细由 bgpd 装，更长前缀自然胜出） |
| └ 主（持 VIP） | 客户端池 | `vpn_notify.sh` | `dev wg-<网关ID>` |
| └ 备（无 VIP，BACKUP） | 全部 | `vpn_notify.sh` | `via <对端 VRRP 地址> dev ns-<vrrp_vlan>` |
| └ FAULT（VRRP 网卡故障） | 全部 | `vpn_notify.sh` | `blackhole`——对端状态未知，指过去可能成环（§2.4） |

三条硬规则：

1. **建接口的脚本（XFRM / WireGuard）一律不装路由**，只往 `tunnel_routes` 里追加一行。
2. **clapi 对主备节点不下发路由**，只下发配置和 `tunnel_routes` 的内容。
3. **`nonat` 条目与路由归属无关**，一律由 clapi 下发到所有节点（含主备），因为它不随角色变化。

**需要注意的四件事**：

- **rp_filter 必须是 loose（2），这是正常运行的前提，不是切换时才用到的细节（2026-09-23 改正）**。路径不对称在**正常运行**时就存在：只要虚拟机不在主节点上，它去往对端的包就从 `ns-<vrrp_vlan>` 进主节点，而主节点上该源地址的路由在 `ns-<vni>`——strict 模式（1）会把**所有非主节点上虚拟机的 VPN 流量**丢掉。两跳切换只是同一件事再多一跳（原主节点从 `ns-<vrrp_vlan>` 收、再从同一接口转给新主）。另外原稿说「netns 有独立的 sysctl 且不继承宿主机当前值」是反的：内核 `net.core.devconf_inherit_init_net` 默认为 0，新 netns 的 IPv4 `conf/all`、`conf/default` **从 init_net 复制**，现在没出事只是因为 Ubuntu 的默认值就是 2。IPsec 解封装后的包带 secpath、跳过 rp_filter，WireGuard 的不跳过（源地址的路由就是 `wg-<id>`，能过）。所以：**`create_local_router.sh` 无条件显式设 `net.ipv4.conf.all.rp_filter=2`、`net.ipv4.conf.default.rp_filter=2`**，不依赖宿主机的值（有人按加固指南把宿主机改成 1，VPN 就整个不通，而且只在虚拟机不在主节点时复现）。§13 待验证第 3 条只是确认实测值，不再决定设计。
- **安全组**。隧道来的流量最终要穿过虚拟机 tap 上的安全组链。默认安全组只对全网放行 22/3389，所以用户建完 VPN 之后还要放行对端网段。界面上要在网关详情页明确提示，不要让用户以为是 VPN 没通。
- **ICMP 重定向**。两跳时原主节点是从 `ns-<vrrp_vlan>` 收、又从 `ns-<vrrp_vlan>` 发，Linux 会给第三方节点回 ICMP redirect。转发节点的 `accept_redirects` 默认是 0（开了 `ip_forward` 就不接受重定向），所以不会污染路由表，但日志和抓包里会多一堆没用的东西、排查时容易被带偏。建议在 `create_local_router.sh` 里一并 `net.ipv4.conf.all.send_redirects=0`。
- **MTU：外层上限是 1450，不是 1500**。`create_veth.sh:28` 把路由器 netns 侧的 veth 一律设成 `mtu 1450`，公网口 `te-<router>-<vlan>` 也是由 `create_veth.sh` 建的，所以外层封装后的包最大只有 1450 字节。据此：

  | | 外层上限 | 封装开销 | 隧道接口 MTU |
  |---|---|---|---|
  | IPsec（ESP + NAT-T UDP 封装） | 1450 | ~73–88 | **1360** |
  | WireGuard | 1450 | 60 | **1390** |

  另外 **IKE 自己的报文也受 1450 限制**（证书或大提案会超），`swanctl.conf` 要开 `fragmentation=yes`（IKEv2 分片，RFC 7383），否则和某些对端协商到一半就没声音了。

  MSS 钳制仍然要做，非 TCP 流量（UDP、ICMP 大包）只能靠上面的 MTU 和 PMTUD：

  ```
  iptables -t mangle -A FORWARD -o <隧道接口> -p tcp --syn -j TCPMSS --clamp-mss-to-pmtu
  iptables -t mangle -A FORWARD -i <隧道接口> -p tcp --syn -j TCPMSS --clamp-mss-to-pmtu
  ```

### 2.4 高可用

沿用负载均衡那一套，每个 VPN 网关一个 `VrrpInstance`（`select=` 按 zone 调度两个节点，第二个排除第一个），keepalived 持有公网浮动 IP。差别：

- **charon 和 `zebra`/`bgpd` 都只在持有浮动 IP 的节点上运行**，由 `notify_master` 启动、`notify_backup` / `notify_fault` 停止；心跳里的守护脚本也只在本节点持有 VIP 时才保证它们活着。理由是 `left=<VIP>` 的连接在没有该地址的节点上既发不出也收不到，BGP 会话又跑在隧道里、隧道没起就没有，让它们跑着只是白占内存和制造误导性的日志。停 bgpd 时它学到的路由随之从内核撤销，正好是我们要的。
- **WireGuard 接口两端都建、都配一样的 peer**，`wg` 绑 `0.0.0.0:51820`，只有持有 VIP 的那台会收到客户端的包。**进程层面不需要 notify 参与**（没有守护进程，接口一直在），但**客户端池的路由归 notify 管**：主节点指向 `wg-<id>`，备节点指向对端 VRRP 地址（见 §2.3 的那条 ⚠️）。切换后客户端的 `PersistentKeepalive`（建议 25 秒）会在几秒内把会话带到新主节点。
- **云上主动访问客户端有个前提**：WireGuard 回包用的是「收到握手时那个包的目的地址」作为源地址，所以只有客户端先连上过、且 `PersistentKeepalive` 维持着，网关才知道该往哪发、用哪个源地址。从没连过的客户端，云上发不过去（这是 WireGuard 的固有特性，不是本设计的缺陷）。`PersistentKeepalive` 要写进下发给客户端的配置模板里，不能留给用户填。
- **IKE SA 不做状态同步**。strongSwan 的 HA 插件依赖 ClusterIP，和这里的 VRRP 模型对不上，不用。切换后隧道靠 DPD 和重协商重建：对端设备一般 30 秒内发现，我方 `notify_master` 里对所有「主动发起」的连接立刻 `swanctl --initiate`，实测应该在 5–10 秒内恢复，具体数字实测后回填。这一条要写进产品文档，不能让用户以为是「无感切换」。

#### `notify_master` 里的顺序是有要求的

```
0. 执行 set_route_table.sh，恢复 fip 路由表的默认路由（见下）
1. 按 tunnel_routes 装路由（BGP 连接装 blackhole）
2. 起 charon，对 initiator 连接 swanctl --initiate
3. 起 zebra，再起 bgpd
```

- 🔴 **第 0 步不能省（2026-09-23 补，原稿漏了；漏了就是「切主后隧道永远建不起来」）**。`create_lb_floating.sh:29` 把 `ip route replace default via <网关> table fip-<vlan>` 写进 `$ROUTES_FILE`；浮动 IP 被摘除时内核按 prefsrc 把这条路由一起删掉，LB 靠 `notify_master` 的 `set_route_table.sh` 把它 eval 回来（`check_lb_process.sh:28` 的注释写明了这件事）。VPN 用 `vpn_notify.sh` 替换了 notify_master，这一步就没人做了：新主节点上源地址为 VIP 的 IKE / ESP / WireGuard 外层包命中 `from <VIP> lookup fip-<vlan>` 却查到一张空表，落回 main 表的默认路由，经 `ti-N` 到 router-0 被 SNAT 成别的地址——对端看到的源地址不对，IKE 协商不上、WireGuard 握手无人应答。`start_keepalived`（`cloudrc:215`）已经导出了 `ROUTES_FILE` / `KEEPALIVE_CONF`，`vpn_notify.sh` 直接调 `set_route_table.sh` 即可（它还顺带做浮动 IP 的免费 ARP）。同理 `create_vpn_gateway.sh` 调 `create_lb_floating.sh` 之前必须 `export ROUTES_FILE=$vrrp_dir/routes`，否则那一行写不进文件。
- **第 1 步必须在第 3 步之前**。新主节点刚起来时还没学到任何明细，这几秒里流量到了这台机器：有 blackhole 就被丢掉（正确）；**没有的话会落到默认路由、经 `router-0` SNAT 漏到公网**——一次主备切换就是一次内网流量泄漏。
- **`zebra` 必须在 `bgpd` 之前**（§4.5）。
- **`swanctl --initiate` 要确认带 `INITIAL_CONTACT`**（strongSwan 发起时默认带，但别被配置关掉）。切换那一刻对端多半还持有旧的 IKE SA、尚未 DPD 超时，新的 IKE_SA_INIT 从**同一个公网 IP** 过来，对端要靠这个通知才知道该清掉旧的。缺了它，恢复时间会从几秒变成「等对端 SA 超时」，可能几十秒。

#### ⚠️ `kill -9` 时 notify 根本不执行

keepalived 被 `kill -9` 时 **`notify_backup` 不会运行**（LB 那边实测过：全部进程被 `kill -9` 时残留浮动 IP 要约 15 秒后才由守护脚本摘除）。于是原主节点上会留下：charon 和 bgpd 还在跑、本地路由还指向自己的隧道口、而 VIP 已经被对端接管——**其余节点的流量送过来直接进黑洞，而且不会自愈**。

所以 `check_vpn_process.sh` 必须是**双向**的（§4.5），不能只管"持有 VIP 时把进程拉起来"。

#### 双主对 VPN 的伤害比对 LB 大得多

附录 B 第 2 条（`clear_vrrp_ip.sh` 按 MAC 删 fdb）会让同 VPC 里删一个 LB 就把 VPN 网关打成双主。LB 双主只是两台 haproxy 都在服务；VPN 双主是**两台 charon 用同一个 VIP 向同一个对端发起**，各自带 INITIAL_CONTACT 清掉对方刚建好的 SA，对端设备上 SA 反复重建；WireGuard 两端都握手，客户端的会话在两台之间跳。同时 §6.1 第 1 类上报「后到优先」会让第三方节点的 nexthop 每次心跳翻转一次。所以：

- 附录 B 第 2 条在 V1 里**先**修（§12），不是顺手。
- clapi 收到第 1 类上报时，若旧主 30 秒内还上报过、另一个 hostid 又声称 master，**不翻转 `master_hyper`、只记警告并计数**（实施值 30 秒，原设计 60 秒；新主接管后第一分钟每次心跳都重报，窗口一过就能生效，§6.1）（V3 接告警）。双主本身要靠修 B2 和节点侧守护来消除，路由翻来翻去只会扩大故障面。

#### FAULT 状态不能指向对端

`notify_backup` 把本地路由指向对端 VRRP 地址是对的：keepalived 处于 BACKUP 意味着它收到了对端的通告，对端就是 MASTER。但 `notify_fault`（本端 VRRP 网卡故障）不同——这时本端根本收不到通告，对端是不是 MASTER 无从得知；两端同时 FAULT 时 A 指 B、B 指 A，第三方节点送来的包在 VXLAN 上来回直到 TTL 耗尽。所以 **`notify_fault` 装 `blackhole`，不装 `via <对端>`**（§2.3 归属表已按此列出），§4.2 里 `notify_fault` 的参数是 `fault` 而不是 `backup`。

### 2.5 命名与既有约束

- **不要用 `site` 这个词**。`Subnet.Type` 里已经有 `site`（`api/src/common/constant.go:27`），指的是一段可以整体挂到虚拟机上的公网地址，和「站点到站点」完全是两回事。本文统一用：网关 = `vpn_gateway`，站点连接 = `vpn_connection`，客户端 = `vpn_client`。中文用「VPN 网关」「站点连接」「VPN 客户端」。
- **`Subnet.IsSite` 字段全仓库没有任何读写点**（只有 `model/subnet.go:25` 一处定义），不要误以为它和站点有关。
- **VRID 只有 8 位（既有缺陷，见附录 B 第 1 条）**。`create_keepalived_conf.sh:64` 直接用 `VrrpInstance` 的自增主键当 `virtual_router_id`，超过 255 时 keepalived 会拒绝整个 `vrrp_instance`。VPN 网关会再消耗 VrrpInstance ID，让这个问题提前暴露。V1 里顺手修掉：在 `vrrp_instances` 上加 `vrid` 列，创建时在**同一 VRRP 子网内**分配 1–255 的空闲值，配置脚本改用它。
- **VRRP 子网的回收判据现在只看 LB（既有代码，附录 B 第 8 条）**。`LoadBalancerAdmin.Delete` 末尾按 `load_balancers` 计数，为 0 就删掉 VRRP 子网并清零 `router.vrrp_subnet_id`。VPC 里有 VPN 网关时删掉最后一个 LB，网关两个 VRRP 地址所在的子网就没了，之后再建 LB 会新建第二个 `192.168.196.0/24`。V1 改成按该路由器的 `vrrp_instances` 计数，网关的删除走同一个判据（§5.4）。

### 2.6 BGP：只在主备两个节点跑，内部走汇总网段

**结论（2026-09-23 确认）：采用「外部 eBGP + 内部汇总网段」，即下表的方案 1。** 这条不再是待决策项，V1 按此开工。

#### 候选方案与否决理由

问题是：**BGP 学到的明细前缀只存在于主节点，其余承载该 VPC 的节点怎么知道去对端的路。** 评估过的全部候选：

| # | 方案 | 非主节点怎么知道路由 | 结论 |
|---|---|---|---|
| 1 | **汇总网段** | 用户声明一个粗范围，一条静态路由指向主节点；主节点跑 eBGP 学明细 + blackhole 兜底 | ✅ **采用** |
| 2 | **clapi 中继明细** | 主节点把学到的明细经心跳上报，clapi 下发静态路由给其余节点 | 🟡 备选兜底（V3，用户给不出汇总时启用；收敛 2–30 秒） |
| 3 | **要求对端只通告聚合** | 对端设备上配 `aggregate-address`，我们学到的就只有一条粗路由，直接分发全集群 | 🟡 可选，取决于对端有没有能力 / 意愿改配置 |
| 4 | **每节点 iBGP** | 每节点跑 zebra+bgpd，主节点 `next-hop-self` 把明细反射过去 | ❌ anycast 地址不可路由（硬阻碍，见下），补齐后还剩八个问题 |
| 5 | **每节点轻量 agent 分发** | 主节点经自研协议把明细推给各节点的小进程 | ❌ 等于自研一个 iBGP，同样卡在唯一地址上 |
| 6 | **网关上 SNAT** | 不需要路由——隧道流量 SNAT 成主节点地址，回程自然回来 | ❌ 丢失真实源地址，且**云上无法主动发起**，站点到站点不成立（§2.3） |
| 7 | **每节点各自建隧道** | 不需要分发——每个节点都有自己的隧道 | ❌ 要 N 个公网 IP，对端要配 N 个 peer 和 N 个 BGP 邻居 |
| 8 | **VPN 网关做成虚拟机** | 路由指向一个普通 VM 的 IP（平台本来就维护它的 fdb/neigh） | ❌ HA 的浮动内网 IP 撞安全组反欺骗，还要占配额、维护镜像（§2.2） |
| 9 | **非主节点默认路由指向主节点** | 不用声明范围，所有非本 VPC 流量都交给主节点 | ❌ 出公网流量一并被吸走，全 VPC 上网挤在一台机器 |
| 10 | **虚拟机侧下发路由**（`Subnet.Routes` / DHCP 121） | 绕开路由器，让虚拟机自己知道下一跳 | ❌ 仍然需要一个可达的下一跳，单独不成立；且只对带 cloud-init 的镜像有效 |

#### 方案 4（每节点 iBGP）为什么是硬阻碍

直觉上最正统的做法是：主节点对外跑 eBGP，各节点之间在 VRRP 子网上跑 iBGP（主节点 `next-hop-self`），明细前缀直接传播到每个节点，亚秒级收敛，clapi 完全不参与。

但在这个代码库上它跑不起来：

> **VRRP 子网里，非主备节点只有 anycast 网关地址 `192.168.196.1`，每个节点都一样。**

`CreateVrrpInstance`（`services/loadbalancer.go:133`）只创建**两个** `Interface` 记录（MASTER / BACKUP，拿到 `.2` / `.3`）。第三个节点上的 `ns-<vrrp_vlan>` 是 `add_fwrule.sh` → `set_subnet_gw.sh` 当作普通网关口建出来的，配的是 `Subnet.Gateway`，**全集群同一个值**。

**而且不只是「主节点分不清谁是谁」，是包根本出不去**：work-03 发往 `192.168.196.1` 的包命中**本机 local 表**，直接回环，一个字节都不进 VXLAN——anycast 地址在节点之间**不可路由**。

之所以这套 anycast 不打架，是因为 VXLAN 没有配泛洪条目：虚拟机 ARP 网关时广播出不了本节点，只有本地路由器应答（虚拟机之间跨节点的 ARP 走另一条路：`vxlan.proxy` + `add_fwrule.sh` 写的静态 `ip neigh` 代答）。

**推论：转发成立、建会话不成立。** 转发时包里的源地址是虚拟机的 IP，anycast 地址从不出现在线路上，所以 §2.3 的静态路由方案完全没问题；而 BGP 要建 TCP 会话，本机地址就是通信的一端。

要补上就得给每个承载该 VPC 的节点在 VRRP 子网里分配唯一地址，并在节点加入 / 退出该 VPC 时动态增删——**那是一块全新的地址管理逻辑，而且漏掉任何一个触发点都是流量黑洞**。再加上每 VPN VPC × 每节点一个 `zebra` + `bgpd`、iBGP 的 hold timer 默认 60/180 秒（宕机收敛**比现在靠心跳的 1–20 秒还慢**）、`nonat` 随 BGP 收敛变成动态的、RR 角色与 `notify_backup` 停进程冲突、FRR 多实例在 netns 里的集成、排障层数从 5 层变 10 层。

**代价和收益不成比例**：唯一真正的用户可见收益只有「不用填汇总网段」一条。

> ⚠️ 「每个节点在 VPC 内有唯一地址」这个能力**本身**可能值得做，但要按**排障工具**立项评估（现在从一个节点的 `router-N` 根本没法 ping 另一个节点的 `router-N`，跨节点 VPC 网络出问题只能读 `bridge fdb` 推断）。那种定位下可以做成「按需临时分配 + 自动回收」，不需要严格的生命周期，成本低得多。**它不是 VPN 的前置依赖，不要写进 V1。**

#### 采用的做法

```
                      eBGP (在 IPsec 隧道里)
   对端设备  ◀───────────────────────────────▶  主节点 router-N（持有浮动 IP）
                                                   │ 学到明细：10.1.2.0/24, 10.1.5.0/24 …
                                                   │ 装进本 netns 路由表
   其余节点 router-N ──静态：10.0.0.0/8 via 主节点 VRRP 地址──┘
```

- **eBGP 只在持有浮动 IP 的节点上跑**，和 charon 一样由 `vpn_notify.sh` 启停（§2.4）。会话建在**隧道内的一对链路地址**上（`tunnel_local_ip` / `tunnel_peer_ip`，一个 /30），所以 XFRM 接口必须配地址——这是和「纯策略模式」最大的不同。
- **其余节点只有一条静态路由**：用户声明的**汇总网段** `remote_summary_cidrs`（比如 `10.0.0.0/8`），走 §2.3 那套机制下发。它只随 API 变化，BGP 怎么收敛都不影响它。
- **主节点上给汇总网段装 `blackhole`**（§2.3 第 3 点）。BGP 明细比汇总更长，转发时自然胜出；某条前缀被对端撤销后，流量落到 blackhole 被丢掉，而不是顺着默认路由漏到公网——**没有这条兜底，一次 BGP 撤销就变成一次内网流量泄漏到互联网**。
- **`nonat` 也只放汇总网段**就够（§2.3 第 5 点），`hash:net` 会命中。

#### 举个完整的例子

对端机房有四个网段，将来还会加：`10.10.1.0/24`（办公）、`10.10.2.0/24`（研发）、`10.10.5.0/24`（测试）、`10.20.0.0/16`（数据中心）。云上 VPC 是 `192.168.62.0/24`，虚拟机分散在三个节点，VPN 主节点是 work-01。

**用户只填一项**（那四个网段不用填，BGP 自动学）：

```json
{ "route_mode": "bgp", "remote_summary_cidrs": "10.0.0.0/8", "local_asn": 65010, "peer_asn": 65001 }
```

**结果**：

```
work-01（主节点）              work-02 / work-03
  10.10.1.0/24  dev ipsec-7      10.0.0.0/8 via 192.168.196.2 dev ns-4098
  10.10.2.0/24  dev ipsec-7      （就这一条，静态）
  10.10.5.0/24  dev ipsec-7
  10.20.0.0/16  dev ipsec-7
  blackhole 10.0.0.0/8
```

`vm-b`（work-02）访问 `10.10.2.8`：本地路由器只有汇总命中 → 送到 work-01 → 那里 `10.10.2.0/24` 更精确（最长前缀匹配）→ 进隧道。**work-02 从头到尾不知道 `10.10.2.0/24` 存在。**

| 事件 | 发生了什么 |
|---|---|
| 对端**新增** `10.10.7.0/24` | work-01 秒级学到；work-02/03 **零操作**（本来就在 `10.0.0.0/8` 里）；clapi 不参与 |
| 对端**撤销** `10.10.2.0/24` | work-01 上明细消失，流量落到 `blackhole` 被丢——**没有这条兜底就会顺着默认路由漏到公网** |
| 主备**切换** | 汇总网段一个字不变，只是 nexthop 从 `.2` 换成 `.3` |

#### 汇总网段的双重身份：它本来就该有

把汇总网段当成「为了绕开拓扑限制的妥协」是**定位错了**。它更准确的身份是**信任边界声明**——「我允许这条隧道往我这边注入多大范围的路由」。

**任何认真的 BGP 部署都必须有入方向前缀过滤**，否则对端一个误配置就能搞死你：

| 对端误通告 | 没有过滤的后果 |
|---|---|
| `0.0.0.0/0` | 你整个 VPC 的**出网流量全被吸进隧道** |
| `192.168.62.0/24`（你自己的 VPC 网段） | **内部通信被黑洞**——同 VPC 虚拟机互访的流量被送进隧道，而 BGP 邻居显示一切正常 |
| 一万条明细 | 主节点路由表被打爆 |

所以**同一个值用在两处**（配置生成见 §4.3）：

1. 其余节点的静态路由（内部那一级）
2. FRR 的 `ip prefix-list` + `route-map ... in`（安全边界），再配 `maximum-prefix` 兜底

这也意味着：**就算将来真上了每节点 iBGP、不再需要汇总来做内部路由，这个声明也得保留**，因为它的安全职责不会消失。

#### 填错会怎样，以及界面要做的提示

- **填太大**（如 `0.0.0.0/0`）：work-02/03 会把**所有**流量都送给主节点，包括出公网的；主节点上没有对应明细的被 blackhole 丢掉 → **整个 VPC 断网**。接口必须拒绝 `0.0.0.0/0`，以及与 VPC 子网、VRRP 子网、`169.0.0.0/8` 重叠的值（§10）。
- **填太小**（只填了 `10.10.0.0/16`，漏了 `10.20.0.0/16`）：**同一个 VPC 里，有的虚拟机能访问、有的不能**——主节点上的虚拟机走本机 BGP 明细，通；其他节点的查不到路由，不通；虚拟机迁移一次症状还会变。这是最难查的一类故障。

  ✅ **自动校验**：BGP 的入方向过滤会直接拒掉范围外的前缀，`report_vpn_status.sh` 把**被拒绝的前缀**一并上报（§6.1），界面在连接详情页明确提示「对端通告的 `10.20.0.0/16` 不在你声明的范围内，已拒绝」。比事后告警更准确，基本消除了这个风险。

- **可以填多个,不用自己算超网**：`remote_summary_cidrs` 是复数，`10.10.0.0/16, 10.20.0.0/16` 完全可以。宁可多列几条，也别为了凑成一条而填得过大。

#### 还要知道的两点

1. **汇总内、但当前没学到的地址会在主节点被丢弃**，而不是走别的路。实际上也没有别的路可走，所以这是正确行为，只是排查时要知道 blackhole 是设计的一部分。
2. **明细前缀的粒度只在主节点上存在**。其余节点看不到「现在学到了哪些」，排查得上主节点。上报到界面的学到前缀列表（§6.1）就是给这个用的。

#### 流量选择符必须放开

**BGP 模式下 IPsec 的 TS 必须是 `0.0.0.0/0 ↔ 0.0.0.0/0`**，不能像静态模式那样按网段精确给——学到的前缀事先不知道，不在 TS 里的流量会被内核策略直接丢掉。所以 `vpn_connections` 要有 `route_mode`（`static` / `bgp`），`swanctl.conf` 的 `local_ts` / `remote_ts` 按它生成（§4.3）。这一点对端设备也要对应配置成路由模式，属于双方都要约定的事，界面上要写明。

---

## 3. 数据模型

新增 4 张表，另外在 2 张已有表上加列。全部走 `dbs.AutoMigrate`。

### 3.1 `vpn_gateways`

| 列 | 类型 | 说明 |
|---|---|---|
| `id` / `uuid` / `created_at` / ... | | `model.Model` |
| `owner` | int64 | 组织 ID |
| `name` | varchar(64) | 与 `router_id` 组成唯一索引 |
| `description` | varchar(255) | |
| `status` | varchar(32) | `pending` / `available` / `error` / `deleting` |
| `router_id` | int64 | 与 `name` 组成唯一索引（**不能只对 `router_id` 建唯一索引**，见下面的说明） |
| `vrrp_instance_id` | int64 | 复用 `model.VrrpInstance` |
| `zone_id` | int64 | 调度用 |
| `floating_ips` | 关联 | `FloatingIp.VpnGatewayID` 外键 |
| `ipsec_enabled` | bool | 站点到站点开关 |
| `client_enabled` | bool | 客户端接入开关 |
| `client_protocol` | varchar(16) | `wireguard`（V1 只有这一个） |
| `client_cidr` | varchar(64) | 客户端地址池，如 `10.8.0.0/24`；`client_enabled` 时必填 |
| `client_port` | int32 | 默认 51820 |
| `client_private_key` | text | **加密存储**，网关自己的 WireGuard 私钥 |
| `client_public_key` | varchar(64) | 公钥，写进客户端配置 |
| `client_dns` | varchar(128) | 推给客户端的 DNS；**默认留空**（见 §6.2 的说明，VPC 内的 dnsmasq 大概率答不了从 wg 接口来的查询） |
| `client_routes` | varchar(512) | 推给客户端的网段（`AllowedIPs`）。**留空 = 派生值**，每次生成客户端配置时重算成「VPC 内当前所有内部子网」；填了就按填的来；`0.0.0.0/0`（全流量模式）接口拒绝（§10）。**不要在创建时把派生结果固化进去**（见下面的说明） |
| `master_hyper` | int32 | 心跳上报的当前主节点 hostid，默认 -1 |
| `master_reported_at` | timestamp | 上次上报时间 |
| `disabled` | bool | **停用开关**（2026-09-24 加，§5.6）。存反向值、`not null default false`，零值与存量行都表示启用；接口上叫 `enabled` |

⚠️ **「一个 VPC 一个网关」不能靠 `router_id` 的唯一索引实现**。`model.Model` 是软删除，删掉的网关那行还在表里，单列唯一索引会让这个 VPC **永远建不出第二个网关**。按仓库既有的做法（PET-1228：先 soft-delete 再 `Unscoped` 改名）：唯一索引建在 `(name, router_id)` 上（和 `LoadBalancer` 的 `idx_router_lb` 一致），删除时把 `name` 改成带时间戳的唯一值；「一个 VPC 一个」的约束在 `VpnGatewayAdmin.Create` 里查一次存活记录来保证。`vpn_connections` 的 `(name, vpn_gateway_id)`、`vpn_clients` 的 `(name, vpn_gateway_id)` 同理。

### 3.2 `vpn_connections`（站点连接）

| 列 | 类型 | 说明 |
|---|---|---|
| `owner` / `name` | | `name` 与 `vpn_gateway_id` 唯一 |
| `vpn_gateway_id` | int64 | |
| `status` | varchar(32) | `pending` / `down` / `up` / `error` / `disabled`（网关停用时，§5.6） |
| `remote_gateway` | varchar(64) | 对端公网地址；空表示只作响应方（对端地址不固定），此时 `initiator` 必须为 false |
| `remote_id` | varchar(128) | 对端 IKE 标识。留空时由下发那一刻取 `remote_gateway`，库里不存默认值（§14.6） |
| `local_id` | varchar(128) | 本端 IKE 标识，默认等于浮动 IP |
| `route_mode` | varchar(16) | `static` / `bgp`（§2.6）。决定 TS 怎么生成、要不要起 bgpd |
| `local_cidrs` | varchar(512) | 本端网段，逗号分隔。**留空 = 派生值**，每次下发时重算成「VPC 内当前所有内部子网」。`static` 模式下用于 TS，`bgp` 模式下用于 `network` 语句与出方向白名单 |
| ~~`advertise_client_cidr`~~ | — | **2026-09-24 删除**：VPN 客户端与远端站点之间一律不互通（§14.7），通告客户端地址池没有意义 |
| `remote_cidrs` | varchar(512) | 对端网段，逗号分隔。**仅 `static` 模式**必填 |
| `remote_summary_cidrs` | varchar(512) | 对端汇总网段。**仅 `bgp` 模式**必填（§2.6）。**一个值三处用**：其余节点的静态路由、`nonat` 条目、FRR 的入方向前缀过滤 |
| `max_prefixes` | int | `maximum-prefix` 上限，默认 1000，`warning-only` |
| `local_asn` / `peer_asn` | int | 仅 `bgp` 模式。本端可用私有 ASN（64512–65534），要校验范围 |
| `tunnel_local_ip` / `tunnel_peer_ip` | varchar(64) | 仅 `bgp` 模式。隧道内的一对链路地址（一个 /30），BGP 会话建在上面；XFRM 接口配 `tunnel_local_ip` |
| `bgp_password` | text | 仅 `bgp` 模式，可选的 TCP-MD5，**加密存储** |
| `bgp_keepalive` / `bgp_hold` | int | 默认 10 / 30（不要用默认的 60/180，对端设备重启后要等三分钟才发现） |
| `auth_method` | varchar(16) | `psk`（V1 只有这一个；`cert` 留给 V4） |
| `psk` | text | **加密存储，不回读** |
| `ike_version` | int | 固定 2 |
| `ike_proposal` | varchar(128) | 如 `aes256-sha256-modp2048`，白名单校验 |
| `esp_proposal` | varchar(128) | 如 `aes256-sha256-modp2048` |
| `ike_lifetime` / `esp_lifetime` | int | 秒，默认 86400 / 3600 |
| `dpd_action` | varchar(16) | `restart` / `clear` / `none`，默认 `restart` |
| `dpd_delay` | int | 秒，默认 30 |
| `initiator` | bool | 是否由我方主动发起，默认 true |
| `if_id` | int | XFRM interface id，网关内唯一，用 `vpn_connections.id` 取模分配后查重 |
| `established_at` | timestamp | 上次建立时间（心跳上报） |
| `bytes_in` / `bytes_out` | int64 | 心跳上报 |
| `last_error` | varchar(512) | 最近一次失败原因（心跳上报） |

### 3.3 `vpn_clients`

| 列 | 类型 | 说明 |
|---|---|---|
| `owner` / `name` | | `name` 与 `vpn_gateway_id` 唯一 |
| `vpn_gateway_id` | int64 | |
| `protocol` | varchar(16) | `wireguard` |
| `ip_address` | varchar(64) | 从 `client_cidr` 里分配的 /32；**删除客户端时必须释放**（软删的记录继续占着地址，池早晚会被吃光） |
| `public_key` | varchar(64) | 客户端公钥 |
| `preshared_key` | text | 可选的 WireGuard 对称预共享密钥，**加密存储**（它要下发到节点，必须能读回来） |
| `enabled` | bool | 停用时从节点上的 peer 列表里摘掉，记录保留（**不要加 `gorm:"default:true"`**，见 CLAUDE.md 里 GORM 会把显式 false 换成列默认值那条） |
| `description` | varchar(255) | |
| `last_handshake_at` | timestamp | 心跳上报 |
| `bytes_in` / `bytes_out` | int64 | 心跳上报 |

⚠️ **客户端私钥不进数据库**。WireGuard 的服务端只需要客户端的**公钥**，私钥纯粹是给用户的。平台代为生成时，在创建响应里返回一次就丢掉，库里只留 `public_key`。这样做有三个好处：库被拖走也拿不到任何客户端身份；不需要为它设计解密回读的接口；`GET .../config` 的语义变得诚实（只能给不含私钥的模板，让用户自己填回去）。想要「配置能重新下载」的，就让用户自己生成密钥对、只把公钥交给平台——这本来就是更安全的用法，界面上做成默认推荐项。

⚠️ **`local_cidrs` 和 `client_routes` 必须是派生值，不能在创建时算一次存成字符串**。它们的默认值是「VPC 内所有内部子网」，而**用户随时会给 VPC 加子网**。固化成快照的话，新建子网之后：

| 受影响的地方 | 症状 |
|---|---|
| `static` 模式的 TS | 新子网不在流量选择符里，那个子网的流量进不了隧道，**没有任何报错** |
| `bgp` 模式的 `network` 语句 | 对端学不到新子网，单向不通 |
| 客户端的 `AllowedIPs` | 客户端访问不到新子网，而且要**重新给每个人下发配置** |

所以：库里留空表示「跟着 VPC 走」，每次下发时现算；用户显式填了值才按填的来。同时 §5.5 要在**子网增删**时触发重新下发。

### 3.4 `vpn_remote_prefixes`（派生集合）

这张表回答一个问题：**「这个网关现在应该让整个 VPC 知道的网段有哪些、每条是哪来的」**。它是由别的表派生出来的当前生效集合，不是用户输入。

| 列 | 类型 | 说明 |
|---|---|---|
| `vpn_gateway_id` | int64 | |
| `cidr` | varchar(64) | 规范化之后的网段（`net.ParseCIDR` 解析后重新格式化，不存用户原文） |
| `source` | varchar(24) | `connection_static` / `connection_summary` / `client_pool` |
| `ref_id` | int64 | 来源记录的 ID（连接 ID 或网关 ID） |

**为什么要单独一张表**，而不是每个下发点自己去 `strings.Split(conn.RemoteCidrs, ",")`：

- 需要这个集合的地方有八九处——建 / 改 / 删连接、建 / 删客户端池、节点重启恢复、虚拟机迁到新节点、新节点上第一台虚拟机、主节点变化。每处各拼一遍，**漏并一个来源就是「站点连接好使、但 VPN 客户端在某个节点上不通」这种极难查的 bug**。
- 有了它才能**只下发增量**：事务里比一下改动前后，只发新增和删除的几条，而不是每次都把全量重推给承载该 VPC 的每个节点。
- §10 那串重叠校验有了唯一出处。
- 界面上能说清楚每条路由是怎么来的。

⚠️ **BGP 学到的前缀不进这张表**。它们的生命周期完全不同（心跳写入、随时变、不该被用户改），而且按 §2.6 的设计根本不参与全 VPC 下发——只在主节点的路由表里，由 §6.1 上报一份给界面展示。真要把它们塞进来，用户下次 PATCH 连接时提交的是他界面上看到的那份，学到的路由会**当场被抹掉**，隧道还通着但流量全黑洞，而且只在「用户恰好改了配置」时触发，测试基本测不出来。

### 3.5 已有表上加的列

- `floating_ips`：加 `vpn_gateway_id int64`（和 `instance_id`、`load_balancer_id` 并列）。
- `vrrp_instances`：加 `vrid int`（§2.5），并把 `create_keepalived_conf.sh` 改成用它。

### 3.6 凭据加密

需要加密存库的只有三样：站点连接的 `psk`、网关自己的 `client_private_key`、客户端的 `preshared_key`——它们都**必须能读回来下发给节点**，所以只能加密而不能丢掉。客户端私钥不在此列（§3.3，根本不存）。

平台目前没有 KMS，用对称密钥：clapi 侧用**独立的 `VPN_SECRET_KEY`**（2026-09-23 改，原稿复用 `CAPTURE_UPLOAD_SECRET`）经 HKDF 派生一个 32 字节密钥，AES-256-GCM 加密，密文 base64 存库，**前面带一个版本前缀**（`v1:`），将来换 KMS 时能识别出哪些要重新加密。不复用 `CAPTURE_UPLOAD_SECRET` 的理由：`deploy-control-node.sh` 对它是「为空时自动生成」，谁把 `.env` 里那一行弄丢再跑一次部署，库里所有 PSK 和私钥就静默地全部解不开了，而且要到下一次下发配置才发现。`VPN_SECRET_KEY` 的约定：`deploy-control-node.sh` 首次生成、`deploy-ha-node.sh` 由 MASTER 生成 BACKUP 同步（同 `GRPC_AUTH_TOKEN`）；**为空时 VPN 接口返回 503 而不是自动生成**；clapi 启动时拿库里任意一条密文试解一次，解不开就打 error 日志并让 VPN 接口返回 503，其他功能不受影响。

三条硬约束：

1. **任何 GET / List 接口都不返回这些字段**（响应结构体里干脆不放）。
2. **下发到节点时**用 `ShellEscape` 包参数，配置内容一律 `<<'EOF'` heredoc（PSK 和 base64 密钥里会出现 `/` `+` `=`，WireGuard 密钥还可能出现 `$`）。
3. **节点上的配置文件权限 600**，目录 700。

---

## 4. 节点侧

### 4.1 目录与依赖

分成两个目录，**这不是随意划分的**：

```
/opt/cloudland/cache/router/router-<N>/
├── vrrp-<VrrpInstanceID>/           # keepalived 的地盘，和负载均衡完全一样的布局
│   ├── keepalived.conf
│   ├── keepalived.pid
│   ├── vrrp.pid
│   └── routes                       # set_route_table.sh（notify_master）恢复 fip 默认路由用
└── vpn-<网关ID>/                    # VPN 自己的数据面
    ├── strongswan.conf              # STRONGSWAN_CONF 指向它，改 vici socket / 日志路径（pid 路径改不了，见 §2.2）
    ├── swanctl.conf                 # 权限 600，含 PSK
    ├── run/                         # charon 的私有 piddir：unshare -m 后 bind 到 /var/run（§2.2 绕法 1），charon.pid 与 charon.vici 落在这里
    ├── charon.log
    ├── wg.conf                      # 权限 600，含网关私钥与各 peer 公钥
    ├── frr/                         # 只有主备两个节点有，且只在持 VIP 时跑（§2.6）；属主 frr:frr，守护进程默认降权到 frr 用户
    │   ├── frr.conf                 # 权限 600，可能含 TCP-MD5 口令
    │   ├── vtysh.conf
    │   └── *.pid / *.vty
    ├── tunnel_routes                # 三列：<网段> <类型 static|bgp|client> <主节点上的目标>
    │                                #   192.168.100.0/24  static  ipsec-7
    │                                #   10.0.0.0/8        bgp     blackhole
    │                                #   10.8.0.0/24       client  wg-3
    │                                # 备节点一律 via <对端 VRRP 地址>，与第三列无关（§2.3 的归属表）
    └── status.reported              # 上次上报的状态文本，用于「变化才上报」
```

**keepalived 的配置放在 `vrrp-<id>/` 里是刻意的**：[check_lb_process.sh](../../../scripts/kvm/check_lb_process.sh) 的 glob 就是 `$router_dir/router-*/vrrp-*/keepalived.conf`，放进去就免费拿到了它那套进程守护——包括「`kill -9` 后先摘掉残留浮动 IP 再拉起」和「网卡还没建好时不拉起」这两段已经在真实重启里验证过的逻辑。VrrpInstance ID 在 LB 和 VPN 之间是同一张表的自增主键，目录不会撞。代价是 `check_vpn_process.sh` 只管 charon 和 wg，不管 keepalived——**这一点要在两个脚本的注释里互相指明**，否则后来的人会以为漏了。

新增依赖（`deploy/docker/scripts/deploy-compute-node.sh` 的安装列表和 `deploy/roles/hyper/tasks/main.yml` 一起加）：

- `strongswan-swanctl`、`strongswan-charon`（Ubuntu 24.04 是 5.9.x，26.04 可能是 6.x；配置一律用 swanctl，两个大版本通用，**不要用 `ipsec.conf` / starter**）
- `wireguard-tools`（内核模块 24.04 / 26.04 都自带）
- `frr`（只用 `zebra` + `bgpd`，`/etc/frr/daemons` 里其余全关；`vtysh` 用于排查）
- **实施时的修正（§14.1）**：FRR 10 要跑 `zebra` + `mgmtd` + `staticd` + `bgpd`（汇总黑洞由 staticd 持有，staticd 依赖 mgmtd）；26.04 上 `charon-systemd` 与 `strongswan-charon` 互斥，只能用 §2.2 的绕法 1；五个守护进程都被 AppArmor 限制，要装 `/etc/apparmor.d/local/` 覆盖

装完要把系统自带的服务全部 disable：

```sh
systemctl disable --now strongswan-starter ipsec strongswan frr 2>/dev/null || true
```

理由和 haproxy 那条一样（`deploy-compute-node.sh:238`）：进程由脚本在 netns 内按实例启动，系统自带的服务会占用 `/etc/swanctl`、`/var/run/frr` 和 500/4500 端口。**FRR 尤其要注意**：它默认跑在宿主机的 default netns 里，会去动宿主机的路由表。

### 4.2 新增脚本

| 脚本 | 作用 |
|---|---|
| `create_vpn_gateway.sh` | 建两个目录、公网口（先 `export ROUTES_FILE=$vrrp_dir/routes` 再调 `create_veth.sh`、`create_lb_floating.sh`，否则 fip 默认路由写不进 routes 文件）、写 `strongswan.conf`、**写 `vrrp-<id>/keepalived.conf`**、放行 UDP 500/4500/51820 **与 ESP（`-p esp -d <VIP>`，§10）**、设 send_redirects / MSS 钳制规则（rp_filter 在 `create_local_router.sh` 里统一设） |
| `create_vpn_ipsec_conf.sh` | 从 stdin 读整份连接配置的 JSON，生成 `swanctl.conf`，持锁重载（`swanctl --load-all`）或启动 charon |
| `create_vpn_wg_conf.sh` | 从 stdin 读客户端列表 JSON，生成 `wg.conf`，`wg syncconf` 增量应用（不会断开未变动的 peer） |
| `create_vpn_bgp_conf.sh` | 从 stdin 读 BGP 连接配置的 JSON，生成 `frr/frr.conf`（含 prefix-list / route-map，见 §4.3.1）；只下发到主备两个节点，持 VIP 的那台启动 / 重载 `zebra` + `bgpd`；放行 `INPUT -p tcp --dport 179 -s <tunnel_peer_ip> -d <tunnel_local_ip>`（隧道内 /30 不在 `nonat` 里，对端先发起会话时 SYN 会被 INPUT 默认 DROP 丢掉，只剩我方发起那一半，碰撞时要等 ConnectRetry） |
| `set_vpn_route.sh` | 在本节点 `router-N` 上装/删对端网段的路由与 `nonat` 条目；带 `local` 参数时装隧道路由，否则装指向某个 VRRP 地址的路由；先确认 `ns-<vrrp_vlan>` 存在，不在就照 `add_fwrule.sh:16` 的做法调 `set_subnet_gw.sh` 建出来，不依赖下发顺序 |
| `vpn_notify.sh` | keepalived 的 `notify_master` / `notify_backup` / `notify_fault`：master 先 `set_route_table.sh` 恢复 fip 默认路由（§2.4 第 0 步），再改写本地路由、启停 charon 与 `zebra`/`bgpd`、发起连接；fault 装 blackhole（§2.4）。**全程持 `flock $lb_lock_file`**，与守护脚本互斥（§4.5） |
| `check_vpn_process.sh` | 心跳守护，**只管 charon 与 wg 接口**（keepalived 由 `check_lb_process.sh` 顺带守着，见 §4.1）；复用 `lb_proc_alive` 与 `flock $lb_lock_file`，**拿到锁之后再读一次本节点有没有 VIP**（§4.5）。实施后还管 FRR，在主节点做流量选择符对账，网关停用时只确保进程不在运行（§4.5） |
| `report_vpn_status.sh` | 心跳上报：主节点身份、每条隧道状态与流量、每个客户端的握手时间与流量 |
| `recover_vpn_gateway`（实施为 `report_rc.sh` 里的函数，没有单独的脚本） | 节点重启后按 `boot_id` 触发回调 `recover_vpn_gateway.sh '<hostid>'`，实际重建由 clapi 重发配置完成 |
| `clear_vpn_gateway.sh` | 删网关：停进程、删接口与路由、删 `nonat` 条目、删目录 |
| `restart_vpn_conn.sh` | 手工重建隧道：对连接块里的**全部** CHILD_SA 逐个 terminate 再 initiate（实施时加） |
| `vpn_lib.sh` | 各脚本共用的函数（实施时加）：`vpn_json_read`（**脚本读 JSON 一律用它**，§14.6）、charon / FRR 启停、按角色装路由 `vpn_apply_routes`、流量选择符对账 `vpn_ipsec_reconcile`、客户端与站点隔离 `vpn_isolate_rules`、停用判断 `vpn_disabled` |

**keepalived 配置不能复用 `create_keepalived_conf.sh`**：它把实例名写死成 `vrrp_instance load_balancer_${vrrp_ID}`（第 61 行）、把 notify 写死成 `notify_master $PWD/set_route_table.sh`（第 100 行附近），而且没有 `notify_backup` / `notify_fault`。VPN 的那份由 `create_vpn_gateway.sh` 自己写，除实例名和 notify 外其余照抄（`state BACKUP` + `nopreempt` + 按角色给优先级、`unicast_src_ip` / `unicast_peer`、`virtual_ipaddress` 挂在 `te-<router>-<vlan>` 上）。

notify 这样写，**把参数直接写进配置行**：

```
notify_master "/opt/cloudland/scripts/backend/vpn_notify.sh <网关ID> master"
notify_backup "/opt/cloudland/scripts/backend/vpn_notify.sh <网关ID> backup"
notify_fault  "/opt/cloudland/scripts/backend/vpn_notify.sh <网关ID> fault"
```

keepalived 会在这些参数之后再追加三个自己的参数（类型、实例名、状态），脚本只读前两个即可。这样 `vpn_notify.sh` 不依赖任何环境变量就能找到自己的目录，`start_keepalived`（`cloudrc:215`）**一行都不用改**——`check_lb_process.sh` 重新拉起 VPN 的 keepalived 时也就自然能用。

`report_rc.sh` 末尾加三行，紧挨着现有的 LB 三行（`report_rc.sh:440-442`）：

```sh
recover_vpn_gateway
check_vpn_process
report_vpn_status
```

**stdout 即协议**：这些脚本除了 `|:-COMMAND-:|` 行不能有任何输出，`check_vpn_process.sh` 和一切 `swanctl` / `wg` 调用都要重定向到 `/dev/null`，否则 clapi 会刷一堆 `no command ... found`。

**心跳里的外部命令一律加 `timeout`**（同存储方案里 `virsh` 那条教训）：`swanctl --list-sas` 在 charon 卡住时会一直等，心跳被阻塞会让整个节点被判离线。

### 4.3 `swanctl.conf` 的生成

按连接生成，一个网关一份文件。关键点：

- `local_addrs = <浮动 IP>`，`remote_addrs = <对端公网地址>`（响应方模式写 `%any`）。
- `if_id_in` / `if_id_out` 都设成连接的 `if_id`，对应节点上 `ip link add ipsec-<if_id> type xfrm dev lo if_id <if_id>` 建出来的接口（路由基于接口，不依赖策略），**接口 MTU 设 1360**（§2.3）。建接口的脚本**只建接口、不装路由**，路由归 `vpn_notify.sh`。`bgp` 模式下还要给接口配上 `tunnel_local_ip/30`，BGP 会话建在上面。
- **`local_ts` / `remote_ts` 按 `route_mode` 生成**：
  - `static`：按 `local_cidrs` / `remote_cidrs` 精确给。对端设备通常要求流量选择符精确匹配，给 `0.0.0.0/0` 协商不上。
  - `bgp`：**必须是 `0.0.0.0/0`**（§2.6）。学到的前缀事先不知道，不在 TS 里的流量会被内核策略直接丢掉，现象是「BGP 邻居 up、路由也学到了、就是不通」。
- `fragmentation = yes`（IKEv2 分片）。公网口 MTU 只有 1450，带证书或长提案的 IKE_AUTH 很容易超，不开的话和某些对端会协商到一半没声音。
- `start_action` / `close_action`（**实施与原设计不同**，§14.6）：**我方发起的连接一律 `start_action = start`，仅响应的用 `none`**，不再按路由方式区分；**`close_action` 一律 `none`**。原因：`close_action = start` 让 charon 在对端关闭时按被关闭的 CHILD_SA **自己的旧选择符**重建，而不是按当前配置。两端改配置的时序一交错，旧选择符会互相复活，BGP 的隧道链路地址落在 SA 之外，会话一直 `Active`。对端关闭后的重建改由守护脚本按配置对账（§4.5）。原设计为：`static` 发起用 `trap`、`bgp` 发起用 `start`（trap 要等第一个包才协商，BGP 会话应该常驻）。`dpd_action` 按配置，接口的 `restart` 在 swanctl 里写作 `start`。
- **`static` 模式下每一对 (本端网段, 对端网段) 生成一个 child，不要把多个网段塞进一个 child 的 TS**（2026-09-23 补）。strongSwan 会把多对 TS 放进一个 CHILD_SA 提议，Cisco ASA / FortiGate 这类实现只接受一对、把它缩成第一对，结果只有一个子网对通、其余静默不通。多个 child 共用同一个 `if_id`，路由不受影响。两个 VPC 互测（§11）两端都是 strongSwan，测不出这条。
- **改配置后按连接块 diff**（实施时加）：静态 ↔ BGP 切换或网段变化时，变了的连接块先 `--terminate`，再对块内**全部** CHILD_SA `--initiate`。CHILD_SA 会保留旧选择符，只 `--load-all` 不生效；判断「要不要重新发起」看的是块范围内有没有 `start_action = start`（原先用 `grep -A20` 永远匹配不到，§14.6）。
- `encap = yes` 可选：强制 UDP 4500 封装，让对端无 NAT 时也不走裸 ESP，§2.3 的 MTU 表就是按 NAT-T 算的。不开的话 INPUT 必须放行 `-p esp`（§4.2、§10）；两者至少做一个，建议都做。
- PSK 放在 `secrets.ike-<name>` 段里，`id` 用 `remote_id`。`remote_gateway` 为空（`%any` 响应方）的连接**必须显式给 `remote_id`，且同一网关内唯一**，否则 IKE_AUTH 时选不出该用哪条 PSK（§10 校验）。
- 算法提案来自数据库字段，**必须白名单校验**（§10），不能让用户往配置文件里塞任意文本。

### 4.3.1 `frr.conf` 的生成（`bgp` 模式）

只下发到主备两个节点。骨架：

```
router bgp <local_asn>
  bgp router-id <tunnel_local_ip>
  neighbor <tunnel_peer_ip> remote-as <peer_asn>
  neighbor <tunnel_peer_ip> timers <bgp_keepalive> <bgp_hold>
  neighbor <tunnel_peer_ip> password <bgp_password>          # 可选 TCP-MD5
  address-family ipv4 unicast
    network <local_cidrs 逐条>                                # 通告本端 VPC 子网
    neighbor <tunnel_peer_ip> route-map FROM-PEER-IN  in
    neighbor <tunnel_peer_ip> route-map TO-PEER-OUT   out
    neighbor <tunnel_peer_ip> maximum-prefix <max_prefixes> warning-only
  exit-address-family
!
ip prefix-list REMOTE seq 5 permit <remote_summary_cidrs 逐条> le 32
ip prefix-list LOCAL  seq 5 permit <local_cidrs 逐条>          # 出方向白名单
route-map FROM-PEER-IN permit 10
  match ip address prefix-list REMOTE
route-map TO-PEER-OUT permit 10
  match ip address prefix-list LOCAL
```

⚠️ **入方向过滤不是可选项**（§2.6）。没有它，对端误通告 `0.0.0.0/0` 会把整个 VPC 的出网流量吸进隧道，误通告本 VPC 的网段会让同 VPC 虚拟机互访黑洞——两种情况下 BGP 邻居都显示一切正常。`REMOTE` 这个 prefix-list 的值**就是用户填的汇总网段**，同一个输入。

**出方向也要显式白名单**。FRR 7.4 起按 RFC 8212 要求 eBGP 邻居必须配 in/out policy，否则**一条路由都不收发**。可以用 `no bgp ebgp-requires-policy` 关掉，但**不要那么做**——那等于"靠现在只写了 `network` 语句、恰好不会多通告"来保证安全。配一个只放行 `local_cidrs` 的 `TO-PEER-OUT` 成本是一条 route-map，还能挡住将来有人加了 `redistribute` 之后的意外泄露。

几个容易写错的地方：

- **`bgp router-id` 必须显式给**：netns 里可能没有合适的接口地址供 FRR 自动选，选错了会和对端的 router-id 冲突。
- **`network` 语句要求本端路由已经在内核路由表里**（有一条连接路由或静态路由覆盖它），所以 FRR 要在 §5.1 阶段二、网关配置下发完成之后再启动。
- 🔴 **没有虚拟机的子网不会被通告**（2026-09-23 核对）。`set_subnet_gw.sh` **没有任何 Go 代码会下发它**，只被 `add_fwrule.sh` / `attach_vm_nic.sh` / `post_migration_net.sh` / `set_vrrp_ip.sh` 调用，而这些都要求该子网里**已经有网卡**。所以一个刚建好、还没有虚拟机的内部子网，在任何节点上都不存在 `ns-<vni>`，没有连接路由，`network` 语句不生效，**对端学不到它**。第一台虚拟机建出来之后 FRR 会重新评估并开始通告。

  用户会把这个现象理解成「BGP 坏了」，所以**界面上要在连接详情页标出来**：哪些 `local_cidrs` 当前实际通告了、哪些因为没有云服务器而没通告。`report_vpn_status.sh` 上报的 `show bgp ipv4 unicast` 里有已通告的列表，对比 `local_cidrs` 即可得出。
- 被拒绝的前缀在 `show bgp ipv4 unicast neighbors <peer> filtered-routes` 里，`report_vpn_status.sh` 上报它（§6.1）。

### 4.4 `wg.conf` 的生成

```
[Interface]
PrivateKey = <网关私钥>
ListenPort = <client_port>
# 地址与路由由脚本用 ip 命令配置，不用 wg-quick

[Peer]
PublicKey = <客户端公钥>
PresharedKey = <可选>
AllowedIPs = <客户端 /32>
```

接口由脚本建：

```sh
ip netns exec $router ip link add wg-$gwID type wireguard
ip netns exec $router wg setconf wg-$gwID $vpn_dir/wg.conf      # 首次
ip netns exec $router wg syncconf wg-$gwID $vpn_dir/wg.conf     # 更新，不断开未变动的 peer
ip netns exec $router ip addr add <池内第一个地址>/32 dev wg-$gwID
ip netns exec $router ip link set wg-$gwID mtu 1390 up
# 只写 tunnel_routes，不装路由：装哪条由角色决定，归 vpn_notify.sh（§2.3 的归属表）
echo "<client_cidr> client wg-$gwID" >>$vpn_dir/tunnel_routes
```

⚠️ **这里绝对不能顺手 `ip route replace <client_cidr> dev wg-$gwID`**。备节点上也会跑这个脚本（配置是两端同步下发的），一旦装上这条路由，备节点就把客户端池的流量吸进了一个没有任何握手的本地 wg 接口——包直接被丢掉，而且现象是「切换回来之后好一阵子不通、手工重启下进程又好了」，非常难排查。路由只有 `vpn_notify.sh` 一个归属（§2.3）。

注意用 `syncconf` 而不是 `setconf`：加一个客户端时后者会把所有 peer 的会话状态清掉，所有人断一次。

### 4.5 进程守护与恢复

- `check_vpn_process.sh` **必须是双向的**。先看本节点有没有那个浮动 IP（判断方法同 `report_lb_health.sh`：`ip -4 -o addr`），然后：

  | 本节点**有** VIP | 本节点**无** VIP |
  |---|---|
  | charon / `zebra` / `bgpd` 不在就拉起 | 这三个还在跑就**停掉** |
  | 路由按「主」的规则校正 | 路由按「备」的规则校正（`via <对端 VRRP 地址>`） |

  ⚠️ **「无 VIP 就停」这半边不能省**：keepalived 被 `kill -9` 时 `notify_backup` 根本不执行（§2.4），会留下「进程还在跑 + 路由还指向本机隧道口 + VIP 已被对端接管」的组合——其余节点的流量送过来直接进黑洞，**而且不会自愈**。

  WireGuard 只检查接口在不在、peer 数对不对（它没有守护进程，两端都该在）。**`zebra` 要在 `bgpd` 之前起**，否则 bgpd 连不上 zapi、学到的路由装不进内核，而且它不会退出，现象是「BGP 邻居 up、`show ip bgp` 有路由、`ip route` 里没有」。**keepalived 不归它管**（§4.1，由 `check_lb_process.sh` 的 glob 顺带守着）。和配置脚本用同一个 `flock $lb_lock_file` 互斥（复用现有锁，避免再引入一把），**持锁时启动的进程必须带 `9>&-`**，否则守护进程继承描述符、锁永远不释放（LB 那次实测无锁时 15 个并发起出 15 个 haproxy）。**`vpn_notify.sh` 也要拿这把锁，守护脚本拿到锁之后要再读一次 VIP**（2026-09-23 补）：notify 是 keepalived 起的、不在任何锁内，与守护脚本「读 VIP → 启停进程」之间有窗口——VIP 刚加上、守护脚本在那之前读到「没有」，就会把 notify 正在起的 charon 杀掉，现象是切主后偶发地要再等一次心跳才通。

- 路由校正拿 `tunnel_routes`（§4.1 的三列格式）和当前角色比对 `ip route`，不一致就按 §2.3 归属表的规则纠正。这同时兜住三个窗口：`kill -9` 之后、keepalived 被守护脚本拉起但 notify 没触发过、接口刚建好还没选出主。
- `recover_vpn_gateway`：和 `recover_loadbalancer` 一样按 `boot_id` 触发，回调 clapi；clapi 把该节点上所有 VPN 网关的完整配置重发一遍（网关 → 路由 → 连接 → 客户端）。**顺序必须是先 `set_vrrp_ip.sh` 再 `sendFdbRules`**，理由同 `recover_loadbalancer.go:26` 的注释：反过来的话 VRRP 网卡会被按网关规则的 MAC 先建出来。
- 🔴 **还有一类 LB 没有的状态：第三方节点**（2026-09-23 补）。承载该 VPC 虚拟机、既非主也非备的节点重启后，`router-N` 由 `launch_vm sync` 重建，里面的 VPN 路由和 `nonat` 条目全丢，而 `recover_loadbalancer` 那套只查「本节点上的 VRRP 网卡」，照抄会漏掉它们。做法：**VPN 路由的补发挂在 `launch_vm` 回调上、紧接 `sendFdbRules`，且不区分 `sync` 与首次创建**（§5.5）——重启后每台虚拟机的 sync 回调都会补一次，幂等；`recover_vpn_gateway` 只负责主备节点自己的那份。
- **流量选择符对账**（2026-09-24 加，§14.6）：主节点每次心跳比较每条连接已装的 CHILD_SA 选择符与配置，不一致、或我方发起的连接一个都没装，就 terminate 再按配置发起，每条连接 60 秒最多一次（`<vpn_dir>/reconcile-<conn>`）。这是 `close_action = none` 之后对端关闭时的重建路径（§4.3）。
- **网关停用**（§5.6）：守护脚本对停用的网关只做「charon / FRR 不在运行、隧道路由全是黑洞、`wg-<gw>` 为 down」，不拉起任何进程。
- **角色切换的排查日志**（§14.5）：每次角色变化在 `<vpn_dir>/notify.log` 记分步时间戳，正常 2 秒内从 `master start` 走到 `done`。keepalived 调 notify 时 umask 更严，所以 `vpn_notify.sh` 固定 `umask 022`、FRR 运行目录用 `install -d -m 755 -o frr -g frr` 建；`vpn_notify.sh sync` 由宿主机 netns 里的脚本调用，`set_route_table.sh` 一律经 `ip netns exec <router>` 执行。

---

## 5. 各操作流程

### 5.1 创建网关

**这是一个两阶段流程，不能一口气下发完**。原因在 [rpcs/set_vrrp_ip.go](../../../api/src/rpcs/set_vrrp_ip.go)：`CreateVrrpInstance` 只用 `select=` 下发**一次** `set_vrrp_ip.sh`（cland 挑一个节点做 MASTER），**BACKUP 那次是在 MASTER 的回调里才下发的**，回调里再把 `hyper` / `peer` 写回库。所以创建请求返回时，两个 `Interface` 的 `Hyper` 仍是 -1，这时候拿 `inter=` 下发只会得到 `no target node`（2026-09-16 之后不再静默丢弃）。

- **阶段一（创建请求内）**
  1. `POST /vpn_gateways`，参数：`name`、`vpc`、`zone`（可选）、`public_subnet`（可选，不填按浮动 IP 的默认选法）、`ipsec_enabled`、`client_enabled`、`client_cidr` 等。
  2. clapi 校验（清单见 §10）。
  3. **先申请浮动 IP**（走 `floatingIpAdmin`），再在事务内 `CreateVrrpInstance`（复用 `services/loadbalancer.go:113`）、建 `vpn_gateways` 记录（`pending`）、生成网关自己的 WireGuard 密钥对（`client_enabled` 时），最后把浮动 IP 的 `VpnGatewayID` 指向网关。接口返回 `pending`。
  4. **顺序与原设计相反**（§14.6）：原设计先建 VRRP 实例再申请公网 IP，而 `CreateVrrpInstance` 会立刻下发 `set_vrrp_ip.sh`，指定的公网 IP 已被占用时数据库回滚了，节点上却留下 VRRP 网卡。

- **阶段二（`set_vrrp_ip` 回调，两端都到齐之后）**
  5. **扩展 `rpcs/set_vrrp_ip.go`**：现在的 `UpdateLoadBalancerStatus` 写死只更新 `load_balancers`，要按同一个 `vrrp_instance_id` 一并更新 `vpn_gateways` 的状态（改名成 `updateVrrpOwnerStatus` 之类，两张表各更一次）。
  6. BACKUP 的回调到达（`peer` 落库）时，才下发 `create_vpn_gateway.sh` 到主备两个节点，随后依次下发 keepalived 配置、站点连接、客户端、回程路由。
  7. 节点回调 `vpn_gateway.sh '<id>' '<hostid>' 'ready'` → 状态置 `available`。
  8. 失败处理（实施时加，§14.6）：`create_vpn_gateway.sh` 任何一步失败都经 `trap EXIT` 回调 `error`（判断公网口建没建成要看 `te-<router>-<vlan>` 是否存在，`create_lb_floating.sh` 的退出码没设带宽限制时必然非零，不能当失败依据）。`error` 的网关任一 PATCH 都会重新下发并回到 `pending`，这是救回的正规路径，`VpnRecoverNode` 也覆盖 `error`。

**永远按「主备两个节点」下发，不要按「当前主节点」下发**。`master_hyper` 只用于决定其余节点的 nexthop（§2.3 第 4 点），不作为 `inter=` 的来源——它在网关刚建好时是 -1，直接拿去下发就是 `inter=-1`。像「重建隧道」这种只对主节点有意义的命令也发给两端，备节点上 charon 没跑，脚本自己判断后静默退出即可。

**其余节点的 nexthop 在创建时用 MASTER 角色接口的地址作初值**（2026-09-23 补）：阶段二下发回程路由时 `master_hyper` 还是 -1，第一次 `vpn_master.sh` 上报最多要等 20 秒。两端都以 `state BACKUP` 启动、优先级 110 / 100，首次选主必然是 MASTER 角色那台（除非它恰好没起来），所以直接用它的 VRRP 地址下发，之后由心跳上报纠正。不写明的话实现会去等 `master_hyper`，网关建好后其余节点要空窗 20 秒以上。

**没有浮动 IP 的网关等于没开**：和 LB 一样，keepalived 配置里没有 `virtual_ipaddress` 就没有意义。所以第一版**创建网关时必须带公网 IP**，不做「先建后绑」。

### 5.2 建站点连接

1. `POST /vpn_gateways/:id/connections`。
2. 校验（完整清单见 §10）：`route_mode` 为 `static` 时 `remote_cidrs` 必填、为 `bgp` 时 `remote_summary_cidrs` 与 ASN、隧道内链路地址必填；网段不与 VPC 内子网、VRRP 子网、`169.0.0.0/8`、客户端池、其他连接的对端网段重叠；算法提案在白名单里；`initiator=true` 时 `remote_gateway` 必填。
3. 分配 `if_id`，落库（`pending`），更新 `vpn_remote_prefixes`（§3.4）。
4. 下发到**主备两个节点**，按顺序：
   1. `create_vpn_ipsec_conf.sh` —— 整份 swanctl 配置重生成 + 重载，建 XFRM 接口（`bgp` 模式顺带配 `tunnel_local_ip/30`）
   2. `set_vpn_route.sh <网关ID> --declare` —— **只往 `tunnel_routes` 里写这条网段，不装路由**（装哪条由 `vpn_notify.sh` 按角色决定，§2.3 归属表）
   3. `create_vpn_bgp_conf.sh`（仅 `bgp` 模式）—— 生成 `frr/frr.conf`，持 VIP 的那台起 `zebra` 再起 `bgpd`。**必须排在第 1 步之后**：`network` 语句要求本端路由已在内核表里，XFRM 接口地址也要先配好（§4.3.1）
5. 下发到**其余承载该 VPC 的节点**：`set_vpn_route.sh <网关ID> <网段> via <主节点 VRRP 地址>` + `nonat` 条目。`bgp` 模式下这里的 `<网段>` 是**汇总网段**，不是明细。
6. `nonat` 条目同时也要下发给主备两节点（它不随角色变化，§2.3 归属表的第 3 条硬规则）。
7. 状态由心跳上报（`down` → `up`）。

### 5.3 建客户端

1. `POST /vpn_gateways/:id/clients`，两种方式：
   - **带 `public_key`**（推荐，界面上做成默认项）：用户自己 `wg genkey` 生成密钥对，只把公钥交给平台。私钥从头到尾不经过平台，配置可以随时重新下载。
   - **不带**：平台代为生成密钥对，**私钥只在这一次响应里出现，不入库**（§3.3）。
2. 从 `client_cidr` 里分配一个空闲 /32（在事务里锁住网关行再分配，避免并发拿到同一个地址；`Unscoped` 查一遍，软删记录占用的地址在删除时就该释放，见 §5.4）。
3. 下发 `create_vpn_wg_conf.sh`（整份 peer 列表重生成 + `wg syncconf`）。
4. 响应里返回完整的客户端配置文本。平台生成私钥的那种要在界面上明确提示「这是唯一一次能拿到私钥，关掉就没了」。

客户端配置长这样：

```
[Interface]
PrivateKey = <客户端私钥；平台不保存，用户自带密钥时这里是占位符>
Address = 10.8.0.5/32
# DNS 默认不下发，原因见 §6.2

[Peer]
PublicKey = <网关公钥>
Endpoint = <网关浮动 IP>:51820
AllowedIPs = 192.168.62.0/24, 192.168.63.0/24
PersistentKeepalive = 25
```

`PersistentKeepalive` 由平台写死在模板里，不给用户改：主备切换的恢复速度和「云上能否主动访问客户端」都依赖它（§2.4）。

### 5.4 删除

- 删连接：下发重生成配置（少一条）、删该网段的路由与 `nonat`、删记录（软删前把 `name` 改成唯一值，见 §3.1）。
- 删客户端：下发重生成 peer 列表、**释放 `ip_address`**（置空，不要只软删记录——否则地址池会被慢慢吃光，而且界面上完全看不出来是谁占的）、软删前改名。
- 删网关：先要求删光连接和客户端（或者在接口里级联删，但要在界面上明确列出将被删除的内容），再下发 `clear_vpn_gateway.sh` 到两个节点和其余承载该 VPC 的节点（清路由和 `nonat`），释放浮动 IP，删 `VrrpInstance`；最后按该路由器剩余的 `vrrp_instances` 数决定要不要删 VRRP 子网，**`LoadBalancerAdmin.Delete` 那处按 `load_balancers` 计数的判据也要改成同一个**（§2.5、附录 B 第 8 条），否则删掉最后一个 LB 会把网关的 VRRP 子网一起删掉。

### 5.5 网关跟着 VPC 走

- **虚拟机迁移**：网关不受影响（隧道在路由器 netns 里，和虚拟机在哪台机器无关）。但迁入的目标节点如果之前没有承载过该 VPC，它的 `router-N` 是新建的，**没有 VPN 路由和 `nonat` 条目**。所以 `migrate_vm.go` 的 `completed` 回调里要补发一次该 VPC 的 VPN 路由（和 `prewarmTargetFdb` 一样的位置）。这是很容易漏掉的一点。
- **VPC 里新建虚拟机**落到一台没承载过该 VPC 的节点时，同样要补发。挂在 `launch_vm` 的回调里，紧接 `sendFdbRules`，**`sync`（节点重启后的开机同步）也走这条**，这样第三方节点重启后 `router-N` 重建时的 VPN 路由与 `nonat` 就顺带补上了（§4.5）。补发内容是该 VPC 网关的 `vpn_remote_prefixes` 全量 + `nonat`，幂等（`ip route replace` / `ipset add -exist`）。
- 🔴 **VPC 里新建 / 删除子网**（容易漏，漏了会静默失效）。`local_cidrs` 和 `client_routes` 留空时是派生值（§3.2），子网一变，三样东西要跟着重发：

  | 重发什么 | 不重发的后果 |
  |---|---|
  | `static` 连接的 swanctl 配置 | 新子网不在 TS 里，该子网流量进不了隧道，**无报错** |
  | `bgp` 连接的 FRR 配置（`network` + 出方向白名单） | 对端学不到新子网，单向不通 |
  | 客户端的 `wg.conf` 与**配置模板** | 客户端的 `AllowedIPs` 里没有新子网；注意已经发出去的客户端配置**需要用户重新下载**，界面上要提示 |

  挂在 `SubnetAdmin.Create` / `Delete` 的成功路径上：该 VPC 有 VPN 网关才触发。**删子网时还要检查**：这个子网是不是某条 `static` 连接 `local_cidrs` 里显式填的值，是的话要么拒绝删除、要么把连接置为需要用户确认的状态。

  ⚠️ 另外还有一个只影响 `bgp` 模式的行为：**没有虚拟机的子网不会被通告**（§4.3.1）——连接路由要等第一台虚拟机建出来才存在。所以"建子网 → 重发 FRR 配置"之后对端可能仍然学不到，要等虚拟机。界面上要把「已通告 / 未通告（无云服务器）」分开显示。
- **删除 VPC**：先要求删掉 VPN 网关。

### 5.6 网关停用 / 启用（2026-09-24 加）

`ipsec_enabled` / `client_enabled` 是「提供哪种能力」：关掉要先删光连接或客户端，两个也不能同时关。临时停掉整个网关、保留配置和公网 IP 用单独的开关 `PATCH /vpn_gateways/:id {"enabled": false|true}`。

- **clapi**：`vpn_gateways.disabled` 置位后照常把 `create_vpn_gateway.sh` 下发到主备两个节点（输入多一个 `enabled`）。停用时所有连接置 `disabled`，启用时置 `pending` 等首次上报；两次都清掉建立时间和 BGP 快照（界面的 BGP 邻居状态读的是快照）。停用时对处于 `down` 的连接先发一次告警恢复：暂停后的第一次上报不是 down↔up 转换，原来在告警的连接永远等不到恢复。停用期间照样能改配置、加连接和客户端（新连接直接是 `disabled`），重启连接返回 400（132008），主节点的状态上报一律忽略。
- **节点**：网关目录里写 `disabled` 标记文件，标记变化时调一次 `vpn_notify.sh <gw> sync`。所有拉起进程的路径都检查它：charon 与 FRR 不启动，`vpn_apply_routes` 不分角色一律装黑洞（含客户端地址池，保证不会漏到公网），`wg-<gw>` 置 down，公网地址上的 IKE / ESP / WireGuard 放行规则不加。keepalived、VRRP、公网 IP、`nonat` 与隔离规则都不动，停用期间主备照常切换。
- **实测**：停用约 1 秒在两个节点生效；启用后新主节点 1.3 秒起 charon 与 FRR，跨 VPC 与客户端 20 秒恢复（§14.8）。

---

## 6. 状态上报与客户端 DNS

### 6.1 状态上报

`report_vpn_status.sh`，只由持有浮动 IP 的节点上报，**内容变化或距上次满 5 分钟**才回调（完全照抄 `report_lb_health.sh` 的节流做法）。上报四类：

1. **主节点身份**：`vpn_master.sh '<网关ID>' '<hostid>'`。clapi 发现变了就重发其余节点的回程路由（§2.3 第 4 点）。
2. **隧道状态**：`timeout 10 swanctl --uri unix://<sock> --list-sas` 解析出每条连接的 `ESTABLISHED` / 字节数 / 建立时间，回调 `vpn_conn_status.sh`。
3. **客户端状态**：`wg show wg-<id> dump` 的握手时间和字节数，回调 `vpn_client_status.sh`。
4. **BGP 邻居、学到的前缀、以及被拒绝的前缀**：`timeout 10 vtysh --vty_socket <frr 目录> -c 'show bgp ipv4 unicast json'` 取已接受的，`... neighbors <peer> filtered-routes json` 取被入方向过滤拒掉的，回调 `vpn_bgp_status.sh`。**这份数据只用来在界面上展示**，不参与任何下发（§2.6、§3.4）——写进库的是一张只读快照，clapi 不能拿它去改路由。前缀多的时候要限量（比如各只报前 200 条 + 总数），别把心跳撑爆。

   **被拒绝的前缀是产品上最有价值的一项**：它直接告诉用户「汇总网段填小了」，界面上要在连接详情页显著提示（§2.6 的「填太小」）。

**实施后的上报节奏**（§14.1、§14.6、§14.8）：主节点身份每 20 秒重发一次；**接管后的第一分钟每次心跳四类都发**，因为 clapi 在旧主静默 30 秒之前拒绝新主声明，而状态上报只接受当前主节点的，被拒的上报又会被缓存压住 5 分钟；网关停用时只发主节点身份，其余三类不发，并清掉上报缓存，启用后第一次心跳立即上报。

clapi 侧这四个 handler 必须幂等（回调会重试 3 次）。

🔴 **接受条件必须分成两套，否则主节点身份永远变不了**：

| 上报类型 | 接受条件 |
|---|---|
| **第 1 类（主节点身份）** | 上报者是该网关 `VrrpInstance` 的**主备之一**即可；同一网关按上报时间**后到优先** |
| 第 2–4 类（隧道 / 客户端 / BGP 状态） | 上报者的 `hostid` 必须等于库里当前的 `master_hyper`，挡掉旧主节点的迟到回调 |

**不能对第 1 类也套用「hostid 要等于当前 master_hyper」那条规则**——第 1 类恰恰是用来通知"主节点变了"的。work-02 接管后上报 `hostid=2`，而库里 `master_hyper` 还是 1，一套用就被拒，`master_hyper` 永远停在 1，§2.3 第 4 点那条「整机宕机后给其余节点重发路由」**永远不会触发**。而且失效时毫无征兆：主备切换看着正常、主节点上的虚拟机也通，只有其他节点上的虚拟机一直黑洞。

「后到优先」有一个例外：**旧主 30 秒内还上报过、又有另一个 hostid 声称 master 时不翻转、只记警告并计数**（实施值 30 秒，原设计 60 秒）（§2.4「双主」）。这时是双主，翻来翻去只会让第三方节点的路由每次心跳换一个方向。

### 6.2 客户端 DNS：默认不下发

一个很自然但**多半行不通**的想法是把 VPC 内部子网的网关地址（比如 `192.168.62.1`）作为 DNS 推给客户端——那里确实有 dnsmasq 在跑。问题是 [set_subnet_dhcp.sh:41,74](../../../scripts/kvm/set_subnet_dhcp.sh#L74) 用的是 `bind-dynamic` + `--interface=ns-<vlan>` 启动的（每个 VNI 一个实例，不这么做第一个实例会占掉 `0.0.0.0:53`，后面的全起不来）。`bind-dynamic` 会按**收包接口**过滤，从 `wg-<id>` 进来的查询大概率直接被丢弃。iptables 不是障碍（客户端池在 `nonat` 里，INPUT 放行），障碍是 dnsmasq 自己。

所以：

- **V2 默认 `client_dns` 留空**，客户端用自己的 DNS，只是访问不了 VPC 内部域名。
- 想支持内部域名解析，两条路，都放到 V3 再定：① 给 VPN 单独起一个 dnsmasq，监听 wg 接口上的一个地址，上游指向控制面 DNS，同时加载各子网的 `dhcp-hostsfile`；② 改 `set_subnet_dhcp.sh` 在 `--interface` 后面追加 `wg-*`——但那个脚本是所有子网共用的、改动面大，而且 dnsmasq 配置一变就要重启，会牵动 DHCP，不划算。倾向 ①。
- 这条先实测再定（§13 待验证第 2 条）：也许 `bind-dynamic` 在「目的地址是 `ns-<vlan>` 上的地址、但从别的接口进来」这种情况下是放行的，那就什么都不用做。

---

## 7. 接口

### 7.1 clapi

```
GET    /vpn_gateways                                 列表（offset/limit/query/order）
POST   /vpn_gateways                                 创建
GET    /vpn_gateways/:id                             详情
PATCH  /vpn_gateways/:id                             改名 / 描述 / client_dns / client_routes / 开关 / enabled（停用启用，§5.6）
DELETE /vpn_gateways/:id                             删除

GET    /vpn_gateways/:id/connections                 站点连接列表
POST   /vpn_gateways/:id/connections                 创建
GET    /vpn_gateways/:id/connections/:conn_id        详情
PATCH  /vpn_gateways/:id/connections/:conn_id        改配置（改动才重新下发）
DELETE /vpn_gateways/:id/connections/:conn_id        删除
POST   /vpn_gateways/:id/connections/:conn_id/restart  手工重建隧道（swanctl --terminate + --initiate；网关停用时 400）

GET    /vpn_gateways/:id/clients                     客户端列表
POST   /vpn_gateways/:id/clients                     创建（**响应含私钥，仅此一次**）
GET    /vpn_gateways/:id/clients/:client_id          详情（不含私钥）
PATCH  /vpn_gateways/:id/clients/:client_id          改名 / 启用停用
DELETE /vpn_gateways/:id/clients/:client_id          删除
GET    /vpn_gateways/:id/clients/:client_id/config   下载配置模板（**永远不含私钥**：用户自带密钥的自己填回去，平台代生成的只有创建那一次能拿到）
```

改完接口注释要 `cd api && make docs` 重新生成 swagger 并提交。

### 7.2 cpgateway

上面每一条都要加进 `cpgateway/src/apis/proxy_routes.go` 的白名单（不在白名单里的路径网关直接 404，界面和外部 API 都用不了——自动调整规则那套接口就是这么废掉的）。都不是 SystemAdmin 路由。

配额：新增 `vpn_gateways` 计数配额（默认 1）。按 CLAUDE.md 的清单，要同时改 `quotaRules`、`QueryResourceAmount` / `PrepareQuota` 的资源分支、`QuotaResourceFields` 与 `integerQuotaFields`、两张表的模型、`schemas.go`、`DeleteRegion` 的用量检查、`consumption_sync.go`、`DEFAULT_*` 系统设置（含 `settingRanges`）和 `conf/config.toml`，前端的 `api/quota.ts` 的 `QUOTA_ROWS`、`useQuota.ts`、概览的 `buildUsageBars`、系统设置的 `numberRanges` 和三种语言文案。网关占的公网 IP 走 `public_ips`，不重复计。

**新增配额列不写迁移代码**：AutoMigrate 之后已有行是 0，部署时手工 `UPDATE org_resource_quotas SET max_vpn_gateways=1`，再登录一次触发对账。

### 7.3 审计

`api/src/apis/audit_actions.go` 的 `auditRoutes` 要补 10 条映射（`POST`/`PATCH`/`DELETE` 各级资源，加上 `restart`），否则只进审计不进组织动态（`TestAuditRoutesMatchRegisteredRoutes` 只校验表里的键是真实路由，不会提醒你漏了）：`vpn_gateway.create` / `.delete` / `.update` / `.connection_create` / `.connection_delete` / `.connection_update` / `.connection_restart` / `.client_create` / `.client_delete` / `.client_update`，资源类型 `vpn_gateway` → `{"vpn_gateways", "name"}`。

实施后又加了 `vpn_gateway.enable` / `vpn_gateway.disable`：只含 `enabled` 的 PATCH 由接口调用 `SetAuditAction` 细化（§5.6）。

三种语言的动作文案都要加（`dashboard.overview.activityActions.vpn_gateway.*` 和失败用的 `activityActionsFailed`）。

### 7.4 前端

新增：

- `web/src/api/vpn.ts`
- `views/dashboard/VpnGateways.vue`（列表）、`VpnGatewayDetail.vue`（详情，标签页：概览 / 站点连接 / 客户端）
- `components/vpn/VpnConnectionModal.vue`、`VpnClientModal.vue`（客户端创建后的密钥展示 + 复制 + 下载 + 二维码）
- 路由 `/dashboard/vpn-gateways`、`/dashboard/vpn-gateways/:id`，侧边栏入口

一律复用既有基础组件，不要另起一套：`BaseModal` / `DeleteModal` / `PageToolbar` / `StatusBadge` / `DataTable` / `PaginationBar` / `InfoRow` / `DetailTabs` / `useListQuery` / `useGoBack` / `useCopyId`。详情页按 `.title-bar` 那套全局结构写（**不要在页面里重新定义 `.title-bar`、`.info-card`、`.icon-btn-table`、`.badge-secondary` 这些类**）。行内操作按钮用 `.row-actions` + `.icon-btn-table`。

图标（lucide）：VPN 网关用 `ShieldCheck`，站点连接用 `Network`，客户端用 `Laptop`，重建隧道用 `RefreshCw`。侧边栏、列表行、详情标题、空状态四处用同一个。

文案三个语言包都要加（`en.ts` / `zh.ts` / `zh-TW.ts`），`npm run i18n:check` 必须过。**注意 TS 常量里的英文文案 `i18n:check` 抓不到**（节点告警那次就是这么漏的），协议名、状态名这类下拉选项只存值，文案走 `t()`。

二维码：WireGuard 客户端配置扫码需要一个 QR 库。不要为此引入新依赖到生产包，用 `qrcode` 的 ESM 版本按需 `import()`，或者干脆只提供「复制配置」和「下载 .conf」，二维码放 V3。倾向后者。

**实施后的界面约定**（2026-09-24，§14.8、§14.9）：

- 列表页**不做 VPC 筛选**：每个 VPC 最多一个网关，筛选结果只有 0 或 1 条；表格里的 VPC 列可以直接点进 VPC。
- 创建弹窗：已有网关的 VPC 在下拉里标「已有 VPN 网关」并禁选，默认选第一个没有网关的。**可用区和公网 IP 都是下拉**：可用区来自 `/zones`，默认「自动」；公网 IP 在选定公网子网后列出空闲地址，默认「自动分配」，**并排除子网网关地址**——`GET /addresses/:subnet` 会把网关那一行当成空闲返回，而 clapi 指定地址分配时不排除网关（CLAUDE.md 待办 B11）。
- 创建与编辑弹窗提交前检查：至少开一种能力；编辑时关站点到站点要先删连接，关客户端 VPN 要先删客户端。提示是中文，不透出后端的英文报错。
- **弹窗报错放在底栏按钮左侧**，不放在表单末尾：长表单里放在末尾要往下滚才看得到，曾让用户以为点「创建」没有反应（后端其实返回了 409）。
- 停用 / 启用在详情页操作菜单里，停用先弹确认；停用后标题与列表显示「已停用」，顶部横幅带「启用」，连接显示「已停用」，重启连接按钮禁用。
- 站点连接弹窗原有的「向对端通告客户端地址池」已随 §14.7 删除。二维码没做，客户端配置只提供复制和下载 `.conf`。

---

## 8. 高可用与故障处理小结

| 场景 | 现象 | 恢复 |
|---|---|---|
| keepalived 正常退出 / 手工切主 | 浮动 IP 漂到对端 | `notify_backup` 即时改本地路由、停 charon 与 bgpd；其余节点 nexthop 仍指向原主，多一跳，**路由零收敛**（端到端仍要等隧道重建） |
| **keepalived 被 `kill -9`** | 浮动 IP 漂到对端，但 **`notify_backup` 不执行** | 原主节点上进程还在跑、路由还指向本机隧道口 → 黑洞且不自愈；靠 `check_vpn_process.sh` 的「无 VIP 就停」那半边收拾（§2.4、§4.5）。**这是最常见的进程死法，必须覆盖** |
| **双主**（附录 B 第 2 条触发） | 两台 charon 用同一 VIP 向对端发起、互清 SA，对端 SA 反复重建；客户端会话在两台间跳 | 修 B2 是前置；旧主 30 秒内还在上报时，clapi 不接受另一个 hostid 的主节点声明，只告警（§2.4、§6.1） |
| **两端同时 FAULT** | 两端都收不到对端通告 | `notify_fault` 装 blackhole 而不是互指，否则包在 VXLAN 上来回到 TTL 耗尽（§2.4） |
| charon / bgpd 异常退出 | 隧道断 / 路由撤销 | 心跳守护拉起（1–20 秒），随后重协商 |
| 主节点整机宕机 | 浮动 IP 漂到对端 | 隧道与 eBGP 在新主上重建；其余节点的 nexthop 在新主第一次心跳上报后由 clapi 重发（设计 1–20 秒；实测 clapi 第 24 秒切换、跨 VPC 探测中断到第 8–23 秒，§14.5）。**汇总网段不变**，只换 nexthop |
| 对端撤销一条 BGP 前缀 | 该网段不可达 | 主节点上明细路由消失，流量落到汇总的 `blackhole` 被丢掉；**其余节点什么都不用改**（§2.6） |
| 对端通告范围外的前缀 | 那个网段学不到 | 入方向 prefix-list 直接拒掉（§4.3.1），被拒前缀经心跳上报、界面提示「汇总网段填小了」。**这是防护生效的正常表现，不是故障** |
| VPC 里新建了子网 | 对端 / 客户端访问不到它 | clapi 在子网创建成功后重发 swanctl / FRR / wg 配置（§5.5）；`bgp` 模式下还要等该子网有第一台虚拟机才会真正通告（§4.3.1） |
| 节点重启（主或备） | netns、路由、`nonat`、XFRM、wg、frr 全丢 | `recover_vpn_gateway` 按 `boot_id` 触发，clapi 重发全部配置 |
| **第三方节点重启**（承载虚拟机、非主非备） | `router-N` 重建后没有 VPN 路由与 `nonat`，该节点上的虚拟机访问对端黑洞 | `launch_vm sync` 回调补发（§4.5、§5.5）；`recover_vpn_gateway` 不管这类节点 |
| 对端设备重启 | 隧道断 | DPD（`dpd_action = restart`，swanctl 里写作 `start`）+ 我方发起的连接 `start_action = start`；`close_action` 一律 `none`，对端关闭后的重建由守护脚本按配置对账（§4.3、§4.5） |
| 虚拟机迁到新节点 | 新节点的 `router-N` 缺 VPN 路由 | `migrate_vm` 的 `completed` 回调补发（§5.5） |
| 网关被停用 | 隧道、BGP、客户端全部中止，公网地址不回应 | 设计行为：两个节点的对端网段与客户端池都是黑洞，不会漏到公网；启用后约 20 秒恢复（§5.6） |

---

## 9. 部署

- `deploy-compute-node.sh` 和 `deploy/roles/hyper/tasks/main.yml` 的包列表加 `strongswan-swanctl`、`strongswan-charon`、`wireguard-tools`、`frr`，并 disable 系统自带的 strongswan / ipsec / frr 服务（§4.1）；§2.2 选了绕法 2 的话再加 `charon-systemd`。
- 控制面：`deploy-control-node.sh` 生成 `VPN_SECRET_KEY`，`deploy-ha-node.sh` 由 MASTER 生成、BACKUP 同步（§3.6）。
- 计算节点的 `cloudland-iptables.service` 基础规则不需要改：VPN 的公网地址在路由器 netns 的 `te-` 设备上，流量经 `br<vlan>` 桥进 netns，不走宿主机的 INPUT 链（和 LB 的浮动 IP 一样，已在 work-01 验证过这条路径）。
- **提交新脚本时记得 `git update-index --chmod=+x`**（本地 `core.filemode=false`），部署到节点时也要确认可执行位。

---

## 10. 安全

- **INPUT 放行清单（`router-N` 默认 DROP）**：UDP 500 / 4500 / 51820 目的为 VIP；**ESP（IP 协议 50）目的为 VIP**（2026-09-23 补）——对端没有 NAT 时 IKEv2 不做 UDP 封装、ESP 裸跑，靠 `RELATED,ESTABLISHED` 只在我方先发包时经 generic conntrack 偶然放行（空闲 600 秒即失效），现象是「对端先发 / 空闲一阵后不通」；**TCP 179 源为 `tunnel_peer_ip`**（隧道内 /30 不在 `nonat`，§4.2）。另外注意 `nonat` 的 INPUT ACCEPT 也放行了对端网段访问 `router-N` 本地的服务（dnsmasq、haproxy 监听的 VIP、`169.x` 点对点地址），对端站点被攻陷时这是一个小的暴露面，V3 考虑只放行 ICMP。
- **凭据**：§3.6。另外 `ErrorResponse` 遇到数据库原始报错只返回业务消息（已有约定），不要让加密列的内容因为约束冲突泄到响应里。
- **注入**：所有下发到节点的参数走 `ShellEscape`，heredoc 一律 `<<'EOF'`。`common/shell_test.go` 会用 AST 检查含 `.sh` / `<<` 的 `Sprintf` 模板，漏包即失败——写的时候就会被挡住。
- **配置行注入**：算法提案、IKE 标识、客户端公钥这些会原样写进配置文件的字段，必须做格式校验（`apis/input_validation.go`）：
  - 提案：`^[a-z0-9]+(-[a-z0-9]+){1,4}(,[a-z0-9]+(-[a-z0-9]+){1,4})*$`，再对每一段查白名单
  - IKE 标识：`^[A-Za-z0-9._@:-]{1,128}$`
  - WireGuard 公钥 / 预共享密钥：`^[A-Za-z0-9+/]{43}=$`
  - CIDR：用 `net.ParseCIDR` 解析后**用解析结果重新格式化**再存，不要存用户原文
  - 端口：1–65535
  这条教训来自 LB 后端地址——原先原样写进 haproxy 配置，可以注入配置行。
- **网段冲突**。`remote_cidrs` 与 `client_cidr` 都要逐条比对，任何一条命中就 400：
  - VPC 内已有的子网网段（含将来新建的——所以**建子网时反过来也要查一遍 VPN 网段**，否则会出现「VPN 通了之后建的子网悄悄不通」）
  - VRRP 子网 `192.168.196.0/24`
  - **`169.0.0.0/8`**：`create_local_router.sh:80-85` 给每个 `router-N` 到 `router-0` 的点对点链路分配的是 `169.<x>.<y>.<z>/31`，撞上就把路由器的默认路由打掉了，表现是整个 VPC 出不了网。**这条只针对 `remote_cidrs` / `remote_summary_cidrs` / `client_cidr`，不套在 `tunnel_local_ip` / `tunnel_peer_ip` 上**（2026-09-23 补）：AWS / Azure 的 BGP-over-IPsec 强制隧道内地址在 `169.254.0.0/16`，一刀切禁掉就和公有云做不了 BGP。点对点地址实际是 `169.<part2>.<part3>.x/31`，`part2 = (路由器 ID % 64516) / 254`，小 ID 根本到不了 254；对链路地址只拒绝与**本路由器实际分配的那个 /31** 重叠（clapi 没记这个值，只能在下发脚本里校验后回报错误）
  - 同一网关上其他站点连接的 `remote_cidrs`、客户端池
  - `0.0.0.0/0` 不允许出现在 `remote_cidrs`；`client_routes` 的全流量模式 V2 也拒绝（见本节最后一条）
- **`local_cidrs` 为空要拒绝**：VPC 还没有内部子网时默认值算出来是空集，配置文件里的 `local_ts` 会是空的，协商必然失败而且报错莫名其妙。
- **`remote_gateway` 为空时 `remote_id` 必填且同一网关内唯一**（§4.3）：`%any` 响应方靠它选 PSK。
- 🔴 **对端能往我们这边注入多大范围的路由，是这套设计里唯一一个「对端可控」的攻击面**（`bgp` 模式特有）。BGP 模式下 IPsec 的 TS 是 `0.0.0.0/0`，内核策略不再起过滤作用，**唯一的边界就是 FRR 的入方向 prefix-list**（§4.3.1）。没有它，一个被攻陷或误配置的对端设备可以：通告 `0.0.0.0/0` 把整个 VPC 的出网流量吸进隧道（中间人）、通告本 VPC 的网段让同 VPC 虚拟机互访黑洞（拒绝服务）、灌一万条明细打爆主节点路由表。三条都不需要任何凭据，只要 BGP 邻居建起来就行。所以：**`bgp` 模式下 `remote_summary_cidrs` 必填且必须生成 prefix-list，`maximum-prefix` 必须配**，不允许「留空表示不限制」这种选项。
- **客户端与远端站点之间禁止互通**（2026-09-24 产品决定，§14.7）：BGP 模式的选择符是 `0.0.0.0/0`，原设计下只要推送路由加上远端网段、再打开「通告客户端地址池」，客户端就能经网关访问机房，反之亦然。现在网关两个节点的路由器 netns 在 `FORWARD` 最前面有 `-i wg+ -o ipsec+ -j DROP` 与反方向一条，`advertise_client_cidr` 已整体删除。推送路由不校验是否与远端网段重叠，填了也只会在网关被丢弃。
- **SQL**：列表接口的过滤一律参数绑定，名称搜索用 `dbs.Contains`，排序用 `dbs.NewOrders`。`dbs/sql_literal_test.go` 会扫描字符串字面量。
- **权限**：创建 / 修改 / 删除要求 `model.OrgWriter`；只读成员只能看。**不要漏掉 PATCH**（`PATCH /subnets/:id` 就漏过写权限检查）。
- **审计里不要出现凭据**：创建连接 / 客户端的请求体不进审计（现有实现只在失败时截取响应体），但要确认失败响应里不会把 PSK 回显出来。
- **客户端全流量模式（`client_routes = 0.0.0.0/0`）V2 不做，接口先拒绝**（2026-09-23 改）。原稿的分析反了：`create_local_router.sh:92` 的 SNAT 条件是「源在 `nonat` 且目的不在 `nonat`」，客户端池加进 `nonat` 之后访问公网**会**被 SNAT——但 SNAT 成的是 `169.x` 再经 `ti-N` 到 router-0，而同一脚本 54–61 行对 `ti-N` 进出各限 **120 pps**（`system_packet_rate_limit` 默认值，全平台经 system router 出网都是这个上限），全流量客户端等于不可用，出口地址还是 system router 的公网 IP 而不是网关的 VIP。要做就得走 `te-`：`ip rule from <client_cidr> lookup fip-<vlan>` + `POSTROUTING -s <client_cidr> -o te-* -j SNAT --to <VIP>`；可 fip 表里没有 VPN 的站点路由，`from <client_cidr>` 会把客户端去对端站点的流量一起劫到公网，得改用 fwmark 或把站点路由同步进 fip 表。这已经是一个独立的小设计，放 §13 待验证第 4 条，V3 视需求再定。

---

## 11. 测试

**环境**：work-01 / work-02 / work-03 三节点。**必须三节点测**——两个节点的时候主备就是全部节点，回程路由那条「第三方节点」路径根本不会被走到，错了也发现不了。

**站点到站点**用两个 VPC 互联来测（不需要真实的对端设备）：

1. 建 `vpn-a`（VPC A）和 `vpn-b`（VPC B）两个网关，各分一个公网 IP。
2. 互相建一条连接，PSK 相同，对端网段填对方的内部子网。
3. VPC A 在三个节点上各放一台虚拟机（`a1`@work-01、`a2`@work-02、`a3`@work-03），VPC B 同理。
4. 验证：`a1`/`a2`/`a3` 与 `b1`/`b2`/`b3` **两两互 ping 通**（9 × 2 个方向）。这一步会暴露回程路由的所有问题。
5. 在虚拟机里抓包确认**看到的是对端的真实内网地址**，不是 `169.x` 也不是网关地址。
6. `iperf3` 打一遍，确认 MSS 钳制生效（没有分片、没有卡死在大包）。

**BGP 模式**（两端都是我们，各自跑 FRR，正好互相当对端）：

7. 把两条连接改成 `route_mode=bgp`，各配一个私有 ASN 和一对隧道内链路地址，汇总网段填对方的 `192.168.0.0/16`。
8. 验证邻居 up、`show ip bgp` 学到对方的内部子网、`ip route` 里有明细、三节点上的虚拟机仍然两两互通。
9. **在 VPC B 里新建一个内部子网**，不改 VPC A 的任何配置——A 侧应该自动学到并连通。这是 BGP 的核心价值，也是这一版唯一能证明它真的在工作的用例。
10. **删掉那个子网**，确认 A 侧撤销路由；此时从 A 的虚拟机访问该网段应该**在主节点被 blackhole 丢掉**，而不是经 `router-0` 漏到公网（§2.6）——抓 `te-` 口确认没有这个目的地址的包出去。
11. 主备切换后重复 8：新主节点重新起 eBGP、重新学到前缀，其余节点的汇总静态路由不需要变。
12. 故意把 TS 配成精确网段（模拟 `route_mode` 判断写错），确认现象是「邻居 up、路由学到、流量不通」——留个印象，这是将来最容易误判成「BGP 没生效」的故障。
13. **入方向前缀过滤**（§4.3.1，安全相关，必测）。⚠️ **这几条危险路由没法从我们的接口发出去**（B 侧也是本平台，FRR 只有按 `local_cidrs` 生成的 `network` 语句，而且出方向还有白名单），所以要**手工进 B 侧的 `vtysh` 临时加** `network 0.0.0.0/0` 之类，测完删掉。在 B 侧故意通告三种危险路由，确认 A 侧全部拒收、且被拒前缀出现在界面上——
    - `0.0.0.0/0` → 拒；A 侧虚拟机出公网不受影响（否则就是整个 VPC 的流量被劫进隧道）
    - A 自己的 VPC 网段 `192.168.62.0/24` → 拒；A 侧同 VPC 虚拟机互访不受影响
    - 超过 `max_prefixes` 条明细 → `maximum-prefix` 告警，邻居不断（`warning-only`）
14. **汇总网段填小**：把 A 侧的汇总从 `192.168.0.0/16` 改成只覆盖一半，确认 ① 被拒前缀上报并在界面提示 ② 主节点上的虚拟机能访问、其他节点的不能（这正是「填太小」的真实症状，值得亲眼看一次）。
15. **`kill -9` keepalived**（§2.4、§4.5，必测）：对主节点的 keepalived 发 `kill -9`，确认 `notify_backup` **没有**执行（这是预期），然后 `check_vpn_process.sh` 在下一次心跳里停掉 charon / bgpd 并把路由改回 `via <对端>`。全程 ping 记录中断时长。**不测这条就等于没覆盖最常见的进程死法。**
16. **主节点身份的上报与接受**（§6.1，必测）：制造一次主备切换，确认库里的 `master_hyper` **真的变了**，且 clapi 给第三个节点重发了 nexthop。如果 `master_hyper` 纹丝不动，就是那条接受条件又写成一套了——而这个 bug 在两节点环境下**测不出来**（第三个节点才是受害者）。
17. **VPC 新建子网**（§5.5）：在 A 侧 VPC 里加一个内部子网，确认 ① swanctl / FRR / wg 配置被重发 ② `bgp` 模式下要等该子网第一台虚拟机建出来才真正通告给对端 ③ 界面把「已通告 / 未通告（无云服务器）」分开显示。
18. **切主后 fip 默认路由恢复**（§2.4 第 0 步，必测）：切主后在新主节点 `ip netns exec router-N ip route show table fip-<vlan>` 必须有默认路由，`tcpdump -i te-<N>-<vlan>` 能看到源为 VIP 的 IKE / WireGuard 包出去。漏了第 0 步的现象是隧道永远建不起来、`ti-N` 上反而出现源为 VIP 的包。
19. **裸 ESP**（§10）：两端都无 NAT 且不开 `encap` 时，`ip -s xfrm state` 里的封装是 ESP 而非 UDP；对端先发流量、以及空闲 15 分钟后再发，都要通（验证 `-p esp` 放行，不能靠 conntrack 凑巧）。
20. **删掉同 VPC 最后一个 LB 后网关不受影响**（§5.4）：`router.vrrp_subnet_id` 不变、VRRP 地址还在、隧道仍通；再建一个 LB 复用同一个 VRRP 子网。
21. **第三方节点重启**（§4.5）：重启既非主也非备、但有该 VPC 虚拟机的节点，确认 `router-N` 重建后 `ip route` 里有 VPN 路由、`ipset list nonat` 里有对端网段，该节点上的虚拟机访问对端恢复。
22. **两端同时 FAULT**（§2.4）：主备两端把 `ns-<vrrp_vlan>` 同时 down，确认两端都装了 blackhole 而不是互指（从第三方节点 `traceroute` 不出现来回）；恢复后重新选主、路由归位。

**客户端到站点**：本机（Windows）装 WireGuard 客户端，连 `vpn-a` 的公网 IP，验证能访问三个节点上的虚拟机，虚拟机上 `tcpdump` 看到的是客户端池地址；再从虚拟机主动 ping 客户端。

**主备切换**：

- 主节点上 `kill -9` keepalived → 记录 ping 丢包数和隧道重建时间
- 主节点上 `kill -9` charon → 记录心跳拉起的时间（最坏 20 秒）
- **真实重启主节点** → 记录全流程恢复时间（LB 那次是 36 秒注册 / 42 秒心跳 / 55 秒恢复）
- 切换期间第三方节点上的虚拟机是否始终可达（这是两跳方案的关键验证点）

**实际执行的用例**在 `test-items/TC-17-VPN网关.md`（VPN-00 到 VPN-11，历史缺陷回归点 1–24），脚本在 work-01 `/root/vpn-e2e-*.sh`。在上面的计划之外补了：代码复查修复的验证（`9-fixes`）、客户端与站点隔离（`10-isolate`）、网关停用 / 启用（`11-disable`）、三次真实重启主节点（`7-monitor`，§14.5）。计划里没做的见 §14.4。

**回归**：跑完要确认 `rb-lb` 仍然 10/10、三节点心跳正常、虚拟机热迁移正常（VPN 路由补发那条改动碰了 `migrate_vm.go`）。

**测试后清理**：两个测试 VPC、6 台虚拟机、2 个网关、2 个公网 IP 全部删掉，确认节点上 `ip netns exec router-N ip route`、`ipset list nonat`、`ip link` 里没有残留，`/opt/cloudland/cache/router/router-*/vpn-*` 目录清零。

---

## 12. 实施阶段

**待决策已全部不阻塞 V1**（BGP 的路由方案 2026-09-23 定了，见 §13「已定」）。

**但 V1 之前先做 §13 待验证的第 1–3 条**：都不需要写产品代码，在 work-01 现有的 `router-8` 里就能验。第 1 条（strongSwan 多实例的启动方式）决定 §2.2 怎么实现，是唯一还能改变设计的；第 2 条（DNS）决定 V2 要不要多做一个 dnsmasq；第 3 条（rp_filter）只是确认实测值，`create_local_router.sh` 无论如何都显式设 2。

### V1：共用骨架 + 站点到站点 ✅（2026-09-23 已实施，见 §14）

- 数据模型：`vpn_gateways`、`vpn_connections`（含 BGP 字段）、`vpn_remote_prefixes`，`floating_ips.vpn_gateway_id`，`vrrp_instances.vrid`；唯一索引一律带 `name`、删除时改名（§3.1）
- 顺手修掉 VRID 8 位的既有缺陷（附录 B 第 1 条）
- **扩展 `rpcs/set_vrrp_ip.go`**：`UpdateLoadBalancerStatus` 现在写死只更新 `load_balancers`，要按 `vrrp_instance_id` 一并更新 `vpn_gateways`；网关的配置下发挂在 BACKUP 回调之后（§5.1）
- 节点脚本：`create_vpn_gateway.sh`、`create_vpn_ipsec_conf.sh`、`create_vpn_bgp_conf.sh`（含 prefix-list / route-map / maximum-prefix，§4.3.1）、`set_vpn_route.sh`、`vpn_notify.sh`、`check_vpn_process.sh`、`report_vpn_status.sh`、`recover_vpn_gateway.sh`、`clear_vpn_gateway.sh`
- **先修掉 `clear_vrrp_ip.sh` 按 MAC 删 fdb 那个既有 bug**（附录 B 第 2 条）：clapi 在下发完 `clear_vrrp_ip.sh` 之后无条件重发一次 `sendFdbRules`。不修的话，删 VPN 网关会打断同 VPC 的负载均衡（反之亦然），而且会把网关打成双主（§2.4）
- **改 `LoadBalancerAdmin.Delete` 的 VRRP 子网回收判据**为按 `vrrp_instances` 计数（附录 B 第 8 条），否则删最后一个 LB 会把网关的 VRRP 子网删掉
- 凭据加密改用独立的 `VPN_SECRET_KEY`（§3.6），两个部署脚本生成 / 同步它
- `create_local_router.sh` 加两个 sysctl：`rp_filter=2`（**无条件**，§2.3）、`send_redirects=0`
- clapi：网关与连接的 CRUD、派生集合的维护与增量下发、四个心跳回调 handler、迁移与新建虚拟机时的路由补发（含 `sync`，§5.5；创建时其余节点的 nexthop 用 MASTER 角色接口地址作初值，§5.1；第 1 类上报的双主不翻转，§6.1）
- cpgateway：代理白名单
- 前端：列表 + 详情 + 站点连接标签页（`static` / `bgp` 两种模式的表单差别不小，BGP 那套要有 ASN、隧道内链路地址、汇总网段，以及邻居状态和学到的前缀的展示）
- 部署脚本加包（含 `frr`）
- 测试：§11 的站点到站点全套（静态 + BGP）+ 主备切换

**BGP 不能挪到后面做**。它决定了 §2.3 的「对端网段」来源、§4.3 的流量选择符怎么给、`vpn_connections` 的字段——先按纯静态做一版再回头加，等于把这三处推翻重来。

### V2：客户端到站点 ✅（2026-09-23 已实施，见 §14）

- 数据模型：`vpn_clients`，网关上的客户端字段
- 节点脚本：`create_vpn_wg_conf.sh`，`report_vpn_status.sh` 加客户端部分
- clapi：客户端 CRUD、地址池分配与**删除时释放**、配置文本生成（私钥不入库，§3.3）
- 前端：客户端标签页；创建弹窗默认走「用户自带公钥」，选「平台代生成」时明确提示私钥只显示这一次
- 全流量模式不做，`client_routes = 0.0.0.0/0` 接口拒绝（§10）
- 测试：§11 的客户端部分

### V3：收尾 ✅（2026-09-23 已实施，见 §14；资源详情页的「操作记录」标签页只做了 VPN 网关自己的）

- 配额（`vpn_gateways`）
- 审计动作映射 + 三语文案
- 隧道 down 的告警（怎么接进现有告警体系要先想清楚：节点告警和 VM 告警都是 Prometheus 规则，而隧道状态在数据库里。最省事的做法是 clapi 在状态变 `down` 时直接走通知渠道，不进 Prometheus）
- 二维码
- 资源详情页的「操作记录」标签页（和 B1 一起做）

### V4（可选，视 §13 待决策第 1 条的结论）— 未做

- IKEv2 + EAP-MSCHAPv2 的客户端接入：要引入每网关一套自签 CA、服务端证书签发与续期、CA 证书下发、EAP 用户库
- 站点连接的证书认证
- OpenVPN（TCP 443 穿透）
- **每节点 iBGP**（去掉汇总网段的要求，让明细前缀传播到每个节点）：前置是给每个承载该 VPC 的节点在 VRRP 子网里分配唯一地址并随节点加入 / 退出动态增删，再加每 VPN VPC × 每节点一个 `zebra` + `bgpd`、短定时器或 BFD。按 §2.6 的设计做，这一步是叠加而不是推翻——`vpn_remote_prefixes` 里 `connection_summary` 那类来源直接停用即可

---

## 13. 待决策与待验证

**已定**

- ✅ **BGP 用「外部 eBGP + 内部汇总网段」，不做每节点 iBGP**（2026-09-23 确认）。十个候选方案的对比与否决理由见 §2.6。**V1 没有阻塞项了，可以开工。** 如果将来产品上认为「让用户填汇总」不可接受，走 §2.6 的方案 2（clapi 中继明细）作为兜底，那是 V3 的一个增量来源，**不推翻 V1 的任何设计**。

**待决策**

> 实施按下面各条的默认值做了：1 用 WireGuard，2 每个 VPC 一个网关，3 用独立的 `VPN_SECRET_KEY`，4 不做 IKEv1。将来要改再重新决策。

1. **客户端到站点用哪个协议**（§2.1）。默认按 WireGuard 写。如果业务上必须零客户端安装，或者必须能穿 UDP 被封的网络，就要改成 IKEv2/EAP 或 OpenVPN，V2 的工作量会翻倍以上（多一套 CA）。**这条定了才能开工 V2**。
2. **一个 VPC 是不是只允许一个 VPN 网关**。第一版按一个写。放开的话要重新想 VRID 分配和多网关的回程路由（不同网关的对端网段不能重叠，否则同一条路由有两个 nexthop）。
3. **PSK 的加密密钥用什么**。本文按独立的 `VPN_SECRET_KEY` 派生写（2026-09-23 从复用 `CAPTURE_UPLOAD_SECRET` 改过来，理由见 §3.6）。如果将来要引入真正的 KMS，现在存的密文要能重新加密——加密时带上版本前缀。
4. **站点连接要不要支持 IKEv1**。老设备（尤其是十年前的防火墙）只有 IKEv1。本文不做。

**待验证**（前三条最先做，都不需要写产品代码，在 work-01 现有的 `router-8` 里就能验；第 1 条是唯一还能改变设计的）

> 2026-09-23：各条的结果见 §14.2（1、3、5、6、7、8、10、11 已验证，2、4、9 未做）。

1. 🔴 **strongSwan 多实例的启动方式**（§2.2）。先查 Ubuntu 24.04 / 26.04 的包实际编译的 piddir（`strings /usr/lib/ipsec/charon | grep charon.pid`）；然后在 `router-8` 里按绕法 1（`unshare -m` + bind 私有目录到 `/var/run`）起两个 charon，或按绕法 2 直接起两个 `charon-systemd`，确认：两个都活着、各自的 pid / vici socket 在自己的目录里、`swanctl --uri` 能分别连上、`kill` 其中一个不影响另一个、SIGTERM 能正常退出。
2. **dnsmasq 能不能答从别的接口进来的查询**（§6.2）。做法：在 `router-8` 里建一个假的 wg 接口配上地址，从它发一个 `dig @192.168.62.1`，看现有的 dnsmasq 实例应不应。答了的话 `client_dns` 就能默认填内部网关，V2 少一整块工作；不答就按 §6.2 的方案 ① 排到 V3。
3. **netns 里的 rp_filter 实测值**（§2.3）。预期是 2：新 netns 的 IPv4 `conf/all`、`conf/default` 从 init_net 复制（`net.core.devconf_inherit_init_net` 默认 0），Ubuntu 默认 2。这条不再决定设计——`create_local_router.sh` 无论结果都显式设 2，实测只是确认「继承自 init_net」这个说法，顺手看一眼 `send_redirects`。
4. **全流量模式**（§10）。已确认默认 SNAT 规则会生效，真正的障碍是 `ti-N` 的 120 pps 限速和出口地址；要做得走 `te-` 并解决 fip 表缺站点路由的问题（fwmark 或同步路由），V3 视需求再设计。
5. **strongSwan 在 26.04 上的版本和包名**。24.04 是 5.9.x，26.04 可能是 6.x，`swanctl.conf` 的语法在两个大版本间基本兼容，但 `strongswan.conf` 的插件路径可能不同。
6. **多个 charon 实例的资源占用**。一个 VPC 一个 charon，几十个 VPC 就是几十个进程。实测单实例常驻内存，确认不会把节点压垮；超过某个数量要考虑改成单 charon 多 netns（不可行，XFRM 隔离在 netns）或者限制每节点的网关数。
7. **主备切换后隧道重建的实际时间**（§2.4）。文中写的 5–10 秒是估计值，实测后回填。
8. **`wg syncconf` 在加减 peer 时是否真的不影响其他 peer 的会话**。文档是这么说的，实测确认。
9. **对端设备互通**。手上只有两个 VPC 互联这一种测法，和真实的 Cisco / 华为 / 飞塔互通没有验证过。上线前至少要和一台真实设备对一次，或者和一个公有云的 VPN 网关对一次。**BGP 模式尤其要对**：TS 给 `0.0.0.0/0`、BGP 跑在隧道内链路地址上，这两条都要对端按路由模式配，配置差异比静态模式大得多。**static 模式要专门验多子网**：每对网段一个 child（§4.3），和只接受一对 TS 的设备（Cisco ASA / FortiGate）对一次；公有云那次顺带验隧道内地址在 `169.254.0.0/16` 时校验不会误拒（§10）。
10. **FRR 在 netns 里跑的可行性与资源占用**。`zebra` + `bgpd` 各起一份、`--vty_socket` 和 pid 指到网关自己的目录、不碰 `/var/run/frr`——这套在 netns 里没实测过。另外 FRR 9 之后引入了 `mgmtd`，要确认只跑 `zebra` + `bgpd` 是否完整可用。
11. **`vtysh` 的 JSON 输出格式**在 FRR 8 / 9 之间有过变化，上报解析要按节点上的实际版本写。

---

## 附录 A：与负载均衡的对照

| | 负载均衡 | VPN 网关 |
|---|---|---|
| 一个 VPC 几个 | 多个 | 一个（V1） |
| 数据面 | haproxy（用户态代理） | charon + 内核 XFRM / WireGuard（内核转发） |
| 回程 | 不需要——代理终结 TCP，回包自然回到 haproxy 的源地址 | **需要每节点静态路由**（§2.3），这是两者最大的差别 |
| 主备 | keepalived，两端都跑 haproxy | keepalived，charon 只在主节点跑；wg 两端都建 |
| 进程守护 | `check_lb_process.sh` | `check_vpn_process.sh`，复用同一把锁 |
| 状态上报 | `report_lb_health.sh`（后端健康） | `report_vpn_status.sh`（主节点 + 隧道 + 客户端） |
| 重启恢复 | `recover_loadbalancer` | `recover_vpn_gateway`，同样按 `boot_id` |
| 浮动 IP 的外网口与策略路由 | `create_lb_floating.sh` | 同一个脚本，原样复用 |
| 放行端口 | 在 `create_keepalived_conf.sh` 里，只放 TCP | 在 `create_vpn_gateway.sh` 里，放 UDP 500 / 4500 / 51820 与 ESP，BGP 的 179 由 `create_vpn_bgp_conf.sh` 放（**不是同一个脚本**） |
| keepalived 配置 | `create_keepalived_conf.sh` | `create_vpn_gateway.sh` 自己写（实例名与 notify 不同，§4.2） |
| notify_master | `set_route_table.sh`（恢复 fip 默认路由 + 免费 ARP） | `vpn_notify.sh`，第 0 步仍调 `set_route_table.sh`，再做路由 / 进程（§2.4） |

## 附录 B：动手时会撞上的既有问题

1. **VRID 用的是全局自增主键，不是每个 VPC 从 1 数**。`create_keepalived_conf.sh:64` 直接把 `VrrpInstance` 的自增主键当 `virtual_router_id`，而 VRRP 的 VRID 只有 8 位（1–255）。所以触发条件**和「单个 VPC 建了几个负载均衡」无关**——是**全平台累计**（含已删除的）建过 255 个 VrrpInstance 之后，第 256 个开始 keepalived 拒绝整个 `vrrp_instance`（配置其余部分照常加载），现象是「浮动 IP 一直不上来、日志里只有一行 `VRID not valid! must be between 1 & 255`」。work-01 上现在的 ID 还是个位数所以没暴露，但 VPN 网关会再消耗这个计数。V1 里修：加 `vrrp_instances.vrid`，**按 VRRP 子网**（= 每个 VPC，那才是 VRID 真正的唯一性范围——同一个二层域）分配 1–255 的空闲值。
2. 🔴 **`clear_vrrp_ip.sh` 按 MAC 删 fdb，会误伤同一个 VPC 里的其他 VRRP 实例**（2026-09-23 发现，未修）。同一 VPC 在同一节点上的所有 VRRP 接口**共用一个 MAC**（设备名按 VNI，`set_vrrp_ip.sh` 里「已被其他 VRRP 实例使用时沿用其 MAC」那段），而脚本里：

   ```bash
   bridge fdb del $local_mac dev v-$vrrp_vlan    # 按 MAC —— 有问题
   bridge fdb del $peer_mac  dev v-$vrrp_vlan    # 按 MAC —— 有问题
   ip neighbor del ${local_ip%%/*} ...           # 按 IP —— 没问题
   ip neighbor del ${peer_ip%%/*}  ...           # 按 IP —— 没问题
   ```

   删掉 LB1 时，work-01 上 `bridge fdb del $peer_mac` 删掉的是 **work-02 的 `ns-<vrrp_vlan>` 共用 MAC** 的条目，而 LB2 也在用它 → work-01 到 work-02 的 VRRP 单播心跳送不达（VXLAN 不泛洪）→ 对端收不到心跳 → **双主**。会自愈，但要等下次 `sendFdbRules` 重发（建虚拟机、节点重启恢复），在那之前一直双主。

   **VPN 网关会给这个 bug 多加一个触发源**：网关多半建在已经有负载均衡的 VPC 上，删网关会打断 LB、删 LB 会打断网关。建议在 V1 一并修掉：**删完之后由 clapi 无条件重发一次 `sendFdbRules`**（一行调用，不动脚本）——「哪个 MAC 还被谁用着」这个判断在节点上做不了，得查库。
3. **`create_veth.sh` 的固定噪音**。新建 VPC 路由器时 `create_veth.sh int-<N>` 会输出 `Error: argument "br<N>" is wrong: Device does not exist`（脚本末尾无条件把 veth 挂进网桥，而 `int-` 这条要移进 `router-0`）。不影响功能，但会混进 VPN 的部署日志里，排查时别被带偏。
4. **`create_lb_floating.sh` 的报错噪音**。未设带宽限制时删 tc 规则会输出 `invalid priority value`。VPN 网关复用这个脚本，同样会有。
5. **`Subnet.IsSite` 是死字段**（`model/subnet.go:25`，全仓库无读写点），别把它和站点连接搞混。
6. **`rescue_vm.sh` 的回调没有 handler**，clapi 会记 `no command rescue_vm found`。和 VPN 无关，但看日志时会看到。
7. **裸 `inter=` 会显式报错**。2026-09-16 起 cland 对 `inter=` 为负或为空返回 `error: no target node`，clapi 的 `HyperExecute` 会报错而不是静默丢弃。VPN 的所有下发都要确保目标节点有效（网关的主备节点、承载该 VPC 的节点集合），尤其是网关还在 `pending`、VrrpInstance 的 `hyper` 还是 -1 的时候。
8. 🔴 **`LoadBalancerAdmin.Delete` 回收 VRRP 子网只按 `load_balancers` 计数**（`services/loadbalancer.go:474`，2026-09-23 发现）。为 0 就 `subnetAdmin.Delete(VrrpSubnet)` 并清零 `router.vrrp_subnet_id`，根本不看这张子网里还有没有别的 `VrrpInstance`。今天没问题是因为 VrrpInstance 只有 LB 一种用户；VPN 网关一来，删掉同 VPC 最后一个 LB 就把网关的 VRRP 子网删了，之后再建 LB 会新建第二个 `192.168.196.0/24`。V1 改为按该路由器的 `vrrp_instances` 计数，网关删除走同一判据（§5.4）。

---

## 附录 C：复查改掉的 13 处（2026-09-22）

初稿写完后按代码逐条核对了一遍，下面这些是改过的。留档是因为有几条属于「读上去很合理、但和这个代码库的实际行为相反」，将来复查别的方案时是同一类陷阱。

**结论没变的部分**：跑在路由器 netns 里、两跳回程路由、站点到站点 IPsec + 客户端到站点 WireGuard 的选型。核对下来都站得住——特别是「第三方节点也有 `ns-<vrrp_vlan>` 且能二层直达主备的 VRRP 地址」这个前提，`sendFdbRules` 的 `localRules` 查询是 `router_id = ? and type <> 'gateway'`，VRRP 网卡（type `vrrp`）确实包含在内。

| # | 初稿写的 | 实际 | 改成 |
|---|---|---|---|
| 1 | 建 wg 接口时顺手装 `ip route ... dev wg-X` | 与 notify 的角色路由冲突，备节点会把流量吸进没握手的接口，黑洞 | 路由只归 `vpn_notify.sh`（§2.3、§4.4） |
| 2 | 隧道 MTU 1400 / 1420（按外层 1500 算） | `create_veth.sh:28` 把 netns 侧 veth 一律设 1450，`te-` 也是 | 1360 / 1390，并开 IKEv2 `fragmentation=yes`（§2.3） |
| 3 | `router_id` 单列唯一索引保证「一 VPC 一网关」 | 软删除的行还在，删一次就再也建不了 | 唯一索引带 `name` + 删除时改名，约束在 Create 里查（§3.1） |
| 4 | 创建后直接下发到「主备两个节点」 | `set_vrrp_ip.go` 是 MASTER 回调里才下发 BACKUP 的，创建返回时两个 `Hyper` 都还是 -1 | 明确的两阶段，并扩展 `UpdateLoadBalancerStatus`（§5.1） |
| 5 | 客户端私钥加密存库 | 与「服务端不再有明文」自相矛盾，且是最大的一处泄露面 | 根本不存，只留公钥（§3.3） |
| 6 | 未写 keepalived 配置由谁生成 | `create_keepalived_conf.sh` 写死了实例名和 notify，没有 notify_backup，不能复用 | `create_vpn_gateway.sh` 自己写；放 `vrrp-<id>/` 以复用 `check_lb_process.sh` 的守护（§4.1、§4.2） |
| 7 | `client_dns` 默认取子网网关 | dnsmasq 是 `bind-dynamic` + `--interface=ns-<vlan>`，从 wg 接口进来的查询大概率被过滤 | 默认留空，实测后再定（§6.2，待验证第 2 条） |
| 8 | 未说明 notify 脚本的输出去向 | 它是 keepalived 的子进程，stdout 不走 `\|:-COMMAND-:\|` | 明确写出来（§2.3） |
| 9 | 冲突校验只列了子网和 VRRP 子网 | `169.0.0.0/8` 是 `router-N`→`router-0` 的点对点链路，撞上整个 VPC 断网 | 补全冲突清单（§10） |
| 10 | 删客户端只说「释放地址」 | 软删记录会继续占着地址 | 明确置空 `ip_address`（§5.4） |
| 11 | 未提 ICMP 重定向 | 两跳同进同出接口，会持续发 redirect | `send_redirects=0`（§2.3） |
| 12 | 「补 11 条审计映射」 | 只有 10 条改动型接口 | 改成 10（§7.3） |
| 13 | 附录 A 说浮动 IP「同一个脚本」 | 放行端口的逻辑在 `create_keepalived_conf.sh` 且只放 TCP | 拆成三行说清楚（附录 A） |

## 附录 D：BGP 进入范围之后的改动（2026-09-22）

初稿把动态路由列为非目标，后来确认 **BGP 是必做项**，由此产生的连锁改动记在这里。

**最关键的一个发现**：最正统的做法（每节点 iBGP）在这个代码库上撞墙——**VRRP 子网里非主备节点只有 anycast 网关地址，全集群同一个**（`CreateVrrpInstance` 只建两个 `Interface`，第三个节点的 `ns-<vrrp_vlan>` 是 `set_subnet_gw.sh` 当普通网关口建的）。转发不受影响，但 BGP 要建 TCP 会话必须有唯一源地址。补齐它要新增一块「节点加入 / 退出 VPC 时动态增删 VRRP 子网地址」的逻辑，再加上每个 VPN VPC × 每个节点一个 bgpd。所以改走「外部 BGP + 内部汇总」（§2.6）。

| 改了什么 | 在哪 |
|---|---|
| BGP 从非目标移进范围；OSPF 与每节点 iBGP 仍是非目标 | §1.3 |
| 新增「BGP：只在主备两个节点跑，内部走汇总网段」整节，含为什么不做每节点 iBGP | §2.6 |
| 「对端网段」明确为派生集合，三个来源**都只随 API 变化** | §2.3 第 2 点、§3.4 |
| 主节点上给汇总网段装 `blackhole` 兜底——没有它，一次 BGP 撤销就是一次内网流量泄漏到公网 | §2.3 第 3 点 |
| `nonat` 只放汇总网段（`hash:net` 会命中明细），学到的前缀不进 ipset | §2.3 第 5 点 |
| `vpn_connections` 加 `route_mode`、`remote_summary_cidrs`、ASN、隧道内链路地址、TCP-MD5、BGP 定时器 | §3.2 |
| 新增 `vpn_remote_prefixes` 表；**BGP 学到的前缀明确不进这张表**（否则用户 PATCH 一次就被抹掉） | §3.4 |
| **TS 按 `route_mode` 生成，BGP 模式必须 `0.0.0.0/0`**——精确 TS 会让「邻居 up、路由学到、就是不通」 | §4.3 |
| XFRM 接口在 BGP 模式下要配 `tunnel_local_ip/30` | §4.3 |
| FRR（只跑 `zebra` + `bgpd`）进目录布局、脚本清单、守护、恢复、部署包列表；`zebra` 必须先于 `bgpd` 起 | §4.1、§4.2、§4.5、§9 |
| 心跳加第 4 类上报（邻居与学到的前缀），**只供展示、不参与下发**，要限量 | §6.1 |
| 测试加 6 个 BGP 用例，核心是「B 侧新建子网 → A 侧自动学到」和「删掉 → 在主节点 blackhole 而不是漏到公网」 | §11 |
| BGP 进 V1，不能挪后——它决定了 §2.3 的网段来源、§4.3 的 TS、`vpn_connections` 的字段 | §12 |
| 待决策新增第 1 条（汇总网段可不可接受），待验证新增 FRR-in-netns、`vtysh` JSON 格式两条 | §13 |

### D.1 2026-09-23：确认采用方案 1，待决策清零

把上面那条待决策定了，同时补齐了三处之前没想到的：

| 改了什么 | 在哪 |
|---|---|
| **确认采用「外部 eBGP + 内部汇总」**，V1 无阻塞项；十个候选方案连同否决理由整理成表留档（之前只写了其中三个，其余七条的理由没地方查） | §2.6、§13「已定」 |
| anycast 阻碍的说法改准：不只是「主节点分不清谁是谁」，是**包命中本机 local 表直接回环、根本不进 VXLAN** | §2.6 |
| 🔴 **汇总网段的第三个用途：FRR 的入方向 prefix-list**。之前只当它是「内部静态路由 + nonat」，漏了它其实是 BGP 模式下**唯一的**路由注入边界——TS 放开成 `0.0.0.0/0` 之后内核策略不再过滤，对端误通告 `0.0.0.0/0` 就能把整个 VPC 的出网流量劫进隧道，通告本 VPC 网段就能让同 VPC 虚拟机互访黑洞，两者都不需要任何凭据 | §2.6、§4.3.1、§10、§11（新增两条测试） |
| 「被拒绝的前缀」加进心跳上报，界面用它直接提示「汇总网段填小了」——比事后告警准确 | §6.1、§2.6 |
| 补了完整的举例（四个对端网段 + 两边路由表 + 新增/撤销/切换三种事件）、填太大 / 填太小的后果 | §2.6 |
| V1 顺手修 `clear_vrrp_ip.sh` 按 MAC 删 fdb 的既有 bug（不修的话删 VPN 网关会打断同 VPC 的 LB） | §12、附录 B 第 2 条 |

## 附录 E：第二轮复查改掉的 12 处（2026-09-23）

确认采用方案 1 之后又做了一轮代码核对式复查，重点是新写的 BGP 部分与既有章节的衔接。**交叉引用全部有效，汇总方案的核心设计站得住**，但衔接处有 12 处问题，其中 5 处会让功能不成立。

**根因**：新增章节时没有回头对齐既有章节。第 2 条尤其值得记——第一轮复查刚抓过一次「同一条路由有两个归属」，在新写的 §5.2 里又犯了一遍。所以这轮把归属关系做成了 §2.3 的显式表格，以后改下发逻辑先对照它。

| # | 问题 | 后果 | 改到哪 |
|---|---|---|---|
| 1 | §6.1 的接受条件「hostid 要等于当前 master_hyper」对**主节点身份上报**也生效 | **逻辑死锁**：`master_hyper` 永远变不了 → 整机宕机后给其余节点重发路由的路径**永远不触发**，而且毫无征兆 | §6.1 拆成两套条件 |
| 2 | §5.2 第 5 步让 clapi 给主备下发路由，与 §2.3「只有 `vpn_notify.sh` 一个归属」冲突；且 bgp 模式下主节点该装 blackhole 而非隧道路由 | 备节点把流量吸进没有握手的本地接口，黑洞 | §2.3 新增**路由归属表** + 三条硬规则；§5.2 重写 |
| 3 | §5.2 的下发流程里漏了 `create_vpn_bgp_conf.sh` | BGP 根本起不来 | §5.2 补上，并写明三步的顺序要求 |
| 4 | `local_cidrs` / `client_routes` 的「默认 = VPC 内所有内部子网」是**创建时的快照** | 用户给 VPC 加子网后：static 的 TS 里没有它（无报错）、bgp 不通告、客户端 `AllowedIPs` 缺失 | §3.2 改成派生值（留空=跟着 VPC 走）；§5.5 增加子网增删的触发 |
| 5 | `tunnel_routes` 两列格式表达不了 bgp 模式 | 实现时没法区分该装 blackhole 还是隧道路由 | §4.1 改成三列 `<网段> <类型> <主节点上的目标>` |
| 6 | `network` 语句依赖连接路由，而 `ns-<vni>` 只在子网**有网卡**时才建 | **没有虚拟机的子网不会被通告**，用户以为 BGP 坏了 | §4.3.1 写明，界面要分开显示「已通告 / 未通告」 |
| 7 | 用 `no bgp ebgp-requires-policy` 关掉 RFC 8212，出方向无过滤 | 靠「恰好只写了 network 语句」保证安全，将来加 redistribute 会泄露 | §4.3.1 改为配 `TO-PEER-OUT` 出方向白名单 |
| 8 | 主备切换的三个细节没写 | blackhole 晚于 bgpd 会**漏流量到公网**；缺 INITIAL_CONTACT 恢复变慢几十秒；`kill -9` 时 notify 不执行留下不自愈黑洞 | §2.4 新增两小节；§4.5 守护脚本改为**双向** |
| 9 | 客户端池要不要通告给对端没说 | 对端机房无法主动访问 VPN 客户端，且没有开关 | §3.2 新增 `advertise_client_cidr`（2026-09-24 又删掉，客户端与站点之间禁止互通，见 §14.7） |
| 10 | §8 表缺三种场景 | `kill -9`、范围外前缀被拒、新建子网都没覆盖 | §8 补三行 |
| 11 | §6.1 说「三类」实际四项 | — | 改为四类 |
| 12 | §11 测试第 13 条在本平台上做不到（我们的 FRR 发不出 `0.0.0.0/0`） | 执行的人会卡住 | 写明要手工进 B 侧 `vtysh` 临时加 |

测试相应增加四条（§11 第 13–17 条）：入方向过滤的手工构造方式、汇总填小的真实症状、**`kill -9` keepalived**、**主节点身份上报**（这条在两节点环境下测不出来，第三个节点才是受害者）、VPC 新建子网。

## 附录 F：第三轮代码核对改掉的 19 处（2026-09-23）

在方案 1 定稿、附录 E 收口之后，又按 `b99855f8` 的代码逐条核对了一遍，这次的重点是「方案自己没标为待验证、但与代码或系统实际行为相反」的地方，同时查了 strongSwan / FRR / 内核的官方文档。**核心机制全部站得住**：第三方节点确有 `ns-<vrrp_vlan>` 且能二层直达主备（`sendFdbRules` 的 `type <> 'gateway'` 含 vrrp 网卡，`add_fwrule.sh:16` 会建网关口）；MTU 1450（`create_veth.sh:23`）；`nonat` 的语义；`create_keepalived_conf.sh` 不可复用；`set_vrrp_ip.go` 的两阶段；`check_lb_process.sh` 的 glob 能顺带守 VPN 的 keepalived 且会先摘残留 VIP；`router-N` 的 FORWARD 没有策略限制，新接口不用加规则；FRR 的 `network` 语句要求 RIB 有路由且默认路由不算（NHT 不经默认路由解析，除非 `ip nht resolve-via-default`），所以「没有虚拟机的子网不通告」成立。

**5 处按原稿实现 V1 即不成立：**

| # | 原稿 | 实际 | 改到哪 |
|---|---|---|---|
| 1 | §2.2 用 `STRONGSWAN_CONF` + `charon.pid_file` 隔离多个 charon | `charon.pid_file` 不存在；pid 目录编译期 `--with-piddir` 固定，第二个 charon 见 pid 文件里的进程还活着就拒启 | §2.2 改为两个绕法（`unshare -m` bind 私有目录到 `/var/run`，或 `charon-systemd`），§4.1 目录加 `run/`，§13 待验证第 1 条最先做 |
| 2 | §2.4 `notify_master` 三步 | 漏了恢复 fip 路由表默认路由：`create_lb_floating.sh:29` 写进 `$ROUTES_FILE` 的那条随 VIP 摘除被内核删掉，LB 靠 `set_route_table.sh` 恢复（`check_lb_process.sh:28`）。漏掉则新主上源为 VIP 的外层包走 `ti-N` 被 SNAT，隧道永远建不起来 | §2.4 加第 0 步；§4.2 `create_vpn_gateway.sh` 先 `export ROUTES_FILE`；附录 A；§11 第 18 条 |
| 3 | 未提 VRRP 子网回收 | `LoadBalancerAdmin.Delete`（`loadbalancer.go:474`）按 `load_balancers` 计数为 0 就删 VRRP 子网，删最后一个 LB 会把网关的子网删掉 | §2.5、§5.4、§12、附录 B 第 8 条：改按 `vrrp_instances` 计数；§11 第 20 条 |
| 4 | §2.3 rp_filter 只在两跳切换时有影响，且「netns 不继承宿主机值」 | 正常运行就不对称（虚拟机不在主节点时回包从 `ns-<vrrp_vlan>` 进），strict 丢掉所有非主节点虚拟机的 VPN 流量；内核 `devconf_inherit_init_net=0` 时 IPv4 conf 从 init_net 复制，说法反了 | §2.3 改正并升为必做，`create_local_router.sh` 无条件设 2；§13 第 3 条只确认实测值 |
| 5 | 放行 UDP 500 / 4500 / 51820 | 漏了 ESP（协议 50）：对端无 NAT 时 IKEv2 不做 UDP 封装，`RELATED,ESTABLISHED` 只在我方先发时经 generic conntrack 偶然放行（600 秒空闲失效）；BGP 的 179 来自不在 `nonat` 的 /30，对端先发起被丢 | §4.2、§4.3（`encap = yes`）、§10、附录 A；§11 第 19 条 |

**中等（7 处）：**

| # | 问题 | 改到哪 |
|---|---|---|
| 6 | §10 全流量分析反了：`create_local_router.sh:92` 的 SNAT 在客户端池进 `nonat` 后**会**生效；真正的障碍是 `ti-N` 的 120 pps 限速（54–61 行）和出口地址不是 VIP，走 `te-` 又碰上 fip 表缺站点路由 | §10 改为 V2 不做、接口拒绝；§12 V2；§13 待验证第 4 条 |
| 7 | 第三方节点重启后 VPN 路由与 `nonat` 无人补（`recover_loadbalancer` 只查本节点的 VRRP 网卡，LB 没有这类状态） | §4.5、§5.5：补发挂在 `launch_vm` 回调含 `sync`；§8；§11 第 21 条 |
| 8 | §5.1 阶段二下发回程路由时 `master_hyper` 还是 -1，无从取 nexthop | §5.1：用 MASTER 角色接口地址作初值；§12 |
| 9 | 双主对 VPN 的伤害比 LB 大（两台 charon 同 VIP 互清 SA、路由每心跳翻转），原稿只当顺手修 B2 | §2.4 新小节、§6.1「不翻转只告警」、§8、§12 |
| 10 | strongSwan 把多对 TS 放进一个 CHILD_SA，Cisco / Fortinet 缩成第一对；两个 VPC 互测测不出 | §4.3 每对一个 child；§13 第 9 条 |
| 11 | `169.0.0.0/8` 禁令若套在隧道内链路地址上，与 AWS / Azure（强制 `169.254.0.0/16`）做不了 BGP | §10：链路地址只拒绝与本路由器实际 p2p /31 重叠；§13 第 9 条 |
| 12 | 加密密钥复用 `CAPTURE_UPLOAD_SECRET`，部署脚本对它「为空时自动生成」，误重置即全部不可解且无提示 | §3.6 改独立 `VPN_SECRET_KEY`，为空 / 解不开返回 503；§9、§12、§13 待决策第 3 条 |

**次要（7 处）：** `vpn_notify.sh` 不在锁内、与守护脚本「读 VIP → 启停进程」有窗口（§4.2、§4.5）；两端同时 FAULT 时路由互指成环，`notify_fault` 改装 blackhole（§2.3 归属表、§2.4、§4.2、§8、§11 第 22 条）；`bgp` 模式 initiator 用 `start_action = start`（§4.3、§8）；`%any` 响应方连接 `remote_id` 必填且唯一（§4.3、§10）；FRR 目录属主 `frr`（§4.1）；`set_vpn_route.sh` 自行保证 `ns-<vrrp_vlan>` 存在（§4.2）；V4 引用的待决策编号（原写第 2 条，应为第 1 条）与 `report_rc.sh` 行号更新（文首）。

测试相应增加五条（§11 第 18–22 条）：切主后 fip 默认路由、裸 ESP、删最后一个 LB 后网关不受影响、第三方节点重启、两端同时 FAULT。

---

## 14. 实施记录（2026-09-23，V1 + V2 + V3 一次做完并在 work-01/02/03 验证）

**范围**：§12 的 V1（共用骨架 + 站点到站点 + BGP）、V2（WireGuard 客户端到站点）、V3（配额、审计与动态、隧道告警、客户端配置文本）一次实施；**V4 未做**（IKEv2/EAP、证书认证、OpenVPN、每节点 iBGP）。代码未提交，以文件覆盖方式部署到 work-01（clapi、cpgateway、nginx 重建）与 work-02/03（脚本同步、软件包与 AppArmor 覆盖手工安装）。

### 14.1 与方案不同的做法

| 章节 | 方案原文 | 实际做法 | 原因 |
|---|---|---|---|
| §2.2 | 绕法 1 或 2 二选一，待验证 | **绕法 1**：`unshare -m` 后把 `<vpn_dir>/run` bind 到 `/run`，再起 `/usr/lib/ipsec/charon`（`vpn_lib.sh` 的 `vpn_start_charon`） | 26.04 的 `charon-systemd` 与 `strongswan-charon` 包互斥，装不到一起；charon 的 piddir 编译死为 `/var/run/charon.pid`（`strings` 核实） |
| §4.1 | `strongswan.conf` 只改 socket / 日志 | 还要 `load_modular = yes` + `include /etc/strongswan.d/charon/*.conf`，且**一行一个键**（单行嵌套段 charon 拒绝解析）；`install_routes = no` | 不 include 插件配置时 charon 起不来 |
| §4.1 | 只列了软件包 | **AppArmor**：Ubuntu 26.04 把 `charon`、`swanctl`、`bgpd`、`staticd`、`wg` 都限制在打包路径里，要写 `/etc/apparmor.d/local/{usr.lib.ipsec.charon,usr.sbin.swanctl,bgpd,staticd,wg}` 放行 `/opt/cloudland/cache/router/router-*/vpn-*/**`、`@{run}/frr/vpn-*/**`、`/etc/frr/vpn-*/*`（部署脚本与 Ansible 角色都已加） | 不加就是一堆 `Permission denied`，现象像配置写错 |
| §2.6 / §4.3.1 | 汇总网段的黑洞是内核路由（`ip route add blackhole`） | **黑洞由 staticd 持有**（`frr.conf` 里 `ip route <汇总> Null0 250`），FRR 守护进程顺序 `zebra → mgmtd → staticd → bgpd`（FRR 10 的 staticd 依赖 `mgmtd`） | 内核黑洞与 BGP 学到的同前缀路由并存时，zebra 不会用 BGP 路由替换掉它（内核路由距离 0），等于对端明细永远进不来 |
| §4.2 | `vpn_apply_routes` 直接装内核路由 | 主节点上「同一前缀 staticd 已持有」就不再装内核黑洞（`vtysh -c "show ip route X"` 里 `Known via "static"` 判定），角色切换时按 `proto boot` 删旧条目 | 交接给 FRR 后再装内核路由会打架 |
| §4.2 | `frr-reload.py` | 必须带 `--confdir /etc/frr/vpn-<gw>`（`-N` 只切 socket 路径），否则 reload 里的 `write` 会把默认 `/etc/frr/frr.conf` 覆盖掉 | 实测踩到 |
| §4.3 | 改连接只 `swanctl --load-all` | **静态 ↔ BGP 切换、网段变化的连接要 `--terminate` 再 `--initiate`**（`create_vpn_ipsec_conf.sh` 按连接块 diff） | CHILD_SA 保留旧的流量选择符，只 load 不生效 |
| §6.1 | 主节点身份「变化才上报」 | 主节点每 20 秒重发一次；**接管后的第一分钟每次心跳都发**；clapi 的分脑窗口 30 秒 | clapi 拒绝了一次主节点声明时节点无从得知，靠重发让声明在窗口过后立刻通过。改前主备切换后其他节点的路由要 50–62 秒才重指，改后 28 秒 |
| §3.5 | `vrrp_instances.vrid` 新列 | 加列 + `AutoUpgrade` 回填（`vrid = id`，`id ≤ 255`），新实例按 VRRP 子网分配 1–255 空闲值 | 两个 VPC 的网关各自拿到 vrid 1，与预期一致 |
| §7.2 | 配额 `vpn_gateways` | 默认 1；**已有配额行 AutoMigrate 后为 0**，部署时要 `UPDATE org_resource_quotas SET max_vpn_gateways = N`，否则创建一律 429 | 与 VPC / LB 配额加列时相同 |
| §7.4 | — | 列表页首次进入不发加载请求（`useListQuery` 只在依赖变化时重载，`onMounted` 漏了 `fetchGateways()`），界面冒烟测试抓到并修掉 | 类型检查 / lint 都过，只有真跑才发现 |

其他实施细节：`swanctl` 的子命令必须在 `--uri` 之前（`swanctl --list-sas --uri ...`，反过来报 `unrecognized option`）；`report_rc.sh` 调 VPN 钩子要 `sudo -E`（否则 `NODE_ID` 为空，回调被 clapi 以 `wrong params` 拒绝），`report_vpn_status.sh` 另从 `/etc/sysconfig/cloudlet` 兜底；`set_vpn_route.sh` 的 nonat 期望集合要压成一行（原先按行比较，刚加的条目又被删掉）；XFRM 接口 MTU 1360，`ipsec+` / `wg+` 上 MSS clamp；FRR 线程改名会被 AppArmor 拒绝（`/proc/<pid>/task/<tid>/comm`），无害，已在覆盖里放行。

### 14.2 §13 待验证的结果

| # | 项目 | 结果 |
|---|---|---|
| 1 | strongSwan 多实例 | ✅ 绕法 1 可行（两个 VPC 各一个 charon，pid / vici 各在自己目录，互不影响，SIGTERM 正常退出）；绕法 2 在 26.04 上装不了 |
| 2 | dnsmasq 答别的接口的查询 | ⏸ 未验，`client_dns` 仍按 §6.2 默认不下发 |
| 3 | netns 里的 rp_filter | ✅ 实测 `all=2 default=2`，`send_redirects=0`（`create_local_router.sh` 显式设置） |
| 4 | 全流量模式 | ⏸ 未做，接口拒绝 `0.0.0.0/0` |
| 5 | 26.04 的 strongSwan | ✅ 6.0.4，包 `strongswan-swanctl` + `strongswan-charon`，插件配置在 `/etc/strongswan.d/charon/` |
| 6 | 多实例资源 | ✅ 主节点每个网关常驻约 **65 MiB**（charon 12.5 + zebra 11 + mgmtd 10 + staticd 6 + bgpd 16 + keepalived 9），备节点只有 keepalived 约 9 MiB |
| 7 | 主备切换时间 | ✅ `kill -9` 主节点 keepalived：VIP 3 秒内切换，隧道与 BGP 在新主上重建，第三节点间的 TCP 探测**中断 0–26 秒**（90 次探测失败 6–10 次），clapi 记录的主节点 28 秒后切换（改前 50–62 秒） |
| 8 | `wg syncconf` 加减 peer | ✅ 停用客户端 4 秒内 peer 被移除、流量停止；重新启用后 **13 秒**恢复（是客户端侧等 WireGuard 重新握手的时间，不是网关） |
| 9 | 真实设备互通 | ⏸ 未做，只有 VPC ↔ VPC |
| 10 | FRR 在 netns 里 | ✅ FRR 10.5.1，`-N vpn-<gw>` pathspace，**必须跑 mgmtd**（staticd 依赖它） |
| 11 | vtysh JSON | ✅ 10.5.1 的 `show bgp ipv4 unicast summary json`：`peers[<ip>].state / pfxRcd / pfxSnt / peerUptime` |

### 14.3 测试结果（对应 §11）

环境：两个 VPC（vpn-a `192.168.71.0/24`，vpn-b `192.168.72.0/24`），各 3 台 Ubuntu 24.04 minimal 虚拟机分散在三个节点，两个网关互为对端。minimal 镜像没有 `ping`，探测一律用 TCP/22（`bash /dev/tcp`）。测试脚本在 work-01 `/root/vpn-e2e-*.sh`（`vpn-e2e-lib.sh` 是公共函数，状态在 `/root/vpn-e2e.state`）。

- **静态站点到站点**：两个网关创建后约 10 秒 `available`；连接建立后 3×3×2 方向 **18/18** 探测通过，conntrack 里源地址是虚拟机真实地址（nonat 生效）
- **BGP**：会话约 20 秒 `Established`；VPC B 新增子网 `192.168.73.0/24` 后 VPC A **不改任何配置**自动学到并可达（18/18 + 新子网 4/4）；prefix-list 按 `remote_summary_cidrs` 过滤生效
- **静态 → BGP 在线切换**：PATCH 连接后隧道自动重建、流量选择符更新
- **主备切换**：见 14.2 第 7 条；旧主由守护脚本拉起 keepalived 回到 BACKUP，VIP / charon / FRR 全部清零；切回再切正常
- **WireGuard 客户端**：平台代生成密钥，配置文本一次性返回私钥（配置端点只给不含私钥的模板）；客户端在 work-03 宿主机上 `wg-quick up`，到 VPC A 三台虚拟机 3/3，虚拟机反向到客户端地址可达；心跳上报握手时间与流量；停用 / 启用见 14.2 第 8 条
- **界面**：列表、详情四个标签（概览 / 站点连接 / 客户端 / 操作记录）、概览页配额条，Playwright 只读冒烟 0 页面错误（`/root/console-test/vpn-ui.js`）
- **回归**：`rb-lb` 全程 10/10；clapi 无 `no command` 噪音；三节点 AppArmor 无新增拒绝（放行 `comm` 后）
- **清理**：删网关 / 虚拟机 / 子网 / VPC 后三节点 `vpn-*` 目录、charon、FRR、`wg-*` / `ipsec-*` 接口、路由与 nonat 条目全部清零，VRRP 子网随最后一个实例删除，浮动 IP 释放

### 14.4 没做 / 没测的

- V4 全部；真实设备互通；dnsmasq 客户端 DNS；全流量模式；多客户端并存时 `wg syncconf` 对其他 peer 会话的影响（代码复查时并发建过两个客户端，只验证了地址分配，没测两个客户端同时在线）

### 14.5 节点重启实测（2026-09-24，三次真实重启 vpn-a 的主节点 work-02）

前两次各暴露一个缺陷，修掉后第三次通过；三处修改都**未提交**、已部署 work-01（clapi 重建）与三节点脚本：

| # | 现象 | 根因 | 修法 |
|---|---|---|---|
| 1 | 节点回来后 `recover_vpn_gateway` 回调 panic（cland 重试 3 次都 panic），vpn-a 的 keepalived 没有在它上面重建，**网关从此只剩一个节点**，心跳不会纠正 | `VpnRecoverNode` 把 `GetVrrpInterfaces` 返回的网卡（只预加载了 `Address`）传给 `SendFdbRules`，后者解引用 `Address.Subnet` | 传外层循环里已预加载 `Address.Subnet` 的同一块网卡；`SendFdbRules` 对缺地址 / 子网的网卡跳过并记错误（`common/fdb.go`） |
| 2 | 主备切换后跨 VPC 流量中断 **80 秒**（+8 .. +91 秒），而 `kill -9` keepalived 的切换只有 20 秒左右 | ① keepalived 调 notify 脚本时 umask 更严，`vpn_start_frr` 的 `mkdir -p /var/run/frr/vpn-<gw>` 建出 0600 目录，FRR 切到 `frr` 用户后 `Can't create pid lock file` 立即退出，4 个守护进程全没起来、`vtysh -b` 失败但脚本回 0；② clapi 切主后 `set_vpn_route.sh` → `vpn_notify.sh sync` 在**宿主机 netns** 跑 `set_route_table.sh`，`ip route replace default ... table fip-756` 永远失败、每 2 秒重试到 60 秒超时，期间持有 lb 锁；60 秒后它才把 FRR 拉起来。`kill -9` 场景下 ② 同样发生，但守护脚本 `check_vpn_process.sh`（cloudlet 上下文、umask 022）在锁空闲的间隙把 FRR 拉起来了，所以只表现为 20 多秒 | `vpn_notify.sh` 固定 `umask 022`，FRR 运行目录 `install -d -m 755 -o frr -g frr`；`set_route_table.sh` 一律 `ip netns exec <router>` 执行。另给 `vpn_notify.sh` 加了分步时间戳日志 `<vpn_dir>/notify.log`（`VPN_TRACE` 时 `vpn_start_charon` / `vpn_start_frr` 也记每一步，FRR 启动输出进日志），排这类问题不用再猜 |
| 3 | 保留环境 `rb-lb` 在 work-02 重启时中断 165 秒 | 与 VPN 无关：`rb-lb` 的 work-01 一侧（`vrrp-3` / `lb-3`）早已不在，只靠 work-02 一个节点 | 删掉 work-01 的 `need_to_sync_lb` 标记让 `recover_loadbalancer` 重建，之后第三次重启 `rb-lb` 0 失败 |

第三次的时间线（探测 va3@work-03 → vb3@work-03，每秒一次）：VIP +9 秒；charon 0.5 秒起来、隧道 +9 秒；对端 BGP 旧会话 +14 秒 Idle、新会话 +18 秒 Established；clapi 切主与第三节点路由重指 +24 秒；**探测中断 +8 .. +23 秒**（338 次失败 6 次）；节点 ssh 回来 +142 秒、cloudlet 注册 +143 秒、虚拟机全部 running +144 秒、hyper 上线 +166 秒、`recover_vpn_gateway` 后 keepalived 以 BACKUP 起来 +186 秒（MAC、fdb / neigh、nonat、回程路由全部正确，AppArmor 0 拒绝）；切回后 charon / FRR / wg 在 2 秒内起来，矩阵 18/18、vb4 3/3、WireGuard 客户端 3/3；`rb-lb` 0 失败。测试脚本：work-01 `/root/vpn-e2e-7-monitor.sh <节点>`（探测 + 跟踪，重启命令另发），用例 `test-items/TC-14` HA-08。

**§13 待验证第 7 条更新**：主备切换的实际中断，`kill -9` 场景 5–16 秒、整机重启 8–23 秒（修复后）。

### 14.6 代码复查后的修复（2026-09-24，未提交，已部署 work-01 与三节点验证）

`/code-review` 提了 15 条，逐条核对后 10 条是真问题，加上验证时新暴露的 3 条，一起修掉：

| # | 问题 | 修法 | 验证 |
|---|---|---|---|
| 1 | 脚本用一次 `read -d'\n'` 接 jq 的多行输出，空字段让后面的变量整体错位——「仅响应」连接的 `remote_gateway` 为空就永远起不来（12 处同一写法） | `vpn_lib.sh` 的 `vpn_json_read`（每个字段经 jq `@sh` 单独赋值，空串保留、null 变字符串 `null`），12 处全部替换 | 仅响应连接的 swanctl 块 `remote_addrs = %any`、id / 提案 / start_action 各在其位 |
| 2 | `vpn_client.go` 的 `Set("gorm:query_option","FOR UPDATE")` 在 GORM v2 里被忽略，客户端地址分配无锁 | `clause.Locking{Strength: "UPDATE"}` | 并发建两个客户端拿到 `.2` / `.3` |
| 3 | `VpnGatewayVrrpReady` 在事务里写 `status=error` 后把错误返回，整个 `set_vrrp_ip` 回调回滚（备节点网卡的节点、error 标记一起丢），网关永远 `pending` | 记下错误后返回 nil，让事务提交 | — |
| 4 | 创建网关先 `CreateVrrpInstance`（已向节点下发 `set_vrrp_ip.sh`）再分配公网 IP，后者失败只回滚数据库、节点上留 VRRP 网卡 | 先分配公网 IP 再建 VRRP 实例 | 指定已占用的 `public_ip` → 400，`vrrp_instances` 不增、三节点无网卡、无下发 |
| 5 | 创建连接把 `remote_id` 默认成对端地址存进库，改 `remote_gateway` 后仍用旧身份 | 不再落库默认值，下发时按对端地址派生（原本就有） | — |
| 6 | 变更连接后只重新发起 `--child net0`，静态模式多对网段只回来一个 CHILD_SA | `vpn_conn_children` 取块内全部子 SA 逐个发起（`create_vpn_ipsec_conf.sh`、`restart_vpn_conn.sh`） | 两个本地网段 → 2 个 CHILD_SA，改远端网段后仍是 2 个 |
| 7 | `error` 是终态：之后的 `ready`、PATCH、节点恢复都不再下发；`pending` 期间的 PATCH 也不下发 | 任一 PATCH 在 `error` 时重新下发并置回 `pending`（ready 回调再收敛）；VRRP 节点已知时 `pending` 也下发；`VpnRecoverNode` 也重建 `error` 网关的节点 | 库里置 `error` → PATCH 描述 → `pending` → 1 秒后 `available`；这条路径当天真实救回过两个网关 |
| 8 | `vrid` 回填只到 `id <= 255`，更早的行保持 NULL，`Pluck` 进 `[]int` 报错；同 VPC 并发创建无唯一性保护 | `AutoUpgrade` v1：NULL → 0、列 `NOT NULL DEFAULT 0`、部分唯一索引 `(vrrp_subnet_id, vrid) WHERE deleted_at IS NULL AND vrid > 0`；`allocateVrid` 跳过 0；`CreateVrrpInstance` 先 `FOR UPDATE` 锁路由器行 | 部署后列定义与索引已核对 |
| 9 | `create_vpn_gateway.sh` 只会上报 `ready`，失败路径不上报，clapi 的 `error` 分支不可达 | `trap ... EXIT`：非零退出上报 `error`；`create_lb_floating.sh` 后按公网口是否建出来判断（它的退出码是最后一条 tc 清理的，没设带宽限制时必然非零——第一版直接 `|| die`，把所有网关都打成了 `error`）；keepalived 起不来也 `die` | 那次误判就是靠第 7 条的 PATCH 重下发救回来的 |
| 10 | cpgateway 删除区域的用量检查没加 `vpn_gateways` | 加上 | 编译 |
| 11 | 状态上报在 clapi 接受之前就写缓存，切主窗口内被拒的上报最长 5 分钟不重发 | 接管后第一分钟内连接 / 客户端 / BGP 上报也每次心跳都发 | 切主后新主 60 秒内 `status=up`、BGP 状态刷新 |
| 12 | `launch_vm` 每台虚拟机都触发一次路由重发 | `VpnResyncNode` 按（路由器，节点）10 秒去重 | — |
| 13 | **验证时新发现**：`create_vpn_ipsec_conf.sh` 判断「terminate 之后要不要重新发起」用 `grep -A20`，而 `start_action` 在块的第 26 行之后，永远匹配不到——之前隧道能回来全靠对端的 `close_action = start` | 改成按块范围的 awk 判断（与 `restart_vpn_conn.sh` 一致） | 切换路由模式后不再需要手工 restart |
| 14 | **验证时新发现，根源性**：`close_action = start` 让 charon 在对端关闭 IKE 时按**被关闭的 CHILD_SA 自己的选择符**重建（不是按当前配置），两端改配置的时序一交错，旧选择符就永远互相复活；BGP 的 /30 不在任何 SA 里，会话一直 `Active` | `close_action = none`；守护脚本在主节点每次心跳做 `vpn_ipsec_reconcile`：已装 CHILD_SA 的选择符与配置不一致、或（发起方）一个也没装，就 terminate 再按配置发起，每条连接 60 秒最多一次 | 两端各自 PATCH 后 25 秒内收敛到 `0.0.0.0/0`、BGP Established、矩阵 18/18 |

**不修的**：9（VPC 加减子网重建静态隧道断几秒，已知限制）、14（校验正则重复）。

**跑通的用例**：F1–F5（`/root/vpn-e2e-9-fixes.sh`）、静态 ↔ BGP 在线切换、`kill -9` 主节点 keepalived 后新主的隧道 / BGP / 状态上报、TC-17 的 VPN-02/03 矩阵。

### 14.7 VPN 客户端与远端站点禁止互通（2026-09-24，未提交）

**决定**：WireGuard 客户端不能经网关访问远端站点，远端站点也不能经网关访问客户端。两种接入各自只通 VPC。

原先的状态：静态模式下客户端地址池不在流量选择符里，本来就不通；BGP 模式的选择符是 `0.0.0.0/0`，只要推送路由里加上远端网段、再打开 `advertise_client_cidr`，就能串起来。这条路径从来没测过。

**做法**：

- **数据面**：网关两个节点的路由器 netns 在 `FORWARD` 链最前面插两条 `DROP`，分别是 `-i wg+ -o ipsec+` 与 `-i ipsec+ -o wg+`（`vpn_lib.sh` 的 `vpn_isolate_rules`，`create_vpn_gateway.sh` 加、`clear_vpn_gateway.sh` 删）。无论推送路由、BGP 学到的前缀、本端网段怎么配，包在这里都会被丢掉。每个 VPC 最多一个网关，所以通配接口名只会匹配这个网关自己的设备
- **控制面**：删掉 `vpn_connections.advertise_client_cidr`（`AutoUpgrade` 删列）、接口字段、BGP 配置里的 `network <client_cidr>` 与前缀列表条目、前端连接弹窗里的选项
- **没有做的**：推送路由（`client_routes`）不校验是否与远端网段重叠。填了也只是让客户端把包发给网关、在网关被丢弃
- **代码复查后补强**（2026-09-24）：规则原先只在 `create_vpn_gateway.sh` 里插一次。删网关时按通配接口名删，同一 VPC 删旧建新时若旧网关的清理晚于新网关的创建执行，新网关的规则会被一起删掉；插入失败也不检查。两处都会让隔离静默失效。现在守护脚本每次心跳都检查并补回（实测手工删掉后 4 秒补回），删网关时路由器上还有别的网关目录就不删这些通配规则（MSS 钳制规则同理，实测用不存在的网关 id 跑一次清理，新网关的 2 条隔离、4 条 MSS 规则都保留）

### 14.8 网关停用 / 启用与"至少开一种能力"的前端预检（2026-09-24，未提交，已部署 work-01 与三节点验证）

**需求**：原来 `ipsec_enabled` / `client_enabled` 只是"提供哪种能力"的开关，关掉要先删光连接或客户端，而且两个不能同时关，所以没有办法临时停掉整个网关而保留配置和公网 IP。

**做法**：

- **接口**：`PATCH /vpn_gateways/:id {"enabled": false|true}`，返回体多一个 `enabled`。库里存的是反向的 `vpn_gateways.disabled`（`not null default false`），零值与存量行都表示启用，不需要回填
- **clapi**：停用时所有连接置 `disabled`，启用时置 `pending` 等首次上报；两次都清掉 `established_at` 和 BGP 快照（界面上的 BGP 邻居状态读的是快照，不清会在停用期间一直显示 `Established`）。停用时对处于 `down` 的连接先发一次恢复：暂停之后的第一次上报不是 down↔up 转换，原来在告警的连接永远等不到恢复。停用期间新建的连接直接是 `disabled`；重启连接返回 400（新错误码 132008）；主节点的状态上报一律忽略。纯启停请求在审计里记为 `vpn_gateway.enable` / `vpn_gateway.disable`
- **节点**：`create_vpn_gateway.sh` 的输入多一个 `enabled`，据此在网关目录写或删 `disabled` 标记文件（主备两个节点都写），标记变化时最后调一次 `vpn_notify.sh <gw> sync`。`vpn_lib.sh` 的 `vpn_disabled` 被所有拉起进程的路径检查：`vpn_start_charon` / `vpn_start_frr` 直接返回，`vpn_apply_routes` 不分角色一律黑洞（包括客户端地址池），`vpn_apply_wg` 把 `wg-<gw>` 置 down；`vpn_notify.sh` 在停用时先黑洞路由、再停 charon 和 FRR、再黑洞一次（zebra 退出时会撤掉它装的路由），主节点仍恢复 fip 路由表；守护脚本对停用的网关只做"确保不在运行"；上报脚本只发主节点身份，不发连接 / 客户端 / BGP 状态，并删掉上报缓存，启用后第一次心跳立即上报。公网地址上的 IKE / ESP / WireGuard 放行规则停用时不加，对端和客户端收不到任何回应
- **不变的**：keepalived、VRRP、公网 IP、`nonat`、隔离规则（§14.7）、所有配置文件。停用期间照样可以改配置、加连接和客户端，启用后按最新配置起来
- **前端**：详情页操作菜单"停用 / 启用"（停用有确认弹窗说明后果），停用后标题与列表的状态显示"已停用"、页面顶部黄色横幅带"启用"按钮、连接状态"已停用"、重启连接按钮禁用。创建与编辑弹窗在提交前检查"至少开一种能力"，编辑时还检查"关站点到站点要先删连接""关客户端 VPN 要先删客户端"，给中文提示（原来是后端英文报错）

**实测**（`/root/vpn-e2e-11-disable.sh`，两条连接 BGP 模式，外加一个客户端）：

| 项目 | 结果 |
|---|---|
| 停用生效 | PATCH 后约 0.9 秒两个节点都完成（`notify.log`："disabled: routes blackholed, processes stopped"）；VPC A↔B 探测 0/6、客户端不通；两个节点的对端网段与客户端地址池都是黑洞，放行规则 0 条，`wg-<gw>` DOWN |
| 两次心跳之后 | 守护脚本没有拉起任何进程；连接仍是 `disabled`；对端网关看到的是 `down` / BGP `Active` |
| 停用期间切主（`kill -9` 主节点 keepalived） | 新主节点 notify 走停用分支，charon / FRR 不起、路由仍是黑洞；旧主被守护脚本拉起成 BACKUP 同样如此；探测仍 0/6 |
| 启用 | 新主节点约 1.3 秒起 charon 和 FRR；VPC A↔B 与客户端 **20 秒**恢复（BGP 重新建立），连接回到 `up` / `Established` |
| 接口 | 停用期间重启连接 400（132008）；审计依次为 `vpn_gateway.disable` 200、`connection_restart` 400、`update` 409、`vpn_gateway.enable` 200 |
| 界面（本机 Playwright，放行对测试网关的启停 PATCH） | 16 项通过：两种能力都关 / 有连接时关站点到站点的中文提示、停用确认弹窗、"已停用"标题与横幅、连接"已停用"且不显示 `Established`、重启按钮禁用、列表页"已停用"、横幅一键启用、无页面错误 |

第一轮实测暴露一处遗漏并已修：停用只清了 `bgp_state` 列，界面读的 BGP 快照 `bgp_status` 没清，停用期间连接行仍显示 `Established`。

### 14.9 界面调整与一个既有问题（2026-09-24，未提交，已部署 work-01）

三轮前端改动，都只动前端、在本机 Playwright 上验证后部署到 work-01（部署前逐文件核对，覆盖前的文件备份在 work-01 `/root/web-before-*-20260924.tar`）：

| 改动 | 原因 | 验证 |
|---|---|---|
| 列表页去掉 VPC 筛选 | 每个 VPC 最多一个网关，筛选只会得到 0 或 1 条 | 工具条无下拉、首次进入的请求不带 `vpc_id`、搜索与空状态正常 |
| 创建弹窗：已有网关的 VPC 禁选，默认选没有网关的；4 个 VPN 弹窗的报错挪到底栏；「该 VPC 已有网关」翻成中文 | 用户点「创建」以为没反应：默认选中了已有网关 `s1` 的 `rb-vpc`，后端 4 次 409，报错在长表单最底部要滚动才看得到且是英文 | 800px 高的窗口里两种能力都关、后端 409（本地模拟）的提示都在可视区域；work-01 上 `rb-vpc` 禁选、默认 `mig-vpc` |
| VPN 网关与负载均衡创建弹窗的可用区改为下拉；VPN 公网 IP 改为按公网子网列出空闲地址的下拉 | 手填不知道有哪些可用区、哪些地址空闲，填错要等提交后才被拒 | 8 项通过：默认「自动」、选子网前地址禁用、请求体带 `zone` / `public_subnet` / `public_ip`、子网改回自动时地址清空 |

**顺带发现的既有问题（未修，CLAUDE.md 待办 B11）**：`GET /addresses/:subnet` 把子网网关那一行当成空闲地址返回（`public-756` 的 `52.117.101.145`），`common.AllocateAddress` 自动分配时排除网关，**指定地址时不排除**。VPN 的地址下拉已在前端排除；创建弹性 IP 页面与创建云服务器弹窗的地址下拉仍会列出网关地址，选了会把上游网关地址分配出去、整个公网子网断网。修法：指定地址分支加 `address != subnet.Gateway`，两个下拉同样排除。
