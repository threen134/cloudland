# Hyper Label & Selector 调度方案

> 用 k8s 风格的 label + selector 替换现有 zone 单一外键调度，实现多维度标签过滤候选 hyper 集合。

## 0. 实际驱动场景

需求职责分离：

- **subnet 与一组特定 hyper 绑定**：只按 **pod + vlan** 维度（网络拓扑），**不涉及硬件**——subnet 是网络资源，跟 cpu/mem/gpu 无关
- **VM 落地 hyper**：要按 **pod + vlan + 硬件维度** 综合过滤，来源是「所有挂载 subnet 的 selector」(贡献 pod+vlan) ∩「instance 自己的 selector」(贡献硬件偏好)

subnet 类型有三种：

- **private-vlan**：私网，VM 直接挂某 vlan
- **public 原生**：VM 网卡直接拿公网 IP（不走 NAT，常见于裸金属或单租户场景）
- **public FIP 池**：VM 在私网，通过 FIP 1:1 NAT 暴露到公网

前两种语义相同（"hyper 必须通该 vlan"），FIP 池语义不同（"hyper 必须有公网上联做 NAT"）。

### Pod 与 vlan 的拓扑事实（设计前置）

后面所有 label / selector 设计都建立在以下物理拓扑事实上：

| # | 事实 | 设计含义 |
|---|---|---|
| 1 | 一台机器**只能在一个 pod** | `pod` label 是单值，`hyper.pod` 一对一 |
| 2 | 一个 pod 是**一组机器**的集合 | `pod=pod-a` 的 selector 命中该 pod 全部机器 |
| 3 | 一个 pod 内部 **public vlan 和 private vlan 各自通常唯一** | 同 pod 内 `vlan-internal` / `vlan-public` 的 value 通常单一；少数例外才需 trunk 多 vlan 兜底 |
| 4 | **不同 pod 可以复用同一个 vlan id** | vlan id 不全局唯一 → subnet selector **必须 `pod` + `vlan` 联合**才能精确定位，单写 `vlan-internal=100` 会跨 pod 误命中 |

事实 4 是 subnet selector 强制要求 `pod` 维度的根本原因（已在 §Subnet 设计 中体现）。

### Hyper 标签设计

hyper 上 label 是统一的（网络 + 硬件都打），**区别在于 subnet selector 只能引用网络类 label，instance selector 才能引用硬件类 label**。

**vlan label 命名约定**：一台 hyper 通常有两种形态：
- **一个内网 vlan + 一个外网 vlan**（普通计算 + 出口节点）→ 同时打 `vlan-internal` 和 `vlan-public`
- **纯外网 vlan**（只跑公网业务的边缘/网关节点）→ 只打 `vlan-public`，**不打** `vlan-internal`

所以 `vlan-internal` 是可选 key（`Exists` 检查能区分两类节点），value 是 vlan id，selector 用等值匹配。
（极少数 trunk 多个 vlan 的机器，可额外补 `vlan-{id}=true` 形式，selector 用 `In` 兜底。）

#### 网络类 label（subnet selector + instance selector 都可用）

| 维度 | label key | value 例子 | 备注 |
|---|---|---|---|
| 机房 / 故障域 | `pod` | `pod-a` / `pod-b` | 通常一台机器一个 pod |
| 内网 vlan | `vlan-internal` | `100` / `300` | 一台机器一个内网 vlan |
| 外网 vlan | `vlan-public` | `200` | 有 `vlan-public` 即代表能承载该公网 vlan 的流量；FIP 也走这个 label 做匹配，不引入额外的 `public-uplink` 标签 |

#### 硬件类 label（**只能** instance selector 用，subnet 禁用）

| 维度 | label key | value 例子 | 备注 |
|---|---|---|---|
| CPU 架构 | `cpu.arch` | `x86_64` / `arm64` | |
| CPU 厂商 | `cpu.vendor` | `intel` / `amd` | |
| CPU 型号 | `cpu.model` | `i7-13700k` / `epyc-7763` | 用户挑特定型号 |
| CPU 核数 | `cpu.cores` | `32` / `64` | |
| 内存档位 | `mem` | `128g` / `512g` | 大内存 selector 用 `Exists` 或 `In` |
| 网卡带宽 | `nic.bandwidth` | `10g` / `25g` / `100g` | 单口速率档位，按主用网卡口算 |
| 网卡数量 | `nic.count` | `2` / `4` | 物理网卡口数 |
| 磁盘容量 | `disk` | `2t` / `8t` / `16t` | 单机本地盘容量档位 |
| GPU 型号 | `gpu` | `a100` / `h100` | 无 GPU 的机器不打 |

> **校验时机**：subnet 创建/更新接口要在 Go 层校验 selector 引用的 key 必须属于「网络类」白名单，否则报错。这样防止运维不小心把 subnet 钉到某个硬件型号上，造成网络资源被硬件耦合污染。

**示例 hyper**：

| hyper | 关键 label | 形态 |
|---|---|---|
| hyper-A | `pod=pod-a`, `vlan-internal=100`, `cpu.vendor=intel`, `mem=128g`, `nic.bandwidth=10g` | 纯内网计算 |
| hyper-B | `pod=pod-a`, `vlan-internal=100`, `vlan-public=200`, `nic.bandwidth=25g`, `nic.count=4` | 内网+外网（可承载 FIP 与 public 原生网卡） |
| hyper-C | `pod=pod-b`, `vlan-internal=300`, `gpu=a100`, `nic.bandwidth=100g` | 纯内网计算（GPU） |
| hyper-D | `pod=pod-a`, `vlan-public=200`, `nic.bandwidth=25g` | 纯外网（边缘/网关） |

### Subnet 设计

subnet selector **必须同时指定 pod 和 vlan**——pod 限定故障域，vlan 限定网络通断。两者缺一会导致跨 pod 误命中（不同 pod 完全可能复用同一 vlan id）。

| subnet | type | selector | 落地约束 |
|---|---|---|---|
| private-vlan-100 | private-vlan | `{pod: "pod-a", vlan-internal: "100"}` | VM 只能起在 A、B |
| private-vlan-300 | private-vlan | `{pod: "pod-b", vlan-internal: "300"}` | VM 只能起在 C |
| public-native-200 | public 原生 | `{pod: "pod-a", vlan-public: "200"}` | VM 网卡直接拿公网，可起在 B、D |
| public-fip-pod-a-vlan200 | public (FIP 池) | `{pod: "pod-a", vlan-public: "200"}` | FIP NAT 可配在 B、D（与 public 原生 selector 形式相同，由 subnet type 字段区分语义） |

### 四类核心约束

1. **创建 VM**：落地 hyper 必须满足 `instance selector ∩ 所有挂载 subnet (含 private-vlan / public 原生) 的 selector`

2. **创建 FIP**：用户必须显式指定 `(pod, public-vlan)`。后端按这两个值匹配一个 public FIP 池 subnet，从中分配一个 IP；FIP 记录里**继承** `(pod, public-vlan)` 作为属性（实际存储可以直接复用 `subnet_id` join 出来，无需冗余列）。

3. **绑定 FIP 到 VM 的 VPC 接口**（仅 FIP 池类型 / public vlan 校验**只发生在这一步**）：cloudland 的 FIP 是 **host-NAT 模型**——FIP 直接在 VM 所在 hyper 上配 NAT（见 `api/src/services/floatingip.go:389` 处 `inter=%d` 用的是 `instance.Hyper`），**不走独立 router gateway**。绑定校验 **两层**：

   a. **VPC 层**：VM 所在 VPC 必须挂载一个相同 `(pod, public-vlan)` 的 public subnet（要么 FIP 池本身，要么 public 原生），否则 VPC 路由不通该公网 vlan，NAT 无意义。
   b. **Hyper 层**：VM 当前 hyper 必须满足 `pod=FIP.pod AND vlan-public=FIP.public-vlan`，否则拒绝绑定（UI 提示先迁移到匹配 hyper）。

4. **VM 迁移**：目标 hyper 必须同时满足 `instance selector + 所有已挂载 subnet（含已绑 FIP 所属 public subnet）的 selector`

### 实际效果

- 创建 VM 选 private-vlan-100 → subnet selector `{pod: "pod-a", vlan-internal: "100"}` → 候选 = {A, B}（D 没有 `vlan-internal` 被排除），调度器挑一台
- 同一 VM 选 public-native-200 做第二张网卡 → subnet 求交后候选 = {B}（A 没 `vlan-public`，D 没 `vlan-internal`）
- 用户额外给该 VM 写 instance selector `{cpu.model: i7-13700k, mem: 128g}` → 与 subnet 求交后再过滤硬件 → 如果 B 不满足 cpu.model，调度失败并提示"硬件需求与网络需求冲突"
- 创建 FIP：用户在前端选 `pod-a + vlan-public=200` → 后端从 `public-fip-pod-a-vlan200` 分一个 IP，FIP 记录隐含 `(pod-a, 200)`
- 该 FIP 绑到上面那个 VM（落在 B）→ B 的 label 含 `pod=pod-a, vlan-public=200`，匹配，绑定成功
- 同 FIP 想绑到 hyper-A 上的另一个 VM → A 没 `vlan-public=200`，拒绝；前端可提示"该 VM 所在 hyper 不通 vlan-200，请迁至 B/D 或换 FIP"
- pod-b 的 VM 想绑 pod-a 的 FIP → pod 不匹配，直接拒绝
- VM 所在 VPC 没挂 vlan-200 的 public subnet → 即使 hyper 物理通 vlan-200，VPC 路由不通，绑定也拒绝（VPC 层校验）
- 纯外网业务（如反向代理）只挂 public-native-200，不挂任何 private subnet → 候选 = {B, D}，调度器倾向把 D 这种纯网关节点用满

---

## 推荐方案

hyper 挂多维标签（网络类 + 硬件类），**subnet selector 限制只能用网络类 label**，**instance selector 可用全集**。Go 层做集合求交得到候选 hyper 列表，SCI 协议层保持不变（依然只收 `group-xxx:host1,host2`）。public vlan 是否存在 = "能否承载 FIP/公网流量"，不引入额外的 `public-uplink` 标签；FIP 校验**只在绑定到 VPC 接口那一步**触发。

**关键权衡**：
- subnet 也带 selector 后，要做"instance selector ∩ 所有挂载 subnet 的 selector"求交
- FIP 绑定时要单独再校验一次 public subnet selector 与 VM 当前 hyper 的匹配
- subnet 创建时强制白名单校验，防止 subnet 被硬件维度污染

---

## 一、数据模型

### 1. hyper 标签（多对多）

```sql
CREATE TABLE hyper_labels (
    id          BIGSERIAL PRIMARY KEY,
    hyper_id    BIGINT NOT NULL,         -- FK -> hypers.id
    key         VARCHAR(63) NOT NULL,    -- 如 "vlan", "cpu.arch", "dc-pod"
    value       VARCHAR(253) NOT NULL,   -- 如 "100", "x86_64", "pod-a-3"
    UNIQUE (hyper_id, key)               -- 同 key 只能有一个 value（k8s 语义）
);
CREATE INDEX idx_hyper_labels_kv ON hyper_labels(key, value);
```

**删掉** `hypers.zone_id` 字段（产品没发布，不留兼容层）。原来的 zone 信息迁成 label `zone=xxx` 即可。

### 2. instance / subnet 的 selector（JSON 列）

```sql
ALTER TABLE instances ADD COLUMN node_selector JSONB;
ALTER TABLE subnets   ADD COLUMN node_selector JSONB;
```

JSON 结构照搬 k8s `LabelSelector`：

```json
{
  "matchLabels": { "dc-pod": "pod-a", "gpu": "a100" },
  "matchExpressions": [
    { "key": "vlan",     "operator": "In",     "values": ["100","200"] },
    { "key": "cpu.arch", "operator": "NotIn",  "values": ["arm64"] },
    { "key": "reserved", "operator": "DoesNotExist" }
  ]
}
```

**为什么用 JSONB 不做规范化表**：hyper 数量小（百级），评估永远是"全表加载 + 内存过滤"，不需要复杂 SQL；JSONB 给前端足够的灵活性。

---

## 二、核心代码改造

### 1. selector 评估器（新文件 `api/src/common/selector.go`）

```go
type Selector struct {
    MatchLabels      map[string]string
    MatchExpressions []Requirement  // key, op, values
}

func (s *Selector) Matches(labels map[string]string) bool { ... }
func MergeSelectors(sels ...*Selector) *Selector { ... }  // 求交
```

支持的 operator：`In / NotIn / Exists / DoesNotExist`（先不做 Gt/Lt，后续再加）。

### 2. 替换 `common/instance.go:32-56` 的 `GetHyperGroup`

```go
// 旧：func GetHyperGroup(ctx, zoneID int64, skipHyper int32) (string, error)
// 新：
func GetHyperGroupBySelector(ctx context.Context, sel *Selector, skipHyper int32) (string, error) {
    hypers := loadActiveHypersWithLabels(ctx)        // 一次 JOIN 加载
    var matched []*model.Hyper
    for _, h := range hypers {
        if h.Hostid == skipHyper { continue }
        if sel.Matches(h.Labels) { matched = append(matched, h) }
    }
    if len(matched) == 0 { return "", ErrNoQualifiedHypervisor }
    hash := shortHash(sel)                            // 给 SCI 一个稳定 group 名便于日志排查
    return fmt.Sprintf("group-sel-%s:%s", hash, joinHostids(matched)), nil
}
```

**SCI 协议不动**，依然 `select=group-xxx:host1,host2 ...`。

### 3. instance 创建路径（改 `services/instance.go:131-153`）

```go
// 1. 收集所有挂载 subnet 的 selector
subnetSels := []*Selector{}
for _, ifc := range interfaces {
    sub := loadSubnet(ifc.SubnetID)
    if sub.NodeSelector != nil { subnetSels = append(subnetSels, sub.NodeSelector) }
}
// 2. 与 instance 自己的 selector 求交
finalSel := MergeSelectors(append(subnetSels, instance.NodeSelector)...)
// 3. 钉机时校验：指定 hyper 的 labels 必须满足 finalSel
if hyperID >= 0 && !finalSel.Matches(getHyperLabels(hyperID)) {
    return ErrHypervisorNotMatchSelector
}
// 4. 拿候选集
hyperGroup, err := GetHyperGroupBySelector(ctx, finalSel, -1)
```

### 4. subnet 创建路径

- `POST /subnets` 接受 `node_selector` 字段，落库
- **新增校验**：subnet 创建时如果 selector 命中 0 台 hyper，**警告但不阻止**（允许提前规划）
- IP 分配本身不变（subnet → IP 池），但 IP 实际下发到 OVS 只发生在 selector 命中的 hyper 上

### 5. FIP 创建路径（`api/src/services/floatingip.go` Create）

```go
// 入参新增（必填）：pod, public_vlan
// 后端按 (pod, public_vlan) 匹配 public 池 subnet：
sub, err := findFIPPoolSubnet(ctx, req.Pod, req.PublicVlan)
// 找不到 → ErrNoMatchingFIPPool（前端引导运维先建对应 subnet）
// 从 sub 分配一个 IP，FIP 记录 SubnetID=sub.ID（pod / public-vlan 不冗余存，按需 join）
```

前端 FIP 创建表单：先选 `pod`（下拉来自 hyper.pod 去重），再级联出该 pod 下可用的 `public-vlan` 列表（来自该 pod 内 hyper 的 `vlan-public` label 去重）。

### 6. FIP 绑定路径（`api/src/services/floatingip.go:389` AttachToInstance）

```go
// 现有代码：control := fmt.Sprintf("inter=%d", instance.Hyper) — 直接在 VM hyper 上配 NAT

pubSubnet := floatingIp.Interface.Address.Subnet  // FIP 所属 public 池 subnet

// 校验 1：VPC 层 — VM 所在 VPC 必须挂同 (pod, public-vlan) 的 public subnet
if !vpcHasMatchingPublicSubnet(ctx, instance.RouterID, pubSubnet) {
    return NewCLError(ErrFIPVpcMismatch,
        "VM's VPC does not include a public subnet matching the FIP's pod/public-vlan", nil)
}

// 校验 2：Hyper 层 — VM 当前 hyper 必须满足 FIP subnet selector
if pubSubnet.NodeSelector != nil {
    hyperLabels := getHyperLabels(instance.Hyper)
    if !pubSubnet.NodeSelector.Matches(hyperLabels) {
        return NewCLError(ErrFIPHyperMismatch,
            fmt.Sprintf("VM is on hyper %d which doesn't match FIP's (pod=%s, vlan-public=%s); migrate first",
                instance.Hyper, getPodFromSelector(pubSubnet.NodeSelector), getVlanFromSelector(pubSubnet.NodeSelector)), nil)
    }
}
```

### 7. VM 迁移路径

迁移时目标 hyper 候选集 = `instance selector ∩ 所有已挂载 subnet selector ∩ 已绑 FIP 所属 public subnet selector`。
任何一个为空 → 拒绝迁移并给出原因（哪个 subnet 把候选集干空了）。

---

## 三、Subnet selector 的语义难点

现在 subnet/vlan 是怎么和 hyper 绑定的？看 OVS 桥配置——**vlan trunk 实际上是物理网线决定的**，不是软件选的。所以 subnet selector 的真实语义应该是：

> "这个 subnet 对应的 vlan 在哪些 hyper 上**确实通了**，请用 selector 描述这件事"

例子：

- vlan-100 只在 dc-pod-a 的交换机上配置了，subnet selector 写 `{dc-pod: pod-a}`
- GPU 直通网络只在 GPU 机器上有，subnet selector 写 `{gpu: Exists}`

**所以 subnet selector 不是"我希望"，而是"物理上能"**。运维给 hyper 打 label 时必须如实反映物理拓扑。

**隐患**：label 错配会导致调度成功但网络不通。建议加一个**事后验证机制**：VM 起来后探活 vlan，失败就标记该 hyper 的 label 可疑。

---

## 四、实施分期

| 阶段 | 内容 | 验收 |
|---|---|---|
| **P1 数据层** | 加 `hyper_labels` 表、instance/subnet 的 `node_selector` JSON 列；写 migration；删 `zone_id` | DB schema 落地，旧 zone 数据迁成 label |
| **P2 selector 库** | 写 `Selector` 类型 + `Matches` + `MergeSelectors` + 单元测试 | 单测覆盖 4 种 operator 和求交场景 |
| **P3 调度替换** | 改 `GetHyperGroup` → `GetHyperGroupBySelector`；instance 创建路径接入 | 用 label 跑通一次 VM 创建 |
| **P4 hyper label CRUD** | API：`PUT /hypers/{id}/labels`、`DELETE /hypers/{id}/labels/{key}` | 管理员能在线打标 |
| **P5 subnet selector** | subnet 创建/更新接受 selector；instance 创建做求交 | 验证 private-vlan 隔离场景 |
| **P6 FIP 创建** | FIP Create 接口接受 `pod + public_vlan`；按这两个 key 找池 subnet 分配 IP | 可创建指定 (pod, vlan) 的 FIP |
| **P7 FIP 绑定 + 迁移校验** | AttachToInstance 加 VPC 层 + Hyper 层双校验；迁移路径接入 | pod-a/vlan-200 FIP 不能绑到 pod-b 或 vlan 不匹配的 VM |
| **P8 前端** | hyper 详情页 label 编辑器；instance/subnet 创建表单加 selector picker；FIP 创建表单 pod+vlan 级联下拉；绑定时按 selector 过滤候选 VM/FIP | UI 可用 |
| **P9（可选）** | label 错配探活、selector 命中数预览、调度模拟器 | - |

---

## 五、已决策项

1. **label key 命名规范**：用**简单 key**，不强制 k8s 的 `domain/key` 形式。允许 `.` 和 `/` 字符（如 `cpu.arch`）以便表达层级，但不要求。

2. **默认 selector 语义**：用户不填 selector → **匹配所有 hyper**（不引入 `default=true` 这种隐式标签，保持显式优先）。

3. **selector 求交失败语义**：求交为空时**创建时即报错**（强一致），不允许"创建成功但永远调度不出去"的 k8s 行为。错误信息要指出**哪个 subnet 把候选集干空了**，便于用户排查。

4. **SCI 协议**：**完全不动**。Go 层把 selector 评估结果拼成 `group-sel-{hash}:host1,host2,...` 字符串传过去，SCI 只解析冒号后的 hostid 候选集，前缀对它无语义。

---

## 涉及的关键文件

| 文件 | 改动类型 |
|---|---|
| `api/src/model/hyper.go` | 删 `ZoneID` 字段，加 `Labels` 关联 |
| `api/src/model/zone.go` | 删除整个文件 |
| `api/src/model/instance.go` | 加 `NodeSelector` JSONB 字段 |
| `api/src/model/subnet.go` | 加 `NodeSelector` JSONB 字段 |
| `api/src/model/hyper_label.go` | **新增** |
| `api/src/common/selector.go` | **新增** |
| `api/src/common/instance.go:32-56` | 替换 `GetHyperGroup` |
| `api/src/services/instance.go:131-153` | 接入 selector 求交 |
| `api/src/services/instance.go` (Migrate) | 迁移路径加 selector 校验（含已绑 FIP 的 public subnet） |
| `api/src/services/subnet.go` | 接受 selector 字段 |
| `api/src/services/floatingip.go:389` | AttachToInstance 加 public subnet selector 校验 |
| `api/src/services/hyper.go` | label CRUD |
| `api/src/routes/hyper.go` | 加 label 管理路由 |
| `web/src/views/dashboard/InstanceCreate.vue` | selector picker |
| `web/src/views/dashboard/SubnetCreate.vue` | selector picker |
| `web/src/views/dashboard/HyperList.vue` | label 编辑器 |
| `web/src/views/dashboard/FloatingIp*.vue` | 绑定 VM 时按 selector 过滤可选 FIP |
