# Org Status 设计方案

## 背景

当前 `Organization` 模型没有状态字段，只有 `deleted_at` 表示软删除。
需要支持以下场景：
- 用户注册后未激活，org 已创建但不可访问
- 管理员对 Org 进行暂停/禁用操作，应对欠费冻结、违规封禁等场景

当前存在的问题：注册时 org 直接以完整状态创建，用户未激活账号时 org 没有任何"未激活"标记，存在安全隐患。

## 状态定义

```python
class OrgStatus(IntEnum):
    PENDING   = 0  # 待激活（用户注册后未激活账号）
    ACTIVE    = 1  # 正常运营
    SUSPENDED = 2  # 暂停（资源保留，禁止写操作）
    DISABLED  = 3  # 禁用（禁止登录和任何访问）
```

### 状态流转

```
注册 → PENDING → (用户激活账号) → ACTIVE
                                    ↕ 管理员操作
                               SUSPENDED / DISABLED
```

- `PENDING` 由系统在注册时自动设置，激活账号后系统自动变为 `ACTIVE`
- `ACTIVE` / `SUSPENDED` / `DISABLED` 三者可由管理员互相切换
- 真正的"永久注销"走 `deleted_at` 软删除（已有）

### 权限矩阵

| 操作 | PENDING | ACTIVE | SUSPENDED | DISABLED |
|---|---|---|---|---|
| 登录 / switch-org | ❌ | ✅ | ✅ | ❌ |
| 查看资源（只读） | ❌ | ✅ | ✅ | ❌ |
| 创建 / 修改 / 删除资源 | ❌ | ✅ | ❌ | ❌ |

- 状态变更（ACTIVE ↔ SUSPENDED ↔ DISABLED）仅限 system org 的 ADMIN 角色操作
- `PENDING` 由系统自动管理，管理员不可手动设置

## 数据模型变更

### cpgateway/app/models/org.py

新增 `OrgStatus` 枚举和 `status` 字段：

```python
class OrgStatus(IntEnum):
    PENDING   = 0
    ACTIVE    = 1
    SUSPENDED = 2
    DISABLED  = 3

class Organization(Base):
    # ... 现有字段 ...
    status = Column(Integer, default=OrgStatus.PENDING, nullable=False)
```

### Alembic Migration

```python
# 新增 status 列
# 存量数据（已激活的 org）默认为 1（ACTIVE）
op.add_column('organizations',
    sa.Column('status', sa.Integer(), nullable=False, server_default='1')
)
```

## 注册与激活流程变更

### register_user（auth_service.py）

创建 org 时设置 `status=OrgStatus.PENDING`：

```python
org = Organization(
    name=user_in.org_name,
    slug=user_in.org_slug,
    org_type=OrgType.TEAM,
    owner_user_id=target_user.id,
    status=OrgStatus.PENDING,  # 新增
)
```

### activate_user（auth_service.py）

激活用户账号时同步将 org 状态改为 `ACTIVE`：

```python
# 激活用户后，同步激活其 owner 的 org
await db.execute(
    update(Organization)
    .where(Organization.owner_user_id == user.id, Organization.status == OrgStatus.PENDING)
    .values(status=OrgStatus.ACTIVE)
)
```

## 接口变更

### 新增：修改 Org 状态（仅系统管理员）

```
PATCH /api/v1/orgs/{org_id}/status
Body: { "status": 2 }
```

权限检查：调用方必须是 system org 的 ADMIN，且不允许将 status 设为 `PENDING`（0）。

### 现有接口响应中补充 status 字段

`GET /api/v1/orgs/` 和 `GET /api/v1/orgs/{org_id}` 的响应 schema 加入 `status`。

## 执行逻辑（CPGateway）

### switch-org（cpgateway/app/api/endpoints/auth.py）

切换组织时检查目标 org 状态：
- `PENDING` / `DISABLED` → 返回 403

### 资源写操作拦截

在 quota 中间件或 proxy 路由前置检查当前 org 状态：
- `SUSPENDED` → 拦截所有非 GET 请求，返回 403
- `PENDING` / `DISABLED` → 拦截所有请求，返回 403

涉及文件：`cpgateway/app/services/quota_service.py` 或在 proxy 路由统一处理。

## 涉及文件清单

| 文件 | 变更内容 |
|---|---|
| `cpgateway/app/models/org.py` | 新增 `OrgStatus` 枚举 + `status` 字段 |
| `cpgateway/app/schemas/org.py` | org 相关 schema 加入 `status` 字段 |
| `cpgateway/app/services/auth_service.py` | 注册时 org 设 PENDING，激活时改为 ACTIVE |
| `cpgateway/app/api/endpoints/orgs.py` | 新增 PATCH status 接口 |
| `cpgateway/app/api/endpoints/auth.py` | switch-org 加 org status 检查 |
| `cpgateway/app/services/quota_service.py` | 写操作前检查 org status |
| `cpgateway/alembic/versions/xxxx_add_org_status.py` | 新增 migration（存量数据默认 ACTIVE） |
| `web/src/views/dashboard/OrgList.vue` (或相关组件) | 展示 status、管理员操作入口 |
| `web/src/locales/en.ts` + `zh.ts` | 新增 status 相关 i18n 文案 |

## 实现步骤

1. **模型 + Migration**：添加 `OrgStatus` 枚举和 `status` 字段，生成并执行 alembic migration
2. **Schema 更新**：org 相关 Pydantic schema 加 status
3. **注册流程**：`register_user` 创建 org 时设 `PENDING`
4. **激活流程**：`activate_user` 激活用户时同步将 org 改为 `ACTIVE`
5. **switch-org 检查**：`PENDING` / `DISABLED` org 拒绝切换
6. **写操作拦截**：`SUSPENDED` / `PENDING` / `DISABLED` org 拦截相应请求
7. **管理员接口**：`PATCH /orgs/{id}/status`，权限校验，禁止设为 PENDING
8. **前端**：状态展示 + 管理员操作按钮 + i18n
