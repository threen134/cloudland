<script setup lang="ts">
// Edit the name and the description of a transit gateway. Used by the list and the detail page.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { transitGatewaysApi, type TransitGateway } from '../../api/transitGateways'
import { useToast } from '../../composables/useToast'
import { isValidName } from '../../utils/validation'
import { errorMessage } from '../../utils/error'
import BaseModal from '../modals/BaseModal.vue'

const props = defineProps<{
    /** The gateway to edit; null keeps the modal closed */
    gateway: TransitGateway | null
}>()
const emit = defineEmits<{ close: []; saved: [gateway: TransitGateway] }>()

const { t } = useI18n()
const toast = useToast()

const form = ref({ name: '', description: '' })
const saving = ref(false)
const error = ref('')
const isNameValid = computed(() => isValidName(form.value.name))

watch(
    () => props.gateway,
    (g) => {
        if (!g) return
        form.value = { name: g.name, description: g.description || '' }
        error.value = ''
    },
    { immediate: true }
)

const close = () => {
    error.value = ''
    emit('close')
}

const save = async () => {
    if (!props.gateway) return
    error.value = ''
    if (!form.value.name || !isNameValid.value) {
        error.value = t('messages.invalidHostname')
        return
    }
    saving.value = true
    try {
        const updated = await transitGatewaysApi.update(props.gateway.id, {
            name: form.value.name,
            description: form.value.description,
        })
        toast.success(t('messages.updateSuccess'))
        emit('saved', updated)
    } catch (err) {
        console.error('Failed to update transit gateway:', err)
        error.value = errorMessage(err, t('messages.error'))
    } finally {
        saving.value = false
    }
}
</script>

<template>
    <BaseModal
        :show="gateway !== null"
        :title="$t('dashboard.transitGateway.editTitle', { name: gateway?.name })"
        :loading="saving"
        form
        @close="close"
        @submit="save"
    >
        <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.name') }} *</label>
            <input
                v-model="form.name"
                type="text"
                maxlength="32"
                :class="['form-input', { 'input-error': !isNameValid }]"
            />
            <div v-if="!isNameValid" class="field-error">{{ $t('messages.invalidHostname') }}</div>
        </div>
        <div class="form-group">
            <label class="form-label">{{ $t('dashboard.forms.description') }}</label>
            <input
                v-model="form.description"
                type="text"
                maxlength="255"
                class="form-input"
                :placeholder="$t('messages.placeholderDescription')"
            />
        </div>

        <template #footer>
            <div v-if="error" class="footer-error">{{ error }}</div>
            <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">
                {{ $t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !form.name">
                {{ saving ? $t('messages.saving') : $t('actions.save') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.field-error {
    margin-top: 4px;
    font-size: var(--font-size-xs);
    color: var(--error-color);
}
</style>
