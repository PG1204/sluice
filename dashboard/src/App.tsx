import { useCallback, useEffect, useState } from "react";
import { fetchStats, StatsSnapshot } from "./api";
import { SummaryCards } from "./components/SummaryCards";
import { TenantUsageChart } from "./components/TenantUsageChart";
import { CostHistogram } from "./components/CostHistogram";
import { TopThrottled } from "./components/TopThrottled";
import { QueryFeed } from "./components/QueryFeed";
import { PlanVisualizer } from "./components/PlanVisualizer";
import { QueryRunner } from "./components/QueryRunner";

const POLL_MS = 2000;

export function App() {
  const [base, setBase] = useState("http://localhost:8080");
  const [apiKey, setApiKey] = useState("dev-key");
  const [stats, setStats] = useState<StatsSnapshot | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setStats(await fetchStats(base, apiKey));
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [base, apiKey]);

  useEffect(() => {
    refresh();
    const id = setInterval(refresh, POLL_MS);
    return () => clearInterval(id);
  }, [refresh]);

  return (
    <div className="app">
      <header className="header">
        <h1>Sluice</h1>
        <span className="subtitle">cost-aware query engine — live dashboard</span>
        <div className="conn">
          <label>
            API&nbsp;
            <input value={base} onChange={(e) => setBase(e.target.value)} size={24} />
          </label>
          <label>
            Key&nbsp;
            <input value={apiKey} onChange={(e) => setApiKey(e.target.value)} size={10} />
          </label>
          {error ? <span className="err">offline: {error}</span> : <span className="ok">connected</span>}
        </div>
      </header>

      <QueryRunner base={base} apiKey={apiKey} onRan={refresh} />

      {stats && (
        <>
          <SummaryCards stats={stats} />
          <div className="grid">
            <TenantUsageChart tenants={stats.tenants} />
            <CostHistogram buckets={stats.cost_buckets} />
            <TopThrottled tenants={stats.tenants} />
          </div>
          <PlanVisualizer base={base} apiKey={apiKey} />
          <QueryFeed events={stats.recent} />
        </>
      )}
    </div>
  );
}
