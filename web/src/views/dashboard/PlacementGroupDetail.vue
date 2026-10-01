<script setup lang="ts">
// A placement group and its members (placement-group-plan.md §8). "Host N" numbers the hosts inside the group so
// that everyone can see whether the members are apart without learning host names; system admins also get the
// host name. The platform never moves members to fix a group that breaks its rule, it only says so here.
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, Boxes, Pencil, Trash2, RefreshCw, Copy, Check, AlertTriangle } from 'lucide-vue-next'
import { placementGroupsApi, type PlacementGroup, type PlacementGroupMember } from '../../api/placementGroups'
import { useAuthStore } from '../../stores/auth'
import { useToast } from '../../composables/useToast'
import { useGoBack } from '../../composables/useGoBack'
import { useCopyId } from '../../composables/useCopyId'
import { errorMessage } from '../../utils/error'
import { formatDateTime } from '../../utils/format'
import { policyText, strictText, ruleText, ruleHint, crowdedSlots } from '../../utils/placementGroup'
import InfoRow from '../../components/base/InfoRow.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PlacementGroupEditModal from '../../components/placementGroup/PlacementGroupEditModal.vue'

const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const toast = useToast()
const authStore = useAuthStore()
const goBack = useGoBack('placement-groups')
const { copiedId, copyId } = useCopyId()

const isSystemAdmin = computed(() => authStore.user?.role === 'admin' || authStore.user?.is_superuser === true)

const group = ref<PlacementGroup | null>(null)
const loading = ref(true)
const error = ref('')
const id = computed(() => String(route.params.id))

const fetchGroup = async (silent = false) => {
    if (!silent) {
        loading.value = true
        error.value = ''
    }
    try {
        group.value = await placementGroupsApi.get(id.value)
    } catch (err) {
        console.error('Failed to load placement group:', err)
        if (!silent || !group.value) error.value = errorMessage(err, t('messages.error'))
    } finally {
        loading.value = false
    }
}

const members = computed<PlacementGroupMember[]>(() => group.value?.members || [])

// ─── Compliance ──────────────────────────────────────────────────────────────
// Hosts carrying more than one member of a spread group: the members on them are marked in the table
const crowded = computed(() => (group.value?.policy === 'spread' ? crowdedSlots(members.value) : new Set<number>()))
const locatedSlots = computed(() => new Set(members.value.filter((m) => m.host_slot > 0).map((m) => m.host_slot)))

const joinNames = (list: PlacementGroupMember[]) =>
    list.map((m) => m.hostname).join(t('dashboard.placementGroup.listSeparator'))

// What is wrong, then the causes that can be told from the migration records (§2.3)
const violation = computed(() => {
    const g = group.value
    if (!g || g.compliant) return ''
    if (g.policy === 'spread' && crowded.value.size) {
        const n = members.value.filter((m) => crowded.value.has(m.host_slot)).length
        return t('dashboard.placementGroup.notCompliantSpread', { n })
    }
    if (g.policy === 'pack' && locatedSlots.value.size > 1) {
        return t('dashboard.placementGroup.notCompliantPack', { n: locatedSlots.value.size })
    }
    return t('dashboard.placementGroup.notCompliantGeneric')
})
const causes = computed(() => {
    const g = group.value
    if (!g || g.compliant) return []
    const list: string[] = []
    // A failed migration only splits a strict pack group moving together (the other members did move)
    const failed = g.policy === 'pack' ? members.value.filter((m) => m.last_migration_failed) : []
    if (failed.length) list.push(t('dashboard.placementGroup.causeMigrationFailed', { name: joinNames(failed) }))
    const ignored = members.value.filter((m) => m.ignored_placement)
    if (ignored.length) list.push(t('dashboard.placementGroup.causeIgnoredPlacement', { name: joinNames(ignored) }))
    return list
})

// ─── Members table ───────────────────────────────────────────────────────────
const memberColumns = computed<Column[]>(() => {
    const cols: Column[] = [
        { key: 'hostname', label: t('dashboard.table.instance'), sortable: true },
        { key: 'status', label: t('dashboard.table.status'), sortable: true },
        {
            key: 'host_slot',
            label: t('dashboard.placementGroup.hostSlotColumn'),
            sortable: true,
            // Members without a location sort last
            sortValue: (row) => (row.host_slot > 0 ? row.host_slot : Number.MAX_SAFE_INTEGER),
        },
    ]
    if (isSystemAdmin.value) cols.push({ key: 'hypervisor', label: t('dashboard.table.hyper'), sortable: true })
    cols.push({ key: 'notes', label: t('dashboard.placementGroup.notes') })
    return cols
})

const hasNotes = (m: PlacementGroupMember) =>
    m.stale_migration || m.stale_provisioning || m.last_migration_failed || m.ignored_placement

const instanceStatusText = (status: string) => {
    const s = (status || '').toLowerCase()
    if (!s) return '-'
    const key = `dashboard.instanceStatus.${s}`
    return te(key) ? t(key) : status
}

// ─── Edit / delete ───────────────────────────────────────────────────────────
const editing = ref<PlacementGroup | null>(null)
const onEdited = async () => {
    editing.value = null
    await fetchGroup(true)
}

const deleteBlockedReason = computed(() =>
    group.value && group.value.member_count > 0
        ? t('dashboard.placementGroup.deleteHasMembers', { n: group.value.member_count })
        : ''
)
const showDelete = ref(false)
const deleting = ref(false)
const deleteError = ref('')
const openDelete = () => {
    deleteError.value = ''
    showDelete.value = true
}
const confirmDelete = async () => {
    if (!group.value) return
    deleting.value = true
    deleteError.value = ''
    try {
        await placementGroupsApi.delete(group.value.id)
        showDelete.value = false
        toast.success(t('messages.deleteSuccess'))
        router.push({ name: 'placement-groups' })
    } catch (err) {
        console.error('Failed to delete placement group:', err)
        // 409 ErrPlacementGroupInUse: a member was added since the page was loaded
        deleteError.value = errorMessage(err, t('messages.error'))
    } finally {
        deleting.value = false
    }
}

onMounted(() => fetchGroup())
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="16" />
                <span>{{ $t('actions.back') }}</span>
            </button>
        </div>

        <div v-if="loading && !group" class="loading-container">
            <div class="loading-spinner"></div>
        </div>

        <div v-else-if="error && !group" class="error-container card">
            <Boxes :size="48" style="opacity: 0.3" />
            <p class="text-error">{{ error }}</p>
            <button class="btn btn-primary btn-sm" @click="fetchGroup()">{{ $t('actions.retry') }}</button>
        </div>

        <template v-else-if="group">
            <!-- Title bar (global .title-bar styles) -->
            <div class="title-bar">
                <div class="title-info">
                    <div class="title-icon"><Boxes :size="20" /></div>
                    <div>
                        <h2 class="resource-title">
                            {{ group.name }}
                            <StatusBadge
                                :variant="group.compliant ? 'success' : 'warning'"
                                :label="
                                    group.compliant
                                        ? $t('dashboard.placementGroup.compliant')
                                        : $t('dashboard.placementGroup.notCompliant')
                                "
                            />
                            <span class="badge" :class="group.policy === 'pack' ? 'badge-info' : 'badge-primary'">{{
                                ruleText(t, te, group)
                            }}</span>
                        </h2>
                        <div class="resource-id-row">
                            <span class="resource-id-text">{{ group.id }}</span>
                            <button
                                class="copy-btn"
                                :title="$t('actions.copy')"
                                :aria-label="$t('actions.copy')"
                                @click="copyId(group.id, 'id')"
                            >
                                <Check v-if="copiedId === 'id'" :size="12" class="copied-icon" />
                                <Copy v-else :size="12" />
                            </button>
                        </div>
                    </div>
                </div>
                <div class="title-actions">
                    <button
                        class="btn btn-secondary btn-sm btn-icon"
                        :title="$t('actions.refresh')"
                        @click="fetchGroup(true)"
                    >
                        <RefreshCw :size="14" />
                    </button>
                    <button class="btn btn-secondary btn-sm" @click="editing = group">
                        <Pencil :size="14" /> {{ $t('actions.edit') }}
                    </button>
                    <button
                        class="btn btn-danger-outline btn-sm"
                        :disabled="!!deleteBlockedReason"
                        :title="deleteBlockedReason || undefined"
                        @click="openDelete"
                    >
                        <Trash2 :size="14" /> {{ $t('actions.delete') }}
                    </button>
                </div>
            </div>

            <!-- Not compliant: what is wrong, the known causes, and that nothing moves by itself -->
            <div v-if="!group.compliant" class="notice-banner" role="status">
                <AlertTriangle :size="16" class="notice-icon" />
                <div class="notice-body">
                    <p>{{ violation }}</p>
                    <p v-for="(cause, i) in causes" :key="i">{{ cause }}</p>
                    <p>{{ $t('dashboard.placementGroup.noAutoFix') }}</p>
                </div>
            </div>

            <div class="info-grid">
                <div class="card info-card">
                    <h3>{{ $t('dashboard.table.generalInformation') }}</h3>
                    <div class="key-value-list">
                        <InfoRow :label="$t('dashboard.table.name')">{{ group.name }}</InfoRow>
                        <InfoRow :label="$t('dashboard.table.description')">{{ group.description || '-' }}</InfoRow>
                        <InfoRow :label="$t('dashboard.table.zone')">{{ group.zone || '-' }}</InfoRow>
                        <InfoRow :label="$t('dashboard.table.createdAt')">{{
                            formatDateTime(group.created_at)
                        }}</InfoRow>
                    </div>
                </div>

                <div class="card info-card">
                    <h3>{{ $t('dashboard.placementGroup.ruleTitle') }}</h3>
                    <div class="key-value-list">
                        <InfoRow :label="$t('dashboard.placementGroup.policy')">{{
                            policyText(t, te, group.policy)
                        }}</InfoRow>
                        <InfoRow :label="$t('dashboard.placementGroup.strictness')">{{
                            strictText(t, group.strict)
                        }}</InfoRow>
                        <InfoRow :label="$t('dashboard.placementGroup.memberCount')">{{
                            group.member_count ?? 0
                        }}</InfoRow>
                        <InfoRow :label="$t('dashboard.placementGroup.distribution')">{{
                            group.host_count > 0
                                ? $t('dashboard.placementGroup.hostCount', { n: group.host_count })
                                : '-'
                        }}</InfoRow>
                    </div>
                    <p v-if="ruleHint(t, te, group)" class="rule-hint">{{ ruleHint(t, te, group) }}</p>
                </div>
            </div>

            <div class="card info-card">
                <h3>{{ $t('dashboard.placementGroup.members') }} ({{ members.length }})</h3>
                <p class="section-hint">{{ $t('dashboard.placementGroup.hostSlotHint') }}</p>
                <DataTable :columns="memberColumns" :rows="members" row-key="id">
                    <template #empty>
                        <div>
                            <Boxes :size="40" style="opacity: 0.3; margin-bottom: 12px" />
                            <p class="text-secondary">{{ $t('dashboard.placementGroup.noMembers') }}</p>
                        </div>
                    </template>

                    <template #cell-hostname="{ row: m }">
                        <router-link :to="{ name: 'instance-detail', params: { id: m.id } }" class="text-link">{{
                            m.hostname || m.id.slice(0, 8)
                        }}</router-link>
                    </template>

                    <template #cell-status="{ row: m }">
                        <StatusBadge :status="m.status" :label="instanceStatusText(m.status)" />
                    </template>

                    <template #cell-host_slot="{ row: m }">
                        <span v-if="m.host_slot > 0" class="slot" :class="{ 'slot-crowded': crowded.has(m.host_slot) }">
                            {{ $t('dashboard.placementGroup.hostSlot', { n: m.host_slot }) }}
                        </span>
                        <span v-else class="text-tertiary" :title="$t('dashboard.placementGroup.noLocationHint')">{{
                            $t('dashboard.placementGroup.noLocation')
                        }}</span>
                        <span v-if="m.target_slot > 0" class="slot-target">
                            → {{ $t('dashboard.placementGroup.hostSlot', { n: m.target_slot }) }}
                        </span>
                    </template>

                    <template #cell-hypervisor="{ row: m }">{{ m.hypervisor || '-' }}</template>

                    <template #cell-notes="{ row: m }">
                        <div v-if="hasNotes(m)" class="notes">
                            <!-- Stuck records still count as taking their host (§2.2): they are only pointed out -->
                            <div
                                v-if="m.stale_migration"
                                class="note note-warning"
                                :title="$t('dashboard.placementGroup.staleMigrationHint')"
                            >
                                <router-link
                                    v-if="isSystemAdmin && m.migration_id"
                                    :to="{ name: 'migration-detail', params: { id: m.migration_id } }"
                                    class="note-link"
                                    >{{ $t('dashboard.placementGroup.staleMigration') }}</router-link
                                >
                                <template v-else>{{ $t('dashboard.placementGroup.staleMigration') }}</template>
                            </div>
                            <div v-if="m.stale_provisioning" class="note note-warning">
                                {{ $t('dashboard.placementGroup.staleProvisioning') }}
                            </div>
                            <div v-if="m.last_migration_failed" class="note note-error">
                                {{ $t('dashboard.placementGroup.lastMigrationFailed') }}
                            </div>
                            <div v-if="m.ignored_placement" class="note">
                                {{ $t('dashboard.placementGroup.ignoredPlacement') }}
                            </div>
                        </div>
                        <span v-else class="text-tertiary">-</span>
                    </template>
                </DataTable>
            </div>
        </template>

        <PlacementGroupEditModal :group="editing" @close="editing = null" @saved="onEdited" />

        <DeleteModal
            :show="showDelete"
            :resource-name="group?.name"
            :resource-id="group?.id"
            :loading="deleting"
            :error="deleteError"
            @close="showDelete = false"
            @confirm="confirmDelete"
        />
    </div>
</template>

<style scoped>
/* .detail-header, .title-bar, .badge-* are global (index.css) */

.loading-container,
.error-container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-4);
    padding: 60px;
}

.notice-banner {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-2);
    margin-bottom: var(--spacing-4);
    padding: var(--spacing-3) var(--spacing-4);
    border-radius: var(--radius-md);
    background: var(--warning-light);
    color: var(--warning-dark);
    font-size: var(--font-size-sm);
    line-height: 1.6;
}

.notice-icon {
    flex-shrink: 0;
    margin-top: 3px;
}

.notice-body p {
    margin: 0;
}

.info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-5);
}

.info-card {
    padding: var(--spacing-5);
}

.info-card h3 {
    margin: 0 0 var(--spacing-4) 0;
    padding-bottom: var(--spacing-3);
    border-bottom: 1px solid var(--border-light);
    font-size: var(--font-size-base);
    font-weight: 600;
    color: var(--text-primary);
}

.key-value-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.rule-hint,
.section-hint {
    margin: var(--spacing-3) 0 0;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-tertiary);
}

.section-hint {
    margin: 0 0 var(--spacing-3);
}

.text-link {
    color: var(--primary-600);
    text-decoration: none;
}

.text-link:hover {
    text-decoration: underline;
}

.slot {
    white-space: nowrap;
}

/* Two members of a spread group on this host */
.slot-crowded {
    color: var(--warning-dark);
    font-weight: var(--font-weight-medium);
}

.slot-target {
    margin-left: var(--spacing-1);
    color: var(--text-secondary);
    white-space: nowrap;
}

.notes {
    display: flex;
    flex-direction: column;
    gap: 2px;
}

.note {
    font-size: var(--font-size-xs);
    line-height: 1.5;
    color: var(--text-secondary);
}

.note-warning {
    color: var(--warning-dark);
}

.note-error {
    color: var(--error-color);
}

.note-link {
    color: inherit;
    text-decoration: underline;
}
</style>
