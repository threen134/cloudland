# Private 子网类型实现方案

## 背景

当前子网类型有 `public`、`internal`、`site`、`vrrp` 四种。用户希望创建不绑定 VPC 的 VLAN 子网（RFC 1918 网段），使其能走 `private_vlan_interface` 物理口。

现有类型无法满足：
- **internal** — 强制要求绑定 VPC
- **public** — 不要求 VPC，但需要系统管理员权限，且会自动创建 dummy floating IP，语义上是公网子网
- **site** — 不能创建虚拟机接口

需要一个新的子网类型 `private`，语义：**管理员创建的独立 VLAN 私网子网，普通用户可以直接使用（挂载 VM），不需要 VPC**。

## 设计原则

- `private` 子网的行为与 `public` 基本一致（不绑 VPC、手动指定 VLAN），但语义上是私网
- **创建需要系统管理员权限**，普通用户只能使用（挂载 VM、读取列表）
- VM 挂载时走标准 interface 创建流程，不创建 dummy floating IP（与 `internal` 一致）
- 权限模型与 `public` 一致：List/Get 不做额外组织级权限检查（因为是管理员创建的共享子网）
- 不影响现有 `private_vlan_interface` 的 RFC 1918 自动判断逻辑

## 数据流

```
管理员创建 private 子网（指定 VLAN ID < 4095 + RFC 1918 网段）
  → 普通用户创建 VM 时选择该子网
  → GetInterfaceInfo() 判断 IsPrivate=true（因为网段是 RFC 1918 且 VLAN < 4095）
  → attach_vm_nic.sh 传 nic_dev=$private_vlan_interface
  → create_link.sh 收到 interface 参数，创建 VLAN 子接口时绑定到 private_vlan_interface 物理口
  → 网桥仍是 br$vlan，但上行物理口不同于 public（public 走默认 vlan_interface）
```

### 网桥与物理口对比

| 类型 | 网桥 | 物理上行口 | 说明 |
|------|------|-----------|------|
| public | br$vlan | `$vlan_interface`（默认） | 公网 IP，IsPrivate=false，create_link.sh 不传 interface 参数 |
| private | br$vlan | `$private_vlan_interface` | RFC 1918，IsPrivate=true，create_link.sh 传入 private_vlan_interface |
| internal (VXLAN) | br$vni | `$vxlan_interface` | VNI >= 4095，走 VXLAN 隧道 |

## 涉及文件

### 1. Go 层 — 新增 Private 常量

**文件**: `api/src/common/constant.go`

```go
const (
    Public   SubnetType = "public"
    Internal SubnetType = "internal"
    Private  SubnetType = "private"   // 新增
    Site     SubnetType = "site"
    Vrrp     SubnetType = "vrrp"
)
```

### 2. Go API 层 — Create 校验逻辑

**文件**: `api/src/apis/subnet.go`

当前第 220-223 行：
```go
if payload.VPC == nil && payload.Type == Internal {
    ErrorResponse(c, http.StatusBadRequest, "VPC must be specified if network type not public", err)
    return
}
```

改为：
```go
if payload.VPC == nil && payload.Type == Internal {
    ErrorResponse(c, http.StatusBadRequest, "VPC must be specified for internal subnets", err)
    return
}
```

> `private` 类型不要求 VPC，此处条件不变（只拦 `Internal`），`private` 自然放行。

### 3. Go 服务层 — Create 权限校验

**文件**: `api/src/services/subnet.go`

当前第 411-423 行只对 `public` 检查系统管理员权限和禁止 VPC：
```go
if rtype == "public" {
    permit = memberShip.IsSystemAdmin()
    if !permit {
        // ...
    }
    if router != nil {
        // public subnet can not be in VPC
    }
}
```

改为同时处理 `private`：
```go
if rtype == "public" || rtype == "private" {
    permit = memberShip.IsSystemAdmin()
    if !permit {
        logger.Error("Not authorized for this operation")
        err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
        return
    }
    if router != nil {
        logger.Errorf("%s subnet can not be created in a vpc", rtype)
        err = NewCLError(ErrPublicSubnetCannotInVPC, fmt.Sprintf("Not able to create %s subnet in a vpc", rtype), nil)
        return
    }
}
```

### 4. Go API 层 — Patch 校验

**文件**: `api/src/apis/subnet.go`

当前第 86 行：
```go
Type SubnetType `json:"type" binding:"omitempty,oneof=public internal site"`
```

改为：
```go
Type SubnetType `json:"type" binding:"omitempty,oneof=public internal private site"`
```

### 5. Go 服务层 — Get/GetByUUID/GetByName 权限

**文件**: `api/src/services/subnet.go`

当前逻辑（第 166-172, 203-210, 240-246 行）：
```go
if subnet.Type == "internal" {
    permit := memberShip.CheckResourceOrg(model.OrgReader, subnet.Owner)
    if !permit {
        // 403
    }
}
```

**不需要修改**。`private` 子网不走 `internal` 分支，与 `public`/`site` 一样不做额外组织权限检查，所有用户可读。

### 6. Go 服务层 — VM 接口创建

**文件**: `api/src/services/instance.go`（第 852-873 行）和 `api/src/services/interface.go`（第 488-513 行）

当前逻辑：
```go
if subnet.Type == "site" {
    // 拒绝
}
if subnet.Type == "public" {
    // 创建 dummy floating IP
}
```

**不需要修改**。`private` 类型不匹配 `"site"` 也不匹配 `"public"`，走标准 interface 创建流程，不创建 dummy floating IP。

> 注意：虽然不创建 dummy floating IP 这点与 `internal` 一致，但 `private` 没有 VPC/Router（RouterID=0），整体行为更接近 `public`（无 VPC 的共享子网），只是少了 dummy floating IP。

### 7. Go RPC 层 — FDB 规则（跳过 private）

**文件**: `api/src/rpcs/launch_vm.go`（第 72, 85 行）

`private` 子网走物理 VLAN，由物理交换机处理 L2 转发，不需要 FDB 规则。且 `private` 的 RouterID=0，若不跳过，第 78 行 `WHERE router_id = 0` 会查到所有无 VPC 的接口，导致 FDB 传播范围错误。

改为与 `public` 一样跳过：

第 72 行：
```go
if subnetType != string(Public) && subnetType != string(Private) {
    spreadRules = append(spreadRules, ...)
}
```

第 85 行：
```go
if iface.Address == nil || iface.Address.Subnet == nil || subnetType == "public" || subnetType == "private" {
    continue
}
```

### 8. 前端 — 类型定义

**文件**: `web/src/api/networks.ts`

第 61 行和第 88 行的类型定义：
```typescript
type?: 'public' | 'internal' | 'private' | 'site'
```

### 9. 前端 — 创建表单（按角色过滤类型选项）

**文件**: `web/src/views/dashboard/SubnetList.vue`

引入 auth store 判断用户角色：
```typescript
import { useAuthStore } from '../../stores/auth'
const authStore = useAuthStore()
const isSystemAdmin = computed(() => authStore.user?.role === 'admin' || authStore.user?.is_superuser)
```

第 349-353 行，下拉选项根据角色动态渲染，`Private` 和 `Public` 仅系统管理员可见：
```vue
<select v-model="newSubnetForm.type" class="form-input">
  <option value="internal">Internal</option>
  <option v-if="isSystemAdmin" value="public">Public</option>
  <option v-if="isSystemAdmin" value="private">Private</option>
  <option value="site">Site</option>
</select>
```

第 85 行，VPC 条件不变（`private` 不需要 VPC）：
```typescript
const requiresVpc = computed(() => newSubnetForm.value.type === 'internal')
```

### 10. 前端 — 类型徽章颜色

**文件**: `web/src/views/dashboard/SubnetList.vue`

第 145-152 行：
```typescript
const getTypeClass = (type: string) => {
    const map: Record<string, string> = {
        'public': 'badge-success',
        'internal': 'badge-primary',
        'private': 'badge-info',       // 新增：区别于 internal 和 public
        'site': 'badge-warning'
    }
    return map[type] || 'badge-gray'
}
```

## 不需要改动的文件

| 文件 | 原因 |
|------|------|
| `api/src/common/interface.go` | `isPrivateNetwork()` 基于 IP 判断，不看子网类型 |
| `api/src/model/subnet.go` | Type 字段是 varchar(20)，无需改模型 |
| `scripts/kvm/attach_vm_nic.sh` | 只读 `is_private` JSON 字段，不关心子网类型 |
| `scripts/kvm/attach_link.sh` | 同上 |
| `api/src/services/floatingip.go` | floating IP 只分配给 `public` 类型，`private` 不涉及 |
### 11. Go 服务层 — Delete 权限校验

**文件**: `api/src/services/subnet.go`

当前第 561 行用 `CheckResourceOrg(OrgWriter, subnet.Owner)` 检查删除权限。`private` 子网是系统管理员创建的共享子网，需要与创建保持对称，只允许系统管理员删除：

```go
if subnet.Type == "public" || subnet.Type == "private" {
    permit = memberShip.IsSystemAdmin()
} else {
    permit = memberShip.CheckResourceOrg(model.OrgWriter, subnet.Owner)
}
if !permit {
    logger.Error("Not authorized to delete the subnet")
    err = NewCLError(ErrPermissionDenied, "Not authorized to delete the subnet", nil)
    return
}
```

## 不需要改动的文件

| 文件 | 原因 |
|------|------|
| `api/src/common/interface.go` | `isPrivateNetwork()` 基于 IP 判断，不看子网类型 |
| `api/src/model/subnet.go` | Type 字段是 varchar(20)，无需改模型 |
| `scripts/kvm/attach_vm_nic.sh` | 只读 `is_private` JSON 字段，不关心子网类型 |
| `scripts/kvm/attach_link.sh` | 同上 |
| `api/src/services/floatingip.go` | floating IP 只分配给 `public` 类型，`private` 不涉及 |

## 行为对比

| 行为 | public | private | internal |
|------|--------|---------|----------|
| 创建权限 | 系统管理员 | 系统管理员 | 组织成员 |
| 删除权限 | 系统管理员 | 系统管理员 | 同组织写权限 |
| 使用权限（挂载 VM） | 所有用户 | 所有用户 | 同组织成员 |
| 需要 VPC | 否 | 否 | 是 |
| 手动指定 VLAN | 可以 | 可以 | 可以（但通常自动分配 VNI） |
| VM 创建时 dummy floating IP | 是 | 否 | 否 |
| FDB 规则 | 跳过 | 跳过（物理 VLAN，无需 FDB） | 发送 |
| 走 private_vlan_interface | 否（公网 IP） | 是（RFC 1918 + VLAN < 4095） | 取决于网段 |

## 典型使用流程

1. **系统管理员**在前端创建子网：
   - 类型选 `Private`
   - 网段填 RFC 1918 地址，如 `192.168.10.0/24`
   - VLAN 填 < 4095 的值，如 `100`
   - 不选 VPC

2. **普通用户**创建 VM 时选择该 Private 子网

3. 系统自动判断 `IsPrivate=true`，VM 网卡走 `private_vlan_interface` 物理口

## 测试要点

### 创建权限
- [ ] 系统管理员创建 private 子网 → 成功
- [ ] 普通用户创建 private 子网 → 403
- [ ] 普通用户创建表单看不到 Private/Public 选项
- [ ] private 子网指定 VPC → 拒绝

### 删除权限
- [ ] 系统管理员删除 private 子网 → 成功
- [ ] 普通用户删除 private 子网 → 403

### VM 使用
- [ ] 普通用户创建 VM 选择 private 子网 → 成功，不创建 dummy floating IP
- [ ] VM 网卡 is_private=true，走 private_vlan_interface

### 列表与查看
- [ ] 普通用户能看到 private 子网列表
- [ ] 普通用户能查看 private 子网详情

### 回归
- [ ] public/internal/site 类型行为不变
- [ ] 已有子网不受影响
