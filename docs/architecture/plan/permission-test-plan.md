# 权限与 Org 关系系统测试计划及测试用例

## 1. 测试综述
根据《权限与 Org 关系系统重构计划》，本次测试核心在于验证新设计的双层角色体系（`SystemRole` 和 `OrgRole`）、全新的 User-Org 关系流程（含中间件交互）、新的权限验证机制（参数化 SQL 鉴权及方法控制）、以及各种用户状态（Active / Dormant / Disabled）的流转正确性。

### 1.1 测试目标
* **功能验证**：确保中间件集成场景（注册、邀请）正确流转，API 表现符合预期。
* **权限安全**：验证跨 Org 隔离、不同角色的越权拦截，防止水平/垂直越权。
* **状态机流转**：校验用户 Dormant、Disabled 状态与 Org 增删改查的关系。
* **系统健壮性**：涵盖并发操作（如并发转让 Owner 与移除成员）、资源限制（有资源的 Org 不可解散）等边界条件。

---

## 2. 测试环境准备
* **前置条件**：
  * 后端数据库执行完迁移 SQL（或通过 GORM AutoMigrate 完成结构更迭）。
  * 调用一次 `adminInit()` 以确保系统初始化完毕（内置 admin 用户和 admin org，以及相应的 Member 记录）。
* **测试账号池（规划）**：
  * **SystemAdmin (SA)**: 系统管理员 (`admin@cloudland.local` 及自己创建的其它管理员账号)
  * **Alice**: 测试主流程用户，主要作为 Org Owner
  * **Bob**: 测试被邀请者，尝试扮演不同角色 (OrgReader / OrgWriter / OrgAdmin)
  * **Carol**: 测试状态流转的第三方角色

---

## 3. 测试用例集

### 3.1 场景集成测试（注册与邀请）

| 用例编号 | 场景描述 | 执行步骤 | 预期结果 |
| :-- | :--- | :--- | :--- |
| **TC-INT-01** | **新用户注册创建 Org** | 1. 中间件调用 `GET /validate?email=alice@test.com` <br>2. 收到不存在后，调用 `POST /users/with-org` (包含 email/password/org_name) | 1. `Validate` 接口返回 `{exists: false}` <br>2. `alice` 被创建，状态为 `Active` <br>3. 自动创建一个类型为 `TeamOrg` 的 Org，`Slug` 格式正确且唯一 <br>4. 自动创建一条 `alice` 作为该 Org `OrgAdmin` 的 Member 记录，具有 Owner 身份 |
| **TC-INT-02** | **邀请已有用户加入 Org** | 1. 验证 `bob@test.com` 邮箱存在 <br>2. `alice`（作为 Owner/Admin）调用 `POST /orgs/{alice_org_id}/members` 邀请 `bob` 成为 `OrgReader` | 1. 成功向目标 Org 加入 `bob` 的 Member 记录，角色为 `OrgReader` <br>2. 若 `bob` 此前无 Org（状态为 `Dormant`），则加入后状态自动恢复为 `Active` |
| **TC-INT-03** | **邀请新用户加入现 Org** | 1. 验证 `carol@test.com` 不存在 <br>2. 中间件代调用 `POST /users` 创建普通用户 <br>3. 调用 `POST /orgs/{alice_org_id}/members` 加入 | 1. `POST /users` 创建出 `carol` 状态为 `Dormant` <br>2. `AddMember` 执行后 `carol` 加入 Org，状态自动变为 `Active` |
| **TC-INT-04** | **枚举邮箱防御检查** | 1. 短时间内频繁调用 `GET /validate?email=...` | 1. API 应触发速率限制或拦截，防止恶意穷举 |

### 3.2 用户与身份状态 (Status) 管理

| 用例编号 | 场景描述 | 执行步骤 | 预期结果 |
| :-- | :--- | :--- | :--- |
| **TC-USR-01** | **Dormant 用户登录验证** | 1. `bob` 被移出所有 Org，进入 `Dormant` 状态 <br>2. `bob` 尝试登录 (`AccessToken`) <br>3. `bob` 尝试调用云资源 API (如读写 Instance) | 1. 登录成功，JWT 中返回状态 `ST=Dormant` <br>2. 调用云资源接口时一致返回 `403 ErrNoOrgMembership` |
| **TC-USR-02** | **SystemAdmin 禁用启用** | 1. SA 禁用 `alice` <br>2. `alice` 尝试登录 <br>3. SA 重新启用 `alice` | 1. 禁用后，`alice` 登录直接返回 `403 ErrUserDisabled`，其 Token 立刻失效（或由网关拦截） <br>2. 重新启用后，检查其关联 Member <br> - 若有 Member，状态为 `Active`<br> - 若无 Member，状态为 `Dormant` |
| **TC-USR-03** | **最后一名 SA 保护** | 1. 系统中只有一个 SA <br>2. SA 尝试调用 `DemoteSystemAdmin` 降级自己 <br>3. SA 尝试被删除 | 1. 返回 `400 ErrCannotSelfDemote` <br>2. 返回 `400 ErrLastSystemAdmin` |

### 3.3 Org 操作与角色权限验证 (OrgRole & SystemRole)

| 用例编号 | 场景描述 | 执行步骤 | 预期结果 |
| :-- | :--- | :--- | :--- |
| **TC-ORG-01** | **Slug 生成与冲突机制** | 1. 创建两个不传 Slug 的 Org <br>2. 传非法或保留字 Slug 创建 Org (如 `admin`, `@#&!` 等) <br>3. 创建与现有 Slug 同名的 Org | 1. 系统自动兜底生成 `org-{id}` 格式的 Slug <br>2. 返回 `ErrSlugReserved` / `ErrSlugInvalid` <br>3. 返回 `409 ErrSlugConflict` |
| **TC-ORG-02** | **跨级身份越权修改防御** | 1. `bob`（作为 `OrgWriter`）尝试提升另一成员为 `OrgAdmin` <br>2. `alice`（Owner）尝试解散本 Org | 1. `bob` 操作失败（无法设置高于自身的角色） <br>2. `alice` 返回 `403`，只有 `SystemAdmin` 能够解散 Org |
| **TC-ORG-03** | **Owner 转让机制** | 1. SA 调用 `TransferOwner` 将 Owner 从 `alice` 转让给 `bob`（当前为 `OrgReader`） <br>2. 后续查询有效权限 | 1. 必须转让给已存在的 Member，成功后 `bob` `IsOrgOwner=true` <br>2. `bob` `EffectiveOrgRole()` 为 `OrgAdmin`（可发起邀请）。`alice` 降回源有存储的 `OrgRole`。 |
| **TC-ORG-04** | **所有者保护与防自杀** | 1. `alice` 尝试将其本人移出 Org <br>2. 另外一名 `OrgAdmin` 尝试将会长 `alice` 移出 Org | 1. 返回 `400 ErrCannotRemoveOwner` <br>2. 返回 `400 ErrCannotRemoveOwner` (必须先由 SA 转让 Owner) |
| **TC-ORG-05** | **拥有资源的 Org 解散** | 1. OrgA 创建有 Instance (实例) <br>2. SA 尝试解散 OrgA <br>3. 清空 Instance 后再次解散 | 1. 返回 `409 ErrOrgHasResources`，解散失败 <br>2. 成功软删 Org，并级联软删该 Org 的所有 Members 记录 |

### 3.4 鉴权调用点替换验证 (Resource Middleware)

| 用例编号 | 场景描述 | 执行步骤 | 预期结果 |
| :-- | :--- | :--- | :--- |
| **TC-RES-01** | **资源参数化隔离查询** | 1. SA 调用 `GetWhere()` 列出实例列表 <br>2. `alice` (普通 OrgOwner) 调用查列表 | 1. SA 返回 `("", nil)`，获取所有实例 <br>2. `alice` 仅返回 `owner=?` 为本 OrgID 的结果，无法查询跨 Org 数据 |
| **TC-RES-02** | **角色资源写权限校验** | 1. `bob` (OrgReader) 尝试创建 Volume 子网等资源 <br>2. 同一接口下 `carol` (OrgWriter) 的操作 | 1. `CheckOrgPermission(model.OrgWriter)` 拦截，返回 `403` 权限不足 <br>2. `carol` 成功创建，Owner 记录指向该 OrgID |
| **TC-RES-03** | **SA 显式切换资源上下文** | 1. SA 携带 Header `X-Resource-Org: {orgID}` 尝试创建云资源资源 | 1. 后台确认能创建在指定的 `orgID` 下，非自身上下文 <br>2. 如果 SA 不带该 Context 创建具体资源，应提示/报错 `ErrMissingOrgContext` |

### 3.5 Token 切换与会话

| 用例编号 | 场景描述 | 执行步骤 | 预期结果 |
| :-- | :--- | :--- | :--- |
| **TC-TKN-01** | **SwitchOrg 接口验证** | 1. `alice` 分别属于 OrgA (Owner) 和 OrgB (Writer) <br>2. 登录默认为 OrgA <br>3. 调用 `POST /switch-org?org_id=OrgB` | 1. 取回的新 JWT Payload 中 `OID` 变为 OrgB，`OR` 变为 `OrgWriter` 的枚举值 <br>2. 原有 Token 不影响（或视注销策略而定，优先测试新 Token 是否完全代表新环境角色） |
| **TC-TKN-02** | **重置密码机制** | 1. 任意登录用户用有效 Token 调用 `ChangePassword` <br>2. 填错旧密码进行验证 | 1. 密码更新成功 <br>2. 返回 `ErrPasswordMismatch` |

---

## 4. 并发与并发竞态测试

| 用例编号 | 测试场景 | 执行说明与测试结果 |
| :-- | :--- | :--- |
| **TC-CON-01** | **并发的 Owner 转让与移除** | 模拟 SystemAdmin 在调用 `TransferOwner`（指定新 Owner 为 `bob`）的同时，OrgAdmin 在并发调用 `RemoveMember(bob)`。 <br> **预期：** 使用了 `SELECT ... FOR UPDATE` 行级锁，系统只能成功一个，不会出现 Org 的 Owner 是一个已经被软删除的非成员状态。 |

---

## 5. 数据完整性与 SQL 校验 (自动化 / 后台验证)

测试期间，通过直连 DB 执行以下检验保证一致性：

1. **唯一性检查**：检查 `users.email` 是否唯一 (忽略空和软删除) 。
2. **唯一性检查**：检查 `organizations.slug` 是否在未被软删的维度全局唯一。
3. **复合唯一键检查**：检查 `members(user_id, org_id)` 是否在 `deleted_at IS NULL` 的前提下唯一。
4. **有效 Owner 校验**：
```sql
-- 验证不存在“僵尸Owner” (即 Owner 设置成了不是本 Org 成员的 User)
SELECT o.name FROM organizations o
WHERE NOT EXISTS (
    SELECT 1 FROM members m
    WHERE m.org_id=o.id AND m.user_id=o.owner_user_id AND m.deleted_at IS NULL
) AND o.deleted_at IS NULL AND o.org_type != 2;
```
5. **Dormant 计算校验**：
```sql
-- 检查是否存在应为 Dormant 但状态错误的 User
SELECT u.email FROM users u
WHERE u.status = 1
  AND u.system_role = 0
  AND u.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM members m WHERE m.user_id=u.id AND m.deleted_at IS NULL
  );
```
6. **资源与 Org 挂钩检验**：
新创建的实例等资源，其 `owner` 字段必须等同于 JWT / Header 传来的合法 `OrgID`，绝不应该有 `owner=0` 的漂浮业务数据（非公用或 System 资源除外）。

---
**完档审核清单：**
- [ ] 中间件交互接口的速率控制与黑客探测抵抗度是否完善？ 
- [ ] GORM 的软删除是否真正在每个查询场景（尤其是 Owner 判定）中发挥了物理隔离效果？
- [ ] 修改了涉及 ~17 个子资源的授权点，每个端点的 `GET/POST/PUT/DELETE` 行为都能按照 `GetOrgFilter`/`CheckResourceOrg` 符合预期？
