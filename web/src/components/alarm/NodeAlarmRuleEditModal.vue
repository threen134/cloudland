<script setup lang="ts">
// 节点告警规则的编辑弹窗，列表页和详情页共用。
// 规则类型不能改（后端每种类型只允许存在一条），所以只有名称、描述、配置三项。
//
// 配置的键必须和规则类型对应的 .j2 模板变量对得上，否则后端会明确报错并列出支持的键
// （见 cpgateway 转发的 clapi services/alarm.go 的 validateNodeAlarmConfig），
// 这里把错误原样显示出来，不要吞掉。
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2 } from 'lucide-vue-next'
import { alarmsApi, RULE_TYPES, type NodeAlarmRule } from '../../api/alarms'
import { errorMessage } from '../../utils/error'
import { useToast } from '../../composables/useToast'
import BaseModal from '../modals/BaseModal.vue'

const props = defineProps<{ show: boolean; rule: NodeAlarmRule | null }>()
const emit = defineEmits<{ close: []; saved: [] }>()

const { t } = useI18n()
const toast = useToast()

const saving = ref(false)
const form = ref({ name: '', description: '' })
const configJsonStr = ref('{}')
const configError = ref('')

const ruleTypeLabel = (type?: string) =>
    type && (RULE_TYPES as readonly string[]).includes(type) ? t('dashboard.alarmRuleTypes.' + type) : type || ''

watch(
    () => props.show,
    (show) => {
        if (!show || !props.rule) return
        form.value = { name: props.rule.name, description: props.rule.description || '' }
        configJsonStr.value = JSON.stringify(props.rule.config ?? {}, null, 2)
        configError.value = ''
    }
)

const validateConfig = () => {
    try {
        JSON.parse(configJsonStr.value)
        configError.value = ''
        return true
    } catch {
        configError.value = t('dashboard.alarmActions.invalidJson')
        return false
    }
}

const submit = async () => {
    if (!props.rule || !form.value.name) return
    if (!validateConfig()) return
    saving.value = true
    try {
        await alarmsApi.updateAlarmRule(props.rule.uuid, {
            name: form.value.name,
            description: form.value.description,
            config: JSON.parse(configJsonStr.value),
        })
        emit('saved')
        emit('close')
        toast.success(t('messages.success'))
    } catch (err) {
        toast.error(errorMessage(err, t('messages.error')))
    } finally {
        saving.value = false
    }
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="t('dashboard.alarmActions.editTitle')"
        size="lg"
        form
        :loading="saving"
        @close="emit('close')"
        @submit="submit"
    >
        <div class="form-stack">
            <div class="form-group">
                <label class="form-label">{{ t('dashboard.alarmActions.ruleType') }}</label>
                <input type="text" class="form-input" :value="ruleTypeLabel(rule?.rule_type)" disabled />
            </div>
            <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.name') }} *</label>
                <input type="text" v-model="form.name" class="form-input" />
            </div>
            <div class="form-group">
                <label class="form-label">{{ t('dashboard.table.description') }}</label>
                <input type="text" v-model="form.description" class="form-input" />
            </div>
            <div class="form-group">
                <label class="form-label">{{ t('dashboard.alarmActions.configLabel') }} *</label>
                <textarea
                    v-model="configJsonStr"
                    class="form-input config-textarea"
                    rows="8"
                    @blur="validateConfig"
                ></textarea>
                <span v-if="configError" class="form-error">{{ configError }}</span>
            </div>
        </div>

        <template #footer>
            <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('actions.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !form.name">
                <Loader2 v-if="saving" :size="14" class="spinning" />
                {{ saving ? t('messages.saving') : t('actions.save') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.form-stack {
    display: flex;
    flex-direction: column;
    gap: 16px;
}
.form-group {
    display: flex;
    flex-direction: column;
}
.form-label {
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: 4px;
    font-weight: 500;
}
.form-input {
    padding: 8px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    font-size: 0.875rem;
}
.form-input:disabled {
    background: var(--bg-tertiary);
    color: var(--text-tertiary);
}
.config-textarea {
    font-family: var(--font-family-mono, monospace);
    font-size: 12px;
    resize: vertical;
}
.form-error {
    color: var(--error-color);
    font-size: 12px;
    margin-top: 4px;
}
</style>
