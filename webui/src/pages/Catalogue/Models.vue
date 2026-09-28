<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { api, getAllPages, type Model, type Pair } from '@/lib/api'
import { createDebouncedSave, createSerialAutosaveQueue } from '@/lib/autosave'
import { errorNotice } from '@/lib/errors'
import { showSavedToast } from '@/lib/save-toast'
import ErrorAlert from '@/components/ErrorAlert.vue'
import JfAlert from '@/components/JfAlert.vue'
import JfBadge from '@/components/JfBadge.vue'
import JfButton from '@/components/JfButton.vue'
import JfCard from '@/components/JfCard.vue'
import JfDrawer from '@/components/JfDrawer.vue'
import JfField from '@/components/JfField.vue'
import JfInput from '@/components/JfInput.vue'
import JfSkeleton from '@/components/JfSkeleton.vue'
import JfSwitch from '@/components/JfSwitch.vue'
import JfTable from '@/components/JfTable.vue'
import type { JfColumn } from '@/lib/table'
import type { components, operations } from '@/lib/generated-api'

const loading = ref(false)
const saving = ref(false)
const error = ref<unknown>()
const search = ref('')
const models = ref<Model[]>([])
const pairs = ref<Pair[]>([])

type SyncState = components['schemas']['SyncState']
type SyncResult = components['schemas']['SyncResult']
const syncState = ref<SyncState>()
const syncLoading = ref(false)
const syncing = ref(false)
const syncError = ref<unknown>()
const syncSuccess = ref('')

const modelEditorOpen = ref(false)
const pairEditorOpen = ref(false)
const modelError = ref<unknown>()
const pairError = ref<unknown>()

const togglingModels = ref<Record<string, boolean>>({})
const togglingPairs = ref<Record<string, boolean>>({})
const deletingPair = ref('')

const modelForm = reactive({ id: '', display_name: '' })
const pairForm = reactive({
  provider: '',
  model: '',
  upstream_model_id: '',
  context_window: 32768 as number | string,
  max_output: '',
  supports_tools: false,
  supports_vision: false,
  supports_audio_input: false,
  supports_reasoning: false,
  enabled: true,
})

type RegistryPatchTask = {
  key: string
  endpoint: string
  body: Record<string, unknown>
  fields?: Map<string, number>
  errorTarget: 'model' | 'pair'
}
const modelDirty = new Set<string>()
const modelPendingText = new Set<string>()
const modelVersions = new Map<string, number>()
const modelTextSaves = new Map<string, ReturnType<typeof createDebouncedSave>>()
const pairDirty = new Set<string>()
const pairPendingText = new Set<string>()
const pairVersions = new Map<string, number>()
const pairTextSaves = new Map<string, ReturnType<typeof createDebouncedSave>>()
const pairCapacityPending = new Set<string>()
const registrySaveQueue = createSerialAutosaveQueue<RegistryPatchTask[]>(runRegistryPatchTasks, mergeRegistryPatchTasks)

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

const modelColumns: JfColumn[] = [
  { key: 'id', title: '模型标识', nowrap: true },
  { key: 'display_name', title: '显示名称' },
  { key: 'enabled', title: '状态', nowrap: true },
  { key: 'pair_count', title: '绑定数', align: 'end' },
  { key: 'actions', title: '操作', nowrap: true },
]

const pairColumns: JfColumn[] = [
  { key: 'provider', title: '提供商', nowrap: true },
  { key: 'model', title: '逻辑模型', nowrap: true },
  { key: 'upstream_model_id', title: '上游模型 ID' },
  { key: 'context_window', title: '上下文窗口', align: 'end' },
  { key: 'capabilities', title: '已声明能力' },
  { key: 'enabled', title: '状态', nowrap: true },
  { key: 'source', title: '元数据来源' },
  { key: 'actions', title: '操作', nowrap: true },
]

function mergeRegistryPatchTasks(current: RegistryPatchTask[], next: RegistryPatchTask[]) {
  const merged = [...current]
  for (const task of next) {
    const index = merged.findIndex(item => item.key === task.key)
    if (index < 0) {
      merged.push(task)
      continue
    }
    const existing = merged[index]!
    merged[index] = {
      ...existing,
      ...task,
      body: { ...existing.body, ...task.body },
      fields: new Map([...(existing.fields ?? []), ...(task.fields ?? [])]),
    }
  }
  return merged
}

async function runRegistryPatchTasks(tasks: RegistryPatchTask[]) {
  for (const task of tasks) {
    saving.value = true
    try {
      await api.patch(task.endpoint, task.body)
      if (task.endpoint.startsWith('/admin/v1/models/')) {
        const id = task.key.slice('model:'.length)
        const row = models.value.find(item => item.id === id)
        if (row) {
          if (typeof task.body.display_name === 'string') row.display_name = task.body.display_name
        }
        if (modelForm.id === id) {
          for (const [field, version] of task.fields ?? []) {
            if (modelVersions.get(field) === version) modelDirty.delete(field)
          }
          modelError.value = undefined
        }
      } else {
        const [provider, model] = JSON.parse(task.key.slice('pair:'.length)) as [string, string]
        const row = pairs.value.find(item => item.provider === provider && item.model === model)
        if (row) {
          if (typeof task.body.upstream_model_id === 'string') row.upstream_model_id = task.body.upstream_model_id
          if (typeof task.body.context_window === 'number') row.context_window = task.body.context_window
          if (task.body.max_output === null || typeof task.body.max_output === 'number') row.max_output = task.body.max_output
          for (const field of ['enabled', 'supports_tools', 'supports_vision', 'supports_audio_input', 'supports_reasoning'] as const) {
            if (typeof task.body[field] === 'boolean') row[field] = task.body[field]
          }
        }
        if (pairForm.provider === provider && pairForm.model === model) {
          for (const [field, version] of task.fields ?? []) {
            if (pairVersions.get(field) === version) pairDirty.delete(field)
          }
          pairError.value = undefined
        }
      }
      showSavedToast()
    } catch (cause) {
      if (task.errorTarget === 'model' && modelForm.id === task.key.slice('model:'.length)) modelError.value = errorNotice(cause)
      else if (task.errorTarget === 'pair' && task.key === `pair:${JSON.stringify([pairForm.provider, pairForm.model])}`) pairError.value = errorNotice(cause)
    } finally {
      saving.value = false
    }
  }
}

async function loadData() {
  loading.value = true
  error.value = undefined
  try {
    const [modelRows, pairRows] = await Promise.all([
      getAllPages<Model>('/admin/v1/models'),
      getAllPages<Pair>('/admin/v1/pairs'),
    ])
    models.value = modelRows
    pairs.value = pairRows
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    loading.value = false
  }
}

async function toggleModel(model: Model) {
  const previous = model.enabled
  const next = !previous
  model.enabled = next
  togglingModels.value[model.id] = true
  try {
    await api.patch(`/admin/v1/models/${encodeURIComponent(model.id)}`, { enabled: next })
    showSavedToast()
  } catch (cause) {
    model.enabled = previous
    error.value = errorNotice(cause)
  } finally {
    delete togglingModels.value[model.id]
  }
}

async function togglePair(pair: Pair) {
  const key = pairRowKey(pair)
  const previous = pair.enabled
  const next = !previous
  pair.enabled = next
  togglingPairs.value[key] = true
  try {
    await api.patch(`/admin/v1/pairs/${encodeURIComponent(pair.provider)}/${encodeURIComponent(pair.model)}`, { enabled: next })
    showSavedToast()
  } catch (cause) {
    pair.enabled = previous
    error.value = errorNotice(cause)
  } finally {
    delete togglingPairs.value[key]
  }
}

async function deletePair(pair: Pair) {
  const key = pairRowKey(pair)
  if (deletingPair.value) return
  if (!window.confirm(`确定永久解除提供商“${pair.provider}”与模型“${pair.model}”的绑定吗？该绑定会从所有路由组移除，后续注册表同步不会自动恢复。`)) return

  deletingPair.value = key
  error.value = undefined
  try {
    await api.delete<operations['deleteAdminPair']['responses'][200]['content']['application/json']>(
      `/admin/v1/pairs/${encodeURIComponent(pair.provider)}/${encodeURIComponent(pair.model)}`,
    )
    await loadData()
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    deletingPair.value = ''
  }
}

function resetModelAutosave() {
  for (const saver of modelTextSaves.values()) saver.cancel()
  modelTextSaves.clear()
  modelDirty.clear()
  modelPendingText.clear()
  modelVersions.clear()
}

function modelFieldSaver(field: string) {
  let saver = modelTextSaves.get(field)
  if (!saver) {
    saver = createDebouncedSave(() => {
      modelPendingText.delete(field)
      queueModelFormSave()
    })
    modelTextSaves.set(field, saver)
  }
  return saver
}

function updateModelField(field: 'display_name', value: string) {
  modelForm.display_name = value
  modelError.value = undefined
  modelDirty.add(field)
  modelPendingText.add(field)
  modelVersions.set(field, (modelVersions.get(field) ?? 0) + 1)
  modelFieldSaver(field).schedule()
}

function queueModelFormSave() {
  const fields = [...modelDirty].filter(field => !modelPendingText.has(field))
  if (!fields.length) return
  if (fields.includes('display_name') && !modelForm.display_name.trim()) {
    modelError.value = '模型显示名称不能为空。'
    return
  }
  const body: Record<string, unknown> = {}
  if (fields.includes('display_name')) body.display_name = modelForm.display_name.trim()
  registrySaveQueue.enqueue([{
    key: `model:${modelForm.id}`,
    endpoint: `/admin/v1/models/${encodeURIComponent(modelForm.id)}`,
    body,
    fields: new Map(fields.map(field => [field, modelVersions.get(field) ?? 0])),
    errorTarget: 'model',
  }])
}

function flushModelText(field: 'display_name') {
  if (!modelPendingText.has(field)) return
  modelPendingText.delete(field)
  void modelFieldSaver(field).flush()
}

function editModel(model: Model) {
  resetModelAutosave()
  modelError.value = undefined
  Object.assign(modelForm, {
    id: model.id,
    display_name: model.display_name,
  })
  modelEditorOpen.value = true
}

function saveModel() {
  for (const saver of modelTextSaves.values()) void saver.flush()
  queueModelFormSave()
}

function resetPairAutosave() {
  for (const saver of pairTextSaves.values()) saver.cancel()
  pairTextSaves.clear()
  pairDirty.clear()
  pairPendingText.clear()
  pairVersions.clear()
  pairCapacityPending.clear()
}

function pairFieldSaver(field: string) {
  let saver = pairTextSaves.get(field)
  if (!saver) {
    saver = createDebouncedSave(() => {
      if (field === 'capacity') {
        pairCapacityPending.clear()
        pairPendingText.delete('context_window')
        pairPendingText.delete('max_output')
      } else {
        pairPendingText.delete(field)
      }
      queuePairFormSave()
    })
    pairTextSaves.set(field, saver)
  }
  return saver
}

function updatePairText(field: 'upstream_model_id' | 'context_window' | 'max_output', value: string) {
  if (field === 'upstream_model_id') pairForm.upstream_model_id = value
  else if (field === 'context_window') pairForm.context_window = value
  else pairForm.max_output = value
  pairError.value = undefined
  pairDirty.add(field)
  pairVersions.set(field, (pairVersions.get(field) ?? 0) + 1)
  if (field === 'context_window' || field === 'max_output') {
    pairCapacityPending.add(field)
    pairPendingText.add(field)
    pairFieldSaver('capacity').schedule()
  } else {
    pairPendingText.add(field)
    pairFieldSaver(field).schedule()
  }
}

function updatePairSwitch(field: 'supports_tools' | 'supports_vision' | 'supports_audio_input' | 'supports_reasoning' | 'enabled', value: boolean) {
  pairForm[field] = value
  pairError.value = undefined
  pairDirty.add(field)
  pairVersions.set(field, (pairVersions.get(field) ?? 0) + 1)
  queuePairFormSave(new Set([field]))
}

function queuePairFormSave(onlyFields?: Set<string>) {
  let fields = [...pairDirty].filter(field => !pairPendingText.has(field) && (!onlyFields || onlyFields.has(field)))
  const capacityFields = ['context_window', 'max_output']
  if (pairCapacityPending.size && fields.some(field => capacityFields.includes(field))) {
    fields = fields.filter(field => !capacityFields.includes(field))
  }
  if (!fields.length) return
  if (fields.includes('upstream_model_id') && !pairForm.upstream_model_id.trim()) {
    pairError.value = '上游模型 ID 不能为空。'
    return
  }
  const body: Record<string, unknown> = {}
  for (const field of fields) {
    if (field === 'upstream_model_id') body.upstream_model_id = pairForm.upstream_model_id.trim()
    else if (!capacityFields.includes(field)) body[field] = pairForm[field as keyof typeof pairForm]
  }
  const capacityChanged = fields.some(field => capacityFields.includes(field))
  if (capacityChanged) {
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
    body.context_window = contextWindow
    body.max_output = maxOutput
    fields = [...new Set([...fields, ...pairDirty].filter(field => capacityFields.includes(field) || fields.includes(field)))]
  }
  registrySaveQueue.enqueue([{
    key: `pair:${JSON.stringify([pairForm.provider, pairForm.model])}`,
    endpoint: `/admin/v1/pairs/${encodeURIComponent(pairForm.provider)}/${encodeURIComponent(pairForm.model)}`,
    body,
    fields: new Map(fields.map(field => [field, pairVersions.get(field) ?? 0])),
    errorTarget: 'pair',
  }])
}

function flushPairText(field: 'upstream_model_id' | 'context_window' | 'max_output') {
  if (field === 'context_window' || field === 'max_output') {
    if (!pairCapacityPending.size) return
    void pairFieldSaver('capacity').flush()
  } else {
    if (!pairPendingText.has(field)) return
    pairPendingText.delete(field)
    void pairFieldSaver(field).flush()
  }
}

function editPair(pair: Pair) {
  resetPairAutosave()
  pairError.value = undefined
  Object.assign(pairForm, {
    provider: pair.provider,
    model: pair.model,
    upstream_model_id: pair.upstream_model_id,
    context_window: pair.context_window,
    max_output: pair.max_output === null ? '' : String(pair.max_output),
    supports_tools: pair.supports_tools,
    supports_vision: pair.supports_vision,
    supports_audio_input: pair.supports_audio_input,
    supports_reasoning: pair.supports_reasoning,
    enabled: pair.enabled,
  })
  pairEditorOpen.value = true
}

function savePair() {
  for (const saver of pairTextSaves.values()) void saver.flush()
  queuePairFormSave()
}

watch(modelEditorOpen, isOpen => {
  if (!isOpen) for (const saver of modelTextSaves.values()) void saver.flush()
})
watch(pairEditorOpen, isOpen => {
  if (!isOpen) for (const saver of pairTextSaves.values()) void saver.flush()
})

async function loadSyncState() {
  syncLoading.value = true
  syncError.value = undefined
  try {
    syncState.value = await api.get<SyncState>('/admin/v1/sync-state')
  } catch (cause) {
    syncError.value = errorNotice(cause)
  } finally {
    syncLoading.value = false
  }
}

async function synchronizeRegistry() {
  syncing.value = true
  syncError.value = undefined
  syncSuccess.value = ''
  try {
    const result = await api.postEmpty<SyncResult>('/admin/v1/sync')
    if (!result.synchronized) {
      syncError.value = '同步未产生成功结果；注册表未做更新。'
      return
    }
    syncSuccess.value = `同步成功：导入 ${result.imported_pairs} 个模型绑定，跳过 ${result.skipped_pairs} 个。`
    await Promise.all([loadData(), loadSyncState()])
  } catch (cause) {
    syncError.value = errorNotice(cause)
  } finally {
    syncing.value = false
  }
}

function formattedSyncTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN')
}

function capabilities(pair: Pair) {
  const values: string[] = []
  if (pair.supports_tools) values.push('工具')
  if (pair.supports_vision) values.push('图像')
  if (pair.supports_audio_input) values.push('音频')
  if (pair.supports_reasoning) values.push('推理')
  return values.length ? values.join(' · ') : '纯文本'
}

function pairRowKey(pair: Pair) {
  return `${pair.provider}·${pair.model}`
}

onMounted(() => {
  void loadData()
  void loadSyncState()
})
</script>

<template>
  <div class="jf-stack">
    <!-- Toolbar -->
    <section class="jf-toolbar">
      <div>
        <h1 class="jf-page-title">模型管理</h1>
      </div>
      <div class="jf-action-group">
        <JfInput
          v-model="search"
          icon="magnifying-glass"
          placeholder="搜索模型标识或提供商"
          aria-label="搜索模型标识或提供商"
          class="w-full sm:w-64"
        />
        <JfButton variant="secondary" icon="arrow-path" :loading="loading" @click="loadData">刷新</JfButton>
      </div>
    </section>

    <ErrorAlert v-if="error" :error="error" />

    <!-- models.dev Global Sync Card -->
    <JfCard title="models.dev 全局同步">
      <template #actions>
        <JfButton
          icon="arrow-path"
          :loading="syncing"
          :disabled="loading || syncLoading"
          @click="synchronizeRegistry"
        >
          立即同步 models.dev
        </JfButton>
      </template>

      <div class="grid gap-3">
        <ErrorAlert v-if="syncError" :error="syncError" />
        <JfAlert v-if="syncSuccess" tone="success" :title="syncSuccess" />

        <div v-if="syncLoading" class="grid gap-2" aria-busy="true">
          <p class="jf-caption text-ink-secondary">正在读取最新同步状态…</p>
          <JfSkeleton height="36px" shape="block" />
        </div>

        <div v-else-if="syncState?.synchronized" class="grid jf-body gap-2 sm:grid-cols-2">
          <p class="jf-caption">
            最近同步：<time :datetime="syncState.fetched_at" class="jf-mono jf-tabular font-medium">{{ formattedSyncTime(syncState.fetched_at) }}</time>
          </p>
          <p class="jf-caption">上游源：<code class="jf-mono jf-anywhere">{{ syncState.url }}</code></p>
          <p class="jf-caption">
            导入绑定数：<span class="jf-mono font-medium">{{ syncState.imported_pairs }}</span> · 跳过项：<span class="jf-mono font-medium">{{ syncState.skipped_pairs }}</span>
          </p>
          <p v-if="syncState.warnings.length" class="jf-caption text-warning sm:col-span-2">
            提醒：{{ syncState.warnings.join('；') }}
          </p>
        </div>

        <p v-else class="jf-caption text-ink-secondary">尚无同步记录</p>
      </div>
    </JfCard>

    <!-- Logical Models Table Card -->
    <JfCard title="逻辑模型" flush>
      <template #actions>
        <JfBadge tone="neutral">{{ visibleModels.length }}</JfBadge>
      </template>

      <div class="jf-scroll-x">
        <JfTable :rows="visibleModels" :columns="modelColumns" :loading="loading" row-key="id" empty-text="没有匹配的逻辑模型">
          <template #cell-id="{ row }">
            <code class="font-mono font-medium">{{ row.id }}</code>
          </template>

          <template #cell-display_name="{ row }">
            <div class="font-medium">{{ row.display_name }}</div>
            <JfBadge tone="neutral" class="mt-1">
              {{ row.owner === 'admin' ? '本地管理员维护' : row.source }}
            </JfBadge>
          </template>

          <template #cell-enabled="{ row }">
            <div class="flex items-center gap-2">
              <JfSwitch
                :model-value="row.enabled"
                :disabled="Boolean(togglingModels[row.id])"
                :aria-label="`${row.enabled ? '停用' : '启用'}逻辑模型 ${row.display_name || row.id}`"
                @update:model-value="toggleModel(row)"
              />
              <span class="jf-caption font-medium select-none" :class="row.enabled ? 'text-ink' : 'text-ink-secondary'">
                {{ row.enabled ? '已启用' : '已停用' }}
              </span>
            </div>
          </template>

          <template #cell-pair_count="{ row }">
            <span class="jf-mono jf-tabular">{{ row.pair_count }}</span>
          </template>

          <template #cell-actions="{ row }">
            <JfButton variant="ghost" size="sm" icon="pencil-square" @click="editModel(row)">编辑</JfButton>
          </template>
        </JfTable>
      </div>
    </JfCard>

    <!-- Provider Bindings & Capabilities Table Card -->
    <JfCard title="Provider 模型绑定与能力" flush>
      <template #actions>
        <JfBadge tone="neutral">{{ visiblePairs.length }}</JfBadge>
      </template>

      <div class="jf-scroll-x">
        <JfTable :rows="visiblePairs" :columns="pairColumns" :loading="loading" :row-key="pairRowKey" empty-text="没有匹配的模型绑定">
          <template #cell-provider="{ row }">
            <code class="font-mono font-medium">{{ row.provider }}</code>
          </template>

          <template #cell-model="{ row }">
            <code class="font-mono">{{ row.model }}</code>
          </template>

          <template #cell-upstream_model_id="{ row }">
            <code class="block max-w-60 jf-truncate font-mono" :title="row.upstream_model_id">{{ row.upstream_model_id }}</code>
          </template>

          <template #cell-context_window="{ row }">
            <span class="font-mono jf-tabular">{{ row.context_window?.toLocaleString() }}</span>
            <span v-if="row.max_output" class="mt-0.5 block jf-caption font-mono jf-tabular text-ink-secondary">
              输出 ≤ {{ row.max_output?.toLocaleString() }}
            </span>
          </template>

          <template #cell-capabilities="{ row }">
            <span class="jf-caption text-ink-secondary">{{ capabilities(row) }}</span>
          </template>

          <template #cell-enabled="{ row }">
            <div class="flex items-center gap-2">
              <JfSwitch
                :model-value="row.enabled"
                :disabled="Boolean(togglingPairs[pairRowKey(row)])"
                :aria-label="`${row.enabled ? '停用' : '启用'}模型绑定 ${row.provider} - ${row.model}`"
                @update:model-value="togglePair(row)"
              />
              <span class="jf-caption font-medium select-none" :class="row.enabled ? 'text-ink' : 'text-ink-secondary'">
                {{ row.enabled ? '已启用' : '已停用' }}
              </span>
            </div>
          </template>

          <template #cell-source="{ row }">
            <JfBadge :tone="row.source === 'modelsdev' ? 'primary' : 'neutral'" class="jf-nowrap">
              {{ row.source === 'modelsdev' ? 'models.dev' : '本地' }}
              <template v-if="row.owner === 'admin'"> · 自定义</template>
            </JfBadge>
          </template>

          <template #cell-actions="{ row }">
            <div class="jf-action-group">
              <JfButton variant="ghost" size="sm" icon="pencil-square" @click="editPair(row)">编辑能力</JfButton>
              <JfButton
                variant="danger-ghost"
                size="sm"
                :loading="deletingPair === pairRowKey(row)"
                :disabled="Boolean(deletingPair)"
                :aria-label="`解除绑定 ${row.provider} - ${row.model}`"
                @click="deletePair(row)"
              >
                解除绑定
              </JfButton>
            </div>
          </template>
        </JfTable>
      </div>
    </JfCard>

    <!-- Edit Logical Model Drawer -->
    <JfDrawer v-model:open="modelEditorOpen" title="编辑逻辑模型">
      <form class="grid gap-5" @submit.prevent="saveModel">
        <JfField label="模型标识">
          <JfInput v-model="modelForm.id" readonly class="w-full font-mono" />
        </JfField>

        <JfField label="显示名称" required>
          <JfInput
            :model-value="modelForm.display_name"
            class="w-full"
            required
            @update:model-value="updateModelField('display_name', $event)"
            @blur="flushModelText('display_name')"
          />
        </JfField>

        <ErrorAlert v-if="modelError" :error="modelError" />
      </form>

      <template #footer>
        <div class="jf-action-group justify-end">
          <JfButton variant="secondary" @click="modelEditorOpen = false">完成</JfButton>
        </div>
      </template>
    </JfDrawer>

    <!-- Edit Provider Binding Drawer -->
    <JfDrawer v-model:open="pairEditorOpen" title="编辑上游模型绑定与能力" size="lg">
      <form class="grid gap-5" @submit.prevent="savePair">
        <div class="grid gap-3 sm:grid-cols-2">
          <JfField label="所属提供商">
            <JfInput v-model="pairForm.provider" readonly class="w-full font-mono" />
          </JfField>
          <JfField label="逻辑模型标识">
            <JfInput v-model="pairForm.model" readonly class="w-full font-mono" />
          </JfField>
        </div>

        <JfField label="上游模型 ID" required>
          <JfInput
            :model-value="pairForm.upstream_model_id"
            class="w-full font-mono"
            required
            @update:model-value="updatePairText('upstream_model_id', $event)"
            @blur="flushPairText('upstream_model_id')"
          />
        </JfField>

        <div class="grid items-start gap-3 sm:grid-cols-2">
          <JfField label="上下文窗口（Tokens）" required>
            <JfInput
              :model-value="pairForm.context_window"
              type="number"
              min="1"
              class="w-full jf-tabular"
              required
              @update:model-value="updatePairText('context_window', $event)"
              @blur="flushPairText('context_window')"
            />
          </JfField>
          <JfField label="最大输出限制（Tokens）">
            <JfInput
              :model-value="pairForm.max_output"
              type="number"
              min="1"
              class="w-full jf-tabular"
              @update:model-value="updatePairText('max_output', $event)"
              @blur="flushPairText('max_output')"
            />
          </JfField>
        </div>

        <div class="rounded-[var(--jf-radius-control)] border border-line p-4">
          <h3 class="jf-module-title mb-3">支持的高级能力声明</h3>
          <div class="grid gap-3 sm:grid-cols-2">
            <JfField label="函数与工具调用 (Tools)" inline>
              <JfSwitch :model-value="pairForm.supports_tools" @update:model-value="updatePairSwitch('supports_tools', $event)" />
            </JfField>
            <JfField label="多模态图片输入 (Vision)" inline>
              <JfSwitch :model-value="pairForm.supports_vision" @update:model-value="updatePairSwitch('supports_vision', $event)" />
            </JfField>
            <JfField label="音频输入 (Audio)" inline>
              <JfSwitch :model-value="pairForm.supports_audio_input" @update:model-value="updatePairSwitch('supports_audio_input', $event)" />
            </JfField>
            <JfField label="深度思考与推理 (Reasoning)" inline>
              <JfSwitch :model-value="pairForm.supports_reasoning" @update:model-value="updatePairSwitch('supports_reasoning', $event)" />
            </JfField>
          </div>
        </div>

        <JfField label="启用该上游绑定" inline>
          <JfSwitch :model-value="pairForm.enabled" @update:model-value="updatePairSwitch('enabled', $event)" />
        </JfField>

        <ErrorAlert v-if="pairError" :error="pairError" />
      </form>

      <template #footer>
        <div class="jf-action-group justify-end">
          <JfButton variant="secondary" @click="pairEditorOpen = false">完成</JfButton>
        </div>
      </template>
    </JfDrawer>
  </div>
</template>
