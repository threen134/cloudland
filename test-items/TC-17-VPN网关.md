# TC-17 VPN 网关（VPN）

优先级 **P1** · 会建 VPC / 虚拟机 / 网关，在计算节点上起 charon / keepalived / FRR / WireGuard · 约 60 min（含主备切换 90 min）

2026-09-23 按 `docs/architecture/plan/vpn-gateway-plan.md` 实施 V1（站点到站点 + BGP）、V2（WireGuard 客户端）、V3（配额 / 审计 / 告警）时的测试用例，已在 work-01/02/03 全量跑通。方案 §14 记了实施中与设计不同的做法、待验证项的结果与没覆盖的项。

> 🚫 **测试网关建在临时 VPC 里，不要建到 `rb-vpc`。** 网关与同 VPC 的 LB 共用 VRRP 子网网卡 `ns-<vni>` 与 MAC，在保留环境里建 / 删网关有打断 `rb-lb` 的风险。全程每做完一步回头确认 `rb-lb` 仍是 10/10。

## 设计约定（判定预期的依据）

- **每个 VPC 最多一个网关** = 一个 VrrpInstance（与 LB 同一张表、同一套 keepalived 守护）+ 一个公网浮动 IP（`floating_ips.vpn_gateway_id`）。主备两个节点由 zone 内 `select=` 调度。
- **主节点**在 VPC 路由器 netns `router-<N>` 里跑：strongSwan **charon**（站点到站点，IKEv2，XFRM 接口 `ipsec-<连接ID>`）、**WireGuard** `wg-<网关ID>`（客户端到站点）、**FRR**（`bgp` 模式的连接；pathspace `-N vpn-<网关ID>`，守护进程 `zebra → mgmtd → staticd → bgpd`）。**备节点只跑 keepalived。**
- 目录：`/opt/cloudland/cache/router/router-<N>/vpn-<网关ID>/`（strongswan.conf / swanctl.conf / wg.conf / run/ / tunnel_routes / *.reported），keepalived 在同级 `vrrp-<VrrpInstanceID>/`；FRR 在 `/etc/frr/vpn-<gw>/frr.conf` 与 `/var/run/frr/vpn-<gw>/`。
- **charon 多实例**：`unshare -m` + `mount --bind <vpn_dir>/run /run` 起（piddir 编译死为 `/var/run/charon.pid`）。`swanctl` 子命令要在 `--uri` **前面**。
- **东西向回程**：其他节点把对端网段指向主节点的 VRRP **单播**地址（`via <master ip> dev ns-<vrrp vlan> onlink`）；主备节点由 `vpn_notify.sh` 按角色装（master 指隧道接口 / 黑洞，backup 指对端，fault 黑洞）；每个节点都加 `nonat` ipset 条目。
- **BGP**：eBGP 跑在隧道内链路地址（`tunnel_local_ip` / `tunnel_peer_ip`）上；对端明细前缀只进主备节点，其他节点只拿 `remote_summary_cidrs`；汇总黑洞是 **staticd** 的 `ip route X Null0 250`，**不是**内核黑洞。
- **主节点身份**：`report_vpn_status.sh` 只在持 VIP 的节点上报 `vpn_master`，每 20 秒重发、接管后第一分钟每次心跳都发；clapi 的分脑窗口 30 秒（旧主 30 秒内还报过就拒绝新声明、只记日志）。
- **状态上报**只接受主节点的：`vpn_conn_status`（IKE SA）、`vpn_client_status`（wg 握手 / 流量）、`vpn_bgp_status`（邻居 / 前缀），变化或 5 分钟重报。
- **客户端**：平台代生成密钥时私钥只在创建响应里出现一次，`GET .../clients/:id/config` 只给不含私钥的模板；`client_routes` 拒绝 `0.0.0.0/0`；`client_dns` 默认不下发。
- **凭据**：PSK / BGP 口令 / 客户端私钥用 `VPN_SECRET_KEY` 加密（`v1:` 前缀），没配时相关接口 503。
- **配额**：cpgateway `vpn_gateways`（默认 1）。**已有配额行 AutoMigrate 后为 0**，测试前确认 Admin 已 `UPDATE org_resource_quotas SET max_vpn_gateways = 5`。

### ⭐ 最容易误判的几点

- **备节点上没有 charon / FRR / wg 是正常的**，只有主节点跑数据面。
- **连接状态 `down` 但节点上 charon 在跑**：先看 `swanctl --list-sas --uri unix://<vpn_dir>/run/charon.vici`，再看对端；两端 PSK / `remote_id` 不一致时 IKE 永远起不来，`last_error` 里有 charon 的原话。
- **客户端重新启用后十几秒才通**：是客户端侧 WireGuard 要等自己重新握手（KEEPALIVE_TIMEOUT + REKEY_TIMEOUT ≈ 15 秒），不是网关。
- **clapi 里的主节点比 VIP 切换晚约 30 秒**：分脑窗口在等旧主静默，数据面早就切过去了（旧主被守护脚本拉起成 BACKUP 后转发给新主）。
- **主节点 keepalived 被杀后旧主上的 charon / FRR 也没了**：`vpn_notify.sh backup` / 守护脚本会把它们停掉，只留 keepalived。

## 测试环境

脚本在 work-01（先 `source /root/st-env.sh`，再 `source /root/vpn-e2e-lib.sh`；状态在 `/root/vpn-e2e.state`，各阶段可以单独重跑）：

| 脚本 | 阶段 |
|---|---|
| `/root/vpn-e2e-lib.sh` | `sv` / `lv` 状态、`on_node`、`gexec`（guest agent 内执行）、`vm_ip` / `vm_host`、`vm_probe`（TCP/22 探测）、`wait_field` |
| `/root/vpn-e2e-1-setup.sh` | 两个 VPC（`vpn-a` 192.168.71.0/24、`vpn-b` 192.168.72.0/24）各 3 台虚拟机分散在三节点 |
| `/root/vpn-e2e-2-gateways.sh` | 两个网关 + 静态站点连接 + 全矩阵探测 |
| `/root/vpn-e2e-3-bgp.sh` | 切到 BGP、VPC B 加子网 192.168.73.0/24 与 vb4，A 自动学到 |
| `/root/vpn-e2e-4-failover.sh` / `-4b-failback.sh` | `kill -9` 主节点 keepalived，探测与切换时间 |
| `/root/vpn-e2e-5-client.sh` / `-5b-disable.sh` | WireGuard 客户端（在 work-03 宿主机上 `wg-quick`）、停用 / 启用 |
| `/root/vpn-e2e-9-fixes.sh` | 2026-09-24 修复的验证：占用 IP 创建 / 仅响应连接 / 多对网段 / 并发客户端 / `error` 救回（会在 VPC A 加子网 `vpn-a-net2`，清理脚本已知道它） |
| `/root/vpn-e2e-11-disable.sh` | 网关停用 / 启用（阶段 3 之后跑）：接口视图、两个节点的标记 / 进程 / WireGuard / 放行规则 / 黑洞路由、流量、停用期间切主、启用后恢复时间、审计 |
| `/root/vpn-e2e-10-isolate.sh` | 客户端与远端站点不互通（在阶段 3 之后跑，两条连接都是 BGP 模式）：放宽推送路由后客户端探测 VPC B、在 VPC B 主节点临时加路由后反向探测客户端，核对两条 `DROP` 的计数；自己建删客户端和临时路由 |
| `/root/vpn-e2e-7-monitor.sh <节点>` | 节点重启跟踪（`TC-14` HA-08），重启命令另发 |
| `/root/vpn-e2e-6-cleanup.sh` | 删网关 / 虚拟机 / 子网 / VPC 并核对节点 |
| `/root/console-test/vpn-ui.js` | 界面只读冒烟（Playwright 容器） |

> ⚠️ 这套脚本的资源名是 `vpn-a` / `vpn-b` / `va1..va3` / `vb1..vb4` / `laptop-1`，**不是** `wp-` 前缀（先于命名约定写成）。收尾检查 C-11 专门按这些名字捞。
> ⚠️ `ubuntu-24.04-minimal` 镜像**没有 `ping`**，连通性一律用 `vm_probe`（`bash /dev/tcp` 打 22 端口，native 安全组放行 22）。

---

## VPN-00 前置

**P1** · 脚本 `/root/vpn-e2e-1-setup.sh`

```bash
ssh work-01 'source /root/st-env.sh; source /root/vpn-e2e-lib.sh
docker exec cloudland-postgres psql -U postgres -d cloudland_cpgateway -Atc "select max_vpn_gateways from org_resource_quotas"
grep -c "^VPN_SECRET_KEY=." /opt/cloudland/deploy/docker/.env
bash /root/vpn-e2e-1-setup.sh'
```

### 检查项

- [ ] `max_vpn_gateways` ≥ 2（否则第二个网关 429 `quota_exceeded`）
- [ ] `.env` 里有 `VPN_SECRET_KEY`（否则建连接 503 `ErrVpnSecretUnavailable`）
- [ ] 6 台虚拟机全部 `running`，`vm_ip va1` 等能取到地址，`vm_probe va1 $(vm_ip va2)` = `ok`
- [ ] 三个节点都有 `router-<A>`、`router-<B>` netns

---

## VPN-01 创建网关

**P1** · 脚本 `/root/vpn-e2e-2-gateways.sh`（前半段）

```bash
# 手工等价（请求体写文件，见 00-环境与前置 的引号陷阱）
api POST /vpn_gateways '{"name":"vpn-a","vpc":{"id":"<vpc uuid>"},"ipsec_enabled":true,"client_enabled":true,"client_cidr":"10.8.0.0/24"}'
api GET /vpn_gateways/<id> | jq '{status, public_ip, master_hostname, nodes}'
```

### 检查项

- [ ] 创建后 `pending` → **约 10 秒** `available`，`public_ip` 是 `public-756` 里的地址，`nodes` 两个节点角色 MASTER / BACKUP
- [ ] `master_hostname` 在一次心跳内（≤ 20 秒）出现，且等于持 VIP 的节点
- [ ] 同一 VPC 再建一个 → **409**（`ErrRouterHasVpnGateway`）
- [ ] 第 N+1 个网关超配额 → 429 `quota_exceeded`，概览页「资源使用情况」多一条「VPN 网关」
- [ ] **指定已被占用的 `public_ip` 创建** → 400，`vrrp_instances` 行数不变，三节点 `router-<新 VPC>` 里没有 `ns-` 网卡，clapi 无 `set_vrrp_ip` 下发
  > **回归点**（2026-09-24）：原先先建 VRRP 实例（已下发 `set_vrrp_ip.sh`）再分配公网 IP，失败只回滚数据库、节点上留网卡
- [ ] **`error` 状态可以救回**：`update vpn_gateways set status='error'` 后 PATCH 任意字段 → `pending` → 两节点 `ready` 后 `available`
  > **回归点**（2026-09-24）：`error` 曾是终态，只能删了重建；`pending` 期间的 PATCH 也不下发。脚本侧 `create_vpn_gateway.sh` 现在失败会上报 `error`（`trap EXIT`），`create_lb_floating.sh` 的退出码不能当失败依据（没设带宽限制时最后一条 tc 清理必然非零）

### 数据库核对

```bash
ssh work-01 'docker exec cloudland-postgres psql -U postgres -d cloudland -Atc "select g.id, g.name, g.status, g.master_hyper, v.hyper, v.peer, v.vrid from vpn_gateways g join vrrp_instances v on v.id=g.vrrp_instance_id where g.deleted_at is null"'
```

- [ ] `vrrp_instances.hyper` / `peer` 落库，两个节点不同
- [ ] `vrid` 在 1–255 之间，**按 VRRP 子网分配**：两个不同 VPC 的网关都拿到 `1` 是正常的
  > **回归点**：VRID 曾直接用 `vrrp_instances` 的自增主键，第 256 个实例起 keepalived 拒绝（`VRID not valid`）。现在是 `vrid` 列，`AutoUpgrade` 回填存量行
- [ ] `floating_ips.vpn_gateway_id` 指向网关

### 节点侧（主节点）

```bash
ssh work-01 'source /root/st-env.sh; source /root/vpn-e2e-lib.sh
m=$(lv master_a); r=$(lv router_a); n=$(lv gwnum_a)
on_node $m "ls /opt/cloudland/cache/router/router-$r/vpn-$n/; pgrep -x charon | wc -l; pgrep -c -f \"vrrp-.*/keepalived.conf\"
ip netns exec router-$r ip -4 -o addr | grep -E \"$(lv vip_a)|ns-|wg-\"
ip netns exec router-$r iptables -S INPUT | grep -E \"500|4500|esp|51820\"
ip netns exec router-$r sysctl -n net.ipv4.conf.all.rp_filter net.ipv4.conf.all.send_redirects"'
```

- [ ] 主节点：VIP 挂在 `te-<router>-756`，charon 1 个，keepalived 2 个进程，`wg-<gw>` 有 `10.8.0.1/32`
- [ ] INPUT 放行 udp 500 / 4500、esp、udp 51820（只对 VIP）
- [ ] `rp_filter=2`、`send_redirects=0`
- [ ] 备节点：只有 keepalived，**没有** charon / FRR / wg（正常）
- [ ] 三个节点 `journalctl -k | grep DENIED` 没有 charon / swanctl / bgpd / staticd / wg 的新增拒绝
  > **回归点**：26.04 的 AppArmor 把这五个都限制在打包路径里，症状是莫名的 `Permission denied`；部署脚本写 `/etc/apparmor.d/local/{usr.lib.ipsec.charon,usr.sbin.swanctl,bgpd,staticd,wg}` 放行

### 涉及接口

`POST/GET/PATCH/DELETE /vpn_gateways{,/:id}`；cpgateway 白名单 17 条；审计 `vpn_gateway.create/update/delete`

---

## VPN-02 静态站点到站点

**P1** · 脚本 `/root/vpn-e2e-2-gateways.sh`（后半段）

```bash
api POST /vpn_gateways/<gw-a>/connections '{"name":"to-b","remote_gateway":"<vip-b>","route_mode":"static","remote_cidrs":"192.168.72.0/24","psk":"VpnTestPsk-2026"}'
api POST /vpn_gateways/<gw-b>/connections '{"name":"to-a","remote_gateway":"<vip-a>","route_mode":"static","remote_cidrs":"192.168.71.0/24","psk":"VpnTestPsk-2026"}'
```

### 检查项

- [ ] 两条连接在 **60 秒内** `status=up`，`established_at` 有值，`if_id` 非 0
- [ ] 主节点 `ipsec-<if_id>` 接口存在、MTU 1360；`swanctl --list-sas --uri unix://.../run/charon.vici` 里 IKE_SA `ESTABLISHED`
- [ ] **路由**：主节点 `192.168.72.0/24 dev ipsec-<id>`；备节点 `via <对端 VRRP 地址>`；第三节点 `via <主节点 VRRP 地址> dev ns-<vrrp vlan> onlink`
- [ ] **nonat**：三个节点 `ip netns exec router-<N> ipset list nonat` 都含对端网段
  > **回归点**：`set_vpn_route.sh` 曾把期望集合按行比较，刚加的 nonat 条目又被删掉（现压成一行比较）
- [ ] 探测矩阵 3×3×2 方向 **18/18**（`va* → vb*`、`vb* → va*`）
- [ ] 主节点 conntrack 里源地址是虚拟机自己的地址（没被 SNAT）
- [ ] 心跳上报：`bytes_in` / `bytes_out` 随探测增长，一条连接的 `last_error` 为空
- [ ] PSK 改错一端 → 该连接 `down`，`last_error` 非空；改回来自动恢复
- [ ] **仅响应连接**（`remote_gateway` 空、`remote_id` 填对端身份、`initiator=false`）：主节点 swanctl 块 `remote_addrs = %any`、`remote { id = <remote_id> }`、提案与 `start_action = none` 各在其位
  > **回归点**（2026-09-24）：脚本用一次 `read -d'\n'` 接 jq 的多行输出，空的 `remote_gateway` 让后面 10 个变量整体错位，这种连接一建就是垃圾配置。现在所有脚本用 `vpn_json_read` 逐字段赋值
- [ ] **多对网段的静态连接**（`local_cidrs` 两个网段）：主节点 `swanctl --list-sas` 有 2 个 `INSTALLED` 的 CHILD_SA；改 `remote_cidrs` 后仍是 2 个
  > **回归点**（2026-09-24）：变更后只重新发起 `--child net0`，第二对网段黑洞
- [ ] **改路由模式 / 网段后不需要手工 restart**：两端各自 PATCH 后 60 秒内主节点的 CHILD_SA 选择符等于配置（BGP 模式 `0.0.0.0/0`），BGP `Established`
  > **回归点**（2026-09-24，两条）：① `create_vpn_ipsec_conf.sh` 用 `grep -A20` 找 `start_action`，块太长永远匹配不到，terminate 后不发起；② `close_action = start` 让 charon 按被关闭 SA 的旧选择符重建，两端更新时序一交错旧选择符永远互相复活，BGP 的 /30 不在任何 SA 里。现在 `close_action = none`，守护脚本每次心跳 `vpn_ipsec_reconcile`（`<vpn_dir>/reconcile-<conn>` 是 60 秒限速戳）

### 涉及接口

`POST/GET/PATCH/DELETE /vpn_gateways/:id/connections{,/:cid}`、`POST .../connections/:cid/restart`；审计 `vpn_connection.*`

---

## VPN-03 BGP 模式与自动学路由

**P1** · 脚本 `/root/vpn-e2e-3-bgp.sh`

```bash
api PATCH /vpn_gateways/<gw-a>/connections/<conn-a> '{"route_mode":"bgp","remote_summary_cidrs":"192.168.72.0/23","local_asn":65010,"peer_asn":65020,"tunnel_local_ip":"169.254.100.1","tunnel_peer_ip":"169.254.100.2","bgp_keepalive":5,"bgp_hold":15}'
api PATCH /vpn_gateways/<gw-b>/connections/<conn-b> '{"route_mode":"bgp","remote_summary_cidrs":"192.168.71.0/24","local_asn":65020,"peer_asn":65010,"tunnel_local_ip":"169.254.100.2","tunnel_peer_ip":"169.254.100.1","bgp_keepalive":5,"bgp_hold":15}'
```

### 检查项

- [ ] 在线切换后隧道自动重建，`status` 回到 `up`
  > **回归点**：静态 → BGP 切换曾只 `swanctl --load-all`，CHILD_SA 保留旧的窄流量选择符，BGP 报文过不去；现在变了的连接块 `--terminate` 再 `--initiate`
- [ ] **约 20 秒**内 `bgp.state=Established`，`bgp.accepted` 含对端明细 `192.168.72.0/24`
- [ ] 主节点 FRR 四个进程都在（`pgrep -f "N vpn-<gw>"` = 4：zebra / mgmtd / staticd / bgpd），`/etc/frr/vpn-<gw>/frr.conf` 有 `ip route 192.168.72.0/23 Null0 250` 与 prefix-list
- [ ] 主节点路由表：明细 `192.168.72.0/24 via 169.254.100.2 dev ipsec-<id> proto bgp`，汇总 `192.168.72.0/23` 是 `blackhole ... proto static`（staticd 的）
  > **回归点**：汇总黑洞曾是内核路由，与 BGP 学到的同前缀路由并存时 zebra 永远不装 BGP 明细。现在黑洞由 staticd 持有；`vpn_apply_routes` 看到 `Known via "static"` 就不再装内核黑洞
- [ ] 备节点 / 第三节点只有 **汇总** `192.168.72.0/23`，没有明细
- [ ] 探测矩阵 18/18
- [ ] VPC B 加子网 `192.168.73.0/24` + 虚拟机 `vb4` 后，**不改任何连接**，A 在 120 秒内学到（`bgp.accepted` 含它），`va1..3 → vb4` 4/4
- [ ] B 的 `effective_local_cidrs` 含新子网，`bgp.advertised` 里也有
- [ ] `/etc/frr/frr.conf`（默认那份）**没有被覆盖**
  > **回归点**：`frr-reload.py` 不带 `--confdir /etc/frr/vpn-<gw>` 时，reload 里的 `write` 会把默认 `/etc/frr/frr.conf` 覆盖掉
- [ ] 主备两台 `journalctl -k | grep DENIED | grep bgpd` 只剩 `task/<tid>/comm`（已放行）或为空

---

## VPN-04 主备切换与切回

**P1** · 脚本 `/root/vpn-e2e-4-failover.sh`（首次）、`/root/vpn-e2e-4b-failback.sh`（可反复跑，每次把当前主杀掉）

```bash
ssh work-01 'bash /root/vpn-e2e-4b-failback.sh'
ssh work-01 'docker logs cloudland-clapi --since 5m 2>&1 | grep -E "master changed|split brain"'
```

### 实测基线（2026-09-23，探测每秒一次、90 次）

| 项 | 值 |
|---|---|
| VIP 切换 | 3 秒内（keepalived 通告间隔） |
| 第三节点间 TCP 探测中断 | **0–26 秒**，90 次失败 6–10 次 |
| clapi 记录的主节点切换 | **28 秒**（改成每心跳重发前是 50–62 秒） |
| 隧道在新主重建 | `status=up` 在切换后 90 秒内 |

### 检查项

- [ ] 新主：`vip=1 charon=1 frr=4`；旧主：`vip=0 charon=0 frr=0`，keepalived 被守护脚本拉起（`keepalived=2`）并处于 BACKUP
- [ ] 第三节点的路由在 clapi 切主后重指向新主的 VRRP 地址
- [ ] clapi 日志一条 `claims master while node X reported Ns ago, possible split brain, keeping routes`（旧主最后一次上报还在 30 秒窗口内时的正常拒绝）紧接着一条 `master changed from X to Y`
  > **回归点**：接管后主节点身份曾只按 20 秒周期重发，被拒一次要再等 20–40 秒，切换 50–62 秒；现在接管后第一分钟每次心跳都发
- [ ] 探测中断窗口 ≤ 30 秒，失败次数 ≤ 10/90
- [ ] 再跑一次 4b 切回，结果同上；`rb-lb` 全程 10/10
- [ ] 新主的 `<vpn_dir>/notify.log`（2026-09-24 起每次角色变化都记分步时间戳）：`master start` → `charon started` → `frr started` → `done` **2 秒内**；接着 clapi 切主后的 `sync` 调用 `set_route_table done (rc=0)` 也在 1 秒内
  > **回归点**（2026-09-24）：FRR 在 keepalived 上下文起不来（umask → 运行目录 0600）、`sync` 路径在宿主机 netns 跑 `set_route_table.sh` 白等 60 秒，详见 `TC-14` HA-08。`kill -9` 场景下两者被 `check_vpn_process.sh` 和 cloudlet 上下文掩盖，只把中断拉长到 20 多秒；真实重启才暴露成 80 秒
- [ ] 真实重启主节点见 `TC-14` HA-08（WireGuard 客户端在切换后对新主可用已在那里验证）

---

## VPN-05 WireGuard 客户端

**P1** · 脚本 `/root/vpn-e2e-5-client.sh`（客户端在 work-03 宿主机的 default netns 里 `wg-quick up`）

```bash
api POST /vpn_gateways/<gw-a>/clients '{"name":"laptop-1","preshared_key":true}'      # 平台代生成密钥
api GET  /vpn_gateways/<gw-a>/clients/<cid>/config                                    # 模板，不含私钥
```

### 检查项

- [ ] 创建响应带 `private_key`（44 字符）与完整 `config`（`[Interface]` / `[Peer]`，`Endpoint = <VIP>:51820`，`AllowedIPs` = VPC 子网，`PersistentKeepalive = 25`）
- [ ] `GET .../config` 的文本里是 `your private key` 占位，**没有**私钥
- [ ] 客户端 `wg-quick up` 后 3 秒内握手；到 VPC A 三台虚拟机 **3/3**；`va1 → 10.8.0.2:22`（work-03 宿主机 sshd）反向可达
- [ ] 一次心跳后 `GET .../clients/<cid>` 的 `last_handshake_at` 非空、`bytes_in/out` > 0
- [ ] 主节点 `wg show wg-<gw>` 里 peer 的 allowed ips = `10.8.0.2/32`
- [ ] **并发创建两个客户端**（两条 POST 同时发）：地址不同（`.2` / `.3`），`wg.conf` 两个 `[Peer]`
  > **回归点**（2026-09-24）：`Set("gorm:query_option","FOR UPDATE")` 在 GORM v2 里被忽略，地址分配无锁；现在 `clause.Locking`
- [ ] 带 `client_routes: "0.0.0.0/0"` 建客户端 → 400（全流量模式按设计拒绝）
- [ ] 用户自带公钥（`public_key` 传入）时响应**没有** `private_key`
- [ ] **客户端与远端站点不互通**（BGP 模式最容易漏）：网关 A 的 `client_routes` 加上 VPC B 的网段、客户端重新 `wg-quick up` 后，客户端到 VPC B 虚拟机探测 **0/3**；主节点 `iptables -vnL FORWARD` 里 `-i wg+ -o ipsec+ DROP` 的计数随探测增加，`ipsec-<conn>` 的发送计数不变；反方向在 VPC B 主节点临时加 `10.8.0.0/24 dev ipsec-<conn>` 路由后从 vb1 探测客户端，网关 A 上 `-i ipsec+ -o wg+ DROP` 计数增加、探测失败（测完删路由）
  > **回归点**（2026-09-24）：客户端与站点之间禁止互通，`advertise_client_cidr` 已删除；两个节点的路由器 netns 都要有这两条规则，节点重启恢复后也要在
- [ ] 手工删掉主节点上的两条隔离规则，**一次心跳内**（≤ 20 秒，实测 4 秒）被守护脚本补回，仍在 `FORWARD` 最前面、没有重复；在网关所在节点对一个不存在的网关 id 跑 `clear_vpn_gateway.sh <router> 99999`，现有网关的隔离与 MSS 规则都不被删
  > **回归点**（2026-09-24）：规则原先只插一次，删旧建新的时序交错或插入失败都会让隔离静默失效

---

## VPN-06 客户端停用 / 启用

**P1** · 脚本 `/root/vpn-e2e-5b-disable.sh`

### 检查项

- [ ] `PATCH {"enabled":false}` 后 **4 秒内**主节点 `wg show wg-<gw> peers` 少一个，客户端探测失败
- [ ] `PATCH {"enabled":true}` 后 peer 回来，客户端 **≤ 20 秒**恢复（实测 13 秒，是客户端重新握手的时间）
- [ ] 删除客户端后地址 `10.8.0.2` 释放，再建一个拿到同一地址

---

## VPN-07 界面冒烟

**P1** · 脚本 `/root/console-test/vpn-ui.js`（只读，网关与客户端要先存在）

```bash
ssh work-01 'cd /root/console-test && docker run --rm --network host -v /root/console-test:/work -w /work \
  -e ADMIN_PASSWORD="$(grep "^ADMIN_PASSWORD=" /opt/cloudland/deploy/docker/.env | cut -d= -f2-)" \
  mcr.microsoft.com/playwright:v1.55.0-noble node vpn-ui.js 2>&1 | tail -20'
```

### 检查项

- [ ] 侧边栏「网络」下有「VPN 网关」
- [ ] **列表页首次进入就有数据**（两行、状态「可用」、主节点列、公网 IP 列）
  > **回归点**：`VpnGateways.vue` 的 `onMounted` 曾只加载 VPC 列表、没调 `fetchGateways()`（列表页的 VPC 筛选 2026-09-24 已去掉：每个 VPC 最多一个网关，筛选结果只有 0 或 1 条；VPC 列表只用于创建弹窗），`useListQuery` 又只在依赖变化时重载，页面永远显示「未发现 VPN 网关」。typecheck / lint 抓不到，只有真跑才发现
- [ ] 详情页标题区 43px、四个标签（概览 / 站点连接 N / 客户端 N / 操作记录）
- [ ] 站点连接标签：路由方式 `BGP` 胶囊、状态「已连接」、远端网段、BGP 状态 `Established`
- [ ] 客户端标签：`laptop-1`、`10.8.0.2`、握手时间
- [ ] 操作记录标签：有本轮的创建 / 更新记录（动作文案 `vpn_gateway.*` / `vpn_connection.*` / `vpn_client.*`）
- [ ] 概览页「资源使用情况」有「VPN 网关」进度条
- [ ] **创建弹窗**：已有网关的 VPC 标注「已有 VPN 网关」且不可选，默认选中第一个没有网关的 VPC；全都有网关时下拉显示「没有可用的 VPC」并给出提示
- [ ] **创建弹窗的可用区与公网 IP 是下拉**：可用区默认「自动（默认可用区）」，默认可用区带「· 默认」；公网 IP 在选公网子网前禁用，选后列出空闲地址且**不含子网网关地址**，子网改回「自动选择」时地址清空；请求体带 `zone` / `public_subnet` / `public_ip`。负载均衡的创建弹窗可用区同样是下拉
  > **回归点**（2026-09-24）：`GET /addresses/:subnet` 会列出子网网关那一行（未分配），clapi 指定地址分配时又不排除网关，照抄弹性 IP 页的过滤会把上游网关地址给出去
- [ ] **弹窗报错在底栏**（创建 / 编辑网关、连接、客户端）：两个能力都关、后端 409 等报错显示在按钮左侧，窗口 800px 高也不用滚动就能看到；「该 VPC 已有网关」是中文
  > **回归点**（2026-09-24）：报错原先放在表单最底部，长表单要往下滚才看得到，默认又选中了已有网关的 VPC，用户点「创建」后以为没有反应（后端其实回了 4 次 409）
- [ ] 0 页面错误
- [ ] 截图在 `/root/console-test/shots/vpn-*.png`

---

## VPN-08 配额、审计与告警

**P2** · 本轮只核对了配额与审计，告警事件**没有单独核对**

- [ ] cpgateway 用量 `org_resource_consumptions.vpn_gateways` 随建 / 删增减；登录对账后仍正确
- [ ] 删网关时连带释放它的公网 IP 用量（`public_ips` 减 1）
- [ ] `GET /activities` 里有 `vpn_gateway.create`、`vpn_connection.create/update`、`vpn_client.create/update/delete`、`vpn_gateway.delete`
- [ ] 连接从 `up` 变 `down`（拔掉对端 / 改错 PSK）后 `alarm_events` 多一条 `fingerprint=vpn-connection-<id>-*`，恢复后 `resolved`；有通知渠道时 `alarm_delivery_logs` 有发送流水
- [ ] 系统设置 `DEFAULT_VPN_GATEWAYS`（默认 1）可改、范围 0–100000

---

## VPN-09 接口校验（负面用例）

**P2** · 本轮**未逐条执行**（实现在 `apis/vpn.go` 的校验器里）

- [ ] `remote_cidrs` 与本 VPC 子网重叠 → 400 `ErrVpnCidrConflict`
- [ ] 两条连接的对端网段重叠 → 400
- [ ] `bgp` 模式缺 `local_asn` / `peer_asn` / `tunnel_*_ip` / `remote_summary_cidrs` → 400
- [ ] `tunnel_local_ip` 与 `tunnel_peer_ip` 不在同一 /30 → 400
- [ ] `client_cidr` 与 VPC 子网 / 对端网段重叠 → 400
- [ ] 删除有网关的 VPC → 409（`ErrRouterHasVpnGateway`）；删除被 VRRP 实例占用的 VRRP 子网 → 400
- [ ] 非本组织的网关 → 404；只读成员写操作 → 403
- [ ] `.env` 没有 `VPN_SECRET_KEY` 时建连接 / 客户端 → 503

---

## VPN-11 网关停用 / 启用

**P1** · 脚本 `/root/vpn-e2e-11-disable.sh`（在阶段 3 之后跑，两条连接都是 BGP 模式；自己建删一个客户端）

```bash
api PATCH /vpn_gateways/<gw-a> '{"enabled":false}'
api PATCH /vpn_gateways/<gw-a> '{"enabled":true}'
```

### 检查项

- [ ] 停用：响应 `enabled=false`、`status` 仍是 `available`，连接 `status=disabled`，**BGP 快照清空**（不显示 `Established`）
- [ ] 两个 VRRP 节点 2 秒内：网关目录有 `disabled` 文件，charon / bgpd 不在，`wg-<gw>` DOWN，公网地址的 INPUT 放行 0 条，对端汇总网段与客户端地址池都是 `blackhole`；`notify.log` 有 `disabled: routes blackholed, processes stopped`
- [ ] 流量：VPC A↔B 探测 0/6，客户端不通
- [ ] 45 秒（两次心跳）后守护脚本没有拉起任何进程，连接仍是 `disabled`（上报被忽略）
- [ ] 停用期间重启连接 → 400（132008）；两个能力都关 → 仍被拒绝
- [ ] 停用期间 `kill -9` 主节点 keepalived：新主节点 notify 走停用分支、不起进程，旧主拉起成 BACKUP 后同样；探测仍 0/6
- [ ] 启用：新主节点 2 秒内起 charon / FRR；VPC A↔B 与客户端 **≤ 30 秒**恢复（实测 20 秒），连接回到 `up` / `Established`
- [ ] 审计：`vpn_gateway.disable` / `vpn_gateway.enable`（纯启停请求才用这两个动作名）
- [ ] 界面：操作菜单"停用"有确认弹窗；停用后标题 / 列表显示"已停用"、顶部横幅带"启用"、连接"已停用"、重启按钮禁用；编辑弹窗两个能力都关、有连接时关站点到站点都给中文提示
  > **回归点**（2026-09-24）：第一版停用只清了 `bgp_state` 列，界面读的是 `bgp_status` 快照，停用期间仍显示 `Established`

---

## VPN-10 删除与清理

**P1** · 脚本 `/root/vpn-e2e-6-cleanup.sh`

```bash
ssh work-01 'bash /root/vpn-e2e-6-cleanup.sh'
ssh work-01 'for h in local work-02 work-03; do echo "== $h"
  c="echo charon=\$(pgrep -x charon | wc -l) frr=\$(pgrep -f \"[N] vpn-\" | wc -l) vpn_dirs=\$(ls -d /opt/cloudland/cache/router/router-*/vpn-* 2>/dev/null | wc -l) frr_etc=\$(ls -d /etc/frr/vpn-* 2>/dev/null | wc -l) frr_run=\$(ls -d /var/run/frr/vpn-* 2>/dev/null | wc -l)"
  if [ "$h" = local ]; then sh -c "$c"; else ssh -n root@$h "$c"; fi; done'
```

### 检查项

- [ ] `DELETE /vpn_gateways/:id` → 204；网关删除时连接、客户端一并删，浮动 IP 释放（`floating_ips where vpn_gateway_id>0` 为 0）
- [ ] 主节点上 `vpn-<gw>` 目录、charon、FRR 四进程、`wg-<gw>` / `ipsec-*` 接口、INPUT 规则、fdb 条目全部清零；备节点与第三节点的路由、nonat 条目清零
- [ ] `vrrp_instances` 行删除，VPC 里没有别的 VRRP 实例时 VRRP 子网一并删除、`router.vrrp_subnet_id` 清零
- [ ] **删完后 clapi 无条件重发同路由器的 fdb**（`common.ResyncRouterVrrpFdb`）：同 VPC 若还有 LB，其 VRRP 单播心跳不受影响、不出现双主
  > **回归点**：`clear_vrrp_ip.sh` 按 MAC 删 fdb，而同 VPC 同节点的所有 VRRP 接口共用一个 MAC，删一个实例会把另一个实例到对端的转发条目一起删掉 → 双主。见 `TC-06` LB-08
- [ ] 虚拟机 / 子网 / VPC 删除后三节点 `router-<A>` / `router-<B>` netns 消失
- [ ] 三节点 `/etc/frr/vpn-*`、`/var/run/frr/vpn-*` 为空；`/etc/frr/frr.conf` 仍是原样
- [ ] `rb-lb` 10/10；clapi 日志无 `no command`、无 `panic`
- [ ] 三节点 `/var/run/frr/vpn-*` 的目录（存在时）是 `drwxr-xr-x frr frr`（见 HA-08 的 umask 回归点）

---

## 清理

上面 VPN-10 就是清理。若脚本中途失败，按依赖顺序手工删：**客户端 → 连接 → 网关（等节点回调） → 虚拟机 → 子网 → VPC**。节点上残留的进程 / 目录可以直接 `pkill -x charon`、`pkill -f "N vpn-<gw>"`、`rm -rf .../vpn-<gw> /etc/frr/vpn-<gw> /var/run/frr/vpn-<gw>`，随 VPC 删除的 netns 会带走接口与路由。

---

## 已知噪音（不要当故障）

| 输出 | 来源 |
|---|---|
| `apparmor="DENIED" ... profile="wg-quick" comm="iptables-save"` | **客户端所在宿主机**（work-03）上 `wg-quick` 的 AppArmor 限制，与网关无关 |
| `apparmor="DENIED" ... profile="bgpd" name="/proc/<pid>/task/<tid>/comm"` | FRR 线程改名，已在本地覆盖里放行；旧节点没更新覆盖时仍会出现，无害 |
| `Error: argument "br<N>" is wrong: Device does not exist` | 新建 VPC 路由器时 `create_veth.sh int-<N>`，见 `TC-03` |
| `vpn_gateway_<vrrp>) Entering BACKUP STATE (init)` | 守护脚本拉起 keepalived 时的正常状态 |

## 测试脚本自身的坑

- `pkill -f` 在 `ssh` 会话里会匹配到**自己这条 ssh 的命令行**，模式要写成 `[v]pn` 这种；变量为空时 `pkill -f ""` 会杀掉节点上**所有**进程（2026-09-23 真实发生过一次，work-01 的 NetworkManager / cloudlet-go / dnsmasq 都被杀）——先判空再 pkill。
- 经 ssh 传多层引号 + awk 极易炸，诊断命令先写成文件（`/root/vpn-diag-*.sh`）再执行。
- 长 python heredoc 在本地 Bash 工具里会断，补丁脚本先 Write 成文件再跑。

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
| 9 | 主备切换后第三节点路由 50–62 秒才重指 | 主节点声明被拒后要等下个 20 秒周期 | VPN-04 `master changed` ≤ 35 秒 |
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
