<script setup lang="ts">
// A storage pool and its state on every host; volumes waiting for adoption can be abandoned here (§5.9)
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Database, RefreshCw, Loader2, Copy, Check } from 'lucide-vue-next'
import { storagePoolsApi, type StoragePool, type HostPool } from '../../api/storagePools'
import { errorMessage } from '../../utils/error'
import { useToast } from '../../composables/useToast'
import { useGoBack } from '../../composables/useGoBack'
import { useCopyId } from '../../composables/useCopyId'
import InfoRow from '../../components/base/InfoRow.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import BaseModal from '../../components/modals/BaseModal.vue'
import CapacityBar from '../../components/storage/CapacityBar.vue'
import type { StatusVariant } from '../../utils/status'

const { t, te } = useI18n()
const route = useRoute()
const toast = useToast()
const goBack = useGoBack('storage-pools')
const { copiedId, copyId } = useCopyId()

const pool = ref<StoragePool | null>(null)
const hosts = ref<HostPool[]>([])
const loading = ref(true)
const error = ref('')

const id = computed(() => String(route.params.id))

const load = async () => {
    loading.value = true
    error.value = ''
    try {
        const [p, h] = await Promise.all([storagePoolsApi.get(id.value), storagePoolsApi.hosts(id.value)])
        pool.value = p
        hosts.value = h.storage_pools
    } catch (err) {
        error.value = errorMessage(err, t('storage.loadFailed'))
    } finally {
        loading.value = false
    }
}
onMounted(load)

const statusVariant = (s: string): StatusVariant =>
    s === 'ready'
        ? 'success'
        : ['degraded', 'maintenance'].includes(s)
          ? 'warning'
          : ['unavailable', 'lost', 'error'].includes(s)
            ? 'error'
            : 'pending'
const statusText = (s: string) => (te(`storage.hostPoolStatus.${s}`) ? t(`storage.hostPoolStatus.${s}`) : s)
const layoutText = (l: string) => (te(`storage.layouts.${l}`) ? t(`storage.layouts.${l}`) : l || '-')

// ---- abandon the volumes waiting for adoption ----
const showAbandon = ref(false)
const abandonHost = ref<number | null>(null)
const abandonConfirm = ref('')
const abandoning = ref(false)
const abandon = async () => {
    if (!pool.value || abandonHost.value === null || abandonConfirm.value !== pool.value.name) return
    abandoning.value = true
    try {
        const r = await storagePoolsApi.abandonOrphans(pool.value.id, abandonHost.value, abandonConfirm.value)
        toast.success(t('storage.abandonDone', { n: r.volumes }))
        showAbandon.value = false
    } catch (err) {
        toast.error(errorMessage(err, t('storage.submitFailed')))
    } finally {
        abandoning.value = false
    }
}
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">← {{ t('actions.back') }}</button>
        </div>
        <div v-if="loading && !pool" class="loading-container"><Loader2 :size="24" class="spinning" /></div>
        <div v-else-if="error" class="error-container">
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-secondary btn-sm" @click="load">{{ t('actions.retry') }}</button>
        </div>
        <template v-else-if="pool">
            <div class="title-bar">
                <div class="title-info">
                    <div class="title-icon"><Database :size="20" /></div>
                    <div>
                        <h2 class="resource-title">
                            {{ pool.name }}
                            <StatusBadge
                                :status="pool.status"
                                :label="t(`storage.poolStatus.${pool.status || 'active'}`)"
                            />
                        </h2>
                        <div class="resource-id-row">
                            <span class="resource-id-text">{{ pool.id }}</span>
                            <button class="copy-btn" :title="t('actions.copy')" @click="copyId(pool.id)">
                                <Check v-if="copiedId === pool.id" :size="14" />
                                <Copy v-else :size="14" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <button class="btn btn-secondary btn-sm" @click="load">
                        <RefreshCw :size="14" :class="{ spinning: loading }" />
                    </button>
                    <button class="btn btn-secondary btn-sm" :disabled="pool.builtin" @click="showAbandon = true">
                        {{ t('storage.abandonOrphans') }}
                    </button>
                </div>
            </div>

            <div class="info-card card">
                <h3 class="card-section-title">{{ t('dashboard.table.overview') }}</h3>
                <div class="info-rows">
                    <InfoRow :label="t('storage.type')">{{
                        pool.shared ? t('storage.shared') : t('storage.local')
                    }}</InfoRow>
                    <InfoRow :label="t('storage.media')">{{
                        pool.builtin ? '-' : (pool.media || '-').toUpperCase()
                    }}</InfoRow>
                    <InfoRow :label="t('storage.fallbackGroup')">{{ pool.fallback_group || '-' }}</InfoRow>
                    <InfoRow :label="t('storage.overRatio')">{{
                        pool.builtin ? t('storage.overRatioBuiltin') : `${pool.over_ratio}x`
                    }}</InfoRow>
                    <InfoRow :label="t('storage.mountPath')">{{ pool.mount_path }}</InfoRow>
                    <InfoRow :label="t('storage.default')">{{
                        pool.is_default ? t('messages.yes') : t('messages.no')
                    }}</InfoRow>
                    <InfoRow :label="t('storage.hostsHaving')"
                        >{{ pool.available_hosts }} / {{ pool.hosts ?? '-' }}</InfoRow
                    >
                    <InfoRow v-if="pool.description" :label="t('dashboard.table.description')">{{
                        pool.description
                    }}</InfoRow>
                </div>
            </div>

            <div class="info-card card">
                <h3 class="card-section-title">{{ t('storage.hostsTitle') }}</h3>
                <table class="data-table">
                    <thead>
                        <tr>
                            <th>{{ t('dashboard.hypervisors') }}</th>
                            <th>{{ t('storage.layout') }}</th>
                            <th>{{ t('storage.capacity') }}</th>
                            <th>{{ t('storage.status') }}</th>
                            <th>{{ t('storage.volumes') }}</th>
                        </tr>
                    </thead>
                    <tbody>
                        <tr v-for="h in hosts" :key="h.hypervisor?.id">
                            <td>
                                <router-link
                                    v-if="h.hypervisor"
                                    :to="{ name: 'hypervisor-detail', params: { id: h.hypervisor.id } }"
                                    class="resource-link"
                                    >{{ h.hypervisor.name }}</router-link
                                >
                            </td>
                            <td>{{ pool.builtin ? '-' : layoutText(h.layout) }}</td>
                            <td>
                                <CapacityBar
                                    v-if="h.capacity_bytes"
                                    :capacity="h.capacity_bytes"
                                    :used="h.used_bytes"
                                    :allocated="h.allocated_bytes"
                                    :reserved="h.reserved_bytes"
                                />
                                <span v-else class="text-secondary">-</span>
                            </td>
                            <td>
                                <StatusBadge :variant="statusVariant(h.status)" :label="statusText(h.status)" />
                                <div v-if="h.reason" class="cell-sub" :title="h.reason">{{ h.reason }}</div>
                                <div v-if="h.storage_full_paused" class="cell-sub text-error">
                                    {{ t('storage.pausedFull', { n: h.storage_full_paused }) }}
                                </div>
                            </td>
                            <td>{{ h.volume_count }}</td>
                        </tr>
                        <tr v-if="!hosts.length">
                            <td colspan="5" class="text-secondary text-center">{{ t('storage.noHosts') }}</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </template>

        <BaseModal
            :show="showAbandon"
            :title="t('storage.abandonTitle', { name: pool?.name })"
            form
            :loading="abandoning"
            @close="showAbandon = false"
            @submit="abandon"
        >
            <p class="hint">{{ t('storage.abandonDesc') }}</p>
            <div class="form-group">
                <label class="form-label">{{ t('storage.oldHostid') }}</label>
                <input v-model.number="abandonHost" type="number" min="0" class="form-input" />
            </div>
            <div class="form-group">
                <label class="form-label">{{ t('storage.confirmPool', { name: pool?.name }) }}</label>
                <input v-model="abandonConfirm" type="text" class="form-input" :placeholder="pool?.name" />
            </div>
            <template #footer>
                <button type="button" class="btn btn-secondary" @click="showAbandon = false">
                    {{ t('actions.cancel') }}
                </button>
                <button
                    type="submit"
                    class="btn btn-danger"
                    :disabled="abandoning || abandonHost === null || abandonConfirm !== pool?.name"
                >
                    {{ t('actions.confirm') }}
                </button>
            </template>
        </BaseModal>
    </div>
</template>

<style scoped>
.info-card {
    margin-bottom: var(--spacing-5);
}

.cell-sub {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    margin-top: 2px;
    max-width: 280px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.hint {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    margin-bottom: var(--spacing-3);
}
</style>
