#!/bin/sh
set -eu
if [ -e .env ]; then
  echo '.env sudah ada; tidak diubah.'
  exit 0
fi
umask 077
python3 - <<'PY'
from pathlib import Path
import secrets,base64
values={name:secrets.token_hex(24) for name in ('POSTGRES_ADMIN_PASSWORD','MARIADB_ADMIN_PASSWORD','INTERNAL_DATABASE_PASSWORD','TARGET_EXECUTOR_PASSWORD','TARGET_AUDIT_PASSWORD')}
values['DATABASE_CREDENTIALS_KEY']=base64.b64encode(secrets.token_bytes(32)).decode()
values['BOOTSTRAP_ADMIN_USERNAME']='admin'
values['BOOTSTRAP_ADMIN_PASSWORD']=secrets.token_urlsafe(24)
values.update(APP_ENV='development',APP_ORIGIN='http://localhost:8080',HTTP_ADDR='127.0.0.1:8080')
values['PLATFORM_DATABASE_DSN']=f"postgres://atlas_internal:{values['INTERNAL_DATABASE_PASSWORD']}@127.0.0.1:55432/atlas_internal?sslmode=disable"
with Path('.env').open('x') as f:
 for name,value in values.items():f.write(f'{name}={value}\n')
Path('.env').chmod(0o600)
PY
echo '.env dibuat dengan rahasia acak dan izin 0600. Buka file secara lokal untuk kredensial bootstrap; jangan bagikan isinya.'
