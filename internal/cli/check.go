package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/siddik7181/zdm/internal/preflight"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func check(args []string, s Streams) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	dsn := fs.String("dsn", os.Getenv("ZDM_DSN"), "primary DSN")
	var replicaDSNs listFlag
	fs.Var(&replicaDSNs, "replica-dsn", "gating replica DSN (repeatable)")
	maxLag := fs.Int64("max-lag", 10, "replica lag in seconds that warns")
	longTxn := fs.Int("long-txn", 60, "open-transaction age in seconds that warns")
	timeout := fs.Duration("timeout", 30*time.Second, "overall time limit")
	format := fs.String("format", "text", "text or json")
	failOn := fs.String("fail-on", "fail", "fail, warn or never")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || *dsn == "" || (*format != "text" && *format != "json") ||
		(*failOn != "fail" && *failOn != "warn" && *failOn != "never") ||
		*maxLag < 0 || *longTxn < 1 || *timeout <= 0 {
		usage(s.Err)
		return exitUsage
	}
	replicaDSNs = append(replicaDSNs, strings.Fields(os.Getenv("ZDM_REPLICA_DSNS"))...)

	primary, _, err := preflight.Open(*dsn)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: primary: %v\n", err)
		return exitUsage
	}
	defer func() { _ = primary.Close() }()
	var replicas []preflight.Replica
	for _, d := range replicaDSNs {
		db, addr, err := preflight.Open(d)
		if err != nil {
			fmt.Fprintf(s.Err, "zdm: replica: %v\n", err)
			return exitUsage
		}
		defer func(db *sql.DB) { _ = db.Close() }(db)
		replicas = append(replicas, preflight.Replica{Addr: addr, DB: db})
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	facts, err := preflight.Gather(ctx, primary, replicas, *longTxn)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	results := preflight.Evaluate(facts, preflight.Options{MaxLagSeconds: *maxLag, LongTxnSeconds: *longTxn})

	if *format == "json" {
		enc := json.NewEncoder(s.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fmt.Fprintf(s.Err, "zdm: %v\n", err)
			return exitUsage
		}
	} else {
		for _, r := range results {
			fmt.Fprintf(s.Out, "%-4s  %s: %s\n", strings.ToUpper(r.Status.String()), r.Check, r.Detail)
		}
	}

	worst := preflight.Worst(results)
	if (*failOn == "fail" && worst >= preflight.Fail) || (*failOn == "warn" && worst >= preflight.Warn) {
		return exitFindings
	}
	return exitOK
}
