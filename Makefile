VERSION ?= v0.1.0

.PHONY: all web build test clean docker

all: build

web:
	pnpm --dir web install --allow-build=esbuild
	pnpm --dir web build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/xiaofantu/vantu/internal/config.Version=$(VERSION)" -o vantu ./cmd/vantu
	ln -sf vantu xft

test:
	CGO_ENABLED=0 go test -v -race ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t xiaofantu/vantu:latest .

clean:
	rm -f vantu xft
	rm -rf web/dist/*
	touch web/dist/.gitkeep
