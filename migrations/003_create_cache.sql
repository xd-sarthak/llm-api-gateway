
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE semantic_cache (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  prompt_hash  TEXT NOT NULL UNIQUE,
  embedding    vector(768),
  prompt       TEXT NOT NULL,
  response     TEXT NOT NULL,
  model        TEXT NOT NULL,
  created_at   TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX idx_cache_embedding ON semantic_cache 
USING ivfflat (embedding vector_cosine_ops)
WITH (lists = 100);