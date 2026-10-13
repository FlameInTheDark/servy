# Servy — build system
GO ?= go
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS ?= -s -w -X github.com/FlameInTheDark/servy/internal/version.Version=$(VERSION) \
           -X github.com/FlameInTheDark/servy/internal/version.Commit=$(COMMIT) \
           -X github.com/FlameInTheDark/servy/internal/version.BuildDate=$(DATE)

.PHONY: all backend frontend build release test clean sqlc proto docker docker-wine

all: build

# build the frontend and embed it
frontend:
	cd frontend && npm ci && npm run build
	rm -rf internal/spa/dist
	cp -r frontend/dist internal/spa/dist

backend:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/servy ./cmd/servy
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/servy-agent ./cmd/servy-agent

build: frontend backend

# cross-platform release archives + SHA256SUMS into dist/
release: frontend
	scripts/build-release.sh $(VERSION)

# panel container image (Java runtimes + steamcmd; no docker CLI needed —
# the panel talks to the Docker Engine API over the mounted socket)
docker:
	docker build -t servy:dev \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(DATE) .

# self-made Wine + SteamCMD base image for Windows-only game servers
# (Abiotic Factor, V Rising, …) — see docker/steamcmd-wine/README.md
docker-wine:
	docker build -t servy-wine:dev docker/steamcmd-wine

# backend only (placeholder UI embedded; API + WS still work)
api:
	$(GO) build -o bin/servy ./cmd/servy

test:
	$(GO) test ./... -count=1

sqlc:
	sqlc generate
	python3 scripts/gen_shim.py

proto:
	protoc --proto_path=proto --go_out=. --go_opt=module=github.com/FlameInTheDark/servy \
	  --go-grpc_out=. --go-grpc_opt=module=github.com/FlameInTheDark/servy proto/node.proto

clean:
	rm -rf bin internal/spa/dist frontend/dist dist
