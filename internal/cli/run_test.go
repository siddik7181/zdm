package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/siddik7181/zdm/internal/plan"
)

func TestConnFromDSN(t *testing.T) {
	c, err := connFromDSN("u:p@tcp(db.example:3307)/")
	if err != nil || c.Host != "db.example" || c.Port != 3307 || c.User != "u" || c.Password != "p" {
		t.Fatalf("%+v %v", c, err)
	}
	// The driver fills in the default port.
	if c, err := connFromDSN("u:secret@tcp(hostonly)/"); err != nil || c.Port != 3306 {
		t.Fatalf("%+v %v", c, err)
	}
	_, err = connFromDSN("u:secret@unix(/tmp/s.sock)/")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unix socket must be rejected without echoing the password: %v", err)
	}
}

func TestPlanAndRunUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"plan"}, {"plan", "--dsn", "x", "--engine", "magic"}, {"run"}, {"run", "plan.json"}, {"status"}, {"rollback"}, {"finalize"}, {"verify"}, {"backfill"}, {"backfill", "--dsn", "x", "--schema", "a", "--table", "t", "--set", "c=1; DROP TABLE t"},
	} {
		if code := Run(args, Streams{Out: discard{}, Err: discard{}}); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }

func TestDestructiveCommandsNeedConfirmationBeforeConnecting(t *testing.T) {
	planFile := filepath.Join(t.TempDir(), "plan.json")
	if err := plan.Default("app", "t", "ADD COLUMN c INT NULL", plan.GhOst).Save(planFile); err != nil {
		t.Fatal(err)
	}
	// The DSN points nowhere; exit 2 with the flag message proves nothing was dialled.
	for _, cmd := range []string{"rollback", "finalize"} {
		var errb strings.Builder
		code := Run([]string{cmd, "--dsn", "u:p@tcp(127.0.0.1:1)/", planFile}, Streams{Out: discard{}, Err: &errb})
		if code != exitUsage || !strings.Contains(errb.String(), "pass --") {
			t.Errorf("%s: exit %d, stderr %q", cmd, code, errb.String())
		}
	}
}
