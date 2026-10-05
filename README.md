# Auto Router

[![CI](https://github.com/nextroad-dev/Auto-Router/actions/workflows/ci.yml/badge.svg)](https://github.com/nextroad-dev/Auto-Router/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[English](README.en.md) | 简体中文

基于 Jev 推荐与本地能力策略的大模型自动路由服务。统一管理上游提供商、模型能力与路由组；客户端请求使用 `auto` 时，服务筛选适配当前请求的候选模型，并选择上游转发。

## 特性

- **统一入口**：兼容 OpenAI Chat Completions / Responses、Anthropic Messages、Gemini generateContent，含流式。
- **自动路由**：`model: "auto"` 由 Jev 推荐任务组（`simple` / `medium` / `complex`），本地策略按能力、上下文窗口过滤，组内按配置顺序选模型。
- **故障转移**：只在同组后续成员间重试，次数与触发条件可配置。
- **管理台**：提供商与模型、模型组、推理密钥、请求日志、用量统计、系统设置。
- **模型目录**：可从 [models.dev](https://models.dev) 同步模型元数据，并支持本地覆盖。
- **单文件部署**：Go 单二进制 + SQLite，管理台内嵌，无需外部依赖；提供 amd64 / arm64 Docker 镜像。

## 快速启动

需要 Docker。运行 GHCR 上的预构建镜像：

```sh
docker run -d \
  --name auto-router \
  --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v auto-router-data:/app/data \
  ghcr.io/nextroad-dev/auto-router:latest
```

也可以使用仓库里的 [`compose.yaml`](compose.yaml)（可参考 [`.env.example`](.env.example)）。

打开 <http://127.0.0.1:8080/admin/>，按页面提示设置管理员密码。之后在管理页面添加提供商和模型、配置模型组，并创建推理密钥。上游 API 密钥与管理员密码都在管理页面配置。

首次设置需要证明你是部署者：尚未设置密码时，服务启动会在日志中打印一次性初始化令牌 `bootstrap_token`。从非本机连接（包括经 Docker 端口映射或反向代理访问）设置密码时，页面会要求填写该令牌；设置成功后令牌立即失效：

```sh
docker logs auto-router 2>&1 | grep bootstrap_token
```

直接在本机运行二进制并通过 loopback 访问时无需令牌。

数据保存在 Docker 命名卷 `auto-router-data` 中（数据库及管理配置），容器重建或升级不会删除该卷，**不要删除它**。升级：

```sh
docker pull ghcr.io/nextroad-dev/auto-router:latest
docker stop auto-router && docker rm auto-router
# 再运行上面的启动命令
```

> **安全提示**：默认端口只绑定本机。若需对外提供服务，请先配置 TLS 反向代理，不要直接将管理页面暴露到公网。

### 从源码构建

需要 Go（版本见 [`go.mod`](go.mod)）。管理台的前端构建产物已提交在 `internal/api/dashboard/static/web/`，因此只构建后端不需要 Node：

```sh
go build -o bin/auto-router ./cmd/auto-router
./bin/auto-router
```

修改前端时需要 Node 22，见 [CONTRIBUTING.md](CONTRIBUTING.md)。

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

管理登录限流按客户端地址分别计数（每地址每分钟 10 次失败），另有全局每分钟 100 次失败的兜底上限。运行时配置（路由、Jev、日志等）在管理台“系统设置”中调整，保存在 SQLite；服务不读取 JSON 配置文件或 `AUTO_ROUTER_*` 运行配置环境变量（Compose 示例中的同名变量只用于 Compose 插值）。

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

推理密钥只在创建或轮换时显示一次。也可将 `model` 设置为 `/v1/models` 返回的具体逻辑模型 ID，直接指定模型而不使用自动路由。

### 支持的接口

| 方法与路径 | 协议 |
| --- | --- |
| `GET /v1/models` | 自动路由模型与可用逻辑模型列表 |
| `POST /v1/chat/completions` | OpenAI Chat Completions |
| `POST /v1/responses` | OpenAI Responses |
| `POST /v1/messages` | Anthropic Messages |
| `POST /v1beta/models/{model}:generateContent` | Gemini generateContent |
| `POST /v1beta/models/{model}:streamGenerateContent` | Gemini 流式生成 |
| `GET /healthz`、`GET /readyz` | 健康与就绪检查 |

Anthropic 和 Gemini 原生接口要求所选上游支持相同协议。OpenAI Chat 文本请求可转换后转发至 Anthropic 或 Gemini，但只支持有限的安全子集；不支持的参数或功能会在发送上游前被拒绝。

完整接口定义见 [OpenAPI 文档](docs/openapi.yaml)。

`GET /v1/models` 始终首先列出虚拟模型 `auto`，默认声明 `context_window: 1000000` 及工具、视觉、推理能力。这些是自动路由入口的声明，不保证每个上游模型都具备；实际请求仍按已配置的模型能力与上下文窗口筛选，没有适合候选时会拒绝请求。

## 自动路由

将请求中的 `model` 设为 `auto`。启用 Jev 时，它只在当前有合格成员的 `simple`、`medium`、`complex` 组中选择任务组；本地策略先按请求特征、模型能力、上下文窗口及硬限制过滤成员，再严格按组内配置顺序选择主模型。不会跨组重试，组内也不做评分排序。

Jev 不可用、未启用或推荐置信度低于 `routing.policy.low_confidence` 时，使用 `routing.auto.default_group`（默认 `medium`）；所选组没有合格成员时明确失败，不自动切换任务等级。

**故障转移**只尝试同组后续成员。`routing.auto.failover.max_attempts` 统计首次请求，范围 1–8，默认 2；请求发送前可确认未送达的失败默认允许重试，超时与 HTTP 状态码默认不重试。启用超时或状态码重试可能导致重复计费或重复执行，状态码仅支持 408、425、429、500、502、503、504。以上设置可在管理台“系统设置 → 全局路由行为”调整。

### Jev 输入模式

Jev 的任务是推荐任务组，模型仍按组内配置顺序选择。Jev 启用且只剩一个有合格成员的任务组时，直接使用该组并跳过推荐调用。`jev.input_mode` 控制哪些信息发往推荐服务：

| 模式 | 管理台名称 | 实际发送内容 |
| --- | --- | --- |
| `redacted`（默认） | 脱敏截取摘要 | 规则替换 URL、邮箱、疑似密钥和长载荷；每个保留文本块取脱敏后的前 512 字节，其他消息保留数量和字节数。规则脱敏不保证匿名化。 |
| `content` | 原文截取摘要 | 系统文本最多 1 KiB；最新用户文本最多 8 KiB；最多 3 条更早用户文本，每条 1 KiB。长文本保留约 2/3 开头和 1/3 结尾。 |
| `features_only` | 仅请求结构特征 | 协议、长度估算、工具与媒体存在性、流式请求和分析完整性等，不发送用户或系统原文。没有任务原文时，推荐信息有限。 |

“摘要”由本地 Go 代码截取、计数和拼接模板得出，不调用模型生成语义总结。工具结果内容不直接发送，工具定义只发送最多 32 个名称，不发送参数 schema；整体文本预算为 16 KiB，超出时删除最旧的用户段。

请求分析器最多读取请求前 4 MiB；解析范围、条目数或嵌套边界触发的不完整分析默认拒绝自动路由。`input_tokens_estimate = ceil(已分析请求 JSON 字节数 / 4)`，只是能力筛选用的粗估值，不是 tokenizer 结果或计费用量。

## 请求日志与排查

管理台的请求日志是结构化路由日志，不包含原始请求/响应正文。排查顺序：

1. 看 `status`、`upstream_status`、`error_code` 和尝试明细，判断客户端请求及上游是否成功。
2. 看 `jev_status`、`confidence`、`fallback_reason` 判断 Jev 推荐是否返回、是否被采用。`jev_status=ok` 与 `selection_mode=first_eligible` 可以同时成立：前者是组选取，后者是组内按配置顺序选模型。
3. 单独查看 `usage_status`。Token 计数来自处理请求的上游模型，不含 Jev 用量；未知字段会省略（界面显示“—”），合法的 0 原样保留。

| 用量状态 | 含义与检查方向 |
| --- | --- |
| `observed` | 至少一个合法非负整数已提取；其余未上报的计数保持为空。 |
| `absent` | 响应结束但未读取到用量；检查上游是否上报 usage，Chat Completions 流式请求是否需要 `stream_options.include_usage`。 |
| `malformed` | 用量字段无法解析或计数类型无效；检查上游 usage 格式。 |
| `oversized` | 用量元数据超过提取边界；与 Jev 提示词上下文限制无关，也不表示转发失败。 |
| `interrupted` | 读取用量前响应中断；检查客户端连接、上游连接及超时。已读到的合法计数会保留。 |

### 诊断工具

`/debug/analyze` 与 `/debug/route` 是可选的诊断端点，服务端仍会执行 loopback/配置限制；它们可能处理敏感请求内容，不通过远程管理页面提供。`/debug/route` 对请求体试运行与 `model: "auto"` 完全相同的线上路由（含真实 Jev 调用，但不调用上游、不写路由日志），返回将要转发的目标、选组过程与特征。

## 文档

- [OpenAPI 接口定义](docs/openapi.yaml)
- [更新日志](CHANGELOG.md)（含日志字段、配置迁移等行为变更）
- [贡献指南](CONTRIBUTING.md)
- [安全策略](SECURITY.md)

## 贡献与许可证

欢迎提交 Issue 和 PR，请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。发现安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。

本项目以 [Apache License 2.0](LICENSE) 发布。内嵌的 IBM Plex 与 Noto Sans SC 字体采用 SIL OFL 1.1，详见 [NOTICE](NOTICE)。
