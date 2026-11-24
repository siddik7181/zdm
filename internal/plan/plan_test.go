package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func good() Plan { return Default("app", "t", "ADD COLUMN c INT NULL", GhOst) }

func TestValidate(t *testing.T) {
	if err := good().Validate(); err != nil {
		t.Fatalf("default plan invalid: %v", err)
	}
	bad := map[string]func(*Plan){
		"table injection":    func(p *Plan) { p.Table = "t`; DROP TABLE x; --" },
		"empty schema":       func(p *Plan) { p.Schema = "" },
		"semicolon in alter": func(p *Plan) { p.Alter = "ADD COLUMN c INT; DROP TABLE t" },
		"empty alter":        func(p *Plan) { p.Alter = "  " },
		"bad engine":         func(p *Plan) { p.Engine = "magic" },
		"lock too long":      func(p *Plan) { p.Cutover.LockTimeoutSeconds = 60 },
		"no attempts":        func(p *Plan) { p.Cutover.MaxAttempts = 0 },
		"no backoff":         func(p *Plan) { p.Cutover.BackoffSeconds = 0 },
		"max below base":     func(p *Plan) { p.Cutover.BackoffSeconds, p.Cutover.MaxBackoffSeconds = 8, 4 },
		"tiny stall":         func(p *Plan) { p.StallTimeout = "1s" },
		"garbage stall":      func(p *Plan) { p.StallTimeout = "soon" },
		"version":            func(p *Plan) { p.Version = 2 },
	}
	for name, mutate := range bad {
		p := good()
		mutate(&p)
		if p.Validate() == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
}

func TestSaveLoadAndHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	p := good()
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := p.Save(path); err == nil {
		t.Fatal("Save must not overwrite an existing plan")
	}
	got, err := Load(path)
	if err != nil || got != p || got.Hash() != p.Hash() {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	other := p
	other.Alter = "ADD COLUMN d INT NULL"
	if other.Hash() == p.Hash() {
		t.Fatal("different plans must hash differently")
	}
}

func TestLoadRejectsUnknownFieldsAndInvalid(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := Load(write("typo.json", `{"version":1,"schemma":"app"}`)); err == nil || !strings.Contains(err.Error(), "schemma") {
		t.Fatalf("typo not caught: %v", err)
	}
	if _, err := Load(write("empty.json", `{}`)); err == nil {
		t.Fatal("empty plan must be invalid")
	}
}
