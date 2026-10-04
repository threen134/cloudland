# -*- mode: sh -*-
# Common part of the drivers of file pools (shared-storage-design.md §4.5.2): the pool is a directory of a shared file
# system mounted at the same place on every host, holding volumes/, images/, nvram/ and tmp/ (made with the pool), and
# a volume is the qcow2 file volumes/volume-<id>.disk. A driver of a file pool sources this file and changes what
# differs. Loaded by drv_load (storage_lib.sh); nothing here prints to stdout.

drv_family=file
# What libvirt does when a write fails: enospace pauses the instance when the file system is full, like local disks
drv_error_policy=enospace
# cache='none': libvirt refuses to migrate a disk on a shared file system with an unsafe cache mode
drv_cache=none

# drv_init <json>: the pool of the JSON clapi sent. Sets guard_error on failure
function drv_init()
{
    local json=$1
    drv_pool=$(jq -r '.pool // empty' <<<"$json" 2>/dev/null)
    drv_root=$(jq -r '.root // empty' <<<"$json" 2>/dev/null)
    drv_fs_type=$(jq -r '.fs_type // empty' <<<"$json" 2>/dev/null)
    if ! valid_uuid "$drv_pool"; then
        guard_error="invalid pool uuid '$drv_pool'"
        return 1
    fi
    if [[ "$drv_root" != /?*/* ]] || [ "$(realpath -m "$drv_root" 2>/dev/null)" != "$drv_root" ]; then
        guard_error="invalid pool root '$drv_root'"
        return 1
    fi
    return 0
}

# drv_volume <json> <volume id>: the volume of the JSON, which must be the file of that volume in this pool: clapi
# decides the path, the host checks it is the one it should be before writing or deleting anything (drv_vol)
function drv_volume()
{
    drv_vol=$(jq -r '.path // empty' <<<"$1" 2>/dev/null)
    if ! [[ "$2" =~ ^[0-9]+$ ]] || [ "$drv_vol" != "$drv_root/volumes/volume-$2.disk" ]; then
        guard_error="'$drv_vol' is not the file of volume $2 in pool $drv_pool"
        return 1
    fi
    return 0
}

# drv_guard: the pool is the one clapi means and it is there: the file system type of the root and the marker made
# with the pool. Without this check a pool whose file system is not mounted would be written on the root file system
# of the host. Sets guard_error on failure
function drv_guard()
{
    local type marker
    guard_error=""
    if [ -n "$drv_fs_type" ]; then
        type=$(timeout 10 stat -f -c %T "$drv_root" 2>/dev/null)
        if [ "$type" != "$drv_fs_type" ]; then
            guard_error="pool $drv_pool: $drv_root is not on a $drv_fs_type file system (${type:-not reachable})"
            return 1
        fi
    fi
    marker=$(timeout 10 cat "$drv_root/.cloudland-pool" 2>/dev/null)
    if ! grep -qx "driver=$drv_name" <<<"$marker" || ! grep -qx "pool_uuid=$drv_pool" <<<"$marker"; then
        guard_error="pool $drv_pool: the marker of $drv_root is missing or names another pool"
        return 1
    fi
    return 0
}

# drv_probe: whether the pool can be written now, and its room. Sets probe_json ({"size", "used", "avail"} in bytes)
# or guard_error. A file system with a quota per directory reports the quota as its size
function drv_probe()
{
    local f df
    probe_json=""
    drv_guard || return 1
    f=$drv_root/tmp/.probe-${NODE_ID:-0}-$$
    if ! timeout 20 touch "$f" 2>/dev/null; then
        guard_error="pool $drv_pool: $drv_root/tmp can not be written"
        return 1
    fi
    timeout 10 rm -f "$f"
    df=$(timeout 10 df -B1 --output=size,used,avail "$drv_root" 2>/dev/null | tail -1)
    if ! [[ "$df" =~ ^[[:space:]]*[0-9]+[[:space:]]+[0-9]+[[:space:]]+[0-9]+[[:space:]]*$ ]]; then
        guard_error="pool $drv_pool: df of $drv_root failed"
        return 1
    fi
    probe_json=$(awk '{printf "{\"size\":%s,\"used\":%s,\"avail\":%s}", $1, $2, $3}' <<<"$df")
}

function drv_exists()
{
    timeout 10 test -f "$1"
}

# drv_size <file>: virtual size in bytes; -U reads an image a running instance has open
function drv_size()
{
    qemu-img info -U --output=json "$1" 2>/dev/null | jq -r '."virtual-size" // empty'
}

# drv_create <file> <size GB>: a new qcow2, never over an existing file (the name of a volume is used once). Sets
# guard_error on failure; a file this call made is removed again
function drv_create()
{
    local path=$1 gb=$2
    if ! [[ "$gb" =~ ^[1-9][0-9]*$ ]]; then
        guard_error="invalid size '$gb'"
        return 1
    fi
    if [ ! -d "$(dirname "$path")" ]; then
        guard_error="pool $drv_pool has no $(dirname "$path")"
        return 1
    fi
    # Taken with noclobber first: two hosts can never both believe they made it
    if ! (set -C; : >"$path") 2>/dev/null; then
        guard_error="$path already exists"
        return 1
    fi
    if ! qemu-img create -q -f qcow2 -o cluster_size=2M "$path" ${gb}G >/dev/null 2>&1; then
        rm -f "$path"
        guard_error="qemu-img create $path failed"
        return 1
    fi
    return 0
}

# drv_resize <file> <size GB>: grow a volume nobody has open
function drv_resize()
{
    qemu-img resize -q "$1" "${2}G" >/dev/null 2>&1
}

# drv_delete <file>: remove the file of one volume; nothing else is ever removed (§12.3)
function drv_delete()
{
    if ! [[ "$(basename "$1")" =~ ^volume-[0-9]+\.disk$ ]] || [ "$(dirname "$1")" != "$drv_root/volumes" ]; then
        guard_error="$1 is not a volume file of pool $drv_pool"
        return 1
    fi
    if ! rm -f "$1"; then
        guard_error="failed to delete $1"
        return 1
    fi
    return 0
}

# drv_users <file>: who still has a volume open, as far as the pool can tell; a file does not say
function drv_users()
{
    return 0
}

# drv_disk_xml <file> <device>: the libvirt disk of a volume
function drv_disk_xml()
{
    printf "<disk type='file' device='disk'>\n   <driver name='qemu' type='qcow2' cache='%s' discard='unmap' error_policy='%s'/>\n   <source file='%s'/>\n   <target dev='%s' bus='virtio'/>\n</disk>\n" \
        "$drv_cache" "$drv_error_policy" "$1" "$2"
}

# --- Copies of images and the boot disks made from them (shared-storage-design.md §9.6, §9.7). The copy of an image
# is the qcow2 file images/image-<id>-<prefix>.qcow2; a boot disk is a copy of it here, a clone where the file system
# can (gpfs.sh)

# drv_base_check <copy>: the copy of an image as clapi names it in this pool. Sets guard_error on failure
function drv_base_check()
{
    if ! [[ "$(basename "$1")" =~ ^image-[0-9]+-[0-9a-f]+\.qcow2$ ]] || [ "$(dirname "$1")" != "$drv_root/images" ]; then
        guard_error="'$1' is not an image copy of pool $drv_pool"
        return 1
    fi
    return 0
}

function drv_base_exists()
{
    timeout 10 test -f "$1"
}

# drv_base_missing <copy>: the copy is surely not there; a pool that does not answer (timeout) is not that
function drv_base_missing()
{
    timeout 10 test -e "$1"
    [ $? -eq 1 ]
}

# drv_base_seal <file>: make a finished import what boot disks are made from; a plain file pool copies it as it is
function drv_base_seal()
{
    return 0
}

# drv_import_cleanup <copy>: temporary files of imports of this copy that died (unchanged for an hour)
function drv_import_cleanup()
{
    local name
    name=$(basename "$1" .qcow2)
    find "$drv_root/tmp" -maxdepth 1 -name "$name.*.import" -mmin +60 -delete 2>/dev/null
    return 0
}

# drv_import_image <image file> <format> <copy>: write the copy of an image into the pool, through a temporary file of
# this job moved into place at the end; a copy found in place already is kept. The check and the move hold a lock
# directory of the copy in tmp/ (mkdir is atomic, on GPFS across hosts too), so that two imports of the same copy do not
# both find it missing and the second replace the first: GPFS refuses a hard link to a clone parent, so link() can not
# put it in place. A lock older than 10 minutes was left by a job that died; one another import holds longer than a
# minute (drv_lock_tries half seconds) fails this one, which never touches that lock. Sets guard_error on failure
function drv_import_image()
{
    local src=$1 fmt=$2 base=$3 tmp lock i rc got=0
    tmp=$drv_root/tmp/$(basename "$base" .qcow2).${NODE_ID:-0}-$$.import
    lock=$drv_root/tmp/$(basename "$base" .qcow2).place
    if ! qemu-img convert -q -f "$fmt" -O qcow2 -o cluster_size=2M "$src" "$tmp" >/dev/null 2>&1; then
        rm -f "$tmp"
        guard_error="writing the image into $tmp failed"
        return 1
    fi
    if ! drv_base_seal "$tmp"; then
        rm -f "$tmp"
        return 1
    fi
    for i in $(seq 1 ${drv_lock_tries:-120}); do
        if mkdir "$lock" 2>/dev/null; then
            got=1
            break
        fi
        find "$lock" -maxdepth 0 -type d -mmin +10 -exec rmdir {} \; 2>/dev/null
        sleep 0.5
    done
    if [ $got = 0 ]; then
        rm -f "$tmp"
        guard_error="another import holds $lock"
        return 1
    fi
    rc=0
    if drv_base_exists "$base"; then
        rm -f "$tmp"
    elif ! mv -T "$tmp" "$base" 2>/dev/null; then
        rm -f "$tmp"
        guard_error="putting the copy in place as $base failed"
        rc=1
    fi
    rmdir "$lock" 2>/dev/null
    return $rc
}

# drv_base_delete <copy>: remove the copy of an image nothing is made from any more; one that is gone is fine
function drv_base_delete()
{
    drv_base_check "$1" || return 1
    drv_base_missing "$1" && return 0
    if ! timeout 10 test -e "$1"; then
        guard_error="pool $drv_pool did not answer looking for $1"
        return 1
    fi
    if ! rm -f "$1" 2>/dev/null; then
        guard_error="removing $1 failed: boot disks may still be cloned from it"
        return 1
    fi
    return 0
}

# drv_clone_fast <copy> <file>: a clone sharing the blocks of the copy; a plain file pool has none
function drv_clone_fast()
{
    return 1
}

# drv_clone <copy> <file> <clone|copy>: a new boot disk made from the copy of its image, never over an existing file.
# Sets drv_cloned (1 for a clone, 0 for a full copy), or guard_error on failure; a file this call made is removed again
function drv_clone()
{
    local base=$1 path=$2 mode=$3
    drv_cloned=0
    if [ ! -d "$(dirname "$path")" ]; then
        guard_error="pool $drv_pool has no $(dirname "$path")"
        return 1
    fi
    if drv_exists "$path"; then
        guard_error="$path already exists"
        return 1
    fi
    if [ "$mode" = "clone" ] && drv_clone_fast "$base" "$path"; then
        drv_cloned=1
        return 0
    fi
    # Taken with noclobber first: qemu-img convert would write over a file another host made meanwhile
    if ! (set -C; : >"$path") 2>/dev/null; then
        guard_error="$path already exists"
        return 1
    fi
    if ! qemu-img convert -q -f qcow2 -O qcow2 -o cluster_size=2M "$base" "$path" >/dev/null 2>&1; then
        rm -f "$path"
        guard_error="copying $base to $path failed"
        return 1
    fi
    return 0
}

# drv_temp_of <file>: where a reinstall makes the new boot disk of a volume before it replaces the old one
function drv_temp_of()
{
    echo "$drv_root/tmp/$(basename "$1" .disk).reinstall.disk"
}

# drv_drop <file>: remove a boot disk or the one a reinstall was making; nothing else
function drv_drop()
{
    if [ "$(dirname "$1")" = "$drv_root/tmp" ] && [[ "$(basename "$1")" =~ ^volume-[0-9]+\.reinstall\.disk$ ]]; then
        rm -f "$1" && return 0
        guard_error="failed to delete $1"
        return 1
    fi
    drv_delete "$1"
}

# drv_rename <from> <to>: the new boot disk of a reinstall takes the place of the old one
function drv_rename()
{
    if ! mv -T "$1" "$2" 2>/dev/null; then
        guard_error="moving $1 to $2 failed"
        return 1
    fi
    return 0
}

# drv_nvram_check <path> <instance id>: the UEFI variables of an instance are kept in nvram/ of its pool
function drv_nvram_check()
{
    if [ "$1" != "$drv_root/nvram/inst-$2_VARS.fd" ]; then
        guard_error="'$1' is not where pool $drv_pool keeps the UEFI variables of instance $2"
        return 1
    fi
    return 0
}
