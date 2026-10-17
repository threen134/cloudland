# 节点脚本沙箱

在本机 WSL2 Ubuntu 里以 root 跑的节点脚本测试：用替身命令（`mm*`、`ceph`、`rbd`、`virsh`）或 netns 模拟节点，不需要 work-x。对应 `TC-01` BUILD-13。原来放在旧机器 `Documents\gpfs-spike\`、`Documents\tgwsim\`，换机器时丢过一次，2026-10-15 取回后放进仓库。

脚本里的路径都按脚本所在位置推算（仓库根是 `../..`），`.gitattributes` 让这个目录在 Windows 上也检出为 LF，WSL 里可以直接执行：

```bash
# Git Bash 里要加 MSYS_NO_PATHCONV=1，否则 /mnt/c/... 会被改写；不要用不带 --exec 的形式（会多一层 shell 展开 $）
R=/mnt/c/Users/<用户>/Documents/cloudland/cloudland
wsl.exe -u root --exec bash $R/test-items/sandbox/storage/stc-all.sh
```

## storage/ —— 共享存储节点脚本

| 文件 | 内容 |
|---|---|
| `stc-install.sh` | 把仓库的 `scripts/` 装到 `/opt/cloudland/scripts`（去掉 CR、设可执行位），每组测试前由 `stc-all.sh` 调用 |
| `stc-all.sh` | 依次跑下面各组并打印每组的汇总行（`stc-test10-memlow.sh` 会改 WSL 的 cgroup，不在其中） |
| `stc-test1`–`18.sh`、`stc-test9-oom.sh` | 作业协议、预检、磁盘识别、共享池探测、NSD 看护、Ceph 客户端、S4 系统盘、S5 健康与指标、S6 对账 / 隔离 / 轮换 / 升级、纠删码、SAN、远程挂载、补清理、OOM 保护（各组的覆盖见设计文档 §16 各阶段的「测试」） |
| `stc-test10-memlow.sh` | cgroup v2 `memory.low` 逐层生效的实验，单独跑 |
| `ceph/spike1.sh`、`spike2.sh` | Ceph 探路：单节点 cephadm 集群、回环盘上的 OSD、RBD 客户端（S3 的结论来源） |
| `ceph/rbd-libvirt.sh` | 在沙箱 Ceph 集群上手工检查卷脚本：建卷、挂到 TCG 域、在线扩容、卸载、删除 |
| `ceph/key-ttl.sh`、`key-ttl-mon.sh` | 密钥轮换后旧会话还能不能用的实验（S6 轮换的结论来源） |

⚠️ `stc-test8.sh` 会临时改 WSL 的 `/root/.ssh/authorized_keys`，别和 Ceph 端到端测试同时跑；WSL 里不能留标签不全的假 Ceph 卷（`ceph-volume lvm list` 会崩）。

## tgw/ —— 中转网关 `apply_tgw.sh`

| 文件 | 内容 |
|---|---|
| `sim.sh` | 单节点 netns 模拟：三个 VPC 路由器加「云服务器」netns，逐个应用场景 JSON（`g*.json`）并探测连通 |
| `sim2.sh` | `apply_tgw.sh` 与 `create_local_router.sh` / `clear_local_router.sh` 并发 10 轮 |
| `sim3.sh <pg-*.json>` | 喂 clapi 服务层生成的状态（`TGW_SIM_DIR=<目录> go test ./src/services -run TgwSimulationStates` 生成 `pg-*.json`），按状态里的可达 / 黑洞清单核对 |
| `gotest.sh` | 在 WSL 里装 Go 与 gcc，跑要 CGO 的测试：cpgateway 全部、api `src/dbs` 与 `src/utils/tracing` |
| `subnet-gw.sh` | VPC 路由器的网关口（2026-10-16 补测发现的 R2-2 及之后的复查）：`set_subnet_gw.sh` 建新口时拆掉同一路由器里网段重叠的残留口（同网段、/25 在 /24 里、/24 包住 /25），连同它的 dnsmasq、目录、网桥，`fip-` 表路由改指新口；`clear_subnet_gw.sh` 的 `port` 模式拆口、拆网桥、删 `nonat`，`address` 模式（同一 VXLAN 还有别的子网）只摘网关地址与它的 `fip-` 路由和 DHCP 配置；不碰别的路由器的同 VNI 口、网桥、dnsmasq，包括本路由器已经不在这台节点上时；路由器没有使用者后连口一起回收；节点重启后口已经没了（路由器重建了但没这个口、或连 netns 都没了）时照样清网桥、上联口、FORWARD 规则和目录，路由器回收时也清；netns 名字被删、还被 dnsmasq 占着时，口的另一端不算别的路由器；参数校验（36 项） |

依赖：`ipset jq net-tools ipcalc tcpdump rsync`（`sim.sh` 会建假的 `/opt/fakebin`）。

## vpn/ —— VPN 网关节点脚本

| 文件 | 内容 |
|---|---|
| `reconcile.sh` | `vpn_lib.sh` 的 `vpn_tunnel_reconcile`（2026-10-16 补测发现的 R2-11）：只有已装的 SA 里有配置中没有的选择符（过时）时才关 IKE SA；只是缺子 SA 时发起方只补发缺的、只响应的一侧不动；每条隧道每分钟最多一次。swanctl 与 charon 是替身（10 项） |
| `ipsec-conf-order.sh` | `create_vpn_ipsec_conf.sh` 删 XFRM 接口的时机（R2-14）：删隧道、隧道换了新的 if_id 时，先重载配置、终止 SA，最后才删接口；删接口卡住时 10 秒后照常往下走并记日志。替身 `ip` 与 charon；`KVM=<目录>` 跑另一份 `scripts/kvm`（如旧提交的脚本）（9 项） |

## vm/ —— 云服务器

| 文件 | 内容 |
|---|---|
| `finish-source-dhcp.sh` | `finish_source_migration.sh` 迁移后在源节点释放 DHCP（R2-13）：VPC 内部子网的每块网卡都调 `del_host.sh`（删绑定、`dhcp_release`），VLAN 网卡不动，在回收路由器之前做，源节点上还定义着这台时什么都不做。替身 cloudrc、`storage_lib.sh`、`virsh` 与被调用的脚本（5 项） |
