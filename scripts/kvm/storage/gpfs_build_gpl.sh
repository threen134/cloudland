#!/bin/bash
# Build the GPFS portability layer for the running kernel (mmbuildgpl, shared-storage-design.md §7.2). Input: {}

cd $(dirname $0)
source ../../cloudrc
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local kernel=$(uname -r) m
    cat >/dev/null
    stc_lock "gpfs-install"
    if [ -f /lib/modules/$kernel/extra/mmfs26.ko ] && [ -f /lib/modules/$kernel/extra/mmfslinux.ko ] && [ -f /lib/modules/$kernel/extra/tracedev.ko ]; then
        echo "the modules of $kernel are built already"
    else
        stc_progress 10 "building the modules for $kernel"
        /usr/lpp/mmfs/bin/mmbuildgpl || stc_fail "mmbuildgpl failed for kernel $kernel"
    fi
    for m in mmfs26 mmfslinux tracedev; do
        [ "$(modinfo -F vermagic /lib/modules/$kernel/extra/$m.ko 2>/dev/null | awk '{print $1}')" = "$kernel" ] || stc_fail "$m.ko is not built for $kernel"
    done
    stc_result "$(jq -cn --arg k "$kernel" '{kernel: $k}')"
}

stc_run "$@"
