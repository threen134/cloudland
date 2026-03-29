# RFC 1918 私有网段独立 VLAN 接口方案

## 背景

当前计算节点的 VLAN 流量（VNI < 4095）统一走 `vlan_interface` 指定的物理设备。在实际部署中，RFC 1918 私有网段（10.0.0.0/8、172.16.0.0/12、192.168.0.0/16）和公网 VLAN 可能需要走不同的物理口——例如内部业务走 bond0，公网 VLAN 走 bond1。

目标：新增 `private_vlan_interface` 配置，RFC 1918 子网的 VLAN 自动使用该接口，非 RFC 1918 子网继续走 `vlan_interface`，不破坏现有网络逻辑。

## 设计原则

- **向后兼容** — `private_vlan_interface` 可选，不配时退化为 `vlan_interface`，已有部署无需改动
- **职责分离** — Go 层（clapi）只做 RFC 1918 判断，传布尔标记 `is_private`；物理设备名解析在计算节点 Shell 层完成（从本地 `cloudrc.local` 读取），因为 clapi 是中心化服务，不知道每个计算节点的实际网卡名
- **不破坏现有网络** — VXLAN（>= 4095）路径完全不变；公网 VLAN 路径不变；仅 RFC 1918 子网增加设备选择
- **显式传递 is_private** — 所有涉及网桥创建的 Shell 脚本路径，都必须通过 JSON 字段或环境变量获得 `is_private` 标记，**绝不靠猜 VLAN 号决定物理接口**
- **不破坏参数签名** — 对含变长参数（如 `[gateways...]`）的脚本，使用环境变量而非位置参数传递可选标记，避免参数位移导致数据丢失

## 判断规则

```
subnet.Network 的网络地址落在以下范围内 → 标记 is_private = true：
  - 10.0.0.0/8
  - 172.16.0.0/12
  - 192.168.0.0/16

否则 → is_private = false（create_link.sh 使用默认 vlan_interface）
```

> **注意**：使用 Go 标准库 `net.IP.IsPrivate()`（Go 1.17+ 内置），不手写 CIDR 遍历。

## 数据流变更

```
现有流程：
  Go GetInterfaceInfo() → VlanInfo{Device: "eth0", Vlan: 100, ...}
  → JSON 传入 attach_vm_nic.sh → ./create_link.sh $vlan
  → create_link.sh 用 $vlan_interface（因为没传第二参数）

新增流程（RFC 1918 子网时）：
  Go GetInterfaceInfo() → VlanInfo{Device: "eth0", Vlan: 100, IsPrivate: true, ...}
  → JSON 传入 attach_vm_nic.sh → 读取 is_private=true
  → 从本地 cloudrc.local 读 $private_vlan_interface
  → ./create_link.sh $vlan $private_vlan_interface
  → create_link.sh 用传入的物理设备
```

## 涉及文件

### 1. Go 层 — VlanInfo 结构体增加 IsPrivate 字段

**文件**: `api/src/common/interface.go`

```go
type VlanInfo struct {
    Device        string          `json:"device"`
    IsPrivate     bool            `json:"is_private"`    // 新增：标记子网是否为 RFC 1918 私有网段
    Vlan          int64           `json:"vlan"`
    Gateway       string          `json:"gateway"`
    Router        int64           `json:"router"`
    PublicLink    int64           `json:"public_link"`
    Inbound       int32           `json:"inbound"`
    Outbound      int32           `json:"outbound"`
    AllowSpoofing bool            `json:"allow_spoofing"`
    IpAddr        string          `json:"ip_address"`
    MacAddr       string          `json:"mac_address"`
    SecRules      []*SecurityData `json:"security"`
    MoreAddresses []string        `json:"more_addresses"`
}
```

> 不传设备名，只传布尔标记。设备名由计算节点本地 `cloudrc.local` 决定，避免 clapi 中心化服务硬编码各节点的网卡名。

### 2. Go 层 — RFC 1918 判断 + GetInterfaceInfo 填充 IsPrivate

**文件**: `api/src/common/interface.go`

使用 Go 标准库 `net.IP.IsPrivate()`（Go 1.17+），替代手写 CIDR 遍历：

```go
// isPrivateNetwork 判断给定的网段（CIDR 格式，如 "10.0.1.0/24"）是否属于 RFC 1918 私有地址范围
func isPrivateNetwork(network string) bool {
    ip, _, err := net.ParseCIDR(network)
    if err != nil {
        ip = net.ParseIP(network)
        if ip == nil {
            logger.Errorf("isPrivateNetwork: failed to parse network %q, treating as non-private", network)
            return false
        }
    }
    return ip.IsPrivate()
}
```

> **与旧方案的区别**：
> - 使用 `net.IP.IsPrivate()` 替代手动遍历三个 CIDR，代码更简洁、不易出错
> - 解析失败时记录 Error 日志（`logger.Errorf`），便于排查，不再静默返回 false
> - `IsPrivate()` 还会匹配 IPv6 私有地址（fc00::/7），但 CloudLand 当前只用 IPv4 CIDR，不影响

在 `GetInterfaceInfo()` 构造 vlanInfo 时设置 `IsPrivate`：

```go
vlanInfo = &VlanInfo{
    Device:        iface.Name,
    IsPrivate:     subnet.Vlan < 4095 && isPrivateNetwork(subnet.Network),  // 新增
    Vlan:          subnet.Vlan,
    // ... 其余字段不变
}
```

> 只对 VLAN（< 4095）生效，VXLAN（>= 4095）始终走 `vxlan_interface`，与此无关。

### 3. Shell 层 — attach_vm_nic.sh 读取 is_private 并决定物理设备

**文件**: `scripts/backend/attach_vm_nic.sh` 和 `scripts/kvm/attach_vm_nic.sh`

> 注意：`scripts/backend/` 和 `scripts/kvm/` 下各有一份 `attach_vm_nic.sh`，两者需同步修改。

#### 3a. backend 版本（scripts/backend/attach_vm_nic.sh）

当前第 15 行：
```bash
read -d'\n' -r vlan ip mac gateway router inbound outbound allow_spoofing < <(jq -r ".vlan, .ip_address, .mac_address, .gateway, .router, .inbound, .outbound, .allow_spoofing" <<<$vlan_info)
```

改为：
```bash
read -d'\n' -r vlan ip mac gateway router inbound outbound allow_spoofing is_private < <(jq -r ".vlan, .ip_address, .mac_address, .gateway, .router, .inbound, .outbound, .allow_spoofing, .is_private" <<<$vlan_info)
```

当前第 18 行：
```bash
./create_link.sh $vlan
```

改为：
```bash
nic_dev=""
if [ "$is_private" = "true" ] && [ -n "$private_vlan_interface" ]; then
    nic_dev=$private_vlan_interface
fi
./create_link.sh $vlan $nic_dev
```

#### 3b. kvm 版本（scripts/kvm/attach_vm_nic.sh）

当前第 39 行：
```bash
read -d'\n' -r vlan ip mac gateway router inbound outbound allow_spoofing < <(jq -r ".vlan, .ip_address, .mac_address, .gateway, .router, .inbound, .outbound, .allow_spoofing" <<<$vlan_info)
```

改为：
```bash
read -d'\n' -r vlan ip mac gateway router inbound outbound allow_spoofing is_private < <(jq -r ".vlan, .ip_address, .mac_address, .gateway, .router, .inbound, .outbound, .allow_spoofing, .is_private" <<<$vlan_info)
```

当前第 44 行：
```bash
./create_link.sh $vlan
```

改为：
```bash
nic_dev=""
if [ "$is_private" = "true" ] && [ -n "$private_vlan_interface" ]; then
    nic_dev=$private_vlan_interface
fi
./create_link.sh $vlan $nic_dev
```

> `private_vlan_interface` 来自计算节点本地的 `cloudrc.local`（通过 `source ../cloudrc` 加载）。未配置时 `nic_dev` 为空，`create_link.sh` 的 `$2` 为空，走已有默认逻辑（`$vlan_interface`），**不影响现有行为**。

### 4. Shell 层 — attach_link.sh 路由器场景同步处理

**文件**: `scripts/backend/attach_link.sh` 和 `scripts/kvm/attach_link.sh`

#### 问题分析

路由器 gateway 同样可能挂载到 RFC 1918 子网。如果 VM 走了 `private_vlan_interface` 而同 VLAN 的路由器网关走了 `vlan_interface`，且两者是不同物理口，**网络不通**。

**旧方案的致命缺陷**：旧方案提议"对 `vlan < 4095` 统一传 `private_vlan_interface`"——这是一刀切逻辑。公网 IP 完全可能通过内部 VLAN（VLAN ID < 4095）透传到宿主机，此时公网子网不属于 RFC 1918，`attach_vm_nic.sh` 侧 `IsPrivate=false` 会将网桥建在 `vlan_interface` 上，而 `attach_link.sh` 侧一刀切将网桥建在 `private_vlan_interface` 上——网关和 VM 接在不同物理口，**公网子网瘫痪**。

#### 解决方案：通过环境变量传入 IS_PRIVATE

**为什么不能用位置参数**：`attach_link.sh` 的签名是 `$0 <router> <vlan> [gateways...]`，网关是变长参数（`shift 2; gateways=$@`）。如果把 `is_private` 插在 `$3`，旧调用方不传该参数时，`$3` 会吞掉第一个网关 IP，`shift 3` 后 `$@` 少一个网关——**网关数据被静默丢失**，且无任何报错。

**环境变量方案**：将 `IS_PRIVATE` 作为环境变量前置传入，**完全不改参数签名**，零风险向下兼容。

脚本参数解析**不动**：
```bash
router=$1
vlan=$2
shift 2
gateways=$@
```

当前第 18 行：
```bash
./create_link.sh $vlan
```

改为：
```bash
nic_dev=""
if [ "$IS_PRIVATE" = "true" ] && [ -n "$private_vlan_interface" ]; then
    nic_dev=$private_vlan_interface
fi
./create_link.sh $vlan $nic_dev
```

> **关键改进**：`IS_PRIVATE` 由 Go 层通过环境变量显式传入（来源于 `isPrivateNetwork(subnet.Network)` 的判断结果），而不是在 Shell 层靠猜 VLAN 号决定。这保证了公网 VLAN（即使 ID < 4095）不会被误绑到 `private_vlan_interface`。

#### Go 层调用方适配

所有构造 `attach_link.sh` 命令的 Go 代码，通过环境变量前缀传入 `IS_PRIVATE`：

```go
isPrivate := "false"
if subnet.Vlan < 4095 && isPrivateNetwork(subnet.Network) {
    isPrivate = "true"
}
command := fmt.Sprintf("IS_PRIVATE=%s /opt/cloudland/scripts/backend/attach_link.sh '%d' '%d' %s",
    isPrivate, router.ID, subnet.Vlan, gatewayArgs)
```

> 若 `attach_link.sh` 的调用方当前不经过 Go 层（如 C++ cloudlet 直接触发），则 `IS_PRIVATE` 环境变量未设置，`[ "$IS_PRIVATE" = "true" ]` 为 false，回退到默认 `$vlan_interface`，**安全兼容旧版调用方且不会丢失任何参数**。

### 5. 配置文件 — cloudrc.local 增加 private_vlan_interface

**文件**: `scripts/cloudrc.local.example`

```bash
vxlan_interface=bond0
vlan_interface=bond0
private_vlan_interface=bond0    # 新增：RFC 1918 私有子网使用的 VLAN 物理接口（可选，不配时使用 vlan_interface）
```

### 6. 部署脚本 — 写入 private_vlan_interface

**文件**: `deploy/docker/scripts/deploy-compute-node.sh`

第 54 行区域增加环境变量声明：
```bash
PRIVATE_VLAN_DEVICE="${PRIVATE_VLAN_DEVICE:-$VLAN_DEVICE}"   # 可选，默认同 VLAN_DEVICE
```

第 266-267 行区域（写 cloudrc.local）增加一行：
```bash
vxlan_interface=$NETWORK_DEVICE
vlan_interface=${VLAN_DEVICE:-$NETWORK_DEVICE}
private_vlan_interface=${PRIVATE_VLAN_DEVICE:-${VLAN_DEVICE:-$NETWORK_DEVICE}}   # 新增
```

### 7. API 层 — HyperDeployPayload 增加字段

**文件**: `api/src/apis/hyper.go`

```go
type HyperDeployPayload struct {
    IP                string `json:"ip" binding:"required"`
    Hostname          string `json:"hostname" binding:"required"`
    NetworkDevice     string `json:"network_device"`
    VlanDevice        string `json:"vlan_device"`
    PrivateVlanDevice string `json:"private_vlan_device"`    // 新增
    DNSServer         string `json:"dns_server"`
    Domain            string `json:"domain"`
    ZoneName          string `json:"zone_name"`
    VirtType          string `json:"virt_type"`
}
```

默认值处理（第 249 行区域）：
```go
if payload.PrivateVlanDevice == "" {
    payload.PrivateVlanDevice = payload.VlanDevice
}
```

### 8. API 服务层 — Deploy 函数增加参数

**文件**: `api/src/services/hyper.go`

`Deploy()` 签名增加 `privateVlanDevice` 参数，部署命令中增加 `PRIVATE_VLAN_DEVICE`：

```go
func (a *HyperAdmin) Deploy(ctx context.Context, ip, hostname, networkDevice, vlanDevice, privateVlanDevice, dnsServer, domain, zoneName, virtType string) (hyper *model.Hyper, deployCmd string, err error)
```

部署命令 `fmt.Sprintf` 增加 `PRIVATE_VLAN_DEVICE=%s`：
```go
deployCmd = fmt.Sprintf(
    "export CONTROLLER_IP=%s HOSTNAME=%s NETWORK_DEVICE=%s VLAN_DEVICE=%s PRIVATE_VLAN_DEVICE=%s DNS_SERVER=%s SCI_CLIENT_ID=%d DOMAIN=%s ZONE_NAME=%s VIRT_TYPE=%s; "+
        "curl -sSL %s | sudo -E bash",
    controllerIP, hostname, networkDevice, vlanDevice, privateVlanDevice, dnsServer, hostID, domain, zoneName, virtType,
    deployScriptURL,
)
```

### 9. 部署脚本环境变量示例

**文件**: `deploy/docker/scripts/compute.env.example`

```bash
PRIVATE_VLAN_DEVICE="eth0"      # RFC 1918 私有子网的 VLAN 物理接口（可选，默认同 VLAN_DEVICE）
```

## 不需要改动的文件

| 文件 | 原因 |
|------|------|
| `scripts/kvm/create_link.sh` | 已支持第二参数 `$interface`，无需修改 |
| `scripts/kvm/set_subnet_gw.sh` | 不调用 `create_link.sh`，只是 `brctl addif br$vlan ln-$vlan` 加入已有网桥，不涉及物理接口选择 |
| `scripts/kvm/set_vrrp_ip.sh` | VRRP 场景，不涉及 RFC 1918 判断 |
| `scripts/kvm/create_veth.sh` | VPC veth 创建，不涉及 |
| `scripts/kvm/add_fwrule.sh` | 防火墙规则，不涉及 |
| `api/src/model/subnet.go` | 数据模型不变 |

## 兼容性说明

1. **不配 `private_vlan_interface`**: 部署脚本 fallback 到 `VLAN_DEVICE`，`private_vlan_interface` 等于 `vlan_interface`；Shell 层 `$private_vlan_interface` 非空但与 `$vlan_interface` 相同 → 行为与当前完全一致
2. **完全不升级 cloudrc.local**: `$private_vlan_interface` 变量未定义，Shell 层 `-n "$private_vlan_interface"` 为 false，`nic_dev` 为空 → `create_link.sh` 走默认 `$vlan_interface`，行为不变
3. **VXLAN 路径不受影响**: `isPrivateNetwork()` 判断前有 `subnet.Vlan < 4095` 前置条件，VXLAN 永远不走此逻辑
4. **公网 VLAN 不受影响**: 公网 IP 不匹配 RFC 1918，`IsPrivate` 为 false，Shell 层不传设备名 → `create_link.sh` 走默认 `$vlan_interface`
5. **attach_link.sh 未设置 IS_PRIVATE 环境变量时**: `$IS_PRIVATE` 为空，`[ "$IS_PRIVATE" = "true" ]` 为 false → 回退到默认 `$vlan_interface`，**安全兼容旧版调用方，且不会丢失任何位置参数**

## 已知局限与未来改进

### 局限：RFC 1918 地址用作"公网"的特殊环境

某些私有云环境中，"公网口" 实际接的是客户内部大网（使用 10.x.x.x 等 RFC 1918 地址作为 External Subnet）。在这种场景下，仅依据 IP 是否符合 RFC 1918 判断物理接口会导致误判——本应走 `vlan_interface`（公网口）的流量被错误路由到 `private_vlan_interface`。

**当前方案的应对**：不配置 `private_vlan_interface`（或配置成与 `vlan_interface` 相同），即可安全退化。

**未来改进方向**：如需精确控制，可在子网模型中新增 `is_private_network` 字段，由管理员在创建子网时显式指定，替代自动 RFC 1918 推断。这需要修改数据库模型和前端 UI，不在本次范围内。

## 测试要点

### 功能测试
- [ ] 创建 10.0.1.0/24 子网的 VM，验证 VLAN 接口绑定到 `private_vlan_interface` 设备
- [ ] 创建 172.16.0.0/16 子网的 VM，验证同上
- [ ] 创建 192.168.100.0/24 子网的 VM，验证同上
- [ ] 创建公网 VLAN 子网（VLAN ID < 4095 但 IP 非 RFC 1918）的 VM，验证仍走 `vlan_interface`
- [ ] 创建 VXLAN 子网（VNI >= 4095）的 VM，验证仍走 `vxlan_interface`
- [ ] 热挂载网卡（attach_vm_nic）到 RFC 1918 子网，验证走 `private_vlan_interface`

### 路由器场景
- [ ] 路由器 gateway 挂载到 10.x.x.x VLAN 子网（is_private=true 显式传入），验证网桥建在 `private_vlan_interface` 上
- [ ] 路由器 gateway 挂载到公网 VLAN 子网（is_private=false），验证网桥建在 `vlan_interface` 上
- [ ] VM 与路由器在同一 RFC 1918 VLAN 子网，验证二层互通
- [ ] **回归**：公网 VLAN（ID < 4095）的 VM 和路由器，验证均走 `vlan_interface`，网络正常

### 兼容性测试
- [ ] 不配置 `private_vlan_interface`，验证所有子网行为与改动前一致
- [ ] 已部署节点不更新 cloudrc.local，验证行为不变
- [ ] `private_vlan_interface` 与 `vlan_interface` 相同时，验证无副作用
- [ ] 已有 VM 重启后网络恢复正常
- [ ] `attach_link.sh` 不设置 `IS_PRIVATE` 环境变量（模拟旧版调用方），验证回退到 `vlan_interface`，且所有网关 IP 正确传递

### 多节点测试
- [ ] 节点 A 配 `private_vlan_interface=bond2`，节点 B 配 `private_vlan_interface=bond3`，验证各节点使用各自的设备

### 单元测试（Go）
- [ ] `isPrivateNetwork("10.0.1.0/24")` → true
- [ ] `isPrivateNetwork("10.255.255.0/24")` → true
- [ ] `isPrivateNetwork("172.16.0.0/16")` → true
- [ ] `isPrivateNetwork("172.31.255.0/24")` → true
- [ ] `isPrivateNetwork("172.32.0.0/16")` → false（超出 172.16-31 范围）
- [ ] `isPrivateNetwork("192.168.100.0/24")` → true
- [ ] `isPrivateNetwork("8.8.8.0/24")` → false
- [ ] `isPrivateNetwork("100.64.0.0/16")` → false（CGN 地址，非 RFC 1918）
- [ ] `isPrivateNetwork("invalid-string")` → false（并输出 Error 日志）
