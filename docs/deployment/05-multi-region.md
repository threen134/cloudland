---
order: 50
---
# 多区域部署 (Multi-Region)

本指南介绍如何在已有一个中央控制面（Central Control Plane）的基础上，通过部署多个区域控制面（Region Control Plane）来实现跨地域的云资源统一管理。

---

## 架构说明

在多区域架构中：
- **中央控制面**：负责用户认证 (IAM)、Web UI、全局 API 路由以及多区域资源调度。
- **区域控制面**：负责本区域内的计算、网络、存储资源的直接管理。每个区域拥有独立的 `clapi` 服务和本地数据库。

---

## 部署区域控制面

在新的服务节点上执行以下步骤，仅部署该区域所需的核心服务。

### 1. 配置环境变量

```bash
# 核心配置参数
export PUBLIC_IP=1.2.3.4                 # 该区域控制节点的公网 IP
export INTERNAL_IP=192.168.1.100         # 该区域控制节点的内网 IP（确保中央端可达）
export NETWORK_DEVICE=eth0               # 主网卡名称
export MANAGEMENT_VIP=192.168.1.100      # 填该区域的 INTERNAL_IP
export DB_LISTEN_IP=127.0.0.1            # 数据库监听 IP

# 安全参数
export POSTGRES_PASSWORD=your_db_password
export CPGATEWAY_SECRET_KEY=your_secret_key_change_me  # 必须与注册时保持一致，用于中央端认证同步
export ADMIN_EMAIL=admin@cloudland.local
```

### 2. 执行一键部署

```bash
# 启用模式：仅部署区域控制面及本地区域数据库
export COMPOSE_PROFILES=dev,region

# 执行部署
curl -sSL https://raw.githubusercontent.com/threen134/cloudland/staging/deploy/docker/scripts/deploy-control-node.sh | sudo -E bash
```

---

## 在中央控制面注册新区域

区域控制面部署完成后，必须在中央端的 Web UI 中进行注册，系统才能感知并接管该区域。

### 注册步骤

1. **登录中央 UI**：使用管理员账号登录中央控制面的 Web 管理界面。
2. **进入区域管理**：导航至 **Administration > Regions** 页面。
3. **添加区域**：点击 **Create** (或创建) 按钮，填写以下信息：

| 字段 | 说明 | 示例 |
| :--- | :--- | :--- |
| **Region Name** | 区域唯一标识符（字母/数字） | `region-2` |
| **Display Name** | 页面显示的友好名称 | `上海可用区` |
| **Internal Endpoint** | 该区域 clapi 的访问地址 | `https://<新区域_INTERNAL_IP>:8255` |
| **Internal Secret** | 部署时设置的 `CPGATEWAY_SECRET_KEY` | `your_secret_key_change_me` |

4. **保存并验证**：点击保存。系统将尝试连接新区域的 API。连接成功后，该区域的状态将变为 `Active`。

---

## 下一步

区域注册成功后，您需要为该区域添加计算节点：
- [添加计算节点](./04-compute-node.md)（注意在添加时选择正确的 Region 参数）
