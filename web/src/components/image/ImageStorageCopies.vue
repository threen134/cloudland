<script setup lang="ts">
// The copies of an image in the shared storage pools (shared-storage-design.md §9.6), for system admins: which pools
// have one, how far a running import got, preheating the image into more pools ahead of the first boot disk there, and
// removing a copy no boot disk is cloned from. Refreshed every 3 seconds while an import or a removal runs.
import { ref, computed, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Flame, Trash2, RefreshCw } from 'lucide-vue-next'
import BaseModal from '../modals/BaseModal.vue'
import DeleteModal from '../modals/DeleteModal.vue'
import StatusBadge from '../base/StatusBadge.vue'
import { imagesApi, type ImageStorageCopy } from '../../api/images'
import { storagePoolsApi, type StoragePool } from '../../api/storagePools'
import { useToast } from '../../composables/useToast'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ imageId: string; imageAvailable: boolean }>()
const { t, te } = useI18n()
const toast = useToast()

const copies = ref<ImageStorageCopy[]>([])
const loading = ref(false)
const loadError = ref('')
let timer: ReturnType<typeof setTimeout> | null = null
// A request still in flight when the page is left must not schedule the next poll, nor one for an image shown before
let unmounted = false
let generation = 0

const running = computed(() => copies.value.some((c) => c.status === 'syncing' || c.status === 'deleting'))

const load = async (silent = false) => {
    const gen = ++generation
    if (!silent) loading.value = true
    try {
        const list = (await imagesApi.storageCopies(props.imageId)).copies
        if (unmounted || gen !== generation) return
        copies.value = list
        loadError.value = ''
    } catch (err) {
        if (unmounted || gen !== generation) return
        if (!silent) loadError.value = errorMessage(err, t('messages.error'))
    } finally {
        if (!unmounted && gen === generation) {
            loading.value = false
            if (timer) clearTimeout(timer)
            timer = running.value ? setTimeout(() => load(true), 3000) : null
        }
    }
}
watch(
    () => props.imageId,
    () => load(),
    { immediate: true }
)
onUnmounted(() => {
    unmounted = true
    if (timer) clearTimeout(timer)
    timer = null
})

const statusText = (c: ImageStorageCopy) => {
    const key = `storage.imageCopies.status.${c.status}`
    return te(key) ? t(key) : c.status
}
const phaseText = (c: ImageStorageCopy) => {
    if (c.status !== 'syncing' || !c.phase) return ''
    const key = `storage.imageCopies.phase.${c.phase}`
    return te(key) ? t(key) : c.phase
}
const dropBlocked = (c: ImageStorageCopy) =>
    c.boot_disks > 0
        ? t('storage.imageCopies.inUse', { n: c.boot_disks })
        : c.status === 'syncing'
          ? t('storage.imageCopies.importing')
          : c.status === 'deleting'
            ? t('storage.imageCopies.status.deleting')
            : ''

// Preheat
const preheatVisible = ref(false)
const pools = ref<StoragePool[]>([])
const chosen = ref<string[]>([])
const poolsLoading = ref(false)
const preheating = ref(false)
const preheatError = ref('')
const hasCopy = (poolId: string) =>
    copies.value.some((c) => c.storage_pool.id === poolId && (c.status === 'synced' || c.status === 'syncing'))
const openPreheat = async () => {
    preheatVisible.value = true
    chosen.value = []
    preheatError.value = ''
    poolsLoading.value = true
    try {
        pools.value = (await storagePoolsApi.list({ limit: 500 })).storage_pools.filter(
            (p) => p.shared && (p.status || 'active') === 'active'
        )
    } catch (err) {
        preheatError.value = errorMessage(err, t('messages.error'))
    } finally {
        poolsLoading.value = false
    }
}
const submitPreheat = async () => {
    if (!chosen.value.length) return
    preheating.value = true
    preheatError.value = ''
    try {
        copies.value = (
            await imagesApi.preheat(props.imageId, {
                storage_pools: chosen.value.map((id) => ({ id })),
            })
        ).copies
        preheatVisible.value = false
        toast.success(t('storage.imageCopies.preheatStarted'))
        load(true)
    } catch (err) {
        preheatError.value = errorMessage(err, t('storage.imageCopies.preheatFailed'))
    } finally {
        preheating.value = false
    }
}

// Remove a copy
const dropTarget = ref<ImageStorageCopy | null>(null)
const dropping = ref(false)
const submitDrop = async () => {
    if (!dropTarget.value) return
    dropping.value = true
    try {
        await imagesApi.dropStorageCopy(props.imageId, dropTarget.value.storage_pool.id)
        toast.success(t('storage.imageCopies.dropStarted'))
        dropTarget.value = null
        load(true)
    } catch (err) {
        toast.error(errorMessage(err, t('storage.imageCopies.dropFailed')))
    } finally {
        dropping.value = false
    }
}
</script>

<template>
    <div class="card info-card">
        <div class="card-head">
            <h3>{{ t('storage.imageCopies.title') }}</h3>
            <div class="head-actions">
                <button class="btn btn-secondary btn-sm btn-icon" :title="t('actions.refresh')" @click="load()">
                    <RefreshCw :size="14" />
                </button>
                <button
                    class="btn btn-secondary btn-sm"
                    :disabled="!imageAvailable"
                    :title="imageAvailable ? '' : t('storage.imageCopies.notAvailable')"
                    @click="openPreheat"
                >
                    <Flame :size="14" /> {{ t('storage.imageCopies.preheat') }}
                </button>
            </div>
        </div>
        <p class="intro">{{ t('storage.imageCopies.intro') }}</p>
        <div v-if="loadError" class="text-error">{{ loadError }}</div>
        <div v-else-if="loading && !copies.length" class="text-secondary">{{ t('messages.loading') }}</div>
        <div v-else-if="!copies.length" class="text-secondary">{{ t('storage.imageCopies.none') }}</div>
        <table v-else class="copies">
            <thead>
                <tr>
                    <th>{{ t('storage.imageCopies.pool') }}</th>
                    <th>{{ t('dashboard.table.status') }}</th>
                    <th>{{ t('storage.imageCopies.host') }}</th>
                    <th>{{ t('storage.imageCopies.bootDisks') }}</th>
                    <th>{{ t('dashboard.table.updatedAt') }}</th>
                    <th></th>
                </tr>
            </thead>
            <tbody>
                <tr v-for="c in copies" :key="c.storage_pool.id">
                    <td>
                        <router-link :to="`/dashboard/storage-pools/${c.storage_pool.id}`" class="resource-link">{{
                            c.storage_pool.name
                        }}</router-link>
                    </td>
                    <td>
                        <StatusBadge :status="c.status" :label="statusText(c)" />
                        <div v-if="phaseText(c)" class="progress-line">
                            <span>{{ phaseText(c) }}</span>
                            <span v-if="c.phase !== 'wait'">{{ c.progress }}%</span>
                            <div v-if="c.phase !== 'wait'" class="bar">
                                <div class="bar-fill" :style="{ width: `${c.progress}%` }"></div>
                            </div>
                        </div>
                        <div v-if="c.reason" class="reason" :title="c.reason">{{ c.reason }}</div>
                    </td>
                    <td>{{ c.host || '-' }}</td>
                    <td>{{ c.boot_disks }}</td>
                    <td class="nowrap">{{ c.updated_at }}</td>
                    <td class="row-actions">
                        <button
                            class="icon-btn-table icon-danger"
                            :disabled="!!dropBlocked(c)"
                            :title="dropBlocked(c) || t('storage.imageCopies.drop')"
                            @click="dropTarget = c"
                        >
                            <Trash2 :size="16" />
                        </button>
                    </td>
                </tr>
            </tbody>
        </table>

        <BaseModal
            :show="preheatVisible"
            :title="t('storage.imageCopies.preheatTitle')"
            size="md"
            :loading="preheating"
            form
            @close="preheatVisible = false"
            @submit="submitPreheat"
        >
            <div class="form-stack">
                <p class="intro">{{ t('storage.imageCopies.preheatIntro') }}</p>
                <div v-if="poolsLoading" class="text-secondary">{{ t('messages.loading') }}</div>
                <div v-else-if="!pools.length" class="text-secondary">{{ t('storage.imageCopies.noSharedPool') }}</div>
                <label v-for="p in pools" v-else :key="p.id" class="checkbox-inline">
                    <input v-model="chosen" type="checkbox" :value="p.id" :disabled="hasCopy(p.id)" />
                    {{ p.name }}
                    <span class="text-secondary">· {{ p.cluster?.name || p.driver }}</span>
                    <span v-if="hasCopy(p.id)" class="text-secondary">· {{ t('storage.imageCopies.hasCopy') }}</span>
                </label>
            </div>
            <template #footer>
                <span v-if="preheatError" class="footer-error text-error">{{ preheatError }}</span>
                <button type="button" class="btn btn-secondary" :disabled="preheating" @click="preheatVisible = false">
                    {{ t('actions.cancel') }}
                </button>
                <button type="submit" class="btn btn-primary" :disabled="preheating || !chosen.length">
                    {{ t('storage.imageCopies.preheat') }}
                </button>
            </template>
        </BaseModal>

        <DeleteModal
            :show="!!dropTarget"
            :resource-name="dropTarget?.storage_pool.name"
            :confirm-label="t('storage.imageCopies.drop')"
            :loading="dropping"
            @close="dropTarget = null"
            @confirm="submitDrop"
        />
    </div>
</template>

<style scoped>
.card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-3);
}

.head-actions {
    display: flex;
    gap: 8px;
}

.intro {
    margin: 8px 0 12px;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.copies {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--font-size-sm);
}

.copies th {
    text-align: left;
    font-weight: 500;
    color: var(--text-secondary);
    padding: 8px;
    border-bottom: 1px solid var(--border-default);
}

.copies td {
    padding: 8px;
    border-bottom: 1px solid var(--border-light);
    vertical-align: top;
}

.progress-line {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 4px;
    color: var(--text-secondary);
}

.bar {
    flex: 1;
    min-width: 60px;
    max-width: 160px;
    height: 6px;
    border-radius: 3px;
    background: var(--border-light);
    overflow: hidden;
}

.bar-fill {
    height: 100%;
    background: var(--primary-color);
}

.reason {
    margin-top: 4px;
    max-width: 320px;
    color: var(--error-color);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.nowrap {
    white-space: nowrap;
}

.form-stack {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.checkbox-inline {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.footer-error {
    margin-right: auto;
}
</style>
