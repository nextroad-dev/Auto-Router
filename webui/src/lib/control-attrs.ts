import type { ClassValue, StyleValue } from 'vue'

const layoutKeys = new Set(['class', 'style'])

export interface SplitAttrs {
  wrapper: { class?: ClassValue; style?: StyleValue }
  control: Record<string, unknown>
}

/**
 * A control renders a wrapper plus a native element. Layout classes belong on the
 * wrapper (`w-full`, `min-w-0`) while everything semantic — id, aria-*, min/max/step,
 * autocomplete and listeners — must land on the native element to keep working.
 */
export function splitControlAttrs(attrs: Record<string, unknown>): SplitAttrs {
  const wrapper: Record<string, unknown> = {}
  const control: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(attrs)) {
    if (layoutKeys.has(key.toLowerCase())) wrapper[key] = value
    else control[key] = value
  }
  return { wrapper: wrapper as SplitAttrs['wrapper'], control }
}
