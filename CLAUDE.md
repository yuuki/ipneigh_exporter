# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
make build              # Cross-compile for linux/amd64 with version ldflags
make test               # go test ./... -race -count=1
make vet                # go vet ./...
make lint               # golangci-lint run

# Run a single test
go test ./... -run TestHandleEvent_DifferentMAC_Flap -v

# E2E tests (requires Lima; spins up Rocky Linux 9.7 VM)
make e2e                # Start VM + run e2e/run.sh
make e2e-teardown       # Stop and delete VM
```

Target binary is Linux-only. macOS builds use stub implementations (`source_stub.go`, `nud_other.go`) for compilation and unit tests, but the exporter only functions on Linux.

## Architecture

The exporter runs a background goroutine subscribed to rtnetlink neighbor events. State is held in an in-memory map, and Prometheus metrics are computed from a snapshot at scrape time.

**Data flow:** `NetlinkSource` → channel → `Watcher` → `NeighborStore.HandleEvent()` → `NeighborCollector.Collect()` (on scrape)

Key design constraints:
- Counters (`mac_change_total`, `events_total`, `errors_total`) are incremented at event time inside `NeighborStore`, not at scrape time. The collector forwards them via `CollectCounters()`.
- Gauges (`entries`, `last_flap_unix_seconds`) are computed at scrape time from a `Snapshot()` under RLock.
- `resolveLink()` uses its own `linkMu` mutex (separate from the main `mu`) to avoid holding the store lock during netlink syscalls.
- Empty-MAC events (e.g., INCOMPLETE state) must not overwrite stored MACs — this prevents false negative flap detection after kernel auto-resolution.

**Platform separation via build tags:**
- `source_netlink.go` + `nud_linux.go`: Linux-only (real netlink + unix constants)
- `source_stub.go` + `nud_other.go`: non-Linux stubs (allows `go test` on macOS)

**Testability interfaces:** `NeighborSource` (event subscription) and `LinkResolver` (link index → name) are interfaces with mock implementations in `source_mock_test.go`. The store's `now` field is injectable for time-dependent tests.

## Flap Detection Rules

- Key: `(Dev, VRF, IP, Family)` — link index, master index, address, address family
- Flap: old MAC and new MAC both non-empty and different
- Not a flap: INCOMPLETE→REACHABLE, state-only change, same-MAC re-add after delete (within grace period)
- After delete-grace expires, old MAC is forgotten (re-add with different MAC is not a flap)
- Per-key rate limiter (`golang.org/x/time/rate`) caps counter increments during flap storms

## Metrics

| Metric | Type | Owner |
|--------|------|-------|
| `linux_neighbor_mac_change_total` | Counter | Store (event-time) |
| `linux_neighbor_entries` | Gauge | Collector (scrape-time) |
| `linux_neighbor_last_flap_unix_seconds` | Gauge | Collector (scrape-time) |
| `linux_neighbor_exporter_events_total` | Counter | Store (event-time) |
| `linux_neighbor_exporter_errors_total` | Counter | Store (event-time) |

## Runtime Requirements

- Linux with `CAP_NET_ADMIN` (for rtnetlink neighbor subscription)
- systemd unit in `dist/` uses `DynamicUser=yes` + `AmbientCapabilities=CAP_NET_ADMIN`
