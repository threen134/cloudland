#!/bin/bash

# Rewrite wg.conf of a VPN gateway from the full peer list and apply it with wg syncconf (adding or
# removing a peer does not disturb the sessions of the others). Runs on both VRRP nodes: the interface
# exists on both, only the floating IP holder receives traffic. No route is installed here (plan §4.4).
#
# stdin JSON: {"port", "address", "private_key", "peers": [{"name","public_key","preshared_key","allowed_ips"}]}

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

[ $# -lt 2 ] && die "$0 <router> <gw_ID>"

ID=$1
router=router-$ID
gw=$2
vpn_dir=$(vpn_dir_of $ID $gw)
[ -d "$vpn_dir" ] || die "VPN gateway $gw is not built on this node"

content=$(cat)
vpn_json_read "$content" port=.port address=.address
private_key=$(jq -r '.private_key' <<<$content)
dev=wg-$gw

if [ -z "$private_key" -o "$private_key" = "null" ]; then
    # client access disabled
    ip netns exec $router ip link del $dev >/dev/null 2>&1
    rm -f $vpn_dir/wg.conf $vpn_dir/wg.key $vpn_dir/wg_address $vpn_dir/wg.reported
    exit 0
fi

umask 077
conf=$vpn_dir/wg.conf.new
cat >$conf <<EOF
[Interface]
PrivateKey = $private_key
ListenPort = $port
EOF
npeer=$(jq '.peers | length' <<<$content)
i=0
while [ $i -lt $npeer ]; do
    vpn_json_read "$content" public_key=.peers[$i].public_key allowed_ips=.peers[$i].allowed_ips
    preshared_key=$(jq -r ".peers[$i].preshared_key" <<<$content)
    cat >>$conf <<EOF

[Peer]
PublicKey = $public_key
AllowedIPs = $allowed_ips
EOF
    [ -n "$preshared_key" -a "$preshared_key" != "null" ] && echo "PresharedKey = $preshared_key" >>$conf
    let i=$i+1
done
umask 022
echo $address >$vpn_dir/wg_address

(
    flock 9
    mv -f $conf $vpn_dir/wg.conf
    rm -f $vpn_dir/wg.reported
    vpn_apply_wg $router $vpn_dir $gw >/dev/null 2>&1
) 9>$lb_lock_file
exit 0
