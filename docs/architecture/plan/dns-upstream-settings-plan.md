# DNS 上游转发配置 Web 化方案

## 背景

内部 DNS 方案（见 `internal-dns-plan.md`）中，dnsmasq 的上游转发地址（`--server=`）
通过控制节点的 `.env` 文件静态配置，修改后需要手动重启容器。

本方案将该配置纳入 System Settings 体系，支持通过 Web 管理界面动态修改，无需登录
服务器。

## 现有架构

```
Web UI → cpgateway (SETTINGS_METADATA + DB) → settings_sync_service
       → clapi /internal/system-settings/sync → system_setting_mirror 表
```

- `cpgateway/app/services/settings_service.py`：`SETTINGS_METADATA` 是所有设置项的
  注册表，加一个 key 即可在 Web UI 的 System Settings 页面自动显示
- `cpgateway` 每次保存设置后调用 `push_settings_to_all_regions` 推送到各 Region 的
  `clapi`
- `clapi` 的 `POST /internal/system-settings/sync` 全量替换本地 `system_setting_mirror`
  表，目前 **sync 完成后没有任何回调**

## 方案概述

```
用户在 Web UI 修改 DNS_UPSTREAM
  → cpgateway 保存并推送 sync 到 clapi
  → clapi SyncSystemSettings 成功后调用 applyDnsSettings()
  → 重写 /opt/cloudland/dns/conf.d/upstream.conf
  → curl docker socket 发送 SIGHUP 给 dnsmasq
  → dnsmasq 热重载配置，新上游立即生效
```

dnsmasq 的 hosts 变更（节点注册/删除）继续由 `--hostsdir` + inotify 自动感知，
**本方案只涉及上游 DNS 配置**，两条路径互不干扰。

## 实现步骤

### 1. cpgateway：注册 DNS_UPSTREAM 设置项

在 `settings_service.py` 的 `SETTINGS_METADATA` 中加入：

```python
# cpgateway/app/services/settings_service.py

SETTINGS_METADATA: dict[str, tuple] = {
    # --- 基础信息 ---
    "PROJECT_NAME":    ...,
    "FRONTEND_URL":    ...,

    # --- 内部 DNS ---
    "DNS_UPSTREAM": ("string", "general", "内部 DNS 上游转发地址（计算节点 hostname 未匹配时转发至此）",
                     False, lambda: env_settings.DNS_UPSTREAM or "8.8.8.8"),

    # ... 其余保持不变
}
```

同时在 `cpgateway/app/core/config.py` 的 `Settings` 中加入：

```python
DNS_UPSTREAM: str = "8.8.8.8"
```

Web UI 的 `SystemSettings.vue` 通用渲染逻辑会自动在 `general` 页签显示此字段，
无需前端改动。i18n key 对应 `settings.fields.DNS_UPSTREAM`，在中英文 locale 文件中
各加一行即可：

```ts
// web/src/locales/zh.ts
DNS_UPSTREAM: 'DNS 上游转发',
DNS_UPSTREAM_desc: '内部 dnsmasq 未能匹配节点 hostname 时，将 DNS 查询转发到此地址。支持逗号分隔多个地址，如 8.8.8.8,8.8.4.4。',

// web/src/locales/en.ts
DNS_UPSTREAM: 'DNS Upstream',
DNS_UPSTREAM_desc: 'Upstream DNS server for queries not matching internal node hostnames. Supports comma-separated multiple addresses, e.g. 8.8.8.8,8.8.4.4.',
```

### 2. docker-compose.yml：调整 dnsmasq 挂载与启动参数

#### 2.1 新增 conf.d 目录挂载

```yaml
  dnsmasq:
    image: drpsychick/dnsmasq:2.89
    container_name: cloudland-dnsmasq
    network_mode: host
    cap_add:
      - NET_ADMIN
    volumes:
      - ../dns:/etc/dnsmasq.d:ro          # hosts 目录（inotify 监听）
      - ../dns/conf.d:/etc/dnsmasq.d/conf.d:ro  # 配置文件目录（upstream.conf 在此）
    command: >
      --no-hosts
      --hostsdir=/etc/dnsmasq.d
      --conf-dir=/etc/dnsmasq.d/conf.d,*.conf
      --listen-address=${INTERNAL_IP:-127.0.0.1}
      --bind-interfaces
      --port=53
      --log-queries
    # 去掉 --server=，改由 conf.d/upstream.conf 控制

  clapi:
    # ... 原有配置 ...
    volumes:
      # ... 原有挂载 ...
      - ../dns/conf.d:/opt/cloudland/dns/conf.d   # ← 新增：clapi 写 upstream.conf
      - /var/run/docker.sock:/var/run/docker.sock  # ← 新增：发 SIGHUP 用
```

说明：
- `--conf-dir=/etc/dnsmasq.d/conf.d,*.conf`：加载目录内所有 `.conf` 文件
- `--server=` 从 command 移除，完全由 `conf.d/upstream.conf` 控制
- docker.sock 挂在 **clapi** 而非 cloudland，职责分离：cloudland 管 hosts，clapi 管配置

#### 2.2 初始化 conf.d 目录和 upstream.conf

```bash
# 部署前执行
mkdir -p deploy/dns/conf.d
echo "server=${DNS_UPSTREAM:-8.8.8.8}" > deploy/dns/conf.d/upstream.conf
```

目录结构：
```
deploy/
  dns/
    hyper-hosts           ← cloudland 读写，inotify 自动生效
    conf.d/
      upstream.conf       ← clapi 读写，变更后 SIGHUP 重载
```

### 3. clapi：sync 成功后应用 DNS 配置

#### 3.1 新增 dns_settings.go

```go
// api/src/services/dns_settings.go

package services

import (
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
)

// ApplyDnsUpstream 将新的上游 DNS 地址写入 upstream.conf 并热重载 dnsmasq。
// 在 SyncSystemSettings 成功提交后调用。
func ApplyDnsUpstream() {
    upstream := GetMirrorSetting("DNS_UPSTREAM")
    if upstream == "" {
        upstream = "8.8.8.8"
    }

    confDir := os.Getenv("DNS_CONF_DIR")
    if confDir == "" {
        confDir = "/opt/cloudland/dns/conf.d"
    }
    confFile := filepath.Join(confDir, "upstream.conf")

    // 支持多个上游地址（逗号分隔）
    var lines []string
    for _, addr := range strings.Split(upstream, ",") {
        addr = strings.TrimSpace(addr)
        if addr != "" {
            lines = append(lines, fmt.Sprintf("server=%s", addr))
        }
    }
    // 兜底：解析结果为空时保证写入一个有效上游，防止 dnsmasq 无上游配置
    if len(lines) == 0 {
        lines = []string{"server=8.8.8.8"}
    }
    content := strings.Join(lines, "\n") + "\n"

    tmpFile := confFile + ".tmp"
    if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
        logger.Errorf("DNS: failed to write %s: %v", tmpFile, err)
        return
    }
    if err := os.Rename(tmpFile, confFile); err != nil {
        logger.Errorf("DNS: rename %s -> %s failed: %v", tmpFile, confFile, err)
        return
    }

    logger.Infof("DNS: upstream set to %q", upstream)
    reloadDnsmasq()
}

// reloadDnsmasq 通过 Docker HTTP API 向 dnsmasq 容器发送 SIGHUP。
// clapi 容器挂载了 /var/run/docker.sock。
// 若 dnsmasq 未运行，curl 返回非零但被 || true 忽略。
func reloadDnsmasq() {
    cmd := `curl -sf --unix-socket /var/run/docker.sock ` +
        `-X POST 'http://localhost/containers/cloudland-dnsmasq/kill?signal=HUP' ` +
        `>/dev/null 2>&1 || true`
    if err := exec.Command("sh", "-c", cmd).Run(); err != nil {
        logger.Warningf("DNS: dnsmasq reload failed (container may not be running): %v", err)
    }
}
```

#### 3.2 在 SyncSystemSettings 成功后调用

```go
// api/src/apis/system_setting.go，Commit 成功后

if err := tx.Commit().Error; err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed: " + err.Error()})
    return
}

logger.Infof("System settings synced: %d settings, version=%d", len(req.Settings), req.ConfigVersion)

// 异步应用 DNS 上游配置（不阻塞 HTTP 响应）
go services.ApplyDnsUpstream()

c.JSON(http.StatusOK, gin.H{"status": "ok", "synced": len(req.Settings), "version": req.ConfigVersion})
```

> **异步调用原因**：curl 调用 docker socket 可能阻塞数百毫秒，不应拖慢 sync 响应。
> 写文件是幂等的，失败有日志，不影响下次 sync 重试。

#### 3.3 clapi Dockerfile 加 curl

```dockerfile
# deploy/docker/dockerfiles/Dockerfile.web（clapi target）
# 在运行时阶段加入 curl
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    && rm -rf /var/lib/apt/lists/*
```

需要确认 clapi 基础镜像，若已含 curl 则跳过此步骤。

### 4. clapi 启动时应用当前配置

clapi 启动时 `system_setting_mirror` 可能已有上次同步的数据，应在启动时
主动应用一次，避免 dnsmasq 重启后使用旧 `upstream.conf`：

```go
// api/src/main.go 或初始化入口，数据库连接建立后

services.ApplyDnsUpstream()
```

## 设计决策

| 问题 | 决策 |
|------|------|
| 为何 hosts 变更不需要 SIGHUP 但上游配置需要 | `--hostsdir` 由 inotify 监听文件变更自动重载；`--conf-dir` 不支持 inotify，必须 SIGHUP |
| docker.sock 挂在 clapi 而非 cloudland | 职责分离：cloudland 管节点注册（hosts），clapi 管系统设置（conf）；两者互不依赖 |
| 上游地址格式 | 支持逗号分隔多个地址（`8.8.8.8,8.8.4.4`），写入时展开为多行 `server=` |
| 原子写 | 先写 `.tmp` 再 `rename`，避免 dnsmasq SIGHUP 时读到截断文件 |
| 空行兜底 | 逗号分隔值全部为空格时，`lines` 解析结果为空，额外兜底写入 `server=8.8.8.8`，防止 dnsmasq 无上游配置启动失败 |
| `reloadDnsmasq` 命名 | 函数命名去掉 `FromClapi` 后缀；`exec.Command` 直接内联，不抽 `runShell` 辅助函数，调用链更短 |
| sync callback 异步 | curl 调用 docker socket 耗时不确定，go routine 异步执行，不阻塞 sync HTTP 响应 |
| 启动时应用 | clapi 和 dnsmasq 可能先后独立重启，启动时主动调用一次 `ApplyDnsUpstream` 保证一致性 |
| `.env` 中的 `DNS_UPSTREAM` | 继续作为初始默认值，部署时写入 `upstream.conf`；一旦用户通过 Web UI 修改，DB 值优先 |

## 文件变更清单

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `cpgateway/app/services/settings_service.py` | 修改 | `SETTINGS_METADATA` 加入 `DNS_UPSTREAM` |
| `cpgateway/app/core/config.py` | 修改 | `Settings` 加入 `DNS_UPSTREAM` 字段 |
| `web/src/locales/zh.ts` | 修改 | 加 `DNS_UPSTREAM` 和 `DNS_UPSTREAM_desc` |
| `web/src/locales/en.ts` | 修改 | 加 `DNS_UPSTREAM` 和 `DNS_UPSTREAM_desc` |
| `deploy/docker/docker-compose.yml` | 修改 | dnsmasq 加 conf.d 挂载，去掉 `--server=`；clapi 加 conf.d 和 docker.sock 挂载 |
| `deploy/dns/conf.d/upstream.conf` | 新增 | 初始上游 DNS 配置文件 |
| `api/src/services/dns_settings.go` | 新增 | `ApplyDnsUpstream`、`reloadDnsmasqFromClapi` |
| `api/src/apis/system_setting.go` | 修改 | Commit 成功后 `go services.ApplyDnsUpstream()` |
| `api/src/main.go`（或初始化入口） | 修改 | 启动时调用 `services.ApplyDnsUpstream()` |
| `deploy/docker/dockerfiles/Dockerfile.web` | 修改（按需） | clapi target 加 `curl` |

## 依赖关系

本方案依赖 `internal-dns-plan.md` 中的以下基础设施已部署：
- `deploy/dns/` 目录存在
- dnsmasq 容器以目录挂载方式运行
- cloudland 容器已配置 `user: root` 和 dns 目录挂载

## 不在本方案范围内

- 多上游 DNS 的 UI 展示优化（当前逗号分隔字符串，后续可改为列表控件）
- dnsmasq `--conf-dir` 的 inotify 支持（dnsmasq 官方不支持，SIGHUP 是唯一方式）
- DNS 配置变更的审计日志
