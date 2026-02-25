#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 1 ] && die "$0 <vlan> [interface]"

# 把第一个入参赋值给 vlan（VLAN 编号）；
# 定义网桥名称 vm_br，格式为 br + VLAN编号（如 VLAN 100 对应 br100）；
# 把第二个可选入参赋值给 interface（自定义网络接口，如 eth0）。
vlan=$1
vm_br=br$vlan
interface=$2

# 检查 /proc/net/dev（系统网络设备状态文件）中是否已存在该网桥；
# grep -q 静默匹配，若网桥已存在（退出码 $? 为 0），脚本直接退出（避免重复创建）。
cat /proc/net/dev | grep -q "\<$vm_br\>:"
[ $? -eq 0 ] && exit 0

# 使用 nmcli 命令创建网桥连接：
# con-name $vm_br：连接名称与网桥名一致；
# type bridge：类型为网桥；
# ifname $vm_br：网桥设备名；
# ipv4.method static：IPv4 为静态配置；
# ipv4.addresses 169.254.169.254/32：配置链路本地地址（仅用于网桥自身标识，无路由功能）。
nmcli connection add con-name $vm_br type bridge ifname $vm_br ipv4.method static ipv4.addresses 169.254.169.254/32
# 修改网桥参数（优化桥接性能）：
# bridge.stp no：关闭 STP（生成树协议，避免虚拟机网络不必要的拓扑检测）；
# bridge.forward-delay 0：转发延迟设为 0（立即转发，减少网络延迟）。
nmcli connection modify $vm_br bridge.stp no
nmcli connection modify $vm_br bridge.forward-delay 0
nmcli connection up $vm_br
# 启用该网桥连接（使网桥生效）。
# 调用自定义工具 apply_bridge（大概率是云环境的网桥配置脚本），传入网桥名，完成网桥的额外配置（如 iptables 规则、转发策略等）。
apply_bridge -I $vm_br
cat /proc/net/dev | grep -q "\<v-$vlan\>:"
# 检查是否已存在 v-VLAN编号 格式的虚拟接口（如 VLAN 100 对应 v-100），若不存在则进入创建逻辑。
# 分支 1：VLAN ≥ 4095（VXLAN 场景）
# [ -z "$interface" ] && interface=$vxlan_interface：若未传入自定义 interface，使用 cloudrc 中定义的 vxlan_interface（默认 VXLAN 底层接口）；
# 创建 VXLAN 接口：
# type vxlan：类型为 VXLAN（虚拟扩展局域网）；
# id $vlan：VXLAN 的 VNI（网络标识）与 VLAN 编号一致；
# vxlan.proxy $proxy_mode：启用 VXLAN 代理模式（proxy_mode 来自 cloudrc）；
# dev $interface：绑定到底层物理 / 逻辑接口；
# ipv4.method disabled：关闭 IPv4（仅用于二层桥接）；
# master $vm_br：将该 VXLAN 接口加入上述网桥。
# 分支 2：VLAN < 4095（普通 VLAN 场景）
# [ -z "$interface" ] && interface=$vlan_interface：若未传入自定义 interface，使用 cloudrc 中定义的 vlan_interface（默认 VLAN 底层接口）；
# 创建 802.1Q VLAN 接口：
# type vlan：类型为 VLAN；
# id $vlan：VLAN 标签编号；
# 其余参数同 VXLAN 分支（关闭 IPv4、加入网桥）。
# bash
#     nmcli connection up v-$vlan
# fi
# 启用刚创建的 VLAN/VXLAN 接口连接。
# bash
# udevadm settle
# 等待 udev（设备管理子系统）完成所有网络设备的初始化，确保接口完全就绪后脚本结束。
# 核心逻辑总结
# 校验入参，加载配置；
# 检查网桥是否已存在，存在则退出；
# 创建并配置网桥（关闭 STP、零转发延迟、链路本地地址）；
# 根据 VLAN 编号创建 VLAN/VXLAN 虚拟接口，并加入网桥；
# 等待设备初始化完成。
# 该脚本是云平台中 “按 VLAN 动态创建网络桥接环境” 的典型实现，适配普通 VLAN 和 VXLAN 两种二层网络场景，依赖 NetworkManager 管理网络配置。
if [ $? -ne 0 ]; then
    if [ $vlan -ge 4095 ]; then
    # 当变量 interface 为空（未定义或值为空字符串）时，就将 vxlan_interface 的值赋给 interface；如果 interface 已经有非空值，则保持其原有值不变（不执行右侧赋值）。
    # vxlan.proxy $proxy_mode：配置 VXLAN 的ARP 代理模式（$proxy_mode 是代理模式变量，常见取值为 on/off 或更精细的模式）。作用是解决 VXLAN 网络中 ARP 广播泛洪问题，代理节点会响应目标主机的 ARP 请求，避免 ARP 报文在 VXLAN 隧道中大量扩散。
        [ -z "$interface" ] && interface=$vxlan_interface
        nmcli connection add con-name v-$vlan type vxlan id $vlan vxlan.proxy $proxy_mode ifname v-$vlan dev $interface ipv4.method disabled master $vm_br
    else
     # 把 cloudrc.local里定义的 vlan_interface 挂在到网桥上
        [ -z "$interface" ] && interface=$vlan_interface
        nmcli connection add con-name v-$vlan type vlan id $vlan ifname v-$vlan dev $interface ipv4.method disabled master $vm_br
    fi
    nmcli connection up v-$vlan
fi
udevadm settle
