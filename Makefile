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

# MTP cross-builds for ARM Linux (Raspberry Pi etc.)
#
# The MTP backend needs cgo + libmtp, which does not cross-compile cleanly.
# These targets build *inside* an emulated target-arch container so the
# native libmtp is linked. Requires a container runtime + qemu-user-static
# (Fedora: `sudo dnf install qemu-user-static`).
#
# The container must run rootful: rootless podman gets its own user
# namespace and can't see the host's binfmt_misc handlers, so an emulated
# exec fails with "Exec format error". Use a rootful runtime:
#   make build-mtp-linux-arm64 CONTAINER="sudo podman"   # rootful podman
#   make build-mtp-linux-arm64 CONTAINER=docker          # docker daemon is rootful (CI uses this)
.PHONY: build-mtp-linux-arm64 build-mtp-linux-armv7
CONTAINER ?= podman
MTP_IMAGE  := docker.io/library/golang:1.25-bookworm
MTP_DEPS   := apt-get update -qq && apt-get install -y -qq libmtp-dev libusb-1.0-0-dev pkg-config

build-mtp-linux-arm64:
	$(CONTAINER) run --rm --platform linux/arm64 -v "$(CURDIR)":/src:z -w /src $(MTP_IMAGE) \
	  bash -c '$(MTP_DEPS) && CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
	    go build -buildvcs=false -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-mtp-linux-arm64 ./cmd/kam-transfer'

build-mtp-linux-armv7:
	$(CONTAINER) run --rm --platform linux/arm/v7 -v "$(CURDIR)":/src:z -w /src $(MTP_IMAGE) \
	  bash -c '$(MTP_DEPS) && CGO_ENABLED=1 GOOS=linux GOARCH=arm GOARM=7 \
	    go build -buildvcs=false -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-mtp-linux-armv7 ./cmd/kam-transfer'
