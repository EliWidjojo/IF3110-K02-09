package main

import (
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"log"
	"os"
	"time"
)

func main() {
	if os.Getenv("PQGO_DEBUG") == "1" {
		log.Fatal("debug protokol harus mati")
	}
	d := database.Dialect(os.Getenv("TARGET_DRIVER"))
	if d == "" {
		d = database.Postgres
	}
	if !d.Valid() {
		log.Fatal("driver tidak didukung")
	}
	driver := "postgres"
	if d.IsMySQL() {
		driver = "mysql"
	}
	location, err := time.LoadLocation(os.Getenv("LEGACY_TIMEZONE"))
	if err != nil || os.Getenv("LEGACY_TIMEZONE") == "" {
		log.Fatal("LEGACY_TIMEZONE wajib ditetapkan eksplisit")
	}
	var users map[int]int
	content, err := os.ReadFile(os.Getenv("LEGACY_USER_MAP"))
	if err != nil || json.Unmarshal(content, &users) != nil {
		log.Fatal("LEGACY_USER_MAP harus berupa file JSON pemetaan ID user lama ke ID internal")
	}
	internal, err := sql.Open("postgres", os.Getenv("PLATFORM_DATABASE_DSN"))
	if err != nil {
		log.Fatal("konfigurasi internal tidak valid")
	}
	defer internal.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, id := range users {
		var exists bool
		if id <= 0 || internal.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM platform.users WHERE id=$1)`, id).Scan(&exists) != nil || !exists {
			log.Fatal("pemetaan menunjuk user internal yang tidak tersedia")
		}
	}
	auditDSN, legacyDSN := os.Getenv("TARGET_AUDIT_DSN"), os.Getenv("TARGET_LEGACY_DSN")
	if auditDSN == "" || legacyDSN == "" || os.Getenv("PLATFORM_DATABASE_DSN") == "" || os.Getenv("TARGET_DATABASE_NAME") == "" {
		log.Fatal("DSN internal, pencatat, sumber, dan nama target wajib diisi")
	}
	if d.IsMySQL() {
		cfg, e := mysql.ParseDSN(legacyDSN)
		if e != nil {
			log.Fatal("konfigurasi sumber tidak valid")
		}
		cfg.ParseTime = true
		legacyDSN = cfg.FormatDSN()
	}
	audit, err := sql.Open(driver, auditDSN)
	if err != nil {
		log.Fatal("konfigurasi pencatat tidak valid")
	}
	defer audit.Close()
	source, err := sql.Open(driver, legacyDSN)
	if err != nil {
		log.Fatal("konfigurasi sumber tidak valid")
	}
	defer source.Close()
	if err = migration.ImportHistory(ctx, audit, source, d, os.Getenv("TARGET_DATABASE_NAME"), users, location); err != nil {
		log.Fatal(err)
	}
	log.Print("Impor history selesai; sumber tidak diubah")
}
