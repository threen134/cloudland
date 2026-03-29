---
order: 70
---
# 运维管理与故障排查

一套稳定的私有云不仅需要成功的部署，更需要精细的日常维护与快速的故障定位能力。本指南总结了 CloudLand Docker 环境下的常用运维操作。

---

## 常用运维指令

所有管理操作请在控制节点的 `/opt/cloudland/deploy/docker` 目录下执行。

### 1. 生命周期管理
```bash
# 启动（并尝试按需构建镜像）
docker compose up -d

# 停止并移除容器（不丢失持久化卷数据）
docker compose down

# 彻底重启特定服务（例如 clapi）
docker compose restart clapi

# 停止所有服务
docker compose stop
```

### 2. 日志查看
日志是排查问题的首要来源。
```bash
# 查看所有组件聚合日志
docker compose logs -f

# 查看后端 API 详细日志
docker compose logs -f clapi

# 查看网关及认证日志
docker compose logs -f cpgateway

# 查看主控 SCI 通信日志
docker compose logs -f cloudland
```

### 3. 数据层操作
```bash
# 进入计算节点数据库（仅限开发模式部署的容器化 DB）
docker compose exec postgres psql -U postgres cloudland

# 查看当前已注册的计算节点列表
cat volumes/host.list
```

---

## 故障排查指南

### 1. 容器由于证书报错无法启动
**症状**: 日志中出现 `open /certs/...: no such file or directory` 或 `is a directory`。
**解决**:
1. 确认 `volumes/certs` 目录下是否已生成 `.crt` 和 `.key` 文件。
2. 如果存在空目录导致挂载失败，请执行 `rm -rf volumes/certs/*`。
3. 重新执行证书初始化：`bash scripts/init-certs.sh`。
4. 执行 `docker compose up -d --force-recreate`。

### 2. 登录 Web UI 报错 404 或 502
**症状**: 浏览器访问 `PUBLIC_IP` 返回 Bad Gateway 或页面无法加载。
**解决**:
- **检查 Nginx 状态**: `docker compose logs nginx`。如果是 `host not found`，说明后端组件（如 cpgateway）启动较慢，等待 1 分钟后重启 Nginx 即可。
- **检查 CPGateway**: `docker compose logs cpgateway`。确保网关已连接数据库并成功初始化。

### 3. 登录后无法看到资源（API 鉴权失败）
**症状**: 登录成功，但获取虚拟机或主机列表失败，控制台显示 Unauthorized。
**解决**:
1. 检查 `.env` 文件中的 `CPGATEWAY_SECRET_KEY`。
2. 确保此 Secret 与创建 Region 时填写的 `internal_secret` 完全一致。
3. 确保控制面 API 服务 (clapi) 已正确加载了该密钥。

### 4. 无法与计算节点通信（主机显示 Offline）
**症状**: Web 界面主机状态长时间为 `Offline`。
**解决**:
- **Host 模式确认**: 确保 `cloudland-cland` 容器运行在 `network_mode: host`。
- **端口检查**: 在控制节点执行 `ss -tlnp | grep 9988` 确认 SCI 监听端口已开启。
- **IP 连通性**: 在计算节点尝试 `ping <CONTROL_IP>`，并确认无防火墙拦截。

---

### 5. CPGateway 问题（完整模式）

**症状**: API 请求返回 401/403/404 错误

```bash
# 1. 检查 cpgateway 服务状态
docker compose logs cpgateway
docker compose exec cpgateway curl http://localhost:8000/

# 2. 验证 RSA 密钥已生成
docker compose exec cpgateway ls -la /app/keys/
# 应看到 private.pem 和 public.pem

# 3. 检查数据库连接
docker compose logs cpgateway | grep "Database"
# 应看到 "✓ Database is ready!"

# 4. 验证 Region 配置是否正确
curl -k -X GET https://<PUBLIC_IP>/api/v1/regions \
  -H "Authorization: Bearer <token>"

# 5. 检查 cpgateway.secret 一致性（三者必须完全一致）
grep CPGATEWAY_SECRET_KEY .env
docker compose exec clapi cat /opt/cloudland/web/conf/config.toml | grep secret
# 以及 Region 记录中的 internal_secret
```

### 6. 数据库连接失败

```bash
# 单节点 (dev 模式) - 检查容器化 DB
docker compose exec postgres pg_isready -U postgres

# HA 模式 - 检查外部 DB 可达性
(echo > /dev/tcp/<DB_HOST>/5432) 2>/dev/null && echo OK || echo FAIL

# 检查密码一致性
grep POSTGRES_PASSWORD .env
```

> [!NOTE]
> clapi 启动时探测数据库 30 次（间隔 2s），60 秒不可达则退出。cpgateway 同样会等待数据库就绪。

### 7. HA 故障切换不生效

```bash
# 1. 检查 Keepalived 状态
systemctl status keepalived

# 2. 检查 VIP 是否漂移
ip addr show <VRRP_INTERFACE> | grep <MANAGEMENT_VIP>

# 3. 检查回调脚本权限
chmod +x scripts/ha-notify.sh

# 4. 手动测试容器启动
docker compose start

# 5. 查看 Keepalived 日志
journalctl -u keepalived -f

# 6. 验证 SSH 免密
ssh -o BatchMode=yes root@<对端IP> true
```

---

## 性能与维护建议

- **磁盘清理**: 随着部署周期的增加，`/opt/cloudland/web/log` 和 `/opt/cloudland/cache` 可能会占用大量空间。建议配置 `logrotate` 或定期手动清理缓存镜像。
- **备份**:
  - **核心数据库**: 建议定期执行 `pg_dump` 备份 `cloudland` 和 `cloudland_cpgateway` 数据库。
  - **SSH 密钥**: 备份 `/opt/cloudland/deploy/.ssh` 目录，这是控制计算节点的唯一凭证。
  - **SSL 证书**: 备份 `volumes/certs/` 目录，更换节点时可复用。
- **升级**:
  1. 执行 `git pull` 拉取代码。
  2. 执行 `docker compose build --no-cache` 重新构建镜像。
  3. 执行 `docker compose up -d` 滚动更新。

---

## 部署日志

所有部署脚本运行时会生成带时间戳的日志文件：

```bash
ls /var/log/cloudland-*-deploy-*.log
# 控制节点: /var/log/cloudland-control-deploy-YYYYMMDD-HHMMSS.log
# HA 节点:  /var/log/cloudland-ha-deploy-YYYYMMDD-HHMMSS.log
# 计算节点: /var/log/cloudland-compute-deploy-YYYYMMDD-HHMMSS.log
```
