<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, type SettingsDocument } from '@/lib/api'
import { createDebouncedSave, createSerialAutosaveQueue, retryOnceOnConflict } from '@/lib/autosave'
import { errorNotice } from '@/lib/errors'
import { enumLabel, inputModeDescription } from '@/lib/labels'
import ErrorAlert from '@/components/ErrorAlert.vue'
import JfAlert from '@/components/JfAlert.vue'
import JfButton from '@/components/JfButton.vue'
import JfCard from '@/components/JfCard.vue'
import JfCheckbox from '@/components/JfCheckbox.vue'
import JfField from '@/components/JfField.vue'
import JfInput from '@/components/JfInput.vue'
import JfSelect from '@/components/JfSelect.vue'
import JfSlider from '@/components/JfSlider.vue'
import JfSwitch from '@/components/JfSwitch.vue'
import type { components } from '@/lib/generated-api'
import {
  buildSettingsPatch,
  normalizeRetryStatusCodes,
  retryStatusCodeOptions,
} from '@/lib/settings-form'
import { clearSession } from '@/lib/session'
import { showSavedToast } from '@/lib/save-toast'
import { confirmAction } from '@/lib/confirm'

const router = useRouter()
type SettingsChange = components['schemas']['SettingsApplied'] | components['schemas']['SettingsReset']

const loading = ref(false)
const saving = ref(false)
const error = ref<unknown>()
const password = reactive({ current: '', next: '', confirm: '' })
const passwordBusy = ref(false)
const passwordError = ref<unknown>()
const report = ref<SettingsDocument>()
const values = reactive<Record<string, unknown>>({})
const retryStatusCodes = ref<number[]>([])
const jevKey = ref('')
const keyConfigured = ref(false)
const dirtySettings = new Set<string>()
const pendingTextPaths = new Set<string>()
const settingVersions = new Map<string, number>()
const textSaves = new Map<string, ReturnType<typeof createDebouncedSave>>()

const inputModes = (['redacted', 'content', 'features_only'] as const)
  .map(mode => ({ label: enumLabel('inputMode', mode), value: mode }))

const policyPaths = [
  'routing.policy.low_confidence',
  'routing.policy.refuse_truncated_evidence',
]

const routePaths = [
  'routing.allow_provider_override',
  'routing.auto.default_group',
  'routing.auto.failover.enabled',
  'routing.auto.failover.max_attempts',
  'routing.auto.failover.retry_on.pre_request_failure',
  'routing.auto.failover.retry_on.timeout',
  'routing.auto.failover.retry_on.status_codes',
]
const logPaths = [
  'routing.log.enabled',
  'routing.log.store_client_ip',
  'routing.log.retention_days',
  'routing.log.jev_trace.enabled',
  'routing.log.jev_trace.retention_days',
]
const jevPaths = ['jev.enabled', 'jev.input_mode', 'jev.base_url', 'jev.model', 'jev.api_key']

const settingsQueue = createSerialAutosaveQueue<Set<string>>(
  paths => persistSettings(paths),
  (current, next) => new Set([...current, ...next]),
)

function settingsResponse(data: SettingsDocument | SettingsChange, overlay?: boolean) {
  report.value = {
    version: data.version,
    overlay: 'overlay' in data ? data.overlay : overlay ?? report.value?.overlay ?? false,
    settings: data.settings,
    effective: data.effective,
    warnings: data.warnings,
  }
  for (const field of data.settings) {
    if (!dirtySettings.has(field.path)) values[field.path] = field.value
    if (field.secret) keyConfigured.value = Boolean(field.set)
  }
  if (!dirtySettings.has('routing.auto.failover.retry_on.status_codes')) {
    retryStatusCodes.value = normalizeRetryStatusCodes(values['routing.auto.failover.retry_on.status_codes']) ?? []
  }
}

async function loadSettings() {
  loading.value = true
  error.value = undefined
  try {
    settingsResponse(await api.get<SettingsDocument>('/admin/v1/settings'))
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    loading.value = false
  }
}

function value<T>(path: string, fallback: T): T {
  return (values[path] as T | undefined) ?? fallback
}

function settingGroup(path: string): string | undefined {
  if (routePaths.includes(path)) return 'route'
  if (policyPaths.includes(path)) return 'policy'
  if (jevPaths.includes(path)) return 'jev'
  if (logPaths.includes(path)) return 'logs'
  return undefined
}

function markSettingDirty(path: string) {
  dirtySettings.add(path)
  settingVersions.set(path, (settingVersions.get(path) ?? 0) + 1)
  error.value = undefined
}

function enqueueDirtyGroup(group: string | undefined) {
  if (!group) return
  const paths = [...dirtySettings].filter(path => settingGroup(path) === group && !pendingTextPaths.has(path))
  if (!paths.length) return
  const message = validateSettingsDraft(paths)
  if (message) {
    error.value = message
    return
  }
  settingsQueue.enqueue(new Set(paths))
}

function setValue(path: string, val: unknown) {
  values[path] = val
  markSettingDirty(path)
  enqueueDirtyGroup(settingGroup(path))
}

function setDraftValue(path: string, val: unknown) {
  values[path] = val
  markSettingDirty(path)
}

function commitSetting(path: string) {
  enqueueDirtyGroup(settingGroup(path))
}

function textSaver(path: string) {
  let saver = textSaves.get(path)
  if (!saver) {
    saver = createDebouncedSave(() => {
      pendingTextPaths.delete(path)
      enqueueDirtyGroup(settingGroup(path))
    })
    textSaves.set(path, saver)
  }
  return saver
}

function setTextValue(path: string, val: unknown) {
  values[path] = val
  markSettingDirty(path)
  pendingTextPaths.add(path)
  textSaver(path).schedule()
}

function flushTextValue(path: string) {
  if (!pendingTextPaths.has(path)) return
  pendingTextPaths.delete(path)
  void textSaver(path).flush()
}

function setJevKey(val: string) {
  jevKey.value = val
  const path = 'jev.api_key'
  if (!val.trim()) {
    textSaves.get(path)?.cancel()
    pendingTextPaths.delete(path)
    dirtySettings.delete(path)
    settingVersions.set(path, (settingVersions.get(path) ?? 0) + 1)
    error.value = undefined
    return
  }
  markSettingDirty(path)
  pendingTextPaths.add(path)
  textSaver(path).schedule()
}

function updateRetryStatus(code: number, checked: boolean) {
  const next = checked
    ? [...retryStatusCodes.value, code]
    : retryStatusCodes.value.filter(value => value !== code)
  const normalized = normalizeRetryStatusCodes(next)
  if (normalized === null) {
    error.value = 'HTTP 重试状态码仅支持 408、425、429、500、502、503、504。'
    return
  }
  retryStatusCodes.value = normalized
  values['routing.auto.failover.retry_on.status_codes'] = normalized
  markSettingDirty('routing.auto.failover.retry_on.status_codes')
  enqueueDirtyGroup('route')
}

function mutable(path: string): boolean {
  return report.value?.settings.find(f => f.path === path)?.mutable ?? true
}

function validateSettingsDraft(paths: string[]): string {
  if (paths.includes('routing.auto.failover.max_attempts')) {
    const attempts = Number(value('routing.auto.failover.max_attempts', 2))
    if (!Number.isInteger(attempts) || attempts < 1 || attempts > 8) return '自动路由总尝试次数必须是 1 到 8 之间的整数（包含首次请求）。'
  }
  if (paths.includes('routing.auto.failover.retry_on.status_codes') && normalizeRetryStatusCodes(retryStatusCodes.value) === null) {
    return 'HTTP 重试状态码仅支持 408、425、429、500、502、503、504。'
  }
  if (paths.includes('routing.policy.low_confidence')) {
    const low = Number(value('routing.policy.low_confidence', 0.45))
    if (!Number.isFinite(low) || low < 0 || low > 1) return '置信度阈值必须在 0 到 1 之间。'
  }
  for (const path of ['routing.log.retention_days', 'routing.log.jev_trace.retention_days']) {
    if (!paths.includes(path)) continue
    const days = Number(value(path, 0))
    if (!Number.isInteger(days) || days < 0 || days > 3650) return '日志留存天数必须是 0 到 3650 之间的整数（0 表示永久保留）。'
  }
  return ''
}

function makePatch(paths: string[]): Record<string, unknown> {
  const overrides: Record<string, unknown> = {}
  for (const path of paths) {
    if (path === 'routing.auto.failover.retry_on.status_codes') overrides[path] = normalizeRetryStatusCodes(retryStatusCodes.value) ?? []
    if (path === 'jev.api_key') overrides[path] = jevKey.value.trim()
  }
  return buildSettingsPatch(report.value?.settings ?? [], paths, values, overrides)
}

async function persistSettings(requestedPaths: Set<string>) {
  let activePaths = [...requestedPaths].filter(path => dirtySettings.has(path) && !pendingTextPaths.has(path))
  if (!activePaths.length) return
  const validation = validateSettingsDraft(activePaths)
  if (validation) {
    error.value = validation
    return
  }

  let savedVersions = new Map(activePaths.map(path => [path, settingVersions.get(path) ?? 0]))
  const initialPatch = makePatch(activePaths)
  if (!Object.keys(initialPatch).length) return

  saving.value = true
  error.value = undefined
  let refreshed: SettingsDocument | undefined
  let wrote = false
  try {
    const result = await retryOnceOnConflict<SettingsChange | SettingsDocument, Record<string, unknown>>(
      patch => {
        if (!Object.keys(patch).length && refreshed) return Promise.resolve(refreshed)
        wrote = true
        return api.patch<SettingsChange>('/admin/v1/settings', patch)
      },
      initialPatch,
      cause => cause instanceof ApiError && cause.code === 'settings_conflict',
      async () => {
        refreshed = await api.get<SettingsDocument>('/admin/v1/settings')
        settingsResponse(refreshed)
      },
      () => {
        activePaths = activePaths.filter(path => dirtySettings.has(path) && !pendingTextPaths.has(path))
        savedVersions = new Map(activePaths.map(path => [path, settingVersions.get(path) ?? 0]))
        return makePatch(activePaths)
      },
    )
    settingsResponse(result, true)
    if (wrote) {
      for (const path of activePaths) {
        if (settingVersions.get(path) !== savedVersions.get(path)) continue
        dirtySettings.delete(path)
        if (path === 'jev.api_key') jevKey.value = ''
      }
      showSavedToast()
    }
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    saving.value = false
  }
}

async function confirmReset() {
  const confirmed = await confirmAction({
    title: '重置全部运行时覆盖？',
    description: '所有在管理台修改过的设置和 Jev 密钥都会恢复为启动配置。此操作不可撤销。',
    confirmLabel: '确认重置',
    danger: true,
  })
  if (confirmed) await resetSettings()
}

async function resetSettings() {
  saving.value = true
  error.value = undefined
  for (const saver of textSaves.values()) saver.cancel()
  textSaves.clear()
  pendingTextPaths.clear()
  dirtySettings.clear()
  jevKey.value = ''
  try {
    const result = await api.delete<SettingsChange>('/admin/v1/settings')
    settingsResponse(result, false)
    showSavedToast('已重置全部运行时覆盖')
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
    passwordError.value = '管理员新密码至少需要 12 字节。'
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
  <div class="jf-stack">
    <!-- Page header -->
    <section class="jf-toolbar">
      <div>
        <h1 class="jf-page-title">系统设置</h1>
      </div>
      <div class="jf-action-group">
        <JfButton variant="secondary" icon="arrow-path" :loading="loading" @click="loadSettings">刷新</JfButton>
      </div>
    </section>

    <ErrorAlert v-if="error" :error="error" />

    <!-- 1. 路由默认行为 -->
    <JfCard title="全局路由行为">
      <div class="grid gap-6">
        <div class="grid gap-5 sm:grid-cols-2">
          <JfField inline label="允许显式提供商覆盖" name="routing-allow-provider-override">
            <JfSwitch
              :model-value="value('routing.allow_provider_override', false)"
              :disabled="loading || !mutable('routing.allow_provider_override')"
              @update:model-value="setValue('routing.allow_provider_override', $event)"
            />
          </JfField>

          <JfField inline label="自动路由故障转移" name="routing-auto-failover">
            <JfSwitch
              :model-value="value('routing.auto.failover.enabled', true)"
              :disabled="loading || !mutable('routing.auto.failover.enabled')"
              @update:model-value="setValue('routing.auto.failover.enabled', $event)"
            />
          </JfField>

          <JfField label="Jev 不可用或低置信度时的默认组" name="routing-auto-default-group">
            <JfSelect
              :model-value="value('routing.auto.default_group', 'medium')"
              :items="[{ label: '简单任务组', value: 'simple' }, { label: '中等任务组', value: 'medium' }, { label: '复杂任务组', value: 'complex' }]"
              :disabled="loading || !mutable('routing.auto.default_group')"
              class="w-full"
              @update:model-value="setValue('routing.auto.default_group', $event)"
            />
          </JfField>

          <JfField label="每个请求的总尝试次数" name="routing-auto-max-attempts">
            <JfInput
              :model-value="value('routing.auto.failover.max_attempts', 2)"
              type="number"
              min="1"
              max="8"
              class="w-full"
              :disabled="loading || !value('routing.auto.failover.enabled', true) || !mutable('routing.auto.failover.max_attempts')"
              @update:model-value="setTextValue('routing.auto.failover.max_attempts', Number($event))"
              @blur="flushTextValue('routing.auto.failover.max_attempts')"
            />
          </JfField>

          <JfField inline label="请求发送前失败时重试" name="routing-auto-retry-pre-request">
            <JfSwitch
              :model-value="value('routing.auto.failover.retry_on.pre_request_failure', true)"
              :disabled="loading || !value('routing.auto.failover.enabled', true) || !mutable('routing.auto.failover.retry_on.pre_request_failure')"
              @update:model-value="setValue('routing.auto.failover.retry_on.pre_request_failure', $event)"
            />
          </JfField>

          <JfField inline label="超时后重试（可能重复执行）" name="routing-auto-retry-timeout">
            <JfSwitch
              :model-value="value('routing.auto.failover.retry_on.timeout', false)"
              :disabled="loading || !value('routing.auto.failover.enabled', true) || !mutable('routing.auto.failover.retry_on.timeout')"
              @update:model-value="setValue('routing.auto.failover.retry_on.timeout', $event)"
            />
          </JfField>

          <div class="sm:col-span-2">
            <fieldset class="grid gap-2">
              <legend class="jf-module-title">可触发重试的 HTTP 状态码（可能重复计费或执行）</legend>
              <div class="flex flex-wrap gap-x-5 gap-y-2">
                <JfCheckbox
                  v-for="code in retryStatusCodeOptions"
                  :key="code"
                  :model-value="retryStatusCodes.includes(code)"
                  :label="String(code)"
                  :disabled="loading || !value('routing.auto.failover.enabled', true) || !mutable('routing.auto.failover.retry_on.status_codes')"
                  @update:model-value="updateRetryStatus(code, $event)"
                />
              </div>
            </fieldset>
          </div>
        </div>

      </div>
    </JfCard>

    <!-- 2. 路由策略与置信度 -->
    <JfCard title="路由策略与置信度">
      <div class="grid gap-6">
        <div class="grid gap-5 sm:grid-cols-2">
          <JfField :label="value('jev.enabled', false) ? 'Jev 选组最低置信度（低于此值使用默认组）' : 'Jev 选组最低置信度（Jev 未启用，暂不生效）'" name="policy-low-confidence">
            <JfSlider
              :model-value="Number(value('routing.policy.low_confidence', 0.45))"
              :min="0"
              :max="1"
              :step="0.01"
              :format-value="v => `${v.toFixed(2)} (${Math.round(v * 100)}%)`"
              :disabled="loading || !mutable('routing.policy.low_confidence')"
              @update:model-value="setDraftValue('routing.policy.low_confidence', Number($event))"
              @change="commitSetting('routing.policy.low_confidence')"
            />
          </JfField>

          <JfField inline label="请求分析不完整时拒绝自动路由" name="policy-refuse-truncated" help="请求超过解析字节数、条目数或嵌套边界时，默认拒绝自动路由。正常的 Jev 文本截取不触发此规则；关闭后可能依据不完整信息筛选模型。">
            <JfSwitch
              :model-value="value('routing.policy.refuse_truncated_evidence', true)"
              :disabled="loading || !mutable('routing.policy.refuse_truncated_evidence')"
              @update:model-value="setValue('routing.policy.refuse_truncated_evidence', $event)"
            />
          </JfField>
        </div>

      </div>
    </JfCard>

    <!-- 3. Jev 任务组选取 -->
    <JfCard title="Jev 任务组推荐">
      <div class="grid gap-6">
        <p class="jf-caption text-ink-secondary">Jev 判断任务复杂度并推荐任务组；实际模型按组内配置顺序选择。发送给 Jev 的文本由本地截取和统计生成，不是完整会话或模型生成的语义摘要。</p>
        <JfAlert
          v-if="!value('jev.enabled', false)"
          tone="info"
          title="Jev 未启用：自动路由使用默认组。启用前需先填写服务地址、模型和认证密钥；启用后仅有一个可用任务组时会跳过推荐。"
        />
        <div class="grid gap-5 sm:grid-cols-2">
          <JfField inline label="启用 Jev 任务组推荐" name="jev-enabled">
            <JfSwitch
              :model-value="value('jev.enabled', false)"
              :disabled="loading || !mutable('jev.enabled')"
              @update:model-value="setValue('jev.enabled', $event)"
            />
          </JfField>

          <JfField label="发送给 Jev 的内容" name="jev-input-mode" :help="inputModeDescription(String(value('jev.input_mode', 'redacted')))">
            <JfSelect
              :model-value="value('jev.input_mode', 'redacted')"
              :items="inputModes"
              :disabled="loading || !mutable('jev.input_mode')"
              class="w-full"
              @update:model-value="setValue('jev.input_mode', $event)"
            />
          </JfField>

          <JfField label="Jev 服务地址" name="jev-base-url">
            <JfInput
              :model-value="value('jev.base_url', '')"
              placeholder="https://jev.example.com"
              class="w-full font-mono"
              :disabled="loading || !mutable('jev.base_url')"
              @update:model-value="setTextValue('jev.base_url', $event)"
              @blur="flushTextValue('jev.base_url')"
            />
          </JfField>

          <JfField label="Jev 使用的模型" name="jev-model">
            <JfInput
              :model-value="value('jev.model', '')"
              placeholder="例如 jev-router-v1"
              class="w-full font-mono"
              :disabled="loading || !mutable('jev.model')"
              @update:model-value="setTextValue('jev.model', $event)"
              @blur="flushTextValue('jev.model')"
            />
          </JfField>

          <JfField
            label="Jev 认证密钥"
            name="jev-api-key"
            class="sm:col-span-2"
          >
            <JfInput
              :model-value="jevKey"
              type="password"
              :placeholder="keyConfigured ? '保持当前密钥不变' : '输入 Jev 认证密钥'"
              class="w-full font-mono"
              :disabled="loading || !mutable('jev.api_key')"
              @update:model-value="setJevKey($event)"
              @blur="flushTextValue('jev.api_key')"
            />
          </JfField>
        </div>

      </div>
    </JfCard>

    <!-- 4. 持久路由日志与审计 -->
    <JfCard title="持久化日志与审计">
      <div class="grid gap-6">
        <div class="grid gap-5 sm:grid-cols-2">
          <JfField inline label="启用持久路由日志" name="log-enabled">
            <JfSwitch
              :model-value="value('routing.log.enabled', true)"
              :disabled="loading || !mutable('routing.log.enabled')"
              @update:model-value="setValue('routing.log.enabled', $event)"
            />
          </JfField>

          <JfField inline label="存储客户端 IP 地址" name="log-store-ip">
            <JfSwitch
              :model-value="value('routing.log.store_client_ip', false)"
              :disabled="loading || !mutable('routing.log.store_client_ip')"
              @update:model-value="setValue('routing.log.store_client_ip', $event)"
            />
          </JfField>

          <JfField label="常规日志留存天数（0 表示永久保留）" name="log-retention">
            <JfInput
              :model-value="value('routing.log.retention_days', 30)"
              type="number"
              min="0"
              max="3650"
              class="w-full jf-tabular"
              :disabled="loading || !mutable('routing.log.retention_days')"
              @update:model-value="setTextValue('routing.log.retention_days', Number($event))"
              @blur="flushTextValue('routing.log.retention_days')"
            />
          </JfField>

          <JfField label="Jev 追踪明细留存天数（0 表示永久保留）" name="log-jev-retention">
            <JfInput
              :model-value="value('routing.log.jev_trace.retention_days', 7)"
              type="number"
              min="0"
              max="3650"
              class="w-full jf-tabular"
              :disabled="loading || !mutable('routing.log.jev_trace.retention_days')"
              @update:model-value="setTextValue('routing.log.jev_trace.retention_days', Number($event))"
              @blur="flushTextValue('routing.log.jev_trace.retention_days')"
            />
          </JfField>
        </div>

      </div>
    </JfCard>

    <!-- 5. 修改管理员密码 -->
    <JfCard title="修改管理员密码">
      <form class="grid gap-5 sm:grid-cols-2" @submit.prevent="changePassword">
        <JfField label="当前管理员密码" name="current-password" required>
          <JfInput
            id="current-password"
            v-model="password.current"
            type="password"
            autocomplete="current-password"
            class="w-full"
            placeholder="输入当前使用的密码"
            required
          />
        </JfField>

        <span class="hidden sm:block" aria-hidden="true" />

        <JfField label="新管理员密码" name="new-password" required>
          <JfInput
            id="new-password"
            v-model="password.next"
            type="password"
            autocomplete="new-password"
            class="w-full"
            placeholder="至少 12 字节（约 12 个英文字符或 4 个汉字）"
            required
          />
        </JfField>

        <JfField label="确认新密码" name="confirm-new-password" required>
          <JfInput
            id="confirm-new-password"
            v-model="password.confirm"
            type="password"
            autocomplete="new-password"
            class="w-full"
            placeholder="再次输入新密码"
            required
          />
        </JfField>

        <ErrorAlert v-if="passwordError" class="sm:col-span-2" :error="passwordError" />

        <div class="jf-action-group justify-end sm:col-span-2">
          <JfButton type="submit" :loading="passwordBusy">更新管理员密码</JfButton>
        </div>
      </form>
    </JfCard>

    <!-- 6. 危险操作区 -->
    <div class="rounded-[var(--jf-radius-control)] border border-danger/20 bg-danger-bg p-5">
      <h3 class="jf-module-title text-danger mb-1">危险操作：重置所有运行时覆盖</h3>
      <JfAlert class="mb-4" tone="warning" title="重置范围：清除全部运行时配置覆盖及 Jev 密钥；不会删除提供商、模型、绑定或推理密钥。" />

      <div class="flex justify-end">
        <JfButton variant="danger-ghost" :disabled="loading || saving" @click="confirmReset">
          重置所有运行时覆盖
        </JfButton>
      </div>
    </div>
  </div>
</template>
