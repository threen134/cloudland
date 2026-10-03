<script setup lang="ts">
// Create a WireGuard client of a VPN gateway.
//
// Two phases: the form, then the result. The result phase shows the full client configuration:
// when the platform generated the key pair, the private key is inside it and is returned exactly
// once (it is never stored), so the modal must not be dismissed before the user saved the file.
// Bringing your own public key is the recommended default: the private key never leaves the device.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import VpnClientConfigBox from './VpnClientConfigBox.vue'
import { vpnClientsApi, type VpnClientCreateResponse, type VpnClientPayload } from '../../api/vpn'
import { isValidName } from '../../utils/validation'
import { errorMessage } from '../../utils/error'

const props = defineProps<{
    show: boolean
    gatewayId: string
}>()

const emit = defineEmits<{
    close: []
    /** A client was created; the parent reloads its list. Fired before the result is dismissed */
    created: [client: VpnClientCreateResponse]
}>()

const { t } = useI18n()

const WG_KEY = /^[A-Za-z0-9+/]{43}=$/

const phase = ref<'form' | 'result'>('form')
const saving = ref(false)
const error = ref('')
const result = ref<VpnClientCreateResponse | null>(null)

const form = ref({
    name: '',
    description: '',
    keyMode: 'own' as 'own' | 'generate',
    public_key: '',
    preshared_key: true,
    enabled: true,
})

const isNameValid = computed(() => isValidName(form.value.name))
const isKeyValid = computed(() => form.value.keyMode !== 'own' || WG_KEY.test(form.value.public_key.trim()))
const canSubmit = computed(
    () => !!form.value.name && isNameValid.value && (form.value.keyMode === 'generate' || !!form.value.public_key)
)

watch(
    () => props.show,
    (show) => {
        if (!show) return
        phase.value = 'form'
        error.value = ''
        result.value = null
        form.value = { name: '', description: '', keyMode: 'own', public_key: '', preshared_key: true, enabled: true }
    }
)

const submit = async () => {
    error.value = ''
    if (!form.value.name || !isNameValid.value) {
        error.value = t('messages.invalidHostname')
        return
    }
    if (!isKeyValid.value) {
        error.value = t('dashboard.vpnGateway.invalidPublicKey')
        return
    }
    saving.value = true
    try {
        const payload: VpnClientPayload = {
            name: form.value.name,
            preshared_key: form.value.preshared_key,
            enabled: form.value.enabled,
        }
        if (form.value.description) payload.description = form.value.description
        if (form.value.keyMode === 'own') payload.public_key = form.value.public_key.trim()
        const created = await vpnClientsApi.create(props.gatewayId, payload)
        result.value = created
        phase.value = 'result'
        emit('created', created)
    } catch (err) {
        error.value = errorMessage(err, t('messages.error'))
    } finally {
        saving.value = false
    }
}

const close = () => {
    if (saving.value) return
    emit('close')
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="phase === 'result' ? t('dashboard.vpnGateway.clientCreated') : t('dashboard.vpnGateway.addClient')"
        :loading="saving"
        size="lg"
        :form="phase === 'form'"
        @close="close"
        @submit="submit"
    >
        <template v-if="phase === 'form'">
            <div class="form-group">
                <label class="form-label">{{ t('dashboard.forms.name') }} *</label>
                <input
                    v-model="form.name"
                    type="text"
                    :class="['form-input', { 'input-error': !isNameValid }]"
                    :placeholder="t('dashboard.forms.placeholder.vpnClientNameExample')"
                />
                <div v-if="!isNameValid" class="text-error form-hint">{{ t('messages.invalidHostname') }}</div>
            </div>

            <div class="form-group">
                <label class="form-label"
                    >{{ t('dashboard.forms.description') }}（{{ t('dashboard.forms.optional') }}）</label
                >
                <input
                    v-model="form.description"
                    type="text"
                    class="form-input"
                    :placeholder="t('messages.placeholderDescription')"
                />
            </div>

            <div class="form-group">
                <label class="form-label">{{ t('dashboard.vpnGateway.keyMode') }}</label>
                <div class="key-mode-options">
                    <label class="key-mode-option" :class="{ selected: form.keyMode === 'own' }">
                        <input v-model="form.keyMode" type="radio" value="own" />
                        <span class="key-mode-text">
                            <span class="key-mode-title">{{ t('dashboard.vpnGateway.keyModeOwn') }}</span>
                            <span class="key-mode-hint">{{ t('dashboard.vpnGateway.keyModeOwnHint') }}</span>
                        </span>
                    </label>
                    <label class="key-mode-option" :class="{ selected: form.keyMode === 'generate' }">
                        <input v-model="form.keyMode" type="radio" value="generate" />
                        <span class="key-mode-text">
                            <span class="key-mode-title">{{ t('dashboard.vpnGateway.keyModeGenerate') }}</span>
                            <span class="key-mode-hint">{{ t('dashboard.vpnGateway.keyModeGenerateHint') }}</span>
                        </span>
                    </label>
                </div>
            </div>

            <div v-if="form.keyMode === 'own'" class="form-group">
                <label class="form-label">{{ t('dashboard.vpnGateway.publicKey') }} *</label>
                <input
                    v-model="form.public_key"
                    type="text"
                    :class="['form-input mono', { 'input-error': form.public_key && !isKeyValid }]"
                    placeholder="xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="
                    spellcheck="false"
                />
                <div v-if="form.public_key && !isKeyValid" class="text-error form-hint">
                    {{ t('dashboard.vpnGateway.invalidPublicKey') }}
                </div>
                <div v-else class="form-hint">{{ t('dashboard.vpnGateway.publicKeyHint') }}</div>
            </div>

            <div class="form-group">
                <label class="checkbox-label">
                    <input v-model="form.preshared_key" type="checkbox" />
                    {{ t('dashboard.vpnGateway.presharedKey') }}
                </label>
                <div class="form-hint">{{ t('dashboard.vpnGateway.presharedKeyHint') }}</div>
            </div>

            <div class="form-group">
                <label class="checkbox-label">
                    <input v-model="form.enabled" type="checkbox" />
                    {{ t('dashboard.vpnGateway.enabled') }}
                </label>
            </div>
        </template>

        <template v-else-if="result">
            <p class="result-intro">
                {{
                    result.private_key
                        ? t('dashboard.vpnGateway.clientCreatedGeneratedHint')
                        : t('dashboard.vpnGateway.clientCreatedOwnKeyHint')
                }}
            </p>
            <VpnClientConfigBox
                :config="result.config"
                :file-name="result.name"
                :one-time="!!result.private_key"
                :private-key-missing="!result.private_key"
            />
        </template>

        <template #footer>
            <template v-if="phase === 'form'">
                <div v-if="error" class="modal-error footer-error">{{ error }}</div>
                <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="saving || !canSubmit">
                    <span
                        v-if="saving"
                        class="loading-spinner"
                        style="width: 16px; height: 16px; border-width: 2px"
                    ></span>
                    {{ saving ? t('messages.creating') : t('dashboard.vpnGateway.addClient') }}
                </button>
            </template>
            <button v-else type="button" class="btn btn-primary" @click="close">
                {{ t('actions.close') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
/* The error sits in the footer next to the buttons: in the body it ended up below the fold of the long
   forms and a failed submit looked like nothing happened */
.modal-error.footer-error {
    flex: 1;
    align-self: center;
    margin: 0;
}
.form-hint {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: 4px;
}

.mono {
    font-family: var(--font-family-mono);
}

.checkbox-label {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
}

.key-mode-options {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-2);
}

.key-mode-option {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-3);
    padding: var(--spacing-3);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: var(--transition-base);
}

.key-mode-option:hover {
    border-color: var(--primary-300);
}

.key-mode-option.selected {
    border-color: var(--primary-color);
    background: var(--primary-50);
}

.key-mode-option input {
    margin-top: 3px;
}

.key-mode-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
}

.key-mode-title {
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    color: var(--text-primary);
}

.key-mode-hint {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    line-height: 1.5;
}

.result-intro {
    margin: 0 0 var(--spacing-4);
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.5;
}

.modal-error {
    margin-top: var(--spacing-4);
    font-size: var(--font-size-sm);
    color: var(--error-color);
    background: var(--error-light);
    padding: var(--spacing-2) var(--spacing-3);
    border-radius: var(--radius-sm);
}
</style>
