# TC-04 弹性公网 IP（FIP）

优先级 **P0** · 会在计算节点上真实下发 iptables 规则 · 约 20 min

## 设计约定

- 公网子网 `public-756`：VLAN 756，`52.117.101.144/28`，网关 `.145`，可用 `.146–.158`。
  - `.146` / `.147` / `.148` 被三个节点的 system router 占用，`.151` 是保留的 `rb-lb`。
  - **可用于测试的实际只有几个地址**，用完必须释放。
- 公网子网上的虚拟机自带 `native` 公网 IP，**随虚拟机释放，不能单独删**。
- 浮动 IP 生效方式：在虚拟机所在节点的 VPC 路由器 netns 里下发 DNAT（公网→内网）+ SNAT（内网→公网）。
- 浮动 IP 的路由表名是 `fip-<vlan>`，登记在 `/etc/iproute2/rt_tables.d/cloudland.conf`（26.04 的 iproute2 已无 `/etc/iproute2/rt_tables`）。

---

## FIP-01 申请（不挂实例）

**P0** · 脚本 `fip.js`

### 步骤

界面：弹性公网 IP 页 → 新建 → 只填名称、公网子网选 `public-756` → 提交。

```bash
ssh work-01 'cd /opt/cloudland/deploy/docker && PW=$(grep "^ADMIN_PASSWORD=" .env | cut -d= -f2-)
docker run --rm --network host -v /root/console-test:/work -w /work -e ADMIN_PASSWORD="$PW" \
  mcr.microsoft.com/playwright:v1.55.0-noble node fip.js 2>&1 | tail -8'
```

### 检查项

- [ ] 表格行数 +1，`toast` 无错误，无 4xx/5xx
- [ ] 分配到的地址落在 `52.117.101.146–158` 内且未与既有地址冲突
- [ ] **未挂载的行上有「挂载」按钮**（挂载后应变成「卸载」）
- [ ] `pageerrors: none`

### 涉及接口

| 方法 | 路径 | 要点 |
|---|---|---|
| POST | `/api/v1/floating_ips` | `{"name"}` + 可选 `public_subnets` / `site_subnets` / `instance` / `inbound` / `outbound` / `public_ip` / `activation_count` |
| PATCH | `/api/v1/floating_ips/:id` | 挂载 / 卸载 |
| DELETE | `/api/v1/floating_ips/:id` | |

### 配额口径

弹性 IP（含负载均衡的 `/load_balancers/{id}/floating_ips`）计入 `public_ips`。**一次创建按上限 `activation_count`（0 按 1）+ 站点子网数预扣**，再按返回数组长度退还未创建的部分。

- [ ] 申请 1 个后 `public_ips` 用量 +1（`select * from org_resource_consumptions`）

---

## FIP-02 挂载到虚拟机 + 节点侧核对

**P0** · 脚本 `fip3.js`

### ⚠️ 选择挂载目标时务必小心

脚本里「选下拉第一项」的写法曾把浮动 IP 挂到了保留的 LB 后端 `rb-be3` 上。**必须按名称选中 `wp-` 前缀的测试虚拟机**，不要按下标选。

### 步骤

先有一台测试虚拟机（见 `TC-02`），然后界面上点该浮动 IP 行的「挂载」→ 下拉选中 `wp-vm` → 确认。

节点侧核对（在虚拟机**所在节点**上）：

```bash
FIP=52.117.101.152      # 换成实际分配到的
VMIP=192.168.62.4       # 换成虚拟机内网 IP
RT=8                    # 换成该 VPC 的 router 编号

ssh work-0X "ip netns exec router-$RT iptables -t nat -S | grep $FIP"
ssh work-0X "ip netns exec router-$RT ip -4 -br addr | grep $FIP"
```

### 检查项

- [ ] 行上的按钮从「挂载」变成「卸载」，列表里显示了挂载的虚拟机
- [ ] netns 里有 DNAT：`-A PREROUTING -d <FIP>/32 -j DNAT --to-destination <VMIP>`
- [ ] netns 里有对应的 SNAT
- [ ] 浮动 IP 挂在 `te-<router>-<vlan>` 接口上
- [ ] 从外部 `curl`/`ping` 该浮动 IP 可达（前提：虚拟机上有服务、安全组已放行）

---

## FIP-03 卸载与释放，节点规则清零

**P0**

### 步骤

界面点「卸载」→ 再点「删除」。

```bash
ssh work-01 'source /root/alarmtest-env.sh; api GET /floating_ips | jq -r ".floating_ips[].name" | grep wp- || echo "已清零"'
ssh work-0X "ip netns exec router-$RT iptables -t nat -S | grep -c $FIP; ip netns exec router-$RT ip -4 -br addr | grep -c $FIP"
```

### 检查项

- [ ] 列表里不再有该浮动 IP
- [ ] netns 里 DNAT / SNAT 规则数为 **0**
- [ ] `te-<router>-<vlan>` 上不再有该地址
- [ ] **`rb-lb` 仍然 10/10**（最重要 —— 浮动 IP 清理曾影响同节点的 LB）

---

## FIP-04 界面显示

**P1** · 脚本 `verify.js`

### 检查项

- [ ] 申请弹窗与挂载弹窗的**实例下拉显示的是内网 IP，不是 UUID**
  ```
  期望：wp-vm (192.168.50.5/24)
  错误：wp-vm (b4d7ff29-184b-4463-bcb3-e6d964bfbab1)
  ```
  > **回归点**：实例响应上**没有** `ip_address` 字段。页面曾写 `inst.ip_address || inst.id`，回退到 UUID。正确做法是从主网卡取：`inst.interfaces?.find(i => i.is_primary)?.ip_address`。这类错误由 `vue-tsc` 检出（见 `TC-01 BUILD-01`）。
- [ ] 下拉只列出可挂载的实例（有 VPC、非 `provisioning`、有主网卡）
- [ ] 带宽（入 / 出）输入范围 1–20000 Mbps
- [ ] 列表里 `native` 类型的 IP 不提供单独删除

---

## FIP-05 配额超限

**P1**

把 `max_public_ips` 临时调到当前用量，再申请一个。

- [ ] 返回 **429**，`error_code` 为 `quota_exceeded`
- [ ] 界面显示的是**本地化的超额原因**（走 `utils/quotaError.ts`），不是原始英文报错
- [ ] 测完把配额改回 200

---

## 历史缺陷回归点

| # | 现象 | 根因 | 判定 |
|---|---|---|---|
| 1 | 测试脚本把浮动 IP 挂到了保留的 `rb-be3` 上，差点破坏 LB 环境 | 脚本「选第一个下拉项」 | 一切选择都按名称匹配 `wp-` |
| 2 | 实例下拉括号里显示 UUID | `inst.ip_address` 字段不存在 | 见 FIP-04 |
| 3 | `create_lb_floating.sh` 未设带宽限制时删 tc 规则输出 `invalid priority value` | **既有噪音**，不影响功能 | 不要当故障 |
| 4 | 删除负载均衡时它挂的弹性 IP 配额没释放 | 网关删除 LB 时要连带释放 | `TC-06` 里核对 |
| 5 | 浮动 IP 在热迁移时中断 ~0.6 秒，completed 阶段再断 ~1.1 秒 | **设计取舍，决定不改**（2026-09-15）。只丢包不断连接 | 见 `TC-07` |

---

## 清理

```bash
ssh work-01 'source /root/alarmtest-env.sh
for f in $(api GET /floating_ips | jq -r ".floating_ips[]|select(.name|startswith(\"wp-\"))|.id"); do
  api PATCH /floating_ips/$f "{}" >/dev/null; api DELETE /floating_ips/$f >/dev/null
done
api GET /floating_ips | jq -r ".floating_ips[].name"'
```

- [ ] 输出里只剩保留的 `rb-fip-*` / `mig-fip-*` / `net-c-*`
