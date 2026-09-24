<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { api, errorMessage, pageURL, type Page, type Provider } from '@/lib/api'
import type { DiscoveredModel, DiscoveredModelsDocument } from '@/lib/admin-contracts'
import type { operations } from '@/lib/generated-api'

type ProviderKind = 'openai' | 'openai_compatible' | 'anthropic' | 'gemini'
type ProviderRow = Provider & { kind?: ProviderKind }
type ProviderDetail = { provider: ProviderRow; pairs: Array<{ upstream_model_id: string }> }

const loading = ref(false)
const providers = ref<ProviderRow[]>([])
const search = ref('')
const enabled = ref('all')
const nextCursor = ref<string | null>(null)
const hasMore = ref(false)
const error = ref('')

const formOpen = ref(false)
const deletingProvider = ref('')
const editing = ref(false)
const saving = ref(false)
const formError = ref('')
const formData = reactive({
  key: '', display_name: '', kind: 'openai_compatible' as ProviderKind,
  base_url: '', api_key: '', enabled: true,
})
const kindOptions = [
  { label: 'OpenAI', value: 'openai' },
  { label: 'OpenAI 兼容', value: 'openai_compatible' },
  { label: 'Anthropic', value: 'anthropic' },
  { label: 'Gemini', value: 'gemini' },
]
const enabledOptions = [
  { label: '全部状态', value: 'all' }, { label: '已启用', value: 'true' }, { label: '已禁用', value: 'false' },
]

async function fetchProviders(reset = true) {
  if (reset) { nextCursor.value = null; providers.value = [] }
  loading.value = true
  error.value = ''
  try {
    const result = await api.get<Page<ProviderRow>>(pageURL('/admin/v1/providers', {
      search: search.value.trim(),
      enabled: enabled.value === 'all' ? '' : enabled.value,
      cursor: nextCursor.value,
      limit: 50,
    }))
    providers.value.push(...result.items)
    nextCursor.value = result.next_cursor
    hasMore.value = result.next_cursor !== null
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    loading.value = false
  }
}

onMounted(() => { void fetchProviders() })

function openCreate() {
  editing.value = false
  formError.value = ''
  Object.assign(formData, { key: '', display_name: '', kind: 'openai_compatible', base_url: '', api_key: '', enabled: true })
  formOpen.value = true
}

async function openEdit(provider: ProviderRow) {
  editing.value = true
  formError.value = ''
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
      key: provider.key, display_name: provider.display_name, kind: provider.kind ?? 'openai_compatible',
      base_url: provider.base_url, api_key: '', enabled: provider.enabled,
    })
  }
  formOpen.value = true
}

async function saveProvider() {
  saving.value = true
  formError.value = ''
  try {
    let createdProvider: ProviderRow | undefined
    const common = {
      display_name: formData.display_name.trim(),
      kind: formData.kind,
      base_url: formData.base_url.trim(),
      enabled: formData.enabled,
    }
    if (editing.value) {
      const payload: Record<string, unknown> = { ...common }
      if (formData.api_key) payload.api_key = formData.api_key
      await api.patch(`/admin/v1/providers/${encodeURIComponent(formData.key)}`, payload)
    } else {
      const result = await api.post<operations['createAdminProvider']['responses'][201]['content']['application/json']>('/admin/v1/providers', { ...common, key: formData.key.trim(), api_key: formData.api_key })
      createdProvider = result
    }
    formOpen.value = false
    formData.api_key = ''
    await fetchProviders(true)
    if (createdProvider) await openModels(createdProvider)
  } catch (cause) {
    formError.value = errorMessage(cause)
  } finally {
    saving.value = false
    formData.api_key = ''
  }
}

const modelsOpen = ref(false)
const modelsLoading = ref(false)
const modelsError = ref('')
const modelsNotice = ref('')
const modelsNoticeColor = ref<'success' | 'warning'>('success')
const activeProvider = ref<ProviderRow>()
const candidateModels = ref<DiscoveredModel[]>([])
const candidateTruncated = ref(false)
const configuredModels = ref<string[]>([])
const manualModel = ref('')
const selectingModel = ref('')

async function openModels(provider: ProviderRow) {
  activeProvider.value = provider
  candidateModels.value = []
  candidateTruncated.value = false
  configuredModels.value = []
  manualModel.value = ''
  modelsError.value = ''
  modelsNotice.value = ''
  modelsOpen.value = true
  modelsLoading.value = true
  const encodedKey = encodeURIComponent(provider.key)
  try {
    const [found, current] = await Promise.all([
      api.get<DiscoveredModelsDocument>(`/admin/v1/providers/${encodedKey}/discover`),
      api.get<ProviderDetail>(`/admin/v1/providers/${encodedKey}`),
    ])
    candidateModels.value = [...new Map((found.items ?? []).filter(model => model.id).map(model => [model.id, model])).values()]
    candidateTruncated.value = found.truncated
    configuredModels.value = [...new Set(current.pairs.map(pair => pair.upstream_model_id).filter(Boolean))]
  } catch (cause) {
    modelsError.value = errorMessage(cause)
  } finally {
    modelsLoading.value = false
  }
}

async function toggleProvider(provider: ProviderRow) {
  try {
    await api.patch(`/admin/v1/providers/${encodeURIComponent(provider.key)}`, { enabled: !provider.enabled })
    await fetchProviders(true)
  } catch (cause) {
    error.value = errorMessage(cause)
  }
}

async function deleteProvider(provider: ProviderRow) {
  if (provider.owner !== 'admin' || deletingProvider.value) return
  const name = provider.display_name || provider.key
  if (!window.confirm(`确定删除提供商“${name}”吗？其模型绑定和路由分组引用也会移除，历史请求日志会保留。`)) return
  deletingProvider.value = provider.key
  error.value = ''
  try {
    await api.delete(`/admin/v1/providers/${encodeURIComponent(provider.key)}`)
    await fetchProviders(true)
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    deletingProvider.value = ''
  }
}

async function selectModel(model: DiscoveredModel) {
  if (!activeProvider.value) return
  selectingModel.value = model.id
  modelsError.value = ''
  modelsNotice.value = ''
  try {
    const result = await api.post<{ metadata_source: string; metadata_applied: boolean; metadata_match: string; metadata_model: string }>(`/admin/v1/providers/${encodeURIComponent(activeProvider.value.key)}/models`, { model: model.id })
    if (!configuredModels.value.includes(model.id)) configuredModels.value.push(model.id)
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
    await fetchProviders(true)
  } catch (cause) {
    modelsError.value = errorMessage(cause)
  } finally {
    selectingModel.value = ''
  }
}

function addManualModel() {
  const model = manualModel.value.trim()
  if (!model) return
  if (!candidateModels.value.some(item => item.id === model)) candidateModels.value.push({ id: model, label: model })
  manualModel.value = ''
}

async function saveModels() {
  modelsOpen.value = false
}

const columns = [
  { accessorKey: 'key', header: '提供商' },
  { accessorKey: 'kind', header: '类型' },
  { accessorKey: 'base_url', header: '端点' },
  { accessorKey: 'api_key_set', header: '密钥' },
  { accessorKey: 'enabled', header: '状态' },
  { accessorKey: 'models', header: '模型' },
  { accessorKey: 'actions', header: '操作' },
]
</script>

<template>
  <div class="space-y-5">
    <section class="flex flex-col justify-between gap-4 sm:flex-row sm:items-center">
      <div class="flex gap-2">
        <UButton color="neutral" variant="outline" icon="i-heroicons-arrow-path" :loading="loading" @click="fetchProviders(true)">刷新</UButton>
        <UButton icon="i-heroicons-plus" @click="openCreate">添加提供商</UButton>
      </div>
    </section>
    <UAlert v-if="error" color="error" variant="soft" :title="error" />
    <UCard class="overflow-hidden">
      <template #header>
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <h3 class="font-semibold">已配置提供商 <UBadge color="neutral" variant="subtle" class="ml-1">{{ providers.length }}</UBadge></h3>
          <div class="flex flex-wrap gap-2">
            <UInput v-model="search" icon="i-heroicons-magnifying-glass" placeholder="搜索提供商" class="w-full sm:w-56" @keydown.enter="fetchProviders(true)" />
            <USelect v-model="enabled" :items="enabledOptions" value-key="value" class="w-36" @update:model-value="fetchProviders(true)" />
          </div>
        </div>
      </template>
      <div class="overflow-x-auto"><UTable :data="providers" :columns="columns" :loading="loading" empty="尚未配置提供商">
        <template #key-cell="{ row }"><div class="font-medium">{{ row.original.display_name || row.original.key }}</div><code class="text-xs text-muted">{{ row.original.key }}</code></template>
        <template #kind-cell="{ row }"><UBadge color="neutral" variant="subtle">{{ row.original.kind || 'OpenAI 兼容' }}</UBadge></template>
        <template #base_url-cell="{ row }"><span class="block max-w-64 truncate font-mono text-xs text-muted" :title="row.original.base_url">{{ row.original.base_url || '—' }}</span></template>
        <template #api_key_set-cell="{ row }"><UBadge :color="row.original.api_key_set ? 'success' : 'warning'" variant="subtle">{{ row.original.api_key_set ? '已配置' : '未配置' }}</UBadge></template>
        <template #enabled-cell="{ row }"><USwitch :model-value="row.original.enabled" :aria-label="`${row.original.enabled ? '禁用' : '启用'} ${row.original.display_name || row.original.key}`" @update:model-value="toggleProvider(row.original)" /></template>
        <template #models-cell="{ row }"><UButton color="neutral" variant="soft" size="sm" @click="openModels(row.original)">选择模型 · {{ row.original.pair_count }}</UButton></template>
        <template #actions-cell="{ row }"><div class="flex items-center"><UButton color="neutral" variant="ghost" size="sm" icon="i-heroicons-pencil-square" @click="openEdit(row.original)">编辑</UButton><UButton v-if="row.original.owner === 'admin'" color="error" variant="ghost" size="sm" icon="i-heroicons-trash" :loading="deletingProvider === row.original.key" :disabled="Boolean(deletingProvider)" :aria-label="`删除 ${row.original.display_name || row.original.key}`" @click="deleteProvider(row.original)">删除</UButton></div></template>
      </UTable></div>
      <div v-if="hasMore" class="flex justify-center border-t border-default p-4"><UButton color="neutral" variant="soft" :loading="loading" @click="fetchProviders(false)">加载更多</UButton></div>
    </UCard>

    <USlideover v-model:open="formOpen" :title="editing ? '编辑提供商' : '添加提供商'" :ui="{ overlay: 'z-[100]', content: 'z-[101]' }">
      <template #body><form class="space-y-4" @submit.prevent="saveProvider">
        <UFormField v-if="!editing" label="提供商标识" required><UInput v-model="formData.key" placeholder="例如：openai" class="w-full" /></UFormField>
        <UFormField label="显示名称"><UInput v-model="formData.display_name" placeholder="例如：OpenAI" class="w-full" /></UFormField>
        <UFormField label="类型" required><USelect v-model="formData.kind" :items="kindOptions" value-key="value" class="w-full" :ui="{ content: 'z-[110]' }" /></UFormField>
        <UFormField label="API 端点" required><UInput v-model="formData.base_url" type="url" placeholder="https://api.example.com/v1" class="w-full" /></UFormField>
        <UFormField :label="editing ? '替换 API 密钥' : 'API 密钥'"><UInput v-model="formData.api_key" type="password" autocomplete="new-password" class="w-full" /></UFormField>
        <UAlert v-if="formError" color="error" variant="soft" :title="formError" />
        <div class="flex justify-end gap-2 pt-3"><UButton color="neutral" variant="ghost" @click="formOpen = false">取消</UButton><UButton type="submit" :loading="saving">{{ editing ? '保存更改' : '添加并选择模型' }}</UButton></div>
      </form></template>
    </USlideover>

    <USlideover v-model:open="modelsOpen" :title="activeProvider ? `模型 · ${activeProvider.display_name || activeProvider.key}` : '选择模型'" :ui="{ overlay: 'z-[100]', content: 'z-[101]' }">
      <template #body>
        <div class="space-y-5">
          <UAlert v-if="modelsError" color="error" variant="soft" :title="modelsError"><template #actions><UButton color="error" variant="ghost" size="sm" :disabled="!activeProvider" @click="activeProvider && openModels(activeProvider)">重试发现</UButton></template></UAlert>
          <UAlert v-if="modelsNotice" :color="modelsNoticeColor" variant="soft" :title="modelsNotice" />
          <div class="flex gap-2">
            <UInput v-model="manualModel" class="min-w-0 flex-1" placeholder="手动输入模型 ID" @keydown.enter.prevent="addManualModel" />
            <UButton color="neutral" variant="outline" :disabled="!manualModel.trim()" @click="addManualModel">添加</UButton>
          </div>
          <div v-if="modelsLoading" class="space-y-3"><USkeleton v-for="n in 5" :key="n" class="h-9 w-full" /></div>
          <div v-else class="space-y-2">
            <p class="text-sm text-muted">发现 {{ candidateModels.length }} 个候选 · 已选择 {{ configuredModels.length }} 个<span v-if="candidateTruncated"> · 结果已截断</span></p>
            <div v-for="model in candidateModels" :key="model.id" class="flex items-center gap-3 rounded-lg border border-default px-3 py-2">
              <div class="min-w-0 flex-1"><div class="truncate text-sm font-medium">{{ model.label || model.id }}</div><code class="block truncate text-xs text-muted">{{ model.id }}</code></div>
              <UButton size="sm" color="neutral" :variant="configuredModels.includes(model.id) ? 'soft' : 'solid'" :loading="selectingModel === model.id" :disabled="Boolean(selectingModel)" @click="selectModel(model)">{{ configuredModels.includes(model.id) ? '更新元数据' : '快速选择' }}</UButton>
            </div>
            <p v-if="!candidateModels.length && !modelsError" class="py-6 text-center text-sm text-muted">没有发现模型，可手动添加。</p>
          </div>
          <div class="flex justify-end border-t border-default pt-4"><UButton color="neutral" variant="ghost" @click="saveModels">完成</UButton></div>
        </div>
      </template>
    </USlideover>
  </div>
</template>
