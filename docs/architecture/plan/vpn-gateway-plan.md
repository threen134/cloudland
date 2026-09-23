# VPN 网关设计（站点到站点 + 客户端到站点）

- **状态**：设计草案，未实施。**§13 的待决策已全部不阻塞 V1，可以开工**（BGP 的路由方案 2026-09-23 确认采用 §2.6 的方案 1）。已经过一轮代码核对式复查（附录 C），之后按「BGP 必做」修订过一轮（附录 D）
- **日期**：2026-09-22，基于 `stage-01` 分支 `1c186c18`（文中行号以此为准）
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
- **最难的一点是东西向回程路由**（§2.3）：VPC 网关是 anycast，虚拟机的回包只会进入**它自己所在节点**的 `router-N`，而隧道只在一个节点上。方案是在每个承载该 VPC 的节点的 `router-N` 上加一条指向「VPN 主节点 VRRP 子网地址」的静态路由，主备两端再用 keepalived 的 notify 脚本即时改写本地那一条。进程级切换零收敛，主节点整机宕机时靠心跳上报的主节点身份变化驱动 clapi 重发，收敛 1–20 秒。
- **对端网段必须加进路由器 netns 的 `nonat` ipset**，否则去往对端的流量会被 `create_local_router.sh:92` 的 SNAT 规则改成 `169.x` 源地址。这一条同时也让 `create_local_router.sh:88` 放行隧道来的入站流量。
- **凭据不回读**：站点连接的 PSK 与网关自己的 WireGuard 私钥加密存库，任何 GET 接口都不返回；**客户端私钥根本不存**，平台生成时只在创建响应里返回一次，库里只留公钥。下发到节点一律走 `ShellEscape` + `<<'EOF'`。
- **隧道接口 MTU 按 1450 算，不是 1500**：`create_veth.sh:28` 把路由器 netns 侧的所有 veth（含公网口 `te-`）都设成 1450，外层封装的上限就是它。
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

**strongSwan 的多实例隔离**需要注意：charon 默认读 `/etc/strongswan.conf`、`/etc/swanctl/swanctl.conf`，pid 和 vici socket 也是固定路径。每个网关一个目录，用环境变量 `STRONGSWAN_CONF` 指到自己的 `strongswan.conf`，在里面改掉 `charon.plugins.vici.socket`、`charon.pid_file`、`charon.filelog`（§4.2）。`swanctl` 调用时带 `--uri unix://<自己的 vici.sock>`。

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
   - `notify_backup` / `notify_fault`：一律 `ip route replace <网段> via <对端 VRRP 地址> dev ns-<vrrp_vlan>`（BGP 连接的汇总也是，因为这时本机没有明细）

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

**需要注意的四件事**：

- **rp_filter**。两跳的情况下路径不对称：去程是「第三方节点 → 原主节点 → 新主节点 → 隧道」，回程是「新主节点 → 第三方节点」。包从 `ns-<vrrp_vlan>` 进入原主节点时，源地址是 VPC 内网段，而该源地址的回程路由走 `ns-<vni>`，**strict 模式的 rp_filter 会丢包**。netns 有独立的 sysctl 且不继承宿主机当前值，所以要在 `create_local_router.sh` 里显式设成 loose：`net.ipv4.conf.all.rp_filter=2`、`net.ipv4.conf.default.rp_filter=2`。列为 §13 待验证第 1 条。
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

### 2.5 命名与既有约束

- **不要用 `site` 这个词**。`Subnet.Type` 里已经有 `site`（`api/src/common/constant.go:27`），指的是一段可以整体挂到虚拟机上的公网地址，和「站点到站点」完全是两回事。本文统一用：网关 = `vpn_gateway`，站点连接 = `vpn_connection`，客户端 = `vpn_client`。中文用「VPN 网关」「站点连接」「VPN 客户端」。
- **`Subnet.IsSite` 字段全仓库没有任何读写点**（只有 `model/subnet.go:25` 一处定义），不要误以为它和站点有关。
- **VRID 只有 8 位（既有缺陷，见附录 B 第 1 条）**。`create_keepalived_conf.sh:64` 直接用 `VrrpInstance` 的自增主键当 `virtual_router_id`，超过 255 时 keepalived 会拒绝整个 `vrrp_instance`。VPN 网关会再消耗 VrrpInstance ID，让这个问题提前暴露。V1 里顺手修掉：在 `vrrp_instances` 上加 `vrid` 列，创建时在**同一 VRRP 子网内**分配 1–255 的空闲值，配置脚本改用它。

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
| `client_routes` | varchar(512) | 推给客户端的网段（`AllowedIPs`），默认 = VPC 内所有内部子网；填 `0.0.0.0/0` 为全流量模式 |
| `master_hyper` | int32 | 心跳上报的当前主节点 hostid，默认 -1 |
| `master_reported_at` | timestamp | 上次上报时间 |

⚠️ **「一个 VPC 一个网关」不能靠 `router_id` 的唯一索引实现**。`model.Model` 是软删除，删掉的网关那行还在表里，单列唯一索引会让这个 VPC **永远建不出第二个网关**。按仓库既有的做法（PET-1228：先 soft-delete 再 `Unscoped` 改名）：唯一索引建在 `(name, router_id)` 上（和 `LoadBalancer` 的 `idx_router_lb` 一致），删除时把 `name` 改成带时间戳的唯一值；「一个 VPC 一个」的约束在 `VpnGatewayAdmin.Create` 里查一次存活记录来保证。`vpn_connections` 的 `(name, vpn_gateway_id)`、`vpn_clients` 的 `(name, vpn_gateway_id)` 同理。

### 3.2 `vpn_connections`（站点连接）

| 列 | 类型 | 说明 |
|---|---|---|
| `owner` / `name` | | `name` 与 `vpn_gateway_id` 唯一 |
| `vpn_gateway_id` | int64 | |
| `status` | varchar(32) | `pending` / `down` / `up` / `error` |
| `remote_gateway` | varchar(64) | 对端公网地址；空表示只作响应方（对端地址不固定），此时 `initiator` 必须为 false |
| `remote_id` | varchar(128) | 对端 IKE 标识，默认等于 `remote_gateway` |
| `local_id` | varchar(128) | 本端 IKE 标识，默认等于浮动 IP |
| `route_mode` | varchar(16) | `static` / `bgp`（§2.6）。决定 TS 怎么生成、要不要起 bgpd |
| `local_cidrs` | varchar(512) | 本端网段，逗号分隔；默认 = VPC 内所有内部子网。`bgp` 模式下用于 BGP 通告（`network` 语句），不用于 TS |
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

平台目前没有 KMS，用现成的对称密钥派生：clapi 侧用 `CAPTURE_UPLOAD_SECRET`（HA 下两台 clapi 共享，`deploy-ha-node.sh` 已经在同步它）经 HKDF 派生一个 32 字节密钥，AES-256-GCM 加密，密文 base64 存库，**前面带一个版本前缀**（`v1:`），将来换 KMS 时能识别出哪些要重新加密。

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
    ├── strongswan.conf              # STRONGSWAN_CONF 指向它，改掉 vici socket / pid / 日志路径
    ├── swanctl.conf                 # 权限 600，含 PSK
    ├── charon.pid
    ├── vici.sock
    ├── charon.log
    ├── wg.conf                      # 权限 600，含网关私钥与各 peer 公钥
    ├── frr/                         # 只有主备两个节点有，且只在持 VIP 时跑（§2.6）
    │   ├── frr.conf                 # 权限 600，可能含 TCP-MD5 口令
    │   ├── vtysh.conf
    │   └── *.pid / *.vty
    ├── tunnel_routes                # <网段> <设备|blackhole> 一行一条，vpn_notify.sh 据此改写路由
    └── status.reported              # 上次上报的状态文本，用于「变化才上报」
```

**keepalived 的配置放在 `vrrp-<id>/` 里是刻意的**：[check_lb_process.sh](../../../scripts/kvm/check_lb_process.sh) 的 glob 就是 `$router_dir/router-*/vrrp-*/keepalived.conf`，放进去就免费拿到了它那套进程守护——包括「`kill -9` 后先摘掉残留浮动 IP 再拉起」和「网卡还没建好时不拉起」这两段已经在真实重启里验证过的逻辑。VrrpInstance ID 在 LB 和 VPN 之间是同一张表的自增主键，目录不会撞。代价是 `check_vpn_process.sh` 只管 charon 和 wg，不管 keepalived——**这一点要在两个脚本的注释里互相指明**，否则后来的人会以为漏了。

新增依赖（`deploy/docker/scripts/deploy-compute-node.sh` 的安装列表和 `deploy/roles/hyper/tasks/main.yml` 一起加）：

- `strongswan-swanctl`、`strongswan-charon`（Ubuntu 24.04 是 5.9.x，26.04 可能是 6.x；配置一律用 swanctl，两个大版本通用，**不要用 `ipsec.conf` / starter**）
- `wireguard-tools`（内核模块 24.04 / 26.04 都自带）
- `frr`（只用 `zebra` + `bgpd`，`/etc/frr/daemons` 里其余全关；`vtysh` 用于排查）

装完要把系统自带的服务全部 disable：

```sh
systemctl disable --now strongswan-starter ipsec strongswan frr 2>/dev/null || true
```

理由和 haproxy 那条一样（`deploy-compute-node.sh:238`）：进程由脚本在 netns 内按实例启动，系统自带的服务会占用 `/etc/swanctl`、`/var/run/frr` 和 500/4500 端口。**FRR 尤其要注意**：它默认跑在宿主机的 default netns 里，会去动宿主机的路由表。

### 4.2 新增脚本

| 脚本 | 作用 |
|---|---|
| `create_vpn_gateway.sh` | 建两个目录、公网口（调 `create_veth.sh`、`create_lb_floating.sh`）、写 `strongswan.conf`、**写 `vrrp-<id>/keepalived.conf`**、放行 UDP 500/4500/51820、设 rp_filter / send_redirects / MSS 钳制规则 |
| `create_vpn_ipsec_conf.sh` | 从 stdin 读整份连接配置的 JSON，生成 `swanctl.conf`，持锁重载（`swanctl --load-all`）或启动 charon |
| `create_vpn_wg_conf.sh` | 从 stdin 读客户端列表 JSON，生成 `wg.conf`，`wg syncconf` 增量应用（不会断开未变动的 peer） |
| `create_vpn_bgp_conf.sh` | 从 stdin 读 BGP 连接配置的 JSON，生成 `frr/frr.conf`（含 prefix-list / route-map，见 §4.3.1）；只下发到主备两个节点，持 VIP 的那台启动 / 重载 `zebra` + `bgpd` |
| `set_vpn_route.sh` | 在本节点 `router-N` 上装/删对端网段的路由与 `nonat` 条目；带 `local` 参数时装隧道路由，否则装指向某个 VRRP 地址的路由 |
| `vpn_notify.sh` | keepalived 的 `notify_master` / `notify_backup` / `notify_fault`，改写本地路由、启停 charon 与 `zebra`/`bgpd`、发起连接 |
| `check_vpn_process.sh` | 心跳守护，**只管 charon 与 wg 接口**（keepalived 由 `check_lb_process.sh` 顺带守着，见 §4.1）；复用 `lb_proc_alive` 与 `flock $lb_lock_file` |
| `report_vpn_status.sh` | 心跳上报：主节点身份、每条隧道状态与流量、每个客户端的握手时间与流量 |
| `recover_vpn_gateway.sh` | 节点重启后按 `boot_id` 触发的回调（脚本只负责发回调，实际重建由 clapi 重发配置完成） |
| `clear_vpn_gateway.sh` | 删网关：停进程、删接口与路由、删 `nonat` 条目、删目录 |

**keepalived 配置不能复用 `create_keepalived_conf.sh`**：它把实例名写死成 `vrrp_instance load_balancer_${vrrp_ID}`（第 61 行）、把 notify 写死成 `notify_master $PWD/set_route_table.sh`（第 100 行附近），而且没有 `notify_backup` / `notify_fault`。VPN 的那份由 `create_vpn_gateway.sh` 自己写，除实例名和 notify 外其余照抄（`state BACKUP` + `nopreempt` + 按角色给优先级、`unicast_src_ip` / `unicast_peer`、`virtual_ipaddress` 挂在 `te-<router>-<vlan>` 上）。

notify 这样写，**把参数直接写进配置行**：

```
notify_master "/opt/cloudland/scripts/backend/vpn_notify.sh <网关ID> master"
notify_backup "/opt/cloudland/scripts/backend/vpn_notify.sh <网关ID> backup"
notify_fault  "/opt/cloudland/scripts/backend/vpn_notify.sh <网关ID> backup"
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
- `start_action = trap`（我方发起）或 `none`（仅响应）；`dpd_action` 按配置。
- PSK 放在 `secrets.ike-<name>` 段里，`id` 用 `remote_id`。
- 算法提案来自数据库字段，**必须白名单校验**（§10），不能让用户往配置文件里塞任意文本。

### 4.3.1 `frr.conf` 的生成（`bgp` 模式）

只下发到主备两个节点。骨架：

```
router bgp <local_asn>
  bgp router-id <tunnel_local_ip>
  no bgp ebgp-requires-policy
  neighbor <tunnel_peer_ip> remote-as <peer_asn>
  neighbor <tunnel_peer_ip> timers <bgp_keepalive> <bgp_hold>
  neighbor <tunnel_peer_ip> password <bgp_password>          # 可选 TCP-MD5
  address-family ipv4 unicast
    network <local_cidrs 逐条>                                # 通告本端 VPC 子网
    neighbor <tunnel_peer_ip> route-map FROM-PEER-IN in
    neighbor <tunnel_peer_ip> maximum-prefix <max_prefixes> warning-only
  exit-address-family
!
ip prefix-list REMOTE seq 5 permit <remote_summary_cidrs 逐条> le 32
route-map FROM-PEER-IN permit 10
  match ip address prefix-list REMOTE
```

⚠️ **入方向过滤不是可选项**（§2.6）。没有它，对端误通告 `0.0.0.0/0` 会把整个 VPC 的出网流量吸进隧道，误通告本 VPC 的网段会让同 VPC 虚拟机互访黑洞——两种情况下 BGP 邻居都显示一切正常。`REMOTE` 这个 prefix-list 的值**就是用户填的汇总网段**，同一个输入。

几个容易写错的地方：

- **`no bgp ebgp-requires-policy`**：FRR 7.4 起默认要求 eBGP 邻居必须配 in/out policy，否则**一条路由都不收发**。我们有 in 没有 out（`network` 语句直接通告），所以要关掉这个开关，或者也给 out 配一个 route-map。
- **`bgp router-id` 必须显式给**：netns 里可能没有合适的接口地址供 FRR 自动选，选错了会和对端的 router-id 冲突。
- **`network` 语句要求本端路由已在内核路由表里**，VPC 子网的网关口（`ns-<vni>`）建好之后才成立——所以 FRR 要在 §5.1 阶段二、网关配置下发完成之后再启动。
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
# 只写 tunnel_routes，不装路由：装哪条由角色决定，归 vpn_notify.sh
echo "<client_cidr> wg-$gwID" >>$vpn_dir/tunnel_routes
```

⚠️ **这里绝对不能顺手 `ip route replace <client_cidr> dev wg-$gwID`**。备节点上也会跑这个脚本（配置是两端同步下发的），一旦装上这条路由，备节点就把客户端池的流量吸进了一个没有任何握手的本地 wg 接口——包直接被丢掉，而且现象是「切换回来之后好一阵子不通、手工重启下进程又好了」，非常难排查。路由只有 `vpn_notify.sh` 一个归属（§2.3）。

注意用 `syncconf` 而不是 `setconf`：加一个客户端时后者会把所有 peer 的会话状态清掉，所有人断一次。

### 4.5 进程守护与恢复

- `check_vpn_process.sh`：只在本节点持有浮动 IP 时检查 charon 和 `zebra` / `bgpd`（判断方法同 `report_lb_health.sh`：看 `ip -4 -o addr` 里有没有那个 VIP）；WireGuard 只检查接口在不在、peer 数对不对。**`zebra` 要在 `bgpd` 之前起**，否则 bgpd 连不上 zapi、学到的路由装不进内核，而且它不会退出，现象是「BGP 邻居 up、`show ip bgp` 有路由、`ip route` 里没有」。**keepalived 不归它管**（§4.1，由 `check_lb_process.sh` 的 glob 顺带守着）。和配置脚本用同一个 `flock $lb_lock_file` 互斥（复用现有锁，避免再引入一把），**持锁时启动的进程必须带 `9>&-`**，否则守护进程继承描述符、锁永远不释放（LB 那次实测无锁时 15 个并发起出 15 个 haproxy）。
- 顺带校一次路由：拿 `tunnel_routes` 和当前角色（本节点有没有 VIP）比对 `ip route`，不一致就按 `vpn_notify.sh` 的规则纠正。这是给「keepalived 被守护脚本拉起、但 notify 没被触发过」和「接口刚建好、keepalived 还没选出主」这两个窗口兜底的，不然路由会一直是空的。
- `recover_vpn_gateway`：和 `recover_loadbalancer` 一样按 `boot_id` 触发，回调 clapi；clapi 把该节点上所有 VPN 网关的完整配置重发一遍（网关 → 路由 → 连接 → 客户端）。**顺序必须是先 `set_vrrp_ip.sh` 再 `sendFdbRules`**，理由同 `recover_loadbalancer.go:26` 的注释：反过来的话 VRRP 网卡会被按网关规则的 MAC 先建出来。

---

## 5. 各操作流程

### 5.1 创建网关

**这是一个两阶段流程，不能一口气下发完**。原因在 [rpcs/set_vrrp_ip.go](../../../api/src/rpcs/set_vrrp_ip.go)：`CreateVrrpInstance` 只用 `select=` 下发**一次** `set_vrrp_ip.sh`（cland 挑一个节点做 MASTER），**BACKUP 那次是在 MASTER 的回调里才下发的**，回调里再把 `hyper` / `peer` 写回库。所以创建请求返回时，两个 `Interface` 的 `Hyper` 仍是 -1，这时候拿 `inter=` 下发只会得到 `no target node`（2026-09-16 之后不再静默丢弃）。

- **阶段一（创建请求内）**
  1. `POST /vpn_gateways`，参数：`name`、`vpc`、`zone`（可选）、`public_subnet`（可选，不填按浮动 IP 的默认选法）、`ipsec_enabled`、`client_enabled`、`client_cidr` 等。
  2. clapi 校验（清单见 §10）。
  3. 事务内：`CreateVrrpInstance`（复用 `services/loadbalancer.go:113`）、建 `vpn_gateways` 记录（`pending`）、生成网关自己的 WireGuard 密钥对（`client_enabled` 时）。
  4. 申请浮动 IP（走 `floatingIpAdmin`，`VpnGatewayID` 指向网关）。接口返回 `pending`。

- **阶段二（`set_vrrp_ip` 回调，两端都到齐之后）**
  5. **扩展 `rpcs/set_vrrp_ip.go`**：现在的 `UpdateLoadBalancerStatus` 写死只更新 `load_balancers`，要按同一个 `vrrp_instance_id` 一并更新 `vpn_gateways` 的状态（改名成 `updateVrrpOwnerStatus` 之类，两张表各更一次）。
  6. BACKUP 的回调到达（`peer` 落库）时，才下发 `create_vpn_gateway.sh` 到主备两个节点，随后依次下发 keepalived 配置、站点连接、客户端、回程路由。
  7. 节点回调 `vpn_gateway.sh '<id>' '<hostid>' 'ready'` → 状态置 `available`。

**永远按「主备两个节点」下发，不要按「当前主节点」下发**。`master_hyper` 只用于决定其余节点的 nexthop（§2.3 第 4 点），不作为 `inter=` 的来源——它在网关刚建好时是 -1，直接拿去下发就是 `inter=-1`。像「重建隧道」这种只对主节点有意义的命令也发给两端，备节点上 charon 没跑，脚本自己判断后静默退出即可。

**没有浮动 IP 的网关等于没开**：和 LB 一样，keepalived 配置里没有 `virtual_ipaddress` 就没有意义。所以第一版**创建网关时必须带公网 IP**，不做「先建后绑」。

### 5.2 建站点连接

1. `POST /vpn_gateways/:id/connections`。
2. 校验：`remote_cidrs` 不与 VPC 内子网、客户端池、其他连接的对端网段重叠；算法提案在白名单里；`remote_gateway` 是合法的公网 IPv4；`initiator=true` 时 `remote_gateway` 必填。
3. 分配 `if_id`，落库（`pending`）。
4. 下发（主备两个节点）：`create_vpn_ipsec_conf.sh`（整份配置重生成 + 重载）。
5. 下发对端网段的路由与 `nonat`：主备两节点用 `set_vpn_route.sh <网关ID> <网段> local`，其余承载该 VPC 的节点用 `set_vpn_route.sh <网关ID> <网段> via <主节点 VRRP 地址>`。
6. 状态由心跳上报（`down` → `up`）。

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
- 删网关：先要求删光连接和客户端（或者在接口里级联删，但要在界面上明确列出将被删除的内容），再下发 `clear_vpn_gateway.sh` 到两个节点和其余承载该 VPC 的节点（清路由和 `nonat`），释放浮动 IP，删 `VrrpInstance`。

### 5.5 网关跟着 VPC 走

- **虚拟机迁移**：网关不受影响（隧道在路由器 netns 里，和虚拟机在哪台机器无关）。但迁入的目标节点如果之前没有承载过该 VPC，它的 `router-N` 是新建的，**没有 VPN 路由和 `nonat` 条目**。所以 `migrate_vm.go` 的 `completed` 回调里要补发一次该 VPC 的 VPN 路由（和 `prewarmTargetFdb` 一样的位置）。这是很容易漏掉的一点。
- **VPC 里新建虚拟机**落到一台没承载过该 VPC 的节点时，同样要补发。挂在 `launch_vm` 的回调里，紧接 `sendFdbRules`。
- **删除 VPC**：先要求删掉 VPN 网关。

---

## 6. 状态上报与客户端 DNS

### 6.1 状态上报

`report_vpn_status.sh`，只由持有浮动 IP 的节点上报，**内容变化或距上次满 5 分钟**才回调（完全照抄 `report_lb_health.sh` 的节流做法）。上报三类：

1. **主节点身份**：`vpn_master.sh '<网关ID>' '<hostid>'`。clapi 发现变了就重发其余节点的回程路由（§2.3 第 4 点）。
2. **隧道状态**：`timeout 10 swanctl --uri unix://<sock> --list-sas` 解析出每条连接的 `ESTABLISHED` / 字节数 / 建立时间，回调 `vpn_conn_status.sh`。
3. **客户端状态**：`wg show wg-<id> dump` 的握手时间和字节数，回调 `vpn_client_status.sh`。
4. **BGP 邻居、学到的前缀、以及被拒绝的前缀**：`timeout 10 vtysh --vty_socket <frr 目录> -c 'show bgp ipv4 unicast json'` 取已接受的，`... neighbors <peer> filtered-routes json` 取被入方向过滤拒掉的，回调 `vpn_bgp_status.sh`。**这份数据只用来在界面上展示**，不参与任何下发（§2.6、§3.4）——写进库的是一张只读快照，clapi 不能拿它去改路由。前缀多的时候要限量（比如各只报前 200 条 + 总数），别把心跳撑爆。

   **被拒绝的前缀是产品上最有价值的一项**：它直接告诉用户「汇总网段填小了」，界面上要在连接详情页显著提示（§2.6 的「填太小」）。

clapi 侧这三个 handler 必须幂等（回调会重试 3 次）。上报的 `hostid` 要和网关当前的主节点一致才接受，避免旧主节点的迟到回调把状态改回去。

### 6.2 客户端 DNS：默认不下发

一个很自然但**多半行不通**的想法是把 VPC 内部子网的网关地址（比如 `192.168.62.1`）作为 DNS 推给客户端——那里确实有 dnsmasq 在跑。问题是 [set_subnet_dhcp.sh:41,74](../../../scripts/kvm/set_subnet_dhcp.sh#L74) 用的是 `bind-dynamic` + `--interface=ns-<vlan>` 启动的（每个 VNI 一个实例，不这么做第一个实例会占掉 `0.0.0.0:53`，后面的全起不来）。`bind-dynamic` 会按**收包接口**过滤，从 `wg-<id>` 进来的查询大概率直接被丢弃。iptables 不是障碍（客户端池在 `nonat` 里，INPUT 放行），障碍是 dnsmasq 自己。

所以：

- **V2 默认 `client_dns` 留空**，客户端用自己的 DNS，只是访问不了 VPC 内部域名。
- 想支持内部域名解析，两条路，都放到 V3 再定：① 给 VPN 单独起一个 dnsmasq，监听 wg 接口上的一个地址，上游指向控制面 DNS，同时加载各子网的 `dhcp-hostsfile`；② 改 `set_subnet_dhcp.sh` 在 `--interface` 后面追加 `wg-*`——但那个脚本是所有子网共用的、改动面大，而且 dnsmasq 配置一变就要重启，会牵动 DHCP，不划算。倾向 ①。
- 这条先实测再定（§13 待验证第 1 条）：也许 `bind-dynamic` 在「目的地址是 `ns-<vlan>` 上的地址、但从别的接口进来」这种情况下是放行的，那就什么都不用做。

---

## 7. 接口

### 7.1 clapi

```
GET    /vpn_gateways                                 列表（offset/limit/query/order）
POST   /vpn_gateways                                 创建
GET    /vpn_gateways/:id                             详情
PATCH  /vpn_gateways/:id                             改名 / 描述 / client_dns / client_routes / 开关
DELETE /vpn_gateways/:id                             删除

GET    /vpn_gateways/:id/connections                 站点连接列表
POST   /vpn_gateways/:id/connections                 创建
GET    /vpn_gateways/:id/connections/:conn_id        详情
PATCH  /vpn_gateways/:id/connections/:conn_id        改配置（改动才重新下发）
DELETE /vpn_gateways/:id/connections/:conn_id        删除
POST   /vpn_gateways/:id/connections/:conn_id/restart  手工重建隧道（swanctl --terminate + --initiate）

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

---

## 8. 高可用与故障处理小结

| 场景 | 现象 | 恢复 |
|---|---|---|
| keepalived 被杀 | 浮动 IP 漂到对端 | notify 脚本即时改本地路由、启停 charon 与 bgpd；其余节点 nexthop 指向原主，多一跳，**路由零收敛**（端到端仍要等隧道重建） |
| charon / bgpd 异常退出 | 隧道断 / 路由撤销 | 心跳守护拉起（1–20 秒），随后重协商 |
| 主节点整机宕机 | 浮动 IP 漂到对端 | 隧道与 eBGP 在新主上重建；其余节点的 nexthop 在新主第一次心跳上报后由 clapi 重发（1–20 秒）。**汇总网段不变**，只换 nexthop |
| 对端撤销一条 BGP 前缀 | 该网段不可达 | 主节点上明细路由消失，流量落到汇总的 `blackhole` 被丢掉；**其余节点什么都不用改**（§2.6） |
| 节点重启 | netns、路由、`nonat`、XFRM、wg、frr 全丢 | `recover_vpn_gateway` 按 `boot_id` 触发，clapi 重发全部配置 |
| 对端设备重启 | 隧道断 | DPD + `start_action = trap` / `dpd_action = restart` 自动重建 |
| 虚拟机迁到新节点 | 新节点的 `router-N` 缺 VPN 路由 | `migrate_vm` 的 `completed` 回调补发（§5.5） |

---

## 9. 部署

- `deploy-compute-node.sh` 和 `deploy/roles/hyper/tasks/main.yml` 的包列表加 `strongswan-swanctl`、`strongswan-charon`、`wireguard-tools`、`frr`，并 disable 系统自带的 strongswan / ipsec / frr 服务（§4.1）。
- 计算节点的 `cloudland-iptables.service` 基础规则不需要改：VPN 的公网地址在路由器 netns 的 `te-` 设备上，流量经 `br<vlan>` 桥进 netns，不走宿主机的 INPUT 链（和 LB 的浮动 IP 一样，已在 work-01 验证过这条路径）。
- **提交新脚本时记得 `git update-index --chmod=+x`**（本地 `core.filemode=false`），部署到节点时也要确认可执行位。

---

## 10. 安全

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
  - **`169.0.0.0/8`**：`create_local_router.sh:80-85` 给每个 `router-N` 到 `router-0` 的点对点链路分配的是 `169.<x>.<y>.<z>/31`，撞上就把路由器的默认路由打掉了，表现是整个 VPC 出不了网
  - 同一网关上其他站点连接的 `remote_cidrs`、客户端池
  - `0.0.0.0/0` 只允许出现在 `client_routes`（全流量模式），不允许出现在 `remote_cidrs`
- **`local_cidrs` 为空要拒绝**：VPC 还没有内部子网时默认值算出来是空集，配置文件里的 `local_ts` 会是空的，协商必然失败而且报错莫名其妙。
- 🔴 **对端能往我们这边注入多大范围的路由，是这套设计里唯一一个「对端可控」的攻击面**（`bgp` 模式特有）。BGP 模式下 IPsec 的 TS 是 `0.0.0.0/0`，内核策略不再起过滤作用，**唯一的边界就是 FRR 的入方向 prefix-list**（§4.3.1）。没有它，一个被攻陷或误配置的对端设备可以：通告 `0.0.0.0/0` 把整个 VPC 的出网流量吸进隧道（中间人）、通告本 VPC 的网段让同 VPC 虚拟机互访黑洞（拒绝服务）、灌一万条明细打爆主节点路由表。三条都不需要任何凭据，只要 BGP 邻居建起来就行。所以：**`bgp` 模式下 `remote_summary_cidrs` 必填且必须生成 prefix-list，`maximum-prefix` 必须配**，不允许「留空表示不限制」这种选项。
- **SQL**：列表接口的过滤一律参数绑定，名称搜索用 `dbs.Contains`，排序用 `dbs.NewOrders`。`dbs/sql_literal_test.go` 会扫描字符串字面量。
- **权限**：创建 / 修改 / 删除要求 `model.OrgWriter`；只读成员只能看。**不要漏掉 PATCH**（`PATCH /subnets/:id` 就漏过写权限检查）。
- **审计里不要出现凭据**：创建连接 / 客户端的请求体不进审计（现有实现只在失败时截取响应体），但要确认失败响应里不会把 PSK 回显出来。
- **客户端全流量模式**（`client_routes = 0.0.0.0/0`）会让客户端的全部上网流量经过 VPC 路由器出公网，源地址是该 VPC 的出口地址。这是用户主动选的，但界面上要说清楚，并且要确认路由器 netns 的 SNAT 对客户端池生效（客户端池加进 `nonat` 之后就**不会**被 SNAT，所以全流量模式需要单独一条 `POSTROUTING -s <client_cidr> -o te-* -j SNAT`，不能只靠默认规则）。这一点容易做错，写进 §13 待验证。

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
13. **入方向前缀过滤**（§4.3.1，安全相关，必测）：在 B 侧故意通告三种危险路由，确认 A 侧全部拒收、且被拒前缀出现在界面上——
    - `0.0.0.0/0` → 拒；A 侧虚拟机出公网不受影响（否则就是整个 VPC 的流量被劫进隧道）
    - A 自己的 VPC 网段 `192.168.62.0/24` → 拒；A 侧同 VPC 虚拟机互访不受影响
    - 超过 `max_prefixes` 条明细 → `maximum-prefix` 告警，邻居不断（`warning-only`）
14. **汇总网段填小**：把 A 侧的汇总从 `192.168.0.0/16` 改成只覆盖一半，确认 ① 被拒前缀上报并在界面提示 ② 主节点上的虚拟机能访问、其他节点的不能（这正是「填太小」的真实症状，值得亲眼看一次）。

**客户端到站点**：本机（Windows）装 WireGuard 客户端，连 `vpn-a` 的公网 IP，验证能访问三个节点上的虚拟机，虚拟机上 `tcpdump` 看到的是客户端池地址；再从虚拟机主动 ping 客户端。

**主备切换**：

- 主节点上 `kill -9` keepalived → 记录 ping 丢包数和隧道重建时间
- 主节点上 `kill -9` charon → 记录心跳拉起的时间（最坏 20 秒）
- **真实重启主节点** → 记录全流程恢复时间（LB 那次是 36 秒注册 / 42 秒心跳 / 55 秒恢复）
- 切换期间第三方节点上的虚拟机是否始终可达（这是两跳方案的关键验证点）

**回归**：跑完要确认 `rb-lb` 仍然 10/10、三节点心跳正常、虚拟机热迁移正常（VPN 路由补发那条改动碰了 `migrate_vm.go`）。

**测试后清理**：两个测试 VPC、6 台虚拟机、2 个网关、2 个公网 IP 全部删掉，确认节点上 `ip netns exec router-N ip route`、`ipset list nonat`、`ip link` 里没有残留，`/opt/cloudland/cache/router/router-*/vpn-*` 目录清零。

---

## 12. 实施阶段

**待决策已全部不阻塞 V1**（BGP 的路由方案 2026-09-23 定了，见 §13「已定」）。

**但 V1 之前先做 §13 待验证的第 1、2 条**：两条都不需要写代码，在 work-01 现有的 `router-8` 里就能验，而且都能改变设计（DNS 那条决定 V2 要不要多做一个 dnsmasq，rp_filter 那条决定两跳方案成不成立）。

### V1：共用骨架 + 站点到站点

- 数据模型：`vpn_gateways`、`vpn_connections`（含 BGP 字段）、`vpn_remote_prefixes`，`floating_ips.vpn_gateway_id`，`vrrp_instances.vrid`；唯一索引一律带 `name`、删除时改名（§3.1）
- 顺手修掉 VRID 8 位的既有缺陷（附录 B 第 1 条）
- **扩展 `rpcs/set_vrrp_ip.go`**：`UpdateLoadBalancerStatus` 现在写死只更新 `load_balancers`，要按 `vrrp_instance_id` 一并更新 `vpn_gateways`；网关的配置下发挂在 BACKUP 回调之后（§5.1）
- 节点脚本：`create_vpn_gateway.sh`、`create_vpn_ipsec_conf.sh`、`create_vpn_bgp_conf.sh`（含 prefix-list / route-map / maximum-prefix，§4.3.1）、`set_vpn_route.sh`、`vpn_notify.sh`、`check_vpn_process.sh`、`report_vpn_status.sh`、`recover_vpn_gateway.sh`、`clear_vpn_gateway.sh`
- **顺手修掉 `clear_vrrp_ip.sh` 按 MAC 删 fdb 那个既有 bug**（附录 B 第 2 条）：clapi 在下发完 `clear_vrrp_ip.sh` 之后无条件重发一次 `sendFdbRules`。不修的话，删 VPN 网关会打断同 VPC 的负载均衡（反之亦然）
- `create_local_router.sh` 加两个 sysctl：`rp_filter=2`（如果待验证第 2 条证实需要）、`send_redirects=0`
- clapi：网关与连接的 CRUD、派生集合的维护与增量下发、四个心跳回调 handler、迁移与新建虚拟机时的路由补发
- cpgateway：代理白名单
- 前端：列表 + 详情 + 站点连接标签页（`static` / `bgp` 两种模式的表单差别不小，BGP 那套要有 ASN、隧道内链路地址、汇总网段，以及邻居状态和学到的前缀的展示）
- 部署脚本加包（含 `frr`）
- 测试：§11 的站点到站点全套（静态 + BGP）+ 主备切换

**BGP 不能挪到后面做**。它决定了 §2.3 的「对端网段」来源、§4.3 的流量选择符怎么给、`vpn_connections` 的字段——先按纯静态做一版再回头加，等于把这三处推翻重来。

### V2：客户端到站点

- 数据模型：`vpn_clients`，网关上的客户端字段
- 节点脚本：`create_vpn_wg_conf.sh`，`report_vpn_status.sh` 加客户端部分
- clapi：客户端 CRUD、地址池分配与**删除时释放**、配置文本生成（私钥不入库，§3.3）
- 前端：客户端标签页；创建弹窗默认走「用户自带公钥」，选「平台代生成」时明确提示私钥只显示这一次
- 测试：§11 的客户端部分

### V3：收尾

- 配额（`vpn_gateways`）
- 审计动作映射 + 三语文案
- 隧道 down 的告警（怎么接进现有告警体系要先想清楚：节点告警和 VM 告警都是 Prometheus 规则，而隧道状态在数据库里。最省事的做法是 clapi 在状态变 `down` 时直接走通知渠道，不进 Prometheus）
- 二维码
- 资源详情页的「操作记录」标签页（和 B1 一起做）

### V4（可选，视 §13 待决策第 2 条的结论）

- IKEv2 + EAP-MSCHAPv2 的客户端接入：要引入每网关一套自签 CA、服务端证书签发与续期、CA 证书下发、EAP 用户库
- 站点连接的证书认证
- OpenVPN（TCP 443 穿透）
- **每节点 iBGP**（去掉汇总网段的要求，让明细前缀传播到每个节点）：前置是给每个承载该 VPC 的节点在 VRRP 子网里分配唯一地址并随节点加入 / 退出动态增删，再加每 VPN VPC × 每节点一个 `zebra` + `bgpd`、短定时器或 BFD。按 §2.6 的设计做，这一步是叠加而不是推翻——`vpn_remote_prefixes` 里 `connection_summary` 那类来源直接停用即可

---

## 13. 待决策与待验证

**已定**

- ✅ **BGP 用「外部 eBGP + 内部汇总网段」，不做每节点 iBGP**（2026-09-23 确认）。十个候选方案的对比与否决理由见 §2.6。**V1 没有阻塞项了，可以开工。** 如果将来产品上认为「让用户填汇总」不可接受，走 §2.6 的方案 2（clapi 中继明细）作为兜底，那是 V3 的一个增量来源，**不推翻 V1 的任何设计**。

**待决策**

1. **客户端到站点用哪个协议**（§2.1）。默认按 WireGuard 写。如果业务上必须零客户端安装，或者必须能穿 UDP 被封的网络，就要改成 IKEv2/EAP 或 OpenVPN，V2 的工作量会翻倍以上（多一套 CA）。**这条定了才能开工 V2**。
2. **一个 VPC 是不是只允许一个 VPN 网关**。第一版按一个写。放开的话要重新想 VRID 分配和多网关的回程路由（不同网关的对端网段不能重叠，否则同一条路由有两个 nexthop）。
3. **PSK 的加密密钥用什么**。本文按 `CAPTURE_UPLOAD_SECRET` 派生写。如果将来要引入真正的 KMS，现在存的密文要能重新加密——加密时带上版本前缀。
4. **站点连接要不要支持 IKEv1**。老设备（尤其是十年前的防火墙）只有 IKEv1。本文不做。

**待验证**（前两条能改变设计，要最先做，而且不需要写一行代码——在 work-01 现有的 `router-8` 里就能验）

1. **dnsmasq 能不能答从别的接口进来的查询**（§6.2）。做法：在 `router-8` 里建一个假的 wg 接口配上地址，从它发一个 `dig @192.168.62.1`，看现有的 dnsmasq 实例应不应。答了的话 `client_dns` 就能默认填内部网关，V2 少一整块工作；不答就按 §6.2 的方案 ① 排到 V3。
2. **netns 里的 rp_filter 默认值**（§2.3）。实测新建 netns 的 `net.ipv4.conf.all.rp_filter` / `.default.rp_filter`（注意：netns 不继承宿主机的当前值，要在**新建的** netns 里读）。如果是 strict，两跳路径会被静默丢包，必须在 `create_local_router.sh` 里设成 2。
3. **全流量模式的 SNAT**（§10 最后一条）。客户端池加进 `nonat` 之后默认 SNAT 规则就不生效了，需要单独加一条，且不能影响隧道内到 VPC 的流量。
4. **strongSwan 在 26.04 上的版本和包名**。24.04 是 5.9.x，26.04 可能是 6.x，`swanctl.conf` 的语法在两个大版本间基本兼容，但 `strongswan.conf` 的插件路径可能不同。
5. **多个 charon 实例的资源占用**。一个 VPC 一个 charon，几十个 VPC 就是几十个进程。实测单实例常驻内存，确认不会把节点压垮；超过某个数量要考虑改成单 charon 多 netns（不可行，XFRM 隔离在 netns）或者限制每节点的网关数。
6. **主备切换后隧道重建的实际时间**（§2.4）。文中写的 5–10 秒是估计值，实测后回填。
7. **`wg syncconf` 在加减 peer 时是否真的不影响其他 peer 的会话**。文档是这么说的，实测确认。
8. **对端设备互通**。手上只有两个 VPC 互联这一种测法，和真实的 Cisco / 华为 / 飞塔互通没有验证过。上线前至少要和一台真实设备对一次，或者和一个公有云的 VPN 网关对一次。**BGP 模式尤其要对**：TS 给 `0.0.0.0/0`、BGP 跑在隧道内链路地址上，这两条都要对端按路由模式配，配置差异比静态模式大得多。
9. **FRR 在 netns 里跑的可行性与资源占用**。`zebra` + `bgpd` 各起一份、`--vty_socket` 和 pid 指到网关自己的目录、不碰 `/var/run/frr`——这套在 netns 里没实测过。另外 FRR 9 之后引入了 `mgmtd`，要确认只跑 `zebra` + `bgpd` 是否完整可用。
10. **`vtysh` 的 JSON 输出格式**在 FRR 8 / 9 之间有过变化，上报解析要按节点上的实际版本写。

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
| 放行端口 | 在 `create_keepalived_conf.sh` 里，只放 TCP | 在 `create_vpn_gateway.sh` 里，放 UDP 500 / 4500 / 51820（**不是同一个脚本**） |
| keepalived 配置 | `create_keepalived_conf.sh` | `create_vpn_gateway.sh` 自己写（实例名与 notify 不同，§4.2） |

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
| 7 | `client_dns` 默认取子网网关 | dnsmasq 是 `bind-dynamic` + `--interface=ns-<vlan>`，从 wg 接口进来的查询大概率被过滤 | 默认留空，实测后再定（§6.2，待验证第 1 条） |
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
