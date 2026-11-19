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
	e := rowExpr([]string{"a", "b"})
	if !strings.Contains(e, "IFNULL(HEX(`a`),'N')") || !strings.HasPrefix(e, "CRC32(CONCAT_WS('#',") {
		t.Errorf("expr = %s", e)
	}
}
