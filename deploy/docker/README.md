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

### 第一步：配置环境变量

```bash
cd /opt/cloudland/deploy/docker

# 复制环境变量模板
cp .env.example .env

# 编辑配置（至少修改以下项）
vi .env
```

**必须修改的配置项：**

| 变量 | 说明 | 示例 |
|------|------|------|
| `PUBLIC_IP` | 控制节点的 IP 地址 | `192.168.1.100` |
| `MANAGEMENT_VIP` | 管理 VIP（单节点填同 PUBLIC_IP） | `192.168.1.100` |
| `NETWORK_DEVICE` | 控制节点的网卡名 | `eth0` |
| `POSTGRES_PASSWORD` | 数据库密码 | 自定义强密码 |
| `ADMIN_PASSWORD` | 管理员登录密码 | 自定义强密码 |

### 第二步：生成证书与 SSH 密钥配置

1. **生成通讯证书**

   ```bash
   # 安装证书工具（如未安装）
   sudo apt install -y gnutls-bin openssl

   # 生成证书
   bash scripts/init-certs.sh
   ```

2. **准备与分发 SSH 密钥**

   CloudLand 主控需要通过 SSH 免密登录到各个计算节点执行任务（例如虚机热迁移）。默认 `docker-compose.yml` 会将宿主机的 `../.ssh` 挂载到容器内。

   因此，你需要在**宿主机**上执行以下操作：

   ```bash
   # 在部署目录 /opt/cloudland/deploy/ 下创建 .ssh 目录
   mkdir -p /opt/cloudland/deploy/.ssh

   # 生成指定的 SSH 密钥对 (不可设置密码)
   ssh-keygen -t rsa -N "" -f /opt/cloudland/deploy/.ssh/cland.key

   # 稍后在添加计算节点时，需要将生成的公钥（cland.key.pub）
   # 追加到每个计算节点 root (或 cland) 用户的 ~/.ssh/authorized_keys 中！
   ```

### 第三步：启动服务

```bash
# 构建并启动所有服务
docker compose up -d --build

# 查看启动状态
docker compose ps

# 查看日志
docker compose logs -f
```

### 第四步：验证

```bash
# 检查所有容器状态
docker compose ps

# 验证 Web 界面
curl -k https://localhost:443
# 应返回 HTML 页面

# 验证 API
curl -k https://localhost:443/api/v1/
# 应返回 JSON

# 验证 Prometheus
curl http://localhost:9090/-/healthy
# 应返回 "Prometheus Server is Healthy."

# 验证 Grafana
curl http://localhost:3000/api/health
# 应返回 {"commit":"...","database":"ok",...}
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

### 方法二：手动部署单个计算节点

适用于快速添加单个节点。

#### 1. 在计算节点上安装依赖

```bash
# 基础包
apt update && apt install -y jq wget mkisofs network-manager net-tools python3-pip

# KVM/虚拟化
apt install -y qemu-system-x86 qemu-utils bridge-utils ipcalc ipset \
    keepalived iputils-arping libvirt-daemon libvirt-daemon-system \
    libvirt-daemon-system-systemd libvirt-clients dnsmasq dnsmasq-utils conntrack

# Python
pip3 install pyparsing
```

#### 2. 创建 cland 用户与配置 SSH 免密

```bash
useradd -m -s /bin/bash cland
echo 'cland ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/cland

# 配置 SSH 免密访问
mkdir -p /home/cland/.ssh
# 将控制节点的 /opt/cloudland/deploy/.ssh/cland.key.pub 内容复制到 authorized_keys 中
# echo "ssh-rsa AAAAB3NzaC1..." > /home/cland/.ssh/authorized_keys
chown -R cland:cland /home/cland/.ssh
chmod 700 /home/cland/.ssh
chmod 600 /home/cland/.ssh/authorized_keys

# 为了支持热迁移等底层操作，同样建议给目标机的 root 用户配置免密
mkdir -p /root/.ssh
# echo "ssh-rsa AAAAB3NzaC1..." >> /root/.ssh/authorized_keys
chmod 700 /root/.ssh
chmod 600 /root/.ssh/authorized_keys
```

#### 3. 复制文件

从控制节点复制以下文件到计算节点：

```bash
CONTROLLER=192.168.1.100

# SCI 库
rsync -avz $CONTROLLER:/opt/sci/ /opt/sci/

# CloudLand 脚本和二进制
rsync -avz --exclude=cache --exclude=log --exclude=db \
    $CONTROLLER:/opt/cloudland/ /opt/cloudland/

# 创建必要目录
mkdir -p /opt/cloudland/{log,run,cache}
mkdir -p /opt/cloudland/cache/{backup,image,instance,meta,router,volume,dnsmasq,xml,qemu_agent}
chown -R cland:cland /opt/cloudland
```

#### 4. 配置 cloudrc.local

编辑 `/opt/cloudland/scripts/cloudrc.local`：

```bash
# 网络设备名
network_device=eth0
# VXLAN 设备
vxlan_device=eth0
# 域名
domain=example.com
# DNS 服务器
dns_server=8.8.8.8
# 超卖比
cpu_over_ratio=1
mem_over_ratio=1
disk_over_ratio=1
```

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
