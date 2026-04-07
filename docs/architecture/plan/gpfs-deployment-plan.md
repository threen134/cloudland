# GPFS（IBM Spectrum Scale）在 CloudLand 环境中的部署与集成方案

## 一、背景与架构定位

### 1.1 当前 CloudLand 存储架构

```
控制节点（Docker Compose）
  └─ cloudland 容器  ──SCI──► Hypervisor 宿主机
                                  └─ cloudlet 进程（宿主机）
                                  └─ KVM 脚本（宿主机）
                                       └─ 读写 /opt/cloudland/cache/volume/  ← 当前本地存储
                                       └─ 读写 /opt/cloudland/cache/instance/
                                       └─ 读写 /opt/cloudland/cache/image/
```

### 1.2 引入 GPFS 后的架构

```
控制节点（Docker Compose）              ┌─ GPFS 服务节点
  └─ cloudland 容器（不变）             │   mmfsd (NSD Server)
                                        │   /dev/sdb, /dev/sdc ...
                                        │
Hypervisor-01 宿主机                    │
  └─ cloudlet + KVM 脚本 ─────────────►│  /mnt/gpfs/cloudland/  (挂载 GPFS)
  └─ /mnt/gpfs/cloudland/volumes/      │       ├── volumes/
  └─ /mnt/gpfs/cloudland/instances/    │       ├── instances/
  └─ /mnt/gpfs/cloudland/images/       │       └── images/
                                        │
Hypervisor-02 宿主机 ───────────────────┘ (同一命名空间，所有节点可见)
Hypervisor-N  宿主机 ───────────────────┘
```

**核心优势**：所有 Hypervisor 节点看到同一个 GPFS 命名空间，VM 磁盘文件天然共享，Live Migration 无需传输磁盘数据。

---

## 二、前提条件与规划

### 2.1 节点角色规划

| 节点类型 | 数量 | 职责 | 是否需要改动 |
|---|---|---|---|
| 控制节点（当前 Docker Compose 节点）| 1 | 运行 CloudLand 控制面，不直接访问 VM 磁盘 | **不需要改动** |
| GPFS 服务节点（NSD Server）| 建议 ≥ 2 | 提供底层块设备给 GPFS 文件系统 | 全新安装 GPFS |
| Hypervisor 节点 | N | 运行 VM，KVM 脚本读写 GPFS 挂载点 | 安装 GPFS 客户端 + 修改 `cloudrc.local` |

> **最小化部署**：GPFS 服务节点可以与某台 Hypervisor 合并（单节点）用于测试，生产环境建议独立。

### 2.2 硬件与系统要求

**GPFS 服务节点**：
- OS：Ubuntu 22.04（与现有 Hypervisor 保持一致）
- 内存：≥ 8GB
- 存储：独立磁盘或 LUN（不使用系统盘），建议 SSD 或 NVMe
- 网络：建议独立存储网络（10GbE+），与业务 VXLAN 网络隔离

**Hypervisor 节点（客户端）**：
- 内核版本固定（GPFS 内核模块与内核强绑定，**升级内核前必须确认 GPFS 兼容性**）
- 安装 `make`, `gcc`, `kernel-devel` 等编译工具（构建内核模块用）
- 存储网络通达 GPFS 服务节点

### 2.3 GPFS License 选择

| 版本 | 限制 | 适用场景 |
|---|---|---|
| Developer Edition（免费）| 12TB 容量上限，不限节点 | 测试、验证、小规模生产 |
| Standard（商业）| 按容量授权 | 生产环境 |
| Data Management（商业）| 含 HSM、ILM 等高级特性 | 数据密集型场景 |

**建议**：先用 Developer Edition 跑通完整流程，确认满足需求后再购买商业 License。

---

## 三、GPFS 服务节点安装

### 3.1 获取安装包

从 IBM Fix Central 下载 Spectrum Scale：

```
https://www.ibm.com/support/fixcentral
产品：IBM Spectrum Scale (GPFS)
版本：5.1.x.x（建议最新稳定版）
平台：Linux x86_64
包名示例：Spectrum_Scale_Developer-5.1.9.0-x86_64-Linux-install
```

### 3.2 在所有 GPFS 节点（服务节点 + Hypervisor）安装基础包

```bash
# 在每个参与 GPFS 集群的节点上执行

# 1. 安装依赖
apt-get update
apt-get install -y build-essential linux-headers-$(uname -r) \
    ksh python3 python3-paramiko libaio1 numactl \
    cpp gcc g++ make m4 libc-dev libglib2.0-dev rpm

# 2. 解压并安装 GPFS
chmod +x Spectrum_Scale_Developer-5.1.9.0-x86_64-Linux-install
./Spectrum_Scale_Developer-5.1.9.0-x86_64-Linux-install --silent

# 验证安装
/usr/lpp/mmfs/bin/mmversion
```

### 3.3 创建 GPFS 集群（在主服务节点上执行）

```bash
# 配置 SSH 互信（所有 GPFS 节点之间）
# GPFS 集群内节点必须 SSH 免密互通
ssh-keygen -t rsa -N "" -f ~/.ssh/id_rsa
# 将公钥分发到所有节点
for node in gpfs-server-01 gpfs-server-02 hyper-01 hyper-02 hyper-03; do
    ssh-copy-id root@$node
done

# 创建 GPFS 集群
# 格式：节点名:角色 (manager-quorum 表示参与仲裁和管理)
mmcrcluster \
    -N gpfs-server-01:manager-quorum,gpfs-server-02:manager-quorum \
    -r /usr/bin/ssh \
    -R /usr/bin/scp \
    --ccr-disable \
    -C cloudland-gpfs

# 将 Hypervisor 节点加入集群（作为客户端节点）
mmaddnode -N hyper-01,hyper-02,hyper-03

# 接受 License
mmchlicense server --accept -N gpfs-server-01,gpfs-server-02
mmchlicense client --accept -N hyper-01,hyper-02,hyper-03

# 启动集群
mmstartup -a

# 验证集群状态
mmgetstate -a
```

### 3.4 创建 NSD（Network Shared Disk）和文件系统

```bash
# 在主服务节点上操作

# 1. 准备 NSD 描述文件 /tmp/nsd_config.txt
# 格式：
#   %nsd:
#    device=<块设备路径>
#    servers=<NSD服务节点>
#    usage=dataAndMetadata
#    failureGroup=<故障组编号>
#    pool=system

cat > /tmp/nsd_config.txt <<'EOF'
%nsd:
 device=/dev/sdb
 servers=gpfs-server-01
 usage=dataAndMetadata
 failureGroup=1
 pool=system

%nsd:
 device=/dev/sdb
 servers=gpfs-server-02
 usage=dataAndMetadata
 failureGroup=2
 pool=system
EOF

# 2. 创建 NSD
mmcrnsd -F /tmp/nsd_config.txt

# 验证 NSD
mmlsnsd

# 3. 创建 GPFS 文件系统
#   -F: NSD 配置文件
#   -B: 块大小（VM 场景建议 4M，大文件读写效率高）
#   -m 1: 元数据副本数
#   -r 1: 数据副本数（如果有 2 个故障组可设为 2）
#   -T: 挂载点
mmcrfs cloudland \
    -F /tmp/nsd_config.txt \
    -B 4M \
    -m 1 \
    -r 1 \
    -T /mnt/gpfs/cloudland \
    --inode-limit 10000000

# 4. 在所有节点挂载
mmmount cloudland -a

# 验证挂载
df -h /mnt/gpfs/cloudland
mmlsmount cloudland -L
```

### 3.5 创建目录结构

```bash
# 在 GPFS 文件系统上创建 CloudLand 所需目录
mkdir -p /mnt/gpfs/cloudland/{volumes,instances,images,meta,backup}
chmod 755 /mnt/gpfs/cloudland
chown -R cland:cland /mnt/gpfs/cloudland

# 验证
ls -la /mnt/gpfs/cloudland/
```

### 3.6 配置开机自动挂载

```bash
# 在所有节点（服务节点 + Hypervisor）上

# 配置 GPFS 开机自启
systemctl enable gpfs

# 配置挂载点在 GPFS 启动后自动挂载
# 编辑 /etc/fstab 或通过 GPFS 自身机制
mmchfs cloudland -A yes   # 自动挂载

# 验证：重启后检查
# mmgetstate && df -h /mnt/gpfs/cloudland
```

---

## 四、Hypervisor 节点客户端配置

### 4.1 加入集群并挂载（同第三章 3.3 步骤）

每台新增的 Hypervisor 节点需要：

```bash
# 1. 安装 GPFS 包（同 3.2）
./Spectrum_Scale_Developer-5.1.9.0-x86_64-Linux-install --silent

# 2. 从主节点将其加入集群
# 在主服务节点执行：
mmaddnode -N <new-hyper-ip>
mmchlicense client --accept -N <new-hyper-ip>
mmstartup -N <new-hyper-ip>
mmmount cloudland -N <new-hyper-ip>
```

### 4.2 修改 CloudLand 的 cloudrc.local

这是 CloudLand 集成 GPFS 的**唯一核心改动**，修改 `/opt/cloudland/scripts/cloudrc.local`：

```bash
# 原有配置（local 存储）：
# volume_dir=/opt/cloudland/cache/volume   ← 这行是 cloudrc 默认值，不需要显式写

# 修改为 GPFS 路径（在 cloudrc.local 中覆盖默认值）：
volume_dir=/mnt/gpfs/cloudland/volumes
image_dir=/mnt/gpfs/cloudland/instances
image_cache=/mnt/gpfs/cloudland/images
```

**就这三行**。所有 KVM 脚本（`create_volume_local.sh`、`attach_volume_local.sh`、`resize_volume_local.sh`、`clear_volume_local.sh` 等）内部都使用 `$volume_dir` / `$image_dir` 变量，路径换了脚本行为完全不变。

### 4.3 在 deploy-compute-node.sh 中集成 GPFS 初始化

在现有部署脚本的第 8 步（配置 cloudrc.local）后增加 GPFS 相关配置：

```bash
# ============ 8.5 GPFS 客户端配置（可选，按需启用）============
GPFS_ENABLED="${GPFS_ENABLED:-no}"
GPFS_MASTER="${GPFS_MASTER:-}"        # GPFS 主服务节点 IP
GPFS_MOUNT="${GPFS_MOUNT:-/mnt/gpfs/cloudland}"

if [ "$GPFS_ENABLED" = "yes" ] && [ -n "$GPFS_MASTER" ]; then
    log "配置 GPFS 客户端"

    # 安装 GPFS 包（需提前将安装包放到固定路径或 HTTP 服务器）
    GPFS_PKG="${GPFS_PKG:-/opt/packages/Spectrum_Scale_Developer-5.1.9.0-x86_64-Linux-install}"
    if [ -f "$GPFS_PKG" ]; then
        chmod +x "$GPFS_PKG"
        "$GPFS_PKG" --silent
    else
        warn "GPFS 安装包不存在: $GPFS_PKG，跳过 GPFS 安装"
    fi

    # 由主节点将本节点加入集群（需要主节点上提前运行 mmaddnode）
    # 这里只做本地挂载点准备
    mkdir -p "$GPFS_MOUNT"/{volumes,instances,images,meta,backup}
    chown -R cland:cland "$GPFS_MOUNT"

    # 更新 cloudrc.local 中的存储路径
    cat >> "$CLOUDLAND_DIR/scripts/cloudrc.local" <<EOF

# GPFS 存储路径（覆盖 cloudrc 默认值）
volume_dir=$GPFS_MOUNT/volumes
image_dir=$GPFS_MOUNT/instances
image_cache=$GPFS_MOUNT/images
EOF

    log "GPFS 存储路径已配置，挂载点: $GPFS_MOUNT"
fi
```

---

## 五、Live Migration 改造

### 5.1 当前限制

查看 `scripts/kvm/target_migration.sh`，当前代码：

```bash
if [ -z "$wds_address" ]; then
    state="not_supported"   # ← local 存储不支持迁移
    echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$SCI_CLIENT_ID' '$state'"
    exit 0
fi
```

**local 存储完全不支持 Live Migration**，而 GPFS 天然支持——两台 Hypervisor 都能看到同一块磁盘文件，不需要任何数据传输。

### 5.2 改造 target_migration.sh

新增 GPFS 分支，复用 local 存储的 VM 定义逻辑但跳过磁盘传输：

```bash
# 在 target_migration.sh 开头判断存储后端
if [ -n "$wds_address" ]; then
    # 走现有 WDS 逻辑（保持不变）
    ...
elif [ -n "$gpfs_mount" ]; then
    # GPFS 分支：磁盘文件已在共享存储上，只需准备 XML 和网络配置
    md=$(cat)
    metadata=$(echo $md | base64 -d)
    mkdir -p $xml_dir/$vm_ID

    ./build_meta.sh "$vm_ID" "$vm_name" <<< $md >/dev/null 2>&1
    vm_meta=$cache_dir/meta/$vm_ID.iso

    # 直接使用 GPFS 上的磁盘文件（已存在，无需复制）
    vm_img=$image_dir/$vm_ID.disk

    # 复用 local 模板（GPFS 用 file 类型磁盘，与 local 相同）
    template=$template_dir/template_with_qa.xml
    [ "$boot_loader" = "uefi" ] && template=$template_dir/template_uefi_with_qa.xml

    vm_xml=$xml_dir/$vm_ID/${vm_ID}.xml
    cp $template $vm_xml

    # ... sed 替换（同 launch_vm.sh 逻辑）

    os_code=$(jq -r '.os_code' <<< $metadata)
    jq .vlans <<< $metadata | ./sync_nic_info.sh "$ID" "$vm_name" "$os_code"

    if [ "$migration_type" = "cold" ]; then
        virsh define $vm_xml
        virsh autostart $vm_ID --disable
    fi

    state="target_prepared"
    echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$SCI_CLIENT_ID' '$state'"
    async_exec ./async_job/complete_migration.sh "$migrate_ID" "$task_ID" "$ID"
else
    state="not_supported"
    echo "|:-COMMAND-:| migrate_vm.sh '$migrate_ID' '$task_ID' '$ID' '$SCI_CLIENT_ID' '$state'"
fi
```

### 5.3 改造 source_migration.sh

`source_migration.sh` 的主体逻辑（`virsh migrate`）本身就不依赖存储类型，**只需确保目标节点能访问到磁盘文件**——用 GPFS 时这个条件天然满足，无需修改。

但需要在 `cloudrc` 中将 `gpfs_mount` 变量暴露出来，让脚本能判断当前存储后端：

```bash
# 在 cloudrc.local 中补充
gpfs_mount=/mnt/gpfs/cloudland
```

---

## 六、镜像管理适配

### 6.1 镜像上传流程（当前）

```
用户上传 qcow2 → cpgateway → 写入控制节点的 image_cache（Docker Volume）
                            → Hypervisor 节点通过 sync_image 拉取到本地 image_cache
```

### 6.2 引入 GPFS 后的镜像流程

```
用户上传 qcow2 → cpgateway → 写入 /mnt/gpfs/cloudland/images/
                → 所有 Hypervisor 节点立即可见，无需同步
```

关键：控制节点的 `nginx` 容器当前挂载了 `image_cache` 这个 Docker named volume 用于提供镜像 HTTP 下载服务。如果引入 GPFS，**控制节点本身不需要挂载 GPFS**，可以保持现有 Docker 流程不变，镜像在每个 Hypervisor 本地 `image_cache` 和 GPFS 上各维护一份，或者将两者统一（完全切换到 GPFS，控制节点通过 NFS re-export 或 sftp 上传到 GPFS）。

**推荐方案**（最简单）：保持控制节点现有 image_cache 不变（仍用 Docker Volume），Hypervisor 上的 `image_cache` 路径改为 GPFS，镜像通过现有的 `sync_image_info.sh` 从控制节点下载到 GPFS 路径，由于 GPFS 共享，第一台 Hypervisor 下载后其他节点立即可用。

---

## 七、监控集成

### 7.1 GPFS 指标采集

GPFS 自带 `mmperfmon` 和 `mmhealth` 工具，通过 `node_exporter` 的 textfile collector 导出到 Prometheus：

```bash
# 在每个 Hypervisor 节点上创建采集脚本
cat > /opt/cloudland/scripts/monitor/export_gpfs_metrics.sh <<'EOF'
#!/bin/bash
# 输出到 node_exporter textfile 目录
OUTPUT=/var/lib/node_exporter/gpfs.prom
TMP=$OUTPUT.tmp

# 文件系统挂载状态
mounted=$(mmlsmount cloudland 2>/dev/null | grep -c "cloudland" || echo 0)
echo "gpfs_filesystem_mounted{fs=\"cloudland\"} $mounted" > $TMP

# 文件系统容量
mmlsfs cloudland --block-size auto 2>/dev/null | awk '
/Total data in file system/ {
    split($NF, a, " ")
    print "gpfs_filesystem_total_bytes{fs=\"cloudland\"} " a[1]
}
/Free full blocks/ {
    split($NF, a, " ")
    print "gpfs_filesystem_free_bytes{fs=\"cloudland\"} " a[1]
}'  >> $TMP

# 节点健康状态
mmhealth node show --output COMPONENT,STATUS 2>/dev/null | awk '
NR>1 {
    status = ($2 == "HEALTHY") ? 1 : 0
    print "gpfs_component_healthy{component=\"" $1 "\"} " status
}' >> $TMP

mv $TMP $OUTPUT
EOF

chmod +x /opt/cloudland/scripts/monitor/export_gpfs_metrics.sh

# 添加 systemd timer（2 分钟采集一次，对齐现有 exporter 节奏）
cat > /etc/systemd/system/gpfs-metrics-exporter.service <<EOF
[Unit]
Description=Export GPFS metrics for Prometheus
[Service]
Type=oneshot
ExecStart=/opt/cloudland/scripts/monitor/export_gpfs_metrics.sh
EOF

cat > /etc/systemd/system/gpfs-metrics-exporter.timer <<EOF
[Unit]
Description=Run GPFS metrics exporter every 2 minutes
[Timer]
OnBootSec=30
OnUnitActiveSec=2min
[Install]
WantedBy=timers.target
EOF

systemctl daemon-reload
systemctl enable --now gpfs-metrics-exporter.timer
```

### 7.2 Prometheus 告警规则

在 `deploy/docker/volumes/prometheus/general_rules/` 下新增 `gpfs_rules.yml`：

```yaml
groups:
  - name: gpfs
    rules:
      - alert: GPFSFilesystemUnmounted
        expr: gpfs_filesystem_mounted{fs="cloudland"} == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "GPFS 文件系统未挂载"
          description: "节点 {{ $labels.instance }} 上的 GPFS 文件系统 cloudland 未挂载，VM 磁盘操作将失败"

      - alert: GPFSFilesystemSpaceLow
        expr: gpfs_filesystem_free_bytes / gpfs_filesystem_total_bytes < 0.15
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "GPFS 文件系统剩余空间不足 15%"
          description: "文件系统 cloudland 剩余空间: {{ $value | humanizePercentage }}"

      - alert: GPFSComponentUnhealthy
        expr: gpfs_component_healthy == 0
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "GPFS 组件异常"
          description: "节点 {{ $labels.instance }} 的 GPFS 组件 {{ $labels.component }} 状态异常"
```

### 7.3 Grafana Dashboard

在 `deploy/docker/config/grafana/dashboards/` 下新增 `gpfs-overview.json`，关键面板：

| 面板名 | 指标 | 说明 |
|---|---|---|
| 文件系统挂载状态 | `gpfs_filesystem_mounted` | 各节点挂载状态热力图 |
| 容量使用率 | `gpfs_filesystem_free_bytes / total` | 整体容量趋势 |
| 组件健康状态 | `gpfs_component_healthy` | NSD、DISK、NETWORK 等组件状态 |

---

## 八、日常运维操作手册

### 8.1 常用运维命令

```bash
# 集群状态总览
mmgetstate -a                    # 各节点 GPFS 状态
mmhealth cluster show            # 集群健康状态
mmdf cloudland                   # 文件系统容量使用

# 挂载/卸载
mmmount cloudland -a             # 所有节点挂载
mmumount cloudland -a            # 所有节点卸载（需先停止所有 VM）
mmmount cloudland -N hyper-01    # 单节点挂载

# 性能诊断
mmpmon input stats_all reset     # 重置性能计数
mmpmon input stats_all           # 查看 IO 统计

# 节点管理
mmgetstate -a                    # 查看所有节点状态
mmstartup -N hyper-03            # 启动单节点 GPFS 服务
mmshutdown -N hyper-03           # 停止单节点 GPFS 服务
```

### 8.2 新增 Hypervisor 节点流程

```bash
# 1. 在新节点安装 GPFS 包
./Spectrum_Scale_Developer-5.1.x.x-x86_64-Linux-install --silent

# 2. 在主服务节点将新节点加入集群
mmaddnode -N <new-hyper-ip>
mmchlicense client --accept -N <new-hyper-ip>
mmstartup -N <new-hyper-ip>
mmmount cloudland -N <new-hyper-ip>

# 3. 修改新节点的 cloudrc.local（配置 GPFS 路径）
# 按第四章 4.2 步骤操作

# 4. 通过 CloudLand API 注册计算节点（现有流程不变）
```

### 8.3 Hypervisor 节点故障处理

```bash
# 场景：某个 Hypervisor 宕机，其上的 VM 需要迁移到其他节点

# 1. 检查 GPFS 状态（其他节点仍可访问磁盘）
mmgetstate -a

# 2. 通过 CloudLand API 将故障节点上的 VM 冷迁移到其他节点
# 由于磁盘在 GPFS 共享，其他节点直接能读到磁盘文件
# virsh define + virsh start 在目标节点即可

# 3. 故障节点恢复后重新挂载 GPFS
mmstartup -N <recovered-hyper>
mmmount cloudland -N <recovered-hyper>
```

### 8.4 内核升级注意事项

**GPFS 内核模块与 Linux 内核版本强绑定，升级内核是高风险操作**：

```bash
# 升级前步骤（每个 Hypervisor 节点）：
# 1. 先将节点上所有 VM 迁移或停止
# 2. 卸载 GPFS
mmumount cloudland -N <this-node>
mmshutdown -N <this-node>

# 3. 检查新内核版本是否有对应的 GPFS 内核模块
#    访问 IBM Fix Central 查询兼容矩阵
#    https://www.ibm.com/support/fixcentral

# 4. 升级内核
apt-get install linux-image-<new-version>
reboot

# 5. 重装 GPFS 内核模块（针对新内核）
/usr/lpp/mmfs/bin/mmbuildgpl

# 6. 重新启动 GPFS
mmstartup -N <this-node>
mmmount cloudland -N <this-node>
```

### 8.5 GPFS 服务节点（NSD Server）故障处理

```bash
# 查看 NSD 状态
mmlsnsd -X

# 如果一台 NSD Server 宕机（有两台时）
mmgetstate -a          # 确认集群状态
mmlsfs cloudland       # 确认文件系统仍然可用（副本机制）

# 修复宕机节点后
mmstartup -N <nsd-server>
mmhealth cluster show   # 确认恢复 HEALTHY
```

---

## 九、存量数据迁移

如果已有 Hypervisor 节点在使用本地存储（`/opt/cloudland/cache/volume/`），需要将数据迁移到 GPFS：

```bash
#!/bin/bash
# migrate_to_gpfs.sh：将本地卷迁移到 GPFS（离线迁移，需先停止 VM）

SRC_VOLUME_DIR=/opt/cloudland/cache/volume
SRC_IMAGE_DIR=/opt/cloudland/cache/instance
DST_VOLUME_DIR=/mnt/gpfs/cloudland/volumes
DST_IMAGE_DIR=/mnt/gpfs/cloudland/instances

echo "开始迁移卷文件..."
# 使用 rsync 保留属性，断点续传
rsync -avh --progress "$SRC_VOLUME_DIR/" "$DST_VOLUME_DIR/"

echo "开始迁移实例磁盘..."
rsync -avh --progress "$SRC_IMAGE_DIR/" "$DST_IMAGE_DIR/"

echo "迁移完成，请更新 cloudrc.local 并重启 cloudlet 服务"
```

**迁移步骤**：
1. 停止该 Hypervisor 上所有 VM（`virsh shutdown` / `virsh destroy`）
2. 运行 `migrate_to_gpfs.sh`
3. 更新 `cloudrc.local` 中的路径变量
4. 重启 `cloudlet` 服务
5. 重新启动 VM

---

## 十、方案总结与决策建议

### GPFS 适用场景

| 场景 | 适合程度 | 说明 |
|---|---|---|
| VM Live Migration 频繁 | ★★★★★ | GPFS 最大价值点，无磁盘传输 |
| Hypervisor 节点故障快速恢复 | ★★★★★ | 其他节点直接接管磁盘 |
| 镜像统一分发 | ★★★★☆ | 一次下载，所有节点可用 |
| 超大规模存储（PB 级）| ★★★★★ | GPFS 原本的设计场景 |
| 小规模（< 5 台 Hypervisor）| ★★★☆☆ | 可用但成本偏高，Ceph 更灵活 |

### 关键风险备忘

| 风险 | 级别 | 缓解措施 |
|---|---|---|
| GPFS License 成本 | 高 | 先用 Developer Edition（12TB 免费）验证 |
| 内核升级兼容性 | 高 | 固定内核版本策略，升级前查兼容矩阵 |
| NSD Server 单点 | 高 | 部署两台 NSD Server + 副本 |
| GPFS 挂载失败导致所有 VM 不可用 | 极高 | 存储网络冗余 + 告警及时发现 |
| 初始学习成本 | 中 | IBM 文档完整，但命令体系与 Linux 传统工具差异大 |
