#!/bin/bash
###############################################################################
# switch_bond_to_nm.sh
#
# 将指定的 bond 接口从 systemd-networkd 切换到 NetworkManager 管理
# 前提条件：系统使用 netplan + systemd-networkd, bond 为 802.3ad LACP 模式
#
# 用法：
#   ./switch_bond_to_nm.sh <bond_name>
#   ./switch_bond_to_nm.sh bond0
#   ./switch_bond_to_nm.sh --rollback <bond_name>
#
# 说明：
#   - 脚本必须以 root 执行
#   - 只切换指定的 bond，其他 bond 仍由 networkd 管理
#   - 脚本通过读取当前运行时配置（ip 命令）自动获取 IP/路由/bond参数
#   - 支持 --rollback 回滚到 networkd
###############################################################################

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

BACKUP_DIR="/root/.bond_to_nm_backup"

usage() {
    echo "用法: $0 [--rollback] <bond_name>"
    echo ""
    echo "  $0 bond0            将 bond0 从 networkd 切换到 NetworkManager"
    echo "  $0 --rollback bond0 回滚 bond0 到 networkd 管理"
    exit 1
}

###############################################################################
# 前置检查
###############################################################################
preflight_check() {
    local bond="$1"

    if [[ $EUID -ne 0 ]]; then
        log_error "必须以 root 运行"
        exit 1
    fi

    # 检查 bond 接口是否存在
    if ! ip link show "$bond" &>/dev/null; then
        log_info "接口 $bond 不存在，跳过迁移"
        exit 0
    fi

    # 检查是否为 bond 类型
    if [[ ! -f "/proc/net/bonding/$bond" ]]; then
        log_error "$bond 不是一个 bond 接口"
        exit 1
    fi

    # 检查是否已经被 NM 管理
    if command -v nmcli &>/dev/null; then
        local nm_state
        nm_state=$(nmcli -t -f DEVICE,STATE device status 2>/dev/null | grep "^${bond}:" | cut -d: -f2 || true)
        if [[ "$nm_state" == "connected" ]]; then
            log_warn "$bond 已经由 NetworkManager 管理，无需切换"
            exit 0
        fi
    fi
}

###############################################################################
# 收集当前 bond 的运行时配置
###############################################################################
collect_bond_info() {
    local bond="$1"

    log_info "正在收集 $bond 的当前配置..."

    # 获取 slave 接口
    BOND_SLAVES=()
    while IFS= read -r line; do
        BOND_SLAVES+=("$line")
    done < <(grep "Slave Interface:" "/proc/net/bonding/$bond" | awk '{print $3}')

    if [[ ${#BOND_SLAVES[@]} -eq 0 ]]; then
        log_error "未找到 $bond 的 slave 接口"
        exit 1
    fi
    log_info "  Slave 接口: ${BOND_SLAVES[*]}"

    # 获取 bond 模式
    BOND_MODE_LINE=$(grep "Bonding Mode:" "/proc/net/bonding/$bond")
    case "$BOND_MODE_LINE" in
        *"802.3ad"*)           BOND_MODE="802.3ad" ;;
        *"balance-rr"*)        BOND_MODE="balance-rr" ;;
        *"active-backup"*)     BOND_MODE="active-backup" ;;
        *"balance-xor"*)       BOND_MODE="balance-xor" ;;
        *"broadcast"*)         BOND_MODE="broadcast" ;;
        *"balance-tlb"*)       BOND_MODE="balance-tlb" ;;
        *"balance-alb"*)       BOND_MODE="balance-alb" ;;
        *)                     BOND_MODE="802.3ad" ;;
    esac
    log_info "  Bond 模式: $BOND_MODE"

    # 获取 bond 参数
    BOND_MIIMON=$(grep "MII Polling Interval" "/proc/net/bonding/$bond" | awk '{print $NF}')
    BOND_UPDELAY=$(grep "Up Delay" "/proc/net/bonding/$bond" | awk '{print $NF}')
    BOND_DOWNDELAY=$(grep "Down Delay" "/proc/net/bonding/$bond" | head -1 | awk '{print $NF}')

    # xmit_hash_policy
    BOND_XMIT_HASH=""
    local xmit_line
    xmit_line=$(grep "Transmit Hash Policy:" "/proc/net/bonding/$bond" || true)
    if [[ -n "$xmit_line" ]]; then
        BOND_XMIT_HASH=$(echo "$xmit_line" | sed 's/.*: //' | awk '{print $1}')
    fi
    [[ -n "$BOND_XMIT_HASH" ]] && log_info "  Transmit Hash: $BOND_XMIT_HASH"

    # LACP rate
    BOND_LACP_RATE=""
    local lacp_line
    lacp_line=$(grep "LACP rate:" "/proc/net/bonding/$bond" || true)
    if [[ -n "$lacp_line" ]]; then
        BOND_LACP_RATE=$(echo "$lacp_line" | awk '{print $NF}')
    fi
    [[ -n "$BOND_LACP_RATE" ]] && log_info "  LACP rate: $BOND_LACP_RATE"

    # 获取 IPv4 地址
    BOND_ADDRESSES=()
    while IFS= read -r addr; do
        BOND_ADDRESSES+=("$addr")
    done < <(ip -4 addr show "$bond" | grep "inet " | awk '{print $2}')

    if [[ ${#BOND_ADDRESSES[@]} -eq 0 ]]; then
        log_warn "  $bond 没有 IPv4 地址"
    else
        log_info "  IP 地址: ${BOND_ADDRESSES[*]}"
    fi

    # 获取 DNS（从 systemd-resolved 或 resolv.conf）
    BOND_DNS=""
    if [[ -f "/run/systemd/network/10-netplan-${bond}.network" ]]; then
        BOND_DNS=$(grep "^DNS=" "/run/systemd/network/10-netplan-${bond}.network" | cut -d= -f2 | tr '\n' ',' | sed 's/,$//')
    fi
    if [[ -z "$BOND_DNS" ]]; then
        BOND_DNS=$(grep "^nameserver" /etc/resolv.conf 2>/dev/null | awk '{print $2}' | head -2 | tr '\n' ',' | sed 's/,$//')
    fi
    [[ -n "$BOND_DNS" ]] && log_info "  DNS: $BOND_DNS"

    # 获取路由
    BOND_ROUTES=()
    while IFS= read -r route; do
        [[ -n "$route" ]] && BOND_ROUTES+=("$route")
    done < <(ip route show dev "$bond" | grep "via" | grep -v "default" | awk '{print $1 " " $3}')

    # 检查默认路由
    BOND_DEFAULT_GW=""
    local default_route
    default_route=$(ip route show default dev "$bond" 2>/dev/null | awk '{print $3}' || true)
    if [[ -n "$default_route" ]]; then
        BOND_DEFAULT_GW="$default_route"
        log_info "  默认网关: $BOND_DEFAULT_GW"
    fi

    if [[ ${#BOND_ROUTES[@]} -gt 0 ]]; then
        log_info "  静态路由: ${#BOND_ROUTES[@]} 条"
        for r in "${BOND_ROUTES[@]}"; do
            log_info "    $r"
        done
    fi
}

###############################################################################
# 备份当前配置
###############################################################################
backup_config() {
    local bond="$1"

    log_info "正在备份配置到 $BACKUP_DIR/$bond/ ..."
    mkdir -p "$BACKUP_DIR/$bond"

    # 备份 netplan 文件
    cp -a /etc/netplan/*.yaml "$BACKUP_DIR/$bond/" 2>/dev/null || true
    cp -a /etc/netplan/*.yaml.bak "$BACKUP_DIR/$bond/" 2>/dev/null || true

    # 备份 networkd 运行时配置
    mkdir -p "$BACKUP_DIR/$bond/systemd-network"
    cp -a /run/systemd/network/10-netplan-${bond}.* "$BACKUP_DIR/$bond/systemd-network/" 2>/dev/null || true
    for slave in "${BOND_SLAVES[@]}"; do
        cp -a "/run/systemd/network/10-netplan-${slave}."* "$BACKUP_DIR/$bond/systemd-network/" 2>/dev/null || true
    done

    # 备份 NM 全局配置
    cp -a /usr/lib/NetworkManager/conf.d/10-globally-managed-devices.conf \
          "$BACKUP_DIR/$bond/10-globally-managed-devices.conf.bak" 2>/dev/null || true

    # 记录当前运行时状态
    ip addr show "$bond" > "$BACKUP_DIR/$bond/ip_addr.txt" 2>/dev/null || true
    ip route show dev "$bond" > "$BACKUP_DIR/$bond/ip_route.txt" 2>/dev/null || true
    cat "/proc/net/bonding/$bond" > "$BACKUP_DIR/$bond/bonding_status.txt" 2>/dev/null || true

    log_info "备份完成"
}

###############################################################################
# 安装 NetworkManager（如果未安装）
###############################################################################
install_nm() {
    if command -v nmcli &>/dev/null; then
        log_info "NetworkManager 已安装"
        return
    fi

    log_info "正在安装 NetworkManager ..."
    apt-get update -qq
    apt-get install -y -qq network-manager
    log_info "NetworkManager 安装完成"
}

###############################################################################
# 禁用 cloud-init 网络管理（如果存在）
###############################################################################
disable_cloud_init_network() {
    if [[ -f /etc/netplan/50-cloud-init.yaml ]]; then
        log_info "检测到 cloud-init 网络配置，正在禁用 ..."
        mkdir -p /etc/cloud/cloud.cfg.d
        echo "network: {config: disabled}" > /etc/cloud/cloud.cfg.d/99-disable-network-config.cfg
        mv /etc/netplan/50-cloud-init.yaml /etc/netplan/50-cloud-init.yaml.bak
        log_info "cloud-init 网络配置已禁用并备份"
    fi
}

###############################################################################
# 执行切换
###############################################################################
do_switch() {
    local bond="$1"

    # 1) 确保 NM 服务在运行
    log_info "正在启动 NetworkManager 服务 ..."
    systemctl enable NetworkManager --now 2>/dev/null || true
    sleep 1

    # 2) 修改 NM 全局配置，允许管理 bond/ethernet/bridge/vlan 设备（持久化）
    local nm_global_conf="/usr/lib/NetworkManager/conf.d/10-globally-managed-devices.conf"
    if [[ -f "$nm_global_conf" ]]; then
        local current_conf
        current_conf=$(cat "$nm_global_conf")
        if echo "$current_conf" | grep -q "unmanaged-devices=\*"; then
            # 检查是否已经有所需类型的例外
            local needs_update=false
            local required_types=(bond ethernet bridge vlan)
            for rtype in "${required_types[@]}"; do
                echo "$current_conf" | grep -q "except:type:${rtype}" || needs_update=true
            done

            if $needs_update; then
                log_info "正在修改 NM 全局配置，允许管理 bond/ethernet/bridge/vlan 设备 ..."
                # 在 unmanaged-devices 行追加缺失的类型例外
                local new_unmanaged
                new_unmanaged=$(echo "$current_conf" | grep "^unmanaged-devices=" | sed 's/$//')
                for rtype in "${required_types[@]}"; do
                    echo "$new_unmanaged" | grep -q "except:type:${rtype}" || new_unmanaged+=",except:type:${rtype}"
                done
                # 替换原文件中的 unmanaged-devices 行
                sed -i "s|^unmanaged-devices=.*|${new_unmanaged}|" "$nm_global_conf"
                log_info "NM 全局配置已更新（持久化）"
                # 重载 NM 配置
                nmcli general reload 2>/dev/null || true
                sleep 1
            fi
        fi
    fi

    # 3) 删除 networkd 对该 bond 的运行时配置
    log_info "正在移除 systemd-networkd 对 $bond 的配置 ..."
    rm -f "/run/systemd/network/10-netplan-${bond}.netdev"
    rm -f "/run/systemd/network/10-netplan-${bond}.network"
    for slave in "${BOND_SLAVES[@]}"; do
        rm -f "/run/systemd/network/10-netplan-${slave}.network"
        # 保留 .link 文件（负责接口重命名和 MAC 匹配，不影响管理归属）
    done
    systemctl reload systemd-networkd || true
    sleep 1

    # 4) 让 NM 管理这些设备
    log_info "正在让 NetworkManager 接管设备 ..."
    nmcli device set "$bond" managed yes 2>/dev/null || true
    for slave in "${BOND_SLAVES[@]}"; do
        nmcli device set "$slave" managed yes 2>/dev/null || true
    done
    sleep 1

    # 5) 构建 bond options 字符串
    local bond_opts="mode=${BOND_MODE},miimon=${BOND_MIIMON:-100}"
    [[ -n "${BOND_XMIT_HASH:-}" ]] && bond_opts+=",xmit_hash_policy=${BOND_XMIT_HASH}"
    [[ -n "${BOND_LACP_RATE:-}" ]] && bond_opts+=",lacp_rate=${BOND_LACP_RATE}"
    [[ -n "${BOND_UPDELAY:-}" && "${BOND_UPDELAY}" != "0" ]] && bond_opts+=",updelay=${BOND_UPDELAY}"
    [[ -n "${BOND_DOWNDELAY:-}" && "${BOND_DOWNDELAY}" != "0" ]] && bond_opts+=",downdelay=${BOND_DOWNDELAY}"

    # 6) 创建 NM bond 连接
    local con_name="${bond}-nm"
    log_info "正在创建 NM bond 连接: $con_name ..."

    local nmcli_args=(
        connection add type bond
        con-name "$con_name"
        ifname "$bond"
        bond.options "$bond_opts"
        ipv6.method link-local
    )

    # IP 地址
    if [[ ${#BOND_ADDRESSES[@]} -gt 0 ]]; then
        nmcli_args+=(ipv4.method manual)
        local addr_str
        addr_str=$(printf "%s," "${BOND_ADDRESSES[@]}")
        addr_str="${addr_str%,}"
        nmcli_args+=(ipv4.addresses "$addr_str")
    else
        nmcli_args+=(ipv4.method disabled)
    fi

    # DNS
    if [[ -n "${BOND_DNS:-}" ]]; then
        nmcli_args+=(ipv4.dns "$BOND_DNS")
    fi

    # 路由
    if [[ ${#BOND_ROUTES[@]} -gt 0 ]]; then
        local route_str=""
        for r in "${BOND_ROUTES[@]}"; do
            local dest gw
            dest=$(echo "$r" | awk '{print $1}')
            gw=$(echo "$r" | awk '{print $2}')
            [[ -n "$route_str" ]] && route_str+=","
            route_str+="${dest} ${gw}"
        done
        nmcli_args+=(ipv4.routes "$route_str")
    fi

    # 默认网关
    if [[ -n "${BOND_DEFAULT_GW:-}" ]]; then
        nmcli_args+=(ipv4.gateway "$BOND_DEFAULT_GW")
    fi

    nmcli "${nmcli_args[@]}"

    # 7) 添加 slave
    log_info "正在添加 slave 接口 ..."
    for slave in "${BOND_SLAVES[@]}"; do
        nmcli connection add type ethernet \
            con-name "${con_name}-slave-${slave}" \
            ifname "$slave" \
            master "$con_name"
        log_info "  已添加 slave: $slave"
    done

    # 8) 激活连接
    log_info "正在激活 $con_name ..."
    nmcli connection up "$con_name"
    sleep 3

    # 9) 清理 NM 自动创建的多余连接
    local wired_cons
    wired_cons=$(nmcli -t -f NAME connection show | grep "^Wired connection" || true)
    if [[ -n "$wired_cons" ]]; then
        while IFS= read -r wcon; do
            nmcli connection delete "$wcon" 2>/dev/null || true
            log_info "  已清理多余连接: $wcon"
        done <<< "$wired_cons"
    fi

    # 10) 从 netplan 配置中移除已切换到 NM 的 bond 及其 slave 定义
    #     防止重启后 netplan generate 重新生成 networkd 配置导致冲突
    log_info "正在清理 netplan 配置中 $bond 的定义 ..."
    local slaves_json
    slaves_json=$(printf '"%s",' "${BOND_SLAVES[@]}")
    slaves_json="[${slaves_json%,}]"

    for yaml_file in /etc/netplan/*.yaml; do
        [[ -f "$yaml_file" ]] || continue
        python3 -c "
import yaml, sys, os

bond_name = '${bond}'
slave_names = ${slaves_json}
fpath = '${yaml_file}'

with open(fpath) as f:
    data = yaml.safe_load(f)

if not data or 'network' not in data:
    sys.exit(0)

net = data['network']
changed = False

# 移除 bond 定义
if 'bonds' in net and bond_name in net['bonds']:
    del net['bonds'][bond_name]
    changed = True
    if not net['bonds']:
        del net['bonds']

# 移除 slave 以太网定义
if 'ethernets' in net:
    for s in slave_names:
        if s in net['ethernets']:
            del net['ethernets'][s]
            changed = True
    if not net['ethernets']:
        del net['ethernets']

if changed:
    # 检查是否还有实质内容
    remaining_keys = [k for k in net.keys() if k not in ('version', 'renderer')]
    if not remaining_keys:
        # 文件已空，删除
        os.remove(fpath)
        print(f'REMOVED {fpath} (empty after cleanup)')
    else:
        with open(fpath, 'w') as f:
            yaml.dump(data, f, default_flow_style=False, allow_unicode=True)
        os.chmod(fpath, 0o600)
        print(f'UPDATED {fpath}')
else:
    print(f'SKIPPED {fpath} (no changes needed)')
" 2>&1 | while read -r line; do log_info "  $line"; done
    done

    # 重新生成 netplan，确保只剩下未切换的接口
    netplan generate 2>/dev/null || true
    log_info "netplan 配置清理完成"
}

###############################################################################
# 验证切换结果
###############################################################################
verify_switch() {
    local bond="$1"

    echo ""
    
    log_info "========== 验证结果 =========="

    # NM 设备状态
    local nm_state
    nm_state=$(nmcli -t -f DEVICE,STATE device status | grep "^${bond}:" | cut -d: -f2)
    if [[ "$nm_state" == "connected" ]]; then
        log_info "✅ $bond 已由 NetworkManager 管理 (state: $nm_state)"
    else
        log_error "❌ $bond NM 状态异常: $nm_state"
        return 1
    fi

    # IP 地址检查
    local current_addrs
    current_addrs=$(ip -4 addr show "$bond" | grep "inet " | awk '{print $2}')
    if [[ -n "$current_addrs" ]]; then
        log_info "✅ IP 地址: $current_addrs"
    else
        log_error "❌ $bond 没有 IPv4 地址"
        return 1
    fi

    # Bond 状态
    local mii_status
    mii_status=$(grep "MII Status:" "/proc/net/bonding/$bond" | head -1 | awk '{print $NF}')
    if [[ "$mii_status" == "up" ]]; then
        log_info "✅ Bond MII 状态: up"
    else
        log_error "❌ Bond MII 状态: $mii_status"
        return 1
    fi

    # Slave 数量
    local slave_count
    slave_count=$(grep -c "Slave Interface:" "/proc/net/bonding/$bond")
    log_info "✅ Bond slave 数量: $slave_count"

    # 路由检查
    local route_count
    route_count=$(ip route show dev "$bond" | wc -l)
    log_info "✅ 路由条目: $route_count"

    # NM 连接列表
    echo ""
    log_info "NM 连接状态:"
    nmcli connection show | grep -E "${bond}|DEVICE" || true

    echo ""
    log_info "========== 切换完成 =========="
}

###############################################################################
# 回滚
###############################################################################
do_rollback() {
    local bond="$1"

    log_info "正在回滚 $bond 到 systemd-networkd ..."

    if [[ ! -d "$BACKUP_DIR/$bond" ]]; then
        log_error "找不到备份目录: $BACKUP_DIR/$bond"
        exit 1
    fi

    # 1) 删除 NM 连接
    local con_name="${bond}-nm"
    nmcli connection delete "$con_name" 2>/dev/null || true
    for slave_file in "$BACKUP_DIR/$bond/systemd-network/"*; do
        local slave_name
        slave_name=$(basename "$slave_file" | sed 's/10-netplan-//' | sed 's/\..*//')
        nmcli connection delete "${con_name}-slave-${slave_name}" 2>/dev/null || true
    done

    # 2) 恢复 networkd 运行时配置
    cp -a "$BACKUP_DIR/$bond/systemd-network/"* /run/systemd/network/ 2>/dev/null || true

    # 3) 恢复 NM 全局配置
    if [[ -f "$BACKUP_DIR/$bond/10-globally-managed-devices.conf.bak" ]]; then
        cp -a "$BACKUP_DIR/$bond/10-globally-managed-devices.conf.bak" \
              /usr/lib/NetworkManager/conf.d/10-globally-managed-devices.conf
    fi

    # 4) 让 NM 释放设备
    nmcli device set "$bond" managed no 2>/dev/null || true

    # 5) 重载服务
    systemctl reload systemd-networkd || true
    systemctl restart NetworkManager || true
    sleep 2

    # 6) 重新应用 netplan
    netplan apply 2>/dev/null || true
    sleep 3

    # 7) 验证
    local nd_state
    nd_state=$(networkctl list "$bond" 2>/dev/null | grep "$bond" | awk '{print $NF}')
    if [[ "$nd_state" == "configured" ]]; then
        log_info "✅ 回滚成功，$bond 已恢复由 systemd-networkd 管理"
    else
        log_warn "回滚后 $bond 状态: $nd_state，请手动检查"
    fi
}

###############################################################################
# 主流程
###############################################################################
main() {
    local rollback=false
    local bond=""

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --rollback) rollback=true; shift ;;
            -h|--help)  usage ;;
            *)          bond="$1"; shift ;;
        esac
    done

    if [[ -z "$bond" ]]; then
        usage
    fi

    if $rollback; then
        do_rollback "$bond"
        exit 0
    fi

    echo "=============================================="
    echo " Bond 接口切换: systemd-networkd → NetworkManager"
    echo " 目标接口: $bond"
    echo "=============================================="
    echo ""

    # 步骤 1: 前置检查
    preflight_check "$bond"

    # 步骤 2: 收集当前配置
    collect_bond_info "$bond"

    # 步骤 3: 备份
    backup_config "$bond"

    # 步骤 4: 安装 NM
    install_nm

    # 步骤 5: 处理 cloud-init
    disable_cloud_init_network

    # 步骤 6: 执行切换
    do_switch "$bond"

    # 步骤 7: 验证
    verify_switch "$bond"
}

main "$@"
