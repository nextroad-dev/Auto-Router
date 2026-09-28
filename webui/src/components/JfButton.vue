<script setup lang="ts">
import JfIcon from './JfIcon.vue'

const props = withDefaults(defineProps<{
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger' | 'danger-ghost'
  size?: 'sm' | 'md' | 'lg'
  icon?: string
  trailingIcon?: string
  loading?: boolean
  disabled?: boolean
  block?: boolean
  /** Icon-only buttons: square shape, and an aria-label is mandatory. */
  square?: boolean
  type?: 'button' | 'submit' | 'reset'
}>(), {
  variant: 'primary',
  size: 'md',
  icon: undefined,
  trailingIcon: undefined,
  loading: false,
  disabled: false,
  block: false,
  square: false,
  type: 'button',
})

const emit = defineEmits<{ click: [MouseEvent] }>()

function onClick(event: MouseEvent) {
  if (props.disabled || props.loading) {
    event.preventDefault()
    event.stopPropagation()
    return
  }
  emit('click', event)
}
</script>

<template>
  <button
    class="jf-button"
    :class="{ 'jf-button-block': block, 'jf-button-square': square }"
    :data-variant="variant"
    :data-size="size"
    :type="type"
    :disabled="disabled || loading"
    :aria-busy="loading ? 'true' : undefined"
    @click="onClick"
  >
    <span v-if="loading" class="jf-button-spinner" aria-hidden="true" />
    <JfIcon v-else-if="icon" :name="icon" :size="size === 'lg' ? 'md' : 'sm'" class="jf-button-icon" />
    <span v-if="$slots.default" class="jf-button-label"><slot /></span>
    <JfIcon v-if="trailingIcon" :name="trailingIcon" :size="size === 'lg' ? 'md' : 'sm'" class="jf-button-icon" />
  </button>
</template>

<style scoped>
.jf-button {
  display: inline-flex;
  min-height: var(--jf-control-md);
  align-items: center;
  justify-content: center;
  gap: var(--jf-space-2);
  border: 0;
  border-radius: var(--jf-radius-control);
  padding: 0 var(--jf-space-4);
  font-size: var(--jf-compact-size);
  line-height: var(--jf-compact-leading);
  font-weight: var(--jf-emphasis-weight);
  white-space: nowrap;
  cursor: pointer;
  transition:
    background var(--jf-duration-fast) var(--jf-ease),
    color var(--jf-duration-fast) var(--jf-ease),
    border-color var(--jf-duration-fast) var(--jf-ease),
    transform var(--jf-duration-instant) var(--jf-ease-in);
}

.jf-button[data-size='sm'] {
  min-height: var(--jf-control-sm);
  padding: 0 var(--jf-space-3);
  gap: var(--jf-space-1);
}

.jf-button[data-size='lg'] {
  min-height: var(--jf-control-lg);
  padding: 0 var(--jf-space-5);
}

.jf-button-square,
.jf-button.jf-button-square,
.jf-button[data-size].jf-button-square {
  padding: 0;
  width: var(--jf-control-md);
  height: var(--jf-control-md);
  min-height: unset;
  flex: none;
}

.jf-button.jf-button-square[data-size='sm'],
.jf-button[data-size='sm'].jf-button-square {
  width: var(--jf-control-sm);
  height: var(--jf-control-sm);
  min-height: unset;
  padding: 0;
}

.jf-button.jf-button-square[data-size='lg'],
.jf-button[data-size='lg'].jf-button-square {
  width: var(--jf-control-lg);
  height: var(--jf-control-lg);
  min-height: unset;
  padding: 0;
}

.jf-button-block {
  width: 100%;
}

.jf-button-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.jf-button[data-variant='primary'] {
  background: var(--jf-primary);
  color: var(--jf-text-on-primary);
}

.jf-button[data-variant='primary']:hover:not(:disabled) {
  background: var(--jf-primary-hover);
}

.jf-button[data-variant='primary']:active:not(:disabled) {
  background: var(--jf-primary-active);
}

.jf-button[data-variant='secondary'] {
  background: var(--jf-tonal);
  color: var(--jf-text);
}

.jf-button[data-variant='secondary']:hover:not(:disabled) {
  background: var(--jf-tonal-hover);
}

.jf-button[data-variant='secondary']:active:not(:disabled) {
  background: var(--jf-tonal-active);
}

.jf-button[data-variant='ghost'] {
  background: transparent;
  color: var(--jf-text);
}

.jf-button[data-variant='ghost']:hover:not(:disabled) {
  background: color-mix(in srgb, var(--jf-text) calc(var(--jf-opacity-ghost-hover) * 100%), transparent);
}

.jf-button[data-variant='ghost']:active:not(:disabled) {
  background: color-mix(in srgb, var(--jf-text) calc(var(--jf-opacity-ghost-active) * 100%), transparent);
}

.jf-button[data-variant='danger'] {
  background: var(--jf-danger);
  color: var(--jf-text-on-danger);
}

.jf-button[data-variant='danger']:hover:not(:disabled) {
  background: var(--jf-danger-hover);
}

.jf-button[data-variant='danger']:active:not(:disabled) {
  background: var(--jf-danger-active);
}

/* Aborting an action is not the same weight as destroying data, so the neutral
   variant carries the danger hue instead of a filled red surface. */
.jf-button[data-variant='danger-ghost'] {
  background: transparent;
  color: var(--jf-danger-text);
}

.jf-button[data-variant='danger-ghost']:hover:not(:disabled) {
  background: var(--jf-danger-bg);
}

.jf-button[data-variant='danger-ghost']:active:not(:disabled) {
  background: var(--jf-danger-border);
}

.jf-button:active:not(:disabled) {
  transform: scale(0.98);
}

.jf-button:disabled {
  opacity: var(--jf-opacity-disabled);
  cursor: not-allowed;
  transform: none;
}

.jf-button-icon {
  flex: none;
}

.jf-button-spinner {
  width: var(--jf-icon-sm);
  height: var(--jf-icon-sm);
  flex: none;
  border-radius: var(--jf-radius-pill);
  border: 2px solid currentColor;
  border-right-color: transparent;
  animation: jf-spin var(--jf-period-spinner) linear infinite;
}

@media (prefers-reduced-motion: reduce) {
  .jf-button-spinner {
    animation: none;
    border-right-color: currentColor;
    opacity: var(--jf-opacity-muted);
  }
}
</style>
