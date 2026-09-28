import type { components } from './generated-api'
import { i18n, messages } from '../i18n'
import { ApiError } from './api'

export type AdminErrorCode = components['schemas']['AdminErrorCode']
export type InferenceErrorCode = components['schemas']['InferenceErrorCode']

/** One operator-facing failure rendering: a Chinese title plus an actionable description. */
export interface ErrorNotice {
  title: string
  description: string
  code: string
  status: number
}

/**
 * The management vocabulary as a runtime list. `satisfies` keeps it a subset of the
 * generated contract enum, and the type-level assertions below prove that the enum and
 * the copy table are the same set in both directions.
 */
export const ADMIN_ERROR_CODES = [
  'invalid_request',
  'invalid_filter',
  'invalid_cursor',
  'invalid_group',
  'invalid_scopes',
  'invalid_model',
  'invalid_password',
  'invalid_session',
  'invalid_api_key',
  'insufficient_scope',
  'cross_site_request',
  'too_many_attempts',
  'request_too_large',
  'unknown_model',
  'unknown_provider',
  'unknown_pair',
  'not_found',
  'provider_exists',
  'model_exists',
  'pair_exists',
  'provider_not_deletable',
  'provider_not_configured',
  'key_not_found',
  'key_name_exists',
  'credential_limit',
  'password_already_set',
  'password_not_set',
  'settings_conflict',
  'restart_required',
  'unsupported_setting',
  'read_only_state',
  'storage_error',
  'snapshot_publish_failed',
  'sync_failed',
  'sync_unavailable',
  'discovery_failed',
  'metadata_lookup_failed',
  'dashboard_missing',
] as const satisfies readonly AdminErrorCode[]

/** The forwarding-surface vocabulary. It also covers the shared credential and diagnostic paths. */
export const INFERENCE_ERROR_CODES = [
  'invalid_request',
  'invalid_request_error',
  'unsupported_media_type',
  'unsupported_conversion',
  'invalid_routing_preference',
  'request_too_large',
  'provider_not_configured',
  'provider_override_disabled',
  'invalid_model_identifier',
  'model_not_found',
  'provider_not_found',
  'auto_routing_unavailable',
  'routing_unavailable',
  'no_eligible_candidate',
  'truncated_evidence',
  'upstream_unavailable',
  'upstream_timeout',
  'client_closed_request',
  'internal_error',
  'invalid_api_key',
  'insufficient_scope',
  'debug_route_unavailable',
] as const satisfies readonly InferenceErrorCode[]

type ErrorCopyKey = keyof typeof messages['zh-CN']['errors']['codes']

// A regenerated contract that adds a code without copy lands in this type and fails
// `npm run typecheck` instead of silently rendering the generic fallback.
type UncoveredErrorCode = Exclude<AdminErrorCode | InferenceErrorCode, ErrorCopyKey>
// A copy key that the contract no longer declares is dead text, and it is reported too.
type UndeclaredErrorCode = Exclude<ErrorCopyKey, AdminErrorCode | InferenceErrorCode>
// The runtime lists are the iteration surface for the copy tests, so they must cover the
// contract as well: a code that is in the enum but not in the list would escape the tests.
type UnlistedErrorCode = Exclude<AdminErrorCode | InferenceErrorCode, typeof ADMIN_ERROR_CODES[number] | typeof INFERENCE_ERROR_CODES[number]>

const copyCoversContract: UncoveredErrorCode extends never ? true : never = true
const copyHasNoOrphans: UndeclaredErrorCode extends never ? true : never = true
const listsCoverContract: UnlistedErrorCode extends never ? true : never = true
// Referenced so the proofs are not tree-shaken away from a reader's attention.
export const errorCopyIsExhaustive = copyCoversContract && copyHasNoOrphans && listsCoverContract

function t(key: string, named?: Record<string, string | number>): string {
  return named ? i18n.global.t(key, named) : i18n.global.t(key)
}

/**
 * Every code in the OpenAPI vocabulary has operator-facing copy. Front-end synthetic codes do not.
 *
 * `te` is asked about the leaf key rather than the branch: vue-i18n treats a message node with
 * children as a container, so `te('errors.codes.invalid_filter')` is false even though the copy
 * exists. `errors.field` and `errors.httpStatus` are plain strings, which is why the sibling
 * lookups below use the same leaf form.
 */
export function isKnownErrorCode(code: string): boolean {
  if (typeof code !== 'string' || code === '') return false
  return i18n.global.te(`errors.codes.${code}.title`)
}

/** The Chinese title for a code, or the raw code when it is not part of the vocabulary. */
export function errorCodeTitle(code: string): string {
  return isKnownErrorCode(code) ? t(`errors.codes.${code}.title`) : code
}

/** The actionable description for a code, or an empty string when the code is unknown. */
export function errorCodeDescription(code: string): string {
  return isKnownErrorCode(code) ? t(`errors.codes.${code}.description`) : ''
}

/** The localized `HTTP <status>` template, so the detail card has one source for its prefix. */
export function httpStatusLabel(status: number): string {
  return t('errors.httpStatus', { status })
}

function fallbackKey(status: number): 'network' | 'clientUnknown' | 'server' | 'unknown' {
  if (status === 0) return 'network'
  if (status >= 400 && status < 500) return 'clientUnknown'
  if (status >= 500 && status < 600) return 'server'
  return 'unknown'
}

/** Render a raw status/code pair, preferring the contract copy and degrading by HTTP class. */
export function errorNoticeForCode(status: number, code: string, field?: string): ErrorNotice {
  let title: string
  let description: string
  if (isKnownErrorCode(code)) {
    title = errorCodeTitle(code)
    description = errorCodeDescription(code)
  } else {
    const key = `errors.fallback.${fallbackKey(status)}`
    title = t(`${key}.title`)
    description = t(`${key}.description`, { status, code: code || 'request_failed' })
  }
  if (field) description = `${description}（${t('errors.field', { field })}）`
  return { title, description, code, status }
}

/**
 * Normalize any thrown value into a title and description.
 *
 * An `ApiError` is a failure the backend described, so it is rendered from the vocabulary.
 * Any other `Error` is a front-end defect or a transport abort: its own message is the only
 * truthful detail available, so it is kept verbatim as the description.
 *
 * Already-resolved notices pass through unchanged, so a caller can store the result of this
 * function and hand it back later without it being re-derived from a non-Error value.
 */
export function errorNotice(error: unknown): ErrorNotice {
  if (isErrorNotice(error)) return error
  if (error instanceof ApiError) return errorNoticeForCode(error.status, error.code, error.field)
  if (error instanceof Error) {
    return {
      title: errorCodeTitle('internal_error'),
      description: error.message || errorCodeDescription('internal_error'),
      code: 'internal_error',
      status: 0,
    }
  }
  return errorNoticeForCode(0, 'request_failed')
}

function isErrorNotice(value: unknown): value is ErrorNotice {
  return typeof value === 'object' && value !== null
    && typeof (value as ErrorNotice).title === 'string'
    && typeof (value as ErrorNotice).description === 'string'
    && typeof (value as ErrorNotice).code === 'string'
    && typeof (value as ErrorNotice).status === 'number'
}

/**
 * Compatibility shim for call sites that still render a single line.
 *
 * New code should use {@link errorNotice} with the shared `ErrorAlert` component so the
 * description is not discarded.
 */
export function errorMessage(error: unknown): string {
  return errorNotice(error).title
}
