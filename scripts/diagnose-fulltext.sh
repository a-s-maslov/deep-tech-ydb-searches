#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CATALOG="${DATASET_CONFIG:-${ROOT}/config/datasets.json}"
CONFIG="${WORKLOAD_CONFIG:-${ROOT}/config/workload.stand.json}"
RUNNER="${SEARCH_WORKLOAD_BIN:-${ROOT}/scripts/stand/run-workload-with-token.sh}"

usage() {
  echo "usage: $0 run|status PROFILE [QUERY_LIMIT]" >&2
  exit 2
}

[[ $# -ge 2 && $# -le 3 ]] || usage
command_name="$1"
profile="$2"
query_limit="${3:-0}"
output="${FULLTEXT_DIAGNOSTIC_OUTPUT:-${ROOT}/quality/results/${profile}-fulltext-diagnostic.json.gz}"
query_representation="${FULLTEXT_DIAGNOSTIC_QUERY_REPRESENTATION:-lexical}"
minimum_should_match="${FULLTEXT_DIAGNOSTIC_MSM:-50%}"

run_diagnostic() {
  "${RUNNER}" \
    -config "${CONFIG}" \
    -dataset-config "${CATALOG}" \
    -dataset-profile "${profile}" \
    -diagnostic-output "${output}" \
    -diagnostic-workers 1 \
    -diagnostic-query-limit "${query_limit}" \
    -diagnostic-query-representation "${query_representation}" \
    -diagnostic-msm "${minimum_should_match}" \
    -diagnostic-warmup=true \
    diagnose-fulltext
}

show_status() {
  [[ -f "${output}" ]] || { echo "diagnostic=MISSING path=${output}"; exit 1; }
  python3 - "${output}" <<'PY'
import gzip, json, pathlib, sys

path = pathlib.Path(sys.argv[1])
with gzip.open(path, "rt", encoding="utf-8") as stream:
    report = json.load(stream)
summary = report["summary"]
print(f"diagnostic=OK path={path}")
print(
    f"profile={report['profile']} queries={report['query_count']} "
    f"query_representation={report.get('query_representation', 'original')} "
    f"documents={report['documents']} fulltext_docs_partitions={report['fulltext_docs_partitions']} "
    f"succeeded={summary['succeeded']} failed={summary['failed']}"
)
print(
    f"client_ms p50={summary['client_p50_ms']:.1f} "
    f"p95={summary['client_p95_ms']:.1f} p99={summary['client_p99_ms']:.1f} "
    f"max={summary['client_max_ms']:.1f}"
)
print(
    f"scored_document_rows p50={summary['scored_document_rows_p50']:.0f} "
    f"p95={summary['scored_document_rows_p95']:.0f} max={summary['scored_document_rows_max']} "
    f"corr(duration,scored_rows)={summary['duration_scored_document_rows_pearson']:.3f} "
    f"corr(duration,cpu)={summary['duration_cpu_pearson']:.3f}"
)
print("slowest:")
for item in report["slowest"][:10]:
    print(
        f"  qid={item['qid']} client_ms={item['client_duration_ms']:.1f} "
        f"cpu_ms={item['cpu_time_ms']:.1f} scored_rows={item['scored_document_rows']} "
        f"text={item['text']!r} lexical={item.get('lexical_query', item['text'])!r}"
    )
PY
}

case "${command_name}" in
  run) run_diagnostic ;;
  status) show_status ;;
  *) usage ;;
esac
