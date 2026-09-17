<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { instancesApi } from '../../api/instances'
import RFB from '@novnc/novnc/lib/rfb'
import KeyTable from '@novnc/novnc/lib/input/keysym'
import { traceVncConnection } from '../../tracing'
import { Terminal, RefreshCw, AlertTriangle, Monitor, Keyboard, ClipboardPaste, ChevronDown, X, SquareTerminal } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
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
        traceVncConnection(rfb.value)

        rfb.value.addEventListener('connect', () => {
            status.value = 'connected'
            console.log('VNC Connected')
        })

        rfb.value.addEventListener('disconnect', (e: any) => {
            // The server side drops pressed keys with the connection
            resetModifiers()
            cancelTyping()
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
        // QEMU's VNC server rejects resize requests ("Invalid screen layout"); scaling is done locally instead
        rfb.value.resizeSession = false
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

// ─── Extra keys ──────────────────────────────────────────────────────────────
// Keys are sent as keysyms without a physical key code and QEMU maps them to keys with its keymap
// (US layout by default). It does not press Shift by itself, see needsShift.

const isConnected = computed(() => status.value === 'connected')

const sendKeysym = (keysym: number, down?: boolean) => {
    if (rfb.value && isConnected.value) {
        rfb.value.sendKey(keysym, null, down)
    }
}

// Presses the keys in order and releases them in reverse order
const sendCombo = (keysyms: number[]) => {
    keysyms.forEach(k => queueKey(k, true))
    ;[...keysyms].reverse().forEach(k => queueKey(k, false))
    showFnMenu.value = false
    rfb.value?.focus()
}

// Modifiers stay pressed on the guest while active. Toolbar toggles are sticky (released by clicking again),
// so shortcuts the browser would intercept (Ctrl+W, Alt+Tab, Win) can be combined with the physical keyboard;
// modifiers pressed on the virtual keyboard are one-shot and released after the next key.
const modifierKeysyms = { shift: KeyTable.XK_Shift_L, ctrl: KeyTable.XK_Control_L, alt: KeyTable.XK_Alt_L, meta: KeyTable.XK_Super_L }
type Modifier = keyof typeof modifierKeysyms
const modifiers = reactive<Record<Modifier, boolean>>({ shift: false, ctrl: false, alt: false, meta: false })
const oneShotModifiers = new Set<Modifier>()

const toggleModifier = (name: Modifier, oneShot = false) => {
    modifiers[name] = !modifiers[name]
    if (modifiers[name] && oneShot) {
        oneShotModifiers.add(name)
    } else {
        oneShotModifiers.delete(name)
    }
    queueKey(modifierKeysyms[name], modifiers[name])
    rfb.value?.focus()
}

const releaseModifiers = (onlyOneShot = false) => {
    for (const name of Object.keys(modifiers) as Modifier[]) {
        if (modifiers[name] && (!onlyOneShot || oneShotModifiers.has(name))) {
            modifiers[name] = false
            oneShotModifiers.delete(name)
            queueKey(modifierKeysyms[name], false)
        }
    }
}

const resetModifiers = () => {
    modifiers.shift = modifiers.ctrl = modifiers.alt = modifiers.meta = false
    oneShotModifiers.clear()
    capsLock.value = false
}

const showFnMenu = ref(false)
const fnKeys = Array.from({ length: 12 }, (_, i) => ({ label: `F${i + 1}`, keysym: KeyTable.XK_F1 + i }))
// Ctrl+Alt+F1..F6 switch Linux virtual terminals
const vtKeys = fnKeys.slice(0, 6)

// ─── Typing text (paste and on-screen keyboard) ─────────────────────────────

// QEMU maps a plain keysym to the key it is printed on but does not press Shift for it ('A' arrives as 'a',
// '>' as '.'), so characters that need Shift on the US layout (QEMU's default keymap) are sent with Shift held
const shiftedSymbols = '~!@#$%^&*()_+{}|:"<>?'
const needsShift = (ch: string) => (ch >= 'A' && ch <= 'Z') || shiftedSymbols.includes(ch)

const charKeysym = (ch: string): number | null => {
    if (ch === '\n') return KeyTable.XK_Return
    if (ch === '\t') return KeyTable.XK_Tab
    const code = ch.codePointAt(0) ?? 0
    // Printable ASCII keysyms equal their code points; other characters have no key on the guest keymap
    return code >= 0x20 && code <= 0x7e ? code : null
}

const typeDelayMs = 8
const typing = ref(false)
const typedCount = ref(0)
const typingTotal = ref(0)
let typingCancelled = false
// Serializes typing so text from the keyboard and a paste never interleave
let typingChain: Promise<void> = Promise.resolve()

const typeText = (text: string) => {
    typingChain = typingChain.then(async () => {
        const chars = Array.from(text.replace(/\r\n?/g, '\n'))
        typingCancelled = false
        typing.value = true
        typedCount.value = 0
        typingTotal.value = chars.length
        for (const ch of chars) {
            if (typingCancelled || !isConnected.value) break
            const keysym = charKeysym(ch)
            if (keysym !== null) {
                const shift = needsShift(ch)
                if (shift) sendKeysym(KeyTable.XK_Shift_L, true)
                sendKeysym(keysym)
                if (shift) sendKeysym(KeyTable.XK_Shift_L, false)
                await new Promise(resolve => setTimeout(resolve, typeDelayMs))
            }
            typedCount.value++
        }
        typing.value = false
    })
    return typingChain
}

// Sends a key press (or only down / up) after the text already queued, keeping the order of all keys
const queueKey = (keysym: number, down?: boolean) => {
    typingChain = typingChain.then(() => sendKeysym(keysym, down))
}

const cancelTyping = () => {
    typingCancelled = true
}

// Paste dialog
const showPaste = ref(false)
const pasteText = ref('')
const pasteFinished = ref(false)
const unsupportedCount = computed(() => Array.from(pasteText.value.replace(/\r/g, '')).filter(ch => charKeysym(ch) === null).length)

const openPaste = async () => {
    pasteFinished.value = false
    showPaste.value = true
    // Prefill from the clipboard when the browser allows it (secure context and user permission)
    if (!pasteText.value && navigator.clipboard?.readText) {
        try {
            pasteText.value = await navigator.clipboard.readText()
        } catch {
            // Permission denied: the user pastes into the text area manually
        }
    }
}

const closePaste = () => {
    cancelTyping()
    showPaste.value = false
    rfb.value?.focus()
}

const sendPaste = async () => {
    if (!pasteText.value || !isConnected.value) return
    // Held modifiers would turn the text into shortcuts
    releaseModifiers()
    pasteFinished.value = false
    await typeText(pasteText.value)
    pasteFinished.value = !typingCancelled
}

// ─── Virtual keyboard ────────────────────────────────────────────────────────

interface VKey {
    // Character key: the character typed without and with Shift
    base?: string
    shifted?: string
    // Non-character key
    label?: string
    keysym?: number
    modifier?: Modifier
    caps?: boolean
    // Relative width, 1 = a letter key
    width?: number
}

const chars = (row: string, shiftedRow: string): VKey[] =>
    Array.from(row).map((base, i) => ({ base, shifted: shiftedRow[i] }))

const vkRows: VKey[][] = [
    [
        { label: 'Esc', keysym: KeyTable.XK_Escape },
        ...fnKeys.map(k => ({ label: k.label, keysym: k.keysym })),
        { label: 'Ins', keysym: KeyTable.XK_Insert },
        { label: 'Del', keysym: KeyTable.XK_Delete },
        { label: 'Home', keysym: KeyTable.XK_Home },
        { label: 'End', keysym: KeyTable.XK_End },
        { label: 'PgUp', keysym: KeyTable.XK_Page_Up },
        { label: 'PgDn', keysym: KeyTable.XK_Page_Down },
    ],
    [...chars('`1234567890-=', '~!@#$%^&*()_+'), { label: '⌫', keysym: KeyTable.XK_BackSpace, width: 2 }],
    [{ label: 'Tab', keysym: KeyTable.XK_Tab, width: 1.5 }, ...chars('qwertyuiop[]\\', 'QWERTYUIOP{}|')],
    [{ label: 'Caps', caps: true, width: 1.75 }, ...chars("asdfghjkl;'", 'ASDFGHJKL:"'), { label: 'Enter ↵', keysym: KeyTable.XK_Return, width: 2.25 }],
    [{ label: 'Shift', modifier: 'shift', width: 2.25 }, ...chars('zxcvbnm,./', 'ZXCVBNM<>?'), { label: 'Shift', modifier: 'shift', width: 2.75 }],
    [
        { label: 'Ctrl', modifier: 'ctrl', width: 1.5 },
        { label: 'Win', modifier: 'meta', width: 1.25 },
        { label: 'Alt', modifier: 'alt', width: 1.25 },
        { base: ' ', shifted: ' ', label: 'Space', width: 6 },
        { label: '←', keysym: KeyTable.XK_Left },
        { label: '↑', keysym: KeyTable.XK_Up },
        { label: '↓', keysym: KeyTable.XK_Down },
        { label: '→', keysym: KeyTable.XK_Right },
    ],
]

const showVirtualKeyboard = ref(false)
// Caps Lock is emulated in the browser (upper case letters are typed with Shift) so the guest's own
// Caps Lock state never gets out of sync with the key labels
const capsLock = ref(false)

const isLetter = (ch: string) => /^[a-z]$/i.test(ch)

// The character a key produces with the current Shift / Caps state
const vkChar = (key: VKey): string => {
    const base = key.base ?? ''
    if (isLetter(base)) {
        return modifiers.shift !== capsLock.value ? key.shifted! : base
    }
    return modifiers.shift ? key.shifted! : base
}

const vkLabel = (key: VKey) => (key.base !== undefined && key.label === undefined ? vkChar(key) : key.label)

const vkActive = (key: VKey) => (key.modifier ? modifiers[key.modifier] : key.caps ? capsLock.value : false)

const pressVirtualKey = (key: VKey) => {
    if (!isConnected.value) return
    if (key.modifier) {
        toggleModifier(key.modifier, true)
        return
    }
    if (key.caps) {
        capsLock.value = !capsLock.value
        return
    }
    if (key.base !== undefined) {
        const ch = vkChar(key)
        const keysym = charKeysym(ch)!
        const otherModifiers = modifiers.ctrl || modifiers.alt || modifiers.meta
        if (modifiers.shift) {
            // Shift is already held on the guest; with Caps inverting it a lower case letter needs it released
            if (!needsShift(ch)) queueKey(KeyTable.XK_Shift_L, false)
            queueKey(keysym)
            if (!needsShift(ch)) queueKey(KeyTable.XK_Shift_L, true)
        } else if (otherModifiers) {
            // Shortcut such as Ctrl+C: send the key as is, adding Shift only for shifted characters
            if (needsShift(ch)) queueKey(KeyTable.XK_Shift_L, true)
            queueKey(keysym)
            if (needsShift(ch)) queueKey(KeyTable.XK_Shift_L, false)
        } else {
            typeText(ch)
        }
    } else if (key.keysym !== undefined) {
        queueKey(key.keysym)
    }
    releaseModifiers(true)
}

const toggleVirtualKeyboard = () => {
    showVirtualKeyboard.value = !showVirtualKeyboard.value
    if (!showVirtualKeyboard.value) {
        releaseModifiers(true)
        keyboardInput.value?.blur()
    }
}

// System keyboard: a hidden text field brings up the soft keyboard on touch devices, and what is
// typed into it is forwarded as key presses
const keyboardInput = ref<HTMLTextAreaElement | null>(null)
const keyboardActive = ref(false)

const toggleKeyboard = () => {
    if (keyboardActive.value) {
        keyboardInput.value?.blur()
    } else {
        keyboardInput.value?.focus()
    }
}

const keyboardSpecialKeys: Record<string, number> = {
    Backspace: KeyTable.XK_BackSpace,
    Enter: KeyTable.XK_Return,
    Tab: KeyTable.XK_Tab,
    Escape: KeyTable.XK_Escape,
    ArrowUp: KeyTable.XK_Up,
    ArrowDown: KeyTable.XK_Down,
    ArrowLeft: KeyTable.XK_Left,
    ArrowRight: KeyTable.XK_Right,
    Delete: KeyTable.XK_Delete,
}

const onKeyboardKeydown = (e: KeyboardEvent) => {
    if (e.isComposing) return
    const keysym = keyboardSpecialKeys[e.key]
    if (keysym !== undefined) {
        e.preventDefault()
        // Text still pending from earlier input events has to reach the guest first
        flushKeyboardInput()
        queueKey(keysym)
    }
}

const flushKeyboardInput = () => {
    const el = keyboardInput.value
    if (!el || !el.value) return
    typeText(el.value)
    el.value = ''
}

const onKeyboardInput = (e: Event) => {
    // Wait for the input method to commit composed text
    if ((e as InputEvent).isComposing) return
    flushKeyboardInput()
}

const switchToSerial = () => {
    router.replace({ name: 'instance-serial-console', params: { id: instanceId } })
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
    <div :class="['console-page', { 'vk-open': showVirtualKeyboard }]">
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
                <div class="key-group key-group-extra" :title="t('dashboard.console.stickyHint')">
                    <button
                        v-for="m in (['ctrl', 'alt', 'meta'] as const)"
                        :key="m"
                        :class="['btn-console', 'btn-key', { active: modifiers[m] }]"
                        :disabled="!isConnected"
                        :aria-pressed="modifiers[m]"
                        @click="toggleModifier(m)"
                    >{{ t(`dashboard.console.keys.${m}`) }}</button>
                </div>
                <div class="key-group key-group-extra">
                    <button class="btn-console btn-key" :disabled="!isConnected" @click="sendCombo([KeyTable.XK_Tab])">Tab</button>
                    <button class="btn-console btn-key" :disabled="!isConnected" @click="sendCombo([KeyTable.XK_Escape])">Esc</button>
                    <div class="fn-dropdown">
                        <button class="btn-console btn-key" :disabled="!isConnected" @click="showFnMenu = !showFnMenu">
                            {{ t('dashboard.console.fnKeys') }} <ChevronDown :size="14" />
                        </button>
                        <div v-if="showFnMenu" class="fn-backdrop" @click="showFnMenu = false"></div>
                        <div v-if="showFnMenu" class="fn-menu">
                            <div class="fn-grid">
                                <button v-for="k in fnKeys" :key="k.label" class="fn-item" @click="sendCombo([k.keysym])">{{ k.label }}</button>
                            </div>
                            <div class="fn-divider"></div>
                            <div class="fn-grid">
                                <button
                                    v-for="k in vtKeys"
                                    :key="`vt-${k.label}`"
                                    class="fn-item fn-item-wide"
                                    @click="sendCombo([KeyTable.XK_Control_L, KeyTable.XK_Alt_L, k.keysym])"
                                >Ctrl+Alt+{{ k.label }}</button>
                            </div>
                        </div>
                    </div>
                </div>
                <div class="key-group">
                    <button class="btn-console btn-key" :disabled="!isConnected" @click="sendCtrlAltDel" :title="t('dashboard.console.cad')">
                        Ctrl+Alt+Del
                    </button>
                    <button
                        :class="['btn-console', { active: showVirtualKeyboard }]"
                        :disabled="!isConnected"
                        :title="t('dashboard.console.virtualKeyboard')"
                        :aria-pressed="showVirtualKeyboard"
                        @click="toggleVirtualKeyboard"
                    >
                        <Keyboard :size="16" />
                    </button>
                    <button class="btn-console" :disabled="!isConnected" :title="t('dashboard.console.paste')" @click="openPaste">
                        <ClipboardPaste :size="16" />
                    </button>
                    <button class="btn-console" :title="t('dashboard.console.serial.switchToSerial')" @click="switchToSerial">
                        <SquareTerminal :size="16" />
                    </button>
                    <button class="btn-console" @click="reload" :title="t('dashboard.console.reconnect')">
                        <RefreshCw :size="16" />
                    </button>
                </div>
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

            <!-- On-screen keyboard target: focusing it opens the soft keyboard on touch devices -->
            <textarea
                ref="keyboardInput"
                class="keyboard-input"
                autocapitalize="off"
                autocomplete="off"
                autocorrect="off"
                spellcheck="false"
                tabindex="-1"
                aria-hidden="true"
                @focus="keyboardActive = true"
                @blur="keyboardActive = false"
                @keydown="onKeyboardKeydown"
                @input="onKeyboardInput"
                @compositionend="flushKeyboardInput"
            ></textarea>
        </main>

        <!-- Virtual keyboard: mousedown is prevented so clicking keys keeps the focus on the console -->
        <section v-if="showVirtualKeyboard" class="vk-panel" :aria-label="t('dashboard.console.virtualKeyboard')">
            <div class="vk-header">
                <span class="vk-title"><Keyboard :size="14" /> {{ t('dashboard.console.virtualKeyboard') }}</span>
                <div class="vk-header-actions">
                    <button
                        :class="['vk-action', { active: keyboardActive }]"
                        :disabled="!isConnected"
                        @mousedown.prevent
                        @click="toggleKeyboard"
                    >{{ t('dashboard.console.systemKeyboard') }}</button>
                    <button class="vk-action" @mousedown.prevent @click="toggleVirtualKeyboard" :aria-label="t('dashboard.console.hideKeyboard')">
                        <ChevronDown :size="16" />
                    </button>
                </div>
            </div>
            <div :class="['vk-rows', { disabled: !isConnected }]">
                <div v-for="(row, r) in vkRows" :key="r" :class="['vk-row', { 'vk-row-fn': r === 0 }]">
                    <button
                        v-for="(key, i) in row"
                        :key="i"
                        :class="['vk-key', { active: vkActive(key), 'vk-key-mod': key.modifier || key.caps, 'vk-key-char': key.label === undefined }]"
                        :style="{ flexGrow: key.width ?? 1 }"
                        :data-key="key.base ?? key.label"
                        :disabled="!isConnected"
                        :aria-pressed="key.modifier || key.caps ? vkActive(key) : undefined"
                        @mousedown.prevent
                        @click="pressVirtualKey(key)"
                    >{{ vkLabel(key) }}</button>
                </div>
            </div>
        </section>

        <div v-if="showPaste" class="paste-overlay" @click.self="closePaste">
            <div class="paste-dialog">
                <div class="paste-header">
                    <h3>{{ t('dashboard.console.pasteTitle') }}</h3>
                    <button class="btn-icon" @click="closePaste" :aria-label="t('actions.cancel')"><X :size="18" /></button>
                </div>
                <p class="paste-hint">{{ t('dashboard.console.pasteHint') }}</p>
                <textarea
                    v-model="pasteText"
                    class="paste-textarea"
                    :placeholder="t('dashboard.console.pastePlaceholder')"
                    :disabled="typing"
                    spellcheck="false"
                ></textarea>
                <div class="paste-status">
                    <span v-if="typing">{{ t('dashboard.console.pasteProgress', { done: typedCount, total: typingTotal }) }}</span>
                    <span v-else-if="pasteFinished" class="paste-done">{{ t('dashboard.console.pasteDone') }}</span>
                    <span v-else-if="unsupportedCount > 0" class="paste-warning">{{ t('dashboard.console.pasteUnsupported', { count: unsupportedCount }) }}</span>
                    <span v-else>{{ t('dashboard.console.pasteCount', { count: Array.from(pasteText).length }) }}</span>
                </div>
                <div class="paste-footer">
                    <button v-if="typing" class="btn-console" @click="cancelTyping">{{ t('dashboard.console.pasteStop') }}</button>
                    <button v-else class="btn-console" @click="closePaste">{{ t('actions.cancel') }}</button>
                    <button class="btn-primary" :disabled="typing || !pasteText || !isConnected" @click="sendPaste">
                        <ClipboardPaste :size="16" /> {{ t('dashboard.console.pasteSend') }}
                    </button>
                </div>
            </div>
        </div>

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
    /* Global heading colors are meant for the light dashboard theme */
    color: #f1f5f9;
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
    /* Lets the console area shrink when the virtual keyboard takes space, instead of growing the page */
    min-height: 0;
    overflow: hidden;
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
    color: #f1f5f9;
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

.btn-primary:disabled,
.btn-console:disabled {
    opacity: 0.45;
    cursor: not-allowed;
}

/* Extra keys toolbar */
.header-right {
    flex-wrap: wrap;
    justify-content: flex-end;
}

.key-group {
    display: flex;
    align-items: center;
    gap: 4px;
    padding-left: 8px;
    border-left: 1px solid #334155;
}

.key-group:first-child {
    border-left: none;
    padding-left: 0;
}

.btn-key {
    padding: 0 10px;
    font-size: 12px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}

.btn-console.active {
    background-color: #2563eb;
    border-color: #3b82f6;
    color: #fff;
}

.fn-dropdown {
    position: relative;
}

.fn-backdrop {
    position: fixed;
    inset: 0;
    z-index: 19;
}

.fn-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    z-index: 20;
    background-color: #1e293b;
    border: 1px solid #334155;
    border-radius: 8px;
    padding: 8px;
    box-shadow: 0 10px 25px rgba(0, 0, 0, 0.4);
}

.fn-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(48px, 1fr));
    gap: 4px;
}

.fn-grid:has(.fn-item-wide) {
    grid-template-columns: repeat(2, minmax(110px, 1fr));
}

.fn-item {
    background-color: #334155;
    border: 1px solid #475569;
    color: #f1f5f9;
    border-radius: 4px;
    padding: 6px 8px;
    font-size: 12px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    cursor: pointer;
    white-space: nowrap;
}

.fn-item:hover {
    background-color: #475569;
}

.fn-divider {
    height: 1px;
    background-color: #334155;
    margin: 8px 0;
}

/* Virtual keyboard */
.vk-panel {
    flex-shrink: 0;
    background-color: #1e293b;
    border-top: 1px solid #334155;
    padding: 6px 12px 10px;
    user-select: none;
    -webkit-user-select: none;
}

.vk-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 6px;
}

.vk-title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: #94a3b8;
}

.vk-header-actions {
    display: flex;
    gap: 6px;
}

.vk-action {
    background-color: #334155;
    border: 1px solid #475569;
    color: #f1f5f9;
    border-radius: 6px;
    height: 26px;
    padding: 0 10px;
    font-size: 12px;
    cursor: pointer;
    display: flex;
    align-items: center;
}

.vk-action.active {
    background-color: #2563eb;
    border-color: #3b82f6;
}

.vk-rows {
    display: flex;
    flex-direction: column;
    gap: 4px;
    max-width: 1100px;
    margin: 0 auto;
}

.vk-row {
    display: flex;
    gap: 4px;
}

.vk-key {
    flex-basis: 0;
    min-width: 0;
    height: 38px;
    background-color: #334155;
    border: 1px solid #475569;
    border-bottom-width: 2px;
    color: #f1f5f9;
    border-radius: 5px;
    font-size: 13px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    cursor: pointer;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    padding: 0 2px;
    touch-action: manipulation;
}

.vk-key-char {
    font-size: 15px;
}

.vk-row-fn .vk-key {
    height: 28px;
    font-size: 11px;
}

.vk-key:hover:not(:disabled) {
    background-color: #475569;
}

.vk-key:active:not(:disabled) {
    background-color: #64748b;
    transform: translateY(1px);
}

.vk-key.vk-key-mod {
    color: #cbd5e1;
}

.vk-key.active {
    background-color: #2563eb;
    border-color: #3b82f6;
    color: #fff;
}

.vk-rows.disabled {
    opacity: 0.45;
}

@media (max-width: 700px) {
    .vk-panel {
        padding: 4px 4px 8px;
    }

    .vk-row {
        gap: 2px;
    }

    .vk-key {
        height: 34px;
        font-size: 11px;
        border-radius: 4px;
    }

    .vk-key-char {
        font-size: 13px;
    }

    /* The virtual keyboard has these keys; hiding them keeps the header to one row on phones */
    .vk-open .key-group-extra {
        display: none;
    }

    /* 19 function / navigation keys do not fit one phone-width row: wrap them into two */
    .vk-row-fn {
        flex-wrap: wrap;
    }

    .vk-row-fn .vk-key {
        flex: 1 0 calc(10% - 2px) !important;
        font-size: 10px;
    }
}

/* Kept in the layout (not display:none) so it can take focus and raise the soft keyboard */
.keyboard-input {
    position: absolute;
    left: 0;
    bottom: 0;
    width: 1px;
    height: 1px;
    opacity: 0;
    border: 0;
    padding: 0;
    resize: none;
    font-size: 16px; /* avoids iOS zooming into the field */
}

/* Paste dialog */
.paste-overlay {
    position: fixed;
    inset: 0;
    z-index: 30;
    background-color: rgba(15, 23, 42, 0.75);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 16px;
}

.paste-dialog {
    width: min(560px, 100%);
    background-color: #1e293b;
    border: 1px solid #334155;
    border-radius: 10px;
    padding: 20px;
    box-shadow: 0 20px 25px -5px rgba(0, 0, 0, 0.4);
}

.paste-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
}

.paste-header h3 {
    margin: 0;
    font-size: 16px;
    color: #f1f5f9;
}

.btn-icon {
    background: none;
    border: none;
    color: #94a3b8;
    cursor: pointer;
    display: flex;
}

.paste-hint {
    color: #94a3b8;
    font-size: 13px;
    margin: 8px 0 12px;
    line-height: 1.5;
}

.paste-textarea {
    width: 100%;
    min-height: 160px;
    box-sizing: border-box;
    resize: vertical;
    background-color: #0f172a;
    color: #f1f5f9;
    border: 1px solid #334155;
    border-radius: 6px;
    padding: 10px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 13px;
}

.paste-status {
    font-size: 12px;
    color: #94a3b8;
    margin-top: 8px;
    min-height: 18px;
}

.paste-warning {
    color: #eab308;
}

.paste-done {
    color: #22c55e;
}

.paste-footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 12px;
}

@media (max-width: 900px) {
    .console-header {
        height: auto;
        min-height: 60px;
        flex-wrap: wrap;
        gap: 8px;
        padding: 8px 12px;
    }

    .instance-id,
    .console-footer {
        display: none;
    }
}
</style>
