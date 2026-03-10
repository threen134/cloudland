# 权限与 Org 关系系统重构计划

## Context

现有权限系统存在以下问题需要彻底重构（不需兼容旧版本）：
1. `Role` 枚举将系统级 Admin 与 Org 级角色混用，导致通过 `OrgName == "admin"` 这种 magic string 判断系统权限
2. `User.Owner` 字段语义模糊（主归属 Org），与 Member 多对多关系冲突
3. `Member` 表冗余字段 `UserName`/`OrgName`，用户/Org 改名后产生数据不一致
4. `GetWhere()` 返回裸拼接 SQL 字符串，有 SQL 注入风险
5. 无 Org 级管理员，所有成员管理必须依赖系统 Admin
6. `authorize.go` 中 `if realUser == "admin"` 魔法字符串判断系统管理员

**目标**：清晰的双层角色体系、参数化 SQL、Org 级自治能力。

---

## 用户与 Org 的关系

- **一个 User 可以属于多个 Org**，每个 Org 里角色独立
- **每个 Org 有且仅有一个 Owner**（存于 `organizations.owner_user_id`）
- **Owner 是可转让的**，转让给 Org 内的其他成员
- **普通 User 不需要有自己的 Org**，只作为 Member 加入别人的 Org
- **邮件验证由外部中间件负责**，后端 API 只在中间件完成邮件验证后才被调用，所有用户创建时 `Status=Active`

## 外部中间件职责边界

> 中间件（Middleware）负责所有与邮件相关的交互（注册验证、邀请链接、密码设置），后端 API 不感知邮件流程，只提供原子操作接口。

---

### 场景一：新用户注册开通租户

```
用户（浏览器）          Middleware                    后端 API                    数据库
     │                     │                              │                          │
     │  填写注册表单         │                              │                          │
     │  email/username/     │                              │                          │
     │  org_name            │                              │                          │
     │─────────────────────►│                              │                          │
     │                      │  GET /validate?email=xxx     │                          │
     │                      │─────────────────────────────►│── SELECT users ─────────►│
     │                      │◄─────────────────────────────│◄─────────────────────────│
     │                      │  {exists:false}（可用）       │                          │
     │                      │                              │                          │
     │  收到注册确认邮件      │  发送邮件（含一次性token）     │                          │
     │◄─────────────────────│                              │                          │
     │                      │                              │                          │
     │  点击确认链接         │                              │                          │
     │─────────────────────►│                              │                          │
     │  用户设置密码         │  验证 token 合法             │                          │
     │─────────────────────►│                              │                          │
     │                      │  POST /users/with-org        │                          │
     │                      │  {username,email,password,   │                          │
     │                      │   org_name}                  │                          │
     │                      │─────────────────────────────►│                          │
     │                      │                              │── INSERT users ──────────►│
     │                      │                              │── INSERT organizations ──►│
     │                      │                              │── INSERT members ────────►│
     │                      │◄─────────────────────────────│  (OrgAdmin, OwnerUserID) │
     │                      │  {user_id, org_id}           │                          │
     │  跳转到登录页         │                              │                          │
     │◄─────────────────────│                              │                          │
```

**结果：** User(Active) + Organization(TeamOrg, OwnerUserID=user.ID) + Member(OrgAdmin) 同时创建，用户可直接登录并管理自己的 Org。

---

### 场景二A：邀请已有用户加入 Org

```
邀请者（浏览器）        Middleware                    后端 API                    数据库
     │                     │                              │                          │
     │  填写邮箱 + OrgRole  │                              │                          │
     │─────────────────────►│                              │                          │
     │                      │  GET /validate?email=xxx     │                          │
     │                      │─────────────────────────────►│── SELECT users ─────────►│
     │                      │◄─────────────────────────────│◄─────────────────────────│
     │                      │  {exists:true, user_id:42}   │                          │
     │                      │                              │                          │
     │                      │  发送「加入 Org」邀请邮件     │                          │
     │                      │  （收件人：目标用户邮箱）      │                          │
     │                      │                              │                          │
     │        目标用户收到邮件并点击确认链接                 │                          │
     │◄ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─│                              │                          │
     │                      │  验证 token 合法             │                          │
     │                      │  POST /orgs/:orgID/members   │                          │
     │                      │  {user_id:42, org_role:1}    │                          │
     │                      │─────────────────────────────►│                          │
     │                      │                              │── INSERT members ────────►│
     │                      │◄─────────────────────────────│  (UserID=42, OrgRole)    │
     │                      │  201 Created                 │                          │
     │  通知：已加入 Org     │                              │                          │
     │◄─────────────────────│                              │                          │
```

**结果：** 目标用户无需重新注册，直接以 Member 身份加入 Org。若该用户原为 Dormant，自动恢复 Active。

---

### 场景二B：邀请新用户加入 Org（用户不存在）

```
邀请者（浏览器）        Middleware                    后端 API                    数据库
     │                     │                              │                          │
     │  填写邮箱 + OrgRole  │                              │                          │
     │─────────────────────►│                              │                          │
     │                      │  GET /validate?email=xxx     │                          │
     │                      │─────────────────────────────►│── SELECT users ─────────►│
     │                      │◄─────────────────────────────│◄─────────────────────────│
     │                      │  {exists:false}              │                          │
     │                      │                              │                          │
     │                      │  发送「注册并加入」邀请邮件   │                          │
     │                      │  （含 orgID + token）        │                          │
     │                      │                              │                          │
     │        新用户收到邮件，点击链接，跳转到密码设置页面   │                          │
     │◄ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─│                              │                          │
     │  填写用户名 + 密码    │                              │                          │
     │─────────────────────►│  验证 token 合法             │                          │
     │                      │                              │                          │
     │                      │  POST /users                 │                          │
     │                      │  {username,email,password}   │                          │
     │                      │─────────────────────────────►│                          │
     │                      │                              │── INSERT users ──────────►│
     │                      │◄─────────────────────────────│  (Status=Active)         │
     │                      │  {user_id: 99}               │                          │
     │                      │                              │                          │
     │                      │  POST /orgs/:orgID/members   │                          │
     │                      │  {user_id:99, org_role:1}    │                          │
     │                      │─────────────────────────────►│                          │
     │                      │                              │── INSERT members ────────►│
     │                      │◄─────────────────────────────│                          │
     │  跳转到登录页         │  201 Created                 │                          │
     │◄─────────────────────│                              │                          │
```

**结果：** 新用户被创建（无自己的 Org）并直接加入目标 Org 成为 Member。

---

### 场景三：用户状态流转总览

```
  Middleware 调用 POST /users 或 POST /users/with-org
                    │
                    ▼
               ┌─────────┐◄──────────────────────────────────┐
               │  Active │                                    │ 被加入新 Org（自动恢复）
               └────┬────┘                                    │
                    │                                         │
      ┌─────────────┼──────────────────┐                 ┌───┴─────┐
      │             │                  │      被移出所有 Org│ Dormant │
      │ SystemAdmin │                  │  ───────────────►└─────────┘
      │ 主动禁用    │                  │
      ▼             │                  │
 ┌──────────┐       │                  │
 │ Disabled │       │                  │
 └────┬─────┘       │                  │
      │             │                  │
      │ SystemAdmin 重新启用           │
      │ （检查 org 归属）              │
      │    ├─ 有 Org 成员 ────────────►┘ → Active
      │    └─ 无 Org 成员 ──────────────────────► Dormant
      │
      │ 说明：Disabled 期间用户无法登录、无任何操作；
      │       重启后状态由 org 归属决定，而非直接回到 Active
      │
      │ 不变量：SystemAdmin 的状态只能是 Active 或 Disabled，不会进入 Dormant
      │         （SystemAdmin 始终持有 admin org 的成员记录，不存在"无 Org"状态）
      ▼
```

> **"删除"不是状态**：用户被 SystemAdmin 删除后，GORM 软删除填充 `deleted_at` 字段，该记录对所有业务查询不可见。`status` 字段仅用于描述活跃用户的登录能力，不设 Deleted 枚举值，避免与软删时间戳语义重叠导致数据不一致。

**各状态说明：**

| 状态 | 可登录 | 触发条件 | 退出条件 |
|------|:------:|----------|----------|
| Active | 是 | Middleware 创建用户 / 被加入 Org | 被移出所有 Org / 被 Disabled |
| Dormant | 是（受限）| 被移出所有 Org（系统自动）/ Disabled 重启后无 Org | 被邀请加入 Org（自动恢复 Active） |
| Disabled | 否 | SystemAdmin 主动禁用 | SystemAdmin 重新启用（→ Active 或 Dormant） |

---

## 新角色体系设计

### SystemRole（存于 users.system_role 字段）
```
SystemUser  = 0  // 普通用户
SystemAdmin = 1  // 系统管理员：可创建/删除 Org、User，跨 Org 查看资源
```

### OrgRole（存于 members.org_role 字段）
```
OrgNone   = 0  // 无权限（不会实际存入）
OrgReader = 1  // 只读：查看本 Org 资源
OrgWriter = 2  // 读写：创建/修改/删除本 Org 资源
OrgAdmin  = 3  // 管理：可邀请/移除成员、修改成员角色（不能超越自身等级）
```

### Org Owner（存于 organizations.owner_user_id 字段）
- **独立于 OrgRole**，标识 Org 的名义归属人
- 每个 Org 全局唯一，通过字段约束保证
- 拥有 OrgAdmin 的全部能力，但 **不能解散 Org、不能转让 Owner**（均为 SystemAdmin 专属操作）

| 操作 | OrgReader | OrgWriter | OrgAdmin | Org Owner | SystemAdmin |
|------|:---------:|:---------:|:--------:|:---------:|:-----------:|
| 查看本 Org 资源 | Y | Y | Y | Y | Y (任意Org) |
| 创建/删除本 Org 资源 | N | Y | Y | Y | Y |
| 邀请本 Org 成员 | N | N | Y（本Org） | Y（本Org） | Y（任意Org） |
| 移除本 Org 成员 | N | N | Y（本Org） | Y（本Org） | Y（任意Org） |
| 修改成员角色 | N | N | Y（本Org） | Y（本Org） | Y（任意Org） |
| 转让 Owner | N | N | N | N | Y（任意Org） |
| 解散 Org（资源须清空） | N | N | N | N | Y（任意Org） |
| 创建 Org（需指定 Owner） | N | N | N | N | Y |
| 创建普通 User | N | N | N | N | Y |
| 创建 SystemAdmin User（直接入 admin org） | N | N | N | N | Y |
| 降级 SystemAdmin → SystemUser（需保留至少一个） | N | N | N | N | Y |
| 删除 User（不能删自己） | N | N | N | N | Y |
| 修改自己的密码（需验证旧密码） | Y | Y | Y | Y | Y |
| 修改自己的 Profile（名字/区域/语言）| Y | Y | Y | Y | Y |
| 修改任意用户的 Remark | N | N | N | N | Y |
| 切换当前 Org 上下文（SwitchOrg） | Y（本人成员的Org）| Y | Y | Y | Y（任意Org）|
| 更改 Org 名称 | N | N | Y | Y | Y |
| 切换 Org 上下文 (X-Resource-Org) | N | N | N | N | Y |

---

## 数据模型变更

### `web/src/model/user.go`
- **删除** `Owner int64` 字段（语义模糊）
- **删除** `Username` 字段（Email 即登录名，合二为一）
- **新增** `SystemRole SystemRole` 字段
- **新增** `Status UserStatus` 字段（处理无 Org 归属的休眠态）
- **Email 作为唯一登录标识**（取代 Username），unique_index

```go
type SystemRole int
const (
    SystemUser  SystemRole = iota // 0 - 普通用户
    SystemAdmin                   // 1 - 系统管理员
)

type UserStatus int
const (
    UserActive   UserStatus = 1  // 正常：属于至少一个 Org，可登录
    UserDormant  UserStatus = 2  // 休眠：不属于任何 Org，可登录但资源操作受限（等待被邀请加入 Org）
    UserDisabled UserStatus = 3  // 禁用：被 SystemAdmin 主动禁用，无法登录
)
// 注意：不设 UserDeleted 枚举值。删除通过 GORM 软删除（deleted_at）实现，
// 软删记录对所有业务查询不可见，与 status 字段语义不重叠。
// 不变量：SystemAdmin（system_role=1）的 status 只能是 UserActive 或 UserDisabled，
// 永远不会是 UserDormant（因为 SystemAdmin 始终持有 admin org 成员记录）。

type User struct {
    Model
    Email      string     `gorm:"size:255;unique_index"`  // 登录名，即用户的唯一标识
    Password   string     `gorm:"size:255"`
    FirstName  string     `gorm:"size:128"`               // 名
    LastName   string     `gorm:"size:128"`               // 姓
    Remark     string     `gorm:"size:512"`               // 备注（管理员可填写，如职位/来源等）
    Region     string     `gorm:"size:64"`                // 区域，如 "cn-north-1"、"us-east-1"
    Language   string     `gorm:"size:16;default:'zh'"`  // 界面语言，如 "zh"、"en"
    SystemRole SystemRole `gorm:"default:0"`
    Status     UserStatus `gorm:"default:1"`              // 默认 Active
    Members    []*Member  `gorm:"foreignkey:UserID"`
}
```

### `web/src/model/org.go`
- **删除** `Role` 枚举（拆分为 OrgRole 和 SystemRole）
- **删除** `Organization.Owner int64` 字段
- **删除** `Member.UserName`、`Member.OrgName`、`Member.Owner` 冗余字段
- **修改** `Member.Role` → `Member.OrgRole OrgRole`
- **新增** `Organization.OrgType OrgType` 字段
- **新增** `Organization.Slug string` 字段（全局唯一可读标识，供 UI 显示和 API 路径使用）
- **新增** members 表唯一约束 `(user_id, org_id)`

```go
type OrgRole int
const (
    OrgNone   OrgRole = iota // 0
    OrgReader               // 1
    OrgWriter               // 2
    OrgAdmin                // 3
)

// OrgType 区分 Org 的用途
type OrgType int
const (
    OrgTypeTeam   OrgType = 1  // 普通团队 Org（默认）：多人协作，可邀请成员
    OrgTypeSystem OrgType = 2  // 系统 Org：全局唯一，adminInit 创建，不可删除
)

type Organization struct {
    Model
    Name        string    `gorm:"size:255"`                      // 展示名，不做唯一约束，仅用于显示
    Slug        string    `gorm:"size:64;uniqueIndex:idx_org_slug,where:deleted_at IS NULL"`  // 全局唯一可读标识；条件唯一索引仅对未软删记录生效，允许已解散 Org 的 Slug 被复用
    OrgType     OrgType   `gorm:"default:1"`
    OwnerUserID int64     `gorm:"not null"`
    Members     []*Member `gorm:"foreignkey:OrgID"`
    OwnerUser   *User     `gorm:"foreignkey:OwnerUserID"`
    DefaultSG   int64
}

type Member struct {
    Model
    UserID  int64   `gorm:"not null;uniqueIndex:idx_user_org"`
    OrgID   int64   `gorm:"not null;uniqueIndex:idx_user_org"`
    OrgRole OrgRole `gorm:"default:1"`
}
```

**OrgType 行为差异：**

| 特性 | TeamOrg | SystemOrg |
|------|:-------:|:---------:|
| 创建方式 | SystemAdmin 创建（必须指定 OwnerUser） | adminInit 自动创建，唯一 |
| 可邀请成员 | 是（Org Owner / OrgAdmin / SystemAdmin） | 是 |
| 可被解散 | 仅 SystemAdmin（资源须清空） | 不可删除 |
| Owner 可转让 | 是（仅 SystemAdmin 操作） | 否 |

**Slug 规则：**
- 格式：小写字母、数字、连字符，长度 3~64，如 `acme-team`、`org-42`
- **必须以小写字母开头**（不能以数字或连字符开头，防止路径解析将其误认为纯数字 ID）
- 全局唯一（unique_index），创建时由调用方传入或系统自动生成
- 自动生成策略：后端在 Org 创建后取 `org-{id}` 作为保底值（字母开头，天然合法）；调用方传入时需通过格式校验
- 创建后**不可修改**（作为稳定标识，防止外部引用失效）；尝试修改时返回 `ErrSlugImmutable`
- SystemAdmin 通过列表/搜索 Org 时可用 Slug 快速区分同名 Org

**业务逻辑影响：**
- `org.Create()`：必须传入 `ownerUserID`；`slug` 可选，未传时后端自动生成 `org-{id}`；校验顺序：① 格式非法（不合规或以数字/连字符开头）→ `ErrSlugInvalid`；② 命中保留值列表 → `ErrSlugReserved`；③ 已被其他 Org 占用 → `ErrSlugConflict`；通过后创建时自动为 Owner 添加一条 `OrgAdmin` 的 Member 记录
- `org.Delete()`：`OrgTypeSystem` 直接拒绝；调用方须是 `IsSystemAdmin`；检查 Org 下是否有资源，有则拒绝（必须先清空资源）；通过后级联软删所有 Member，受影响用户无其他 Org 则设 Dormant
- `org.TransferOwner(newOwnerUserID)`：新增方法，仅 SystemAdmin 可调用；新 Owner 必须已是 Org 成员；**并发安全**：使用 `SELECT ... FOR UPDATE` 锁定 Org 记录，防止 TransferOwner 与 RemoveMember(newOwner) 并发时出现 Owner 指向非成员的竞态
- `adminInit()`：创建 admin org 时固定使用 `Slug = "admin"`（`OrgType = OrgTypeSystem`，`OwnerUserID = admin.ID`）；**保留 Slug 列表**在代码中以常量集合维护，普通 `org.Create()` 传入保留值时一律返回 `ErrSlugReserved`（独立于 `ErrSlugConflict`，便于前端区分提示）
  ```go
  // reservedSlugs 与系统路由路径保持一致，防止租户 Slug 占用后影响路由解析
  var reservedSlugs = map[string]struct{}{
      "admin":  {},
      "system": {},
      "root":   {},
      "api":    {},
      "auth":   {},
      "health": {},
      "metrics":{},
  }
  ```

### `web/src/model/model.go`
- **删除** `OwnerInfo *Organization` 关联字段（基础模型不应携带 Org 关联查询）
- **修改** `Creater` 默认值从 `1` 改为 `0`（0 表示系统创建）
- **`Creater` 降级为纯审计字段**：仅记录"谁创建了这条资源"，不再用于权限判断；资源的增删改权限完全由用户在 Org 内的 `OrgRole` 决定

---

## 权限层变更

### `web/src/common/member.go` — 完整重写

```go
type MemberShip struct {
    UserID      int64
    UserEmail   string            // Email 即登录名（取代 UserName）
    SystemRole  model.SystemRole  // 系统级角色（来自 users 表）
    OrgID       int64
    OrgName     string
    OrgRole     model.OrgRole     // Org 级角色（来自 members 表，存储值）
    IsOrgOwner  bool              // 是否为当前 Org 的 Owner（来自 organizations.owner_user_id）
}

// OrgOwner 权限规则：
// IsOrgOwner=true 时，有效 OrgRole = max(OrgRole, OrgAdmin)，即隐式拥有 OrgAdmin 能力。
// members.org_role 字段中仍会存入实际值（通常是 OrgAdmin），但权限判断以 EffectiveOrgRole() 为准：
//   func (m *MemberShip) EffectiveOrgRole() model.OrgRole {
//       if m.IsOrgOwner && m.OrgRole < model.OrgAdmin { return model.OrgAdmin }
//       return m.OrgRole
//   }
// CheckOrgPermission 内部调用 EffectiveOrgRole()，上层调用方无需感知 IsOrgOwner。

// 核心方法（新接口）
func (m *MemberShip) IsSystemAdmin() bool
func (m *MemberShip) EffectiveOrgRole() model.OrgRole                               // 考虑 IsOrgOwner 的实际生效角色
func (m *MemberShip) GetOrgFilter() (query string, args []interface{})              // 参数化，取代 GetWhere()
func (m *MemberShip) CheckOrgPermission(reqRole model.OrgRole) bool                 // 内部使用 EffectiveOrgRole()
func (m *MemberShip) CheckSystemPermission() bool                                    // 取代 CheckPermission(Admin)
func (m *MemberShip) CheckResourceOrg(reqRole model.OrgRole, ownerOrgID int64) bool       // 已知归属OrgID，直接验证
func (m *MemberShip) CheckResourceOrgByID(reqRole model.OrgRole, table string, id int64) (bool, error)  // 通过资源ID查DB再验证
func (m *MemberShip) CanManageOrgMembers() bool        // EffectiveOrgRole()>=OrgAdmin || IsSystemAdmin
func (m *MemberShip) CanManageTargetOrg(orgID int64) bool  // IsSystemAdmin（任意Org） || (CanManageOrgMembers && m.OrgID==orgID)
// CanTransferOwner / DeleteOrg：仅 IsSystemAdmin()
// CheckCreater()：已删除，Creater 降级为纯审计字段，不再用于权限判断

// GetOrgFilter（参数化，防 SQL 注入）
// SystemAdmin: ("", nil)
// 普通用户:    ("owner = ?", []interface{}{m.OrgID})
```

`GetDBMemberShip` 更新：
- JOIN `organizations` 表额外查询 `owner_user_id`，判断当前用户是否为 Owner
- 填充 `IsOrgOwner = (org.OwnerUserID == userID)`
- SystemAdmin 无需 Member 记录，但仍查询 Org 名称

---

## JWT 变更

### `web/src/routes/jwt.go`

`CustomClaims` 拆分单一 `Role` 为双角色：

```go
type CustomClaims struct {
    jwt.RegisteredClaims
    UID string            `json:"uid"`
    OID string            `json:"oid"`
    SR  model.SystemRole  `json:"sr"`  // 新增 SystemRole
    OR  model.OrgRole     `json:"or"`  // 新增 OrgRole（取代原 Role）
    ST  model.UserStatus  `json:"st"`  // 新增 UserStatus（Dormant 用户登录后前端可感知）
}
```

`NewToken` 签名更新：`func NewToken(u, o, uid, oid string, sysRole model.SystemRole, orgRole model.OrgRole, status model.UserStatus)`

> 注意：JWT 结构变化会使所有现有 token 失效，用户需重新登录。

**Dormant 用户登录后的 JWT 行为**：
- `ST = UserDormant`，`OID = ""`，`OR = OrgNone`
- 前端检测到 `ST=Dormant` 时，显示"等待加入 Org"提示页，告知用户需由 SystemAdmin 邀请加入某个 Org
- 用户无法自行创建 Org（`POST /orgs` 对 Dormant/Active 普通用户均返回 403）
- SystemAdmin 为该用户执行 `AddMember` 后，用户状态自动变为 Active；下次登录或刷新 token 时获得新的 OID/OR

---

## 授权中间件变更

### `web/src/apis/authorize.go`

- **删除** `if realUser == "admin" { memberShip.Role = model.Admin }` magic string
- **改为** 检查 JWT Claims 中的 `SR`（SystemRole）字段判断系统 Admin
- `X-Resource-Org` 切换权限改为检查 `claims.SR == model.SystemAdmin`
- **SystemAdmin 全局视图行为**：
  - 携带 `X-Resource-Org: <orgID>` → `GetOrgFilter()` 返回 `("owner = ?", orgID)`，限定该 Org 资源
  - 不携带 `X-Resource-Org` → `GetOrgFilter()` 返回 `("", nil)`，查询结果涵盖所有 Org（全局视图）
  - 创建资源时若 `OID` 为空（未指定 Org 上下文）→ 返回 `ErrMissingOrgContext`，要求通过 `X-Resource-Org` 或 `SwitchOrg` 明确上下文

---

## 业务逻辑变更

### `web/src/routes/user.go`
- `Validate()`：**新增公开接口**（无需鉴权，供中间件调用）；支持校验：
  - `?email=xxx` → 检查邮箱是否已被注册，返回是否存在及对应 user_id
  - （org_name 不做唯一性校验，Org 以 ID 为唯一标识，name 仅作展示用）
  - ⚠️ **安全注意**：该接口可被恶意调用用于用户枚举（枚举已注册邮箱）；后端应对该接口实施速率限制（rate limit），或要求中间件持有共享密钥（如 `X-Middleware-Secret` Header）才可调用
- `CreateWithOrg()`：**新增方法**（场景一，中间件在邮件验证后调用）；接收 `email, password, org_name`；**在同一数据库事务内**原子完成：
  1. User（`Email=email, Status=Active`）
  2. Organization（`OrgType=TeamOrg`，`OwnerUserID=user.ID`，`Slug` 先以空字符串占位）
  3. Member（`OrgRole=OrgAdmin`）
  4. 事务提交后立即执行 `UPDATE organizations SET slug='org-{id}' WHERE id=?`（此时 id 已知）；若调用方传入了 slug 则在步骤 2 直接写入
  任一步骤失败全部回滚
- `Create()`：接收 `email, password, system_role(可选)`；两种场景：
  - **场景二情况B（中间件调用）**：不传 `system_role`，创建普通用户，`Status=Dormant`（无 Org 归属，符合状态机），不创建 Org；中间件紧接着调用 `POST /orgs/:orgID/members` 成功后状态自动变为 Active；若 AddMember 调用失败，用户保持 Dormant（而非 Active），不会出现"Active 但无 Org"的状态污染
  - **SystemAdmin 创建系统管理员**：传入 `system_role=SystemAdmin`；仅 SystemAdmin 可调用；创建用户时同时：
    1. 设置 `SystemRole=SystemAdmin`，`Status=Active`
    2. 自动加入 admin org（`OrgTypeSystem`），`OrgRole=OrgAdmin`
    3. 无需邮件验证，直接可用（密码由 SystemAdmin 设定或生成后带外告知）
- `DemoteSystemAdmin(targetUserID)`：**新增方法**；仅 SystemAdmin 可调用；将目标用户的 `SystemRole` 从 `SystemAdmin` 降级为 `SystemUser`，同时：
  1. 校验：不能对自己降级（防止最后一个 SystemAdmin 误操作）
  2. 校验：系统中 SystemAdmin 数量必须 > 1，否则拒绝（保证至少有一个 SystemAdmin）
  3. 从 admin org 移除该用户的 Member 记录
  4. 执行 Dormant 后置检查（若无其他 Org 则设为 Dormant）
- `Delete()`：仅 SystemAdmin 可删；前置检查：
  1. 不能删除自己
  2. 若 target.SystemRole == SystemAdmin：校验系统 SystemAdmin 数量 > 1，否则返回 `ErrLastSystemAdmin`
  3. 查询该用户是 Owner 的所有 Org：
     - 该 Org 还有其他成员 → 拒绝，提示先 `TransferOwner`
     - 该 Org 只有此用户一人（无其他成员）且 Org 无云资源 → 允许同步解散该 Org
     - 该 Org 只有此用户一人但有云资源 → 拒绝，提示先清空资源再解散 Org
  4. 通过后，以下操作在同一事务内完成：级联软删所有 Member 记录（含自动解散符合条件的 Org）、再软删 User；任一步骤失败全部回滚
- `Enable()/Disable()`：SystemAdmin 启用/禁用用户；重新启用时：查询剩余 Member 数 > 0 → Active，= 0 → Dormant
- `ChangePassword()`：**新增方法**；任何已登录用户（包括 Dormant）可调用；接收 `old_password, new_password`；流程：
  1. 从 JWT 取当前 `UserID`，查询用户记录
  2. `CompareHashAndPassword(user.Password, old_password)` 验证旧密码 → 不匹配返回 `ErrPasswordMismatch`
  3. `GenerateFromPassword(new_password)` 生成新哈希
  4. `UPDATE users SET password=? WHERE id=?`
  > 注意：Disabled 用户无法获取 token，自然无法调用此接口，无需额外校验
- `AccessToken()`：用 email 登录；检查 `User.Status`：`UserDisabled` → 403 ErrUserDisabled；`UserDormant` → **允许登录**，JWT 中携带 `Status=Dormant`，前端/中间件引导用户创建 Org；可选传入 `org_id` 参数指定登录后的 Org 上下文（不传则取 `member.created_at` 最早的 Org）
- `SwitchOrg()`：**新增方法**；任何已登录用户可调用（含 Dormant，用于登录后切换）；接收 `org_id`；校验用户是该 Org 的成员（SystemAdmin 可切换到任意 Org）；返回新 token（OID/OR 更新为目标 Org）
  > **前端处理要点**：收到新 token 后必须立即替换本地存储（localStorage / Cookie）中的旧 token，并刷新当前页面资源状态（建议触发全局 store 重置或页面 reload），确保后续所有请求的 `OID`/`OR` 与新 Org 上下文一致；切勿仅更新部分状态导致新旧 token 混用
- `UpdateProfile()`：**新增方法**；任何已登录用户可调用；可修改自己的 `FirstName、LastName、Region、Language`；SystemAdmin 额外可修改任意用户的 `Remark` 字段；Email 和 Password 通过独立接口修改

### `web/src/routes/org.go`
- `Create()`：**仅 SystemAdmin 可调用**；必须传入 `ownerUserID`；权限检查 `CheckSystemPermission()`；创建 Org 时设置 `OwnerUserID`，同时为 Owner 创建 `OrgAdmin` Member 记录；非 SystemAdmin 调用一律返回 403（包括 Dormant 用户和 Active 普通用户）
- `RenameOrg(orgID, newName)`：**新增方法**；OrgOwner / OrgAdmin / SystemAdmin 可调用；`OrgTypeSystem` 禁止更名；直接更新 `organizations.name`（无唯一限制）
- `AddMember(orgID, userID, role)`：**新增方法**；权限检查：
  - `IsSystemAdmin()` → 可操作任意 Org，无需自己是该 Org 成员
  - 否则检查 `CanManageOrgMembers()` 且当前 Org 匹配目标 orgID
  - 被邀请的 userID 必须存在且 Status != Disabled
  - 不能将 OrgRole 设置高于操作者自身的 OrgRole（SystemAdmin 除外）
  - 创建 Member 记录；若 user.Status == Dormant → 自动恢复 Active
- `RemoveMember(orgID, userID)`：权限检查同 `AddMember`；移除后执行 Dormant 后置检查
- `UpdateMemberRole(orgID, userID, newRole)`：**新增方法**；权限检查同上；限制：
  - 不能将角色设置高于操作者自身 OrgRole（SystemAdmin 除外）
  - **target 是 Org Owner 时，任何人（包括 SystemAdmin）都不能修改其 OrgRole**（OrgOwner 的有效权限隐式为 OrgAdmin，修改存储值无意义且易造成混淆）；如需调整，须先 TransferOwner 变更归属，再操作原 Owner 的 OrgRole
- `Update()`（成员管理聚合接口）：调用上述方法，权限检查委托给各子方法
- `Delete()`：权限检查：**仅 `IsSystemAdmin`**，`OrgTypeSystem` 直接拒绝；流程：
  1. 检查 Org 下是否有云资源（instance/volume 等）→ 有则返回 `ErrOrgHasResources`
  2. 级联软删所有 Member 记录
  3. 对受影响用户执行 Dormant 后置检查
  4. 软删 Org 记录
- `TransferOwner()`：**新增方法**；**仅 SystemAdmin 可调用**；流程：
  1. 校验新 Owner 必须已是该 Org 的 Member（`deleted_at IS NULL`）
  2. 更新 `organizations.owner_user_id`（新 Owner 的 `members.org_role` 字段值不变；其有效权限由 `EffectiveOrgRole()` 隐式保证为 OrgAdmin）
  3. 原 Owner 的 `members.org_role` 保持不变，但其 `IsOrgOwner` 变为 false，有效权限降回其 `members.org_role` 存储值；SystemAdmin 可后续调用 `UpdateMemberRole` 显式调整
  4. 以上步骤在同一事务内完成（含 `SELECT ... FOR UPDATE` 行锁）

### `web/src/routes/admin.go`
- `adminInit()`：幂等操作（每次服务启动都可安全调用）；采用 check-before-create 策略：
  1. 查询 `system_role=1` 的用户是否存在 → 不存在则创建 admin 用户（`SystemRole=SystemAdmin, Status=Active`，Email 读取配置项 `ADMIN_EMAIL`，默认为 `admin@cloudland.local`）
  2. 查询 `org_type=2` 的 Org 是否存在 → 不存在则创建 admin org（`OrgTypeSystem, OwnerUserID=admin.ID`）
  3. 查询 admin 的 Member 记录是否存在 → 不存在则创建（`OrgRole=OrgAdmin`）

---

## 外部中间件集成

外部中间件（如自动化平台）负责所有邮件交互，使用 admin 账号（SystemAdmin）调用后端 API。

### 两种场景的 API 调用序列

**场景一：新用户注册（用户成为新 Org 的 Owner）**

```
中间件在邮件验证通过后调用：
  GET /validate?email=xxx                → { exists: false }（邮箱可用）
  [发送邮件，等待用户点击确认]
  POST /users/with-org                   → 一次性创建 User + Org
    Body: { username, email, password, org_name }   // org_name 为展示名，无唯一限制
    Response: { user_id, org_id }
```

**场景二：邀请用户加入现有 Org**

```
中间件在邀请者发起邀请后调用：
  GET /validate?email=xxx                → { exists: true/false, user_id? }

  情况A（用户已存在）：
    [发送加入邀请邮件，等待用户点击确认]
    POST /orgs/:orgID/members
      Body: { user_id: <已有ID>, org_role: 1 }

  情况B（用户不存在）：
    [发送注册+加入邀请邮件，用户点击后设置密码]
    POST /users
      Body: { username, email, password }   → 创建 User(Active)，不创建 Org
    POST /orgs/:orgID/members
      Body: { user_id: <新ID>, org_role: 1 }
```

| 项目 | 场景一（新用户注册） | 场景二A（已有用户） | 场景二B（新用户加入已有Org） |
|------|:-------------------:|:------------------:|:---------------------------:|
| User 创建 | `POST /users/with-org` | 不创建 | `POST /users` |
| Org 创建 | 是（同时创建） | 否 | 否 |
| Member 创建 | 是（OrgAdmin） | `POST /orgs/:id/members` | `POST /orgs/:id/members` |
| 是否成为 Org Owner | 是 | 否 | 否 |

---

## 用户无 Org 归属的处理

### 触发场景
1. OrgOwner / OrgAdmin 将成员移除，被移除用户不再属于任何 Org
2. Org 被解散，Org 内部分成员不再属于任何其他 Org

### 处理原则
- **不自动删除用户**：删除用户是 SystemAdmin 专属操作，OrgOwner / OrgAdmin 无此权限
- **不拒绝移除操作**：移除是合法行为，不因"用户将无 Org"而阻断
- **系统自动标记 Dormant**：移除后执行后置检查，无 Org 则设 `user.Status = UserDormant`
- **Dormant 用户可登录**：`AccessToken()` 允许 Dormant 用户获取 token（JWT 中 `ST=Dormant`），但调用资源相关接口时返回 `ErrNoOrgMembership`；前端引导用户**等待 SystemAdmin 邀请加入 Org**（Dormant 用户不能自建 Org）

> 用户状态流转详见前文「场景三：用户状态流转总览」图示。

### 移除成员的后置检查逻辑

```
RemoveMember(orgID, userID):
  1. 检查 user 是否为 org.OwnerUserID → 是则拒绝（必须先 TransferOwner）
  2. 软删 Member 记录
  3. 查询 user 剩余 Member 数量
       └─► 数量 = 0 → UPDATE users SET status=Dormant WHERE id=userID
       └─► 数量 > 0 → 无需处理
```

### 解散 Org 的后置检查逻辑

```
DeleteOrg(orgID):
  0. 前置检查：非 SystemAdmin 拒绝（Owner 也无权限）；OrgTypeSystem 拒绝；Org 下有云资源拒绝
  1. 收集 Org 内所有 member.user_id（含 Owner，Owner 也可能因此无 Org 而 Dormant）
  2. 软删所有 Member 记录
  3. 对每个受影响 user：
       查询剩余 Member 数量 = 0 → 设 UserDormant
       查询剩余 Member 数量 > 0 → 无需处理（仍 Active）
  4. 软删 Organization 记录
```

### 加入 Org 时自动恢复 Active

```
AddMember(orgID, userID, role):
  1. 创建 Member 记录
  2. 如果 user.Status == UserDormant → UPDATE users SET status=Active WHERE id=userID
```

### 新增错误码

```go
ErrNoOrgMembership   = 100210  // Dormant 用户操作受限资源时返回，HTTP 403（登录本身不拦截）
ErrUserDisabled      = 100211  // 用户已被禁用，HTTP 403
ErrCannotRemoveOwner = 100212  // 不能移除 Org Owner，必须先转让，HTTP 400
ErrEmailConflict     = 100213  // 邮箱已被注册（Validate 接口返回），HTTP 409
ErrLastSystemAdmin   = 100214  // 不能降级/删除最后一个 SystemAdmin，HTTP 400
ErrCannotSelfDemote  = 100215  // 不能对自己执行降级操作，HTTP 400
ErrMissingOrgContext = 100216  // SystemAdmin 创建资源时未指定 Org 上下文，HTTP 400
ErrOrgHasResources   = 100217  // Org 下仍有云资源，无法解散，HTTP 409
ErrOrgHasMembers     = 100218  // 无法删除用户：该用户仍是某 Org 的 Owner 且 Org 有其他成员，HTTP 409
ErrSlugConflict      = 100219  // Org Slug 已被其他 Org 占用，HTTP 409
ErrSlugImmutable     = 100220  // Org Slug 创建后不可修改，HTTP 400
ErrSlugInvalid       = 100221  // Org Slug 格式非法（不合规或以数字/连字符开头），HTTP 400
ErrSlugReserved      = 100222  // Org Slug 为系统保留值（admin/system/root/api 等），HTTP 409
```

---

## 资源文件批量替换（~17 个文件）

所有资源路由文件（instance/volume/image/subnet/router/key/floatingip/secgroup/secrule/
flavor/dictionary/ipgroup/loadbalancer/interface/storage/backup/consistency_group）进行统一替换：

| 旧调用 | 新调用 |
|--------|--------|
| `memberShip.GetWhere()` → `db.Where(where)` | `memberShip.GetOrgFilter()` → `db.Where(query, args...)` |
| `memberShip.ValidateOwner(model.Reader, r.Owner)` | `memberShip.CheckResourceOrg(model.OrgReader, r.Owner)` |
| `memberShip.ValidateOwner(model.Writer, r.Owner)` | `memberShip.CheckResourceOrg(model.OrgWriter, r.Owner)` |
| `memberShip.CheckPermission(model.Reader)` | `memberShip.CheckOrgPermission(model.OrgReader)` |
| `memberShip.CheckPermission(model.Writer)` | `memberShip.CheckOrgPermission(model.OrgWriter)` |
| `memberShip.CheckPermission(model.Admin)` | `memberShip.CheckSystemPermission()` |
| `memberShip.CheckOwner(model.Writer, "tbl", id)` | `memberShip.CheckResourceOrgByID(model.OrgWriter, "tbl", id)` |
| `memberShip.CheckCreater("tbl", id)` | **直接删除**（Creater 已降级为纯审计字段，不再参与权限判断） |

> **资源归属字段说明**：
> - 所有资源模型（Instance/Volume/Subnet 等）均有 `Owner int64` 字段，存储的是 **Organization.ID**（即资源属于哪个 Org）
> - 创建资源时从 JWT 的 `OID`（当前 Org）写入 `Owner`，从 `UID` 写入 `Creater`
> - `Owner`：权限判断依据，决定"谁能操作这个资源"
> - `Creater`：纯审计字段，记录"谁创建了这个资源"，不参与权限判断
>
> 示例（创建 Instance）：
> ```go
> instance := &model.Instance{
>     Owner:   memberShip.OrgID,   // 来自 JWT OID，标识资源归属 Org
>     Creater: memberShip.UserID,  // 来自 JWT UID，纯审计
> }
> ```

---

## 数据库迁移 SQL（有序执行）

```sql
-- Step 1: 添加新字段（非破坏性）
ALTER TABLE users ADD COLUMN system_role INT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN status INT NOT NULL DEFAULT 1;              -- 新增 UserStatus（存量数据默认 Active）
ALTER TABLE users ADD COLUMN email VARCHAR(255) DEFAULT '';              -- 新增 Email（登录名）
ALTER TABLE users ADD COLUMN first_name VARCHAR(128) DEFAULT '';        -- 名
ALTER TABLE users ADD COLUMN last_name VARCHAR(128) DEFAULT '';         -- 姓
ALTER TABLE users ADD COLUMN remark VARCHAR(512) DEFAULT '';            -- 备注
ALTER TABLE users ADD COLUMN region VARCHAR(64) DEFAULT '';             -- 区域
ALTER TABLE users ADD COLUMN language VARCHAR(16) DEFAULT 'zh';        -- 界面语言
CREATE UNIQUE INDEX idx_users_email ON users(email) WHERE deleted_at IS NULL AND email != '';
-- 注意：旧表字段名以实际 schema 为准（可能是 name 或 username），执行前确认
UPDATE users SET system_role = 1 WHERE name = 'admin';  -- 若字段为 username 则改为 WHERE username = 'admin'
-- 将现有无 Org 归属的用户标记为 Dormant
UPDATE users SET status = 2
WHERE id NOT IN (SELECT DISTINCT user_id FROM members WHERE deleted_at IS NULL)
  AND name != 'admin' AND deleted_at IS NULL;  -- 同上，字段名以实际为准
ALTER TABLE organizations ADD COLUMN org_type INT NOT NULL DEFAULT 1;
ALTER TABLE organizations ADD COLUMN owner_user_id BIGINT NOT NULL DEFAULT 0;  -- 新增 Owner
UPDATE organizations SET org_type = 2 WHERE name = 'admin';  -- 标记系统 Org
-- 迁移旧 Owner 字段：旧 organizations.owner 存的是 User.ID
UPDATE organizations SET owner_user_id = owner WHERE owner > 0;
ALTER TABLE members ADD COLUMN org_role INT NOT NULL DEFAULT 0;

-- Step 2: 迁移旧 role 值
UPDATE members SET org_role = CASE
    WHEN role >= 3 THEN 3   -- Owner/Admin -> OrgAdmin
    WHEN role = 2 THEN 2    -- Writer -> OrgWriter
    WHEN role = 1 THEN 1    -- Reader -> OrgReader
    ELSE 0 END;

-- Step 3: 添加唯一约束（先清理重复记录）
DELETE m1 FROM members m1 JOIN members m2
  ON m1.user_id=m2.user_id AND m1.org_id=m2.org_id AND m1.org_role < m2.org_role;
CREATE UNIQUE INDEX idx_members_user_org ON members(user_id, org_id)
  WHERE deleted_at IS NULL;

-- Step 4: 删除冗余字段
ALTER TABLE members DROP COLUMN user_name, DROP COLUMN org_name,
                     DROP COLUMN owner, DROP COLUMN role;
ALTER TABLE users DROP COLUMN owner;
ALTER TABLE users DROP COLUMN name;          -- 旧 username 字段（以 email 作为唯一登录标识）
ALTER TABLE organizations DROP COLUMN owner;

-- Step 5（可选）: 创建资源共享表
CREATE TABLE resource_shares (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    uuid VARCHAR(64) UNIQUE,
    created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
    creater BIGINT DEFAULT 0,
    resource_type VARCHAR(64) NOT NULL,
    resource_id   BIGINT NOT NULL,
    owner_org_id  BIGINT NOT NULL,
    shared_org_id BIGINT NOT NULL DEFAULT 0,  -- 0 = 公开
    permission    INT NOT NULL DEFAULT 1,
    UNIQUE KEY idx_share (resource_type, resource_id, shared_org_id)
);
```

---

## 实施顺序

1. `model/org.go` + `model/user.go` + `model/model.go` — 数据模型，先跑 AutoMigrate
2. `common/member.go` — 重写 MemberShip，新旧接口暂时并存
3. `routes/jwt.go` — 更新 Token Claims（部署后旧 token 失效）
4. `apis/authorize.go` — 去掉 magic string
5. `routes/admin.go` — adminInit 设置 SystemAdmin
6. `routes/user.go` + `routes/org.go` — 核心业务逻辑
7. 资源文件（~17个）— 批量替换调用点
8. 执行迁移 SQL

---

## 验证方案

### 权限单元测试
- `GetOrgFilter()` SystemAdmin 返回空，普通用户返回参数化条件
- `CheckOrgPermission()` 级联验证：OrgWriter 满足 OrgReader 但不满足 OrgAdmin
- `CanManageOrgMembers()` OrgAdmin=true，OrgWriter=false
- `CheckResourceOrg()` OrgID 不匹配时返回 false

### 集成场景
1. SystemAdmin 创建 Org（指定 alice@example.com 为 Owner）→ alice 自动成为 OrgAdmin Member，Status=Active
2. alice 邀请 bob@example.com 为 OrgReader → Middleware 发邮件，bob 确认后 AddMember → bob.Status=Active
3. alice 移除 bob（bob 无其他 Org）→ bob.Status 自动变为 Dormant；bob 用 email 登录成功（JWT ST=Dormant），调用创建实例接口 → 403 ErrNoOrgMembership；bob 创建新 Org → Status 恢复 Active
4. SystemAdmin 将 bob 加入另一个 Org → bob.Status 恢复为 Active
5. alice 尝试移除自己（Org Owner）→ 403 ErrCannotRemoveOwner
6. SystemAdmin 将 Owner 从 alice 转让给 carol → carol.IsOrgOwner=true
7. alice 尝试解散 Org → 403（仅 SystemAdmin 可解散）
8. SystemAdmin 解散 Org（Org 下有 instance）→ 409，提示先清空资源；清空后再解散 → 成功，成员无其他 Org 则 Dormant
9. SystemAdmin 禁用 dave → dave.Status=Disabled，dave 无法登录；SystemAdmin 重新启用 dave：dave 有 Org → Active，无 Org → Dormant
10. SystemAdmin 通过 X-Resource-Org 切换 Org → 成功；普通用户尝试 → 403
11. 中间件调用 POST /users（场景二B）后 AddMember 失败 → 用户状态保持 Dormant，不出现 Active 但无 Org 的脏状态
12. SystemAdmin 将 Owner 从 alice 转让给 bob（bob 当前是 OrgReader）→ bob.IsOrgOwner=true，其 members.org_role 仍为 OrgReader（存储值不变），但 EffectiveOrgRole()=OrgAdmin；alice.IsOrgOwner=false，其有效权限降回其 members.org_role 存储值（OrgAdmin）；SystemAdmin 可进一步调用 UpdateMemberRole 显式降低 alice 角色
13. Org 被软删除后，使用相同 Slug 创建新 Org → 成功（条件唯一索引不阻止已删除记录的 Slug 复用）
14. TransferOwner(carol) 与 RemoveMember(carol) 并发执行 → 其中一个获得行锁，另一个等待；最终结果确定（不出现 Owner 指向非成员的状态）

### 数据完整性验证 SQL
```sql
-- 每个 Org 有且仅有一个有效 OwnerUserID
SELECT o.name FROM organizations o
LEFT JOIN users u ON u.id = o.owner_user_id AND u.deleted_at IS NULL
WHERE (o.owner_user_id = 0 OR u.id IS NULL) AND o.deleted_at IS NULL;

-- 每个 Org 的 Owner 必须是该 Org 的成员
SELECT o.name FROM organizations o
WHERE NOT EXISTS (
    SELECT 1 FROM members m
    WHERE m.org_id=o.id AND m.user_id=o.owner_user_id AND m.deleted_at IS NULL
) AND o.deleted_at IS NULL AND o.org_type != 2;

-- 每个 Org 至少有一个 OrgAdmin
SELECT o.name FROM organizations o WHERE NOT EXISTS (
    SELECT 1 FROM members m WHERE m.org_id=o.id AND m.org_role=3 AND m.deleted_at IS NULL
) AND o.deleted_at IS NULL;

-- 至少存在一个 SystemAdmin
SELECT COUNT(*) FROM users WHERE system_role=1 AND deleted_at IS NULL;

-- 无 Org 归属的用户必须是 Dormant 状态（不应是 Active）
SELECT u.email FROM users u
WHERE u.status = 1
  AND u.system_role = 0
  AND u.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM members m WHERE m.user_id=u.id AND m.deleted_at IS NULL
  );

-- Org Owner 不能是 Dormant 状态
SELECT u.email, o.name FROM organizations o
JOIN users u ON u.id = o.owner_user_id
WHERE u.status = 2 AND o.deleted_at IS NULL;
```

---

## 关键文件路径

| 文件 | 变更类型 |
|------|---------|
| `web/src/model/org.go` | 重写 |
| `web/src/model/user.go` | 修改 |
| `web/src/model/model.go` | 微调 |
| `web/src/common/member.go` | 重写 |
| `web/src/routes/jwt.go` | 修改签名 |
| `web/src/apis/authorize.go` | 修改鉴权逻辑 |
| `web/src/routes/user.go` | 修改（新增 Validate、CreateWithOrg 方法） |
| `web/src/routes/org.go` | 修改 |
| `web/src/routes/admin.go` | 修改 adminInit |
| `web/src/routes/instance.go` + 16 个资源文件 | 批量替换调用 |
