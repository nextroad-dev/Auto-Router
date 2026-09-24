<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, type SettingsDocument } from '@/lib/api'
import { errorNotice } from '@/lib/errors'
import ErrorAlert from '@/components/ErrorAlert.vue'
import type { components } from '@/lib/generated-api'
import { buildJevPatch, buildSettingsPatch, routingPreferenceOptions, splitSettingList, validateTierRows, validateUniqueList, type TierRow } from '@/lib/settings-form'
import { clearSession } from '@/lib/session'

const router = useRouter()
type SettingField = components['schemas']['SettingField']
type SettingsChange = { version: number; settings: SettingField[]; effective: Record<string, unknown>; warnings: string[]; changed?: string[] }

const loading = ref(false)
const saving = ref(false)
// Holds either a thrown backend failure or a locally authored validation sentence;
// ErrorAlert renders both without the page tracking which one it is.
const error = ref<unknown>()
const saved = ref(false)
const password = reactive({ current: '', next: '', confirm: '' })
const passwordBusy = ref(false)
const passwordError = ref<unknown>()
const report = ref<SettingsDocument>()
const values = reactive<Record<string, unknown>>({})
const listValues = reactive<Record<string, string>>({})
const jevKey = ref('')
const keyConfigured = ref(false)
const costTiers = ref<TierRow[]>([])
const latencyTiers = ref<TierRow[]>([])
const pendingConflict = ref<Record<string, unknown>>()
const conflictLabel = ref('')
const resetConfirm = ref(false)

const inputModes = [
  { label: '脱敏内容（redacted）', value: 'redacted' },
  { label: '提取内容（content）', value: 'content' },
  { label: '仅特征（features_only）', value: 'features_only' },
]
const sources: Record<string, string> = { default: '代码默认值', file: '配置文件', env: '环境变量', runtime: '运行时覆盖' }

const listPaths = [
  'routing.policy.allow_models', 'routing.policy.deny_models',
  'routing.policy.allow_providers', 'routing.policy.deny_providers',
  'routing.policy.allow_pairs', 'routing.policy.deny_pairs',
  'registry.sync.include',
]
const policyPaths = [
  'routing.policy.high_confidence', 'routing.policy.low_confidence', 'routing.policy.refuse_truncated_evidence',
  'routing.policy.default_model', 'routing.policy.cost_tiers', 'routing.policy.latency_tiers',
  'routing.policy.allow_models', 'routing.policy.deny_models', 'routing.policy.allow_providers',
  'routing.policy.deny_providers', 'routing.policy.allow_pairs', 'routing.policy.deny_pairs',
]
const routePaths = ['routing.default_preference', 'routing.allow_provider_override', 'routing.auto.failover.enabled']
const logPaths = [
  'routing.log.enabled', 'routing.log.store_client_ip', 'routing.log.retention_days',
  'routing.log.jev_trace.enabled', 'routing.log.jev_trace.retention_days',
]
const adminPaths = ['admin.session_ttl']
const syncPaths = ['registry.sync.include']
const debugPaths = ['routing.analyzer_debug_endpoint', 'routing.policy_debug_endpoint']

const settingIndex = computed(() => new Map((report.value?.settings ?? []).map(field => [field.path, field])))
const settingRows = computed(() => (report.value?.settings ?? []).map(field => ({
  path: field.path,
  value: field.secret ? (field.set ? '已配置（不回显）' : '未配置') : displayValue(field.value),
  source: sources[field.source] ?? field.source,
  mutable: field.mutable,
  reason: field.reason || (field.restart_required ? '需要重启后生效' : ''),
})))

function displayValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'boolean') return value ? '启用' : '停用'
  if (Array.isArray(value)) return value.length ? JSON.stringify(value) : '（空）'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}
function value<T>(path: string, fallback: T): T {
  const current = values[path]
  return (current === undefined || current === null ? fallback : current) as T
}
function setValue(path: string, next: unknown) { values[path] = next }
function mutable(path: string) { return settingIndex.value.get(path)?.mutable ?? false }
function metadata(path: string) {
  const field = settingIndex.value.get(path)
  if (!field) return '读取设置中…'
  const state = field.mutable ? '可运行时修改' : `只读${field.reason ? `：${field.reason}` : '（需要重启）'}`
  return `${sources[field.source] ?? field.source} · ${state}`
}
function list(path: string) {
  return listValues[path] ?? ''
}
function parsedList(path: string) { return splitSettingList(list(path)) }
function setList(path: string, text: string) { listValues[path] = text }
function tiers(kind: 'cost' | 'latency') { return kind === 'cost' ? costTiers.value : latencyTiers.value }
function addTier(kind: 'cost' | 'latency') { tiers(kind).push({ model: '', provider: '', tier: 0 }) }
function removeTier(kind: 'cost' | 'latency', index: number) { tiers(kind).splice(index, 1) }
function hydrate() {
  for (const field of report.value?.settings ?? []) values[field.path] = field.value
  for (const path of listPaths) {
    const fieldValue = settingIndex.value.get(path)?.value
    listValues[path] = Array.isArray(fieldValue) ? fieldValue.map(String).join('\n') : ''
  }
  const cost = settingIndex.value.get('routing.policy.cost_tiers')?.value
  const latency = settingIndex.value.get('routing.policy.latency_tiers')?.value
  costTiers.value = Array.isArray(cost) ? cost.map(tier => ({ ...(tier as TierRow) })) : []
  latencyTiers.value = Array.isArray(latency) ? latency.map(tier => ({ ...(tier as TierRow) })) : []
  const secret = settingIndex.value.get('jev.api_key')
  keyConfigured.value = Boolean(secret?.set)
  jevKey.value = ''
}

async function loadSettings(): Promise<boolean> {
  loading.value = true
  error.value = undefined
  try {
    const next = await api.get<SettingsDocument>('/admin/v1/settings')
    report.value = next
    hydrate()
    return true
  } catch (cause) {
    error.value = errorNotice(cause)
    return false
  } finally {
    loading.value = false
  }
}

function makePatch(paths: string[], overrides: Record<string, unknown> = {}) {
  return buildSettingsPatch(report.value?.settings ?? [], paths, values, overrides)
}

function validateTiersForSave(rows: TierRow[], name: string) {
  const message = validateTierRows(rows, name)
  if (message) error.value = message
  return !message
}
function validateLists(paths: string[]) {
  for (const path of paths) {
    const message = validateUniqueList(list(path), path)
    if (message) {
      error.value = message
      return false
    }
  }
  return true
}

function settingsResponse(next: SettingsChange) {
  const overlay = next.settings.some(field => field.source === 'runtime')
  const document: SettingsDocument = {
    version: next.version,
    overlay,
    settings: next.settings,
    effective: next.effective,
    warnings: next.warnings,
  }
  report.value = document
  hydrate()
}

async function submitPatch(patch: Record<string, unknown>, label: string) {
  if (!Object.keys(patch).length) {
    error.value = '此区域没有可运行时修改的设置。'
    return
  }
  saving.value = true
  error.value = undefined
  saved.value = false
  try {
    const result = await api.patch<SettingsChange>('/admin/v1/settings', patch)
    settingsResponse(result)
    pendingConflict.value = undefined
    conflictLabel.value = ''
    saved.value = true
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 409) {
      const refreshed = await loadSettings()
      if (refreshed) {
        pendingConflict.value = patch
        conflictLabel.value = label
        error.value = undefined
      } else {
        error.value = '设置版本冲突，且无法重新读取当前设置。请刷新页面后再继续。'
      }
    } else {
      error.value = errorNotice(cause)
    }
  } finally {
    saving.value = false
  }
}
async function confirmConflictRetry() {
  if (!pendingConflict.value) return
  const patch = pendingConflict.value
  pendingConflict.value = undefined
  await submitPatch(patch, conflictLabel.value)
}
function cancelConflictRetry() {
  pendingConflict.value = undefined
  conflictLabel.value = ''
}

async function saveRouting() {
  await submitPatch(makePatch(routePaths), '路由行为')
}
async function savePolicy() {
  error.value = undefined
  if (!validateTiersForSave(costTiers.value, '成本等级') || !validateTiersForSave(latencyTiers.value, '延迟等级')) return
  if (!validateLists(policyPaths)) return
  const high = Number(value('routing.policy.high_confidence', 0.7))
  const low = Number(value('routing.policy.low_confidence', 0.3))
  if (!Number.isFinite(high) || !Number.isFinite(low) || low < 0 || low > 1 || high < 0 || high > 1 || low >= high) {
    error.value = '置信度阈值必须在 0 到 1 之间，且低阈值必须小于高阈值。'
    return
  }
  const overrides: Record<string, unknown> = {
    'routing.policy.cost_tiers': costTiers.value.map(row => ({ ...(row.model?.trim() ? { model: row.model.trim() } : {}), ...(row.provider?.trim() ? { provider: row.provider.trim() } : {}), tier: Number(row.tier) })),
    'routing.policy.latency_tiers': latencyTiers.value.map(row => ({ ...(row.model?.trim() ? { model: row.model.trim() } : {}), ...(row.provider?.trim() ? { provider: row.provider.trim() } : {}), tier: Number(row.tier) })),
  }
  for (const path of listPaths.filter(path => path !== 'registry.sync.include')) overrides[path] = parsedList(path)
  await submitPatch(makePatch(policyPaths, overrides), '策略')
}
async function saveLogs() {
  for (const path of ['routing.log.retention_days', 'routing.log.jev_trace.retention_days']) {
    const days = Number(value(path, 0))
    if (!Number.isInteger(days) || days < 0 || days > 3650) {
      error.value = '日志留存天数必须是 0 到 3650 的整数（0 表示永久保留）。'
      return
    }
  }
  await submitPatch(makePatch(logPaths, {
    'routing.log.retention_days': Number(value('routing.log.retention_days', 30)),
    'routing.log.jev_trace.retention_days': Number(value('routing.log.jev_trace.retention_days', 7)),
  }), '日志')
}
async function saveJev() {
  const patch = buildJevPatch(report.value?.settings ?? [], values, jevKey.value)
  await submitPatch(patch, 'Jev')
}
async function saveSession() {
  const ttl = String(value('admin.session_ttl', '12h')).trim()
  if (!ttl) {
    error.value = '会话 TTL 不能为空，例如 12h 或 30m。'
    return
  }
  await submitPatch(makePatch(adminPaths, { 'admin.session_ttl': ttl }), '管理会话')
}
async function saveSyncScope() {
  if (!validateLists(syncPaths)) return
  await submitPatch(makePatch(syncPaths, { 'registry.sync.include': parsedList('registry.sync.include') }), '同步范围')
}
async function saveDebug() { await submitPatch(makePatch(debugPaths), '高级诊断') }

async function resetSettings() {
  saving.value = true
  error.value = undefined
  saved.value = false
  try {
    const result = await api.delete<SettingsChange>('/admin/v1/settings')
    settingsResponse(result)
    pendingConflict.value = undefined
    resetConfirm.value = false
    saved.value = true
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    saving.value = false
  }
}

async function changePassword() {
  passwordError.value = undefined
  if (!password.current || !password.next) {
    passwordError.value = '请输入当前密码和新密码。'
    return
  }
  if (password.next !== password.confirm) {
    passwordError.value = '两次输入的新密码不一致。'
    return
  }
  if (new TextEncoder().encode(password.next).length < 12) {
    passwordError.value = '新密码至少需要 12 字节。'
    return
  }
  passwordBusy.value = true
  try {
    await api.patch('/admin/v1/password', { current_password: password.current, password: password.next })
    password.current = ''
    password.next = ''
    password.confirm = ''
    clearSession()
    await router.replace({ name: 'login' })
  } catch (cause) {
    passwordError.value = errorNotice(cause)
  } finally {
    passwordBusy.value = false
  }
}

onMounted(() => { void loadSettings() })
</script>

<template>
  <div class="space-y-5">
    <ErrorAlert v-if="error" :error="error" />
    <UAlert v-if="saved" color="success" variant="soft" title="设置已保存，页面已使用服务端返回的生效值更新。" />
    <UAlert v-if="report?.warnings.length" color="warning" variant="soft" title="配置提醒">
      <ul class="mt-2 list-disc space-y-1 pl-5 text-sm"><li v-for="warning in report.warnings" :key="warning">{{ warning }}</li></ul>
    </UAlert>
    <UAlert v-if="pendingConflict" color="warning" variant="soft" title="设置已被其他操作修改。页面已重新读取最新值；请确认本次改动仍然符合预期后再重试。">
      <template #actions>
        <div class="flex gap-2">
          <UButton color="warning" size="sm" :loading="saving" @click="confirmConflictRetry">确认并重试{{ conflictLabel }}</UButton>
          <UButton color="neutral" variant="ghost" size="sm" @click="cancelConflictRetry">放弃本次改动</UButton>
        </div>
      </template>
    </UAlert>

    <UCard>
      <template #header>
        <div><h3 class="font-semibold">路由行为</h3><p class="mt-1 text-sm text-muted">偏好是无请求头时的全局默认值。请求方可通过 <code>X-Routing-Preference</code> 为单次请求覆盖。</p></div>
      </template>
      <div class="grid gap-4 sm:grid-cols-2">
        <UFormField label="全局默认路由偏好" :hint="metadata('routing.default_preference')">
          <USelect :model-value="value('routing.default_preference', 'balanced')" :items="routingPreferenceOptions" value-key="value" :disabled="loading || saving || !mutable('routing.default_preference')" class="w-full" @update:model-value="setValue('routing.default_preference', $event)" />
          <template #description>quality 按管理员声明的模型能力评分，不代表真实基准测试；cost / latency 按下方维护的相对等级评分，不是账单价格或实测延迟。</template>
        </UFormField>
        <UFormField label="允许显式 Provider 覆盖" :hint="metadata('routing.allow_provider_override')"><USwitch :model-value="value('routing.allow_provider_override', false)" :disabled="loading || saving || !mutable('routing.allow_provider_override')" @update:model-value="setValue('routing.allow_provider_override', $event)" /></UFormField>
        <UFormField label="自动路由故障转移" :hint="metadata('routing.auto.failover.enabled')"><USwitch :model-value="value('routing.auto.failover.enabled', true)" :disabled="loading || saving || !mutable('routing.auto.failover.enabled')" @update:model-value="setValue('routing.auto.failover.enabled', $event)" /><template #description>仅对符合条件的上游传输失败尝试一次有界重试。</template></UFormField>
        <div class="flex items-end justify-end"><UButton :loading="saving" :disabled="loading" @click="saveRouting">保存路由行为</UButton></div>
      </div>
    </UCard>

    <UCard>
      <template #header><div><h3 class="font-semibold">策略</h3><p class="mt-1 text-sm text-muted">allow-list 为空表示不额外限制。等级越小越优先；等级为 3 或更高不再获得等级奖励。</p></div></template>
      <div class="grid gap-4 sm:grid-cols-2">
        <UFormField label="高置信度阈值" :hint="metadata('routing.policy.high_confidence')"><UInput :model-value="value('routing.policy.high_confidence', 0.7)" type="number" min="0" max="1" step="0.01" :disabled="loading || saving || !mutable('routing.policy.high_confidence')" class="w-full" @update:model-value="setValue('routing.policy.high_confidence', Number($event))" /></UFormField>
        <UFormField label="低置信度阈值" :hint="metadata('routing.policy.low_confidence')"><UInput :model-value="value('routing.policy.low_confidence', 0.3)" type="number" min="0" max="1" step="0.01" :disabled="loading || saving || !mutable('routing.policy.low_confidence')" class="w-full" @update:model-value="setValue('routing.policy.low_confidence', Number($event))" /></UFormField>
        <UFormField label="截断证据时拒绝路由" :hint="metadata('routing.policy.refuse_truncated_evidence')"><USwitch :model-value="value('routing.policy.refuse_truncated_evidence', true)" :disabled="loading || saving || !mutable('routing.policy.refuse_truncated_evidence')" @update:model-value="setValue('routing.policy.refuse_truncated_evidence', $event)" /></UFormField>
        <UFormField label="低置信度默认模型" :hint="metadata('routing.policy.default_model')"><UInput :model-value="value('routing.policy.default_model', '')" placeholder="留空表示不指定" :disabled="loading || saving || !mutable('routing.policy.default_model')" class="w-full" @update:model-value="setValue('routing.policy.default_model', $event)" /></UFormField>
      </div>

      <div class="mt-6 grid gap-5 xl:grid-cols-2">
        <section v-for="kind in (['cost', 'latency'] as const)" :key="kind" class="rounded-lg border border-default p-4">
          <div class="flex items-start justify-between gap-3"><div><h4 class="font-medium">{{ kind === 'cost' ? '成本等级' : '延迟等级' }}</h4><p class="mt-1 text-xs text-muted">每项填写模型或 Provider（二选一）及非负整数等级。</p></div><UButton size="sm" color="neutral" variant="outline" :disabled="loading || saving || !mutable(kind === 'cost' ? 'routing.policy.cost_tiers' : 'routing.policy.latency_tiers')" @click="addTier(kind)">添加</UButton></div>
          <div v-if="tiers(kind).length" class="mt-3 space-y-3">
            <div v-for="(row, index) in tiers(kind)" :key="`${kind}-${index}`" class="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_1fr_7rem_auto]">
              <UInput v-model="row.model" placeholder="模型 ID" :disabled="loading || saving || !mutable(kind === 'cost' ? 'routing.policy.cost_tiers' : 'routing.policy.latency_tiers')" />
              <UInput v-model="row.provider" placeholder="Provider" :disabled="loading || saving || !mutable(kind === 'cost' ? 'routing.policy.cost_tiers' : 'routing.policy.latency_tiers')" />
              <UInput v-model.number="row.tier" type="number" min="0" step="1" placeholder="等级" :disabled="loading || saving || !mutable(kind === 'cost' ? 'routing.policy.cost_tiers' : 'routing.policy.latency_tiers')" />
              <UButton color="error" variant="ghost" :disabled="loading || saving || !mutable(kind === 'cost' ? 'routing.policy.cost_tiers' : 'routing.policy.latency_tiers')" :aria-label="`删除${kind === 'cost' ? '成本' : '延迟'}等级 ${index + 1}`" @click="removeTier(kind, index)">删除</UButton>
            </div>
          </div>
          <p v-else class="mt-3 text-sm text-muted">未配置等级表。</p>
          <p class="mt-2 text-xs text-muted">{{ metadata(kind === 'cost' ? 'routing.policy.cost_tiers' : 'routing.policy.latency_tiers') }}</p>
        </section>
      </div>

      <div class="mt-6 grid gap-4 sm:grid-cols-2">
        <UFormField v-for="entry in [
          ['routing.policy.allow_models', '允许模型'], ['routing.policy.deny_models', '拒绝模型'],
          ['routing.policy.allow_providers', '允许 Provider'], ['routing.policy.deny_providers', '拒绝 Provider'],
          ['routing.policy.allow_pairs', '允许 Provider/模型对'], ['routing.policy.deny_pairs', '拒绝 Provider/模型对'],
        ]" :key="entry[0]" :label="entry[1]" :hint="metadata(entry[0])">
          <UTextarea :model-value="list(entry[0])" :rows="3" placeholder="每行一个标识" :disabled="loading || saving || !mutable(entry[0])" class="w-full" @update:model-value="setList(entry[0], $event)" />
          <template #description>每行一项；Provider/模型对格式为 provider/model。</template>
        </UFormField>
      </div>
      <div class="mt-5 flex justify-end"><UButton :loading="saving" :disabled="loading" @click="savePolicy">保存策略</UButton></div>
    </UCard>

    <UCard>
      <template #header><div><h3 class="font-semibold">持久日志</h3><p class="mt-1 text-sm text-muted">留存为 0 表示永久保留；最大 3650 天。</p></div></template>
      <div class="grid gap-4 sm:grid-cols-2">
        <UFormField label="启用路由日志" :hint="metadata('routing.log.enabled')"><USwitch :model-value="value('routing.log.enabled', true)" :disabled="loading || saving || !mutable('routing.log.enabled')" @update:model-value="setValue('routing.log.enabled', $event)" /></UFormField>
        <UFormField label="记录客户端 IP（个人数据）" :hint="metadata('routing.log.store_client_ip')"><USwitch :model-value="value('routing.log.store_client_ip', false)" :disabled="loading || saving || !mutable('routing.log.store_client_ip')" @update:model-value="setValue('routing.log.store_client_ip', $event)" /><template #description>启用后会将客户端地址写入持久日志。</template></UFormField>
        <UFormField label="路由日志留存天数" :hint="metadata('routing.log.retention_days')"><UInput :model-value="value('routing.log.retention_days', 30)" type="number" min="0" max="3650" step="1" :disabled="loading || saving || !mutable('routing.log.retention_days')" class="w-full" @update:model-value="setValue('routing.log.retention_days', Number($event))" /></UFormField>
        <UFormField label="启用 Jev 诊断 trace" :hint="metadata('routing.log.jev_trace.enabled')"><USwitch :model-value="value('routing.log.jev_trace.enabled', false)" :disabled="loading || saving || !mutable('routing.log.jev_trace.enabled')" @update:model-value="setValue('routing.log.jev_trace.enabled', $event)" /><template #description>Trace 保存诊断元数据，不保存原始提示文本。</template></UFormField>
        <UFormField label="Jev trace 留存天数" :hint="metadata('routing.log.jev_trace.retention_days')"><UInput :model-value="value('routing.log.jev_trace.retention_days', 7)" type="number" min="0" max="3650" step="1" :disabled="loading || saving || !mutable('routing.log.jev_trace.retention_days')" class="w-full" @update:model-value="setValue('routing.log.jev_trace.retention_days', Number($event))" /></UFormField>
      </div>
      <div class="mt-5 flex justify-end"><UButton :loading="saving" :disabled="loading" @click="saveLogs">保存日志设置</UButton></div>
    </UCard>

    <UCard>
      <template #header><div><h3 class="font-semibold">Jev</h3><p class="mt-1 text-sm text-muted">密钥仅写入、不回显。密钥输入留空表示保留现有密钥；重置所有运行时覆盖会清除运行时保存的 Jev 密钥。</p></div></template>
      <div class="grid gap-4 sm:grid-cols-2">
        <UFormField label="启用" :hint="metadata('jev.enabled')"><USwitch :model-value="value('jev.enabled', false)" :disabled="loading || saving || !mutable('jev.enabled')" @update:model-value="setValue('jev.enabled', $event)" /></UFormField>
        <UFormField label="Jev 输入模式" :hint="metadata('jev.input_mode')"><USelect :model-value="value('jev.input_mode', 'redacted')" :items="inputModes" value-key="value" :disabled="loading || saving || !mutable('jev.input_mode')" class="w-full" @update:model-value="setValue('jev.input_mode', $event)" /><template #description>选择 Jev 可收到的请求内容范围；features_only 不发送客户文本。</template></UFormField>
        <UFormField label="服务端点" :hint="metadata('jev.base_url')"><UInput :model-value="value('jev.base_url', '')" type="url" placeholder="https://..." :disabled="loading || saving || !mutable('jev.base_url')" class="w-full" @update:model-value="setValue('jev.base_url', $event)" /></UFormField>
        <UFormField label="模型" :hint="metadata('jev.model')"><UInput :model-value="value('jev.model', '')" placeholder="jev-latest" :disabled="loading || saving || !mutable('jev.model')" class="w-full" @update:model-value="setValue('jev.model', $event)" /></UFormField>
        <UFormField :label="keyConfigured ? '替换 API 密钥（已配置）' : 'API 密钥'" :hint="metadata('jev.api_key')"><UInput v-model="jevKey" type="password" autocomplete="new-password" :placeholder="keyConfigured ? '留空保留现有密钥' : '仅在需要设置时填写'" :disabled="loading || saving || !mutable('jev.api_key')" class="w-full" /><template #description>{{ keyConfigured ? '当前有密钥；输入新值将替换，清空输入不会删除。' : '密钥不会被读取或回显。' }}</template></UFormField>
      </div>
      <div class="mt-5 flex justify-end"><UButton :loading="saving" :disabled="loading" @click="saveJev">保存 Jev</UButton></div>
    </UCard>

    <UCard>
      <template #header><div><h3 class="font-semibold">管理与同步</h3><p class="mt-1 text-sm text-muted">会话 TTL 使用 Go duration 格式，例如 12h、30m。models.dev 同步范围每行一个 provider/model；空列表不允许同步。</p></div></template>
      <div class="grid gap-4 sm:grid-cols-2">
        <UFormField label="管理员会话 TTL" :hint="metadata('admin.session_ttl')"><UInput :model-value="value('admin.session_ttl', '12h')" placeholder="12h" :disabled="loading || saving || !mutable('admin.session_ttl')" class="w-full" @update:model-value="setValue('admin.session_ttl', $event)" /></UFormField>
        <UFormField label="models.dev 同步包含范围" :hint="metadata('registry.sync.include')"><UTextarea :model-value="list('registry.sync.include')" :rows="4" placeholder="例如：openai/gpt-4.1" :disabled="loading || saving || !mutable('registry.sync.include')" class="w-full" @update:model-value="setList('registry.sync.include', $event)" /><template #description>此处是 models.dev 的全局同步 allow-list，不是 Provider 的发现接口。</template></UFormField>
      </div>
      <div class="mt-5 flex justify-end gap-2"><UButton color="neutral" variant="outline" :loading="saving" :disabled="loading" @click="saveSyncScope">保存同步范围</UButton><UButton :loading="saving" :disabled="loading" @click="saveSession">保存会话 TTL</UButton></div>
    </UCard>

    <UCard>
      <template #header><div><h3 class="font-semibold">高级：本地诊断端点</h3><p class="mt-1 text-sm text-muted">启用项不会绕过服务端校验，也不会使端点可远程访问。</p></div></template>
      <UAlert color="warning" variant="soft" title="安全限制：/debug/analyze 与 /debug/route 仅用于离线诊断，并受服务端 loopback 监听及配置约束。它们可能接收敏感请求数据；本管理页面不提供远程调试交互。" />
      <div class="mt-4 grid gap-4 sm:grid-cols-2">
        <UFormField label="启用 /debug/analyze" :hint="metadata('routing.analyzer_debug_endpoint')"><USwitch :model-value="value('routing.analyzer_debug_endpoint', false)" :disabled="loading || saving || !mutable('routing.analyzer_debug_endpoint')" @update:model-value="setValue('routing.analyzer_debug_endpoint', $event)" /></UFormField>
        <UFormField label="启用 /debug/route" :hint="metadata('routing.policy_debug_endpoint')"><USwitch :model-value="value('routing.policy_debug_endpoint', false)" :disabled="loading || saving || !mutable('routing.policy_debug_endpoint')" @update:model-value="setValue('routing.policy_debug_endpoint', $event)" /></UFormField>
      </div>
      <div class="mt-5 flex justify-end"><UButton color="neutral" variant="outline" :loading="saving" :disabled="loading" @click="saveDebug">保存高级设置</UButton></div>
    </UCard>

    <UCard>
      <template #header><h3 class="font-semibold">运行时覆盖</h3></template>
      <p class="mb-4 text-sm text-muted">重置将移除全部运行时设置覆盖并恢复代码默认值，同时清除运行时保存的 Jev 密钥；不会删除 Provider、模型、绑定或入站凭据。</p>
      <div v-if="resetConfirm" class="mb-4 rounded-lg border border-warning bg-warning/5 p-4" role="alertdialog" aria-label="确认重置所有运行时覆盖">
        <p class="font-medium">确认重置全部运行时覆盖？</p><p class="mt-1 text-sm text-muted">此操作影响所有设置区域，不可单独撤销。Provider/模型数据及入站凭据会保留。</p>
        <div class="mt-3 flex justify-end gap-2"><UButton color="neutral" variant="ghost" :disabled="saving" @click="resetConfirm = false">取消</UButton><UButton color="error" :loading="saving" @click="resetSettings">确认重置</UButton></div>
      </div>
      <div class="flex justify-end"><UButton color="error" variant="outline" :disabled="loading || saving" @click="resetConfirm = true">重置所有运行时覆盖</UButton></div>
    </UCard>

    <UCard>
      <template #header><div><h3 class="font-semibold">设置来源与生效值</h3><p class="mt-1 text-sm text-muted">以下信息来自 settings API。秘密只显示配置状态，不显示密钥内容。</p></div></template>
      <div class="overflow-x-auto">
        <table class="w-full min-w-[680px] text-left text-sm">
          <thead><tr class="border-b border-default text-muted"><th class="px-3 py-2">路径</th><th class="px-3 py-2">生效值</th><th class="px-3 py-2">来源</th><th class="px-3 py-2">状态 / 原因</th></tr></thead>
          <tbody><tr v-for="row in settingRows" :key="row.path" class="border-b border-default/60"><td class="px-3 py-2 font-mono text-xs">{{ row.path }}</td><td class="max-w-80 break-all px-3 py-2">{{ row.value }}</td><td class="px-3 py-2">{{ row.source }}</td><td class="px-3 py-2">{{ row.mutable ? '运行时可变' : row.reason || '需要重启' }}</td></tr></tbody>
        </table>
      </div>
    </UCard>

    <UCard>
      <template #header><h3 class="font-semibold">管理员密码</h3></template>
      <form class="grid gap-4 sm:grid-cols-2" @submit.prevent="changePassword">
        <UFormField label="当前密码" required><UInput v-model="password.current" type="password" autocomplete="current-password" class="w-full" /></UFormField>
        <span class="hidden sm:block" />
        <UFormField label="新密码" required><UInput v-model="password.next" type="password" autocomplete="new-password" class="w-full" /></UFormField>
        <UFormField label="确认新密码" required><UInput v-model="password.confirm" type="password" autocomplete="new-password" class="w-full" /></UFormField>
        <ErrorAlert v-if="passwordError" class="sm:col-span-2" :error="passwordError" />
        <div class="flex justify-end sm:col-span-2"><UButton type="submit" :loading="passwordBusy">更新密码</UButton></div>
      </form>
    </UCard>
  </div>
</template>
