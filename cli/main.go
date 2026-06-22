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
	"fmt"
	"os"
	"strings"

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
	case "cost":
		return runCost(args[1:])
	case "tables":
		return runTables(args[1:])
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// parseArgs pulls the --data flag out of args, wherever it appears, and returns
// the data directory plus the remaining positional arguments. We hand-roll this
// (rather than use the flag package) so flags can follow the SQL string —
// flag.Parse stops at the first positional, which silently drops a trailing
// --data.
func parseArgs(args []string) (dataDir string, positionals []string, err error) {
	dataDir = "./testdata"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--data" || a == "-data":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--data requires a directory")
			}
			dataDir = args[i+1]
			i++
		case strings.HasPrefix(a, "--data="):
			dataDir = strings.TrimPrefix(a, "--data=")
		case strings.HasPrefix(a, "-data="):
			dataDir = strings.TrimPrefix(a, "-data=")
		case strings.HasPrefix(a, "-"):
			return "", nil, fmt.Errorf("unknown flag %q", a)
		default:
			positionals = append(positionals, a)
		}
	}
	return dataDir, positionals, nil
}

func runQuery(args []string) error {
	dataDir, rest, err := parseArgs(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf(`usage: sluice query [--data DIR] "<SQL>"`)
	}
	result, err := engine.New(dataDir).Query(context.Background(), rest[0])
	if err != nil {
		return err
	}
	fmt.Print(result.String())
	return nil
}

func runExplain(args []string) error {
	// --cost switches to the optimized plan annotated with cost estimates.
	cost := false
	filtered := args[:0:0]
	for _, a := range args {
		if a == "--cost" {
			cost = true
			continue
		}
		filtered = append(filtered, a)
	}

	dataDir, rest, err := parseArgs(filtered)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf(`usage: sluice explain [--cost] [--data DIR] "<SQL>"`)
	}

	eng := engine.New(dataDir)
	var out string
	if cost {
		out, err = eng.ExplainCost(context.Background(), rest[0])
	} else {
		out, err = eng.Explain(rest[0])
	}
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func runCost(args []string) error {
	dataDir, rest, err := parseArgs(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf(`usage: sluice cost [--data DIR] "<SQL>"`)
	}
	c, err := engine.New(dataDir).Cost(context.Background(), rest[0])
	if err != nil {
		return err
	}
	fmt.Printf("%.1f\n", c)
	return nil
}

func runTables(args []string) error {
	dataDir, _, err := parseArgs(args)
	if err != nil {
		return err
	}
	tables, err := engine.New(dataDir).Tables()
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
	fmt.Println(`  sluice query   "SELECT ..."  [--data DIR]            run a query`)
	fmt.Println(`  sluice explain "SELECT ..."  [--cost] [--data DIR]   print the plan (--cost: optimized + cost)`)
	fmt.Println(`  sluice cost    "SELECT ..."  [--data DIR]            print the estimated query cost`)
	fmt.Println(`  sluice tables                [--data DIR]            list available tables`)
	fmt.Println(`  sluice --version`)
}
