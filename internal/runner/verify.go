package runner

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/siddik7181/zdm/internal/plan"
)

const qTableExists = `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?`

func exists(ctx context.Context, db *sql.DB, schema, table string) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx, qTableExists, schema, table).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// PostCheck confirms the engine left the schema clean: the table exists, no half-built
// helper tables remain, and the old table is kept exactly when the plan asked for it.
// It is safe to repeat. It does not prove the data matches; see docs for the verification roadmap.
func PostCheck(ctx context.Context, db *sql.DB, p plan.Plan) error {
	ok, err := exists(ctx, db, p.Schema, p.Table)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("table %s.%s does not exist", p.Schema, p.Table)
	}
	leftovers, old := []string{"_" + p.Table + "_gho", "_" + p.Table + "_ghc"}, "_"+p.Table+"_del"
	if p.Engine == plan.PtOsc {
		leftovers, old = []string{"_" + p.Table + "_new"}, "_"+p.Table+"_old"
	}
	for _, name := range leftovers {
		if ok, err := exists(ctx, db, p.Schema, name); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("leftover helper table %s.%s; the change did not finish cleanly", p.Schema, name)
		}
	}
	hasOld, err := exists(ctx, db, p.Schema, old)
	if err != nil {
		return err
	}
	if p.KeepOldTable && !hasOld {
		return fmt.Errorf("old table %s.%s is missing, so there is nothing to roll back to", p.Schema, old)
	}
	if !p.KeepOldTable && hasOld {
		return fmt.Errorf("old table %s.%s was not dropped", p.Schema, old)
	}
	return nil
}
