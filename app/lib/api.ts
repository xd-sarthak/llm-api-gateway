const BASE = process.env.NEXT_PUBLIC_GATEWAY_URL ?? "http://localhost:8080";

export type Stats = {
  total_requests: number;
  total_tokens: number;
  total_cost_usd: number;
  avg_latency_ms: number;
  cache_entries: number;
};

export type UsageRow = {
  api_key: string;
  key_name?: string;
  model: string;
  requests: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens: number;
  total_cost_usd: number;
  avg_latency_ms: number;
};

export type APIKey = {
  id: string;
  name: string;
  is_active: boolean;
  created_at: string;
};

export type CreatedKey = {
  id: string;
  key: string;
  note: string;
};

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${BASE}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
    cache: "no-store",
  });

  if (!response.ok) {
    let message = `Request failed with status ${response.status}`;
    try {
      const body = (await response.json()) as { error?: string };
      if (body.error) {
        message = body.error;
      }
    } catch {}
    throw new Error(message);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return response.json() as Promise<T>;
}

export function getBaseUrl() {
  return BASE;
}

export function getStats(): Promise<Stats> {
  return request<Stats>("/admin/stats", { method: "GET" });
}

export function getUsage(): Promise<UsageRow[]> {
  return request<UsageRow[]>("/admin/usage", { method: "GET" });
}

export function getKeys(): Promise<APIKey[]> {
  return request<APIKey[]>("/admin/keys", { method: "GET" });
}

export function createKey(name: string): Promise<CreatedKey> {
  return request<CreatedKey>("/admin/keys", {
    method: "POST",
    body: JSON.stringify({ name }),
  });
}

export function deactivateKey(id: string): Promise<void> {
  return request<void>(`/admin/keys/${id}`, { method: "DELETE" });
}
