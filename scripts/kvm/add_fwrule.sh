#!/bin/bash

cd `dirname $0`
source ../cloudrc

rules=$(cat)
len=$(jq length <<< $rules)
vtep_ip=$(ifconfig $vxlan_interface | grep 'inet ' | awk '{print $2}')
i=0
while [ $i -lt $len ]; do
#    instance=$(jq -r .instance <<< $rule)
    read -d'\n' -r vni router gateway outer_ip inner_ip inner_mac < <(jq -r ".[$i].vni, .[$i].router, .[$i].gateway, .[$i].outer_ip, .[$i].inner_ip, .[$i].inner_mac" <<<$rules)
    cat /proc/net/dev | grep -q "\<br$vni\>:" || ./create_link.sh $vni
    # 网关口按路由器 netns 内是否存在判断，不能看网桥：网桥由 NetworkManager 持久化，节点重启后自动重建，
    # 路由器 netns 和其中的网关口不会，按网桥判断会让重启过的节点永远缺少该子网网关（LB 访问不到后端）
    ip netns exec router-$router ip link show ns-$vni >/dev/null 2>&1 || ./set_subnet_gw.sh $router $vni $gateway
    if [ "$outer_ip" != "$vtep_ip" ]; then
	bridge fdb | grep "\<$inner_mac\>"
        [ $? -eq 0 ] && bridge fdb del $inner_mac dev v-$vni
        bridge fdb add $inner_mac dev v-$vni dst $outer_ip self permanent
	in_ip=${inner_ip%%/*}
	ip neighbor | grep "\<$in_ip\> dev v-$vni\>"
        [ $? -eq 0 ] && ip neighbor del $in_ip dev v-$vni
        ip neighbor add $in_ip lladdr $inner_mac dev v-$vni nud permanent
    fi
    let i=$i+1
done
