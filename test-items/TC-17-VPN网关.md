# TC-17 VPN 网关（VPN）

优先级 **P1** · 会建 VPC / 虚拟机 / 网关，在计算节点上起 charon / keepalived / FRR / WireGuard · 单网关可跑的部分约 90 min，VPN-18 另约 20 min（2026-09-28 核对：当前环境只有 1 个空闲公网地址，见「当前环境的可执行范围」）

2026-09-23 按 `docs/architecture/plan/vpn-gateway-plan.md` 实施 V1（站点到站点 + BGP）、V2（WireGuard 客户端）、V3（配额 / 审计 / 告警）时的测试用例，已在 work-01/02/03 全量跑通。方案 §18 记了实施中与设计不同的做法和测出来的问题，§17 是没做与没测的项（2026-09-25 起两份 VPN 设计文档已合并为 `vpn-gateway-plan.md`，旧章节号见其附录 D）。2026-09-27 的隧道高可用、单节点网关、代码复查修复部署后只做过 §18.18 的抽查；**2026-09-28 按代码逐条核对了本文件**，新增 VPN-19 至 VPN-21（凭据回读按角色、界面写操作、代码复查修复的回归），都还没跑过。

> 🚫 **测试网关建在临时 VPC 里，不要建到 `rb-vpc`。** 网关与同 VPC 的 LB 共用 VRRP 子网网卡 `ns-<vni>` 与 MAC，在保留环境里建 / 删网关有打断 `rb-lb` 的风险（`rb-vpc` 里已经有一个来历未记录的保留网关 `a1`，同样不能动）。全程每做完一步回头确认 `rb-lb` 仍是 10/10。

## 设计约定（判定预期的依据）

- **每个 VPC 最多一个网关** = 一个 VrrpInstance（与 LB 同一张表、同一套 keepalived 守护）+ 一或两个公网浮动 IP（`floating_ips.vpn_gateway_id`、`vpn_endpoint`）。主备两个节点由 zone 内 `select=` 调度；**zone 里只有一个可用节点时主备网关按单节点运行**，有了第二个节点后看护任务自动补备节点；双活网关要两个节点，不够时创建返回 400（132009）。
- **主节点**在 VPC 路由器 netns `router-<N>` 里跑：strongSwan **charon**（站点到站点，IKEv2，XFRM 接口 `ipsec-<隧道 if_id>`）、看护循环 `vpn_watch.sh <网关ID>`、**FRR**（只在有 BGP 连接时起，pathspace `-N vpn-<网关ID>`，守护进程 **5 个**：`zebra → mgmtd → staticd → bfdd → bgpd`，R1 起 bfdd 总是起）。**备节点只跑 keepalived**；WireGuard 接口 `wg-<网关ID>` 两个节点都有（配置两边都下发），只是客户端流量只进持有 VIP 的节点。
- 目录：`/opt/cloudland/cache/router/router-<N>/vpn-<网关ID>/`（strongswan.conf / swanctl.conf / wg.conf / run/ / tunnel_routes / notify.log / traffic.base / watch.pid / *.reported），keepalived 在同级 `vrrp-<VrrpInstanceID>/`（含 `keepalived.pid`）；FRR 在 `/etc/frr/vpn-<gw>/frr.conf` 与 `/var/run/frr/vpn-<gw>/`。
- **charon 多实例**：`unshare -m` + `mount --bind <vpn_dir>/run /run` 起，pid 在 `<vpn_dir>/run/charon.pid`。`swanctl` 子命令要在 `--uri` **前面**。
- **东西向回程**：其他节点把对端网段指向网关节点的 VRRP **单播**地址（`via <ip> dev ns-<vrrp vlan> onlink`，由 `nexthops` 文件与 F7 探测维护）；主备节点由 `vpn_notify.sh` 按角色装（master 指隧道接口 / 黑洞，backup 指对端，fault 黑洞）；每个节点都加 `nonat` ipset 条目。
- **BGP**：eBGP 跑在隧道内链路地址（`tunnel_local_ip` / `tunnel_peer_ip`，同一 /30）上；对端明细前缀只进网关节点，其他节点只拿 `remote_summary_cidrs`；汇总黑洞是 **staticd** 的 `ip route X Null0 250`，**不是**内核黑洞。
- **主节点身份**：`report_vpn_status.sh` 只在持 VIP 的节点上报 `vpn_master`，每 20 秒重发、接管后第一分钟每次心跳都发；clapi 的分脑窗口 30 秒，但有「旧主 `vpn_backup` 主动让位」或「cland 报告旧主失联」任一证据就立即接管（R0 起，日志 `takes over ... at once (...)`）。主节点声明 90 秒没刷新时看护任务清主节点、把隧道置 down 并告警。
- **状态上报**只接受主节点的（双活是隧道所在节点）：`vpn_conn_status`（IKE SA）、`vpn_client_status`（wg 握手 / 流量）、`vpn_bgp_status`（邻居 / 前缀 / BFD），变化或 5 分钟重报。连接状态由隧道汇总：`up` / `degraded`（只有非主隧道在线；负载分担时部分在线）/ `down` / `pending` / `disabled`。
- **客户端**：平台代生成密钥时私钥只在创建响应里出现一次，`GET .../clients/:id/config` 只给不含私钥的模板（`<your private key>` 占位）；`client_routes` 拒绝 `0.0.0.0/0`（2026-09-27 才真正生效）；`client_dns` 默认不下发。
- **凭据**：PSK / BGP 口令 / 客户端预共享密钥用 `VPN_SECRET_KEY` 加密（`v1:` 前缀），没配时建网关 / 连接 / 客户端 503（132031）。**有写权限（编辑者及以上、系统管理员）时查询直接返回明文**（`psk`、`bgp_password`、`tunnels[].psk`，客户端配置模板里的 `PresharedKey`），观察者只有 `*_set`、模板里是 `<preshared key>`。
- **配额**：cpgateway `vpn_gateways`（系统默认 1）。Admin 已设为 **5**，**当前保留环境占 4 个**（`a1`、`ibmx-gw`、`ibmr-gw`、`aax-gw`），公网 IP 用量 9 / 200。所以**同一时刻只能再建 1 个测试网关**，第 2 个会被网关 429 拦下。
- **错误码**：132005 同 VPC 已有网关 409；132006 网关还没可用 400；132007 还有连接 / 客户端 / 隧道在用 409；132008 已停用 400；132009 zone 可用节点不够 400；132031 凭据不可用 503；132032 网段冲突 400；132033 VPC 有网关不能删（409，**当前实际拿不到，见 VPN-09**）；131004 公网地址不够 409；参数错 100003（400）。

### ⭐ 最容易误判的几点

- **备节点上没有 charon / FRR / 看护循环是正常的**，只有主节点跑数据面；但 `wg-<gw>` 接口两个节点都有，别当成残留。
- **节点上的进程总数被保留网关污染**：work-02 上常驻 4 个 charon、4 个看护循环、若干组 FRR。判断测试网关时一律按网关看：`<vpn_dir>/run/charon.pid` 是否存活、`ps -eo args | grep -cE '^/usr/lib/frr/[a-z]+ -N vpn-<gw> '`、`ps -eo args | grep -c '[v]rrp-<vrrp>/keepalived.conf'`、`<vpn_dir>/watch.pid`，**不要**用 `pgrep -x charon | wc -l`。
- **`pgrep -f` / `pgrep -c -f` 经 ssh 执行会把自己这条命令算进去**（09-27 的脚本里 keepalived 数成了 3），模式写成 `[v]rrp-...` 这种带方括号的形式；`vpn_watch.sh` 循环里临时 fork 的子 shell 命令行和父进程一样，数看护循环要看父进程不是 `vpn_watch.sh` 的那些（或直接对照 `watch.pid`）。
- **连接状态 `down` 但节点上 charon 在跑**：先看 `swanctl --list-sas --uri unix://<vpn_dir>/run/charon.vici`，再看对端；两端 PSK / `remote_id` 不一致时 IKE 永远起不来，**隧道的** `tunnels[].last_error` 里有 charon 的原话（2026-09-30 修好上报，2026-10-01 已部署；部署前一直是空的）。对着不存在的对端（本文件用 `198.51.100.x`）建的连接本来就一直是 `down`。
- **客户端重新启用 / 切主后十几秒才通**：是客户端侧 WireGuard 要等自己重新握手（KEEPALIVE_TIMEOUT + REKEY_TIMEOUT ≈ 15 秒），不是网关。
- **主节点 keepalived 被杀后旧主上的 charon / FRR 也没了**：`vpn_notify.sh backup` / 守护脚本会把它们停掉，只留 keepalived。

## 测试环境

脚本在 work-01（先 `source /root/st-env.sh`，再 `source /root/vpn-e2e-lib.sh`；状态在 `/root/vpn-e2e.state`，各阶段可以单独重跑）。**命令一律写成 work-01 上的脚本文件再执行**，经 ssh 一行传 JSON 的引号很容易出错；`st-env.sh` 占用变量名 `B`、`T`、`BODY`、`CODE`，自己的脚本别用。

| 脚本 | 阶段 | 2026-09-28 现状 |
|---|---|---|
| `/root/vpn-e2e-lib.sh` | `sv` / `lv` 状态、`on_node`、`gexec`（guest agent 内执行）、`vm_ip` / `vm_host`、`vm_probe`（TCP/22 探测）、`wait_field` | 可用 |
| `/root/vpn-e2e-1-setup.sh` | 两个 VPC（`vpn-a` 192.168.71.0/24、`vpn-b` 192.168.72.0/24）各 3 台虚拟机分散在三节点 | 可用；**先把旧状态文件挪走**（见 VPN-00） |
| `/root/vpn-e2e-2-gateways.sh` | 两个网关 + 静态站点连接 + 全矩阵探测 | **跑不通**：建完 vpn-a 后 vpn-b 被 429 / 409 拦下，脚本 `fail` 退出。单网关用 VPN-01 的手工步骤 |
| `/root/vpn-e2e-3-bgp.sh` | 切到 BGP、VPC B 加子网 192.168.73.0/24 与 vb4，A 自动学到 | 依赖 vpn-b，SKIP |
| `/root/vpn-e2e-4-failover.sh` / `-4b-failback.sh` | `kill -9` 主节点 keepalived，探测与切换时间 | 探测的是 VPC A → VPC B，单网关时不适用；用 VPN-04 的单网关步骤 |
| `/root/vpn-e2e-5-client.sh` / `-5b-disable.sh` | WireGuard 客户端（在 work-03 宿主机上 `wg-quick`）、停用 / 启用 | 可用，前提是 VPN-01 把 `gw_a` / `master_a` / `router_a` / `gwnum_a` 写进状态 |
| `/root/vpn-e2e-9-fixes.sh` | 2026-09-24 修复的验证：占用 IP 创建 / 仅响应连接 / 多对网段 / 并发客户端 / `error` 救回 | 依赖 vpn-b，整体跑不通；F4、F5 可照抄到 vpn-a 上（见 VPN-01、VPN-05） |
| `/root/vpn-e2e-11-disable.sh` | 网关停用 / 启用 | 依赖 vpn-b 与 BGP 连接；单网关按 VPN-11 手工做 |
| `/root/vpn-e2e-10-isolate.sh` | 客户端与远端站点不互通 | 依赖 vpn-b；单网关用 VPN-05 的「假对端」做法 |
| `/root/vpn-e2e-7-monitor.sh <节点>` | 节点重启跟踪（`TC-14` HA-08），重启命令另发 | 破坏性，按 TC-14 |
| `/root/vpn-e2e-6-cleanup.sh` | 删网关 / 虚拟机 / 子网 / VPC 并核对节点 | 可用，但最后几项的「节点上计数为 0」被保留网关污染，按 VPN-10 的按网关核对 |
| `/root/deploy-verify-20260927.sh` | 09-27 部署后抽查：凭据回读、`client_routes` 400、网关地址 `.145`、伪造 `node_liveness` | 可用（第 3 项对保留的 `ibmx-gw` 发了一次必然 400 的 PATCH；以后改用测试网关） |
| `/root/zone-test-20260927.sh` | VPN-18 单节点与补备节点 | 可用；清理里 `DELETE /zones/<uuid>` 会 404（接口按**名字**删），要手工 `DELETE /zones/ztest` |
| `/root/ibmr-fo.sh`、`/root/bfd-*.sh`、`/root/aax-*.sh`、`/root/ibm-aax.sh`、`/root/ibmx-*.sh` | VPN-12 至 VPN-17 的故障注入与观察 | 都作用于保留网关，**未经用户同意不跑**；`ibmr-fo.sh` 的 `M1` 还是已删除的旧 IBM 成员 `150.239.81.237`，`tunnel1` 场景要先改成 `150.239.85.49` |
| `/root/console-test/*.js` | 界面脚本 | 按约定在**本机**对 5173 跑（副本在 work-01，`BASE` 改成 `http://127.0.0.1:5173`）；`vpn-ui.js` 与 `vpn-secrets-ui.js` 已过时，见 VPN-07 |

> ⚠️ 这套脚本的资源名是 `vpn-a` / `vpn-b` / `va1..va3` / `vb1..vb4` / `laptop-1`，**不是** `wp-` 前缀（先于命名约定写成）。收尾检查 C-11 专门按这些名字捞。
> ⚠️ `ubuntu-24.04-minimal` 镜像**没有 `ping`**，虚拟机发起的连通性一律用 `vm_probe`（`bash /dev/tcp` 打 22 端口，native 安全组放行 22）；WireGuard 客户端在 work-03 宿主机上，可以用 `ping`（setup 脚本给 native 组放行了 ICMP）。
> ⚠️ 本文件里的「假对端」一律用文档保留地址 `198.51.100.0/24`（TEST-NET-2），远端网段用 `10.99.x.0/24`，都与现有 VPC、VRRP 子网、客户端地址池不冲突。

## 当前环境的可执行范围（2026-09-28 核对）

**约束**：`public-756` 只剩 `52.117.101.158` 一个可分配地址（`.145` 是子网网关，库里 `allocated=false`，但 09-27 起指定它会 400）；Admin 的 VPN 网关配额 5 个已用 4 个。保留网关 `a1`（rb-vpc，.152）、`ibmx-gw`（ibmx，.153，连接 `to-ibm` / `to-ibmr-bfd` / `to-aax`）、`ibmr-gw`（ibmr，.154 / .155，`to-ibm-r` / `to-ibmx-bfd`）、`aax-gw`（aax，双活 .156 / .157，`to-ibmx-aa` / `to-ibm-aa`）**只做只读观察**（`GET`、节点上的 `cat` / `ip` / `iptables -S` / `vtysh -c 'show ...'` / `swanctl --list-*`），不 PATCH、不建删连接、不重启、不注入故障。三个保留主备网关的主节点都是 work-02、备节点 work-03（`a1` 是 work-02 / work-01），`aax-gw` 的 node1 在 work-02、node2 在 work-03；work-01 是 ibmx / ibmr / aax 的「第三节点」。

| 用例 | 当前 | 说明 / 要跑全需要什么 |
|---|---|---|
| VPN-00 前置 | 可跑 | 先挪走旧状态文件 |
| VPN-01 创建网关 | 可跑 | 只建 `vpn-a` 一个；409 同 VPC 要临时把配额调到 6 |
| VPN-02 静态站点到站点 | 部分 | 配置、路由、nonat、MSS、流量接口可跑（假对端 + 保留网关只读）；**连通、18/18 矩阵、流量计数、重协商 SKIP**：要第二个公网地址建 vpn-b |
| VPN-03 BGP | 部分 | 只能核对配置（frr.conf、5 个守护进程、Null0 250、默认 frr.conf 不被覆盖）；Established、学路由 SKIP（同上）；Established / BFD 可在保留网关上只读看 |
| VPN-04 主备切换 | 可跑（单网关版本） | 用 WireGuard 客户端探测代替 VPC A → B |
| VPN-05 WireGuard 客户端 | 可跑 | 「客户端与站点不互通」用假对端 + DROP 计数验证 |
| VPN-06 客户端停用 / 启用 | 可跑 | 多客户端同时在线（新增）要第二台宿主机当客户端 |
| VPN-07 界面冒烟 | 可跑 | 本机 5173，只读 |
| VPN-08 配额、审计与告警 | 部分 | 新告警 SKIP（要真实对端让隧道 up → down）；历史告警只读核对 |
| VPN-09 接口校验 | 可跑 | 观察者一项要按 VPN-19 准备账号；`VPN_SECRET_KEY` 一项 SKIP（要改 `.env` 重启 clapi） |
| VPN-11 网关停用 / 启用 | 部分 | 站点连通与 BGP 快照 SKIP |
| VPN-12 双隧道与双地址 | 只读 | 建双地址网关要 2 个空闲地址；在 `ibmr-gw` / `ibmx-gw` 上只读核对 |
| VPN-13 快速切换 | 只读 | 故障注入都作用于保留网关 + IBM，要用户同意；单网关的 keepalived 场景在 VPN-04 |
| VPN-14 双活 | 只读 | 建双活网关要 2 个空闲地址；`aax-gw` 只读 |
| VPN-15 负载分担与全连接 | 部分 | 「负载分担改回主隧道优先」可在假对端上做；其余只读 / SKIP |
| VPN-16 F7 | 只读 | work-01 上看 ibmx / ibmr / aax 的 `nexthops`；断网 SKIP |
| VPN-17 公网地址增减 | 部分 | 加 vip2 要第二个空闲地址 SKIP；创建中改地址（回归点 47）、`addable_endpoint`、按隧道流量可跑 |
| VPN-18 单节点 | 可跑 | 与 vpn-a **互斥**（都要 `.158`、配额也只剩 1 个）；`error=resource` 一项 SKIP |
| VPN-19 凭据回读按角色 | 可跑 | 要按 TC-18 PG-14 的方法建一个只读成员 |
| VPN-20 界面写操作 | 可跑 | 用 vpn-a；只放行测试网关的写请求 |
| VPN-21 代码复查修复 | 部分 | 47 / 49 / 50 / 52 / 53 可跑；46 / 48 / 51 / 55 SKIP |
| VPN-10 删除与清理 | 可跑 | 按网关核对残留，不按节点总数 |

**建议顺序**（单网关，约 90 min）：VPN-00 → VPN-01（先做建网关前的负面用例，再建 vpn-a）→ VPN-21 的 47 → VPN-05 → VPN-06 → VPN-02 / 03 的假对端部分 → VPN-09 → VPN-15 部分 → VPN-19 → VPN-11 → VPN-04 → VPN-20 → VPN-07 → VPN-08 → VPN-21 其余 → VPN-10；之后另做 VPN-18。只读用例（VPN-12 至 VPN-17 的只读部分）随时可做。

**要跑全 SKIP 的部分需要**：① 第二个空闲公网地址（例如用户确认 `a1` 可以删 → 放出 `.152`；或给 `public-756` 扩容 / 加一个公网子网），才能建 vpn-b 做站点互通、BGP、双隧道、双活；② 用户同意对保留网关 / IBM 连接注入故障（VPN-13、14、15、16 的切换测量）；③ 改 `.env` 并重启 clapi 的窗口（`VPN_SECRET_KEY` 相关）。

---

## VPN-00 前置

**P1** · 脚本 `/root/vpn-e2e-1-setup.sh`

```bash
# on work-01
mv /root/vpn-e2e.state /root/vpn-e2e.state.$(date +%Y%m%d%H%M) 2>/dev/null   # 09-24 leftovers (gw_a, gw_b, ...) make stage 2 skip creation
docker exec cloudland-postgres psql -U postgres -d cloudland_cpgateway -Atc "select q.max_vpn_gateways, c.vpn_gateways, q.max_public_ips, c.public_ips from org_resource_quotas q join org_resource_consumptions c on c.org_id=q.org_id and c.region_id=q.region_id join organizations o on o.id=q.org_id where o.name='Admin'"
docker exec cloudland-postgres psql -U postgres -d cloudland -Atc "select address from addresses where address like '52.117.101.%' and allocated=false and deleted_at is null"
grep -c "^VPN_SECRET_KEY=." /opt/cloudland/deploy/docker/.env
bash /root/vpn-e2e-1-setup.sh
```

### 检查项

- [ ] Admin `max_vpn_gateways − vpn_gateways ≥ 1`（2026-09-28 为 5 − 4 = 1：只够一个测试网关，否则 429 `quota_exceeded`）
- [ ] 空闲地址只有 `.145`（网关，不可分配）与 `.158`；不是这样先查清谁占了，别直接开跑
- [ ] `.env` 里有 `VPN_SECRET_KEY`（否则建网关 / 连接 / 客户端 503，132031）
- [ ] 6 台虚拟机全部 `running`，`vm_ip va1` 等能取到地址，`vm_probe va1 $(vm_ip va2)` = `ok`（单网关时 VPC B 的 3 台用不上，嫌慢可以只建 VPC B 与子网）
- [ ] 三个节点都有 `router-<A>`、`router-<B>` netns
- [ ] 基线快照（清理时对照）：三节点 `ls -d /opt/cloudland/cache/router/router-*/vpn-* | wc -l`（2026-09-28 为每台 4 个：vpn-13..16）、`ls -d /etc/frr/vpn-* | wc -l`（work-01 0、work-02 3、work-03 3）、`select count(*) from floating_ips where vpn_gateway_id > 0 and deleted_at is null`（6）、`md5sum /etc/frr/frr.conf`（三节点）

---

## VPN-01 创建网关

**P1** · 单网关手工步骤（`/root/vpn-e2e-2-gateways.sh` 在建 vpn-b 时会失败退出）

**先做建网关前的负面用例**：vpn-a 建好后配额满（5/5），之后任何 `POST /vpn_gateways` 都先被网关 429 拦下，看不到 clapi 的校验。下面几条都在 VPC B 上做，不会占公网地址：

```bash
source /root/st-env.sh; source /root/vpn-e2e-lib.sh
vb=$(lv vpc_b)
before=$(psql_c "select count(*) from vrrp_instances where deleted_at is null")
api_code POST /vpn_gateways "{\"name\":\"neg-used\",\"vpc\":{\"id\":\"$vb\"},\"ipsec_enabled\":true,\"public_ip\":\"52.117.101.153\"}"   # taken
api_code POST /vpn_gateways "{\"name\":\"neg-gwip\",\"vpc\":{\"id\":\"$vb\"},\"ipsec_enabled\":true,\"public_ip\":\"52.117.101.145\"}"   # subnet gateway
api_code POST /vpn_gateways "{\"name\":\"neg-rt\",\"vpc\":{\"id\":\"$vb\"},\"client_enabled\":true,\"client_cidr\":\"10.9.0.0/24\",\"client_routes\":\"10.1.2.3/0\"}"
api_code POST /vpn_gateways "{\"name\":\"neg-off\",\"vpc\":{\"id\":\"$vb\"},\"ipsec_enabled\":false}"
echo "vrrp before=$before after=$(psql_c "select count(*) from vrrp_instances where deleted_at is null")"
```

然后建 vpn-a 并把后续脚本要用的状态写进去：

```bash
va=$(lv vpc_a)
gw=$(api POST /vpn_gateways "{\"name\":\"vpn-a\",\"vpc\":{\"id\":\"$va\"},\"ipsec_enabled\":true,\"client_enabled\":true,\"client_cidr\":\"10.8.0.0/24\"}" | jq -r .id)
sv gw_a $gw
wait_field /vpn_gateways/$gw .status available 180
sv vip_a $(api GET /vpn_gateways/$gw | jq -r .public_ip)
sv gwnum_a $(psql_c "select id from vpn_gateways where uuid='$gw'")
sv router_a $(psql_c "select router_id from vpn_gateways where uuid='$gw'")
sv vrrp_a $(psql_c "select vrrp_instance_id from vpn_gateways where uuid='$gw'")
for i in $(seq 1 30); do m=$(api GET /vpn_gateways/$gw | jq -r .master_hostname); [ -n "$m" ] && break; sleep 2; done; sv master_a $m
api GET /vpn_gateways/$gw | jq '{status, ha_mode, public_ip, public_ips, addable_endpoint, master_hostname, nodes, status_reason}'
```

### 检查项

- [ ] 负面用例：占用地址 `.153` → 400、网关地址 `.145` → 400（`is the gateway of subnet public-756 and cannot be assigned`，B11）、`client_routes` 为任意 `/0` → 400（规范化后是 `0.0.0.0/0`，100003）、两个能力都关 → 400（`Enable site-to-site, client access or both`）；`vrrp_instances` 行数不变；`.145` / `.158` 仍是 `allocated=false`；三节点 `router-<B>` 里没有 `ns-` 网卡，clapi 无 `set_vrrp_ip` 下发；cpgateway 用量不变
  > **回归点**（2026-09-24）：原先先建 VRRP 实例（已下发 `set_vrrp_ip.sh`）再分配公网 IP，失败只回滚数据库、节点上留网卡。⚠️ `.158` 被占以后再指定占用地址，返回的是 409（131004，`Not enough idle addresses`）
- [ ] 创建后 `pending` → **约 10 秒** `available`（09-27 实测 5 秒），`public_ip` = `52.117.101.158`，`public_ips` 一项 `vip1`，`addable_endpoint` = `vip2`，`ha_mode` = `active_standby`，`nodes` 两个节点角色 MASTER / BACKUP，`status_reason` 不出现
- [ ] `master_hostname` 在一次心跳内（≤ 20 秒）出现，且等于持 VIP 的节点
- [ ] 配额满后再建 → 429 `quota_exceeded`（cpgateway 拦下，clapi 收不到），概览页「资源使用情况」有「VPN 网关」且 5/5
- [ ] 同一 VPC 再建一个 → **409**（132005 `ErrVpnGatewayExists`，`This VPC already has a VPN gateway`）。⚠️ 配额满时先是 429，要验 409 得临时 `api PUT /resources/quota/305fcaf9-c76a-4ed5-a1ae-eddb8d8fc64e/49fce7f0-0ae3-49e1-8ffb-02c816b26bb1 '{"max_vpn_gateways":6}'`，测完改回 5（2026-09-28 核对；原写的 `ErrRouterHasVpnGateway` 是删 VPC 的错误码）
- [ ] **`error` 状态可以救回**：`update vpn_gateways set status='error', status_reason='test' where id=<gwnum_a>` 后界面显示红色提示条，PATCH 任意字段（如 `{"description":"redispatch"}`）→ 立即 `pending`、`status_reason` 清空（**PATCH 的响应本身**就不再带 `status_reason`，库里也清空）→ 两节点 `ready` 后 `available`
  > **2026-09-30 已修（`api/src/services/vpn.go` 的 `vpnApplyStatus`：响应按刚写进库的状态与原因生成），2026-10-01 已部署，按本条判定**。原记录（2026-09-28 晚观察）：库里已清空，但 PATCH 响应里的 `status_reason` 仍是旧值
- [ ] （部署后，可选）建网关时带带宽 `"inbound":100,"outbound":50`：两个 VRRP 节点的宿主机 `ext-<router>-756` 上有 `class 1:659e ... rate 100Mbit`（按目的地址），路由器 netns 的 `te-<router>-756` 上有 `class 1:659e ... rate 50Mbit`（按源地址）；WireGuard 客户端经网关下载 / 上传时速率约等于设定值
  > **2026-09-30 已修（`scripts/kvm/fip_lib.sh`、`create_lb_floating.sh`、`clear_lb_floating.sh`；编号规则见 `TC-04`「设计约定 · 带宽限速」），2026-10-01 已部署**。原先 VPN 网关（与负载均衡同一段脚本）的公网地址限速入出两个方向都从没生效
  > **回归点**（2026-09-24）：`error` 曾是终态，只能删了重建；`pending` 期间的 PATCH 也不下发。脚本侧 `create_vpn_gateway.sh` 现在失败会上报 `error`（`trap EXIT`），`create_lb_floating.sh` 的退出码不能当失败依据（没设带宽限制时最后一条 tc 清理必然非零）

### 数据库核对

```bash
psql_c "select g.id, g.name, g.status, g.master_hyper, v.id, v.vrid from vpn_gateways g join vrrp_instances v on v.id=g.vrrp_instance_id where g.deleted_at is null order by g.id"
psql_c "select device, name, hyper from interfaces where type='vrrp' and device=$(lv vrrp_a) and deleted_at is null"
psql_c "select fip_address, vpn_gateway_id, vpn_endpoint, type from floating_ips where vpn_gateway_id=$(lv gwnum_a) and deleted_at is null"
```

- [ ] VRRP 两块网卡（MASTER / BACKUP）的 `hyper` 落库，两个节点不同
- [ ] `vrid` 在 1–255 之间，**按 VRRP 子网分配**：不同 VPC 的网关都拿到 `1` 是正常的（保留的 4 个网关都是 1）
  > **回归点**：VRID 曾直接用 `vrrp_instances` 的自增主键，第 256 个实例起 keepalived 拒绝（`VRID not valid`）。现在是 `vrid` 列，`AutoUpgrade` 回填存量行
- [ ] `floating_ips` 一行：`vpn_gateway_id` 指向网关、`vpn_endpoint=vip1`、`type=vpngateway`

### 节点侧（按网关核对，不看节点总数）

```bash
m=$(lv master_a); r=$(lv router_a); n=$(lv gwnum_a); v=$(lv vrrp_a); vip=$(lv vip_a)
d=/opt/cloudland/cache/router/router-$r/vpn-$n
for h in work-01 work-02 work-03; do on_node $h "echo \$(hostname): dir=\$([ -d $d ] && echo 1 || echo 0) charon=\$(p=\$(cat $d/run/charon.pid 2>/dev/null); [ -n \"\$p\" ] && kill -0 \$p 2>/dev/null && echo 1 || echo 0) frr=\$(ps -eo args | grep -cE '^/usr/lib/frr/[a-z]+ -N vpn-$n ') keepalived=\$(ps -eo args | grep -c '[v]rrp-$v/keepalived.conf') watch=\$(cat $d/watch.pid 2>/dev/null) wg=\$(ip netns exec router-$r ip -br addr show wg-$n 2>/dev/null | awk '{print \$2, \$3}') vip=\$(ip netns exec router-$r ip -4 -o addr | grep -c ' $vip/')"; done
on_node $m "ip netns exec router-$r iptables -S INPUT | grep -E '$vip.*(500|4500|esp|51820)'; ip netns exec router-$r sysctl -n net.ipv4.conf.all.rp_filter net.ipv4.conf.all.send_redirects net.ipv4.fib_multipath_hash_policy"
```

- [ ] 主节点：VIP 挂在 `te-<router>-756`（`vip=1`），`charon=1`，`keepalived=2`（父进程 + VRRP 子进程），`watch` 有 pid，`wg-<gw>` UP 且有 `10.8.0.1/32`；没有 BGP 连接时 `frr=0`（FRR 只在有 BGP 连接时起，2026-09-28 核对）
- [ ] INPUT 放行 udp 500 / 4500、esp、udp 51820（目的地址是 VIP）
- [ ] `rp_filter=2`、`send_redirects=0`、`fib_multipath_hash_policy=1`
- [ ] 备节点：`keepalived=2`、`charon=0`、`frr=0`、没有 `watch.pid`、`vip=0`；**`wg-<gw>` 也在**（正常，配置两个节点都下发）
- [ ] 第三节点：目录里只有 `nexthops` / `nexthop_hosts` / `nonat.current` / `routes.current` / `vrrp_vlan` 这类路由状态
- [ ] 三个节点 `journalctl -k --since -10min | grep DENIED` 没有 charon / swanctl / bgpd / staticd / bfdd / wg 的新增拒绝
  > **回归点**：26.04 的 AppArmor 把这些都限制在打包路径里，症状是莫名的 `Permission denied`；部署脚本写 `/etc/apparmor.d/local/{usr.lib.ipsec.charon,usr.sbin.swanctl,bgpd,staticd,bfdd,wg}` 放行

### 涉及接口

`POST/GET/PATCH/DELETE /vpn_gateways{,/:id}`、`GET /vpn_gateways/:id/traffic`、`POST /vpn_gateways/:id/public_ips`、`GET/DELETE /vpn_gateways/:id/public_ips/:endpoint`；cpgateway 白名单 **21 条**（2026-09-28 核对，`cpgateway/src/apis/proxy_routes.go`）；审计 `vpn_gateway.create` / `update` / `delete` / `enable` / `disable` / `public_ip_add` / `public_ip_remove`

---

## VPN-02 静态站点到站点

**P1** · 脚本 `/root/vpn-e2e-2-gateways.sh`（后半段）。**2026-09-28：两网关互通部分 SKIP**（需要第二个公网地址建 vpn-b）；下面分成「要对端」「假对端可跑」「保留网关只读」三块。

```bash
# needs vpn-b (SKIP now)
api POST /vpn_gateways/<gw-a>/connections '{"name":"to-b","remote_gateway":"<vip-b>","route_mode":"static","remote_cidrs":"192.168.72.0/24","psk":"VpnTestPsk-2026"}'
api POST /vpn_gateways/<gw-b>/connections '{"name":"to-a","remote_gateway":"<vip-a>","route_mode":"static","remote_cidrs":"192.168.71.0/24","psk":"VpnTestPsk-2026"}'
# single gateway: a connection towards a peer that does not exist
api POST /vpn_gateways/$(lv gw_a)/connections '{"name":"dummy-st","remote_gateway":"198.51.100.10","route_mode":"static","remote_cidrs":"10.99.1.0/24","psk":"DummyPsk-2026"}'
```

### 检查项（要对端，当前 SKIP）

- [ ] 两条连接在 **60 秒内** `status=up`，隧道 `established_at` 有值
- [ ] 主节点 `swanctl --list-sas --uri unix://<vpn_dir>/run/charon.vici` 里 IKE_SA `ESTABLISHED`
- [ ] 探测矩阵 3×3×2 方向 **18/18**（`va* → vb*`、`vb* → va*`）
- [ ] 主节点 conntrack 里源地址是虚拟机自己的地址（没被 SNAT）
- [ ] 心跳上报：隧道与连接的 `bytes_in` / `bytes_out` 随探测增长，每条隧道的 `tunnels[].last_error` 为空（连接级已没有 `last_error` 字段）
- [ ] PSK 改错一端 → 该连接 `down`，对应隧道的 `last_error` 非空（charon 的认证失败原话）；改回来自动恢复、`last_error` 清空
- [ ] **多对网段的静态连接**（`local_cidrs` 两个网段）：主节点 `swanctl --list-sas` 有 2 个 `INSTALLED` 的 CHILD_SA；改 `remote_cidrs` 后仍是 2 个
  > **回归点**（2026-09-24）：变更后只重新发起 `--child net0`，第二对网段黑洞
- [ ] **改路由模式 / 网段后不需要手工 restart**：两端各自 PATCH 后 60 秒内主节点的 CHILD_SA 选择符等于配置（BGP 模式 `0.0.0.0/0`），BGP `Established`
  > **回归点**（2026-09-24，两条）：① `create_vpn_ipsec_conf.sh` 用 `grep -A20` 找 `start_action`，块太长永远匹配不到，terminate 后不发起；② `close_action = start` 让 charon 按被关闭 SA 的旧选择符重建，两端更新时序一交错旧选择符永远互相复活，BGP 的 /30 不在任何 SA 里。现在 `close_action = none`，守护脚本每次心跳 `vpn_ipsec_reconcile`（`<vpn_dir>/reconcile-<隧道名>` 是 60 秒限速戳）
- [ ] **流量统计**：打一段流量后连接 `bytes_in` / `bytes_out` 随之增长（可按包长精确核对）；等一次 CHILD_SA 重协商（`esp_lifetime` 设 300 可以快一点）后不清零；切主后 `established_at` 刷新、计数从 0 开始
  > **回归点**（2026-09-24）：原先永远是 0（解析不到），修好解析后也会随重协商清零；现在读隧道接口计数

### 检查项（假对端 `dummy-st`，可跑）

- [ ] 创建返回 200，连接 `status` 先 `pending`、一次心跳后 `down`；连接本身 `psk_set=true`，管理员有写权限所以还带 `psk`（见 VPN-19）；`tunnels` 一项：`slot=1`、`endpoint=vip1`、`priority=primary`、`if_id` = 隧道 ID、`psk_set=false`（没有隧道级密钥，用连接的）
- [ ] 主节点 `ipsec-<if_id>` 接口存在、MTU 1360；`swanctl.conf` 里有 `c<连接ID>-t1` 块，`remote_addrs = 198.51.100.10`、`local_ts` 是 VPC A 的子网、`remote_ts = 10.99.1.0/24`、`close_action = none`
- [ ] **路由**：主节点 `10.99.1.0/24 dev ipsec-<if_id>`（单隧道静态连接不看 SA 状态）；备节点 `via <主节点 VRRP 地址> dev ns-<vrrp vlan> onlink`；第三节点同样经主节点 VRRP 地址，且 `<vpn_dir>/nexthops` 有这一行
- [ ] **nonat**：三个节点 `ip netns exec router-<A> ipset list nonat` 都含 `10.99.1.0/24`
  > **回归点**：`set_vpn_route.sh` 曾把期望集合按行比较，刚加的 nonat 条目又被删掉（现压成一行比较）
- [ ] 一次心跳后**隧道的** `tunnels[0].last_error` 非空，是 charon 日志里这条隧道最近一次失败的原话（对不存在的对端通常是重传超时、`peer not responding` 一类），界面连接状态「未连接」；第一次上报不产生告警（新建后的第一次上报不算 up → down）
  ```bash
  api GET /vpn_gateways/$(lv gw_a)/connections/<连接 id> | jq '{status, tunnels: [.tunnels[] | {slot, status, last_error}]}'
  ```
- [ ] 文本里**没有重试计数**（不含 `(N/M)`、`retransmit N of`、`message ID N` 这类数字）：连续几次心跳文本相同、`vpn_tunnels.last_error` 不因计数变化而反复更新；最长 512 个字符；隧道 up 后清空；charon 重启（日志里 `Starting IKE charon`）之前的失败不算
  > **2026-09-30 已修（D13 / FAIL-N4：`scripts/kvm/vpn_lib.sh` 的 `vpn_tunnel_events` 读 `charon.log` 末尾 512 KiB 取每条隧道最近的失败原话，`report_vpn_status.sh` 记在 `tunnel_errors` 文件里随状态上报；`api/src/rpcs/vpn.go` 的 `vpnTunnelLastError` 写库：up 置空、截到 512、变了才写），2026-10-01 已部署，部署后按这两项判定**。
  >
  > 2026-09-30 部署后实测：对不存在的对端是 `peer not responding, trying again`，连续 3 分钟不变；刚重启 charon / 连接后的一分钟里偶尔出现一次 `giving up after 3 retransmits`（charon 同一秒先记它、再记 trying again，看护循环触发的即时上报偶尔读在两行之间），不算计数变化
  >
  > 原记录（2026-09-28 实测）：`down` 两分钟后连接 `last_error=null`、隧道 `""`，`report_vpn_status.sh` 把 `"error":""` 写死，节点从不上报 charon 的报错。**连接级的 `last_error` 早已删除**（界面读的是 `tunnels[].last_error`），原用例查连接级的是写错了
- [ ] **仅响应连接**（`remote_gateway` 空、`remote_id` 填对端身份、`initiator=false`）：主节点 swanctl 块 `remote_addrs = %any`、`remote { id = <remote_id> }`、提案与 `start_action = none` 各在其位
  > **回归点**（2026-09-24）：脚本用一次 `read -d'\n'` 接 jq 的多行输出，空的 `remote_gateway` 让后面 10 个变量整体错位，这种连接一建就是垃圾配置。现在所有脚本用 `vpn_json_read` 逐字段赋值
- [ ] **基线随 charon 启动重记**：主节点 `kill -9 $(cat <vpn_dir>/run/charon.pid)`，看护循环 / 守护脚本拉起后 `<vpn_dir>/traffic.base` 的修改时间与新 charon 启动时间同一秒，文件里有 `ipsec-<if_id>` 一行
  > **回归点**（2026-09-24）：基线原先只由切换脚本记，守护脚本先拉起 charon 时漏记，流量带上一任期的量
- [ ] **状态回调频率**：隧道状态不变时，主节点 `<vpn_dir>/status.reported`、`wg.reported` 的修改时间约每 60 秒变一次（采样 3 分钟 3–4 次），而不是每次心跳
  > **回归点**（2026-09-24）：修好流量统计后字节数每次都变，连接状态回调变成每 1–20 秒一次
- [ ] **MSS**：主节点 `ip netns exec router-<N> iptables -t mangle -S FORWARD` 有 4 条 `--tcp-flags SYN,RST SYN` 规则（`-o ipsec+` / `-o wg+` 各一条 `--clamp-mss-to-pmtu`，`-i ipsec+ --set-mss 1320`，`-i wg+ --set-mss 1350`），没有 `--syn` 的旧规则（保留网关 `router-50` 上已核对一致）。抓 SYN-ACK 的 MSS 要真实对端，SKIP
  > **回归点**（2026-09-24）：`--syn` 不匹配 SYN-ACK，对端的 MSS 原样到达虚拟机，靠路径 MTU 探测兜底
- [ ] 重启连接：`POST .../restart` 200；`?tunnel=1` 200；`?tunnel=2` → 400（`The connection has no such tunnel`）；`?tunnel=5` → 400（`tunnel must be a slot, 1 to 4`）
- [ ] 删除 `dummy-st` → 204，三节点的 `10.99.1.0/24` 路由与 nonat 条目消失，`ipsec-<if_id>` 删除，`vpn_tunnels` 里它的行被软删

### 检查项（保留网关只读，可跑）

- [ ] **流量历史**：work-02 的 `/var/lib/node_exporter/cloudland_vpn.prom` 有 `cloudland_vpn_connection_bytes_total{gateway_id="14",connection="c12",tunnel="t1",direction="in"}` 这类行（每条隧道 in / out 两行），work-03（备节点）没有 ibmx-gw 的数据行；Prometheus 查得到 `cloudland_vpn_connection_bytes_total{gateway_id="14"}`，`hostname` 是主节点
- [ ] `GET /vpn_gateways/<ibmx-gw>/traffic?start=<now-3600>&end=<now>&step=60s` → 200，61 个时间点，`connections` 是 `to-aax` / `to-ibm` / `to-ibmr-bfd`；以下返回 400：30 天配 60 秒步长（`Too many points`）、跨度 32 天、结束时间在 2 小时后、开始 / 结束取 ±9223372036854775000（`Invalid time range`）、步长 `1500ms`（`whole number of seconds`）、`by=x`；`step=1.5m` → 200、`step` 为 `90s`（2026-09-28 已在 ibmx-gw 上逐条核对）
- [ ] 打一段已知速率的流量后速率相符（bit/s，窗口 120 秒所以起初逐渐爬升）——要能在保留环境打流量，SKIP 或用户同意后在 ibmx 的虚拟机上做
- [ ] 指标文件里读不到计数的设备不出现（不导出 0）

### 涉及接口

`POST/GET/PATCH/DELETE /vpn_gateways/:id/connections{,/:conn_id}`、`POST .../connections/:conn_id/restart[?tunnel=N]`；审计 `vpn_gateway.connection_create` / `connection_update` / `connection_delete` / `connection_restart`（2026-09-28 核对：资源类型都是 `vpn_gateway`，没有 `vpn_connection.*`）

---

## VPN-03 BGP 模式与自动学路由

**P1** · 脚本 `/root/vpn-e2e-3-bgp.sh`（依赖 vpn-b，**当前 SKIP**）；单网关只做假对端的配置核对，BGP 运行状态在保留网关上只读看。

```bash
# needs vpn-b (SKIP now)
api PATCH /vpn_gateways/<gw-a>/connections/<conn-a> '{"route_mode":"bgp","remote_summary_cidrs":"192.168.72.0/23","local_asn":65010,"peer_asn":65020,"tunnel_local_ip":"169.254.100.1","tunnel_peer_ip":"169.254.100.2","bgp_keepalive":5,"bgp_hold":15}'
api PATCH /vpn_gateways/<gw-b>/connections/<conn-b> '{"route_mode":"bgp","remote_summary_cidrs":"192.168.71.0/24","local_asn":65020,"peer_asn":65010,"tunnel_local_ip":"169.254.100.2","tunnel_peer_ip":"169.254.100.1","bgp_keepalive":5,"bgp_hold":15}'
# single gateway: BGP towards a peer that does not exist
api POST /vpn_gateways/$(lv gw_a)/connections '{"name":"dummy-bgp","remote_gateway":"198.51.100.20","route_mode":"bgp","psk":"DummyPsk-2026","bgp_password":"Dummy-Bgp-1","remote_summary_cidrs":"10.99.8.0/22","local_asn":65010,"peer_asn":65020,"tunnel_local_ip":"169.254.120.1","tunnel_peer_ip":"169.254.120.2"}'
```

### 检查项（要对端，当前 SKIP）

- [ ] 在线切换后隧道自动重建，`status` 回到 `up`
  > **回归点**：静态 → BGP 切换曾只 `swanctl --load-all`，CHILD_SA 保留旧的窄流量选择符，BGP 报文过不去；现在变了的连接块 `--terminate` 再 `--initiate`
- [ ] **约 20 秒**内隧道 `bgp.state=Established`，`bgp.accepted` 含对端明细 `192.168.72.0/24`
- [ ] 主节点路由表：明细 `192.168.72.0/24 via 169.254.100.2 dev ipsec-<id> proto bgp`，汇总 `192.168.72.0/23` 是 `blackhole ... proto static`（staticd 的）
  > **回归点**：汇总黑洞曾是内核路由，与 BGP 学到的同前缀路由并存时 zebra 永远不装 BGP 明细。现在黑洞由 staticd 持有；`vpn_apply_routes` 看到 `Known via "static"` 就不再装内核黑洞
- [ ] 备节点 / 第三节点只有 **汇总** `192.168.72.0/23`，没有明细
- [ ] 探测矩阵 18/18
- [ ] VPC B 加子网 `192.168.73.0/24` + 虚拟机 `vb4` 后，**不改任何连接**，A 在 120 秒内学到（`bgp.accepted` 含它），`va1..3 → vb4` 4/4
- [ ] B 的 `effective_local_cidrs` 含新子网，`bgp.advertised` 里也有

### 检查项（假对端 `dummy-bgp`，可跑）

- [ ] 主节点 FRR **5 个**守护进程都在：`ps -eo args | grep -cE '^/usr/lib/frr/[a-z]+ -N vpn-<gw> '` = 5（zebra / mgmtd / staticd / bfdd / bgpd；2026-09-28 核对，原写 4 个是 R1 之前）
- [ ] `/etc/frr/vpn-<gw>/frr.conf` 有 `ip route 10.99.8.0/22 Null0 250`、prefix-list、`neighbor 169.254.120.2 remote-as 65020`、口令；`ipsec-<if_id>` 上配了 `169.254.120.1/30`；INPUT 放行 `-s 169.254.120.2 -d 169.254.120.1 --dport 179`
- [ ] 连接返回 `bgp_password_set=true`，有写权限时带 `bgp_password`；一次心跳后隧道 `bgp.state` 不是 `Established`（`Active` / `Connect` 之类），`bfd_state` 为空（没开 BFD）
- [ ] `/etc/frr/frr.conf`（默认那份）**没有被覆盖**：测试前后 `md5sum` 一致
  > **回归点**：`frr-reload.py` 不带 `--confdir /etc/frr/vpn-<gw>` 时，reload 里的 `write` 会把默认 `/etc/frr/frr.conf` 覆盖掉
- [ ] `frr.conf` 两个 VRRP 节点上都有（配置两边都下发），FRR 进程只在主节点；删掉 `dummy-bgp`（网关上不剩 BGP 连接）后，主节点 `vpn-<gw>` 的 FRR 进程全部退出、两个节点的 `/etc/frr/vpn-<gw>/` 都删除；`/var/run/frr/vpn-<gw>`（存在时）是 `drwxr-xr-x frr frr`
- [ ] 主备两台 `journalctl -k | grep DENIED | grep -E 'bgpd|bfdd'` 只剩 `task/<tid>/comm`（已放行）或为空

### 检查项（保留网关只读）

- [ ] work-02：`vtysh -N vpn-14 -c 'show bgp ipv4 unicast summary'` 有 ibmx-gw 的 `to-ibmr-bfd` 两个邻居（169.254.110.x），都 Established；接口返回这两条隧道 `bgp.state=Established`、`bfd_state=up`
- [ ] work-03（`ibmx-gw` / `ibmr-gw` 的备节点）上没有 `-N vpn-14` / `-N vpn-15` 的 FRR 进程；work-02 上 `/var/run/frr/vpn-14..16` 都是 `drwxr-xr-x frr frr`（2026-09-28 已核对）

---

## VPN-04 主备切换与切回

**P1** · 原脚本 `/root/vpn-e2e-4-failover.sh` / `-4b-failback.sh` 探测 VPC A → VPC B，**单网关时不适用**；改用 WireGuard 客户端（VPN-05 起好的 work-03 宿主机）探测 VPC A 的虚拟机。

```bash
# on work-01; the WireGuard client of VPN-05 is up on work-03
m=$(api GET /vpn_gateways/$(lv gw_a) | jq -r .master_hostname); r=$(lv router_a); n=$(lv gwnum_a); v=$(lv vrrp_a)
peer=$(api GET /vpn_gateways/$(lv gw_a) | jq -r ".nodes[] | select(.hostname != \"$m\") | .hostname")
on_node work-03 "nohup ping -D -O -i 0.2 -W 1 -c 600 $(vm_ip va1) >/root/vpn-fo-ping.log 2>&1 &"
sleep 10; since=$(date -u +%Y-%m-%dT%H:%M:%S); t0=$(date +%s)
on_node $m "pkill -9 -f '[v]rrp-$v/keepalived.conf'"
for i in $(seq 1 60); do [ "$(api GET /vpn_gateways/$(lv gw_a) | jq -r .master_hostname)" = "$peer" ] && break; sleep 1; done
echo "clapi master after $(( $(date +%s) - t0 )) s"
docker logs --since "$since" cloudland-clapi 2>&1 | grep -E "VPN gateway $n[: ].*(takes over|master changed|split brain)"
sv master_a $peer
```

### 实测基线

| 项 | 2026-09-23（R0 之前，VPC A → B 探测） | 2026-09-27（单节点补备后 `kill -9`，zone-test） |
|---|---|---|
| VIP 切换 | 3 秒内 | 2 秒 |
| 第三节点间 TCP 探测中断 | 0–26 秒 | 没测 |
| clapi 记录的主节点切换 | 28 秒 | 没测（IBM 对端时 ≤ 5 秒，VPN-13） |

### 检查项

- [ ] 新主：VIP 在、charon 存活、看护循环在（有 BGP 连接时 FRR 5 个）；旧主：VIP 0、charon 0、FRR 0、看护循环已退出，keepalived 被守护脚本拉起（`keepalived=2`）并处于 BACKUP、不抢主
  > 由 keepalived notify 拉起的看护循环，父进程是 notify 进程而不是 1（2026-09-28 核对）；数看护循环按 `watch.pid` 核对，别按 `ppid==1` 筛。单网关时 WireGuard 客户端放在**不是网关节点**的宿主机（如 work-01）上最干净
- [ ] clapi **5 秒内**切主：日志 `node Y takes over from node X at once (the old master released the address)`（或先一条 `possible split brain, keeping routes`、紧接着 `node Y takes over from node X (the old master released the address)`），然后 `master changed from X to Y`（2026-09-28 核对：R0 起有让位证据就不等 30 秒窗口；原写的「28 秒、一定先有分脑日志」是 R0 之前的行为）
  > **回归点**：接管后主节点身份曾只按 20 秒周期重发，被拒一次要再等 20–40 秒，切换 50–62 秒；现在接管后第一分钟每次心跳都发，且 keepalived 父进程被杀时看护循环会摘地址、上报 `vpn_backup`（回归点 33）
- [ ] 第三节点 `<vpn_dir>/nexthops` 在 clapi 切主后把新主的 VRRP 地址排第一，路由改经新主
- [ ] WireGuard 客户端探测中断 ≤ 25 秒（WireGuard 要等客户端重新握手，约 15 秒，**新增/未跑过**，待确认）；`/root/vpn-fo-ping.log` 里按连续丢包算中断窗口
- [ ] 新主的 `<vpn_dir>/notify.log`：`master start` → `master set_route_table done (rc=0)` → `routes applied` → `charon started (rc=0)` → `frr started (rc=0)`（没有 BGP 连接时也记这一行，函数直接返回）→ `watch loop started` → `done` **2 秒内**；clapi 切主后的 `sync` 调用（前两行记 `sync start` / `sync lock acquired`，拿到锁后按是否持有 VIP 改判，之后各行记 `master`）的 `set_route_table done (rc=0)` 也在 1 秒内
  > **回归点**（2026-09-24）：FRR 在 keepalived 上下文起不来（umask → 运行目录 0600）、`sync` 路径在宿主机 netns 跑 `set_route_table.sh` 白等 60 秒，详见 `TC-14` HA-08。`kill -9` 场景下两者被 `check_vpn_process.sh` 和 cloudlet 上下文掩盖，只把中断拉长到 20 多秒；真实重启才暴露成 80 秒
- [ ] 再做一次切回（对新主重复上面的命令），结果同上；`rb-lb` 全程 10/10；保留网关的主节点不变
- [ ] **正常退出**（**新增/未跑过**，CLAUDE.md G25）：对当前主节点 `kill -TERM $(cat /opt/cloudland/cache/router/router-<r>/vrrp-<v>/keepalived.pid)`，记录备节点拿到 VIP 的时间。预期 ≤ 1 秒（keepalived 退出时发优先级 0 的通告），若约 3 秒说明没发（待确认，方案 §17）；旧主被守护脚本拉起后是 BACKUP
- [ ] 真实重启主节点见 `TC-14` HA-08（WireGuard 客户端在切换后对新主可用已在那里验证）

---

## VPN-05 WireGuard 客户端

**P1** · 脚本 `/root/vpn-e2e-5-client.sh`（客户端在 work-03 宿主机的 default netns 里 `wg-quick up`；需要 VPN-01 写好的 `gw_a`）

```bash
api POST /vpn_gateways/<gw-a>/clients '{"name":"laptop-1","preshared_key":true}'      # platform generates the key pair
api GET  /vpn_gateways/<gw-a>/clients/<cid>/config                                    # template, no private key
```

### 检查项

- [ ] 创建响应带 `private_key`（44 字符）与完整 `config`（`[Interface]` / `[Peer]`，`Endpoint = 52.117.101.158:51820`，`AllowedIPs` = VPC A 子网，`PersistentKeepalive = 25`，`PresharedKey`）
- [ ] `GET .../config` 的文本里是 `PrivateKey = <your private key>` 占位，**没有**私钥；有写权限时 `PresharedKey` 是真实值（观察者见 VPN-19）
- [ ] 客户端 `wg-quick up` 后 3 秒内握手；到 VPC A 三台虚拟机 **3/3**；`va1 → 10.8.0.2:22`（work-03 宿主机 sshd）反向可达
- [ ] 一次心跳后 `GET .../clients/<cid>` 的 `last_handshake_at` 非空、`bytes_in/out` > 0
- [ ] 主节点 `wg show wg-<gw>` 里 peer 的 allowed ips = `10.8.0.2/32`（备节点的 `wg-<gw>` 里也有这个 peer，但没有握手）
- [ ] **并发创建两个客户端**（两条 POST 同时发，照抄 `vpn-e2e-9-fixes.sh` 的 F4）：地址不同（`.3` / `.4`），主节点 `wg.conf` 三个 `[Peer]`
  > **回归点**（2026-09-24）：`Set("gorm:query_option","FOR UPDATE")` 在 GORM v2 里被忽略，地址分配无锁；现在 `clause.Locking`
- [ ] 建网关、改网关时 `client_routes` 含 `0.0.0.0/0`（或任何 `x.x.x.x/0`）→ 400（100003，`full-tunnel mode is not supported`），配置不变（全流量模式按设计拒绝；它是网关的字段，不是客户端的）；**2026-09-27 起生效，已部署实测**。在 vpn-a 上做：`api_code PATCH /vpn_gateways/$(lv gw_a) '{"client_routes":"0.0.0.0/0"}'`，前后 `client_routes` 一致。界面上建 / 编辑网关直接在底栏提示、不发请求（`gw-exclude.js`，副本在 work-01 `/root/console-test/`，在本机对 5173 跑）
- [ ] 用户自带公钥（`public_key` 传入）时响应**没有** `private_key`，`config` 里是占位；非法公钥 → 400；重复公钥 → 400
- [ ] **客户端与远端站点不互通**（单网关用假对端验证，2026-09-28 新写法）：在 vpn-a 上建静态连接 `iso` 到 `198.51.100.30`、`remote_cidrs` `10.99.2.0/24`；把网关 `client_routes` 改成 `192.168.71.0/24,10.99.2.0/24`，客户端重新下载配置 `wg-quick up`；客户端 `ping -c 5 10.99.2.1` 全部失败，主节点 `ip netns exec router-<r> iptables -vnL FORWARD` 里丢包计数增加 5 左右——排在最前的是按地址池的 `-s 10.8.0.0/24 -o ipsec+ -j DROP`，所以通常是它在涨，`-i wg+ -o ipsec+ -j DROP` 不涨也正常（两条合计增加即可；2026-09-28 核对规则顺序）——`ipsec-<if_id>` 的 TX 计数不变；测完改回 `client_routes`、删连接。有 vpn-b 时按原脚本 `/root/vpn-e2e-10-isolate.sh` 做双向（VPC B 虚拟机探测 0/3、反向 `-i ipsec+ -o wg+ DROP` 计数增加）
  > **回归点**（2026-09-24）：客户端与站点之间禁止互通，`advertise_client_cidr` 已删除；两个节点的路由器 netns 都要有这两条规则，节点重启恢复后也要在
- [ ] 两个 VRRP 节点的 `FORWARD` 最前面 4 条依次是按地址池的 `-d 10.8.0.0/24 -i ipsec+ -j DROP`、`-s 10.8.0.0/24 -o ipsec+ -j DROP`，再是按接口的 `-i ipsec+ -o wg+ -j DROP`、`-i wg+ -o ipsec+ -j DROP`（`ibmx-gw` 的 `router-50` 2026-09-28 只读核对一致）
- [ ] 手工删掉主节点上的两条隔离规则，**一次心跳内**（≤ 30 秒；心跳间隔名义 1–20 秒随机，2026-09-28 实测 13–24 秒一次、那次补回用了 29 秒，更早一次 4 秒）被守护脚本补回，仍在 `FORWARD` 最前面、没有重复；在网关所在节点对一个不存在的网关 id 跑 `/opt/cloudland/scripts/backend/clear_vpn_gateway.sh <router> 99999`，现有网关的隔离与 MSS 规则都不被删
  > **回归点**（2026-09-24）：规则原先只插一次，删旧建新的时序交错或插入失败都会让隔离静默失效

---

## VPN-06 客户端停用 / 启用

**P1** · 脚本 `/root/vpn-e2e-5b-disable.sh`（需要状态里有 `master_a` / `router_a` / `gwnum_a`）

### 检查项

- [ ] `PATCH {"enabled":false}` 后 **4 秒内**主节点 `wg show wg-<gw> peers` 少一个，客户端探测失败
- [ ] `PATCH {"enabled":true}` 后 peer 回来，客户端 **≤ 20 秒**恢复（实测 13 秒，是客户端重新握手的时间）
- [ ] 删除客户端后地址释放，再建一个拿到同一地址（地址池从第 3 个地址起取最小空闲，`.1` 是网关）
- [ ] **多个客户端同时在线**（**新增/未跑过**，CLAUDE.md G28）：第二个客户端 `laptop-2` 放在 work-02 宿主机上 `wg-quick up`（work-02 也装了 wireguard-tools），两个客户端各自每 0.2 秒 ping `va1`；期间停用 / 启用 / 新建 / 删除**另一个**客户端 3 次，没被改动的客户端 0 丢包（`wg syncconf` 不断开未变动的 peer）；测完两台宿主机都 `wg-quick down` 并删掉配置文件

---

## VPN-07 界面冒烟

**P1** · 按约定在**本机** `npm run dev`（5173，`/api` 代理到 work-01）上用 Playwright 跑，**每个脚本都要按 pathname 兜底拦截写请求**（登录除外）。work-01 上 nginx 是 09-27 的 `facff649`，VPN 界面与本地一致，也可以在 work-01 的 Playwright 容器里对 `https://127.0.0.1` 跑只读脚本。

```bash
# local, in a scratch directory
npm i playwright@1.55.0 && npx playwright install chromium
scp work-01:/root/console-test/{gw-exclude,vpn-peer-config,vpn-conn-table-fit,vpntraffic2,vpntraffic3,vpn-traffic-view}.js .
ADMIN_PASSWORD=... GW_ID=a53b2307-e906-429d-9623-bc9be0c7be10 node vpn-peer-config.js
```

> ⚠️ `vpn-ui.js`（09-23）已过时：它要列表里有 `vpn-a` / `vpn-b`、详情页 4 个标签、vpn-a 的 BGP 连接 `Established`；现在详情页有 **5 个标签**，单网关也没有 BGP 对端。要用就先把这三处改掉（BGP 那项改看 `ibmx-gw` 的 `to-ibmr-bfd`）。`vpn-secrets-ui.js` 同样过时，见 VPN-19。

### 检查项

- [ ] 侧边栏「网络」下有「VPN 网关」
- [ ] **列表页首次进入就有数据**：至少 5 行（保留的 `a1` / `ibmx-gw` / `ibmr-gw` / `aax-gw` 加测试网关），状态「可用」，有高可用模式列（`aax-gw` 为双活）、公网 IP 列（`ibmr-gw` 两个地址各带「公网地址 1 / 2」，`aax-gw` 带「节点 1 (work-02)」「节点 2 (work-03)」）、主节点列（1280 宽以上显示；`aax-gw` 为 `-`）
  > **回归点**：`VpnGateways.vue` 的 `onMounted` 曾只加载 VPC 列表、没调 `fetchGateways()`（列表页的 VPC 筛选 2026-09-24 已去掉：每个 VPC 最多一个网关，筛选结果只有 0 或 1 条；VPC 列表只用于创建弹窗），`useListQuery` 又只在依赖变化时重载，页面永远显示「未发现 VPN 网关」。typecheck / lint 抓不到，只有真跑才发现
- [ ] 详情页标题区约 43px、**五个标签**（概览 / 站点连接 N / 客户端 N / 监控 / 操作记录；2026-09-28 核对，监控标签是 09-24 加的）
- [ ] 站点连接标签（看 `ibmx-gw`）：`to-ibmr-bfd` 路由方式 `BGP`、状态「已连接」、隧道行是状态圆点加 BGP 标签（悬停显示 BGP / BFD 状态），`to-aax` 带 ECMP 标签、不显示主隧道 / 备隧道；展开行两张隧道卡片
- [ ] 客户端标签（看 vpn-a）：`laptop-1`、`10.8.0.2`、握手时间
- [ ] 操作记录标签：有本轮的创建 / 更新记录，文案是「为 VPN 网关 X 添加了站点连接」这类（动作 `vpn_gateway.*`）
- [ ] 概览页「资源使用情况」有「VPN 网关」进度条（测试网关在时 5/5 标红）
- [ ] **创建弹窗**：已有网关的 VPC 标注「已有 VPN 网关」且不可选，默认选中第一个没有网关的 VPC；全都有网关时下拉显示「没有可用的 VPC」并给出提示
- [ ] **创建弹窗的可用区与公网 IP 是下拉**：可用区默认「自动（默认可用区）」，默认可用区带「· 默认」；公网 IP 在选公网子网前禁用，选后列出空闲地址且**不含子网网关地址 `.145`**（当前应只有 `.158`），子网改回「自动选择」时地址清空；请求体带 `zone` / `public_subnet` / `public_ip`；主备 / 双活二选一，双活时显示占用几个公网 IP。负载均衡的创建弹窗可用区同样是下拉
  > **回归点**（2026-09-24）：`GET /addresses/:subnet` 会列出子网网关那一行（未分配），clapi 指定地址分配时又不排除网关，照抄弹性 IP 页的过滤会把上游网关地址给出去（后端 09-27 也拦了，见回归点 54）
- [ ] **「监控」标签**：进详情页不请求 `/traffic`，切到「监控」才请求；一次一张整行宽的图：只开站点到站点的网关没有切换按钮，两种都开时图上方有「站点连接 / 客户端」切换且切换不再请求；站点视图有「按隧道」开关，勾上后请求带 `by=tunnel`、每条隧道一条线，多条连接时可以只看一条；每个对象入 / 出两条线颜色不同，右侧显示当前速率；没有数据时只有一行提示；客户端 ≤ 4 个逐个画线、更多时画合计；切 6 小时后请求的 `step=5m`；手机宽度无横向溢出；先点 30 天再立刻点 1 小时，30 天的响应晚到时不覆盖图表、高亮仍是 1 小时；末尾几个点没有数据时不显示当前速率（`vpntraffic2.js`、`vpntraffic3.js` 用浏览器内改写返回覆盖只开站点到站点、两种都开、多客户端、乱序响应与过时速率，只读；需要环境变量 `GW_ID`（带站点连接的网关）与 `ADMIN_PASSWORD`；另有 `vpn-traffic-view.js`）
- [ ] **弹窗报错在底栏**（创建 / 编辑网关、连接、客户端）：两个能力都关、后端 409 等报错显示在按钮左侧，窗口 800px 高也不用滚动就能看到；「该 VPC 已有网关」是中文；可用区节点不够（132009）显示「所选可用区的可用计算节点不够…」
  > **回归点**（2026-09-24）：报错原先放在表单最底部，长表单要往下滚才看得到，默认又选中了已有网关的 VPC，用户点「创建」后以为没有反应（后端其实回了 4 次 409）
- [ ] **站点连接表格不横向滚动**：1920、1600、1440、1366、1280、1024 六档操作列都在屏幕内，展开行两张隧道卡片完整；1600 以下没有流量列、1440 以下没有远端网段列、1280 以下路由方式挪到名称下面、隧道行可以换行（`vpn-conn-table-fit.js`，本机对 5173 跑；`ibmx-gw` 与 `aax-gw`。1280 与 1024 只余约 2px，连接名很长时可能溢出）
- [ ] **对端配置弹窗**（连接操作列的「对端配置」）：静态双隧道的 `to-ibm` 列出网段对、主隧道是哪条、「对端较小地址」建议，主隧道没对着较小地址时有黄色警告；BGP 的 `to-ibmr-bfd` 列出两端 ASN、隧道内 /30 地址、计时器、BFD、接受范围，且**不出现**「较小地址」建议；双活负载分担的 `to-ibmx-aa` 标「负载分担」、带节点名、有双活提示、没有「主隧道是隧道」；管理员（写权限）时隧道卡片带预共享密钥（打码，可显示 / 复制，隧道自己的密钥优先）、BGP 口令同理，顶部黄色提示含明文密钥，复制与下载的文本是明文；「复制全部」与「下载 .txt」内容一致（Windows 剪贴板换行是 CRLF，比较前归一）；英文界面不混中文标点；390 宽下隧道卡片与底栏按钮都在弹窗内（`vpn-peer-config.js`，本机对 5173 跑，只读，需要 `ADMIN_PASSWORD`）。观察者一侧见 VPN-19
- [ ] 文档站：`https://169.61.110.83/docs/guide/vpn-gateways.html` 200（09-27 随 nginx 发布，CLAUDE.md G30）
- [ ] 0 页面错误；0 个被拦截的写请求（只读脚本）
- [ ] 截图放在本机 Playwright 目录（原 work-01 `/root/console-test/shots/vpn-*.png` 是容器里跑时的位置）

---

## VPN-08 配额、审计与告警

**P2** · 配额与审计可跑；**新告警 SKIP**（要真实对端让隧道 up → down），历史告警只读核对

- [ ] cpgateway 用量 `org_resource_consumptions.vpn_gateways` 随建 / 删增减（Admin 4 → 5 → 4）；`public_ips` 同步（9 → 10 → 9）；登录对账后仍正确
- [ ] 删网关时连带释放它的公网 IP 用量（按 `GET` 到的 `floating_ips` 个数；双地址网关释放 2 个）
- [ ] `GET /activities` 里有 `vpn_gateway.create`、`vpn_gateway.connection_create` / `connection_update` / `connection_delete`、`vpn_gateway.client_create` / `client_update` / `client_delete`、`vpn_gateway.enable` / `disable`（VPN-11）、`vpn_gateway.delete`（2026-09-28 核对：没有 `vpn_connection.*` / `vpn_client.*` 这类动作名）
- [ ] 连接从 `up` 变 `down`（拔掉对端 / 改错 PSK）后 `alarm_events` 多一条 `fingerprint=vpn-connection-<连接ID>-<unix>`、`alert_name=VpnConnectionDown`、`labels` 含 `severity=critical`、`source=vpn`、`vpn_gateway`、`vpn_connection`，恢复后 `resolved`；多隧道连接断一条是 `vpn-tunnel-<隧道ID>-<unix>` / `VpnTunnelDown` / `warning`；有通知渠道时 `alarm_delivery_logs` 有发送流水 —— **要对端，当前 SKIP**
- [ ] 只读：`select alert_name, status, count(*) from alarm_events where alert_name like 'Vpn%' group by 1,2`（2026-09-28：`VpnConnectionDown` resolved 26、`VpnTunnelDown` resolved 16，没有 firing）；抽一条看 `labels` 字段齐全
- [ ] VPN 告警只推给**网关所属组织**自己启用的渠道：`alarm_delivery_logs` 里 VPN 事件的 `channel_name` 都属于该组织（Admin 的网关只有 `ops-webhook` / `oncall-backup-webhook`）；非 Admin 组织的网关（做过 VPN-19 的外部组织时）只推给它自己的渠道
  > **2026-09-30 已修（D2：渠道镜像按组织 UUID 解析本地 ID，`api/src/services/vpn_notify.go` 用 `EnabledChannelsOfOrg`，组织 ID ≤ 0 时不推），2026-10-01 已部署，按本条判定**。原先按 `org_id = gateway.Owner` 取渠道，而镜像里存的是控制面的组织 ID，会推给「clapi 本地 ID 恰好等于别的组织控制面 ID」的组织。细节见 `TC-10` ALM-07
- [ ] 系统设置 `DEFAULT_VPN_GATEWAYS`（默认 1）：`GET /system/settings` 里有、范围 0–100000；`PUT` 负数或小数 → 400（不改值）

---

## VPN-09 接口校验（负面用例）

**P2** · 2026-09-28 按 `apis/vpn.go`、`services/vpn*.go` 逐条核对并写成可执行的请求，**未跑过**。都在 vpn-a 上做（网关 `available`、开着站点到站点，VPC A 是 192.168.71.0/24），请求写进 work-01 上的脚本，用 `api_code` 看 `CODE` 与 `error_code`。建网关的几条负面用例在 VPN-01 里（要在建 vpn-a 之前做）。

```bash
g=$(lv gw_a)
c() { api_code POST /vpn_gateways/$g/connections "$1" | cut -c1-160; }
c '{"name":"n1","remote_gateway":"198.51.100.40","route_mode":"static","remote_cidrs":"192.168.71.0/25","psk":"NegTestPsk-01"}'   # overlaps the VPC
c '{"name":"n2","remote_gateway":"198.51.100.40","route_mode":"static","remote_cidrs":"0.0.0.0/0","psk":"NegTestPsk-01"}'
c '{"name":"n3","remote_gateway":"198.51.100.40","route_mode":"static","remote_cidrs":"192.168.196.0/24","psk":"NegTestPsk-01"}' # VRRP subnet
c '{"name":"ok1","remote_gateway":"198.51.100.41","route_mode":"static","remote_cidrs":"10.99.3.0/24","psk":"NegTestPsk-01"}'    # 200, kept for n4
c '{"name":"n4","remote_gateway":"198.51.100.42","route_mode":"static","remote_cidrs":"10.99.3.0/25","psk":"NegTestPsk-01"}'    # overlaps ok1
```

- [ ] `remote_cidrs` 与本 VPC 子网重叠 → 400（132032 `ErrVpnCidrConflict`）；`0.0.0.0/0` → 400（132032）；与 VRRP 子网 `192.168.196.0/24` 或 `169.0.0.0/8` 重叠 → 400（132032）
- [ ] 两条连接的对端网段重叠 → 400（132032）
- [ ] `bgp` 模式缺 `remote_summary_cidrs` / `local_asn` / `peer_asn` → 400；`local_asn` 等于 `peer_asn` → 400（`must differ (eBGP)`）；`bgp_hold` 小于 3 倍 `bgp_keepalive` → 400；同一网关两条 BGP 连接的 `local_asn` 不同 → 400
- [ ] `tunnel_local_ip` 与 `tunnel_peer_ip` 不在同一 /30 → 400；两者相同 → 400；落在 VPC 子网里 → 400（132032）；与别的连接的隧道地址重复 → 400
- [ ] `psk` 缺失 → 400（`psk is required`）；7 个字符 / 含引号或反斜杠 → 400；`bgp_password` 含空格 → 400；`ike_proposal` 为 `aes256-md5` → 400；`remote_id` 含空格 → 400
- [ ] 重名连接 → 400（`A connection with this name already exists`）
- [ ] 仅响应隧道（不填 `remote_gateway`）但 `initiator` 为 true（默认）→ 400；不填 `remote_id` → 400
- [ ] `tunnels` 5 项 → 400（绑定校验）；单地址网关用 `"endpoint":"vip2"` → 400（`The gateway has no public address vip2`）；两个 `primary` → 400；两条隧道本端与对端地址都相同 → 400
- [ ] `client_cidr` 与 VPC 子网 / 对端网段重叠：没有客户端时 `PATCH {"client_cidr":"192.168.71.0/24"}` → 400（132032）；有客户端时改地址池 → 409（132007 `Delete the clients before changing the client pool`）
- [ ] 有连接时 `PATCH {"ipsec_enabled":false}` → 409（132007）；有客户端时 `PATCH {"client_enabled":false}` → 409（132007）；两个能力都关 → 400
- [ ] 客户端 VPN 关着时建客户端 → 400（`Client access is disabled on this gateway`）
- [ ] **删除有网关的 VPC**：`DELETE /vpcs/$(lv vpc_a)` → **409**（132033 `Delete the VPN gateway first`，界面文案「该 VPC 有 VPN 网关，请先删除网关。」），网关与 VPC 都还在，没有下发 `clear_local_router.sh`
  > **2026-09-30 已修（D14：`api/src/services/router.go` 把 VPN 网关检查挪到浮动 IP 检查之前），2026-10-01 已部署，按本条判定**。原记录：实际是 400（131307 `There are associated floating ips`），网关的公网地址带着 `router_id`，先被浮动 IP 检查拦下
- [ ] 删除被 VRRP 实例占用的 VRRP 子网 → 400（131006 `Some addresses of this subnet are still in use`）。只对测试网关的 VRRP 子网做（`type=vrrp`、名字以 `vpn-a-` 开头、`router_id` 是 VPC A），别碰保留的 `rb-lb-*` / `ibmx-gw-*` 等
- [ ] 非本组织的网关 → 404（132001）；只读成员写操作 → 403（100004）。其他组织与只读账号的准备见 VPN-19；部署 2026-09-30 的修复之前，新组织在 clapi 里可能被解析成组织 ID 0（`runs/2026-09-28-7adc07d0+补测.md` FAIL-1），要先确认 `docker logs cloudland-clapi | grep "UpsertOrgByUUID: ok id="` 不是 0，否则「看不到别的组织的网关」这条的结论不可信；部署后不应再出现 0
- [ ] `.env` 没有 `VPN_SECRET_KEY` 时建网关 / 连接 / 客户端 → 503（132031）—— 要改 `.env` 重启 clapi，**SKIP**
- [ ] 测完删掉 `ok1` 等测试连接

---

## VPN-11 网关停用 / 启用

**P1** · 脚本 `/root/vpn-e2e-11-disable.sh`（依赖 vpn-b 与 BGP 连接，**单网关时手工做**，站点与 BGP 部分 SKIP）。单网关时先在 vpn-a 上留一条假对端静态连接（`dummy-st`）和一个在线的 WireGuard 客户端。

```bash
api PATCH /vpn_gateways/$(lv gw_a) '{"enabled":false}'
api PATCH /vpn_gateways/$(lv gw_a) '{"enabled":true}'
```

### 检查项

- [ ] 停用：响应 `enabled=false`、`status` 仍是 `available`，连接与隧道 `status=disabled`，隧道 `established_at`、`bgp`、`bfd_state` 清空
- [ ] 两个 VRRP 节点 2 秒内：网关目录有 `disabled` 文件，charon 不在（有 BGP 时 FRR 也不在），`wg-<gw>` DOWN，公网地址的 INPUT 放行 0 条，对端网段与客户端地址池都是 `blackhole`；`notify.log` 有 `disabled: routes blackholed, processes stopped`；VIP 仍在主节点（公网地址保留）
- [ ] **主节点同样 2 秒内生效**，哪怕网关上挂着几条连向不存在对端的连接（`dummy-st` 等）：`notify.log` 里 `disabled:` 一行与请求时间相差 ≤ 2 秒；守护脚本在锁里只做检查、**不等对端**，一轮持锁不到 1 秒（results.md 实测 0.67 秒，原来 12.97 秒以上）
  ```bash
  # on the master: how long the watchdog holds the lb lock, and who initiates tunnels
  ssh work-0X "ps -eo pid,etimes,args | grep -E '[v]pn_initiate.sh|[s]wanctl --initiate'; ls -l <vpn_dir>/initiate-*.lock <vpn_dir>/reconcile.lock 2>/dev/null"
  ```
- [ ] 隧道由后台的 `vpn_initiate.sh`（`setsid`，每个网关一个 `reconcile`、每条隧道一把 `initiate-<隧道名>.lock`）发起，**每条隧道 60 秒最多发起一次**；`swanctl --list-sas` 有 5 秒超时。发起前在锁内重新核对：已经 `INSTALLED` 的子 SA、正在 `CREATED` / `CONNECTING` 的 IKE SA 都跳过，`swanctl --list-sas` 里每条隧道最多一个 CHILD_SA（不重复）
- [ ] 改连接配置（`create_vpn_ipsec_conf.sh`）时，`--terminate` 在锁内、重新发起在锁外（后台 `vpn_initiate.sh initiate`），配置下发请求很快返回；同时守护脚本的 reconcile 在跑也不会建出第二个 CHILD_SA
  > **2026-09-30 已修（D8 / FAIL-N5：`scripts/kvm/check_vpn_process.sh` 持锁不发起；新增 `scripts/kvm/vpn_initiate.sh`；`vpn_lib.sh` 的 `vpn_tunnel_reconcile` 锁内判断、`vpn_tunnel_run` 跳过已安装的子 SA 与正在建立的 IKE SA；`create_vpn_ipsec_conf.sh` 的 terminate 在锁内、发起在锁外），2026-10-01 已部署，部署后按这三项判定**。
  >
  > 原记录（2026-09-28 实测：主节点晚了 30 秒）：网关上有连向不存在对端的连接时，守护脚本 `check_vpn_process.sh` 在 `lb_process.lock` 里对每条 down 隧道串行 `swanctl --initiate --timeout 20`（实测一次持锁 ≥59 秒），主节点的 `create_vpn_gateway.sh` 在同一把锁上等，cloudlet 串行队列随之停住；同节点的 LB 守护整轮跳过（CLAUDE.md 待办 G24 的实证）
- [ ] 流量：客户端不通（有 vpn-b 时 VPC A↔B 探测 0/6）
- [ ] 45 秒（两次心跳）后守护脚本、看护循环没有拉起任何进程，连接仍是 `disabled`（上报被忽略）
- [ ] 停用期间重启连接 → 400（132008）；两个能力都关 → 仍被拒绝（400）；停用期间可以改配置、加连接和客户端，新连接直接是 `disabled`
- [ ] 停用期间 `kill -9` 主节点 keepalived：新主节点 notify 走停用分支、不起进程，旧主拉起成 BACKUP 后同样；客户端仍不通
- [ ] 启用：连接回到 `pending`；新主节点 2 秒内起 charon（有 BGP 时 FRR）；客户端 **≤ 30 秒**恢复（实测 20 秒）；有真实对端时连接回到 `up` / `Established`
- [ ] 审计：`vpn_gateway.disable` / `vpn_gateway.enable`（纯启停请求才用这两个动作名，夹带别的字段记 `vpn_gateway.update`）
- [ ] 界面：操作菜单「停用」有确认弹窗；停用后标题 / 列表显示「已停用」、顶部横幅带「启用」、连接「已停用」、重启按钮禁用；编辑弹窗两个能力都关、有连接时关站点到站点都给中文提示
  > **回归点**（2026-09-24）：第一版停用只清了 `bgp_state` 列，界面读的是 `bgp_status` 快照，停用期间仍显示 `Established`

---

## VPN-12 主备双隧道与双公网地址

**P1** · 需要一个对端有两个地址的环境，比如 IBM Cloud 的 route 模式 VPN 网关（它的一条连接自带两条隧道，每个成员地址一条）。work-01 上的 IBM 测试环境：搭建脚本 `/root/ibmr-1-setup.sh`，状态在 `/root/ibmr.state`，连接的建法见 `/root/ibmr-conn.sh`（BGP）与 `/root/ibmr-static.sh`（静态）。方案见 `docs/architecture/plan/vpn-gateway-plan.md` §4.1、§18.7。**2026-09-28：建双地址网关要 2 个空闲公网地址，SKIP；在保留的 `ibmr-gw`（.154 / .155）、`ibmx-gw` 上只读核对。**

```bash
# two public addresses
api POST /vpn_gateways '{"name":"...","vpc":{"id":"..."},"ipsec_enabled":true,"public_ips":[{"public_subnet":{"id":"<pub>"}},{"public_subnet":{"id":"<pub>"}}]}'
# two tunnels (BGP), the list position is the slot
api POST /vpn_gateways/<gw>/connections '{"name":"...","route_mode":"bgp","psk":"...","local_asn":65010,"peer_asn":64520,"remote_summary_cidrs":"...",
  "tunnels":[{"endpoint":"vip1","remote_gateway":"<peer 1>","tunnel_local_ip":"169.254.100.2","tunnel_peer_ip":"169.254.100.1","priority":"primary"},
             {"endpoint":"vip2","remote_gateway":"<peer 2>","tunnel_local_ip":"169.254.100.14","tunnel_peer_ip":"169.254.100.13","priority":"standby"}]}'
```

### 检查项

- [ ] 双地址网关：`public_ips` 两项（`vip1` / `vip2`，`hostid=-1`），`addable_endpoint` 为空；主节点公网口上有两个地址，备节点没有；**两个节点**各地址 3 条 INPUT 放行（500 / 4500 / ESP）和一对策略路由规则；两个节点的 keepalived 配置 `virtual_ipaddress` 都是两行（`ibmr-gw` 2026-09-28 只读核对：work-02 / work-03 都是 3 + 3 条、4 条规则，地址只在 work-02）
- [ ] `GET /vpn_gateways/<ibmr-gw>/public_ips/vip2` → 200 地址 `.155`；`GET /vpn_gateways/<ibmx-gw>/public_ips/vip2` → 404（`The gateway has no such public address`）
- [ ] 配额：创建双地址网关预扣 2 个公网 IP，删除时释放 2 个（SKIP）
- [ ] 两条隧道都 `up`，连接 `up`；swanctl 里两个 IKE 会话 `c<连接>-t1` / `-t2`，从地址 2 出发的那条本端是地址 2（`ibmx-gw` 的 `to-ibmr-bfd` 与 `ibmr-gw` 的 `to-ibmx-bfd` 只读可看）
- [ ] BGP：work-02 上 `vtysh -N vpn-15 -c 'show bgp ipv4 unicast 192.168.93.0/24'`（`ibmr-gw` 从 `ibmx-gw` 学到的路由）两条路径：经主隧道（下一跳 169.254.110.1）AS_PATH `65020`、localpref 200、best；经备隧道（169.254.110.5）AS_PATH `65020 65020 65020`（对端对备隧道的通告前置 2 次，`as_path_prepend` 默认 2）、localpref 100（2026-09-28 只读核对一致）
- [ ] 静态：`ibmr-gw` 的 `tunnel_routes` 为 `10.241.0.0/24 static ipsec-13,ipsec-14`（主在前），内核路由走主隧道（2026-09-28 已核对 `tunnel_routes`）
- [ ] 阻断主隧道（主节点路由器 netns 里丢弃到对端 1 的包）：连接变 `degraded`（界面「降级」），流量切到备隧道；恢复后回到 `up`、路由切回主隧道 —— 故障注入，**SKIP**
- [ ] 校验（可在 vpn-a 上做，见 VPN-09）：两条隧道本端和对端地址都相同 → 400；网关只有一个地址时用 `vip2` → 400；两个 `primary` → 400；BGP 两条隧道的隧道 IP 同一个 /30 → 400
- [ ] 旧写法兼容：不带 `tunnels`、只带顶层 `remote_gateway` 等字段 → 建成单隧道连接（VPN-02 的 `dummy-st` 就是这种写法）
- [ ] 界面：创建网关可选 1 个 / 2 个地址；连接表的隧道列每条隧道一行、带主 / 备和状态；`degraded` 显示「降级」；展开后每条隧道一张卡片；编辑弹窗回填两条隧道；两条隧道时重启菜单可选单条（「重启隧道 1（主隧道）」）
  > **回归点**（2026-09-24）：改 BGP 计时器后已建立的会话不会按新值重新协商（现在下发时对计时器变了的邻居清一次会话）；从 BGP 改成静态后隧道上残留旧的 BGP 状态

---

## VPN-13 快速切换

**P1** · work-01 `/root/ibmr-fo.sh <tunnel1|keepalived|linkcut> [秒数]`：三台虚拟机每 0.1 秒 ping 对端，第 20 秒注入故障，按连续丢包分段报告中断，并列出 clapi 看到的切主时间、cland 的失联判定、charon 日志。**模拟断网要用 `linkcut`（`tc netem` 丢弃出向帧），不要用 iptables（脚本里的 `netcut`）：它拦不住 ARP，被隔离的节点会继续回应公网地址的 ARP，结果不可信。** ⚠️ 脚本里的 `M1=150.239.81.237` 是已删除的旧 IBM 成员，`tunnel1` 场景要先改成 `150.239.85.49`。

**2026-09-28：所有故障注入都作用于保留网关与 IBM 连接，未经用户同意 SKIP**；只读项可跑，单网关的 keepalived 场景见 VPN-04。

### 检查项

- [ ] 主节点上每个网关一个看护循环：`ps -eo ppid,args | grep '[v]pn_watch.sh' | awk '$1==1 {print $NF}'` 在 work-02 上是 13 14 15 16、work-03 上只有 16（`aax-gw` 双活，两个节点都跑）、work-01 上没有（2026-09-28 核对），与各自的 `watch.pid` 一致；杀掉测试网关的看护循环后下一次心跳（≤ 20 秒）被守护脚本拉起
- [ ] 立即上报：主节点切换或隧道状态变化后，`/run/cloudland/vpn-report.trigger` 被改写，clapi 3 秒内看到新状态（不等心跳）—— 可在 vpn-a 上用 VPN-04 观察
- [ ] 存活信号：cland 日志在节点断网后约 4 秒出现 `Node <id> liveness: gone`，恢复后出现 `ok`；clapi 日志 `cland reports node <id> as gone`（SKIP：要断一个节点的网）
- [ ] `keepalived`：clapi 日志 `takes over ... (the old master released the address)`，切主在 5 秒内；新主节点 2 秒内建好隧道；所有节点上的虚拟机中断 ≤ 对端 BGP 保持时间 + 1 秒（IBM、计时器 3/9 秒时实测 8.8 秒）
- [ ] `linkcut`：clapi 日志 `takes over ... (cland lost the old master)`，切主在 6 秒内；其他节点上的虚拟机中断 ≤ 12 秒（实测 9.8 秒）；恢复后切回原主节点再中断一次（实测 9.9 秒）
- [ ] `tunnel1`（BGP，计时器 3/9 秒）：中断 ≤ 10 秒（实测 7.0 秒）；默认 10/30 秒时约 23 秒
- [ ] 静态双隧道 `tunnel1`：中断约 21 秒（对端失效检测 10 秒 + 三次重传约 11 秒）
- [ ] IKE 重传参数（只读）：`strongswan.conf` 里有 `retransmit_tries = 3`、`retransmit_timeout = 1.5`、`retransmit_base = 1.4`；新建连接的 `dpd_delay` 默认 10（vpn-a 的 `dummy-st` 可看）
- [ ] BFD（两个 CloudLand 网关互为对端，work-01 `/root/bfd-setup.sh [间隔毫秒]` 建连接，`/root/bfd-fo.sh [秒数] [tunnel|keepalived]` 测切换，`/root/bfd-set.sh '<PATCH 内容>'` 改两端参数）：只读部分——主节点 `vtysh -N vpn-15 -c 'show bfd peers brief'` 每条隧道一个会话、状态 up（2026-09-28 核对：两个会话 169.254.110.2/.6 均 up），接口是 `ipsec-<id>`；接口返回的隧道 `bfd_state` 为 up；内核日志没有 bfdd 的 DENIED
- [ ] BFD 切换：只阻断主隧道，300 毫秒 × 3 中断 ≤ 1.5 秒（实测 0.7 秒），1000 毫秒 × 3 ≤ 4 秒（实测 2.4 秒），关掉 BFD 约 24 秒；关掉 BFD 后 `show bfd peers` 为空、BGP 会话不中断（SKIP：要改保留网关的连接）
  > **回归点**（2026-09-24）：keepalived 父进程被杀时，子进程自己摘掉公网地址却不调切换脚本，看护循环随之退出而不让位，其他节点的虚拟机要等 30 秒窗口（17.7 秒）；现在看护循环发现地址丢失会降级并立即上报
- [ ] 没有节点持有公网地址（`/root/ibmx-nomaster.sh [秒数]`：两个节点的公网口 down 150 秒）：约 90 秒后 clapi 日志出现 `no node claimed its public address`，接口里主节点为空，连接为 down，每条连接一个 `VpnConnectionDown`；公网口恢复后 10 秒内主节点与状态复原、告警解除（SKIP：ibmx-gw 三条连接都会断）
  > **回归点**（2026-09-24）：原先只有持有地址的节点上报，两个节点都 FAULT 时 clapi 一直显示原来的主节点和全部 up（实际中断了 9 分钟）
- [ ] 伪造的 `node_liveness` 被忽略、cland 积压时不误判失联：见 VPN-21

---

## VPN-14 双活模式

**P1** · 方案 §4.2。2026-09-24 在 work-x 实测过，结果见方案 §18.8。环境 `aax-gw` ↔ `ibmx-gw` 保留，脚本在 work-01 `/root/aax-*.sh`（状态文件 `/root/aax.state`）。需要三个节点：两个网关节点加一个只有虚拟机的节点。对端可以是另一个 CloudLand 网关（主备或双活都行），或 IBM route 模式网关：IBM 一条连接只认一个对端地址，要建两条连接分别对两个网关节点的地址，回程只能按路由优先级主备（方案 §16.4，`/root/ibm-aax.sh`）。**2026-09-28：建双活网关要 2 个空闲地址，SKIP；`aax-gw` 只读核对，故障注入要用户同意。**

```bash
api POST /vpn_gateways '{"name":"aa-gw","vpc":{"id":"<vpc>"},"ha_mode":"active_active","ipsec_enabled":true,"public_ips":[{"public_subnet":{"id":"<pub>"}}]}'
# two tunnels, node1 / node2 one each; without priorities the primary goes to the node carrying fewer primaries
api POST /vpn_gateways/<gw>/connections '{"name":"to-peer","route_mode":"static","psk":"...","remote_cidrs":"10.241.0.0/24",
  "tunnels":[{"endpoint":"node1","remote_gateway":"<peer 1>"},{"endpoint":"node2","remote_gateway":"<peer 2>"}]}'
```

### 检查项

- [ ] 创建：`ha_mode` 为 `active_active`，`public_ips` 两项 `node1` / `node2`，各带节点名（`aax-gw`：node1 .156 work-02、node2 .157 work-03），`addable_endpoint` = `vip1`，`master_hostname` 为空；配额扣 2 个公网 IP（开客户端 VPN 时 3 个）
- [ ] 两个节点的公网口上各只有自己的地址，重启节点后地址与 `fip-<vlan>` 表的默认路由还在；没开客户端 VPN 时两个节点都没有 keepalived，`vrrp-<id>/` 目录为空（`aax-gw` 2026-09-28 只读核对：work-02 只有 .156、work-03 只有 .157，`vrrp-20/` 空、keepalived 0）
- [ ] 两个节点都有 charon、FRR（5 个守护进程，含 bfdd）、看护循环；各自的 `swanctl.conf` 只有自己的隧道，`local_addrs` 是自己的地址
- [ ] iBGP：`vtysh -N vpn-16 -c 'show bgp summary'` 有对端节点 VRRP 地址的会话（ASN 64999，描述 `ibgp`），Established；`show bfd peers brief` 有它、状态 up（间隔 300 毫秒）
- [ ] 静态连接：SA 起来的节点有 `ip route <远端网段> ipsec-<id> tag 100`（主）或 `tag 101 211`（备）；没有主隧道的节点 `show ip route <远端网段>` 走 iBGP 到对端节点；两个节点都有 `Null0 254`（`aax-gw` 两条连接都是负载分担，各节点只有自己那条、都是 tag 100；2026-09-28 `show running-config` 核对一致）
- [ ] 第三个节点：`/opt/cloudland/cache/router/router-<N>/vpn-<gw>/nexthops` 里主隧道所在节点排第一，路由经它的 VRRP 地址；负载分担的连接第二列是 `1`、路由是经两个 VRRP 地址的多路径（work-01 的 `router-52/vpn-16/nexthops` 2026-09-28 为 `192.168.93.0/24 1 192.168.196.2,192.168.196.3`）
- [ ] 断开主隧道（节点还活着）：连接 `degraded`，主隧道所在节点改经 iBGP 转给另一个节点；clapi 把第三个节点的路由改到另一个节点（日志 `the preferred gateway node of a connection changed`）；测中断时间（SKIP）
- [ ] 主隧道所在节点断电（`echo b > /proc/sysrq-trigger`）或断网（`tc netem`）：另一个节点的 BFD 约 1 秒断 iBGP、走自己的隧道；第三个节点的 F7 约 1 秒切走（见 VPN-16）；测中断时间，对照方案 §15.1（断网实测：另一个网关节点 0.9 秒、第三节点 2.1 秒）（SKIP；断电从来没测过，CLAUDE.md G21）
- [ ] 对端设备一次断掉两条隧道：连接直接变「未连接」并发 `VpnConnectionDown`（critical），不会停在「降级」（回归点 46，SKIP）
- [ ] 两个节点之间断链、两台都活着：各走自己的隧道，业务不受影响（双活下这种故障无害）（SKIP）
- [ ] 客户端 VPN：没有 vip1 时打开被拒（400，`Add a public address for the client VPN first`）；`POST .../public_ips` 加 vip1 后能打开，两个节点都跑 keepalived，客户端流量只经持有 vip1 的节点；客户端访问不到远端站点（两个节点的 `FORWARD` 都有 `-s <地址池> -o ipsec+ -j DROP`）
- [ ] 状态：两个节点各报自己的隧道，界面上每条隧道显示所在节点；停用 / 启用网关两个节点都生效
- [ ] 改连接配置（PATCH 描述、计时器等）期间两个节点的 BGP 会话都不重建（`/root/aax-uptime.sh desc`，前后 `show bgp summary` 的 Up/Down 时间只增不减），业务 0 丢包（`/root/aax-cfgchange.sh '<PATCH 内容>'`）（SKIP：改保留连接）
  > **回归点**（2026-09-24）：① 每次下发配置都会重置全部 BGP 会话（R1 起就有）：FRR 把 `neighbor X bfd` 和 `neighbor X bfd profile P` 显示成两行，生成的配置只有后一行，frr-reload 每次先删 BFD 再加回来；② 双活静态连接的备节点走了自己的备隧道，没有经 iBGP 走对端的主隧道：bgpd 本地起源的路由 weight 32768 压过了 local-preference，现在 `TUNNEL-STATIC` 里 `set weight 0`

---

## VPN-15 负载分担与全连接

**P2** · 方案 §5.5、§2.4。2026-09-24 实测了双活网关上 BGP 与静态连接的负载分担（方案 §18.8，`/root/aax-ecmp.sh`）；主备网关的四条隧道全连接、与 IBM `distribute_traffic` 同时打开没有测。

### 检查项

- [ ] `traffic_policy: ecmp` 的主备网关静态连接：主节点上远端网段是一条多路径路由，经所有 up 的隧道；断一条隧道后只剩另一条；`sysctl net.ipv4.fib_multipath_hash_policy` 为 1（只读：`ibmx-gw` 的 `to-aax` 是主备网关上的负载分担静态连接，work-02 的 `tunnel_routes` 为 `192.168.95.0/24 static ipsec-26+ipsec-27`，`ip route show 192.168.95.0/24` 是经 `ipsec-26` / `ipsec-27` 的两个 nexthop，2026-09-28 已核对；主隧道优先的 `to-ibm` 是 `ipsec-12,ipsec-19`、路由只走 `ipsec-12`）
- [ ] `ecmp` 的 BGP 连接：各隧道 local-preference 都是 200、不前置；`show bgp ipv4 unicast <网段>` 有多条 multipath；FRR 配置有 `maximum-paths`（要 BGP 对端，SKIP）
- [ ] 双活网关的 `ecmp` 连接：两个节点各走自己的隧道，第三个节点的路由是经两个 VRRP 地址的多路径（只读，见 VPN-14）
- [ ] 汇总状态：都 up 为 `up`，部分 up 为 `degraded`
- [ ] **从 `ecmp`（负载分担）改回 `preferred`（主隧道优先）**（**新增/未跑过**，CLAUDE.md G22 的一半，假对端可跑）：在 vpn-a 上建 `{"name":"ecmp-t","route_mode":"static","remote_cidrs":"10.99.5.0/24","psk":"DummyPsk-2026","traffic_policy":"ecmp","tunnels":[{"remote_gateway":"198.51.100.51"},{"remote_gateway":"198.51.100.52"}]}` → 两条隧道 `priority` 都是 `primary`、都是 `vip1`，主节点 `tunnel_routes` 目标用 `+` 分隔；`PATCH {"traffic_policy":"preferred"}` → 隧道 1 `primary`、隧道 2 `standby`，`tunnel_routes` 变为 `,` 分隔、主在前；界面上连接的流量分配显示「主隧道优先」、隧道行带主 / 备
- [ ] 全连接：主备双地址网关、对端两个地址，一条连接 4 条隧道；隧道 1 为主时 local-preference 200 / 100 / 90 / 80，前置次数逐级加一；逐条断开时按排名切换（要 2 个我方地址 + 2 个对端地址，SKIP；只有单元测试）
- [ ] 与 IBM 的 `distribute_traffic` 配合：IBM 按源地址分流（方案 §16.3），两端都分担时单条隧道断开的影响（SKIP）
  > 2026-09-24 测过双活网关对 IBM（方案 §16.4，work-01 `/root/ibm-aax.sh`）：IBM 一条连接只认一个对端地址，要两条连接；两条路由同优先级会被 IBM 拒绝（`route_conflict`），回程只能按优先级主备，`distribute_traffic` 没有效果。阻断隧道或断网时回程中断 31–33 秒，有一次超过 65 秒

---

## VPN-16 其他节点本地快速切换（F7）

**P1** · 方案 §8.2 F7。2026-09-24 实测过（方案 §18.8）：主隧道所在节点断网时，第三节点中断 2.1 秒。**2026-09-28：断网 SKIP（保留网关），只读项在 work-01（ibmx / ibmr / aax 的第三节点）上看。**

### 检查项

- [ ] 承载 VPC、但不是网关节点的节点上有**一个** `vpn_nexthop_watch.sh`（`ps -eo pid,ppid,args | grep '[v]pn_nexthop_watch'`，父进程是 1，与 `/run/cloudland/vpn-nexthop/watch.pid` 一致），`/run/cloudland/vpn-nexthop/` 下有各网关节点地址的状态文件（work-01 上是 `10.191.202.13`、`10.191.202.29`）；杀掉后下一次心跳被拉起
- [ ] `nexthops` / `nexthop_hosts` 内容与网关节点一致：VRRP 地址 → 节点管理地址（work-01 的 `router-50/vpn-14/nexthop_hosts` 为 `192.168.196.2 10.191.202.29`、`192.168.196.3 10.191.202.13`），路由按 `nexthops` 装（`ip netns exec router-50 ip route` 的 `10.241.0.0/24 via 192.168.196.2 dev ns-<vlan> onlink`）
- [ ] 网关节点断网：第三个节点约 1 秒内把路由改到另一个网关节点（节点日志 `vpn: gateway node <ip> does not answer, routing around it`），恢复后连续 10 次应答（3 秒）再切回，切回时不再断（SKIP）
  > **回归点**（2026-09-24）：原先连续 3 次应答就切回，节点网络刚恢复、BGP 会话还没起来，恢复时又断了 0.2 秒和 0.1 秒两次
- [ ] 主备网关：切走后流量经备节点转给主节点（此时主节点没死只是 ping 不通时，业务仍通）；VRRP 切换完成后 clapi 下发的新顺序与本地状态一致（SKIP）
- [ ] 所有网关节点都不通时保持偏好顺序的第一个，不删路由（SKIP）
- [ ] 删掉测试网关后，它的第三节点上 `vpn-<gw>/` 目录与路由清掉。⚠️ 该节点上还有别的网关（保留环境下 work-01 一直有 ibmx / ibmr / aax）时，**探测进程不退出**是正常的；只有节点上最后一个网关的路由清掉后，探测进程才在 10 秒内退出（2026-09-28 核对）

---

## VPN-17 公网地址增减、隧道告警、按隧道的流量

**P2** · 方案 §9.3、§9.6、§9.7。2026-09-24 实测过（方案 §18.8）。脚本在 work-01：`/root/aax-pubip.sh`、`/root/ibmx-rmvip2.sh`、`/root/aax-client-cycle.sh`、`/root/aax-traffic.sh`（都作用于保留网关）。**2026-09-28：加地址要第二个空闲公网地址，SKIP；以下标「可跑」的在 vpn-a 或只读做。**

### 检查项

- [ ] 主备单地址网关 `addable_endpoint` 为 `vip2`（`ibmx-gw`、vpn-a）；双地址的 `ibmr-gw` 为空；双活没开客户端的 `aax-gw` 为 `vip1`（只读，可跑，2026-09-28 已核对）
- [ ] `POST .../public_ips` 后两个地址都在主节点公网口上，keepalived 持有两个；配额多扣 1 个（SKIP）。当前没有空闲地址时 vpn-a 上 `POST .../public_ips {}` → 409（131004 `Not enough idle addresses`），网关与配额不变（可跑）
- [ ] 有隧道用着 vip2 时 `DELETE .../public_ips/vip2` → 409（132007 `Connection X still has a tunnel on this address`）；没有时删除成功，地址、策略路由、INPUT 规则都清掉，配额释放 1 个（SKIP）；`DELETE .../public_ips/vip1` 永远 400（`This public address cannot be removed from the gateway`，vpn-a 可跑）
- [ ] 双活网关 `addable_endpoint` 为 `vip1`（没有客户端地址时）；客户端 VPN 开着时删不掉 vip1（409，132007）
- [ ] **创建中不能加减地址**（回归点 47，可跑）：见 VPN-21
- [ ] 带流量删 vip2（`/root/ibmx-rmvip2.sh`）：0 丢包；主节点一直是 MASTER（keepalived 日志只有 `ip address ... no longer exist`，没有 `Entering BACKUP` / `FAULT`）；**两个节点**的 `te-<路由器>-<VLAN>` 都还在（SKIP）
- [ ] 双活网关加 vip1、开客户端 VPN、关闭、删 vip1（`/root/aax-client-cycle.sh`）：虚拟机到对端 0 丢包；开着时两个节点都有 keepalived 和 `wg-<网关>`，关掉并删 vip1 后都没有、`vrrp-<id>` 目录为空，公网口与节点地址都在；配额 +1 后 -1（SKIP）
  > **回归点**（2026-09-24）：删 vip2 曾把两个节点共用的公网口一起删掉，网关当场失去主节点，keepalived 一直停在 FAULT。原因是 keepalived 重载前先删了地址（keepalived 看到自己的地址被外部删除就退出 MASTER），`clear_lb_floating.sh` 又按「口上已没有地址」删口（备节点的口本来就不挂地址）
- [ ] 审计与组织动态有 `vpn_gateway.public_ip_add` / `public_ip_remove`（SKIP，随加减地址）
- [ ] 两条隧道的连接断一条：告警事件里出现 `VpnTunnelDown`（warning），隧道恢复后解除；两条都断：`VpnConnectionDown`（critical）；停用网关时两类都解除（SKIP，要对端）
- [ ] `GET .../traffic?by=tunnel`：每条隧道一条曲线，名字 `<连接> / t<slot>`，带 `connection_id` 与 `slot`（可跑，`ibmx-gw` 2026-09-28 已核对）；双活网关两个节点的计数加起来等于连接总量（`/root/aax-traffic.sh` 逐点对比，单位 bit/s，120 秒窗口，所以刚开始发流量时数值逐渐爬升；只读可跑，但要有流量）

---

## VPN-18 单节点网关与自动补备节点

**P1** · 方案 §4.1、§9.3、§18.15。**2026-09-27 部署后实测过**（方案 §18.18，脚本 work-01 `/root/zone-test-20260927.sh`：双活 132009、主备 5 秒单节点可用、移入第二个节点 292 秒补上备节点、`kill -9` 后 2 秒接管；没测补备节点时的真实流量与 HA 双 clapi）。要一个只有一个可用节点的 zone：临时建 zone、把一个节点移过去，测完移回。

> ⚠️ **与 vpn-a 互斥**：单节点网关同样要 `.158`，Admin 的 VPN 网关配额也只剩 1 个。先做完其他用例并删掉 vpn-a（VPN-10），再做这条。
> ⚠️ 三个节点都承担着保留网关 / `rb-lb` 的 VRRP 角色，没有「干净」的节点；移 zone 只影响这段时间里新资源选节点，已放置的 VRRP 实例不动（09-27 移 work-03、work-02 实测无影响）。**期间 zone0 只剩一个节点，别并行跑别的建资源用例。**
> ⚠️ zone 的删除接口按**名字**：`DELETE /zones/ztest`（09-27 的脚本传了 uuid，返回 404）。

```bash
api POST /zones '{"name":"ztest"}'                                        # note the zone uuid
api POST /vpn_gateways '{"name":"sn-gw","vpc":{"id":"<vpc>"},"zone":"ztest","ipsec_enabled":true}'   # empty zone -> 132009
api PATCH /hypers/<work-03 uuid> '{"zone_id":"<ztest uuid>"}'             # back to zone0 18c64201-49a4-48b9-a0b8-36c784be63fd afterwards
api POST /vpn_gateways '{"name":"sn-aa","vpc":{"id":"<vpc>"},"zone":"ztest","ha_mode":"active_active","ipsec_enabled":true}'
api POST /vpn_gateways '{"name":"sn-gw","vpc":{"id":"<vpc>"},"zone":"ztest","ipsec_enabled":true}'
```

### 检查项

- [ ] 空 zone（还没移节点）里建网关 → 400，`error_code` 132009（`Zone ztest has 0 available compute node(s)...`），界面在底栏显示「所选可用区的可用计算节点不够…」，没有分配公网 IP（`.158` 仍空闲）、没有 VRRP 实例、配额回滚。这一条不占地址，任何时候都能做
- [ ] 单节点 zone 里建双活网关 → 400（132009）
- [ ] 单节点 zone 里建主备网关：约 1 分钟内 `available`（不再停在 `pending`；09-27 实测 5 秒）；`nodes` 只有一个（MASTER）；clapi 日志有 `runs on a single node, without high availability`；节点上 keepalived 约 3 秒后为 MASTER、charon 起来、公网地址在 `te-` 口上
- [ ] 界面（本机 5173，只读）：详情页标题有「单节点」标签、黄色提示条；列表节点列有「单节点」（节点列 1280 宽以上才显示）；创建中（`pending`）不显示。前端没有 `single_node` 字段，是按「主备、不在创建 / 删除中、`nodes` 只有一个」算的（`single_node` 是负载均衡接口的字段，见 TC-06 LB-07b）
- [ ] 连接能建、隧道能起（假对端即可看配置）；`kill -9` 该节点 keepalived：看护循环摘地址，守护脚本下次心跳拉起，约 3 秒后恢复（记中断时间）
- [ ] **自动补备节点**：把 zone0 的另一个节点也移进 ztest，20 秒到 5 分钟内 clapi 日志 `VRRP instance <id> runs on node <n> alone, placing its BACKUP node in zone <z>`，BACKUP 网卡有了节点，`nodes` 两个，「单节点」标签消失；**补的过程中业务 0 丢包**（原节点 keepalived 保持 MASTER、只重载同样的配置；要测流量就先起 VPN-05 的 WireGuard 客户端，**新增/未跑过**）；之后 `kill -9` 主节点 keepalived，备节点接管（09-27 实测 2 秒）
- [ ] 补上的备节点如果之前是这个 VPC 的普通节点：它的 `vpn-<网关>/` 目录里不再有 `nexthops`、`nexthop_hosts`、`routes.current`，F7 探测循环不再改它的路由；之后 `kill -9` 主节点 keepalived，这个节点升主后路由指向自己的隧道、没有被改回经对端（回归点 45）
- [ ] HA 两台 clapi 时（没有就跳过）：补备节点只下发一次，`interfaces` 里 BACKUP 网卡只落到一个节点
- [ ] `error=resource`：ztest 里第二个节点在库里是可用、但 cloudlet 停着时建主备网关 → 不卡 `pending`，按单节点运行，clapi 日志 `set_vrrp_ip.sh for the BACKUP interface of VRRP instance <id> found no connected node`；恢复 cloudlet 后自动补备节点。**SKIP**：停哪个节点的 cloudlet 都会让保留网关失去上报（work-02 是三个保留网关的主节点，90 秒后被当成无主、隧道置 down 并告警；work-03 上跑着 `aax-gw` 的 node2，cland 3 秒判失联后会把它的隧道置 down 并改路由）
- [ ] 失败原因：数据库把测试网关置 `status='error', status_reason='test'`（安全做法）→ 详情页红色提示条、列表状态悬停显示；任一 PATCH 救回后 `status_reason` 清空。节点侧真实失败（临时改坏 `create_vpn_gateway.sh` 让它退出非零 → `status_reason` 为「Node <节点> failed to build the gateway (see its cloudlet log)」）会影响同节点上保留网关的重下发，**只在用户同意时做**，改前备份、做完立即恢复
- [ ] 收尾：删测试网关与 VPC，两个节点移回 zone0，`DELETE /zones/ztest`，`GET /hypers` 与 `GET /zones` 核对只剩 zone0、三节点都在 zone0，`.158` 回到 `allocated=false`
  > **回归点**（2026-09-27）：zone 里只有一个可用节点时网关永远停在 `pending`、PATCH 也救不回来（`SetVrrpIp` 选不到 BACKUP 只更新负载均衡表；救回路径写死两个节点）；`error=resource` 回调被当成节点回调解析，第 3 个参数（VNI）当节点 ID，报错后 cland 重试 3 次放弃

---

## VPN-19 凭据回读（按角色）

**P1** · **新增/未跑过**（2026-09-28）。方案 §9.2、§18.12；CLAUDE.md G19。09-27 部署后只验证了「管理员查连接返回预共享密钥」，观察者、客户端配置模板、换密钥都没测。

**准备只读账号**（有现成的 Admin 组织只读账号就直接用，如 2026-09-28 那轮的共享账号 `wpr_reader_0928b`，`/root/wpr-lib.sh` 的 `tok_reader`；没有时照 TC-18 PG-14，脚本 work-01 `/root/pg-e2e-14.sh` 前半段）：`POST /auth/register` 注册 `vpnr_<日期>` → 管理员 `PUT /users/<uuid>/enable` → `POST /orgs/305fcaf9-c76a-4ed5-a1ae-eddb8d8fc64e/members {"user_uuid":"<uuid>","org_role":1}` 加进 Admin 组织当观察者；登录时带 `org_uuid` 再 `switch-region`。这个用户注册时会带出一个自助注册的组织，它不会同步到区域（FAIL-2），与本用例无关，清理时一起删。

在 vpn-a 上准备：一条带隧道级密钥的连接和一条带 BGP 口令的连接（假对端），一个 `preshared_key:true` 的客户端。

```bash
api POST /vpn_gateways/$(lv gw_a)/connections '{"name":"sec-st","route_mode":"static","remote_cidrs":"10.99.6.0/24","psk":"ConnPsk-2026",
  "tunnels":[{"remote_gateway":"198.51.100.61"},{"remote_gateway":"198.51.100.62","psk":"TunnelPsk-2026"}]}'
api POST /vpn_gateways/$(lv gw_a)/clients '{"name":"sec-cl","preshared_key":true}'
```

### 检查项

- [ ] 编辑者（管理员）：`GET /vpn_gateways/<gw>`、`GET .../connections`、`GET .../connections/<id>` 里 `sec-st` 带 `psk` = `ConnPsk-2026`、`psk_set=true`；隧道 2 带 `tunnels[1].psk` = `TunnelPsk-2026`、`psk_set=true`，隧道 1 没有 `psk`、`psk_set=false`；`dummy-bgp`（VPN-03）带 `bgp_password`、`bgp_password_set=true`。保留的 `ibmx-gw` 三条连接都带 `psk`（2026-09-28 已核对，不要把值打印出来）
- [ ] 编辑者：`GET .../clients/<sec-cl>/config` 的 `PresharedKey` 是真实值（44 字符 base64），`PrivateKey = <your private key>`
- [ ] 观察者：同样的查询都是 200，**没有** `psk` / `bgp_password` / `tunnels[].psk` 字段，只有 `*_set`；客户端配置模板是 `PresharedKey = <preshared key>`
- [ ] 观察者写操作一律 403（100004）：PATCH 网关、建连接、建客户端、重启连接
- [ ] 界面（本机 5173，以观察者登录）：连接展开行显示「已设置 / 未设置」、没有显示 / 复制按钮；对端配置弹窗顶部是「只对编辑者及以上角色显示」、文本里没有密钥；以管理员登录时打码、可显示 / 复制，复制与下载的文本是明文。⚠️ `vpn-secrets-ui.js` 写于后端部署前：它的 editor 一轮在浏览器里伪造密钥、viewer 一轮用真实返回——**现在管理员的真实返回就带密钥，viewer 一轮会失败**，要改成以观察者账号登录，或在浏览器里删掉 `psk` / `bgp_password` 字段模拟
- [ ] 换掉 `VPN_SECRET_KEY` 后查询仍是 200，只是没有这些字段（clapi 日志 `VPN credential not returned`）——要改 `.env` 并重启 clapi，保留网关之后的任何重下发都会解不开密钥，**SKIP**。按代码，此时编辑者下载**带预共享密钥**的客户端配置会 503（132031），不是省略字段（待确认是否符合设计）
- [ ] 清理：删 `sec-st`、`sec-cl`；`DELETE /orgs/305fcaf9-.../members/<uuid>`、删观察者用户和它注册时带出的组织（`pg-e2e-14-cleanup.sh` 的做法）
  > **回归点**（2026-09-25）：见回归点 42

---

## VPN-20 界面写操作（本机 5173，真实提交）

**P1** · **新增/未跑过**（2026-09-28，CLAUDE.md G20：R2 / R3 界面、对端配置、密钥显示、单节点标签都只做过只读检查）。在本机 5173 上用 Playwright 真实提交，但**只放行测试网关的写请求**：`POST /vpn_gateways`（且请求体的 `vpc.id` 是 VPC A）、`/vpn_gateways/<vpn-a>/**`，其余写请求（尤其 `/vpn_gateways/<保留网关>/**`）一律按 pathname 拦截并计数，结束时被拦截的写请求应为 0（出现就说明界面往不该写的地方发了请求，记 FAIL 并附路径）。可以用来代替 VPN-01 的「接口建 vpn-a」（二选一，`.158` 只有一个）。

### 检查项

- [ ] 创建弹窗：选 VPC A、主备、公网子网 `public-756` → 地址下拉只有 `.158`；勾站点到站点与客户端 VPN、地址池 `10.8.0.0/24` → 创建成功，列表出现「创建中」，约 10 秒变「可用」
- [ ] 编辑弹窗：改描述、改客户端路由（先填 `0.0.0.0/0` 被底栏拦下、不发请求，再填 `192.168.71.0/24` 保存成功）
- [ ] 连接弹窗：建一条 2 条隧道的静态连接（对端 `198.51.100.71` / `.72`，远端网段 `10.99.7.0/24`，第二条隧道单独填预共享密钥）→ 表格出现，隧道行两条、主 / 备；编辑弹窗回填两条隧道与密钥状态；改成负载分担后连接带 ECMP 标签
- [ ] 重启菜单：「重启全部隧道」与「重启隧道 2」各点一次，请求分别不带 / 带 `?tunnel=2`，都 200
- [ ] 对端配置弹窗打开自己这条连接：两条隧道各自的预共享密钥（隧道 2 用自己的），复制 / 下载的文本是明文
- [ ] 客户端弹窗：默认「用户自带公钥」；改成平台代生成 → 显示私钥只出现一次的提示，可复制、下载 `.conf`；列表出现地址 `10.8.0.x`；停用 / 启用按钮生效
- [ ] 公网地址：详情页「添加」打开的弹窗里地址下拉没有空闲地址，按「自动」提交后后端 409（131004，没有空闲地址），报错留在弹窗里；创建中（刚建好那几秒）添加 / 释放按钮禁用，悬停提示「等网关变成「可用」后再加减公网地址」
- [ ] 操作菜单「停用」→ 确认弹窗 → 顶部横幅「已停用」带「启用」、连接「已停用」、重启按钮禁用 →「启用」恢复
- [ ] 依次删客户端、连接、网关（删除确认弹窗文案是「其下所有站点连接和客户端将一并删除…」），列表回到只有保留网关
- [ ] 0 页面错误；审计里这些操作都有对应动作名（VPN-08）

---

## VPN-21 2026-09-27 代码复查修复的回归

**P1** · **新增/未跑过**（2026-09-28）。方案 §18.16、§18.17；回归点 46–53、55。§18.18 部署后只验证了伪造 `node_liveness`，其余「只有代码与单元测试层面的验证」。

### 检查项

- [ ] **回归点 47 创建中不能加减地址**（可跑）：`POST /vpn_gateways` 建 vpn-a 后**立刻**（`pending` 期间，约 5–10 秒）`api_code POST /vpn_gateways/<id>/public_ips '{}'` → 400（132006 `Change the public addresses once the gateway is available`），`DELETE .../public_ips/vip2` 同样 400（132006）——**只对双地址网关成立**：单地址网关没有 vip2，网关删除前的 GET 先返回 404 `The gateway has no such public address`（2026-09-28 实测），单地址时只测 POST；网关、`floating_ips`、cpgateway 用量不变；界面上的添加 / 释放按钮在非「可用」时禁用
- [ ] **回归点 49 删网关不留隧道**（可跑）：vpn-a 上还有连接时删网关（VPN-10），之后 `select count(*) from vpn_tunnels where vpn_gateway_id = <gwnum_a> and deleted_at is null` = 0；单独删一条连接后它的隧道行同样软删
- [ ] **回归点 50 同一地址两条只响应隧道**（可跑）：在 vpn-a 上建 `{"name":"resp2","route_mode":"static","remote_cidrs":"10.99.4.0/24","psk":"DummyPsk-2026","initiator":false,"tunnels":[{"remote_id":"branch-a.example.com"},{"remote_id":"branch-b.example.com"}]}` → 200（两条都在 `vip1`、对端都空）；两条 `remote_id` 相同 → 400（`Responder-only tunnels of the same public address need different identities`）；再建一条连接用同一个 `remote_id` → 400（`remote_id must be unique among responder-only tunnels of the same public address`）；主节点 `swanctl.conf` 两个块都是 `remote_addrs = %any`、`id` 各自正确
- [ ] **回归点 52 伪造的 `node_liveness` 被忽略**（可跑，照 `/root/deploy-verify-20260927.sh` 第 7 项；clapi 容器里没有 `curl` 时 SKIP）：在 work-01 上 `docker exec cloudland-clapi curl -s -X POST http://127.0.0.1:5005/internal/execute -d '{"Id":0,"Extra":3,"Control":"callback","Command":"node_liveness.sh '\''1'\'' '\''gone'\''"}'`，clapi 日志 `Ignoring node_liveness about node 1 that came from node 3`，**没有** `cland reports node 1 as gone`、没有接管。⚠️ 被报失联的节点选 work-01（hostid 1）：万一修复回退、真被当成失联，work-01 上没有任何保留网关的主节点或双活隧道，影响最小（09-27 用的是节点 2，回退时会把 `aax-gw` 在 work-02 的隧道置 down）
- [ ] **回归点 53 看护循环不双开**（可跑，在 vpn-a 主节点上）：

  ```bash
  # on the master of vpn-a
  cd /opt/cloudland/scripts/backend && source ../cloudrc && source ./vpn_lib.sh
  d=/opt/cloudland/cache/router/router-<r>/vpn-<gw>
  vpn_watch_stop $d; sleep 1; vpn_watch_start $d & vpn_watch_start $d & wait; sleep 2
  for p in $(pgrep -f "vpn_watch[.]sh <gw>\$"); do ps -o args= -p $(ps -o ppid= -p $p) | grep -q "vpn_watch[.]sh" || echo $p; done   # top-level loops
  cat $d/watch.pid
  ```

  顶层循环只有 1 个，且等于 `watch.pid`；`vpn_nexthop_watch.sh` 同理（第三节点上 `vpn_nexthop_watch_start` 两次并发，只有一个）
- [ ] **回归点 46 / §18.17 `vpnTunnelsDown` 加锁**（SKIP）：要双活网关的两个节点同时上报两条隧道都断，只能对保留的 `aax-gw` 做故障注入
- [ ] **回归点 48 cland 回调积压时不误判失联**（SKIP）：要让 clapi 慢到 cland 回调队列（10000 条）过半，没有安全的造法；判定标准是 cland 日志不出现成批的 `liveness: gone`
- [ ] **回归点 51 删除时解除告警**（SKIP）：要先有一条 firing 的 `VpnTunnelDown` / `VpnConnectionDown`，需要真实对端让隧道 up → down；有条件时：备隧道断着时删掉它（PATCH 只留一条隧道），`VpnTunnelDown` 变 resolved；删连接 / 删网关后 `VpnConnectionDown` 变 resolved
- [ ] **回归点 55 VRRP 网卡重复放置**（SKIP）：要 cland 重试一个下发过 BACKUP 之后事务失败的 MASTER 回调，造不出来；日常只核对 `select device, name, count(distinct hyper) from interfaces where type='vrrp' and deleted_at is null group by 1,2 having count(distinct hyper) > 1` 为空

---

## VPN-10 删除与清理

**P1** · 脚本 `/root/vpn-e2e-6-cleanup.sh`（它最后打印的节点计数包含保留网关，按下面的「按网关核对」判定）

```bash
# on work-01; r/n/v of the test gateway come from the state file
r=$(lv router_a); n=$(lv gwnum_a); v=$(lv vrrp_a)
bash /root/vpn-e2e-6-cleanup.sh
for h in work-01 work-02 work-03; do on_node $h "echo \$(hostname): dir=\$(ls -d /opt/cloudland/cache/router/router-$r/vpn-$n 2>/dev/null | wc -l) vrrp_dir=\$(ls -d /opt/cloudland/cache/router/router-$r/vrrp-$v 2>/dev/null | wc -l) frr=\$(ps -eo args | grep -cE '^/usr/lib/frr/[a-z]+ -N vpn-$n ') frr_etc=\$(ls -d /etc/frr/vpn-$n 2>/dev/null | wc -l) frr_run=\$(ls -d /var/run/frr/vpn-$n 2>/dev/null | wc -l) keepalived=\$(ps -eo args | grep -c '[v]rrp-$v/keepalived.conf') watch=\$(ps -eo args | grep -c '[v]pn_watch.sh $n\$') all_vpn_dirs=\$(ls -d /opt/cloudland/cache/router/router-*/vpn-* 2>/dev/null | wc -l)"; done
psql_c "select count(*) from floating_ips where deleted_at is null and vpn_gateway_id > 0"
psql_c "select count(*) from vpn_tunnels where vpn_gateway_id = $n and deleted_at is null"
```

### 检查项

- [ ] `DELETE /vpn_gateways/:id` → 204；网关删除时连接、客户端、隧道一并删（回归点 49），公网 IP 释放：`floating_ips where vpn_gateway_id > 0` 回到基线 **6**（保留网关的；2026-09-28 核对，原写的「为 0」是没有保留网关时的预期），`.158` 回到 `allocated=false`
- [ ] 测试网关在三个节点上的 `vpn-<gw>` 目录、`vrrp-<id>` 目录、charon、FRR、keepalived、看护循环、`wg-<gw>` / `ipsec-*` 接口、INPUT 规则、fdb 条目全部清零；备节点与第三节点的路由、nonat 条目清零；`all_vpn_dirs` 回到 VPN-00 记的基线（每台 4 个）
- [ ] 路由器上没有别的网关时，隔离与 MSS 规则随之删掉（测试 VPC 的 router-<A> 应清零）；保留网关所在路由器上的这些规则不受影响
- [ ] `vrrp_instances` 行删除，VPC 里没有别的 VRRP 实例时 VRRP 子网一并删除、`router.vrrp_subnet_id` 清零
- [ ] **删完后 clapi 无条件重发同路由器的 fdb**（`common.ResyncRouterVrrpFdb`）：同 VPC 若还有 LB，其 VRRP 单播心跳不受影响、不出现双主
  > **回归点**：`clear_vrrp_ip.sh` 按 MAC 删 fdb，而同 VPC 同节点的所有 VRRP 接口共用一个 MAC，删一个实例会把另一个实例到对端的转发条目一起删掉 → 双主。见 `TC-06` LB-08
- [ ] **先删网关节点上的虚拟机、网关还在时**（部署后做，只用测试 VPC）：网关节点上这个 VPC 的最后一台虚拟机删掉后，`router-<A>` netns、`vpn-<gw>` 目录、charon / keepalived 都还在，网关仍 `available`、隧道状态不变（节点调试日志 `clear_local_router.sh: router-<A> kept, used by VPN gateway vpn-<gw>`）；非网关节点（没有 `vrrp_id` 的 `vpn-*` 目录、只有路由）照常清掉
  > **2026-09-30 已修（D3：`scripts/kvm/clear_local_router.sh` 把带 `vrrp_id` 的 `vpn-*` 目录算作使用者），2026-10-01 已部署，按本条判定**。原先（2026-09-28 读代码推断，未实测）删掉网关节点上 VPC 的最后一台虚拟机会把网关的 netns 与目录一起拆掉，与 `TC-06` LB-08 同一根因
- [ ] 虚拟机 / 子网 / VPC 删除后三节点 `router-<A>` / `router-<B>` netns 消失
- [ ] 三节点 `/etc/frr/vpn-<测试网关>`、`/var/run/frr/vpn-<测试网关>` 不存在；保留网关的 `/etc/frr/vpn-14..16` 在 work-02、work-03 上都还在（配置下发给两个 VRRP 节点，备节点也有文件、只是不跑进程；`/var/run/frr/vpn-*` 只在跑 FRR 的节点上：work-02 有 14..16、work-03 只有 16，2026-09-28 核对）；`/etc/frr/frr.conf` 仍是原样（与 VPN-03 前的 `md5sum` 一致）
- [ ] `rb-lb` 10/10；四个保留网关仍是 `available`、连接全部 `up`；clapi 日志无 `no command`、无 `panic`
- [ ] 三节点 `/var/run/frr/vpn-*` 的目录（存在时）是 `drwxr-xr-x frr frr`（见 HA-08 的 umask 回归点）
- [ ] 做过的临时改动都已恢复：Admin `max_vpn_gateways` = 5（VPN-01 的 409 用例）、观察者账号已删（VPN-19）、zone 只剩 zone0（VPN-18）、work-02 / work-03 宿主机上的 WireGuard 客户端已 `wg-quick down` 并删掉配置

---

## 清理

上面 VPN-10 就是清理。若脚本中途失败，按依赖顺序手工删：**客户端 → 连接 → 网关（等节点回调） → 虚拟机 → 子网 → VPC**。节点上残留的测试网关进程 / 目录**只按测试网关的 ID 清**：`kill $(cat <vpn_dir>/run/charon.pid)`、`pkill -f '[N] vpn-<测试网关ID> '`、`rm -rf <vpn_dir> /etc/frr/vpn-<测试网关ID> /var/run/frr/vpn-<测试网关ID>`，随 VPC 删除的 netns 会带走接口与路由。**不要** `pkill -x charon` 或 `pkill -f "N vpn-"`：会杀掉保留网关的进程（2026-09-28 核对）。最后跑 `00-收尾检查.md` 的 C-11（其中的「节点上计数」同样要扣掉保留网关的基线）。

---

## 已知噪音（不要当故障）

| 输出 | 来源 |
|---|---|
| `apparmor="DENIED" ... profile="wg-quick" comm="iptables-save"` | **客户端所在宿主机**（work-03 / work-02）上 `wg-quick` 的 AppArmor 限制，与网关无关 |
| `apparmor="DENIED" ... profile="bgpd" name="/proc/<pid>/task/<tid>/comm"` | FRR 线程改名，已在本地覆盖里放行；旧节点没更新覆盖时仍会出现，无害 |
| `Error: argument "br<N>" is wrong: Device does not exist` | 新建 VPC 路由器时 `create_veth.sh int-<N>`，见 `TC-03` |
| `vpn_gateway_<vrrp>) Entering BACKUP STATE (init)` | 守护脚本拉起 keepalived 时的正常状态 |
| 假对端连接的 `last_error`、charon 日志里的 IKE 重传与超时 | 对端 `198.51.100.x` 本来就不存在 |

## 测试脚本自身的坑

- `pkill -f` / `pgrep -f` 在 `ssh` 会话里会匹配到**自己这条 ssh 的命令行**，模式要写成 `[v]pn` 这种；变量为空时 `pkill -f ""` 会杀掉节点上**所有**进程（2026-09-23 真实发生过一次，work-01 的 NetworkManager / cloudlet-go / dnsmasq 都被杀）——先判空再 pkill。
- 数进程时别按节点总数：保留网关在 work-02 / work-03 上常驻 charon、FRR、keepalived、看护循环（2026-09-28 核对）。
- 经 ssh 传多层引号 + awk 极易炸，诊断命令先写成文件（`/root/vpn-diag-*.sh`）再执行。
- 长 python heredoc 在本地 Bash 工具里会断，补丁脚本先 Write 成文件再跑。
- `/root/vpn-e2e.state` 会一直留着上一轮的键，`2-gateways.sh` 看到 `gw_a` 有值就跳过创建；每轮开头先挪走（VPN-00）。
- zone 按名字删（`DELETE /zones/ztest`），按 uuid 删返回 404。
- `st-env.sh` 占用 `B`、`T`、`BODY`、`CODE`，自己的脚本别用这几个变量名。

## 历史缺陷回归点汇总

| # | 现象 | 根因 | 判定方法 |
|---|---|---|---|
| 1 | 列表页永远「未发现 VPN 网关」 | `onMounted` 漏了首次 `fetchGateways()` | VPN-07 |
| 2 | 所有 `vpn_*` 回调被 clapi 以 `wrong params` 拒绝 | `report_rc.sh` 用 `sudo bash` 调钩子，`NODE_ID` 为空 | 心跳后 `master_hostname` 有值 |
| 3 | nonat 条目加了又被删 | `set_vpn_route.sh` 按行比较期望集合 | VPN-02 nonat |
| 4 | `swanctl: unrecognized option '--uri'` | 子命令必须在 `--uri` 前 | 状态上报 `status=up` |
| 5 | charon / bgpd / wg `Permission denied` | AppArmor 打包路径限制 | `journalctl -k \| grep DENIED` |
| 6 | BGP 学到明细但流量走黑洞 | 内核黑洞与 BGP 同前缀，zebra 不换 | VPN-03 路由表 `proto static` 黑洞 + `proto bgp` 明细 |
| 7 | `/etc/frr/frr.conf` 被清空 | `frr-reload.py` 没带 `--confdir` | VPN-03 |
| 8 | 静态 → BGP 后 BGP 起不来 | CHILD_SA 保留旧流量选择符 | VPN-03 切换后 `Established` |
| 9 | 主备切换后第三节点路由 50–62 秒才重指 | 主节点声明被拒后要等下个 20 秒周期 | VPN-04：R0 起 clapi 5 秒内 `takes over` + `master changed`（R0 之前的基线是 ≤ 35 秒） |
| 10 | 第 256 个 VRRP 实例起 keepalived 拒绝 | VRID 用自增主键 | VPN-01 `vrid` 列 |
| 11 | 删一个 VRRP 实例打断同 VPC 另一个 | `clear_vrrp_ip.sh` 按共用 MAC 删 fdb | VPN-10 / LB-08 |
| 12 | 节点重启后网关只剩一个节点（2026-09-24） | `recover_vpn_gateway` 回调空指针 panic | `TC-14` HA-08 |
| 13 | 主备切换后 FRR 起不来、BGP 迟 80 秒（2026-09-24） | keepalived notify 的 umask 让 `/var/run/frr/vpn-*` 成 0600 | HA-08、VPN-04 `notify.log` |
| 14 | `sync` 调用持锁 60 秒（2026-09-24） | `set_route_table.sh` 在宿主机 netns 里永远失败 | HA-08、VPN-04 `notify.log` |
| 15 | 仅响应连接永远起不来（2026-09-24） | 多变量 `read` 遇空字段错位 | VPN-02 仅响应 |
| 16 | 并发建客户端分到同一地址（2026-09-24） | GORM v1 的 `FOR UPDATE` 写法被 v2 忽略 | VPN-05 并发 |
| 17 | 网关永远 `pending` / `error` 终态（2026-09-24） | `VrrpReady` 回滚事务；`Update` 只在 `available` 下发；脚本失败不上报 | VPN-01 error 救回 |
| 18 | 创建失败节点残留 VRRP 网卡（2026-09-24） | 公网 IP 在 VRRP 实例之后分配 | VPN-01 占用 IP |
| 19 | 多对网段只回来一个 CHILD_SA（2026-09-24） | 只发起 `net0` | VPN-02 多对 |
| 20 | 改模式后 BGP 一直 `Active`，要手工 restart（2026-09-24） | `grep -A20` 找不到 `start_action`；`close_action = start` 复活旧选择符 | VPN-02 改模式、VPN-03 |
| 21 | 切主后连接 / BGP 状态最长 5 分钟不刷新（2026-09-24） | 上报缓存写在 clapi 接受之前 | VPN-04 |
| 22 | 客户端经网关访问远端站点（2026-09-24，产品决定禁止） | BGP 模式选择符是 `0.0.0.0/0`，加推送路由即可串通 | VPN-05 不互通 |
| 23 | 停用期间连接仍显示 BGP `Established`（2026-09-24） | 停用只清了 `bgp_state`，界面读 `bgp_status` 快照 | VPN-11 |
| 24 | 点「创建」没有反应（2026-09-24） | 默认选中已有网关的 VPC，409 报错在长表单底部看不到且是英文 | VPN-07 创建弹窗 |
| 25 | 客户端与站点的隔离规则丢了不会补（2026-09-24） | 只在建网关时插一次；删网关按通配名删会误删同 VPC 新网关的规则 | VPN-05 不互通 |
| 26 | 站点连接的流量统计永远是 0，且随重协商清零（2026-09-24，与 IBM Cloud 互通时发现） | 正则没考虑 XFRM 接口 SA 在 SPI 后的 `(-\|0x...)`；SA 计数本身每次重协商清零。现读隧道接口计数减接管基线 | VPN-02 流量 |
| 27 | 对端的 SYN-ACK 不做 MSS 钳制（2026-09-24，与 IBM Cloud 互通时发现） | 规则用 `--syn` 只匹配纯 SYN；进隧道方向又按通往虚拟机的 1450 算。现 `--tcp-flags SYN,RST SYN` + 进隧道 `--set-mss` | VPN-02 MSS |
| 28 | 任意成员能用流量接口打挂 clapi（2026-09-24，代码复查） | 只检查开始小于结束，极端值相减溢出、绕过点数上限，时间戳循环耗尽内存 | VPN-02 流量历史的 400 用例 |
| 29 | 流量带上上一任期的量（2026-09-24，代码复查） | 基线只由切换脚本记，守护脚本抢先拉起 charon 时漏记 | VPN-02 基线 |
| 30 | 连接状态回调每次心跳都发（2026-09-24，代码复查） | 按整份负载比较，字节数每次都变 | VPN-02 状态回调频率 |
| 31 | 流量曲线出现几 Gbps 的假尖峰（2026-09-24，代码复查） | 读计数失败时导出 0，`rate()` 当成计数重置 | VPN-02 流量历史 |
| 32 | 快速切换时段后图表是另一个时段的数据、「当前速率」是几小时前的值（2026-09-24，代码复查） | 请求没有代次；当前速率取范围内最后一个非空点 | VPN-07 监控标签 |
| 33 | keepalived 父进程被杀时旧主不让位，其他节点的虚拟机中断 17.7 秒（2026-09-24，IBM 实测） | 子进程摘地址不调切换脚本，看护循环直接退出 | VPN-13 keepalived、VPN-04 |
| 34 | 改 BGP 计时器不生效（2026-09-24，IBM 实测） | 已建立的会话沿用旧的协商值 | VPN-12 |
| 35 | 迁移给已软删除的连接也建了隧道（2026-09-24） | 迁移语句没有带上 `deleted_at` | 部署后查 `vpn_tunnels` 里 `deleted_at is null` 的行都对应存活的连接 |
| 36 | 每次下发配置都重置全部 BGP 会话（R1 起就有，2026-09-24 R2 实测发现） | 生成的配置缺 `neighbor X bfd` 那一行，frr-reload 每次先删 BFD 再加回来 | VPN-14 改配置 |
| 37 | 双活静态连接的备节点走自己的备隧道（2026-09-24） | bgpd 本地起源的路由 weight 32768 压过 local-preference | VPN-14 静态连接 |
| 38 | F7 切回太早，节点恢复时又断两次（2026-09-24） | 连续 3 次应答就切回，那时 BGP 会话还没起来 | VPN-16 |
| 39 | 删公网地址后网关失去主节点，keepalived 一直停在 FAULT（2026-09-24） | 先删地址再重载 keepalived；`clear_lb_floating.sh` 按「口上没有地址」删共用的公网口（负载均衡同样受影响，见 `TC-06` LB-08） | VPN-17 删 vip2 |
| 40 | 两个节点都不持有地址时仍显示原主节点与全部 up（2026-09-24） | 只有持有地址的节点上报，没有人发现上报停了 | VPN-13 无主检测 |
| 41 | 改成静态路由后隧道仍留着 BGP 状态（2026-09-24） | BGP 状态上报与改路由方式并发，把刚清掉的值写了回去 | VPN-12 |
| 42 | 观察者能从客户端配置模板拿到 WireGuard 预共享密钥（2026-09-25 发现） | `Config` 不看写权限，与其他凭据的规则不一致 | VPN-19 |
| 43 | `client_routes` 填 `0.0.0.0/0` 没被拒（2026-09-26 发现，09-27 修并部署实测） | 文档与本用例都写着接口拒绝，代码只校验了格式；拒绝 `0.0.0.0/0` 的 `validateVpnPrefixes` 不管客户端路由 | VPN-05、VPN-01 负面用例 |
| 44 | 单节点 zone 里的网关永远 `pending`，`error=resource` 回调被误读（2026-09-27 修） | `SetVrrpIp` 选不到 BACKUP 只管负载均衡；救回路径写死两个节点；`error=resource` 带的是下发命令的参数 | VPN-18 |
| 45 | 普通节点被补成备节点后，F7 继续改写它的路由（2026-09-27 代码复查发现，未上线即修） | 探测循环按 `nexthop_hosts` 文件遍历、不看本节点角色；`set_vpn_route.sh` 的网关节点分支没清掉普通节点时的状态 | VPN-18 |
| 46 | 双活两条隧道同时断，连接仍显示「降级」、没有 `VpnConnectionDown`（2026-09-27 代码复查） | 两个节点的上报并发处理，各自用对方隧道的旧状态算汇总；`vpnTunnelsDown`（cland 报节点失联时置隧道 down）同样没锁网关行（§18.17 一并修） | VPN-14、VPN-21：对端设备一次断掉两条隧道，连接应直接变「未连接」并发严重告警 |
| 47 | 创建中的网关加减公网地址，节点上不生效（2026-09-27 代码复查） | 下发只在可用 / 错误时做，之后没人补推 | VPN-21：创建中 `POST` / `DELETE .../public_ips` → 400（132006），界面按钮禁用 |
| 48 | clapi 积压时所有节点被 cland 判失联（2026-09-27 代码复查） | 读流协程阻塞在回调队列上，存活消息读不出来 | VPN-21：回调队列过半时 cland 日志不应出现成批的 `liveness: gone` |
| 49 | 删网关后 `vpn_tunnels` 留着孤儿记录（2026-09-27 代码复查） | 删网关只软删连接 | VPN-10、VPN-21：删网关后 `select count(*) from vpn_tunnels where vpn_gateway_id = <id> and deleted_at is null` 为 0 |
| 50 | 同一地址两条只响应隧道被拒（2026-09-27 代码复查） | 地址唯一性按「本端地址 + 对端地址」，对端都空 | VPN-21：两条都不填 `remote_gateway`、`remote_id` 不同 → 200；相同 → 400 |
| 51 | 删隧道 / 连接 / 网关后告警永不解除（2026-09-27 代码复查） | 解除只靠「恢复」上报 | VPN-21：备隧道断着时删掉它，`VpnTunnelDown` 变 resolved |
| 52 | 计算节点能伪造别的节点失联（2026-09-27 代码复查） | `node_liveness` 回调不核对来源 | VPN-21：以 Extra=3 伪造 `node_liveness.sh '1' 'gone'`，clapi 日志 `Ignoring node_liveness`，没有接管、隧道状态不变 |
| 53 | 看护循环起两份（2026-09-27 代码复查） | 查 pid 与写 pid 不是原子的 | VPN-21：并发两次 `vpn_watch_start` 后顶层循环只有 1 个、等于 `watch.pid`（别用 `pgrep -fc`，会数到循环里 fork 的子 shell） |
| 54 | 指定子网网关地址建网关会把上游网关占掉（附录 B 第 5 条 / CLAUDE.md B11，2026-09-27 修并部署） | `AllocateAddress` 指定地址时不排除网关；公网子网的网关行从不标记为已分配。现 `CreateInterface` 对非网关口指定网关地址返回 400，三个地址下拉统一排除 | VPN-01 负面用例：`public_ip` 为 `.145` → 400，`.145` 仍 `allocated=false`；VPN-07 下拉不含 `.145` |
| 55 | 同一块 VRRP 网卡被下发两次时两个节点带着同样的地址和 MAC（既有，2026-09-27 复查后续发现并修） | `SetVrrpIp` 先改节点再读出来比较，重复放置检测是死代码。现先读再记，后到的节点上 `clear_vrrp_ip.sh` 并重发整个路由器的 VRRP fdb | VPN-21：`interfaces` 里同一 VRRP 网卡只落在一个节点（造不出重复下发，只做日常核对） |
| 56 | 站点连接 / 隧道的 `last_error` 永远为空（D13 / FAIL-N4，2026-09-28；2026-09-30 修，2026-10-01 已部署） | `report_vpn_status.sh` 把 `"error":""` 写死；用例原来查的是早已删除的连接级字段 | VPN-02：`tunnels[].last_error` 是 charon 原话、不带重试计数 |
| 57 | 对端不可达时守护脚本在 lb 锁里串行发起隧道、持锁一分钟以上，网关停用在主节点晚 30 秒生效、同节点配置下发排队（D8 / FAIL-N5，2026-09-28；2026-09-30 修，2026-10-01 已部署） | `check_vpn_process.sh` 持锁 `swanctl --initiate --timeout 20` | VPN-11 前三项 |
| 58 | 后台发起与配置下发同时发起同一条隧道，建出重复的 CHILD_SA（2026-09-30 审查时发现并修，2026-10-01 已部署） | 发起前不在锁内核对 SA 状态 | VPN-11 第三项：每条隧道最多一个 CHILD_SA |
| 59 | 删有网关的 VPC 返回 400 131307 而不是 409 132033（D14；2026-09-30 修，2026-10-01 已部署） | 先查浮动 IP，网关的公网地址先被拦 | VPN-09 |
| 60 | `error` 救回时 PATCH 响应里的 `status_reason` 仍是旧值（2026-09-28 观察；2026-09-30 修，2026-10-01 已部署） | 只改了内存里的 `Status` | VPN-01 |
| 61 | VPN 告警可能推给别的组织的通知渠道（D2，2026-09-28；2026-09-30 修，2026-10-01 已部署） | 渠道镜像存的是控制面组织 ID | VPN-08、`TC-10` ALM-07 |
| 62 | 删掉网关节点上 VPC 的最后一台虚拟机会把网关的 netns 与目录一起拆掉（D3 推断；2026-09-30 修，2026-10-01 已部署） | `clear_local_router.sh` 不看 `vpn-*` | VPN-10 第一项 |
| 63 | VPN 网关公网地址的限速入出方向都从没生效（2026-09-30 修，2026-10-01 已部署） | 与负载均衡同一段 `create_lb_floating.sh`，见 `TC-06` 回归点 20 | VPN-01 带宽一项（可选） |
