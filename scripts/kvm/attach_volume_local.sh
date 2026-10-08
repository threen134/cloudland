#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 8 ] && echo "$0 <vm_ID> <volume_ID> <volume_path> <volume_uuid> <size_GB> <pool_uuid|builtin> <hostid> <new|existing>" && exit -1

vm_ID=inst-$1
vol_ID=$2
vol_path=$3
vol_uuid=$4
vol_size=$5
pool=$6
hostid=$7
mode=$8
created=0

# Report the failure; a file created by this run is removed again, so clapi can mark the volume not created
fail()
{
    [ "$created" = "1" ] && rm -f "$vol_path"
    echo "|:-COMMAND-:| $(basename $0) '$1' '$vol_ID' '-' '$mode' '${2//\'/}'"
    exit 0
}

pool_enter "$pool" "$hostid" "$vol_path" || fail "$1" "$guard_error"
if [ "$mode" = "new" ]; then
    # Never reuse a file left behind: it belongs to no volume and may hold somebody's data
    [ -e "$vol_path" ] && fail "$1" "volume file $vol_path already exists"
    mkdir -p "$(dirname $vol_path)" || fail "$1" "can not create the directory of $vol_path"
    created=1
    qemu-img create -q -f qcow2 -o cluster_size=2M "$vol_path" ${vol_size}G >/dev/null 2>&1 || fail "$1" "failed to create $vol_path"
else
    # A missing file means the data is gone; attaching an empty disk in its place would hide that
    [ -f "$vol_path" ] || fail "$1" "volume file $vol_path not found"
fi
vol_xml=$xml_dir/$vm_ID/disk-${vol_ID}.xml
cp $template_dir/volume.xml $vol_xml
# Take the next free vdX
used=$(virsh domblklist $vm_ID 2>/dev/null | tail -n +3 | awk '{print $1}')
device=""
for suffix in {b..z}; do
    grep -qx "vd$suffix" <<< "$used" || { device=vd$suffix; break; }
done
[ -z "$device" ] && fail "$1" "no free device name left"
sed -i "s#VOLUME_SOURCE#$vol_path#g;s#VOLUME_TARGET#$device#g;" $vol_xml
if ! virsh attach-device $vm_ID $vol_xml --config --persistent >/dev/null 2>&1; then
    rm -f $vol_xml
    fail "$1" "virsh attach-device failed"
fi
echo "|:-COMMAND-:| $(basename $0) '$1' '$vol_ID' '$device'"
vm_xml=$xml_dir/$vm_ID/$vm_ID.xml
virsh dumpxml --security-info $vm_ID 2>/dev/null | sed "s/autoport='yes'/autoport='no'/g" > $vm_xml.dump && mv -f $vm_xml.dump $vm_xml
