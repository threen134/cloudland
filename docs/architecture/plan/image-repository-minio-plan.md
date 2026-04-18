# S3 兼容镜像仓库方案（MinIO / 外部 S3）

## 一、背景

### 1.1 现状问题

当前镜像上传流程：Go API 通过 SCI 将 `create_image.sh` 派发到**随机一台** compute node 执行，镜像只落在该节点的 `/opt/cloudland/cache/image/` 目录下。

| 问题 | 影响 |
|------|------|
| 镜像无分发机制 | `sync_target` 被注释（`scripts/kvm/async_job/create_image.sh:29`），ansible inventory 已废弃，其他 compute node 拿不到镜像 |
| 启动 VM 直接失败 | `launch_vm.sh:116`、`reinstall_vm.sh:47`、`rescue_vm.sh:39` 检测到 `$image_cache/$img_name` 不存在时直接 exit |
| 镜像状态不可追踪 | 不知道哪个节点有哪些镜像，节点重装/磁盘清理后无感知 |
| 远程 Region 无法共享 | 多 Region 部署时各区域镜像完全隔离，无法复用 |

### 1.2 目标

- 引入 S3 兼容的对象存储作为集中式镜像仓库，所有镜像有单一存储源
- 支持两种部署模式：**内置 MinIO**（docker-compose 自动部署）或 **外部 S3**（AWS S3 / 阿里云 OSS / 腾讯云 COS 等）
- compute node 按需从对象存储拉取镜像到本地缓存，缓存命中则不拉取
- 最小改造面，不影响 WDS 存储路径（有 WDS 时继续走块存储）

---

## 二、架构总览

### 2.1 改造前

```
用户上传 URL
  └─ Go API (image.go)
       └─ SCI 派发 → 随机 compute node
            └─ create_image.sh: curl $url → /opt/cloudland/cache/image/
                 └─ 只在这一台节点，其他节点启 VM 时报 "Image not available"
```

### 2.2 改造后

```
用户上传 URL
  └─ Go API (clapi)
       └─ Go 代码直接执行（不经 SCI，不走 shell 脚本）：
            ├─ http.Get(url) → minio.PutObject()  // 流式直传，Docker 内网
            ├─ minio.GetObject() 读头部 → 检测 format/size  // 无需 qemu-img
            └─ 更新 DB: state=available, format, size

VM 启动（SCI + shell，新增 presigned URL 传参）
  └─ clapi 生成 presigned GET URL → SCI 传参
       └─ launch_vm.sh (compute node)
            ├─ 本地缓存命中 → 直接用（touch 更新 mtime）
            └─ 缓存未命中 → curl <presigned-url> → 本地缓存 → 继续启动
```

### 2.3 部署拓扑

```mermaid
graph TB
    subgraph ControlNode["控制节点 (Docker Compose)"]
        clapi["clapi<br/>REST API :8255<br/>minio-go SDK"]
        cland["cland<br/>SCI 主控"]
        minio["MinIO<br/>S3 API :9000<br/>Console :9001<br/>volume: minio_data<br/>bucket: images"]

        clapi -->|"minio-go<br/>PutObject / RemoveObject<br/>Docker 内网直连"| minio
    end

    subgraph ComputeNodes["同 Region Compute Nodes (内网)"]
        C1["Compute-01<br/>本地缓存: /opt/.../image/"]
        C2["Compute-02<br/>本地缓存: /opt/.../image/"]
        C3["Compute-03<br/>本地缓存: /opt/.../image/"]
    end

    ExtURL(("外部镜像 URL"))

    ExtURL -.->|"http.Get → PutObject<br/>流式直传 (clapi 内存流转)"| clapi

    C1 -->|"presigned GET URL<br/>curl (clapi 生成)"| minio
    C2 -->|"presigned GET URL<br/>curl (clapi 生成)"| minio
    C3 -->|"presigned GET URL<br/>curl (clapi 生成)"| minio

    cland -->|"SCI 指令<br/>launch_vm.sh"| C1
    cland -->|"SCI 指令<br/>launch_vm.sh"| C2
    cland -->|"SCI 指令<br/>launch_vm.sh"| C3

    style ControlNode fill:#e8f4fd,stroke:#1a73e8
    style ComputeNodes fill:#f0f9e8,stroke:#34a853
    style minio fill:#455a64,color:#fff,stroke:#455a64
    style clapi fill:#0d47a1,color:#fff,stroke:#0d47a1
    style ExtURL fill:#fff,stroke:#666,stroke-dasharray: 5 5
```

**数据流说明：**

```mermaid
sequenceDiagram
    participant User as 用户
    participant API as clapi (Go)
    participant Ext as 外部 URL
    participant MinIO as MinIO
    participant SCI as cland (SCI)
    participant Compute as Compute Node
    participant Cache as 本地缓存

    Note over User,Cache: ── 镜像上传流程（clapi 直接处理，不经 SCI） ──
    User->>API: POST /images {url: "https://..."}
    API->>API: 创建 DB 记录, state=downloading
    API->>Ext: http.Get(url)
    Ext-->>API: io.ReadCloser (镜像数据流)
    API->>MinIO: minio.PutObject(bucket, name, reader)
    Note over API,MinIO: 流式直传：Ext → clapi 内存 → MinIO<br/>零本地磁盘占用
    MinIO-->>API: 上传完成
    API->>MinIO: minio.GetObject() 读头部 32 字节
    Note over API,MinIO: qcow2 magic: QFI\xfb (offset 0-3)<br/>virtual size: BigEndian.Uint64 (offset 24-31)
    MinIO-->>API: format=qcow2, size=10G
    API->>API: 更新 DB: state=available, format, size

    Note over User,Cache: ── VM 启动流程（SCI + shell，新增 presigned URL 传参） ──
    User->>API: POST /instances {image_id: ...}
    API->>API: 生成 presigned GET URL (2h有效)
    API->>SCI: HyperExecute(inter=N, launch_vm.sh ..., url_b64)
    SCI->>Compute: 在目标 compute node 执行
    Compute->>Cache: 检查本地缓存
    alt 缓存命中
        Cache-->>Compute: 直接使用 (touch 更新 mtime)
    else 缓存未命中
        Compute->>MinIO: curl <presigned-url>
        MinIO-->>Cache: 下载到本地缓存
    end
    Compute->>Compute: qemu-img convert → 启动 VM
```

---

## 三、对象存储部署（双模式）

clapi 使用 `minio-go` SDK（S3 兼容），Go 代码层面不区分 MinIO 和外部 S3——只是配置不同。

### 3.1 两种部署模式对比

| | 模式 A：内置 MinIO | 模式 B：外部 S3 |
|---|---|---|
| 适用场景 | 私有化部署、离线环境、开发测试 | 云上部署、已有对象存储 |
| 部署方式 | docker-compose 自动拉起 | 用户自行准备，只提供连接信息 |
| COMPOSE_PROFILES | 加 `minio`（如 `full,dev,region,minio`） | **不加** `minio`，MinIO 容器不启动 |
| 存储位置 | 控制节点本地磁盘（minio_data 卷） | 云厂商托管（AWS S3 / 阿里云 OSS / 腾讯云 COS 等） |
| 运维 | 自行管理磁盘、备份 | 云厂商保障可用性和持久性 |
| Compute node 拉取 | presigned GET URL（内网 `http://INTERNAL_IP:9000`） | presigned GET URL（公网/VPC endpoint） |
| Bucket 初始化 | minio-init 容器自动创建（私有 bucket） | 用户在云控制台预先创建（私有 bucket） |

**通过 COMPOSE_PROFILES 控制是否启动 MinIO 容器：**

```bash
# 内置 MinIO（私有化部署）
COMPOSE_PROFILES=full,dev,region,minio

# 外部 S3（云上部署），MinIO 容器不启动
COMPOSE_PROFILES=full,dev,region
```

### 3.2 统一环境变量

在 `.env.example` 中新增：

```bash
# ---- 镜像仓库（S3 兼容对象存储）----
# 使用内置 MinIO: COMPOSE_PROFILES 加 minio，如 full,dev,region,minio
# 使用外部 S3:   COMPOSE_PROFILES 不加 minio，MinIO 容器不启动

# S3 连接信息（两种模式都用这组变量，只是值不同）
S3_ENDPOINT=images.cloudland.internal:9000  # 内置 MinIO: 用域名 | 外部 S3: s3.amazonaws.com
S3_ACCESS_KEY=cloudland            # 内置 MinIO: MINIO_ROOT_USER | 外部 S3: AWS Access Key
S3_SECRET_KEY=                     # ⚠️ 必填，至少 12 位。内置 MinIO: MINIO_ROOT_PASSWORD | 外部 S3: AWS Secret Key
S3_BUCKET=images                   # bucket 名称
S3_REGION=us-east-1                # 外部 S3 需要，MinIO 可忽略
S3_USE_SSL=false                   # 内置 MinIO: false | 外部 S3: true
S3_UPLOAD_TIMEOUT_MINUTES=120      # 镜像上传总超时，默认 120min；极大镜像（>40GB 或带宽 <100Mbps）上调

# ---- capture 上传（compute → clapi → MinIO 转发）----
CLAPI_INTERNAL_URL=http://clapi.cloudland.internal:8255/api/v1  # compute 访问 clapi 的内部 URL（含 /api/v1 前缀）
SCI_SHARED_SECRET=                  # ⚠️ 必填，至少 32 位随机串。用于 HMAC 签发 capture token

# 注意: 不需要 IMAGE_DOWNLOAD_ENDPOINT 环境变量
# compute node 下载镜像通过 clapi 生成的 presigned GET URL（SCI 命令参数传入）

# ---- 内置 MinIO 专用（COMPOSE_PROFILES 含 minio 时生效）----
MINIO_ROOT_USER=${S3_ACCESS_KEY}
MINIO_ROOT_PASSWORD=${S3_SECRET_KEY}    # ⚠️ MinIO 要求至少 8 位，建议 12+ 位强密码
```

clapi 容器 environment 新增：

```yaml
    environment:
      S3_ENDPOINT: ${S3_ENDPOINT:-}       # 为空则不启用 S3，回退到 legacy 本地模式
      S3_ACCESS_KEY: ${S3_ACCESS_KEY:-}
      S3_SECRET_KEY: ${S3_SECRET_KEY:-}
      S3_BUCKET: ${S3_BUCKET:-images}
      S3_REGION: ${S3_REGION:-}
      S3_USE_SSL: ${S3_USE_SSL:-false}
      S3_UPLOAD_TIMEOUT_MINUTES: ${S3_UPLOAD_TIMEOUT_MINUTES:-120}
```

> clapi 启动时检查 `S3_ENDPOINT` 是否非空来决定是否初始化 S3 客户端（见 6.2 `InitS3()`）。不需要额外的 `IMAGE_STORE_TYPE` 开关。

### 3.3 模式 A：内置 MinIO

#### Docker Compose 服务

在 `deploy/docker/docker-compose.yml` 中新增（`region` profile）：

```yaml
  # ---------- MinIO 镜像仓库（内置模式，需要 minio profile）----------
  minio:
    image: minio/minio:latest
    container_name: cloudland-minio
    restart: unless-stopped
    profiles:
      - minio
    environment:
      MINIO_ROOT_USER: ${MINIO_ROOT_USER:-cloudland}
      MINIO_ROOT_PASSWORD: ${MINIO_ROOT_PASSWORD:?MINIO_ROOT_PASSWORD is required}
    volumes:
      - minio_data:/data
    command: server /data --console-address ":9001"
    ports:
      - "${INTERNAL_IP:-127.0.0.1}:9000:9000"   # S3 API
      - "${INTERNAL_IP:-127.0.0.1}:9001:9001"   # Web Console
    networks:
      cloudland:
        ipv4_address: 172.28.0.95
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 10s
      timeout: 5s
      retries: 5

  minio-init:
    image: minio/mc:latest
    container_name: cloudland-minio-init
    profiles:
      - minio
    entrypoint: >
      /bin/sh -c "
      until mc alias set cland http://minio:9000 $${MINIO_ROOT_USER} $${MINIO_ROOT_PASSWORD}; do sleep 2; done;
      mc mb --ignore-existing cland/$${S3_BUCKET};
      mc anonymous set none cland/$${S3_BUCKET};
      echo 'Bucket initialized (private, access via presigned URL only).';
      echo 'TIP: configure lifecycle rule to abort incomplete multipart uploads after N days:';
      echo '     mc ilm rule add --expire-delete-marker cland/'\"$${S3_BUCKET}\"'   # 具体 flag 以 mc 版本为准';
      "
    environment:
      MINIO_ROOT_USER: ${MINIO_ROOT_USER:-cloudland}
      MINIO_ROOT_PASSWORD: ${MINIO_ROOT_PASSWORD:?MINIO_ROOT_PASSWORD is required}
      S3_BUCKET: ${S3_BUCKET:-images}
    networks:
      cloudland:
    depends_on:
      minio:
        condition: service_healthy
```

volumes 新增：

```yaml
  minio_data:
    name: cloudland-minio-data
```

clapi 服务新增 `depends_on`（仅内置模式需要，外部 S3 不依赖）：

内置 MinIO 模式下，clapi 需要等 minio 就绪再启动。外部 S3 模式下没有 minio 容器。

两种处理方式（二选一）：

```yaml
# 方式 A：COMPOSE_PROFILES 已保证 minio 不启动时 depends_on 自动忽略
#         （Docker Compose v2.20.2+ 支持 required: false）
  clapi:
    depends_on:
      minio:
        condition: service_healthy
        required: false

# 方式 B：兼容老版本 Docker Compose（< v2.20.2）
#         主 yml 不写 depends_on；内置 MinIO 模式建议用 override 文件补充
#         （否则 InitS3 的 BucketExists ping 会在 minio 启动前失败，
#          导致 clapi 进入 legacy 模式，需要手动重启 clapi 才能恢复）。
# --- docker-compose.override.yml (仅内置 MinIO 模式需要) ---
#  clapi:
#    depends_on:
#      minio:
#        condition: service_healthy
```

> 实施时先检查线上 docker-compose 版本：`docker compose version`。若已 ≥ v2.20.2 用方式 A 更简洁；否则走方式 B 的容错路径。

#### Bucket 策略

**私有 bucket、认证访问**（不设匿名下载，避免内网任意机器未授权下载镜像）：

- **上传/删除**：clapi 通过 minio-go SDK 认证（access key 仅在 clapi 环境变量中）
- **Compute node 下载**：clapi 在 SCI 派发 launch_vm.sh 时生成 presigned GET URL（有效期 2h），通过命令参数传给 shell 脚本，无需在 compute node 上配置凭据

#### Compute node 拉取

同 Region 内网，通过 presigned URL 访问（URL 由 clapi 生成并通过 SCI 参数传入）：

```
clapi 生成 presigned GET URL  →  SCI 传给 launch_vm.sh
Compute Node  →  curl <presigned-url>  →  MinIO (http://INTERNAL_IP:9000)
```

### 3.4 模式 B：外部 S3

#### 前置准备（用户手动）

1. 在云厂商控制台创建 bucket（如 `cloudland-images`，**私有 bucket**，不需要设置公共读）
2. 创建 IAM 用户 / Access Key，赋予该 bucket 的读写权限
3. 将 Access Key 配置到 `.env` 中（clapi 使用此凭据生成 presigned URL）

#### .env 配置示例

```bash
# COMPOSE_PROFILES 不含 minio，MinIO 容器不启动
S3_ENDPOINT=s3.amazonaws.com          # AWS
# S3_ENDPOINT=oss-cn-beijing.aliyuncs.com  # 阿里云 OSS
# S3_ENDPOINT=cos.ap-beijing.myqcloud.com  # 腾讯云 COS
S3_ACCESS_KEY=AKIAIOSFODNN7EXAMPLE
S3_SECRET_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
S3_BUCKET=cloudland-images
S3_REGION=us-east-1
S3_USE_SSL=true
```

#### Compute node 拉取

Bucket 为私有，compute node 通过 clapi 生成的 presigned GET URL 下载（与内置 MinIO 模式一致）：

```
clapi 生成 presigned GET URL  →  SCI 传给 launch_vm.sh
Compute Node  →  curl <presigned-url>  →  AWS S3
```

---

## 四、DNS 域名注册

endpoint 支持域名（不硬编码 IP），利用已有的 dnsmasq 内部 DNS 服务实现域名解析。

### 4.1 为什么用域名

- **IP 变更无感知**：控制节点 IP 变了只需更新 DNS 记录，不用改每台 compute node 的 `cloudrc.local`
- **内外统一**：内置 MinIO 用 `images.cloudland.internal`，外部 S3 用 `s3.amazonaws.com`，compute node 配置格式一致
- **已有基础设施**：dnsmasq 已经在跑，compute node 的 `/etc/resolv.conf` 已指向控制节点 DNS

### 4.2 内置 MinIO 的 DNS 注册

**推荐方案：clapi 启动时自动注册**（复用现有 `RegisterHostInDns` 函数）。

理由：
- `RegisterHostInDns` 是幂等的（已有条目会更新 IP），每次 clapi 启动都调一次没有副作用
- IP 变更后重启 clapi 即可自动更新 DNS，不需要重新跑部署脚本
- hyper 节点的 DNS 注册已经走这条路（`HyperAdmin.Deploy` 中调用），MinIO 域名复用同一机制，逻辑统一

```go
// clapi 启动时，如果 S3 endpoint 是内置 MinIO，注册 DNS
func registerMinIODns() {
    hostname := viper.GetString("minio.dns_hostname") // "images.cloudland.internal"
    ip := viper.GetString("management_vip")            // INTERNAL_IP
    if hostname != "" && ip != "" {
        RegisterHostInDns(hostname, ip)
    }
}
```

注册后 compute node 即可解析：

```
images.cloudland.internal  →  10.0.0.5  (INTERNAL_IP)
```

### 4.3 环境变量

`.env.example` 新增：

```bash
# MinIO 内部域名（注册到 dnsmasq，compute node 通过此域名访问 MinIO）
MINIO_HOSTNAME=images.cloudland.internal
```

clapi 生成 presigned URL 时，URL 中的 host 取决于 `S3_ENDPOINT` 的配置：

- **内置 MinIO**：`S3_ENDPOINT=images.cloudland.internal:9000` → presigned URL 为 `http://images.cloudland.internal:9000/images/...?X-Amz-Signature=...`
- **外部 S3**：`S3_ENDPOINT=s3.amazonaws.com` → presigned URL 为 `https://bucket.s3.amazonaws.com/...?X-Amz-Signature=...`

内置 MinIO 用域名而非 IP 的好处是：compute node 通过 dnsmasq 解析，控制节点 IP 变更时只需更新 DNS 记录。

### 4.4 compute node DNS 解析链路

compute node 部署时 `deploy-compute-node.sh` 已经把 `/etc/resolv.conf` 指向控制节点的 dnsmasq（`INTERNAL_IP:53`）。解析链路：

```
compute node curl images.cloudland.internal
  → /etc/resolv.conf → dnsmasq (INTERNAL_IP:53)
    → hyper-hosts 匹配 → 返回 INTERNAL_IP
      → curl http://INTERNAL_IP:9000/images/<name>
```

外部 S3 域名（如 `s3.amazonaws.com`）在 dnsmasq 中没有匹配条目，走上游 DNS（`DNS_UPSTREAM`）正常解析。

---

## 五、Compute Node 镜像拉取

每个 Region 的 compute node 从本 Region 的对象存储拉取镜像。

改造后 compute node **不需要** 配置 S3 endpoint 或凭据。拉取流程完全由 presigned URL 驱动：

1. clapi 在派发 `launch_vm.sh`（或 `reinstall_vm.sh`、`rescue_vm.sh`）时，为目标镜像生成 presigned GET URL（有效期 2h）
2. presigned URL 通过 SCI 命令参数（base64 编码）传给 shell 脚本
3. shell 脚本调用 `ensure_image_cached "$img_name" "$download_url"` 下载

**内置 MinIO 模式：** presigned URL 指向 `http://INTERNAL_IP:9000/images/...?X-Amz-Signature=...`

**外部 S3 模式：** presigned URL 指向 `https://bucket.s3.region.amazonaws.com/...?X-Amz-Signature=...`

对 compute node 完全透明，无需区分内外模式。

---

## 六、镜像上传流程改造：clapi Go 代码直接处理

### 6.1 核心思路

**上传流程完全脱离 SCI + shell 脚本体系**，由 clapi 的 Go 代码直接完成：

| | 改造前（SCI + shell） | 改造后（clapi Go 代码）✅ |
|---|---|---|
| 执行位置 | 随机 compute node | clapi 容器内 |
| 网络访问 | 需要 `inet_access`（router-0 netns） | Docker bridge NAT，天然能访问外网 |
| 访问 MinIO | N/A（落本地磁盘） | Docker 内网直连 `minio:9000` |
| 依赖 | curl, qemu-img, mc | minio-go SDK（编译进二进制） |
| 格式检测 | `qemu-img info` | Go 读 qcow2 头部字节（见 6.3） |
| 进度追踪 | shell 回调，粗粒度 | Go 代码实时更新 DB 状态 |
| 错误处理 | shell exit code | Go error handling |
| 凭据分发 | 需要在执行节点配 mc 凭据 | 凭据仅在 clapi 环境变量中 |

### 6.2 clapi 新增 S3 客户端（统一适配 MinIO / 外部 S3）

**文件**: 新增 `api/src/services/s3.go`

Go 依赖（minio-go 兼容所有 S3 API）：

```bash
cd api && go get github.com/minio/minio-go/v7
```

S3 客户端初始化：

```go
import "github.com/minio/minio-go/v7"
import "github.com/minio/minio-go/v7/pkg/credentials"

var s3Client *minio.Client
var s3Bucket string

func InitS3() {
    endpoint := viper.GetString("s3.endpoint") // 为空则不启用
    if endpoint == "" {
        logger.Info("S3_ENDPOINT not set, image S3 store disabled")
        return
    }

    accessKey := viper.GetString("s3.access_key")
    secretKey := viper.GetString("s3.secret_key")
    useSSL := viper.GetBool("s3.use_ssl")      // MinIO: false | S3: true
    region := viper.GetString("s3.region")      // MinIO: "" | S3: "us-east-1"
    s3Bucket = viper.GetString("s3.bucket")

    client, err := minio.New(endpoint, &minio.Options{
        Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
        Secure: useSSL,
        Region: region,
    })
    if err != nil {
        // 不用 Fatalf —— S3 初始化失败不应杀掉整个 clapi 进程
        // S3Enabled() 将返回 false，服务降级到 legacy 本地模式
        logger.Errorf("Failed to init S3 client: %v (falling back to legacy mode)", err)
        return
    }

    // minio.New 不发网络请求，只构造 client struct。
    // 要真正判定 endpoint/凭据/bucket 可用，必须发一次实际请求（BucketExists 足够轻）。
    // 不做这一步的话，MinIO 未启动 / 凭据错 / bucket 不存在都要等第一次 upload 才暴露，
    // "S3Enabled() → false 自动降级" 的承诺就落空了。
    pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    exists, err := client.BucketExists(pingCtx, s3Bucket)
    if err != nil {
        logger.Errorf("S3 bucket check failed (endpoint=%s bucket=%s): %v (falling back to legacy mode)", endpoint, s3Bucket, err)
        return
    }
    if !exists {
        logger.Errorf("S3 bucket %q does not exist on %s (falling back to legacy mode)", s3Bucket, endpoint)
        return
    }

    s3Client = client
    logger.Infof("S3 image store initialized: endpoint=%s bucket=%s ssl=%v", endpoint, s3Bucket, useSSL)
}

func S3Enabled() bool {
    return s3Client != nil
}

// GenerateDownloadURL 生成 presigned GET URL，供 compute node 下载镜像
func GenerateDownloadURL(ctx context.Context, objectName string) (string, error) {
    u, err := s3Client.PresignedGetObject(ctx, s3Bucket, objectName, 2*time.Hour, nil)
    if err != nil {
        return "", err
    }
    return u.String(), nil
}
```

在 `main.go` 或服务初始化时调用 `InitS3()`。**Go 代码中不区分 MinIO 和外部 S3**，统一使用 `s3Client`。

### 6.3 镜像格式检测（纯 Go，无需 qemu-img）

qcow2 文件头格式固定：

| Offset | 长度 | 内容 |
|--------|------|------|
| 0-3 | 4 bytes | Magic: `QFI\xfb` |
| 4-7 | 4 bytes | Version (uint32 BE) |
| 24-31 | 8 bytes | Virtual size (uint64 BE) |

```go
import "encoding/binary"

type ImageInfo struct {
    Format      string // "qcow2" 或 "raw"
    VirtualSize uint64
}

func detectImageFormat(ctx context.Context, bucket, objectName string) (*ImageInfo, error) {
    obj, err := s3Client.GetObject(ctx, bucket, objectName, minio.GetObjectOptions{})
    if err != nil {
        return nil, err
    }
    defer obj.Close()

    header := make([]byte, 32)
    if _, err := io.ReadFull(obj, header); err != nil {
        return nil, err
    }

    // qcow2 magic: 0x514649fb ("QFI\xfb")
    // qcow2 分支直接从头部读 virtual size，不需要 Stat，避免多一次 API 往返
    if binary.BigEndian.Uint32(header[0:4]) == 0x514649fb {
        virtualSize := binary.BigEndian.Uint64(header[24:32])
        return &ImageInfo{Format: "qcow2", VirtualSize: virtualSize}, nil
    }

    // 非 qcow2 → 视为 raw，虚拟大小 = 对象实际大小，此时才调 Stat
    stat, err := obj.Stat()
    if err != nil {
        return nil, err
    }
    return &ImageInfo{Format: "raw", VirtualSize: uint64(stat.Size)}, nil
}
```

### 6.4 Create 方法改造

**文件**: `api/src/services/image.go` — `ImageAdminService.Create()`

当前流程（第 124-146 行）：拼 shell 命令 → `HyperExecute()` → 等待 shell 回调。

改造后（非 WDS 路径）：

```go
func (a *ImageAdminService) Create(ctx context.Context, ...) (image *model.Image, err error) {
    // ... 创建 DB 记录（不变）...

    if wdsAddress == "" && S3Enabled() {
        // S3 路径（MinIO 或外部 S3）：clapi 直接处理，不经 SCI
        // 使用独立 context（不用请求 ctx，handler 返回后请求 ctx 会被取消）。
        // 超时防止 goroutine 永久卡住（网络断开、源站无响应等）。
        // 默认 2h，可通过 S3_UPLOAD_TIMEOUT_MINUTES 调整 —— 40GB 镜像在 100Mbps 下约 55 分钟，
        //   30 分钟默认值会误伤大镜像，2h 给出充裕余量，极限场景（Windows Server 40–80GB）再上调。
        // 只传 image.ID（不传 image 指针），避免 goroutine 与 handler 共享可变对象。
        timeoutMin := viper.GetInt("s3.upload_timeout_minutes")
        if timeoutMin <= 0 {
            timeoutMin = 120
        }
        uploadCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMin)*time.Minute)
        imageID := image.ID
        go func() {
            defer cancel()
            a.uploadImageToS3(uploadCtx, imageID, url, storageID)
        }()
        return
    }

    if wdsAddress != "" {
        // WDS 路径：仍走 SCI + create_image.sh（不变）
        control := "inter="
        command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_image.sh '%d' '%s' '%s' '%d'", ...)
        err = HyperExecute(ctx, control, command)
        return
    }

    // 无 S3 + 无 WDS：回退到原始行为（SCI + shell，仅本地存储）
    control := "inter="
    command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_image.sh '%d' '%s' '%s' '%d'", ...)
    err = HyperExecute(ctx, control, command)
    return
}
```

异步上传方法：

> **关于 DB 句柄**：goroutine 里所有 DB 调用必须**用 global `dbs.DB` 句柄**，而不是 `GetContextDB(uploadCtx)`。
> 原因：uploadCtx 是从 `context.Background()` 派生的，没有请求级事务；而现有的 `GetContextDB` 在没有事务绑定时会 fallback 到 global db，本身不会报错，但如果未来 `GetContextDB` 改成严格校验 tx，这段逻辑会悄悄变成 nil db。
> 所以 `getImageByID` / `updateImageState` / `updateImageComplete` 三个私有方法都要显式用 global db，避免将来依赖 ctx：
>
> ```go
> func (a *ImageAdminService) getImageByID(id int64) (*model.Image, error) {
>     img := &model.Image{}
>     return img, dbs.DB.Where("id = ?", id).Take(img).Error  // 不用 GetContextDB
> }
> ```

```go
func (a *ImageAdminService) uploadImageToS3(ctx context.Context, imageID int64, url string, storageID int64) {
    // 从 DB 重新加载 image（不共享 handler 中的指针，避免 race condition）
    // 注意：这里用全局 db，不用 GetContextDB(ctx) —— goroutine 的 ctx 是 Background 派生的
    image, err := a.getImageByID(imageID)
    if err != nil {
        logger.Errorf("S3 upload: failed to load image %d: %v", imageID, err)
        return
    }

    bucket := s3Bucket
    prefix := strings.Split(image.UUID, "-")[0]
    objectName := fmt.Sprintf("image-%d-%s", image.ID, prefix)

    // 更新状态为 downloading
    a.updateImageState(image.ID, "downloading")

    // 1. 流式传输：外部 URL → MinIO
    // HTTP client 不设整体 Timeout（否则覆盖 ctx）；依赖 uploadCtx 驱动取消，
    // 额外设置 TCP 建连/TLS 握手/响应头超时，防止源站挂起
    httpClient := &http.Client{
        Transport: &http.Transport{
            DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
            TLSHandshakeTimeout:   30 * time.Second,
            ResponseHeaderTimeout: 60 * time.Second,
        },
    }
    // 用 NewRequestWithContext 让 ctx 驱动整体取消；不设 Client.Timeout（否则会覆盖 ctx 的大镜像耗时预算）
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        logger.Errorf("S3 upload: bad url %s: %v", url, err)
        a.updateImageState(image.ID, "error")
        return
    }
    resp, err := httpClient.Do(req)
    if err != nil {
        logger.Errorf("S3 upload: failed to fetch %s: %v", url, err)
        a.updateImageState(image.ID, "error")
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        logger.Errorf("S3 upload: fetch %s returned HTTP %d", url, resp.StatusCode)
        a.updateImageState(image.ID, "error")
        return
    }

    // 始终传 size=-1：minio-go 据此自动 multipart。
    // 若传 resp.ContentLength 且源站给出 >5GB 的 Content-Length，minio-go 会走单次 PUT（对外 S3 会直接 413）。
    // PartSize 64MB：64MB × 10000 part = ~640GB 对象上限，默认 5MB 只能覆盖 ~50GB（不够 Windows Server）。
    // 与 6.6 capture 路径保持一致的 options，Create/Capture 两条路径共用同一 S3 写入特征。
    putOpts := minio.PutObjectOptions{
        ContentType: "application/octet-stream",
        PartSize:    64 * 1024 * 1024,
    }
    _, err = s3Client.PutObject(ctx, bucket, objectName, resp.Body, -1, putOpts)
    if err != nil {
        // 清理残缺对象（含未完成 multipart 的 part 由 bucket lifecycle 规则兜底）
        s3Client.RemoveObject(context.Background(), bucket, objectName, minio.RemoveObjectOptions{})
        logger.Errorf("S3 upload: PutObject failed for image %d: %v", image.ID, err)
        a.updateImageState(image.ID, "error")
        return
    }

    // 2. 检测格式和大小
    info, err := detectImageFormat(ctx, bucket, objectName)
    if err != nil || (info.Format != "qcow2" && info.Format != "raw") {
        logger.Errorf("S3 upload: format detection failed for image %d: %v", image.ID, err)
        a.updateImageState(image.ID, "error")
        return
    }

    // 3. 更新 DB（object name 不入库，由 s3ObjectName(image) 在需要时重算）
    a.updateImageComplete(image.ID, info.Format, info.VirtualSize)
    logger.Infof("S3 upload: image %d (%s) uploaded as %s", image.ID, info.Format, objectName)
}
```

### 6.5 Delete 方法改造

**文件**: `api/src/services/image.go` — Delete 相关方法

当前：通过 SCI 派发 `clear_image.sh` 到 compute node 删除本地文件。

改造后（非 WDS 路径）：

```go
// s3ObjectName 按 deterministic 规则由 image 构造，避免 DB 新增字段
func s3ObjectName(image *model.Image) string {
    prefix := strings.Split(image.UUID, "-")[0]
    return fmt.Sprintf("image-%d-%s", image.ID, prefix)
}

func (a *ImageAdminService) deleteImageFromS3(ctx context.Context, image *model.Image) error {
    if !S3Enabled() {
        return nil
    }
    return s3Client.RemoveObject(ctx, s3Bucket, s3ObjectName(image), minio.RemoveObjectOptions{})
}
```

WDS 路径仍走原 `clear_image.sh`。

### 6.6 capture_image.sh 特殊处理

从 VM 抓快照仍需在该 VM 的 hypervisor 上执行（`inter=<hyper>`），因为需要访问 VM 磁盘。capture 完成后需要把镜像传到 S3 对象存储。

**⚠️ 不能用 presigned PUT URL**：S3/MinIO 单次 PUT 上限是 5GB，而 capture 出的镜像（Windows Server、挂载数据盘、含用户数据的生产 VM）经常超过这个阈值。`curl -X PUT -T` 不做 multipart，直接失败。

**推荐方案：compute → clapi → MinIO 流式转发**

compute node capture 完成后，以 HTTP POST 流式上传到 clapi 的内部端点，clapi 作为代理再走 `minio.PutObject()` 转发到 S3。整个链路 clapi 不落盘，复用 6.4 的 minio-go 代码路径。

```
capture_image.sh (on compute)
  │  POST http://<clapi>:8255/internal/images/<id>/upload
  │  Header: X-Capture-Token: <HMAC(secret, image_id+expiry)>
  │  Body:   镜像字节流 (application/octet-stream)
  ▼
clapi (gin handler)
  │  c.Request.Body → io.Reader
  ▼
minio.PutObject(ctx, bucket, name, reader, -1, {PartSize: 64MB})
  │  size=-1 触发 minio-go 自动 multipart：
  │    - 单 part 64MB，最大 10000 part → 单对象上限 ~640GB
  │    - 流式 read-write，clapi 内存只保留一个 PartSize buffer
  ▼
MinIO / 外部 S3
```

**clapi 侧 handler**：

```go
// api/src/routes/internal.go
// POST /internal/images/:id/upload
// Header: X-Capture-Token: <hex(HMAC-SHA256(secret, image_id || expiry))>
// Query:  expiry=<unix_ts>
// Body:   application/octet-stream
func (h *ImageHandler) UploadCapture(c *gin.Context) {
    imageID, err := strconv.ParseInt(c.Param("id"), 10, 64)
    if err != nil {
        c.AbortWithStatus(http.StatusBadRequest)
        return
    }

    // 1. 校验 HMAC token 和时效
    token := c.GetHeader("X-Capture-Token")
    expiry, _ := strconv.ParseInt(c.Query("expiry"), 10, 64)
    if !verifyCaptureToken(token, imageID, expiry) {
        c.AbortWithStatus(http.StatusUnauthorized)
        return
    }

    // 2. 加载 image 记录
    image, err := imageAdmin.getImageByID(imageID)
    if err != nil {
        c.AbortWithStatus(http.StatusNotFound)
        return
    }

    // 3. 流式转发到 MinIO（size=-1 触发 multipart，支持 >5GB）
    ctx := c.Request.Context()
    _, err = s3Client.PutObject(ctx, s3Bucket, s3ObjectName(image),
        c.Request.Body, -1,
        minio.PutObjectOptions{
            ContentType: "application/octet-stream",
            PartSize:    64 * 1024 * 1024, // 64MB×10000 part = ~640GB 上限，5MB 默认只够 50GB
        })
    if err != nil {
        imageAdmin.updateImageState(imageID, "error")
        c.AbortWithStatus(http.StatusInternalServerError)
        return
    }

    // 4. 格式检测 + DB 更新
    info, err := detectImageFormat(ctx, s3Bucket, s3ObjectName(image))
    if err != nil {
        imageAdmin.updateImageState(imageID, "error")
        c.AbortWithStatus(http.StatusInternalServerError)
        return
    }
    imageAdmin.updateImageComplete(imageID, info.Format, info.VirtualSize)
    c.JSON(http.StatusOK, gin.H{"ok": true})
}

// HMAC token：secret 只在 clapi 环境变量里，compute 侧永远不暴露
func verifyCaptureToken(token string, imageID, expiry int64) bool {
    if time.Now().Unix() > expiry {
        return false
    }
    mac := hmac.New(sha256.New, []byte(viper.GetString("sci.shared_secret")))
    fmt.Fprintf(mac, "%d|%d", imageID, expiry)
    expected := hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(token), []byte(expected))
}
```

**路由注册**：现有 `/api/v1/internal/*` 都挂在 `authGroup`（JWT `Authorize()` 中间件）下，但 compute node 没有 JWT，所以 capture 上传端点必须**挂在 `authGroup` 之外**。在 `api/src/apis/routes.go` 的 `Register()` 里：

```go
// api/src/apis/routes.go — 紧接着 `v1 := r.Group(apiV1)` 之后，`authGroup := ...` 之前
// 内部 capture 上传：用 HMAC token 自证（X-Capture-Token），不走 JWT Authorize
v1.POST("/internal/images/:id/upload", imageAPI.UploadCapture)
```

> 注意三点：
> 1. 路径放在 `v1` 之下（完整路径 `/api/v1/internal/images/:id/upload`），与 `CLAPI_INTERNAL_URL` 配置保持一致（6.6 派发 token 时拼接的 URL）
> 2. **不要加到 `authGroup` 里**：compute 侧没有 JWT；鉴权完全由 handler 内部的 `verifyCaptureToken` 承担
> 3. handler 自己不引中间件 —— 要做 body size 限制或 rate limit，在 gin route 链上单独挂，别复用 authGroup 的公共 middleware（避免 ClientIP 检查、org header 强制等逻辑误伤内部调用）

**派发 capture 时 clapi 生成 token 并传给 shell**：

```go
// clapi 派发 capture_image.sh 时
expiry := time.Now().Add(2 * time.Hour).Unix()
mac := hmac.New(sha256.New, []byte(viper.GetString("sci.shared_secret")))
fmt.Fprintf(mac, "%d|%d", image.ID, expiry)
token := hex.EncodeToString(mac.Sum(nil))

clapiURL := viper.GetString("clapi.internal_url") // 如 http://clapi.cloudland.internal:8255/api/v1
uploadURL := fmt.Sprintf("%s/internal/images/%d/upload?expiry=%d", clapiURL, image.ID, expiry)

control := fmt.Sprintf("inter=%d", instance.Hyper)
command := fmt.Sprintf(
    "/opt/cloudland/scripts/backend/capture_image.sh '%d' '%s' '%d' '%s' '%d' '%s' '%s'",
    image.ID, prefix, instance.ID, bootVolumeUUID, storageID, uploadURL, token)
```

**compute 侧 capture_image.sh 末尾**：

```bash
# 参数末尾两个位置：$upload_url $capture_token （S3 未启用时两者都为空）
upload_url=${!#}
shift || true
capture_token=${!#}   # 这里按脚本实际位置参数数量调整；或改用 --url/--token 命名参数

if [ -n "$upload_url" ] && [ -n "$capture_token" ]; then
    # 流式上传：curl --data-binary @file，避免把文件读进内存
    # --max-time 7200：对应 clapi 侧 S3_UPLOAD_TIMEOUT_MINUTES 默认 120min
    curl -sS -f --max-time 7200 \
        -X POST \
        -H "X-Capture-Token: $capture_token" \
        -H "Content-Type: application/octet-stream" \
        --data-binary "@$image_cache/$image_name" \
        "$upload_url"
    rc=$?
    rm -f "$image_cache/$image_name"   # 上传完成后清理临时文件
    [ $rc -ne 0 ] && { echo "capture upload failed"; exit 1; }
fi
```

**为什么比 presigned PUT 和 ssh 拉流都好**：

| 维度 | presigned PUT | ssh 拉流 | **clapi 转发（本方案）** |
|------|--------------|---------|----------------------|
| 5GB 上限 | ❌ 限制 | ✅ | ✅ |
| compute 侧新依赖 | curl（已有） | 无新增 | curl（已有） |
| clapi 侧新依赖 | 无 | **ssh 客户端+known_hosts** | 无 |
| 凭据分发 | S3 presigned URL | ssh key | **短期 HMAC token** |
| 实现复杂度 | 最简单但功能不够 | 需管理 compute 信任链 | **复用已有 gin 路由** |
| 与 6.4 Create 路径 | 独立实现 | 独立实现 | **共用 `minio.PutObject(reader,-1)`** |

**适用于内置 MinIO 和外部 S3**。与 Create 流程对称（`http.Get(url).Body → PutObject` vs `gin Request.Body → PutObject`），Go 代码路径完全复用。

**compute 如何找到 clapi**：

- 内置 MinIO：已注册 `images.cloudland.internal`（4.2 节），同样机制注册 `clapi.cloudland.internal`，compute 通过 dnsmasq 解析
- 外部 S3：用 `INTERNAL_IP` 或单独注册 `clapi.cloudland.internal`；envvar `CLAPI_INTERNAL_URL` 覆盖
- 环境变量 `CLAPI_INTERNAL_URL` 在 `.env.example` 中新增

**注意事项**：

- **流量放大**：compute → clapi → MinIO 走两跳。内置 MinIO 时 clapi/MinIO 同宿主机 Docker bridge，loopback 级可忽略；外部 S3 时 clapi 替 compute 转了一跳互联网流量，capture 低频可接受
- **clapi 内存**：`PartSize × 并发上传数`，64MB × 10 并发 = 640MB 峰值，对 clapi 容器资源无压力
- **中断清理**：连接中断时 minio-go 已提交的 part 会留在 MinIO 的 `__incomplete/` 区域。依赖 bucket lifecycle rule 定期清理（mc flag 因版本而异，详见 3.3 节 minio-init 的 TIP 输出）
- **gin handler 超时**：默认无 http ReadTimeout 时单连接可无限持续；实施时在 clapi 容器的 `http.Server` 上显式设置 `ReadHeaderTimeout: 10s`（防慢客户端头部），不设 `ReadTimeout`（body 读取由 ctx 驱动）

### 6.7 Image 状态机

引入 S3 对象存储后状态机不变，但语义变化：

| 状态 | 含义（改造前） | 含义（改造后） |
|------|--------------|--------------|
| `downloading` | shell 正在下载到 compute node | clapi 正在从外部 URL 流式传入 MinIO |
| `available` | 在某个 compute node 上可用 | 在 MinIO 仓库中可用，compute node 按需拉取 |
| `error` | 下载或上传失败 | 流式传入失败或格式校验不通过 |

---

## 七、镜像启动/拉取流程改造

### 7.1 cloudrc 新增公共函数

**文件**: `scripts/cloudrc`

```bash
# 确保镜像在本地缓存中，如果不在则从 presigned URL 拉取
# 用法: ensure_image_cached <img_name> [presigned_download_url]
# 返回: 0=成功 1=失败
#
# 并发安全：
#   - 同节点上多个 VM 并发启动同一镜像时，会通过 flock 串行化，后来者等待第一个下载完成后直接命中缓存
#   - 下载到 .partial 临时文件，完成后原子 mv；即使中途 crash/kill 也不会留下"非空但截断"的坏文件
#   - 说明：S3/MinIO presigned URL 的签名只在请求 *发起* 时校验一次，下载过程中到期不会中断，
#     所以 --retry-max-time 600 的作用是限制"开始下载前"的重试窗口，不是下载过程中的超时
function ensure_image_cached()
{
    local img_name=$1
    local download_url=$2   # presigned GET URL，由 clapi 生成并通过 SCI 参数传入
    local target="$image_cache/$img_name"
    local partial="$target.partial"
    local lock="$image_cache/.$img_name.lock"

    mkdir -p $image_cache

    # 快速路径：已命中直接返回
    if [ -s "$target" ]; then
        return 0
    fi

    if [ -z "$download_url" ]; then
        return 1
    fi

    # 用 flock 串行化同节点同镜像的下载
    (
        flock -x 9
        # 进入临界区再次检查：可能已被前一个持锁者下载完成
        if [ -s "$target" ]; then
            exit 0
        fi
        # 被 SIGTERM/SIGINT kill 时也清理 .partial，避免下次命中坏文件
        trap 'rm -f "$partial"' EXIT INT TERM
        # 下载到临时文件，成功后原子 mv
        # 不用 -k：内置 MinIO 走 HTTP 无需 TLS，外部 S3 走 HTTPS 应验证证书
        # --retry 5 --retry-max-time 600：限制建连阶段的重试窗口
        curl -s -f --retry 5 --retry-max-time 600 -o "$partial" "$download_url"
        if [ $? -ne 0 ] || [ ! -s "$partial" ]; then
            exit 1
        fi
        mv -f "$partial" "$target"
        trap - EXIT INT TERM  # mv 成功后不再需要清理
    ) 9>"$lock"
    return $?
}
```

### 7.2 clapi 生成 presigned GET URL 并传入 SCI 命令

clapi 在派发 `launch_vm.sh` 时，如果 S3 已启用，为该镜像生成 presigned GET URL 并作为额外参数传给 shell 脚本：

```go
// clapi image launch 流程中
func (a *ImageAdminService) buildLaunchCommand(...) string {
    // ... 原有参数 ...
    if S3Enabled() && image.Status == "available" {
        downloadURL, err := s3Client.PresignedGetObject(
            ctx, s3Bucket, s3ObjectName(image), 2*time.Hour, nil)
        if err == nil {
            // presigned URL 作为 base64 编码参数传入，避免 shell 特殊字符转义问题
            urlB64 := base64.StdEncoding.EncodeToString([]byte(downloadURL.String()))
            command += fmt.Sprintf(" '%s'", urlB64)
        }
    }
    return command
}
```

shell 端解码：

```bash
# 约定：clapi 一律用 base64 编码 URL 后作为最后一个位置参数传入。
# S3 未启用时 clapi 传空串，shell 侧 -z 判空后走 legacy 行为。
# 不做"以 http 开头就当原始 URL"之类的启发式 —— base64 字母表中
# 小写字母可能恰好拼出 'http' 前缀，误判会导致 curl 拉一个 garbage URL。
image_download_url_b64=${!#}   # 最后一个位置参数
image_download_url=""
if [ -n "$image_download_url_b64" ]; then
    image_download_url=$(echo "$image_download_url_b64" | base64 -d 2>/dev/null || echo "")
fi
```

> 具体位置以各脚本的 `[ $# -lt X ]` 检查和当前参数列表为准，实施时统一追加到末尾，并更新脚本头部的 usage 注释（N+1 参数：`image_download_url_b64`）。

### 7.3 launch_vm.sh 改造

**文件**: `scripts/kvm/launch_vm.sh:116-120`

当前：

```bash
if [ ! -s "$image_cache/$img_name" ]; then
    echo "Image is not available!"
    echo "|:-COMMAND-:| create_volume_local '$vol_ID' 'volume-${vol_ID}.disk' '$vol_state' 'image $img_name not available!'"
    exit -1
fi
```

改为：

```bash
if ! ensure_image_cached "$img_name" "$image_download_url"; then
    echo "|:-COMMAND-:| create_volume_local '$vol_ID' 'volume-${vol_ID}.disk' '$vol_state' 'image $img_name not available!'"
    exit -1
fi
# 更新 mtime 标记为"最近使用"，供 GC 脚本 (find -mtime +30) 判断保留/清理
touch "$image_cache/$img_name"
```

### 7.4 reinstall_vm.sh 和 rescue_vm.sh 同步改造

**文件**: `scripts/kvm/reinstall_vm.sh:47`、`scripts/kvm/rescue_vm.sh:39`

同样替换为 `ensure_image_cached "$img_name" "$image_download_url"` 调用，**并在成功后执行 `touch "$image_cache/$img_name"`**。clapi 在派发这些脚本时同样生成 presigned GET URL。

> **必须加 touch**：8.2 的 GC 脚本按 mtime 判断是否清理。reinstall/rescue 都属于"实际在用该镜像"的路径，不更新 mtime 会被 GC 误判为冷数据清理，下次命中时又要重新从 S3 拉取。

---

## 八、镜像删除流程改造

### 8.1 clapi 直接删除 MinIO 对象

非 WDS 路径下，clapi Delete 方法直接调用 `s3Client.RemoveObject()`（见 6.5），不再通过 SCI 派发 `clear_image.sh`。

> **compute node 本地缓存不立即清理**：删除镜像后，散落在各 compute node 上的本地缓存副本仍会存在，由 8.2 的 GC 脚本按 mtime 超期统一清理（默认 30 天）。因为 object name 由 image.ID + UUID 前缀 deterministic 构造，删除并新建镜像不会撞车，残留副本不会污染新镜像，只是短期占用磁盘。

### 8.2 Compute Node 本地缓存清理

compute node 上的 `/opt/cloudland/cache/image/` 是缓存副本，需要定期清理以释放磁盘。

```bash
#!/bin/bash
# image_cache_gc.sh — 清理 N 天未使用的本地镜像缓存
# 注意: 用 mtime 而非 atime（很多文件系统挂载 noatime/relatime，atime 不可靠）
# ensure_image_cached 下载完成后自动带上当前 mtime；
# launch_vm.sh 使用镜像后执行 touch 更新 mtime 标记为"最近使用"。
source /opt/cloudland/scripts/cloudrc

MAX_AGE_DAYS=${IMAGE_CACHE_MAX_AGE:-30}

find $image_cache -type f -mtime +${MAX_AGE_DAYS} -delete 2>/dev/null
```

**Trade-off**：`IMAGE_CACHE_MAX_AGE` 越短，磁盘占用越低；但超期后的首次 launch 会重新从 S3 拉取整个镜像（几十 GB 可能耗时 1–30 分钟），用户感知为"冷启动慢"。默认 30 天保守，磁盘紧张的 compute 可调到 7–14 天。此值通过 `cloudrc.local` 覆盖。

部署为 crontab：

```cron
0 3 * * * /opt/cloudland/scripts/kvm/image_cache_gc.sh
```

---

## 九、Compute Node cloudrc.local 配置

改造后 compute node **不再需要** 配置 `image_repo_endpoint`——镜像下载 URL 由 clapi 以 presigned URL 形式生成，通过 SCI 命令参数直接传给 shell 脚本。

好处：
- compute node 不存储任何 S3 凭据或 endpoint 配置
- presigned URL 有时效（2h），过期自动失效
- 内置 MinIO / 外部 S3 对 compute node 完全透明

> 如果需要在 compute node 上做脱离 SCI 的手动操作（如调试），可在 `cloudrc.local` 中手动配置 endpoint，但正常流程不依赖此配置。

---

## 十、多 Region 部署

### 10.1 每个 Region 一个 MinIO

在多 Region 架构下，每个 Region 的控制节点（运行 `COMPOSE_PROFILES=region`）各自运行一个 MinIO 实例。镜像上传到该 Region 的 MinIO，该 Region 的 compute node 从本 Region MinIO 拉取。

```mermaid
graph TB
    subgraph Central["中央控制面 (full,dev)"]
        cpgateway["cpgateway"]
        nginx_c["nginx"]
    end

    subgraph RegionA["Region A 控制面 (region)"]
        clandA["cland-A"]
        clapiA["clapi-A"]
        minioA["MinIO-A"]
        clapiA --> minioA
    end

    subgraph RegionB["Region B 控制面 (region)"]
        clandB["cland-B"]
        clapiB["clapi-B"]
        minioB["MinIO-B"]
        clapiB --> minioB
    end

    CA1["Compute A1"] -->|拉取| minioA
    CA2["Compute A2"] -->|拉取| minioA
    CB1["Compute B1"] -->|拉取| minioB

    cpgateway -->|代理| clapiA
    cpgateway -->|代理| clapiB

    style Central fill:#f3e5f5,stroke:#7b1fa2
    style RegionA fill:#e8f4fd,stroke:#1a73e8
    style RegionB fill:#fef7e0,stroke:#f9ab00
    style minioA fill:#455a64,color:#fff
    style minioB fill:#455a64,color:#fff
```

### 10.2 镜像隔离

各 Region 的镜像完全独立，不跨 Region 共享。用户在哪个 Region 上传镜像，镜像就存在哪个 Region 的 MinIO 里，只能被该 Region 的 compute node 使用。

**⚠️ object name 冲突风险（仅外部 S3 场景）**

object name 由 `image-<ID>-<UUID前缀>` 构造，**image.ID 是各 Region DB 独立发的自增 ID**。不同 Region 建的镜像很可能拿到相同的 ID（如两地同时创建时 ID=5），一旦这些 Region 共用同一个 S3 bucket，就会互相覆盖对方的 object。

**隔离策略（必须二选一）**：

| 选项 | 做法 | 适用场景 |
|------|------|---------|
| A. 每 Region 独立 bucket（推荐） | `S3_BUCKET=cloudland-images-<region>`，如 `cloudland-images-cn-beijing`、`cloudland-images-us-east` | 外部 S3 常见 IAM 隔离粒度，审计/配额都按 bucket 分 |
| B. 共用 bucket + region 前缀 | object name 改为 `<region_uuid>/image-<ID>-<prefix>` | 非要共用（成本/管理单一）时用；S3 不是真正的目录，`/` 只是命名 |

内置 MinIO 模式下每个 Region 的控制节点各自跑一个 MinIO 实例，天然不会撞车，无需处理。

plan 默认推荐 A（每 Region 独立 bucket），`.env` 里 `S3_BUCKET` 配置时带 region 后缀即可。若选 B，需修改 `s3ObjectName()` 函数注入 region_uuid 前缀，并在 6.2 `InitS3()` 里读取 `REGION_UUID` 环境变量。

---

## 十一、WDS 场景兼容

引入 S3 对象存储不影响 WDS 路径。三种存储模式优先级：

| 优先级 | 条件 | 上传路径 | 启动路径 |
|--------|------|---------|---------|
| 1 | 有 WDS（`$wds_address` 非空） | SCI + `create_image.sh` → WDS 块存储 | WDS snapshot clone |
| 2 | 无 WDS + 有 S3（`S3Enabled()`） | clapi Go 代码 → S3（MinIO 或外部） | `ensure_image_cached` → curl S3 |
| 3 | 无 WDS + 无 S3 | SCI + `create_image.sh` → compute 本地 | 仅本地缓存（原始行为） |

**WDS 路径下 S3 不介入**：即便同时配置了 `S3_ENDPOINT` 和 WDS，WDS 场景走原有的 `create_image.sh` + `inet_access curl` + `qemu-img convert` + WDS upload 路径，不经 clapi Go 直传。理由：WDS 需要 raw 格式 + uss_gateway 同步，逻辑耦合太深，改造收益小；保持现有 shell 路径可靠性更高。

---

## 十二、安全考虑

| 层面 | 内置 MinIO | 外部 S3 |
|------|----------|---------|
| 端口暴露 | 仅绑定 `INTERNAL_IP`，不暴露到公网 | 云厂商管理，通过 IAM 权限控制 |
| 上传/删除 | minio-go SDK 认证，凭据仅在 clapi 环境变量中 | 同左（S3 access key） |
| Bucket 策略 | **私有 bucket**，compute node 通过 presigned URL 下载 | 同左（presigned GET URL） |
| 默认密码 | **无默认密码**，`MINIO_ROOT_PASSWORD` 必填（`docker-compose` 启动时校验） | 用户自行管理云厂商 IAM 凭据 |
| 网络隔离 | docker bridge 内网，compute 内网直连 | VPC endpoint 或公网 HTTPS |
| capture 上传 | compute → clapi → MinIO 流式转发（HMAC token 鉴权，>5GB 走 multipart） | 同左（minio-go 兼容 S3 标准 multipart） |
| TLS | 内网 HTTP（无 `-k` 跳过验证），外部 S3 使用 HTTPS 并验证证书 | HTTPS + 证书验证 |

---

## 十三、实施步骤

| 步骤 | 内容 | 涉及文件 |
|------|------|----------|
| 1 | docker-compose 新增 minio + minio-init 服务（`minio` profile） | `deploy/docker/docker-compose.yml` |
| 2 | `.env.example` 新增 S3 配置项 + COMPOSE_PROFILES 说明 + MINIO_HOSTNAME | `deploy/docker/.env.example` |
| 3 | ~~在 deploy-control-node.sh 注册 DNS~~ **不需要**：4.2 已决定由 clapi 启动时注册（幂等、IP 变更无需重跑部署），部署脚本保持原样 | — |
| 4 | clapi 启动时调用 `RegisterHostInDns(MINIO_HOSTNAME, INTERNAL_IP)` 和 `RegisterHostInDns(CLAPI_HOSTNAME, INTERNAL_IP)`（capture 转发用） | `api/src/services/s3.go` |
| 5 | clapi 引入 minio-go 依赖，新增 S3 客户端初始化 | `api/go.mod`, `api/src/services/s3.go` |
| 6 | clapi 实现 `detectImageFormat()` 纯 Go 格式检测 | `api/src/services/s3.go` |
| 7 | ~~新增 `s3_object_name` 字段~~ **不需要新字段**：object name 由 `image.ID` + `UUID` 前缀 deterministic 构造（格式 `image-<ID>-<prefix>`），delete / presigned URL 生成都在 Go 代码里按规则重算。保持 Image model 原样 | `api/src/services/image.go` |
| 8 | clapi 改造 `Create` 方法：非 WDS 路径走 Go 直传 + 生成 presigned URL | `api/src/services/image.go` |
| 9 | clapi 改造 `Delete` 方法：非 WDS 路径走 Go 直删 | `api/src/services/image.go` |
| 10 | clapi 改造 launch/reinstall/rescue 派发：生成 presigned GET URL 传给 shell | `api/src/services/image.go` |
| 11 | `cloudrc` 新增 `ensure_image_cached` 函数（接收 presigned URL 参数） | `scripts/cloudrc` |
| 12 | 改造 `launch_vm.sh` 缓存 miss 时用 presigned URL 拉取 | `scripts/kvm/launch_vm.sh` |
| 13 | 同步改造 `reinstall_vm.sh`、`rescue_vm.sh` | `scripts/kvm/reinstall_vm.sh`、`rescue_vm.sh` |
| 14 | 添加本地缓存 GC cron 脚本（用 mtime 替代 atime） | `scripts/kvm/image_cache_gc.sh` |
| 15 | `deploy-compute-node.sh` 部署配置 | `deploy/deploy-compute-node.sh` |
| 16 | 新增 capture 上传 handler（`UploadCapture`）+ `verifyCaptureToken` HMAC 校验 | `api/src/apis/image.go`（或新增 `api/src/apis/capture.go`） |
| 16a | **在 `apis/routes.go` 的 `Register()` 把 `POST /internal/images/:id/upload` 挂到 `v1`（**不进 `authGroup`**，鉴权由 handler 内 HMAC 完成）** | `api/src/apis/routes.go` |
| 16b | 改造 `capture_image.sh`：capture 完成后 `curl --data-binary @file` 流式上传到 clapi 内部端点；末尾参数接收 `upload_url`、`capture_token` | `scripts/kvm/async_job/capture_image.sh` |
| 16c | clapi `ImageAdminService.Create` capture 分支生成 HMAC token + uploadURL 并通过 SCI 参数下发 | `api/src/services/image.go` |
| 17 | 已有镜像迁移：从 compute node 回收到 S3 | 手动 / 一次性脚本 |

---

## 十四、待确认的决策点

| 编号 | 问题 | 选项 | 决定 |
|------|------|------|------|
| D1 | 镜像上传在哪执行？ | A) SCI + shell B) clapi Go 代码 | **B：clapi Go 代码**，零外部依赖，Docker 内网直连 MinIO |
| D2 | 格式检测方式 | A) qemu-img info B) Go 读 qcow2 头部 | **B：Go 读头部**，无需安装 qemu-img |
| D3 | capture_image 上传方式 | A) compute curl PUT + presigned URL B) compute mc cp C) ssh 拉流 D) compute POST → clapi 内部端点 → multipart PutObject | **D：compute → clapi → MinIO 流式转发**。避开 5GB 上限；compute 只用 curl，clapi 不装 ssh；HMAC token 短期鉴权；复用 6.4 的 minio-go 路径 |
| D4 | Compute node 本地缓存 GC 策略 | A) 按天数（mtime）B) 按容量上限 C) 手动 | A：用 mtime（非 atime），launch_vm.sh 使用后 touch 更新 mtime |
| D5 | Bucket 访问模式 | A) 匿名可读 B) presigned URL | **B：presigned URL**，bucket 私有，clapi 生成短期有效的下载/上传 URL |

---

## 十五、注意事项

- 内置 MinIO 模式：`minio_data` 数据卷需要足够磁盘空间，建议控制节点挂载独立数据盘
- 外部 S3 模式：无本地存储压力，但需注意云厂商的流量费用（尤其是跨 AZ/Region 出流量）
- compute node 拉取镜像走管理网（内置 MinIO）或 VPC endpoint（外部 S3），不经 `inet_access`（router-0 netns）
- 已有镜像（散落在各 compute node 上的）需要手动迁移到 MinIO，或者在 `ensure_image_cached` 中兼容旧文件（本地有就用本地的）
- WDS 路径完全不受影响，MinIO 只服务于无 WDS 的部署场景
- `capture_image.sh`（从 VM 快照捕获镜像）仍需在该 VM 的 hypervisor 上执行，capture 完成后 compute 通过 `POST /internal/images/<id>/upload` 把镜像流式发给 clapi，clapi 用 minio-go multipart 转发到 S3（避开 presigned PUT 的 5GB 上限）。凭据用 HMAC token，只在 clapi 环境变量里保存 secret
- clapi 容器已在 Docker bridge 网络中（172.28.0.30），天然能通过 Docker NAT 访问外部 URL，也能通过内网直连 MinIO（172.28.0.95），无网络命名空间问题
- 外部 URL 的 `resp.Body`（`io.ReadCloser`）直接传给 `minio.PutObject()` 的 reader 参数，数据流经 clapi 内存但不落磁盘。统一传 `size=-1` + `PartSize=64MB` 触发 multipart，单对象上限 ~640GB；若改传真实 ContentLength 会退化为单次 PUT，>5GB 直接失败
- 上传中途失败（网络断、源 URL 不可达）时，需显式 `RemoveObject` 清理 MinIO 中的残缺对象；MinIO 也可配置 lifecycle rule 自动清理 incomplete multipart upload
- `Image` model 不需新增字段：S3 object name 采用 `image-<ID>-<UUID前缀>` 这一 deterministic 规则，由 Go 代码在需要时重算，delete/capture/presigned URL 都复用此函数

---

## 十六、运维：监控、备份与回滚

### 16.1 监控

| 监控项 | 方式 | 告警条件 |
|--------|------|----------|
| MinIO 服务健康 | Docker healthcheck（`mc ready local`）+ Prometheus `minio_health_status` | 连续 3 次 unhealthy |
| 磁盘使用率 | Prometheus `minio_disk_storage_used_bytes / total` | > 80% |
| 上传失败率 | clapi 日志中 `S3 upload:.*failed` 计数（Loki） | 5min 内 > 3 次 |
| 下载延迟 | compute node `curl` 耗时（可加 `--write-out` 打点） | P95 > 60s |
| presigned URL 过期 | launch_vm.sh 中 `curl` 返回 403 | 任意次数告警（说明 URL 有效期不足） |

内置 MinIO 可开启 Prometheus metrics：

```yaml
  minio:
    environment:
      MINIO_PROMETHEUS_AUTH_TYPE: public
    # Prometheus scrape: http://INTERNAL_IP:9000/minio/v2/metrics/cluster
```

### 16.2 备份（内置 MinIO）

| 策略 | 方式 |
|------|------|
| 数据卷备份 | 定期 `docker run --rm -v minio_data:/data -v /backup:/backup alpine tar czf /backup/minio-$(date +%F).tar.gz /data` |
| 跨站镜像 | `mc mirror cland/images remote/images`（需第二个 MinIO 或 S3） |
| 外部 S3 | 云厂商自带版本管理和跨区域复制，无需额外备份 |

### 16.3 回滚方案

如果 S3 镜像仓库上线后出现严重问题，可以快速回滚到 legacy 模式：

1. **clapi 侧**：清空 `S3_ENDPOINT` 环境变量 → 重启 clapi → `S3Enabled()` 返回 false → 自动回退到 SCI + shell 路径
2. **compute node 侧**：`ensure_image_cached` 收到空的 presigned URL 时返回失败，回退到"本地有就用本地"的原始行为
3. **数据不丢失**：已上传到 S3 的镜像仍然在 MinIO/S3 中，可在修复后重新启用
4. **COMPOSE_PROFILES**：移除 `minio` → MinIO 容器不再启动，但 `minio_data` 卷保留
