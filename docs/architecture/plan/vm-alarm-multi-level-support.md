# VM 告警规则多级告警支持（Multi-Level Support）实现计划

## 1. 背景与目标

目前 CloudLand 的虚拟机告警规则（CPU、内存、带宽）采用的是"规则组"模式（`RuleGroupV2`）。每个规则组有一个全局 `Level` 字段，且 **API 层强制限制每组只能包含一条阈值规则**（`len(rules) == 1`）。同步逻辑（`alarm_rebuild.go`）也只取 `details[0]` 进行渲染。

**目标：** 支持在同一个告警规则组内配置多条阈值，每条阈值可以有独立的告警级别。例如：
- 策略 1：CPU 负载 > 80% 持续 5 分钟 -> **Warning**
- 策略 2：CPU 负载 > 95% 持续 2 分钟 -> **Critical**

## 2. 现状分析

### 2.1 数据模型（`api/src/model/alarmrules.go`）

- **RuleGroupV2**：包含 `Level`（组级别）、`Name`、`Type`（cpu/memory/bw）、`Enabled` 等字段。
- **CPURuleDetail**：包含 `Name`、`Limit`（1-100）、`Rule`（gt/lt）、`Duration`、`Over`、`DownDuration`、`DownTo`，**无独立 Level 字段**。
- **MemoryRuleDetail**：字段与 CPURuleDetail 相同，**无独立 Level 字段**。
- **BWRuleDetail**：新版使用 `Direction`（in/out）、`Limit`、`Duration`，同时保留了旧版双向字段做向下兼容，**无独立 Level 字段**。

### 2.2 API 层（`api/src/apis/alarms.go`）

- `CreateCPURule` / `CreateMemoryRule`：**强制校验 `len(rules) == 1`**，即每次只允许提交一条规则。
- `CreateBWRule`：同样限制单条规则，但通过 `direction` 字段区分入站/出站。
- 删除/更新逻辑仅处理单条 detail。

### 2.3 同步逻辑（`api/src/services/alarm_rebuild.go`）

- `rebuildCPURule` / `rebuildMemoryRule` / `rebuildBWRule` 均只取 `details[0]`。
- 每个规则组只渲染一个 Prometheus 规则文件（如 `VM-cpu-rule.yml.j2`）。
- 模板中的 Alert 名称格式为 `CPUUsage_{{ owner }}_{{ rule_group }}`，不含索引后缀。

### 2.4 Prometheus 模板（`deploy/roles/monitor/templates/`）

- `VM-cpu-rule.yml.j2`：使用 `severity: "{{ level }}"` 标签，Alert 名称无索引区分。
- `VM-memory-rule.yml.j2`：结构同 CPU。
- `VM-in-bw-rule.yml.j2` / `VM-out-bw-rule.yml.j2`：按方向分文件，结构类似。

### 2.5 前端（`web/src/views/dashboard/VMAlarmRules.vue`）

- 创建表单中 `level` 为全局字段，不在 `rules` 数组内。
- UI 上 `addRuleRow()` 允许添加多行，但提交时 API 会拒绝多条规则。
- TypeScript 接口（`web/src/api/vmAlarmRules.ts`）中 `CPURuleDetail`、`MemoryRuleDetail`、`BWRuleDetail` 均无 `level` 字段。

## 3. 详细设计方案

### 3.1 后端变更 (Golang)

#### A. 数据模型 (Model) — `api/src/model/alarmrules.go`

为**所有三种** RuleDetail 结构体增加 `Level` 字段：

```go
// CPURuleDetail
type CPURuleDetail struct {
    // ... 现有字段 ...
    Level string `json:"level" gorm:"column:level;type:varchar(32)"` // 新增
}

// MemoryRuleDetail
type MemoryRuleDetail struct {
    // ... 现有字段 ...
    Level string `json:"level" gorm:"column:level;type:varchar(32)"` // 新增
}

// BWRuleDetail
type BWRuleDetail struct {
    // ... 现有字段 ...
    Level string `json:"level" gorm:"column:level;type:varchar(32)"` // 新增
}
```

> 注意：GORM AutoMigrate 会自动添加新列，无需手动写 SQL migration。但需确认项目启动时是否调用了 AutoMigrate。

#### B. API 层 — `api/src/apis/alarms.go`

1. **移除 `len(rules) == 1` 限制**，改为 `len(rules) >= 1`。
2. 创建时循环遍历 `rules` 数组，为每条 detail 创建独立记录。
3. 每条 detail 的 API 请求体增加可选 `level` 字段。
4. **更新逻辑**（Update 相关端点）也需同步支持多条 detail 的增删改。
5. **删除逻辑**：删除规则组时需清理所有关联的 Prometheus 规则文件（不再只有一个文件）。

#### C. 规则同步逻辑 — `api/src/services/alarm_rebuild.go`

重构所有 `rebuild*Rule` 函数，从单条处理改为**遍历处理**：

```go
func rebuildCPURule(group *model.RuleGroupV2) error {
    details, err := GetCPURuleDetails(group.UUID)
    if err != nil || len(details) == 0 {
        return err
    }

    for i, detail := range details {
        ruleData := map[string]interface{}{
            "owner":            group.Owner,
            "rule_group":       group.RuleID,
            "name":             detail.Name,
            "rule_operator":    operatorMap[detail.Rule],
            "limit_value":      detail.Limit,
            "duration_minutes": detail.Duration,
            "rule_id":          group.RuleID,
            "global_rule_id":   group.UUID,
            "region_id":        group.RegionID,
            "level":            detail.Level,
            "detail_index":     i,
        }

        // 输出文件名加索引后缀：alarm-cpu-{rule_id}-{i}.yml
        outputFile := fmt.Sprintf("alarm-cpu-%s-%d.yml", group.RuleID, i)
        ProcessTemplate("VM-cpu-rule.yml.j2", outputFile, ruleData)
    }
    return nil
}
```

对 `rebuildMemoryRule` 和 `rebuildBWRule` 做相同重构。

#### D. 删除逻辑更新

删除规则组时，需要清理所有带索引后缀的 Prometheus 规则文件：
```go
// 旧：只删除 alarm-cpu-{rule_id}.yml
// 新：删除 alarm-cpu-{rule_id}-*.yml（所有索引文件）
```

### 3.2 Prometheus 模板变更

更新 `deploy/roles/monitor/templates/` 下的模板，Alert 名称增加索引后缀以确保唯一性：

**`VM-cpu-rule.yml.j2`**：
```yaml
# 旧
- alert: CPUUsage_{{ owner }}_{{ rule_group }}
# 新
- alert: CPUUsage_{{ owner }}_{{ rule_group }}_{{ detail_index }}
```

对 `VM-memory-rule.yml.j2`、`VM-in-bw-rule.yml.j2`、`VM-out-bw-rule.yml.j2` 做相同修改。

### 3.3 前端变更 (Vue 3)

#### A. TypeScript 接口 — `web/src/api/vmAlarmRules.ts`

为所有 detail 接口增加 `level` 字段：

```typescript
interface CPURuleDetail {
  name: string
  rule: 'gt' | 'lt'
  limit: number
  duration: number
  level: string  // 新增：必填
}

interface MemoryRuleDetail {
  name: string
  limit: number
  duration: number
  level: string  // 新增：必填
}

interface BWRuleDetail {
  direction: 'in' | 'out'
  name: string
  limit: number
  duration: number
  level: string  // 新增：必填
}
```

#### B. 界面调整 — `web/src/views/dashboard/VMAlarmRules.vue`

1. 在"阈值配置"区域的每一行（`rule-row`）增加一个**级别选择下拉框**。
2. 下拉选项：`紧急 (Critical)`、`告警 (Warning)`、`提示 (Info)`。
3. 移除前端对多条规则的限制（目前 UI 允许添加但 API 拒绝，修改后两端一致）。
4. 每条规则的 `level` 为必填项，新增行时默认值为 `warning`。
5. **移除全局级别选择器**（`RuleGroupV2.Level` 不再使用），级别完全由每条子规则独立控制。

#### C. 表格展示

- 规则列表中，级别列改为显示该组包含的所有级别标签（如同时显示 Warning + Critical 两个 badge）。
- 或展示最高级别，hover 时 tooltip 显示完整列表。

### 3.4 国际化 (i18n)

在 `zh.ts` 和 `en.ts` 中增加：

```typescript
// en.ts
ruleLevel: 'Rule Level',
multipleThresholds: 'Multiple Thresholds',

// zh.ts
ruleLevel: '规则级别',
multipleThresholds: '多级阈值',
```

## 4. 实施步骤

| 阶段 | 任务 | 涉及文件 | 优先级 |
| :--- | :--- | :--- | :--- |
| **Phase 1** | **数据模型扩展**：为 CPURuleDetail、MemoryRuleDetail、BWRuleDetail 增加 Level 字段 | `api/src/model/alarmrules.go` | P0 |
| **Phase 2** | **API 层改造**：移除 `len(rules)==1` 限制，支持多条 detail 创建/更新/删除 | `api/src/apis/alarms.go` | P0 |
| **Phase 3** | **同步逻辑重构**：重构 `rebuild*Rule` 函数，遍历 details 生成多个 Prometheus 规则文件 | `api/src/services/alarm_rebuild.go` | P0 |
| **Phase 4** | **模板更新**：Alert 名称增加索引后缀，确保多条规则不互相覆盖 | `deploy/roles/monitor/templates/VM-*.yml.j2` | P0 |
| **Phase 5** | **前端 API 接口更新**：TypeScript 接口增加 level 字段 | `web/src/api/vmAlarmRules.ts` | P1 |
| **Phase 6** | **前端 UI 升级**：行内级别选择器、移除单条规则限制、列表展示调整 | `web/src/views/dashboard/VMAlarmRules.vue` | P1 |
| **Phase 7** | **国际化**：增加新翻译键 | `web/src/locales/en.ts`, `zh.ts` | P2 |
| **Phase 8** | **端到端测试**：验证多级告警触发后 Alertmanager 收到正确 severity | - | P0 |

## 5. 注意事项

- **文件唯一性**：每条 detail 对应独立的 Prometheus 规则文件，文件名格式 `alarm-{type}-{rule_id}-{index}.yml`，避免覆盖。
- **Alert 名称唯一性**：模板中 Alert 名称增加 `_{{ detail_index }}` 后缀。
- **删除清理**：删除规则组时必须清理所有关联的索引文件，建议使用 glob 模式匹配 `alarm-{type}-{rule_id}-*.yml`。
- **性能影响**：规则文件数量与 detail 数量成正比，但单个规则组通常不会配置超过 3-5 条阈值，整体影响可控。
- **BW 规则特殊处理**：BW 规则同时涉及 direction 维度，一个 BW detail 已经按方向分文件，多级支持后文件命名需考虑 `alarm-bw-{direction}-{rule_id}-{index}.yml`。
- **AutoMigrate 确认**：需确认 API 启动时是否对 CPURuleDetail / MemoryRuleDetail / BWRuleDetail 调用了 GORM AutoMigrate，否则新增的 `level` 列不会自动创建。

> 注：产品尚未发布，无需考虑向下兼容。所有变更直接替换现有实现，`level` 为 detail 的必填字段，不再保留组级别 fallback 逻辑。
