# AGENTS.md

## Rules
- README "Flap Detection Rules" is pinned by the reference model of `FuzzStoreModel` (`store_model_test.go`): change rule, model and README together.
- An event without a MAC (e.g. INCOMPLETE) never overwrites a stored MAC: otherwise a flap across a kernel re-resolution goes undetected.
- Counters are incremented at event time in `NeighborStore`, never at scrape time; gauges are computed at scrape time from `Snapshot()`.
- Resolve link names under `linkMu`, before taking `mu`: the store lock is never held during a netlink syscall.
- `HandleEvent` runs for every kernel neighbour update: keep its refresh path allocation-free (`BenchmarkHandleEvent_Refresh`).
- `ip` is a label only on the flap series, never on `ipneigh_entries`.

## Compatibility contracts
- Metric names, types, labels and their closed value sets (`store.go`, `collector.go`; `dashboards/` queries them); CLI flags and HTTP endpoints (`main.go`).
- Packaging: `packaging/` and the image `ghcr.io/yuuki/ipneigh_exporter` (`.goreleaser.yml`).

## Before opening a pull request
- `make check`; also `make test-integration` (root) for `source_netlink.go` or netlink handling.
- Parsing, flap rules or label handling: also `make test-fuzz FUZZTIME=30s`.
- `e2e/run.sh`, the systemd unit or end-to-end behaviour: also `make e2e` where Lima is available.

## Git conventions
- Branches: `<type>/<issue>-<summary>` (`<type>/<summary>` without an issue); tools' `agent/*`, `claude/*`, `copilot/*`, `dependabot/*` keep their names.
- Commits and PR titles: Conventional Commits 1.0.0, types `feat` `fix` `perf` `refactor` `test` `docs` `build` `ci` `chore` `revert`; scopes: `store`, `watcher`, `collector`, `netlink`, `e2e`, `packaging`, `dashboards`.
