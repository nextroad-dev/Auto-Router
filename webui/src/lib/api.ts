import type { components, operations } from './generated-api'

export type Schemas = components['schemas']
export type Page<T> = { items: T[]; next_cursor: string | null }
export type Session = Schemas['SessionProbe']
export type Rate = Schemas['MetricRate']
export type LogEvent = Schemas['LogEvent']
export type Model = Schemas['Model']
export type Provider = Schemas['Provider']
export type Pair = Schemas['Pair']
export type SettingField = Schemas['SettingField']
export type SettingsDocument = Schemas['SettingsReport']
export type AdminHealth = Schemas['AdminHealth']
export type LogSummary = Schemas['LogSummary']
export type LogStatsGroup = Schemas['LogStatsGroup']
export type InboundKey = Schemas['InboundKey']
export type OneTimeKey = Schemas['OneTimeKey']
export type CreateInboundKeyRequest = operations['createAdminKey']['requestBody']['content']['application/json']

export function createInboundKeyRequest(name: string): CreateInboundKeyRequest {
  return { name: name.trim(), scopes: ['inference'] }
}

export interface ApiFailure {
  code: string
  message: string
  field?: string
}

export class ApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string, readonly field?: string) {
    super(message)
    this.name = 'ApiError'
  }
}

let unauthorized: (() => void) | undefined
export function setUnauthorizedHandler(handler: () => void) { unauthorized = handler }

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body !== undefined && init.body !== null) headers.set('Content-Type', 'application/json')
  let response: Response
  try {
    response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    throw new ApiError(0, 'network_error', '无法连接管理服务，请检查服务状态。')
  }
  const raw = await response.text()
  let payload: unknown = null
  if (raw) {
    try { payload = JSON.parse(raw) as unknown } catch { payload = null }
  }
  if (!response.ok) {
    const detail = typeof payload === 'object' && payload !== null && 'error' in payload
      ? (payload as { error?: ApiFailure }).error : undefined
    const code = detail?.code ?? 'request_failed'
    const message = detail?.message ?? `请求失败（HTTP ${response.status}）。`
    if (response.status === 401 && !(path === '/admin/v1/session' && init.method === 'POST')) unauthorized?.()
    throw new ApiError(response.status, code, message, detail?.field)
  }
  return payload as T
}

export const api = {
  get: <T>(path: string, signal?: AbortSignal) => request<T>(path, { method: 'GET', signal }),
  post: <T>(path: string, body: unknown) => request<T>(path, { method: 'POST', body: JSON.stringify(body) }),
  postEmpty: <T>(path: string) => request<T>(path, { method: 'POST' }),
  put: <T>(path: string, body: unknown) => request<T>(path, { method: 'PUT', body: JSON.stringify(body) }),
  patch: <T>(path: string, body: unknown) => request<T>(path, { method: 'PATCH', body: JSON.stringify(body) }),
  delete: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
}

export async function getAllPages<T>(path: string, baseParams: Record<string, string | number | boolean | null | undefined> = {}) {
  const result: T[] = []
  let cursor: string | null = null
  do {
    const page: Page<T> = await api.get<Page<T>>(pageURL(path, { ...baseParams, limit: 200, cursor }))
    result.push(...page.items)
    cursor = page.next_cursor
  } while (cursor)
  return result
}

export function pageURL(path: string, params: Record<string, string | number | boolean | null | undefined>) {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== null && value !== undefined && String(value) !== '') query.set(key, String(value))
  }
  const suffix = query.toString()
  return suffix ? `${path}?${suffix}` : path
}

export function formatRate(value: number | null | undefined) {
  return value === null || value === undefined || !Number.isFinite(value) ? '暂无数据' : `${(value * 100).toFixed(2)}%`
}
