<script setup lang="ts">
// Attach a VPC of the organization to a transit gateway. The backend refuses a VPC attached elsewhere (409 133012),
// a gateway with 10 attachments already (409 133013) and overlapping networks (400 133014). The members of this
// gateway are left out; a VPC attached to another gateway is listed but disabled, with that gateway named, so the
// user sees why instead of getting the 409.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { vpcsApi, type VPC } from '../../api/networks'
import { transitGatewaysApi, type TgwAttachment, type TgwRouteTable } from '../../api/transitGateways'
import { OPTION_LIST_LIMIT } from '../../api/listParams'
import { useToast } from '../../composables/useToast'
import { errorMessage } from '../../utils/error'
import BaseModal from '../modals/BaseModal.vue'

const props = defineProps<{
    show: boolean
    gatewayId: string
    routeTables: TgwRouteTable[]
    /** Current attachments of the gateway: their VPCs are not offered again */
    attachments: TgwAttachment[]
}>()
const emit = defineEmits<{ close: []; attached: [attachment: TgwAttachment] }>()

const { t } = useI18n()
const toast = useToast()

const vpcs = ref<VPC[]>([])
const loadingVpcs = ref(false)
const vpcLoadError = ref('')
const form = ref({ vpcId: '', routeTableId: '', propagate: true })
const saving = ref(false)
const error = ref('')

const attachedVpcIds = computed(() => new Set(props.attachments.map((a) => a.vpc?.id)))
// Members of this gateway (the ones being detached included) are not offered
const candidateVpcs = computed(() =>
    vpcs.value.filter((v) => !attachedVpcIds.value.has(v.id) && v.transit_gateway?.id !== props.gatewayId)
)
// A VPC is attached to one gateway at most: the ones on another gateway come last, disabled
const freeVpcs = computed(() => candidateVpcs.value.filter((v) => !v.transit_gateway))
const elsewhereVpcs = computed(() => candidateVpcs.value.filter((v) => !!v.transit_gateway))
// Default table first
const tables = computed(() => [...props.routeTables].sort((a, b) => Number(b.is_default) - Number(a.is_default)))
const defaultTableId = computed(() => props.routeTables.find((rt) => rt.is_default)?.id || '')
// Internal subnets of the chosen VPC (VRRP and the like are not routed by the gateway)
const selectedVpcCidrs = computed(() =>
    (vpcs.value.find((v) => v.id === form.value.vpcId)?.subnets || [])
        .filter((s) => s.type === 'internal')
        .map((s) => s.network || s.network_cidr)
        .filter(Boolean)
)

const loadVpcs = async () => {
    loadingVpcs.value = true
    vpcLoadError.value = ''
    try {
        const res = await vpcsApi.list({ limit: OPTION_LIST_LIMIT, order: 'name' })
        vpcs.value = res.vpcs || []
    } catch (err) {
        console.error('Failed to load VPCs:', err)
        vpcs.value = []
        vpcLoadError.value = errorMessage(err, t('messages.error'))
    } finally {
        loadingVpcs.value = false
    }
}

watch(
    () => props.show,
    (visible) => {
        if (!visible) return
        form.value = { vpcId: '', routeTableId: defaultTableId.value, propagate: true }
        error.value = ''
        loadVpcs()
    },
    { immediate: true }
)

const close = () => {
    error.value = ''
    emit('close')
}

const submit = async () => {
    error.value = ''
    if (!form.value.vpcId) {
        error.value = t('dashboard.transitGateway.selectVpcRequired')
        return
    }
    saving.value = true
    try {
        const tableId = form.value.routeTableId
        const attachment = await transitGatewaysApi.attach(props.gatewayId, {
            vpc: { id: form.value.vpcId },
            // Left out for the default table: the backend picks it
            route_table: tableId && tableId !== defaultTableId.value ? { id: tableId } : undefined,
            propagate: form.value.propagate,
        })
        toast.success(t('dashboard.transitGateway.attachStarted'))
        emit('attached', attachment)
    } catch (err) {
        console.error('Failed to attach VPC:', err)
        error.value = errorMessage(err, t('messages.error'))
    } finally {
        saving.value = false
    }
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="$t('dashboard.transitGateway.attachVpc')"
        :loading="saving"
        size="lg"
        form
        @close="close"
        @submit="submit"
    >
        <div class="form-group">
            <label class="form-label">{{ $t('dashboard.transitGateway.vpc') }} *</label>
            <select v-model="form.vpcId" class="form-input" :disabled="loadingVpcs">
                <option value="" disabled>
                    {{ loadingVpcs ? $t('messages.loading') : $t('dashboard.transitGateway.selectVpc') }}
                </option>
                <option v-for="v in freeVpcs" :key="v.id" :value="v.id">{{ v.name }}</option>
                <option v-for="v in elsewhereVpcs" :key="v.id" :value="v.id" disabled>
                    {{
                        $t('dashboard.transitGateway.vpcOnOtherGateway', {
                            vpc: v.name,
                            gateway: v.transit_gateway?.name || '-',
                        })
                    }}
                </option>
            </select>
            <div v-if="vpcLoadError" class="field-error">{{ vpcLoadError }}</div>
            <template v-else-if="!loadingVpcs && !freeVpcs.length">
                <div class="form-hint">{{ $t('dashboard.transitGateway.noVpcToAttach') }}</div>
                <div v-if="elsewhereVpcs.length" class="form-hint">
                    {{ $t('dashboard.transitGateway.otherGatewayHint') }}
                </div>
            </template>
            <div v-else-if="selectedVpcCidrs.length" class="form-hint">
                {{ $t('dashboard.transitGateway.vpcSubnets', { cidrs: selectedVpcCidrs.join(', ') }) }}
            </div>
            <div v-else class="form-hint">{{ $t('dashboard.transitGateway.overlapHint') }}</div>
            <div v-if="!vpcLoadError && freeVpcs.length && elsewhereVpcs.length" class="form-hint">
                {{ $t('dashboard.transitGateway.otherGatewayHint') }}
            </div>
        </div>

        <div class="form-group">
            <label class="form-label">{{ $t('dashboard.transitGateway.routeTable') }}</label>
            <select v-model="form.routeTableId" class="form-input">
                <option v-for="rt in tables" :key="rt.id" :value="rt.id">
                    {{ rt.name }}{{ rt.is_default ? ` · ${$t('dashboard.transitGateway.defaultTag')}` : '' }}
                </option>
            </select>
            <div class="form-hint">{{ $t('dashboard.transitGateway.routeTableHint') }}</div>
        </div>

        <div class="form-group">
            <label class="checkbox-row">
                <input v-model="form.propagate" type="checkbox" />
                <span>{{ $t('dashboard.transitGateway.propagateToDefault') }}</span>
            </label>
            <div class="form-hint checkbox-hint">{{ $t('dashboard.transitGateway.propagateToDefaultHint') }}</div>
        </div>

        <div class="notice">{{ $t('dashboard.transitGateway.securityGroupHint') }}</div>

        <template #footer>
            <div v-if="error" class="footer-error">{{ error }}</div>
            <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">
                {{ $t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !form.vpcId">
                <span v-if="saving" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px"></span>
                {{ $t('dashboard.transitGateway.attach') }}
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
    margin-top: 4px;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
    word-break: break-word;
}

.checkbox-row {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    cursor: pointer;
}

.checkbox-hint {
    padding-left: 22px;
}

.notice {
    padding: var(--spacing-3);
    border-radius: var(--radius-md);
    background: var(--info-light);
    color: var(--info-dark);
    font-size: var(--font-size-xs);
    line-height: 1.6;
}
</style>
