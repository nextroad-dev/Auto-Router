import { createI18n } from 'vue-i18n'

export const messages = {
  'zh-CN': {
    app: {
      navigation: '导航菜单',
      overview: '总览',
      providers: '提供商',
      models: '模型管理',
      groups: '模型分组',
      settings: '系统设置',
      keys: 'API 密钥',
      logs: '请求日志',
      admin: '管理员',
      logout: '退出登录',
      theme: '切换颜色主题',
      openMenu: '打开导航菜单',
      closeMenu: '关闭导航菜单',
      workspace: '工作区',
      management: '管理',
    },
    common: {
      refresh: '刷新',
      loading: '正在加载…',
      retry: '重试',
      empty: '暂无数据',
      error: '暂时无法加载数据',
      enabled: '已启用',
      disabled: '已禁用',
      status: '状态',
      actions: '操作',
      cancel: '取消',
      save: '保存',
      create: '创建',
      edit: '编辑',
      search: '搜索',
      previous: '上一页',
      next: '下一页',
      noData: '暂无数据',
    },
  },
} as const

export const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackLocale: 'zh-CN',
  messages,
  datetimeFormats: {
    'zh-CN': {
      short: { year: 'numeric', month: '2-digit', day: '2-digit' },
      dateTime: { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false },
    },
  },
  numberFormats: {
    'zh-CN': {
      decimal: { maximumFractionDigits: 2 },
      integer: { maximumFractionDigits: 0 },
    },
  },
})
