GO ?= go
COVERAGE_FILE ?= coverage.out
COMPOSE ?= docker compose

.PHONY: build test race bench coverage fmt fmt-check vet lint check docker-up docker-ps docker-smoke docker-down

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

fmt:
	$(GO) run ./scripts/check-format.go -write cmd internal pkg scripts

fmt-check:
	$(GO) run ./scripts/check-format.go cmd internal pkg scripts

vet:
	$(GO) vet ./...

lint: fmt-check vet

check: lint test

docker-up:
	$(COMPOSE) up --build -d

docker-ps:
	$(COMPOSE) ps

docker-smoke:
	$(GO) run ./scripts/h2c-smoke.go

docker-down:
	$(COMPOSE) down
