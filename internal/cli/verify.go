package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/siddik7181/zdm/internal/cutover"
	"github.com/siddik7181/zdm/internal/plan"
	"github.com/siddik7181/zdm/internal/preflight"
	"github.com/siddik7181/zdm/internal/verify"
)

func verifyCmd(args []string, s Streams) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	dsn := fs.String("dsn", os.Getenv("ZDM_DSN"), "primary DSN")
	chunk := fs.Int("chunk", 5000, "rows per checksum chunk")
	timeout := fs.Duration("timeout", 30*time.Minute, "overall time limit")
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 || *dsn == "" || *chunk < 1 || (*format != "text" && *format != "json") {
		usage(s.Err)
		return exitUsage
	}
	p, err := plan.Load(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	db, _, err := preflight.Open(*dsn) // read-only session
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	defer func() { _ = db.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	old := cutover.OldTable(p)
	res, err := verify.Compare(ctx, db, p.Schema, old, p.Table, *chunk)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	if res.Mismatches == nil {
		res.Mismatches = []verify.Mismatch{}
	}
	if *format == "json" {
		enc := json.NewEncoder(s.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintf(s.Err, "zdm: %v\n", err)
			return exitUsage
		}
	} else {
		fmt.Fprintf(s.Out, "compared %s with %s on %d columns: %d rows in %d chunks, %d differ\n",
			old, p.Table, len(res.Columns), res.Rows, res.Chunks, len(res.Mismatches))
		for _, m := range res.Mismatches {
			fmt.Fprintf(s.Out, "  keys (%s, %s]: rows %d vs %d\n", m.After, m.Through, m.RowsA, m.RowsB)
		}
	}
	if !res.OK() {
		return exitFindings
	}
	return exitOK
}
