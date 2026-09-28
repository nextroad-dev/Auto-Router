import { describe, expect, it } from 'vitest'
import { selectLatencyMetrics } from '../src/lib/dashboard-metrics'

describe('selectLatencyMetrics', () => {
  it('uses automatic-routing samples when available', () => {
    expect(selectLatencyMetrics({
      auto: { count: 2, mean_duration_ms: 12.6, p95_duration_ms: 18 },
      all: { count: 5, mean_duration_ms: 20, p95_duration_ms: 40 },
    })).toEqual({ source: 'auto', sampleCount: 2, meanMs: 13, p95Ms: 18 })
  })

  it('labels overall fallback correctly when there are no automatic samples', () => {
    expect(selectLatencyMetrics({
      auto: { count: 0, mean_duration_ms: null, p95_duration_ms: null },
      all: { count: 3, mean_duration_ms: 0, p95_duration_ms: 4 },
    })).toEqual({ source: 'all', sampleCount: 3, meanMs: 0, p95Ms: 4 })
  })

  it('does not use overall values when automatic samples exist but a metric is absent', () => {
    expect(selectLatencyMetrics({
      auto: { count: 1, mean_duration_ms: null },
      all: { count: 4, mean_duration_ms: 99, p95_duration_ms: 120 },
    })).toEqual({ source: 'auto', sampleCount: 1, meanMs: null, p95Ms: null })
  })

  it('returns no measurements for missing samples and ignores non-finite values', () => {
    expect(selectLatencyMetrics({
      auto: { count: 0, mean_duration_ms: Number.NaN },
      all: { count: 0, mean_duration_ms: Number.POSITIVE_INFINITY },
    })).toEqual({ source: null, sampleCount: 0, meanMs: null, p95Ms: null })
    expect(selectLatencyMetrics(undefined)).toEqual({ source: null, sampleCount: 0, meanMs: null, p95Ms: null })
  })
})
