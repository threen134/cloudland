<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from '../../composables/useToast'
import { useCopyId } from '../../composables/useCopyId'
import { useListQuery } from '../../composables/useListQuery'
import { flavorsApi, type Flavor, type FlavorPayload } from '../../api/flavors'
import { useRegionStore } from '../../stores/region'
import { isValidName } from '../../utils/validation'

const region = useRegionStore()

import { Plus, SquareStack, MemoryStick, Search, Trash2, Cpu, HardDrive, Check, Copy, RefreshCw } from 'lucide-vue-next'
import BaseModal from '../../components/modals/BaseModal.vue'
import DeleteModal from '../../components/modals/DeleteModal.vue'
import PageToolbar from '../../components/base/PageToolbar.vue'
import DataTable, { type Column } from '../../components/base/DataTable.vue'
import PaginationBar from '../../components/base/PaginationBar.vue'

const toast = useToast()

const { copiedId, copyId } = useCopyId()

const createModalVisible = ref(false)
const creating = ref(false)
const createError = ref('')
const newFlavorForm = ref<FlavorPayload>({
    name: '',
    cpu: 1,
    memory: 1024, // Starting with 1GB in MB? Min was 16. Let's assume MB.
    disk: 20
})

const { t } = useI18n()
const isNameValid = computed(() => isValidName(newFlavorForm.value.name))


// 分页后排序只能排当前页，会误导用户，所以列上不再提供排序
const columns = computed<Column[]>(() => [
    { key: 'name', label: t('dashboard.table.nameId') },
    { key: 'cpu', label: t('specs.cpu') },
    { key: 'ram', label: t('specs.ram') },
    { key: 'disk', label: t('specs.storage') },
    { key: 'actions', label: t('dashboard.table.actions'), align: 'center' },
])

// 分页与搜索都在服务端做
const {
    items: flavors,
    total,
    page,
    pageSize,
    loading,
    error: loadError,
    search: searchQuery,
    load: fetchFlavors,
    reload: reloadFlavors,
} = useListQuery<Flavor>(
    async ({ offset, limit, query }) => {
        const response = await flavorsApi.fetchFlavors({ offset, limit, query: query || undefined })
        return { items: response.flavors || [], total: response.total ?? 0 }
    },
    { watchSources: [computed(() => region.currentRegionId)] }
)

const openCreateModal = () => {
    newFlavorForm.value = {
        name: '',
        cpu: 1,
        memory: 1024,
        disk: 20
    }
    createModalVisible.value = true
}

const closeCreateModal = () => {
    createModalVisible.value = false
    createError.value = ''
}

const handleCreateFlavor = async () => {
    createError.value = ''
    if (!newFlavorForm.value.name) {
        createError.value = t('dashboard.flavorActions.enterName')
        return
    }
    if (!isNameValid.value) {
        createError.value = t('messages.invalidHostname')
        return
    }

    
    creating.value = true
    try {
        await flavorsApi.createFlavor(newFlavorForm.value)
        // 列表按创建时间倒序，新建的在第一页
        await reloadFlavors()
        closeCreateModal()
        toast.success(t('messages.createSuccess'))
    } catch (err: any) {
        console.error('Failed to create flavor:', err)
        createError.value = err.response?.data?.error_message || err.message || t('messages.error')
    } finally {
        creating.value = false
    }
}

// --- Delete Confirmation Modal ---
const deleteModalVisible = ref(false)
const deletingResource = ref(false)
const deleteError = ref('')
const resourceToDelete = ref<any>(null)

const handleDeleteClick = (item: any) => {
    resourceToDelete.value = item
    deleteModalVisible.value = true
}
const closeDeleteModal = () => {
    deleteModalVisible.value = false
    resourceToDelete.value = null
    deleteError.value = ''
}
const confirmDelete = async () => {
    if (!resourceToDelete.value) return
    deletingResource.value = true
    deleteError.value = ''
    try {
        await flavorsApi.deleteFlavor(resourceToDelete.value.name || resourceToDelete.value.id)
        await fetchFlavors()
        closeDeleteModal()
        toast.success(t('messages.deleteSuccess'))
    } catch (error: any) {
        console.error('Failed to delete flavor:', error)
        deleteError.value = error.response?.data?.error_message || error.message || t('messages.error')
    } finally {
        deletingResource.value = false
    }
}

const formatRam = (val: number | string) => {
    const mb = typeof val === 'string' ? parseInt(val) : val
    if (!mb) return '-'
    if (mb >= 1024) {
        return `${(mb / 1024).toFixed(0)} ${t('specs.gb')}`
    }
    // If the value is very small (like 1, 2, 4), it's probably already in GB
    if (mb < 64) {
        return `${mb} ${t('specs.gb')}`
    }
    return `${mb} ${t('specs.mb')}`
}

onMounted(() => {
    if (region.currentRegionId) {
        fetchFlavors()
    }
})
</script>

<template>
  <div>
    <PageToolbar v-model:search="searchQuery">
      <template #actions>
        <button class="btn btn-secondary btn-sm btn-icon" @click="() => fetchFlavors()" :title="$t('actions.refresh')">
          <RefreshCw :size="14" :class="{ spinning: loading }" />
        </button>
        <button class="btn btn-primary btn-sm" @click="openCreateModal">
          <Plus :size="14" /> {{ $t('dashboard.buttons.createFlavor') }}
        </button>
      </template>
    </PageToolbar>

    <DataTable
      :columns="columns"
      :rows="flavors"
      :row-key="(flavor: any) => flavor.uuid || flavor.name"
      :loading="loading"
      :error="loadError"
      @retry="() => fetchFlavors()"
    >
      <template #empty>
        <div v-if="searchQuery">
          <Search :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p>{{ $t('messages.noResults') }}</p>
        </div>
        <div v-else>
          <SquareStack :size="48" style="opacity: 0.3; margin-bottom: 16px;" />
          <p class="text-secondary">{{ $t('messages.noData') }}</p>
        </div>
      </template>

      <template #cell-name="{ row: flavor }">
        <div class="resource-link-static">
          <div class="resource-info">
            <div class="resource-icon">
              <SquareStack :size="16" />
            </div>
            <div>
              <div class="resource-name">{{ flavor.name }}</div>
              <div class="resource-id-row">
                <span class="resource-id" :title="flavor.uuid">{{ (flavor.uuid || '').slice(0, 8) }}...</span>
                <button class="copy-btn-mini" @click.stop.prevent="flavor.uuid && copyId(flavor.uuid)" :title="t('actions.copy')" :aria-label="t('actions.copy')">
                  <Check v-if="copiedId === flavor.uuid" :size="10" style="color: #10b981;" />
                  <Copy v-else :size="10" />
                </button>
              </div>
            </div>
          </div>
        </div>
      </template>

      <template #cell-cpu="{ row: flavor }">
        <div class="spec-cell">
          <Cpu :size="14" class="text-secondary" />
          <span>{{ flavor.cpu || '-' }}</span>
        </div>
      </template>

      <template #cell-ram="{ row: flavor }">
        <div class="spec-cell">
          <MemoryStick :size="14" class="text-secondary" />
          <span>{{ formatRam(flavor.memory) }}</span>
        </div>
      </template>

      <template #cell-disk="{ row: flavor }">
        <div class="spec-cell">
          <HardDrive :size="14" class="text-secondary" />
          <span>{{ flavor.disk }} {{ $t('specs.gb') }}</span>
        </div>
      </template>

      <template #cell-actions="{ row: flavor }">
        <div class="actions">
          <button class="btn btn-ghost btn-sm text-error" :title="$t('actions.delete')" @click="handleDeleteClick(flavor)">
            <Trash2 :size="14" />
          </button>
        </div>
      </template>

      <template #footer>
        <PaginationBar :page="page" :page-size="pageSize" :total="total" @update:page="page = $event" />
      </template>
    </DataTable>

    <!-- Create Flavor Modal -->
    <BaseModal
      :show="createModalVisible"
      :title="$t('dashboard.buttons.createFlavor')"
      form
      :loading="creating"
      @close="closeCreateModal"
      @submit="handleCreateFlavor"
    >
      <div class="form-grid">
        <div class="form-group">
          <label class="form-label">{{ $t('dashboard.table.name') }}</label>
          <input
            v-model="newFlavorForm.name"
            type="text"
            :class="['form-input', { 'input-error': !isNameValid }]"
            :placeholder="$t('dashboard.forms.placeholder.flavorNameExample')"
          />
          <div v-if="!isNameValid" class="text-error text-xs mt-1">
            {{ $t('messages.invalidHostname') }}
          </div>

        </div>

        <div class="form-row">
          <div class="form-group flex-1">
            <label class="form-label">{{ $t('dashboard.table.vcpus') }}</label>
            <div class="input-with-unit">
              <input
                v-model.number="newFlavorForm.cpu"
                type="number"
                class="form-input"
                min="1"
              />
              <span class="unit">{{ $t('specs.cores').replace('{n}', '') }}</span>
            </div>
          </div>
          <div class="form-group flex-1">
            <label class="form-label">{{ $t('specs.ram') }}</label>
            <div class="input-with-unit">
              <input
                v-model.number="newFlavorForm.memory"
                type="number"
                class="form-input"
                min="16"
              />
              <span class="unit">{{ $t('specs.mb') }}</span>
            </div>
          </div>
        </div>

        <div class="form-group">
          <label class="form-label">{{ $t('dashboard.overview.disk') }}</label>
          <div class="input-with-unit">
            <input
              v-model.number="newFlavorForm.disk"
              type="number"
              class="form-input"
              min="1"
            />
            <span class="unit">{{ $t('specs.gb') }}</span>
          </div>
        </div>
      </div>

      <template #footer>
        <div class="modal-footer-stack">
          <div v-if="createError" class="text-error" style="font-size:var(--font-size-sm);background:var(--error-light);padding:var(--spacing-2);border-radius:var(--radius-sm)">
            {{ createError }}
          </div>
          <div style="display: flex; justify-content: flex-end; gap: var(--spacing-2);">
            <button type="button" class="btn btn-secondary" @click="closeCreateModal" :disabled="creating">{{ $t('actions.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="creating">
              <span v-if="creating" class="loading-spinner" style="width: 16px; height: 16px; border-width: 2px;"></span>
              {{ creating ? $t('messages.loading') : $t('dashboard.buttons.createFlavor') }}
            </button>
          </div>
        </div>
      </template>
    </BaseModal>

    <!-- Delete Confirmation Modal -->
    <DeleteModal
      :show="deleteModalVisible"
      :resource-name="resourceToDelete?.name"
      :resource-id="resourceToDelete?.uuid"
      :loading="deletingResource"
      :error="deleteError"
      @close="closeDeleteModal"
      @confirm="confirmDelete"
    />
  </div>
</template>

<style scoped>

/* .resource-info etc. are global from index.css */

.resource-link-static {
  display: block;
  padding: 4px 0;
}

.spec-cell {
  display: flex;
  align-items: center;
  gap: 8px;
  font-family: var(--font-family-mono);
}

.actions {
  display: flex;
  justify-content: center;
  gap: var(--spacing-2);
}

.text-error {
  color: var(--error-color);
}

/* Modal Styles */
.form-grid {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-4);
}

.form-row {
  display: flex;
  gap: var(--spacing-4);
}

.flex-1 {
  flex: 1;
}

.form-group {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-1);
}

.form-label {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--text-secondary);
}

.form-input {
  padding: 8px 12px;
  border-radius: var(--radius-md);
  border: 1px solid var(--border-light);
  background: var(--bg-primary);
  color: var(--text-primary);
  font-size: var(--font-size-sm);
  width: 100%;
}

.form-input:focus {
  outline: none;
  border-color: var(--primary-color);
  box-shadow: 0 0 0 2px var(--primary-100);
}

.input-with-unit {
  position: relative;
  display: flex;
  align-items: center;
}

.input-with-unit .form-input {
  padding-right: 60px;
}

.input-with-unit .unit {
  position: absolute;
  right: 12px;
  font-size: var(--font-size-xs);
  color: var(--text-tertiary);
  pointer-events: none;
}

/* 底部按钮区需要纵向堆叠（错误提示在按钮上方），.modal-footer 属于 BaseModal，这里用一层包裹元素 */
.modal-footer-stack {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: var(--spacing-2);
  width: 100%;
}

.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
