// Package verify compares two tables chunk by chunk with checksums. It only reads, and it is only
// meaningful when neither table is changing, such as a rehearsal on a copy or a paused system.
// Against a live table, rows written during the comparison show up as false mismatches.
package verify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/siddik7181/zdm/internal/keyset"
)

// Mismatch is one chunk whose contents differ.
type Mismatch struct {
	After     string `json:"after"` // exclusive lower key bound; empty for the first chunk
	Through   string `json:"through"`
	RowsA     int64  `json:"rows_a"`
	RowsB     int64  `json:"rows_b"`
	Checksums bool   `json:"checksums_differ"`
}

// Result summarises a comparison.
type Result struct {
	Columns    []string   `json:"columns"`
	Chunks     int        `json:"chunks"`
	Rows       int64      `json:"rows"` // rows in table A
	Mismatches []Mismatch `json:"mismatches"`
}

// OK reports whether every chunk matched.
func (r Result) OK() bool { return len(r.Mismatches) == 0 }

func columns(ctx context.Context, db *sql.DB, schema, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// commonColumns keeps the columns present in both tables, in table A's order, so a column the
// change added or dropped does not count as a difference.
func commonColumns(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, c := range b {
		inB[c] = true
	}
	var out []string
	for _, c := range a {
		if inB[c] {
			out = append(out, c)
		}
	}
	return out
}

func rowExpr(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		// HEX works for every type and is never 'N', so NULL cannot collide with a value.
		parts[i] = "IFNULL(HEX(" + keyset.Quote(c) + "),'N')"
	}
	return "CRC32(CONCAT_WS('#'," + strings.Join(parts, ",") + "))"
}

type sum struct {
	rows     int64
	xor, add uint64
}

// Compare checksums tables a and b in key order, chunk rows at a time.
func Compare(ctx context.Context, db *sql.DB, schema, a, b string, chunk int) (Result, error) {
	if chunk < 1 {
		return Result{}, errors.New("chunk must be at least 1")
	}
	colsA, err := columns(ctx, db, schema, a)
	if err != nil {
		return Result{}, err
	}
	colsB, err := columns(ctx, db, schema, b)
	if err != nil {
		return Result{}, err
	}
	if len(colsA) == 0 || len(colsB) == 0 {
		return Result{}, errors.New("both tables must exist")
	}
	cols := commonColumns(colsA, colsB)
	pk, err := keyset.PrimaryKey(ctx, db, schema, a)
	if err != nil {
		return Result{}, err
	}
	pkB, err := keyset.PrimaryKey(ctx, db, schema, b)
	if err != nil {
		return Result{}, err
	}
	if len(pk) == 0 || strings.Join(pk, ",") != strings.Join(pkB, ",") {
		return Result{}, errors.New("both tables need the same PRIMARY KEY")
	}

	tableA, tableB := keyset.Quote(schema)+"."+keyset.Quote(a), keyset.Quote(schema)+"."+keyset.Quote(b)
	sumSQL := "SELECT COUNT(*), COALESCE(BIT_XOR(c),0), COALESCE(SUM(c),0) FROM (SELECT " + rowExpr(cols) + " AS c FROM %s WHERE %s) t"

	res := Result{Columns: cols}
	var lower []any
	for {
		// Upper bound: the key of the chunk-th row of A after the lower bound.
		upper, err := keyset.Next(ctx, db, schema, a, pk, lower, chunk)
		if err != nil {
			return res, err
		}
		rangeWhere, rangeArgs := keyset.Range(pk, lower, upper)
		sa, err := checksum(ctx, db, fmt.Sprintf(sumSQL, tableA, rangeWhere), rangeArgs)
		if err != nil {
			return res, err
		}
		sb, err := checksum(ctx, db, fmt.Sprintf(sumSQL, tableB, rangeWhere), rangeArgs)
		if err != nil {
			return res, err
		}
		res.Chunks++
		res.Rows += sa.rows
		if sa != sb {
			res.Mismatches = append(res.Mismatches, Mismatch{
				After: keyset.Render(lower), Through: keyset.Render(upper), RowsA: sa.rows, RowsB: sb.rows, Checksums: sa.rows == sb.rows,
			})
		}
		if upper == nil {
			return res, nil
		}
		lower = upper
	}
}

func checksum(ctx context.Context, db *sql.DB, query string, args []any) (sum, error) {
	var s sum
	err := db.QueryRowContext(ctx, query, args...).Scan(&s.rows, &s.xor, &s.add)
	return s, err
}
