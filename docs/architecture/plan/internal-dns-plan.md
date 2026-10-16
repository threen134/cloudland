---
order: 20
---

# P20 内部 DNS：控制面 dnsmasq 解析节点主机名

> 状态：已实施，**维护中**。2026-10-15 按代码重写；原方案写于 2026-04（C++ 主控时期，含 `rpcworker.cpp`、`persistNodeAdd`、从 JSON 重建等已不存在的做法），原文见 git `64974e5c`。改这部分行为时同步改本文。
>
> 上游转发的界面配置是另一份文档 [P19 dns-upstream-settings-plan.md](dns-upstream-settings-plan.md)，那份是历史文档，里面的 `--conf-dir` 做法已改掉，以本文 §6 为准。

## 1. 用途

控制面运行一个 dnsmasq 容器，计算节点和控制面的两个容器（cland、clapi）都把它当 DNS。它负责解析：

| 名字 | 指向 | 谁写进去 | 谁在用 |
|---|---|---|---|
| 计算节点主机名（`hypers.hostname`） | `hypers.host_ip` | clapi | 迁移时目标节点没有 `host_ip` 才用主机名兜底（`rpcs/migrate_vm.go`）；运维人工 ssh |
| 控制节点自身主机名 | `INTERNAL_IP` | `deploy-control-node.sh` | 计算节点按主机名访问控制节点 |
| `S3_ENDPOINT` 的主机名，只在它是内置 MinIO 时：等于 `MINIO_HOSTNAME`（默认 `images.cloudland.internal`）或以 `.cloudland.internal` 结尾；外部 S3 走上游 | `MANAGEMENT_VIP` | clapi | 计算节点下载镜像的 presigned URL；clapi 自己 `InitS3` |
| `CLAPI_HOSTNAME`（默认 `clapi.cloudland.internal`） | `MANAGEMENT_VIP` | clapi | 从虚拟机捕获镜像时计算节点的上传地址（`CLAPI_INTERNAL_URL` 默认 `https://<CLAPI_HOSTNAME>:8255/api/v1`） |
| 其他名字 | 转发上游（`DNS_UPSTREAM`） | — | — |

原方案的出发点是「迁移靠主机名互访，`/etc/hosts` 维护不过来」。这一点已经不成立：2026-09 起 `source_migration.sh` 用目标节点的内网 IP 连接（主机名不一定能解析、`known_hosts` 通常只记 IP），节点主机名解析现在只是兜底。实际用得最多的是 MinIO / clapi 两个内部域名。

```
计算节点                          控制节点
  resolv.conf: nameserver <MANAGEMENT_VIP>   ← 排第一；后面可能还有网卡带来的解析器（§5）
  |  images.cloudland.internal ?  |
  |------------------------------>| dnsmasq（host 网络，监听 INTERNAL_IP:53）
  |<------------------------------|   /etc/dnsmasq.hosts/ 下的 hosts 文件 → 命中即答
  |                               |   其余 → upstream.conf 里的 server=
```

## 2. dnsmasq 容器

`deploy/docker/docker-compose.yml`，profile `region`：

```yaml
  dnsmasq:
    image: drpsychick/dnsmasq:2.89
    container_name: cloudland-dnsmasq
    restart: unless-stopped
    network_mode: host
    cap_add: [NET_ADMIN]
    volumes:
      - ../dns/hosts:/etc/dnsmasq.hosts:ro     # hosts 文件目录，inotify 监听
      - ../dns/conf.d:/etc/dnsmasq.conf.d:ro   # upstream.conf
    command: >
      --keep-in-foreground
      --conf-file=/dev/null
      --no-resolv
      --no-hosts
      --hostsdir=/etc/dnsmasq.hosts
      --servers-file=/etc/dnsmasq.conf.d/upstream.conf
      --listen-address=${INTERNAL_IP:-127.0.0.1}
      --bind-interfaces
      --port=53
      --log-queries
    healthcheck:
      test: ["CMD-SHELL", "pgrep -x dnsmasq >/dev/null || exit 1"]
```

- `--conf-file=/dev/null` 不加载镜像自带的配置，`--no-resolv` 不读容器的 resolv.conf，上游只来自 `--servers-file`
- `--keep-in-foreground` 下绑定 53 端口失败会立即退出，所以健康检查只看进程在不在；cland 容器 `depends_on: dnsmasq: service_healthy`
- cland、clapi 两个容器都配了 `dns: ${INTERNAL_IP}`。clapi 在 bridge 网络里，不指过来的话 Docker 内置解析器解析不了 `images.cloudland.internal`，`InitS3` 会失败
- dnsmasq 的日志不进 `docker logs`

## 3. 文件与归属

```
deploy/
  dns/
    hosts/                 ← 挂到 dnsmasq 的 /etc/dnsmasq.hosts（--hostsdir）
      hyper-hosts          ← clapi 独占，启动时整份重建
      control-hosts        ← deploy-control-node.sh 独占，clapi 不碰
    conf.d/
      upstream.conf        ← clapi 写（ApplyDnsUpstream），部署脚本只写初始值
```

- clapi 挂的是整个 `../dns`（`/opt/cloudland/dns`），另挂 `/var/run/docker.sock` 用于重载上游（§6）。cland 容器不挂、也不写
- 两个 hosts 文件都按 `/etc/hosts` 格式（`IP 主机名`）。一个文件只有一个写入方：**hyper-hosts 会在 clapi 每次启动时按 `hypers` 表整份重写**，所以别的东西不能往里放条目（2026-10-15 之前部署脚本把控制节点主机名写在这里，clapi 一启动就被冲掉，见变更记录）
- `hyper-hosts`、`control-hosts`、`upstream.conf` 和两个目录里点开头的 `.tmp` 临时文件都在 `.gitignore` 里
- dnsmasq 在容器里以 `dnsmasq` 用户（非 root）读这些文件（work-01 上 `ps` 核对过），所以目录要 755、文件要 644，不能依赖写入方的 umask

**初始化**由 `deploy-control-node.sh` 第 5 步做：建 `dns/`、`hosts/`、`conf.d/` 并设为 755；`hyper-hosts` 不存在时建空文件；把 `INTERNAL_IP 主机名` 写进 `control-hosts`（先写点开头的临时文件、设 644 再 `mv`，每次部署都覆盖，所以 IP 变了重跑一次即可）；`upstream.conf` 不存在时按 `.env` 的 `DNS_UPSTREAM` 写（逗号分隔、每个一行 `server=`，没配就是 `8.8.8.8`）；三个文件都设成 644。MinIO / clapi 两个域名不在这里写，clapi 启动时重建 hyper-hosts 一起写进去。不用脚本、照 compose 文件开头的步骤手工初始化时，`control-hosts` 也要自己写。

## 4. clapi 怎么维护 hyper-hosts

### 4.1 写法（`api/src/services/dns_hosts.go`）

| 函数 | 做什么 |
|---|---|
| `SyncHostsInDns(names...)` | 按 `hypers` 表重写这几个名字的行，**一次写入**：没有节点再用这个名字就删掉，否则指向用它的那台节点的 `host_ip`。读的是已提交的数据，所以要在改动提交之后调。别的行（包括两个服务名）不动；结果与原文件相同时不写 |
| `RebuildDnsHostsFile()` | clapi 启动时整份重写：先是服务名（`serviceDnsEntries`：内置 MinIO 的名字与 `CLAPI_HOSTNAME`，都指 `MANAGEMENT_VIP`，`MANAGEMENT_VIP` 为空时没有），再是 `hypers` 表 `host_ip`、`hostname` 都不为空的全部节点（**不按状态过滤**，部署中的节点也在），同名的只留第一个；目录不存在时先建 |

共同的约定：

- 文件路径默认 `/opt/cloudland/dns/hosts/hyper-hosts`，环境变量 `HYPER_DNS_HOSTS_FILE` 可改（测试用）
- 两个函数都在进程内锁 `dnsHostsMu` 下执行（原方案写的是「不加锁」）
- **同名的多台节点**（`hypers.hostname` 不唯一）：这个名字指向 `hostid` 最小的那台，重建与同步用同一条规则；hosts 文件里一个名字只有一行
- 先写**点开头**的临时文件 `.hyper-hosts.tmp`、显式设成 644，再 `rename` 成 `hyper-hosts`，失败时删掉临时文件。dnsmasq 的 `--hostsdir` 跳过点开头的文件；如果临时文件叫 `hyper-hosts.tmp`（原方案的写法），dnsmasq 会把它当成一份 hosts 文件读进去，rename 走之后里面的条目还留在缓存里（§8 实测）
- 按空白分隔取第二列精确比较主机名，不做子串匹配；`#` 开头的注释行不参与匹配、原样保留
- 写失败只记日志，不向调用方报错（DNS 不影响接口本身成败）

### 4.2 什么时候写

| 时机 | 调用 | 位置 |
|---|---|---|
| 注册新节点（`POST /hypers`，`HyperAdmin.Deploy` 建出状态 4 的记录后） | `SyncHostsInDns(hostname)` | `services/hyper.go` |
| 重试部署（已有状态 4 / 5 的记录，`db.Save` 之后，地址是新传的） | `SyncHostsInDns(hostname)` | 同上 |
| 删除节点（删库成功、`NodeRemove` 之后） | `SyncHostsInDns(hyper.Hostname)`：没有别的节点叫这个名字就删掉，有就改指向那台；删除失败时不调 | 同上 |
| 心跳 `hyper_status.sh` 上报的主机名或地址与库里不同 | 主机名变了 `SyncHostsInDns(旧名, 新名)`，只有地址变了 `SyncHostsInDns(名字)`；两者都没变不调。**在事务提交之后执行**（defer 注册在开事务之前），事务回滚时不调 | `rpcs/hyper_status.go` |
| clapi 启动 | 后台 goroutine 依次：`RebuildDnsHostsFile`（含服务名）→ `ApplyDnsUpstream` → `initS3WithRetry` | `services/services.go` |

- 心跳上报的地址是节点的 VTEP 地址（`report_rc.sh` 的 `$vtep_ip`），与部署时填的 IP 通常相同。**上报的地址为空**（开机时、切换 bond 的瞬间 VXLAN 网卡还没有地址）时不覆盖库里的 `host_ip`，DNS 条目也不动
- 为什么要在提交之后写：`SyncHostsInDns` 按已提交的数据重写；如果在事务里先写文件，clapi 正好启动、重建读到的还是旧地址，会把旧地址写回去，而之后的心跳因为库里与上报一致不会再改，旧地址要留到下次重启
- 服务名与节点在启动时一次写入：原先先重建（只有节点）、再单独补两个服务名，中间隔着 `ApplyDnsUpstream` 的两次 Docker 调用（各最多 3 秒），这段时间里查 `images.cloudland.internal` 会被转发到上游、拿到「没有这个名字」
- 顺序不能反：DNS 先就绪，`InitS3` 才解析得了 `S3_ENDPOINT`
- 写进 hyper-hosts 的是「注册过的节点」，不是「在线的节点」：离线节点的条目保留；部署失败的节点保留到被删除（`report_rc.sh` 只上报状态 0 / 1，没有任何路径把节点置成 5，原先心跳里「状态 5 删条目」的分支从来没执行过，2026-10-15 删掉）

## 5. 计算节点

`deploy/docker/scripts/deploy-compute-node.sh` 第 1 步：

- **解析器指向 `CONTROLLER_IP`**（即 deploy_command 里的 `MANAGEMENT_VIP`，没配时退到 `PUBLIC_IP`）。有 systemd-resolved 时写 drop-in `/etc/systemd/resolved.conf.d/cloudland.conf`（`DNS=$CONTROLLER_IP`、`DNSStubListener=no`），重启 resolved、清缓存，再把 `/etc/resolv.conf` 链到 `/run/systemd/resolve/resolv.conf`；没有 resolved 时直接写 `nameserver $CONTROLLER_IP`
- **`CONTROLLER_IP` 只是第一个 nameserver**：`/run/systemd/resolve/resolv.conf` 里除了全局的 `DNS=`，还有各网卡自己带的解析器。IBM Classic 的节点 bond1 带着 `10.0.80.11`、`10.0.80.12`，work-02 / 03 上实测是三条：控制面、`10.0.80.11`、`10.0.80.12`（2026-10-15）。glibc 按顺序问：第一台答了（包括「没有这个名字」）就用它的结果，不答（超时、端口不可达）才问下一台。所以控制面正常时所有名字都由 dnsmasq 答；控制面或 dnsmasq 停了，**节点主机名与两个内部域名都解析不到，外网域名由后面的解析器答**（控制面主机整台不可达时每次查询先等第一台超时，默认 5 秒）。网卡上没有别的解析器的环境，控制面停了就什么都解析不了
- **`DNS_SERVER` 不是这个 DNS**：它写进 `cloudrc.local` 的 `dns_server`，是给虚拟机用的（config drive 的 `network_data.json`、内部子网 DHCP、router-0 的 resolv.conf），界面默认 8.8.8.8。原方案 §7.3 写的「`DNS_SERVER` = 控制节点内网 IP、也是 dnsmasq 监听地址」不对
- `/etc/hosts` 仍然写：本机 `OWN_IP 主机名`（`NETWORK_DEVICE` 的地址，cloudlet 上报时要用），加脚本顶部可选的 `OTHER_NODES`
- 注册不经 DNS：节点用 `cloudlet-go node-add`（gRPC）向 cland 注册，不上报 IP；hyper-hosts 里的地址来自部署接口的参数和之后的心跳

## 6. 上游转发

- `ApplyDnsUpstream`（`services/dns_settings.go`）取系统设置 `DNS_UPSTREAM`（cpgateway 推到区域的镜像值；没有就 `8.8.8.8`），按逗号拆成多行 `server=`，同样经点开头的临时文件原子写 `conf.d/upstream.conf`，然后重载 dnsmasq
- 重载：经 docker.sock 调 Docker exec API，在 dnsmasq 容器里执行 `kill -HUP 1`。**不用 Docker 的 kill 接口**：哪怕只发 SIGHUP，它也会把容器标记为手动停止，`unless-stopped` 的 dnsmasq 在宿主机重启后不再被拉起。dnsmasq 没在运行时只记 Info
- 必须用 `--servers-file`：SIGHUP 会重读它，而 `--conf-dir` / `--conf-file` 里的 `server=` 只在启动时读（P19 原方案用的是 `--conf-dir`）
- 调用时机：clapi 启动时一次（§4.2），cpgateway 推送系统设置（`POST /internal/system-settings/sync`）成功后异步一次
- SIGHUP 不会清掉 hostsdir 里的条目（§8 实测）

## 7. 设计决策

| 问题 | 决策 |
|---|---|
| hosts 变更怎么通知 dnsmasq | `--hostsdir` + inotify，只写文件，不发信号；上游变更才用 SIGHUP |
| 挂载粒度 | 挂目录不挂文件：文件 bind mount 固定在创建时的 inode，rename 之后容器看不到新文件 |
| 原子写 | 点开头的临时文件 + rename；点开头是为了不被 hostsdir 读进去 |
| 文件归属 | 一个文件一个写入方：clapi 独占 `hyper-hosts`（可以整份重建），部署脚本独占 `control-hosts` |
| 数据来源 | `hypers` 表是唯一来源，启动时整份重建，不靠持久化的中间文件 |
| 并发 | 进程内互斥锁；HA 下两台 clapi 各写自己机器上的文件，不需要跨进程锁，但两份文件会暂时不一致（见 §9） |
| 主机名匹配 | 按第二列精确比较，注释行跳过 |
| 写入层 | 在 clapi 的接口层与回调层（Deploy / Delete / 心跳）维护，节点侧不参与 |
| 增量怎么写 | 不传「名字 → 地址」，只传受影响的名字，由 `SyncHostsInDns` 按表重写：同名节点、改名、删除都按表的现状决定，不会误删别的节点还在用的名字；改动提交之后才写 |
| 文件权限 | 写入方显式设 755 / 644，dnsmasq 以非 root 用户读 |
| 镜像版本 | 固定 `drpsychick/dnsmasq:2.89` |

## 8. dnsmasq 的实际行为（2026-10-15 实测）

在本机 WSL 用 dnsmasq 2.92 跑同样的参数（`--conf-file=/dev/null --no-resolv --no-hosts --hostsdir=<目录>`）测的；生产镜像是 2.89，它的程序里有同样的「inotify: flushed N names」逻辑（work-01 上 `docker exec cloudland-dnsmasq` 核对过），行为一致。

| 操作 | 结果 |
|---|---|
| 目录里新建点开头的文件 | 不读 |
| 新建不以点开头的 `hyper-hosts.tmp`，再 rename 成 `hyper-hosts` | `.tmp` 一出现就被读进去；rename 走之后它的条目**一直留着**（之后改 `hyper-hosts` 也清不掉） |
| rename 替换 `hyper-hosts`，新内容少了某个名字 | 该名字立即解析不到（dnsmasq 先清掉这个文件读过的全部名字再重读） |
| rename 替换，某个名字的 IP 变了 | 只返回新 IP |
| 两个文件里同名同 IP | 只返回一条 |
| 两个文件里同名不同 IP | 两条都返回 |
| SIGHUP | hostsdir 的条目都还在 |
| 删除一个 hosts 文件 | 它的条目立即解析不到 |

部署脚本的那一段也在沙箱里原样跑过：在 `umask 077` 下执行、dnsmasq 以 `nobody` 运行，控制节点主机名能解析，模拟 clapi 整份重写 hyper-hosts 后仍能解析，换 IP 重跑后被覆盖。不设 755 时 dnsmasq 报 `bad dynamic directory ... Permission denied`。测试脚本在会话临时目录（`dnstest.sh`、`ctrlhosts.sh`、`dnsq.py`），没进仓库。

## 9. 已知限制

- **`MANAGEMENT_VIP` 与 `INTERNAL_IP` 不同时，计算节点解析不了内部名字**（按代码推断，没在 HA 环境验证）：dnsmasq 用 `--bind-interfaces` 只监听 `INTERNAL_IP`，计算节点的第一个 nameserver 是 VIP，VIP 的 53 端口没有进程（端口不可达，glibc 立刻换下一台）。节点主机名与两个内部域名都解析不到；外网域名要看节点网卡上有没有别的解析器（IBM Classic 节点有，§5）。HA 部署一定是这种情况；`deploy-ha-node.sh` 也不建 `dns/` 目录、不写 `control-hosts` 与初始 `upstream.conf`。另外两台控制节点各有一份 hyper-hosts 和 upstream.conf：某次增删、某次改上游只发生在处理那个请求或回调的那台 clapi 上，另一台要到重启重建时才跟上（上游要等下一次设置同步）
- **内部名字只有控制面能答**：控制面或 dnsmasq 停了，节点主机名与两个内部域名都解析不到；外网域名靠节点网卡带来的其他解析器，没有的环境就什么都解析不了（§5）
- 同名的多台节点只有 `hostid` 最小的那台能按名字解析到（§4.1）
- 控制节点同时是计算节点、且注册时填的 IP 与 `INTERNAL_IP` 不同时，它的主机名会返回两个地址（都是本机）
- 部署失败的节点保留条目，直到被删除
- `deploy-compute-node.sh` 结束时的提示「如有多个计算节点，请修改脚本顶部 OTHER_NODES 变量并同步 /etc/hosts」已过时：节点之间的主机名由本服务解析
- 没有 DNSSEC；内部域名只是 hosts 记录，没有 SOA / NS

## 10. 扩展：DNS 独立部署

现在要求 dnsmasq 与 clapi 在同一台机器上（共享 `deploy/dns/` 目录）。要把 DNS 拆出去时，可以换成支持 RFC 2136 动态更新的服务（BIND9 / PowerDNS，开 TSIG），把 `SyncHostsInDns` / `RebuildDnsHostsFile` 改成网络调用；调用点（§4.2）不用变。没做。

## 11. 涉及的文件

| 文件 | 内容 |
|---|---|
| `deploy/docker/docker-compose.yml` | dnsmasq 服务；cland / clapi 的 `dns:`；clapi 挂 `../dns` 与 docker.sock；文件开头的手工初始化步骤 |
| `deploy/docker/scripts/deploy-control-node.sh` | 第 5 步建目录、写 `control-hosts` 与初始 `upstream.conf`、设权限 |
| `deploy/docker/scripts/deploy-compute-node.sh` | 第 1 步 resolv.conf、`/etc/hosts`；第 8 步 `dns_server`（给虚拟机） |
| `api/src/services/dns_hosts.go` | `SyncHostsInDns`、`RebuildDnsHostsFile`、服务名（`serviceDnsEntries`） |
| `api/src/services/dns_settings.go` | 上游转发与重载 |
| `api/src/services/services.go` | 启动时的顺序 |
| `api/src/services/hyper.go` | Deploy / Delete 之后同步那个名字 |
| `api/src/rpcs/hyper_status.go` | 心跳时跟随主机名、地址变化（提交之后）；空地址不覆盖 |
| `api/src/rpcs/hyper_status_dns_pg_test.go` | PostgreSQL：`TestHyperStatusDnsPG`（改地址只在提交后可见、空地址、空 cpu 型号、同名节点改名、删除）；`TestRebuildDnsHostsFilePG`（服务名的三种情况、同名取 `hostid` 最小、旧内容不留） |

## 12. 回归用例对照

由 `test-items/scripts/check_route_coverage.py --update-design-maps` 按追踪表 `test-items/回归功能追踪.csv` 的「设计依据」列生成，不要手改。

<!-- regression-map:begin -->

> 由 `test-items/scripts/check_route_coverage.py --update-design-maps` 从回归追踪表（`test-items/回归功能追踪.csv` 的「设计依据」列）生成，**不要手改**：改追踪表后重新生成，CI 会检查两边是否一致。

| 章节 | 功能（功能域 / 大功能 / 子功能） | 回归用例 |
|---|---|---|
| §3 文件与归属 | 部署与计算节点 / 部署 / 内部 DNS | TC-27 DEP-07；TC-01 BUILD-07c |
| §4.1 写法 | 部署与计算节点 / 部署 / 内部 DNS | TC-27 DEP-07；TC-01 BUILD-07c |
| §4.2 什么时候写 | 部署与计算节点 / 部署 / 内部 DNS | TC-27 DEP-07；TC-01 BUILD-07c |
| §5 计算节点 | 部署与计算节点 / 部署 / 内部 DNS | TC-27 DEP-07；TC-01 BUILD-07c |
| §6 上游转发 | 部署与计算节点 / 部署 / 内部 DNS | TC-27 DEP-07；TC-01 BUILD-07c |

<!-- regression-map:end -->

## 变更记录

- 2026-04：原方案（C++ 主控时期）
- 2026-10-15：按代码重写全文。同日修了两处：控制节点主机名从 `hyper-hosts` 挪到独立的 `control-hosts`（原先 clapi 每次启动重建 hyper-hosts 时把它冲掉，控制节点不是计算节点时就解析不到）；心跳上报的主机名变化时先删旧名的条目（原先旧名一直留着）。删去心跳里按状态 5 删条目的分支（没有任何路径上报 5）
- 2026-10-15（同日，代码审查后）：增删改为 `SyncHostsInDns` 按表重写（原先按名字直接删，同名的另一台节点会被误删；改名要写两次文件），心跳里的写入挪到事务提交之后（原先在事务里写，碰上 clapi 启动重建会把旧地址写回去且之后不再纠正）；心跳上报的地址为空时不再把 `host_ip` 写成空；`cpu_model` 为空时心跳回调不再下标越界（解析时末尾的空参数被丢掉，原有问题，x86 节点碰不到）；启动时服务名与节点一次写入（原先两次写入之间有空窗），内置 MinIO 用自定义的 `MINIO_HOSTNAME` 时也写进去（原先只认 `.cloudland.internal`，从来没注册上）；部署脚本显式设目录与文件权限；compose 文件开头的手工步骤补上 `control-hosts`；`.gitignore` 忽略临时文件。§5、§9 更正：计算节点的 resolv.conf 不只控制面一个 nameserver（实测还有 bond1 带来的 IBM 解析器），控制面停了或 HA 下解析不了的是内部名字，外网域名有兜底
