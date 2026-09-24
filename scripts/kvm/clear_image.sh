#!/bin/bash
# Remove the node cache copy of an image (legacy local mode without S3)

cd $(dirname $0)
source ../cloudrc

[ $# -lt 3 ] && die "$0 <ID> <prefix> <format>"

ID=$1
prefix=$2
format=$3

rm -f $image_cache/image-${ID}-${prefix}.${format}
