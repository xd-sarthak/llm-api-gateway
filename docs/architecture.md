# Architecture

> Deep-dive into the internals of llm/gateway — how every request flows from client to upstream and back.

---

## System Overview

llm/gateway is a Go HTTP server that proxies chat completion requests to OpenRouter. It adds authentication, rate limiting, semantic caching, usage tracking, and cost attribution on top of a single upstream API key.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              llm/gateway                               │
│                                                                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐ │
│  │  Auth    │→│  Rate    │→│  Cache   │→│  Proxy   │→│  Usage   │ │
│  │Middleware│  │ Limiter  │  │ Lookup   │  │ Handler  │  │ Logger   │ │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘ │
│       │              │              │              │              │       │
│  Postgres        Redis         Postgres       OpenRouter     Postgres   │
│  (api_keys)    (buckets)   (semantic_cache)                (usage_logs) │
│                                                                         │
│  ┌──────────────────────────┐                                           │
│  │     Admin API            │  ← Next.js Dashboard                      │
│  │  /admin/stats,keys,usage │                                           │
│  └──────────────────────────┘                                           │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## Request Lifecycle

Every `POST /v1/chat/completions` request passes through this pipeline:

```
1. RECEIVE        Client sends request with Bearer token
                  │
2. AUTHENTICATE   Auth middleware extracts token
                  SHA-256 hash the raw key
                  Query api_keys WHERE key = hash
                  Reject if missing / inactive
                  Inject APIKey into request context
                  │
3. RATE LIMIT     Extract key ID from context
                  Execute Redis Lua token bucket script
                  Set X-RateLimit-Limit and X-RateLimit-Remaining headers
                  Return 429 + Retry-After if exhausted
                  │
4. CACHE CHECK    Extract last user message as prompt
                  Hash prompt → exact match in semantic_cache
                  If miss → generate embedding via Gemini API
                  pgvector cosine similarity search (threshold ≥ 0.80)
                  If hit → return cached response with X-Cache: HIT
                  │
5. PROXY          Read request body (max 1 MiB)
                  Create upstream request to OpenRouter
                  Replace Authorization with server's OPENROUTER_API_KEY
                  Forward request, relay response status + body
                  │
6. LOG (async)    Parse response for usage data (tokens)
                  Calculate cost from pricing cache
                  INSERT into usage_logs
                  If success response → store in semantic_cache
                  │
7. RESPOND        Client receives upstream response
```

---

## Component Deep-Dives

### Authentication (`internal/auth/`)

**Files:** `middleware.go`, `context.go`

The auth layer uses a zero-trust approach: the database never stores raw API keys. Clients present `Bearer sk_...` tokens, which are SHA-256 hashed before any database lookup.

```
Client token: sk_a1b2c3d4e5f6...
       ↓ SHA-256
Hash: 7ba88f38e4c9a2...
       ↓
SELECT key, is_active FROM api_keys WHERE key = $1
```

**Security properties:**
- Database leak doesn't expose usable credentials
- No salt needed — keys are high-entropy random bytes (32 bytes hex)
- Inactive keys are rejected even if the hash matches

**Context propagation:** On successful auth, the middleware injects an `APIKey` struct into the request context. Downstream handlers access it via `auth.APIKeyFromContext(ctx)`.

---

### Rate Limiting (`internal/ratelimit/`)

**Files:** `limiter.go`, `middleware.go`

Implements a **token bucket** algorithm entirely in Redis via a single Lua script — guaranteeing atomicity without distributed locks.

```lua
-- Simplified logic (actual script in limiter.go)
tokens = min(capacity, tokens + elapsed * refill_rate)
if tokens >= 1 then
    tokens = tokens - 1    -- consume
    allowed = true
else
    retry_after = ceil((1 - tokens) / refill_rate)
    allowed = false
end
```

**Properties:**
- Per-key buckets: `ratelimit:{key_hash}`
- Configurable capacity and window via `RATE_LIMIT_PER_MINUTE`
- Bucket TTL = 2× window (auto-cleanup of inactive keys)
- Precise `Retry-After` header on 429 responses
- Survives server restarts (state lives in Redis)

---

### Semantic Cache (`internal/cache/`)

**Files:** `store.go`, `embeddings.go`

Two-tier caching strategy that saves tokens on repeated or similar queries:

```
Tier 1: Exact Match
  SHA-256 hash of the user's prompt
  O(1) lookup in semantic_cache.prompt_hash
  
Tier 2: Semantic Similarity
  Generate 768-dim embedding via Gemini API
  pgvector cosine distance search across all cached embeddings
  Accept if similarity ≥ threshold (default 0.80)
```

**Cache invalidation:** Currently append-only with ON CONFLICT DO NOTHING. Identical prompts reuse the first cached response.

**Embedding model:** Gemini `gemini-embedding-001` with 768 output dimensions. The embedding call happens synchronously during cache lookup, but only on exact-match misses.

---

### Proxy Handler (`internal/proxy/`)

**Files:** `handler.go`, `pricing.go`

The core forwarding logic:

1. Read and cap incoming body at 1 MiB (`http.MaxBytesReader`)
2. Create a new request to `https://openrouter.ai/api/v1/chat/completions`
3. Replace the auth header with the server's `OPENROUTER_API_KEY`
4. Forward the request with a 60-second timeout
5. Relay the response status code, Content-Type, and body verbatim

**Cost calculation:** On startup (and every 6 hours), the gateway fetches the full model pricing table from OpenRouter's `/api/v1/models` endpoint. Per-request cost is computed as:

```
cost = prompt_tokens × prompt_price + completion_tokens × completion_price
```

**Async logging:** After relaying the response, usage data (tokens, cost, latency, status) is written to `usage_logs` in a goroutine — it never blocks the response path.

---

### Storage Layer (`internal/storage/`)

**Files:** `db.go`, `queries.go`, `usage.go`, `redis.go`

A thin data access layer. No ORM — just `database/sql` with raw queries.

| Function | Query | Used By |
|----------|-------|---------|
| `GetAPIKeyByHash` | `SELECT key, is_active FROM api_keys WHERE key = $1` | Auth middleware |
| `InsertUsageLog` | `INSERT INTO usage_logs (...)` | Proxy handler (async) |

**Global state:** `storage.DB` is a package-level `*sql.DB`. This is a pragmatic choice for a small service — the connection pool is safe for concurrent use.

---

### Admin API (`internal/admin/`)

**Files:** `handlers.go`, `routes.go`

Five endpoints for the dashboard, all registered under `/admin` via Chi subrouter:

| Handler | SQL | Notes |
|---------|-----|-------|
| `GetStats` | Aggregate COUNT, SUM, AVG over usage_logs + COUNT from semantic_cache | Single response with all dashboard numbers |
| `GetUsage` | GROUP BY api_key, model with LEFT JOIN api_keys for human-readable names | Returns `key_name` instead of raw hash |
| `ListKeys` | SELECT from api_keys ORDER BY created_at DESC | All keys with status |
| `CreateKey` | INSERT hash into api_keys, RETURNING id | Generates `sk_` prefixed key, returns plaintext once |
| `DeactivateKey` | UPDATE api_keys SET is_active = false | Soft delete by UUID |

---

### Admin Dashboard (`app/`)

**Stack:** Next.js 16, TypeScript, Tailwind CSS v4

Single-page client app with three tabs:

| Tab | Data Source | Features |
|-----|-------------|----------|
| Overview | `/admin/stats` + `/admin/usage` | Stat cards (requests, tokens, cost, cache) + usage table |
| API Keys | `/admin/keys` | Create/deactivate keys, status badges, inline form |
| Usage | `/admin/usage` | Full breakdown with prompt/completion token split |

**Design system:** Dark theme (#0a0a0a), DM Sans for UI, Geist Mono for data, accent green (#4ade80) for active states, accent blue (#60a5fa) for token counts.

---

## Data Model

### `api_keys`

```sql
id                   UUID PRIMARY KEY
key                  TEXT UNIQUE        -- SHA-256 hash of the client-facing key
name                 TEXT               -- Human-readable label
rate_limit_per_minute INTEGER           -- Per-key override (not yet enforced)
is_active            BOOLEAN            -- Soft delete flag
created_at           TIMESTAMP
```

### `usage_logs`

```sql
id                UUID PRIMARY KEY
api_key           TEXT               -- Foreign key to api_keys.key (hash)
model             TEXT               -- e.g. "openai/gpt-4o-mini"
prompt_tokens     INT
completion_tokens INT
total_tokens      INT
latency_ms        INT                -- End-to-end including upstream
status_code       INT                -- Upstream HTTP status
cost_usd          NUMERIC(10,8)      -- Calculated from pricing cache
created_at        TIMESTAMP
```

### `semantic_cache`

```sql
id           UUID PRIMARY KEY
prompt_hash  TEXT UNIQUE             -- SHA-256 of the prompt text
embedding    vector(768)             -- Gemini embedding for similarity search
prompt       TEXT                    -- Original prompt (for debugging)
response     TEXT                    -- Full upstream JSON response
model        TEXT
created_at   TIMESTAMP
```

**Index:** IVFFlat on `embedding` with 100 lists for fast approximate nearest neighbor search.

---

## Security Model

| Concern | Approach |
|---------|----------|
| Key storage | SHA-256 hashed, never stored in plaintext |
| Key generation | 32 bytes from `crypto/rand`, hex-encoded with `sk_` prefix |
| Upstream key | Server-side only, injected per-request, never exposed to clients |
| Admin endpoints | Currently unauthenticated — suitable for internal networks only |
| CORS | Permissive (`*`) for local development |

> **Production note:** Add authentication to `/admin/*` routes before exposing to the internet. Consider API key or session-based auth for the admin API.

---

## Testing Strategy

| Layer | Approach | External Deps |
|-------|----------|--------------|
| Unit tests | Fake `database/sql` drivers per package | None |
| Smoke tests | `scripts/test_gateway.sh` — full lifecycle | Postgres, Redis |
| Manual | cURL against running gateway | All |

Every internal package has `_test.go` files. The fake driver pattern lets tests record and verify SQL queries without touching a real database.

```bash
# Unit tests (no deps)
go test ./... -v

# Smoke tests (needs Postgres + Redis)
./scripts/test_gateway.sh

# Skip upstream calls
./scripts/test_gateway.sh --skip-upstream
```

---

## Future Considerations

- Per-key rate limit overrides (column exists, not yet wired)
- Admin API authentication
- Streaming response support (SSE)
- Multi-provider routing (Anthropic, Google, etc.)
- Graceful shutdown with in-flight request draining
- Structured logging (JSON)
- Cache TTL and eviction policies
- Prometheus metrics endpoint
