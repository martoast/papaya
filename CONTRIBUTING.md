# Contributing

Thanks for helping. Papaya is small on purpose: one binary, no database, the
tailnet as the only login. Changes that keep it that way are the easiest to
accept.

- **Open an issue first** for anything bigger than a bug fix, so we can agree on
  the shape before you spend time on it.
- **Tests come with the change.** `make test` runs the Go suite with the race
  detector against a real in-process SSH server; `make test-browser` runs the
  Playwright tests. A bug fix should include the test that would have caught it.
- **Security-sensitive code** (identity, allow lists, uploads, anything that
  reaches a remote shell) gets extra review. Read the "How authentication works"
  section of the README before touching it.
- **Never commit secrets**, even disposable ones. CI runs a secret scanner.
- Keep the README's configuration table in step with any setting you add.

Development setup is in the README under **Development**. Security reports go
through [SECURITY.md](SECURITY.md), not public issues.
