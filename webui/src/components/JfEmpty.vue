<script setup lang="ts">
import { computed } from 'vue'
import JfIcon from './JfIcon.vue'

const props = withDefaults(defineProps<{
  title: string
  /** The action offered must match the reason the data is missing. */
  variant?: 'first-use' | 'filter' | 'no-permission' | 'error' | 'plain'
  icon?: string
}>(), {
  variant: 'plain',
  icon: undefined,
})

const glyph = computed(() => props.icon ?? ({
  'first-use': 'plus',
  'filter': 'magnifying-glass',
  'no-permission': 'lock-closed',
  'error': 'exclamation-triangle',
  'plain': 'information-circle',
} as const)[props.variant])
</script>

<template>
  <div class="jf-empty" :data-variant="variant">
    <span class="jf-empty-icon" aria-hidden="true">
      <JfIcon :name="glyph" size="xl" />
    </span>
    <p class="jf-empty-title">{{ title }}</p>
    <div v-if="$slots.action" class="jf-empty-action">
      <slot name="action" />
    </div>
  </div>
</template>

<style scoped>
/* A dashed frame means "drop files here"; an empty state stays a plain block. */
.jf-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--jf-space-1);
  padding: var(--jf-space-8) var(--jf-space-4);
  text-align: center;
}

.jf-empty-icon {
  display: grid;
  place-items: center;
  width: var(--jf-control-lg);
  height: var(--jf-control-lg);
  margin-bottom: var(--jf-space-2);
  border-radius: var(--jf-radius-pill);
  background: var(--jf-tonal);
  color: var(--jf-text-secondary);
}

.jf-empty-title {
  font-size: var(--jf-compact-size);
  font-weight: var(--jf-emphasis-weight);
  color: var(--jf-text);
}

.jf-empty-action {
  margin-top: var(--jf-space-4);
}
</style>
