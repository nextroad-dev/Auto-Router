import { describe, expect, it } from 'vitest'
import type { components } from '../src/lib/generated-api'
import {
  buildJevPatch,
  buildSettingsPatch,
  normalizeRetryStatusCodes,
  retryStatusCodeOptions,
  splitSettingList,
  validateUniqueList,
} from '../src/lib/settings-form'

type SettingField = components['schemas']['SettingField']
const fields: SettingField[] = [
  { path: 'routing.auto.default_group', value: 'medium', source: 'default', mutable: true, restart_required: false },
  { path: 'routing.policy.low_confidence', value: 0.45, source: 'default', mutable: true, restart_required: false },
  { path: 'jev.enabled', value: false, source: 'default', mutable: true, restart_required: false },
  { path: 'jev.input_mode', value: 'redacted', source: 'default', mutable: true, restart_required: false },
  { path: 'jev.base_url', value: '', source: 'default', mutable: true, restart_required: false },
  { path: 'jev.model', value: '', source: 'default', mutable: true, restart_required: false },
  { path: 'jev.api_key', value: null, source: 'default', mutable: true, restart_required: false, secret: true, set: false },
  { path: 'admin.session_ttl', value: '12h', source: 'default', mutable: false, restart_required: true, reason: 'restart required' },
]

describe('settings form helpers', () => {
  it('normalizes the retry-status multi-select to unique ascending integers', () => {
    expect(retryStatusCodeOptions).toEqual([408, 425, 429, 500, 502, 503, 504])
    expect(normalizeRetryStatusCodes([503, 408, 503, 429])).toEqual([408, 429, 503])
    expect(normalizeRetryStatusCodes([])).toEqual([])
    expect(normalizeRetryStatusCodes([418])).toBeNull()
    expect(normalizeRetryStatusCodes([429.5])).toBeNull()
    expect(normalizeRetryStatusCodes(['429'])).toBeNull()
    expect(normalizeRetryStatusCodes(null)).toBeNull()
  })

  it('creates nested partial patches for selected mutable fields only', () => {
    expect(buildSettingsPatch(fields, ['routing.auto.default_group', 'routing.policy.low_confidence'], {
      'routing.auto.default_group': 'simple',
      'routing.policy.low_confidence': 0.6,
      'admin.session_ttl': '24h',
    })).toEqual({ routing: { auto: { default_group: 'simple' }, policy: { low_confidence: 0.6 } } })
    expect(buildSettingsPatch(fields, ['routing.auto.default_group'], { 'routing.auto.default_group': 'complex' }))
      .toEqual({ routing: { auto: { default_group: 'complex' } } })
  })

  it('omits a blank Jev credential so it is never replaced by an empty value', () => {
    const values = { 'jev.enabled': true, 'jev.input_mode': 'features_only', 'jev.base_url': 'https://jev.example', 'jev.model': 'latest' }
    expect(buildJevPatch(fields, values, '   ')).toEqual({ jev: {
      enabled: true, input_mode: 'features_only', base_url: 'https://jev.example', model: 'latest',
    } })
    expect(buildJevPatch(fields, values, ' new-secret ')).toEqual({ jev: {
      enabled: true, input_mode: 'features_only', base_url: 'https://jev.example', model: 'latest', api_key: 'new-secret',
    } })
  })

  it('parses one allow/deny entry per line and validates duplicate rows', () => {
    expect(splitSettingList(' alpha\n\nbeta \r\n')).toEqual(['alpha', 'beta'])
    expect(validateUniqueList('alpha\nbeta', 'allow-models')).toBe('')
    expect(validateUniqueList('alpha\n alpha ', 'allow-models')).toContain('重复')
  })
})
