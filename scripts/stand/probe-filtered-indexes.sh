#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONFIG="${FILTER_PROBE_CONFIG:-${ROOT}/config/workload.stand.json}"

exec "${ROOT}/scripts/stand/run-workload-with-token.sh" \
  -config "${CONFIG}" probe-filtered
