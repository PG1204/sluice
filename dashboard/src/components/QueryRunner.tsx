import { useState } from "react";
import { runQuery } from "../api";

// QueryRunner lets you submit SQL straight from the dashboard to generate
// traffic and watch the feed/charts react — handy for a live demo.
export function QueryRunner({ base, apiKey, onRan }: { base: string; apiKey: string; onRan: () => void }) {
  const [sql, setSql] = useState("SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name");
  const [result, setResult] = useState<string | null>(null);

  const submit = async () => {
    const { status, body } = await runQuery(base, apiKey, sql);
    setResult(`HTTP ${status} — ${JSON.stringify(body)}`);
    onRan();
  };

  return (
    <div className="panel runner">
      <h2>Run a query</h2>
      <div className="plan-input">
        <textarea value={sql} onChange={(e) => setSql(e.target.value)} rows={2} />
        <button onClick={submit}>Run</button>
      </div>
      {result && <pre className="result">{result}</pre>}
    </div>
  );
}
