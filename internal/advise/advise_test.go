package advise

import (
	"testing"

	"github.com/siddik7181/zdm/internal/plan"
)

func TestChoose(t *testing.T) {
	ok := TableFacts{Exists: true, InnoDB: true, HasUniqueKey: true}
	with := func(m func(*TableFacts)) TableFacts { f := ok; m(&f); return f }
	cases := []struct {
		name   string
		f      TableFacts
		engine string
		refuse bool
	}{
		{"plain", ok, plan.GhOst, false},
		{"child fk", with(func(f *TableFacts) { f.ChildFKs = 1 }), plan.PtOsc, false},
		{"parent fk", with(func(f *TableFacts) { f.ParentFKs = 2 }), plan.PtOsc, false},
		{"trigger", with(func(f *TableFacts) { f.Triggers = 1 }), plan.PtOsc, false},
		{"galera", with(func(f *TableFacts) { f.Galera = true }), plan.PtOsc, false},
		{"missing", with(func(f *TableFacts) { f.Exists = false }), "", true},
		{"myisam", with(func(f *TableFacts) { f.InnoDB = false }), "", true},
		{"no key", with(func(f *TableFacts) { f.HasUniqueKey = false }), "", true},
	}
	for _, c := range cases {
		got := Choose(c.f)
		if got.Engine != c.engine || got.Refuse != c.refuse || len(got.Reasons) == 0 {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}
