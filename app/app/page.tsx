"use client";

import { useEffect, useState, useCallback } from "react";
import {
  getStats,
  getUsage,
  getKeys,
  createKey,
  deactivateKey,
  getBaseUrl,
  type Stats,
  type UsageRow,
  type APIKey,
  type CreatedKey,
} from "@/lib/api";

type Tab = "overview" | "keys" | "usage";

/* ------------------------------------------------------------------ */
/*  Number formatting helpers                                          */
/* ------------------------------------------------------------------ */
function fmtInt(n: number) {
  return n.toLocaleString();
}
function fmtCost(n: number) {
  return `$${n.toFixed(6)}`;
}
function fmtLatency(n: number) {
  return `${Math.round(n)}ms`;
}
function displayKey(row: UsageRow) {
  return row.key_name || truncateKey(row.api_key);
}
function truncateKey(k: string) {
  if (k.length <= 16) return k;
  return `${k.slice(0, 8)}...${k.slice(-5)}`;
}
function fmtDate(iso: string) {
  return new Date(iso).toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}
function fmtTokens(n: number | undefined | null) {
  if (n == null || n === 0) return "—";
  return fmtInt(n);
}

/* ------------------------------------------------------------------ */
/*  Main Dashboard                                                     */
/* ------------------------------------------------------------------ */
export default function Dashboard() {
  const [tab, setTab] = useState<Tab>("overview");

  const [stats, setStats] = useState<Stats | null>(null);
  const [usage, setUsage] = useState<UsageRow[]>([]);
  const [keys, setKeys] = useState<APIKey[]>([]);

  const [showForm, setShowForm] = useState(false);
  const [keyName, setKeyName] = useState("");
  const [newKey, setNewKey] = useState<CreatedKey | null>(null);
  const [loading, setLoading] = useState(false);

  const refresh = useCallback(async () => {
    const [s, u, k] = await Promise.allSettled([
      getStats(),
      getUsage(),
      getKeys(),
    ]);
    if (s.status === "fulfilled") setStats(s.value);
    if (u.status === "fulfilled") setUsage(u.value);
    if (k.status === "fulfilled") setKeys(k.value);
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const handleCreate = async () => {
    if (!keyName.trim()) return;
    setLoading(true);
    try {
      const created = await createKey(keyName.trim());
      setNewKey(created);
      setKeyName("");
      setShowForm(false);
      refresh();
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const handleDeactivate = async (id: string) => {
    try {
      await deactivateKey(id);
      refresh();
    } catch (e) {
      console.error(e);
    }
  };

  const baseUrl = getBaseUrl().replace(/^https?:\/\//, "");

  const tabs: { key: Tab; label: string }[] = [
    { key: "overview", label: "Overview" },
    { key: "keys", label: "API keys" },
    { key: "usage", label: "Usage" },
  ];

  return (
    <div
      className="flex flex-col min-h-screen font-sans"
      style={{ background: "#0a0a0a", color: "#e8e8e8" }}
    >
      {/* ====== Top bar ====== */}
      <header
        className="flex items-center justify-between px-6"
        style={{
          height: 56,
          borderBottom: "0.5px solid #1e1e1e",
        }}
      >
        <span className="font-mono text-[15px] tracking-tight">
          llm<span style={{ color: "#4ade80" }}>/</span>gateway
        </span>

        <nav className="flex gap-1">
          {tabs.map((t) => (
            <button
              key={t.key}
              onClick={() => setTab(t.key)}
              className="cursor-pointer transition-colors"
              style={{
                padding: "6px 16px",
                borderRadius: 6,
                fontSize: 13,
                background: tab === t.key ? "#111111" : "transparent",
                color: tab === t.key ? "#e8e8e8" : "#555555",
                border:
                  tab === t.key ? "0.5px solid #1e1e1e" : "0.5px solid transparent",
              }}
            >
              {t.label}
            </button>
          ))}
        </nav>

        <span className="font-mono" style={{ fontSize: 12, color: "#555555" }}>
          {baseUrl}
        </span>
      </header>

      {/* ====== Body ====== */}
      <main style={{ padding: 24 }}>
        {tab === "overview" && <OverviewTab stats={stats} usage={usage} />}
        {tab === "keys" && (
          <KeysTab
            keys={keys}
            showForm={showForm}
            setShowForm={setShowForm}
            keyName={keyName}
            setKeyName={setKeyName}
            newKey={newKey}
            setNewKey={setNewKey}
            loading={loading}
            handleCreate={handleCreate}
            handleDeactivate={handleDeactivate}
          />
        )}
        {tab === "usage" && <UsageTab usage={usage} />}
      </main>
    </div>
  );
}

/* ================================================================== */
/*  OVERVIEW TAB                                                       */
/* ================================================================== */
function OverviewTab({
  stats,
  usage,
}: {
  stats: Stats | null;
  usage: UsageRow[];
}) {
  return (
    <>
      {/* Stat cards */}
      <div
        className="grid gap-4"
        style={{ gridTemplateColumns: "repeat(4, 1fr)", marginBottom: 32 }}
      >
        <StatCard
          label="Total Requests"
          value={stats ? fmtInt(stats.total_requests) : "—"}
          subtitle="all time"
        />
        <StatCard
          label="Total Tokens"
          value={stats ? fmtInt(stats.total_tokens) : "—"}
          subtitle="prompt + completion"
          valueColor="#60a5fa"
        />
        <StatCard
          label="Total Cost"
          value={stats ? fmtCost(stats.total_cost_usd) : "—"}
          subtitle="USD"
        />
        <StatCard
          label="Cache Entries"
          value={stats ? fmtInt(stats.cache_entries) : "—"}
          subtitle="semantic cache"
          valueColor="#4ade80"
        />
      </div>

      {/* Usage table */}
      <p
        className="font-sans"
        style={{
          fontSize: 11,
          textTransform: "uppercase",
          letterSpacing: "0.08em",
          color: "#555555",
          marginBottom: 12,
        }}
      >
        Recent Usage
      </p>
      <div
        style={{
          background: "#111111",
          border: "0.5px solid #1e1e1e",
          borderRadius: 10,
          overflowX: "auto",
        }}
      >
        <table
          className="font-sans"
          style={{ width: "100%", fontSize: 13, borderCollapse: "collapse" }}
        >
          <thead>
            <tr style={{ borderBottom: "0.5px solid #1e1e1e" }}>
              <Th>API Key</Th>
              <Th>Model</Th>
              <Th align="right">Requests</Th>
              <Th align="right">Tokens</Th>
              <Th align="right">Avg Latency</Th>
              <Th align="right">Cost</Th>
            </tr>
          </thead>
          <tbody>
            {usage.map((row, i) => (
              <tr
                key={i}
                className="table-row-hover"
              >
                <Td mono>{displayKey(row)}</Td>
                <Td mono muted>
                  {row.model}
                </Td>
                <Td align="right">{fmtInt(row.requests)}</Td>
                <Td align="right" mono style={{ color: "#60a5fa" }}>
                  {fmtTokens(row.total_tokens)}
                </Td>
                <Td align="right" mono>
                  {fmtLatency(row.avg_latency_ms)}
                </Td>
                <Td align="right" mono muted>
                  {fmtCost(row.total_cost_usd)}
                </Td>
              </tr>
            ))}
            {usage.length === 0 && (
              <tr>
                <td
                  colSpan={6}
                  style={{
                    textAlign: "center",
                    padding: "40px 16px",
                    color: "#555555",
                    fontSize: 13,
                  }}
                >
                  No usage data yet
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </>
  );
}

/* ================================================================== */
/*  API KEYS TAB                                                       */
/* ================================================================== */
function KeysTab({
  keys,
  showForm,
  setShowForm,
  keyName,
  setKeyName,
  newKey,
  setNewKey,
  loading,
  handleCreate,
  handleDeactivate,
}: {
  keys: APIKey[];
  showForm: boolean;
  setShowForm: (v: boolean) => void;
  keyName: string;
  setKeyName: (v: string) => void;
  newKey: CreatedKey | null;
  setNewKey: (v: CreatedKey | null) => void;
  loading: boolean;
  handleCreate: () => void;
  handleDeactivate: (id: string) => void;
}) {
  return (
    <>
      {/* Header */}
      <div
        className="flex items-center justify-between"
        style={{ marginBottom: 16 }}
      >
        <p
          className="font-sans"
          style={{
            fontSize: 11,
            textTransform: "uppercase",
            letterSpacing: "0.08em",
            color: "#555555",
          }}
        >
          API Keys
        </p>
        {!showForm && (
          <button
            onClick={() => {
              setShowForm(true);
              setNewKey(null);
            }}
            className="cursor-pointer"
            style={{
              padding: "6px 16px",
              borderRadius: 6,
              fontSize: 13,
              fontWeight: 500,
              background: "#4ade80",
              color: "#0a0a0a",
              border: "none",
            }}
          >
            + New key
          </button>
        )}
      </div>

      {/* Inline create form */}
      {showForm && (
        <div
          className="flex items-center gap-3"
          style={{
            background: "#111111",
            border: "0.5px solid #1e1e1e",
            borderRadius: 10,
            padding: 16,
            marginBottom: 16,
          }}
        >
          <input
            type="text"
            placeholder="Key name"
            value={keyName}
            onChange={(e) => setKeyName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && handleCreate()}
            autoFocus
            style={{
              flex: 1,
              background: "#0a0a0a",
              border: "0.5px solid #1e1e1e",
              borderRadius: 6,
              padding: "6px 12px",
              fontSize: 13,
              color: "#e8e8e8",
              outline: "none",
            }}
          />
          <button
            onClick={handleCreate}
            disabled={loading || !keyName.trim()}
            className="cursor-pointer"
            style={{
              padding: "6px 16px",
              borderRadius: 6,
              fontSize: 13,
              fontWeight: 500,
              background: "#4ade80",
              color: "#0a0a0a",
              border: "none",
              opacity: loading || !keyName.trim() ? 0.4 : 1,
            }}
          >
            {loading ? "Creating..." : "Create"}
          </button>
          <button
            onClick={() => {
              setShowForm(false);
              setKeyName("");
            }}
            className="cursor-pointer"
            style={{
              padding: "6px 16px",
              borderRadius: 6,
              fontSize: 13,
              color: "#555555",
              background: "transparent",
              border: "none",
            }}
          >
            Cancel
          </button>
        </div>
      )}

      {/* Newly created key banner */}
      {newKey && (
        <div
          style={{
            background: "rgba(74, 222, 128, 0.1)",
            border: "0.5px solid rgba(74, 222, 128, 0.3)",
            borderRadius: 10,
            padding: 16,
            marginBottom: 16,
          }}
        >
          <p
            className="font-mono"
            style={{
              color: "#4ade80",
              fontSize: 14,
              marginBottom: 4,
              wordBreak: "break-all",
            }}
          >
            {newKey.key}
          </p>
          <p style={{ color: "rgba(74, 222, 128, 0.7)", fontSize: 11 }}>
            Copy this key — it won&apos;t be shown again
          </p>
        </div>
      )}

      {/* Keys table */}
      <div
        style={{
          background: "#111111",
          border: "0.5px solid #1e1e1e",
          borderRadius: 10,
          overflowX: "auto",
        }}
      >
        <table
          className="font-sans"
          style={{ width: "100%", fontSize: 13, borderCollapse: "collapse" }}
        >
          <thead>
            <tr style={{ borderBottom: "0.5px solid #1e1e1e" }}>
              <Th>Name</Th>
              <Th>ID</Th>
              <Th>Status</Th>
              <Th>Created</Th>
              <Th align="right">Action</Th>
            </tr>
          </thead>
          <tbody>
            {keys.map((k) => (
              <tr key={k.id} className="table-row-hover">
                <Td>
                  <span style={{ fontWeight: 500 }}>{k.name}</span>
                </Td>
                <Td mono muted>
                  {truncateKey(k.id)}
                </Td>
                <Td>
                  <StatusBadge active={k.is_active} />
                </Td>
                <Td muted>{fmtDate(k.created_at)}</Td>
                <Td align="right">
                  {k.is_active && (
                    <button
                      onClick={() => handleDeactivate(k.id)}
                      className="cursor-pointer"
                      style={{
                        padding: "4px 12px",
                        borderRadius: 6,
                        fontSize: 11,
                        color: "#f87171",
                        border: "0.5px solid rgba(248, 113, 113, 0.3)",
                        background: "transparent",
                      }}
                    >
                      Deactivate
                    </button>
                  )}
                </Td>
              </tr>
            ))}
            {keys.length === 0 && (
              <tr>
                <td
                  colSpan={5}
                  style={{
                    textAlign: "center",
                    padding: "40px 16px",
                    color: "#555555",
                    fontSize: 13,
                  }}
                >
                  No API keys yet
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </>
  );
}

/* ================================================================== */
/*  USAGE TAB                                                          */
/* ================================================================== */
function UsageTab({ usage }: { usage: UsageRow[] }) {
  return (
    <>
      <p
        className="font-sans"
        style={{
          fontSize: 11,
          textTransform: "uppercase",
          letterSpacing: "0.08em",
          color: "#555555",
          marginBottom: 12,
        }}
      >
        Usage Breakdown
      </p>
      <div
        style={{
          background: "#111111",
          border: "0.5px solid #1e1e1e",
          borderRadius: 10,
          overflowX: "auto",
        }}
      >
        <table
          className="font-sans"
          style={{ width: "100%", fontSize: 13, borderCollapse: "collapse" }}
        >
          <thead>
            <tr style={{ borderBottom: "0.5px solid #1e1e1e" }}>
              <Th>API Key</Th>
              <Th>Model</Th>
              <Th align="right">Requests</Th>
              <Th align="right">Prompt Tokens</Th>
              <Th align="right">Completion Tokens</Th>
              <Th align="right">Cost USD</Th>
              <Th align="right">Avg Latency</Th>
            </tr>
          </thead>
          <tbody>
            {usage.map((row, i) => (
              <tr key={i} className="table-row-hover">
                <Td mono>{displayKey(row)}</Td>
                <Td mono muted>
                  {row.model}
                </Td>
                <Td align="right">{fmtInt(row.requests)}</Td>
                <Td align="right" mono style={{ color: "#60a5fa" }}>
                  {fmtTokens(row.prompt_tokens)}
                </Td>
                <Td align="right" mono style={{ color: "#60a5fa" }}>
                  {fmtTokens(row.completion_tokens)}
                </Td>
                <Td align="right" mono muted>
                  {fmtCost(row.total_cost_usd)}
                </Td>
                <Td align="right" mono>
                  {fmtLatency(row.avg_latency_ms)}
                </Td>
              </tr>
            ))}
            {usage.length === 0 && (
              <tr>
                <td
                  colSpan={7}
                  style={{
                    textAlign: "center",
                    padding: "40px 16px",
                    color: "#555555",
                    fontSize: 13,
                  }}
                >
                  No usage data yet
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </>
  );
}

/* ================================================================== */
/*  Shared primitives                                                  */
/* ================================================================== */
function StatCard({
  label,
  value,
  subtitle,
  valueColor = "#e8e8e8",
}: {
  label: string;
  value: string;
  subtitle: string;
  valueColor?: string;
}) {
  return (
    <div
      style={{
        background: "#111111",
        border: "0.5px solid #1e1e1e",
        borderRadius: 10,
        padding: "16px 20px",
      }}
    >
      <p
        className="font-sans"
        style={{
          fontSize: 11,
          textTransform: "uppercase",
          letterSpacing: "0.08em",
          color: "#555555",
          marginBottom: 8,
        }}
      >
        {label}
      </p>
      <p
        className="font-mono"
        style={{ fontSize: 22, lineHeight: 1.2, color: valueColor }}
      >
        {value}
      </p>
      <p
        className="font-sans"
        style={{ fontSize: 11, color: "#555555", marginTop: 4 }}
      >
        {subtitle}
      </p>
    </div>
  );
}

function StatusBadge({ active }: { active: boolean }) {
  return active ? (
    <span
      style={{
        display: "inline-block",
        padding: "2px 8px",
        borderRadius: 4,
        fontSize: 11,
        fontWeight: 500,
        color: "#4ade80",
        background: "rgba(74, 222, 128, 0.1)",
        border: "0.5px solid rgba(74, 222, 128, 0.3)",
      }}
    >
      Active
    </span>
  ) : (
    <span
      style={{
        display: "inline-block",
        padding: "2px 8px",
        borderRadius: 4,
        fontSize: 11,
        fontWeight: 500,
        color: "#f87171",
        background: "rgba(248, 113, 113, 0.1)",
        border: "0.5px solid rgba(248, 113, 113, 0.3)",
      }}
    >
      Inactive
    </span>
  );
}

function Th({
  children,
  align = "left",
}: {
  children: React.ReactNode;
  align?: "left" | "right";
}) {
  return (
    <th
      className="font-sans"
      style={{
        fontSize: 11,
        textTransform: "uppercase",
        letterSpacing: "0.08em",
        color: "#555555",
        fontWeight: 400,
        padding: "12px 16px",
        textAlign: align,
        whiteSpace: "nowrap",
      }}
    >
      {children}
    </th>
  );
}

function Td({
  children,
  mono,
  muted,
  align = "left",
  style: extraStyle,
}: {
  children: React.ReactNode;
  mono?: boolean;
  muted?: boolean;
  align?: "left" | "right";
  style?: React.CSSProperties;
}) {
  return (
    <td
      className={mono ? "font-mono" : "font-sans"}
      style={{
        padding: "12px 16px",
        textAlign: align,
        color: muted ? "#555555" : undefined,
        whiteSpace: "nowrap",
        ...extraStyle,
      }}
    >
      {children}
    </td>
  );
}
