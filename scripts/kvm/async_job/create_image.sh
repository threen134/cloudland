#!/bin/bash

cd `dirname $0`
source ../../cloudrc

[ $# -lt 4 ] && die "$0 <ID> <prefix> <url> <storage_ID>"

ID=$1
prefix=$2
url=$3
storage_ID=$4
image_name=image-$ID-$prefix
state=error
mkdir -p $image_cache
image=$image_cache/$image_name
# -f：HTTP 4xx/5xx 视为失败；-L：跟随跳转（镜像站常返回 302）。不限总时长（大镜像可能下载数小时）：
# 连续 5 分钟低于 1KB/s 视为卡住并中止；只对开始后 5 分钟内的快速失败（DNS、连接、5xx）重试，避免从头重下大文件
inet_access curl -fsSL -k --connect-timeout 30 --speed-time 300 --speed-limit 1024 --retry 2 --retry-max-time 300 "$url" -o "$image"
curl_rc=$?

if [ $curl_rc -ne 0 ] || [ ! -s "$image" ]; then
    rm -f $image
    echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$format' '0' 'null' '$storage_ID'"
    exit -1
fi
format=$(qemu-img info $image | grep 'file format' | cut -d' ' -f3)
# qemu-img 把认不出的文件（HTML 错误页、压缩包等）都报成 raw：raw 镜像必须带 MBR/GPT 引导签名 0x55AA
if [ "$format" = "raw" ] && [ "$(od -An -tx1 -j510 -N2 $image 2>/dev/null | tr -d ' \n')" != "55aa" ]; then
    format=""
fi
if [ "$format" != "qcow2" -a "$format" != "raw" ]; then
    rm -f $image
    echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$format' '0' 'null' '$storage_ID'"
    exit -1
fi
state=downloaded
image_size=$(qemu-img info ${image} | grep 'virtual size:' | cut -d' ' -f5 | tr -d '(')

if [ -z "$wds_address" ]; then
    mv $image ${image}.$format
    state=available
    #sync_target /opt/cloudland/cache/image
    volume_id=""
else
    get_wds_token
    qemu-img convert -f $format -O raw ${image} ${image}.raw
    format=raw
    uss_id=$(get_uss_gateway)
    uss_service=$(systemctl -a | grep uss | awk '{print $1}')
    if [ -n "$uss_service" ]; then
        cat /etc/systemd/system/$uss_service | grep cloudland
        if [ $? -ne 0 ]; then
            wds_curl PUT "api/v2/sync/wds/uss/$uss_id" '{"action":"add","mount_path":"/opt/cloudland/cache/image"}'
            systemctl restart $uss_service
        fi
    else
        docker ps | grep USS | awk '{print $1}' | xargs docker inspect | grep cloudland
        if [ $? -ne 0 ]; then
            wds_curl PUT "api/v2/sync/wds/uss/$uss_id" '{"action":"add","mount_path":"/opt/cloudland/cache/image"}'
            sleep 60
        fi
    fi
    for i in {1..5}; do
        task_ret=$(wds_curl "PUT" "api/v2/sync/block/volumes/import" "{\"volname\": \"$image_name\", \"path\": \"${image}.raw\", \"ussid\": \"$uss_id\", \"start_blockid\": 0, \"volsize\": $image_size, \"poolid\": \"$wds_pool_id\", \"num_block\": 0, \"speed\": 8}")
        task_id=$(jq -r .task_id <<<$task_ret)
        state=uploading
        echo "${TRACEPARENT:+trace=${TRACEPARENT:3:32} }$task_ret" >>$log_dir/image_upload.log
        [ -z "$task_id" -o "$task_id" = null ] && sleep 2 && continue
        for j in {1..1000}; do
            st=$(wds_curl GET "api/v2/sync/block/volumes/tasks/$task_id" | jq -r .task.state)
            [ "$st" = "TASK_COMPLETE" ] && state=uploaded && break
            [ "$st" = "TASK_FAILED" ] && state=failed && break
            sleep 5
        done
        volume_id=$(wds_curl GET "api/v2/sync/block/volumes?name=$image_name" | jq -r '.volumes[0].id')
        [ -n "$volume_id" -a "$state" = "uploaded" ] && state=available && break
    done
fi
rm -f ${image}
echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$format' '$image_size' '$volume_id' '$storage_ID'"
