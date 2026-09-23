---
order: 80
---
# 配置本地存储池

计算节点上除系统盘之外的空闲硬盘，需要先**建成存储池**才能被云硬盘和云服务器的系统盘使用。本文讲如何在部署完计算节点之后配置这些盘。

设计细节见 `docs/architecture/plan/local-multi-disk-storage-plan.md`。

---

## 概念

CloudLand 的存储池分两层：

| 层 | 是什么 | 谁来建 |
|---|---|---|
| **存储池** | 区域级的一个名字，比如 `local-ssd`、`local-hdd`；用户建云硬盘时在这里选 | 管理员，一个区域建一次 |
| **节点存储池** | 某台机器上属于这个池的一个目录 | 管理员，按机器逐台配置 |

- 每台机器上**一个池只对应一个目录**，路径在所有机器上一致：`/opt/cloudland/pools/<池UUID>`。
- 同类的多块盘用 LVM 合成一个目录，可选 RAID1 做冗余。
- **内置池 `local`** 固定存在、不能删，就是每台机器的 `/opt/cloudland/cache`（和系统盘同一个文件系统）。不配置任何存储池时，所有磁盘都落在这里。

> 一台机器可以有 0 个或多个存储池；同一个池不要求每台机器都有。云硬盘只能挂给**有这个池**的机器上的云服务器。

---

## 准备工作

- **依赖包**：`lvm2`、`mdadm`、`xfsprogs`、`rsync` 由 `deploy-compute-node.sh` 自动安装，无需手工装。
- **磁盘要求**：只接受**整盘**（不是分区），且不能是系统盘、不能是正在使用的盘。盘上有旧数据时要显式勾选「清除」。
- **不要手工挂载**：`/opt/cloudland/pools/` 下的目录由 CloudLand 自己写 `/etc/fstab`（带 `nofail`）并挂载。
- **生产环境不要设 `storage_allow_loop`**：那是测试环境用 loop 设备模拟磁盘的开关。

---

## 第一步：建一个存储池（区域级，一次）

**Web 管理界面**：左侧 **存储池** → 新建。

**API**：

```bash
curl -sk -X POST https://<PUBLIC_IP>/api/v1/storage_pools \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"local-ssd","media":"ssd","over_ratio":3,"fallback_group":"local","description":"NVMe of the compute nodes"}'
```

| 字段 | 说明 |
|---|---|
| `media` | `ssd` / `hdd` / `nvme`。建池、加盘时后端会检查盘的介质是否与池一致，不一致要显式确认 |
| `over_ratio` | 超分比例。qcow2 是精简置备，实际占用通常远小于申请容量；`3` 表示允许分配到容量的 3 倍。默认 `1`（不超分） |
| `fallback_group` | 可互换组。迁移时目标机器没有原池，只会在**同组**的池里自动替换 |
| `is_default` | 建云硬盘不选池时用哪个 |

---

## 第二步：扫描计算节点的磁盘

**Web 管理界面**：计算节点详情 → **存储** 标签页 → 扫描磁盘。

```bash
curl -sk -X POST https://<PUBLIC_IP>/api/v1/hypers/<节点UUID>/disks/scan -H "Authorization: Bearer $TOKEN"
# 扫描是异步的，约 10 秒后读取结果
curl -sk https://<PUBLIC_IP>/api/v1/hypers/<节点UUID>/disks -H "Authorization: Bearer $TOKEN" | jq .
```

扫描结果里每块盘有一个状态：

| 状态 | 含义 | 能否建池 |
|---|---|---|
| `free` | 空闲、无任何签名 | 可以 |
| `dirty` | 盘上有分区或文件系统签名 | 要显式勾「清除」 |
| `system` | 系统盘（根、`/boot`、swap 所在） | **不可** |
| `in_use` | 已挂载或被其他用途占用 | **不可** |
| `cloudland_pool` | 盘上是 CloudLand 的池（本机或其他机器留下的） | 见「节点重装后接管」 |
| `unknown_member` | LVM / md 的成员，但读不出属主 | **不可**（防误删） |
| `shared` | 疑似共享 LUN（多路径等） | **不可** |

---

## 第三步：在节点上建池

**Web 管理界面**：计算节点详情 → **存储** → 建池，选盘、选布局、输入本机名确认。

```bash
curl -sk -X POST https://<PUBLIC_IP>/api/v1/hypers/<节点UUID>/storage_pools \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"storage_pool":{"id":"<池UUID>"},"layout":"raid1","disks":["nvme0n1","nvme1n1"],
       "wipe":true,"confirm":"<本机主机名>"}'
```

| 布局 | 说明 |
|---|---|
| `single` | 单盘，**无冗余**，坏盘即丢数据 |
| `linear` | 多盘拼接容量，**无冗余**，任一盘坏则整池不可用 |
| `raid1` | 按**成对**的盘做镜像，可容忍每对坏一块；两块容量不同时按小的算 |

其他字段：`wipe`（清除盘上旧数据）、`allow_media_mismatch`（放行介质不一致，会记进审计）、`confirm`（必须输入这台机器的主机名，防止配错机器）。

建池是异步的：状态从 `creating` 变为 `ready` 即可用。节点上会出现 `/opt/cloudland/pools/<池UUID>`（含 `volumes/`、`nvram/`、`tmp/` 三个子目录）、一行带 `nofail` 的 fstab、以及池根的 `.cloudland-pool` 标记文件。

> **新建的 XFS 池「已用」不是 0**：1.8 TB 的盘上约 36 GB（2% 左右），是文件系统自己的元数据预留，属正常。

---

## 日常操作

| 操作 | 接口 | 说明 |
|---|---|---|
| 加盘 | `POST /hypers/{uuid}/storage_pools/{pool}/extend` | 在线扩容，池里的虚拟机不受影响。单盘池加一块后布局变成 `linear`（仍无冗余）；RAID1 按对加 |
| 换盘 | `POST .../replace_disk` | RAID1 降级后换上新盘，后台重建，期间可读写 |
| 维护 | `POST .../maintenance` | 进入维护后拒绝新分配，探测不会自动把池挂回来，适合换硬件 |
| 宣告丢失 | `POST .../lost` | 盘彻底坏了：池上的卷标记为 `lost`，可以强制卸下、删除记录。**不会擦盘上的数据** |
| 恢复 | `POST .../restore` | 盘修好、节点报告健康后把池和文件还在的卷恢复可用 |
| 统计占用 | `POST .../usage` → `GET .../usage` | 列出池里最占空间的文件，并对应到卷 / 云服务器 / 组织 |
| 删池 | `DELETE /hypers/{uuid}/storage_pools/{pool}?confirm=<主机名>` | 池里还有卷时会被拒绝；`&force=true` 才删残留文件（文件清单进审计） |

---

## 容量与超分

- 准入按「**机器 × 池**」计算：`容量 × 超分比例` 对已分配量。
- 占用率 **≥ 90%** 的池拒绝新分配，即使超分比例还没用满。
- 云服务器的系统盘与迁移会**预留**空间（24 小时过期），避免并发创建把池挤爆。
- 跨过 **80% / 85% / 90%** 会立即上报，界面上容量条变色。

**池被写满时**：云服务器会被 QEMU 暂停（不会写坏数据），界面显示「存储空间不足」；腾出空间后下一次探测会自动恢复运行。虚拟机内删文件后执行 `fstrim` 可以把空间还给池。

---

## 开机行为

- 池由 fstab 的 `nofail` 自动挂载，**盘坏了不会导致机器开不了机**。
- 磁盘所在池还没就绪的云服务器**不会**被启动，而是进「待启动列表」，界面显示「等待存储就绪」；池挂上后下一次心跳自动启动。心跳不会因为等池而阻塞。

---

## 节点重装后接管

节点重装系统、保留数据盘时，池还在盘上：

1. 重新注册节点并部署（**hostid 不复用**，会分到新号）。
2. 扫描磁盘，盘显示为 `cloudland_pool`。
3. 点「接管」（`POST /hypers/{uuid}/storage_pools/adopt`）：系统组装阵列、改写 LVM 标签与池标记、写回 fstab，池上的卷恢复可用；文件已经不在的卷标记为 `lost`。

删除节点时如果要保留盘上的数据，用 `DELETE /hypers/{uuid}?keep_pools=true`：卷会标记为「等待接管」，等新节点接管后恢复。确定不要了就用存储池详情页的「放弃接管」。

---

## 监控与告警

- 节点通过 node_exporter 的 textfile 收集器上报 `cloudland_pool_status` 与 `cloudland_pool_usage_ratio`。
- 告警规则类型 **`local_pool`**（节点告警页新建），模板 `local-pool-monitor.yml.j2`。
- 池的状态取值：`creating` / `ready` / `degraded`（RAID1 降级）/ `unavailable`（探测不到，15 分钟无上报也会转成它）/ `maintenance` / `lost` / `error`。

---

## 常见问题

- **池一直是 `creating`**：看节点 `journalctl -u cloudlet-go`，多半是盘被占用或 `mkfs` 失败；超时后会转为 `error`，清掉后可重建。
- **池变成 `unavailable`**：节点或磁盘 I/O 卡住、或该节点心跳中断。其他池和虚拟机不受影响。
- **删池被拒绝**："pool is not empty" 会列出残留文件；确认无用后加 `force=true`。
- **建池报「已经在这台机器上配置过」**：`/etc/fstab` 里还有该池的行（历史上建池失败可能留下），删掉那行再重试。
- **挂载云硬盘提示机器上没有这个池**：已落盘的卷只能挂给同节点、且有该池的云服务器；要跨机器用，先迁移云服务器。
- **RAID1 一块盘坏了**：池变 `degraded` 并告警，虚拟机继续读写；换盘后重建完成自动回到 `ready`。
