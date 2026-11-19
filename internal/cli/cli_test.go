package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, stdin string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = Run(args, Streams{In: strings.NewReader(stdin), Out: &o, Err: &e})
	return code, o.String(), e.String()
}

const risky = "ALTER TABLE t MODIFY COLUMN a BIGINT;\nDELETE FROM t;\n"

func TestLintExitCodes(t *testing.T) {
	cases := []struct {
		name  string
		sql   string
		flags []string
		want  int
	}{
		{"clean", "ALTER TABLE t ADD COLUMN a INT NULL, ALGORITHM=INSTANT;", nil, exitOK},
		{"error finding fails by default", risky, nil, exitFindings},
		{"warning only passes at default", "ALTER TABLE t MODIFY COLUMN a BIGINT;", nil, exitOK},
		{"warning fails with --fail-on warning", "ALTER TABLE t MODIFY COLUMN a BIGINT;", []string{"--fail-on", "warning"}, exitFindings},
		{"never", risky, []string{"--fail-on", "never"}, exitOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"lint"}, tc.flags...)
			code, _, errOut := run(t, tc.sql, append(args, "-")...)
			if code != tc.want {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.want, errOut)
			}
		})
	}
}

func TestLintJSONAndText(t *testing.T) {
	_, out, _ := run(t, risky, "lint", "--format", "json", "--fail-on", "never", "-")
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 2 {
		t.Fatalf("bad json (%v): %s", err, out)
	}
	if got[0]["rule"] != "column-rewrite" || got[0]["severity"] != "warning" || got[1]["line"] != float64(2) {
		t.Fatalf("unexpected findings: %v", got)
	}

	_, out, _ = run(t, "SELECT 1;", "lint", "--format", "json", "-")
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("clean json must be [], got %q", out)
	}

	_, out, _ = run(t, risky, "lint", "--fail-on", "never", "-")
	if !strings.Contains(out, "-:2: error [unbounded-dml]") {
		t.Fatalf("text output: %q", out)
	}
}

func TestLintFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.sql")
	if err := os.WriteFile(p, []byte(risky), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(t, "", "lint", p)
	if code != exitFindings || !strings.Contains(out, p+":2:") {
		t.Fatalf("exit %d out %q", code, out)
	}
	if code, _, _ := run(t, "", "lint", filepath.Join(t.TempDir(), "missing.sql")); code != exitUsage {
		t.Fatalf("missing file exit %d, want %d", code, exitUsage)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{}, {"nope"}, {"lint"}, {"lint", "a", "b"}, {"lint", "--format", "xml", "-"}, {"lint", "--fail-on", "x", "-"}, {"lint", "--bogus", "-"},
	} {
		if code, _, _ := run(t, "", args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestVersionAndHelp(t *testing.T) {
	if code, out, _ := run(t, "", "version"); code != exitOK || strings.TrimSpace(out) != "dev" {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, out, _ := run(t, "", "--help"); code != exitOK || !strings.Contains(out, "usage: zdm") {
		t.Fatalf("help: %d %q", code, out)
	}
}
