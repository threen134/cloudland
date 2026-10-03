#!/bin/bash
# Download an installer from the package repository with its presigned URL and check its SHA-256
# (shared-storage-design.md §6.1). It stays in $cache_dir/storage-pkg/<sha256>/ for later joins and reinstalls.
# Input: {"url", "sha256", "size_bytes", "name"}. Result: {"path"}

cd $(dirname $0)
source ../../cloudrc
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input url sha size name dir file pid have pct
    input=$(cat)
    url=$(jq -r .url <<<"$input")
    sha=$(jq -r .sha256 <<<"$input")
    size=$(jq -r .size_bytes <<<"$input")
    name=$(jq -r .name <<<"$input")
    [[ "$sha" =~ ^[0-9a-f]{64}$ ]] || stc_fail "invalid sha256"
    [[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || stc_fail "invalid file name"
    [[ "$size" =~ ^[0-9]+$ ]] || stc_fail "invalid size"
    [[ "$url" =~ ^https?:// ]] || stc_fail "invalid url"
    dir=$cache_dir/storage-pkg/$sha
    file=$dir/$name
    mkdir -p $dir
    stc_lock "fetch-${sha:0:16}"
    if [ -f $file ] && [ "$(stat -c %s $file)" = "$size" ]; then
        stc_progress 50 "checking the cached copy"
        if [ "$(sha256sum $file | cut -d' ' -f1)" = "$sha" ]; then
            stc_result "$(jq -cn --arg p "$file" '{path: $p, cached: true}')"
            return 0
        fi
        rm -f $file
    fi
    # The URL holds the signature: keep it off the log. A stalled transfer (the network gone) is given up after a
    # minute under 1 KiB/s instead of hanging on TCP retransmissions for a quarter of an hour
    curl -fsS --connect-timeout 30 --speed-limit 1024 --speed-time 60 --retry 3 --retry-delay 5 -o $file.partial "$url" &
    pid=$!
    while kill -0 $pid 2>/dev/null; do
        have=$(stat -c %s $file.partial 2>/dev/null || echo 0)
        pct=$((size > 0 ? have * 90 / size : 0))
        stc_progress $pct "downloaded $((have / 1048576)) of $((size / 1048576)) MiB"
        sleep 5
    done
    wait $pid || { rm -f $file.partial; stc_fail "the download failed"; }
    [ "$(stat -c %s $file.partial)" = "$size" ] || { rm -f $file.partial; stc_fail "the download has the wrong size"; }
    stc_progress 92 "checking the SHA-256"
    [ "$(sha256sum $file.partial | cut -d' ' -f1)" = "$sha" ] || { rm -f $file.partial; stc_fail "the download has the wrong SHA-256"; }
    mv -f $file.partial $file
    stc_result "$(jq -cn --arg p "$file" '{path: $p, cached: false}')"
}

stc_run "$@"
