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
        *) echo -e "\033[1;33m[WARN] 未验证的 Ubuntu 版本 ${VERSION_ID:-未知}，仅支持 24.04 / 26.04，继续执行可能失败\033[0m" ;;
    esac
else
    echo "错误: 无法识别操作系统。本脚本仅支持 Ubuntu 系统。"
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
# 通过 `bash -s < script.sh` / `curl … | bash` 这种管道执行时，BASH_SOURCE[0]
# 是空的，直接展开会被 set -u 拦截。用 :- 默认值兜底指到当前工作目录。
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$PWD/deploy-compute-node.sh}")" 2>/dev/null && pwd || echo "$PWD")"
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
NODE_ID="${NODE_ID:?错误: 必须设置 NODE_ID}"                                   # 计算节点编号（即控制面分配的 hostid）
ZONE_NAME="${ZONE_NAME:-zone0}"                       # 可用区名称
VIRT_TYPE="${VIRT_TYPE:-kvm-x86_64}"                  # 虚拟化类型
CLOUDLAND_DIR="${CLOUDLAND_DIR:-/opt/cloudland}"      # CloudLand 安装目录
DEPLOY_DIR="$CLOUDLAND_DIR/deploy/docker"

# ============ 可选配置 ============

# 其他计算节点列表（用于 /etc/hosts，格式: "IP HOSTNAME" 每行一条）
# 如只有本机则留空
OTHER_NODES=""
# 例如:
# OTHER_NODES="192.168.1.202 hyper02
# 192.168.1.203 hyper03"

# ============ 函数定义 ============
log() { echo -e "\n\033[1;32m[$(date '+%H:%M:%S')] $1\033[0m"; }
warn() { echo -e "\033[1;33m[WARN] $1\033[0m"; }

# cland-go 共享令牌，由控制面生成的 deploy_command 传入；缺失时本节点无法向 cland-go 注册
GRPC_AUTH_TOKEN="${GRPC_AUTH_TOKEN:-}"
if [ -z "$GRPC_AUTH_TOKEN" ]; then
    warn "未设置 GRPC_AUTH_TOKEN，cloudlet-go 将被 cland-go 拒绝；请使用控制面生成的 deploy_command，或在 compute.env 中配置与控制面 .env 相同的值"
fi

# ============ 1. base 角色：系统基础配置 ============
log "1/16 - 系统基础配置 (base role)"

# 设置 hostname
hostnamectl set-hostname "$HOSTNAME"

# 生成 /etc/hosts
# 本机 hostname 必须解析到本机 IP（cloudlet-go 上报健康信息时使用）
OWN_IP=$(ip addr show "$NETWORK_DEVICE" 2>/dev/null | grep 'inet ' | awk '{print $2}' | cut -d/ -f1 | head -1)
if [ -z "$OWN_IP" ]; then
    OWN_IP=$(hostname -I | awk '{print $1}')
fi
if [ -z "$OWN_IP" ]; then
    log "ERROR: 无法获取本机 IP，请检查 NETWORK_DEVICE=$NETWORK_DEVICE"
    exit 1
fi
cat > /etc/hosts <<EOF
127.0.0.1   localhost localhost.localdomain localhost4 localhost4.localdomain4
::1         localhost localhost.localdomain localhost6 localhost6.localdomain6

# CloudLand nodes
$OWN_IP $HOSTNAME
EOF

# 追加其他节点
if [ -n "$OTHER_NODES" ]; then
    echo "$OTHER_NODES" >> /etc/hosts
fi

# 将 DNS 指向控制节点的 dnsmasq，兼容 systemd-resolved
if systemctl is-active --quiet systemd-resolved 2>/dev/null; then
    mkdir -p /etc/systemd/resolved.conf.d/
    cat > /etc/systemd/resolved.conf.d/cloudland.conf <<EOF
[Resolve]
DNS=$CONTROLLER_IP
DNSStubListener=no
EOF
    systemctl restart systemd-resolved
    resolvectl flush-caches 2>/dev/null || true
    # 将 /etc/resolv.conf 指向 resolved 的非 stub 文件（包含真实 DNS 地址）
    ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
else
    echo "nameserver $CONTROLLER_IP" > /etc/resolv.conf
fi

# 文件描述符上限
cat > /etc/security/limits.d/cloudland.conf <<EOF
root      soft    nofile          102400
root      hard    nofile          112640
*         soft    nofile          102400
*         hard    nofile          112640
EOF

# NTP 时间同步（Ubuntu 24.04 的 ntp 只是指向 ntpsec 的过渡包，26.04 已移除 ntp，统一使用 ntpsec）
apt-get update -qq
apt-get install -y ntpsec
systemctl enable --now ntpsec || true

# 删除 unattended-upgrade
apt-get remove -y unattended-upgrades 2>/dev/null || true

# 创建 cland 用户并确保 home 目录权限正确（SSH 公钥认证要求 home 目录属主为用户自己）
id cland &>/dev/null || useradd -m -s /bin/bash cland
CLAND_HOME=$(getent passwd cland | cut -d: -f6)
chown cland:cland "$CLAND_HOME"
echo 'cland ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/cland

# Ubuntu 26.04 默认 sudo 为 sudo-rs，会忽略 sudo -E；cloudlet-go 与 scripts/kvm 依赖 -E 传递
# NODE_ID/TRACEPARENT 等环境变量（丢失后回调主机 ID 为空，clapi 返回 400），切回经典 sudo
if readlink -f /usr/bin/sudo | grep -q 'sudo-rs\|cargo'; then
    # 精简镜像可能只装了 sudo-rs，经典 sudo 由 sudo 包提供（/usr/bin/sudo.ws）
    [ -x /usr/bin/sudo.ws ] || apt-get install -y sudo
    update-alternatives --set sudo /usr/bin/sudo.ws
    log "已将 sudo 从 sudo-rs 切换为经典 sudo（sudo.ws）"
fi

# 屏蔽 UFW
systemctl mask ufw 2>/dev/null || true
systemctl stop ufw 2>/dev/null || true

# ============ 2. base 防火墙规则 ============
log "2/16 - 配置 iptables 基础规则"

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
    # 重启 Docker 时秒退的容器（如 dnsmasq，exit 0）不会被 unless-stopped 策略拉起，显式恢复控制面容器
    (cd "$DEPLOY_DIR" && docker compose up -d) || warn "控制面容器恢复失败，请手动执行: cd $DEPLOY_DIR && docker compose up -d"
fi

# 持久化 iptables：24.04/26.04 未安装 ifupdown，/etc/network/if-pre-up.d 钩子不会执行。
# 改用 systemd 在网络、Docker、libvirt、cloudlet-go 启动前恢复基础规则，避免开机到 cloudlet 首次心跳
# （report_rc.sh 的 sync_instance 会再次恢复并同步实例规则）之间主机防火墙处于未生效状态
rm -f /etc/network/if-pre-up.d/iptablesload /etc/network/if-post-down.d/iptablessave
cat > /etc/systemd/system/cloudland-iptables.service <<'UNIT'
[Unit]
Description=Restore CloudLand base iptables rules
DefaultDependencies=no
After=local-fs.target
Before=network-pre.target docker.service libvirtd.service cloudlet-go.service shutdown.target
Wants=network-pre.target
Conflicts=shutdown.target
ConditionFileNotEmpty=/etc/iptables.rules

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/iptables-restore /etc/iptables.rules

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable cloudland-iptables.service

# ============ 3. 安装依赖包 ============
log "3/16 - 安装依赖包"

apt-get install -y jq wget mkisofs network-manager net-tools python3-pip

apt-get install -y qemu-system-x86 qemu-utils bridge-utils ipcalc ipset \
    keepalived haproxy iputils-arping libvirt-daemon libvirt-daemon-system \
    libvirt-daemon-system-systemd libvirt-clients dnsmasq-base dnsmasq-utils \
    conntrack cloud-utils socat
# Local storage pools: LVM, RAID1, XFS, and rsync keeping sparse files when shut-off instances migrate
apt-get install -y lvm2 mdadm xfsprogs rsync libxml2-utils
# 负载均衡的 haproxy 由 create_haproxy_conf.sh 在路由器 netns 内按实例启动，不需要系统自带的服务
systemctl disable --now haproxy 2>/dev/null || true

# VPN gateways: strongSwan (IKEv2), WireGuard and FRR (BGP) run per gateway inside the router netns,
# started by the vpn scripts; the system services would grab /etc/swanctl, /run/frr and the IKE ports
apt-get install -y strongswan-swanctl strongswan-charon wireguard-tools frr
systemctl disable --now strongswan-starter strongswan ipsec frr 2>/dev/null || true
# Ubuntu confines charon, swanctl and bgpd with AppArmor to their packaged paths; allow the per-gateway
# directories (charon's pid dir is compiled in, the scripts bind-mount a private one over /run)
mkdir -p /etc/apparmor.d/local
cat > /etc/apparmor.d/local/usr.lib.ipsec.charon <<'EOF'
# CloudLand VPN gateways: one charon per VPC router netns, config and logs under the router cache dir
  /opt/cloudland/cache/router/router-*/vpn-*/ r,
  /opt/cloudland/cache/router/router-*/vpn-*/** rwk,
EOF
cat > /etc/apparmor.d/local/usr.sbin.swanctl <<'EOF'
# CloudLand VPN gateways: per-gateway swanctl.conf and vici socket
  /opt/cloudland/cache/router/router-*/vpn-*/ r,
  /opt/cloudland/cache/router/router-*/vpn-*/** rw,
EOF
for daemon in bgpd staticd; do
cat > /etc/apparmor.d/local/$daemon <<'EOF'
# CloudLand VPN gateways: one FRR pathspace per gateway (-N vpn-<id>)
  @{run}/frr/vpn-*/ rw,
  @{run}/frr/vpn-*/** rwk,
  /etc/frr/vpn-*/ r,
  /etc/frr/vpn-*/* rw,
  # thread naming, denied by the stock profile (harmless, only log noise)
  owner @{PROC}/@{pid}/task/@{tid}/comm rw,
EOF
done
cat > /etc/apparmor.d/local/wg <<'EOF'
# CloudLand VPN gateways: wg reads the per-gateway configuration and key
  /opt/cloudland/cache/router/router-*/vpn-*/ r,
  /opt/cloudland/cache/router/router-*/vpn-*/** r,
EOF
if command -v apparmor_parser >/dev/null 2>&1 && [ -d /sys/kernel/security/apparmor ]; then
    for profile in usr.lib.ipsec.charon usr.sbin.swanctl bgpd staticd wg; do
        [ -f /etc/apparmor.d/$profile ] && apparmor_parser -r /etc/apparmor.d/$profile || true
    done
fi

# Docker（用于跑 libvirt-exporter 和 promtail-agent 监控容器）
if ! command -v docker &>/dev/null; then
    log "安装 Docker (docker.io)..."
    apt-get install -y docker.io
fi
systemctl enable --now docker

# pyparsing 已不再需要（仅 backup/ 下废弃脚本使用），跳过安装

# ============ 4. SSH 配置 ============
log "4/16 - 配置 SSH 免密"

mkdir -p $CLAND_HOME/.ssh /root/.ssh "$CLOUDLAND_DIR/deploy/.ssh"
touch $CLAND_HOME/.ssh/authorized_keys /root/.ssh/authorized_keys

SSH_KEYS_INSTALLED=""

# 优先从 deploy 命令传入的 CLAND_PUBKEY env 装公钥（解决鸡生蛋问题）
# 控制面 clapi 的 POST /api/v1/hypers 会把 cland.key.pub 内容嵌入到 deploy_command
# 里。第一次部署时本地肯定没有 cland.key/cland.key.pub，本地搜索会失败；需要在
# cloudlet-go 启动前把公钥写到 cland 用户的 authorized_keys 里，以便控制面通过
# SSH 访问计算节点执行脚本操作。
if [ -n "${CLAND_PUBKEY:-}" ]; then
    grep -qF "$CLAND_PUBKEY" $CLAND_HOME/.ssh/authorized_keys \
        || printf '%s\n' "$CLAND_PUBKEY" >> $CLAND_HOME/.ssh/authorized_keys
    grep -qF "$CLAND_PUBKEY" /root/.ssh/authorized_keys \
        || printf '%s\n' "$CLAND_PUBKEY" >> /root/.ssh/authorized_keys
    log "已通过 CLAND_PUBKEY env 安装控制面公钥到 authorized_keys"
fi

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
        grep -qF "$LOCAL_PUB" $CLAND_HOME/.ssh/authorized_keys 2>/dev/null \
            || printf '%s\n' "$LOCAL_PUB" >> $CLAND_HOME/.ssh/authorized_keys
        grep -qF "$LOCAL_PUB" /root/.ssh/authorized_keys 2>/dev/null \
            || printf '%s\n' "$LOCAL_PUB" >> /root/.ssh/authorized_keys
        # 私钥写入所有需要的位置 (如果是搜索到目标路径本身，则跳过拷贝操作，避免 cp 报错)
        TARGET_SSH_DIR="$CLOUDLAND_DIR/deploy/.ssh"
        if [ "$(realpath "$p")" != "$(realpath "$TARGET_SSH_DIR")" ]; then
            cp "$p/cland.key" "$TARGET_SSH_DIR/cland.key"
            cp "$p/cland.key.pub" "$TARGET_SSH_DIR/cland.key.pub"
        fi
        cp "$p/cland.key" /root/.ssh/id_rsa
        chmod 600 /root/.ssh/id_rsa "$TARGET_SSH_DIR/cland.key"
        log "从本地文件获取 SSH 密钥: $p"
        SSH_KEYS_INSTALLED="local"
        break
    fi
done

if [ -z "$SSH_KEYS_INSTALLED" ]; then
    log "本地未找到 SSH 密钥，将在节点注册时从控制节点获取"
fi

chown -R cland:cland $CLAND_HOME/.ssh "$CLOUDLAND_DIR/deploy/.ssh"
chmod 700 $CLAND_HOME/.ssh /root/.ssh
chmod 600 $CLAND_HOME/.ssh/authorized_keys 2>/dev/null || true
chmod 600 /root/.ssh/authorized_keys 2>/dev/null || true

# ============ 5. 编译安装 cloudlet-go ============
log "5/16 - 编译安装 cloudlet-go 二进制"

# 安装 Go（未安装或低于 api/go.mod 要求的版本时）
GO_VERSION="1.26.0"
export PATH=/usr/local/go/bin:$PATH
CURRENT_GO=$(go env GOVERSION 2>/dev/null | sed 's/^go//' || true)
if [ -z "$CURRENT_GO" ] || [ "$(printf '%s\n%s\n' "$GO_VERSION" "$CURRENT_GO" | sort -V | head -1)" != "$GO_VERSION" ]; then
    log "安装 Go $GO_VERSION（当前: ${CURRENT_GO:-未安装}）..."
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm -f /tmp/go.tar.gz
    echo 'export PATH=/usr/local/go/bin:$PATH' > /etc/profile.d/golang.sh
fi

if [ ! -d "$CLOUDLAND_DIR/api" ]; then
    log "未检测到源码 (缺少 api 目录)，开始自动拉取..."
    git clone -b "${REPO_BRANCH:-staging}" "${REPO_URL:-https://github.com/threen134/cloudland.git}" /tmp/cloudland
    cp -r /tmp/cloudland/* "$CLOUDLAND_DIR/"
    rm -rf /tmp/cloudland
fi

# 编译前先停止正在运行的服务，避免覆盖二进制时 "Text file busy"
systemctl stop cloudlet-go 2>/dev/null || true

cd "$CLOUDLAND_DIR/api"
make cloudlet
mkdir -p "$CLOUDLAND_DIR/bin"
cp -f cloudlet-go "$CLOUDLAND_DIR/bin/cloudlet-go"

# ============ 6. 创建目录结构 ============
log "6/16 - 创建目录结构"

mkdir -p "$CLOUDLAND_DIR"/{log,run,cache} "$CLOUDLAND_DIR/run/async_job"
mkdir -p "$CLOUDLAND_DIR/cache"/{backup,image,instance,meta,router,volume,dnsmasq,xml,qemu_agent}
# -xdev: never walk into mounted storage pools (instance disks there belong to libvirt while they run)
find "$CLOUDLAND_DIR" -xdev -exec chown cland:cland {} +

# ============ 7. 创建 backend 软链接 ============
log "7/16 - 创建 scripts/backend → kvm 软链接"

ln -sfn "$CLOUDLAND_DIR/scripts/kvm" "$CLOUDLAND_DIR/scripts/backend"
chown -h cland:cland "$CLOUDLAND_DIR/scripts/backend"

# 设置脚本可执行权限
chmod -R 755 "$CLOUDLAND_DIR/scripts/kvm"

# ============ 8. 配置 cloudrc.local ============
# 与 Ansible hyper/templates/cloudrc.local.kvm-x86_64.j2 完全对齐
log "8/16 - 配置 cloudrc.local"

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
EOF
chown cland:cland "$CLOUDLAND_DIR/scripts/cloudrc.local"

# ============ 9. KVM 嵌套虚拟化 ============
log "9/16 - 配置 KVM 嵌套虚拟化"

cat > /etc/modprobe.d/kvm-nested.conf <<EOF
options kvm-intel nested=1
options kvm-intel enable_shadow_vmcs=1
options kvm-intel enable_apicv=1
options kvm-intel ept=1
EOF

# ============ 10. 网络管理切换到 NetworkManager ============
# 必须在启动 cloudlet-go 之前完成：cloudlet-go 注册后控制面会立即下发 system router、br<vlan>/v-<vlan> 的创建，
# v-<vlan> 建在 bond 上，之后再切换 bond 会与网桥创建竞争并可能丢失已建好的 VLAN 接口
log "10/16 - 切换 bond 到 NetworkManager，netplan 渲染器改为 NetworkManager 并屏蔽 networkd"

# 与控制节点一致：把承载 VXLAN/VLAN 的 bond 从 systemd-networkd 迁移为 NM 连接（bondX-nm，存为 /etc/netplan/90-NM-*.yaml）；
# 激活连接时 bond 会短暂中断，通过该 bond 的 SSH 登录执行本脚本时建议用 systemd-run/nohup 后台运行
SWITCH_BOND_SCRIPT="$DEPLOY_DIR/scripts/switch_bond_to_nm.sh"
for dev in $(printf '%s\n' "$NETWORK_DEVICE" "$VLAN_DEVICE" "$PRIVATE_VLAN_DEVICE" | awk '!seen[$0]++'); do
    [[ "$dev" == bond* ]] || continue
    if [ -f "$SWITCH_BOND_SCRIPT" ]; then
        bash "$SWITCH_BOND_SCRIPT" "$dev" || warn "$dev 切换到 NetworkManager 失败，请检查（回滚: bash $SWITCH_BOND_SCRIPT --rollback $dev）"
    else
        warn "未找到 $SWITCH_BOND_SCRIPT，跳过 $dev 切换"
    fi
done

# 下载 yq 工具
YQ=/tmp/yq
if [ ! -f "$YQ" ]; then
    wget -q https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64 -O "$YQ"
    chmod +x "$YQ"
fi

# 检查并切换 netplan 渲染器（未迁移的接口重启后也由 NM 接管）
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

# ============ 11. 配置并启动服务 ============
log "11/16 - 配置并启动 cloudlet-go/libvirtd/NetworkManager"

mkdir -p /etc/sysconfig

# --- cloudlet-go 环境变量 ---
cat > /etc/sysconfig/cloudlet <<EOF
CLAND_ENDPOINT=${CONTROLLER_IP}:5006
# 计算节点编号（控制面分配的 hostid）；cloudlet-go 执行脚本时注入，脚本在回调中用它标识本节点
NODE_ID=$NODE_ID
ZONE_NAME=$ZONE_NAME
VIRT_TYPE=$VIRT_TYPE
GRPC_AUTH_TOKEN=${GRPC_AUTH_TOKEN:-}
# 本节点命令并发数，默认 1：与 C++ cloudlet 一样按到达顺序串行执行
#CLOUDLET_CONCURRENCY=1
# 链路追踪：导出到控制节点 OTel Collector（与 CLAND_ENDPOINT 同一地址）
OTEL_EXPORTER_OTLP_ENDPOINT=http://${CONTROLLER_IP}:4317
OTEL_RESOURCE_ATTRIBUTES=cloudland.node_id=$NODE_ID,cloudland.zone=$ZONE_NAME
EOF
chmod 600 /etc/sysconfig/cloudlet

# --- cloudlet-go service ---
cat > /lib/systemd/system/cloudlet-go.service <<EOF
[Unit]
Description=Cloudlet service (Go gRPC)
After=network.target

[Service]
Type=simple
User=cland
EnvironmentFile=/etc/sysconfig/cloudlet
ExecStart=$CLOUDLAND_DIR/bin/cloudlet-go
KillMode=process
# cloudlet-go 仅在节点被控制面删除时正常退出，此时不应被拉起
Restart=on-failure
RestartSec=5
OOMScoreAdjust=-1000

[Install]
WantedBy=multi-user.target
EOF

# 停止旧版服务（如果存在）
systemctl stop cloudlet scid 2>/dev/null || true
systemctl disable cloudlet scid 2>/dev/null || true

# 启动服务
systemctl daemon-reload
systemctl enable --now cloudlet-go
systemctl enable --now libvirtd
systemctl enable --now NetworkManager

# 删除默认 libvirt 网络
virsh net-destroy default 2>/dev/null || true
virsh net-undefine default 2>/dev/null || true

# ============ 12. 内核参数 ============
log "12/16 - 配置内核参数"

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
log "13/16 - 部署监控 agent"

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
log "14/16 - 部署南北向流量计量"

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

# ============ 15. 获取 cland 私钥并验证 cloudlet-go 连接 ============
log "15/16 - 获取 cland 私钥并验证 cloudlet-go 与控制面连接"

# 首次部署本地没有 cland 私钥（create_portmap.sh、迁移的 qemu+ssh 需要）：
# 通过 cloudlet-go node-add 调用 cland 的 NodeAdd，控制面向 clapi 校验 hostid 后返回密钥对
if [ -z "${SSH_KEYS_INSTALLED:-}" ]; then
    REGISTER_RESP_FILE=$(mktemp)
    for i in 1 2 3 4 5; do
        if CLAND_ENDPOINT="${CONTROLLER_IP}:5006" NODE_ID="$NODE_ID" HOSTNAME="$HOSTNAME" \
            GRPC_AUTH_TOKEN="${GRPC_AUTH_TOKEN:-}" \
            "$CLOUDLAND_DIR/bin/cloudlet-go" node-add > "$REGISTER_RESP_FILE"; then
            break
        fi
        warn "node-add 第 $i 次失败，3 秒后重试"
        : > "$REGISTER_RESP_FILE"
        sleep 3
    done

    if jq -e '.status == "ok"' "$REGISTER_RESP_FILE" &>/dev/null; then
        PRIV_KEY=$(jq -r '.private_key // empty' "$REGISTER_RESP_FILE")
        PUB_KEY=$(jq -r '.public_key // empty' "$REGISTER_RESP_FILE")
        if [ -n "$PRIV_KEY" ] && [ -n "$PUB_KEY" ]; then
            printf '%s\n' "$PRIV_KEY" > "$CLOUDLAND_DIR/deploy/.ssh/cland.key"
            printf '%s\n' "$PUB_KEY"  > "$CLOUDLAND_DIR/deploy/.ssh/cland.key.pub"
            grep -qF "$PUB_KEY" $CLAND_HOME/.ssh/authorized_keys \
                || printf '%s\n' "$PUB_KEY" >> $CLAND_HOME/.ssh/authorized_keys
            grep -qF "$PUB_KEY" /root/.ssh/authorized_keys \
                || printf '%s\n' "$PUB_KEY" >> /root/.ssh/authorized_keys
            cp "$CLOUDLAND_DIR/deploy/.ssh/cland.key" /root/.ssh/id_rsa
            cp "$CLOUDLAND_DIR/deploy/.ssh/cland.key" $CLAND_HOME/.ssh/cland.key
            chown -R cland:cland $CLAND_HOME/.ssh "$CLOUDLAND_DIR/deploy/.ssh"
            chmod 600 $CLAND_HOME/.ssh/cland.key /root/.ssh/id_rsa \
                      "$CLOUDLAND_DIR/deploy/.ssh/cland.key"
            log "已通过 node-add 从控制面获取 cland 密钥"
            SSH_KEYS_INSTALLED="api"
        fi
    fi
    rm -f "$REGISTER_RESP_FILE"
fi

# cloudlet-go 启动后自动通过 gRPC 连接 cland-go 并注册
# 等待 cloudlet-go 进程启动并检查日志确认连接成功
RETRY=0
MAX_RETRY=15
while [ $RETRY -lt $MAX_RETRY ]; do
    if systemctl is-active --quiet cloudlet-go; then
        # 检查日志中是否有注册成功的信息
        if journalctl -u cloudlet-go --no-pager -n 20 2>/dev/null | grep -q "Registered with cland successfully"; then
            log "cloudlet-go 已成功连接到控制面"
            break
        fi
    fi
    RETRY=$((RETRY + 1))
    if [ $RETRY -ge $MAX_RETRY ]; then
        warn "cloudlet-go 尚未确认连接成功（可能需要等待控制面就绪），请手动检查: journalctl -u cloudlet-go -f"
        break
    fi
    sleep 2
done

if [ -z "${SSH_KEYS_INSTALLED:-}" ]; then
    warn "未能获取 cland 私钥，portmap 与虚拟机迁移不可用；控制面就绪后可重新执行本脚本"
fi

# ============ 16. 部署监控代理 Promtail (Trace 日志回传) ============
log "16/16 - 部署 Promtail 日志回传代理"

mkdir -p /var/lib/promtail
cat > /opt/cloudland/promtail-client.yaml <<EOF
server:
  http_listen_port: 9080
  grpc_listen_port: 0

positions:
  filename: /var/lib/promtail/positions.yaml

clients:
  - url: http://${CONTROLLER_IP}:3100/loki/api/v1/push

scrape_configs:
# 脚本日志（log_debug 等），带 trace=<trace_id>
- job_name: cloudland-scripts
  static_configs:
  - targets:
      - localhost
    labels:
      job: local-compute-logs
      host: $(hostname)
      node_id: "${NODE_ID}"
      __path__: /opt/cloudland/log/*.log
# cloudlet-go 的 systemd 日志，带 [trace=<trace_id>] 前缀
- job_name: cloudlet-journal
  journal:
    max_age: 12h
    labels:
      job: cloudlet
      host: $(hostname)
      node_id: "${NODE_ID}"
  relabel_configs:
    - source_labels: ['__journal__systemd_unit']
      regex: 'cloudlet-go\\.service'
      action: keep
EOF

if command -v docker &>/dev/null; then
    docker rm -f promtail-agent 2>/dev/null || true
    docker run -d --name promtail-agent \
      --network host \
      --restart always \
      -v /opt/cloudland/log:/opt/cloudland/log:ro \
      -v /var/lib/promtail:/var/lib/promtail \
      -v /var/log/journal:/var/log/journal:ro \
      -v /run/log/journal:/run/log/journal:ro \
      -v /etc/machine-id:/etc/machine-id:ro \
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
