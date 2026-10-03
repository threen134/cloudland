# 分布式链路追踪方案（OpenTelemetry + Grafana Tempo）

> **实施状态（2026-09-14）**：阶段 1、2 已实施（含 cpgateway-go 网关埋点）；阶段 3 中数据库、S3、前端埋点已实施，Tempo metrics-generator 与中央 Tempo 未实施。实施与原方案的差异见 §11。

## 1. 背景

### 1.1 现状

一次资源操作（如创建 VM）会跨越多个进程和主机：

```
Web UI → CPGateway (:8000) → clapi (:8255) → cland (:5006, gRPC) → cloudlet (计算节点) → shell 脚本
                                  ↑                                      │
                                  └──── clapi 回调 (:5005) ← cland ←─────┘  (脚本输出 / callback 行)
```

目前排查问题只能靠各组件日志手工拼接，已有的追踪能力零散且不完整：

| 能力 | 现状 | 问题 |
|------|------|------|
| `X-Request-ID` | clapi Gin 中间件生成，经 gRPC `trace_id` 字段传到 cland → cloudlet → 环境变量 `RequestID`，回调时以 `X-Request-ID` 头带回 clapi | 只是日志关联 ID，看不到调用关系和各段耗时；前端、网关不生成；脚本日志不打印 |
| 业务日志 | `logger.Ctx(ctx)` 输出 `request_id` 字段（rpcs 与 services 关键路径） | 同上 |
| Jaeger | `api/src/logs/` 有上游 2019 年的 OpenTracing + `jaeger-client-go` 集成 | 仅 3 处调用（`dbs/db.go` 迁移、`model/hyper.go` `Updates`）；默认地址 `127.0.0.1:6381`（UDP，疑似 6831 笔误），部署中未配置也未运行 Jaeger，span 全部丢弃；依赖均已停止维护 |
| 观测平台 | 每个 Region 部署 Grafana 10.2 + Loki 2.9 + Promtail + Prometheus | 无链路追踪后端 |

### 1.2 目标

1. 任意一次 API 请求，可以在 Grafana 中看到完整调用链瀑布图：网关 → clapi → cland → cloudlet → 脚本 → 回调 → 后续命令。
2. 全链路只有一个标识 `trace_id`：日志、响应头、脚本环境变量统一使用。
3. 日志与链路双向跳转：Loki 日志行点击 `trace_id` 打开链路；链路 span 一键查询对应日志。
4. 追踪后端不可用时不影响业务（异步导出，失败丢弃）。

### 1.3 非目标

- ~~前端浏览器埋点（后续按需）~~ 已在阶段 3 实施，见 §11。
- Python CPGateway 埋点（即将被 CPGateway-Go 替代，见 `cpgateway-go-migration-plan.md`）。
- C++ `cloudland` / `cloudlet` 旧实现（部署已切换到 Go 版 `cland-go` / `cloudlet-go`）。
- 跨 Region 的统一链路存储（见 §5.6）。

---

## 2. 技术选型

### 2.1 埋点层：OpenTelemetry

OpenTracing 已并入 OpenTelemetry 并归档，`jaeger-client-go` 已被 Jaeger 官方废弃并建议迁移到 OTel SDK。无论后端选什么，埋点都应使用 OTel：

- W3C Trace Context（`traceparent` 头）是标准传播格式，nginx 等中间层默认透传。
- Go 生态有官方插桩：`otelgin`（Gin）、`otelgrpc`（gRPC）、`otelhttp`（net/http）。
- 后端可替换：OTLP 协议被 Tempo、Jaeger v2 等原生支持，更换后端不改业务代码。
- `api/go.sum` 已间接引入 `go.opentelemetry.io/otel v1.44.0`（grpc 依赖），SDK 版本与之对齐。

### 2.2 追踪后端：Grafana Tempo

控制面请求量不大，Tempo 与 Jaeger 都能胜任，选择 Tempo 的理由是**存储与运维模型**：

| | Tempo | Jaeger |
|---|---|---|
| 持久化存储 | 本地盘或对象存储（S3/MinIO），无需索引集群 | 生产需 Elasticsearch/OpenSearch 或 Cassandra；Badger 仅单机 |
| 运维一致性 | 与现有 Loki 同体系，部署/配置方式相似 | 新增一套独立存储与 UI |
| HA 扩展 | 对接外部对象存储即可跨节点共享 | 需要外部 ES/Cassandra 集群 |
| 查看入口 | 现有 Grafana | 独立 UI（也可接 Grafana） |
| 链路分析 UI | Grafana trace 视图 + TraceQL；服务拓扑需开启 metrics-generator | 专用 UI，trace 对比、依赖图开箱即用 |

日志 ↔ 链路跳转两者在 Grafana 中都能配置，不是 Tempo 独有优势。若后续更看重专用链路 UI，可将 OTLP 导出地址切到 Jaeger v2，业务代码不变。

---

## 3. 总体设计

### 3.1 链路全景

以创建 VM 为例，一条 trace 内的 span 结构：

```
[cpgateway-go] POST /api/v1/instances                     (otelgin server)
 └─[cpgateway-go] proxy → clapi                            (otelhttp client)
    └─[clapi] POST /api/v1/instances                       (otelgin server)
       └─[clapi] ClandService/Execute                      (otelgrpc client)
          └─[cland] ClandService/Execute                   (otelgrpc server)
             └─[cland] dispatch inter=3                    (手动 span)
                └─[cloudlet] execute launch_vm.sh          (手动 span，跨 stream 消息传播)
                   ├─[clapi] callback launch_vm            (手动 span，脚本输出 |:-COMMAND-:| 行)
                   │  └─[clapi] ClandService/Execute       (回调中继续下发命令，仍在同一 trace)
                   │     └─ ...
                   └─ (脚本日志 script.log 打印 trace_id)
```

API 请求的 span 在返回响应时结束，脚本执行和回调稍后到达，作为已结束 span 的子 span 挂在同一 trace 下（OTel 允许），因此异步操作的完整生命周期可在一条 trace 中查看。

### 3.2 统一标识：trace_id 取代 request_id

| 位置 | 现在 | 改为 |
|------|------|------|
| HTTP 请求传播 | `X-Request-ID` | `traceparent`（W3C） |
| HTTP 响应头 | `X-Request-ID` | `X-Trace-ID`（便于用户/前端复制排查） |
| Go ctx | `ctx.Value("X-Request-ID")` | `trace.SpanContextFromContext(ctx)` |
| gRPC unary（clapi → cland） | `ExecuteRequest.trace_id` | gRPC metadata（otelgrpc 自动注入/提取），删除字段 |
| gRPC stream 消息（cland ↔ cloudlet） | `trace_id` 字符串 | `map<string,string> trace_context`（W3C carrier） |
| 脚本环境变量 | `RequestID` | `TRACEPARENT` |
| 日志字段 | `request_id` / `[req=<id>]` | `trace_id` + `span_id` / `[trace=<id>]` |

产品未发布，不保留 `X-Request-ID` 兼容。已完成的 `logger.Ctx(ctx)` 调用点（约 1530 处）保持不变，只替换其内部 ID 来源。

**未配置导出地址时仍然生成 trace_id**：SDK 始终安装 `TracerProvider`，只是不挂 exporter。这样没有部署 Tempo 的环境，日志依然有可关联的 `trace_id`。

### 3.3 各段传播方式

| 链路段 | 协议 | 传播方式 |
|--------|------|---------|
| cpgateway-go 入口 | HTTP | `otelgin` 提取 `traceparent`（无则新建 root span） |
| cpgateway-go → clapi | HTTP (ReverseProxy) | `otelhttp.NewTransport` 注入 |
| clapi 入口 | HTTP (Gin) | `otelgin` 提取 |
| clapi → cland | gRPC unary | `otelgrpc.NewClientHandler` / `NewServerHandler`（stats handler） |
| cland → cloudlet | gRPC 双向流 `CommandStream` | **手动**：`CommandRequest.trace_context` 注入/提取。流是长连接，stats handler 只能生成一个覆盖整条流的 span，无法区分单条命令 |
| cloudlet → 脚本 | 进程环境变量 | `TRACEPARENT`（`sudo -E` 保留环境，与现有 `RequestID` 相同路径） |
| cloudlet → cland | stream 消息 | `CommandResult` / `CallbackLine` / `ErrorReport` 的 `trace_context` 回传 cloudlet execute span 的上下文 |
| cland → clapi 回调 | HTTP POST `/internal/execute` | 手动注入 `traceparent` 头（不创建 client span，见 §3.5） |
| clapi 回调入口 | HTTP (Macaron) | 中间件提取；`frontback.Execute` 解码出命令后按需创建 span |

### 3.4 Span 设计

| 服务 (`service.name`) | Span 名称 | 来源 | 关键属性 |
|------|------|------|---------|
| `cpgateway` | `HTTP {method} {route}` | otelgin | `cloudland.org_id`、`cloudland.user_id`、`cloudland.region` |
| `cpgateway` | `HTTP {method}`（client） | otelhttp | `server.address`（region 后端） |
| `clapi` | `HTTP {method} {route}` | otelgin | `cloudland.org_id`、`cloudland.user_id`（取自 `X-*` 头） |
| `clapi` | `cloudland.v1.ClandService/Execute` | otelgrpc client | — |
| `clapi` | `callback {cmd}` | 手动 | `cloudland.command`、`cloudland.host_id`、`cloudland.msg_id` |
| `cland` | `cloudland.v1.ClandService/Execute` | otelgrpc server | — |
| `cland` | `dispatch {control 指令}` | 手动 | `cloudland.control`、`cloudland.target_nodes` |
| `cloudlet` | `execute {脚本名}` | 手动 | `cloudland.node_id`、`cloudland.msg_id`、`process.exit_code`；非 0 退出码设为 Error 状态 |

Resource 属性：`service.name`、`service.version`（取 `api/.version`）、`host.name`、`cloudland.region`，cloudlet 额外带 `cloudland.node_id`。

### 3.5 降噪

控制面有大量周期性、无业务含义的调用，不处理会淹没有效链路：

| 噪音源 | 频率 | 处理 |
|--------|------|------|
| cloudlet `ReportHealth`（执行 `report_rc.sh`） | 每节点 1–20 秒一次 | otelgrpc 过滤掉该方法；cland 转发的 `control=report` 回调不带 trace 上下文，clapi 对无上游 trace 上下文的回调一律不建 span |
| 脚本普通输出行（`CommandResult`） | 每行一次 HTTP 回调 | cland 回调只注入 `traceparent` 不建 client span；clapi 仅在解码出已注册命令时建 span |
| `CommandStream` 流本身 | 长连接 | otelgrpc 过滤，改由手动 span 覆盖单条命令 |
| Prometheus SD `/api/v1/prometheus/sd/*`、`/api/v1/version`、健康检查 | 周期轮询 | otelgin 路径过滤 |

采样：默认 `ParentBased(AlwaysOn)`，控制面请求量低，全量保留。后续如需降采样，在 SDK 初始化中配置，下游跟随父 span 决策，保证链路完整。

### 3.6 敏感信息

下发命令中可能包含密码等敏感参数（如 VNC 密码、云盘凭据）。**Span 属性只记录脚本名（命令首个 token），禁止记录完整命令、请求体或 Authorization 等认证头**。

---

## 4. 组件改造

### 4.1 公共 tracing 包：`api/src/utils/tracing/`

clapi、cland、cloudlet 同属 `api` module，共用一个初始化包；cpgateway-go 为独立 module，复制同等实现。

```go
// Init 初始化全局 TracerProvider 与 W3C 传播器。
// 未配置 OTEL_EXPORTER_OTLP_ENDPOINT 时不挂 exporter，但仍生成 trace_id 供日志关联。
func Init(ctx context.Context, serviceName string) (shutdown func(context.Context) error, err error) {
	res, err := resource.New(ctx,
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES
		resource.WithHost(),
		resource.WithAttributes(semconv.ServiceName(serviceName), semconv.ServiceVersion(version)),
	)
	if err != nil {
		return nil, err
	}
	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlptracegrpc.New(ctx) // 读取标准 OTEL_EXPORTER_OTLP_* 环境变量
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp)) // 异步批量，队列满丢弃，不阻塞业务
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

// Inject / Extract 用于 stream 消息中的 map<string,string> trace_context
func Inject(ctx context.Context) map[string]string
func Extract(ctx context.Context, carrier map[string]string) context.Context

// TraceIDFromContext 供日志使用，无有效 span 时返回空字符串
func TraceIDFromContext(ctx context.Context) string
```

新增依赖：`go.opentelemetry.io/otel`、`otel/sdk`、`otel/exporters/otlp/otlptrace/otlptracegrpc`，以及 contrib 的 `otelgin`、`otelgrpc`（otel v1.46.0、otelgrpc v0.71.0、otelgin v0.63.0，见 §11；`otelhttp` 留待 cpgateway-go 接入时引入）。

### 4.2 clapi

| 位置 | 改动 |
|------|------|
| `cmds/api/main.go` | `RunDaemon` 启动前调用 `tracing.Init(ctx, "clapi")`，退出时 `shutdown` 刷新缓冲 |
| `src/apis/routes.go` | `log.RequestID()` → `otelgin.Middleware("clapi", 路径过滤)` + 写 `X-Trace-ID` 响应头的中间件；部分 service 直接传入 `*gin.Context` 作为 ctx，由日志封装回退到 `Request.Context()` 取 span，不开启 `ContextWithFallback`（见 §11） |
| `src/common/clients.go` | `grpc.NewClient` 增加 `grpc.WithStatsHandler(otelgrpc.NewClientHandler())`；`HyperExecute` 删除 request id 生成与 `TraceId` 赋值 |
| `src/rpcs/rpcs.go` | `log.MacaronRequestID()` → `MacaronTracing()`：从请求头提取 `traceparent` 写入 `c.Req.Context()` |
| `src/rpcs/frontback.go` | `dispatchExecute` 中仅当 ctx 携带上游 trace 上下文且命令已注册时创建 `callback {cmd}` span，命令返回后结束 |
| `src/utils/log/` | `ContextLogger` 的 ID 来源改为 `tracing.TraceIDFromContext`，前缀 `[trace=<id>]`；JSONFormatter 输出 `trace_id`、`span_id`；Gin 访问日志 `request_id` → `trace_id`；删除 `RequestID()`、`MacaronRequestID()`、`RequestIDKey` |
| `src/services/image.go:137` | 异步上传 goroutine 使用 `context.WithoutCancel(ctx)` 派生超时 ctx，保留 span 上下文 |

### 4.3 cland

| 位置 | 改动 |
|------|------|
| `cmds/cland/main.go` | `tracing.Init(ctx, "cland")`；`grpc.NewServer` 增加 `grpc.StatsHandler(otelgrpc.NewServerHandler(过滤 CommandStream / ReportHealth))`，与现有 token 拦截器共存 |
| `src/cland/server.go` | `Execute` 将 gRPC ctx 传入 `Dispatcher.Dispatch` |
| `src/cland/dispatcher.go` | `Dispatch(ctx, req)` 创建 `dispatch` span，`CommandRequest.TraceContext = tracing.Inject(ctx)`；`error=resource` 回调同样携带上下文 |
| `src/cland/cloudlet_server.go` | 收到 `CommandResult` / `CallbackLine` / `ErrorReport` 时 `tracing.Extract` 出上下文传给回调转发 |
| `src/cland/callback.go` | `Forward(ctx, ...)`：`otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))`，删除 `X-Request-ID` |

### 4.4 cloudlet 与脚本

| 位置 | 改动 |
|------|------|
| `cmds/cloudlet/main.go` | `tracing.Init(ctx, "cloudlet")`，退出前 `shutdown`；`grpc.NewClient` 不加 stats handler（仅有流和被过滤的 ReportHealth） |
| `src/cloudlet/executor.go` | `ExecuteCommand`：`Extract(req.TraceContext)` → 创建 `execute {脚本名}` span；`cmd.Env` 中 `RequestID=` → `TRACEPARENT=`；回传消息 `TraceContext = Inject(spanCtx)`；按退出码设置 span 状态 |
| `scripts/cloudrc` | `log_debug` 输出增加 `trace=${TRACEPARENT:3:32}`（截取 traceparent 中的 32 位 trace id）；删除注释掉的 `JAEGER_SERVICE_NAME` |

### 4.5 proto 变更（`api/proto/cloudland/v1/cloudland.proto`）

cland 与计算节点上的 cloudlet 可能不同时升级，**不复用字段号**，旧字段 `reserved`：

```proto
message ExecuteRequest {
  // ...
  reserved 5;  // 原 trace_id，改由 gRPC metadata 传播
}

message CommandRequest {
  // ...
  reserved 5;                               // 原 trace_id
  map<string, string> trace_context = 6;    // W3C traceparent / tracestate
}

// CommandResult / CallbackLine / ErrorReport 同样处理：reserved 旧字段号，新增 trace_context
```

重新生成 `api/src/proto/cloudlandpb/` 与 `api/cloudland/v1/` 下的 `*.pb.go`。

### 4.6 cpgateway-go

与 Compose 切换到 CPGateway-Go 同步进行（迁移方案 §7.1）：

| 位置 | 改动 |
|------|------|
| `main.go` | 初始化 tracing（复制 §4.1 实现），`service.name=cpgateway` |
| `src/apis/routes.go` | `requestIDMiddleware` → `otelgin.Middleware` + `X-Trace-ID` 响应头 |
| `src/apis/proxy.go` | `httputil.ReverseProxy` 的 `Transport` 外包 `otelhttp.NewTransport` |
| `src/services/*`、`src/apis/*` 中的 `http.Client` | 区域同步、配额查询等后台请求同样包 `otelhttp.NewTransport` |

### 4.7 清理旧实现

- 删除 `api/src/logs/`（OpenTracing + Jaeger）、`api/src/dbs/logging.go`、`api/src/model/tracing.go`，以及 `dbs/db.go:127,159`、`model/hyper.go:165` 的 `startLogging` 调用。
- `api/go.mod` 移除 `github.com/uber/jaeger-client-go`、`github.com/uber/jaeger-lib`、`github.com/opentracing/opentracing-go`；`github.com/sirupsen/logrus` 如无其他引用一并移除。
- 删除所有 `X-Request-ID` / `RequestID` / `request_id` 相关代码与配置。

---

## 5. 部署变更

### 5.1 Tempo 服务（`deploy/docker/docker-compose.yml`，`region` profile）

```yaml
  # ---------- Tempo ----------
  tempo:
    image: grafana/tempo:<锁定 2.x 具体版本>
    container_name: cloudland-tempo
    restart: unless-stopped
    profiles:
      - region
    command: -config.file=/etc/tempo/tempo.yaml
    volumes:
      - ./config/tempo/tempo.yaml:/etc/tempo/tempo.yaml:ro
      - tempo_data:/var/tempo
    ports:
      - "${INTERNAL_IP:-127.0.0.1}:4317:4317"   # OTLP gRPC，供计算节点 cloudlet 导出；仅绑定内网
    networks:
      cloudland:
        ipv4_address: 172.28.0.83              # 实施时确认未占用
```

`deploy/docker/config/tempo/tempo.yaml`（单体模式）：

```yaml
server:
  http_listen_port: 3200

distributor:
  receivers:
    otlp:
      protocols:
        grpc:
          endpoint: 0.0.0.0:4317   # 显式绑定，新版本默认只监听 localhost
        http:
          endpoint: 0.0.0.0:4318

compactor:
  compaction:
    block_retention: 168h          # 保留 7 天

storage:
  trace:
    backend: local                 # HA 场景可改为 s3，见 §5.6
    wal:
      path: /var/tempo/wal
    local:
      path: /var/tempo/blocks
```

### 5.2 Grafana 数据源

`config/grafana/provisioning/datasources/loki.yaml` 增加 `uid` 与日志 → 链路跳转：

```yaml
  - name: Loki
    type: loki
    uid: loki
    # ...现有配置
    jsonData:
      derivedFields:
        - name: TraceID
          matcherRegex: '"trace_id":"(\w+)"'
          datasourceUid: tempo
          url: '$${__value.raw}'        # provisioning 中 $ 需转义为 $$
```

新增 `tempo.yaml`，配置链路 → 日志跳转：

```yaml
apiVersion: 1
datasources:
  - name: Tempo
    type: tempo
    uid: tempo
    access: proxy
    url: http://tempo:3200
    jsonData:
      tracesToLogsV2:
        datasourceUid: loki
        spanStartTimeShift: "-5m"
        spanEndTimeShift: "5m"
        customQuery: true
        query: '{container=~"cloudland-.+"} |= "$${__trace.traceId}"'
```

Grafana 当前为 10.2，实施时需在该版本上验证 TraceQL 查询与双向跳转。

### 5.3 Promtail

`config/promtail/promtail-config.yaml` 的 json stage：`request_id` → `trace_id`，新增 `span_id`。两者均为高基数字段，不提升为 label。

### 5.4 服务环境变量

| 服务 | 环境变量 |
|------|---------|
| clapi | `OTEL_SERVICE_NAME=clapi`、`OTEL_EXPORTER_OTLP_ENDPOINT=http://tempo:4317`、`OTEL_RESOURCE_ATTRIBUTES=cloudland.region=<region>` |
| cland（`cloudland` 服务） | `OTEL_SERVICE_NAME=cland`、`OTEL_EXPORTER_OTLP_ENDPOINT=http://tempo:4317` |
| cpgateway-go | `OTEL_EXPORTER_OTLP_ENDPOINT=${CPGATEWAY_OTLP_ENDPOINT-http://tempo:4317}`：默认导出到本机 Tempo；仅部署中央控制面时在 `.env` 设 `CPGATEWAY_OTLP_ENDPOINT=` 关闭（见 §5.6） |

导出地址在 compose 中按服务固定（cland 为 host 网络，与 bridge 网络服务的地址不同）。服务未设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 时只生成 trace_id、不导出。

### 5.5 计算节点（`deploy/docker/scripts/deploy-compute-node.sh`）

`/etc/sysconfig/cloudlet` 追加：

```bash
OTEL_SERVICE_NAME=cloudlet
OTEL_EXPORTER_OTLP_ENDPOINT=http://${CONTROLLER_IP}:4317
OTEL_RESOURCE_ATTRIBUTES=cloudland.node_id=$NODE_ID,cloudland.zone=$ZONE_NAME
```

地址与 `CLAND_ENDPOINT` 一致（HA 下为 VIP）。控制节点防火墙需放通计算节点网段访问 4317/tcp。

计算节点上的 `script.log` 目前未被 Promtail 采集，脚本日志中的 `trace_id` 需登录节点查看；采集计算节点日志不在本方案范围内。

### 5.6 多 Region 与 HA

**多 Region**：沿用现有观测组件"每 Region 一套"的模型，Tempo 部署在各 Region。

- 单节点（`full,dev,region`）：网关与区域组件共用本机 Tempo，链路完整。
- 中央网关独立部署（仅 `full,dev`，本机没有 Tempo/Loki）：网关不导出 span，但仍生成并传播 `traceparent`。Region Tempo 中的链路从 clapi 开始，根 span 显示为缺失（Tempo 可正常展示），不影响区域内排查。
- 如需跨 Region 查看网关段，后续可在中央部署 Tempo，以同一 `trace_id` 分别查询（见 §9）。

**HA（Active/Standby）**：Tempo 与 Loki 一样使用本地卷，故障切换后原 MASTER 上的历史链路在新节点不可见，新请求不受影响。如需切换后保留历史，将 `storage.trace.backend` 改为 `s3` 并指向两节点共享的外部对象存储。

---

## 6. 分阶段实施

| 阶段 | 内容 | 粗估 |
|------|------|------|
| **阶段 1：区域核心链路** | §4.1 tracing 包；§4.2 clapi；§4.3 cland；§4.4 cloudlet（不含脚本）；§4.5 proto；§4.7 清理；§5.1–5.5 部署 | 5–7 人天 |
| **阶段 2：网关与脚本** | §4.6 cpgateway-go（随其 Compose 切换）；`cloudrc` 脚本日志打印 trace_id；异步 goroutine 上下文修复 | 2–3 人天 |
| **阶段 3：按需增强** | GORM 插桩（`gorm.io/plugin/opentelemetry`，注意 span 量）；Tempo metrics-generator 生成服务拓扑与 RED 指标（需 Prometheus 开启 `--web.enable-remote-write-receiver`）；前端埋点；中央 Tempo | 按需 |

阶段 1 完成后即可在单节点环境看到 clapi → cland → cloudlet → 回调的完整链路。

---

## 7. 验证方案

**单元测试**
- `tracing.Inject` / `Extract` 往返后 trace_id、span_id 一致。
- `ContextLogger` 在有 span 的 ctx 下 JSON 输出 `trace_id`、`span_id`，调用位置指向业务代码（沿用 `utils/log/context_test.go`）。
- `MacaronTracing` 从 `traceparent` 头提取后，handler 中 `c.Req.Context()` 可取到同一 trace。

**单节点端到端**（`COMPOSE_PROFILES=full,dev,region`）
1. 创建 VM，记录响应头 `X-Trace-ID`。
2. Grafana → Tempo 按 trace_id 查询，确认 span 链：`clapi HTTP` → `Execute`（client/server）→ `dispatch` → `cloudlet execute launch_vm.sh` → `clapi callback launch_vm`。
3. 从 Loki 日志行点击 TraceID 跳转到链路；从链路 span 跳转到日志。
4. 计算节点 `script.log` 中对应行带同一 trace_id。
5. 空跑 10 分钟无操作，确认 Tempo 中没有 `ReportHealth` / `report_rc` 产生的 root trace。
6. 停止 Tempo 容器后创建 VM，业务正常、各服务无阻塞，日志仍有 trace_id。

---

## 8. 风险与缓解

| 风险 | 缓解 |
|------|------|
| 命令参数中的密码泄露到链路存储 | Span 属性只记录脚本名；代码评审检查 `SetAttributes` 调用 |
| 周期性上报产生海量无效 trace | §3.5 过滤规则；验证方案第 5 步 |
| 追踪后端故障影响业务 | BatchSpanProcessor 异步导出，队列满丢弃；验证方案第 6 步 |
| cland 与 cloudlet 升级不同步导致 proto 不兼容 | 新增字段号、旧字段 `reserved`，缺失上下文时降级为新 root span |
| OTLP 端口暴露 | 仅绑定 `INTERNAL_IP`，防火墙限定计算节点网段 |
| 异步操作 trace 时间跨度长（分钟级），按耗时筛选失真 | 以 span 维度分析耗时；属已知特性 |
| Grafana 10.2 版本较旧，部分 Tempo 功能不可用 | 实施时验证基础查询与跳转；Grafana 升级另行评估 |

---

## 9. 待决策事项

| # | 问题 | 建议 |
|---|------|------|
| 1 | 多 Region 部署时，中央网关的 span 存放位置 | 阶段 1–2 不单独处理（§5.6），有跨 Region 排查需求时再部署中央 Tempo |
| 2 | Python CPGateway 是否埋点 | 不做，随 CPGateway-Go 切换一并获得网关段链路；若切换时间较远可再评估（FastAPI + httpx 插桩约 0.5 人天） |
| 3 | Tempo 链路保留期 | 7 天 |
| 4 | HA 下是否对接外部对象存储保留历史链路 | 默认本地卷，与 Loki 保持一致 |

---

## 10. 文件清单

**新增**
- `api/src/utils/tracing/tracing.go`、`middleware.go`（及测试）
- `api/src/cland/dispatcher_trace_test.go`、`api/src/rpcs/frontback_trace_test.go`
- `deploy/docker/config/tempo/tempo.yaml`
- `deploy/docker/config/grafana/provisioning/datasources/tempo.yaml`

**修改**
- `api/cmds/api/main.go`、`api/cmds/cland/main.go`、`api/cmds/cloudlet/main.go`
- `api/src/apis/routes.go`、`api/src/common/clients.go`
- `api/src/rpcs/rpcs.go`、`api/src/rpcs/frontback.go`
- `api/src/cland/server.go`、`dispatcher.go`、`cloudlet_server.go`、`callback.go`
- `api/src/cloudlet/executor.go`
- `api/src/utils/log/logger.go`、`context.go`、`context_test.go`
- `api/src/services/image.go`、`api/src/dbs/db.go`、`api/src/model/hyper.go`
- `api/cmds/alarm_rules_manager/alarm_rules_manager.go`
- `api/proto/cloudland/v1/cloudland.proto` 及生成的 `*.pb.go`
- `api/go.mod`、`api/go.sum`
- `scripts/cloudrc`
- `cpgateway-go/main.go`、`cpgateway-go/src/apis/routes.go`、`proxy.go` 等（阶段 2）
- `deploy/docker/docker-compose.yml`、`deploy/docker/README.md`
- `deploy/docker/config/grafana/provisioning/datasources/loki.yaml`
- `deploy/docker/config/promtail/promtail-config.yaml`
- `deploy/docker/scripts/deploy-compute-node.sh`

**删除**
- `api/src/logs/`
- `api/src/dbs/logging.go`、`api/src/model/tracing.go`

---

## 11. 实施记录与差异

| 项 | 原方案 | 实际实施 | 原因 |
|----|--------|---------|------|
| OTel 依赖版本 | 与 otel v1.44.0 配套 | otel v1.46.0、otelgrpc v0.71.0、otelgin v0.63.0 | otelgin v0.64+ 要求 gin ≥ 1.11，会连带把 gin 升到 1.12（引入 quic-go 等依赖）；固定 otelgin v0.63.0，gin 仅升补丁版本 1.10.1 |
| genproto | — | 旧的整体版 `google.golang.org/genproto` 升级到最新 | 与 OTLP exporter 引入的 `genproto/googleapis/api` 拆分模块产生 ambiguous import，`go mod tidy` 失败 |
| `*gin.Context` 取 span | clapi 开启 `ContextWithFallback` | 日志封装对 `*gin.Context` 回退到 `Request.Context()` | `ContextWithFallback` 会让 `Done/Err` 跟随请求取消，改变直接传入 `*gin.Context` 的业务调用行为 |
| 回调 span 条件 | 有父上下文或非降噪命令 | 仅"有上游 trace 上下文且命令已注册"时创建 | 周期上报本就不带上下文；普通输出行解码不出已注册命令，规则更简单 |
| alarm-rules-manager | 未列出 | 同步接入 `GinMiddleware` 与 `Init` | 原先使用已删除的 `RequestID` 中间件 |
| `Version` 变量 | — | cland、cloudlet、alarm-rules-manager 的 main 包新增 `Version` | Makefile 以 `-X main.Version` 注入版本，原先变量不存在，注入被静默忽略 |
| Tempo 数据卷 | — | 无需 init 容器 | 镜像以 10001 用户运行、`/var/tempo` 属主为 10001，命名卷首次挂载继承属主；镜像为 distroless，没有 `chown` |
| `.env.example` | 增加 `OTEL_EXPORTER_OTLP_ENDPOINT` | 增加 `CPGATEWAY_OTLP_ENDPOINT`、`CPGATEWAY_OTLP_HTTP_ENDPOINT`（网关单独部署在中央时覆盖，设为空关闭）与“链路追踪”一节（头部/尾部采样、Tempo 保留期与 S3、Collector 导出地址、Grafana 查询地址） | 各服务导出地址固定为本 Region 的 Collector；cland 为 host 网络，单一变量无法同时覆盖 |
| `api/cloudland/v1/*.pb.go` | 重新生成 | 删除 | 无任何代码引用的过期副本（与改动前的 proto 已不一致）；生成代码只保留 `api/src/proto/cloudlandpb/` |
| cpgateway-go（§4.6） | ReverseProxy 外包 `otelhttp` | 代理已被重写为 `forwardToRegion` 自行发请求：请求使用 `c.Request.Context()`，`InsecureClient` 包 `HTTPTransport`；配额查询链（`PrepareQuota` → `QueryResourceAmount`/`QueryFlavorAmount` → `getBackendJSON`）、`clapiRequest`、`firingCount` 补 ctx；logrus 通过 `LogrusHook` 为 `log.WithContext(ctx)` 输出 `trace_id`/`span_id`；访问日志追加 `trace_id`；转发时跳过后端同名 `X-Trace-ID` 响应头 | 在并行会话结束后实施；后台任务与外部通知的后续接入见下方“后台任务”“出站 HTTP”行；103 处 logrus 调用中 83 处所在函数无 ctx |
| 日志 trace_id 覆盖 | rpcs 与下发命令的 services | api 中可取得 `ctx` 或 `*gin.Context` 的 logger 调用全部转换；cland、cloudlet 的标准库日志用 `tracing.Logf` 加 `[trace=<id>]` 前缀 | 约 200 处所在函数无 ctx；`apis`、`model` 中的标准库 `log.Printf` 改用 `tracing.Logf` 加 `[trace=<id>]` 前缀（所在函数无 ctx 的保持原样） |
| 出站 HTTP | 未规划 | 新增 `tracing.HTTPTransport`（仅在请求带上游 span 时建 client span 并注入 `traceparent`），用于内部服务：`adjust.go`、`monitor.go`、`alarms.go` 的 Prometheus 查询，clapi → alarm-rules-manager（`ProcessTemplate`/`WriteFile` 等文件操作调用链全部补 ctx），网关 → clapi 的转发、配额查询与区域推送。外部服务只建 client span、不注入 trace 头：ClickHouse（`clickhouse.query`，凭据从 URL 移到 `X-ClickHouse-User`/`X-ClickHouse-Key` 请求头）、WDS（`wds.auth`、`wds.volume_details`）、交换机 API（`switch_api.request`）、告警/通知 webhook（`notify.*`）、飞书（`notify.feishu`）、SMTP（`notify.email`）、镜像下载（`image.download`） | 不把内部 trace id 暴露给第三方；URL 不再含密码后，span 可安全记录请求地址 |
| Loki 日志跳转正则 | `"trace_id":"(\w+)"` | `trace(?:_id)?"?[:=]"?(\w+)` | cpgateway-go 的 logrus 日志与访问日志为文本格式（`trace_id=...`），cland、cloudlet 与 clapi 标准库日志使用 `[trace=...]` 前缀，需同时匹配 |
| 数据库追踪 | `gorm.io/plugin/opentelemetry` | 自研 `tracing.GormPlugin`（GORM 回调）：仅在 ctx 带上游 span 时建 `gorm.<op>` span，只记录带占位符的 SQL；api 在 `GetContextDB`/`StartTransaction` 绑定 `context.WithoutCancel(ctx)`，网关新增 `dbs.DBContext` 并在 handler 中替换 51 处 `dbs.DB()` | 官方插件没有"仅在有父 span 时创建"的选项，周期上报的查询会产生大量孤立 trace；`WithoutCancel` 避免客户端断开时中断数据库操作；api 22 处、网关 29 处直接取 DB 的调用无 ctx |
| S3 追踪 | 未规划 | `S3PutObject`/`S3RemoveObject`/`S3DetectImage` 手动建 `s3.<op>` span（`aws.s3.bucket`、`aws.s3.key`） | 不在 HTTP 层注入 trace 头：S3 可能是第三方对象存储 |
| 前端埋点 | 非目标 | `@opentelemetry/sdk-trace-web` 2.2.0 + XHR 插件：axios 请求注入 `traceparent`；span 经网关登录态接口 `POST /api/v1/telemetry/traces`（512KB 上限，本身不产生 trace）转发到 OTel Collector 的 OTLP/HTTP；API 报错时控制台输出 trace id，错误提示显示 Trace ID；路由切换建 `route <路径模板>` span，VNC 控制台建 `vnc connect` span；`VITE_TRACING_ENABLED`、`VITE_TRACING_SAMPLE_RATIO` 控制开关与采样率 | 不对公网暴露 Tempo OTLP 端口；上报用 fetch，避免 401 触发全局退出登录；版本与已有间接依赖对齐；未引入 document-load 插件（会拉入更高版本 SDK，重复打包） |
| OTel Collector | 各服务直连 Tempo | 新增 Collector（contrib 0.160.0）：服务、计算节点、浏览器转发的 span 先到 Collector；`span_metrics`/`service_graph` 连接器在尾部采样之前计算指标（Prometheus 抓取 `:8889`）；尾部采样保留出错链路与慢链路（`TAIL_SAMPLING_LATENCY_MS`），其余按 `TAIL_SAMPLING_PERCENT` 保留；Tempo 不再发布 4317，只在内网发布查询端口 3200，Loki 3100 也改为只绑定内网 | 指标不受采样影响；采样策略集中配置；多 Region 统一存储时只需修改 `TRACING_BACKEND_ENDPOINT` |
| 头部采样 | 全量 | `ParentBased` + 自定义 root 采样器：请求链路按 `TRACING_SAMPLE_RATIO`（默认 1）；`tracing.StartBackground` 创建、带 `cloudland.background=true` 属性的后台 root span 按 `TRACING_BACKGROUND_SAMPLE_RATIO`（默认 0.01） | 心跳、消费对账等周期任务量大，全量采集会淹没请求链路 |
| 后台任务 | 无上游请求不接入 | 网关区域心跳每轮、登录触发的消费对账各起一个后台 trace；区域上线、系统设置与通知渠道变更等由请求触发的异步推送沿用请求 ctx（`context.WithoutCancel`）；gin handler 中启动的 goroutine 一律在 `go` 语句处求值 ctx，不在 goroutine 内读取 `*gin.Context` | 区域推送失败是常见故障点，需要能查到；gin 会复用 `*gin.Context`，goroutine 内读取存在数据竞争 |
| nginx | 非目标 | 镜像改为 `nginx:1.30.4-alpine-otel`（`ngx_otel_module`）：`/api/v1/` 开启 `otel_trace` 并以 `propagate` 延续浏览器的 `traceparent`，`/websockify` 记录握手 span；访问日志追加 `trace_id=` | 已验证 Collector 地址无法解析时 nginx 仍能启动并正常转发；consoleproxy 是构建时拉取的第三方 libvirt-console-proxy，无法埋点，VNC 链路止于 nginx，前端另建 `vnc connect` span |
| 看板与告警 | 仅 Grafana 数据源 | Grafana 升级到 12.4.3；provisioning 新增 Prometheus 数据源（uid `prometheus`，供 Tempo 服务拓扑使用）与「CloudLand 链路追踪」看板（RED、服务拓扑、Top 接口、cloudlet 脚本耗时）；`config/prometheus/tracing_rules/tracing.yml` 定义 4 条告警（错误率、P95 延迟、服务间调用失败率、Collector 不可达），带 `source=tracing` 标签，由 Alertmanager 路由到 `platform` 接收器 | clapi 的 `/alerts/process` 会把收到的所有告警写入租户告警事件表，平台告警不能进入 |
| Tempo 存储 | 本地卷 | `TEMPO_STORAGE_BACKEND=s3` + `TEMPO_S3_*` 切换到对象存储，`TEMPO_RETENTION` 设置保留时间（配置启用 `-config.expand-env=true`）；开启 `stream_over_http_enabled`，Grafana 12 的 TraceQL 流式搜索复用 3200 端口 | HA 故障切换后仍可查询历史链路 |
| 计算节点日志 | Promtail 采集脚本日志 | promtail-agent 增加 cloudlet journald 采集（`cloudlet-go.service`），positions 持久化到 `/var/lib/promtail`，日志带 `host`、`node_id` 标签；cloudlet 的资源属性带 `cloudland.node_id`、`cloudland.zone` | 原配置 positions 在容器内，容器重建后会重复推送 |
| 脚本 | `log_debug` 带 trace | `die` 同时经 `log_debug` 记录失败原因；`create_image.sh` 写入 `image_upload.log` 的行带 `trace=` | `die` 的标准输出由 cloudlet 回传，格式保持不变 |
