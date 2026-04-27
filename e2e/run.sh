#!/bin/bash
set -eu -o pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VM_NAME="ipneigh-e2e"
LISTEN_PORT=9144
EXPORTER_PATH="/tmp/ipneigh_exporter"
HOST_BINARY="$(mktemp -t ipneigh_exporter.XXXXXX)"
PASSED=0
FAILED=0

trap 'rm -f "$HOST_BINARY"' EXIT

cleanup() {
    echo "--- cleanup ---"
    lima_shell sudo pkill -f ipneigh_exporter 2>/dev/null || true
    lima_shell sudo ip netns del e2e-ns 2>/dev/null || true
}

lima_shell() {
    limactl shell --workdir /tmp "$VM_NAME" "$@"
}

assert_match() {
    local desc="$1" pattern="$2" body="$3"
    if echo "$body" | grep -qE "$pattern"; then
        echo "  PASS: $desc"
        PASSED=$((PASSED + 1))
    else
        echo "  FAIL: $desc (pattern: $pattern)"
        echo "  body: $body"
        FAILED=$((FAILED + 1))
    fi
}

assert_no_match() {
    local desc="$1" pattern="$2" body="$3"
    if echo "$body" | grep -qE "$pattern"; then
        echo "  FAIL: $desc (unexpected match: $pattern)"
        echo "  body: $body"
        FAILED=$((FAILED + 1))
    else
        echo "  PASS: $desc"
        PASSED=$((PASSED + 1))
    fi
}

# --- Build ---
echo "=== Building for Linux ==="
GOARCH=$(lima_shell uname -m 2>/dev/null | sed 's/aarch64/arm64/;s/x86_64/amd64/')
(cd "$PROJECT_DIR" && GOOS=linux GOARCH="$GOARCH" CGO_ENABLED=0 go build -o "$HOST_BINARY" .)
limactl copy "$HOST_BINARY" "$VM_NAME:$EXPORTER_PATH"
lima_shell chmod +x "$EXPORTER_PATH"
echo "Built binary for linux/$GOARCH"

# --- Setup ---
echo "=== Setting up test environment ==="
cleanup

lima_shell bash -c "
set -eux
sudo ip netns add e2e-ns
sudo ip link add veth-host type veth peer name veth-ns
sudo ip link set veth-ns netns e2e-ns
sudo ip addr add 10.200.0.1/24 dev veth-host
sudo ip link set veth-host up
sudo ip netns exec e2e-ns ip addr add 10.200.0.2/24 dev veth-ns
sudo ip netns exec e2e-ns ip link set veth-ns up
sudo ip netns exec e2e-ns ip link set lo up
"

# --- Start exporter ---
echo "=== Starting exporter ==="
lima_shell bash -c "
sudo $EXPORTER_PATH \
    --web.listen-address=:$LISTEN_PORT \
    --neighbor.device-include='^veth-host$' \
    --neighbor.sync-interval=5m \
    --neighbor.delete-grace=5s \
    --log.level=debug &
" &
sleep 2

# --- Test 1: healthz/readyz ---
echo "=== Test: Health endpoints ==="
HEALTH=$(lima_shell curl -s -o /dev/null -w '%{http_code}' "http://localhost:$LISTEN_PORT/healthz")
assert_match "healthz returns 200" "^200$" "$HEALTH"

# --- Test 2: Initial metrics (no flap yet) ---
echo "=== Test: Initial metrics ==="
METRICS=$(lima_shell curl -s "http://localhost:$LISTEN_PORT/metrics")
assert_no_match "no flap counter initially" 'linux_neighbor_mac_change_total' "$METRICS"
assert_match "exporter events counter exists" 'linux_neighbor_exporter_events_total' "$METRICS"

# --- Test 3: Create ARP entry ---
echo "=== Test: Create ARP entry ==="
lima_shell sudo ip neigh replace 10.200.0.2 lladdr aa:bb:cc:dd:ee:01 dev veth-host nud reachable
sleep 1
METRICS=$(lima_shell curl -s "http://localhost:$LISTEN_PORT/metrics")
assert_match "neighbor entry exists" 'linux_neighbor_entries\{.*dev="veth-host".*state="reachable".*\} [0-9]' "$METRICS"
assert_no_match "no flap on first entry" 'linux_neighbor_mac_change_total' "$METRICS"

# --- Test 4: MAC flap ---
echo "=== Test: MAC flap detection ==="
lima_shell sudo ip neigh replace 10.200.0.2 lladdr aa:bb:cc:dd:ee:02 dev veth-host nud reachable
sleep 1
METRICS=$(lima_shell curl -s "http://localhost:$LISTEN_PORT/metrics")
assert_match "flap counter = 1" 'linux_neighbor_mac_change_total\{.*dev="veth-host".*ip="10.200.0.2".*\} 1' "$METRICS"
assert_match "flap event counted" 'linux_neighbor_exporter_events_total\{type="flap"\} 1' "$METRICS"
assert_match "last flap timestamp exists" 'linux_neighbor_last_flap_unix_seconds\{.*ip="10.200.0.2".*\}' "$METRICS"

# --- Test 5: Second flap ---
echo "=== Test: Second flap ==="
lima_shell sudo ip neigh replace 10.200.0.2 lladdr aa:bb:cc:dd:ee:03 dev veth-host nud reachable
sleep 1
METRICS=$(lima_shell curl -s "http://localhost:$LISTEN_PORT/metrics")
assert_match "flap counter = 2" 'linux_neighbor_mac_change_total\{.*dev="veth-host".*ip="10.200.0.2".*\} 2' "$METRICS"

# --- Test 6: State change without MAC change (no flap) ---
echo "=== Test: State change only ==="
lima_shell sudo ip neigh replace 10.200.0.2 lladdr aa:bb:cc:dd:ee:03 dev veth-host nud stale
sleep 1
METRICS=$(lima_shell curl -s "http://localhost:$LISTEN_PORT/metrics")
assert_match "flap counter still 2" 'linux_neighbor_mac_change_total\{.*dev="veth-host".*ip="10.200.0.2".*\} 2' "$METRICS"
assert_match "state updated to stale" 'linux_neighbor_entries\{.*dev="veth-host".*state="stale".*\}' "$METRICS"

# --- Test 7: Delete and re-add with same MAC (no flap within grace) ---
echo "=== Test: Delete + re-add same MAC ==="
lima_shell sudo ip neigh del 10.200.0.2 dev veth-host
sleep 1
lima_shell sudo ip neigh replace 10.200.0.2 lladdr aa:bb:cc:dd:ee:03 dev veth-host nud reachable
sleep 1
METRICS=$(lima_shell curl -s "http://localhost:$LISTEN_PORT/metrics")
assert_match "flap counter still 2 after same MAC re-add" 'linux_neighbor_mac_change_total\{.*dev="veth-host".*ip="10.200.0.2".*\} 2' "$METRICS"

# --- Test 8: Delete and re-add with different MAC (flap within grace) ---
echo "=== Test: Delete + re-add different MAC ==="
lima_shell sudo ip neigh del 10.200.0.2 dev veth-host
sleep 1
lima_shell sudo ip neigh replace 10.200.0.2 lladdr aa:bb:cc:dd:ee:04 dev veth-host nud reachable
sleep 1
METRICS=$(lima_shell curl -s "http://localhost:$LISTEN_PORT/metrics")
assert_match "flap counter = 3 after different MAC re-add" 'linux_neighbor_mac_change_total\{.*dev="veth-host".*ip="10.200.0.2".*\} 3' "$METRICS"

# --- Test 9: Multiple IPs tracked independently ---
echo "=== Test: Multiple IPs ==="
lima_shell sudo ip neigh replace 10.200.0.3 lladdr bb:cc:dd:ee:ff:01 dev veth-host nud reachable
sleep 1
METRICS=$(lima_shell curl -s "http://localhost:$LISTEN_PORT/metrics")
assert_no_match "no flap for new IP" 'linux_neighbor_mac_change_total\{.*ip="10.200.0.3"' "$METRICS"
assert_match "entries count increased" 'linux_neighbor_entries\{.*dev="veth-host".*state="reachable".*\} [0-9]' "$METRICS"

# --- Test 10: readyz returns 200 after events ---
echo "=== Test: readyz after events ==="
READY=$(lima_shell curl -s -o /dev/null -w '%{http_code}' "http://localhost:$LISTEN_PORT/readyz")
assert_match "readyz returns 200" "^200$" "$READY"

# --- Cleanup ---
cleanup

# --- Summary ---
echo ""
echo "=============================="
echo "  Results: $PASSED passed, $FAILED failed"
echo "=============================="

if [ "$FAILED" -gt 0 ]; then
    exit 1
fi
