package platform

import (
	"context"
	"database/sql"
)

// Kunci transaksi mencegah dua proses memasang schema internal secara bersamaan.
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(311009001)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, SchemaSQL); err != nil {
		return err
	}
	return tx.Commit()
}

// SchemaSQL hanya diterapkan pada database internal platform, terpisah dari migrasi database target.
// Penyimpanan kredensial oleh modul authentication perlu merujuk identitas user yang sama.
const SchemaSQL = `
CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.users (
	id SERIAL PRIMARY KEY,
	username TEXT NOT NULL UNIQUE CHECK (btrim(username) <> ''),
	is_active BOOLEAN NOT NULL DEFAULT TRUE,
	is_platform_admin BOOLEAN NOT NULL DEFAULT FALSE
);

-- User lama dapat belum memiliki hash; modul authentication harus menolak login sampai kredensial tersedia.
ALTER TABLE platform.users
	ADD COLUMN IF NOT EXISTS password_hash TEXT CHECK (btrim(password_hash) <> '');

CREATE TABLE IF NOT EXISTS platform.groups (
	id SERIAL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE CHECK (btrim(name) <> '')
);

-- Penghapusan user atau group membersihkan keanggotaannya tanpa menghapus entitas di sisi lain.
CREATE TABLE IF NOT EXISTS platform.group_memberships (
	user_id INT NOT NULL REFERENCES platform.users(id) ON DELETE CASCADE,
	group_id INT NOT NULL REFERENCES platform.groups(id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, group_id)
);

CREATE TABLE IF NOT EXISTS platform.database_connections (
	id SERIAL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE CHECK (btrim(name) <> ''),
	host TEXT NOT NULL CHECK (btrim(host) <> ''),
	port INT NOT NULL DEFAULT 5432 CHECK (port BETWEEN 1 AND 65535),
	database_name TEXT NOT NULL CHECK (btrim(database_name) <> ''),
	username TEXT NOT NULL CHECK (btrim(username) <> ''),
	ssl_mode TEXT NOT NULL DEFAULT 'require'
		CHECK (ssl_mode IN ('disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full')),
	ssl_root_cert TEXT NOT NULL DEFAULT '',
	-- Kolom ini menyimpan envelope password terenkripsi, sedangkan username disimpan pada kolom terpisah.
	encrypted_credentials BYTEA NOT NULL CHECK (octet_length(encrypted_credentials) > 0)
);

-- Username konfigurasi lama dibiarkan NULL sampai dilengkapi tanpa menebak atau menimpa kredensial yang sudah ada.
ALTER TABLE platform.database_connections
	ADD COLUMN IF NOT EXISTS username TEXT CHECK (btrim(username) <> ''),
	ADD COLUMN IF NOT EXISTS ssl_root_cert TEXT NOT NULL DEFAULT '';

-- Penghapusan group atau konfigurasi database mencabut akses terkait tanpa menghapus entitas di sisi lain.
CREATE TABLE IF NOT EXISTS platform.group_database_access (
	group_id INT NOT NULL REFERENCES platform.groups(id) ON DELETE CASCADE,
	database_id INT NOT NULL REFERENCES platform.database_connections(id) ON DELETE CASCADE,
	access_level TEXT NOT NULL CHECK (access_level IN ('View Only', 'View + Edit')),
	PRIMARY KEY (group_id, database_id)
);

ALTER TABLE platform.users ADD COLUMN IF NOT EXISTS security_version BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS privilege_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE platform.database_connections ADD COLUMN IF NOT EXISTS driver TEXT NOT NULL DEFAULT 'postgres' CHECK(driver IN ('postgres','mariadb','mysql')),
 ADD COLUMN IF NOT EXISTS audit_username TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS platform.legacy_imports(source_key TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, imported_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS platform.legacy_id_map(source_key TEXT NOT NULL,kind TEXT NOT NULL,legacy_id TEXT NOT NULL,new_id INT NOT NULL,PRIMARY KEY(source_key,kind,legacy_id));

-- Job disimpan pada database internal; pengajuan tidak mengubah target.
-- RESTRICT mempertahankan identitas target/pengaju selama job masih tersimpan.
CREATE TABLE IF NOT EXISTS platform.migration_jobs (
 id BIGSERIAL PRIMARY KEY,
 database_id INT NOT NULL REFERENCES platform.database_connections(id) ON DELETE RESTRICT,
 submitted_by INT NOT NULL REFERENCES platform.users(id) ON DELETE RESTRICT,
 idempotency_key TEXT NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
 version INT NOT NULL CHECK (version > 0),
 query_sql TEXT NOT NULL CHECK (octet_length(query_sql) BETWEEN 1 AND 12288 AND btrim(query_sql) <> ''),
 non_atomic_ddl BOOLEAN NOT NULL DEFAULT FALSE,
 driver TEXT NOT NULL CHECK (driver IN ('postgres','mariadb','mysql')),
 status TEXT NOT NULL DEFAULT 'QUEUED' CHECK (status IN ('QUEUED','RUNNING','SUCCESS','FAILED','NEEDS_REVIEW')),
 worker_id TEXT CHECK (btrim(worker_id) <> ''),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 started_at TIMESTAMPTZ,
 finished_at TIMESTAMPTZ,
 error_code TEXT NOT NULL DEFAULT '',
 UNIQUE (database_id, submitted_by, idempotency_key)
);
CREATE INDEX IF NOT EXISTS migration_jobs_database_created ON platform.migration_jobs(database_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS migration_jobs_worker_status ON platform.migration_jobs(worker_id, status, id);
CREATE INDEX IF NOT EXISTS migration_jobs_queued ON platform.migration_jobs(id) WHERE status='QUEUED';

CREATE OR REPLACE FUNCTION platform.preserve_job_submission() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN
 IF ROW(OLD.id, OLD.database_id, OLD.submitted_by, OLD.idempotency_key, OLD.version,
        OLD.query_sql, OLD.non_atomic_ddl, OLD.driver, OLD.created_at)
    IS DISTINCT FROM
    ROW(NEW.id, NEW.database_id, NEW.submitted_by, NEW.idempotency_key, NEW.version,
        NEW.query_sql, NEW.non_atomic_ddl, NEW.driver, NEW.created_at) THEN
  RAISE EXCEPTION 'job submission is immutable';
 END IF;
 NEW.updated_at = clock_timestamp();
 RETURN NEW;
END $body$;
DROP TRIGGER IF EXISTS preserve_job_submission ON platform.migration_jobs;
CREATE TRIGGER preserve_job_submission BEFORE UPDATE ON platform.migration_jobs
 FOR EACH ROW EXECUTE FUNCTION platform.preserve_job_submission();

CREATE OR REPLACE FUNCTION platform.user_security_change() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN
 IF OLD.password_hash IS DISTINCT FROM NEW.password_hash OR (OLD.is_active AND NOT NEW.is_active) OR (OLD.is_platform_admin AND NOT NEW.is_platform_admin) THEN
  NEW.security_version=OLD.security_version+1;
 END IF;
 IF (NOT OLD.is_platform_admin AND NEW.is_platform_admin) OR (NOT OLD.is_active AND NEW.is_active) THEN
  NEW.privilege_version=OLD.privilege_version+1;
 END IF;
 RETURN NEW;
END $body$;
DROP TRIGGER IF EXISTS user_security_change ON platform.users;
CREATE TRIGGER user_security_change BEFORE UPDATE ON platform.users FOR EACH ROW EXECUTE FUNCTION platform.user_security_change();

CREATE OR REPLACE FUNCTION platform.group_security_change() RETURNS trigger LANGUAGE plpgsql AS $body$
DECLARE increasing BOOLEAN;
BEGIN
 IF TG_TABLE_NAME='group_memberships' THEN
  IF TG_OP='INSERT' THEN UPDATE platform.users SET privilege_version=privilege_version+1 WHERE id=NEW.user_id;
  ELSE UPDATE platform.users SET security_version=security_version+1 WHERE id=OLD.user_id; END IF;
 ELSE
  increasing=TG_OP='INSERT';
  IF TG_OP='UPDATE' THEN
   IF OLD.access_level=NEW.access_level THEN RETURN NEW; END IF;
   increasing=NEW.access_level='View + Edit';
  END IF;
  IF increasing THEN UPDATE platform.users SET privilege_version=privilege_version+1 WHERE id IN (SELECT user_id FROM platform.group_memberships WHERE group_id=NEW.group_id);
  ELSE UPDATE platform.users SET security_version=security_version+1 WHERE id IN (SELECT user_id FROM platform.group_memberships WHERE group_id=OLD.group_id); END IF;
 END IF;
 RETURN NULL;
END $body$;
DROP TRIGGER IF EXISTS membership_security_change ON platform.group_memberships;
CREATE TRIGGER membership_security_change AFTER INSERT OR DELETE ON platform.group_memberships FOR EACH ROW EXECUTE FUNCTION platform.group_security_change();
DROP TRIGGER IF EXISTS grant_security_change ON platform.group_database_access;
CREATE TRIGGER grant_security_change AFTER INSERT OR UPDATE OR DELETE ON platform.group_database_access FOR EACH ROW EXECUTE FUNCTION platform.group_security_change();
`
