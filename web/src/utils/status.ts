// 资源状态 → 展示语义。
//
// 原先各列表页都有一份自己的 getStatusClass，同一个状态在不同页面可能是不同的颜色
// （例如 available 在云硬盘页是 active、在镜像页是 running）。这里收敛成五种语义，
// 由 StatusBadge 统一渲染。
//
// 只要不是这里列出的状态，一律按 pending（进行中）处理：状态串来自后端，
// 新增状态时宁可显示成"进行中"，也不要因为查不到而没有任何样式。

export type StatusVariant = 'success' | 'neutral' | 'pending' | 'error' | 'warning'

const VARIANTS: Record<StatusVariant, string[]> = {
    success: [
        'running',
        'active',
        'available',
        'attached',
        'in-use',
        'in_use',
        'completed',
        'done',
        'migrated',
        'enabled',
        'healthy',
        'ready',
        'resolved',
    ],
    neutral: ['stopped', 'shutoff', 'shut_off', 'inactive', 'disabled', 'offline', 'unknown'],
    warning: ['paused', 'maintenance', 'degraded', 'warning', 'suspended'],
    error: ['error', 'failed', 'not_supported', 'timeout', 'rollback', 'source_rollback', 'firing'],
    pending: [
        'pending',
        'creating',
        'provisioning',
        'starting',
        'stopping',
        'deleting',
        'detaching',
        'attaching',
        'migrating',
        'in_progress',
        'deploying',
        'updating',
        'target_prepared',
        'source_prepared',
        'rebooting',
        'resizing',
    ],
}

const LOOKUP: Record<string, StatusVariant> = {}
for (const [variant, states] of Object.entries(VARIANTS) as [StatusVariant, string[]][]) {
    for (const state of states) LOOKUP[state] = variant
}

export const statusVariant = (status?: string | null): StatusVariant => {
    if (!status) return 'neutral'
    return LOOKUP[status.toLowerCase()] ?? 'pending'
}
