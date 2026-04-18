#!/bin/bash

cd `dirname $0`
source ../../cloudrc

[ $# -lt 5 ] && echo "$0 <img_ID> <img_Prefix> <vm_ID> <boot_volume> <storage_ID> [<upload_url> <capture_token>]" && exit -1

img_ID=$1
prefix=$2
vm_ID=inst-$3
boot_volume=$4
storage_ID=$5
# S3/MinIO 上传参数（仅非 WDS + S3 启用时由 clapi 下发）
upload_url=$6
capture_token=$7
image_name=image-$img_ID-$prefix
state=error

# its better to let user shutdown the vm before capturing the image
# virsh suspend $vm_ID
if [ -z "$wds_address" ]; then
    # capture the image from the running instance locally
    image=${image_dir}/image-$vm_ID.qcow2
    inst_img=$cache_dir/instance/${vm_ID}.disk

    format=$(qemu-img info $inst_img | grep 'file format' | cut -d' ' -f3)
    qemu-img convert -f $format -O qcow2 $inst_img $image
    if [ -s "$image" ]; then
        if [ -n "$upload_url" ] && [ -n "$capture_token" ]; then
            # S3/MinIO 路径：POST 给 clapi /internal/images/:id/upload
            # 不做 curl 层重试：N GB 文件重传代价高；且 clapi 返回 5xx 时重试会让 DB 状态在 error↔available 之间闪，
            # 失败由用户重新发起 capture
            http_code=$(curl -sS -o /dev/null -w "%{http_code}" --max-time 7200 \
                -X POST \
                -H "X-Capture-Token: $capture_token" \
                -H "Content-Type: application/octet-stream" \
                --data-binary "@$image" \
                "$upload_url")
            curl_rc=$?
            [ $curl_rc -ne 0 ] && http_code="000"
            if [ "$http_code" = "200" ]; then
                state=available
            else
                log_debug $vm_ID "capture upload failed http=$http_code curl_rc=$curl_rc image=$image_name"
            fi
            rm -f "$image"
        else
            # legacy 单节点模式：不分发，由 FE 回调更新 DB
            state=available
        fi
    fi
    volume_id=""
else
    # clone the image from the boot volume on the remote storage WDS
    if [ -z "$boot_volume" ]; then
        echo "|:-COMMAND-:| capture_image.sh '$img_ID' 'error' 'qcow2' 'boot_volume is not specified' '' '$storage_ID'"
        exit -1
    fi
    get_wds_token
    # refine the image capture flow
    # 1. take the snapshot of the boot volume
    snapshot_ret=$(wds_curl POST "api/v2/sync/block/snaps/" "{\"description\":\"snapshot for image $image_name\", \"name\":\"$image_name\", \"volume_id\":\"$boot_volume\"}")
    read -d'\n' -r snapshot_id ret_code message < <(jq -r ".id, .ret_code, .message" <<<$snapshot_ret)
    if [ "$ret_code" != "0" ]; then
        log_debug $vm_ID "failed to create snapshot for boot volume $boot_volume: $message"
        echo "|:-COMMAND-:| capture_image.sh '$img_ID' 'error' 'qcow2' 'failed to create snapshot for the boot volume: $message' '$storage_ID'"
        exit -1
    fi
    log_debug $vm_ID "snapshot $snapshot_id created for boot volume $boot_volume"
    # 2. copy_clone the snapshot
    clone_ret=$(wds_curl PUT "api/v2/sync/block/snaps/$snapshot_id/copy_clone" "{\"name\":\"$image_name\", \"speed\": 32, \"phy_pool_id\": \"$wds_pool_id\"}")
    read -d'\n' -r task_id ret_code message < <(jq -r ".task_id, .ret_code, .message" <<<$clone_ret)
    if [ "$ret_code" != "0" ]; then
        log_debug $vm_ID "failed to clone snapshot $snapshot_id: $message"
        echo "|:-COMMAND-:| capture_image.sh '$img_ID' 'error' 'qcow2' 'failed to clone the snapshot: $message' '$storage_ID'"
        exit -1
    fi
    log_debug $vm_ID "clone task $task_id created for snapshot $snapshot_id"
    for i in {1..150}; do
         st=$(wds_curl GET "api/v2/sync/block/volumes/tasks/$task_id" | jq -r .task.state)
	     [ "$st" = "TASK_COMPLETE" ] && state=uploaded && break
	     [ "$st" = "TASK_FAILED" ] && state=failed && break
	    sleep 5
    done
    # 3. delete the snapshot
    delete_ret=$(wds_curl DELETE "api/v2/sync/block/snaps/${snapshot_id}?force=true")
    read -d'\n' -r ret_code message < <(jq -r ".ret_code, .message" <<<$delete_ret)
    log_debug $vm_ID "delete snapshot $snapshot_id: $message"

    # 4. get the volume id from the image name
    volume_id=$(wds_curl GET "api/v2/sync/block/volumes?name=$image_name" | jq -r '.volumes[0].id')
    [ -n "$volume_id" ] && state=available
fi
# virsh resume $vm_ID
echo "|:-COMMAND-:| capture_image.sh '$img_ID' '$state' 'qcow2' 'success' '$volume_id' '$storage_ID'"
