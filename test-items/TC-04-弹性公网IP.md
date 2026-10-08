# TC-04 弹性公网 IP（FIP）

优先级 **P0** · 会在计算节点上真实下发 iptables 规则 · 约 30 min

## ⚠️ 公网地址只剩 1 个（2026-09-28 基线）

`public-756` 只剩 **`52.117.101.158`** 一个空闲地址。凡是要占公网地址的用例——本文件 FIP-01～03 / FIP-07 / FIP-08、`TC-02` 公网子网上的虚拟机、`TC-05 SG-05`、`TC-06` 负载均衡浮动 IP、`TC-17` VPN 网关、`TC-18` 公网子网上的成员——**同一时刻只能有一个在用**：

1. 开始前查一次 `.158` 是 `allocated=false`（命令见 FIP-01 前置），不是就先找到占用者；
2. 占用期间在执行记录里写明「`.158` 由 TC-xx 占用」；
3. 用完**立即**释放，再查一次 `allocated=false`，才交给下一个用例。

本文件按 FIP-01 → FIP-02 → FIP-07 →（`TC-05 SG-05`）→ FIP-03 的顺序共用同一个浮动 IP，中间不释放；FIP-04、FIP-05 不占地址，可以穿插；FIP-06 也不占地址，但要在 `.158` 空闲时跑（FIP-01 之前或 FIP-03 之后，否则「指定已分配地址」那条会先因为没有空闲地址返回 409）。FIP-08 要另占一次，放最后。

## 设计约定

- 公网子网 `public-756`：VLAN 756，`52.117.101.144/28`，网关 `.145`。当前占用：`.146` / `.147` / `.148` 三个节点的 system router，`.149` `net-c` 自带的 native IP，`.150` `mig-fip`（挂 `net-a`），`.151` `rb-fip`（`rb-lb` 的负载均衡浮动 IP），`.152` VPN 网关 `a1`，`.153` `ibmx-gw`，`.154` / `.155` `ibmr-gw`，`.156` / `.157` `aax-gw`。网关 `.145` 在地址表里是 `allocated=false`，但指定它分配一律 400（`36098cf7`，见 `TC-03 NET-09`）。
- 类型（`type` 字段）：`floating`（本页申请的）、`native`（公网子网上的虚拟机自带，**随虚拟机释放，不能单独删**）、`loadbalancer`（`/load_balancers/{id}/floating_ips` 建的，见 `TC-06`）、`vpngateway`（建 VPN 网关时分配，见 `TC-17`）、`site`、`reserved`。接口 `PATCH` 只接受 `floating`；`DELETE` 只接受 `floating` 和 `loadbalancer`，其他 400。
- 申请：`POST /floating_ips` 返回**数组**；库里的名称是 `<name>-<序号>-<纳秒>`（如 `wp-fip-0-1790…`）；`public_ip` 带前缀（`52.117.101.158/28`）。`activation_count`（0 按 1，上限 64）大于空闲地址数时 409；`public_ip` 与 `activation_count>1` 或站点子网不能同时给（400）。
- 挂载条件：虚拟机在 VPC 里（否则 400 `Instance has no router`）、不是 `provisioning`、有主网卡。
- 生效方式：在虚拟机**所在节点**执行 `create_floating.sh`——路由器 netns 里建 `te-<router>-756`（外网口）并配上浮动 IP，策略路由 `from/to <内网 IP> lookup fip-756`（表 `fip-756` 的默认路由指向 `.145`），nat 表 DNAT（公网→内网）+ SNAT（内网→公网，`nonat` 集合除外），设了带宽时加 tc。
- 路由表名 `fip-<vlan>` 登记在 `/etc/iproute2/rt_tables.d/cloudland.conf`（26.04 的 iproute2 已无 `/etc/iproute2/rt_tables`），`fip-756` 的编号是 8。
- 卸载：`clear_floating.sh` 删规则与地址（含带宽限速的 MARK 规则与 tc）；`te-<router>-756` 上**没有别的地址、且路由器里已没有 `lookup fip-756` 的策略规则**时才把这个口删掉（与 `clear_lb_floating.sh` 同一判断）。
  > **2026-09-30 已修（`scripts/kvm/clear_floating.sh`），2026-10-01 已部署，部署后按 FIP-03 的新检查项判定**。原先只看「口上没有地址」：在 LB / VPN 的**备节点**上 keepalived 平时不持有地址，卸载一个普通浮动 IP 会把它们共用的 `te-` 口一起删掉，切主时浮动 IP 上不来；另外原脚本把内网地址 `$3` 当成 mark 算，MARK 规则与入方向 tc **从没删掉过**，现在用浮动 IP ID（`$5`）。
  >
  > 部署前（及部署后复测这一条之前）照旧：**测试虚拟机一律放在 `TC-03` 的 `wp-vpc` 里**，不要用 `rb-vpc` / `ibmx` / `ibmr` / `aax` 里的虚拟机做挂载试验。
- **带宽限速**（`inbound` / `outbound`，Mbit/s，1–20000；2026-09-30 按 `scripts/kvm/fip_lib.sh` 重做，2026-10-01 已部署）：
  - **编号按公网地址，不按浮动 IP id**：`n` = 公网地址的低 16 位（`c × 256 + d`），class `1:<n 的十六进制>`、filter 优先级 `n`（十进制）。`52.117.101.158` → `n = 101×256+158 = 26014`，class **`1:659e`**、`pref 26014`。mark 仍是浮动 IP id（mangle `MARK --set-mark <id>`，fw filter 显示为 `handle 0x<id 的十六进制>`）
  - **设备**（每个地址在每块设备上一个 class + 一个 filter）：实例浮动 IP 的**入方向**在路由器 netns 的 `ns-<内网 vlan>`（fw，按 mark）；**所有出方向**（实例、负载均衡、VPN 网关）在路由器 netns 的 `te-<router>-<vlan>`（u32 按源地址，`match <地址十六进制>/ffffffff at 12`）；负载均衡 / VPN 网关的**入方向**在**宿主机**侧的 `ext-<router>-<vlan>`（`te-` 的 veth 对端，u32 按目的地址 `at 16`）
  - **不限速的地址**：低 16 位是 `0.0`（`n=0` 是 qdisc 本身）或 `0.16`（`n=0x10` 是 htb 的 default class）的地址设不了限速，节点记日志 `bandwidth limit not set on <dev>: no tc number for this address`（stderr 与调试日志，不影响挂载）；同一块设备上两个地址低 16 位相同时，后来的被拒绝、**不覆盖**先来的（`fip_tc_claim`），卸载只删自己的
  - 改带宽走 `set_floating_bandwidth.sh`（只换 tc，不动 DNAT / 策略路由，见 FIP-07）；升级前按旧编号（class `1:<id>`、prio `<id>`）设的限速，在这个浮动 IP 下次改带宽或卸载时被清掉
  > **2026-09-30 已修（新增 `scripts/kvm/fip_lib.sh`；`create_floating.sh`、`clear_floating.sh`、`set_floating_bandwidth.sh`、`create_lb_floating.sh`、`clear_lb_floating.sh`），2026-10-01 已部署，部署后按 FIP-02 / FIP-03 / FIP-07 与 `TC-06` LB-04 的限速检查项判定**。
  >
  > 原记录（2026-09-30 审查时发现，既有问题）：出方向的 classid / prio 算成 `mark_id + 2147483647`，超出 tc 的 16 位范围，**实例与负载均衡的出方向限速从没生效过**；负载均衡 / VPN 网关的入方向 filter 挂在 `te-` 上按目的地址匹配，而发往 VIP 的流量是从 `te-` **进**路由器的（tc 只管出设备的流量），**同样从没生效过**。只有实例浮动 IP 的入方向（`ns-` 上按 mark）一直有效
- 配额（cpgateway）：弹性 IP（含负载均衡的 `/load_balancers/{id}/floating_ips`）计入 `public_ips`。**一次创建按上限 `activation_count`（0 按 1）+ 站点子网数预扣**，再按返回数组长度退还未创建的部分；失败整笔退回。删除前先 GET 该浮动 IP，`owner_uuid` 不是调用方组织（系统管理员删别的组织的）时不释放，留给那个组织下次登录对账。登录对账按 `GET /floating_ips` 的 `total` 覆盖用量（所有类型都算，Admin 组织基线 9）。
- 组织动态：申请 `floating_ip.create`，挂载 / 卸载都记 `floating_ip.update`，释放 `floating_ip.delete`。

---

## FIP-01 申请（不挂实例）

**P0** · 脚本 `fip3.js` 的申请部分（写入类：只放开 `POST` / `PATCH /api/v1/floating_ips*`）· **占用 `.158`**

> ⚠️ **不要直接跑 `fip.js`**（2026-09-28 核对脚本）：它把弹窗里**每个下拉都选第一项**，包括「绑定实例」，会把新浮动 IP 直接挂到列表里第一台虚拟机上（可能是保留的 `rb-be*`、`net-a`、`ix*`……）。用 `fip3.js`（申请时实例保持「不挂载」），或者用接口。

### 前置

```bash
ssh work-01 'source /root/alarmtest-env.sh
PS=$(api GET "/subnets?limit=100" | jq -r ".subnets[]|select(.name==\"public-756\")|.id")
api GET /addresses/$PS | jq -c "[.addresses[]|select(.allocated|not)|.address]"
docker exec cloudland-postgres psql -U postgres -d cloudland_cpgateway -Atc "select public_ips from org_resource_consumptions c join organizations o on o.id=c.org_id where o.uuid='"'"'305fcaf9-c76a-4ed5-a1ae-eddb8d8fc64e'"'"'"'
```

期望空闲只有 `.158` 和网关 `.145`，`public_ips` 用量 9。

### 步骤

界面：弹性公网 IP 页 → 新建 → 名称 `wp-fip`、公网子网选 `public-756` →「公网 IP」下拉看一眼（只有「自动分配」和 `.158`）→ 提交。

```bash
ssh work-01 'cd /opt/cloudland/deploy/docker && PW=$(grep "^ADMIN_PASSWORD=" .env | cut -d= -f2-)
docker run --rm --network host -v /root/console-test:/work -w /work -e ADMIN_PASSWORD="$PW" \
  mcr.microsoft.com/playwright:v1.55.0-noble node fip3.js 2>&1 | tail -8'
```

> `fip3.js` 申请完接着做 FIP-02 的挂载，跑之前先按 FIP-02 的提示改好挂载目标。界面首选本机 5173 验证；弹性 IP 页自 `facff649` 起没改动，work-01 的 nginx 界面与本地一致，也可以在 work-01 容器里跑原脚本。用接口代替时：`api POST /floating_ips` 带 `{"name":"wp-fip","public_subnets":[{"id":"<public-756>"}]}`（写文件再 `-d @file`）。

### 检查项

- [ ] 表格行数 +1，`toast` 无错误，无 4xx/5xx
- [ ] 分配到的是 **`52.117.101.158`**，类型「floating」，名称是「填的名字 + `-0-<纳秒>`」（`fip3.js` 填 `wp-fip-<5 位数>`）
- [ ] 「公网 IP」下拉**不含子网网关 `.145`**（`assignableAddresses` 排除；`gw-exclude.js` 覆盖）；接口带 `public_ip: "52.117.101.145"` 申请 → 400，`.145` 仍是 `allocated=false`（`TC-03 NET-09` 已含这条，跑过就不用重复）
- [ ] **未挂载的行上有「挂载」按钮**（挂载后应变成「卸载」）
- [ ] `pageerrors: none`

### 涉及接口

| 方法 | 路径 | 要点 |
|---|---|---|
| POST | `/api/v1/floating_ips` | `{"name"}`（2–32）+ 可选 `public_subnet` / `public_subnets` / `site_subnets` / `instance` / `inbound` / `outbound`（1–20000）/ `public_ip` / `activation_count`（0–64）；返回数组 |
| PATCH | `/api/v1/floating_ips/:id` | `inbound` / `outbound` 0–20000（0 = 不限速，2026-09-30 起）；带 `instance:{id}` 是挂载；**`instance:null` 才是卸载**；请求体里没有 `instance` / `load_balancer` 键（`{}`、只有 `inbound` / `outbound`）时**保持挂载不变**（2026-09-30 改，2026-10-01 已部署；之前 `{}` 等于卸载），见 FIP-07 |
| DELETE | `/api/v1/floating_ips/:id` | 已挂载的先卸载再释放；浮动 IP 所属组织、或它挂载的实例 / 负载均衡所属组织的写成员可以释放 |
| GET | `/api/v1/floating_ips` | `offset`/`limit`/`query`（匹配公网地址、内网地址、名称）/`order`（`name`、`fip_address`、`type`…） |
| POST / DELETE | `/api/v1/load_balancers/:id/floating_ips[/:fid]` | 负载均衡的浮动 IP，见 `TC-06` |

### 配额口径

- [ ] 申请 1 个后 `public_ips` 用量 9 → **10**（前置里那条 SQL）

---

## FIP-02 挂载到虚拟机 + 节点侧核对

**P0** · 脚本 `fip3.js`（写入类：只放开 `PATCH /api/v1/floating_ips/*`）

### ⚠️ 选择挂载目标时务必小心

脚本里「选下拉第一项」的写法曾把浮动 IP 挂到了保留的 LB 后端 `rb-be3` 上。**必须按名称选中 `wp-vm`**（`TC-03 NET-02` 在 `wp-vpc` 里建的那台），不要按下标选，也不要选其他 VPC 的虚拟机（原因见「设计约定」卸载一条）。

> `fip3.js` 现在写死 `TARGET_VM = 'net-c'`，而 `net-c` 在公网子网上、不在挂载下拉里，于是**退回第一项**——正是当年挂到 `rb-be3` 的写法（2026-09-28 核对）。跑之前把 `TARGET_VM` 改成 `wp-vm`，并把 `|| opts[0]` 改成找不到就报错退出。

### 步骤

界面上点 `wp-fip` 行的「挂载」→ 下拉选中 `wp-vm` → 确认。

节点侧核对（在虚拟机**所在节点**上）：

```bash
FIP=52.117.101.158      # public_ip without the prefix
VMIP=<wp-vm 的内网 IP>
RT=<wp-vpc 的 router 编号>   # TC-03 NET-02 queries routers.id

ssh work-0X "ip netns exec router-$RT iptables -t nat -S | grep -E '$FIP|$VMIP/32'
ip netns exec router-$RT ip -4 -br addr show te-$RT-756
ip netns exec router-$RT ip rule | grep -w $VMIP
ip netns exec router-$RT ip route show table fip-756
grep -w fip-756 /etc/iproute2/rt_tables.d/cloudland.conf"
# from outside (work-01 host or your own machine): wp-vm uses the native group, 22 and ICMP are open
ssh work-01 "ping -c3 -W2 $FIP; timeout 5 bash -c '</dev/tcp/$FIP/22' && echo 22-open"
```

### 检查项

- [ ] 行上的按钮从「挂载」变成「卸载」，「挂载到」列显示 `wp-vm`；接口返回的 `target_interface.ip_address` 是 `wp-vm` 的内网 IP
- [ ] netns 里有 DNAT：`-A PREROUTING -d 52.117.101.158/32 -j DNAT --to-destination <VMIP>`
- [ ] netns 里有 SNAT：`-A POSTROUTING -s <VMIP>/32 -m set ! --match-set nonat dst -j SNAT --to-source 52.117.101.158`
- [ ] 浮动 IP 配在 `te-<router>-756` 上（`52.117.101.158/28`）
- [ ] 策略路由 `from <VMIP> lookup fip-756`、`to <VMIP> lookup fip-756` 各一条；表 `fip-756` 的默认路由是 `via 52.117.101.145`
- [ ] `rt_tables.d/cloudland.conf` 里有 `8 fip-756`
- [ ] 从外部 ping 通、22 端口可连
- [ ] 其他节点上**没有**这个浮动 IP 的规则（只下发到虚拟机所在节点）

---

## FIP-03 卸载与释放，节点规则清零

**P0** · **释放 `.158`**（在 FIP-07 与 `TC-05 SG-05` 之后做）

### 步骤

界面点「卸载」→ 再点「释放」（删除图标）。

```bash
ssh work-01 'source /root/alarmtest-env.sh; api GET "/floating_ips?limit=100" | jq -r ".floating_ips[].name" | grep wp- || echo "gone"
PS=$(api GET "/subnets?limit=100" | jq -r ".subnets[]|select(.name==\"public-756\")|.id")
api GET /addresses/$PS | jq -c ".addresses[]|select(.address==\"52.117.101.158/28\")|{address,allocated}"'
ssh work-0X "ip netns exec router-$RT iptables -t nat -S | grep -c $FIP; ip netns exec router-$RT ip rule | grep -c -w $VMIP; ip netns exec router-$RT ip link show te-$RT-756"
```

### 检查项

- [ ] 列表里不再有该浮动 IP
- [ ] netns 里 DNAT / SNAT 规则数为 **0**，策略路由为 **0**
- [ ] `te-<router>-756` 已删除（`wp-vpc` 的路由器上没有别的浮动 IP、也没有 `lookup fip-756` 规则；报 `does not exist` 即通过）
- [ ] 带宽相关的残留为 0（FIP-07 设过限速时才有意义）：`ip netns exec router-$RT iptables -t mangle -S PREROUTING | grep -c $FIP` 为 0；`ns-<wp-subnet 的 vlan>` 上没有 `1:659e` 的 class、没有 `pref 26014` 的 filter；`te-<router>-756` 已随口删除（口还在时同样没有 `1:659e`）
  ```bash
  ssh work-0X "ip netns exec router-$RT tc class show dev ns-<内网 vlan> | grep -c '1:659e '; ip netns exec router-$RT tc filter show dev ns-<内网 vlan> | grep -c 'pref 26014 '"
  ```
  > **2026-09-30 已修（`scripts/kvm/clear_floating.sh` 经 `fip_lib.sh` 按公网地址编号删 tc、按浮动 IP id 删 MARK），2026-10-01 已部署，按本条判定**。原先 mark 用内网地址 `$3` 算（算术错误，没有 mark），MARK 规则与 tc 从不删除
- [ ] **共用外网口不被误删**（部署后做；需要一个在 LB / VPN **备节点**上、同一路由器的场景，公网 IP 不够时记 SKIP）：同一路由器上还有负载均衡 / VPN 网关的 `lookup fip-756` 规则时，卸载普通浮动 IP 后 `te-<router>-756` 仍在
  > **2026-09-30 已修（`scripts/kvm/clear_floating.sh`），2026-10-01 已部署，按本条判定**。复现需要第二个公网 IP（2026-09-28 那轮因此 SKIP）
- [ ] `.158` 回到 `allocated=false`；`public_ips` 用量回到 **9**
- [ ] 组织动态依次有 `floating_ip.create` / `floating_ip.update`（挂载、卸载各一条）/ `floating_ip.delete`
- [ ] **`rb-lb` 仍然 10/10**（浮动 IP 清理曾影响同节点的 LB）

---

## FIP-04 界面显示

**P1** · 脚本 `verify.js`（只读，按 pathname 拦截写请求）

### 检查项

- [ ] 申请弹窗与挂载弹窗的**实例下拉显示的是内网 IP，不是 UUID**
  ```
  期望：wp-vm (192.168.77.2)
  错误：wp-vm (b4d7ff29-184b-4463-bcb3-e6d964bfbab1)
  ```
  > **回归点**：实例响应上**没有** `ip_address` 字段。页面曾写 `inst.ip_address || inst.id`，回退到 UUID。现在从主网卡取（`utils/instance.ts` 的 `primaryIp`）。这类错误由 `vue-tsc` 检出（见 `TC-01 BUILD-01`）。
- [ ] 下拉只列出可挂载的实例（有 VPC、非 `provisioning`、有主网卡）
- [ ] 实例下拉的 `GET /instances`、公网 / 站点子网下拉的 `GET /subnets` 都带 `limit=500`
  > **2026-09-30 已修（`web/src/views/dashboard/FloatingIPList.vue` 用 `OPTION_LIST_LIMIT`），2026-10-01 已部署，按本条判定**。原记录：不带 `limit`，超过 50 条会缺项（与 FAIL-8 同类）
- [ ] 列表第一列的表头是「名称 / ID」
  > **2026-09-30 已修（`FloatingIPList.vue`），2026-10-01 已部署，按本条判定**。原先误写成「用户名」（D16）
- [ ] 带宽（入 / 出）输入范围 1–20000 Mbps，超出时接口 400
- [ ] 各类型行上的按钮（2026-09-28 核对 `FloatingIPList.vue`）：

  | 类型 | 挂载 / 卸载 | 释放 |
  |---|---|---|
  | `floating` | 可用 | 可用 |
  | `site` | 可用 | 禁用 |
  | `native`（`net-c`） | 禁用 | 禁用 |
  | `loadbalancer`（`rb-fip`） | 禁用 | **可点——不要点**，会把 `rb-lb` 的浮动 IP 删掉 |
  | `vpngateway`（`a1`、`ibmx-gw`、`ibmr-gw`、`aax-gw`） | 禁用 | 禁用 |

- [ ] 列表服务端分页、搜索（按名称 / 公网地址 / 内网地址）、按名称 / 地址 / 类型排序；「挂载到」列不可排序

---

## FIP-05 配额超限

**P1** · 不占地址（网关直接拒绝，不转发到 clapi）

```bash
ssh work-01 'source /root/alarmtest-env.sh
Q=/resources/quota/305fcaf9-c76a-4ed5-a1ae-eddb8d8fc64e/49fce7f0-0ae3-49e1-8ffb-02c816b26bb1
U=$(docker exec cloudland-postgres psql -U postgres -d cloudland_cpgateway -Atc "select public_ips from org_resource_consumptions c join organizations o on o.id=c.org_id where o.uuid='"'"'305fcaf9-c76a-4ed5-a1ae-eddb8d8fc64e'"'"'")
echo "usage=$U"; api PUT $Q "{\"max_public_ips\":$U}" >/dev/null
cat > /tmp/f.json <<JEOF
{"name":"wp-fipq"}
JEOF
curl -sk -o /tmp/o.json -w "%{http_code} " -X POST "$B/floating_ips" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -d @/tmp/f.json; jq -c .detail /tmp/o.json
api PUT $Q "{\"max_public_ips\":200}" | jq -c "{max_public_ips}"'
```

把 `max_public_ips` 临时调到当前用量（基线 9，`.158` 被占着时是 10；`PUT` 只改传了的字段），再申请一个。

> 不想动 Admin 的配额（多人同时回归时）：管理员 `POST /orgs` 建一个临时组织（管理员自动成为它的组织管理员），`POST /auth/switch-org {"org_uuid","region"}` 拿到该组织的 token，把它的 `max_public_ips` 设为 0 再申请，同样是 429，网关直接拒绝、不转发 clapi；测完 `DELETE /orgs/<uuid>`（2026-09-28 按此执行）。

- [ ] 返回 **429**，`detail.error` 为 `quota_exceeded`、`detail.resource` 为 `public_ips`（2026-09-28 核对：错误在 `detail` 对象里，不是 `error_code`）
- [ ] 界面显示的是**本地化的超额原因**（走 `utils/quotaError.ts`），不是原始英文报错（界面验证时同样先调低配额，只放开 `POST /api/v1/floating_ips`）
- [ ] `.158` 没被占用
- [ ] 测完把配额改回 **200**

---

## FIP-06 申请失败的几条路径

**P1** · 新增（2026-09-28）· 不占地址（都在分配之前失败），`.158` 必须空闲

```bash
ssh work-01 'source /root/alarmtest-env.sh
cgw(){ docker exec cloudland-postgres psql -U postgres -d cloudland_cpgateway -Atc "select public_ips from org_resource_consumptions c join organizations o on o.id=c.org_id where o.uuid='"'"'305fcaf9-c76a-4ed5-a1ae-eddb8d8fc64e'"'"'"; }
try(){ cat > /tmp/f.json; echo -n "$1: "; curl -sk -o /tmp/o.json -w "%{http_code} " -X POST "$B/floating_ips" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -d @/tmp/f.json; jq -c "{error_code,error_message}" /tmp/o.json; }
echo "usage before: $(cgw)"
try "two addresses, one left" <<JEOF
{"name":"wp-fip2","activation_count":2}
JEOF
try "public_ip with count 2" <<JEOF
{"name":"wp-fip3","public_ip":"52.117.101.158","activation_count":2}
JEOF
try "address already taken" <<JEOF
{"name":"wp-fip4","public_ip":"52.117.101.151"}
JEOF
try "one-char name" <<JEOF
{"name":"w"}
JEOF
try "bandwidth 30000" <<JEOF
{"name":"wp-fip5","inbound":30000}
JEOF
echo "usage after: $(cgw)"
api GET "/floating_ips?limit=100" | jq -r ".floating_ips[].name" | grep wp- || echo "no leftovers"'
```

- [ ] `activation_count: 2` → **409**，error_code **131004**（`Not enough idle addresses for public subnets`）
- [ ] 同时给 `public_ip` 与 `activation_count: 2` → **400**（`Public ip and subnets cannot be specified at the same time`）
- [ ] 指定已分配的 `.151` → **400**，error_code 131202；`rb-lb` 不受影响（分配只查 `allocated=false` 的行）
- [ ] 名称 1 个字符、带宽 30000 → **400**
- [ ] 每条失败后 cpgateway 的 `public_ips` 用量都没变（预扣整笔退回）；没有 `wp-` 残留

---

## FIP-07 PATCH 的语义与权限

**P1** · 新增（2026-09-28）· 用 FIP-02 挂在 `wp-vm` 上的 `.158`

> **2026-09-30 已修，2026-10-01 已部署，部署后按本节判定**（`api/src/apis/floatingip.go` 按请求体里有没有 `instance` / `load_balancer` 键决定是否改挂载；`api/src/services/floatingip.go` 的 `checkWritable` / `checkDetachable` / `bandwidthChangeAllowed`，挂载目标的检查挪到卸载之前；新增节点脚本 `scripts/kvm/set_floating_bandwidth.sh`）。
>
> 原记录（D5 / FIP-07，2026-09-28）：`FloatingIpAdmin.Update` 无条件先卸载、没带 `instance` 就不再挂回，只改带宽的请求会把浮动 IP 卸掉，`inbound` / `outbound` 从不写进数据库；`Detach` / `Delete` 没有角色检查，只读成员能卸载、能释放；只读成员挂载被 403 之前，卸载已经在节点上执行。
>
> ⚠️ **行为变化**：`PATCH {}` **不再卸载**，卸载必须显式 `{"instance":null}`（界面的「卸载」发的就是它）；调用方脚本里用 `{}` 卸载的都要改（本文件清理段已改）。

- [ ] `{"instance":{"id":"<wp-vm>"}}` 挂载；`{"instance":null}` 卸载；再挂回去继续后面的检查
- [ ] `PATCH {}` → 200，**仍挂在 `wp-vm` 上**（接口 `instance` 不为空、节点上 DNAT 还在），cland 日志里这次请求没有下发 `clear_floating.sh`
- [ ] **只改带宽保持挂载**：`PATCH {"inbound":100,"outbound":50}` → 200，返回里 `inbound=100`、`outbound=50`，库里 `floating_ips.inbound/outbound` 同值；仍挂在 `wp-vm` 上，DNAT / SNAT / 策略路由都还在（规则数与改之前相同，不多不少）；cland 日志下发的是 `set_floating_bandwidth.sh '<router>' '52.117.101.158' '756' '<内网 vlan>' '<浮动 IP id>' '100' '50'`，**不是** `clear_floating.sh` + `create_floating.sh`
  ```bash
  docker logs cloudland-cland --since 1m 2>&1 | grep -oE "(set_floating_bandwidth|create_floating|clear_floating)\.sh[^\"]*" | tail -3
  # the tc objects of 52.117.101.158 (n = 26014, class 1:659e) on the three devices; ext- is on the host
  ssh work-0X "for dev in ns-<内网 vlan> te-$RT-756; do echo == \$dev; ip netns exec router-$RT tc class show dev \$dev | grep '1:659e '; ip netns exec router-$RT tc filter show dev \$dev | grep -A2 'pref 26014 ' | head -3; done
  echo == ext-$RT-756; tc class show dev ext-$RT-756 | grep -c '1:659e '
  ip netns exec router-$RT iptables -t mangle -S PREROUTING | grep $FIP"
  ```
  - [ ] 入方向：`ns-<内网 vlan>` 上有 `class htb 1:659e ... rate 100Mbit`，filter `pref 26014 fw` 指向 `1:659e`（`handle 0x<浮动 IP id 的十六进制>`）；mangle 表有 `-d 52.117.101.158/32 -j MARK --set-mark <浮动 IP id>` **一条**（再改一次带宽仍是一条）
  - [ ] 出方向：`te-<router>-756` 上有 `class htb 1:659e ... rate 50Mbit`，filter `pref 26014 u32` 带 `match 3475659e/ffffffff at 12`（源地址 `.158`）；实例浮动 IP 在宿主机的 `ext-<router>-756` 上**没有**东西（计数 0，那是给负载均衡 / VPN 入方向用的）
  - [ ] **iperf 实测**（出方向原先从没生效，只看 tc 配置不够）：`wp-vm` 里装 `iperf3`（`sudo apt-get install -y iperf3`，经浮动 IP 出网），对公网上的 iperf3 服务端测 10 秒。服务端可以用 IBM 对接环境里常驻 iperf3 的 `cl-vpntest-vsi`（`150.239.112.34`，只读使用，不改它），或自己临时起一个。出方向 `iperf3 -c <服务端> -t 10` 约 **50 Mbit/s**（±10%）；入方向 `iperf3 -c <服务端> -t 10 -R` 约 **100 Mbit/s**；改之前（不限速）各测一次作对照
    > 2026-09-30 部署后复测时：`cl-vpntest-vsi` 的 5201 从 work-01 与虚拟机都连不上；另一个 VPC 的虚拟机经 router-0 SNAT 访问 `.158` 也不行（router-0 的 SNAT 出网本身只有约 1–5 Mbit/s——这是**有意的限速**，见 `TC-03` 设计约定「没有浮动 IP 的出网」）。可用的替代：`wp-vm` 里 `curl -s -o /dev/null -m 12 -w %{speed_download} http://ash-speed.hetzner.com/1GB.bin`（入方向，不限速约 930 Mbit/s）与 `head -c 300000000 /dev/zero | curl -s -o /dev/null -m 12 -w %{speed_upload} --data-binary @- http://speedtest.tele2.net/upload.php`（出方向，不限速约 200 Mbit/s），结果 ×8/10⁶ 换成 Mbit/s
  - [ ] `PATCH {"inbound":0,"outbound":0}` → **200**，去掉限速：两块设备上这个地址的 class / filter 与 MARK 规则都消失，挂载不变；库里 `inbound=outbound=0`（2026-09-30 改：PATCH 的带宽校验由 `min=1` 放宽为 `min=0`，0 表示不限速，与 `set_floating_bandwidth.sh` 一致；之前限速一旦设上就没法经接口取消。2026-10-01 已部署，按本条判定）；`-1`、`20001` → 400
  - [ ] 部署前就设过限速的浮动 IP（旧编号 class `1:<id>`、`pref <id>`）：部署后对它改一次带宽，旧的 class / filter 被清掉、只剩新编号的（保留环境里目前没有设了限速的浮动 IP，没有就记 SKIP）
  - [ ] 设不了限速的地址（低 16 位 `0.0` / `0.16`；`public-756` 里没有这样的地址）只用节点脚本的单元测试覆盖（results.md：156 项），记 SKIP
- [ ] 未挂载时改带宽：`{"instance":null}` 卸载后 `PATCH {"inbound":200}` → 200，只写库、cland 日志**没有**下发；再挂载时 `create_floating.sh` 的第 8 个参数就是 200（按库里的值生效）
- [ ] 挂载目标不合法时**当前挂载不动**：`PATCH {"instance":{"id":"<net-c>"}}`（`net-c` 在公网子网、没有路由器；只读它，不改它）→ 400（`Instance has no router`），`wp-fip` 仍挂在 `wp-vm` 上、节点 DNAT 还在
- [ ] 带宽只对「挂在实例上 / 未挂载」的普通浮动 IP 生效：改挂到负载均衡的同时改带宽（`{"load_balancer":{"id":"<临时 LB>"},"inbound":100}`）→ 400（100003 `The bandwidth can only be changed for a floating ip of an instance`），什么都不改。要有一个没绑浮动 IP 的临时 LB（如 `TC-06` LB-01 建好、还没绑浮动 IP 时）才能测，否则以单元测试 `TestBandwidthChangeAllowed` 为准、记 SKIP；**不要拿 `rb-lb` 做目标**。负载均衡 / native / VPN 网关自己的地址 PATCH 在接口层就是 400（`Invalid public ip type`，只接受 `floating`）
- [ ] **权限**：只读成员（`TC-03 NET-08` 的 R）对 `wp-fip` 发 `PATCH {"instance":null}` → **403**（100004），仍挂载、DNAT 还在；发 `PATCH {"instance":{"id":"<wp-vm>"}}` → 403，且**节点上没有被卸载**（DNAT 一直在，cland 日志没有这次的 `clear_floating.sh`）；发 `PATCH {"inbound":10}` → 403，库里带宽不变
- [ ] 只读成员 `DELETE /floating_ips/<wp-fip>` → **403**，`.158` 仍被占用。部署后可以真的发（预期被拒）；万一被释放说明修复没生效，记 FAIL，重新申请 `.158` 再继续
- [ ] 跨组织卸载 / 释放（浮动 IP 属于 A 组织、挂在 B 组织的实例上时，B 的写成员删实例会连带卸载）只用单元 / PG 测试覆盖：`TestFloatingIpCascadeDetach`、`TestInstanceDeleteWithForeignFloatingIpPG`（`TC-01` BUILD-07b / 07c），接口上造这个场景要系统管理员跨组织挂载，本套件不做

---

## FIP-08 配额按 owner_uuid 释放

**P2** · 新增（2026-09-28）· **另占一次 `.158`**，放在 FIP-03 之后

`TC-03 NET-08` 的外部组织成员 X 申请一个浮动 IP，管理员（系统管理员，Admin 组织）把它删掉。

- [ ] 先确认 X 的组织在 clapi 里解析出的 ID 不是 0（`docker logs cloudland-clapi 2>&1 | grep "UpsertOrgByUUID: ok id="`）。部署 2026-09-30 的修复之前，是 0 时**不要做**：owner=0 的资源 `owner_uuid` 会显示成 Admin 的 UUID（FAIL-3），管理员删除时会错误地释放 Admin 组织的配额；部署之后不应再出现 0（出现就是 `TC-11` ORG-10 的新问题）
- [ ] 管理员 `DELETE /floating_ips/<X 的>?all_orgs=true` 成功（204）；Admin 组织的 `public_ips` 用量**不变**；cpgateway 日志有 `is owned by org ... not the caller's org: quota not released`
  > **2026-09-30 已修（`cpgateway/src/services/quota.go` 的 `PrepareQuota` 把原始查询串带给删除前的 GET，`cpgateway/src/apis/proxy.go` 传入），2026-10-01 已部署，按本条判定**。不带 `?all_orgs=true` 时仍是 400（clapi 按调用方组织过滤，属设计）。
  >
  > 原记录（D10 / FAIL-N2，2026-09-28）：带 `?all_orgs=true` 也返回 400 `Failed to query floatingIp (Details: record not found)`——网关删除前算释放量的 GET 丢了查询串，GET 400 后网关中止删除，「按 owner_uuid 不释放」的分支经网关走不到。部署前做完由 X 自己删掉这个浮动 IP
- [ ] X 组织的用量仍多算 1，等它下次登录对账纠正（每个组织-区域一小时内只对账一次）
- [ ] `.158` 回到空闲

---

## 历史缺陷回归点

| # | 现象 | 根因 | 判定 |
|---|---|---|---|
| 1 | 测试脚本把浮动 IP 挂到了保留的 `rb-be3` 上，差点破坏 LB 环境 | 脚本「选第一个下拉项」 | 一切选择都按名称匹配 `wp-` |
| 2 | 实例下拉括号里显示 UUID | `inst.ip_address` 字段不存在 | 见 FIP-04 |
| 3 | `create_lb_floating.sh` 未设带宽限制时删 tc 规则输出 `invalid priority value` | **既有噪音**，不影响功能 | 不要当故障 |
| 4 | 删除负载均衡时它挂的弹性 IP 配额没释放 | 网关删除 LB 时要连带释放 | `TC-06` 里核对 |
| 5 | 浮动 IP 在热迁移时中断 ~0.6 秒，completed 阶段再断 ~1.1 秒 | **设计取舍，决定不改**（2026-09-15）。只丢包不断连接 | 见 `TC-07` |
| 6 | 子网网关地址能被指定分配（2026-09-24 发现，2026-09-27 修，`36098cf7`，待办 B11） | 地址列表把网关行当空闲返回，`AllocateAddress` 指定地址时不排除网关；选中就把公网网关占掉、整个子网断网。弹性 IP、云服务器（TC-02 创建弹窗）、VPN 三处下拉同源 | FIP-01 的下拉与接口两项、`TC-03 NET-09` |
| 7 | 删负载均衡 / VPN 网关的浮动 IP 时，备节点上同路由器共用的 `te-<router>-<vlan>` 被删，另一个 LB / 网关切主时浮动 IP 上不来（2026-09-24 修，`dd248a2e`） | `clear_lb_floating.sh` 原先按「口上没有地址」判断，备节点上 keepalived 平时不持有地址 | `TC-06` / `TC-17`；普通浮动 IP 的 `clear_floating.sh` 见第 8 条 |
| 8 | 卸载普通浮动 IP 时同样会删掉 LB / VPN 备节点上共用的 `te-` 口；MARK 规则与入方向 tc 从来没删过（2026-09-28 读代码发现；2026-09-30 修，2026-10-01 已部署） | `clear_floating.sh` 只看口上有没有地址；mark 用内网地址 `$3` 算（算术错误，没有 mark） | FIP-03 的「带宽残留为 0」「共用外网口不被误删」两项 |
| 9 | 只改带宽的 `PATCH {"inbound":N}` 把浮动 IP 卸掉，带宽也没落库；`PATCH {}` 等于卸载（D5 / FIP-07，2026-09-28；2026-09-30 修，2026-10-01 已部署） | `Update` 无条件先卸载，只有带 `instance` 才挂回 | FIP-07：`{}` 与只改带宽都保持挂载，改带宽下发 `set_floating_bandwidth.sh`；卸载要 `{"instance":null}` |
| 10 | 只读成员能卸载、释放浮动 IP；只读成员挂载被 403 之前，卸载已在节点上执行（D5，2026-09-28；2026-09-30 修，2026-10-01 已部署） | `Detach` / `Delete` 没有角色检查；挂载目标检查在卸载之后 | FIP-07 权限三项：403 且节点 DNAT 不动 |
| 11 | 系统管理员经网关删不掉别的组织的浮动 IP，带 `?all_orgs=true` 也 400（D10 / FAIL-N2，2026-09-28；2026-09-30 修，2026-10-01 已部署） | 网关删除前算释放量的 GET 丢了查询串 | FIP-08：带 `?all_orgs=true` 204，Admin 用量不变 |
| 12 | 实例浮动 IP 的**出方向**限速从没生效（2026-09-30 审查时发现，既有；同日修，2026-10-01 已部署） | classid / prio 用 `mark_id + 2147483647`，超出 tc 16 位，tc 拒绝 | FIP-07：`te-` 上 `1:659e` 与 `pref 26014`，iperf 出方向约等于设定值 |
| 13 | 弹性 IP 列表名称列表头写成「用户名」；实例 / 子网下拉不带 `limit`（D16、FAIL-8 同类，2026-09-28；2026-09-30 修，2026-10-01 已部署） | 文案键用错；取列表不带 `limit` | FIP-04 |
| 14 | 负载均衡 / VPN 网关浮动 IP 的**入方向与出方向**限速都从没生效（2026-09-30 修，2026-10-01 已部署） | 出方向同上；入方向 filter 挂在 `te-` 上按目的地址匹配，而发往 VIP 的流量是从 `te-` 进路由器的 | `TC-06` LB-04「负载均衡浮动 IP 限速」：入方向在宿主机 `ext-` 上，iperf 实测 |
| 15 | 浮动 IP id 增长到 10000 以上后限速设不上（2026-09-30 修，2026-10-01 已部署） | tc 把十进制 id 当十六进制次号读，16 位放不下 | 编号改为公网地址低 16 位；FIP-07 按 `1:659e` 核对 |

---

## 清理

```bash
ssh work-01 'source /root/alarmtest-env.sh
for f in $(api GET "/floating_ips?limit=100" | jq -r ".floating_ips[]|select(.name|startswith(\"wp-\"))|.id"); do
  # detach needs an explicit null since 2026-09-30 ("{}" keeps the attachment); DELETE detaches by itself as well
  api PATCH /floating_ips/$f "{\"instance\":null}" >/dev/null; api DELETE /floating_ips/$f >/dev/null
done
api GET "/floating_ips?limit=100" | jq -r ".total, (.floating_ips[]|\"\(.public_ip) \(.type) \(.name)\")"'
```

- [ ] 只剩 9 个保留的：`rb-fip-*`(.151)、`mig-fip-*`(.150)、`net-c-*`(.149)、`a1-*`(.152)、`ibmx-gw-*`(.153)、`ibmr-gw-*`(.154/.155)、`aax-gw-*`(.156/.157)
- [ ] `.158` 是 `allocated=false`，`public_ips` 用量 9，`max_public_ips` 是 200
