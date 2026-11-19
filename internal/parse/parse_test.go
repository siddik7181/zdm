package parse

import (
	"reflect"
	"testing"
)

func TestAlterClauses(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		table     string
		ops       []Op
		algorithm string
		lock      string
	}{
		{
			name:  "add nullable column",
			sql:   "ALTER TABLE `db`.`users` ADD COLUMN last_seen DATETIME NULL, ALGORITHM=INSTANT",
			table: "users", algorithm: "INSTANT",
			ops: []Op{{Kind: OpAddColumn, Column: "last_seen"}},
		},
		{
			name:  "add not null with default",
			sql:   "alter table t add column a int not null default 0",
			table: "t",
			ops:   []Op{{Kind: OpAddColumn, Column: "a", NotNull: true, HasDefault: true}},
		},
		{
			name:  "add generated and auto_increment",
			sql:   "ALTER TABLE t ADD COLUMN g INT AS (1+1) STORED, ADD COLUMN n BIGINT AUTO_INCREMENT UNIQUE",
			table: "t",
			ops: []Op{
				{Kind: OpAddColumn, Column: "g", Generated: true},
				{Kind: OpAddColumn, Column: "n", Generated: true},
			},
		},
		{
			name:  "lock clause",
			sql:   "ALTER TABLE t ADD INDEX i (a), ALGORITHM=INPLACE, LOCK=NONE",
			table: "t", algorithm: "INPLACE", lock: "NONE",
			ops: []Op{{Kind: OpAddIndex}},
		},
		{
			name:  "column rewrites",
			sql:   "ALTER TABLE t DROP COLUMN a, MODIFY COLUMN b BIGINT, CHANGE c d INT, RENAME COLUMN e TO f",
			table: "t",
			ops: []Op{
				{Kind: OpDropColumn, Column: "a"},
				{Kind: OpModifyColumn, Column: "b"},
				{Kind: OpChangeColumn, Column: "c"},
				{Kind: OpRenameColumn, Column: "e"},
			},
		},
		{
			name:  "keys",
			sql:   "ALTER TABLE t DROP PRIMARY KEY, ADD PRIMARY KEY (id), DROP INDEX i, ADD CONSTRAINT fk FOREIGN KEY (p) REFERENCES parent (id)",
			table: "t",
			ops: []Op{
				{Kind: OpDropPrimaryKey}, {Kind: OpAddPrimaryKey}, {Kind: OpDropIndex}, {Kind: OpAddForeignKey},
			},
		},
		{
			name:  "drop foreign key is not an index drop",
			sql:   "ALTER TABLE t DROP FOREIGN KEY fk",
			table: "t",
			ops:   []Op{{Kind: OpOther}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Script(tc.sql)
			if len(got) != 1 {
				t.Fatalf("got %d statements, want 1", len(got))
			}
			s := got[0]
			if s.Err != nil {
				t.Fatalf("parse error: %v", s.Err)
			}
			if s.Kind != KindAlterTable || s.Table != tc.table || s.Algorithm != tc.algorithm || s.Lock != tc.lock {
				t.Fatalf("got kind=%v table=%q algorithm=%q lock=%q", s.Kind, s.Table, s.Algorithm, s.Lock)
			}
			if !reflect.DeepEqual(s.Ops, tc.ops) {
				t.Fatalf("ops = %+v, want %+v", s.Ops, tc.ops)
			}
		})
	}
}

func TestDML(t *testing.T) {
	got := Script("UPDATE t SET a=1; UPDATE t SET a=1 WHERE id<10; DELETE FROM t; DELETE FROM t WHERE id=1;")
	want := []struct {
		kind  Kind
		where bool
	}{{KindUpdate, false}, {KindUpdate, true}, {KindDelete, false}, {KindDelete, true}}
	if len(got) != len(want) {
		t.Fatalf("got %d statements, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].HasWhere != w.where || got[i].Table != "t" {
			t.Errorf("stmt %d = kind %v where %v table %q", i, got[i].Kind, got[i].HasWhere, got[i].Table)
		}
	}
}

func TestScriptLinesAndRecovery(t *testing.T) {
	// Shaped like `alembic upgrade --sql` output: comments, a semicolon inside a string,
	// and one statement the parser cannot handle.
	sql := "-- Running upgrade a -> b\n" +
		"BEGIN;\n" +
		"\n" +
		"INSERT INTO t VALUES ('a;b');\n" +
		"THIS IS NOT SQL;\n" +
		"ALTER TABLE t ADD COLUMN c INT;\n" +
		"COMMIT;\n"
	got := Script(sql)
	if len(got) != 5 {
		t.Fatalf("got %d statements, want 5: %+v", len(got), got)
	}
	wantLines := []int{2, 4, 5, 6, 7}
	for i, l := range wantLines {
		if got[i].Line != l {
			t.Errorf("stmt %d line = %d, want %d (%q)", i, got[i].Line, l, got[i].SQL)
		}
	}
	if got[2].Err == nil {
		t.Error("garbage statement should carry a parse error")
	}
	if got[3].Err != nil || got[3].Kind != KindAlterTable {
		t.Errorf("statement after a bad one must still parse: %+v", got[3])
	}
}

func TestLinesWithBlockAndHashComments(t *testing.T) {
	sql := "/* a\nb */ ALTER TABLE t ADD COLUMN a INT;\n# c\n\n/* d */\nDELETE FROM t WHERE id=1;"
	got := Script(sql)
	if len(got) != 2 || got[0].Line != 2 || got[1].Line != 6 {
		t.Fatalf("got %+v", got)
	}
}

func TestUnsplittableScript(t *testing.T) {
	got := Script("INSERT INTO t VALUES ('unterminated")
	if len(got) != 1 || got[0].Err == nil || got[0].Line != 1 {
		t.Fatalf("want one error statement, got %+v", got)
	}
}

func TestEmptyScript(t *testing.T) {
	if got := Script(" \n-- nothing\n"); len(got) > 1 {
		t.Fatalf("got %d statements", len(got))
	}
}
