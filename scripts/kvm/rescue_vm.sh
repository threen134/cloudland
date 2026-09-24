#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 11 ] && die "$0 <vm_ID> <image> <name> <cpu> <memory> <disk_size> <disk_id> <boot_loader> <instance_uuid> <image_download_url_b64> <boot_disk_path>"

ID=$1
vm_ID=inst-$ID
img_name=$2
vm_name=$3
vm_cpu=$4
vm_mem=$5
disk_size=$6
disk_ID=$7
boot_loader=$8
instance_uuid=${9:-$ID}
# presigned GET URL of S3 (base64), empty when S3 is not configured
image_download_url_b64=${10}
# the boot disk of the instance, attached to the rescue system as vdb
boot_disk=${11}
image_download_url=""
if [ -n "$image_download_url_b64" ]; then
    image_download_url=$(echo "$image_download_url_b64" | base64 -d 2>/dev/null || echo "")
fi
state=error
vm_vnc=""
vol_state=error
snapshot=1
vm_rescue=$vm_ID-rescue

pending_start_remove $ID
./action_vm.sh $ID stop
./action_vm.sh $ID hard_stop
md=$(cat)
metadata=$(echo $md | base64 -d)
./build_meta.sh "$vm_ID" "$vm_name-rescue" "true" <<< $md >/dev/null 2>&1

vm_meta=$cache_dir/meta/$vm_ID-rescue.iso
template=$template_dir/template_with_qa.xml
if [ "$boot_loader" = "uefi" ]; then
    template=$template_dir/template_uefi_with_qa.xml
fi
vm_img=$image_dir/$vm_rescue.disk
if ! ensure_image_cached "$img_name" "$image_download_url"; then
    echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$NODE_ID' 'failed'"
    exit -1
fi
# Mark the cached image as recently used for the cache cleanup
touch "$image_cache/$img_name"
format=$(qemu-img info $image_cache/$img_name | grep 'file format' | cut -d' ' -f3)
if ! qemu-img convert -f $format -O qcow2 $image_cache/$img_name $vm_img >/dev/null 2>&1; then
    rm -f $vm_img
    echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$NODE_ID' 'failed'"
    exit -1
fi
vol_state=attached
disk_template=$template_dir/volume.xml

[ -z "$vm_mem" ] && vm_mem='1024m'
[ -z "$vm_cpu" ] && vm_cpu=1
let vm_mem=${vm_mem%[m|M]}*1024
vm_QA="$qemu_agent_dir/$vm_rescue.agent"
vm_xml=$xml_dir/$vm_ID/$vm_rescue.xml
cp $template $vm_xml
cpu_vendor=$(lscpu | grep "Vendor ID" | awk -F ':' '{print $2}' | tr -d ' ')
if [ "$cpu_vendor" = "GenuineIntel" ]; then
    vm_virt_feature="vmx"
else
    vm_virt_feature="svm"
fi
os_code=$(jq -r '.os_code' <<< $metadata)
vm_nested=disable
vm_nvram="$image_dir/${vm_rescue}_VARS.fd"
if [ "$boot_loader" = "uefi" ]; then
    cp $nvram_template $vm_nvram
    sed -i \
    -e "s/VM_ID/$vm_rescue/g" \
    -e "s/VM_MEM/$vm_mem/g" \
    -e "s/VM_CPU/$vm_cpu/g" \
    -e "s#VM_IMG#$vm_img#g" \
    -e "s#VM_META#$vm_meta#g" \
    -e "s#VM_AGENT#$vm_QA#g" \
    -e "s/VM_NESTED/$vm_nested/g" \
    -e "s/VM_VIRT_FEATURE/$vm_virt_feature/g" \
    -e "s#VM_BOOT_LOADER#$uefi_boot_loader#g" \
    -e "s#VM_NVRAM#$vm_nvram#g" \
    -e "s/INSTANCE_UUID/$instance_uuid/g" \
    $vm_xml
else
    sed -i \
    -e "s/VM_ID/$vm_rescue/g" \
    -e "s/VM_MEM/$vm_mem/g" \
    -e "s/VM_CPU/$vm_cpu/g" \
    -e "s#VM_IMG#$vm_img#g" \
    -e "s#VM_META#$vm_meta#g" \
    -e "s#VM_AGENT#$vm_QA#g" \
    -e "s/VM_NESTED/$vm_nested/g" \
    -e "s/VM_VIRT_FEATURE/$vm_virt_feature/g" \
    -e "s/INSTANCE_UUID/$instance_uuid/g" \
    $vm_xml
fi

virsh define $vm_xml
./generate_vm_instance_map.sh add $vm_ID

disk_xml=$xml_dir/$vm_ID/disk-${disk_ID}-rescue.xml
cp $disk_template $disk_xml

sed -i "s#VOLUME_SOURCE#$boot_disk#g;s#VOLUME_TARGET#vdb#g" $disk_xml

virsh attach-device $vm_rescue $disk_xml --config --persistent

# Attach data volumes that are currently attached to the original VM
original_xml=$(virsh dumpxml $vm_ID 2>/dev/null)
# ASCII 'c' -> vdc (vda=rescue image, vdb=boot volume)
next_rescue_letter=99

for vol_xml in $xml_dir/$vm_ID/disk-*.xml; do
    [ ! -f "$vol_xml" ] && continue
    [[ "$(basename $vol_xml)" == *-rescue* ]] && continue
    vid=$(basename "$vol_xml" .xml | sed 's/disk-//')
    [ "$vid" = "$disk_ID" ] && continue
    src_path=$(grep -oP "file='[^']+'" "$vol_xml" | head -1 | cut -d"'" -f2)
    if [ -z "$src_path" ] || ! echo "$original_xml" | grep -q "$src_path"; then
        log_debug $ID "Data volume $vid not found in dumpxml, skipping (likely detached)"
        continue
    fi
    log_debug $ID "Data volume $vid verified attached, source: $src_path"

    rescue_xml=$xml_dir/$vm_ID/disk-${vid}-rescue.xml
    cp "$vol_xml" "$rescue_xml"
    new_target=vd$(printf "\\$(printf '%03o' "$next_rescue_letter")")
    sed -i "s/<target dev='[^']*'/<target dev='$new_target'/g" "$rescue_xml"
    log_debug $ID "Created rescue copy disk-${vid}-rescue.xml with target remapped to $new_target"

    virsh attach-device $vm_rescue "$rescue_xml" --config --persistent
    if [ $? -eq 0 ]; then
        log_debug $ID "Successfully attached data volume $vid as $new_target to rescue VM $vm_rescue"
    else
        log_debug $ID "Failed to attach data volume $vid as $new_target to rescue VM $vm_rescue"
    fi
    let next_rescue_letter=$next_rescue_letter+1
done
log_debug $ID "Data volume attachment for rescue VM $vm_rescue completed"

vlans=$(jq .vlans <<< $metadata)
nvlan=$(jq length <<< $vlans)
i=0
while [ $i -lt $nvlan ]; do
    read -d'\n' -r vlan mac < <(jq -r ".[$i].vlan, .[$i].mac_address" <<<$vlans)
    nic_name=tap$(echo $mac | cut -d: -f4- | tr -d :)
    interface_xml=$xml_dir/$vm_ID/$nic_name.xml
    if [ ! -f "$interface_xml" ]; then
        template=$template_dir/interface.xml
        cp $template $interface_xml
        sed -i "s/VM_MAC/$mac/g; s/VM_BRIDGE/br$vlan/g; s/VM_VTEP/$nic_name/g; s/QUEUE_NUM/1/g" $interface_xml
    fi
    virsh attach-device $vm_rescue $interface_xml --config --persistent
    let i=$i+1
done
virsh start $vm_rescue
[ $? -eq 0 ] && state=rescuing
echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$NODE_ID' 'sync'"

# check if the vm is windows and whether to change the rdp port
if [ "$os_code" = "windows" ]; then
    rdp_port=$(jq -r '.login_port' <<< $metadata)
    if [ -n "$rdp_port" ] && [ "${rdp_port}" != "3389" ]  && [ ${rdp_port} -gt 0 ]; then
        # run the script to change the rdp port in background
        async_exec ./async_job/win_rdp_port.sh $vm_ID $rdp_port
    fi
fi
