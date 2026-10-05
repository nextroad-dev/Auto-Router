# Security Policy

Auto Router 保管上游 API 密钥并代理请求，安全问题我们会优先处理。

## 报告漏洞

请**不要**在公开 Issue 中披露漏洞。请通过 GitHub 的私密漏洞报告提交：
仓库的 **Security → Report a vulnerability**（<https://github.com/nextroad-dev/Auto-Router/security/advisories/new>）。

请尽量包含：影响的版本或镜像摘要、复现步骤、影响范围，以及（如有）修复建议。请勿在报告中附带真实的 API 密钥。

我们会尽快确认收到并跟进，修复发布后会在更新日志中说明。

## 支持的版本

仅对最新发布版本及 `main` 分支提供安全修复。

## 部署建议

- 默认只绑定 `127.0.0.1`；对外服务前请放置 TLS 反向代理，并正确设置 `-trusted-proxies`。
- 不要将管理页面直接暴露到公网。
- 妥善保管数据卷：其中包含数据库、上游密钥与管理配置。
- `/debug/analyze`、`/debug/route` 可能处理敏感请求内容，仅限本机访问。
