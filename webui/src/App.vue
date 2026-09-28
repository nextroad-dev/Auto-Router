<script setup lang="ts">
import { computed, onMounted, ref, useTemplateRef, watch } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import JfButton from '@/components/JfButton.vue'
import JfIcon from '@/components/JfIcon.vue'
import { api, setUnauthorizedHandler } from '@/lib/api'
import { clearSession } from '@/lib/session'
import { savedToastVisible } from '@/lib/save-toast'
import { useOverlayFocus } from '@/lib/overlay'
import { router } from '@/router'

const { t } = useI18n()
const route = useRoute()
const isLogin = computed(() => route.name === 'login')
const menuOpen = ref(false)
const desktopLayout = useMediaQuery('(min-width: 960px)')
const sidebarPanel = useTemplateRef<HTMLElement>('sidebarPanel')
const collapsed = ref(false)
const colorMode = ref<'light' | 'dark'>('light')

onMounted(() => {
  let stored: string | null = null
  try { stored = window.localStorage.getItem('auto-router-color-mode') } catch { /* use the system preference */ }
  const mode = stored === 'dark' || stored === 'light'
    ? stored
    : window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  colorMode.value = mode
  applyColorMode(mode)
  try { collapsed.value = window.localStorage.getItem('auto-router-sidebar-collapsed') === 'true' } catch { /* keep the rail expanded */ }
})

function applyColorMode(mode: 'light' | 'dark') {
  document.documentElement.setAttribute('data-theme', mode)
  document.documentElement.style.colorScheme = mode
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', mode === 'dark' ? '#1A1A18' : '#F7F7F3')
}

function toggleColorMode() {
  colorMode.value = colorMode.value === 'dark' ? 'light' : 'dark'
  applyColorMode(colorMode.value)
  try { window.localStorage.setItem('auto-router-color-mode', colorMode.value) } catch { /* theme still changes for this visit */ }
}

function toggleCollapsed() {
  collapsed.value = !collapsed.value
  try { window.localStorage.setItem('auto-router-sidebar-collapsed', String(collapsed.value)) } catch { /* rail reopens next visit */ }
}

const navigation = computed(() => [
  { label: t('app.overview'), icon: 'squares-2x2', to: '/' },
  { label: t('app.providers'), icon: 'server-stack', to: '/providers' },
  { label: t('app.models'), icon: 'cpu-chip', to: '/models' },
  { label: t('app.groups'), icon: 'layers-2', to: '/pairs' },
])
const systemNavigation = computed(() => [
  { label: t('app.settings'), icon: 'cog-6-tooth', to: '/settings' },
  { label: t('app.keys'), icon: 'key', to: '/keys' },
  { label: t('app.logs'), icon: 'document-text', to: '/logs' },
])
const pageTitle = computed(() => typeof route.meta.title === 'string' ? route.meta.title : t('app.overview'))

useOverlayFocus({
  open: menuOpen,
  panel: sidebarPanel,
  onEscape: () => { menuOpen.value = false },
})
watch(desktopLayout, (isDesktop) => {
  if (isDesktop) menuOpen.value = false
})
watch(() => route.fullPath, () => { menuOpen.value = false })

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
  <main v-if="isLogin" class="auth-scene flex min-h-screen items-center justify-center px-4 py-10">
    <RouterView />
  </main>

  <div v-else class="dashboard-shell min-h-screen">
    <div v-if="menuOpen" class="dashboard-scrim lg:hidden" @click="menuOpen = false" />

    <aside
      ref="sidebarPanel"
      id="dashboard-navigation"
      :class="['dashboard-sidebar', { 'dashboard-sidebar-open': menuOpen, 'dashboard-sidebar-collapsed': collapsed }]"
      :role="menuOpen && !desktopLayout ? 'dialog' : undefined"
      :aria-modal="menuOpen && !desktopLayout ? 'true' : undefined"
      :aria-hidden="!desktopLayout && !menuOpen ? 'true' : undefined"
      :inert="!desktopLayout && !menuOpen"
      :aria-label="t('app.navigation')"
    >
      <div class="sidebar-brand">
        <span class="sidebar-brand-title">Auto Router</span>
        <span class="sidebar-brand-close lg:hidden">
          <JfButton variant="ghost" square icon="x-mark" :aria-label="t('app.closeMenu')" @click="menuOpen = false" />
        </span>
      </div>

      <div class="sidebar-section-label">{{ t('app.workspace') }}</div>
      <nav class="sidebar-nav" :aria-label="t('app.workspace')">
        <RouterLink
          v-for="item in navigation"
          :key="item.to"
          :to="item.to"
          class="sidebar-link"
          :class="{ 'sidebar-link-active': route.path === item.to }"
          :aria-current="route.path === item.to ? 'page' : undefined"
          :title="collapsed ? item.label : undefined"
          @click="menuOpen = false"
        >
          <JfIcon :name="item.icon" size="md" class="sidebar-icon" aria-hidden="true" />
          <span class="sidebar-link-label">{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="sidebar-section-label mt-6">{{ t('app.management') }}</div>
      <nav class="sidebar-nav" :aria-label="t('app.management')">
        <RouterLink
          v-for="item in systemNavigation"
          :key="item.to"
          :to="item.to"
          class="sidebar-link"
          :class="{ 'sidebar-link-active': route.path === item.to }"
          :aria-current="route.path === item.to ? 'page' : undefined"
          :title="collapsed ? item.label : undefined"
          @click="menuOpen = false"
        >
          <JfIcon :name="item.icon" size="md" class="sidebar-icon" aria-hidden="true" />
          <span class="sidebar-link-label">{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="sidebar-bottom">
        <JfButton
          class="sidebar-logout-button"
          variant="ghost"
          icon="arrow-left-on-rectangle"
          :aria-label="t('app.logout')"
          block
          @click="logout"
        >
          <span class="sidebar-logout-label">{{ t('app.logout') }}</span>
        </JfButton>
      </div>
    </aside>

    <div :class="['dashboard-main', { 'dashboard-main-collapsed': collapsed }]">
      <header class="dashboard-topbar">
        <div class="flex min-w-0 items-center gap-2">
          <!-- Responsive display lives on a wrapper: the button's own display declaration
               is unlayered and would outrank Tailwind's `lg:hidden`. -->
          <span class="lg:hidden">
            <JfButton
              variant="ghost"
              square
              icon="bars-3"
              :aria-label="t('app.openMenu')"
              :aria-expanded="menuOpen"
              aria-controls="dashboard-navigation"
              @click="menuOpen = true"
            />
          </span>
          <span class="hidden lg:inline-flex">
            <JfButton
              variant="ghost"
              square
              :icon="collapsed ? 'chevron-right' : 'chevron-left'"
              :aria-label="collapsed ? t('app.expandNav') : t('app.collapseNav')"
              :aria-pressed="collapsed"
              @click="toggleCollapsed"
            />
          </span>
          <div class="min-w-0">
            <span class="dashboard-topbar-title jf-truncate">{{ pageTitle }}</span>
          </div>
        </div>
        <div class="jf-action-group shrink-0">
          <JfButton
            variant="ghost"
            square
            :icon="colorMode === 'dark' ? 'sun' : 'moon'"
            :aria-label="t('app.theme')"
            :aria-pressed="colorMode === 'dark'"
            @click="toggleColorMode"
          />
        </div>
      </header>

      <main class="dashboard-content">
        <div class="dashboard-content-inner">
          <RouterView />
        </div>
      </main>
    </div>

    <div v-if="savedToastVisible" class="save-toast" role="status" aria-live="polite" aria-atomic="true">
      更改已保存
    </div>
  </div>
</template>

<style scoped>
.save-toast {
  position: fixed;
  z-index: 2000;
  right: max(1rem, env(safe-area-inset-right));
  bottom: max(1rem, env(safe-area-inset-bottom));
  padding: 0.75rem 1rem;
  border: 1px solid var(--jf-success-border);
  border-radius: var(--jf-radius-control);
  background: var(--jf-success-bg);
  color: var(--jf-success-text);
  box-shadow: var(--jf-shadow-toast);
}

.sidebar-logout-button.jf-button {
  justify-content: flex-start;
}

.dashboard-sidebar-collapsed .sidebar-logout-label {
  display: none;
}

.dashboard-sidebar-collapsed .sidebar-logout-button.jf-button {
  justify-content: center;
  padding-inline: 0;
}
</style>
