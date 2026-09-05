#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

grep -q 'flock -n -E 75 -o' "${ROOT}/scripts/demo.sh"
if grep -q 'exec 9>' "${ROOT}/scripts/demo.sh"; then
  echo "demo lock descriptor would leak into background processes" >&2
  exit 1
fi
grep -q 'SEARCH_WORKLOAD_METRICS_URL' "${ROOT}/scripts/dataset.sh"
if grep -q '19091/health' "${ROOT}/scripts/dataset.sh"; then
  echo "dataset activation still probes a non-existent health endpoint" >&2
  exit 1
fi
grep -q '127.0.0.1:19091/metrics' "${ROOT}/scripts/demo.sh"

mkdir -p "${TMP}/chaos-md/workload"
printf 'YDB_CLI=ydb\nYDB_PROFILE=test\n' >"${TMP}/chaos-md/workload/env.local.sh"

demo() {
  DEMO_CHAOS_DIR="${TMP}/chaos-md" \
  DEMO_WORKLOAD_CONFIG="${ROOT}/config/workload.stand.example.json" \
  DEMO_WORKLOAD_BIN="${TMP}/search-workload" \
    bash "${ROOT}/scripts/demo.sh" --dry-run "$@"
}

overview="$(demo stage overview)"
grep -q 'profile overview' <<<"${overview}"
grep -q 'workload/manage.sh' <<<"${overview}"
grep -q 'dynamic-nodes.sh set 3' <<<"${overview}"
grep -q 'partition-fixed fulltext' <<<"${overview}"

split="$(demo action fulltext-split)"
grep -q 'partition-elastic fulltext' <<<"${split}"

base="$(demo stage capacity-base)"
grep -q 'dynamic-nodes.sh set 3' <<<"${base}"
grep -q 'partition-elastic all' <<<"${base}"
grep -q 'profile elasticity-base' <<<"${base}"

demand="$(demo stage capacity-demand)"
grep -q 'profile elasticity-demand' <<<"${demand}"

scale="$(demo action scale-9)"
grep -q 'dynamic-nodes.sh set 9' <<<"${scale}"

resilience="$(demo stage resilience)"
grep -q 'Keep the scaled workload running' <<<"${resilience}"

prepare="$(demo prepare)"
grep -q 'reset-fulltext' <<<"${prepare}"
grep -q 'demo-scripts' <<<"${prepare}"
grep -q 'demo-check' <<<"${prepare}"
grep -q 'manage-observer.sh restart' <<<"${prepare}"

bootstrap="$(demo bootstrap --yes)"
grep -q 'dataset.sh build scale-1m' <<<"${bootstrap}"
grep -q 'dynamic-nodes.sh status' <<<"${bootstrap}"
grep -q 'dataset.sh activate scale-1m --reset' <<<"${bootstrap}"
grep -q 'wait for stable partitions' <<<"${bootstrap}"
grep -q 'workload/manage.sh --type search stop' <<<"${bootstrap}"

recovery="$(demo recover)"
grep -q '03-disk-fail.sh -1 -D' <<<"${recovery}"
grep -q '12-server-stop.sh -4 -D' <<<"${recovery}"
grep -q 'dynamic-nodes.sh set 3' <<<"${recovery}"

fault="$(demo fault process)"
grep -q '09-proc-kill.sh -1 --hold -t 600' <<<"${fault}"
server_fault="$(demo fault server)"
grep -q '12-server-stop.sh -1 --hold -t 600' <<<"${server_fault}"
disk_fault="$(demo fault disk)"
grep -q '03-disk-fail.sh -1 --hold -t 600 -d vdb' <<<"${disk_fault}"
dc_fault="$(demo fault dc)"
grep -q '12-server-stop.sh -4 --hold -t 600' <<<"${dc_fault}"

process_restore="$(demo restore process)"
grep -q '09-proc-kill.sh -1 -D' <<<"${process_restore}"
server_restore="$(demo restore server)"
grep -q '12-server-stop.sh -1 -D' <<<"${server_restore}"
disk_restore="$(demo restore disk)"
grep -q '03-disk-fail.sh -1 -D -d vdb' <<<"${disk_restore}"
dc_restore="$(demo restore dc)"
grep -q '12-server-stop.sh -4 -D' <<<"${dc_restore}"

preflight="$(demo preflight)"
grep -q 'demo-check' <<<"${preflight}"
grep -q '09-proc-kill.sh -C ydb-s1' <<<"${preflight}"
grep -q '12-server-stop.sh -C ydb-s1' <<<"${preflight}"
grep -q '03-disk-fail.sh -C ydb-s1 -d vdb' <<<"${preflight}"
grep -q '12-server-stop.sh -4 -C' <<<"${preflight}"

if demo stage unknown >/dev/null 2>&1; then
  echo "unknown stage unexpectedly succeeded" >&2
  exit 1
fi

echo "demo control smoke: OK"
