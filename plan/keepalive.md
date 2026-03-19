# Region 心跳机制实施方案

在 [cpgateway](file:///Users/spark/workspace/cloud/cloudland/deploy/docker/dockerfiles/Dockerfile.cpgateway) 中添加后台心跳机制，自动监控各 Region 的可用性，并更新数据库中的 `is_available` 状态。

## 已实现的变更

### [组件] CPGateway 后端

#### [修改] [region.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/models/region.py)
- 添加 `last_check_at` (DateTime)：记录最后一次心跳检查的时间。
- 添加 `status_message` (String)：记录检查失败时的错误信息。
- 添加 `fail_count` (Integer, 默认 0)：记录连续失败次数。
- 添加 `maintenance_mode` (Boolean, 默认 False)：允许手动暂停心跳检测和 API 转发。

#### [修改] [region_schema.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/schemas/region.py)
- 更新 `RegionPublic` 以包含 `maintenance_mode`, `last_check_at`, `status_message`。
- 更新 `RegionAdmin` 以包含 `fail_count`。
- 更新 `RegionUpdate` 以允许设置 `maintenance_mode`。

#### [修改] [config.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/core/config.py)
- 添加 `REGION_HEARTBEAT_INTERVAL` (默认: 60s)。
- 添加 `REGION_HEARTBEAT_TIMEOUT` (默认: 5s)。
- 添加 `REGION_HEARTBEAT_OFFLINE_THRESHOLD` (默认: 3)。

#### [新建] [heartbeat_service.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/services/heartbeat_service.py)
- 实现 `HeartbeatService`，包含一个异步方法来 ping 所有 Region。
- 使用 `httpx` 向 `{internal_endpoint}/api/v1/version` 发送 GET 请求。
- **逻辑**:
  - **维护模式**: 完全跳过该 Region（提前返回），不修改任何字段。
  - **成功**: 将 `fail_count` 重置为 0，更新 `last_check_at`，并将 `is_available` 设置为 `True`（自动恢复）。
  - **失败**: 增加 `fail_count`。如果 `fail_count >= OFFLINE_THRESHOLD`，则将 `is_available` 设置为 `False`。更新 `status_message` 为错误详情。
- `check_all_regions()` 在数据库查询层面过滤掉 `maintenance_mode = True` 的 Region（双重保护）。

#### [修改] [main.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/main.py)
- 添加后台任务循环，按配置的时间间隔运行 `HeartbeatService.check_all_regions()`。
- 在 `@app.on_event("startup")` 处理程序中启动此循环。
- 将任务引用存储在 `app.state.heartbeat_task` 中，防止因垃圾回收而被静默终止。

#### [修改] [regions.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/api/endpoints/regions.py)
- 在 `PATCH /{region_uuid}` 中：当设置 `maintenance_mode=True` 时，自动设置 `is_available=False`，以便立即阻止 API 转发和 Token 签发，而无需修改代理或认证逻辑。

#### [修改] [sync_db_schema.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/scripts/sync_db_schema.py)
- 将 `maintenance_mode` 添加到现有数据库的迁移列列表中。

### [组件] 测试

#### [新建] [test_heartbeat_logic.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/tests/test_heartbeat_logic.py)
- 使用 `_make_http_mock()` 辅助函数正确模拟 `httpx.AsyncClient` 作为异步上下文管理器。
- 打补丁路径：`app.services.heartbeat_service.httpx.AsyncClient`。
- 测试用例：
  1. 在线 → 保持在线 (HTTP 200)
  2. 在线 → 失败 1 次 (HTTP 500, 仍然可用，未达到阈值)
  3. 失败 3 次 → 离线 (HTTP 503, 达到阈值)
  4. 离线 → 在线 (HTTP 200, 自动恢复)
  5. 维护模式 → 心跳成功不会将 `is_available` 翻转回 True

## 设计决策

### maintenance_mode vs is_available
- `is_available`: 由心跳机制自动管理。反映实际的连通性。
- `maintenance_mode`: 由管理员通过 API 手动管理。表示主观意图的暂停。
- **交互**: 设置 `maintenance_mode=True` 会自动设置 `is_available=False`。现有的代理和认证对 `is_available` 的检查将起到强制执行作用，无需修改这些层级的代码。
- **恢复**: 当设置 `maintenance_mode=False` 时，`is_available` 保持 `False` 直到下一次成功的心跳探测将其恢复。

### 冲突解决（手动禁用 vs 心跳）
- 原始问题：通过 API 手动设置 `is_available=False` 会在下一次成功的心跳探测时被覆盖。
- 修复：使用 `maintenance_mode` 进行主观禁用。心跳会跳过处于维护模式的 Region，不会恢复其 `is_available` 状态。

## 验证计划

### 自动化测试
- `tests/test_heartbeat_logic.py` 涵盖了包括维护模式在内的完整状态机。

### 手动验证
1. 本地启动 [cpgateway](file:///Users/spark/workspace/cloud/cloudland/deploy/docker/dockerfiles/Dockerfile.cpgateway)。
2. 添加一个指向本地测试服务器（或无效 URL）的模拟 Region。
3. 观察日志以确认心跳任务正在运行。
4. 验证 regions 表中的 `is_available`, `last_check_at`, 和 `maintenance_mode` 字段。
5. 停止/启动测试服务器，验证状态是否随之切换。
6. 通过 `PATCH /api/v1/regions/{uuid}` 设置 `maintenance_mode=True`，验证请求返回 503 且心跳不会恢复可用性。
