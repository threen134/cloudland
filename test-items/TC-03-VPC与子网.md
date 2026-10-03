# TC-03 VPC 与子网（NET）

优先级 **P0** · 会在计算节点上真实创建网络对象 · 约 40 min（NET-08 要准备测试账号，另计 15 min）

> VPC 和子网不只是数据库记录 —— 会在计算节点上建 netns（`router-<N>`）、VXLAN 设备（`v-<vni>`）、网关口和 dnsmasq。**测完必须删干净**，否则节点上会越积越多。
>
> 🚫 所有写操作只针对本用例建的 `wp-` 资源。保留 VPC（`rb-vpc`、`mig-vpc`、`ibmx`、`ibmr`、`aax`）与公网子网 `public-756` 只做读，**不要对它们发 PATCH / DELETE**（哪怕预期会被拒：检查一旦失效就是真改）。

## 测试环境（2026-09-28 基线）

- 管理员视角：VPC 5 个（`rb-vpc`、`mig-vpc`、`ibmx`、`ibmr`、`aax`）；子网 10 个 —— `public-756`(public)、`rb-int` / `mig-int` / `ibmx-net` / `ibmr-net` / `aax-net`(internal)，以及 4 个 VRRP 子网 `rb-lb-*`、`ibmx-gw-*`、`ibmr-gw-*`、`aax-gw-*`（负载均衡 / VPN 网关自动建的，`192.168.196.0/24`）。
- 节点 netns：work-01 = `router-0 8 50 51 52`；work-02 / work-03 = `router-0 5 8 50 51 52`。
- 界面验证：首选本机 `npm run dev`（5173，`/api` 代理到 work-01 真实环境），Playwright 在本机跑；VPC / 子网页面自 `facff649` 起没有改动，work-01 的 nginx 界面与本地一致，也可以在 work-01 的 Playwright 容器里跑下面的原脚本（打 `https://127.0.0.1`）。**只读检查一律按 pathname 兜底拦截写请求**（见 `00-环境与前置.md` §3）；写入类脚本只放开本用例要写的路径。
- `api GET` 默认只回 50 条，取全量带 `?limit=100`。

## 设计约定（判定预期的依据）

- VPC 创建时自动建一个 `<name>-native` 安全组（`is_default=true`），**预置对全网开放的 22/3389**，另有出方向 tcp/udp 全开、入方向 udp 68、icmp 进出任意；之后 VPC 里每建一个子网，再给 native 组加两条「从该子网网段进来的 tcp/udp 1-65535」（`setRouting`）。
- 内部子网（`type=internal`）必须带 `vpc`（否则 400 `VPC must be specified for internal subnets`），VNI 在 4096–16777215 里随机分配，走 VXLAN，**自带 DHCP**。
- `type=private` / `public` 是 VLAN 子网，**仅系统管理员可建（403）**，**不能放进 VPC**（400，error_code 131110）。
- 请求体校验：`gateway` 必须是纯 IP（带 `/24` 返回 400）；`network_cidr` 必须是 IPv4 CIDR，地址数 5–1000（`/30`、`/22` 都返回 400，error_code 100012）；子网名 2–64 字符、VPC 名 2–32 字符。
- **修改子网的权限**（2026-09-21 `d70a6606`，与删除同一规则）：公网 / 私网子网只有系统管理员能改；其他子网要求本组织写权限；改类型只有系统管理员；`priority` 不传时保留原值（0 是合法值）。`PATCH` 只改数据库记录，类型、IP 组、DHCP 改了**不会重新下发到节点**，所以界面只开放改名称（公网子网额外可改优先级）。
- **子网网关地址不能被指定分配**（2026-09-27 `36098cf7`，待办 B11）：`GET /addresses/:subnet` 仍把网关那一行当空闲返回（公网子网的网关是上游路由器，从不标记为已分配），但给云服务器 / 弹性 IP 指定网关地址一律 400（`common.CreateInterface` 的 `ClaimsSubnetGateway`，只有类型以 `gateway` 开头的网卡——子网自己的网关口——放行）；前端三个地址下拉经 `web/src/api/networks.ts` 的 `assignableAddresses` 排除网关。
- **删除 VPC 的前置检查**（`services/router.go`）：先查本组织**写权限**（403 100004，在开事务与下发任何命令之前，2026-09-30 修，2026-10-01 已部署）；再查 VPN 网关（409，132033，界面文案「该 VPC 有 VPN 网关，请先删除网关。」；2026-09-30 起排在浮动 IP 之前，2026-10-01 已部署；原先网关的公网地址先被浮动 IP 检查拦下、返回 400 131307，D14）；然后是浮动 IP（含负载均衡的，error_code 131307，界面文案「该 VPC 关联了弹性公网 IP，无法删除。」）、非 VRRP 子网（131308）、端口映射 / 负载均衡（131309）。删除时连带删 VRRP 子网和**该 VPC 下的全部安全组**（native 组和用户建的组）。改 VPC（`PATCH /vpcs/:id`）同样要本组织写权限（2026-09-30 修，2026-10-01 已部署）。
- 新环境要先建 `type=public` 的子网，否则 system router 分配不到公网 IP。
- **VPC 网关是 anycast**：每个节点的路由器都持有同一个网关 IP。所以「从 A 节点的 router netns ping B 节点上的虚拟机」**必然不通** —— 虚拟机把回包发给了本地路由器。验证连通性要在虚拟机所在节点上做，或用另一台虚拟机发起。
- VXLAN 设备端口固定 **8472**（nmcli 的默认值；IANA 的 4789 会导致跨节点完全不通）。没有配置泛洪条目，广播 / 未知单播不跨节点。

- **没有浮动 IP 的虚拟机出网是限速的（有意设计，不是故障）**：VPC 路由器与 router-0 之间的 `ti-<N>` 口在路由器 netns 的 FORWARD 链里进、出各限 `system_packet_rate_limit` 个包 / 秒（默认 **120**，突发为其一半，超出直接 DROP），由 `scripts/kvm/create_local_router.sh` 在建路由器时加（上游 2025-01-27 `3a39b110`「add packet rate limit for system router」）。所以经 router-0 SNAT 出网只有约 1 Mbit/s 量级（实测下载 140–280 KB/s；GSO 合并的大包只算一个包，所以会跳动），有浮动 IP 的虚拟机不经过这条路、不受影响。数值可在节点 `cloudrc.local` 里设 `system_packet_rate_limit`，但规则只在**新建路由器时**加一次（`-C` 判重），改了配置已有路由器不会更新。看命中：`ip netns exec router-<N> iptables -L FORWARD -v -n -x`（2026-10-01 查清）

---

## NET-01 界面创建 VPC + 内部子网

**P0** · 脚本 `vpcsubnet.js`（写入类：只放开 `POST /api/v1/vpcs`、`POST /api/v1/subnets`）

### 步骤

```bash
sed 's/\r$//' vpcsubnet.js > /tmp/x.js && scp -q /tmp/x.js work-01:/root/console-test/vpcsubnet.js
ssh work-01 'cd /opt/cloudland/deploy/docker && PW=$(grep "^ADMIN_PASSWORD=" .env | cut -d= -f2-)
docker run --rm --network host -v /root/console-test:/work -w /work -e ADMIN_PASSWORD="$PW" \
  mcr.microsoft.com/playwright:v1.55.0-noble node vpcsubnet.js 2>&1 | tail -10'
```

脚本流程：登录 → VPC 页点新建、填名称、提交 → 子网页点新建、填名称 / 选类型「内网」/ 选刚建的 VPC / 填 CIDR `192.168.77.0/24` / 网关 `192.168.77.1` → 提交 → 读回。VPC 名 `wp-vpc-<ts>`，子网名 `wp-subnet-<ts>`。

### 检查项

- [ ] 建 VPC：表格行数 +1，无 4xx/5xx 请求
- [ ] 子网表单里的「VPC」下拉**能找到刚建的 VPC**（下拉是空的就是 bug）
- [ ] 这个下拉的请求是 `GET /vpcs?limit=500`（Playwright 记查询串）
  > **2026-09-30 已修（`web/src/views/dashboard/SubnetList.vue` 用 `OPTION_LIST_LIMIT`），2026-10-01 已部署，按本条判定**。原记录：不带 `limit`，超过 50 个 VPC 会缺项（与 FAIL-8 同类）
- [ ] 建子网：表格行数 +1，弹窗无错误，无失败请求
- [ ] 新 VPC 的 native 组：`api GET "/security_groups?vpc_id=<VPC uuid>"` 只有 `<VPC 名>-native`，`is_default=true`，规则含 tcp 22 与 3389（`0.0.0.0/0`），建完子网后多出 `subnet-192.168.77.0-24-ingress-tcp/udp` 两条
- [ ] **建 VPC 不改动别的安全组的默认标记**：建之前、之后各查一次 `is_default` 的组，之后只多出新 VPC 的 native 组，原有的一个都不少（尤其是 id 最小的那个组）
  ```bash
  docker exec cloudland-postgres psql -U postgres -d cloudland -Atc "select id, name from security_groups where is_default and deleted_at is null order by id" > /tmp/sgdef.before
  # ... create the VPC, then the same query into /tmp/sgdef.after
  diff /tmp/sgdef.before /tmp/sgdef.after    # only the new <vpc>-native line added
  ```
  > **2026-09-30 已修（`api/src/services/secgroup.go` 的 `Switch`：路由器还没有默认组时不再 `Take` ID 0），2026-10-01 已部署，按本条判定**。原先新建 VPC 时 `Take(ID=0)` 取到任意一个组、把它的 `is_default` 清掉（审查 D5 时顺带发现，既有问题）。部署前实测若有组被清掉，照实记、手工改回
- [ ] `pageerrors: none`

### 涉及接口

| 方法 | 路径 | 请求体要点 |
|---|---|---|
| POST | `/api/v1/vpcs` | `{"name","description"}`，名称 2–32 |
| POST | `/api/v1/subnets` | `{"name","network_cidr","gateway","type":"internal","vpc":{"id"}}`，可选 `dns`、`base_domain`、`start_ip`、`end_ip`、`vlan`、`dhcp`、`priority` |
| GET | `/api/v1/vpcs` · `/api/v1/subnets` | `offset`/`limit`/`query`/`order`；子网另有 `group_id`、`ipgroup_type` |
| PATCH | `/api/v1/vpcs/:id` | `{"name"}` 必填，`description` 可选（传空串即清空） |
| PATCH | `/api/v1/subnets/:id` | `name` / `priority` / `type` / `group` / `dhcp`，都可选 |

---

## NET-02 节点侧对象真的建出来了

**P0**

VPC 和子网创建后，路由器 netns 是**懒创建**的 —— 只有当该 VPC 里有资源落到某个节点时才会在那个节点上出现。所以要先在子网里建一台虚拟机。

### 步骤

```bash
ssh work-01 'source /root/alarmtest-env.sh
S=$(api GET "/subnets?limit=100" | jq -r ".subnets[]|select(.name|startswith(\"wp-subnet\"))|.id")
IMG=$(api GET "/images?limit=100" | jq -r ".images[]|select(.name==\"ubuntu-24.04-minimal\")|.id")
K=$(api GET "/keys?limit=100" | jq -r ".keys[]|select(.name==\"vmtest-key\")|.id")
cat > /tmp/vm.json <<JEOF
{"hostname":"wp-vm","image":{"id":"$IMG"},"flavor":"vmtest-small","zone":"zone0",
 "keys":[{"id":"$K"}],"primary_interface":{"subnets":[{"id":"$S"}]}}
JEOF
curl -sk -X POST "$B/instances" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -d @/tmp/vm.json \
  | jq -c ".[0]|{hostname,status,ip:.interfaces[0].ip_address}"'
```

等到 `running`，记下分配到的 IP 和所在节点。VNI 与路由器编号这样取（路由器编号就是 `routers.id`，接口不返回）：

```bash
ssh work-01 'source /root/alarmtest-env.sh
api GET "/subnets?limit=100" | jq -r ".subnets[]|select(.name|startswith(\"wp-subnet\"))|\"VNI=\(.vlan)\""
docker exec cloudland-postgres psql -U postgres -d cloudland -Atc "select '"'"'RT='"'"' || id from routers where name like '"'"'wp-vpc%'"'"' and deleted_at is null"'
```

然后在**虚拟机所在节点**上核对：

```bash
VNI=<子网的 vlan 值>
RT=<router 编号>

ssh work-0X "ip -br link show v-$VNI                          # VXLAN device
ip -d link show v-$VNI | grep -o 'dstport [0-9]*'             # port
ip netns list | grep -w router-$RT                            # netns
ip netns exec router-$RT ip -4 -br addr show ns-$VNI          # gateway address
ps -ef | grep -c '[d]nsmasq --interface=ns-$VNI'              # DHCP (one dnsmasq per VNI)
ip netns exec router-$RT ping -c3 -W2 <VM IP>"
```

### 检查项

- [ ] `v-<VNI>` 设备存在，且 `dstport 8472`
- [ ] netns `router-<N>` 存在
- [ ] netns 内 `ns-<VNI>` 上有网关地址 `192.168.77.1/24`
- [ ] 该 VNI 有且只有 1 个 dnsmasq 进程
  > 不要用 `ip netns exec ... ps -ef | grep dnsmasq`：netns 不隔离 PID，会把本机所有 dnsmasq（含 work-01 上控制面的 dnsmasq 容器）都数进去（2026-09-28 核对）
- [ ] 从该节点的 netns ping 虚拟机 **0% 丢包**
- [ ] 虚拟机拿到了正确的 IP（DHCP 日志在宿主机上：`grep DHCPACK /var/log/dnsmasq.log | tail`，能看到虚拟机 MAC）
  > ubuntu 镜像由 cloud-init 按 config drive 静态配置（`ip route` 显示 `proto static`），**不发 DHCP 请求**，日志里没有它的 DHCPACK，只能在 `router-<N>/<VNI>/dhcp_hosts` 看到 MAC→IP 绑定、登录虚拟机看地址。要验证 DHCP 本身，另在同一子网建一台 `cirros-0.6.2` + `vmtest-tiny`，它所在节点的日志里有 `DHCPACK(ns-<VNI>) <IP> <MAC> <主机名>`（2026-09-28 核对）

### ⚠️ 排查提示

新建 VPC 路由器时，节点日志会输出：

```
Error: argument "br<N>" is wrong: Device does not exist
```

这是**既有噪音**，不是故障。根因是 `create_local_router.sh` → `create_veth.sh int-<N>`：`int-` 这条 veth 要移进 `router-0`、不挂网桥，但 `create_veth.sh` 末尾无条件执行 `ip link set dev $device master br${device##*-}`。

> 这台 `wp-vm` 与 `wp-vpc` / `wp-subnet` 留给 NET-04 到 NET-09、`TC-04`（挂 `.158` 浮动 IP）、`TC-05 SG-02 / SG-05`（安全组与生效）复用，都做完再跑 NET-03 删除。执行顺序：NET-01 → 02 → 04 → 05 → 06 → 07 → 08 → 09 →（TC-04、TC-05）→ NET-03。

---

## NET-03 删除后节点侧清理干净

**P0**

### 步骤

```bash
ssh work-01 'source /root/alarmtest-env.sh
V=$(api GET "/vpcs?limit=100" | jq -r ".vpcs[]|select(.name|startswith(\"wp-vpc\"))|.id")
echo "--- delete the VPC while its subnet exists: expect 400 / 131308"
curl -sk -o /tmp/o.json -w "%{http_code} " -X DELETE "$B/vpcs/$V" -H "Authorization: Bearer $T"; jq -c "{error_code,error_message}" /tmp/o.json
I=$(api GET "/instances?limit=100" | jq -r ".instances[]|select(.hostname==\"wp-vm\")|.id"); api DELETE /instances/$I >/dev/null
sleep 30
S=$(api GET "/subnets?limit=100" | jq -r ".subnets[]|select(.name|startswith(\"wp-subnet\"))|.id"); api DELETE /subnets/$S >/dev/null
sleep 5
api DELETE /vpcs/$V
sleep 6
api GET "/vpcs?limit=100" | jq -r ".vpcs[].name"
api GET "/security_groups?limit=100" | jq -r ".security_groups[].name" | grep wp- || echo "native and user groups of the VPC are gone"'
```

```bash
ssh work-01 'for h in local work-02 work-03; do echo -n "$h: "
  c="ip netns list | tr \"\n\" \" \"; echo -n \" | vxlan: \"; ip -br link show type vxlan | awk \"{print \\\$1}\" | tr \"\n\" \" \""
  if [ "$h" = local ]; then sh -c "$c"; else ssh -n root@$h "$c"; fi; echo
done'
```

### 检查项

- [ ] 子网还在时删 VPC 返回 **400**，error_code **131308**（`There are associated subnets`），VPC 与节点侧对象都没动
- [ ] VPC 列表不再有 `wp-vpc` 开头的（NET-08 的 `wp-rvpc` 由「清理」一并删）
- [ ] `<name>-native` 安全组**随 VPC 连带删除**；该 VPC 下用户建的安全组（如 `TC-05` 的 `wp-sg`）也一起删除
- [ ] 三个节点上 `router-<N>` netns 已消失
- [ ] 三个节点上 `v-<VNI>` VXLAN 设备已消失
- [ ] `/opt/cloudland/cache/router/router-<N>/` 目录已删除
- [ ] 保留资源不受影响：work-01 仍有 `router-0 8 50 51 52`，work-02 / work-03 仍有 `router-0 5 8 50 51 52`（2026-09-28 基线）

---

## NET-04 参数校验

**P1**

```bash
ssh work-01 'source /root/alarmtest-env.sh
V=$(api GET "/vpcs?limit=100" | jq -r ".vpcs[]|select(.name|startswith(\"wp-vpc\"))|.id")
try(){ cat > /tmp/b.json; echo -n "$1: "; curl -sk -o /tmp/o.json -w "%{http_code} " -X POST "$B/subnets" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -d @/tmp/b.json; jq -c "{error_code}" /tmp/o.json; }
try "gateway with mask" <<JEOF
{"name":"wp-bad1","network_cidr":"192.168.99.0/24","gateway":"192.168.99.1/24","type":"internal","vpc":{"id":"$V"}}
JEOF
try "internal without vpc" <<JEOF
{"name":"wp-bad2","network_cidr":"192.168.99.0/24","type":"internal"}
JEOF
try "cidr /30" <<JEOF
{"name":"wp-bad3","network_cidr":"192.168.99.0/30","type":"internal","vpc":{"id":"$V"}}
JEOF
try "cidr /22" <<JEOF
{"name":"wp-bad4","network_cidr":"192.168.96.0/22","type":"internal","vpc":{"id":"$V"}}
JEOF
try "cidr without mask" <<JEOF
{"name":"wp-bad5","network_cidr":"1.2.3.4","type":"internal","vpc":{"id":"$V"}}
JEOF
try "vlan subnet in a vpc" <<JEOF
{"name":"wp-bad6","network_cidr":"10.250.1.0/24","type":"public","vlan":999,"vpc":{"id":"$V"}}
JEOF
api GET "/subnets?limit=100" | jq -r ".subnets[].name" | grep wp-bad || echo "no leftovers"'
```

- [ ] `gateway` 带 `/24` → **400**（`Invalid input JSON`）
- [ ] 内部子网不带 `vpc` → **400**
- [ ] `/30`（4 个地址）、`/22`（1024 个地址）→ **400**，error_code **100012**
- [ ] CIDR 漏掩码、`999.0.0.0/8` → **400**
- [ ] 管理员往 VPC 里放 VLAN 子网 → **400**，error_code **131110**
- [ ] 以上都没有留下 `wp-bad*` 子网
- [ ] 名称 1 个字符或 65 个字符 → 400（子网名后端是 2–64；VPC 名是 2–32）
- [ ] 界面：子网页、VPC 详情页的新建子网弹窗按 64 校验名称，不合法时输入框标红并显示提示，点提交被拦下、不发请求（提交按钮不禁用）；VPC 列表页行内「新建子网」弹窗按 32 校验（比后端严，不算缺陷）（2026-09-28 核对）
- [ ] 界面：**子网表单不在前端校验 CIDR**，非法 CIDR 提交后弹窗内显示后端 400 的报错、不产生残留（2026-09-28 核对：原预期「前端拦住」与代码不符）
- [ ] 非系统管理员创建 `type=public` / `private` 子网 → **403**（界面上这两个类型选项只对系统管理员显示）
  > 已知：VPC 列表页新建 VPC / 行内新建子网的两句空值提示是写死的英文（`VPCList.vue` 的 `Please enter a VPC name.`、`Please fill in Name and CIDR.`），`i18n:check` 只查含中文的字面量，抓不到

---

## NET-05 子网 IP 与地址列表

**P1**

```bash
ssh work-01 'source /root/alarmtest-env.sh
for n in rb-int public-756; do
  S=$(api GET "/subnets?limit=100" | jq -r ".subnets[]|select(.name==\"$n\")|.id")
  api GET /subnets/$S | jq -c "{name, gateway, total_count, allocated_count, reserved_count, available_count, idle_count}"
  api GET /addresses/$S | jq -c "{n:(.addresses|length), allocated:[.addresses[]|select(.allocated)]|length, free:[.addresses[]|select((.allocated|not) and (.reserved|not))|.address]}"
done'
```

- [ ] 返回该子网的地址池，`allocated` / `reserved` 标记正确
- [ ] `public-756`（2026-09-28 基线）：共 14 行，已分配 12；**空闲的是 `.158` 和网关 `.145`**——网关那一行被当作空闲返回是**设计如此**（上游路由器从不标记为已分配），但 `available_count` / `idle_count` 都排除网关，等于 **1**
- [ ] 内部子网的网关行是已分配的（子网的网关口占着它）
- [ ] 创建虚拟机 / 弹性 IP 的弹窗、VPN 网关的公网地址选择器里，IP 下拉只列出**未分配、未保留、且不是子网网关**的地址（见 NET-09）

---

## NET-06 子网列表：服务端分页、搜索、排序、过滤

**P1** · 新增（2026-09-28）

分页、搜索、排序都在服务端做（`SubnetList.vue` 用 `useListQuery`，默认每页 20）。

```bash
ssh work-01 'source /root/alarmtest-env.sh
api GET "/subnets?limit=3"                  | jq -c "{total, n:(.subnets|length)}"
api GET "/subnets?limit=3&offset=9"         | jq -c "{total, n:(.subnets|length)}"
api GET "/subnets?query=int&limit=100"      | jq -c "{total, names:[.subnets[].name]}"
api GET "/subnets?order=vlan&limit=100"     | jq -c "[.subnets[].vlan]"
api GET "/subnets?order=-name&limit=100"    | jq -c "[.subnets[].name]"
api GET "/subnets?ipgroup_type=system&limit=100" | jq -c "{total}"
for q in "ipgroup_type=bogus" "group_id=00000000-0000-0000-0000-000000000000" "limit=-1"; do
  echo -n "$q: "; curl -sk -o /dev/null -w "%{http_code}\n" "$B/subnets?$q" -H "Authorization: Bearer $T"; done'
```

- [ ] `limit` / `offset` 生效，`total` 是过滤后的总数（管理员基线 10，加上本轮的 `wp-subnet` 是 11）
- [ ] `query` 按名称子串匹配（区分大小写，`%`、`_` 按字面匹配）：`query=int` 只回 `rb-int`、`mig-int`
- [ ] `order` 按真实列排序（`name` / `network` / `vlan` / `type`，`-` 前缀倒序）；IP / CIDR 列是 varchar 字典序，不是数值序
- [ ] `ipgroup_type` 只接受 `system` / `resource`，其他值 400；不存在的 `group_id` 400；负的 `offset` / `limit` 400
- [ ] 多个过滤条件（`query` + `group_id` + `ipgroup_type`）是 **AND**，不互相覆盖（`TC-13 SEC-01`）
- [ ] 列表里能看到 VRRP 子网（类型标签「VRRP」），不要从界面删它们（有 VRRP 网卡占用，接口会 400 `Some addresses of this subnet are still in use`，但保留环境不做这个试验）
- [ ] 界面：搜索、翻页、按名称 / CIDR / VLAN / 类型排序都触发新的请求；「IP 使用率」「VPC」两列不可排序
  > **接口没有按 VPC、按类型过滤**：前端 `subnetsApi.list` 的 `vpc` 参数后端不认，界面也没有这两个筛选。`TC-12` 里「子网列表名称搜索 + VPC 过滤为 AND」一条与代码不符（2026-09-28 核对），以本条为准

---

## NET-07 编辑子网（界面 + PATCH 语义）

**P1** · 新增（2026-09-28），`2cd919cd`（界面）+ `d70a6606`（后端）

### 界面（本机 5173，只放开对 `wp-subnet` 的 PATCH）

- [ ] 子网列表每行有「编辑」（铅笔图标）；弹窗标题「编辑子网 {name}」，只有名称；公网子网多一个「优先级」（0–100000 的整数，清空后不能保存，提示「0-100000，越小优先级越高」）
- [ ] 以系统管理员打开 `public-756` 的编辑弹窗：能看到优先级输入框 → **只看不保存，点取消**
- [ ] 非系统管理员：公网 / 私网子网的编辑按钮禁用，悬停提示「公网 / 私网子网是共享资源，只有系统管理员可以修改」
- [ ] 只发送改动过的字段；什么都没改直接点保存 → 关闭弹窗，不发请求
- [ ] 名称规则只校验**新名称**：名称没改时，即使旧名不符合规则（接口建的子网可能带 `.`）也能保存
- [ ] 保存成功后列表静默刷新，toast「更新成功」；组织动态里有 `subnet.update`

### 接口（只对 `wp-subnet`）

```bash
ssh work-01 'source /root/alarmtest-env.sh
S=$(api GET "/subnets?limit=100" | jq -r ".subnets[]|select(.name|startswith(\"wp-subnet\"))|.id")
api PATCH /subnets/$S "{\"priority\":7}"                     | jq -c "{name,priority,type,dhcp}"
api PATCH /subnets/$S "{\"name\":\"wp-subnet-renamed\"}"     | jq -c "{name,priority,type,dhcp}"
api PATCH /subnets/$S "{\"priority\":0}"                     | jq -c "{name,priority}"
curl -sk -o /dev/null -w "type=vrrp -> %{http_code}\n" -X PATCH "$B/subnets/$S" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -d "{\"type\":\"vrrp\"}"'
```

- [ ] 只改名后 `priority` 仍是 7（**回归点**：原先每次修改都把优先级清零）
- [ ] 显式传 `priority: 0` 能改成 0
- [ ] `type` 只接受 `public` / `internal` / `private` / `site`，传 `vrrp` 返回 400
- [ ] 改名后 `type`、`dhcp` 不变

> 名称改了以后，后面步骤里按 `startswith("wp-subnet")` 取子网的命令照样能用。

---

## NET-08 子网与 VPC 的改动权限

**P1** · 新增（2026-09-28）

### 准备账号

照 `TC-18 PG-14` 的做法（脚本模板 work-01 `/root/pg-e2e-14.sh`，里面有 `ulogin` / `ucall`）准备三个账号，用户名带日期后缀（注销后用户名永久占用，不能复用）：

| 代号 | 身份 |
|---|---|
| R | Admin 组织的**只读**成员 |
| W | Admin 组织的**读写**成员（非系统管理员） |
| X | 另一个组织的读写成员。该组织要由**管理员** `POST /orgs` 建，建完确认 clapi 里解析出的组织 ID 不是 0（`docker logs cloudland-clapi 2>&1 \| grep "UpsertOrgByUUID: ok id="`）。部署 2026-09-30 的修复之前：自助注册的组织不同步到区域（FAIL-2），ID 是 0 时按 CLAUDE.md 用内部同步接口重推一次（FAIL-1）；**部署之后**这两条都不该再出现，出现就按 `TC-11` ORG-10 记新问题 |

再以管理员另建一个空 VPC `wp-rvpc`（没有子网），给下面的 VPC 删除试验用。

### 检查项（只对 `wp-` 资源）

| 操作 | R | W | X |
|---|---|---|---|
| `GET /subnets/<wp-subnet>` | 200 | 200 | **403** |
| `PATCH /subnets/<wp-subnet>` 改名 | **403** | 200 | **403** |
| `PATCH /subnets/<wp-subnet>` `{"type":"site"}` | 403 | **403**（`Only system admins can change the subnet type`） | 403 |
| `PATCH /subnets/<public-756>` `{"name":"public-756"}`（同名，万一放行也不改任何值） | **403** | **403** | **403** |
| `GET /subnets` | 看得到本组织子网 + 公网子网 | 同 R | 只看得到公网 / 私网子网，看不到 `wp-subnet` |
| `PATCH /vpcs/<wp-rvpc>` `{"name":"wp-rvpc-x"}` | **403**（100004 `Not authorized to update the router`） | 200 | 400（按组织过滤查不到） |
| `DELETE /vpcs/<wp-rvpc>` | **403**（100004 `Not authorized to delete the router`），**不下发** `clear_local_router.sh` | — | 400 |

按表从上到下执行，一律用 UUID 引用（改名后名字会变），新名字都以 `wp-` 开头（如 `wp-subnet-w`），免得清理时漏掉。

- [ ] 子网各项符合上表（**回归点**：`d70a6606` 之前 `PATCH /subnets/:id` 没有写权限检查，只读成员能改本组织子网，任何登录用户能改公网 / 私网子网的名称、类型、优先级）
- [ ] VPC 的改名与删除只允许本组织写成员：R 改名 → 403、名字不变；R 删除 → 403、VPC 还在，且 cland 日志里**没有**这次请求下发的 `clear_local_router.sh '<router id>'`
  ```bash
  # cland only logs the control string, never the command text, and since 2026-09-30 the node journal only shows the
  # sudo wrapper (the command travels in an environment variable). A VPC delete is the only bare "toall=" (empty group):
  docker logs cloudland-cland --since <R 的 DELETE 之前的时间> --until <之后 10 秒> 2>&1 | grep -c "control=toall= "    # expect 0
  ```
  > 2026-09-30 部署后复测时核对：原写法 `grep -c "clear_local_router.sh '<router id>'"` 在 cland 日志里永远是 0（日志不含命令文本），测不出来；管理员删同一个 VPC 时 cland 记一条 `control=toall= `，可作对照
  > **2026-09-30 已修（`api/src/services/router.go`：`Update` 加写权限检查；`Delete` 的写权限检查挪到开事务与下发之前），2026-10-01 已部署，按本条判定**
  >
  > 原记录（D5 / FAIL-N1，2026-09-28）：`RouterAdmin.Update` 没有权限检查（R 改名 200）；`RouterAdmin.Delete` 只要求 `OrgReader`，R 删除一路做到删 native 安全组才被挡住（400 141004），事务回滚、VPC 还在，但回滚前已 `toall=` 下发了 `clear_local_router.sh`，节点侧清理不会回滚
- [ ] 响应里只有业务消息（error_code 100004），没有数据库原始报错

测完注销三个账号、删掉 X 的组织（`/root/pg-e2e-14-cleanup.sh` 的写法）。

---

## NET-09 子网网关地址不能被指定分配（B11）

**P0** · 新增（2026-09-28），`36098cf7`

### 接口

```bash
ssh work-01 'source /root/alarmtest-env.sh
cgw(){ docker exec cloudland-postgres psql -U postgres -d cloudland_cpgateway -Atc "select public_ips, cpu_cores from org_resource_consumptions c join organizations o on o.id=c.org_id where o.uuid='"'"'305fcaf9-c76a-4ed5-a1ae-eddb8d8fc64e'"'"'"; }
echo "usage before: $(cgw)"
echo "--- floating ip on the public gateway"
cat > /tmp/f.json <<JEOF
{"name":"wp-gwfip","public_ip":"52.117.101.145"}
JEOF
curl -sk -o /tmp/o.json -w "%{http_code} " -X POST "$B/floating_ips" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -d @/tmp/f.json; jq -c "{error_code,error_message}" /tmp/o.json
echo "--- instance on the internal gateway"
S=$(api GET "/subnets?limit=100" | jq -r ".subnets[]|select(.name|startswith(\"wp-subnet\"))|.id")
GW=$(api GET /subnets/$S | jq -r ".gateway|split(\"/\")[0]")
IMG=$(api GET "/images?limit=100" | jq -r ".images[]|select(.name==\"ubuntu-24.04-minimal\")|.id")
K=$(api GET "/keys?limit=100" | jq -r ".keys[]|select(.name==\"vmtest-key\")|.id")
cat > /tmp/vmgw.json <<JEOF
{"hostname":"wp-gwvm","image":{"id":"$IMG"},"flavor":"vmtest-small","zone":"zone0","keys":[{"id":"$K"}],
 "primary_interface":{"subnets":[{"id":"$S"}],"ip_address":"$GW"}}
JEOF
curl -sk -o /tmp/o.json -w "%{http_code} " -X POST "$B/instances" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -d @/tmp/vmgw.json; jq -c "{error_code,error_message}" /tmp/o.json
echo "usage after: $(cgw)"
PS=$(api GET "/subnets?limit=100" | jq -r ".subnets[]|select(.name==\"public-756\")|.id")
api GET /addresses/$PS | jq -c ".addresses[]|select(.address==\"52.117.101.145/28\")|{address,allocated}"
docker exec cloudland-postgres psql -U postgres -d cloudland -Atc "select count(*) from instances where hostname='"'"'wp-gwvm'"'"' and deleted_at is null"'
```

- [ ] 指定 `.145` 申请弹性 IP → **400**，error_code 131202，消息含 `52.117.101.145 is the gateway of subnet public-756 and cannot be assigned`
- [ ] 云服务器主网卡指定内部子网网关 → **400**，消息含 `is the gateway of subnet`（error_code 111015）；`instances` 里没有 `wp-gwvm`
  > 规格要用 `vmtest-small`：`vmtest-tiny` 的磁盘装不下 ubuntu 镜像，会先返回 400 111906（`Flavor disk size is not enough for the image`），测不到网关检查（2026-09-28 执行时发现，已改）
- [ ] `.145` 仍是 `allocated=false`；cpgateway 的 `public_ips`、`cpu_cores` 用量前后不变（预扣已退回）
- [ ] 子网自己的网关口不受影响：NET-01/02 建出的内部子网网关 `192.168.77.1` 照常配在 `ns-<VNI>` 上（网关口类型 `gateway` 放行）

### 界面（本机 5173，只读）

脚本 `gw-exclude.js`（在 work-01 `/root/console-test/`，`BASE=http://127.0.0.1:5173`，拷到本机跑；已按 pathname 拦截全部写请求）：

- [ ] 弹性 IP 新建弹窗选 `public-756` 后，「公网 IP」下拉**不含 `.145`**（现在只有 `.158`）
- [ ] 创建云服务器弹窗的地址下拉不含所选子网的网关
- [ ] VPN 网关新建弹窗的公网地址选择器不含 `.145`
- [ ] 脚本记录的原始 `GET /addresses/...` 里 `.145` 确实是空闲行（否则这项检查证明不了什么）
- [ ] 弹性 IP 弹窗的「公网 IP」还有一个自由输入框：手工填 `.145` 提交，弹窗内显示后端 400 的报错（界面写请求拦截下看不到这一步，用接口那一项代替）

---

## 历史缺陷回归点

| # | 现象 | 根因 | 判定方法 |
|---|---|---|---|
| 1 | 新计算节点上虚拟机与其他节点二层隔离（浮动 IP 仍正常，容易误判成迁移问题） | `nmcli connection up v-<vni>` 在新节点上必然失败（netplan 的 udev 规则只把已知接口标为 managed）；而 `create_link.sh` 在「网桥已存在」时直接退出，缺失的上联口永远补不上 | 新增计算节点后，在该节点上确认 `v-<vni>` 存在且 `master br<vni>` |
| 2 | 跨节点完全不通 | VXLAN `dstport` 用了 IANA 的 4789，与 nmcli 建出来的 8472 不一致 | `ip -d link show v-<vni> \| grep dstport` 必须是 8472 |
| 3 | 第二个节点激活网桥约 30s 后 `br<vlan>` 与 `v-<vlan>` 被删除，system router 外网口断开 | `create_link.sh` 给每个节点的 `br<vlan>` 配同一个 `169.254.169.254/32`，NM 的 IPv4 地址冲突检测判定冲突 | 建网桥时必须带 `ipv4.dad-timeout 0`；已有节点 `nmcli connection modify br<vlan> ipv4.dad-timeout 0` |
| 4 | 内部子网虚拟机拿不到 DHCP | 路由器 netns 的 INPUT 默认 DROP，没放行 `-i ns-+ udp/67`（请求源地址 0.0.0.0 不在 nonat 集合） | `ip netns exec router-<N> iptables -S INPUT \| grep 67` |
| 5 | 节点重启后子网网关口补不回来 | `add_fwrule.sh` 原先按**网桥**是否存在决定是否建网关口。网桥由 NM 持久化、开机自动重建，但 netns 里的网关口不会 | 现在应按 `ns-<vni>` 是否存在判断。重启节点后见 `TC-14` |
| 6 | cirros 虚拟机在 public/private 子网拿不到 IP | **这是设计，不是 bug**。普通 VLAN 子网按设计不提供 DHCP，只靠 config drive；cirros 不带 cloud-init | 不要「修」它 |
| 7 | 只读成员能改本组织子网，任何登录用户能改公网 / 私网子网的名称、类型、优先级（2026-09-21 修，`d70a6606`，待办 A4） | `PATCH /subnets/:id` 没有写权限检查 | NET-08 子网各行 |
| 8 | 编辑子网只改名，优先级被清零（2026-09-21 修） | `priority` 是值类型，不传即 0 | NET-07 接口第一项 |
| 9 | 子网网关地址能被指定分配，选中就把公网网关占掉、整个子网断网（2026-09-24 发现，2026-09-27 修，`36098cf7`，待办 B11） | 地址列表把网关行当空闲返回，`AllocateAddress` 指定地址时不排除网关；弹性 IP、云服务器、VPN 三处下拉同源 | NET-09 |
| 10 | 字典 / 子网 / IP 组列表的多个过滤互相覆盖（后者盖前者）；`query` 直接拼进 SQL（2026-09-17 修） | 过滤写成 SQL 片段后被覆盖；名称搜索未参数绑定 | NET-06 与 `TC-13 SEC-01` |
| 11 | 子网名 33–64 个字符前端放行、后端 400 或反过来（2026-09-20 修，`c0770e1a`） | 前端 `isValidName` 原先不校验长度，子网后端是 max=64 | NET-04 名称两项 |
| 12 | 子网列表的「编辑」按钮没绑事件，点了没反应（2026-09-21 修，`2cd919cd`） | 从未实现 | NET-07 界面 |
| 13 | 只读成员能改 VPC 名；只读成员删 VPC 被安全组权限挡住回滚，但回滚前已向所有节点下发 `clear_local_router.sh`（D5 / FAIL-N1，2026-09-28；2026-09-30 修，2026-10-01 已部署） | `RouterAdmin.Update` 无权限检查；`Delete` 只要求 `OrgReader`，且检查在下发之后 | NET-08：R 改名 / 删除都是 403，cland 日志没有这次的 `clear_local_router.sh` |
| 14 | 新建 VPC 时别的某个安全组的 `is_default` 被清掉（审查 D5 时发现；2026-09-30 修，2026-10-01 已部署） | `SecgroupAdmin.Switch` 在路由器还没有默认组时 `Take(ID=0)`，取到任意一个组 | NET-01：建 VPC 前后 `is_default` 的组只多出新 native 组 |
| 15 | 子网表单的 VPC 下拉超过 50 个 VPC 缺项（与 FAIL-8 同类；2026-09-30 修，2026-10-01 已部署） | 取列表不带 `limit` | NET-01：请求带 `limit=500` |

---

## 清理

```bash
ssh work-01 'source /root/alarmtest-env.sh
for n in $(api GET "/instances?limit=100" | jq -r ".instances[]|select(.hostname|startswith(\"wp-\"))|.id"); do api DELETE /instances/$n >/dev/null; done
sleep 30
for n in $(api GET "/subnets?limit=100"   | jq -r ".subnets[]|select(.name|startswith(\"wp-\"))|.id");      do api DELETE /subnets/$n   >/dev/null; done
for n in $(api GET "/vpcs?limit=100"      | jq -r ".vpcs[]|select(.name|startswith(\"wp-\"))|.id");         do api DELETE /vpcs/$n      >/dev/null; done
api GET "/vpcs?limit=100" | jq -r ".total"; api GET "/subnets?limit=100" | jq -r ".total"'
```

- [ ] VPC 回到 5 个、子网回到 10 个（2026-09-28 基线）
- [ ] NET-08 的三个账号已注销、X 的组织已删

然后跑 `00-收尾检查.md` 的 C-05 / C-06。
