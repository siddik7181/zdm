// Package runner drives a plan through its phases, persisting progress after each one.
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/siddik7181/zdm/internal/state"
)

// Step is the work of one phase. It must inspect the database and be safe to run again,
// because a crash can happen after the work but before progress is saved.
type Step func(ctx context.Context) error

// Steps maps each phase to its work.
type Steps map[state.Phase]Step

// Run executes every incomplete phase in order and saves state after each success.
// On failure it stops, leaving the failed phase to be retried by the next run.
func Run(ctx context.Context, s state.State, statePath string, steps Steps, now func() time.Time, log func(string)) (state.State, error) {
	for p := s.Next(); p != state.Done; p = s.Next() {
		step, ok := steps[p]
		if !ok {
			return s, fmt.Errorf("no implementation for phase %q", p)
		}
		log("phase " + string(p) + ": start")
		if err := step(ctx); err != nil {
			return s, fmt.Errorf("phase %s: %w", p, err)
		}
		if err := s.Complete(p, now()); err != nil {
			return s, err
		}
		if err := s.Save(statePath); err != nil {
			return s, fmt.Errorf("save state after %s: %w", p, err)
		}
		log("phase " + string(p) + ": done")
	}
	return s, nil
}
