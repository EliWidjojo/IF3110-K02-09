package migration

import (
	"IF3110-K02-09/internal/database"
	"context"
	"database/sql"
	"errors"
	"strings"
)

func mysqlAccount(account string) (string, error) {
	parts := strings.Split(account, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("akun harus user@host")
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	return quote(parts[0]) + "@" + quote(parts[1]), nil
}

func SetupMySQLHistory(ctx context.Context, db *sql.DB, target, executor string) error {
	account, err := mysqlAccount(executor)
	if err != nil {
		return err
	}
	schema := database.MariaDB.Quote(HistorySchema(database.MariaDB, target))
	statements := []string{
		"CREATE DATABASE IF NOT EXISTS " + schema,
		"CREATE TABLE IF NOT EXISTS " + schema + `.attempts(id BIGINT AUTO_INCREMENT PRIMARY KEY,version INT NOT NULL,executed_by INT NOT NULL,executed_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),finished_at TIMESTAMP(6) NULL,status VARCHAR(20) NOT NULL,query_sql MEDIUMTEXT NOT NULL,sql_state VARCHAR(5) NOT NULL DEFAULT '',error_code VARCHAR(40) NOT NULL DEFAULT '',capability_hash CHAR(64) UNIQUE,legacy_source_id BIGINT UNIQUE,successful_version INT GENERATED ALWAYS AS(CASE WHEN status='SUCCESS' THEN version ELSE NULL END) STORED,UNIQUE(successful_version)) ENGINE=InnoDB`,
		"CREATE TABLE IF NOT EXISTS " + schema + ".legacy_sources(id INT PRIMARY KEY,fingerprint VARCHAR(64) NOT NULL) ENGINE=InnoDB",
		"CREATE OR REPLACE SQL SECURITY DEFINER VIEW " + schema + `.history AS SELECT id,version,executed_by,executed_at,finished_at,status,query_sql,sql_state,error_code FROM ` + schema + `.attempts`,
		"DROP PROCEDURE IF EXISTS " + schema + `.complete_attempt`,
		"CREATE PROCEDURE " + schema + `.complete_attempt(IN capability VARCHAR(64)) SQL SECURITY DEFINER MODIFIES SQL DATA BEGIN UPDATE ` + schema + `.attempts SET status='SUCCESS',finished_at=CURRENT_TIMESTAMP(6),capability_hash=NULL WHERE status='RUNNING' AND capability_hash=SHA2(capability,256); IF ROW_COUNT()<>1 THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='migration capability rejected'; END IF; END`,
		"GRANT SELECT ON " + schema + ".history TO " + account,
		"GRANT EXECUTE ON PROCEDURE " + schema + ".complete_attempt TO " + account,
	}
	for _, q := range statements {
		if _, err = db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func verifyMySQLHistory(ctx context.Context, target, audit *sql.DB, cfg database.DatabaseConnection) error {
	schema := HistorySchema(cfg.Driver, cfg.DatabaseName)
	var executor, owner string
	if target.QueryRowContext(ctx, `SELECT CURRENT_USER()`).Scan(&executor) != nil || audit.QueryRowContext(ctx, `SELECT CURRENT_USER()`).Scan(&owner) != nil || owner == executor {
		return ErrUnsafeTarget
	}
	for _, db := range []*sql.DB{target, audit} {
		var count int
		q := `SELECT COUNT(*) FROM information_schema.USER_PRIVILEGES WHERE GRANTEE=CONCAT(QUOTE(SUBSTRING_INDEX(CURRENT_USER(),'@',1)),'@',QUOTE(SUBSTRING_INDEX(CURRENT_USER(),'@',-1))) AND PRIVILEGE_TYPE<>'USAGE'`
		if db.QueryRowContext(ctx, q).Scan(&count) != nil || count != 0 {
			return ErrUnsafeTarget
		}
		// Role tambahan ditolak agar privilege tidak tersembunyi di balik role yang dapat diaktifkan ulang.
		if db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.APPLICABLE_ROLES`).Scan(&count) != nil || count != 0 {
			return ErrUnsafeTarget
		}
	}
	var count int
	q := `SELECT (SELECT COUNT(*) FROM information_schema.SCHEMA_PRIVILEGES WHERE TABLE_SCHEMA=? AND PRIVILEGE_TYPE<>'USAGE')+(SELECT COUNT(*) FROM information_schema.TABLE_PRIVILEGES WHERE TABLE_SCHEMA=? AND TABLE_NAME='attempts')`
	if target.QueryRowContext(ctx, q, schema, schema).Scan(&count) != nil || count != 0 {
		return ErrUnsafeTarget
	}
	var definer string
	if audit.QueryRowContext(ctx, `SELECT DEFINER FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA=? AND ROUTINE_NAME='complete_attempt' AND SECURITY_TYPE='DEFINER'`, schema).Scan(&definer) != nil || definer != owner {
		return ErrUnsafeTarget
	}
	if audit.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME='attempts' AND ENGINE='InnoDB'`, schema).Scan(&count) != nil || count != 1 {
		return ErrUnsafeTarget
	}
	return nil
}

func (e *Engine) runMySQL(ctx context.Context, target *sql.DB, control *sql.Conn, cfg database.DatabaseConnection, req MigrationRequest, statements []string, capability string, out ExecutionResult, fail func(error, string) (ExecutionResult, error)) (ExecutionResult, error) {
	schema := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName))
	if req.NonAtomicDDL {
		if _, err := target.ExecContext(ctx, statements[0]); err != nil {
			return fail(err, "ddl_failed")
		}
		// DDL DBMS ini melakukan implicit commit sehingga hasil tidak pernah dijanjikan atomik dengan history.
		if _, err := control.ExecContext(ctx, "CALL "+schema+".complete_attempt(?)", capability); err != nil {
			return fail(err, "ddl_history_failed")
		}
		out.Status = "SUCCESS"
		return out, nil
	}
	var count int
	if target.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_type='BASE TABLE' AND engine<>'InnoDB'`).Scan(&count) != nil || count != 0 {
		return fail(ErrUnsafeTarget, "non_transactional_engine")
	}
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return fail(err, "begin_failed")
	}
	defer tx.Rollback()
	for _, stmt := range statements {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fail(err, "sql_failed")
		}
	}
	if _, err = tx.ExecContext(ctx, "CALL "+schema+".complete_attempt(?)", capability); err != nil {
		_ = tx.Rollback()
		return fail(err, "history_failed")
	}
	if err = tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fail(err, "commit_failed")
	}
	out.Status = "SUCCESS"
	return out, nil
}
