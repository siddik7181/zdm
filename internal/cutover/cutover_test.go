package cutover

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/siddik7181/zdm/internal/plan"
)

func TestRetryableOnlyForLockErrors(t *testing.T) {
	if !retryable(fmt.Errorf("x: %w", &mysql.MySQLError{Number: 1205})) || !retryable(&mysql.MySQLError{Number: 1213}) {
		t.Error("lock timeout and deadlock must be retryable")
	}
	if retryable(&mysql.MySQLError{Number: 1146}) || retryable(errors.New("boom")) {
		t.Error("other errors must not be retried")
	}
}

func TestOldTableNames(t *testing.T) {
	if got := OldTable(plan.Default("s", "t", "ADD COLUMN c INT", plan.GhOst)); got != "_t_del" {
		t.Errorf("gh-ost old table = %s", got)
	}
	if got := OldTable(plan.Default("s", "t", "ADD COLUMN c INT", plan.PtOsc)); got != "_t_old" {
		t.Errorf("pt-osc old table = %s", got)
	}
}

func TestSleepStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("err=%v", err)
	}
}

func TestBackoffDoublesCapsAndJitters(t *testing.T) {
	c := plan.Cutover{BackoffSeconds: 1, MaxBackoffSeconds: 8}
	cases := []struct {
		attempt int
		r       float64
		want    time.Duration
	}{
		{1, 0, 500 * time.Millisecond},
		{1, 1, time.Second},
		{3, 0, 2 * time.Second},
		{3, 1, 4 * time.Second},
		{10, 0, 4 * time.Second},
		{10, 1, 8 * time.Second}, // capped at 8 s
	}
	for _, tc := range cases {
		if got := Backoff(c, tc.attempt, tc.r); got != tc.want {
			t.Errorf("attempt %d r=%v: got %v want %v", tc.attempt, tc.r, got, tc.want)
		}
	}
}

func TestPinnedAlgorithmIsNeverOverridden(t *testing.T) {
	p := plan.Default("s", "t", "ADD COLUMN c INT NULL, ALGORITHM=INPLACE", plan.GhOst)
	if err := TryInstant(context.Background(), nil, p, nil); !errors.Is(err, ErrNotInstant) {
		t.Fatalf("got %v", err)
	}
}
