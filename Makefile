BINARY  := gogalaxy
PKG     := github.com/jvzantvoort/gogalaxy
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS := -s -w -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.Commit=$(COMMIT)

# Only our own packages; the galaxy_ng reference tree contains unrelated Go code.
PKGS := ./cmd/... ./internal/... .

.PHONY: build test vet fmt clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

test:
	go test $(PKGS)

vet:
	go vet $(PKGS)

fmt:
	gofmt -w cmd internal main.go

clean:
	rm -f $(BINARY)
