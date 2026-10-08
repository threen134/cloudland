#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 3 ] && die "$0 <vm_ID> <volume_ID> <target>"

vm_ID=inst-$1
vol_ID=$2
target=$3

# Only the definition changes: the file is on a lost pool and must not be touched.
# A running instance keeps the disk until its next restart.
vol_xml=$xml_dir/$vm_ID/disk-${vol_ID}.xml
if [ -n "$target" ]; then
    virsh detach-disk $vm_ID $target --config >/dev/null 2>&1
elif [ -f "$vol_xml" ]; then
    virsh detach-device $vm_ID $vol_xml --config >/dev/null 2>&1
fi
rm -f $vol_xml
vm_xml=$xml_dir/$vm_ID/$vm_ID.xml
virsh dumpxml --security-info --inactive $vm_ID 2>/dev/null | sed "s/autoport='yes'/autoport='no'/g" > $vm_xml.dump && mv -f $vm_xml.dump $vm_xml
exit 0
