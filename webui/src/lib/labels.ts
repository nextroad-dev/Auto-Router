import { i18n } from '../i18n'
import { errorCodeTitle } from './errors'

/** A closed enum vocabulary rendered for display. Unknown members fall back to the raw value. */
export type LabelNamespace = 'protocol' | 'routingMode' | 'selectionMode'
  | 'confidenceBand' | 'usageStatus' | 'statusClass' | 'inputMode'
  | 'jevStatus' | 'fallbackReason' | 'group'

function t(key: string, named?: Record<string, string | number>): string {
  return named ? i18n.global.t(key, named) : i18n.global.t(key)
}

function hasKey(key: string): boolean {
  return i18n.global.te(key)
}

/** A vocabulary member is a leaf string, so the message key is exactly `labels.<ns>.<value>`. */
function labelKey(namespace: LabelNamespace, value: string): string {
  return `labels.${namespace}.${value}`
}

/**
 * Render one member of a closed enum vocabulary.
 *
 * The raw value is returned unchanged when the vocabulary does not contain it, because a
 * stored log row can carry a value written by a different build: showing the raw token is
 * more useful than an empty cell.
 */
export function enumLabel(namespace: LabelNamespace, value: string | null | undefined): string {
  if (value === null || value === undefined || value === '') return ''
  const key = labelKey(namespace, value)
  return hasKey(key) ? t(key) : value
}

/** Operator guidance is shared by settings and request details. */
export function inputModeDescription(value: string): string {
  const key = `routingInputDescriptions.${value}`
  return hasKey(key) ? t(key) : '未知输入模式，请核对管理台与服务端版本。'
}

export function usageStatusDescription(value: string): string {
  const key = `usageDescriptions.${value}`
  return hasKey(key) ? t(key) : '未知用量状态，请核对运行版本和结构化日志。'
}

/** Jev status is the closed set plus the `failure:<reason>` form. */
export function jevStatusLabel(value: string | null | undefined): string {
  if (value === null || value === undefined || value === '') return ''
  const failurePrefix = 'failure:'
  if (value.startsWith(failurePrefix)) {
    const reason = value.slice(failurePrefix.length)
    return t('labels.jevStatusFailure', { reason: fallbackReasonLabel(reason) })
  }
  return enumLabel('jevStatus', value)
}

/** Fallback reason is the closed set plus the `not_eligible:<code>` form. */
export function fallbackReasonLabel(value: string | null | undefined): string {
  if (value === null || value === undefined || value === '') return ''
  const notEligiblePrefix = 'not_eligible:'
  if (value.startsWith(notEligiblePrefix)) {
    return t('labels.notEligible', { code: value.slice(notEligiblePrefix.length) })
  }
  return enumLabel('fallbackReason', value)
}

/**
 * The error-code label shown in the request log.
 *
 * A code that is part of the contract vocabulary renders as its title; an unknown value
 * renders as the raw code so an operator can still grep for it in the database.
 */
export function logErrorCodeLabel(value: string | null | undefined): string {
  if (value === null || value === undefined || value === '') return ''
  return errorCodeTitle(value)
}
