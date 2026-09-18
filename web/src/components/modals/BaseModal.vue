<script setup lang="ts">
// 所有弹窗的外壳。原先每个页面各写一遍 overlay + 内容骨架，于是没有一个弹窗
// 支持 ESC 关闭、背景滚动锁定和回车提交。这里统一实现一次。
//
// 视觉结构与原来完全一致（.modal-overlay > .modal-content.card > header/body/footer，
// 样式来自全局 index.css），所以迁移不改变外观。
//
// 注意：页面 scoped 样式只对自己模板里的元素生效，弹窗骨架元素现在属于本组件，
// 页面里针对 .modal-content 的宽度定制请改用 size / contentClass。
import { ref, watch, nextTick, onBeforeUnmount } from 'vue'
import { X } from 'lucide-vue-next'

const props = withDefaults(
    defineProps<{
        show: boolean
        title?: string
        /** 宽度档位：sm 400 / md 500（默认）/ lg 600 / xl 800 */
        size?: 'sm' | 'md' | 'lg' | 'xl'
        /** 额外的 class，加在 .modal-content 上 */
        contentClass?: string
        /** 提交进行中：关闭按钮禁用，ESC 与点击遮罩不生效，避免半途关掉 */
        loading?: boolean
        /** 不显示右上角关闭按钮 */
        hideClose?: boolean
        /** 用 <form> 包裹内容，回车提交（触发 submit 事件） */
        form?: boolean
    }>(),
    {
        size: 'md',
        contentClass: '',
    }
)

const emit = defineEmits<{ close: []; submit: [] }>()

const contentRef = ref<HTMLElement | null>(null)

const requestClose = () => {
    if (!props.loading) emit('close')
}

const onKeydown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') requestClose()
}

// 打开时锁住背景滚动，关闭或组件销毁时恢复。
// 记录原值而不是直接清空，避免与其他也改过 overflow 的代码互相覆盖。
let previousOverflow: string | null = null

const lockScroll = () => {
    if (previousOverflow === null) {
        previousOverflow = document.body.style.overflow
        document.body.style.overflow = 'hidden'
    }
}

const unlockScroll = () => {
    if (previousOverflow !== null) {
        document.body.style.overflow = previousOverflow
        previousOverflow = null
    }
}

watch(
    () => props.show,
    async (visible) => {
        if (visible) {
            lockScroll()
            document.addEventListener('keydown', onKeydown)
            // 焦点移入弹窗，否则键盘用户仍停留在背景页面上
            await nextTick()
            const focusable = contentRef.value?.querySelector<HTMLElement>(
                'input:not([type="hidden"]):not([disabled]), select:not([disabled]), textarea:not([disabled]), button:not([disabled])'
            )
            focusable?.focus()
        } else {
            unlockScroll()
            document.removeEventListener('keydown', onKeydown)
        }
    },
    { immediate: true }
)

onBeforeUnmount(() => {
    unlockScroll()
    document.removeEventListener('keydown', onKeydown)
})

const onSubmit = () => emit('submit')
</script>

<template>
    <div v-if="show" class="modal-overlay" @click.self="requestClose">
        <component
            :is="form ? 'form' : 'div'"
            ref="contentRef"
            class="modal-content card"
            :class="[`modal-${size}`, contentClass]"
            role="dialog"
            aria-modal="true"
            @submit.prevent="onSubmit"
        >
            <div v-if="title || $slots.header || !hideClose" class="modal-header">
                <slot name="header">
                    <h3>{{ title }}</h3>
                </slot>
                <button
                    v-if="!hideClose"
                    type="button"
                    class="btn btn-ghost btn-sm icon-btn"
                    :disabled="loading"
                    :aria-label="$t('actions.close')"
                    @click="requestClose"
                >
                    <X :size="18" />
                </button>
            </div>

            <div class="modal-body">
                <slot />
            </div>

            <div v-if="$slots.footer" class="modal-footer">
                <slot name="footer" />
            </div>
        </component>
    </div>
</template>

<style scoped>
/* 宽度档位；其余外观继承全局 .modal-overlay / .modal-content。
   窄屏下一律留出边距，避免弹窗贴到屏幕两侧 */
.modal-sm {
    max-width: min(400px, 92vw);
}
.modal-md {
    max-width: min(500px, 92vw);
}
.modal-lg {
    max-width: min(600px, 92vw);
}
.modal-xl {
    max-width: min(800px, 92vw);
}
</style>
