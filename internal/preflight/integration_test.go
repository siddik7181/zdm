//go:build integration

package preflight

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// Needs the docker lab: `make lab-up`, then `make test-integration`.
func env(t *testing.T, k string) string {
	t.Helper()
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("%s not set", k)
	}
	return v
}

func TestAgainstLab(t *testing.T) {
	primaryDSN, replicaDSN := env(t, "ZDM_TEST_DSN"), env(t, "ZDM_TEST_REPLICA_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	primary, _, err := Open(primaryDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = primary.Close() }()
	rdb, raddr, err := Open(replicaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rdb.Close() }()

	// The session must be read-only at the server, whatever the code does.
	if _, err := primary.ExecContext(ctx, "CREATE DATABASE zdm_must_not_exist"); err == nil {
		_, _ = primary.ExecContext(ctx, "DROP DATABASE zdm_must_not_exist")
		t.Fatal("write succeeded on a connection that must be read-only")
	}

	// Hold a transaction open on a separate, writable connection.
	holder, _, err := Open(primaryDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)

	facts, err := Gather(ctx, primary, []Replica{{Addr: raddr, DB: rdb}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	results := Evaluate(facts, Options{MaxLagSeconds: 30, LongTxnSeconds: 1})
	for _, r := range results {
		t.Logf("%s %s: %s", r.Status, r.Check, r.Detail)
	}
	if find(t, results, "version").Status != Pass {
		t.Error("lab version should be a supported LTS")
	}
	if find(t, results, "long_transactions").Status != Warn {
		t.Error("the held transaction should be reported")
	}
	if got := find(t, results, "replica "+raddr).Status; got == Fail {
		t.Errorf("lab replica should be healthy, got %v", got)
	}
	if w := Worst(results); w == Fail {
		t.Errorf("lab primary should have no failing check")
	}
}

func TestWaitQuietSeesMetadataLockHolders(t *testing.T) {
	dsn := env(t, "ZDM_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	for _, q := range []string{
		"DROP DATABASE IF EXISTS zdm_wq", "CREATE DATABASE zdm_wq",
		"CREATE TABLE zdm_wq.t (id INT PRIMARY KEY)", "INSERT INTO zdm_wq.t VALUES (1)",
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE zdm_wq") }()
	db, _, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if err := WaitQuiet(ctx, db, "zdm_wq", "t", 1, 100*time.Millisecond, func(Txn) { t.Error("nothing holds the table") }); err != nil {
		t.Fatal(err)
	}

	// A transaction that read the table keeps its lock until it commits.
	conn, err := admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	for _, q := range []string{"START TRANSACTION", "SELECT * FROM zdm_wq.t"} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(2 * time.Second)
	waits := 0
	release := time.AfterFunc(3*time.Second, func() { _, _ = conn.ExecContext(context.Background(), "COMMIT") })
	defer release.Stop()
	start := time.Now()
	if err := WaitQuiet(ctx, db, "zdm_wq", "t", 1, 500*time.Millisecond, func(Txn) { waits++ }); err != nil {
		t.Fatal(err)
	}
	if waits == 0 || time.Since(start) < 500*time.Millisecond {
		t.Errorf("returned while the transaction was open: waits=%d after %v", waits, time.Since(start))
	}

	// A long autocommit SELECT holds the lock too, and has no row in innodb_trx.
	go func() { _, _ = admin.ExecContext(ctx, "SELECT SLEEP(4) FROM zdm_wq.t") }()
	time.Sleep(2500 * time.Millisecond)
	start = time.Now()
	if err := WaitQuiet(ctx, db, "zdm_wq", "t", 1, 300*time.Millisecond, func(Txn) {}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Errorf("a running SELECT must hold the swap back, waited %v", time.Since(start))
	}

	cctx, ccancel := context.WithCancel(ctx)
	go func() { _, _ = admin.ExecContext(ctx, "SELECT SLEEP(4) FROM zdm_wq.t") }()
	time.Sleep(2500 * time.Millisecond)
	time.AfterFunc(500*time.Millisecond, ccancel)
	if err := WaitQuiet(cctx, db, "zdm_wq", "t", 1, 100*time.Millisecond, func(Txn) {}); err == nil {
		t.Error("a cancelled wait must return the context error")
	}
}
