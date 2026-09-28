<script setup lang="ts">
import { computed } from 'vue'
import { icons, type IconName } from '@/lib/icons'

const props = withDefaults(defineProps<{
  name: IconName | string
  size?: 'xs' | 'sm' | 'md' | 'lg' | 'xl' | '2xl'
  /** Omit for decorative icons: they are hidden from assistive tech. */
  label?: string
}>(), {
  size: 'md',
  label: undefined,
})

const glyph = computed(() => icons[props.name as IconName])
const box = computed(() => `var(--jf-icon-${props.size})`)
</script>

<template>
  <svg
    v-if="glyph"
    class="jf-icon"
    :style="{ width: box, height: box }"
    :viewBox="glyph.viewBox"
    :role="label ? 'img' : undefined"
    :aria-label="label"
    :aria-hidden="label ? undefined : 'true'"
    focusable="false"
    v-html="glyph.body"
  />
  <span
    v-else
    class="jf-icon-fallback"
    :style="{ width: box, height: box }"
    role="img"
    :aria-label="label || String(name)"
  />
</template>

<style scoped>
/* Size is applied in CSS: `var()` is not a valid value for the SVG width attribute. */
.jf-icon {
  display: inline-block;
  flex: none;
  vertical-align: -0.125em;
}

/* A missing glyph keeps the layout it should have occupied instead of collapsing. */
.jf-icon-fallback {
  display: inline-block;
  flex: none;
  border-radius: var(--jf-radius-check);
  background: var(--jf-tonal-active);
}
</style>
