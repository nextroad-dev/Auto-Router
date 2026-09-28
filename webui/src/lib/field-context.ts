import type { InjectionKey } from 'vue'

/**
 * Link between JfField and the control placed in its slot: the field owns the id the
 * label points at, and the control binds it back so the accessible name and the
 * described-by relationship are wired without the page repeating ids.
 */
export interface JfFieldContext {
  controlId: string
  describedBy: string | undefined
  invalid: boolean
  required: boolean
}

export const jfFieldKey: InjectionKey<JfFieldContext> = Symbol('jf-field')
