package main

import (
	"testing"

	"go.uber.org/goleak"
)

// Watcher.Run starts the periodic sync goroutine and NetlinkSource.Subscribe
// starts the forwarder (and, through the netlink library, the socket reader).
// All of them must end with their context, so the package fails if a test
// leaves one behind.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
