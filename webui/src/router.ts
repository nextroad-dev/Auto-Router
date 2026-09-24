import { createRouter, createWebHistory } from 'vue-router'
import { api, type Session } from '@/lib/api'
import { clearSession, sessionState } from '@/lib/session'

export const router = createRouter({
  history: createWebHistory('/admin/'),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('./pages/Auth/Login.vue'),
      meta: { title: '登录' }
    },
    {
      path: '/',
      name: 'dashboard',
      component: () => import('./pages/Dashboard/Index.vue'),
      meta: { title: '总览', requiresAuth: true }
    },
    {
      path: '/providers',
      name: 'providers',
      component: () => import('./pages/Catalogue/Providers.vue'),
      meta: { title: '提供商', requiresAuth: true }
    },
    {
      path: '/models',
      name: 'models',
      component: () => import('./pages/Catalogue/Models.vue'),
      meta: { title: '模型管理', requiresAuth: true }
    },
    {
      path: '/pairs',
      name: 'model-groups',
      component: () => import('./pages/Catalogue/ModelGroups.vue'),
      meta: { title: '模型分组', requiresAuth: true }
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('./pages/System/Settings.vue'),
      meta: { title: '系统设置', requiresAuth: true }
    },
    {
      path: '/keys',
      name: 'keys',
      component: () => import('./pages/System/Keys.vue'),
      meta: { title: '凭据管理', requiresAuth: true }
    },
    {
      path: '/logs',
      name: 'logs',
      component: () => import('./pages/Logs/Index.vue'),
      meta: { title: '请求日志', requiresAuth: true }
    },
    {
      path: '/:pathMatch(.*)*',
      redirect: '/'
    }
  ]
})

let sessionCheck: Promise<boolean> | undefined

async function hasServerSession() {
  if (sessionState.name) return true
  if (!sessionCheck) {
    sessionCheck = api.get<Session>('/admin/v1/session').then(session => {
      sessionState.name = session.name
      sessionState.expiresAt = session.expires_at
      return true
    }).catch(() => {
      clearSession()
      return false
    }).finally(() => { sessionCheck = undefined })
  }
  return sessionCheck
}

router.beforeEach(async (to) => {
  if (to.meta.requiresAuth && !(await hasServerSession())) {
    return { name: 'login', query: { next: to.fullPath } }
  }
  return true
})

router.afterEach((to) => {
  const title = typeof to.meta.title === 'string' ? to.meta.title : ''
  document.title = title ? `${title} · Auto Router` : 'Auto Router'
})
