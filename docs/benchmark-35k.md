# Benchmark 35k success TPS

Measured locally on 2026-10-08 with Go 1.26.5, Linux 7.0.0-34-generic,
Docker 29.6.2, and 24 logical host CPUs.

## Conditions and acceptance

- One Gateway and five PDU replicas; round-robin routing, h2c on both legs.
- Gateway cgroup `cpu.max = 100000 100000`: one vCPU. `memory.max = 1073741824`:
  one GiB RAM. These limits were checked on the running container.
- Access/lifecycle logs disabled during both the original and optimized runs.
- 40,000 create-session attempts in a one-second measurement window, with at
  least 35,000 successful HTTP 201 responses required.
- The existing runner also requires all 40,000 requests to be written to the
  socket in that window. The acceptance calculation and deadline are unchanged.
- The optimized workload uses three h2c connections with 64 streams each
  (192 workers); the original workload used 15 connections with 32 streams
  each (480 workers). The load generator uses `GOMAXPROCS=2`; the Gateway uses
  `GOMAXPROCS=1`. The generator's setting does not increase the Gateway quota.
- A real create-session request warms each connection before measurement;
  warmup requests are excluded from the totals.

## Final results

Five consecutive runs on the final source and runtime configuration:

| Run | Sent | Success TPS | Failed | p50 (ms) | p95 (ms) | p99 (ms) | targetMet |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| 1 | 40,000 | 40,000 | 0 | 46.759 | 63.571 | 65.555 | true |
| 2 | 40,000 | 40,000 | 0 | 25.486 | 59.180 | 62.421 | true |
| 3 | 40,000 | 40,000 | 0 | 34.608 | 78.479 | 89.116 | true |
| 4 | 40,000 | 40,000 | 0 | 35.973 | 91.087 | 94.346 | true |
| 5 | 40,000 | 40,000 | 0 | 55.363 | 65.689 | 67.654 | true |

The original 40k workload returned 20,874 successful TPS and 19,126 timeouts.
With the source/runtime optimizations but the original 15-connection workload,
one diagnostic run reached 38,337 successful TPS; `targetMet` was still false
because only 38,817 requests reached the socket. Thus both Gateway changes and
load-generator tuning contribute to the final result.

An additional five-second measurement completed 175,000/175,000 requests with
zero failures and 35,000 successful TPS; p50/p95/p99 were 5.915/12.182/15.076 ms.
This check used the optimized three-connection workload before pinning the
generator to two Go threads. It is a five-second check, not a long soak test.

TPS is successes divided by the configured measurement window, not by the
shorter actual completion time. Latencies include time queued for a worker.
The runner dispatches jobs in the first 85% of the window; no pacing change was
made. Only successful, in-window responses contribute latency samples.

After the final runs, the Gateway container's measured memory peak was
63,647,744 bytes (60.7 MiB). Its resource endpoint reported 62.2 MiB of Go
runtime memory (`runtime.MemStats.Sys`, which differs from container memory).
The settled pprof goroutine count was 24.

## Cause and changes

The original Gateway saturated its one-vCPU quota. Adding PDU replicas did
not help because the Gateway still performed the same per-request HTTP/2 work.
The original five-second CPU profile spent 26.1% of CPU samples in syscalls,
mostly socket writes; allocation/GC and goroutine scheduling also consumed CPU.

1. Added a 32 KiB per-connection write buffer on the Gateway's downstream and
   data-plane upstream connections. Small HTTP/2 HEADERS/DATA writes share a
   socket write. A 250 microsecond timer drains partial batches. Buffer capacity
   is fixed; a flush failure closes the socket to unblock readers. Close drains
   accepted writes with a 10 ms deadline so shutdown cannot hang behind a peer
   that is not reading. The control-plane discovery transport is unchanged.
2. Reused upstream response-header value slices rather than allocating one
   slice per copied header, and avoided redundant canonicalization when deleting
   known hop-by-hop header names.
3. Set the main Compose Gateway to `GOMAXPROCS=1`, `GOGC=800`, and
   `GOMEMLIMIT=768MiB`. Go's cgroup-aware default rounds a CPU quota below two up
   to two Go processors; explicitly selecting one avoids that mismatch here.
   Less frequent GC trades additional memory for CPU headroom. `GOMEMLIMIT` is
   a soft Go runtime limit; the one-GiB container limit is still enforced.
4. Tuned the workload to three connections with 64 streams and two Go threads
   in the generator. More connections/streams were not automatically faster.

The optimized five-second profile spent 12.56% of CPU samples in syscalls.
These profiles use different offered loads (40k/s before, 35k/s after), so their
percentages are diagnostic evidence rather than an isolated comparison of just
the buffering change. Batching adds a small timer delay at low load; the legacy
15k test still passed with zero failures, with p50/p95/p99 of 2.007/3.250/4.582 ms.
The timer interval is not a hard latency ceiling under CPU load.

## Reproduce

```sh
ACCESS_LOG_ENABLED=false LOG_ENABLED=false docker compose up --build -d --scale pdu-session=5
go run ./scripts/h2c-smoke.go http://localhost:18080/health 5
make load-test-35k
```

The smoke check waits until all five PDU replicas are observed. Immediately
benchmarking a newly restarted Gateway can otherwise return 503 until discovery
and health checks publish its backends.

`LOAD_TEST_35K_ARGS` and `LOAD_TEST_35K_GOMAXPROCS` override the new Make target.
`GATEWAY_GOMAXPROCS`, `GATEWAY_GOGC`, and `GATEWAY_GOMEMLIMIT` override the Compose
runtime settings. The old `make load-test` retains its 15k acceptance workload.

Validation: `go test -race ./...`, `go vet ./...`, format checks, h2c smoke with
five replicas, and ten repeated race-enabled Gateway test suites (including
goleak). New connection tests cover data integrity, bounded buffering, timed
flushes, draining on close, blocked-write shutdown, deadlines, and propagation
of asynchronous write failures.

## Maximum TPS follow-up

Tested the same running configuration without changing source, Gateway limits,
PDU count, logging, connections, streams, or generator runtime settings.
For this sweep, `min-success` equals the offered request count: a pass means
every request was sent and completed successfully within the window.

One-second sweep and boundary repeats:

| Offered requests | Successful TPS observed | Full passes / runs | Notes |
| --- | --- | --- | --- |
| 40,000 | 40,000 | 1 / 1 | No errors; earlier acceptance also passed five times. |
| 42,000 | 42,000 | 3 / 3 | No errors; p99 88.396–114.741 ms. |
| 43,000 | 41,346–43,000 | 1 / 3 | Near the cutoff; some runs timed out. |
| 44,000 | 41,697–44,000 | 1 / 3 | Some runs timed out. |
| 45,000 | 41,100–45,000 | 1 / 4 | One initial full pass, three subsequent failures. |
| 47,000 | 43,504–44,213 | 0 / 3 | Saturated. |
| 50,000 | 43,662 | 0 / 1 | 6,338 timeout/cutoff failures. |
| 60,000 | 42,492 | 0 / 1 | 17,508 timeout/cutoff failures. |
| 80,000 | 45,252 | 0 / 1 | Highest observed one-second success count, but 34,748 failures. |

Longer measurement windows:

| Offered TPS | Window | Successful requests | Successful TPS | Failures | p99 (ms) |
| ---: | --- | --- | --- | --- | --- |
| 40,000 | 5 s | 200,000 / 200,000 | 40,000 | 0 | 287.411 |
| 42,000 | 5 s, run 1 | 210,000 / 210,000 | 42,000 | 0 | 645.362 |
| 42,000 | 5 s, run 2 | 210,000 / 210,000 | 42,000 | 0 | 561.800 |
| 42,000 | 5 s, run 3 | 210,000 / 210,000 | 42,000 | 0 | 565.425 |
| 45,000 | 5 s | 216,685 / 225,000 | 43,337 | 8,315 | 905.242 |
| 50,000 | 5 s | 215,591 / 250,000 | 43,118.2 | 34,409 | 1,325.727 |

The highest zero-error level repeated successfully in this sweep is **42k TPS**.
The observed single-window peak is **45,252 success TPS**, with overload errors;
it is not a demonstrated zero-error operating rate. A 40k target leaves more
headroom than 42k. Near saturation, queueing increases tail latency considerably.
These are short local windows with the runner's existing 85% dispatch pacing,
not a long-duration soak or a theoretical maximum across all possible tuning.

After the sweep the Gateway remained at one vCPU/one GiB and five healthy PDU
replicas. Settled pprof goroutines: 24; container memory snapshot: 44.01 MiB.
Raw repeated-run JSON files are in `/tmp/gateway-max-tps.xr9BYg` locally.

```sh
GOMAXPROCS=2 go run ./cmd/loadtest \
  -target http://localhost:18080/nsmf-pdusession/v1/sm-contexts \
  -requests 210000 -min-success 210000 -duration 5s \
  -connections 3 -streams 64 -request-timeout 3s
```
