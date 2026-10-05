# Auto Router

基于 Jev 推荐与本地能力策略的大模型自动路由服务。统一管理上游提供商、模型能力与路由组；客户端请求使用 `auto` 时，服务筛选适配当前请求的候选模型，并选择上游转发。

## 快速启动（推荐）

需要 Docker。运行以下命令启动 GHCR 上的预构建镜像：

```sh
docker run -d \
  --name auto-router \
  --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v auto-router-data:/app/data \
  ghcr.io/nextroad-dev/auto-router:latest
```

打开 <http://127.0.0.1:8080/admin/>，按页面提示设置管理员密码。之后在管理页面添加提供商和模型、配置模型组，并创建推理密钥。上游 API 密钥与管理员密码在管理页面配置。

首次设置需要证明你是部署者：尚未设置密码时，服务启动会在日志中打印一次性初始化令牌 `bootstrap_token`。从非本机连接（包括经 Docker 端口映射或反向代理访问）设置密码时，页面会要求填写该令牌；设置成功后令牌立即失效。获取方式：

```sh
docker logs auto-router 2>&1 | grep bootstrap_token
```

直接在本机运行二进制并通过 loopback 访问时无需令牌。

数据保存在 Docker 命名卷 `auto-router-data` 中，容器重建或升级不会删除该卷。升级到最新镜像：

```sh
docker pull ghcr.io/nextroad-dev/auto-router:latest
docker stop auto-router
docker rm auto-router
# 再运行上面的启动命令
```

不要删除数据卷；删除 `auto-router-data` 会同时删除数据库及管理配置。默认端口只绑定本机。若需对外提供服务，请先配置 TLS 反向代理，不要直接将管理页面暴露到公网。

### 反向代理与启动参数

以下参数在启动时通过命令行设置（Docker 中直接追加在镜像名之后，镜像入口已包含 `-listen 0.0.0.0:8080`）：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-trusted-proxies` | 空 | 逗号分隔的反向代理 CIDR 或 IP。只有来自这些地址的 `X-Forwarded-For` / `X-Forwarded-Proto` 才会被采信；为空时一律以 TCP 对端地址为客户端地址。多级代理时从右向左跳过可信跳，取第一个不可信地址。`Forwarded`、`X-Forwarded-*`、`X-Real-IP` 永远不会转发给上游提供商。 |
| `-secure-cookies` | `auto` | 管理会话 Cookie 的 `Secure` 属性：`auto` 在 TLS 直连或可信代理声明 `https` 时设置；`always` / `never` 强制。 |
| `-upstream-body-timeout` | `10m` | 非流式上游响应在收到响应头后，两次读取之间允许的最长停顿；`0` 关闭。 |
| `-stream-idle-timeout` | `0` | 流式上游响应两次读取之间允许的最长停顿；默认关闭，避免截断长时间推理。 |
| `-max-buffered-response-bytes` | 32 MiB | 需要整体缓冲以转换协议（Anthropic/Gemini 非流式）的上游响应上限；超出返回 502 `upstream_response_too_large`。 |

例如 Nginx 与服务同机、对外提供 HTTPS：`-trusted-proxies 127.0.0.1`。Docker 网络内的代理请填写其所在网段，例如 `-trusted-proxies 172.16.0.0/12`。

管理登录限流按客户端地址分别计数（每地址每分钟 10 次失败），另有全局每分钟 100 次失败的兜底上限。

## 自动路由

将请求中的 `model` 设为 `auto`，即可请求自动路由。启用 Jev 时，它只在当前有合格成员的 `simple`、`medium`、`complex` 组中选择任务组；本地策略先按请求特征、模型能力、上下文窗口及硬限制过滤成员，再严格按组内配置顺序选择主模型。不会跨组重试，组内也不做评分排序。

Jev 不可用、未启用或推荐置信度低于 `routing.policy.low_confidence` 时，使用 `routing.auto.default_group`（默认 `medium`）；所选组没有合格成员时明确失败，不自动切换任务等级。故障转移只尝试同组后续成员。`routing.auto.failover.max_attempts` 统计首次请求，范围为 1–8，默认 2；请求发送前可确认未送达的失败默认允许重试，超时与 HTTP 状态码默认不重试。启用超时或状态码重试可能导致重复计费或重复执行，状态码仅支持 408、425、429、500、502、503、504。以上设置可在管理台“系统设置 → 全局路由行为”调整。

### Jev 输入与字段口径

Jev 的任务是推荐任务组，模型仍按组内配置顺序选择。Jev 启用且只剩一个有合格成员的任务组时，直接使用该组并跳过推荐调用；未启用时仍使用默认组。`jev.input_mode` 控制哪些信息发往推荐服务：

| 模式 | 管理台名称 | 实际发送内容 |
| --- | --- | --- |
| `redacted`（默认） | 脱敏截取摘要 | 规则替换 URL、邮箱、疑似密钥和长载荷；每个保留文本块取脱敏后的前 512 字节，其他消息保留数量和字节数。规则脱敏不保证匿名化。 |
| `content` | 原文截取摘要 | 系统文本最多 1 KiB；视图中最新用户文本最多 8 KiB；最多 3 条更早用户文本，每条 1 KiB。长文本保留约 2/3 开头和 1/3 结尾。 |
| `features_only` | 仅请求结构特征 | 协议、长度估算、工具与媒体存在性、流式请求和分析完整性等，不发送用户或系统原文。没有任务原文时，推荐信息有限。 |

这里的“摘要”由本地 Go 代码截取、计数和拼接模板得出，不调用模型生成语义总结。文本模式下，每段用户消息之后的 assistant/工具消息按角色统计数量和文本字节数，并附该段最后一条 assistant 文本的截取（`content` 最多 256 字节，`redacted` 为脱敏后前 512 字节）；工具结果内容不直接发送，工具定义只发送最多 32 个名称，不发送参数 schema。整体文本预算是 16 KiB，超出时删除最旧的用户段；该预算不包含完整 JSON 编码和固定推荐指令，也不是 Jev tokenizer 的上下文计量。

分析器最多读取请求前 4 MiB，供摘要使用的文本视图最多保留 256 条消息、共 1 MiB；旧消息可能在摘要前被逐出。解析范围、条目数或嵌套边界触发的不完整分析默认拒绝自动路由，而正常的摘要截取不触发这一拒绝。`input_tokens_estimate = ceil(已分析请求 JSON 字节数 / 4)`，包含 JSON 结构及内联载荷，是能力筛选用的粗估值，不是精确 tokenizer 结果或计费用量。

`has_code`、`reasoning_likely` 及正文关键词识别已移除，不再出现在 Jev 结构特征输入、诊断 API 或 `-analyze-check` 输出中。工具调用、文件检索、图片/文件/音频、输出上限等协议结构识别继续用于能力筛选；客户端的 reasoning 参数仍按原请求转发上游。传输客户端中未生效的 `jev.Config.InputMode` 字段也已移除，输入模式只由实际构建 Jev 请求的路由层管理。

`GET /v1/models` 始终首先列出虚拟模型 `auto`，默认声明：

- `context_window`: `1000000`
- `supports_tools`: `true`
- `supports_vision`: `true`
- `supports_reasoning`: `true`

这些是自动路由入口的能力声明，不保证每个上游模型都具备相同能力或 1M 上下文。实际请求仍按已配置的模型能力与上下文窗口筛选；没有适合候选时会拒绝请求。列表中其余条目是当前有可用上游绑定的逻辑模型，也可直接作为请求的 `model`。

## 发起请求

在管理页面创建推理密钥后，将密钥作为 Bearer Token 使用：

```sh
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Authorization: Bearer YOUR_INFERENCE_KEY' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

推理密钥只在创建或轮换时显示一次。请求也可将 `model` 设置为 `/v1/models` 返回的具体逻辑模型 ID，以直接指定模型而不使用自动路由。

## 支持的接口

| 方法与路径 | 协议 |
| --- | --- |
| `GET /v1/models` | 自动路由模型与可用逻辑模型列表 |
| `POST /v1/chat/completions` | OpenAI Chat Completions |
| `POST /v1/responses` | OpenAI Responses |
| `POST /v1/messages` | Anthropic Messages |
| `POST /v1beta/models/{model}:generateContent` | Gemini generateContent |
| `POST /v1beta/models/{model}:streamGenerateContent` | Gemini 流式生成 |

Anthropic 和 Gemini 原生接口要求所选上游支持相同协议。OpenAI Chat 文本请求可转换后转发至 Anthropic 或 Gemini，但只支持有限的安全子集；不支持的参数或功能会在发送上游前被拒绝。

管理界面提供提供商与模型管理、模型组配置、推理密钥管理、请求日志和用量统计。系统设置页可配置全局路由行为（默认组、故障转移）、Jev 选组置信度阈值、请求分析完整性策略、持久日志和 Jev 输入模式；每个区域只提交对应的设置 patch。会话 TTL 与 models.dev 同步范围可通过设置 API 调整。旧版的路由偏好（`routing.default_preference` 与 `X-Routing-Preference` 请求头）、高置信度阈值、默认兜底模型和成本/延迟等级已移除：它们不影响分组路由。数据库中保存的旧值会在启动时被忽略，仍发送的 `X-Routing-Preference` 请求头会被忽略并在转发前剥离。

请求日志中的用量状态 `oversized` 表示用量元数据超过提取边界，与 Jev 的提示词上下文限制无关，也不表示请求转发失败。OpenAI、Anthropic 和 Gemini 响应均支持增量提取用量，长响应或大流式事件不会仅因正文体积导致用量观测停止；增量扫描时单个用量值仍有 4 KiB 上限。流式事件中分开上报的输入、输出 token 数会合并保留，未上报的计数保持为空。

### 请求日志排查顺序

1. 看 `status`、`upstream_status`、`error_code` 和尝试明细，判断客户端请求及上游是否成功。
2. 看 `jev_status`、`confidence`、`fallback_reason` 判断 Jev 推荐是否返回、是否被采用。`jev_status=ok` 与 `selection_mode=first_eligible` 可以同时成立：前者是组选取，后者是组内按配置顺序选模型。
3. 单独查看 `usage_status`。Token 计数来自处理请求的上游模型，不包含 Jev 用量；未知字段会省略，界面的“—”及旧响应的 `null` 代表未知，合法的 0 会原样保留。`bytes_written` 是响应字节数，不是客户端输入或 Jev 提示词大小。管理台的 JSON 是结构化路由日志，不是原始请求/响应正文。

| 用量状态 | 含义与检查方向 |
| --- | --- |
| `observed` | 至少一个合法非负整数已提取；其余未上报的计数保持为空。 |
| `absent` | 响应结束但未读取到用量；检查上游是否上报 usage，Chat Completions 流式请求是否需要 `stream_options.include_usage`。 |
| `malformed` | 用量字段无法解析或计数类型无效；检查上游 usage 格式。 |
| `oversized` | 用量提取触及边界；先核对运行版本，再检查用量值大小。旧版本会因未完成的 SSE 事件超过 16 KiB 或事件数超过 4096 停止统计，9 月 30 日提交 `e16c21d` 已改为增量扫描。 |
| `interrupted` | 读取用量前响应中断；检查客户端连接、上游连接及超时。已读到的合法计数会保留。 |

部署更新后应核对运行镜像摘要或启动日志中的版本，管理台与服务端应来自同一构建。历史日志中的旧状态和空计数不会自动回填；旧版模型选择枚举保留用于读取历史记录，界面会标注“旧版”。

日志 API、服务日志与 `-logs-check` 统一省略未知或不适用的可选字段：未启用 IP 记录时无 `client_ip`，成功请求无空错误字段，未调用 Jev 时无 `jev_latency_ms`，未产生有效推荐时无 `confidence`。实际推荐置信度为 0 或调用耗时不足 1 ms 时仍输出 0。历史记录中失败/跳过 Jev 的占位置信度 0 只在读取时隐藏，不改写数据库；有效推荐优先展示历史追踪中的原始置信度。

`confidence_band` 不再生成或作为主日志字段输出：它不是当前选模型的依据。`evidence_hash` 移至 `diagnostics.evidence_hash`，用于核对决策元数据，不是提示词哈希，也不参与路由；有实际历史分档时保留为 `diagnostics.legacy_confidence_band` / `legacy_trace_confidence_band`。`confidence_band` 查询参数仅兼容筛选旧记录，新记录不再填充该列。管理台将诊断信息与 JSON 默认折叠，并直接展示上游逐次尝试、故障转移及脱敏失败摘录。

当前组路由的日志 API 与 `-logs-check` 不再输出空的旧版 `candidate_models`、`model_count`、`selected_model` 和 `probabilities`；旧记录有实际数据时仍可读取，CLI JSON 的旧 `selected` 改为与 API 一致的 `selected_model`，旧模型分布同时保留模型标识和概率。Jev 追踪不再重复主记录已有的状态、耗时、置信度、兜底原因和指纹；`group_count` 可由 `candidate_groups.length` 得出，不再输出。当前推荐使用 `candidate_groups`、`recommended_group`、`selected_group` 与 `group_probabilities`。这些是日志输出契约变更，直接解析上述字段的脚本需要同步调整；没有删除历史数据库字段，也没有回填未知用量。

模型管理页支持永久删除任意来源的逻辑模型：确认后会一并删除其全部提供商绑定、移除模型分组引用并刷新运行时目录，提供商和历史路由日志保持不变。删除排除记录会持久保存在数据库中，配置导入和 models.dev 同步不会自动恢复该模型；显式重新创建模型或在提供商页面重新绑定可以恢复模型，其他已删除绑定仍需逐个显式恢复。如果返回 `snapshot_publish_failed`，删除已经提交，但运行时刷新失败，需要重启以加载数据库中的最新目录。

模型管理页可查看 models.dev 最近同步结果并显式触发全局同步；仪表盘分开显示服务健康/依赖状态与路由、Jev 窗口汇总。设置页的“重置所有运行时覆盖”会恢复代码默认值并清除运行时保存的 Jev 密钥，但不会删除 Provider、模型、绑定或入站凭据。

`/debug/analyze` 与 `/debug/route` 是可选的诊断端点，服务端仍会执行 loopback/配置限制；它们可能处理敏感请求内容，不通过远程管理页面提供交互。`/debug/route` 对请求体试运行与 `model: "auto"` 完全相同的线上路由（含真实 Jev 调用，但不调用上游、不写路由日志），返回将要转发的目标、选组过程与特征。

**旧配置迁移：** 服务启动不再读取旧 JSON 配置文件或 `AUTO_ROUTER_*` 运行配置环境变量；运行配置以代码默认值启动，再应用 SQLite 中保存的运行时覆盖。旧值不会自动迁移，请在管理台的“系统设置”中重新录入需要保留的运行时设置。传入旧版 `-config` 只会显示忽略提示；旧环境变量存在时会显示通用迁移提示，不会记录或回显变量名和值。`-sync-models` 仅显示迁移提示并立即退出，不会执行同步；请使用管理台的“模型管理 → 立即同步 models.dev”。Compose 示例中的 `AUTO_ROUTER_VERSION`、`AUTO_ROUTER_BIND` 与 `AUTO_ROUTER_PORT` 是 Compose 文件插值项，不会作为服务运行配置传入容器。

服务状态也可通过 `GET /healthz` 与 `GET /readyz` 检查。完整接口定义见 [OpenAPI 文档](docs/openapi.yaml)。

## 发布前验证

CI 运行 Go vet/test/race/build 与 WebUI 的 OpenAPI 类型生成一致性检查、Vitest、Playwright E2E、类型检查、生产构建和嵌入资源一致性检查。发布前仍建议本地复跑 E2E，并检查失败 trace：

```sh
cd webui
npm ci
npm run test:e2e
```

默认发布端口保持 loopback 绑定；公开访问应通过 TLS 反向代理。
