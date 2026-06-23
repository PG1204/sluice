# dashboard

React + Recharts observability dashboard for Sluice. It polls the API's `/stats`
endpoint and renders:

- summary cards (total queries, throttled, throttle rate, tenants)
- tokens consumed per tenant (bar chart)
- query cost distribution (histogram)
- top throttled tenants (bar chart)
- a live query feed (recent queries with cost, tokens, latency, outcome)
- a query-plan visualizer (enter SQL → see the optimized plan as a tree with
  per-operator row/cost estimates)
- a "run a query" box to generate traffic and watch the dashboard react

## Run it

Start the API server (from the repo root), then the dashboard:

```bash
# terminal 1 — the engine API over the sample data
go run ./cmd/sluice-server --data ./testdata

# terminal 2 — the dashboard (Vite dev server on :3000)
cd dashboard
npm install
npm run dev
```

Open http://localhost:3000. The API base URL (`http://localhost:8080`) and API
key (`dev-key`) are editable in the header. The server sends permissive CORS
headers so the browser can call it cross-origin in dev.

`npm run build` produces a static bundle in `dist/`.
