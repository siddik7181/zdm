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

func q(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

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

func primaryKey(ctx context.Context, db *sql.DB, schema, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? AND INDEX_NAME='PRIMARY' ORDER BY SEQ_IN_INDEX`, schema, table)
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

func list(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = q(c)
	}
	return strings.Join(parts, ",")
}

func rowExpr(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		// HEX works for every type and is never 'N', so NULL cannot collide with a value.
		parts[i] = "IFNULL(HEX(" + q(c) + "),'N')"
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
	pk, err := primaryKey(ctx, db, schema, a)
	if err != nil {
		return Result{}, err
	}
	pkB, err := primaryKey(ctx, db, schema, b)
	if err != nil {
		return Result{}, err
	}
	if len(pk) == 0 || strings.Join(pk, ",") != strings.Join(pkB, ",") {
		return Result{}, errors.New("both tables need the same PRIMARY KEY")
	}

	pkList := list(pk)
	rowVal := "(" + strings.Repeat("?,", len(pk)-1) + "?)"
	tableA, tableB := q(schema)+"."+q(a), q(schema)+"."+q(b)
	boundSQL := "SELECT " + pkList + " FROM " + tableA + " %s ORDER BY " + pkList + " LIMIT 1 OFFSET " + fmt.Sprint(chunk-1)
	sumSQL := "SELECT COUNT(*), COALESCE(BIT_XOR(c),0), COALESCE(SUM(c),0) FROM (SELECT " + rowExpr(cols) + " AS c FROM %s WHERE %s) t"

	res := Result{Columns: cols}
	var lower []any
	for {
		where := "TRUE"
		var args []any
		if lower != nil {
			where = "(" + pkList + ") > " + rowVal
			args = append(args, lower...)
		}
		// Upper bound: the key of the chunk-th row of A after the lower bound.
		upper, err := bound(ctx, db, fmt.Sprintf(boundSQL, whereClause(lower != nil, pkList, rowVal)), lower, len(pk))
		if err != nil {
			return res, err
		}
		rangeWhere, rangeArgs := where, args
		if upper != nil {
			rangeWhere += " AND (" + pkList + ") <= " + rowVal
			rangeArgs = append(append([]any{}, args...), upper...)
		}
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
				After: render(lower), Through: render(upper), RowsA: sa.rows, RowsB: sb.rows, Checksums: sa.rows == sb.rows,
			})
		}
		if upper == nil {
			return res, nil
		}
		lower = upper
	}
}

func whereClause(hasLower bool, pkList, rowVal string) string {
	if hasLower {
		return "WHERE (" + pkList + ") > " + rowVal
	}
	return ""
}

func render(key []any) string {
	parts := make([]string, len(key))
	for i, k := range key {
		if b, ok := k.([]byte); ok {
			k = string(b)
		}
		parts[i] = fmt.Sprint(k)
	}
	return strings.Join(parts, ",")
}

func bound(ctx context.Context, db *sql.DB, query string, lower []any, n int) ([]any, error) {
	dest := make([]any, n)
	ptrs := make([]any, n)
	for i := range dest {
		ptrs[i] = &dest[i]
	}
	err := db.QueryRowContext(ctx, query, lower...).Scan(ptrs...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // fewer than chunk rows remain: the last chunk is unbounded
	}
	for i, v := range dest {
		if b, ok := v.([]byte); ok {
			dest[i] = string(b) // a string, not bytes, so the column's collation decides the order
		}
	}
	return dest, err
}

func checksum(ctx context.Context, db *sql.DB, query string, args []any) (sum, error) {
	var s sum
	err := db.QueryRowContext(ctx, query, args...).Scan(&s.rows, &s.xor, &s.add)
	return s, err
}
