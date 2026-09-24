<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { api, errorMessage, formatRate } from '@/lib/api'
import type { DashboardReport } from '@/lib/admin-contracts'

const report = ref<DashboardReport>()
const loading = ref(false)
const error = ref('')
let refreshTimer: ReturnType<typeof setInterval> | undefined

async function loadDashboard() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    report.value = await api.get<DashboardReport>('/admin/v1/dashboard')
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void loadDashboard()
  refreshTimer = setInterval(() => {
    if (document.visibilityState === 'visible') void loadDashboard()
  }, 5000)
})

onUnmounted(() => {
  if (refreshTimer !== undefined) clearInterval(refreshTimer)
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
</script>

<template>
  <div class="space-y-6">
    <section class="flex justify-end">
      <UButton color="neutral" variant="outline" icon="i-heroicons-arrow-path" :loading="loading" @click="loadDashboard">刷新</UButton>
    </section>

    <UAlert v-if="error" color="error" variant="soft" :title="error">
      <template #actions><UButton color="error" variant="ghost" size="sm" @click="loadDashboard">重试</UButton></template>
    </UAlert>

    <section class="grid gap-4 md:grid-cols-2">
      <UCard>
        <div class="flex items-start justify-between gap-4">
          <div>
            <p class="text-sm text-muted">请求成功率</p>
            <p class="mt-4 text-4xl font-semibold tracking-tight">{{ loading ? '—' : rate(report?.success_rate.value) }}</p>
            <p class="mt-2 text-sm text-muted">{{ count(report?.success_rate.numerator) }} / {{ count(report?.success_rate.denominator) }} 个请求成功</p>
          </div>
          <UIcon name="i-lucide-circle-check" class="h-6 w-6 text-success" aria-hidden="true" />
        </div>
      </UCard>
      <UCard>
        <div class="flex items-start justify-between gap-4">
          <div>
            <p class="text-sm text-muted">输出 TPS · 最近 60 秒</p>
            <p class="mt-4 text-4xl font-semibold tracking-tight">{{ loading ? '—' : tps(report?.output_tps_60s) }}</p>
            <p class="mt-2 text-sm text-muted">每秒输出 Token 数</p>
          </div>
          <UIcon name="i-lucide-gauge" class="h-6 w-6 text-primary" aria-hidden="true" />
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
