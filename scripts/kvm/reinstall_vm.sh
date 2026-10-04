#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh
source ./vnc_lib.sh

[ $# -lt 13 ] && die "$0 <vm_ID> <image> <snapshot> <volume_id> <cpu> <memory> <disk_size> <hostname> <boot_loader> <instance_uuid> <image_download_url_b64> <pool_uuid|builtin> <boot_disk_relpath>"

ID=$1
vm_ID=inst-$ID
img_name=$2
snapshot=$3
vol_ID=$4
vm_cpu=$5
vm_mem=$6
disk_size=$7
vm_name=$8
boot_loader=$9
instance_uuid=${10:-$ID}
# presigned GET URL of S3 (base64), empty when S3 is not configured
image_download_url_b64=${11}
pool=${12}
disk_relpath=${13}
image_download_url=""
if [ -n "$image_download_url_b64" ]; then
    image_download_url=$(echo "$image_download_url_b64" | base64 -d 2>/dev/null || echo "")
fi
state=error
vol_state=error

md=$(cat)
metadata=$(echo $md | base64 -d)
let fsize=$disk_size*1024*1024*1024

boot_disk=$(jq -c '.boot_disk // empty' <<<"$metadata" 2>/dev/null)
boot_disk_xml=""
if [ -n "$boot_disk" ]; then
    # A boot disk in a shared pool (shared-storage-design.md §9.8): the new disk is made from the copy of the new image
    # under a name of its own first, and takes the place of the old disk only once the instance is down
    new_img=""
    function disk_fail()
    {
        [ -n "$new_img" ] && drv_drop "$new_img"
        shared_boot_report "$vol_ID" error "$1"
        exit -1
    }
    # The disk is shared by the hosts of the pool: only the host the instance is defined on replaces it, never one it
    # left (a reinstall sent to a host the instance is not on would remove a disk in use elsewhere)
    virsh dominfo $vm_ID >/dev/null 2>&1 || disk_fail "instance $ID is not defined on host $NODE_ID"
    pending_start_remove $ID
    shared_boot_load "$boot_disk" "$vol_ID" "$ID" || disk_fail "$guard_error"
    tmp_img=$(drv_temp_of "$drv_vol")
    # Left by a reinstall that died
    if drv_exists "$tmp_img"; then
        drv_drop "$tmp_img" || disk_fail "$guard_error"
    fi
    shared_boot_make "$tmp_img" "$disk_size" || disk_fail "$guard_error"
    new_img=$tmp_img
    vm_img=$drv_vol
    vm_nvram=${sb_nvram:-$image_dir/${vm_ID}_VARS.fd}
    boot_disk_xml=$(drv_disk_xml "$drv_vol" vda)
else
    pool_root=$(pool_root "$pool") || die "invalid pool $pool"
    vm_img=$pool_root/$disk_relpath
    new_img=$vm_img.reinstall

    function disk_fail()
    {
        rm -f "$new_img"
        echo "|:-COMMAND-:| create_volume_local '$vol_ID' '$disk_relpath' 'error' '${1//\'/}'"
        exit -1
    }

    pending_start_remove $ID
    pool_enter "$pool" "$NODE_ID" "$vm_img" || disk_fail "$guard_error"
    # Build the new boot disk next to the old one first, so a failure leaves the instance as it was
    if ! ensure_image_cached "$img_name" "$image_download_url"; then
        disk_fail "image $img_name not available!"
    fi
    touch "$image_cache/$img_name"
    format=$(qemu-img info $image_cache/$img_name | grep 'file format' | cut -d' ' -f3)
    qemu-img convert -f $format -O qcow2 $image_cache/$img_name $new_img >/dev/null 2>&1 || disk_fail "failed to convert image $img_name"
    vsize=$(qemu-img info $new_img | grep 'virtual size:' | cut -d' ' -f5 | tr -d '(')
    [ -z "$vsize" ] && disk_fail "failed to read the size of $new_img"
    [ "$vsize" -gt "$fsize" ] && disk_fail "flavor is smaller than image size"
    qemu-img resize -q $new_img "${disk_size}G" &>/dev/null || disk_fail "failed to resize the new boot disk to ${disk_size}G"
fi

vm_xml=$xml_dir/$vm_ID/${vm_ID}.xml
# The definition and its backups carry the VNC password: instances defined before it existed have an open directory
chmod 700 $xml_dir/$vm_ID 2>/dev/null
mv $vm_xml $vm_xml-$(date +'%s.%N')
# --security-info keeps the VNC password in the definition written again below
virsh dumpxml --security-info $vm_ID >$vm_xml
vnc_xml_ensure_passwd $vm_xml
virsh destroy $vm_ID >/dev/null 2>&1
if [ -n "$boot_disk" ]; then
    # The UEFI variables stay until the new disk is in place: a failure brings the instance back as it was
    virsh undefine --keep-nvram $vm_ID >/dev/null 2>&1
    if ! drv_drop "$drv_vol"; then
        err=$guard_error
        virsh define $vm_xml >/dev/null 2>&1 && virsh start $vm_ID >/dev/null 2>&1
        disk_fail "the old boot disk could not be removed: $err"
    fi
    if ! drv_rename "$new_img" "$drv_vol"; then
        # The old disk is gone: the new one is kept under its own name, for whoever repairs this
        err=$guard_error
        new_img=""
        disk_fail "$err"
    fi
    vol_state=attached
    shared_boot_report "$vol_ID" attached
else
    virsh undefine --nvram $vm_ID >/dev/null 2>&1
    mv -f "$new_img" "$vm_img" || disk_fail "failed to replace $vm_img"
    vol_state=attached
    echo "|:-COMMAND-:| create_volume_local '$vol_ID' '$disk_relpath' '$vol_state' 'success'"
fi

# rebuild metadata
./build_meta.sh "$vm_ID" "$vm_name" <<< $md >/dev/null 2>&1

[ -z "$vm_mem" ] && vm_mem='1024m'
[ -z "$vm_cpu" ] && vm_cpu=1
let vm_mem=${vm_mem%[m|M]}*1024

if [ -n "$boot_disk" ]; then
    : # set with the boot disk
elif [ "$pool" = "builtin" ]; then
    vm_nvram="$image_dir/${vm_ID}_VARS.fd"
else
    vm_nvram="$pool_root/nvram/${vm_ID}_VARS.fd"
fi
# Check if we need to switch boot mode
is_uefi_current=$(grep -c "loader.*type=.pflash" $vm_xml)
should_be_uefi=0
[ "$boot_loader" = "uefi" ] && should_be_uefi=1
if [ "$should_be_uefi" = "1" ] && [ "$is_uefi_current" = "1" ]; then
    mkdir -p "$(dirname $vm_nvram)"
    cp $nvram_template $vm_nvram
    sed -i "s#<nvram[^>]*>.*</nvram>#<nvram>$vm_nvram</nvram>#" $vm_xml
fi

if [ "$is_uefi_current" != "$should_be_uefi" ]; then

    log_debug $vm_ID "Switching boot mode to $boot_loader"

    if [ "$boot_loader" = "uefi" ]; then
        # Switch from BIOS to UEFI
        log_debug $vm_ID "Converting BIOS configuration to UEFI"

        # Create NVRAM file
        mkdir -p "$(dirname $vm_nvram)"
        cp $nvram_template $vm_nvram
        
        # Add UEFI loader and nvram to <os> section
        sed -i '/<\/os>/i\    <loader readonly='\''yes'\'' type='\''pflash'\''>'$uefi_boot_loader'</loader>' $vm_xml
        sed -i '/<\/os>/i\    <nvram>'$vm_nvram'</nvram>' $vm_xml
        
        # Change metadata disk from IDE to SCSI for UEFI
        if grep -q 'target.*dev=.hdd.*bus=.ide' $vm_xml && grep -q '/meta/' $vm_xml; then
            # Change metadata disk from IDE to SCSI and remove old address
            sed -i 's/dev=.hdd./dev="hdb"/g; s/bus=.ide./bus="scsi"/g' $vm_xml
            sed -i '/<target.*hdb.*scsi/,/<\/disk>/ { /<address type=.drive/d }' $vm_xml
            
            # Add SCSI controller if not exists (never remove IDE - other disks might use it)
            if ! grep -q 'controller.*scsi' $vm_xml; then
                sed -i '/<\/devices>/i\    <controller type="scsi" index="0" model="virtio-scsi"/>' $vm_xml
            fi
        fi
        
    else
        # Switch from UEFI to BIOS
        log_debug $vm_ID "Converting UEFI configuration to BIOS"

        # Remove UEFI loader and nvram from <os> section - use line-by-line approach
        # Remove loader lines
        sed -i '/^[[:space:]]*<loader.*type=.*pflash/d' $vm_xml
        sed -i '/^[[:space:]]*<\/loader>/d' $vm_xml  
        # Remove nvram lines, with or without attributes (libvirt writes <nvram template='...'>)
        sed -i '/^[[:space:]]*<nvram[ >\/]/d' $vm_xml
        # libvirt records the firmware it found for a UEFI loader (<os firmware='efi'> and the <firmware> features) and
        # fills the loader and the variables in again from it on define: drop that too, or the domain stays UEFI
        sed -i "s/<os firmware=['\"]efi['\"]>/<os>/" $vm_xml
        sed -i '/^[[:space:]]*<firmware>/,/^[[:space:]]*<\/firmware>/d' $vm_xml

        # Change metadata disk from SCSI to IDE for BIOS
        if grep -q 'target.*dev=.hdb.*bus=.scsi' $vm_xml && grep -q '/meta/' $vm_xml; then
            # Change metadata disk from SCSI to IDE and remove old address
            sed -i 's/dev=.hdb./dev="hdd"/g; s/bus=.scsi./bus="ide"/g' $vm_xml
            sed -i '/<target.*hdd.*ide/,/<\/disk>/ { /<address type=.pci/d }' $vm_xml
            
            # Add IDE controller if not exists (never remove SCSI - other disks might use it)  
            if ! grep -q 'controller.*ide' $vm_xml; then
                sed -i '/<\/devices>/i\    <controller type="ide" index="0"/>' $vm_xml
            fi
        fi
        
        # Clean up NVRAM file if exists
        rm -f "$vm_nvram"
    fi
fi

# Update basic parameters (memory, CPU, instance UUID)
sed_cmd="s#>.*</memory>#>$vm_mem</memory>#g; s#>.*</currentMemory>#>$vm_mem</currentMemory>#g; s#>.*</vcpu>#>$vm_cpu</vcpu>#g; s#\(<topology[^>]*\)cores='[0-9]*'#\1cores='$vm_cpu'#g"
# Add replacement for instance UUID in metadata
sed_cmd="$sed_cmd; s#<instance_id>.*</instance_id>#<instance_id>$instance_uuid</instance_id>#g"
sed -i "$sed_cmd" $vm_xml
# The disk of a shared pool as its driver describes it now
if [ -n "$boot_disk_xml" ] && ! xml_replace_disk $vm_xml vda "$boot_disk_xml"; then
    echo "|:-COMMAND-:| launch_vm.sh '$ID' '$state' '$NODE_ID' 'sync'"
    exit -1
fi
virsh define $vm_xml
virsh autostart $vm_ID --disable
virsh start $vm_ID
[ $? -eq 0 ] && state=running
echo "|:-COMMAND-:| launch_vm.sh '$ID' '$state' '$NODE_ID' 'sync'"

# check if the vm is windows and whether to change the rdp port
os_code=$(jq -r '.os_code' <<< $metadata)
if [ "$os_code" = "windows" ]; then
    rdp_port=$(jq -r '.login_port' <<< $metadata)
    if [ -n "$rdp_port" ] && [ "${rdp_port}" != "3389" ]  && [ ${rdp_port} -gt 0 ]; then
        # run the script to change the rdp port in background
        async_exec ./async_job/win_rdp_port.sh $ID $rdp_port
    fi
    async_exec ./async_job/win_primary_ip.sh $ID <<< $metadata
fi
