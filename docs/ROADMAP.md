# V1 分阶段交付计划

每阶段先实现、再独立测试。只有该阶段测试通过，才能进入下一阶段；不能以空实现、mock 成功或 UI 占位宣称 V1 已完成。

当前状态：**阶段 1 实现中**。其余阶段均未开始。

## 阶段与测试门槛

| 阶段 | 范围 | 独立验收门槛 |
| --- | --- | --- |
| 1. 基础架构 | Go 模块、配置、SQLite 连接/事务迁移、健康检查、生命周期、CI | 配置错误立即失败；SQLite 可持久化重开；迁移幂等/失败回滚；存活与就绪语义不同；退出有界；`go test ./...`、`go vet ./...`、构建与进程级 smoke 通过 |
| 2. Model Registry | 本地目录导入、模型能力/价格/启停/优先级、Provider 配置和一对多映射、存储接口 | 同模型多 Provider；重复/悬空映射拒绝；价格与容量校验；重启后保留；新增模型仅改配置；密钥不泄露 |
| 3. Bifrost Proxy | 可替换 Provider 接口、Gateway 适配、两条 API 的指定模型直通、SSE 与取消 | 用本地 fake gateway 测试路径/头/查询参数/未知字段透传；tools/reasoning/usage 原样保留；分块及时到达；取消传播；hop-by-hop 头处理；错误状态透传；另提供真实 Bifrost 的可选集成测试 |
| 4. Jev Client | 核验官方 API、鉴权、固定版本、候选能力输入、model/probabilities/confidence 归一化 | 固定 wire fixtures；请求超时/取消；4xx/5xx；缺失或非法概率；候选外模型；禁止无意使用 `latest`；无真实凭据时明确仅验证 mock，不能声称已打通 TypeSafe |
| 5. Analyzer | Chat Completions 与 Responses 输入提取、规则特征、协议与用户偏好 | 文本/system/tools/图片/多模态/代码/推理/长上下文/快速响应样例；未知字段兼容；空输入/畸形输入/长度边界；不调用 Agent、不改写原请求 |
| 6. Policy Engine | 硬性能力过滤、可配置 confidence 分层、成本/优先级、用户规则、解释与 fallback | 纯函数表驱动测试；高/中/低 confidence 边界；Provider 禁用/不可用；黑白名单；tools/vision/context 限制；无合格候选；确定性平局；fallback 不能绕过硬性约束 |
| 7. Auto Routing | `model=auto` 编排、只替换目标模型、指定模型绕过、有界 fallback | 两个 API 的端到端 fake Jev + fake gateway 测试；显式模型不调用 Jev/Policy；推荐能被 Policy 覆盖；Provider 不可用 fallback；流开始后不重试；请求取消与错误分类 |
| 8. Logging | SQLite 路由日志、usage/cost、请求关联、可选 Jev 调试轨迹、脱敏 | 成功/失败/取消/流式均有日志；字段完整；缺失 usage 不虚构；金额计算可复现；日志失败不污染已发送响应；默认无 prompt/密钥；明确数据保留策略 |
| 9. Admin API | Models/Providers/Logs/Settings API、鉴权、校验、密钥遮蔽、配置生效方式 | 授权检查；CRUD/分页/筛选；非法配置拒绝；密钥更新但不明文返回；并发读取看到一致快照；配置更新影响后续请求且不破坏在途请求 |
| 10. Dashboard | 简单后台：Dashboard、Models、Providers、Logs、Settings | 请求/模型/Provider 分布、Auto Router 命中率、平均 latency、tokens、成本与数据库一致；可查原因；空态/错误态；表单校验；键盘可用；不暴露密钥 |

## V1 总验收对应关系

| 需求验收项 | 证明阶段 |
| --- | --- |
| 1. OpenAI 兼容客户端可连接 | 3、7，并用真实客户端与 Bifrost 复核 |
| 2. `model=auto` 触发路由 | 7 |
| 3. 指定模型绕过路由 | 3、7 |
| 4. Jev 按候选模型返回结果 | 4，另需真实 TypeSafe 验证 |
| 5. Policy 能覆盖 Jev | 6、7 |
| 6. Provider 不可用时 fallback | 6、7 |
| 7. Streaming 正常 | 3、7，真实流式集成复核 |
| 8. Tool Calling 透传 | 3、7 |
| 9. 完整路由日志 | 8、9 |
| 10. 解释模型选择原因 | 6、8、10 |
| 11. 新增模型不改 Router 核心 | 2、7、9 |
| 12. 更换 Bifrost 不重写 Jev/Policy | 3、6、7，用第二个 fake Provider adapter 验证依赖隔离 |

## 测试层次

1. **单元测试**：无外网、无真实凭据，覆盖配置、规则和错误路径。
2. **本地集成测试**：临时 SQLite、`httptest` 上游、真实 HTTP 分块读取与取消。
3. **进程 smoke**：构建二进制，用临时目录启动；验证存活、就绪、未知路径和正常退出。
4. **可选外部集成**：显式提供 Bifrost/TypeSafe 配置后运行；测试结果区分真实调用与模拟调用，默认 CI 不花费模型调用费用。

新增阶段必须保留此前测试通过；协议层不得因为添加路由或日志而失去 streaming、tool calling 或未知字段的兼容性。
