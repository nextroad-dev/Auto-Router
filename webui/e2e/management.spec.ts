import { expect, test, type Page } from '@playwright/test'

const preferenceValues = {
  'routing.allow_provider_override': false,
  'routing.analyzer_debug_endpoint': false,
  'routing.policy_debug_endpoint': false,
  'routing.auto.failover.enabled': true,
  'routing.auto.failover.retry_on.status_codes': [],
  'routing.auto.default_group': 'medium',
  'routing.policy.low_confidence': 0.45,
  'routing.policy.refuse_truncated_evidence': true,
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
    warnings: ['测试用系统提醒不应显示在设置页'],
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
  const settingsPatchBodies: unknown[] = []
  const providerPatchRequests: Array<{ path: string; body: unknown }> = []
  const registryPatchRequests: Array<{ path: string; body: unknown }> = []
  const groupPutBodies: unknown[] = []
  let modelRows: Array<Record<string, unknown>> = []
  let groupDocument = { simple: [], medium: [], complex: [] }
  let failNextProviders = false
  let failNextProvidersUnknown = false
  let failNextProviderDiscovery = false
  let failNextSetupStatus = false
  let providerPagination = false
  let overallLatencyOnly = false
  let providerDetailPairs: Array<{ model: string; upstream_model_id: string; enabled: boolean }> = []
  let pairRows: Array<Record<string, unknown>> = []
  let providerRows: Array<Record<string, unknown>> = []
  let failNextGroupPut = false
  const providerRequests: Array<{ cursor: string | null; enabled: string | null }> = []
  const providerModelSelections: Array<{ path: string; body: unknown }> = []
  const pairDeleteRequests: string[] = []
  await page.route('**/admin/v1/**', async route => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const json = (body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
    if (path === '/admin/v1/session' && request.method() === 'GET') return json({ authenticated: true, name: 'admin', scopes: ['admin'], expires_at: '2030-01-01T00:00:00Z' })
    if (path === '/admin/v1/setup/status' && request.method() === 'GET') {
      if (failNextSetupStatus) {
        failNextSetupStatus = false
        return json({ error: { code: 'request_failed', message: 'temporary setup probe failure' } }, 503)
      }
      return json({ password_set: true })
    }
    if (path === '/admin/v1/settings' && request.method() === 'GET') return json(settingsReport())
    if (path === '/admin/v1/settings' && request.method() === 'PATCH') {
      patchCount++
      patchBody = request.postDataJSON()
      settingsPatchBodies.push(patchBody)
      if (conflictNextPatch) {
        conflictNextPatch = false
        return json({ error: { code: 'settings_conflict', message: 'settings changed' } }, 409)
      }
      return json({ ...settingsReport(), version: 1, changed: ['routing.auto.default_group'] })
    }
    if (path === '/admin/v1/settings' && request.method() === 'DELETE') {
      deleteSettingsCount++
      return json({ ...settingsReport(), version: 0 })
    }
    if (path === '/admin/v1/models' && request.method() === 'GET') return json({ items: modelRows, next_cursor: null })
    if (path.startsWith('/admin/v1/models/') && request.method() === 'PATCH') {
      const id = decodeURIComponent(path.slice('/admin/v1/models/'.length))
      const body = request.postDataJSON() as Record<string, unknown>
      registryPatchRequests.push({ path, body })
      modelRows = modelRows.map(row => row.id === id ? { ...row, ...body } : row)
      return json(modelRows.find(row => row.id === id) ?? { id, ...body })
    }
    if (path === '/admin/v1/pairs' && request.method() === 'GET') return json({ items: pairRows, next_cursor: null })
    if (path.startsWith('/admin/v1/pairs/') && request.method() === 'PATCH') {
      const pathParts = path.split('/')
      const provider = decodeURIComponent(pathParts[4] ?? '')
      const model = decodeURIComponent(pathParts.slice(5).join('/'))
      const body = request.postDataJSON() as Record<string, unknown>
      registryPatchRequests.push({ path, body })
      pairRows = pairRows.map(row => row.provider === provider && row.model === model ? { ...row, ...body } : row)
      return json(pairRows.find(row => row.provider === provider && row.model === model) ?? { provider, model, ...body })
    }
    if (path === '/admin/v1/groups' && request.method() === 'GET') return json(groupDocument)
    if (path === '/admin/v1/groups' && request.method() === 'PUT') {
      const body = request.postDataJSON()
      groupPutBodies.push(body)
      if (failNextGroupPut) {
        failNextGroupPut = false
        return json({ error: { code: 'invalid_group', message: 'groups must contain distinct enabled provider/model pairs, at most eight per group', field: 'groups' } }, 400)
      }
      groupDocument = body as typeof groupDocument
      return json(groupDocument)
    }
    if (path.startsWith('/admin/v1/pairs/') && request.method() === 'DELETE') {
      pairDeleteRequests.push(path)
      const pathParts = path.split('/')
      const provider = decodeURIComponent(pathParts[4] ?? '')
      const model = decodeURIComponent(pathParts.slice(5).join('/'))
      pairRows = pairRows.filter(row => row.provider !== provider || row.model !== model)
      providerDetailPairs = providerDetailPairs.filter(pair => provider !== 'provider-a' || pair.model !== model)
      return json({ provider, model, deleted: true })
    }
    if (path === '/admin/v1/providers/provider-a/discover' && request.method() === 'GET') {
      if (failNextProviderDiscovery) {
        failNextProviderDiscovery = false
        return json({ error: { code: 'request_failed', message: 'model discovery unavailable' } }, 502)
      }
      return json({ items: [{ id: 'gpt-4o', label: 'GPT-4o' }], truncated: false })
    }
    if (path === '/admin/v1/providers/provider-a' && request.method() === 'GET') {
      return json({
        provider: {
          key: 'provider-a', display_name: 'First Provider', kind: 'openai_compatible',
          base_url: 'https://a.example/v1', enabled: true,
        },
        pairs: providerDetailPairs,
      })
    }
    if (path.startsWith('/admin/v1/providers/') && request.method() === 'PATCH') {
      const body = request.postDataJSON()
      providerPatchRequests.push({ path, body })
      return json({ provider: { key: 'provider-a', ...body } })
    }
    if (path === '/admin/v1/providers/provider-a/models' && request.method() === 'POST') {
      providerModelSelections.push({ path, body: request.postDataJSON() })
      return json({ provider: 'provider-a', model: 'gpt-4o', metadata_source: 'local-default', metadata_applied: false, metadata_match: '', metadata_model: '' })
    }
    if (path === '/admin/v1/providers') {
      providerRequests.push({ cursor: url.searchParams.get('cursor'), enabled: url.searchParams.get('enabled') })
      if (failNextProviders) {
        failNextProviders = false
        return json({ error: { code: 'unknown_provider', message: 'the requested provider is not registered' } }, 404)
      }
      if (failNextProvidersUnknown) {
        failNextProvidersUnknown = false
        return json({ error: { code: 'weird_code', message: 'an error the console has never seen' } }, 500)
      }
      if (providerPagination) {
        const first = { key: 'provider-a', kind: 'openai_compatible', display_name: 'First Provider', enabled: true, priority: 0, source: 'local', owner: 'admin', gateway_provider: null, base_url: 'https://a.example/v1', api_key_set: true, pair_count: 0 }
        const second = { ...first, key: 'provider-b', display_name: 'Second Provider' }
        return url.searchParams.get('cursor') === 'page-2'
          ? json({ items: [second], next_cursor: null })
          : json({ items: [first], next_cursor: 'page-2' })
      }
      return json({ items: providerRows, next_cursor: null })
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
        { name: 'jev_group_adoption_rate', numerator: 2, denominator: 3, value: 2 / 3, numerator_label: 'automatic requests routed to the Jev-recommended group', denominator_label: 'automatic requests whose Jev call succeeded' },
      ],
      latency: overallLatencyOnly
        ? { all: { count: 2, mean_duration_ms: 0, p95_duration_ms: 0 }, auto: { count: 0, mean_duration_ms: null, p95_duration_ms: null } }
        : { all: { count: 10, mean_duration_ms: 300, p95_duration_ms: 500 }, auto: { count: 6, mean_duration_ms: 250, p95_duration_ms: 400 } },
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
    get settingsPatchBodies() { return settingsPatchBodies },
    get providerPatchRequests() { return providerPatchRequests },
    get registryPatchRequests() { return registryPatchRequests },
    get groupPutBodies() { return groupPutBodies },
    get requestedSummaryWindow() { return requestedSummaryWindow },
    get providerRequests() { return providerRequests },
    get providerModelSelections() { return providerModelSelections },
    get pairDeleteRequests() { return pairDeleteRequests },
    setProviderDetailPairs(pairs: Array<{ model: string; upstream_model_id: string; enabled: boolean }>) { providerDetailPairs = pairs },
    setPairRows(rows: Array<Record<string, unknown>>) { pairRows = rows },
    setModelRows(rows: Array<Record<string, unknown>>) { modelRows = rows },
    setProviderRows(rows: Array<Record<string, unknown>>) { providerRows = rows },
    setGroupDocument(document: { simple: unknown[]; medium: unknown[]; complex: unknown[] }) { groupDocument = document as typeof groupDocument },
    failGroupPutOnce() { failNextGroupPut = true },
    enableProviderPagination() { providerPagination = true },
    failProviderDiscoveryOnce() { failNextProviderDiscovery = true },
    useOverallLatencyWithoutSamples() { overallLatencyOnly = true },
    failSetupStatusOnce() { failNextSetupStatus = true },
    failSync() { failNextSync = true },
    failProviders() { failNextProviders = true },
    failProvidersWithUnknownCode() { failNextProvidersUnknown = true },
    conflictPatch() { conflictNextPatch = true },
  }
}

test('settings auto-save, status multi-select, shared toast and global reset', async ({ page }) => {
  const mock = await installApiMocks(page)
  await page.goto('/admin/settings')
  await expect(page.getByRole('heading', { name: '系统设置' })).toBeVisible()
  await expect(page.getByRole('heading', { name: '系统生效配置全景' })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: '管理会话与 models.dev 同步范围' })).toHaveCount(0)
  await expect(page.getByText('管理员会话有效时长')).toHaveCount(0)
  await expect(page.getByText('models.dev 同步范围覆盖')).toHaveCount(0)
  await expect(page.getByText('Jev 不可用或低置信度时的默认组')).toBeVisible()
  for (const label of [
    '全局默认路由偏好',
    '高置信度阈值',
    '低置信度默认兜底模型',
    '允许调用的模型白名单',
    '禁止调用的模型黑名单',
    '允许的提供商白名单',
    '禁止的提供商黑名单',
    '允许的提供商/模型对',
    '禁止的提供商/模型对',
  ]) {
    await expect(page.getByText(label, { exact: true })).toHaveCount(0)
  }
  await expect(page.getByText('每行填写一个对象标识，留空表示不限制')).toHaveCount(0)
  await expect(page.getByText('测试用系统提醒不应显示在设置页')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /保存/ })).toHaveCount(0)

  await page.getByRole('combobox').first().click()
  await page.getByRole('option', { name: '复杂' }).click()
  await expect.poll(() => mock.patchCount).toBe(1)
  expect(mock.patchBody).toEqual({ routing: { auto: { default_group: 'complex' } } })
  await expect(page.getByText('更改已保存', { exact: true })).toBeVisible()

  await page.getByRole('checkbox', { name: '429' }).check()
  await expect.poll(() => mock.patchCount).toBe(2)
  expect(mock.patchBody).toEqual({ routing: { auto: { failover: { retry_on: { status_codes: [429] } } } } })

  await page.getByRole('button', { name: '重置所有运行时覆盖' }).click()
  await expect(page.getByRole('alertdialog')).toBeVisible()
  await page.getByRole('button', { name: '取消' }).click()
  expect(mock.deleteSettingsCount).toBe(0)
  await page.getByRole('button', { name: '重置所有运行时覆盖' }).click()
  await page.getByRole('button', { name: '确认重置' }).click()
  await expect.poll(() => mock.deleteSettingsCount).toBe(1)
})

test('settings conflicts reload and rebase once without manual retry', async ({ page }) => {
  const mock = await installApiMocks(page)
  await page.goto('/admin/settings')
  await expect(page.getByRole('heading', { name: '系统设置' })).toBeVisible()
  mock.conflictPatch()
  await page.getByRole('combobox').first().click()
  await page.getByRole('option', { name: '复杂' }).click()
  await expect.poll(() => mock.patchCount).toBe(2)
  expect(mock.settingsPatchBodies).toEqual([
    { routing: { auto: { default_group: 'complex' } } },
    { routing: { auto: { default_group: 'complex' } } },
  ])
  await expect(page.getByText('更改已保存', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: /确认并重试/ })).toHaveCount(0)
})

test('registry sync reports success and failure without inventing a successful state', async ({ page }) => {
  const mock = await installApiMocks(page)
  await page.goto('/admin/models')
  await page.getByRole('button', { name: '立即同步 models.dev' }).click()
  await expect(page.getByText(/同步成功：导入 3 个模型绑定/)).toBeVisible()
  await expect(page.getByText(/导入绑定数：3 · 跳过项：1/)).toBeVisible()
  expect(mock.syncCount).toBe(1)

  mock.failSync()
  await page.getByRole('button', { name: '立即同步 models.dev' }).click()
  // The title is the existing sentence an operator already relies on; the description is the
  // new actionable half that this change adds.
  await expect(page.getByText('同步失败，现有配置未被修改。')).toBeVisible()
  await expect(page.getByText(/请稍后重试/)).toBeVisible()
  expect(mock.syncCount).toBe(2)

  await page.getByRole('button', { name: '立即同步 models.dev' }).click()
  await expect(page.getByText(/同步成功：导入 3 个模型绑定/)).toBeVisible()
  expect(mock.syncCount).toBe(3)
})

test('login setup status failure offers a working retry action', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.failSetupStatusOnce()
  await page.goto('/admin/login')

  const retry = page.getByRole('button', { name: '重试读取状态' })
  await expect(retry).toBeVisible()
  await retry.click()
  const password = page.getByLabel('管理员密码')
  await expect(password).toBeVisible()
  await expect(password).toHaveAttribute('required', '')
  const requiredText = page.locator('label').filter({ hasText: '管理员密码' }).locator('.jf-sr-only')
  await expect(requiredText).toHaveText('（必填）')
  await expect(requiredText).toHaveCSS('clip-path', 'inset(50%)')
  await expect(page.locator('label').filter({ hasText: '管理员密码' })).not.toContainText('*')
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
  await expect(page.getByRole('textbox', { name: '搜索请求 ID、模型或提供商' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: '按错误码筛选' })).toBeVisible()

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
  await page.getByRole('combobox', { name: '路由模式' }).click()
  await expect(page.getByRole('option', { name: '自动路由', exact: true })).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('button', { name: '查看明细' }).click()

  // The detail panel uses contract-backed fields and keeps the complete event available as JSON.
  await expect(page.getByText('openai · gpt-small')).toBeVisible()
  await expect(page.getByText('混合决策')).toBeVisible()
  await expect(page.getByText(/"fallback_reason": "not_eligible:vision"/)).toBeVisible()
  await expect(page.getByText(/"error_code": "upstream_unavailable"/)).toBeVisible()
})

test('sidebar account details are replaced by a logout button', async ({ page }) => {
  await installApiMocks(page)
  await page.goto('/admin/')

  const sidebar = page.locator('.dashboard-sidebar')
  await expect(sidebar.getByRole('button', { name: '退出登录' })).toBeVisible()
  await expect(sidebar.getByText('admin', { exact: true })).toHaveCount(0)
  await sidebar.getByRole('button', { name: '退出登录' }).click()
  await expect(page.getByRole('heading', { name: '登录 Auto Router' })).toBeVisible()
})

test('mobile navigation is a labeled modal and returns focus on Escape', async ({ page }) => {
  await installApiMocks(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/admin/')

  const trigger = page.getByRole('button', { name: '打开导航菜单' })
  await trigger.click()
  const navigationDialog = page.getByRole('dialog', { name: '导航菜单' })
  await expect(navigationDialog).toBeVisible()
  await expect(trigger).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByRole('navigation', { name: '工作区' })).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(navigationDialog).toHaveCount(0)
  await expect(trigger).toHaveAttribute('aria-expanded', 'false')
  await expect(trigger).toBeFocused()
})

test('provider pagination appends rows and resets when a filter changes', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.enableProviderPagination()
  await page.goto('/admin/providers')

  await expect(page.getByText('First Provider', { exact: true })).toBeVisible()
  await expect(page.getByText('Second Provider', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '加载更多提供商' }).click()
  await expect(page.getByText('Second Provider', { exact: true })).toBeVisible()
  expect(mock.providerRequests.at(-1)).toEqual({ cursor: 'page-2', enabled: null })

  await page.getByRole('combobox', { name: '提供商状态筛选' }).click()
  await page.getByRole('option', { name: '已禁用', exact: true }).click()
  await expect(page.getByText('Second Provider', { exact: true })).toHaveCount(0)
  await expect.poll(() => mock.providerRequests.at(-1)).toEqual({ cursor: null, enabled: 'false' })

  await page.getByRole('button', { name: /模型绑定 · 0/ }).click()
  await expect(page.getByRole('textbox', { name: '输入上游模型 ID' })).toBeVisible()
})

test('provider edits autosave and do not expose redundant status or save controls', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.enableProviderPagination()
  await page.goto('/admin/providers')
  await page.getByRole('button', { name: '编辑', exact: true }).click()

  const displayName = page.getByRole('textbox', { name: '显示名称' })
  await expect(displayName).toBeVisible()
  await displayName.fill('Renamed Provider')
  await expect.poll(() => mock.providerPatchRequests).toEqual([{
    path: '/admin/v1/providers/provider-a', body: { display_name: 'Renamed Provider' },
  }])
  await expect(page.getByText('更改已保存', { exact: true })).toBeVisible()
  await expect(page.getByRole('switch', { name: '启用该提供商' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '保存更改' })).toHaveCount(0)
})

test('provider model selection uses the documented endpoint and request body', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.enableProviderPagination()
  await page.goto('/admin/providers')
  await page.getByRole('button', { name: /模型绑定 · 0/ }).click()

  const modelInput = page.getByRole('textbox', { name: '输入上游模型 ID' })
  await modelInput.fill('gpt-4o')
  await modelInput.press('Enter')
  await expect(page.getByText(/models.dev 中未找到唯一匹配/)).toBeVisible()
  await expect.poll(() => mock.providerModelSelections).toEqual([
    { path: '/admin/v1/providers/provider-a/models', body: { model: 'gpt-4o' } },
  ])
})

test('provider drawer permanently unbinds a configured model', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.enableProviderPagination()
  mock.setProviderDetailPairs([{ model: 'gpt-4o', upstream_model_id: 'gpt-4o', enabled: true }])
  page.on('dialog', dialog => dialog.accept())
  await page.goto('/admin/providers')
  await page.getByRole('button', { name: /模型绑定 · 0/ }).click()

  await expect(page.getByRole('heading', { name: '当前模型绑定' })).toBeVisible()
  await page.getByRole('button', { name: '解除绑定 gpt-4o' }).click()
  await expect(page.getByText('已永久解除绑定：gpt-4o')).toBeVisible()
  await expect(page.getByRole('heading', { name: '当前模型绑定' })).toHaveCount(0)
  await expect.poll(() => mock.pairDeleteRequests).toEqual(['/admin/v1/pairs/provider-a/gpt-4o'])
})

test('provider discovery recognises a disabled binding instead of offering to bind it again', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.enableProviderPagination()
  mock.setProviderDetailPairs([{ model: 'openai/gpt-4o', upstream_model_id: 'gpt-4o', enabled: false }])
  await page.goto('/admin/providers')
  await page.getByRole('button', { name: /模型绑定 · 0/ }).click()

  await expect(page.getByText('已绑定 · 已停用')).toBeVisible()
  await expect(page.getByRole('button', { name: '绑定', exact: true })).toHaveCount(1)
  await page.getByRole('button', { name: '重新启用' }).click()
  await expect.poll(() => mock.registryPatchRequests).toEqual([{
    path: '/admin/v1/pairs/provider-a/openai%2Fgpt-4o', body: { enabled: true },
  }])
  await expect(page.getByText('已绑定', { exact: true })).toBeVisible()
})

test('provider bindings remain removable when model discovery fails', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.enableProviderPagination()
  mock.setProviderDetailPairs([{ model: 'gpt-4o', upstream_model_id: 'gpt-4o', enabled: true }])
  mock.failProviderDiscoveryOnce()
  page.on('dialog', dialog => dialog.accept())
  await page.goto('/admin/providers')
  await page.getByRole('button', { name: /模型绑定 · 0/ }).click()

  await expect(page.getByRole('heading', { name: '当前模型绑定' })).toBeVisible()
  await page.getByRole('button', { name: '解除绑定 gpt-4o' }).click()
  await expect(page.getByText('已永久解除绑定：gpt-4o')).toBeVisible()
  await expect.poll(() => mock.pairDeleteRequests).toEqual(['/admin/v1/pairs/provider-a/gpt-4o'])
})

test('logical model edits autosave after text input stops', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.setModelRows([{
    id: 'model-a', display_name: 'Model A', enabled: true,
    pair_count: 1, source: 'local', owner: 'admin',
  }])
  await page.goto('/admin/models')
  await page.getByRole('button', { name: '编辑', exact: true }).click()

  await page.getByRole('textbox', { name: '显示名称' }).fill('Renamed Model')
  await expect.poll(() => mock.registryPatchRequests).toEqual([{
    path: '/admin/v1/models/model-a', body: { display_name: 'Renamed Model' },
  }])
  await expect(page.getByText('更改已保存', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '保存更改' })).toHaveCount(0)
})

test('model binding capacity inputs align in one desktop row', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.setPairRows([{
    provider: 'provider-a', model: 'model-a', upstream_model_id: 'upstream-a',
    context_window: 32768, max_output: 4096, supports_tools: false, supports_vision: false,
    supports_audio_input: false, supports_reasoning: false, enabled: true,
    source: 'local', owner: 'admin',
  }])
  await page.goto('/admin/models')
  await page.getByRole('button', { name: '编辑能力' }).click()

  const contextWindow = page.getByRole('spinbutton', { name: '上下文窗口（Tokens）' })
  const maxOutput = page.getByRole('spinbutton', { name: '最大输出限制（Tokens）' })
  const contextBox = await contextWindow.boundingBox()
  const outputBox = await maxOutput.boundingBox()
  expect(contextBox).not.toBeNull()
  expect(outputBox).not.toBeNull()
  expect(Math.abs(contextBox!.y - outputBox!.y)).toBeLessThanOrEqual(1)
  expect(Math.abs(contextBox!.height - outputBox!.height)).toBeLessThanOrEqual(1)
})

test('model binding capacity fields autosave together', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.setPairRows([{
    provider: 'provider-a', model: 'model-a', upstream_model_id: 'upstream-a',
    context_window: 32768, max_output: 4096, supports_tools: false, supports_vision: false,
    supports_audio_input: false, supports_reasoning: false, enabled: true,
    source: 'local', owner: 'admin',
  }])
  await page.goto('/admin/models')
  await page.getByRole('button', { name: '编辑能力' }).click()
  await page.getByRole('spinbutton', { name: '上下文窗口（Tokens）' }).fill('10000')
  await page.getByRole('spinbutton', { name: '上下文窗口（Tokens）' }).press('Tab')
  await expect.poll(() => mock.registryPatchRequests).toEqual([{
    path: '/admin/v1/pairs/provider-a/model-a', body: { context_window: 10000, max_output: 4096 },
  }])
  await expect(page.getByText('更改已保存', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '保存更改' })).toHaveCount(0)
})

function groupPair(provider: string, model: string, enabled = true) {
  return {
    provider, model, upstream_model_id: model,
    context_window: 32768, max_output: 4096, supports_tools: false, supports_vision: false,
    supports_audio_input: false, supports_reasoning: false, enabled,
    source: 'local', owner: 'admin',
  }
}

function groupProvider(key: string, enabled = true) {
  return { key, kind: 'openai_compatible', display_name: key, enabled, priority: 0, source: 'local', owner: 'admin', gateway_provider: null, base_url: 'https://a.example/v1', api_key_set: true, pair_count: 1 }
}

function groupModel(id: string, enabled = true) {
  return { id, display_name: id, enabled, pair_count: 1, source: 'local', owner: 'admin' }
}

test('model groups autosave checkbox changes without a save button', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.setPairRows([groupPair('provider-a', 'model-a')])
  mock.setProviderRows([groupProvider('provider-a')])
  mock.setModelRows([groupModel('model-a')])
  await page.goto('/admin/groups')
  await page.getByRole('checkbox', { name: 'provider-a · model-a' }).first().check()
  await expect.poll(() => mock.groupPutBodies).toEqual([{
    simple: [{ provider: 'provider-a', model: 'model-a' }], medium: [], complex: [],
  }])
  await expect(page.getByRole('button', { name: /保存/ })).toHaveCount(0)
  await expect(page.getByText('更改已保存', { exact: true })).toBeVisible()
})

test('model groups only offer routable pairs and flag stale members', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.setPairRows([
    groupPair('provider-a', 'model-a'),
    groupPair('provider-a', 'model-off', false),
    groupPair('provider-b', 'model-a'),
  ])
  mock.setProviderRows([groupProvider('provider-a'), groupProvider('provider-b', false)])
  mock.setModelRows([groupModel('model-a'), groupModel('model-off')])
  mock.setGroupDocument({ simple: [{ provider: 'provider-b', model: 'model-a' }], medium: [], complex: [] })
  await page.goto('/admin/groups')

  await expect(page.getByRole('checkbox', { name: 'provider-a · model-off（绑定已停用）' }).first()).toBeDisabled()
  await expect(page.getByText('组内有不可用的成员').first()).toBeVisible()
  await expect(page.getByText('提供商已停用', { exact: true }).first()).toBeVisible()
  await page.getByRole('button', { name: '移除 provider-b model-a' }).click()
  await expect.poll(() => mock.groupPutBodies).toEqual([{ simple: [], medium: [], complex: [] }])
})

test('model groups roll back a rejected save', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.setPairRows([groupPair('provider-a', 'model-a')])
  mock.setProviderRows([groupProvider('provider-a')])
  mock.setModelRows([groupModel('model-a')])
  mock.failGroupPutOnce()
  await page.goto('/admin/groups')

  const checkbox = page.getByRole('checkbox', { name: 'provider-a · model-a' }).first()
  await checkbox.check()
  await expect(page.getByText('模型分组配置无效')).toBeVisible()
  await expect(checkbox).not.toBeChecked()
})

test('the legacy pairs path redirects to model groups', async ({ page }) => {
  await installApiMocks(page)
  await page.goto('/admin/pairs')
  await expect(page).toHaveURL(/\/admin\/groups$/)
  await expect(page.getByRole('heading', { name: '模型分组', level: 1 })).toBeVisible()
})

test('model management can permanently unbind a pair and refresh the table', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.setPairRows([{
    provider: 'provider-a', model: 'tenant/model', upstream_model_id: 'upstream-model',
    context_window: 32768, max_output: null, supports_tools: false, supports_vision: false,
    supports_audio_input: false, supports_reasoning: false, enabled: true,
    source: 'local', owner: 'admin',
  }])
  page.on('dialog', dialog => dialog.accept())
  await page.goto('/admin/models')

  await expect(page.getByText('tenant/model', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '解除绑定 provider-a - tenant/model' }).click()
  await expect(page.getByText('没有匹配的模型绑定')).toBeVisible()
  await expect.poll(() => mock.pairDeleteRequests).toEqual(['/admin/v1/pairs/provider-a/tenant%2Fmodel'])
})

test('dashboard displays overall latency source and preserves zero measurements', async ({ page }) => {
  const mock = await installApiMocks(page)
  mock.useOverallLatencyWithoutSamples()
  await page.goto('/admin/')

  await expect(page.getByText('全部请求平均延迟', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('0 ms', { exact: true })).toHaveCount(3)
})

test('dashboard separates health from operational routing summary', async ({ page }) => {
  const mock = await installApiMocks(page)
  await page.goto('/admin/')
  await expect(page.getByRole('heading', { name: '系统总览' })).toBeVisible()
  await expect(page.getByText('服务运行正常')).toBeVisible()
  await expect(page.getByRole('heading', { name: '请求状态分布' })).toBeVisible()
  await expect(page.getByRole('heading', { name: '智能路由决策效能' })).toBeVisible()
  await expect(page.getByRole('heading', { name: '流量与 Token 构成' })).toBeVisible()
  await expect(page.getByText('自动路由: 6 (60.0%)')).toBeVisible()
  await page.getByRole('combobox').first().click()
  await page.getByRole('option', { name: /最近 7 天/ }).click()
  await expect.poll(() => mock.requestedSummaryWindow).toBe('7d')
})
