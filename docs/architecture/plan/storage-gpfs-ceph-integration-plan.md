# GPFS / Ceph 存储集成与运维计划

## 背景与可行性

CloudLand 当前支持三种存储后端：
- **local**：本地 qcow2 文件（`/opt/cloudland/cache/volume/`）
- **WDS**（私有块存储）：通过 REST API + vhost/vSCSI 挂载
- **GlusterFS**：通过 QEMU 网络磁盘协议挂载

存储后端的选择通过 `cloudrc.local` 中的 `wds_address` 变量和 Go API 的 `volume.driver` 配置项决定。整体架构对存储后端扩展是开放的，**集成 Ceph RBD 和 GPFS 是可行的**，但需要在以下层次做工作：脚本层、Go API 层、配置层、部署层和运维层。

---

## 一、现有架构分析

### 存储选择逻辑（当前）

```
cloudrc.local
  └── wds_address 是否设置？
        ├── 是 → 使用 WDS vhost 路径（create_volume_wds_vhost.sh 等）
        └── 否 → 使用 local qcow2 路径（create_volume_local.sh 等）
```

Go API 层（`api/src/services/volume.go`）：
```go
func GetVolumeDriver() string {
    // 读取 volume.driver 配置，默认 "local"
}
```

调用链：Go API → SCI 消息 → cloudlet 节点 → shell 脚本

---

## 二、主要工作项

### 工作一：Ceph RBD 后端集成

#### 1.1 脚本层（`scripts/kvm/`）

| 需新增脚本 | 对应现有脚本 | 说明 |
|---|---|---|
| `create_volume_ceph.sh` | `create_volume_wds_vhost.sh` | 调用 `rbd create` 创建 RBD 块设备 |
| `attach_volume_ceph.sh` | `attach_volume_wds_vhost.sh` | 生成 libvirt RBD disk XML，调用 `virsh attach-device` |
| `detach_volume_ceph.sh` | `detach_volume_wds_vhost.sh` | 调用 `virsh detach-device` 并释放 RBD 资源 |
| `clear_volume_ceph.sh` | `clear_volume_wds_vhost.sh` | 调用 `rbd rm` 删除块设备 |
| `resize_volume_ceph.sh` | `resize_volume_wds_vhost.sh` | 调用 `rbd resize` 扩容 |
| `create_snapshot_ceph.sh` | `create_snapshot_wds_vhost.sh` | 调用 `rbd snap create` |
| `delete_snapshot_ceph.sh` | `delete_snapshot_wds_vhost.sh` | 调用 `rbd snap rm` |
| `clone_image_ceph.sh` | `clone_image.sh` | 基于 Ceph snapshot clone 创建启动盘 |

`launch_vm.sh` 中增加 Ceph 分支：
```bash
elif [ -n "$ceph_monitors" ]; then
    # 使用 Ceph RBD 作为 VM 启动盘
    template=$template_dir/ceph_template_with_qa.xml
fi
```

`cloudrc` 新增 Ceph 相关变量（写入 `cloudrc.local`）：
```bash
ceph_monitors="10.0.0.1:6789,10.0.0.2:6789,10.0.0.3:6789"
ceph_pool="cloudland"
ceph_user="cloudland"
ceph_keyring="/etc/ceph/ceph.client.cloudland.keyring"
```

#### 1.2 libvirt XML 模板（`scripts/xml/`）

新增 `ceph_template_with_qa.xml` 和 `ceph_template_uefi_with_qa.xml`，disk 部分改为：
```xml
<disk type='network' device='disk'>
  <driver name='qemu' type='raw' cache='writeback'/>
  <source protocol='rbd' name='CEPH_POOL/VOLUME_NAME'>
    <host name='CEPH_MON_HOST' port='6789'/>
  </source>
  <auth username='CEPH_USER'>
    <secret type='ceph' uuid='CEPH_SECRET_UUID'/>
  </auth>
  <target dev='vda' bus='virtio'/>
</disk>
```

需要在每个 hypervisor 节点预先注册 libvirt secret（`virsh secret-define`）。

#### 1.3 Go API 层（`api/src/services/volume.go`）

- 在 `GetVolumeDriver()` 支持返回 `"ceph"` 驱动
- `CreateVolume` 分支增加 Ceph 路径，调用对应 SCI 命令
- `volume.driver = ceph` 时，`vol_path` 格式：`rbd://pool/volume-name`
- Pool 管理：新增或复用 `StoragePool` 模型，支持 Ceph pool 类型

#### 1.4 数据库（Go API 模型层）

- `volumes` 表的 `path` 字段已经存储后端路径（`wds_vhost://pool/id`），Ceph 路径使用 `rbd://pool/name` 格式，无需加字段
- 如需多 Ceph 集群支持，`storage_pools` 表中增加 `ceph_cluster_id` 字段

---

### 工作二：GPFS（IBM Spectrum Scale）后端集成

GPFS 与 Ceph 的集成方式不同——GPFS 是一个共享并行文件系统，VM 磁盘镜像以文件形式存储在挂载点上（类似 GlusterFS），**不需要 libvirt 特殊磁盘协议**。

#### 2.1 脚本层（`scripts/kvm/`）

| 需新增脚本 | 对应现有脚本 | 说明 |
|---|---|---|
| `create_volume_gpfs.sh` | `create_volume_local.sh` | 在 GPFS 挂载目录创建 qcow2 文件 |
| `attach_volume_gpfs.sh` | `attach_volume_local.sh` | 生成标准 virtio-blk XML，使用 GPFS 路径 |
| `detach_volume_gpfs.sh` | `detach_volume_local.sh` | 从 VM 热拔盘 |
| `clear_volume_gpfs.sh` | `clear_volume_local.sh` | 删除 GPFS 上的卷文件 |
| `resize_volume_gpfs.sh` | `resize_volume_local.sh` | `qemu-img resize` GPFS 路径文件 |

`cloudrc` 新增 GPFS 变量：
```bash
gpfs_mount="/mnt/gpfs/cloudland"
gpfs_volume_dir="$gpfs_mount/volumes"
gpfs_image_dir="$gpfs_mount/images"
```

`launch_vm.sh` 增加 GPFS 分支：
```bash
elif [ -n "$gpfs_mount" ]; then
    vm_img=$gpfs_volume_dir/$vm_ID.disk
    # 逻辑同 local 路径，但路径指向 GPFS 挂载点
fi
```

#### 2.2 GPFS 的优势场景

- **实时迁移（live migration）无需共享存储**：所有节点挂载同一 GPFS 命名空间，`target_migration.sh` / `source_migration.sh` 无需数据复制
- **镜像统一分发**：image_cache 直接放在 GPFS 上，所有 hypervisor 节点共享

#### 2.3 Go API 层

- `volume.driver = gpfs` 时调用对应 SCI 命令，路径格式 `gpfs://volume-${vol_ID}.disk`
- 基本逻辑与 local driver 相同，主要区别在 `cloudrc.local` 中的路径变量

---

### 工作三：配置层统一（driver 选择机制）

当前 `wds_address` 是隐式开关，需要改为显式的 `storage_backend` 配置：

#### 3.1 `cloudrc.local` 统一化

```bash
# 存储后端选择：local | gluster | wds | ceph | gpfs
storage_backend=ceph

# Ceph 配置（storage_backend=ceph 时生效）
ceph_monitors="..."
ceph_pool="cloudland"
ceph_user="cloudland"
ceph_keyring="/etc/ceph/ceph.client.cloudland.keyring"

# GPFS 配置（storage_backend=gpfs 时生效）
gpfs_mount="/mnt/gpfs/cloudland"
```

#### 3.2 `cloudrc` 路由函数

在 `cloudrc` 中增加统一入口函数，各脚本不再直接判断 `wds_address`：

```bash
function get_storage_backend() {
    echo "${storage_backend:-local}"
}
```

#### 3.3 Go API 配置（`config.yaml`）

```yaml
volume:
  driver: ceph        # local | wds | ceph | gpfs
  ceph:
    monitors: "10.0.0.1:6789"
    pool: "cloudland"
  gpfs:
    mount: "/mnt/gpfs/cloudland"
```

---

### 工作四：部署与安装

#### 4.1 Ansible 角色（`deploy/roles/`）

新增角色：
- `roles/ceph_client/`：在所有 hypervisor 节点安装 `ceph-common`，分发 keyring，注册 libvirt secret
- `roles/gpfs_client/`：在所有 hypervisor 节点安装 GPFS 客户端，配置挂载点，写入 `/etc/fstab`

在 `deploy/cloudland.yml` 中集成这两个角色（条件执行）：
```yaml
- hosts: hyper
  roles:
    - role: ceph_client
      when: storage_backend == "ceph"
    - role: gpfs_client
      when: storage_backend == "gpfs"
```

#### 4.2 Docker Compose 环境（`deploy/docker/`）

`docker-compose.yml` 的 cloudland / clapi 服务需要：
- **Ceph**：挂载 `/etc/ceph` 目录和 keyring 文件进容器
- **GPFS**：将 GPFS 挂载点以 bind mount 方式传入容器（需要宿主机先挂载 GPFS）

#### 4.3 计算节点部署脚本（`deploy/docker/scripts/deploy-compute-node.sh`）

增加存储后端初始化步骤：
- Ceph：`apt install ceph-common`，写入 `/etc/ceph/ceph.conf`，`virsh secret-define`
- GPFS：安装 Spectrum Scale 客户端，`mmmount` 挂载，`mmauth` 认证

---

### 工作五：镜像管理适配

当前镜像流程（`capture_image.sh`, `clone_image.sh`, `clear_image.sh`）依赖本地文件路径。

#### 5.1 Ceph 镜像管理

- 镜像上传：qcow2 → raw → `rbd import` 到 Ceph image pool
- 镜像克隆：`rbd snap create` + `rbd clone` 替代 `qemu-img convert`
- 镜像缓存：利用 Ceph 的 parent/clone 机制，不需要每节点缓存

新增脚本：
- `create_image_ceph.sh`：`rbd import --image-format 2`
- `clone_image_ceph.sh`：`rbd snap protect` + `rbd clone`
- `clear_image_ceph.sh`：`rbd snap unprotect` + `rbd rm`

#### 5.2 GPFS 镜像管理

- 镜像存储在 GPFS 共享路径，所有节点直接可见，无需分发
- `create_image.sh` / `clear_image.sh` 路径改为 GPFS 路径即可
- 利用 GPFS 快照功能替代 qcow2 快照（可选优化）

---

### 工作六：迁移（Live Migration）适配

#### 6.1 Ceph RBD 迁移

- RBD 天然支持 live migration：VM 磁盘在 Ceph 集群，不在 hypervisor 本地
- `source_migration.sh` / `target_migration.sh` 无需复制磁盘，只迁移内存状态
- 需要目标节点也配置 Ceph keyring 和 libvirt secret

#### 6.2 GPFS 迁移

- 同 local 路径的 GPFS 文件对所有节点可见，迁移时无需磁盘传输
- `target_migration.sh` 中的文件同步逻辑（`rsync`）可跳过

---

### 工作七：监控与运维

#### 7.1 Ceph 监控集成（Prometheus）

在现有 Prometheus + Grafana 体系（`deploy/docker/`）中：
- 部署 `ceph-mgr` 开启 `prometheus` 模块
- 在 Prometheus 配置中增加 ceph scrape target
- Grafana 导入官方 Ceph Dashboard（Dashboard ID: 2842, 5336）

监控指标：
- OSD 状态、PG 健康状态
- 读写 IOPS、吞吐量、延迟
- 集群容量使用率

#### 7.2 GPFS 监控集成

- 使用 `mmhealth node show` / `mmperfmon` 采集性能数据
- 通过 Prometheus `node_exporter` 自定义 textfile collector 导出 GPFS 指标
- 或使用 IBM 提供的 `ibm_cloud_monitoring` exporter

监控指标：
- 文件系统容量与使用率
- 读写吞吐量、延迟
- 节点挂载状态

#### 7.3 告警规则

在 `deploy/docker/config/prometheus/` 下增加存储告警规则：

```yaml
# Ceph 告警
- alert: CephClusterUnhealthy
  expr: ceph_health_status != 0
  for: 5m

- alert: CephOSDDown
  expr: ceph_osd_up == 0

# GPFS 告警
- alert: GPFSMountFailed
  expr: gpfs_filesystem_mounted == 0
```

---

### 工作八：前端适配（`web/src/`）

- 存储池列表页面（`StoragePoolList.vue` 或在 `SystemSettings.vue` 中）：显示 Ceph pool 列表、GPFS 文件系统
- 卷创建时支持选择存储后端（如果需要多后端混用）
- i18n 补充 Ceph / GPFS 相关文本（`locales/zh.ts`, `locales/en.ts`）

---

## 三、工作优先级与建议路径

```
Phase 1（可行性验证）
  └── Ceph：在单个 hypervisor 节点手动验证 RBD attach/detach
  └── GPFS：验证 GPFS 挂载后 qcow2 文件的读写性能

Phase 2（脚本层集成）
  └── 完成所有 *_ceph.sh / *_gpfs.sh 脚本
  └── 完成 libvirt XML 模板
  └── 修改 launch_vm.sh 路由逻辑

Phase 3（API 层集成）
  └── Go API volume.driver 支持 ceph/gpfs
  └── SCI 命令路由到对应脚本

Phase 4（部署自动化）
  └── Ansible roles: ceph_client, gpfs_client
  └── deploy-compute-node.sh 集成存储初始化

Phase 5（监控与运维）
  └── Prometheus 指标采集
  └── Grafana Dashboard
  └── 告警规则
```

---

## 四、关键依赖与风险

| 风险项 | 说明 | 缓解措施 |
|---|---|---|
| Ceph 网络延迟 | RBD 通过网络访问，延迟高于本地盘 | 使用专用存储网络，启用 librbd 缓存 |
| GPFS License | IBM GPFS 需要商业授权 | 评估 OpenZFS + NFS 或 BeeGFS 作为替代 |
| libvirt secret 分发 | 每个 hypervisor 需要预配置 Ceph keyring | 通过 Ansible 自动化分发 |
| 多后端混用复杂度 | 同一集群同时有 local + Ceph + GPFS 卷 | 初期单一后端，后期按区域隔离后端类型 |
| 存量数据迁移 | 现有 local 卷迁移到 Ceph/GPFS | 提供 `migrate_volume.sh` 工具脚本（离线迁移） |

---

## 五、参考文档

- Ceph RBD + libvirt 集成：https://docs.ceph.com/en/latest/rbd/libvirt/
- QEMU RBD 驱动文档：https://qemu.readthedocs.io/en/latest/system/devices/block.html
- IBM Spectrum Scale（GPFS）文档：https://www.ibm.com/docs/en/spectrum-scale
- 现有 WDS 集成参考：`scripts/kvm/create_volume_wds_vhost.sh`，`scripts/kvm/attach_volume_wds_vhost.sh`
