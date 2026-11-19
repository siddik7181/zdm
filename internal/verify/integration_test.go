//go:build integration

package verify

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

func exec(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestCompareAgainstLab(t *testing.T) {
	dsn := os.Getenv("ZDM_TEST_DSN")
	if dsn == "" {
		t.Skip("ZDM_TEST_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	const s = "zdm_verify_test"
	exec(t, db, "DROP DATABASE IF EXISTS "+s, "CREATE DATABASE "+s)
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + s) })
	ctx := context.Background()

	for _, tbl := range []string{"a", "b"} {
		exec(t, db, "CREATE TABLE "+s+"."+tbl+" (k1 INT NOT NULL, k2 VARCHAR(10) NOT NULL, v VARCHAR(20) NULL, PRIMARY KEY (k1,k2))")
		for i := 1; i <= 25; i++ {
			v := fmt.Sprintf("'val%d'", i)
			if i%7 == 0 {
				v = "NULL"
			}
			exec(t, db, fmt.Sprintf("INSERT INTO %s.%s VALUES (%d,'k%d',%s)", s, tbl, i/5, i, v))
		}
	}
	// b gets an extra column, as after the change; that must not count as a difference.
	exec(t, db, "ALTER TABLE "+s+".b ADD COLUMN extra INT NULL")

	res, err := Compare(ctx, db, s, "a", "b", 4)
	if err != nil || !res.OK() || res.Rows != 25 || res.Chunks < 6 {
		t.Fatalf("identical tables: %+v %v", res, err)
	}

	exec(t, db,
		"UPDATE "+s+".b SET v='changed' WHERE k2='k10'",
		"DELETE FROM "+s+".b WHERE k2='k20'",
		"INSERT INTO "+s+".b (k1,k2,v) VALUES (99,'zzz','extra row')",
		"UPDATE "+s+".b SET v=NULL WHERE k2='k3'", // NULL vs a value must differ, not be skipped
	)
	res, err = Compare(ctx, db, s, "a", "b", 4)
	if err != nil || res.OK() || len(res.Mismatches) < 4 {
		t.Fatalf("expected at least 4 differing chunks: %+v %v", res, err)
	}

	if _, err := Compare(ctx, db, s, "a", "missing", 4); err == nil {
		t.Fatal("missing table must error")
	}
}
