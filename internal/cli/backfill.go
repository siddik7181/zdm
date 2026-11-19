package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/siddik7181/zdm/internal/backfill"
	"github.com/siddik7181/zdm/internal/preflight"
)

func backfillCmd(args []string, s Streams) int {
	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	dsn := fs.String("dsn", os.Getenv("ZDM_DSN"), "primary DSN")
	var replicaDSNs listFlag
	fs.Var(&replicaDSNs, "replica-dsn", "replica to throttle on (repeatable)")
	c := backfill.Config{}
	fs.StringVar(&c.Schema, "schema", "", "schema name")
	fs.StringVar(&c.Table, "table", "", "table name")
	fs.StringVar(&c.Set, "set", "", `SET clause, e.g. "status = COALESCE(status, 'new')" (must be idempotent)`)
	fs.StringVar(&c.Where, "where", "", "extra filter, e.g. \"status IS NULL\"")
	fs.IntVar(&c.Chunk, "chunk", 1000, "rows per chunk")
	fs.DurationVar(&c.Pause, "pause", 100*time.Millisecond, "sleep between chunks")
	fs.DurationVar(&c.MaxLag, "max-lag", 5*time.Second, "wait while a replica is further behind than this")
	fs.BoolVar(&c.Execute, "execute", false, "really update rows; without it only counts what would change")
	fs.StringVar(&c.Checkpoint, "checkpoint", "", "resume file (default zdm-backfill-<schema>.<table>.json)")
	fs.StringVar(&c.StopFile, "stop-file", "", "creating this file stops the run before the next chunk")
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || *dsn == "" || (*format != "text" && *format != "json") {
		usage(s.Err)
		return exitUsage
	}
	if c.Checkpoint == "" {
		c.Checkpoint = fmt.Sprintf("zdm-backfill-%s.%s.json", c.Schema, c.Table)
	}
	replicaDSNs = append(replicaDSNs, strings.Fields(os.Getenv("ZDM_REPLICA_DSNS"))...)
	if err := c.Validate(); err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	if c.Execute && len(replicaDSNs) == 0 {
		fmt.Fprintln(s.Err, "zdm: warning: no --replica-dsn, so replica lag will not throttle this backfill")
	}

	db, err := backfill.Open(*dsn)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	defer func() { _ = db.Close() }()
	var replicas []preflight.Replica
	for _, d := range replicaDSNs {
		rdb, addr, err := preflight.Open(d)
		if err != nil {
			fmt.Fprintf(s.Err, "zdm: replica: %v\n", err)
			return exitUsage
		}
		defer func(db *sql.DB) { _ = db.Close() }(rdb)
		replicas = append(replicas, preflight.Replica{Addr: addr, DB: rdb})
	}
	var lag backfill.Lag
	if len(replicas) > 0 {
		lag = backfill.ReplicaLag(replicas)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if !c.Execute {
		fmt.Fprintln(s.Out, "dry run: counting only, nothing is changed (add --execute to update rows)")
	}
	rep, err := backfill.Run(ctx, db, c, lag, func(m string) { fmt.Fprintln(s.Out, m) })
	if *format == "json" {
		enc := json.NewEncoder(s.Out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		fmt.Fprintf(s.Out, "chunks %d, rows %d, done %v\n", rep.Chunks, rep.Rows, rep.Done)
		if rep.Stopped != "" {
			fmt.Fprintf(s.Out, "stopped: %s\n", rep.Stopped)
		}
	}
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitFindings
	}
	return exitOK
}
