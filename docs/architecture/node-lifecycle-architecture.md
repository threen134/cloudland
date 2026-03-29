# 计算节点生命周期管理 — 纯 API 驱动部署与服务调用关系

## 系统架构概览

```mermaid
graph TB
    subgraph "外部"
        CLI["管理员 CLI / UI"]
    end

    subgraph "Web 层 (Go)"
        API["REST API<br/>apis/hyper.go"]
        VIEW["Web UI<br/>routes/hyper.go"]
        HYPER_ADMIN["HyperAdmin<br/>routes/hyper.go"]
        MIG_ADMIN["MigrationAdmin<br/>routes/migration.go"]
        SSH["SSH Executor<br/>common/ssh.go"]
        CLIENT["clients.go<br/>HyperExecute()<br/>NodeAdd() / NodeRemove()"]
    end

    subgraph "控制面 cland (C++)"
        RPC["rpcworker<br/>HTTP Server<br/>:50080<br/>提供脚本下载"]
        NETLAYER["NetLayer<br/>addBackend()<br/>removeBackend()"]
        SCI["SCI 框架<br/>SCI_BE_add()<br/>SCI_BE_remove()"]
    end

    subgraph "计算节点 (C++)"
        CLOUDLET["cloudlet<br/>SCI 后端进程"]
        BASH["bash<br/>执行部署脚本"]
    end

    subgraph "数据库"
        DB[("PostgreSQL<br/>hypers / instances / routers<br/>interfaces / resources<br/>migrations")]
    end

    CLI -->|"REST API<br/>POST /api/v1/hypers"| API
    CLI -->|"Web UI"| VIEW
    API --> HYPER_ADMIN
    VIEW --> HYPER_ADMIN
    
    HYPER_ADMIN -->|"1. 分配 HostID"| DB
    HYPER_ADMIN -->|"2. SSH 连接"| SSH
    SSH -->|"3. 下载并执行部署命令"| BASH
    BASH -->|"curl /internal/deploy-script"| RPC
    SSH -- "4. 成功后" --> CLIENT
    CLIENT -->|"5. POST /internal/node/add"| RPC
    
    HYPER_ADMIN -->|"触发迁移"| MIG_ADMIN
    MIG_ADMIN --> CLIENT
    CLIENT -->|"HTTP POST<br/>/internal/execute"| RPC
    RPC --> NETLAYER
    NETLAYER --> SCI
    SCI <-->|"SCI 协议"| CLOUDLET
    
    HYPER_ADMIN --> DB
    MIG_ADMIN --> DB
```

---

## 阶段一览

| # | 阶段 | 触发方 | 服务调用链 |
|---|------|--------|-----------|
| A | 部署 (Deploy) | 管理员 | API → Web 分配ID → Web SSH目标机执行部署 → Web 调用 cland `/internal/node/add` → `SCI_BE_add()` |
| B | 软维护 (Pause) | 管理员 | API → `HyperAdmin.Patch()` → DB 更新 status=0 (disabled) |
| C | 退役 (Decommission) | 管理员 | API → `HyperAdmin.Decommission()` → `MigrationAdmin.Create()` 触发迁移 → cland `/internal/execute` |
| D | 移除 (Remove) | 管理员 | API → `HyperAdmin.CompleteDecommission()` → cland `/internal/node/remove` → `SCI_BE_remove()` + 清理 DB |

---

## 步骤 A：API 驱动部署新节点 (Deploy)

彻底摒弃 `host.list` 和人工运行部署脚本。管理员直接提供机器的 SSH 信息，系统全自动完成部署和注册。

```mermaid
sequenceDiagram
    participant A as 管理员
    participant W as Web API (HyperAdmin)
    participant DB as 数据库
    participant T as 目标机器 (SSH)
    participant C as cland (rpcworker)
    participant SCI as SCI 框架

    A->>W: POST /api/v1/hypers<br/>{"ip":"10.x", "ssh_user":"root", ...}
    
    W->>DB: SELECT MAX(hostid) + 1
    DB-->>W: 返回新 ID (例如: 5)
    W->>DB: INSERT hypers SET status=4 (deploying), hostid=5
    
    W-->>A: 200 OK 返回预创建的 hyper 记录 (异步部署在后台继续)
    
    Note over W,T: 异步协程开始部署
    W->>T: SSH Connect & Run Command
    Note right of T: export 环境变量<br/>curl 调用 /internal/deploy-script 获取脚本并 bash 运行
    T->>C: GET /internal/deploy-script
    C-->>T: 返回 deploy-compute-node.sh
    T-->>W: 部署脚本执行成功完毕
    
    W->>C: (调用 NodeAdd) POST /internal/node/add {"id":5}
    C->>SCI: SCI_BE_add()
    SCI-->>C: SCI_SUCCESS
    C-->>W: 200 OK
    
    W->>DB: UPDATE hypers SET status=1 (active), remark="Deployed OK"
```

### 接口定义

**REST API 端点** — `POST /api/v1/hypers`

```json
// Request
{
  "ip": "192.168.1.100",
  "ssh_user": "root",
  "ssh_password": "mypassword",
  "network_device": "eth0",
  "vlan_device": "eth1",
  "zone_name": "zone1"
}

// Response 200 (表示已接受请求加入后台部署行列)
{
  "hostid": 5,
  "hostname": "hyper-192-168-1-100",
  "status": "deploying",
  "remark": "Deployment in progress..."
}
```

前端可以通过轮询 `GET /api/v1/hypers/5` 来观察状态从 `deploying(4)` 变为 `active(1)` 或 `deploy_failed(5)`。

**cland 新增端点** — `GET /internal/deploy-script`
返回控制面本地的 `/opt/cloudland/deploy/docker/scripts/deploy-compute-node.sh` 文件内容给计算节点。

---

## 步骤 B：退役排空 (Decommission)

```mermaid
sequenceDiagram
    participant A as 管理员
    participant W as Web API
    participant DB as 数据库
    participant MA as MigrationAdmin
    participant C as cland

    A->>W: POST /api/v1/hypers/5/decommission
    W->>DB: UPDATE hypers SET status=2 (draining)
    Note over DB: status=2 停止接收新实例调度

    W->>DB: SELECT * FROM instances<br/>WHERE hyper=5 AND status NOT IN ('deleted','deleting')
    DB-->>W: [inst-1, inst-2, ...]

    loop 每个活跃实例
        W->>MA: Create(ctx, name, [instance], force=false, tgtHyper=-1)
        MA->>DB: 生成 Migration 记录
        MA->>C: POST /internal/execute (触发底层 target_migration.sh)
    end

    W-->>A: 200 OK {"status":"draining", "migrations": [...]}
```

### 接口定义

**REST API 端点** — `POST /api/v1/hypers/:hostid/decommission`

```json
// Response 200
{
  "hostid": 5,
  "status": "draining",
  "migrations": [
    { "id": 101, "instance_id": 1, "status": "in_progress" }
  ]
}
```

---

## 步骤 C：完成退役并移除 (Complete Decommission)

```mermaid
sequenceDiagram
    participant A as 管理员
    participant W as Web API
    participant DB as 数据库
    participant C as cland
    participant SCI as SCI 框架

    A->>W: POST /api/v1/hypers/5/complete-decommission

    rect rgb(255, 235, 235)
        Note over W,DB: 前置检查：必须确认资源已排空
        W->>DB: 检查 instances WHERE hyper=5 是否为空
        W->>DB: 检查 routers WHERE hyper=5 OR peer=5 是否为空
        W->>DB: 检查 interfaces WHERE hyper=5 是否为空
    end

    Note over W: 检查通过
    W->>C: (调用 NodeRemove) POST /internal/node/remove {"id":5}
    C->>SCI: SCI_BE_remove(5)
    SCI-->>C: SCI_SUCCESS
    C-->>W: 200 OK

    W->>DB: DELETE FROM resources WHERE hostid=5
    W->>DB: UPDATE hypers SET status=3 (decommissioned) WHERE hostid=5
    
    W-->>A: 200 OK
```

### 接口定义

**REST API 端点** — `POST /api/v1/hypers/:hostid/complete-decommission`

```json
// Response 200 (成功)
{ 
  "hostid": 5, 
  "status": "decommissioned" 
}

// Response 409 (有残留资源，拒绝)
{
  "error": "node still has active resources",
  "remaining_instances": 1,
  "remaining_routers": 0,
  "remaining_interfaces": 2
}
```

---

## 部署流程所需网络访问清单

采用此架构后，Web 层和控制面需要新开通以下内部网络访问：

1. **Web 层 ➔ 计算节点 (SSH)**
   - 端口: 22 (或自定义 SSH 端口)
   - 用途: `common/ssh.go` 连接到新节点执行命令。

2. **计算节点 ➔ cland 控制面 (HTTP)**
   - 端口: `50080` (C++ rpcworker HTTP Server 端口) 
   - 注：脚本中写的是 `5006`，如果在防火墙或 HAProxy 后这是转发端口，需要确保计算节点能以此端口访问到控制面。
   - 用途: 下载部署脚本 (`/internal/deploy-script`)。

3. **Web 层 ➔ cland 控制面 (HTTP)**
   - 端口: `50080`
   - 用途: API 注册 (`/internal/node/add`)、移除 (`/internal/node/remove`)、执行命令 (`/internal/execute`)。

---

## 数据库模型状态值补充

**`model.Hyper.Status` 最终枚举定义**：

| 值 | 状态码 | 说明 | 可否被调度新实例 |
|----|--------|------|-----------------|
| 0 | `disabled` | 被管理员手动暂停调度 | ❌ 否 |
| 1 | `active` | 正常运行且接收调度 | ✅ 是 |
| 2 | `draining` | 正在排空实例，准备退役 | ❌ 否 |
| 3 | `decommissioned` | 已彻底从 SCI 和数据库摘除 | ❌ 否 |
| 4 | `deploying` | 刚刚创建并分配了 ID，正在后台 SSH 部署 | ❌ 否 |
| 5 | `deploy_failed` | SSH 部署脚本执行失败，或注册 SCI 失败 | ❌ 否 |
