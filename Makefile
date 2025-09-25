# Simple developer helpers

.PHONY: all build test lint install hooks

all: build

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...

install:
	GOOS=$(shell uname -s | tr '[:upper:]' '[:lower:]') GOARCH=$(shell uname -m) go build -o pc ./cmd/pc

hooks:
	sh scripts/setup-git-hooks.sh
