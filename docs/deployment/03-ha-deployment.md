---
order: 30
---
# 高可用 (HA) 部署

CloudLand 支持双控制节点 **Active/Standby (主备)** 模式。通过 Keepalived 实现 VRRP VIP 漂移，以及外部数据库支持，确保区域控制面在单一主节点发生故障时能够自动切换。

---

## 架构图

```text
┌─────────────────┐          ┌─────────────────┐
│  控制节点 A      │          │  控制节点 B      │
│  (MASTER)       │          │  (BACKUP)       │
│                 │          │                 │
│  Keepalived ────┼── VRRP ──┼── Keepalived    │
│  Docker 容器组   │          │  Docker 容器组   │
│  (运行中)       │          │  (已停止)       │
└───────┬─────────┘          └───────┬─────────┘
        │                            │
        └──── MANAGEMENT_VIP ────────┘
                    │
           ┌───────┴───────┐
           │  外部 PostgreSQL │
           └───────────────┘
```

**故障切换流程：**
1. **MASTER 故障**: 当主节点宕机后，Keepalived 在 3 秒内检测到心跳丢失。
2. **VIP 漂移**: 漂移到备节点 (BACKUP)，并执行 `ha-notify.sh` 脚本。
3. **容器启动**: 备节点自动执行 `docker compose start`。
4. **计算节点接入**: 计算节点检测到与主控通信断开，会自动重连到 `MANAGEMENT_VIP:9988`。

---

## 部署前提

1. **两台控制节点**: 硬件配置相同，网络互通且处于同一二层网络。
2. **外部 PostgreSQL 数据库**: 两台控制节点必须连接同一个数据库实例（不推荐在 HA 模式下使用本地 postgres 容器）。
3. **root 权限与 SSH 免密**: 两台节点之间必须配置双向的 `root` 用户 SSH 免密登录。

---

## 部署步骤

### 1. 配置 SSH 免密
在两台控制节点上相互克隆 SSH 密钥：
```bash
# 生成密钥
ssh-keygen -t rsa -N '' -f /root/.ssh/id_rsa
# A 节点执行
ssh-copy-id root@<节点B_IP>
# B 节点执行
ssh-copy-id root@<节点A_IP>
```

### 2. 部署 MASTER 节点 (控制节点 A)
```bash
export HA_ROLE=MASTER
export PUBLIC_IP=1.2.3.4                 # 与 B 节点相同
export INTERNAL_IP=10.0.0.5              # A 节点真实 IP
export MANAGEMENT_VIP=10.0.0.100         # VRRP VIP
export PEER_IP=10.0.0.6                  # B 节点真实 IP
export NETWORK_DEVICE=eth0
export VRRP_INTERFACE=eth0
export DB_HOST=10.0.0.200                # 外部数据库地址
export ADMIN_PASSWORD=your_admin_password

# 执行 HA 部署脚本
sudo -E bash scripts/deploy-ha-node.sh
```

### 3. 部署 BACKUP 节点 (控制节点 B)
```bash
export HA_ROLE=BACKUP
export PUBLIC_IP=1.2.3.4                 # 与 A 节点相同
export INTERNAL_IP=10.0.0.6              # B 节点真实 IP
export MANAGEMENT_VIP=10.0.0.100         # 与 A 节点相同
export PEER_IP=10.0.0.5                  # A 节点真实 IP
export NETWORK_DEVICE=eth0
export VRRP_INTERFACE=eth0
export DB_HOST=10.0.0.200                # 指向同一个外部数据库
export ADMIN_PASSWORD=your_admin_password

# 执行 HA 部署脚本
sudo -E bash scripts/deploy-ha-node.sh
```

---

## 验证切换逻辑

### 检查服务与 VIP
1. 在 **MASTER** 节点检查 VIP 是否已生效: `ip addr show eth0 | grep 10.0.0.100`。
2. 确认 **MASTER** 节点容器已启动: `docker compose ps`。
3. 确认 **BACKUP** 节点容器已创建但处于 `Exited` (或已停止) 状态。

### 模拟手动切换
1. 停止 MASTER 节点的 Keepalived: `sudo systemctl stop keepalived`。
2. 观察 BACKUP 节点的容器状态: 应当自动启动。
3. 观察 VIP 状态: 应当漂移至 BACKUP 节点网卡。

---

## 进阶配置参考

| 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `VRRP_ROUTER_ID` | `51` | 同一网段下多个 HA 集群时需唯一。 |
| `VRRP_AUTH_PASS` | `cloudland` | VRRP 认证密码，两节点必须一致。 |
| `DB_PORT` | `5432` | 外部数据库连接端口。 |
| `HA_SYNC_INTERVAL` | `1m` | 自动同步 SSH 密钥与证书的周期。 |

---

## HA 日常运维

### Keepalived 管理

```bash
# 查看 Keepalived 实时状态
systemctl status keepalived

# 查看 Keepalived 日志
journalctl -u keepalived -f

# 查看 VIP 是否在当前节点
ip addr show <VRRP_INTERFACE> | grep <MANAGEMENT_VIP>
```

### host.list 同步

MASTER 节点通过 cron 任务每分钟将 `host.list` 同步到 BACKUP 节点：

```bash
# 查看同步状态（MASTER 节点）
cat /etc/cron.d/cloudland-ha-sync

# BACKUP 节点上该任务已禁用
cat /etc/cron.d/cloudland-ha-sync.disabled
```

### 手动 VIP 回切

HA 使用 `nopreempt` 模式，故障恢复后 VIP 不会自动回切。如需手动回切：

```bash
# 在当前持有 VIP 的节点执行
sudo systemctl stop keepalived

# 在目标节点执行
sudo systemctl restart keepalived
```

### 部署日志

```bash
# 查看 HA 部署日志
ls /var/log/cloudland-ha-deploy-*.log
```

---

## 计算节点 HA 配置

HA 模式下，计算节点需开启 SCI 自动重连，以便主控切换后自动重连新 MASTER：

```bash
export SCI_ENABLE_FAILOVER=yes
# ... 其他计算节点环境变量 ...
sudo -E bash deploy/docker/scripts/deploy-compute-node.sh
```

---

## 已知限制

- **外部 DB 必须预先准备好**：两台控制节点连接同一个 PostgreSQL 实例，HA 不含数据库高可用。
- **host.list 同步延迟**：cron 每分钟同步一次，极端情况下切换时可能丢失最近新增的计算节点。
- **nopreempt 模式**：故障恢复后 VIP 不自动回切，需手动干预（见上方运维章节）。
- **Prometheus 告警规则未同步**：切换后需在新 MASTER 上手动重建自定义的告警规则。
- **监控数据不连续**：切换后 Prometheus 时序数据从新节点重新采集，历史数据在原节点。
