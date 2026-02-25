#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 12 ] && die "$0 <vm_ID> <image> <qa_enabled> <snapshot> <name> <cpu> <memory> <disk_size> <volume_id> <nested_enable> <boot_loader> <pool_ID> <instance_uuid> <image_volume_id>"

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
pool_ID=${12}
instance_uuid=${13:-$ID}
image_volume_id=${14}
state=error
vm_vnc=""
vol_state=error


#读取标准输入（stdin）中的 Base64 编码数据，先对其进行解码得到 JSON 格式的元数据，
#再从该 JSON 数据中提取两个指定字段（disk_iops_limit 和 disk_bps_limit）的值，
#最终将这两个值分别存入两个 Shell 变量中 sysdisk_iops_limit sysdisk_bps_limit
#./launch_vm.sh <参数1> <参数2> ... < metadata.json
# '%s'<<EOF\n%s\nEOF" 通过这样的方式传递标准输入作为matadate给脚本
#读取标准输入（stdin）的所有内容，并将其完整保存到变量md中。调用的时候通过
md=$(cat)
#将变量md中存储的 Base64 编码数据进行解码，解码后的结果保存到变量metadata中
metadata=$(echo $md | base64 -d)
#这行代码是核心逻辑的收尾，作用是 使用jq工具从 JSON 格式的metadata变量中，提取disk_iops_limit和disk_bps_limit两个字段的原始值，
#再通过read命令将这两个提取结果分别赋值给sysdisk_iops_limit和sysdisk_bps_limit两个变量
read -d'\n' -r sysdisk_iops_limit sysdisk_bps_limit < <(jq -r ".disk_iops_limit, .disk_bps_limit" <<<$metadata)

#进行磁盘大小的单位换算，将原始磁盘大小（默认应为 GB 单位）转换为字节（Byte）单位，并将结果赋值给变量 fsize。
let fsize=$disk_size*1024*1024*1024
#针对指定虚拟机（通过 vm_ID 和 vm_name 标识）生成相关元数据（最终产出 vm_ID.iso 元数据文件），
#并根据虚拟机的启动模式（uefi/ 非 uefi）选择对应的 XML 模板文件
# ./build_meta.sh 会创建一个 ISO 文件是 OpenStack 实例的元数据启动盘（metadata ISO），作用是在 VM 启动阶段，
#为实例提供初始化配置信息，替代传统的 DHCP 元数据服务（比如 169.254.169.254 这个元数据地址）。
# ISO 文件地址为 /opt/cloudland/cache/meta/inst-1.iso
# meta_data.json     network_data.json  vendor_data.json   
# root@worknode-01:~# cat  /mnt/meta_iso/openstack/latest/meta_data.json 
# {
#   "name": "instance-01.example.com",
#   "public_keys": {"key0": "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDI/yzT9rqaMjGVIK4gVASQ0E/8EUvKi2Iz1Q7ZC8ozvz1C7x1IfCwhQT0LlLAzbxuzDZChGPQdteK9iun0HC+/Mf5eaITCxUTmLaL+Xzp4a/DW4XfWayZFx2tyn0uBoIc0k+G8nrP50w0KT13AeP8715G0BejAAFWyt2OKsZDNEk7kHC6uZtJ9UHJvDdSVq4ZtU6W/1nza5/kCMjJGdWrrPrm7OIvRajjnqtWDNBhKBdQGIqRUYUxC6RrWFk0Bh67Yjgk1OUOM/JW3eDEI0nDHQ0xkJhMQAvU/vjdjGWfarSbh1bZibiUMNg/nK8evdKPve5ux3iL/TiV+egHbhP7+NYdcxB2t/ICyMqdRCsIiLQsutPzSEzAQEPwHA5HXzQ1JO319WIdhWPmMj5d/D3qxLfSMR6lTzciEHpHF7mE3fbOl86Xm3dgPjDSDMiGsMfRI+LS4vF8y43AN+fxAIx4XfSVYojgO0KqcrSA/mOP3db2t1TEdTzBY3dWi55iykKU= spark@localhost\n"},
#   "launch_index": 0,
#   "hostname": "instance-01.example.com",
#   "availability_zone": "cloudland",
#   "uuid": "inst-1",
#   "admin_pass": "BtYuyNachUi9esHB",
#   "random_seed": "KlbpE+ZJlBHPOJEqeAXUULl1zxKa3b0FVxS6a6AVu3dXtrPvIj1pKuV2ShFXdU/lVr+OEzNBuZg8unYJJzLiJaYEV3HEUsUwWqp+WWty65PM27BWcBMdFdkn86jXGBCHoYHQ/KjLR6C+AfHpwBH5ZcXNEhi0F2yemBgycNQZY7K97OuPFpU45ggOmqtOLh8CSHLt2IKVI6u0wgf01DoQ/wmqsN8opHvfFKa8RBTo8KvSloRyp+dHrdmFgQXRUwvm/NhmlJ5FL5E8GxjAR9B8j3wQ0gKfpDShamzXq5hPtMKsqLo1W/aTy4JaytUjiuUlqxfBbLP4JRQpdXiW+uWiWHBlOxwv8Bt0Biq5fI8EHcqzOA5RWvG1bbjJW/7viMU1H/0WAPqDe293KJgMQsMllhXHxhBhRbGTCCQ/YBx6cTzdxaXuRLYxDEVsbV3VWBZhGufakjYK6CxSx73End+Euo7F0Vk7smZHgYvDWB1DJRSHn1u+PouLsihpbFgEjg/zjmWN4Ma4ZRWJSRLKPwJVZh1U9kxhIdrGHeqQebEjvxuMOsDl7tkNMUoq9jmzTk6OZd5o4aZUVKXskngV8yDOiQAZBKzQDSvHzMPWAqOiAheE6gf1yo2W4F1iF8XFXnUuEkqrOU0rXhUNOjZmmqXegFVgLTlMyk/Xys+nepmAPaA="
# }
# root@worknode-01:~# cat  /mnt/meta_iso/openstack/latest/network_data.json 
# {
#   "userdata_type": "plain",
#   "networks": [
#     {
#       "type": "ipv4",
#       "ip_address": "192.168.1.20",
#       "netmask": "255.255.255.0",
#       "link": "eth0",
#       "id": "network0",
#       "routes": [
#         {
#           "network": "0.0.0.0",
#           "netmask": "0.0.0.0",
#           "gateway": "192.168.1.1"
#         }
#       ]
#     }
#   ],
#   "links": [
#     {
#       "ethernet_mac_address": "52:54:8c:cc:34:01",
#       "mtu": 1450,
#       "id": "eth0",
#       "type": "phy"
#     }
#   ],
#   "volumes": null,
#   "os_code": "linux",
#   "disk_iops_limit": 0,
#   "disk_bps_limit": 0,
#   "services": [
#     {
#       "type": "dns",
#       "address": "8.8.8.8"
#     }
#   ]
# }
# root@worknode-01:~# cat  /mnt/meta_iso/openstack/latest/vendor_data.json 
# "Content-Type: multipart/mixed; boundary=\"//\"\nMIME-Version: 1.0\n\n--//\nContent-Type: text/cloud-config; charset=\"us-ascii\"\nMIME-Version: 1.0\nContent-Transfer-Encoding: 7bit\nContent-Disposition: attachment; filename=\"cloud-config.txt\"\n\n#cloud-config\nssh_pwauth: true\ndisable_root: false\nchpasswd:\n  expire: false\n  users:\n    - name: root\n      password: $6$rounds=4096$yk3o6LiJNhKVr4hA$Ewm5wHgvnCkwDZs1GesZ1eRKTcACqC4U6ITmGf3soVPsD269m5aKWGrqQLdMw/AD2pW5btbbfhtmnMWpBT5gP0\n  list: |\n    root:$6$rounds=4096$yk3o6LiJNhKVr4hA$Ewm5wHgvnCkwDZs1GesZ1eRKTcACqC4U6ITmGf3soVPsD269m5aKWGrqQLdMw/AD2pW5btbbfhtmnMWpBT5gP0\nwrite_files:\nruncmd:\n  - |\n    if [ -f /etc/sysconfig/qemu-ga ]; then\n      sed -i 's/--allow-rpcs=/--allow-rpcs=guest-exec,/;/BLACKLIST_RPC/d' /etc/sysconfig/qemu-ga\n    elif [ -f /lib/systemd/system/qemu-guest-agent.service ]; then\n      sed -i \"s#/usr/bin/qemu-ga#/usr/bin/qemu-ga -b ''#\" /lib/systemd/system/qemu-guest-agent.service\n      sed -i \"s#/usr/sbin/qemu-ga#/usr/sbin/qemu-ga -b ''#\" /lib/systemd/system/qemu-guest-agent.service\n      systemctl daemon-reload\n    fi\n    systemctl restart qemu-guest-agent.service\n--//--"
# vendor_data.json 是 OpenStack 云初始化数据的标准组成部分（属于供应商侧提供的初始化配置），/mnt/meta_iso/ 是云主机内部挂载的「元数据 ISO 镜像」目录，OpenStack 会将实例的元数据、初始化配置等封装在该 ISO 中，供云主机内部的 cloud-init 程序读取并执行。

./build_meta.sh "$vm_ID" "$vm_name" <<< $md >/dev/null 2>&1
vm_meta=$cache_dir/meta/$vm_ID.iso
template=$template_dir/template_with_qa.xml
if [ "$boot_loader" = "uefi" ]; then
    template=$template_dir/template_uefi_with_qa.xml
fi

if [ -z "$wds_address" ]; then
    vm_img=$volume_dir/$vm_ID.disk
    # 优先从本地卷目录查找虚拟机磁盘
    if [ ! -f "$vm_img" ]; then
        vm_img=$image_dir/$vm_ID.disk
        # 切换到镜像目录，检查镜像缓存有效性
         #-s 用于判断文件是否存在且大小大于 0（非空文件）
        if [ ! -s "$image_cache/$img_name" ]; then
            echo "Image is not available!"
            echo "|:-COMMAND-:| create_volume_local '$vol_ID' 'volume-${vol_ID}.disk' '$vol_state' 'image $img_name not available!'"
            exit -1
        fi
        #镜像格式转换（转为 qcow2 格式）
        # 提取原镜像文件格式
        format=$(qemu-img info $image_cache/$img_name | grep 'file format' | cut -d' ' -f3)
        # 拼接格式转换命令
        cmd="qemu-img convert -f $format -O qcow2 $image_cache/$img_name $vm_img"
        # 执行转换命令
        result=$(eval "$cmd")
        #磁盘大小校验与调整
        # 提取转换后镜像的虚拟大小
        vsize=$(qemu-img info $vm_img | grep 'virtual size:' | cut -d' ' -f5 | tr -d '(')
        if [ "$vsize" -gt "$fsize" ]; then
            # 登记卷创建失败（规格小于镜像大小）
            echo "|:-COMMAND-:| create_volume_local '$vol_ID' 'volume-${vol_ID}.disk' '$vol_state' 'flavor is smaller than image size'"
            exit -1
        fi
        ## 调整磁盘镜像大小
        qemu-img resize -q $vm_img "${disk_size}G" &> /dev/null
        #登记卷创建成功
        vol_state=attached
        echo "|:-COMMAND-:| create_volume_local.sh '$vol_ID' 'volume-${vol_ID}.disk' '$vol_state' 'success'"
    fi
else
    get_wds_token
    if [ -z "$pool_ID" ]; then
        pool_ID=$wds_pool_id
    fi
    image=$(basename $img_name .raw)
    if [ "$pool_ID" != "$wds_pool_id" ]; then
        pool_prefix=$(get_uuid_prefix "$pool_ID")
        image=${image}-${pool_prefix}
    fi
    vhost_name=instance-$ID-volume-$vol_ID-$RANDOM
    snapshot_name=${image}-${snapshot}
    read -d'\n' -r snapshot_id volume_size <<< $(wds_curl GET "api/v2/sync/block/snaps?name=$snapshot_name" | jq -r '.snaps[0] | "\(.id) \(.snap_size)"')
    if [ -z "$snapshot_id" -o "$snapshot_id" = null ]; then
        snapshot_ret=$(wds_curl POST "api/v2/sync/block/snaps" "{\"name\": \"$snapshot_name\", \"description\": \"$snapshot_name\", \"volume_id\": \"$image_volume_id\"}")
        read -d'\n' -r snapshot_id volume_size <<< $(wds_curl GET "api/v2/sync/block/snaps?name=$snapshot_name" | jq -r '.snaps[0] | "\(.id) \(.snap_size)"')
        if [ -z "$snapshot_id" -o "$snapshot_id" = null ]; then
            echo "|:-COMMAND-:| create_volume_wds_vhost '$vol_ID' '$vol_state' '' 'failed to create image snapshot, $snapshot_ret'"
            exit -1
        fi
        wds_curl DELETE "api/v2/sync/block/snaps/$image-$(($snapshot-1))?force=false"
    fi
    volume_ret=$(wds_curl POST "api/v2/sync/block/snaps/$snapshot_id/clone" "{\"name\": \"$vhost_name\"}")
    volume_id=$(echo $volume_ret | jq -r .id)
    if [ -z "$volume_id" -o "$volume_id" = null ]; then
        echo "|:-COMMAND-:| create_volume_wds_vhost '$vol_ID' '$vol_state' '' 'failed to create boot volume based on snapshot $snapshot_name, $volume_ret!'"
        exit -1
    fi
    if [ "$fsize" -gt "$volume_size" ]; then
        expand_ret=$(wds_curl PUT "api/v2/sync/block/volumes/$volume_id/expand" "{\"size\": $fsize}")
        ret_code=$(echo $expand_ret | jq -r .ret_code)
        if [ "$ret_code" != "0" ]; then
            echo "|:-COMMAND-:| create_volume_wds_vhost '$vol_ID' '$vol_state' 'wds_vhost://$pool_ID/$volume_id' 'failed to expand boot volume to size $fsize, $expand_ret'"
            exit -1
        fi
    fi
    # if sysdisk_iops_limit > 0 or sysdisk_bps_limit > 0 update volume qos
    if [ "$sysdisk_iops_limit" -gt 0 -o "$sysdisk_bps_limit" -gt 0 ]; then
        sysdisk_bps_limit=$(($sysdisk_bps_limit * $wds_bps_factor))
        update_ret=$(wds_curl PUT "api/v2/sync/block/volumes/$volume_id/qos" "{\"qos\": {\"iops_limit\": $sysdisk_iops_limit, \"bps_limit\": $sysdisk_bps_limit}}")
        log_debug $vol_ID "update volume qos: $update_ret"
    fi
    uss_id=$(get_uss_gateway)
    vhost_ret=$(wds_curl POST "api/v2/sync/block/vhost" "{\"name\": \"$vhost_name\"}")
    vhost_id=$(echo $vhost_ret | jq -r .id)
    uss_ret=$(wds_curl PUT "api/v2/sync/block/vhost/bind_uss" "{\"vhost_id\": \"$vhost_id\", \"uss_gw_id\": \"$uss_id\", \"lun_id\": \"$volume_id\", \"is_snapshot\": false}")
    ret_code=$(echo $uss_ret | jq -r .ret_code)
    if [ "$ret_code" != "0" ]; then
        echo "|:-COMMAND-:| create_volume_wds_vhost '$vol_ID' '$vol_state' 'wds_vhost://$pool_ID/$volume_id' 'failed to create wds vhost for boot volume, $vhost_ret, $uss_ret!'"
        exit -1
    fi
    vol_state=attached
    echo "|:-COMMAND-:| create_volume_wds_vhost '$vol_ID' '$vol_state' 'wds_vhost://$pool_ID/$volume_id' 'success'"
    ux_sock=/var/run/wds/$vhost_name
    template=$template_dir/wds_template_with_qa.xml
    if [ "$boot_loader" = "uefi" ]; then
        template=$template_dir/wds_template_uefi_with_qa.xml
    fi
fi

#用于配置虚拟机（VM）相关参数、创建目录并准备虚拟机配置文件的 Shell 脚本片段，
#核心是完成虚拟机运行前的基础参数初始化和配置文件准备工作，
[ -z "$vm_mem" ] && vm_mem='1024m'
[ -z "$vm_cpu" ] && vm_cpu=1
let vm_mem=${vm_mem%[m|M]}*1024
mkdir -p $xml_dir/$vm_ID
#QEMU 虚拟机代理文件路径（后缀.agent），用于宿主机与虚拟机之间的通信；
#/opt/cloudland/cache/qemu_agent
#qemu-agent（QEMU 虚拟机代理）用于和宿主机上的 libvirt 等管理进程通信的套接字文件。
vm_QA="$qemu_agent_dir/$vm_ID.agent"
vm_xml=$xml_dir/$vm_ID/${vm_ID}.xml
cp $template $vm_xml
#嵌套虚拟化功能开关配置
if [ "$nested_enable" = "true" ]; then
    vm_nested="require"
else
    vm_nested="disable"
fi
#if-else 逻辑：识别 CPU 厂商，匹配对应的硬件虚拟化特性 ——Intel（GenuineIntel）对应 vmx 虚拟化指令集，
#AMD 对应 svm 虚拟化指令集，为后续虚拟机启用硬件加速虚拟化提供依据。
cpu_vendor=$(lscpu | grep "Vendor ID" | awk -F ':' '{print $2}' | tr -d ' ')
if [ "$cpu_vendor" = "GenuineIntel" ]; then
    vm_virt_feature="vmx"
else
    vm_virt_feature="svm"
fi
#CPU 核数 > 2 时启用更多网络队列，提升网络并发处理能力，是一种简单的性能适配策略。
vhost_queue_num=1
if [ "$vm_cpu" -gt 2 ]; then
    vhost_queue_num=2
fi
#os_code 是从虚拟机元数据（JSON 格式）中提取的操作系统标识字段，
#核心作用是区分虚拟机的操作系统类型（如 Windows/Linux 等），并基于不同系统执行差异化的后置配置逻辑
os_code=$(jq -r '.os_code' <<< $metadata)
#sed -i "s/VM_ID/$vm_ID/g; s/VM_MEM/$vm_mem/g; s/VM_CPU/$vm_cpu/g; s#VM_IMG#$vm_img#g; s#VM_UNIX_SOCK#$ux_sock#g; s#VM_META#$vm_meta#g; s#VM_AGENT#$vm_QA#g; s/VM_NESTED/$vm_nested/g; s/VM_VIRT_FEATURE/$vm_virt_feature/g; s/INSTANCE_UUID/$instance_uuid/g" $vm_xml
#定义虚拟机 NVRAM（非易失性随机访问存储器，UEFI 启动模式下用于存储启动配置、硬件信息等）的文件路径，后续 UEFI 分支会用到。
vm_nvram="$image_dir/${vm_ID}_VARS.fd"
if [ "$boot_loader" = "uefi" ]; then
# 先执行 cp 命令：复制 UEFI 模板文件 $nvram_template 到 $vm_nvram，为 UEFI 启动提供必要的配置文件；
# 再执行 sed -i 原地替换：在通用占位符替换的基础上，
# 新增了 2 个 UEFI 模式专属的替换项（VM_BOOT_LOADER、VM_NVRAM），适配 UEFI 启动的特殊配置需求。
    cp $nvram_template $vm_nvram
    sed -i \
    -e "s/VM_ID/$vm_ID/g" \
    -e "s/VM_MEM/$vm_mem/g" \
    -e "s/VM_CPU/$vm_cpu/g" \
    -e "s/VHOST_QUEUE_NUM/$vhost_queue_num/g" \
    -e "s#VM_IMG#$vm_img#g" \
    -e "s#VM_UNIX_SOCK#$ux_sock#g" \
    -e "s#VM_META#$vm_meta#g" \
    -e "s#VM_AGENT#$vm_QA#g" \
    -e "s/VM_NESTED/$vm_nested/g" \
    -e "s/VM_VIRT_FEATURE/$vm_virt_feature/g" \
    -e "s#VM_BOOT_LOADER#$uefi_boot_loader#g" \
    -e "s#VM_NVRAM#$vm_nvram#g" \
    -e "s/INSTANCE_UUID/$instance_uuid/g" \
    $vm_xml
else
# 无需复制 NVRAM 模板文件（BIOS 启动不依赖该文件）；
#仅执行通用占位符替换，去掉了 UEFI 模式专属的两个替换项，满足 BIOS 启动的配置需求
    sed -i \
    -e "s/VM_ID/$vm_ID/g" \
    -e "s/VM_MEM/$vm_mem/g" \
    -e "s/VM_CPU/$vm_cpu/g" \
    -e "s/VHOST_QUEUE_NUM/$vhost_queue_num/g" \
    -e "s#VM_IMG#$vm_img#g" \
    -e "s#VM_UNIX_SOCK#$ux_sock#g" \
    -e "s#VM_META#$vm_meta#g" \
    -e "s#VM_AGENT#$vm_QA#g" \
    -e "s/VM_NESTED/$vm_nested/g" \
    -e "s/VM_VIRT_FEATURE/$vm_virt_feature/g" \
    -e "s/INSTANCE_UUID/$instance_uuid/g" \
    $vm_xml
fi

#根据指定的 XML 配置文件，在 libvirt 中定义（注册）一个虚拟机，但不会启动该虚拟机
virsh define $vm_xml
#该代码文件是一个名为 generate_vm_instance_map.sh 的 Bash 脚本，核心功能是生成 / 维护 Prometheus 监控指标文件，
#建立虚拟机（VM）的域名（domain）与实例 ID（instance_id）的映射关系，同时区分虚拟机的正常（normal）和救援（rescue）模式，便于 Prometheus 采集和监控。
./generate_vm_instance_map.sh add $vm_ID
#禁用该虚拟机的 "开机自动启动"（不随 libvirt 服务启动而自动运行）
#该命令仅配置自动启动规则，不会立即启动虚拟机，与virsh start功能区分开。
virsh autostart $vm_ID --disable
#提取虚拟机网络元数据中的 VLAN 信息，并同步到网卡配置相关的自定义脚本中，
#完成虚拟机网卡 / VLAN 的配置同步，接收 3 个参数（虚拟机 ID、虚拟机名称、操作系统编码）
# vlans 包含多个接口信息，是个数组
jq .vlans <<< $metadata | ./sync_nic_info.sh "$ID" "$vm_name" "$os_code"
#立即启动该虚拟机（执行实际的开机操作）。
virsh start $vm_ID
#判断上一条命令（virsh start $vm_ID）的执行结果，若启动成功则将虚拟机状态变量state设为running
[ $? -eq 0 ] && state=running
echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$SCI_CLIENT_ID' 'init'"

# check if the vm is windows and whether to change the rdp port
#判断目标虚拟机（VM）是否为 Windows 系统，若是则尝试读取指定的 RDP 端口号，
#满足条件后后台异步修改 RDP 端口，最后异步执行 Windows 虚拟机主 IP 相关的处理脚本
if [ "$os_code" = "windows" ]; then
    rdp_port=$(jq -r '.login_port' <<< $metadata)
    if [ -n "$rdp_port" ] && [ "${rdp_port}" != "3389" ]  && [ ${rdp_port} -gt 0 ]; then
        # run the script to change the rdp port in background
        #async_exec 是自定义的异步执行工具 / 函数（Shell 本身无此内置命令），
        #作用是将后续脚本放入后台运行，不阻塞当前主进程。
        async_exec ./async_job/win_rdp_port.sh $ID $rdp_port
    fi
    async_exec ./async_job/win_primary_ip.sh $ID <<< $metadata
fi
