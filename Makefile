VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X main.version=$(VERSION) -X main.revision=$(REVISION)
E2E_VM_NAME := ipneigh-e2e

.PHONY: build test lint vet clean e2e e2e-setup e2e-teardown

build:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o ipneigh_exporter .

test:
	go test ./... -race -count=1

lint:
	golangci-lint run

vet:
	go vet ./...

clean:
	rm -f ipneigh_exporter

e2e-setup:
	limactl start --name=$(E2E_VM_NAME) e2e/lima.yaml --tty=false 2>/dev/null || \
		limactl start $(E2E_VM_NAME) --tty=false

e2e: e2e-setup
	./e2e/run.sh

e2e-teardown:
	limactl stop $(E2E_VM_NAME) 2>/dev/null || true
	limactl delete $(E2E_VM_NAME) 2>/dev/null || true
