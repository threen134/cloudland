<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { keysApi, type SSHKey } from '../../api/keys'
import { ArrowLeft, Key, Trash2, Copy, Check } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const keyId = route.params.id as string

const sshKey = ref<SSHKey | null>(null)
const loading = ref(true)
const error = ref('')
const deleting = ref(false)
const copied = ref(false)

const fetchKey = async () => {
    loading.value = true
    error.value = ''
    try {
        const response = await keysApi.getKey(keyId)
        sshKey.value = (response.data as any).key // Assuming backend returns { key: ... }
    } catch (err) {
        console.error('Failed to fetch SSH key:', err)
        error.value = 'Failed to load SSH key details.'
    } finally {
        loading.value = false
    }
}

const handleDelete = async () => {
    if (!confirm('Are you sure you want to delete this SSH key? This action cannot be undone.')) return
    
    deleting.value = true
    try {
        await keysApi.deleteKey(keyId)
        router.push({ name: 'ssh-keys' })
    } catch (err) {
        console.error('Failed to delete SSH key:', err)
        alert('Failed to delete SSH key.')
        deleting.value = false
    }
}

const goBack = () => {
    router.back()
}

const copyPublicKey = async () => {
    if (sshKey.value?.public_key) {
        await navigator.clipboard.writeText(sshKey.value.public_key)
        copied.value = true
        setTimeout(() => copied.value = false, 2000)
    }
}

onMounted(fetchKey)
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
            <button class="btn btn-primary" @click="fetchKey">{{ $t('actions.retry') }}</button>
        </div>

        <div v-else-if="sshKey" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="resource-icon">
                    <Key :size="24" />
                </div>
                <div class="title-info">
                    <h1>{{ sshKey.name }}</h1>
                    <div class="subtitle">
                        <span class="id-text">{{ sshKey.id }}</span>
                        <span class="type-badge">{{ sshKey.type || 'ssh-rsa' }}</span>
                    </div>
                </div>
                <div class="title-actions">
                    <button class="btn btn-danger" @click="handleDelete" :disabled="deleting">
                        <Trash2 :size="16" /> {{ deleting ? 'Deleting...' : 'Delete Key' }}
                    </button>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                    <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                    <div class="key-value-list">
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.name') }}</span>
                            <span class="value">{{ sshKey.name }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.fingerprint') }}</span>
                            <span class="value mono text-sm">{{ sshKey.finger_print || '-' }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">{{ $t('dashboard.table.createdAt') }}</span>
                            <span class="value">{{ sshKey.created_at || '-' }}</span>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Public Key -->
            <div class="card key-card">
                <div class="card-header">
                    <h3>{{ $t('dashboard.table.publicKey') }}</h3>
                    <button class="btn btn-ghost btn-sm" @click="copyPublicKey">
                        <component :is="copied ? Check : Copy" :size="14" /> {{ copied ? $t('messages.copied') : $t('actions.copy') }}
                    </button>
                </div>
                <div class="key-content">
                    <pre>{{ sshKey.public_key }}</pre>
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
    align-items: center;
    gap: var(--spacing-4);
    padding: var(--spacing-6);
    margin-bottom: var(--spacing-6);
}

.resource-icon {
    width: 48px;
    height: 48px;
    background: var(--bg-tertiary);
    color: var(--primary-color);
    border-radius: var(--radius-md);
    display: flex;
    align-items: center;
    justify-content: center;
}

.title-info {
    flex: 1;
}

.title-info h1 {
    font-size: var(--font-size-xl);
    font-weight: 600;
    margin: 0 0 4px 0;
    color: var(--text-primary);
}

.subtitle {
    display: flex;
    align-items: center;
    gap: var(--spacing-3);
    font-size: var(--font-size-sm);
}

.id-text {
    font-family: var(--font-family-mono);
    color: var(--text-secondary);
}

.type-badge {
    background: var(--gray-100);
    color: var(--gray-700);
    padding: 2px 8px;
    border-radius: 12px;
    font-size: 11px;
    font-weight: 600;
    font-family: var(--font-family-mono);
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
}

.kv-item .value {
    color: var(--text-primary);
    font-weight: 500;
}

.value.mono {
    font-family: var(--font-family-mono);
}

.text-sm {
    font-size: 12px;
}

/* Key Card */
.key-card {
    padding: 0;
    overflow: hidden;
}

.card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--spacing-4) var(--spacing-5);
    border-bottom: 1px solid var(--border-light);
}

.card-header h3 { margin: 0; font-size: var(--font-size-md); }

.key-content {
    padding: var(--spacing-4);
    background: var(--bg-secondary);
    overflow-x: auto;
}

.key-content pre {
    margin: 0;
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
    white-space: pre-wrap;
    word-break: break-all;
    color: var(--text-primary);
}

.btn-danger {
    background: var(--error-color);
    color: white;
    border: none;
    padding: 8px 20px;
    border-radius: var(--radius-md);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
}
.btn-danger:hover { background: var(--error-dark); }
.btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
