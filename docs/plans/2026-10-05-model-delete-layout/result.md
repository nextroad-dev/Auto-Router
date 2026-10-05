# 逻辑模型删除与模型密集页面宽度修复: Implementation Result

## Completion Status

Completed。用户于 2026-10-05 回复“实施”批准计划；代码、OpenAPI、生成类型、回归测试和内嵌前端资产已更新，计划范围内验证通过。未部署、未操作用户数据库。用户随后明确要求“提交”，授权把本次改动提交到本地 Git；不推送。

## Actual Changes

### 逻辑模型永久删除

- `internal/storage/adminmodel_delete.go`：新增 DeleteModel 和模型级排除读写。事务内检查存在、记录模型和现有绑定排除、移除所有模型分组引用并连续重排、删除全部提供商绑定、删除模型；失败回滚。提供商及历史日志保持不变。
- `internal/storage/migrations.go`：追加迁移 14 `persist_deleted_logical_model_exclusions`，创建 deleted_models 表；迁移 1–13 未修改。
- `internal/storage/adminpair_delete.go`：小幅提取共享的分组过滤/重排辅助函数，保留原绑定解除语义。
- `internal/storage/registry.go`：本地和 models.dev 导入均跳过已删除模型及其绑定，包括新出现提供商的绑定，正确计算排除/同步跳过数量。
- `internal/storage/adminregistry.go`、`internal/storage/provider_models.go`：显式创建模型/选模在成功事务中清除模型排除；选模只清除所选提供商的绑定排除，其他已删除绑定不自动恢复。
- `internal/api/admin.go`、`internal/api/admin_registry.go`：新增 DELETE `/admin/v1/models/{id...}`，支持含斜杠 ID。成功返回 `{id, deleted: true}`；未知模型返回 404 unknown_model；提交后刷新分组和目录，发布失败报告 snapshot_publish_failed，不假称数据库已回滚。
- `docs/openapi.yaml`、`webui/src/lib/generated-api.ts`、`README.md`：更新删除、恢复及运行时发布失败语义，移除旧文档“模型没有 DELETE”的限制。
- `webui/src/pages/Catalogue/Models.vue`：所有来源逻辑模型均有删除按钮；确认显示标识、绑定数、分组清理/日志保留/同步不恢复。支持取消、忙碌防重、协调自动保存和启停、成功刷新两张表，以及已提交发布失败时重新读取目录并保留错误。
- `internal/storage/adminmodel_delete_test.go`、`internal/api/admin_model_delete_test.go`：新增事务、分组顺序、绑定/日志保留、持久排除、恢复、迁移、API/鉴权及发布失败测试。

### 页面宽度

- `webui/src/style.css`：jf-stack 明确 `minmax(0, 1fr)` 列和可缩小边界；内容内层 min-width: 0；局部滚动容器有最大宽度。
- `webui/src/components/JfCard.vue`：操作区允许缩小并受最大宽度约束。
- `webui/src/components/JfTable.vue`：表格包装层局部横向滚动；普通单元格允许长文本换行，不取消明确 nowrap 的操作/状态列。
- `webui/src/pages/Catalogue/Models.vue`：模型/提供商标识有界换行并附完整提示。
- `webui/src/pages/Catalogue/Providers.vue`：提供商名称/标识有界换行；绑定与发现模型抽屉长 ID/标签可换行，窄屏列表行纵向排布以保留操作。
- `webui/src/pages/Catalogue/ModelGroups.vue`：候选与排序容器可缩小，排序行可换行，省略标识保留完整提示。
- `webui/src/pages/Dashboard/Index.vue`：模型排行表名称有界换行，图表省略名称附完整提示。
- `webui/src/components/JfDialog.vue`、`JfDrawer.vue`：标题容器各增加 overflow-wrap: anywhere，防止长模型确认文案/提供商标题溢出。没有改变组件 API。
- `webui/e2e/management.spec.ts`：增加模型删除 mock、取消/全来源删除/忙碌/失败/部分发布失败回归；100 个长标识模型的四视口几何与操作测试。
- `internal/api/dashboard/static/web/**`：通过 Vite 正式构建更新哈希资源和入口。未手工编辑生成文件。

## Implementation Record

- 按批准计划完成后端删除、同步排除及显式恢复，再完成前端交互和共享布局边界修复。
- 首轮存储测试及紧接的后端回归均失败于新增测试夹具：启用提供商缺少必填 base_url。文件型数据库在初始失败路径未及时关闭，Windows 临时目录清理同时报占用错误。
- 修正测试夹具（补测试端点，使用 t.Cleanup 关闭每次打开的连接），没有放宽生产校验或削弱断言。修正后存储/API/命令入口和全量 Go 测试通过。
- Playwright 新增定向回归首次运行全部通过，随后完整浏览器回归全部通过。
- 构建后运行内嵌资源完整性检查，并重跑 internal/api 测试，确保 Go 内嵌资产仍可用。
- 最终审阅源码和生成类型差异，确认只有追加迁移、目标功能/布局与正式构建资源变更；gofmt 和 diff 空白检查通过。

## Delegation

None。主代理直接实施；后台任务仅运行本地 Go/npm/Playwright 命令，未调用子代理或 Fusion。

## Verification Commands and Results

| 命令 | 实际结果 |
| --- | --- |
| `go test ./internal/storage -run "Test(DeleteModel\|DeletedModel\|CreateModelExplicitly\|ModelExclusion\|DeletePair\|DeletedPair)" -count=1` | 初次失败：测试夹具缺 base_url；已修正，后续完整存储回归覆盖通过。任务 b416d68a6。 |
| `go test ./internal/storage ./internal/api ./cmd/auto-router` | 首次存储夹具失败，API 与命令入口通过（b75939221）；修正后重跑全部通过，storage 5.481s，其余缓存通过（bf5f21585）。 |
| `go test ./...` | 所有有测试的 Go 包通过，无测试包正常列出（b0e760472）。 |
| `cd webui && npm run api:types` | 成功生成 deleteAdminModel 类型（b54c32c5c）。 |
| `cd webui && npm test && npm run typecheck` | 8 个文件、58 项 Vitest 单测通过；vue-tsc 无错误（b91367123）。 |
| `cd webui && npm run test:e2e -- --grep "logical model\|many long model"` | Chromium 8 项定向测试通过，31.9s（b0e9a4f12）。 |
| `cd webui && npm run test:e2e` | Chromium 38 项完整测试通过，57.0s（b05a66bbc）。包含原有设置/日志/导航/提供商/自动保存/分组/凭据回归。 |
| `cd webui && npm run build && npm run check:embedded-assets && cd .. && go test ./internal/api` | Vite 成功构建；246 个可达内嵌资源和 202 个 WOFF2-only Noto 分片校验通过；构建后 API 测试通过，4.528s（ba1d7c0ad）。 |
| `gofmt -l`（本次修改/新增的 10 个 Go 文件） | 无输出，格式符合 gofmt。 |
| `git diff --check`、Git 状态和目标差异审阅 | 通过；原有未跟踪 .serena/ 未修改，后台任务生成 .pi/ 日志未纳入业务变更。 |

### 浏览器布局和行为证据

- 数据：100 个带斜杠、长连续模型标识；模型表、绑定表、提供商配置/发现列表、三组共 300 个候选复选框、100 行仪表盘模型用量。
- 视口：390×844、768×1024、1280×800、1440×900。
- 在模型管理、提供商页面/抽屉、模型分组、仪表盘图表/表格检查 documentElement scrollWidth 不超过 clientWidth + 1。
- 宽绑定表滚动后验证操作按钮在视口范围内；长模型删除确认面板、提供商抽屉面板及正文均无横向内容溢出；分组下移操作实际触发保存。
- 删除取消不发请求；管理员/配置/models.dev 来源模型可删除；忙碌期间重复点击不发第二次请求，同步入口禁用；存储失败保留行并显示错误；快照发布失败时刷新已提交的删除结果并提示运行配置未刷新。
- 永久排除和重启恢复行为由真实 SQLite/Go 测试验证；前端 mock 不作为持久化证据。

## Plan Deviations

无实质性偏离或需要重新审批的行为变更。批准范围内新增两处标题容器换行细节（JfDialog/JfDrawer），已补充到 plan.md；其余功能、迁移、兼容性及验收保持批准方案。

## Knowledge Capture

- 通过读取 nmem.cmd 中的实际可执行文件路径，直接调用 nmem.exe 恢复了 Nowledge 检索；规划阶段的 CLI 路径失败没有被隐瞒。
- 定向历史检索得到既有“分组最多 8 个成员、无效绑定禁用及保存失败回滚”的决策，与当前源码一致，本次没有改变。
- 新增一条原子决策记忆：`e24619b8-57aa-4d23-aa4f-9dfdbad6ba68`，标题“Auto-Router：逻辑模型永久删除与显式恢复边界”，provenance `source_app=pi`。记录模型/绑定双层排除、显式恢复范围以及提交后快照发布失败的含义，不保存源码、日志或凭据。

## Remaining Issues and Skipped Checks

- 计划内功能与验证无未解决错误。
- 未部署、未重启当前用户服务、未运行生产迁移或删除用户模型；正在运行的旧二进制需要使用新构建版本重启后才有新功能。
- 浏览器端到端使用项目现有 API mocks；真实数据库行为由 Go 测试覆盖，未对用户正在运行的服务做真实删除烟雾测试，避免外部数据变更。
- 未专项执行不同浏览器、完整无障碍审计、亮暗主题截图比较或大规模性能/虚拟化测试；没有宣称这些通过。本次几何验收为真实 Chromium 与上述四视口。
- 未安装依赖或浏览器、未新增依赖；用户于同日明确授权本地 Git 提交，不执行 push。没有独立 lint 脚本，因此未臆造 lint 结果。
