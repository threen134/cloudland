#!/bin/bash
# Remove the definition of an instance this host no longer owns (shared-storage-design.md §11.4): it was recovered on
# another host while this one was taken for dead, or deleted meanwhile. The domain is stopped and undefined, its
# security group chains, config drive and local UEFI variables go; no disk is touched, whichever pool it is in, and
# nothing in the database changes but the clean-up being reported: <vm_ID> <router>

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 2 ] && die "$0 <vm_ID> <router>"

ID=$1
router=$2
[[ "$ID" =~ ^[0-9]+$ ]] || die "invalid instance id $ID"
[[ "$router" =~ ^[0-9]+$ ]] || die "invalid router id $router"
vm_ID=inst-$ID
vm_rescue=$vm_ID-rescue

reconcile_hold_remove $ID
pending_start_remove $ID
vm_xml=$(timeout 30 virsh dumpxml $vm_ID 2>/dev/null)
nvram=$(echo "$vm_xml" | xmllint --xpath 'string(/domain/os/nvram)' - 2>/dev/null)
./generate_vm_instance_map.sh remove $vm_ID
./generate_vm_instance_map.sh remove $vm_rescue

# A rescue domain first: it may have the shared boot disk attached too. Its own disk is local and goes with it
timeout 60 virsh destroy $vm_rescue >/dev/null 2>&1
timeout 60 virsh undefine --nvram $vm_rescue >/dev/null 2>&1 || timeout 60 virsh undefine $vm_rescue >/dev/null 2>&1
rm -f $xml_dir/$vm_ID/*rescue* ${cache_dir}/meta/${vm_ID}-rescue.iso ${image_dir}/${vm_rescue}.* ${image_dir}/${vm_rescue}_VARS.fd

timeout 60 virsh destroy $vm_ID >/dev/null 2>&1
# UEFI variables in a shared pool belong to the copy running elsewhere now: kept. Local ones go
timeout 60 virsh undefine $(nvram_undefine_flag "$nvram") $vm_ID >/dev/null 2>&1 || timeout 60 virsh undefine $vm_ID >/dev/null 2>&1
state=done
if timeout 30 virsh dominfo $vm_ID >/dev/null 2>&1; then
    state=error
    log_debug $ID "the stale definition of instance $ID could not be removed"
fi

count=$(echo "$vm_xml" | xmllint --xpath 'count(/domain/devices/interface)' - 2>/dev/null)
for (( i=1; i <= ${count:-0}; i++ )); do
    vif_dev=$(echo "$vm_xml" | xmllint --xpath "string(/domain/devices/interface[$i]/target/@dev)" - 2>/dev/null)
    [ -z "$vif_dev" ] && continue
    ./clear_sg_chain.sh $vif_dev
    rm -f "$async_job_dir/$vif_dev"
done
if [ "$state" = "done" ]; then
    rm -f ${image_dir}/${vm_ID}_VARS.fd ${cache_dir}/meta/${vm_ID}.iso
    rm -rf $xml_dir/$vm_ID
    ./clear_local_router.sh $router $ID
fi
echo "|:-COMMAND-:| $(basename $0) '$ID' '$state'"
