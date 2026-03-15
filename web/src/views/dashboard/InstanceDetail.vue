<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { instancesApi, type Instance } from '../../api/instances'
import { ArrowLeft, Play, Square, RotateCw, Trash2, Terminal, Server, Cpu, HardDrive, Network, Key, ExternalLink, Copy, Check } from 'lucide-vue-next'
import DeleteModal from '../../components/modals/DeleteModal.vue'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const instanceId = route.params.id as string

const instance = ref<Instance | null>(null)
const loading = ref(true)
const error = ref('')
const actionLoading = ref<string | null>(null)
const copiedField = ref<string | null>(null)

// --- Delete Confirmation Modal Logic ---
const deleteModalVisible = ref(false)
const deletingInstance = ref(false)
const deleteError = ref('')

const handleDeleteClick = () => {
    deleteModalVisible.value = true
}

const closeDeleteModal = () => {
    deleteModalVisible.value = false
    deleteError.value = ''
}

const confirmDelete = async () => {
    if (!instance.value) return
    deletingInstance.value = true
    deleteError.value = ''
    try {
        await instancesApi.deleteInstance(instanceId)
        router.push({ name: 'instances' })
    } catch (err: any) {
        console.error('Failed to delete instance:', err)
        deleteError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        deletingInstance.value = false
    }
}

const fetchInstance = async (showLoading: boolean = true) => {
    if (showLoading) {
        loading.value = true
        error.value = ''
    }
    try {
        const response = await instancesApi.getInstance(instanceId)
        const data = response.data as any
        instance.value = data.instance || data
    } catch (err) {
        console.error('Failed to fetch instance:', err)
        if (showLoading) error.value = 'Failed to load instance details.'
    } finally {
        if (showLoading) loading.value = false
    }
}

const handleAction = async (action: 'start' | 'stop' | 'restart') => {
    if (!instance.value) return
    
    actionLoading.value = action
    try {
        switch (action) {
            case 'start':
                await instancesApi.startInstance(instanceId, instance.value.hostname)
                break
            case 'stop':
                await instancesApi.stopInstance(instanceId, instance.value.hostname)
                break
            case 'restart':
                await instancesApi.rebootInstance(instanceId, instance.value.hostname)
                break
        }
        
        let targetStableStates: string[] = []
        if (action === 'start' || action === 'restart') targetStableStates = ['running', 'active', 'error']
        if (action === 'stop') targetStableStates = ['stopped', 'shutoff', 'shut_off', 'error']

        let attempts = 0
        const checkStatus = async () => {
            attempts++
            await fetchInstance(false)
            const currentStatus = instance.value?.status?.toLowerCase() || ''
            if (targetStableStates.includes(currentStatus) || attempts >= 15) {
                actionLoading.value = null
            } else {
                setTimeout(checkStatus, 3000)
            }
        }
        
        // Start polling after 2 seconds
        setTimeout(checkStatus, 2000)
    } catch (err) {
        console.error(`Failed to ${action} instance:`, err)
        actionLoading.value = null
    }
}

const openConsole = async () => {
    // Open window immediately to avoid popup blockers
    const consoleWindow = window.open('about:blank', '_blank')
    if (!consoleWindow) {
        alert('Popup blocked! Please allow popups for this site.')
        return
    }

    try {
        const response = await instancesApi.getConsole(instanceId)
        const { console_host, console_port, console_path, token } = response.data
        
        const host = console_host
        const port = console_port || 443
        const path = console_path || 'websockify'
        const encrypt = port === 443
        
        const externalUrl = `https://novnc.com/noVNC/vnc.html?host=${host}&port=${port}&autoconnect=true&encrypt=${encrypt}&path=${path}?token=${token}`
        consoleWindow.location.href = externalUrl
    } catch (error: any) {
        console.error('Failed to get console info:', error)
        consoleWindow.close()
        alert('Failed to get console info: ' + (error.response?.data?.error_message || error.message))
    }
}

const goBack = () => {
    router.back()
}

const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text).then(() => {
        copiedField.value = field
        setTimeout(() => { copiedField.value = null }, 2000)
    })
}

const getStatusClass = (status: string) => {
    const statusMap: Record<string, string> = {
        'running': 'status-running',
        'active': 'status-running',
        'stopped': 'status-stopped',
        'shutoff': 'status-stopped',
        'shut_off': 'status-stopped',
        'pending': 'status-pending',
        'starting': 'status-pending',
        'stopping': 'status-pending',
        'error': 'status-error'
    }
    return statusMap[status?.toLowerCase()] || 'status-pending'
}

const formatMemory = (mb?: number) => {
    if (!mb) return '-'
    if (mb >= 1024) {
        return `${(mb / 1024).toFixed(0)} GB`
    }
    return `${mb} MB`
}

const navigateToVolume = (volumeId: string) => {
    router.push({ name: 'volume-detail', params: { id: volumeId } })
}

const navigateToSecurityGroup = (sgId: string) => {
    router.push({ name: 'security-group-detail', params: { id: sgId } })
}

onMounted(() => fetchInstance())
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
            <button class="btn btn-primary" @click="() => fetchInstance()">Retry</button>
        </div>

        <div v-else-if="instance" class="detail-content">
            <!-- Title Bar -->
            <div class="title-bar card">
                <div class="title-info">
                    <div class="title-icon">
                        <Server :size="28" />
                    </div>
                    <div>
                        <h2 class="instance-title">{{ instance.hostname }}</h2>
                        <div class="instance-id-row">
                            <span class="instance-id">{{ instance.id }}</span>
                            <button class="copy-btn" @click="copyToClipboard(instance.id, 'id')" :title="$t('messages.copied')">
                                <Check v-if="copiedField === 'id'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <span :class="['badge', 'badge-lg', getStatusClass(instance.status)]">
                        {{ instance.status }}
                    </span>
                </div>
            </div>

            <!-- Info Grid -->
            <div class="info-grid">
                <!-- General Info -->
                <div class="card info-card">
                    <h3>General Information</h3>
                    <div class="key-value-list">

                        <div class="kv-item">
                            <span class="label">Created At</span>
                            <span class="value">{{ instance.created_at || '-' }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label">Zone</span>
                            <span class="value">{{ instance.zone || '-' }}</span>
                        </div>
                         <div class="kv-item">
                            <span class="label">Hypervisor</span>
                            <span class="value">{{ instance.hypervisor || '-' }}</span>
                        </div>
                    </div>
                </div>

                <!-- Specs -->
                <div class="card info-card">
                    <h3>Specifications</h3>
                    <div class="key-value-list">
                        <div class="kv-item" v-if="instance.flavor && typeof instance.flavor === 'object' && instance.flavor.name">
                            <span class="label">Flavor</span>
                            <span class="value">{{ instance.flavor.name }}</span>
                        </div>
                        <div class="kv-item">
                            <span class="label"><Cpu :size="14" /> vCPU</span>
                            <span class="value">{{ instance.flavor?.cpu || instance.cpu || '-' }}</span>
                        </div>
                        <div class="kv-item">
                             <span class="label"><Server :size="14" /> RAM</span>
                             <span class="value">{{ formatMemory(instance.flavor?.memory || instance.memory) }}</span>
                        </div>
                        <div class="kv-item">
                             <span class="label"><HardDrive :size="14" /> Disk</span>
                             <span class="value">{{ instance.flavor?.disk || instance.disk || '-' }} GB</span>
                        </div>
                         <div class="kv-item">
                             <span class="label"><HardDrive :size="14" /> Image</span>
                             <span class="value">{{ instance.image?.name || '-' }}</span>
                        </div>
                    </div>
                </div>

                <!-- Network -->
                <div class="card info-card">
                    <h3>Network</h3>
                    <div class="key-value-list">
                         <div class="kv-item" v-if="instance.vpc?.name">
                            <span class="label">VPC</span>
                            <span class="value">{{ instance.vpc.name }}</span>
                        </div>
                        
                        <div v-if="!instance.interfaces?.length" class="text-secondary" style="font-size: 13px; margin-top: 8px;">
                            No network interfaces
                        </div>
                        <template v-else>
                            <div v-for="(iface, index) in instance.interfaces" :key="iface.id" class="interface-block" :style="{ marginTop: index > 0 ? '16px' : '8px', paddingTop: index > 0 ? '16px' : '0', borderTop: index > 0 ? '1px dashed var(--border-light)' : 'none' }">
                                <div class="kv-item" style="margin-bottom: 8px;">
                                    <span class="label" style="color: var(--primary-color); font-weight: 500;">
                                        <Network :size="14" /> {{ iface.name || 'Interface' }}
                                        <span v-if="iface.is_primary" class="status-badge status-running" style="margin-left:8px; font-size: 10px; padding: 0 4px;">PRI</span>
                                    </span>
                                </div>
                                <div class="kv-item">
                                    <span class="label" style="padding-left: 20px;">Subnet</span>
                                    <span class="value">{{ iface.subnet?.name || '-' }}</span>
                                </div>
                                <div class="kv-item">
                                    <span class="label" style="padding-left: 20px;">IP Address</span>
                                    <span class="value mono">{{ iface.ip_address || '-' }}</span>
                                </div>
                                <div class="kv-item">
                                    <span class="label" style="padding-left: 20px;">MAC Address</span>
                                    <span class="value mono">{{ iface.mac_address || '-' }}</span>
                                </div>
                                <template v-for="fip in iface.floating_ips" :key="fip.id">
                                    <div v-if="(fip.ip_address || fip.fip_address) !== iface.ip_address" class="kv-item">
                                        <span class="label" style="padding-left: 20px;">Floating IP</span>
                                        <span class="value mono">{{ fip.ip_address || fip.fip_address || '-' }}</span>
                                    </div>
                                    <div v-if="fip.vlan" class="kv-item">
                                        <span class="label" style="padding-left: 20px;">FIP VLAN</span>
                                        <span class="value">{{ fip.vlan }}</span>
                                    </div>
                                </template>
                                <div class="kv-item" v-if="iface.security_groups?.length">
                                    <span class="label" style="padding-left: 20px;">Security Groups</span>
                                    <span class="value">
                                        <template v-for="(sg, sgIndex) in iface.security_groups" :key="sg.id">
                                            <a href="#" @click.prevent="navigateToSecurityGroup(sg.id)" class="resource-link">{{ sg.name || sg.id.substring(0, 8) }}</a><span v-if="sgIndex < iface.security_groups.length - 1">, </span>
                                        </template>
                                    </span>
                                </div>
                            </div>
                        </template>
                    </div>
                </div>


                <!-- Storage -->
                <div class="card info-card">
                     <h3>Storage</h3>
                     <div class="key-value-list">
                          <div v-if="!instance.volumes?.length" class="text-secondary" style="font-size: 13px;">
                              No volumes attached
                          </div>
                          <div v-else v-for="volume in instance.volumes" :key="volume.id" class="kv-item">
                             <span class="label">
                                <HardDrive :size="14" /> 
                                {{ volume.target || volume.device || 'Volume' }}
                                <span v-if="volume.booting || volume.boot_index === 0" class="status-badge status-success" style="margin-left:8px; font-size: 10px; padding: 0 4px;">BOOT</span>
                             </span>
                             <span class="value">
                                <span v-if="volume.size" class="mono" style="margin-right: 8px; color: var(--text-secondary);">{{ volume.size }} GB</span>
                                <a href="#" @click.prevent="navigateToVolume(volume.id)" class="resource-link">
                                    {{ volume.name || volume.id.substring(0, 8) }} <ExternalLink :size="12" style="display:inline; margin-left:2px;" />
                                </a>
                             </span>
                          </div>
                     </div>
                </div>



                <!-- Security -->
                <div class="card info-card">
                     <h3>SSH Keys</h3>
                    <div class="key-value-list">
                        <div v-if="!instance.keys?.length" class="kv-item">
                            <span class="label"><Key :size="14" /> Key Pair</span>
                            <span class="value">-</span>
                        </div>
                        <div v-else v-for="key in instance.keys" :key="key.id" class="kv-item">
                            <span class="label"><Key :size="14" /> Key Pair</span>
                            <span class="value">{{ key.name }}</span>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Action Bar -->
             <div class="action-bar card">
                <div class="action-group">
                    <button 
                        v-if="['stopped', 'shutoff', 'shut_off'].includes(instance.status?.toLowerCase())"
                        class="btn btn-secondary"
                        @click="handleAction('start')"
                        :disabled="!!actionLoading"
                    >
                        <span v-if="actionLoading === 'start'" class="loading-spinner small mr-1"></span>
                        <Play v-else :size="16" /> 
                        {{ actionLoading === 'start' ? 'Starting...' : 'Start' }}
                    </button>
                    <button 
                         v-else
                        class="btn btn-secondary"
                        @click="handleAction('stop')"
                        :disabled="!!actionLoading"
                    >
                        <span v-if="actionLoading === 'stop'" class="loading-spinner small mr-1"></span>
                        <Square v-else :size="16" /> 
                        {{ actionLoading === 'stop' ? 'Stopping...' : 'Stop' }}
                    </button>
                    <button 
                        class="btn btn-secondary"
                        @click="handleAction('restart')"
                        :disabled="!!actionLoading || instance.status !== 'running'"
                    >
                        <span v-if="actionLoading === 'restart'" class="loading-spinner small mr-1"></span>
                        <RotateCw v-else :size="16" /> 
                        {{ actionLoading === 'restart' ? 'Rebooting...' : 'Reboot' }}
                    </button>
                     <button class="btn btn-secondary" @click="openConsole">
                        <img src="/images/vnc.svg" alt="VNC" width="16" height="16" /> Console
                    </button>
                </div>
                <div class="action-group">
                    <button class="btn btn-danger" @click="handleDeleteClick">
                        <Trash2 :size="16" /> {{ $t('actions.delete') }}
                    </button>
                </div>
            </div>
        </div>

        <DeleteModal
            :show="deleteModalVisible"
            :resource-name="instance?.hostname"
            :resource-id="instance?.id"
            :loading="deletingInstance"
            :error="deleteError"
            @close="closeDeleteModal"
            @confirm="confirmDelete"
        />
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

.instance-title {
    margin: 0 0 4px 0;
    font-size: var(--font-size-xl);
    font-weight: var(--font-weight-semibold);
    color: var(--primary-color);
}

.instance-id-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
}

.instance-id {
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
    transition: all var(--transition-base);
}

.btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
    filter: grayscale(100%);
}

.loading-spinner.small {
    width: 14px;
    height: 14px;
    border-width: 2px;
}

.mr-1 {
    margin-right: 6px;
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

.resource-link {
    color: var(--primary-600);
    cursor: pointer;
    text-decoration: none;
    transition: color 0.2s;
}

.resource-link:hover {
    color: var(--primary-700);
    text-decoration: underline;
}
</style>
