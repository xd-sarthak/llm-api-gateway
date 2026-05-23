CREATE TABLE metric_events (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  category    TEXT NOT NULL,
  name        TEXT NOT NULL,
  value       DOUBLE PRECISION NOT NULL,
  unit        TEXT NOT NULL,
  labels      JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at  TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX idx_metric_events_created_at ON metric_events(created_at);
CREATE INDEX idx_metric_events_category_name ON metric_events(category, name);
CREATE INDEX idx_metric_events_labels ON metric_events USING GIN (labels);
