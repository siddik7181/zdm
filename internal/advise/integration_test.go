//go:build integration

package advise

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/siddik7181/zdm/internal/plan"
)

// Needs the docker lab: `make lab-up`, then `make test-integration`.
func TestGatherAgainstLab(t *testing.T) {
	dsn := os.Getenv("ZDM_TEST_DSN")
	if dsn == "" {
		t.Skip("ZDM_TEST_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	const schema = "zdm_advise_test"
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + schema,
		"CREATE DATABASE " + schema,
		"CREATE TABLE " + schema + ".plain (id INT NOT NULL PRIMARY KEY, v INT)",
		"CREATE TABLE " + schema + ".nokey (id INT, v INT)",
		"CREATE TABLE " + schema + ".nullable_unique (id INT NULL, UNIQUE KEY (id))",
		"CREATE TABLE " + schema + ".parent (id INT NOT NULL PRIMARY KEY)",
		"CREATE TABLE " + schema + ".child (id INT NOT NULL PRIMARY KEY, pid INT NOT NULL, FOREIGN KEY (pid) REFERENCES " + schema + ".parent(id))",
		"CREATE TABLE " + schema + ".trg (id INT NOT NULL PRIMARY KEY)",
		"CREATE TRIGGER " + schema + ".t1 BEFORE INSERT ON " + schema + ".trg FOR EACH ROW SET NEW.id = NEW.id",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + schema) })

	cases := []struct {
		table  string
		engine string
		refuse bool
	}{
		{"plain", plan.GhOst, false},
		{"nokey", "", true},
		{"nullable_unique", "", true},
		{"parent", plan.PtOsc, false},
		{"child", plan.PtOsc, false},
		{"trg", plan.PtOsc, false},
		{"missing", "", true},
	}
	for _, c := range cases {
		f, err := Gather(ctx, db, schema, c.table)
		if err != nil {
			t.Fatalf("%s: %v", c.table, err)
		}
		got := Choose(f)
		if got.Engine != c.engine || got.Refuse != c.refuse {
			t.Errorf("%s: facts %+v -> %+v", c.table, f, got)
		}
	}
}
