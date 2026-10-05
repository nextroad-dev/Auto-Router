<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, formatRate } from '@/lib/api'
import { errorNotice } from '@/lib/errors'
import ErrorAlert from '@/components/ErrorAlert.vue'
import JfAlert from '@/components/JfAlert.vue'
import JfButton from '@/components/JfButton.vue'
import JfCard from '@/components/JfCard.vue'
import JfIcon from '@/components/JfIcon.vue'
import JfSelect from '@/components/JfSelect.vue'
import JfTable from '@/components/JfTable.vue'
import type { JfColumn } from '@/lib/table'
import { selectLatencyMetrics } from '@/lib/dashboard-metrics'
import type { DashboardReport } from '@/lib/admin-contracts'
import type { components } from '@/lib/generated-api'

type AdminHealth = components['schemas']['AdminHealth']
type LogSummary = components['schemas']['LogSummary']
type SummaryWindow = '1h' | '24h' | '7d' | '30d'

const report = ref<DashboardReport>()
const loading = ref(false)
const error = ref<unknown>()
const health = ref<AdminHealth>()
const summary = ref<LogSummary>()
const healthLoading = ref(false)
const summaryLoading = ref(false)
const healthError = ref<unknown>()
const summaryError = ref<unknown>()
const summaryWindow = ref<SummaryWindow>('24h')

const modelView = ref<'chart' | 'table'>('chart')
const groupView = ref<'chart' | 'table'>('chart')

const windowOptions: Array<{ label: string; value: SummaryWindow }> = [
  { label: '最近 1 小时', value: '1h' },
  { label: '最近 24 小时', value: '24h' },
  { label: '最近 7 天', value: '7d' },
  { label: '最近 30 天', value: '30d' },
]

const modelColumns: JfColumn[] = [
  { key: 'id', title: '逻辑模型', width: '16rem' },
  { key: 'requests', title: '调用次数', align: 'end' },
  { key: 'input_tokens', title: '输入 Token', align: 'end' },
  { key: 'output_tokens', title: '输出 Token', align: 'end' },
  { key: 'total_tokens', title: '总 Token', align: 'end' },
]

const groupColumns: JfColumn[] = [
  { key: 'id', title: '模型分组', nowrap: true },
  { key: 'requests', title: '调用次数', align: 'end' },
  { key: 'input_tokens', title: '输入 Token', align: 'end' },
  { key: 'output_tokens', title: '输出 Token', align: 'end' },
  { key: 'total_tokens', title: '总 Token', align: 'end' },
]

let refreshTimer: ReturnType<typeof setInterval> | undefined
let operationsTimer: ReturnType<typeof setInterval> | undefined
let summaryRequestId = 0
let dashboardRequestId = 0

async function loadHealth() {
  if (healthLoading.value) return
  healthLoading.value = true
  healthError.value = undefined
  try {
    health.value = await api.get<AdminHealth>('/admin/v1/health')
  } catch (cause) {
    healthError.value = errorNotice(cause)
  } finally {
    healthLoading.value = false
  }
}

async function loadSummary(window = summaryWindow.value) {
  const requestId = ++summaryRequestId
  summaryLoading.value = true
  summaryError.value = undefined
  try {
    const result = await api.get<LogSummary>(`/admin/v1/logs/summary?window=${encodeURIComponent(window)}`)
    if (requestId === summaryRequestId) summary.value = result
  } catch (cause) {
    if (requestId === summaryRequestId) summaryError.value = errorNotice(cause)
  } finally {
    if (requestId === summaryRequestId) summaryLoading.value = false
  }
}

async function refreshOperations() {
  await Promise.all([loadHealth(), loadSummary()])
}

async function refreshAll() {
  await Promise.all([loadDashboard(), refreshOperations()])
}

// Usage rankings follow the same reporting window as the summary so one screen never
// mixes periods; only output TPS is a fixed 60-second figure, and its card says so.
async function loadDashboard(window = summaryWindow.value) {
  const requestId = ++dashboardRequestId
  loading.value = true
  error.value = undefined
  try {
    const result = await api.get<DashboardReport>(`/admin/v1/dashboard?window=${encodeURIComponent(window)}`)
    if (requestId === dashboardRequestId) report.value = result
  } catch (cause) {
    if (requestId === dashboardRequestId) error.value = errorNotice(cause)
  } finally {
    if (requestId === dashboardRequestId) loading.value = false
  }
}

onMounted(() => {
  void loadDashboard()
  void refreshOperations()
  refreshTimer = setInterval(() => {
    if (document.visibilityState === 'visible' && !loading.value) void loadDashboard()
  }, 5000)
  operationsTimer = setInterval(() => {
    if (document.visibilityState === 'visible') void refreshOperations()
  }, 30000)
})

onUnmounted(() => {
  if (refreshTimer !== undefined) clearInterval(refreshTimer)
  if (operationsTimer !== undefined) clearInterval(operationsTimer)
})

function changeSummaryWindow(next: SummaryWindow) {
  summaryWindow.value = next
  void loadSummary(next)
  void loadDashboard(next)
}

function count(value: number | null | undefined) {
  return value === null || value === undefined ? '—' : new Intl.NumberFormat('zh-CN').format(value)
}

function rate(value: number | null | undefined) {
  return value === null || value === undefined ? '—' : formatRate(value)
}

function tps(value: number | null | undefined) {
  return value === null || value === undefined || !Number.isFinite(value) ? '—' : value.toFixed(2)
}

function rowName(row: { id: string; name?: string }) {
  return row.name?.trim() || row.id
}

const healthLabel = computed(() => {
  if (health.value?.status === 'ok') return '运行正常'
  if (health.value?.status === 'not_ready') return '未就绪'
  if (healthError.value) return '状态未知'
  return healthLoading.value ? '检查中…' : '—'
})

const tpsWindowLabel = '近 60 秒'

function summaryRate(name: string) {
  return summary.value?.rates.find(item => item.name === name)
}

// Donut Chart for HTTP status distribution
const totalStatusCount = computed(() => {
  if (!summary.value?.statuses) return 0
  const { success = 0, client_error = 0, server_error = 0 } = summary.value.statuses
  return success + client_error + server_error
})

const statusDonut = computed(() => {
  const total = totalStatusCount.value
  const s = summary.value?.statuses
  const success = s?.success ?? 0
  const client = s?.client_error ?? 0
  const server = s?.server_error ?? 0

  const C = 2 * Math.PI * 45 // 282.743
  if (total <= 0) {
    return {
      empty: true,
      slices: [],
      successCount: 0,
      clientCount: 0,
      serverCount: 0,
      successPct: '0.0',
      clientPct: '0.0',
      serverPct: '0.0',
    }
  }

  const pSuccess = success / total
  const pClient = client / total
  const pServer = server / total

  const lenSuccess = pSuccess * C
  const lenClient = pClient * C
  const lenServer = pServer * C

  return {
    empty: false,
    successCount: success,
    clientCount: client,
    serverCount: server,
    successPct: (pSuccess * 100).toFixed(1),
    clientPct: (pClient * 100).toFixed(1),
    serverPct: (pServer * 100).toFixed(1),
    slices: [
      {
        key: 'success',
        label: '成功 (2xx)',
        count: success,
        percent: (pSuccess * 100).toFixed(1),
        stroke: 'var(--jf-success-text)',
        dasharray: `${lenSuccess} ${C}`,
        dashoffset: 0,
      },
      {
        key: 'client_error',
        label: '客户端错误 (4xx)',
        count: client,
        percent: (pClient * 100).toFixed(1),
        stroke: 'var(--jf-warning-text)',
        dasharray: `${lenClient} ${C}`,
        dashoffset: -lenSuccess,
      },
      {
        key: 'server_error',
        label: '服务端错误 (5xx)',
        count: server,
        percent: (pServer * 100).toFixed(1),
        stroke: 'var(--jf-danger-text)',
        dasharray: `${lenServer} ${C}`,
        dashoffset: -(lenSuccess + lenClient),
      },
    ],
  }
})

// Circular gauge stroke calculator (radius = 34, C = 213.628)
function ringDash(rateVal: number | null | undefined, radius = 34) {
  const C = 2 * Math.PI * radius
  if (rateVal == null || !Number.isFinite(rateVal) || rateVal <= 0) return `0 ${C.toFixed(1)}`
  const ratio = Math.min(1, Math.max(0, rateVal))
  return `${(ratio * C).toFixed(1)} ${C.toFixed(1)}`
}

// Models ranked list
const rankedModels = computed(() => {
  const list = [...(report.value?.models ?? [])]
  list.sort((a, b) => (b.requests || 0) - (a.requests || 0))
  return list
})

const maxModelReq = computed(() => {
  return Math.max(1, ...rankedModels.value.map(m => m.requests || 0))
})

const totalModelReq = computed(() => {
  return rankedModels.value.reduce((sum, m) => sum + (m.requests || 0), 0)
})

// Groups ranked list
const rankedGroups = computed(() => {
  const list = [...(report.value?.groups ?? [])]
  list.sort((a, b) => (b.requests || 0) - (a.requests || 0))
  return list
})

const maxGroupReq = computed(() => {
  return Math.max(1, ...rankedGroups.value.map(g => g.requests || 0))
})

const totalGroupReq = computed(() => {
  return rankedGroups.value.reduce((sum, g) => sum + (g.requests || 0), 0)
})

// Token breakdown
const tokenBreakdown = computed(() => {
  const input = summary.value?.tokens.input_tokens ?? 0
  const output = summary.value?.tokens.output_tokens ?? 0
  const total = input + output
  const inPct = total > 0 ? (input / total) * 100 : 0
  const outPct = total > 0 ? (output / total) * 100 : 0
  return {
    empty: total <= 0,
    input,
    output,
    total,
    inPct: inPct.toFixed(1),
    outPct: outPct.toFixed(1),
  }
})

// Auto Routing vs Explicit Ratio
const routeRatio = computed(() => {
  const auto = summary.value?.requests.auto ?? 0
  const explicit = summary.value?.requests.explicit ?? 0
  const total = auto + explicit
  const autoPct = total > 0 ? (auto / total) * 100 : 0
  const explicitPct = total > 0 ? (explicit / total) * 100 : 0
  return {
    auto,
    explicit,
    total,
    autoPct: autoPct.toFixed(1),
    explicitPct: explicitPct.toFixed(1),
  }
})

// Latency benchmark values
const latencyMetrics = computed(() => selectLatencyMetrics(summary.value?.latency))
const meanLatency = computed(() => latencyMetrics.value.meanMs)
const p95Latency = computed(() => latencyMetrics.value.p95Ms)
const latencySourceLabel = computed(() => latencyMetrics.value.source === 'auto' ? '自动请求' : latencyMetrics.value.source === 'all' ? '全部请求' : '请求')
</script>

<template>
  <div class="dashboard-root flex flex-col gap-4">
    <!-- 顶部紧凑控制栏：消除顶部大面积留白 -->
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <h1 class="jf-page-title text-2xl font-medium leading-none">总览</h1>
        <span
          class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium"
          :class="health?.status === 'ok' ? 'bg-success-bg text-success' : 'bg-warning-bg text-warning'"
        >
          <span class="h-1.5 w-1.5 rounded-full" :class="health?.status === 'ok' ? 'bg-success' : 'bg-warning'"></span>
          {{ health?.status === 'ok' ? '服务运行正常' : health?.status === 'not_ready' ? '系统未就绪' : healthError ? '健康状态未知' : '检查中…' }}
        </span>
      </div>

      <div class="flex items-center gap-2">
        <JfSelect
          :model-value="summaryWindow"
          :items="windowOptions"
          label="时间窗口"
          size="sm"
          class="w-32"
          @update:model-value="changeSummaryWindow"
        />
        <JfButton
          variant="secondary"
          size="sm"
          icon="arrow-path"
          :loading="loading || healthLoading || summaryLoading"
          @click="refreshAll"
        >
          刷新
        </JfButton>
      </div>
    </div>

    <ErrorAlert v-if="error" :error="error">
      <template #actions>
        <JfButton variant="danger-ghost" size="sm" @click="loadDashboard()">重试</JfButton>
      </template>
    </ErrorAlert>
    <ErrorAlert v-if="healthError" :error="healthError" />
    <ErrorAlert v-if="summaryError" :error="summaryError" />
    <JfAlert v-if="summary?.log_disabled" tone="warning" title="持久路由日志当前已停用；汇总中的空数据不代表 0 次请求。" />

    <!-- 1. 核心指标卡片：纯净数据展示，无多余说明干扰，每张卡片配备专属图标 -->
    <section class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <JfCard density="compact">
        <div class="flex items-center justify-between">
          <span class="jf-caption font-medium text-ink-secondary">系统健康</span>
          <span class="metric-icon-wrap">
            <JfIcon name="circle-check" size="sm" :class="health?.status === 'ok' ? 'text-success' : 'text-ink-secondary'" />
          </span>
        </div>
        <div class="jf-metric mt-2">{{ healthLabel }}</div>
      </JfCard>

      <JfCard density="compact">
        <div class="flex items-center justify-between">
          <span class="jf-caption font-medium text-ink-secondary">请求总量</span>
          <span class="metric-icon-wrap">
            <JfIcon name="queue-list" size="sm" class="text-ink-secondary" />
          </span>
        </div>
        <div class="jf-metric mt-2">{{ count(summary?.requests.total) }} <span class="jf-caption font-normal text-ink-secondary">条</span></div>
      </JfCard>

      <JfCard density="compact">
        <div class="flex items-center justify-between">
          <span class="jf-caption font-medium text-ink-secondary">请求成功率</span>
          <span class="metric-icon-wrap">
            <JfIcon name="check-circle" size="sm" class="text-success" />
          </span>
        </div>
        <div class="jf-metric mt-2">{{ rate(summaryRate('client_request_success_rate')?.value) }}</div>
      </JfCard>

      <JfCard density="compact">
        <div class="flex items-center justify-between">
          <span class="jf-caption font-medium text-ink-secondary">输出 TPS（{{ tpsWindowLabel }}）</span>
          <span class="metric-icon-wrap">
            <JfIcon name="gauge" size="sm" class="text-ink-secondary" />
          </span>
        </div>
        <div class="jf-metric mt-2">{{ tps(report?.output_tps_60s) }}</div>
      </JfCard>

      <JfCard density="compact">
        <div class="flex items-center justify-between">
          <span class="jf-caption font-medium text-ink-secondary">Token 吞吐总量</span>
          <span class="metric-icon-wrap">
            <JfIcon name="cpu-chip" size="sm" class="text-ink-secondary" />
          </span>
        </div>
        <div class="jf-metric mt-2">{{ count(tokenBreakdown.total) }}</div>
      </JfCard>

      <JfCard density="compact">
        <div class="flex items-center justify-between">
          <span class="jf-caption font-medium text-ink-secondary">活跃提供商</span>
          <span class="metric-icon-wrap">
            <JfIcon name="server-stack" size="sm" class="text-ink-secondary" />
          </span>
        </div>
        <div class="jf-metric mt-2">{{ health?.catalog.providers.enabled ?? '—' }} / {{ health?.catalog.providers.total ?? '—' }}</div>
      </JfCard>
      <JfCard density="compact">
        <div class="flex items-center justify-between">
          <span class="jf-caption font-medium text-ink-secondary">已启用模型</span>
          <span class="metric-icon-wrap">
            <JfIcon name="cpu-chip" size="sm" class="text-ink-secondary" />
          </span>
        </div>
        <div class="jf-metric mt-2">{{ health?.catalog.models.enabled ?? '—' }} / {{ health?.catalog.models.total ?? '—' }}</div>
      </JfCard>
      <JfCard density="compact">
        <div class="flex items-center justify-between">
          <span class="jf-caption font-medium text-ink-secondary">已启用绑定</span>
          <span class="metric-icon-wrap">
            <JfIcon name="layers-2" size="sm" class="text-ink-secondary" />
          </span>
        </div>
        <div class="jf-metric mt-2">{{ health?.catalog.pairs.enabled ?? '—' }} / {{ health?.catalog.pairs.total ?? '—' }}</div>
      </JfCard>
    </section>

    <!-- 2. 可视化图表区：请求状态分布环形图 + 核心路由采纳效能仪表 -->
    <section class="grid gap-4 lg:grid-cols-2">
      <!-- 环形图：请求状态分布 -->
      <JfCard title="请求状态分布">
        <div class="flex flex-col items-center justify-around gap-6 sm:flex-row">
          <!-- SVG 环形图 -->
          <div class="relative flex h-36 w-36 shrink-0 items-center justify-center">
            <svg aria-hidden="true" class="h-full w-full -rotate-90" viewBox="0 0 110 110">
              <!-- 底色轨道 -->
              <circle
                cx="55"
                cy="55"
                r="45"
                fill="none"
                stroke="var(--jf-border-subtle)"
                stroke-width="12"
              />
              <template v-if="!statusDonut.empty">
                <circle
                  v-for="slice in statusDonut.slices"
                  :key="slice.key"
                  cx="55"
                  cy="55"
                  r="45"
                  fill="none"
                  :stroke="slice.stroke"
                  stroke-width="12"
                  :stroke-dasharray="slice.dasharray"
                  :stroke-dashoffset="slice.dashoffset"
                  stroke-linecap="round"
                  class="transition-all duration-500 ease-out"
                />
              </template>
            </svg>
            <div class="absolute inset-0 flex flex-col items-center justify-center text-center">
              <span class="jf-metric text-xl font-medium leading-none">{{ totalStatusCount > 0 ? `${statusDonut.successPct}%` : '—' }}</span>
              <span class="jf-caption mt-1 text-ink-secondary">{{ totalStatusCount > 0 ? '2xx 占比' : '暂无数据' }}</span>
            </div>
          </div>

          <!-- 图例与明细 -->
          <div class="flex w-full flex-col justify-center gap-2.5 sm:max-w-xs">
            <div class="flex items-center justify-between rounded-lg bg-tonal px-3 py-2 text-xs">
              <div class="flex items-center gap-2">
                <span class="h-2.5 w-2.5 rounded-full bg-success"></span>
                <span class="text-ink">成功 (2xx)</span>
              </div>
              <div class="flex items-center gap-3">
                <span class="font-mono text-ink">{{ count(statusDonut.successCount) }}</span>
                <span class="w-12 text-right font-mono text-ink-secondary">{{ statusDonut.successPct }}%</span>
              </div>
            </div>

            <div class="flex items-center justify-between rounded-lg bg-tonal px-3 py-2 text-xs">
              <div class="flex items-center gap-2">
                <span class="h-2.5 w-2.5 rounded-full bg-warning"></span>
                <span class="text-ink">客户端错误 (4xx)</span>
              </div>
              <div class="flex items-center gap-3">
                <span class="font-mono text-ink">{{ count(statusDonut.clientCount) }}</span>
                <span class="w-12 text-right font-mono text-ink-secondary">{{ statusDonut.clientPct }}%</span>
              </div>
            </div>

            <div class="flex items-center justify-between rounded-lg bg-tonal px-3 py-2 text-xs">
              <div class="flex items-center gap-2">
                <span class="h-2.5 w-2.5 rounded-full bg-danger"></span>
                <span class="text-ink">服务端错误 (5xx)</span>
              </div>
              <div class="flex items-center gap-3">
                <span class="font-mono text-ink">{{ count(statusDonut.serverCount) }}</span>
                <span class="w-12 text-right font-mono text-ink-secondary">{{ statusDonut.serverPct }}%</span>
              </div>
            </div>
          </div>
        </div>
      </JfCard>

      <!-- 环形仪表盘：智能决策效能 -->
      <JfCard title="智能路由决策效能">
        <div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <!-- 仪表 1: 自动路由占比 -->
          <div class="flex flex-col items-center">
            <div class="relative flex h-20 w-20 items-center justify-center">
              <svg aria-hidden="true" class="h-full w-full -rotate-90" viewBox="0 0 80 80">
                <circle cx="40" cy="40" r="34" fill="none" stroke="var(--jf-border-subtle)" stroke-width="7" />
                <circle
                  cx="40"
                  cy="40"
                  r="34"
                  fill="none"
                  stroke="var(--jf-primary)"
                  stroke-width="7"
                  :stroke-dasharray="ringDash(summaryRate('auto_usage_rate')?.value)"
                  stroke-linecap="round"
                  class="transition-all duration-500"
                />
              </svg>
              <span class="absolute font-mono text-sm font-medium">{{ rate(summaryRate('auto_usage_rate')?.value) }}</span>
            </div>
            <span class="jf-caption mt-2 text-center text-ink-secondary">自动路由占比</span>
          </div>

          <!-- 仪表 2: Jev 推荐成功请求占自动路由请求的比例，不是调用成功率 -->
          <div class="flex flex-col items-center">
            <div class="relative flex h-20 w-20 items-center justify-center">
              <svg aria-hidden="true" class="h-full w-full -rotate-90" viewBox="0 0 80 80">
                <circle cx="40" cy="40" r="34" fill="none" stroke="var(--jf-border-subtle)" stroke-width="7" />
                <circle
                  cx="40"
                  cy="40"
                  r="34"
                  fill="none"
                  stroke="var(--jf-success-text)"
                  stroke-width="7"
                  :stroke-dasharray="ringDash(summaryRate('jev_invocation_rate')?.value)"
                  stroke-linecap="round"
                  class="transition-all duration-500"
                />
              </svg>
              <span class="absolute font-mono text-sm font-medium">{{ rate(summaryRate('jev_invocation_rate')?.value) }}</span>
            </div>
            <span class="jf-caption mt-2 text-center text-ink-secondary" title="Jev 返回有效推荐的次数 ÷ 自动路由请求数；跳过调用的请求也计入分母。">Jev 成功推荐占比</span>
          </div>

          <!-- 仪表 3: 自动决策成功率 -->
          <div class="flex flex-col items-center">
            <div class="relative flex h-20 w-20 items-center justify-center">
              <svg aria-hidden="true" class="h-full w-full -rotate-90" viewBox="0 0 80 80">
                <circle cx="40" cy="40" r="34" fill="none" stroke="var(--jf-border-subtle)" stroke-width="7" />
                <circle
                  cx="40"
                  cy="40"
                  r="34"
                  fill="none"
                  stroke="var(--jf-info-text)"
                  stroke-width="7"
                  :stroke-dasharray="ringDash(summaryRate('auto_decision_success_rate')?.value)"
                  stroke-linecap="round"
                  class="transition-all duration-500"
                />
              </svg>
              <span class="absolute font-mono text-sm font-medium">{{ rate(summaryRate('auto_decision_success_rate')?.value) }}</span>
            </div>
            <span class="jf-caption mt-2 text-center text-ink-secondary">自动决策成功率</span>
          </div>

          <!-- 仪表 4: 成功推荐中未发生默认组兜底的比例 -->
          <div class="flex flex-col items-center">
            <div class="relative flex h-20 w-20 items-center justify-center">
              <svg aria-hidden="true" class="h-full w-full -rotate-90" viewBox="0 0 80 80">
                <circle cx="40" cy="40" r="34" fill="none" stroke="var(--jf-border-subtle)" stroke-width="7" />
                <circle
                  cx="40"
                  cy="40"
                  r="34"
                  fill="none"
                  stroke="var(--jf-warning-text)"
                  stroke-width="7"
                  :stroke-dasharray="ringDash(summaryRate('jev_group_adoption_rate')?.value)"
                  stroke-linecap="round"
                  class="transition-all duration-500"
                />
              </svg>
              <span class="absolute font-mono text-sm font-medium">{{ rate(summaryRate('jev_group_adoption_rate')?.value) }}</span>
            </div>
            <span class="jf-caption mt-2 text-center text-ink-secondary" title="采用 Jev 推荐组且未兜底的次数 ÷ Jev 返回有效推荐的次数。">Jev 选组采纳率</span>
          </div>
        </div>
      </JfCard>
    </section>

    <!-- 3. 流量构成对比条：Token 构成比例 + 路由模式占比 + 延迟基准对比 -->
    <section class="grid gap-4 lg:grid-cols-2">
      <!-- Token 吞吐构成与模式分配 -->
      <JfCard title="请求与上游 Token 用量">
        <div class="flex flex-col gap-4">
          <p class="jf-caption text-ink-secondary">Token 来自上游响应中已提取的用量，不包含 Jev 调用用量；未读取到的计数不按 0 补齐，用量异常不等同于请求失败。</p>
          <!-- Token 构成比例条 -->
          <div>
            <div class="mb-1.5 flex items-center justify-between text-xs">
              <span class="text-ink-secondary">Token 构成（输入 / 输出）</span>
              <span class="font-mono text-ink">总计 {{ count(tokenBreakdown.total) }}</span>
            </div>
            <p v-if="tokenBreakdown.empty" class="py-1 text-xs text-ink-secondary">暂无 Token 用量数据</p>
            <div v-else class="flex h-3 w-full overflow-hidden rounded-full bg-tonal">
              <div
                class="bg-info transition-all duration-500"
                :style="{ width: `${tokenBreakdown.inPct}%` }"
                :title="`输入 Token: ${count(tokenBreakdown.input)} (${tokenBreakdown.inPct}%)`"
              ></div>
              <div
                class="bg-success transition-all duration-500"
                :style="{ width: `${tokenBreakdown.outPct}%` }"
                :title="`输出 Token: ${count(tokenBreakdown.output)} (${tokenBreakdown.outPct}%)`"
              ></div>
            </div>
            <div v-if="!tokenBreakdown.empty" class="mt-2 flex items-center justify-between text-xs">
              <span class="inline-flex items-center gap-1.5 text-ink">
                <span class="h-2 w-2 rounded-full bg-info"></span>
                输入: {{ count(tokenBreakdown.input) }} ({{ tokenBreakdown.inPct }}%)
              </span>
              <span class="inline-flex items-center gap-1.5 text-ink">
                <span class="h-2 w-2 rounded-full bg-success"></span>
                输出: {{ count(tokenBreakdown.output) }} ({{ tokenBreakdown.outPct }}%)
              </span>
            </div>
          </div>

          <!-- 路由模式分配条 -->
          <div class="border-t border-line pt-3">
            <div class="mb-1.5 flex items-center justify-between text-xs">
              <span class="text-ink-secondary">路由选择（自动路由 / 指定模型）</span>
              <span class="font-mono text-ink">总计 {{ count(routeRatio.total) }} 次</span>
            </div>
            <p v-if="routeRatio.total <= 0" class="py-1 text-xs text-ink-secondary">暂无请求数据</p>
            <div v-else class="flex h-3 w-full overflow-hidden rounded-full bg-tonal">
              <div
                class="bg-primary transition-all duration-500"
                :style="{ width: `${routeRatio.autoPct}%` }"
                :title="`自动路由: ${count(routeRatio.auto)} (${routeRatio.autoPct}%)`"
              ></div>
              <div
                class="bg-warning transition-all duration-500"
                :style="{ width: `${routeRatio.explicitPct}%` }"
                :title="`指定模型: ${count(routeRatio.explicit)} (${routeRatio.explicitPct}%)`"
              ></div>
            </div>
            <div v-if="routeRatio.total > 0" class="mt-2 flex items-center justify-between text-xs">
              <span class="inline-flex items-center gap-1.5 text-ink">
                <span class="h-2 w-2 rounded-full bg-primary"></span>
                自动路由: {{ count(routeRatio.auto) }} ({{ routeRatio.autoPct }}%)
              </span>
              <span class="inline-flex items-center gap-1.5 text-ink">
                <span class="h-2 w-2 rounded-full bg-warning"></span>
                指定模型: {{ count(routeRatio.explicit) }} ({{ routeRatio.explicitPct }}%)
              </span>
            </div>
          </div>
        </div>
      </JfCard>

      <!-- 延迟数据只展示观测值与样本数，不以未经确认的阈值给出质量评级。 -->
      <JfCard title="响应延迟统计">
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="rounded-lg bg-tonal p-4">
            <span class="jf-caption text-ink-secondary">{{ latencySourceLabel }}平均延迟</span>
            <p class="jf-metric mt-2">{{ meanLatency !== null ? `${meanLatency} ms` : '—' }}</p>
            <p class="jf-caption mt-1 text-ink-secondary">样本 {{ count(latencyMetrics.sampleCount) }} 条</p>
          </div>
          <div class="rounded-lg bg-tonal p-4">
            <span class="jf-caption text-ink-secondary">{{ latencySourceLabel }} P95 延迟</span>
            <p class="jf-metric mt-2">{{ p95Latency !== null ? `${p95Latency} ms` : '—' }}</p>
            <p class="jf-caption mt-1 text-ink-secondary">样本 {{ count(latencyMetrics.sampleCount) }} 条</p>
          </div>
        </div>
      </JfCard>
    </section>

    <!-- 4. 模型用量排行柱状图 + 分组用量柱状图 -->
    <section class="grid gap-4 xl:grid-cols-2">
      <!-- 模型排行图表 -->
      <JfCard title="模型调用排行">
        <template #actions>
          <div class="flex items-center gap-1" role="group" aria-label="模型用量展示方式">
            <JfButton
              size="sm"
              :aria-pressed="modelView === 'chart'"
              :variant="modelView === 'chart' ? 'secondary' : 'ghost'"
              @click="modelView = 'chart'"
            >
              图表
            </JfButton>
            <JfButton
              size="sm"
              :aria-pressed="modelView === 'table'"
              :variant="modelView === 'table' ? 'secondary' : 'ghost'"
              @click="modelView = 'table'"
            >
              表格
            </JfButton>
          </div>
        </template>

        <!-- 柱状图视图 -->
        <div v-if="modelView === 'chart'" class="flex flex-col gap-3 py-1">
          <div v-if="rankedModels.length === 0" class="py-8 text-center text-xs text-ink-secondary">
            暂无模型用量记录
          </div>
          <div
            v-for="(m, idx) in rankedModels.slice(0, 6)"
            :key="m.id"
            class="flex flex-col gap-1.5"
          >
            <div class="flex items-center justify-between text-xs">
              <div class="flex items-center gap-2 min-w-0">
                <span class="flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-tonal font-mono text-[10px] text-ink-secondary">
                  {{ idx + 1 }}
                </span>
                <span class="jf-truncate font-mono font-medium text-ink" :title="rowName(m)">{{ rowName(m) }}</span>
              </div>
              <div class="flex items-center gap-3 shrink-0 font-mono">
                <span class="text-ink">{{ count(m.requests) }} 次</span>
                <span class="w-12 text-right text-ink-secondary">
                  {{ totalModelReq > 0 ? ((m.requests / totalModelReq) * 100).toFixed(1) : '0.0' }}%
                </span>
              </div>
            </div>
            <div class="h-2 w-full overflow-hidden rounded-full bg-tonal">
              <div
                class="h-full rounded-full bg-primary transition-all duration-500"
                :style="{ width: `${Math.min(100, Math.round(((m.requests || 0) / maxModelReq) * 100))}%` }"
              ></div>
            </div>
          </div>
        </div>

        <!-- 表格视图 -->
        <div v-else class="jf-scroll-x -mx-4 -mb-4">
          <JfTable :rows="report?.models ?? []" :columns="modelColumns" :loading="loading" empty-text="暂无模型用量记录">
            <template #cell-id="{ row }"><span class="block min-w-28 max-w-56 jf-anywhere jf-mono font-medium" :title="rowName(row)">{{ rowName(row) }}</span></template>
            <template #cell-requests="{ row }"><span class="jf-nowrap jf-mono">{{ count(row.requests) }}</span></template>
            <template #cell-input_tokens="{ row }"><span class="jf-nowrap jf-mono">{{ count(row.input_tokens) }}</span></template>
            <template #cell-output_tokens="{ row }"><span class="jf-nowrap jf-mono">{{ count(row.output_tokens) }}</span></template>
            <template #cell-total_tokens="{ row }"><span class="jf-nowrap jf-mono">{{ count(row.total_tokens) }}</span></template>
          </JfTable>
        </div>
      </JfCard>

      <!-- 分组排行图表 -->
      <JfCard title="分组流量分配">
        <template #actions>
          <div class="flex items-center gap-1" role="group" aria-label="分组用量展示方式">
            <JfButton
              size="sm"
              :aria-pressed="groupView === 'chart'"
              :variant="groupView === 'chart' ? 'secondary' : 'ghost'"
              @click="groupView = 'chart'"
            >
              图表
            </JfButton>
            <JfButton
              size="sm"
              :aria-pressed="groupView === 'table'"
              :variant="groupView === 'table' ? 'secondary' : 'ghost'"
              @click="groupView = 'table'"
            >
              表格
            </JfButton>
          </div>
        </template>

        <!-- 柱状图视图 -->
        <div v-if="groupView === 'chart'" class="flex flex-col gap-3 py-1">
          <div v-if="rankedGroups.length === 0" class="py-8 text-center text-xs text-ink-secondary">
            暂无分组用量记录
          </div>
          <div
            v-for="(g, idx) in rankedGroups.slice(0, 6)"
            :key="g.id"
            class="flex flex-col gap-1.5"
          >
            <div class="flex items-center justify-between text-xs">
              <div class="flex items-center gap-2 min-w-0">
                <span class="flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-tonal font-mono text-[10px] text-ink-secondary">
                  {{ idx + 1 }}
                </span>
                <span class="jf-truncate font-medium text-ink">{{ rowName(g) }}</span>
              </div>
              <div class="flex items-center gap-3 shrink-0 font-mono">
                <span class="text-ink">{{ count(g.requests) }} 次</span>
                <span class="w-12 text-right text-ink-secondary">
                  {{ totalGroupReq > 0 ? ((g.requests / totalGroupReq) * 100).toFixed(1) : '0.0' }}%
                </span>
              </div>
            </div>
            <div class="h-2 w-full overflow-hidden rounded-full bg-tonal">
              <div
                class="h-full rounded-full bg-primary transition-all duration-500"
                :style="{ width: `${Math.min(100, Math.round(((g.requests || 0) / maxGroupReq) * 100))}%` }"
              ></div>
            </div>
          </div>
        </div>

        <!-- 表格视图 -->
        <div v-else class="jf-scroll-x -mx-4 -mb-4">
          <JfTable :rows="report?.groups ?? []" :columns="groupColumns" :loading="loading" empty-text="暂无分组用量记录">
            <template #cell-id="{ row }"><span class="font-medium">{{ rowName(row) }}</span></template>
            <template #cell-requests="{ row }"><span class="jf-nowrap jf-mono">{{ count(row.requests) }}</span></template>
            <template #cell-input_tokens="{ row }"><span class="jf-nowrap jf-mono">{{ count(row.input_tokens) }}</span></template>
            <template #cell-output_tokens="{ row }"><span class="jf-nowrap jf-mono">{{ count(row.output_tokens) }}</span></template>
            <template #cell-total_tokens="{ row }"><span class="jf-nowrap jf-mono">{{ count(row.total_tokens) }}</span></template>
          </JfTable>
        </div>
      </JfCard>
    </section>
  </div>
</template>

<style scoped>
.metric-icon-wrap {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: var(--jf-radius-control);
  background: var(--jf-tonal);
}
</style>
