.PHONY: build build-debug test test-ci test-integration test-e2e vet fmt checks clean

COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION ?= 0.1.0

build:
	CGO_ENABLED=0 go build -tags netgo -ldflags "-s -w -extldflags '-static' -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.Date=$(DATE)" -o warpgen ./cmd/warpgen

build-debug:
	CGO_ENABLED=0 go build -tags netgo -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.Date=$(DATE)" -o warpgen-debug ./cmd/warpgen

test:
	go test ./... -count=1 -short

test-ci:
	go test ./... -count=1 -short -race -cover -coverprofile=coverage.out

test-integration:
	WARPGEN_INTEGRATION=1 go test -tags integration ./cli/ -count=1 -run Integration -v

# Сетевые проверки пайплайна run: реальные туннель, аккаунт и warp-plus.
# Гонять только на свободной машине, в CI не запускать.
test-e2e:
	WARPGEN_E2E=1 go test -tags e2e ./core/ -count=1 -run E2E -v

vet:
	go vet ./...

fmt:
	gofmt -l . | grep -v '^$$' || true

checks: vet fmt test

clean:
	rm -f warpgen warpgen-debug coverage.out
