GO ?= go
COVERAGE_FILE ?= coverage.out
COMPOSE ?= docker compose
LOAD_TEST_ARGS ?= -target http://localhost:18080/nsmf-pdusession/v1/sm-contexts -duration 10s -connections 4 -streams 50 -request-timeout 3s
PDU_REPLICAS ?= 3
UI_BASE_URL ?= http://localhost:18090
UI_EXPECTED_BACKENDS ?= $(PDU_REPLICAS)

.PHONY: build test race lifecycle bench coverage fmt fmt-check vet lint check client-run load-test docker-up docker-ps docker-smoke docker-e2e-round-robin docker-ui-up docker-ui-scale docker-ui-ps docker-ui-smoke docker-ui-weighted-up docker-ui-weighted-ps docker-ui-weighted-smoke docker-ui-load-up docker-ui-load-ps docker-ui-load-smoke docker-weighted-up docker-weighted-ps docker-e2e-weighted docker-weighted-down docker-load-up docker-load-ps docker-e2e-load docker-load-down docker-scale-up docker-scale-ps docker-e2e-scale docker-scale-down docker-failure-up docker-failure-ps docker-e2e-failure docker-failure-down docker-performance-up docker-performance-ps docker-performance-down docker-down

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
	$(COMPOSE) --profile tools up --build -d --scale pdu-session=$(PDU_REPLICAS)

docker-ui-scale:
	$(COMPOSE) --profile tools up -d --scale pdu-session=$(PDU_REPLICAS) --no-recreate

docker-ui-ps:
	$(COMPOSE) --profile tools ps

docker-ui-smoke:
	$(GO) run ./scripts/ui-smoke.go $(UI_BASE_URL) $(UI_EXPECTED_BACKENDS)

docker-ui-weighted-up:
	$(COMPOSE) -f docker-compose.weighted.yml --profile tools up --build -d

docker-ui-weighted-ps:
	$(COMPOSE) -f docker-compose.weighted.yml --profile tools ps

docker-ui-weighted-smoke:
	$(GO) run ./scripts/ui-smoke.go $(UI_BASE_URL) $(UI_EXPECTED_BACKENDS)

docker-ui-load-up:
	$(COMPOSE) -f docker-compose.load.yml --profile tools up --build -d

docker-ui-load-ps:
	$(COMPOSE) -f docker-compose.load.yml --profile tools ps

docker-ui-load-smoke:
	$(GO) run ./scripts/ui-smoke.go $(UI_BASE_URL) $(UI_EXPECTED_BACKENDS)

docker-weighted-up:
	$(COMPOSE) -f docker-compose.weighted.yml up --build -d

docker-weighted-ps:
	$(COMPOSE) -f docker-compose.weighted.yml ps

docker-e2e-weighted:
	$(GO) run ./scripts/e2e-weighted.go

docker-weighted-down:
	$(COMPOSE) -f docker-compose.weighted.yml --profile tools down --remove-orphans

docker-load-up:
	$(COMPOSE) -f docker-compose.load.yml up --build -d

docker-load-ps:
	$(COMPOSE) -f docker-compose.load.yml ps

docker-e2e-load:
	$(GO) run ./scripts/e2e-load.go

docker-load-down:
	$(COMPOSE) -f docker-compose.load.yml --profile tools down --remove-orphans

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

docker-performance-up:
	$(COMPOSE) -f docker-compose.performance.yml up --build -d --scale pdu-session=3

docker-performance-ps:
	$(COMPOSE) -f docker-compose.performance.yml ps

docker-performance-down:
	$(COMPOSE) -f docker-compose.performance.yml down --remove-orphans

docker-down:
	$(COMPOSE) --profile tools down --remove-orphans

# 	# 1. Chạy Round Robin
#     make docker-ui-up ROUTING_MODE=round_robin PDU_REPLICAS=3
#     make docker-ui-smoke PDU_REPLICAS=3
#     make docker-down

#     # 2. Chạy Weighted
#     make docker-ui-up ROUTING_MODE=weighted PDU_REPLICAS=3
#     make docker-ui-smoke PDU_REPLICAS=3
#     make docker-down

#     # 3. Chạy Load-based
#     make docker-ui-up ROUTING_MODE=load PDU_REPLICAS=3
#     make docker-ui-smoke PDU_REPLICAS=3
#     make docker-down

# tps 1s  -> 15000
# connection pool h2c
# encode và decode json
# client -> gateway -> logic (max time response)
# optimize -> 1s -> 10000 rps
# dns -> chạy pdu trên máy khác -> làm sao để dns resolver có thể lookup ra
# hiện tại tại sao dns server tại sao nó hoạt động được 
# chuyển http1.1 -> h2c

