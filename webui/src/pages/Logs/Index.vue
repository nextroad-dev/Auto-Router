<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, pageURL, type LogEvent, type Page } from '@/lib/api'
import { errorCodeDescription, errorNotice, httpStatusLabel } from '@/lib/errors'
import { enumLabel, fallbackReasonLabel, jevStatusLabel, logErrorCodeLabel } from '@/lib/labels'
import ErrorAlert from '@/components/ErrorAlert.vue'
import type { operations } from '@/lib/generated-api'

type LogQuery = NonNullable<operations['listAdminLogs']['parameters']['query']>

type FilterOption = { label: string; value: string }

const loading = ref(false)
const logs = ref<LogEvent[]>([])
const nextCursor = ref<string | null>(null)
const hasMore = ref(false)
const error = ref<unknown>()
/**
 * The sentinel for "no filter".
 *
 * reka-ui's `SelectItem` rejects an empty string because the empty string is how a select
 * signals "nothing chosen", so the three filter vocabularies use an explicit sentinel and
 * translate it back to an absent query parameter in `currentQuery`.
 */
const ANY = '__any__'

const search = ref('')
const errorCode = ref('')
const statusClass = ref(ANY)
const protocol = ref(ANY)
const routingMode = ref(ANY)
const windowKey = ref('24h')
const detailOpen = ref(false)
const detailLoading = ref(false)
const detailError = ref<unknown>()
const selectedLog = ref<LogEvent>()

function withAll(label: string, options: Array<{ label: string; value: string }>): FilterOption[] {
  return [{ label, value: ANY }, ...options]
}

/** A filter value as the API expects it: `undefined` when the caller has not narrowed anything. */
function filterValue(value: string): string | undefined {
  return value === ANY || value === '' ? undefined : value
}

/* The filter vocabularies are derived from the same label tables the table cells use, so a
 * translated enum member cannot drift between the two places it is shown. */
const statusOptions: FilterOption[] = withAll('全部状态', (['success', 'client_error', 'server_error'] as const)
  .map(value => ({ label: enumLabel('statusClass', value), value })))
const protocolOptions: FilterOption[] = withAll('全部协议', (['chat_completions', 'responses', 'native'] as const)
  .map(value => ({ label: enumLabel('protocol', value), value })))
const routingOptions: FilterOption[] = withAll('全部路由', (['auto', 'explicit'] as const)
  .map(value => ({ label: enumLabel('routingMode', value), value })))
const timeOptions: FilterOption[] = [
  { label: '最近 1 小时', value: '1h' },
  { label: '最近 24 小时', value: '24h' },
  { label: '最近 7 天', value: '7d' },
  { label: '最近 30 天', value: '30d' },
  { label: '全部时间', value: 'all' },
]

function timeBounds() {
  if (windowKey.value === 'all') return { from: '', to: '' }
  const duration = { '1h': 60 * 60_000, '24h': 24 * 60 * 60_000, '7d': 7 * 24 * 60 * 60_000, '30d': 30 * 24 * 60 * 60_000 }[windowKey.value] ?? 24 * 60 * 60_000
  const to = new Date()
  return { from: new Date(to.getTime() - duration).toISOString(), to: to.toISOString() }
}

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

function currentQuery(cursor: string | null = null): LogQuery {
  const bounds = timeBounds()
  return {
    limit: 50,
    cursor: cursor ?? undefined,
    search: search.value.trim() || undefined,
    error_code: errorCode.value.trim() || undefined,
    status_class: statusClass.value === ANY ? undefined : statusClass.value as LogQuery['status_class'],
    protocol: filterValue(protocol.value) as LogQuery['protocol'],
    routing_mode: filterValue(routingMode.value) as LogQuery['routing_mode'],
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
  error.value = undefined
  try {
    const result = await api.get<Page<LogEvent>>(pageURL('/admin/v1/logs', currentQuery(nextCursor.value)))
    logs.value.push(...result.items)
    nextCursor.value = result.next_cursor
    hasMore.value = result.next_cursor !== null
  } catch (cause) {
    error.value = errorNotice(cause)
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
  return enumLabel('protocol', value)
}

function enumOrDash(namespace: Parameters<typeof enumLabel>[0], value: string | null | undefined) {
  return value === null || value === undefined || value === '' ? '—' : enumLabel(namespace, value)
}

function fallbackOrDash(value: string | null | undefined) {
  return value === null || value === undefined || value === '' ? '—' : fallbackReasonLabel(value)
}

function jevStatusOrDash(value: string | null | undefined) {
  return value === null || value === undefined || value === '' ? '—' : jevStatusLabel(value)
}

// The stored code is read back from the database, so a row written by an older build may
// carry a value that is no longer in the contract vocabulary. `logErrorCodeLabel` falls
// back to the raw token in that case, so an operator can still grep for it.
function errorCodeTitleOrRaw(value: string | null | undefined) {
  return logErrorCodeLabel(value) || '—'
}

function detailErrorDescription(event: LogEvent) {
  const status = httpStatusLabel(event.status)
  const code = event.error_code
  if (!code) return status
  const detail = errorCodeDescription(code)
  return detail ? `${status} · ${detail}` : `${status} · ${code}`
}

async function openDetails(event: LogEvent) {
  selectedLog.value = event
  detailError.value = undefined
  detailOpen.value = true
  detailLoading.value = true
  try {
    const result = await api.get<Page<LogEvent>>(pageURL('/admin/v1/logs', { request_id: event.request_id, limit: 1 }))
    if (result.items[0]) selectedLog.value = result.items[0]
  } catch (cause) {
    detailError.value = errorNotice(cause)
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

    <ErrorAlert v-if="error" :error="error" />

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
          <template #started_at-cell="{ row }"><span class="whitespace-nowrap font-mono text-xs text-muted">{{ formatDate(row.original.started_at) }}</span></template>
          <template #request_id-cell="{ row }"><code class="font-mono text-xs">{{ row.original.request_id }}</code></template>
          <template #protocol-cell="{ row }"><UBadge color="neutral" variant="subtle">{{ protocolLabel(row.original.protocol) }}</UBadge></template>
          <template #requested_model-cell="{ row }"><span class="block max-w-52 truncate font-mono text-xs" :title="row.original.requested_model ?? ''">{{ valueOrDash(row.original.requested_model) }}</span></template>
          <template #status-cell="{ row }"><UBadge :color="statusColor(row.original.status)" variant="subtle">{{ row.original.status }}</UBadge></template>
          <template #error_code-cell="{ row }"><span v-if="row.original.error_code" class="inline-flex flex-wrap items-baseline gap-1"><span class="text-xs text-error">{{ errorCodeTitleOrRaw(row.original.error_code) }}</span><code class="font-mono text-[11px] text-muted" :title="row.original.error_code">{{ row.original.error_code }}</code></span><span v-else class="text-muted">—</span></template>
          <template #provider-cell="{ row }"><span class="text-xs">{{ valueOrDash(row.original.provider) }}</span></template>
          <template #duration_ms-cell="{ row }"><span class="whitespace-nowrap text-xs">{{ row.original.duration_ms }} ms</span></template>
          <template #actions-cell="{ row }"><UButton size="xs" color="neutral" variant="ghost" @click="openDetails(row.original)">查看</UButton></template>
        </UTable>
      </div>
      <div v-if="hasMore" class="flex justify-center px-4 pt-2 pb-4">
        <UButton color="neutral" variant="soft" :loading="loading" @click="fetchLogs(false)">加载更多</UButton>
      </div>
    </UCard>

    <USlideover v-model:open="detailOpen" :title="selectedLog ? `请求详情 · ${selectedLog.request_id}` : '请求详情'">
      <template #body>
        <div v-if="selectedLog" class="space-y-5">
          <ErrorAlert v-if="detailError" :error="detailError" />
          <USkeleton v-if="detailLoading" class="h-10 w-full" />
          <ErrorAlert v-if="selectedLog.error_code && !detailLoading" :title="errorCodeTitleOrRaw(selectedLog.error_code)" :description="detailErrorDescription(selectedLog)" />

          <section class="grid grid-cols-2 gap-x-4 gap-y-3 text-sm">
            <div><div class="text-xs text-muted">开始时间</div><div class="mt-1">{{ formatDate(selectedLog.started_at) }}</div></div>
            <div><div class="text-xs text-muted">协议 / 状态</div><div class="mt-1">{{ protocolLabel(selectedLog.protocol) }} · HTTP {{ selectedLog.status }}</div></div>
            <div><div class="text-xs text-muted">请求模型</div><div class="mt-1 break-all font-mono">{{ valueOrDash(selectedLog.requested_model) }}</div></div>
            <div><div class="text-xs text-muted">生效模型</div><div class="mt-1 break-all font-mono">{{ valueOrDash(selectedLog.effective_model) }}</div></div>
            <div><div class="text-xs text-muted">提供商</div><div class="mt-1">{{ valueOrDash(selectedLog.provider) }}</div></div>
            <div><div class="text-xs text-muted">上游模型</div><div class="mt-1 break-all font-mono">{{ valueOrDash(selectedLog.upstream_model) }}</div></div>
            <div><div class="text-xs text-muted">路由模式 / 选择模式</div><div class="mt-1">{{ enumOrDash('routingMode', selectedLog.routing_mode) }} / {{ enumOrDash('selectionMode', selectedLog.selection_mode) }}</div></div>
            <div><div class="text-xs text-muted">上游状态 / 尝试次数</div><div class="mt-1">{{ valueOrDash(selectedLog.upstream_status) }} / {{ selectedLog.gateway_attempts }}</div></div>
            <div><div class="text-xs text-muted">耗时 / 路由耗时</div><div class="mt-1">{{ selectedLog.duration_ms }} ms / {{ valueOrDash(selectedLog.routing_latency_ms) }} ms</div></div>
            <div><div class="text-xs text-muted">输入 / 输出 Token</div><div class="mt-1">{{ valueOrDash(selectedLog.input_tokens) }} / {{ valueOrDash(selectedLog.output_tokens) }}</div></div>
            <div><div class="text-xs text-muted">Jev 状态</div><div class="mt-1">{{ jevStatusOrDash(selectedLog.jev_status) }}</div></div>
            <div><div class="text-xs text-muted">Fallback 原因</div><div class="mt-1 break-all">{{ fallbackOrDash(selectedLog.fallback_reason) }}</div></div>
            <div><div class="text-xs text-muted">路由偏好</div><div class="mt-1">{{ enumOrDash('routingPreference', selectedLog.routing_preference) }} · {{ enumOrDash('preferenceSource', selectedLog.preference_source) }}</div></div>
            <div><div class="text-xs text-muted">置信度区间</div><div class="mt-1">{{ enumOrDash('confidenceBand', selectedLog.confidence_band) }}</div></div>
            <div><div class="text-xs text-muted">用量观测</div><div class="mt-1">{{ enumOrDash('usageStatus', selectedLog.usage_status) }}</div></div>
          </section>

          <section v-if="selectedLog.attempts?.length" class="space-y-2 pt-2">
            <h4 class="text-sm font-medium">上游尝试</h4>
            <div v-for="attempt in selectedLog.attempts" :key="attempt.id" class="rounded-lg border border-default p-3 text-xs">
              <div class="flex flex-wrap items-center gap-2">
                <UBadge color="neutral" variant="subtle">第 {{ attempt.attempt_index }} 次</UBadge>
                <span>{{ valueOrDash(attempt.provider) }} · {{ valueOrDash(attempt.model_id) }}</span>
                <UBadge v-if="attempt.status" :color="statusColor(attempt.status)" variant="subtle">{{ attempt.status }}</UBadge>
                <span v-if="attempt.error_code" class="inline-flex flex-wrap items-baseline gap-1"><span class="text-error">{{ errorCodeTitleOrRaw(attempt.error_code) }}</span><code class="font-mono text-[11px] text-muted">{{ attempt.error_code }}</code></span>
              </div>
              <div class="mt-2 text-muted">分组 {{ valueOrDash(attempt.group_name) }} · {{ enumLabel('usageStatus', attempt.usage_status) }} · {{ formatDate(attempt.started_at) }} · Token {{ valueOrDash(attempt.input_tokens) }} / {{ valueOrDash(attempt.output_tokens) }}</div>
            </div>
          </section>

          <section v-if="selectedLog.jev_trace" class="space-y-2 pt-2">
            <h4 class="text-sm font-medium">Jev 路由诊断</h4>
            <div class="text-xs text-muted">状态 {{ jevStatusOrDash(selectedLog.jev_trace.status) }} · 候选 {{ selectedLog.jev_trace.candidate_count }} · 模式 {{ enumOrDash('inputMode', selectedLog.jev_trace.input_mode) }}</div>
            <p v-if="selectedLog.jev_trace.failure_reason" class="text-sm text-error">{{ fallbackReasonLabel(selectedLog.jev_trace.failure_reason) }}</p>
            <div class="flex flex-wrap gap-1.5"><UBadge v-for="candidate in selectedLog.jev_trace.candidate_models" :key="candidate" color="neutral" variant="subtle">{{ candidate }}</UBadge></div>
            <div v-for="probability in selectedLog.jev_trace.probabilities" :key="probability.model" class="flex justify-between gap-3 text-xs"><span class="break-all">{{ probability.model }}</span><span>{{ (probability.probability * 100).toFixed(1) }}%</span></div>
          </section>

          <div class="pt-2 text-xs text-muted">Request ID：<code class="break-all">{{ selectedLog.request_id }}</code></div>
        </div>
      </template>
    </USlideover>
  </div>
</template>
