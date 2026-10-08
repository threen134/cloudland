#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 3 ] && die "$0 <vm_ID> <user> <passwd>"

ID=$1
vm_ID=inst-$ID
username=$2
passwd=$3
vm_rescue=$(virsh list --all | grep "\<$vm_ID-" | awk '{print $2}')
[ -n "$vm_rescue" ] && vm_ID=$vm_rescue
if ! err=$(virsh set-user-password --domain "$vm_ID" --user "$username" --password "$passwd" 2>&1); then
    # clapi answered the request before the guest was asked: the callback is how the failure gets to the instance.
    # The guest agent is often not up yet shortly after the first boot (it may still be installing).
    reason=failed
    grep -qiE "agent is not (responding|connected|available)|agent unavailable" <<< "$err" && reason=agent_unavailable
    log_debug $vm_ID "set_user_passwd.sh: failed to set the password of $username: $err"
    echo "|:-COMMAND-:| $(basename $0) '$ID' 'error' '$reason'"
    exit 1
fi
echo "|:-COMMAND-:| $(basename $0) '$ID' 'success'"
