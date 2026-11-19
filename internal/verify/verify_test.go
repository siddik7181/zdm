package verify

import (
	"slices"
	"strings"
	"testing"
)

func TestCommonColumnsIgnoresAddedAndDropped(t *testing.T) {
	got := commonColumns([]string{"id", "v", "old"}, []string{"id", "new", "v"})
	if !slices.Equal(got, []string{"id", "v"}) {
		t.Fatalf("got %v", got)
	}
}

func TestQuotingAndExpressions(t *testing.T) {
	if q("a`b") != "`a``b`" {
		t.Errorf("backtick not escaped: %s", q("a`b"))
	}
	e := rowExpr([]string{"a", "b"})
	if !strings.Contains(e, "IFNULL(HEX(`a`),'N')") || !strings.HasPrefix(e, "CRC32(CONCAT_WS('#',") {
		t.Errorf("expr = %s", e)
	}
	if render([]any{[]byte("x"), int64(2)}) != "x,2" {
		t.Error("render")
	}
}
