#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="${OBSERVER_STATE_DIR:-${ROOT}/.runtime/observer}"
PID_FILE="${STATE_DIR}/observer.pid"
LOG_FILE="${STATE_DIR}/observer.log"
METRICS_URL="${OBSERVER_METRICS_URL:-http://127.0.0.1:19092/metrics}"

is_running() {
  [[ -f "${PID_FILE}" ]] || return 1
  kill -0 "$(cat "${PID_FILE}")" 2>/dev/null
}

case "${1:-}" in
  start)
    if is_running; then
      echo "observer already running: pid $(cat "${PID_FILE}")"
      exit 0
    fi
    mkdir -p "${STATE_DIR}"
    nohup "${ROOT}/scripts/stand/run-observer-with-token.sh" >"${LOG_FILE}" 2>&1 &
    echo $! >"${PID_FILE}"
    for _ in {1..20}; do
      # Do not use grep -q under pipefail here. On a large metrics response it
      # exits after the first match, curl observes a closed pipe (code 23), and
      # a healthy observer is reported as a startup timeout.
      if curl -fsS --max-time 2 "${METRICS_URL}" | grep '^ydb_partition_observer_up.* 1$' >/dev/null; then
        echo "observer started: pid $(cat "${PID_FILE}"), metrics ${METRICS_URL}"
        exit 0
      fi
      if ! is_running; then
        echo "observer stopped during startup; see ${LOG_FILE}" >&2
        exit 1
      fi
      sleep 1
    done
    echo "observer health timeout; see ${LOG_FILE}" >&2
    exit 1
    ;;
  stop)
    if ! is_running; then
      echo "observer already stopped"
      rm -f "${PID_FILE}"
      exit 0
    fi
    pid="$(cat "${PID_FILE}")"
    kill "${pid}"
    for _ in {1..20}; do
      kill -0 "${pid}" 2>/dev/null || { rm -f "${PID_FILE}"; echo "observer stopped"; exit 0; }
      sleep 0.25
    done
    echo "observer did not stop in time: pid ${pid}" >&2
    exit 1
    ;;
  restart)
    "${BASH_SOURCE[0]}" stop
    exec "${BASH_SOURCE[0]}" start
    ;;
  status)
    if is_running; then
      echo "observer running: pid $(cat "${PID_FILE}")"
      curl -fsS --max-time 2 "${METRICS_URL}" | grep '^ydb_partition_observer_up' || true
    else
      echo "observer stopped"
      exit 1
    fi
    ;;
  *)
    echo "usage: $0 start|stop|restart|status" >&2
    exit 2
    ;;
esac
