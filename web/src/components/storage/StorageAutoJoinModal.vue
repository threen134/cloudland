<script setup lang="ts">
// The zones whose hosts join a storage cluster as clients on their own (shared-storage-design.md §6.3): every online
// host of those zones that is not in the cluster, the ones there now included, joins with a task of its own once the
// cluster is free; with new only, just the hosts registered from then on. No zone turns it off.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import { storageClustersApi, type StorageCluster } from '../../api/storageClusters'
import { zonesApi, type Zone } from '../../api/zones'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean; cluster: StorageCluster | null }>()
const emit = defineEmits<{ close: []; saved: [cluster: StorageCluster] }>()
const { t } = useI18n()

const zones = ref<Zone[]>([])
const chosen = ref<string[]>([])
const newOnly = ref(false)
const loading = ref(false)
const loadError = ref('')
const submitting = ref(false)
const submitError = ref('')

watch(
    () => props.show,
    async (show) => {
        if (!show) return
        chosen.value = (props.cluster?.auto_join?.zones || []).map((z) => z.id || '').filter(Boolean)
        newOnly.value = !!props.cluster?.auto_join?.new_only
        submitError.value = ''
        loading.value = true
        loadError.value = ''
        try {
            zones.value = (await zonesApi.fetchZones({ limit: 500, order: 'name' })).zones
        } catch (err) {
            loadError.value = errorMessage(err, t('messages.error'))
        } finally {
            loading.value = false
        }
    }
)

const before = computed(() =>
    (props.cluster?.auto_join?.zones || [])
        .map((z) => z.id)
        .sort()
        .join(',')
)
const unchanged = computed(
    () =>
        [...chosen.value].sort().join(',') === before.value &&
        (newOnly.value && chosen.value.length > 0) === !!props.cluster?.auto_join?.new_only
)

const submit = async () => {
    if (!props.cluster || unchanged.value) return
    submitting.value = true
    submitError.value = ''
    try {
        emit(
            'saved',
            await storageClustersApi.update(props.cluster.id, {
                auto_join_zones: chosen.value,
                auto_join_new_only: newOnly.value && chosen.value.length > 0,
            })
        )
    } catch (err) {
        submitError.value = errorMessage(err, t('storage.cluster.actionFailed'))
    } finally {
        submitting.value = false
    }
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="t('storage.clusterDetail.autoJoinTitle')"
        size="md"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="submit"
    >
        <div class="form-stack">
            <p class="intro">{{ t('storage.clusterDetail.autoJoinIntro') }}</p>
            <div v-if="loadError" class="text-error">{{ loadError }}</div>
            <div v-else-if="loading" class="text-secondary">{{ t('messages.loading') }}</div>
            <div v-else class="zones">
                <label v-for="z in zones" :key="z.id" class="zone-chip" :class="{ active: chosen.includes(z.id) }">
                    <input v-model="chosen" type="checkbox" :value="z.id" />
                    {{ z.name }}
                </label>
            </div>
            <label v-if="!loading && !loadError" class="checkbox-inline">
                <input v-model="newOnly" type="checkbox" :disabled="!chosen.length" />
                {{ t('storage.clusterDetail.autoJoinNewOnly') }}
            </label>
            <small v-if="newOnly && chosen.length" class="text-secondary">{{
                t('storage.clusterDetail.autoJoinNewOnlyHint')
            }}</small>
        </div>
        <template #footer>
            <span v-if="submitError" class="footer-error text-error">{{ submitError }}</span>
            <button type="button" class="btn btn-secondary" :disabled="submitting" @click="emit('close')">
                {{ t('actions.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="submitting || unchanged">
                {{ t('actions.save') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.form-stack {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.intro {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.zones {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
}

.zone-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.zone-chip.active {
    border-color: var(--primary-color);
    background: var(--primary-light);
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
