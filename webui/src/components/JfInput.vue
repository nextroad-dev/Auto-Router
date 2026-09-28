<script setup lang="ts">
import { computed, inject, ref, useAttrs, useTemplateRef } from 'vue'
import JfIcon from './JfIcon.vue'
import { jfFieldKey } from '@/lib/field-context'
import { splitControlAttrs } from '@/lib/control-attrs'

defineOptions({ inheritAttrs: false })

const props = withDefaults(defineProps<{
  modelValue?: string | number | null
  type?: 'text' | 'password' | 'number' | 'url' | 'email' | 'search'
  size?: 'sm' | 'md' | 'lg'
  icon?: string
  invalid?: boolean
  disabled?: boolean
}>(), {
  modelValue: '',
  type: 'text',
  size: 'md',
  icon: undefined,
  invalid: false,
  disabled: false,
})

const emit = defineEmits<{ 'update:modelValue': [string] }>()

const attrs = useAttrs()
const split = computed(() => splitControlAttrs(attrs as Record<string, unknown>))
const field = inject(jfFieldKey)
const invalid = computed(() => props.invalid || Boolean(field?.invalid))

const inputElement = useTemplateRef<HTMLInputElement>('input')
defineExpose({ focus: () => inputElement.value?.focus() })

function onInput(event: Event) {
  emit('update:modelValue', (event.target as HTMLInputElement).value)
}
</script>

<template>
  <div class="jf-input" :class="split.wrapper.class" :data-size="size" :data-invalid="invalid || undefined">
    <JfIcon v-if="icon" :name="icon" size="sm" class="jf-input-icon" aria-hidden="true" />
    <input
      ref="input"
      class="jf-input-el"
      :id="field?.controlId"
      :type="type"
      :value="modelValue ?? ''"
      :disabled="disabled"
      :required="field?.required || undefined"
      :aria-invalid="invalid ? 'true' : undefined"
      :aria-describedby="field?.describedBy"
      v-bind="split.control"
      @input="onInput"
    >
  </div>
</template>

<style scoped>
.jf-input {
  display: flex;
  align-items: center;
  gap: var(--jf-space-2);
  min-width: 0;
  min-height: var(--jf-control-md);
  border: 1px solid var(--jf-border-control);
  border-radius: var(--jf-radius-control);
  background: var(--jf-surface);
  padding: 0 var(--jf-space-3);
  transition:
    border-color var(--jf-duration-fast) var(--jf-ease),
    background var(--jf-duration-fast) var(--jf-ease);
}

.jf-input[data-size='sm'] {
  min-height: var(--jf-control-sm);
  font-size: var(--jf-caption-size);
}

.jf-input[data-size='lg'] {
  min-height: var(--jf-control-lg);
  font-size: var(--jf-body-size);
}

.jf-input:hover {
  border-color: var(--jf-border-strong);
}

.jf-input:has(.jf-input-el:focus) {
  border-color: var(--jf-border-focus);
}

.jf-input[data-invalid] {
  border-color: var(--jf-danger);
}

.jf-input[data-invalid]:has(.jf-input-el:focus) {
  border-color: var(--jf-danger-hover);
}

.jf-input:focus-within {
  outline: var(--jf-border-width-focus) solid var(--jf-border-focus);
  outline-offset: 1px;
}

.jf-input-el {
  min-width: 0;
  flex: 1;
  border: 0;
  background: transparent;
  padding: 0;
  color: var(--jf-text);
  font-family: inherit;
}

.jf-input-el:focus {
  outline: none;
}

.jf-input-el::placeholder {
  color: var(--jf-text-secondary);
  opacity: 1;
}

.jf-input:has(.jf-input-el:disabled) {
  background: var(--jf-tonal);
  opacity: var(--jf-opacity-disabled);
  cursor: not-allowed;
}

/* The step buttons add a second alignment surface inside a 40px field. */
.jf-input-el[type='number'] {
  appearance: textfield;
  -moz-appearance: textfield;
}

.jf-input-icon {
  flex: none;
  color: var(--jf-text-secondary);
}
</style>
