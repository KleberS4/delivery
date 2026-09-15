BINARY  := delivery
PKG     := ./...
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0-dev")
LDFLAGS := -s -w -X github.com/kleberS4/delivery/internal/cli.Version=$(VERSION)

# Target platforms: linux and darwin, amd64 and arm64.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build test test-race lint fmt fmt-check vet staticcheck cover release clean check

all: check build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/delivery

# The full check, identical to what CI runs.
check: fmt-check vet staticcheck test test-race

test:
	go test $(PKG)

# rapid already defaults to a random seed and prints the failing one, so a
# property failure is replayable from the log. Shrinking is never disabled.
# The flag is not passed explicitly: it is undefined in the test binaries of
# packages that do not import rapid, and would fail them.
test-race:
	go test -race $(PKG)

# -coverpkg counts execution across package boundaries, so packages without
# their own tests are credited for what the CLI tests actually run.
cover:
	go test -coverpkg=$(PKG) -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -20

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

vet:
	go vet $(PKG)

staticcheck:
	@command -v staticcheck >/dev/null 2>&1 || { \
		echo "staticcheck not found; install with: go install honnef.co/go/tools/cmd/staticcheck@latest"; exit 1; }
	staticcheck $(PKG)

# Static binaries for every target platform, with checksums.
release: clean
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(DIST)/$(BINARY)-$$os-$$arch ./cmd/delivery; \
	done
	@cd $(DIST) && sha256sum * > SHA256SUMS
	@echo "binaries and checksums in $(DIST)/"

clean:
	rm -rf $(DIST) $(BINARY) coverage.out
