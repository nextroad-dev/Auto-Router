<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { api, ApiError, errorMessage } from '@/lib/api'
import type { PasswordSession, SetupStatus } from '@/lib/admin-contracts'
import { sessionState } from '@/lib/session'

const router = useRouter()
const route = useRoute()
const initialized = ref<boolean>()
const password = ref('')
const confirmation = ref('')
const busy = ref(false)
const errorMsg = ref('')

function safeTarget(target: unknown): string {
  if (typeof target !== 'string' || !target.startsWith('/') || target.startsWith('//')) return '/'
  try {
    const url = new URL(target, window.location.origin)
    if (url.origin !== window.location.origin) return '/'
    return `${url.pathname}${url.search}${url.hash}`
  } catch { return '/' }
}

async function loadSetupStatus() {
  errorMsg.value = ''
  try {
    const status = await api.get<SetupStatus>('/admin/v1/setup/status')
    initialized.value = status.password_set
  } catch (cause) {
    errorMsg.value = errorMessage(cause)
  }
}

async function establishSession(value: string) {
  await api.post<PasswordSession>('/admin/v1/session', { password: value })
  const session = await api.get<PasswordSession>('/admin/v1/session')
  sessionState.name = session.name
  sessionState.expiresAt = session.expires_at
  await router.replace(safeTarget(route.query.next))
}

async function submit() {
  errorMsg.value = ''
  if (!password.value) { errorMsg.value = initialized.value ? '请输入管理员密码。' : '请设置管理员密码。'; return }
  if (initialized.value === false && password.value !== confirmation.value) {
    errorMsg.value = '两次输入的密码不一致。'
    return
  }
  if (initialized.value === false && new TextEncoder().encode(password.value).length < 12) {
    errorMsg.value = '管理员密码至少需要 12 字节。'
    return
  }

  busy.value = true
  const submitted = password.value
  try {
    if (initialized.value === false) {
      await api.post('/admin/v1/setup/password', { password: submitted })
      initialized.value = true
    }
    await establishSession(submitted)
  } catch (cause) {
    errorMsg.value = cause instanceof ApiError && cause.status === 401
      ? '管理员密码不正确。'
      : errorMessage(cause)
  } finally {
    password.value = ''
    confirmation.value = ''
    busy.value = false
  }
}

onMounted(() => { void loadSetupStatus() })
</script>

<template>
  <UCard class="login-card w-full max-w-[440px] overflow-hidden">
    <div class="mb-8 flex flex-col items-center text-center">
      <h1 class="text-2xl font-semibold tracking-tight">{{ initialized === false ? '设置管理员密码' : '登录' }}</h1>
    </div>

    <UAlert v-if="errorMsg" color="error" variant="soft" class="mb-5" role="alert" :title="errorMsg" />
    <USkeleton v-if="initialized === undefined && !errorMsg" class="mb-5 h-10 w-full" />

    <form v-if="initialized !== undefined" class="space-y-5" @submit.prevent="submit">
      <UFormField :label="initialized ? '管理员密码' : '新管理员密码'" name="admin-password" required>
        <UInput v-model="password" type="password" :placeholder="initialized ? '输入管理员密码' : '创建管理员密码'" icon="i-heroicons-lock-closed" :disabled="busy" :autocomplete="initialized ? 'current-password' : 'new-password'" autofocus size="lg" class="w-full" />
      </UFormField>
      <UFormField v-if="!initialized" label="确认密码" name="confirm-password" required>
        <UInput v-model="confirmation" type="password" placeholder="再次输入密码" :disabled="busy" autocomplete="new-password" size="lg" class="w-full" />
      </UFormField>
      <UButton type="submit" block size="lg" :loading="busy">{{ initialized ? '登录' : '设置密码并登录' }}</UButton>
    </form>

  </UCard>
</template>

<style scoped>
.login-card { border-radius: 18px; box-shadow: 0 22px 70px rgb(24 24 27 / 8%); }
</style>
