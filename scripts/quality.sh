#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CATALOG="${DATASET_CONFIG:-${ROOT}/config/datasets.json}"
CONFIG="${WORKLOAD_CONFIG:-${ROOT}/config/workload.stand.json}"
VENV="${DATA_VENV:-${ROOT}/.venv-data}"
RUNNER="${SEARCH_WORKLOAD_BIN:-${ROOT}/scripts/stand/run-workload-with-token.sh}"

usage() {
  echo "usage: $0 exact|evaluate|all|status PROFILE" >&2
  exit 2
}

[[ $# -eq 2 ]] || usage
command_name="$1"
profile="$2"

profile_field() {
  python3 - "${CATALOG}" "${profile}" "$1" <<'PY'
import json, pathlib, sys
path, name, field = pathlib.Path(sys.argv[1]).resolve(), sys.argv[2], sys.argv[3]
profiles = json.loads(path.read_text(encoding="utf-8")).get("profiles", {})
if name not in profiles:
    raise SystemExit(f"unknown dataset profile: {name}")
if field not in profiles[name]:
    raise SystemExit(f"dataset profile {name!r} has no {field!r}")
value = profiles[name][field]
if field.endswith("_file") or field == "output_dir":
    value = (path.parent / value).resolve()
print(value)
PY
}

exact_file="$(profile_field exact_file)"
quality_file="$(profile_field quality_file)"

build_exact() {
  [[ -x "${VENV}/bin/deep-tech-data" ]] || {
    echo "data environment is missing; build the dataset profile first" >&2
    exit 1
  }
  "${VENV}/bin/python" -c 'import faiss' >/dev/null 2>&1 || {
    echo "FAISS is missing; install the quality extra:" >&2
    echo "  ${VENV}/bin/python -m pip install -e '${ROOT}[quality]'" >&2
    exit 1
  }
  "${VENV}/bin/deep-tech-data" build-exact \
    --profile "${profile}" --top-k 30 --output "${exact_file}"
}

evaluate() {
  [[ -f "${exact_file}" ]] || {
    echo "exact reference is missing: ${exact_file}" >&2
    echo "run: $0 exact ${profile}" >&2
    exit 1
  }
  "${RUNNER}" -config "${CONFIG}" -dataset-config "${CATALOG}" \
    -dataset-profile "${profile}" quality
}

show_status() {
  [[ -f "${exact_file}" ]] || { echo "exact=MISSING path=${exact_file}"; return 1; }
  echo "exact=OK path=${exact_file}"
  [[ -f "${quality_file}" ]] || { echo "quality=MISSING path=${quality_file}"; return 1; }
  python3 - "${quality_file}" <<'PY'
import gzip, json, pathlib, sys
path = pathlib.Path(sys.argv[1])
with gzip.open(path, "rt", encoding="utf-8") as stream:
    report = json.load(stream)
print(f"quality=OK path={path}")
print(f"profile={report['profile']} queries={report['query_count']} top_k={report['top_k']}")
print(f"ann_recall={report['ann_recall']:.6f}")
for method in ("exact", "fulltext", "vector", "hybrid"):
    values = report["metrics"][method]
    print(
        f"{method}: qrel_recall_micro={values['qrel_recall_micro']:.6f} "
        f"ndcg@10={values['ndcg']:.6f} hit_rate={values['hit_rate']:.6f}"
    )
PY
}

case "${command_name}" in
  exact) build_exact ;;
  evaluate) evaluate ;;
  all) build_exact; evaluate ;;
  status) show_status ;;
  *) usage ;;
esac
