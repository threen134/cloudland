#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh
source ./vnc_lib.sh

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

boot_disk=$(jq -c '.boot_disk // empty' <<<"$metadata" 2>/dev/null)
boot_disk_xml=""
# Recovering an instance of a host that is down (shared-storage-design.md §11.3): its disks are in shared pools and
# used as they are; the definition is made again from the record and reported as an evacuation
evacuate=$(jq -c '.evacuate // empty' <<<"$metadata" 2>/dev/null)
report_reason=init
[ -n "$evacuate" ] && report_reason=evacuate
if [ -n "$evacuate" ]; then
    function disk_fail()
    {
        echo "|:-COMMAND-:| $(basename $0) '$ID' 'error' '$NODE_ID' 'evacuate' '${1//\'/}'"
        exit -1
    }
    [ "$(jq -r '.existing // false' <<<"$boot_disk")" = "true" ] || disk_fail "an evacuation needs the existing boot disk"
    # Sent again (a retry, a lost report): already defined here, it is only reported, and started when it should run
    if timeout 30 virsh dominfo $vm_ID >/dev/null 2>&1; then
        st=$(timeout 30 virsh domstate $vm_ID 2>/dev/null | sed 's/shut off/shut_off/')
        if [ "$st" != "running" ] && [ "$(jq -r '.start' <<<"$evacuate")" = "true" ]; then
            vnc_ensure_domain_passwd $vm_ID
            timeout 120 virsh start $vm_ID >/dev/null 2>&1 && st=running
        fi
        echo "|:-COMMAND-:| $(basename $0) '$ID' '$st' '$NODE_ID' 'evacuate'"
        exit 0
    fi
    drv_load "$boot_disk" || disk_fail "$guard_error"
    drv_volume "$boot_disk" "$vol_ID" || disk_fail "$guard_error"
    drv_guard || disk_fail "$guard_error"
    drv_exists "$drv_vol" || disk_fail "boot disk $drv_vol not found"
    # UEFI variables of a file pool are there for every host; those of an RBD disk were on the host that is down, and
    # are made again from the template: the boot entries are lost, the default boot path is taken
    vm_nvram=$(jq -r '.nvram // empty' <<<"$boot_disk")
    [ -z "$vm_nvram" ] && vm_nvram=$image_dir/${vm_ID}_VARS.fd
    if [ "$boot_loader" = "uefi" ] && [ ! -s "$vm_nvram" ]; then
        cp $nvram_template $vm_nvram || disk_fail "failed to create $vm_nvram"
    fi
    vm_img=$drv_vol
    boot_disk_xml=$(drv_disk_xml "$drv_vol" vda)
    vol_state=attached
elif [ -n "$boot_disk" ]; then
    # A boot disk in a shared pool (shared-storage-design.md §9.7): made by the driver of the pool from the copy of the
    # image in the pool, a clone or a full copy; clapi sent where everything is
    function disk_fail()
    {
        shared_boot_report "$vol_ID" error "$1"
        exit -1
    }
    shared_boot_load "$boot_disk" "$vol_ID" "$ID" || disk_fail "$guard_error"
    shared_boot_make "$drv_vol" "$disk_size" || disk_fail "$guard_error"
    # UEFI variables next to the disk in a file pool, where every host opens them; on this host for an RBD disk
    vm_nvram=${sb_nvram:-$image_dir/${vm_ID}_VARS.fd}
    if [ "$boot_loader" = "uefi" ] && ! cp $nvram_template $vm_nvram; then
        drv_drop "$drv_vol"
        disk_fail "failed to create $vm_nvram"
    fi
    vm_img=$drv_vol
    boot_disk_xml=$(drv_disk_xml "$drv_vol" vda)
    vol_state=attached
    shared_boot_report "$vol_ID" attached
else
    pool_root=$(pool_root "$pool") || die "invalid pool $pool"
    vm_img=$pool_root/$disk_relpath
    created=0

    # Report the boot disk could not be created and give its place back
    function disk_fail()
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
fi

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
# The definitions saved here carry the VNC password
chmod 700 $xml_dir/$vm_ID
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
# The disk of a shared pool as its driver describes it, in place of the file disk of the template
if [ -n "$boot_disk_xml" ] && ! xml_replace_disk $vm_xml vda "$boot_disk_xml"; then
    echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$NODE_ID' '$report_reason'"
    exit -1
fi
# A random VNC password of its own: QEMU accepts the password of the console only when started with one
if ! vnc_xml_set_passwd $vm_xml; then
    echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$NODE_ID' '$report_reason'"
    exit -1
fi

# evacuate_fail <message>: an evacuation that can not go on leaves nothing of the instance here (definition, security
# group chains, router), or the heartbeat of this host would take over the instance the evacuation gave back to its
# host; no disk is touched (clear_stale_vm.sh, its report kept out)
function evacuate_fail()
{
    local msg=${1//$'\n'/ }
    ./clear_stale_vm.sh "$ID" "$(jq -r '[.vlans[]?.router | numbers] | max // 0' <<<"$metadata" 2>/dev/null || echo 0)" >/dev/null
    echo "|:-COMMAND-:| $(basename $0) '$ID' 'error' '$NODE_ID' 'evacuate' '${msg//\'/}'"
    exit -1
}

define_out=$(virsh define $vm_xml 2>&1)
define_rc=$?
echo "$define_out" >&2
[ -n "$evacuate" ] && [ $define_rc -ne 0 ] && evacuate_fail "defining it on $(hostname) failed: ${define_out:0:200}"
# The data disks of an evacuated instance, with the devices they had (the guest knows them by those), before it starts
if [ -n "$evacuate" ]; then
    while read -r row; do
        dev=$(jq -r '.device' <<<"$row")
        dvol=$(jq -r '.volume_id' <<<"$row")
        [[ "$dev" =~ ^vd[a-z]+$ ]] && [[ "$dvol" =~ ^[0-9]+$ ]] || evacuate_error="invalid data disk $dvol $dev"
        if [ -z "$evacuate_error" ] && drv_load "$row" && drv_volume "$row" "$dvol" && drv_guard && drv_exists "$drv_vol"; then
            drv_disk_xml "$drv_vol" "$dev" >$xml_dir/$vm_ID/disk-${dvol}.xml
            virsh attach-device $vm_ID $xml_dir/$vm_ID/disk-${dvol}.xml --config >/dev/null 2>&1 || evacuate_error="attaching data disk $dvol failed"
        else
            evacuate_error=${evacuate_error:-"data disk $dvol: ${guard_error:-not found}"}
        fi
        [ -n "$evacuate_error" ] && break
    done < <(jq -c '.data_disks[]?' <<<"$metadata")
    [ -n "$evacuate_error" ] && evacuate_fail "$evacuate_error"
    virsh dumpxml --security-info $vm_ID 2>/dev/null | sed "s/autoport='yes'/autoport='no'/g" >$vm_xml.dump && mv -f $vm_xml.dump $vm_xml
fi
# Map the libvirt domain to its instance id for the Prometheus metrics
./generate_vm_instance_map.sh add $vm_ID
# Instances are started by report_rc.sh after a host reboot, once their pools are checked
virsh autostart $vm_ID --disable
jq .vlans <<< $metadata | ./sync_nic_info.sh "$ID" "$vm_name" "$os_code"
if [ -n "$evacuate" ] && [ "$(jq -r '.start' <<<"$evacuate")" != "true" ]; then
    # It was shut off on the host that is down: it stays so here
    state=shut_off
else
    start_out=$(virsh start $vm_ID 2>&1)
    start_rc=$?
    echo "$start_out" >&2
    [ $start_rc -eq 0 ] && state=running
    [ -n "$evacuate" ] && [ $start_rc -ne 0 ] && evacuate_fail "starting it on $(hostname) failed: ${start_out:0:200}"
fi
echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$NODE_ID' '$report_reason'"
[ -n "$evacuate" ] && exit 0

# Windows: change the RDP port when another one is asked for, and set the primary IP
if [ "$os_code" = "windows" ]; then
    rdp_port=$(jq -r '.login_port' <<< $metadata)
    if [ -n "$rdp_port" ] && [ "${rdp_port}" != "3389" ] && [ ${rdp_port} -gt 0 ]; then
        async_exec ./async_job/win_rdp_port.sh $ID $rdp_port
    fi
    async_exec ./async_job/win_primary_ip.sh $ID <<< $metadata
fi
