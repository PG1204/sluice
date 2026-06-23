import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { TenantUsage } from "../api";

export function TopThrottled({ tenants }: { tenants: TenantUsage[] }) {
  const data = [...tenants]
    .filter((t) => t.throttled > 0)
    .sort((a, b) => b.throttled - a.throttled)
    .slice(0, 5)
    .map((t) => ({ tenant: t.tenant, throttled: t.throttled }));

  return (
    <div className="panel">
      <h2>Top throttled tenants</h2>
      {data.length === 0 ? (
        <p className="muted">No throttling yet.</p>
      ) : (
        <ResponsiveContainer width="100%" height={240}>
          <BarChart data={data} layout="vertical">
            <CartesianGrid strokeDasharray="3 3" stroke="#2a2a3a" />
            <XAxis type="number" stroke="#aaa" allowDecimals={false} />
            <YAxis type="category" dataKey="tenant" stroke="#aaa" width={80} />
            <Tooltip contentStyle={{ background: "#1b1b27", border: "1px solid #333" }} />
            <Bar dataKey="throttled" fill="#ef6b6b" radius={[0, 4, 4, 0]} />
          </BarChart>
        </ResponsiveContainer>
      )}
    </div>
  );
}
