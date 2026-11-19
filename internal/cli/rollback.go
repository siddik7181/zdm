package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/siddik7181/zdm/internal/cutover"
	"github.com/siddik7181/zdm/internal/plan"
)

// openForCutover loads the plan and opens a writable connection with the plan's lock bound.
func openForCutover(fs *flag.FlagSet, args []string, s Streams) (plan.Plan, string, int) {
	dsn := fs.String("dsn", os.Getenv("ZDM_DSN"), "primary DSN")
	if err := fs.Parse(args); err != nil {
		return plan.Plan{}, "", exitUsage
	}
	if fs.NArg() != 1 || *dsn == "" {
		usage(s.Err)
		return plan.Plan{}, "", exitUsage
	}
	p, err := plan.Load(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return plan.Plan{}, "", exitUsage
	}
	return p, *dsn, exitOK
}

func rollbackCmd(args []string, s Streams) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	accept := fs.Bool("discard-writes-since-swap", false, "acknowledge that newer rows in the new table are not restored")
	p, dsn, code := openForCutover(fs, args, s)
	if code != exitOK {
		return code
	}
	if !*accept {
		fmt.Fprintln(s.Err, "zdm: rollback restores the old table, which lacks every row written since the change; pass --discard-writes-since-swap to confirm")
		return exitUsage
	}
	db, err := cutover.Open(dsn, p.Cutover.LockTimeoutSeconds)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	defer func() { _ = db.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cutover.Rollback(ctx, db, p, cutover.Sleep); err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitFindings
	}
	fmt.Fprintf(s.Out, "rolled back %s.%s; the rejected table is parked as %s\n", p.Schema, p.Table, cutover.RolledBackTable(p))
	return exitOK
}

func finalizeCmd(args []string, s Streams) int {
	fs := flag.NewFlagSet("finalize", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	drop := fs.Bool("drop-old-table", false, "confirm the permanent drop")
	p, dsn, code := openForCutover(fs, args, s)
	if code != exitOK {
		return code
	}
	if !*drop {
		fmt.Fprintf(s.Err, "zdm: this permanently drops %s.%s; pass --drop-old-table to confirm\n", p.Schema, cutover.OldTable(p))
		return exitUsage
	}
	db, err := cutover.Open(dsn, p.Cutover.LockTimeoutSeconds)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	defer func() { _ = db.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cutover.DropOld(ctx, db, p); err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitFindings
	}
	fmt.Fprintf(s.Out, "dropped %s.%s\n", p.Schema, cutover.OldTable(p))
	return exitOK
}
