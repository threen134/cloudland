# 内部 DNS 服务方案：计算节点 Hostname 动态解析

## 背景

当前问题：计算节点之间（特别是 VM 迁移场景）需要通过 hostname 互相访问，例如：

```bash
# source_migration.sh
virsh migrate --live $vm_ID qemu+ssh://$target_hyper/system
```

现有方案是在每个节点的 `/etc/hosts` 里静态维护所有节点的 hostname→IP 映射。
缺陷：
- 新增节点时，所有**已部署**节点的 `/etc/hosts` 不会自动更新
- 节点数量增多后维护成本极高，容易出现遗漏导致迁移失败

## 方案概述

在控制节点运行一个 dnsmasq 容器作为**内部权威 DNS**，专门解析计算节点 hostname。

- 计算节点的 `DNS_SERVER` 指向控制节点内网 IP
- 每次 `/internal/node/add` 注册成功后，cloudland 将新节点写入 dnsmasq hosts 文件
- dnsmasq 通过 `--hostsdir` + inotify 自动感知文件变更，**无需任何信号或 API 通知**
- 所有计算节点立刻能解析新节点 hostname，无需手动维护 `/etc/hosts`

```
计算节点 A                控制节点
  |                         |
  | DNS query: worknode03   |
  |------------------------>| dnsmasq:53
  |<------------------------| 10.193.191.100
  |                         |
  | virsh migrate qemu+ssh://worknode03/system
  |------- SSH:22 --------> 计算节点 C (worknode03)
```

## 实现步骤

### 1. Dockerfile.cloudland：添加 curl（保留供其他 API 调用使用，DNS 不再需要）

> 若 cloudland 其他功能不需要 curl，此步骤可跳过。当前 plan 的 DNS 机制
> 不再依赖 curl 或 Docker socket。

### 2. docker-compose.yml 添加 dnsmasq 服务

新增 `dnsmasq` 服务；cloudland 服务挂载同一个 `dns` 目录，写入即生效：

```yaml
# deploy/docker/docker-compose.yml

  dnsmasq:
    image: drpsychick/dnsmasq:2.89
    container_name: cloudland-dnsmasq
    restart: unless-stopped
    profiles:
      - region
    network_mode: host
    cap_add:
      - NET_ADMIN
    volumes:
      - ../dns:/etc/dnsmasq.d:ro    # 目录挂载；inotify 监听目录内文件变更
    command: >
      --no-hosts
      --hostsdir=/etc/dnsmasq.d
      --server=${DNS_UPSTREAM:-8.8.8.8}
      --listen-address=${INTERNAL_IP:-127.0.0.1}
      --bind-interfaces
      --port=53
      --log-queries

  cloudland:
    # ... 原有配置 ...
    user: root                        # ← 新增：需要写 dns 目录下的文件
    volumes:
      # ... 原有挂载 ...
      - ../dns:/opt/cloudland/dns     # ← 新增：与 dnsmasq 共享同一目录
```

说明：
- **`--hostsdir` + inotify**：dnsmasq 监听目录内所有文件的增删改，cloudland 写文件后
  dnsmasq 自动重新加载，无需 SIGHUP、Docker socket 或任何网络通知机制
- **目录挂载而非文件挂载**：file bind mount 固定在创建时的 inode，`rename()` 后
  容器看到的仍是旧 inode；目录挂载按路径查找，`rename()` 后新 inode 立即可见
- `deploy/dns/` 目录须在 `docker compose up` 前存在（含至少一个文件），
  否则 Docker 会把路径创建为目录挂载点但 dnsmasq 启动时找不到任何 hosts 文件
  会记录 warning（不影响运行）
- cloudland 已使用 `network_mode: host` 管理计算节点通信，以 root 运行可接受
- dnsmasq 镜像固定版本 `2.89`，不使用 `latest`
- `--server=${DNS_UPSTREAM:-8.8.8.8}`：上游 DNS 从 `.env` 的 `DNS_UPSTREAM` 读取

> **多上游 DNS**：若需多个上游，在 `.env` 中分别定义：
> ```bash
> DNS_UPSTREAM_1=8.8.8.8
> DNS_UPSTREAM_2=8.8.4.4
> ```
> 在 docker-compose.yml command 里展开为多条 `--server=` 参数。

### 3. 初始化 hosts 文件

```bash
# 部署前在控制节点上执行
mkdir -p deploy/dns
touch deploy/dns/hyper-hosts
# 可选：写入控制节点自身条目
echo "10.193.191.118 worknode02" >> deploy/dns/hyper-hosts
```

文件格式与 `/etc/hosts` 相同，由 `/internal/node/add` 注册时自动维护，勿手动编辑。

目录结构：
```
deploy/
  docker/
    docker-compose.yml
  dns/
    hyper-hosts     ← cloudland 读写；dnsmasq 通过 hostsdir + inotify 自动感知变更
  .ssh/
```

### 4. hyper.go：部署/删除时维护 DNS

> **设计决策**：`/internal/node/add` 是 C++ cloudland 的 RPC 端点（`rpcworker.cpp`），
> 而 hyper 的 IP 和 hostname 在管理员通过 Web UI 发起部署时已经确定，并由 clapi 的
> `HyperAdmin.Deploy` 写入数据库。因此直接在 Go 层维护 `hyper-hosts`，
> 无需修改任何 C++ 代码。

新增 `api/src/services/dns_hosts.go`，提供写文件的工具函数：

```go
// api/src/services/dns_hosts.go

package services

import (
    "os"
    "path/filepath"
    "strings"
)

func dnsHostsFile() string {
    if v := os.Getenv("HYPER_DNS_HOSTS_FILE"); v != "" {
        return v
    }
    return "/opt/cloudland/dns/hyper-hosts"
}

// RegisterHostInDns 将 hostname→ip 写入 hyper-hosts（幂等，支持 IP 变更）。
// rename 原子替换后 dnsmasq inotify 自动重载，无需 SIGHUP。
func RegisterHostInDns(hostname, ip string) {
    if hostname == "" || ip == "" {
        return
    }
    hostsFile := dnsHostsFile()

    var lines []string
    if data, err := os.ReadFile(hostsFile); err == nil {
        for _, l := range strings.Split(string(data), "\n") {
            if hostsLineMatchesHostname(l, hostname) {
                continue // 过滤旧条目（支持 IP 变更）
            }
            if strings.TrimSpace(l) != "" {
                lines = append(lines, l)
            }
        }
    }
    lines = append(lines, ip+" "+hostname)

    content := strings.Join(lines, "\n") + "\n"
    tmpFile := hostsFile + ".tmp"
    if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
        logger.Errorf("DNS: write %s failed: %v", tmpFile, err)
        return
    }
    if err := os.Rename(tmpFile, hostsFile); err != nil {
        logger.Errorf("DNS: rename %s -> %s failed: %v", tmpFile, hostsFile, err)
        return
    }
    logger.Infof("DNS: registered %s -> %s", hostname, ip)
}

// RemoveHostFromDns 从 hyper-hosts 删除指定 hostname 条目。
func RemoveHostFromDns(hostname string) {
    if hostname == "" {
        return
    }
    hostsFile := dnsHostsFile()

    data, err := os.ReadFile(hostsFile)
    if err != nil {
        return
    }
    var lines []string
    found := false
    for _, l := range strings.Split(string(data), "\n") {
        if hostsLineMatchesHostname(l, hostname) {
            found = true
            continue
        }
        if strings.TrimSpace(l) != "" {
            lines = append(lines, l)
        }
    }
    if !found {
        return
    }

    content := strings.Join(lines, "\n") + "\n"
    tmpFile := hostsFile + ".tmp"
    if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
        logger.Errorf("DNS: write %s failed: %v", tmpFile, err)
        return
    }
    if err := os.Rename(tmpFile, hostsFile); err != nil {
        logger.Errorf("DNS: rename %s -> %s failed: %v", tmpFile, hostsFile, err)
        return
    }
    logger.Infof("DNS: removed %s", hostname)
}

// hostsLineMatchesHostname 精确匹配 hosts 行的 hostname 字段（防止子串误匹配）。
func hostsLineMatchesHostname(line, hostname string) bool {
    fields := strings.Fields(line)
    return len(fields) >= 2 && fields[1] == hostname
}

// RebuildDnsHostsFile 从数据库重建 hyper-hosts，供 clapi 启动时调用。
func RebuildDnsHostsFile() {
    db := DB()
    var hypers []struct {
        Hostname string
        HostIP   string
    }
    if err := db.Table("hypers").Select("hostname, host_ip").
        Where("host_ip != ''").Find(&hypers).Error; err != nil {
        logger.Errorf("DNS: rebuild query failed: %v", err)
        return
    }

    hostsFile := dnsHostsFile()
    dir := filepath.Dir(hostsFile)
    if err := os.MkdirAll(dir, 0755); err != nil {
        logger.Errorf("DNS: mkdir %s failed: %v", dir, err)
        return
    }

    var lines []string
    for _, h := range hypers {
        if h.Hostname != "" && h.HostIP != "" {
            lines = append(lines, h.HostIP+" "+h.Hostname)
        }
    }
    content := strings.Join(lines, "\n") + "\n"
    tmpFile := hostsFile + ".tmp"
    if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
        logger.Errorf("DNS: rebuild write failed: %v", err)
        return
    }
    if err := os.Rename(tmpFile, hostsFile); err != nil {
        logger.Errorf("DNS: rebuild rename failed: %v", err)
        return
    }
    logger.Infof("DNS: rebuilt %s with %d entries", hostsFile, len(lines))
}
```

#### 4.1 `HyperAdmin.Deploy` 部署成功后写入

```go
// api/src/services/hyper.go，db.Create(hyper) 成功后

if err = db.Create(hyper).Error; err != nil {
    return nil, "", NewCLError(ErrSQLSyntaxError, "Failed to create hypervisor record", err)
}
RegisterHostInDns(hostname, ip)   // ← 新增
```

重试部署（已有记录）的分支同理，在 `db.Save(hyper)` 成功后调用 `RegisterHostInDns(hostname, ip)`。

#### 4.2 `HyperAdmin.Delete` 删除时移除

```go
// api/src/services/hyper.go，db.Delete(hyper) 之前（hyper 记录已查出）

RemoveHostFromDns(hyper.Hostname)   // ← 新增
if err = db.Delete(hyper).Error; err != nil {
    ...
}
```

### 5. 控制节点重启后重建 hyper-hosts

clapi 启动时直接从数据库 `hypers` 表重建，不依赖 JSON 持久化文件：

```go
// api/src/services/services.go，Init() 中

func Init() {
    AdminInit()
    RebuildAlarmRulesOnStartup()
    RebuildDnsHostsFile()   // ← 新增：从 DB 重建 hyper-hosts
    ApplyDnsUpstream()      // ← 已有：写 upstream.conf 并 SIGHUP
}
```

### 7. deploy-compute-node.sh：指向内部 DNS 并上报 IP

#### 7.1 注册请求加 ip 字段

`OWN_IP` 获取逻辑已在步骤 1（/etc/hosts 生成处）存在，直接复用：

```bash
# deploy-compute-node.sh 步骤 15
curl -sf -X POST "http://${CONTROLLER_IP}:${RPC_SERVER_PORT}/internal/node/add" \
  -H "Content-Type: application/json" \
  -d "{\"hostname\": \"${HOSTNAME}\", \"id\": ${SCI_CLIENT_ID}, \"level\": 1, \"ip\": \"${OWN_IP}\"}"
```

#### 7.2 设置 resolv.conf（兼容 systemd-resolved）

直接写 `/etc/resolv.conf` 在 Ubuntu 18.04+ 上会被 `systemd-resolved` 重启后覆盖，
需通过 `resolved.conf.d/` 持久化配置：

```bash
# deploy-compute-node.sh，hostname 设置之后

if systemctl is-active --quiet systemd-resolved 2>/dev/null; then
    # systemd-resolved 系统：通过 drop-in 持久化，重启后不丢失
    mkdir -p /etc/systemd/resolved.conf.d/
    cat > /etc/systemd/resolved.conf.d/cloudland.conf <<EOF
[Resolve]
DNS=$CONTROLLER_IP
DNSStubListener=no
EOF
    systemctl restart systemd-resolved
    rm -f /etc/resolv.conf
    echo "nameserver $CONTROLLER_IP" > /etc/resolv.conf
else
    # 无 systemd-resolved 系统（CentOS 7 等）直接写入
    cat > /etc/resolv.conf <<EOF
nameserver $CONTROLLER_IP
EOF
fi
```

dnsmasq 已配置 `--server=${DNS_UPSTREAM:-8.8.8.8}` 转发外网查询，
计算节点 `resolv.conf` 只需一个条目。

#### 7.3 compute.env 示例

```bash
# compute.env
DNS_SERVER=10.193.191.118    # 控制节点内网 IP，也是 dnsmasq 监听地址
```

## 设计决策

| 问题 | 决策 |
|------|------|
| DNS 更新通知机制 | `--hostsdir` + inotify：cloudland 只写文件，dnsmasq 自动感知变更，无需 SIGHUP、Docker socket 或任何网络通知 |
| bind mount 粒度 | 挂载**目录**；file bind mount 固定在创建时 inode，`rename()` 后新 inode 对容器不可见；目录挂载按路径查找，`rename()` 后立即可见，且与 inotify 配合正确 |
| 文件写入方式 | 先写 `.tmp` 再 `rename` 原子替换；`rename` 触发 inotify 事件，同时保证 dnsmasq reload 时读到完整文件 |
| hostname 去重 | 按空白分割取第二列精确比较，不使用 `string::find`，防止子串误匹配 |
| IP 变更 | 写入前过滤旧行再追加，自动覆盖，无需手动 remove 再 add |
| cloudland 用户权限 | docker-compose 加 `user: root`；cloudland 已用 host 网络管理 VM，root 可接受 |
| 持久化与重建 | `persistNodeAdd` 保存 ip 字段；控制节点启动时 `rebuildDnsHostsFile` 从 JSON 重建，`rename` 自动触发 inotify |
| systemd-resolved | 通过 `resolved.conf.d/cloudland.conf` 持久化 DNS 配置，重启后不丢失；无 systemd-resolved 系统直接写 resolv.conf |
| DNS 写入层选择 | 在 Go（clapi）的 `HyperAdmin.Deploy/Delete` 维护，而非 C++（rpcworker.cpp）；IP 和 hostname 在 API 层已知，无需修改 C++ |
| 重建数据源 | 启动时直接查 `hypers` 表重建，不依赖 JSON 持久化文件，数据来源唯一 |
| 并发写入保护 | clapi 为多 goroutine 服务，`hyper-hosts` 写入用原子 rename；并发部署场景概率低，当前不加锁 |
| 镜像版本 | 固定 `drpsychick/dnsmasq:2.89`（部署前确认 tag 存在），不使用 `latest` |
| dnsmasq 上游 DNS | 由 `dns-upstream-settings-plan.md` 管理，通过 `--conf-dir` + `upstream.conf` 热更新，不再硬编码 `--server=` |

## 文件变更清单

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `deploy/docker/docker-compose.yml` | 新增/修改 | 添加 `dnsmasq` 服务（`--hostsdir` + `--conf-dir`）；cloudland 加 `user: root`、dns 目录挂载；clapi 加 conf.d 和 docker.sock 挂载 |
| `deploy/dns/hyper-hosts` | 新增 | DNS hosts 文件（初始为空，clapi 启动时从 DB 重建） |
| `deploy/dns/conf.d/upstream.conf` | 新增 | 初始上游 DNS 配置，由 dns-upstream-settings-plan.md 管理 |
| `api/src/services/dns_hosts.go` | 新增 | `RegisterHostInDns`、`RemoveHostFromDns`、`RebuildDnsHostsFile`、`hostsLineMatchesHostname` |
| `api/src/services/hyper.go` | 修改 | `Deploy` 成功后调用 `RegisterHostInDns`；`Delete` 前调用 `RemoveHostFromDns` |
| `api/src/services/services.go` | 修改 | `Init()` 加 `RebuildDnsHostsFile()` |
| `deploy/docker/scripts/deploy-compute-node.sh` | 修改 | 兼容 systemd-resolved，持久化 resolv.conf 指向控制节点 |

## 不在本方案范围内

- 多控制节点 HA 下的 DNS 同步（当前部署为单控制节点）
- DNSSEC
- 替换系统 DNS（本方案仅在内网节点 hostname 解析层面生效，不影响公网解析）

## 扩展路径：DNS 独立部署

当前方案要求 dnsmasq 与 cloudland 在同一台机器（共享 `dns/` 目录，inotify 本地生效）。

若未来需要 DNS 独立部署，将 `registerHostInDns` / `removeHostFromDns` 替换为
**RFC 2136 动态 DNS 更新**（`nsupdate`），其余代码（`persistNodeAdd`、
`lookupHostnameById`、`rebuildDnsHostsFile` 等）不受影响：

```cpp
// 替换后的 registerHostInDns（需要 cloudland 容器安装 bind9-dnsutils）
static void registerHostInDns(const string &hostname, const string &ip) {
    string cmd =
        "printf 'server %s\\n"
        "update delete %s. A\\n"
        "update add %s. 60 A %s\\n"
        "send\\n' "
        "%s %s %s %s "
        "| nsupdate -y hmac-md5:cloudland:%s",
        dnsServer.c_str(),
        hostname.c_str(), hostname.c_str(), ip.c_str(),
        dnsServer.c_str(), hostname.c_str(), hostname.c_str(), ip.c_str(),
        tsigKey.c_str();
    system(cmd.c_str());
}
```

迁移步骤：将 dnsmasq 替换为支持 RFC 2136 的 DNS 服务（BIND9 / PowerDNS），
开启 TSIG 认证的动态更新，修改 `HYPER_DNS_HOSTS_FILE` 相关逻辑为网络调用即可。
