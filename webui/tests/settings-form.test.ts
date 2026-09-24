import { describe, expect, it } from 'vitest'
import type { components } from '../src/lib/generated-api'
import {
  buildJevPatch,
  buildSettingsPatch,
  routingPreferenceOptions,
  splitSettingList,
  validateTierRows,
  validateUniqueList,
} from '../src/lib/settings-form'

type SettingField = components['schemas']['SettingField']
const fields: SettingField[] = [
  { path: 'routing.default_preference', value: 'balanced', source: 'default', mutable: true, restart_required: false },
  { path: 'routing.policy.default_model', value: '', source: 'default', mutable: true, restart_required: false },
  { path: 'jev.enabled', value: false, source: 'default', mutable: true, restart_required: false },
  { path: 'jev.input_mode', value: 'redacted', source: 'default', mutable: true, restart_required: false },
  { path: 'jev.base_url', value: '', source: 'default', mutable: true, restart_required: false },
  { path: 'jev.model', value: '', source: 'default', mutable: true, restart_required: false },
  { path: 'jev.api_key', value: null, source: 'default', mutable: true, restart_required: false, secret: true, set: false },
  { path: 'admin.session_ttl', value: '12h', source: 'default', mutable: false, restart_required: true, reason: 'restart required' },
]

describe('settings form helpers', () => {
  it('exposes all four supported global preference defaults', () => {
    expect(routingPreferenceOptions.map(option => option.value)).toEqual(['balanced', 'quality', 'cost', 'latency'])
  })

  it('creates nested partial patches for selected mutable fields only', () => {
    expect(buildSettingsPatch(fields, ['routing.default_preference', 'routing.policy.default_model'], {
      'routing.default_preference': 'cost',
      'routing.policy.default_model': 'small-model',
      'admin.session_ttl': '24h',
    })).toEqual({ routing: { default_preference: 'cost', policy: { default_model: 'small-model' } } })
    expect(buildSettingsPatch(fields, ['routing.default_preference'], { 'routing.default_preference': 'latency' }))
      .toEqual({ routing: { default_preference: 'latency' } })
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

  it('validates tier target exclusivity, non-negative integer ranks, and duplicates', () => {
    expect(validateTierRows([{ model: 'gpt-a', tier: 0 }, { provider: 'provider-b', tier: 2 }], '成本等级')).toBe('')
    expect(validateTierRows([{ model: 'gpt-a', provider: 'provider-b', tier: 0 }], '成本等级')).toContain('只填写')
    expect(validateTierRows([{ model: 'gpt-a', tier: -1 }], '成本等级')).toContain('非负整数')
    expect(validateTierRows([{ model: 'gpt-a', tier: 0 }, { model: 'gpt-a', tier: 2 }], '成本等级')).toContain('重复')
  })
})
