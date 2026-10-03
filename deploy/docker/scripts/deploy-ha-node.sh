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
# 部署分支优先级：显式指定 > 已部署 .env 中的值 > 已有仓库当前检出的分支 > staging，
# 避免在旧 .env（无 REPO_BRANCH）的环境上重跑脚本时把 .env 与 deploy_command 改到默认分支
if [ -z "${REPO_BRANCH:-}" ] && [ -f "$DEPLOY_DIR/.env" ]; then
    REPO_BRANCH=$(grep '^REPO_BRANCH=' "$DEPLOY_DIR/.env" | cut -d'=' -f2- || true)
fi
if [ -z "${REPO_BRANCH:-}" ] && [ -d "$CLOUDLAND_DIR/.git" ]; then
    REPO_BRANCH=$(git -c safe.directory="$CLOUDLAND_DIR" -C "$CLOUDLAND_DIR" branch --show-current 2>/dev/null || true)
fi
REPO_BRANCH="${REPO_BRANCH:-staging}"

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
    git clone -b "$REPO_BRANCH" "$REPO_URL" "$CLOUDLAND_DIR"
fi

cd "$DEPLOY_DIR"

# ============ 1. 检查配置 ============
log "1/5 - 检查 HA 配置"

if [ ! -f ".env" ]; then
    cp .env.example .env
fi

mkdir -p volumes/alertmanager
chown -R 65534:65534 volumes/alertmanager

# Prometheus rule templates: clapi renders node alarm rules from these (mounted read-only
# at /etc/prometheus/node_templates). Without them every node alarm rule create fails with
# "File does not exist: <template>.yml.j2".
log "同步 Prometheus 规则模板..."
mkdir -p volumes/prometheus/node_templates volumes/prometheus/general_rules volumes/prometheus/rules_enabled
cp -f ../roles/monitor/templates/*.yml.j2 volumes/prometheus/node_templates/

vars=("PUBLIC_IP" "INTERNAL_IP" "MANAGEMENT_VIP" "NETWORK_DEVICE" "DB_LISTEN_IP" "POSTGRES_USER" "POSTGRES_PASSWORD" "POSTGRES_DB" "ADMIN_PASSWORD" "HA_ROLE" "PEER_IP" "VRRP_INTERFACE" "DB_HOST" "DB_PORT" "GRPC_AUTH_TOKEN" "CAPTURE_UPLOAD_SECRET" "VPN_SECRET_KEY" "GRPC_LISTEN" "TELEMETRY_LISTEN_IP" "REPO_BRANCH" "DEPLOY_SCRIPT_URL")

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
sed -i '/^COMPOSE_PROFILES=/d' .env
echo "COMPOSE_PROFILES=full,region" >> .env # 全量 + 区域控制面，使用外部 DB（不启用 dev）

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

# 两台控制节点必须一致的共享密钥，MASTER 未提供时自动生成，BACKUP 始终以 MASTER 的值为准：
# GRPC_AUTH_TOKEN（计算节点 deploy_command 中只下发一份）、
# CAPTURE_UPLOAD_SECRET（上传凭证可能由一台 clapi 签发、另一台校验）、
# VPN_SECRET_KEY（VPN 凭据由任一台 clapi 加密、另一台解密下发）
SHARED_SECRETS=("GRPC_AUTH_TOKEN" "CAPTURE_UPLOAD_SECRET" "VPN_SECRET_KEY")
if [[ "$HA_ROLE" == "MASTER" ]]; then
    if [ ! -f "$CLOUDLAND_DIR/deploy/.ssh/cland.key" ]; then
        ssh-keygen -t rsa -f "$CLOUDLAND_DIR/deploy/.ssh/cland.key" -N ""
    fi
    bash scripts/init-certs.sh
    for name in "${SHARED_SECRETS[@]}"; do
        if [ -z "$(grep "^${name}=" .env | cut -d'=' -f2- || true)" ]; then
            sed -i "/^${name}=/d" .env
            echo "${name}=$(openssl rand -hex 32)" >> .env
            log "已生成 ${name} 并写入 .env"
        fi
    done
else
    log "正在从 MASTER ($PEER_IP) 同步证书和 SSH 密钥..."
    rsync -avz "$PEER_IP:$CLOUDLAND_DIR/deploy/.ssh/" "$CLOUDLAND_DIR/deploy/.ssh/"
    rsync -avz "$PEER_IP:$DEPLOY_DIR/volumes/certs/" "$DEPLOY_DIR/volumes/certs/"

    for name in "${SHARED_SECRETS[@]}"; do
        log "正在从 MASTER ($PEER_IP) 同步 ${name}..."
        master_value=$(ssh -o BatchMode=yes root@"$PEER_IP" "grep '^${name}=' $DEPLOY_DIR/.env | cut -d'=' -f2-" || true)
        if [ -z "$master_value" ]; then
            warn "无法从 MASTER 读取 ${name}，请先完成 MASTER 节点部署"
            exit 1
        fi
        sed -i "/^${name}=/d" .env
        echo "${name}=${master_value}" >> .env
    done
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
