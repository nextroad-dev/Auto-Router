<script setup lang="ts">
withDefaults(defineProps<{
  tone?: 'neutral' | 'primary' | 'info' | 'success' | 'warning' | 'danger'
  /** Status dot: the meaning must still be spelled out in `label`. */
  dot?: boolean
  label?: string
}>(), {
  tone: 'neutral',
  dot: false,
  label: undefined,
})
</script>

<template>
  <span class="jf-badge" :data-tone="tone" :aria-label="dot ? label : undefined">
    <span v-if="dot" class="jf-badge-dot" aria-hidden="true" />
    <slot v-else />
  </span>
</template>

<style scoped>
.jf-badge {
  display: inline-flex;
  align-items: center;
  gap: var(--jf-space-1);
  border-radius: var(--jf-radius-pill);
  padding: 1px var(--jf-space-2);
  font-size: var(--jf-caption-size);
  line-height: 1.4;
  font-weight: var(--jf-emphasis-weight);
  white-space: nowrap;
}

.jf-badge-dot {
  width: var(--jf-space-2);
  height: var(--jf-space-2);
  border-radius: var(--jf-radius-pill);
  background: currentColor;
}

.jf-badge[data-tone='neutral'] {
  background: var(--jf-tonal);
  color: var(--jf-text);
}

.jf-badge[data-tone='primary'] {
  background: var(--jf-primary);
  color: var(--jf-text-on-primary);
}

.jf-badge[data-tone='info'] {
  background: var(--jf-info-bg);
  color: var(--jf-info-text);
}

.jf-badge[data-tone='success'] {
  background: var(--jf-success-bg);
  color: var(--jf-success-text);
}

.jf-badge[data-tone='warning'] {
  background: var(--jf-warning-bg);
  color: var(--jf-warning-text);
}

.jf-badge[data-tone='danger'] {
  background: var(--jf-danger-bg);
  color: var(--jf-danger-text);
}
</style>
