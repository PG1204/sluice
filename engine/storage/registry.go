package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Registry resolves table names to data files in a single configured
// directory. A table's name is its file's base name without extension, so
// data/orders.csv is the table "orders". This is the engine's catalog: the
// API's /tables endpoint and the planner's name resolution both go through it.
type Registry struct {
	dir string
}

// supportedExts maps a file extension to how the registry should open it.
// Extending the engine to a new file format means adding one entry here and a
// DataSource implementation — nothing else changes.
var supportedExts = map[string]func(path string) (DataSource, error){
	".csv": func(path string) (DataSource, error) { return OpenCSV(path) },
	".parquet": func(path string) (DataSource, error) {
		return OpenParquet(path)
	},
}

// NewRegistry creates a registry over the given data directory.
func NewRegistry(dir string) *Registry {
	return &Registry{dir: dir}
}

// Tables lists the available table names, sorted. A name backed by more than
// one file (e.g. both orders.csv and orders.parquet) appears once.
func (r *Registry) Tables() ([]string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, fmt.Errorf("list data dir %q: %w", r.dir, err)
	}

	seen := make(map[string]struct{})
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if _, ok := supportedExts[ext]; !ok {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Open returns a DataSource for the named table. It tries each supported
// extension in a deterministic order (CSV before Parquet) and returns the
// first match. The context is accepted for symmetry and future remote sources;
// local file opening is not cancellable.
func (r *Registry) Open(_ context.Context, name string) (DataSource, error) {
	if err := validateTableName(name); err != nil {
		return nil, err
	}

	// Deterministic order so a table backed by multiple files resolves
	// consistently. Matches the "CSV before Parquet" intent.
	for _, ext := range []string{".csv", ".parquet"} {
		path := filepath.Join(r.dir, name+ext)
		if _, err := os.Stat(path); err == nil {
			return supportedExts[ext](path)
		}
	}
	return nil, fmt.Errorf("table %q not found in %q", name, r.dir)
}

// Schema opens the table just long enough to read its schema, then closes it.
// Useful for the /tables endpoint, which wants layouts without reading data.
func (r *Registry) Schema(ctx context.Context, name string) (Schema, error) {
	src, err := r.Open(ctx, name)
	if err != nil {
		return Schema{}, err
	}
	defer src.Close()
	return src.Schema(), nil
}

// validateTableName rejects names that could escape the data directory. Table
// names are simple identifiers, never paths.
func validateTableName(name string) error {
	if name == "" {
		return fmt.Errorf("empty table name")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid table name %q", name)
	}
	return nil
}
