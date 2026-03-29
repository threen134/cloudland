# SSH Key 自动分发方案

## 背景

当前计算节点部署时，SSH key（`cland.key` / `cland.key.pub`）需要手动放到部署脚本同级 `.ssh/` 目录下。这个 key 用于：

1. **镜像同步**（`sync_target` — 本地存储模式下通过 rsync 分发镜像到计算节点）
2. **虚机冷迁移**（`copy_target` / `action_target` — 计算节点之间 scp 传输磁盘文件 + ssh 远程执行 virsh）

> 注：`create_portmap.sh` / `clear_portmap.sh` 中也使用了 SSH key 建立反向隧道，但该功能当前无前端 UI 和 API endpoint 暴露，属于历史遗留。

**关键路径：** 运行时脚本通过 `$cland_private_key`（即 `$deploy_dir/.ssh/cland.key`，定义在 `scripts/cloudrc:24`）读取私钥，因此该路径下必须存在 key 文件。

目标：通过 API 自动分发 SSH key，计算节点执行部署脚本时自动从控制节点获取，无需手动拷贝。

## 方案概述

在 SCI 服务的 `/internal/node/add` 接口中，注册节点成功后将 SSH key 随 response 一起返回。部署脚本在调用注册接口时直接获取 key，不引入新的服务依赖。

```
前端点击部署 → Deploy API（clapi）返回 deploy_command
→ 计算节点执行部署脚本 → curl POST /internal/node/add（SCI 5006）
→ SCI 注册节点成功，响应中附带 SSH key → 脚本解析响应写入本地 → 完成信任建立
```

### 设计原则

- **不引入新依赖** — 计算节点当前只与 SCI（5006）和 Loki（3100）通信，不新增与 clapi 的交互
- **复用现有请求** — 注册节点和获取 key 合并为一次调用，无需额外 endpoint
- **向后兼容** — 脚本保留本地文件探测作为兜底
- **幂等安全** — 重复执行不会覆盖已有公钥、不会破坏密钥格式

## 安全说明

当前控制面与计算节点之间的内部通信均无认证：

| 通道 | 端口 | 认证 |
|------|------|------|
| SCI RPC（`/internal/execute`、`/node/add`、`/node/remove`） | 5006 | 无 |
| SCI 二进制协议 | SCI 端口 | 无 |
| clapi internal | 8255 | 仅 Shared Secret（CPGateway → clapi） |

防火墙规则放行所有私网段（10/8、172.16/12、192.168/16），在 response 中附带 SSH key 不增加额外风险。后续可统一治理内部通信安全。

## 涉及文件

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `src/rpcworker.cpp` | 修改 | `/internal/node/add` 返回值中附带 SSH key |
| `deploy/docker/scripts/deploy-compute-node.sh` | 修改 | 解析注册响应中的 key 并写入本地 |

## 详细设计

### 1. 修改 SCI `/internal/node/add` 响应

**位置：** `src/rpcworker.cpp` 第 379-410 行

在注册成功后，读取 key 文件内容，附加到 JSON response 中。

**当前响应：**
```json
{"status": "ok", "id": 1}
```

**修改后响应：**
```json
{
  "status": "ok",
  "id": 1,
  "public_key": "ssh-rsa AAAA...",
  "private_key": "-----BEGIN RSA PRIVATE KEY-----\n..."
}
```

**C++ 实现要点：**

```cpp
http.Post("/internal/node/add", [this](const Request &req, Response &res) {
    // ... 现有的注册逻辑不变 ...

    Json::Value resp;
    Json::FastWriter writer;
    if (result >= 0) {
      resp["status"] = "ok";
      resp["id"] = result;

      // 读取 SSH key 文件，路径通过环境变量配置，默认 fallback 到容器内挂载路径
      const char *keyDirEnv = getenv("CLOUDLAND_SSH_KEY_DIR");
      string keyDir = keyDirEnv ? keyDirEnv : "/opt/cloudland/deploy/.ssh";
      ifstream pubFile(keyDir + "/cland.key.pub");
      ifstream privFile(keyDir + "/cland.key");
      if (pubFile.is_open() && privFile.is_open()) {
        string pubKey((istreambuf_iterator<char>(pubFile)),
                       istreambuf_iterator<char>());
        string privKey((istreambuf_iterator<char>(privFile)),
                        istreambuf_iterator<char>());
        // 确保读取内容非空，避免下发空密钥导致计算节点写入空文件
        if (!pubKey.empty() && !privKey.empty()) {
          resp["public_key"] = pubKey;
          resp["private_key"] = privKey;
        } else {
          log_warn("SSH key files in %s are empty, skipping key distribution",
                   keyDir.c_str());
        }
      } else {
        log_warn("SSH key files not found in %s, skipping key distribution",
                 keyDir.c_str());
      }
    } else {
      res.status = 500;
      resp["error"] = "failed to add backend";
      resp["rc"] = result;
    }
    res.set_content(writer.write(resp), "application/json");
});
```

**说明：**
- `cloudland` 容器已挂载 key 目录：`../.ssh:/opt/cloudland/deploy/.ssh:ro`（`docker-compose.yml:71`）
- key 路径通过 `CLOUDLAND_SSH_KEY_DIR` 环境变量配置，避免硬编码；默认值兼容当前容器挂载路径
- key 文件不存在或内容为空时只记录 warning，不影响节点注册
- 需要在文件头部添加 `#include <fstream>` 和 `#include <iterator>`

### 2. 修改部署脚本

**位置：** `deploy/docker/scripts/deploy-compute-node.sh`

将节点注册和 SSH key 获取合并，注册响应中直接解析 key。

#### 改动 1：步骤 4 SSH 配置段（第 186-218 行）

先尝试本地文件，找不到则留给步骤 15 从 API 获取。增加文件可读性检查，避免权限问题导致静默 fallback。

```bash
# ============ 4. SSH 配置 ============
log "4/15 - 配置 SSH 免密"

mkdir -p /home/cland/.ssh /root/.ssh "$CLOUDLAND_DIR/deploy/.ssh"

SSH_KEYS_INSTALLED=""

# 尝试从本地文件获取（兼容手动部署）
SEARCH_PATHS=("$SCRIPT_DIR/.ssh" "$CLOUDLAND_DIR/deploy/.ssh")
for p in "${SEARCH_PATHS[@]}"; do
    if [ -f "$p/cland.key.pub" ] && [ -f "$p/cland.key" ]; then
        # 检查文件是否可读，避免权限错误导致静默 fallback
        if [ ! -r "$p/cland.key.pub" ] || [ ! -r "$p/cland.key" ]; then
            warn "SSH 密钥文件存在于 $p 但不可读，请检查文件权限"
            continue
        fi
        # 公钥追加而非覆盖，避免清除管理员手动添加的公钥
        grep -qF "$(cat "$p/cland.key.pub")" /home/cland/.ssh/authorized_keys 2>/dev/null \
            || cat "$p/cland.key.pub" >> /home/cland/.ssh/authorized_keys
        grep -qF "$(cat "$p/cland.key.pub")" /root/.ssh/authorized_keys 2>/dev/null \
            || cat "$p/cland.key.pub" >> /root/.ssh/authorized_keys
        # 私钥写入所有需要的位置
        cp "$p/cland.key" "$CLOUDLAND_DIR/deploy/.ssh/cland.key"
        cp "$p/cland.key" /root/.ssh/id_rsa
        chmod 600 /root/.ssh/id_rsa "$CLOUDLAND_DIR/deploy/.ssh/cland.key"
        log "从本地文件获取 SSH 密钥: $p"
        SSH_KEYS_INSTALLED="local"
        break
    fi
done

if [ -z "$SSH_KEYS_INSTALLED" ]; then
    log "本地未找到 SSH 密钥，将在节点注册时从控制节点获取"
fi

chown -R cland:cland /home/cland/.ssh "$CLOUDLAND_DIR/deploy/.ssh"
chmod 700 /home/cland/.ssh /root/.ssh
chmod 600 /home/cland/.ssh/authorized_keys 2>/dev/null || true
chmod 600 /root/.ssh/authorized_keys 2>/dev/null || true
```

#### 改动 2：步骤 15 节点注册段（第 553-566 行）

解析注册响应，提取 SSH key 并写入**所有必需路径**。使用 `printf` 写入私钥避免 `echo` 解析转义字符破坏格式；公钥使用追加模式避免覆盖已有条目。

```bash
# ============ 15. 通过 API 向控制面注册计算节点 ============
log "15/15 - 通过 API 向控制面注册计算节点"
RPC_SERVER_PORT="${RPC_SERVER_PORT:-5006}"
for i in 1 2 3; do
    REGISTER_RESP=$(curl -sf -X POST "http://${CONTROLLER_IP}:${RPC_SERVER_PORT}/internal/node/add" \
        -H "Content-Type: application/json" \
        -d "{\"hostname\": \"${HOSTNAME}\", \"id\": ${SCI_CLIENT_ID}, \"level\": 1}" 2>/dev/null || true)
    if [ -n "$REGISTER_RESP" ] && echo "$REGISTER_RESP" | jq -e '.status == "ok"' &>/dev/null; then
        log "节点注册成功"

        # 从注册响应中提取 SSH key（如果本地未安装）
        if [ -z "$SSH_KEYS_INSTALLED" ]; then
            PUB_KEY=$(echo "$REGISTER_RESP" | jq -r '.public_key // empty')
            PRIV_KEY=$(echo "$REGISTER_RESP" | jq -r '.private_key // empty')
            if [ -n "$PUB_KEY" ] && [ -n "$PRIV_KEY" ]; then
                # 公钥 → authorized_keys（追加模式，避免覆盖已有公钥）
                grep -qF "$PUB_KEY" /home/cland/.ssh/authorized_keys 2>/dev/null \
                    || printf '%s\n' "$PUB_KEY" >> /home/cland/.ssh/authorized_keys
                grep -qF "$PUB_KEY" /root/.ssh/authorized_keys 2>/dev/null \
                    || printf '%s\n' "$PUB_KEY" >> /root/.ssh/authorized_keys

                # 私钥 → 三个位置（使用 printf 确保多行内容原样写入）：
                # 1. $deploy_dir/.ssh/cland.key — 运行时脚本读取路径（cloudrc:24）
                # 2. /root/.ssh/id_rsa — root 用户 SSH 默认路径
                # 3. /home/cland/.ssh/cland.key — cland 用户备份
                mkdir -p "$CLOUDLAND_DIR/deploy/.ssh"
                printf '%s\n' "$PRIV_KEY" > "$CLOUDLAND_DIR/deploy/.ssh/cland.key"
                printf '%s\n' "$PUB_KEY"  > "$CLOUDLAND_DIR/deploy/.ssh/cland.key.pub"
                cp "$CLOUDLAND_DIR/deploy/.ssh/cland.key" /root/.ssh/id_rsa
                cp "$CLOUDLAND_DIR/deploy/.ssh/cland.key" /home/cland/.ssh/cland.key

                # 权限设置
                chown -R cland:cland /home/cland/.ssh "$CLOUDLAND_DIR/deploy/.ssh"
                chmod 700 /home/cland/.ssh
                chmod 600 /home/cland/.ssh/authorized_keys /home/cland/.ssh/cland.key
                chmod 600 /root/.ssh/id_rsa /root/.ssh/authorized_keys
                chmod 600 "$CLOUDLAND_DIR/deploy/.ssh/cland.key"

                log "从控制节点注册响应中获取 SSH 密钥成功"
                SSH_KEYS_INSTALLED="api"
            else
                warn "控制节点未返回 SSH 密钥"
            fi
        fi
        break
    else
        warn "节点注册失败 (尝试 $i/3)，5 秒后重试..."
        sleep 5
    fi
done

if [ -z "$SSH_KEYS_INSTALLED" ]; then
    warn "未能获取 SSH 密钥，请手动配置"
fi
```

### 私钥写入路径说明

运行时各功能读取私钥的路径不同，必须确保所有路径都有文件：

| 路径 | 用途 | 读取方 |
|------|------|--------|
| `$CLOUDLAND_DIR/deploy/.ssh/cland.key` | `sync_target`、`copy_target`、`action_target` | `scripts/cloudrc:24`（`$cland_private_key`） |
| `/root/.ssh/id_rsa` | root SSH 默认密钥 | 部署脚本原有逻辑 |
| `/home/cland/.ssh/cland.key` | cland 用户备份 | — |

## 优先级与兼容性

SSH key 获取优先级（先到先得）：

1. **本地文件**（步骤 4）— 脚本同级 `.ssh/` 或 CloudLand 默认路径（兼容手动部署）
2. **注册响应**（步骤 15）— 从 `/internal/node/add` 响应中自动获取（推荐的自动化方式）
3. **手动配置**（告警）— 两者都失败时输出警告

现有的手动放文件方式完全不受影响。

## 测试验证

1. 部署新计算节点，不放 `.ssh/` 文件，确认脚本能从注册响应中自动获取 key
2. 验证 key 写入了所有三个路径（`$CLOUDLAND_DIR/deploy/.ssh/`、`/root/.ssh/`、`/home/cland/.ssh/`）
3. 拉取后验证控制节点能 SSH 到计算节点（`ssh -i cland.key cland@<node>`）
4. 验证 key 文件不存在时（SCI 容器未挂载 .ssh），节点注册仍正常成功，只是不返回 key
5. 验证 key 文件存在但内容为空时，SCI 不返回 key，脚本输出警告
6. 验证手动放置 `.ssh/` 文件时，步骤 4 直接安装，步骤 15 跳过 key 提取
7. 验证本地文件存在但权限不可读时，脚本输出警告并 fallback 到 API 获取
8. 验证重复执行部署脚本时，authorized_keys 中不会出现重复的公钥条目
9. 验证虚机迁移（`migrate_vm.sh`）中 `copy_target`/`action_target` 正常工作
10. 编译 SCI（`cd src && make`）确认 C++ 改动无编译错误
