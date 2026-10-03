<script setup lang="ts">
// The creation wizard of a storage cluster (shared-storage-design.md §13.3): a page, not a dialog, the steps are
// many. A managed cluster: kind, software package, hosts with their roles and disks, parameters, the precheck of
// the hosts, confirmation; an imported cluster: kind, its hosts, what CloudLand must know of it, confirmation. The
// kinds and what each supports come from the backends (GET /storage_backends); the backend checks everything again.
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ArrowLeft, Check, ServerCog, Download, ClipboardCheck, Lock } from 'lucide-vue-next'
import {
    storageClustersApi,
    TASK_LIVE_STATUSES,
    type StorageBackend,
    type StorageTask,
    type StorageParams,
} from '../../api/storageClusters'
import { storagePackagesApi, type StoragePackage } from '../../api/storagePackages'
import StorageHostPicker, { type HostPick } from '../../components/storage/StorageHostPicker.vue'
import StoragePrecheckResults from '../../components/storage/StoragePrecheckResults.vue'
import StatusBadge from '../../components/base/StatusBadge.vue'
import { useGoBack } from '../../composables/useGoBack'
import { useToast } from '../../composables/useToast'
import { errorMessage } from '../../utils/error'
import { formatBytes } from '../../utils/format'
import { roleText, storageKindText, taskStatusText, taskVariant } from '../../utils/storageCluster'

const { t, te } = useI18n()
const router = useRouter()
const toast = useToast()
const goBack = useGoBack('storage-clusters')

type Mode = 'managed' | 'external'
type StepId = 'type' | 'software' | 'hosts' | 'params' | 'precheck' | 'confirm'

const backends = ref<StorageBackend[]>([])
const loadError = ref('')
const kind = ref('')
const mode = ref<Mode>('managed')
const backend = computed(() => backends.value.find((b) => b.kind === kind.value))

// ---- the steps ----
const steps = computed<StepId[]>(() => {
    if (mode.value === 'external') return ['type', 'hosts', 'params', 'confirm']
    const list: StepId[] = ['type']
    if (backend.value?.package) list.push('software')
    list.push('hosts', 'params', 'precheck', 'confirm')
    return list
})
const stepIndex = ref(0)
const step = computed(() => steps.value[stepIndex.value])

// ---- type ----
interface TypeOption {
    kind: string
    mode: Mode
    enabled: boolean
    later?: boolean
    label: string
    hint: string
}
const hintOf = (key: string) => (te(key) ? t(key) : '')
const typeOptions = computed<TypeOption[]>(() => {
    const list: TypeOption[] = []
    for (const b of backends.value) {
        list.push({
            kind: b.kind,
            mode: 'managed',
            enabled: b.capabilities?.managed,
            later: !b.capabilities?.managed,
            label: t('storage.wizard.managed', { kind: storageKindText(t, te, b.kind) }),
            hint: hintOf(`storage.wizard.managedHints.${b.kind}`) || hintOf(`storage.cluster.kindHints.${b.kind}`),
        })
        if (b.capabilities?.external) {
            list.push({
                kind: b.kind,
                mode: 'external',
                enabled: true,
                label: t('storage.wizard.external', { kind: storageKindText(t, te, b.kind) }),
                hint: hintOf(`storage.wizard.externalHints.${b.kind}`),
            })
        }
    }
    return list
})
const chooseType = (o: TypeOption) => {
    if (!o.enabled) return
    kind.value = o.kind
    mode.value = o.mode
}

// ---- software ----
const packages = ref<StoragePackage[]>([])
const packagesLoading = ref(false)
const packageId = ref('')
const kindPackages = computed(() => packages.value.filter((p) => p.kind === kind.value && p.status === 'ready'))
const loadPackages = async () => {
    packagesLoading.value = true
    try {
        packages.value = (await storagePackagesApi.list({ limit: 500 })).storage_packages || []
        const usable = kindPackages.value.filter((p) => p.accepted_by)
        if (!usable.some((p) => p.id === packageId.value)) packageId.value = usable[0]?.id || ''
    } catch (err) {
        loadError.value = errorMessage(err, t('messages.error'))
    } finally {
        packagesLoading.value = false
    }
}
const chosenPackage = computed(() => packages.value.find((p) => p.id === packageId.value))
const editionText = (e?: string) =>
    e && te(`storage.packages.editions.${e}`) ? t(`storage.packages.editions.${e}`) : e || '-'

// ---- hosts ----
const pick = ref<HostPick>({ roles: {}, disks: {} })
const picker = ref<InstanceType<typeof StorageHostPicker> | null>(null)
watch([kind, mode], () => {
    pick.value = { roles: {}, disks: {} }
})
const hostCount = computed(() => Object.keys(pick.value.roles).length)
const diskCount = computed(() => Object.keys(pick.value.disks).length)

// ---- params ----
const gpfs = ref({ fs_name: 'fs1', block_size: '4M', data_replicas: 2, pagepool_mib: 1024 })
const ceph = ref({ osd_memory_target_mib: 2048, image: '', cluster_network: '' })
const importParams = ref({ fs_name: '', mount_point: '' })
const cephImport = ref({ fsid: '', mon_addrs: '', client_user: 'cloudland', client_key: '' })
const uuidRe = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const monList = computed(() =>
    cephImport.value.mon_addrs
        .split(/[\s,]+/)
        .map((m) => m.trim())
        .filter(Boolean)
)
const testLayout = ref(false)
const allowUnsupported = ref(false)
const rawParams = ref('{}')
const params = computed<StorageParams | null>(() => {
    if (mode.value === 'external') {
        if (kind.value === 'ceph') {
            return {
                fsid: cephImport.value.fsid.trim().toLowerCase(),
                mon_addrs: monList.value,
                client_user: cephImport.value.client_user.trim(),
                client_key: cephImport.value.client_key.trim(),
            }
        }
        const p: StorageParams = { fs_name: importParams.value.fs_name.trim() }
        if (importParams.value.mount_point.trim()) p.mount_point = importParams.value.mount_point.trim()
        return p
    }
    if (kind.value === 'ceph') {
        return {
            test: testLayout.value || undefined,
            osd_memory_target_mib: Number(ceph.value.osd_memory_target_mib) || undefined,
            image: ceph.value.image.trim() || undefined,
            cluster_network: ceph.value.cluster_network.trim() || undefined,
        }
    }
    if (kind.value === 'gpfs') {
        return {
            test: testLayout.value || undefined,
            fs_name: gpfs.value.fs_name.trim() || undefined,
            block_size: gpfs.value.block_size,
            data_replicas: Number(gpfs.value.data_replicas) || undefined,
            pagepool_mib: Number(gpfs.value.pagepool_mib) || undefined,
        }
    }
    // A kind without a form of its own: its parameters as JSON
    try {
        const p = JSON.parse(rawParams.value || '{}')
        return typeof p === 'object' && p && !Array.isArray(p) ? { ...p, test: testLayout.value || undefined } : null
    } catch {
        return null
    }
})
const paramsValid = computed(() => {
    if (!params.value) return false
    if (mode.value === 'external' && kind.value === 'ceph') {
        const c = cephImport.value
        return (
            uuidRe.test(c.fsid.trim()) &&
            monList.value.length > 0 &&
            monList.value.length <= 9 &&
            monList.value.every((m) => /^\d{1,3}(\.\d{1,3}){3}(:\d{2,5})?$/.test(m)) &&
            /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(c.client_user.trim()) &&
            !c.client_user.trim().startsWith('client.') &&
            /^[A-Za-z0-9+/]{38,64}={0,2}$/.test(c.client_key.trim())
        )
    }
    if (mode.value === 'external') return /^[A-Za-z][A-Za-z0-9_]{0,31}$/.test(importParams.value.fs_name.trim())
    if (kind.value === 'ceph') {
        const c = ceph.value
        const mem = Number(c.osd_memory_target_mib)
        return (
            mem >= 896 &&
            mem <= 65536 &&
            (!c.image.trim() || /^[a-z0-9][a-z0-9./:_@-]{0,199}$/.test(c.image.trim())) &&
            (!c.cluster_network.trim() || /^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/.test(c.cluster_network.trim()))
        )
    }
    if (kind.value === 'gpfs')
        return !gpfs.value.fs_name.trim() || /^[A-Za-z][A-Za-z0-9_]{0,31}$/.test(gpfs.value.fs_name.trim())
    return true
})
watch(testLayout, (test) => {
    if (kind.value === 'gpfs') gpfs.value.data_replicas = test ? 1 : 2
})

// ---- precheck ----
const precheckTask = ref<StorageTask | null>(null)
const precheckBusy = ref(false)
const precheckError = ref('')
let precheckTimer: ReturnType<typeof setInterval> | null = null
const stopPolling = () => {
    if (precheckTimer) clearInterval(precheckTimer)
    precheckTimer = null
}
onUnmounted(stopPolling)
// Anything changed before the precheck makes its result stale
watch(
    [pick, params, allowUnsupported, packageId],
    () => {
        precheckTask.value = null
        stopPolling()
    },
    { deep: true }
)
const precheckLive = computed(() => !!precheckTask.value && TASK_LIVE_STATUSES.includes(precheckTask.value.status))
const precheckPassed = computed(() => precheckTask.value?.status === 'succeeded')
const startPrecheck = async () => {
    if (!picker.value || !params.value) return
    precheckBusy.value = true
    precheckError.value = ''
    stopPolling()
    try {
        const task = await storageClustersApi.precheck({
            kind: kind.value,
            ...picker.value.payload(),
            params: params.value,
            allow_unsupported: allowUnsupported.value,
        })
        precheckTask.value = await storageClustersApi.getTask(task.id)
        precheckTimer = setInterval(async () => {
            if (!precheckTask.value) return stopPolling()
            try {
                precheckTask.value = await storageClustersApi.getTask(precheckTask.value.id)
            } catch {
                // keep the last view; the next round tries again
            }
            if (!precheckLive.value) stopPolling()
        }, 3000)
    } catch (err) {
        precheckError.value = errorMessage(err, t('storage.cluster.actionFailed'))
    } finally {
        precheckBusy.value = false
    }
}

// ---- confirm ----
const name = ref('')
const description = ref('')
const submitting = ref(false)
const submitError = ref('')
const nameValid = computed(() => /^[A-Za-z][A-Za-z0-9._-]{0,62}$/.test(name.value))

const submit = async () => {
    if (!picker.value || !params.value || !nameValid.value) return
    submitting.value = true
    submitError.value = ''
    try {
        const plan = picker.value.payload()
        const cluster =
            mode.value === 'external'
                ? await storageClustersApi.import({
                      kind: kind.value,
                      name: name.value,
                      description: description.value || undefined,
                      hypervisors: plan.nodes.map((n) => n.hypervisor),
                      params: params.value,
                  })
                : await storageClustersApi.create({
                      kind: kind.value,
                      name: name.value,
                      description: description.value || undefined,
                      package_id: backend.value?.package ? packageId.value : undefined,
                      ...plan,
                      params: params.value,
                      allow_unsupported: allowUnsupported.value,
                  })
        toast.success(mode.value === 'external' ? t('storage.wizard.importStarted') : t('storage.wizard.deployStarted'))
        router.push({ name: 'storage-cluster-detail', params: { id: cluster.id }, query: { tab: 'tasks' } })
    } catch (err) {
        submitError.value = errorMessage(err, t('storage.cluster.actionFailed'))
    } finally {
        submitting.value = false
    }
}

// ---- moving between steps ----
const stepValid = computed(() => {
    switch (step.value) {
        case 'type':
            return !!backend.value
        case 'software':
            return !!chosenPackage.value?.accepted_by
        case 'hosts':
            return hostCount.value > 0 && !(picker.value?.missingWipe ?? false)
        case 'params':
            return paramsValid.value
        case 'precheck':
            return precheckPassed.value
        case 'confirm':
            return nameValid.value && !submitting.value
    }
    return false
})
const next = () => {
    if (!stepValid.value) return
    if (step.value === 'confirm') return submit()
    stepIndex.value++
    if (step.value === 'software') loadPackages()
}
const back = () => {
    if (stepIndex.value > 0) stepIndex.value--
}
watch(steps, () => {
    if (stepIndex.value >= steps.value.length) stepIndex.value = steps.value.length - 1
})

const stepLabel = (s: StepId) => t(`storage.wizard.steps.${s}`)
// The parameters as the summary shows them: a key is never shown
const paramsText = computed(() => {
    if (!params.value) return ''
    const p = { ...params.value }
    if (p.client_key) p.client_key = '******'
    return JSON.stringify(p)
})
const selectedHosts = computed(() => picker.value?.selectedHosts || [])

onMounted(async () => {
    try {
        backends.value = await storageClustersApi.backends()
        const first = typeOptions.value.find((o) => o.enabled)
        if (first) chooseType(first)
    } catch (err) {
        loadError.value = errorMessage(err, t('messages.error'))
    }
})
</script>

<template>
    <div class="detail-page">
        <div class="detail-header">
            <button class="btn btn-ghost btn-sm" @click="goBack">
                <ArrowLeft :size="14" /> {{ t('actions.back') }}
            </button>
        </div>
        <div class="title-bar">
            <div class="title-info">
                <div class="title-icon"><ServerCog :size="20" /></div>
                <div>
                    <h2 class="resource-title">{{ t('storage.wizard.title') }}</h2>
                    <div class="resource-id-row">
                        <span class="resource-id-text">{{ t('storage.wizard.subtitle') }}</span>
                    </div>
                </div>
            </div>
        </div>

        <ol class="stepper">
            <li v-for="(s, i) in steps" :key="s" :class="{ active: i === stepIndex, done: i < stepIndex }">
                <span class="step-no"
                    ><Check v-if="i < stepIndex" :size="12" /><template v-else>{{ i + 1 }}</template></span
                >
                <span class="step-label">{{ stepLabel(s) }}</span>
            </li>
        </ol>

        <div class="card info-card wizard-card">
            <div v-if="loadError" class="text-error">{{ loadError }}</div>

            <!-- 1. Kind and mode -->
            <template v-if="step === 'type'">
                <h3>{{ t('storage.wizard.typeTitle') }}</h3>
                <div class="type-grid">
                    <button
                        v-for="o in typeOptions"
                        :key="`${o.kind}-${o.mode}`"
                        type="button"
                        class="type-card"
                        :class="{ active: kind === o.kind && mode === o.mode, disabled: !o.enabled }"
                        :disabled="!o.enabled"
                        @click="chooseType(o)"
                    >
                        <span class="type-card-head">
                            <ServerCog v-if="o.mode === 'managed'" :size="18" />
                            <Download v-else :size="18" />
                            <span class="type-card-title">{{ o.label }}</span>
                            <span v-if="o.later" class="badge badge-secondary">{{ t('storage.wizard.later') }}</span>
                        </span>
                        <span class="type-card-hint">{{ o.hint }}</span>
                    </button>
                    <div v-if="backends.some((b) => b.kind === 'gpfs')" class="type-card disabled">
                        <span class="type-card-head">
                            <Lock :size="18" />
                            <span class="type-card-title">{{ t('storage.wizard.ece') }}</span>
                            <span class="badge badge-secondary">{{ t('storage.wizard.later') }}</span>
                        </span>
                        <span class="type-card-hint">{{ t('storage.wizard.eceHint') }}</span>
                    </div>
                </div>
            </template>

            <!-- 2. Software -->
            <template v-else-if="step === 'software'">
                <h3>{{ t('storage.wizard.softwareTitle') }}</h3>
                <p class="intro">{{ t('storage.wizard.softwareIntro') }}</p>
                <div v-if="packagesLoading" class="muted-line">{{ t('messages.loading') }}</div>
                <div v-else-if="kindPackages.length === 0" class="muted-line">
                    {{ t('storage.wizard.noPackage') }}
                    <router-link :to="{ name: 'storage-packages' }" class="resource-link">{{
                        t('storage.packagesPage')
                    }}</router-link>
                </div>
                <div v-else class="package-list">
                    <label
                        v-for="p in kindPackages"
                        :key="p.id"
                        class="package-row"
                        :class="{ disabled: !p.accepted_by, active: packageId === p.id }"
                    >
                        <input v-model="packageId" type="radio" :value="p.id" :disabled="!p.accepted_by" />
                        <span class="package-text">
                            <span class="name">{{ p.file_name }}</span>
                            <span class="sub"
                                >{{ p.version }} · {{ editionText(p.edition) }} · {{ (p.distros || []).join(', ') }} ·
                                {{ formatBytes(p.size_bytes) }}</span
                            >
                        </span>
                        <span v-if="!p.accepted_by" class="badge badge-warning">{{
                            t('storage.wizard.licenseNotAccepted')
                        }}</span>
                    </label>
                </div>
            </template>

            <!-- 3. Hosts, roles, disks -->
            <template v-if="steps.includes('hosts')">
                <div v-show="step === 'hosts'">
                    <h3>
                        {{
                            mode === 'external' ? t('storage.wizard.importHostsTitle') : t('storage.wizard.hostsTitle')
                        }}
                    </h3>
                    <p class="intro">
                        {{
                            mode === 'external'
                                ? hintOf(`storage.wizard.importHostsIntros.${kind}`) ||
                                  t('storage.wizard.importHostsIntro')
                                : hintOf(`storage.wizard.hostsIntros.${kind}`) || t('storage.wizard.hostsIntro')
                        }}
                    </p>
                    <StorageHostPicker
                        ref="picker"
                        v-model="pick"
                        :backend="backend"
                        :mode="mode === 'external' ? 'hosts' : 'roles'"
                    />
                    <p v-if="picker?.missingWipe" class="text-secondary">{{ t('storage.cluster.wipeNeeded') }}</p>
                </div>
            </template>

            <!-- 4. Parameters -->
            <template v-if="step === 'params'">
                <h3>{{ t('storage.wizard.paramsTitle') }}</h3>
                <template v-if="mode === 'external' && kind === 'ceph'">
                    <p class="intro">{{ t('storage.wizard.cephImportIntro') }}</p>
                    <div class="form-grid">
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.cephFsid') }} *</label>
                            <input v-model="cephImport.fsid" type="text" class="form-input mono" maxlength="36" />
                            <span class="form-hint">{{ t('storage.wizard.cephFsidHint') }}</span>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.cephMons') }} *</label>
                            <input v-model="cephImport.mon_addrs" type="text" class="form-input mono" />
                            <span class="form-hint">{{ t('storage.wizard.cephMonsHint') }}</span>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.cephUser') }} *</label>
                            <input
                                v-model="cephImport.client_user"
                                type="text"
                                class="form-input mono"
                                maxlength="64"
                            />
                            <span class="form-hint">{{ t('storage.wizard.cephUserHint') }}</span>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.cephKey') }} *</label>
                            <input
                                v-model="cephImport.client_key"
                                type="password"
                                class="form-input mono"
                                autocomplete="off"
                                maxlength="70"
                            />
                            <span class="form-hint">{{ t('storage.wizard.cephKeyHint') }}</span>
                        </div>
                    </div>
                </template>
                <template v-else-if="mode === 'external'">
                    <p class="intro">{{ t('storage.wizard.importParamsIntro') }}</p>
                    <div class="form-grid">
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.fsName') }} *</label>
                            <input v-model="importParams.fs_name" type="text" class="form-input" maxlength="32" />
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.mountPoint') }}</label>
                            <input
                                v-model="importParams.mount_point"
                                type="text"
                                class="form-input"
                                :placeholder="`/gpfs/${importParams.fs_name || 'fs1'}`"
                            />
                        </div>
                    </div>
                </template>
                <template v-else-if="kind === 'gpfs'">
                    <div class="form-grid">
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.fsName') }}</label>
                            <input v-model="gpfs.fs_name" type="text" class="form-input" maxlength="32" />
                            <span class="form-hint">{{ t('storage.wizard.fsNameHint') }}</span>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.blockSize') }}</label>
                            <select v-model="gpfs.block_size" class="form-input">
                                <option v-for="b in ['1M', '2M', '4M', '8M', '16M']" :key="b" :value="b">
                                    {{ b }}
                                </option>
                            </select>
                            <span class="form-hint">{{ t('storage.wizard.blockSizeHint') }}</span>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.dataReplicas') }}</label>
                            <select v-model.number="gpfs.data_replicas" class="form-input">
                                <option v-if="testLayout" :value="1">1</option>
                                <option :value="2">2</option>
                                <option :value="3">3</option>
                            </select>
                            <span class="form-hint">{{ t('storage.wizard.dataReplicasHint') }}</span>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.pagepool') }}</label>
                            <input
                                v-model.number="gpfs.pagepool_mib"
                                type="number"
                                min="256"
                                step="256"
                                class="form-input"
                            />
                            <span class="form-hint">{{ t('storage.wizard.pagepoolHint') }}</span>
                        </div>
                    </div>
                </template>
                <template v-else-if="kind === 'ceph'">
                    <div class="form-grid">
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.cephReplicas') }}</label>
                            <input :value="testLayout ? 1 : 3" type="text" class="form-input" disabled />
                            <span class="form-hint">{{ t('storage.wizard.cephReplicasHint') }}</span>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.cephOsdMemory') }}</label>
                            <input
                                v-model.number="ceph.osd_memory_target_mib"
                                type="number"
                                min="896"
                                max="65536"
                                step="256"
                                class="form-input"
                            />
                            <span class="form-hint">{{ t('storage.wizard.cephOsdMemoryHint') }}</span>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.cephImage') }}</label>
                            <input v-model="ceph.image" type="text" class="form-input mono" />
                            <span class="form-hint">{{ t('storage.wizard.cephImageHint') }}</span>
                        </div>
                        <div class="form-group">
                            <label class="form-label">{{ t('storage.wizard.cephClusterNetwork') }}</label>
                            <input
                                v-model="ceph.cluster_network"
                                type="text"
                                class="form-input mono"
                                placeholder="10.0.1.0/24"
                            />
                            <span class="form-hint">{{ t('storage.wizard.cephClusterNetworkHint') }}</span>
                        </div>
                    </div>
                </template>
                <template v-else>
                    <div class="form-group">
                        <label class="form-label">{{ t('storage.wizard.rawParams') }}</label>
                        <textarea v-model="rawParams" class="form-input mono" rows="6" />
                    </div>
                </template>
                <label v-if="mode === 'managed'" class="checkbox-inline" :title="t('storage.cluster.testLayoutHint')">
                    <input v-model="testLayout" type="checkbox" />
                    {{ t('storage.cluster.testLayout') }}
                </label>
                <span v-if="testLayout" class="form-hint warn-hint">{{ t('storage.cluster.testLayoutHint') }}</span>
            </template>

            <!-- 5. Precheck -->
            <template v-else-if="step === 'precheck'">
                <h3>{{ t('storage.wizard.precheckTitle') }}</h3>
                <p class="intro">{{ t('storage.wizard.precheckIntro') }}</p>
                <div class="precheck-bar">
                    <label class="checkbox-inline" :title="t('storage.cluster.allowUnsupportedHint')">
                        <input v-model="allowUnsupported" type="checkbox" />
                        {{ t('storage.cluster.allowUnsupported') }}
                    </label>
                    <button
                        type="button"
                        class="btn btn-primary btn-sm"
                        :disabled="precheckBusy || precheckLive"
                        @click="startPrecheck"
                    >
                        <ClipboardCheck :size="14" />
                        {{ precheckTask ? t('storage.wizard.precheckAgain') : t('storage.cluster.startPrecheck') }}
                    </button>
                    <StatusBadge
                        v-if="precheckTask"
                        :variant="taskVariant(precheckTask.status)"
                        :label="taskStatusText(t, te, precheckTask.status)"
                    />
                    <router-link
                        v-if="precheckTask"
                        :to="{ name: 'storage-task-detail', params: { id: precheckTask.id } }"
                        class="resource-link"
                        target="_blank"
                        >{{ t('storage.wizard.taskDetail') }}</router-link
                    >
                </div>
                <div v-if="allowUnsupported" class="form-hint warn-hint">
                    {{ t('storage.cluster.allowUnsupportedHint') }}
                </div>
                <div v-if="precheckError" class="text-error">{{ precheckError }}</div>
                <div v-if="precheckTask?.status === 'failed'" class="text-error">
                    {{ precheckTask.message || t('storage.wizard.precheckFailed') }}
                </div>
                <StoragePrecheckResults :task="precheckTask" />
            </template>

            <!-- 6. Confirm -->
            <template v-else-if="step === 'confirm'">
                <h3>{{ t('storage.wizard.confirmTitle') }}</h3>
                <div class="form-grid">
                    <div class="form-group">
                        <label class="form-label">{{ t('storage.wizard.clusterName') }} *</label>
                        <input v-model="name" type="text" class="form-input" maxlength="63" />
                        <span class="form-hint" :class="{ 'text-error': name && !nameValid }">{{
                            t('storage.wizard.clusterNameHint')
                        }}</span>
                    </div>
                    <div class="form-group">
                        <label class="form-label">{{ t('dashboard.table.description') }}</label>
                        <input v-model="description" type="text" class="form-input" maxlength="256" />
                    </div>
                </div>
                <dl class="summary">
                    <dt>{{ t('storage.cluster.kind') }}</dt>
                    <dd>{{ typeOptions.find((o) => o.kind === kind && o.mode === mode)?.label }}</dd>
                    <template v-if="chosenPackage && mode === 'managed'">
                        <dt>{{ t('storage.wizard.package') }}</dt>
                        <dd>{{ chosenPackage.file_name }} ({{ chosenPackage.version }})</dd>
                    </template>
                    <dt>{{ t('storage.cluster.hostsLabel') }}</dt>
                    <dd>
                        <div v-for="h in selectedHosts" :key="h.uuid">
                            {{ h.hostname }}
                            <span v-if="mode === 'managed'" class="text-secondary">
                                · {{ (pick.roles[h.uuid] || []).map((r) => roleText(t, te, r)).join(', ') }}
                            </span>
                        </div>
                    </dd>
                    <template v-if="mode === 'managed'">
                        <dt>{{ t('storage.cluster.disksLabel') }}</dt>
                        <dd>
                            {{ t('storage.wizard.diskSummary', { n: diskCount }) }}
                            <span v-if="Object.values(pick.disks).some(Boolean)" class="text-error">
                                ·
                                {{
                                    t('storage.wizard.wipeSummary', {
                                        n: Object.values(pick.disks).filter(Boolean).length,
                                    })
                                }}</span
                            >
                        </dd>
                    </template>
                    <dt>{{ t('storage.wizard.paramsTitle') }}</dt>
                    <dd class="mono">{{ paramsText }}</dd>
                </dl>
                <p class="intro">
                    {{
                        mode === 'external'
                            ? hintOf(`storage.wizard.importNotes.${kind}`) || t('storage.wizard.importNote')
                            : t('storage.wizard.deployNote')
                    }}
                </p>
            </template>
        </div>

        <div class="wizard-footer">
            <span v-if="submitError" class="text-error footer-error">{{ submitError }}</span>
            <button type="button" class="btn btn-secondary" :disabled="stepIndex === 0 || submitting" @click="back">
                {{ t('storage.wizard.back') }}
            </button>
            <button type="button" class="btn btn-primary" :disabled="!stepValid" @click="next">
                {{
                    step === 'confirm'
                        ? submitting
                            ? t('storage.cluster.starting')
                            : mode === 'external'
                              ? t('storage.wizard.import')
                              : t('storage.wizard.deploy')
                        : t('storage.wizard.next')
                }}
            </button>
        </div>
    </div>
</template>

<style scoped>
.stepper {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-2) var(--spacing-5);
    list-style: none;
    padding: 0;
    margin: var(--spacing-4) 0;
}

.stepper li {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text-tertiary);
    font-size: var(--font-size-sm);
}

.stepper li.active {
    color: var(--text-primary);
    font-weight: 600;
}

.stepper li.done {
    color: var(--success-dark);
}

.step-no {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    border: 1px solid currentColor;
    font-size: var(--font-size-xs);
}

.stepper li.active .step-no {
    background: var(--primary-color);
    border-color: var(--primary-color);
    color: var(--text-inverse);
}

.wizard-card {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-4);
}

.wizard-card h3 {
    margin: 0;
}

.intro {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.6;
}

.type-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: var(--spacing-3);
}

.type-card {
    display: flex;
    flex-direction: column;
    gap: 6px;
    text-align: left;
    padding: var(--spacing-3) var(--spacing-4);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-primary);
    cursor: pointer;
}

.type-card.active {
    border-color: var(--primary-color);
    background: var(--primary-light);
}

.type-card.disabled {
    cursor: not-allowed;
    opacity: 0.55;
}

.type-card-head {
    display: flex;
    align-items: center;
    gap: 8px;
}

.type-card-title {
    font-weight: 600;
}

.type-card-hint {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    line-height: 1.5;
}

.package-list {
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
}

.package-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-light);
    cursor: pointer;
}

.package-row:last-child {
    border-bottom: none;
}

.package-row.active {
    background: var(--bg-secondary);
}

.package-row.disabled {
    opacity: 0.6;
    cursor: not-allowed;
}

.package-text {
    flex: 1;
    min-width: 0;
}

.package-text .name {
    display: block;
    font-weight: 500;
    font-size: var(--font-size-sm);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.package-text .sub {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
}

.form-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: var(--spacing-4);
}

.form-hint {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    margin-top: 4px;
}

.warn-hint {
    color: var(--warning-dark);
}

.checkbox-inline {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: var(--font-size-sm);
}

.muted-line {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
}

.precheck-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--spacing-3);
}

.summary {
    display: grid;
    grid-template-columns: 144px minmax(0, 1fr);
    gap: 8px var(--spacing-4);
    margin: 0;
    font-size: var(--font-size-sm);
}

.summary dt {
    color: var(--text-secondary);
}

.summary dd {
    margin: 0;
    word-break: break-word;
}

.mono {
    font-family: var(--font-family-mono);
    font-size: var(--font-size-xs);
}

.wizard-footer {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: var(--spacing-2);
    margin-top: var(--spacing-4);
}

.footer-error {
    margin-right: auto;
    font-size: var(--font-size-sm);
}
</style>
