#!/usr/bin/env bash
# One-command state transitions for the workshop demo.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Let flock remain the parent of this invocation and close its descriptor in
# the child. Workload and observer daemons must not inherit the demo lock.
if command -v flock >/dev/null 2>&1 && [[ "${DEMO_LOCK_HELD:-}" != 1 ]]; then
  mkdir -p "${ROOT}/.runtime"
  if flock -n -E 75 -o "${ROOT}/.runtime/demo.lock" \
      env DEMO_LOCK_HELD=1 bash "${BASH_SOURCE[0]}" "$@"; then
    exit 0
  else
    rc=$?
    (( rc == 75 )) && echo "another demo command is running" >&2
    exit "${rc}"
  fi
fi

CHAOS_DIR="${DEMO_CHAOS_DIR:-$(cd "${ROOT}/.." 2>/dev/null && pwd)/chaos-md}"
CONFIG="${DEMO_WORKLOAD_CONFIG:-${ROOT}/config/workload.stand.json}"
DATASET="${DEMO_DATASET_PROFILE:-scale-1m}"
BIN="${DEMO_WORKLOAD_BIN:-${ROOT}/bin/search-workload}"
BASE_NODES="${DEMO_BASE_NODES:-3}"
SCALE_NODES="${DEMO_SCALE_NODES:-9}"
NODE_WAIT="${DEMO_NODE_WAIT_SECONDS:-90}"
START_WAIT="${DEMO_WORKLOAD_START_WAIT_SECONDS:-30}"
FAULT_SAFETY_TIMEOUT="${DEMO_FAULT_SAFETY_TIMEOUT_SECONDS:-${DEMO_FAULT_TIMEOUT_SECONDS:-600}}"
FAULT_DISK="${DEMO_FAULT_DISK:-vdb}"
CHECK_HOST="${DEMO_FAILURE_CHECK_HOST:-ydb-s1}"
WARMUP_MIN="${DEMO_WARMUP_MIN_SECONDS:-300}"
WARMUP_TIMEOUT="${DEMO_WARMUP_TIMEOUT_SECONDS:-1200}"
WARMUP_INTERVAL="${DEMO_WARMUP_INTERVAL_SECONDS:-30}"
WARMUP_TARGET_RATIO="${DEMO_WARMUP_TARGET_RATIO:-0.95}"
WARMUP_STABLE_SAMPLES="${DEMO_WARMUP_STABLE_SAMPLES:-3}"
DEMO_YQL_DIR="${DEMO_YQL_DIR:-${ROOT}/.runtime/demo-yql}"
DEMO_QUERY_ID="${DEMO_QUERY_ID:-0}"
DRY_RUN=false
CONFIRMED=false

usage() {
  cat <<'EOF'
Usage:
  scripts/demo.sh [--dry-run] bootstrap --yes
  scripts/demo.sh [--dry-run] prepare|preflight|status|stop|recover|scripts
  scripts/demo.sh [--dry-run] stage overview|fulltext-limit|capacity-base|capacity-demand|resilience
  scripts/demo.sh [--dry-run] action fulltext-split|scale-9|partitions
  scripts/demo.sh [--dry-run] fault process|server|disk|dc
  scripts/demo.sh [--dry-run] restore process|server|disk|dc

bootstrap --yes  build artifacts, recreate the workshop table, build indexes,
                 warm their topology and prepare the initial demo state
prepare          return an existing dataset to the initial demo state
preflight        read-only checks immediately before the demo
status           concise workload, nodes, observer and partition status
recover          stop workload and restore process, server, disk and DC failures
scripts          regenerate browser YQL from the same builders as the workload
EOF
}

POSITIONAL=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run|-n) DRY_RUN=true; shift ;;
    --yes) CONFIRMED=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) POSITIONAL+=("$1"); shift ;;
  esac
done

COMMAND="${POSITIONAL[0]:-}"
SUBCOMMAND="${POSITIONAL[1]:-}"
(( ${#POSITIONAL[@]} <= 2 )) || { echo "too many arguments: ${POSITIONAL[*]}" >&2; exit 2; }
[[ -n "${COMMAND}" ]] || { usage >&2; exit 2; }
[[ -d "${CHAOS_DIR}" ]] || { echo "chaos-md not found: ${CHAOS_DIR}" >&2; exit 2; }
[[ -f "${CONFIG}" ]] || { echo "workload config not found: ${CONFIG}" >&2; exit 2; }

readarray -t SCHEMA_NAMES < <(python3 -c '
import json, sys
c = json.load(open(sys.argv[1], encoding="utf-8"))
print(c["table_path"].strip("/"))
print(c["fulltext_index"])
print(c["vector_index"])
' "${CONFIG}")
TABLE_PATH="${SCHEMA_NAMES[0]}"
FULLTEXT_INDEX="${SCHEMA_NAMES[1]}"
VECTOR_INDEX="${SCHEMA_NAMES[2]}"

mkdir -p "${ROOT}/.runtime"

step() { printf '\n==> %s\n' "$*"; }

print_cmd() {
  printf '+'
  printf ' %q' "$@"
  printf '\n'
}

run() {
  print_cmd "$@"
  [[ "${DRY_RUN}" == true ]] || "$@"
}

load_workload_env() {
  local env_file="${CHAOS_DIR}/workload/env.local.sh"
  [[ -f "${env_file}" ]] || { echo "workload environment not found: ${env_file}" >&2; return 2; }
  set -a
  # shellcheck disable=SC1090
  source "${env_file}"
  set +a
}

chaos_workload() {
  local args=(bash workload/manage.sh --type search "$@")
  [[ "${DRY_RUN}" == false ]] || args+=(--dry-run)
  (cd "${CHAOS_DIR}" && run "${args[@]}")
}

set_nodes() {
  local count="$1"
  local args=(bash cluster/dynamic-nodes.sh set "${count}" --wait "${NODE_WAIT}")
  [[ "${DRY_RUN}" == false ]] || args+=(--dry-run)
  (cd "${CHAOS_DIR}" && run "${args[@]}")
}

node_status() {
  (cd "${CHAOS_DIR}" && bash cluster/dynamic-nodes.sh status)
}

check_node_control() {
  (cd "${CHAOS_DIR}" && run bash cluster/dynamic-nodes.sh status)
}

assert_nodes() {
  [[ "${DRY_RUN}" == false ]] || return 0
  local expected="$1" output
  output="$(node_status)"
  printf '%s\n' "${output}"
  grep -q "active dynamic nodes: ${expected}/" <<<"${output}" || {
    echo "expected ${expected} active dynamic nodes" >&2
    return 1
  }
}

workload_status() {
  local rc=0
  (cd "${CHAOS_DIR}" && bash workload/manage.sh --type search status) || rc=$?
  return "${rc}"
}

assert_profile() {
  [[ "${DRY_RUN}" == false ]] || return 0
  local expected="$1" output
  output="$(workload_status)" || { printf '%s\n' "${output}"; return 1; }
  printf '%s\n' "${output}"
  grep -q "profile=${expected}" <<<"${output}" || {
    echo "expected running workload profile ${expected}" >&2
    return 1
  }
}

switch_profile() {
  local profile="$1"
  step "Switch workload to ${profile}"
  chaos_workload stop
  chaos_workload --profile "${profile}" start --wait "${START_WAIT}"
  [[ "${DRY_RUN}" == true ]] || assert_profile "${profile}"
}

raw_partitions() {
  (cd "${CHAOS_DIR}" && bash workload/manage.sh --type search action partitions)
}

partition_summary_from_stdin() {
  python3 -c '
import json, sys
raw = sys.stdin.read()
start = raw.find("[")
if start < 0:
    raise SystemExit("partition JSON not found")
stats = json.loads(raw[start:])
suffixes = {
    "main": sys.argv[1].strip("/"),
    "fulltext": f"/{sys.argv[2]}/indexImplDocsTable",
    "vector": f"/{sys.argv[3]}/indexImplPostingTable",
}
result = {}
for item in stats:
    path = item["path"]
    for name, suffix in suffixes.items():
        if path.endswith(suffix):
            result[name] = item["partitions"]
print(" ".join(f"{name}={result.get(name, 0)}" for name in ("main", "fulltext", "vector")))
' "${TABLE_PATH}" "${FULLTEXT_INDEX}" "${VECTOR_INDEX}"
}

partition_summary() {
  [[ "${DRY_RUN}" == false ]] || { echo "main=<dry-run> fulltext=<dry-run> vector=<dry-run>"; return 0; }
  raw_partitions | partition_summary_from_stdin
}

workload_sample() {
  local metrics_url="${SEARCH_WORKLOAD_METRICS_URL:-http://127.0.0.1:19091/metrics}"
  curl -fsS --max-time 3 "${metrics_url}" | python3 -c '
import re, sys

ratio = float(sys.argv[1])
required = ("fulltext", "vector", "hybrid", "dml_check")
values = {}
for line in sys.stdin:
    match = re.match(r"^(ydb_workload_[a-zA-Z0-9_]+)\{([^}]*)\}\s+([-+0-9.eE]+)$", line.strip())
    if not match:
        continue
    metric, raw_labels, raw_value = match.groups()
    labels = dict(re.findall(r"([a-zA-Z_][a-zA-Z0-9_]*)=\"([^\"]*)\"", raw_labels))
    scenario = labels.get("scenario")
    if scenario:
        values[(metric, scenario)] = float(raw_value)

ready = True
parts = []
for scenario in required:
    actual = values.get(("ydb_workload_rps", scenario))
    target = values.get(("ydb_workload_target_rps", scenario))
    if actual is None or target is None or target <= 0:
        parts.append(f"{scenario}=missing")
        ready = False
        continue
    parts.append(f"{scenario}={actual:.1f}/{target:.1f}")
    ready = ready and actual >= target * ratio

errors = sum(values.get(("ydb_workload_countError", scenario), 0) for scenario in required)
retries = sum(values.get(("ydb_workload_retries", scenario), 0) for scenario in required)
dropped = sum(values.get(("ydb_workload_dropped", scenario), 0) for scenario in required)
ready = ready and errors == 0 and retries == 0 and dropped == 0
parts.extend((f"errors={errors:.0f}", f"retries={retries:.0f}", f"dropped={dropped:.0f}", f"ready={int(ready)}"))
print(" ".join(parts))
' "${WARMUP_TARGET_RATIO}"
}

fulltext_partition_count() {
  partition_summary | sed -n 's/.*fulltext=\([0-9][0-9]*\).*/\1/p'
}

assert_single_fulltext_partition() {
  [[ "${DRY_RUN}" == false ]] || return 0
  local count
  count="$(fulltext_partition_count)"
  [[ "${count}" == 1 ]] || {
    echo "expected one full-text partition, found ${count:-unknown}; run prepare" >&2
    return 1
  }
}

generate_scripts() {
  step "Generate browser YQL from workload query builders"
  run "${BIN}" -config "${CONFIG}" -dataset-config "${ROOT}/config/datasets.json" \
    -dataset-profile "${DATASET}" -demo-output "${DEMO_YQL_DIR}" -demo-query-id "${DEMO_QUERY_ID}" demo-scripts
  verify_demo_queries
}

verify_demo_queries() {
  step "Execute the exact browser-demo YQL through the Table API"
  load_workload_env
  run "${ROOT}/scripts/stand/run-workload-with-token.sh" \
    -config "${CONFIG}" -dataset-config "${ROOT}/config/datasets.json" \
    -dataset-profile "${DATASET}" -demo-query-id "${DEMO_QUERY_ID}" demo-check
}

clean_dml() {
  step "Remove the reserved DML range"
  load_workload_env
  run "${ROOT}/scripts/stand/run-workload-with-token.sh" \
    -config "${CONFIG}" -dataset-config "${ROOT}/config/datasets.json" \
    -dataset-profile "${DATASET}" -batch-size 5000 clean-dml
}

observer() {
  local command="$1"
  run bash "${ROOT}/scripts/stand/manage-observer.sh" "${command}"
}

prepare_state() {
  step "Build the current Go workload"
  run bash "${ROOT}/scripts/stand/build-workload.sh"
  step "Stop previous workload"
  chaos_workload stop
  clean_dml
  step "Restore the three-node initial topology"
  set_nodes "${BASE_NODES}"
  step "Enable elastic partitioning before rebuilding the full-text index"
  chaos_workload action partition-elastic all
  step "Rebuild and pin the full-text index at one partition"
  chaos_workload action reset-fulltext
  chaos_workload action partition-fixed fulltext
  step "Check schema and all search branches"
  chaos_workload prepare
  generate_scripts
  step "Refresh observer token and metrics"
  observer restart
  observer status
  if [[ "${DRY_RUN}" == false ]]; then
    local count
    count="$(fulltext_partition_count)"
    [[ "${count}" == 1 ]] || {
      echo "full-text index has ${count:-unknown} partitions after prepare, expected 1" >&2
      return 1
    }
    echo "partitions: $(partition_summary)"
  fi
}

preflight() {
  step "Dataset artifacts"
  run bash "${ROOT}/scripts/dataset.sh" status "${DATASET}"
  step "Schema and search checks"
  chaos_workload prepare
  verify_demo_queries
  step "Dynamic nodes"
  assert_nodes "${BASE_NODES}"
  step "Observer"
  observer status
  step "Failure permissions (read-only)"
  (cd "${CHAOS_DIR}" && run bash 09-proc-kill.sh -C "${CHECK_HOST}")
  (cd "${CHAOS_DIR}" && run bash 12-server-stop.sh -C "${CHECK_HOST}")
  (cd "${CHAOS_DIR}" && run bash 03-disk-fail.sh -C "${CHECK_HOST}" -d "${FAULT_DISK}")
  (cd "${CHAOS_DIR}" && run bash 12-server-stop.sh -4 -C)
  assert_single_fulltext_partition
  [[ "${DRY_RUN}" == true ]] || echo "partitions: $(partition_summary)"
  echo "preflight: OK"
}

warm_topology() {
  step "Warm table and index topology on ${SCALE_NODES} nodes"
  set_nodes "${SCALE_NODES}"
  chaos_workload action partition-elastic all
  switch_profile elasticity-demand
  [[ "${DRY_RUN}" == false ]] || { echo "+ wait for stable partitions"; return 0; }

  local started now signature previous="" sample sample_ready stable=0
  started="$(date +%s)"
  while true; do
    workload_status >/dev/null
    signature="$(partition_summary)"
    if sample="$(workload_sample 2>&1)"; then
      sample_ready="$(sed -n 's/.*ready=\([01]\).*/\1/p' <<<"${sample}")"
    else
      sample="metrics=unavailable (${sample})"
      sample_ready=0
    fi
    now="$(date +%s)"
    printf 'warmup elapsed=%ss partitions: %s; %s\n' "$((now - started))" "${signature}" "${sample}"
    if (( now - started >= WARMUP_MIN )); then
      if [[ "${signature}" == "${previous}" && "${sample_ready}" == 1 ]]; then
        stable=$((stable + 1))
      else
        stable=0
      fi
      (( stable >= WARMUP_STABLE_SAMPLES )) && break
    fi
    (( now - started < WARMUP_TIMEOUT )) || {
      chaos_workload stop
      echo "topology did not stabilize in ${WARMUP_TIMEOUT}s" >&2
      return 1
    }
    previous="${signature}"
    sleep "${WARMUP_INTERVAL}"
  done
  chaos_workload stop
  chaos_workload action partition-elastic all
  echo "warmup: OK, ${signature}; ${sample}"
}

bootstrap() {
  [[ "${CONFIRMED}" == true ]] || {
    echo "bootstrap recreates the configured workshop table; pass --yes" >&2
    return 2
  }
  step "Stop previous workload"
  chaos_workload stop
  step "Build the Go workload"
  run bash "${ROOT}/scripts/stand/build-workload.sh"
  step "Build or verify the ${DATASET} runtime artifacts"
  if [[ "${DRY_RUN}" == true ]]; then
    print_cmd bash "${ROOT}/scripts/dataset.sh" build "${DATASET}"
  elif ! bash "${ROOT}/scripts/dataset.sh" status "${DATASET}"; then
    bash "${ROOT}/scripts/dataset.sh" build "${DATASET}"
  fi
  step "Verify SSH control of dynamic nodes"
  check_node_control
  load_workload_env
  step "Recreate the workshop table and indexes"
  run bash "${ROOT}/scripts/dataset.sh" activate "${DATASET}" --reset
  warm_topology
  prepare_state
  echo "bootstrap: OK"
}

stage() {
  case "${SUBCOMMAND}" in
    overview)
      assert_single_fulltext_partition
      step "Prepare the three-node overview"
      chaos_workload stop
      set_nodes "${BASE_NODES}"
      chaos_workload action partition-fixed fulltext
      chaos_workload --profile overview start --wait "${START_WAIT}"
      [[ "${DRY_RUN}" == true ]] || assert_profile overview
      ;;
    fulltext-limit)
      assert_single_fulltext_partition
      step "Keep the full-text index fixed at one partition"
      chaos_workload stop
      chaos_workload action partition-fixed fulltext
      chaos_workload --profile fulltext-partition start --wait "${START_WAIT}"
      [[ "${DRY_RUN}" == true ]] || assert_profile fulltext-partition
      ;;
    capacity-base)
      step "Prepare three-node elastic capacity"
      chaos_workload stop
      set_nodes "${BASE_NODES}"
      chaos_workload action partition-elastic all
      chaos_workload --profile elasticity-base start --wait "${START_WAIT}"
      [[ "${DRY_RUN}" == true ]] || assert_profile elasticity-base
      ;;
    capacity-demand)
      assert_nodes "${BASE_NODES}"
      assert_profile elasticity-base
      switch_profile elasticity-demand
      ;;
    resilience)
      step "Keep the scaled workload running for failure scenarios"
      assert_nodes "${SCALE_NODES}"
      assert_profile elasticity-demand
      ;;
    *) echo "unknown stage: ${SUBCOMMAND:-<empty>}" >&2; usage >&2; return 2 ;;
  esac
}

action() {
  case "${SUBCOMMAND}" in
    fulltext-split)
      assert_profile fulltext-partition
      step "Enable load-based split without restarting workload"
      chaos_workload action partition-elastic fulltext
      [[ "${DRY_RUN}" == true ]] || echo "partitions: $(partition_summary)"
      ;;
    scale-9)
      assert_profile elasticity-demand
      step "Add dynamic nodes without restarting workload"
      set_nodes "${SCALE_NODES}"
      ;;
    partitions)
      [[ "${DRY_RUN}" == true ]] && print_cmd bash workload/manage.sh --type search action partitions || echo "partitions: $(partition_summary)"
      ;;
    *) echo "unknown action: ${SUBCOMMAND:-<empty>}" >&2; usage >&2; return 2 ;;
  esac
}

fault() {
  assert_profile elasticity-demand
  assert_nodes "${SCALE_NODES}"
  case "${SUBCOMMAND}" in
    process) (cd "${CHAOS_DIR}" && run bash 09-proc-kill.sh -1 --hold -t "${FAULT_SAFETY_TIMEOUT}") ;;
    server)  (cd "${CHAOS_DIR}" && run bash 12-server-stop.sh -1 --hold -t "${FAULT_SAFETY_TIMEOUT}") ;;
    disk)    (cd "${CHAOS_DIR}" && run bash 03-disk-fail.sh -1 --hold -t "${FAULT_SAFETY_TIMEOUT}" -d "${FAULT_DISK}") ;;
    dc)      (cd "${CHAOS_DIR}" && run bash 12-server-stop.sh -4 --hold -t "${FAULT_SAFETY_TIMEOUT}") ;;
    *) echo "unknown fault: ${SUBCOMMAND:-<empty>}" >&2; usage >&2; return 2 ;;
  esac
  echo "fault ${SUBCOMMAND}: ACTIVE; restore with: ./scripts/demo.sh restore ${SUBCOMMAND}"
}

restore() {
  case "${SUBCOMMAND}" in
    process) (cd "${CHAOS_DIR}" && run bash 09-proc-kill.sh -1 -D) ;;
    server)  (cd "${CHAOS_DIR}" && run bash 12-server-stop.sh -1 -D) ;;
    disk)    (cd "${CHAOS_DIR}" && run bash 03-disk-fail.sh -1 -D -d "${FAULT_DISK}") ;;
    dc)      (cd "${CHAOS_DIR}" && run bash 12-server-stop.sh -4 -D) ;;
    *) echo "unknown restore target: ${SUBCOMMAND:-<empty>}" >&2; usage >&2; return 2 ;;
  esac
  step "Verify search after recovery"
  chaos_workload prepare
  [[ "${DRY_RUN}" == true ]] || assert_profile elasticity-demand
}

recover() {
  step "Stop workload"
  chaos_workload stop
  step "Restore DC, disk, server services and process state"
  (cd "${CHAOS_DIR}" && run bash 12-server-stop.sh -4 -D)
  (cd "${CHAOS_DIR}" && run bash 03-disk-fail.sh -1 -D -d "${FAULT_DISK}")
  (cd "${CHAOS_DIR}" && run bash 12-server-stop.sh -1 -D)
  (cd "${CHAOS_DIR}" && run bash 09-proc-kill.sh -1 -D)
  step "Return to three dynamic nodes and elastic partitioning"
  set_nodes "${BASE_NODES}"
  chaos_workload action partition-elastic all
  observer restart
  chaos_workload prepare
  echo "recover: OK; run prepare to recreate the one-partition full-text start"
}

status() {
  step "Workload"
  workload_status || true
  step "Dynamic nodes"
  node_status
  step "Observer"
  observer status || true
  step "Partitions"
  echo "$(partition_summary)"
}

case "${COMMAND}" in
  bootstrap) bootstrap ;;
  prepare) prepare_state ;;
  preflight) preflight ;;
  scripts) generate_scripts ;;
  stage) stage ;;
  action) action ;;
  fault) fault ;;
  restore) restore ;;
  status) status ;;
  stop) chaos_workload stop ;;
  recover) recover ;;
  *) echo "unknown command: ${COMMAND}" >&2; usage >&2; exit 2 ;;
esac
