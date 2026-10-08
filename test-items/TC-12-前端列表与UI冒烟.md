# TC-12 前端列表与 UI 冒烟（UI）

优先级 **P0** · 非破坏性（脚本在 context 上兜底拦截写请求；只有 UI-07 会真实打开一次文本控制台并 switch-org） · 约 25 min

改了任何共享组件（`DataTable`、`BaseModal`、`PageToolbar`、`PaginationBar`、`StatusBadge`、`useListQuery`…）或任何列表页，都必须跑完本文件。共享组件一处改动会同时影响 20 多个页面。

## 验证位置（2026-09-28 核对）

- **以本机 `http://127.0.0.1:5173`（`npm run dev`）为准**：`web/.env.development` 的 `VITE_API_PROXY_TARGET=https://169.61.110.83` 把 `/api` 代理到 **work-01 真实环境**，界面与本地 HEAD 一致；Playwright 在本机跑（CLAUDE.md「前端改动只在本地验证」）。
- work-01 公网界面（`https://169.61.110.83`，nginx）停在 09-27 的 `facff649`：**没有放置组**（侧边栏没有入口，`/dashboard/placement-groups` 落到兜底路由被送回首页，创建云服务器弹窗没有放置组下拉）。只在「前端刚部署到 work-01」时把脚本的 `BASE` 换成公网地址再跑一遍 UI-01 做部署后冒烟；两边结果不一致时以 5173 为准，执行记录里写明 work-01 界面的版本。
- 5173 上的每个操作都**真实打到 work-01**，所以脚本一律按 pathname 兜底拦截写请求（见前置第 3 条）。

## 前置

1. dev server：
   ```bash
   export PATH="/c/Program Files/nodejs:$PATH"
   curl -s -o /dev/null -w "%{http_code}\n" --max-time 5 http://127.0.0.1:5173/   # expect 200
   # not running: start it in another terminal and leave it there
   cd web && npm run dev
   ```
2. Playwright 装在会话临时目录（新会话要重装；浏览器缓存在 `%LOCALAPPDATA%\ms-playwright`，已有时很快）：
   ```bash
   TC12=<会话临时目录>/tc12 && mkdir -p "$TC12" && cd "$TC12"
   echo '{"private":true}' > package.json
   npm i playwright@1.55.0 pixelmatch@5.3.0 pngjs@7 && npx playwright install chromium
   export ADMIN_PASSWORD="$(ssh work-01 "grep '^ADMIN_PASSWORD=' /opt/cloudland/deploy/docker/.env | cut -d= -f2-")"
   ```
   把文末「附：脚本」里的脚本写到 `$TC12` 下。
3. **写请求兜底拦截**：脚本在 `context.route` 上按 `URL.pathname` 判断，除 `GET` 和 `/api/v1/auth/` 以外的 `/api/` 请求一律 `abort` 并记下来（不要写成 `**/api/v1/xxx` 这种 glob，带 `?region=` 的请求会漏掉）。UI-01 / UI-02 连 `telemetry/traces` 与 `metrics/*/his_data` 这两个只读语义的 POST 也拦下，用来核对「被拦的只有这几条」。
4. 脚本自身的坑：
   - `ctx.newPage()` 新开的页拿不到 sessionStorage 里的 token（没勾「记住我」），会被送回登录页。控制台页要在同一个页里 `goto`，或像真实用户那样由 `window.open` 弹出（会继承 sessionStorage）。
   - `DataTable` 的加载 / 空 / 错误态本身也是 tbody 里的一行（`.table-state`），数行数要用 `.data-table > tbody > tr:not(:has(.table-state))`。work-01 `/root/console-test/deployed-regression.js` 直接数 `tbody tr`，**空列表也算 1 行**，抓不到「列表永远空」这类问题。
   - work-01 `/root/console-test/` 下的旧脚本（`deployed-regression.js`、`create-modals-sweep.js`、`vmmodal.js`）`BASE` 写死 `https://127.0.0.1`、页面清单过时，`create-modals-sweep.js` 还**没有拦截写请求**，本文件不再使用。

---

## UI-01 全页面渲染冒烟

**P0** · 脚本 `tc12-smoke.js`（附录）· 约 3 min

```bash
cd "$TC12" && node tc12-smoke.js 2>&1 | tee smoke.log
```

脚本以 admin 登录（1500×950、zh-CN），依次打开侧边栏上的全部列表页与操作动态页（26 个），数表格行（不含状态行）/ 卡片、找页面上泄漏的文案键名、记录 4xx/5xx 与页面错误和首次列表请求；再打开 17 个列表各自第一行的详情页（放置组为 0 个时跳过，剩 16 个）；最后在同一个页里打开图形控制台、文本控制台、节点终端三页。

**基线**（2026-09-28，work-01 数据；数据量会变，关注的是「不为 0、没报错、请求参数对」）：

| 页面 | 期望 | 首次列表请求（去掉 `region=`） |
|---|---|---|
| overview | 卡片 = 2 | `volumes?limit=100&type=all`、`vpcs?limit=100`… |
| activities | 行 = 20 | `activities?limit=20&start=…&end=…`（预设范围也显式带 `end`） |
| instances | 行 15 | `instances?offset=0&limit=20&order=-created_at` |
| placement-groups | 0 个组时是空态（图标 +「还没有放置组」+ 说明）；**仅 5173** | `placement_groups?offset=0&limit=20&order=-created_at` |
| volumes | 行 15（含系统盘） | `volumes?offset=0&limit=20&order=-created_at&type=all` |
| images / flavors / keys | 2 / 3 / 3 | `…?offset=0&limit=20&order=-created_at` |
| vpcs / subnets / floating-ips | 5 / 10 / 9 | 同上 |
| security-groups | 17 | `security_groups?offset=0&limit=20`（不排序） |
| load-balancers | 1 | `load_balancers?offset=0&limit=20`（不排序） |
| vpn-gateways | 4（a1、ibmx-gw、ibmr-gw、aax-gw） | `vpn_gateways?offset=0&limit=20&order=-created_at` |
| users | 1（当前组织成员） | `orgs/<组织 uuid>/members?offset=0&limit=20` |
| orgs / regions / zones | 1 / 1 / 1 | `orgs?offset=0&limit=20` / `regions?limit=500` / `zones?offset=0&limit=20&order=name` |
| hypervisors | 3 | `hypers?offset=0&limit=20`（不排序） |
| storage-pools | 2（local、local-hdd） | `storage_pools?offset=0&limit=20`（不排序） |
| migrations | 20（共 113） | `migrations?offset=0&limit=20&order=-created_at` |
| alarms / vm-alarm-rules / notification-channels | 7 / 4 / 2 | 通知渠道 `notification-channels?limit=500` |
| alarm-events | 20 | — |
| settings | 卡片 = 5 | — |
| ~~audit-logs、quota~~ | **已废弃**（2026-09-28）：前端没有这两条路由（审计日志只有接口 `GET /audit_logs`，配额在组织详情的「配额」标签），访问会被兜底路由送回首页，旧脚本把它们记成 0 行也看不出问题 | |

详情页基线：16 个都有 `.title-bar`、`.detail-header` 里的返回按钮文案是「返回」、页面不横向滚动；带标签页的：云服务器「基本信息 / 资源监控 / 监控与告警」，安全组「安全组规则 N / 关联网卡 N」，VPN 网关「概览 / 站点连接 N / 客户端 N / 监控 / 操作记录」，组织「成员 / 配额」，计算节点「概览 / 存储 / 资源监控」。

控制台三页：`POST /instances/<id>/console`、`POST /hypers/<uuid>/console` 被拦下，三页都显示「连接失败 / Network Error / 重试」，`.console-main` 的 `min-height` 为 `0px`。真实连接见 `TC-09`。

检查项：

- [ ] 26 个页面都停在自己的路径（没有被送回首页或登录页），行数不为 0（放置组 0 个时的空态除外）
- [ ] 没有 `KEYLEAK`（页面上出现 `dashboard.xxx` 这类键名）、没有 `BAD`（4xx/5xx）、`pageerrors: none`
- [ ] 被拦截的写请求**只有** `POST /api/v1/telemetry/traces`、`POST /api/v1/metrics/instances/{cpu,memory}/his_data`，以及控制台页的两个 `POST …/console`
- [ ] 列表首次请求都带 `offset=0&limit=20`；服务端排序的页面带 `order`（明细见 UI-03）
- [ ] **VPN 网关列表首次进入就请求了 `/vpn_gateways`**，行数 = 4
- [ ] 16 个详情页 `title-bar=1`、返回按钮存在、`hscroll=0`、没有 `BAD` / `ERROR`（返回按钮的两种写法见已知问题 K-7）
- [ ] 控制台三页没有页面错误，停在各自路径（没被送回登录页）

### 历史缺陷回归点

| 现象 | 根因 |
|---|---|
| 组织列表页改用 `useListQuery` 后完全空白 | 误以为 `useListQuery` 会自动加载，把 `onMounted(fetchOrgs)` 删了。**它不会自动加载**，必须显式触发 |
| VPN 网关列表页永远「未发现 VPN 网关」（2026-09-23） | 同一根因：`VpnGateways.vue` 的 `onMounted` 只调了 `fetchVpcs()`。typecheck / lint 全过，只有真跑冒烟才发现——**新列表页一律要跑一次 UI 冒烟**（放置组、存储池列表都是这之后加的，已在本脚本里） |
| 安全组列表里超过 50 个的组看不到 | 前端没传 `limit`，后端默认只回 50 条，前端还在本地过滤 |
| 安全组列表的 `vpc` 列显示的是另一条安全组的名字 | 后端 `SecgroupAdmin.List` 查路由器时复用了列表语句（GORM Statement 复用），详情接口正常、只有列表错 |
| 负载均衡列表状态显示后端原文（未翻译）（2026-09-21） | `StatusBadge` 的 `label` 原先可省略、缺省显示原始状态串；现在是必填，漏传即类型检查报错 |
| 冒烟脚本对空列表报「行 = 1」 | 数的是 `tbody tr`，把 `DataTable` 的空态行算进去了（见前置第 4 条） |

---

## UI-02 创建弹窗冒烟

**P0** · 脚本 `tc12-modals.js`（附录）· 约 3 min

```bash
cd "$TC12" && node tc12-modals.js 2>&1 | tee modals.log
```

逐页点工具条上的主按钮打开创建弹窗（17 个），记录每个下拉的选项数、背景滚动锁、焦点位置、ESC 是否关闭、打开弹窗时发出的**不带 `limit`** 的列表请求。

**基线**（2026-09-28）：

```
instances       [创建云服务器] 镜像 *=2 | 规格 / 方案 *=3 [vmtest-large (2 vCPU, 4 GB RAM, 500 GB 磁盘)] | 可用区=1 | 放置组 (可选)=0 | 虚拟私有网络 (VPC)=5 | 子网=2 | IP 地址=251
placement-groups [创建放置组] 可用区=1                     images [创建镜像] 操作系统类型=3 | 启动引导=2
flavors / keys / vpcs / zones  无下拉                      subnets [创建子网] 类型=4 | 虚拟私有网络 (VPC) *=5
floating-ips    [申请弹性公网IP] 公网子网=1 | 绑定实例=14   security-groups [创建安全组] 虚拟私有网络 (VPC) (可选)=5
load-balancers  [创建负载均衡] 虚拟私有网络 (VPC)=5 | 可用区（可选）=1
vpn-gateways    [创建 VPN 网关] 虚拟私有网络 (VPC) *=5 | 可用区（可选）=1 | 公网子网（可选）=1 | 公网 IP（可选）=0(disabled)
hypervisors     [部署节点] 可用区=1 | 虚拟化类型=2          storage-pools [创建存储池] 介质=3
migrations      [开始迁移任务] 按节点筛选=3 | 需要迁移的云服务器 *=15 | 目标节点（可选）=3
notification-channels [创建 通知渠道] 渠道类型=2            vm-alarm-rules [创建 CPU 告警规则] 类型=3 | ?=3
```

检查项：

- [ ] 17 个弹窗全部打开，`pageerrors: none`，被拦截的写请求只有 telemetry / his_data
- [ ] 必填下拉没有空的。**按设计为空、不算失败**：VPN 的「公网 IP（可选）」在没选公网子网时禁用且无选项（`VpnPublicAddressPicker`，选了子网才列出空闲地址、并排除子网网关）；创建云服务器的「放置组 (可选)」在本组织 0 个放置组时为空
- [ ] 规格项形如 `vmtest-large (2 vCPU, 4 GB RAM, 500 GB 磁盘)` —— 数字不能是 `0` 或空
- [ ] **migrations 下拉数是 3**（按节点筛选 / 云服务器 / 目标节点）。若是 4，说明「迁移类型」下拉被改回来了 —— 见 `TC-07 MIG-04`
- [ ] 每个弹窗 `body=hidden->-`（打开时锁背景滚动、关掉后恢复）、`esc=closed`、`focus` 在弹窗内、**不在关闭按钮上**（落点规则见 UI-05）
- [ ] `noLimitGET` **为空**：17 个弹窗打开时发出的列表请求都带 `limit`（下拉 / 选择器一律 `limit=500`，`web/src/api/listParams.ts` 的 `OPTION_LIST_LIMIT`）
  > **2026-09-30 已修（`OPTION_LIST_LIMIT` 用到 20 余个页面与组件），2026-10-01 已部署，按本条判定**。部署前的已知清单（FAIL-8，2026-09-28）：instances 8 个（images、vpcs、security_groups、keys、subnets、floating_ips、flavors、zones），subnets→vpcs，floating-ips→instances、subnets，hypervisors→zones，migrations→instances、hypers，vm-alarm-rules→notification-channels；部署前只要求「不多出新的」
- [ ] **打开后什么都不改、直接按回车，不发任何写请求**（兜底拦截记下的写请求数为 0），弹窗也不被关掉；每个弹窗都这样试一次（results.md：66 个传 `form` 的弹窗，实测 39 个打开直接回车 0 写请求，脚本 `p5-enter.js`）
  > **2026-09-30 已修（`web/src/components/modals/BaseModal.vue`），2026-10-01 已部署，按本条判定**；细节见 UI-05
- [ ] VPN 网关弹窗的 VPC 下拉里，已有网关的 VPC 选项是禁用的（5 个里 4 个禁用，只有 mig-vpc 可选）

  > **回归点**：`Flavor` 的字段是 `uuid` / `cpu` / `memory` / `disk`，不是 `id` / `vcpus` / `ram`。页面上曾写成 `f.vcpus || f.cpu` 这种带回退的形式，靠回退才显示正确。

---

## UI-03 服务端分页 / 搜索 / 排序

**P0**

**约定**：列表页的分页、搜索、排序**一律在服务端做**。分页之后如果搜索或排序还在前端，用户操作的只是当前这一页，比没有更糟。

### 3.1 clapi 侧列表

参数 `offset` / `limit` / `query` / `order`，返回 `{total, <资源>}`。接口只读，经 cpgateway：

```bash
ssh work-01 'source /root/alarmtest-env.sh >/dev/null 2>&1
# volumes lists data disks only unless type=all (the UI always sends it)
for r in instances "volumes?type=all" images flavors keys vpcs subnets floating_ips security_groups load_balancers vpn_gateways placement_groups storage_pools migrations zones hypers; do
  k=${r%%\?*}; sep=$([ "$k" = "$r" ] && echo "?" || echo "&")
  out=$(api GET "/$r${sep}limit=1")
  printf "%-18s total=%-5s n=%s\n" "$k" "$(echo "$out" | jq -r ".total // \"no-total\"")" "$(echo "$out" | jq -r ".$k | length")"
done
echo "--- query narrows total"
api GET "/vpn_gateways?query=ibm&limit=50" | jq -c "{total, names:[.vpn_gateways[].name]}"
echo "--- order"
api GET "/instances?order=hostname&limit=5"  | jq -r "[.instances[].hostname]|join(\",\")"
api GET "/instances?order=-hostname&limit=5" | jq -r "[.instances[].hostname]|join(\",\")"
echo "--- illegal order is dropped, not an error"
curl -sk -o /dev/null -w "%{http_code}\n" "$B/instances?order=;drop&limit=2" -H "Authorization: Bearer $T"'
```

2026-09-28 的输出：instances 15、volumes 15、images 2、flavors 3、keys 3、vpcs 5、subnets 10、floating_ips 9、security_groups 17、load_balancers 1、vpn_gateways 4、placement_groups 0、storage_pools 2、migrations 113、zones 1、hypers 3；`query=ibm` → 2（ibmr-gw、ibmx-gw）；正序 `ax1,ax2,ax3,big-vm,ir1`、倒序 `rb-be3,rb-be1,net-c,net-b,net-a`；非法 order → 200。

- [ ] 每个资源都返回 `total`，且 `limit=1` 时只回 1 条（总数为 0 的回 0 条）
- [ ] 云硬盘不带 `type=all` 时只列数据盘（work-01 上是 0 条）—— 这是接口设计，不是缺陷；界面一律带 `type=all`
- [ ] `total` 是**过滤后**的总数（带 `query` 时随之变化）
- [ ] 正序 / 倒序结果相反；非法 `order` 被丢弃，接口仍返回 200（`dbs.Sortby` 用正则限定成标识符）

**各页开放排序的列**（2026-09-28，`tc12-smoke.js` 输出的 `sortable:`；点表头只发请求、不在本地重排，`order` 是数据库真实列名，`sortField` 与列 key 不同的写在括号里）：

| 页面 | 可排序列 |
|---|---|
| 云服务器 | 名称（`hostname`）、状态 |
| 放置组 | 名称、策略（`policy`）、创建时间 |
| 云硬盘 | 名称、状态、容量（`size`）、启动盘（`booting`） |
| 镜像 | 名称、可见性、架构、格式、容量、状态 |
| 规格 | 名称、CPU、内存（`memory`）、存储（`disk`） |
| SSH 密钥 | 名称、指纹（`finger_print`）、创建时间 |
| VPC | 名称、状态、创建时间 |
| 子网 | 名称、网段（`network`）、VLAN / VXLAN、类型 |
| 弹性 IP | 名称（表头文案见 K-1）、IP 地址（`fip_address`）、类型 |
| VPN 网关 | 名称、状态、创建时间 |
| 可用区 | 名称、备注（默认 `order=name`） |
| 迁移 | 名称、类型、状态、进度（`progress`）、创建者（`creater_name`）、创建时间 |
| 安全组、负载均衡、计算节点、存储池 | 不排序（接口分页，页面上没有排序箭头） |
| 节点告警、通知渠道、区域 | 本地排序（一次取全量：节点告警不分页、通知渠道与区域 `limit=500`），可以接受 |

- [ ] 表格里的排序箭头与上表一致；点表头后请求里的 `order` 正确、再点一次变成 `-<列>`，页码回到 1
- [ ] 以下**排不了**的列没有排序箭头：卷的「挂载到」、子网的「VPC」、弹性 IP 的「挂载到」（关联表字段）；云服务器的用量、IP 使用率（前端算出来的）；可用区的「类型」（列名是 SQL 保留字 `default`）
- [ ] 迁移按「进度」倒序时，进度为空的旧记录排在最前（38 条早于进度功能的记录 `progress` 为 NULL，PostgreSQL 倒序默认 NULLS FIRST，见 K-9）

> **注意**：IP / CIDR 列是 varchar **字典序**，不是按 IP 数值排。`192.168.100.1` 会排在 `192.168.9.1` 前面，这是预期行为。

### 3.2 网关侧列表

组织 / 用户 / 区域 / 通知渠道 / 组织成员（`cpgateway/src/apis/list_query.go`），参数 `offset` / `limit` / `query` / **`order`**（`order` 2026-09-30 加，2026-10-01 已部署；之前没有），默认每页 50、上限 500。`order` 形如 `name` / `-name`，只认各列表的白名单：组织 `name`、`slug`、`status`、`created_at`；用户 `username`、`email`、`status`、`created_at`；区域 `name`、`display_name`、`created_at`；通知渠道 `name`、`type`、`enabled`、`created_at`；组织成员 `username`、`email`、`org_role`、`created_at`，其他值忽略、按默认顺序返回。

```bash
ssh work-01 'source /root/alarmtest-env.sh >/dev/null 2>&1
api GET "/orgs?limit=1"  | jq -c "{total, n:(.orgs|length)}"
api GET "/users?limit=1" | jq -c "{total, n:(.users|length)}"
api GET "/regions?limit=1" | jq -c "{total, n:(.regions|length)}"
api GET "/notification-channels?limit=1" | jq -c "{total, n:(.channels|length)}"
echo "--- case-insensitive search"
api GET "/orgs?query=ADMIN" | jq -c "{total, names:[.orgs[].name]}"
echo "--- limit above 500 is clamped"
api GET "/orgs?limit=99999" | jq -c "{total}"
echo "--- % is a literal, not a wildcard"
api GET "/orgs?query=%25" | jq -c "{total}"'
```

- [ ] 都返回 `{total, <资源>}`（通知渠道的数组键是 `channels`）
- [ ] `query=ADMIN` 能搜到 `Admin`（用 `LOWER(col) LIKE`，测试默认跑 SQLite 没有 `ILIKE`）
- [ ] `limit` 超过 500 不报错
- [ ] `query=%` 当字面量处理，`total=0`（`%` / `_` / `\` 会转义）
- [ ] 组织列表、用户列表的排序在服务端：点表头发 `order=<列>` / `order=-<列>`，只有白名单里的列有排序箭头（组织列表的「描述」「属主」没有）；`order=name;drop` 这类值 200、按默认顺序
  ```bash
  ssh work-01 'source /root/alarmtest-env.sh >/dev/null 2>&1
  api GET "/orgs?limit=3&order=-name" | jq -c "[.orgs[].name]"; api GET "/orgs?limit=3&order=name" | jq -c "[.orgs[].name]"
  api GET "/regions?limit=3&order=-created_at" | jq -c "{total}"; api GET "/orgs?limit=3&order=bogus" | jq -c "{total}"'
  ```
  > **2026-09-30 已修（K-2 / D16），2026-10-01 已部署，按本条判定**。原记录：网关列表没有 `order`，两页的列都有排序箭头、只排当前这一页

**切换器不走分页**：组织切换器用 `/auth/me/orgs`，区域切换器和组织详情页的成员列表显式传 `limit=500`（每个页面都能看到 `regions?limit=500`）。

- [ ] 顶栏的组织 / 区域切换器把所有选项都列出来了

### 3.3 多个过滤条件是 AND

```bash
ssh work-01 'source /root/alarmtest-env.sh >/dev/null 2>&1
V=$(api GET "/vpcs?query=aax&limit=5" | jq -r ".vpcs[0].id")
api GET "/security_groups?vpc_id=$V&limit=50" | jq -c "{total, names:[.security_groups[].name]}"   # 1: aax-native
api GET "/security_groups?vpc_id=$V&query=ibmx&limit=50" | jq -c "{total}"                        # 0
api GET "/vpn_gateways?vpc_id=$V&limit=50" | jq -c "{total, names:[.vpn_gateways[].name]}"        # 1: aax-gw'
```

- [ ] 安全组 `vpc_id` + `query` 同时生效（两个条件是 AND，不是后者覆盖前者）
- [ ] 子网列表的过滤只有 `query`、`group_id`、`ipgroup_type`，**没有 VPC 过滤**（`api/src/apis/subnet.go`，页面上也没有 VPC 筛选）。原检查项「子网名称搜索 + VPC 过滤」作废（2026-09-28 核对）
- [ ] IP 组 `dic_id` / `type` 过滤：work-01 上 `GET /ip_groups`、`/dictionaries` 都是 0 条，**当前 SKIP**；有数据时带两个过滤各查一次，结果是两者的交集

### 历史缺陷回归点

| 现象 | 根因 |
|---|---|
| 组织列表接口 500，报 `COUNT(organizations.*)` 语法错 | GORM 计数复用了带 `Select("organizations.*")` 的语句。修法见 `countAndPage`：计数要 `Session` 复制一份，且计数前清掉 `Select` |
| ~~备份列表的搜索词失效、带 `volume_id` 时拼出无参数的 `owner = ?`~~ | **已废弃**：备份随 WDS 于 2026-09-22 整体删除 |
| IP 组列表的 `dic_id` / `type` 过滤从未生效 | 搜索条件被 `query, args := GetOrgFilter()` 整个覆盖 |
| 字典 / 子网 / IP 组的多个过滤互相覆盖（后者盖前者） | 现在应是 AND 关系 |
| 组织 / 用户列表的表头排序只排当前页（K-2 / D16，2026-09-28；2026-09-30 修，2026-10-01 已部署） | 网关列表接口没有 `order`，前端用 `DataTable` 本地排序。判定：UI-03.2 排序一项 |
| 下拉 / 选择器超过 50 条缺项（FAIL-8，2026-09-28；2026-09-30 修，2026-10-01 已部署） | 取列表不带 `limit`，后端默认 50。判定：UI-02 的 `noLimitGET` 为空 |

---

## UI-04 表格组件（DataTable）与分页条

**P1**

`DataTable` 是**泛型组件**（`generic="T extends Record<string, any>"`），`#cell-*` 插槽里的 `row` 是具体接口类型。

- [ ] 改完 `DataTable.vue` 后 `BUILD-01` 类型检查仍为 0 错误（泛型推断断了会在这里暴露）
- [ ] 带下拉菜单 / 悬浮卡片的页面传了 `allowOverflow`，内容没被容器裁掉：云服务器列表（IP 悬浮、「更多」菜单）、VPC 列表（子网数悬浮）、计算节点列表、VPN 网关详情的站点连接表
- [ ] 可展开行（`expandable`）一次只展开一行：告警事件、VM 告警规则、VPN 网关详情的站点连接
- [ ] 加载态 / 空态 / 错误态 + 重试按钮都在；空态是图标 + 文案（不再只是一行灰字）；带搜索词 / 筛选时空态是「无结果」而不是「还没有…」（放置组列表可用搜索框验证）
- [ ] `hideBelow` 只用 `1600 / 1440 / 1280 / 1024 / 768` 这几档（`grep -rhoE "hideBelow: [0-9]+" web/src --include=*.vue | sort | uniq -c`，2026-09-28：1024×4、1280×7、1440×1、1600×1）

**窄屏**（本机脚本把视口调到 1440 / 1280 / 1024，量 `document.documentElement.scrollWidth - clientWidth` 与 `.table-container` 的溢出；2026-09-28 实测）：

- [ ] 1440 宽下所有列表页、详情页都不横向滚动
- [ ] 1280 / 1024 下**没开 `allowOverflow`** 的表格放不下时在 `.table-container` 里横向滚动（VPN 网关列表 1280 +115 / 1024 +215、子网 1024 +147、告警事件 1024 +55 等），页面本身不滚动 —— 可以接受
- [ ] **开了 `allowOverflow`** 的表格不能把操作列挤出屏幕：云服务器、VPC 列表 1280 / 1024 下操作列都在屏幕内；计算节点列表 1280 下超出 109px、1024 下超出 285px（整个内容区横向滚动，K-3）
- [ ] VPN 网关详情「站点连接」表格在 1920 / 1600 / 1440 / 1366 / 1280 / 1024 都不横向滚动，操作列在屏幕内；1600 以下没有「流量」列、1440 以下没有「远端网段」列、1280 以下「路由方式」挪到名称下面（1024 宽只剩 名称 / 隧道 / 状态 / 操作）；隧道行是状态圆点（`StatusBadge dotOnly`，悬停显示状态文字）。详细检查见 `TC-17 VPN-07`

分页条（`PaginationBar`）：

- [ ] 每页条数选择器有 10 / 20 / 50 / 100，默认 20（`tc12-modals.js` 第一行输出）
- [ ] 只有一页时仍显示（可以调大每页条数），`total = 0` 时不显示
- [ ] 当前页被删空后退回到最后一页（`useListQuery`；要删数据，只在建资源的用例里顺带看）

**静默刷新不能卡住用户发起的加载**（`useListQuery.load(true)`）：脚本 `tc12-silent.js`（附录）在浏览器里把迁移列表第一条改写成 `in_progress`（触发每 5 秒的静默轮询）、并把之后的列表请求都延迟 6 秒，然后点「状态」表头：

```bash
cd "$TC12" && node tc12-silent.js   # expect: spinner=0 rows=20 sortedBy=状态 ▲ pageerrors=0
```

- [ ] 15 秒后表格里没有加载圈、行数正常、排序箭头在「状态」上

### 历史缺陷回归点

| 现象 | 根因 | 判定 |
|---|---|---|
| 迁移列表翻页 / 排序时加载圈一直转（2026-09-21） | 用户发起的加载进行中时，5 秒一次的静默刷新把它作废了（代号 +1），那次加载的 `finally` 再也清不掉 `loading` | `tc12-silent.js` 输出 `spinner=0` |
| 宽表格在窄屏把操作列挤出屏幕 | 开了 `allowOverflow` 的表格不会在容器里滚动，而是撑宽整个内容区 | 上面的窄屏检查；详情页宽表格按 1440 宽下约 1110px 控制列宽、次要列用 `hideBelow` |
| 计算节点列表在 1280 宽下操作列出屏 109px、1024 宽下 285px（K-3，2026-09-28；2026-09-30 修，2026-10-01 已部署） | 同上，`HypervisorList.vue` 没有 `hideBelow` | 1280 / 1024 宽下计算节点列表的操作列在屏内、页面不横向滚动 |

---

## UI-05 弹窗组件（BaseModal / DeleteModal）

**P1**

- [ ] ESC 能关闭；点遮罩能关闭（`tc12-modals.js` 的 `esc=closed`）
- [ ] 打开时 body 不滚动，关闭后恢复（`body=hidden->-`）
- [ ] 打开后焦点在弹窗内：落在带 `autofocus` 的元素，否则**第一个可见的输入框 / 下拉 / 文本域**；**永远不落在按钮上**（原先落在右上角关闭按钮，打开后直接回车就关掉弹窗，K-4）。第一个表单项是复选框、单选框或文件框时（例如维护弹窗），焦点给**弹窗本身**（`.modal-content`，没有焦点框）
- [ ] 传了 `form` 的弹窗，**打开后没动过任何输入就按回车 → 不提交**（没有写请求）；改过任一输入（`input` / `change` 事件）之后在输入框里回车才触发提交；在复选框 / 单选框上按回车**永远不提交**；点提交按钮不受影响
  > **2026-09-30 已修（`web/src/components/modals/BaseModal.vue` 的 `focusInitial` / `onEnterKey`），2026-10-01 已部署，部署后按这两项判定**。原先的规则「传了 `form` 的弹窗在输入框里回车触发提交」会把预填值或默认值直接提交出去（例如调整配置什么都没改也会关机再开机）
- [ ] 调整配置弹窗（云服务器详情 / 列表）规格没改时「确定」按钮禁用、回车也不提交
  > **2026-09-30 已修（`InstanceDetail.vue` 的 `resizeUnchanged`），2026-10-01 已部署**
- [ ] 创建云服务器弹窗在列表还在加载时焦点先给弹窗本身，加载完交给「主机名」输入框（用户已经把焦点挪走时不抢）
- [ ] `loading` 期间关闭按钮禁用，ESC 与点遮罩不生效（要一次真实提交，只在建资源的用例里顺带看）
- [ ] 删除确认（`DeleteModal`）显示资源名与 ID；不叫「删除」的场景用 `confirmLabel`，不可删时确认按钮禁用并说明原因（例如：存储池列表里内置池的删除按钮提示「内置存储池不能删除」，安全组列表里默认组提示「默认安全组不能删除」，放置组有成员时删除禁用）
- [ ] 所有弹窗都基于 `BaseModal`：`grep -rl 'class="modal-overlay"' web/src --include=*.vue` 只有 `BaseModal.vue`
  > **2026-09-30 已修（`SecurityRuleModal.vue` 改用 `BaseModal`；它不传 `form`、按钮 `type=button`，回车不保存，见 `TC-05` SG-03），2026-10-01 已部署，按本条判定**（本地代码上这条 grep 已经只剩 `BaseModal.vue`）。原记录（K-5）：安全组规则弹窗自写遮罩，没有 ESC 关闭、背景滚动锁和自动聚焦

**长表单弹窗的报错放在底栏按钮旁**（原先在长表单最底部，用户点「创建」收到 409 却以为没反应）：

- [ ] 报错在底栏（`.footer-error`）：创建云服务器、创建 / 编辑放置组、VPN 的创建网关 / 连接 / 客户端 / 加公网地址，以及（2026-09-30 起）负载均衡创建 / 编辑、镜像创建、子网创建 / 编辑、安全组规则
  > 后四类 **2026-09-30 已修（`LoadBalancers.vue`、`Images.vue`、`SubnetList.vue`、`SecurityRuleModal.vue`），2026-10-01 已部署，按本条判定**。原记录（K-6）：报错仍在正文底部
- [ ] 放置组重名：报错在底栏、弹窗不关，文案是本地化的「同名的放置组已存在。」（不是后端原文「Code 111702: …」）
  > **2026-09-30 已修（`web/src/utils/error.ts` 按 clapi 错误码本地化，只收「消息里没有具体信息」的码：111702、111905、121012、131004（仅「没有足够空闲地址」那一种）、131307、131308、132005、132025、132033、141007、141008、151001、161006、171011），2026-10-01 已部署，按本条判定**。带具体数字 / 名称的消息（如「Only N addresses can be allocated」）仍显示原文
- [ ] 子网名称的错误提示是「…长度 2 到 64 个字符」（子网名上限 64，原先复用了主机名的 2–32 文案）；VPC 列表里新建子网同样按 64 校验

### 历史缺陷回归点

| 现象 | 根因 | 判定 |
|---|---|---|
| 弹窗打开后直接回车就被关掉（K-4 / D16，2026-09-28；2026-09-30 修，2026-10-01 已部署） | `BaseModal` 按文档顺序取第一个可聚焦元素，头部关闭按钮最先 | UI-05 焦点一项：`focus` 不是 `BUTTON` |
| 打开表单弹窗什么都没改、回车就把预填值 / 默认值提交出去，调整配置会白白关机再开机（2026-09-30 修，2026-10-01 已部署） | 表单弹窗的隐式提交不看有没有改动；维护弹窗第一个控件是复选框，Chromium 里在复选框上回车会提交表单 | UI-02「直接按回车不发写请求」、UI-05 回车一项 |
| 安全组规则弹窗没有 ESC、不锁背景滚动（K-5，2026-09-28；2026-09-30 修，2026-10-01 已部署） | 自写 `.modal-overlay` | UI-05 `grep modal-overlay` 只剩 `BaseModal.vue` |
| 负载均衡 / 镜像 / 子网的长表单报错在正文底部，点「创建」收到错误却以为没反应（K-6；2026-09-30 修，2026-10-01 已部署） | 报错放在表单末尾 | UI-05 底栏一项 |
| 放置组等重名错误显示后端英文原文（2026-09-28 观察；2026-09-30 修，2026-10-01 已部署） | 前端直接显示 `error_message` | UI-05 放置组重名一项 |

### scoped 样式的坑

页面的 scoped CSS 只对自己模板里的元素生效。把结构搬进子组件后，页面里针对 `.modal-content` / `.page-header` / `.search-box` 的规则会**静默失效**（成为死代码）。子组件想给插槽内容排版只能用 `:deep()`。

- [ ] 重构过的页面外观与重构前一致（必要时做像素对比，见 UI-06）

---

## UI-06 视觉回归（像素对比）

**P1** — 仅当改动是**纯外观重构**（换令牌、抽组件、调样式）时

脚本 `tc12-shots.js` + `tc12-diff.js`（附录；本机和 work-01 都没有 ImageMagick，用 `pixelmatch`）：

```bash
cd "$TC12"
# before the change (git stash / checkout the old commit, dev server hot-reloads)
OUT=shots-before node tc12-shots.js
# after the change
OUT=shots-after node tc12-shots.js
node tc12-diff.js shots-before shots-after     # writes *.diff.png next to the changed shots
```

- [ ] 预期不变的页面差异像素为 0（同一代码连拍两次实测为 0；「最近动态」的相对时间、加载圈会造成与样式无关的差异，先用 `PAGES=` 缩小范围再看 diff 图）

### 历史缺陷回归点

| 现象 | 根因 | 判定 |
|---|---|---|
| 把 `#64748b` 换成 `var(--text-secondary)` 后颜色变了 | `--text-secondary` 的值是 `#475569`，不是同一个色。**颜色令牌化必须逐个核对令牌的实际值等于原来的十六进制值**，不能按语义猜 | 颜色令牌替换后像素差异为 0 |
| 系统设置页「状态异常」的红字和窄屏 tab 栏布局都不生效（2026-09-21） | 去重样式时删掉了 `.status-text.nok, .text-error {…}` 里的 `.text-error`，留下 `.status-text.nok,` 与后面的 `@media (max-width: 720px)` 连在一起，生产构建报 `css-syntax-error` | `BUILD-04` 构建输出没有 `css-syntax-error`；视口 700 宽下系统设置页 tab 栏正常换行 |
| `Home.vue` 的 `--primary-color` 失效 | `--primary-color: var(--primary-color)` 自引用循环 | 首页主色按钮有颜色 |

### 样式令牌约定

- z-index 一律用 `--z-*`：dropdown-backdrop 90 / dropdown 100 / sticky 200 / fixed 300（顶栏）/ sidebar 320 / modal-backdrop 400 / modal 500 / modal-nested 550 / tooltip 600 / toast 700。组件内部的局部层叠（控制台遮罩、登录页装饰）保持 0/1/2，**不要动**。
- 断点从 480 / 768 / 1024 / 1280 里挑。存量的 640 / 700 / 720 / 900 / 1200 是按具体组件调出来的，**不要强行统一**。
- `.vue` 里没有硬编码颜色（只剩 `Home.vue` 局部调色板的令牌定义行）；canvas / chart.js / xterm.js 要真实颜色字符串，用 `utils/cssVar.ts` 的 `cssVar('--token')`，不能写 `var()`。

---

## UI-07 多窗口登录态

**P1** · 脚本 work-01 `/root/console-test/token-sync-e2e.js`，拷到本机改两处后跑 · **会真实写**：打开两次文本控制台（节点上起一次性 socat 桥，关窗或 60 秒无人连接即清理，不改虚拟机）、真实调用一次 `switch-org`（只吊销本浏览器的 token）

cpgateway 签发新 token（登录、switch-org、switch-region）时会**吊销旧 token**，而未勾「记住我」时 token 在 sessionStorage、每个窗口一份。

```bash
cd "$TC12" && scp -q work-01:/root/console-test/token-sync-e2e.js .
# BASE is hard-coded to the work-01 nginx; the instance action button is btn-secondary since the 09-21 detail page redesign
sed -i "s#const BASE = 'https://127.0.0.1'#const BASE = 'http://127.0.0.1:5173'#; s#.action-dropdown > button.btn-primary#.action-dropdown > button#" token-sync-e2e.js
# VM3: any running Linux instance whose serial console is a login terminal (e.g. net-c); only a console session is opened
VM3=<云服务器 uuid> node token-sync-e2e.js 2>&1 | tail -20
```

- [ ] 登录后与两次页面加载，`switch-org` 调用次数为 **0**
- [ ] 主窗口刷新后，另开的控制台窗口仍能正常请求（不被踢回登录）
- [ ] 真实切换组织后，旧 token 401，其他窗口经 `BroadcastChannel('cloudland-auth')` 收到新 token 并自动重连
- [ ] 其他用户的 token 被忽略（只有已登录且 `sub` 相同的窗口才采纳）

### 历史缺陷回归点

| 现象 | 根因 |
|---|---|
| 主窗口刷新或新开管理页，其他窗口（控制台、别的标签页）下一次请求 401 被踢回登录 | `Layout.vue` 曾经每次加载都调 `switch-org`。现在只在 token 的 `org_id` 不是目标组织时才调 |
| 脚本卡在「点操作按钮」超时（2026-09-28 核对） | 09-21 详情页改版后操作按钮是 `btn btn-secondary btn-sm`，脚本还按 `.btn-primary` 找（脚本问题，不是界面问题） |

---

## UI-08 浏览器存储

**P1**

- [ ] 所有 key 都来自 `utils/storage.ts` 的 `STORAGE_KEYS`，代码里没有字面量
  ```bash
  cd web/src && grep -rnE "(local|session)Storage\.(get|set|remove)Item\(['\"]" --include=*.ts --include=*.vue . | grep -v utils/storage.ts
  # expect: no output
  ```
  例外：`router/index.ts` 的 `cloudland_chunk_reload`（sessionStorage，部署后旧标签页加载不到新分块时防止反复刷新，跳转完成即删，与登录态无关）
- [ ] 退出登录（菜单）与 401 强制登出（`clearAuthStorage()`）后，两种存储里的 **6 个登录态键**全部被清空：`cloudland_token`、`cloudland_user`、`cloudland_remember`、`cloudland_org_id`、`cloudland_region_uuid`、`cloudland_region_id`（只清 token 会把组织 / 区域留给下一个账号）
  ```js
  // in the browser console or Playwright after logout
  Object.keys(localStorage).concat(Object.keys(sessionStorage)).filter(k => k.startsWith('cloudland'))
  // allowed to remain: cloudland_language (chosen UI language), cloudland_login_attempts (failed logins, drives the captcha)
  ```

---

## UI-09 链路追踪

**P2** · 脚本 work-01 `/root/console-test/tracing-check.js`，拷到本机把 `BASE` 从 `http://127.0.0.1:5190` 改成 `http://127.0.0.1:5173`（它不拦截 `telemetry/traces`，这是本用例要看的）

- [ ] 前端仍在经 `POST /api/v1/telemetry/traces` 上报 span（5173 经代理发到 work-01 的 cpgateway），响应 2xx
- [ ] 错误 toast 上显示 `Trace ID: …`（`ToastContainer.vue`）

---

## UI-10 共享组件与全局样式约定（静态检查）

**P1** · 新增（2026-09-28）· 本机在仓库根目录跑，约 1 min

```bash
cd web/src
# 1. global classes must not be redefined in scoped styles (a scoped copy wins over index.css)
for sel in title-bar title-info resource-title icon-btn-table row-actions btn-danger-outline badge-secondary spinning; do
  echo ".$sel: $(grep -rlE "^\s*\.$sel\b[^{]*\{|,\s*\.$sel\b[^{]*\{" --include=*.vue . | tr '\n' ' ')"
done
grep -rl '@keyframes spin\b' --include=*.vue .
# 2. every detail page uses the shared title area and back button
for f in views/dashboard/*Detail.vue; do grep -q 'class="title-bar"' $f && grep -q 'class="detail-header"' $f || echo "MISSING $f"; done
# 3. one icon per action
grep -rnwE "Edit|Edit2|Edit3|SquarePen" --include=*.vue . | grep -v "<!--\|//"
grep -rn "Settings2\|⏸" --include=*.vue .
# 4. t('a') || ... never falls back (vue-i18n returns the key itself)
grep -rnE "t\('[^']+'\) *\|\|" --include=*.vue --include=*.ts . | wc -l
# 5. English labels in TS constants are not caught by i18n:check
grep -rnE "label: '[A-Z][a-z]" --include=*.ts . | grep -v "^./locales"
```

2026-09-28 基线与检查项：

- [ ] 1：全部为空，只有 `.badge-secondary` 出现在 `AlarmEvents.vue`（那组实色「通知类型」标签里的实心灰，有意保留）；`@keyframes spin` 无输出
- [ ] 2：无 `MISSING`（17 个详情页都有 `.title-bar` 与 `.detail-header`）
- [ ] 3：编辑一律 `Pencil`、删除 `Trash2`、启停 `Power`、暂停 `Pause`、挂载 / 卸载 `Link` / `Unlink`、扩容 `Maximize2`、图形控制台 `Monitor`、文本控制台 `SquareTerminal`；第一条 grep 只剩 3 行英文单词（`VpnConnectionModal.vue:37` 的 JSDoc、`Layout.vue:190` 的 `console.log('Edit User clicked')`、`VpnGatewayDetail.vue` 的 CSS 注释），没有 lucide 图标导入；`Settings2` 只出现在系统设置页「常规」标签的图标；没有 `⏸`
- [ ] 4：计数 **12**，不能增加（存量在 FloatingIPList、HypervisorDetail、OrgDetail、OrgList、SubnetList，主键都存在，回退永不生效，不影响显示）
- [ ] 5：只有 `api/vmAlarmRules.ts` 的 `VM_RULE_TYPES`（`Memory` / `Bandwidth`），且模板里用的是 `t('dashboard.vmAlarmRules.ruleTypes.<value>')`，没有渲染 `.label`
- [ ] `npm run i18n:check` 通过（2026-09-28：三个语言包各 2419 键，代码用到的 1535 键都存在），但它**抓不到** TS 常量里的英文（上面第 5 条）
- [ ] 表格行内操作按钮是 `.row-actions` > `.icon-btn-table`（32×32、只放图标、名称在 `title`，删除类 `.icon-danger` 静止灰悬停红、开关开启时 `.is-active` 绿）；不可用时按钮禁用、`title` 写原因（云硬盘「系统盘不能卸载」、存储池「内置存储池不能删除」、安全组「默认安全组不能删除」）
- [ ] `StatusBadge` 的 `label` 必填；`dotOnly` 目前只用在 VPN 站点连接的隧道行，悬停 `title` 显示状态文字
- [ ] 详情页标题区不是卡片（没有白底 / 渐变 / 左侧蓝条），状态标签紧跟名字，操作按钮 `btn-secondary btn-sm`，删除用 `.btn-danger-outline`；`InfoRow` 标签固定一列（≥1280 宽 144px、以下 112px），值左对齐紧跟

### 历史缺陷回归点

| 现象 | 根因 | 判定 |
|---|---|---|
| 告警 / VM 告警规则 / 通知渠道的「启用」状态一直是灰色（2026-09-21） | 页面里 scoped 的 `.icon-btn-table` 副本把 `text-success` 压成了灰 | 上面第 1 条为空；启用的开关按钮是绿色 |
| VM 告警规则页满屏紫色标签 | `.badge-secondary` 没有全局定义，6 个页面各写一份、三种颜色 | 同上 |
| 节点告警操作列按钮竖排 | `.actions-cell` 从未定义 | 节点告警列表操作按钮横排 |
| Windows 下详情页标签栏右端出现竖向滚动条 | `DetailTabs` 溢出 1px；底线改成了内阴影 | 云服务器 / VPN 网关详情的标签栏没有滚动条 |
| 节点告警规则类型在中文界面显示英文（2026-09-21） | `RULE_TYPES` 的文案写在 TS 常量里，`i18n:check` 只查模板与含中文的字符串字面量 | 上面第 5 条；节点告警列表「规则类型」列是中文 |
| 创建云服务器页面直接显示 `marketplace.cancel` | 以为是无用键删掉了；vue-i18n 缺键时显示键名、`t('a') \|\| t('b')` 不会回退 | UI-01 无 `KEYLEAK`；`BUILD-02` |

---

## UI-11 新页面冒烟（存储池 / VPN 网关 / 放置组 / 迁移 / 操作动态 / 控制台）

**P1** · 新增（2026-09-28）· 在 5173 上手工看，约 5 min；深入的检查在各自的用例里

UI-01 的脚本已自动覆盖这些页面的渲染与首次请求，这里补脚本不看的内容：

- [ ] **存储池**（系统管理员）：列表 2 行（local 内置、local-hdd），内置池删除按钮禁用；详情有标题区与各节点的池状态；计算节点详情「存储」标签列出磁盘与池 —— 细节见 `TC-16 STO-11`
- [ ] **VPN 网关**：列表 4 行，首次进入就有 `/vpn_gateways` 请求；详情进入时**不**请求 `/traffic`，切到「监控」才请求（1 小时范围 `step=60s`）；「站点连接」操作列「对端配置」打开弹窗，标题「对端配置 · <连接名>」，密钥打码；窄屏表格见 UI-04；单节点网关 / 负载均衡标「单节点」 —— 细节见 `TC-17 VPN-07`
- [ ] **放置组**（**只在 5173**）：侧边栏「计算」分组里有「放置组」；列表有可用区筛选、服务端分页 / 搜索 / 排序；创建弹窗两张策略卡片，切到集中时「严格」关、切回分散时开；编辑只能改名称和描述；有成员时删除禁用；详情页（要先有组）有策略、分布、成员表 —— 写操作见 `TC-18` 的「界面」一节
- [ ] **迁移**：列表有进度列，只有存在进行中的迁移时才每 5 秒刷新；详情页阶段与进度条；终态 `rollback` 显示「已回滚」（FAIL-9，2026-09-30 修，2026-10-01 已部署）；按「进度」倒序时进度为空的旧记录排在最后（K-9，2026-09-30 修，2026-10-01 已部署）
- [ ] **操作动态**（`/dashboard/activities`，概览「最近动态」卡片进入）：顶部「← 概览」返回；时间范围 / 资源类型 / 结果筛选；「加载更多」游标翻页、**没有页码**；类型筛选里有「放置组」
- [ ] **控制台三页**：图形控制台、文本控制台、节点终端在窄屏打开虚拟键盘时工具栏不被挤出屏幕（`.console-main` 的 `min-height: 0`）；节点终端在需要密码时先显示密码框 —— 真实连接与键盘见 `TC-09`

---

## 已知界面问题（2026-09-28 核对；2026-09-30 已全部修改，2026-10-01 已部署）

下表各项 **2026-09-30 都已在本地代码里修掉、2026-10-01 已部署**（界面在本机 5173 上就能验证修复后的样子，不用等部署；work-01 的 nginx 界面要等重建）。在 5173 上再出现就是新问题；在 work-01 公网界面上出现、且 nginx 还没重建，仍按已知问题记。

| 编号 | 现象 | 定位 | 来源 | 2026-09-30 修改 |
|---|---|---|---|---|
| FAIL-7 | 云服务器列表「规格」列只有「2C / 2G」，没有规格名 | 接口返回 `flavor: ""`（创建时不落 `flavor_id`）；`InstanceList.vue` 只在有名字时显示第二行 | `runs/2026-09-28-7adc07d0+补测.md` | 后端记 `flavor_id`（`services/instance.go`），**存量实例不回填**，只有部署后新建的有规格名（`TC-02` VM-08） |
| FAIL-8 | 下拉数据源不带 `limit`，超过 50 条会缺项 | `CreateInstanceModal.vue` 8 个；另有 `FloatingIPList.vue`、`SubnetList.vue`、`SecurityGroups.vue`、`LoadBalancers.vue`、`MigrationList.vue`、`HypervisorList.vue`、`HypervisorDetail.vue`、`VMAlarmRules.vue` | 同上（范围 2026-09-28 核对补全） | 一律 `OPTION_LIST_LIMIT`（500），UI-02 的 `noLimitGET` 应为空 |
| FAIL-9 | 迁移列表把终态 `rollback` 显示成红色「回滚中」，看起来像还在进行 | `locales/*.ts` 的 `migrationStatus.rollback` | 同上 | 改成「已回滚」 |
| — | 放置组重名时底栏显示后端原文「Code 111702: …」 | `PlacementGroupList.vue` 直接用 `errorMessage(err)` | 同上「其他观察」 | `utils/error.ts` 按错误码本地化（UI-05） |
| K-1 | 弹性 IP 列表的名称列表头是「用户名」 | `FloatingIPList.vue` 用了 `dashboard.table.userName` | 本次核对 | 改为 `nameId`（「名称 / ID」） |
| K-2 | 组织列表、用户列表的排序只排当前这一页 | 网关列表接口没有 `order` 参数 | 本次核对 | cpgateway 加 `order`、前端改服务端排序（UI-03.2） |
| K-3 | 计算节点列表在 1280 宽下操作列出屏（内容区多 109px，1024 宽多 285px） | `HypervisorList.vue` 开了 `allowOverflow`、没有 `hideBelow` | 本次核对 | 「主机 IP」「可用区」1440 以下隐藏、「实例数」1280 以下隐藏，1280 以下用量列收窄；1280 / 1024 宽复查操作列在屏内（UI-04 窄屏检查） |
| K-4 | 弹窗打开后焦点落在右上角关闭按钮，直接回车会关掉弹窗 | `BaseModal.vue` 按文档顺序取第一个可聚焦元素 | 本次核对 | 焦点给第一个输入项、从不给按钮；未改动时回车不提交（UI-05） |
| K-5 | 安全组规则弹窗不能用 ESC 关闭，打开时背景仍可滚动 | `SecurityRuleModal.vue` 自写 `.modal-overlay` | 本次核对 | 改用 `BaseModal`（UI-05、`TC-05` SG-03） |
| K-6 | 部分长表单弹窗的报错仍在正文底部 | `LoadBalancers.vue`、`Images.vue`、`SubnetList.vue` | CLAUDE.md「长表单弹窗的报错放底栏」 | 挪到底栏 `.footer-error`（UI-05） |
| K-7 | 返回按钮两种写法 | `StoragePoolDetail.vue`、`PlacementGroupDetail.vue` 是文字「← 返回」 | 本次核对 | 统一为 `ArrowLeft` 图标 +「返回」 |
| K-8 | 通知渠道弹窗标题「创建 通知渠道」中间多一个空格 | `NotificationChannels.vue` 用模板字符串拼两段文案 | 本次核对 | 新文案键 `createNotificationChannel` / `editNotificationChannel` |
| K-9 | 迁移按「进度」倒序时，进度为空的旧记录排在最前 | 早于进度功能的记录 `progress` 为 NULL，PostgreSQL 倒序默认 NULLS FIRST | 本次核对 | 后端排序加 `NULLS LAST`（`services/migration.go`，要部署 clapi） |

---

## 清理

本文件的脚本只读（UI-07 除外），不建资源。

- UI-07 打开的文本控制台会话：关掉浏览器即结束，节点上 60 秒内清理端口、iptables 规则与 socat（检查方法见 `TC-09`）
- 本机：`$TC12` 可以整个删掉；dev server 如果是本轮启动的就停掉，原本在跑的不要动（别的会话可能在用）

---

## 附：脚本

在 `$TC12` 下用 `cat > <文件> <<'EOF' … EOF` 写入（单引号 heredoc，内容不展开）。都以 admin 登录、在 context 上兜底拦截写请求；`BASE` 默认 `http://127.0.0.1:5173`，部署后冒烟时 `BASE=https://169.61.110.83`。

### tc12-smoke.js（UI-01）

```js
// TC-12 UI-01: read-only render smoke. Every non-GET /api/ request except /api/v1/auth/ is aborted and listed.
const { chromium } = require('playwright')
const BASE = process.env.BASE || 'http://127.0.0.1:5173'
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const LISTS = ['', 'activities', 'instances', 'placement-groups', 'volumes', 'images', 'flavors', 'keys', 'vpcs',
    'subnets', 'floating-ips', 'security-groups', 'load-balancers', 'vpn-gateways', 'users', 'orgs', 'regions',
    'zones', 'hypervisors', 'storage-pools', 'migrations', 'alarms', 'vm-alarm-rules', 'notification-channels',
    'alarm-events', 'settings']
// list pages whose first row links to a detail page
const DETAIL = ['instances', 'placement-groups', 'volumes', 'images', 'vpcs', 'subnets', 'floating-ips',
    'security-groups', 'load-balancers', 'vpn-gateways', 'users', 'orgs', 'zones', 'hypervisors', 'storage-pools',
    'migrations', 'alarms']
;(async () => {
    const browser = await chromium.launch()
    const ctx = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'zh-CN', viewport: { width: 1500, height: 950 } })
    const blocked = new Set(), gets = [], bad = [], errors = []
    await ctx.route((u) => u.pathname.startsWith('/api/'), (route) => {
        const req = route.request(), p = new URL(req.url()).pathname
        if (req.method() === 'GET' || p.startsWith('/api/v1/auth/')) return route.continue()
        blocked.add(`${req.method()} ${p}`)
        return route.abort()
    })
    const page = await ctx.newPage()
    page.on('pageerror', (e) => errors.push(e.message))
    page.on('request', (r) => { const u = new URL(r.url()); if (r.method() === 'GET' && u.pathname.startsWith('/api/v1/')) gets.push(u.pathname + u.search) })
    page.on('response', (r) => { const u = new URL(r.url()); if (u.pathname.startsWith('/api/') && r.status() >= 400) bad.push(`${r.status()} ${u.pathname}`) })
    await page.goto(`${BASE}/login`)
    await page.fill('#email', 'admin')
    await page.fill('#password', process.env.ADMIN_PASSWORD)
    await page.click('button[type=submit]')
    await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 30000 })
    const note = (before) => `${bad.length ? ' BAD:' + [...new Set(bad)].join(',') : ''}${errors.length > before ? ' ERROR:' + errors[before].slice(0, 80) : ''}`

    const details = {}
    for (const p of LISTS) {
        const before = errors.length
        gets.length = 0; bad.length = 0
        await page.goto(`${BASE}/dashboard${p ? '/' + p : ''}`)
        await page.waitForLoadState('networkidle'); await sleep(800)
        // DataTable renders its loading / empty / error state as a row too: do not count it
        const rows = await page.locator('.data-table > tbody > tr:not(:has(.table-state))').count()
        const empty = await page.locator('.data-table > tbody > tr:has(.table-state)').count()
        const cards = await page.locator('.card').count()
        const path = new URL(page.url()).pathname
        const leak = await page.evaluate(() => (document.body.innerText.match(/\b(dashboard|messages|actions|storage|quota|specs)\.[a-zA-Z_.]+\b/g) || []).slice(0, 3))
        const list = [...new Set(gets.filter((g) => /offset=|limit=|order=/.test(g) && !g.startsWith('/api/v1/regions')).map((g) => g.replace(/&?region=[^&]*/, '')))]
        const sortable = (await page.locator('.data-table th.sortable').allInnerTexts()).map((s) => s.replace(/[▲▼\s]+$/, '').trim())
        console.log(`${(p || 'overview').padEnd(22)} path=${path} rows=${rows}${empty ? ' (state row)' : ''} cards=${cards}${leak.length ? ' KEYLEAK:' + leak.join(',') : ''}${note(before)}`)
        console.log(`    GET ${list.slice(0, 2).join(' | ') || '-'}    sortable: ${sortable.join(', ') || '-'}`)
        if (DETAIL.includes(p)) {
            const href = await page.locator(`.data-table a[href^="/dashboard/${p}/"]`).first().getAttribute('href', { timeout: 2000 }).catch(() => null)
            if (href) details[p] = href
        }
    }

    console.log('--- detail pages')
    for (const [p, href] of Object.entries(details)) {
        const before = errors.length
        bad.length = 0
        await page.goto(`${BASE}${href}`)
        await page.waitForLoadState('networkidle'); await sleep(800)
        const titleBar = await page.locator('.title-bar').count()
        const back = (await page.locator('.detail-header button').allInnerTexts()).map((s) => s.trim())
        const tabs = (await page.locator('.tab-btn').allInnerTexts()).map((s) => s.trim().replace(/\s+/g, ' '))
        const hscroll = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
        console.log(`${p.padEnd(18)} title-bar=${titleBar} back=${JSON.stringify(back)} hscroll=${hscroll} tabs=${JSON.stringify(tabs)}${note(before)}`)
    }

    // Console pages in the same tab: a new page would not carry the sessionStorage token
    console.log('--- console pages (POST .../console is blocked: expect the error state, no pageerror)')
    const inst = details.instances?.split('/').pop(), hyper = details.hypervisors?.split('/').pop()
    for (const u of [`/console/${inst}`, `/serial-console/${inst}`, `/host-console/${hyper}`]) {
        const before = errors.length
        await page.goto(`${BASE}${u}`)
        await page.waitForLoadState('networkidle'); await sleep(2500)
        const minH = await page.evaluate(() => { const el = document.querySelector('.console-main'); return el ? getComputedStyle(el).minHeight : '-' })
        const text = (await page.locator('body').innerText()).replace(/\s+/g, ' ').slice(0, 90)
        console.log(`${u.split('/')[1].padEnd(16)} path=${new URL(page.url()).pathname.split('/')[1]} console-main.min-height=${minH} :: ${text}${note(before)}`)
    }

    console.log('\nblocked writes:', JSON.stringify([...blocked]))
    console.log('pageerrors:', errors.length ? JSON.stringify(errors.slice(0, 5)) : 'none')
    await browser.close()
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1) })
```

### tc12-modals.js（UI-02、UI-05）

```js
// TC-12 UI-02 / UI-05: open every create modal read-only. Writes other than /api/v1/auth/ are aborted.
const { chromium } = require('playwright')
const BASE = process.env.BASE || 'http://127.0.0.1:5173'
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const PAGES = ['instances', 'placement-groups', 'images', 'flavors', 'keys', 'vpcs', 'subnets', 'floating-ips',
    'security-groups', 'load-balancers', 'vpn-gateways', 'zones', 'hypervisors', 'storage-pools', 'migrations',
    'notification-channels', 'vm-alarm-rules']
;(async () => {
    const browser = await chromium.launch()
    const ctx = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'zh-CN', viewport: { width: 1500, height: 950 } })
    const blocked = new Set(), gets = [], errors = []
    await ctx.route((u) => u.pathname.startsWith('/api/'), (route) => {
        const req = route.request(), p = new URL(req.url()).pathname
        if (req.method() === 'GET' || p.startsWith('/api/v1/auth/')) return route.continue()
        blocked.add(`${req.method()} ${p}`)
        return route.abort()
    })
    const page = await ctx.newPage()
    page.on('pageerror', (e) => errors.push(e.message))
    page.on('request', (r) => { const u = new URL(r.url()); if (r.method() === 'GET' && u.pathname.startsWith('/api/v1/')) gets.push(u.pathname + u.search) })
    await page.goto(`${BASE}/login`)
    await page.fill('#email', 'admin')
    await page.fill('#password', process.env.ADMIN_PASSWORD)
    await page.click('button[type=submit]')
    await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 30000 })

    await page.goto(`${BASE}/dashboard/instances`)
    await page.waitForLoadState('networkidle')
    console.log(`page sizes=${JSON.stringify(await page.locator('.page-size-select option').allInnerTexts())} current=${await page.locator('.page-size-select').inputValue()}`)

    for (const p of PAGES) {
        const before = errors.length
        await page.goto(`${BASE}/dashboard/${p}`)
        await page.waitForLoadState('networkidle'); await sleep(800)
        gets.length = 0
        await page.locator('.page-header .header-actions button.btn-primary').first().click()
        // the create-instance modal loads eight lists before it renders its form
        await page.locator('.modal-overlay select').first().waitFor({ timeout: p === 'instances' ? 20000 : 3000 }).catch(() => {})
        await sleep(1500)
        const open = (await page.locator('.modal-overlay').count()) > 0
        const title = await page.locator('.modal-overlay .modal-header h3').first().innerText().catch(() => '-')
        const selects = await page.locator('.modal-overlay select').evaluateAll((ss) => ss.filter((s) => s.offsetParent).map((s) => {
            const label = (s.closest('.form-group')?.querySelector('label')?.textContent || '?').trim().replace(/\s+/g, ' ')
            const opts = [...s.options].filter((o) => o.value)
            return `${label}=${opts.length}${s.disabled ? '(disabled)' : ''}${opts[0] && /规格/.test(label) ? ' [' + opts[0].textContent.trim() + ']' : ''}`
        }))
        const lock = await page.evaluate(() => document.body.style.overflow || '-')
        const focus = await page.evaluate(() => { const a = document.activeElement; return a?.closest('.modal-content') ? `${a.tagName}.${[...a.classList].join('.')}` : 'outside' })
        const noLimit = [...new Set(gets.filter((g) => !/[?&]limit=/.test(g) && !/\/(regions|auth|metrics|addresses|system|telemetry)/.test(g)).map((g) => g.split('?')[0]))]
        await page.keyboard.press('Escape'); await sleep(400)
        const closed = (await page.locator('.modal-overlay').count()) === 0
        const unlock = await page.evaluate(() => document.body.style.overflow || '-')
        console.log(`${p.padEnd(22)} ${open ? 'open' : 'NOT OPEN'} [${title}] ${selects.join(' | ') || '(no select)'}`)
        console.log(`    body=${lock}->${unlock} focus=${focus} esc=${closed ? 'closed' : 'STILL OPEN'} noLimitGET=[${noLimit.join(',')}]${errors.length > before ? ' ERROR:' + errors[before].slice(0, 80) : ''}`)
    }
    console.log('\nblocked writes:', JSON.stringify([...blocked]))
    console.log('pageerrors:', errors.length ? JSON.stringify(errors.slice(0, 5)) : 'none')
    await browser.close()
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1) })
```

### tc12-silent.js（UI-04）

```js
// Read-only: a 5 s silent poll must not strand a user-started load (useListQuery). Migrations list rewritten
// in the browser so that one row is in_progress (starts the poll); every list GET after the first is delayed 6 s.
const { chromium } = require('playwright')
const BASE = process.env.BASE || 'http://127.0.0.1:5173'
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
;(async () => {
    const browser = await chromium.launch()
    const ctx = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'zh-CN', viewport: { width: 1500, height: 900 } })
    let listGets = 0
    await ctx.route((u) => u.pathname.startsWith('/api/'), async (route) => {
        const req = route.request(), u = new URL(req.url())
        if (req.method() !== 'GET' && !u.pathname.startsWith('/api/v1/auth/')) return route.abort()
        if (req.method() === 'GET' && u.pathname === '/api/v1/migrations') {
            const n = ++listGets
            const resp = await route.fetch()
            const body = await resp.json()
            if (body.migrations?.[0]) body.migrations[0].status = 'in_progress'
            if (n > 1) await sleep(6000)
            return route.fulfill({ response: resp, json: body })
        }
        return route.continue()
    })
    const page = await ctx.newPage()
    const errors = []
    page.on('pageerror', (e) => errors.push(e.message))
    await page.goto(`${BASE}/login`)
    await page.fill('#email', 'admin'); await page.fill('#password', process.env.ADMIN_PASSWORD)
    await page.click('button[type=submit]')
    await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 30000 })
    await page.goto(`${BASE}/dashboard/migrations`)
    await page.waitForLoadState('networkidle'); await sleep(500)
    await page.locator('.data-table th.sortable', { hasText: '状态' }).click() // user-started load, delayed 6 s
    await sleep(15000)
    const spinner = await page.locator('.data-table .loading-spinner').count()
    const rows = await page.locator('.data-table > tbody > tr:not(:has(.table-state))').count()
    const order = await page.locator('.data-table th[aria-sort]').innerText().catch(() => '-')
    console.log(`list GETs=${listGets} spinner=${spinner} rows=${rows} sortedBy=${order.trim()} pageerrors=${errors.length}`)
    await browser.close()
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1) })
```

### tc12-shots.js 与 tc12-diff.js（UI-06）

```js
// tc12-shots.js — full-page screenshots into OUT for before / after comparison (read-only)
const { chromium } = require('playwright')
const fs = require('fs')
const BASE = process.env.BASE || 'http://127.0.0.1:5173'
const OUT = process.env.OUT || 'shots'
const PAGES = (process.env.PAGES || ',instances,volumes,security-groups,vpn-gateways,storage-pools,alarms,vm-alarm-rules,alarm-events,settings,hypervisors,orgs').split(',')
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
;(async () => {
    fs.mkdirSync(OUT, { recursive: true })
    const browser = await chromium.launch()
    const ctx = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'zh-CN', viewport: { width: 1500, height: 950 } })
    await ctx.route((u) => u.pathname.startsWith('/api/'), (route) => {
        const req = route.request(), p = new URL(req.url()).pathname
        return req.method() === 'GET' || p.startsWith('/api/v1/auth/') ? route.continue() : route.abort()
    })
    const page = await ctx.newPage()
    await page.goto(`${BASE}/login`); await sleep(1500)
    await page.screenshot({ path: `${OUT}/login.png`, fullPage: true })
    await page.fill('#email', 'admin'); await page.fill('#password', process.env.ADMIN_PASSWORD)
    await page.click('button[type=submit]')
    await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 30000 })
    for (const p of PAGES) {
        await page.goto(`${BASE}/dashboard${p ? '/' + p : ''}`)
        await page.waitForLoadState('networkidle'); await sleep(1200)
        await page.screenshot({ path: `${OUT}/${p || 'overview'}.png`, fullPage: true, animations: 'disabled' })
    }
    await browser.close()
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1) })
```

```js
// tc12-diff.js — pixel difference of same-named PNGs in two directories: node tc12-diff.js <before> <after>
const fs = require('fs'), path = require('path')
const { PNG } = require('pngjs'), pixelmatch = require('pixelmatch')
const [a, b] = process.argv.slice(2)
for (const f of fs.readdirSync(a).filter((n) => n.endsWith('.png'))) {
    if (!fs.existsSync(path.join(b, f))) { console.log(`${f.padEnd(28)} missing in ${b}`); continue }
    const x = PNG.sync.read(fs.readFileSync(path.join(a, f))), y = PNG.sync.read(fs.readFileSync(path.join(b, f)))
    if (x.width !== y.width || x.height !== y.height) { console.log(`${f.padEnd(28)} size ${x.width}x${x.height} -> ${y.width}x${y.height}`); continue }
    const diff = new PNG({ width: x.width, height: x.height })
    const n = pixelmatch(x.data, y.data, diff.data, x.width, x.height, { threshold: 0 })
    if (n) fs.writeFileSync(path.join(b, f.replace('.png', '.diff.png')), PNG.sync.write(diff))
    console.log(`${f.padEnd(28)} diff pixels=${n}`)
}
```
