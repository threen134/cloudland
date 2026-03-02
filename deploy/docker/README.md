# CloudLand Docker 部署指南

## 目录

- [架构概览](#架构概览)
- [前提条件](#前提条件)
- [快速开始](#快速开始)
- [配置说明](#配置说明)
- [添加计算节点](#添加计算节点)
- [目录结构](#目录结构)
- [运维命令](#运维命令)
- [故障排查](#故障排查)

---

## 架构概览

本方案将 CloudLand **控制面**容器化，**计算面**保持裸机部署：

```
┌──────────── 控制节点 (Docker Compose) ──────────────────────┐
│                                                            │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────┐ │
│  │  nginx   │  │  clbase  │  │  clapi   │  │consoleproxy │ │
│  │  :80/443 │  │  :5443   │  │  :8255   │  │   :9443     │ │
│  └──────────┘  └──────────┘  └──────────┘  └─────────────┘ │
│                                                            │
│  ┌──────────┐  ┌──────────┐                                │
│  │ postgres │  │cloudland │ ← network_mode: host           │
│  │  :5432   │  │  :9988   │   (与 hyper 节点 SCI 通信)       │
│  └──────────┘  └──────────┘                                │
│                                                            │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────┐ │
│  │prometheus│  │alertmgr  │  │ grafana  │  │alarm-rules  │ │
│  │  :9090   │  │  :9093   │  │  :3000   │  │   :8256     │ │
│  └──────────┘  └──────────┘  └──────────┘  └─────────────┘ │
└────────────────────────────────────────────────────────────┘
                            │
                     SCI 协议 (端口 9988)
                            │
        ┌───────────────────┼───────────────────┐
        ▼                   ▼                   ▼
┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│ 计算节点 1    │  │ 计算节点 2    │  │ 计算节点 N   │
│  scid        │  │  scid        │  │  scid        │
│  cloudlet    │  │  cloudlet    │  │  cloudlet    │
│  libvirtd    │  │  libvirtd    │  │  libvirtd    │
│  KVM/QEMU    │  │  KVM/QEMU    │  │  KVM/QEMU   │
└──────────────┘  └──────────────┘  └──────────────┘
```

### 服务与端口详细说明

在上述微服务架构中，各核心组件所绑定和串联的网络交互端口及其主要职责如下：

| 组件角色 | 监听端口 | 协议类型 | 部署与绑定建议 | 核心作用说明 |
|----------|----------|----------|----------------|--------------|
| **nginx** | `80`、`443` | HTTP(S) | 面向公网 / 管理网 (`PUBLIC_IP`) | 充当系统的反向代理层与统一出入口，承载用户的真实流量并进行 SSL 卸载。 |
| **consoleproxy** | `9443` | WSS | 反代网及内网通信 (`INTERNAL_IP`) | **VNC 代理服务**，将底层计算节点暴漏的虚拟显示器串流转换为可通过浏览器页面渲染的 WebSockets 数据帧。 |
| **clbase** | `5443` | HTTPS | 反代网及内网通信 (`INTERNAL_IP`) | **Web 后端控制器主服务**，负责承接所有从浏览器 Portal 页面提交的主机管理请求和页面渲染逻辑。 |
| **clbase** | `5005` | HTTP(RPC) | 仅限宿主机或内网 (`INTERNAL_IP`) | **状态更新回调端点**，专门监听并接收 C++ 主控 `cloudland` 等底层执行异步长耗时任务（如创建虚机卷等）完成后的状态结果，回写到数据库。 |
| **clapi** | `8255` | HTTPS | 反代网及内网通信 (`INTERNAL_IP`) | **RESTful API 服务端点**，专为 Terraform、脚本和第三方云管平台提供标准的可编程控制原语接口。 |
| **cloudland** | `9988` | SCI (TCP) | 限定特定物理网卡 (`NETWORK_DEVICE`) | **南向安全信道**（基于宿主网络），负责与所有物理机的 `cloudlet` 代理维持自定义的保活长连接通讯链路，以承载底层的真实操作。 |
| **cloudland** | `5006` | HTTP(RPC) | 仅限宿主机或内网 (`INTERNAL_IP`) | **北向指令分发接口**，承接上层模块 (如 clbase/clapi 集群) 触发的操作命令，并将其精准投递到对应 9988 SCI 长连接里的物理机上。 |
| **postgres** | `5432` | TCP | 仅限宿主机或内网 (`DB_LISTEN_IP`) | 平台核心关系型数据库，存储租户、网络配置、各类资源的元数据。 |
| **prometheus**等 | `9090`等 | HTTP | 建议通过内网访问隔离 | 包括 **Grafana (3000)**、**Alertmanager (9093)**、**alarm-rules (8256)** 构成体系内一体化的资源巡查调度及运维监控大屏。 |

> **⚠️ 网络安全与端口暴露说明：**
> 在此架构中，为了最高级别的安全隔离，**仅有 Nginx 的 `80/443` 端口是直接暴露在公网/外部网络 (`0.0.0.0`)的**，充当了所有流量的统一网关。
> 其他后端核心组件（如 `clbase`, `clapi`, `consoleproxy`, `postgres` 甚至是未代理出来的监控端点），其端口绑定均受 `.env` 中的 `INTERNAL_IP` 环境变量强力约束。在未指定的情况下，它们**默认只监听在宿主机的本地回环接口 (`127.0.0.1`)**，彻底防止了物理公网的端口扫描与直连攻击。

## 前提条件

**控制节点：**
- Linux (Ubuntu 20.04+ 推荐)
- Docker Engine 24.0+
- Docker Compose v2.20+
- `gnutls-bin` (用于生成证书，Ubuntu: `apt install gnutls-bin`)
- `openssl` (用于生成 DH 参数)
- 至少 4GB 内存，20GB 磁盘

**计算节点：**
- Linux (Ubuntu 20.04+ 推荐)
- KVM/QEMU 支持 (`/dev/kvm` 存在)
- 与控制节点网络互通

## 快速开始

### 第一步：一键自动部署 (推荐)

如果您想在全新的环境中快速完成部署（例如直接在您的云服务器上执行），只需在 bash 中运行以下命令：

```bash
# 1. 设置配置参数
export PUBLIC_IP=1.2.3.4
export INTERNAL_IP=192.168.1.100
export NETWORK_DEVICE=eth0
export MANAGEMENT_VIP=192.168.1.100
export DB_LISTEN_IP=127.0.0.1
export POSTGRES_USER=postgres
export POSTGRES_PASSWORD=your_db_password
export POSTGRES_DB=cloudland
export ADMIN_PASSWORD=your_admin_password

# 2. 执行一键部署脚本
curl -sSL https://raw.githubusercontent.com/threen134/cloudland/master/deploy/docker/scripts/deploy-control-node.sh | sudo -E bash
```

> **💡 参数说明：**
> 
> | 变量名 | 必填 | 详细说明 (来自 .env.example) |
> | :--- | :--- | :--- |
> | `PUBLIC_IP` | 是 | **公网/外部 IP**：用于 Web UI、REST API 以及 VNC 控制台代理的主入口。 |
> | `INTERNAL_IP` | 是 | **内部管理网 IP**：用于组件间 RPC 通信及计算节点（Hyper）与控制节点间的 SCI 通信（9988 端口）。 |
> | `NETWORK_DEVICE` | 是 | **管理网卡名**：`cloudland` 主控监听的网络设备（对应 `INTERNAL_IP` 所在的网卡）。 |
> | `MANAGEMENT_VIP` | 是 | **管理 VIP**：单节点部署填 `INTERNAL_IP` 即可。 |
> | `DB_LISTEN_IP` | 否 | **数据库监听 IP**：推荐 `127.0.0.1` 或内网地址，确保数据库不暴露给公网。 |
> | `POSTGRES_PASSWORD` | 是 | **数据库密码**：PostgreSQL 的连接密码。 |
> | `ADMIN_PASSWORD` | 是 | **管理员密码**：CloudLand Web 管理界面的登录密码。 |
>
> **💡 执行说明：**
> 1. 以上命令将自动完成：克隆代码、安装 Docker、配置网络与证书，并一键启动所有容器。
> 2. **必须使用 `sudo -E`**：这能确保您在当前 Shell 中 `export` 的环境变量能正确传递给脚本执行环境。
> 3. **SSH 密钥**：脚本会自动在 `deploy/.ssh/` 下生成 CloudLand 所需 `cland.key` 密钥对。

### 第二步：检查与验证

部署完成后，您可以分别通过 CLI 和浏览器验证各服务状态。

#### CLI 验证

```bash
# 1. 检查容器运行状态
docker compose ps

# 2. 检查实时服务日志 (Ctrl+C 退出)
docker compose logs -f

# 3. 验证 Web 路由
curl -k https://localhost:443

# 4. 验证 API 连通性 (应返回 JSON)
curl -k https://localhost:443/api/v1/

# 5. 验证监控服务健康状态
curl http://localhost:9090/-/healthy
```

**访问方式：**

| 服务 | URL | 默认凭据 |
|------|-----|---------|
| Web 管理界面 | `https://<PUBLIC_IP>:443` | admin / `ADMIN_PASSWORD` |
| REST API Swagger | `https://<PUBLIC_IP>:443/swagger/api/v1/` | - |
| Grafana | `http://<PUBLIC_IP>:3000` | admin / `GRAFANA_ADMIN_PASSWORD` |
| Prometheus | `http://<PUBLIC_IP>:9090` | - |

---

## 配置说明

### 环境变量

所有配置通过 `.env` 文件管理，详见 [.env.example](.env.example)。

### 自定义 config.toml

如需高级配置，可以手动创建 `config.toml` 并挂载进容器，跳过自动生成：

```bash
# 创建自定义配置
mkdir -p volumes/web
cp config/web/config.toml.example volumes/web/config.toml
vi volumes/web/config.toml
```

然后在 `docker-compose.yml` 的 `clbase` 和 `clapi` 服务中添加卷挂载：

```yaml
volumes:
  - ./volumes/web/config.toml:/opt/cloudland/web/conf/config.toml:ro
```

---

## 添加计算节点

计算节点需裸机部署，以下是完整的添加步骤。

### 方法一：使用 Ansible（推荐）

适用于批量部署多个计算节点。

#### 1. 准备 Ansible 主机清单

在控制节点上编辑 `deploy/hosts/hosts`：

```ini
[hyper]
hyper01 ansible_host=192.168.1.201 ansible_ssh_private_key_file=/opt/cloudland/deploy/.ssh/cland.key client_id=0 zone_name=zone0 virt_type=kvm-x86_64
hyper02 ansible_host=192.168.1.202 ansible_ssh_private_key_file=/opt/cloudland/deploy/.ssh/cland.key client_id=1 zone_name=zone0 virt_type=kvm-x86_64

[cland]
controller ansible_host=192.168.1.100

[web]
controller ansible_host=192.168.1.100
```

#### 2. 更新 host.list

将新节点的 hostname 添加到 `deploy/docker/volumes/host.list`：

```
hyper01
hyper02
```

#### 3. 部署计算节点

```bash
cd /opt/cloudland/deploy

# 仅部署 hyper 角色
ansible-playbook -i hosts/hosts service.yml --tags hyper

# 或使用完整部署
ansible-playbook -i hosts/hosts cloudland.yml --tags hyper
```

#### 4. 重启 cloudland 主控

```bash
cd /opt/cloudland/deploy/docker
docker compose restart cloudland
```

### 方法二：手动部署单个计算节点 (同机混部脚本)

适用于快速添加单个计算节点，也支持与控制面在同一台宿主机跑。

脚本通过 **环境变量** 实现参数化配置。目前支持的所有核心配置项及默认值如下：

| 环境变量 | 说明 | 默认值 |
| :--- | :--- | :--- |
| `CONTROLLER_IP` | 控制节点的 IP 地址（必填） | `192.168.1.100` |
| `HOSTNAME` | 本计算节点的名称 | `hyper01` |
| `NETWORK_DEVICE` | 承载 VXLAN 等管理的内部网卡名 | `eth0` |
| `VLAN_DEVICE` | 承载外网 VLAN 流量的网卡名 | (同 `NETWORK_DEVICE`) |
| `DNS_SERVER` | 虚拟机默认 DNS | `8.8.8.8` |
| `SCI_CLIENT_ID` | 节点ID，必须全局唯一并严格递增 | `0` |

#### 执行一键部署

1. **配置文件传参执行 (推荐)**：

   在执行脚本之前，先准备好本节点的配置参数文件。脚本会自动尝试读取当前目录下的 `compute.env`。

   ```bash
   cd /opt/cloudland/deploy/docker/scripts
   
   # 由模板复制生成配置文件
   cp compute.env.example compute.env
   
   # 编辑参数 (必须按实际环境修改 CONTROLLER_IP, HOSTNAME 等)
   vi compute.env
   
   # 执行部署
   sudo bash deploy-compute-node.sh
   ```
   
   *(注：如果您不想创建文件，脚本依然完全支持向上游兼容的 `CONTROLLER_IP="..." bash deploy-compute-node.sh` 内联环境变量直接注入方式。)*

2. **更新控制节点 Host 列表**：

   在控制节点上将新计算节点的 hostname 添加到数据库中，使其被主控纳管：

   ```bash
   echo "worknode01" >> /opt/cloudland/deploy/docker/volumes/host.list
   docker compose restart cloudland
   ```

> **同机混部架构特别提示：** 
> 我们的 `deploy-compute-node.sh` 内置了“防冲突自愈机制”。如果它检测到本节点运行着 `cloudland-nginx` 容器，将会智能放行 Web 端口并通过重启 Docker 来修复被清空的 `DOCKER-USER` iptables 防火墙链路。

#### 5. 配置并启动 scid

```bash
# 复制 service 文件
cat > /lib/systemd/system/scid.service <<EOF
[Unit]
Description=SCI daemon
After=network.target

[Service]
Type=forking
ExecStart=/bin/sh -c /opt/sci/sbin/scidv1
ExecStop=/usr/bin/killall scidv1
KillMode=process
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now scid
```

#### 6. 配置并启动 cloudlet

```bash
# 环境变量
cat > /etc/sysconfig/cloudlet <<EOF
SCI_CLIENT_ID=0
SCI_JOB_KEY=12345
SCI_AGENT_PATH=/opt/sci/bin
SCI_LOG_ENABLE=yes
SCI_LOG_DIRECTORY=/opt/cloudland/log
SCI_DEVICE_NAME=eth0
SCI_HEAD_ADDR=${CONTROLLER}
LD_LIBRARY_PATH=/opt/sci/lib64
EOF

# Service 文件
cat > /lib/systemd/system/cloudlet.service <<EOF
[Unit]
Description=Cloudlet service
After=network.target

[Service]
Type=simple
User=cland
EnvironmentFile=/etc/sysconfig/cloudlet
ExecStart=/opt/cloudland/bin/cloudlet
KillMode=process
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now cloudlet
```

> **注意：** `SCI_CLIENT_ID` 每个计算节点必须唯一（从 0 开始递增）。  
> `SCI_HEAD_ADDR` 填控制节点的 IP 地址。

#### 7. 配置 libvirtd

```bash
systemctl enable --now libvirtd

# 删除默认网络
virsh net-destroy default 2>/dev/null
virsh net-undefine default 2>/dev/null
```

#### 8. 配置 NetworkManager

```bash
systemctl enable --now NetworkManager
```

#### 9. 内核参数

```bash
# 加载模块
modprobe br_netfilter

# 设置参数
sysctl -w net.bridge.bridge-nf-call-iptables=1
sysctl -w net.bridge.bridge-nf-call-arptables=1
sysctl -w net.bridge.bridge-nf-call-ip6tables=1
sysctl -w net.netfilter.nf_conntrack_max=6553600
sysctl -w net.core.rmem_max=16777216
sysctl -w net.core.wmem_max=16777216

# 持久化
cat >> /etc/sysctl.conf <<EOF
net.bridge.bridge-nf-call-iptables=1
net.bridge.bridge-nf-call-arptables=1
net.bridge.bridge-nf-call-ip6tables=1
net.netfilter.nf_conntrack_max=6553600
net.core.rmem_max=16777216
net.core.wmem_max=16777216
EOF
```

#### 10. 更新控制节点

在控制节点上将新计算节点的 hostname 添加到 `host.list`：

```bash
echo "hyper-new" >> /opt/cloudland/deploy/docker/volumes/host.list
docker compose restart cloudland
```

### （可选）为计算节点安装监控

```bash
# 安装 node_exporter
apt install -y prometheus-node-exporter

# 重启并配置端口为 9101
cat > /etc/default/prometheus-node-exporter <<EOF
ARGS="--web.listen-address=:9101"
EOF
systemctl restart prometheus-node-exporter
```

然后更新控制节点的 `config/prometheus/prometheus.yml`，添加新节点：

```yaml
scrape_configs:
  - job_name: 'prometheus_node_exporter'
    static_configs:
      - targets: ['192.168.1.201:9101']
        labels:
          node_type: 'compute'
```

```bash
# 重启 Prometheus 使配置生效
docker compose restart prometheus
```

---

## 目录结构

```
deploy/docker/
├── docker-compose.yml        # 主编排文件
├── .env.example              # 环境变量模板 → 复制为 .env
├── .env                      # 实际环境变量（不入版本管理）
│
├── dockerfiles/
│   ├── Dockerfile.sci         # SCI 库编译基础镜像
│   ├── Dockerfile.cloudland   # cloudland 主控 (C++)
│   ├── Dockerfile.web         # clbase + clapi + alarm-rules-manager (Go)
│   └── Dockerfile.consoleproxy # VNC 控制台代理 (Go)
│
├── scripts/
│   ├── init-certs.sh          # 证书初始化脚本
│   ├── init-db.sql            # PostgreSQL 初始化
│   ├── cloudland-entrypoint.sh # cloudland 容器启动脚本
│   └── web-entrypoint.sh      # clbase/clapi 容器启动脚本
│
├── config/
│   ├── nginx/
│   │   ├── nginx.conf         # nginx 主配置
│   │   └── ssl.conf           # SSL 反向代理配置
│   └── prometheus/
│       └── prometheus.yml     # Prometheus 抓取配置
│
└── volumes/                   # 运行时数据（不入版本管理）
    ├── host.list              # 计算节点列表
    └── certs/                 # 证书目录（init-certs.sh 生成）
        ├── cland/
        ├── nginx/
        └── console/
```

---

## 运维命令

```bash
cd /opt/cloudland/deploy/docker

# ---------- 生命周期 ----------
docker compose up -d --build          # 构建并启动
docker compose down                   # 停止并删除容器
docker compose restart <service>      # 重启单个服务
docker compose stop                   # 停止所有服务

# ---------- 日志 ----------
docker compose logs -f                # 查看所有日志
docker compose logs -f clbase         # 查看单个服务日志
docker compose logs --tail=100 clapi  # 最近 100 行

# ---------- 状态 ----------
docker compose ps                     # 查看运行状态
docker compose top                    # 查看进程

# ---------- 更新 ----------
docker compose pull                   # 拉取最新官方镜像
docker compose up -d --build          # 重新构建自定义镜像
docker compose up -d --build clbase   # 仅重建单个服务

# ---------- 数据 ----------
docker compose exec postgres psql -U postgres cloudland  # 进入数据库
docker volume ls | grep cloudland                        # 查看数据卷
```

---

## 故障排查

### 容器启动失败

```bash
# 查看具体错误
docker compose logs <service-name>

# 常见原因：
# 1. 证书未生成 → 执行 bash scripts/init-certs.sh
# 2. 端口被占用 → netstat -tlnp | grep <port>
# 3. 数据库连接失败 → 检查 .env 中的密码配置
```

### cloudland 无法与计算节点通信

```bash
# 检查 cloudland 是否在 host 网络模式
docker inspect cloudland-cland | grep NetworkMode
# 应输出 "NetworkMode": "host"

# 检查 9988 端口是否监听
ss -tlnp | grep 9988

# 检查 host.list 是否正确
cat volumes/host.list

# 检查计算节点 scid 是否运行
ssh hyper01 "systemctl status scid"
```

### 数据库连接失败

```bash
# 测试数据库连接
docker compose exec postgres pg_isready -U postgres
# 应输出 "accepting connections"

# 检查数据库是否创建
docker compose exec postgres psql -U postgres -l | grep cloudland
```

### Web 界面无法访问

```bash
# 检查 nginx → clbase 连通性
docker compose exec nginx wget -qO- --no-check-certificate https://clbase:5443

# 检查证书文件
ls -la volumes/certs/nginx/
ls -la volumes/certs/cland/
```
