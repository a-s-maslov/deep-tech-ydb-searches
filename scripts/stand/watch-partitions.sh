#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONFIG="${1:-${ROOT}/config/workload.stand.json}"
INTERVAL="${WATCH_INTERVAL:-5}"
BIN="${SEARCH_WORKLOAD_BIN:-${ROOT}/bin/search-workload}"

while true; do
  clear
  date -Is
  "${BIN}" -config "${CONFIG}" partitions
  sleep "${INTERVAL}"
done
