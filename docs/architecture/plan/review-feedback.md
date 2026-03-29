# User Alarm Notification Plan Review

总体来看，该方案逻辑清晰，模块职责划分合理（CPGateway 负责全局配置和通知分发，clapi 负责区域级底层规则）。但在**数据一致性、权限控制、告警可靠性**方面存在一些设计漏洞和不合理之处：

### 1. 权限越权漏洞（IDOR）
- **问题所在（Phase 2.2）**：CPGateway 提供 `/api/v1/alarm-rules/{rule_uuid}/notifications` 进行渠道绑定/解绑。但由于 CPGateway 本地不保存告警规则数据，它**无法验证 `rule_uuid` 是否真的属于当前发起请求的用户**。
- **风险**：恶意用户可以通过猜测别人的 `rule_uuid`，将自己的 Webhook 绑定上去（窃听别人虚拟机的告警信息），或者恶意解绑别人的通知渠道（导致别人收不到告警）。
- **优化建议**：CPGateway 在处理绑定/解绑操作时，必须先带上用户的身份信息向目标 Region 的 clapi 发起一次查询（如 `GET /metrics/alarm/.../rule/{uuid}`），验证该规则的 `owner` 是否匹配当前用户，确认拥有权限后再写入本地 `alarm_notification_bindings` 表。

### 2. 分布式数据一致性与“孤儿”数据
- **问题所在（Phase 6.3 交互描述 & Phase 5 代理）**：
  前端创建规则的流程被设计为分两步：1) POST 规则到 clapi，2) POST 绑定关系到 CPGateway本地。如果第一步成功但第二步网络超时，用户将拥有一个永远不会发送通知的孤儿规则。
  同样，当用户删除已有的告警规则时，只提到了代理删除 `/api/v1/metrics/alarm/.../rule/{uuid}`，并未提及清理 CPGateway 的绑定表。
- **优化建议**：
  - **创建**：利用 CPGateway 作为代理的优势。前端发给 CPGateway 的创建规则 Payload 中可以一次性包含 `channel_uuids: []`。CPGateway 拦截该请求，先把规则部分代理给 clapi，当 clapi 成功返回新创建的 `rule_uuid` 后，CPGateway **自动在本地事务中插入** `alarm_notification_bindings`，最后再返回给前端。避免前端协调分布式创建。
  - **删除**：CPGateway 在代理 `DELETE` 告警规则接口并收到 clapi 的成功响应后，应附加执行一条 SQL 清理本地对应 `rule_group_uuid` 的相关 bindings，防止产生僵尸数据。

### 3. 告警丢失与重试机制被打破 (Critical)
- **问题所在 1（Phase 4.1 异步转发）**：
  `clapi` 收到 AlertManager webhook 后，直接开一个 goroutine `go a.forwardAlertToCPGateway` 进行异步转发，然后立刻对 AlertManager 响应 `200 OK`。如果 CPGateway 此时重启或网络不通，因为是异步发出，发送失败就会直接丢弃，该告警将**永久丢失**。
- **问题所在 2（Phase 3.2 暴力去重）**：
  `if existing and event_data["status"] == "firing": return existing`。AlertManager 自身有 `repeat_interval`（例如每 x 小时后如果还没恢复则重复提醒）机制。CPGateway 暴力把后续所有 `firing` 状态拦截掉，会导致原本用于持续提醒的告警被吞掉，变成了故障无论持续多久都只有 1 次通知。
- **优化建议**：
  1. `ProcessAlertWebhook` 的转发不要用异步 goroutine 且抛弃结果。应**同步请求** CPGateway（可设合理超时），如果 CPGateway 返回错误或超时，clapi 应当向 AlertManager 返回 `5xx` 状态码。这样可以直接利用 AlertManager 的重试队列机制保障告警不丢失。
  2. CPGateway 接到后续相同 `firing` 告警时，**不应该直接跳过**发通知阶段，而应该当做一次“重复提醒 / reminders”处理，继续走 Notification Channel 发送（可更新 `event.updated_at`），尊重 AlertManager 发送该事件的初衷。

### 4. 告警事件状态和通知记录被覆盖（Phase 3.2 & 1.3）
- **问题所在**：按 Phase 3.2 的代码，当收到 `resolved` 恢复告警时，是对旧的 `firing` 事件记录进行覆盖修改（更改 status、resolved_at、notify_status）。如果发送“恢复通知”时网络报错导致发送失败，这条记录的 `notify_status` 会被更新为 `failed`。这不仅覆盖了它当初抛出 `firing` 时可能存在的 `sent` 的状态，还会导致用户在排查历史时产生困惑：“这告警当初到底有没有成功推给我？”
- **优化建议**：
  最好将**“告警生命周期实体”**（AlarmEvent）与**“通知投递流水记录”**（NotificationDeliveryLog 表）拆分。每次发送通知（无论是触发、重复提醒还是恢复），都单独往流水记录表中记一笔，这样历史行为可清晰追溯。如果必须混在同一张表里，至少应把字段拆为 `firing_notify_status` 和 `resolved_notify_status`。

### 5. 组网与安全（Phase 4.2）
- **问题所在**：`cpgateway.internal_url = "http://cpgateway:8000"`, 并使用静态 Header Header `X-Internal-Secret` 交互。
- **提醒**：CPGateway 是全局中心，clapi 位于各个边缘 Region。如果这两者的通讯需要跨越公网，明文 HTTP + 静态 Secret 容易被中间人嗅探和重放攻击。如果环境是跨互联网组合的，建议强行要求 HTTPS 访问，或者限制源 IP 加上更短的 Token 机制（如让 clapi 用内部 RSA 签短效 JWT 发过去）。若都是内网专线 VPC，则现状可接受。
