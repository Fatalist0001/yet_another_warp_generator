.PHONY: build build-debug test test-ci test-integration test-e2e vet fmt checks clean gui-deps gui-test gui-analyze gui-run gui-build

# GUI на Flutter: SDK берётся из PATH или из локального .toolchain/flutter/bin,
# кэш пакетов — рядом с локальным SDK (основной диск не занимаем).
FLUTTER ?= $(shell command -v flutter 2>/dev/null || echo $(CURDIR)/.toolchain/flutter/bin/flutter)
GUI_DIR := gui
PUB_CACHE_DIR := $(if $(wildcard $(CURDIR)/.toolchain/pub-cache),$(CURDIR)/.toolchain/pub-cache,$(HOME)/.pub-cache)
GUI_ENV := PUB_CACHE=$(PUB_CACHE_DIR)

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

# --- GUI (Flutter, Linux) ---

gui-deps:
	cd $(GUI_DIR) && $(GUI_ENV) $(FLUTTER) pub get

gui-test:
	cd $(GUI_DIR) && $(GUI_ENV) $(FLUTTER) test

gui-analyze:
	cd $(GUI_DIR) && $(GUI_ENV) $(FLUTTER) analyze

# GUI ищет бинарник в WARPGEN_BIN: при запуске из gui/ путь к ./warpgen иначе не очевиден.
gui-run:
	cd $(GUI_DIR) && WARPGEN_BIN=$(CURDIR)/warpgen $(GUI_ENV) $(FLUTTER) run -d linux

gui-build:
	cd $(GUI_DIR) && $(GUI_ENV) $(FLUTTER) build linux --release
