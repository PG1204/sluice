import { useState } from "react";
import { fetchPlan, PlanNode } from "../api";

export function PlanVisualizer({ base, apiKey }: { base: string; apiKey: string }) {
  const [sql, setSql] = useState("SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name");
  const [tree, setTree] = useState<PlanNode | null>(null);
  const [error, setError] = useState<string | null>(null);

  const visualize = async () => {
    try {
      setTree(await fetchPlan(base, apiKey, sql));
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setTree(null);
    }
  };

  return (
    <div className="panel">
      <h2>Query plan visualizer</h2>
      <div className="plan-input">
        <textarea value={sql} onChange={(e) => setSql(e.target.value)} rows={2} />
        <button onClick={visualize}>Explain</button>
      </div>
      {error && <p className="err">{error}</p>}
      {tree && (
        <div className="plan-tree">
          <PlanNodeView node={tree} />
        </div>
      )}
    </div>
  );
}

function PlanNodeView({ node }: { node: PlanNode }) {
  return (
    <div className="plan-node">
      <div className="plan-box">
        <span className="plan-label">{node.label}</span>
        <span className="plan-est">
          rows={node.rows} · cost={node.cost}
        </span>
      </div>
      {node.children && node.children.length > 0 && (
        <div className="plan-children">
          {node.children.map((c, i) => (
            <PlanNodeView key={i} node={c} />
          ))}
        </div>
      )}
    </div>
  );
}
