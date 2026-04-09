# LLM Gateway Architecture

## Purpose

This system is a small HTTP gateway that sits between clients and OpenRouter's chat completion API.

It does three things:

1. Exposes a single local endpoint: `POST /v1/chat/completions`
2. Authenticates callers using gateway-managed API keys stored in Postgres
3. Proxies validated requests to OpenRouter using one shared upstream API key

In practice, this lets you issue separate client-facing API keys without exposing the real OpenRouter key to end users.

## High-Level Design

```text
+---------+      Bearer <gateway-key>      +-------------------+
| Client  | -----------------------------> | Go HTTP Server    |
+---------+                                | Chi router        |
                                           +---------+---------+
                                                     |
                                                     v
                                           +-------------------+
                                           | Auth middleware   |
                                           | SHA-256 key hash  |
                                           | Postgres lookup   |
                                           +---------+---------+
                                                     |
                                      valid key      v
                                           +-------------------+
                                           | Proxy handler     |
                                           | Body size limit   |
                                           | Header rewrite    |
                                           +---------+---------+
                                                     |
                                                     v
                                           +-------------------+
                                           | OpenRouter API    |
                                           | chat completions  |
                                           +-------------------+
```

## Main Components

### 1. Server Bootstrap

File: `cmd/server/main.go`

Responsibilities:

- Loads environment variables from `.env`
- Validates required configuration
- Initializes the Postgres connection
- Builds the HTTP router
- Registers global middleware
- Mounts the protected chat endpoint
- Starts the HTTP server

Startup sequence:

1. Load `.env` via `godotenv`
2. Require `OPENROUTER_API_KEY`
3. Require `DATABASE_URL`
4. Call `storage.Init()`
5. Create a Chi router
6. Attach logging and panic recovery middleware
7. Register `POST /v1/chat/completions` behind `auth.RequireAPIKey`
8. Start listening on `PORT` or fallback to `8080`

### 2. Authentication Middleware

File: `internal/auth/middleware.go`

Responsibilities:

- Reads the client `Authorization` header
- Requires the format `Bearer <key>`
- Hashes the presented API key with SHA-256
- Verifies the hash against the database
- Rejects missing, malformed, unknown, or inactive keys

Important design choice:

- The database lookup uses the SHA-256 hash of the client key instead of the raw key value.
- This reduces exposure if the `api_keys` table is leaked, assuming only hashes are stored there.

Authentication flow:

1. Read `Authorization`
2. Split on the first space
3. Require the `Bearer` scheme
4. SHA-256 hash the supplied token
5. Call `storage.IsValidAPIKey(hash)`
6. Continue only if the database returns `is_active = true`

### 3. Proxy Handler

File: `internal/proxy/handler.go`

Responsibilities:

- Accepts the authenticated chat-completions request
- Caps the incoming body at 1 MiB
- Streams the request body upstream
- Replaces the client auth header with the server's OpenRouter API key
- Returns the upstream status code and body to the caller

Proxy behavior:

- Upstream URL is fixed to `https://openrouter.ai/api/v1/chat/completions`
- Request method is always `POST`
- Content type is forced to `application/json`
- Upstream timeout is 60 seconds
- The incoming request context is reused, so client cancellation propagates upstream

Response behavior:

- Copies upstream `Content-Type` if present
- Forwards upstream status code as-is
- Streams the upstream response body directly to the client

### 4. Storage Layer

Files:

- `internal/storage/db.go`
- `internal/storage/queries.go`

Responsibilities:

- Opens a global Postgres connection pool
- Checks database reachability during startup
- Exposes a query for API key validation

Current query:

```sql
SELECT is_active
FROM api_keys
WHERE key = $1
```

Behavior:

- No matching row means invalid key
- Matching row returns the `is_active` flag
- Any database error becomes a server error in middleware

## Request Lifecycle

```text
Client request
  -> Chi router
  -> Logger middleware
  -> Recoverer middleware
  -> RequireAPIKey middleware
       -> parse bearer token
       -> hash token
       -> query Postgres
       -> reject or continue
  -> HandleChat
       -> limit body size
       -> create upstream request
       -> inject OPENROUTER_API_KEY
       -> send to OpenRouter
       -> relay response
  -> Client receives upstream result
```

## Configuration

Environment variables used by the system:

- `OPENROUTER_API_KEY`: shared upstream credential used by the gateway
- `DATABASE_URL`: Postgres connection string
- `PORT`: local server port, default `8080`

Note:

- The checked-in `.env` currently contains real-looking secrets and local database credentials. In a production-grade setup, this file should not be committed.

## Data Model Assumptions

The code implies a table similar to:

```sql
CREATE TABLE api_keys (
  key TEXT PRIMARY KEY,
  is_active BOOLEAN NOT NULL DEFAULT true
);
```

Expected semantics:

- `key` stores the SHA-256 hash of the issued client API key
- `is_active` controls whether that key can authenticate

## Architectural Characteristics

### What this system does well

- Very small operational surface area
- Clear separation between auth, proxying, and storage
- Keeps the upstream provider key server-side
- Uses database-backed key revocation via `is_active`
- Preserves client cancellation through request context

### Current constraints

- Only one endpoint is supported
- Only one upstream provider is supported
- No request/response auditing
- No rate limiting or quota enforcement
- No per-key usage attribution
- No structured config object or dependency injection
- Uses a package-global database handle
- No graceful shutdown handling
- Limited header forwarding
- No retries, circuit breaking, or upstream fallback

## Design Tradeoffs

### Simplicity over extensibility

The code is intentionally direct. There are no interfaces, service abstractions, or config structs. That keeps the code easy to read, but it also means future additions will likely require refactoring rather than simple extension.

### Shared upstream identity

All authenticated client requests are executed using one `OPENROUTER_API_KEY`. This is a common gateway pattern, but it means upstream billing and provider-side attribution are shared unless the gateway adds its own accounting layer.

### Hash-based key verification

Hashing client keys before lookup is a good baseline, but the design currently assumes unsalted SHA-256 is sufficient. That may be acceptable for random high-entropy API keys, but it is still a security-sensitive choice.

## How To Study This Codebase

Recommended reading order:

1. `cmd/server/main.go`
2. `internal/auth/middleware.go`
3. `internal/storage/db.go`
4. `internal/storage/queries.go`
5. `internal/proxy/handler.go`

When reading, follow these questions:

1. Where does trust enter the system?
2. Which credentials belong to clients, and which belong to the gateway?
3. What data crosses process boundaries?
4. What failures are rejected locally versus delegated to the upstream provider?
5. What would need to change to support multiple providers or per-tenant policies?

## Suggested Next Refactors

If you want to grow this system beyond a minimal prototype, the highest-value next steps are:

1. Introduce a proper config struct and dependency wiring
2. Replace package globals with injected dependencies
3. Add database migrations and schema docs
4. Add request logging with key identifiers, not raw secrets
5. Add rate limiting and per-key usage tracking
6. Add graceful shutdown and health endpoints
7. Preserve or intentionally filter additional upstream headers
8. Add tests for auth failures, proxy failures, and body size limits
