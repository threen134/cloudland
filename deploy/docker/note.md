# CloudLand 通信架构笔记

## 核心通信矩阵

| 通信双方 | 端口 | 协议 | 范围 | 备注 |
| :--- | :--- | :--- | :--- | :--- |
| **计算节点 cloudlet-go → 主控 cland-go** | **5006** | gRPC 双向流 | **跨物理机（管理网）** | 计算节点主动连接 `CLAND_ENDPOINT`（`MANAGEMENT_VIP:5006`），接收命令、回传结果与心跳；`GRPC_AUTH_TOKEN` 鉴权，断线自动重连 |
| **clapi → 主控 cland-go** | **5006** | gRPC | **本地/管理网** | **下行通道**：clapi 经 `ClandService.Execute` 下发命令 |
| **主控 cland-go → clapi** | **5005** | HTTP | **本地/管理网** | **上行通道**：`/internal/execute` 转发计算节点的回调与执行结果 |
| **cpgateway → clapi** | **8255** | HTTPS | **本地/管理网** | 按 Region 代理 REST API |
| **clapi / cpgateway → PostgreSQL** | **5432** | SQL | **本地/管理网** | 资源与配置数据持久化 |
| **计算节点 → Loki / OTel Collector** | **3100 / 4317** | HTTP / gRPC | **管理网** | 日志推送、链路追踪导出 |

## 网络安全隔离原则
1. **公网层**：仅暴露 80、443（Web 访问与 API 入口）。
2. **管理层**：5005、5006、5432、8255 等端口只应在管理网可达，不对公网开放。
3. **5006 同时面向计算节点**：部署脚本不对其做来源限制，依赖 `GRPC_AUTH_TOKEN` 鉴权与内网隔离（安全组 / 外部防火墙禁止公网访问）。
