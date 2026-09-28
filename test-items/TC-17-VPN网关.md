# TC-17 VPN 网关（VPN）

优先级 **P1** · 会建 VPC / 虚拟机 / 网关，在计算节点上起 charon / keepalived / FRR / WireGuard · 约 60 min（含主备切换 90 min）

2026-09-23 按 `docs/architecture/plan/vpn-gateway-plan.md` 实施 V1（站点到站点 + BGP）、V2（WireGuard 客户端）、V3（配额 / 审计 / 告警）时的测试用例，已在 work-01/02/03 全量跑通。方案 §18 记了实施中与设计不同的做法和测出来的问题，§17 是没做与没测的项（2026-09-25 起两份 VPN 设计文档已合并为 `vpn-gateway-plan.md`，旧章节号见其附录 D）。

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
- [ ] **流量统计**：打一段流量后 `GET /vpn_gateways/<gw>` 的连接 `bytes_in` / `bytes_out` 随之增长（ping 的字节数可以按包长精确核对）；等一次 CHILD_SA 重协商（`esp_lifetime` 设 300 可以快一点）后不清零；切主后 `established_at` 刷新、计数从 0 开始
  > **回归点**（2026-09-24）：原先永远是 0（解析不到），修好解析后也会随重协商清零；现在读隧道接口计数
- [ ] **基线随 charon 启动重记**：主节点 `kill -9` 该网关的 charon（pid 在 `<vpn_dir>/run/charon.pid`），守护脚本拉起后 `<vpn_dir>/traffic.base` 的修改时间与新 charon 启动时间同一秒；`GET` 里连接的 `bytes_in/out` 等于 `ipsec-<if_id>` 的 `rx/tx_bytes` 减 `traffic.base` 里的值
  > **回归点**（2026-09-24）：基线原先只由切换脚本记，守护脚本先拉起 charon 时漏记，流量带上一任期的量
- [ ] **状态回调频率**：隧道空闲、状态不变时，主节点 `<vpn_dir>/status.reported`、`wg.reported` 的修改时间约每 60 秒变一次（采样 3 分钟 3–4 次），而不是每次心跳
  > **回归点**（2026-09-24）：修好流量统计后字节数每次都变，连接状态回调变成每 1–20 秒一次
- [ ] **MSS**：主节点 `ip netns exec router-<N> iptables -t mangle -S FORWARD` 有 4 条 `--tcp-flags SYN,RST SYN` 规则（`-o` 两条 `--clamp-mss-to-pmtu`，`-i ipsec+` `--set-mss 1320`，`-i wg+` `--set-mss 1350`），没有 `--syn` 的旧规则；主节点抓 `tcp[13] & 2 != 0`，对端 SYN-ACK 出 `ns-` 时 MSS ≤ 1320；虚拟机 `ip route flush cache` 后建连，`ip route get <对端地址>` 不出现 `mtu 1360`
  > **回归点**（2026-09-24）：`--syn` 不匹配 SYN-ACK，对端的 MSS 原样到达虚拟机，靠路径 MTU 探测兜底
- [ ] **流量历史**：主节点 `/var/lib/node_exporter/cloudland_vpn.prom` 有该网关每条连接 `direction="in"/"out"` 两行（备节点没有该网关的数据行）；Prometheus 查得到 `cloudland_vpn_connection_bytes_total{gateway_id="<id>"}`，`hostname` 是主节点；打一段已知速率的流量后 `GET /vpn_gateways/<gw>/traffic?start=&end=&step=60s` 对应连接的速率与之相符（bit/s）；以下返回 400：30 天配 60 秒步长（点数超限）、跨度 32 天、结束时间在 2 小时后、开始 / 结束取 ±9223372036854775000（溢出）、步长 `1500ms`；`step=1.5m` 按 90 秒返回 200。指标文件里读不到计数的设备不出现（不导出 0）

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
- [ ] 建网关、改网关时 `client_routes` 含 `0.0.0.0/0`（或任何 `x.x.x.x/0`）→ 400（全流量模式按设计拒绝；它是网关的字段，不是客户端的）；界面上建 / 编辑网关直接在底栏提示、不发请求（`gw-exclude.js`，副本在 work-01 `/root/console-test/`，在本机对 5173 跑）
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
- [ ] **「监控」标签**：进详情页不请求 `/traffic`，切到「监控」才请求；一次一张整行宽的图：只开站点到站点的网关没有切换按钮，两种都开时图上方有「站点连接 / 客户端」切换且切换不再请求；每个对象入 / 出两条线颜色不同，右侧显示当前速率；没有数据时只有一行提示；客户端 ≤ 4 个逐个画线、更多时画合计；切 6 小时后请求的 `step=5m`；手机宽度无横向溢出；先点 30 天再立刻点 1 小时，30 天的响应晚到时不覆盖图表、高亮仍是 1 小时；末尾几个点没有数据时不显示当前速率（`vpntraffic2.js`、`vpntraffic3.js` 用浏览器内改写返回覆盖只开站点到站点、两种都开、多客户端、乱序响应与过时速率，只读；副本在 work-01 `/root/console-test/`，`BASE` 是公网地址，本机跑时改成 `http://127.0.0.1:5173`；需要环境变量 `GW_ID`（带站点连接的网关）与 `ADMIN_PASSWORD`；另有 `/root/console-test/vpn-traffic-view.js`）
- [ ] **弹窗报错在底栏**（创建 / 编辑网关、连接、客户端）：两个能力都关、后端 409 等报错显示在按钮左侧，窗口 800px 高也不用滚动就能看到；「该 VPC 已有网关」是中文
  > **回归点**（2026-09-24）：报错原先放在表单最底部，长表单要往下滚才看得到，默认又选中了已有网关的 VPC，用户点「创建」后以为没有反应（后端其实回了 4 次 409）
- [ ] **1440 宽下站点连接表格不横向滚动**：操作列在屏幕内，展开行两张隧道卡片完整；隧道行是状态圆点（悬停显示状态文字）、BGP 连接有 BGP 标签（悬停显示 BGP / BFD 状态）；1600 以下没有流量列（`vpn-conn-table-fit.js`，work-01 `/root/console-test/`，在本机对 5173 跑）。2026-09-27 起 1366、1280、1024 同样不横向滚动（1440 以下没有远端网段列；1280 以下路由方式在名称下面、隧道行可以换行）
- [ ] **对端配置弹窗**（连接操作列的「对端配置」）：静态双隧道的连接列出网段对、主隧道是哪条、「对端较小地址」建议，主隧道没对着较小地址时有黄色警告；BGP 连接列出两端 ASN、隧道内 /30 地址、计时器、BFD、接受范围，且**不出现**「较小地址」建议；双活负载分担的连接标「负载分担」、带节点名、有双活提示、没有「主隧道是隧道」；有写权限时隧道卡片带预共享密钥（打码，可显示 / 复制，隧道自己的密钥优先）、BGP 口令同理，顶部黄色提示含明文密钥，复制与下载的文本是明文；观察者看到「只对编辑者及以上角色显示」、文本里没有密钥（`vpn-secrets-ui.js`，work-01 `/root/console-test/`，在本机对 5173 跑，后端未部署前用浏览器改写返回模拟编辑者）；「复制全部」与「下载 .txt」内容一致（Windows 剪贴板换行是 CRLF，比较前归一）；英文界面不混中文标点；390 宽下隧道卡片与底栏按钮都在弹窗内。脚本 `vpn-peer-config.js`（副本在 work-01 `/root/console-test/`，在本机对 5173 跑，只读，需要 `ADMIN_PASSWORD`；`ibmx-gw` 的 `to-ibm` / `to-ibmr-bfd` 与 `aax-gw` 的 `to-ibmx-aa`）
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
- [ ] **凭据回读**：编辑者 / 系统管理员的 `GET /vpn_gateways/:id`、连接列表与详情带 `psk`、`bgp_password`（隧道有自己的密钥时 `tunnels[].psk` 也有），观察者没有这些字段、只有 `*_set`；观察者下载客户端配置得到 `PresharedKey = <preshared key>`，编辑者得到真实值；换掉 `VPN_SECRET_KEY` 后查询仍是 200，只是没有这些字段（clapi 日志 `VPN credential not returned`）

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

## VPN-12 主备双隧道与双公网地址

**P1** · 需要一个对端有两个地址的环境，比如 IBM Cloud 的 route 模式 VPN 网关（它的一条连接自带两条隧道，每个成员地址一条）。work-01 上的 IBM 测试环境：搭建脚本 `/root/ibmr-1-setup.sh`，状态在 `/root/ibmr.state`，连接的建法见 `/root/ibmr-conn.sh`（BGP）与 `/root/ibmr-static.sh`（静态）。方案见 `docs/architecture/plan/vpn-gateway-plan.md` §4.1、§18.7。

```bash
# 两个公网地址
api POST /vpn_gateways '{"name":"...","vpc":{"id":"..."},"ipsec_enabled":true,"public_ips":[{"public_subnet":{"id":"<pub>"}},{"public_subnet":{"id":"<pub>"}}]}'
# 两条隧道（BGP），列表位置就是隧道号
api POST /vpn_gateways/<gw>/connections '{"name":"...","route_mode":"bgp","psk":"...","local_asn":65010,"peer_asn":64520,"remote_summary_cidrs":"...",
  "tunnels":[{"endpoint":"vip1","remote_gateway":"<对端1>","tunnel_local_ip":"169.254.100.2","tunnel_peer_ip":"169.254.100.1","priority":"primary"},
             {"endpoint":"vip2","remote_gateway":"<对端2>","tunnel_local_ip":"169.254.100.14","tunnel_peer_ip":"169.254.100.13","priority":"standby"}]}'
```

### 检查项

- [ ] 双地址网关：`public_ips` 两项；主节点公网口上有两个地址，备节点没有；两个节点各地址 3 条 INPUT 放行（500 / 4500 / ESP）和一对策略路由规则；keepalived 的 `virtual_ipaddress` 两行
- [ ] 配额：创建双地址网关预扣 2 个公网 IP，删除时释放 2 个
- [ ] 两条隧道都 `up`，连接 `up`；swanctl 里两个 IKE 会话 `c<连接>-t1` / `-t2`，从地址 2 出发的那条本端是地址 2
- [ ] BGP：`show bgp ipv4 unicast <对端网段>` 主隧道那条 localpref 200 并被选中，备隧道 100；发往备隧道的通告带 `65010 65010` 前置，发往主隧道的没有
- [ ] 静态：`tunnel_routes` 目标是 `ipsec-<主>,ipsec-<备>`；内核路由走主隧道
- [ ] 阻断主隧道（主节点路由器 netns 里丢弃到对端 1 的包）：连接变 `degraded`，流量切到备隧道；恢复后回到 `up`、路由切回主隧道
- [ ] 校验：两条隧道本端和对端地址都相同 → 400；网关只有一个地址时用 `vip2` → 400；两个 `primary` → 400；BGP 两条隧道的隧道 IP 同一个 /30 → 400
- [ ] 旧写法兼容：不带 `tunnels`、只带顶层 `remote_gateway` 等字段 → 建成单隧道连接
- [ ] 界面：创建网关可选 1 个 / 2 个地址；连接表的隧道列每条隧道一行、带主 / 备和状态；`degraded` 显示「降级」；展开后每条隧道一张卡片；编辑弹窗回填两条隧道；两条隧道时重启可选单条
  > **回归点**（2026-09-24）：改 BGP 计时器后已建立的会话不会按新值重新协商（现在下发时对计时器变了的邻居清一次会话）；从 BGP 改成静态后隧道上残留旧的 BGP 状态

---

## VPN-13 快速切换

**P1** · work-01 `/root/ibmr-fo.sh <tunnel1|keepalived|linkcut> [秒数]`：三台虚拟机每 0.1 秒 ping 对端，第 20 秒注入故障，按连续丢包分段报告中断，并列出 clapi 看到的切主时间、cland 的失联判定、charon 日志。**模拟断网要用 `linkcut`（`tc netem` 丢弃出向帧），不要用 iptables：它拦不住 ARP，被隔离的节点会继续回应公网地址的 ARP，结果不可信。**

### 检查项

- [ ] 主节点上每个网关一个看护循环：`pgrep -af 'vpn_watch[.]sh'`；备节点上没有；杀掉后下一次心跳（≤ 20 秒）被守护脚本拉起
- [ ] 立即上报：主节点切换或隧道状态变化后，`/run/cloudland/vpn-report.trigger` 被改写，clapi 3 秒内看到新状态（不等心跳）
- [ ] 存活信号：cland 日志在节点断网后约 4 秒出现 `Node <id> liveness: gone`，恢复后出现 `ok`；clapi 日志 `cland reports node <id> as gone`
- [ ] `keepalived`：clapi 日志 `takes over ... (the old master released the address)`，切主在 5 秒内；新主节点 2 秒内建好隧道；所有节点上的虚拟机中断 ≤ 对端 BGP 保持时间 + 1 秒（IBM、计时器 3/9 秒时实测 8.8 秒）
- [ ] `linkcut`：clapi 日志 `takes over ... (cland lost the old master)`，切主在 6 秒内；其他节点上的虚拟机中断 ≤ 12 秒（实测 9.8 秒）；恢复后切回原主节点再中断一次（实测 9.9 秒）
- [ ] `tunnel1`（BGP，计时器 3/9 秒）：中断 ≤ 10 秒（实测 7.0 秒）；默认 10/30 秒时约 23 秒
- [ ] 静态双隧道 `tunnel1`：中断约 21 秒（对端失效检测 10 秒 + 三次重传约 11 秒）
- [ ] IKE 重传参数：`strongswan.conf` 里有 `retransmit_tries = 3`、`retransmit_timeout = 1.5`、`retransmit_base = 1.4`；新建连接的 `dpd_delay` 默认 10
- [ ] BFD（两个 CloudLand 网关互为对端，work-01 `/root/bfd-setup.sh [间隔毫秒]` 建连接，`/root/bfd-fo.sh [秒数] [tunnel|keepalived]` 测切换，`/root/bfd-set.sh '<PATCH 内容>'` 改两端参数）：主节点 `vtysh -N vpn-<gw> -c 'show bfd peers'` 每条隧道一个会话、状态 up、间隔与配置一致，接口是 `ipsec-<id>`；接口返回的隧道 `bfd_state` 为 up；内核日志没有 bfdd 的 DENIED
- [ ] BFD 切换：只阻断主隧道，300 毫秒 × 3 中断 ≤ 1.5 秒（实测 0.7 秒），1000 毫秒 × 3 ≤ 4 秒（实测 2.4 秒），关掉 BFD 约 24 秒；关掉 BFD 后 `show bfd peers` 为空、BGP 会话不中断
  > **回归点**（2026-09-24）：keepalived 父进程被杀时，子进程自己摘掉公网地址却不调切换脚本，看护循环随之退出而不让位，其他节点的虚拟机要等 30 秒窗口（17.7 秒）；现在看护循环发现地址丢失会降级并立即上报
- [ ] 没有节点持有公网地址（`/root/ibmx-nomaster.sh [秒数]`：两个节点的公网口 down 150 秒）：约 90 秒后 clapi 日志出现 `no node claimed its public address`，接口里主节点为空，连接为 down，每条连接一个 `VpnConnectionDown`；公网口恢复后 10 秒内主节点与状态复原、告警解除
  > **回归点**（2026-09-24）：原先只有持有地址的节点上报，两个节点都 FAULT 时 clapi 一直显示原来的主节点和全部 up（实际中断了 9 分钟）

---

## VPN-14 双活模式

**P1** · 方案 §4.2。2026-09-24 在 work-x 实测过，结果见方案 §18.8。环境 `aax-gw` ↔ `ibmx-gw` 保留，脚本在 work-01 `/root/aax-*.sh`（状态文件 `/root/aax.state`）。需要三个节点：两个网关节点加一个只有虚拟机的节点。对端可以是另一个 CloudLand 网关（主备或双活都行），或 IBM route 模式网关：IBM 一条连接只认一个对端地址，要建两条连接分别对两个网关节点的地址，回程只能按路由优先级主备（方案 §16.4，`/root/ibm-aax.sh`）。

```bash
api POST /vpn_gateways '{"name":"aa-gw","vpc":{"id":"<vpc>"},"ha_mode":"active_active","ipsec_enabled":true,"public_ips":[{"public_subnet":{"id":"<pub>"}}]}'
# 两条隧道，node1 / node2 各一条；不指定主备时主隧道放在承担主隧道较少的节点
api POST /vpn_gateways/<gw>/connections '{"name":"to-peer","route_mode":"static","psk":"...","remote_cidrs":"10.241.0.0/24",
  "tunnels":[{"endpoint":"node1","remote_gateway":"<对端1>"},{"endpoint":"node2","remote_gateway":"<对端2>"}]}'
```

### 检查项

- [ ] 创建：`ha_mode` 为 `active_active`，`public_ips` 两项 `node1` / `node2`，各带节点名；配额扣 2 个公网 IP（开客户端 VPN 时 3 个）
- [ ] 两个节点的公网口上各只有自己的地址，重启节点后地址与 `fip-<vlan>` 表的默认路由还在；没开客户端 VPN 时两个节点都没有 keepalived，`vrrp-<id>/keepalived.conf` 不存在
- [ ] 两个节点都有 charon、FRR（含 bfdd）、看护循环；各自的 `swanctl.conf` 只有自己的隧道，`local_addrs` 是自己的地址
- [ ] iBGP：`vtysh -N vpn-<gw> -c 'show bgp summary'` 有对端节点 VRRP 地址的会话，Established；`show bfd peers` 有它，300 毫秒
- [ ] 静态连接：SA 起来的节点有 `ip route <远端网段> ipsec-<id> tag 100`（主）或 `tag 101 211`（备）；没有主隧道的节点 `show ip route <远端网段>` 走 iBGP 到对端节点；两个节点都有 `Null0 254`
- [ ] 第三个节点：`/opt/cloudland/cache/router/router-<N>/vpn-<gw>/nexthops` 里主隧道所在节点排第一，路由经它的 VRRP 地址
- [ ] 断开主隧道（节点还活着）：连接 `degraded`，主隧道所在节点改经 iBGP 转给另一个节点；clapi 把第三个节点的路由改到另一个节点（日志 `the preferred gateway node of a connection changed`）；测中断时间
- [ ] 主隧道所在节点断电（`echo b > /proc/sysrq-trigger`）或断网（`tc netem`）：另一个节点的 BFD 约 1 秒断 iBGP、走自己的隧道；第三个节点的 F7 约 1 秒切走（见 VPN-16）；测中断时间，对照方案 §15.1（断网实测：另一个网关节点 0.9 秒、第三节点 2.1 秒）
- [ ] 两个节点之间断链、两台都活着：各走自己的隧道，业务不受影响（双活下这种故障无害）
- [ ] 客户端 VPN：没有 vip1 时打开被拒（`Add a public address for the client VPN first`）；`POST .../public_ips` 加 vip1 后能打开，两个节点都跑 keepalived，客户端流量只经持有 vip1 的节点；客户端访问不到远端站点（两个节点的 `FORWARD` 都有 `-s <地址池> -o ipsec+ -j DROP`）
- [ ] 状态：两个节点各报自己的隧道，界面上每条隧道显示所在节点；停用 / 启用网关两个节点都生效
- [ ] 改连接配置（PATCH 描述、计时器等）期间两个节点的 BGP 会话都不重建（`/root/aax-uptime.sh desc`，前后 `show bgp summary` 的 Up/Down 时间只增不减），业务 0 丢包（`/root/aax-cfgchange.sh '<PATCH 内容>'`）
  > **回归点**（2026-09-24）：① 每次下发配置都会重置全部 BGP 会话（R1 起就有）：FRR 把 `neighbor X bfd` 和 `neighbor X bfd profile P` 显示成两行，生成的配置只有后一行，frr-reload 每次先删 BFD 再加回来；② 双活静态连接的备节点走了自己的备隧道，没有经 iBGP 走对端的主隧道：bgpd 本地起源的路由 weight 32768 压过了 local-preference，现在 `TUNNEL-STATIC` 里 `set weight 0`

---

## VPN-15 负载分担与全连接

**P2** · 方案 §5.5、§2.4。2026-09-24 实测了双活网关上 BGP 与静态连接的负载分担（方案 §18.8，`/root/aax-ecmp.sh`）；主备网关的四条隧道全连接、与 IBM `distribute_traffic` 同时打开没有测。

### 检查项

- [ ] `traffic_policy: ecmp` 的主备网关静态连接：主节点上远端网段是一条多路径路由，经所有 up 的隧道；断一条隧道后只剩另一条；`sysctl net.ipv4.fib_multipath_hash_policy` 为 1
- [ ] `ecmp` 的 BGP 连接：各隧道 local-preference 都是 200、不前置；`show bgp ipv4 unicast <网段>` 有多条 multipath；FRR 配置有 `maximum-paths`
- [ ] 双活网关的 `ecmp` 连接：两个节点各走自己的隧道，第三个节点的路由是经两个 VRRP 地址的多路径
- [ ] 汇总状态：都 up 为 `up`，部分 up 为 `degraded`
- [ ] 从 `ecmp`（负载分担）改回 `preferred`（主隧道优先）：重新选出一条主隧道，其余为备隧道；界面上连接的流量分配显示「主隧道优先」
- [ ] 全连接：主备双地址网关、对端两个地址，一条连接 4 条隧道；隧道 1 为主时 local-preference 200 / 100 / 90 / 80，前置次数逐级加一；逐条断开时按排名切换
- [ ] 与 IBM 的 `distribute_traffic` 配合：IBM 按源地址分流（方案 §16.3），两端都分担时单条隧道断开的影响
  > 2026-09-24 测过双活网关对 IBM（方案 §16.4，work-01 `/root/ibm-aax.sh`）：IBM 一条连接只认一个对端地址，要两条连接；两条路由同优先级会被 IBM 拒绝（`route_conflict`），回程只能按优先级主备，`distribute_traffic` 没有效果。阻断隧道或断网时回程中断 31–33 秒，有一次超过 65 秒

---

## VPN-16 其他节点本地快速切换（F7）

**P1** · 方案 §8.2 F7。2026-09-24 实测过（方案 §18.8）：主隧道所在节点断网时，第三节点中断 2.1 秒。

### 检查项

- [ ] 承载 VPC、但不是网关节点的节点上有一个 `vpn_nexthop_watch.sh`（`pgrep -af 'vpn_nexthop_watch[.]sh'`），`/run/cloudland/vpn-nexthop/` 下有各网关节点地址的状态；杀掉后下一次心跳被拉起
- [ ] 网关节点断网：第三个节点约 1 秒内把路由改到另一个网关节点（节点日志 `does not answer, routing around it`），恢复后连续 10 次应答（3 秒）再切回，切回时不再断
  > **回归点**（2026-09-24）：原先连续 3 次应答就切回，节点网络刚恢复、BGP 会话还没起来，恢复时又断了 0.2 秒和 0.1 秒两次
- [ ] 主备网关：切走后流量经备节点转给主节点（此时主节点没死只是 ping 不通时，业务仍通）；VRRP 切换完成后 clapi 下发的新顺序与本地状态一致
- [ ] 所有网关节点都不通时保持偏好顺序的第一个，不删路由
- [ ] VPC 里最后一个网关删掉后，第三个节点的目录与路由清掉，看护进程 10 秒内退出

---

## VPN-17 公网地址增减、隧道告警、按隧道的流量

**P2** · 方案 §9.3、§9.6、§9.7。2026-09-24 实测过（方案 §18.8）。脚本在 work-01：`/root/aax-pubip.sh`、`/root/ibmx-rmvip2.sh`、`/root/aax-client-cycle.sh`、`/root/aax-traffic.sh`。

### 检查项

- [ ] 主备单地址网关 `addable_endpoint` 为 `vip2`；`POST .../public_ips` 后两个地址都在主节点公网口上，keepalived 持有两个；配额多扣 1 个
- [ ] 有隧道用着 vip2 时 `DELETE .../public_ips/vip2` 被拒；没有时删除成功，地址、策略路由、INPUT 规则都清掉，配额释放 1 个
- [ ] 双活网关 `addable_endpoint` 为 `vip1`（没有客户端地址时）；客户端 VPN 开着时删不掉 vip1
- [ ] 带流量删 vip2（`/root/ibmx-rmvip2.sh`）：0 丢包；主节点一直是 MASTER（keepalived 日志只有 `ip address ... no longer exist`，没有 `Entering BACKUP` / `FAULT`）；**两个节点**的 `te-<路由器>-<VLAN>` 都还在
- [ ] 双活网关加 vip1、开客户端 VPN、关闭、删 vip1（`/root/aax-client-cycle.sh`）：虚拟机到对端 0 丢包；开着时两个节点都有 keepalived 和 `wg-<网关>`，关掉并删 vip1 后都没有、`vrrp-<id>` 目录为空，公网口与节点地址都在；配额 +1 后 -1
  > **回归点**（2026-09-24）：删 vip2 曾把两个节点共用的公网口一起删掉，网关当场失去主节点，keepalived 一直停在 FAULT。原因是 keepalived 重载前先删了地址（keepalived 看到自己的地址被外部删除就退出 MASTER），`clear_lb_floating.sh` 又按「口上已没有地址」删口（备节点的口本来就不挂地址）
- [ ] 审计与组织动态有 `vpn_gateway.public_ip_add` / `public_ip_remove`
- [ ] 两条隧道的连接断一条：告警事件里出现 `VpnTunnelDown`（warning），隧道恢复后解除；两条都断：`VpnConnectionDown`（critical）；停用网关时两类都解除
- [ ] `GET .../traffic?by=tunnel`：每条隧道一条曲线，带 `connection_id` 与 `slot`；双活网关两个节点的计数加起来等于连接总量（`/root/aax-traffic.sh` 逐点对比，单位 bit/s，120 秒窗口，所以刚开始发流量时数值逐渐爬升）

---

## VPN-18 单节点网关与自动补备节点

**P1** · 方案 §4.1、§9.3、§18.15。**2026-09-27 部署后实测过**（方案 §18.18，脚本 work-01 `/root/zone-test-20260927.sh`：双活 132009、主备 5 秒单节点可用、移入第二个节点 292 秒补上备节点、`kill -9` 后 2 秒接管；没测补备节点时的真实流量与 HA 双 clapi）。要一个只有一个可用节点的 zone：临时建 zone、把一个节点移过去，测完移回。移动只影响这段时间里「新资源选节点」，已有的虚拟机、负载均衡、网关都不动；选一个不是保留网关 VRRP 节点的节点更稳妥，测完一定要移回。

```bash
api POST /zones '{"name":"ztest"}'                                   # 记下 zone uuid
api PATCH /hypers/<work-03 uuid> '{"zone_id":"<ztest uuid>"}'        # 测完改回 zone0 的 uuid
api POST /vpn_gateways '{"name":"sn-gw","vpc":{"id":"<vpc>"},"zone":"ztest","ipsec_enabled":true}'
api POST /vpn_gateways '{"name":"sn-aa","vpc":{"id":"<另一个 vpc>"},"zone":"ztest","ha_mode":"active_active","ipsec_enabled":true}'
```

### 检查项

- [ ] 空 zone（还没移节点）里建网关 → 400，`error_code` 132009，界面在底栏显示「所选可用区的可用计算节点不够…」，没有分配公网 IP、没有 VRRP 实例
- [ ] 单节点 zone 里建双活网关 → 400（132009）
- [ ] 单节点 zone 里建主备网关：约 1 分钟内 `available`（不再停在 `pending`）；`nodes` 只有一个（MASTER）；clapi 日志有 `runs on a single node, without high availability`；节点上 keepalived 约 3 秒后为 MASTER、charon / FRR 起来、公网地址在 `te-` 口上
- [ ] 界面：详情页标题有「单节点」标签、黄色提示条；列表节点列有「单节点」；创建中（`pending`）不显示
- [ ] 连接能建、隧道能起；`kill -9` 该节点 keepalived：看护循环摘地址，守护脚本下次心跳拉起，约 3 秒后恢复（记中断时间）
- [ ] **自动补备节点**：把 zone0 的另一个节点也移进 ztest，20 秒到 5 分钟内 clapi 日志 `placing its BACKUP node`，BACKUP 网卡有了节点，`nodes` 两个，「单节点」标签消失；**补的过程中业务 0 丢包**（原节点 keepalived 保持 MASTER、只重载同样的配置）；之后 `kill -9` 主节点 keepalived，备节点接管
- [ ] 补上的备节点如果之前是这个 VPC 的普通节点：它的 `vpn-<网关>/` 目录里不再有 `nexthops`、`nexthop_hosts`、`routes.current`，F7 探测循环不再改它的路由；之后 `kill -9` 主节点 keepalived，这个节点升主后路由指向自己的隧道、没有被改回经对端（回归点 45）
- [ ] HA 两台 clapi 时（没有就跳过）：补备节点只下发一次，`interfaces` 里 BACKUP 网卡只落到一个节点
- [ ] `error=resource`：ztest 里第二个节点在库里是可用、但 cloudlet 停着时建主备网关 → 不卡 `pending`，按单节点运行，clapi 日志 `found no connected node`；恢复 cloudlet 后自动补备节点
- [ ] 失败原因：让一个节点建网关失败（例如临时改坏 `create_vpn_gateway.sh` 让它退出非零）→ 网关 `error`，`status_reason` 为「Node … failed to build the gateway」，详情页红色提示条、列表状态悬停显示；修好后任一 PATCH 救回，`status_reason` 清空
- [ ] 收尾：两个节点移回 zone0、删 ztest 与测试网关，`GET /hypers` 核对 zone 已恢复
  > **回归点**（2026-09-27）：zone 里只有一个可用节点时网关永远停在 `pending`、PATCH 也救不回来（`SetVrrpIp` 选不到 BACKUP 只更新负载均衡表；救回路径写死两个节点）；`error=resource` 回调被当成节点回调解析，第 3 个参数（VNI）当节点 ID，报错后 cland 重试 3 次放弃

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
| 26 | 站点连接的流量统计永远是 0，且随重协商清零（2026-09-24，与 IBM Cloud 互通时发现） | 正则没考虑 XFRM 接口 SA 在 SPI 后的 `(-\|0x...)`；SA 计数本身每次重协商清零。现读隧道接口计数减接管基线 | VPN-02 流量 |
| 27 | 对端的 SYN-ACK 不做 MSS 钳制（2026-09-24，与 IBM Cloud 互通时发现） | 规则用 `--syn` 只匹配纯 SYN；进隧道方向又按通往虚拟机的 1450 算。现 `--tcp-flags SYN,RST SYN` + 进隧道 `--set-mss` | VPN-02 MSS |
| 28 | 任意成员能用流量接口打挂 clapi（2026-09-24，代码复查） | 只检查开始小于结束，极端值相减溢出、绕过点数上限，时间戳循环耗尽内存 | VPN-02 流量历史的 400 用例 |
| 29 | 流量带上上一任期的量（2026-09-24，代码复查） | 基线只由切换脚本记，守护脚本抢先拉起 charon 时漏记 | VPN-02 基线 |
| 30 | 连接状态回调每次心跳都发（2026-09-24，代码复查） | 按整份负载比较，字节数每次都变 | VPN-02 状态回调频率 |
| 31 | 流量曲线出现几 Gbps 的假尖峰（2026-09-24，代码复查） | 读计数失败时导出 0，`rate()` 当成计数重置 | VPN-02 流量历史 |
| 32 | 快速切换时段后图表是另一个时段的数据、「当前速率」是几小时前的值（2026-09-24，代码复查） | 请求没有代次；当前速率取范围内最后一个非空点 | VPN-07 监控标签 |
| 33 | keepalived 父进程被杀时旧主不让位，其他节点的虚拟机中断 17.7 秒（2026-09-24，IBM 实测） | 子进程摘地址不调切换脚本，看护循环直接退出 | VPN-13 keepalived |
| 34 | 改 BGP 计时器不生效（2026-09-24，IBM 实测） | 已建立的会话沿用旧的协商值 | VPN-12 |
| 35 | 迁移给已软删除的连接也建了隧道（2026-09-24） | 迁移语句没有带上 `deleted_at` | 部署后查 `vpn_tunnels` 里 `deleted_at is null` 的行都对应存活的连接 |
| 36 | 每次下发配置都重置全部 BGP 会话（R1 起就有，2026-09-24 R2 实测发现） | 生成的配置缺 `neighbor X bfd` 那一行，frr-reload 每次先删 BFD 再加回来 | VPN-14 改配置 |
| 37 | 双活静态连接的备节点走自己的备隧道（2026-09-24） | bgpd 本地起源的路由 weight 32768 压过 local-preference | VPN-14 静态连接 |
| 38 | F7 切回太早，节点恢复时又断两次（2026-09-24） | 连续 3 次应答就切回，那时 BGP 会话还没起来 | VPN-16 |
| 39 | 删公网地址后网关失去主节点，keepalived 一直停在 FAULT（2026-09-24） | 先删地址再重载 keepalived；`clear_lb_floating.sh` 按「口上没有地址」删共用的公网口（负载均衡同样受影响，见 `TC-06` LB-08） | VPN-17 删 vip2 |
| 40 | 两个节点都不持有地址时仍显示原主节点与全部 up（2026-09-24） | 只有持有地址的节点上报，没有人发现上报停了 | VPN-13 无主检测 |
| 41 | 改成静态路由后隧道仍留着 BGP 状态（2026-09-24） | BGP 状态上报与改路由方式并发，把刚清掉的值写了回去 | VPN-12 |
| 42 | 观察者能从客户端配置模板拿到 WireGuard 预共享密钥（2026-09-25 发现） | `Config` 不看写权限，与其他凭据的规则不一致 | VPN-09 凭据回读 |
| 43 | `client_routes` 填 `0.0.0.0/0` 没被拒（2026-09-26 发现） | 文档与本用例都写着接口拒绝，代码只校验了格式；拒绝 `0.0.0.0/0` 的 `validateVpnPrefixes` 不管客户端路由 | VPN-05 |
| 44 | 单节点 zone 里的网关永远 `pending`，`error=resource` 回调被误读（2026-09-27 修） | `SetVrrpIp` 选不到 BACKUP 只管负载均衡；救回路径写死两个节点；`error=resource` 带的是下发命令的参数 | VPN-18 |
| 45 | 普通节点被补成备节点后，F7 继续改写它的路由（2026-09-27 代码复查发现，未上线即修） | 探测循环按 `nexthop_hosts` 文件遍历、不看本节点角色；`set_vpn_route.sh` 的网关节点分支没清掉普通节点时的状态 | VPN-18 |
| 46 | 双活两条隧道同时断，连接仍显示「降级」、没有 `VpnConnectionDown`（2026-09-27 代码复查） | 两个节点的上报并发处理，各自用对方隧道的旧状态算汇总 | VPN-14：对端设备一次断掉两条隧道，连接应直接变「未连接」并发严重告警 |
| 47 | 创建中的网关加减公网地址，节点上不生效（2026-09-27 代码复查） | 下发只在可用 / 错误时做，之后没人补推 | VPN-17：创建中 `POST` / `DELETE .../public_ips` → 400（132006），界面按钮禁用 |
| 48 | clapi 积压时所有节点被 cland 判失联（2026-09-27 代码复查） | 读流协程阻塞在回调队列上，存活消息读不出来 | 回调队列过半时 cland 日志不应出现成批的 `liveness: gone` |
| 49 | 删网关后 `vpn_tunnels` 留着孤儿记录（2026-09-27 代码复查） | 删网关只软删连接 | VPN-10：删网关后 `select count(*) from vpn_tunnels where vpn_gateway_id = <id> and deleted_at is null` 为 0 |
| 50 | 同一地址两条只响应隧道被拒（2026-09-27 代码复查） | 地址唯一性按「本端地址 + 对端地址」，对端都空 | VPN-09：两条都不填 `remote_gateway`、`remote_id` 不同 → 200；相同 → 400 |
| 51 | 删隧道 / 连接 / 网关后告警永不解除（2026-09-27 代码复查） | 解除只靠「恢复」上报 | VPN-08：备隧道断着时删掉它，`VpnTunnelDown` 变 resolved |
| 52 | 计算节点能伪造别的节点失联（2026-09-27 代码复查） | `node_liveness` 回调不核对来源 | 在一个节点上让脚本输出 `\|:-COMMAND-:\| node_liveness.sh '<别的节点>' 'gone'`，clapi 日志 `Ignoring node_liveness`，没有接管、隧道状态不变 |
| 53 | 看护循环起两份（2026-09-27 代码复查） | 查 pid 与写 pid 不是原子的 | 连续两次 `vpn_watch_start` 后 `pgrep -fc 'vpn_watch.sh <gw>'` 为 1 |
