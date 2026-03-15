<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { instancesApi } from '../../api/instances'
import RFB from '@novnc/novnc/lib/rfb'
import { Terminal, Maximize, Minimize, RefreshCw, AlertTriangle, Monitor, ExternalLink } from 'lucide-vue-next'

const route = useRoute()
const { t } = useI18n()
const instanceId = route.params.id as string

const container = ref<HTMLElement | null>(null)
const rfb = ref<any>(null)
const status = ref('connecting') // connecting, connected, disconnected, error
const errorMessage = ref('')
const instanceName = ref('')

const fetchConsoleInfo = async () => {
    try {
        status.value = 'connecting'
        const response = await instancesApi.getConsole(instanceId)
        const { console_url, instance } = response.data
        
        // Try to get hostname from console response or fallback to ID
        instanceName.value = instance?.hostname || instance?.id || instanceId

        // If we don't have a good hostname yet, try fetching full instance info
        if (!instance?.hostname) {
            instancesApi.getInstance(instanceId).then(res => {
                const fullInstance = res.data.instance || res.data
                if (fullInstance.hostname) {
                    instanceName.value = fullInstance.hostname
                }
            }).catch(e => console.warn('Could not fetch full instance info', e))
        }

        let url = console_url
        const consoleData = response.data
        if (!url && consoleData.console_host) {
            const host = consoleData.console_host
            const port = consoleData.console_port || 443
            const path = consoleData.console_path || 'websockify'
            const token = consoleData.token
            const protocol = port === 443 ? 'wss' : 'ws'
            url = `${protocol}://${host}:${port}/${path}?token=${token}`
        }

        if (!url) {
            throw new Error('No console URL returned from API')
        }

        // Store the host info for external access
        vncUrl.value = url
        vncHostInfo.value = {
            host: consoleData.console_host,
            port: consoleData.console_port || 443,
            path: consoleData.console_path || 'websockify',
            token: consoleData.token
        }

        connectVnc(url)
    } catch (err: any) {
        console.error('Failed to get console info:', err)
        status.value = 'error'
        errorMessage.value = err.response?.data?.error_message || err.message || t('dashboard.console.errorSubtitle')
    }
}

const connectVnc = (url: string) => {
    if (!container.value) return

    try {
        rfb.value = new RFB(container.value, url, {
            credentials: { password: '' }
        })

        rfb.value.addEventListener('connect', () => {
            status.value = 'connected'
            console.log('VNC Connected')
        })

        rfb.value.addEventListener('disconnect', (e: any) => {
            if (status.value !== 'error') {
                status.value = 'disconnected'
            }
            console.log('VNC Disconnected', e)
        })

        rfb.value.addEventListener('credentialsrequired', () => {
            // Usually not needed for these proxied consoles as token is in URL
            rfb.value.sendCredentials({ password: '' })
        })

        rfb.value.scaleViewport = true
        rfb.value.resizeSession = true
    } catch (err: any) {
        console.error('VNC Connection error:', err)
        status.value = 'error'
        errorMessage.value = err.message
    }
}

const sendCtrlAltDel = () => {
    if (rfb.value) {
        rfb.value.sendCtrlAltDel()
    }
}

const vncUrl = ref('')
const vncHostInfo = ref<any>(null)

const openExternal = () => {
    if (!vncHostInfo.value) return
    const { host, port, path, token } = vncHostInfo.value
    const externalUrl = `https://novnc.com/noVNC/vnc.html?host=${host}&port=${port}&autoconnect=true&encrypt=${port === 443}&path=${path}?token=${token}`
    window.open(externalUrl, '_blank')
}

const reload = () => {
    if (rfb.value) {
        rfb.value.disconnect()
    }
    fetchConsoleInfo()
}

onMounted(() => {
    fetchConsoleInfo()
})

onUnmounted(() => {
    if (rfb.value) {
        rfb.value.disconnect()
    }
})
</script>

<template>
    <div class="console-page">
        <header class="console-header">
            <div class="header-left">
                <div class="app-icon">
                    <Monitor :size="20" />
                </div>
                <div class="instance-info">
                    <h1 class="instance-name">{{ instanceName }}</h1>
                    <span class="instance-id">{{ instanceId }}</span>
                </div>
                <div :class="['status-indicator', status]">
                    <span class="status-dot"></span>
                    <span class="status-text">{{ t(`dashboard.console.${status}`) }}</span>
                </div>
            </div>
            <div class="header-right">
                <button class="btn-console" @click="sendCtrlAltDel" :title="t('dashboard.console.cad')">
                   CAD
                </button>
                <button class="btn-console" @click="openExternal" :title="t('actions.external') || 'Open in External Client'">
                    <ExternalLink :size="16" />
                </button>
                <button class="btn-console" @click="reload" :title="t('dashboard.console.reconnect')">
                    <RefreshCw :size="16" />
                </button>
            </div>
        </header>

        <main class="console-main">
            <div v-if="status === 'error'" class="error-overlay">
                <div class="error-content card">
                    <AlertTriangle :size="48" class="text-error" />
                    <h2>{{ t('dashboard.console.error') }}</h2>
                    <p>{{ errorMessage }}</p>
                    <button class="btn btn-primary mt-4" @click="reload">
                        <RefreshCw :size="16" /> {{ t('dashboard.console.tryAgain') }}
                    </button>
                </div>
            </div>

            <div v-if="status === 'connecting'" class="loading-overlay">
                <div class="loading-content">
                    <div class="loading-spinner-lg"></div>
                    <p>{{ t('dashboard.console.connecting') }}</p>
                </div>
            </div>

            <div ref="container" class="vnc-container"></div>
        </main>

        <footer class="console-footer">
            <div class="footer-hint">
                <Terminal :size="14" />
                <span>{{ t('dashboard.console.hint') }}</span>
            </div>
        </footer>
    </div>
</template>

<style scoped>
.console-page {
    display: flex;
    flex-direction: column;
    height: 100vh;
    width: 100vw;
    background-color: #0f172a;
    color: #f1f5f9;
    overflow: hidden;
}

.console-header {
    height: 60px;
    background-color: #1e293b;
    border-bottom: 1px solid #334155;
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0 20px;
    flex-shrink: 0;
    z-index: 10;
}

.header-left {
    display: flex;
    align-items: center;
    gap: 16px;
}

.app-icon {
    width: 36px;
    height: 36px;
    background: linear-gradient(135deg, #3b82f6, #2563eb);
    border-radius: 8px;
    display: flex;
    align-items: center;
    justify-content: center;
    color: white;
}

.instance-info {
    display: flex;
    flex-direction: column;
}

.instance-name {
    font-size: 16px;
    font-weight: 600;
    margin: 0;
    line-height: 1.2;
}

.instance-id {
    font-size: 11px;
    color: #94a3b8;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}

.status-indicator {
    display: flex;
    align-items: center;
    gap: 8px;
    background-color: #0f172a;
    padding: 4px 12px;
    border-radius: 100px;
    font-size: 12px;
    border: 1px solid #334155;
}

.status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
}

.status-indicator.connecting .status-dot {
    background-color: #eab308;
    box-shadow: 0 0 8px #eab308;
    animation: pulse 1.5s infinite;
}

.status-indicator.connected .status-dot {
    background-color: #22c55e;
    box-shadow: 0 0 8px #22c55e;
}

.status-indicator.disconnected .status-dot {
    background-color: #94a3b8;
}

.status-indicator.error .status-dot {
    background-color: #ef4444;
    box-shadow: 0 0 8px #ef4444;
}

.header-right {
    display: flex;
    align-items: center;
    gap: 8px;
}

.btn-console {
    background-color: #334155;
    border: 1px solid #475569;
    color: #f1f5f9;
    height: 32px;
    padding: 0 12px;
    border-radius: 6px;
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    transition: all 0.2s;
}

.btn-console:hover {
    background-color: #475569;
    border-color: #64748b;
}

.console-main {
    flex-grow: 1;
    position: relative;
    background-color: #000;
    display: flex;
    align-items: center;
    justify-content: center;
}

.vnc-container {
    width: 100%;
    height: 100%;
    overflow: hidden;
}

/* noVNC Styles override if needed */
:deep(.vnc-container > div) {
    height: 100% !important;
    width: 100% !important;
    display: flex !important;
    align-items: center !important;
    justify-content: center !important;
}

.error-overlay, .loading-overlay {
    position: absolute;
    inset: 0;
    background-color: rgba(15, 23, 42, 0.9);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 5;
}

.error-content {
    max-width: 400px;
    text-align: center;
    padding: 32px;
    background-color: #1e293b;
    border: 1px solid #334155;
    box-shadow: 0 20px 25px -5px rgba(0, 0, 0, 0.3);
}

.error-content h2 {
    margin: 16px 0 8px;
    font-size: 20px;
}

.error-content p {
    color: #94a3b8;
    font-size: 14px;
}

.loading-content {
    text-align: center;
}

.loading-spinner-lg {
    width: 48px;
    height: 48px;
    border: 4px solid rgba(59, 130, 246, 0.2);
    border-top: 4px solid #3b82f6;
    border-radius: 50%;
    animation: spin 1s linear infinite;
    margin: 0 auto 16px;
}

.console-footer {
    height: 32px;
    background-color: #1e293b;
    border-top: 1px solid #334155;
    display: flex;
    align-items: center;
    padding: 0 20px;
    font-size: 12px;
    color: #64748b;
}

.footer-hint {
    display: flex;
    align-items: center;
    gap: 8px;
}

@keyframes spin {
    to { transform: rotate(360deg); }
}

@keyframes pulse {
    0% { opacity: 0.6; }
    50% { opacity: 1; }
    100% { opacity: 0.6; }
}

.btn-primary {
    background-color: #3b82f6;
    color: white;
    border: none;
    padding: 8px 16px;
    border-radius: 6px;
    font-weight: 500;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 8px;
}

.btn-primary:hover {
    background-color: #2563eb;
}

.mt-4 { margin-top: 16px; }
</style>
