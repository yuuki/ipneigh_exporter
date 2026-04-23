package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

type NeighborKey struct {
	Dev    int
	VRF    int
	IP     netip.Addr
	Family int
}

type NeighborEntry struct {
	MAC       net.HardwareAddr
	State     int
	DevName   string
	VRFName   string
	LastSeen  time.Time
	LastFlap  time.Time
	Deleted   bool
	DeletedAt time.Time
	FlapCount uint64
}

type StoreConfig struct {
	StaleTTL      time.Duration
	DeleteGrace   time.Duration
	FlapRate      rate.Limit
	FlapBurst     int
	DeviceInclude *regexp.Regexp
	DeviceExclude *regexp.Regexp
}

type LinkResolver interface {
	LinkName(index int) (string, error)
}

type NeighborStore struct {
	mu           sync.RWMutex
	entries      map[NeighborKey]*NeighborEntry
	flapLimiters map[NeighborKey]*rate.Limiter

	linkMu    sync.RWMutex
	linkNames map[int]string

	flapCounter *prometheus.CounterVec
	eventsTotal *prometheus.CounterVec
	errorsTotal *prometheus.CounterVec

	linkResolver LinkResolver
	config       StoreConfig
	logger       *slog.Logger
	now          func() time.Time
}

func NewNeighborStore(config StoreConfig, resolver LinkResolver, logger *slog.Logger) *NeighborStore {
	return &NeighborStore{
		entries:      make(map[NeighborKey]*NeighborEntry),
		linkNames:    make(map[int]string),
		flapLimiters: make(map[NeighborKey]*rate.Limiter),
		flapCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "linux_neighbor_mac_change_total",
			Help: "Number of MAC address changes detected for the same IP.",
		}, []string{"dev", "vrf", "ip", "family"}),
		eventsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "linux_neighbor_exporter_events_total",
			Help: "Total number of events processed by the exporter.",
		}, []string{"type"}),
		errorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "linux_neighbor_exporter_errors_total",
			Help: "Total number of errors encountered by the exporter.",
		}, []string{"stage"}),
		linkResolver: resolver,
		config:       config,
		logger:       logger,
		now:          time.Now,
	}
}

func (s *NeighborStore) HandleEvent(ev NeighborEvent) {
	devName := s.resolveLink(ev.LinkIndex)
	if devName == "" {
		s.errorsTotal.WithLabelValues("link_resolve").Inc()
		return
	}

	if !s.deviceAllowed(devName) {
		return
	}

	vrfName := s.resolveLink(ev.MasterIndex)

	key := NeighborKey{
		Dev:    ev.LinkIndex,
		VRF:    ev.MasterIndex,
		IP:     ev.IP,
		Family: ev.Family,
	}

	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if ev.Type == RTM_DELNEIGH {
		s.eventsTotal.WithLabelValues("neigh_del").Inc()
		if entry, ok := s.entries[key]; ok {
			entry.Deleted = true
			entry.DeletedAt = now
			entry.State = ev.State
		}
		return
	}

	s.eventsTotal.WithLabelValues("neigh_new").Inc()

	entry, exists := s.entries[key]
	if !exists {
		s.entries[key] = &NeighborEntry{
			MAC:      ev.HardwareAddr,
			State:    ev.State,
			DevName:  devName,
			VRFName:  vrfName,
			LastSeen: now,
		}
		return
	}

	oldMAC := entry.MAC
	newMAC := ev.HardwareAddr

	if entry.Deleted && !now.Before(entry.DeletedAt.Add(s.config.DeleteGrace)) {
		oldMAC = nil
	}

	entry.Deleted = false
	if len(newMAC) > 0 {
		entry.MAC = newMAC
	}
	entry.State = ev.State
	entry.DevName = devName
	entry.VRFName = vrfName
	entry.LastSeen = now

	if isFlap(oldMAC, newMAC) {
		if s.allowFlap(key) {
			entry.FlapCount++
			entry.LastFlap = now
			familyStr := familyString(ev.Family)
			s.flapCounter.WithLabelValues(devName, vrfName, ev.IP.String(), familyStr).Inc()
			s.eventsTotal.WithLabelValues("flap").Inc()
			s.logger.Info("flap detected",
				"dev", devName, "ip", ev.IP, "old_mac", oldMAC, "new_mac", newMAC)
		} else {
			s.eventsTotal.WithLabelValues("flap_rate_limited").Inc()
		}
	}
}

func isFlap(oldMAC, newMAC net.HardwareAddr) bool {
	return len(oldMAC) > 0 && len(newMAC) > 0 && !bytes.Equal(oldMAC, newMAC)
}

func (s *NeighborStore) allowFlap(key NeighborKey) bool {
	lim, ok := s.flapLimiters[key]
	if !ok {
		lim = rate.NewLimiter(s.config.FlapRate, s.config.FlapBurst)
		s.flapLimiters[key] = lim
	}
	return lim.Allow()
}

func (s *NeighborStore) RunGC(ctx context.Context) {
	if s.config.StaleTTL <= 0 {
		return
	}
	ticker := time.NewTicker(s.config.StaleTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.gc()
		}
	}
}

func (s *NeighborStore) gc() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.entries {
		if now.Sub(entry.LastSeen) > s.config.StaleTTL {
			delete(s.entries, key)
			delete(s.flapLimiters, key)
			s.eventsTotal.WithLabelValues("gc_purge").Inc()
		}
	}
}

func (s *NeighborStore) Snapshot() map[NeighborKey]*NeighborEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := make(map[NeighborKey]*NeighborEntry, len(s.entries))
	for k, v := range s.entries {
		cp := *v
		if v.MAC != nil {
			cp.MAC = make(net.HardwareAddr, len(v.MAC))
			copy(cp.MAC, v.MAC)
		}
		snap[k] = &cp
	}
	return snap
}

func (s *NeighborStore) resolveLink(index int) string {
	if index == 0 {
		return ""
	}
	s.linkMu.RLock()
	name, ok := s.linkNames[index]
	s.linkMu.RUnlock()
	if ok {
		return name
	}
	name, err := s.linkResolver.LinkName(index)
	if err != nil {
		s.logger.Debug("failed to resolve link", "index", index, "error", err)
		return ""
	}
	if name == "" {
		return ""
	}
	s.linkMu.Lock()
	s.linkNames[index] = name
	s.linkMu.Unlock()
	return name
}

func (s *NeighborStore) DescribeCounters(ch chan<- *prometheus.Desc) {
	s.flapCounter.Describe(ch)
	s.eventsTotal.Describe(ch)
	s.errorsTotal.Describe(ch)
}

func (s *NeighborStore) CollectCounters(ch chan<- prometheus.Metric) {
	s.flapCounter.Collect(ch)
	s.eventsTotal.Collect(ch)
	s.errorsTotal.Collect(ch)
}

func (s *NeighborStore) deviceAllowed(name string) bool {
	if s.config.DeviceInclude != nil && !s.config.DeviceInclude.MatchString(name) {
		return false
	}
	if s.config.DeviceExclude != nil && s.config.DeviceExclude.MatchString(name) {
		return false
	}
	return true
}

func familyString(family int) string {
	if family == syscall.AF_INET6 {
		return "ipv6"
	}
	return "ipv4"
}
