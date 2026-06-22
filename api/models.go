package api

import (
	"github.com/PG1204/sluice/common"
	"github.com/PG1204/sluice/engine"
	"github.com/PG1204/sluice/engine/storage"
)

// queryRequest / explainRequest are the JSON bodies for the SQL endpoints.
type sqlRequest struct {
	SQL string `json:"sql"`
}

// columnInfo describes one output column.
type columnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
}

// queryResponse is the result of POST /query.
type queryResponse struct {
	Columns  []columnInfo `json:"columns"`
	Rows     [][]any      `json:"rows"`
	RowCount int          `json:"row_count"`
}

// explainResponse is the result of POST /explain.
type explainResponse struct {
	Plan string  `json:"plan"`
	Cost float64 `json:"cost"`
}

// tableInfo / tablesResponse are the result of GET /tables.
type tableInfo struct {
	Name    string       `json:"name"`
	Columns []columnInfo `json:"columns"`
}

type tablesResponse struct {
	Tables []tableInfo `json:"tables"`
}

// quotaResponse is the result of GET /quota.
type quotaResponse struct {
	Tenant    string  `json:"tenant"`
	Remaining int64   `json:"remaining"`
	Rate      float64 `json:"rate"`
	Burst     int64   `json:"burst"`
}

// healthResponse is the result of GET /health.
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// newQueryResponse converts an engine result into the JSON response shape,
// turning each cell into its native JSON value (NULL -> null).
func newQueryResponse(result *engine.Result) queryResponse {
	resp := queryResponse{
		Columns:  schemaColumns(result.Schema),
		Rows:     make([][]any, 0, result.RowCount()),
		RowCount: result.RowCount(),
	}
	for _, batch := range result.Batches {
		for row := 0; row < batch.NumRows(); row++ {
			cells := make([]any, batch.NumCols())
			for col := 0; col < batch.NumCols(); col++ {
				cells[col] = batch.Columns[col].Value(row) // nil for NULL
			}
			resp.Rows = append(resp.Rows, cells)
		}
	}
	return resp
}

// schemaColumns projects a storage schema into the API's column descriptors.
func schemaColumns(s storage.Schema) []columnInfo {
	cols := make([]columnInfo, len(s.Fields))
	for i, f := range s.Fields {
		cols[i] = columnInfo{Name: f.Name, Type: f.Type.String(), Nullable: f.Nullable}
	}
	return cols
}

// healthBody is the static health payload.
func healthBody() healthResponse {
	return healthResponse{Status: "ok", Version: common.Version}
}
