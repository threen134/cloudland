<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from 'lucide-vue-next'
import { securityGroupsApi, type SecurityRule } from '../../api/networks'
import {
    newSecurityRuleForm, securityRuleFormFromRule, securityRulePayloadFromForm, validateSecurityRuleForm,
    type SecurityRuleForm
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

watch(() => props.show, (show) => {
    if (!show) return
    form.value = props.rule ? securityRuleFormFromRule(props.rule) : newSecurityRuleForm()
    submitError.value = ''
}, { immediate: true })

const close = () => {
    if (!saving.value) emit('close')
}

const save = async () => {
    if (formError.value) return
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
  <div v-if="show" class="modal-overlay" @click.self="close">
    <div class="modal-content card">
      <div class="modal-header">
        <h3>{{ rule ? $t('actions.edit') : $t('dashboard.buttons.addRule') }}</h3>
        <button class="btn btn-ghost btn-sm icon-btn" @click="close"><X :size="20" /></button>
      </div>
      <div class="modal-body">
        <div class="form-group">
          <label class="form-label">{{ $t('dashboard.table.name') }}</label>
          <input v-model="form.name" type="text" class="form-input" :placeholder="$t('dashboard.forms.placeholder.nameExample')">
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
            <input v-model.number="form.port_min" type="number" class="form-input" :class="{ 'input-error': !!formError }" min="1" max="65535">
          </div>
          <div class="form-group flex-1">
            <label class="form-label">{{ $t('dashboard.table.portMax') }}</label>
            <input v-model.number="form.port_max" type="number" class="form-input" :class="{ 'input-error': !!formError }" min="1" max="65535">
          </div>
        </div>
        <div v-else class="form-row">
          <div class="form-group flex-1">
            <label class="form-label">{{ $t('dashboard.securityGroupDetail.icmpType') }}</label>
            <input v-model.number="form.icmp_type" type="number" class="form-input" :class="{ 'input-error': !!formError }" min="0" max="254" :placeholder="$t('dashboard.securityGroupDetail.icmpAnyPlaceholder')">
          </div>
          <div class="form-group flex-1">
            <label class="form-label">{{ $t('dashboard.securityGroupDetail.icmpCode') }}</label>
            <input v-model.number="form.icmp_code" type="number" class="form-input" :class="{ 'input-error': !!formError }" min="0" max="255" :placeholder="$t('dashboard.securityGroupDetail.icmpAnyPlaceholder')">
          </div>
        </div>
        <div v-if="formError" class="field-error">{{ formError }}</div>
        <div class="form-group">
          <label class="form-label">{{ $t('dashboard.table.remoteCidr') }}</label>
          <input v-model="form.remote_cidr" type="text" class="form-input" :placeholder="$t('dashboard.forms.placeholder.cidrExample')">
        </div>
        <div v-if="submitError" class="submit-error">{{ submitError }}</div>
      </div>
      <div class="modal-footer">
        <button class="btn btn-secondary" @click="close" :disabled="saving">{{ $t('actions.cancel') }}</button>
        <button class="btn btn-primary" @click="save" :disabled="saving || !!formError">
          <span v-if="saving" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
          {{ saving ? (rule ? $t('messages.saving') : $t('messages.creating')) : (rule ? $t('actions.save') : $t('dashboard.buttons.addRule')) }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.form-row {
  display: flex;
  gap: 16px;
}

.flex-1 { flex: 1; min-width: 0; }

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

.submit-error {
  color: var(--error-color);
  font-size: var(--font-size-sm);
  background: var(--error-light);
  padding: var(--spacing-2);
  border-radius: var(--radius-sm);
}
</style>
