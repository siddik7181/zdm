package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/siddik7181/zdm/internal/cutover"
)

func cleanupCmd(args []string, s Streams) int {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	confirm := fs.Bool("engine-is-stopped", false, "drop the leftovers; only after you checked the engine is not running")
	p, dsn, code := openForCutover(fs, args, s)
	if code != exitOK {
		return code
	}
	db, err := cutover.Open(dsn, p.Cutover.LockTimeoutSeconds)
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitUsage
	}
	defer func() { _ = db.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if !*confirm {
		found, err := cutover.Leftovers(ctx, db, p)
		if err != nil {
			fmt.Fprintf(s.Err, "zdm: %v\n", err)
			return exitFindings
		}
		for _, l := range found {
			fmt.Fprintf(s.Out, "leftover %s %s\n", l.Kind, l.Name)
		}
		fmt.Fprintf(s.Out, "%d leftover(s); nothing dropped. Dropping during a live migration breaks it: once the engine is stopped, add --engine-is-stopped\n", len(found))
		return exitOK
	}
	found, err := cutover.Cleanup(ctx, db, p, cutover.Sleep)
	for _, l := range found {
		fmt.Fprintf(s.Out, "dropped %s %s\n", l.Kind, l.Name)
	}
	if err != nil {
		fmt.Fprintf(s.Err, "zdm: %v\n", err)
		return exitFindings
	}
	return exitOK
}
