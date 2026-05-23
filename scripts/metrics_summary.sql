-- Historical metrics summary for the last hour.
-- Adjust the interval in the params CTE for a different reporting window.
WITH params AS (
  SELECT now() - interval '1 hour' AS since
),
latency AS (
  SELECT
    name AS layer,
    count(*) AS samples,
    percentile_cont(0.50) WITHIN GROUP (ORDER BY value) AS p50_ms,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY value) AS p95_ms,
    percentile_cont(0.99) WITHIN GROUP (ORDER BY value) AS p99_ms
  FROM metric_events, params
  WHERE category = 'latency'
    AND created_at >= params.since
  GROUP BY name
),
throughput AS (
  SELECT
    count(*)::double precision / greatest(extract(epoch FROM now() - params.since), 1) AS requests_per_second
  FROM metric_events, params
  WHERE category = 'counter'
    AND name = 'http.requests'
    AND created_at >= params.since
),
errors AS (
  SELECT
    name AS error_type,
    count(*) AS errors
  FROM metric_events, params
  WHERE category = 'error'
    AND created_at >= params.since
  GROUP BY name
),
cache AS (
  SELECT
    count(*) AS lookups,
    count(*) FILTER (WHERE labels->>'status' = 'hit') AS hits,
    count(*) FILTER (WHERE labels->>'status' = 'miss') AS misses,
    count(*) FILTER (WHERE labels->>'status' = 'error') AS errors,
    count(*) FILTER (WHERE labels->>'status' = 'hit')::double precision / nullif(count(*), 0) AS hit_rate
  FROM metric_events, params
  WHERE category = 'cache'
    AND name = 'semantic_cache_lookup'
    AND created_at >= params.since
),
business AS (
  SELECT
    name,
    sum(value) AS total
  FROM metric_events, params
  WHERE category = 'business'
    AND created_at >= params.since
  GROUP BY name
)
SELECT jsonb_pretty(jsonb_build_object(
  'window_start', (SELECT since FROM params),
  'throughput', (SELECT row_to_json(throughput) FROM throughput),
  'latency_by_layer', COALESCE((SELECT jsonb_agg(row_to_json(latency)) FROM latency), '[]'::jsonb),
  'errors_by_type', COALESCE((SELECT jsonb_agg(row_to_json(errors)) FROM errors), '[]'::jsonb),
  'cache_efficiency', (SELECT row_to_json(cache) FROM cache),
  'business_metrics', COALESCE((SELECT jsonb_object_agg(name, total) FROM business), '{}'::jsonb)
)) AS metrics_summary;
