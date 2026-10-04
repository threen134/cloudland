# -*- mode: sh -*-
# Driver of the pools on GPFS (shared-storage-design.md §9.4): a pool is an independent fileset, its root the junction
# /gpfs/<fs>/<fileset>, which is not a mount point (stat -f still says gpfs). The rest is the common part of the file
# pools.

source "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/file.sh"

drv_name=gpfs
# At its quota GPFS answers EDQUOT, and QEMU only pauses by itself on ENOSPC: stop at any write error instead of
# handing an I/O error to the guest (§9.5, V8)
drv_error_policy=stop
drv_mmclone=${gpfs_bin:-/usr/lpp/mmfs/bin}/mmclone

# drv_base_seal <file>: the finished import becomes a clone parent, read-only from now on (V4: it can be renamed, it
# can not be removed while it has clones, clones must be in the same fileset)
function drv_base_seal()
{
    if ! timeout 600 $drv_mmclone snap "$1" >/dev/null 2>&1; then
        guard_error="mmclone snap $1 failed"
        return 1
    fi
    return 0
}

# drv_clone_fast <copy> <file>: a writable clone of the copy, made at once whatever its size
function drv_clone_fast()
{
    timeout 600 $drv_mmclone copy "$1" "$2" >/dev/null 2>&1 && return 0
    rm -f "$2" 2>/dev/null
    return 1
}
