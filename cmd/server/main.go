package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"time"

	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/server"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func run() error {
	store, err := auth.OpenStore(env("IDENTITY_FILE", "data/identity.json"))
	if err != nil {
		return errors.New("cannot open identity store")
	}
	if store.Empty() {
		name, password := os.Getenv("BOOTSTRAP_ADMIN_USERNAME"), os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")
		if name == "" || password == "" {
			return errors.New("first startup requires BOOTSTRAP_ADMIN_USERNAME and BOOTSTRAP_ADMIN_PASSWORD (12+ bytes)")
		}
		if _, err = store.CreateUser(name, password, true); err != nil {
			return errors.New("cannot create bootstrap admin; check username and password length")
		}
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
	targets := map[string]*database.Target{}
	defer func() {
		for _, t := range targets {
			t.DB.Close()
		}
	}()
	if file := os.Getenv("DATABASE_CONFIG"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return errors.New("cannot read DATABASE_CONFIG")
		}
		var configs []database.Config
		if json.Unmarshal(b, &configs) != nil {
			return errors.New("invalid DATABASE_CONFIG JSON")
		}
		validID := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
		for _, c := range configs {
			if !validID.MatchString(c.ID) || targets[c.ID] != nil {
				return errors.New("target IDs must be unique and contain only letters, digits, underscores or hyphens")
			}
			t, err := database.Open(context.Background(), c)
			if err != nil {
				return fmt.Errorf("cannot connect target %s", c.ID)
			}
			targets[c.ID] = t
			if err := migration.NewMigrationServiceWithDialect(t.DB, t.Dialect).InitMigrationTable(); err != nil {
				return fmt.Errorf("cannot initialize migration history for target %s", c.ID)
			}
		}
	}
	app := &http.Server{Addr: env("HTTP_ADDR", "127.0.0.1:8080"), Handler: server.New(a, targets), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdown)
	}()
	log.Print("Database platform started")
	err = app.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
