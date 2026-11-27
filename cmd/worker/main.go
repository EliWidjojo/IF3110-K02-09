package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/observability"
	"IF3110-K02-09/internal/platform"
	"github.com/lib/pq"
)

func run(ctx context.Context) error {
	dsn := os.Getenv("PLATFORM_DATABASE_DSN")
	if dsn == "" || os.Getenv("PQGO_DEBUG") == "1" {
		return errors.New("konfigurasi database internal tidak valid")
	}
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		return errors.New("database internal tidak tersedia")
	}
	db := sql.OpenDB(observability.WrapConnector(connector, observability.DB{System: "postgresql", Role: "platform"}))
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
	slog.Info("worker.started", "worker_id", name)
	for ctx.Err() == nil {
		worked, err := w.ProcessNext(ctx)
		if worked && err == nil {
			continue
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("worker.poll", "worker_id", name, "status", "error", "error", err.Error())
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
	shutdown, err := observability.Setup("atlas-worker")
	if err != nil {
		slog.Error("worker.stopped", "status", "error", "error", err.Error())
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = run(ctx)
	stop()
	flush, done := context.WithTimeout(context.Background(), 5*time.Second)
	_ = shutdown(flush)
	done()
	if err != nil {
		slog.Error("worker.stopped", "status", "error", "error", err.Error())
		os.Exit(1)
	}
	slog.Info("worker.stopped", "status", "success")
}
