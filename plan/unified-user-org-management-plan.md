# 统一用户与 Org 管理重构计划

## Context

当前架构中 Middle（Python/FastAPI）与 Cloudland（Go/Gin）各自维护用户体系，存在以下问题：

1. **数据碎片化**：每个 Region 的 Cloudland 有独立的 users/organizations/members 表，Middle 通过 `BackendAccount` 向各 Region 同步用户，导致 N 份用户数据难以保持一致
2. **Org 概念缺失**：Middle 没有 Org/Member 模型，Cloudland 有但仅限于 Region 内
3. **Token 不统一**：Middle 签发 HS256 JWT，Cloudland 签发 RS256 JWT，两套 Token 无法互通
4. **管理分散**：用户生命周期分散在 Middle + 多个 Cloudland 实例中

**目标**：
- Middle 成为全局控制平面，统一管理 User、Org、Member，作为唯一 Token 签发方
- Middle 同时作为 API Gateway，所有前端请求统一经过 Middle 转发到对应 Cloudland
- Cloudland 完全无状态，不存储 User/Org 数据，仅作内网资源平面，不对外暴露
- Org 是全局概念，跨所有 Region 共享，同一 Org 可在任意 Region 操作资源

---

## 整体架构

```
前端
  │
  │ 所有请求（含资源操作）
  ▼
cloudland-middle（全局控制平面 + API Gateway）
  ├── User Registry（全局唯一）
  ├── Org Registry（全局，跨 Region 共享）
  ├── Member Registry（User ↔ Org ↔ OrgRole）
  ├── JWT 签发（RS256，携带 org_id/region/roles）
  ├── JWT 验签（统一鉴权，不再依赖 Cloudland 验签）
  └── Reverse Proxy（根据 token.region 转发到对应 Cloudland 内网地址）
          │
          │ 内网转发（已鉴权，携带 X-User-ID / X-Org-ID / X-Org-Role Header）
          ▼
  Cloudland-A / B / C（Region 级资源平面，纯内网）
    ├── 信任 Middle 转发的 Header（不再自行验签 JWT）
    ├── 读取 X-User-ID / X-Org-ID / X-Org-Role
    ├── 资源表（org_id 仅作字符串标签，无 FK）
    └── 拒绝一切来自公网的直接请求
```

### 请求转发流程

```
前端 POST /api/v1/vms  (Header: Authorization: Bearer <token>)
  │
  ▼
Middle Reverse Proxy 层：
  1. 验签 JWT（RS256）
  2. 检查 jti 未被吊销
  3. 检查 token.region == 目标 Region
  4. 查 regions 表取 Cloudland 内网地址
  5. 转发请求到 Cloudland，去掉 Authorization Header
     改加：X-User-ID / X-Org-ID / X-Org-Role / X-Is-Owner
  │
  ▼
Cloudland（内网）：
  直接读取 Header，执行资源操作，返回结果
  │
  ▼
Middle 将响应透传给前端
```

---

## 第一部分：cloudland-middle 变更（Python/FastAPI）

### 1.1 数据模型新增

**新增 `app/models/org.py`**
```python
class OrgType(int, Enum):
    TEAM   = 1  # 普通团队 Org
    SYSTEM = 2  # 系统 Org（全局唯一，不可删除）

class Organization(Base):
    __tablename__ = "organizations"
    id            = Column(BigInteger, primary_key=True)
    uuid          = Column(UUID, unique=True, default=uuid4)
    name          = Column(String(255))
    slug          = Column(String(64))        # 唯一可读标识，创建后不可改
    org_type      = Column(Integer, default=OrgType.TEAM)
    owner_user_id = Column(BigInteger, ForeignKey("users.id"), nullable=False)
    created_at    = Column(DateTime, default=now)
    deleted_at    = Column(DateTime, nullable=True)
    # Unique index: slug WHERE deleted_at IS NULL
```

**新增 `app/models/region.py`**
```python
class Region(Base):
    __tablename__ = "regions"
    id                = Column(String(64), primary_key=True)  # e.g. "cn-north"
    display_name      = Column(String(128))
    internal_endpoint = Column(String(512))                   # Cloudland 内网地址（仅 Middle 转发用）
    internal_secret   = Column(String(256))                   # 每个 Region 独立共享密钥
    is_available      = Column(Boolean, default=True)
    created_at        = Column(DateTime, default=now)
```

**新增 `app/models/member.py`**
```python
class OrgRole(int, Enum):
    NONE   = 0
    READER = 1
    WRITER = 2
    ADMIN  = 3

class Member(Base):
    __tablename__ = "members"
    id         = Column(BigInteger, primary_key=True)
    user_id    = Column(BigInteger, ForeignKey("users.id"), nullable=False)
    org_id     = Column(BigInteger, ForeignKey("organizations.id"), nullable=False)
    org_role   = Column(Integer, default=OrgRole.READER)
    created_at = Column(DateTime, default=now)
    deleted_at = Column(DateTime, nullable=True)
    # Unique index: (user_id, org_id) WHERE deleted_at IS NULL
```

**修改 `app/models/user.py`**
```python
class SystemRole(int, Enum):
    USER  = 0
    ADMIN = 1

class UserStatus(int, Enum):
    ACTIVE   = 1  # 属于至少一个 Org
    DORMANT  = 2  # 不属于任何 Org，可登录但受限
    DISABLED = 3  # 被 SystemAdmin 禁用

# 新增字段：
system_role = Column(Integer, default=SystemRole.USER)
status      = Column(Integer, default=UserStatus.ACTIVE)
first_name  = Column(String(128), default='')
last_name   = Column(String(128), default='')
remark      = Column(String(512), default='')

# 废弃字段（迁移后删除）：
# username → 以 email 作为唯一登录标识
# BackendAccount 关联（Cloudland 无状态后不再需要同步）
```

---

### 1.2 JWT 统一（RS256，Middle 作为唯一签发方）

**修改 `app/core/security.py`**

```python
# 从 HS256 迁移到 RS256
# 私钥用于签发，公钥分发给各 Cloudland

class TokenClaims(BaseModel):
    sub: str          # global_user_id
    email: str
    org_id: str       # 当前操作的 Org
    region: str       # 目标 Region
    sr: int           # SystemRole (0=User, 1=Admin)
    or_: int          # OrgRole (0-3) alias: "or"
    st: int           # UserStatus (1/2/3)
    is_owner: bool    # 是否为当前 Org 的 Owner
    jti: str          # 唯一 Token ID（用于吊销）
    exp: int          # 过期时间（建议 1h）

def create_access_token(claims: TokenClaims) -> str:
    # 使用 RSA 私钥签发
    pass

def get_public_key_pem() -> str:
    # 返回 RSA 公钥（供 Cloudland 拉取）
    pass
```

**废弃 `GET /auth/public-key`**（Cloudland 不再自行验签，无需公钥分发）

---

### 1.3 Reverse Proxy 层（API Gateway）

**新增 `app/middleware/proxy.py`**

Middle 作为唯一网关，所有前端对 Cloudland 资源的请求统一经 Middle 转发。

```python
# 路由规则：
# /api/v1/cloudland/**  →  转发到对应 Region 的 Cloudland 内网地址
# /api/v1/orgs/**       →  Middle 自身处理
# /api/v1/users/**      →  Middle 自身处理
# /auth/**              →  Middle 自身处理

class ReverseProxyMiddleware:
    """
    拦截 /api/v1/cloudland/** 请求，转发到 Cloudland 内网
    """

    async def __call__(self, request):
        # 1. 从 Authorization Header 提取 JWT
        token = extract_bearer_token(request)
        claims = verify_and_decode(token)

        # 2. 检查 jti 是否已被吊销
        if is_revoked(claims.jti):
            return Response(401, "Token revoked")

        # 3. 从 claims.region 查 regions 表，获取 internal_endpoint
        region = get_region(claims.region)
        if not region or not region.is_available:
            return Response(503, "Region unavailable")

        # 4. 转发请求：去掉 Authorization Header，改加内部 Header
        forwarded_headers = {
            "X-User-ID":   claims.sub,
            "X-User-Email": claims.email,
            "X-Org-ID":    claims.org_id,
            "X-Org-Role":  str(claims.or_),
            "X-Is-Owner":  str(claims.is_owner),
            "X-System-Role": str(claims.sr),
        }

        # 5. 代理请求到 Cloudland 内网，携带该 Region 的独立密钥
        forwarded_headers["X-Forwarded-Secret"] = region.internal_secret
        target_url = f"{region.internal_endpoint}{request.path}"
        response = await proxy_request(target_url, request, forwarded_headers)
        return response
```

**安全保障**：
- Cloudland 只监听内网端口，iptables/Security Group 拒绝公网直连
- 转发请求携带 `X-Forwarded-Secret` 共享密钥，Cloudland 验证此 Header 防止伪造
- Middle 去掉原始 Authorization Header，Cloudland 不接触 JWT

---

### 1.4 Org 管理 API

**新增 `app/api/endpoints/orgs.py`**

```
POST   /orgs                           创建 Org（仅 SystemAdmin）
GET    /orgs                           列出 Org（SystemAdmin 全量，普通用户仅自己的）
GET    /orgs/{org_id}                  Org 详情
PATCH  /orgs/{org_id}                  改名（OrgOwner / OrgAdmin / SystemAdmin）
DELETE /orgs/{org_id}                  解散（仅 SystemAdmin，需先清空资源）

POST   /orgs/{org_id}/members          添加成员（AddMember）
DELETE /orgs/{org_id}/members/{uid}    移除成员（RemoveMember）
PATCH  /orgs/{org_id}/members/{uid}    修改成员角色（UpdateMemberRole）
POST   /orgs/{org_id}/transfer-owner   转让 Owner（仅 SystemAdmin）
```

---

### 1.5 Region 管理 API

**新增 `app/api/endpoints/regions.py`**

所有写操作仅限 SystemAdmin（`token.sr == 1`）。Region 从 DB 动态读取，增删无需重启 Middle。

```
POST   /regions                          注册新 Region（仅 SystemAdmin）
  Body: {
    id:                "ap-south",        // Region 唯一标识，创建后不可改
    display_name:      "亚太南部",
    internal_endpoint: "http://10.0.3.1:8080",
    internal_secret:   "auto-generate"    // 可传入指定值，或传 "auto-generate" 由系统生成
  }
  逻辑：
    - 校验 id 格式（仅允许 [a-z0-9-]）
    - 校验 internal_endpoint 可达（可选，HEAD 请求探测）
    - 若 internal_secret == "auto-generate"，生成 64 位随机字符串
    - 插入 regions 表，is_available 默认 true
  Response: {
    id, display_name, internal_endpoint, internal_secret, is_available
    // ⚠️ internal_secret 仅在创建时返回一次，用于配置到 Cloudland
  }

GET    /regions                          列出所有 Region
  - 公开接口（无需鉴权），前端初始化时调用
  - 不返回 internal_endpoint 和 internal_secret
  Response: [{ region_id, display_name, is_available }]

GET    /regions/{region_id}              Region 详情（仅 SystemAdmin）
  - 返回含 internal_endpoint 的完整信息（不含 internal_secret）

PATCH  /regions/{region_id}              更新 Region（仅 SystemAdmin）
  Body: { display_name?, internal_endpoint?, is_available? }
  - is_available 设为 false 时：该 Region 不可再签发新 Token，但已有 Token 仍可转发
  - 更新 internal_endpoint 时：可选探测新地址是否可达

DELETE /regions/{region_id}              删除 Region（仅 SystemAdmin）
  - 需先确认该 Region 下无资源（或强制删除标记）
  - 软删除（标记 deleted_at），Proxy 层不再路由到该 Region

POST   /regions/{region_id}/rotate-secret  轮换密钥（仅 SystemAdmin）
  - 生成新的 internal_secret
  - 返回新密钥（仅此次返回，需同步更新 Cloudland 配置）
  Response: { region_id, new_secret }
```

---

### 1.6 用户管理 API 扩展

**修改 `app/api/endpoints/users.py`**

```
POST /users/with-org    注册新用户 + 创建 Org（场景一，邮件验证后调用）
POST /users             仅创建用户（场景二B，无 Org）
GET  /users/validate    检查 email 是否可用（供邮件流程使用，需限流）

PUT  /users/{uid}/enable    SystemAdmin 启用用户
PUT  /users/{uid}/disable   SystemAdmin 禁用用户
PUT  /users/{uid}/demote    降级 SystemAdmin → SystemUser
DELETE /users/{uid}         删除用户（级联处理 Owner/Member）
PATCH  /users/{uid}/profile 修改 Profile（name/region/language）
PUT    /users/{uid}/password 修改密码（需验证旧密码）
```

---

### 1.7 Token 端点更新

**修改 `app/api/endpoints/auth.py`**

```
POST /auth/token
  Body: { email, password, org_id?(可选), region?(可选) }
  - 验证密码
  - 若 org_id 未传：取用户最早加入的 Org
  - 查询 Member 记录 → 确认 user ∈ org
  - 查询 Organization → 确认 is_owner
  - 签发包含完整 Claims 的 RS256 JWT
  Response: { access_token, token_type, expires_in }

POST /auth/token/refresh
  - 验证旧 token（检查 jti 未被吊销）
  - 重新查询最新的 org_role/status
  - 签发新 token（新 jti）

POST /auth/token/revoke
  - 将 jti 写入 revocation_store（Redis 或 DB）
  - TTL = 原 token 剩余有效期

POST /auth/switch-org
  Header: Authorization: Bearer <当前有效 Token>
  Body: { org_id, region?(可选) }
  - 验证旧 Token 签名合法（必须在有效期内）
  - 从旧 Token 取出 sub（user_id）
  - 查 members 表：user ∈ org_id？→ 否则 403
  - 查 organizations 表：org 存在且未删除？→ 否则 404
  - 确认 is_owner（user_id == org.owner_user_id）
  - region 未传时：取用户上次使用的 region（存 user.last_region）或 org 的首个可用 region
  - 签发新 Token（新 jti，新 org_id/region/or/is_owner）
  - 吊销旧 Token 的 jti（推送吊销事件到各 Cloudland）
  Response: { access_token, token_type, expires_in, org_id, region }

POST /auth/switch-region
  Header: Authorization: Bearer <当前有效 Token>
  Body: { region }
  - 验证旧 Token 签名合法
  - 从旧 Token 取出 sub / org_id（org 保持不变，仅换 region）
  - 查 regions 表：region 存在且 is_available？→ 否则 404/503
  - 签发新 Token（新 region，新 jti，其余 claims 不变）
  - 吊销旧 Token 的 jti
  Response: { access_token, token_type, expires_in, org_id, region }

GET /regions
  - 无需鉴权（公开接口，前端初始化时调用）
  - 返回所有可用 Region 列表（不暴露内网地址）
  Response: [
    {
      region_id:    "cn-north",
      display_name: "中国北部",
      is_available: true      // false 时前端置灰该 Region
    }
  ]

GET /users/me/orgs
  Header: Authorization: Bearer <当前 Token>
  - 查询当前用户所属的所有 Org 列表（供前端切换 Org 下拉框使用）
  Response: [
    {
      org_id,
      name,
      slug,
      org_role,       // 当前用户在该 Org 的角色
      is_owner,
      is_current,     // 是否是 Token 中当前激活的 Org
    }
  ]
```

---

### 1.8 Token 吊销存储

**新增 `app/models/token_revocation.py`**（或使用 Redis）

```python
class TokenRevocation(Base):
    __tablename__ = "token_revocations"
    jti        = Column(String(64), primary_key=True)
    expires_at = Column(DateTime)  # 到期后可清理
```

由于 Middle 作为 API Gateway 统一验签，Token 吊销只需在 Middle 本地检查即可，**不再需要向 Cloudland 推送吊销事件**。

Middle 验签时若发现 jti 在吊销表中 → 直接返回 401，请求不会转发到 Cloudland。

---

### 1.9 废弃 BackendAccount 用户同步

当前 Middle 在用户激活时调用 `sync_user_to_region()` 向各 Cloudland 创建用户。

**迁移后**：删除 `sync_user_to_region()` 和 `BackendAccount` 模型（Cloudland 无状态后无需同步）。

---

## 第二部分：Cloudland 变更（Go/Gin）

**参考文件**：`cloudland/plan/permission-redesign-plan.md`（该 plan 的大部分内容保持不变，仅改动数据来源）

### 2.1 废弃 JWT 验签（改为读取 Middle 转发的 Header）

**修改 `web/src/routes/jwt.go`**

```go
// 删除：NewToken()（Token 由 Middle 签发，Cloudland 不再签发）
// 删除：ParseToken()（Cloudland 不再自行验签，Middle 已统一验签）
// 删除：RSA 公钥拉取逻辑
// 删除：CustomClaims struct（不再需要）
// 整个 jwt.go 文件可在迁移完成后删除
```

---

### 2.2 MemberShip 从 Header 构建

**修改 `web/src/common/member.go`**

```go
// 删除：GetDBMemberShip()（不再查 DB）
// 新增：GetMemberShipFromHeaders(c *gin.Context) *MemberShip

func GetMemberShipFromHeaders(c *gin.Context) *MemberShip {
    return &MemberShip{
        UserID:     parseID(c.GetHeader("X-User-ID")),
        UserEmail:  c.GetHeader("X-User-Email"),
        SystemRole: model.SystemRole(parseIntHeader(c, "X-System-Role")),
        OrgID:      parseID(c.GetHeader("X-Org-ID")),
        OrgRole:    model.OrgRole(parseIntHeader(c, "X-Org-Role")),
        IsOrgOwner: c.GetHeader("X-Is-Owner") == "true",
    }
}

// 所有 Check* 方法保持不变（逻辑不变，只是数据来源变了）
```

---

### 2.3 授权中间件更新

**修改 `web/src/apis/authorize.go`**

```go
// 删除：if realUser == "admin" { ... } magic string
// 删除：数据库查询 user / member / org
// 删除：JWT 验签逻辑（Middle 已验签）
// 删除：jti 吊销检查（Middle 已处理）
// 新增：校验内网共享密钥（防止绕过 Middle 直连）

func Authorize(c *gin.Context) {
    // 1. 验证请求来自 Middle（共享密钥）
    secret := c.GetHeader("X-Forwarded-Secret")
    if secret != config.InternalSecret {
        c.AbortWithStatus(403)
        return
    }

    // 2. 从 Header 构建 MemberShip（Middle 已完成鉴权）
    memberShip := GetMemberShipFromHeaders(c)
    if memberShip.UserID == 0 || memberShip.OrgID == 0 {
        c.AbortWithStatus(400)
        return
    }

    c.Set("membership", memberShip)
    c.Next()
}
```

---

### 2.4 资源模型去 FK 约束

**修改所有资源模型**（instance/volume/subnet 等）

```go
// 删除：Owner int64 上的 gorm:"foreignkey:..." 约束（如有）
// 保留：Owner int64（存储 org_id，纯字符串语义）
// 保留：Creater int64（纯审计，存储 user_id）
// 说明：Owner 字段命名保持不变，语义从"拥有者用户"变为"归属 Org"
```

---

### 2.5 废弃 User/Org/Member 管理 API

**修改 `web/src/routes/user.go` / `org.go`**

所有 user/org/member 的写操作接口返回 `501 Not Implemented`，并在响应体中提示改用 Middle API：

```go
// 保留（仍需要）：
//   AccessToken()  → 转发到 Middle /auth/token（或直接废弃，前端直接调 Middle）
//   SwitchOrg()    → 转发到 Middle /auth/switch-org

// 废弃（返回 501）：
//   CreateUser, DeleteUser, UpdateUser
//   CreateOrg, DeleteOrg, AddMember, RemoveMember
```

---

### 2.6 资源文件批量替换

与 `permission-redesign-plan.md` 一致，对 ~17 个资源文件执行：

| 旧调用 | 新调用 |
|--------|--------|
| `memberShip.GetWhere()` | `memberShip.GetOrgFilter()` |
| `memberShip.ValidateOwner(model.Reader, r.Owner)` | `memberShip.CheckResourceOrg(model.OrgReader, r.Owner)` |
| `memberShip.CheckPermission(model.Admin)` | `memberShip.CheckSystemPermission()` |
| `memberShip.CheckCreater(...)` | **删除**（Creater 纯审计） |

---

### 2.7 删除 users / organizations / members 表

迁移完成后，通过 DB 迁移脚本移除这三张表（数据已导入 Middle）。

---

## 第三部分：数据迁移

### 迁移顺序

```
Step 1：从各 Cloudland Region 导出 users / organizations / members 数据
        → 合并去重（以 email 为 key 做 dedup）
        → 导入到 Middle 的全局 DB

Step 2：Middle DB 执行 Alembic migration
        → 新增 organizations / members 表
        → users 表新增 system_role / status / first_name / last_name / remark 字段

Step 3：Middle 切换到 RS256 Token 签发
        → 公钥通过 GET /auth/public-key 对外暴露

Step 4：Cloudland 更新 JWT 验签逻辑（接受 Middle 签发的 RS256 Token）
        → 双轨并行：旧 Token（Cloudland 签发）继续有效直到过期
        → 新 Token（Middle 签发）也可验证通过

Step 5：前端 / Middle 登录入口切换，Token 全部由 Middle 签发

Step 6：Cloudland 移除 user/org/member 表和相关 API（双轨期结束后）
```

---

## 关键文件路径

### cloudland-middle（Python）

| 文件 | 操作 |
|------|------|
| `app/models/user.py` | 修改（新增 system_role/status 字段） |
| `app/models/org.py` | **新增** |
| `app/models/region.py` | **新增** |
| `app/models/member.py` | **新增** |
| `app/core/security.py` | 修改（HS256 → RS256，新增 TokenClaims） |
| `app/middleware/proxy.py` | **新增**（Reverse Proxy / API Gateway） |
| `app/api/endpoints/auth.py` | 修改（org-aware token 签发，switch-org/region，revoke） |
| `app/api/endpoints/users.py` | 修改（扩展用户管理 API） |
| `app/api/endpoints/orgs.py` | **新增** |
| `app/api/endpoints/regions.py` | **新增**（Region 管理 API，仅 SystemAdmin） |
| `app/services/auth_service.py` | 修改（删除 sync_user_to_region，新增 org 相关逻辑） |
| `app/services/org_service.py` | **新增** |
| `app/models/token_revocation.py` | **新增** |
| `alembic/versions/xxx_add_org_member.py` | **新增**（DB migration） |

### cloudland（Go）

| 文件 | 操作 |
|------|------|
| `web/src/routes/jwt.go` | **删除**（Cloudland 不再验签，Middle 统一处理） |
| `web/src/common/member.go` | 修改（GetMemberShipFromHeaders 替换 GetDBMemberShip） |
| `web/src/apis/authorize.go` | 修改（去 JWT/DB 查询，改读 X-* Header + 验证共享密钥） |
| `web/src/routes/user.go` | 修改（写操作返回 501） |
| `web/src/routes/org.go` | 修改（写操作返回 501） |
| `web/src/routes/admin.go` | 修改（adminInit 移到 Middle） |
| 17 个资源文件 | 批量替换（同 permission-redesign-plan.md） |

---

## 实施顺序

```
Phase 1  Middle：新增 Org/Member/Region 模型 + Alembic 迁移 + 数据导入
Phase 2  Middle：RS256 Token 签发 + Token 吊销存储
Phase 3  Middle：Reverse Proxy 层实现（/api/v1/cloudland/** → Cloudland 内网）
Phase 4  Middle：Org/User 管理 API 全量实现
Phase 5  Middle：switch-org / switch-region 端点
Phase 6  Cloudland：authorize.go 改为读取 X-* Header + 验证共享密钥
Phase 7  Cloudland：member.go 切换到 GetMemberShipFromHeaders
Phase 8  Cloudland：17 个资源文件批量替换
Phase 9  前端：所有请求统一指向 Middle（登录 + 资源操作）
Phase 10 Cloudland：关闭公网端口，仅保留内网访问
Phase 11 验证稳定后：Cloudland 删除 jwt.go / user/org/member 表及 API
```

---

## 验证方案

1. Middle 签发 RS256 Token，前端通过 Middle 转发资源请求，Cloudland 收到带 X-* Header 的请求
2. 只读用户尝试创建资源 → Cloudland 读取 X-Org-Role=Reader → 返回 403
3. 用户不属于某 Org → Middle 拒绝签发该 Org 的 Token（403），请求不会到达 Cloudland
4. 用户被移出 Org → Middle Token 吊销 → Middle Proxy 层拦截旧 Token，不转发
5. 同一 Org 成员切换 Region → switch-region 签发新 Token，Middle 转发到新 Region 的 Cloudland
6. 直连 Cloudland 公网 → 被 iptables / Security Group 拒绝
7. 伪造 X-* Header 绕过 Middle 直连内网 → Cloudland 验证 X-Forwarded-Secret 失败 → 403
8. 跨 Region 聚合查询 → Middle `/orgs/{org_id}/resources` 向多个 Cloudland 内网汇总数据
