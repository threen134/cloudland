<script setup lang="ts">
// One "label — value" row of a detail page info card.
//
// Every detail page used to have its own version, with four label classes (label / info-label /
// detail-label / form-label) and different sizes, colors and layouts; this unifies them.
//
// An empty value shows '-', no need to write `|| '-'` everywhere.
// Label in a fixed-width column, value right next to it and left-aligned. It used to be
// label far left / value far right, which on a wide card left 400-500px to read across.
defineProps<{
    label: string
    /** 值用等宽字体显示（UUID、IP、MAC 这类） */
    mono?: boolean
}>()
</script>

<template>
    <div class="info-row">
        <span class="info-label">
            <slot name="label">{{ label }}</slot>
        </span>
        <span class="info-value" :class="{ mono }">
            <slot>-</slot>
        </span>
    </div>
</template>

<style scoped>
.info-row {
    display: grid;
    grid-template-columns: 144px minmax(0, 1fr);
    align-items: center;
    column-gap: var(--spacing-4);
    padding: var(--spacing-2) 0;
    min-height: 32px;
    border-bottom: 1px solid var(--border-light);
}

/* Detail cards are half the page wide; below 1280px give the value the room instead */
@media (max-width: 1279px) {
    .info-row {
        grid-template-columns: 112px minmax(0, 1fr);
    }
}

.info-row:last-child {
    border-bottom: none;
}

.info-label {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    flex-shrink: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
}

.info-value {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-2);
    min-width: 0;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    font-weight: var(--font-weight-medium);
    word-break: break-all;
}

.info-value.mono {
    font-family: var(--font-family-mono);
}
</style>
