# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.5

FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

ARG SERVICE
RUN case "${SERVICE}" in \
      mock-users|mock-orders) ;; \
      *) echo "unsupported SERVICE: ${SERVICE}" >&2; exit 1 ;; \
    esac && \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/service \
      "./cmd/${SERVICE}"

FROM scratch

COPY --from=builder /out/service /service

USER 65532:65532

EXPOSE 8081 8082

ENTRYPOINT ["/service"]
