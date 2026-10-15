<script lang="ts">
// Dialogs open right now, the last one on top: a dialog can open another over itself (uploading a package from the
// storage upgrade dialog), and ESC closes only the topmost. Module scope: <script setup> runs once per instance
const openStack: symbol[] = []

// The page behind does not scroll while any dialog is open. Kept with the stack rather than per dialog: dialogs do
// not always close in the reverse order they opened (a license dialog pops up on its own over a dialog that then
// closes), and a value saved per dialog would restore the lock of another one
let savedOverflow: string | null = null
const syncScrollLock = () => {
    if (openStack.length && savedOverflow === null) {
        savedOverflow = document.body.style.overflow
        document.body.style.overflow = 'hidden'
    } else if (!openStack.length && savedOverflow !== null) {
        document.body.style.overflow = savedOverflow
        savedOverflow = null
    }
}
</script>

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
const self = Symbol('modal')
const pushOpen = () => {
    if (!openStack.includes(self)) openStack.push(self)
    syncScrollLock()
}
const popOpen = () => {
    const i = openStack.indexOf(self)
    if (i >= 0) openStack.splice(i, 1)
    syncScrollLock()
}

const requestClose = () => {
    if (!props.loading) emit('close')
}

const onKeydown = (e: KeyboardEvent) => {
    if (e.key === 'Escape' && openStack[openStack.length - 1] === self) requestClose()
}

// Initial focus: the element marked autofocus, else the first visible form control of the body, else the
// dialog itself. Never a button: the close button in the header comes first in document order, and Enter on
// it closed the dialog right after it opened. Nor a checkbox, radio or file input: Enter on a checkbox submits
// the form in Chromium (a maintenance dialog starts with one), so when one of them comes first the dialog
// itself takes the focus.
const FIELD_SELECTOR = [
    'input:not([type="hidden"]):not([type="button"]):not([type="submit"]):not([type="reset"]):not([type="image"]):not([disabled]):not([readonly])',
    'select:not([disabled])',
    'textarea:not([disabled]):not([readonly])',
].join(', ')
const NO_INITIAL_FOCUS_TYPES = new Set(['checkbox', 'radio', 'file'])

const isVisible = (el: HTMLElement) => el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden'

const focusInitial = () => {
    const root = contentRef.value
    if (!root) return
    const marked = root.querySelector<HTMLElement>('[autofocus]')
    const first =
        marked && isVisible(marked)
            ? marked
            : Array.from(root.querySelectorAll<HTMLElement>(FIELD_SELECTOR)).find(
                  (el) => !el.closest('.modal-header') && el.tabIndex >= 0 && isVisible(el)
              )
    const field = first instanceof HTMLInputElement && NO_INITIAL_FOCUS_TYPES.has(first.type) ? undefined : first
    const target = field ?? root
    target.focus()
}

// Enter in a field of a form dialog submits it (implicit submission). It does so only once the user has changed
// something in the dialog: the initial focus is in the first field now, and Enter right after opening must not
// send the prefilled or default values (an unchanged resize still stops and starts the instance). Enter on a
// checkbox or radio never submits. Clicking the submit button is not affected.
let edited = false
const onFieldEdit = () => {
    edited = true
}
const onEnterKey = (e: KeyboardEvent) => {
    if (!props.form || e.isComposing) return
    const el = e.target
    if (!(el instanceof HTMLInputElement)) return
    if (!edited || el.type === 'checkbox' || el.type === 'radio') e.preventDefault()
}

watch(
    () => props.show,
    async (visible) => {
        if (visible) {
            edited = false
            pushOpen()
            document.addEventListener('keydown', onKeydown)
            // 焦点移入弹窗，否则键盘用户仍停留在背景页面上
            await nextTick()
            focusInitial()
        } else {
            popOpen()
            document.removeEventListener('keydown', onKeydown)
        }
    },
    { immediate: true }
)

onBeforeUnmount(() => {
    popOpen()
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
            tabindex="-1"
            @submit.prevent="onSubmit"
            @keydown.enter="onEnterKey"
            @input="onFieldEdit"
            @change="onFieldEdit"
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
/* The dialog takes the focus itself when it has no form control; no focus ring around the whole card */
.modal-content:focus {
    outline: none;
}
</style>
