// Getting a storage package where it is needed (shared-storage-design.md §6.1, §13.3): the creation wizard and the
// upgrade dialog list the packages of one storage type and let the admin upload one and accept its license on the
// spot, instead of sending them to the package list and losing what they had filled in. Pair it with
// StoragePackageUploadModal (show: showUpload, @done: uploaded, @close: uploadClosed) and StorageLicenseModal
// (show: !!licenseId, @accepted: accepted).
//
// active: whether the place using it is on screen (the wizard on its software step, the upgrade dialog open). On it
// loads the list; off it stops refreshing, forgets the package it was waiting for and closes its dialogs, so a license
// never pops up over another step or page, and a package accepted late never changes a choice made since.
import { ref, computed, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { storagePackagesApi, type StoragePackage } from '../api/storagePackages'
import { packageBusy } from '../utils/storageCluster'
import { errorMessage } from '../utils/error'

export interface StoragePackageFlowOptions {
    kind: () => string | undefined
    active: () => boolean
    /** A package the place can use: only for these the license is offered on its own once verified */
    wanted?: (pkg: StoragePackage) => boolean
    /** A package uploaded here and accepted, here or before, ready to be chosen */
    onAccepted?: (pkg: StoragePackage) => void
}

export function useStoragePackageFlow(opts: StoragePackageFlowOptions) {
    const { t } = useI18n()
    const packages = ref<StoragePackage[]>([])
    const loading = ref(false)
    const error = ref('')
    const showUpload = ref(false)
    const licenseId = ref('')
    // The package uploaded here last, and whether its license is still to be offered once it is verified
    const uploadedId = ref('')
    let offerPending = false
    const wanted = (p: StoragePackage) => (opts.wanted ? opts.wanted(p) : true)

    // The packages of this kind that are or will be usable; an upload from the browser left unfinished is resumed
    // from the upload dialog, a failed one is dealt with on the package list (or shown below, when it was uploaded here)
    const ofKind = computed(() =>
        packages.value.filter((p) => p.kind === opts.kind() && (p.status === 'ready' || packageBusy(p)))
    )
    const uploaded = computed(() => packages.value.find((p) => p.id === uploadedId.value) || null)
    // The package uploaded here failed (download or verification): it leaves the list, so its reason is shown instead
    const failed = computed(() => (uploaded.value?.status === 'error' ? uploaded.value : null))

    // Only the latest request counts: a slow poll answering after the load that followed an accept would bring back
    // the list from before it
    let generation = 0
    const load = async (quiet = false) => {
        const mine = ++generation
        if (!quiet) loading.value = true
        try {
            const list = (await storagePackagesApi.list({ limit: 500 })).storage_packages || []
            if (mine !== generation) return
            packages.value = list
            error.value = ''
            const pkg = uploaded.value
            if (offerPending && opts.active() && pkg && pkg.status !== 'uploading' && pkg.status !== 'verifying') {
                offerPending = false
                if (pkg.status === 'ready' && wanted(pkg)) {
                    if (pkg.accepted_by) opts.onAccepted?.(pkg)
                    else licenseId.value = pkg.id
                }
            }
        } catch (err) {
            if (mine === generation && !quiet) error.value = errorMessage(err, t('messages.error'))
        } finally {
            if (mine === generation) loading.value = false
        }
    }

    let timer: ReturnType<typeof setInterval> | null = null
    const stopTimer = () => {
        if (timer) clearInterval(timer)
        timer = null
    }
    watch(
        () => opts.active() && ofKind.value.some(packageBusy),
        (busy) => {
            if (busy && !timer) timer = setInterval(() => load(true), 5000)
            else if (!busy) stopTimer()
        }
    )
    watch(
        opts.active,
        (on) => {
            if (on) return void load()
            stopTimer()
            generation++
            offerPending = false
            uploadedId.value = ''
            showUpload.value = false
            licenseId.value = ''
        },
        { immediate: true }
    )
    onUnmounted(stopTimer)

    const uploadDone = (pkg: StoragePackage) => {
        showUpload.value = false
        uploadedId.value = pkg.id
        offerPending = true
        load(true)
    }
    // An upload stopped or cut off shows up as unfinished in the upload dialog next time
    const uploadClosed = () => {
        showUpload.value = false
        load(true)
    }
    const accepted = (pkg: StoragePackage) => {
        licenseId.value = ''
        load(true)
        if (opts.active()) opts.onAccepted?.(pkg)
    }

    return {
        packages,
        ofKind,
        uploaded,
        failed,
        loading,
        error,
        load,
        showUpload,
        licenseId,
        uploadDone,
        uploadClosed,
        accepted,
    }
}
