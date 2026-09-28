export interface JfColumn {
  /** Property read from the row when the page does not provide a `cell-<key>` slot. */
  key: string
  title: string
  align?: 'start' | 'center' | 'end'
  /** Fixed track sizing; leave unset to let the content decide. */
  width?: string
  /** Identifiers and timestamps should not break mid-token. */
  nowrap?: boolean
}

export function cellAlign(column: JfColumn) {
  return column.align === 'end' ? 'right' : column.align === 'center' ? 'center' : 'left'
}

export type JfRowKey = string | number

/**
 * `key` names a property; a function computes the key, e.g. from a composite id.
 * Rows are typed as plain objects because an interface has no implicit index
 * signature, and pages should not need a cast to hand one to the table.
 */
export function rowKeyOf<T extends object>(
  row: T,
  key: string | ((row: T) => JfRowKey) | undefined,
  index: number,
): JfRowKey {
  if (typeof key === 'function') return key(row)
  const named = key ? (row as Record<string, unknown>)[key] : undefined
  return named === undefined ? index : String(named)
}

/** Cell value for a column key, for the rare slot that renders generically. */
export function cellValue<T extends object>(row: T, key: string): unknown {
  return (row as Record<string, unknown>)[key]
}
