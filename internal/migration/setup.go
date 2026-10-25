package migration

import (
	"IF3110-K02-09/internal/database"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/lib/pq"
)

func HistorySchema(d database.Dialect, name string) string {
	return database.HistorySchema(d, name)
}

// SetupPostgresHistory dijalankan eksplisit memakai pemilik history, terpisah dari akun eksekusi migration.
func SetupPostgresHistory(ctx context.Context, db *sql.DB, executor string) error {
	if executor == "" {
		return errors.New("role executor wajib diisi")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(311009003)`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
 CREATE SCHEMA IF NOT EXISTS platform_migrations;
 REVOKE ALL ON SCHEMA platform_migrations FROM PUBLIC;
 CREATE TABLE IF NOT EXISTS platform_migrations.attempts(
  id BIGSERIAL PRIMARY KEY,version INT NOT NULL CHECK(version>0),executed_by INT NOT NULL,
  executed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),finished_at TIMESTAMPTZ,
  status TEXT NOT NULL CHECK(status IN ('RUNNING','SUCCESS','FAILED','INTERRUPTED','NEEDS_REVIEW')),
  query_sql TEXT NOT NULL,sql_state TEXT NOT NULL DEFAULT '',error_code TEXT NOT NULL DEFAULT '',
  capability_hash BYTEA UNIQUE,legacy_source_id BIGINT UNIQUE
 );
 ALTER TABLE platform_migrations.attempts ADD COLUMN IF NOT EXISTS legacy_source_id BIGINT UNIQUE;
 CREATE TABLE IF NOT EXISTS platform_migrations.legacy_sources(id INT PRIMARY KEY CHECK(id=1),fingerprint TEXT NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS one_successful_version ON platform_migrations.attempts(version) WHERE status='SUCCESS';
 REVOKE ALL ON ALL TABLES IN SCHEMA platform_migrations FROM PUBLIC;
 REVOKE ALL ON ALL SEQUENCES IN SCHEMA platform_migrations FROM PUBLIC;
 CREATE OR REPLACE VIEW platform_migrations.history AS SELECT id,version,executed_by,executed_at,finished_at,status,query_sql,sql_state,error_code FROM platform_migrations.attempts;
 CREATE OR REPLACE FUNCTION platform_migrations.complete_attempt(capability TEXT) RETURNS VOID LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $body$
 DECLARE changed INT;
 BEGIN
  UPDATE platform_migrations.attempts SET status='SUCCESS',finished_at=clock_timestamp(),capability_hash=NULL
  WHERE status='RUNNING' AND capability_hash=pg_catalog.sha256(pg_catalog.convert_to(capability,'UTF8'));
  GET DIAGNOSTICS changed=ROW_COUNT;
  IF changed<>1 THEN RAISE EXCEPTION 'migration capability rejected' USING ERRCODE='42501'; END IF;
 END $body$;
 REVOKE ALL ON FUNCTION platform_migrations.complete_attempt(TEXT) FROM PUBLIC;
 GRANT USAGE ON SCHEMA platform_migrations TO %s;
 GRANT SELECT ON platform_migrations.history TO %s;
 GRANT EXECUTE ON FUNCTION platform_migrations.complete_attempt(TEXT) TO %s;
 `, pq.QuoteIdentifier(executor), pq.QuoteIdentifier(executor), pq.QuoteIdentifier(executor)))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Verifikasi privilege menolak akun istimewa atau jalur yang memungkinkan executor menguasai history.
func verifyPostgresHistory(ctx context.Context, target, audit *sql.DB) error {
	var owner string
	var safe bool
	err := audit.QueryRowContext(ctx, `SELECT current_user,NOT (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls)
 AND n.nspowner=r.oid AND c.relowner=r.oid AND p.proowner=r.oid AND p.prosecdef
 FROM pg_roles r JOIN pg_namespace n ON n.nspname='platform_migrations'
 JOIN pg_class c ON c.relnamespace=n.oid AND c.relname='attempts'
 JOIN pg_proc p ON p.pronamespace=n.oid AND p.oid='platform_migrations.complete_attempt(text)'::regprocedure WHERE r.rolname=current_user`).Scan(&owner, &safe)
	if err != nil || !safe {
		return ErrUnsafeTarget
	}
	err = target.QueryRowContext(ctx, `SELECT NOT (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls)
 AND current_user<>$1 AND NOT pg_has_role(current_user,$1,'MEMBER')
 AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE member=r.oid)
 AND NOT has_schema_privilege(current_user,'platform_migrations','CREATE')
 AND NOT has_table_privilege(current_user,'platform_migrations.attempts','SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 AND has_table_privilege(current_user,'platform_migrations.history','SELECT')
 AND NOT has_table_privilege(current_user,'platform_migrations.history','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 AND has_function_privilege(current_user,'platform_migrations.complete_attempt(text)','EXECUTE')
 AND NOT EXISTS(SELECT 1 FROM pg_database WHERE datname=current_database() AND datdba=r.oid)
 FROM pg_roles r WHERE rolname=current_user`, owner).Scan(&safe)
	if err != nil || !safe {
		return ErrUnsafeTarget
	}
	return nil
}
