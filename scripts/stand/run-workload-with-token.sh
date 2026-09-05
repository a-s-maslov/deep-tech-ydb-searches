#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="${SEARCH_WORKLOAD_REAL_BIN:-${ROOT}/bin/search-workload}"
DATASET_CONFIG="${DATASET_CONFIG:-${ROOT}/config/datasets.json}"
ACTIVE_DATASET_FILE="${ACTIVE_DATASET_FILE:-${ROOT}/.runtime/active-dataset}"
YDB_CLI="${YDB_CLI:-ydb}"
YDB_PROFILE="${YDB_PROFILE:-}"

[[ -x "${BIN}" ]] || { echo "workload binary not found: ${BIN}" >&2; exit 2; }
command -v "${YDB_CLI}" >/dev/null 2>&1 || {
  echo "YDB CLI not found: ${YDB_CLI}" >&2
  exit 2
}
[[ -n "${YDB_PROFILE}" ]] || {
  echo "YDB_PROFILE is required" >&2
  exit 2
}

# The token exists only in this process environment and is refreshed for every
# prepare/run invocation. It is never written to the workload configuration.
YDB_TOKEN="$("${YDB_CLI}" --profile "${YDB_PROFILE}" auth get-token --force)"
[[ -n "${YDB_TOKEN}" ]] || { echo "YDB CLI returned an empty token" >&2; exit 1; }
export YDB_TOKEN

arguments=("$@")
has_dataset=false
for argument in "${arguments[@]}"; do
  [[ "${argument}" == "-dataset-profile" || "${argument}" == -dataset-profile=* ]] && has_dataset=true
done
if [[ "${has_dataset}" == false && -r "${ACTIVE_DATASET_FILE}" ]]; then
  dataset_profile="$(tr -d '[:space:]' < "${ACTIVE_DATASET_FILE}")"
  [[ -n "${dataset_profile}" ]] || { echo "active dataset marker is empty" >&2; exit 2; }
  arguments=(
    -dataset-config "${DATASET_CONFIG}"
    -dataset-profile "${dataset_profile}"
    "${arguments[@]}"
  )
fi

exec "${BIN}" "${arguments[@]}"
