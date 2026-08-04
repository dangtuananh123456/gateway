GO ?= go
COVERAGE_FILE ?= coverage.out
COMPOSE ?= docker compose
LOAD_TEST_ARGS ?= -target http://localhost:18080/nsmf-pdusession/v1/sm-contexts -duration 10s -connections 4 -streams 50 -request-timeout 3s

.PHONY: build test race lifecycle bench coverage fmt fmt-check vet lint check client-run load-test docker-up docker-ps docker-smoke docker-e2e-round-robin docker-ui-up docker-ui-ps docker-ui-smoke docker-weighted-up docker-weighted-ps docker-e2e-weighted docker-weighted-down docker-load-up docker-load-ps docker-e2e-load docker-load-down docker-scale-up docker-scale-ps docker-e2e-scale docker-scale-down docker-failure-up docker-failure-ps docker-e2e-failure docker-failure-down docker-down

build:
	$(GO) build ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

lifecycle:
	$(GO) test -count=10 ./internal/discovery ./internal/gateway ./internal/registry ./internal/routing

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

load-test:
	$(GO) run ./cmd/loadtest $(LOAD_TEST_ARGS)

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

docker-weighted-up:
	$(COMPOSE) -f docker-compose.weighted.yml up --build -d

docker-weighted-ps:
	$(COMPOSE) -f docker-compose.weighted.yml ps

docker-e2e-weighted:
	$(GO) run ./scripts/e2e-weighted.go

docker-weighted-down:
	$(COMPOSE) -f docker-compose.weighted.yml down --remove-orphans

docker-load-up:
	$(COMPOSE) -f docker-compose.load.yml up --build -d

docker-load-ps:
	$(COMPOSE) -f docker-compose.load.yml ps

docker-e2e-load:
	$(GO) run ./scripts/e2e-load.go

docker-load-down:
	$(COMPOSE) -f docker-compose.load.yml down --remove-orphans

docker-scale-up:
	$(COMPOSE) -f docker-compose.scale.yml up --build -d --scale pdu-session=3

docker-scale-ps:
	$(COMPOSE) -f docker-compose.scale.yml ps

docker-e2e-scale:
	$(GO) run ./scripts/e2e-scale.go

docker-scale-down:
	$(COMPOSE) -f docker-compose.scale.yml down --remove-orphans

docker-failure-up:
	$(COMPOSE) -f docker-compose.failure.yml up --build -d

docker-failure-ps:
	$(COMPOSE) -f docker-compose.failure.yml ps

docker-e2e-failure:
	$(GO) run ./scripts/e2e-failure.go

docker-failure-down:
	$(COMPOSE) -f docker-compose.failure.yml down --remove-orphans

docker-down:
	$(COMPOSE) down --remove-orphans
