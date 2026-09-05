#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONFIG="${1:-${ROOT}/config/workload.stand.json}"
BIN="${SEARCH_WORKLOAD_BIN:-${ROOT}/bin/search-workload}"

[[ -x "${BIN}" ]] || { echo "workload binary not found: ${BIN}" >&2; exit 2; }
[[ -f "${CONFIG}" ]] || { echo "config not found: ${CONFIG}" >&2; exit 2; }

"${BIN}" -config "${CONFIG}" -drop init
"${BIN}" -config "${CONFIG}" load
"${BIN}" -config "${CONFIG}" indexes
"${BIN}" -config "${CONFIG}" -wait-timeout 30m wait
"${BIN}" -config "${CONFIG}" -scope all partition-elastic
"${BIN}" -config "${CONFIG}" check
"${BIN}" -config "${CONFIG}" partitions
