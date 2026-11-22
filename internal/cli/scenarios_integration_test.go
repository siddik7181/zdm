//go:build integration

package cli

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type labEnv struct {
	db           *sql.DB
	dsn, replica string
	schema, plan string
	out, errb    *bytes.Buffer
	s            Streams
}

func newLab(t *testing.T, schema string, rows int, engine string) *labEnv {
	t.Helper()
	dsn, replica := os.Getenv("ZDM_TEST_DSN"), os.Getenv("ZDM_TEST_REPLICA_DSN")
	if dsn == "" || replica == "" {
		t.Skip("lab DSNs not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + schema); _ = db.Close() })
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + schema, "CREATE DATABASE " + schema,
		"CREATE TABLE " + schema + ".t (id INT NOT NULL PRIMARY KEY, v INT NOT NULL DEFAULT 0)",
		"INSERT INTO " + schema + ".t (id) VALUES (1)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n < rows; n *= 2 {
		if _, err := db.Exec(fmt.Sprintf("INSERT INTO %s.t (id) SELECT id + %d FROM %s.t WHERE id <= %d", schema, n, schema, n)); err != nil {
			t.Fatal(err)
		}
	}
	l := &labEnv{db: db, dsn: dsn, replica: replica, schema: schema, out: &bytes.Buffer{}, errb: &bytes.Buffer{}}
	l.s = Streams{Out: l.out, Err: l.errb}
	l.plan = filepath.Join(t.TempDir(), "plan.json")
	if code := Run([]string{"plan", "--dsn", dsn, "--schema", schema, "--table", "t", "--alter", "ADD COLUMN c INT NULL", "--out", l.plan}, l.s); code != 0 {
		t.Fatalf("plan: %d %s", code, l.errb)
	}
	if engine == "pt-osc" {
		b, _ := os.ReadFile(l.plan)
		_ = os.WriteFile(l.plan, bytes.ReplaceAll(b, []byte(`"gh-ost"`), []byte(`"pt-osc"`)), 0o600)
	}
	return l
}

func (l *labEnv) run(extra ...string) int {
	l.out.Reset()
	l.errb.Reset()
	args := append([]string{"run", "--behind-proxy", "--dsn", l.dsn, "--replica-dsn", l.replica}, extra...)
	return Run(append(args, l.plan), l.s)
}

func (l *labEnv) count(q string, args ...any) int {
	var n int
	_ = l.db.QueryRow(q, args...).Scan(&n)
	return n
}

func (l *labEnv) hasColumn(col string) bool {
	return l.count("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME='t' AND COLUMN_NAME=?", l.schema, col) == 1
}

func needBin(t *testing.T, b string) {
	t.Helper()
	if _, err := exec.LookPath(b); err != nil {
		t.Skipf("%s not on PATH (make tools)", b)
	}
}

func TestRollbackAfterRealRun(t *testing.T) {
	needBin(t, "gh-ost")
	l := newLab(t, "zdm_sc_rb", 5000, "gh-ost")
	if code := l.run("--execute"); code != 0 {
		t.Fatalf("run: %d %s", code, l.errb)
	}
	if !l.hasColumn("c") {
		t.Fatal("column missing after run")
	}
	if code := Run([]string{"verify", "--dsn", l.dsn, l.plan}, l.s); code != 0 {
		t.Fatalf("verify: %d %s %s", code, l.out, l.errb)
	}
	if code := Run([]string{"rollback", "--dsn", l.dsn, "--discard-writes-since-swap", l.plan}, l.s); code != 0 {
		t.Fatalf("rollback: %d %s", code, l.errb)
	}
	if l.hasColumn("c") || l.count("SELECT COUNT(*) FROM zdm_sc_rb.t") != 8192 {
		t.Fatal("rollback did not restore the old table")
	}
	if code := Run([]string{"finalize", "--dsn", l.dsn, "--drop-old-table", l.plan}, l.s); code != 0 {
		t.Fatalf("finalize: %d %s", code, l.errb)
	}
}

func TestRunRefusesWhenReplicaBroken(t *testing.T) {
	needBin(t, "gh-ost")
	l := newLab(t, "zdm_sc_rep", 100, "gh-ost")
	rdb, err := sql.Open("mysql", l.replica)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = rdb.Exec("START REPLICA"); _ = rdb.Close() }()
	if _, err := rdb.Exec("STOP REPLICA"); err != nil {
		t.Fatal(err)
	}
	if code := l.run("--execute"); code == 0 {
		t.Fatal("run succeeded against a stopped replica")
	}
	if l.hasColumn("c") || l.count("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='zdm_sc_rep'") != 1 {
		t.Fatal("a refused run still changed the schema")
	}
}

func TestPlanTamperingIsRefused(t *testing.T) {
	needBin(t, "gh-ost")
	l := newLab(t, "zdm_sc_tamper", 100, "gh-ost")
	st := l.plan + ".state"
	_ = os.WriteFile(st, []byte(`{"plan_hash":"deadbeef","completed":["preflight","migrate"]}`), 0o600)
	if code := l.run("--execute"); code == 0 || l.hasColumn("c") {
		t.Fatalf("run must refuse a state saved for another plan: code %d", code)
	}
}

func TestPtOscKeepsConcurrentWrites(t *testing.T) {
	needBin(t, "pt-online-schema-change")
	l := newLab(t, "zdm_sc_ptw", 20000, "pt-osc")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	inserted := 0
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := l.db.Exec(fmt.Sprintf("INSERT INTO zdm_sc_ptw.t (id, v) VALUES (%d, 1)", 1_000_000+i)); err == nil {
				mu.Lock()
				inserted++
				mu.Unlock()
			}
			_, _ = l.db.Exec("UPDATE zdm_sc_ptw.t SET v = v + 1 WHERE id = 5")
			_, _ = l.db.Exec("DELETE FROM zdm_sc_ptw.t WHERE id = 7")
			time.Sleep(5 * time.Millisecond)
		}
	}()
	code := l.run("--execute")
	close(stop)
	wg.Wait()
	if code != 0 {
		t.Fatalf("run: %d\n%s", code, tail(l.errb.Bytes()))
	}
	want := 32768 - 1 + inserted // id 7 deleted; ids start 1..32768
	if got := l.count("SELECT COUNT(*) FROM zdm_sc_ptw.t"); got != want || !l.hasColumn("c") {
		t.Fatalf("rows %d, want %d", got, want)
	}
	if l.count("SELECT COUNT(*) FROM zdm_sc_ptw.t WHERE id = 7") != 0 {
		t.Fatal("deleted row came back")
	}
}

// SIGINT is the polite stop. The rerun must finish and leave nothing behind.
func TestInterruptThenResume(t *testing.T) {
	needBin(t, "gh-ost")
	l := newLab(t, "zdm_sc_int", 1_000_000, "gh-ost")
	bin := filepath.Join(t.TempDir(), "zdm")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/siddik7181/zdm/cmd/zdm").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	args := []string{"run", "--execute", "--behind-proxy", "--dsn", l.dsn, "--replica-dsn", l.replica, l.plan}
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t.Context(), 60*time.Second, func() bool { return tableExists(l.db, l.schema, "_t_gho") }) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("engine never started")
	}
	_ = syscall.Kill(cmd.Process.Pid, syscall.SIGINT)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("zdm did not exit within 60s of SIGINT")
	}
	if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
		t.Fatalf("resume: %v\n%s", err, tail(out))
	}
	if !l.hasColumn("c") || l.count("SELECT COUNT(*) FROM zdm_sc_int.t") != 1<<20 {
		t.Fatal("resumed run did not complete the change")
	}
	_ = strings.TrimSpace
}

// A reader that holds a metadata lock blocks the cut-over. The engine must keep retrying, not
// force it or lose data, and finish once the reader ends.
func TestLongTransactionBlocksCutoverThenReleases(t *testing.T) {
	needBin(t, "gh-ost")
	l := newLab(t, "zdm_sc_mdl", 300_000, "gh-ost")
	started := make(chan struct{})
	go func() {
		if !waitFor(t.Context(), 60*time.Second, func() bool { return tableExists(l.db, l.schema, "_t_gho") }) {
			close(started)
			return
		}
		conn, err := l.db.Conn(t.Context())
		if err != nil {
			close(started)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.ExecContext(t.Context(), "START TRANSACTION")
		_, _ = conn.ExecContext(t.Context(), "SELECT COUNT(*) FROM zdm_sc_mdl.t")
		close(started)
		time.Sleep(12 * time.Second)
		_, _ = conn.ExecContext(t.Context(), "COMMIT")
	}()
	code := l.run("--execute")
	<-started
	if code != 0 {
		t.Fatalf("run: %d\n%s", code, tail(l.errb.Bytes()))
	}
	if !l.hasColumn("c") || l.count("SELECT COUNT(*) FROM zdm_sc_mdl.t") != 1<<19 {
		t.Fatal("change incomplete or rows lost")
	}
}

// The primary restarts mid-copy. Whatever zdm does, a retry after cleanup must finish with no row lost.
func TestPrimaryRestartMidCopy(t *testing.T) {
	for _, eng := range []string{"gh-ost", "pt-osc"} {
		t.Run(eng, func(t *testing.T) {
			bin := map[string]string{"gh-ost": "gh-ost", "pt-osc": "pt-online-schema-change"}[eng]
			needBin(t, bin)
			schema := "zdm_sc_pr" + strings.ReplaceAll(eng, "-", "")
			l := newLab(t, schema, 1_000_000, eng)
			helper := map[string]string{"gh-ost": "_t_gho", "pt-osc": "_t_new"}[eng]
			go func() {
				if waitFor(t.Context(), 60*time.Second, func() bool { return tableExists(l.db, schema, helper) }) {
					_ = exec.Command("docker", "restart", "docker-primary-1").Run()
				}
			}()
			first := l.run("--execute")
			t.Logf("first run exit %d", first)
			if !waitFor(t.Context(), 120*time.Second, func() bool { return l.db.Ping() == nil }) {
				t.Fatal("primary did not come back")
			}
			time.Sleep(10 * time.Second) // replicas reconnect
			for attempt := 0; attempt < 3 && !l.hasColumn("c"); attempt++ {
				Run([]string{"cleanup", "--engine-is-stopped", "--dsn", l.dsn, l.plan}, l.s)
				if code := l.run("--execute"); code != 0 {
					t.Logf("retry %d exit %d: %s", attempt, code, tail(l.errb.Bytes()))
				}
			}
			if !l.hasColumn("c") || l.count("SELECT COUNT(*) FROM "+schema+".t") != 1<<20 {
				t.Fatalf("not complete after retries: column=%v rows=%d", l.hasColumn("c"), l.count("SELECT COUNT(*) FROM "+schema+".t"))
			}
		})
	}
}
