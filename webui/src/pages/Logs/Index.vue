<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { api, pageURL, type LogEvent, type Page } from '@/lib/api'
import { errorNotice } from '@/lib/errors'
import { enumLabel, fallbackReasonLabel, jevStatusLabel, logErrorCodeLabel, usageStatusDescription } from '@/lib/labels'
import ErrorAlert from '@/components/ErrorAlert.vue'
import JfBadge from '@/components/JfBadge.vue'
import JfButton from '@/components/JfButton.vue'
import JfCard from '@/components/JfCard.vue'
import JfDrawer from '@/components/JfDrawer.vue'
import JfEmpty from '@/components/JfEmpty.vue'
import JfInput from '@/components/JfInput.vue'
import JfSelect from '@/components/JfSelect.vue'
import JfTable from '@/components/JfTable.vue'
import type { JfColumn } from '@/lib/table'
import type { operations } from '@/lib/generated-api'

type LogQuery = NonNullable<operations['listAdminLogs']['parameters']['query']>
type FilterOption = { label: string; value: string }

const loading = ref(false)
const logs = ref<LogEvent[]>([])
const nextCursor = ref<string | null>(null)
const hasMore = ref(false)
const error = ref<unknown>()

const ANY = '__any__'
const search = ref('')
const errorCode = ref('')
const statusClass = ref(ANY)
const protocol = ref(ANY)
const routingMode = ref(ANY)
const windowKey = ref('24h')

const detailOpen = ref(false)
const selectedLog = ref<LogEvent>()
const queryRange = ref({ from: '', to: '' })

function withAll(label: string, options: Array<{ label: string; value: string }>): FilterOption[] {
  return [{ label, value: ANY }, ...options]
}

function filterValue(value: string): string | undefined {
  return value === ANY || value === '' ? undefined : value
}

const statusOptions: FilterOption[] = withAll('全部状态', (['success', 'client_error', 'server_error'] as const)
  .map(val => ({ label: enumLabel('statusClass', val), value: val })))

const protocolOptions: FilterOption[] = withAll('全部协议', (['chat_completions', 'responses', 'native'] as const)
  .map(val => ({ label: enumLabel('protocol', val), value: val })))

const routingOptions: FilterOption[] = withAll('全部路由模式', (['auto', 'explicit'] as const)
  .map(val => ({ label: enumLabel('routingMode', val), value: val })))

const timeOptions: FilterOption[] = [
  { label: '最近 1 小时', value: '1h' },
  { label: '最近 6 小时', value: '6h' },
  { label: '最近 24 小时', value: '24h' },
  { label: '最近 7 天', value: '7d' },
  { label: '最近 30 天', value: '30d' },
]

const columns: JfColumn[] = [
  { key: 'started_at', title: '时间', nowrap: true },
  { key: 'request_id', title: '请求 ID', nowrap: true },
  { key: 'protocol', title: '协议', nowrap: true },
  { key: 'requested_model', title: '请求模型' },
  { key: 'status', title: 'HTTP 状态', nowrap: true },
  { key: 'error_code', title: '错误状态' },
  { key: 'provider', title: '执行提供商', nowrap: true },
  { key: 'duration_ms', title: '耗时', align: 'end', nowrap: true },
  { key: 'actions', title: '明细', nowrap: true },
]

function currentQuery(cursor?: string | null): LogQuery {
  const query: LogQuery = {
    limit: 50,
    from: queryRange.value.from,
    to: queryRange.value.to,
    search: search.value.trim() || undefined,
    error_code: errorCode.value.trim() || undefined,
    status_class: filterValue(statusClass.value) as LogQuery['status_class'],
    protocol: filterValue(protocol.value) as LogQuery['protocol'],
    routing_mode: filterValue(routingMode.value) as LogQuery['routing_mode'],
    cursor: cursor || undefined,
  }
  return query
}

let logsRequestId = 0

async function fetchLogs(reset = true) {
  if (searchTimer !== undefined) clearTimeout(searchTimer)
  searchTimer = undefined
  const requestId = ++logsRequestId
  if (reset) {
    const to = new Date()
    const durationMs: Record<string, number> = {
      '1h': 60 * 60 * 1000,
      '6h': 6 * 60 * 60 * 1000,
      '24h': 24 * 60 * 60 * 1000,
      '7d': 7 * 24 * 60 * 60 * 1000,
      '30d': 30 * 24 * 60 * 60 * 1000,
    }
    queryRange.value = {
      from: new Date(to.getTime() - durationMs[windowKey.value]!).toISOString(),
      to: to.toISOString(),
    }
    nextCursor.value = null
    logs.value = []
  }
  loading.value = true
  error.value = undefined
  try {
    const result = await api.get<Page<LogEvent>>(pageURL('/admin/v1/logs', currentQuery(nextCursor.value)))
    // A newer query replaced this one while it was in flight; its rows would mix filters.
    if (requestId !== logsRequestId) return
    logs.value.push(...result.items)
    nextCursor.value = result.next_cursor
    hasMore.value = result.next_cursor !== null
  } catch (cause) {
    if (requestId === logsRequestId) error.value = errorNotice(cause)
  } finally {
    if (requestId === logsRequestId) loading.value = false
  }
}

// Text filters apply as the operator types, matching the select filters beside them.
let searchTimer: ReturnType<typeof setTimeout> | undefined
watch([search, errorCode], () => {
  if (searchTimer !== undefined) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { void fetchLogs(true) }, 300)
})

function formatDate(value: string | null | undefined) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? value
    : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(date)
}

function valueOrDash(value: string | number | boolean | null | undefined) {
  return value === null || value === undefined || value === '' ? '—' : String(value)
}

function jevConfidence(log: LogEvent) {
  return log.jev_status === 'ok' && log.confidence != null
    ? log.confidence.toFixed(2)
    : '未产生推荐置信度'
}

function stageLatency(value: number | null | undefined, missing: string) {
  return value == null ? missing : `${value} ms`
}

function statusColor(status: number): 'danger' | 'warning' | 'success' {
  if (status >= 500) return 'danger'
  if (status >= 400) return 'warning'
  return 'success'
}

function protocolLabel(value: string) {
  return enumLabel('protocol', value)
}

function openDetails(log: LogEvent) {
  selectedLog.value = log
  detailOpen.value = true
}

function errorCodeTitleOrRaw(code: string | null | undefined): string {
  if (!code) return ''
  return logErrorCodeLabel(code)
}

onMounted(() => { void fetchLogs(true) })
</script>

<template>
  <div class="jf-stack">
    <!-- Toolbar -->
    <section class="jf-toolbar">
      <div>
        <h1 class="jf-page-title">请求日志</h1>
      </div>
      <div class="jf-action-group">
        <JfButton variant="secondary" icon="arrow-path" :loading="loading" @click="fetchLogs(true)">刷新日志</JfButton>
      </div>
    </section>

    <ErrorAlert v-if="error" :error="error" />

    <!-- Filters Row -->
    <div class="jf-field-row">
      <JfInput
        v-model="search"
        icon="magnifying-glass"
        placeholder="搜索请求 ID、模型或提供商"
        aria-label="搜索请求 ID、模型或提供商"
        class="basis-56 grow"
        @keydown.enter="fetchLogs(true)"
      />
      <JfInput
        v-model="errorCode"
        placeholder="错误码（如 no_eligible_candidate）"
        aria-label="按错误码筛选"
        class="basis-56 grow font-mono"
        @keydown.enter="fetchLogs(true)"
      />
      <JfSelect
        v-model="statusClass"
        :items="statusOptions"
        label="HTTP 状态筛选"
        class="w-40 shrink-0"
        @update:model-value="fetchLogs(true)"
      />
      <JfSelect
        v-model="protocol"
        :items="protocolOptions"
        label="协议筛选"
        class="w-36 shrink-0"
        @update:model-value="fetchLogs(true)"
      />
      <JfSelect
        v-model="routingMode"
        :items="routingOptions"
        label="路由模式"
        class="w-36 shrink-0"
        @update:model-value="fetchLogs(true)"
      />
      <JfSelect
        v-model="windowKey"
        :items="timeOptions"
        label="时间跨度"
        class="w-36 shrink-0"
        @update:model-value="fetchLogs(true)"
      />
    </div>

    <!-- Request Logs Card -->
    <JfCard flush>
      <template #header>
        <div class="flex items-center gap-2">
          <h2 class="jf-module-title">请求记录</h2>
          <JfBadge tone="neutral">{{ logs.length }}</JfBadge>
        </div>
      </template>

      <div class="jf-scroll-x">
        <JfTable :rows="logs" :columns="columns" row-key="id" :loading="loading">
          <template #empty>
            <JfEmpty variant="filter" title="当前筛选条件下没有匹配的请求日志" />
          </template>

          <template #cell-started_at="{ row }">
            <span class="jf-caption jf-mono text-ink-secondary">{{ formatDate(row.started_at) }}</span>
          </template>

          <template #cell-request_id="{ row }">
            <code class="jf-caption jf-mono font-medium">{{ row.request_id }}</code>
          </template>

          <template #cell-protocol="{ row }">
            <JfBadge tone="neutral">{{ protocolLabel(row.protocol) }}</JfBadge>
          </template>

          <template #cell-requested_model="{ row }">
            <span class="jf-caption jf-mono jf-truncate block max-w-52" :title="row.requested_model ?? ''">
              {{ valueOrDash(row.requested_model) }}
            </span>
          </template>

          <template #cell-status="{ row }">
            <JfBadge :tone="statusColor(row.status)">{{ row.status }}</JfBadge>
          </template>

          <template #cell-error_code="{ row }">
            <span v-if="row.error_code" class="inline-flex flex-wrap items-baseline gap-1">
              <span class="text-danger font-medium">{{ errorCodeTitleOrRaw(row.error_code) }}</span>
              <code class="jf-caption jf-mono text-ink-secondary" :title="row.error_code">{{ row.error_code }}</code>
            </span>
            <span v-else class="text-ink-secondary">—</span>
          </template>

          <template #cell-provider="{ row }">
            <span class="font-mono text-xs">{{ valueOrDash(row.provider) }}</span>
          </template>

          <template #cell-duration_ms="{ row }">
            <span class="jf-mono jf-tabular">{{ row.duration_ms }} ms</span>
          </template>

          <template #cell-actions="{ row }">
            <JfButton size="sm" variant="ghost" @click="openDetails(row)">查看明细</JfButton>
          </template>
        </JfTable>
      </div>

      <template v-if="hasMore" #footer>
        <div class="flex justify-center">
          <JfButton variant="secondary" :loading="loading" @click="fetchLogs(false)">加载更多记录</JfButton>
        </div>
      </template>
    </JfCard>

    <!-- Detailed Log Inspector Drawer -->
    <JfDrawer v-model:open="detailOpen" :title="`请求明细 · ${selectedLog?.request_id}`" size="lg">
      <div v-if="selectedLog" class="grid min-w-0 grid-cols-1 gap-5">
        <!-- Overview Grid -->
        <div class="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2 rounded-[var(--jf-radius-control)] border border-line p-4">
          <div>
            <span class="jf-caption text-ink-secondary block">请求时间</span>
            <span class="jf-mono text-sm">{{ formatDate(selectedLog.started_at) }}</span>
          </div>
          <div>
            <span class="jf-caption text-ink-secondary block">HTTP 状态 / 请求总耗时</span>
            <span class="jf-mono text-sm font-medium">
              <JfBadge :tone="statusColor(selectedLog.status)">HTTP {{ selectedLog.status }}</JfBadge>
              <span class="ml-2">· {{ selectedLog.duration_ms }} ms</span>
            </span>
          </div>
          <div>
            <span class="jf-caption text-ink-secondary block">请求协议 / 路由模式</span>
            <span class="text-sm font-medium">
              {{ protocolLabel(selectedLog.protocol) }} · {{ enumLabel('routingMode', selectedLog.routing_mode) }}
            </span>
          </div>
          <div v-if="selectedLog.requested_model">
            <span class="jf-caption text-ink-secondary block">客户端请求模型</span>
            <code class="font-mono text-sm">{{ selectedLog.requested_model }}</code>
          </div>
          <div v-if="selectedLog.provider || selectedLog.effective_model">
            <span class="jf-caption text-ink-secondary block">最终分派提供商 / 模型</span>
            <span class="font-mono text-sm font-medium">
              {{ selectedLog.provider || '—' }} · {{ selectedLog.effective_model || '—' }}
            </span>
          </div>
          <div v-if="selectedLog.selection_mode">
            <span class="jf-caption text-ink-secondary block">模型选择规则</span>
            <span class="text-sm">{{ enumLabel('selectionMode', selectedLog.selection_mode) }}</span>
          </div>
          <template v-if="selectedLog.routing_mode === 'auto'">
            <div>
              <span class="jf-caption text-ink-secondary block">Jev 任务组推荐状态</span>
              <span class="text-sm">{{ jevStatusLabel(selectedLog.jev_status) || '—' }}</span>
            </div>
            <div>
              <span class="jf-caption text-ink-secondary block">Jev 选组置信度 / 默认组兜底原因</span>
              <span class="text-sm">{{ jevConfidence(selectedLog) }}<template v-if="selectedLog.fallback_reason"> · {{ fallbackReasonLabel(selectedLog.fallback_reason) }}</template></span>
            </div>
            <div>
              <span class="jf-caption text-ink-secondary block">最终任务组</span>
              <span class="text-sm">{{ enumLabel('group', selectedLog.attempts?.at(-1)?.group_name ?? selectedLog.jev_trace?.selected_group) || '未记录' }}</span>
            </div>
            <div>
              <span class="jf-caption text-ink-secondary block">Jev 调用耗时 / 路由总耗时</span>
              <span class="jf-mono text-sm">{{ stageLatency(selectedLog.jev_latency_ms, '未调用或未记录') }} / {{ stageLatency(selectedLog.routing_latency_ms, '未执行或未记录') }}</span>
            </div>
            <p class="jf-caption text-ink-secondary sm:col-span-2">Jev 负责选任务组，模型按组内配置顺序选择。“组内按配置顺序选模型”与 Jev 推荐成功可以同时出现。</p>
          </template>
          <div>
            <span class="jf-caption text-ink-secondary block">已发送给客户端的响应字节数</span>
            <span class="jf-mono text-sm">{{ selectedLog.bytes_written.toLocaleString() }} B</span>
          </div>
          <div v-if="selectedLog.client_ip">
            <span class="jf-caption text-ink-secondary block">客户端 IP（已启用记录）</span>
            <code class="jf-mono text-sm jf-anywhere">{{ selectedLog.client_ip }}</code>
          </div>
          <div v-if="selectedLog.upstream_status != null && selectedLog.upstream_status !== selectedLog.status">
            <span class="jf-caption text-ink-secondary block">上游 HTTP 状态（与客户端不同）</span>
            <JfBadge :tone="statusColor(selectedLog.upstream_status)">{{ selectedLog.upstream_status }}</JfBadge>
          </div>
          <div v-if="selectedLog.error_code" class="sm:col-span-2">
            <span class="jf-caption text-ink-secondary block">请求失败原因</span>
            <span class="text-sm text-danger">{{ errorCodeTitleOrRaw(selectedLog.error_code) }}</span>
            <code class="jf-caption jf-mono ml-2 jf-anywhere">{{ selectedLog.error_code }}</code>
          </div>
        </div>

        <!-- Token Usage -->
        <div class="rounded-[var(--jf-radius-control)] border border-line p-4">
          <div class="mb-2 flex flex-wrap items-center gap-2">
            <h3 class="jf-module-title">上游模型 Token 用量</h3>
            <JfBadge tone="neutral">{{ enumLabel('usageStatus', selectedLog.usage_status) }}</JfBadge>
          </div>
          <p class="jf-caption mb-3 text-ink-secondary">{{ usageStatusDescription(selectedLog.usage_status) }}</p>
          <div class="grid grid-cols-3 gap-2 jf-mono text-sm">
            <div>
              <span class="jf-caption text-ink-secondary block">输入 Token</span>
              <strong>{{ selectedLog.input_tokens?.toLocaleString() ?? '—' }}</strong>
            </div>
            <div>
              <span class="jf-caption text-ink-secondary block">输出 Token</span>
              <strong>{{ selectedLog.output_tokens?.toLocaleString() ?? '—' }}</strong>
            </div>
            <div>
              <span class="jf-caption text-ink-secondary block">总 Token</span>
              <strong>{{ selectedLog.total_tokens?.toLocaleString() ?? '—' }}</strong>
            </div>
          </div>
        </div>

        <section v-if="selectedLog.attempts?.length || selectedLog.gateway_attempts > 0" aria-label="上游尝试记录" class="min-w-0 rounded-[var(--jf-radius-control)] border border-line p-4">
          <h3 class="jf-module-title mb-2">上游尝试记录</h3>
          <p class="jf-caption mb-3 text-ink-secondary">共 {{ selectedLog.gateway_attempts }} 次尝试 · {{ selectedLog.failover_used ? '已使用故障转移' : '未使用故障转移' }}。以下为已持久化记录；旧日志可能缺少逐次明细。</p>
          <ol v-if="selectedLog.attempts?.length" class="grid gap-3">
            <li v-for="attempt in selectedLog.attempts" :key="attempt.id" class="min-w-0 rounded-[var(--jf-radius-control)] bg-tonal p-3">
              <div class="flex flex-wrap items-center gap-2 text-sm">
                <span>第 {{ attempt.attempt_index }} 次</span>
                <JfBadge v-if="attempt.status != null" :tone="statusColor(attempt.status)">HTTP {{ attempt.status }}</JfBadge>
                <JfBadge tone="neutral">{{ enumLabel('usageStatus', attempt.usage_status) }}</JfBadge>
              </div>
              <p class="jf-caption jf-mono mt-2 jf-anywhere">{{ valueOrDash(attempt.provider) }} / {{ valueOrDash(attempt.model_id) }}<template v-if="attempt.group_name"> · {{ enumLabel('group', attempt.group_name) }}</template></p>
              <p class="jf-caption mt-1 text-ink-secondary">开始 {{ formatDate(attempt.started_at) }}<template v-if="attempt.completed_at"> · 完成 {{ formatDate(attempt.completed_at) }}</template></p>
              <p v-if="attempt.error_code" class="jf-caption mt-2 text-danger jf-anywhere">{{ errorCodeTitleOrRaw(attempt.error_code) }} · {{ attempt.error_code }}</p>
              <p v-if="attempt.error_detail" class="jf-caption mt-1 jf-anywhere">{{ attempt.error_detail }}</p>
              <p v-if="attempt.input_tokens != null || attempt.output_tokens != null || attempt.total_tokens != null" class="jf-caption jf-mono mt-2">Token 输入 {{ valueOrDash(attempt.input_tokens) }} / 输出 {{ valueOrDash(attempt.output_tokens) }} / 总计 {{ valueOrDash(attempt.total_tokens) }}</p>
            </li>
          </ol>
        </section>

        <details v-if="selectedLog.diagnostics || selectedLog.jev_trace" :key="`diagnostics-${selectedLog.id}`" class="min-w-0 rounded-[var(--jf-radius-control)] border border-line p-4">
          <summary class="jf-module-title cursor-pointer">诊断信息（按需展开）</summary>
          <p class="jf-caption my-3 text-ink-secondary">指纹用于核对决策元数据，不是提示词哈希，也不参与选模型。历史分档仅保留用于核对旧记录。</p>
          <dl class="grid gap-3 text-sm">
            <div v-if="selectedLog.diagnostics?.evidence_hash">
              <dt class="jf-caption text-ink-secondary">决策元数据指纹</dt>
              <dd class="jf-mono jf-anywhere">{{ selectedLog.diagnostics.evidence_hash }}</dd>
            </div>
            <div v-if="selectedLog.diagnostics?.legacy_confidence_band || selectedLog.diagnostics?.legacy_trace_confidence_band">
              <dt class="jf-caption text-ink-secondary">历史置信度分档</dt>
              <dd>{{ enumLabel('confidenceBand', selectedLog.diagnostics.legacy_confidence_band) || '未记录' }}<template v-if="selectedLog.diagnostics.legacy_trace_confidence_band"> · 历史追踪分档 {{ enumLabel('confidenceBand', selectedLog.diagnostics.legacy_trace_confidence_band) }}</template></dd>
            </div>
            <div v-if="selectedLog.jev_trace">
              <dt class="jf-caption text-ink-secondary">Jev 输入模式 / 候选数量</dt>
              <dd>{{ enumLabel('inputMode', selectedLog.jev_trace.input_mode) }} · {{ selectedLog.jev_trace.candidate_count }} 个提供商/模型候选 · {{ selectedLog.jev_trace.candidate_groups.length }} 个任务组</dd>
            </div>
            <div v-if="selectedLog.jev_trace?.recommended_group">
              <dt class="jf-caption text-ink-secondary">Jev 原始推荐组（兜底前）</dt>
              <dd>{{ enumLabel('group', selectedLog.jev_trace.recommended_group) }}</dd>
            </div>
            <div v-if="selectedLog.jev_trace?.failure_reason">
              <dt class="jf-caption text-ink-secondary">Jev 失败详情</dt>
              <dd>{{ fallbackReasonLabel(selectedLog.jev_trace.failure_reason) }}</dd>
            </div>
          </dl>
        </details>

        <details :key="`json-${selectedLog.id}`" class="min-w-0 rounded-[var(--jf-radius-control)] border border-line p-4">
          <summary class="jf-module-title cursor-pointer">结构化路由日志（JSON）</summary>
          <p class="jf-caption my-3 text-ink-secondary">只包含路由、耗时、逐次尝试和用量，不包含原始请求正文、响应正文或 Jev 提示词；失败尝试可能含有界限内脱敏错误摘录。未知或不适用的字段会省略；“—”及旧日志的 null 不代表 0。</p>
          <pre class="max-h-96 overflow-auto rounded-[var(--jf-radius-control)] bg-tonal p-3.5 font-mono text-xs leading-relaxed jf-anywhere">{{ JSON.stringify(selectedLog, null, 2) }}</pre>
        </details>
      </div>

      <template #footer>
        <div class="jf-action-group justify-end">
          <JfButton variant="secondary" @click="detailOpen = false">关闭</JfButton>
        </div>
      </template>
    </JfDrawer>
  </div>
</template>
