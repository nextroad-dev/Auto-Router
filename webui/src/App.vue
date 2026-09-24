<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { api, setUnauthorizedHandler } from '@/lib/api'
import { clearSession, sessionState } from '@/lib/session'
import { router } from '@/router'

const { t } = useI18n()
const route = useRoute()
const isLogin = computed(() => route.name === 'login')
const menuOpen = ref(false)
const colorMode = ref<'light' | 'dark'>('light')

onMounted(() => {
  let stored: string | null = null
  try { stored = window.localStorage.getItem('auto-router-color-mode') } catch { /* use the system preference */ }
  const mode = stored === 'dark' || stored === 'light'
    ? stored
    : window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  colorMode.value = mode
  document.documentElement.classList.toggle('dark', mode === 'dark')
})

function toggleColorMode() {
  colorMode.value = colorMode.value === 'dark' ? 'light' : 'dark'
  document.documentElement.classList.toggle('dark', colorMode.value === 'dark')
  try { window.localStorage.setItem('auto-router-color-mode', colorMode.value) } catch { /* theme still changes for this visit */ }
}

const navigation = computed(() => [
  { label: t('app.overview'), icon: 'i-heroicons-squares-2x2', to: '/' },
  { label: t('app.providers'), icon: 'i-heroicons-server-stack', to: '/providers' },
  { label: t('app.models'), icon: 'i-heroicons-cpu-chip', to: '/models' },
  { label: t('app.groups'), icon: 'i-lucide-layers-2', to: '/pairs' },
])
const systemNavigation = computed(() => [
  { label: t('app.settings'), icon: 'i-heroicons-cog-6-tooth', to: '/settings' },
  { label: t('app.keys'), icon: 'i-heroicons-key', to: '/keys' },
  { label: t('app.logs'), icon: 'i-heroicons-document-text', to: '/logs' },
])
const pageTitle = computed(() => typeof route.meta.title === 'string' ? route.meta.title : t('app.overview'))

setUnauthorizedHandler(() => {
  clearSession()
  if (route.name !== 'login') void router.replace({ name: 'login', query: { next: route.fullPath } })
})


async function logout() {
  try { await api.delete<{ logged_out: boolean }>('/admin/v1/session') } catch { /* local session is cleared either way */ }
  clearSession()
  await router.replace({ name: 'login' })
}
</script>

<template>
  <UApp :toaster="null">
    <main v-if="isLogin" class="auth-scene min-h-screen flex items-center justify-center px-4 py-10">
      <RouterView />
    </main>

    <div v-else class="dashboard-shell min-h-screen">
      <div v-if="menuOpen" class="fixed inset-0 z-40 bg-neutral-950/40 backdrop-blur-sm lg:hidden" @click="menuOpen = false" />

      <aside :class="['dashboard-sidebar', { 'dashboard-sidebar-open': menuOpen }]" :aria-label="t('app.navigation')">
        <div class="sidebar-brand">
          <UButton class="lg:hidden" color="neutral" variant="ghost" icon="i-heroicons-x-mark" :aria-label="t('app.closeMenu')" @click="menuOpen = false" />
        </div>

        <div class="sidebar-section-label">{{ t('app.workspace') }}</div>
        <nav class="sidebar-nav" :aria-label="t('app.workspace')">
          <RouterLink v-for="item in navigation" :key="item.to" :to="item.to" class="sidebar-link" :class="{ 'sidebar-link-active': route.path === item.to }" @click="menuOpen = false">
            <UIcon :name="item.icon" class="h-[18px] w-[18px]" />
            <span>{{ item.label }}</span>
            <span v-if="route.path === item.to" class="sidebar-active-dot" />
          </RouterLink>
        </nav>

        <div class="sidebar-section-label mt-8">{{ t('app.management') }}</div>
        <nav class="sidebar-nav" :aria-label="t('app.management')">
          <RouterLink v-for="item in systemNavigation" :key="item.to" :to="item.to" class="sidebar-link" :class="{ 'sidebar-link-active': route.path === item.to }" @click="menuOpen = false">
            <UIcon :name="item.icon" class="h-[18px] w-[18px]" />
            <span>{{ item.label }}</span>
            <span v-if="route.path === item.to" class="sidebar-active-dot" />
          </RouterLink>
        </nav>

        <div class="sidebar-bottom">
          <div class="account-card">
            <span class="grid h-[34px] w-[34px] shrink-0 place-items-center rounded-full border border-default bg-elevated text-primary"><UIcon name="i-heroicons-user" aria-hidden="true" /></span>
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm font-medium">{{ sessionState.name || t('app.admin') }}</span>
            </span>
            <UButton color="neutral" variant="ghost" icon="i-heroicons-arrow-left-on-rectangle" :aria-label="t('app.logout')" @click="logout" />
          </div>
        </div>
      </aside>

      <div class="dashboard-main min-w-0">
        <header class="dashboard-topbar">
          <div class="flex min-w-0 items-center gap-3">
            <UButton class="lg:hidden" color="neutral" variant="ghost" icon="i-heroicons-bars-3" :aria-label="t('app.openMenu')" @click="menuOpen = true" />
            <div class="min-w-0">
              <h1 class="truncate text-base font-semibold tracking-tight sm:text-lg">{{ pageTitle }}</h1>
            </div>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <UButton color="neutral" variant="ghost" :icon="colorMode === 'dark' ? 'i-lucide-sun' : 'i-lucide-moon'" :aria-label="t('app.theme')" @click="toggleColorMode" />
          </div>
        </header>

        <main class="dashboard-content">
          <div class="mx-auto w-full max-w-[1440px]">
            <RouterView />
          </div>
        </main>
      </div>
    </div>
  </UApp>
</template>
