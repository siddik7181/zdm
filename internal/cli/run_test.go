package cli

import (
	"strings"
	"testing"
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
		{"plan"}, {"plan", "--dsn", "x", "--engine", "magic"}, {"run"}, {"run", "plan.json"}, {"status"},
	} {
		if code := Run(args, Streams{Out: discard{}, Err: discard{}}); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }
