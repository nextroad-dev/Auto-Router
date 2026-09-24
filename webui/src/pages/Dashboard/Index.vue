<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { api, formatRate } from '@/lib/api'
import { errorNotice } from '@/lib/errors'
import ErrorAlert from '@/components/ErrorAlert.vue'
import type { DashboardReport } from '@/lib/admin-contracts'
import type { components } from '@/lib/generated-api'

type AdminHealth = components['schemas']['AdminHealth']
type LogSummary = components['schemas']['LogSummary']

const report = ref<DashboardReport>()
const loading = ref(false)
const error = ref<unknown>()
const health = ref<AdminHealth>()
const summary = ref<LogSummary>()
const healthLoading = ref(false)
const summaryLoading = ref(false)
const healthError = ref<unknown>()
const summaryError = ref<unknown>()
const summaryWindow = ref<'1h' | '24h' | '7d' | '30d'>('24h')
const windowOptions = [
  { label: '最近 1 小时', value: '1h' },
  { label: '最近 24 小时', value: '24h' },
  { label: '最近 7 天', value: '7d' },
  { label: '最近 30 天', value: '30d' },
]
let refreshTimer: ReturnType<typeof setInterval> | undefined
let operationsTimer: ReturnType<typeof setInterval> | undefined
let summaryRequestId = 0

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

async function loadDashboard() {
  if (loading.value) return
  loading.value = true
  error.value = undefined
  try {
    report.value = await api.get<DashboardReport>('/admin/v1/dashboard')
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void loadDashboard()
  void refreshOperations()
  refreshTimer = setInterval(() => {
    if (document.visibilityState === 'visible') void loadDashboard()
  }, 5000)
  operationsTimer = setInterval(() => {
    if (document.visibilityState === 'visible') void refreshOperations()
  }, 30000)
})

onUnmounted(() => {
  if (refreshTimer !== undefined) clearInterval(refreshTimer)
  if (operationsTimer !== undefined) clearInterval(operationsTimer)
})

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
function summaryRate(name: string) {
  return summary.value?.rates.find(item => item.name === name)
}
function summaryRateLabel(name: string) {
  const labels: Record<string, string> = {
    client_request_success_rate: '客户端请求成功率',
    auto_usage_rate: '自动路由占比',
    auto_decision_success_rate: '自动路由决策成功率',
    jev_invocation_rate: 'Jev 调用成功率',
    jev_top1_adoption_rate: 'Jev Top-1 采纳比例',
  }
  return labels[name] ?? name
}
function duration(value: number | undefined) {
  if (value === undefined || !Number.isFinite(value)) return '—'
  const seconds = Math.floor(value / 1000)
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  return days ? `${days} 天 ${hours} 小时` : hours ? `${hours} 小时 ${minutes} 分钟` : `${minutes} 分钟`
}
function optionalNumber(value: number | null | undefined, suffix = '') {
  return value === null || value === undefined ? '暂无数据' : `${new Intl.NumberFormat('zh-CN').format(value)}${suffix}`
}
</script>

<template>
  <div class="space-y-6">
    <section class="flex justify-end">
      <UButton color="neutral" variant="outline" icon="i-heroicons-arrow-path" :loading="loading || healthLoading || summaryLoading" @click="refreshAll">刷新</UButton>
    </section>

    <ErrorAlert v-if="error" :error="error">
      <template #actions><UButton color="error" variant="ghost" size="sm" @click="loadDashboard">重试</UButton></template>
    </ErrorAlert>

    <section class="space-y-3">
      <div class="flex flex-wrap items-end justify-between gap-3"><div><h2 class="text-lg font-semibold">服务健康</h2><p class="text-sm text-muted">运行依赖及处理队列状态，与业务流量指标分开呈现。</p></div><UButton size="sm" color="neutral" variant="ghost" :loading="healthLoading" @click="loadHealth">刷新健康状态</UButton></div>
      <ErrorAlert v-if="healthError" :error="healthError" />
      <section class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <UCard><p class="text-sm text-muted">服务与存储</p><p class="mt-2 text-xl font-semibold">{{ health?.status === 'ok' ? '运行正常' : health?.status === 'not_ready' ? '未就绪' : '—' }}</p><p class="mt-1 text-xs text-muted">数据库：{{ health?.storage === 'ready' ? '可用' : health?.storage === 'not_ready' ? '不可用' : '未连接' }} · 运行 {{ duration(health?.uptime_ms) }}</p></UCard>
        <UCard><p class="text-sm text-muted">注册表</p><p class="mt-2 text-xl font-semibold">{{ health?.catalog.generation ?? '—' }} <span class="text-sm font-normal text-muted">代</span></p><p class="mt-1 text-xs text-muted">Provider {{ health?.catalog.providers.enabled ?? '—' }}/{{ health?.catalog.providers.total ?? '—' }} · 模型 {{ health?.catalog.models.enabled ?? '—' }}/{{ health?.catalog.models.total ?? '—' }} · 绑定 {{ health?.catalog.pairs.enabled ?? '—' }}/{{ health?.catalog.pairs.total ?? '—' }}</p></UCard>
        <UCard><p class="text-sm text-muted">持久路由日志队列</p><p class="mt-2 text-xl font-semibold">{{ health?.routing_log.enabled ? '已启用' : health ? '已停用' : '—' }}</p><p class="mt-1 text-xs text-muted">队列 {{ health?.routing_log.queue_depth ?? '—' }}/{{ health?.routing_log.queue_capacity ?? '—' }} · 写入 {{ count(health?.routing_log.written) }}</p></UCard>
        <UCard><p class="text-sm text-muted">运行时管理状态</p><p class="mt-2 text-xl font-semibold">{{ health?.settings.overlay ? '有运行时覆盖' : health ? '代码默认值' : '—' }}</p><p class="mt-1 text-xs text-muted">设置版本 {{ health?.settings.version ?? '—' }} · 有效会话 {{ health?.sessions.live ?? '—' }}/{{ health?.sessions.bound ?? '—' }} · 丢弃/失败 {{ count(health?.routing_log.dropped) }}/{{ count(health?.routing_log.failed) }}</p></UCard>
      </section>
    </section>

    <section class="space-y-3">
      <div class="flex flex-wrap items-end justify-between gap-3"><div><h2 class="text-lg font-semibold">路由与 Jev 汇总</h2><p class="text-sm text-muted">窗口内的请求分布和已定义运营指标；不把采纳比例解释为模型路由准确率。</p></div><div class="flex items-center gap-2"><USelect v-model="summaryWindow" :items="windowOptions" value-key="value" aria-label="汇总时间窗口" @update:model-value="loadSummary($event)" /><UButton size="sm" color="neutral" variant="ghost" :loading="summaryLoading" @click="loadSummary()">刷新汇总</UButton></div></div>
      <ErrorAlert v-if="summaryError" :error="summaryError" />
      <UAlert v-if="summary?.log_disabled" color="warning" variant="soft" title="持久路由日志当前已关闭；窗口汇总中的空数据不代表 0 次请求。" />
      <section class="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <UCard><p class="text-sm text-muted">请求分布 · {{ summary?.window.key ?? summaryWindow }}</p><p class="mt-2 text-xl font-semibold">{{ count(summary?.requests.total) }} 条</p><p class="mt-1 text-xs text-muted">自动 {{ count(summary?.requests.auto) }} · 指定 {{ count(summary?.requests.explicit) }}</p></UCard>
        <UCard v-for="name in ['client_request_success_rate', 'auto_usage_rate', 'jev_invocation_rate', 'jev_top1_adoption_rate']" :key="name"><p class="text-sm text-muted">{{ summaryRateLabel(name) }}</p><p class="mt-2 text-2xl font-semibold">{{ rate(summaryRate(name)?.value) }}</p><p class="mt-1 text-xs text-muted">{{ count(summaryRate(name)?.numerator) }} / {{ count(summaryRate(name)?.denominator) }} · {{ summaryRate(name)?.numerator_label ?? '暂无数据' }}</p></UCard>
      </section>
      <UCard><template #header><h3 class="font-semibold">其他窗口指标</h3></template><div class="grid gap-4 text-sm sm:grid-cols-2 xl:grid-cols-4"><p>自动决策成功率：<strong>{{ rate(summaryRate('auto_decision_success_rate')?.value) }}</strong></p><p>自动请求平均延迟：<strong>{{ optionalNumber(summary?.latency.auto.mean_duration_ms, ' ms') }}</strong></p><p>自动请求 P95 延迟：<strong>{{ optionalNumber(summary?.latency.auto.p95_duration_ms, ' ms') }}</strong></p><p>尝试输出 TPS（最近 60 秒）：<strong>{{ tps(summary?.attempt_output_tokens_per_second_60s) }}</strong></p><p>输入 Token：<strong>{{ optionalNumber(summary?.tokens.input_tokens) }}</strong></p><p>输出 Token：<strong>{{ optionalNumber(summary?.tokens.output_tokens) }}</strong></p><p>服务端错误：<strong>{{ count(summary?.statuses.server_error) }}</strong></p><p>平均延迟样本：<strong>{{ count(summary?.latency.all.count) }}</strong></p></div></UCard>
    </section>

    <section class="grid gap-4 md:grid-cols-2">
      <UCard>
        <div class="flex items-start justify-between gap-4">
          <div>
            <p class="text-sm text-muted">请求成功率</p>
            <p class="mt-4 jf-kpi">{{ loading ? '—' : rate(report?.success_rate.value) }}</p>
            <p class="mt-2 text-sm text-muted">{{ count(report?.success_rate.numerator) }} / {{ count(report?.success_rate.denominator) }} 个请求成功</p>
          </div>
          <UIcon name="i-lucide-circle-check" class="h-6 w-6 text-success" aria-hidden="true" />
        </div>
      </UCard>
      <UCard>
        <div class="flex items-start justify-between gap-4">
          <div>
            <p class="text-sm text-muted">输出 TPS · 最近 60 秒</p>
            <p class="mt-4 jf-kpi">{{ loading ? '—' : tps(report?.output_tps_60s) }}</p>
            <p class="mt-2 text-sm text-muted">每秒输出 Token 数</p>
          </div>
          <UIcon name="i-lucide-gauge" class="h-6 w-6 text-muted" aria-hidden="true" />
        </div>
      </UCard>
    </section>

    <section class="grid gap-4 xl:grid-cols-2">
      <UCard class="overflow-hidden">
        <template #header><h3 class="font-semibold">模型用量</h3></template>
        <div class="overflow-x-auto">
          <UTable :data="report?.models ?? []" :columns="[
            { accessorKey: 'id', header: '模型' },
            { accessorKey: 'requests', header: '调用次数' },
            { accessorKey: 'input_tokens', header: '输入 Token' },
            { accessorKey: 'output_tokens', header: '输出 Token' },
            { accessorKey: 'total_tokens', header: '总 Token' },
          ]" :loading="loading" empty="暂无模型用量">
            <template #id-cell="{ row }"><span class="font-mono text-xs">{{ rowName(row.original) }}</span></template>
            <template #requests-cell="{ row }">{{ count(row.original.requests) }}</template>
            <template #input_tokens-cell="{ row }">{{ count(row.original.input_tokens) }}</template>
            <template #output_tokens-cell="{ row }">{{ count(row.original.output_tokens) }}</template>
            <template #total_tokens-cell="{ row }">{{ count(row.original.total_tokens) }}</template>
          </UTable>
        </div>
      </UCard>

      <UCard class="overflow-hidden">
        <template #header><h3 class="font-semibold">分组用量</h3></template>
        <div class="overflow-x-auto">
          <UTable :data="report?.groups ?? []" :columns="[
            { accessorKey: 'id', header: '分组' },
            { accessorKey: 'requests', header: '调用次数' },
            { accessorKey: 'input_tokens', header: '输入 Token' },
            { accessorKey: 'output_tokens', header: '输出 Token' },
            { accessorKey: 'total_tokens', header: '总 Token' },
          ]" :loading="loading" empty="暂无分组用量">
            <template #id-cell="{ row }"><span class="font-medium">{{ rowName(row.original) }}</span></template>
            <template #requests-cell="{ row }">{{ count(row.original.requests) }}</template>
            <template #input_tokens-cell="{ row }">{{ count(row.original.input_tokens) }}</template>
            <template #output_tokens-cell="{ row }">{{ count(row.original.output_tokens) }}</template>
            <template #total_tokens-cell="{ row }">{{ count(row.original.total_tokens) }}</template>
          </UTable>
        </div>
      </UCard>
    </section>
  </div>
</template>
