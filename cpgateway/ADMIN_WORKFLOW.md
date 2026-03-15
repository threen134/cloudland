# Admin User 工作流程

## 系统架构

### 用户层级

1. **Root User (超级管理员)**
   - **中间件唯一用户**：在 `users` 表中只有这一个用户
   - `is_superuser=True`, `is_active=True`
   - 系统启动时自动创建
   - 用于管理整个系统

2. **Backend Admin Accounts (后端管理员账号映射)**
   - 在 `backend_accounts` 表中的记录
   - 每个 Region 一条记录（`is_admin=True`）
   - 所有记录都关联到 Root User (`user_id` 指向 Root)
   - 存储各 Region 后端 Admin 的用户名和密码

3. **Normal Users (普通用户)**
   - 通过注册流程创建的用户
   - 在 `users` 表中有独立记录
   - 激活后自动在所有 Region 创建 Backend Account (`is_admin=False`)

### 数据库结构

#### users 表
```
id | username | email              | is_superuser | is_active
---+----------+--------------------+--------------+----------
1  | root     | root@cloudland.com | true         | true
2  | alice    | alice@example.com  | false        | true
3  | bob      | bob@example.com    | false        | true
```

#### backend_accounts 表
```
id | user_id | region_name | username | is_admin | password
---+---------+-------------+----------+----------+----------
1  | 1       | tor-04      | admin    | true     | Admin123
2  | 1       | tor-05      | admin    | true     | Admin456
3  | 2       | tor-04      | alice    | false    | random...
4  | 2       | tor-05      | alice    | false    | random...
5  | 3       | tor-04      | bob      | false    | random...
```

**说明：**
- ID 1-2：Root User 到各 Region Admin 的映射
- ID 3-5：普通用户到各 Region 的映射

## 初始化流程

### 1. 系统启动时自动创建 Root User

```python
# 在 app/main.py 的 startup 事件中
# 检查 users 表中是否存在 FIRST_SUPERUSER_USERNAME
# 如果不存在，则创建：
User(
    username=settings.FIRST_SUPERUSER_USERNAME,
    email=settings.FIRST_SUPERUSER_EMAIL,
    hashed_password=get_password_hash(settings.FIRST_SUPERUSER_PASSWORD),
    is_active=True,
    is_superuser=True
)
```

### 2. Root User 登录

```http
POST /api/v1/auth/token
Content-Type: application/x-www-form-urlencoded

username=root&password=rootpassword
```

返回：
```json
{
  "access_token": "eyJhbGc...",
  "token_type": "bearer"
}
```

### 3. Root User 创建 Region

```http
POST /api/v1/regions
Authorization: Bearer {root_token}
Content-Type: application/json

{
  "name": "tor-04",
  "endpoint_url": "https://163.74.70.150",
  "description": "Toronto Region 04"
}
```

### 4. Root User 为每个 Region 创建 Admin 映射

```http
POST /api/v1/admin-accounts
Authorization: Bearer {root_token}
Content-Type: application/json

{
  "region": "tor-04",
  "admin_account": "admin",
  "password": "AdminPassword123",
  "email": "admin@cloudland.local"
}
```

**逻辑说明：**
- 在 `backend_accounts` 表中创建一条记录
- `user_id` 指向 Root User
- 存储该 Region 后端 Admin 的用户名和密码
- 幂等：重复调用相同参数会直接返回已存在的记录

### 5. 为其他 Region 创建 Admin 映射

```http
# 为 tor-05 创建（同样映射到 Root User）
POST /api/v1/admin-accounts
Authorization: Bearer {root_token}
Content-Type: application/json

{
  "region": "tor-05",
  "admin_account": "admin",
  "password": "DifferentPassword456",
  "email": "admin@cloudland.local"
}
```

## 前端登录流程

### 场景：Root User 登录并选择 Region

1. **Root User 登录中间件**
```http
POST /api/v1/auth/token
Content-Type: application/x-www-form-urlencoded

username=root&password=rootpassword
```

2. **前端获取 Root User 可管理的 Regions**
```http
GET /api/v1/users/me
Authorization: Bearer {root_token}
```

返回：
```json
{
  "id": 1,
  "username": "root",
  "email": "root@cloudland.com",
  "is_superuser": true,
  "backend_accounts": [
    {
      "region_name": "tor-04",
      "username": "admin",
      "is_admin": true,
      "region_uuid": "..."
    },
    {
      "region_name": "tor-05",
      "username": "admin",
      "is_admin": true,
      "region_uuid": "..."
    }
  ]
}
```

3. **前端根据 Region 选择对应的 Backend Admin**
   - 用户在前端选择要管理的 Region（如 "tor-04"）
   - 前端在调用代理 API 时，带上 `region` 参数
   - 中间件会自动使用该 Region 对应的 Admin 凭证获取 Token

## 数据流示例

### Root 登录后访问 tor-04 的资源

```
1. 前端: POST /auth/token (username=root)
   ↓
2. 中间件: 返回 root_token
   ↓
3. 前端: GET /instances?region=tor-04 (Authorization: Bearer root_token)
   ↓
4. 中间件: 
   - 从 root_token 解析出 user_id=1
   - 查询 backend_accounts 表找到 user_id=1 AND region_name='tor-04' AND admin=true 的记录
   - 使用该记录的 username/password 向 tor-04 后端获取 admin token
   - 使用 admin token 代理请求到 tor-04 后端
   ↓
5. 后端 tor-04: 返回实例列表
   ↓
6. 中间件: 转发响应给前端
```

## 关键特性

✅ **自动初始化**：Root User 在系统启动时自动创建  
✅ **幂等性**：重复创建不会报错，会复用已有资源  
✅ **一对多**：一个 Admin User 只管理一个 Region  
✅ **前端灵活**：前端可以让用户选择要管理的 Region  
✅ **无需后端 API**：创建 Admin Account 只写中间件数据库

## 注意事项

1. **密码一致性**：Admin User 的密码应该与后端实际的 admin 账号密码一致
2. **Token 获取**：中间件会使用 `backend_accounts` 中的凭证向后端获取 Token
3. **权限隔离**：Admin User 只能管理其有 Backend Account 的 Region
