# Contributing

1. Read `docs/03-architecture.md` and `docs/05-decisions-and-open-questions.md` first.
2. `make check` must pass (vet, golangci-lint, tests).
3. Every lint rule needs a golden test: `testdata/<case>.sql` plus `<case>.expected.json`.
4. Claims about MySQL behavior must cite the manual for the exact version (8.4 or 9.7) or be tagged
   `[verify]` in the docs.
5. Nothing that mutates a database may default to executing: dry-run first, explicit flag to execute.

Security issues: do not open a public issue; see `SECURITY.md`.

## Docker tests

`make e2e` installs gh-ost into `./bin`, then for MySQL 8.4 and 9.7 starts the lab, runs the integration
tests (including SIGKILL-mid-copy-then-resume for gh-ost and pt-osc) and stops the lab. pt-online-schema-change
must be installed (`brew install percona-toolkit` / `apt install percona-toolkit`). Needs Docker; takes minutes.
