<script setup lang="ts">
import { computed, inject } from 'vue'
import { jfFieldKey } from '@/lib/field-context'

const props = withDefaults(defineProps<{
  modelValue?: number
  min?: number
  max?: number
  step?: number
  disabled?: boolean
  formatValue?: (val: number) => string
  format?: (val: number) => string
}>(), {
  modelValue: 0,
  min: 0,
  max: 1,
  step: 0.01,
  disabled: false,
  formatValue: undefined,
  format: undefined,
})

const emit = defineEmits<{ 'update:modelValue': [number]; change: [number] }>()

const field = inject(jfFieldKey)

const numValue = computed(() => Number(props.modelValue ?? props.min))

const percentage = computed(() => {
  const range = props.max - props.min
  if (range <= 0) return 0
  const pct = ((numValue.value - props.min) / range) * 100
  return Math.min(100, Math.max(0, pct))
})

const displayValue = computed(() => {
  const formatter = props.formatValue ?? props.format
  if (formatter) {
    return formatter(numValue.value)
  }
  return String(numValue.value)
})

function onInput(event: Event) {
  const target = event.target as HTMLInputElement
  emit('update:modelValue', Number(target.value))
}

function onChange(event: Event) {
  const target = event.target as HTMLInputElement
  emit('change', Number(target.value))
}
</script>

<template>
  <div class="jf-slider-container">
    <div class="jf-slider-track-wrap">
      <input
        type="range"
        class="jf-slider-input"
        :id="field?.controlId"
        :min="min"
        :max="max"
        :step="step"
        :value="numValue"
        :disabled="disabled"
        :aria-describedby="field?.describedBy"
        :style="{
          background: `linear-gradient(to right, var(--jf-primary) 0%, var(--jf-primary) ${percentage}%, var(--jf-tonal-active) ${percentage}%, var(--jf-tonal-active) 100%)`
        }"
        @input="onInput"
        @change="onChange"
      />
    </div>
    <div class="jf-slider-badge" aria-live="polite">
      {{ displayValue }}
    </div>
  </div>
</template>

<style scoped>
.jf-slider-container {
  display: flex;
  align-items: center;
  gap: var(--jf-space-3);
  width: 100%;
}

.jf-slider-track-wrap {
  position: relative;
  flex: 1;
  display: flex;
  align-items: center;
  height: var(--jf-control-sm);
}

.jf-slider-input {
  -webkit-appearance: none;
  appearance: none;
  width: 100%;
  height: 6px;
  border-radius: var(--jf-radius-pill);
  outline: none;
  margin: 0;
  cursor: pointer;
  transition: opacity var(--jf-duration-fast) var(--jf-ease);
}

.jf-slider-input:disabled {
  opacity: var(--jf-opacity-disabled);
  cursor: not-allowed;
}

.jf-slider-input:focus-visible {
  outline: var(--jf-border-width-focus) solid var(--jf-border-focus);
  outline-offset: 3px;
}

/* Webkit (Chrome / Safari / Edge) */
.jf-slider-input::-webkit-slider-thumb {
  -webkit-appearance: none;
  appearance: none;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: var(--jf-surface);
  border: 2px solid var(--jf-primary);
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.12);
  cursor: pointer;
  transition: transform var(--jf-duration-fast) var(--jf-ease), background-color var(--jf-duration-fast) var(--jf-ease);
}

.jf-slider-input:hover:not(:disabled)::-webkit-slider-thumb {
  transform: scale(1.15);
  background: var(--jf-surface-hover);
}

.jf-slider-input:active:not(:disabled)::-webkit-slider-thumb {
  transform: scale(1.05);
  background: var(--jf-tonal-hover);
}

/* Firefox */
.jf-slider-input::-moz-range-thumb {
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: var(--jf-surface);
  border: 2px solid var(--jf-primary);
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.12);
  cursor: pointer;
  transition: transform var(--jf-duration-fast) var(--jf-ease), background-color var(--jf-duration-fast) var(--jf-ease);
}

.jf-slider-input:hover:not(:disabled)::-moz-range-thumb {
  transform: scale(1.15);
  background: var(--jf-surface-hover);
}

.jf-slider-badge {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 4rem;
  padding: 0 var(--jf-space-2);
  height: var(--jf-control-sm);
  background: var(--jf-tonal);
  border-radius: var(--jf-radius-control);
  font-family: var(--jf-font-mono);
  font-size: 0.8125rem;
  font-weight: 500;
  color: var(--jf-text);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
</style>
