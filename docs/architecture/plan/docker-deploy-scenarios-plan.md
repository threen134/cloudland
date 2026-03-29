# CloudLand Docker 部署方案更新计划

> 日期: 2026-03-13
> 状态: 待审核

## 1. 背景与现状分析

### 1.1 当前架构组件

| 组件 | 位置 | 技术栈 | Docker 状态 |
|------|------|--------|-------------|
| cloudland 主控 (SCI) | `src/`, `sci/` | C++ | ✅ 已有 `Dockerfile.cloudland`，已集成 |
| clapi (统一 Go API，含原 clbase) | `api/` | Go | ⚠️ `Dockerfile.web` 仍引用旧 `web/` 目录，**需更新**；不对外暴露端口，仅内网可达 |
| alarm-rules-manager | `api/cmds/alarm_rules_manager` | Go | ⚠️ 同上，需随 Dockerfile 一起更新 |
| consoleproxy | 独立 Dockerfile | Go | ✅ 已有，已集成 |
| nginx | `config/nginx/` | Nginx | ✅ 已有，需更新路由 |
| 监控栈 | prometheus/grafana/loki/promtail/alertmanager | - | ✅ 已有，已集成 |
| **Vue.js 前端 (新 UI)** | `web/` (vite + vue3) | TypeScript/Vue3 | ❌ **无 Dockerfile，未集成，参数化可选组件** |
| **middle 中间层** | `middle/` | Python FastAPI | ⚠️ 有独立 `middle/docker/`，**未集成到主 compose** |

### 1.2 关键变更事实

1. **clbase 已合并进 clapi** — Go 代码已从 `web/` 迁移到 `api/`，只保留一个统一的 `clapi` 服务
2. **`web/` 目录现在是纯 Vue.js 前端** — 不再包含 Go 代码，构建产出 `dist/` 静态文件
3. **clapi 不对外暴露** — 所有外部 API 请求通过 middle 代理访问 clapi，clapi 仅在 Docker 内网可达 (或跨机时仅管理网可达)
4. **Vue.js UI 为参数化可选组件** — 通过独立 profile `ui` 控制是否部署，与 `full` (middle) 解耦

### 1.3 核心问题

1. **`Dockerfile.web` 过时** — 仍引用 `web/go.mod`，实际 Go 代码已迁移到 `api/`
2. **docker-compose.yml 中 clbase 服务多余** — clbase 已合并进 clapi
3. **clapi 不应对外暴露端口** — 移除公网端口映射，仅 Docker 内网可达；跨机部署时仅映射到管理网 IP (INTERNAL_IP)，允许其他机器上的 middle 通过管理网访问
4. **Vue.js 前端无 Dockerfile** — 无法容器化构建
5. **middle 层独立部署** — 与主 docker-compose.yml 分离，无法统一编排
6. **场景二 (仅控制面) 不需要 nginx** — nginx 应仅在场景一 (full profile) 启动

### 1.4 组件调用关系 (目标架构)

**场景一: UI + Middle + 控制面 (完整部署)**

```
用户浏览器
    │
    ▼
┌──────────────────────────────────────────────────────────┐
│                  Nginx + UI (80/443)                      │
│   静态文件服务 + 反向代理                                  │
│                                                           │
│   /              → Vue.js 静态文件 (nginx 直服) [可选]     │
│   /api/v1/       → middle (FastAPI :8000)                 │
│   /websockify    → consoleproxy (:9443)                   │
└────────┬───────────────────────────┬──────────────────────┘
         │                           │
         ▼                           ▼
   ┌──────────┐                ┌────────────┐
   │  middle   │                │consoleproxy│
   │  :8000    │                │  :9443     │
   └────┬─────┘                └────────────┘
        │ (Docker 内网调用, 不对外暴露)
        ▼
   ┌──────────┐
   │  clapi   │
   │  :8255   │ (仅内网)
   └────┬─────┘
        │
        ├──────────────┐
        ▼              ▼
   ┌────────┐    ┌──────────┐
   │postgres│    │cloudland │ (SCI 主控, host 网络)
   └────────┘    │  :9988   │
                 └────┬─────┘
                      │ SCI 协议
                      ▼
                计算节点 (Hyper)
```

**场景二: 仅控制面 (无 nginx / middle / UI)**

```
                 ┌──────────┐
                 │cloudland │ (SCI 主控, host 网络)
                 │  :9988   │
                 └────┬─────┘
                      │ SCI 协议
                      ▼
                计算节点 (Hyper)

内部服务 (仅 Docker 内网 / 管理网可达):
  clapi :8255 (无端口映射，或仅映射到 INTERNAL_IP)
  consoleproxy :9443
  postgres :5432
```

**关键设计**:
- 场景一: nginx 作为统一入口，内置 Vue.js 静态文件，`/` 直接服务静态文件，`/api/v1/` 代理到 middle → clapi (内网)。clapi 不对外暴露。
- 场景二: 无 nginx/middle/UI，clapi 仅内网可达 (无端口映射) 或仅映射到管理网 IP (INTERNAL_IP)，供其他机器上的 middle 或内部脚本通过管理网调用。
- **Middle → clapi 认证**: middle 通过 Region 模型管理 clapi 连接，转发请求时携带 `X-Forwarded-Secret` (共享密钥) + 用户上下文 Header (`X-User-ID`, `X-Org-ID` 等)，clapi 验证 `middle.secret` 配置项。

---

## 2. 部署场景定义

### 场景一：完整部署 (UI + Middle + CloudLand 控制面)

**适用场景**: 生产环境完整部署，带 Vue.js 前端界面

**启动的服务**:
- nginx (内置 Vue.js 静态文件 + 反向代理 middle/consoleproxy)
- middle (FastAPI，统一 API 网关)
- clapi (仅 Docker 内网)
- consoleproxy
- cloudland (SCI 主控)
- postgres (dev 模式) 或外部 DB
- 监控栈 (可选)

**启动命令**:
```bash
COMPOSE_PROFILES=full,dev docker compose up -d --build
# 或生产模式 (外部 DB):
COMPOSE_PROFILES=full docker compose up -d --build
```

### 场景二：仅控制面部署 (CloudLand 控制面)

**适用场景**: 只需要 cloudland 控制面，不需要 nginx / middle / UI。clapi 仅内网可达 (无端口映射) 或仅映射到管理网 IP，供其他机器上的 middle 或内部脚本通过管理网调用。

**启动的服务**:
- clapi (仅内网，无端口映射或仅映射到 INTERNAL_IP)
- consoleproxy
- cloudland (SCI 主控)
- postgres (dev 模式) 或外部 DB
- 监控栈 (可选)

**不启动的服务**: nginx, middle, ui

**启动命令**:
```bash
COMPOSE_PROFILES=dev docker compose up -d --build
# 或生产模式:
docker compose up -d --build
```

---

## 3. 需要新增/修改的文件清单

### 3.1 新增文件

| 文件 | 说明 |
|------|------|
| `deploy/docker/dockerfiles/Dockerfile.ui` | Vue.js 前端构建 (多阶段: node build → 产出静态文件)，**可选组件** |
| `deploy/docker/dockerfiles/Dockerfile.middle` | middle 层 Dockerfile (基于现有 `middle/docker/Dockerfile` 改造) |
| `deploy/docker/scripts/middle-entrypoint.sh` | middle 容器入口脚本 (DB 就绪探测等) |

### 3.2 修改文件

| 文件 | 修改内容 |
|------|---------|
| `deploy/docker/dockerfiles/Dockerfile.web` | **重写**: 改为引用 `api/` 目录，移除 clbase target |
| `deploy/docker/docker-compose.yml` | 移除 clbase；nginx 移入 `full` profile；新增 `ui` (profile: ui)、`middle` (profile: full) |
| `deploy/docker/config/nginx/ssl.conf` | `/` 服务 Vue.js 静态文件；`/api/v1` 代理到 middle；移除直接代理 clapi |
| `deploy/docker/.env.example` | 新增 middle 相关环境变量 |
| `deploy/docker/scripts/deploy-control-node.sh` | 支持 `COMPOSE_PROFILES=full,ui` 选项 |
| `deploy/docker/scripts/init-db.sql` | 新增 `cloudland_middle` 数据库 |
| `deploy/docker/README.md` | 补充场景化部署文档 |

---

## 4. 详细实施步骤

### Step 1: 重写 Dockerfile.web (clapi 统一构建)

**文件**: `deploy/docker/dockerfiles/Dockerfile.web`

**核心变更**: 源码目录从 `web/` 改为 `api/`，移除 clbase target，只保留 clapi 和 alarm-rules-manager

```dockerfile
# ---- Stage 1: Go 编译 ----
FROM golang:1.23 AS builder
WORKDIR /build

COPY api/go.mod api/go.sum ./
RUN go mod download
COPY api/ ./

RUN echo "docker-build" > .version

# 编译 clapi (已含原 clbase 功能)
RUN cd cmds/api && go build -o /out/clapi -ldflags "-X \"main.Version=$(cat /build/.version)\""

# 编译 alarm_rules_manager
RUN cd cmds/alarm_rules_manager && CGO_ENABLED=0 go build -o /out/alarm_rules_manager

# ---- Stage 2: clapi 运行时 ----
FROM ubuntu:22.04 AS clapi
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates openssh-client rsync && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /opt/cloudland/web/conf
COPY --from=builder /out/clapi /opt/cloudland/web/clapi
COPY api/docs/ /opt/cloudland/web/docs/
COPY api/templates/ /opt/cloudland/web/templates/
COPY api/conf/locale/ /opt/cloudland/web/conf/locale/
WORKDIR /opt/cloudland/web
COPY deploy/docker/scripts/web-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8255
ENTRYPOINT ["/entrypoint.sh"]
CMD ["./clapi", "--daemon"]

# ---- Stage 3: alarm-rules-manager ----
FROM alpine:3.18 AS alarm-rules-manager
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/alarm_rules_manager /opt/cloudland/web/bin/alarm_rules_manager
EXPOSE 8256
ENTRYPOINT ["/opt/cloudland/web/bin/alarm_rules_manager"]
```

### Step 2: 创建 Nginx + UI 的 Dockerfile (打包在一起)

**文件**: `deploy/docker/dockerfiles/Dockerfile.nginx-ui`

**策略**: 多阶段构建 — Stage 1 编译 Vue.js 前端，Stage 2 将静态文件打包进 nginx 镜像

```dockerfile
# ---- Stage 1: 编译 Vue.js 前端 ----
FROM node:20-alpine AS builder
WORKDIR /build

# 安装依赖
COPY web/package.json web/package-lock.json* ./
RUN npm ci

# 复制源码并构建
COPY web/ ./
RUN npm run build

# ---- Stage 2: Nginx + 静态文件 ----
FROM nginx:1.25-alpine
WORKDIR /usr/share/nginx/html

# 复制 Vue.js 构建产物
COPY --from=builder /build/dist /usr/share/cloudland-ui

# 复制 nginx 配置
COPY deploy/docker/config/nginx/nginx.conf /etc/nginx/nginx.conf
COPY deploy/docker/config/nginx/ssl.conf /etc/nginx/conf.d/ssl.conf
COPY deploy/docker/config/nginx/default.conf /etc/nginx/conf.d/default.conf

EXPOSE 80 443 4000

CMD ["nginx", "-g", "daemon off;"]
```

### Step 3: 创建 Middle 层的 Dockerfile

**文件**: `deploy/docker/dockerfiles/Dockerfile.middle`

```dockerfile
FROM python:3.12-slim
WORKDIR /app
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
RUN apt-get update && apt-get install -y gcc && rm -rf /var/lib/apt/lists/*
COPY middle/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY middle/app ./app
COPY middle/cland_bankend_swager.json ./
RUN mkdir -p /app/data /app/keys
EXPOSE 8000
HEALTHCHECK --interval=15s --timeout=5s --retries=5 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:8000/').read()" || exit 1
COPY deploy/docker/scripts/middle-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
CMD ["python", "-m", "app.main"]
```

### Step 4: 更新 docker-compose.yml

**关键变更**:
1. **移除 clbase 服务** (已合并进 clapi)
2. **clapi 移除公网端口映射** — 默认仅 Docker 内网可达；跨机部署时可选映射到 `${INTERNAL_IP}:8255` (管理网)
3. **nginx 移入 `full` profile，并集成 UI** — 使用新的 `Dockerfile.nginx-ui` 构建，内置 Vue.js 静态文件
4. **移除独立的 ui 服务** — UI 已打包进 nginx 容器
5. **新增 middle 服务** (profiles: full)

```yaml
  # ========== 移除 clbase 服务 (整个 block 删除) ==========

  # ========== clapi: 移除公网端口映射，仅内网可达 ==========
  clapi:
    build:
      context: ../../
      dockerfile: deploy/docker/dockerfiles/Dockerfile.web
      target: clapi
    image: cloudland-clapi:latest
    container_name: cloudland-clapi
    restart: unless-stopped
    environment:
      # ... 保持现有环境变量 ...
    volumes:
      - ./volumes/certs:/certs:ro
      - ./volumes/certs/cland/selfsigned.crt:/etc/ssl/certs/alarm_rules_manager.crt:ro
      - ../.ssh:/opt/cloudland/deploy/.ssh:ro
    # 默认不映射端口到宿主机，仅 Docker 内网可达
    # 跨机部署时，可通过环境变量 CLAPI_EXPOSE_INTERNAL=true 映射到管理网 IP
    ports:
      - "${INTERNAL_IP:-127.0.0.1}:8255:8255"  # 默认仅 localhost，跨机时设置 INTERNAL_IP 为管理网 IP
    networks:
      cloudland:
        ipv4_address: 172.28.0.30

  # ========== nginx: 移入 full profile，集成 UI (仅场景一) ==========
  nginx:
    build:
      context: ../../
      dockerfile: deploy/docker/dockerfiles/Dockerfile.nginx-ui
    image: cloudland-nginx-ui:latest
    container_name: cloudland-nginx
    restart: unless-stopped
    profiles:
      - full                             # 仅场景一启动
    volumes:
      - ./volumes/certs/nginx:/certs/nginx:ro
      - image_cache:/opt/cloudland/cache/image
    ports:
      - "80:80"
      - "443:443"
      - "4000:4000"
    networks:
      cloudland:
        ipv4_address: 172.28.0.50
    depends_on:
      - middle
      - consoleproxy

  # ========== 新增: Middle 中间层 (profile: full) ==========
  middle:
    build:
      context: ../../
      dockerfile: deploy/docker/dockerfiles/Dockerfile.middle
    image: cloudland-middle:latest
    container_name: cloudland-middle
    restart: unless-stopped
    profiles:
      - full
    environment:
      DATABASE_URL: "postgresql+asyncpg://${POSTGRES_USER:-postgres}:${POSTGRES_PASSWORD:-d6Passwd}@${DB_HOST:-postgres}:${DB_PORT:-5432}/${MIDDLE_DB:-cloudland_middle}"
      SECRET_KEY: ${MIDDLE_SECRET_KEY:-change-me-in-production}
      FIRST_SUPERUSER_EMAIL: ${ADMIN_EMAIL:-admin@cloudland.local}
      FIRST_SUPERUSER_USERNAME: ${MIDDLE_ADMIN_USER:-admin}
      FIRST_SUPERUSER_PASSWORD: ${ADMIN_PASSWORD:-passw0rd}
      FRONTEND_URL: "https://${PUBLIC_IP:-127.0.0.1}"
      SMTP_HOST: ${SMTP_HOST:-localhost}
      SMTP_PORT: ${SMTP_PORT:-587}
      SMTP_TLS: ${SMTP_TLS:-True}
      SMTP_USER: ${SMTP_USER:-}
      SMTP_PASSWORD: ${SMTP_PASS:-}
      SMTP_FROM: ${ALERT_SENDER:-noreply@cloudland.local}
      SMTP_FROM_NAME: "CloudLand"
      LISTEN_ADDR: "0.0.0.0"
      LISTEN_PORT: "8000"
    volumes:
      - middle_data:/app/data
      - middle_keys:/app/keys
    ports:
      - "${INTERNAL_IP:-127.0.0.1}:8000:8000"
    networks:
      cloudland:
        ipv4_address: 172.28.0.96
    depends_on:
      - clapi
```

**新增 volumes**:
```yaml
  middle_data:
    name: cloudland-middle-data
  middle_keys:
    name: cloudland-middle-keys
```

**移除 volumes**:
```yaml
  # ui_dist 不再需要 (UI 已打包进 nginx 镜像)
```

### Step 5: 更新 Nginx 配置 (ssl.conf)

nginx 仅在场景一 (`full` profile) 启动，配置已在 Dockerfile 中打包。

**核心变更**:
- `/` 服务 Vue.js 静态文件 (位于 `/usr/share/cloudland-ui`)
- `/api/v1/` 代理到 middle (middle 内部代理 clapi)
- 移除原有直接代理 clbase/clapi 的配置

```nginx
server {
    listen 443 http2 ssl;
    # ... SSL 配置不变 ...

    # Vue.js SPA — nginx 直接服务静态文件
    root /usr/share/cloudland-ui;
    index index.html;

    location / {
        try_files $uri $uri/ /index.html;
        expires 7d;
        add_header Cache-Control "public";
    }

    # 所有 API 请求 → middle (middle 内部代理到 clapi)
    location /api/v1/ {
        proxy_pass http://middle:8000/api/v1/;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_buffering off;
        client_max_body_size 10m;
        proxy_read_timeout 60s;
    }

    # VNC Console WebSocket
    location /websockify {
        proxy_pass https://consoleproxy:9443;
        proxy_set_header Host $host:$server_port;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_buffering off;
        client_max_body_size 0;
        proxy_read_timeout 3600s;
    }
}
```

### Step 6: 更新 .env.example

```ini
# ---- Middle 中间层配置 (full 模式) ----
MIDDLE_DB=cloudland_middle
MIDDLE_SECRET_KEY=change-me-in-production
MIDDLE_ADMIN_USER=admin
```

### Step 7: 更新 deploy-control-node.sh

```bash
vars=("PUBLIC_IP" "INTERNAL_IP" "MANAGEMENT_VIP" "NETWORK_DEVICE"
      "DB_LISTEN_IP" "POSTGRES_USER" "POSTGRES_PASSWORD" "POSTGRES_DB"
      "ADMIN_PASSWORD" "ADMIN_EMAIL" "COMPOSE_PROFILES" "DB_HOST" "DB_PORT"
      "MIDDLE_DB" "MIDDLE_SECRET_KEY" "MIDDLE_ADMIN_USER")
```


### Step 8: 更新 init-db.sql

```sql
-- 为 middle 层创建独立数据库
CREATE DATABASE cloudland_middle;
GRANT ALL PRIVILEGES ON DATABASE cloudland_middle TO postgres;
```

### Step 9: 更新 README.md

补充场景化部署文档。

---

## 5. Profile 与场景映射

| COMPOSE_PROFILES 值 | 启动的服务 | 场景 |
|---------------------|-----------|------|
| `dev` | 控制面 (clapi 仅内网 + cloudland + consoleproxy) + 本地 postgres | 场景二 (开发) |
| (空) | 控制面 (外部 DB, clapi 仅内网) | 场景二 (生产) |
| `full,dev` | nginx (含 UI) + middle + 控制面 + 本地 postgres | 场景一完整 (开发) |
| `full` | nginx (含 UI) + middle + 控制面 (外部 DB) | 场景一完整 (生产) |

**无 profile 的服务** (始终启动): cloudland, clapi, consoleproxy, 监控栈
**`dev` profile**: postgres
**`full` profile**: nginx (内置 UI), middle

**UI 集成到 nginx**: 不再需要独立的 `ui` profile，UI 静态文件已打包进 nginx 镜像

---

## 6. 关键设计决策

### 6.1 clapi 端口映射策略

**默认行为 (同机部署)**:
- clapi 端口映射到 `127.0.0.1:8255`，仅本机可达
- middle 通过 Docker 服务名 `clapi:8255` 访问 (Docker 内网)
- Region 配置: `internal_endpoint = "https://clapi:8255"`

**跨机部署**:
- 设置环境变量 `INTERNAL_IP=<管理网IP>` (如 `10.0.0.5`)
- clapi 端口映射到 `${INTERNAL_IP}:8255`，管理网可达，公网不可达
- 其他机器上的 middle 通过管理网访问 clapi
- Region 配置: `internal_endpoint = "https://10.0.0.5:8255"`

**认证机制**:
- middle 转发请求时携带 `X-Forwarded-Secret: <region.internal_secret>`
- clapi 验证 `viper.GetString("middle.secret")` 是否匹配
- 每个 Region 可配置独立的 `internal_secret`，需确保 clapi 端配置一致

### 6.2 Middle → clapi 认证机制

middle 已有完整的认证设计，通过 **Region** 模型管理与 clapi 的连接认证:

**认证流程**:
1. 管理员在 middle 中创建 Region，配置 `internal_endpoint` (clapi 内网地址) 和 `internal_secret` (共享密钥)
2. 用户请求到达 middle，middle 验签 JWT (RS256)，从 claims 中获取 `region`
3. middle 查询 `regions` 表，获取对应 Region 的 `internal_endpoint` 和 `internal_secret`
4. middle 转发请求到 clapi，携带 `X-Forwarded-Secret: <internal_secret>` Header
5. middle 同时注入 `X-User-ID`, `X-Org-ID`, `X-User-Email` 等用户上下文 Header，移除原始 `Authorization`
6. clapi 验证 `X-Forwarded-Secret` 是否匹配 `viper.GetString("middle.secret")` 配置项

**Region 数据模型** (`middle/app/models/region.py`):

| 字段 | 说明 |
|------|------|
| `name` | 区域唯一标识 (如 `cn-north`)，创建后不可修改 |
| `internal_endpoint` | clapi 内网地址 (如 `https://clapi:8255` 或跨机 `https://10.0.0.5:8255`) |
| `internal_secret` | 每个 Region 独立的共享密钥，创建时生成并仅返回一次 |
| `is_available` | 是否对外可用，为 false 时拒绝转发到该 Region |

**Docker 部署时的配置要点**:
- 同机部署: Region 的 `internal_endpoint` 填 `https://clapi:8255` (Docker 服务名)
- 跨机部署: Region 的 `internal_endpoint` 填 `https://<控制面 INTERNAL_IP>:8255`
- `internal_secret` 在 middle 创建 Region 时自动生成或手动指定，需确保 clapi 端 `middle.secret` 配置项与之一致
- SSL: middle 使用 `httpx.AsyncClient(verify=False)` 已跳过自签名证书验证，无需额外配置 CA

### 6.3 Vue.js UI 打包进 Nginx

- UI 不再是独立服务，而是在构建时打包进 nginx 镜像
- 使用多阶段构建: Stage 1 编译 Vue.js，Stage 2 将 dist/ 复制到 nginx 镜像
- nginx 直接服务静态文件，性能更好，无需额外容器
- 更新 UI 需要重新构建 nginx 镜像: `docker compose build nginx`
- 开发时可在宿主机运行 `npm run dev`，生产部署使用打包后的镜像

### 6.4 Nginx 仅场景一

nginx 移入 `full` profile，仅场景一启动。好处:
- 只需维护一套 nginx 配置 (无需降级版)
- 场景二更轻量，减少不必要的组件
- 消除了"两套 ssl.conf"的维护负担

### 6.5 clbase 清理

clbase 已合并进 clapi，需要从以下位置清理:
- `docker-compose.yml`: 移除 clbase service
- `Dockerfile.web`: 移除 clbase target，改引用 `api/` 目录
- `ssl.conf`: 移除 `/ → clbase:5443` 代理
- nginx `depends_on`: 移除 clbase

### 6.6 Middle 数据库策略

共享 postgres 实例，middle 使用独立数据库 `cloudland_middle`，与控制面 `cloudland` 库隔离。

---

## 7. 验证计划

### 场景二验证 (仅控制面, 无 nginx)
```bash
# 1. 启动 (同机部署，clapi 仅 localhost 可达)
COMPOSE_PROFILES=dev docker compose up -d --build

# 2. 验证
docker compose ps                              # clapi, cloudland, consoleproxy, postgres running
                                               # nginx, middle, ui 均未启动

# 3. 从 Docker 内网访问 clapi
docker compose exec clapi curl -k https://localhost:8255/api/v1/status   # 容器内部可达
# 宿主机仅 localhost 可达 (默认映射到 127.0.0.1:8255)
curl -k https://localhost:8255/api/v1/status

# 4. 跨机部署验证 (clapi 映射到管理网)
# 设置 INTERNAL_IP=10.0.0.5 后重启
# 其他机器通过管理网访问: curl -k https://10.0.0.5:8255/api/v1/status
```

### 场景一验证 (Middle + 控制面, 无 UI)
```bash
# 1. 启动
COMPOSE_PROFILES=full,dev docker compose up -d --build

# 2. 验证 nginx + middle 链路
docker compose ps                              # nginx, middle, clapi, cloudland 等 running
curl -k https://localhost:443/api/v1/           # nginx → middle → clapi
docker compose logs middle                       # 无报错
```

### 场景一验证 (UI + Middle + 控制面)
```bash
# 1. 启动
COMPOSE_PROFILES=full,dev docker compose up -d --build

# 2. 验证 UI
curl -k https://localhost:443/                  # nginx 服务 Vue.js 静态文件

# 3. 验证 API
curl -k https://localhost:443/api/v1/           # nginx → middle → clapi

# 4. 更新 UI (需重新构建)
# 修改 web/src/App.vue 后:
docker compose build nginx
docker compose up -d nginx
```

---

## 8. 风险与注意事项

1. **Dockerfile.web 重写风险**: 需确认 `api/` 目录下的 Go 模块结构与文件路径 (templates, docs, conf/locale 等是否存在)
2. **Middle → clapi 认证配置**: 需确保 clapi 的 `middle.secret` 配置项与 middle 创建 Region 时的 `internal_secret` 一致
3. **clapi 端口映射策略**: 默认映射到 `127.0.0.1:8255` (仅本机)，跨机部署时需设置 `INTERNAL_IP` 为管理网 IP
4. **UI 更新流程**: UI 打包在 nginx 镜像中，更新前端代码需重新构建镜像，不支持热重载
5. **数据库初始化**: `cloudland_middle` 数据库需通过 `init-db.sql` 预创建
6. **镜像体积**: nginx 镜像包含 Vue.js 构建产物，体积会增加 (约 5-10MB)，但简化了部署架构

---

## 9. 执行顺序

```
Step 1 → Step 2 → Step 3 → Step 4 → Step 5 → Step 6~8 → Step 9
重写       UI       Middle   Compose   Nginx    配置/DB    文档
Dockerfile Dockerfile Dockerfile 整合    路由     更新     README
(clapi)   (可选)
```

建议集成测试顺序:
1. 先完成 Step 1，确保 clapi 单独能跑
2. 加入 Step 3 + Step 4 (middle)，验证 middle → clapi 链路
3. 最后加入 Step 2 (ui)，验证完整场景
