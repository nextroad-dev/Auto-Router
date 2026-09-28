<script setup lang="ts" generic="T = unknown">
import { computed, inject } from 'vue'
import { jfFieldKey } from '@/lib/field-context'

const props = withDefaults(defineProps<{
  modelValue?: T
  value: T
  label?: string
  name?: string
  disabled?: boolean
}>(), {
  modelValue: undefined,
  label: undefined,
  name: undefined,
  disabled: false,
})

const emit = defineEmits<{ 'update:modelValue': [T] }>()

const field = inject(jfFieldKey)
const radioId = computed(() => field?.controlId)
const isChecked = computed(() => props.modelValue === props.value)

function onChange() {
  emit('update:modelValue', props.value)
}
</script>

<template>
  <label class="jf-radio" :data-disabled="disabled || undefined" :for="radioId">
    <input
      :id="radioId"
      type="radio"
      :name="name"
      class="jf-radio-input"
      :checked="isChecked"
      :value="value"
      :disabled="disabled"
      :aria-describedby="field?.describedBy"
      @change="onChange"
    >
    <span class="jf-radio-text"><slot>{{ label }}</slot></span>
  </label>
</template>

<style scoped>
.jf-radio {
  display: flex;
  align-items: flex-start;
  gap: var(--jf-space-2);
  min-width: 0;
  padding: var(--jf-space-1) 0;
  cursor: pointer;
  user-select: none;
}

/* Jude-Frontweb Radio specification:
   16px circular shape (50%), white surface with border-control,
   checked state: primary background with centered 6px white dot. */
.jf-radio-input {
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
    border-color var(--jf-duration-fast) var(--jf-ease);
}

.jf-radio-input:hover:not(:disabled) {
  border-color: var(--jf-border-strong);
}

.jf-radio-input:focus-visible {
  outline: var(--jf-border-width-focus) solid var(--jf-border-focus);
  outline-offset: 2px;
}

.jf-radio-input:checked {
  background-color: var(--jf-primary);
  border-color: var(--jf-primary);
}

.jf-radio-input:checked::after {
  content: "";
  display: block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background-color: var(--jf-surface);
}

.jf-radio-text {
  min-width: 0;
  color: var(--jf-text);
  font-size: var(--jf-compact-size);
  line-height: var(--jf-compact-leading);
  text-wrap: pretty;
}

.jf-radio[data-disabled] {
  opacity: var(--jf-opacity-disabled);
  cursor: not-allowed;
}
</style>
