#!/bin/bash
# ============================================================
# CloudLand 控制节点一键部署脚本 (Docker 方式)
# ============================================================
set -euo pipefail

CLOUDLAND_DIR="${CLOUDLAND_DIR:-/opt/cloudland}"
DEPLOY_DIR="$CLOUDLAND_DIR/deploy/docker"

log() { echo -e "\n\033[1;32m[$(date '+%H:%M:%S')] $1\033[0m"; }
warn() { echo -e "\033[1;33m[WARN] $1\033[0m"; }

if [[ $EUID -ne 0 ]]; then
   echo "错误: 本脚本必须以 root 权限运行"
   exit 1
fi

cd "$DEPLOY_DIR"

# ============ 1. 检查配置 ============
log "1/4 - 检查基础配置"
if [ ! -f ".env" ]; then
    warn "未找到 .env 文件！"
    echo "请先复制并配置环境变量："
    echo "  cp .env.example .env"
    echo "  vi .env"
    exit 1
fi

# ============ 2. 安装 Docker ============
log "2/4 - 检查 Docker 环境"
if ! command -v docker &>/dev/null || ! docker compose version &>/dev/null; then
    warn "未检测到 Docker 或 Docker Compose，开始自动安装..."
    curl -fsSL https://get.docker.com -o get-docker.sh
    sh get-docker.sh
    rm -f get-docker.sh
    systemctl enable --now docker
    if ! command -v docker &>/dev/null; then
        echo "错误: Docker 安装失败，请手动检查网络后重试"
        exit 1
    fi
else
    echo "Docker 环境已就绪."
fi

# ============ 3. 生成证书 ============
log "3/4 - 生成控制面安全证书"
bash scripts/init-certs.sh

# ============ 4. 启动容器 ============
log "4/4 - 启动 CloudLand 控制面服务"
docker compose pull || warn "部分官方镜像拉取失败，尝试回退本地构建..."
docker compose up -d --build

log "✅ 控制面部署已启动！"
echo "您可以通过命令检查服务状态："
echo "  cd $DEPLOY_DIR && docker compose ps"
echo "  cd $DEPLOY_DIR && docker compose logs -f"
