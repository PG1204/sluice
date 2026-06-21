// Package storage reads tabular data into in-memory columnar batches.
//
// The central abstraction is DataSource: a pull-based, batch-at-a-time reader
// (Schema / Next / Close) that the rest of the engine consumes uniformly,
// whether the bytes come from CSV or Parquet. Data is held column-at-a-time
// (see column.go) rather than row-at-a-time, which is cache-friendly for scans
// and aggregations and sets up vectorized execution later.
//
// Pieces:
//   - Type, Schema, Field         — the logical type system (types.go, batch.go)
//   - Column / TypedColumn[T]      — nullable, typed columnar storage (column.go)
//   - Batch                        — a chunk of rows as parallel columns (batch.go)
//   - DataSource                   — the reader interface (datasource.go)
//   - CSVSource, ParquetSource     — concrete readers (csv.go, parquet.go)
//   - Registry                     — table-name -> file resolution (registry.go)
package storage
