# Auto Router

按请求特征、模型能力与成本策略自动选择模型与 Provider 的独立 LLM Router。

**当前状态：仅阶段 1（基础架构）完成。** 路由、Jev、Bifrost 代理和管理后台尚未实现，模型 API（`/v1/chat/completions`、`/v1/responses`）和后台路径当前返回 `404`，不提供会误导客户端的假成功响应。

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

## 配置

加载顺序为：内置默认值 → JSON 文件 → 环境变量（环境变量优先）。未知 JSON 字段、非法值、空值环境变量都会让进程在监听前失败，而不是静默使用默认值。`configs/example.json` 与内置默认值保持一致。

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

时长使用 Go duration 写法（`5s`、`250ms`、`1m`）。`database.busy_timeout` 必须是整数毫秒。

说明：

- `database.path` 必须是文件系统路径，不接受 `file:` URI；`#`、`%`、空格等字符按字面量处理。相对路径相对进程工作目录解析，父目录会自动创建，新建的数据库文件在支持 POSIX 权限的平台上权限为 `0600`。
- `http.address` 中端口为 `0` 时由内核分配端口（用于测试与临时实例）。
- 未设置全局 `ReadTimeout` / `WriteTimeout`，以避免后续 SSE 流被截断；请求头超时与 keep-alive 空闲超时仍然生效。
- 收到 `SIGINT` / `SIGTERM` 后先停止接收新连接并在 `http.shutdown_timeout` 内等待在途请求结束，超时后强制关闭，进程退出。

## 端点

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| `GET` / `HEAD` | `/healthz` | 存活检查，仅表示进程在运行 |
| `GET` / `HEAD` | `/readyz` | 就绪检查，会执行一次带超时的数据库探测 |

就绪失败只返回 `{"status":"not_ready"}`，不暴露数据库路径、驱动错误或配置内容；响应带 `Cache-Control: no-store`。

## 安全

- 默认只监听回环地址。**当前没有身份认证**，不要直接暴露到公网或不受信任网络。
- 阶段 1 不记录请求体、prompt 或任何凭据。后续阶段的凭据与调试日志必须显式开启并脱敏。

## 目录结构

```text
cmd/auto-router/      进程入口、装配、生命周期
internal/api/         HTTP 边界与探针
internal/config/      启动配置、默认值、环境覆盖、校验
internal/storage/     SQLite 连接与事务迁移
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

测试不使用真实凭据或外网：使用临时 SQLite、`httptest` 与本地上游。CI（`.github/workflows/ci.yml`）在 Linux 与 Windows 上运行 `go mod verify`、`go vet`、`go test` 与构建，并在 Linux 上额外运行 `-race`。

阶段 1 已验证的内容：

- 配置默认值、文件与环境变量优先级、非法/未知/空值拒绝
- SQLite 持久化重开、含 `#` 与 `%` 的路径按字面量处理、WAL/foreign_keys/busy_timeout 在连接重建后仍生效、内存库彼此隔离
- 迁移幂等、升级保留数据、失败批次整体回滚、历史校验和/名称不匹配与更新版本库被拒绝
- 探针语义、405/404 边界、就绪超时与取消传播
- 优雅排空在途请求、超时强制关闭、真实进程启动/探针/数据库落盘的正向冒烟

平台限制：Windows 无法向当前进程投递 `os.Interrupt`，因此信号到优雅退出的真实链路测试在 Windows 上跳过，由 Linux CI 覆盖；优雅排空逻辑本身在两端都有测试。

## 后续阶段

按 [docs/ROADMAP.md](docs/ROADMAP.md) 依次进行：Model Registry → Bifrost Proxy → Jev Client → Analyzer → Policy Engine → Auto Routing → Logging → Admin API → Dashboard。每个阶段单独实现并测试后再进入下一阶段。