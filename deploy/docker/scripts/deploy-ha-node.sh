#!/bin/bash
# ============================================================
# CloudLand 控制节点 HA 部署脚本 (Docker + Keepalived)
# ============================================================
set -euo pipefail

# 日志同时输出到终端和文件
DEPLOY_LOG="/var/log/cloudland-ha-deploy-$(date '+%Y%m%d-%H%M%S').log"
exec > >(tee -a "$DEPLOY_LOG") 2>&1

CLOUDLAND_DIR="${CLOUDLAND_DIR:-/opt/cloudland}"
REPO_URL="${REPO_URL:-https://github.com/threen134/cloudland.git}"
DEPLOY_DIR="$CLOUDLAND_DIR/deploy/docker"

log() { echo -e "\n\033[1;32m[$(date '+%H:%M:%S')] $1\033[0m"; }
warn() { echo -e "\033[1;33m[WARN] $1\033[0m"; }

if [[ $EUID -ne 0 ]]; then
   echo "错误: 本脚本必须以 root 权限运行"
   exit 1
fi

# ============ 0. 系统环境与仓库 ============
log "0/5 - 准备环境"
if command -v timedatectl &>/dev/null; then
    timedatectl set-timezone UTC
fi

if [ ! -d "$CLOUDLAND_DIR" ]; then
    if ! command -v git &>/dev/null; then
        apt-get update && apt-get install -y git
    fi
    git clone "$REPO_URL" "$CLOUDLAND_DIR"
fi

cd "$DEPLOY_DIR"

# ============ 1. 检查配置 ============
log "1/5 - 检查 HA 配置"

if [ ! -f ".env" ]; then
    cp .env.example .env
fi

mkdir -p volumes/alertmanager
chown -R 65534:65534 volumes/alertmanager

vars=("PUBLIC_IP" "INTERNAL_IP" "MANAGEMENT_VIP" "NETWORK_DEVICE" "DB_LISTEN_IP" "POSTGRES_USER" "POSTGRES_PASSWORD" "POSTGRES_DB" "ADMIN_PASSWORD" "HA_ROLE" "PEER_IP" "VRRP_INTERFACE" "DB_HOST" "DB_PORT")

for var in "${vars[@]}"; do
    val="${!var:-}"
    if [ -n "$val" ]; then
        if grep -q "^${var}=" .env; then
            escaped_val=$(echo "$val" | sed 's/[&/\]/\\&/g')
            sed -i "s|^${var}=.*|${var}=${escaped_val}|" .env
        else
            echo "${var}=${val}" >> .env
        fi
    fi
done

# Force HA vars in .env
sed -i '/^SCI_ENABLE_FAILOVER=/d' .env
echo "SCI_ENABLE_FAILOVER=yes" >> .env
sed -i '/^COMPOSE_PROFILES=/d' .env
echo "COMPOSE_PROFILES=" >> .env # disable dev profile to use external DB

required_vars=("PUBLIC_IP" "INTERNAL_IP" "NETWORK_DEVICE" "MANAGEMENT_VIP" "ADMIN_PASSWORD" "HA_ROLE" "PEER_IP" "VRRP_INTERFACE" "DB_HOST")
missing_vars=()
for var in "${required_vars[@]}"; do
    val_in_env=$(grep "^${var}=" .env | cut -d'=' -f2- || echo "")
    if [[ -z "${!var:-}" && -z "$val_in_env" ]]; then
        missing_vars+=("$var")
    fi
done

if [ ${#missing_vars[@]} -ne 0 ]; then
    warn "缺少关键配置项: ${missing_vars[*]}"
    exit 1
fi

HA_ROLE=$(grep '^HA_ROLE=' .env | cut -d'=' -f2-)
PEER_IP=$(grep '^PEER_IP=' .env | cut -d'=' -f2-)
MANAGEMENT_VIP=$(grep '^MANAGEMENT_VIP=' .env | cut -d'=' -f2-)
VRRP_INTERFACE=$(grep '^VRRP_INTERFACE=' .env | cut -d'=' -f2-)

if [[ "$HA_ROLE" != "MASTER" && "$HA_ROLE" != "BACKUP" ]]; then
    warn "HA_ROLE 必须是 MASTER 或 BACKUP"
    exit 1
fi

# ============ 2. 安装依赖 ============
log "2/6 - 安装依赖"
export DEBIAN_FRONTEND=noninteractive
apt-get update && apt-get install -y keepalived rsync

# ============ 3. 检查节点间 SSH 免密 ============
log "3/6 - 检查节点间 root SSH 免密登录"
if ! ssh -o BatchMode=yes -o ConnectTimeout=5 root@"$PEER_IP" true 2>/dev/null; then
    warn "无法免密 SSH 到对端节点 root@$PEER_IP"
    echo "HA 部署要求两台控制节点之间已配置 root SSH 免密登录（用于证书同步和 host.list 同步）。"
    echo "请先在两台节点上执行以下操作："
    echo "  1. ssh-keygen -t rsa -N '' -f /root/.ssh/id_rsa  (如果还没有密钥)"
    echo "  2. ssh-copy-id root@<对端IP>"
    echo "  3. 重新运行本脚本"
    exit 1
fi

# ============ 4. 处理证书和 SSH 密钥 ============
log "4/6 - 处理证书和 SSH 密钥"
mkdir -p "$CLOUDLAND_DIR/deploy/.ssh"
mkdir -p "$DEPLOY_DIR/volumes/certs"

if [[ "$HA_ROLE" == "MASTER" ]]; then
    if [ ! -f "$CLOUDLAND_DIR/deploy/.ssh/cland.key" ]; then
        ssh-keygen -t rsa -f "$CLOUDLAND_DIR/deploy/.ssh/cland.key" -N ""
    fi
    bash scripts/init-certs.sh
else
    log "正在从 MASTER ($PEER_IP) 同步证书和 SSH 密钥..."
    rsync -avz "$PEER_IP:$CLOUDLAND_DIR/deploy/.ssh/" "$CLOUDLAND_DIR/deploy/.ssh/"
    rsync -avz "$PEER_IP:$DEPLOY_DIR/volumes/certs/" "$DEPLOY_DIR/volumes/certs/"
fi

# ============ 5. 检查 Docker 并启动服务 ============
log "5/6 - 配置 Docker 服务"
if ! command -v docker &>/dev/null || ! docker compose version &>/dev/null; then
    curl -fsSL https://get.docker.com -o get-docker.sh
    sh get-docker.sh
    rm -f get-docker.sh
    systemctl enable --now docker
fi

docker compose pull || true
docker compose up -d --build

if [[ "$HA_ROLE" == "BACKUP" ]]; then
    log "当前为 BACKUP 节点，停止容器运行 (等待 keepalived 唤醒)"
    docker compose stop
fi

# ============ 6. 配置并启动 Keepalived (最后启动，确保证书和容器已就绪) ============
log "6/6 - 配置并启动 Keepalived ($HA_ROLE)"

if [[ "$HA_ROLE" == "MASTER" ]]; then
    VRRP_PRIORITY=101
else
    VRRP_PRIORITY=100
fi
VRRP_ROUTER_ID="${VRRP_ROUTER_ID:-51}"
VRRP_AUTH_PASS="${VRRP_AUTH_PASS:-cloudland}"
VRRP_VIP_MASK="${VRRP_VIP_MASK:-24}"

chmod +x "$DEPLOY_DIR/scripts/ha-notify.sh"

mkdir -p /etc/keepalived
sed -e "s/%%HA_STATE%%/$HA_ROLE/g" \
    -e "s/%%VRRP_INTERFACE%%/$VRRP_INTERFACE/g" \
    -e "s/%%VRRP_ROUTER_ID%%/$VRRP_ROUTER_ID/g" \
    -e "s/%%VRRP_PRIORITY%%/$VRRP_PRIORITY/g" \
    -e "s/%%VRRP_AUTH_PASS%%/$VRRP_AUTH_PASS/g" \
    -e "s/%%MANAGEMENT_VIP%%/$MANAGEMENT_VIP/g" \
    -e "s/%%VRRP_VIP_MASK%%/$VRRP_VIP_MASK/g" \
    "$DEPLOY_DIR/config/keepalived/keepalived.conf.template" > /etc/keepalived/keepalived.conf

systemctl enable --now keepalived
systemctl restart keepalived

log "✅ HA 部署 ($HA_ROLE) 此节点处理完毕！"
