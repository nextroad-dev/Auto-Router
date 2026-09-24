<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, errorMessage, type SettingsDocument } from '@/lib/api'
import { clearSession } from '@/lib/session'

const router = useRouter()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const saved = ref(false)
const keyConfigured = ref(false)
const jev = reactive({ enabled: false, base_url: '', model: '', api_key: '' })
const password = reactive({ current: '', next: '', confirm: '' })
const passwordBusy = ref(false)
const passwordError = ref('')

async function loadSettings() {
  loading.value = true
  error.value = ''
  try {
    const report = await api.get<SettingsDocument>('/admin/v1/settings')
    const field = (path: string) => report.settings.find(item => item.path === path)
    jev.enabled = Boolean(field('jev.enabled')?.value)
    jev.base_url = String(field('jev.base_url')?.value ?? '')
    jev.model = String(field('jev.model')?.value ?? '')
    jev.api_key = ''
    keyConfigured.value = Boolean(field('jev.api_key')?.set)
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    loading.value = false
  }
}

async function saveJev() {
  error.value = ''
  saved.value = false
  if (jev.enabled && (!jev.base_url.trim() || !jev.model.trim() || (!keyConfigured.value && !jev.api_key.trim()))) {
    error.value = '启用 Jev 时需要端点、模型和 API 密钥。'
    return
  }
  saving.value = true
  try {
    const values: Record<string, unknown> = {
      enabled: jev.enabled,
      base_url: jev.base_url.trim(),
      model: jev.model.trim(),
    }
    if (jev.api_key.trim()) values.api_key = jev.api_key.trim()
    await api.patch('/admin/v1/settings', { jev: values })
    await loadSettings()
    saved.value = true
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    saving.value = false
  }
}

async function changePassword() {
  passwordError.value = ''
  if (!password.current || !password.next) {
    passwordError.value = '请输入当前密码和新密码。'
    return
  }
  if (password.next !== password.confirm) {
    passwordError.value = '两次输入的新密码不一致。'
    return
  }
  if (new TextEncoder().encode(password.next).length < 12) {
    passwordError.value = '新密码至少需要 12 字节。'
    return
  }
  passwordBusy.value = true
  try {
    await api.patch('/admin/v1/password', { current_password: password.current, password: password.next })
    password.current = ''
    password.next = ''
    password.confirm = ''
    clearSession()
    await router.replace({ name: 'login' })
  } catch (cause) {
    passwordError.value = errorMessage(cause)
  } finally {
    passwordBusy.value = false
  }
}

onMounted(() => { void loadSettings() })
</script>

<template>
  <div class="space-y-5">
    <UAlert v-if="error" color="error" variant="soft" :title="error" />
    <UAlert v-if="saved" color="success" variant="soft" title="已保存" />

    <UCard>
      <template #header><h3 class="font-semibold">Jev</h3></template>
      <form class="grid gap-4 sm:grid-cols-2" @submit.prevent="saveJev">
        <UFormField label="启用">
          <USwitch v-model="jev.enabled" :disabled="loading || saving" />
        </UFormField>
        <span class="hidden sm:block" />
        <UFormField label="服务端点">
          <UInput v-model="jev.base_url" type="url" placeholder="https://..." :disabled="loading || saving" class="w-full" />
        </UFormField>
        <UFormField label="模型">
          <UInput v-model="jev.model" placeholder="jev-latest" :disabled="loading || saving" class="w-full" />
        </UFormField>
        <UFormField :label="keyConfigured ? '替换 API 密钥' : 'API 密钥'">
          <UInput v-model="jev.api_key" type="password" autocomplete="new-password" :placeholder="keyConfigured ? '留空保留现有密钥' : ''" :disabled="loading || saving" class="w-full" />
        </UFormField>
        <div class="flex justify-end sm:col-span-2">
          <UButton type="submit" icon="i-heroicons-check" :loading="saving" :disabled="loading">保存 Jev</UButton>
        </div>
      </form>
    </UCard>

    <UCard>
      <template #header><h3 class="font-semibold">管理员密码</h3></template>
      <form class="grid gap-4 sm:grid-cols-2" @submit.prevent="changePassword">
        <UFormField label="当前密码" required>
          <UInput v-model="password.current" type="password" autocomplete="current-password" class="w-full" />
        </UFormField>
        <span class="hidden sm:block" />
        <UFormField label="新密码" required>
          <UInput v-model="password.next" type="password" autocomplete="new-password" class="w-full" />
        </UFormField>
        <UFormField label="确认新密码" required>
          <UInput v-model="password.confirm" type="password" autocomplete="new-password" class="w-full" />
        </UFormField>
        <UAlert v-if="passwordError" class="sm:col-span-2" color="error" variant="soft" :title="passwordError" />
        <div class="flex justify-end sm:col-span-2">
          <UButton type="submit" :loading="passwordBusy">更新密码</UButton>
        </div>
      </form>
    </UCard>
  </div>
</template>
