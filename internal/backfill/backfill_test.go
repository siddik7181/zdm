package backfill

import (
	"path/filepath"
	"strings"
	"testing"
)

func good() Config {
	return Config{Schema: "app", Table: "t", Set: "c = 1", Chunk: 100}
}

func TestValidate(t *testing.T) {
	if err := good().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Config){
		"table injection": func(c *Config) { c.Table = "t`; DROP TABLE x" },
		"semicolon":       func(c *Config) { c.Set = "c=1; DROP TABLE t" },
		"line comment":    func(c *Config) { c.Where = "a=1 -- hide" },
		"block comment":   func(c *Config) { c.Where = "a=1 /* x */" },
		"hash comment":    func(c *Config) { c.Set = "c=1 # x" },
		"empty set":       func(c *Config) { c.Set = " " },
		"chunk zero":      func(c *Config) { c.Chunk = 0 },
		"chunk huge":      func(c *Config) { c.Chunk = 1 << 30 },
		"negative lag":    func(c *Config) { c.MaxLag = -1 },
	}
	for name, mutate := range bad {
		c := good()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestCheckpointRoundTripAndMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	c := good()
	cp, resumed, err := loadCheckpoint(path, c.fingerprint())
	if err != nil || resumed {
		t.Fatalf("fresh: %v %v", resumed, err)
	}
	cp.LastKey = []string{"42"}
	if err := cp.save(path); err != nil {
		t.Fatal(err)
	}
	got, resumed, err := loadCheckpoint(path, c.fingerprint())
	if err != nil || !resumed || strings.Join(got.LastKey, ",") != "42" {
		t.Fatalf("reload: %+v %v %v", got, resumed, err)
	}
	other := c
	other.Set = "c = 2"
	if _, _, err := loadCheckpoint(path, other.fingerprint()); err == nil {
		t.Fatal("a checkpoint from a different backfill must be refused")
	}
}
