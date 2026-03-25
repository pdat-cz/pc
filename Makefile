# Simple developer helpers

.PHONY: all build test lint install hooks

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS ?= -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)' -X 'main.date=$(DATE)'

all: build

build:
	go build -ldflags "$(LDFLAGS)" ./...

test:
	go test ./...

lint:
	go vet ./...

install:
	GOOS=$(shell uname -s | tr '[:upper:]' '[:lower:]') GOARCH=$(shell uname -m) go build -ldflags "$(LDFLAGS)" -o pc ./cmd/pc

hooks:
	sh scripts/setup-git-hooks.sh
