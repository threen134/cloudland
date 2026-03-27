# API 认证 (JWT)

## 获取 Token

在访问受保护的 API 之前，您需要先通过认证接口获取 JWT。

<div class="api-method post">POST</div> `/api/v1/login`

### 请求参数

| 参数名 | 类型 | 必选 | 描述 |
|--------|------|-----|------|
| email | string | 是 | 用户登录账号 (Email) |
| password | string | 是 | 用户密码 |

### 响应示例

```json
{
  "code": 200,
  "message": "Login successfully",
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
    "expires": 86400
  }
}
```

## 使用方式

将获取到的 Token 放在 HTTP 请求头的 `Authorization` 字段中：

```bash
curl -H "Authorization: Bearer <YOUR_TOKEN>" \
     http://localhost:8255/api/v1/instances
```
