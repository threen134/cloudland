#!/bin/bash
# Apply what clapi decided about the instances this host reported with node_recovered (shared-storage-design.md
# §11.4). JSON on stdin: {"boot", "reason", "start": [ids], "stale": [{"id", "router"}]}
#   start: still this host's: held ones may start (from the pending start list, once their pools are usable)
#   stale: owned elsewhere now, or deleted: the definition goes (clear_stale_vm.sh), no disk is touched
# Instances in neither list stay as they are (a migration involving the host goes on, a delete was just sent); held
# ones stay held, and the host asks again every minute (reason "leave") until each is decided
# An answer to the request of an earlier boot only cleans up: what may start was decided for a boot that is over

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

input=$(cat)
boot=$(jq -r '.boot // empty' <<<"$input")
reason=$(jq -r '.reason // empty' <<<"$input")
current=$(cat /proc/sys/kernel/random/boot_id)

exec 7>$run_dir/node_reconcile.lock
flock -w 120 7 || die "another reconcile is running"

while read -r id router; do
    [[ "$id" =~ ^[0-9]+$ ]] && [[ "$router" =~ ^[0-9]+$ ]] || continue
    ./clear_stale_vm.sh "$id" "$router"
done < <(jq -r '.stale[]? | "\(.id) \(.router // 0)"' <<<"$input")

if [ "$boot" != "$current" ]; then
    log_debug node "reconcile answer of boot $boot ignored for starting: this is boot $current"
    exit 0
fi
for id in $(jq -r '.start[]? | numbers' <<<"$input"); do
    [[ "$id" =~ ^[0-9]+$ ]] || continue
    if reconcile_held $id; then
        reconcile_hold_remove $id
        # Started by the heartbeat from the pending start list, once its pools are usable
        [ "$(timeout 30 virsh domstate inst-$id 2>/dev/null)" = "running" ] || pending_start_add $id
    fi
done
# A held domain that is gone (its delete went through) is no longer waited for
if [ -f $reconcile_hold_file ]; then
    for id in $(cat $reconcile_hold_file); do
        [[ "$id" =~ ^[0-9]+$ ]] || continue
        timeout 30 virsh dominfo inst-$id >/dev/null 2>&1 || { reconcile_hold_remove $id; pending_start_remove $id; }
    done
fi
echo "$current" >$run_dir/reconciled_boot
if [ -s $reconcile_hold_file ]; then
    # Not decided yet: asked again once a minute (reconcile_check), until each one may start or goes
    echo "leave $(date +%s)" >$run_dir/reconcile_pending
else
    rm -f $run_dir/reconcile_pending
fi
echo "|:-COMMAND-:| node_reconciled '$NODE_ID' '$current' '$reason'"
