export interface LatencyGroup {
  count: number
  mean_duration_ms?: number | null
  p95_duration_ms?: number | null
}

export interface LatencyMetrics {
  source: 'auto' | 'all' | null
  sampleCount: number
  meanMs: number | null
  p95Ms: number | null
}

function roundedFinite(value: number | null | undefined): number | null {
  return value !== null && value !== undefined && Number.isFinite(value) ? Math.round(value) : null
}

/**
 * Prefer the automatic-routing cohort when it has observations; otherwise show the
 * overall cohort explicitly. Never label overall measurements as automatic metrics.
 */
export function selectLatencyMetrics(latency: { auto: LatencyGroup; all: LatencyGroup } | null | undefined): LatencyMetrics {
  const selected = latency?.auto.count
    ? { source: 'auto' as const, group: latency.auto }
    : latency?.all.count
      ? { source: 'all' as const, group: latency.all }
      : null

  return {
    source: selected?.source ?? null,
    sampleCount: selected?.group.count ?? 0,
    meanMs: roundedFinite(selected?.group.mean_duration_ms),
    p95Ms: roundedFinite(selected?.group.p95_duration_ms),
  }
}
