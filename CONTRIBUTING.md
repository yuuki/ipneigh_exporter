# Contributing

Thank you for helping. Bug reports and pull requests are welcome on GitHub;
for security issues see [SECURITY.md](SECURITY.md) instead.

- Open an issue first for a larger change or a change to the metrics, labels
  or flags, which users' alerts and dashboards depend on.
- Keep pull requests focused, with tests for behaviour changes, and update
  README.md when user-visible behaviour changes.
- Run `make check` before pushing; CI runs the same checks. Changes to the
  netlink code should also pass `make test-integration` (root), and the Lima
  end-to-end suite runs in CI (`make e2e` locally).
- Commits, branches and pull request titles follow the Git conventions in
  [AGENTS.md](AGENTS.md).

[AGENTS.md](AGENTS.md) lists the contracts that must not change silently and
the checks each kind of change needs.
