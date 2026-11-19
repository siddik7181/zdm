package keyset

import (
	"slices"
	"testing"
)

func TestQuoteAndRange(t *testing.T) {
	if Quote("a`b") != "`a``b`" {
		t.Errorf("backtick not escaped: %s", Quote("a`b"))
	}
	w, a := Range([]string{"x", "y"}, nil, nil)
	if w != "TRUE" || a != nil {
		t.Errorf("open range: %q %v", w, a)
	}
	w, a = Range([]string{"x", "y"}, []any{1, "a"}, []any{9, "z"})
	if w != "TRUE AND (`x`,`y`) > (?,?) AND (`x`,`y`) <= (?,?)" || !slices.Equal(a, []any{1, "a", 9, "z"}) {
		t.Errorf("range: %q %v", w, a)
	}
	if Render([]any{"k", int64(2)}) != "k,2" {
		t.Error("render")
	}
}
