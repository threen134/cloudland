#!/bin/bash
# List the files of a pool by the space they really take (§6.1 of the local storage plan).
# The directories come from the pool id only: no path is accepted from the caller.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

[ $# -lt 1 ] && die "$0 <pool_uuid|builtin>"

pool=$1
root=$(pool_root "$pool") || die "invalid pool $pool"
if [ "$pool" = "builtin" ]; then
    dirs="$image_dir $volume_dir"
else
    pool_guard $root $pool $NODE_ID || die "$guard_error"
    dirs="$root/volumes"
fi
# Paths are reported relative to the pool root, as clapi records them
json=$(timeout 300 find $dirs -xdev -maxdepth 1 -type f -printf '%b %s %P %h\n' 2>/dev/null |
    awk -v root="$root/" '{dir = substr($4, length(root) + 1); print $1 * 512, $2, dir "/" $3}' |
    sort -rn | head -200 |
    jq -R -s -c 'split("\n") | map(select(. != "") | split(" ") | {bytes: (.[0] | tonumber), size: (.[1] | tonumber), path: .[2]})')
echo "|:-COMMAND-:| pool_usage '$NODE_ID' '$pool' '$(echo -n "${json:-[]}" | base64 -w0)'"
