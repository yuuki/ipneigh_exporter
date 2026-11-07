# AGENTS.md

Guide for coding agents (and humans) working in this repository. README.md is
the user-facing documentation; this file covers how the code is built, tested
and changed.

## 1. Overview and architecture

ipneigh_exporter is a Prometheus exporter that watches the Linux kernel
neighbour table (ARP/NDP) through rtnetlink and detects MAC address flaps. A
background goroutine is subscribed to neighbour events; state is held in an
in-memory map, and the gauges are computed from a snapshot at scrape time.

It runs on Linux with `CAP_NET_ADMIN` (rtnetlink neighbour subscription); the
shipped systemd unit grants only that capability to a dynamic user.

Data flow: `NetlinkSource` → channel → `Watcher` → `NeighborStore.HandleEvent()`
→ `NeighborCollector.Collect()` (on scrape).

- `NetlinkSource` (`source_netlink.go`): subscribes to `RTM_NEWNEIGH` /
  `RTM_DELNEIGH` with `ListExisting: true` (the current table is loaded at
  start), converts library updates with `convertNeigh`, skips non-IP families,
  counts unparsable updates as `netlink_parse`, and lists the table for resyncs.
- `Watcher` (`watcher.go`): applies events to the store, owns the periodic
  resync (`--neighbor.sync-interval`: List, refresh, then GC), and reports
  readiness (`/readyz` is 200 after the first event or successful sync).
- `NeighborStore` (`store.go`): the state map keyed by `(Dev, VRF, IP,
  Family)`, flap detection, per-key rate limiting, GC, and the counters.
- `NeighborCollector` (`collector.go`): gauges from a `Snapshot()`; forwards the
  store's counters.
- `main.go`: flags, logger (`log/slog`, logfmt or JSON), HTTP server
  (`/metrics`, `/healthz`, `/readyz`, `/`).

Flap detection rules:

- Key: `(Dev, VRF, IP, Family)`: link index, master index, address, family.
- Flap: old MAC and new MAC both non-empty and different.
- Not a flap: INCOMPLETE→REACHABLE, a state-only change, a re-add with the same
  MAC after a delete.
- A delete keeps the MAC for `--neighbor.delete-grace`; a re-add at or after
  the end of the grace period starts without a MAC, so it is not a flap.
- Empty-MAC events (e.g. INCOMPLETE) never overwrite a stored MAC; this
  prevents false negatives after kernel auto-resolution.
- A per-key `golang.org/x/time/rate` limiter (`--neighbor.flap-burst`, 1/s)
  caps counter increments during flap storms; suppressed flaps count as
  `flap_rate_limited`. GC of a key also drops its limiter.
- GC keeps deleted entries within the grace period and purges entries not seen
  for longer than the sync interval. A failed or partial sync purges nothing.

## 2. Directory layout

Flat `package main` at the repository root.

| Path | Contents |
|------|----------|
| `main.go` | Entry point: flags, logging, HTTP server |
| `source.go`, `source_common.go` | `NeighborEvent`, `NeighborSource` interface, shared error handling |
| `source_netlink.go`, `nud_linux.go` | Linux: real netlink source and resolver, `unix` constants |
| `source_stub.go`, `nud_other.go` | Non-Linux stubs so that the package compiles and unit tests run (e.g. on macOS) |
| `store.go`, `watcher.go`, `collector.go` | State, event loop, Prometheus collector |
| `*_test.go` | Unit, fuzz, integration (`source_netlink_integration_test.go`) tests and benchmarks (`bench_test.go`) |
| `testdata/fuzz/` | Fuzz regression inputs (commit new crashers here) |
| `e2e/` | Lima (Rocky Linux 9 cloud image) end-to-end suite: `lima.yaml`, `run.sh` |
| `packaging/` | systemd unit and `/etc/default` file shipped in release archives |
| `dashboards/` | Grafana dashboard template (checked by `dashboard_test.go`) |
| `docs/images/` | README figures |
| `.github/` | Workflows, Dependabot, PR template |

## 3. Build

The binary is Linux-only (Go version pinned in `go.mod`).

```bash
make build    # linux/amd64 binary ./ipneigh_exporter with version ldflags
make clean
```

Releases are built by GoReleaser (`.goreleaser.yml`, `Dockerfile.goreleaser`)
from `v*` tags in `.github/workflows/release.yml`. `Dockerfile` is a local,
self-contained image build.

## 4. Test

| Command | What | Needs |
|---------|------|-------|
| `make test` | Unit tests with `-race` (also runs the fuzz seed corpora) | nothing |
| `make test-race` | Alias of `make test` | nothing |
| `make test-fuzz` | Every fuzz target for `FUZZTIME` (default 30s); add `FUZZFLAGS=-parallel=2` on a shared machine | nothing |
| `make test-integration` | `TestNetlinkIntegration*` against the real kernel, each in its own network namespace with a veth pair | root (runs the test binary with `sudo` unless already root); IPv6 test skips on kernels without IPv6 |
| `make e2e` / `make e2e-teardown` | Lima VM (Rocky Linux 9) with a real exporter, veth pair and `ip neigh` changes (`e2e/run.sh`) | Lima, KVM; not run by required CI |
| `make bench` | Benchmarks to `bench.txt` (`BENCH_COUNT`, default 6); compare with `benchstat` | nothing |

Single test: `go test ./... -run TestHandleEvent_DifferentMAC_Flap -v`.

Fuzz targets: `FuzzStoreModel` (random event/sync/GC sequences against a
reference model of the rules in section 1), `FuzzCollectorLabelValues`
(arbitrary interface names, IPs and MACs through store and scrape),
`FuzzConvertNeigh` (Linux; netlink conversion contract).

Testing patterns:

- `NeighborSource` and `LinkResolver` are interfaces; mocks live in
  `source_mock_test.go`. The store's `now` field is injectable.
- Flap limiters use the wall clock; deterministic tests use `rate.Inf` or a
  zero rate with a fixed burst.
- `goleak.VerifyTestMain` runs for the package: every goroutine a test starts
  (watcher, periodic sync, forwarder, netlink reader) must end.
- `e2e_script_test.go` and `dashboard_test.go` assert on `e2e/run.sh` and the
  dashboard JSON, so edits to those files go through these tests.

## 5. Static analysis

```bash
make lint          # golangci-lint (.golangci.yml) + go mod tidy -diff
make fmt           # golangci-lint fmt (gofmt)
make vet           # go vet for Linux and for GOOS=darwin (the stubs)
make lint-actions  # actionlint + zizmor on .github/ (offline unless GH_TOKEN is set)
make vuln          # govulncheck
make complexity    # cccc ranking (needs cccc; CI tracks scores with octocov)
```

Tool versions are pinned at the top of the `Makefile` and in the workflows;
bump them together.

## 6. Coding conventions

- Logging: `log/slog` (`newLogger` in `main.go`); per-event detail at Debug.
- Errors: wrap with `%w`; count failures by stage with `RecordError` (the
  `stage` label values are listed in section 7).
- Comments and docs in English; comments say why.
- Commits: short scoped subjects, `fix:`, `feat:`, `ci:`, `chore:`, `docs:`,
  `test:`.

## 7. Compatibility contracts

Changing any of these is a user-visible change; call it out in the PR and
update README.md.

| Metric | Type | Labels | Owner |
|--------|------|--------|-------|
| `ipneigh_mac_change_total` | Counter | `dev`, `vrf`, `ip`, `family` | Store (event time) |
| `ipneigh_entries` | Gauge | `dev`, `vrf`, `family`, `state` | Collector (scrape time) |
| `ipneigh_last_flap_unix_seconds` | Gauge | `dev`, `vrf`, `ip`, `family` | Collector (scrape time) |
| `ipneigh_events_total` | Counter | `type`: `neigh_new`, `neigh_del`, `sync`, `flap`, `flap_rate_limited`, `gc_purge` | Store (event time) |
| `ipneigh_errors_total` | Counter | `stage`: `netlink_receive`, `netlink_parse`, `netlink_list`, `link_resolve` | Store (event time) |

- `state` values: `reachable`, `stale`, `delay`, `probe`, `failed`,
  `incomplete`, `permanent`, `noarp` (`unknown` otherwise); `family`: `ipv4`,
  `ipv6`. Interface and VRF names go through `labelValue()`: bytes that are
  not valid UTF-8 (Linux allows them in interface names) become U+FFFD.
- CLI flags (`main.go`, README table): `--web.listen-address`,
  `--web.metrics-path`, `--neighbor.device-include`, `--neighbor.device-exclude`,
  `--neighbor.sync-interval`, `--neighbor.delete-grace`, `--neighbor.flap-burst`,
  `--log.level`, `--log.format`, `--version`.
- HTTP endpoints: `/metrics` (configurable), `/healthz`, `/readyz`, `/`.
- Packaging: `packaging/ipneigh_exporter.service` (`DynamicUser=yes`,
  `AmbientCapabilities=CAP_NET_ADMIN`) and `packaging/ipneigh_exporter.default`,
  shipped in the release archives; image `ghcr.io/yuuki/ipneigh_exporter`.
- `dashboards/ipneigh-exporter.json` queries the metrics above.

## 8. Concurrency and resource management

- Counters are incremented at event time inside `NeighborStore`, never at
  scrape time; the collector forwards them via `CollectCounters()`.
- Gauges are computed at scrape time from `Snapshot()` (a deep copy taken under
  `RLock`); a scrape does no netlink I/O.
- `resolveLink()` uses its own `linkMu`, separate from `mu`, so the store lock
  is never held during netlink syscalls. Resolve names before taking `mu`.
- `NetlinkSource.forward` owns the subscription: it closes `done` (ending the
  netlink subscription) before closing its output channel, and stops when ctx
  ends, including while blocked on a send.
- `Watcher.Run` starts the periodic sync goroutine and cancels it when it
  returns (it does not wait for it to finish). If the event channel closes
  (e.g. the netlink socket overflowed with ENOBUFS), Run returns
  `errNeighborEventChannelClosed` and `main` exits with status 1 for the
  supervisor to restart it.
- `NetlinkSource.List` checks ctx only between address families; netlink dumps
  are not cancellable.

## 9. Performance constraints

- `HandleEvent` runs for every kernel neighbour update and must stay cheap:
  no allocation on the refresh path (`BenchmarkHandleEvent_Refresh`), link
  names resolved outside `mu`, and sync resolves each link once per pass.
- Cardinality: `ip` appears only on flap series (`mac_change_total`,
  `last_flap_unix_seconds`), never on `ipneigh_entries`; the rate limiter
  bounds counter growth during storms; GC bounds the map.
- The netlink channels are buffered (256); a slow consumer leads to socket
  overflow and a restart (section 8).

## 10. Before opening a PR

- `make check` (lint, vet, lint-actions, race tests, build) must pass.
- Changes to `source_netlink.go` or netlink handling: `make test-integration`
  (root).
- Changes to parsing, flap rules or label handling: `make test-fuzz
  FUZZTIME=30s`; commit any new failing input under `testdata/fuzz/`.
- Changes to the e2e script, systemd unit or end-to-end behaviour: `make e2e`
  where Lima is available (CI runs it as a separate workflow).
- Keep README.md and this file in sync with behaviour changes.

## 11. Rules for AI agents

Do not:

- disable, skip, delete or weaken tests or lint rules to make checks pass, or
  hide failures (`|| true`, `continue-on-error`, blanket excludes);
- edit generated or tool-managed files by hand, or vendor dependencies;
- add dependencies without a stated justification (test-only ones included);
- change the public contracts of section 7 without calling it out;
- loosen workflow permissions, unpin actions, or add caches to release jobs;
- commit secrets, rewrite history, force-push, tag or publish releases.

## 12. Changes that need human review

Even with green CI: workflow, release and packaging files (`.github/`,
`.goreleaser.yml`, Dockerfiles, `packaging/`), permissions and secrets, the
netlink source and conversion code (privileged input path), label handling,
the public contracts of section 7, dependency additions, and large refactors.
