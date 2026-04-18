#!/bin/bash
# image_cache_gc.sh — 清理 N 天未使用的本地镜像缓存
# 说明:
#   - 用 mtime 而非 atime（很多文件系统挂载 noatime/relatime，atime 不可靠）
#   - ensure_image_cached 下载完成后文件带上当前 mtime
#   - launch_vm.sh / reinstall_vm.sh / rescue_vm.sh 使用镜像前 touch 更新 mtime
# Cron 示例: 0 3 * * * /opt/cloudland/scripts/kvm/image_cache_gc.sh

cd $(dirname $0)
source ../cloudrc

MAX_AGE_DAYS=${IMAGE_CACHE_MAX_AGE:-30}

if [ ! -d "$image_cache" ]; then
    exit 0
fi

# 只清理普通文件（跳过锁文件、.partial 残留留给 ensure_image_cached 的 trap 处理）
find "$image_cache" -maxdepth 1 -type f -mtime +${MAX_AGE_DAYS} ! -name ".*.lock" ! -name "*.partial" -delete 2>/dev/null

# 顺带清理 30 天以上的残留 .partial（正常应由 trap 清理，这里兜底）
find "$image_cache" -maxdepth 1 -type f -mtime +${MAX_AGE_DAYS} -name "*.partial" -delete 2>/dev/null
