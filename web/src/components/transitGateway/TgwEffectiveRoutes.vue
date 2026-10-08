<script setup lang="ts">
// Effective routes of a route table, as the nodes install them: the propagated subnets (after the prefix filters),
// the static routes and the blackholes. Traffic from the VPCs associated with the table is routed by them.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw, Route } from 'lucide-vue-next'
import { transitGatewaysApi, type TgwEffectiveRoute, type TgwRouteTable } from '../../api/transitGateways'
import { errorMessage } from '../../utils/error'
import { routeTypeText, routeTypeBadge } from '../../utils/transitGateway'
import DataTable, { type Column } from '../base/DataTable.vue'

const props = defineProps<{
    gatewayId: string
    routeTables: TgwRouteTable[]
    /** Id of the selected table, shared with the route tables tab */
    modelValue: string
}>()
const emit = defineEmits<{ 'update:modelValue': [id: string] }>()

const { t, te } = useI18n()

const tables = computed(() => [...props.routeTables].sort((a, b) => Number(b.is_default) - Number(a.is_default)))
const tableId = computed({
    get: () => props.modelValue,
    set: (id: string) => emit('update:modelValue', id),
})
const table = computed(() => props.routeTables.find((rt) => rt.id === props.modelValue) || null)

const routes = ref<TgwEffectiveRoute[]>([])
const loading = ref(false)
const error = ref('')
// Responses of an older table are dropped
let generation = 0

const load = async () => {
    const id = props.modelValue
    if (!id) {
        routes.value = []
        return
    }
    const current = ++generation
    loading.value = true
    error.value = ''
    try {
        const list = await transitGatewaysApi.effectiveRoutes(props.gatewayId, id)
        if (current === generation) routes.value = list
    } catch (err) {
        console.error('Failed to load effective routes:', err)
        if (current === generation) {
            routes.value = []
            error.value = errorMessage(err, t('messages.error'))
        }
    } finally {
        if (current === generation) loading.value = false
    }
}

watch(() => props.modelValue, load, { immediate: true })

// Local sort: the whole list is loaded at once
const columns = computed<Column[]>(() => [
    {
        key: 'destination',
        label: t('dashboard.transitGateway.destination'),
        sortable: true,
        // Numeric order of the addresses, then the prefix length
        sortValue: (row) => {
            const [ip, len] = String(row.destination).split('/')
            const n = ip.split('.').reduce((acc, octet) => acc * 256 + Number(octet), 0)
            return n * 64 + Number(len || 0)
        },
    },
    { key: 'type', label: t('dashboard.table.type'), sortable: true },
    { key: 'target', label: t('dashboard.transitGateway.target') },
])
</script>

<template>
    <div>
        <div class="tab-toolbar">
            <div class="toolbar-left">
                <label class="toolbar-label" for="tgw-effective-table">{{
                    $t('dashboard.transitGateway.routeTable')
                }}</label>
                <select id="tgw-effective-table" v-model="tableId" class="filter-select">
                    <option v-for="rt in tables" :key="rt.id" :value="rt.id">
                        {{ rt.name }}{{ rt.is_default ? ` · ${$t('dashboard.transitGateway.defaultTag')}` : '' }}
                    </option>
                </select>
            </div>
            <button class="btn btn-secondary btn-sm btn-icon" :title="$t('actions.refresh')" @click="load">
                <RefreshCw :size="14" :class="{ spinning: loading }" />
            </button>
        </div>

        <p v-if="table" class="tab-hint">
            <template v-if="table.associations.length">{{
                $t('dashboard.transitGateway.effectiveFor', {
                    vpcs: table.associations.map((a) => a.vpc?.name || a.id.slice(0, 8)).join(', '),
                })
            }}</template>
            <template v-else>{{ $t('dashboard.transitGateway.effectiveForNone') }}</template>
        </p>

        <DataTable
            :columns="columns"
            :rows="routes"
            :row-key="(r) => `${r.type}-${r.destination}`"
            :loading="loading"
            :error="error"
            @retry="load"
        >
            <template #empty>
                <div>
                    <Route :size="40" style="opacity: 0.3; margin-bottom: 12px" />
                    <p class="text-secondary">{{ $t('dashboard.transitGateway.noEffectiveRoutes') }}</p>
                </div>
            </template>
            <template #cell-destination="{ row: r }">
                <code class="mono">{{ r.destination }}</code>
            </template>
            <template #cell-type="{ row: r }">
                <span class="badge" :class="routeTypeBadge(r.type)">{{ routeTypeText(t, te, r.type) }}</span>
            </template>
            <template #cell-target="{ row: r }">
                <router-link
                    v-if="r.attachment?.vpc?.id"
                    :to="{ name: 'vpc-detail', params: { id: r.attachment.vpc.id } }"
                    class="text-link"
                    >{{ r.attachment.vpc.name }}</router-link
                >
                <span v-else-if="r.type === 'blackhole'" class="text-secondary">{{
                    $t('dashboard.transitGateway.dropped')
                }}</span>
                <span v-else class="text-tertiary">-</span>
            </template>
        </DataTable>
    </div>
</template>

<style scoped>
.tab-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-3);
    margin-bottom: var(--spacing-3);
}

.toolbar-left {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    min-width: 0;
}

.toolbar-label {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    white-space: nowrap;
}

/* Same look as the selects of the list toolbars (PageToolbar) */
.filter-select {
    height: 36px;
    min-width: 200px;
    max-width: 100%;
    padding: 0 var(--spacing-3);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-md);
    background: var(--bg-primary);
    font-size: var(--font-size-sm);
    color: var(--text-primary);
}

.filter-select:focus {
    outline: none;
    border-color: var(--primary-300);
    box-shadow: 0 0 0 2px var(--primary-100);
}

.tab-hint {
    margin: 0 0 var(--spacing-3);
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
}

.mono {
    font-family: var(--font-family-mono);
}

.text-link {
    color: var(--primary-600);
    text-decoration: none;
}

.text-link:hover {
    text-decoration: underline;
}
</style>
