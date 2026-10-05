# 贡献指南

感谢你愿意参与 Auto Router！

## 开始之前

- 较大的改动（新协议、路由策略、存储结构）请先开 Issue 讨论。
- 安全问题请按 [SECURITY.md](SECURITY.md) 私下报告，不要公开提交。
- 参与即表示同意遵守 [行为准则](CODE_OF_CONDUCT.md)，贡献内容以 [Apache-2.0](LICENSE) 授权。

## 开发环境

- Go：版本见 [`go.mod`](go.mod)
- Node 22（仅修改管理台前端时需要）

```sh
git clone https://github.com/nextroad-dev/Auto-Router.git
cd Auto-Router
go run ./cmd/auto-router      # 默认监听本机，打开 /admin/
```

## 后端

```sh
gofmt -l .              # 不应有输出
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
```

- 代码需通过 `gofmt`；仓库使用 LF 换行（见 `.gitattributes`）。
- 行为改动请附带测试。
- 修改管理 API 时同步更新 [`docs/openapi.yaml`](docs/openapi.yaml)。

## 管理台前端（`webui/`）

```sh
cd webui
npm ci
npm run dev               # 开发服务器
npm run api:types         # 由 openapi.yaml 生成 src/lib/generated-api.ts
npm test                  # Vitest
npm run typecheck
npm run test:e2e          # Playwright（首次需 npx playwright install chromium）
npm run build             # 产物输出到 internal/api/dashboard/static/web/
npm run check:embedded-assets
```

构建产物（`internal/api/dashboard/static/web/`）提交在仓库中，使 `go build` 不依赖 Node。**修改前端后请重新 `npm run build` 并一同提交产物**，CI 会校验其与源码一致。`src/lib/generated-api.ts` 同样需与 `docs/openapi.yaml` 保持一致。

## 提交与 PR

- 从 `main` 创建分支；提交信息建议使用 `feat:` / `fix:` / `docs:` / `chore:` 前缀。
- 一个 PR 聚焦一件事，说明动机与测试方式。
- 影响使用者的行为变更（日志字段、配置、接口）请记入 [CHANGELOG.md](CHANGELOG.md) 的 `Unreleased`。
- 提交前请本地跑通上面的检查；CI 会在 Linux 和 Windows 上运行。
