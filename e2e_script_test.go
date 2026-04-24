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
