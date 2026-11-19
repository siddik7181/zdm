package backfill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/siddik7181/zdm/internal/preflight"
)

// ReplicaLag returns a Lag that reports the worst lag over the replicas. A replica whose
// replication is stopped, or whose lag is unknown, is an error: unknown lag is not zero lag.
func ReplicaLag(replicas []preflight.Replica) Lag {
	return func(ctx context.Context) (time.Duration, error) {
		var worst time.Duration
		for _, r := range replicas {
			f := preflight.ReadReplica(ctx, r.Addr, r.DB)
			switch {
			case f.Err != nil:
				return 0, fmt.Errorf("%s: %w", r.Addr, f.Err)
			case f.IORunning != "Yes" || f.SQLRunning != "Yes":
				return 0, fmt.Errorf("%s: replication is not running", r.Addr)
			case f.LagSeconds == nil:
				return 0, errors.New(r.Addr + ": lag unknown")
			}
			if l := time.Duration(*f.LagSeconds) * time.Second; l > worst {
				worst = l
			}
		}
		return worst, nil
	}
}
