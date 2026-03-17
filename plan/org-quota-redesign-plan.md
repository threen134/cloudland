# Org-Level Quota System Redesign Plan

## 背景

当前 quota 系统是 per-user 的，每个用户都有自己的 `ResourceQuota` 和 `ResourceConsumption`。
这不合理：云资源属于组织，计费按组织，应该改为 **per-org-per-region** 的 quota。

同时，当前系统存在两个缺陷：
1. **ProxyService 不检查配额** — 请求直接转发到 Cloudland，没有任何资源限制
2. **ResourceConsumption 不自动更新** — 创建后始终为 0，从不追踪实际使用量

---

## 核心设计：per-org-per-region Quota

每个 org 在每个 region 有独立的配额和消费记录：

```
Organization ──1:N── OrgResourceQuota (每个 region 一条)
Organization ──1:N── OrgResourceConsumption (每个 region 一条)

唯一约束: (org_id, region_id)
```

**理由**：
- 不同 region 的资源池独立，不能混用
- JWT 中已包含 region 信息，proxy 转发时自然知道目标 region
- 管理员可以按 region 灵活分配配额（如：org A 在 region-east 给 100 核，region-west 给 50 核）
- 消费也按 region 分开统计，避免跨 region 误算

---

## Phase 1: 数据模型改造（per-user → per-org-per-region）

### 1.1 新建 OrgResourceQuota 模型

**文件**: `cpgateway/app/models/org_resource_quota.py`

```python
class OrgResourceQuota(Base):
    __tablename__ = "org_resource_quotas"

    id = Column(BigInteger, primary_key=True)
    org_id = Column(BigInteger, ForeignKey("organizations.id"), nullable=False)
    region_id = Column(BigInteger, ForeignKey("regions.id"), nullable=False)
    max_cpu_cores = Column(Float, nullable=False, default=0.0)
    max_ram_gb = Column(Float, nullable=False, default=0.0)
    max_public_ips = Column(Integer, nullable=False, default=0)
    max_disk_gb = Column(Float, nullable=False, default=0.0)
    created_at = Column(DateTime, default=func.now())
    updated_at = Column(DateTime, default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("org_id", "region_id", name="uq_org_region_quota"),
    )

    org = relationship("Organization", back_populates="resource_quotas")
    region = relationship("Region")
```

### 1.2 新建 OrgResourceConsumption 模型

**文件**: `cpgateway/app/models/org_resource_consumption.py`

```python
class OrgResourceConsumption(Base):
    __tablename__ = "org_resource_consumptions"

    id = Column(BigInteger, primary_key=True)
    org_id = Column(BigInteger, ForeignKey("organizations.id"), nullable=False)
    region_id = Column(BigInteger, ForeignKey("regions.id"), nullable=False)
    cpu_cores = Column(Float, nullable=False, default=0.0)
    ram_gb = Column(Float, nullable=False, default=0.0)
    public_ips = Column(Integer, nullable=False, default=0)
    disk_gb = Column(Float, nullable=False, default=0.0)
    created_at = Column(DateTime, default=func.now())
    updated_at = Column(DateTime, default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("org_id", "region_id", name="uq_org_region_consumption"),
    )

    org = relationship("Organization", back_populates="resource_consumptions")
    region = relationship("Region")
```

### 1.3 更新 Organization 模型

**文件**: `cpgateway/app/models/org.py`

添加 relationships（注意是 1:N，不是 1:1）：
```python
resource_quotas = relationship("OrgResourceQuota", back_populates="org", cascade="all, delete-orphan")
resource_consumptions = relationship("OrgResourceConsumption", back_populates="org", cascade="all, delete-orphan")
```

### 1.4 删除旧的 per-user quota 代码

产品未上线，不需要兼容，直接删除：

- **删除模型文件**：`resource_quota.py`、`resource_consumption.py`
- **删除 User 模型中的旧 relationships**：`resource_consumption`、`resource_quota`
- **删除旧 schemas**：`resource.py` 中所有 per-user 的 `ResourceQuota`、`ResourceConsumption`、`UserResourceInfo` 等
- **删除旧 API**：`resources.py` 中所有 per-user 端点
- **删除 `activate_user()` 中的旧初始化逻辑**

### 1.5 数据库迁移

- 创建 `org_resource_quotas` 表（含 `org_id` + `region_id` 联合唯一约束）
- 创建 `org_resource_consumptions` 表（含 `org_id` + `region_id` 联合唯一约束）
- 删除旧的 `resource_quotas` 和 `resource_consumptions` 表

### 1.6 更新 `__init__.py`

在 models 的 `__init__.py` 中注册新模型，删除旧模型引用。

---

## Phase 2: Schema 和 API 改造

### 2.1 更新 Pydantic Schemas

**文件**: `cpgateway/app/schemas/resource.py`

```python
# 新增 org-region 级别 schemas
# 注意：拆分为"独立返回"和"嵌套用"两种 schema，避免 region_name/org_uuid 重复出现

# --- 嵌套用（不含 org_uuid/region_name，由外层提供） ---

class QuotaFields(BaseModel):
    """配额字段（嵌套用）"""
    max_cpu_cores: float
    max_ram_gb: float
    max_public_ips: int
    max_disk_gb: float

class ConsumptionFields(BaseModel):
    """消费字段（嵌套用）"""
    cpu_cores: float
    ram_gb: float
    public_ips: int
    disk_gb: float

class OrgResourceQuotaUpdate(BaseModel):
    max_cpu_cores: Optional[float] = None
    max_ram_gb: Optional[float] = None
    max_public_ips: Optional[int] = None
    max_disk_gb: Optional[float] = None

# --- 独立返回用（含完整标识信息） ---

class OrgResourceQuota(QuotaFields):
    """单独返回配额时使用"""
    org_uuid: str
    region_name: str
    created_at: datetime
    updated_at: datetime

class OrgResourceConsumption(ConsumptionFields):
    """单独返回消费时使用"""
    org_uuid: str
    region_name: str

# --- 组合 schema ---

class OrgResourceInfo(BaseModel):
    """单个 region 的配额和消费（嵌套用 Fields，避免重复字段）"""
    region_name: str
    consumption: ConsumptionFields
    quota: QuotaFields

class OrgResourceSummary(BaseModel):
    """所有 region 的汇总"""
    org_uuid: str
    regions: List[OrgResourceInfo]
```

### 2.2 更新 API Endpoints

**文件**: `cpgateway/app/api/endpoints/resources.py`

新端点设计：

| 端点 | 说明 |
|------|------|
| `GET /resources/quota/{org_uuid}` | 获取 org 所有 region 的配额 |
| `GET /resources/quota/{org_uuid}/{region_name}` | 获取 org 在特定 region 的配额 |
| `PUT /resources/quota/{org_uuid}/{region_name}` | 设置 org 在特定 region 的配额 |
| `GET /resources/consumption/{org_uuid}` | 获取 org 所有 region 的消费 |
| `GET /resources/consumption/{org_uuid}/{region_name}` | 获取 org 在特定 region 的消费 |
| `GET /resources/info/{org_uuid}` | 获取 org 所有 region 的配额+消费汇总 |
| `GET /resources/info/{org_uuid}/{region_name}` | 获取 org 在特定 region 的配额+消费 |

**权限控制**：
- 查看配额/消费：org 成员（任意角色）
- 修改配额：superuser only

---

## Phase 3: Quota 初始化改造

### 3.1 修改 Auth Service

**文件**: `cpgateway/app/services/auth_service.py`

**当前**：在 `activate_user()` 中为 user 创建 quota/consumption

**改为**：在**创建 org 时**，为每个已有 region 初始化 quota/consumption

```python
# 在 org 创建后，为每个 region 初始化
# 默认配额从配置文件读取（config.py 中的 DEFAULT_* 值）
regions = await db.execute(select(Region))
for region in regions.scalars().all():
    org_quota = OrgResourceQuota(
        org_id=org.id,
        region_id=region.id,
        max_cpu_cores=settings.DEFAULT_CPU_CORES,
        max_ram_gb=settings.DEFAULT_RAM_GB,
        max_public_ips=settings.DEFAULT_PUBLIC_IPS,
        max_disk_gb=settings.DEFAULT_DISK_GB,
    )
    org_consumption = OrgResourceConsumption(
        org_id=org.id,
        region_id=region.id,
    )
    db.add(org_quota)
    db.add(org_consumption)
```

`config.py` 中已有默认值：
```python
DEFAULT_CPU_CORES: float = 4.0
DEFAULT_RAM_GB: float = 8.0
DEFAULT_PUBLIC_IPS: int = 2
DEFAULT_DISK_GB: float = 50.0
```
可通过环境变量覆盖（Pydantic Settings 机制）。

### 3.2 新增 Region 时自动初始化

当管理员添加新 region 时，需要为所有现有 org 创建该 region 的 quota/consumption 记录。
可在 region 创建的 API 中加入这个逻辑。

**注意**：`activate_user()` 中的旧 per-user quota 创建逻辑已在 Phase 1.4 中删除。

---

## Phase 4: ProxyService Quota 检查

### 4.1 新建 QuotaService

**文件**: `cpgateway/app/services/quota_service.py`

```python
class QuotaService:
    """Org-Region 级别配额检查与消费追踪，proxy 层统一处理 CREATE 和 DELETE"""

    async def check_and_reserve(
        self, db: AsyncSession, org_id: int, region_id: int, amount: dict
    ) -> None:
        """
        原子操作：检查配额 + 预扣资源，在同一个事务中完成。
        使用 SELECT FOR UPDATE 锁定 consumption 行，防止并发超卖。
        amount: {"cpu_cores": 2, "ram_gb": 4, "disk_gb": 50} 等
        配额不足抛出 QuotaExceededError。

        流程：
        1. SELECT FOR UPDATE consumption 行（加行锁）
        2. 对比 consumption + amount 是否超过 quota
        3. 超过则抛异常（自动释放锁）
        4. 不超过则立即更新 consumption（预扣）
        5. commit（释放锁）

        如果后续 Cloudland 请求失败，调用 release() 回滚预扣。
        """

    async def release(
        self, db: AsyncSession, org_id: int, region_id: int, amount: dict
    ) -> None:
        """减少 org 在指定 region 的资源消费（删除资源成功时 / 创建失败回滚时调用）"""

    async def query_resource_amount(
        self, region: Region, proxy_path: str, resource_id: str, headers: dict
    ) -> dict:
        """
        查询资源的规格（DELETE 前 / RESIZE 前获取当前资源量）。
        向 Cloudland 后端发送 GET 请求获取资源详情：
        - GET /instances/{id} → 返回 cpu, memory, disk
        - GET /volumes/{id} → 返回 size
        - GET /floating_ips/{id} → 固定返回 {"public_ips": 1}
        返回 amount dict，格式同 check_and_reserve 的 amount 参数。
        """

    async def query_flavor_amount(
        self, region: Region, flavor_id: int, headers: dict
    ) -> dict:
        """
        查询 flavor 规格（RESIZE / CREATE 时获取目标 flavor 的资源量）。
        向 Cloudland 后端发送 GET /flavors/{flavor_id}。
        返回 {"cpu_cores": X, "ram_gb": Y, "disk_gb": Z}。
        """

    async def get_quota_info(
        self, db: AsyncSession, org_id: int, region_id: Optional[int] = None
    ) -> Union[OrgResourceInfo, OrgResourceSummary]:
        """获取 org 的配额和消费信息，可按 region 过滤"""
```

### 4.2 请求分类 — 哪些请求需要检查配额

需要定义一个映射，标明哪些 API 路径 + HTTP 方法会影响配额：

```python
# CREATE 和 DELETE 都在 proxy 层处理，不需要定期同步
QUOTA_RULES = {
    # (method, path_pattern): action
    ("POST", "/instances"): "consume",        # 创建 VM → 预扣 cpu, ram, disk
    ("POST", "/volumes"): "consume",          # 创建卷 → 预扣 disk
    ("POST", "/floating_ips"): "consume",     # 申请浮动 IP → 预扣 public_ips
    ("DELETE", "/instances/{id}"): "release",  # 删除 VM → 先查规格再释放
    ("DELETE", "/volumes/{id}"): "release",    # 删除卷 → 先查 size 再释放
    ("DELETE", "/floating_ips/{id}"): "release", # 删除浮动 IP → 释放 1 个 public_ip
    ("PUT", "/instances/{id}"): "resize",         # resize VM → 按差值预扣或释放
    ("PUT", "/volumes/{id}"): "resize",           # resize Volume → 按 size 差值预扣或释放
}
```

### 4.3 修改 ProxyService.forward_to_region()

**文件**: `cpgateway/app/services/proxy_service.py`

Proxy 层统一处理 CREATE（预扣）和 DELETE（先查后释放），不依赖定期同步。

> **注意**：当前 `forward_to_region()` 的实际签名是 `(request, db, proxy_path)`，
> region 从 JWT claims 中获取，不是从参数传入。compute/network 端点调用时传入的
> `region_name` 参数实际未被使用。改造时需统一签名。

```python
async def forward_to_region(self, request, db, proxy_path):
    # 1. 已有逻辑：JWT 验证、region 解析、header 构建
    # ... org_internal_id 和 region 已经可用

    # 2. [新增] superuser 和 system org 跳过配额检查
    if claims.get("sr") == 1 or org.org_type == OrgType.SYSTEM:
        return await self._forward_request(...)

    # 3. [新增] 判断是否需要配额操作
    quota_action = self._match_quota_rule(request.method, proxy_path)

    reserved = False
    resource_amount = None

    if quota_action == "consume":
        # === CREATE 流程：先预扣再转发 ===
        # 3a. 从请求体中提取资源需求量
        #     - POST /instances: 提取 flavor_id，调用 query_flavor_amount() 查 Cloudland 获取 cpu/ram/disk
        #     - POST /volumes: 直接从请求体取 size → {"disk_gb": size}
        #     - POST /floating_ips: 固定返回 {"public_ips": 1}
        resource_amount = await self._extract_resource_amount(request, region, forwarded_headers)
        # 3b. 原子操作：检查配额 + 预扣资源（SELECT FOR UPDATE）
        await self.quota_service.check_and_reserve(db, org_internal_id, region.id, resource_amount)
        reserved = True

    elif quota_action == "release":
        # === DELETE 流程：先查资源规格（额外一次 GET 请求） ===
        resource_id = self._extract_resource_id(proxy_path)
        resource_amount = await self.quota_service.query_resource_amount(
            region, proxy_path, resource_id, forwarded_headers
        )

    elif quota_action == "resize":
        # === RESIZE 流程：按资源类型分别处理 ===
        body = await request.json()
        resource_id = self._extract_resource_id(proxy_path)
        resource_diff = {}
        shrink_amount = {}

        if "/instances/" in proxy_path:
            # --- VM resize：通过 flavor 差值计算 ---
            new_flavor_id = body.get("flavor")
            if new_flavor_id is not None:
                # 1. 查当前 VM 的资源量
                old_amount = await self.quota_service.query_resource_amount(
                    region, proxy_path, resource_id, forwarded_headers
                )
                # 2. 查 Cloudland 获取新 flavor 资源量
                new_amount = await self.quota_service.query_flavor_amount(
                    region, new_flavor_id, forwarded_headers
                )
                # 3. 计算差值
                resource_diff = {k: new_amount[k] - old_amount[k] for k in old_amount}
            else:
                quota_action = None  # 请求体无 flavor → 非 resize（改名等），跳过

        elif "/volumes/" in proxy_path:
            # --- Volume resize：通过 size 差值计算 ---
            new_size = body.get("size")
            if new_size is not None:
                # 1. 查当前 volume 的 size
                old_amount = await self.quota_service.query_resource_amount(
                    region, proxy_path, resource_id, forwarded_headers
                )
                # 2. 计算差值（仅 disk_gb）
                resource_diff = {"disk_gb": new_size - old_amount.get("disk_gb", 0)}
            else:
                quota_action = None  # 请求体无 size → 非 resize，跳过

        # 4. 拆分为正值（需预扣）和负值（成功后释放）
        if resource_diff:
            reserve_amount = {k: v for k, v in resource_diff.items() if v > 0}
            shrink_amount = {k: abs(v) for k, v in resource_diff.items() if v < 0}
            # 5. 扩容部分 → 预扣
            if reserve_amount:
                await self.quota_service.check_and_reserve(db, org_internal_id, region.id, reserve_amount)
                reserved = True
                resource_amount = reserve_amount

    try:
        # 4. 已有逻辑：转发请求到 Cloudland
        response = await self._forward_request(...)

        if response.status_code in (200, 201, 204):
            # 5a. DELETE 成功 → 释放配额
            if quota_action == "release" and resource_amount:
                await self.quota_service.release(db, org_internal_id, region.id, resource_amount)
            # 5b. RESIZE 成功 → 释放缩容部分（无论是否有扩容预扣）
            elif quota_action == "resize" and shrink_amount:
                await self.quota_service.release(db, org_internal_id, region.id, shrink_amount)
        else:
            # 5c. CREATE/RESIZE扩容 失败 → 回滚预扣
            if reserved:
                await self.quota_service.release(db, org_internal_id, region.id, resource_amount)

        return response
    except Exception:
        # 6. 网络异常：回滚预扣（仅 CREATE/RESIZE扩容 场景）
        if reserved:
            await self.quota_service.release(db, org_internal_id, region.id, resource_amount)
        raise
```

**DELETE 流程说明**：
1. 从 path 中提取资源 ID（如 `/instances/abc-123` → `abc-123`）
2. 向 Cloudland 发 GET 请求查询资源详情（`GET /instances/abc-123`）→ 获取 cpu/ram/disk
3. 转发 DELETE 请求到 Cloudland
4. DELETE 成功（200/204）→ release 对应配额
5. DELETE 失败 → 不释放（资源还在）

**代价**：每次 DELETE 多一次 GET 请求。可接受，因为 DELETE 频率远低于 GET/LIST。

### 4.4 配额检查的关键问题：如何获取资源量

统一采用**实时查 Cloudland**的方式获取资源量，CREATE 和 DELETE 模式一致：

**创建 VM（POST /instances）**：
- 请求体中包含 `flavor_id`，但不含具体 cpu/ram 数值
- proxy 先向 Cloudland 发 `GET /flavors/{flavor_id}` 查询 cpu/ram/disk
- 拿到真实资源量后再做配额检查和预扣
- 每次创建 VM 多一次 GET 请求，代价可接受（创建频率不高）

**创建 Volume（POST /volumes）**：
- 请求体中直接包含 `size`（GB），无需额外查询

**创建 Floating IP（POST /floating_ips）**：
- 固定消耗 1 个 public_ip，无需额外查询

**删除资源时**：
- DELETE 前先向 Cloudland 发 GET 请求查询资源详情（`GET /instances/{id}`、`GET /volumes/{id}`）
- 获取 cpu/ram/disk 等规格
- DELETE 成功后调用 `release()` 释放对应配额
- 每次 DELETE 多一次 GET 请求，代价可接受（DELETE 频率低）

---

## Phase 5: 错误处理

### 5.1 Quota 超限异常

```python
class QuotaExceededError(HTTPException):
    def __init__(self, resource: str, requested: float, available: float, limit: float, region: str):
        detail = {
            "error": "quota_exceeded",
            "resource": resource,
            "region": region,
            "requested": requested,
            "available": available,
            "limit": limit,
            "message": f"Org quota exceeded for {resource} in region {region}: "
                       f"requested {requested}, available {available} (limit: {limit})"
        }
        # 使用 429 而非 403：
        # - 403 是权限不足（Forbidden），会与 RBAC 权限错误混淆
        # - 429 是资源限额（Too Many Requests），语义更准确
        super().__init__(status_code=429, detail=detail)
```

---

## Phase 6: 前端改造

### 6.1 现状分析

当前前端 quota 相关代码：

| 文件 | 当前功能 | 问题 |
|------|----------|------|
| `web/src/views/dashboard/Overview.vue` | Dashboard 展示 CPU/RAM/Disk 使用率 | `used` 已从真实 API 聚合（instances/volumes 等），但 `total`（上限）是硬编码的（64 CPU, 128GB RAM 等），需改为从 quota API 取 |
| `web/src/views/dashboard/UserList.vue` | 管理员按**用户**管理配额的 modal | 粒度错误，应改为按 org-region 管理 |
| `web/src/api/users.ts` | `getUserQuota()`, `updateUserQuota()` | API 调用是 per-user 的，需改为 per-org-region |
| `web/src/api/stats.ts` | `getStats()` → `/quotas` | **死代码**：后端无 `/quotas` 端点，Overview.vue 也从未调用。应直接删除 |
| `web/src/locales/en.ts`, `zh.ts` | quota 相关翻译 | 需补充 region 相关翻译 |

### 6.2 TypeScript 类型改造

**文件**: `web/src/api/users.ts`（或新建 `web/src/api/quota.ts`）

```typescript
// 替换原有 per-user 类型
// 与后端 schema 保持一致：拆分嵌套用和独立返回用类型

// --- 嵌套用（不含 org_uuid/region_name） ---

export interface QuotaFields {
  max_cpu_cores: number
  max_ram_gb: number
  max_public_ips: number
  max_disk_gb: number
}

export interface ConsumptionFields {
  cpu_cores: number
  ram_gb: number
  public_ips: number
  disk_gb: number
}

export interface OrgResourceQuotaUpdate {
  max_cpu_cores?: number
  max_ram_gb?: number
  max_public_ips?: number
  max_disk_gb?: number
}

// --- 独立返回用 ---

export interface OrgResourceQuota extends QuotaFields {
  org_uuid: string
  region_name: string
  created_at: string
  updated_at: string
}

export interface OrgResourceConsumption extends ConsumptionFields {
  org_uuid: string
  region_name: string
}

// --- 组合类型 ---

export interface OrgResourceInfo {
  region_name: string
  consumption: ConsumptionFields
  quota: QuotaFields
}

export interface OrgResourceSummary {
  org_uuid: string
  regions: OrgResourceInfo[]
}
```

### 6.3 API 调用改造

**文件**: `web/src/api/quota.ts`（新建，集中管理 quota API）

```typescript
// 获取 org 在所有 region 的配额+消费汇总
getOrgResourceSummary(orgUuid: string): Promise<OrgResourceSummary>
  → GET /resources/info/{org_uuid}

// 获取 org 在特定 region 的配额+消费
getOrgRegionResourceInfo(orgUuid: string, regionName: string): Promise<OrgResourceInfo>
  → GET /resources/info/{org_uuid}/{region_name}

// 获取 org 在特定 region 的配额
getOrgQuota(orgUuid: string, regionName: string): Promise<OrgResourceQuota>
  → GET /resources/quota/{org_uuid}/{region_name}

// 更新 org 在特定 region 的配额 (superuser only)
updateOrgQuota(orgUuid: string, regionName: string, payload: OrgResourceQuotaUpdate): Promise<OrgResourceQuota>
  → PUT /resources/quota/{org_uuid}/{region_name}
```

### 6.4 Overview Dashboard 改造

**文件**: `web/src/views/dashboard/Overview.vue`

**当前**：
- `used` 值已从真实 API 聚合（遍历 instances 累加 flavor.cpu/memory，遍历 volumes 累加 size 等）
- `total` 值是硬编码的（64 CPU, 128GB RAM, 2000GB disk, 100 instances 等）

**改为**：
1. 从当前 tenant store 获取 `org_uuid` 和当前选中的 `region`
2. 调用 `GET /resources/info/{org_uuid}/{region_name}` 获取真实数据
3. 进度条的 `total` 改为取自 quota API（替换硬编码值）
4. `used` 可保留现有的聚合逻辑，或改用 consumption API（proxy 层已实时追踪消费数据）
5. 如果 org 有多个 region，用户已通过 header 的 region 切换器选择了当前 region，无需额外选择器

```
┌─────────────────────────────────────────────────┐
│  Overview - Region: [region-east ▾]             │
├──────────────┬──────────────┬───────────────────┤
│ CPU          │ Memory       │ Disk              │
│ ████░░ 12/20 │ ██████░ 48/64│ ███░░░░ 300/1000  │
│ 60%          │ 75%          │ 30%               │
├──────────────┬──────────────┬───────────────────┤
│ Public IPs   │              │                   │
│ ██░░░░ 3/10  │              │                   │
│ 30%          │              │                   │
└──────────────┴──────────────┴───────────────────┘
```

> **注**：Overview 只展示有配额上限的 4 项资源（CPU、Memory、Disk、Public IPs）。
> Traffic（流量）为计量型资源，不适用 consume/release 模式，暂不追踪，后续单独实现计量系统。

### 6.5 配额管理入口改造 — OrgDetail 新增 Quota Tab

**决策**：配额管理放在 OrgDetail 页面中作为一个 tab，不新建独立页面。

**理由**：
- 配额是 org 的属性，跟成员列表、基本信息放一起符合直觉
- 操作频率低，不值得占独立路由
- 管理员在同一页面能看到 org 信息、成员、配额，上下文完整

**改造内容**：

**文件**: `web/src/views/dashboard/OrgDetail.vue`

1. 在现有 tab 结构中新增 **"Quota"** tab
2. tab 内容按 region 分组，每个 region 一个 card/section
3. 每个 region card 显示资源表格（Resource / Quota / Used / Usage%）
4. superuser 可编辑 Quota 列（inline 编辑或点击 Edit 按钮弹 modal）
5. 普通 org 成员只读展示

**界面设计**：

```
OrgDetail Page
┌─────────────────────────────────────────────────────┐
│  Org: my-team                                       │
│  [Members]  [Settings]  [Quota] ← 新增 tab          │
├─────────────────────────────────────────────────────┤
│                                                     │
│  ┌─ region-east ──────────────────────────────────┐ │
│  │ Resource     │ Quota    │ Used   │ Usage       │ │
│  │──────────────┼──────────┼────────┼─────────────│ │
│  │ CPU Cores    │ [20   ]  │ 12     │ ████░░ 60%  │ │
│  │ Memory (GB)  │ [64   ]  │ 48     │ ██████░ 75% │ │
│  │ Disk (GB)    │ [1000 ]  │ 300    │ ███░░░ 30%  │ │
│  │ Public IPs   │ [10   ]  │ 3      │ ██░░░░ 30%  │ │
│  │                              [Save] ← superuser │ │
│  └─────────────────────────────────────────────────┘ │
│                                                     │
│  ┌─ region-west ──────────────────────────────────┐ │
│  │ Resource     │ Quota    │ Used   │ Usage       │ │
│  │──────────────┼──────────┼────────┼─────────────│ │
│  │ CPU Cores    │ [10   ]  │ 2      │ █░░░░░ 20%  │ │
│  │ ...          │ ...      │ ...    │ ...         │ │
│  │                              [Save] ← superuser │ │
│  └─────────────────────────────────────────────────┘ │
│                                                     │
│  ⓘ 普通成员：Quota 列显示为纯文本，无编辑按钮       │
│  ⓘ Superuser：Quota 列显示为 input，有 Save 按钮    │
└─────────────────────────────────────────────────────┘
```

**进度条颜色规则**（复用 Overview 的逻辑）：
- < 75%：绿色
- 75% - 90%：黄色
- \> 90%：红色

**数据加载**：
- 切到 Quota tab 时调用 `GET /resources/info/{org_uuid}` 获取所有 region 数据
- Save 按钮调用 `PUT /resources/quota/{org_uuid}/{region_name}` 逐 region 提交

**同时**：从 `UserList.vue` 中移除 per-user 配额管理 modal（Gauge 图标和相关代码）。

### 6.6 Quota 超限错误处理

**文件**: 全局 API 拦截器（`web/src/api/client.ts` 或类似）

当后端返回 `429 quota_exceeded` 时，前端应展示友好提示：

```typescript
// 在 axios/fetch 拦截器中
if (error.response?.status === 429 && error.response.data?.error === 'quota_exceeded') {
  const { resource, region, requested, available, limit } = error.response.data
  // 显示结构化提示，如：
  // "配额不足：region-east 的 CPU 配额已达上限（已用 18/20 核，本次请求 4 核）"
  showQuotaExceededNotification({ resource, region, requested, available, limit })
  return  // 不走通用错误处理
}
```

### 6.7 i18n 补充

**文件**: `web/src/locales/en.ts`, `web/src/locales/zh.ts`

```typescript
quota: {
    manage: 'Manage Quota' / '配额管理',
    cpuCores: 'CPU Cores' / 'CPU 核数',
    ramGb: 'Memory (GB)' / '内存 (GB)',
    diskGb: 'Disk (GB)' / '磁盘 (GB)',
    publicIps: 'Public IPs' / '公网 IP',
    // 新增
    region: 'Region' / '区域',
    used: 'Used' / '已使用',
    available: 'Available' / '可用',
    limit: 'Limit' / '上限',
    exceeded: 'Quota Exceeded' / '配额超限',
    exceededMessage: 'Quota exceeded for {resource} in {region}: requested {requested}, available {available} (limit: {limit})'
        / '{region} 的 {resource} 配额不足：请求 {requested}，可用 {available}（上限 {limit}）',
    noQuota: 'No quota assigned' / '未分配配额',
    allRegions: 'All Regions' / '所有区域',
},
```

---

## 实施顺序与依赖关系

```
Phase 1 (数据模型) ──→ Phase 2 (Schema/API) ──→ Phase 3 (初始化)
                                                      │
                                                      ▼
                                                Phase 4 (Proxy 配额检查 + 消费追踪)
                                                      │
                                                      ▼
                                                Phase 5 (错误处理)
                                                      │
                                                      ▼
                                                Phase 6 (前端改造)
```

Phase 6 依赖 Phase 2（API 端点就绪）和 Phase 5（错误格式确定），可与 Phase 4 并行开发（前端先 mock 数据）。

---

## 需要确认的决策点

1. ~~**旧的 per-user quota 表如何处理？**~~ — **已确认**：产品未上线，直接删除旧表和旧代码
2. ~~**Flavor 资源映射**~~ — **已确认**：实时查 Cloudland（`GET /flavors/{id}`），不维护本地 flavor 表
3. ~~**同步频率**~~ — **已取消**：不再需要定期同步，proxy 层统一处理 CREATE 和 DELETE
4. ~~**Superuser 是否受配额限制？**~~ — **已确认**：superuser 不受配额限制
5. ~~**System org 是否需要特殊处理？**~~ — **已确认**：system org 不受配额限制
6. ~~**并发安全**~~ — **已解决**：使用 check_and_reserve 原子操作（SELECT FOR UPDATE + 预扣在同一事务中）
7. ~~**新增 region 时**~~ — **已确认**：自动为所有 org 初始化该 region 的 quota（使用 config 默认值）
8. ~~**配额管理入口**~~ — **已确认**：放在 OrgDetail 页面作为 Quota tab

---

## 涉及文件清单

### 后端

| 文件 | 操作 |
|------|------|
| `cpgateway/app/models/org_resource_quota.py` | 新建 |
| `cpgateway/app/models/org_resource_consumption.py` | 新建 |
| `cpgateway/app/models/resource_quota.py` | 删除 |
| `cpgateway/app/models/resource_consumption.py` | 删除 |
| `cpgateway/app/models/org.py` | 修改（加 relationships） |
| `cpgateway/app/models/user.py` | 修改（删除旧 quota relationships） |
| `cpgateway/app/models/__init__.py` | 修改（注册新模型，删除旧模型） |
| `cpgateway/app/schemas/resource.py` | 重写（删除旧 schemas，新增 org-region schemas） |
| `cpgateway/app/api/endpoints/resources.py` | 重写（删除旧端点，新增 per-org-per-region） |
| `cpgateway/app/services/auth_service.py` | 修改（org 创建时按 region 初始化 quota） |
| `cpgateway/app/services/quota_service.py` | 新建（check_and_reserve / release / query_resource_amount） |
| `cpgateway/app/services/proxy_service.py` | 修改（加入 CREATE 预扣 + DELETE 先查后释放） |
| alembic migration | 新建（创建新表 + 删除旧表） |

### 前端

| 文件 | 操作 |
|------|------|
| `web/src/api/quota.ts` | 新建（集中 quota API 调用） |
| `web/src/api/users.ts` | 修改（删除旧的 quota 类型和 API） |
| `web/src/api/stats.ts` | 删除（死代码，后端无对应端点） |
| `web/src/views/dashboard/Overview.vue` | 修改（硬编码 total 改为 quota API，保留 used 聚合逻辑） |
| `web/src/views/dashboard/UserList.vue` | 修改（删除 per-user 配额管理 modal） |
| `web/src/views/dashboard/OrgDetail.vue` | 修改（新增 org-region 配额管理界面） |
| `web/src/locales/en.ts` | 修改（补充 region/quota 翻译） |
| `web/src/locales/zh.ts` | 修改（补充 region/quota 翻译） |
| `web/src/api/client.ts`（或拦截器位置） | 修改（增加 quota_exceeded 错误处理） |
