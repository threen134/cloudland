#!/bin/bash

# Expose the serial console of an instance on a one-time TCP port for the console proxy.
# The pty libvirt allocates for <serial type='pty'> is relayed by serial_console_bridge.sh, which accepts a
# single connection from the control plane only and removes its firewall rules when done.
# stdout is the callback protocol: print nothing but the |:-COMMAND-:| line

cd $(dirname $0)
source ../cloudrc

[ $# -lt 2 ] && die "$0 <vm_ID> <session> [console_proxy_source]"

ID=${1##inst-}
# Chosen by the clapi request waiting for this console, returned in the callback
session=$2
proxy_source=$3
[[ "$ID" =~ ^[0-9]+$ ]] || die "Invalid instance ID"
[[ "$session" =~ ^[0-9a-f]{16,64}$ ]] || die "Invalid session"
[ -z "$proxy_source" ] || [[ "$proxy_source" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]] || die "Invalid console proxy source"
vm_ID=inst-$ID
current_vm=$vm_ID
vm_rescue=$(virsh list --all | grep "\<$vm_ID-" | awk '{print $2}')
[ -n "$vm_rescue" ] && current_vm=$vm_rescue

pty=$(virsh ttyconsole $current_vm 2>/dev/null)
[ -c "$pty" ] || die "$current_vm has no serial console pty"
command -v socat >/dev/null || die "socat is not installed"

# Sources allowed to connect: the controller endpoint (MANAGEMENT_VIP); the host of the console proxy as
# reported by clapi, which differs from the VIP in an HA control plane because outgoing connections use the
# node's primary address; and the proxy container itself when it runs on this node (its traffic to a local
# address is not masqueraded)
cland_ip=$(sed -n 's/^CLAND_ENDPOINT=\([^:]*\):.*/\1/p' /etc/sysconfig/cloudlet 2>/dev/null)
[ -n "$cland_ip" ] || die "CLAND_ENDPOINT is not configured"
sources=$cland_ip
[ -n "$proxy_source" ] && [ "$proxy_source" != "$cland_ip" ] && sources="$sources $proxy_source"
proxy_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' cloudland-consoleproxy 2>/dev/null)
[ -n "$proxy_ip" ] && sources="$sources $proxy_ip"
# Listen on the address this node uses to reach the controller
local_ip=$(ip -4 route get $cland_ip 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -1)
[ -n "$local_ip" ] || die "Unable to determine the local address towards $cland_ip"

bridge_dir=$run_dir/serial_console
mkdir -p $bridge_dir
# Only one session per instance: a second reader on the pty would split the output between them
old_pid=$(cat $bridge_dir/$vm_ID.pid 2>/dev/null)
if [ -n "$old_pid" ] && grep -qF serial_console_bridge /proc/$old_pid/cmdline 2>/dev/null; then
    kill $old_pid 2>/dev/null
    sleep 0.5
fi

for port in $(shuf -i 16000-16999 -n 20); do
    ss -Htln "sport = :$port" | grep -q . && continue
    setsid ./serial_console_bridge.sh $vm_ID $pty $local_ip $port $sources </dev/null >>$log_dir/serial_console.log 2>&1 &
    # Report the port only once it accepts connections
    for i in $(seq 1 30); do
        ss -Htln "sport = :$port" | grep -q . && break
        kill -0 $! 2>/dev/null || break
        sleep 0.1
    done
    if ss -Htln "sport = :$port" | grep -q .; then
        echo "|:-COMMAND-:| $(basename $0) '$ID' '$session' '$port' '$local_ip'"
        exit 0
    fi
done
die "Unable to start the serial console bridge for $current_vm"
