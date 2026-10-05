<script setup lang="ts">
withDefaults(defineProps<{
  title?: string
  /** Comfortable is a standalone object; compact is a dense panel or a nested group. */
  density?: 'comfortable' | 'compact'
  /** Flat drops the elevation so cards never stack shadows inside one another. */
  flat?: boolean
  /** Flush cancels body padding for tables to touch edges cleanly */
  flush?: boolean
}>(), {
  title: undefined,
  density: 'comfortable',
  flat: false,
  flush: false,
})
</script>

<template>
  <section class="jf-card" :data-density="density" :class="{ 'jf-card-flat': flat, 'jf-card-flush': flush }">
    <header v-if="title || $slots.header || $slots.actions" class="jf-card-header">
      <div class="jf-card-heading">
        <slot name="header">
          <h3 class="jf-module-title">{{ title }}</h3>
        </slot>
      </div>
      <div v-if="$slots.actions" class="jf-card-actions">
        <slot name="actions" />
      </div>
    </header>

    <div class="jf-card-body">
      <slot />
    </div>

    <footer v-if="$slots.footer" class="jf-card-footer">
      <slot name="footer" />
    </footer>
  </section>
</template>

<style scoped>
/* Sections are separated by spacing and heading level only — no dividers inside a card. */
.jf-card {
  border-radius: var(--jf-card-radius);
  background: var(--jf-surface);
  box-shadow: var(--jf-shadow-soft);
  padding: var(--jf-card-padding-comfortable);
  min-width: 0;
  overflow: hidden;
}

.jf-card[data-density='compact'] {
  padding: var(--jf-card-padding-compact);
}

.jf-card-flat {
  box-shadow: none;
  border: 1px solid var(--jf-border-subtle);
}

.jf-card-header {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--jf-space-3);
}

.jf-card-heading {
  min-width: 0;
}

.jf-card-actions {
  min-width: 0;
  max-width: 100%;
}

.jf-card-body {
  margin-top: var(--jf-card-section-gap);
  min-width: 0;
}

.jf-card-body:empty {
  margin-top: 0;
}

.jf-card[data-density='compact'] .jf-card-body {
  margin-top: var(--jf-card-section-gap-compact);
}

.jf-card-footer {
  margin-top: var(--jf-card-section-gap);
}

.jf-card[data-density='compact'] .jf-card-footer {
  margin-top: var(--jf-card-section-gap-compact);
}

/* Flush tables inside cards: cancel horizontal and bottom padding so table rows bleed to the edge */
.jf-card-flush .jf-card-body {
  margin-inline: calc(var(--jf-card-padding-comfortable) * -1);
  margin-bottom: calc(var(--jf-card-padding-comfortable) * -1);
}

.jf-card[data-density='compact'].jf-card-flush .jf-card-body {
  margin-inline: calc(var(--jf-card-padding-compact) * -1);
  margin-bottom: calc(var(--jf-card-padding-compact) * -1);
}

.jf-card-flush :deep(.jf-table th:first-child),
.jf-card-flush :deep(.jf-table td:first-child) {
  padding-left: var(--jf-card-padding-comfortable);
}

.jf-card-flush :deep(.jf-table th:last-child),
.jf-card-flush :deep(.jf-table td:last-child) {
  padding-right: var(--jf-card-padding-comfortable);
}

.jf-card[data-density='compact'].jf-card-flush :deep(.jf-table th:first-child),
.jf-card[data-density='compact'].jf-card-flush :deep(.jf-table td:first-child) {
  padding-left: var(--jf-card-padding-compact);
}

.jf-card[data-density='compact'].jf-card-flush :deep(.jf-table th:last-child),
.jf-card[data-density='compact'].jf-card-flush :deep(.jf-table td:last-child) {
  padding-right: var(--jf-card-padding-compact);
}
</style>
