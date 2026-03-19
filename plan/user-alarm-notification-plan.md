# 用户告警规则配置与通知推送方案

## 一、目标

让普通用户能在前端：
1. 创建/管理针对虚拟机的告警规则（CPU、内存、带宽）
2. 配置告警通知方式（邮件、飞书 Webhook、自定义 Webhook）
3. 告警触发时，由区域所在的后端自动推送通知到用户

## 二、现状分析

### 已有能力
| 组件 | 状态 |
|------|------|
| clapi：CPU/内存/带宽告警规则 CRUD | ✅ 已完成 |
| clapi：规则绑定/解绑 VM | ✅ 已完成 |
| clapi：启用/禁用规则 | ✅ 已完成 |
| Prometheus 规则模板渲染 + 热加载 | ✅ 已完成 |
| AlertManager → Webhook 回调 | ✅ 已完成 |

### 缺失能力
| 组件 | 状态 |
|------|------|
| 前端：告警规则管理页面 | ❌ 缺失 |
| 前端：通知渠道配置页面 | ❌ 缺失 |
| 前端：告警历史/当前告警展示 | ❌ 缺失 |
| CPGateway 通知渠道管理（API + 库表） | ❌ 缺失 |
| CPGateway 推送配置到 clapi 的下发机制 | ❌ 缺失 |
| clapi 保存通知渠道及其与规则的绑定关系 | ❌ 缺失 |
| clapi 接收并本地发起告警通知推送（邮件/飞书/Webhook）| ❌ 缺失 |
| clapi 本地持久化告警历史记录与发送流水 | ❌ 缺失 |

## 三、整体架构

### 核心设计原则

**区域闭环与单向依赖：**
- **全局配置中心（CPGateway）**：负责维护跨 Region 的、属于用户个人的“通知渠道”模型。
- **配置单向分发（Push）**：CPGateway 主动将用户的通知渠道变化“下推分发”给各个 Region 的 clapi（clapi 绝不去主动调用 CPGateway）。
- **区域数据闭环（clapi）**：告警规则、渠道配置副本、告警绑定关系全部在本地 Region 的 clapi 存储并验证事务。告警一旦触发，由本地 clapi 直接查库发送，绝不跨区依赖总部节点网络，提高系统容灾和时效性。
- **避免分布式事务问题**：使用反向代理。用户操作由前端指向 CPGateway，CPGateway 做权限验证和路由，然后代理转发给区域 clapi 处理完整的本地 DB 事务。

### 架构图

```text
用户前端
  ├── 通知渠道管理（全局维度）
  │     → CPGateway 入库保存 → 如果有变更，扇出同步给各 Region clapi
  │
  ├── 告警规则聚合操作（切 Region 操作）
  │     → CPGateway 拦截并补充验证身份 → 代理透传 → Region clapi 落本地数据库 (同时包含规则+渠道绑定)
  │
  └── 告警历史记录与发送流水的查看
        → CPGateway 根据 Region 选择代理透传查询该特定 Region clapi

告警触发运转链路（Region 内闭环处理）：
  [区域内部] Prometheus -> 触发 -> AlertManager 
                 └─ 回调 Webhook ─> Region clapi (`ProcessAlertWebhook` 接口)
                                           ├── 1. 查找本地区域同步来的 Rule_Group 关联的 notification_channels
                                           ├── 2. 写入/更新本地 `alarm_events` 告警主状态表
                                           ├── 3. 执行本地 Golang 常驻服务发送 Email / 飞书 / Webhook
                                           └── 4. 向本地 `alarm_delivery_logs` 流水表中写入发送结果
```

## 四、详细设计

### Phase 1：数据模型规划

#### 1.1 CPGateway 全局模型（用户中心侧）

文件：`cpgateway/app/models/notification.py`

```python
# CPGateway 核心只维护用户所创建的各种全球性发送方式配置
class NotificationChannel(Base):
    __tablename__ = "notification_channels"
    id = Column(BigInteger, primary_key=True)
    uuid = Column(String(36), unique=True, index=True)
    user_id = Column(BigInteger, index=True, nullable=False)
    name = Column(String(128), nullable=False)
    type = Column(String(32), nullable=False) # 取值: enum(email / feishu / webhook)
    config = Column(JSONB, nullable=False)
    enabled = Column(Boolean, default=True)
    # 注意: 不存储具体的 binding 关系。Binding 关系依赖所在区存在的规则，交由下层 clapi 管理，避免跨域级联删除问题。
```

#### 1.2 clapi 区域模型（配置副本集与历史全记录）

在各个 Region 的 clapi `api/src/models/` 中补充相应表结构：

```go
// 1. 通知渠道镜像表（CPGateway 单向下发的数据，不可修改只能查询使用）
type NotificationChannel struct {
    ID      uint   `gorm:"primaryKey"`
    UUID    string `gorm:"uniqueIndex"`
    UserID  uint   `gorm:"index"`
    Name    string
    Type    string // email, feishu, webhook
    Config  string // JSON 配置详情 
    Enabled bool
}

// 2. 告警规则与渠道绑定关系表（在 clapi 本端处理，强一致关联）
type AlarmNotificationBinding struct {
    ID            uint   `gorm:"primaryKey"`
    RuleGroupUUID string `gorm:"index"`
    ChannelUUID   string `gorm:"index"`
    UserID        uint   // 安全防御字段，防止越权绑定。
}

// 3. 本地告警事件记录表（不再依赖中心化数据上报，本地闭环）
type AlarmEvent struct {
    ID            uint   `gorm:"primaryKey"`
    UUID          string `gorm:"uniqueIndex"`
    RuleGroupUUID string `gorm:"index"`
    AlertName     string
    Owner         string `gorm:"index"`
    VMUUID        string `gorm:"index"`
    VMName        string
    Severity      string
    Status        string // firing, resolved
    Summary       string
    Labels        string // 标签集
    FiredAt       time.Time 
    ResolvedAt    *time.Time
}

// 4. 通知发送业务日志（彻底解决状态履历混乱且相互踩踏覆盖的 bug）
type AlarmDeliveryLog struct {
    ID           uint   `gorm:"primaryKey"`
    EventUUID    string `gorm:"index"`
    ChannelUUID  string
    NotifyType   string // firing_trigger(首发触发), repeat_remind(周期重复提醒), resolved(恢复发信)
    Status       string // sent(成功), failed(失败)
    ErrorMessage string
    SentAt       time.Time
}
```

### Phase 2：CPGateway 业务流向接口改造

#### 2.1 全局通知渠道操作响应（前端与 CPGateway）

用户在前端对通知渠道进行增修删：
以 RESTful 为基准 `GET/POST/PUT/DELETE /api/v1/notification-channels` 。
这是唯一一处 CPGateway 会发生**写库**行为的主动作链路。

#### 2.2 配置下发与全量同步机制（CPGateway <-> Region clapi）

为了保证配置的最终一致性并支持“新 Region 上线”或“clapi 重启/断网恢复”，同步机制分为**增量扇出**与**全量拉取**两部分：

**A. 变更时的增量扇出 (Push by CPGateway)**
此步彻底实现"配置实时发送给每个 region"。如果发生上述 2.1 修改行为，CPGateway 本地保存结束后会向**系统内各有效 Region** 的 clapi 异步下发镜像同步指令：

`POST {clapi_internal_url}/api/v1/internal/notification-channels/sync`

Payload 示例：
```json
{
  "action": "upsert", // 或是 delete
  "channel": {
    "uuid": "xxxxx",
    "user_id": 123,
    "name": "My Workspace Group",
    "type": "feishu",
    "config": {"webhook_url": "xxxx"},
    "enabled": true
  }
}
```

**B. 启动时的主动全量拉取 (Pull by clapi)**
如果是**全新的 Region 首次上线**，或者某个 Region 的 `clapi` 发生过异常重启，它可能会漏掉增量 Push 消息。因此，clapi 在每次启动初始化时（或基于兜底的 CronJob 定时），主动要求向 CPGateway 拉取一次全量通知渠道列表数据：

`GET {cpgateway_internal_url}/api/v1/internal/notification-channels/export`

clapi 收到包含系统中所有 Channel 的全量 JSON 数组后，利用类似于 `UPSERT (ON CONFLICT DO UPDATE)` 的 SQL 语法，补齐并刷新本地的 `NotificationChannel` 镜像表，并清理已被上游删除的无用数据。

**安全保证**：不论是 Push 还是 Pull 接口，双方均使用基于中心化安全密钥签发出的内部 `X-Auth-Internal` Http header。

### Phase 3：clapi 的本地功能延伸

#### 3.1 代理拦截与联合本地事务写入
这是解决原版“分布式孤岛规则漏洞”及“篡改 UUID 验证越权漏洞（IDOR）”的核心环节。

前端将原本的提交接口升级——如提交告警创建申请至 CPGateway 时：
```json
POST /api/v1/metrics/alarm/cpu/rules
{
    ...规则原有配置...,
    "channel_uuids": ["c-uuid-1", "c-uuid-2"]
}
```

**CPGateway**：做透传代理。将请求传递给选中的目标 Region clapi。
**clapi** 接收并启动事务：
1. JWT 中获得当前 `UserID`。如果传来 `channel_uuids`，则通过内部记录的 `NotificationChannel` 表比对归属权：只有对应拥有人是该 UserID，才授权继续。
2. 基础 Rule 信息入库。
3. `channel_uuids` 批量存入 `AlarmNotificationBinding` 关联表。
4. 热刷 Prometheus 配置文件。
5. 所有步骤执行成功后事务 Commit，失败全体 Rollback，保证不留残余数据。同样，在收到 DELETE 请求时，在此事务闭环内也清理冗余 Binding。

#### 3.2 接收 Alertmanager 轮训与消除暴力丢弃告警状态
本地 `api/src/apis/alarms.go` 重点升级回调能力。

- 查询该条报警通过 ID，寻找最近的一条 `AlarmEvent` 。如果 AlertManager 发来一笔状态依然是 `firing` 的重灾包。
- 取消原本 “已有 firing 就 Drop 的判定”，引入尊重 `AlertManager` 的周期性重发判定。更新其最近的时间戳并将之定义为 "重复提醒动作(Remind)"。
- 根据 `rule_group_uuid` 查询提取绑定的所有的通道。
- 使用 Golang 发起异步发送线程调用 `AlarmNotifier` 工具。

#### 3.3 本地化通知发送服务 (Golang `AlarmNotifier`)
无需跨国或跨专线网络回传总部；如果 Region 自身对网络可通任意服务（外网飞书或本地邮件服务器），它会自己推送：
- 邮件支持使用开源库如 `gopkg.in/gomail.v2` 组织带样式的网页版告警通知。需要在各 Region `clapi` 补充 SMTP 系统环境变量配置。
- 飞书通过构建 `POST` 携带 `HMAC256` 签名的请求将结构体发出。
- 这些流程在每次尝试发出后，无论发生 Network Timeout 等何种失败原因，都作为一次独立行为在 `AlarmDeliveryLog` 加一条 Log 流水，留存前端回溯证据链。

### Phase 4：面向前端重构呈现逻辑 
在原来设计里 UI 原地不动，逻辑更改如下：
- **历史记录列表**与**当前报警明细**：前端在请求时必须依赖上方当前切换的 `Region_ID` 来发起。CPGateway 仅仅根据此信息将查询向该地 `clapi` 代理过去，由 `clapi` 取出自己本地记录的表单集合给客户返回即可。

## 五、相比之前版本的核心方案优势与价值

1. **绝对的高可用容错与业务连通性保障级别**
   边缘侧故障告警发送链路不会因为“断开到管控中枢节点”而全局沉默崩溃，各机房区域只要与用户本地环境通信正常（甚至机房本地服务自己直接连飞书开放平台），依然可以及时触达维保预警！
2. **根除因为多次写入动作造成的时序数据撕裂**
   没有了中心与边缘同步写入不同字段状态的情况。不仅规则、渠道的强行绑闭环于 clapi SQLite 事务内保护孤儿数据，还增设了纯净只读操作的 Log 历史链避免了数据相互覆盖污染问题，实现发送溯源。
3. **安全底线夯实防入侵**
   绑定前强制检验本地从总部收到的验证信标，斩断通过越权 UUID 强行修改他人物理机器监控回调的通路风险。
