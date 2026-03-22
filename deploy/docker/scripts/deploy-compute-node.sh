#!/bin/bash
# ============================================================
# CloudLand 计算节点一键部署脚本（Docker 控制面 + 同机计算节点）
# 完整对齐 Ansible all-in-one 部署
# ============================================================
set -euo pipefail

# 日志同时输出到终端和文件
DEPLOY_LOG="/var/log/cloudland-compute-deploy-$(date '+%Y%m%d-%H%M%S').log"
exec > >(tee -a "$DEPLOY_LOG") 2>&1

if [[ $EUID -ne 0 ]]; then
   echo "错误: 本脚本必须以 root 权限运行"
   exit 1
fi

# 检查操作系统版本 (必须为 Ubuntu 22)
if [ -f /etc/os-release ]; then
    . /etc/os-release
    if [ "$ID" != "ubuntu" ] || [ "${VERSION_ID%%.*}" != "22" ]; then
        echo "错误: 本脚本仅支持 Ubuntu 22 版本 (如 22.04)。"
        echo "当前系统: ${NAME:-未知} ${VERSION_ID:-未知}"
        exit 1
    fi
else
    echo "错误: 无法识别操作系统。本脚本仅支持 Ubuntu 22 版本。"
    exit 1
fi

# ============ 0. 准备系统环境 ============
echo -e "\033[1;32m[INFO] 配置系统时区为 UTC...\033[0m"
if command -v timedatectl &>/dev/null; then
    timedatectl set-timezone UTC
    echo "系统时区已设置为 UTC。"
else
    ln -sf /usr/share/zoneinfo/UTC /etc/localtime
    echo "通过 link 方式将系统时区设置为 UTC。"
fi


# ============ 读取配置文件 ============
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$SCRIPT_DIR/compute.env"

if [ -f "$ENV_FILE" ]; then
    echo -e "\033[1;32m[INFO] 检测到配置文件 $ENV_FILE，正在加载参数...\033[0m"
    source "$ENV_FILE"
fi

# ============ 环境变量配置 (必填参数) ============
CONTROLLER_IP="${CONTROLLER_IP:?错误: 必须设置 CONTROLLER_IP}"                 # 控制节点 IP
HOSTNAME="${HOSTNAME:?错误: 必须设置 HOSTNAME}"                                 # 本节点的 hostname
NETWORK_DEVICE="${NETWORK_DEVICE:?错误: 必须设置 NETWORK_DEVICE}"               # 物理网卡名 (跑 VXLAN)
VLAN_DEVICE="${VLAN_DEVICE:-$NETWORK_DEVICE}"                                   # 物理网卡名 (跑 VLAN)
PRIVATE_VLAN_DEVICE="${PRIVATE_VLAN_DEVICE:-$VLAN_DEVICE}"                      # 物理网卡名 (跑 RFC 1918 私有 VLAN，可选)
DOMAIN="${DOMAIN:?错误: 必须设置 DOMAIN}"                                         # 域名
DNS_SERVER="${DNS_SERVER:?错误: 必须设置 DNS_SERVER}"                             # DNS
SCI_CLIENT_ID="${SCI_CLIENT_ID:?错误: 必须设置 SCI_CLIENT_ID}"                    # 计算节点编号（唯一递增）
ZONE_NAME="${ZONE_NAME:-zone0}"                       # 可用区名称
VIRT_TYPE="${VIRT_TYPE:-kvm-x86_64}"                  # 虚拟化类型
CLOUDLAND_DIR="${CLOUDLAND_DIR:-/opt/cloudland}"      # CloudLand 安装目录
DEPLOY_DIR="$CLOUDLAND_DIR/deploy/docker"
SCI_ENABLE_FAILOVER="${SCI_ENABLE_FAILOVER:-no}"      # HA 模式设为 yes

# ============ 可选配置 ============
WDS_ADDRESS="${WDS_ADDRESS:-}"                      # WDS 存储地址（留空则不使用）
WDS_ADMIN="${WDS_ADMIN:-}"                          # WDS 管理员
WDS_PASS="${WDS_PASS:-}"                            # WDS 密码
WDS_POOL_ID="${WDS_POOL_ID:-}"                      # WDS 存储池 ID

# 其他计算节点列表（用于 /etc/hosts，格式: "IP HOSTNAME" 每行一条）
# 如只有本机则留空
OTHER_NODES=""
# 例如:
# OTHER_NODES="192.168.1.202 hyper02
# 192.168.1.203 hyper03"

# ============ 函数定义 ============
log() { echo -e "\n\033[1;32m[$(date '+%H:%M:%S')] $1\033[0m"; }
warn() { echo -e "\033[1;33m[WARN] $1\033[0m"; }

# ============ 1. base 角色：系统基础配置 ============
log "1/15 - 系统基础配置 (base role)"

# 设置 hostname
hostnamectl set-hostname "$HOSTNAME"

# 生成 /etc/hosts（与 Ansible base/templates/hosts.j2 对齐）
cat > /etc/hosts <<EOF
127.0.0.1   localhost localhost.localdomain localhost4 localhost4.localdomain4
::1         localhost localhost.localdomain localhost6 localhost6.localdomain6

# CloudLand nodes
$CONTROLLER_IP $HOSTNAME
EOF

# 追加其他节点
if [ -n "$OTHER_NODES" ]; then
    echo "$OTHER_NODES" >> /etc/hosts
fi

# 文件描述符上限
cat > /etc/security/limits.d/cloudland.conf <<EOF
root      soft    nofile          102400
root      hard    nofile          112640
*         soft    nofile          102400
*         hard    nofile          112640
EOF

# NTP 时间同步
apt-get update -qq
apt-get install -y ntp
systemctl enable --now ntp

# 删除 unattended-upgrade
apt-get remove -y unattended-upgrades 2>/dev/null || true

# 创建 cland 用户
id cland &>/dev/null || useradd -m -s /bin/bash cland
echo 'cland ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/cland

# 屏蔽 UFW
systemctl mask ufw 2>/dev/null || true
systemctl stop ufw 2>/dev/null || true

# ============ 2. base 防火墙规则 ============
log "2/15 - 配置 iptables 基础规则"

iptables -P INPUT ACCEPT
iptables -P FORWARD ACCEPT
iptables -P OUTPUT ACCEPT
iptables -F
iptables -I INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT
iptables -A INPUT -p icmp -j ACCEPT
iptables -A INPUT -i lo -j ACCEPT
iptables -A INPUT -p tcp -m state --state NEW -m tcp --dport 22 -j ACCEPT
iptables -I INPUT -s 10.0.0.0/8 -j ACCEPT
iptables -I INPUT -s 172.16.0.0/12 -j ACCEPT
iptables -I INPUT -s 192.168.0.0/16 -j ACCEPT
iptables -P INPUT DROP
iptables -P FORWARD DROP
iptables -P OUTPUT ACCEPT

# 针对 CloudLand Docker 同机混部的优化：当检测到本节点运行了 CloudLand 控制面容器时，才放行 Web 端口并重启 Docker 恢复网络链
if command -v docker &>/dev/null && docker ps --format '{{.Names}}' | grep -q 'cloudland-nginx'; then
    iptables -I INPUT -p tcp -m multiport --dports 80,443,4000 -j ACCEPT
fi

iptables-save -c > /etc/iptables.rules

# 恢复 Docker 的专属网络转发链 (如 DOCKER-USER)
if command -v docker &>/dev/null && systemctl is-active --quiet docker && docker ps --format '{{.Names}}' | grep -q 'cloudland-nginx'; then
    systemctl restart docker || true
fi

# 持久化 iptables
mkdir -p /etc/network/if-pre-up.d /etc/network/if-post-down.d
cat > /etc/network/if-pre-up.d/iptablesload <<'SCRIPT'
#!/bin/sh
iptables-restore < /etc/iptables.rules
exit 0
SCRIPT
chmod +x /etc/network/if-pre-up.d/iptablesload

cat > /etc/network/if-post-down.d/iptablessave <<'SCRIPT'
#!/bin/sh
iptables-save -c > /etc/iptables.rules
if [ -f /etc/iptables.downrules ]; then
   iptables-restore < /etc/iptables.downrules
fi
exit 0
SCRIPT
chmod +x /etc/network/if-post-down.d/iptablessave

# ============ 3. 安装依赖包 ============
log "3/15 - 安装依赖包"

apt-get install -y jq wget mkisofs network-manager net-tools python3-pip

apt-get install -y qemu-system-x86 qemu-utils bridge-utils ipcalc ipset \
    keepalived iputils-arping libvirt-daemon libvirt-daemon-system \
    libvirt-daemon-system-systemd libvirt-clients dnsmasq-base dnsmasq-utils \
    conntrack cloud-utils

pip3 install pyparsing

# ============ 4. SSH 配置 ============
log "4/15 - 配置 SSH 免密"

mkdir -p /home/cland/.ssh /root/.ssh "$CLOUDLAND_DIR/deploy/.ssh"

SSH_KEYS_INSTALLED=""

# 尝试从本地文件获取（兼容手动部署）
SEARCH_PATHS=("$SCRIPT_DIR/.ssh" "$CLOUDLAND_DIR/deploy/.ssh")
for p in "${SEARCH_PATHS[@]}"; do
    if [ -f "$p/cland.key.pub" ] && [ -f "$p/cland.key" ]; then
        # 检查文件是否可读，避免权限错误导致静默 fallback
        if [ ! -r "$p/cland.key.pub" ] || [ ! -r "$p/cland.key" ]; then
            warn "SSH 密钥文件存在于 $p 但不可读，请检查文件权限"
            continue
        fi
        # 公钥追加而非覆盖，避免清除管理员手动添加的公钥
        LOCAL_PUB=$(cat "$p/cland.key.pub")
        grep -qF "$LOCAL_PUB" /home/cland/.ssh/authorized_keys 2>/dev/null \
            || printf '%s\n' "$LOCAL_PUB" >> /home/cland/.ssh/authorized_keys
        grep -qF "$LOCAL_PUB" /root/.ssh/authorized_keys 2>/dev/null \
            || printf '%s\n' "$LOCAL_PUB" >> /root/.ssh/authorized_keys
        # 私钥写入所有需要的位置
        cp "$p/cland.key" "$CLOUDLAND_DIR/deploy/.ssh/cland.key"
        cp "$p/cland.key.pub" "$CLOUDLAND_DIR/deploy/.ssh/cland.key.pub"
        cp "$p/cland.key" /root/.ssh/id_rsa
        chmod 600 /root/.ssh/id_rsa "$CLOUDLAND_DIR/deploy/.ssh/cland.key"
        log "从本地文件获取 SSH 密钥: $p"
        SSH_KEYS_INSTALLED="local"
        break
    fi
done

if [ -z "$SSH_KEYS_INSTALLED" ]; then
    log "本地未找到 SSH 密钥，将在节点注册时从控制节点获取"
fi

chown -R cland:cland /home/cland/.ssh "$CLOUDLAND_DIR/deploy/.ssh"
chmod 700 /home/cland/.ssh /root/.ssh
chmod 600 /home/cland/.ssh/authorized_keys 2>/dev/null || true
chmod 600 /root/.ssh/authorized_keys 2>/dev/null || true

# ============ 5. 编译安装 SCI 和 CloudLand ============
log "5/15 - 编译安装 SCI 和 CloudLand 二进制"

apt-get install -y build-essential autoconf automake libtool make g++ libssl-dev libjsoncpp-dev

cd "$CLOUDLAND_DIR/sci"
./configure && make && make install

cd "$CLOUDLAND_DIR/src"
make clean && make && make install

# ============ 6. 创建目录结构 ============
log "6/15 - 创建目录结构"

mkdir -p "$CLOUDLAND_DIR"/{log,run,cache}
mkdir -p "$CLOUDLAND_DIR/cache"/{backup,image,instance,meta,router,volume,dnsmasq,xml,qemu_agent}
chown -R cland:cland "$CLOUDLAND_DIR"

# ============ 7. 创建 backend 软链接 ============
log "7/15 - 创建 scripts/backend → kvm 软链接"

ln -sfn "$CLOUDLAND_DIR/scripts/kvm" "$CLOUDLAND_DIR/scripts/backend"
chown -h cland:cland "$CLOUDLAND_DIR/scripts/backend"

# 设置脚本可执行权限
chmod -R 755 "$CLOUDLAND_DIR/scripts/kvm"

# ============ 8. 配置 cloudrc.local ============
# 与 Ansible hyper/templates/cloudrc.local.kvm-x86_64.j2 完全对齐
log "8/15 - 配置 cloudrc.local"

cat > "$CLOUDLAND_DIR/scripts/cloudrc.local" <<EOF
# -*- mode: sh -*-
dns_server=$DNS_SERVER
cloud_domain=$DOMAIN
cpu_over_ratio=1
mem_over_ratio=1
disk_over_ratio=1
vxlan_interface=$NETWORK_DEVICE
vlan_interface=${VLAN_DEVICE:-$NETWORK_DEVICE}
private_vlan_interface=${PRIVATE_VLAN_DEVICE:-${VLAN_DEVICE:-$NETWORK_DEVICE}}
use_lb=true
proxy_mode=true
wds_address=$WDS_ADDRESS
wds_admin=$WDS_ADMIN
wds_pass=$WDS_PASS
wds_pool_id=$WDS_POOL_ID
EOF
chown cland:cland "$CLOUDLAND_DIR/scripts/cloudrc.local"

# ============ 9. KVM 嵌套虚拟化 ============
log "9/15 - 配置 KVM 嵌套虚拟化"

cat > /etc/modprobe.d/kvm-nested.conf <<EOF
options kvm-intel nested=1
options kvm-intel enable_shadow_vmcs=1
options kvm-intel enable_apicv=1
options kvm-intel ept=1
EOF

# ============ 10. 配置并启动服务 ============
log "10/15 - 配置并启动 scid/cloudlet/libvirtd/NetworkManager"

mkdir -p /etc/sysconfig

# --- scid ---
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

# --- cloudlet 环境变量（与 Ansible hyper/templates/cloudlet.j2 完全对齐）---
cat > /etc/sysconfig/cloudlet <<EOF
SCI_JOB_KEY=12345
SCI_LIB_PATH=/opt/sci/lib64
SCI_AGENT_PATH=/opt/sci/bin
SCI_LOG_ENABLE=yes
SCI_LOG_DIRECTORY=$CLOUDLAND_DIR/log
SCI_ENABLE_LISTENER=yes
SCI_USE_EXTLAUNCHER=yes
SCI_ENABLE_FAILOVER=$SCI_ENABLE_FAILOVER
SCI_SEGMENT_SIZE=1048576
LD_LIBRARY_PATH=\$LD_LIBRARY_PATH:/opt/sci/lib64
SCI_CLIENT_ID=$SCI_CLIENT_ID
ZONE_NAME=$ZONE_NAME
VIRT_TYPE=$VIRT_TYPE
EOF

# --- cloudlet service ---
cat > /lib/systemd/system/cloudlet.service <<EOF
[Unit]
Description=Cloudlet service
After=network.target

[Service]
Type=simple
User=cland
EnvironmentFile=/etc/sysconfig/cloudlet
ExecStart=$CLOUDLAND_DIR/bin/cloudlet
KillMode=process
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

# 启动服务
systemctl daemon-reload
systemctl enable --now scid
systemctl enable --now cloudlet
systemctl enable --now libvirtd
systemctl enable --now NetworkManager

# 删除默认 libvirt 网络
virsh net-destroy default 2>/dev/null || true
virsh net-undefine default 2>/dev/null || true

# ============ 11. Netplan + networkd 配置 ============
log "11/15 - 切换 netplan 渲染器为 NetworkManager 并屏蔽 networkd"

# 下载 yq 工具
YQ=/tmp/yq
if [ ! -f "$YQ" ]; then
    wget -q https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64 -O "$YQ"
    chmod +x "$YQ"
fi

# 检查并切换 netplan 渲染器
for f in /etc/netplan/*.yaml; do
    if [ -f "$f" ]; then
        renderer=$("$YQ" '.network.renderer' "$f")
        if [ "$renderer" != "NetworkManager" ]; then
            "$YQ" '.network.renderer = "NetworkManager"' -i "$f"
            warn "已修改 $f 的渲染器为 NetworkManager，可能需要重启！"
        fi
    fi
done

# 停止并屏蔽 systemd-networkd
systemctl stop systemd-networkd 2>/dev/null || true
systemctl mask systemd-networkd

# ============ 12. 内核参数 ============
log "12/15 - 配置内核参数"

modprobe br_netfilter

sysctl -w net.bridge.bridge-nf-call-iptables=1
sysctl -w net.bridge.bridge-nf-call-arptables=1
sysctl -w net.bridge.bridge-nf-call-ip6tables=1
sysctl -w net.netfilter.nf_conntrack_max=6553600
sysctl -w net.core.rmem_max=16777216
sysctl -w net.core.wmem_max=16777216

# 持久化（避免重复追加）
grep -q "bridge-nf-call-iptables" /etc/sysctl.conf || cat >> /etc/sysctl.conf <<EOF
net.bridge.bridge-nf-call-iptables=1
net.bridge.bridge-nf-call-arptables=1
net.bridge.bridge-nf-call-ip6tables=1
net.netfilter.nf_conntrack_max=6553600
net.core.rmem_max=16777216
net.core.wmem_max=16777216
EOF

# ============ hyper 节点 iptables 补充 ============
iptables -D FORWARD -j REJECT --reject-with icmp-host-prohibited 2>/dev/null || true
iptables -A FORWARD -j REJECT --reject-with icmp-host-prohibited
iptables-save -c > /etc/iptables.rules

# ============ 13. 监控部署 (monitor_hyper 角色) ============
log "13/15 - 部署监控 agent"

# --- prometheus-node-exporter (端口 9101 + textfile 采集器) ---
apt-get install -y prometheus-node-exporter
systemctl stop prometheus-node-exporter 2>/dev/null || true

mkdir -p /etc/systemd/system/prometheus-node-exporter.service.d
cat > /etc/systemd/system/prometheus-node-exporter.service.d/override.conf <<EOF
[Service]
ExecStart=
ExecStart=/usr/bin/prometheus-node-exporter --web.listen-address=:9101 --collector.textfile.directory=/var/lib/node_exporter
EOF

mkdir -p /var/lib/node_exporter
id prometheus &>/dev/null && chown prometheus:prometheus /var/lib/node_exporter || true

systemctl daemon-reload
systemctl enable --now prometheus-node-exporter

# --- libvirt-exporter Docker 容器 ---
if command -v docker &>/dev/null; then
    docker rm -f prometheus-libvirt-exporter 2>/dev/null || true
    docker run -d \
        --name prometheus-libvirt-exporter \
        --network host \
        --privileged \
        --restart always \
        -v /:/host:ro,rslave \
        -v /var/run/libvirt:/var/run/libvirt \
        kiennt26/prometheus-libvirt-exporter:latest
else
    warn "Docker 未安装，跳过 libvirt-exporter 部署"
fi

# --- ip-block-exporter ---
mkdir -p "$CLOUDLAND_DIR/scripts/monitor"

cat > /etc/systemd/system/ip-block-exporter.service <<EOF
[Unit]
Description=Export blocked IP metrics
After=network.target

[Service]
Type=oneshot
User=root
ExecStart=$CLOUDLAND_DIR/scripts/monitor/export_blocked_ips.sh
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/ip-block-exporter.timer <<EOF
[Unit]
Description=Run IP block exporter periodically
Requires=ip-block-exporter.service

[Timer]
OnBootSec=10
OnUnitActiveSec=2min
Unit=ip-block-exporter.service

[Install]
WantedBy=timers.target
EOF

# --- packet-drop-exporter ---
cat > /etc/systemd/system/packet-drop-exporter.service <<EOF
[Unit]
Description=Export packet drop metrics
After=network.target

[Service]
Type=oneshot
User=root
ExecStart=$CLOUDLAND_DIR/scripts/monitor/export_packet_drops.sh
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/packet-drop-exporter.timer <<EOF
[Unit]
Description=Run packet drop exporter periodically
Requires=packet-drop-exporter.service

[Timer]
OnBootSec=10
OnUnitActiveSec=2min
Unit=packet-drop-exporter.service

[Install]
WantedBy=timers.target
EOF

systemctl daemon-reload
# 仅在监控脚本存在时启动 timer
if [ -f "$CLOUDLAND_DIR/scripts/monitor/export_blocked_ips.sh" ]; then
    systemctl enable --now ip-block-exporter.timer
else
    warn "export_blocked_ips.sh 不存在，跳过 ip-block-exporter"
fi
if [ -f "$CLOUDLAND_DIR/scripts/monitor/export_packet_drops.sh" ]; then
    systemctl enable --now packet-drop-exporter.timer
else
    warn "export_packet_drops.sh 不存在，跳过 packet-drop-exporter"
fi

# ============ 14. 计量部署 (metering_hyper 角色) ============
log "14/15 - 部署南北向流量计量"

mkdir -p "$CLOUDLAND_DIR/scripts/metering"

cat > /etc/systemd/system/north-south-metrics.service <<EOF
[Unit]
Description=Generate North-South Traffic Metrics
After=network.target

[Service]
Type=oneshot
User=root
ExecStart=$CLOUDLAND_DIR/scripts/metering/generate_north_south_metrics.sh
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/north-south-metrics.timer <<EOF
[Unit]
Description=Run North-South Metrics Generation every 5min
Requires=north-south-metrics.service

[Timer]
OnBootSec=10
OnUnitActiveSec=5min
Unit=north-south-metrics.service

[Install]
WantedBy=timers.target
EOF

systemctl daemon-reload
if [ -f "$CLOUDLAND_DIR/scripts/metering/generate_north_south_metrics.sh" ]; then
    systemctl enable --now north-south-metrics.timer
else
    warn "generate_north_south_metrics.sh 不存在，跳过 north-south-metrics"
fi

# ============ 15. 通过 API 向控制面注册计算节点 ============
log "15/15 - 通过 API 向控制面注册计算节点"
RPC_SERVER_PORT="${RPC_SERVER_PORT:-5006}"
for i in 1 2 3; do
    REGISTER_RESP=$(curl -sf -X POST "http://${CONTROLLER_IP}:${RPC_SERVER_PORT}/internal/node/add" \
        -H "Content-Type: application/json" \
        -d "{\"hostname\": \"${HOSTNAME}\", \"id\": ${SCI_CLIENT_ID}, \"level\": 1}" 2>/dev/null || true)
    if [ -n "$REGISTER_RESP" ] && echo "$REGISTER_RESP" | jq -e '.status == "ok"' &>/dev/null; then
        log "节点注册成功"

        # 从注册响应中提取 SSH key（如果本地未安装）
        if [ -z "$SSH_KEYS_INSTALLED" ]; then
            PUB_KEY=$(echo "$REGISTER_RESP" | jq -r '.public_key // empty')
            PRIV_KEY=$(echo "$REGISTER_RESP" | jq -r '.private_key // empty')
            if [ -n "$PUB_KEY" ] && [ -n "$PRIV_KEY" ]; then
                # 公钥 → authorized_keys（追加模式，避免覆盖已有公钥）
                grep -qF "$PUB_KEY" /home/cland/.ssh/authorized_keys 2>/dev/null \
                    || printf '%s\n' "$PUB_KEY" >> /home/cland/.ssh/authorized_keys
                grep -qF "$PUB_KEY" /root/.ssh/authorized_keys 2>/dev/null \
                    || printf '%s\n' "$PUB_KEY" >> /root/.ssh/authorized_keys

                # 私钥 → 三个位置（使用 printf 确保多行内容原样写入）：
                # 1. $deploy_dir/.ssh/cland.key — 运行时脚本读取路径（cloudrc:24）
                # 2. /root/.ssh/id_rsa — root 用户 SSH 默认路径
                # 3. /home/cland/.ssh/cland.key — cland 用户备份
                mkdir -p "$CLOUDLAND_DIR/deploy/.ssh"
                printf '%s\n' "$PRIV_KEY" > "$CLOUDLAND_DIR/deploy/.ssh/cland.key"
                printf '%s\n' "$PUB_KEY"  > "$CLOUDLAND_DIR/deploy/.ssh/cland.key.pub"
                cp "$CLOUDLAND_DIR/deploy/.ssh/cland.key" /root/.ssh/id_rsa
                cp "$CLOUDLAND_DIR/deploy/.ssh/cland.key" /home/cland/.ssh/cland.key

                # 权限设置
                chown -R cland:cland /home/cland/.ssh "$CLOUDLAND_DIR/deploy/.ssh"
                chmod 700 /home/cland/.ssh
                chmod 600 /home/cland/.ssh/authorized_keys /home/cland/.ssh/cland.key
                chmod 600 /root/.ssh/id_rsa /root/.ssh/authorized_keys
                chmod 600 "$CLOUDLAND_DIR/deploy/.ssh/cland.key"

                log "从控制节点注册响应中获取 SSH 密钥成功"
                SSH_KEYS_INSTALLED="api"
            else
                warn "控制节点未返回 SSH 密钥"
            fi
        fi
        break
    else
        warn "节点注册失败 (尝试 $i/3)，5 秒后重试..."
        sleep 5
    fi
done

if [ -z "$SSH_KEYS_INSTALLED" ]; then
    warn "未能获取 SSH 密钥，请手动配置"
fi

# ============ 16. 部署监控代理 Promtail (Trace 日志回传) ============
log "16/16 - 部署 Promtail 日志回传代理"

cat > /opt/cloudland/promtail-client.yaml <<EOF
server:
  http_listen_port: 9080
  grpc_listen_port: 0

positions:
  filename: /tmp/positions.yaml

clients:
  - url: http://${CONTROLLER_IP}:3100/loki/api/v1/push

scrape_configs:
- job_name: cloudland-scripts
  static_configs:
  - targets:
      - localhost
    labels:
      job: local-compute-logs
      host: ${HOSTNAME}
      __path__: /opt/cloudland/log/*.log
EOF

if command -v docker &>/dev/null; then
    docker rm -f promtail-agent 2>/dev/null || true
    docker run -d --name promtail-agent \
      --network host \
      --restart always \
      -v /opt/cloudland/log:/opt/cloudland/log:ro \
      -v /opt/cloudland/promtail-client.yaml:/etc/promtail/config.yml:ro \
      grafana/promtail:2.9.2 -config.file=/etc/promtail/config.yml
    log "Promtail 日志反向代理容器启动成功"
else
    warn "Docker 未安装，跳过 Promtail 日志回传代理部署"
fi

log "✅ 部署完成！"
echo ""
echo "注意事项："
echo "  1. 如修改了 netplan 渲染器，可能需要重启机器"
echo "  2. 如有多个计算节点，请修改脚本顶部 OTHER_NODES 变量并同步 /etc/hosts"
