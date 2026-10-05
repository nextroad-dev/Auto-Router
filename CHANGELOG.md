# Changelog

本文件记录对使用者可见的行为变更，尤其是日志字段与配置契约。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [Unreleased]

### 新增

- 模型管理页支持永久删除任意来源的逻辑模型：一并删除其全部提供商绑定、移除模型分组引用并刷新运行时目录；提供商和历史路由日志保持不变。删除排除记录持久保存，配置导入和 models.dev 同步不会自动恢复；显式重新创建模型或在提供商页面重新绑定可恢复，其他已删除绑定仍需逐个显式恢复。返回 `snapshot_publish_failed` 时删除已提交但运行时刷新失败，需重启以加载最新目录。
- 模型管理页可查看 models.dev 最近同步结果并显式触发全局同步；仪表盘分开显示服务健康/依赖状态与路由、Jev 窗口汇总。

### 变更

- 模型密集页面（模型管理、提供商模型抽屉、模型分组、仪表盘模型用量）在大量模型和长标识下保持可用宽度，横向滚动限定在表格内部。
- 请求日志用量提取改为增量扫描：长响应或大流式事件不再因正文体积导致用量观测停止（单个用量值仍有 4 KiB 上限）。流式事件中分开上报的输入、输出 token 数会合并保留。旧版本曾因未完成的 SSE 事件超过 16 KiB 或事件数超过 4096 而停止统计，升级后需核对运行版本。历史日志中的旧状态和空计数不会回填。
- 日志 API、服务日志与 `-logs-check` 统一省略未知或不适用的可选字段：未启用 IP 记录时无 `client_ip`，成功请求无空错误字段，未调用 Jev 时无 `jev_latency_ms`，未产生有效推荐时无 `confidence`。实际置信度为 0 或耗时不足 1 ms 时仍输出 0。历史记录中失败/跳过 Jev 的占位置信度 0 仅在读取时隐藏，不改写数据库。
- `evidence_hash` 移至 `diagnostics.evidence_hash`（不是提示词哈希，不参与路由）。
- 当前组路由的日志 API 与 `-logs-check` 不再输出空的 `candidate_models`、`model_count`、`selected_model`、`probabilities`；旧记录有数据时仍可读取。CLI JSON 的 `selected` 改名为 `selected_model`，与 API 一致。Jev 追踪不再重复主记录已有的状态、耗时、置信度、兜底原因和指纹；`group_count` 不再输出，可由 `candidate_groups.length` 得出。推荐使用 `candidate_groups`、`recommended_group`、`selected_group`、`group_probabilities`。**直接解析这些字段的脚本需要同步调整。**没有删除历史数据库字段。

### 移除

- `confidence_band` 不再生成，也不再作为主日志字段输出；`confidence_band` 查询参数仅用于兼容筛选旧记录。有实际历史分档时保留为 `diagnostics.legacy_confidence_band` / `legacy_trace_confidence_band`。
- `has_code`、`reasoning_likely` 及正文关键词识别已移除，不再出现在 Jev 结构特征输入、诊断 API 或 `-analyze-check` 输出中。工具调用、文件检索、图片/文件/音频、输出上限等协议结构识别继续用于能力筛选；客户端的 reasoning 参数仍按原请求转发上游。
- 传输客户端中未生效的 `jev.Config.InputMode` 字段已移除，输入模式只由路由层管理。
- 旧版路由偏好（`routing.default_preference` 与 `X-Routing-Preference` 请求头）、高置信度阈值、默认兜底模型和成本/延迟等级已移除，它们不影响分组路由。数据库中保存的旧值启动时被忽略；仍发送的 `X-Routing-Preference` 请求头会被忽略并在转发前剥离。

### 迁移说明

- 服务启动不再读取旧 JSON 配置文件或 `AUTO_ROUTER_*` 运行配置环境变量；运行配置以代码默认值启动，再应用 SQLite 中保存的运行时覆盖。旧值不会自动迁移，请在管理台“系统设置”重新录入。传入旧版 `-config` 只显示忽略提示；检测到旧环境变量时显示通用迁移提示，不记录或回显变量名和值。
- `-sync-models` 仅显示迁移提示并立即退出，请使用管理台“模型管理 → 立即同步 models.dev”。
- 设置页“重置所有运行时覆盖”会恢复代码默认值并清除运行时保存的 Jev 密钥，但不会删除 Provider、模型、绑定或入站凭据。
