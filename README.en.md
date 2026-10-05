# Auto Router

[![CI](https://github.com/nextroad-dev/Auto-Router/actions/workflows/ci.yml/badge.svg)](https://github.com/nextroad-dev/Auto-Router/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

English | [简体中文](README.md)

A self-hosted LLM gateway that routes requests automatically. Manage upstream providers, model capabilities and routing groups in one place; when a client sends `model: "auto"`, Auto Router picks a task group (via the Jev recommender plus local capability policy), filters eligible models, and forwards to the upstream.

> The detailed documentation is currently maintained in Chinese ([README.md](README.md)). This page covers the essentials; the [OpenAPI spec](docs/openapi.yaml) is in English.

## Features

- OpenAI Chat Completions / Responses, Anthropic Messages and Gemini generateContent endpoints, including streaming.
- `auto` routing: Jev recommends a group (`simple` / `medium` / `complex`), local policy filters models by capability and context window, then picks the first eligible model in configured order.
- Failover only within the same group; attempts and triggers are configurable.
- Built-in admin console: providers, models, groups, inference keys, request logs, usage, settings.
- Model metadata sync from [models.dev](https://models.dev) with local overrides.
- Single Go binary + SQLite, embedded admin UI, multi-arch Docker image.

## Quick start

```sh
docker run -d \
  --name auto-router \
  --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v auto-router-data:/app/data \
  ghcr.io/nextroad-dev/auto-router:latest
```

Open <http://127.0.0.1:8080/admin/> and set the admin password. When connecting from a non-loopback address (including through Docker port mapping), the page asks for a one-time `bootstrap_token` printed in the startup log:

```sh
docker logs auto-router 2>&1 | grep bootstrap_token
```

Then add providers and models, configure groups, and create an inference key. Do not delete the `auto-router-data` volume: it holds the database and all configuration.

> **Security**: the port is bound to localhost by default. Put a TLS reverse proxy in front (and set `-trusted-proxies`) before exposing it.

## Send a request

```sh
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Authorization: Bearer YOUR_INFERENCE_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"model": "auto", "messages": [{"role": "user", "content": "Hello"}]}'
```

## Build from source

```sh
go build -o bin/auto-router ./cmd/auto-router
```

The built admin UI is committed under `internal/api/dashboard/static/web/`, so Node is only needed when changing the frontend. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[Apache-2.0](LICENSE). Bundled fonts are under SIL OFL 1.1, see [NOTICE](NOTICE). Report vulnerabilities per [SECURITY.md](SECURITY.md).
