<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { api, errorMessage, getAllPages, type Model, type Pair } from '@/lib/api'

const loading = ref(false)
const saving = ref(false)
const error = ref('')
const search = ref('')
const models = ref<Model[]>([])
const pairs = ref<Pair[]>([])

const modelEditorOpen = ref(false)
const pairEditorOpen = ref(false)
const modelError = ref('')
const pairError = ref('')
const modelForm = reactive({ id: '', display_name: '', enabled: true, priority: 0 })
const pairForm = reactive({
  provider: '', model: '', upstream_model_id: '', context_window: 32768,
  max_output: '', supports_tools: false, supports_vision: false,
  supports_audio_input: false, supports_reasoning: false, enabled: true, priority: 0,
})

const visibleModels = computed(() => {
  const term = search.value.trim().toLowerCase()
  if (!term) return models.value
  return models.value.filter(item => `${item.id} ${item.display_name}`.toLowerCase().includes(term))
})
const visiblePairs = computed(() => {
  const term = search.value.trim().toLowerCase()
  if (!term) return pairs.value
  return pairs.value.filter(item => `${item.provider} ${item.model} ${item.upstream_model_id}`.toLowerCase().includes(term))
})

const modelColumns = [
  { accessorKey: 'id', header: '模型 ID' },
  { accessorKey: 'display_name', header: '名称' },
  { accessorKey: 'enabled', header: '状态' },
  { accessorKey: 'priority', header: '优先级' },
  { accessorKey: 'pair_count', header: '绑定数' },
  { accessorKey: 'actions', header: '操作' },
]
const pairColumns = [
  { accessorKey: 'provider', header: '提供商' },
  { accessorKey: 'model', header: '逻辑模型' },
  { accessorKey: 'upstream_model_id', header: '上游模型 ID' },
  { accessorKey: 'context_window', header: '上下文' },
  { accessorKey: 'capabilities', header: '能力' },
  { accessorKey: 'enabled', header: '状态' },
  { accessorKey: 'source', header: '元数据来源' },
  { accessorKey: 'actions', header: '操作' },
]

async function loadData() {
  loading.value = true
  error.value = ''
  try {
    const [modelRows, pairRows] = await Promise.all([
      getAllPages<Model>('/admin/v1/models'),
      getAllPages<Pair>('/admin/v1/pairs'),
    ])
    models.value = modelRows
    pairs.value = pairRows
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    loading.value = false
  }
}

function editModel(model: Model) {
  modelError.value = ''
  Object.assign(modelForm, { id: model.id, display_name: model.display_name, enabled: model.enabled, priority: model.priority })
  modelEditorOpen.value = true
}

async function saveModel() {
  saving.value = true
  modelError.value = ''
  try {
    await api.patch(`/admin/v1/models/${encodeURIComponent(modelForm.id)}`, {
      display_name: modelForm.display_name.trim(), enabled: modelForm.enabled, priority: Number(modelForm.priority),
    })
    modelEditorOpen.value = false
    await loadData()
  } catch (cause) {
    modelError.value = errorMessage(cause)
  } finally {
    saving.value = false
  }
}

function editPair(pair: Pair) {
  pairError.value = ''
  Object.assign(pairForm, {
    provider: pair.provider, model: pair.model, upstream_model_id: pair.upstream_model_id,
    context_window: pair.context_window, max_output: pair.max_output === null ? '' : String(pair.max_output),
    supports_tools: pair.supports_tools, supports_vision: pair.supports_vision,
    supports_audio_input: pair.supports_audio_input, supports_reasoning: pair.supports_reasoning,
    enabled: pair.enabled, priority: pair.priority,
  })
  pairEditorOpen.value = true
}

async function savePair() {
  const contextWindow = Number(pairForm.context_window)
  const maxOutput = pairForm.max_output.trim() === '' ? null : Number(pairForm.max_output)
  if (!Number.isInteger(contextWindow) || contextWindow <= 0) {
    pairError.value = '上下文窗口必须是正整数。'
    return
  }
  if (maxOutput !== null && (!Number.isInteger(maxOutput) || maxOutput <= 0 || maxOutput > contextWindow)) {
    pairError.value = '最大输出必须是正整数，且不能大于上下文窗口。'
    return
  }
  saving.value = true
  pairError.value = ''
  try {
    await api.patch(`/admin/v1/pairs/${encodeURIComponent(pairForm.provider)}/${encodeURIComponent(pairForm.model)}`, {
      upstream_model_id: pairForm.upstream_model_id.trim(),
      context_window: contextWindow,
      max_output: maxOutput,
      supports_tools: pairForm.supports_tools,
      supports_vision: pairForm.supports_vision,
      supports_audio_input: pairForm.supports_audio_input,
      supports_reasoning: pairForm.supports_reasoning,
      enabled: pairForm.enabled,
      priority: Number(pairForm.priority),
    })
    pairEditorOpen.value = false
    await loadData()
  } catch (cause) {
    pairError.value = errorMessage(cause)
  } finally {
    saving.value = false
  }
}

function capabilities(pair: Pair) {
  const values: string[] = []
  if (pair.supports_tools) values.push('工具')
  if (pair.supports_vision) values.push('图片')
  if (pair.supports_audio_input) values.push('音频')
  if (pair.supports_reasoning) values.push('推理')
  return values.length ? values.join(' · ') : '纯文本'
}

onMounted(() => { void loadData() })
</script>

<template>
  <div class="space-y-5">
    <section class="flex flex-col justify-between gap-3 sm:flex-row sm:items-end">
      <p class="text-sm text-muted">管理逻辑模型及各提供商绑定的上游 ID、上下文和能力元数据。</p>
      <div class="flex gap-2">
        <UInput v-model="search" icon="i-heroicons-magnifying-glass" placeholder="搜索模型或提供商" class="w-full sm:w-64" />
        <UButton color="neutral" variant="outline" icon="i-heroicons-arrow-path" :loading="loading" @click="loadData">刷新</UButton>
      </div>
    </section>
    <UAlert v-if="error" color="error" variant="soft" :title="error" />

    <UCard class="overflow-hidden">
      <template #header><div class="flex items-center justify-between"><h2 class="font-semibold">逻辑模型</h2><UBadge color="neutral" variant="subtle">{{ visibleModels.length }}</UBadge></div></template>
      <div class="overflow-x-auto">
        <UTable :data="visibleModels" :columns="modelColumns" :loading="loading" empty="没有匹配的模型">
          <template #id-cell="{ row }"><code class="font-mono text-xs">{{ row.original.id }}</code></template>
          <template #display_name-cell="{ row }"><div class="font-medium">{{ row.original.display_name }}</div><UBadge color="neutral" variant="subtle" class="mt-1">{{ row.original.owner === 'admin' ? '本地管理' : row.original.source }}</UBadge></template>
          <template #enabled-cell="{ row }"><UBadge :color="row.original.enabled ? 'success' : 'neutral'" variant="subtle">{{ row.original.enabled ? '启用' : '停用' }}</UBadge></template>
          <template #actions-cell="{ row }"><UButton color="neutral" variant="ghost" size="sm" icon="i-heroicons-pencil-square" @click="editModel(row.original)">编辑</UButton></template>
        </UTable>
      </div>
    </UCard>

    <UCard class="overflow-hidden">
      <template #header><div class="flex items-center justify-between"><div><h2 class="font-semibold">Provider 模型绑定与能力</h2><p class="mt-1 text-xs text-muted">能力用于路由硬筛选；未知能力应保持关闭，确认上游支持后再开启。</p></div><UBadge color="neutral" variant="subtle">{{ visiblePairs.length }}</UBadge></div></template>
      <div class="overflow-x-auto">
        <UTable :data="visiblePairs" :columns="pairColumns" :loading="loading" empty="没有匹配的模型绑定">
          <template #provider-cell="{ row }"><span class="font-medium">{{ row.original.provider }}</span></template>
          <template #model-cell="{ row }"><code class="font-mono text-xs">{{ row.original.model }}</code></template>
          <template #upstream_model_id-cell="{ row }"><code class="block max-w-64 truncate font-mono text-xs" :title="row.original.upstream_model_id">{{ row.original.upstream_model_id }}</code></template>
          <template #context_window-cell="{ row }"><span class="tabular-nums">{{ row.original.context_window.toLocaleString() }}</span><span v-if="row.original.max_output" class="block text-[11px] text-muted">输出 ≤ {{ row.original.max_output.toLocaleString() }}</span></template>
          <template #capabilities-cell="{ row }"><span class="text-xs">{{ capabilities(row.original) }}</span></template>
          <template #enabled-cell="{ row }"><UBadge :color="row.original.enabled ? 'success' : 'neutral'" variant="subtle">{{ row.original.enabled ? '启用' : '停用' }}</UBadge></template>
          <template #source-cell="{ row }"><UBadge :color="row.original.source === 'modelsdev' ? 'primary' : 'neutral'" variant="subtle">{{ row.original.source === 'modelsdev' ? 'models.dev' : '本地' }}<span v-if="row.original.owner === 'admin'"> · 已编辑</span></UBadge></template>
          <template #actions-cell="{ row }"><UButton color="neutral" variant="ghost" size="sm" icon="i-heroicons-pencil-square" @click="editPair(row.original)">编辑能力</UButton></template>
        </UTable>
      </div>
    </UCard>

    <USlideover v-model:open="modelEditorOpen" title="编辑逻辑模型">
      <template #body><form class="space-y-4" @submit.prevent="saveModel">
        <UFormField label="模型 ID"><UInput v-model="modelForm.id" readonly class="w-full" /></UFormField>
        <UFormField label="显示名称" required><UInput v-model="modelForm.display_name" class="w-full" /></UFormField>
        <UFormField label="优先级"><UInput v-model.number="modelForm.priority" type="number" min="0" class="w-full" /></UFormField>
        <div class="flex items-center justify-between rounded-lg border border-default p-3"><span class="text-sm">启用此逻辑模型</span><USwitch v-model="modelForm.enabled" /></div>
        <UAlert v-if="modelError" color="error" variant="soft" :title="modelError" />
        <div class="flex justify-end gap-2"><UButton color="neutral" variant="ghost" @click="modelEditorOpen = false">取消</UButton><UButton type="submit" :loading="saving">保存模型</UButton></div>
      </form></template>
    </USlideover>

    <USlideover v-model:open="pairEditorOpen" title="编辑 Provider 模型信息">
      <template #body><form class="space-y-4" @submit.prevent="savePair">
        <div class="grid grid-cols-2 gap-3"><UFormField label="提供商"><UInput v-model="pairForm.provider" readonly class="w-full" /></UFormField><UFormField label="逻辑模型"><UInput v-model="pairForm.model" readonly class="w-full" /></UFormField></div>
        <UFormField label="上游模型 ID" required><UInput v-model="pairForm.upstream_model_id" class="w-full" /><template #hint>支持自定义前缀；此 ID 会原样发送给上游。</template></UFormField>
        <div class="grid grid-cols-2 gap-3"><UFormField label="上下文窗口（tokens）" required><UInput v-model.number="pairForm.context_window" type="number" min="1" class="w-full" /></UFormField><UFormField label="最大输出（留空表示未知）"><UInput v-model="pairForm.max_output" type="number" min="1" class="w-full" /></UFormField></div>
        <div class="space-y-3 rounded-lg border border-default p-3">
          <USwitch v-model="pairForm.supports_tools" label="支持工具调用" />
          <USwitch v-model="pairForm.supports_vision" label="支持图片输入" />
          <USwitch v-model="pairForm.supports_audio_input" label="支持音频输入" />
          <USwitch v-model="pairForm.supports_reasoning" label="推理能力" />
        </div>
        <div class="grid grid-cols-2 gap-3"><UFormField label="优先级"><UInput v-model.number="pairForm.priority" type="number" min="0" class="w-full" /></UFormField><div class="flex items-center justify-between gap-2 pt-5"><span class="text-sm">启用绑定</span><USwitch v-model="pairForm.enabled" /></div></div>
        <UAlert v-if="pairError" color="error" variant="soft" :title="pairError" />
        <div class="flex justify-end gap-2"><UButton color="neutral" variant="ghost" @click="pairEditorOpen = false">取消</UButton><UButton type="submit" :loading="saving">保存绑定信息</UButton></div>
      </form></template>
    </USlideover>
  </div>
</template>
