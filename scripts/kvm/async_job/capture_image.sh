#!/bin/bash
# Capture an image from the boot disk of a shut off instance

cd `dirname $0`
source ../../cloudrc

[ $# -lt 4 ] && echo "$0 <img_ID> <img_Prefix> <vm_ID> <boot_disk_path> [<upload_url> <capture_token>]" && exit -1

img_ID=$1
prefix=$2
vm_ID=inst-$3
boot_disk=$4
# Upload parameters of S3/MinIO: sent by clapi when S3 is enabled, empty otherwise
upload_url=$5
capture_token=$6
image_name=image-$img_ID-$prefix
state=error
size=0

if [ ! -f "$boot_disk" ]; then
    echo "|:-COMMAND-:| capture_image.sh '$img_ID' 'error' 'qcow2' '0' 'boot disk $boot_disk not found'"
    exit -1
fi
mkdir -p $image_cache
image=$cache_tmp_dir/image-$vm_ID-$img_ID.qcow2
mkdir -p $cache_tmp_dir
format=$(qemu-img info -U $boot_disk | grep 'file format' | cut -d' ' -f3)
qemu-img convert -U -f $format -O qcow2 $boot_disk $image
if [ -s "$image" ]; then
    size=$(qemu-img info $image | grep 'virtual size:' | cut -d' ' -f5 | tr -d '(')
    if [ -n "$upload_url" ] && [ -n "$capture_token" ]; then
        # S3/MinIO: POST to clapi /internal/images/:id/upload.
        # No retry here: sending an N GB file again is expensive, and retrying a 5xx from clapi would flip the
        # record between error and available. A failed capture is started again by the user.
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
        # Legacy local mode: keep the image in the node cache under the name instances are launched from
        mv -f $image $image_cache/$image_name.qcow2 && state=available
    fi
fi
rm -f "$image"
echo "|:-COMMAND-:| capture_image.sh '$img_ID' '$state' 'qcow2' '$size' ''"
