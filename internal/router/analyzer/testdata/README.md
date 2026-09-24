# Analyzer fixtures

每个文件都是**一份完整的请求体**（不是响应，也不是 wire 片段），按协议分组命名：
`chat_*.json` 对应 `POST /v1/chat/completions`，`responses_*.json` 对应 `POST /v1/responses`。

- 文件是**手写**的样例，用于固定特征口径的断言；它们不代表上游 Provider 的真实回包，
  也不承担"协议正确性"的举证责任（那是阶段 3 的透传测试与阶段 4 的 wire fixture）。
- 每个文件在 `analyzer_test.go` 的表里出现一次，并断言**完整的 `Features` 值**（不是"非零即可"），
  所以改动分析口径必须同步改动 fixture 与期望值。
- `chat_multimodal_redaction.json` 里的 `data:` 载荷是**故意可搜索的金丝雀**
  （`CANARY-BASE64-PAYLOAD`）：测试断言它不出现在任何特征、视图或错误信息中。
- 未知字段样例（`chat_unknown_fields.json`、`responses_unknown_items.json`）同时被
  `internal/proxy` 的透传测试引用，用于证明"未知字段不被拒绝、转发字节不变"。
