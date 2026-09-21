<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { instancesApi } from '../../api/instances'
import { hypervisorsApi } from '../../api/hypervisors'
import {
    SquareTerminal,
    RefreshCw,
    AlertTriangle,
    Copy,
    ClipboardPaste,
    Scaling,
    Monitor,
    LockKeyhole,
} from 'lucide-vue-next'

// Text console on the instance's first serial port, or (route host-console) a root shell on a hypervisor. The
// console proxy relays raw bytes between this websocket and the node, so the terminal emulation happens here
// in xterm.js. A host console token opens a single session: every connection asks for the password again.

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const instanceId = route.params.id as string
const isHost = route.name === 'host-console'

const container = ref<HTMLElement | null>(null)
// auth (host only, once the gateway asks for the password), connecting, connected, disconnected, error
const status = ref('connecting')
const errorMessage = ref('')
const instanceName = ref('')
const notice = ref('')
const noticeWarn = ref(false)
const password = ref('')
const authError = ref('')
const idleMinutes = ref(0)
const passwordInput = ref<HTMLInputElement | null>(null)

let term: XTerm | null = null
let fitAddon: FitAddon | null = null
let socket: WebSocket | null = null
let resizeObserver: ResizeObserver | null = null
let noticeTimer: ReturnType<typeof setTimeout> | null = null
const encoder = new TextEncoder()

const showNotice = (text: string, warn = false) => {
    notice.value = text
    noticeWarn.value = warn
    if (noticeTimer) clearTimeout(noticeTimer)
    noticeTimer = setTimeout(() => {
        notice.value = ''
    }, 2500)
}

const send = (text: string) => {
    if (socket?.readyState === WebSocket.OPEN) {
        socket.send(encoder.encode(text))
    }
}

const copySelection = async () => {
    const text = term?.getSelection()
    if (!text) {
        showNotice(t('dashboard.console.serial.nothingSelected'), true)
        return
    }
    try {
        await navigator.clipboard.writeText(text)
        showNotice(t('dashboard.console.serial.copied'))
    } catch {
        showNotice(t('dashboard.console.serial.clipboardDenied'), true)
    }
    term?.focus()
}

const pasteClipboard = async () => {
    try {
        const text = await navigator.clipboard.readText()
        // term.paste applies bracketed paste mode when the shell enabled it, so multi-line text is not run line by line
        if (text) term?.paste(text)
    } catch {
        showNotice(t('dashboard.console.serial.clipboardDenied'), true)
    }
    term?.focus()
}

// Whether the cursor sits right after a shell prompt with nothing typed yet. The size command is typed into
// the console, so anywhere else (login or password prompt, a full-screen program, a half-typed command)
// it would end up as input for that program
const atShellPrompt = () => {
    if (!term) return false
    const buffer = term.buffer.active
    const line = (
        buffer.getLine(buffer.baseY + buffer.cursorY)?.translateToString(true, 0, buffer.cursorX) ?? ''
    ).trim()
    // A bare ">" is the continuation prompt of an unfinished command, not a new prompt
    return /[$#%>]$/.test(line) && line !== '>'
}

// The serial line carries no window size: tell the shell explicitly so full-screen programs use the window
const syncSize = () => {
    if (!term) return
    if (!atShellPrompt()) {
        showNotice(t('dashboard.console.serial.syncSizeNeedsPrompt'), true)
        term.focus()
        return
    }
    // The leading space keeps the command out of the shell history (HISTCONTROL=ignoreboth on Ubuntu)
    send(` stty rows ${term.rows} cols ${term.cols}\r`)
    term.focus()
}

const setupTerminal = () => {
    if (term || !container.value) return
    term = new XTerm({
        cursorBlink: true,
        convertEol: false,
        scrollback: 5000,
        fontSize: 14,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
        theme: { background: '#000000' },
    })
    fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(container.value)
    fitAddon.fit()
    term.onData((data) => send(data))
    // Ctrl+C / Ctrl+V go to the guest; the shifted variants copy and paste like in desktop terminals
    term.attachCustomKeyEventHandler((e) => {
        if (e.type === 'keydown' && e.ctrlKey && e.shiftKey && (e.code === 'KeyC' || e.code === 'KeyV')) {
            e.preventDefault()
            if (e.code === 'KeyC') copySelection()
            else pasteClipboard()
            return false
        }
        return true
    })
    resizeObserver = new ResizeObserver(() => fitAddon?.fit())
    resizeObserver.observe(container.value)
}

const apiError = (err: any) =>
    err.response?.data?.detail ||
    err.response?.data?.error_message ||
    err.message ||
    t('dashboard.console.errorSubtitle')

// Drop the current socket: its close event must not mark a new attempt as disconnected
const detachSocket = () => {
    const old = socket
    socket = null
    old?.close()
}

const openSocket = (url: string) => {
    const ws = new WebSocket(url, ['binary'])
    ws.binaryType = 'arraybuffer'
    socket = ws
    ws.onopen = () => {
        if (socket !== ws) return
        status.value = 'connected'
        term?.focus()
        // Nothing was reading the serial line before: a carriage return brings up the prompt again.
        // A host shell starts fresh and prints its own prompt
        if (!isHost) send('\r')
    }
    ws.onmessage = (ev) => {
        if (socket !== ws) return
        term?.write(typeof ev.data === 'string' ? ev.data : new Uint8Array(ev.data))
    }
    ws.onclose = () => {
        if (socket !== ws) return
        if (status.value !== 'error') status.value = 'disconnected'
    }
    ws.onerror = () => {
        if (socket !== ws) return
        if (status.value === 'connecting') {
            status.value = 'error'
            errorMessage.value = t('dashboard.console.errorSubtitle')
        }
    }
}

const connectSerial = async () => {
    status.value = 'connecting'
    errorMessage.value = ''
    detachSocket()
    try {
        const data = await instancesApi.getConsole(instanceId, 'serial')
        // 控制台接口返回的 instance 只有 id 和 owner，主机名要单独查（同 InstanceConsole.vue）
        instanceName.value = data.instance?.id || instanceId
        instancesApi
            .getInstance(instanceId)
            .then((full) => {
                if (full.hostname) instanceName.value = full.hostname
            })
            .catch(() => {})
        const url = data.console_url
        if (!url) throw new Error('No console URL returned from API')

        await nextTick()
        setupTerminal()
        openSocket(url)
    } catch (err: any) {
        status.value = 'error'
        errorMessage.value = apiError(err)
    }
}

// Asking for the password is a system setting: the console is opened without one first, and the form is shown
// only when the gateway asks for it
const requestPassword = async (message = '') => {
    detachSocket()
    password.value = ''
    authError.value = message
    status.value = 'auth'
    await nextTick()
    passwordInput.value?.focus()
}

// Whether a host console request is on its way, so a second click does not start another one
let hostBusy = false

const connectHost = async () => {
    if (hostBusy || (status.value === 'auth' && !password.value)) return
    hostBusy = true
    status.value = 'connecting'
    errorMessage.value = ''
    authError.value = ''
    detachSocket()
    await nextTick()
    setupTerminal()
    fitAddon?.fit()
    try {
        const data = await hypervisorsApi.openConsole(instanceId, {
            password: password.value || undefined,
            rows: term?.rows,
            cols: term?.cols,
        })
        password.value = ''
        if (data.hyper?.name) instanceName.value = data.hyper.name
        idleMinutes.value = Math.round((data.idle_timeout || 0) / 60)
        if (!data.console_url) throw new Error('No console URL returned from API')
        term?.reset()
        openSocket(data.console_url)
    } catch (err: any) {
        const status400 = err.response?.status === 400
        const detail = String(err.response?.data?.detail ?? '')
        // The setting asks for the password: show the form without calling it a failure
        if (status400 && detail.includes('Password is required')) {
            await requestPassword()
            return
        }
        // A wrong password (403) or too many attempts (429) keep the password form open. A refusal from clapi
        // (the feature was turned off, or the account is not a system admin) is a 403 as well but carries an
        // error code: retyping the password would not help, so it is shown as an error
        if (!err.response?.data?.error_code && (err.response?.status === 403 || err.response?.status === 429)) {
            await requestPassword(apiError(err))
            return
        }
        status.value = 'error'
        errorMessage.value = apiError(err)
    } finally {
        hostBusy = false
    }
}

// Every attempt starts without a password: a console that does not ask for one connects right away
const connectHostFromStart = () => {
    password.value = ''
    return connectHost()
}

const connect = () => (isHost ? connectHostFromStart() : connectSerial())

const switchToGraphical = () => {
    router.replace({ name: 'instance-console', params: { id: instanceId } })
}

onMounted(() => {
    if (!isHost) {
        connectSerial()
        return
    }
    instanceName.value = instanceId
    hypervisorsApi
        .getHypervisor(instanceId)
        .then((res) => {
            if (res.hostname) instanceName.value = res.hostname
        })
        .catch(() => {})
    connectHostFromStart()
})

onUnmounted(() => {
    const ws = socket
    socket = null
    ws?.close()
    resizeObserver?.disconnect()
    term?.dispose()
    if (noticeTimer) clearTimeout(noticeTimer)
})
</script>

<template>
    <div class="console-page">
        <header class="console-header">
            <div class="header-left">
                <div class="app-icon">
                    <SquareTerminal :size="20" />
                </div>
                <div class="instance-info">
                    <h1 class="instance-name">{{ instanceName }}</h1>
                    <span class="instance-id"
                        >{{ t(isHost ? 'dashboard.console.host.title' : 'dashboard.console.serial.title') }} ·
                        {{ instanceId }}</span
                    >
                </div>
                <div :class="['status-indicator', status]">
                    <span class="status-dot"></span>
                    <span class="status-text">{{
                        status === 'auth' ? t('dashboard.console.host.passwordTitle') : t(`dashboard.console.${status}`)
                    }}</span>
                </div>
                <span v-if="notice" :class="['notice', { warn: noticeWarn }]">{{ notice }}</span>
            </div>
            <div class="header-right">
                <button
                    class="btn-console"
                    :disabled="status !== 'connected'"
                    :title="t('dashboard.console.serial.copy')"
                    @click="copySelection"
                >
                    <Copy :size="16" />
                </button>
                <button
                    class="btn-console"
                    :disabled="status !== 'connected'"
                    :title="t('dashboard.console.serial.paste')"
                    @click="pasteClipboard"
                >
                    <ClipboardPaste :size="16" />
                </button>
                <button
                    class="btn-console"
                    :disabled="status !== 'connected'"
                    :title="t('dashboard.console.serial.syncSizeHint')"
                    @click="syncSize"
                >
                    <Scaling :size="16" /> <span class="btn-text">{{ t('dashboard.console.serial.syncSize') }}</span>
                </button>
                <button
                    v-if="!isHost"
                    class="btn-console"
                    :title="t('dashboard.console.serial.switchToVnc')"
                    @click="switchToGraphical"
                >
                    <Monitor :size="16" /> <span class="btn-text">{{ t('dashboard.console.serial.switchToVnc') }}</span>
                </button>
                <button class="btn-console" :title="t('dashboard.console.reconnect')" @click="connect">
                    <RefreshCw :size="16" />
                </button>
            </div>
        </header>

        <main class="console-main">
            <div v-if="status === 'error'" class="error-overlay">
                <div class="error-content">
                    <AlertTriangle :size="48" class="text-error" />
                    <h2>{{ t('dashboard.console.error') }}</h2>
                    <p>{{ errorMessage }}</p>
                    <button class="btn-primary mt-4" @click="connect">
                        <RefreshCw :size="16" /> {{ t('dashboard.console.tryAgain') }}
                    </button>
                </div>
            </div>
            <div v-if="status === 'auth'" class="error-overlay">
                <form class="error-content auth-form" @submit.prevent="connectHost">
                    <LockKeyhole :size="40" class="text-warn" />
                    <h2>{{ t('dashboard.console.host.passwordTitle') }}</h2>
                    <p>{{ t('dashboard.console.host.passwordDesc', { name: instanceName }) }}</p>
                    <!-- The username field lets password managers fill in the password of the current account -->
                    <input
                        type="text"
                        autocomplete="username"
                        class="visually-hidden"
                        tabindex="-1"
                        aria-hidden="true"
                    />
                    <input
                        ref="passwordInput"
                        v-model="password"
                        type="password"
                        class="password-input"
                        autocomplete="current-password"
                        :placeholder="t('dashboard.console.host.passwordPlaceholder')"
                    />
                    <p v-if="authError" class="auth-error">
                        {{ t('dashboard.console.host.authFailed') }}: {{ authError }}
                    </p>
                    <button type="submit" class="btn-primary mt-4" :disabled="!password">
                        <SquareTerminal :size="16" /> {{ t('dashboard.console.host.open') }}
                    </button>
                </form>
            </div>
            <div v-if="status === 'connecting'" class="loading-overlay">
                <div class="loading-content">
                    <div class="loading-spinner-lg"></div>
                    <p>{{ t('dashboard.console.connecting') }}</p>
                </div>
            </div>
            <div ref="container" class="terminal-container"></div>
        </main>

        <footer class="console-footer">
            <span v-if="isHost">{{ t('dashboard.console.host.hint', { minutes: idleMinutes || 15 }) }}</span>
            <span v-else>{{ t('dashboard.console.serial.hint') }}</span>
        </footer>
    </div>
</template>

<style scoped>
.console-page {
    display: flex;
    flex-direction: column;
    height: 100vh;
    width: 100vw;
    background-color: var(--bg-dark);
    color: #f1f5f9;
    overflow: hidden;
}

.console-header {
    min-height: 60px;
    background-color: #1e293b;
    border-bottom: 1px solid #334155;
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    padding: 8px 20px;
    flex-shrink: 0;
}

.header-left,
.header-right {
    display: flex;
    align-items: center;
    gap: 12px;
}

.header-right {
    gap: 6px;
    flex-wrap: wrap;
}

.app-icon {
    width: 36px;
    height: 36px;
    background: linear-gradient(135deg, var(--success-color), #059669);
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
    color: #f1f5f9;
    margin: 0;
    line-height: 1.2;
}

.instance-id {
    font-size: 11px;
    color: var(--text-tertiary);
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}

.status-indicator {
    display: flex;
    align-items: center;
    gap: 8px;
    background-color: var(--bg-dark);
    padding: 4px 12px;
    border-radius: 100px;
    font-size: 12px;
    border: 1px solid #334155;
}

.status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background-color: #94a3b8;
}

.status-indicator.connecting .status-dot {
    background-color: #eab308;
}
.status-indicator.connected .status-dot {
    background-color: #22c55e;
    box-shadow: 0 0 8px #22c55e;
}
.status-indicator.error .status-dot {
    background-color: #ef4444;
}

.notice {
    font-size: 12px;
    color: #22c55e;
}

.notice.warn {
    color: #eab308;
}

.btn-console {
    background-color: #334155;
    border: 1px solid #475569;
    color: #f1f5f9;
    height: 32px;
    padding: 0 10px;
    border-radius: 6px;
    font-size: 13px;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 6px;
}

.btn-console:hover:not(:disabled) {
    background-color: #475569;
}

.btn-console:disabled {
    opacity: 0.45;
    cursor: not-allowed;
}

.console-main {
    flex-grow: 1;
    min-height: 0;
    position: relative;
    background-color: #000;
    padding: 6px 0 0 8px;
}

.terminal-container {
    width: 100%;
    height: 100%;
}

.error-overlay,
.loading-overlay {
    position: absolute;
    inset: 0;
    background-color: rgba(15, 23, 42, 0.9);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 5;
}

.error-content {
    max-width: 420px;
    text-align: center;
    padding: 32px;
    background-color: #1e293b;
    border: 1px solid #334155;
    border-radius: 10px;
}

.error-content h2 {
    margin: 16px 0 8px;
    font-size: 20px;
    color: #f1f5f9;
}

.error-content p {
    color: var(--text-tertiary);
    font-size: 14px;
}

.text-error {
    color: #ef4444;
}

.text-warn {
    color: #eab308;
}

.auth-form {
    width: min(420px, calc(100vw - 32px));
    box-sizing: border-box;
}

.password-input {
    width: 100%;
    box-sizing: border-box;
    margin-top: 12px;
    height: 38px;
    padding: 0 12px;
    border-radius: 6px;
    border: 1px solid #475569;
    background-color: var(--bg-dark);
    color: #f1f5f9;
    font-size: 14px;
}

.password-input:focus {
    outline: none;
    border-color: #3b82f6;
}

.auth-error {
    margin: 10px 0 0;
    color: #f87171 !important;
    font-size: 13px !important;
}

.btn-primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}

.visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    opacity: 0;
    pointer-events: none;
}

.loading-content {
    text-align: center;
}

.loading-spinner-lg {
    width: 48px;
    height: 48px;
    border: 4px solid rgba(16, 185, 129, 0.2);
    border-top: 4px solid var(--success-color);
    border-radius: 50%;
    animation: spin 1s linear infinite;
    margin: 0 auto 16px;
}

@keyframes spin {
    to {
        transform: rotate(360deg);
    }
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

.mt-4 {
    margin-top: 16px;
}

.console-footer {
    min-height: 32px;
    background-color: #1e293b;
    border-top: 1px solid #334155;
    display: flex;
    align-items: center;
    padding: 4px 20px;
    font-size: 12px;
    color: var(--gray-500);
}

@media (max-width: 700px) {
    .console-header {
        padding: 8px 12px;
    }

    .instance-id,
    .btn-text,
    .console-footer {
        display: none;
    }
}
</style>
