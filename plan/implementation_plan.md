# 标准化 JSON 日志 (控制面与后端)

将所有 Go 和 C++ 服务（`clapi`、`clbase`、`alarm-rules-manager`、`cloudland` 和 `cloudlet`）的日志统一为纯 JSON 格式，便于通过 Loki/Grafana 进行结构化查询和可观测性分析。

## 部署架构说明

| 服务 | 运行环境 | 日志目标 |
|------|----------|----------|
| `clapi` / `clbase` / `alarm-rules-manager` | Docker 容器 | stdout → Promtail → Loki |
| `cloudland` (cland) | Docker 容器 | stdout → Promtail → Loki |
| `cloudlet` | **裸金属节点** | 文件（`/opt/cloudland/log/`），JSON 格式 |

> cloudlet 运行在 Hyper 裸金属节点，**不在 Docker 环境中**，无法使用 Docker 环境变量。
> 日志写入本地文件，由裸金属节点上独立的 Promtail agent 采集（未来）或人工查阅。

---

## 方案设计

### 阶段 1：Go 服务日志标准化 [已完成]

- **[utils/log/logger.go](file:///Users/spark/workspace/cloud/cloudland/web/src/utils/log/logger.go)**: 实现通用 [RequestID](file:///Users/spark/workspace/cloud/cloudland/web/src/utils/log/logger.go#275-289) 和 [Logger](file:///Users/spark/workspace/cloud/cloudland/web/src/utils/log/logger.go#290-339) 中间件，支持 Gin 和 Macaron。
- **apis**: 删除本地中间件，切换到 `utils/log`；`logging.log_dir == ""` 时输出纯 JSON 到 stdout。
- **routes**: `clbase` 服务切换到 `rlog.MacaronLogger`。
- **alarm-rules-manager**: 修复中间件缺失导致的编译问题。

### 阶段 2：C++ 日志标准化 (cloudland & cloudlet) [进行中]

核心修改集中在 `Loger` 单例类，两个服务共享同一套逻辑：

#### 2a. `Loger` 类扩展（[log.hpp](file:///Users/spark/workspace/cloud/cloudland/src/common/log.hpp) + [log.cpp](file:///Users/spark/workspace/cloud/cloudland/src/common/log.cpp)）

**[log.hpp](file:///Users/spark/workspace/cloud/cloudland/src/common/log.hpp)** 新增私有字段：
```cpp
bool useStdout; // Docker 模式：写 stdout
bool useJson;   // JSON 格式：useStdout=true 时自动开启，或由 SCI_LOG_FORMAT=json 启用
```

**[log.cpp](file:///Users/spark/workspace/cloud/cloudland/src/common/log.cpp) — `init()` 重构**：
- 移除 `assert(directory)` 硬断言，改为条件判断
- 当 `directory` 为 NULL、空字符串或 `"stdout"` 时，设置 `useStdout = true` + `useJson = true`，跳过文件初始化
- 检查环境变量 `SCI_LOG_FORMAT`：若为 `"json"`，设置 `useJson = true`（文件模式下也输出 JSON，供裸金属节点使用）

**[log.cpp](file:///Users/spark/workspace/cloud/cloudland/src/common/log.cpp) — `print()` 重构**：
- **JSON 分支**（`useJson == true`）：
  - 用 `vsnprintf` 先将 message 渲染到 `char msgBuf[]`
  - 对 `"` `\` `\n` 等特殊字符做 JSON 转义
  - 用 `clock_gettime(CLOCK_REALTIME)` 获取毫秒精度时间戳，格式 `2026-03-07T10:00:00.000Z`
  - 从 `getenv("RequestID")` 读取 request_id（由 cloudlet 的命令执行流程通过 `setenv` 注入）
  - 用 `snprintf` 一次性构建完整 JSON 行
  - `useStdout` 时用 `write(STDOUT_FILENO, ...)` 单次系统调用写出（原子，线程安全，行长 < 4096 时不会交错）
  - 文件模式时用 `fopen/fputs/fclose`（与现有行为一致）
- **纯文本分支**（`useJson == false`，现有行为）：保持不变，文件模式专用
- 新增 `levelNames[]` 数组（`"CRITICAL"` 等）供 JSON 输出使用，避免 `[CRIT]` 带括号

JSON 输出字段：
```json
{"time":"2026-03-07T10:00:00.000Z","level":"INFO","file":"netlayer.cpp","line":42,"thread":12345,"request_id":"abc-123","message":"..."}
```

#### 2b. cloudland (Docker 容器)

**[cloudland.cpp](file:///Users/spark/workspace/cloud/cloudland/src/cloudland.cpp) — `initParams()` 修改**：
- 在解析 `-l logDir` 命令行参数之后，额外检查 `SCI_LOG_DIRECTORY` 环境变量
- 若该变量存在且为空字符串，调用 `Loger::init("", ...)` 触发 stdout+JSON 模式
- 否则使用命令行参数指定的 logDir（向后兼容裸机部署）

**[docker-compose.yml](file:///Users/spark/workspace/cloud/cloudland/deploy/docker/docker-compose.yml)** — cloudland 服务新增环境变量：
```yaml
SCI_LOG_DIRECTORY: ""   # 空字符串 = stdout JSON 模式
```

#### 2c. cloudlet (裸金属节点)

**[cloudlet.cpp](file:///Users/spark/workspace/cloud/cloudland/src/cloudlet.cpp) — `main()` 无需修改**：
- 现有代码已读取 `SCI_LOG_DIRECTORY` 并传给 `Loger::init()`
- 重构后的 `init()` 会自动检查 `SCI_LOG_FORMAT` 环境变量
- 在裸金属节点的启动脚本（`/etc/environment` 或 systemd unit）中设置 `SCI_LOG_FORMAT=json`，即可获得 JSON 文件日志

---

## 变更文件清单

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| [src/common/log.hpp](file:///Users/spark/workspace/cloud/cloudland/src/common/log.hpp) | 修改 | 新增 `useStdout`、`useJson` 字段 |
| [src/common/log.cpp](file:///Users/spark/workspace/cloud/cloudland/src/common/log.cpp) | 修改 | 重构 `init()` 和 `print()`，新增 JSON 渲染和 stdout 输出路径 |
| [src/cloudland.cpp](file:///Users/spark/workspace/cloud/cloudland/src/cloudland.cpp) | 修改 | `initParams()` 检测 `SCI_LOG_DIRECTORY` 环境变量 |
| [src/cloudlet.cpp](file:///Users/spark/workspace/cloud/cloudland/src/cloudlet.cpp) | **不修改** | 现有 env var 读取逻辑已满足需求 |
| [deploy/docker/docker-compose.yml](file:///Users/spark/workspace/cloud/cloudland/deploy/docker/docker-compose.yml) | 修改 | cloudland 服务添加 `SCI_LOG_DIRECTORY: ""` |

---

## 验证计划

1. **编译验证**：在本地运行 `make` 确保 C++ 代码编译成功，无新增 warning。

2. **Docker 验证（cloudland）**：
   - 在测试服务器 (`165.192.110.235`) 上构建并部署。
   - `docker compose logs -f cloudland-cland` 查看是否输出单行 JSON。
   - 确认 `level`、`file`、`line`、`message`、`request_id` 字段均存在。

3. **裸金属验证（cloudlet）**：
   - 在 Hyper 节点设置 `export SCI_LOG_FORMAT=json`，重启 cloudlet。
   - 检查 `/opt/cloudland/log/cloudlet.*.log` 文件，确认为 JSON 格式。

4. **Loki 验证**：
   - 在 Grafana Explore 中执行 `{container="cloudland-cland"} | json` 确认字段被解析。
   - 确认 `level`、`module`、`method` 等 label 可在 Grafana 侧边栏正常过滤。
