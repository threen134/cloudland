#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 3 ] && die "$0 <ID> <url> <hyperType>"

ID=$1
url=$2
hyperType=$3

state=error
image=$image_cache/image-$1
curl -s $url -o $image
format=$(qemu-img info $image | grep 'file format' | cut -d' ' -f3)
[ "$format" = "qcow2" -o "$format" = "raw" ] && state=available
[ ! -s "$image" ] && state=error
[ $hyperType = "zvm" ] && format=img
mv $image ${image}.${format}
#sync_target /opt/cloudland/cache/image
echo "|:-COMMAND-:| $(basename $0) '$ID' '$state' '$format'"
