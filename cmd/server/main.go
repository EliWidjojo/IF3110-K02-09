package main

import (
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/platform"
	"IF3110-K02-09/internal/server"
	"context"
	"database/sql"
	"errors"
	_ "github.com/lib/pq"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func run() error {
	if os.Getenv("PQGO_DEBUG") == "1" {
		return errors.New("debug protokol database harus dinonaktifkan")
	}
	dsn := os.Getenv("PLATFORM_DATABASE_DSN")
	if dsn == "" {
		return errors.New("PLATFORM_DATABASE_DSN wajib tersedia")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return errors.New("konfigurasi database internal tidak valid")
	}
	defer db.Close()
	db.SetMaxOpenConns(15)
	db.SetMaxIdleConns(3)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if db.PingContext(ctx) != nil {
		return errors.New("database internal tidak dapat dihubungi")
	}
	if err = platform.EnsureSchema(ctx, db); err != nil {
		return errors.New("schema internal tidak dapat disiapkan")
	}
	cipher, err := database.NewDatabasePasswordCipherFromEnv()
	if err != nil {
		return err
	}
	if path := os.Getenv("IMPORT_LEGACY_IDENTITY"); path != "" {
		if err = platform.ImportLegacy(ctx, db, cipher, path, os.Getenv("IMPORT_LEGACY_DATABASES")); err != nil {
			return err
		}
	}
	store := auth.NewPostgresStore(db)
	if err = store.Bootstrap(ctx, os.Getenv("BOOTSTRAP_ADMIN_USERNAME"), os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")); err != nil {
		return err
	}
	_ = os.Unsetenv("BOOTSTRAP_ADMIN_PASSWORD")
	sessions, err := auth.NewSessions(auth.DefaultSessionConfig())
	if err != nil {
		return err
	}
	a, err := auth.NewHTTP(store, sessions, env("APP_ORIGIN", "http://localhost:8080"), env("APP_ENV", "development") == "production")
	if err != nil {
		return err
	}
	module, err := platform.NewPlatformModule(db, cipher, platform.SecurityDependencies{Identity: a, CSRF: a, Sessions: a, PasswordHasher: platform.PasswordHasherFunc(func(_ context.Context, password string) (admin.PasswordHash, error) {
		hash, e := auth.HashPassword(password)
		if e != nil {
			return "", admin.ErrInvalidUserInput
		}
		return admin.PasswordHash(hash), nil
	})})
	if err != nil {
		return err
	}
	defer module.Close()
	app := &http.Server{Addr: env("HTTP_ADDR", "127.0.0.1:8080"), Handler: server.New(a, module), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-stopCtx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = app.Shutdown(shutdown)
	}()
	log.Print("Database platform started")
	err = app.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.New("server HTTP berhenti")
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
