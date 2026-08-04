GO ?= go
COVERAGE_FILE ?= coverage.out
COMPOSE ?= docker compose

.PHONY: build test race bench coverage fmt fmt-check vet lint check client-run docker-up docker-ps docker-smoke docker-e2e-round-robin docker-ui-up docker-ui-ps docker-ui-smoke docker-down

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

client-run:
	$(GO) run ./cmd/client

docker-up:
	$(COMPOSE) up --build -d --scale pdu-session=3

docker-ps:
	$(COMPOSE) ps

docker-smoke:
	$(GO) run ./scripts/h2c-smoke.go

docker-e2e-round-robin:
	$(GO) run ./scripts/e2e-round-robin.go

docker-ui-up:
	$(COMPOSE) --profile tools up --build -d --scale pdu-session=3

docker-ui-ps:
	$(COMPOSE) --profile tools ps

docker-ui-smoke:
	$(GO) run ./scripts/ui-smoke.go

docker-down:
	$(COMPOSE) down --remove-orphans
