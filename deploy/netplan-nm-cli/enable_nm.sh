#!/usr/bin/env bash
set -Eeuo pipefail

############################################
# 全局配置
############################################
NETPLAN_FILE="/etc/netplan/01-netcfg.yaml"
CONN_LIST=(
  bond0 bond1
  bond0-eth0 bond0-eth2
  bond1-eth1 bond1-eth3
)

############################################
# 日志 & 错误处理
############################################
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO] $*${NC}"; }
warn()  { echo -e "${YELLOW}[WARN] $*${NC}"; }
error() { echo -e "${RED}[ERROR] $*${NC}"; exit 1; }

trap 'error "脚本在第 $LINENO 行失败"' ERR

############################################
# 基础检查
############################################
require_root() {
  [[ $EUID -eq 0 ]] || error "请使用 root 或 sudo 执行"
}

require_cmd() {
  command -v "$1" &>/dev/null || error "缺少命令: $1"
}

############################################
# NetworkManager 相关函数
############################################
set_nm_managed_true() {
  local conf="/etc/NetworkManager/NetworkManager.conf"
  [[ -f $conf ]] || return 0
  sed -i 's/^[[:space:]]*managed[[:space:]]*=[[:space:]]*false/managed=true/' "$conf"
}

install_networkmanager() {
  info "安装并启用 NetworkManager"

  systemctl disable --now systemd-networkd 2>/dev/null || true

  apt update -y
  apt install -y network-manager

  systemctl enable --now NetworkManager
  systemctl is-active --quiet NetworkManager || error "NetworkManager 未正常运行"
}

set_netplan_renderer_nm() {
  [[ -f $NETPLAN_FILE ]] || error "Netplan 文件不存在: $NETPLAN_FILE"

  if grep -qE 'renderer:[[:space:]]*networkd' "$NETPLAN_FILE"; then
    info "切换 netplan renderer → NetworkManager"
    sed -i 's/renderer:[[:space:]]*networkd/renderer: NetworkManager/' "$NETPLAN_FILE"
  fi
}

############################################
# Bond 解析 & 应用
############################################
apply_netplan_bonds_with_nmcli() {
  local file="$1"
  info "解析 netplan bond 配置: $file"

  declare -A IFACES ADDR GW DNS OPTS
  local bond=""

  while read -r line; do
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z $line || $line == \#* ]] && continue

    if [[ $line =~ ^bond[0-9]+: ]]; then
      bond="${line%%:*}"
      continue
    fi
    [[ -z $bond ]] && continue

    [[ $line == "- eth"* ]] && IFACES[$bond]+="${line#- } "
    [[ $line =~ addresses:\ \[ ]] && ADDR[$bond]=$(sed -E 's/.*\[(.*)\].*/\1/' <<<"$line")
    [[ $line =~ ^gateway4: ]] && GW[$bond]="${line#gateway4: }"
    [[ $line =~ ^-\ 10\. ]] && DNS[$bond]+="${line#- } "

    [[ $line =~ ^mode: ]] && OPTS[$bond]="mode=${line#mode: }"
    [[ $line =~ ^lacp-rate: ]] && OPTS[$bond]+=",lacp_rate=${line#lacp-rate: }"
    [[ $line =~ ^mii-monitor-interval: ]] && OPTS[$bond]+=",miimon=${line#mii-monitor-interval: }"
    [[ $line =~ ^transmit-hash-policy: ]] && OPTS[$bond]+=",xmit_hash_policy=${line#transmit-hash-policy: }"
    [[ $line =~ all-slaves-active:\ true ]] && OPTS[$bond]+=",all_slaves_active=1"
  done < "$file"

  for b in "${!IFACES[@]}"; do
    info "配置 bond: $b"
    nmcli con delete "$b" &>/dev/null || true

    nmcli con add type bond ifname "$b" con-name "$b" \
      bond.options "${OPTS[$b]}" \
      ipv4.method manual \
      ipv4.addresses "${ADDR[$b]}" \
      ${GW[$b]+ipv4.gateway "${GW[$b]}"} \
      ${DNS[$b]+ipv4.dns "${DNS[$b]}"}

    for i in ${IFACES[$b]}; do
      nmcli con delete "$b-$i" &>/dev/null || true
      nmcli con add type ethernet ifname "$i" con-name "$b-$i" master "$b"
    done
  done
}

############################################
# 静态路由同步
############################################

iproute_to_nmcli_bond0() {
  local CONN="bond0"

  ip route | awk '
  $0 ~ /dev bond0/ && $0 ~ / via / && $1 != "default" {
    dest=$1
    gw=""
    metric=""

    for (i=1;i<=NF;i++) {
      if ($i=="via") gw=$(i+1)
      if ($i=="metric") metric=$(i+1)
    }

    print dest, gw, metric
  }' | while read -r DEST GW METRIC; do

    if [[ -n "$METRIC" ]]; then
      nmcli con mod "$CONN" +ipv4.routes "$DEST $GW $METRIC"
    else
      nmcli con mod "$CONN" +ipv4.routes "$DEST $GW"
    fi

  done
}

############################################
# 激活连接
############################################
activate_connections() {
  info "激活 NetworkManager 连接"
  for c in "${CONN_LIST[@]}"; do
    nmcli con show "$c" &>/dev/null || { warn "跳过不存在连接 $c"; continue; }
    nmcli con up "$c" || warn "$c 激活失败"
  done
}

############################################
# 主流程
############################################
main() {
  require_root

  install_networkmanager
  set_nm_managed_true
  systemctl restart NetworkManager
  require_cmd nmcli
  set_netplan_renderer_nm

  apply_netplan_bonds_with_nmcli "$NETPLAN_FILE"
  iproute_to_nmcli_bond0
  netplan generate
  systemctl restart NetworkManager
  sleep 5
  activate_connections
  systemctl restart NetworkManager

  info "当前活跃连接："
  nmcli con show --active
  info "网络初始化完成"
  systemctl status systemd-networkd
  systemctl stop systemd-networkd
  systemctl disable systemd-networkd
}

main "$@"
