package cli

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/siddik7181/zdm/internal/advise"
	"github.com/siddik7181/zdm/internal/engine"
	"github.com/siddik7181/zdm/internal/plan"
	"github.com/siddik7181/zdm/internal/preflight"
	"github.com/siddik7181/zdm/internal/runner"
	"github.com/siddik7181/zdm/internal/state"
)

func statePath(planPath string) string { return planPath + ".state" }

func planCmd(args []string, s Streams) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	dsn := fs.String("dsn", os.Getenv("ZDM_DSN"), "primary DSN")
	schema := fs.String("schema", "", "schema name")
	table := fs.String("table", "", "table name")
	alter := fs.String("alter", "", `ALTER clauses, e.g. "ADD COLUMN c INT NULL"`)
	eng := fs.String("engine", "auto", "auto, gh-ost or pt-osc")
	out := fs.String("out", "zdm-plan.json", "plan file to write")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || *dsn == "" || (*eng != "auto" && *eng != plan.GhOst && *eng != plan.PtOsc) {
		usage(s.Err)
		return exitUsage
	}

	db, _, err := preflight.Open(*dsn)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The identifier check in Validate runs before the names reach any query.
	p := plan.Default(*schema, *table, *alter, plan.GhOst)
	if err := p.Validate(); err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	facts, err := advise.Gather(ctx, db, *schema, *table)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	choice := advise.Choose(facts)
	if choice.Refuse {
		fmt.Fprintf(s.Err, "zdm: cannot plan: %s\n", strings.Join(choice.Reasons, "; "))
		return exitFindings
	}
	p.Engine = choice.Engine
	if *eng != "auto" {
		p.Engine = *eng
		if *eng != choice.Engine {
			fmt.Fprintf(s.Err, "zdm: warning: %s is recommended, you forced %s (%s)\n", choice.Engine, *eng, strings.Join(choice.Reasons, "; "))
		}
	}
	if err := p.Save(*out); err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(s.Out, "wrote %s: engine %s for %s.%s (~%d rows)\n", *out, p.Engine, p.Schema, p.Table, facts.Rows)
	for _, r := range choice.Reasons {
		fmt.Fprintf(s.Out, "  - %s\n", r)
	}
	return exitOK
}

func statusCmd(args []string, s Streams) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		usage(s.Err)
		return exitUsage
	}
	p, err := plan.Load(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	st, err := state.Load(statePath(fs.Arg(0)), p.Hash())
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(s.Out, "%s.%s via %s: next phase %s, completed %v\n", p.Schema, p.Table, p.Engine, st.Next(), st.Completed)
	return exitOK
}

// connFromDSN extracts what the engine needs. The password stays out of any output.
func connFromDSN(dsn string) (engine.Conn, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return engine.Conn{}, errors.New("invalid DSN")
	}
	host, portStr, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return engine.Conn{}, errors.New("DSN must use tcp(host:port)")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return engine.Conn{}, errors.New("DSN has an invalid port")
	}
	return engine.Conn{Host: host, Port: port, User: cfg.User, Password: cfg.Passwd}, nil
}

// longTxnSeconds is the age past which an open transaction counts as able to block the swap.
const longTxnSeconds = 60

func runCmd(args []string, s Streams) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	dsn := fs.String("dsn", os.Getenv("ZDM_DSN"), "primary DSN")
	replicaDSN := fs.String("replica-dsn", "", "replica DSN; gh-ost reads the binlog from it")
	execute := fs.Bool("execute", false, "really run; without it only preflight and an engine dry run happen")
	behindProxy := fs.Bool("behind-proxy", false, "skip gh-ost's port check (Docker port mapping, proxies)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 || *dsn == "" {
		usage(s.Err)
		return exitUsage
	}
	planPath := fs.Arg(0)
	p, err := plan.Load(planPath)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}

	primary, _, err := preflight.Open(*dsn)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: primary: %v\n", err)
		return exitUsage
	}
	defer func() { _ = primary.Close() }()
	conn, err := connFromDSN(*dsn)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: primary: %v\n", err)
		return exitUsage
	}
	conn.BehindProxy = *behindProxy
	var replicas []preflight.Replica
	if *replicaDSN != "" {
		db, addr, err := preflight.Open(*replicaDSN)
		if err != nil {
			fmt.Fprintf(s.Err, "zdm: replica: %v\n", err)
			return exitUsage
		}
		defer func() { _ = db.Close() }()
		replicas = append(replicas, preflight.Replica{Addr: addr, DB: db})
		rc, err := connFromDSN(*replicaDSN)
		if err != nil {
			fmt.Fprintf(s.Err, "zdm: replica: %v\n", err)
			return exitUsage
		}
		rc.PrimaryHost, rc.BehindProxy = net.JoinHostPort(conn.Host, strconv.Itoa(conn.Port)), *behindProxy
		if p.Engine == plan.GhOst {
			conn = rc
		} else { // pt-osc writes, so it stays on the primary and only throttles on the replica
			conn.ReplicaHost, conn.ReplicaPort = rc.Host, rc.Port
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log := func(m string) { fmt.Fprintln(s.Out, m) }

	var resumed bool
	steps := runner.Steps{
		state.Preflight: func(ctx context.Context) error { return runPreflight(ctx, primary, replicas, p) },
		state.Migrate: func(ctx context.Context) error {
			var gateErr chan error
			conn.Resume = resumed
			onLine := func(l string) { fmt.Fprintln(s.Out, "  | "+l) }
			if p.Engine == plan.GhOst {
				gateErr = make(chan error, 1)
				dir, err := os.MkdirTemp("", "zdm-gate-")
				if err != nil {
					return err
				}
				defer func() { _ = os.RemoveAll(dir) }()
				conn.PostponeFlag = filepath.Join(dir, "postpone")
				var once sync.Once
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				onLine = func(l string) {
					fmt.Fprintln(s.Out, "  | "+l)
					if strings.Contains(l, "Row copy complete") {
						once.Do(func() { go func() { gateErr <- openGate(ctx, primary, p, conn.PostponeFlag, s.Out, cancel) }() })
					}
				}
			}
			spec, err := engine.Build(p, conn, true)
			if err != nil {
				return err
			}
			defer spec.Cleanup()
			err = engine.Run(ctx, spec, p.Stall(), onLine)
			select {
			case ge := <-gateErr:
				if ge != nil {
					return ge
				}
			default:
			}
			return err
		},
		state.Verify: func(ctx context.Context) error { return runner.PostCheck(ctx, primary, p) },
	}

	if !*execute {
		return dryRun(ctx, s, steps, p, conn)
	}
	st, err := state.Load(statePath(planPath), p.Hash())
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	resumed = len(st.Completed) > 0
	if _, err := runner.Run(ctx, st, statePath(planPath), steps, time.Now, log); err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitFindings
	}
	log("done")
	return exitOK
}

func dryRun(ctx context.Context, s Streams, steps runner.Steps, p plan.Plan, conn engine.Conn) int {
	fmt.Fprintln(s.Out, "dry run: nothing is changed and no state is saved")
	if err := steps[state.Preflight](ctx); err != nil {
		fmt.Fprintf(s.Err, "zdm: preflight: %v\n", err)
		return exitFindings
	}
	fmt.Fprintln(s.Out, "preflight: ok")
	spec, err := engine.Build(p, conn, false)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	defer spec.Cleanup()
	if err := engine.Run(ctx, spec, p.Stall(), func(l string) { fmt.Fprintln(s.Out, "  | "+l) }); err != nil {
		fmt.Fprintf(s.Err, "zdm: engine dry run: %v\n", err)
		return exitFindings
	}
	fmt.Fprintln(s.Out, "engine dry run: ok")
	return exitOK
}

func runPreflight(ctx context.Context, primary *sql.DB, replicas []preflight.Replica, p plan.Plan) error {
	facts, err := preflight.Gather(ctx, primary, replicas, longTxnSeconds)
	if err != nil {
		return err
	}
	var failed []string
	for _, r := range preflight.Evaluate(facts, preflight.Options{MaxLagSeconds: 10, LongTxnSeconds: longTxnSeconds}) {
		if r.Status == preflight.Fail {
			failed = append(failed, r.Check+": "+r.Detail)
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	tf, err := advise.Gather(ctx, primary, p.Schema, p.Table)
	if err != nil {
		return err
	}
	if c := advise.Choose(tf); c.Refuse || (p.Engine == plan.GhOst && c.Engine == plan.PtOsc) {
		return fmt.Errorf("plan engine %s is not safe for this table: %s", p.Engine, strings.Join(c.Reasons, "; "))
	}
	return nil
}

// openGate lets gh-ost cut over once no long transaction could hold the swap back. If the check
// itself fails, the cut-over is still released: gh-ost's own lock timeout bounds the damage. If holders
// outlast the plan's deadline it stops the run instead, which leaves the table untouched.
func openGate(ctx context.Context, db *sql.DB, p plan.Plan, flag string, out io.Writer, stopRun func()) error {
	c := p.Cutover
	err := preflight.WaitQuiet(ctx, db, p.Schema, p.Table, preflight.Gate{
		// A holder older than the lock timeout would make the swap attempt fail, so that is the bar.
		Seconds: c.LockTimeoutSeconds, Poll: 5 * time.Second,
		MaxWait:   time.Duration(c.GateMaxWaitSeconds) * time.Second,
		KillAfter: time.Duration(c.KillIdleAfterSeconds) * time.Second,
		OnWait: func(t preflight.Txn) {
			fmt.Fprintf(out, "cut-over held: session %s has held a lock on the table for %ds\n", t.ID, t.AgeSeconds)
		},
		OnKill: func(t preflight.Txn, err error) {
			if err != nil {
				fmt.Fprintf(out, "zdm: could not end idle read-only session %s: %v\n", t.ID, err)
				return
			}
			fmt.Fprintf(out, "zdm: ended session %s (idle in a read-only transaction for %ds, nothing written)\n", t.ID, t.AgeSeconds)
		},
	})
	switch {
	case errors.Is(err, preflight.ErrGateTimeout):
		stopRun()
		return fmt.Errorf("cut-over not started: %w after %ds; the table is unchanged, run zdm cleanup to remove the shadow table", err, c.GateMaxWaitSeconds)
	case err != nil && ctx.Err() != nil:
		return nil
	case err != nil:
		fmt.Fprintf(out, "zdm: %v; releasing cut-over anyway\n", err)
	}
	_ = os.Remove(flag) //nolint:gosec // flag is the temp path zdm itself chose
	return nil
}
