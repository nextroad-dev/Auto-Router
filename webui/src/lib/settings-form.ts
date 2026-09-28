import type { components } from './generated-api'

type SettingField = components['schemas']['SettingField']
export interface TierRow { model?: string; provider?: string; tier: number }

export const retryStatusCodeOptions = [408, 425, 429, 500, 502, 503, 504] as const

/** Validate, deduplicate, and numerically sort selected transient HTTP statuses. */
export function normalizeRetryStatusCodes(values: unknown): number[] | null {
  if (!Array.isArray(values)) return null
  if (values.some(value => typeof value !== 'number' || !Number.isInteger(value) || !retryStatusCodeOptions.includes(value as typeof retryStatusCodeOptions[number]))) return null
  return [...new Set(values as number[])].sort((left, right) => left - right)
}

export const routingPreferenceOptions = [
  { label: '均衡', value: 'balanced' },
  { label: '质量', value: 'quality' },
  { label: '成本', value: 'cost' },
  { label: '延迟', value: 'latency' },
]

export function splitSettingList(text: string) {
  return text.split(/\r?\n/).map(item => item.trim()).filter(Boolean)
}

export function validateUniqueList(text: string, label: string) {
  const entries = splitSettingList(text)
  return new Set(entries).size === entries.length ? '' : `${label} 中存在重复条目。`
}

export function validateTierRows(rows: TierRow[], label: string) {
  const seen = new Set<string>()
  for (const row of rows) {
    const model = row.model?.trim() ?? ''
    const provider = row.provider?.trim() ?? ''
    if ((!model && !provider) || (model && provider)) return `${label}的每一行必须只填写模型或提供商其中之一。`
    if (!Number.isInteger(Number(row.tier)) || Number(row.tier) < 0) return `${label}等级必须是非负整数。`
    const key = model ? `model:${model}` : `provider:${provider}`
    if (seen.has(key)) return `${label}中不能重复声明同一模型或提供商。`
    seen.add(key)
  }
  return ''
}

function setNested(target: Record<string, unknown>, path: string, value: unknown) {
  const segments = path.split('.')
  let current = target
  for (const segment of segments.slice(0, -1)) {
    if (typeof current[segment] !== 'object' || current[segment] === null || Array.isArray(current[segment])) current[segment] = {}
    current = current[segment] as Record<string, unknown>
  }
  current[segments[segments.length - 1]] = value
}

/** Build a nested partial settings patch from only the selected mutable paths. */
export function buildSettingsPatch(fields: SettingField[], paths: string[], values: Record<string, unknown>, overrides: Record<string, unknown> = {}) {
  const byPath = new Map(fields.map(field => [field.path, field]))
  const patch: Record<string, unknown> = {}
  for (const path of paths) {
    if (!byPath.get(path)?.mutable) continue
    const entry = Object.prototype.hasOwnProperty.call(overrides, path) ? overrides[path] : values[path]
    if (entry !== undefined) setNested(patch, path, entry)
  }
  return patch
}

/** An empty Jev key deliberately means "leave the stored credential unchanged". */
export function buildJevPatch(fields: SettingField[], values: Record<string, unknown>, key: string) {
  const paths = ['jev.enabled', 'jev.input_mode', 'jev.base_url', 'jev.model']
  const patch = buildSettingsPatch(fields, paths, values)
  if (key.trim() && fields.some(field => field.path === 'jev.api_key' && field.mutable)) {
    setNested(patch, 'jev.api_key', key.trim())
  }
  return patch
}
