BINARY := faws
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X github.com/kensantoso/faws/cmd.version=$(VERSION)" -o $(BINARY) .

test:
	go test ./...

# go env GOBIN is empty on a default Go setup (nothing has ever set it), and
# `install ... /faws` against that empty string fails with a read-only
# filesystem error. Fall back to GOPATH/bin, exactly like `go install` itself
# does when GOBIN is unset.
GOBIN := $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)

install: build
	install -m 0755 $(BINARY) $(GOBIN)/$(BINARY)

.PHONY: build test install
