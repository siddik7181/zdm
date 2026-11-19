// Package preflight inspects a live MySQL primary and its gating replicas, read-only,
// and judges whether an online schema change can run safely.
package preflight

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Txn is a transaction that has been open longer than the configured threshold.
type Txn struct {
	ID         string
	AgeSeconds int64
}

// ReplicaFacts is what one gating replica reported.
type ReplicaFacts struct {
	Addr       string // host:port from the DSN; never the credentials
	Err        error
	IORunning  string
	SQLRunning string
	LagSeconds *int64 // nil when MySQL reports NULL (replication not running)
}

// Facts is everything gathered from the servers, before any judgement.
type Facts struct {
	Version     string
	Vars        map[string]string
	LongTxns    []Txn
	LongTxnsErr error
	Replicas    []ReplicaFacts
}

const varsQuery = `SHOW GLOBAL VARIABLES WHERE Variable_name IN
	('gtid_mode', 'enforce_gtid_consistency', 'log_bin', 'binlog_format',
	 'binlog_row_image', 'binlog_expire_logs_seconds', 'read_only')`

// Open connects with a session that MySQL itself keeps read-only, so a bug here cannot write.
func Open(dsn string) (*sql.DB, string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, "", fmt.Errorf("invalid DSN: %w", err)
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["transaction_read_only"] = "1"
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, "", err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	return db, cfg.Addr, nil
}

// Replica is a gating replica connection.
type Replica struct {
	Addr string
	DB   *sql.DB
}

// Gather reads facts. It fails only when the primary cannot be queried at all; every other
// problem is recorded in the facts so the evaluation can report it.
func Gather(ctx context.Context, primary *sql.DB, replicas []Replica, longTxnSeconds int) (Facts, error) {
	f := Facts{Vars: map[string]string{}}
	if err := primary.QueryRowContext(ctx, "SELECT @@version").Scan(&f.Version); err != nil {
		return f, fmt.Errorf("read version: %w", err)
	}
	rows, err := primary.QueryContext(ctx, varsQuery)
	if err != nil {
		return f, fmt.Errorf("read variables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return f, err
		}
		f.Vars[k] = v
	}
	if err := rows.Err(); err != nil {
		return f, err
	}

	f.LongTxns, f.LongTxnsErr = longTxns(ctx, primary, longTxnSeconds)
	for _, r := range replicas {
		f.Replicas = append(f.Replicas, ReadReplica(ctx, r.Addr, r.DB))
	}
	return f, nil
}

func longTxns(ctx context.Context, db *sql.DB, seconds int) ([]Txn, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT trx_id, TIMESTAMPDIFF(SECOND, trx_started, NOW()) FROM information_schema.innodb_trx
		 WHERE trx_started < NOW() - INTERVAL ? SECOND ORDER BY trx_started LIMIT 20`, seconds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Txn
	for rows.Next() {
		var t Txn
		if err := rows.Scan(&t.ID, &t.AgeSeconds); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReadReplica reports replication state and lag of one replica.
func ReadReplica(ctx context.Context, addr string, db *sql.DB) ReplicaFacts {
	r := ReplicaFacts{Addr: addr}
	rows, err := db.QueryContext(ctx, "SHOW REPLICA STATUS")
	if err != nil {
		r.Err = err
		return r
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		r.Err = err
		return r
	}
	if !rows.Next() {
		r.Err = fmt.Errorf("not configured as a replica")
		return r
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		r.Err = err
		return r
	}
	for i, c := range cols {
		switch c {
		case "Replica_IO_Running":
			r.IORunning = vals[i].String
		case "Replica_SQL_Running":
			r.SQLRunning = vals[i].String
		case "Seconds_Behind_Source":
			if vals[i].Valid {
				var n int64
				if _, err := fmt.Sscan(vals[i].String, &n); err == nil {
					r.LagSeconds = &n
				}
			}
		}
	}
	return r
}
