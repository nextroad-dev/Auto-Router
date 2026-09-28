<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  lines?: number
  width?: string
  height?: string
  shape?: 'bar' | 'block' | 'circle'
}>(), {
  lines: 1,
  width: undefined,
  height: undefined,
  shape: 'bar',
})

const box = computed(() => ({
  width: props.width ?? (props.shape === 'circle' ? 'var(--jf-control-md)' : '100%'),
  height: props.height ?? (props.shape === 'bar' ? 'var(--jf-space-4)' : props.shape === 'circle' ? undefined : 'var(--jf-control-md)'),
}))
</script>

<template>
  <!-- The placeholder is decorative; the loading container carries aria-busy. -->
  <span class="jf-skeleton-wrap" aria-hidden="true">
    <span
      v-for="line in props.lines"
      :key="line"
      class="jf-skeleton"
      :class="`jf-skeleton-${shape}`"
      :style="line === props.lines && props.lines > 1 ? { ...box, width: '62%' } : box"
    />
  </span>
</template>

<style scoped>
.jf-skeleton-wrap {
  display: block;
  min-width: 0;
}

.jf-skeleton {
  display: block;
  background: linear-gradient(
    90deg,
    var(--jf-tonal) 0%,
    var(--jf-tonal-hover) 40%,
    var(--jf-tonal) 80%
  );
  background-size: 200% 100%;
  animation: jf-shimmer var(--jf-period-shimmer) linear infinite;
}

.jf-skeleton + .jf-skeleton {
  margin-top: var(--jf-space-2);
}

.jf-skeleton-bar {
  border-radius: var(--jf-space-1);
}

.jf-skeleton-block {
  border-radius: var(--jf-radius-control);
}

.jf-skeleton-circle {
  aspect-ratio: 1 / 1;
  border-radius: var(--jf-radius-pill);
}
</style>
