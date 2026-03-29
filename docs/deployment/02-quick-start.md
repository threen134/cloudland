---
order: 20
---
# 快速开始（单节点部署）

本指南介绍如何在 **单台 Ubuntu 控制节点** 上通过 Docker Compose 快速构建包含完整 Web UI、监控告警、用户认证及数据库的 CloudLand 全量环境。

---

## 准备工作

在执行部署脚本前，请确认您已满足 [前置要求](./01-prerequisites.md)，并且您已拥有 root 权限。

---

## 部署步骤

部署脚本将自动完成以下工作：克隆代码仓库、安装 Docker、生成 SSL 证书与 SSH 密钥、启动所有容器服务。您只需设置环境变量并执行一条命令。

### 1. 配置环境变量

```bash
# 核心配置参数
export PUBLIC_IP=1.2.3.4                 # 控制节点公网 IP（浏览器访问使用）
export INTERNAL_IP=192.168.1.100         # 管理网内网 IP（内部组件通信）
export NETWORK_DEVICE=eth0               # 主网卡名称
export MANAGEMENT_VIP=192.168.1.100      # 管理 VIP（单节点部署填 INTERNAL_IP）
export DB_LISTEN_IP=127.0.0.1            # 数据库监听 IP

# 安全参数（必填）
export POSTGRES_PASSWORD=your_db_password
export ADMIN_PASSWORD=your_admin_password
export ADMIN_EMAIL=admin@cloudland.local
export CPGATEWAY_SECRET_KEY=your_secret_key_change_me  # 重要：生产环境必须修改，用于认证同步

# 启用模式：全量单节点（中央控制面 + 区域控制面 + 本地数据库）
export COMPOSE_PROFILES=full,dev,region

# 可选：通知方式（email / feishu / both）
# export NOTIFICATION_METHOD=feishu
# export FEISHU_WEBHOOK_URL=https://open.feishu.cn/open-apis/bot/v2/hook/xxxxx
# export FEISHU_SECRET=your_feishu_secret
```

### 2. 执行一键部署
```bash
curl -sSL https://raw.githubusercontent.com/threen134/cloudland/staging/deploy/docker/scripts/deploy-control-node.sh | sudo -E bash
```

> [!IMPORTANT]
> - 请确保使用 `sudo -E`，以保证环境变量能够传递到脚本中。
> - 脚本会自动将代码克隆到 `/opt/cloudland`（如已存在则跳过）。
> - 部署过程中会生成自签名 SSL 证书（`volumes/certs`）及 SSH 密钥（`/opt/cloudland/deploy/.ssh`）。
> - 部署日志保存在 `/var/log/cloudland-control-deploy-*.log`。

---

## 验证部署

部署完成后，检查容器运行状态：
```bash
docker compose ps
```
所有容器应显示为 `Up` 状态。可以通过查看实时日志确认各个模块已成功初始化：
```bash
docker compose logs -f
```

---

## 访问您的云环境

您可以按以下 URL 和凭据登录。

| 服务 | URL | 默认账号 | 默认密码 |
| :--- | :--- | :--- | :--- |
| **Web 管理界面** | `https://<PUBLIC_IP>:443` | `admin` | `your_admin_password` |
| **API 参考** | `https://<PUBLIC_IP>:443/docs/api/` | - | - |
| **监控看板 (Grafana)** | `http://<PUBLIC_IP>:3000` | `admin` | `your_admin_password` |
| **监控指标 (Prometheus)** | `http://<PUBLIC_IP>:9090` | - | - |

---

## 首次初始化 (关键步骤)

全量模式部署后，系统尚不知道您部署的 Regional API 地址。请创建一个 Region（区域）来完成初始化。

### 方式一：通过 Web 管理界面（推荐）

1. 使用 `ADMIN_EMAIL` 和 `ADMIN_PASSWORD` 登录 Web UI。
2. 进入 **Administration > Regions**（区域管理）页面。
3. 点击 **创建** 按钮，填写以下信息：

| 字段 | 说明 | 示例 |
| :--- | :--- | :--- |
| Region Name | 区域唯一标识 | `default` |
| Display Name | 区域显示名称 | `默认区域` |
| Internal Endpoint | clapi 服务地址（`https://<INTERNAL_IP>:8255`） | `https://192.168.1.100:8255` |
| Internal Secret | 必须与 `.env` 中的 `CPGATEWAY_SECRET_KEY` 一致 | `your_secret_key_change_me` |

4. 创建成功后，系统会显示生成的 Secret，请妥善保管。

### 方式二：通过 REST API

```bash
# 获取 Token
TOKEN=$(curl -sk -X POST https://<PUBLIC_IP>/api/v1/auth/token \
  -H "Content-Type: application/json" \
  -d '{"username":"<ADMIN_EMAIL>", "password":"<ADMIN_PASSWORD>"}' | jq -r .access_token)

# 注册默认区域 (Region)
curl -sk -X POST https://<PUBLIC_IP>/api/v1/regions \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "default",
    "display_name": "默认区域",
    "internal_endpoint": "https://<INTERNAL_IP>:8255",
    "internal_secret": "<CPGATEWAY_SECRET_KEY>"
  }'
```

> [!TIP]
> - `internal_endpoint` 填写 `INTERNAL_IP`，即 clapi 服务实际监听的地址。
> - `internal_secret` 必须与 `.env` 中的 `CPGATEWAY_SECRET_KEY` 完全一致。

---

## 进阶：多区域扩展

如果您需要部署更多的地理区域，或在已有中央控制面的情况下通过**仅部署区域控制面**来横向扩展资源，请参考：

- [多区域部署 (Multi-Region)](./05-multi-region.md)

---

## 下一步

部署完成控制面后，请前往 [添加计算节点](./04-compute-node.md) 扩展您的云算力。
