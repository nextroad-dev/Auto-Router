import { reactive } from 'vue'

export type ConfirmOptions = {
  title: string
  description?: string
  confirmLabel: string
  /** Destructive actions use the danger button and are announced as an alertdialog. */
  danger?: boolean
}

type ConfirmState = ConfirmOptions & { open: boolean }

/** The single confirmation dialog rendered by App.vue; pages ask through confirmAction. */
export const confirmState = reactive<ConfirmState>({ open: false, title: '', confirmLabel: '' })
let settle: ((confirmed: boolean) => void) | undefined

/** Ask the operator to confirm an action. Resolves false when dismissed. */
export function confirmAction(options: ConfirmOptions): Promise<boolean> {
  settle?.(false)
  Object.assign(confirmState, { description: undefined, danger: false, ...options, open: true })
  return new Promise(resolve => { settle = resolve })
}

export function resolveConfirm(confirmed: boolean) {
  confirmState.open = false
  const done = settle
  settle = undefined
  done?.(confirmed)
}
