package cutover

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/siddik7181/zdm/internal/plan"
)

// Leftover is a helper object an engine created and a crash left behind.
type Leftover struct {
	Kind string // "table" or "trigger"
	Name string
}

// helperTables are the scratch tables the plan's engine creates, never the old or live table.
func helperTables(p plan.Plan) []string {
	if p.Engine == plan.PtOsc {
		return []string{"_" + p.Table + "_new"}
	}
	return []string{"_" + p.Table + "_gho", "_" + p.Table + "_ghc"}
}

// helperTriggers are the triggers pt-osc puts on the live table. A killed pt-osc leaves them
// running, so every write to the table keeps paying for them.
func helperTriggers(p plan.Plan) []string {
	if p.Engine != plan.PtOsc {
		return nil
	}
	var out []string
	for _, k := range []string{"ins", "upd", "del"} {
		out = append(out, fmt.Sprintf("pt_osc_%s_%s_%s", p.Schema, p.Table, k))
	}
	return out
}

// Leftovers lists what a crashed engine left behind. It only reads.
func Leftovers(ctx context.Context, db *sql.DB, p plan.Plan) ([]Leftover, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var out []Leftover
	for _, t := range helperTables(p) {
		ok, err := exists(ctx, db, p.Schema, t)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, Leftover{"table", t})
		}
	}
	want := helperTriggers(p)
	if len(want) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=? AND EVENT_OBJECT_TABLE=?`, p.Schema, p.Table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if slices.Contains(want, n) {
			out = append(out, Leftover{"trigger", n})
		}
	}
	return out, rows.Err()
}

// Cleanup drops the leftovers. Triggers go first so the live table stops paying for them.
// The caller must be sure the engine is not running: this would break a live migration.
func Cleanup(ctx context.Context, db *sql.DB, p plan.Plan) ([]Leftover, error) {
	found, err := Leftovers(ctx, db, p)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(found, func(a, b Leftover) int { // "trigger" sorts after "table", so flip it
		if a.Kind == b.Kind {
			return 0
		}
		if a.Kind == "trigger" {
			return -1
		}
		return 1
	})
	for _, l := range found {
		stmt := "DROP TABLE IF EXISTS "
		if l.Kind == "trigger" {
			stmt = "DROP TRIGGER IF EXISTS "
		}
		if _, err := db.ExecContext(ctx, stmt+quote(p.Schema)+"."+quote(l.Name)); err != nil {
			return found, err
		}
	}
	return found, nil
}
