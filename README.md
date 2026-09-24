# Auto Router

单用户 LLM 网关：管理上游提供商与模型，把推理请求转发到已选择的模型，并为每个推理密钥提供鉴权。

## 启动

```sh
go run ./cmd/auto-router
```

默认监听 `127.0.0.1:8080`，数据库为当前工作目录下的 `data/auto-router.db`。新建数据库使用 schema v8。

如需在本机并行运行另一实例，可用 `-listen 127.0.0.1:8081` 指定监听地址。

### Docker Compose（GHCR）

```sh
cp .env.example .env
docker compose pull
docker compose up -d
docker compose logs -f auto-router
```

打开 <http://127.0.0.1:8080/admin/> 完成首次管理员密码设置。Compose 默认只将端口绑定到本机 loopback；需要从其他机器访问时，应先配置 TLS 反向代理，再调整 `.env` 中的 `AUTO_ROUTER_BIND`。数据保存在 `auto-router-data` 命名卷中；升级时运行 `docker compose pull && docker compose up -d`。不要用 `docker compose down -v`，否则会删除数据库卷。

镜像为 `ghcr.io/nextroad-dev/auto-router`，支持 `linux/amd64` 与 `linux/arm64`。`.env` 中的 `AUTO_ROUTER_VERSION` 可设为 `latest` 或具体版本（例如 `1.2.3`）。首次发布后，仓库维护者需在 GitHub Packages 将该镜像包可见性设为 **Public**，用户才能免登录拉取。

GitHub Actions 会在 PR 上构建验证、在 `main` 发布 `edge`，并在推送 `v*` 标签时发布版本标签和 `latest`：

```sh
git tag v1.2.3
git push origin v1.2.3
```

首次启动时按管理页面提示设置一个 owner 密码。首次设置由 `GET /admin/v1/setup/status` 检查状态，并由 `POST /admin/v1/setup/password` 提交。密码至少 12 字节，可在系统设置中修改。

低于 v8 的旧数据库不会自动迁移至当前版本，也不会被自动删除。需要使用全新数据库时，请先停止服务，并自行备份或移走旧数据库文件，再启动服务。

忘记密码时，停止服务并通过环境变量提供新密码后运行离线恢复命令：

```sh
AUTO_ROUTER_RECOVERY_PASSWORD='your-new-password' go run ./cmd/auto-router -recover-admin
```

PowerShell：

```powershell
$env:AUTO_ROUTER_RECOVERY_PASSWORD = "你的新密码"
go run ./cmd/auto-router -recover-admin
Remove-Item Env:AUTO_ROUTER_RECOVERY_PASSWORD
```

恢复命令要求数据库已存在且为受支持版本，并取得服务监听端口；成功后新密码立即生效。该环境变量只用于本次恢复操作。

## 配置与使用

- **提供商**：在「提供商」中添加上游 URL、API 密钥与类型：`openai`、`openai_compatible`、`anthropic` 或 `gemini`。通过模型发现后选择要加入路由的模型。上游 API 密钥只写入，不会在读取响应中返回。
- **模型组**：在「模型组」中配置简单、中等、复杂三组。每组由已启用的提供商/模型组合组成，顺序就是 fallback 顺序；每组最多 8 项。
- **调用密钥**：在「调用密钥」创建具名 inference key。原始值只在创建或轮换时显示一次。客户端通过 `Authorization: Bearer <key>` 调用。
- **仪表盘**：展示请求成功率、时间窗口内各模型与模型组的实际上游尝试次数和 Token，以及最近 60 秒已完成尝试的输出 Token TPS。

启用 Jev 后，请求内容会按默认的脱敏模式发送至 Jev 服务；选择 features-only 模式时不会发送对话文本。原始 Jev 请求与响应正文默认不捕获。

## API

推理入口：

| 方法与路径 | 协议 |
| --- | --- |
| `GET /v1/models` | 可路由逻辑模型列表 |
| `POST /v1/chat/completions` | OpenAI Chat Completions |
| `POST /v1/responses` | OpenAI Responses |
| `POST /v1/messages` | Anthropic Messages |
| `POST /v1beta/models/{model}:generateContent` | Gemini generateContent |
| `POST /v1beta/models/{model}:streamGenerateContent` | Gemini 流式生成 |

Anthropic 与 Gemini 原生入口按对应协议直通，要求所选上游支持相同协议。OpenAI Chat 文本请求可以转换后转发至 Anthropic 或 Gemini，但仅支持有限的安全子集；不支持的参数或功能会在发送上游前被拒绝。

管理端主要接口：

| 方法与路径 | 用途 |
| --- | --- |
| `GET /admin/v1/setup/status` · `POST /admin/v1/setup/password` | 首次设置 owner 密码 |
| `POST /admin/v1/session` · `GET/DELETE /admin/v1/session` | 密码登录、会话检查与退出 |
| `PATCH /admin/v1/password` | 修改 owner 密码 |
| `GET /admin/v1/providers` · `POST /admin/v1/providers` | 查看与新增提供商 |
| `GET /admin/v1/providers/{key}/discover` | 从上游发现模型 |
| `POST /admin/v1/providers/{key}/models` | 选择该提供商的模型 |
| `GET/PUT /admin/v1/groups` | 读取或整体更新简单/中等/复杂模型组 |
| `GET /admin/v1/dashboard?window=24h` | 仪表盘指标；窗口可选 `1h`、`24h`、`7d`、`30d` |
| `GET /admin/v1/logs/stats?group_by=attempt_model` | 按实际模型汇总上游尝试；用 `attempt_group` 按模型组汇总 |
| `GET/POST /admin/v1/keys` · `POST /admin/v1/keys/{name}/rotate` · `DELETE /admin/v1/keys/{name}` | 管理具名推理密钥 |

管理 API 使用登录后签发的 `HttpOnly` 会话 Cookie；推理密钥只用于推理 API。完整请求、响应与错误结构见 [docs/openapi.yaml](docs/openapi.yaml)。

服务状态探针为 `GET /healthz` 和 `GET /readyz`。管理静态页面由服务自身提供。由于服务默认使用 HTTP 并绑定 loopback，若经反向代理对外提供访问，应由代理终止 TLS。
