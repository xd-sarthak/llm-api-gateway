<div align="center">

# llm/gateway

**A self-hosted API gateway for LLM providers with key management, rate limiting, semantic caching, usage tracking, and a real-time admin dashboard.**

[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Next.js](https://img.shields.io/badge/Next.js-16-000000?logo=next.js&logoColor=white)](https://nextjs.org)
[![Postgres](https://img.shields.io/badge/PostgreSQL-16-4169E1?logo=postgresql&logoColor=white)](https://postgresql.org)
[![Redis](https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white)](https://redis.io)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

</div>

---

## Why

You have one OpenRouter API key. You need to give LLM access to multiple services, teams, or projects — each with their own key, rate limit, and usage tracking. You don't want to expose your provider key, and you want to see exactly who's using what.

**llm/gateway** sits between your clients and the upstream provider, giving you:

- 🔑 **Per-client API keys** — issue, revoke, and track keys independently
- 🚦 **Token bucket rate limiting** — Redis-backed, per-key, configurable window
- 🧠 **Semantic caching** — exact hash + pgvector similarity matching, saves tokens on repeated queries
- 📊 **Usage logging** — per-request logging with model, tokens, cost, latency
- 💰 **Automatic cost tracking** — fetches live pricing from OpenRouter
- 🖥️ **Admin dashboard** — real-time Next.js UI for managing keys and monitoring usage

---

## Architecture

```
                                    ┌─────────────────────────────────────────┐
                                    │            llm/gateway                  │
                                    │                                         │
  Client ──Bearer key──►  ┌─────────┴──────────┐                              │
                          │   Chi Router        │                              │
                          │   Logger + CORS     │                              │
                          └────────┬────────────┘                              │
                                   │                                           │
                          ┌────────▼────────────┐                              │
                          │  Auth Middleware     │◄──── Postgres (api_keys)     │
                          │  SHA-256 hash lookup │                              │
                          └────────┬────────────┘                              │
                                   │                                           │
                          ┌────────▼────────────┐                              │
                          │  Rate Limiter        │◄──── Redis (token bucket)   │
                          │  Per-key enforcement │                              │
                          └────────┬────────────┘                              │
                                   │                                           │
                          ┌────────▼────────────┐     ┌──────────────────┐     │
                          │  Semantic Cache      │◄───►│ Postgres         │     │
                          │  Hash + pgvector     │     │ (semantic_cache)  │     │
                          └────────┬────────────┘     └──────────────────┘     │
                                   │ cache miss                                │
                          ┌────────▼────────────┐                              │
                          │  Proxy Handler       │──────► OpenRouter API       │
                          │  Body limit + relay  │                              │
                          └────────┬────────────┘                              │
                                   │ async                                     │
                          ┌────────▼────────────┐                              │
                          │  Usage Logger        │──────► Postgres (usage_logs)│
                          │  Cost Calculator     │◄──── Pricing cache          │
                          └─────────────────────┘                              │
                                                                               │
  Admin ──────────────►   ┌─────────────────────┐                              │
                          │  Admin API           │◄──── Postgres               │
                          │  /admin/*            │                              │
                          └─────────────────────┘                              │
                                    └─────────────────────────────────────────┘

  Dashboard (Next.js)  ──────────►  GET/POST /admin/*
```

---

## Quick Start

### Prerequisites

- Go 1.24+
- Node.js 20+ & pnpm
- PostgreSQL 16 (with pgvector extension)
- Redis 7+

### 1. Clone and configure

```bash
git clone https://github.com/xd-sarthak/llm-api-gateway.git
cd llm-api-gateway
cp .env.example .env
# Edit .env with your actual keys
```

### 2. Start infrastructure

```bash
docker compose up -d   # Postgres with pgvector
# Redis must be running on localhost:6379
```

### 3. Run migrations

```bash
psql $DATABASE_URL -f migrations/001_create_api_keys.sql
psql $DATABASE_URL -f migrations/002_create_usage.sql
psql $DATABASE_URL -f migrations/003_create_cache.sql
```

### 4. Start the gateway

```bash
go run ./cmd/server
# → server is running on port 8080
```

### 5. Start the admin dashboard

```bash
cd app
pnpm install
pnpm dev
# → http://localhost:3000
```

---

## Usage

### Create an API key

```bash
curl -X POST http://localhost:8080/admin/keys \
  -H 'Content-Type: application/json' \
  -d '{"name": "my-service"}'
```

Returns a `sk_` prefixed key — **save it, it's shown only once**.

### Make a chat completion request

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_your_key_here' \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

The gateway authenticates the key, checks the rate limit, checks the semantic cache, proxies to OpenRouter if needed, logs usage, and returns the response.

---

## API Reference

### Proxy Endpoint

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/v1/chat/completions` | Bearer key | Proxies to OpenRouter with auth, rate limit, and caching |

### Admin Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/admin/stats` | Aggregate stats (requests, tokens, cost, cache entries) |
| `GET` | `/admin/usage` | Per-key, per-model usage breakdown |
| `GET` | `/admin/keys` | List all API keys |
| `POST` | `/admin/keys` | Create a new API key |
| `DELETE` | `/admin/keys/:id` | Deactivate an API key |

---

## Project Structure

```
llm-gateway/
├── cmd/server/             # Application entrypoint
│   └── main.go             # Server bootstrap, router, middleware wiring
├── internal/
│   ├── admin/              # Admin API handlers + route registration
│   │   ├── handlers.go     # Stats, usage, key CRUD handlers
│   │   └── routes.go       # Chi route group /admin/*
│   ├── auth/               # Authentication middleware
│   │   ├── middleware.go    # Bearer token → SHA-256 → Postgres lookup
│   │   └── context.go      # Request-scoped API key context
│   ├── cache/              # Semantic caching layer
│   │   ├── store.go        # Exact hash + pgvector similarity lookup/store
│   │   └── embeddings.go   # Gemini embedding API client
│   ├── proxy/              # LLM proxy handler
│   │   ├── handler.go      # Request relay, body limit, usage logging
│   │   └── pricing.go      # OpenRouter pricing cache + cost calculation
│   ├── ratelimit/          # Rate limiting
│   │   ├── middleware.go    # Chi middleware, per-key enforcement
│   │   └── limiter.go      # Redis token bucket via Lua script
│   └── storage/            # Data access layer
│       ├── db.go           # Postgres connection init
│       ├── queries.go      # API key lookup query
│       ├── usage.go        # Usage log insert
│       └── redis.go        # Redis connection init
├── migrations/             # SQL migration files (apply in order)
│   ├── 001_create_api_keys.sql
│   ├── 002_create_usage.sql
│   └── 003_create_cache.sql
├── scripts/
│   └── test_gateway.sh     # End-to-end smoke test suite
├── app/                    # Next.js 16 admin dashboard
│   ├── app/
│   │   ├── layout.tsx      # Root layout with DM Sans + Geist Mono fonts
│   │   ├── page.tsx        # Dashboard UI (Overview, API Keys, Usage tabs)
│   │   └── globals.css     # Design tokens and base styles
│   └── lib/
│       └── api.ts          # Typed fetch client for all admin endpoints
├── docs/
│   └── architecture.md     # Detailed architecture documentation
├── docker-compose.yml      # Postgres + pgvector for local dev
├── .env.example            # Environment variable template
├── go.mod
└── go.sum
```

---

## Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `OPENROUTER_API_KEY` | ✅ | — | Upstream OpenRouter API key |
| `DATABASE_URL` | ✅ | — | Postgres connection string |
| `GEMINI_API_KEY` | — | — | For semantic cache embeddings |
| `PORT` | — | `8080` | Gateway listen port |
| `RATE_LIMIT_PER_MINUTE` | — | `60` | Default per-key rate limit |
| `SEMANTIC_CACHE_SIMILARITY_THRESHOLD` | — | `0.80` | Cosine similarity threshold for cache hits |
| `NEXT_PUBLIC_GATEWAY_URL` | — | `http://localhost:8080` | Gateway URL for the dashboard |

---

## Testing

### Unit tests

```bash
go test ./... -v
```

Every internal package has unit tests — auth, admin, cache, proxy, ratelimit, and storage. Tests use fake `database/sql` drivers with no external dependencies.

### End-to-end smoke tests

```bash
./scripts/test_gateway.sh
```

Runs the full lifecycle: seeds test keys, starts the gateway, verifies auth failures, rate limiting, upstream proxying, and usage logging.

See `--help` for options like `--skip-upstream`, `--port`, `--rate-limit`.

---

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| **SHA-256 hashed keys** | Keys stored as hashes — a database leak doesn't expose client credentials |
| **Redis token bucket** | Lua-scripted atomic rate limiting; survives restarts, shared across instances |
| **Two-tier cache** | Exact hash match first (fast), then pgvector cosine similarity (smart) |
| **Async usage logging** | Writes don't block the response path; cost calculation happens in background |
| **Single upstream key** | Gateway pattern — clients get isolated keys, billing stays centralized |
| **No external deps for tests** | Fake SQL drivers let all tests run without Postgres/Redis |

---

## License

MIT
