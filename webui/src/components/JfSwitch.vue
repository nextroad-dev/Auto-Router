<script setup lang="ts">
import { computed, inject } from 'vue'
import { jfFieldKey } from '@/lib/field-context'

const props = withDefaults(defineProps<{
  modelValue?: boolean
  disabled?: boolean
}>(), {
  modelValue: false,
  disabled: false,
})

const emit = defineEmits<{ 'update:modelValue': [boolean] }>()

const field = inject(jfFieldKey)
const checked = computed(() => Boolean(props.modelValue))
</script>

<template>
  <!-- Immediate setting: the switch reports its state through aria-checked and stays
       readable without colour, since the thumb position carries it. -->
  <button
    type="button"
    role="switch"
    class="jf-switch"
    :data-checked="checked || undefined"
    :disabled="disabled"
    :aria-checked="checked ? 'true' : 'false'"
    :id="field?.controlId"
    :aria-describedby="field?.describedBy"
    @click="emit('update:modelValue', !checked)"
  >
    <span class="jf-switch-thumb" aria-hidden="true" />
  </button>
</template>

<style scoped>
.jf-switch {
  --jf-switch-width: 42px;
  --jf-switch-height: 24px;
  --jf-switch-thumb-size: 18px;
  --jf-switch-travel: 18px;

  display: inline-flex;
  width: var(--jf-switch-width);
  height: var(--jf-switch-height);
  flex: none;
  align-items: center;
  padding: 2px;
  border: 1px solid var(--jf-border-control);
  border-radius: var(--jf-radius-pill);
  background: var(--jf-tonal);
  cursor: pointer;
  box-sizing: border-box;
  transition:
    background-color var(--jf-duration-normal) var(--jf-ease),
    border-color var(--jf-duration-normal) var(--jf-ease);
}

.jf-switch:hover:not(:disabled) {
  border-color: var(--jf-border-strong);
}

.jf-switch:focus-visible {
  outline: var(--jf-border-width-focus) solid var(--jf-border-focus);
  outline-offset: 2px;
}

.jf-switch[data-checked] {
  background-color: var(--jf-primary);
  border-color: var(--jf-primary);
}

.jf-switch-thumb {
  width: var(--jf-switch-thumb-size);
  height: var(--jf-switch-thumb-size);
  border-radius: 50%;
  background-color: #ffffff;
  box-shadow: 0 1px 3px rgb(0 0 0 / 0.2), 0 1px 2px rgb(0 0 0 / 0.12);
  transition:
    transform var(--jf-duration-normal) cubic-bezier(0.2, 0, 0, 1),
    width var(--jf-duration-fast) var(--jf-ease),
    background-color var(--jf-duration-normal) var(--jf-ease);
  flex: none;
}

:root[data-theme='dark'] .jf-switch .jf-switch-thumb {
  background-color: #e5e5dd;
  box-shadow: 0 1px 4px rgb(0 0 0 / 0.4);
}

.jf-switch[data-checked] .jf-switch-thumb {
  transform: translateX(var(--jf-switch-travel));
}

:root[data-theme='dark'] .jf-switch[data-checked] .jf-switch-thumb {
  background-color: #1a1a18;
}

.jf-switch:active:not(:disabled) .jf-switch-thumb {
  width: calc(var(--jf-switch-thumb-size) + 4px);
}

.jf-switch[data-checked]:active:not(:disabled) .jf-switch-thumb {
  transform: translateX(calc(var(--jf-switch-travel) - 4px));
  width: calc(var(--jf-switch-thumb-size) + 4px);
}

.jf-switch:disabled {
  opacity: var(--jf-opacity-disabled);
  cursor: not-allowed;
}
</style>
