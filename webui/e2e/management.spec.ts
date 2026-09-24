import { expect, test, type Page } from '@playwright/test'

const preferenceValues = {
  'routing.allow_provider_override': false,
  'routing.default_preference': 'balanced',
  'routing.analyzer_debug_endpoint': false,
  'routing.policy_debug_endpoint': false,
  'routing.auto.failover.enabled': true,
  'routing.policy.high_confidence': 0.7,
  'routing.policy.low_confidence': 0.3,
  'routing.policy.refuse_truncated_evidence': true,
  'routing.policy.default_model': '',
  'routing.policy.cost_tiers': [],
  'routing.policy.latency_tiers': [],
  'routing.policy.allow_models': [],
  'routing.policy.deny_models': [],
  'routing.policy.allow_providers': [],
  'routing.policy.deny_providers': [],
  'routing.policy.allow_pairs': [],
  'routing.policy.deny_pairs': [],
  'routing.log.enabled': true,
  'routing.log.store_client_ip': false,
  'routing.log.retention_days': 30,
  'routing.log.jev_trace.enabled': false,
  'routing.log.jev_trace.retention_days': 7,
  'jev.enabled': false,
  'jev.input_mode': 'redacted',
  'jev.base_url': '',
  'jev.api_key': null,
  'jev.model': '',
  'admin.session_ttl': '12h',
  'registry.sync.include': [],
} as const

function settingsReport() {
  return {
    version: 0,
    overlay: false,
    settings: Object.entries(preferenceValues).map(([path, value]) => ({
      path,
      value,
      source: 'default',
      mutable: true,
      restart_required: false,
      ...(path === 'jev.api_key' ? { secret: true, set: false } : {}),
    })),
    effective: {},
    warnings: [],
  }
}

function logEvent() {
  return {
    id: 1,
    request_id: 'req-abcdef',
    started_at: '2026-09-24T00:00:00Z',
    duration_ms: 120,
    protocol: 'chat_completions',
    routing_mode: 'auto',
    selection_mode: 'blend',
    requested_model: 'auto',
    effective_model: 'gpt-small',
    provider: 'openai',
    upstream_model: 'gpt-4o-mini',
    status: 503,
    upstream_status: null,
    error_code: 'no_eligible_candidate',
    stream: false,
    bytes_written: 0,
    gateway_attempts: 1,
    failover_used: false,
    usage_status: 'absent',
    routing_preference: 'balanced',
    preference_source: 'default',
    jev_status: 'failure:jev_timeout',
    confidence_band: 'low',
    fallback_reason: 'not_eligible:vision',
    attempts: [{
      id: 1, attempt_index: 1, group_name: 'simple', provider: 'openai', model_id: 'gpt-small',
      started_at: '2026-09-24T00:00:00Z', status: 503, error_code: 'upstream_unavailable', usage_status: 'absent',
    }],
  }
}

async function installApiMocks(page: Page) {
  let deleteSettingsCount = 0
  let syncCount = 0
  let patchCount = 0
  let failNextSync = false
  let conflictNextPatch = false
  let didSync = false
  let requestedSummaryWindow = ''
  let patchBody: unknown
  let failNextProviders = false
  let failNextProvidersUnknown = false
  await page.route('**/admin/v1/**', async route => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const json = (body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
    if (path === '/admin/v1/session' && request.method() === 'GET') return json({ authenticated: true, name: 'admin', scopes: ['admin'], expires_at: '2030-01-01T00:00:00Z' })
    if (path === '/admin/v1/settings' && request.method() === 'GET') return json(settingsReport())
    if (path === '/admin/v1/settings' && request.method() === 'PATCH') {
      patchCount++
      patchBody = request.postDataJSON()
      if (conflictNextPatch) {
        conflictNextPatch = false
        return json({ error: { code: 'settings_conflict', message: 'settings changed' } }, 409)
      }
      return json({ ...settingsReport(), version: 1, changed: ['routing.default_preference'] })
    }
    if (path === '/admin/v1/settings' && request.method() === 'DELETE') {
      deleteSettingsCount++
      return json({ ...settingsReport(), version: 0 })
    }
    if (path === '/admin/v1/models' || path === '/admin/v1/pairs') return json({ items: [], next_cursor: null })
    if (path === '/admin/v1/providers') {
      if (failNextProviders) {
        failNextProviders = false
        return json({ error: { code: 'unknown_provider', message: 'the requested provider is not registered' } }, 404)
      }
      if (failNextProvidersUnknown) {
        failNextProvidersUnknown = false
        return json({ error: { code: 'weird_code', message: 'an error the console has never seen' } }, 500)
      }
      return json({ items: [], next_cursor: null })
    }
    if (path === '/admin/v1/sync-state' && request.method() === 'GET') return didSync
      ? json({ synchronized: true, url: 'https://models.dev/api.json', fetched_at: '2026-09-24T00:00:00Z', allowlist_digest: 'test-digest', imported_pairs: 3, skipped_pairs: 1, warnings: [] })
      : json({ synchronized: false })
    if (path === '/admin/v1/sync' && request.method() === 'POST') {
      syncCount++
      if (failNextSync) {
        failNextSync = false
        return json({ error: { code: 'sync_failed', message: 'models.dev synchronization failed; the registry was not changed' } }, 502)
      }
      didSync = true
      return json({ synchronized: true, url: 'https://models.dev/api.json', fetched_at: '2026-09-24T00:00:00Z', imported_pairs: 3, skipped_pairs: 1, warnings: [] })
    }
    if (path === '/admin/v1/dashboard') return json({ window: { key: '24h', from: '', to: '' }, success_rate: { numerator: 9, denominator: 10, value: 0.9 }, models: [], groups: [], output_tps_60s: 3.5 })
    if (path === '/admin/v1/health') return json({
      status: 'ok', uptime_ms: 7200000, started_at: '2026-09-24T00:00:00Z',
      catalog: { generation: 4, providers: { total: 2, enabled: 1 }, models: { total: 3, enabled: 2 }, pairs: { total: 4, enabled: 2 } },
      routing_log: { enabled: true, queue_depth: 1, queue_capacity: 100, written: 30, dropped: 0, failed: 0 },
      storage: 'ready', settings: { version: 0, overlay: false }, sessions: { live: 1, ttl_ms: 43200000, bound: 128 },
    })
    if (path === '/admin/v1/logs/summary') {
      requestedSummaryWindow = url.searchParams.get('window') ?? '24h'
      return json({
      window: { key: requestedSummaryWindow, from: '', to: '' },
      requests: { total: 10, auto: 6, explicit: 4 },
      rates: [
        { name: 'client_request_success_rate', numerator: 9, denominator: 10, value: 0.9, numerator_label: 'successful requests', denominator_label: 'requests' },
        { name: 'auto_usage_rate', numerator: 6, denominator: 10, value: 0.6, numerator_label: 'automatic requests', denominator_label: 'requests' },
        { name: 'auto_decision_success_rate', numerator: 5, denominator: 6, value: 5 / 6, numerator_label: 'decisions', denominator_label: 'automatic requests' },
        { name: 'jev_invocation_rate', numerator: 4, denominator: 6, value: 2 / 3, numerator_label: 'successful Jev calls', denominator_label: 'automatic requests' },
        { name: 'jev_top1_adoption_rate', numerator: 2, denominator: 3, value: 2 / 3, numerator_label: 'top-1 adoption', denominator_label: 'recommendations' },
      ],
      latency: { all: { count: 10, mean_duration_ms: 300, p95_duration_ms: 500 }, auto: { count: 6, mean_duration_ms: 250, p95_duration_ms: 400 } },
      tokens: { input_tokens: 1000, output_tokens: 500, total_tokens: 1500, observed_usage_count: 10, input_observed_count: 10, output_observed_count: 10, total_observed_count: 10 },
      attempt_output_tokens_per_second_60s: 3.5, statuses: { success: 9, client_error: 0, server_error: 1 }, models: [], recent: [], log_disabled: false,
      })
    }
    if (path === '/admin/v1/logs') return json({ items: [logEvent()], next_cursor: null })
    return json({ error: { code: 'not_found', message: 'unexpected mocked endpoint' } }, 404)
  })
  return {
    get deleteSettingsCount() { return deleteSettingsCount },
    get syncCount() { return syncCount },
    get patchCount() { return patchCount },
    get patchBody() { return patchBody },
    get requestedSummaryWindow() { return requestedSummaryWindow },
    failSync() { failNextSync = true },
    failProviders() { failNextProviders = true },
    failProvidersWithUnknownCode() { failNextProvidersUnknown = true },
    conflictPatch() { conflictNextPatch = true },
  }
}

test('settings only patches its area and confirms global reset', async ({ page }) => {
  const mock = await installApiMocks(page)
  await page.goto('/admin/settings')
  await expect(page.getByText('设置来源与生效值')).toBeVisible()
  await expect(page.getByText('routing.auto.failover.enabled', { exact: true })).toBeVisible()

  const selects = page.getByRole('combobox')
  await selects.first().click()
  await page.getByRole('option', { name: /成本/ }).click()
  await page.getByRole('button', { name: '保存路由行为' }).click()
  await expect.poll(() => mock.patchBody).toEqual({ routing: { default_preference: 'cost', allow_provider_override: false, auto: { failover: { enabled: true } } } })

  await page.getByRole('button', { name: '重置所有运行时覆盖' }).click()
  await expect(page.getByRole('alertdialog')).toBeVisible()
  await page.getByRole('button', { name: '取消' }).click()
  expect(mock.deleteSettingsCount).toBe(0)
  await page.getByRole('button', { name: '重置所有运行时覆盖' }).click()
  await page.getByRole('button', { name: '确认重置' }).click()
  await expect.poll(() => mock.deleteSettingsCount).toBe(1)
})

test('settings conflict requires explicit confirmation before retry', async ({ page }) => {
  const mock = await installApiMocks(page)
  await page.goto('/admin/settings')
  await expect(page.getByText('设置来源与生效值')).toBeVisible()
  mock.conflictPatch()
  await page.getByRole('combobox').first().click()
  await page.getByRole('option', { name: /成本/ }).click()
  await page.getByRole('button', { name: '保存路由行为' }).click()
  await expect(page.getByText(/页面已重新读取最新值/)).toBeVisible()
  expect(mock.patchCount).toBe(1)
  await page.getByRole('button', { name: '确认并重试路由行为' }).click()
  await expect.poll(() => mock.patchCount).toBe(2)
})

test('registry sync reports success and failure without inventing a successful state', async ({ page }) => {
  const mock = await installApiMocks(page)
  await page.goto('/admin/models')
  await page.getByRole('button', { name: '立即同步 models.dev' }).click()
  await expect(page.getByText(/同步成功：导入 3 个模型绑定/)).toBeVisible()
  await expect(page.getByText(/导入绑定：3 · 跳过：1/)).toBeVisible()
  expect(mock.syncCount).toBe(1)

  mock.failSync()
  await page.getByRole('button', { name: '立即同步 models.dev' }).click()
  // The title is the existing sentence an operator already relies on; the description is the
  // new actionable half that this change adds.
  await expect(page.getByText('同步失败，现有配置未被修改。')).toBeVisible()
  await expect(page.getByText(/请稍后重试/)).toBeVisible()
  expect(mock.syncCount).toBe(2)
})

test('a known management error renders Chinese title and advice while an unknown code degrades by status', async ({ page }) => {
  const mock = await installApiMocks(page)

  mock.failProviders()
  await page.goto('/admin/providers')
  await expect(page.getByText('找不到指定提供商')).toBeVisible()
  await expect(page.getByText(/刷新列表确认对象是否仍然存在/)).toBeVisible()
  // The raw machine code stays out of the operator-facing alert.
  await expect(page.getByText('unknown_provider')).toHaveCount(0)

  mock.failProvidersWithUnknownCode()
  await page.getByRole('button', { name: '刷新' }).click()
  await expect(page.getByText('服务暂时不可用')).toBeVisible()
  await expect(page.getByText(/weird_code/)).toBeVisible()
})

test('the request log renders localized error codes and enum labels beside the raw tokens', async ({ page }) => {
  await installApiMocks(page)
  await page.goto('/admin/logs')

  // The column leads with the Chinese title and keeps the machine code available.
  await expect(page.getByText('没有可用的候选模型')).toBeVisible()
  await expect(page.getByText('no_eligible_candidate')).toBeVisible()

  // Filter options come from the same label tables as the cells, so the two places a
  // translated enum member appears cannot drift.
  await page.getByRole('combobox', { name: '协议筛选' }).click()
  await expect(page.getByRole('option', { name: '原生协议', exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await page.getByRole('combobox', { name: 'HTTP 状态筛选' }).click()
  await expect(page.getByRole('option', { name: '成功（2xx）', exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await page.getByRole('combobox', { name: '路由模式筛选' }).click()
  await expect(page.getByRole('option', { name: '自动路由', exact: true })).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('button', { name: '查看' }).click()

  // The detail card carries the actionable description rather than repeating the code.
  await expect(page.getByText(/HTTP 503 · 刷新列表确认对象是否仍然存在/)).toBeVisible()
  // Enum fields render as labels, including the two prefixed vocabularies.
  await expect(page.getByText('混合决策')).toBeVisible()
  await expect(page.getByText('失败（Jev 超时）')).toBeVisible()
  await expect(page.getByText('不满足筛选：vision')).toBeVisible()
  await expect(page.getByText('上游未上报').first()).toBeVisible()
  // The upstream attempt keeps its own label and raw token.
  await expect(page.getByText('上游提供商无法访问')).toBeVisible()
  await expect(page.getByText('upstream_unavailable')).toBeVisible()
})

test('dashboard separates health from operational routing summary', async ({ page }) => {
  const mock = await installApiMocks(page)
  await page.goto('/admin/')
  await expect(page.getByRole('heading', { name: '服务健康' })).toBeVisible()
  await expect(page.getByText('数据库：可用')).toBeVisible()
  await expect(page.getByRole('heading', { name: '路由与 Jev 汇总' })).toBeVisible()
  await expect(page.getByText('自动 6 · 指定 4')).toBeVisible()
  await expect(page.getByText('Jev Top-1 采纳比例')).toBeVisible()
  await page.getByRole('combobox').first().click()
  await page.getByRole('option', { name: /最近 7 天/ }).click()
  await expect.poll(() => mock.requestedSummaryWindow).toBe('7d')
})
