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
          <component :is="iconMap[toast.type]" :size="20" class="toast-icon" />
          <span class="toast-message">{{ toast.message }}</span>
          <button class="toast-close" @click="removeToast(toast.id)" aria-label="Close">
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
  top: 24px;
  right: 24px;
  z-index: 10000;
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-width: 400px;
  pointer-events: none;
}

.toast-item {
  pointer-events: auto;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 16px;
  background: var(--bg-primary);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-card), 0 8px 24px rgba(0, 0, 0, 0.08);
  min-width: 300px;
  animation-fill-mode: forwards;
}

.toast-icon {
  flex-shrink: 0;
}

.toast-message {
  flex: 1;
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-medium);
  color: var(--text-primary);
  line-height: var(--line-height-normal);
}

.toast-close {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 4px;
  border: none;
  background: transparent;
  color: var(--text-light);
  cursor: pointer;
  border-radius: var(--radius-sm);
  transition: all var(--transition-fast);
  flex-shrink: 0;
}

.toast-close:hover {
  background: var(--bg-tertiary);
  color: var(--text-secondary);
}

/* Icon colors per type */
.toast-success .toast-icon { color: var(--success-color); }
.toast-error .toast-icon { color: var(--error-color); }
.toast-warning .toast-icon { color: var(--warning-color); }
.toast-info .toast-icon { color: var(--primary-color); }

/* Transitions */
.toast-enter-active {
  animation: toast-in 0.3s cubic-bezier(0.4, 0, 0.2, 1);
}
.toast-leave-active {
  animation: toast-out 0.2s ease-in forwards;
}
.toast-move {
  transition: transform 0.3s ease;
}

@keyframes toast-in {
  from { opacity: 0; transform: translateY(-12px) scale(0.96); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}

@keyframes toast-out {
  from { opacity: 1; transform: translateX(0); }
  to { opacity: 0; transform: translateX(30px); }
}
</style>
