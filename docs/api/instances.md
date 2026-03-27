# 实例 API

## 查询实例列表

<div class="api-method get">GET</div> `/api/v1/instances`

### 查询参数

| 参数名 | 类型 | 描述 |
|--------|------|------|
| org_id | string | 过滤指定组织的实例 |
| status | string | 过滤指定状态 (running/stopped) |

## 创建新实例

<div class="api-method post">POST</div> `/api/v1/instances`

```json
{
  "name": "api-test",
  "flavor_id": "c2-m4-d80",
  "image_id": "centos-7-cloud",
  "subnet_id": "private-net-01"
}
```

## 执行动作 (重启/关机等)

<div class="api-method post">POST</div> `/api/v1/instances/:id/action`

| 动作 (Action) | 描述 |
|--------------|------|
| start | 开机 |
| stop | 强行关机 |
| reboot | 重启应用 |
