#!/usr/bin/env bash
# Prepare this SIWC source package; never download upstream deployment files.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
if [[ ! -f ../Dockerfile.siwc || ! -f docker-compose.local.yml || ! -f .env.example ]]; then
    echo "Run this script from the complete sub2api-siwc source package." >&2
    exit 1
fi
if [[ -e .env ]]; then
    echo "Keeping existing .env and credentials."
else
    command -v openssl >/dev/null 2>&1 || { echo "openssl is required." >&2; exit 1; }
    umask 077
    jwt_secret=$(openssl rand -hex 32)
    totp_key=$(openssl rand -hex 32)
    postgres_password=$(openssl rand -hex 32)
    admin_suffix=$(openssl rand -hex 6)
    sed -e "s/^JWT_SECRET=.*/JWT_SECRET=${jwt_secret}/" \
        -e "s/^TOTP_ENCRYPTION_KEY=.*/TOTP_ENCRYPTION_KEY=${totp_key}/" \
        -e "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=${postgres_password}/" \
        -e "s/^ADMIN_EMAIL=.*/ADMIN_EMAIL=admin-${admin_suffix}@sub2api-siwc.local/" \
        .env.example > .env
    chmod 600 .env
    echo "Created .env with independent SIWC credentials."
fi
mkdir -p sub2api-siwc-data sub2api-siwc-postgres-data sub2api-siwc-redis-data
cat <<'NEXT'
Preparation complete. Review .env, then run from this deploy directory:
  docker compose -f docker-compose.local.yml up -d --build
  docker compose -f docker-compose.local.yml logs -f sub2api-siwc
Default URL: http://localhost:10099
If ADMIN_PASSWORD is empty, find the generated password in first-start logs.
See DOCKER.md for separate production deployment and external database settings.
NEXT
