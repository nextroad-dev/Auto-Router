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
      keys: '推理密钥',
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
      disabled: '已停用',
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
          description: '检查填写的内容与格式后重试。',
        },
        invalid_filter: {
          title: '筛选条件无效',
          description: '调整筛选条件后重新查询。',
        },
        invalid_cursor: {
          title: '分页位置已失效',
          description: '分页位置已过期，请重新加载列表。',
        },
        invalid_group: {
          title: '模型分组配置无效',
          description: '每组成员不能重复，必须是已启用的绑定（其提供商与模型也须启用），每组最多 8 个。',
        },
        invalid_scopes: {
          title: '推理密钥权限范围无效',
          description: '推理密钥只支持 inference 范围。',
        },
        invalid_model: {
          title: '模型标识无效',
          description: '模型标识须以字母或数字开头，只能包含字母、数字及 . _ : / - @ ~ +，最多 128 个字符，且不能是 auto。',
        },
        invalid_password: {
          title: '密码不正确或不符合要求',
          description: '确认当前密码输入正确；新密码至少需要 12 字节。',
        },
        invalid_session: {
          title: '管理会话已失效',
          description: '登录已过期，请重新登录管理台。',
        },
        invalid_api_key: {
          title: '推理密钥无效或已失效',
          description: '检查请求携带的推理密钥是否正确；密钥轮换后需改用新密钥。',
        },
        insufficient_scope: {
          title: '权限不足',
          description: '当前登录身份或推理密钥没有执行该操作的权限。',
        },
        cross_site_request: {
          title: '请求来源未通过安全校验',
          description: '请直接在管理台页面内操作，不要从其他站点发起请求。',
        },
        bootstrap_token_required: {
          title: '需要初始化令牌',
          description: '从非本机访问进行首次设置时，需要填写服务启动日志中打印的一次性初始化令牌（bootstrap_token）。',
        },
        too_many_attempts: {
          title: '尝试次数过多',
          description: '失败次数过多，请等待一分钟后再试。',
        },
        request_too_large: {
          title: '提交内容过大',
          description: '提交内容超过大小上限，请减少内容后重试。',
        },
        unknown_model: {
          title: '找不到指定模型',
          description: '该模型可能已被删除，刷新列表后重试。',
        },
        unknown_provider: {
          title: '找不到指定提供商',
          description: '该提供商可能已被删除，刷新列表后重试。',
        },
        unknown_pair: {
          title: '找不到指定绑定',
          description: '该绑定可能已被解除，刷新列表后重试。',
        },
        not_found: {
          title: '接口不存在',
          description: '管理台与服务端版本可能不一致，刷新页面后重试。',
        },
        provider_exists: {
          title: '提供商标识已存在',
          description: '换一个提供商标识，或直接编辑已有的提供商。',
        },
        model_exists: {
          title: '模型标识已存在',
          description: '换一个模型标识，或直接编辑已有的模型。',
        },
        pair_exists: {
          title: '该提供商与模型绑定已存在',
          description: '该绑定已存在，可在模型管理页编辑它。',
        },
        provider_not_deletable: {
          title: '该提供商不可删除',
          description: '只有管理员创建的提供商可以删除；同步或配置文件来源的提供商可以停用。',
        },
        provider_not_configured: {
          title: '提供商未配置上游地址',
          description: '先在提供商页面填写上游地址。',
        },
        key_not_found: {
          title: '找不到该推理密钥',
          description: '该推理密钥可能已被删除，刷新列表后重试。',
        },
        key_name_exists: {
          title: '推理密钥名称已存在',
          description: '换一个名称，或轮换已有的同名推理密钥。',
        },
        credential_limit: {
          title: '已达到推理密钥数量上限',
          description: '先删除不再使用的推理密钥，再创建新的。',
        },
        password_already_set: {
          title: '管理密码已设置',
          description: '请直接登录；忘记密码需使用离线恢复流程。',
        },
        password_not_set: {
          title: '管理密码尚未设置',
          description: '先完成初始化，设置管理员密码。',
        },
        settings_conflict: {
          title: '设置已被其他操作修改',
          description: '页面已重新读取最新设置，确认后再修改一次。',
        },
        restart_required: {
          title: '该设置需要重启服务才能生效',
          description: '运行时修改已被拒绝。请核对受支持的启动参数，调整启动配置并重启服务。',
        },
        unsupported_setting: {
          title: '该设置不支持在线修改',
          description: '检查字段名与当前版本支持的设置；启动参数类选项需要调整启动参数后重启，旧 JSON 配置文件已不再读取。',
        },
        read_only_state: {
          title: '当前设置为只读状态',
          description: '服务未挂载可写存储，设置无法保存。',
        },
        storage_error: {
          title: '存储操作失败',
          description: '数据库读写失败，请稍后重试；若持续失败请查看服务日志。',
        },
        snapshot_publish_failed: {
          title: '改动已保存但运行配置未刷新',
          description: '改动已写入数据库，但运行中的配置未刷新；重启服务后生效。',
        },
        sync_failed: {
          title: '同步失败，现有配置未被修改。',
          description: '确认服务能访问 models.dev 后重试，详细原因见服务日志。',
        },
        sync_unavailable: {
          title: '同步功能当前不可用',
          description: '服务当前无法执行同步，请稍后重试；若持续出现请查看服务日志。',
        },
        discovery_failed: {
          title: '无法读取提供商模型列表',
          description: '检查提供商地址、上游密钥与网络连通性后重试。',
        },
        metadata_lookup_failed: {
          title: '无法获取模型能力元数据',
          description: '暂时无法从 models.dev 读取能力信息，可稍后重试，或在模型管理页手动填写能力。',
        },
        dashboard_missing: {
          title: '管理台页面渲染失败',
          description: '管理台页面资源缺失，请重新加载页面；若持续出现请重新构建或部署服务。',
        },

        // ---- Forwarding-surface codes -------------------------------------------------
        invalid_request_error: {
          title: '请求内容无效',
          description: '检查请求体的字段与格式后重试。',
        },
        unsupported_media_type: {
          title: '请求内容类型不支持',
          description: '请求须使用 Content-Type: application/json。',
        },
        unsupported_conversion: {
          title: '该提供商无法保持请求语义',
          description: '所选提供商无法无损表达该请求，请改用协议兼容的提供商或简化请求。',
        },
        provider_override_disabled: {
          title: '未启用提供商覆盖语法',
          description: '可在“系统设置 → 全局路由行为”中开启“允许显式提供商覆盖”。',
        },
        invalid_model_identifier: {
          title: '模型标识格式无效',
          description: '检查请求中 model 字段的格式。',
        },
        model_not_found: {
          title: '找不到该模型',
          description: '请求的模型不存在或没有可用绑定，可通过 GET /v1/models 查看可用模型。',
        },
        provider_not_found: {
          title: '找不到该提供商',
          description: '请求的提供商不存在或已停用。',
        },
        auto_routing_unavailable: {
          title: '自动路由当前不可用',
          description: '检查模型分组配置后重试；详细原因见服务日志。',
        },
        routing_unavailable: {
          title: '路由服务不可用',
          description: '路由服务暂未就绪，请稍后重试；若持续出现请查看服务日志。',
        },
        no_eligible_candidate: {
          title: '没有可用的候选模型',
          description: '检查模型分组配置，以及请求所需的能力与上下文窗口。',
        },
        truncated_evidence: {
          title: '请求超出分析边界，无法安全路由',
          description: '减小请求体积，或直接指定模型。',
        },
        upstream_unavailable: {
          title: '上游提供商无法访问',
          description: '检查提供商地址、上游密钥与网络连通性后重试。',
        },
        upstream_timeout: {
          title: '上游提供商响应超时',
          description: '上游未在限定时间内响应，可稍后重试或更换提供商。',
        },
        upstream_response_too_large: {
          title: '上游响应过大',
          description: '提供商返回的内容超过了转换缓冲上限。可改用流式请求，或调整 -max-buffered-response-bytes 后重试。',
        },
        client_closed_request: {
          title: '客户端提前断开连接',
          description: '客户端在请求完成前断开了连接，可重试。',
        },
        internal_error: {
          title: '服务内部错误',
          description: '请稍后重试；若持续出现请查看服务日志。',
        },
        debug_route_unavailable: {
          title: '离线诊断端点未就绪',
          description: '离线诊断端点尚未就绪，请稍后重试。',
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
        explicit: '指定模型',
        jev: '旧版：Jev 直接选模型',
        blend: '旧版：混合决策',
        default_model: '旧版：默认模型兜底',
        first_eligible: '组内按配置顺序选模型',
      },
      confidenceBand: {
        high: '高',
        medium: '中',
        low: '低',
      },
      usageStatus: {
        observed: '已提取上游用量',
        absent: '未读取到上游用量',
        malformed: '上游用量字段无效',
        oversized: '用量提取超限',
        interrupted: '响应中断，未读取到用量',
      },
      statusClass: {
        success: '成功（2xx）',
        client_error: '客户端错误（4xx）',
        server_error: '服务端错误（5xx）',
      },
      inputMode: {
        content: '原文截取摘要',
        redacted: '脱敏截取摘要',
        features_only: '仅请求结构特征',
      },
      group: {
        simple: '简单任务组',
        medium: '中等任务组',
        complex: '复杂任务组',
      },
      jevStatus: {
        disabled: '未启用',
        skipped_single_model: '仅一个可用任务组，跳过推荐',
        skipped_insufficient_evidence: '证据不足跳过',
        skipped_too_many_models: '旧版：候选模型过多，跳过推荐',
        ok: 'Jev 推荐成功',
      },
      // Accessories to the two prefixed vocabularies. `failure:` reuses `fallbackReason`,
      // `not_eligible:` reuses `notEligible`; only the prefixes need a rendering.
      jevStatusFailure: '失败（{reason}）',
      fallbackReason: {
        none: '未发生兜底',
        not_requested: '未调用 Jev',
        confidence_low: '选组置信度低于阈值',
        selected_model_ineligible: '旧版：推荐模型不可用',
        jev_timeout: 'Jev 超时',
        jev_unavailable: 'Jev 不可达',
        jev_rejected: 'Jev 拒绝请求',
        jev_invalid_result: 'Jev 结果无效',
        jev_canceled: 'Jev 调用取消',
        truncated_evidence: '证据被截断',
        no_candidates: '无候选模型',
      },
      notEligible: '不满足筛选：{code}',
    },
    routingInputDescriptions: {
      content: '发送截取的用户与系统原文、其他消息的数量和字节数，并附每段最后一条 assistant 文本的短截取（256 字节）。系统最多 1 KiB，最新用户文本最多 8 KiB，最多保留 3 条更早用户文本（每条 1 KiB）；整体摘要预算 16 KiB。工具输出只保留统计，工具仅发送名称。',
      redacted: '默认模式。按规则替换 URL、邮箱、疑似密钥和长载荷，每个保留的文本块（含每段最后一条 assistant 文本）取脱敏后的前 512 字节；其他消息和工具输出保留统计。脱敏为规则匹配，不保证匿名化。',
      features_only: '发送协议、长度估算、工具、媒体、流式请求和分析完整性等结构特征，不发送用户或系统原文。不再根据关键词猜测代码或推理倾向；缺少任务内容时，选组信息有限。',
    },
    usageDescriptions: {
      observed: '至少一个合法 token 计数已从上游响应读取。未上报的计数显示为“—”，真实的 0 显示为 0；这些计数属于处理请求的上游模型。',
      absent: '响应结束，但没有读取到可用的用量字段。请检查上游是否上报 usage；Chat Completions 流式请求可能需要开启 stream_options.include_usage。',
      malformed: '上游用量字段无法解析，或计数不是非负整数。请核对上游 usage 格式；缺失计数不会补成 0。',
      oversized: '用量提取超过观测边界，不表示 Jev 或上游上下文超限，也不表示请求失败。先核对运行版本：旧版本可能因单个 SSE 事件超过 16 KiB 或事件数超过 4096 停止统计；当前增量扫描限制的是单个用量值（4 KiB）。',
      interrupted: '响应在读取到用量前中断。请检查客户端断开、上游连接及超时；已经读取到的合法计数会保留。',
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
