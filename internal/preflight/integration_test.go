//go:build integration

package preflight

import (
	"context"
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

func TestWaitQuietBlocksUntilTxnEnds(t *testing.T) {
	dsn := env(t, "ZDM_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, _, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	holder, _, err := Open(dsn)
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

	waits := 0
	release := time.AfterFunc(3*time.Second, func() { _, _ = conn.ExecContext(context.Background(), "COMMIT") })
	defer release.Stop()
	start := time.Now()
	if err := WaitQuiet(ctx, db, 1, 500*time.Millisecond, func(Txn) { waits++ }); err != nil {
		t.Fatal(err)
	}
	if waits == 0 || time.Since(start) < 2*time.Second {
		t.Errorf("returned while the transaction was open: waits=%d after %v", waits, time.Since(start))
	}

	cctx, ccancel := context.WithCancel(ctx)
	if _, err := conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	time.AfterFunc(time.Second, ccancel)
	if err := WaitQuiet(cctx, db, 1, 200*time.Millisecond, func(Txn) {}); err == nil {
		t.Error("a cancelled wait must return the context error")
	}
}
