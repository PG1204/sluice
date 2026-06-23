# Sluice — Architecture

A deeper look at how Sluice is built and why. For the plain-English orientation
see the README; for the headline feature see
[cost-based-throttling.md](cost-based-throttling.md).

## The shape of the system

```
                 ┌──────────────────────────────────────────────┐
   HTTP / CLI ──►│ API (api/, cmd/sluice-server)                 │
                 │  auth (X-API-Key → tenant)                    │
                 │  ┌─────────────────────────────────────────┐  │
                 │  │ /query lifecycle:                        │  │
                 │  │  1. Prepare  (parse→plan→optimize→cost)  │  │
                 │  │  2. Allow    (charge cost to limiter)    │──┼──► limiter/
                 │  │  3. Execute  (run the prepared plan)     │  │   (token bucket,
                 │  └─────────────────────────────────────────┘  │    Redis/Lua)
                 │  observability: /metrics, /stats, X-Request-ID │
                 └───────────────────────┬──────────────────────┘
                                         │
                 ┌───────────────────────▼──────────────────────┐
                 │ engine/                                       │
                 │  lexer → parser → ast                         │
                 │  → logical plan (+ schema/type validation)    │
                 │  → optimizer (stats, cardinality, cost, rules)│
                 │  → physical plan (Volcano operators)          │
                 └───────────────────────┬──────────────────────┘
                                         │
                 ┌───────────────────────▼──────────────────────┐
                 │ engine/storage: CSV / Parquet → columnar      │
                 │  batches; schema + stats catalog (cached)     │
                 └───────────────────────────────────────────────┘
```

Dependencies point inward: `storage` knows nothing of `logical`; `logical`
knows nothing of `physical`; the `limiter` has no dependency on the `engine` at
all (the API is the only thing that knows about both). `common` is the leaf.

## The query pipeline

### 1. Lexer (`engine/lexer`)
A hand-written, byte-level scanner. Each token carries a `Position`
(line/column/offset) so every later error can point at the exact spot.
Hand-written (not generated) buys precise error messages and full control over
keyword handling — and it's a core thing to be able to explain.

### 2. Parser (`engine/parser`)
Recursive descent for statement structure; a **Pratt parser** (precedence
climbing) for expressions. Pratt collapses what would be six near-identical
precedence-level methods into one `parseExpression(minPrec)` loop plus a
binding-power table — adding an operator is a one-line change. Errors are
values and fail fast with a `line:column`.

### 3. AST (`engine/ast`)
Plain data nodes with a **precedence-aware pretty-printer** that renders the
tree back to minimally-parenthesized SQL. That printer doubles as a test
oracle: parse → print → re-parse must yield an identical tree.

### 4. Logical plan (`engine/logical`)
The AST becomes a tree of relational operators (Scan, Filter, Project, Join,
Aggregate, Sort, Limit). A `scope` of named relations drives name resolution
(qualified `t.col`, ambiguity detection); a single recursive `checker`
type-checks expressions. Aggregates are *extracted* into the Aggregate node and
GROUP-BY membership is validated. Every node knows its output schema.

### 5. Optimizer (`engine/optimizer`) — the novel core
- **Statistics**: row count + per-column distinct/null/min-max, computed by
  scanning a table once and **cached**.
- **Cardinality + cost**: one bottom-up pass produces per-operator row and cost
  estimates and a single total. Textbook selectivity heuristics (equality =
  `1/distinct`, ranges from min/max, `AND` multiplies under independence);
  equi-join cardinality `|L|·|R| / max(distinct keys)`.
- **Rules** (cost-reducing rewrites, applied in order): predicate pushdown
  (filters below joins), projection pushdown (scans read only used columns),
  join reorder (smaller input on the hash-join build side).

The cost model is deliberately column-aware at the scan and makes hash-join
builds cost more than probes, so the rules produce a *visible* cost drop rather
than a no-op. `EXPLAIN COST` shows the annotated tree; `Cost(sql)` returns the
total — the number the limiter charges.

### 6. Physical execution (`engine/physical`)
The **Volcano iterator model**: every operator implements `Open / Next / Close`
and pulls columnar batches from its children. Pipelined operators (scan,
filter, project, limit) hold one batch; pipeline breakers (sort, hash
aggregate, hash-join build side) buffer. Expressions are compiled once
(column refs bound to positions) and evaluated over a small tagged-union
`Value`, with SQL three-valued logic for NULLs. Execution is row-at-a-time
*within* a batch — naive by design; the columnar layout is ready for
vectorization later.

### Storage (`engine/storage`)
A `DataSource` is a pull-based, batch-at-a-time reader (`Schema/Next/Close`),
implemented for CSV (with type inference) and Parquet. Data is held
**column-at-a-time** (typed, nullable columns) rather than row-at-a-time. The
`Registry` maps a table name to a file and caches inferred schemas — without
that cache, every query re-scanned the whole CSV just to re-learn its types
(benchmarking caught this: it cut planning from ~28ms to ~2µs).

## The rate limiter (`limiter/`)
A standalone, cost-aware limiter with no engine dependency. `Allow(ctx,
tenant, cost)` debits `cost` tokens from the tenant's bucket. Three
implementations behind one interface:
- **TokenBucket** — in-memory; the refill/consume math is a pure function so it
  can be unit-tested exhaustively.
- **Redis** — the same math as an atomic **Lua script**, so multiple API
  instances share one bucket per tenant with no distributed lock.
- **SlidingWindow** — a weighted-counter alternative.

Token bucket (over leaky bucket / fixed window) tolerates bursts up to a cap
while bounding the sustained rate, and "a query costs N tokens" falls out
naturally.

## The feedback loop (`api/`)
`/query` runs **Prepare → Allow → Execute**. Prepare does all the parsing,
planning, optimizing, cost estimation, and operator building — so every input
error surfaces as a `400` *before* any tokens are charged, and the cost charged
comes from the exact plan that will run. Allow maps cost to tokens
(`ceil(cost / cost_per_token)`) and consumes them; if the tenant is out, it
returns `429` with `Retry-After` and a cost breakdown. Only then does Execute
run the prepared operator.

## Observability (`api/`, `dashboard/`)
Two paths: **Prometheus** (`/metrics`, on a private registry) is the durable
metrics-of-record; an in-memory **collector** keeps a bounded rolling view
(recent-query feed, per-tenant usage, cost histogram) served at `/stats` for
the dashboard. Every request gets an `X-Request-ID` tying its response, logs,
and feed entry together. The React + Recharts dashboard polls `/stats`.

## Cross-cutting choices
- **Errors are values**, wrapped with context; no panics in normal flow.
- **Injected clocks** in the limiter make refill/expiry tests deterministic.
- **Custom Prometheus registry** per server so tests don't collide on the
  global default.
- **One dependency per concern**, justified: `parquet-go` (Parquet),
  `go-redis` (distributed limiter), `prometheus/client_golang` (metrics),
  `testify` (assertions). Everything else is the standard library.

## Limitations & what's next
- Cost estimates assume column independence and uniform distributions — no
  histograms. The classic place estimates go wrong.
- Join optimization is build-side selection for a single join; no multi-join
  enumeration.
- Results materialize in memory (fine for the CLI/demo; streaming is a later
  concern).
- The limiter charges the *estimated* cost; reconciling it with *actual*
  measured cost — an **adaptive cost model** — is the most interesting
  follow-up and would close the loop between the planner and reality.
- Vectorized (batch-at-a-time) expression evaluation is the obvious throughput
  win; the columnar layout is already in place for it.
