//go:build integration

package backfill

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/siddik7181/zdm/internal/preflight"
)

func lab(t *testing.T) (*sql.DB, Config) {
	t.Helper()
	dsn := os.Getenv("ZDM_TEST_DSN")
	if dsn == "" {
		t.Skip("ZDM_TEST_DSN not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	const schema = "zdm_backfill_test"
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + schema, "CREATE DATABASE " + schema,
		"CREATE TABLE " + schema + ".t (id INT NOT NULL PRIMARY KEY, c INT NULL)",
		"INSERT INTO " + schema + ".t (id) WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1000) SELECT i FROM n",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + schema); _ = db.Close() })
	dir := t.TempDir()
	return db, Config{
		Schema: schema, Table: "t", Set: "c = 1", Where: "c IS NULL", Chunk: 100,
		Checkpoint: filepath.Join(dir, "cp.json"), StopFile: filepath.Join(dir, "stop"),
	}
}

func filled(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM zdm_backfill_test.t WHERE c = 1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDryRunChangesNothingThenExecuteFillsEverything(t *testing.T) {
	db, c := lab(t)
	ctx := context.Background()
	rep, err := Run(ctx, db, c, nil, func(string) {})
	if err != nil || !rep.Done || rep.Rows != 1000 || rep.Chunks != 11 || filled(t, db) != 0 {
		t.Fatalf("dry run: %+v %v filled=%d", rep, err, filled(t, db))
	}
	if _, err := os.Stat(c.Checkpoint); err == nil {
		t.Fatal("a dry run must not write a checkpoint")
	}
	c.Execute = true
	rep, err = Run(ctx, db, c, nil, func(string) {})
	if err != nil || !rep.Done || rep.Rows != 1000 || filled(t, db) != 1000 {
		t.Fatalf("execute: %+v %v filled=%d", rep, err, filled(t, db))
	}
	again, err := Run(ctx, db, c, nil, func(string) {})
	if err != nil || !again.Done || !again.Resumed || again.Chunks != 0 {
		t.Fatalf("finished backfill must not run again: %+v %v", again, err)
	}
}

func TestStopFileHaltsAndResumeFinishes(t *testing.T) {
	db, c := lab(t)
	c.Execute = true
	ctx := context.Background()
	calls := 0
	lag := func(context.Context) (time.Duration, error) {
		calls++
		if calls == 3 {
			_ = os.WriteFile(c.StopFile, nil, 0o600) // the operator pulls the kill switch
		}
		return 0, nil
	}
	rep, err := Run(ctx, db, c, lag, func(string) {})
	if err != nil || rep.Stopped == "" || rep.Done || rep.Chunks != 3 || filled(t, db) != 300 {
		t.Fatalf("stopped run: %+v %v filled=%d", rep, err, filled(t, db))
	}
	if err := os.Remove(c.StopFile); err != nil {
		t.Fatal(err)
	}
	rep, err = Run(ctx, db, c, nil, func(string) {})
	if err != nil || !rep.Done || !rep.Resumed || rep.Rows != 700 || filled(t, db) != 1000 {
		t.Fatalf("resume: %+v %v filled=%d", rep, err, filled(t, db))
	}
}

func TestLagThrottlesAndUnknownLagStops(t *testing.T) {
	db, c := lab(t)
	c.Execute, c.Checkpoint, c.MaxLag = true, "", time.Second
	var logs []string
	n := 0
	lag := func(context.Context) (time.Duration, error) {
		n++
		if n == 1 {
			return 5 * time.Second, nil
		}
		return 0, nil
	}
	rep, err := Run(context.Background(), db, c, lag, func(s string) { logs = append(logs, s) })
	if err != nil || !rep.Done || len(logs) != 1 || !strings.Contains(logs[0], "waiting") {
		t.Fatalf("throttle: %+v %v %v", rep, err, logs)
	}

	c.Set, c.Where = "c = 2", ""
	if _, err := db.Exec("UPDATE zdm_backfill_test.t SET c = 1"); err != nil {
		t.Fatal(err)
	}
	broken := func(context.Context) (time.Duration, error) { return 0, os.ErrDeadlineExceeded }
	if _, err := Run(context.Background(), db, c, broken, func(string) {}); err == nil || filled(t, db) != 1000 {
		t.Fatalf("unknown lag must stop before writing: %v", err)
	}
}

func TestReplicaLagAgainstLab(t *testing.T) {
	rdsn := os.Getenv("ZDM_TEST_REPLICA_DSN")
	if rdsn == "" {
		t.Skip("ZDM_TEST_REPLICA_DSN not set")
	}
	db, addr, err := preflight.Open(rdsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	l, err := ReplicaLag([]preflight.Replica{{Addr: addr, DB: db}})(context.Background())
	if err != nil || l > 30*time.Second {
		t.Fatalf("lag = %v err = %v", l, err)
	}
}
