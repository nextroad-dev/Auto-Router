import { createI18n } from 'vue-i18n'

// The error vocabulary mirrors docs/openapi.yaml exactly. `errors.codes` is keyed by
// the union of AdminErrorCode and InferenceErrorCode; webui/src/lib/errors.ts proves
// at compile time that no contract code is missing and that no stale key remains.
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
      collapseNav: '收起导航栏',
      expandNav: '展开导航栏',
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
    errors: {
      codes: {
        // ---- Management-surface codes -------------------------------------------------
        invalid_request: {
          title: '请求内容无效',
          description: '检查表单中的必填项与格式后重试。',
        },
        invalid_filter: {
          title: '筛选条件无效',
          description: '检查表单中的必填项与格式后重试。调整筛选项后重新查询。',
        },
        invalid_cursor: {
          title: '分页位置已失效',
          description: '检查表单中的必填项与格式后重试。重新加载列表。',
        },
        invalid_group: {
          title: '模型分组配置无效',
          description: '检查表单中的必填项与格式后重试。三个分组各含互不重复的已启用绑定，每组最多 8 个。',
        },
        invalid_scopes: {
          title: '凭据权限范围无效',
          description: '检查表单中的必填项与格式后重试。推理凭据仅支持 inference 范围。',
        },
        invalid_model: {
          title: '模型标识无效',
          description: '检查表单中的必填项与格式后重试。',
        },
        invalid_password: {
          title: '密码不正确或不符合要求',
          description: '请重新登录管理台，或改用具备该权限的凭据。密码长度至少 12 字节。',
        },
        invalid_session: {
          title: '管理会话已失效',
          description: '请重新登录管理台，或改用具备该权限的凭据。',
        },
        invalid_api_key: {
          title: '凭据无效或已失效',
          description: '请重新登录管理台，或改用具备该权限的凭据。',
        },
        insufficient_scope: {
          title: '权限不足',
          description: '请重新登录管理台，或改用具备该权限的凭据。',
        },
        cross_site_request: {
          title: '请求来源未通过安全校验',
          description: '请重新登录管理台，或改用具备该权限的凭据。请从管理台页面内发起操作。',
        },
        bootstrap_token_required: {
          title: '需要初始化令牌',
          description: '从非本机访问进行首次设置时，需要填写服务启动日志中打印的一次性初始化令牌（bootstrap_token）。',
        },
        too_many_attempts: {
          title: '尝试次数过多',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。请稍后重试。',
        },
        request_too_large: {
          title: '提交内容过大',
          description: '检查表单中的必填项与格式后重试。减少提交体积后重试。',
        },
        unknown_model: {
          title: '找不到指定模型',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。',
        },
        unknown_provider: {
          title: '找不到指定提供商',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。',
        },
        unknown_pair: {
          title: '找不到指定绑定',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。',
        },
        not_found: {
          title: '接口不存在',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。管理台与后端版本可能不匹配，刷新页面后重试。',
        },
        provider_exists: {
          title: '提供商标识已存在',
          description: '刷新后重试；重复提交不会覆盖已有数据。',
        },
        model_exists: {
          title: '模型标识已存在',
          description: '刷新后重试；重复提交不会覆盖已有数据。',
        },
        pair_exists: {
          title: '该提供商与模型绑定已存在',
          description: '刷新后重试；重复提交不会覆盖已有数据。',
        },
        provider_not_deletable: {
          title: '该提供商不可删除',
          description: '请重新登录管理台，或改用具备该权限的凭据。仅管理员创建的提供商可删除。',
        },
        provider_not_configured: {
          title: '提供商未配置上游地址',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。先在提供商页面填写上游地址。',
        },
        key_not_found: {
          title: '找不到该凭据',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。',
        },
        key_name_exists: {
          title: '凭据名称已存在',
          description: '刷新后重试；重复提交不会覆盖已有数据。',
        },
        credential_limit: {
          title: '已达到凭据数量上限',
          description: '刷新后重试；重复提交不会覆盖已有数据。先撤销不再使用的凭据。',
        },
        password_already_set: {
          title: '管理密码已设置',
          description: '刷新后重试；重复提交不会覆盖已有数据。请直接登录；忘记密码需使用离线恢复流程。',
        },
        password_not_set: {
          title: '管理密码尚未设置',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。先完成初始化设置。',
        },
        settings_conflict: {
          title: '设置已被其他操作修改',
          description: '刷新后重试；重复提交不会覆盖已有数据。页面已重新读取最新值，确认后再提交。',
        },
        restart_required: {
          title: '该设置需要重启服务才能生效',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。改动已保存，重启后生效。',
        },
        unsupported_setting: {
          title: '该设置不支持在线修改',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。需离线或在服务启动前修改。',
        },
        read_only_state: {
          title: '当前设置为只读状态',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。服务未挂载可写存储，无法保存。',
        },
        storage_error: {
          title: '存储操作失败',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。',
        },
        snapshot_publish_failed: {
          title: '改动已保存但运行配置未刷新',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。重启服务后生效。',
        },
        sync_failed: {
          title: '同步失败，现有配置未被修改。',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。',
        },
        sync_unavailable: {
          title: '同步功能当前不可用',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。',
        },
        discovery_failed: {
          title: '无法读取提供商模型列表',
          description: '请检查提供商地址与网络连通性后重试。',
        },
        metadata_lookup_failed: {
          title: '无法获取模型能力元数据',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。可在模型管理页手动填写能力。',
        },
        dashboard_missing: {
          title: '管理台页面渲染失败',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。重新加载页面。',
        },

        // ---- Forwarding-surface codes -------------------------------------------------
        invalid_request_error: {
          title: '请求内容无效',
          description: '检查表单中的必填项与格式后重试。',
        },
        unsupported_media_type: {
          title: '请求内容类型不支持',
          description: '检查表单中的必填项与格式后重试。请使用 application/json。',
        },
        unsupported_conversion: {
          title: '该提供商无法保持请求语义',
          description: '检查表单中的必填项与格式后重试。改用兼容协议或简化请求。',
        },
        provider_override_disabled: {
          title: '未启用提供商覆盖语法',
          description: '请重新登录管理台，或改用具备该权限的凭据。需管理员开启 routing.allow_provider_override。',
        },
        invalid_model_identifier: {
          title: '模型标识格式无效',
          description: '检查表单中的必填项与格式后重试。',
        },
        model_not_found: {
          title: '找不到该模型',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。',
        },
        provider_not_found: {
          title: '找不到该提供商',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。',
        },
        auto_routing_unavailable: {
          title: '自动路由当前不可用',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。',
        },
        routing_unavailable: {
          title: '自动路由不可用',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。',
        },
        no_eligible_candidate: {
          title: '没有可用的候选模型',
          description: '刷新列表确认对象是否仍然存在，可能已被其他操作删除。检查模型组配置与能力/上下文筛选条件。',
        },
        truncated_evidence: {
          title: '请求超出分析边界，无法安全路由',
          description: '检查表单中的必填项与格式后重试。减小请求体积或改用指定模型。',
        },
        upstream_unavailable: {
          title: '上游提供商无法访问',
          description: '请检查提供商地址与网络连通性后重试。',
        },
        upstream_timeout: {
          title: '上游提供商响应超时',
          description: '请检查提供商地址与网络连通性后重试。可重试或更换提供商。',
        },
        upstream_response_too_large: {
          title: '上游响应过大',
          description: '提供商返回的内容超过了转换缓冲上限。可改用流式请求，或调整 -max-buffered-response-bytes 后重试。',
        },
        client_closed_request: {
          title: '客户端提前断开连接',
          description: '请检查提供商地址与网络连通性后重试。请求未完成，可重试。',
        },
        internal_error: {
          title: '服务内部错误',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。',
        },
        debug_route_unavailable: {
          title: '离线诊断端点未就绪',
          description: '服务暂时无法完成该操作，请稍后重试；若持续失败请查看服务日志。',
        },
      },
      // Front-end synthetic failures. They are not part of the OpenAPI vocabulary, so they
      // live outside `codes` and cannot be mistaken for a backend contract.
      fallback: {
        network: {
          title: '无法连接管理服务',
          description: '请确认服务已启动且网络可达。',
        },
        clientUnknown: {
          title: '操作未能完成',
          description: '服务返回了未识别的错误（HTTP {status}，代码 {code}），请刷新后重试。',
        },
        server: {
          title: '服务暂时不可用',
          description: '服务端返回了未识别的错误（HTTP {status}，代码 {code}），请稍后重试并查看服务日志。',
        },
        unknown: {
          title: '操作未能完成',
          description: '服务返回了未识别的错误（代码 {code}），请刷新后重试；若持续失败请查看服务日志。',
        },
      },
      field: '字段：{field}',
      httpStatus: 'HTTP {status}',
    },
    labels: {
      protocol: {
        chat_completions: 'Chat Completions',
        responses: 'Responses',
        native: '原生协议',
      },
      routingMode: {
        auto: '自动路由',
        explicit: '指定模型',
      },
      selectionMode: {
        explicit: '显式指定',
        jev: 'Jev 推荐',
        blend: '混合决策',
        default_model: '默认模型兜底',
        first_eligible: '首个可用候选',
      },
      confidenceBand: {
        high: '高',
        medium: '中',
        low: '低',
      },
      usageStatus: {
        observed: '已观测',
        absent: '上游未上报',
        malformed: '格式异常',
        oversized: '超出观测上限',
        interrupted: '中断未观测',
      },
      statusClass: {
        success: '成功（2xx）',
        client_error: '客户端错误（4xx）',
        server_error: '服务端错误（5xx）',
      },
      inputMode: {
        content: '完整内容',
        redacted: '脱敏内容',
        features_only: '仅特征',
      },
      jevStatus: {
        disabled: '未启用',
        skipped_single_model: '单模型跳过',
        skipped_insufficient_evidence: '证据不足跳过',
        skipped_too_many_models: '候选过多跳过',
        ok: '成功',
      },
      // Accessories to the two prefixed vocabularies. `failure:` reuses `fallbackReason`,
      // `not_eligible:` reuses `notEligible`; only the prefixes need a rendering.
      jevStatusFailure: '失败（{reason}）',
      fallbackReason: {
        none: '未发生兜底',
        not_requested: '未请求推荐',
        confidence_low: '置信度过低',
        selected_model_ineligible: '推荐模型不可用',
        jev_timeout: 'Jev 超时',
        jev_unavailable: 'Jev 不可达',
        jev_rejected: 'Jev 拒绝',
        jev_invalid_result: 'Jev 结果无效',
        jev_canceled: 'Jev 调用取消',
        truncated_evidence: '证据被截断',
        no_candidates: '无候选模型',
      },
      notEligible: '不满足筛选：{code}',
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
