#!/bin/bash
# Download an image into the node cache (legacy local mode without S3)

cd `dirname $0`
source ../../cloudrc

[ $# -lt 3 ] && die "$0 <ID> <prefix> <url>"

ID=$1
prefix=$2
url=$3
image_name=image-$ID-$prefix
state=error
format=""
mkdir -p $image_cache
image=$image_cache/$image_name
# -f：HTTP 4xx/5xx 视为失败；-L：跟随跳转（镜像站常返回 302）。不限总时长（大镜像可能下载数小时）：
# 连续 5 分钟低于 1KB/s 视为卡住并中止；只对开始后 5 分钟内的快速失败（DNS、连接、5xx）重试，避免从头重下大文件
inet_access curl -fsSL -k --connect-timeout 30 --speed-time 300 --speed-limit 1024 --retry 2 --retry-max-time 300 "$url" -o "$image"
curl_rc=$?

if [ $curl_rc -ne 0 ] || [ ! -s "$image" ]; then
    rm -f $image
    echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$format' '0'"
    exit -1
fi
format=$(qemu-img info $image | grep 'file format' | cut -d' ' -f3)
# qemu-img 把认不出的文件（HTML 错误页、压缩包等）都报成 raw：raw 镜像必须带 MBR/GPT 引导签名 0x55AA
if [ "$format" = "raw" ] && [ "$(od -An -tx1 -j510 -N2 $image 2>/dev/null | tr -d ' \n')" != "55aa" ]; then
    format=""
fi
if [ "$format" != "qcow2" -a "$format" != "raw" ]; then
    rm -f $image
    echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$format' '0'"
    exit -1
fi
image_size=$(qemu-img info ${image} | grep 'virtual size:' | cut -d' ' -f5 | tr -d '(')
mv $image ${image}.$format
state=available
echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$format' '$image_size'"
