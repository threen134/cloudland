#!/bin/bash

# Expose the serial console of an instance on a one-time TCP port for the console proxy.
# The pty libvirt allocates for <serial type='pty'> is relayed by console_bridge.sh, which accepts a
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
vm_ID=inst-$ID
current_vm=$vm_ID
vm_rescue=$(virsh list --all | grep "\<$vm_ID-" | awk '{print $2}')
[ -n "$vm_rescue" ] && current_vm=$vm_rescue

pty=$(virsh ttyconsole $current_vm 2>/dev/null)
[ -c "$pty" ] || die "$current_vm has no serial console pty"
console_bridge_addrs "$proxy_source"

# Only one session per instance: a second reader on the pty would split the output between them
old_pid=$(cat $run_dir/console_bridge/$vm_ID.pid 2>/dev/null)
if [ -n "$old_pid" ] && grep -qF console_bridge /proc/$old_pid/cmdline 2>/dev/null; then
    kill $old_pid 2>/dev/null
    sleep 0.5
fi

port=$(start_console_bridge $vm_ID 0 "FILE:$pty,raw,echo=0" $log_dir/serial_console.log) || \
    die "Unable to start the serial console bridge for $current_vm"
echo "|:-COMMAND-:| $(basename $0) '$ID' '$session' '$port' '$console_listen_ip'"
