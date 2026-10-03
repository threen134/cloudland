#!/bin/bash
# Attach a volume of a shared pool to an instance of this host (shared-storage-design.md §9.4). The pool and the volume
# come as JSON on stdin. Detaching is the same as for local volumes (detach_volume_local.sh, with the disk-<id>.xml
# kept here).

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 3 ] && die "$0 <vm_ID> <volume_ID> <volume_UUID>"

inst=$1
vol_ID=$2
vm_ID=inst-$inst
args=$(cat)
[[ "$inst" =~ ^[0-9]+$ ]] && [[ "$vol_ID" =~ ^[0-9]+$ ]] || die "invalid instance or volume id"

function fail()
{
    echo "|:-COMMAND-:| attach_volume_shared '$inst' '$vol_ID' '-' '${1//\'/}'"
    exit 0
}

drv_load "$args" || fail "$guard_error"
drv_volume "$args" "$vol_ID" || fail "$guard_error"
drv_guard || fail "$guard_error"
drv_exists "$drv_vol" || fail "volume $drv_vol not found"
[ -d $xml_dir/$vm_ID ] || fail "instance $inst is not defined on this host"
vol_xml=$xml_dir/$vm_ID/disk-${vol_ID}.xml
# Take the next free vdX
used=$(virsh domblklist $vm_ID 2>/dev/null | tail -n +3 | awk '{print $1}')
device=""
for suffix in {b..z}; do
    grep -qx "vd$suffix" <<<"$used" || { device=vd$suffix; break; }
done
[ -z "$device" ] && fail "no free device name left"
drv_disk_xml "$drv_vol" "$device" >$vol_xml
if ! virsh attach-device $vm_ID $vol_xml --config --persistent >/dev/null 2>&1; then
    rm -f $vol_xml
    fail "virsh attach-device failed"
fi
echo "|:-COMMAND-:| attach_volume_shared '$inst' '$vol_ID' '$device'"
vm_xml=$xml_dir/$vm_ID/$vm_ID.xml
virsh dumpxml --security-info $vm_ID 2>/dev/null | sed "s/autoport='yes'/autoport='no'/g" >$vm_xml.dump && mv -f $vm_xml.dump $vm_xml
