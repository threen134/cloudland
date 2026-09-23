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
target_hostid=$7
# The disk plan of clapi on stdin: where every disk of the instance goes on the target (§7 of the storage plan)
plan=$(cat)
state=failed
prepared=0
ssh_opts="-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new"
target_uri=qemu+ssh://$target_hyper/system
work_dir=$run_dir/migration-$migration_ID
mkdir -p $work_dir

log_debug $ID "source_migration.sh: Starting migration_ID=$migration_ID, task_ID=$task_ID, target_hyper=$target_hyper, migration_type=$migration_type"

function report()
{
    echo "|:-COMMAND-:| migrate_vm.sh '$migration_ID' '$task_ID' '$ID' '$NODE_ID' '$1' '${2//\'/}'"
}

# Remove what this migration created on the target; nothing is removed once the domain is defined there
function clear_target_disks()
{
    [ "$prepared" = "1" ] || return
    ssh $ssh_opts $target_hyper /opt/cloudland/scripts/backend/prepare_migration_disks.sh cleanup $migration_ID $target_hostid $ID </dev/null >/dev/null 2>&1
}

function fail()
{
    clear_target_disks
    rm -rf $work_dir
    report $state "$1"
    exit 0
}

pending_start_remove $ID
log_debug $ID "source_migration.sh: Dumping XML for $vm_ID"
virsh dumpxml $vm_ID >$xml_dir/$vm_ID/${vm_ID}.xml
if [ "$migration_type" != "warm" ]; then
    rm -rf $work_dir
    report not_supported "cold migration requires shared storage"
    exit 0
fi

state='source_rollback'
vm_state=$(virsh domstate $vm_ID)
old_state=$vm_state
# Check ssh and record the host key of the target: virsh qemu+ssh does not accept new host keys by itself
if ! ssh -n $ssh_opts $target_hyper true; then
    log_debug $ID "source_migration.sh: ssh to $target_hyper failed"
    fail "ssh to target host failed"
fi
ndisk=$(jq length <<<"$plan" 2>/dev/null)
if [ -z "$ndisk" ] || [ "$ndisk" -eq 0 ]; then
    fail "empty disk plan"
fi
# The disks come from the plan, each must be a disk of the domain
blklist=$(virsh domblklist $vm_ID --details | awk '$1 == "file" && $2 == "disk" {print $3, $4}')
migrate_disks=""
prep="[]"
moved=""
for (( i=0; i < ndisk; i++ )); do
    read -r src dst dst_pool < <(jq -r ".[$i] | \"\(.src_path) \(.dst_path) \(.dst_pool_uuid)\"" <<<"$plan")
    dev=$(awk -v p="$src" '$2 == p {print $1}' <<<"$blklist")
    [ -z "$dev" ] && fail "disk $src of the plan is not a disk of the instance"
    info=$(qemu-img info -U --output=json "$src" 2>/dev/null)
    [ -z "$info" ] && fail "can not read disk $src"
    vsize=$(jq -r '."virtual-size"' <<<"$info")
    cluster=$(jq -r '."cluster-size" // 65536' <<<"$info")
    actual=$(jq -r '."actual-size" // 0' <<<"$info")
    prep=$(jq -c --arg d "$dst" --arg p "$dst_pool" --argjson v "$vsize" --argjson c "$cluster" --argjson a "$actual" \
        '. + [{dst_path: $d, dst_pool: $p, vsize: $v, cluster: $c, actual: $a}]' <<<"$prep")
    migrate_disks="$migrate_disks,$dev"
    [ "$src" != "$dst" ] && moved="$moved $src=$dst"
done

# On the target: check the pools and the free space, refuse to overwrite a file, create the empty images of a live copy
prep_mode=live
[ "$vm_state" = "shut off" ] && prep_mode=offline
prepared=1
result=$(ssh $ssh_opts $target_hyper /opt/cloudland/scripts/backend/prepare_migration_disks.sh prepare $migration_ID $target_hostid $prep_mode <<<"$prep" 2>/dev/null | tail -1)
[ "$result" = "OK" ] || fail "${result#ERROR }"
if [ "$vm_state" = "shut off" ]; then
    # rsync --sparse keeps the holes of the images: scp would write them out and fill the space discard gave back
    for (( i=0; i < ndisk; i++ )); do
        read -r src dst < <(jq -r ".[$i] | \"\(.src_path) \(.dst_path)\"" <<<"$plan")
        rsync -S -e "ssh $ssh_opts" "$src" "$target_hyper:$dst" >/dev/null 2>&1 || fail "failed to copy disk $src to the target"
    done
fi

# NVRAM of a UEFI instance (boot entries, Secure Boot state): the target only has an empty template.
# It stays next to the boot disk: $image_dir for the builtin pool, nvram/ of any other pool.
src_nvram=$(xmllint --xpath 'string(/domain/os/nvram)' $xml_dir/$vm_ID/${vm_ID}.xml 2>/dev/null)
dst_nvram=$src_nvram
boot_pool=$(jq -r '.[] | select(.booting) | .dst_pool_uuid' <<<"$plan")
boot_root=$(jq -r '.[] | select(.booting) | .dst_pool_root' <<<"$plan")
if [ -n "$src_nvram" ] && [ -n "$boot_pool" ]; then
    if [ "$boot_pool" = "builtin" ]; then
        dst_nvram=$image_dir/${vm_ID}_VARS.fd
    else
        dst_nvram=$boot_root/nvram/${vm_ID}_VARS.fd
    fi
    [ "$src_nvram" != "$dst_nvram" ] && moved="$moved $src_nvram=$dst_nvram"
fi
if [ -f "$src_nvram" ]; then
    scp -q $ssh_opts $src_nvram $target_hyper:$dst_nvram || fail "failed to copy the NVRAM to the target"
fi

# Disks changing pool (L2): the definition on the target points at the new paths
xml_opts=""
if [ -n "$moved" ]; then
    new_xml=$work_dir/$vm_ID.xml
    virsh dumpxml --security-info --migratable $vm_ID >$new_xml || fail "failed to dump the definition"
    for m in $moved; do
        sed -i "s#'${m%%=*}'#'${m#*=}'#g; s#>${m%%=*}<#>${m#*=}<#g" $new_xml
    done
    if which virt-xml-validate >/dev/null 2>&1 && ! virt-xml-validate $new_xml domain >/dev/null 2>&1; then
        fail "the rewritten definition is not valid"
    fi
    # An offline migration ignores --persistent-xml (libvirt 12): the rewritten definition is defined on the
    # target after the migration instead
    if [ "$vm_state" != "shut off" ]; then
        xml_opts="--xml $new_xml --persistent-xml $new_xml"
    fi
fi
# Descriptions of the data disks, used to detach them later; rewritten for the disks changing pool
for vol_xml in $xml_dir/$vm_ID/disk-*.xml; do
    [ -f "$vol_xml" ] || continue
    [[ "$(basename $vol_xml)" == *-rescue* ]] && continue
    copy=$work_dir/$(basename $vol_xml)
    cp $vol_xml $copy
    for m in $moved; do
        sed -i "s#'${m%%=*}'#'${m#*=}'#g" $copy
    done
    scp -q $ssh_opts $copy $target_hyper:$xml_dir/$vm_ID/ || fail "failed to copy $(basename $vol_xml) to the target"
done

# VPC network warm-up, effective the moment the instance switches (virsh migrate returns over a second after the
# instance runs on the target, switching after it would cut the network that much longer):
# - this host points the MAC of the instance at the target: while the instance is here the bridge forwards through
#   its tap; once the tap is gone the bridge floods to the VXLAN port and this entry sends the traffic to the target
#   (instances of the same VPC here, return traffic of floating IPs and SNAT on the router here)
# - the target points the gateway MAC of the router here at this host: after the switch the instance still sends
#   outbound traffic to this router by its ARP cache; floating IP and SNAT addresses do not change, and clapi
#   announces the gateway of the target once it rebuilt the floating IPs there after completed
#   (forwarding entry only, no neighbour entry: the VXLAN ARP proxy would answer with it and give other instances
#   of the target the wrong gateway MAC)
prewarmed=""
vm_xml_file=$xml_dir/$vm_ID/${vm_ID}.xml
vtep_ip=$(ifconfig $vxlan_interface 2>/dev/null | grep 'inet ' | awk '{print $2}')
if [[ "$target_hyper" =~ ^[0-9.]+$ ]]; then
    count=$(xmllint --xpath 'count(/domain/devices/interface)' $vm_xml_file 2>/dev/null)
    for (( i=1; i <= ${count:-0}; i++ )); do
        mac=$(xmllint --xpath "string(/domain/devices/interface[$i]/mac/@address)" $vm_xml_file)
        vni=$(xmllint --xpath "string(/domain/devices/interface[$i]/source/@bridge)" $vm_xml_file)
        vni=${vni#br}
        [[ "$vni" =~ ^[0-9]+$ ]] && [ "$vni" -ge 4095 ] && ip link show v-$vni >/dev/null 2>&1 || continue
        bridge fdb replace $mac dev v-$vni dst $target_hyper self permanent >/dev/null 2>&1 && prewarmed="$prewarmed $mac/$vni"
        gw_mac=$(ip netns exec router-$router cat /sys/class/net/ns-$vni/address 2>/dev/null)
        [ -n "$gw_mac" ] && [ -n "$vtep_ip" ] && \
            ssh -n $ssh_opts $target_hyper "bridge fdb replace $gw_mac dev v-$vni dst $vtep_ip self permanent" >/dev/null 2>&1
    done
fi
# Progress: domjobinfo every 3 seconds in the background (stdout is forwarded at once, also while virsh migrate blocks)
(
    while sleep 3; do
        stats=$(virsh domjobinfo $vm_ID --rawstats 2>/dev/null)
        total=$(awk '/^data_total:/ {print $2}' <<<"$stats")
        processed=$(awk '/^data_processed:/ {print $2}' <<<"$stats")
        [[ "$total" =~ ^[0-9]+$ ]] && [[ "$processed" =~ ^[0-9]+$ ]] && [ "$total" -gt 0 ] || continue
        echo "|:-COMMAND-:| migrate_progress.sh '$migration_ID' '$ID' '$(( processed * 100 / total ))' '$processed' '$total'"
    done
) &
progress_pid=$!
if [ "$vm_state" = "shut off" ]; then
    log_debug $ID "source_migration.sh: Starting offline migration to $target_hyper"
    virsh migrate --undefinesource --persistent --offline $xml_opts $vm_ID $target_uri
else
    # No --suspend: QEMU resumes on the target right after the switch (libvirt pauses the source before it)
    log_debug $ID "source_migration.sh: Starting live migration with local storage to $target_hyper, disks ${migrate_disks#,}"
    virsh migrate --undefinesource --persistent --live --copy-storage-all --migrate-disks ${migrate_disks#,} \
        --migrateuri tcp://$target_hyper --disks-uri tcp://$target_hyper $xml_opts $vm_ID $target_uri
fi
migrate_rc=$?
kill $progress_pid 2>/dev/null
if [ $migrate_rc -ne 0 ]; then
    log_debug $ID "source_migration.sh: virsh migrate failed with non-zero exit code"
    # The instance is still here: drop the VXLAN entries set up for the switch
    for p in $prewarmed; do
        bridge fdb del ${p%/*} dev v-${p#*/} self >/dev/null 2>&1
    done
    fail "virsh migrate returns non-zero"
fi
if [ -n "$moved" ] && [ "$vm_state" = "shut off" ]; then
    if ! scp -q $ssh_opts $new_xml $target_hyper:$run_dir/migration-$migration_ID.xml ||
        ! ssh -n $ssh_opts $target_hyper "virsh define $run_dir/migration-$migration_ID.xml >/dev/null && rm -f $run_dir/migration-$migration_ID.xml"; then
        report $state "failed to define the instance with its new disk paths on the target"
        exit 0
    fi
fi
# Let the target drop old entries pointing the MAC of the instance at other hosts (when it already had instances of
# the VPC). The gateway is not announced here: the floating IPs of the target are rebuilt only after completed, and
# announcing earlier would SNAT outbound traffic to the host address and break existing connections.
ssh -n $ssh_opts $target_hyper /opt/cloudland/scripts/backend/post_migration_net.sh $ID >/dev/null 2>&1 &
# The NVRAM template of target_migration.sh in the builtin pool is not used when the NVRAM went to another pool
if [ -n "$dst_nvram" ] && [ "$dst_nvram" != "$image_dir/${vm_ID}_VARS.fd" ]; then
    ssh -n $ssh_opts $target_hyper rm -f $image_dir/${vm_ID}_VARS.fd >/dev/null 2>&1
fi
log_debug $ID "source_migration.sh: virsh migrate command completed, waiting for VM to disappear from source"
for i in {1..60}; do
    vm_state=$(virsh domstate $vm_ID 2>/dev/null)
    if [ -z "$vm_state" ]; then
        # Fallback: resume on the target only when it ran before and is still paused there
        if [ "$old_state" = "running" ] && [ "$(ssh -n $ssh_opts $target_hyper virsh domstate $vm_ID 2>/dev/null | head -1)" = "paused" ]; then
            ssh -n $ssh_opts $target_hyper virsh resume $vm_ID >/dev/null
            if [ $? -ne 0 ]; then
                log_debug $ID "source_migration.sh: failed to resume vm on the target host"
                report $state "failed to resume vm on target host"
                exit 0
            fi
            log_debug $ID "source_migration.sh: vm $vm_ID on target host resumed"
        fi
        break
    fi
    sleep 0.5
done
rm -rf $work_dir
if [ -n "$vm_state" ]; then
    log_debug $ID "source_migration.sh: VM still exists after 60 seconds wait"
    report $state "vm remains after virsh migrate"
    exit 0
fi
log_debug $ID "source_migration.sh: VM successfully removed from source"

state="source_prepared"
log_debug $ID "source_migration.sh: Migration preparation completed, reporting state=$state"
report $state ""
