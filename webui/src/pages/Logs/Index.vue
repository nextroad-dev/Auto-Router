<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, errorMessage, pageURL, type LogEvent, type Page } from '@/lib/api'
import type { operations } from '@/lib/generated-api'

type LogQuery = NonNullable<operations['listAdminLogs']['parameters']['query']>

type FilterOption = { label: string; value: string }

const loading = ref(false)
const logs = ref<LogEvent[]>([])
const nextCursor = ref<string | null>(null)
const hasMore = ref(false)
const error = ref('')
const search = ref('')
const errorCode = ref('')
const statusClass = ref('')
const protocol = ref('')
const routingMode = ref('')
const windowKey = ref('24h')
const detailOpen = ref(false)
const detailLoading = ref(false)
const detailError = ref('')
const selectedLog = ref<LogEvent>()

const statusOptions: FilterOption[] = [
  { label: '全部状态', value: '' },
  { label: '成功（2xx）', value: 'success' },
  { label: '客户端错误（4xx）', value: 'client_error' },
  { label: '服务端错误（5xx）', value: 'server_error' },
]
const protocolOptions: FilterOption[] = [
  { label: '全部协议', value: '' },
  { label: 'Chat Completions', value: 'chat_completions' },
  { label: 'Responses', value: 'responses' },
  { label: '原生协议', value: 'native' },
]
const routingOptions: FilterOption[] = [
  { label: '全部路由', value: '' },
  { label: '自动路由', value: 'auto' },
  { label: '指定模型', value: 'explicit' },
]
const timeOptions: FilterOption[] = [
  { label: '最近 1 小时', value: '1h' },
  { label: '最近 24 小时', value: '24h' },
  { label: '最近 7 天', value: '7d' },
  { label: '最近 30 天', value: '30d' },
  { label: '全部时间', value: 'all' },
]

const columns = [
  { accessorKey: 'started_at', header: '时间' },
  { accessorKey: 'request_id', header: '请求 ID' },
  { accessorKey: 'protocol', header: '协议' },
  { accessorKey: 'requested_model', header: '请求模型' },
  { accessorKey: 'status', header: '状态' },
  { accessorKey: 'error_code', header: '错误码' },
  { accessorKey: 'provider', header: '上游' },
  { accessorKey: 'duration_ms', header: '耗时' },
  { accessorKey: 'actions', header: '详情' },
]

function timeBounds() {
  if (windowKey.value === 'all') return { from: '', to: '' }
  const duration = { '1h': 60 * 60_000, '24h': 24 * 60 * 60_000, '7d': 7 * 24 * 60 * 60_000, '30d': 30 * 24 * 60 * 60_000 }[windowKey.value] ?? 24 * 60 * 60_000
  const to = new Date()
  return { from: new Date(to.getTime() - duration).toISOString(), to: to.toISOString() }
}

function currentQuery(cursor: string | null = null): LogQuery {
  const bounds = timeBounds()
  return {
    limit: 50,
    cursor: cursor ?? undefined,
    search: search.value.trim() || undefined,
    error_code: errorCode.value.trim() || undefined,
    status_class: statusClass.value ? statusClass.value as LogQuery['status_class'] : undefined,
    protocol: protocol.value ? protocol.value as LogQuery['protocol'] : undefined,
    routing_mode: routingMode.value ? routingMode.value as LogQuery['routing_mode'] : undefined,
    from: bounds.from || undefined,
    to: bounds.to || undefined,
  }
}

async function fetchLogs(reset = true) {
  if (loading.value) return
  if (reset) {
    nextCursor.value = null
    hasMore.value = false
    logs.value = []
  }
  loading.value = true
  error.value = ''
  try {
    const result = await api.get<Page<LogEvent>>(pageURL('/admin/v1/logs', currentQuery(nextCursor.value)))
    logs.value.push(...result.items)
    nextCursor.value = result.next_cursor
    hasMore.value = result.next_cursor !== null
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    loading.value = false
  }
}

function formatDate(value: string | null | undefined) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }).format(date)
}

function valueOrDash(value: string | number | boolean | null | undefined) {
  return value === null || value === undefined || value === '' ? '—' : String(value)
}

function statusColor(status: number) {
  if (status >= 500) return 'error'
  if (status >= 400) return 'warning'
  return 'success'
}

function protocolLabel(value: string) {
  return value === 'chat_completions' ? 'Chat Completions' : value === 'responses' ? 'Responses' : '原生协议'
}

async function openDetails(event: LogEvent) {
  selectedLog.value = event
  detailError.value = ''
  detailOpen.value = true
  detailLoading.value = true
  try {
    const result = await api.get<Page<LogEvent>>(pageURL('/admin/v1/logs', { request_id: event.request_id, limit: 1 }))
    if (result.items[0]) selectedLog.value = result.items[0]
  } catch (cause) {
    detailError.value = errorMessage(cause)
  } finally {
    detailLoading.value = false
  }
}

onMounted(() => { void fetchLogs() })
</script>

<template>
  <div class="space-y-5">
    <section class="flex flex-col justify-between gap-3 sm:flex-row sm:items-end">
      <div>
        <p class="text-sm text-muted">仅记录进入推理转发路径的请求；请求正文不会显示在日志中。</p>
      </div>
      <UButton color="neutral" variant="outline" icon="i-heroicons-arrow-path" :loading="loading" @click="fetchLogs(true)">刷新</UButton>
    </section>

    <UAlert v-if="error" color="error" variant="soft" :title="error" />

    <UCard class="overflow-hidden">
      <template #header>
        <div class="flex flex-col gap-3">
          <div class="flex items-center justify-between gap-2">
            <h2 class="font-semibold">请求记录 <UBadge color="neutral" variant="subtle" class="ml-1">{{ logs.length }}</UBadge></h2>
            <USelect v-model="windowKey" :items="timeOptions" value-key="value" class="w-36" aria-label="时间范围" @update:model-value="fetchLogs(true)" />
          </div>
          <div class="grid gap-2 sm:grid-cols-2 xl:grid-cols-5">
            <UInput v-model="search" icon="i-heroicons-magnifying-glass" placeholder="搜索请求 ID、模型或提供商" @keydown.enter="fetchLogs(true)" />
            <UInput v-model="errorCode" placeholder="错误码，如 no_eligible_candidate" @keydown.enter="fetchLogs(true)" />
            <USelect v-model="statusClass" :items="statusOptions" value-key="value" aria-label="HTTP 状态筛选" @update:model-value="fetchLogs(true)" />
            <USelect v-model="protocol" :items="protocolOptions" value-key="value" aria-label="协议筛选" @update:model-value="fetchLogs(true)" />
            <USelect v-model="routingMode" :items="routingOptions" value-key="value" aria-label="路由模式筛选" @update:model-value="fetchLogs(true)" />
          </div>
        </div>
      </template>

      <div class="overflow-x-auto">
        <UTable :data="logs" :columns="columns" :loading="loading" empty="当前筛选没有匹配的请求日志">
          <template #started_at-cell="{ row }"><span class="whitespace-nowrap text-xs text-muted">{{ formatDate(row.original.started_at) }}</span></template>
          <template #request_id-cell="{ row }"><code class="font-mono text-xs">{{ row.original.request_id }}</code></template>
          <template #protocol-cell="{ row }"><UBadge color="neutral" variant="subtle">{{ protocolLabel(row.original.protocol) }}</UBadge></template>
          <template #requested_model-cell="{ row }"><span class="block max-w-52 truncate font-mono text-xs" :title="row.original.requested_model ?? ''">{{ valueOrDash(row.original.requested_model) }}</span></template>
          <template #status-cell="{ row }"><UBadge :color="statusColor(row.original.status)" variant="subtle">{{ row.original.status }}</UBadge></template>
          <template #error_code-cell="{ row }"><code v-if="row.original.error_code" class="text-xs text-error">{{ row.original.error_code }}</code><span v-else class="text-muted">—</span></template>
          <template #provider-cell="{ row }"><span class="text-xs">{{ valueOrDash(row.original.provider) }}</span></template>
          <template #duration_ms-cell="{ row }"><span class="whitespace-nowrap text-xs">{{ row.original.duration_ms }} ms</span></template>
          <template #actions-cell="{ row }"><UButton size="xs" color="neutral" variant="ghost" @click="openDetails(row.original)">查看</UButton></template>
        </UTable>
      </div>
      <div v-if="hasMore" class="flex justify-center border-t border-default p-4">
        <UButton color="neutral" variant="soft" :loading="loading" @click="fetchLogs(false)">加载更多</UButton>
      </div>
    </UCard>

    <USlideover v-model:open="detailOpen" :title="selectedLog ? `请求详情 · ${selectedLog.request_id}` : '请求详情'">
      <template #body>
        <div v-if="selectedLog" class="space-y-5">
          <UAlert v-if="detailError" color="error" variant="soft" :title="detailError" />
          <USkeleton v-if="detailLoading" class="h-10 w-full" />
          <UAlert v-if="selectedLog.error_code" color="error" variant="soft" :title="selectedLog.error_code" :description="`HTTP ${selectedLog.status} · 请求未能成功完成`" />

          <section class="grid grid-cols-2 gap-x-4 gap-y-3 text-sm">
            <div><div class="text-xs text-muted">开始时间</div><div class="mt-1">{{ formatDate(selectedLog.started_at) }}</div></div>
            <div><div class="text-xs text-muted">协议 / 状态</div><div class="mt-1">{{ protocolLabel(selectedLog.protocol) }} · HTTP {{ selectedLog.status }}</div></div>
            <div><div class="text-xs text-muted">请求模型</div><div class="mt-1 break-all font-mono">{{ valueOrDash(selectedLog.requested_model) }}</div></div>
            <div><div class="text-xs text-muted">生效模型</div><div class="mt-1 break-all font-mono">{{ valueOrDash(selectedLog.effective_model) }}</div></div>
            <div><div class="text-xs text-muted">提供商</div><div class="mt-1">{{ valueOrDash(selectedLog.provider) }}</div></div>
            <div><div class="text-xs text-muted">上游模型</div><div class="mt-1 break-all font-mono">{{ valueOrDash(selectedLog.upstream_model) }}</div></div>
            <div><div class="text-xs text-muted">路由模式 / 选择模式</div><div class="mt-1">{{ valueOrDash(selectedLog.routing_mode) }} / {{ valueOrDash(selectedLog.selection_mode) }}</div></div>
            <div><div class="text-xs text-muted">上游状态 / 尝试次数</div><div class="mt-1">{{ valueOrDash(selectedLog.upstream_status) }} / {{ selectedLog.gateway_attempts }}</div></div>
            <div><div class="text-xs text-muted">耗时 / 路由耗时</div><div class="mt-1">{{ selectedLog.duration_ms }} ms / {{ valueOrDash(selectedLog.routing_latency_ms) }} ms</div></div>
            <div><div class="text-xs text-muted">输入 / 输出 Token</div><div class="mt-1">{{ valueOrDash(selectedLog.input_tokens) }} / {{ valueOrDash(selectedLog.output_tokens) }}</div></div>
            <div><div class="text-xs text-muted">Jev 状态</div><div class="mt-1">{{ valueOrDash(selectedLog.jev_status) }}</div></div>
            <div><div class="text-xs text-muted">Fallback 原因</div><div class="mt-1 break-all">{{ valueOrDash(selectedLog.fallback_reason) }}</div></div>
          </section>

          <section v-if="selectedLog.attempts?.length" class="space-y-2 border-t border-default pt-4">
            <h3 class="text-sm font-semibold">上游尝试</h3>
            <div v-for="attempt in selectedLog.attempts" :key="attempt.id" class="rounded-lg border border-default p-3 text-xs">
              <div class="flex flex-wrap items-center gap-2">
                <UBadge color="neutral" variant="subtle">第 {{ attempt.attempt_index }} 次</UBadge>
                <span>{{ valueOrDash(attempt.provider) }} · {{ valueOrDash(attempt.model_id) }}</span>
                <UBadge v-if="attempt.status" :color="statusColor(attempt.status)" variant="subtle">{{ attempt.status }}</UBadge>
                <code v-if="attempt.error_code" class="text-error">{{ attempt.error_code }}</code>
              </div>
              <div class="mt-2 text-muted">分组 {{ valueOrDash(attempt.group_name) }} · {{ formatDate(attempt.started_at) }} · Token {{ valueOrDash(attempt.input_tokens) }} / {{ valueOrDash(attempt.output_tokens) }}</div>
            </div>
          </section>

          <section v-if="selectedLog.jev_trace" class="space-y-2 border-t border-default pt-4">
            <h3 class="text-sm font-semibold">Jev 路由诊断</h3>
            <div class="text-xs text-muted">状态 {{ selectedLog.jev_trace.status }} · 候选 {{ selectedLog.jev_trace.candidate_count }} · 模式 {{ selectedLog.jev_trace.input_mode }}</div>
            <p v-if="selectedLog.jev_trace.failure_reason" class="text-sm text-error">{{ selectedLog.jev_trace.failure_reason }}</p>
            <div class="flex flex-wrap gap-1.5"><UBadge v-for="candidate in selectedLog.jev_trace.candidate_models" :key="candidate" color="neutral" variant="subtle">{{ candidate }}</UBadge></div>
            <div v-for="probability in selectedLog.jev_trace.probabilities" :key="probability.model" class="flex justify-between gap-3 text-xs"><span class="break-all">{{ probability.model }}</span><span>{{ (probability.probability * 100).toFixed(1) }}%</span></div>
          </section>

          <div class="border-t border-default pt-3 text-xs text-muted">Request ID：<code class="break-all">{{ selectedLog.request_id }}</code></div>
        </div>
      </template>
    </USlideover>
  </div>
</template>
