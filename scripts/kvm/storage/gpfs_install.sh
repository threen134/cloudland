#!/bin/bash
# Install GPFS from a fetched installer (shared-storage-design.md §6.1, §7.2): take the packages out of its tar.gz
# payload instead of running the self-extracting script (no Java, no root shell script of 1.7 GB), check them against
# the md5 sums of its manifest and install them with apt, which brings their dependencies (gpfs.base needs
# iputils-arping, which 24.04 does not have). Input: {"sha256", "name", "payload_line", "version", "debs": {file: md5}}

cd $(dirname $0)
source ../../cloudrc
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input sha name line version file tmp members f md5 have aio
    input=$(cat)
    sha=$(jq -r .sha256 <<<"$input")
    name=$(jq -r .name <<<"$input")
    line=$(jq -r .payload_line <<<"$input")
    version=$(jq -r .version <<<"$input")
    [[ "$sha" =~ ^[0-9a-f]{64}$ ]] || stc_fail "invalid sha256"
    [[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || stc_fail "invalid file name"
    [[ "$line" =~ ^[0-9]+$ ]] || stc_fail "invalid payload line"
    stc_lock "gpfs-install"
    export DEBIAN_FRONTEND=noninteractive
    apt-cache show libaio1t64 >/dev/null 2>&1 && aio=libaio1t64 || aio=libaio1
    if [ "$(dpkg-query -W -f='${Version}' gpfs.base 2>/dev/null)" = "$version" ] &&
        [ "$(dpkg-query -W -f='${Status}' gpfs.base 2>/dev/null)" = "install ok installed" ]; then
        echo "gpfs.base $version is installed already"
    else
        file=$cache_dir/storage-pkg/$sha/$name
        [ -f $file ] || stc_fail "the installer was not fetched"
        tmp=$cache_tmp_dir/gpfs-install-$STC_RUN
        rm -rf $tmp && mkdir -p $tmp
        members=$(jq -r '.debs | keys[] | "gpfs_debs/" + .' <<<"$input")
        stc_progress 10 "taking the packages out of the installer"
        tail -n +$line $file | tar -xzf - -C $tmp $members || { rm -rf $tmp; stc_fail "the packages could not be taken out of the installer"; }
        for f in $(jq -r '.debs | keys[]' <<<"$input"); do
            md5=$(jq -r --arg f "$f" '.debs[$f]' <<<"$input")
            have=$(md5sum $tmp/gpfs_debs/$f | cut -d' ' -f1)
            [ "$have" = "$md5" ] || { rm -rf $tmp; stc_fail "$f does not match the md5 of the manifest"; }
        done
        stc_progress 40 "installing"
        apt-get -o DPkg::Lock::Timeout=600 update -q || echo "apt-get update failed, going on with the lists at hand"
        apt-get -o DPkg::Lock::Timeout=600 install -y -q build-essential "linux-headers-$(uname -r)" ksh m4 $aio python3 iputils-arping \
            $tmp/gpfs_debs/*.deb || { rm -rf $tmp; stc_fail "apt-get install failed"; }
        rm -rf $tmp
    fi
    # The build tools may be missing when gpfs.base was there already (installed by hand, or a failed run)
    apt-get -o DPkg::Lock::Timeout=600 install -y -q build-essential "linux-headers-$(uname -r)" ksh m4 $aio python3 iputils-arping >/dev/null ||
        stc_fail "installing the build tools failed"
    [ "$(dpkg-query -W -f='${Status}' gpfs.base 2>/dev/null)" = "install ok installed" ] || stc_fail "gpfs.base is not installed"
    touch $run_dir/storage/gpfs.cloudland
    stc_result "$(jq -cn --arg v "$(dpkg-query -W -f='${Version}' gpfs.base)" '{version: $v}')"
}

stc_run "$@"
