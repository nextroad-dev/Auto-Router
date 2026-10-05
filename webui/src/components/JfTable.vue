<script setup lang="ts" generic="T extends object">
import { computed } from 'vue'
import JfSkeleton from './JfSkeleton.vue'
import { cellAlign, cellValue, rowKeyOf, type JfColumn } from '@/lib/table'

const props = withDefaults(defineProps<{
  columns: JfColumn[]
  rows: T[]
  rowKey?: string | ((row: T) => string | number)
  loading?: boolean
  emptyText?: string
  /** Kept visible while the body scrolls inside a short container. */
  stickyHeader?: boolean
}>(), {
  rowKey: undefined,
  loading: false,
  emptyText: undefined,
  stickyHeader: false,
})

defineSlots<{
  empty?: () => unknown
  [name: `cell-${string}`]: (props: { row: T; value: unknown; index: number }) => unknown
}>()

const skeletonRows = computed(() => props.rows.length || 5)
const showEmpty = computed(() => !props.loading && !props.rows.length)
</script>

<template>
  <div class="jf-table-wrap" :aria-busy="loading ? 'true' : undefined">
    <table class="jf-table" :data-sticky="stickyHeader || undefined">
      <colgroup>
        <col v-for="column in columns" :key="column.key" :style="column.width ? { width: column.width } : undefined">
      </colgroup>
      <thead>
        <tr>
          <th v-for="column in columns" scope="col" :style="{ textAlign: cellAlign(column) }">
            <span>{{ column.title }}</span>
          </th>
        </tr>
      </thead>
      <tbody v-if="loading">
        <tr v-for="row in skeletonRows" :key="`skeleton-${row}`">
          <td v-for="column in columns" :key="column.key">
            <JfSkeleton height="16px" />
          </td>
        </tr>
      </tbody>
      <tbody v-else-if="showEmpty">
        <tr class="jf-table-empty-row">
          <td :colspan="columns.length">
            <slot name="empty">
              <p class="jf-table-empty">{{ emptyText }}</p>
            </slot>
          </td>
        </tr>
      </tbody>
      <tbody v-else>
        <tr v-for="(row, index) in rows" :key="rowKeyOf(row, rowKey, index)">
          <td
            v-for="column in columns"
            :key="column.key"
            :data-nowrap="column.nowrap || undefined"
            :style="{ textAlign: cellAlign(column) }"
          >
            <slot :name="`cell-${column.key}`" :row="row" :value="cellValue(row, column.key)" :index="index">
              <span v-if="cellValue(row, column.key) === undefined || cellValue(row, column.key) === ''" class="jf-table-absent">—</span>
              <template v-else>{{ cellValue(row, column.key) }}</template>
            </slot>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.jf-table-wrap {
  min-width: 0;
  max-width: 100%;
  overflow-x: auto;
}

.jf-table {
  width: 100%;
  min-width: 100%;
  border-collapse: collapse;
  background: transparent;
  font-size: var(--jf-compact-size);
}

.jf-table th {
  border-bottom: 1px solid var(--jf-border-subtle);
  padding: var(--jf-space-2) var(--jf-space-3);
  color: var(--jf-text-secondary);
  font-size: var(--jf-caption-size);
  line-height: var(--jf-caption-leading);
  font-weight: var(--jf-emphasis-weight);
  text-align: left;
  white-space: nowrap;
}

.jf-table[data-sticky] thead th {
  position: sticky;
  top: 0;
  z-index: var(--jf-z-base);
  background: var(--jf-surface);
}

.jf-table td {
  border-bottom: 1px solid var(--jf-border-subtle);
  padding: var(--jf-space-3);
  color: var(--jf-text);
  font-variant-numeric: tabular-nums;
  vertical-align: middle;
  overflow-wrap: anywhere;
}

.jf-table tbody tr:last-child td {
  border-bottom: 0;
}

.jf-table tbody tr {
  transition: background var(--jf-duration-fast) var(--jf-ease);
}

.jf-table tbody tr:hover {
  background: var(--jf-surface-hover);
}

.jf-table td[data-nowrap] {
  white-space: nowrap;
}

.jf-table-empty-row td {
  padding: var(--jf-space-8) var(--jf-space-3);
}

.jf-table-empty {
  color: var(--jf-text-secondary);
  text-align: center;
}

.jf-table-absent {
  color: var(--jf-decorative-muted);
}
</style>
