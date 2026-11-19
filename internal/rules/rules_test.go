package rules

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/siddik7181/zdm/internal/parse"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestGolden(t *testing.T) {
	inputs, err := filepath.Glob("testdata/*.sql")
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no golden inputs: %v", err)
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".sql")
		t.Run(name, func(t *testing.T) {
			sql, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			findings := Check(parse.Script(string(sql)))
			if findings == nil {
				findings = []Finding{}
			}
			got, err := json.MarshalIndent(findings, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := strings.TrimSuffix(in, ".sql") + ".expected.json"
			if *update {
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("findings differ from %s (run with -update to accept)\n got: %s\nwant: %s", golden, got, want)
			}
		})
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("é", 300)
	got := truncate(s)
	if !strings.HasSuffix(got, "...") || len(got) > maxStatementLen+3 {
		t.Fatalf("bad truncation: len=%d", len(got))
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatal("truncated in the middle of a rune")
		}
	}
}

func TestSeverityJSON(t *testing.T) {
	b, _ := json.Marshal(Finding{Severity: Warning})
	if !strings.Contains(string(b), `"severity":"warning"`) {
		t.Fatalf("got %s", b)
	}
}
