// Package api is the HTTP service layer exposing the query engine: /query,
// /explain, /tables, /health, and /quota, with API-key auth and per-key
// quota config. It wires the optimizer's cost estimate into the limiter —
// the cost-based throttling middleware lives here.
//
// Implemented in Phases 7 and 8.
package api
