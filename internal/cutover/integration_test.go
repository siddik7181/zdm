//go:build integration

package cutover

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/siddik7181/zdm/internal/plan"
)

func setup(t *testing.T, schema string) (*sql.DB, plan.Plan) {
	t.Helper()
	dsn := os.Getenv("ZDM_TEST_DSN")
	if dsn == "" {
		t.Skip("ZDM_TEST_DSN not set")
	}
	db, err := Open(dsn, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + schema); _ = db.Close() })
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + schema, "CREATE DATABASE " + schema,
		"CREATE TABLE " + schema + ".t (id INT PRIMARY KEY, c INT)", // the new table
		"CREATE TABLE " + schema + "._t_del (id INT PRIMARY KEY)",   // what the engine left behind
		"INSERT INTO " + schema + ".t VALUES (1,1),(2,2)", "INSERT INTO " + schema + "._t_del VALUES (1)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p := plan.Default(schema, "t", "ADD COLUMN c INT NULL", plan.GhOst)
	p.Cutover.MaxAttempts = 5
	return db, p
}

func TestRollbackAndIdempotence(t *testing.T) {
	db, p := setup(t, "zdm_cut1")
	ctx := context.Background()
	if err := Rollback(ctx, db, p, Sleep); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM zdm_cut1.t").Scan(&n); err != nil || n != 1 {
		t.Fatalf("restored table rows = %d (%v)", n, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM zdm_cut1._t_rolled_back").Scan(&n); err != nil || n != 2 {
		t.Fatalf("parked table rows = %d (%v)", n, err)
	}
	if err := Rollback(ctx, db, p, Sleep); err != nil {
		t.Fatalf("second rollback must be a no-op: %v", err)
	}
}

func TestRollbackRetriesWhileBlockedThenSucceeds(t *testing.T) {
	db, p := setup(t, "zdm_cut2")
	ctx := context.Background()

	// An open transaction that has read t holds a metadata lock, so the RENAME waits and times out.
	blocker, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close() }()
	if _, err := blocker.ExecContext(ctx, "START TRANSACTION"); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, "SELECT * FROM zdm_cut2.t"); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	sleep := func(ctx context.Context, _ time.Duration) error {
		attempts++
		if attempts == 2 {
			_, _ = blocker.ExecContext(ctx, "COMMIT")
		}
		return nil
	}
	if err := Rollback(ctx, db, p, sleep); err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("expected the rename to be retried, attempts = %d", attempts)
	}
}

func TestRollbackGivesUpAndChangesNothing(t *testing.T) {
	db, p := setup(t, "zdm_cut3")
	p.Cutover.MaxAttempts = 2
	ctx := context.Background()
	blocker, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close() }()
	_, _ = blocker.ExecContext(ctx, "START TRANSACTION")
	_, _ = blocker.ExecContext(ctx, "SELECT * FROM zdm_cut3.t")
	if err := Rollback(ctx, db, p, func(context.Context, time.Duration) error { return nil }); err == nil {
		t.Fatal("want an error when the lock never clears")
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM zdm_cut3.t").Scan(&n); err != nil || n != 2 {
		t.Fatalf("a failed rename must change nothing: rows=%d %v", n, err)
	}
	_, _ = blocker.ExecContext(ctx, "COMMIT")
}

func TestNothingToRollBackAndDropOld(t *testing.T) {
	db, p := setup(t, "zdm_cut4")
	ctx := context.Background()
	if err := DropOld(ctx, db, p); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(ctx, db, p, Sleep); !errors.Is(err, ErrNothingToRollBack) {
		t.Fatalf("want ErrNothingToRollBack, got %v", err)
	}
}

func TestCleanupDropsOnlyEngineHelpers(t *testing.T) {
	db, p := setup(t, "zdm_cut5")
	p.Engine = plan.PtOsc
	ctx := context.Background()
	for _, q := range []string{
		"CREATE TABLE zdm_cut5._t_new (id INT PRIMARY KEY)",
		"CREATE TRIGGER zdm_cut5.pt_osc_zdm_cut5_t_ins AFTER INSERT ON zdm_cut5.t FOR EACH ROW SET @x = 1",
		"CREATE TRIGGER zdm_cut5.pt_osc_zdm_cut5_t_upd AFTER UPDATE ON zdm_cut5.t FOR EACH ROW SET @x = 1",
		"CREATE TRIGGER zdm_cut5.mine AFTER DELETE ON zdm_cut5.t FOR EACH ROW SET @x = 1",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	found, err := Leftovers(ctx, db, p)
	if err != nil || len(found) != 3 {
		t.Fatalf("want 3 leftovers (_t_new + 2 triggers), got %v, %v", found, err)
	}
	if _, err := Cleanup(ctx, db, p, Sleep); err != nil {
		t.Fatal(err)
	}
	found, _ = Leftovers(ctx, db, p)
	if len(found) != 0 {
		t.Fatalf("leftovers remain: %v", found)
	}
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='zdm_cut5' AND TRIGGER_NAME='mine'").Scan(&n)
	ok, _ := exists(ctx, db, "zdm_cut5", "_t_del")
	if n != 1 || !ok {
		t.Fatalf("user trigger or old table was dropped: trigger=%d old=%v", n, ok)
	}
}
