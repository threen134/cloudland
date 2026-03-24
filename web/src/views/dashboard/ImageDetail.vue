<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { imagesApi, type Image } from '../../api/images'
import { useAuthStore } from '../../stores/auth'
import { useTenantStore } from '../../stores/tenant'
import { ArrowLeft, HardDrive, Trash2, Server, Monitor, Disc, Copy, Check, Tag, CalendarDays, Eye, EyeOff, ChevronDown } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const imageId = route.params.id as string
const auth = useAuthStore()
const tenant = useTenantStore()
const isSuperuser = computed(() => auth.user?.is_superuser === true)
const currentOrgName = computed(() => tenant.currentOrg?.name || '')

const image = ref<Image | null>(null)
const loading = ref(true)
const error = ref('')
const deleting = ref(false)
const togglingVisibility = ref(false)
const copiedField = ref<string | null>(null)
const showActionMenu = ref(false)

const toggleActionMenu = () => {
    showActionMenu.value = !showActionMenu.value
}

const closeActionMenu = () => {
    showActionMenu.value = false
}

const canDelete = computed(() => {
    if (!image.value) return false
    if (isSuperuser.value) return true
    return image.value.owner === currentOrgName.value
})

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

const toggleVisibility = async () => {
    if (!image.value) return
    togglingVisibility.value = true
    try {
        await imagesApi.patchImage(imageId, { public: !image.value.public })
        await fetchImage()
    } catch (err) {
        console.error('Failed to toggle visibility:', err)
    } finally {
        togglingVisibility.value = false
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

const getStatusText = (status: string | undefined) => {
    if (!status) return t('dashboard.imageStatus.active')
    const key = status.toLowerCase()
    const translated = t(`dashboard.imageStatus.${key}`)
    return translated === `dashboard.imageStatus.${key}` ? status : translated
}

const getStatusClass = (status: string | undefined) => {
    if (!status) return 'status-running'
    const s = status.toLowerCase()
    if (s === 'active' || s === 'available') return 'status-running'
    if (s === 'error' || s === 'failed') return 'status-error'
    if (s === 'deleting' || s === 'pending') return 'status-pending'
    return 'status-stopped'
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
                        <h2 class="image-title">
                            {{ image.name }}
                            <span :class="['badge', getStatusClass(image.status)]">
                                {{ getStatusText(image.status) }}
                            </span>
                        </h2>
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
                    <div class="action-dropdown">
                        <button class="btn btn-primary" @click="toggleActionMenu">
                            {{ $t('actions.actions') }} <ChevronDown :size="14" />
                        </button>
                        <Transition name="dropdown">
                            <div v-if="showActionMenu" class="dropdown-menu" @click="closeActionMenu">
                                <button v-if="isSuperuser" class="dropdown-item" @click="toggleVisibility" :disabled="togglingVisibility">
                                    <EyeOff v-if="image.public" :size="14" />
                                    <Eye v-else :size="14" />
                                    {{ image.public ? $t('dashboard.table.setPrivate') : $t('dashboard.table.setPublic') }}
                                </button>
                                <div v-if="isSuperuser && canDelete" class="dropdown-divider"></div>
                                <button v-if="canDelete" class="dropdown-item dropdown-item-danger" @click="handleDelete" :disabled="deleting">
                                    <Trash2 :size="14" /> {{ $t('actions.delete') }}
                                </button>
                            </div>
                        </Transition>
                        <div v-if="showActionMenu" class="dropdown-backdrop" @click="closeActionMenu"></div>
                    </div>
                </div>
            </div>

            <!-- Info Grid -->
            <!-- Two-Column Layout -->
            <div class="two-col-layout">
                <div class="col-stack">
                    <!-- General Info -->
                    <div class="card info-card">
                        <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                        <div class="key-value-list">
                            <div class="kv-item">
                                <span class="label"><Server :size="14" /> {{ $t('dashboard.table.status') }}</span>
                                <span class="value">{{ getStatusText(image.status) }}</span>
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
                </div>

                <div class="col-stack">
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
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
}

.image-title .badge {
    font-size: var(--font-size-xs);
    font-weight: 500;
    vertical-align: middle;
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

/* Action Dropdown */
.action-dropdown {
    position: relative;
}

.dropdown-backdrop {
    position: fixed;
    inset: 0;
    z-index: 9;
}

.dropdown-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    min-width: 180px;
    background: var(--bg-primary, #fff);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
    padding: 4px 0;
    z-index: 10;
}

.dropdown-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 14px;
    border: none;
    background: none;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
    transition: background 0.15s;
    text-align: left;
}

.dropdown-item:hover:not(:disabled) {
    background: var(--bg-hover, #f3f4f6);
}

.dropdown-item:disabled {
    opacity: 0.4;
    cursor: not-allowed;
}

.dropdown-item-danger {
    color: var(--error-color, #ef4444);
}

.dropdown-item-danger:hover:not(:disabled) {
    background: #fef2f2;
}

.dropdown-divider {
    height: 1px;
    background: var(--border-light);
    margin: 4px 0;
}

.dropdown-enter-active {
    transition: opacity 0.15s, transform 0.15s;
}

.dropdown-leave-active {
    transition: opacity 0.1s, transform 0.1s;
}

.dropdown-enter-from {
    opacity: 0;
    transform: translateY(-4px);
}

.dropdown-leave-to {
    opacity: 0;
    transform: translateY(-4px);
}

/* Two-Column Layout */
.two-col-layout {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-4);
    align-items: start;
}

.col-stack {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-4);
}

@media (max-width: 768px) {
    .two-col-layout {
        grid-template-columns: 1fr;
    }
}

/* Info Cards */
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

.value.mono, .mono {
    font-family: var(--font-family-mono);
}

.value.uppercase {
    text-transform: uppercase;
}

.btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    transition: all var(--transition-base);
}

.btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
    filter: grayscale(100%);
}
</style>
