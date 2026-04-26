package main

import (
	"os"
	"regexp"
	"testing"
)

func TestE2ERunUsesGuestWorkdirForLimaShell(t *testing.T) {
	body, err := os.ReadFile("e2e/run.sh")
	if err != nil {
		t.Fatal(err)
	}

	pattern := regexp.MustCompile(`limactl shell --workdir /tmp "\$VM_NAME"`)
	if !pattern.Match(body) {
		t.Fatal("e2e/run.sh must run limactl shell commands from a guest directory that exists in CI")
	}
}

func TestE2ERunCopiesExporterIntoGuest(t *testing.T) {
	body, err := os.ReadFile("e2e/run.sh")
	if err != nil {
		t.Fatal(err)
	}

	pathPattern := regexp.MustCompile(`EXPORTER_PATH="/tmp/ipneigh_exporter"`)
	if !pathPattern.Match(body) {
		t.Fatal("e2e/run.sh must use a guest-local exporter path")
	}

	copyPattern := regexp.MustCompile(`limactl copy "\$HOST_BINARY" "\$VM_NAME:\$EXPORTER_PATH"`)
	if !copyPattern.Match(body) {
		t.Fatal("e2e/run.sh must copy the built exporter into the guest before starting it")
	}
}
