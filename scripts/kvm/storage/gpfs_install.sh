#!/bin/bash
# Install GPFS from a fetched installer (shared-storage-design.md §6.1, §7.2): take the packages out of its tar.gz
# payload instead of running the self-extracting script (no Java, no root shell script of 1.7 GB), check them against
# the md5 sums of its manifest and install them with apt, which brings their dependencies (gpfs.base needs
# iputils-arping, which 24.04 does not have). Input: {"sha256", "name", "payload_line", "version", "debs": {file: md5}}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input
    input=$(cat)
    stc_lock "gpfs-install"
    gpfs_install_packages "$input"
    touch $run_dir/storage/gpfs.cloudland
    stc_result "$(jq -cn --arg v "$(dpkg-query -W -f='${Version}' gpfs.base)" '{version: $v}')"
}

stc_run "$@"
