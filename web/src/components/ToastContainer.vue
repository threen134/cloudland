<script setup lang="ts">
import { useToast } from '../composables/useToast'
import { X, CheckCircle2, AlertCircle, AlertTriangle, Info } from 'lucide-vue-next'

const { toasts, removeToast } = useToast()

const iconMap = {
    success: CheckCircle2,
    error: AlertCircle,
    warning: AlertTriangle,
    info: Info,
}
</script>

<template>
  <Teleport to="body">
    <div class="toast-container" v-if="toasts.length > 0">
      <TransitionGroup name="toast">
        <div v-for="toast in toasts" :key="toast.id" :class="['toast-item', `toast-${toast.type}`]">
          <component :is="iconMap[toast.type]" :size="18" class="toast-icon" />
          <span class="toast-message">{{ toast.message }}</span>
          <button class="toast-close" @click="removeToast(toast.id)">
            <X :size="14" />
          </button>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.toast-container {
  position: fixed;
  top: 20px;
  right: 20px;
  z-index: 9999;
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: 420px;
}

.toast-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 16px;
  border-radius: 8px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  font-size: 0.875rem;
  color: #fff;
  min-width: 280px;
}

.toast-success { background: #059669; }
.toast-error { background: #dc2626; }
.toast-warning { background: #d97706; }
.toast-info { background: #2563eb; }

.toast-icon { flex-shrink: 0; }

.toast-message { flex: 1; line-height: 1.4; }

.toast-close {
  background: none;
  border: none;
  color: rgba(255, 255, 255, 0.7);
  cursor: pointer;
  padding: 2px;
  display: flex;
  align-items: center;
  border-radius: 4px;
  flex-shrink: 0;
}

.toast-close:hover { color: #fff; background: rgba(255, 255, 255, 0.15); }

/* Transitions */
.toast-enter-active { animation: toastIn 0.3s ease-out; }
.toast-leave-active { animation: toastOut 0.2s ease-in forwards; }

@keyframes toastIn {
  from { opacity: 0; transform: translateX(40px); }
  to { opacity: 1; transform: translateX(0); }
}

@keyframes toastOut {
  from { opacity: 1; transform: translateX(0); }
  to { opacity: 0; transform: translateX(40px); }
}
</style>
