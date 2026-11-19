# Security

Report vulnerabilities privately through GitHub's "Report a vulnerability" button on the repository's
Security tab, not in a public issue. Include the zdm version, the MySQL version and a minimal
reproduction. Expect an acknowledgement within a week.

## What zdm touches

- It connects to your MySQL servers with the credentials you give it. Preflight, `check`, `verify` and
  `backfill` dry runs use read-only sessions; everything that writes needs an explicit flag.
- Passwords reach gh-ost and pt-online-schema-change through a mode-0600 temp file, never the command
  line, and are not logged. DSNs are not printed.
- `--set` and `--where` for `backfill` are SQL fragments you write. They are rejected if they contain `;`
  or comment markers, but they run with the connection's privileges: do not build them from untrusted input.

Run zdm with a dedicated MySQL user that has only the privileges it needs.
