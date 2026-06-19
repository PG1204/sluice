// Package storage reads tabular data into in-memory columnar batches. It
// defines the DataSource interface, CSV and Parquet readers, the type system
// (Int64, Float64, String, Boolean, Null), and the schema registry that
// resolves a table name to a file in the configured data directory.
//
// Implemented in Phase 2.
package storage
