package main

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	entriesDesc = prometheus.NewDesc(
		"linux_neighbor_entries",
		"Number of neighbor entries by state.",
		[]string{"dev", "vrf", "family", "state"}, nil,
	)
	lastFlapDesc = prometheus.NewDesc(
		"linux_neighbor_last_flap_unix_seconds",
		"Unix timestamp of the most recent MAC flap for this neighbor.",
		[]string{"dev", "vrf", "ip", "family"}, nil,
	)
)

type NeighborCollector struct {
	store *NeighborStore
}

func NewNeighborCollector(store *NeighborStore) *NeighborCollector {
	return &NeighborCollector{store: store}
}

func (c *NeighborCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- entriesDesc
	ch <- lastFlapDesc
	c.store.DescribeCounters(ch)
}

func (c *NeighborCollector) Collect(ch chan<- prometheus.Metric) {
	snap := c.store.Snapshot()

	type entriesKey struct {
		Dev, VRF, Family, State string
	}
	counts := make(map[entriesKey]float64)

	for key, entry := range snap {
		if entry.Deleted {
			continue
		}
		devName := entry.DevName
		vrfName := entry.VRFName
		familyStr := familyString(key.Family)
		stateStr := nudStateString(entry.State)

		ek := entriesKey{devName, vrfName, familyStr, stateStr}
		counts[ek]++

		if !entry.LastFlap.IsZero() {
			ch <- prometheus.MustNewConstMetric(
				lastFlapDesc, prometheus.GaugeValue,
				float64(entry.LastFlap.Unix()),
				devName, vrfName, key.IP.String(), familyStr,
			)
		}
	}

	for ek, count := range counts {
		ch <- prometheus.MustNewConstMetric(
			entriesDesc, prometheus.GaugeValue, count,
			ek.Dev, ek.VRF, ek.Family, ek.State,
		)
	}

	c.store.CollectCounters(ch)
}

func nudStateString(state int) string {
	switch {
	case state&NUD_REACHABLE != 0:
		return "reachable"
	case state&NUD_STALE != 0:
		return "stale"
	case state&NUD_DELAY != 0:
		return "delay"
	case state&NUD_PROBE != 0:
		return "probe"
	case state&NUD_FAILED != 0:
		return "failed"
	case state&NUD_INCOMPLETE != 0:
		return "incomplete"
	case state&NUD_PERMANENT != 0:
		return "permanent"
	case state&NUD_NOARP != 0:
		return "noarp"
	default:
		return "unknown"
	}
}
