# models.dev fixture provenance

`api.json` is a trimmed but verbatim copy of a real models.dev payload. The
mapping and decode tests use it so field names, value shapes and real-world edge
cases are verified against the upstream format instead of an invented one.

- Source: `https://models.dev/api.json`
- Captured: 2026-09-21 (UTC), snapshot size 4 708 807 bytes / 222 providers / 7863 models
- Trimming: four providers were kept with a small subset of their `models`
  objects. Provider objects and model objects are unchanged byte-for-byte after
  JSON reserialization (pretty-printed, UTF-8 preserved).

Included on purpose:

| Case | Fixture entry | Why |
| --- | --- | --- |
| Same logical model at two providers | `openai/gpt-4.1-nano`, `helicone/gpt-4.1-nano` (also `gpt-4o-mini`) | proves one `models` row plus two `provider_models` rows |
| Slash inside the model ID | `groq/llama-3.3-70b-versatile` | first-slash splitting |
| Provider-prefixed model ID | `groq/compound-mini` | lookup falls back to `<provider>/<model>` when the map key itself is prefixed |
| `limit.output > limit.context` | `qiniu-ai/meituan/longcat-flash-lite` (320000 > 256000) | clamp plus warning; 67 such models exist upstream |
| Model without image input | `openai/gpt-3.5-turbo` | `supports_vision=false` from `modalities.input` |
| Fields to discard | every model: `cost`, `open_weights`, `knowledge`, `release_date`, `temperature`, `structured_output`, `attachment`, `family`, `description`, ... | lenient decode plus explicit "no pricing stored" |

Observed upstream shape (2026-09-21):

```json
{
  "<provider-key>": {
    "id": "...", "name": "...", "env": ["..."], "npm": "...", "doc": "...", "api": "...",
    "models": {
      "<model-id>": {
        "id": "...", "name": "...", "reasoning": true, "tool_call": true,
        "modalities": {"input": ["text", "image", "pdf"], "output": ["text"]},
        "limit": {"context": 128000, "input": 1047576, "output": 16384},
        "cost": {"input": 0.15, "output": 0.6, "cache_read": 0.075}
      }
    }
  }
}
```

Notes that drove implementation decisions:

- Model IDs may contain `@` (version qualifiers such as `claude-sonnet-4@20250514`)
  and `~`; the first version of the ID pattern rejected them.
- 89 of 7863 model keys are prefixed with their provider key (for example
  `groq/groq/compound-mini`), so the allowlist lookup tries the exact model
  reference first and then `<provider>/<model>`.
- `limit.context` is always present in this snapshot but may be `0` upstream
  (for example `greenpt/green-s`); such a model cannot satisfy the
  `context_window > 0` invariant and is skipped with a warning.
- Unknown/extra fields are present on every model; decoding must ignore them.

To refresh the fixture, capture `https://models.dev/api.json`, keep the same
four providers and model subsets, and update this file with the new capture
date and any changed observations.
