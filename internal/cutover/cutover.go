// Package cutover swaps tables back and cleans up after a change. The swap is one atomic
// RENAME TABLE under a short lock_wait_timeout, so a blocked attempt changes nothing and can be retried.
package cutover

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/siddik7181/zdm/internal/plan"
)

// MySQL error numbers that mean "try again later", not "this is wrong".
const (
	errLockWaitTimeout = 1205
	errDeadlock        = 1213
)

// Open returns a writable connection whose lock_wait_timeout is set at connect time. It is set in the
// DSN, not with SET inside a transaction, so every pooled connection has it.
func Open(dsn string, lockTimeoutSeconds int) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid DSN")
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["lock_wait_timeout"] = fmt.Sprint(lockTimeoutSeconds)
	cfg.Timeout = 10 * time.Second
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

// OldTable is the name the engine gave the pre-change table.
func OldTable(p plan.Plan) string {
	if p.Engine == plan.PtOsc {
		return "_" + p.Table + "_old"
	}
	return "_" + p.Table + "_del"
}

// RolledBackTable is where the rejected new table is parked, so nothing is dropped.
func RolledBackTable(p plan.Plan) string { return "_" + p.Table + "_rolled_back" }

func quote(s string) string { return "`" + s + "`" } // names are validated identifiers, so no backtick can occur

func retryable(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == errLockWaitTimeout || me.Number == errDeadlock)
}

func exists(ctx context.Context, db *sql.DB, schema, table string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?`, schema, table).Scan(&n)
	return n > 0, err
}

// ErrNothingToRollBack means the old table is gone and no rollback was recorded.
var ErrNothingToRollBack = errors.New("old table does not exist; there is nothing to roll back to")

// Rollback puts the old table back and parks the current one as _<table>_rolled_back.
// Rows written to the current table since the change are NOT in the old table. The caller must have
// accepted that. It inspects the schema before every attempt, so it is safe to run again after a crash.
func Rollback(ctx context.Context, db *sql.DB, p plan.Plan, sleep func(context.Context, time.Duration) error) error {
	if err := p.Validate(); err != nil {
		return err
	}
	old, parked := OldTable(p), RolledBackTable(p)
	if len(parked) > 64 {
		return errors.New("table name too long to park the rolled-back table")
	}
	stmt := fmt.Sprintf("RENAME TABLE %s.%s TO %s.%s, %s.%s TO %s.%s",
		quote(p.Schema), quote(p.Table), quote(p.Schema), quote(parked),
		quote(p.Schema), quote(old), quote(p.Schema), quote(p.Table))

	for attempt := 1; ; attempt++ {
		cur, err := exists(ctx, db, p.Schema, p.Table)
		if err != nil {
			return err
		}
		hasOld, err := exists(ctx, db, p.Schema, old)
		if err != nil {
			return err
		}
		hasParked, err := exists(ctx, db, p.Schema, parked)
		if err != nil {
			return err
		}
		switch {
		case !cur:
			return fmt.Errorf("table %s.%s does not exist; refusing to guess", p.Schema, p.Table)
		case !hasOld && hasParked:
			return nil // a previous attempt already completed
		case !hasOld:
			return ErrNothingToRollBack
		case hasParked:
			return fmt.Errorf("%s.%s already exists; remove or rename it first", p.Schema, parked)
		}

		_, err = db.ExecContext(ctx, stmt)
		if err == nil {
			return nil
		}
		if !retryable(err) || attempt >= p.Cutover.MaxAttempts {
			return fmt.Errorf("rename (attempt %d of %d): %w", attempt, p.Cutover.MaxAttempts, err)
		}
		//nolint:gosec // jitter, not security
		if err := sleep(ctx, Backoff(p.Cutover, attempt, rand.Float64())); err != nil {
			return err
		}
	}
}

// Backoff is the wait after the given failed attempt (1-based): base doubling up to the cap, then
// half fixed and half scaled by r in [0,1) so concurrent retriers spread out but never wait zero.
func Backoff(c plan.Cutover, attempt int, r float64) time.Duration {
	d := time.Duration(c.BackoffSeconds) * time.Second
	limit := time.Duration(c.MaxBackoffSeconds) * time.Second
	for i := 1; i < attempt && d < limit; i++ {
		d *= 2
	}
	d = min(d, limit)
	return d/2 + time.Duration(float64(d/2)*r)
}

// Sleep waits for d or until the context ends.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// DropOld permanently removes the old table. It is the one irreversible step, so callers gate it.
func DropOld(ctx context.Context, db *sql.DB, p plan.Plan) error {
	if err := p.Validate(); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s.%s", quote(p.Schema), quote(OldTable(p))))
	return err
}
