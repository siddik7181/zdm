# 6. Landscape, name and licences

Researched by web search; re-check before relying on it. **[verify]**

## Existing tools

| Tool | What it is | Relation to zdm |
|---|---|---|
| gh-ost (GitHub, MIT) | Triggerless online schema change from the binlog | zdm runs it as an external engine |
| pt-online-schema-change (Percona, GPL-2 or Artistic) | Trigger-based copy; the choice for foreign keys | zdm runs it as an external engine |
| **Spirit** (Block/Cash App, `block/spirit`) | gh-ost reimplementation, MySQL 8.0+: INSTANT first, resumable from a checkpoint table, always checksums before cut-over, kills blocking transactions | **Closest competitor, and it overlaps the engine half of zdm.** It is an engine, not a linter or preflight. |
| Skeema | Declarative schema as code, routes big tables through gh-ost or pt-osc via `alter-wrapper` | Alternative to orchestrating by hand; schema-as-code, not a cut-over safety tool |
| Atlas | Declarative or versioned migrations, planner and linter, many databases | Does not drive an online change tool |
| Vitess / PlanetScale online DDL | Managed online DDL | Needs Vitess or PlanetScale |

Positioning that survives this: zdm is **not** a new copy engine. Its value is the part around the
engine: lint with MySQL 8.4/9.7 knowledge, preflight of replicas and binlogs, a plan that picks an
engine, resumable phases, rollback, cleanup after crashes, and backfill. A future engine option could be
Spirit. Do not claim to replace gh-ost, pt-osc or Spirit.

## Name

`zdm` is already used by DataStax's `zdm-proxy` (Cassandra, Go) and Oracle's Zero Downtime Migration
(databases). Different domains, but searches will collide. Decide whether to rename before the first release.

## Licences

- zdm: Apache-2.0.
- gh-ost is MIT, pt-online-schema-change is GPL-2 or Artistic. zdm only runs them as separate processes and
  does not link, vendor or ship them, so zdm's licence is unaffected. Do **not** bundle either binary in a
  zdm release or container image without checking the GPL obligations.
