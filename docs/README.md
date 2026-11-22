# zdm docs index

`zdm` (working name, collision unchecked) is a planned open-source tool for
**zero-downtime schema changes on MySQL**. It is also a learning project.

These docs capture the discussion, research, decisions and plan so nothing is lost.

| File | Contents |
|---|---|
| [01-concepts.md](01-concepts.md) | How migrations work end to end; MySQL internals (buffer pool, redo, undo, binlog), metadata locks, online vs offline DDL |
| [02-research-findings.md](02-research-findings.md) | Sourced findings: gh-ost, pt-osc, Vitess, Cloud SQL, MySQL 8.4/9.7. Marks what is verified vs not |
| [03-architecture.md](03-architecture.md) | Pipeline, strategy selection, cut-over, topology and crash safety, layout |
| [04-roadmap.md](04-roadmap.md) | Build order, learning milestones, spikes, open-source plan |
| [05-decisions-and-open-questions.md](05-decisions-and-open-questions.md) | Decisions made, positioning, risks, open questions |
| [06-landscape.md](06-landscape.md) | Competitors (incl. Spirit), name clash, licences |
| [07-large-table-run.md](07-large-table-run.md) | 100M-row gh-ost run: time, lag, checks, limits |

## Status (2026-11-19)

- Design phase. **No Go code yet.**
- `docs/` is the only content in the repo.
- Next step: milestone 1, a local Docker lab (MySQL 8.4 and 9.7, primary + 2 replicas, GTID on). Both `mysql:8.4` and `mysql:9.7` images exist on Docker Hub (checked with `docker manifest inspect`).

## Reading the "verified" markers

- **[sourced]**: found in a web source or official doc during research (source named).
- **[verify]**: from memory or a single secondary source. Check before relying on it.
- **[spike]**: needs a real experiment (Cloud SQL, 9.7 tool compatibility, etc.).
