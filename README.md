# Auto Router

按请求特征、模型能力与策略自动选择模型与 Provider 的独立 LLM Router。

**当前状态：阶段 1（基础架构）与阶段 2（Model Registry）已完成。** 模型 API（`/v1/chat/completions`、`/v1/responses`）和后台路径当前返回 `404`，不提供会误导客户端的假成功响应；Bifrost 代理、Jev、Policy 与管理后台尚未实现。

架构边界见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)，分阶段交付与验收门槛见 [docs/ROADMAP.md](docs/ROADMAP.md)。

## 快速开始

```bash
go build -o bin/auto-router ./cmd/auto-router
./bin/auto-router -config configs/example.json
```

不带 `-config` 时使用内置默认值。默认监听 `127.0.0.1:8080`，数据库位于 `data/auto-router.db`。

检查服务：

```bash
curl -i http://127.0.0.1:8080/healthz   # 进程存活
curl -i http://127.0.0.1:8080/readyz    # 配置与数据库可用
```

## 模型注册表

注册表由三部分组成：

1. **models.dev 同步**（可选、显式触发）：把白名单里的 `provider/model` 元数据与能力写入 SQLite；
2. **本地覆盖**（配置文件）：提供 `base_url`、`api_key`、启停、优先级与上游模型标识；
3. **SQLite**：运行时唯一真相，路由只读不可变快照。

第一次使用：

```bash
cp configs/registry.example.json config.local.json   # 可用 ${VAR} 引用环境变量
export OPENAI_API_KEY=sk-...
./bin/auto-router -config config.local.json -sync-models   # 同步后退出，不启动 HTTP
./bin/auto-router -config config.local.json                # 正常启动
```

关键语义：

- 同步只写元数据（`display_name`、能力、`upstream_model_id`、上下文/输出容量），**永不改写** `base_url`、`api_key`、`enabled`、`priority` 与本地条目；新建的同步 Provider 默认 `enabled=false` 且没有凭据，因此配置之前不可能被转发。
- 白名单条目在源数据中找不到会让**整次同步失败并回滚**；“新增模型只改配置”指加一条白名单后重跑 `-sync-models`，零代码改动。
- 从白名单或配置中移除的条目只置 `enabled=0`，永不删除，避免残留凭据被继续使用。
- `registry.sync.include` 为空时 `-sync-models` 直接失败，不会把空目录当作成功。
- 启动与请求路径不做任何网络访问，也没有后台同步；目录为空时只记录 `WARN`，服务照常提供（`/readyz` 只取决于数据库可达）。
- 阶段 2 **不存价格**：models.dev 的 `cost` 等字段同步时丢弃，表结构中没有价格/成本列，Dashboard 成本与 Policy 成本输入已从 V1 移除（见 [ROADMAP 需求偏差](docs/ROADMAP.md)）。

## 配置

加载顺序为：内置默认值 → JSON 文件 → 环境变量（环境变量优先）。未知 JSON 字段、非法值、空值环境变量都会让进程在监听前失败，而不是静默使用默认值。`configs/example.json` 与内置默认值保持一致；`configs/registry.example.json` 给出注册表段的完整示例（可直接作为 `-config` 使用）。

标量字段：

| JSON 字段 | 环境变量 | 默认值 |
| --- | --- | --- |
| `http.address` | `AUTO_ROUTER_HTTP_ADDRESS` | `127.0.0.1:8080` |
| `http.read_header_timeout` | `AUTO_ROUTER_READ_HEADER_TIMEOUT` | `5s` |
| `http.idle_timeout` | `AUTO_ROUTER_IDLE_TIMEOUT` | `60s` |
| `http.readiness_timeout` | `AUTO_ROUTER_READINESS_TIMEOUT` | `2s` |
| `http.shutdown_timeout` | `AUTO_ROUTER_SHUTDOWN_TIMEOUT` | `10s` |
| `database.path` | `AUTO_ROUTER_DATABASE_PATH` | `data/auto-router.db` |
| `database.busy_timeout` | `AUTO_ROUTER_DATABASE_BUSY_TIMEOUT` | `5s` |
| `log.level` | `AUTO_ROUTER_LOG_LEVEL` | `info` |

注册表字段（**只能来自配置文件**，环境变量不覆盖列表）：

| JSON 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `registry.sync.url` | `https://models.dev/api.json` | 必须是 `https`；仅回环地址允许 `http`（测试用） |
| `registry.sync.timeout` | `30s` | 单次抓取超时；响应体上限 32 MiB |
| `registry.sync.include` | `[]` | 白名单，格式 `provider/model`，以第一个 `/` 分隔 |
| `registry.providers[]` | `[]` | `key`、`display_name`、`base_url`、`api_key`、`enabled`、`priority` |
| `registry.models[]` | `[]` | `provider`、`model`、`upstream_model_id`、`display_name`、`context_window`、`max_output`、`supports_tools`、`supports_vision`、`supports_reasoning`、`enabled`、`priority` |

约定：

- `api_key` 支持 `${VAR}` 展开；变量未设置会让进程启动失败并只报变量名，不打印值。`$${` 转义为字面量 `${`。
- `registry.models[].provider` 必须在本文件的 `registry.providers[]` 中声明；写成未声明的 Provider 直接报错（几乎都是拼写错误，且这样的 pair 永远无法启用）。
- `enabled` 省略即 `false`（fail-closed）；`priority` 数值越小越优先；`upstream_model_id` 省略时默认等于 `model`。
- 标识约束：Provider key 为 `^[a-z0-9][a-z0-9._-]{0,63}$`；模型 ID 允许字母数字与 `._:/@~+-`（真实 models.dev 中存在 `meta-llama/Llama-3.3-70B`、`claude-sonnet-4@20250514` 这类 ID），最长 128 字符。
- 容量约束：`context_window > 0`；`max_output` 省略或落在 `(0, context_window]`；违反约束的配置在启动前报错。

## 命令与端点

| 命令 / 端点 | 作用 |
| --- | --- |
| `-config <file>` | 指定 JSON 配置文件 |
| `-sync-models` | 从 models.dev 同步白名单后退出（不启动 HTTP） |
| `GET` / `HEAD` `/healthz` | 存活检查，仅表示进程在运行 |
| `GET` / `HEAD` `/readyz` | 就绪检查，会执行一次带超时的数据库探测 |

就绪失败只返回 `{"status":"not_ready"}`，不暴露数据库路径、驱动错误或配置内容；响应带 `Cache-Control: no-store`。

## 安全

- 默认只监听回环地址。**当前没有身份认证**，不要直接暴露到公网或不受信任网络。
- Provider 的 `api_key` **明文存储**在 SQLite 中。缓解措施：POSIX 下数据库与 `-wal`/`-shm` 为 `0600`、日志与错误信息一律掩码（`sk-…abcd`）、支持 `${VAR}` 避免明文入库版本、`.gitignore` 覆盖 `*.db*`。
- **残余风险（未假装已解决）**：磁盘或备份可读、Windows ACL 未加固、无静态加密、无密钥轮换。静态加密与 Windows ACL 列为显式推迟项。
- 普通运行日志不记录请求体、prompt 或凭据。

## 目录结构

```text
cmd/auto-router/      进程入口、装配、生命周期、-sync-models
internal/api/         HTTP 边界与探针
internal/config/      启动配置、默认值、环境覆盖、registry 段与校验
internal/models/      注册表领域类型、校验、排序契约、只读快照
internal/modelsdev/   models.dev 抓取、白名单映射与真实 fixture
internal/storage/     SQLite 连接、事务迁移、注册表持久化
configs/              示例配置
docs/                 架构边界与阶段计划
```

## 开发

```bash
gofmt -l cmd internal          # 应无输出
go vet ./...
go test -count=1 ./...
go build ./cmd/auto-router
```

`go test -race` 需要 cgo 与 C 工具链；本机没有 C 工具链时该命令无法运行，`-race` 由 Linux CI 覆盖。

测试不使用真实凭据或外网：使用临时 SQLite、`httptest` 与本地上游。`internal/modelsdev/testdata/api.json` 是从真实 `models.dev/api.json` 抓取并裁剪的固定 fixture（来源与裁剪方式见同目录 README），测试本身不访问网络。CI（`.github/workflows/ci.yml`）在 Linux 与 Windows 上运行 `go mod verify`、`go vet`、`go test` 与构建，并在 Linux 上额外运行 `-race`。

已完成的验证：

- 阶段 1：配置默认值、文件与环境变量优先级、非法/未知/空值拒绝；SQLite 持久化重开、含 `#` 与 `%` 的路径按字面量处理、WAL/foreign_keys/busy_timeout 在连接重建后仍生效、内存库彼此隔离；迁移幂等、失败批次整体回滚、历史校验和/名称不匹配与更新版本库被拒绝；探针语义、405/404 边界、就绪超时与取消传播；优雅排空在途请求、超时强制关闭、真实进程启动/探针/数据库落盘的正向冒烟。
- 阶段 2：迁移 v1 建表幂等、外键与 CHECK 约束、事务失败整体回滚；同步幂等、白名单移除与本地条目移除只禁用不删除、本地覆盖不被同步改写；models.dev 映射（含共享模型、含 `/` 的模型 ID、Provider 前缀模型键、`max_output` 截断告警、缺上下文跳过、缺失白名单整体失败、`cost` 被丢弃）；`httptest` 假源下的超时/4xx/5xx/畸形 JSON；真实进程 `-sync-models` → 启动 `/readyz` → 关闭 → 重开仍可加载；密钥不进日志与错误信息、POSIX 文件权限收紧。

平台限制：Windows 无法向当前进程投递 `os.Interrupt`，因此信号到优雅退出的真实链路测试在 Windows 上跳过，由 Linux CI 覆盖；优雅排空逻辑本身在两端都有测试。

## 后续阶段

按 [docs/ROADMAP.md](docs/ROADMAP.md) 依次进行：Bifrost Proxy → Jev Client → Analyzer → Policy Engine → Auto Routing → Logging → Admin API → Dashboard。每个阶段单独实现并测试后再进入下一阶段。
