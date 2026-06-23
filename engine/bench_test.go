package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// benchRows is the size of the generated table the benchmarks run against.
// Large enough that execution dominates fixed planning overhead.
const benchRows = 100_000

// generateEvents writes an events.csv of n rows into dir and returns the dir.
// Data is deterministic (no randomness) so benchmarks are reproducible.
func generateEvents(tb testing.TB, n int) string {
	tb.Helper()
	dir := tb.TempDir()
	categories := []string{"web", "mobile", "api", "batch", "internal"}
	regions := []string{"us", "eu", "apac"}

	var b strings.Builder
	b.WriteString("id,category,amount,region\n")
	for i := 0; i < n; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteByte(',')
		b.WriteString(categories[i%len(categories)])
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(i % 1000))
		b.WriteString(".50,")
		b.WriteString(regions[i%len(regions)])
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.csv"), []byte(b.String()), 0o644); err != nil {
		tb.Fatal(err)
	}
	return dir
}

func benchmarkQuery(b *testing.B, sql string) {
	dir := generateEvents(b, benchRows)
	eng := New(dir)
	ctx := context.Background()

	// Warm the stats cache and verify the query is valid before timing.
	if _, err := eng.Query(ctx, sql); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.Query(ctx, sql); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	// rows/sec across the scanned table.
	b.ReportMetric(float64(benchRows)*float64(b.N)/b.Elapsed().Seconds(), "rows/sec")
}

// BenchmarkScanFilter measures a full scan with a filter (no aggregation).
func BenchmarkScanFilter(b *testing.B) {
	benchmarkQuery(b, "SELECT id, amount FROM events WHERE amount > 500")
}

// BenchmarkAggregate measures a GROUP BY with multiple aggregates.
func BenchmarkAggregate(b *testing.B) {
	benchmarkQuery(b, "SELECT category, COUNT(*), SUM(amount), AVG(amount) FROM events GROUP BY category")
}

// BenchmarkFilterAggregateSort measures filter + group + order together.
func BenchmarkFilterAggregateSort(b *testing.B) {
	benchmarkQuery(b, "SELECT category, COUNT(*) AS n FROM events WHERE amount > 200 GROUP BY category ORDER BY n DESC")
}

// BenchmarkPlanOnly measures parse + plan + optimize + cost (no execution) —
// the work the rate limiter waits on before admitting a query.
func BenchmarkPlanOnly(b *testing.B) {
	dir := generateEvents(b, benchRows)
	eng := New(dir)
	ctx := context.Background()
	sql := "SELECT category, COUNT(*) FROM events WHERE amount > 200 GROUP BY category"
	if _, err := eng.Cost(ctx, sql); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.Cost(ctx, sql); err != nil {
			b.Fatal(err)
		}
	}
}

// Example_throughput documents how to run the benchmarks.
func Example_throughput() {
	fmt.Println("go test -bench=. -benchmem ./engine/")
	// Output: go test -bench=. -benchmem ./engine/
}
