package main

import "golang.org/x/sys/unix"

const (
	NUD_INCOMPLETE = unix.NUD_INCOMPLETE
	NUD_REACHABLE  = unix.NUD_REACHABLE
	NUD_STALE      = unix.NUD_STALE
	NUD_DELAY      = unix.NUD_DELAY
	NUD_PROBE      = unix.NUD_PROBE
	NUD_FAILED     = unix.NUD_FAILED
	NUD_NOARP      = unix.NUD_NOARP
	NUD_PERMANENT  = unix.NUD_PERMANENT

	RTM_NEWNEIGH = unix.RTM_NEWNEIGH
	RTM_DELNEIGH = unix.RTM_DELNEIGH
)
