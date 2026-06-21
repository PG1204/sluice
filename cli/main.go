// Command sluice is the command-line interface to the Sluice query engine.
//
// Usage:
//
//	sluice query   "SELECT ..."   [--data DIR]   run a query and print results
//	sluice explain "SELECT ..."   [--data DIR]   print the logical plan
//	sluice tables                 [--data DIR]   list available tables
//	sluice --version
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/PG1204/sluice/common"
	"github.com/PG1204/sluice/engine"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sluice:", err)
		os.Exit(1)
	}
}

// run is the testable entrypoint: it returns an error instead of calling
// os.Exit, so behavior can be asserted in unit tests.
func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "--version", "-v", "version":
		fmt.Printf("sluice %s\n", common.Version)
		return nil
	case "-h", "--help", "help":
		printUsage()
		return nil
	case "query":
		return runQuery(args[1:])
	case "explain":
		return runExplain(args[1:])
	case "tables":
		return runTables(args[1:])
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// queryCommand parses the shared flags for the SQL-taking subcommands and
// returns the engine and the SQL text.
func queryCommand(name string, args []string) (*engine.Engine, string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	dataDir := fs.String("data", "./testdata", "directory of CSV/Parquet table files")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	if fs.NArg() == 0 {
		return nil, "", fmt.Errorf("usage: sluice %s [--data DIR] \"<SQL>\"", name)
	}
	return engine.New(*dataDir), fs.Arg(0), nil
}

func runQuery(args []string) error {
	eng, sql, err := queryCommand("query", args)
	if err != nil {
		return err
	}
	result, err := eng.Query(context.Background(), sql)
	if err != nil {
		return err
	}
	fmt.Print(result.String())
	return nil
}

func runExplain(args []string) error {
	eng, sql, err := queryCommand("explain", args)
	if err != nil {
		return err
	}
	plan, err := eng.Explain(sql)
	if err != nil {
		return err
	}
	fmt.Print(plan)
	return nil
}

func runTables(args []string) error {
	fs := flag.NewFlagSet("tables", flag.ContinueOnError)
	dataDir := fs.String("data", "./testdata", "directory of CSV/Parquet table files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tables, err := engine.New(*dataDir).Tables()
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		fmt.Println("(no tables found)")
		return nil
	}
	for _, t := range tables {
		fmt.Println(t)
	}
	return nil
}

func printUsage() {
	fmt.Printf("sluice %s — cost-aware query engine\n\n", common.Version)
	fmt.Println("usage:")
	fmt.Println(`  sluice query   "SELECT ..."  [--data DIR]   run a query`)
	fmt.Println(`  sluice explain "SELECT ..."  [--data DIR]   print the logical plan`)
	fmt.Println(`  sluice tables                [--data DIR]   list available tables`)
	fmt.Println(`  sluice --version`)
}
