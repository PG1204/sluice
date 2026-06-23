# Cost-Based Throttling: pricing a query before you run it

This is the idea Sluice is built around. It's short, but it's the part worth
understanding.

## The problem with counting requests

Almost every rate limiter counts *requests*: "100 requests per minute per API
key." That's easy and it's fair when requests are roughly equal — a login, a
profile fetch, a like.

Queries are not roughly equal. Consider two tenants hitting an analytics API at
the *same* request rate:

- Tenant A runs `SELECT id FROM events WHERE id = 42` — an indexed point lookup.
- Tenant B runs `SELECT region, COUNT(*) FROM events GROUP BY region` over a
  billion rows — a full scan and aggregation.

A request counter treats these identically. So Tenant B can saturate the
database — CPU, memory, I/O — while staying comfortably under a request limit,
starving Tenant A. The limit is measuring the wrong thing. It's counting cars
crossing a bridge when what you care about is total weight.

Production data warehouses (BigQuery's "bytes billed," Snowflake credits,
Redshift WLM) all meter by *work*, not by request count, for exactly this
reason. Sluice does the same — but builds the loop end to end so you can see
every piece.

## The insight: the planner already knows the cost

Here's the thing — a query engine **already estimates how expensive a query is**,
because it has to, to optimize it. Before executing, the optimizer:

1. looks up table statistics (row counts, distinct values per column),
2. estimates how many rows each operator will produce (cardinality), and
3. assigns a cost to each operator and sums them into one number.

That cost number is normally used only internally, to pick a good plan. Sluice's
move is to **expose it and hand it to the rate limiter.** The information is
already there; we just route it somewhere new.

```
SQL → parse → plan → ┌─ optimize → cost estimate ─┐
                     │                            ▼
                     │                    rate limiter: can this
                     │                    tenant afford `cost`?
                     │                       │ yes        │ no
                     ▼                       ▼            ▼
                  (the plan)             execute       429 + retry
```

## How the cost is computed

`Cost(sql)` is the optimizer's total, in abstract units. The model is
deliberately simple — the goal is *relative* ordering of queries, not absolute
accuracy:

- **scan** ≈ rows × columns read (so reading fewer columns is cheaper — which
  is why projection pushdown lowers cost),
- **filter / project / aggregate** ≈ input rows,
- **hash join** ≈ build-side rows × 2 + probe-side rows (building costs more
  than probing, which is why putting the smaller table on the build side wins),
- **sort** ≈ n·log n.

Selectivity heuristics estimate how many rows survive each step: equality is
`1/distinct_values`, ranges come from column min/max, `AND` multiplies
(assuming independence). These are textbook and imperfect — the independence
assumption is the classic place estimates go wrong — but good enough to rank
queries by how much work they are.

You can see it directly:

```
$ sluice explain --cost "SELECT o.name, c.city FROM orders o \
    JOIN customers c ON o.name = c.name WHERE o.amount > 100"
Project: o.name, c.city  (rows=6 cost=6.0)
  Join INNER on o.name = c.name  (rows=6 cost=12.0)
    Filter: o.amount > 100  (rows=6 cost=8.0)
      Scan: orders AS o [name, amount]  (rows=8 cost=16.0)   ← only 2 columns read
    Scan: customers AS c [name, city]  (rows=3 cost=6.0)
Total cost: 48.0
```

## From cost to quota

The limiter is a **token bucket** per tenant: it refills at a steady rate up to
a burst capacity. A query costs `tokens = ceil(cost / cost_per_token)` tokens
(at least 1). Token bucket is the right shape here — it tolerates short bursts
up to the cap while bounding the sustained rate, and "this query costs N tokens
instead of 1" is a trivial change to make it cost-aware.

The order of operations matters:

1. **Prepare** — parse, plan, optimize, estimate cost, build the operator.
   Every *input* error (bad SQL, unknown column) is a `400` here, **before any
   tokens are spent**.
2. **Allow** — charge `tokens` to the bucket. Out of quota → `429` with
   `Retry-After` and a body explaining the cost.
3. **Execute** — run the *same* prepared plan, so the cost charged is exactly
   the cost of what runs.

In a multi-instance deployment the bucket lives in Redis, and the
refill-read-decide-write runs as a single atomic Lua script — so concurrent
requests across instances can't over-admit, with no distributed lock.

## Does it work?

Yes, and it's measurable. With both tenants at the same request rate and the
same quota, the one running expensive queries is admitted far fewer times:

> 60 requests each, identical 200-token bucket — **cheap-query tenant: 11
> admitted; expensive-query tenant: 5 admitted.** The expensive tenant is
> throttled first, even though both sent the same number of requests at the
> same rate.

And the limiting is *exact*: with a burst of *B* tokens and a query costing *c*
tokens, exactly `floor(B/c)` queries pass before a `429` — verified under
heavy concurrency (in-memory with the race detector, and against real Redis).

## What I'd do next

The cost charged is the optimizer's *estimate*. The natural next step is an
**adaptive cost model**: measure each query's *actual* work as it runs, compare
it to the estimate, and feed the error back to correct future estimates. That
closes the loop between the planner and reality — and it's genuinely novel
territory, because it makes the rate limiter's fairness improve over time.
