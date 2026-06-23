# Sluice

**A cost-aware SQL query engine with built-in, cost-based rate limiting.**

Sluice runs SQL over CSV and Parquet files — and *prices every query before it
runs*. The query optimizer estimates each query's cost, and that estimate is
what the rate limiter charges against a tenant's quota. Cheap queries barely
touch the quota; expensive queries drain it fast and get throttled sooner.

That feedback loop — **the planner telling the rate limiter how costly a query
is** — is the thing this project is built around. Most systems rate-limit by
*request count*; production data warehouses (BigQuery, Snowflake, Redshift)
meter by *work*. Sluice does the latter, from scratch.

```
$ sluice query "SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name"
name   COUNT(*)
Alice  2
Bob    2
Carol  1
(3 rows)
```

When the same query comes over HTTP and the tenant is out of quota:

```json
HTTP 429  {"error":"rate limit exceeded","estimated_cost":36,"tokens_required":4,"remaining":2}
```

---

## Architecture

```
   Client / CLI ──HTTP──►  API  ──►  Rate limiter ──► Query engine ──► Storage
                          (auth)     (token bucket,    (lex → parse →   (CSV /
                                      Redis-backed)     plan → COST →    Parquet)
                                          ▲             optimize →
                                          │             execute)
                                          └──── estimated query cost ────┘
                                                (the feedback loop)
                                  │
                          Observability: Prometheus /metrics, /stats, React dashboard
```

The query pipeline: **lexer → parser → logical plan (+ validation) → cost
estimator & optimizer → physical executor (Volcano model)**. The optimizer's
total cost is exposed as one number; the API charges it to the limiter before
executing. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the deep dive
and [docs/cost-based-throttling.md](docs/cost-based-throttling.md) for the
design of the headline feature.

## Quick start

Requires Go 1.26+. The repo bundles a sample dataset in `testdata/` (tables
`orders`, `customers`).

```bash
make build                  # build the CLI -> bin/sluice
make test                   # full test suite (race detector on)

# run a query
./bin/sluice query "SELECT name, COUNT(*) FROM orders GROUP BY name" --data ./testdata

# see the optimized plan with per-operator cost estimates
./bin/sluice explain --cost "SELECT o.name, c.city FROM orders o \
  JOIN customers c ON o.name = c.name WHERE o.amount > 100" --data ./testdata
```

### As a service

```bash
go run ./cmd/sluice-server --data ./testdata        # API on :8080

curl -s -X POST localhost:8080/query -H 'X-API-Key: dev-key' \
  -d '{"sql":"SELECT name, COUNT(*) FROM orders GROUP BY name"}'
```

`docker compose up` brings up the API + Redis. The React + Recharts dashboard
(live query feed, quota usage, cost distribution, plan visualizer) lives in
[dashboard/](dashboard/): `cd dashboard && npm install && npm run dev`.

## Example queries and their cost

Cost is in abstract units (≈ rows × columns processed, plus join/sort work).
The API converts cost to quota tokens (`tokens = ceil(cost / cost_per_token)`).

| Query (over `testdata/`) | Cost |
|---|---|
| `SELECT name, COUNT(*) FROM orders GROUP BY name` | 19 |
| `SELECT name FROM orders WHERE amount > 100` | 30 |
| `SELECT * FROM orders` | 40 |
| `SELECT o.name, c.city FROM orders o JOIN customers c ON o.name = c.name WHERE o.amount > 100` | 48 |

Projection pushdown is why `SELECT name …` (30) costs less than `SELECT *` (40):
the optimizer reads only the columns the query needs.

## Performance

Measured with `go test -bench=. ./engine/` over a generated 100k-row CSV on an
Apple M4 (single-threaded; reproduce with the benchmarks in
[engine/bench_test.go](engine/bench_test.go)):

| Workload (100k rows) | Latency / query | Throughput |
|---|---|---|
| Scan + filter | ~49 ms | ~2.0M rows/sec |
| Group-by aggregate (4 aggregates) | ~54 ms | ~1.9M rows/sec |
| Filter + aggregate + sort | ~56 ms | ~1.8M rows/sec |
| Plan only (parse → optimize → cost) | ~2.3 µs | — |

Planning is ~2µs, so the cost estimate the limiter waits on is effectively free
relative to execution. Rate-limiting is **exact**: with a burst of *B* tokens
and a query costing *c*, exactly `floor(B/c)` queries are admitted before a
`429` — verified under concurrency with the race detector and against real
Redis.

## Layout

| Path | What lives here |
|---|---|
| `engine/{lexer,parser,ast}` | SQL text → tokens → validated AST |
| `engine/{logical,optimizer,physical}` | logical plan → cost-based optimization → Volcano executor |
| `engine/storage` | CSV/Parquet readers, columnar batches, schema/stats catalog |
| `engine` | façade tying it together (`Query`, `Explain`, `Cost`) |
| `limiter` | cost-aware rate limiter: token bucket (in-memory + Redis/Lua), sliding window |
| `api`, `cmd/sluice-server` | HTTP service (auth, cost-based throttling, metrics) + its binary |
| `cli` | the `sluice` command-line tool |
| `dashboard` | React + Recharts observability dashboard |
| `docs/` | architecture, design docs, demo script |

## Tech decisions & tradeoffs

Choices worth defending (the long form is in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)):

- **Hand-written lexer + recursive-descent/Pratt parser**, not a generator —
  precise `line:column` errors and full control; it's the point of the
  exercise.
- **Columnar batches + Volcano iterator model** — cache-friendly scans, bounded
  memory (a batch per operator), and a clean path to vectorized execution later.
- **Cost-based throttling over request counting** — fairness by *work*: one
  tenant's expensive analytical scans can't crowd out another's cheap lookups
  at the same request rate.
- **Token bucket over leaky bucket / fixed window** — tolerates bursts up to a
  cap while bounding the sustained rate; a query simply costs N tokens.
- **Redis + atomic Lua script** for the distributed limiter — the
  refill-and-consume step is atomic server-side, so no distributed lock and no
  over-admission across instances.
- **Estimate, then charge, then execute** — every input error is a 400 *before*
  any tokens are spent; the cost charged is from the exact plan that runs.

Known limitations (and what I'd do next): cost estimates assume column
independence and uniform distributions (no histograms); single-join build-side
reordering only; results materialize in memory; "actual" (measured) cost vs.
the estimate isn't yet reconciled — an adaptive cost model is the most
interesting follow-up.

## Status

Built in phases 0–10: lexer/parser, storage, logical plan, executor, cost
optimizer, rate limiter, HTTP API, cost-based throttling, observability +
dashboard, and this polish pass. All packages tested with the race detector;
engine and limiter packages are >80% covered.

## License

MIT — see [LICENSE](LICENSE).
