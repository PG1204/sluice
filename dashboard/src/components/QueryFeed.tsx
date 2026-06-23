import { QueryEvent } from "../api";

export function QueryFeed({ events }: { events: QueryEvent[] }) {
  return (
    <div className="panel">
      <h2>Live query feed</h2>
      <div className="feed-scroll">
        <table className="feed">
          <thead>
            <tr>
              <th>time</th>
              <th>tenant</th>
              <th>SQL</th>
              <th>cost</th>
              <th>tokens</th>
              <th>rows</th>
              <th>ms</th>
              <th>outcome</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e) => (
              <tr key={e.request_id} className={`row-${e.outcome}`}>
                <td className="mono">{e.time.replace("T", " ").replace("Z", "")}</td>
                <td>{e.tenant}</td>
                <td className="sql" title={e.sql}>{e.sql}</td>
                <td>{e.estimated_cost}</td>
                <td>{e.tokens}</td>
                <td>{e.rows}</td>
                <td>{e.latency_ms}</td>
                <td>
                  <span className={`badge badge-${e.outcome}`}>{e.outcome}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
