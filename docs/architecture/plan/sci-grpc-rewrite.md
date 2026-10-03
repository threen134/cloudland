# SCI C++ → Go gRPC 重写方案

## 1. 背景与动机

当前 CloudLand 控制面与计算节点之间的通信依赖 SCI（Scalable Communication Infrastructure），一套 ~20,000 行 C++ 自定义二进制协议框架，涉及三个独立进程：

| 进程 | 角色 | 问题 |
|------|------|------|
| cland (C++) | 消息路由器，运行在容器内 | 自定义二进制协议，调试困难 |
| cloudlet (C++) | 计算节点 agent，systemd 服务 | 需编译 SCI 库，部署复杂 |
| scidv1 | 外部启动器守护进程 (:6188) | 两阶段握手机制脆弱，易被旧控制面抢注 |

**核心痛点：**
- scidv1 的 REGISTER/REQUEST 握手依赖 jobKey + nodeID 匹配，任何异地控制面都能抢注
- 计算节点部署需编译 SCI C++ 库（autoconf/make），耗时且易失败
- 自定义 Packer 二进制序列化，没有 schema 描述，难以调试和扩展
- 调度器以 .so 动态库形式加载，无法单独测试

**目标：** 用 Go + gRPC 重写整个 SCI 层，消除所有 C++ 依赖，简化部署。保持现有架构语义不变——单 cland 管理所有节点，Zone 仍为 clapi 层的逻辑分组概念。

## 2. 架构对比

### 2.1 当前架构

```
clapi (Go, :8255/:5005)
  │
  │ HTTP POST /internal/execute
  │ HTTP POST /internal/node/add
  │ HTTP POST /internal/node/remove
  ▼
cland (C++, :5006 HTTP, :9988 SCI listener)     ← 容器内，network_mode: host
  │
  │ SCI 二进制协议（自定义 Packer 序列化）
  │ 经 scidv1 (:6188) 两阶段握手建立连接
  ▼
cloudlet (C++, systemd)                          ← 每台计算节点
  │
  │ popen("sudo -E script.sh 2>&1")
  │ SCI_Upload 逐行回传 stdout
  ▼
scripts/kvm/*.sh                                 ← 100+ hypervisor 操作脚本（不变）
```

**回调链路：**
```
script stdout → cloudlet SCI_Upload → cland frontHandler
  → HTTP POST /internal/execute → clapi frontback.go → RPC handler (e.g. LaunchVM)
```

### 2.2 新架构

```
clapi (Go, :8255/:5005)
  │
  │ gRPC (ClandService)
  ▼
cland-go (Go, :5006 gRPC)                       ← 容器内，单实例管理所有节点
  │
  │ gRPC 双向 stream (CloudletService.CommandStream)
  │ cloudlet 主动拨入，无需 scidv1
  ▼
cloudlet-go (Go, systemd)                        ← 每台计算节点，单个静态二进制
  │
  │ exec.Command("sudo", "-E", script, args...)
  │ bufio.Scanner 逐行回传 stdout
  ▼
scripts/kvm/*.sh                                 ← 不变
```

**回调链路（不变）：**
```
script stdout → cloudlet-go gRPC stream → cland-go
  → HTTP POST /internal/execute → clapi frontback.go → RPC handler
```

**关键设计原则：**
- **cland-go 不感知 Zone。** Zone 仍然是 clapi 层的逻辑分组概念（DB 中 `hypers.zone_id`），clapi 通过 `GetHyperGroup()` 查询同 Zone 内的候选节点，构建 `select=group-zone-{id}:{hostid1},{hostid2}` 控制字符串。cland-go 只按节点 ID 路由，不关心节点属于哪个 Zone。
- **与现有架构语义完全一致。** 跨 Zone 的 VRRP 分组（`toall=group-vrrp-{id}:{hyper1},{hyper2}`）、FDB 规则广播等操作无需任何特殊处理，因为所有节点都连在同一个 cland-go 上。
- **扩展通过 Region。** 每个 Region 是独立的 clapi + cland-go + DB。需要管理更多数据中心时，增加 Region 即可。gRPC 单进程可轻松支撑数千并发 stream，单 Region 内不需要分片。

### 2.3 消除的组件

| 组件 | 行数 | 替代方案 |
|------|------|---------|
| sci/libsci/ | 11,793 | gRPC 标准库 |
| sci/scid/ (scidv1) | 1,111 | cloudlet 直连 cland，无需中转 |
| sci/common/ | 3,966 | Go 标准库 + protobuf |
| src/cloudland.cpp + rpcworker.cpp | ~840 | cland-go (~600 行) |
| src/cloudlet.cpp | 328 | cloudlet-go (~400 行) |
| src/filter/scheduler.cpp + rcmanager.cpp | ~500 | cland-go 内置调度器 (~150 行) |
| **合计消除** | **~18,500** | **预估新增 ~3,000-5,000 行 Go** |

### 2.4 不变的组件

- `api/src/rpcs/` — 35+ RPC 回调处理器（LaunchVM, HyperStatus, InstanceStatus 等）
- `api/src/rpcs/frontback.go` — 回调分发机制
- `api/src/common/instance.go` — `GetHyperGroup()` Zone 候选节点查询（完全不变）
- `scripts/kvm/` — 100+ hypervisor 操作脚本
- `scripts/backend/report_rc.sh` — 健康上报脚本
- `|:-COMMAND-:|` 回调格式 — 脚本输出协议
- CPGateway, Frontend, Database — 完全不涉及

## 3. Proto 定义

文件位置：`api/proto/cloudland/v1/cloudland.proto`

> **模块归属：** proto 定义放在 `api/` 目录下，生成的 Go 代码位于 `api/src/proto/cloudlandpb/`。
> cland-go 和 cloudlet-go 也放在 `api/` 的 go.mod 下（`api/src/cland/`、`api/src/cloudlet-go/`），
> 避免多 go.mod 的跨模块依赖问题。

```protobuf
syntax = "proto3";
package cloudland.v1;
option go_package = "api/src/proto/cloudlandpb";

// ====================================================================
// ClandService: clapi → cland（控制面内部通信）
// 替代当前 cland 的 3 个 HTTP 端点
// ====================================================================
service ClandService {
  // 替代 POST /internal/execute
  rpc Execute(ExecuteRequest) returns (ExecuteReply);

  // 替代 POST /internal/node/add
  rpc NodeAdd(NodeAddRequest) returns (NodeAddReply);

  // 替代 POST /internal/node/remove
  rpc NodeRemove(NodeRemoveRequest) returns (NodeRemoveReply);

  // 文件传输（控制面 → 计算节点）
  // 注意：当前 C++ 实现中此功能已停用——rpcworker.cpp 的 Transmit() 方法被注释掉，
  // clapi 中也没有 TransmitFile 的 Go API 封装。cloudlet 端的 type=file 接收逻辑仍有效。
  // 新设计恢复此功能，通过 gRPC stream 重新实现文件传输通道。
  rpc TransmitFile(stream FileChunk) returns (TransmitAck);

  // 分组管理
  rpc ListGroups(ListGroupsRequest) returns (ListGroupsReply);
}

message ExecuteRequest {
  int32  id       = 1;  // 消息 ID（通常 100）
  int32  extra    = 2;  // 附加参数（节点 ID 或优先级）
  string control  = 3;  // 路由指令，如 "inter=5", "select=zone0:1,2 cpu=4"
  string command  = 4;  // Shell 命令
  string trace_id = 5;  // 请求追踪 ID
}

message ExecuteReply {
  string status = 1;
}

message NodeAddRequest {
  string hostname = 1;
  int32  id       = 2;
  int32  level    = 3;
}

message NodeAddReply {
  string status      = 1;
  int32  id          = 2;
  string public_key  = 3;
  string private_key = 4;
}

message NodeRemoveRequest {
  int32 id = 1;
}

message NodeRemoveReply {
  string status = 1;
}

message FileChunk {
  int32  id       = 1;
  string control  = 2;
  string filepath = 3;
  int64  filesize = 4;
  int32  checksum = 5;
  int64  fileseek = 6;
  bytes  content  = 7;
  int32  extra    = 8;
}

message TransmitAck {
  string status = 1;
}

message ListGroupsRequest {}
message ListGroupsReply {
  repeated string groups = 1;
}

// ====================================================================
// CloudletService: cloudlet → cland（agent 注册 + 命令流）
// 替代整个 SCI 协议 + scidv1 握手
// ====================================================================
service CloudletService {
  // 双向 stream：cland 下发命令，cloudlet 回传结果
  // 替代 SCI_Initialize + SCI_Upload + backHandler
  // cloudlet 主动拨入 cland 建立此 stream，首条消息为 RegisterRequest
  rpc CommandStream(stream CloudletMessage) returns (stream ClandMessage);

  // 健康上报（独立 RPC，不占用命令 stream）
  rpc ReportHealth(HealthReport) returns (HealthReportAck);
}

// === cloudlet → cland 上行消息 ===
message CloudletMessage {
  oneof payload {
    RegisterRequest register = 1;
    CommandResult   result   = 2;
    CallbackLine    callback = 3;
    ErrorReport     error    = 4;
  }
}

message RegisterRequest {
  int32  node_id   = 1;  // SCI_CLIENT_ID
  string hostname  = 2;
}

message CommandResult {
  int32  msg_id   = 1;
  int32  node_id  = 2;
  string control  = 3;  // "callback"
  string output   = 4;  // stdout 单行
  string trace_id = 5;
}

message CallbackLine {
  int32  msg_id   = 1;
  int32  node_id  = 2;
  string command  = 3;  // 从 |:-COMMAND-:| 解析出的命令
  string trace_id = 4;
}

message ErrorReport {
  int32  msg_id    = 1;
  int32  node_id   = 2;
  string command   = 3;
  int32  exit_code = 4;
  string trace_id  = 5;
}

// === cland → cloudlet 下行消息 ===
message ClandMessage {
  oneof payload {
    RegisterAck      register_ack = 1;
    CommandRequest   command      = 2;
    FileTransfer     file         = 3;
    ShutdownRequest  shutdown     = 4;  // 通知 cloudlet 优雅退出
  }
}

// 替代 C++ 中 SCI_Query(HEALTH_STATUS) 的退出机制
// cland-go 在 NodeRemove 或 term= 时通过 stream 发送此消息
message ShutdownRequest {
  string reason = 1;  // 如 "node_removed", "cland_shutdown"
}

message RegisterAck {
  bool   success = 1;
  string error   = 2;
}

message CommandRequest {
  int32  msg_id   = 1;
  int32  extra    = 2;
  string control  = 3;
  string command  = 4;
  string trace_id = 5;
}

message FileTransfer {
  int32  msg_id   = 1;
  string filepath = 2;
  int64  filesize = 3;
  int64  fileseek = 4;
  bytes  content  = 5;
  int32  checksum = 6;
}

message HealthReport {
  int32  node_id            = 1;
  string hostname           = 2;
  int64  cpu_available      = 3;
  int64  cpu_total          = 4;
  int64  memory_available   = 5;
  int64  memory_total       = 6;
  int64  disk_available     = 7;
  int64  disk_total         = 8;
  int64  network_available  = 9;
  int64  network_total      = 10;
  int64  load_available     = 11;
  int64  load_total         = 12;
  string raw_report         = 13;  // report_rc.sh 第一行原始输出（cpu=X/Y memory=X/Y ...）
  repeated string callback_lines = 14;  // 后续行（|:-COMMAND-:| 格式，如 hyper_status.sh）
}

message HealthReportAck {
  bool accepted = 1;
}
```

## 4. 组件设计

### 4.1 cland-go（新 Go cland）

**替代：** C++ cloudland + scidv1 + scheduler.so

**目录结构：**
```
api/src/cland/
  main.go               — 入口，gRPC server 启动
  server.go             — ClandService 实现（Execute, NodeAdd, NodeRemove）
  cloudlet_server.go    — CloudletService 实现（CommandStream, ReportHealth）
  node_registry.go      — 已连接节点注册表（nodeID → stream 映射）
  group_manager.go      — 分组 CRUD（替代 NetLayer groupMap）
  scheduler.go          — 资源调度器（替代 scheduler.so / rcmanager）
  dispatcher.go         — control 字符串解析 + 消息路由
  callback.go           — 回调转发到 clapi（HTTP POST /internal/execute）
  config.go             — 配置加载
```

**环境变量（config.go）：**

| 环境变量 | 默认值 | 用途 | C++ 对应 |
|---------|--------|------|---------|
| `GRPC_LISTEN` | `0.0.0.0:5006` | gRPC 监听地址 | `RPC_SERVER_ENDPOINT` |
| `CLAPI_ENDPOINT` | `localhost:5005` | clapi 回调地址（HTTP POST） | `RPC_REMOTE_ENDPOINT` |
| `CLOUDLAND_SSH_KEY_DIR` | `/opt/cloudland/deploy/.ssh` | SSH 密钥目录（NodeAdd 时读取） | 同名 |
| `GRPC_AUTH_TOKEN` | （必填） | gRPC 共享令牌，clapi 和 cloudlet 连接时通过 metadata 传递 | 无（C++ 版本无认证） |
| 不再需要 | — | — | `CLOUDLET_PATH`（新架构 cloudlet 主动连接） |
| 不再需要 | — | — | `NODE_PERSIST_FILE`（cloudlet 自动重连，无需持久化） |
| 不再需要 | — | — | `SCHEDULE_SO_FILE`（调度器内置） |

**核心逻辑：**

#### 节点注册表 (node_registry.go)

```go
type NodeRegistry struct {
    mu      sync.RWMutex
    nodes   map[int32]*ConnectedNode  // nodeID → 连接信息
}

type ConnectedNode struct {
    ID       int32
    Hostname string
    Stream   CloudletService_CommandStreamServer
    Cancel   context.CancelFunc
}

func (r *NodeRegistry) Register(node *ConnectedNode) error
func (r *NodeRegistry) Unregister(nodeID int32)
func (r *NodeRegistry) Get(nodeID int32) (*ConnectedNode, bool)
func (r *NodeRegistry) GetAll() []*ConnectedNode
func (r *NodeRegistry) SendTo(nodeID int32, msg *ClandMessage) error
func (r *NodeRegistry) Broadcast(nodeIDs []int32, msg *ClandMessage)
```

**注意：** 注册表中没有 Zone 信息。cland-go 只维护 `nodeID → stream` 映射，不关心节点属于哪个 Zone。Zone 的候选节点过滤由 clapi 的 `GetHyperGroup()` 在发送 Execute 之前完成。

#### 节点持久化：不再需要 + 节点验证：启动全量拉取 + 内存缓存

当前 C++ cland 将已注册节点持久化到 `registered_nodes.json`（`rpcworker.cpp:309-367`），重启时逐个调用 `addBackend()` 恢复 SCI 连接（`rpcworker.cpp:548-563`）。这是因为 SCI 连接由 cland 主动发起——不持久化就不知道该连谁。

**新架构不需要持久化。** cloudlet-go 有永久重连循环，cland-go 重启后所有 cloudlet 会在几秒内自动重连并发送 `RegisterRequest`，节点注册表在内存中从零重建。即使有持久化文件，在 cloudlet 重连前 stream 为 nil，命令同样发不出去，所以持久化没有实际价值。

**但需要验证连接合法性。** 去掉持久化不代表任何 TCP 连接都可以注册。采用「启动全量拉取 + 内存缓存 + fallback 实时查询」方案：

**clapi 新增两个内部端点（`api/src/apis/hyper.go`）：**

```go
// GET /internal/nodes/valid — 返回所有合法 hostid 列表（cland-go 启动时调用一次）
router.GET("/internal/nodes/valid", func(c *gin.Context) {
    var hostIDs []int32
    db.Model(&model.Hyper{}).Where("hostid >= 0").Pluck("hostid", &hostIDs)
    c.JSON(200, hostIDs)  // [1, 2, 5, 12, ...]
})

// GET /internal/node/verify?id=<hostid> — 单节点验证（缓存未命中时 fallback）
router.GET("/internal/node/verify", func(c *gin.Context) {
    id, _ := strconv.Atoi(c.Query("id"))
    hyper := &model.Hyper{}
    err := db.Where("hostid = ?", id).Take(hyper).Error
    if err != nil {
        c.JSON(404, map[string]string{"status": "not_found"})
        return
    }
    c.JSON(200, map[string]interface{}{"status": "ok", "hostname": hyper.Hostname})
})
```

**cland-go 节点验证缓存（`cloudlet_server.go`）：**

```go
type Server struct {
    // ...其他字段...
    validNodes    sync.Map  // hostid(int32) → true，启动时从 clapi 全量拉取
    clapiEndpoint string
}

// 启动时异步调用，带重试从 clapi 拉取所有合法 hostid
// 如果 clapi 尚未就绪（容器编排中常见），会重试最多 10 次
func (s *Server) refreshValidNodesWithRetry() {
    go func() {
        for i := 0; i < 10; i++ {
            if err := s.refreshValidNodes(); err == nil {
                return
            }
            time.Sleep(time.Duration(i+1) * 3 * time.Second)
        }
        log.Printf("WARN: failed to preload valid nodes after retries, falling back to per-node verify")
    }()
}

func (s *Server) refreshValidNodes() error {
    resp, err := http.Get(s.clapiEndpoint + "/internal/nodes/valid")
    if err != nil {
        return fmt.Errorf("failed to fetch valid nodes: %w", err)
    }
    defer resp.Body.Close()
    var ids []int32
    if err := json.NewDecoder(resp.Body).Decode(&ids); err != nil {
        return fmt.Errorf("failed to decode valid nodes: %w", err)
    }
    for _, id := range ids {
        s.validNodes.Store(id, true)
    }
    log.Printf("Loaded %d valid nodes from clapi", len(ids))
    return nil
}

// 验证节点：先查内存缓存，未命中则 fallback 到 clapi 实时查询
func (s *Server) isNodeValid(nodeID int32) bool {
    // 1. 内存缓存命中 → 直接通过
    if _, ok := s.validNodes.Load(nodeID); ok {
        return true
    }
    // 2. 缓存未命中 → fallback 实时查询（处理启动后新增的节点）
    resp, err := http.Get(fmt.Sprintf("%s/internal/node/verify?id=%d", s.clapiEndpoint, nodeID))
    if err != nil {
        return false
    }
    defer resp.Body.Close()
    if resp.StatusCode != 200 {
        return false
    }
    // 查到了，加入缓存
    s.validNodes.Store(nodeID, true)
    return true
}

func (s *Server) CommandStream(stream pb.CloudletService_CommandStreamServer) error {
    // 第一条消息必须是 RegisterRequest
    msg, err := stream.Recv()
    if err != nil {
        return err
    }
    reg := msg.GetRegister()
    if reg == nil {
        return fmt.Errorf("first message must be RegisterRequest")
    }

    // 验证节点合法性（内存缓存 → fallback clapi）
    if !s.isNodeValid(reg.NodeId) {
        stream.Send(&pb.ClandMessage{
            Payload: &pb.ClandMessage_RegisterAck{
                RegisterAck: &pb.RegisterAck{Success: false, Error: "node not registered in hypers table"},
            },
        })
        log.Printf("Rejected node %d: not found in clapi", reg.NodeId)
        return fmt.Errorf("rejected unregistered node %d", reg.NodeId)
    }

    // 验证通过，注册到内存
    stream.Send(&pb.ClandMessage{
        Payload: &pb.ClandMessage_RegisterAck{
            RegisterAck: &pb.RegisterAck{Success: true},
        },
    })
    s.registry.Register(&ConnectedNode{ID: reg.NodeId, Hostname: reg.Hostname, Stream: stream})
    defer s.registry.Unregister(reg.NodeId)
    log.Printf("Node %d (%s) registered", reg.NodeId, reg.Hostname)

    // 进入命令收发主循环...
}
```

**缓存更新时机：**
- **启动时**：`refreshValidNodes()` 一次性拉取全量，重启后大批 cloudlet 重连时全部走缓存命中，零 HTTP 调用
- **NodeAdd 时**：cland-go 的 `NodeAdd` gRPC handler 执行后，顺便 `s.validNodes.Store(req.Id, true)`
- **NodeRemove 时**：cland-go 的 `NodeRemove` gRPC handler 执行后，`s.validNodes.Delete(req.Id)`
- **缓存未命中**：新节点在 cland-go 运行期间被添加但 NodeAdd 还没调到 cland-go 时，fallback 到实时查询兜底

**设计优势：**
- **重启零风暴**：几百个 cloudlet 重连只需 1 次 bulk HTTP 调用（而非 N 次）
- **天然同步**：NodeAdd / NodeRemove 的 gRPC handler 本来就在 cland-go 里，直接更新缓存，无需额外通知机制
- **Fallback 兜底**：缓存未命中不会误拒，只是多一次 HTTP 调用
- **实现轻量**：clapi 端 ~10 行，cland-go 端 ~30 行

**删除的文件/配置：** `NODE_PERSIST_FILE` 环境变量、`/opt/cloudland/cache/registered_nodes.json` 文件均不再需要。

#### NodeAdd SSH 密钥分发 + 缓存更新 (server.go)

当前 C++ NodeAdd 做两件事：(1) 通过 SCI 连接计算节点（`addBackend`），(2) 读取 SSH 密钥返回给 clapi。新架构中 (1) 不再需要——cloudlet 主动连接 cland-go。NodeAdd 的职责简化为**读取 SSH 密钥 + 更新验证缓存**（`rpcworker.cpp:469-489`）：

```go
func (s *Server) NodeAdd(ctx context.Context, req *pb.NodeAddRequest) (*pb.NodeAddReply, error) {
    log.Printf("NodeAdd: hostname=%s id=%d level=%d", req.Hostname, req.Id, req.Level)

    // 1. 更新验证缓存（cloudlet 重连时直接命中缓存）
    s.validNodes.Store(req.Id, true)

    // 2. 读取 SSH 密钥（CLOUDLAND_SSH_KEY_DIR，默认 /opt/cloudland/deploy/.ssh/）
    reply := &pb.NodeAddReply{Status: "ok", Id: req.Id}
    keyDir := os.Getenv("CLOUDLAND_SSH_KEY_DIR")
    if keyDir == "" { keyDir = "/opt/cloudland/deploy/.ssh" }
    if pubKey, err := os.ReadFile(keyDir + "/cland.key.pub"); err == nil {
        if privKey, err := os.ReadFile(keyDir + "/cland.key"); err == nil {
            reply.PublicKey = string(pubKey)
            reply.PrivateKey = string(privKey)
        }
    }
    return reply, nil
}

func (s *Server) NodeRemove(ctx context.Context, req *pb.NodeRemoveRequest) (*pb.NodeRemoveReply, error) {
    log.Printf("NodeRemove: id=%d", req.Id)

    // 1. 从验证缓存中移除
    s.validNodes.Delete(req.Id)

    // 2. 如果节点在线，发送 ShutdownRequest，取消 context，断开 stream
    if node, ok := s.registry.Get(req.Id); ok {
        node.Stream.Send(&pb.ClandMessage{
            Payload: &pb.ClandMessage_Shutdown{
                Shutdown: &pb.ShutdownRequest{Reason: "node_removed"},
            },
        })
        node.Cancel()  // 取消 CommandStream 的 context，触发主循环退出
        s.registry.Unregister(req.Id)
    }

    return &pb.NodeRemoveReply{Status: "ok"}, nil
}
```

#### 消息调度 (dispatcher.go)

解析 control 字符串，路由到正确的 cloudlet。**必须支持所有现有指令：**

| 指令 | 语义 | 路由方式 |
|------|------|---------|
| `inter=N` (N≥0) | 发送到指定节点 | `registry.SendTo(N, msg)` |
| `inter=N` (N<0) | 交给调度器选节点 | `scheduler.GetBestNode(...)` |
| `select=desc cpu=X mem=Y disk=Z` | 按资源需求选节点 | 创建候选组 → 调度器选择 |
| `group=name` | 发送到命名分组 | `groupMgr.GetMembers(name)` → 广播 |
| `toall=agent` | 发送到所有 agent | 广播到所有连接 + 返回节点拓扑 |
| `toall=desc` | 发送到描述匹配的组 | 解析描述 → 广播 |
| `mkgrp=desc` | 创建分组 | `groupMgr.Create(desc)` |
| `rmgrp=name` | 删除分组 | `groupMgr.Delete(name)` |
| `lsgrp=` | 列出所有分组（返回 JSON） | `groupMgr.List()` — 唯一直接返回 gRPC 响应的指令 |
| `callback` | 本地回调执行 | `exec.Command(...)` |
| `term=` | 服务终止信号 | 设置 `running=false`，优雅关闭 |
| `type=file` | 文件传输 | 通过 stream 发送 FileTransfer |

**回调方向的 control 值**（cloudlet → cland → clapi，由 frontHandler 处理）：

| control 值 | 语义 | 处理方式 |
|------------|------|---------|
| `callback` | 普通回调，解析 `\|:-COMMAND-:\|` | 逐条提取命令，转发到 clapi |
| `callback=agent` | Agent 回调，不解析命令标记 | 整条消息直接转发到 clapi |
| `report` | 健康上报响应 | 转发到 clapi（触发 hyper_status/report_rc handler） |
| `error` / `error=resource` | 错误响应（调度失败等） | 转发到 clapi |

**`select=` 的简化处理：** C++ 中 `select=` 会调用 `createGroup()` 创建一个 SCI group，再通过 SCHEDULE_FILTER 发送（`rpcworker.cpp:190-201`）。创建的临时 group 留在 groupMap 中不销毁，同名 select 会覆盖。新设计中 **不需要创建临时 group**——dispatcher 直接从 control 字符串解析候选节点列表（`select=group-zone-1:5,8,12` → candidates=[5,8,12]），传给 scheduler.GetBestNode() 选出最优节点，然后用 registry.SendTo() 定向发送。这比 C++ 实现更简洁，语义完全等价。

> **格式约定：** `select=` 的值总是由 clapi 的 `GetHyperGroup()` 生成，格式固定为 `group-{type}-{id}:{hostid1},{hostid2},...`（如 `group-zone-1:5,8,12`）。后接空格分隔的资源需求参数（`cpu=X memory=Y disk=Z`）。不存在其他调用方产生不同格式的 select。

#### 调度器 (scheduler.go)

直接移植 `src/filter/rcmanager.cpp` 的调度算法：

```go
type Scheduler struct {
    mu        sync.RWMutex
    resources map[int32]*Resource  // nodeID → 当前资源
    counter   int                  // round-robin 计数器
}

type Resource struct {
    CPU, CPUTotal         int64
    Mem, MemTotal         int64
    Disk, DiskTotal       int64
    Network, NetworkTotal int64
    Load, LoadTotal       int64
}

// UpdateResource 由 ReportHealth RPC 调用更新
func (s *Scheduler) UpdateResource(nodeID int32, r *Resource)

// GetBestNode 从候选节点中选择资源最充裕的节点
// 算法与 C++ rcmanager.cpp getBestBranch() 对齐：
//   round-robin 起点，找第一个满足所有资源条件的节点
//   评分公式：(1 - cpu需求/(cpu可用+1)) * (1 - mem需求/(mem可用+1))
//            * (1 - disk需求/(disk可用+1)) * (1 - network需求/(network可用+1)) > 0
// 调度失败时返回 error，cland-go 生成 error=resource 回调通知 clapi
func (s *Scheduler) GetBestNode(cpu, mem, disk, network int64, candidates []int32) (int32, error)
```

#### 回调转发 (callback.go)

当 cloudlet 通过 stream 发送 `CommandResult` 或 `CallbackLine` 时，cland-go 转发到 clapi：

```go
// 替代 C++ FrontBack::ExecuteAsync 的 HTTP POST
// 注意：当前 C++ 实现的 JSON 字段名为小写（id, extra, control, command），
// 且未转发 trace_id（现有 bug）。新实现统一使用 Go struct 序列化（首字母大写），
// 因为 frontback.go 的 json.Unmarshal 是 case-insensitive 的，两种都接受。
// 同时修复 trace_id 丢失问题，通过 RequestID header 传递。
func (s *Server) forwardCallback(msgID, nodeID int32, control, command, traceID string) {
    body := ExecuteRequest{
        Id:      msgID,
        Extra:   nodeID,
        Control: control,
        Command: command,
    }
    req, _ := http.NewRequest("POST", s.clapiEndpoint+"/internal/execute", ...)
    req.Header.Set("RequestID", traceID)  // 修复：C++ 版本未转发此 header
    s.httpClient.Do(req)
}
```

**兼容性保证：**
- `api/src/rpcs/frontback.go` 和所有 35+ RPC handler **零改动**
- Go 的 `json.Unmarshal` 对字段名 case-insensitive，兼容两种大小写
- 唯一行为变化：回调现在携带 `RequestID` header（之前 C++ 版本遗漏了这个）

### 4.2 cloudlet-go（新 Go cloudlet）

**替代：** C++ cloudlet + scidv1 依赖

**目录结构：**
```
api/src/cloudlet-go/
  main.go          — 入口，环境变量读取，gRPC 连接
  executor.go      — 命令执行（sudo -E）
  reporter.go      — 健康上报循环（report_rc.sh）
  parser.go        — |:-COMMAND-:| 行解析
  stream.go        — gRPC stream 管理
  reconnect.go     — 断线重连（指数退避）
```

**核心流程：**

```go
func main() {
    clandAddr := os.Getenv("CLAND_ENDPOINT")  // 如 "10.193.168.60:5006"
    nodeID    := os.Getenv("SCI_CLIENT_ID")
    hostname  := os.Getenv("HOSTNAME")

    for {  // 外层重连循环
        conn, _ := grpc.Dial(clandAddr, grpc.WithInsecure(), grpc.WithKeepaliveParams(...))
        client := pb.NewCloudletServiceClient(conn)
        stream, _ := client.CommandStream(ctx)

        // 注册（只发 nodeID 和 hostname，不发 zone 信息）
        stream.Send(&pb.CloudletMessage{
            Payload: &pb.CloudletMessage_Register{
                Register: &pb.RegisterRequest{NodeId: nodeID, Hostname: hostname},
            },
        })

        // 等待注册确认（cland-go 调 clapi 验证后返回）
        ackMsg, err := stream.Recv()
        if err != nil { break }
        ack := ackMsg.GetRegisterAck()
        if ack == nil || !ack.Success {
            log.Printf("Registration rejected: %s, retrying...", ack.GetError())
            conn.Close()
            time.Sleep(backoff())
            continue  // 外层重连循环
        }

        // 启动健康上报 goroutine
        go healthReportLoop(client, nodeID, hostname)

        // 主循环：接收命令，执行，回传结果
        for {
            msg, err := stream.Recv()
            if err != nil { break }  // stream 断了，外层循环重连

            switch p := msg.Payload.(type) {
            case *pb.ClandMessage_Command:
                go executeCommand(stream, p.Command)
            case *pb.ClandMessage_File:
                go receiveFile(p.File)
            case *pb.ClandMessage_Shutdown:
                // 优雅退出（替代 C++ 的 SCI_Query(HEALTH_STATUS) 机制）
                // 不直接 os.Exit — 等待活跃命令完成后再退出
                log.Printf("Shutdown requested: %s, waiting for active commands...", p.Shutdown.Reason)
                cancel()  // 取消 context，通知所有 executeCommand goroutine 停止接受新任务
                wg.Wait() // 等待正在执行的命令完成（executor 中用 wg.Add/Done 跟踪）
                conn.Close()
                os.Exit(0)
            }
        }
        conn.Close()
        time.Sleep(backoff())  // 指数退避重连
    }
}
```

**命令执行（executor.go）：**

```go
// 命令前置校验 — 移植自 C++ cloudlet.cpp backHandler 的 guard 逻辑
func shouldExecute(req *pb.CommandRequest) bool {
    ctl := req.Control
    // toall=agent 消息由 cloudlet 跳过（不执行），仅用于拓扑上报
    if strings.Contains(ctl, "toall=agent") {
        return false
    }
    // inter= 值无效时丢弃
    if idx := strings.Index(ctl, "inter="); idx >= 0 {
        val := ctl[idx+len("inter="):]
        if i := strings.IndexAny(val, " \t"); i >= 0 { val = val[:i] }
        n, err := strconv.Atoi(val)
        if err != nil || n < 0 {
            return false
        }
    }
    return true
}

func executeCommand(stream pb.CloudletService_CommandStreamClient, req *pb.CommandRequest) {
    if !shouldExecute(req) {
        return
    }

    cmd := exec.Command("sudo", "-E", "bash", "-c", req.Command)
    cmd.Env = append(os.Environ(), "RequestID="+req.TraceId)
    stdout, _ := cmd.StdoutPipe()
    cmd.Start()

    scanner := bufio.NewScanner(stdout)
    for scanner.Scan() {
        line := scanner.Text()

        if strings.Contains(line, "|:-COMMAND-:|") {
            // 解析回调命令
            command := parseCallback(line)
            stream.Send(&pb.CloudletMessage{
                Payload: &pb.CloudletMessage_Callback{...},
            })
        } else {
            // 普通输出
            stream.Send(&pb.CloudletMessage{
                Payload: &pb.CloudletMessage_Result{...},
            })
        }
    }

    if err := cmd.Wait(); err != nil {
        // 非零退出码，发送错误报告
        stream.Send(&pb.CloudletMessage{
            Payload: &pb.CloudletMessage_Error{...},
        })
    }
}
```

**健康上报（reporter.go）：**

当前 C++ cloudlet 的 report_rc.sh 执行流程比较特殊（`cloudlet.cpp:259-311`）：
1. 第一行用 `control="report"` 通过 `SCHEDULE_FILTER` 发送 → scheduler 的 `filter_input()` 拦截，解析 `cpu=X/Y memory=X/Y disk=X/Y network=X/Y load=X/Y` 格式更新资源表
2. 后续行用 `control="callback"` 通过 `SCI_FILTER_NULL` 发送 → 走正常回调链路到 clapi（如 `hyper_status.sh`、`report_rc.sh` 回调）
3. 每次执行间隔 `random() % 20 + 1` 秒（1-20 秒随机）

新的 cloudlet-go 对应实现：

```go
func healthReportLoop(client pb.CloudletServiceClient, stream pb.CloudletService_CommandStreamClient, nodeID int32, hostname string) {
    for {
        // 执行 report_rc.sh
        cmd := exec.Command("sudo", "-E", "/opt/cloudland/scripts/backend/report_rc.sh")
        stdout, _ := cmd.StdoutPipe()
        cmd.Start()

        scanner := bufio.NewScanner(stdout)
        var callbackLines []string

        // 第一行是资源报告（cpu=X/Y memory=X/Y ...）
        if scanner.Scan() {
            firstLine := scanner.Text()
            report := parseResourceReport(firstLine)  // 解析 key=avail/total 格式
            report.NodeId = nodeID
            report.Hostname = hostname
            report.RawReport = firstLine

            // 后续行是 |:-COMMAND-:| 回调
            for scanner.Scan() {
                callbackLines = append(callbackLines, scanner.Text())
            }
            report.CallbackLines = callbackLines

            // 发送 ReportHealth RPC（独立 unary RPC，不占用 CommandStream）
            client.ReportHealth(ctx, report)
        }
        cmd.Wait()

        // cland-go 收到 ReportHealth 后：
        // 1. 用 report 中的结构化字段更新 scheduler 资源表
        // 2. 将 callback_lines 逐条通过 HTTP POST 转发到 clapi（与普通回调相同路径）

        time.Sleep(time.Duration(rand.Intn(20)+1) * time.Second)
    }
}

// 解析 report_rc.sh 第一行：cpu=avail/total memory=avail/total ...
func parseResourceReport(line string) *pb.HealthReport {
    report := &pb.HealthReport{}
    for _, field := range strings.Fields(line) {
        parts := strings.SplitN(field, "=", 2)
        if len(parts) != 2 { continue }
        vals := strings.SplitN(parts[1], "/", 2)
        avail, _ := strconv.ParseInt(vals[0], 10, 64)
        total := avail
        if len(vals) == 2 { total, _ = strconv.ParseInt(vals[1], 10, 64) }
        switch parts[0] {
        case "cpu":     report.CpuAvailable, report.CpuTotal = avail, total
        case "memory":  report.MemoryAvailable, report.MemoryTotal = avail, total
        case "disk":    report.DiskAvailable, report.DiskTotal = avail, total
        case "network": report.NetworkAvailable, report.NetworkTotal = avail, total
        case "load":    report.LoadAvailable, report.LoadTotal = avail, total
        }
    }
    return report
}
```

**cland-go 端 ReportHealth 处理（cloudlet_server.go）：**

```go
func (s *Server) ReportHealth(ctx context.Context, r *pb.HealthReport) (*pb.HealthReportAck, error) {
    // 1. 更新调度器资源表
    s.scheduler.UpdateResource(r.NodeId, &Resource{
        CPU: r.CpuAvailable, CPUTotal: r.CpuTotal,
        Mem: r.MemoryAvailable, MemTotal: r.MemoryTotal,
        Disk: r.DiskAvailable, DiskTotal: r.DiskTotal,
        Network: r.NetworkAvailable, NetworkTotal: r.NetworkTotal,
        Load: r.LoadAvailable, LoadTotal: r.LoadTotal,
    })

    // 2. 将 raw_report 以 control="report" 转发到 clapi（触发 report_rc handler）
    //    C++ 中第一行经 SCHEDULE_FILTER 后仍到达 frontHandler，以 control="report" 转发
    if r.RawReport != "" {
        s.forwardCallback(100, r.NodeId, "report", r.RawReport, "")
    }

    // 3. 将 callback_lines 逐条以 control="callback" 转发到 clapi
    for _, line := range r.CallbackLines {
        s.forwardCallback(100, r.NodeId, "callback", line, "")
    }

    return &pb.HealthReportAck{Accepted: true}, nil
}
```

**对比当前 C++ cloudlet：**
- 不需要 SCI 库、不需要 scidv1、不需要 Packer 序列化
- 环境变量 `CLAND_ENDPOINT` 替代 scidv1 的 REGISTER/REQUEST 握手
- gRPC keepalive 替代 SCI enable_recover
- 部署时只需下载一个静态 Go 二进制文件
- 健康上报的 SCHEDULE_FILTER 机制由 ReportHealth 独立 RPC 替代，更清晰

### 4.3 clapi 改动（最小化）

**仅修改 1 个文件：** `api/src/common/clients.go`

当前代码（HTTP POST）：
```go
func HyperExecute(ctx context.Context, control, command string) error {
    endpoint := viper.GetString("sci.endpoint") + "/internal/execute"
    body := ExecuteRequest{Id: 100, Extra: 0, Control: control, Command: command}
    resp, err := http.Post(endpoint, "application/json", ...)
    ...
}
```

改为 gRPC：
```go
var clandClient pb.ClandServiceClient  // 单例，启动时初始化

func HyperExecute(ctx context.Context, control, command string) error {
    _, err := clandClient.Execute(ctx, &pb.ExecuteRequest{
        Id: 100, Control: control, Command: command, TraceId: getTraceID(ctx),
    })
    return err
}
```

`NodeAdd` 和 `NodeRemove` 同样从 HTTP POST 改为 gRPC 调用。

**不改动的文件：**
- `api/src/rpcs/frontback.go` — 回调分发逻辑完全不变
- `api/src/rpcs/*.go` — 所有 35+ RPC handler 完全不变
- `api/src/services/*.go` — 业务逻辑层不变
- `api/src/common/instance.go` — `GetHyperGroup()` Zone 过滤逻辑不变

## 5. 完整消息流转

以创建 VM 为例：

```
1. 用户请求
   POST /api/v1/instances → clapi instance.go Create()

2. clapi 查询同 Zone 候选节点（不变）
   GetHyperGroup(ctx, zoneID, -1) → "group-zone-1:1,2,3"
   control = "select=group-zone-1:1,2,3 cpu=4 memory=8192 disk=102400"
   command = "/opt/cloudland/scripts/backend/launch_vm.sh '15' 'image.qcow2' ..."

3. clapi → cland-go (gRPC，替代 HTTP POST)
   ClandService.Execute(ExecuteRequest{control, command, trace_id})

4. cland-go 解析 control（与 C++ 逻辑一致）
   dispatcher.go: 检测到 select= → 创建候选组 [1,2,3] → 解析资源需求
   scheduler.go: GetBestNode(cpu=4, mem=8192, disk=102400, network=0, candidates=[1,2,3])
   → 选中 nodeID=2

5. cland-go → cloudlet (gRPC stream)
   通过 nodeID=2 的 CommandStream 发送:
   ClandMessage{Command: CommandRequest{msg_id=100, command="launch_vm.sh ..."}}

6. cloudlet 执行
   exec.Command("sudo", "-E", "bash", "-c", "launch_vm.sh ...")
   逐行读取 stdout

7. cloudlet → cland-go (gRPC stream)
   每行 stdout → CloudletMessage{Result: CommandResult{output="..."}}
   |:-COMMAND-:| 行 → CloudletMessage{Callback: CallbackLine{command="launch_vm.sh '15' 'running' '2'"}}
   非零退出 → CloudletMessage{Error: ErrorReport{exit_code=1}}

8. cland-go → clapi (HTTP POST，与现有格式完全相同)
   POST http://clapi:5005/internal/execute
   Header: RequestID: <trace_id>
   {"Id":100, "Extra":2, "Control":"callback", "Command":"launch_vm.sh '15' 'running' '2'"}

9. clapi 回调处理（不变）
   frontback.go 解析命令名 "launch_vm" → 调用 rpcs/launch_vm.go LaunchVM()
   → 更新数据库中 instance 状态为 running
```

## 6. 扩展策略

### 6.1 当前拓扑（不变）

```
                         CPGateway (全局)
                           │
               ┌───────────┼───────────┐
               ▼           ▼           ▼
          Region-A     Region-B     Region-C
         (tokyo-05)   (osaka-01)   (beijing)
            │              │           │
        clapi + cland   clapi + cland  ...
            │              │
      ┌─────┼─────┐    ┌──┼──┐
      ▼     ▼     ▼    ▼     ▼
   node-1 node-2 node-3 node-4 node-5
  (zone-a)(zone-a)(zone-b)
```

- **Region = 数据中心**，独立的 clapi + cland-go + DB
- **Zone = Region 内的逻辑分组**，clapi 用 `GetHyperGroup()` 按 Zone 过滤候选节点
- **cland-go 不感知 Zone**，所有节点扁平连接

### 6.2 为什么不需要 cland 分片

| 维度 | 说明 |
|------|------|
| **连接数** | gRPC 单进程轻松支撑数千并发 stream。一个 cloudlet = 一条 stream，远比 SCI 高效 |
| **消息吞吐** | gRPC 基于 HTTP/2 多路复用，比 SCI 自定义 TCP 协议吞吐更高 |
| **调度负载** | 调度器只在收到 `select=` 时做一次选择，内存中操作，微秒级 |
| **跨 Zone 操作** | VRRP 分组（`toall=group-vrrp-{id}`）、FDB 规则广播等天然支持跨 Zone 节点 |
| **现有验证** | 当前 C++ cland 单实例已在运行，Go 版本只会更高效 |

### 6.3 如果真的需要更多容量

1. **首选：增加 Region** — 每个 Region 是独立的 clapi + cland-go + DB，已有完整支持
2. **备选：未来引入 relay 中间层** — 如果单 Region 超过数千节点（极端场景），可以在 cland-go 和 cloudlet-go 之间加一层轻量 gRPC relay 做消息中转。这与 Zone 概念正交，不影响 clapi 的 Zone 逻辑

## 7. 分阶段执行计划

### Phase 1: MVP — Go 替换 C++

**目标：** 功能完全对等替换，零改动 clapi RPC handler 和 shell 脚本。

**步骤：**

| # | 任务 | 产出 | 依赖 |
|---|------|------|------|
| 1.1 | 定义 proto | `api/proto/cloudland/v1/cloudland.proto` | 无 |
| 1.2 | 生成 Go 代码 | `api/src/proto/cloudlandpb/*.go` | 1.1 |
| 1.3 | 实现 cland-go 核心 | `api/src/cland/` — server, registry, dispatcher | 1.2 |
| 1.4 | 实现 cloudlet-go | `api/src/cloudlet-go/` — executor, reporter, stream | 1.2 |
| 1.5 | 移植调度器 | `api/src/cland/scheduler.go` | 1.3 |
| 1.6 | 修改 clients.go | HTTP → gRPC | 1.2 |
| 1.7 | Dockerfile + systemd | `Dockerfile.cland-go`, `cloudlet-go.service` | 1.3, 1.4 |
| 1.8 | 集成测试 | 部署单节点，创建 VM，验证全链路 | 1.1-1.7 |

**验收标准：**
- [ ] 计算节点注册成功，健康上报正常
- [ ] 创建 VM 成功，收到 launch_vm 回调
- [ ] hyper_status, inst_status, report_rc 正常
- [ ] 文件传输正常
- [ ] toall=, group=, select=, inter= 路由正确
- [ ] 跨 Zone 操作正常（VRRP 分组、FDB 广播）
- [ ] cland-go 重启后 cloudlet 自动重连
- [ ] cloudlet-go 重启后重新注册
- [ ] 非法节点（未在 hypers 表中）连接被拒绝，返回 RegisterAck{success: false}
- [ ] cland-go 重启后 bulk 预加载 validNodes 正常（clapi 延迟启动时重试成功）
- [ ] NodeAdd 后新节点无需重启 cland-go 即可连接（缓存动态更新）
- [ ] gRPC 共享令牌认证：无令牌或错误令牌的连接被拒绝

**预估工期：** 2-3 周

### Phase 2: 清理与增强

| # | 任务 |
|---|------|
| 2.1 | 删除 C++ 源码（sci/, src/*.cpp） |
| 2.2 | 删除旧 Dockerfile 和 entrypoint |
| 2.3 | 简化 deploy-compute-node.sh（无需编译 SCI） |
| 2.4 | 添加 mTLS（cloudlet → cland） |
| 2.5 | 添加 Prometheus metrics |
| 2.6 | 考虑合并 cland-go 到 clapi 进程（可选，减少一跳） |

**预估工期：** 1 周

## 8. 目录结构总览

### 新增文件

```
api/proto/cloudland/v1/
  cloudland.proto                    -- 服务和消息定义

api/src/cland/
  main.go                           -- 入口
  server.go                         -- ClandService 实现
  cloudlet_server.go                -- CloudletService 实现
  node_registry.go                  -- 节点注册表
  group_manager.go                  -- 分组管理
  scheduler.go                      -- 资源调度器
  dispatcher.go                     -- control 解析 + 路由
  callback.go                       -- 回调转发
  config.go                         -- 配置

api/src/cloudlet-go/
  main.go                           -- 入口 + 重连
  executor.go                       -- 命令执行
  reporter.go                       -- 健康上报
  parser.go                         -- |:-COMMAND-:| 解析
  stream.go                         -- stream 管理

api/src/proto/cloudlandpb/
  cloudland.pb.go                   -- 生成的 protobuf 代码
  cloudland_grpc.pb.go              -- 生成的 gRPC 代码

deploy/docker/dockerfiles/
  Dockerfile.cland-go               -- 新 cland 镜像
```

### 修改文件

```
api/src/common/clients.go           -- HTTP → gRPC
api/src/apis/hyper.go               -- 新增 /internal/nodes/valid（bulk）+ /internal/node/verify（单个）端点
api/go.mod                          -- 添加 gRPC 依赖
deploy/docker/docker-compose.yml    -- 替换 cloudland 服务定义
deploy/docker/scripts/deploy-compute-node.sh  -- 安装 Go cloudlet
```

### 删除文件（Phase 2）

```
sci/                                -- 整个目录 (~20K 行)
src/cloudland.cpp, cloudlet.cpp, rpcworker.cpp, netlayer.cpp
src/handler.cpp, sendmsg.cpp, grpcmsg.cpp, grpcfile.cpp
src/filter/scheduler.cpp, rcmanager.cpp
src/remotexec/remotexec.proto
src/Makefile
deploy/docker/dockerfiles/Dockerfile.cloudland
deploy/docker/scripts/cloudland-entrypoint.sh
```

## 9. 有意不移植的 SCI 遗留逻辑

| 逻辑 | C++ 源码位置 | 不移植原因 |
|------|-------------|-----------|
| SCI 树形自动分层（launch_tree1-4） | `sci/libsci/launcher.cpp:550-708` | CloudLand 使用外部启动器模式（`SCI_USE_EXTLAUNCHER=yes`），实际部署是扁平拓扑，从未启用树形分层。gRPC 单进程可支撑数千连接，不需要中间 agent 层 |
| `inter=myID` 触发 `report_topology()` | `scheduler.cpp:277-282` | SCI 树形拓扑的遗留（多级 agent 场景）。新架构是扁平的 gRPC 直连，节点拓扑由 `toall=agent` + `callback=agent` 直接收集 |
| SCI 树形 filter 层级（`SCI_Filter_bcast`/`SCI_Filter_upload`） | `scheduler.cpp:82-130` | SCI 的中间 agent 多级过滤机制。新架构 cland-go 直接管理所有 cloudlet，调度逻辑内置在 cland-go 中，不需要 filter 管道 |
| SSH 认证（libpsec sign/verify） | `sci/scid/extlaunch.cpp:109-158` | 当前部署中 `sshAuth=false`（默认禁用），所有 sign/verify 返回 0。gRPC 可用 mTLS 替代（Phase 2） |
| SCI 恢复模式（enable_recover） | `sci/libsci/` 多处 | gRPC keepalive + 客户端重连循环完全覆盖此场景 |

## 10. 风险分析

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| gRPC stream 半开连接 | cloudlet 与 cland 失联但双方不知 | gRPC keepalive（30s 间隔，10s 超时），40s 内检测并重连 |
| 调度器移植正确性 | 资源选择结果与 C++ 不一致 | 单元测试：用相同输入对比 Go 和 C++ 结果 |
| report_rc.sh 输出解析 | 健康数据丢失或格式错误 | 保留 raw_report 字段直传，同时结构化解析，有一个就够 |
| 文件传输偏移写入 | 大文件传输数据损坏 | 端到端测试：传输镜像文件，校验 checksum |
| 并发命令执行 | cloudlet 内存/goroutine 爆炸 | executor 加 semaphore 限制并发数（默认 32） |
| 回调 `|:-COMMAND-:|` 解析差异 | C++ 用 `|:-COMMAND:-|` (注意冒号位置不同) 做 skip | 对齐 C++ handler.cpp:86 的实际偏移量，单元测试覆盖边界情况 |
| clapi 不可用 | 新节点无法注册（fallback 查询失败），回调丢失 | refreshValidNodes 带重试；回调转发加本地重试队列（指数退避，最多 3 次） |

## 11. 关键源码参考

| 现有文件 | 对应新组件 | 参考内容 |
|---------|-----------|---------|
| `src/rpcworker.cpp:123-255` | `dispatcher.go` | Execute 调度逻辑（所有 control 指令） |
| `src/rpcworker.cpp:442-527` | `server.go` | NodeAdd/NodeRemove 端点 |
| `src/rpcworker.cpp:309-367` | `cloudlet_server.go` validNodes 缓存 + clapi `/internal/nodes/valid` | 节点持久化 → 启动全量拉取 + 内存缓存 + fallback 实时查询 |
| `src/cloudlet.cpp:30-163` | `executor.go` | backHandler 命令执行和回调 |
| `src/cloudlet.cpp:260-312` | `reporter.go` | 健康上报循环 |
| `src/filter/rcmanager.cpp` | `scheduler.go` | 资源调度算法 |
| `src/netlayer.cpp:141-255` | `group_manager.go` | 分组 CRUD 和消息发送 |
| `src/handler.cpp:44-112` | `callback.go` | 回调分发（frontHandler） |
| `api/src/common/clients.go` | `clients.go`（修改） | HyperExecute/NodeAdd/NodeRemove |
| `api/src/common/instance.go` | 不变 | GetHyperGroup() Zone 候选节点过滤 |
| `api/src/rpcs/frontback.go` | 不变 | 回调接收格式参考 |

## 12. 实现修订（2026-09-14）

实现评审后以下几处与前文设计不同，以本节为准：

| 主题 | 前文设计 | 当前实现 |
|------|---------|---------|
| 节点校验端点 | clapi gin 路由 | 挂在 clapi 内部端口（`internal.listen`，默认 5005）：`GET /internal/nodes/valid` 返回 `[{hostid, hostname, status}]`，`GET /internal/node/verify?id=` |
| 校验缓存 | 启动加载一次，只增不减 | 启动时重试直至加载成功，之后每 5 分钟全量替换；clapi 已删除的节点从拓扑中移除 |
| 鉴权 | `GRPC_AUTH_TOKEN` 可选 | cland-go 未配置令牌拒绝启动（本地开发可设 `GRPC_AUTH_DISABLED=true`，此时 NodeAdd 不下发私钥）；clapi、cloudlet 通过 per-RPC credentials 携带；部署脚本自动生成并下发 |
| ReportHealth | 仅凭 node_id | 必须携带 cland 在 CommandStream 响应头 `x-cloudland-session` 中下发的会话 ID |
| NodeAdd | 更新缓存并返回密钥 | 实时向 clapi 校验 hostid；由部署脚本通过 `cloudlet-go node-add` 调用 |
| 命令执行 | 每条命令一个 goroutine（上限 32） | 默认按到达顺序串行（与 C++ SCI 单线程 handler 一致），`CLOUDLET_CONCURRENCY` 可调 |
| 断线 | 等待活跃命令结束后重连 | 立即重连；运行中命令的输出最多等待 3 分钟在新 stream 上发送 |
| 重复注册 | — | 新 stream 取消旧 stream；旧 stream 退出时不会注销新 stream；RegisterAck 保证先于任何命令下发 |
| control 解析 | 按值非空匹配 | 按 key 是否出现匹配，顺序与 `rpcworker.cpp` 一致；空组名或空成员表示全部节点；`group=` 在组内调度一个节点；`toall=agent`、`inter=-1` 触发拓扑上报；不支持 `type=file` |
| 调度 | 候选节点直接调度 | 仅在线节点参与；调度或发送失败时异步回调 `error=resource` |
| 健康上报转发 | 首行以 `report` 转发，回调行原样转发 | 首行只更新调度器；回调行去掉 `\|:-COMMAND-:\|` 标记后以 msg_id 0 转发；StatusReporter 每 5 秒上报汇总资源（hostid=-1）与拓扑（`callback=agent`，断连节点及启动 90 秒内未连入的活跃节点 status=10） |
| 回调转发 | 同步 HTTP POST | 100 worker 异步队列；连接失败或 5xx 最多尝试 3 次 |
| clapi 调用 | 使用请求 ctx | `context.WithoutCancel` + 30 秒超时，HTTP 客户端断开不影响命令下发 |
| cland 停止 | 通知 cloudlet 退出 | 不通知，cloudlet 自动重连；仅 `node_removed` 使 cloudlet 退出 |
| 脚本环境 | `RequestID` | 注入 `SCI_CLIENT_ID` 与 `TRACEPARENT` |
