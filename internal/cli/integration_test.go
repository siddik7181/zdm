//go:build integration

package cli

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// Needs the docker lab and gh-ost on PATH. It runs a real change while another goroutine writes.
func TestPlanRunEndToEndWithGhost(t *testing.T) {
	dsn, replica := os.Getenv("ZDM_TEST_DSN"), os.Getenv("ZDM_TEST_REPLICA_DSN")
	if dsn == "" || replica == "" {
		t.Skip("lab DSNs not set")
	}
	if _, err := exec.LookPath("gh-ost"); err != nil {
		t.Skip("gh-ost not on PATH")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const schema = "zdm_e2e"
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + schema, "CREATE DATABASE " + schema,
		"CREATE TABLE " + schema + ".t (id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, v INT)",
		"INSERT INTO " + schema + ".t (v) VALUES (1),(2),(3),(4),(5)",
		"INSERT INTO " + schema + ".t (v) SELECT v FROM " + schema + ".t",
		"INSERT INTO " + schema + ".t (v) SELECT v FROM " + schema + ".t",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + schema) })

	planFile := filepath.Join(t.TempDir(), "plan.json")
	var out, errb bytes.Buffer
	s := Streams{Out: &out, Err: &errb}
	if code := Run([]string{"plan", "--dsn", dsn, "--schema", schema, "--table", "t", "--alter", "ADD COLUMN c INT NULL", "--out", planFile}, s); code != 0 {
		t.Fatalf("plan: %d %s", code, errb.String())
	}

	const writes = 30
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			_, _ = db.Exec(fmt.Sprintf("INSERT INTO %s.t (v) VALUES (%d)", schema, i))
			time.Sleep(50 * time.Millisecond)
		}
	}()
	args := []string{"run", "--execute", "--behind-proxy", "--dsn", dsn, "--replica-dsn", replica, planFile}
	code := Run(args, s)
	wg.Wait()
	if code != 0 {
		t.Fatalf("run: %d\n%s", code, errb.String())
	}

	var n, hasCol int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + schema + ".t").Scan(&n); err != nil || n != 20+writes {
		t.Fatalf("rows = %d, want %d (%v)", n, 20+writes, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME='t' AND COLUMN_NAME='c'", schema).Scan(&hasCol); err != nil || hasCol != 1 {
		t.Fatalf("column not added: %d %v", hasCol, err)
	}

	out.Reset()
	if code := Run([]string{"status", planFile}, s); code != 0 || !strings.Contains(out.String(), "next phase done") {
		t.Fatalf("status: %d %s", code, out.String())
	}
}

// pt-osc handles the foreign-key tables that gh-ost refuses. Needs pt-online-schema-change on PATH.
func TestPlanRunEndToEndWithPtOscForeignKeys(t *testing.T) {
	dsn, replica := os.Getenv("ZDM_TEST_DSN"), os.Getenv("ZDM_TEST_REPLICA_DSN")
	if dsn == "" || replica == "" {
		t.Skip("lab DSNs not set")
	}
	if _, err := exec.LookPath("pt-online-schema-change"); err != nil {
		t.Skip("pt-online-schema-change not on PATH")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const schema = "zdm_e2e_fk"
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + schema, "CREATE DATABASE " + schema,
		"CREATE TABLE " + schema + ".parent (id INT NOT NULL PRIMARY KEY)",
		"CREATE TABLE " + schema + ".child (id INT NOT NULL PRIMARY KEY, pid INT NOT NULL, FOREIGN KEY (pid) REFERENCES " + schema + ".parent(id))",
		"INSERT INTO " + schema + ".parent VALUES (1),(2),(3)",
		"INSERT INTO " + schema + ".child VALUES (1,1),(2,2),(3,3)",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + schema) })

	for _, table := range []string{"child", "parent"} {
		planFile := filepath.Join(t.TempDir(), table+".json")
		var out, errb bytes.Buffer
		s := Streams{Out: &out, Err: &errb}
		if code := Run([]string{"plan", "--dsn", dsn, "--schema", schema, "--table", table, "--alter", "ADD COLUMN c INT NULL", "--out", planFile}, s); code != 0 {
			t.Fatalf("plan %s: %d %s", table, code, errb.String())
		}
		if !strings.Contains(out.String(), "engine pt-osc") {
			t.Fatalf("%s: want pt-osc, got %s", table, out.String())
		}
		if code := Run([]string{"run", "--execute", "--dsn", dsn, "--replica-dsn", replica, planFile}, s); code != 0 {
			t.Fatalf("run %s: %d\n%s", table, code, errb.String())
		}
		var hasCol int
		if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? AND COLUMN_NAME='c'", schema, table).Scan(&hasCol); err != nil || hasCol != 1 {
			t.Fatalf("%s: column not added: %d %v", table, hasCol, err)
		}
	}
	var fks int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=? AND TABLE_NAME='child'", schema).Scan(&fks); err != nil || fks != 1 {
		t.Fatalf("foreign key lost: %d %v", fks, err)
	}
}
