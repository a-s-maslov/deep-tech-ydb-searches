#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONFIG="${OBSERVER_CONFIG:-${ROOT}/config/workload.stand.json}"
DEFAULT_ENV_FILE="${ROOT}/../chaos-md/workload/env.local.sh"
ENV_FILE="${OBSERVER_ENV_FILE:-${DEFAULT_ENV_FILE}}"

# In the documented stand layout chaos-md and this repository are siblings.
# Reuse its local (gitignored) integration settings for YDB_CLI/YDB_PROFILE.
# Standalone users can provide the same variables in the environment or point
# OBSERVER_ENV_FILE at another local file.
if [[ -r "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
fi

exec "${ROOT}/scripts/stand/run-workload-with-token.sh" \
  -config "${CONFIG}" observe
