#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PYTHON="${PYTHON:-python3}"
VENV="${DATA_VENV:-${ROOT}/.venv-data}"
TORCH_INDEX_URL="${DATA_TORCH_INDEX_URL:-https://download.pytorch.org/whl/cpu}"
SIZE=50000
PROFILE=""

arguments=("$@")
for ((index=0; index<${#arguments[@]}; index++)); do
  case "${arguments[index]}" in
    --size) SIZE="${arguments[index+1]}" ;;
    --size=*) SIZE="${arguments[index]#--size=}" ;;
    --profile) PROFILE="${arguments[index+1]}" ;;
    --profile=*) PROFILE="${arguments[index]#--profile=}" ;;
  esac
done

if ! command -v "${PYTHON}" >/dev/null 2>&1; then
  echo "Python interpreter not found: ${PYTHON}" >&2
  exit 1
fi

if ! "${PYTHON}" -c 'import sys; raise SystemExit(sys.version_info < (3, 11))'; then
  echo "Python 3.11 or newer is required: ${PYTHON}" >&2
  exit 1
fi

# Debian and Ubuntu package the venv/ensurepip support separately. Check it
# before any large source download starts, so a clean stand fails fast with an
# actionable message.
if ! "${PYTHON}" -c 'import ensurepip, venv' >/dev/null 2>&1; then
  echo "Python venv support is missing. On Ubuntu install it first:" >&2
  echo "  sudo apt-get install python3-venv" >&2
  exit 1
fi

if [[ ! -x "${VENV}/bin/python" ]]; then
  "${PYTHON}" -m venv "${VENV}"
fi

if ! "${VENV}/bin/python" -m pip --version >/dev/null 2>&1; then
  echo "The data virtual environment is incomplete: ${VENV}" >&2
  echo "Remove this generated directory and run the command again." >&2
  exit 1
fi

# PyPI Linux wheels may pull the complete CUDA runtime even on a CPU-only
# preparation host. Install the official CPU wheel first; the project install
# below then reuses the already satisfied torch dependency.
"${VENV}/bin/python" -m pip install \
  --index-url "${TORCH_INDEX_URL}" \
  'torch>=2.4,<3'
"${VENV}/bin/python" -m pip install -e "${ROOT}[model,quality]"
"${VENV}/bin/deep-tech-data" prepare-runtime "$@"

if [[ -n "${PROFILE}" ]]; then
  OUTPUT="$(${VENV}/bin/python - "${ROOT}/config/datasets.json" "${PROFILE}" <<'PY'
import json, pathlib, sys
path, name = pathlib.Path(sys.argv[1]).resolve(), sys.argv[2]
profile = json.loads(path.read_text(encoding="utf-8"))["profiles"][name]
print((path.parent / profile["output_dir"]).resolve())
PY
)"
else
  OUTPUT="${ROOT}/data/output/smoke-${SIZE}"
fi

cat <<EOF

Runtime data are ready:
  ${OUTPUT}/workload-documents.jsonl.gz
  ${OUTPUT}/workload-queries.json.gz
  ${OUTPUT}/SHA256SUMS
EOF
