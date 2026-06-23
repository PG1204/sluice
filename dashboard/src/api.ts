// Types mirror the JSON returned by the Sluice API (api/stats.go, engine.PlanNode).

export interface TenantUsage {
  tenant: string;
  queries: number;
  throttled: number;
  tokens_consumed: number;
}

export interface CostBucket {
  label: string;
  count: number;
}

export interface QueryEvent {
  request_id: string;
  time: string;
  tenant: string;
  sql: string;
  outcome: string; // ok | throttled | error
  estimated_cost: number;
  tokens: number;
  rows: number;
  latency_ms: number;
}

export interface StatsSnapshot {
  total_queries: number;
  total_throttled: number;
  tenants: TenantUsage[];
  cost_buckets: CostBucket[];
  recent: QueryEvent[];
}

export interface PlanNode {
  label: string;
  rows: number;
  cost: number;
  children?: PlanNode[];
}

export interface QueryOutcome {
  status: number;
  body: unknown;
}

function headers(apiKey: string): HeadersInit {
  return { "Content-Type": "application/json", "X-API-Key": apiKey };
}

export async function fetchStats(base: string, apiKey: string): Promise<StatsSnapshot> {
  const resp = await fetch(`${base}/stats`, { headers: headers(apiKey) });
  if (!resp.ok) throw new Error(`stats: HTTP ${resp.status}`);
  return resp.json();
}

export async function fetchPlan(base: string, apiKey: string, sql: string): Promise<PlanNode> {
  const resp = await fetch(`${base}/plan`, {
    method: "POST",
    headers: headers(apiKey),
    body: JSON.stringify({ sql }),
  });
  const body = await resp.json();
  if (!resp.ok) throw new Error((body && body.error) || `plan: HTTP ${resp.status}`);
  return body as PlanNode;
}

export async function runQuery(base: string, apiKey: string, sql: string): Promise<QueryOutcome> {
  const resp = await fetch(`${base}/query`, {
    method: "POST",
    headers: headers(apiKey),
    body: JSON.stringify({ sql }),
  });
  return { status: resp.status, body: await resp.json() };
}
