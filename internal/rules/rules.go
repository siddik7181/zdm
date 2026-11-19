// Package rules lints a parsed migration script for changes that can lock or rebuild a live table.
// Rules are offline and conservative: they flag patterns visible in the SQL alone.
package rules

import (
	"unicode/utf8"

	"github.com/siddik7181/zdm/internal/parse"
)

// Severity orders findings; a higher value is worse.
type Severity int

// Severities.
const (
	Info Severity = iota
	Warning
	Error
)

var severityNames = [...]string{"info", "warning", "error"}

func (s Severity) String() string { return severityNames[s] }

// MarshalText makes severities serialize as their names in JSON.
func (s Severity) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Finding is one problem found in one statement.
type Finding struct {
	Severity  Severity `json:"severity"`
	Rule      string   `json:"rule"`
	Line      int      `json:"line"`
	Statement string   `json:"statement"`
	Why       string   `json:"why"`
	Fix       string   `json:"fix"`
}

const maxStatementLen = 200

// Check runs every rule over a script and returns findings in statement order.
func Check(stmts []parse.Statement) []Finding {
	var out []Finding
	altered := map[string]bool{}
	for _, s := range stmts {
		add := func(sev Severity, rule, why, fix string) {
			out = append(out, Finding{sev, rule, s.Line, truncate(s.SQL), why, fix})
		}
		if s.Err != nil {
			add(Warning, "unparsed", "The statement could not be parsed, so it was not checked: "+s.Err.Error(),
				"Check it by hand, or split it into smaller statements.")
			continue
		}
		switch s.Kind {
		case parse.KindAlterTable:
			checkAlter(s, altered, add)
		case parse.KindUpdate, parse.KindDelete:
			if !s.HasWhere {
				add(Error, "unbounded-dml", "UPDATE or DELETE with no WHERE touches every row in one transaction: long locks, huge undo log, replica lag.",
					"Move the data change to a batched backfill (keyset pagination, throttled).")
			}
		}
	}
	return out
}

type adder func(sev Severity, rule, why, fix string)

func checkAlter(s parse.Statement, altered map[string]bool, add adder) {
	if altered[s.Table] {
		add(Warning, "multiple-alters", "A second ALTER on `"+s.Table+"` in the same migration takes another metadata lock and may rebuild twice.",
			"Combine the changes into one ALTER TABLE, or split them into separate migrations.")
	}
	altered[s.Table] = true

	if s.Algorithm == "COPY" {
		add(Error, "algorithm-copy", "ALGORITHM=COPY rebuilds the whole table and blocks writes while it runs.",
			"Use an online schema change tool, or an expand/contract migration.")
	}
	if s.Algorithm == "INSTANT" && s.Lock != "" && s.Lock != "DEFAULT" {
		add(Error, "instant-with-lock", "ALGORITHM=INSTANT does not accept a LOCK clause; MySQL rejects the statement.",
			"Remove the LOCK clause.")
	}

	onlyAdds := len(s.Ops) > 0
	for _, op := range s.Ops {
		if op.Kind != parse.OpAddColumn || op.Generated {
			onlyAdds = false
		}
	}
	if onlyAdds && s.Algorithm == "" {
		add(Warning, "missing-instant-pin", "Without ALGORITHM=INSTANT, MySQL silently falls back to a table rebuild if the change does not qualify.",
			"Add ALGORITHM=INSTANT so an ineligible change fails instead of rebuilding.")
	}

	for _, op := range s.Ops {
		switch op.Kind {
		case parse.OpAddColumn:
			if op.NotNull {
				why := "NOT NULL on an existing table needs a permanent default, and the default becomes part of the schema."
				if !op.HasDefault {
					why = "NOT NULL with no DEFAULT makes MySQL fill existing rows with an implicit value (0, '', ...) that the schema never states."
				}
				add(Warning, "not-null-add-column", why,
					"Add the column as NULL, backfill, then tighten later; or get the default approved explicitly.")
			}
		case parse.OpModifyColumn, parse.OpChangeColumn:
			add(Warning, "column-rewrite", "Changing column `"+op.Column+"` usually rebuilds the table and is not INSTANT.",
				"Use expand/contract: add a new column, backfill, switch readers, drop the old one.")
		case parse.OpAddPrimaryKey, parse.OpDropPrimaryKey:
			add(Warning, "primary-key-change", "Changing the primary key rebuilds the whole table.",
				"Use an online schema change tool on a shadow table.")
		case parse.OpDropColumn, parse.OpRenameColumn:
			add(Warning, "breaking-column-change", "Column `"+op.Column+"` is dropped or renamed: cheap for MySQL, but application versions still running read the old name.",
				"Deploy code that no longer uses the column first (contract phase), then drop or rename.")
		case parse.OpAddIndex:
			add(Info, "add-index", "A secondary index is built in place, but replicas apply it serially and can lag on a large table.",
				"Check replica lag during the build; for very large tables use an online schema change tool.")
		case parse.OpAddForeignKey:
			add(Warning, "add-foreign-key", "Adding a foreign key can take long locks and validates existing rows; gh-ost cannot run on tables with foreign keys.",
				"Prefer pt-online-schema-change for tables with foreign keys, and check the parent key is unique.")
		}
	}
}

func truncate(s string) string {
	if len(s) <= maxStatementLen {
		return s
	}
	n := maxStatementLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}
