/**
 * ============================================================================
 *  LLM API Gateway — Production Workload Simulator (k6)
 * ============================================================================
 *
 *  This is NOT a toy benchmark. It simulates realistic production traffic
 *  against an OpenAI-compatible LLM API Gateway that implements:
 *
 *    • API key authentication (Bearer token)
 *    • Redis token-bucket rate limiting
 *    • Exact prompt cache (SHA-256 hash match)
 *    • Semantic cache (pgvector cosine similarity)
 *    • Upstream provider fallback (OpenRouter)
 *    • Prometheus metrics exposition
 *
 *  Traffic mix (configurable):
 *    70%  — Exact cache hits   (identical prompts from a fixed pool)
 *    20%  — Semantic cache hits (paraphrased prompts that should match)
 *    10%  — Cache misses        (unique prompts that force upstream calls)
 *
 * ============================================================================
 *  EXECUTOR DESIGN NOTES
 * ============================================================================
 *
 *  We use `ramping-arrival-rate` for all scenarios. Why?
 *
 *  VU-based executors (e.g. ramping-vus) model "concurrent users" — each VU
 *  loops as fast as it can. This means throughput depends on response time:
 *  if the server slows down, fewer requests are sent. That hides latency
 *  degradation behind reduced load.
 *
 *  Arrival-rate executors decouple throughput from response time. They
 *  schedule iterations at a fixed rate regardless of how long each takes.
 *  If the server slows down, k6 allocates more VUs to maintain the target
 *  RPS. This exposes:
 *
 *    • Latency degradation under constant load
 *    • Concurrency scaling behavior (VU pool exhaustion)
 *    • Queue depth / backpressure characteristics
 *    • Upstream provider slowdown impact
 *
 *  This is essential for infrastructure-level performance testing where you
 *  need to answer "what happens at exactly N req/s?" rather than "how fast
 *  can N users hammer the server?"
 *
 * ============================================================================
 *  SEMANTIC CACHE TESTING
 * ============================================================================
 *
 *  The gateway computes embeddings for prompts and stores them in pgvector.
 *  On lookup, it finds the nearest neighbor and checks if cosine similarity
 *  exceeds a threshold (default 0.80).
 *
 *  To test this, we maintain a pool of "semantic variation" prompts — these
 *  are paraphrases of prompts that should already be cached. For example:
 *
 *    Base:      "What is the capital of France?"
 *    Variation: "Tell me the capital city of France"
 *
 *  These hit the embedding pipeline (Gemini API call + pgvector query) but
 *  should return cached responses. This lets us measure:
 *
 *    • Embedding generation latency under load
 *    • pgvector nearest-neighbor query performance
 *    • Similarity threshold accuracy
 *    • Whether semantic hits actually save upstream calls
 *
 * ============================================================================
 */

import http from "k6/http";
import { check, sleep, fail } from "k6";
import { Counter, Rate, Trend } from "k6/metrics";

// ============================================================================
//  CUSTOM METRICS
// ============================================================================
//
//  These track request categories independently of k6 built-in metrics so we
//  can correlate cache behavior with latency/error distributions.

/** Counts requests that should hit the exact cache (identical prompt hash). */
const exactCacheRequests = new Counter("exact_cache_requests");

/** Counts requests that should hit the semantic cache (paraphrased prompts). */
const semanticCacheRequests = new Counter("semantic_cache_requests");

/** Counts requests that should miss all caches (unique prompts). */
const cacheMissRequests = new Counter("cache_miss_requests");

/** Counts requests where the gateway falls back to a secondary provider. */
const providerFallbackRequests = new Counter("provider_fallback_requests");

/** Tracks the overall request failure rate across all traffic types. */
const requestFailureRate = new Rate("request_failure_rate");

/** End-to-end latency trend for exact-cache requests. */
const exactCacheLatency = new Trend("exact_cache_latency", true);

/** End-to-end latency trend for semantic-cache requests. */
const semanticCacheLatency = new Trend("semantic_cache_latency", true);

/** End-to-end latency trend for cache-miss requests. */
const cacheMissLatency = new Trend("cache_miss_latency", true);

// ============================================================================
//  ENVIRONMENT CONFIGURATION
// ============================================================================

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const API_KEY = __ENV.API_KEY;
const MODEL = __ENV.MODEL || "openai/gpt-4o-mini";

// Scenario tuning knobs — all have sane defaults for local testing.
const START_RATE = Number(__ENV.START_RATE || 5);
const MAX_RATE = Number(__ENV.MAX_RATE || 50);
const DURATION = __ENV.DURATION || "2m";
const PREALLOCATED_VUS = Number(__ENV.PREALLOCATED_VUS || 20);
const MAX_VUS = Number(__ENV.MAX_VUS || 200);

// Traffic distribution weights (must sum to 100).
const EXACT_CACHE_PCT = Number(__ENV.EXACT_CACHE_PCT || 70);
const SEMANTIC_CACHE_PCT = Number(__ENV.SEMANTIC_CACHE_PCT || 20);
// Remainder is cache miss.

// ============================================================================
//  PROMPT POOLS
// ============================================================================

/**
 * Exact-cache pool — these prompts are sent verbatim and repeatedly.
 * After the first request caches the response, subsequent requests with
 * the same SHA-256 hash hit the exact-match path (no embedding needed).
 */
const EXACT_CACHE_PROMPTS = [
  "What is the capital of France?",
  "Explain photosynthesis in simple terms.",
  "What are the three laws of thermodynamics?",
  "How does TCP/IP work?",
  "What is the difference between a stack and a queue?",
  "Summarize the theory of relativity.",
  "What causes tides in the ocean?",
  "Explain how a neural network learns.",
  "What is the Pythagorean theorem?",
  "Describe the water cycle.",
  "What is the speed of light in a vacuum?",
  "How do vaccines work?",
  "What is the difference between HTTP and HTTPS?",
  "Explain the concept of recursion in programming.",
  "What are the primary colors of light?",
  "How does a compiler differ from an interpreter?",
  "What is the Big Bang theory?",
  "Explain supply and demand in economics.",
  "What is DNA and what does it do?",
  "How does encryption work?",
];

/**
 * Semantic-variation pool — paraphrases of exact-cache prompts.
 *
 * Each entry maps to a prompt in EXACT_CACHE_PROMPTS but with different
 * wording. The gateway should:
 *   1. Miss the exact cache (different SHA-256 hash)
 *   2. Generate an embedding via the Gemini API
 *   3. Find a cosine-similar neighbor in pgvector
 *   4. Return the cached response if similarity >= threshold
 *
 * This is the most expensive cache path and the primary bottleneck we
 * want to stress-test.
 */
const SEMANTIC_VARIATION_PROMPTS = [
  "Tell me the capital city of France.",
  "Can you explain photosynthesis simply?",
  "List the three thermodynamics laws.",
  "Describe how the TCP/IP protocol functions.",
  "What distinguishes a stack from a queue in data structures?",
  "Give me a brief summary of Einstein's relativity.",
  "Why do ocean tides happen?",
  "How does a neural network train itself?",
  "State the Pythagorean theorem and what it means.",
  "Explain the water cycle process.",
  "How fast does light travel in a vacuum?",
  "How do vaccines protect the body?",
  "What's the distinction between HTTP and HTTPS?",
  "Define recursion as used in computer science.",
  "What are the primary colors in the light spectrum?",
  "Compare compilers and interpreters.",
  "Describe the Big Bang cosmological theory.",
  "How do supply and demand affect prices?",
  "What role does DNA play in biology?",
  "How does modern encryption protect data?",
];

// ============================================================================
//  PROMPT GENERATION HELPERS
// ============================================================================

/**
 * Generates a unique prompt guaranteed to miss all caches.
 * Uses __ITER (iteration counter) and __VU (virtual user ID) plus a
 * timestamp to guarantee global uniqueness across all VUs and runs.
 */
function generateUniqueMissPrompt(vu, iter) {
  const topics = [
    "quantum entanglement", "black hole thermodynamics",
    "CRISPR gene editing applications", "post-quantum cryptography",
    "neuromorphic computing architectures", "dark matter detection",
    "topological insulators", "RNA interference mechanisms",
    "zero-knowledge proof systems", "magnetohydrodynamics",
  ];
  const topic = topics[iter % topics.length];
  const ts = Date.now();
  return `Explain ${topic} with focus on recent ${ts} developments ` +
    `in context ${vu}-${iter}. Include specific numerical data.`;
}

/**
 * Selects a traffic type based on configured distribution weights.
 * Returns: "exact" | "semantic" | "miss"
 */
function selectTrafficType() {
  const roll = Math.random() * 100;
  if (roll < EXACT_CACHE_PCT) return "exact";
  if (roll < EXACT_CACHE_PCT + SEMANTIC_CACHE_PCT) return "semantic";
  return "miss";
}

/**
 * Picks a prompt based on the traffic type.
 */
function getPromptForType(type, vu, iter) {
  switch (type) {
    case "exact":
      return EXACT_CACHE_PROMPTS[iter % EXACT_CACHE_PROMPTS.length];
    case "semantic":
      return SEMANTIC_VARIATION_PROMPTS[iter % SEMANTIC_VARIATION_PROMPTS.length];
    case "miss":
      return generateUniqueMissPrompt(vu, iter);
    default:
      return EXACT_CACHE_PROMPTS[0];
  }
}

// ============================================================================
//  REQUEST BUILDER
// ============================================================================

/**
 * Builds an OpenAI-compatible chat completion request payload.
 *
 * Payload shape matches:
 *   POST /v1/chat/completions
 *   {
 *     "model": MODEL,
 *     "messages": [{ "role": "user", "content": PROMPT }]
 *   }
 */
function buildChatPayload(prompt) {
  return JSON.stringify({
    model: MODEL,
    messages: [{ role: "user", content: prompt }],
  });
}

/**
 * Returns the standard headers for an authenticated gateway request.
 */
function buildHeaders() {
  return {
    Authorization: `Bearer ${API_KEY}`,
    "Content-Type": "application/json",
  };
}

// ============================================================================
//  SCENARIO HANDLER (shared default function)
// ============================================================================

/**
 * Core iteration function used by all scenarios.
 *
 * Each iteration:
 *   1. Selects a traffic type (exact / semantic / miss)
 *   2. Picks the appropriate prompt
 *   3. Sends the request to the gateway
 *   4. Validates the response structure
 *   5. Records custom metrics by traffic type
 *   6. Detects provider fallback from response headers
 */
export default function () {
  if (!API_KEY) {
    fail("API_KEY environment variable is required. " +
      "Set it with: k6 run -e API_KEY=your_key ...");
  }

  const trafficType = selectTrafficType();
  const prompt = getPromptForType(trafficType, __VU, __ITER);
  const payload = buildChatPayload(prompt);

  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, {
    headers: buildHeaders(),
    tags: {
      endpoint: "chat_completions",
      traffic_type: trafficType,
      model: MODEL,
    },
    timeout: "60s",
  });

  // ---- Record per-type metrics ----
  const duration = res.timings.duration;
  switch (trafficType) {
    case "exact":
      exactCacheRequests.add(1);
      exactCacheLatency.add(duration);
      break;
    case "semantic":
      semanticCacheRequests.add(1);
      semanticCacheLatency.add(duration);
      break;
    case "miss":
      cacheMissRequests.add(1);
      cacheMissLatency.add(duration);
      break;
  }

  // ---- Detect provider fallback ----
  // The gateway sets X-Cache: HIT on cache hits. If we expected a cache hit
  // but got a miss, the request went upstream — possibly to a fallback provider.
  const cacheHeader = res.headers["X-Cache"] || "";
  if (trafficType !== "miss" && cacheHeader !== "HIT" && res.status === 200) {
    providerFallbackRequests.add(1);
  }

  // ---- Response validation ----
  const isSuccess = res.status === 200;
  requestFailureRate.add(!isSuccess);

  let parsed = null;
  let parseOk = false;
  try {
    if (res.body && res.body.length > 0) {
      parsed = JSON.parse(res.body);
      parseOk = true;
    }
  } catch (_) {
    // JSON parse failed — recorded in checks below.
  }

  check(res, {
    "status is 200": (r) => r.status === 200,
    "response body is not empty": (r) => r.body && r.body.length > 0,
    "JSON parse succeeds": () => parseOk,
    "response has model field": () => parsed && typeof parsed.model === "string",
    "response has choices array": () =>
      parsed && Array.isArray(parsed.choices) && parsed.choices.length > 0,
    "response has usage object": () =>
      parsed && parsed.usage && typeof parsed.usage.total_tokens === "number",
  });

  // Brief pause to avoid hammering beyond the arrival rate (the executor
  // controls actual RPS — this just yields the VU back to the pool).
  sleep(0.05);
}

// ============================================================================
//  K6 OPTIONS — SCENARIOS & THRESHOLDS
// ============================================================================
//
//  Four scenarios covering the full testing spectrum:
//
//    smoke_test   — Low RPS sanity check. Catches configuration errors,
//                   auth failures, and basic connectivity issues before
//                   committing to a full load test.
//
//    load_test    — Ramps from START_RATE to MAX_RATE over DURATION.
//                   The primary scenario for measuring steady-state
//                   performance and cache effectiveness.
//
//    stress_test  — Pushes to 2× MAX_RATE to find breaking points.
//                   Exposes rate-limiter behavior, connection pool
//                   exhaustion, and upstream timeout cascades.
//
//    spike_test   — Sudden burst to 3× MAX_RATE then immediate drop.
//                   Tests recovery behavior, queue drain time, and
//                   whether the gateway sheds load gracefully.

export const options = {
  scenarios: {
    smoke_test: {
      executor: "ramping-arrival-rate",
      startRate: Math.max(1, Math.floor(START_RATE / 5)),
      timeUnit: "1s",
      preAllocatedVUs: Math.max(5, Math.floor(PREALLOCATED_VUS / 4)),
      maxVUs: Math.max(10, Math.floor(MAX_VUS / 4)),
      stages: [
        { duration: "10s", target: Math.max(1, Math.floor(START_RATE / 5)) },
        { duration: "20s", target: Math.max(2, Math.floor(START_RATE / 2)) },
        { duration: "10s", target: 0 },
      ],
      tags: { scenario: "smoke_test" },
      env: { SCENARIO: "smoke_test" },
    },

    load_test: {
      executor: "ramping-arrival-rate",
      startRate: START_RATE,
      timeUnit: "1s",
      preAllocatedVUs: PREALLOCATED_VUS,
      maxVUs: MAX_VUS,
      stages: [
        { duration: "30s", target: START_RATE },            // warm-up
        { duration: DURATION, target: MAX_RATE },           // ramp to peak
        { duration: "30s", target: Math.floor(MAX_RATE * 0.8) }, // sustained
        { duration: "20s", target: 0 },                     // cool-down
      ],
      startTime: "45s", // starts after smoke_test completes
      tags: { scenario: "load_test" },
      env: { SCENARIO: "load_test" },
    },

    stress_test: {
      executor: "ramping-arrival-rate",
      startRate: MAX_RATE,
      timeUnit: "1s",
      preAllocatedVUs: PREALLOCATED_VUS * 2,
      maxVUs: MAX_VUS * 2,
      stages: [
        { duration: "20s", target: MAX_RATE },              // baseline
        { duration: "40s", target: MAX_RATE * 2 },          // push to 2×
        { duration: "30s", target: MAX_RATE * 2 },          // sustain stress
        { duration: "20s", target: 0 },                     // recovery
      ],
      startTime: `${45 + 30 + parseDurationSeconds(DURATION) + 30 + 20 + 10}s`,
      tags: { scenario: "stress_test" },
      env: { SCENARIO: "stress_test" },
    },

    spike_test: {
      executor: "ramping-arrival-rate",
      startRate: START_RATE,
      timeUnit: "1s",
      preAllocatedVUs: PREALLOCATED_VUS * 3,
      maxVUs: MAX_VUS * 3,
      stages: [
        { duration: "10s", target: START_RATE },            // calm before storm
        { duration: "5s", target: MAX_RATE * 3 },           // instant spike
        { duration: "15s", target: MAX_RATE * 3 },          // hold spike
        { duration: "5s", target: START_RATE },              // instant drop
        { duration: "20s", target: START_RATE },             // observe recovery
        { duration: "10s", target: 0 },                      // drain
      ],
      startTime: `${45 + 30 + parseDurationSeconds(DURATION) + 30 + 20 + 10 + 20 + 40 + 30 + 20 + 10}s`,
      tags: { scenario: "spike_test" },
      env: { SCENARIO: "spike_test" },
    },
  },

  thresholds: {
    // ---- Latency SLOs ----
    http_req_duration: [
      "p(50)<3000",   // p50 under 3s
      "p(95)<10000",  // p95 under 10s — accounts for upstream LLM latency
      "p(99)<25000",  // p99 under 25s — hard ceiling
    ],

    // ---- Reliability SLOs ----
    http_req_failed: ["rate<0.05"],           // <5% transport-level failures
    request_failure_rate: ["rate<0.10"],       // <10% application-level failures
    checks: ["rate>0.90"],                     // >90% of all checks must pass

    // ---- Per-type latency SLOs ----
    // Exact cache should be fast (no embedding, no upstream).
    exact_cache_latency: ["p(95)<500"],
    // Semantic cache includes embedding generation — allow more headroom.
    semantic_cache_latency: ["p(95)<5000"],
    // Cache miss goes upstream — bounded by LLM response time.
    cache_miss_latency: ["p(95)<15000"],
  },
};

// ============================================================================
//  CUSTOM SUMMARY
// ============================================================================

export function handleSummary(data) {
  const m = (name, key) => data.metrics[name]?.values?.[key] ?? "N/A";
  const fmt = (v) => (typeof v === "number" ? v.toFixed(2) : v);

  const divider = "═".repeat(68);
  const thinDiv = "─".repeat(68);

  const lines = [
    "",
    divider,
    "  LLM GATEWAY — WORKLOAD SIMULATION RESULTS",
    divider,
    "",
    "  THROUGHPUT",
    thinDiv,
    `  Total requests ......... ${fmt(m("http_reqs", "count"))}`,
    `  Requests/sec (avg) ..... ${fmt(m("http_reqs", "rate"))}`,
    `  Iteration duration ..... ${fmt(m("iteration_duration", "avg"))} ms (avg)`,
    "",
    "  LATENCY (ms)",
    thinDiv,
    `  p50 .................... ${fmt(m("http_req_duration", "med"))}`,
    `  p95 .................... ${fmt(m("http_req_duration", "p(95)"))}`,
    `  p99 .................... ${fmt(m("http_req_duration", "p(99)"))}`,
    `  max .................... ${fmt(m("http_req_duration", "max"))}`,
    "",
    "  LATENCY BY TRAFFIC TYPE (ms, p95)",
    thinDiv,
    `  Exact cache ............ ${fmt(m("exact_cache_latency", "p(95)"))}`,
    `  Semantic cache ......... ${fmt(m("semantic_cache_latency", "p(95)"))}`,
    `  Cache miss ............. ${fmt(m("cache_miss_latency", "p(95)"))}`,
    "",
    "  TRAFFIC DISTRIBUTION",
    thinDiv,
    `  Exact cache requests ... ${fmt(m("exact_cache_requests", "count"))}`,
    `  Semantic cache reqs .... ${fmt(m("semantic_cache_requests", "count"))}`,
    `  Cache miss requests .... ${fmt(m("cache_miss_requests", "count"))}`,
    `  Provider fallbacks ..... ${fmt(m("provider_fallback_requests", "count"))}`,
    "",
    "  RELIABILITY",
    thinDiv,
    `  HTTP failure rate ...... ${fmt(m("http_req_failed", "rate"))}`,
    `  App failure rate ....... ${fmt(m("request_failure_rate", "rate"))}`,
    `  Checks pass rate ....... ${fmt(m("checks", "rate"))}`,
    "",
    divider,
    "",
  ];

  return {
    stdout: lines.join("\n"),
  };
}

// ============================================================================
//  UTILITY
// ============================================================================

/**
 * Parses a k6-style duration string (e.g. "2m", "30s", "1h") into seconds.
 * Used to compute scenario start offsets so they run sequentially.
 */
function parseDurationSeconds(d) {
  const match = String(d).match(/^(\d+)(s|m|h)$/);
  if (!match) return 120; // default 2m
  const val = parseInt(match[1], 10);
  switch (match[2]) {
    case "s": return val;
    case "m": return val * 60;
    case "h": return val * 3600;
    default:  return val;
  }
}
