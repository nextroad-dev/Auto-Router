<script setup lang="ts">
import { computed, inject, onMounted, useTemplateRef, watch } from 'vue'
import { jfFieldKey } from '@/lib/field-context'

const props = withDefaults(defineProps<{
  modelValue?: boolean
  indeterminate?: boolean
  label?: string
  disabled?: boolean
}>(), {
  modelValue: false,
  indeterminate: false,
  label: undefined,
  disabled: false,
})

const emit = defineEmits<{ 'update:modelValue': [boolean] }>()

const field = inject(jfFieldKey)
const boxId = computed(() => field?.controlId)
const inputRef = useTemplateRef<HTMLInputElement>('input')

function syncIndeterminate() {
  if (inputRef.value) {
    inputRef.value.indeterminate = Boolean(props.indeterminate)
  }
}

onMounted(syncIndeterminate)
watch(() => props.indeterminate, syncIndeterminate)

function onChange(event: Event) {
  emit('update:modelValue', (event.target as HTMLInputElement).checked)
}
</script>

<template>
  <label class="jf-checkbox" :data-disabled="disabled || undefined" :for="boxId">
    <input
      ref="input"
      :id="boxId"
      type="checkbox"
      class="jf-checkbox-input"
      :checked="modelValue"
      :disabled="disabled"
      :aria-describedby="field?.describedBy"
      @change="onChange"
    >
    <span class="jf-checkbox-text"><slot>{{ label }}</slot></span>
  </label>
</template>

<style scoped>
.jf-checkbox {
  display: flex;
  align-items: flex-start;
  gap: var(--jf-space-2);
  min-width: 0;
  padding: var(--jf-space-1) 0;
  cursor: pointer;
  user-select: none;
}

/* Jude-Frontweb Checkbox specification:
   Round frame (radius: 50%), solid-filled dot (●) when checked (no checkmark/✓),
   centered short dash when indeterminate. */
.jf-checkbox-input {
  appearance: none;
  -webkit-appearance: none;
  width: 16px;
  height: 16px;
  flex: none;
  margin: 3px 0 0;
  display: inline-grid;
  place-items: center;
  border-radius: 50%;
  border: 1px solid var(--jf-border-control);
  background: var(--jf-surface);
  cursor: inherit;
  transition:
    background-color var(--jf-duration-fast) var(--jf-ease),
    border-color var(--jf-duration-fast) var(--jf-ease),
    box-shadow var(--jf-duration-fast) var(--jf-ease);
}

.jf-checkbox-input:hover:not(:disabled) {
  border-color: var(--jf-border-strong);
}

.jf-checkbox-input:focus-visible {
  outline: var(--jf-border-width-focus) solid var(--jf-border-focus);
  outline-offset: 2px;
}

/* Checked state: Pure solid circle (●) with primary background and border */
.jf-checkbox-input:checked {
  background-color: var(--jf-primary);
  border-color: var(--jf-primary);
}

/* Indeterminate state: Primary background with centered horizontal dash */
.jf-checkbox-input:indeterminate {
  background-color: var(--jf-primary);
  border-color: var(--jf-primary);
}

.jf-checkbox-input:indeterminate::after {
  content: "";
  display: block;
  width: 8px;
  height: 2px;
  border-radius: 1px;
  background-color: var(--jf-text-on-primary);
}

.jf-checkbox-text {
  min-width: 0;
  color: var(--jf-text);
  font-size: var(--jf-compact-size);
  line-height: var(--jf-compact-leading);
  text-wrap: pretty;
}

.jf-checkbox[data-disabled] {
  opacity: var(--jf-opacity-disabled);
  cursor: not-allowed;
}
</style>
