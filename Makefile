VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X main.version=$(VERSION) -X main.revision=$(REVISION)
E2E_VM_NAME := ipneigh-e2e

# Tool versions. CI pins the same versions (.github/workflows); bump together.
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION   ?= v1.9.0
ACTIONLINT_VERSION    ?= v1.7.12
ZIZMOR_VERSION        ?= 1.32.0
FUZZTIME              ?= 30s
FUZZFLAGS             ?=
BENCH_COUNT           ?= 6

# Build go-run tools with this module's toolchain: with an older default go,
# `go run tool@version` uses the Go version of the tool's own go.mod, and a
# golangci-lint built that way refuses this module. Evaluated in the module,
# GOVERSION is the go.mod toolchain.
GO_TOOLCHAIN = $(shell go env GOVERSION)
GOLANGCI_LINT ?= GOTOOLCHAIN=$(GO_TOOLCHAIN) go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
# uvx or pipx (preinstalled on GitHub-hosted runners) runs the pinned zizmor.
ZIZMOR ?= $(if $(shell command -v uvx 2>/dev/null),uvx zizmor@$(ZIZMOR_VERSION),pipx run zizmor==$(ZIZMOR_VERSION))
# test-integration needs root; it runs the test binary with sudo unless make
# itself already runs as root.
SUDO ?= $(if $(filter 0,$(shell id -u)),,sudo)

.PHONY: build check test test-race test-fuzz test-integration bench lint fmt lint-actions vet vuln complexity clean e2e e2e-setup e2e-teardown

build:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o ipneigh_exporter .

# The local pre-PR gate: what the required CI jobs check without root or a VM.
check: lint vet lint-actions test-race build

test:
	go test ./... -race -count=1

# The cross-repository name; test already runs the race detector.
test-race: test

# Run every fuzz target for FUZZTIME. Seed corpora (f.Add and testdata/fuzz)
# also run as part of test.
test-fuzz:
	@set -e; for pkg in $$(go list ./...); do \
		for f in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz' || true); do \
			echo "== $$pkg $$f"; \
			go test -run='^$$' -fuzz="^$$f\$$" -fuzztime=$(FUZZTIME) $(FUZZFLAGS) $$pkg; \
		done; \
	done

# Real-kernel netlink tests in throwaway network namespaces (root required).
# The test binary is built as the current user and run as root.
test-integration:
	go test -c -race -o ipneigh_exporter.test .
	$(SUDO) env IPNEIGH_NETLINK_INTEGRATION=1 ./ipneigh_exporter.test -test.run '^TestNetlinkIntegration' -test.count=1 -test.v

# Benchmarks; compare runs with benchstat.
bench:
	go test -run='^$$' -bench=. -benchmem -count=$(BENCH_COUNT) ./... | tee bench.txt

# golangci-lint (linters and formatters per .golangci.yml) and go.mod tidiness.
lint:
	$(GOLANGCI_LINT) run
	go mod tidy -diff

fmt:
	$(GOLANGCI_LINT) fmt

# Lint (actionlint) and audit (zizmor) the GitHub Actions workflows.
lint-actions:
	GOTOOLCHAIN=$(GO_TOOLCHAIN) go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	$(ZIZMOR) $(if $(GH_TOKEN),,--offline) .

# Vet the Linux build and the non-Linux stubs (source_stub.go, nud_other.go).
vet:
	go vet ./...
	GOOS=darwin go vet ./...

# Known vulnerabilities reachable from the code.
vuln:
	GOTOOLCHAIN=$(GO_TOOLCHAIN) go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# Rank the most complex functions with cccc (https://github.com/moznion/cccc);
# what is analyzed is configured in cccc.toml.
complexity:
	cccc --table --top-cognitive 20 .

clean:
	rm -f ipneigh_exporter ipneigh_exporter.test bench.txt

e2e-setup:
	mkdir -p /tmp/lima
	limactl start --name=$(E2E_VM_NAME) e2e/lima.yaml --tty=false 2>/dev/null || \
		limactl start $(E2E_VM_NAME) --tty=false

e2e: e2e-setup
	./e2e/run.sh

e2e-teardown:
	limactl stop $(E2E_VM_NAME) 2>/dev/null || true
	limactl delete $(E2E_VM_NAME) 2>/dev/null || true
