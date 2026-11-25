package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/platform"
	_ "github.com/lib/pq"
)

func run(ctx context.Context) error {
	dsn := os.Getenv("PLATFORM_DATABASE_DSN")
	if dsn == "" || os.Getenv("PQGO_DEBUG") == "1" {
		return errors.New("konfigurasi database internal tidak valid")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return errors.New("database internal tidak tersedia")
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	start, done := context.WithTimeout(ctx, 20*time.Second)
	err = platform.EnsureSchema(start, db)
	done()
	if err != nil {
		return errors.New("gagal menyiapkan schema worker")
	}
	cipher, err := database.NewDatabasePasswordCipherFromEnv()
	if err != nil {
		return err
	}
	name := os.Getenv("WORKER_ID")
	if name == "" {
		host, _ := os.Hostname()
		name = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	w, err := migration.NewWorker(db, database.NewDatabaseConnectionRepository(db, cipher), name)
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		worked, err := w.ProcessNext(ctx)
		if worked && err == nil {
			continue
		}
		if err != nil && ctx.Err() == nil {
			log.Print("pemeriksaan job terhenti; worker akan mencoba kembali")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
