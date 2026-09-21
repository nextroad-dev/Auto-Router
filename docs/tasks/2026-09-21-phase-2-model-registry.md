# 阶段 2 完成记录：Model Registry（models.dev 同步 + 本地覆盖）

| 项 | 值 |
| --- | --- |
| 阶段 | 2 / 10（详见 [../ROADMAP.md](../ROADMAP.md)） |
| 状态 | **已完成并验证** |
| 提交 | `c510d65` feat: add stage 2 model registry (models.dev sync, local overrides, snapshots) |
| 上一阶段 | `8b3722f` 阶段 1 基础架构 |
| 完成日期 | 2026-09-21 |
| 变更规模 | 32 个文件，+5179 / −69 行 |
| 新增包 | `internal/models`、`internal/modelsdev`、`internal/storage/registry.go`、`internal/config/registry.go`、`cmd/auto-router/registry.go` |
| 验证环境 | Windows + Go 1.27.1；`CGO_ENABLED=0` 构建通过；`-race` 由 Linux CI 覆盖 |

## 1. 目标与边界

交付一个**可由外部同步的模型 / Provider 注册表**：models.dev 提供模型元数据与能力；本地配置提供 `base_url`、`api_key`、启停、优先级与上游模型标识；SQLite 是运行时唯一真相；路由侧只依赖不可变快照。新增模型只需改配置或重跑同步，零代码改动。

本阶段**不做**：模型/Provider 的 HTTP API、Bifrost 调用、Jev、Policy、成本计算、后台页面、基于健康检查的可用状态、后台自动同步、任何真实模型调用。

## 2. 已锁定决策与需求偏差

| 决策 | 取值 | 影响 |
| --- | --- | --- |
| 价格 / 成本 | **完全不存**：无价格列、无成本计算，models.dev `cost` 同步时丢弃 | 需求 7 的 `input_price`/`output_price`、需求 11 的 cost、需求 12 的 Dashboard 成本、阶段 6 Policy 的价格输入全部移出 V1 |
| Provider 密钥 | **明文落库**（SQLite `api_key`） | 记录为已接受的残余风险，配以掩码、POSIX 权限、`${VAR}` 等缓解 |
| 元数据来源 | models.dev 同步 | 不硬编码任何厂商 URL，`base_url` 只来自本地配置 |
| 同步范围 | 精确到模型的显式白名单 | 白名单缺失项导致整次同步失败，不接受“部分成功” |

偏差已同步写入 [../ROADMAP.md](../ROADMAP.md)（需求偏差段落 + 阶段 2/6/8/10 表格改写）与 [../ARCHITECTURE.md](../ARCHITECTURE.md)。

## 3. 交付内容

### 3.1 数据模型（迁移 v1，追加式）

`internal/storage/migrations.go` 追加 `migration{Version: 1, Name: "create_registry_tables"}`，历史校验和机制保证已应用迁移不可改写。4 张 `STRICT` 表：

| 表 | 主键 | 关键列 | 约束 |
| --- | --- | --- | --- |
| `providers` | `key` | `display_name`、`base_url`、`api_key`、`enabled`、`priority`、`source`、`updated_at` | `enabled IN (0,1)`、`source IN ('modelsdev','local')` |
| `models` | `id` | `display_name`、`enabled`、`priority`、`source`、`updated_at` | 同上 |
| `provider_models` | `(provider_key, model_id)` | `upstream_model_id`、`context_window`、`max_output`、`supports_tools`、`supports_vision`、`supports_reasoning`、`enabled`、`priority`、`source`、`updated_at` | `context_window > 0`；`max_output IS NULL OR (0 < max_output <= context_window)`；FK `ON DELETE RESTRICT` |
| `registry_sync_state` | `source` | `url`、`fetched_at`、`allowlist_digest`、`imported_pairs`、`skipped_pairs`、`warnings`(JSON)、`updated_at` | `source IN ('modelsdev')` |

**无任何价格 / 成本列。** 外键使用 `RESTRICT`，永不级联删除。

### 3.2 `internal/models`：领域与快照

- `Source`（`modelsdev` / `local`）、`Provider`、`Model`、`Pair`、`Catalog`。
- 能力以 **Pair（Provider × 逻辑模型）** 为单位，不做跨 Provider 合并。
- `Catalog` 在构造时校验（重复 key/对、悬空引用、容量越界），**拷贝并排序**输入，并建立 provider/model/pairsByModel 索引；`Provider(key)`、`Lookup(modelID)`、`PairsForModel(modelID)`（仅当 pair、Provider、逻辑模型三者都启用）、`Warnings()`（有界）。
- 排序契约（阶段 6 直接复用）：`pair.priority` 升序 → `provider.priority` 升序 → `provider.key` 升序 → `model.id` 升序；数值小者优先。
- `Store` 以 `atomic.Pointer` 发布快照，写侧加锁递增 `Generation`（单调、并发下不重号）；`Load()` 永不为 nil。
- `Provider` 实现 `slog.LogValuer`：`api_key` 输出掩码（`sk-…qrst`，长度 < 16 输出 `***`，空串输出空），`base_url` 保留。

校验矩阵（`internal/models/validate.go`）：

| 对象 | 规则 |
| --- | --- |
| provider key | `^[a-z0-9][a-z0-9._-]{0,63}$`，唯一 |
| base_url | 绝对 `http(s)`，禁止 userinfo / query / fragment，允许路径前缀；`enabled=true` 时必填，禁用时可为空 |
| model id | `^[A-Za-z0-9][A-Za-z0-9._:/@~+-]{0,127}$`，唯一 |
| pair | `(provider,model)` 唯一（重复导入幂等）、`upstream_model_id` 非空且 ≤256 字节、`context_window > 0`、`max_output` 为空或 `(0, context_window]`、`priority >= 0` |
| 目录一致性 | `PairsForModel` 只返回三者均启用的项；启用 pair + 停用 Provider/模型 → `WARN` |

### 3.3 同步流程（`-sync-models`）

1. 读取 `registry.sync.url`（默认 `https://models.dev/api.json`）与 `timeout`（默认 `30s`）；URL 必须 `https`，仅回环地址允许 `http`；响应体上限 32 MiB。
2. 宽松解码：忽略未知字段；顶层不是对象、或某个 Provider 缺少 `models` 对象 → 格式错误。
3. 逐个解析白名单 `provider/model`（**以第一个 `/` 分隔**，模型名可含 `/`）；若源数据中模型键自带 Provider 前缀（如 `groq/groq/compound-mini`），按 `<provider>/<model>` 回退查找。
4. 白名单项缺失 → **整体失败**，列出全部缺失项（上限 20 条 + 计数），事务回滚，库不变。
5. 映射：`supports_tools ← tool_call`、`supports_reasoning ← reasoning`、`supports_vision ← modalities.input 含 image`；`context_window ← limit.context`、`max_output ← limit.output`（缺失或 ≤0 → NULL）；`cost`、`open_weights`、`knowledge`、`release_date`、`temperature` 等一律丢弃；`max_output > context_window` 截断为窗口并记 warning；`limit.context` 缺失或 ≤0 的模型跳过并记 warning（计入 `skipped_pairs`）。
6. 单个事务写入：upsert providers（仅 `display_name`）、upsert models（`display_name`）、upsert pairs（能力 / 上游 id / 容量）、把同源但已从白名单移除的 pair 置 `enabled=0`、写 `registry_sync_state`。
7. **同步绝不改写本地覆盖**：`source='local'` 的行整行跳过；`base_url`、`api_key`、`enabled`、`priority`、`upstream_model_id` 覆盖都不会被同步覆盖。新建 Provider 行 `enabled=0`、无 URL / 密钥。
8. 重新读取目录并输出摘要日志（新增/更新/禁用条数、warnings），退出码反映成败；该模式**不启动 HTTP 服务**。

### 3.4 本地覆盖流程（启动时）

- `registry.providers[]` → upsert（`source='local'`，全字段权威）；`registry.models[]` → upsert 逻辑模型与 pair（`source='local'`，显式能力，`upstream_model_id` 缺省等于模型 ID，`display_name` 缺省等于模型 ID）。
- 本轮配置中不存在的 `source='local'` 条目置 `enabled=0`（永不删除），避免残留失效凭据被继续使用。
- 启动**不做任何网络访问**；目录为空只记 `WARN` 并继续提供服务（`/readyz` 只看数据库可达）。
- 目录一致性：启用 pair 而 Provider 停用 → 启动日志 `WARN`。

### 3.5 配置（`registry` 段）

```json
"registry": {
  "sync": {
    "url": "https://models.dev/api.json",
    "timeout": "30s",
    "include": ["openai/gpt-4o", "groq/llama-3.3-70b-versatile"]
  },
  "providers": [
    { "key": "openai", "display_name": "OpenAI", "base_url": "https://api.openai.com/v1",
      "api_key": "${OPENAI_API_KEY}", "enabled": true, "priority": 10 }
  ],
  "models": [
    { "provider": "openai", "model": "private-model", "upstream_model_id": "private-model-v2",
      "display_name": "Private", "context_window": 32000, "max_output": 4096,
      "supports_tools": true, "supports_vision": false, "supports_reasoning": false,
      "enabled": true, "priority": 50 }
  ]
}
```

- `api_key` 支持 `${VAR}` 展开；变量未设置视为配置错误（错误信息只含变量名）；`$${` 转义为字面量 `${`。
- 列表不支持环境变量覆盖（环境变量只覆盖既有标量字段）。
- `registry.models[].provider` 必须在本文件的 `registry.providers[]` 中声明，否则报错（未声明的 Provider 永远无法启用，几乎都是拼写错误）。
- 默认值：`url=https://models.dev/api.json`、`timeout=30s`、其余为空列表；`Defaults()` 与 `configs/example.json` 仍逐字段相等（测试改为 `reflect.DeepEqual`，因 `Config` 含切片不再可比较）。
- 新增 `configs/registry.example.json`（仅 `registry` 段，其余字段沿用默认值；缺失环境变量时启动直接失败）。
- `Config.Warnings()`：启用的 Provider 缺少 `api_key` → 启动 `WARN`（本机网关允许空密钥）。

### 3.6 密钥安全与残余风险

已实现缓解：

- POSIX 下数据库、`-wal`、`-shm` 收紧为 `0600`；新建数据库目录 `0700`。`storage.Open` 在连接后收紧一次，迁移（首个写事务，会延迟创建 sidecar）后再执行 `storage.HardenDatabaseFiles` 一次。Windows 跳过（无 POSIX 权限位）。
- 日志与结构体输出一律掩码：`Provider` 实现 `slog.LogValuer`；错误信息由 `wrapWrite` 包装，不回显绑定参数。
- `${VAR}` 支持，避免明文写进受版本控制的配置文件；`.gitignore` 覆盖 `*.db*`。

**残余风险（明确记录，未假装解决）**：磁盘/备份可读、Windows ACL 未加固、无静态加密、无密钥轮换。静态加密与 Windows ACL 加固为显式推迟项。

## 4. 验收对照

| 阶段 2 门槛 | 证明 |
| --- | --- |
| 同模型多 Provider | fixture 中 `gpt-4.1-nano` 同时属于 `openai` 与 `helicone`：目录 1 行 `models` + 2 行 `provider_models`（测试断言 + 真实进程 smoke） |
| 重复/悬空映射拒绝 | 同一配置重放幂等（第二次只报 update，无重复行）；悬空 pair 在写入前被拒，事务回滚；直连 SQL 插入悬空 FK 被拒 |
| 校验（含容量） | 校验矩阵表驱动测试：非法 key/id/URL（userinfo、query、fragment）、`max_output > context_window`、`context_window=0`、负优先级、source 枚举、布尔越界 |
| 重启后保留 | 关闭进程 → 重开同一 SQLite → 目录一致（存储测试 + 真实进程 smoke） |
| 新增模型仅改配置 | 追加一条白名单 → 重跑 `-sync-models` → 目录出现该模型，零代码改动（`TestRunSyncModelsImportsAllowlistWithoutListening` 第二轮同步断言） |
| 密钥不泄露 | 掩码单测 + JSON 日志断言 + 错误信息不含密钥 + 真实进程 smoke 扫描日志 + POSIX `0600` 断言（Linux CI） |
| 同步失败不留痕 | 缺失白名单项、4xx/5xx、畸形 JSON、超时、超大体、`http` 非回环被拒（回环允许）：库内容与 `registry_sync_state` 均不变 |

## 5. 验证结果

```text
gofmt -l cmd internal          → 无输出
go vet ./...                   → 通过
go test -count=1 ./...         → 全部通过
  cmd/auto-router              84.5%
  internal/api                100.0%
  internal/config              97.8%
  internal/models              97.3%
  internal/modelsdev           95.8%
  internal/storage             84.8%
  total                        91.0%
CGO_ENABLED=0 go build ./cmd/auto-router → 通过
```

测试资产：阶段 2 新增 9 个测试文件、57 个测试函数，并改造 4 个既有测试文件（迁移日志断言、POSIX 权限、`reflect.DeepEqual`、schema 版本号）。仓库累计 91 个测试函数。

真实进程验证（手工 + 自动化 smoke）：

1. `-sync-models` 指向本地 `httptest` 假源 → 退出码 0，写入 2 个 Provider / 2 个逻辑模型 / 2 个 pair 与 `registry_sync_state`；
2. 配置本地 Provider 后启动 → `/readyz` 返回 `{"status":"ready"}`；
3. 关闭进程 → 重开数据库仍可加载目录，同步 pair 已可路由；
4. 全量日志扫描确认 `sk-…` 明文密钥未出现；缺失环境变量时退出码 1 且只打印变量名。

观察到的日志摘要（同步模式与启动模式）：

```json
{"msg":"model registry synchronized","providers":{"added":2,"updated":0,"disabled":0},"models":{"added":2,...},"pairs":{"added":2,...}}
{"msg":"model registry loaded","providers":2,"providers_enabled":0,"models":2,"pairs":2}
{"msg":"model registry warning","detail":"pair helicone/gpt-4.1-nano is enabled but provider \"helicone\" is disabled"}
```

## 6. 实现中发现并修正的偏差

这些是“先抓真实 fixture 再写映射”直接换来的结果，已记录在 `internal/modelsdev/testdata/README.md`：

1. **模型 ID 字符集**：计划中的 `^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$` 会拒绝真实数据中的 `@`（`claude-sonnet-4@20250514`，144 处）与 `~`（36 处）。放宽为 `^[A-Za-z0-9][A-Za-z0-9._:/@~+-]{0,127}$`。
2. **Provider 前缀模型键**：7863 个模型键中有 89 个以自身 Provider 前缀开头（如 `groq/groq/compound-mini`），因此白名单查找增加 `<provider>/<model>` 回退；逻辑模型 ID 取上游 `id` 字段（该数据集里恰好等于 map key）。
3. **`limit.output > limit.context` 真实存在**：快照中有 67 个此类模型（如 `qiniu-ai/meituan/longcat-flash-lite` 320000 > 256000），截断 + warning 路径因此是必需的，并与 fixture 回归绑定。
4. **`limit.context` 可为 0**：如 `greenpt/green-s`，无法满足 `context_window > 0`，改为跳过并计数 / 告警，不让单条脏数据阻断整个白名单。
5. **`limit.output` 可为 0**：按 NULL（未知）处理，不写成 0 容量。
6. **共享逻辑模型需去重**：映射结果按 `id` 折叠 Provider/模型行，否则同模型多 Provider 会撞主键。

其他实现决策（计划未细化但已测试）：

- 同一批次内重复条目按 upsert 折叠（last-wins），不报错。
- 同步写入 `registry_sync_state` 前对 warnings 截断至 100 条并附计数标记。
- 同步刚结束时新建 Provider 必然停用，为避免噪声，**同步模式不打印目录一致性 WARN**，改由下次启动打印（那时才可操作）。
- `Catalog` 内部建立索引，`PairsForModel` 不再全表扫描，为阶段 6/7 的请求路径做准备。

## 7. 相关文件

| 路径 | 作用 |
| --- | --- |
| `internal/storage/registry.go` | 导入 / 禁用 / 加载目录 / 同步状态 |
| `internal/storage/migrations.go` | 迁移 v1（注册表 4 表） |
| `internal/storage/sqlite.go` | 连接、WAL、`HardenDatabaseFiles` |
| `internal/models/*.go` | 领域类型、校验、目录与快照、掩码 |
| `internal/modelsdev/modelsdev.go` | 抓取、解码、白名单映射、目录指纹 |
| `internal/modelsdev/testdata/` | 真实 `models.dev/api.json` 裁剪 fixture 与来源说明 |
| `internal/config/registry.go` | `registry` 段解析、`${VAR}` 展开、校验、告警 |
| `cmd/auto-router/registry.go` | 启动装配、`-sync-models` 编排、摘要日志 |
| `cmd/auto-router/smoke_test.go` | 真实二进制 smoke |
| `configs/registry.example.json` | 注册表示例配置 |

## 8. 后续阶段

按 [../ROADMAP.md](../ROADMAP.md)：阶段 3（Bifrost Proxy）→ 4（Jev Client）→ 5（Analyzer）→ 6（Policy Engine，复用本阶段的排序契约与 `PairsForModel`）→ 7 → 8（Logging，去掉 cost）→ 9（Admin API，密钥只回显掩码）→ 10（Dashboard，去掉成本）。
