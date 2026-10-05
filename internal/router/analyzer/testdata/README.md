# Analyzer fixtures

每个文件都是**一份完整的请求体**（不是响应，也不是 wire 片段），按协议分组命名：
`chat_*.json` 对应 `POST /v1/chat/completions`，`responses_*.json` 对应 `POST /v1/responses`。

- 文件是**手写**的样例，不代表上游 Provider 的真实回包；协议转发正确性由
  `internal/proxy` 与提供商适配器的测试单独验证。
- `analyzer_test.go` 覆盖代码/推理文本仍保留、reasoning 内部内容不进入视图、Responses 工具结构
  仍可用于能力筛选，以及内联媒体载荷脱敏。其余文件可供 `-analyze-check` 手工排查。
- `has_code`、`reasoning_likely` 已移除，不再根据正文关键词或 reasoning 参数猜测任务难度。
  客户端 reasoning 参数仍由代理层原样转发，不由分析器生成新的字段。
- `chat_multimodal_redaction.json` 里的 `data:` 载荷是**故意可搜索的金丝雀**
  （`CANARY-BASE64-PAYLOAD`）：测试断言它不出现在任何特征、视图或错误信息中。
- 未知字段样例为 `chat_unknown_fields.json` 与 `responses_multimodal_and_unknown.json`。
  `unrecognized_parts` 只统计未识别的消息/内容结构，不代表拒绝未知顶层字段。
