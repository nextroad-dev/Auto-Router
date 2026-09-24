<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { api, createInboundKeyRequest, errorMessage, type InboundKey, type OneTimeKey } from '@/lib/api'

const loading = ref(false)
const keys = ref<InboundKey[]>([])
const error = ref('')
const slideOpen = ref(false)
const formLoading = ref(false)
const formError = ref('')
const revealOpen = ref(false)
const revealedKey = ref('')
const revealedTitle = ref('')
const copied = ref(false)
const formData = reactive({ name: '' })

async function fetchKeys() {
  loading.value = true; error.value = ''
  try {
    const result = await api.get<{ items: InboundKey[] }>('/admin/v1/keys')
    keys.value = result.items
  } catch (cause) { error.value = errorMessage(cause) }
  finally { loading.value = false }
}
onMounted(() => { void fetchKeys() })

function openCreate() {
  formError.value = ''; formData.name = ''; slideOpen.value = true
}
function showKey(result: OneTimeKey, title: string) {
  revealedKey.value = result.key
  revealedTitle.value = title
  copied.value = false
  revealOpen.value = true
}
async function createKey() {
  formError.value = ''
  const name = formData.name.trim()
  if (!name) { formError.value = '请输入凭据名称。'; return }
  if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(name)) {
    formError.value = '名称须以小写字母或数字开头，且只能包含小写字母、数字、点、下划线或连字符（最多 64 个字符）。'
    return
  }
  formLoading.value = true
  try {
    const result = await api.post<OneTimeKey>('/admin/v1/keys', createInboundKeyRequest(name))
    slideOpen.value = false
    showKey(result, '凭据已创建')
    await fetchKeys()
  } catch (cause) { formError.value = errorMessage(cause) }
  finally { formLoading.value = false }
}
async function rotateKey(item: InboundKey) {
  if (!window.confirm(`确定轮换 API 密钥“${item.name}”吗？旧密钥将立即失效。`)) return
  loading.value = true; error.value = ''
  try {
    const result = await api.post<OneTimeKey>(`/admin/v1/keys/${encodeURIComponent(item.name)}/rotate`, {})
    showKey(result, `凭据“${item.name}”已轮换`)
	await fetchKeys()
  } catch (cause) { error.value = errorMessage(cause) }
  finally { loading.value = false }
}
async function disableKey(item: InboundKey) {
  if (!window.confirm(`确定停用 API 密钥“${item.name}”吗？该密钥将立即失效。`)) return
  loading.value = true; error.value = ''
  try {
    await api.delete(`/admin/v1/keys/${encodeURIComponent(item.name)}`)
    await fetchKeys()
  } catch (cause) { error.value = errorMessage(cause) }
  finally { loading.value = false }
}
async function copyRevealedKey() {
  try {
    await navigator.clipboard.writeText(revealedKey.value)
    copied.value = true
  } catch { error.value = '无法访问剪贴板，请手动选择并复制密钥。' }
}
function closeReveal() {
  revealOpen.value = false
  revealedKey.value = ''
  copied.value = false
}
function showCreatedAt(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}
const columns = [
  { accessorKey: 'name', header: '凭据名称' }, { accessorKey: 'active', header: '状态' },
  { accessorKey: 'created_at', header: '创建时间' },
  { accessorKey: 'actions', header: '操作' },
]
</script>

<template>
  <div class="space-y-5">
    <section class="flex flex-col justify-between gap-4 sm:flex-row sm:items-end">
      <div><p class="text-sm text-muted">为调用方命名；密钥只在创建或轮换时显示一次。</p></div>
      <div class="flex gap-2"><UButton color="neutral" variant="outline" icon="i-heroicons-arrow-path" :loading="loading" @click="fetchKeys">刷新</UButton><UButton icon="i-heroicons-plus" @click="openCreate">创建 API 密钥</UButton></div>
    </section>
    <UAlert v-if="error" color="error" variant="soft" :title="error" />
    <UCard class="overflow-hidden">
      <template #header><h3 class="font-semibold">已登记 API 密钥 <UBadge color="neutral" variant="subtle" class="ml-1">{{ keys.length }}</UBadge></h3></template>
      <div class="overflow-x-auto"><UTable :data="keys" :columns="columns" :loading="loading" empty="尚未登记凭据">
        <template #name-cell="{ row }"><div class="font-medium">{{ row.original.name }}</div><div class="mt-0.5 font-mono text-[11px] text-muted">{{ row.original.key_set ? '密钥已写入' : '密钥未设置' }}</div></template>
        <template #active-cell="{ row }"><UBadge :color="row.original.active ? 'success' : 'neutral'" variant="subtle">{{ row.original.active ? '有效' : '已停用' }}</UBadge></template>
        <template #created_at-cell="{ row }"><span class="whitespace-nowrap text-xs text-muted">{{ showCreatedAt(row.original.created_at) }}</span></template>
        <template #actions-cell="{ row }"><div class="flex gap-1"><UButton color="neutral" variant="ghost" size="sm" icon="i-heroicons-arrow-path-rounded-square" :disabled="!row.original.active" @click="rotateKey(row.original)">轮换</UButton><UButton color="error" variant="ghost" size="sm" icon="i-heroicons-no-symbol" :disabled="!row.original.active" @click="disableKey(row.original)">停用</UButton></div></template>
      </UTable></div>
    </UCard>

    <USlideover v-model:open="slideOpen" title="创建 API 密钥">
      <template #body><form class="space-y-5" @submit.prevent="createKey">
        <UFormField label="名称" name="key-name" required><UInput v-model="formData.name" autocomplete="off" placeholder="例如：production-app" class="w-full" /></UFormField>
        <UAlert v-if="formError" color="error" variant="soft" :title="formError" />
        <div class="flex justify-end gap-2"><UButton color="neutral" variant="ghost" @click="slideOpen = false">取消</UButton><UButton type="submit" :loading="formLoading">创建密钥</UButton></div>
      </form></template>
    </USlideover>

    <UModal v-model:open="revealOpen" :title="revealedTitle" description="此密钥不会再次显示。复制并妥善保存后关闭此窗口。" :dismissible="false">
      <template #body><UAlert color="warning" variant="soft" icon="i-heroicons-eye-slash" title="请立即保存 · 仅显示一次"><template #description><div class="one-time-secret mt-3 rounded-lg border border-default bg-neutral-50 p-3 font-mono text-sm dark:bg-neutral-950">{{ revealedKey }}</div></template></UAlert><div class="mt-4 flex justify-end gap-2"><UButton color="neutral" variant="outline" icon="i-heroicons-clipboard-document" @click="copyRevealedKey">{{ copied ? '已复制' : '复制密钥' }}</UButton><UButton @click="closeReveal">我已保存</UButton></div></template>
    </UModal>
  </div>
</template>

<style scoped>
.one-time-secret { overflow-wrap: anywhere; user-select: all; }
</style>
