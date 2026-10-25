package main

import (
	"IF3110-K02-09/internal/migration"
	"context"
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"log"
	"os"
	"time"
)

func main() {
	driver := os.Getenv("TARGET_DRIVER")
	if driver == "" {
		driver = "postgres"
	}
	sqlDriver := driver
	if driver == "mariadb" {
		sqlDriver = "mysql"
	}
	dsn := os.Getenv("TARGET_AUDIT_DSN")
	role := os.Getenv("TARGET_EXECUTOR_ROLE")
	if dsn == "" || role == "" || os.Getenv("PQGO_DEBUG") == "1" {
		log.Fatal("TARGET_AUDIT_DSN dan TARGET_EXECUTOR_ROLE wajib tersedia; debug protokol harus mati")
	}
	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		log.Fatal("konfigurasi target tidak valid")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if driver == "postgres" {
		err = migration.SetupPostgresHistory(ctx, db, role)
	} else if driver == "mariadb" || driver == "mysql" {
		err = migration.SetupMySQLHistory(ctx, db, os.Getenv("TARGET_DATABASE_NAME"), role)
	} else {
		log.Fatal("driver tidak didukung")
	}
	if err != nil {
		log.Fatal("setup history gagal; periksa role, database, dan privilege sesuai README")
	}
	log.Print("Setup history selesai; sumber history lama tidak diubah")
}
