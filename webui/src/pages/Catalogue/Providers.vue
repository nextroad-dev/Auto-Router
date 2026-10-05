<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import { api, pageURL, type Page, type Provider } from '@/lib/api'
import { createDebouncedSave, createSerialAutosaveQueue } from '@/lib/autosave'
import { errorNotice } from '@/lib/errors'
import { showSavedToast } from '@/lib/save-toast'
import { confirmAction } from '@/lib/confirm'
import ErrorAlert from '@/components/ErrorAlert.vue'
import JfAlert from '@/components/JfAlert.vue'
import JfBadge from '@/components/JfBadge.vue'
import JfButton from '@/components/JfButton.vue'
import JfCard from '@/components/JfCard.vue'
import JfDrawer from '@/components/JfDrawer.vue'
import JfField from '@/components/JfField.vue'
import JfInput from '@/components/JfInput.vue'
import JfSelect from '@/components/JfSelect.vue'
import JfSkeleton from '@/components/JfSkeleton.vue'
import JfSwitch from '@/components/JfSwitch.vue'
import JfTable from '@/components/JfTable.vue'
import type { JfColumn } from '@/lib/table'
import type { DiscoveredModel, DiscoveredModelsDocument } from '@/lib/admin-contracts'
import type { operations } from '@/lib/generated-api'

type ProviderKind = 'openai' | 'openai_compatible' | 'anthropic' | 'gemini'
type ProviderRow = Provider & { kind?: ProviderKind }
type ProviderPair = { model: string; upstream_model_id: string; enabled: boolean }
type ProviderDetail = { provider: ProviderRow; pairs: ProviderPair[] }

const loading = ref(false)
const loadingMore = ref(false)
const providers = ref<ProviderRow[]>([])
const search = ref('')
const enabled = ref('all')
const nextCursor = ref<string | null>(null)
const hasMore = ref(false)
const error = ref<unknown>()

const formOpen = ref(false)
const deletingProvider = ref('')
const editing = ref(false)
const saving = ref(false)
const formError = ref<unknown>()
const formData = reactive({
  key: '',
  display_name: '',
  kind: 'openai_compatible' as ProviderKind,
  base_url: '',
  api_key: '',
  enabled: true,
})

type ProviderFormField = 'display_name' | 'kind' | 'base_url' | 'api_key'
type ProviderFormSave = {
  key: string
  body: Record<string, unknown>
  versions: Map<ProviderFormField, number>
  apiKeySnapshot?: string
}
const formDirty = new Set<ProviderFormField>()
const formVersions = new Map<ProviderFormField, number>()
const formTextSaves = new Map<ProviderFormField, ReturnType<typeof createDebouncedSave>>()
const formSaveQueue = createSerialAutosaveQueue<ProviderFormSave>(persistProviderForm, (_current, next) => next)

const kindOptions = [
  { label: 'OpenAI', value: 'openai' },
  { label: 'OpenAI 兼容', value: 'openai_compatible' },
  { label: 'Anthropic', value: 'anthropic' },
  { label: 'Gemini', value: 'gemini' },
]

const enabledOptions = [
  { label: '全部状态', value: 'all' },
  { label: '已启用', value: 'true' },
  { label: '已停用', value: 'false' },
]

function resetProviderAutosave() {
  for (const saver of formTextSaves.values()) saver.cancel()
  formTextSaves.clear()
  formDirty.clear()
  formVersions.clear()
}

function providerFieldSaver(field: ProviderFormField) {
  let saver = formTextSaves.get(field)
  if (!saver) {
    saver = createDebouncedSave(() => enqueueProviderFormSave())
    formTextSaves.set(field, saver)
  }
  return saver
}

function updateProviderField(field: ProviderFormField, value: string) {
  formData[field] = value as never
  formError.value = undefined
  if (!editing.value) return
  if (field === 'api_key' && !value.trim()) {
    formTextSaves.get(field)?.cancel()
    formDirty.delete(field)
    formVersions.set(field, (formVersions.get(field) ?? 0) + 1)
    return
  }
  formDirty.add(field)
  formVersions.set(field, (formVersions.get(field) ?? 0) + 1)
  if (field === 'kind') enqueueProviderFormSave()
  else providerFieldSaver(field).schedule()
}

function flushProviderField(field: ProviderFormField) {
  void providerFieldSaver(field).flush()
}

function enqueueProviderFormSave() {
  if (!editing.value || !formDirty.size) return
  if (formDirty.has('display_name') && !formData.display_name.trim()) {
    formError.value = '提供商显示名称不能为空。'
    return
  }
  const body: Record<string, unknown> = {}
  if (formDirty.has('display_name')) body.display_name = formData.display_name.trim()
  if (formDirty.has('kind')) body.kind = formData.kind
  if (formDirty.has('base_url')) body.base_url = formData.base_url.trim()
  if (formDirty.has('api_key') && formData.api_key.trim()) body.api_key = formData.api_key.trim()
  if (!Object.keys(body).length) return
  const versions = new Map([...formDirty].map(field => [field, formVersions.get(field) ?? 0]))
  formSaveQueue.enqueue({
    key: formData.key,
    body,
    versions,
    ...(typeof body.api_key === 'string' ? { apiKeySnapshot: body.api_key } : {}),
  })
}

async function persistProviderForm(task: ProviderFormSave) {
  saving.value = true
  try {
    await api.patch(`/admin/v1/providers/${encodeURIComponent(task.key)}`, task.body)
    if (formData.key === task.key) {
      for (const [field, version] of task.versions) {
        if (formVersions.get(field) !== version) continue
        formDirty.delete(field)
        if (field === 'api_key' && formData.api_key.trim() === task.apiKeySnapshot) formData.api_key = ''
      }
      formError.value = undefined
    }
    const row = providers.value.find(provider => provider.key === task.key)
    if (row) {
      if (typeof task.body.display_name === 'string') row.display_name = task.body.display_name
      if (typeof task.body.kind === 'string') row.kind = task.body.kind as ProviderKind
      if (typeof task.body.base_url === 'string') row.base_url = task.body.base_url
      if (typeof task.body.api_key === 'string') row.api_key_set = true
    }
    showSavedToast()
  } catch (cause) {
    if (formData.key === task.key) formError.value = errorNotice(cause)
  } finally {
    saving.value = false
  }
}

async function fetchProviders(reset = true) {
  if (loading.value || loadingMore.value) return
  if (!reset && (!hasMore.value || !nextCursor.value)) return

  if (reset) {
    nextCursor.value = null
    hasMore.value = false
    providers.value = []
    loading.value = true
  } else {
    loadingMore.value = true
  }
  error.value = undefined
  try {
    const result = await api.get<Page<ProviderRow>>(pageURL('/admin/v1/providers', {
      search: search.value.trim(),
      enabled: enabled.value === 'all' ? '' : enabled.value,
      cursor: reset ? null : nextCursor.value,
      limit: 50,
    }))
    providers.value.push(...result.items)
    nextCursor.value = result.next_cursor
    hasMore.value = result.next_cursor !== null
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    if (reset) loading.value = false
    else loadingMore.value = false
  }
}

onMounted(() => { void fetchProviders() })

// Search runs as the operator types, like the model page's filter; Enter still runs it at once.
let searchTimer: ReturnType<typeof setTimeout> | undefined
function scheduleSearch(delay = 300) {
  if (searchTimer !== undefined) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    searchTimer = undefined
    if (loading.value || loadingMore.value) scheduleSearch(100)
    else void fetchProviders(true)
  }, delay)
}
function searchNow() {
  if (searchTimer !== undefined) clearTimeout(searchTimer)
  searchTimer = undefined
  scheduleSearch(0)
}
watch(search, () => scheduleSearch())

function openCreate() {
  resetProviderAutosave()
  editing.value = false
  formError.value = undefined
  Object.assign(formData, {
    key: '',
    display_name: '',
    kind: 'openai_compatible',
    base_url: '',
    api_key: '',
    enabled: true,
  })
  formOpen.value = true
}

async function openEdit(provider: ProviderRow) {
  resetProviderAutosave()
  editing.value = true
  formError.value = undefined
  try {
    const detail = await api.get<{ provider: ProviderRow }>(`/admin/v1/providers/${encodeURIComponent(provider.key)}`)
    Object.assign(formData, {
      key: provider.key,
      display_name: detail.provider.display_name,
      kind: detail.provider.kind ?? 'openai_compatible',
      base_url: detail.provider.base_url,
      api_key: '',
      enabled: detail.provider.enabled,
    })
  } catch {
    Object.assign(formData, {
      key: provider.key,
      display_name: provider.display_name,
      kind: provider.kind ?? 'openai_compatible',
      base_url: provider.base_url,
      api_key: '',
      enabled: provider.enabled,
    })
  }
  formOpen.value = true
}

async function toggleProvider(provider: ProviderRow) {
  const previous = provider.enabled
  const next = !previous
  provider.enabled = next
  try {
    await api.patch(`/admin/v1/providers/${encodeURIComponent(provider.key)}`, { enabled: next })
    showSavedToast()
  } catch (cause) {
    provider.enabled = previous
    error.value = errorNotice(cause)
  }
}

async function deleteProvider(provider: ProviderRow) {
  const confirmed = await confirmAction({
    title: `删除提供商“${provider.display_name || provider.key}”？`,
    description: '它的全部模型绑定会一并解除，并从所有模型分组中移除。此操作不可撤销。',
    confirmLabel: '删除提供商',
    danger: true,
  })
  if (!confirmed) return
  deletingProvider.value = provider.key
  try {
    await api.delete(`/admin/v1/providers/${encodeURIComponent(provider.key)}`)
    showSavedToast(`已删除提供商：${provider.display_name || provider.key}`)
    await fetchProviders(true)
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    deletingProvider.value = ''
  }
}

async function saveProvider() {
  formError.value = undefined
  if (editing.value) {
    for (const saver of formTextSaves.values()) void saver.flush()
    enqueueProviderFormSave()
    return
  }
  const key = formData.key.trim()
  if (!key) {
    formError.value = '提供商标识不能为空。'
    return
  }
  saving.value = true
  let createdProvider: ProviderRow | undefined
  try {
    const common = {
      display_name: formData.display_name.trim(),
      kind: formData.kind,
      base_url: formData.base_url.trim(),
      enabled: formData.enabled,
    }
    const result = await api.post<operations['createAdminProvider']['responses'][201]['content']['application/json']>(
      '/admin/v1/providers',
      { ...common, key, api_key: formData.api_key },
    )
    createdProvider = result
    formOpen.value = false
    formData.api_key = ''
    await fetchProviders(true)
    if (createdProvider) await openModels(createdProvider)
  } catch (cause) {
    formError.value = errorNotice(cause)
  } finally {
    saving.value = false
    formData.api_key = ''
  }
}

watch(formOpen, isOpen => {
  if (!isOpen && editing.value) {
    for (const saver of formTextSaves.values()) void saver.flush()
  }
})

const modelsOpen = ref(false)
const modelsLoading = ref(false)
const modelsError = ref<unknown>()
const modelsNotice = ref('')
const modelsNoticeColor = ref<'success' | 'warning'>('success')
const activeProvider = ref<ProviderRow>()
const candidateModels = ref<DiscoveredModel[]>([])
const candidateTruncated = ref(false)
const configuredPairs = ref<ProviderPair[]>([])
const manualModel = ref('')
const selectingModel = ref('')
const deletingModel = ref('')

async function openModels(provider: ProviderRow) {
  activeProvider.value = provider
  candidateModels.value = []
  candidateTruncated.value = false
  configuredPairs.value = []
  manualModel.value = ''
  modelsError.value = undefined
  modelsNotice.value = ''
  modelsOpen.value = true
  modelsLoading.value = true
  const encodedKey = encodeURIComponent(provider.key)
  try {
    const [discovery, detail] = await Promise.allSettled([
      api.get<DiscoveredModelsDocument>(`/admin/v1/providers/${encodedKey}/discover`),
      api.get<ProviderDetail>(`/admin/v1/providers/${encodedKey}`),
    ])
    if (discovery.status === 'fulfilled') {
      const found = discovery.value
      candidateModels.value = [...new Map((found.items ?? []).filter(model => model.id).map(model => [model.id, model])).values()]
      candidateTruncated.value = found.truncated
    } else {
      modelsError.value = errorNotice(discovery.reason)
    }
    if (detail.status === 'fulfilled') {
      configuredPairs.value = detail.value.pairs
    } else {
      modelsError.value ??= errorNotice(detail.reason)
    }
  } finally {
    modelsLoading.value = false
  }
}

/** The existing binding for a discovered upstream ID, whether or not it is enabled. */
function configuredPair(upstreamId: string) {
  return configuredPairs.value.find(pair => pair.upstream_model_id === upstreamId)
}

async function enableModelPair(pair: ProviderPair) {
  const provider = activeProvider.value
  if (!provider || selectingModel.value || deletingModel.value) return
  selectingModel.value = pair.upstream_model_id
  modelsError.value = undefined
  modelsNotice.value = ''
  try {
    await api.patch(`/admin/v1/pairs/${encodeURIComponent(provider.key)}/${encodeURIComponent(pair.model)}`, { enabled: true })
    pair.enabled = true
    showSavedToast()
  } catch (cause) {
    modelsError.value = errorNotice(cause)
  } finally {
    selectingModel.value = ''
  }
}

async function addModelPair(upstreamId: string) {
  if (!activeProvider.value || !upstreamId.trim() || deletingModel.value) return
  selectingModel.value = upstreamId
  modelsError.value = undefined
  modelsNotice.value = ''
  try {
    const result = await api.post<operations['selectProviderModel']['responses'][200]['content']['application/json']>(
      `/admin/v1/providers/${encodeURIComponent(activeProvider.value.key)}/models`,
      { model: upstreamId.trim() },
    )
    const existing = configuredPairs.value.find(pair => pair.model === result.model)
    if (existing) {
      existing.upstream_model_id = upstreamId.trim()
      existing.enabled = true
    } else {
      configuredPairs.value.push({ model: result.model, upstream_model_id: upstreamId.trim(), enabled: true })
    }
    if (result.metadata_source === 'models.dev') {
      modelsNoticeColor.value = 'success'
      modelsNotice.value = `已从 models.dev 更新能力信息（${result.metadata_match} 匹配：${result.metadata_model}）。`
    } else if (result.metadata_source === 'admin-override') {
      modelsNoticeColor.value = 'success'
      modelsNotice.value = '检测到 models.dev 元数据，但此绑定已有管理员编辑，已保留现有值。'
    } else {
      modelsNoticeColor.value = 'warning'
      modelsNotice.value = 'models.dev 中未找到唯一匹配；此模型使用保守默认值，请到“模型管理”核对并编辑能力信息。'
    }
    manualModel.value = ''
    await fetchProviders(true)
  } catch (cause) {
    modelsError.value = errorNotice(cause)
  } finally {
    selectingModel.value = ''
  }
}

async function removeModelPair(pair: ProviderPair) {
  const provider = activeProvider.value
  if (!provider || deletingModel.value) return
  const confirmed = await confirmAction({
    title: `解除“${provider.display_name || provider.key}”与“${pair.model}”的绑定？`,
    description: '该绑定会从所有模型分组中移除，后续注册表同步也不会自动恢复。',
    confirmLabel: '解除绑定',
    danger: true,
  })
  if (!confirmed) return

  deletingModel.value = pair.model
  modelsError.value = undefined
  modelsNotice.value = ''
  try {
    await api.delete<operations['deleteAdminPair']['responses'][200]['content']['application/json']>(
      `/admin/v1/pairs/${encodeURIComponent(provider.key)}/${encodeURIComponent(pair.model)}`,
    )
    configuredPairs.value = configuredPairs.value.filter(item => item.model !== pair.model)
    const providerRow = providers.value.find(row => row.key === provider.key)
    if (providerRow) providerRow.pair_count = Math.max(0, providerRow.pair_count - 1)
    modelsNotice.value = `已永久解除绑定：${pair.model}`
    modelsNoticeColor.value = 'success'
    await fetchProviders(true)
  } catch (cause) {
    modelsError.value = errorNotice(cause)
  } finally {
    deletingModel.value = ''
  }
}

const columns: JfColumn[] = [
  { key: 'key', title: '提供商', width: '14rem' },
  { key: 'kind', title: '类型' },
  { key: 'base_url', title: '端点地址' },
  { key: 'api_key_set', title: '上游密钥' },
  { key: 'enabled', title: '状态', nowrap: true },
  { key: 'models', title: '模型绑定' },
  { key: 'actions', title: '操作', nowrap: true },
]
</script>

<template>
  <div class="jf-stack">
    <!-- Toolbar -->
    <section class="jf-toolbar">
      <div>
        <h1 class="jf-page-title">提供商</h1>
      </div>
      <div class="jf-action-group">
        <JfButton variant="secondary" icon="arrow-path" :loading="loading" @click="fetchProviders(true)">刷新</JfButton>
        <JfButton icon="plus" @click="openCreate">添加提供商</JfButton>
      </div>
    </section>

    <ErrorAlert v-if="error" :error="error" />

    <!-- Providers Table Card -->
    <JfCard flush>
      <template #header>
        <div class="flex items-center gap-2">
          <h2 class="jf-section-title">已配置提供商</h2>
          <JfBadge tone="neutral">{{ providers.length }}</JfBadge>
        </div>
      </template>

      <template #actions>
        <div class="jf-action-group">
          <JfInput
            v-model="search"
            icon="magnifying-glass"
            placeholder="搜索提供商名称或标识"
            aria-label="搜索提供商名称或标识"
            class="w-48 sm:w-60"
            @keydown.enter="searchNow"
          />
          <JfSelect
            v-model="enabled"
            :items="enabledOptions"
            label="提供商状态筛选"
            :disabled="loading || loadingMore"
            class="w-32"
            @update:model-value="fetchProviders(true)"
          />
        </div>
      </template>

      <div class="jf-scroll-x">
        <JfTable :rows="providers" :columns="columns" :loading="loading" empty-text="尚未配置任何提供商">
          <template #cell-key="{ row }">
            <div class="max-w-56 jf-anywhere font-medium" :title="row.display_name || row.key">{{ row.display_name || row.key }}</div>
            <code class="block max-w-56 jf-anywhere jf-caption font-mono text-ink-secondary" :title="row.key">{{ row.key }}</code>
          </template>

          <template #cell-kind="{ row }">
            <JfBadge tone="neutral" class="jf-nowrap">
              {{ kindOptions.find(k => k.value === (row.kind ?? 'openai_compatible'))?.label || 'OpenAI 兼容' }}
            </JfBadge>
          </template>

          <template #cell-base_url="{ row }">
            <span class="jf-caption font-mono text-ink-secondary jf-anywhere">{{ row.base_url || '—' }}</span>
          </template>

          <template #cell-api_key_set="{ row }">
            <JfBadge :tone="row.api_key_set ? 'success' : 'warning'" class="jf-nowrap">
              {{ row.api_key_set ? '已配置' : '未配置' }}
            </JfBadge>
          </template>

          <template #cell-enabled="{ row }">
            <div class="flex items-center gap-2">
              <JfSwitch
                :model-value="row.enabled"
                :aria-label="`${row.enabled ? '停用' : '启用'}提供商 ${row.display_name || row.key}`"
                @update:model-value="toggleProvider(row)"
              />
              <span class="jf-caption font-medium select-none" :class="row.enabled ? 'text-ink' : 'text-ink-secondary'">
                {{ row.enabled ? '已启用' : '已停用' }}
              </span>
            </div>
          </template>

          <template #cell-models="{ row }">
            <JfButton variant="secondary" size="sm" @click="openModels(row)">
              模型绑定 · {{ row.pair_count }}
            </JfButton>
          </template>

          <template #cell-actions="{ row }">
            <div class="jf-action-group justify-end">
              <JfButton variant="ghost" size="sm" icon="pencil-square" @click="openEdit(row)">编辑</JfButton>
              <JfButton
                v-if="row.owner === 'admin'"
                variant="danger-ghost"
                size="sm"
                icon="trash"
                :loading="deletingProvider === row.key"
                :disabled="Boolean(deletingProvider)"
                :aria-label="`删除 ${row.display_name || row.key}`"
                @click="deleteProvider(row)"
              >
                删除
              </JfButton>
              <span v-else title="同步或配置文件来源的提供商不可删除，可停用">
                <JfButton
                  variant="danger-ghost"
                  size="sm"
                  icon="trash"
                  disabled
                  :aria-label="`删除 ${row.display_name || row.key}（同步或配置文件来源的提供商不可删除，可停用）`"
                >
                  删除
                </JfButton>
              </span>
            </div>
          </template>
        </JfTable>
      </div>

      <template v-if="hasMore" #footer>
        <div class="flex justify-center">
          <JfButton
            variant="secondary"
            :loading="loadingMore"
            :disabled="loading"
            @click="fetchProviders(false)"
          >
            加载更多提供商
          </JfButton>
        </div>
      </template>
    </JfCard>

    <!-- Create / Edit Provider Drawer -->
    <JfDrawer v-model:open="formOpen" :title="editing ? '编辑提供商' : '添加提供商'">
      <form class="grid gap-5" @submit.prevent="saveProvider">
        <JfField label="提供商标识" name="provider-key" required>
          <JfInput
            v-model="formData.key"
            :disabled="editing || saving"
            placeholder="例如 deepseek"
            class="w-full font-mono"
            required
          />
        </JfField>

        <JfField label="显示名称" name="provider-name" required>
          <JfInput
            :model-value="formData.display_name"
            :disabled="!editing && saving"
            placeholder="例如 DeepSeek 官方"
            class="w-full"
            required
            @update:model-value="updateProviderField('display_name', $event)"
            @blur="flushProviderField('display_name')"
          />
        </JfField>

        <JfField label="协议类型" name="provider-kind">
          <JfSelect
            :model-value="formData.kind"
            :items="kindOptions"
            :disabled="!editing && saving"
            class="w-full"
            @update:model-value="updateProviderField('kind', String($event))"
          />
        </JfField>

        <JfField label="API 端点地址" name="provider-endpoint">
          <JfInput
            :model-value="formData.base_url"
            :disabled="!editing && saving"
            placeholder="https://api.example.com/v1"
            class="w-full font-mono"
            @update:model-value="updateProviderField('base_url', $event)"
            @blur="flushProviderField('base_url')"
          />
        </JfField>

        <JfField
          label="上游密钥"
          name="provider-apikey"
        >
          <JfInput
            :model-value="formData.api_key"
            type="password"
            :disabled="!editing && saving"
            :placeholder="editing ? '保持当前密钥不变' : 'sk-...'"
            class="w-full font-mono"
            @update:model-value="updateProviderField('api_key', $event)"
            @blur="flushProviderField('api_key')"
          />
        </JfField>

        <JfField v-if="!editing" label="启用该提供商" inline>
          <JfSwitch v-model="formData.enabled" :disabled="saving" />
        </JfField>

        <ErrorAlert v-if="formError" :error="formError" />
      </form>

      <template #footer>
        <div class="jf-action-group justify-end">
          <JfButton v-if="editing" variant="secondary" @click="formOpen = false">完成</JfButton>
          <template v-else>
            <JfButton variant="ghost" :disabled="saving" @click="formOpen = false">取消</JfButton>
            <JfButton :loading="saving" @click="saveProvider">创建提供商</JfButton>
          </template>
        </div>
      </template>
    </JfDrawer>

    <!-- Model Discovery & Binding Drawer -->
    <JfDrawer v-model:open="modelsOpen" :title="`管理模型绑定 · ${activeProvider?.display_name || activeProvider?.key}`" size="lg">
      <div class="grid gap-5">
        <ErrorAlert v-if="modelsError" :error="modelsError" />
        <JfAlert v-if="modelsNotice" :tone="modelsNoticeColor" :title="modelsNotice" />

        <!-- Manual addition row -->
        <div class="rounded-[var(--jf-radius-control)] border border-line p-4">
          <h3 class="jf-module-title mb-2">手动绑定模型</h3>
          <div class="flex gap-2">
            <JfInput
              v-model="manualModel"
              placeholder="输入上游模型 ID，例如 gpt-4o"
              aria-label="输入上游模型 ID"
              class="min-w-0 grow font-mono"
              @keydown.enter.prevent="addModelPair(manualModel)"
            />
            <JfButton
              variant="secondary"
              :disabled="!manualModel.trim() || Boolean(selectingModel)"
              :loading="selectingModel === manualModel.trim()"
              @click="addModelPair(manualModel)"
            >
              绑定
            </JfButton>
          </div>
        </div>

        <div v-if="configuredPairs.length">
          <div class="flex items-center justify-between mb-2">
            <h3 class="jf-module-title">当前模型绑定</h3>
            <JfBadge tone="neutral">{{ configuredPairs.length }}</JfBadge>
          </div>
          <div class="grid gap-2">
            <div
              v-for="pair in configuredPairs"
              :key="pair.model"
              class="flex min-w-0 flex-col items-stretch justify-between gap-3 rounded-[var(--jf-radius-control)] bg-tonal px-3.5 py-2.5 sm:flex-row sm:items-center"
            >
              <div class="min-w-0 flex-1">
                <code class="font-mono font-medium block jf-anywhere" :title="pair.model">{{ pair.model }}</code>
                <span class="jf-caption text-ink-secondary block jf-anywhere">上游 ID：{{ pair.upstream_model_id }}</span>
              </div>
              <div class="jf-action-group shrink-0">
                <JfBadge :tone="pair.enabled ? 'success' : 'neutral'">{{ pair.enabled ? '已启用' : '已停用' }}</JfBadge>
                <JfButton
                  size="sm"
                  variant="danger-ghost"
                  :loading="deletingModel === pair.model"
                  :disabled="Boolean(deletingModel) || Boolean(selectingModel)"
                  :aria-label="`解除绑定 ${pair.model}`"
                  @click="removeModelPair(pair)"
                >
                  解除绑定
                </JfButton>
              </div>
            </div>
          </div>
        </div>

        <!-- Discovered models list -->
        <div>
          <div class="flex items-center justify-between mb-2">
            <h3 class="jf-module-title">发现的上游模型</h3>
            <JfBadge tone="neutral">{{ candidateModels.length }}</JfBadge>
          </div>

          <div v-if="modelsLoading" class="grid gap-2" aria-busy="true">
            <JfSkeleton v-for="n in 5" :key="n" height="40px" shape="block" />
          </div>

          <div v-else-if="candidateModels.length" class="grid gap-2 max-h-[460px] overflow-y-auto pr-1">
            <div
              v-for="model in candidateModels"
              :key="model.id"
              class="flex min-w-0 flex-col items-stretch justify-between gap-3 rounded-[var(--jf-radius-control)] bg-tonal px-3.5 py-2.5 sm:flex-row sm:items-center"
            >
              <div class="min-w-0 flex-1">
                <code class="font-mono font-medium block jf-anywhere" :title="model.id">{{ model.id }}</code>
                <span v-if="model.label" class="jf-caption text-ink-secondary block jf-anywhere">{{ model.label }}</span>
              </div>
              <div class="jf-action-group shrink-0">
                <JfBadge v-if="configuredPair(model.id)?.enabled" tone="success">已绑定</JfBadge>
                <template v-else-if="configuredPair(model.id)">
                  <JfBadge tone="neutral">已绑定 · 已停用</JfBadge>
                  <JfButton
                    size="sm"
                    variant="ghost"
                    :loading="selectingModel === model.id"
                    :disabled="Boolean(selectingModel) || Boolean(deletingModel)"
                    @click="enableModelPair(configuredPair(model.id)!)"
                  >
                    重新启用
                  </JfButton>
                </template>
                <JfButton
                  v-else
                  size="sm"
                  variant="ghost"
                  :loading="selectingModel === model.id"
                  @click="addModelPair(model.id)"
                >
                  绑定
                </JfButton>
              </div>
            </div>
          </div>

          <p v-else class="py-6 text-center text-sm text-ink-secondary">未发现可用模型</p>
        </div>
      </div>

      <template #footer>
        <div class="jf-action-group justify-end">
          <JfButton variant="secondary" @click="modelsOpen = false">完成</JfButton>
        </div>
      </template>
    </JfDrawer>
  </div>
</template>
