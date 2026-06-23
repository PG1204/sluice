import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { TenantUsage } from "../api";

export function TenantUsageChart({ tenants }: { tenants: TenantUsage[] }) {
  const data = tenants.map((t) => ({ tenant: t.tenant, tokens: t.tokens_consumed }));
  return (
    <div className="panel">
      <h2>Tokens consumed per tenant</h2>
      <ResponsiveContainer width="100%" height={240}>
        <BarChart data={data}>
          <CartesianGrid strokeDasharray="3 3" stroke="#2a2a3a" />
          <XAxis dataKey="tenant" stroke="#aaa" />
          <YAxis stroke="#aaa" allowDecimals={false} />
          <Tooltip contentStyle={{ background: "#1b1b27", border: "1px solid #333" }} />
          <Bar dataKey="tokens" fill="#5b8def" radius={[4, 4, 0, 0]} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
