import { describe, expect, it } from 'vitest'
import { messages } from '../src/i18n'
import { enumLabel, fallbackReasonLabel, inputModeDescription, jevStatusLabel, logErrorCodeLabel, usageStatusDescription } from '../src/lib/labels'

const labels = messages['zh-CN'].labels

describe('enumLabel', () => {
  it('renders the closed protocol vocabulary', () => {
    expect(enumLabel('protocol', 'chat_completions')).toBe('Chat Completions')
    expect(enumLabel('protocol', 'responses')).toBe('Responses')
    expect(enumLabel('protocol', 'native')).toBe('原生协议')
  })

  it('renders the closed routing vocabularies', () => {
    expect(enumLabel('routingMode', 'auto')).toBe('自动路由')
    expect(enumLabel('routingMode', 'explicit')).toBe('指定模型')
    expect(enumLabel('selectionMode', 'explicit')).toBe('指定模型')
    expect(enumLabel('selectionMode', 'jev')).toBe('旧版：Jev 直接选模型')
    expect(enumLabel('selectionMode', 'blend')).toBe('旧版：混合决策')
    expect(enumLabel('selectionMode', 'default_model')).toBe('旧版：默认模型兜底')
    expect(enumLabel('selectionMode', 'first_eligible')).toBe('组内按配置顺序选模型')
  })

  it('renders the preference, band, observation and class vocabularies', () => {
    expect(enumLabel('confidenceBand', 'high')).toBe('高')
    expect(enumLabel('confidenceBand', 'medium')).toBe('中')
    expect(enumLabel('confidenceBand', 'low')).toBe('低')
    expect(enumLabel('usageStatus', 'observed')).toBe('已提取上游用量')
    expect(enumLabel('usageStatus', 'absent')).toBe('未读取到上游用量')
    expect(enumLabel('usageStatus', 'malformed')).toBe('上游用量字段无效')
    expect(enumLabel('usageStatus', 'oversized')).toBe('用量提取超限')
    expect(enumLabel('usageStatus', 'interrupted')).toBe('响应中断，未读取到用量')
    expect(enumLabel('statusClass', 'success')).toBe('成功（2xx）')
    expect(enumLabel('statusClass', 'client_error')).toBe('客户端错误（4xx）')
    expect(enumLabel('statusClass', 'server_error')).toBe('服务端错误（5xx）')
    expect(enumLabel('inputMode', 'content')).toBe('原文截取摘要')
    expect(enumLabel('inputMode', 'redacted')).toBe('脱敏截取摘要')
    expect(enumLabel('inputMode', 'features_only')).toBe('仅请求结构特征')
  })

  it('covers every declared member of each vocabulary', () => {
    const namespaces = ['protocol', 'routingMode', 'selectionMode', 'confidenceBand', 'usageStatus', 'statusClass', 'inputMode', 'group'] as const
    for (const namespace of namespaces) {
      const table = labels[namespace] as Record<string, string>
      expect(Object.keys(table).length).toBeGreaterThan(0)
      for (const [value, expected] of Object.entries(table)) {
        expect(enumLabel(namespace, value)).toBe(expected)
      }
    }
  })

  it('returns the raw value for a member the vocabulary does not know', () => {    // A log row written by a different build can carry a value this console has no label for.
    expect(enumLabel('selectionMode', 'future_mode')).toBe('future_mode')
    expect(enumLabel('usageStatus', 'unknown')).toBe('unknown')
  })

  it('returns an empty string for a missing value so the caller can render its own dash', () => {
    expect(enumLabel('protocol', null)).toBe('')
    expect(enumLabel('protocol', undefined)).toBe('')
    expect(enumLabel('protocol', '')).toBe('')
  })
})

describe('jevStatusLabel', () => {
  it('renders the literal statuses', () => {
    expect(jevStatusLabel('disabled')).toBe('未启用')
    expect(jevStatusLabel('skipped_single_model')).toBe('仅一个可用任务组，跳过推荐')
    expect(jevStatusLabel('skipped_insufficient_evidence')).toBe('证据不足跳过')
    expect(jevStatusLabel('skipped_too_many_models')).toBe('旧版：候选模型过多，跳过推荐')
    expect(jevStatusLabel('ok')).toBe('Jev 推荐成功')
  })

  it('expands the failure prefix into the localized reason', () => {
    expect(jevStatusLabel('failure:jev_timeout')).toBe('失败（Jev 超时）')
    expect(jevStatusLabel('failure:jev_unavailable')).toBe('失败（Jev 不可达）')
    expect(jevStatusLabel('failure:jev_rejected')).toBe('失败（Jev 拒绝请求）')
    expect(jevStatusLabel('failure:jev_invalid_result')).toBe('失败（Jev 结果无效）')
    expect(jevStatusLabel('failure:jev_canceled')).toBe('失败（Jev 调用取消）')
  })

  it('keeps an unrecognized reason visible instead of dropping it', () => {
    expect(jevStatusLabel('failure:brand_new_reason')).toBe('失败（brand_new_reason）')
  })

  it('returns an empty string for a missing value', () => {
    expect(jevStatusLabel(null)).toBe('')
    expect(jevStatusLabel(undefined)).toBe('')
    expect(jevStatusLabel('')).toBe('')
  })
})

describe('fallbackReasonLabel', () => {
  it('renders the closed reason vocabulary', () => {
    expect(fallbackReasonLabel('none')).toBe('未发生兜底')
    expect(fallbackReasonLabel('not_requested')).toBe('未调用 Jev')
    expect(fallbackReasonLabel('confidence_low')).toBe('选组置信度低于阈值')
    expect(fallbackReasonLabel('selected_model_ineligible')).toBe('旧版：推荐模型不可用')
    expect(fallbackReasonLabel('jev_timeout')).toBe('Jev 超时')
    expect(fallbackReasonLabel('jev_unavailable')).toBe('Jev 不可达')
    expect(fallbackReasonLabel('jev_rejected')).toBe('Jev 拒绝请求')
    expect(fallbackReasonLabel('jev_invalid_result')).toBe('Jev 结果无效')
    expect(fallbackReasonLabel('jev_canceled')).toBe('Jev 调用取消')
    expect(fallbackReasonLabel('truncated_evidence')).toBe('证据被截断')
    expect(fallbackReasonLabel('no_candidates')).toBe('无候选模型')
  })

  it('expands the not_eligible prefix while keeping the exclusion code verbatim', () => {
    expect(fallbackReasonLabel('not_eligible:vision')).toBe('不满足筛选：vision')
    expect(fallbackReasonLabel('not_eligible:requires_tools')).toBe('不满足筛选：requires_tools')
    // The exclusion code is a machine token with no copy table of its own, so it stays raw.
    expect(fallbackReasonLabel('not_eligible:')).toBe('不满足筛选：')
  })

  it('falls back to the raw value for an unknown reason', () => {
    expect(fallbackReasonLabel('future_reason')).toBe('future_reason')
  })

  it('returns an empty string for a missing value', () => {
    expect(fallbackReasonLabel(null)).toBe('')
    expect(fallbackReasonLabel(undefined)).toBe('')
  })
})

describe('operator descriptions', () => {
  it('distinguishes usage extraction bounds from request or context failures', () => {
    expect(usageStatusDescription('oversized')).toContain('不表示 Jev 或上游上下文超限')
    expect(usageStatusDescription('observed')).toContain('真实的 0 显示为 0')
    expect(usageStatusDescription('future_status')).toContain('未知用量状态')
  })

  it('describes local excerpts rather than a full conversation', () => {
    expect(inputModeDescription('content')).toContain('整体摘要预算 16 KiB')
    expect(inputModeDescription('redacted')).toContain('前 512 字节')
    expect(inputModeDescription('features_only')).toContain('不发送用户或系统原文')
    expect(inputModeDescription('future_mode')).toContain('未知输入模式')
  })
})

describe('logErrorCodeLabel', () => {
  it('reuses the error copy title so the two views cannot drift', () => {
    expect(logErrorCodeLabel('no_eligible_candidate')).toBe(messages['zh-CN'].errors.codes.no_eligible_candidate.title)
    expect(logErrorCodeLabel('upstream_timeout')).toBe(messages['zh-CN'].errors.codes.upstream_timeout.title)
  })

  it('keeps a stored code that is no longer in the vocabulary visible', () => {
    expect(logErrorCodeLabel('legacy_code_from_older_build')).toBe('legacy_code_from_older_build')
  })

  it('returns an empty string for a missing value', () => {
    expect(logErrorCodeLabel(null)).toBe('')
    expect(logErrorCodeLabel(undefined)).toBe('')
    expect(logErrorCodeLabel('')).toBe('')
  })
})
