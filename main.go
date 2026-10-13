package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/glgaspar/minusOne/db"
	"github.com/glgaspar/minusOne/processor"
	"github.com/glgaspar/minusOne/web"
	"golang.org/x/sync/errgroup"
)

type config struct {
	Addr        string                // MINUSONE_ADDR
	DatabaseURL string                // built from MINUSONE_DB_*
	Storage     processor.StoreConfig // MINUSONE_S3_*

	// AllowRegistration lets anyone who can reach the server create an account.
	// MINUSONE_REGISTRATION=closed turns it off once everyone has an account.
	AllowRegistration bool
}

func loadConfig() (config, error) {
	// built with url.URL so passwords with @, :, / etc. are escaped
	dbURL := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(env("MINUSONE_DB_USER", "minusone"), env("MINUSONE_DB_PASSWORD", "minusone")),
		Host:     net.JoinHostPort(env("MINUSONE_DB_HOST", "localhost"), env("MINUSONE_DB_PORT", "5432")),
		Path:     env("MINUSONE_DB_NAME", "minusone"),
		RawQuery: url.Values{"sslmode": {env("MINUSONE_DB_SSLMODE", "disable")}}.Encode(),
	}

	cfg := config{
		Addr:        env("MINUSONE_ADDR", ":8080"),
		DatabaseURL: dbURL.String(),
		Storage: processor.StoreConfig{
			Endpoint:  env("MINUSONE_S3_ENDPOINT", "localhost:9000"),
			AccessKey: env("MINUSONE_S3_ACCESS_KEY", "minusone"),
			SecretKey: env("MINUSONE_S3_SECRET_KEY", "minusone-secret"),
			Bucket:    env("MINUSONE_S3_BUCKET", "minusone"),
			UseSSL:    env("MINUSONE_S3_SSL", "false") == "true",
		},
	}

	switch reg := env("MINUSONE_REGISTRATION", "open"); reg {
	case "open":
		cfg.AllowRegistration = true
	case "closed":
		cfg.AllowRegistration = false
	default:
		return config{}, fmt.Errorf("MINUSONE_REGISTRATION must be open or closed, got %q", reg)
	}

	return cfg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	database, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return err
	}

	store, err := processor.NewStore(ctx, cfg.Storage)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return processor.Run(ctx, database, store) })
	g.Go(func() error {
		server := web.New(database, web.Config{AllowRegistration: cfg.AllowRegistration})
		log.Printf("listening on %s", cfg.Addr)
		return server.Run(ctx, cfg.Addr)
	})
	return g.Wait()
}
