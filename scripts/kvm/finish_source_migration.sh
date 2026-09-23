#!/bin/bash

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 7 ] && die "$0 <migration_ID> <task_ID> <vm_ID> <router> <target_hyper> <migration_type> <target_hostid>"

migration_ID=$1
task_ID=$2
ID=$3
vm_ID=inst-$ID
router=$4
target_hyper=$5
migration_type=$6
# The disk plan on stdin: its source paths are the files to delete here
plan=$(cat)
state=failed

vm_xml=$(cat $xml_dir/$vm_ID/$vm_ID.xml)
# After a migration this host must not have the instance any more (--undefinesource removed it). If it is still
# defined, a rollback defined and started it again: never destroy it nor delete its disks, they are the only copy.
# The check comes before any destroy / undefine.
if virsh domstate $vm_ID >/dev/null 2>&1; then
    log_debug $ID "finish_source_migration.sh: vm still defined on source, skip cleanup"
    exit 0
fi
pending_start_remove $ID
src_nvram=$(echo $vm_xml | xmllint --xpath 'string(/domain/os/nvram)' - 2>/dev/null)

# Delete a file of a pool of this host: <path>. A pool file is only deleted with the pool mounted and checked.
function delete_disk()
{
    local path=$1 pool
    case "$path" in
        $pools_dir/*)
            pool=${path#$pools_dir/}
            pool=${pool%%/*}
            ;;
        $cache_dir/*)
            pool=builtin
            ;;
        *)
            return
            ;;
    esac
    if pool_enter "$pool" "$NODE_ID" "$path"; then
        rm -f "$path"
    else
        log_debug $ID "finish_source_migration.sh: $path kept: $guard_error"
    fi
    exec 8>&-
}

ndisk=$(jq length <<<"$plan" 2>/dev/null)
if [ -n "$ndisk" ] && [ "$ndisk" -gt 0 ]; then
    for path in $(jq -r '.[].src_path' <<<"$plan"); do
        delete_disk $path
    done
else
    # Migrations made before the disk plans: the file disks of the definition
    for path in $(echo $vm_xml | xmllint --xpath "/domain/devices/disk[@device='disk']/source/@file" - 2>/dev/null | sed 's/file="\([^"]*\)"/\1/g'); do
        delete_disk $path
    done
fi
[ -n "$src_nvram" ] && delete_disk $src_nvram

count=$(echo $vm_xml | xmllint --xpath 'count(/domain/devices/interface)' -)
for (( i=1; i <= $count; i++ )); do
    vif_dev=$(echo $vm_xml | xmllint --xpath "string(/domain/devices/interface[$i]/target/@dev)" -)
    ./clear_sg_chain.sh $vif_dev >/dev/null 2>&1
done
./clear_local_router.sh $router >/dev/null 2>&1

./generate_vm_instance_map.sh remove $vm_ID >/dev/null 2>&1

rm -f ${cache_dir}/meta/${vm_ID}.iso
rm -rf $xml_dir/$vm_ID

# Custom metrics of the instance stay with its old host otherwise
./cleanup_vm_custom_metrics.sh $ID >/dev/null 2>&1
exit 0
