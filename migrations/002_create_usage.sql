CREATE TABLE usage_logs (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  api_key       TEXT NOT NULL,
  model         TEXT NOT NULL,
  prompt_tokens     INT NOT NULL DEFAULT 0,
  completion_tokens INT NOT NULL DEFAULT 0,
  total_tokens      INT NOT NULL DEFAULT 0,
  latency_ms    INT NOT NULL DEFAULT 0,
  status_code   INT NOT NULL DEFAULT 200,
  created_at    TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX idx_usage_api_key ON usage_logs(api_key);
CREATE INDEX idx_usage_created_at ON usage_logs(created_at);
ALTER TABLE usage_logs ADD COLUMN cost_usd NUMERIC(10, 8) NOT NULL DEFAULT 0;