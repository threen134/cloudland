# VPC 互联：分布式 Transit Gateway 执行计划

> 状态：**已实施 T1–T4，2026-10-01 已部署 work-01 / 02 / 03 并在三节点实测**（2026-09-22 初稿；2026-10-01 复查修订后实施，实施记录与偏差见 §9，修订见 §8）
> 范围：同一区域、同一组织内多个 VPC 经 Transit Gateway（下称 TGW）互通，支持路由表隔离与前缀过滤

## 0. 摘要

- **分布式**：每台相关节点上各有一个 `tgw-<ID>` netns，流量在源虚拟机所在节点完成跨 VPC 转发，不绕集中节点，与现有 anycast VPC 路由器的风格一致
- **数据路径**：A 的虚拟机 → 本节点 `router-A` → 本节点 `tgw-T` → 本节点 `router-B` → `ns-<B vni>` → VXLAN → 对端节点 B 的虚拟机；回程在对端节点对称走一遍
- **改动集中在三处，TGW netns 本身反而简单**：
  1. 转发条目的下发范围：`common/fdb.go` 等处按 `router_id`（单个 VPC）划定，要扩大到「同一 TGW 下所有 VPC」
  2. 路由器回收：`clear_local_router.sh` / `clear_link.sh` 要把 TGW 挂载算作使用者，否则只为中转而建的路由器会被拆掉（§2.6）
  3. 与同 VPC 的 VPN 网关共存：TGW 路由单独一张表且优先、`nonat` 各自记账、网段互相校验、VPN 流量不经 TGW 中转（§2.2、§2.5）
- **节点侧用全量期望状态对账**：新增 `apply_tgw.sh`，每次接收某个 TGW 的完整期望状态（挂载、路由、黑洞、nonat）并幂等对齐，带代次号丢弃乱序的旧状态；挂载、改路由、节点重启恢复、迁移都走这一个入口
- **前缀过滤在路由层做，无状态**：每个挂载关联一张 TGW 路由表，表里的条目 = 被传播进来的子网网段（按前缀列表过滤）+ 静态路由 / 黑洞。所有节点下发同一份；有状态的访问控制交给安全组
- 阶段：T1 基础互通（单路由表、全互通）→ T2 路由表与隔离 → T3 前缀过滤 → T4 前端与文档

## 1. 背景与目标

### 1.1 现状（2026-10-01 核对）

- 每个 VPC 在节点上是一个 `router-N` netns（`scripts/kvm/create_local_router.sh`），经 `/31` veth（`int-N` ↔ `ti-N`，地址 `169.<a>.<b>.x`，`a`、`b` 都不超过 253）接到 `router-0`，默认路由指向 `router-0`；内网段在 `nonat` ipset 里，出内网的流量 SNAT 为 169.x 地址后交给 `router-0` 出公网。`ti-N` 上按包限速（`system_packet_rate_limit`，默认每秒 120 个包），只针对这条出公网的路径
- `router-0` 上没有任何到其他 VPC 内网段的路由，**VPC 之间内网不通**，只能经浮动 IP 绕公网
- 节点之间的 VXLAN **不泛洪**（无 `00:00:00:00:00:00` 条目），跨节点可达完全依赖 `add_fwrule.sh` 写入的静态 fdb 与 `ip neigh`。下发范围由 `api/src/common/fdb.go` 的 `SendFdbRules` 决定（`rpcs/launch_vm.go` 的 `sendFdbRules` 只是包装），按 `router_id` 查同 VPC 的其他网卡
- **路由器回收**：`clear_local_router.sh`（2026-09-30 重写）在虚拟机删除或迁出（`clear_vm.sh`、`finish_source_migration.sh`、`async_job/clear_target_migration.sh`）、VRRP 网卡清理（`clear_vrrp_ip.sh`）、删除 VPC（`toall=`）时调用，按「使用者」决定拆不拆：LB / VPN 网关（`bridge_lib.sh` 的 `router_vrrp_user`）、网桥上的 tap、引用该网桥的虚拟机定义、迁入中的网卡，一个都没有就拆。删除一台从未落地的虚拟机时 `clear_vm` 是 `toall=`，**每个节点**都会对它的路由器跑一遍
- `routers.peer` 是 VPC 路由器的主备节点，与本功能无关
- VPC 没有 VPC 级网段，网段挂在各子网上；有 LB 或 VPN 网关的 VPC 有一个 VRRP 子网，网段**固定为 `192.168.196.0/24`、各 VPC 相同**（`services/loadbalancer.go`，`services/vpn_prefix.go` 的 `vrrpSubnetCidr`）
- 同一个路由器 netns 里可能还有 VPN 网关：charon / FRR / WireGuard；`set_vpn_route.sh` 往**主表**装对端网段的路由、往 `nonat` 加对端网段，并在 `vpn-<网关>/nonat.current` 记录自己加了哪些；FRR 的 zebra 也管主表；BGP 隧道内地址允许落在 `169.254.0.0/16`（公有云 VPN 要求，`vpn_prefix.go` 的 `validateTunnelLink`）
- 安全组：VPC 的默认安全组对 `0.0.0.0/0` 放行 ICMP、22/3389、DHCP，**TCP/UDP 只放行本 VPC 的各子网**（建子网时 `services/subnet.go` 往默认组里追加该子网的 tcp/udp 规则）

### 1.2 目标

- 一个 TGW 可以挂多个 VPC，默认全互通
- 可以用多张 TGW 路由表做隔离（如 hub-spoke）
- 可以按子网前缀过滤传播，并支持静态路由与黑洞
- 挂有浮动 IP 的虚拟机、跨节点、迁移、节点重启后都能正常互通
- 与同 VPC 的负载均衡、VPN 网关共存，互不影响
- 安全组可以直接按对方 VPC 的网段放行（互访不做 SNAT，保留源地址）

### 1.3 非目标（v1 不做）

- 跨组织共享 TGW、跨区域互联
- 一个 VPC 同时挂多个 TGW（v1 限定一个）
- TGW 上的有状态防火墙（分布式路径不对称，做不了；需要时用安全组）
- **TGW 与 VPN 之间的中转**：VPN 的对端网段、客户端地址池不传播进 TGW，VPN 流量也不经 TGW 转发（显式 DROP，见 §2.2）。同一个 VPC 同时有 VPN 网关和 TGW 挂载是支持的
- 公网、经典 VLAN 子网（public / private 类型）不参与
- LB 后端跨 VPC（haproxy 在 `router-A` 里以 VRRP 地址访问后端，后端在别的 VPC 时的回程与 VRRP 子网冲突问题另议）
- 按挂载限速（见 §2.2 的「限速」）
- 配额（先不计费，T4 再评估是否加 `max_transit_gateways`）

## 2. 设计要点

### 2.1 节点上的拓扑

```
            node1
  ┌──────────────────────────────────────────┐
  │ router-A ──tr-<attA>══ta-<attA>──┐        │
  │   │ns-<vniA>                      tgw-T   │
  │ router-B ──tr-<attB>══ta-<attB>──┘        │
  │   │ns-<vniB> ──br<vniB>──v-<vniB>──VXLAN──┼──▶ node2 上 B 的虚拟机
  └──────────────────────────────────────────┘
```

- `tgw-<T>`：每台相关节点一个 netns，`ip_forward=1`，**`rp_filter=0`**（all、default 与每个 `ta-` 都设）：它的路由只能经「`iif ta-<att>` 选表」的规则查到，而反向路径查找不带入接口，连 loose 模式也会把每个包丢掉（实施时改，见 §9）。VPC 路由器保持 `create_local_router.sh` 设的 2：对端网段在表 252 里，`pref 100` 的规则没有入接口条件，反向路径查找看得到
- 每个挂载一对 veth：VPC 路由器侧 `tr-<挂载ID>`，TGW 侧 `ta-<挂载ID>`（名字长度在 15 字符以内）。直接 `ip link add tr-X netns router-N type veth peer name ta-X netns tgw-T` 创建，**不用 `create_veth.sh`**：它把一端挂到宿主机网桥上，而且只设了 netns 那一端的 MTU。**两端 MTU 都设 1450**，与路由器其他口一致（VXLAN 开销）
- 地址：从 **`169.254.254.0/24`** 按挂载在本 TGW 内的槽位（`tgw_attachments.slot`，0–127）取 `/31`，TGW 侧 `169.254.254.(2×slot)`，路由器侧 `169.254.254.(2×slot+1)`。同一挂载在所有节点上用同一对地址（netns 隔离，不冲突），便于排障。选这一段的原因：
  - `int-N`/`ti-N` 的地址是 `169.<a>.<b>.x`，`a` 不超过 253，永远落不到 `169.254.x.x`
  - 不在 `169.254.169.0/24` 里（各网桥上有 `169.254.169.254/32` 占位）
  - VPN 的 BGP 隧道内地址也在 `169.254.0.0/16` 里，会和 `tr-` 在同一个路由器 netns 里并存，所以 `validateTunnelLink` 要拒绝这一段，挂载时也要检查成员 VPC 已有的隧道地址（§2.5）
- 按 TGW 内槽位而不是挂载 ID 取地址：挂载 ID 全局自增，删了再建会把一个 `/24` 用完；而每个路由器只挂一个 TGW，`tgw-T` 里只有自己的挂载，地址只需在 TGW 内唯一

### 2.2 路由

- **VPC 路由器（`router-A`）**：
  - TGW 的路由**全部放在单独的路由表 `tgw`**（表号固定 252，在 `/etc/iproute2/rt_tables.d/cloudland.conf` 登记；`ensure_fip_table` 只用 2–251，不会撞上），再加**一条**规则 `ip rule add pref 100 lookup tgw`。表里：
    - 关联 TGW 路由表里的每个前缀 → `via <ta 地址> dev tr-<attA>`
    - TGW 内其他成员的前缀、但不在关联表里的 → `blackhole`（T2）
  - 一条规则就够：表里没有匹配时内核继续查下一条规则，A 自己的网段、公网、VPN 对端都不在 `tgw` 表里，照常落到后面的 fip 规则与主表；`blackhole` 是匹配后丢弃，不会落到默认路由被 SNAT 发往公网
  - **不能放进主表**，原因有二：
    - 浮动 IP 的 `ip rule from <内网 IP> lookup fip-<vlan>`（`create_floating.sh`；实施前不带优先级，内核从 32765 往下分配，现在固定 `pref 32000`）和 LB 的 fip 规则都先于主表命中，而 fip 表里只有公网默认路由和建表时已有的本 VPC 子网，跨 VPC 的流量会从公网口发出
    - 同 VPC 的 VPN 网关的 FRR zebra 管主表，和它的路由混在一起没法对账
    - `pref 100` 排在上面这些规则之前
  - ⚠️ **浮动 IP 的规则必须带显式优先级**（实施时发现，见 §9）：内核给不带 `pref` 的 `ip rule add` 分配「第一条非 local 规则的优先级减一」。`pref 100` 存在之后，`create_floating.sh`、`create_lb_floating.sh` 新加的规则会被分到 99，排到 TGW 规则前面，把跨 VPC 流量拐进浮动 IP 表。两个脚本改为 `pref 32000`（cloudrc 的 `fip_rule_pref`）
  - 优先级的含义：成员 VPC 的网段在 `router-A` 里**优先于** VPN 的路由（含 BGP 学到的）。静态配置的冲突在校验时拦下（§2.5）；BGP 动态学到的冲突拦不住，以 TGW 为准
  - **nonat**：可达前缀加入 `router-A` 的 `nonat`。**这一步是必需的**：路由器的 SNAT（`-m set --match-set nonat src -m set ! --match-set nonat dst`）和浮动 IP 的 SNAT（`-s <内网 IP> -m set ! --match-set nonat dst -j SNAT --to-source <浮动 IP>`）都按 `nonat` 判断，漏加就会把源地址改成 169.x 或浮动 IP，回程断掉。`nonat` 有十几个脚本在写，TGW 在 `$router_dir/router-N/tgw.nonat` 记录自己加了哪些，只删「自己加过、现在不再需要」的（同 VPN 的 `nonat.current`）
  - **防泄漏与隔离**，插在 FORWARD 最前面，对账时每次补齐：
    - `-i tr+ -o ti-<N>`、`-i ti-<N> -o tr+`、`-i tr+ -o te+`、`-i te+ -o tr+` → DROP。从 TGW 进来的流量在本路由器找不到路由时（比如目的子网在本节点还没建网关口），不会走默认路由被 SNAT 发往公网；公网侧也不能借这个路由器直达别的 VPC
    - `-i wg+ -o tr+`、`-i ipsec+ -o tr+` 及反方向 → DROP。VPN 流量不经 TGW 中转（非目标；而且对端 VPC 没有回程路由，本来就是单向的）
  - 路由器 INPUT 对 `nonat` 里的源地址放行（`-m set --match-set nonat src -j ACCEPT`），所以别的成员 VPC 的虚拟机能访问本路由器 netns 里的服务（网关地址、DHCP 的 dnsmasq），与同 VPC 内一致，可以接受
- **TGW（`tgw-T`）**：每张 TGW 路由表对应一张内核路由表，表号 `1000 + 路由表槽位`（`tgw-T` 私有，不登记名字）；`ip rule add iif ta-<att> lookup <表号>` 按入口选表。表里是「前缀 → `via <tr 地址> dev ta-<目标挂载>`」或 `blackhole`，每张表末尾一条 `blackhole default`；`tgw-T` 的主表除直连的 `/31` 外不放任何路由
- **限速**：TGW 流量走 `tr-`/`ta-`，不经 `ti-N`，**不受 `system_packet_rate_limit` 限制**。这是有意的：那个限速是给出公网的 SNAT 流量设的，TGW 是东西向流量，与 VPC 内跨子网互访一样不限速。需要按挂载限速时另议
- **conntrack 与安全组**：路径不对称（A→B 在源节点经 `router-A`、`tgw-T`、`router-B`，回程在对端节点），路由器 netns 里的 conntrack 只看得到单向。路由器里没有按连接状态过滤的 FORWARD 规则，NAT 也因 `nonat` 跳过，所以不受影响。宿主机上的安全组在虚拟机所在节点看得到完整的双向流（同跨节点的同 VPC 跨子网互访），有状态规则照常生效
- **有效路由的计算**（clapi 侧，所有节点共用一份结果）：
  1. 传播：挂载的每个 internal 子网网段，传播到它传播目标的路由表中，按该传播的前缀列表过滤（T3）
  2. 静态路由：用户写的「前缀 → 挂载」或「前缀 → 黑洞」
  3. 同一前缀静态优先于传播；不同前缀按最长前缀匹配由内核处理
  4. VRRP 子网（`type=vrrp`）**不传播**；VPN 的对端网段、客户端地址池**不传播**

### 2.3 转发条目的下发范围

定义 **S(T)** = 挂在 TGW T 上的任一 VPC 有网卡（`type NOT IN ('gateway', 'vrrp')`、`hyper >= 0`）所在的节点集合。VRRP 网卡不算：LB 与 VPN 网关的 VRRP 网卡只服务本 VPC，跨 VPC 访问 LB 后端、VPN 中转都是非目标。

- 抽一个 `routerScope(ctx, routerID) []int64`：没挂 TGW（或挂载已删除）时返回 `[routerID]`，挂了返回全部成员。下面的查询统一走它，**只扩大普通网卡的范围，VRRP 网卡的条目仍只在本 VPC 内下发**
- 要改的查询（2026-10-01 核对的清单，T1 开工前再全量 grep `router_id = ?` 与 `RouterID` 核对一遍）：
  - `common/fdb.go:33`：新网卡的条目扩散给哪些节点
  - `common/fdb.go:93`：本节点要收哪些其他网卡的条目（新节点收到 T 下所有成员网卡的条目）
  - `rpcs/clear_vm.go:53`、`:64`：删除网卡时 `del_fwrule.sh` 的节点列表
  - `rpcs/migrate_vm.go:158`：`prewarmTargetFdb`
  - `rpcs/attach_vm_nic.go`（经 `sendFdbRules`）、`rpcs/detach_vm_nic.go`：加网卡、卸网卡
- 不改（只服务本 VPC 的 VRRP 网卡）：`common/fdb.go:161` 的 `ResyncRouterVrrpFdb`、`rpcs/set_vrrp_ip.go`、`rpcs/recover_loadbalancer.go`、`services/vpn.go` 里给 VRRP 网卡补发的那条，以及 `SendFdbRules` 的 `vrrpInstance` 分支
- **节点进出 S(T)**：clapi 在网卡落到或离开某节点的回调里判断——`launch_vm`、`clear_vm`、迁移的 `target_prepared` 与 `completed`、加网卡、卸网卡。新进入的节点调 `TgwResyncNode`（§2.4）；不再有任何成员网卡的节点下发空期望状态
- **挂载时交换已有的转发条目**（实施时补，见 §9）：`SendFdbRules` 只在网卡落地时运行，挂载前已在运行的云服务器的条目不会自己扩散。挂载（和 `resync`）后 `tgwSyncFdb` 给 S(T) 每台节点发一次「所有成员云服务器网卡中、不在本节点的那些」的条目，`add_fwrule.sh` 顺带建出路由器、网关口和 VXLAN 口
- 节点收到 B 的条目时，`add_fwrule.sh` → `set_subnet_gw.sh` → `create_local_router.sh` 会建出 `router-B`、`ns-<vni>`、`br<vni>`/`v-<vni>`。`apply_tgw.sh` 不依赖这个顺序（§2.4）
- 规模：T 下 VPC 越多，S(T) 中每台节点的 netns、VXLAN 设备和条目数越多。v1 限制每个 TGW 最多 10 个挂载，T1 结束时实测条目数与下发耗时

### 2.4 节点侧：`apply_tgw.sh`（全量对账）

- **输入**：参数 `<tgw_id> <generation>`，期望状态是 JSON，经加引号的 heredoc（`<<'EOF'`）从 stdin 传入。命令经环境变量传给执行器，上限约 128 KiB，10 个挂载的 JSON 远小于此。内容：挂载列表（`att_id`、`router_id`、`slot`、两端地址、关联表号）、每张表的路由、每个成员路由器要装的可达前缀与黑洞前缀。JSON 字段按 VPN 脚本的约定逐个读（`vpn_json_read` 的写法，jq `@sh` 单独赋值），不要用 `read -d` 一次拆多个字段——空字段会让后面的变量整体错位
- **行为**：不存在就建，多余的删掉，已一致的不动；幂等，可重复执行
- **代次号**：`transit_gateways.generation` 在每次影响期望状态的变更里与变更同一事务 +1，随命令下发（节点离开 S(T) 也算一次变更，见 §9.5）。节点在 `$cache_dir/tgw/<T>/generation` 记**收到过的最大代次**，一拿到状态就记下，**收到更小的就丢弃**（异步下发加上 cland 回调重试会乱序；先记下是为了一次半途失败的新状态不会被晚到的旧状态覆盖），相等的照常执行（节点重启后要重建）；`state.json` 只在应用成功后才写
- **成员路由器不在就自己建**（调 `create_local_router.sh`），不依赖 `add_fwrule.sh` 先到。子网网关口仍由 fdb 条目带来；还没到的窗口里转发不到，但有 §2.2 的 DROP 兜底，不会泄漏到公网
- **路由器锁**：在 cloudrc 加节点级的路由器锁 `router_lock_file <N>`（`$run_dir/lock/router-<N>.lock`），`create_local_router.sh`、`clear_local_router.sh`、`apply_tgw.sh` 在改路由器结构之前都要拿。否则 `clear_local_router.sh` 判断「没人用」与真正拆除之间，`apply_tgw.sh` 刚插上的 `tr-` 会被一起拆掉；两个脚本同时建同一个路由器也会出错（`create_local_router.sh` 的「已存在就退出」不是原子的）
  - `apply_tgw.sh` 按路由器编号升序拿所有成员路由器的锁，避免死锁；另拿 `$run_dir/tgw-<T>.lock`
  - VPN、浮动 IP、LB 的脚本改的是别的对象（主表、fip 表与规则、`nonat` 里各自的条目；`ipset add/del` 单条是原子的），不需要拿这把锁
  - `apply_tgw.sh` 不启动常驻进程；以后要在持锁时启动进程，同 `lb_lock_file` 的约定带 `9>&-`
- **回调**：`|:-COMMAND-:| apply_tgw.sh '<T>' '<generation>' 'ok|error' '<原因>'`，原因为空时用 `-` 占位（clapi 的解析会丢掉末尾的空参数）。clapi 写入 `tgw_node_states`（§3）。handler 必须幂等：cland 最多重试 3 次
- **怎么下发**：
  - 期望状态对 S(T) 里的每台节点都一样，用 `toall=group-tgw-<T>:<id>,<id>...` 一次发出（同 `services/vpn_dispatch.go`），只有一台时用 `inter=<id>`
  - ⚠️ **成员列表绝不能为空**：cland 遇到空成员列表或未知组名会回退为「所有节点」（`cland/dispatcher.go` 的 `resolveTargets`）。不要用裸 `toall=`，它就是发给所有节点
  - 一次变更的下发集合 = 变更前的 S(T) ∪ 变更后的 S(T)；离开的节点收到空期望状态
- **什么时候下发**：
  - 挂载、解绑、改关联表、路由表与静态路由变化、传播或前缀列表变化、成员 VPC 增删子网 → S(T) 全部节点
  - 节点新进入 S(T)：成员 VPC 的第一台虚拟机落到该节点、加网卡；迁移目标在 `target_prepared` 回调里与 `prewarmTargetFdb` 一起下发（切换之前就要建好，否则切换那一刻跨 VPC 中断）→ 该节点
  - 节点重启：`launch_vm` 回调里、与 `VpnResyncNode` 同一位置调 `TgwResyncNode(routerID, hyper)`，按（TGW，节点）10 秒去重（同 `vpnResyncDue`）
  - 节点离开 S(T)：`clear_vm`、迁移 `completed` 后的源节点、卸网卡后不再有成员网卡 → 该节点收到空状态
  - `POST /transit_gateways/:id/resync`：手工重发全部节点（排障用）
- **空期望状态**：删除 `tgw-T`、各 `tr-`/`ta-` veth、`tgw` 表里的路由、`pref 100` 规则、TGW 的 FORWARD 规则、`nonat` 里 TGW 记账的条目，然后对每个原成员路由器调 `clear_local_router.sh`（没有别的使用者就回收，§2.6）。`generation` 文件保留，晚到的旧状态不会把网关建回来
- 其他约定沿用现有：只输出 `|:-COMMAND-:|` 回调行，其他输出重定向；iptables / ipset 先 `-C` 检查再加，`ip rule` 先查再加

### 2.5 校验

所有校验和权限检查都在**任何下发之前**做完（2026-09-30 修复的 D5、FAIL-N1：先下发、后鉴权，被拒的请求在节点上已经生效）。

- **网段不重叠**：
  - 挂载时，新 VPC 的所有 internal 子网与 T 下现有成员的网段不能重叠（VRRP 子网除外）
  - 成员 VPC 新建子网时，同样对 T 做重叠校验，重叠则 400
  - 挂载与「成员 VPC 建子网」都要先 `FOR UPDATE` 锁住 TGW 那一行再查：两个成员同时建子网、或两个 VPC 同时挂载，会各自通过校验。挂载的槽位分配与 10 个上限也靠这把锁
- **所有成员的子网都不能与 `192.168.196.0/24` 重叠**，不管现在有没有 LB 或 VPN 网关（VRRP 子网可以以后再建）。在 `router-A` 里 `tgw` 表优先于主表，重叠的成员网段会盖住本 VPC 的 VRRP 直连路由，LB 与 VPN 的心跳全断
- 成员子网不能与 `169.254.254.0/24` 重叠
- **与 VPN 网关**：
  - 成员 VPC 的网段不能与 T 内任一成员的 VPN 网关的对端网段（`remote_cidrs`）、客户端地址池重叠
  - 反过来，成员 VPC 新建或修改 VPN 连接、客户端地址池时也要对 T 校验
  - `validateTunnelLink` 拒绝 `169.254.254.0/24`；挂载时检查成员 VPC 已有的隧道地址
  - BGP 学到的前缀拦不住，以 TGW 为准（§2.2）
- **路由对称性**：A 能到 B 而 B 到不了 A 时回包必丢。不阻止（允许单向黑洞的特殊用法），网关详情返回 `asymmetric_routes`，界面在详情页顶部列出这些单向的 VPC 对
- **权限**：TGW 与 VPC 必须属于同一组织，系统管理员也不能跨组织挂载（非目标）；写操作需要组织写权限；系统管理员可以查看全部
- **删除**：删除 VPC 时有挂载 → 409；删除 TGW 要求没有挂载；成员 VPC 删子网 → 重新下发
- **安全组**：默认安全组对 `0.0.0.0/0` 放行 ICMP 和 22/3389，TCP/UDP 只放行本 VPC 的子网（§1.1）。所以挂上 TGW 之后，**不加规则时 ping 和 SSH 能通，其他 TCP/UDP 不通**，要用户在对方的安全组里按网段放行。这符合预期，T4 的使用指南要写明

### 2.6 路由器回收

初稿写的「节点上最后一台虚拟机离开后路由器从不回收」，在 2026-09-30 重写 `clear_local_router.sh` 之后已经不成立。现在的问题正好相反：**只为中转而建的路由器，在 `router_user` 看来没有任何使用者**（没有 tap、没有虚拟机定义、没有 VRRP）。会触发的场景：

- 删除一台从未落地的 B 虚拟机：`clear_vm` 是 `toall=`，node1 也跑 `clear_local_router.sh router-B` → node1 上 A 到 B 的流量静默中断
- node1 上 A 的最后一台虚拟机被删除或迁走，而 node1 上还有 C 的虚拟机（C→A 要用 node1 上的 `router-A`）→ `router-A` 被拆
- `clear_link.sh`（由 `detach_vm_nic.sh`、`clear_vrrp_ip.sh` 调用）拆网桥 `br<vni>`，而中转路由器的网关口挂在上面

改法：

- `bridge_lib.sh` 加 `router_tgw_user <router>`：路由器 netns 里有 `tr-*` 设备就算有使用者。`clear_local_router.sh` 的 `router_user` 与 `clear_link.sh` 的非 `unused` 分支都要查它
- 由谁回收：`apply_tgw.sh` 收到空状态（节点离开 S(T)）或解绑后，先删掉 `tr-`，再调 `clear_local_router.sh`，之后按原有判断决定拆不拆
- 删除 VPC 前必须先解绑（409），所以删 VPC 时路由器里不会有 `tr-`

## 3. 数据模型（clapi）

| 表 | 字段 | 说明 |
|---|---|---|
| `transit_gateways` | id、uuid、owner、name、description、status、`generation` | status：available / deleting；`generation` 是期望状态的代次（§2.4） |
| `tgw_route_tables` | id、uuid、tgw_id、name、`is_default`、`slot` | 建 TGW 时自动建一张默认表；内核表号 = `1000 + slot` |
| `tgw_attachments` | id、uuid、tgw_id、router_id（一个 VPC 只挂一个 TGW）、route_table_id（关联表）、`slot`（TGW 内 0–127，决定 veth 地址）、status、status_reason、owner | status：attaching / available / detaching / error |
| `tgw_propagations` | id、route_table_id、attachment_id、`prefixes`（允许的前缀列表，空 = 全部传播） | T1 只有「默认表 ← 全部挂载」；T3 加前缀列表 |
| `tgw_routes` | id、route_table_id、destination（CIDR）、attachment_id（黑洞时为 0）、type（static / blackhole） | T2 |
| `tgw_node_states` | tgw_id、hyper、generation、status（ok / error）、reason、updated_at | 各节点已应用到哪一代，挂载状态与排障都看它 |

- **唯一索引一律是部分索引**（`WHERE deleted_at IS NULL`）：`tgw_attachments (router_id)`、`(tgw_id, slot)`，`tgw_route_tables (tgw_id, slot)`，`tgw_propagations (route_table_id, attachment_id)`，`tgw_routes (route_table_id, destination)`。全表唯一索引会让「解绑后再挂」「删掉路由再加同一网段」报 duplicate key——2026-09-30 规则组的通知渠道绑定就是这么坏的，做法参照 `model/notification.go` 里用 `AutoUpgrade` 建部分索引
- **挂载状态**：所有在线的 S(T) 节点都回报 `generation ≥` 挂载那一代且都是 ok → available；任一节点 error → error（`status_reason` 记节点与原因；任一 PATCH 或 `resync` 重新下发并回到 attaching）；5 分钟没收齐 → error；离线节点不计（回来时由 `TgwResyncNode` 补发）。detaching 同理，收齐后软删除
- `routerScope` 查询走 `tgw_attachments.router_id` 的部分唯一索引
- 按 CLAUDE.md 约定：零值有意义的字段（布尔、槽位 0）不加 `gorm:"default"`；SQL 条件一律参数绑定

## 4. 接口

clapi（`api/src/apis/transit_gateway.go`，新增）：

- `POST/GET /transit_gateways`、`GET/PATCH/DELETE /transit_gateways/:id`（删除要求没有挂载；`GET` 详情带各节点的代次与状态）
- `POST /transit_gateways/:id/attachments {"vpc":{"id"},"route_table":{"id"}}`、`DELETE .../attachments/:att_id`、`PATCH .../attachments/:att_id`（改关联表）
- `GET/POST /transit_gateways/:id/route_tables`、`DELETE .../route_tables/:rt_id`（默认表不能删；有关联时拒绝）
- `POST/DELETE .../route_tables/:rt_id/propagations`（`prefixes` 可选）
- `POST/DELETE .../route_tables/:rt_id/routes`
- `GET .../route_tables/:rt_id/effective_routes`：返回计算后的有效路由（来源：传播 / 静态 / 黑洞），用于排障和前端展示
- `POST /transit_gateways/:id/resync`：重新下发全部节点
- `GET /vpcs/:id` 返回里补 `transit_gateway`（挂载信息）

每个改动型接口都要：

- 在 `apis/audit_actions.go` 的 `auditRoutes` 加映射（资源类型 `transit_gateway`，动作 create / delete / attach / detach / route_update 等），并在 `auditNameColumns` 加 `transit_gateway`，**带属主列**（2026-09-30 起按属主快照资源名）
- `cpgateway/src/apis/proxy_routes.go` 加白名单
- 注释写 swag，`make docs` 重新生成（`-dir` 已含 `src/services`）

另外：

- clapi `services/org.go` 的 `orgBlockingResources` 加上 `transit_gateways`，组织删除前要先删掉
- 删除 VPC、成员 VPC 增删子网、VPN 连接与客户端地址池的增改按 §2.5 校验或触发下发

## 5. 实施阶段

### T1：基础互通（单路由表、全互通）

交付：
- 模型：`transit_gateways`、`tgw_route_tables`（只有默认表）、`tgw_attachments`、`tgw_propagations`（自动全传播）、`tgw_node_states`，部分唯一索引
- 接口：TGW 增删查、挂载 / 解绑、有效路由查询、`resync`
- `routerScope` 与 §2.3 所有下发点的改造；节点进出 S(T) 的判断
- `scripts/kvm/apply_tgw.sh`（`tgw` 表与 `pref 100` 规则、FORWARD 隔离规则、rp_filter、MTU、nonat 记账、代次号）
- cloudrc 的路由器锁，`create_local_router.sh`、`clear_local_router.sh` 拿锁；`bridge_lib.sh` 的 `router_tgw_user`，`clear_local_router.sh`、`clear_link.sh` 查它
- 挂载、解绑、成员增删子网、节点新进入 S(T)、迁移 `target_prepared`、`launch_vm` 回调时的下发
- §2.5 的全部校验，包括 VPN 一侧（`validateTunnelLink`、VPN 连接与客户端地址池的增改）

验收（work-01/02/03；用临时 VPC，**不挂保留的 `ibmx` / `ibmr` / `aax`**：它们有 VPN 网关且在和 IBM 对接）：
1. VPC A、B、C 挂同一 TGW，三个 VPC 的虚拟机分散在三台节点，两两 TCP 互通（在对方安全组里放行网段后）；对端看到的源地址是对方虚拟机的内网 IP
2. 带浮动 IP 的虚拟机跨 VPC 互通，同时出公网仍走浮动 IP
3. 安全组用 **TCP 判定，不用 ping**（默认安全组对 `0.0.0.0/0` 放行 ICMP）：默认安全组下 ping 与 22 端口通、8080 不通；在 B 的安全组里加 A 的网段后 8080 通，删掉后不通
4. 挂载前与解绑后都不通；解绑后节点上 `tgw-T`、veth、`ip rule`、`tgw` 表、FORWARD 规则、`nonat` 里的对方前缀全部清理，只为中转而建的路由器被回收
5. 重叠网段挂载、成员 VPC 建重叠子网、成员子网与 `192.168.196.0/24` 重叠、与成员 VPN 的对端网段重叠，都返回 400
6. 热迁移一台 B 的虚拟机到一台原本不在 S(T) 的节点，迁移中 A→B 的中断与同 VPC 场景相当，迁移后互通
7. 真实重启一台节点，恢复后跨 VPC 互通（记录恢复耗时）
8. **中转路由器不被误拆**：删除一台从未落地的 B 虚拟机（`toall=` 的 `clear_vm`）后，node1 上的 `router-B` 还在、A→B 仍通；node1 上 A 的最后一台虚拟机删除后（node1 上还有 C），C→A 仍通；之后 node1 上不再有任何成员的虚拟机时，`tgw-T` 与各路由器被回收
9. **与 VPN 共存**：成员 VPC 有 VPN 网关时隧道与 BGP 不受影响；VPN 客户端访问其他成员 VPC 不通；挂载期间 VPN 主备切换后跨 VPC 互访正常
10. **不泄漏**：在 `router-0` 与公网口抓包，跨 VPC 的流量一个包都不出现，包括目的子网在本节点还没有网关口的窗口
11. 回归：同 VPC 内互访、LB `rb-lb`、浮动 IP、VPC 删除（有挂载时 409）、保留的 VPN 连接全部 up
12. 记录 10 个挂载时单台节点上的 netns 数、fdb 条目数、一次 `SendFdbRules` 与一次 `apply_tgw.sh` 的耗时

用例写进新的 `test-items/TC-19-Transit网关.md`。

### T2：多路由表与隔离

交付：`tgw_route_tables` 增删、挂载改关联、`tgw_routes`（静态 / 黑洞）、按 `iif` 选表、VPC 路由器侧的黑洞前缀、对称性警告

验收：
1. hub-spoke：共享 VPC 与 A、B 互通，A↔B 不通（A 访问 B 的包在 `router-A` 被黑洞，不会从公网口发出——在 `router-0` 抓包确认）
2. 静态黑洞覆盖传播路由；更具体的静态路由在黑洞内放行一段
3. 改关联表后秒级生效，所有 S(T) 节点一致（逐台比对 `ip route show table`，`tgw_node_states` 全部是同一代）

### T3：前缀过滤

交付：`tgw_propagations.prefixes`（允许列表，元素须是某个成员子网或其超网 / 子网）、有效路由计算按前缀过滤、成员 VPC 新增子网时按列表决定是否传播（有列表按列表，无列表默认传播）

验收：
1. B 只传播 `10.2.1.0/24`，A 访问 B 的 `10.2.9.0/24` 不通、访问 `10.2.1.0/24` 通
2. B 新建子网：有列表时不自动传播，无列表时自动传播
3. `effective_routes` 与各节点实际路由一致

### T4：前端、文档、收尾

- 前端：TGW 列表 / 详情（挂载、路由表、有效路由三个标签页，复用 `DataTable`、`DetailTabs`、`BaseModal`；详情显示各节点的同步状态）；VPC 详情显示所属 TGW；侧边栏入口；活动文案 `dashboard.overview.activityActions.transit_gateway.*`；三种语言文案，`npm run i18n:check`、`typecheck`、`lint` 零错误
- 使用指南 `docs/guide/` 加一页（含 §2.5 的安全组说明、与 VPN 网关共存的限制）；部署文档 `docs/deployment/` 增加 TGW 一节（排障：`ip netns exec tgw-<T> ip route show table all`、`ip netns exec router-<N> ip rule`、`ip route show table tgw`、`bridge fdb`、`$cache_dir/tgw/<T>/generation`）
- 评估是否加配额

## 6. 风险

| 风险 | 应对 |
|---|---|
| 下发范围扩大后遗漏某个调用点，表现为「部分节点间单向不通」 | §2.3 的清单，开工前再 grep 一遍；验收覆盖 3×3 节点组合 |
| 只为中转而建的路由器被回收逻辑拆掉，跨 VPC 静默中断 | §2.6 的 `router_tgw_user`；T1 验收第 8 项 |
| 浮动 IP / LB 的策略路由、默认路由把跨 VPC 流量带到公网 | §2.2 的 `pref 100` 规则与 FORWARD DROP；T1 验收第 2、10 项与 T2 第 1 项在 `router-0` 抓包 |
| 与同 VPC 的 VPN 网关冲突（网段、nonat、路由优先级、169.254 地址） | §2.5 的双向校验，`nonat` 各自记账，`tgw` 表独立于 FRR 管的主表；T1 验收第 9 项 |
| 对账脚本与 `add_fwrule.sh`、`clear_local_router.sh` 并发改同一个路由器 | §2.4 的路由器锁；`apply_tgw.sh` 只动自己名下的 `tr-`/`ta-`、`tgw` 表、`pref 100` 规则、FORWARD 规则与 `nonat` 里自己记账的条目 |
| 异步下发与回调重试乱序，旧状态覆盖新状态 | 代次号，节点丢弃更小的代次 |
| 下发的成员列表为空时 cland 回退为所有节点 | 生成控制串时断言非空，空集合不下发 |
| 规模：挂载多时每节点 netns 与条目暴涨 | v1 上限 10 个挂载；T1 实测数据决定是否做「按可达性裁剪下发范围」 |
| 节点离线期间的变更 | 节点重新上线时 `launch_vm` 回调里的 `TgwResyncNode` 全量补发，不依赖增量 |

## 7. 待确认

1. 是否需要按挂载限速（现在 TGW 东西向流量不限速，§2.2）
2. 每个 TGW 10 个挂载的上限，T1 实测后再定
3. BGP 学到的前缀与成员网段冲突时，现在只按 TGW 优先、不提示；要不要在 VPN 的 BGP 状态上报里检测并告警
4. 是否计配额（T4）

初稿的三条待确认已有结论，并入正文：默认安全组只放行本 VPC 的 TCP/UDP（§2.5）；`tr-`/`ta-` 的地址只在路由器与 TGW 的 netns 里，虚拟机看不到，元数据走 config drive 不经网络，`169.254.169.254` 占位也不在新地址段里（§2.1）；路由器里的 `tgw` 表固定 252，`tgw-T` 的表号是它私有的，不用登记（§2.2）。

## 8. 修订记录

- **2026-10-01**：按当前代码复查后修订。
  - 路由器回收：2026-09-30 重写的 `clear_local_router.sh` 会拆掉只为中转而建的路由器，新增 `router_tgw_user` 和路由器锁；删掉「从不回收」的旧说法（§2.6、§2.4）
  - veth 地址段从 `169.254.0.0/16` 按挂载 ID 取址，改为 `169.254.254.0/24` 按 TGW 内槽位取址，避开 VPN 的 BGP 隧道地址；两端 MTU 1450（§2.1）
  - 路由：去掉初稿里自相矛盾的写法（先说放主表、后说放单独的表），统一为 `tgw` 表加一条 `pref 100` 规则；补上 `nonat` 记账、FORWARD 防泄漏与 VPN 隔离、限速与 conntrack 的说明（§2.2）
  - 与 VPN 网关共存：校验、优先级、`nonat` 归属、中转隔离（§1.3、§2.2、§2.5）
  - 下发：初稿的 `toall=` 会发给所有节点，改为带成员列表的组；加代次号与节点状态表；迁移在 `target_prepared` 阶段下发；重启恢复按（TGW，节点）去重（§2.4、§3）
  - 软删除表的唯一索引改为部分索引（§3）
  - 代码位置更新（`sendFdbRules` 已移到 `common/fdb.go`），列出全部按 `router_id` 查询的位置，VRRP 网卡排除在扩大范围之外（§2.3）
  - 补上：权限检查在下发之前、跨组织挂载禁止、`orgBlockingResources`、审计取名的属主列、删除 VPC 时 409（§2.5、§4）
  - 验收：安全组改用 TCP 判定（默认组放行 ICMP）；新增中转路由器不被误拆、与 VPN 共存、不泄漏三项；测试不挂保留的 VPN 环境（§5）

## 9. 实施记录（2026-10-01）

### 9.1 代码

| 位置 | 内容 |
|---|---|
| `api/src/model/transit_gateway.go` | 六张表（含 `tgw_node_states`），部分唯一索引由 `AutoUpgrade` 建 |
| `api/src/services/transit_gateway.go` | 网关、挂载、路由表、传播、静态路由的增删改；所有校验在下发之前；自己开的事务提交之后才下发 |
| `api/src/services/transit_gateway_state.go` | 有效路由计算、节点状态组装（`assembleTgwState`，纯函数）、S(T)、下发、代次、节点回报与挂载状态、看护循环、网段校验、`TgwResyncNode` / `TgwNodeCheckLeave` / `TgwRouterChanged` |
| `api/src/common/tgw.go`、`fdb.go` | `RouterScope`；`SendFdbRules` 对普通网卡按整个网关的范围交换条目（VRRP 网卡不扩大） |
| `api/src/rpcs/` | `apply_tgw` 回调；`launch_vm`、`attach_vm_nic`、`clear_vm`、`detach_vm_nic`、`migrate_vm`（`target_prepared`、`completed`、回滚）的钩子；`clear_vm` / `prewarmTargetFdb` 的范围 |
| `api/src/services/` 其他 | 子网建删的校验与重新下发、删 VPC 409、VPN 网段与隧道地址的双向校验、`orgBlockingResources`、启动看护 |
| `api/src/apis/transit_gateway.go` | 21 个接口；审计映射与 `auditNameColumns`；VPC 详情的 `transit_gateway` |
| `cpgateway/src/apis/proxy_routes.go` | 白名单 |
| `scripts/kvm/apply_tgw.sh` | 节点对账（新） |
| `scripts/cloudrc`、`bridge_lib.sh`、`clear_local_router.sh`、`clear_link.sh`、`create_local_router.sh` | `tgw_table`、`ensure_tgw_table`、`router_lock_file`、`router_tgw_user`；回收守卫；路由器锁 |
| `scripts/kvm/create_floating.sh`、`create_lb_floating.sh` | 浮动 IP 规则带 `pref $fip_rule_pref`（32000） |
| `web/`、`docs/guide/transit-gateways.md`、`docs/deployment/07-operations.md`、`test-items/TC-19-中转网关.md` | 界面、使用指南、排查、用例 |

### 9.2 与前文不同的做法

- **`tgw-<T>` 的 `rp_filter` 是 0**，不是 2（§2.1 已改）：本机 netns 模拟里 2 会丢掉所有转发包。
- **浮动 IP 规则带显式优先级**（§2.2 已改）：模拟里复现了不带 `pref` 时被分到 99、跨 VPC 流量走浮动 IP 表的情况。
- **挂载与 `resync` 时交换转发条目**（§2.3 已改）。
- **路由器侧的归属**：`router-<N>/tgw.owner` 记录 `<网关 ID> <挂载 ID>`。清理一个挂载时，只有归属仍是它才清表 252、规则、nonat 与 FORWARD 规则，只删自己的 veth；接入时先删掉该路由器里别的 `tr-*`（一个 VPC 同时只属于一个挂载，别的必然是旧的，而且不同网关的同一槽位会给出同一个 `/31`）。防的是同一个 VPC 从一个网关挪到另一个网关时两边的下发乱序。
- **挂载时的传播**：`propagate`（缺省 true）是「把这个 VPC 的子网传播进**默认**路由表」，与它关联哪张表无关；中心辐射这类配置用传播接口单独加。
- **挂载状态**：`error` 的挂载仍算成员（在期望状态里），节点后来都回报 ok 时自动回到 `available`；解绑失败时保持 `detaching` 并写原因，再解绑一次就是重试。
- **静态路由**：除默认路由外，还拒绝与 `169.0.0.0/8` 重叠、与任一成员 VPN 网关网段重叠的目的；每张表最多 100 条，每个网关最多 20 张表。
- **节点回报**多带一个「挂载数」：0 表示离开的节点应用了空状态，clapi 删掉它的状态行而不是记录。
- **S(T)** 还包括成员云服务器进行中迁移的目标节点，回滚时排除那次迁移再判断目标节点是否离开。

### 9.3 测试

- **Go 单元测试**（`api/src/services/transit_gateway_test.go`）：全互通、中心辐射、前缀过滤（窄于 / 宽于子网）、静态路由覆盖与黑洞、指向非活动成员的路由、覆盖 VPC 自身网段的路由、链路地址、目的校验、保留网段。`go build`、`go vet`、`apis` / `common` / `rpcs` / `services` 的现有测试通过（含审计路由映射、SQL 字面量扫描、shell 转义检查）；Swagger 已重新生成。
- **netns 模拟**（本机 WSL2，不是 CloudLand 环境）：用 Go 生成的状态 JSON 驱动 `apply_tgw.sh`，三个 VPC 路由器加模拟虚拟机，70 项全部通过（§9.5 修完后重跑）：全互通、源地址保留、带浮动 IP 的虚拟机、不带优先级的规则被分到 99 的复现与 `pref 32000` 的修法、幂等、子网没有网关口时被 `-i tr+ -o ti-+` 丢弃且 router-0 收不到、`clear_local_router.sh` 保留有 `tr-` 的路由器、中心辐射、黑洞与 `/32` 静态路由、前缀过滤、旧代次被丢弃、解绑与回收、VPC 换网关时下发乱序、清空；§9.5 加的：`[detached]` 规则被删除、半途失败的运行记下代次而不记状态且之后旧代次被丢弃、`tgw.owner` 找到的残留 veth 被清理、VPN 也在用的 `nonat` 条目保留、`gz:` 压缩状态、清空后晚到的旧状态被丢弃、`-i tr+ -o tr+` 规则。另跑 10 轮 `apply_tgw.sh` 与 `clear_local_router.sh` / `create_local_router.sh` 并发，全部得到正确结果。
- **PostgreSQL 流程测试**（`transit_gateway_pg_test.go`，本机 WSL 里的 PostgreSQL 18 + 记录命令的假 cland）：建网关重名、挂载时的下发集合与控制串（单节点 `inter=`、多节点 `toall=group-tgw-<ID>:…`）、挂载时交换的转发条目、状态内容（VRRP 子网不传播、链路地址）、节点回报与挂载状态（attaching → available、节点报错 → error、修好后回到等待、超时列出节点）、旧代次不覆盖、所有校验在任何下发之前（重叠 VPC、重复挂载、成员子网重叠、VRRP 段、VPN 网段双向、隧道地址、静态路由盖住 VPN 网段）、删成员 VPC / 删有挂载的网关被拒、改关联与空表、传播重复与不重叠、解绑完成后软删且槽位与名字可重用（部分唯一索引）、节点加入去重、节点离开的空状态与状态行删除、成员子网变化重新下发；§9.5 加的：离开时代次 +1 与 `leaving` 行、未确认节点的退避重发、删网关后对 `leaving` 节点重发、单向路由检测、成员 VPN 网段进 `throw`。现有的 services / rpcs / apis 全部 PG 测试同时通过。测试里生成的状态 JSON 再喂给 netns 模拟（`sim3.sh`，按状态自动搭路由器与模拟虚拟机），两两连通与可达列表全部一致（14 项）。需要 CGO 的 cpgateway 测试与 api 的 `dbs` / `tracing` 包在 WSL 里跑，全部通过
- **三节点实测**（2026-10-01 部署后，记录 `test-items/runs/2026-10-01-7acd9bbd+中转网关.md`）：TC-19 除 TGW-12 外全部跑完，产品行为都符合设计；与用例不符的地方都是用例写法或测试方法的问题，已改用例。关键数字：挂载到 `available` 1.4–2.6 秒；改路由表、传播、关联 3–6 秒全部节点同步；热迁移到原本不在网关里的节点，请求后 6.3 秒（`target_prepared` 阶段）那台节点就建好 `tgw-<ID>`，迁移期间跨 VPC 最长断 0.4 秒；节点状态丢失后重新下发或开机同步 3 秒恢复；节点应用失败、障碍消失后看护循环 61 秒自动重发恢复；节点离开时先显示 `leaving`，确认后删除状态行、代次 +1。
- **真实重启 work-02**（TGW-12，用户同意后补跑）：到 work-02 的跨 VPC 中断 232 秒（节点 +188 秒上线，之后约 59 秒是开机同步下发的几十条命令在节点上串行排队，中转网关状态约 +235 秒应用），不经过 work-02 的跨 VPC 互访 0 丢包，rb-lb 0 失败。网关详情里被重启节点一直显示 `ok`（代次没变），恢复与否要看数据面。
- **测试中发现的既有问题**：删除云服务器时 `del_fwrule.sh` 把 MAC 当 IP 传给 `del_host.sh`，`dhcp_release` 从未成功，2 小时内复用同一地址的新云服务器（靠 DHCP 的镜像）拿到 clapi 不认识的动态地址，和谁都不通。与中转网关无关，已修并部署三台节点。
- **仍没测**：与 VPN 网关同在一个 VPC、界面写操作（界面只在本机 5173 上用假数据看过）、成员合计几百块网卡时的状态大小与 `gz:` 压缩路径（只在本机模拟里验证）。

### 9.5 代码复查后的修改（2026-10-01）

实施完成后做了一轮独立的代码复查（15 条），全部修掉：

- **重发与离开的确认**：节点没确认的状态（落后于当前代次、报错、下发时离线、cland 重启丢了命令）由看护循环 `tgwRetry` 重发，同一节点按 1 分钟起、翻倍到 10 分钟退避；离线节点等它回来再发。离开 S(T) 的节点在 `tgw_node_states` 里留一行 `leaving`，确认应用了空状态才删，之前一直重发；删掉的网关只剩这些行，看护循环照样处理。网关详情的节点列表也列出它们（状态「退出中」），算在「同步中」里
- **离开也加代次**：`TgwNodeCheckLeave` 让代次 +1，空状态的代次一定大于还在路上的旧状态，节点先收到空状态、后收到旧状态时旧的被丢弃（原先同代次，旧状态晚到会把网关建回来）
- **挂载状态的判定**：`tgwEvaluate` 的每次写入都带上判定时的状态与代次作为条件，判定与解绑、改配置并发时以后者为准；原因超长时截断
- **VPN 网段**：成员 VPC 自己的 VPN 网段（对端网段、客户端地址池）进路由器表 252 的 `throw`，任何路由表的路由都抢不走通往本 VPC VPN 的流量
- **锁**：挂载、给成员 VPC 建子网、删 VPC 都先锁 VPC 那一行再锁网关那一行，统一顺序；原先「两个请求各自通过校验」的窗口在 VPC 一侧也关上了
- **事务提交后才下发**：成员 VPC 建删子网时，`TgwRouterChanged` 只在调用方的事务里加代次，返回的下发在提交之后执行（原先在事务里下发，回滚时节点已经应用）
- **状态过大时压缩**：超过 64 KiB 的状态 gzip 后 base64，加 `gz:` 前缀下发，`apply_tgw.sh` 解压；压缩后仍超过 120 KiB 的不下发、记错误（命令经环境变量传给执行器，单个字符串约 128 KiB 上限）
- **单向路由提示**：详情返回 `asymmetric_routes`（§2.5）
- **迁移失败 / 超时**：目标节点同样按「没有成员了就离开」处理（原先只处理回滚）
- **节点脚本**：
  - 要清理的挂载 = 上次成功状态里的 ∪ 路由器 `tgw.owner` 仍指向本网关的（一次半途失败的运行没记下状态，它插上的 veth 原先永远清不掉）；路由器 netns 已经不在时只删这两个文件
  - 拿不到路由器锁算失败（原先静默跳过、照样回报 ok）
  - FORWARD 防泄漏规则在装任何路由之前插好，插不上就不装这个路由器的路由；新增 `-i tr+ -o tr+` 丢弃（回环，或一个路由器换网关时把两个网关连起来）；iptables 一律 `-w 10`（xtables 锁是所有 netns 共用的，心跳与 LB 守护脚本一直在拿）
  - 删 `nonat` 条目时跳过别的脚本也在用的：本 VPC 的网段与 VPN 网段（两份状态里的 `throw`）、VPN 网关路由集写下的 `vpn-*/nonat.current`
  - `ip rule show` 把 `ta-` 已不存在的规则显示成 `iif ta-N [detached] lookup …`，原先的匹配认不出、永远删不掉
  - `clear_vrrp_ip.sh` 调 `clear_link.sh <vlan> vrrp`：VRRP 子网从不经过中转网关，删最后一个 VRRP 地址时不必因为路由器挂着网关而保留这条链路
  - `create_local_router.sh` 拿不到路由器锁时退出，不再不加锁地照建

### 9.6 已知限制

- cpgateway 删组织前只看配额用量，中转网关不是配额资源：只剩中转网关的组织在控制面能删掉，区域侧会因 `orgBlockingResources` 拒绝（409）而留下组织（放置组、密钥等非配额资源同样如此，原有问题）。
- `tgwSyncFdb` 把所有成员网卡的条目放进一条命令（经环境变量传给执行器，约 128 KiB 上限），成员合计约 600 块网卡以上要分批。
- 节点重启后约 1 分钟才恢复中转：节点上线时开机同步一次下发几十条命令，cloudlet 串行执行，`apply_tgw.sh` 排在后面（TGW-12 实测节点 +188 秒上线、约 +235 秒应用、+247 秒恢复）。可以考虑给路由类命令提高优先级。
- 网关详情的节点列表只反映节点上次回报的代次与结果：节点离线期间仍显示 `ok`（代次没变、状态行不变），看不出离线；可以叠加节点的在线状态。
- 操作动态里有几条失败的中转网关操作没有资源名（2026-10-01 实测时观察到，没深查）。
