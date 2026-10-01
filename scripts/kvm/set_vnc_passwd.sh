#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 2 ] && die "$0 <vm_ID> <vnc_pass>"

ID=${1##inst-}
vnc_pass=$2
vm_ID=inst-$ID

# The console must not be opened with a password that is not in effect: tell clapi instead of the address
function fail()
{
    log_debug $vm_ID "set_vnc_passwd.sh: $1"
    echo "|:-COMMAND-:| $(basename $0) '$ID' 'error' '${1//\'/}'"
    exit 1
}

# clapi generates letters and digits; VNC authentication uses 8 characters
[[ "$vnc_pass" =~ ^[A-Za-z0-9]{1,8}$ ]] || fail "invalid VNC password"
current_vm=$vm_ID
vm_rescue=$(virsh list --all | grep "\<$vm_ID-" | awk '{print $2}')
[ -n "$vm_rescue" ] && current_vm=$vm_rescue
# Only a running QEMU serves VNC (paused included)
domid=$(virsh domid $current_vm 2>/dev/null | head -1)
[[ "$domid" =~ ^[0-9]+$ ]] || fail "$current_vm is not running"

# The definitions saved next to the device description carry the VNC password
chmod 700 $xml_dir/$vm_ID 2>/dev/null
vnc_xml=$(mktemp) || fail "can not create a temporary file"
trap 'rm -f $vnc_xml' EXIT
sed "s/VNC_PASS/$vnc_pass/g" $template_dir/vnc_template.xml >$vnc_xml
# QEMU takes a password only when it was started with VNC password authentication (a passwd in the definition,
# see vnc_lib.sh); an instance defined without one serves VNC unauthenticated until its next cold start
if ! err=$(virsh update-device $current_vm $vnc_xml --live 2>&1); then
    # Keep it for the next start: then QEMU starts with password authentication
    virsh update-device $current_vm $vnc_xml --config >/dev/null 2>&1
    if ! virsh dumpxml --security-info $current_vm 2>/dev/null | grep -o "<graphics type='vnc'[^>]*>" | grep -q " passwd="; then
        fail "VNC password authentication is off until the instance is stopped and started again"
    fi
    fail "failed to set the VNC password: $(echo "$err" | grep -v '^$' | tail -1)"
fi
virsh update-device $current_vm $vnc_xml --config >/dev/null 2>&1 || log_debug $vm_ID "set_vnc_passwd.sh: failed to keep the VNC password in the definition"

vnc_port=$(virsh dumpxml $current_vm 2>/dev/null | xmllint --xpath "string(/domain/devices/graphics[@type='vnc']/@port)" - 2>/dev/null)
[[ "$vnc_port" =~ ^[0-9]+$ ]] && [ "$vnc_port" -gt 0 ] || fail "no VNC port of $current_vm"
local_ip=$(ifconfig $vnc_interface | grep 'inet ' | awk '{print $2}' | head -1)
[ -n "$local_ip" ] || fail "no address on $vnc_interface"
echo "|:-COMMAND-:| $(basename $0) '$ID' '$vnc_port' '$local_ip'"
