#!/usr/bin/env bash
# Usage: bash scripts/benchmark-pdu-matrix.sh [output-directory [PDU-count ...]]
# Run from any directory. Restores the original replica count, even on failure.
set -euo pipefail
cd "$(dirname "$0")/.."

artifact_dir=${1:-docs/benchmarks/50k-pdu-2026-10-08}
replica_counts=(1 3 5 10)
if (( $# > 1 )); then
  replica_counts=("${@:2}")
fi
for replicas in "${replica_counts[@]}"; do
  if [[ ! "$replicas" =~ ^[1-9][0-9]*$ ]]; then
    printf 'PDU count must be a positive integer: %s\n' "$replicas" >&2
    exit 1
  fi
done
mkdir -p "$artifact_dir"
binary_dir=$(mktemp -d /tmp/gateway-pdu-matrix.XXXXXX)
go build -o "$binary_dir/loadtest" ./cmd/loadtest

gateway_id=$(docker compose ps -q gateway)
initial_replicas=$(docker compose ps --status running -q pdu-session | wc -l | tr -d '[:space:]')
if [[ -z "$gateway_id" || "$initial_replicas" -lt 1 ]]; then
  printf 'Start Gateway and PDU containers before running the matrix.\n' >&2
  exit 1
fi
gateway_scope="/sys/fs/cgroup/system.slice/docker-$gateway_id.scope"

wait_membership() {
  local expected=$1 observed
  for ((attempt=0; attempt<60; attempt++)); do
    observed=$(curl --http2-prior-knowledge -fsS http://localhost:18080/gateway/backends | jq -r .count) || observed=unavailable
    if [[ "$observed" == "$expected" ]]; then
      printf 'Healthy backends: %s\n' "$observed"
      return 0
    fi
    sleep 1
  done
  printf 'Membership timeout: observed %s, expected %s\n' "$observed" "$expected" >&2
  return 1
}

restore_topology() {
  local result=$?
  trap - EXIT
  printf '\nRestoring %s PDU replicas.\n' "$initial_replicas"
  if ! ACCESS_LOG_ENABLED=false LOG_ENABLED=false docker compose up -d --no-recreate --scale "pdu-session=$initial_replicas"; then
    result=1
  elif ! wait_membership "$initial_replicas"; then
    result=1
  fi
  exit "$result"
}
trap restore_topology EXIT

docker inspect "$gateway_id" | jq '.[0] | {
  gatewayId: .Id,
  imageId: .Image,
  nanoCpus: .HostConfig.NanoCpus,
  memoryLimitBytes: .HostConfig.Memory,
  memorySwapLimitBytes: .HostConfig.MemorySwap,
  runtime: [.Config.Env[] | select(test("^(GOMAXPROCS|GOGC|GOMEMLIMIT|ACCESS_LOG_ENABLED|LOG_ENABLED)="))]
}' > "$artifact_dir/environment.json"

cpu_usage_usec() {
  if [[ -r "$gateway_scope/cpu.stat" ]]; then
    awk '$1 == "usage_usec" {print $2}' "$gateway_scope/cpu.stat"
  else
    printf '0\n'
  fi
}

results=()
for replicas in "${replica_counts[@]}"; do
  printf '\nTopology: one Gateway + %s PDU replicas\n' "$replicas"
  ACCESS_LOG_ENABLED=false LOG_ENABLED=false docker compose up -d --no-recreate --scale "pdu-session=$replicas"
  wait_membership "$replicas"
  if [[ "$(docker compose ps -q gateway)" != "$gateway_id" ]]; then
    printf 'Gateway was recreated; comparison would be invalid.\n' >&2
    exit 1
  fi

  for run in 1 2 3; do
    result_file="$artifact_dir/pdu-$replicas-run-$run.result.json"
    metadata_file="$artifact_dir/pdu-$replicas-run-$run.metadata.json"
    measured_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    cpu_before=$(cpu_usage_usec)
    wall_before=$(date +%s%N)
    if GOMAXPROCS=2 "$binary_dir/loadtest" \
      -target http://localhost:18080/nsmf-pdusession/v1/sm-contexts \
      -requests 50000 -min-success 35000 -duration 1s \
      -connections 3 -streams 64 -request-timeout 3s \
      > "$result_file" 2> "$artifact_dir/pdu-$replicas-run-$run.stderr.log"; then
      exit_code=0
    else
      exit_code=$?
    fi
    wall_after=$(date +%s%N)
    cpu_after=$(cpu_usage_usec)
    # A failed acceptance target is expected under overload; a failed warmup is not.
    jq -e '.targetRequests == 50000 and .totalRequests == 50000 and has("latencyP90Millis")' "$result_file" > /dev/null
    memory_current=null
    memory_peak=null
    if [[ -r "$gateway_scope/memory.current" ]]; then
      memory_current=$(< "$gateway_scope/memory.current")
      memory_peak=$(< "$gateway_scope/memory.peak")
    fi
    jq --argjson pdu "$replicas" --argjson run "$run" --arg at "$measured_at" \
      --argjson exitCode "$exit_code" --argjson cpuBefore "$cpu_before" \
      --argjson cpuAfter "$cpu_after" --argjson wallBefore "$wall_before" \
      --argjson wallAfter "$wall_after" --argjson memory "$memory_current" \
      --argjson memoryPeak "$memory_peak" '. + {
        pduReplicas: $pdu, run: $run, measuredAtUTC: $at, exitCode: $exitCode,
        gatewayCPUPercentIncludingWarmup: (if $cpuAfter > $cpuBefore then
          100 * ($cpuAfter - $cpuBefore) * 1000 / ($wallAfter - $wallBefore) else null end),
        gatewayMemoryCurrentBytes: $memory, gatewayMemoryPeakLifetimeBytes: $memoryPeak
      }' "$result_file" > "$metadata_file"
    results+=("$metadata_file")
    jq '{pduReplicas,run,successfulTps,sentRequests,failedRequests,latencyP50Millis,latencyP90Millis,latencyP99Millis,targetMet,gatewayCPUPercentIncludingWarmup}' "$metadata_file"
    sleep 2
  done
done

jq -s 'sort_by(.pduReplicas,.run) | group_by(.pduReplicas) | map({
  pduReplicas: .[0].pduReplicas,
  runs: length,
  successfulTpsMean: (map(.successfulTps) | add / length),
  successfulTpsMin: (map(.successfulTps) | min),
  successfulTpsMax: (map(.successfulTps) | max),
  sentRequestsMean: (map(.sentRequests) | add / length),
  failedRequestsMean: (map(.failedRequests) | add / length),
  latencyP50MillisMean: (map(.latencyP50Millis) | add / length),
  latencyP90MillisMean: (map(.latencyP90Millis) | add / length),
  latencyP95MillisMean: (map(.latencyP95Millis) | add / length),
  latencyP99MillisMean: (map(.latencyP99Millis) | add / length),
  runsAtLeast35kSuccess: (map(select(.successfulTps >= 35000)) | length),
  targetMetRuns: (map(select(.targetMet)) | length),
  gatewayCPUPercentIncludingWarmupMean: (map(.gatewayCPUPercentIncludingWarmup) | map(select(. != null)) | if length > 0 then add / length else null end)
})' "${results[@]}" > "$artifact_dir/summary.json"
printf '\nMatrix summary:\n'
jq . "$artifact_dir/summary.json"
