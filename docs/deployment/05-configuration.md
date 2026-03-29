# 配置参考手册

CloudLand 采用分层配置策略。对于容器化部署，大部分关键参数通过 Docker 环境变量控制，而业务细节则通过配置文件挂载。

---

## 环境变量 (.env)

所有 Docker 组件的配置都集中在 `deploy/docker/.env` 文件中。以下是根据功能模块划分的核心变量参考：

### 1. 部署模式
| 变量名 | 说明 | 示例 |
| :--- | :--- | :--- |
| `COMPOSE_PROFILES` | 控制启用的组件集合，多个 Profile 用逗号分隔。 | `full,dev,region` |

可用 Profile：
- **`region`** — 区域控制面（cloudland、clapi、consoleproxy、监控栈）
- **`full`** — 中央控制面（Nginx + CPGateway + Web UI）
- **`dev`** — 本地容器化 PostgreSQL

常用组合参考 [部署模式说明](./index.md#部署模式说明)。

### 2. 基础网络
| 变量名 | 说明 | 示例 |
| :--- | :--- | :--- |
| `PUBLIC_IP` | 系统的外部访问出口（弹性 IP 或公网 IP）。 | `121.43.x.x` |
| `INTERNAL_IP` | 跨组件通信的私有 IP。 | `172.16.0.10` |
| `MANAGEMENT_VIP` | 在 HA 模式下使用的虚拟漂移 IP。 | `172.16.0.100` |

### 3. 数据库配置
| 变量名 | 说明 | 示例 |
| :--- | :--- | :--- |
| `DB_HOST` | 数据库地址（单机填 `postgres` 使用自带容器）。 | `postgres` 或 `10.0.x.x` |
| `POSTGRES_PASSWORD` | 数据库 ROOT 密码。 | `d6Passwd` |
| `DB_NAME` | CloudLand 核心元数据数据库名。 | `cloudland` |

### 4. 安全与认证
| 变量名 | 说明 | 示例 |
| :--- | :--- | :--- |
| `CPGATEWAY_SECRET_KEY` | 认证密钥，用于 Gateway 与 API 之间的可信验证。 | `RANDOM_STRING_HERE` |
| `ADMIN_PASSWORD` | 系统初始管理员 `admin` 的密码。 | `AgFFTFV8AzK4FG0` |
| `ADMIN_EMAIL` | 管理员邮箱地址。 | `admin@local.com` |

### 5. 通知与邮件
| 变量名 | 说明 | 示例 |
| :--- | :--- | :--- |
| `NOTIFICATION_METHOD` | 用户注册激活通知方式: `email` / `feishu` / `both`。 | `email` |
| `SMTP_HOST` | SMTP 邮件服务器地址。 | `smtp.example.com` |
| `SMTP_PORT` | SMTP 端口。 | `587` |
| `SMTP_TLS` | 是否启用 TLS。 | `True` |
| `SMTP_USER` | SMTP 认证用户名。 | `alert@example.com` |
| `SMTP_PASS` | SMTP 认证密码。 | `changeme` |
| `ALERT_SENDER` | 告警发件人地址。 | `alert@example.com` |
| `ALERT_RECIPIENT` | 告警收件人地址。 | `admin@local.com` |

### 6. 飞书通知（可选）
| 变量名 | 说明 | 示例 |
| :--- | :--- | :--- |
| `FEISHU_WEBHOOK_URL` | 飞书自定义机器人 Webhook 地址。 | `https://open.feishu.cn/...` |
| `FEISHU_SECRET` | 飞书机器人签名校验密钥（可选）。 | - |

### 7. 监控
| 变量名 | 说明 | 示例 |
| :--- | :--- | :--- |
| `CLAPI_SD_ENDPOINT` | Prometheus http_sd 服务发现地址。 | `http://clapi:8255` |
| `GRAFANA_ADMIN_PASSWORD` | Grafana 管理员密码。 | `admin` |
| `SLACK_WEBHOOK_URL` | Slack 告警 Webhook（可选）。 | `https://hooks.slack.com/...` |
| `SLACK_CHANNEL` | Slack 告警频道。 | `#cloudland-alerts` |

---

## 核心目录结构

部署于 `/opt/cloudland` 目录下的关键结构如下：

```text
/opt/cloudland/deploy/docker/
├── docker-compose.yml       # 容器编排主文件
├── .env                     # 运行时环境变量配置
├── config/                  # 静态配置文件目录
│   ├── nginx/               # Nginx 站点配置及负载均衡设置
│   ├── prometheus/          # 监控采集规则
│   └── grafana/             # 这里的 dashboard 定义
├── scripts/                 # 管理及自动化部署脚本
└── volumes/                 # 持久化数据目录
    ├── certs/               # 系统自动生成的 SSL/TLS 证书
    ├── pg-data/             # 容器化数据库存储（仅 dev 模式）
    ├── prometheus-data/     # 监控指标时序数据
    └── host.list            # 注册在线的计算节点名单（格式: 每行一个 hostname）
```

---

## 高级自定义配置

如需修改底层微服务的详细参数（例如 API 超时、日志等级），可以采用 **容器挂载** 的方式覆盖默认配置。

以 `clapi` 为例，您可以自定义其 `config.toml`：

1. **准备配置文件**:
   ```bash
   cp deploy/docker/config/web/config.toml.example /opt/cloudland/custom_clapi.toml
   vi /opt/cloudland/custom_clapi.toml
   ```

2. **在 docker-compose.yml 中添加映射**:
   ```yaml
   services:
     clapi:
       volumes:
         - /opt/cloudland/custom_clapi.toml:/opt/cloudland/web/conf/config.toml:ro
   ```

3. **重启服务**:
   ```bash
   docker compose up -d clapi
   ```

---

## 证书初始化管理

CloudLand 依赖 SSL 通信。如果您需要手动重新生成证书（例如更换了 `PUBLIC_IP`），请执行：

```bash
cd /opt/cloudland/deploy/docker
# 清除旧证书
rm -rf volumes/certs/*
# 触发重新生成
bash scripts/init-certs.sh
# 重启依赖服务
docker compose restart nginx clapi consoleproxy
```

> [!NOTE]
> `init-certs.sh` 会根据 `.env` 中的 IP 地址自动生成适合当前环境的证书。
