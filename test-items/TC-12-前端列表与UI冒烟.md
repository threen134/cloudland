# TC-12 前端列表与 UI 冒烟（UI）

优先级 **P0** · 非破坏性（脚本内置写请求拦截） · 约 15 min

改了任何共享组件（`DataTable`、`BaseModal`、`PageToolbar`、`PaginationBar`、`useListQuery`…）或任何列表页，都必须跑完本文件。共享组件一处改动会同时影响 20 多个页面。

## UI-01 全页面渲染冒烟

**P0** · 脚本 `deployed-regression.js`

```bash
ssh work-01 'cd /opt/cloudland/deploy/docker && PW=$(grep "^ADMIN_PASSWORD=" .env | cut -d= -f2-)
docker run --rm --network host -v /root/console-test:/work -w /work -e ADMIN_PASSWORD="$PW" \
  mcr.microsoft.com/playwright:v1.55.0-noble node deployed-regression.js 2>&1 | tail -50'
```

脚本逐个打开 25 个页面，统计表格行数 / 卡片数，并在 context 上兜底拦截除 auth 外的所有写请求。

**基线**（数据量会变，关注的是「不为 0 且不报错」）：

| 页面 | 期望 | 页面 | 期望 |
|---|---|---|---|
| overview | 卡片 ≥ 2 | migrations | 行 > 0 |
| instances | 行 > 0 | alarms | 行 = 7 |
| volumes | 行 > 0 | vm-alarm-rules | 行 = 4 |
| images | 行 ≥ 2 | alarm-events | 行 > 0 |
| flavors | 行 ≥ 3 | notification-channels | 行 = 2 |
| keys | 行 ≥ 3 | users | 行 ≥ 1 |
| vpcs | 行 ≥ 2 | orgs | 行 ≥ 1 |
| subnets | 行 ≥ 4 | regions | 行 ≥ 1 |
| floating-ips | 行 > 0 | activities | 行 > 0 |
| security-groups | 行 > 0 | audit-logs | 行 ≥ 0 |
| load-balancers | 行 ≥ 1 | settings | 卡片 ≥ 5 |
| hypervisors | 行 = 3 | quota | — |
| zones | 行 ≥ 1 | vpn-gateways | 行 ≥ 0（脚本尚未覆盖，用 `TC-17` VPN-07 的 `vpn-ui.js`） |

检查项：

- [ ] 25 个页面全部渲染，没有页面行数意外为 0
- [ ] `pageerrors: none`
- [ ] 被拦截的写请求**只有** `telemetry/traces` 与 `metrics/*/his_data`（这两个是只读语义的 POST）

### 历史缺陷回归点

| 现象 | 根因 |
|---|---|
| 组织列表页改用 `useListQuery` 后完全空白 | 误以为 `useListQuery` 会自动加载，把 `onMounted(fetchOrgs)` 删了。**它不会自动加载**，必须显式触发 |
| VPN 网关列表页永远「未发现 VPN 网关」（2026-09-23） | 同一根因：`VpnGateways.vue` 的 `onMounted` 只调了 `fetchVpcs()`。typecheck / lint 全过，只有真跑冒烟才发现——**新列表页一律要跑一次 UI 冒烟** |
| 安全组列表里超过 50 个的组看不到 | 前端没传 `limit`，后端默认只回 50 条，前端还在本地过滤 |
| 安全组列表的 `vpc` 列显示的是另一条安全组的名字 | 后端 `SecgroupAdmin.List` 查路由器时复用了列表语句（GORM Statement 复用），详情接口正常、只有列表错 |

---

## UI-02 创建弹窗冒烟

**P0** · 脚本 `create-modals-sweep.js`

```bash
ssh work-01 'cd /opt/cloudland/deploy/docker && PW=$(grep "^ADMIN_PASSWORD=" .env | cut -d= -f2-)
docker run --rm --network host -v /root/console-test:/work -w /work -e ADMIN_PASSWORD="$PW" \
  mcr.microsoft.com/playwright:v1.55.0-noble node create-modals-sweep.js 2>&1 | tail -20'
```

逐页打开创建弹窗，检查弹窗是否出现、必填下拉是否有可选项。

**基线**：

```
images  弹窗=开 下拉 2（空的: 无）   flavors 弹窗=开 下拉 0
keys    弹窗=开 下拉 0              vpcs    弹窗=开 下拉 0
subnets 弹窗=开 下拉 2              floating-ips 弹窗=开 下拉 2
security-groups 弹窗=开 下拉 1      load-balancers 弹窗=开 下拉 1
zones   弹窗=开 下拉 0              migrations 弹窗=开 下拉 3
hypervisors 弹窗=开 下拉 2          notification-channels 弹窗=开 下拉 1
vm-alarm-rules 弹窗=开 下拉 2
```

检查项：

- [ ] 13 个弹窗全部打开
- [ ] **「空的: 无」** —— 没有任何必填下拉是空的
- [ ] `pageerrors 总数: 0`
- [ ] **migrations 下拉数是 3**（实例筛选 / 实例 / 目标节点）。若是 4，说明「迁移类型」下拉被改回来了 —— 见 `TC-07 MIG-04`

创建虚拟机的弹窗不在这个脚本里（在实例页），单独跑：

```bash
ssh work-01 'cd /opt/cloudland/deploy/docker && PW=$(grep "^ADMIN_PASSWORD=" .env | cut -d= -f2-)
docker run --rm --network host -v /root/console-test:/work -w /work -e ADMIN_PASSWORD="$PW" \
  mcr.microsoft.com/playwright:v1.55.0-noble node vmmodal.js 2>&1 | tail -5'
```

- [ ] 镜像 / 规格 / VPC / 子网 / IP 四个下拉均非空
- [ ] 规格项形如 `vmtest-large (2 vCPU, 4 GB RAM, 500 GB 磁盘)` —— 数字不能是 `0` 或空

  > **回归点**：`Flavor` 的字段是 `uuid` / `cpu` / `memory` / `disk`，不是 `id` / `vcpus` / `ram`。页面上曾写成 `f.vcpus || f.cpu` 这种带回退的形式，靠回退才显示正确。

---

## UI-03 服务端分页 / 搜索 / 排序

**P0**

**约定**：列表页的分页、搜索、排序**一律在服务端做**。分页之后如果搜索或排序还在前端，用户操作的只是当前这一页，比没有更糟。

### 3.1 clapi 侧列表

参数 `offset` / `limit` / `query` / `order`，返回 `{total, <资源>}`。

```bash
ssh work-01 'source /root/alarmtest-env.sh
for r in instances volumes images flavors keys vpcs subnets floating_ips security_groups load_balancers; do
  t=$(api GET "/$r?limit=1" | jq -r ".total // \"无total\"")
  n=$(api GET "/$r?limit=1" | jq -r ".$r | length")
  printf "%-18s total=%-5s 返回条数=%s\n" "$r" "$t" "$n"
done'
```

- [ ] 每个资源都返回 `total`，且 `limit=1` 时只回 1 条
- [ ] `total` 是**过滤后**的总数（带 `query` 时随之变化）

排序：

```bash
ssh work-01 'source /root/alarmtest-env.sh
api GET "/instances?order=hostname&limit=5"  | jq -r ".instances[].hostname"
echo ---
api GET "/instances?order=-hostname&limit=5" | jq -r ".instances[].hostname"
echo --- 非法排序列应被丢弃而不是报错
api GET "/instances?order=;drop&limit=2" | jq -r ".instances[].hostname"'
```

- [ ] 正序 / 倒序结果相反
- [ ] 非法 `order` 被丢弃，接口仍返回 200（`dbs.Sortby` 用正则限定成标识符）

**只有能映射到本表真实列的列才开放排序**。以下是**排不了**的，页面上不应出现排序箭头：

- 关联表字段：卷的「挂载虚拟机」、子网的「VPC」
- 前端算出来的列：IP 使用率、用量指标
- 可用区的「类型」（对应列名是 `default`，SQL 保留字，排序会语法报错）

- [ ] 上述列在页面上没有排序功能

> **注意**：IP / CIDR 列是 varchar **字典序**，不是按 IP 数值排。`192.168.100.1` 会排在 `192.168.9.1` 前面，这是预期行为。

### 3.2 网关侧列表

组织 / 用户 / 区域 / 通知渠道 / 组织成员（`cpgateway/src/apis/list_query.go`），默认每页 50、上限 500。

```bash
ssh work-01 'source /root/alarmtest-env.sh
api GET "/orgs?limit=1"  | jq -c "{total, n:(.orgs|length)}"
api GET "/users?limit=1" | jq -c "{total, n:(.users|length)}"
api GET "/regions?limit=1" | jq -c "{total, n:(.regions|length)}"
echo "--- 大小写不敏感搜索"
api GET "/orgs?query=ADMIN" | jq -c "{total, names:[.orgs[].name]}"
echo "--- limit 超上限应被截到 500"
api GET "/orgs?limit=99999" | jq -c "{total}"
echo "--- % 应被当字面量转义，不是通配符"
api GET "/orgs?query=%25" | jq -c "{total}"'
```

- [ ] 都返回 `{total, <资源>}`
- [ ] `query=ADMIN` 能搜到 `Admin`（用 `LOWER(col) LIKE`，测试默认跑 SQLite 没有 `ILIKE`）
- [ ] `limit` 超过 500 不报错
- [ ] `query=%` 当字面量处理，`total=0`（`%` / `_` / `\` 会转义）

**切换器不走分页**：组织切换器用 `/auth/me/orgs`，区域切换器和组织详情页的成员列表显式传 `limit=500`。

- [ ] 顶栏的组织 / 区域切换器把所有选项都列出来了

### 历史缺陷回归点

| 现象 | 根因 |
|---|---|
| 组织列表接口 500，报 `COUNT(organizations.*)` 语法错 | GORM 计数复用了带 `Select("organizations.*")` 的语句。修法见 `countAndPage`：计数要 `Session` 复制一份，且计数前清掉 `Select` |
| 备份列表的搜索词失效、带 `volume_id` 时拼出无参数的 `owner = ?` | 搜索条件被 `query, args := GetOrgFilter()` 整个覆盖 |
| IP 组列表的 `dic_id` / `type` 过滤从未生效 | 同上，被覆盖 |
| 字典 / 子网 / IP 组的多个过滤互相覆盖（后者盖前者） | 现在应是 AND 关系 |

- [ ] 备份列表带搜索词能正确过滤
- [ ] IP 组列表的 `dic_id` / `type` 过滤生效
- [ ] 子网列表同时带名称搜索 + VPC 过滤时，两个条件都生效（AND）

---

## UI-04 表格组件（DataTable）

**P1**

`DataTable` 是**泛型组件**（`generic="T extends Record<string, any>"`），`#cell-*` 插槽里的 `row` 是具体接口类型。

- [ ] 改完 `DataTable.vue` 后 `BUILD-01` 类型检查仍为 0 错误（泛型推断断了会在这里暴露）
- [ ] 带下拉菜单 / 悬浮卡片的页面（实例列表的 IP 悬浮、各页的操作菜单）内容没有被容器裁掉 —— 这些页面要传 `allowOverflow`
- [ ] 可展开行的页面（告警事件、VM 告警规则）点击行能展开，一次只展开一行
- [ ] 加载态 / 空态 / 错误态 + 重试按钮都在

分页条（`PaginationBar`）：

- [ ] 每页条数选择器有 10 / 20 / 50 / 100，默认 20
- [ ] 当前页删空后自动退回上一页

---

## UI-05 弹窗组件（BaseModal）

**P1**

- [ ] ESC 能关闭
- [ ] 点遮罩能关闭
- [ ] 打开时 body 不滚动，关闭后恢复
- [ ] 自动聚焦到第一个输入框
- [ ] `loading` 期间无法关闭
- [ ] 传了 `form` 的弹窗，回车触发提交

### scoped 样式的坑

页面的 scoped CSS 只对自己模板里的元素生效。把结构搬进子组件后，页面里针对 `.modal-content` / `.page-header` / `.search-box` 的规则会**静默失效**（成为死代码）。

- [ ] 重构过的页面外观与重构前一致（必要时做像素对比，见下）

---

## UI-06 视觉回归（像素对比）

**P1** — 仅当改动是**纯外观重构**（换令牌、抽组件、调样式）时

```bash
# 改动前
ssh work-01 '... node pixel-shots.js /work/shots-before'
# 改动后
ssh work-01 '... node pixel-shots.js /work/shots-after'
# 对比
ssh work-01 'cd /root/console-test && for f in shots-before/*.png; do
  n=$(basename $f); d=$(compare -metric AE $f shots-after/$n /dev/null 2>&1)
  echo "$n 差异像素=$d"
done'
```

- [ ] 预期不变的页面差异像素为 0

### 历史缺陷回归点

把 `#64748b` 换成 `var(--text-secondary)` 时改变了实际颜色 —— `--text-secondary` 的值是 `#475569`，不是同一个色。**颜色令牌化必须逐个核对令牌的实际值等于原来的十六进制值**，不能按语义猜。

- [ ] 颜色令牌替换后像素差异为 0

### 样式令牌约定

- z-index 一律用 `--z-*`：dropdown-backdrop 90 / dropdown 100 / sticky 200 / fixed 300（顶栏）/ sidebar 320 / modal-backdrop 400 / modal 500 / modal-nested 550 / tooltip 600 / toast 700。组件内部的局部层叠（控制台遮罩、登录页装饰）保持 0/1/2，**不要动**。
- 断点从 480 / 768 / 1024 / 1280 里挑。存量的 640 / 700 / 720 / 900 / 1200 是按具体组件调出来的，**不要强行统一**。

---

## UI-07 多窗口登录态

**P1** · 脚本 `token-sync-e2e.js`

cpgateway 签发新 token（登录、switch-org、switch-region）时会**吊销旧 token**，而未勾「记住我」时 token 在 sessionStorage、每个窗口一份。

```bash
ssh work-01 'cd /opt/cloudland/deploy/docker && PW=$(grep "^ADMIN_PASSWORD=" .env | cut -d= -f2-)
docker run --rm --network host -v /root/console-test:/work -w /work -e ADMIN_PASSWORD="$PW" \
  mcr.microsoft.com/playwright:v1.55.0-noble node token-sync-e2e.js 2>&1 | tail -20'
```

- [ ] 登录后与两次页面加载，`switch-org` 调用次数为 **0**
- [ ] 主窗口刷新后，另开的控制台窗口仍能正常请求（不被踢回登录）
- [ ] 真实切换组织后，旧 token 401，其他窗口经 `BroadcastChannel('cloudland-auth')` 收到新 token 并自动重连
- [ ] 其他用户的 token 被忽略（只有已登录且 `sub` 相同的窗口才采纳）

### 历史缺陷回归点

`Layout.vue` 曾经每次加载都调 `switch-org`，主窗口刷新或新开管理页就吊销其他窗口的 token。现在只在 token 的 `org_id` 不是目标组织时才调。

---

## UI-08 浏览器存储

**P1**

- [ ] 所有 key 都来自 `utils/storage.ts` 的 `STORAGE_KEYS`，代码里没有字面量
  ```bash
  cd web/src && grep -rn "localStorage\.\(get\|set\|remove\)Item(['\"]" --include=*.ts --include=*.vue . | grep -v storage.ts
  # 期望：无输出
  ```
- [ ] 退出登录 / 401 强制登出后，两种存储里的 7 个 key **全部**被清空（只清 token 会把组织 / 区域留给下一个账号）
  ```bash
  # 在浏览器控制台或 Playwright 里检查
  # Object.keys(localStorage).concat(Object.keys(sessionStorage)).filter(k => k.startsWith('cloudland'))
  ```

---

## UI-09 链路追踪

**P2** · 脚本 `tracing-check.js`

- [ ] 前端仍在经 `POST /api/v1/telemetry/traces` 上报 span
- [ ] 错误 toast 上显示 Trace ID
