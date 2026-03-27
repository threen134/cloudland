# API 参考概览

## 基本信息

CloudLand 提供了一套完整的 RESTful API，用于管理云平台的各种资源。

- **Base URL**: `http://<API_SERVER_IP>:8255/api/v1`
- **Content-Type**: `application/json`
- **认证方式**: JWT Bearer Token

## 接口状态码

| 状态码 | 描述 |
|--------|------|
| 200 | 请求成功 |
| 201 | 资源创建成功 |
| 400 | 请求参数错误 |
| 401 | 认证失败 (Token 无效或过期) |
| 403 | 权限不足 (RBAC 拦截) |
| 404 | 资源未找到 |
| 500 | 服务器内部错误 |

## 响应格式

所有 API 响应均遵循统一的 JSON 结构：

```json
{
  "code": 200,          // 业务代码
  "message": "success", // 描述信息
  "data": { ... }       // 返回的业务数据
}
```
