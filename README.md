# Sluice

**A cost-aware query engine with built-in distributed rate limiting.**

Queries pay a "toll" based on how expensive they are. The query optimizer
estimates each query's cost *before* execution, and that estimate is what the
rate limiter charges against a tenant's quota. Cheap queries barely touch the
quota; expensive queries drain it fast.

This feedback loop between the query planner and the rate limiter mirrors how
production cloud warehouses (BigQuery, Snowflake, Redshift) meter usage — and
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
./bin/sluice --version
make test       # run unit tests (race detector on)
make ci         # vet + lint + test, as CI runs it
make help       # list all targets
```

`docker-compose up` will bring up the full stack (api + redis + dashboard) once
those services exist.

## Design decisions

Non-obvious choices are recorded as short Architecture Decision Records in
[docs/decisions/](docs/decisions/).

## License

MIT — see [LICENSE](LICENSE).
