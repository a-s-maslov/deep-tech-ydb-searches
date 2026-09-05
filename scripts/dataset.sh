#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CATALOG="${DATASET_CONFIG:-${ROOT}/config/datasets.json}"
CONFIG="${WORKLOAD_CONFIG:-${ROOT}/config/workload.stand.json}"
BIN="${SEARCH_WORKLOAD_BIN:-${ROOT}/scripts/stand/run-workload-with-token.sh}"
ACTIVE="${ACTIVE_DATASET_FILE:-${ROOT}/.runtime/active-dataset}"

workload_metrics_url() {
  if [[ -n "${DATASET_WORKLOAD_METRICS_URL:-}" ]]; then
    printf '%s\n' "${DATASET_WORKLOAD_METRICS_URL}"
    return
  fi
  if [[ -n "${SEARCH_WORKLOAD_METRICS_URL:-}" ]]; then
    printf '%s\n' "${SEARCH_WORKLOAD_METRICS_URL}"
    return
  fi
  python3 -c '
import json, sys
address = json.load(open(sys.argv[1], encoding="utf-8"))["metrics"]["listen_address"]
host, port = address.rsplit(":", 1)
if host in ("", "0.0.0.0", "::", "[::]"):
    host = "127.0.0.1"
print(f"http://{host}:{port}/metrics")
' "${CONFIG}"
}

usage() {
  cat <<EOF
Usage:
  $0 build PROFILE
  $0 status PROFILE
  $0 activate PROFILE --reset

Profiles are defined in ${CATALOG}.
EOF
}

[[ $# -ge 2 ]] || { usage >&2; exit 2; }
command_name="$1"
profile="$2"
shift 2

profile_field() {
  python3 - "${CATALOG}" "${profile}" "$1" <<'PY'
import json, pathlib, sys
path, name, field = pathlib.Path(sys.argv[1]).resolve(), sys.argv[2], sys.argv[3]
profiles = json.loads(path.read_text(encoding="utf-8")).get("profiles", {})
if name not in profiles:
    raise SystemExit(f"unknown dataset profile: {name}")
value = profiles[name][field]
if field.endswith("_file") or field == "output_dir":
    value = (path.parent / value).resolve()
print(value)
PY
}

status() {
  output="$(profile_field output_dir)"
  expected="$(profile_field size)"
  manifest="${output}/manifest.json"
  documents="$(profile_field document_file)"
  queries="$(profile_field query_file)"
  [[ -f "${manifest}" ]] || { echo "profile=${profile} artifacts=MISSING path=${output}"; return 1; }
  actual="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["documents"])' "${manifest}")"
  [[ "${actual}" == "${expected}" ]] || { echo "profile=${profile} documents=${actual} expected=${expected}"; return 1; }
  [[ -f "${documents}" && -f "${queries}" ]] || { echo "profile=${profile} runtime_files=MISSING"; return 1; }
  echo "profile=${profile} artifacts=OK documents=${actual} path=${output}"
}

case "${command_name}" in
  build)
    [[ $# -eq 0 ]] || { usage >&2; exit 2; }
    exec bash "${ROOT}/scripts/stand/prepare-runtime-data.sh" --profile "${profile}"
    ;;
  status)
    [[ $# -eq 0 ]] || { usage >&2; exit 2; }
    status
    if [[ -r "${ACTIVE}" ]]; then
      echo "active=$(tr -d '[:space:]' < "${ACTIVE}")"
    else
      echo "active=none"
    fi
    ;;
  activate)
    [[ "${1:-}" == "--reset" && $# -eq 1 ]] || {
      echo "activate is destructive and requires --reset" >&2
      exit 2
    }
    status
    [[ -f "${CONFIG}" ]] || { echo "workload config not found: ${CONFIG}" >&2; exit 2; }
    metrics_url="$(workload_metrics_url)"
    if curl -fsS --max-time 2 "${metrics_url}" >/dev/null 2>&1; then
      echo "workload metrics are responding at ${metrics_url}; stop it before changing the dataset" >&2
      exit 1
    fi
    common=(-config "${CONFIG}" -dataset-config "${CATALOG}" -dataset-profile "${profile}")
    "${BIN}" "${common[@]}" -drop init
    "${BIN}" "${common[@]}" load
    "${BIN}" "${common[@]}" indexes
    "${BIN}" "${common[@]}" -wait-timeout 60m wait
    "${BIN}" "${common[@]}" -scope all partition-elastic
    expected="$(profile_field size)"
    count_output="$("${BIN}" "${common[@]}" count)"
    actual="$(sed -n 's/^documents=//p' <<<"${count_output}" | tail -1)"
    [[ "${actual}" == "${expected}" ]] || {
      echo "YDB document count mismatch: actual=${actual:-unknown} expected=${expected}" >&2
      exit 1
    }
    echo "YDB documents=${actual}"
    "${BIN}" "${common[@]}" check
    "${BIN}" "${common[@]}" partitions
    mkdir -p "$(dirname "${ACTIVE}")"
    printf '%s\n' "${profile}" > "${ACTIVE}"
    echo "active=${profile}"
    ;;
  *) usage >&2; exit 2 ;;
esac
