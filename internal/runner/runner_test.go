package runner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/siddik7181/zdm/internal/state"
)

func TestFailedPhaseIsRetriedAndDonePhasesAreNotRepeated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	calls := map[state.Phase]int{}
	failMigrate := true
	steps := Steps{
		state.Preflight: func(context.Context) error { calls[state.Preflight]++; return nil },
		state.Migrate: func(context.Context) error {
			calls[state.Migrate]++
			if failMigrate {
				return errors.New("engine crashed")
			}
			return nil
		},
		state.Verify: func(context.Context) error { calls[state.Verify]++; return nil },
	}
	nop := func(string) {}
	now := time.Now

	s, err := Run(context.Background(), state.New("h"), path, steps, now, nop)
	if err == nil || s.Next() != state.Migrate {
		t.Fatalf("first run: next=%s err=%v", s.Next(), err)
	}

	// A new process loads the saved state and continues from the failed phase.
	failMigrate = false
	loaded, err := state.Load(path, "h")
	if err != nil {
		t.Fatal(err)
	}
	s, err = Run(context.Background(), loaded, path, steps, now, nop)
	if err != nil || s.Next() != state.Done {
		t.Fatalf("second run: next=%s err=%v", s.Next(), err)
	}
	if calls[state.Preflight] != 1 || calls[state.Migrate] != 2 || calls[state.Verify] != 1 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestMissingStepAndCancelledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	if _, err := Run(context.Background(), state.New("h"), path, Steps{}, time.Now, func(string) {}); err == nil {
		t.Fatal("missing step must error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	steps := Steps{state.Preflight: func(c context.Context) error { return c.Err() }}
	if _, err := Run(ctx, state.New("h"), path, steps, time.Now, func(string) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
