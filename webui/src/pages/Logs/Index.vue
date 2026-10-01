<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, pageURL, type LogEvent, type Page } from '@/lib/api'
import { errorCodeDescription, errorNotice } from '@/lib/errors'
import { enumLabel, fallbackReasonLabel, jevStatusLabel, logErrorCodeLabel } from '@/lib/labels'
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

async function fetchLogs(reset = true) {
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
  return Number.isNaN(date.getTime())
    ? value
    : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(date)
}

function valueOrDash(value: string | number | boolean | null | undefined) {
  return value === null || value === undefined || value === '' ? '—' : String(value)
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
      <div v-if="selectedLog" class="grid gap-5">
        <!-- Overview Grid -->
        <div class="grid gap-3 sm:grid-cols-2 rounded-[var(--jf-radius-control)] border border-line p-4">
          <div>
            <span class="jf-caption text-ink-secondary block">请求时间</span>
            <span class="jf-mono text-sm">{{ formatDate(selectedLog.started_at) }}</span>
          </div>
          <div>
            <span class="jf-caption text-ink-secondary block">响应状态与耗时</span>
            <span class="jf-mono text-sm font-medium">
              <JfBadge :tone="statusColor(selectedLog.status)">{{ selectedLog.status }}</JfBadge>
              <span class="ml-2">{{ selectedLog.duration_ms }} ms</span>
            </span>
          </div>
          <div>
            <span class="jf-caption text-ink-secondary block">请求协议 / 路由模式</span>
            <span class="text-sm font-medium">
              {{ protocolLabel(selectedLog.protocol) }} · {{ enumLabel('routingMode', selectedLog.routing_mode) }}
            </span>
          </div>
          <div>
            <span class="jf-caption text-ink-secondary block">客户端请求模型</span>
            <code class="font-mono text-sm">{{ selectedLog.requested_model }}</code>
          </div>
          <div>
            <span class="jf-caption text-ink-secondary block">最终分派提供商 / 模型</span>
            <span class="font-mono text-sm font-medium">
              {{ selectedLog.provider || '—' }} · {{ selectedLog.effective_model || '—' }}
            </span>
          </div>
          <div>
            <span class="jf-caption text-ink-secondary block">决策方式</span>
            <span class="text-sm">{{ enumLabel('selectionMode', selectedLog.selection_mode) }}</span>
          </div>
        </div>

        <!-- Token Usage -->
        <div v-if="selectedLog.input_tokens != null || selectedLog.output_tokens != null || selectedLog.total_tokens != null" class="rounded-[var(--jf-radius-control)] border border-line p-4">
          <h3 class="jf-module-title mb-2">Token 统计</h3>
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

        <!-- Raw Log JSON payload -->
        <div>
          <h3 class="jf-module-title mb-2">原始事件载荷 (Raw JSON)</h3>
          <pre class="max-h-96 overflow-auto rounded-[var(--jf-radius-control)] bg-tonal p-3.5 font-mono text-xs leading-relaxed jf-anywhere">{{ JSON.stringify(selectedLog, null, 2) }}</pre>
        </div>
      </div>

      <template #footer>
        <div class="jf-action-group justify-end">
          <JfButton variant="secondary" @click="detailOpen = false">关闭</JfButton>
        </div>
      </template>
    </JfDrawer>
  </div>
</template>
