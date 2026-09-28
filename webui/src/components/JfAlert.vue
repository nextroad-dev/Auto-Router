<script setup lang="ts">
import { computed } from 'vue'
import JfIcon from './JfIcon.vue'

const props = withDefaults(defineProps<{
  tone?: 'neutral' | 'info' | 'success' | 'warning' | 'danger'
  title?: string
  description?: string
  /** Only urgent feedback should use `alert`; polite status stays out of the way. */
  role?: 'status' | 'alert'
  icon?: string
  hideIcon?: boolean
}>(), {
  tone: 'neutral',
  title: undefined,
  description: undefined,
  role: 'status',
  icon: undefined,
  hideIcon: false,
})

const glyph = computed(() => props.icon ?? ({
  neutral: 'information-circle',
  info: 'information-circle',
  success: 'check-circle',
  warning: 'exclamation-triangle',
  danger: 'exclamation-triangle',
} as const)[props.tone])
</script>

<template>
  <div class="jf-alert" :data-tone="tone" :role="role">
    <JfIcon v-if="!hideIcon" :name="glyph" size="md" class="jf-alert-icon" />
    <div class="jf-alert-content">
      <p v-if="title" class="jf-alert-title">{{ title }}</p>
      <p v-if="description" :class="{ 'jf-alert-description': true, 'mt-1': !!title }">{{ description }}</p>
      <slot />
      <div v-if="$slots.description" class="jf-alert-description" :class="{ 'mt-1': !!title }">
        <slot name="description" />
      </div>
      <div v-if="$slots.actions" class="jf-alert-actions">
        <slot name="actions" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.jf-alert {
  display: flex;
  align-items: flex-start;
  gap: var(--jf-space-3);
  border: 1px solid var(--jf-alert-border);
  border-radius: var(--jf-radius-control);
  background: var(--jf-alert-bg);
  padding: var(--jf-space-3) var(--jf-space-4);
  color: var(--jf-text);
  animation: jf-fade-in var(--jf-duration-normal) var(--jf-ease-out) both;
}

.jf-alert-content {
  flex: 1;
  min-width: 0;
}

.jf-alert-title {
  font-weight: var(--jf-emphasis-weight);
}

.jf-alert-description,
.jf-alert-content :deep(p) {
  color: var(--jf-text-secondary);
  text-wrap: pretty;
}

.jf-alert-actions {
  margin-top: var(--jf-space-3);
  display: flex;
  flex-wrap: wrap;
  gap: var(--jf-space-2);
}

.jf-alert[data-tone='neutral'] {
  --jf-alert-accent: var(--jf-text-secondary);
  --jf-alert-border: var(--jf-border-subtle);
  --jf-alert-bg: var(--jf-tonal);
}

.jf-alert[data-tone='info'] {
  --jf-alert-accent: var(--jf-info-text);
  --jf-alert-border: var(--jf-info-border);
  --jf-alert-bg: var(--jf-info-bg);
}

.jf-alert[data-tone='success'] {
  --jf-alert-accent: var(--jf-success-text);
  --jf-alert-border: var(--jf-success-border);
  --jf-alert-bg: var(--jf-success-bg);
}

.jf-alert[data-tone='warning'] {
  --jf-alert-accent: var(--jf-warning-text);
  --jf-alert-border: var(--jf-warning-border);
  --jf-alert-bg: var(--jf-warning-bg);
}

.jf-alert[data-tone='danger'] {
  --jf-alert-accent: var(--jf-danger-text);
  --jf-alert-border: var(--jf-danger-border);
  --jf-alert-bg: var(--jf-danger-bg);
}

.jf-alert-icon {
  margin-top: 2px;
  flex: none;
  color: var(--jf-alert-accent);
}
</style>
