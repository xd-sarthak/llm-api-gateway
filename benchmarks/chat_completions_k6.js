import http from "k6/http";
import { check, sleep } from "k6";

export const options = {
  scenarios: {
    happy_path: {
      executor: "constant-arrival-rate",
      rate: Number(__ENV.RATE || 10),
      timeUnit: "1s",
      duration: __ENV.DURATION || "1m",
      preAllocatedVUs: Number(__ENV.VUS || 20),
      maxVUs: Number(__ENV.MAX_VUS || 100),
    },
  },
  thresholds: {
    http_req_duration: ["p(50)<5000", "p(99)<30000"],
    http_req_failed: ["rate<0.05"],
  },
};

const baseURL = __ENV.BASE_URL || "http://localhost:8080";
const apiKey = __ENV.API_KEY;
const model = __ENV.MODEL || "openai/gpt-4o-mini";

export default function () {
  if (!apiKey) {
    throw new Error("API_KEY is required");
  }

  const payload = JSON.stringify({
    model,
    messages: [
      { role: "system", content: "Answer concisely." },
      { role: "user", content: `Say hello from k6 request ${__VU}-${__ITER}.` },
    ],
  });

  const res = http.post(`${baseURL}/v1/chat/completions`, payload, {
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json",
    },
    tags: {
      endpoint: "chat_completions",
      model,
    },
  });

  check(res, {
    "status is 2xx": (r) => r.status >= 200 && r.status < 300,
    "has body": (r) => r.body && r.body.length > 0,
  });

  sleep(0.1);
}

export function handleSummary(data) {
  const metricValue = (name, key) => data.metrics[name]?.values?.[key] || 0;
  const p50 = metricValue("http_req_duration", "p(50)") || metricValue("http_req_duration", "med");

  return {
    stdout: [
      `requests=${metricValue("http_reqs", "count")}`,
      `rps=${metricValue("http_reqs", "rate")}`,
      `p50_ms=${p50}`,
      `p99_ms=${metricValue("http_req_duration", "p(99)")}`,
      `failed_rate=${metricValue("http_req_failed", "rate")}`,
      `checks_rate=${metricValue("checks", "rate")}`,
      "",
    ].join("\n"),
  };
}
