<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import { securityGroupsApi, type SecurityRule } from '../../api/networks'
import {
    newSecurityRuleForm,
    securityRuleFormFromRule,
    securityRulePayloadFromForm,
    validateSecurityRuleForm,
    type SecurityRuleForm,
} from '../../utils/securityRule'
import { errorMessage } from '../../utils/error'

// Add or edit one rule of a security group; `rule` null means add
const props = defineProps<{
    show: boolean
    groupId: string
    rule: SecurityRule | null
}>()
const emit = defineEmits<{ close: []; saved: [edited: boolean] }>()

const { t } = useI18n()
const form = ref<SecurityRuleForm>(newSecurityRuleForm())
const saving = ref(false)
const submitError = ref('')
// Live validation, the same check as on submit: shown inline and disables saving
const formError = computed(() => validateSecurityRuleForm(form.value, t))

watch(
    () => props.show,
    (show) => {
        if (!show) return
        form.value = props.rule ? securityRuleFormFromRule(props.rule) : newSecurityRuleForm()
        submitError.value = ''
    },
    { immediate: true }
)

const close = () => {
    if (!saving.value) emit('close')
}

const save = async () => {
    if (saving.value || formError.value) return
    saving.value = true
    submitError.value = ''
    try {
        const payload = securityRulePayloadFromForm(form.value)
        if (props.rule) {
            await securityGroupsApi.patchRule(props.groupId, props.rule.id, payload)
        } else {
            await securityGroupsApi.addRule(props.groupId, payload)
        }
        emit('saved', !!props.rule)
    } catch (err) {
        console.error('Failed to save rule:', err)
        submitError.value = errorMessage(err, t('messages.error'))
    } finally {
        saving.value = false
    }
}
</script>

<template>
    <!-- Not a form: Enter in a field must not save. The defaults of a new rule (ingress TCP 80 from 0.0.0.0/0)
         pass validation, and an edited rule is pushed to every node of the group; saving takes the button -->
    <BaseModal
        :show="show"
        :title="rule ? $t('actions.edit') : $t('dashboard.buttons.addRule')"
        :loading="saving"
        @close="close"
    >
        <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.name') }}</label>
            <input
                v-model="form.name"
                type="text"
                class="form-input"
                :placeholder="$t('dashboard.forms.placeholder.nameExample')"
            />
        </div>
        <div class="form-row">
            <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.table.direction') }}</label>
                <div class="select-wrapper">
                    <select v-model="form.direction" class="form-input">
                        <option value="ingress">{{ $t('dashboard.table.ingress') }}</option>
                        <option value="egress">{{ $t('dashboard.table.egress') }}</option>
                    </select>
                </div>
            </div>
            <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.table.protocol') }}</label>
                <div class="select-wrapper">
                    <select v-model="form.protocol" class="form-input">
                        <option value="tcp">TCP</option>
                        <option value="udp">UDP</option>
                        <option value="icmp">ICMP</option>
                    </select>
                </div>
            </div>
        </div>
        <div v-if="form.protocol !== 'icmp'" class="form-row">
            <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.table.portMin') }}</label>
                <input
                    v-model.number="form.port_min"
                    type="number"
                    class="form-input"
                    :class="{ 'input-error': !!formError }"
                    min="1"
                    max="65535"
                />
            </div>
            <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.table.portMax') }}</label>
                <input
                    v-model.number="form.port_max"
                    type="number"
                    class="form-input"
                    :class="{ 'input-error': !!formError }"
                    min="1"
                    max="65535"
                />
            </div>
        </div>
        <div v-else class="form-row">
            <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.securityGroupDetail.icmpType') }}</label>
                <input
                    v-model.number="form.icmp_type"
                    type="number"
                    class="form-input"
                    :class="{ 'input-error': !!formError }"
                    min="0"
                    max="254"
                    :placeholder="$t('dashboard.securityGroupDetail.icmpAnyPlaceholder')"
                />
            </div>
            <div class="form-group flex-1">
                <label class="form-label">{{ $t('dashboard.securityGroupDetail.icmpCode') }}</label>
                <input
                    v-model.number="form.icmp_code"
                    type="number"
                    class="form-input"
                    :class="{ 'input-error': !!formError }"
                    min="0"
                    max="255"
                    :placeholder="$t('dashboard.securityGroupDetail.icmpAnyPlaceholder')"
                />
            </div>
        </div>
        <div v-if="formError" class="field-error">{{ formError }}</div>
        <div class="form-group">
            <label class="form-label">{{ $t('dashboard.table.remoteCidr') }}</label>
            <input
                v-model="form.remote_cidr"
                type="text"
                class="form-input"
                :placeholder="$t('dashboard.forms.placeholder.cidrExample')"
            />
        </div>

        <template #footer>
            <div v-if="submitError" class="footer-error">{{ submitError }}</div>
            <button type="button" class="btn btn-secondary" @click="close" :disabled="saving">
                {{ $t('actions.cancel') }}
            </button>
            <button type="button" class="btn btn-primary" :disabled="saving || !!formError" @click="save">
                <span v-if="saving" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px"></span>
                {{
                    saving
                        ? rule
                            ? $t('messages.saving')
                            : $t('messages.creating')
                        : rule
                          ? $t('actions.save')
                          : $t('dashboard.buttons.addRule')
                }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.form-row {
    display: flex;
    gap: 16px;
}

.flex-1 {
    flex: 1;
    min-width: 0;
}

.input-error {
    border-color: var(--error-color) !important;
    box-shadow: 0 0 0 3px var(--error-light) !important;
}

.field-error {
    font-size: var(--font-size-xs);
    color: var(--error-color);
    margin-top: calc(-1 * var(--spacing-3));
    margin-bottom: var(--spacing-2);
}
</style>
