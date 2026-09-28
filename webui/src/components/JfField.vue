<script setup lang="ts">
import { computed, provide, reactive, useId } from 'vue'
import JfIcon from './JfIcon.vue'
import { jfFieldKey, type JfFieldContext } from '@/lib/field-context'

const props = withDefaults(defineProps<{
  label: string
  /** Id of the control inside the default slot; generated when omitted. */
  name?: string
  error?: string
  required?: boolean
  /** Place the label beside the control for dense rows such as switch lists. */
  inline?: boolean
}>(), {
  name: undefined,
  error: undefined,
  required: false,
  inline: false,
})

const generatedId = useId()
const controlId = computed(() => props.name ?? `jf-field-${generatedId}`)
const messageId = computed(() => `${controlId.value}-message`)

// Read through getters so an error that only appears after submit still reaches the
// control's aria-describedby without the page re-rendering the field.
const context: JfFieldContext = reactive({
  get controlId() { return controlId.value },
  get describedBy() { return props.error ? messageId.value : undefined },
  get invalid() { return Boolean(props.error) },
  get required() { return props.required },
})

provide(jfFieldKey, context)
</script>

<template>
  <div class="jf-field" :data-inline="inline || undefined">
    <label :for="controlId">
      <span>{{ label }}</span>
      <span v-if="required" class="jf-sr-only">（必填）</span>
    </label>

    <div class="jf-field-control">
      <slot />
    </div>

    <!-- Keep validation feedback directly associated with its control. -->
    <p v-if="error" :id="messageId" class="jf-field-message" data-error="true">
      <JfIcon name="exclamation-circle" size="sm" aria-hidden="true" />
      <span>{{ error }}</span>
    </p>
  </div>
</template>

<style scoped>
.jf-field {
  display: grid;
  min-width: 0;
}

.jf-field > label {
  display: inline-flex;
  align-items: baseline;
  gap: var(--jf-space-px);
  margin-bottom: var(--jf-space-2);
  font-size: var(--jf-compact-size);
  line-height: var(--jf-compact-leading);
  font-weight: var(--jf-emphasis-weight);
  color: var(--jf-text);
}

.jf-field[data-inline] {
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  column-gap: var(--jf-space-3);
}

.jf-field[data-inline] > label {
  margin-bottom: 0;
}

.jf-field[data-inline] .jf-field-message {
  grid-column: 1 / -1;
}

.jf-field-control {
  min-width: 0;
}

.jf-field-message {
  display: flex;
  align-items: flex-start;
  gap: var(--jf-space-1);
  margin-top: var(--jf-space-1);
  font-size: var(--jf-caption-size);
  line-height: var(--jf-caption-leading);
  color: var(--jf-text-secondary);
  text-wrap: pretty;
}

.jf-field-message[data-error] {
  color: var(--jf-danger-text);
}

.jf-field-message svg {
  flex: none;
  margin-top: 2px;
}

.jf-sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border-width: 0;
}
</style>
