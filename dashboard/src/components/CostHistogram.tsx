import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { CostBucket } from "../api";

export function CostHistogram({ buckets }: { buckets: CostBucket[] }) {
  return (
    <div className="panel">
      <h2>Query cost distribution</h2>
      <ResponsiveContainer width="100%" height={240}>
        <BarChart data={buckets}>
          <CartesianGrid strokeDasharray="3 3" stroke="#2a2a3a" />
          <XAxis dataKey="label" stroke="#aaa" />
          <YAxis stroke="#aaa" allowDecimals={false} />
          <Tooltip contentStyle={{ background: "#1b1b27", border: "1px solid #333" }} />
          <Bar dataKey="count" fill="#9b7aef" radius={[4, 4, 0, 0]} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
