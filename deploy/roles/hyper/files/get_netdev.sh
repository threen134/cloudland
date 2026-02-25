#!/bin/bash
#该脚本文件是一个基于 Bash 编写的 Shell 脚本，核心作用是根据指定的本地 IP 地址，
#查找对应的网络设备名称，并针对桥接网络设备（bridge）做了特殊处理。
local_ip=$1
netdev=$(ip addr show | grep $local_ip | sed 's/.* //')
# 判断网络设备名称是否以 br 开头（桥接设备特征）：
if [ "${netdev##br}" != "$netdev" ]; then
    netdev=$(ip -d -o link show | grep 'master br5000' | grep bridge_slave | head -1 | cut -d: -f2 | xargs)
fi
# echo "$netdev"
# 础场景：通过 IP 地址快速定位对应的物理 / 虚拟网络接口名称；
# 桥接网络适配：若定位到的是桥接设备（br 开头），则进一步找到该桥接设备下的第一个从设备（适配桥接网络环境的设备名称获取需求）；
# 典型应用：网络配置自动化、设备监控脚本、容器 / 虚拟化环境的网络设备识别等。