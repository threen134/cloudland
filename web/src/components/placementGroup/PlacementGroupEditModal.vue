<script setup lang="ts">
// Edit a placement group: only the name and the description can change; the policy, the strictness and the zone
// are fixed at creation (clapi refuses them with 400). Used by the list and the detail page.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { placementGroupsApi, type PlacementGroup } from '../../api/placementGroups'
import { useToast } from '../../composables/useToast'
import { isValidName } from '../../utils/validation'
import { errorMessage } from '../../utils/error'
import BaseModal from '../modals/BaseModal.vue'

const props = defineProps<{
    /** The group to edit; null keeps the modal closed */
    group: PlacementGroup | null
}>()
const emit = defineEmits<{ close: []; saved: [group: PlacementGroup] }>()

const { t } = useI18n()
const toast = useToast()

const form = ref({ name: '', description: '' })
const saving = ref(false)
const error = ref('')
const isNameValid = computed(() => isValidName(form.value.name))

watch(
    () => props.group,
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
    if (!props.group) return
    error.value = ''
    if (!form.value.name || !isNameValid.value) {
        error.value = t('messages.invalidHostname')
        return
    }
    saving.value = true
    try {
        const updated = await placementGroupsApi.update(props.group.id, {
            name: form.value.name,
            description: form.value.description,
        })
        toast.success(t('messages.success'))
        emit('saved', updated)
    } catch (err) {
        console.error('Failed to update placement group:', err)
        error.value = errorMessage(err, t('messages.error'))
    } finally {
        saving.value = false
    }
}
</script>

<template>
    <BaseModal
        :show="group !== null"
        :title="$t('dashboard.placementGroup.editTitle', { name: group?.name })"
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
        <div class="form-hint">{{ $t('dashboard.placementGroup.immutableHint') }}</div>

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

.form-hint {
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
}

/* In the footer next to the buttons, like the other forms */
.footer-error {
    flex: 1;
    align-self: center;
    padding: var(--spacing-2) var(--spacing-3);
    border-radius: var(--radius-sm);
    background: var(--error-light);
    color: var(--error-color);
    font-size: var(--font-size-sm);
}
</style>
