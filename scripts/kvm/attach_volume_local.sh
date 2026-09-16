#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 4 ] && echo "$0 <vm_ID> <volume_ID> <volume_path> <volume_uuid> [size_GB]" && exit -1

vm_ID=inst-$1
vol_ID=$2
vol_path=$volume_dir/$3
vol_uuid=$4
vol_size=$5
# 本地卷在首次挂载时于虚拟机所在节点创建
if [ ! -f "$vol_path" ]; then
    if [ -z "$vol_size" ] || ! qemu-img create -q -f qcow2 -o cluster_size=2M $vol_path ${vol_size}G; then
        echo "|:-COMMAND-:| $(basename $0) '' '$vol_ID' ''"
        exit 0
    fi
fi
vol_xml=$xml_dir/$vm_ID/disk-${vol_ID}.xml
cp $template_dir/volume.xml $vol_xml
# 取下一个空闲的 vdX：原先按 WDS 的 type='network' 计数，本地卷是 type='file'，永远算成 0，第二块数据盘会与 vdb 冲突
used=$(virsh domblklist $vm_ID 2>/dev/null | tail -n +3 | awk '{print $1}')
device=""
for suffix in {b..z}; do
    grep -qx "vd$suffix" <<< "$used" || { device=vd$suffix; break; }
done
if [ -z "$device" ]; then
    echo "|:-COMMAND-:| $(basename $0) '' '$vol_ID' ''"
    exit 0
fi
sed -i "s#VOLUME_SOURCE#$vol_path#g;s#VOLUME_TARGET#$device#g;" $vol_xml
virsh attach-device $vm_ID $vol_xml --config --persistent
if [ $? -eq 0 ]; then
    echo "|:-COMMAND-:| $(basename $0) '$1' '$vol_ID' '$device'"
else
    echo "|:-COMMAND-:| $(basename $0) '' '$vol_ID' ''"
fi
vm_xml=$xml_dir/$vm_ID/$vm_ID.xml
virsh dumpxml --security-info $vm_ID 2>/dev/null | sed "s/autoport='yes'/autoport='no'/g" > $vm_xml.dump && mv -f $vm_xml.dump $vm_xml
