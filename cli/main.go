// Command sluice is the command-line interface to the Sluice query engine.
//
// At this stage it is a scaffold: it knows its version and prints usage.
// The `query` and `explain` subcommands are wired up in Phase 4 (executor)
// and Phase 5 (cost estimator).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/PG1204/sluice/common"
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
	fs := flag.NewFlagSet("sluice", flag.ContinueOnError)
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		fmt.Printf("sluice %s\n", common.Version)
		return nil
	}

	fmt.Printf("sluice %s — cost-aware query engine\n", common.Version)
	fmt.Println("usage: sluice [--version] <command> [args]")
	fmt.Println("commands (coming soon): query, explain, tables")
	return nil
}
