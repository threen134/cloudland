# CPGateway Go 迁移方案

> **实施状态（2026-09-14）**：CPGateway-Go 已按 Python 实现完成功能对齐（接口契约、业务逻辑、权限），面向**全新环境**部署、不做数据迁移，Python `cpgateway/` 代码已移除。以下为实际实现与原方案的主要差异，正文相关章节已同步标注：
>
> | 方面 | 原方案 | 实际实现 |
> |------|--------|---------|
> | 用户状态 | 不引入 `is_active`/`is_superuser` | 保留两列，还原 Python 的 Dormant 语义 |
> | 错误格式 | 统一 clapi 格式 | 兼容 FastAPI `{"detail": ...}`，前端零修改 |
> | 配额检查 | Gin 中间件 | 代理 handler 内预扣/结算，`clause.Locking` 行锁 |
> | 代理 | `httputil.ReverseProxy` | 按 Python 路由生成 150 条白名单 + `http.Client` 转发 |
> | 邮件 | go-mail + 模板文件 | `net/smtp` + 内联中英文模板，SMTP/飞书配置读系统设置 |
> | 配置 | toml + `GATEWAY_DB_URI` | viper，环境变量覆盖（`db.uri` → `DB_URI`） |
> | 数据迁移 | 一次性迁移脚本 | 不需要，AutoMigrate 建表 |
>
> 与 Python 的有意差异：所有登录态接口都校验 token 吊销并拒绝已禁用用户；`DELETE /instances/{id}/interfaces/{iid}` 等子资源删除不释放实例配额；重新邀请时一并清理已过期的邀请记录；Python 中会返回 500 的几处（删除区域、组织详情、禁用用户切换区域）按预期实现。
>
> Python 中不存在、为前端新增的接口：`PUT /users/{uuid}`（前端「编辑用户」使用；用户名/邮箱仅系统管理员可改，角色为调用者当前组织内的角色，组织管理员可改）；成员列表响应增加 `username` 字段。

## 1. 背景与动机

### 1.1 现状

CPGateway 是 Python FastAPI 实现的控制面网关（~9,000 行），承担以下职责：

| 职责 | 说明 |
|------|------|
| 认证鉴权 | RS256 JWT 签发/验证/吊销，用户注册/激活/登录 |
| 多租户管理 | 组织 CRUD、成员管理、邀请流程 |
| 多区域管理 | Region 注册、心跳监测、密钥轮转 |
| 配额引擎 | 资源预扣/回滚、实时用量追踪 |
| 反向代理 | 请求透传到区域后端，注入认证头 |
| 系统设置 | 动态配置管理、跨区域同步 |
| 通知服务 | 邮件/飞书通知、频道管理 |

当前请求链路：

```
Web UI (:5173) → CPGateway (Python :8000) → Go API (clapi :8255) → C++ cland (:5006)
```

### 1.2 迁移收益

| 收益 | 说明 |
|------|------|
| **消除 Python 运行时** | 不再依赖 virtualenv、pip、uvicorn，网关也变为单个 Go 二进制 |
| **统一语言栈** | 网关和区域 API 都是 Go，复用模型、工具库、构建流程 |
| **简化部署** | Python 容器 → Go 二进制容器，镜像更小、启动更快 |
| **类型安全** | 编译期捕获错误，减少运行时类型问题 |
| **性能提升** | goroutine 天然适合代理层的高并发 IO |
| **统一 ORM** | 两套 ORM（SQLAlchemy + GORM）+ 两套迁移工具 → 统一 GORM + AutoMigrate |

### 1.3 迁移代价

| 代价 | 说明 |
|------|------|
| 开发工作量 | 预计 9,000 行 Python → 12,000-15,000 行 Go |
| Go 生态差异 | Pydantic 校验 → struct tag + validator；Jinja2 → html/template；httpx → net/http |
| 迁移期风险 | 新旧代码并行期间需保持功能一致 |

---

## 2. 现有 Go API 可参考基础

Go API（`api/`）已有大量可参考的代码和模式。CPGateway-Go 作为独立项目，可以参考（或通过 Go module 引用）这些组件：

### 2.1 可参考组件

| 组件 | clapi 位置 | CPGateway-Go 复用方式 |
|------|-----------|---------------------|
| Gin 路由框架 | `api/src/apis/routes.go` | 参考路由组织方式，网关独立定义自己的路由 |
| 权限模型 | `api/src/common/member.go` | 参考 `MemberShip` struct 定义，网关端需要相同的角色模型 |
| 错误处理 | `api/src/common/error_codes.go` | **未采用**：为前端零修改，网关沿用 FastAPI 的 `{"detail": ...}` 错误格式（`cpgateway-go/src/common/httperr.go`） |
| GORM ORM | `api/src/dbs/db.go` | 参考连接池、事务管理模式 |
| 基础模型 | `api/src/model/` | 参考 `Model` 基类（ID、UUID、软删除、审计字段） |
| 组织模型 | `api/src/model/org.go` | 参考字段定义，网关端扩展（新增 OrgStatus、邀请字段） |
| 用户模型 | `api/src/model/user.go` | 参考字段定义，网关端微调（UserInvited 枚举） |

### 2.2 区域 clapi 不改动

当前 Go API 的 `Authorize()` 中间件信任 CPGateway 注入的 `X-*` Header + `X-Forwarded-Secret` 共享密钥。迁移后这一行为**保持不变**——CPGateway-Go 仍然以相同方式注入 `X-*` Header，区域 clapi 不需要任何修改。

---

## 3. 迁移策略：渐进式替换

采用**分阶段迁移**而非一次性重写，每个阶段独立可部署、可回滚。CPGateway-Go 是独立服务，与区域 clapi 互不影响。

### 3.1 多区域架构决策

当前多区域部署架构：
```
CPGateway (1 实例，中央网关)
  ├→ clapi-regionA (区域A) → cland-A
  └→ clapi-regionB (区域B) → cland-B
```

迁移后保持现有部署拓扑不变：**独立的网关服务**（Go 重写）+ 独立的区域 clapi，两个是不同的 Go 二进制、不同的代码库。网关只是从 Python 换成 Go，区域 clapi 完全不改动，继续信任网关注入的 `X-*` Header。

```
CPGateway-Go (独立二进制，:8000)    ← 新的 Go 网关，替代 Python CPGateway
  ├→ clapi-regionA (:8255) → cland-A   ← 不改动
  └→ clapi-regionB (:8255) → cland-B   ← 不改动
```

### 3.2 总体架构演进

```
阶段 0（现状）：
  Web UI → CPGateway (Python :8000) → clapi-region (:8255) → cland
  
阶段 1（认证层迁移）：
  Web UI → CPGateway-Go (:8000) [JWT 签发/验证/用户管理]
         → clapi-region (:8255) → cland
         ↘ Python CPGateway 可选保留做灰度对比

阶段 2（配额迁移）：
  Web UI → CPGateway-Go (:8000) [认证 + 配额 + 代理]
         → clapi-region (:8255) → cland
         × Python CPGateway 下线

阶段 3（辅助功能迁移）：
  Web UI → CPGateway-Go (:8000) [完整功能]
         → clapi-region (:8255) → cland
```

> **注意**：CPGateway-Go 是独立的 Go 项目（`cpgateway-go/`），有自己的 `go.mod`、`main.go` 和构建产物，与区域 clapi（`api/`）是两个完全独立的代码库和二进制。两者可以共享部分 model 定义（通过 Go module 引用或代码复制），但运行时互不依赖。

### 3.3 数据库策略

#### CPGateway-Go 独立数据库

CPGateway-Go 需要**自己独立的数据库**，存放全局控制面数据。这与当前 Python CPGateway 的数据库架构保持一致：

```
CPGateway-Go (:8000)   →  PostgreSQL: cloudland_cpgateway（网关库，由 .env 的 CPGATEWAY_DB 指定）
                           ├── users            # 全局用户表（认证、角色）
                           ├── organizations    # 全局组织表（租户、配额）
                           ├── members          # 用户-组织关系 + 邀请
                           ├── regions          # 区域注册信息
                           ├── token_revocations # JWT 吊销记录
                           ├── org_resource_quotas       # 配额限制
                           ├── org_resource_consumptions # 实时用量
                           ├── system_settings           # 系统设置（主表，可读写）
                           ├── system_config_version     # 配置版本号
                           └── notification_channels     # 通知频道

区域 clapi (:8255)     →  PostgreSQL: cloudland（区域业务库）
                           ├── users            # 用户镜像（从网关同步，含 ID 映射）
                           ├── organizations    # 组织镜像（从网关同步）
                           ├── members          # 成员镜像
                           ├── instances, volumes, subnets ...  # 业务资源表
                           ├── system_setting_mirror     # 设置镜像（只读）
                           └── notification_channels     # 通知频道镜像
```

**设计原则**：
- 网关库是用户/组织/配额的**唯一数据源**（Single Source of Truth）
- 区域库的 users/organizations/members 是镜像，通过 `/internal/*/sync` 端点从网关推送
- 两个库可以在同一个 PostgreSQL 实例上（不同 database），也可以分开部署

#### 模型与 CPGateway 对比

CPGateway-Go 的模型可参考现有 `api/src/model/` 的定义（字段命名、风格保持一致），但代码独立存放在 `cpgateway-go/` 下：

| 网关库表 | clapi 已有对应模型 | 实际差异 | CPGateway-Go 策略 |
|---------|-------------------|---------|-------------------|
| `users` | 有（`api/src/model/user.go`） | 已有 `FirstName`、`LastName`、`Remark`、`Language`、`SystemRole`、`Status`，缺 CPGateway 的 `is_active`/`is_superuser` | **参考 clapi 定义 + 保留 `is_active`/`is_superuser`**（见下方说明） |
| `organizations` | 有（`api/src/model/org.go`） | 已有 `Slug`、`OrgType`、`OwnerUserID`、`DefaultSG`。**仅缺** `Status`（OrgStatus） | **参考 + 扩展**：新增 `Status OrgStatus` 字段 |
| `members` | 有（`api/src/model/org.go`） | 已有 `UserID`、`OrgID`、`OrgRole`。**缺** 邀请相关字段 + 唯一索引需改为部分索引 | **参考 + 扩展**：新增邀请字段 + 部分唯一索引 |
| `regions` | 无 | — | **新建** |
| `token_revocations` | 无 | — | **新建** |
| `org_resource_quotas` | 无 | — | **新建** |
| `org_resource_consumptions` | 无 | — | **新建** |
| `system_settings` | **部分**（clapi 有 `SystemSettingMirror` 只读镜像） | 网关需要可读写的主表 | **新建主表** `SystemSetting`（可 CRUD） |
| `system_config_version` | **部分**（clapi 有 `system_setting_mirror_version`） | 同上 | **新建** `SystemConfigVersion` 主表 |
| `notification_channels` | 有（`api/src/model/notification.go`） | 字段基本一致 | **参考 clapi 定义** |

#### User 模型的 `is_active`/`is_superuser` 处理

> **实际实现（已调整）**：保留 `is_active`、`is_superuser` 两列，与 Python 语义一致。Python 中 `status=DORMANT` 同时表示“注册未激活”（`is_active=false`）和“已激活但不属于任何组织”（`is_active=true`，仍可登录），只用 `Status` 无法区分，会导致被移出所有组织的用户无法登录。管理员判断为 `is_superuser || system_role == ADMIN`。

#### 数据迁移

> **不适用**：全新环境部署，不从 Python CPGateway 迁移数据，首次启动由 AutoMigrate 建表。

---

## 4. 分阶段实施计划

### 阶段 1：认证与用户管理（核心，优先级最高）

**目标**：CPGateway-Go 具备独立的 JWT 签发/验证能力，接管用户、组织、成员管理。

#### 1.1 JWT 认证模块

**新建文件**：`cpgateway-go/src/common/jwt.go`

```
功能清单：
├── RSA 密钥加载（从文件或环境变量）
├── create_access_token()  — RS256 签名，Claims 包含:
│   sub, email, org_id, org_name, region, sr, or, st, is_owner, jti, exp
├── decode_access_token()  — 验证签名 + 过期时间
├── create_activation_token()  — HS256，用于邮件激活
├── create_invitation_token() — HS256，用于组织邀请
├── verify_activation_token()
└── verify_invitation_token()
```

**Go 依赖**：`github.com/golang-jwt/jwt/v4`（go.mod 已有）

#### 1.2 密码哈希模块

**新建文件**：`cpgateway-go/src/common/password.go`

```
功能清单：
├── HashPassword(plain string) string        — bcrypt 哈希
└── VerifyPassword(plain, hashed string) bool — bcrypt 验证
```

**Go 依赖**：`golang.org/x/crypto/bcrypt`（go.mod 已有）

#### 1.3 扩展数据模型

**新建**：`cpgateway-go/src/model/user.go`（参考 `api/src/model/user.go`）

参考 clapi User 已有字段：`Email`、`FirstName`、`LastName`、`Remark`、`Language`、`SystemRole`、`Status`。

```
实际实现：
├── UserStatus 枚举新增 UserInvited=0（Invited=0, Active=1, Dormant=2, Disabled=3）
├── 密码列为 hashed_password（与 Python 一致），bcrypt 哈希
└── 新增 IsActive / IsSuperuser 两列（原因见 3.3）
```

> 注意：`Status` 等零值有业务含义的字段不能带 GORM `default` 标签，否则 `UserInvited=0` 这类零值会在插入时被替换成列默认值。

**新建**：`cpgateway-go/src/model/org.go`（参考 `api/src/model/org.go`）

参考 clapi Organization 已有字段：`Name`、`Slug`、`OrgType`、`OwnerUserID`、`DefaultSG`。

```
新增字段：
└── Status OrgStatus  (0=Pending, 1=Active, 2=Suspended, 3=Disabled)

新增枚举：
├── OrgPending   OrgStatus = 0
├── OrgActive    OrgStatus = 1
├── OrgSuspended OrgStatus = 2
└── OrgDisabled  OrgStatus = 3
```

**新建**：`cpgateway-go/src/model/member.go`（参考 `api/src/model/org.go` 的 Member struct）

参考 clapi Member 已有字段：`UserID`、`OrgID`、`OrgRole`。

```
新增字段（邀请流程）：
├── InvitationToken     string    `gorm:"size:512;uniqueIndex"`
├── InvitationStatus    int       // 0=Pending, 1=Accepted, 2=Expired, 3=Cancelled
├── InvitedBy           int64
├── InvitationExpiresAt *time.Time
└── GrantSuperuser      int       `gorm:"default:0"`

唯一索引修复（关键）：
  当前：uniqueIndex:idx_user_org（普通唯一索引）
  改为：AutoUpgrade 创建部分唯一索引
        CREATE UNIQUE INDEX idx_member_user_org
        ON members (user_id, org_id)
        WHERE deleted_at IS NULL
  原因：支持软删除后重新邀请同一用户到同一组织
```

**新建**：`cpgateway-go/src/model/region.go`
```
字段：
├── UUID              string (unique)
├── Name              string (unique)
├── DisplayName       string
├── InternalEndpoint  string
├── InternalSecret    string
├── IsAvailable       bool    (default: true)
├── MaintenanceMode   bool    (default: false)
├── Description       string
├── LastCheckAt       *time.Time
├── StatusMessage     string
└── FailCount         int
```

**新建**：`cpgateway-go/src/model/token_revocation.go`
```
字段：
├── JTI       string (PK)
└── ExpiresAt time.Time
```

#### 1.4 认证 API Handler

**新建文件**：`cpgateway-go/src/apis/auth.go`

| 路由 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/v1/auth/register` | POST | Public | 用户注册（创建 user + org + 配额） |
| `/api/v1/auth/activate` | GET | Public | 邮件激活 |
| `/api/v1/auth/token` | POST | Public | 登录，返回 JWT |
| `/api/v1/auth/token/form` | POST | Public | OAuth2 表单登录（兼容旧客户端） |
| `/api/v1/auth/token/revoke` | POST | Auth | 吊销当前 token（写入 token_revocations） |
| `/api/v1/auth/me` | GET | Auth | 当前用户信息 |
| `/api/v1/auth/me/orgs` | GET | Auth | 当前用户所属组织列表 |
| `/api/v1/auth/switch-org` | POST | Auth | 切换组织（吊销旧 token，签发新 token） |
| `/api/v1/auth/switch-region` | POST | Auth | 切换区域（吊销旧 token，签发新 token） |
| `/api/v1/auth/invitation/info` | GET | Public | 查询邀请详情（验证 token，返回组织名+邀请人） |
| `/api/v1/auth/invitation/accept` | POST | Public | 接受邀请（创建/激活用户，标记成员为 ACCEPTED） |
| `/api/v1/auth/public-key` | GET | Public | 返回 RS256 公钥（供外部服务验证 JWT） |

**新建文件**：`cpgateway-go/src/services/auth.go`

**注册逻辑（register_user）**：
1. 先检查 org slug 唯一性（先于用户检查）
2. 如果 email 已存在但 `is_active=false`：**更新**已有用户字段（不新建），复用 placeholder
3. 创建 User（`status=Dormant`, 未激活）
4. 创建 Organization（`status=Pending`，非 Active）
5. 创建 Member（`role=Admin`）
6. 初始化所有区域的配额 + 消费记录
7. 发送激活邮件（激活链接 = `{FRONTEND_URL}/activate?token={HS256_token}`，24h 过期）
8. 以上步骤在**同一事务**内原子提交
9. 异步推送 Org 到所有区域

**激活逻辑（activate_user）**：
- **幂等**：已激活的用户再次调用不报错，直接返回成功
- 原子更新：`user.status → Active` + `org.status → Active`（仅激活注册时创建的那个 PENDING org）

**登录逻辑（login_user）**：
- Org 解析优先级：请求指定 org_uuid > 用户最早创建的 org（`MIN(created_at)`）
- Region 解析优先级：请求指定 region UUID > 第一个 is_available=true 的区域
- SystemAdmin **跳过** membership 验证（可访问任何 org）
- `is_owner` = `org.owner_user_id == user.id`
- 登录成功后**异步触发消费对账**（见 2.6）

**切换组织（switch_org）**：
- 重新验证 membership（防止已被移除的用户切换）
- 重新从 DB 查询最新 user/org 状态（不信任旧 claims）
- 吊销旧 token → 签发新 token（`on_conflict_do_nothing` 防重复 jti）

**切换区域（switch_region）**：
- 同样重新验证 membership + 查 DB 最新状态
- 吊销旧 token → 签发新 token

**Token 吊销**：
- 写入 `token_revocations` 表（`jti` + `expires_at` = token 原始过期时间，用于后续清理过期记录）

#### 1.5 用户管理 API Handler

**新建文件**：`cpgateway-go/src/apis/user_mgmt.go`

| 路由 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/v1/users` | GET | SystemAdmin | 用户列表 |
| `/api/v1/users/:uuid` | GET | SystemAdmin | 用户详情 |
| `/api/v1/users/:uuid/enable` | PUT | SystemAdmin | 启用 |
| `/api/v1/users/:uuid/disable` | PUT | SystemAdmin | 禁用 |
| `/api/v1/users/:uuid/demote` | PUT | SystemAdmin | 降级 |
| `/api/v1/users/:uuid/promote` | PUT | SystemAdmin | 升级 |
| `/api/v1/users/:uuid` | DELETE | SystemAdmin | 软删除用户 |
| `/api/v1/users/:uuid/profile` | PATCH | Self/SystemAdmin | 更新用户资料 |
| `/api/v1/users/:uuid/password` | PUT | Self/SystemAdmin | 修改密码 |

**业务约束**：
- **Enable**：根据用户是否有 membership 决定状态（有 → `Active`，无 → `Dormant`）
- **Disable**：不能禁用自己；设置 `status=Disabled`
- **Demote**：不能降级自己；系统必须**至少保留 2 个 SystemAdmin** 才允许降级
- **Delete**：不能删除自己；如果用户拥有**含其他成员的组织**则拒绝（防 ownership 孤儿）；软删除时 email/username 改名（`del{timestamp}+{email}`、`{username}_del{timestamp}`）释放唯一约束；同时软删除所有 membership
- **Profile**：普通用户只能改自己的 `first_name`/`last_name`/`language`；`remark` 字段仅 SystemAdmin 可设置
- **Password**：普通用户改自己需验证旧密码；SystemAdmin 重置他人密码不需旧密码

#### 1.6 组织管理 API Handler

**新建文件**：`cpgateway-go/src/apis/org_mgmt.go`

| 路由 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/v1/orgs` | POST | SystemAdmin | 创建组织（异步推送到所有区域） |
| `/api/v1/orgs` | GET | Auth | 列表（管理员看全部，普通用户看自己的） |
| `/api/v1/orgs/:uuid` | GET | Auth | 详情 |
| `/api/v1/orgs/:uuid` | PATCH | OrgAdmin | 更新名称/描述 |
| `/api/v1/orgs/:uuid` | DELETE | SystemAdmin | 软删除（slug 改名释放唯一性） |
| `/api/v1/orgs/:uuid/status` | PATCH | SystemAdmin | 更新状态（PENDING/ACTIVE/SUSPENDED/DISABLED） |
| `/api/v1/orgs/:uuid/transfer-owner` | POST | SystemAdmin | 转让所有权 |
| `/api/v1/orgs/:uuid/members` | GET | OrgAdmin | 成员列表 |
| `/api/v1/orgs/:uuid/members` | POST | SystemAdmin | 直接添加成员（跳过邀请） |
| `/api/v1/orgs/:uuid/members/:user_uuid` | PATCH | OrgAdmin | 更新成员角色 |
| `/api/v1/orgs/:uuid/members/:user_uuid` | DELETE | OrgAdmin | 移除成员 |
| `/api/v1/orgs/:uuid/invitations` | POST | OrgAdmin | 发送邀请（创建或复用 INVITED 用户） |
| `/api/v1/orgs/:uuid/invitations` | GET | OrgAdmin | 邀请列表 |
| `/api/v1/orgs/:uuid/invitations/:inv_uuid` | DELETE | OrgAdmin | 取消邀请（软删除 member 记录） |

**业务约束**：
- **Create**：管理员创建的 org 直接 `status=Active`（区别于用户注册时的 `Pending`）；原子初始化所有区域配额；异步推送到所有区域
- **List**：SystemAdmin 看所有非删除 org；普通用户只看自己有 membership 的 org（JOIN members 表）
- **Update**：只能改 name/description，**不能**改 org_type/slug/status
- **Delete**：不能删除 `SYSTEM` 类型的 org；slug 改名为 `{slug}_del{timestamp}` 释放唯一约束
- **Status**：不能手动设为 `Pending`（保留给注册流程）；不能修改 `SYSTEM` org 的状态
- **Transfer-owner**：新 owner 必须是该 org 的现有成员
- **Direct Add Member**：仅 SystemAdmin 可用（普通管理员需走邀请流程）
- **Invite**：如果目标 email 对应的用户不存在，创建 placeholder 用户（`username=_inv_{random_hex}`，`status=Invited`）；如果目标用户已是 active member 则拒绝；同一 user+org 的旧 pending 邀请自动取消
- **Invite Superuser**：`grant_superuser=1` 仅在 SYSTEM org 中允许
- **Cancel Invitation**：标记 member `status=Cancelled` + 软删除；如果被邀请的 placeholder 用户（status=Invited）**没有任何其他 membership**，则**硬删除**该 placeholder 用户（防孤儿）

**新建文件**：`cpgateway-go/src/services/invitation.go`

**接受邀请（accept_invitation）**：
- 验证 token 有效性 + 未过期（过期时主动标记 `status=Expired`）
- **新用户（status=Invited）**：必须提供 username + password → 更新 placeholder 用户为正式用户
- **未激活用户（is_active=false 且非 Invited）**：同样需要 username + password
- **已有活跃用户（status=Active）**：忽略 username/password，直接关联
- `grant_superuser=1` 时同时设置 `SystemRole=Admin`
- 标记 member `invitation_status=Accepted`

#### 1.7 区域管理 API Handler

**新建文件**：`cpgateway-go/src/apis/region.go`

| 路由 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/v1/regions` | POST | SystemAdmin | 注册区域（触发冷启动配置推送，见 2.7） |
| `/api/v1/regions` | GET | Auth | 列表（不返回 internal_secret） |
| `/api/v1/regions/:uuid` | GET | SystemAdmin | 详情（含内部配置） |
| `/api/v1/regions/:uuid` | PATCH | SystemAdmin | 更新（maintenance_mode、endpoint 等） |
| `/api/v1/regions/:uuid` | DELETE | SystemAdmin | 删除（需 maintenance_mode=true 且所有 org 消费量=0） |
| `/api/v1/regions/:uuid/rotate-secret` | POST | SystemAdmin | 轮转密钥 |

**业务约束**：
- **Region 名称校验**：`^[a-z0-9][a-z0-9-]*[a-z0-9]$`（小写字母+数字+连字符，最少 2 字符，不能以连字符开头/结尾）
- **Create**：`internal_secret` 传 `"auto-generate"` 时自动生成 64 字符 URL-safe token；初始 `is_available=false`；`display_name` 默认等于 `name`；原子初始化所有现有 org 的配额/消费记录；异步触发冷启动推送（见 2.7）
- **Update 状态机**：
  - 进入 `maintenance_mode=true` → 自动设 `is_available=false`
  - 设置 `is_available=true` → 自动退出 `maintenance_mode=false`
  - 区域从不可用恢复为可用 → 触发配置补推（填补离线期间的数据差异）
- **Delete**：必须先 `maintenance_mode=true`；必须该区域所有 org 的消费总量=0；清理对应的 `OrgResourceQuota` 和 `OrgResourceConsumption` 记录
- **Rotate-secret**：生成新的 64 字符 URL-safe token，返回一次后不再可见

#### 1.8 网关 Authorize 中间件

**新建文件**：`cpgateway-go/src/apis/authorize.go`

网关直接验证前端传来的 JWT，不走 Header 信任：

```
Authorization: Bearer <JWT>
→ 验证 RS256 签名 + 过期时间
→ 查询 token_revocations 检查吊销
→ 查询 DB 验证 user 存在（404 if missing）
→ 从 Claims 解析出 MemberShip（user_id, org_id, roles 等）
→ 注入 context
```

**Org 状态两级过滤**（两个不同的中间件/依赖）：
- `get_current_org()`：允许 `Active` + `Suspended`（读操作可用）
- `get_current_active_org()`：仅允许 `Active`（写操作需要，`Suspended` 的 org 拒绝写）
- `Pending` 和 `Disabled` 在两级中都被拒绝

**权限判定**：`is_superuser` 和 `system_role == Admin` 两个条件**取 OR**（兼容旧数据）

> 区域 clapi 的 `Authorize()` 中间件**不改动**，继续信任网关注入的 `X-*` Header。

#### 1.9 邮件发送模块

**新建文件**：`cpgateway-go/src/common/email.go`

```
功能清单：
├── SendActivationEmail(to, activationToken string)
├── SendInvitationEmail(to, orgName, invitationToken string)
└── 内部实现：net/smtp 或 github.com/wneessen/go-mail
```

**模板**：从 `cpgateway/app/templates/` 迁移到 `cpgateway-go/templates/`，改用 Go `html/template`。

#### 1.10 阶段 1 完成标准

- [ ] 用户可以通过 CPGateway-Go 注册、激活、登录、获取 JWT
- [ ] JWT 可以被 CPGateway-Go 直接验证（不依赖 Python CPGateway）
- [ ] 组织 CRUD、成员管理、邀请流程全部在 CPGateway-Go 工作
- [ ] 区域管理在 CPGateway-Go 工作
- [ ] Python CPGateway 可以选择性关闭认证相关路由
- [ ] 前端只需更改 API 端点即可切换

---

### 阶段 2：反向代理与配额引擎

**目标**：CPGateway-Go 具备请求代理能力和配额管控，完全替代 Python CPGateway 的网关功能。

#### 2.1 配额模型

**新建**：`cpgateway-go/src/model/org_resource_quota.go`
```
字段：
├── OrgID       int64
├── RegionID    int
├── MaxCpuCores float64
├── MaxRamGB    float64
├── MaxPublicIPs int
└── MaxDiskGB   float64
Unique: (OrgID, RegionID)
```

**新建**：`cpgateway-go/src/model/org_resource_consumption.go`
```
字段：
├── OrgID     int64
├── RegionID  int
├── CpuCores  float64
├── RamGB     float64
├── PublicIPs  int
└── DiskGB    float64
Unique: (OrgID, RegionID)
```

#### 2.2 配额服务

**新建文件**：`cpgateway-go/src/services/quota.go`

```
核心函数：
├── InitializeOrgQuotas(ctx, orgID)      — 创建 org 时初始化所有区域配额
├── InitializeRegionQuotas(ctx, regionID) — 创建区域时为所有 org 初始化配额
├── CheckAndReserve(ctx, orgID, regionID, amount)
│   └── SELECT FOR UPDATE 锁行 → 检查 current+request ≤ limit → 预扣
├── Release(ctx, orgID, regionID, amount)
│   └── max(0, current - amount) 防负值
├── QueryResourceAmount(region, path, resourceID, headers)
│   └── 向区域后端 GET 资源详情，提取 cpu/memory/disk
│   └── floating_ips 不查询，固定返回 {"public_ips": 1}
│   └── 查询失败返回 502（防止配额静默泄漏）
└── QueryFlavorAmount(region, flavorID, headers)
    └── cpu: "cpu" 或 "vcpus" 字段
    └── memory: "memory" 或 "ram" 字段，**除以 1024**（MB → GB）
    └── disk: "disk" 字段
```

**Instance 资源量提取优先级**：
- 请求 body 中直接指定 `cpu`/`memory`/`disk` > 通过 `flavor_id` 查询 flavor 规格
- `memory` 字段单位是 MB，存储和比较时需**除以 1024 转 GB**
- `disk` 不存在时默认 0

**配额规则表**（从 Python CPGateway 迁移）：
```go
var quotaRules = map[QuotaRuleKey]string{
    {"POST", "instances"}:    "consume",
    {"POST", "volumes"}:      "consume",
    {"POST", "floating_ips"}: "consume",
    {"DELETE", "instances"}:   "release",
    {"DELETE", "volumes"}:     "release",
    {"DELETE", "floating_ips"}: "release",
    {"PUT", "instances"}:      "resize",
    {"PUT", "volumes"}:        "resize",
}
// 路径匹配是**前缀匹配**，如 "instances" in path
```

**Resize 差值计算逻辑（关键）**：
```
Instance resize:
1. 从请求 body 提取 new_amount（cpu/ram_gb/disk_gb）
2. 向区域 GET /instances/{id} 查询 old_amount
3. 逐字段计算 diff = new - old
4. diff > 0 的字段：CheckAndReserve（预扣增量）
5. diff < 0 的字段：记录 shrink_amount（绝对值）
6. 后端成功后：Release(shrink_amount)（释放缩减部分）
7. 后端失败后：Release(reserve_amount)（回滚增量预扣）

Volume resize:
1. new_size 从请求 body 获取
2. old_size 从区域 GET /volumes/{id} 获取
3. delta = new_size - old_size → CheckAndReserve/Release
```

**配额默认值三级回退**：
1. `system_settings` 表中的配置
2. 环境变量（`DEFAULT_CPU_CORES` 等）
3. 硬编码默认值（cpu=4, ram=8, ips=2, disk=50）

#### 2.3 反向代理层

**新建文件**：`cpgateway-go/src/apis/proxy.go`

CPGateway-Go 需要将资源操作请求**代理到区域 clapi**（与当前 Python CPGateway 行为完全一致）。使用 Go 标准库 `net/http/httputil.ReverseProxy`。

```
代理流程（CPGateway-Go → 区域 clapi）：
1. Authorize 中间件已验证 JWT → MemberShip 已在 context 中
2. 从 JWT Claims 解析 region UUID → 查询 Region 表 → 获取 internal_endpoint
3. 检查 org 状态（SUSPENDED 拒绝写操作）
4. 配额预处理（见 2.4）
5. 构建 X-* 转发头
6. 移除 hop-by-hop 头
7. httputil.ReverseProxy 转发到 {region.internal_endpoint}/api/v1/{path}
8. 配额后处理（见 2.4）
```

**注入的转发头**（与 Python CPGateway 完全一致，区域 clapi 依赖这些头做权限判断）：
```
X-User-ID:          内部整数 ID（不是 UUID）
X-User-Email:       用户邮箱
X-Org-ID:           内部整数 ID（不是 UUID）
X-Org-Name:         组织名称
X-Org-Role:         整数枚举值（0=None, 1=Reader, 2=Writer, 3=Admin）
X-Is-Owner:         "true" / "false"（小写字符串）
X-System-Role:      整数（0=User, 1=Admin）
X-Forwarded-Secret: 区域的 internal_secret
```

**移除的 hop-by-hop 头**：`Connection`, `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`, `TE`, `Trailers`, `Transfer-Encoding`, `Upgrade`, `Host`, `Content-Length`, `Authorization`

**超时配置**：
- 代理转发超时：30s（`PROXY_TIMEOUT_SECONDS`）
- 配额查询超时：10s（`BACKEND_REQUEST_TIMEOUT`，查 flavor/resource 用）

**错误处理**：
- 后端返回 400+：回滚配额预扣
- 网络错误（连接超时/拒绝）：回滚配额预扣
- DELETE 成功（200/201/204）：释放配额
- RESIZE 成功：释放缩减部分的配额

**路由注册**（按权限分层）：

```go
proxyHandler := NewRegionProxy()

// 需要登录的资源操作（所有用户）
authGroup := v1.Group("", AuthMiddleware())
{
    authGroup.Any("/instances/*path", proxyHandler.Forward)
    authGroup.Any("/volumes/*path", proxyHandler.Forward)
    authGroup.Any("/backups/*path", proxyHandler.Forward)
    authGroup.Any("/flavors/*path", proxyHandler.Forward)
    authGroup.Any("/images/*path", proxyHandler.Forward)
    authGroup.Any("/subnets/*path", proxyHandler.Forward)
    authGroup.Any("/vpcs/*path", proxyHandler.Forward)
    authGroup.Any("/floating_ips/*path", proxyHandler.Forward)
    authGroup.Any("/security_groups/*path", proxyHandler.Forward)
    authGroup.Any("/ip_groups/*path", proxyHandler.Forward)
    authGroup.Any("/load_balancers/*path", proxyHandler.Forward)
    authGroup.Any("/keys/*path", proxyHandler.Forward)
    authGroup.Any("/zones/*path", proxyHandler.Forward)
    authGroup.Any("/dictionaries/*path", proxyHandler.Forward)
    authGroup.Any("/migrations/*path", proxyHandler.Forward)
    authGroup.Any("/tasks/*path", proxyHandler.Forward)
    authGroup.Any("/addresses/*path", proxyHandler.Forward)
    authGroup.GET("/version", proxyHandler.Forward)
    // 告警 & 监控
    authGroup.Any("/alarm/*path", proxyHandler.Forward)
    authGroup.Any("/metrics/*path", proxyHandler.Forward)
}

// 需要管理员权限的操作
adminGroup := v1.Group("", AuthMiddleware(), AdminRequired())
{
    adminGroup.Any("/hypers/*path", proxyHandler.Forward)
    adminGroup.Any("/node-alarm-rules/*path", proxyHandler.Forward)
}
```

**代理层特殊拦截逻辑**：

| 拦截点 | 说明 |
|--------|------|
| `POST /instances` 中 `hypervisor` 字段 | 非 SystemAdmin 请求 body 含 `hypervisor` 字段时返回 403（防止普通用户钉选物理机） |
| org 状态检查 | SUSPENDED 状态的 org 拒绝所有写操作（POST/PUT/PATCH/DELETE） |

#### 2.4 配额检查中间件

> **实际实现（已调整）**：未使用 Gin 中间件。中间件先于代理 handler 执行，此时还没有解析出区域；现在配额在代理 handler 内按 Python 流程执行：`services.PrepareQuota`（预扣或查询资源量）→ 转发 → `services.FinishQuota`（按后端状态码结算），行锁使用 `clause.Locking`。下方中间件代码仅保留为原设计参考。

CPGateway 的配额引擎在代理层工作——拦截请求，预扣配额，根据后端响应状态码回滚。CPGateway-Go 使用 **Gin 中间件** 复现这一模式（与 Python CPGateway 一致，统一拦截，不侵入区域 clapi 业务代码）。

网关模式的中间件实现：

```go
func QuotaEnforcement() gin.HandlerFunc {
    return func(c *gin.Context) {
        rule := matchQuotaRule(c.Request.Method, extractResource(c.FullPath()))
        if rule == "" {
            c.Next()
            return
        }
        
        // Pre-handler: 预扣配额
        reserved, amount, err := quotaService.CheckAndReserve(ctx, orgID, regionID, ...)
        if err != nil {
            ErrorResponse(c, http.StatusTooManyRequests, "quota exceeded", err)
            c.Abort()
            return
        }
        
        c.Next() // 执行代理转发
        
        // Post-handler: 根据响应状态码决定确认或回滚
        if c.Writer.Status() >= 400 && reserved {
            quotaService.Release(ctx, orgID, regionID, amount) // 回滚
        }
        // DELETE 成功时释放配额
        if rule == "release" && c.Writer.Status() < 300 {
            quotaService.Release(ctx, orgID, regionID, amount)
        }
    }
}
```

#### 2.5 区域心跳服务

**新建文件**：`cpgateway-go/src/services/heartbeat.go`

```
功能：
├── goroutine 定期检查所有 Region 的健康状态
├── GET {region.internal_endpoint}/api/v1/version
├── 连续失败 N 次 → 标记 is_available=false
├── 恢复后自动标记 is_available=true
└── 新区域首次上线 → 触发冷启动配置推送（见 2.7）
```

#### 2.6 消费异步对账服务

**新建文件**：`cpgateway-go/src/services/consumption_sync.go`

**背景**：配额消费数据（`OrgResourceConsumption`）可能因网络异常、后端失败等原因与区域实际资源不一致。需要定期对账校准。

```
触发时机：
└── 用户登录时，如果该 org-region 上次对账超过 1 小时，启动后台 goroutine 对账

对账流程：
1. 并发查询区域 clapi：GET /instances、GET /volumes、GET /floating_ips（带 X-Org-ID）
2. 聚合实际用量：CPU cores、RAM GB、Disk GB、Public IP count
3. SELECT FOR UPDATE 锁定 OrgResourceConsumption 行
4. 更新为实际用量值
5. 记录对账日志（旧值 vs 新值）

去重机制：
└── 内存 sync.Map 记录正在对账的 orgID-regionID 对，防止并发重复对账
```

#### 2.7 区域冷启动配置推送

**新建文件**：`cpgateway-go/src/services/region_provisioning.go`

**背景**：新区域注册后，区域 clapi 的 users/organizations/members/settings/notification_channels 表为空，需要从网关推送初始数据。

```
触发时机：
├── 新区域注册（POST /regions）
└── 心跳服务检测到区域从不可用恢复为可用

推送内容（按顺序）：
1. POST {region}/api/v1/internal/orgs/sync           — 推送所有组织
2. POST {region}/api/v1/internal/system-settings/sync — 推送系统设置
3. POST {region}/api/v1/internal/notification-channels/sync — 批量推送所有通知频道
   payload: { "action": "bulk_sync", "channels": [...] }

推送策略：
├── 异步 goroutine 执行，不阻塞主请求
├── 每次推送失败重试 3 次，指数退避（2s → 4s → 8s）
└── 记录推送结果日志
```

#### 2.8 异步同步通用模式

以上多个同步服务（OrgSync、SettingsSync、NotificationSync、ConsumptionSync）共享一套通用模式：

**新建文件**：`cpgateway-go/src/services/sync_common.go`

```
通用模式：
├── FanOutToAllRegions(ctx, fn) — 查询所有 is_available=true 的区域，并发执行 fn
├── RetryWithBackoff(ctx, fn, maxRetries=3) — 指数退避重试（2s → 4s → 8s）
├── 所有同步请求携带 X-Forwarded-Secret header（区域 clapi 认证用）
└── 超时控制：单次请求默认 10s 超时
```

#### 2.9 阶段 2 完成标准

- [ ] 配额预扣/回滚在 CPGateway-Go 端工作
- [ ] 资源 CRUD 操作自动执行配额检查（通过中间件）
- [ ] 代理层特殊拦截逻辑（hypervisor 钉选、org 状态检查）工作
- [ ] 区域心跳在 CPGateway-Go 运行
- [ ] 消费异步对账在登录时触发
- [ ] 新区域注册后自动推送初始配置
- [ ] Python CPGateway 可以完全关闭

---

### 阶段 3：辅助功能迁移

**目标**：迁移剩余的辅助功能，确保功能完整。

#### 3.1 资源配额管理 API

**新建文件**：`cpgateway-go/src/apis/resource_quota.go`

| 路由 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/v1/resources/quota/:org_uuid` | GET | Member/SystemAdmin | 获取 org 所有区域配额 |
| `/api/v1/resources/quota/:org_uuid/:region_uuid` | GET | Member/SystemAdmin | 获取特定区域配额 |
| `/api/v1/resources/quota/:org_uuid/:region_uuid` | PUT | SystemAdmin | 更新配额限制 |
| `/api/v1/resources/consumption/:org_uuid` | GET | Member/SystemAdmin | 获取所有区域用量 |
| `/api/v1/resources/consumption/:org_uuid/:region_uuid` | GET | Member/SystemAdmin | 获取特定区域用量 |
| `/api/v1/resources/info/:org_uuid` | GET | Member/SystemAdmin | 配额+用量汇总（所有区域） |
| `/api/v1/resources/info/:org_uuid/:region_uuid` | GET | Member/SystemAdmin | 配额+用量汇总（特定区域） |

#### 3.2 系统设置

**新建文件**：`cpgateway-go/src/apis/system_settings.go`

区域 clapi 已有 `SystemSettingMirror`（只读镜像）和 `/internal/system-settings/sync` 端点。CPGateway-Go 需要：

- **新建 `SystemSetting` 主表模型**（可读写，网关数据库），取代 Python CPGateway 的 `system_settings` 表
- 区域 clapi 的 `SystemSettingMirror` **不改动**：仍通过 `/internal/system-settings/sync` 接收推送
- 设置变更时异步推送到所有区域 clapi（通过 SettingsSyncService）

| 路由 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/v1/system/settings` | GET | SystemAdmin | 获取所有设置（密钥类值用 `******` 掩码） |
| `/api/v1/system/settings` | PUT | SystemAdmin | 批量更新设置 |
| `/api/v1/system/settings/test-notification` | POST | SystemAdmin | 测试通知渠道连通性 |

**业务细节**：
- **GET**：`is_secret=true` 的字段返回 `"******"` 掩码值
- **PUT**：值为 `"******"` 的字段**跳过更新**（保留数据库中的实际值）；每次更新自增 `config_version`；更新完成后异步推送到所有区域
- **Settings 分类**：`general`（通用）、`quota`（配额默认值）、`notification`（通知配置）
- **Test-notification 支持 4 种渠道**：
  - 邮件：SMTP 连接测试（支持 SSL:465 / STARTTLS:587 / 明文 三种模式）
  - 飞书：POST webhook（如果配了 secret 则附 HMAC-SHA256 签名）
  - Slack：标准 webhook POST
  - 自定义 Webhook：支持 GET/POST/PUT/PATCH 方法（验证方法合法性）

#### 3.3 Infrastructure 端点

**新建文件**：`cpgateway-go/src/apis/infrastructure.go`

| 路由 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/v1/system/infrastructure` | GET | SystemAdmin | 获取区域的运行时基础设施配置（S3、SCI 等），透传到区域 clapi 内部 API |
| `/api/v1/system/infrastructure/test-s3` | POST | SystemAdmin | 测试区域 S3 存储连通性 |

#### 3.4 通知频道管理

**新建文件**：`cpgateway-go/src/apis/notification_channels.go`

区域 clapi 已有 `NotificationChannel` 模型和同步端点。CPGateway-Go 管理主数据，变更后异步推送到所有区域。

| 路由 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/v1/notification-channels` | GET | Auth | 列表（按当前 org 过滤） |
| `/api/v1/notification-channels` | POST | Auth | 创建频道（异步推送 upsert 到所有区域） |
| `/api/v1/notification-channels/:uuid` | GET | Auth | 详情 |
| `/api/v1/notification-channels/:uuid` | PUT | Auth | 更新频道（异步推送 upsert 到所有区域） |
| `/api/v1/notification-channels/:uuid` | DELETE | Auth | 删除频道（异步推送 delete 到所有区域） |

**通知类型支持**：
- 邮件（SMTP，支持 TLS/SSL 选项）
- 飞书 Webhook（HMAC-SHA256 签名：`sign = base64(hmac_sha256("timestamp\nsecret"))`，无 secret 时省略签名）
- Slack Webhook（标准 POST）
- 自定义 Webhook（GET/POST/PUT/PATCH，方法校验）

**通知内容国际化**：
- 激活邮件/飞书消息支持中英文（根据 `user.language` 字段，"zh" 或 "en"）
- 激活链接格式：`{FRONTEND_URL}/activate?token={token}`（24h 过期提示）
- 邀请链接格式：`{FRONTEND_URL}/invite/accept?token={token}`（区分已有用户和新用户的消息文本）

#### 3.5 告警汇总

**新建文件**：`cpgateway-go/src/apis/alarm_summary.go`

| 路由 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/v1/alarm/summary` | GET | Auth | 跨区域告警聚合（并发查询所有活跃区域，2s 超时，不可达返回 -1） |

#### 3.6 启动初始化逻辑

**新建文件**：`cpgateway-go/src/services/init.go`

CPGateway-Go 启动时执行（与 Python CPGateway `main.py` 的 startup event 对应）：

```
启动流程：
1. GORM AutoMigrate 创建/更新所有表
2. 注册 HTTP 请求日志中间件（记录 Method、Path、StatusCode、Duration）
3. 检查并创建 root 超级管理员（[superuser] 配置节，is_superuser=true, system_role=Admin, status=Active）
4. 检查并创建 "admin" 系统组织（OrgType=SYSTEM, slug="admin", status=Active）
5. 确保 root 用户是 admin 组织的 ADMIN 成员
6. 启动区域心跳 goroutine（后台持续运行）
```

#### 3.7 阶段 3 完成标准

- [ ] 资源配额管理 API 完整（7 个端点）
- [ ] 系统设置 CRUD + 测试通知 + 异步推送到区域
- [ ] Infrastructure 端点（运行时配置查看 + S3 测试）
- [ ] 通知频道 CRUD + 自动同步到区域
- [ ] 告警汇总跨区域聚合
- [ ] 启动初始化逻辑（root 用户、admin 组织）
- [ ] 所有 Python CPGateway 的 API 端点在 CPGateway-Go 有对应实现
- [ ] 前端无需修改即可切换到 CPGateway-Go
- [ ] Python CPGateway 代码归档

---

## 5. 数据迁移方案

**不适用**：CPGateway-Go 部署到全新环境，不迁移 Python CPGateway 的数据（原迁移流程与字段映射已删除）。首次启动由 GORM AutoMigrate 建表，启动初始化创建 root 用户和 `admin` 系统组织。

---

## 6. 前端适配

### 6.1 API 端点变更

如果 Go API 和 CPGateway 监听不同端口，前端需要调整 `VITE_API_PROXY_TARGET`。

**推荐方案**：CPGateway-Go 监听 `:8000`（接管 Python CPGateway 的端口），前端零修改。

### 6.2 响应格式兼容

确保 CPGateway-Go 的响应 JSON 结构与 Python CPGateway 一致：
- 字段命名：snake_case（Gin 默认 `json:"field_name"` tag）
- 错误格式：**沿用 FastAPI 的 `{"detail": ...}`**（429 配额超限为 `{"detail": {"error": "quota_exceeded", ...}}`），前端 `api/client.ts` 依赖 `detail`，无需修改

### 6.3 Token 格式

JWT Claims 结构保持不变，前端无需修改 token 解析逻辑。

---

## 7. 部署变更

### 7.1 Docker Compose 变更（已实施）

- `cpgateway` 服务改为 Go 镜像：`deploy/docker/dockerfiles/Dockerfile.cpgateway-go`（多阶段构建，`CGO_ENABLED=0`，运行时 ubuntu:22.04，`GIN_MODE=release`，健康检查 `/health`）；入口脚本 `deploy/docker/scripts/cpgateway-go-entrypoint.sh`（等待数据库、缺失时生成 RSA 密钥对）
- 服务名、容器名、IP `172.28.0.96`、端口 `8000`、密钥卷 `cpgateway_keys` 保持不变，nginx 无需修改
- 数据库沿用 `init-db.sql` 创建的 `cloudland_cpgateway`，表由 AutoMigrate 创建；不再需要 alembic 和 `cpgateway_data` 卷
- 区域 clapi 不改动；注册区域时 `internal_secret` 必须等于 clapi 的 `CPGATEWAY_SECRET_KEY`

### 7.2 配置

配置文件为 `cpgateway-go/conf/config.toml`（viper 加载），任意键都可用环境变量覆盖：`.` 换成 `_` 后大写，如 `db.uri` → `DB_URI`。compose 中的映射：

| 环境变量 | 配置键 | 取值（.env） |
|---------|-------|-------------|
| `DB_TYPE` / `DB_URI` | `db.type` / `db.uri` | `postgres` / `postgres://…/${CPGATEWAY_DB}` |
| `AUTH_SECRET_KEY` | `auth.secret_key` | `CPGATEWAY_SECRET_KEY` |
| `AUTH_RSA_PRIVATE_KEY_PATH` / `AUTH_RSA_PUBLIC_KEY_PATH` | `auth.rsa_private_key_path` / `auth.rsa_public_key_path` | `/app/keys/private.pem` / `/app/keys/public.pem` |
| `SUPERUSER_EMAIL` / `SUPERUSER_USERNAME` / `SUPERUSER_PASSWORD` | `superuser.*` | `ADMIN_EMAIL` / `CPGATEWAY_ADMIN_USER` / `ADMIN_PASSWORD` |
| `FRONTEND_URL`、`SMTP_*`、`FEISHU_*`、`DNS_UPSTREAM` | `frontend.url`、`smtp.*`、`feishu.*`、`dns.upstream` | 同名变量，仅作系统设置的默认值 |
| `SERVER_LISTEN_ADDR` / `SERVER_LISTEN_PORT` | `server.listen_addr` / `server.listen_port` | `0.0.0.0` / `8000` |

其余可调项（`proxy.*` 超时、`quota.defaults.*`、`heartbeat.*`、`auth.access_token_expire_minutes`）见 `config.toml`。系统设置（SMTP、飞书、默认配额、FRONTEND_URL 等）以数据库中保存的值优先，配置文件和环境变量只作兜底。

---

## 8. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| 功能回归 | 用户无法登录/操作 | `parity_test.go` 覆盖主流程，CI 在 SQLite 和 PostgreSQL 上各跑一遍；出问题回滚到上一个镜像版本 |
| JWT 密钥丢失 | 已签发 token 全部失效 | 密钥保存在 `cpgateway_keys` 卷中，重建容器不会重新生成 |
| SQLite 与 PostgreSQL 行为差异 | 上线后建表或 SQL 报错 | CI 使用 PostgreSQL 14 服务容器运行同一套测试 |
| 配额计算偏差 | 资源超卖或误拒 | 单元测试覆盖所有配额场景，对照 Python 实现逐一验证 |
| 前端适配遗漏 | 部分功能不可用 | 整理完整 API 兼容性矩阵，逐端点验证 |

---

## 9. 工作量估算

| 阶段 | 主要工作 | 预估工作量 |
|------|---------|----------|
| 阶段 1 | JWT、密码、用户/组织/区域 CRUD、邮件、启动初始化 | 2-3 周 |
| 阶段 2 | 反向代理、配额引擎、消费对账、心跳、区域冷启动、同步服务 | 2-3 周 |
| 阶段 3 | 系统设置、Infrastructure、通知频道、告警汇总、前端适配、部署 | 1-2 周 |
| 测试与调试 | 端到端测试、PostgreSQL 实测、多区域联调 | 1 周 |
| **合计** | | **6-9 周**（单人全职） |

---

## 10. Go 依赖

CPGateway-Go 是独立项目，有自己的 `go.mod`。实际使用的主要依赖：

| 依赖 | 用途 | 说明 |
|------|------|------|
| `github.com/gin-gonic/gin` | HTTP 路由框架 | 与 clapi 保持一致 |
| `gorm.io/gorm` + `gorm.io/driver/postgres` | ORM / PostgreSQL | 生产使用 |
| `gorm.io/driver/sqlite` | SQLite | 仅单元测试（需 CGO） |
| `github.com/golang-jwt/jwt/v4` | JWT 签发/验证 | |
| `golang.org/x/crypto/bcrypt` | 密码哈希 | |
| `github.com/google/uuid` | UUID 生成 | |
| `github.com/spf13/viper` | 配置加载 + 环境变量覆盖 | |
| `github.com/sirupsen/logrus` | 日志 | |

邮件使用标准库 `net/smtp`（未引入 go-mail），配置解析使用 viper（未引入 BurntSushi/toml）。

---

## 11. 文件清单总结

### 新建项目：`cpgateway-go/`

```
cpgateway-go/
├── go.mod / go.sum / Makefile
├── main.go                     # 入口：加载配置 → AutoMigrate → 初始化 root 用户 → 心跳 → Gin :8000
├── conf/
│   └── config.toml             # 网关配置（环境变量可覆盖）
├── src/
│   ├── common/
│   │   ├── jwt.go              # JWT 签发/验证（RS256 + HS256）
│   │   ├── password.go         # bcrypt 密码哈希
│   │   ├── email.go            # SMTP 发送（465 隐式 TLS / STARTTLS / 明文）
│   │   └── httperr.go          # FastAPI 兼容错误响应 {"detail": ...}
│   ├── model/                  # user / org / member / region / token_revocation /
│   │                           # org_resource_quota / org_resource_consumption /
│   │                           # system_setting / notification
│   ├── dbs/
│   │   ├── db.go               # GORM 连接、AutoMigrate、AutoUpgrade（测试可用 CPGATEWAY_TEST_DB_URI 切到 PostgreSQL）
│   │   └── config.go
│   ├── apis/
│   │   ├── routes.go              # 路由注册
│   │   ├── authorize.go           # ClaimsAuth / ActiveUser / Superuser / CurrentOrg
│   │   ├── schemas.go             # 与 Python Pydantic schema 一致的响应结构
│   │   ├── auth.go                # 注册/激活/登录/切换/吊销/邀请信息与接受
│   │   ├── user_mgmt.go           # 用户管理（含前端编辑用户用的 PUT /users/:uuid）
│   │   ├── org_mgmt.go            # 组织、成员、邀请
│   │   ├── region_mgmt.go         # 区域管理（CRUD + 密钥轮转）
│   │   ├── resource_mgmt.go       # 配额/用量 API
│   │   ├── system_settings.go     # 系统设置 + 通知测试
│   │   ├── infrastructure.go      # 运行时配置查看 + S3 测试
│   │   ├── notification_channels.go
│   │   ├── alarm_summary.go       # 跨区域告警汇总
│   │   ├── proxy.go               # 代理转发 + 配额预扣/结算
│   │   ├── proxy_routes.go        # 150 条代理路由白名单（含管理员标记）
│   │   └── parity_test.go         # 端到端流程测试（SQLite / PostgreSQL）
│   └── services/
│       ├── init.go                # 启动初始化（root 用户、admin 组织）
│       ├── auth.go / invitation.go
│       ├── quota.go               # 配额引擎（PrepareQuota / FinishQuota）
│       ├── heartbeat.go           # 区域心跳 + 恢复时触发冷启动
│       ├── consumption_sync.go    # 登录触发的用量对账
│       ├── region_provisioning.go # 区域冷启动推送（渠道、设置、组织）
│       ├── org_sync.go / notification_channels.go / system_settings.go  # 推送到 clapi
│       ├── email.go               # 激活/邀请通知（邮件 + 飞书，中英文模板）
│       └── sync_common.go         # 推送重试、内网 HTTP 客户端、后端 URL 构建
```

### 区域 clapi（`api/`）不改动

CPGateway-Go 是完全独立的项目，区域 clapi 的代码**零修改**。

### 部署文件修改

```
deploy/docker/docker-compose.yml                    # cpgateway 服务改用 Go 镜像
deploy/docker/dockerfiles/Dockerfile.cpgateway-go   # 新增
deploy/docker/scripts/cpgateway-go-entrypoint.sh    # 新增（等待数据库、生成 RSA 密钥）
.github/workflows/cpgateway-go.yaml                 # 新增 CI（gofmt / vet / SQLite + PostgreSQL 测试）
```

---

## 12. 决策记录

| 决策点 | 选项 | 建议 | 理由 |
|--------|------|------|------|
| 迁移策略 | 一次性重写 vs 渐进式 | **渐进式** | 每个阶段独立可部署，降低风险 |
| 项目架构 | 合并到 clapi 同一二进制 vs 独立项目 | **独立项目（`cpgateway-go/`）** | 保持与当前 Python CPGateway 相同的部署拓扑，区域 clapi 零改动 |
| 代理层处理 | ReverseProxy vs http.Client | **http.Client 转发 + 路由白名单** | 需读取完整响应后结算配额；路由按 Python 逐条注册，避免暴露未开放的后端接口 |
| 配额检查方式 | Gin 中间件 vs 代理 handler 内执行 | **代理 handler 内执行**（原建议中间件） | 中间件先于 handler 执行、拿不到区域；与 Python 在转发流程中处理一致 |
| User 状态字段 | 新增 is_active/is_superuser vs 复用 Status/SystemRole | **保留 is_active/is_superuser**（原建议复用） | 只用 Status 无法区分“注册未激活”和“已激活但无组织”（Dormant） |
| 数据库 | 新建 vs 复用 | **网关独立库，全新环境不迁移数据** | 网关库存放全局数据，区域 clapi 不改动 |
| 错误格式 | 兼容 FastAPI vs 统一 Go 格式 | **兼容 FastAPI `{"detail"}`**（原建议统一 Go 格式） | 前端 `api/client.ts` 依赖 `detail`，前端零修改 |
| 端口 | 保持 :8000 vs 用新端口 | **保持 :8000** | 前端零修改 |
| 邮件库 | net/smtp vs go-mail | **net/smtp**（原建议 go-mail） | 只需 SMTP 发送（465 隐式 TLS / STARTTLS / 明文），无需额外依赖 |
| SystemSetting | 复用 Mirror vs 新建主表 | **新建主表 + 区域 Mirror 不变** | 网关端需要可读写的主表；区域端继续用 Mirror 接收同步 |
