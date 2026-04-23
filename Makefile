VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X main.version=$(VERSION) -X main.revision=$(REVISION)

.PHONY: build test lint vet clean

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
