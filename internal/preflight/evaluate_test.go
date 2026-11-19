package preflight

import (
	"errors"
	"strings"
	"testing"
)

func goodVars() map[string]string {
	return map[string]string{
		"gtid_mode": "ON", "enforce_gtid_consistency": "ON", "log_bin": "ON",
		"binlog_format": "ROW", "binlog_row_image": "FULL",
		"binlog_expire_logs_seconds": "2592000", "read_only": "OFF",
	}
}

func lag(n int64) *int64 { return &n }

func find(t *testing.T, rs []Result, check string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Check == check {
			return r
		}
	}
	t.Fatalf("no result for %q in %+v", check, rs)
	return Result{}
}

var opts = Options{MaxLagSeconds: 10, LongTxnSeconds: 60}

func TestHealthyPrimaryPasses(t *testing.T) {
	f := Facts{
		Version: "9.7.4", Vars: goodVars(),
		Replicas: []ReplicaFacts{{Addr: "r1:3306", IORunning: "Yes", SQLRunning: "Yes", LagSeconds: lag(0)}},
	}
	if w := Worst(Evaluate(f, opts)); w != Pass {
		t.Fatalf("worst = %v, want pass: %+v", w, Evaluate(f, opts))
	}
}

func TestVersions(t *testing.T) {
	for v, want := range map[string]Status{
		"8.4.11": Pass, "9.7.2-google": Pass, "9.7.4-log": Pass,
		"9.3.0": Warn, "8.0.40": Fail, "5.7.44": Fail, "weird": Warn,
	} {
		f := Facts{Version: v, Vars: goodVars()}
		if got := find(t, Evaluate(f, opts), "version").Status; got != want {
			t.Errorf("%s: %v, want %v", v, got, want)
		}
	}
}

func TestBinlogSettingsFail(t *testing.T) {
	for name, bad := range map[string]string{
		"gtid_mode": "OFF", "enforce_gtid_consistency": "OFF", "log_bin": "OFF",
		"binlog_format": "MIXED", "binlog_row_image": "MINIMAL",
	} {
		v := goodVars()
		v[name] = bad
		if got := find(t, Evaluate(Facts{Version: "8.4.1", Vars: v}, opts), name).Status; got != Fail {
			t.Errorf("%s=%s: %v, want fail", name, bad, got)
		}
	}
	v := goodVars()
	delete(v, "gtid_mode")
	if got := find(t, Evaluate(Facts{Version: "8.4.1", Vars: v}, opts), "gtid_mode").Status; got != Fail {
		t.Errorf("missing variable must fail, got %v", got)
	}
}

func TestRetentionAndReadOnly(t *testing.T) {
	v := goodVars()
	v["binlog_expire_logs_seconds"] = "3600"
	v["read_only"] = "ON"
	rs := Evaluate(Facts{Version: "8.4.1", Vars: v}, opts)
	if find(t, rs, "binlog_retention").Status != Warn || find(t, rs, "target_writable").Status != Warn {
		t.Fatalf("got %+v", rs)
	}
	v["binlog_expire_logs_seconds"] = "0" // never expire
	if find(t, Evaluate(Facts{Version: "8.4.1", Vars: v}, opts), "binlog_retention").Status != Pass {
		t.Fatal("0 means no expiry and must pass")
	}
}

func TestLongTransactions(t *testing.T) {
	f := Facts{Version: "8.4.1", Vars: goodVars(), LongTxns: []Txn{{ID: "42", AgeSeconds: 900}}}
	r := find(t, Evaluate(f, opts), "long_transactions")
	if r.Status != Warn || !strings.Contains(r.Detail, "900s") {
		t.Fatalf("got %+v", r)
	}
	f = Facts{Version: "8.4.1", Vars: goodVars(), LongTxnsErr: errors.New("denied")}
	if find(t, Evaluate(f, opts), "long_transactions").Status != Warn {
		t.Fatal("an unreadable check must warn, not pass")
	}
}

func TestReplicas(t *testing.T) {
	cases := map[string]struct {
		r    ReplicaFacts
		want Status
	}{
		"ok":         {ReplicaFacts{IORunning: "Yes", SQLRunning: "Yes", LagSeconds: lag(3)}, Pass},
		"lagging":    {ReplicaFacts{IORunning: "Yes", SQLRunning: "Yes", LagSeconds: lag(11)}, Warn},
		"at limit":   {ReplicaFacts{IORunning: "Yes", SQLRunning: "Yes", LagSeconds: lag(10)}, Pass},
		"stopped":    {ReplicaFacts{IORunning: "No", SQLRunning: "Yes"}, Fail},
		"unknown":    {ReplicaFacts{IORunning: "Yes", SQLRunning: "Yes"}, Fail},
		"unreadable": {ReplicaFacts{Err: errors.New("refused")}, Fail},
	}
	for name, tc := range cases {
		tc.r.Addr = "r:3306"
		f := Facts{Version: "8.4.1", Vars: goodVars(), Replicas: []ReplicaFacts{tc.r}}
		if got := find(t, Evaluate(f, opts), "replica r:3306").Status; got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
	if find(t, Evaluate(Facts{Version: "8.4.1", Vars: goodVars()}, opts), "gating_replicas").Status != Warn {
		t.Fatal("no replicas given must warn")
	}
}
