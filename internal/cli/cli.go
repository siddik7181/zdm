// Package cli implements the zdm command line.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/siddik7181/zdm/internal/parse"
	"github.com/siddik7181/zdm/internal/rules"
	"github.com/siddik7181/zdm/internal/version"
)

// Exit codes.
const (
	exitOK       = 0
	exitFindings = 1 // lint found something at or above --fail-on
	exitUsage    = 2 // bad arguments or unreadable input
)

// maxInput bounds how much SQL lint will read, so a bad pipe cannot exhaust memory.
const maxInput = 64 << 20

// Streams carries the streams a command uses, so tests can fake them.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Run executes the command line and returns the process exit code.
func Run(args []string, s Streams) int {
	if len(args) == 0 {
		usage(s.Err)
		return exitUsage
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(s.Out, version.String())
		return exitOK
	case "lint":
		return lint(args[1:], s)
	case "check":
		return check(args[1:], s)
	case "plan":
		return planCmd(args[1:], s)
	case "run":
		return runCmd(args[1:], s)
	case "verify":
		return verifyCmd(args[1:], s)
	case "rollback":
		return rollbackCmd(args[1:], s)
	case "finalize":
		return finalizeCmd(args[1:], s)
	case "status":
		return statusCmd(args[1:], s)
	case "help", "-h", "--help":
		usage(s.Out)
		return exitOK
	}
	fmt.Fprintf(s.Err, "zdm: unknown command %q\n", args[0])
	usage(s.Err)
	return exitUsage
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: zdm <command>

commands:
  lint <file|->   check a SQL migration for risky changes (reads stdin for "-")
  check           read-only preflight of a live primary and its replicas
  plan            inspect a table and write a plan file with the chosen engine
  run <plan>      run a plan: preflight, engine, post-check; resumable
  status <plan>   show which phase a plan is at
  verify <plan>   checksum the new table against the kept old table (only meaningful when both are idle)
  rollback <plan> put the old table back (atomic rename, bounded lock wait, retried)
  finalize <plan> drop the old table for good (irreversible, needs a flag)
  version         print the version

plan flags:
  --dsn DSN --schema S --table T --alter "ADD COLUMN ..." [--engine auto|gh-ost|pt-osc] [--out FILE]

run flags:
  --dsn DSN                 primary (default $ZDM_DSN)
  --replica-dsn DSN         replica the engine reads from (gh-ost)
  --execute                 really run; without it: preflight + engine dry run, no state saved
  --behind-proxy            skip gh-ost's port check (Docker port mapping, proxies)

verify flags:
  --dsn DSN  --chunk ROWS (default 5000)  --timeout DURATION  --format text|json

rollback flags:
  --dsn DSN                 primary (default $ZDM_DSN)
  --discard-writes-since-swap
                            required: rows written to the new table since the change are not in the old one

finalize flags:
  --dsn DSN                 primary (default $ZDM_DSN)
  --drop-old-table          required: permanently drops the old table

lint flags:
  --format text|json        output format (default text)
  --fail-on error|warning|info|never
                            exit 1 when a finding is at least this severe (default error)

check flags:
  --dsn DSN                 primary, user:pass@tcp(host:3306)/ (default $ZDM_DSN)
  --replica-dsn DSN         gating replica; repeatable (also $ZDM_REPLICA_DSNS, whitespace separated)
  --max-lag SECONDS         replica lag that warns (default 10)
  --long-txn SECONDS        open-transaction age that warns (default 60)
  --timeout DURATION        overall time limit (default 30s)
  --format text|json
  --fail-on fail|warn|never exit 1 at this status or worse (default fail)
`)
}

func lint(args []string, s Streams) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	format := fs.String("format", "text", "text or json")
	failOn := fs.String("fail-on", "error", "error, warning, info or never")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 || (*format != "text" && *format != "json") {
		usage(s.Err)
		return exitUsage
	}
	threshold, ok := parseThreshold(*failOn)
	if !ok {
		fmt.Fprintf(s.Err, "zdm: invalid --fail-on %q\n", *failOn)
		return exitUsage
	}

	name := fs.Arg(0)
	sql, err := readInput(name, s.In)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}

	findings := rules.Check(parse.Script(sql))
	if *format == "json" {
		if findings == nil {
			findings = []rules.Finding{}
		}
		enc := json.NewEncoder(s.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(findings); err != nil {
			fmt.Fprintf(s.Err, "zdm: %v\n", err)
			return exitUsage
		}
	} else {
		for _, f := range findings {
			fmt.Fprintf(s.Out, "%s:%d: %s [%s] %s\n    fix: %s\n", name, f.Line, f.Severity, f.Rule, f.Why, f.Fix)
		}
	}

	if threshold >= 0 {
		for _, f := range findings {
			if f.Severity >= threshold {
				return exitFindings
			}
		}
	}
	return exitOK
}

// parseThreshold returns -1 for "never".
func parseThreshold(s string) (rules.Severity, bool) {
	switch s {
	case "error":
		return rules.Error, true
	case "warning":
		return rules.Warning, true
	case "info":
		return rules.Info, true
	case "never":
		return -1, true
	}
	return 0, false
}

func readInput(name string, stdin io.Reader) (string, error) {
	if name == "-" {
		return readBounded(stdin)
	}
	f, err := os.Open(name) //nolint:gosec // reading the user-supplied path is the command's purpose
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return readBounded(f)
}

func readBounded(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxInput {
		return "", errors.New("input larger than 64 MiB")
	}
	return string(b), nil
}
