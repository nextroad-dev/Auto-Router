<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { api, ApiError } from '@/lib/api'
import { errorNotice } from '@/lib/errors'
import ErrorAlert from '@/components/ErrorAlert.vue'
import JfCard from '@/components/JfCard.vue'
import JfField from '@/components/JfField.vue'
import JfInput from '@/components/JfInput.vue'
import JfButton from '@/components/JfButton.vue'
import JfSkeleton from '@/components/JfSkeleton.vue'
import type { PasswordSession, SetupStatus } from '@/lib/admin-contracts'
import { sessionState } from '@/lib/session'

const router = useRouter()
const route = useRoute()
const initialized = ref<boolean>()
const password = ref('')
const confirmation = ref('')
const busy = ref(false)
const setupStatusLoading = ref(false)
const setupStatusFailed = ref(false)
const errorMsg = ref<unknown>()

function safeTarget(target: unknown): string {
  if (typeof target !== 'string' || !target.startsWith('/') || target.startsWith('//')) return '/'
  try {
    const url = new URL(target, window.location.origin)
    if (url.origin !== window.location.origin) return '/'
    return `${url.pathname}${url.search}${url.hash}`
  } catch { return '/' }
}

async function loadSetupStatus() {
  if (setupStatusLoading.value) return
  setupStatusLoading.value = true
  setupStatusFailed.value = false
  errorMsg.value = undefined
  try {
    const status = await api.get<SetupStatus>('/admin/v1/setup/status')
    initialized.value = status.password_set
  } catch (cause) {
    setupStatusFailed.value = true
    errorMsg.value = errorNotice(cause)
  } finally {
    setupStatusLoading.value = false
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
  errorMsg.value = undefined
  if (!password.value) {
    errorMsg.value = initialized.value ? '请输入管理员密码。' : '请设置管理员密码。'
    return
  }
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
      : errorNotice(cause)
  } finally {
    password.value = ''
    confirmation.value = ''
    busy.value = false
  }
}

onMounted(() => { void loadSetupStatus() })
</script>

<template>
  <JfCard class="auth-card w-full max-w-[440px]">
    <div class="mb-2 text-center">
      <h1 class="jf-page-title">{{ initialized === false ? '设置管理员密码' : '登录 Auto Router' }}</h1>
    </div>

    <ErrorAlert v-if="errorMsg" class="mt-5" :error="errorMsg">
      <template v-if="setupStatusFailed" #actions>
        <JfButton variant="secondary" :loading="setupStatusLoading" @click="loadSetupStatus">重试读取状态</JfButton>
      </template>
    </ErrorAlert>
    <div v-if="initialized === undefined && !errorMsg" class="mt-5" aria-busy="true">
      <JfSkeleton height="48px" shape="block" />
    </div>

    <form v-if="initialized !== undefined" class="mt-6 grid gap-5" @submit.prevent="submit">
      <JfField :label="initialized ? '管理员密码' : '新管理员密码'" name="admin-password" required>
        <JfInput
          v-model="password"
          type="password"
          :placeholder="initialized ? '输入管理员密码' : '创建管理员密码（≥12位）'"
          icon="lock-closed"
          size="lg"
          :disabled="busy"
          :autocomplete="initialized ? 'current-password' : 'new-password'"
          autofocus
        />
      </JfField>
      <JfField v-if="!initialized" label="确认密码" name="confirm-password" required>
        <JfInput
          v-model="confirmation"
          type="password"
          placeholder="再次输入密码"
          size="lg"
          :disabled="busy"
          autocomplete="new-password"
        />
      </JfField>
      <JfButton type="submit" block size="lg" :loading="busy">
        {{ initialized ? '登录控制台' : '设置密码并登录' }}
      </JfButton>
    </form>
  </JfCard>
</template>

<style scoped>
.auth-card {
  box-shadow: var(--jf-shadow-floating);
  border-radius: var(--jf-radius-dialog);
  padding: var(--jf-space-8);
}
</style>
