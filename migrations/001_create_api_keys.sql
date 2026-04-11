CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS api_keys (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  key                  TEXT NOT NULL UNIQUE,
  name                 TEXT,
  rate_limit_per_minute INTEGER DEFAULT 60,
  is_active            BOOLEAN DEFAULT true,
  created_at           TIMESTAMP DEFAULT now()
);
