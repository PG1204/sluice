import { StatsSnapshot } from "../api";

export function SummaryCards({ stats }: { stats: StatsSnapshot }) {
  const throttleRate =
    stats.total_queries > 0
      ? ((stats.total_throttled / stats.total_queries) * 100).toFixed(1)
      : "0.0";
  return (
    <div className="cards">
      <Card label="Total queries" value={stats.total_queries} />
      <Card label="Throttled (429)" value={stats.total_throttled} />
      <Card label="Throttle rate" value={`${throttleRate}%`} />
      <Card label="Tenants" value={stats.tenants.length} />
    </div>
  );
}

function Card({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="card">
      <div className="card-value">{value}</div>
      <div className="card-label">{label}</div>
    </div>
  );
}
