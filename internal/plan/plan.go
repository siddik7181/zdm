// Package plan defines the plan file: what to change and how, with no credentials in it.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Engines that can run a plan.
const (
	GhOst = "gh-ost"
	PtOsc = "pt-osc"
)

// Cutover bounds how long the final swap may block traffic.
type Cutover struct {
	LockTimeoutSeconds int `json:"lock_timeout_seconds"` // per attempt; the table is blocked at most this long
	MaxAttempts        int `json:"max_attempts"`
	// BackoffSeconds and MaxBackoffSeconds shape the wait between failed swap attempts: it doubles from
	// the first up to the second. zdm's own retries (rollback) add jitter; gh-ost's have none.
	BackoffSeconds    int `json:"backoff_seconds"`
	MaxBackoffSeconds int `json:"max_backoff_seconds"`
	MaxLagSeconds     int `json:"max_lag_seconds"` // gating replicas must be under this to swap
}

// Plan is a reviewed, reproducible description of one schema change.
type Plan struct {
	Version int     `json:"version"`
	Schema  string  `json:"schema"`
	Table   string  `json:"table"`
	Alter   string  `json:"alter"` // the ALTER clauses only, e.g. "ADD COLUMN c INT NULL"
	Engine  string  `json:"engine"`
	Cutover Cutover `json:"cutover"`
	// StallTimeout is how long the engine may print nothing before it is killed.
	StallTimeout string `json:"stall_timeout"`
	// KeepOldTable leaves the pre-change table in place so the change can be rolled back.
	KeepOldTable bool `json:"keep_old_table"`
}

// Default fills the settings a plan author usually does not set.
func Default(schema, table, alter, engine string) Plan {
	return Plan{
		Version: 1, Schema: schema, Table: table, Alter: alter, Engine: engine,
		Cutover:      Cutover{LockTimeoutSeconds: 1, MaxAttempts: 10, BackoffSeconds: 1, MaxBackoffSeconds: 30, MaxLagSeconds: 2},
		StallTimeout: "5m", KeepOldTable: true,
	}
}

var ident = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}$`)

// Validate rejects a plan that is unsafe or cannot run.
func (p Plan) Validate() error {
	var errs []error
	if p.Version != 1 {
		errs = append(errs, fmt.Errorf("unsupported plan version %d", p.Version))
	}
	if !ident.MatchString(p.Schema) || !ident.MatchString(p.Table) {
		errs = append(errs, errors.New("schema and table must be plain identifiers (letters, digits, _ and $)"))
	}
	if strings.TrimSpace(p.Alter) == "" || strings.Contains(p.Alter, ";") {
		errs = append(errs, errors.New("alter must be non-empty ALTER clauses with no semicolon"))
	}
	if p.Engine != GhOst && p.Engine != PtOsc {
		errs = append(errs, fmt.Errorf("engine must be %q or %q", GhOst, PtOsc))
	}
	if c := p.Cutover; c.LockTimeoutSeconds < 1 || c.LockTimeoutSeconds > 10 || c.MaxAttempts < 1 || c.MaxLagSeconds < 0 ||
		c.BackoffSeconds < 1 || c.MaxBackoffSeconds < c.BackoffSeconds {
		errs = append(errs, errors.New("cutover: lock_timeout_seconds must be 1-10, max_attempts >= 1, max_lag_seconds >= 0, backoff_seconds >= 1, max_backoff_seconds >= backoff_seconds"))
	}
	if d, err := time.ParseDuration(p.StallTimeout); err != nil || d < 10*time.Second {
		errs = append(errs, errors.New("stall_timeout must be a duration of at least 10s"))
	}
	return errors.Join(errs...)
}

// Stall returns the parsed stall timeout.
func (p Plan) Stall() time.Duration {
	d, _ := time.ParseDuration(p.StallTimeout)
	return d
}

// Hash identifies the plan's content, so saved state is never applied to a different plan.
func (p Plan) Hash() string {
	b, _ := json.Marshal(p) // struct marshaling is deterministic
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Save writes the plan as indented JSON. It refuses to overwrite an existing file.
func (p Plan) Save(path string) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // user-chosen output path
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Load reads and validates a plan file. Unknown fields are an error, to catch typos.
func Load(path string) (Plan, error) {
	b, err := os.ReadFile(path) //nolint:gosec // user-chosen plan path
	if err != nil {
		return Plan{}, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	// Plans written before the backoff fields existed keep working with the defaults.
	p := Plan{Cutover: Cutover{BackoffSeconds: 1, MaxBackoffSeconds: 30}}
	if err := dec.Decode(&p); err != nil {
		return Plan{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return p, p.Validate()
}
