<script setup lang="ts">
// Frame shared by the monitoring views: title, time range buttons, custom range, refresh and error line.
// The content goes in the default slot and styles itself. The state comes from useMonitoring in the parent.
import type { Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { Clock, RefreshCw } from 'lucide-vue-next'
import { TIME_RANGES } from '../../composables/useMonitoring'

defineProps<{
    title: string
    icon: Component
    timeRange: string
    loading: boolean
    error: string
}>()
const customStart = defineModel<string>('customStart', { required: true })
const customEnd = defineModel<string>('customEnd', { required: true })
const emit = defineEmits<{
    (e: 'set-range', range: { value: string; step: string }): void
    (e: 'toggle-custom'): void
    (e: 'refresh'): void
}>()

const { t } = useI18n()
</script>

<template>
    <div class="monitoring-panel card">
        <div class="monitor-header">
            <div class="monitor-title">
                <component :is="icon" :size="20" class="text-primary" />
                <h3>{{ title }}</h3>
            </div>
            <div class="monitor-actions">
                <div class="range-selector">
                    <button
                        v-for="r in TIME_RANGES"
                        :key="r.value"
                        class="btn btn-ghost btn-xs"
                        :class="{ active: timeRange === r.value }"
                        @click="emit('set-range', r)"
                    >
                        {{ t(`dashboard.monitoring.ranges.${r.value}`) }}
                    </button>
                    <button
                        class="btn btn-ghost btn-xs"
                        :class="{ active: timeRange === 'custom' }"
                        @click="emit('toggle-custom')"
                    >
                        {{ t('dashboard.monitoring.custom') }}
                    </button>
                </div>
                <button
                    class="btn btn-ghost btn-xs"
                    :title="t('actions.refresh')"
                    :aria-label="t('actions.refresh')"
                    :disabled="loading"
                    @click="emit('refresh')"
                >
                    <RefreshCw :size="14" :class="{ spinning: loading }" />
                </button>
            </div>
        </div>

        <div v-if="timeRange === 'custom'" class="custom-range-picker">
            <div class="filter-group">
                <label><Clock :size="14" /> {{ t('dashboard.monitoring.start') }}</label>
                <input v-model="customStart" type="datetime-local" class="form-select" />
            </div>
            <div class="filter-group">
                <label><Clock :size="14" /> {{ t('dashboard.monitoring.end') }}</label>
                <input v-model="customEnd" type="datetime-local" class="form-select" />
            </div>
            <button class="btn btn-primary btn-sm" :disabled="loading" @click="emit('refresh')">
                {{ t('actions.apply') }}
            </button>
        </div>

        <div v-if="error" class="monitor-error">{{ error }}</div>

        <slot />
    </div>
</template>

<style scoped>
.monitoring-panel {
    padding: var(--spacing-5);
}

.monitor-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--spacing-3);
    margin-bottom: var(--spacing-4);
}

.monitor-title {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.monitor-title h3 {
    margin: 0;
    font-size: var(--font-size-base);
    font-weight: 600;
}

.monitor-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.range-selector {
    display: flex;
    padding: 2px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-secondary);
}

.range-selector .btn {
    height: 24px;
    min-height: 24px;
    padding: 2px 10px;
    font-size: var(--font-size-xs);
}

.range-selector .btn.active {
    background: var(--bg-primary);
    color: var(--text-primary);
    box-shadow: var(--shadow-sm);
}

.custom-range-picker {
    display: flex;
    align-items: flex-end;
    flex-wrap: wrap;
    gap: var(--spacing-3);
    margin-bottom: var(--spacing-4);
}

.filter-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-1);
}

.filter-group label {
    display: flex;
    align-items: center;
    gap: var(--spacing-1);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.monitor-error {
    margin-bottom: var(--spacing-3);
    padding: var(--spacing-2) var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--error-light);
    color: var(--error-color);
    font-size: var(--font-size-sm);
}
</style>
