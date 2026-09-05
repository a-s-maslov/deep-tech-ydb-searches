#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

mkdir -p "${TMP}/bin" "${TMP}/output"
printf '{}\n' >"${TMP}/output/documents.jsonl.gz"
printf '{}\n' >"${TMP}/output/queries.json.gz"
printf '{"documents":1}\n' >"${TMP}/output/manifest.json"
cat >"${TMP}/datasets.json" <<EOF
{"profiles":{"test":{"size":1,"output_dir":"output","document_file":"output/documents.jsonl.gz","query_file":"output/queries.json.gz"}}}
EOF
cat >"${TMP}/workload.json" <<'EOF'
{"metrics":{"listen_address":"0.0.0.0:19123"}}
EOF
cat >"${TMP}/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${*: -1}" >"${FAKE_CURL_URL:?}"
[[ "${FAKE_WORKLOAD_RUNNING:-0}" == 1 ]]
EOF
cat >"${TMP}/bin/workload" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${FAKE_WORKLOAD_LOG:?}"
[[ " $* " != *" count "* ]] || printf 'documents=1\n'
EOF
chmod +x "${TMP}/bin/curl" "${TMP}/bin/workload"

export PATH="${TMP}/bin:${PATH}"
export DATASET_CONFIG="${TMP}/datasets.json"
export WORKLOAD_CONFIG="${TMP}/workload.json"
export SEARCH_WORKLOAD_BIN="${TMP}/bin/workload"
export ACTIVE_DATASET_FILE="${TMP}/active-dataset"
export FAKE_CURL_URL="${TMP}/curl-url"
export FAKE_WORKLOAD_LOG="${TMP}/workload-log"

export FAKE_WORKLOAD_RUNNING=1
if bash "${ROOT}/scripts/dataset.sh" activate test --reset >/dev/null 2>&1; then
  echo "dataset activation was allowed while workload metrics were available" >&2
  exit 1
fi
grep -Fx 'http://127.0.0.1:19123/metrics' "${FAKE_CURL_URL}" >/dev/null
[[ ! -e "${FAKE_WORKLOAD_LOG}" ]]

export FAKE_WORKLOAD_RUNNING=0
bash "${ROOT}/scripts/dataset.sh" activate test --reset >/dev/null
grep -q -- '-drop init' "${FAKE_WORKLOAD_LOG}"
grep -qx 'test' "${ACTIVE_DATASET_FILE}"

echo "dataset control smoke: OK"
