package migration

import (
	"IF3110-K02-09/internal/database"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type legacyAttempt struct {
	ID          int64
	Version     int
	User        int
	At          time.Time
	Status, SQL string
}

// History lama disalin atomik dan idempoten tanpa mengubah sumber atau menebak pemetaan identitas.
func ImportHistory(ctx context.Context, audit, source *sql.DB, d database.Dialect, target string, userMap map[int]int, location *time.Location) error {
	failed := errors.New("impor history ditolak; periksa setup, sumber, zona waktu, pemetaan user, dan konflik history")
	if location == nil {
		return failed
	}
	control, err := audit.Conn(ctx)
	if err != nil {
		return failed
	}
	defer control.Close()
	cfg := database.DatabaseConnection{Driver: d, DatabaseName: target}
	if lockHistory(ctx, control, cfg) != nil {
		return failed
	}
	defer unlockHistory(control, cfg)
	sourceSchema := "public"
	if d.IsMySQL() {
		sourceSchema = target
	}
	rows, err := source.QueryContext(ctx, "SELECT id,version,executed_by,executed_at,status,query_sql FROM "+d.Quote(sourceSchema)+".schema_migrations ORDER BY id")
	if err != nil {
		return failed
	}
	attempts := []legacyAttempt{}
	for rows.Next() {
		var a legacyAttempt
		if rows.Scan(&a.ID, &a.Version, &a.User, &a.At, &a.Status, &a.SQL) != nil {
			rows.Close()
			return failed
		}
		if a.ID <= 0 || a.Version <= 0 || userMap[a.User] <= 0 || (a.Status != "SUCCESS" && a.Status != "FAILED") {
			rows.Close()
			return failed
		}
		a.User = userMap[a.User]
		a.At = time.Date(a.At.Year(), a.At.Month(), a.At.Day(), a.At.Hour(), a.At.Minute(), a.At.Second(), a.At.Nanosecond(), location).UTC()
		attempts = append(attempts, a)
	}
	if rows.Err() != nil {
		rows.Close()
		return failed
	}
	rows.Close()
	content, err := json.Marshal(attempts)
	if err != nil {
		return failed
	}
	sum := sha256.Sum256(content)
	fingerprint := hex.EncodeToString(sum[:])
	clear(content)
	schema := d.Quote(HistorySchema(d, target))
	tx, err := control.BeginTx(ctx, nil)
	if err != nil {
		return failed
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT fingerprint FROM "+schema+".legacy_sources WHERE id=1").Scan(&existing)
	if err == nil {
		if existing != fingerprint {
			return failed
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return failed
	}
	var count int
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM "+schema+".attempts").Scan(&count) != nil || count != 0 {
		return failed
	}
	q := "INSERT INTO " + schema + ".attempts(version,executed_by,executed_at,finished_at,status,query_sql,error_code,legacy_source_id) VALUES("
	for i := 1; i <= 8; i++ {
		if i > 1 {
			q += ","
		}
		q += d.Bind(i)
	}
	q += ")"
	for _, a := range attempts {
		if _, err = tx.ExecContext(ctx, q, a.Version, a.User, a.At, a.At, a.Status, a.SQL, "legacy_import", a.ID); err != nil {
			return failed
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+schema+".legacy_sources(id,fingerprint) VALUES(1,"+d.Bind(1)+")", fingerprint); err != nil {
		return failed
	}
	if tx.Commit() != nil {
		return failed
	}
	return nil
}
