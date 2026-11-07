# AGENTS.md

This file holds only what the code does not show. User-visible behaviour,
including the flap detection rules, is specified in [README.md](README.md).

## Rules the code does not show

- The flap detection rules (README "Flap Detection Rules") are pinned by the
  reference model of `FuzzStoreModel` (`store_model_test.go`): change a rule,
  the model and README together.
- An event without a MAC (e.g. INCOMPLETE) never overwrites a stored MAC:
  otherwise a flap across a kernel re-resolution goes undetected.
- Counters are incremented at event time in `NeighborStore`, never at scrape
  time; gauges are computed at scrape time from `Snapshot()`, and a scrape
  does no netlink I/O.
- Resolve link names under `linkMu`, before taking `mu`: the store lock is
  never held during a netlink syscall.
- `HandleEvent` runs for every kernel neighbour update: keep its refresh path
  allocation-free (`BenchmarkHandleEvent_Refresh`).
- Cardinality: `ip` appears only on flap series, never on `ipneigh_entries`.
  Interface and VRF names become labels only through `labelValue()` (Linux
  allows bytes that are not valid UTF-8 in them).
- English for code, comments and docs.

## Compatibility contracts

A change is user-visible: call it out in the PR and update README.md.

- Metric names, types, labels and the closed `state`, `family`, `type` and
  `stage` value sets: `store.go`, `collector.go`; `dashboards/` queries them.
- CLI flags and HTTP endpoints: `main.go`.
- Packaging: `packaging/` (the unit grants only `CAP_NET_ADMIN` to a dynamic
  user) and the image `ghcr.io/yuuki/ipneigh_exporter` (`.goreleaser.yml`).

## Before opening a pull request

- `make check`.
- `source_netlink.go` or netlink handling: also `make test-integration` (root).
- Parsing, flap rules or label handling: also `make test-fuzz FUZZTIME=30s`;
  commit any new failing input under `testdata/fuzz/`.
- `e2e/run.sh`, the systemd unit or end-to-end behaviour: also `make e2e`
  where Lima is available.

## Git conventions

Branches: `<type>/<issue>-<summary>`, or `<type>/<summary>` without an issue
(`fix/166-connect-errors`, `feat/qp-counters`). `<type>` is a commit type
below; `<summary>` is 2–5 lowercase ASCII words joined by `-`. Branches named
by tools keep their names (`agent/*`, `claude/*`, `copilot/*`,
`dependabot/*`). Never push to the default branch.

Commits follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

    <type>(<scope>)!: <subject>

    <body>

    <footer>

- `type`: `feat` (new user-visible behaviour), `fix` (bug fix), `perf`,
  `refactor` (no behaviour change), `test`, `docs`, `build` (build system,
  dependencies, packaging), `ci` (workflows), `chore` (anything else, e.g.
  `chore(release): prepare v1.2.3`), `revert`.
- `scope`: optional; the package or area changed: `store`, `watcher`,
  `collector`, `netlink`, `e2e`, `packaging`, `dashboards`.
- `!` and a `BREAKING CHANGE: <what changed and how to migrate>` footer for
  any change to a compatibility contract above.
- `subject`: imperative, lower case (except identifiers), no final period,
  at most 72 characters including the prefix.
- `body`: why the change is needed and what it affects, wrapped at 72
  columns; required unless the subject says everything.
- `footer`: `Fixes #N` / `Refs #N`, `BREAKING CHANGE:`, trailers such as
  `Co-Authored-By:`.
- One logical change per commit; regenerated files go in the commit that
  changes their source. Pull request titles use the same format (they become
  the squash commit).

## Forbidden

- Disabling, skipping, deleting or weakening tests or lint rules to make
  checks pass, or hiding failures (`|| true`, `continue-on-error`, blanket
  excludes).
- Vendoring dependencies; adding dependencies, test-only ones included,
  without a stated justification.
- Changing a compatibility contract without calling it out.
- Loosening workflow permissions, unpinning actions, or adding caches to
  release jobs.
- Committing secrets, rewriting history, force-pushing, tagging or publishing
  releases.

## Needs human review

Even with green CI: `.github/`, `.goreleaser.yml`, Dockerfiles and
`packaging/`; permissions and secrets; the netlink source and conversion code
(privileged input path); label handling; the compatibility contracts;
dependency additions; large refactors.

## Environment notes

- With `GH_TOKEN` set but the GitHub API unreachable, run
  `env -u GH_TOKEN make check`.
