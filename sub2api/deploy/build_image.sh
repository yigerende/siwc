#!/usr/bin/env bash
# Build the SIWC frontend and backend into their own image.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
docker build -t "${SUB2API_SIWC_IMAGE:-sub2api-siwc:latest}" \
    -f "${REPO_ROOT}/Dockerfile.siwc" "$@" "${REPO_ROOT}"
