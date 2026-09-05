#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mkdir -p "${ROOT}/bin"
cd "${ROOT}/workload"
CGO_ENABLED=0 go build -trimpath -o "${ROOT}/bin/search-workload" ./cmd/search-workload
echo "built ${ROOT}/bin/search-workload (commands: run, observe and administrative actions)"
