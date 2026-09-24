/** Local types for admin endpoints being introduced alongside the UI.
 * Keep them here until OpenAPI includes them; generated-api.ts stays generated.
 */
export interface DashboardMetric {
  numerator: number
  denominator: number
  value: number | null
}

export interface DashboardCountRow {
  id: string
  name?: string
  requests: number
  input_tokens: number | null
  output_tokens: number | null
  total_tokens: number | null
}

export interface DashboardReport {
  window: { key: string; from: string; to: string }
  success_rate: DashboardMetric
  models: DashboardCountRow[]
  groups: DashboardCountRow[]
  output_tps_60s: number | null
}

export interface GroupModel {
  provider: string
  model: string
}

export interface ModelGroupsDocument {
  simple: GroupModel[]
  medium: GroupModel[]
  complex: GroupModel[]
}

export interface DiscoveredModel {
  id: string
  label?: string
}

export interface DiscoveredModelsDocument {
  items: DiscoveredModel[]
  truncated: boolean
}

export interface SetupStatus {
  password_set: boolean
}

export interface PasswordSession {
  name: string
  expires_at: string
}
