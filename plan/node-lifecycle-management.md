# 计算节点完整生命周期管理 — 彻底移除 host.list，API 驱动部署

## Context

当前 `host.list` 有三个核心问题：
1. 按行位置分配 ID，移除中间条目导致后续 ID 错位，破坏数据库关联
2. 仅在 `SCI_Initialize()` 时读取一次，增删节点必须重启 cland
3. 静态文件本身就是多余的中间层，增加运维复杂度

**新方案**：**彻底去掉 `host.list`**，改为纯 API 驱动：
- cland 以零后端启动，SCI 不加载任何 hostfile
- 管理员通过 REST API 提交目标机器信息（IP、用户名、密码等），Web 层 SSH 到目标机器执行部署脚本
- 部署完成后自动调用 `SCI_BE_add()` 注册节点到控制面
- 数据库 hypers 表成为节点注册的唯一权威数据源，ID 自动递增唯一分配
- 节点退役通过 API → `SCI_BE_remove()` 移除

**SCI 框架已有的能力**（无需新增，只需暴露）：
- `SCI_BE_add(sci_be_t *be)` — 运行时动态添加后端（`api.cpp:871-909`）
- `SCI_BE_remove(int be_id)` — 运行时动态移除后端（`api.cpp:911-935`）

---

## Phase 1: SCI 层 — 支持零后端启动（不需要 hostfile）

### 1a: topology.cpp — 跳过 hostfile 加载

**文件**: `sci/libsci/topology.cpp`（第 153-169 行）

当 `host_list == NULL` 且 `hostfile == NULL` 且无 `SCI_HOST_FILE` 环境变量时，跳过 `beMap.input()`，直接以空 beMap 启动：

```cpp
// 修改前:
if (hostlist != NULL) {
    rc = beMap.input((const char **)hostlist, numItem);
} else {
    char *hostfile = gCtrlBlock->getEndInfo()->fe_info.hostfile;
    if ((envp = ::getenv("SCI_HOST_FILE")) != NULL) {
        hostfile = envp;
    }
    if (hostfile == NULL) {
        hostfile = "host.list";   // ← 硬编码默认值
    }
    rc = beMap.input(hostfile, numItem);
}
if (rc != SCI_SUCCESS) {
    return rc;
}

// 修改后:
if (hostlist != NULL) {
    rc = beMap.input((const char **)hostlist, numItem);
    if (rc != SCI_SUCCESS) {
        return rc;
    }
} else {
    char *hostfile = gCtrlBlock->getEndInfo()->fe_info.hostfile;
    if ((envp = ::getenv("SCI_HOST_FILE")) != NULL) {
        hostfile = envp;
    }
    if (hostfile != NULL) {
        rc = beMap.input(hostfile, numItem);
        if (rc != SCI_SUCCESS) {
            return rc;
        }
    } else {
        log_info("No host file specified, starting with zero backends (API registration mode)");
    }
}
```

### 1b: topology.cpp — 保护零后端时的 height 计算

**文件**: `sci/libsci/topology.cpp`（第 178 行）

```cpp
// 修改前:
height = (int) ::ceil(::log((double)beMap.size()) / ::log((double)fanOut));

// 修改后:
if (beMap.size() > 0) {
    height = (int) ::ceil(::log((double)beMap.size()) / ::log((double)fanOut));
} else {
    height = 0;
}
```

### 1c: bemap.cpp — 已完成的稀疏 ID 放开 ✅

---

## Phase 2: rpcworker + netlayer — 去掉 host.list，暴露节点管理端点

### 2a: netlayer — 去掉 hostfile 参数

**文件**: `src/netlayer.hpp`

```cpp
int initFE(char *backend, RpcWorker *rpcWorker);  // 去掉 hostfile 参数
int addBackend(int beID, const char *hostname, int level = 1);
int removeBackend(int beID);
```

**文件**: `src/netlayer.cpp`

```cpp
int NetLayer::initFE(char *backend, RpcWorker *rpcWorker) {
    sciInfo.fe_info.hostfile = NULL;  // 不设 hostfile → 触发零后端模式
    sciInfo.fe_info.bepath = (char *)bePath.c_str();
    // ... 其余不变
}

int NetLayer::addBackend(int beID, const char *hostname, int level) {
    sci_be_t be = {beID, const_cast<char*>(hostname), level};
    int rc = SCI_BE_add(&be);
    return (rc == SCI_SUCCESS) ? be.id : rc;
}

int NetLayer::removeBackend(int beID) {
    return SCI_BE_remove(beID);
}
```

### 2b: rpcworker — 移除 host.list 引用 + 添加 HTTP 端点

**文件**: `src/rpcworker.cpp`

1. 删除 `#define CLOUD_HOST_FILE` 和所有 `hFile` 相关逻辑
2. 调用改为 `sciNet.initFE(const_cast<char*>(bePath), this);`
3. 添加 HTTP 端点（jsoncpp + httplib 已有）：

```cpp
// POST /internal/node/add    {"hostname":"compute-5","id":5,"level":1}
// POST /internal/node/remove  {"id":5}
```

---

## Phase 3: Web 层 — 管理员 API 驱动部署 Hyper（核心新增）

### 3a: 添加 SSH 依赖

**文件**: `web/go.mod`

```
go get golang.org/x/crypto
```

### 3b: SSH 执行器

**新建文件**: `web/src/common/ssh.go`

封装 SSH 远程执行功能，供部署和退役流程使用：

```go
package common

import (
    "fmt"
    "golang.org/x/crypto/ssh"
    "time"
)

type SSHConfig struct {
    Host     string
    Port     int
    User     string
    Password string
}

// RunCommand 通过 SSH 在远程机器上执行命令，返回组合输出
func (c *SSHConfig) RunCommand(cmd string, timeout time.Duration) (output string, err error) {
    config := &ssh.ClientConfig{
        User: c.User,
        Auth: []ssh.AuthMethod{ssh.Password(c.Password)},
        HostKeyCallback: ssh.InsecureIgnoreHostKey(),
        Timeout: 30 * time.Second,
    }
    addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
    client, err := ssh.Dial("tcp", addr, config)
    if err != nil {
        return "", fmt.Errorf("SSH connect to %s failed: %w", addr, err)
    }
    defer client.Close()

    session, err := client.NewSession()
    if err != nil {
        return "", fmt.Errorf("SSH session creation failed: %w", err)
    }
    defer session.Close()

    out, err := session.CombinedOutput(cmd)
    return string(out), err
}
```

### 3c: Hyper 模型扩展

**文件**: `web/src/model/hyper.go`

新增状态和部署参数：

```go
const (
    HYPER_DISABLED       = "disabled"       // 0: 不可调度
    HYPER_ACTIVE         = "active"         // 1: 正常运行
    HYPER_DRAINING       = "draining"       // 2: 正在排空实例
    HYPER_DECOMMISSIONED = "decommissioned" // 3: 已退役
    HYPER_DEPLOYING      = "deploying"      // 4: 正在部署中
    HYPER_DEPLOY_FAILED  = "deploy_failed"  // 5: 部署失败
)

var HyperStatusValues = map[int32]string{
    0: HYPER_DISABLED,
    1: HYPER_ACTIVE,
    2: HYPER_DRAINING,
    3: HYPER_DECOMMISSIONED,
    4: HYPER_DEPLOYING,
    5: HYPER_DEPLOY_FAILED,
}
```

### 3d: ID 自动分配逻辑

**文件**: `web/src/routes/hyper.go` — 新增方法：

```go
// AllocateHostID 从数据库获取下一个可用的递增唯一 ID
func (a *HyperAdmin) AllocateHostID(ctx context.Context) (int32, error) {
    _, db := GetContextDB(ctx)
    var maxID sql.NullInt32
    err := db.Model(&model.Hyper{}).Select("MAX(hostid)").Row().Scan(&maxID)
    if err != nil {
        return 0, err
    }
    if !maxID.Valid {
        return 0, nil  // 第一个节点，ID 从 0 开始
    }
    return maxID.Int32 + 1, nil
}
```

### 3e: 部署工作流（核心）

**文件**: `web/src/routes/hyper.go` — 新增方法：

**`Deploy(ctx, req DeployHyperRequest) (hyper, error)`**:

```go
type DeployHyperRequest struct {
    IP            string `json:"ip" binding:"required"`             // 目标机器 IP
    SSHUser       string `json:"ssh_user" binding:"required"`       // SSH 用户名
    SSHPassword   string `json:"ssh_password" binding:"required"`   // SSH 密码
    SSHPort       int    `json:"ssh_port"`                          // SSH 端口，默认 22
    Hostname      string `json:"hostname"`                          // 节点名称，默认用 IP 生成
    NetworkDevice string `json:"network_device" binding:"required"` // VXLAN 物理网卡
    VlanDevice    string `json:"vlan_device"`                       // VLAN 网卡，默认同 network_device
    Domain        string `json:"domain"`                            // 域名
    DNSServer     string `json:"dns_server"`                        // DNS
    ZoneName      string `json:"zone_name"`                         // 可用区，默认 zone0
}
```

流程：
1. **权限检查**：`memberShip.CheckPermission(model.Admin)` — 仅管理员可操作
2. **分配 ID**：调用 `AllocateHostID()` 获取递增唯一 ID
3. **创建数据库记录**：状态设为 `deploying`(4)，预占 Hostid
4. **异步执行部署**（`go func()` 避免 HTTP 请求超时）：
   a. SSH 连接到目标机器
   b. 上传/生成 `compute.env` 配置文件（基于请求参数 + 分配的 ID）
   c. 执行部署脚本（已在控制面容器中，需要先 scp 到目标机器）
   d. 部署成功后：调用 `NodeAdd(hostname, hostID)` 注册到 SCI 拓扑，更新状态为 `active`(1)
   e. 部署失败：更新状态为 `deploy_failed`(5)，记录失败原因到 `Remark`
5. **立即返回**：返回 hyper 记录（状态为 deploying），前端可轮询状态

```go
func (a *HyperAdmin) Deploy(ctx context.Context, req DeployHyperRequest) (hyper *model.Hyper, err error) {
    // 1. 权限检查
    memberShip := GetMemberShip(ctx)
    if !memberShip.CheckPermission(model.Admin) {
        return nil, NewCLError(ErrPermissionDenied, "Admin only", nil)
    }

    // 2. 分配递增唯一 ID
    hostID, err := a.AllocateHostID(ctx)
    if err != nil {
        return nil, err
    }

    // 3. 设置默认值
    if req.SSHPort == 0 { req.SSHPort = 22 }
    if req.Hostname == "" { req.Hostname = fmt.Sprintf("hyper-%s", strings.ReplaceAll(req.IP, ".", "-")) }
    if req.VlanDevice == "" { req.VlanDevice = req.NetworkDevice }
    if req.ZoneName == "" { req.ZoneName = "zone0" }
    if req.Domain == "" { req.Domain = "cloud.local" }
    if req.DNSServer == "" { req.DNSServer = "8.8.8.8" }

    // 4. 预创建数据库记录
    _, db := GetContextDB(ctx)
    zone := &model.Zone{Name: req.ZoneName}
    db.Where("name = ?", req.ZoneName).FirstOrCreate(zone)

    hyper = &model.Hyper{
        Hostid:   hostID,
        Hostname: req.Hostname,
        HostIP:   req.IP,
        Status:   4,  // deploying
        ZoneID:   zone.ID,
        Remark:   "Deployment in progress...",
    }
    if err = db.Create(hyper).Error; err != nil {
        return nil, err
    }

    // 5. 获取控制面 IP（从配置或环境变量）
    controllerIP := viper.GetString("sci.controller_ip")

    // 6. 异步部署
    go func() {
        sshCfg := &SSHConfig{
            Host: req.IP, Port: req.SSHPort,
            User: req.SSHUser, Password: req.SSHPassword,
        }

        // 构建远程执行的环境变量 + 部署命令
        deployCmd := fmt.Sprintf(
            `export CONTROLLER_IP="%s" HOSTNAME="%s" NETWORK_DEVICE="%s" VLAN_DEVICE="%s" `+
            `DOMAIN="%s" DNS_SERVER="%s" SCI_CLIENT_ID=%d ZONE_NAME="%s" && `+
            `curl -sf "http://%s:5006/internal/deploy-script" -o /tmp/deploy-compute-node.sh && `+
            `bash /tmp/deploy-compute-node.sh`,
            controllerIP, req.Hostname, req.NetworkDevice, req.VlanDevice,
            req.Domain, req.DNSServer, hostID, req.ZoneName,
            controllerIP,
        )

        output, err := sshCfg.RunCommand(deployCmd, 30*time.Minute)

        db := dbs.DB()
        if err != nil {
            logger.Errorf("Deploy hyper %s failed: %v, output: %s", req.IP, err, output)
            db.Model(&model.Hyper{}).Where("hostid = ?", hostID).Updates(map[string]interface{}{
                "status": 5,  // deploy_failed
                "remark": fmt.Sprintf("Deploy failed: %v\n%s", err, truncate(output, 500)),
            })
            return
        }

        // 部署成功 → 注册到 SCI 拓扑
        _, addErr := NodeAdd(req.Hostname, hostID)
        if addErr != nil {
            logger.Errorf("Deploy succeeded but SCI registration failed: %v", addErr)
            db.Model(&model.Hyper{}).Where("hostid = ?", hostID).Updates(map[string]interface{}{
                "status": 5,
                "remark": fmt.Sprintf("Deploy OK but SCI registration failed: %v", addErr),
            })
            return
        }

        db.Model(&model.Hyper{}).Where("hostid = ?", hostID).Updates(map[string]interface{}{
            "status": 1,  // active
            "remark": "Deployed successfully",
        })
        logger.Infof("Hyper %s (ID=%d) deployed and registered successfully", req.Hostname, hostID)
    }()

    return hyper, nil
}
```

### 3f: 退役工作流

**文件**: `web/src/routes/hyper.go` — 新增方法：

**`Decommission(ctx, hostID, targetHyper)`**:
1. 设 status=2（draining）—— `GetHyperGroup()` 已有 `status=1` 过滤，立即停止调度
2. 查询节点上活跃实例
3. 对每个实例调用已有的 `migrationAdmin.Create()` 迁移
4. 返回迁移任务列表

**`CompleteDecommission(ctx, hostID)`**:
1. 检查无剩余实例
2. 调用 `NodeRemove(hostID)` 从 SCI 拓扑移除
3. 设 status=3（decommissioned）
4. 清理 Resource 记录

### 3g: REST API 端点

**文件**: `web/src/apis/hyper.go`

```go
// POST /api/v1/hypers                                    — 部署新 hyper（Admin only）
func (v *HyperAPI) Deploy(c *gin.Context)

// POST /api/v1/hypers/:hostid/decommission               — 开始退役
func (v *HyperAPI) Decommission(c *gin.Context)

// POST /api/v1/hypers/:hostid/complete-decommission      — 完成退役
func (v *HyperAPI) CompleteDecommission(c *gin.Context)
```

**文件**: `web/src/apis/routes.go`（第 73 行后）

```go
authGroup.POST("/api/v1/hypers", hyperAPI.Deploy)
authGroup.POST("/api/v1/hypers/:hostid/decommission", hyperAPI.Decommission)
authGroup.POST("/api/v1/hypers/:hostid/complete-decommission", hyperAPI.CompleteDecommission)
```

### 3h: cland 端提供部署脚本下载

**文件**: `src/rpcworker.cpp` — 新增 HTTP 端点：

```cpp
// GET /internal/deploy-script — 返回 deploy-compute-node.sh 脚本内容
http.Get("/internal/deploy-script", [](const Request &req, Response &res) {
    // 读取 /opt/cloudland/deploy/docker/scripts/deploy-compute-node.sh
    // 返回脚本内容
});
```

### 3i: Web UI 路由

**文件**: `web/src/routes/routes.go`（第 113 行后）

```go
m.Post("/hypers/deploy", hyperView.Deploy)
m.Post("/hypers/:id/decommission", hyperView.Decommission)
m.Post("/hypers/:id/complete_decommission", hyperView.CompleteDecommission)
```

---

## Phase 4: 部署脚本 — 移除所有 host.list 依赖

### 4a: cloudland-entrypoint.sh — 删除 host.list 处理

**文件**: `deploy/docker/scripts/cloudland-entrypoint.sh`

删除第 12-22 行所有 host.list 相关逻辑。

### 4b: deploy-compute-node.sh — 移除 host.list 写入 + 改为 API 注册

**文件**: `deploy/docker/scripts/deploy-compute-node.sh`

将第 553-570 行改为：

```bash
# ============ 15. 通过 API 注册到控制面 ============
log "15/15 - 通过 API 向控制面注册计算节点"
for i in $(seq 1 5); do
    if curl -sf -X POST "http://${CONTROLLER_IP}:5006/internal/node/add" \
        -H "Content-Type: application/json" \
        -d "{\"hostname\": \"${HOSTNAME}\", \"id\": ${SCI_CLIENT_ID}, \"level\": 1}"; then
        log "节点注册成功"
        break
    fi
    warn "注册失败，${i}/5 次重试..."
    sleep 5
done
```

### 4c: deploy-control-node.sh — 移除 host.list 初始化

删除第 74-78 行。

### 4d: deploy-ha-node.sh — 移除 host.list 同步

删除第 44-45 行和第 169-171 行。

### 4e: docker-compose.yml — 移除 host.list volume 挂载

删除第 69 行 `- ./volumes/host.list:/opt/cloudland/etc/host.list`。

### 4f: README.md — 更新文档

移除所有 host.list 描述，添加 API 部署方式说明。

---

## 完整的 Hyper 生命周期

```
                    POST /api/v1/hypers
                    (ip, user, password, network_device, ...)
                           │
                           ▼
                    ┌──────────────┐
                    │  deploying   │ (status=4)
                    │  SSH 执行部署  │
                    └──────┬───────┘
                      成功 │    │ 失败
                           ▼    ▼
                    ┌────────┐  ┌──────────────┐
           ┌───────│ active  │  │ deploy_failed │ (status=5)
           │       │(status=1)│  └──────────────┘
           │       └────┬───┘      可重试部署
           │            │
           │  PATCH (status=0)
           │            ▼
           │       ┌──────────┐
           │       │ disabled │ (status=0) 暂停调度
           │       └──────────┘
           │
           │  POST /hypers/:id/decommission
           │            │
           │            ▼
           │       ┌──────────┐
           │       │ draining │ (status=2) 排空实例
           │       └────┬─────┘
           │            │ 实例迁移完毕
           │            ▼
           │  POST /hypers/:id/complete-decommission
           │            │
           │            ▼
           │       ┌────────────────┐
           └──────▶│ decommissioned │ (status=3)
                   │ SCI_BE_remove  │
                   └────────────────┘
```

---

## 实施顺序

| 步骤 | 内容 | 涉及文件 |
|------|------|---------|
| 1 | SCI 层：跳过 hostfile 加载 + height 保护 | `sci/libsci/topology.cpp`, `sci/libsci/bemap.cpp`（已完成） |
| 2 | NetLayer + rpcworker：去掉 hostfile + HTTP 端点 | `src/netlayer.hpp`, `src/netlayer.cpp`, `src/rpcworker.cpp` |
| 3 | Web SSH 执行器 | `web/src/common/ssh.go`（新建） |
| 4 | Hyper 模型 + ID 分配 + 部署工作流 | `web/src/model/hyper.go`, `web/src/routes/hyper.go` |
| 5 | REST API + Web UI 路由 | `web/src/apis/hyper.go`, `web/src/apis/routes.go`, `web/src/routes/routes.go` |
| 6 | 部署脚本清理 | 6 个文件 |

## 向后兼容

- 如果设环境变量 `SCI_HOST_FILE`，SCI 照旧加载走原有路径
- 只有当 hostfile 为 NULL 时才进入零后端 API 注册模式

## 验证方式

1. **零后端启动**：不传 hostfile 启动 cland，确认正常启动
2. **API 部署**：`POST /api/v1/hypers` 提交目标机器信息，验证 SSH 部署 + SCI 注册
3. **部署状态轮询**：`GET /api/v1/hypers/:hostid` 观察 deploying → active
4. **API 退役**：decommission → complete-decommission，验证实例迁移 + SCI 移除
5. **兼容模式**：设 `SCI_HOST_FILE` 启动，验证原有行为不变
