#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 15 ] && die "$0 <vm_ID> <image> <qa_enabled> <snapshot> <name> <cpu> <memory> <disk_size> <volume_id> <nested_enable> <boot_loader> <instance_uuid> <image_download_url_b64> <pool_uuid|builtin> <boot_disk_relpath>"

ID=$1
vm_ID=inst-$ID
img_name=$2
qa_enabled=$3
snapshot=$4
vm_name=$5
vm_cpu=$6
vm_mem=$7
disk_size=$8
vol_ID=$9
nested_enable=${10}
boot_loader=${11}
instance_uuid=${12:-$ID}
# presigned GET URL of S3 (base64), empty when S3 is not configured
image_download_url_b64=${13}
pool=${14}
disk_relpath=${15}
image_download_url=""
if [ -n "$image_download_url_b64" ]; then
    image_download_url=$(echo "$image_download_url_b64" | base64 -d 2>/dev/null || echo "")
fi
state=error
vol_state=error

# The metadata comes base64 encoded on stdin
md=$(cat)
metadata=$(echo $md | base64 -d)

let fsize=$disk_size*1024*1024*1024

pool_root=$(pool_root "$pool") || die "invalid pool $pool"
vm_img=$pool_root/$disk_relpath
created=0

# Report the boot disk could not be created and give its place back
disk_fail()
{
    [ "$created" = "1" ] && rm -f "$vm_img"
    echo "|:-COMMAND-:| create_volume_local '$vol_ID' '$disk_relpath' 'error' '${1//\'/}'"
    exit -1
}

pool_enter "$pool" "$NODE_ID" "$vm_img" || disk_fail "$guard_error"
[ -e "$vm_img" ] && disk_fail "boot disk $vm_img already exists"
mkdir -p "$(dirname $vm_img)" || disk_fail "can not create the directory of $vm_img"

# The image is fetched from S3 through the presigned URL when it is not cached; ensure_image_cached serialises with flock
if ! ensure_image_cached "$img_name" "$image_download_url"; then
    disk_fail "image $img_name not available!"
fi
# Mark the cached image as recently used for the cache cleanup (find -mtime +30)
touch "$image_cache/$img_name"
format=$(qemu-img info $image_cache/$img_name | grep 'file format' | cut -d' ' -f3)
created=1
qemu-img convert -f $format -O qcow2 $image_cache/$img_name $vm_img >/dev/null 2>&1 || disk_fail "failed to convert image $img_name"
vsize=$(qemu-img info $vm_img | grep 'virtual size:' | cut -d' ' -f5 | tr -d '(')
[ -z "$vsize" ] && disk_fail "failed to read the size of $vm_img"
[ "$vsize" -gt "$fsize" ] && disk_fail "flavor is smaller than image size"
qemu-img resize -q $vm_img "${disk_size}G" &>/dev/null || disk_fail "failed to resize $vm_img to ${disk_size}G"

# UEFI variables stay next to the boot disk: the builtin pool keeps them in $image_dir, other pools in nvram/
if [ "$pool" = "builtin" ]; then
    vm_nvram="$image_dir/${vm_ID}_VARS.fd"
else
    vm_nvram="$pool_root/nvram/${vm_ID}_VARS.fd"
fi
if [ "$boot_loader" = "uefi" ]; then
    mkdir -p "$(dirname $vm_nvram)" && cp $nvram_template $vm_nvram || disk_fail "failed to create $vm_nvram"
fi
vol_state=attached
echo "|:-COMMAND-:| create_volume_local '$vol_ID' '$disk_relpath' '$vol_state' 'success'"
# Release the pool lock: the rest does not write into the pool
exec 8>&-

./build_meta.sh "$vm_ID" "$vm_name" <<< $md >/dev/null 2>&1
vm_meta=$cache_dir/meta/$vm_ID.iso
template=$template_dir/template_with_qa.xml
if [ "$boot_loader" = "uefi" ]; then
    template=$template_dir/template_uefi_with_qa.xml
fi

[ -z "$vm_mem" ] && vm_mem='1024m'
[ -z "$vm_cpu" ] && vm_cpu=1
let vm_mem=${vm_mem%[m|M]}*1024
mkdir -p $xml_dir/$vm_ID
vm_QA="$qemu_agent_dir/$vm_ID.agent"
vm_xml=$xml_dir/$vm_ID/${vm_ID}.xml
cp $template $vm_xml
if [ "$nested_enable" = "true" ]; then
    vm_nested="require"
else
    vm_nested="disable"
fi
cpu_vendor=$(lscpu | grep "Vendor ID" | awk -F ':' '{print $2}' | tr -d ' ')
if [ "$cpu_vendor" = "GenuineIntel" ]; then
    vm_virt_feature="vmx"
else
    vm_virt_feature="svm"
fi
vhost_queue_num=1
if [ "$vm_cpu" -gt 2 ]; then
    vhost_queue_num=2
fi
os_code=$(jq -r '.os_code' <<< $metadata)
sed -i \
    -e "s/VM_ID/$vm_ID/g" \
    -e "s/VM_MEM/$vm_mem/g" \
    -e "s/VM_CPU/$vm_cpu/g" \
    -e "s/VHOST_QUEUE_NUM/$vhost_queue_num/g" \
    -e "s#VM_IMG#$vm_img#g" \
    -e "s#VM_META#$vm_meta#g" \
    -e "s#VM_AGENT#$vm_QA#g" \
    -e "s/VM_NESTED/$vm_nested/g" \
    -e "s/VM_VIRT_FEATURE/$vm_virt_feature/g" \
    -e "s#VM_BOOT_LOADER#$uefi_boot_loader#g" \
    -e "s#VM_NVRAM#$vm_nvram#g" \
    -e "s/INSTANCE_UUID/$instance_uuid/g" \
    $vm_xml

virsh define $vm_xml
# Map the libvirt domain to its instance id for the Prometheus metrics
./generate_vm_instance_map.sh add $vm_ID
# Instances are started by report_rc.sh after a host reboot, once their pools are checked
virsh autostart $vm_ID --disable
jq .vlans <<< $metadata | ./sync_nic_info.sh "$ID" "$vm_name" "$os_code"
virsh start $vm_ID
[ $? -eq 0 ] && state=running
echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$NODE_ID' 'init'"

# Windows: change the RDP port when another one is asked for, and set the primary IP
if [ "$os_code" = "windows" ]; then
    rdp_port=$(jq -r '.login_port' <<< $metadata)
    if [ -n "$rdp_port" ] && [ "${rdp_port}" != "3389" ] && [ ${rdp_port} -gt 0 ]; then
        async_exec ./async_job/win_rdp_port.sh $ID $rdp_port
    fi
    async_exec ./async_job/win_primary_ip.sh $ID <<< $metadata
fi
