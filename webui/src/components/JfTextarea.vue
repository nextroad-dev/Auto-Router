<script setup lang="ts">
import { computed, inject, useAttrs } from 'vue'
import { jfFieldKey } from '@/lib/field-context'
import { splitControlAttrs } from '@/lib/control-attrs'

defineOptions({ inheritAttrs: false })

const props = withDefaults(defineProps<{
  modelValue?: string | number | null
  rows?: number
  disabled?: boolean
  invalid?: boolean
}>(), {
  modelValue: '',
  rows: 4,
  disabled: false,
  invalid: false,
})

const emit = defineEmits<{ 'update:modelValue': [string] }>()

const attrs = useAttrs()
const split = computed(() => splitControlAttrs(attrs as Record<string, unknown>))
const field = inject(jfFieldKey)
const invalid = computed(() => props.invalid || Boolean(field?.invalid))
</script>

<template>
  <div class="jf-textarea" :class="split.wrapper.class" :data-invalid="invalid || undefined">
    <textarea
      class="jf-textarea-el"
      :id="field?.controlId"
      :rows="rows"
      :value="modelValue ?? ''"
      :disabled="disabled"
      :required="field?.required || undefined"
      :aria-invalid="invalid ? 'true' : undefined"
      :aria-describedby="field?.describedBy"
      v-bind="split.control"
      @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
    />
  </div>
</template>

<style scoped>
.jf-textarea {
  min-width: 0;
  border: 1px solid var(--jf-border-control);
  border-radius: var(--jf-radius-control);
  background: var(--jf-surface);
  transition: border-color var(--jf-duration-fast) var(--jf-ease);
}

.jf-textarea:hover {
  border-color: var(--jf-border-strong);
}

.jf-textarea[data-invalid] {
  border-color: var(--jf-danger);
}

.jf-textarea:focus-within {
  outline: var(--jf-border-width-focus) solid var(--jf-border-focus);
  outline-offset: 1px;
}

.jf-textarea-el {
  display: block;
  width: 100%;
  min-width: 0;
  border: 0;
  border-radius: inherit;
  background: transparent;
  padding: var(--jf-space-3);
  color: var(--jf-text);
  font-family: inherit;
  line-height: var(--jf-compact-leading);
  resize: vertical;
}

.jf-textarea-el:focus {
  outline: none;
}

.jf-textarea-el::placeholder {
  color: var(--jf-text-secondary);
  opacity: 1;
}

.jf-textarea:has(.jf-textarea-el:disabled) {
  background: var(--jf-tonal);
  opacity: var(--jf-opacity-disabled);
}
</style>
