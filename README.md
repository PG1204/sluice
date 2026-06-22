# Sluice

**A cost-aware query engine with built-in distributed rate limiting.**

Queries pay a "toll" based on how expensive they are. The query optimizer
estimates each query's cost *before* execution, and that estimate is what the
rate limiter charges against a tenant's quota. Cheap queries barely touch the
quota; expensive queries drain it fast.

This feedback loop between the query planner and the rate limiter mirrors how
production cloud warehouses (BigQuery, Snowflake, Redshift) meter usage - and
is almost never built from scratch.

> Status: **early development**, built in phases.

## Architecture

```
Client / CLI
     │ HTTP
API Gateway (auth + routing)
     │
Rate Limiter  ◄── Redis        ← charges the optimizer's cost estimate
     │ (allowed queries pass through)
Query Engine: Lexer → Parser → Logical Plan → Cost Estimator → Physical Plan → Executor
     │
Storage: Parquet / CSV
```

A full diagram lives in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) *(coming in a later phase)*.

## Layout

| Path          | What lives here |
|---------------|-----------------|
| `engine/`     | Lexer, parser, AST, logical/physical plans, optimizer, storage |
| `limiter/`    | Distributed token-bucket rate limiter (Redis-backed) |
| `api/`        | HTTP service layer + cost-based throttling middleware |
| `cli/`        | `sluice` command-line query tool |
| `common/`     | Shared types, errors, version |
| `dashboard/`  | React observability dashboard *(Phase 9)* |
| `docs/`       | Architecture and design-decision notes |
| `testdata/`   | Sample CSV/Parquet fixtures |

## Quick start (dev)

Requires Go 1.26+.

```bash
make build      # build the CLI into bin/sluice
make test       # run unit tests (race detector on)
make ci         # vet + lint + test, as CI runs it
make help       # list all targets
```

Run a query against the bundled sample tables in `testdata/`:

```bash
./bin/sluice tables --data ./testdata
./bin/sluice query "SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name" --data ./testdata

# Cost-based planning: the optimizer estimates rows and cost per operator
# before execution. This total cost is what the rate limiter will charge.
./bin/sluice explain --cost "SELECT o.name, c.city FROM orders o JOIN customers c ON o.name = c.name WHERE o.amount > 100"
./bin/sluice cost "SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name"
```

### HTTP API

Run the engine as a service (`POST /query`, `POST /explain`, `GET /tables`,
`GET /quota`, `GET /health`; all but `/health` need an `X-API-Key`):

```bash
go run ./cmd/sluice-server --data ./testdata      # listens on :8080

curl localhost:8080/health
curl -s -X POST localhost:8080/query -H 'X-API-Key: dev-key' \
  -d '{"sql":"SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name"}'
curl -s localhost:8080/quota -H 'X-API-Key: dev-key'
```

API keys and per-tenant quotas come from a JSON config (`--config`); without one
a `dev-key` default is used. The full spec is in [docs/openapi.yaml](docs/openapi.yaml).

`docker compose up` builds and runs the API server with Redis over the sample
data; the dashboard service arrives in a later phase.

## Design decisions

Non-obvious choices are recorded as short Architecture Decision Records in
[docs/decisions/](docs/decisions/).

## License

MIT — see [LICENSE](LICENSE).
