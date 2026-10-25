#!/bin/sh
set -eu
psql --username "$POSTGRES_USER" --dbname postgres -v ON_ERROR_STOP=1 <<'SQL'
\getenv internal_password INTERNAL_DATABASE_PASSWORD
\getenv executor_password TARGET_EXECUTOR_PASSWORD
\getenv audit_password TARGET_AUDIT_PASSWORD
CREATE ROLE atlas_internal LOGIN PASSWORD :'internal_password';
CREATE ROLE atlas_executor LOGIN PASSWORD :'executor_password';
CREATE ROLE atlas_audit LOGIN PASSWORD :'audit_password';
CREATE DATABASE atlas_internal OWNER atlas_internal;
CREATE DATABASE atlas_target;
CREATE DATABASE atlas_test_control;
GRANT CREATE ON DATABASE atlas_target TO atlas_audit;
\connect atlas_target
ALTER SCHEMA public OWNER TO atlas_executor;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SQL
