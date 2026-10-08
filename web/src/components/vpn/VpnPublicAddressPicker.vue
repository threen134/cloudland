<script setup lang="ts">
// Public subnet and address of one public address of a VPN gateway (create modal, "add public address").
// Both are optional: an empty subnet lets clapi pick one, an empty address lets it allocate one. The address
// list holds the free addresses of the chosen subnet, without its gateway and without the addresses the
// same form already picked for its other entries (exclude).
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { subnetsApi, assignableAddresses, type Subnet } from '../../api/networks'

const props = defineProps<{
    /** Public subnets to choose from */
    subnets: Subnet[]
    /** Addresses picked for the other entries of the same form */
    exclude?: string[]
    /** Text of the empty subnet option; default "Automatic" */
    autoSubnetLabel?: string
}>()

const subnetId = defineModel<string>('subnetId', { required: true })
const ip = defineModel<string>('ip', { required: true })

const { t } = useI18n()

const free = ref<string[]>([])
const loading = ref(false)

const fetchFree = async (id: string) => {
    free.value = []
    loading.value = false
    if (!id) return
    loading.value = true
    try {
        const res = await subnetsApi.listAddresses(id)
        // Only the answer for the subnet still selected counts
        if (subnetId.value !== id) return
        const gateway = props.subnets.find((s) => s.id === id)?.gateway
        free.value = assignableAddresses(res.addresses, gateway).map((a) => a.address.split('/')[0])
    } catch (err) {
        console.error('Failed to fetch subnet addresses:', err)
    } finally {
        if (subnetId.value === id) loading.value = false
    }
}

// A new subnet drops the address picked in the old one; the first run (mount) keeps a picked address, so an
// entry hidden and shown again by its form does not lose it
watch(
    subnetId,
    (id, previous) => {
        if (previous !== undefined) ip.value = ''
        fetchFree(id)
    },
    { immediate: true }
)

const options = computed(() => free.value.filter((addr) => addr === ip.value || !props.exclude?.includes(addr)))

const hint = computed(() => {
    if (!subnetId.value) return t('dashboard.vpnGateway.publicIpNeedSubnet')
    if (!loading.value && !free.value.length) return t('dashboard.vpnGateway.publicIpNoFree')
    return t('dashboard.vpnGateway.publicIpHint')
})
</script>

<template>
    <div class="form-row">
        <div class="form-group form-group-grow">
            <label class="form-label"
                >{{ t('dashboard.vpnGateway.publicSubnet') }}（{{ t('dashboard.forms.optional') }}）</label
            >
            <div class="select-wrapper">
                <select v-model="subnetId" class="form-input">
                    <option value="">{{ autoSubnetLabel || t('dashboard.vpnGateway.publicSubnetAuto') }}</option>
                    <option v-for="s in subnets" :key="s.id" :value="s.id">
                        {{ s.name }} ({{ s.network_cidr || s.network }})
                    </option>
                </select>
            </div>
        </div>
        <div class="form-group form-group-grow">
            <label class="form-label"
                >{{ t('dashboard.vpnGateway.publicIp') }}（{{ t('dashboard.forms.optional') }}）</label
            >
            <div class="select-wrapper">
                <select v-model="ip" class="form-input" :disabled="!subnetId || loading">
                    <option value="">{{ t('dashboard.vpnGateway.publicIpAuto') }}</option>
                    <option v-for="addr in options" :key="addr" :value="addr">{{ addr }}</option>
                </select>
            </div>
            <div class="form-hint">{{ hint }}</div>
        </div>
    </div>
</template>

<style scoped>
.form-row {
    display: flex;
    gap: var(--spacing-3);
}

.form-group-grow {
    flex: 1;
    min-width: 0;
}

.form-hint {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: 4px;
}

/* Phones: the two selects one above the other */
@media (max-width: 480px) {
    .form-row {
        flex-direction: column;
        gap: 0;
    }
}
</style>
