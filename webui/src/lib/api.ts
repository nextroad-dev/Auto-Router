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

const errorMessages: Record<string, string> = {
  network_error: '无法连接管理服务，请检查服务状态。',
  invalid_password: '管理员密码不正确。',
  insufficient_scope: '此凭据没有执行该操作的权限。',
  invalid_request: '请求内容无效，请检查表单后重试。',
  invalid_filter: '筛选条件无效，请调整筛选项。',
  invalid_cursor: '分页位置已失效，请重新加载列表。',
  unknown_model: '找不到指定的模型。',
  unknown_provider: '找不到指定的提供商。',
  unknown_pair: '找不到指定的路由绑定。',
  provider_exists: '此提供商标识已存在。',
  key_not_found: '找不到此凭据。',
  key_name_exists: '此凭据名称已存在。',
  credential_limit: '已达到凭据数量上限。',
  request_too_large: '提交内容过大。',
  settings_conflict: '设置已被其他操作修改，请刷新后重试。',
  restart_required: '此设置需要重启服务才能生效。',
  unsupported_setting: '此设置不支持在线修改。',
  read_only_state: '当前设置为只读状态。',
  storage_error: '存储操作失败，请稍后重试。',
  snapshot_publish_failed: '设置已保存，但服务暂时无法发布最新配置。',
  empty_allowlist: '同步范围为空，请先配置允许列表。',
  sync_failed: '同步失败，现有配置未被修改。',
  sync_unavailable: '同步服务当前不可用。',
  too_many_attempts: '尝试次数过多，请稍后重试。',
  cross_site_request: '请求来源未通过安全校验，请刷新页面后重试。',
}

export function errorMessage(error: unknown) {
  if (error instanceof ApiError) {
    const message = errorMessages[error.code] ?? (error.status === 0 ? '无法连接管理服务，请检查服务状态。' : '操作未能完成，请检查输入或稍后重试。')
    return error.field ? `${message}（字段：${error.field}）` : message
  }
  return error instanceof Error ? error.message : '发生未知错误。'
}

export function formatRate(value: number | null | undefined) {
  return value === null || value === undefined || !Number.isFinite(value) ? '暂无数据' : `${(value * 100).toFixed(2)}%`
}
