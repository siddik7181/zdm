// Package state records which phase of a plan has completed, so a crashed run can resume.
// The file is only a hint: every phase must inspect the database before acting.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Phase is a step of the pipeline, in execution order.
type Phase string

// Pipeline phases.
const (
	Preflight Phase = "preflight"
	Copy      Phase = "copy" // shadow table, capture, backfill and catch-up, run by the engine
	Verify    Phase = "verify"
	Swap      Phase = "swap"
	Done      Phase = "done"
)

var order = []Phase{Preflight, Copy, Verify, Swap, Done}

// State is persisted next to the plan.
type State struct {
	PlanHash  string    `json:"plan_hash"`
	Completed []Phase   `json:"completed"`
	Updated   time.Time `json:"updated"`
	Note      string    `json:"note,omitempty"`
}

// ErrPlanChanged means the state belongs to a different plan.
var ErrPlanChanged = errors.New("state belongs to a different plan; remove it only after checking the database")

// New returns an empty state for a plan.
func New(planHash string) State { return State{PlanHash: planHash} }

// Next returns the first phase not yet completed.
func (s State) Next() Phase {
	for _, p := range order {
		if !s.Has(p) {
			return p
		}
	}
	return Done
}

// Has reports whether a phase is completed.
func (s State) Has(p Phase) bool {
	for _, c := range s.Completed {
		if c == p {
			return true
		}
	}
	return false
}

// Complete marks the next phase done. Phases cannot be skipped or repeated.
func (s *State) Complete(p Phase, now time.Time) error {
	if p == Done || s.Next() != p {
		return fmt.Errorf("cannot complete %q: next phase is %q", p, s.Next())
	}
	s.Completed = append(s.Completed, p)
	s.Updated = now.UTC()
	return nil
}

// Save writes atomically: temp file, fsync, rename, so a crash leaves the old or the new file.
func (s State) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".zdm-state-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if _, err := tmp.Write(append(b, '\n')); err != nil {
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

// Load reads a state file. A missing file yields a new state. A different plan hash is an error.
func Load(path, planHash string) (State, error) {
	b, err := os.ReadFile(path) //nolint:gosec // derived from the plan path
	if errors.Is(err, os.ErrNotExist) {
		return New(planHash), nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.PlanHash != planHash {
		return State{}, ErrPlanChanged
	}
	return s, nil
}
