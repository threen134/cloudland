# TC-03 VPC 与子网（NET）

优先级 **P0** · 会在计算节点上真实创建网络对象 · 约 20 min

> VPC 和子网不只是数据库记录 —— 会在计算节点上建 netns（`router-<N>`）、VXLAN 设备（`v-<vni>`）、网关口和 dnsmasq。**测完必须删干净**，否则节点上会越积越多。

## 设计约定（判定预期的依据）

- VPC 创建时自动建一个 `<name>-native` 安全组，**预置对全网开放的 22/3389**。
- 内部子网（`type=internal`）VNI 自动分配（> 4095），走 VXLAN，**自带 DHCP**。
- `type=private` / `public` 是 VLAN 子网，**仅系统管理员可建，不能放进 VPC**。
- `gateway` 必须是纯 IP，带 `/24` 返回 400。
- 新环境要先建 `type=public` 的子网，否则 system router 分配不到公网 IP。
- **VPC 网关是 anycast**：每个节点的路由器都持有同一个网关 IP。所以「从 A 节点的 router netns ping B 节点上的虚拟机」**必然不通** —— 虚拟机把回包发给了本地路由器。验证连通性要在虚拟机所在节点上做，或用另一台虚拟机发起。
- VXLAN 设备端口固定 **8472**（nmcli 的默认值；IANA 的 4789 会导致跨节点完全不通）。没有配置泛洪条目，广播 / 未知单播不跨节点。

---

## NET-01 界面创建 VPC + 内部子网

**P0** · 脚本 `vpcsubnet.js`

### 步骤

```bash
sed 's/\r$//' vpcsubnet.js > /tmp/x.js && scp -q /tmp/x.js work-01:/root/console-test/vpcsubnet.js
ssh work-01 'cd /opt/cloudland/deploy/docker && PW=$(grep "^ADMIN_PASSWORD=" .env | cut -d= -f2-)
docker run --rm --network host -v /root/console-test:/work -w /work -e ADMIN_PASSWORD="$PW" \
  mcr.microsoft.com/playwright:v1.55.0-noble node vpcsubnet.js 2>&1 | tail -10'
```

脚本流程：登录 → VPC 页点新建、填名称、提交 → 子网页点新建、填名称 / 选类型「内部」/ 选刚建的 VPC / 填 CIDR `192.168.77.0/24` / 网关 `192.168.77.1` → 提交 → 读回。

### 检查项

- [ ] 建 VPC：表格行数 +1，无 4xx/5xx 请求
- [ ] 子网表单里的「VPC」下拉**能找到刚建的 VPC**（下拉是空的就是 bug）
- [ ] 建子网：表格行数 +1，弹窗无错误，无失败请求
- [ ] `pageerrors: none`

### 涉及接口

| 方法 | 路径 | 请求体要点 |
|---|---|---|
| POST | `/api/v1/vpcs` | `{"name"}` |
| POST | `/api/v1/subnets` | `{"name","network_cidr","gateway","type":"internal","vpc":{"id"}}` |
| GET | `/api/v1/vpcs` · `/api/v1/subnets` | `offset`/`limit`/`query`/`order` |

---

## NET-02 节点侧对象真的建出来了

**P0**

VPC 和子网创建后，路由器 netns 是**懒创建**的 —— 只有当该 VPC 里有资源落到某个节点时才会在那个节点上出现。所以要先在子网里建一台虚拟机。

### 步骤

```bash
ssh work-01 'source /root/alarmtest-env.sh
S=$(api GET /subnets | jq -r ".subnets[]|select(.name|startswith(\"wp-subnet\"))|.id")
IMG=$(api GET /images | jq -r ".images[]|select(.name==\"ubuntu-24.04-minimal\")|.id")
K=$(api GET /keys | jq -r ".keys[]|select(.name==\"vmtest-key\")|.id")
cat > /tmp/vm.json <<JEOF
{"hostname":"wp-vm","image":{"id":"$IMG"},"flavor":"vmtest-small","zone":"zone0",
 "keys":[{"id":"$K"}],"primary_interface":{"subnets":[{"id":"$S"}]}}
JEOF
curl -sk -X POST "$B/instances" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -d @/tmp/vm.json \
  | jq -c ".[0]|{hostname,status,ip:.interfaces[0].ip_address}"'
```

等到 `running`，记下分配到的 IP 和所在节点，然后在**该节点**上核对：

```bash
VNI=<子网的 vlan 值>   # api GET /subnets | jq '.subnets[]|select(.name|startswith("wp-subnet"))|.vlan'
RT=<router 编号>        # ip netns list 里新出现的那个

ssh work-0X "ip -br link show v-$VNI                      # VXLAN 设备
ip -d link show v-$VNI | grep -o 'dstport [0-9]*'         # 端口
ip netns list | grep router-$RT                           # netns
ip netns exec router-$RT ip -4 -br addr                   # 网关地址
ip netns exec router-$RT ps -ef | grep -c [d]nsmasq       # DHCP
ip netns exec router-$RT ping -c3 -W2 <虚拟机 IP>"
```

### 检查项

- [ ] `v-<VNI>` 设备存在，且 `dstport 8472`
- [ ] netns `router-<N>` 存在
- [ ] netns 内有网关地址 `192.168.77.1/24`（在 `ns-<VNI>` 上）
- [ ] netns 内有 dnsmasq 进程
- [ ] 从该节点的 netns ping 虚拟机 **0% 丢包**
- [ ] 虚拟机拿到了正确的 IP（DHCP 日志：`ip netns exec router-<N> grep DHCPACK /var/log/dnsmasq.log`）

### ⚠️ 排查提示

新建 VPC 路由器时，节点日志会输出：

```
Error: argument "br<N>" is wrong: Device does not exist
```

这是**既有噪音**，不是故障。根因是 `create_local_router.sh` → `create_veth.sh int-<N>`：`int-` 这条 veth 要移进 `router-0`、不挂网桥，但 `create_veth.sh` 末尾无条件执行 `ip link set dev $device master br${device##*-}`。

---

## NET-03 删除后节点侧清理干净

**P0**

### 步骤

```bash
ssh work-01 'source /root/alarmtest-env.sh
I=$(api GET /instances | jq -r ".instances[]|select(.hostname==\"wp-vm\")|.id"); api DELETE /instances/$I >/dev/null
sleep 30
S=$(api GET /subnets | jq -r ".subnets[]|select(.name|startswith(\"wp-subnet\"))|.id"); api DELETE /subnets/$S >/dev/null
sleep 5
V=$(api GET /vpcs | jq -r ".vpcs[]|select(.name|startswith(\"wp-vpc\"))|.id"); api DELETE /vpcs/$V
sleep 6
api GET /vpcs | jq -r ".vpcs[].name"
api GET /security_groups | jq -r ".security_groups[].name" | grep wp- || echo "native 组已连带删除"'
```

```bash
ssh work-01 'for h in local work-02 work-03; do echo -n "$h: "
  c="ip netns list | tr \"\n\" \" \"; echo -n \" | vxlan: \"; ip -br link show type vxlan | awk \"{print \\\$1}\" | tr \"\n\" \" \""
  if [ "$h" = local ]; then sh -c "$c"; else ssh -n root@$h "$c"; fi; echo
done'
```

### 检查项

- [ ] VPC 列表不再有 `wp-` 前缀的
- [ ] `<name>-native` 安全组**随 VPC 连带删除**
- [ ] 三个节点上 `router-<N>` netns 已消失
- [ ] 三个节点上 `v-<VNI>` VXLAN 设备已消失
- [ ] `/opt/cloudland/cache/router/router-<N>/` 目录已删除
- [ ] 保留资源不受影响（`router-0` / `router-5` / `router-8` 还在）

---

## NET-04 参数校验

**P1**

```bash
ssh work-01 'source /root/alarmtest-env.sh
V=$(api GET /vpcs | jq -r ".vpcs[]|select(.name==\"rb-vpc\")|.id")
echo "--- gateway 带掩码应 400"
cat > /tmp/b1.json <<JEOF
{"name":"wp-bad1","network_cidr":"192.168.99.0/24","gateway":"192.168.99.1/24","type":"internal","vpc":{"id":"$V"}}
JEOF
curl -sk -o /dev/null -w "%{http_code}\n" -X POST "$B/subnets" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -d @/tmp/b1.json'
```

- [ ] `gateway` 带 `/24` 返回 **400**
- [ ] 名称少于 2 字符或多于 32 字符，前端保存按钮禁用
- [ ] 非法 CIDR（如 `1.2.3.4`、`999.0.0.0/8`）前端就被拦住并给出提示
- [ ] 非系统管理员创建 `type=public` / `private` 子网被拒绝
- [ ] 往 VPC 里放 VLAN 子网被拒绝

---

## NET-05 子网 IP 与地址列表

**P1**

```bash
ssh work-01 'source /root/alarmtest-env.sh
S=$(api GET /subnets | jq -r ".subnets[]|select(.name==\"rb-int\")|.id")
api GET /addresses/$S | jq -c "{n:(.addresses|length), 已分配:[.addresses[]|select(.allocated)]|length}"'
```

- [ ] 返回该子网的地址池，`allocated` / `reserved` 标记正确
- [ ] 创建虚拟机 / 浮动 IP 的弹窗里，IP 下拉只列出**未分配且未保留**的地址

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

---

## 清理

```bash
ssh work-01 'source /root/alarmtest-env.sh
for n in $(api GET /instances | jq -r ".instances[]|select(.hostname|startswith(\"wp-\"))|.id"); do api DELETE /instances/$n >/dev/null; done
sleep 30
for n in $(api GET /subnets   | jq -r ".subnets[]|select(.name|startswith(\"wp-\"))|.id");      do api DELETE /subnets/$n   >/dev/null; done
for n in $(api GET /vpcs      | jq -r ".vpcs[]|select(.name|startswith(\"wp-\"))|.id");         do api DELETE /vpcs/$n      >/dev/null; done'
```

然后跑 `00-收尾检查.md` 的 C-05 / C-06。
