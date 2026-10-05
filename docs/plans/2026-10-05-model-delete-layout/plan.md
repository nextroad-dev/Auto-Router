# 逻辑模型删除与模型密集页面宽度修复: Implementation Plan

## Goal

1. 模型管理的逻辑模型支持明确确认后的永久删除，而不是只停用。删除该模型的全部提供商绑定和分组引用，保留提供商、其他模型及历史路由日志，刷新运行时快照。
2. 删除记录在配置导入、models.dev 同步及重新打开数据库后仍有效，不自动复活模型或绑定；管理员显式重新创建模型或重新绑定可恢复所选对象。
3. 模型管理、提供商模型抽屉、模型分组、仪表盘模型用量在大量模型和长标识下保持可用宽度。必要的横向滚动限定在表格内部；窄屏操作可见，不用全局隐藏溢出掩盖问题。

## Current Facts and Evidence

### 当前仓库事实

- 检查日期：2026-10-05（本机 `date +%Y-%m-%d`）。Git 工作区最初只有未跟踪的 `.serena/`，不修改该目录。目标仓库及祖先未发现适用的 AGENTS.md。
- 前端是 Vue 3 + TypeScript + Tailwind CSS，后端 Go + SQLite；`webui/package.json` 已有 Vitest、Playwright、类型生成、类型检查和构建脚本，无需新增依赖。
- `webui/src/pages/Catalogue/Models.vue`：逻辑模型操作列只有“编辑”，状态使用 PATCH；目前仅提供模型绑定解除操作，逻辑模型本身没有删除函数和按钮。
- `internal/api/admin.go:199-204` 注册了模型 GET/POST/PATCH，没有模型 DELETE。Serena 对 `internal/api/admin_registry.go` 的符号概览及 `handleModel*` 精确实现检索与此一致。
- `docs/openapi.yaml` 的创建模型说明明确保留旧契约“没有 DELETE，只停用”。本任务要改变这一行为，需同步修正文档和生成类型，不能只添加前端按钮。
- `internal/storage/adminpair_delete.go` 与 `handlePairDelete` 是现有永久解除绑定模式：事务删除、持久化排除、清理分组并重新连续排序、提交后刷新分组和目录。Serena 引用检索确认 `removePairFromRoutingGroups` 目前仅被 `DeletePair` 使用。
- `internal/storage/migrations.go`：模型绑定指向 models、分组成员指向绑定，均为 ON DELETE RESTRICT；历史路由事件、尝试中的模型名称为快照文本，并没有指向 models 的外键。当前最新迁移版本为 13，模型删除需要先清理分组和绑定；不删除历史日志。
- Serena 精确检索 `ApplyRegistryImport`：当前只跳过 `deleted_provider_models` 内的绑定排除，仍会重新插入缺失的模型。仅物理删除 models 行无法保证删除持久性，需要新增模型级排除。
- Serena 精确检索 `CreateModel` / `SelectProviderModel`：显式创建模型、提供商选模均能写入 models；恢复逻辑需覆盖这两条路径。引用检索确认前者由模型创建 API 与测试调用，后者由提供商选模 API 与测试调用。
- 布局当前证据：
  - `Models.vue` 的模型 ID、提供商和绑定逻辑模型列使用 nowrap，长标识无宽度上限；JfTable 使用自动表格布局。
  - `webui/src/style.css` 的 `.jf-stack` 使用隐式 grid 列，没有 `minmax(0, 1fr)`；页面内容内层没有显式 min-width: 0。已有 `.jf-scroll-x`，应强化宽度边界而非取消能力。
  - `Providers.vue` 的模型抽屉中上游 ID、发现模型标签为未限制长文本，列表行操作区 shrink-0，窄屏可能挤压内容。
  - `JfCard.vue` 操作区 flex-shrink: 0，无最大宽度；窄屏筛选工具栏需允许自身和内容缩小/换行。
  - `ModelGroups.vue` 候选文案已有 anywhere 换行，排序区已有省略，保留并补足容器边界、完整标识提示和窄屏能力。
  - `Dashboard/Index.vue` 的模型用量表 ID 仍为 nowrap，图表名称已有省略；该页纳入模型名称宽度回归。
- 布局问题尚未通过浏览器复现/验证，以上为源码上的风险点；实施后使用真实 Chromium 几何断言确认。已有 `webui/e2e/management.spec.ts` API mock 框架和抽屉溢出断言可复用。
- `webui/vite.config.ts` 构建输出直接到 `internal/api/dashboard/static/web`，正式修复需重新生成内嵌资产；现有 `check:embedded-assets` 用于检查。

### 历史上下文与工具限制

- 已使用注入的 Nowledge 全局工程约定（先探索、书面计划、明确批准、实施验证、记录结果）。尝试定向检索 `Auto-Router 逻辑模型 删除 布局`：bash 下 nmem 不存在，nmem.cmd 又因带空格的 Windows 路径引用失败；未获取或假定历史项目决策。
- 已读取 frontend-design 技能和页面编排参考，保持现有视觉系统，仅修复布局与交互。
- Serena 已激活实际 Auto-Router 项目，Go LSP 可用；Vue/CSS 用当前源码与定向文本检索。项目未完成首次 onboarding；是否执行该可选初始化待用户决定，不阻塞本任务，当前不写 Serena 项目记忆。

## Expected Files / Modules to Change

### 模型删除完整链路

- 新增 `internal/storage/adminmodel_delete.go`、`internal/storage/adminmodel_delete_test.go`：模型删除事务、模型排除读写、分组/绑定清理及回归测试。
- `internal/storage/migrations.go`：追加版本 14 的 deleted_models 排除表，不修改已有迁移，不回填/删除日志。
- `internal/storage/registry.go`：本地与 models.dev 导入均跳过已删除模型及其绑定，保持导入计数和同步状态一致。
- `internal/storage/adminregistry.go`、`internal/storage/provider_models.go`：显式创建/选模在成功事务内清除相应模型排除；选模只恢复用户显式选择的绑定。
- `internal/storage/adminpair_delete.go`：按需要复用/小幅提取现有分组过滤与重排逻辑，使模型级删除不破坏其他成员；既有绑定删除行为不变。不重构无关提供商删除。
- `internal/api/admin.go`、`internal/api/admin_registry.go`、新增 `internal/api/admin_model_delete_test.go`：注册 DELETE `/admin/v1/models/{id...}`，返回 `{id, deleted: true}`，沿用 unknown_model / snapshot_publish_failed 错误及鉴权约定，刷新分组和目录。
- `docs/openapi.yaml`、`webui/src/lib/generated-api.ts`、`README.md`：同步模型删除契约、后果和恢复方式。
- `webui/src/pages/Catalogue/Models.vue`：删除入口、确认、忙碌防重复、刷新模型/绑定列表，协调已有自动保存与启停请求，错误使用现有 ErrorAlert。

### 宽度修复与验证

- `webui/src/style.css`、`webui/src/components/JfCard.vue`、`webui/src/components/JfTable.vue`：只对相关容器的可缩小边界、工具栏和局部表格滚动进行必要修正；保留视觉 tokens、表格和操作语义。
- 实施细节补充（2026-10-05，批准范围内）：`webui/src/components/JfDialog.vue`、`webui/src/components/JfDrawer.vue` 标题容器各追加 `overflow-wrap: anywhere`，用于长模型确认描述和提供商抽屉标题；不改变组件 API 或业务行为。
- `webui/src/pages/Catalogue/Models.vue`、`Providers.vue`、`ModelGroups.vue`、`webui/src/pages/Dashboard/Index.vue`：长模型/提供商标识的有界显示（换行或省略附完整提示）、抽屉响应式行布局；不重做页面。
- `webui/e2e/management.spec.ts`：删除交互/API mock，以及大量模型、长名称和多视口的浏览器回归。
- `internal/api/dashboard/static/web/**`：使用正式构建重新生成内嵌资产，不手工改生成文件。
- 本目录 `plan.md` / 实施后 `result.md`：审批和实际结果记录。

## Implementation Steps

1. 获批后记录实际批准内容。补模型删除测试和布局浏览器场景，覆盖当前缺失功能/风险，不修改无关现有工作。
2. 新增模型级排除迁移。实现单个事务：检查模型存在 → 记录模型和现有绑定排除 → 移除分组中的该模型所有成员并保持其他成员顺序/连续位置 → 删除绑定 → 删除模型 → 提交。任何失败全部回滚。
3. 更新导入路径：跳过排除模型及其全部绑定（包括同步新出现的提供商），防止外键错误和重建；导入/跳过计数反映真实结果。更新显式创建与选模恢复路径，成功才清除模型排除，保留未显式恢复绑定的排除。
4. 添加管理 DELETE 路由/处理器。沿用绑定删除的提交后分组/目录刷新顺序与部分发布失败报告，删除未知模型返回 404，不假装回滚已提交数据。
5. 更新 OpenAPI 和生成类型。前端添加每行删除按钮，确认说明模型标识、绑定数量、分组清理、历史日志保留和同步不恢复；取消不发请求，确认期间防重入。删除前协调该模型的自动保存/启停，成功后更新两张表；失败保留操作反馈。
6. 修复有证据的容器/长文本宽度问题：页面/grid 子项可缩小，工具栏换行，模型标识有界显示且可获取全文，抽屉行在窄屏保留操作。宽表格只在局部容器横向滚动，不设置全局 overflow-x: hidden。
7. 执行后端、前端和 Chromium 回归；必要时在已批准范围内修正并重跑。重新构建内嵌前端资产，检查差异与 Git 状态，写真实 result.md。

## Delegation

直接执行即可。删除链路、持久化排除和前端反馈相互依赖，需要统一核对；本次没有明确的子代理授权，不启动委派或 Fusion 工作流。

## Verification and Acceptance

### 后端

- `go test ./internal/storage ./internal/api ./cmd/auto-router`，随后 `go test ./...`。
- 存储测试覆盖：零绑定/多提供商绑定模型；三组内多处引用；其他成员顺序和连续位置；提供商不删除；历史事件/尝试可读取；不存在模型；事务失败回滚；本地导入与 models.dev 同步不复活；持久排除在重新开库后有效；显式重新创建/选模恢复且未选择的旧绑定仍排除。
- API 测试覆盖：带斜杠的模型 ID、成功响应、404、沿用管理鉴权与无数据库保护、分组/目录刷新、刷新失败时数据库已提交且报告 snapshot_publish_failed。
- 迁移追加且保留已有 checksum；不修改迁移 1–13，不修改历史日志保存与统计契约。

### 前端

在 `webui` 目录：

- `npm run api:types`（由 OpenAPI 更新生成类型）。
- `npm test`。
- `npm run typecheck`。
- `npm run test:e2e`（已有 Chromium 配置；若浏览器缺失，先报告并请求必要安装许可，不默默安装）。
- `npm run build`。
- `npm run check:embedded-assets`。
- 对较长命令使用 bg_run；Windows 后台命令使用 cmd 语法。

### 行为与布局验收

- 删除确认取消不发 DELETE；确认后目标模型及其绑定消失，按钮显示忙碌并防重复。失败可读，错误不被吞掉；现有编辑自动保存、启停和绑定解除继续通过回归。
- 用至少 100 个模型/绑定、长连续标识及带斜杠 ID 测试模型管理、提供商抽屉、模型分组、仪表盘模型用量表。
- 代表视口 390×844、768×1024、1280×800、1440×900；检查 `document.documentElement.scrollWidth <= clientWidth + 1`。
- 抽屉面板和正文不被长模型名称撑宽；窄屏删除、绑定、分组排序/移除可触达。表格宽度超出时局部容器能滚动到操作列，完整标识可从换行文本、提示或详情获取。
- 同步、重新加载数据库和显式重新绑定的行为以真实后端测试为证；前端 mock 不充当持久化验证。
- 实际命令和浏览器检查结果在 result.md 中如实记录；尚未运行上述验证。

## Risks and Assumptions

- 本方案默认所有来源的逻辑模型均可删除，包括管理员、配置文件和 models.dev；与仅能删除管理员来源提供商的现有规则不同，是明确提请批准的模型行为变更。
- 删除一个逻辑模型意味着删除其全部提供商绑定、移除所有相关分组成员；可能让某些分组为空。确认对话框必须明确这一点，不自动补充候选或改变路由策略。
- 新增模型级持久排除表需要应用启动正常执行追加迁移；计划授权的是代码/测试，不包含生产部署、直接操作用户数据库或批量删除现有模型。
- 历史日志保留模型标识快照，即使对应模型已删除也继续可读；修正旧 OpenAPI 对“不允许删除”的描述，而不是删除日志解决引用问题。
- 显式重新创建/绑定是可逆恢复入口：创建模型只恢复模型本身；选模恢复该模型和用户所选提供商绑定，其他已删除绑定不无意复活。
- 删除与自动保存/启停/sync 的竞态需要前端防重和后端事务保证；快照发布失败沿用现有明确报告已提交数据的模式。
- CSS 共享组件修复可能影响日志/设置等其他页面，因此完整前端回归必须执行；不以全局隐藏横向溢出或裁切操作来通过几何断言。
- 目前未运行浏览器，实际是否存在更深的溢出源待回归确认；若修复需要新增无关模块、依赖、业务/API/持久化方案变化，先更新计划并重新审批。

## Non-goals

- 不改变提供商删除权限，不新增批量删除、分页/虚拟化或全新搜索功能。
- 不修改模型分组容量、优先级、自动路由、Jev 或日志保留策略。
- 不重做视觉系统、不引入新依赖、不顺带重构其他注册表操作。
- 不提交/推送 Git，不部署，不直接删除生产/用户数据库数据，不安装依赖或浏览器。

## Approval Status

- Status: Approved
- Created: 2026-10-05
- Approval: 用户于 2026-10-05 明确回复“实施”，批准上述范围。
