#!/bin/bash

base_dir=$(dirname $0)
cd $base_dir
script_dir=$(pwd)
source ../cloudrc
source ./storage_lib.sh

exec <&-

cpu=0
total_cpu=$(cat /proc/cpuinfo | grep -c processor)
memory=0
if [ -z "$system_reserved_memory" ]; then
    let system_reserved_memory=$(cat /proc/meminfo | grep MemTotal | awk '{print $2}')/4
    [ $system_reserved_memory -gt 64000000 ] && system_reserved_memory=64000000
fi
total_memory=$(( $(free | grep 'Mem:' | awk '{print $2}') - $system_reserved_memory ))
disk=0
total_disk=0
network=0
total_network=0
load=$(w | head -1 | cut -d',' -f5 | cut -d'.' -f1 | xargs)
total_load=0
vtep_ip=$(ifconfig $vxlan_interface | grep 'inet ' | awk '{print $2}')

function probe_arp()
{
    cd /opt/cloudland/cache/router
    for router in *; do
        ID=${router##router-}
        ext_ips=$(sudo ip netns exec $router ip addr show te-$ID | grep 'inet ' | awk '{print $2}')
        ext_mac=$(sudo ip netns exec $router ip -o link show te-$ID | awk '{print $17}')
        for ip in $ext_ips; do
            sudo ip netns exec $router arping -c 1 -I te-$ID ${ip%%/*}
        done
    done
    cd -
}

function daily_job()
{
    daily_state_file=$run_dir/daily_state_file
    current_date=$(date +%Y%m%d)
    if [ -f "$daily_state_file" ]; then
        last_run_date=$(cat $daily_state_file)
    fi
    if [ "$last_run_date" != "$current_date" ]; then
        sudo $base_dir/operation/cleanup_outdated_iptables.sh >>$log_dir/iptables_cleanup.log 2>&1
        echo "$current_date" >$daily_state_file
    fi
}

function halfday_job()
{
    local state_file="$run_dir/halfday_state_file"
    local current_halfday=$(date +"%Y%m%d-%p")  # e.g., 20250807-AM or 20250807-PM

    if [[ -f "$state_file" ]]; then
        local last_halfday=$(< "$state_file")
        [[ "$last_halfday" == "$current_halfday" ]] && return
    fi

    ./generate_vm_instance_map.sh full >/dev/null
    echo "$current_halfday" > "$state_file"
}

# True when every disk of a paused domain that failed ran out of space in a local pool (§6.1 of the storage plan).
# An instance paused for another I/O error, or by its user, is not one of these.
function paused_nospace()
{
    local dom=$1 errs dev err src
    # Every virsh call here has a timeout: this runs in the heartbeat, and a pool that filled up is exactly when
    # libvirtd can hang. A blocked heartbeat has the host reported offline (see the WDS wait this replaced)
    timeout 10 sudo virsh domstate --reason $dom 2>/dev/null | grep -qi "i/o error" || return 1
    errs=$(timeout 10 sudo virsh domblkerror $dom 2>/dev/null)
    [ -z "$errs" ] && return 1
    grep -qi "no errors" <<<"$errs" && return 1
    while read dev err; do
        dev=${dev%:}
        [ -z "$dev" ] && continue
        grep -qi "no space" <<<"$err" || return 1
        src=$(timeout 10 sudo virsh domblklist $dom --details 2>/dev/null | awk -v d=$dev '$3 == d {print $4}')
        case "$src" in
            $pools_dir/*|$cache_dir/*) ;;
            *) return 1 ;;
        esac
    done <<<"$errs"
    return 0
}

function inst_status()
{
    inst_list_file=$image_dir/old_inst_list
    old_state_time=$(stat -c %W $inst_list_file)
    [ -z "$old_state_time" ] && old_state_time=0
    current_time=$(date +"%s")
    [ $(( $current_time - $old_state_time )) -gt 600 ] && rm -f $inst_list_file
    old_inst_list=$(cat $inst_list_file 2>/dev/null)
    all_inst_list=$(sudo virsh list --all | tail -n +3 | cut -d' ' -f3-)
    shutoff_list=$(sudo virsh list --all | grep 'shut off' | awk '{print $2}')
    for inst in $shutoff_list; do
        echo "$all_inst_list" | grep -q $inst-rescue
	[ $? -eq 0 ] && all_inst_list=$(echo "$all_inst_list" | grep -v $inst-rescue | sed "s/$inst.*shut off/$inst rescuing/")
    done
    n=0
    export inst_list=""
    all_inst_list=$(echo "$all_inst_list" | sed 's/inst-//g;s/-rescue//g;s/shut off/shut_off/g')
    # One "<id> <state>" per line; states only the storage code knows: paused_nospace, pending_storage
    pending_list=$cache_dir/pending_start
    all_inst_list=$(while read id st; do
        [ -z "$id" ] && continue
        if [ "$st" = "paused" ] && paused_nospace inst-$id; then
            st=paused_nospace
        elif [ "$st" = "shut_off" ] && grep -qx "$id" $pending_list 2>/dev/null; then
            # Waiting for a pool, or its pools are fine and libvirt refused to start it
            if [ -f $run_dir/start_failed-$id ] && instance_pools_ok $id; then
                st=start_failed
            else
                st=pending_storage
            fi
        fi
        echo "$id $st"
    done <<<"$all_inst_list")
    while read line; do
        [ -z "$line" ] && continue
        # Whole-line match: "5 paused" must not hide "5 paused_nospace", nor "15 running" hide "5 running"
        grep -qxF "$line" <<<"$old_inst_list"
        [ $? -eq 0 ] && continue
        inst_list="$line $inst_list"
        if [ $n -eq 10 ]; then
            n=0
            inst_list=$(echo $inst_list)
            echo "|:-COMMAND-:| inst_status.sh '$NODE_ID' '$inst_list'"
            inst_list=""
        fi
        let n=$n+1
    done <<<$all_inst_list
    echo "$all_inst_list" >$inst_list_file
    inst_list=$(echo $inst_list)
    [ -n "$inst_list" ] && echo "|:-COMMAND-:| inst_status.sh '$NODE_ID' '$inst_list'"
}

function vlan_status()
{
    cd /opt/cloudland/cache/dnsmasq
    old_vlan_list=$(cat old_vlan_list 2>/dev/null)
    vlan_list=$(ls | grep vlan | grep -v old_vlan_list | xargs | sed 's/vlan//g')
    [ "$vlan_list" = "$old_vlan_list" ] && return
    vlan_arr=($vlan_list)
    nlist=$(ip netns list | grep vlan | cut -d' ' -f1 | xargs | sed 's/vlan//g')
    vlan_status_list=""
    for var in ${vlan_arr[*]}; do
        status="INACTIVE"
        [[ $nlist =~ $var ]] && status="ACTIVE"
        first=""
        [[ -d "vlan$var" ]] && [[ -f "vlan$var/vlan$var.FIRST" ]] && first="FIRST"
        second=""
        [[ -d "vlan$var" ]] && [[ -f "vlan$var/vlan$var.SECOND" ]] && second="SECOND"
        vlan_status_list="$vlan_status_list $var:$status:$first:$second"
    done
    vlan_status_list=$(echo $vlan_status_list | sed -e 's/^[ ]*//g')
    [ -n "$vlan_status_list" ] && echo "|:-COMMAND-:| vlan_status.sh '$NODE_ID' '$vlan_status_list'"
    echo "$vlan_list" >old_vlan_list
}

function router_status()
{
    cd /opt/cloudland/cache/router
    old_router_list=$(cat old_router_list 2>/dev/null)
    router_list=$(ls router* 2>/dev/null)
    router_list=$(echo "$router_list $(sudo ip netns list | grep router | cut -d' ' -f1)" | xargs | sed 's/router-//g')
    [ "$router_list" = "$old_router_list" ] && return
    [ -n "$router_list" ] && echo "|:-COMMAND-:| router_status.sh '$NODE_ID' '$router_list'"
    echo "$router_list" >old_router_list
}

function check_system_router()
{
    sudo systemctl status NetworkManager >/dev/null
    [ $? -ne 0 ] && sudo systemctl restart NetworkManager
    # 只看退出码：本脚本 stdout 除首行外都会作为回调命令发给 clapi，不能输出其他内容
    sudo ip netns exec router-0 ip r | grep -q default
    if [ $? -ne 0 ]; then
        # 目录只由 cloudrc 的延迟日志按需创建，新节点上可能不存在；缺了它任务写不出去，router-0 永远不会重建
        sudo mkdir -p $async_job_dir
        sudo -E bash -c "echo '|:-COMMAND-:|' system_router.sh \'$NODE_ID\' \'$HOSTNAME\' >$async_job_dir/system_router.done"
    fi
}

function check_conntrack()
{
    inst_list_file=$image_dir/old_inst_list
    [ -z "$syn_threshold_src_dst" ] && syn_threshold_src_dst=1500
    [ -z "$syn_threshold_src" ] && syn_threshold_src=3000
    [ -z "$syn_threshold_dst" ] && syn_threshold_dst=5000
    [ -z "$base_conn_num" ] && base_conn_num=1000000
    inst_num=$(wc -l <$inst_list_file)
    if [ "$inst_num" -gt 0 ]; then
        syn_threshold_dst=$(($base_conn_num/$inst_num))
        [ "$syn_threshold_dst" -lt 4000 ] && syn_threshold_dst=4000
        [ "$syn_threshold_dst" -gt 20000 ] && syn_threshold_dst=20000
        syn_threshold_src=$(($syn_threshold_dst * 3 / 5))
        syn_threshold_src_dst=$(($syn_threshold_src/2))
    fi
    sudo $base_dir/operation/check_halfopen_connections.sh $syn_threshold_src_dst $syn_threshold_src $syn_threshold_dst
}

function recover_loadbalancer()
{
    # 与 sync_instance 一样按 boot_id 判断：run_dir 在磁盘上，原先只 touch 标记文件，
    # 节点重启后标记仍在，负载均衡从不重建（只在节点首次部署时触发过一次）
    lb_flag_file=$run_dir/need_to_sync_lb
    boot_file=/proc/sys/kernel/random/boot_id
    diff $lb_flag_file $boot_file >/dev/null 2>&1 && return
    echo "|:-COMMAND-:| recover_loadbalancer.sh '$NODE_ID'"
    sudo cp $boot_file $lb_flag_file
}

function check_lb_process()
{
    # 运行中异常退出的 keepalived / haproxy 由心跳拉起；输出不能进 stdout（会被当作回调）
    sudo bash $base_dir/check_lb_process.sh >/dev/null 2>&1
}

function recover_vpn_gateway()
{
    # Same boot_id logic as recover_loadbalancer: after a reboot clapi re-pushes every VPN gateway of
    # which this node is a VRRP member. Nodes that only host instances of such a VPC get their routes
    # back through the launch_vm sync callbacks.
    vpn_flag_file=$run_dir/need_to_sync_vpn
    boot_file=/proc/sys/kernel/random/boot_id
    diff $vpn_flag_file $boot_file >/dev/null 2>&1 && return
    echo "|:-COMMAND-:| recover_vpn_gateway.sh '$NODE_ID'"
    sudo cp $boot_file $vpn_flag_file
}

function check_vpn_process()
{
    # charon / FRR watchdog in both directions (start on the floating IP holder, stop elsewhere)
    sudo -E bash $base_dir/check_vpn_process.sh >/dev/null 2>&1
}

function report_vpn_status()
{
    # Master identity, tunnel, client and BGP state; only |:-COMMAND-:| lines reach stdout.
    # -E keeps NODE_ID: the callbacks carry it and clapi rejects them without it
    sudo -E bash $base_dir/report_vpn_status.sh 2>/dev/null
}

function report_lb_health()
{
    # Backend health check results of the load balancers this node is master of; prints callback lines only
    sudo bash $base_dir/report_lb_health.sh 2>/dev/null
}

function sync_instance()
{
    flag_file=$run_dir/need_to_sync
    boot_file=/proc/sys/kernel/random/boot_id
    diff $flag_file $boot_file >/dev/null 2>&1
    [ $? -eq 0 ] && return
    sudo iptables-restore </etc/iptables.rules
    bridges=$(cat /proc/net/dev | grep br | awk -F: '{print $1}')
    sudo iptables -N secgroup-chain && sudo iptables -A secgroup-chain -j ACCEPT
    for bridge in $bridges; do
	# 追加到链尾：网桥内部放行必须排在安全组跳转之后（apply_fw 把安全组跳转插在 FORWARD 第 3 条），
	# 插在前面会让该网桥上所有虚拟机的安全组失效；下面会把兜底 REJECT 重新挪到最后
	sudo iptables -C FORWARD -i $bridge -o $bridge -j ACCEPT
	[ $? -ne 0 ] && sudo iptables -A FORWARD -i $bridge -o $bridge -j ACCEPT
    done
    sudo iptables -D FORWARD -j REJECT --reject-with icmp-host-prohibited
    sudo iptables -A FORWARD -j REJECT --reject-with icmp-host-prohibited
    insts=$(ls $xml_dir)
    for inst in $insts; do
        inst_id=${inst/inst-/}
        [[ "$inst_id" =~ ^[0-9]+$ ]] || continue
        # libvirtd would start an autostart domain at boot before its pools are checked
        sudo virsh autostart inst-$inst_id --disable >/dev/null 2>&1
        if [ "$(sudo virsh domstate inst-$inst_id 2>/dev/null)" = "running" ]; then
            echo "|:-COMMAND-:| launch_vm.sh '$inst_id' 'running' '$NODE_ID' 'sync'"
        elif ! instance_pools_ok $inst_id; then
            # Waiting in the heartbeat for a pool would get the host taken offline: start it later
            pending_start_add $inst_id
        elif try_start_instance $inst_id; then
            echo "|:-COMMAND-:| launch_vm.sh '$inst_id' 'running' '$NODE_ID' 'sync'"
        else
            # Its pools are fine and libvirt still refused: reported as start_failed, retried from the list with a back-off
            pending_start_add $inst_id
        fi
    done
    sudo cp $boot_file $flag_file
}

function boot_time()
{
    date -d "$(uptime -s)" +%s 2>/dev/null || echo 0
}

# A pool is usable when its probe found it ready or degraded since this boot; the builtin pool always is
function pool_state_ok()
{
    local st=$pool_state_dir/$1.state status ts
    [ "$1" = "builtin" ] && return 0
    [ -f $st ] || return 1
    read status ts < <(jq -r '"\(.status) \(.ts)"' $st 2>/dev/null)
    [ "$status" = "ready" -o "$status" = "degraded" ] || return 1
    [ "${ts:-0}" -ge "$(boot_time)" ]
}

# Pools holding the disks of an instance, from its definition: <instance id>
function instance_pools()
{
    local xml=$xml_dir/inst-$1/inst-$1.xml src p
    for src in $(xmllint --xpath '//devices/disk[@device="disk"]/source/@file' $xml 2>/dev/null | grep -o '"[^"]*"' | tr -d '"'); do
        case "$src" in
            $pools_dir/*) p=${src#$pools_dir/}; echo ${p%%/*} ;;
            *) echo builtin ;;
        esac
    done | sort -u
}

function instance_pools_ok()
{
    local p
    for p in $(instance_pools $1); do
        pool_state_ok $p || return 1
    done
    return 0
}

function pending_start_add()
{
    local list=$cache_dir/pending_start
    grep -qx "$1" $list 2>/dev/null || echo "$1" >>$list
}

# Start the instances that wait for their pools, in the background (§4.8 of the storage plan)
function pending_start()
{
    local list=$cache_dir/pending_start id st last
    [ -s $list ] || return
    for id in $(cat $list); do
        st=$(sudo virsh domstate inst-$id 2>/dev/null)
        # Gone, or started by hand
        if [ -z "$st" ] || [ "$st" = "running" ]; then
            pending_start_remove $id
            continue
        fi
        instance_pools_ok $id || continue
        # A start that failed with its pools fine is retried every 5 minutes, not every heartbeat
        last=$(cat $run_dir/start_attempt-$id 2>/dev/null)
        [ $(( $(date +%s) - ${last:-0} )) -ge 300 ] || continue
        async_exec $script_dir/async_job/start_pending.sh $id
    done
}

# Keep one background probe per pool running and report the pools (§4.7 of the storage plan).
# The heartbeat never touches the pool disks itself: a broken disk can block any command on it.
function pool_report()
{
    local now=$(date +%s) pool st pid_file stuck ts json status reason bucket sig last_ts last_sig
    mkdir -p $pool_state_dir
    for pool in builtin $(local_pools); do
        st=$pool_state_dir/$pool.state
        pid_file=$pool_state_dir/$pool.pid
        stuck=0
        if probe_alive "$(cat $pid_file 2>/dev/null)" $pool; then
            [ $((now - $(stat -c %Y $pid_file))) -gt 300 ] && stuck=1
        else
            ts=$(jq -r '.ts // 0' $st 2>/dev/null)
            if [ $((now - ${ts:-0})) -ge 60 ]; then
                setsid $script_dir/pool_probe.sh $pool </dev/null >/dev/null 2>&1 &
                echo $! >$pid_file
            fi
        fi
        if [ $stuck -eq 1 ]; then
            json='{}'
            status=unavailable
            reason="probe stuck"
        elif [ -f $st ]; then
            json=$(cat $st)
            status=$(jq -r '.status' <<<"$json")
            reason=$(jq -r '.reason // ""' <<<"$json")
        else
            continue
        fi
        # Crossing 80%, 85% or 90% of use is reported at once, like a status change
        bucket=$(jq -r 'if (.size // 0) > 0 then ((.used // 0) * 100 / .size | floor) else 0 end
            | if . >= 90 then 3 elif . >= 85 then 2 elif . >= 80 then 1 else 0 end' <<<"$json" 2>/dev/null)
        sig="$status|$reason|$bucket"
        read last_ts last_sig < <(cat $pool_state_dir/$pool.reported 2>/dev/null)
        if [ "$sig" != "$last_sig" ] || [ $((now - ${last_ts:-0})) -ge 300 ]; then
            pool_status_callback report $pool "$status" "$reason" "$json"
            echo "$now $sig" >$pool_state_dir/$pool.reported
        fi
    done
}

function sync_delayed_job()
{
    for f in $(ls $async_job_dir/*.done); do
        cat $f
	sudo rm -f $f
    done
}

function calc_resource()
{
    virtual_cpu=0
    virtual_memory=0
    virtual_disk=0
    for xml in $(ls $xml_dir/*/*.xml 2>/dev/null); do
        vcpu=$(xmllint --xpath 'string(/domain/vcpu)' $xml)
        vmem=$(xmllint --xpath 'string(/domain/memory)' $xml)
        [ -n "$vcpu" ] && let virtual_cpu=$virtual_cpu+$vcpu
        [ -n "$vmem" ] && let virtual_memory=$virtual_memory+$vmem
    done
    # The disk figures are the room of the built-in pool, in bytes, with the capacity clapi uses for it
    # (applyCapacity in storage_callbacks.go): what CloudLand holds already (own: the instance and volume
    # directories, measured by the pool probe in the background) plus what is free, times the over-commit ratio.
    # The OS, the control plane and the image cache share the root file system and their part is not for
    # allocation. Less the virtual size of every disk in the pool; disks in other pools are admitted by clapi
    # against their own pool. Only *.disk files count, so NVRAM and lists are skipped; the JSON output gives the
    # exact size in bytes (the text output was read as GiB whatever its unit, a 528 KiB NVRAM counted as 528 GiB).
    for vdisk_file in $image_dir/*.disk $volume_dir/*.disk; do
        [ -f "$vdisk_file" ] || continue
        vdisk=$(qemu-img info --force-share --output=json "$vdisk_file" 2>/dev/null | jq -r '."virtual-size" // 0')
        virtual_disk=$((virtual_disk + ${vdisk:-0}))
    done
    own_disk=$(jq -r '.own // 0' $pool_state_dir/builtin.state 2>/dev/null)
    avail_disk=$(df -B1 --output=avail $image_dir | tail -1 | tr -d ' ')
    total_disk=$(echo "(${own_disk:-0}+${avail_disk:-0})*$disk_over_ratio" | bc)
    total_disk=${total_disk%.*}
    disk=$((total_disk - virtual_disk))
    [ $disk -lt 0 ] && disk=0
    total_cpu=$(echo "$total_cpu*$cpu_over_ratio" | bc)
    total_cpu=${total_cpu%.*}
    cpu=$(echo "$total_cpu-$virtual_cpu" | bc)
    cpu=${cpu%.*}
    [ $cpu -lt 0 ] && cpu=0
    total_memory=$(echo "$total_memory*$mem_over_ratio" | bc)
    total_memory=${total_memory%.*}
    memory=$(echo "$total_memory-$virtual_memory" | bc)
    memory=${memory%.*}
    free_mem=$(cat /proc/meminfo | grep -i MemFree | awk '{print $2}')
    [ $memory -lt $free_mem ] && memory=$free_mem
    if [ $(( $(date +"%s") % 10 )) -gt 7 ]; then
	rm -f $run_dir/old_resource_list
    fi
    state=1
    if [ -f "$run_dir/disabled" ]; then
        echo "cpu=0/$total_cpu memory=0/$total_memory disk=0/$total_disk network=$network/$total_network load=$load/$total_load"
        state=0
    else
        echo "cpu=$cpu/$total_cpu memory=$memory/$total_memory disk=$disk/$total_disk network=$network/$total_network load=$load/$total_load"
    fi
    cd /opt/cloudland/run
    let disk=$disk/1000*1000
    let total_disk=$total_disk/1000*1000
    old_resource_list=$(cat old_resource_list 2>/dev/null)
    resource_list="'$cpu' '$total_cpu' '$memory' '$total_memory' '$disk' '$total_disk' '$state'"
    echo "'$cpu' '$total_cpu' '$memory' '$total_memory' '$disk' '$total_disk' '$state'" >/opt/cloudland/run/old_resource_list
    [ "$resource_list" = "$old_resource_list" ] && return
    cpu_model=$(lscpu | grep 'Model name:' | cut -d: -f2 | xargs)
    echo "|:-COMMAND-:| hyper_status.sh '$NODE_ID' '$HOSTNAME' '$cpu' '$total_cpu' '$memory' '$total_memory' '$disk' '$total_disk' '$state' '$vtep_ip' '$ZONE_NAME' '$cpu_over_ratio' '$mem_over_ratio' '$disk_over_ratio' '$cpu_model'"
}

calc_resource
pool_report
sync_instance
pending_start
recover_loadbalancer
check_lb_process
report_lb_health
recover_vpn_gateway
check_vpn_process
report_vpn_status
sync_delayed_job
check_system_router
#probe_arp >/dev/null 2>&1
inst_status
check_conntrack
daily_job
halfday_job
#vlan_status
#router_status
