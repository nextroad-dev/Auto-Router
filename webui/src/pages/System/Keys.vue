<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { api, createInboundKeyRequest, type InboundKey, type OneTimeKey } from '@/lib/api'
import { errorNotice } from '@/lib/errors'
import ErrorAlert from '@/components/ErrorAlert.vue'
import JfAlert from '@/components/JfAlert.vue'
import JfBadge from '@/components/JfBadge.vue'
import JfButton from '@/components/JfButton.vue'
import JfCard from '@/components/JfCard.vue'
import JfDialog from '@/components/JfDialog.vue'
import JfDrawer from '@/components/JfDrawer.vue'
import JfField from '@/components/JfField.vue'
import JfInput from '@/components/JfInput.vue'
import JfTable from '@/components/JfTable.vue'
import type { JfColumn } from '@/lib/table'

const loading = ref(false)
const keys = ref<InboundKey[]>([])
const error = ref<unknown>()
const slideOpen = ref(false)
const formLoading = ref(false)
const formError = ref<unknown>()
const revealOpen = ref(false)
const revealedKey = ref('')
const revealedTitle = ref('')
const copied = ref(false)
const formData = reactive({ name: '' })

async function fetchKeys() {
  loading.value = true
  error.value = undefined
  try {
    const result = await api.get<{ items: InboundKey[] }>('/admin/v1/keys')
    keys.value = result.items
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    loading.value = false
  }
}

onMounted(() => { void fetchKeys() })

function openCreate() {
  formError.value = undefined
  formData.name = ''
  slideOpen.value = true
}

function showKey(result: OneTimeKey, title: string) {
  revealedKey.value = result.key
  revealedTitle.value = title
  copied.value = false
  revealOpen.value = true
}

async function createKey() {
  formError.value = undefined
  const name = formData.name.trim()
  if (!name) {
    formError.value = '请输入密钥名称。'
    return
  }
  if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(name)) {
    formError.value = '名称须以小写字母或数字开头，且只能包含小写字母、数字、点、下划线或连字符（最多 64 个字符）。'
    return
  }
  formLoading.value = true
  try {
    const result = await api.post<OneTimeKey>('/admin/v1/keys', createInboundKeyRequest(name))
    slideOpen.value = false
    await fetchKeys()
    showKey(result, `推理密钥已创建 · ${name}`)
  } catch (cause) {
    formError.value = errorNotice(cause)
  } finally {
    formLoading.value = false
  }
}

async function rotateKey(key: InboundKey) {
  if (!window.confirm(`确定要轮换密钥“${key.name}”吗？旧密钥将立即失效，所有使用旧密钥的客户端需同步更新。`)) return
  loading.value = true
  try {
    const result = await api.postEmpty<OneTimeKey>(`/admin/v1/keys/${encodeURIComponent(key.name)}/rotate`)
    await fetchKeys()
    showKey(result, `推理密钥已轮换 · ${key.name}`)
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    loading.value = false
  }
}

async function deleteKey(key: InboundKey) {
  if (!window.confirm(`确定要撤销并删除密钥“${key.name}”吗？使用该密钥的推理请求将被立刻拒绝。`)) return
  loading.value = true
  try {
    await api.delete(`/admin/v1/keys/${encodeURIComponent(key.name)}`)
    await fetchKeys()
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    loading.value = false
  }
}

async function copyRevealedKey() {
  try {
    await navigator.clipboard.writeText(revealedKey.value)
    copied.value = true
  } catch {
    error.value = '无法访问剪贴板，请手动选中文本并复制。'
  }
}

function closeReveal() {
  revealOpen.value = false
  revealedKey.value = ''
  copied.value = false
}

function showCreatedAt(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? '—'
    : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

const columns: JfColumn[] = [
  { key: 'name', title: '密钥名称' },
  { key: 'active', title: '状态', nowrap: true },
  { key: 'created_at', title: '创建时间' },
  { key: 'actions', title: '操作', nowrap: true },
]
</script>

<template>
  <div class="jf-stack">
    <!-- Toolbar -->
    <section class="jf-toolbar">
      <div>
        <h1 class="jf-page-title">推理密钥</h1>
      </div>
      <div class="jf-action-group">
        <JfButton variant="secondary" icon="arrow-path" :loading="loading" @click="fetchKeys">刷新</JfButton>
        <JfButton icon="plus" @click="openCreate">创建推理密钥</JfButton>
      </div>
    </section>

    <ErrorAlert v-if="error" :error="error" />

    <!-- Keys Table Card -->
    <JfCard flush>
      <template #header>
        <div class="flex items-center gap-2">
          <h2 class="jf-section-title">已创建的推理密钥</h2>
          <JfBadge tone="neutral">{{ keys.length }}</JfBadge>
        </div>
      </template>

      <div class="jf-scroll-x">
        <JfTable :columns="columns" :rows="keys" row-key="name" :loading="loading" empty-text="尚未创建任何推理密钥">
          <template #cell-name="{ row }">
            <span class="font-mono font-medium">{{ row.name }}</span>
          </template>

          <template #cell-active="{ row }">
            <JfBadge :tone="row.active ? 'success' : 'neutral'">
              {{ row.active ? '有效' : '已失效' }}
            </JfBadge>
          </template>

          <template #cell-created_at="{ row }">
            <span class="jf-caption jf-mono text-ink-secondary">{{ showCreatedAt(row.created_at) }}</span>
          </template>

          <template #cell-actions="{ row }">
            <div class="jf-action-group justify-end">
              <JfButton size="sm" variant="ghost" icon="arrow-path-rounded-square" @click="rotateKey(row)">轮换</JfButton>
              <JfButton size="sm" variant="danger-ghost" icon="trash" @click="deleteKey(row)">撤销</JfButton>
            </div>
          </template>
        </JfTable>
      </div>
    </JfCard>

    <!-- Create Key Drawer -->
    <JfDrawer v-model:open="slideOpen" title="创建推理密钥">
      <form class="grid gap-5" @submit.prevent="createKey">
        <JfField
          label="密钥名称"
          name="key-name"
          required
        >
          <JfInput
            v-model="formData.name"
            placeholder="例如 web-client、app-production"
            class="w-full font-mono"
            required
            autofocus
          />
        </JfField>


        <ErrorAlert v-if="formError" :error="formError" />
      </form>

      <template #footer>
        <div class="jf-action-group justify-end">
          <JfButton variant="ghost" :disabled="formLoading" @click="slideOpen = false">取消</JfButton>
          <JfButton :loading="formLoading" @click="createKey">生成密钥</JfButton>
        </div>
      </template>
    </JfDrawer>

    <!-- Reveal Key Dialog -->
    <JfDialog v-model:open="revealOpen" :title="revealedTitle" size="md">
      <div class="grid gap-4">
        <JfAlert
          tone="warning"
          title="推理密钥仅展示一次，请立即复制并妥善保存"
        />

        <div class="rounded-[var(--jf-radius-control)] border border-line bg-tonal p-3">
          <code class="block font-mono text-sm jf-anywhere select-all">{{ revealedKey }}</code>
        </div>
      </div>

      <template #footer>
        <div class="jf-action-group justify-end">
          <JfButton
            :variant="copied ? 'secondary' : 'primary'"
            :icon="copied ? 'check' : 'clipboard-document'"
            @click="copyRevealedKey"
          >
            {{ copied ? '已复制到剪贴板' : '复制密钥' }}
          </JfButton>
          <JfButton variant="ghost" @click="closeReveal">关闭</JfButton>
        </div>
      </template>
    </JfDialog>
  </div>
</template>
