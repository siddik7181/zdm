package cutover

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"

	"github.com/go-sql-driver/mysql"

	"github.com/siddik7181/zdm/internal/plan"
)

// MySQL errors that mean "INSTANT cannot do this change", so the engine must.
const (
	errAlgorithmNotSupported       = 1845
	errAlgorithmNotSupportedReason = 1846
)

var hasAlgorithm = regexp.MustCompile(`(?i)\bALGORITHM\s*=`)

// ErrNotInstant means the change cannot run as ALGORITHM=INSTANT; nothing was changed.
var ErrNotInstant = errors.New("change cannot run as ALGORITHM=INSTANT")

func notInstant(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == errAlgorithmNotSupported || me.Number == errAlgorithmNotSupportedReason)
}

// TryInstant runs the plan's ALTER with ALGORITHM=INSTANT, which only edits metadata: no copy, no
// triggers, no swap, and no replica lag, even on a table with foreign keys. If MySQL refuses it
// returns ErrNotInstant and changed nothing. A lock timeout is retried with backoff, as a blocked
// ALTER queues other statements for at most lock_timeout_seconds. INPLACE is deliberately not tried:
// it rebuilds the table, and replicas apply that serially, so it would lag them.
func TryInstant(ctx context.Context, db *sql.DB, p plan.Plan, onRetry func(attempt int, err error)) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if hasAlgorithm.MatchString(p.Alter) {
		return ErrNotInstant // the author pinned an algorithm; respect it
	}
	stmt := fmt.Sprintf("ALTER TABLE %s.%s %s, ALGORITHM=INSTANT", quote(p.Schema), quote(p.Table), p.Alter)
	var err error
	for attempt := 1; attempt <= p.Cutover.MaxAttempts; attempt++ {
		_, err = db.ExecContext(ctx, stmt)
		switch {
		case err == nil:
			return nil
		case notInstant(err):
			return ErrNotInstant
		case !retryable(err):
			return err
		}
		if onRetry != nil {
			onRetry(attempt, err)
		}
		if attempt < p.Cutover.MaxAttempts {
			//nolint:gosec // jitter, not security
			if serr := Sleep(ctx, Backoff(p.Cutover, attempt, rand.Float64())); serr != nil {
				return serr
			}
		}
	}
	return fmt.Errorf("instant alter still blocked after %d attempts: %w", p.Cutover.MaxAttempts, err)
}
