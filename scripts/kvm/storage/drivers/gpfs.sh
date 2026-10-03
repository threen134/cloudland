# -*- mode: sh -*-
# Driver of the pools on GPFS (shared-storage-design.md §9.4): a pool is an independent fileset, its root the junction
# /gpfs/<fs>/<fileset>, which is not a mount point (stat -f still says gpfs). The rest is the common part of the file
# pools.

source "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/file.sh"

drv_name=gpfs
# At its quota GPFS answers EDQUOT, and QEMU only pauses by itself on ENOSPC: stop at any write error instead of
# handing an I/O error to the guest (§9.5, V8)
drv_error_policy=stop
