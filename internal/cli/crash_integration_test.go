//go:build integration

package cli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Kills zdm and its engine mid-copy with SIGKILL, then proves the rerun finishes the change
// (after `cleanup` for pt-osc) with no row lost and no helper object left.
func TestCrashMidCopyThenResume(t *testing.T) {
	dsn, replica := os.Getenv("ZDM_TEST_DSN"), os.Getenv("ZDM_TEST_REPLICA_DSN")
	if dsn == "" || replica == "" {
		t.Skip("lab DSNs not set")
	}
	for _, c := range []struct {
		engine, bin, helper string
		needsCleanup        bool
	}{
		{"gh-ost", "gh-ost", "_t_gho", false},
		{"pt-osc", "pt-online-schema-change", "_t_new", true},
	} {
		t.Run(c.engine, func(t *testing.T) {
			if _, err := exec.LookPath(c.bin); err != nil {
				t.Skipf("%s not on PATH (make tools)", c.bin)
			}
			crashAndResume(t, dsn, replica, c.engine, c.helper, c.needsCleanup)
		})
	}
}

func crashAndResume(t *testing.T, dsn, replica, engine, helper string, needsCleanup bool) {
	schema := "zdm_crash_" + strings.ReplaceAll(engine, "-", "")
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + schema) })
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + schema, "CREATE DATABASE " + schema,
		"CREATE TABLE " + schema + ".t (id INT NOT NULL PRIMARY KEY, pad CHAR(200) NOT NULL DEFAULT 'x')",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// 1M rows by doubling: the copy must outlast the time it takes to notice the helper table.
	if _, err := db.ExecContext(ctx, "INSERT INTO "+schema+".t (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	for n := 1; n < 1_000_000; n *= 2 {
		q := "INSERT INTO " + schema + ".t (id) SELECT id + " + itoa(n) + " FROM " + schema + ".t WHERE id <= " + itoa(n)
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var want int
	_ = db.QueryRow("SELECT COUNT(*) FROM " + schema + ".t").Scan(&want)

	dir := t.TempDir()
	bin := filepath.Join(dir, "zdm")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/siddik7181/zdm/cmd/zdm").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	planFile := filepath.Join(dir, "plan.json")
	if out, err := exec.Command(bin, "plan", "--dsn", dsn, "--schema", schema, "--table", "t", "--alter", "ADD COLUMN c INT NULL", "--out", planFile).CombinedOutput(); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(planFile); engine == "pt-osc" {
		_ = os.WriteFile(planFile, bytes.ReplaceAll(b, []byte(`"gh-ost"`), []byte(`"pt-osc"`)), 0o600)
	}
	runArgs := []string{"run", "--execute", "--behind-proxy", "--dsn", dsn, "--replica-dsn", replica, planFile}

	cmd := exec.Command(bin, runArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // so the kill reaches the engine too
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(ctx, 60*time.Second, func() bool { return tableExists(db, schema, helper) }) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatalf("engine never created %s", helper)
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
	if !tableExists(db, schema, helper) {
		t.Skip("copy finished before the kill; table too small for this machine")
	}

	if needsCleanup {
		if out, err := exec.Command(bin, "cleanup", "--engine-is-stopped", "--dsn", dsn, planFile).CombinedOutput(); err != nil {
			t.Fatalf("cleanup: %v\n%s", err, out)
		}
	}
	if out, err := exec.Command(bin, runArgs...).CombinedOutput(); err != nil {
		t.Fatalf("resume: %v\n%s", err, tail(out))
	}

	var got, hasCol, left int
	_ = db.QueryRow("SELECT COUNT(*) FROM " + schema + ".t").Scan(&got)
	_ = db.QueryRow("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME='t' AND COLUMN_NAME='c'", schema).Scan(&hasCol)
	_ = db.QueryRow("SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=?", schema).Scan(&left)
	if got != want || hasCol != 1 || left != 0 || tableExists(db, schema, helper) {
		t.Fatalf("rows %d/%d, column %d, triggers %d, helper present %v", got, want, hasCol, left, tableExists(db, schema, helper))
	}
}

func tableExists(db *sql.DB, schema, table string) bool {
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", schema, table).Scan(&n)
	return n > 0
}

func waitFor(ctx context.Context, d time.Duration, f func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) && ctx.Err() == nil {
		if f() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func itoa(n int) string { return strconv.Itoa(n) }

func tail(b []byte) string {
	if len(b) > 1500 {
		b = b[len(b)-1500:]
	}
	return string(b)
}
