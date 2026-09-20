#!/bin/bash
# ============================================================
# CloudLand 控制节点一键部署脚本 (Docker 方式)
# 支持环境变量注入，适用于 curl | bash 部署
# ============================================================
set -euo pipefail

# 日志同时输出到终端和文件
DEPLOY_LOG="/var/log/cloudland-control-deploy-$(date '+%Y%m%d-%H%M%S').log"
exec > >(tee -a "$DEPLOY_LOG") 2>&1

CLOUDLAND_DIR="${CLOUDLAND_DIR:-/opt/cloudland}"
REPO_URL="${REPO_URL:-https://github.com/threen134/cloudland.git}"
DEPLOY_DIR="$CLOUDLAND_DIR/deploy/docker"
# 部署分支优先级：显式指定 > 已部署 .env 中的值 > 已有仓库当前检出的分支 > staging，
# 避免在旧 .env（无 REPO_BRANCH）的环境上重跑脚本时被悄悄切到默认分支
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

# 检查操作系统版本（支持 Ubuntu 24.04 / 26.04）
if [ -f /etc/os-release ]; then
    . /etc/os-release
    if [ "$ID" != "ubuntu" ]; then
        echo "错误: 本脚本仅支持 Ubuntu 系统。"
        echo "当前系统: ${NAME:-未知} ${VERSION_ID:-未知}"
        exit 1
    fi
    case "$VERSION_ID" in
        24.04|26.04) ;;
        *) warn "未验证的 Ubuntu 版本 ${VERSION_ID:-未知}，仅支持 24.04 / 26.04，继续执行可能失败" ;;
    esac
else
    echo "错误: 无法识别操作系统。本脚本仅支持 Ubuntu 系统。"
    exit 1
fi

# ============ 0. 准备系统环境 ============
log "0/5 - 配置系统时区为 UTC"
if command -v timedatectl &>/dev/null; then
    timedatectl set-timezone UTC
    echo "系统时区已设置为 UTC。"
else
    ln -sf /usr/share/zoneinfo/UTC /etc/localtime
    echo "通过 link 方式将系统时区设置为 UTC。"
fi


# ============ 0. 检查并准备仓库 ============
if [ ! -d "$CLOUDLAND_DIR" ]; then
    log "检测到目录 $CLOUDLAND_DIR 不存在，正在准备环境..."
    if ! command -v git &>/dev/null; then
        warn "未检测到 git，尝试安装..."
        apt-get update && apt-get install -y git
    fi
    log "正在克隆 CloudLand 仓库 (分支 $REPO_BRANCH)..."
    git clone -b "$REPO_BRANCH" "$REPO_URL" "$CLOUDLAND_DIR"
elif [ -d "$CLOUDLAND_DIR/.git" ]; then
    # 同机部署计算节点后仓库属主为 cland，root 执行 git 需声明 safe.directory；
    # 计算节点脚本会 chmod 脚本目录，忽略权限位变化，只把内容改动视为未提交修改
    git_repo=(git -c safe.directory="$CLOUDLAND_DIR" -c core.fileMode=false -C "$CLOUDLAND_DIR")
    if [ "$("${git_repo[@]}" branch --show-current)" != "$REPO_BRANCH" ]; then
        # api/.version 由 make 构建时重写，属编译产物，不算未提交改动
        if [ -n "$("${git_repo[@]}" status --porcelain --untracked-files=no -- . ':!api/.version')" ]; then
            echo "错误: $CLOUDLAND_DIR 有未提交的改动，无法切换到分支 $REPO_BRANCH，请先提交或还原后重试。"
            exit 1
        fi
        log "切换仓库分支到 $REPO_BRANCH..."
        "${git_repo[@]}" fetch origin "+refs/heads/$REPO_BRANCH:refs/remotes/origin/$REPO_BRANCH"
        # 以远端为准重置本地分支，避免沿用过期的同名本地分支
        "${git_repo[@]}" checkout -B "$REPO_BRANCH" "origin/$REPO_BRANCH"
    fi
fi

if [ ! -d "$DEPLOY_DIR" ]; then
    echo "错误: 部署目录 $DEPLOY_DIR 不存在，请检查仓库路径。"
    exit 1
fi

cd "$DEPLOY_DIR"

# ============ 1. 检查配置与环境变量注入 ============
log "1/5 - 检查基础配置"

# 如果没有 .env，则从 .env.example 创建
if [ ! -f ".env" ]; then
    log "未找到 .env 文件，正在从模板初始化..."
    cp .env.example .env
    
fi

# 预先创建并执行 Alertmanager 挂载目录权限 (UID 65534 为 nobody)
log "执行 Alertmanager 目录权限..."
mkdir -p volumes/alertmanager
chown -R 65534:65534 volumes/alertmanager

# Prometheus rule templates: clapi renders node alarm rules from these (mounted read-only
# at /etc/prometheus/node_templates). Without them every node alarm rule create fails with
# "File does not exist: <template>.yml.j2".
log "同步 Prometheus 规则模板..."
mkdir -p volumes/prometheus/node_templates volumes/prometheus/general_rules volumes/prometheus/rules_enabled
cp -f ../roles/monitor/templates/*.yml.j2 volumes/prometheus/node_templates/

# 定义需要注入的环境变量
vars=("PUBLIC_IP" "INTERNAL_IP" "MANAGEMENT_VIP" "NETWORK_DEVICE" "DB_LISTEN_IP" "POSTGRES_USER" "POSTGRES_PASSWORD" "POSTGRES_DB" "ADMIN_PASSWORD" "ADMIN_EMAIL" "COMPOSE_PROFILES" "DB_HOST" "DB_PORT" "CPGATEWAY_SECRET_KEY" "FEISHU_WEBHOOK_URL" "FEISHU_SECRET" "S3_ENDPOINT" "S3_ACCESS_KEY" "S3_SECRET_KEY" "S3_BUCKET" "S3_REGION" "S3_USE_SSL" "S3_UPLOAD_TIMEOUT_MINUTES" "MINIO_HOSTNAME" "CLAPI_HOSTNAME" "CAPTURE_UPLOAD_SECRET" "GRAFANA_ADMIN_PASSWORD" "DNS_UPSTREAM" "MINIO_ROOT_USER" "MINIO_ROOT_PASSWORD" "GRPC_AUTH_TOKEN" "GRPC_LISTEN" "TELEMETRY_LISTEN_IP" "REPO_BRANCH" "DEPLOY_SCRIPT_URL")

# 注入环境变量到 .env (如果当前 Shell 环境中有定义)
for var in "${vars[@]}"; do
    val="${!var:-}"
    if [ -n "$val" ]; then
        if grep -q "^${var}=" .env; then
            current_val=$(grep "^${var}=" .env | cut -d'=' -f2-)
            if [ "$val" != "$current_val" ]; then
                log "注入/覆盖环境变量: $var=********"
                # 转义 sed 分隔符
                escaped_val=$(echo "$val" | sed 's/[&/\]/\\&/g')
                sed -i "s|^${var}=.*|${var}=${escaped_val}|" .env
            fi
        else
            log "追加环境变量: $var=********"
            echo "${var}=${val}" >> .env
        fi
    fi
done

# 再次检查关键变量是否已配置
required_vars=("PUBLIC_IP" "INTERNAL_IP" "NETWORK_DEVICE" "MANAGEMENT_VIP" "ADMIN_EMAIL" "ADMIN_PASSWORD")
missing_vars=()
for var in "${required_vars[@]}"; do
    # 同时检查当前环境和 .env 文件
    val_in_env=$(grep "^${var}=" .env | cut -d'=' -f2- || echo "")
    if [[ -z "${!var:-}" && -z "$val_in_env" ]]; then
        missing_vars+=("$var")
    fi
done

if [ ${#missing_vars[@]} -ne 0 ]; then
    warn "缺少关键配置项: ${missing_vars[*]}"
    echo "请以 root 身份导出环境变量后再执行（Ubuntu 26.04 默认的 sudo-rs 会忽略 sudo -E），例如："
    echo "  sudo -i"
    echo "  export PUBLIC_IP=x.x.x.x ADMIN_PASSWORD=xxxx; curl -sSL ... | bash"
    echo "或者手动编辑 $DEPLOY_DIR/.env 文件。"
    exit 1
fi

# 启用 minio profile 时，校验 MinIO 必填参数
compose_profiles=$(grep "^COMPOSE_PROFILES=" .env | cut -d'=' -f2- || echo "")
compose_profiles="${COMPOSE_PROFILES:-$compose_profiles}"
if [[ "$compose_profiles" == *minio* ]]; then
    minio_missing=()
    for var in "MINIO_ROOT_PASSWORD" "S3_SECRET_KEY"; do
        val_in_env=$(grep "^${var}=" .env | cut -d'=' -f2- || echo "")
        if [[ -z "${!var:-}" && -z "$val_in_env" ]]; then
            minio_missing+=("$var")
        fi
    done
    if [ ${#minio_missing[@]} -ne 0 ]; then
        warn "启用了 minio profile，但缺少必填参数: ${minio_missing[*]}"
        exit 1
    fi
    # 内置 MinIO：S3_ENDPOINT 未配置时指向 MinIO 内部域名（未启用 minio 时保持为空，即 legacy 本地镜像模式）
    if [ -z "$(grep '^S3_ENDPOINT=' .env | cut -d'=' -f2- || true)" ]; then
        minio_host=$(grep '^MINIO_HOSTNAME=' .env | cut -d'=' -f2- || true)
        sed -i '/^S3_ENDPOINT=/d' .env
        echo "S3_ENDPOINT=${minio_host:-images.cloudland.internal}:9000" >> .env
        log "已按内置 MinIO 设置 S3_ENDPOINT=${minio_host:-images.cloudland.internal}:9000"
    fi
fi

# cland-go 强制要求 gRPC 共享令牌：未提供时自动生成（重复部署沿用 .env 中已有的值）
if [ -z "$(grep '^GRPC_AUTH_TOKEN=' .env | cut -d'=' -f2- || true)" ]; then
    sed -i '/^GRPC_AUTH_TOKEN=/d' .env
    echo "GRPC_AUTH_TOKEN=$(openssl rand -hex 32)" >> .env
    log "已生成 GRPC_AUTH_TOKEN 并写入 .env"
fi

# 从虚拟机创建镜像时上传凭证的签名密钥：为空时 clapi 拒绝所有上传，同样自动生成
if [ -z "$(grep '^CAPTURE_UPLOAD_SECRET=' .env | cut -d'=' -f2- || true)" ]; then
    sed -i '/^CAPTURE_UPLOAD_SECRET=/d' .env
    echo "CAPTURE_UPLOAD_SECRET=$(openssl rand -hex 32)" >> .env
    log "已生成 CAPTURE_UPLOAD_SECRET 并写入 .env"
fi

# 显示当前使用的关键配置摘要 (脱敏)
echo -e "\n--- 部署配置摘要 ---"
for var in "${required_vars[@]}"; do
    val=$(grep "^${var}=" .env | cut -d'=' -f2-)
    if [[ "$var" == *"PASSWORD"* ]]; then
        echo "  $var: ********"
    else
        echo "  $var: $val"
    fi
done
echo -e "-------------------\n"

# ============ 2. 网络迁移 (可选/检查) ============
log "2/5 - 迁移网络管理 (networkd -> NetworkManager)"
net_dev=$(grep "^NETWORK_DEVICE=" .env | cut -d'=' -f2- || echo "")
net_dev="${NETWORK_DEVICE:-$net_dev}"
if [ -f "scripts/switch_bond_to_nm.sh" ] && [[ "$net_dev" == bond* ]]; then
    bash scripts/switch_bond_to_nm.sh bond0 || warn "bond0 迁移跳过或已完成"
    bash scripts/switch_bond_to_nm.sh bond1 || warn "bond1 迁移跳过或已完成"
else
    log "网络设备为 $net_dev，非 bond 接口，跳过 bond 迁移"
fi

# ============ 3. 准备凭证 ============
log "3/5 - 生成 SSH 密钥凭证"
id cland &>/dev/null || useradd -m -s /bin/bash cland
chown cland:cland /home/cland
mkdir -p "$CLOUDLAND_DIR/deploy/.ssh"
if [ ! -f "$CLOUDLAND_DIR/deploy/.ssh/cland.key" ]; then
    ssh-keygen -t rsa -f "$CLOUDLAND_DIR/deploy/.ssh/cland.key" -N ""
    echo "SSH 密钥已生成."
else
    echo "SSH 密钥已存在，跳过生成."
fi
chown -R cland:cland "$CLOUDLAND_DIR/deploy/.ssh"
chmod 600 "$CLOUDLAND_DIR/deploy/.ssh/cland.key"

# ============ 4. 安装 Docker ============
log "4/5 - 检查 Docker 环境"
if ! command -v docker &>/dev/null || ! docker compose version &>/dev/null; then
    warn "未检测到 Docker 或 Docker Compose，开始自动安装..."
    curl -fsSL https://get.docker.com -o get-docker.sh
    sh get-docker.sh
    rm -f get-docker.sh
    systemctl enable --now docker
else
    echo "Docker 环境已就绪."
fi

# ============ 5. 启动服务与证书 ============
log "5/5 - 启动 CloudLand 控制面服务"
bash scripts/init-certs.sh

# 预初始化 dnsmasq 所需目录和占位文件
# dnsmasq 容器启动时要求 --hostsdir 和 --conf-dir 路径存在，否则会告警/空转
# cland 容器通过 docker-compose dns: 字段独立指向本机 dnsmasq，无需改宿主 resolv.conf
DNS_UPSTREAM_VAL=$(grep '^DNS_UPSTREAM=' .env | cut -d'=' -f2- || echo "")
mkdir -p ../dns/hosts ../dns/conf.d
[ -f ../dns/hosts/hyper-hosts ] || touch ../dns/hosts/hyper-hosts
# 注册控制节点自身 hostname 到 dnsmasq
# 同时注册 MinIO、clapi 的内部域名，计算节点经本机 dnsmasq 解析
CTRL_HOSTNAME=$(hostname)
INTERNAL_IP_VAL=$(grep '^INTERNAL_IP=' .env | cut -d'=' -f2-)
if [ -n "$CTRL_HOSTNAME" ] && [ -n "$INTERNAL_IP_VAL" ]; then
    for entry in "$CTRL_HOSTNAME" \
                 "$(grep '^MINIO_HOSTNAME=' .env | cut -d'=' -f2-)" \
                 "$(grep '^CLAPI_HOSTNAME=' .env | cut -d'=' -f2-)"; do
        if [ -n "$entry" ] && ! grep -qw "$entry" ../dns/hosts/hyper-hosts 2>/dev/null; then
            echo "$INTERNAL_IP_VAL $entry" >> ../dns/hosts/hyper-hosts
            log "已注册 DNS: $INTERNAL_IP_VAL $entry"
        fi
    done
fi
# 支持逗号分隔的多个上游地址，每个一行 server= (对齐 clapi ApplyDnsUpstream 实现)
# 这是 bootstrap 配置，clapi 启动后会根据 DB 中 DNS_UPSTREAM 设置覆写
if [ ! -f ../dns/conf.d/upstream.conf ]; then
    if [ -n "$DNS_UPSTREAM_VAL" ]; then
        printf '%s\n' "$DNS_UPSTREAM_VAL" | tr ',' '\n' | awk 'NF{print "server="$1}' \
            > ../dns/conf.d/upstream.conf
    else
        echo "server=8.8.8.8" > ../dns/conf.d/upstream.conf
    fi
fi

docker compose pull || warn "部分官方镜像拉取失败，尝试本地构建..."
docker compose up -d --build

log "✅ 控制面部署已完成！"
PUBLIC_IP=$(grep '^PUBLIC_IP=' .env | cut -d'=' -f2-)
echo "Web 访问地址: https://${PUBLIC_IP}"
ADMIN_EMAIL=$(grep '^ADMIN_EMAIL=' .env | cut -d'=' -f2-)
echo "默认用户名: ${ADMIN_EMAIL:-admin@cloudland.local}"
ADMIN_PASSWORD=$(grep '^ADMIN_PASSWORD=' .env | cut -d'=' -f2-)
echo "默认密码: ${ADMIN_PASSWORD:-passw0rd}"
echo "监控服务: http://${PUBLIC_IP}:9090/-/healthy"

echo "常用维护命令："
echo "  cd $DEPLOY_DIR && docker compose ps"
echo "  cd $DEPLOY_DIR && docker compose logs -f"
