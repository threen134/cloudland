# 镜像公共/私有可见性设计方案

## 背景

当前镜像 `List` 查询没有按 `owner` 过滤，所有镜像对所有租户都可见。需要重新设计：

- **普通租户**：只能看到自己创建的镜像 + 公共镜像
- **SystemAdmin**：可以将镜像设为公共，供所有租户使用

## 现有基础

| 组件 | 现状 |
|------|------|
| `Image.Visibility` 字段 | 数据库已存在 (`varchar(36)`)，未使用 |
| `Image.Owner` 字段 | 已存在，创建时设为 `memberShip.OrgID` |
| `GetOrgFilter()` | 其他资源（instance, volume 等）已使用此方法按 owner 过滤；**镜像不能直接复用**，因为需要 `owner = ? OR visibility = ?` 的组合条件 |
| 系统组织 | `OrgTypeSystem = 2`，启动时创建，名称 "admin"，全局唯一 |
| 前端 ImageDetail | 已有 `image.public` 的 badge 展示逻辑（通过 `[key: string]: any` 索引签名访问，字段未在类型中显式声明） |

## 权限说明

设置 visibility（public/private）的权限：**仅 SystemAdmin**（即 `CheckSystemPermission()`）。
`CheckSystemPermission()` 等价于 `IsSystemAdmin()`，检查 `SystemRole == SystemAdmin`，不包含 admin org 的普通成员。

## 设计方案

### 1. 数据模型

复用现有 `Visibility` 字段，在 `api/src/model/image.go` 中定义两个常量：

- `ImageVisibilityPrivate = "private"` — 默认，仅 owner org 可见
- `ImageVisibilityPublic  = "public"`  — 所有租户可见

创建镜像时的默认值由创建者身份决定：

- **SystemAdmin 创建**：`Visibility = ImageVisibilityPublic`
- **普通租户创建**：`Visibility = ImageVisibilityPrivate`

### 2. Go API 层改动

#### 2.1 `ImageResponse` 增加字段

**文件**: `api/src/apis/image.go`

`ImageResponse` 增加两个字段：
- `Public bool json:"public"`，在 `getImageResponse` 中映射为 `image.Visibility == model.ImageVisibilityPublic`
- `Owner string json:"owner"`，返回 owner org 的 UUID 或名称，供前端判断当前用户是否为 image 的所属租户（影响 delete/edit 按钮的展示）

#### 2.2 `ImagePatchPayload` 增加字段

**文件**: `api/src/apis/image.go`

`ImagePatchPayload` 增加 `Public *bool json:"public" binding:"omitempty"`（可选，仅 SystemAdmin 可生效）。

**现有字段 binding 修复**：当前 `ImagePatchPayload` 的 `Name`、`OSCode`、`OSVersion`、`User`、`OsFamily` 均标记为 `binding:"required"`，与 PATCH 语义（部分更新）冲突。若只想切换 public/private，还必须传所有 required 字段，体验很差。应将现有字段的 `binding:"required"` 改为 `binding:"omitempty"`，Service 层 `Update` 方法内对空值字段跳过更新即可（当前已有 `if name != "" { ... }` 模式）。

创建时不需要 public 参数（默认 private），`ImagePayload` 不变。

`Patch` handler 在调用 `imageAdmin.Update(...)` 时将 `payload.Public` 传入。

#### 2.3 Service 层 — `Update` 方法签名变更

**文件**: `api/src/services/image.go`

`Update` 方法签名增加 `public *bool` 参数。在方法内部：

- 若 `public != nil`，检查 `memberShip.CheckSystemPermission()`，无权限则返回 `ErrPermissionDenied`
- 有权限则根据 `*public` 的值设置 `image.Visibility`

**注意**：当前 `Update` 已经要求 `CheckSystemPermission()`，即非 SystemAdmin 调用 Update 会在方法入口就返回 `ErrPermissionDenied`，visibility 设置逻辑只需在已通过该检查后执行即可。

#### 2.4 Service 层 — `Create` 方法改造

**文件**: `api/src/services/image.go`

无论走哪条分支（普通上传 或 从实例捕获），在最终 `db.Create(image)` 之前，统一设置：

```
image.Visibility = model.ImageVisibilityPrivate
```

**特别注意**：从实例捕获时走 `instance.Image.Clone()` 路径，`Clone()` 会复制原镜像的 `Visibility` 字段。若原镜像是 public，捕获出的镜像会错误地继承 public 状态。因此必须在两条分支汇合后、写入 DB 前，根据 `memberShip.IsSystemAdmin()` 统一覆盖，不能只在 else 分支里设置。

#### 2.5 Service 层 — `List` 方法改造

**文件**: `api/src/services/image.go`

当前 List 无任何 owner 过滤。改造后：

- **SystemAdmin**：不加 owner/visibility 过滤，看到全部镜像
- **普通用户**：过滤条件为 `(owner = <orgID> OR visibility = 'public')`，同时叠加 name 模糊搜索条件

**增加 `visibility` 过滤参数**：List API 增加可选的 `visibility=public|private|all` query parameter（默认 `all`），方便前端按可见性筛选。SystemAdmin 可使用任意值；普通用户传 `private` 时只看自己 org 的，传 `public` 时只看公共的，传 `all` 或不传时看 own + public。

**修复 SQL 注入**：当前 `query` 参数直接拼接到 SQL（`fmt.Sprintf("name like '%%%s%%'", query)`），存在 SQL 注入漏洞。改造时必须改为参数化查询：`db.Where("name LIKE ?", "%"+query+"%")`。

**关键**：`Count` 查询和 `Find` 查询必须使用完全相同的过滤条件，否则返回的 `total` 与实际结果集数量不一致，导致分页错误。两条 DB 语句都需要修改，不能只改 `Find`。

#### 2.6 Service 层 — Get 系列方法权限调整

**文件**: `api/src/services/image.go`

涉及 `GetImageByUUID`、`GetImageByName`、`Get` 三个方法。`GetImage` 方法是前三者的包装（按 UUID 或 Name 委托调用），改了内部方法后自动生效，无需单独改动。

当前三个方法均使用 `memberShip.CheckOrgPermission(model.OrgReader)`，该检查只验证用户在当前 org 是否有读权限，不验证资源归属，导致任何 org 的用户都能读任意镜像（现有 bug）。

修改逻辑：
1. 若 `image.Visibility == ImageVisibilityPublic` → 直接放行，无需 org 检查
2. 否则走 `memberShip.CheckResourceOrg(model.OrgReader, image.Owner)` 验证归属

**`GetImageByName` 特殊情况**：name 不是全局唯一键，若两个 org 各有一个名为相同的 public 镜像，`db.Where("name = ?", name).Take(image)` 会随机返回一条。此方法主要用于 RPC 内部回调，风险可控，但调用方应优先使用 UUID。在文档中注明该方法不保证在同名 public 镜像场景下的确定性，建议外部调用通过 UUID 查询。

### 3. 前端改动

#### 3.1 API 类型

**文件**: `web/src/api/images.ts`

`Image` 接口显式增加 `public?: boolean` 字段（当前通过 `[key: string]: any` 隐式访问）。

无需改 `ImagePayload`（创建时不设置 public）。

#### 3.2 镜像列表页

**文件**: `web/src/views/dashboard/Images.vue`

- 表格增加"可见性"列，显示公共/私有 badge
- SystemAdmin 用户在操作区增加"设为公共/私有"入口
- **Delete 按钮条件展示**：当前 delete 按钮对所有用户无条件展示，普通租户对非本 org 的 public image 点击删除会得到 403。改为：非 owner org 且非 SystemAdmin 时隐藏 delete 按钮（需要 `ImageResponse` 中返回的 `owner` 字段与当前 org 对比）

#### 3.3 镜像详情页

**文件**: `web/src/views/dashboard/ImageDetail.vue`

- 已有 `image.public` 的展示逻辑，字段加到类型后行为不变（`false` 和 `undefined` 均走 private 分支）
- SystemAdmin 用户增加切换 public/private 的操作按钮，调用 PATCH 接口传 `{ public: true/false }`
- 同列表页，非 owner org 且非 SystemAdmin 时隐藏 delete 按钮

### 4. 数据迁移

对现有数据做一次迁移，通过 `dbs.AutoUpgrade` 机制注册：

- 将所有 `visibility` 为空或 NULL 的镜像设为 `'private'`
- 将 SystemAdmin 创建的镜像（可通过 `owner` 关联 `organizations.type = 2` 判断）设为 `'public'`，与新建逻辑保持一致

**幂等性**：迁移 SQL 必须带 `WHERE visibility = '' OR visibility IS NULL` 条件，确保重复执行不会覆盖已手动设置过的 visibility 值。`AutoUpgrade` 在每次服务启动时都会执行，必须保证幂等。

### 5. CPGateway

**无需改动**。CPGateway 只做透明代理转发，过滤逻辑在 Go API 后端处理。

## 实施步骤

| 步骤 | 内容 | 涉及文件 |
|------|------|----------|
| 1 | Model 层增加 `ImageVisibilityPrivate` / `ImageVisibilityPublic` 常量 | `api/src/model/image.go` |
| 2 | `Create` 两条分支汇合后按创建者身份设 Visibility：SystemAdmin → `public`，其他 → `private`（覆盖 Clone 继承） | `api/src/services/image.go` |
| 3 | `List`：修复 SQL 注入（参数化 query）；**Count 和 Find** 均增加 `owner = ? OR visibility = 'public'` 过滤（SystemAdmin 不过滤）；增加可选 `visibility` 过滤参数 | `api/src/services/image.go`, `api/src/apis/image.go` |
| 4 | `GetImageByUUID`、`GetImageByName`、`Get` 改为：public 镜像直接放行，private 镜像走 `CheckResourceOrg` | `api/src/services/image.go` |
| 5 | `Update` 签名增加 `public *bool`，在已通过 `CheckSystemPermission` 后设置 visibility | `api/src/services/image.go` |
| 6 | `ImageResponse` 增加 `Public bool` 和 `Owner string`；`ImagePatchPayload` 增加 `Public *bool` 并将现有字段 binding 改为 `omitempty`；`Patch` handler 传参 | `api/src/apis/image.go` |
| 7 | 数据迁移：现有镜像设为 private（通过 `AutoUpgrade`，确保幂等） | `api/src/model/image.go` 的 `init()` |
| 8 | 前端 `Image` 接口增加 `public?: boolean` 和 `owner?: string` | `web/src/api/images.ts` |
| 9 | 前端列表页增加可见性列和可见性筛选；详情页 SystemAdmin 增加切换按钮；非 owner 非 SystemAdmin 隐藏 delete 按钮 | `web/src/views/dashboard/Images.vue`, `ImageDetail.vue` |

## 注意事项

- 创建实例时选镜像走同一个 `List` 接口，过滤改造后租户可以看到并使用公共镜像，无需额外改动
- Delete 权限保持不变：只有 owner org 的 Writer 或 SystemAdmin 可以删除；公共镜像被其他租户实例引用时 refCount 检查已阻止删除
- `GetImageByName` 在同名 public 镜像场景下行为不确定，内部 RPC 回调可接受，外部调用应用 UUID
- 设置 visibility 的权限仅限 SystemAdmin（`CheckSystemPermission()`），不包含 admin org 的普通成员
- Delete 权限补充说明：`CheckResourceOrg` 中 SystemAdmin 直接放行（`member.go:69`），因此 SystemAdmin 可删除任何 org 的 image，无需额外处理
- **已有 bug（顺带修复）**：`api/src/apis/image.go` 第 234 行 API 层调用 `Create` 时 hardcoded `isRescue = true`，导致所有通过 API 创建的 image 都被标记为 rescue image。改造 Create 调用时应使用 `payload.IsRescue` 传入
