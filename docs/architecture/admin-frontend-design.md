# Administration 前端区块设计

## 总览

Administration 区块仅对 Superuser 可见，用于管理全局云基础设施资源。

### 导航结构 (Layout.vue)

```
Administration (仅 Superuser 可见)
├── Regions (区域管理)      — 管理多区域部署
├── Zones (可用区)           — 管理可用区
├── Hypervisors (计算节点)  — 节点状态/资源/超配
├── Migrations (热迁移)     — 实例迁移管理
└── Alarms (节点告警)       — 告警策略管理
```

## 各模块 API 对应的前端功能设计

### 1. Regions (区域管理)

**API 端点：** cpgateway 直接管理

| API | 方法 | 前端功能 |
|-----|------|---------|
| `POST /api/v1/regions` | 创建 | 创建区域对话框（名称、endpoint、secret 自动生成/自定义） |
| `GET /api/v1/regions` | 列表 | 区域列表（搜索、分页） |
| `GET /api/v1/regions/{uuid}` | 详情 | 区域详情（含内部 endpoint） |
| `PATCH /api/v1/regions/{uuid}` | 编辑 | 编辑对话框 |
| `DELETE /api/v1/regions/{uuid}` | 删除 | 确认删除对话框 |
| `POST /regions/{uuid}/rotate-secret` | 轮换密钥 | 密钥轮换按钮 |

**页面设计：**
- RegionList.vue — 列表页，支持搜索、创建/编辑/删除模态框、密钥轮换操作

### 2. Zones (可用区)

**API 端点：** 代理到 Cloudland 后端

| API | 方法 | 前端功能 |
|-----|------|---------|
| `GET /api/v1/zones` | 列表 | 可用区列表 |
| `POST /api/v1/zones` | 创建 | 创建可用区 |
| `GET /api/v1/zones/{name}` | 详情 | 跳转详情页，展示关联节点 |
| `PATCH /api/v1/zones/{name}` | 编辑 | 编辑可用区 |
| `DELETE /api/v1/zones/{name}` | 删除 | 删除确认 |

**页面设计：**
- ZoneList.vue — 列表页，支持筛选
- ZoneDetail.vue — 详情页，展示 Zone 内关联的 Hypervisor 节点

### 3. Hypervisors (计算节点)

**API 端点：** 代理到 Cloudland 后端（Go API: `api/src/apis/hyper.go`）

| API | 方法 | 前端功能 | CPGateway 代理 |
|-----|------|---------|---------------|
| `GET /api/v1/hypers` | 列表 | 节点列表（CPU/内存/磁盘使用率、状态） | ✅ |
| `GET /api/v1/hypers/{uuid}` | 详情 | 详情页（资源分配、运行实例） | ✅ |
| `PATCH /api/v1/hypers/{uuid}` | 更新 | 修改状态、所属 Zone、超配比率、备注 | ✅ |
| `POST /api/v1/hypers` | 部署 | 部署新计算节点（IP、hostname、网卡、Zone、VirtType），返回一键部署命令 | ✅ 已补充 |
| `POST /api/v1/hypers/{uuid}/maintain` | 维护 | 进入维护模式，可选迁移全部实例到指定/自动目标节点 | ✅ 已补充 |
| `DELETE /api/v1/hypers/{uuid}` | 删除 | 删除节点记录（需无运行实例），清理关联资源和 SCI 注册 | ✅ 已补充 |

**Deploy 请求体 (`HyperDeployPayload`)：**
```json
{
  "ip": "192.168.1.100",        // required - 节点 IP
  "hostname": "compute-01",     // required - 主机名
  "network_device": "eth0",     // 默认 eth0
  "vlan_device": "eth0",        // 默认同 network_device
  "dns_server": "8.8.8.8",      // 默认 8.8.8.8
  "domain": "example.com",      // 默认 example.com
  "zone_name": "zone0",         // 可选，不填则使用默认 Zone
  "virt_type": "kvm-x86_64"     // 默认 kvm-x86_64
}
```

**Maintain 请求体 (`HyperMaintainPayload`)：**
```json
{
  "target_hyper": -1,   // 目标节点 hostid，-1 表示自动调度
  "migrate": true       // 是否迁移实例
}
```

**节点状态枚举：**
- `0` - Disabled (已禁用)
- `1` - Active (活跃)
- `2` - Maintaining (维护中)
- `4` - Deploying (部署中)
- `5` - Deploy Failed (部署失败)

**页面设计：**
- HypervisorList.vue — 列表页，展示各节点 CPU/内存/磁盘使用率、在线状态；支持部署新节点、删除节点
- HypervisorDetail.vue — 详情页，支持编辑超配比率、Zone 归属、备注；支持维护模式操作

### 4. Migrations (迁移管理)

**API 端点：** 代理到 Cloudland 后端

| API | 方法 | 前端功能 |
|-----|------|---------|
| `GET /api/v1/migrations` | 列表 | 迁移任务列表（状态、进度） |
| `GET /api/v1/migrations/{id}` | 详情 | 详情页（进度追踪） |
| `POST /api/v1/migrations` | 创建 | 创建迁移（选择实例、目标节点/自动调度、热/冷迁移） |

**页面设计：**
- MigrationList.vue — 列表页，展示迁移任务状态和进度
- MigrationDetail.vue — 详情页，进度追踪、源/目标节点信息

### 5. Node Alarm Rules (节点告警规则)

**API 端点：** 代理到 Cloudland 后端（Go API: `api/src/apis/alarms.go`）

| API | 方法 | 前端功能 | CPGateway 代理 |
|-----|------|---------|---------------|
| `GET /api/v1/node-alarm-rules` | 列表 | 告警规则列表（支持按 uuid/rule_type 筛选） | ✅ |
| `POST /api/v1/node-alarm-rules` | 创建 | 创建告警规则对话框（选择规则类型、填写动态配置） | ✅ |
| `DELETE /api/v1/node-alarm-rules/{uuid}` | 删除 | 删除确认对话框 | ✅ |
| `POST /api/v1/metrics/alarm/sync-mappings` | 同步 | 同步 VM 告警规则映射按钮 | ✅ |

**NodeAlarmRule 数据模型：**
```json
{
  "uuid": "auto-generated",
  "rule_type": "node_available",   // required - 规则类型
  "name": "node-down-alert",      // required - 规则名称
  "config": { ... },              // required - 动态 JSON 配置（按规则类型不同）
  "description": "节点不可达告警",  // 可选
  "enabled": true,                // 默认 true
  "owner": "admin"                // 规则创建者
}
```

**支持的规则类型 (`rule_type`)：**
| 类型 | 说明 |
|------|------|
| `node_available` | 节点可用性监控（宕机/不可达） |
| `control_node` | 控制节点管理资源监控 |
| `compute_node` | 计算节点资源监控 |
| `hypervisor_vcpu` | Hypervisor vCPU 资源监控 |
| `packet_drop` | 网络丢包监控 |
| `ip_block` | IP 段监控 |
| `ipgroup_available_ip` | IP 组可用 IP 监控 |

**页面设计：**
- NodeAlarmRuleList.vue — 列表页，按规则类型分组展示；支持创建/删除操作
  - 创建对话框：选择 rule_type 后动态渲染对应的配置表单
  - 删除操作：确认对话框，显示规则名称和类型
  - 筛选：按 rule_type 筛选

## 权限控制

- **路由守卫：** 所有 admin 路由设置 `meta: { requiresSuperAdmin: true }`
- **侧边栏：** Layout.vue 中通过 `authStore.isSuperUser` 控制 Administration 区块显隐
- **后端：** 使用 `get_current_superuser` 依赖项强制校验

## 技术栈

- **框架：** Vue 3 + TypeScript
- **状态管理：** Pinia (auth.ts, region.ts)
- **HTTP 客户端：** Axios (api/client.ts，自动注入 JWT、Org ID、Region)
- **国际化：** i18n (en.ts / zh.ts)
- **图标：** Lucide Icons

## 当前实现状态

| 模块 | 路由 | 视图 | API 客户端 | 状态 |
|------|------|------|-----------|------|
| Regions | ✅ | ✅ RegionList.vue | ✅ | 已实现 |
| Zones | ✅ | ✅ ZoneList/Detail.vue | ✅ zones.ts | 已实现 |
| Hypervisors | ✅ | ✅ HypervisorList/Detail.vue | ✅ hypervisors.ts | 已实现 |
| Migrations | ✅ | ✅ MigrationList/Detail.vue | ✅ migrations.ts | 已实现 |
| Node Alarm Rules | ⚠️ | ⚠️ 需重构为 NodeAlarmRuleList.vue | ✅ 代理已补充 | 需修正前端视图 |
