# Contributing

1. Read `docs/03-architecture.md` and `docs/05-decisions-and-open-questions.md` first.
2. `make check` must pass (vet, golangci-lint, tests).
3. Every lint rule needs a golden test: `testdata/<case>.sql` plus `<case>.expected.json`.
4. Claims about MySQL behavior must cite the manual for the exact version (8.4 or 9.7) or be tagged
   `[verify]` in the docs.
5. Nothing that mutates a database may default to executing: dry-run first, explicit flag to execute.

Security issues: do not open a public issue; see `SECURITY.md`.
