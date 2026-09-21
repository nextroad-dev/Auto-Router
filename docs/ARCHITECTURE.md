# 架构与 V1 边界

本项目按阶段实现用户提供的 Auto Router V1 需求。优先级为：**正确性 > 可维护性 > 兼容性 > 性能 > 功能数量**。

## 当前范围

阶段 1 交付可运行、可测试的基础服务：Go 入口、运行配置、SQLite 连接与事务迁移、存活/就绪检查和优雅退出。阶段 2 交付模型注册表：models.dev 元数据同步、本地覆盖、持久化与只读快照。`/v1/chat/completions`、`/v1/responses`、路由、Provider 调用和管理后台尚未实现，不提供假成功的占位接口。

阶段与验收门槛见 [ROADMAP.md](ROADMAP.md)。后续代码随对应阶段加入，不预先堆放空实现。

## 目标数据流

```text
Client
  │
  ▼
API ── requested_model != auto ────────────────────────┐
  │                                                    │
  └─ requested_model == auto                          │
       │                                               │
       ▼                                               │
    Analyzer → Jev → Policy → RouteDecision             │
                                 │                     │
                                 ▼                     ▼
                               Proxy → Provider adapter
                                           │
                                           ▼
                                      Bifrost Gateway
                                           │
                                           ▼
                                      Provider / Model
```

指定模型时跳过 Analyzer、Jev 和 Policy，直接交给 Provider 层。自动路由只替换请求中的目标 `model`，不重写 prompt、工具定义或协议字段。

## 模块职责与依赖方向

| 模块 | 职责 | 不负责 |
| --- | --- | --- |
| `cmd/auto-router` | 配置、依赖装配、进程生命周期、`-sync-models` 维护模式 | 路由规则、SQL、协议转换 |
| `internal/api` | HTTP 边界、健康检查；后续请求/管理 API | 模型打分、Provider 协议实现 |
| `internal/config` | 启动配置、默认值、环境覆盖、`registry` 段与 `${VAR}` 展开、校验 | HTTP 请求、数据库访问 |
| `internal/storage` | SQLite 连接、事务迁移、注册表导入/禁用/加载 | Jev 调用、路由决策 |
| `internal/models`（阶段 2） | 注册表领域类型、校验、确定性排序契约、只读快照与代次 | SQL、HTTP、models.dev 字段 |
| `internal/modelsdev`（阶段 2） | models.dev 抓取与白名单映射 | SQL、路由决策 |
| `internal/proxy`（阶段 3） | 请求转发、取消、流式原样返回 | OpenAI/Anthropic/Gemini 转换 |
| `internal/providers`（阶段 3） | 可替换的 Provider 层契约 | 路由算法 |
| `internal/providers/bifrost`（阶段 3） | Bifrost Gateway 适配 | 读写 Bifrost 内部数据库 |
| `internal/router/jev`（阶段 4） | TypeSafe API 适配、结果校验 | 本地策略、数据库访问 |
| `internal/router/analyzer`（阶段 5） | 规则式特征提取 | 调用 Agent、协议转换 |
| `internal/router/policy`（阶段 6） | 纯策略函数、硬性过滤、优先级和 fallback | HTTP、SQL、Jev SDK |
| `internal/router/decision`（阶段 6） | 稳定的决策数据类型 | Provider 实现细节 |
| `internal/router`（阶段 7） | 编排 Analyzer、模型快照、Jev 和 Policy | 协议转换、SQL、直接模型调用 |
| `internal/logging`（阶段 8） | 路由事件与可选调试信息 | 改写上游响应 |

接口定义在使用方或最小契约包中，按实际用例引入。Analyzer、Jev、Policy 之间只传递普通 Go 数据结构。Policy 不依赖 Bifrost；Bifrost Gateway 改为 Bifrost Core 或其他实现时，只修改 Provider 装配和适配层。

最终决策契约：

```go
type RouteDecision struct {
    Provider   string
    Model      string
    Confidence float64
    Reason     string
}
```

## Model Registry 与 Provider

注册表以 **Provider × 逻辑模型（Pair）** 为单位保存能力，不把不同 Provider 的能力合并后猜测。

- `providers`：`key`、`display_name`、`base_url`、`api_key`、`enabled`、`priority`、`source`。
- `models`：逻辑模型 `id`、`display_name`、`enabled`、`priority`、`source`。
- `provider_models`：`(provider, model)` 主键、`upstream_model_id`、`context_window`、`max_output`、tools/vision/reasoning 能力、`enabled`、`priority`、`source`。
- `registry_sync_state`：最近一次 models.dev 同步的 URL、时间、白名单指纹、导入/跳过条数与告警。

来源只有两种：`modelsdev`（同步）与 `local`（配置文件）。

- **models.dev 只提供元数据与能力**，不提供 `base_url` 或凭据；同步**只写** `display_name`、能力、`upstream_model_id` 与容量。
- 新建的同步 Provider 默认 `enabled=0` 且无 `base_url`/`api_key`，因此在本地配置之前不可能被转发。
- 本地配置对 `base_url`、`api_key`、`enabled`、`priority`、`upstream_model_id` 具有权威性：同步永不改写 `source='local'` 的行；本地条目从配置中移除时置 `enabled=0` 而不删除，避免残留凭据被继续使用。
- 同步时从白名单移除的 pair 同样只置 `enabled=0`，永不删除；删除只由后续 Admin API 执行。
- 白名单是精确到模型的显式运维意图：条目以**第一个 `/`** 分隔为 `provider/model`（允许模型名内含 `/`），任何条目在源数据中找不到都会让**整次同步失败并回滚**，不会“部分成功”。上下文窗口不可用的上游记录会跳过并记入告警。
- **不存价格**：`cost`、`open_weights`、`knowledge`、`release_date`、`temperature` 等字段同步时丢弃，表结构里没有价格/成本列。

排序契约（阶段 6 直接复用，保证确定性）：`pair.priority` 升序 → `provider.priority` 升序 → `provider.key` 升序 → `model.id` 升序。数值小者优先。

路由只读 `internal/models.Catalog` 快照：`Provider(key)`、`Lookup(modelID)`、`PairsForModel(modelID)`。`PairsForModel` 只返回 **pair、Provider、逻辑模型三者都启用** 的项。快照由 `internal/models.Store` 原子发布并递增 `Generation`，在途请求继续使用旧快照。

同步的触发方式只有 `-sync-models` 一个显式进程模式：启动与请求路径**不做网络访问**、不做后台同步；目录为空时启动只记 `WARN` 并继续提供服务，`/readyz` 不依赖注册表内容。

## Analyzer、Jev 与 Policy

Analyzer 提取原始用户请求、system prompt 是否存在、tools、图片/多模态、输入长度、代码/复杂推理/长上下文/低延迟特征、客户端协议与路由偏好。V1 使用规则，不引入 Agent 或自动改写 prompt。

Jev 接入需求指定的 TypeSafe `POST /v1/systemone`。在阶段 4 实现前核验官方请求/响应 schema、鉴权和固定模型版本的实际方式；**不得凭文档中的概念字段臆造 wire format**。候选列表及其能力随请求输入，结果归一化为模型、候选概率与 confidence。不默认使用 `latest`。

Policy 保留最终决定权：

1. 先过滤禁用/不可用 Provider、黑名单、白名单限制、上下文不足、缺少 tools/vision 等硬性不兼容候选。
2. 再综合 Jev、用户偏好、能力与明确的排序/平局规则（阶段 2 的优先级契约为其基础）。
3. 高 confidence 使用合格的 Jev 推荐；中 confidence 与静态规则综合；低 confidence、Jev 超时或无效输出按配置 fallback。
4. 高低阈值、默认模型、fallback 列表和路由偏好均配置化，校验 `0 <= low < high <= 1`。
5. fallback 也必须满足硬性约束；没有合格候选时明确失败，不强行选择默认模型。
6. Provider 不可用时支持有界 fallback。已经向客户端发送响应头或流数据后，不跨 Provider 重试，也不把新响应拼接到已开始的流上。

## 协议与流式边界

- V1 面向 `POST /v1/chat/completions` 和 `POST /v1/responses`。
- 协议转换、Provider SDK、SSE 事件格式、tool-call 转换、reasoning 字段和上游基础重试由 Bifrost 负责。
- Auto Router 保留未知请求字段；不通过不完整的请求 DTO 反序列化后重建整个协议消息。
- 流式代理必须及时 flush、传播客户端取消，不缓冲整段响应，也不将 Bifrost SSE 转换为另一种 SSE。
- HTTP 服务不设置覆盖整个流的全局 `WriteTimeout` 或 `ReadTimeout`。请求头有超时；请求体限额、读取期限、Jev 超时和上游连接超时分别在相关阶段实现。
- 代理仅做 HTTP 必需的 hop-by-hop 头清理与受控鉴权，不把客户端凭据当成上游 Provider Key 泄露。
- 对已发送的流、已提交的请求和可能产生工具副作用的请求，重试边界必须显式测试。

## 存储、安全与可观测性

- V1 使用单进程 SQLite；纯 Go 驱动，支持 `CGO_ENABLED=0`。
- 开启 WAL、foreign keys 与有界 busy timeout；初始连接池为单连接，优先正确性。
- 迁移按版本递增、事务执行，记录名称与校验和，拒绝未知的较新版本或已修改的历史迁移。阶段 1 仅建迁移记录表，阶段 2 追加迁移 v1 建注册表 4 张 `STRICT` 表；业务表随对应阶段追加，历史迁移永不改写。
- 默认仅监听 `127.0.0.1`。阶段 1 没有身份认证，只能用于本机开发或受信任网络；管理 API 加入前必须完成鉴权。不得将未鉴权服务直接暴露到公网。
- 配置文件和环境变量中的凭据不得进入日志。后续 Provider/Jev 密钥不回传到管理 API 明文响应。
- 路由日志覆盖 request ID、时间、协议、原始/最终模型、Provider、Jev confidence、原因、latency、status 和 usage；V1 不计算 cost。
- Jev 原始输入/输出与决策轨迹默认关闭，显式开启后仍脱敏。不得把 prompt、API Key 或请求体写进普通运行日志。
- usage 缺失时保留为未知，不伪装成 0；观察流式 usage 不得改变转发字节。

## 凭据与残余风险

Provider 的 `api_key` **明文存储在 SQLite 中**（无静态加密），这是已接受的设计选择，不是被假装保护好的资产。已经实现的缓解：

- POSIX 上数据库与 `-wal`/`-shm` 收紧为 `0600`，新建数据库目录为 `0700`；
- `models.Provider` 实现 `slog.LogValuer`，`api_key` 一律输出掩码，错误信息与日志不包含完整密钥；
- `api_key` 支持 `${VAR}`，可避免把明文写进受版本控制的配置文件；变量未设置直接报错，不静默使用空密钥；
- `.gitignore` 覆盖 `*.db*`。

仍未解决（明确记为残余风险）：磁盘/备份泄漏、Windows ACL 未加固、无静态加密、无密钥轮换。静态加密与 Windows ACL 加固推迟到后续阶段。

## 非目标

V1 不实现自研协议转换器/LLM SDK、Agent 工作流、多级 Agent 路由、prompt 改写、复杂负载均衡、自动 benchmark/训练、分布式部署或多租户计费。后台先采用简单页面和 Admin API，不引入独立复杂前端架构。
