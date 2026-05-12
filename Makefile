BINARY := kam-transfer
DIST := dist
PKG := github.com/kamdynamics/kam-transfer
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(PKG)/internal/version.Version=$(VERSION)

.PHONY: all build test tidy clean run lint
all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) ./cmd/kam-transfer

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
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 ./cmd/kam-transfer

build-macos:
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-macos-amd64 ./cmd/kam-transfer

build-macos-arm:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-macos-arm64 ./cmd/kam-transfer

build-windows:
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe ./cmd/kam-transfer

build-all: build-linux build-macos build-macos-arm build-windows
