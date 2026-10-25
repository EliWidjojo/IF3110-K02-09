#!/bin/sh
set -eu
sh scripts/dev-env.sh
command -v docker >/dev/null 2>&1 || { echo 'Docker Compose diperlukan untuk jalur development ini; gunakan setup PostgreSQL manual pada README jika tidak tersedia.' >&2; exit 1; }
set -a
. ./.env
set +a
docker compose up -d --wait postgres
TARGET_DRIVER=postgres TARGET_AUDIT_DSN="postgres://atlas_audit:$TARGET_AUDIT_PASSWORD@127.0.0.1:55432/atlas_target?sslmode=disable" TARGET_EXECUTOR_ROLE=atlas_executor go run ./cmd/target-setup
if [ "${1:-}" = '--mariadb' ]; then
 docker compose --profile bonus up -d --wait mariadb
 history=$(python3 -c "import hashlib;print('atlas_history_'+hashlib.sha256(b'atlas_target').hexdigest()[:16])")
 TARGET_DRIVER=mariadb TARGET_AUDIT_DSN="atlas_audit:$TARGET_AUDIT_PASSWORD@tcp(127.0.0.1:53307)/$history" TARGET_DATABASE_NAME=atlas_target TARGET_EXECUTOR_ROLE='atlas_executor@%' go run ./cmd/target-setup
fi
echo 'Database dan history siap. Muat .env pada shell, lalu jalankan go run ./cmd/server.'
