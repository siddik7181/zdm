// Package advise reads facts about one table and picks the change strategy.
package advise

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/siddik7181/zdm/internal/plan"
)

// TableFacts is what the strategy depends on.
type TableFacts struct {
	Exists       bool
	InnoDB       bool
	HasUniqueKey bool // a PRIMARY or NOT NULL unique key, required to copy by key
	ChildFKs     int  // foreign keys on this table
	ParentFKs    int  // foreign keys in other tables that reference this one
	Triggers     int
	Galera       bool
	Rows         int64 // estimate from the optimizer statistics
}

// Choice is the recommended engine and the reasons for it.
type Choice struct {
	Engine  string   `json:"engine,omitempty"`
	Reasons []string `json:"reasons"`
	Refuse  bool     `json:"refuse"` // true when no safe engine applies
}

// Choose picks an engine. Triggers-based pt-osc is the safe default when foreign keys,
// triggers or Galera are in play, because gh-ost refuses or misbehaves with them.
func Choose(f TableFacts) Choice {
	switch {
	case !f.Exists:
		return Choice{Refuse: true, Reasons: []string{"table does not exist"}}
	case !f.InnoDB:
		return Choice{Refuse: true, Reasons: []string{"table is not InnoDB"}}
	case !f.HasUniqueKey:
		return Choice{Refuse: true, Reasons: []string{"table has no PRIMARY or NOT NULL unique key; rows cannot be copied by key"}}
	}
	var why []string
	if f.ChildFKs > 0 {
		why = append(why, fmt.Sprintf("%d foreign key(s) on the table; gh-ost refuses these", f.ChildFKs))
	}
	if f.ParentFKs > 0 {
		why = append(why, fmt.Sprintf("%d foreign key(s) reference the table; gh-ost refuses these", f.ParentFKs))
	}
	if f.Triggers > 0 {
		why = append(why, fmt.Sprintf("%d existing trigger(s); gh-ost refuses these", f.Triggers))
	}
	if f.Galera {
		why = append(why, "Galera cluster; gh-ost is not supported")
	}
	if len(why) > 0 {
		return Choice{Engine: plan.PtOsc, Reasons: why}
	}
	return Choice{Engine: plan.GhOst, Reasons: []string{"no foreign keys, triggers or Galera; binlog-based copy avoids trigger overhead on the primary"}}
}

// queries are constants: schema and table go in as arguments, never into the SQL text.
const (
	qTable = `SELECT ENGINE, COALESCE(TABLE_ROWS,0) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?`
	// A unique index counts only if none of its columns is nullable.
	qUnique = `SELECT COUNT(*) FROM (
		SELECT s.INDEX_NAME FROM information_schema.STATISTICS s
		JOIN information_schema.COLUMNS c ON c.TABLE_SCHEMA=s.TABLE_SCHEMA AND c.TABLE_NAME=s.TABLE_NAME AND c.COLUMN_NAME=s.COLUMN_NAME
		WHERE s.TABLE_SCHEMA=? AND s.TABLE_NAME=? AND s.NON_UNIQUE=0
		GROUP BY s.INDEX_NAME HAVING SUM(c.IS_NULLABLE='YES')=0) u`
	qChildFK  = `SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=? AND TABLE_NAME=?`
	qParentFK = `SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE UNIQUE_CONSTRAINT_SCHEMA=? AND REFERENCED_TABLE_NAME=?`
	qTriggers = `SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE EVENT_OBJECT_SCHEMA=? AND EVENT_OBJECT_TABLE=?`
	qGalera   = `SHOW VARIABLES LIKE 'wsrep_on'`
)

// Gather reads the table facts from a (read-only) connection.
func Gather(ctx context.Context, db *sql.DB, schema, table string) (TableFacts, error) {
	var f TableFacts
	var engine sql.NullString
	err := db.QueryRowContext(ctx, qTable, schema, table).Scan(&engine, &f.Rows)
	if errors.Is(err, sql.ErrNoRows) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("table: %w", err)
	}
	f.Exists, f.InnoDB = true, engine.String == "InnoDB"

	counts := []struct {
		q   string
		dst *int
	}{{qChildFK, &f.ChildFKs}, {qParentFK, &f.ParentFKs}, {qTriggers, &f.Triggers}}
	for _, c := range counts {
		if err := db.QueryRowContext(ctx, c.q, schema, table).Scan(c.dst); err != nil {
			return f, fmt.Errorf("facts: %w", err)
		}
	}
	var unique int
	if err := db.QueryRowContext(ctx, qUnique, schema, table).Scan(&unique); err != nil {
		return f, fmt.Errorf("unique key: %w", err)
	}
	f.HasUniqueKey = unique > 0

	var name, val string
	switch err := db.QueryRowContext(ctx, qGalera).Scan(&name, &val); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return f, fmt.Errorf("galera: %w", err)
	default:
		f.Galera = val == "ON"
	}
	return f, nil
}
