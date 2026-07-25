GO ?= go
COVERAGE_FILE ?= coverage.out
COMPOSE ?= docker compose

.PHONY: build test race bench coverage docker-up

build:
	$(GO) build ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

bench:
	$(GO) test "-run=^$$" "-bench=." -benchmem ./...

coverage:
	$(GO) test "-covermode=atomic" "-coverprofile=$(COVERAGE_FILE)" ./...
	$(GO) tool cover "-func=$(COVERAGE_FILE)"

docker-up:
	$(COMPOSE) up --build -d
