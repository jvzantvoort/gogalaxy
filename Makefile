BINARY  := gogalaxy
PKG     := github.com/jvzantvoort/gogalaxy
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)
CONTAINER ?= $(shell command -v docker 2>/dev/null || command -v podman 2>/dev/null)

.PHONY: build test cover lint vet fmt tidy snapshot docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

test:
	go test -race ./...

cover:
	go test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run ./...

vet:
	go vet ./...

fmt:
	golangci-lint fmt ./...

tidy:
	go mod tidy

# Local dry run of the release: all platforms, no publishing.
snapshot:
	goreleaser release --snapshot --clean --skip=docker,sbom

# Local container image, using the same Dockerfile as releases.
docker:
	rm -rf dist/docker && mkdir -p dist/docker/linux/amd64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/docker/linux/amd64/gogalaxy .
	cp Dockerfile gogalaxy.example.yaml dist/docker/
	$(CONTAINER) build --platform linux/amd64 -t gogalaxy:$(VERSION) dist/docker

clean:
	rm -rf $(BINARY) dist coverage.out
