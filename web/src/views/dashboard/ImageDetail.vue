<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { imagesApi, type Image } from '../../api/images'
import { ArrowLeft, HardDrive, Trash2, Server, Monitor, Disc, Copy, Check, Tag, CalendarDays } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const imageId = route.params.id as string

const image = ref<Image | null>(null)
const loading = ref(true)
const error = ref('')
const deleting = ref(false)
const copiedField = ref<string | null>(null)

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

const fetchImage = async () => {
    loading.value = true
    error.value = ''
    try {
        const response = await imagesApi.getImage(imageId)
        const data = response.data as any
        image.value = data.image || data
    } catch (err) {
        console.error('Failed to fetch image:', err)
        error.value = 'Failed to load image details.'
    } finally {
        loading.value = false
    }
}

const handleDelete = async () => {
    if (!confirm('Are you sure you want to delete this image? This action cannot be undone.')) return
    
    deleting.value = true
    try {
        await imagesApi.deleteImage(imageId)
        router.push({ name: 'images' })
    } catch (err) {
        console.error('Failed to delete image:', err)
        alert('Failed to delete image.')
        deleting.value = false
    }
}

const goBack = () => {
    router.back()
}

const formatSize = (bytes?: number) => {
    if (!bytes) return '-'
    const k = 1024
    const units = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return `${(bytes / Math.pow(k, i)).toFixed(1)} ${units[i]}`
}

onMounted(fetchImage)
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" /> {{ $t('actions.back') }}
            </button>
        </div>

        <div v-if="loading" class="loading-container">
            <div class="loading-spinner"></div>
        </div>

        <div v-else-if="error" class="error-container card">
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-primary" @click="fetchImage">{{ $t('actions.retry') }}</button>
        </div>

        <div v-else-if="image" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="title-info">
                    <div class="title-icon">
                        <Disc :size="28" />
                    </div>
                    <div>
                        <h2 class="image-title">{{ image.name }}</h2>
                        <div class="image-id-row">
                            <span class="image-id">{{ image.id }}</span>
                            <button class="copy-btn" @click="copyToClipboard(image.id, 'id')" :title="$t('messages.copied')">
                                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <span :class="['badge', 'badge-lg', image.public ? 'status-running' : 'status-stopped']">
                        {{ image.public ? $t('dashboard.table.public') : $t('dashboard.table.private') }}
                    </span>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                    <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                             <span class="label"><Server :size="14" /> {{ $t('dashboard.table.status') }}</span>
                            <span class="value">{{ image.status || 'Active' }}</span>
                        </div>
                         <div class="kv-item">
                             <span class="label"><CalendarDays :size="14" /> {{ $t('dashboard.table.createdAt') }}</span>
                            <span class="value">{{ image.created_at || '-' }}</span>
                        </div>
                    </div>
                </div>

                <!-- OS Components -->
                <div class="card info-card">
                     <h3>{{ $t('dashboard.table.osKernel') }}</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.osFamily') }}</span>
                            <span class="value">{{ image.os_family || '-' }}</span>
                        </div>
                         <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.osVersion') }}</span>
                            <span class="value">{{ image.os_version || '-' }}</span>
                        </div>
                        <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.architecture') }}</span>
                            <span class="value mono">{{ image.architecture || 'x86_64' }}</span>
                        </div>
                         <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.bootLoader') }}</span>
                            <span class="value">{{ image.boot_loader || 'BIOS' }}</span>
                        </div>
                    </div>
                </div>

                <!-- File Specs -->
                <div class="card info-card">
                     <h3>{{ $t('dashboard.table.fileDetails') }}</h3>
                    <div class="key-value-list">
                         <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.format') }}</span>
                            <span class="value uppercase">{{ image.format || 'qcow2' }}</span>
                        </div>
                         <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.size') }}</span>
                            <span class="value">{{ formatSize(image.size) }}</span>
                        </div>
                         <div class="kv-item">
                             <span class="label">{{ $t('dashboard.table.defaultUser') }}</span>
                            <span class="value mono">{{ image.user || 'root' }}</span>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Action Bar -->
             <div class="action-bar card">
                <div class="action-group">
                    <!-- Placeholder for future actions like 'Launch Instance' -->
                </div>
                <div class="action-group">
                    <button class="btn btn-danger" @click="handleDelete" :disabled="deleting">
                        <Trash2 :size="16" /> {{ $t('actions.delete') }}
                    </button>
                </div>
            </div>
        </div>
    </div>
</template>

<style scoped>
.detail-page {
    max-width: 1200px;
    margin: 0 auto;
}

.detail-header {
    margin-bottom: var(--spacing-4);
}

.loading-container, .error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 60px;
}

/* Title Bar */
.title-bar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--spacing-5);
}

.title-info {
    display: flex;
    align-items: center;
    gap: var(--spacing-4);
}

.title-icon {
    width: 52px;
    height: 52px;
    border-radius: var(--radius-lg);
    background: linear-gradient(135deg, var(--primary-50), var(--primary-100));
    color: var(--primary-color);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
}

.image-title {
    margin: 0 0 4px 0;
    font-size: var(--font-size-xl);
    font-weight: var(--font-weight-semibold);
    color: var(--primary-color);
}

.image-id-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.image-id {
    font-size: var(--font-size-xs);
    color: var(--text-light);
    font-family: var(--font-family-mono);
}

.copy-btn {
    background: none;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-sm);
    padding: 2px 5px;
    cursor: pointer;
    color: var(--text-light);
    display: inline-flex;
    align-items: center;
    transition: all 0.15s;
}

.copy-btn:hover {
    color: var(--primary-color);
    border-color: var(--primary-200);
    background: var(--primary-50);
}

.copied-icon {
    color: var(--success-color);
}

.title-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
}

.badge-lg {
    font-size: var(--font-size-sm);
    padding: 6px 14px;
}

/* Info Grid */
.info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-6);
}

.info-card {
    padding: var(--spacing-5);
}

.info-card h3 {
    font-size: var(--font-size-md);
    font-weight: 600;
    margin: 0 0 var(--spacing-4) 0;
    color: var(--text-primary);
    border-bottom: 1px solid var(--border-light);
    padding-bottom: var(--spacing-3);
}

.key-value-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.kv-item {
    display: flex;
    justify-content: space-between;
    font-size: var(--font-size-sm);
}

.kv-item .label {
    color: var(--text-secondary);
    display: flex;
    align-items: center;
    gap: 6px;
}

.kv-item .value {
    color: var(--text-primary);
    font-weight: 500;
    text-align: right;
}

.value.mono {
    font-family: var(--font-family-mono);
}

.value.uppercase {
    text-transform: uppercase;
}

/* Action Bar */
.action-bar {
    padding: var(--spacing-4);
    display: flex;
    justify-content: space-between;
    align-items: center;
}

.action-group {
    display: flex;
    gap: var(--spacing-3);
}

.btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
}

.btn-danger {
    background: var(--error-color);
    color: white;
    border: none;
    padding: 8px 16px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    transition: background var(--transition-base);
}

.btn-danger:hover {
    background: var(--error-dark);
}
</style>
