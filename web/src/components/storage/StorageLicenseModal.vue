<script setup lang="ts">
// The license of a storage package (shared-storage-design.md §6.1): the full text in the language of the interface
// (English when the installer has none), and the box to accept it; a deployment can only use a package whose license
// is accepted, and who accepted it when is recorded.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseModal from '../modals/BaseModal.vue'
import { storagePackagesApi, type StoragePackage } from '../../api/storagePackages'
import { formatDateTime } from '../../utils/format'
import { errorMessage } from '../../utils/error'

const props = defineProps<{ show: boolean; packageId: string }>()
const emit = defineEmits<{ close: []; accepted: [pkg: StoragePackage] }>()
const { t, locale } = useI18n()

const pkg = ref<StoragePackage | null>(null)
const loading = ref(false)
const agreed = ref(false)
const submitting = ref(false)
const error = ref('')

const text = computed(() => {
    const license = pkg.value?.license || {}
    const lang = locale.value === 'zh-TW' ? 'zh_TW' : locale.value === 'zh' ? 'zh' : 'en'
    return license[lang] || license.en || ''
})

watch(
    () => [props.show, props.packageId],
    async () => {
        if (!props.show || !props.packageId) return
        agreed.value = false
        error.value = ''
        loading.value = true
        try {
            pkg.value = await storagePackagesApi.get(props.packageId)
        } catch (err) {
            error.value = errorMessage(err, t('messages.error'))
        } finally {
            loading.value = false
        }
    },
    { immediate: true }
)

const accept = async () => {
    if (!agreed.value || !pkg.value) return
    submitting.value = true
    error.value = ''
    try {
        emit('accepted', await storagePackagesApi.acceptLicense(pkg.value.id))
    } catch (err) {
        error.value = errorMessage(err, t('messages.error'))
    } finally {
        submitting.value = false
    }
}
</script>

<template>
    <BaseModal
        :show="show"
        :title="t('storage.packages.licenseTitle')"
        size="xl"
        :loading="submitting"
        form
        @close="emit('close')"
        @submit="accept"
    >
        <div class="license">
            <div v-if="loading" class="muted-line">{{ t('storage.packages.loading') }}</div>
            <template v-else-if="pkg">
                <div class="license-meta">
                    {{ pkg.file_name }} · {{ pkg.version }} · {{ pkg.edition }}
                    <span v-if="pkg.accepted_by" class="accepted">{{
                        t('storage.packages.acceptedBy', {
                            user: pkg.accepted_by,
                            time: formatDateTime(pkg.accepted_at || ''),
                        })
                    }}</span>
                </div>
                <pre class="license-text">{{ text }}</pre>
                <label v-if="!pkg.accepted_by" class="checkbox-inline">
                    <input v-model="agreed" type="checkbox" />
                    {{ t('storage.packages.agree') }}
                </label>
            </template>
        </div>
        <template #footer>
            <span v-if="error" class="footer-error text-error">{{ error }}</span>
            <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('actions.close') }}</button>
            <button
                v-if="pkg && !pkg.accepted_by"
                type="submit"
                class="btn btn-primary"
                :disabled="!agreed || submitting"
            >
                {{ t('storage.packages.accept') }}
            </button>
        </template>
    </BaseModal>
</template>

<style scoped>
.license {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-3);
}

.license-meta {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
}

.accepted {
    margin-left: var(--spacing-2);
    color: var(--success-dark);
}

.license-text {
    max-height: 55vh;
    overflow: auto;
    margin: 0;
    padding: var(--spacing-3);
    background: var(--bg-secondary);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    font-family: inherit;
    font-size: var(--font-size-xs);
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-word;
}

.footer-error {
    margin-right: auto;
    font-size: var(--font-size-sm);
}
</style>
