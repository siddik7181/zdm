package preflight

import (
	"fmt"
	"strconv"
	"strings"
)

// Status is the outcome of one check.
type Status int

// Statuses, ordered by severity.
const (
	Pass Status = iota
	Warn
	Fail
)

var statusNames = [...]string{"pass", "warn", "fail"}

func (s Status) String() string { return statusNames[s] }

// MarshalText makes statuses serialize as their names in JSON.
func (s Status) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Result is the verdict of one check.
type Result struct {
	Check  string `json:"check"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Options are the thresholds for Evaluate.
type Options struct {
	MaxLagSeconds  int64
	LongTxnSeconds int
}

// Evaluate judges the facts. It never touches a database.
func Evaluate(f Facts, o Options) []Result {
	var out []Result
	add := func(check string, st Status, format string, a ...any) {
		out = append(out, Result{check, st, fmt.Sprintf(format, a...)})
	}

	switch major, minor, ok := parseVersion(f.Version); {
	case !ok:
		add("version", Warn, "cannot parse version %q", f.Version)
	case major == 8 && minor == 4, major == 9 && minor == 7:
		add("version", Pass, "%s is a supported LTS release", f.Version)
	case major == 9:
		add("version", Warn, "%s is an innovation release; only 8.4 and 9.7 are tested", f.Version)
	default:
		add("version", Fail, "%s is not supported; targets are 8.4 LTS and 9.7 LTS", f.Version)
	}

	required := []struct{ name, want, why string }{
		{"gtid_mode", "ON", "replica-safe cut-over waits on GTID sets"},
		{"enforce_gtid_consistency", "ON", "required by gtid_mode=ON"},
		{"log_bin", "ON", "binary logging is needed for replication and gh-ost"},
		{"binlog_format", "ROW", "gh-ost reads row events"},
		{"binlog_row_image", "FULL", "gh-ost needs full row images"},
	}
	for _, r := range required {
		got, present := f.Vars[r.name]
		if present && strings.EqualFold(got, r.want) {
			add(r.name, Pass, "%s", got)
		} else {
			add(r.name, Fail, "is %q, want %s: %s", got, r.want, r.why)
		}
	}

	if v, ok := f.Vars["binlog_expire_logs_seconds"]; ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 && n < 86400 {
			add("binlog_retention", Warn, "%ds is under 24h; a long change may outlive its binlogs", n)
		} else {
			add("binlog_retention", Pass, "%ss", v)
		}
	}

	if strings.EqualFold(f.Vars["read_only"], "ON") {
		add("target_writable", Warn, "read_only is ON: this looks like a replica, not the primary")
	}

	switch {
	case f.LongTxnsErr != nil:
		add("long_transactions", Warn, "could not check (needs PROCESS privilege): %v", f.LongTxnsErr)
	case len(f.LongTxns) > 0:
		add("long_transactions", Warn, "%d transaction(s) open over %ds, the oldest is %ds (id %s); they block metadata locks and queue every query behind an ALTER",
			len(f.LongTxns), o.LongTxnSeconds, f.LongTxns[0].AgeSeconds, f.LongTxns[0].ID)
	default:
		add("long_transactions", Pass, "none open over %ds", o.LongTxnSeconds)
	}

	if len(f.Replicas) == 0 {
		add("gating_replicas", Warn, "none given; pass --replica-dsn for every replica that must keep up")
	}
	for _, r := range f.Replicas {
		name := "replica " + r.Addr
		switch {
		case r.Err != nil:
			add(name, Fail, "cannot read status: %v", r.Err)
		case r.IORunning != "Yes" || r.SQLRunning != "Yes":
			add(name, Fail, "replication not running (IO=%s, SQL=%s)", r.IORunning, r.SQLRunning)
		case r.LagSeconds == nil:
			add(name, Fail, "lag is unknown")
		case *r.LagSeconds > o.MaxLagSeconds:
			add(name, Warn, "lag %ds is over %ds", *r.LagSeconds, o.MaxLagSeconds)
		default:
			add(name, Pass, "running, lag %ds", *r.LagSeconds)
		}
	}
	return out
}

// Worst returns the most severe status in the results.
func Worst(rs []Result) Status {
	w := Pass
	for _, r := range rs {
		if r.Status > w {
			w = r.Status
		}
	}
	return w
}

// parseVersion reads "8.4.11", "9.7.4-log" or "8.4.11-google".
func parseVersion(v string) (major, minor int, ok bool) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	return major, minor, err1 == nil && err2 == nil
}
