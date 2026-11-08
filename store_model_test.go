package main

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"golang.org/x/time/rate"
)

// This file drives NeighborStore (and Watcher.syncOnce) with random event
// sequences and compares it after every step with a reference model written
// from the flap detection rules in README.md:
//
//   - a flap is a new MAC that differs from the remembered one, both non-empty;
//   - an event without a MAC (INCOMPLETE) never overwrites the remembered MAC;
//   - a delete keeps the MAC for DeleteGrace; a re-add at or after
//     DeletedAt+DeleteGrace starts from no MAC, so it is never a flap;
//   - each key has its own flap limiter, dropped when GC purges the key;
//   - GC keeps deleted entries within the grace period and purges entries not
//     seen for longer than SyncInterval;
//   - a sync that fails to list, or that cannot resolve a device, purges
//     nothing;
//   - a sync applies a dump taken when it started listing, at that time:
//     a key that an event updated while the dump was in flight is newer than
//     the dump, so the sync leaves it alone (no flap, no undoing a delete, no
//     refresh).
//
// Flap limiting is made deterministic with rate.Inf (no limit) or rate 0
// (a burst that is never refilled): the store's limiters use the wall clock.

const (
	modelSync = time.Minute
	// The grace period is either shorter than the sync interval (the default
	// setup) or longer, which is when GC has to keep deleted entries.
	modelShortGrace = 10 * time.Second
	modelLongGrace  = 2 * modelSync
)

var (
	// Link indexes: 1 and 2 resolve, 3 does not (a link_resolve error).
	modelDevs = []int{1, 2, 3}
	// VRF master indexes: 0 is none, 10 resolves, 11 does not (no error).
	modelVRFs  = []int{0, 10, 11}
	modelIPs   = []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("2001:db8::1")}
	modelMACs  = []net.HardwareAddr{nil, {2, 0, 0, 0, 0, 1}, {2, 0, 0, 0, 0, 2}, {2, 0, 0, 0, 0, 3}}
	modelNUDs  = []int{NUD_INCOMPLETE, NUD_REACHABLE, NUD_STALE, NUD_DELAY, NUD_PROBE, NUD_FAILED, NUD_NOARP, NUD_PERMANENT, 0}
	modelNames = map[int]string{1: "eth0", 2: "eth1", 10: "vrf-red"}
)

// Operations, one byte each followed by their arguments.
const (
	opNew      = iota
	opNewAgain // a second weight for new-neighbor events, the common case
	opDel
	opAdvance
	opSync
	opGC
	opCount
)

type modelEntry struct {
	mac       string // "" when no MAC is remembered
	state     int
	devName   string
	vrfName   string
	lastSeen  time.Time
	lastFlap  time.Time
	deleted   bool
	deletedAt time.Time
	flapCount uint64
}

type storeModel struct {
	grace   time.Duration
	limit   rate.Limit
	burst   int
	include *regexp.Regexp
	exclude *regexp.Regexp

	now     time.Time
	ready   bool
	entries map[NeighborKey]*modelEntry
	// While a sync's dump is in flight: when the sync started and the keys
	// an event updated since (nil otherwise).
	syncAt  time.Time
	touched map[NeighborKey]bool
	tokens  map[NeighborKey]int // flaps left per key under rate 0
	flaps   map[[4]string]float64
	events  map[string]float64
	errors  map[string]float64
}

func (m *storeModel) allowed(name string) bool {
	if m.include != nil && !m.include.MatchString(name) {
		return false
	}
	return m.exclude == nil || !m.exclude.MatchString(name)
}

func (m *storeModel) allowFlap(key NeighborKey) bool {
	if m.limit == rate.Inf {
		return true
	}
	left, ok := m.tokens[key]
	if !ok {
		left = m.burst
	}
	if left == 0 {
		m.tokens[key] = 0
		return false
	}
	m.tokens[key] = left - 1
	return true
}

func (m *storeModel) upsert(key NeighborKey, ev NeighborEvent, devName, vrfName string, now time.Time, countNew bool) {
	if countNew {
		m.events["neigh_new"]++
	}
	newMAC := string(ev.HardwareAddr)
	e, ok := m.entries[key]
	if !ok {
		m.entries[key] = &modelEntry{mac: newMAC, state: ev.State, devName: devName, vrfName: vrfName, lastSeen: now}
		return
	}
	oldMAC := e.mac
	if e.deleted && !now.Before(e.deletedAt.Add(m.grace)) {
		oldMAC, e.mac = "", ""
	}
	e.deleted = false
	if newMAC != "" {
		e.mac = newMAC
	}
	e.state, e.devName, e.vrfName, e.lastSeen = ev.State, devName, vrfName, now
	if oldMAC == "" || newMAC == "" || oldMAC == newMAC {
		return
	}
	if !m.allowFlap(key) {
		m.events["flap_rate_limited"]++
		return
	}
	e.flapCount++
	e.lastFlap = now
	m.flaps[[4]string{devName, vrfName, ev.IP.String(), familyString(ev.Family)}]++
	m.events["flap"]++
}

func (m *storeModel) handle(ev NeighborEvent) {
	devName := modelNames[ev.LinkIndex]
	if devName == "" {
		m.errors["link_resolve"]++
		return
	}
	if !m.allowed(devName) {
		return
	}
	key := NeighborKey{Dev: ev.LinkIndex, VRF: ev.MasterIndex, IP: ev.IP, Family: ev.Family}
	if ev.Type == RTM_DELNEIGH {
		m.events["neigh_del"]++
		if e, ok := m.entries[key]; ok {
			e.deleted, e.deletedAt, e.lastSeen, e.state = true, m.now, m.now, ev.State
			m.touch(key)
		}
		return
	}
	m.upsert(key, ev, devName, modelNames[ev.MasterIndex], m.now, true)
	m.touch(key)
}

// touch records that an event updated key while a sync's dump is in flight.
func (m *storeModel) touch(key NeighborKey) {
	if m.touched != nil {
		m.touched[key] = true
	}
}

// listing starts a sync: its dump is taken now, then events may update the
// store before sync applies it.
func (m *storeModel) listing() {
	m.syncAt, m.touched = m.now, map[NeighborKey]bool{}
}

func (m *storeModel) sync(list []NeighborEvent, listErr bool) {
	touched := m.touched
	m.touched = nil
	if listErr {
		m.errors["netlink_list"]++
		return
	}
	ok := true
	for _, ev := range list {
		devName := modelNames[ev.LinkIndex]
		if devName == "" {
			m.errors["link_resolve"]++
			ok = false
			continue
		}
		if !m.allowed(devName) {
			continue
		}
		key := NeighborKey{Dev: ev.LinkIndex, VRF: ev.MasterIndex, IP: ev.IP, Family: ev.Family}
		if touched[key] {
			continue // an event newer than the dump
		}
		m.upsert(key, ev, devName, modelNames[ev.MasterIndex], m.syncAt, false)
	}
	if ok {
		m.events["sync"]++
		m.gc(m.syncAt)
	}
	m.ready = true
}

func (m *storeModel) gc(now time.Time) {
	for key, e := range m.entries {
		if e.deleted && now.Sub(e.deletedAt) <= m.grace {
			continue
		}
		if now.Sub(e.lastSeen) > modelSync {
			delete(m.entries, key)
			delete(m.tokens, key)
			m.events["gc_purge"]++
		}
	}
}

// opReader hands out the fuzz input one byte at a time, then zeros.
type opReader struct{ data []byte }

func (r *opReader) next() int {
	if len(r.data) == 0 {
		return 0
	}
	b := r.data[0]
	r.data = r.data[1:]
	return int(b)
}

func (r *opReader) pick(n int) int { return r.next() % n }

func (r *opReader) event(typ uint16) NeighborEvent {
	k := r.next()
	addr := modelIPs[(k/9)%len(modelIPs)]
	family := syscall.AF_INET
	if addr.Is6() {
		family = syscall.AF_INET6
	}
	ev := NeighborEvent{
		Type:        typ,
		LinkIndex:   modelDevs[k%3],
		MasterIndex: modelVRFs[(k/3)%3],
		Family:      family,
		IP:          addr,
		State:       modelNUDs[r.pick(len(modelNUDs))],
	}
	if typ == RTM_NEWNEIGH {
		ev.HardwareAddr = modelMACs[r.pick(len(modelMACs))]
	}
	return ev
}

func FuzzStoreModel(f *testing.F) {
	// Config byte: bit 0 rate 0 (else rate.Inf), bits 1-2 burst, bit 3
	// include ^eth, bit 4 exclude ^eth1$, bit 5 long grace. Key byte k: dev k%3, VRF (k/3)%3,
	// IP (k/9)%3. Then opNew key nud mac, opDel key nud, opAdvance step,
	// opSync n errByte (key nud mac)*n op*m, opGC. In opSync, errByte%4 == 1
	// fails the list and m = (errByte/4)%4 operations (opNew, opNewAgain,
	// opDel or opAdvance with their arguments) happen while the dump is in
	// flight, after it was taken.
	const (
		cfgInf, cfgRate0Burst1, cfgRate0Burst2, cfgLongGrace = 0x00, 0x03, 0x05, 0x20
		mac1, mac2, mac3, noMAC                              = 1, 2, 3, 0
		reachable, incomplete                                = 1, 0
		key                                                  = 0 // eth0, no VRF, 10.0.0.1
	)
	advance := func(step int) []byte { return []byte{opAdvance, byte(step)} }
	seeds := map[string][]byte{
		"flap":                       {cfgInf, opNew, key, reachable, mac1, opNew, key, reachable, mac2},
		"incomplete then reachable":  {cfgInf, opNew, key, incomplete, noMAC, opNew, key, reachable, mac1},
		"incomplete keeps the MAC":   {cfgInf, opNew, key, reachable, mac1, opNew, key, incomplete, noMAC, opNew, key, reachable, mac2},
		"delete re-add same MAC":     {cfgInf, opNew, key, reachable, mac1, opDel, key, 0, opNew, key, reachable, mac1},
		"delete re-add other MAC":    {cfgInf, opNew, key, reachable, mac1, opDel, key, 0, opNew, key, reachable, mac2},
		"storm under rate limit":     {cfgRate0Burst2, opNew, key, reachable, mac1, opNew, key, reachable, mac2, opNew, key, reachable, mac3, opNew, key, reachable, mac1, opNew, key, reachable, mac2},
		"sync refreshes, gc purges":  {cfgInf, opNew, key, reachable, mac1, opNew, 1, reachable, mac2, opAdvance, 6, opSync, 1, 0, key, reachable, mac1, opGC},
		"failed sync purges nothing": {cfgInf, opNew, key, reachable, mac1, opAdvance, 7, opSync, 1, 1, key, reachable, mac1, opSync, 1, 0, 2, reachable, mac1},
		"filters and other keys":     {0x18, opNew, 1, reachable, mac1, opNew, 3, reachable, mac2, opNew, 4, reachable, mac1, opNew, 9, reachable, mac3, opNew, 18, reachable, mac2},
		"gc keeps deleted in grace":  {cfgLongGrace, opNew, key, reachable, mac1, opDel, key, 0, opAdvance, 5, opAdvance, 5, opGC, opAdvance, 1, opGC},
		"purge resets the limiter":   {cfgRate0Burst1, opNew, key, reachable, mac1, opNew, key, reachable, mac2, opNew, key, reachable, mac3, opAdvance, 7, opGC, opNew, key, reachable, mac1, opNew, key, reachable, mac2},
		// The dump of these syncs still shows mac1 when an event changes or
		// deletes the entry (errByte 4: one operation while listing).
		"stale dump after a flap":   {cfgInf, opNew, key, reachable, mac1, opSync, 1, 4, key, reachable, mac1, opNew, key, reachable, mac2, opNew, key, reachable, mac2},
		"stale dump after a delete": {cfgInf, opNew, key, reachable, mac1, opSync, 1, 4, key, reachable, mac1, opDel, key, 0, opGC},
		"stale dump, failed list":   {cfgInf, opNew, key, reachable, mac1, opSync, 1, 5, key, reachable, mac1, opNew, key, reachable, mac2},
		"clock moves while listing": {cfgInf, opNew, key, reachable, mac1, opNew, 1, reachable, mac1, opAdvance, 5, opSync, 2, 8, key, reachable, mac2, 1, reachable, mac3, opAdvance, 2, opNew, 1, reachable, mac2, opGC},
	}
	for _, step := range []int{3, 4} { // re-add just before and at the end of the grace period
		seeds[fmt.Sprintf("delete grace step %d", step)] = append(append([]byte{cfgInf, opNew, key, reachable, mac1, opDel, key, 0}, advance(step)...), opNew, key, reachable, mac2, opGC)
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &opReader{data: data}
		cfg := r.next()
		m := &storeModel{
			grace:   modelShortGrace,
			limit:   rate.Inf,
			now:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			entries: map[NeighborKey]*modelEntry{},
			tokens:  map[NeighborKey]int{},
			flaps:   map[[4]string]float64{},
			events:  map[string]float64{},
			errors:  map[string]float64{},
		}
		if cfg&1 != 0 {
			m.limit, m.burst = 0, (cfg>>1)&3
		}
		if cfg&8 != 0 {
			m.include = regexp.MustCompile(`^eth`)
		}
		if cfg&16 != 0 {
			m.exclude = regexp.MustCompile(`^eth1$`)
		}
		if cfg&32 != 0 {
			m.grace = modelLongGrace
		}
		steps := []time.Duration{0, time.Nanosecond, time.Second, m.grace - time.Nanosecond, m.grace, modelSync, modelSync + time.Nanosecond, 3 * modelSync}
		store := NewNeighborStore(StoreConfig{
			SyncInterval:  modelSync,
			DeleteGrace:   m.grace,
			FlapRate:      m.limit,
			FlapBurst:     m.burst,
			DeviceInclude: m.include,
			DeviceExclude: m.exclude,
		}, &mockResolver{names: modelNames}, testLogger())
		store.now = func() time.Time { return m.now }
		source := &channelSource{}
		watcher := NewWatcher(source, store, testLogger())

		// eventOrAdvance reads the arguments of an event or a clock step and
		// applies it to the model and the store.
		eventOrAdvance := func(op int) string {
			if op == opAdvance {
				d := steps[r.pick(len(steps))]
				m.now = m.now.Add(d)
				return fmt.Sprintf("advance %v", d)
			}
			typ := uint16(RTM_NEWNEIGH)
			if op == opDel {
				typ = RTM_DELNEIGH
			}
			ev := r.event(typ)
			m.handle(ev)
			store.HandleEvent(ev)
			return fmt.Sprintf("event %+v", ev)
		}

		for step := 0; step < 64 && len(r.data) > 0; step++ {
			var desc string
			switch op := r.pick(opCount); op {
			case opNew, opNewAgain, opDel, opAdvance:
				desc = eventOrAdvance(op)
			case opSync:
				n, errByte := r.pick(4), r.next()
				listErr, inFlight := errByte%4 == 1, (errByte/4)%4
				source.list, source.listErr = nil, nil
				for range n {
					source.list = append(source.list, r.event(RTM_NEWNEIGH))
				}
				if listErr {
					source.listErr = errors.New("list failed")
				}
				// List returns the dump taken when it started; meanwhile the
				// watcher handles events and the clock moves on.
				var during []string
				source.duringList = func() {
					for range inFlight {
						during = append(during, eventOrAdvance(r.pick(opAdvance+1)))
					}
				}
				m.listing()
				watcher.syncOnce(t.Context())
				m.sync(source.list, listErr)
				desc = fmt.Sprintf("sync %+v (list error %v) while %v", source.list, listErr, during)
			case opGC:
				desc = "gc"
				m.gc(m.now)
				store.gc()
			}
			if err := compareStore(m, store, watcher); err != nil {
				t.Fatalf("step %d (%s): %v", step, desc, err)
			}
		}
	})
}

func compareStore(m *storeModel, store *NeighborStore, w *Watcher) error {
	snap := store.Snapshot()
	if len(snap) != len(m.entries) {
		return fmt.Errorf("store has %d entries, model %d", len(snap), len(m.entries))
	}
	var flaps uint64
	for key, want := range m.entries {
		got, ok := snap[key]
		switch {
		case !ok:
			return fmt.Errorf("entry %+v missing", key)
		case string(got.MAC) != want.mac:
			return fmt.Errorf("entry %+v: MAC %v, want %v", key, got.MAC, net.HardwareAddr(want.mac))
		case got.State != want.state || got.DevName != want.devName || got.VRFName != want.vrfName:
			return fmt.Errorf("entry %+v: state/dev/vrf %d/%q/%q, want %d/%q/%q", key, got.State, got.DevName, got.VRFName, want.state, want.devName, want.vrfName)
		case !got.LastSeen.Equal(want.lastSeen) || !got.LastFlap.Equal(want.lastFlap):
			return fmt.Errorf("entry %+v: last seen/flap %v/%v, want %v/%v", key, got.LastSeen, got.LastFlap, want.lastSeen, want.lastFlap)
		case got.Deleted != want.deleted || !got.DeletedAt.Equal(want.deletedAt):
			return fmt.Errorf("entry %+v: deleted %v at %v, want %v at %v", key, got.Deleted, got.DeletedAt, want.deleted, want.deletedAt)
		case got.FlapCount != want.flapCount:
			return fmt.Errorf("entry %+v: %d flaps, want %d", key, got.FlapCount, want.flapCount)
		}
		flaps += got.FlapCount
	}
	// Counters outlive purged entries, so live entries account for at most
	// the counted flaps.
	if float64(flaps) > m.events["flap"] {
		return fmt.Errorf("entries count %d flaps, more than the %v of events_total{type=flap}", flaps, m.events["flap"])
	}
	// A snapshot is a deep copy: scribbling on it must not reach the store.
	for _, e := range snap {
		for i := range e.MAC {
			e.MAC[i] ^= 0xff
		}
	}
	for key, e := range store.Snapshot() {
		if string(e.MAC) != m.entries[key].mac {
			return fmt.Errorf("entry %+v: snapshot shares its MAC with the store", key)
		}
	}

	wantFlaps := map[string]float64{}
	for labels, v := range m.flaps {
		wantFlaps[strings.Join(labels[:], "|")] = v
	}
	for _, c := range []struct {
		name   string
		cv     *prometheus.CounterVec
		labels []string
		want   map[string]float64
	}{
		{"ipneigh_mac_change_total", store.flapCounter, []string{"dev", "vrf", "ip", "family"}, wantFlaps},
		{"ipneigh_events_total", store.eventsTotal, []string{"type"}, m.events},
		{"ipneigh_errors_total", store.errorsTotal, []string{"stage"}, m.errors},
	} {
		got, err := counterSeries(c.cv, c.labels)
		if err != nil {
			return err
		}
		if !maps.Equal(got, c.want) {
			return fmt.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
	if w.Ready() != m.ready {
		return fmt.Errorf("watcher ready %v, want %v", w.Ready(), m.ready)
	}
	return compareGauges(m, store)
}

// counterSeries returns the series of cv keyed by their label values, in the
// order of names, joined with "|".
func counterSeries(cv *prometheus.CounterVec, names []string) (map[string]float64, error) {
	ch := make(chan prometheus.Metric, 64)
	go func() { cv.Collect(ch); close(ch) }()
	series := map[string]float64{}
	var err error
	for metric := range ch {
		var pb dto.Metric
		if werr := metric.Write(&pb); werr != nil {
			err = werr
			continue
		}
		values := make([]string, len(names))
		for _, lp := range pb.GetLabel() {
			if i := slices.Index(names, lp.GetName()); i >= 0 {
				values[i] = lp.GetValue()
			}
		}
		series[strings.Join(values, "|")] = pb.GetCounter().GetValue()
	}
	return series, err
}

// compareGauges checks the scrape-time gauges against the model: entries
// aggregated by (dev, vrf, family, state) and one last-flap series per live
// entry that ever flapped. Duplicated series are counted, not rejected, since
// two VRF indexes can share an empty name.
func compareGauges(m *storeModel, store *NeighborStore) error {
	wantEntries := map[[4]string]float64{}
	wantLastFlap := map[string]int{}
	for key, e := range m.entries {
		if e.deleted {
			continue
		}
		wantEntries[[4]string{e.devName, e.vrfName, familyString(key.Family), nudStateString(e.state)}]++
		if !e.lastFlap.IsZero() {
			wantLastFlap[fmt.Sprint(e.devName, e.vrfName, key.IP, familyString(key.Family), e.lastFlap.Unix())]++
		}
	}
	ch := make(chan prometheus.Metric, 64)
	go func() { NewNeighborCollector(store).Collect(ch); close(ch) }()
	gotEntries := map[[4]string]float64{}
	gotLastFlap := map[string]int{}
	var err error
	for metric := range ch {
		var pb dto.Metric
		if werr := metric.Write(&pb); werr != nil {
			err = werr
			continue
		}
		l := map[string]string{}
		for _, lp := range pb.GetLabel() {
			l[lp.GetName()] = lp.GetValue()
		}
		switch metric.Desc() {
		case entriesDesc:
			k := [4]string{l["dev"], l["vrf"], l["family"], l["state"]}
			if _, dup := gotEntries[k]; dup {
				err = fmt.Errorf("ipneigh_entries%v exposed twice", k)
			}
			gotEntries[k] = pb.GetGauge().GetValue()
		case lastFlapDesc:
			gotLastFlap[fmt.Sprint(l["dev"], l["vrf"], l["ip"], l["family"], int64(pb.GetGauge().GetValue()))]++
		}
	}
	if err != nil {
		return err
	}
	if !maps.Equal(gotEntries, wantEntries) {
		return fmt.Errorf("ipneigh_entries %v, want %v", gotEntries, wantEntries)
	}
	if !maps.Equal(gotLastFlap, wantLastFlap) {
		return fmt.Errorf("ipneigh_last_flap_unix_seconds %v, want %v", gotLastFlap, wantLastFlap)
	}
	return nil
}
