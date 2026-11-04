VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.0.0-dev)
LDFLAGS := -s -w -X github.com/llanc/vantu/internal/config.Version=$(VERSION)
IMAGE   ?= ghcr.io/llanc/vantu

.PHONY: all web build test vet lint clean docker

all: build

web:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o vantu ./cmd/vantu
	ln -sf vantu xft

test:
	CGO_ENABLED=0 go test ./...

vet:
	go vet ./...

lint: vet
	@test -z "$$(gofmt -l ./cmd ./internal ./web)" || (gofmt -l ./cmd ./internal ./web; echo "run: gofmt -w ./cmd ./internal ./web"; exit 1)

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

clean:
	rm -f vantu xft
	find web/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
