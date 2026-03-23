<script setup lang="ts">
import { X, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
    show: boolean
    title?: string
    message?: string
    resourceName?: string
    resourceId?: string
    loading?: boolean
    error?: string
}>()

const emit = defineEmits(['close', 'confirm'])
const { t } = useI18n()

const handleClose = () => {
    if (!props.loading) {
        emit('close')
    }
}

const handleConfirm = () => {
    emit('confirm')
}
</script>

<template>
    <div v-if="show" class="modal-overlay" @click.self="handleClose">
        <div class="modal-content card delete-modal">
            <div class="modal-header">
                <h3>{{ title || t('actions.delete') }}</h3>
                <button class="btn btn-ghost btn-sm icon-btn" @click="handleClose" :disabled="loading">
                    <X :size="20" />
                </button>
            </div>
            <div class="modal-body">
                <div class="delete-warning">
                    <div class="delete-warning-icon">
                        <Trash2 :size="32" />
                    </div>
                    <p class="delete-warning-text">
                        {{ message || t('dashboard.deleteConfirm.message') }}
                    </p>
                    <div v-if="resourceName || resourceId" class="delete-resource-info">
                        <span class="delete-resource-label">{{ t('dashboard.deleteConfirm.resource') }}</span>
                        <span v-if="resourceName" class="delete-resource-name">{{ resourceName }}</span>
                        <span v-if="resourceId" class="delete-resource-id">{{ resourceId }}</span>
                    </div>
                    <div v-if="error" class="text-error-box">
                        {{ error }}
                    </div>
                </div>
            </div>
            <div class="modal-footer">
                <button class="btn btn-secondary" @click="handleClose" :disabled="loading">
                    {{ t('actions.cancel') }}
                </button>
                <button class="btn btn-danger" @click="handleConfirm" :disabled="loading">
                    <span v-if="loading" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
                    <Trash2 v-else :size="14" />
                    {{ loading ? (t('dashboard.deleteConfirm.deleting') || 'Deleting...') : (t('actions.delete') || 'Delete') }}
                </button>
            </div>
        </div>
    </div>
</template>

<style scoped>
.delete-warning {
    text-align: center;
}

.delete-warning-icon {
    width: 64px;
    height: 64px;
    border-radius: 50%;
    background: var(--error-light);
    display: flex;
    align-items: center;
    justify-content: center;
    margin: 0 auto var(--spacing-4);
    color: var(--error-color);
}

.delete-warning-text {
    font-size: var(--font-size-base);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-4) 0;
    line-height: var(--line-height-relaxed);
}

.delete-resource-info {
    background: var(--bg-secondary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    padding: var(--spacing-3) var(--spacing-4);
    text-align: left;
}

.delete-resource-label {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
    display: block;
    margin-bottom: var(--spacing-1);
}

.delete-resource-name {
    font-size: var(--font-size-base);
    font-weight: 600;
    color: var(--text-primary);
    display: block;
}

.delete-resource-id {
    font-size: var(--font-size-xs);
    color: var(--text-light);
    font-family: var(--font-family-mono);
    display: block;
    margin-top: 2px;
}

.text-error-box {
    margin-top: var(--spacing-4);
    font-size: var(--font-size-sm);
    background: var(--error-light);
    padding: var(--spacing-2);
    border-radius: var(--radius-sm);
    color: var(--error-color);
    text-align: left;
}
.icon-btn:hover {
    color: var(--text-primary);
}

.loading-spinner {
    border: 2px solid rgba(255, 255, 255, 0.3);
    border-top: 2px solid white;
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
}

@keyframes spin {
    to { transform: rotate(360deg); }
}

.btn-danger {
    background: var(--error-color);
    color: white;
    border: none;
    padding: 8px 20px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    font-weight: 500;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    transition: background 0.2s;
}

.btn-danger:hover {
    background: var(--error-dark);
}

.btn-danger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}
</style>
