// Package keyset walks a table in primary-key order, chunk by chunk, with no OFFSET over the whole table.
package keyset

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Quote backtick-quotes an identifier.
func Quote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

// List quotes and joins column names.
func List(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = Quote(c)
	}
	return strings.Join(parts, ",")
}

// PrimaryKey returns the PRIMARY KEY columns in order.
func PrimaryKey(ctx context.Context, db *sql.DB, schema, table string) ([]string, error) {
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

// RowValue is the placeholder tuple for n key columns: (?,?).
func RowValue(n int) string { return "(" + strings.Repeat("?,", n-1) + "?)" }

// Range is the WHERE fragment and arguments selecting keys in (lower, upper]. A nil bound is open.
func Range(pk []string, lower, upper []any) (string, []any) {
	where, args := "TRUE", []any(nil)
	if lower != nil {
		where += " AND (" + List(pk) + ") > " + RowValue(len(pk))
		args = append(args, lower...)
	}
	if upper != nil {
		where += " AND (" + List(pk) + ") <= " + RowValue(len(pk))
		args = append(args, upper...)
	}
	return where, args
}

// Next returns the key of the chunk-th row after lower, or nil when fewer than chunk rows remain
// (the final chunk is then unbounded above).
func Next(ctx context.Context, db *sql.DB, schema, table string, pk []string, lower []any, chunk int) ([]any, error) {
	if chunk < 1 {
		return nil, errors.New("chunk must be at least 1")
	}
	where, args := Range(pk, lower, nil)
	// Identifiers are quoted; every value is a bound parameter.
	query := fmt.Sprintf("SELECT %s FROM %s.%s WHERE %s ORDER BY %s LIMIT 1 OFFSET %d", //nolint:gosec // see above
		List(pk), Quote(schema), Quote(table), where, List(pk), chunk-1)
	dest := make([]any, len(pk))
	ptrs := make([]any, len(pk))
	for i := range dest {
		ptrs[i] = &dest[i]
	}
	err := db.QueryRowContext(ctx, query, args...).Scan(ptrs...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	for i, v := range dest {
		if b, ok := v.([]byte); ok {
			dest[i] = string(b) // a string, not bytes, so the column's collation decides the order
		}
	}
	return dest, err
}

// Render prints a key for humans.
func Render(key []any) string {
	parts := make([]string, len(key))
	for i, k := range key {
		parts[i] = fmt.Sprint(k)
	}
	return strings.Join(parts, ",")
}
