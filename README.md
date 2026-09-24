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

数据保存在 Docker 命名卷 `auto-router-data` 中，容器重建或升级不会删除该卷。升级到最新镜像：

```sh
docker pull ghcr.io/nextroad-dev/auto-router:latest
docker stop auto-router
docker rm auto-router
# 再运行上面的启动命令
```

不要删除数据卷；删除 `auto-router-data` 会同时删除数据库及管理配置。默认端口只绑定本机。若需对外提供服务，请先配置 TLS 反向代理，不要直接将管理页面暴露到公网。

## 自动路由

将请求中的 `model` 设为 `auto`，即可请求自动路由。启用 Jev 时，它推荐简单、中等或复杂路由组；本地策略根据请求特征、模型能力和上下文窗口过滤候选，再从所选模型组中决定具体上游。Jev 不可用或未启用时，服务仍按本地路由策略处理请求。

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

管理界面还提供提供商与模型管理、模型组配置、推理密钥管理、请求日志和用量统计。服务状态可通过 `GET /healthz` 与 `GET /readyz` 检查。完整接口定义见 [OpenAPI 文档](docs/openapi.yaml)。
