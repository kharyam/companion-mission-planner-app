BINARY := kam-transfer
DIST := dist
PKG := github.com/kamdynamics/kam-transfer
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(PKG)/internal/version.Version=$(VERSION)

# Default build is pure Go: ADB-only, cross-compiles cleanly.
# `make build-mtp` adds the libmtp cgo backend; requires libmtp-devel.
.PHONY: all build build-mtp test tidy clean run lint
all: build

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) ./cmd/kam-transfer

build-mtp:
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-mtp ./cmd/kam-transfer

run: build
	./$(DIST)/$(BINARY) serve

test:
	go test ./...

tidy:
	go mod tidy

lint:
	go vet ./...

clean:
	rm -rf $(DIST)

# Cross-compile targets
.PHONY: build-linux build-macos build-macos-arm build-windows build-all
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 ./cmd/kam-transfer

build-macos:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-macos-amd64 ./cmd/kam-transfer

build-macos-arm:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-macos-arm64 ./cmd/kam-transfer

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe ./cmd/kam-transfer

build-all: build-linux build-macos build-macos-arm build-windows
