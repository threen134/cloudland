<script setup lang="ts">
// Capacity of a storage pool: raw capacity with what is used on disk and what is promised to volumes
// (allocated, reservations included). Never multiplied by an over-commit ratio (§8.3 of the storage plan).
// The colour follows the used ratio: 80% warning, 90% danger; allocations above the capacity are marked.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatBytes } from '../../utils/format'

const props = defineProps<{
    capacity: number
    used: number
    allocated?: number
    reserved?: number
}>()

const { t } = useI18n()
// An empty pool reads 0 B rather than the placeholder of formatBytes
const fmt = (v?: number) => (v && v > 0 ? formatBytes(v) : '0 B')

const ratio = computed(() => (props.capacity > 0 ? Math.min(props.used / props.capacity, 1) : 0))
const allocatedRatio = computed(() =>
    props.capacity > 0 && props.allocated ? Math.min(props.allocated / props.capacity, 1) : 0
)
const level = computed(() => (ratio.value >= 0.9 ? 'danger' : ratio.value >= 0.8 ? 'warning' : ''))
const overcommitted = computed(() => props.capacity > 0 && (props.allocated || 0) > props.capacity)
const title = computed(() =>
    [
        `${t('storage.used')}: ${fmt(props.used)} (${Math.round(ratio.value * 100)}%)`,
        `${t('storage.allocated')}: ${fmt(props.allocated)}`,
        props.reserved ? `${t('storage.reserved')}: ${fmt(props.reserved)}` : '',
        `${t('storage.total')}: ${fmt(props.capacity)}`,
    ]
        .filter(Boolean)
        .join('\n')
)
</script>

<template>
    <div class="capacity" :title="title">
        <div class="capacity-text">
            <span>{{ fmt(used) }} / {{ fmt(capacity) }}</span>
            <span v-if="overcommitted" class="badge badge-warning capacity-over">{{ t('storage.overcommitted') }}</span>
        </div>
        <div class="capacity-bar">
            <div class="capacity-allocated" :style="{ width: allocatedRatio * 100 + '%' }"></div>
            <div class="capacity-used" :class="level" :style="{ width: ratio * 100 + '%' }"></div>
        </div>
        <div v-if="allocated !== undefined" class="capacity-sub">
            {{ t('storage.allocated') }} {{ fmt(allocated) }}
            <template v-if="reserved">· {{ t('storage.reserved') }} {{ fmt(reserved) }}</template>
        </div>
    </div>
</template>

<style scoped>
.capacity {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 160px;
    font-size: var(--font-size-xs);
}

.capacity-text {
    display: flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
}

.capacity-over {
    font-size: 0.6875rem;
}

.capacity-bar {
    position: relative;
    height: 6px;
    background: var(--gray-100);
    border-radius: 3px;
    overflow: hidden;
}

.capacity-allocated,
.capacity-used {
    position: absolute;
    top: 0;
    left: 0;
    height: 100%;
    border-radius: 3px;
}

.capacity-allocated {
    background: var(--primary-light, var(--gray-200));
}

.capacity-used {
    background: var(--primary-color);
}

.capacity-used.warning {
    background: var(--warning-color);
}

.capacity-used.danger {
    background: var(--error-color);
}

.capacity-sub {
    color: var(--text-secondary);
}
</style>
