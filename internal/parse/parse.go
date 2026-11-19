// Package parse turns a SQL script into a small, parser-independent model.
// It is the only package that imports the Vitess sqlparser, so the parser can be swapped.
package parse

import (
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// Kind is the statement category the lint rules care about.
type Kind int

// Statement kinds.
const (
	KindOther Kind = iota
	KindAlterTable
	KindCreateTable
	KindDropTable
	KindUpdate
	KindDelete
)

// OpKind is one clause of an ALTER TABLE.
type OpKind int

// ALTER TABLE clause kinds.
const (
	OpOther OpKind = iota
	OpAddColumn
	OpDropColumn
	OpModifyColumn
	OpChangeColumn
	OpRenameColumn
	OpAddIndex
	OpDropIndex
	OpAddPrimaryKey
	OpDropPrimaryKey
	OpAddForeignKey
)

// Op is one ALTER TABLE clause. Column fields are only set for column ops.
type Op struct {
	Kind       OpKind
	Column     string
	NotNull    bool
	HasDefault bool
	Generated  bool // generated or auto_increment column
}

// Statement is one SQL statement from a script.
type Statement struct {
	SQL   string
	Line  int // 1-based line where the statement starts
	Kind  Kind
	Table string // target table, unqualified and unquoted
	Err   error  // set when the statement could not be parsed; other fields are then zero
	// ALTER TABLE only.
	Ops       []Op
	Algorithm string // upper-case ALGORITHM= value, "" when absent
	Lock      string // upper-case LOCK= value, "" when absent
	// UPDATE/DELETE only.
	HasWhere bool
}

var parser = sqlparser.NewTestParser()

// Script splits sql into statements and parses each. A statement that fails to parse is
// returned with Err set; the rest of the script is still processed. A script that cannot be
// split (for example an unterminated quote) yields a single statement carrying that error.
func Script(sql string) []Statement {
	pieces, err := parser.SplitStatementToPieces(sql)
	if err != nil {
		return []Statement{{SQL: sql, Line: 1, Err: err}}
	}
	var out []Statement
	cursor, line := 0, 1 // line is the line number at offset cursor
	for _, piece := range pieces {
		text := strings.TrimSpace(piece)
		if text == "" {
			continue
		}
		at := line
		if i := strings.Index(sql[cursor:], text); i >= 0 {
			start := cursor + i
			at += strings.Count(sql[cursor:start+leadingNoise(text)], "\n")
			line += strings.Count(sql[cursor:start+len(text)], "\n")
			cursor = start + len(text)
		}
		out = append(out, statement(text, at))
	}
	return out
}

// leadingNoise returns the length of the whitespace and comments before the first token.
func leadingNoise(s string) int {
	i := 0
	for i < len(s) {
		rest := s[i:]
		switch {
		case rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r':
			i++
		case strings.HasPrefix(rest, "--") || rest[0] == '#':
			end := strings.IndexByte(rest, '\n')
			if end < 0 {
				return len(s)
			}
			i += end + 1
		case strings.HasPrefix(rest, "/*") && !strings.HasPrefix(rest, "/*!"):
			end := strings.Index(rest, "*/")
			if end < 0 {
				return len(s)
			}
			i += end + 2
		default:
			return i
		}
	}
	return i
}

func statement(text string, line int) Statement {
	s := Statement{SQL: text, Line: line}
	node, err := parser.Parse(text)
	if err != nil {
		s.Err = err
		return s
	}
	switch n := node.(type) {
	case *sqlparser.AlterTable:
		s.Kind = KindAlterTable
		s.Table = n.Table.Name.String()
		for _, opt := range n.AlterOptions {
			switch o := opt.(type) {
			case sqlparser.AlgorithmValue:
				s.Algorithm = strings.ToUpper(string(o))
			case *sqlparser.LockOption:
				s.Lock = lockName(o)
			default:
				s.Ops = append(s.Ops, ops(opt)...)
			}
		}
	case *sqlparser.CreateTable:
		s.Kind = KindCreateTable
		s.Table = n.Table.Name.String()
	case *sqlparser.DropTable:
		s.Kind = KindDropTable
		if len(n.FromTables) > 0 {
			s.Table = n.FromTables[0].Name.String()
		}
	case *sqlparser.Update:
		s.Kind = KindUpdate
		s.Table = firstTable(n.TableExprs)
		s.HasWhere = n.Where != nil
	case *sqlparser.Delete:
		s.Kind = KindDelete
		s.Table = firstTable(n.TableExprs)
		s.HasWhere = n.Where != nil
	}
	return s
}

// lockName extracts the value of a LOCK= clause (NONE, SHARED, EXCLUSIVE, DEFAULT).
func lockName(o *sqlparser.LockOption) string {
	f := strings.Fields(strings.ToUpper(sqlparser.String(o)))
	if len(f) == 0 {
		return ""
	}
	return strings.TrimPrefix(f[len(f)-1], "=")
}

func firstTable(exprs []sqlparser.TableExpr) string {
	if len(exprs) == 0 {
		return ""
	}
	if a, ok := exprs[0].(*sqlparser.AliasedTableExpr); ok {
		if t, err := a.TableName(); err == nil {
			return t.Name.String()
		}
	}
	return ""
}

func ops(opt sqlparser.AlterOption) []Op {
	switch o := opt.(type) {
	case *sqlparser.AddColumns:
		res := make([]Op, 0, len(o.Columns))
		for _, c := range o.Columns {
			op := Op{Kind: OpAddColumn, Column: c.Name.String()}
			if t := c.Type; t != nil && t.Options != nil {
				op.NotNull = t.Options.Null != nil && !*t.Options.Null
				op.HasDefault = t.Options.Default != nil
				op.Generated = t.Options.As != nil || t.Options.Autoincrement
			}
			res = append(res, op)
		}
		return res
	case *sqlparser.DropColumn:
		return []Op{{Kind: OpDropColumn, Column: o.Name.Name.String()}}
	case *sqlparser.ModifyColumn:
		return []Op{{Kind: OpModifyColumn, Column: o.NewColDefinition.Name.String()}}
	case *sqlparser.ChangeColumn:
		return []Op{{Kind: OpChangeColumn, Column: o.OldColumn.Name.String()}}
	case *sqlparser.RenameColumn:
		return []Op{{Kind: OpRenameColumn, Column: o.OldName.Name.String()}}
	case *sqlparser.AddIndexDefinition:
		if o.IndexDefinition.Info.Type == sqlparser.IndexTypePrimary {
			return []Op{{Kind: OpAddPrimaryKey}}
		}
		return []Op{{Kind: OpAddIndex}}
	case *sqlparser.DropKey:
		switch o.Type {
		case sqlparser.PrimaryKeyType:
			return []Op{{Kind: OpDropPrimaryKey}}
		case sqlparser.ForeignKeyType:
			return []Op{{Kind: OpOther}}
		}
		return []Op{{Kind: OpDropIndex}}
	case *sqlparser.AddConstraintDefinition:
		if _, ok := o.ConstraintDefinition.Details.(*sqlparser.ForeignKeyDefinition); ok {
			return []Op{{Kind: OpAddForeignKey}}
		}
	}
	return []Op{{Kind: OpOther}}
}
