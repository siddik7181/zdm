// Package backfill updates existing rows in small primary-key chunks, so a large data change never
// holds long locks or floods replicas. It is a dry run unless Execute is set.
package backfill

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/siddik7181/zdm/internal/keyset"
)

// Config describes one backfill. Set and Where are SQL fragments written by the operator.
// Set must be idempotent: after a crash the last chunk may run twice.
type Config struct {
	Schema, Table string
	Set           string // SET clause body, e.g. "status = COALESCE(status, 'new')"
	Where         string // optional extra filter
	Chunk         int
	Pause         time.Duration // sleep between chunks
	MaxLag        time.Duration // wait while any replica is further behind than this
	Execute       bool
	Checkpoint    string // file recording the last finished key; empty disables resume
	StopFile      string // creating this file stops the run cleanly before the next chunk
}

var ident = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}$`)

// Validate rejects a config that could run more than one statement or cannot make progress.
func (c Config) Validate() error {
	var errs []error
	if !ident.MatchString(c.Schema) || !ident.MatchString(c.Table) {
		errs = append(errs, errors.New("schema and table must be plain identifiers"))
	}
	for name, frag := range map[string]string{"set": c.Set, "where": c.Where} {
		if strings.ContainsAny(frag, ";#") || strings.Contains(frag, "--") || strings.Contains(frag, "/*") {
			errs = append(errs, fmt.Errorf("%s must not contain ';', '#' or comments", name))
		}
	}
	if strings.TrimSpace(c.Set) == "" {
		errs = append(errs, errors.New("set is required"))
	}
	if c.Chunk < 1 || c.Chunk > 100000 {
		errs = append(errs, errors.New("chunk must be 1-100000"))
	}
	if c.Pause < 0 || c.MaxLag < 0 {
		errs = append(errs, errors.New("pause and max lag must not be negative"))
	}
	return errors.Join(errs...)
}

func (c Config) fingerprint() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{c.Schema, c.Table, c.Set, c.Where}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Report is what a run did.
type Report struct {
	Chunks  int    `json:"chunks"`
	Rows    int64  `json:"rows"` // rows changed (or, in a dry run, rows that would match)
	Stopped string `json:"stopped,omitempty"`
	Done    bool   `json:"done"`
	Resumed bool   `json:"resumed"`
}

// Lag reports the worst replica lag, or an error when it cannot be known.
type Lag func(ctx context.Context) (time.Duration, error)

type checkpoint struct {
	Fingerprint string   `json:"fingerprint"`
	LastKey     []string `json:"last_key"`
	Done        bool     `json:"done"`
}

func loadCheckpoint(path, fp string) (checkpoint, bool, error) {
	if path == "" {
		return checkpoint{Fingerprint: fp}, false, nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // operator-chosen path
	if errors.Is(err, os.ErrNotExist) {
		return checkpoint{Fingerprint: fp}, false, nil
	}
	if err != nil {
		return checkpoint{}, false, err
	}
	var cp checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return checkpoint{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if cp.Fingerprint != fp {
		return checkpoint{}, false, errors.New("checkpoint belongs to a different backfill; delete it only if you are sure")
	}
	return cp, true, nil
}

func (cp checkpoint) save(path string) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".zdm-backfill-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func stopRequested(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// Open connects with short lock waits, so a chunk that cannot get its locks fails instead of queueing.
func Open(dsn string) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid DSN")
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["innodb_lock_wait_timeout"] = "5"
	cfg.Params["lock_wait_timeout"] = "5"
	cfg.Timeout = 10 * time.Second
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run walks the table in primary-key order. It returns early, without error, when the stop file
// appears; the checkpoint then lets a later run continue from the same place.
func Run(ctx context.Context, db *sql.DB, c Config, lag Lag, log func(string)) (Report, error) {
	var rep Report
	if err := c.Validate(); err != nil {
		return rep, err
	}
	pk, err := keyset.PrimaryKey(ctx, db, c.Schema, c.Table)
	if err != nil {
		return rep, err
	}
	if len(pk) == 0 {
		return rep, errors.New("table has no PRIMARY KEY")
	}
	cp, resumed, err := loadCheckpoint(c.Checkpoint, c.fingerprint())
	if err != nil {
		return rep, err
	}
	if cp.Done {
		rep.Done, rep.Resumed = true, true
		return rep, nil
	}
	var lower []any
	if resumed && len(cp.LastKey) == len(pk) {
		for _, k := range cp.LastKey {
			lower = append(lower, k)
		}
		rep.Resumed = true
	}

	target := keyset.Quote(c.Schema) + "." + keyset.Quote(c.Table)
	for {
		if stopRequested(c.StopFile) {
			rep.Stopped = "stop file present"
			return rep, nil
		}
		for lag != nil {
			l, err := lag(ctx)
			if err != nil {
				return rep, fmt.Errorf("cannot read replica lag, refusing to continue blind: %w", err)
			}
			if l <= c.MaxLag {
				break
			}
			log(fmt.Sprintf("replica lag %s above %s; waiting", l, c.MaxLag))
			if err := wait(ctx, time.Second); err != nil {
				return rep, err
			}
			if stopRequested(c.StopFile) {
				rep.Stopped = "stop file present"
				return rep, nil
			}
		}

		upper, err := keyset.Next(ctx, db, c.Schema, c.Table, pk, lower, c.Chunk)
		if err != nil {
			return rep, err
		}
		where, args := keyset.Range(pk, lower, upper)
		if strings.TrimSpace(c.Where) != "" {
			where += " AND (" + c.Where + ")"
		}
		var n int64
		if c.Execute {
			res, err := db.ExecContext(ctx, "UPDATE "+target+" SET "+c.Set+" WHERE "+where, args...) //nolint:gosec // operator-supplied fragments, validated; keys are bound
			if err != nil {
				return rep, fmt.Errorf("chunk %d: %w", rep.Chunks+1, err)
			}
			if n, err = res.RowsAffected(); err != nil {
				return rep, err
			}
		} else if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target+" WHERE "+where, args...).Scan(&n); err != nil { //nolint:gosec // as above
			return rep, fmt.Errorf("chunk %d: %w", rep.Chunks+1, err)
		}
		rep.Chunks++
		rep.Rows += n

		if upper == nil {
			rep.Done = true
			if c.Execute {
				cp.Done = true
				return rep, cp.save(c.Checkpoint)
			}
			return rep, nil
		}
		if c.Execute {
			cp.LastKey = cp.LastKey[:0]
			for _, k := range upper {
				cp.LastKey = append(cp.LastKey, fmt.Sprint(k))
			}
			if err := cp.save(c.Checkpoint); err != nil {
				return rep, fmt.Errorf("save checkpoint: %w", err)
			}
		}
		lower = upper
		if err := wait(ctx, c.Pause); err != nil {
			return rep, err
		}
	}
}
