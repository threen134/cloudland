# CloudLand 容器部署控制面高可用 (HA) 实现计划

## Context

当前 Docker 部署方案为单控制节点，无高可用能力。需要支持两台控制节点 Active/Standby 模式，通过 VRRP (keepalived) 管理 VIP 漂移，实现控制面自动故障切换。

**设计原则：**
- Keepalived 装在宿主机 OS 上（不容器化）
- PostgreSQL 连接外部 DB，不在 docker-compose 中运行
- 通过 keepalived 的 `notify` 脚本调用 `docker compose start/stop` 控制整组容器的启停
- cloudland-entrypoint.sh 不做 VIP 检测，容器的生命周期完全由 Docker + keepalived 管理
- 对现有单节点部署零影响（单节点用 `deploy-control-node.sh`，HA 用 `deploy-ha-node.sh`，互不干扰）


## 故障切换流程

```
MASTER 宕机 → keepalived 检测失败(~3s) → VIP 漂到 BACKUP
→ keepalived notify_master 脚本执行 → docker compose start 启动所有容器
→ 计算节点 SCI 自动重连到 MANAGEMENT_VIP:9988

原 MASTER 恢复（nopreempt 模式下 VIP 不回切）：
→ 原 MASTER 的 keepalived 变为 BACKUP
→ notify_backup 脚本执行 → docker compose stop 停止容器
```

## 实现步骤

### 步骤 1: 修改 `deploy/docker/docker-compose.yml`

改动点：

1. **postgres 服务加 `profiles: ["dev"]`**：
   - dev 模式（`COMPOSE_PROFILES=dev`）：本地启动 postgres 容器，DB_HOST=postgres
   - 生产/HA 模式（不设置 profile）：不启动 postgres，连接外部 DB
   - **现有单节点部署脚本 `deploy-control-node.sh` 也需要适配**：在 .env 中默认设置 `COMPOSE_PROFILES=dev` 和 `DB_HOST=postgres`
2. **clbase / clapi 环境变量改为可配置**：
   - `DB_HOST: postgres` → `DB_HOST: ${DB_HOST:-postgres}`
   - `DB_PORT: "5432"` → `DB_PORT: ${DB_PORT:-5432}`
3. **移除 `depends_on: postgres`**：postgres 加了 profile 后，profile 未激活时 `depends_on` 会报错，必须移除。DB 就绪等待改由 `web-entrypoint.sh` 中的 wait-for-db 循环保证（见步骤 1.1）
4. **cloudland 服务**：添加 `SCI_ENABLE_FAILOVER: ${SCI_ENABLE_FAILOVER:-no}` 环境变量

> **关于 depends_on 移除后的启动顺序：** 经查 `web/src/dbs/db.go` 的 `openDB()` 函数，DB 连接失败会直接 `panic()` 且无重试。因此移除 `depends_on` 后需要在 entrypoint 加 wait-for-db，否则 dev 模式下 postgres 还没就绪时 clbase/clapi 会崩溃。

### 步骤 1.1: 修改 `deploy/docker/scripts/web-entrypoint.sh`

在启动应用前增加 wait-for-db 循环，确保数据库可连接：

```bash
# Wait for database to be ready (pg_isready 不可用，用 bash TCP 探测)
echo "Waiting for database at ${DB_HOST}:${DB_PORT} ..."
for i in $(seq 1 30); do
    if (echo > /dev/tcp/${DB_HOST}/${DB_PORT}) 2>/dev/null; then
        echo "Database is ready."
        break
    fi
    echo "Attempt $i/30: database not ready, retrying in 2s..."
    sleep 2
done
```

> **为什么不用 pg_isready：** clbase/clapi 镜像基于 `ubuntu:22.04`，只装了 `ca-certificates openssh-client rsync`，没有 `postgresql-client`。用 bash 内置的 `/dev/tcp` 做 TCP 端口探测，零依赖。
>
> 两种模式都安全：dev 模式等待本地 postgres 启动；HA 模式等待外部 DB 可连接。

> **SCI_ENABLE_FAILOVER 说明：** SCI 通信层参数，控制计算节点在 SCI 主控连接断开后是否自动重连。设为 `yes` 后，新 MASTER 以 rescue 模式启动接受重连，计算节点的 `PurifierProcessor::recover()` 自动重连 MANAGEMENT_VIP:9988。
>
> **HA_ENABLED 不再需要：** 之前是在 entrypoint 里做 VIP 检测循环的开关，现在容器启停由 keepalived notify 脚本控制。只保留 `SCI_ENABLE_FAILOVER`。

### 步骤 2: 修改 `deploy/docker/.env.example`

追加 HA 配置项（默认注释掉，不影响单节点用户）：

```bash
# ---- 部署模式 ----
# dev: 单节点开发模式，本地启动 postgres（默认）
# 留空或不设置: 生产/HA 模式，连接外部 DB
COMPOSE_PROFILES=dev

# ---- 数据库 ----
# dev 模式: DB_HOST=postgres（连接本地容器）
# 生产/HA 模式: DB_HOST=<外部 DB IP>
DB_HOST=postgres
DB_PORT=5432

# ---- HA 配置（可选，单节点无需修改）----
#HA_ROLE=MASTER
#PEER_IP=
#VRRP_INTERFACE=eth0
#VRRP_ROUTER_ID=51
#VRRP_AUTH_PASS=cloudland
#VRRP_VIP_MASK=24
#SCI_ENABLE_FAILOVER=yes
```

### 步骤 3: 新建 `deploy/docker/config/keepalived/keepalived.conf.template`

Keepalived 配置模板，部署脚本用 sed 替换占位符：

```
vrrp_instance CLOUDLAND_HA {
    state %%HA_STATE%%
    interface %%VRRP_INTERFACE%%
    virtual_router_id %%VRRP_ROUTER_ID%%
    priority %%VRRP_PRIORITY%%
    advert_int 1
    nopreempt

    authentication {
        auth_type PASS
        auth_pass %%VRRP_AUTH_PASS%%
    }

    virtual_ipaddress {
        %%MANAGEMENT_VIP%%/%%VRRP_VIP_MASK%%
    }

    notify_master "/opt/cloudland/deploy/docker/scripts/ha-notify.sh MASTER"
    notify_backup "/opt/cloudland/deploy/docker/scripts/ha-notify.sh BACKUP"
    notify_fault  "/opt/cloudland/deploy/docker/scripts/ha-notify.sh FAULT"
}
```

### 步骤 4: 新建 `deploy/docker/scripts/ha-notify.sh`

keepalived 状态变更时的回调脚本，核心逻辑：

```bash
#!/bin/bash
# keepalived notify 脚本 — 控制 docker compose 容器启停 + HA 同步 cron
COMPOSE_DIR=/opt/cloudland/deploy/docker
CRON_FILE=/etc/cron.d/cloudland-ha-sync
STATE=$1

case "$STATE" in
    MASTER)
        # VIP 漂到本机，启动所有服务
        cd $COMPOSE_DIR && docker compose start
        # 启用同步 cron（MASTER 向 BACKUP 推送）
        [ -f ${CRON_FILE}.disabled ] && mv ${CRON_FILE}.disabled $CRON_FILE
        ;;
    BACKUP|FAULT)
        # VIP 离开本机，停止所有服务
        cd $COMPOSE_DIR && docker compose stop
        # 禁用同步 cron（BACKUP 不推送）
        [ -f $CRON_FILE ] && mv $CRON_FILE ${CRON_FILE}.disabled
        ;;
esac
```

- 使用 `docker compose start/stop`（不是 up/down），保留容器和网络，启停速度快
- 两台节点部署时都先 `docker compose up -d` 创建容器，然后 BACKUP 节点 `docker compose stop`

### 步骤 5: 新建 `deploy/docker/scripts/deploy-ha-node.sh`

HA 部署脚本，支持 MASTER 和 BACKUP 两种角色。主要流程：

1. 校验必需变量（HA 特有 + 原有必需）：
   - HA 特有：`HA_ROLE`, `PEER_IP`, `VRRP_INTERFACE`, `DB_HOST`
   - 原有必需：`PUBLIC_IP`, `INTERNAL_IP`, `MANAGEMENT_VIP`, `NETWORK_DEVICE`, `ADMIN_PASSWORD`
2. 安装 keepalived（`apt install keepalived`）
3. 根据 `HA_ROLE` 渲染 keepalived.conf（MASTER priority=101, BACKUP priority=100）
4. 部署 `ha-notify.sh` 到指定位置
5. 启动 keepalived 服务
6. 写入 `.env` 的 HA 变量：
   - `DB_HOST`：外部数据库地址
   - `SCI_ENABLE_FAILOVER=yes`
7. MASTER：生成证书 + SSH 密钥
8. BACKUP：从 MASTER rsync 证书和 SSH 密钥
9. 两台都执行 `docker compose up -d --build` 创建容器
10. BACKUP 节点执行 `docker compose stop`（等待 keepalived 切换时启动）

### 步骤 6: 修改 `deploy/docker/scripts/deploy-control-node.sh`

适配 dev 模式，确保单节点部署默认使用本地 postgres：

1. `vars` 数组（第 83 行）追加 `"COMPOSE_PROFILES"` 和 `"DB_HOST"`：
```bash
vars=("PUBLIC_IP" "INTERNAL_IP" "MANAGEMENT_VIP" "NETWORK_DEVICE" "DB_LISTEN_IP" "POSTGRES_USER" "POSTGRES_PASSWORD" "POSTGRES_DB" "ADMIN_PASSWORD" "COMPOSE_PROFILES" "DB_HOST")
```

2. `.env` 初始化后（第 75 行附近），确保 dev 模式默认值已写入：
   - `.env.example` 中已有 `COMPOSE_PROFILES=dev` 和 `DB_HOST=postgres`，`cp .env.example .env` 时会自动带上
   - 无需额外代码，只要 `.env.example` 更新正确即可

> 这样现有的 `deploy-control-node.sh` 用户无感升级：从更新后的 `.env.example` 拷贝 `.env` 时自动获得 dev 模式默认值。

### 步骤 7: 修改 `deploy/docker/scripts/deploy-compute-node.sh`

在生成 `/etc/sysconfig/cloudlet` 时加入 `SCI_ENABLE_FAILOVER`，让计算节点支持主控切换后自动重连。

当前（第 296-310 行）缺少这个变量，需要：

1. 脚本顶部环境变量区域增加：
```bash
SCI_ENABLE_FAILOVER="${SCI_ENABLE_FAILOVER:-no}"  # HA 模式设为 yes
```

2. 生成 cloudlet 配置时加入：
```bash
cat > /etc/sysconfig/cloudlet <<EOF
...
SCI_ENABLE_LISTENER=yes
SCI_USE_EXTLAUNCHER=yes
SCI_ENABLE_FAILOVER=$SCI_ENABLE_FAILOVER
...
EOF
```

> **两边都要开启才能完成自动重连：**
> - 控制节点 `SCI_ENABLE_FAILOVER=yes` → 新 MASTER 以 rescue 模式启动，接受重连
> - 计算节点 `SCI_ENABLE_FAILOVER=yes` → cloudlet 检测断连后自动重连 MANAGEMENT_VIP:9988
> - 只开一边不行：只开控制端，计算节点不会主动重连；只开计算端，新 MASTER 拒绝连接

### 步骤 8: cloudland-entrypoint.sh 不做改动

容器的生命周期完全由 `docker compose start/stop` 控制，entrypoint 保持原有的 `exec /opt/cloudland/bin/cloudland`。

> **部署脚本关系：** `deploy-ha-node.sh` 是完全独立的脚本，不调用 `deploy-control-node.sh`。它自身包含完整的部署流程（系统检查、Docker 安装、.env 配置、证书生成、容器构建启动），在此基础上增加 keepalived 安装配置和 HA 特有逻辑。两个脚本是平行关系：
> - 单节点开发部署 → `deploy-control-node.sh`
> - 双节点 HA 部署 → `deploy-ha-node.sh`

## 涉及文件

| 操作 | 文件 |
|------|------|
| 修改 | `deploy/docker/docker-compose.yml` |
| 修改 | `deploy/docker/.env.example` |
| 修改 | `deploy/docker/scripts/web-entrypoint.sh` |
| 修改 | `deploy/docker/scripts/deploy-control-node.sh` |
| 修改 | `deploy/docker/scripts/deploy-compute-node.sh` |
| 新建 | `deploy/docker/config/keepalived/keepalived.conf.template` |
| 新建 | `deploy/docker/scripts/ha-notify.sh` |
| 新建 | `deploy/docker/scripts/deploy-ha-node.sh` |

## 部署流程

### MASTER 节点

```bash
export HA_ROLE=MASTER
export MANAGEMENT_VIP=10.0.0.100
export INTERNAL_IP=10.0.0.5
export PEER_IP=10.0.0.6
export VRRP_INTERFACE=eth0
export DB_HOST=10.0.0.200   # 外部 DB 地址

sudo -E bash deploy/docker/scripts/deploy-ha-node.sh
# → 安装 keepalived (MASTER, priority=101)
# → docker compose up -d --build（容器全部运行）
```

### BACKUP 节点

```bash
export HA_ROLE=BACKUP
export MANAGEMENT_VIP=10.0.0.100
export INTERNAL_IP=10.0.0.6
export PEER_IP=10.0.0.5
export MASTER_IP=10.0.0.5
export VRRP_INTERFACE=eth0
export DB_HOST=10.0.0.200   # 同一个外部 DB

sudo -E bash deploy/docker/scripts/deploy-ha-node.sh
# → 安装 keepalived (BACKUP, priority=100)
# → docker compose up -d --build → docker compose stop（容器已创建但未运行）
```

## 主从同步策略

### 部署时同步（BACKUP 从 MASTER 一次性复制）

| 文件/目录 | 说明 | 同步方式 |
|-----------|------|----------|
| `deploy/.ssh/cland.key` + `.pub` | SCI 通信用 SSH 密钥，计算节点信任此密钥 | `deploy-ha-node.sh` 中 BACKUP 角色自动 rsync |
| `volumes/certs/` | TLS 证书（nginx、clbase/clapi、VNC console） | 同上 |
| `.env` | 环境变量配置 | 脚本自动生成，`INTERNAL_IP` 和 `HA_ROLE` 两台不同，其余相同 |

### 运行期间持续同步

| 文件/目录 | 变更时机 | 影响 | 同步方案 |
|-----------|----------|------|----------|
| `volumes/host.list` | 新增/删除计算节点时 | cloudland 启动时读取，决定 SCI 连接哪些计算节点。BACKUP 的 host.list 过期 → 切换后丢失新计算节点 | cron 定时 rsync（每 1 分钟） |
| ~~Prometheus 告警规则~~ | ~~Web UI 创建/修改告警规则时~~ | ~~切换后告警规则不一致~~ | 暂不处理，后续再考虑 |

### 不需要同步

| 文件/目录 | 原因 |
|-----------|------|
| PostgreSQL 数据 | 两台连同一个外部 DB，天然一致 |
| Prometheus TSDB | 监控历史数据，丢失后可从计算节点重新采集 |
| Alertmanager 数据 | 告警历史和静默状态，非关键 |
| Grafana dashboards | 可通过 provisioning 或导入重建 |
| CloudLand cache/log | 缓存可重建，日志不影响功能 |

### 同步脚本方案

在 `deploy-ha-node.sh` 中为 **两台节点** 配置 cron 任务，MASTER 定期推送到 BACKUP：

```bash
# /etc/cron.d/cloudland-ha-sync (仅 MASTER 节点生效)
* * * * * root rsync -az /opt/cloudland/deploy/docker/volumes/host.list PEER_IP:/opt/cloudland/deploy/docker/volumes/host.list 2>/dev/null
```

> **ha-notify.sh 中处理角色切换时的 cron：** 当 MASTER → BACKUP 时禁用同步 cron，BACKUP → MASTER 时启用同步 cron。这样只有当前 MASTER 才会推送文件。

## 验证方法

```bash
# 1. 检查 keepalived 和 VIP
systemctl status keepalived
ip addr show $VRRP_INTERFACE | grep $MANAGEMENT_VIP

# 2. 检查服务状态
docker compose ps  # MASTER: 全部 running；BACKUP: 全部 stopped

# 3. 模拟故障切换
sudo systemctl stop keepalived  # 在 MASTER 上执行
# BACKUP 上验证：VIP 已漂移，ha-notify.sh 自动执行 docker compose start

# 4. 故障恢复（nopreempt 模式，VIP 不会自动回切）
sudo systemctl start keepalived  # 原 MASTER 变为 BACKUP 角色
```

## 已知限制

- **DB 外部依赖**：需要提前准备好可用的 PostgreSQL，两台控制节点都连同一个 DB
- **host.list 同步延迟**：cron 每分钟同步一次，极端情况下新增计算节点后 1 分钟内发生切换可能丢失
- **Prometheus 告警规则未同步**：暂不处理，切换后需手动重建告警规则
- **nopreempt**：故障恢复后 VIP 不会自动回切，需手动干预或去掉 nopreempt
- **cron 同步依赖 SSH 免密**：MASTER → BACKUP 的 rsync 需要 root SSH 免密登录
