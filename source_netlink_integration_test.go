//go:build linux

package main

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
)

// Integration tests against the real kernel. Each test creates its own
// network namespace with a veth pair, so the host's interfaces and neighbor
// tables are never touched. They need root (CAP_SYS_ADMIN for the namespace,
// CAP_NET_ADMIN inside it) and run only when IPNEIGH_NETLINK_INTEGRATION=1, as
// `make test-integration` and CI do; with the variable set, a missing
// privilege fails the test instead of skipping it.

const netlinkIntegrationEnv = "IPNEIGH_NETLINK_INTEGRATION"

type testNetns struct {
	ns netns.NsHandle
	h  *netlink.Handle // bound to ns, usable from any goroutine
}

func newTestNetns(t *testing.T) *testNetns {
	t.Helper()
	if os.Getenv(netlinkIntegrationEnv) != "1" {
		t.Skipf("set %s=1 and run as root (make test-integration)", netlinkIntegrationEnv)
	}
	n := &testNetns{}
	// netns.New moves the calling thread into the new namespace; doing it on
	// a locked thread that then exits keeps every other thread where it was.
	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		ns, err := netns.New()
		n.ns = ns
		errCh <- err
	}()
	if err := <-errCh; err != nil {
		t.Fatalf("create network namespace: %v", err)
	}
	t.Cleanup(func() { n.ns.Close() })
	h, err := netlink.NewHandleAt(n.ns)
	if err != nil {
		t.Fatalf("netlink handle in namespace: %v", err)
	}
	t.Cleanup(h.Close)
	n.h = h
	return n
}

// run calls fn on a thread inside the namespace. NetlinkSource uses the
// package-level netlink functions, which open their sockets in the namespace
// of the calling thread.
func (n *testNetns) run(fn func()) error {
	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		if err := netns.Set(n.ns); err != nil {
			errCh <- err
			return
		}
		fn()
		errCh <- nil
	}()
	return <-errCh
}

// addVeth creates a veth pair, brings both ends up and returns the first.
func (n *testNetns) addVeth(t *testing.T, name string) netlink.Link {
	t.Helper()
	if err := n.h.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: "peer0"}); err != nil {
		t.Fatalf("add veth %q: %v", name, err)
	}
	for _, ifname := range []string{name, "peer0"} {
		link, err := n.h.LinkByName(ifname)
		if err != nil {
			t.Fatalf("find %q: %v", ifname, err)
		}
		if err := n.h.LinkSetUp(link); err != nil {
			t.Fatalf("set %q up: %v", ifname, err)
		}
	}
	link, err := n.h.LinkByName(name)
	if err != nil {
		t.Fatalf("find %q: %v", name, err)
	}
	return link
}

func (n *testNetns) trySetNeigh(link netlink.Link, addr, hw string) error {
	family := netlink.FAMILY_V4
	if net.ParseIP(addr).To4() == nil {
		family = netlink.FAMILY_V6
	}
	return n.h.NeighSet(&netlink.Neigh{
		LinkIndex: link.Attrs().Index, Family: family, State: netlink.NUD_REACHABLE,
		IP: net.ParseIP(addr), HardwareAddr: mac(hw),
	})
}

func (n *testNetns) setNeigh(t *testing.T, link netlink.Link, addr, hw string) {
	t.Helper()
	if err := n.trySetNeigh(link, addr, hw); err != nil {
		t.Fatalf("set neighbor %s lladdr %s: %v", addr, hw, err)
	}
}

// netnsSource subscribes and lists inside a test namespace.
type netnsSource struct {
	n   *testNetns
	src *NetlinkSource
}

func (s *netnsSource) Subscribe(ctx context.Context) (ch <-chan NeighborEvent, err error) {
	if rerr := s.n.run(func() { ch, err = s.src.Subscribe(ctx) }); rerr != nil {
		return nil, rerr
	}
	return ch, err
}

func (s *netnsSource) List(ctx context.Context) (events []NeighborEvent, err error) {
	if rerr := s.n.run(func() { events, err = s.src.List(ctx) }); rerr != nil {
		return nil, rerr
	}
	return events, err
}

// handleResolver resolves link names inside a test namespace.
type handleResolver struct{ h *netlink.Handle }

func (r handleResolver) LinkName(index int) (string, error) {
	link, err := r.h.LinkByIndex(index)
	if err != nil {
		return "", err
	}
	return link.Attrs().Name, nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The whole pipeline (rtnetlink subscription with ListExisting, conversion,
// store, periodic List, collector) against kernel neighbor tables, including
// an interface name that is not valid UTF-8, which Linux accepts.
func TestNetlinkIntegration_WatcherTracksKernelNeighbors(t *testing.T) {
	for _, linkName := range []string{"veth-it", "v\xff\xfe"} {
		t.Run(linkName, func(t *testing.T) {
			n := newTestNetns(t)
			link := n.addVeth(t, linkName)
			n.setNeigh(t, link, "192.0.2.10", "02:00:00:00:00:01") // before Subscribe: bootstrap

			store := NewNeighborStore(StoreConfig{
				SyncInterval: 100 * time.Millisecond,
				DeleteGrace:  30 * time.Second,
				FlapRate:     rate.Inf,
			}, handleResolver{n.h}, testLogger())
			source := &netnsSource{n: n, src: NewNetlinkSource(testLogger(), store.RecordError)}
			w := NewWatcher(source, store, testLogger())

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			errCh := make(chan error, 1)
			go func() { errCh <- w.Run(ctx) }()

			v4 := NeighborKey{Dev: link.Attrs().Index, IP: ip("192.0.2.10"), Family: syscall.AF_INET}
			macOf := func(key NeighborKey) string {
				if e := store.Snapshot()[key]; e != nil && !e.Deleted {
					return e.MAC.String()
				}
				return ""
			}
			flaps := func() float64 {
				return counterValue(store.flapCounter, labelValue(linkName), "", "192.0.2.10", "ipv4")
			}

			waitFor(t, "the existing neighbor to be listed", func() bool { return macOf(v4) == "02:00:00:00:00:01" })
			if !w.Ready() {
				t.Fatal("watcher not ready after the first event")
			}

			n.setNeigh(t, link, "192.0.2.10", "02:00:00:00:00:02")
			waitFor(t, "the first flap", func() bool { return flaps() == 1 })

			if err := n.h.NeighDel(&netlink.Neigh{LinkIndex: link.Attrs().Index, Family: netlink.FAMILY_V4, IP: net.ParseIP("192.0.2.10")}); err != nil {
				t.Fatalf("delete neighbor: %v", err)
			}
			waitFor(t, "the delete", func() bool { e := store.Snapshot()[v4]; return e != nil && e.Deleted })
			n.setNeigh(t, link, "192.0.2.10", "02:00:00:00:00:03") // within the grace period
			waitFor(t, "the flap of the re-add", func() bool { return flaps() == 2 && macOf(v4) == "02:00:00:00:00:03" })

			n.setNeigh(t, link, "192.0.2.11", "02:00:00:00:00:04")
			other := NeighborKey{Dev: link.Attrs().Index, IP: ip("192.0.2.11"), Family: syscall.AF_INET}
			waitFor(t, "a second neighbor", func() bool { return macOf(other) == "02:00:00:00:00:04" })
			waitFor(t, "a periodic sync", func() bool { return counterValue(store.eventsTotal, "sync") > 0 })

			listed, err := source.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			found := map[NeighborKey]string{}
			for _, ev := range listed {
				found[NeighborKey{Dev: ev.LinkIndex, VRF: ev.MasterIndex, IP: ev.IP, Family: ev.Family}] = ev.HardwareAddr.String()
			}
			if found[v4] != "02:00:00:00:00:03" || found[other] != "02:00:00:00:00:04" {
				t.Fatalf("List = %v, want %v and %v", listed, v4, other)
			}

			for _, stage := range []string{"netlink_receive", "netlink_parse", "netlink_list", "link_resolve"} {
				if got := counterValue(store.errorsTotal, stage); got != 0 {
					t.Errorf("errors_total{stage=%q} = %v, want 0", stage, got)
				}
			}
			var exposed float64
			for _, mf := range collectAll(t, NewNeighborCollector(store)) {
				if mf.GetName() == "ipneigh_mac_change_total" {
					for _, m := range mf.GetMetric() {
						exposed += m.GetCounter().GetValue()
					}
				}
			}
			if exposed != 2 {
				t.Fatalf("scrape exposes %v flaps, want 2", exposed)
			}

			cancel()
			select {
			case err := <-errCh:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Run: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not return after cancellation")
			}
		})
	}
}

// IPv6 (NDP) neighbors arrive with their own family, both from the
// subscription and from List.
func TestNetlinkIntegration_IPv6Neighbors(t *testing.T) {
	n := newTestNetns(t)
	link := n.addVeth(t, "veth-it6")
	source := NewNetlinkSource(testLogger(), func(stage string) { t.Errorf("recorded error stage %q", stage) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var ch <-chan NeighborEvent
	var err error
	if rerr := n.run(func() { ch, err = source.Subscribe(ctx) }); rerr != nil || err != nil {
		t.Fatalf("Subscribe: %v %v", rerr, err)
	}
	if err := n.trySetNeigh(link, "2001:db8::10", "02:00:00:00:00:06"); errors.Is(err, unix.EAFNOSUPPORT) {
		t.Skip("the kernel has no IPv6 support")
	} else if err != nil {
		t.Fatalf("set IPv6 neighbor: %v", err)
	}

	want := NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: link.Attrs().Index, Family: syscall.AF_INET6,
		IP: ip("2001:db8::10"), HardwareAddr: mac("02:00:00:00:00:06"), State: NUD_REACHABLE,
	}
	timeout := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("subscription ended before the IPv6 neighbor event")
			}
			// Other IPv6 neighbors (such as solicited-node or router entries)
			// may show up on the new link; only the one set here matters.
			got = ev.IP == want.IP
			if got && (ev.Type != want.Type || ev.LinkIndex != want.LinkIndex || ev.Family != want.Family ||
				ev.HardwareAddr.String() != want.HardwareAddr.String() || ev.State&NUD_REACHABLE == 0) {
				t.Fatalf("event %+v, want %+v", ev, want)
			}
		case <-timeout:
			t.Fatal("timeout waiting for the IPv6 neighbor event")
		}
	}

	var listed []NeighborEvent
	if rerr := n.run(func() { listed, err = source.List(ctx) }); rerr != nil || err != nil {
		t.Fatalf("List: %v %v", rerr, err)
	}
	for _, ev := range listed {
		if ev.IP == want.IP && ev.Family == syscall.AF_INET6 && ev.HardwareAddr.String() == want.HardwareAddr.String() {
			return
		}
	}
	t.Fatalf("List = %+v, want an entry for %v", listed, want.IP)
}

// When neighbor updates overflow the subscription socket, the kernel reports
// ENOBUFS and the netlink library ends the subscription: the source records a
// netlink_receive error and closes its channel, so Watcher.Run returns and the
// exporter exits for its supervisor to restart it (README "Failure Modes").
func TestNetlinkIntegration_OverflowEndsSubscription(t *testing.T) {
	n := newTestNetns(t)
	link := n.addVeth(t, "veth-ov")
	var (
		mu     sync.Mutex
		stages []string
	)
	source := NewNetlinkSource(testLogger(), func(stage string) {
		mu.Lock()
		defer mu.Unlock()
		stages = append(stages, stage)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var ch <-chan NeighborEvent
	var err error
	if rerr := n.run(func() { ch, err = source.Subscribe(ctx) }); rerr != nil || err != nil {
		t.Fatalf("Subscribe: %v %v", rerr, err)
	}
	// Nobody reads while the MAC changes 20000 times: far more than the two
	// 256-event channels and the socket buffer hold.
	for i := range 20000 {
		n.setNeigh(t, link, "192.0.2.10", net.HardwareAddr{2, 0, 0, 0, byte(i >> 8), byte(i)}.String())
	}

	timeout := time.After(10 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-ch:
		case <-timeout:
			t.Fatal("the subscription survived the overflow")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(stages, "netlink_receive") {
		t.Fatalf("recorded stages %v, want netlink_receive", stages)
	}
}
