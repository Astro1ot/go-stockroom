package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Astro1ot/go-stockroom/internal/catalog"
	"github.com/Astro1ot/go-stockroom/internal/httpapi"
	"github.com/Astro1ot/go-stockroom/internal/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func main() {
	health := flag.Bool("healthcheck", false, "check the local readiness endpoint")
	flag.Parse()
	if *health {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/readyz")
		if err != nil {
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("stockroom stopped", "error", err)
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(startup, env("DATABASE_URL", "postgres://stockroom:stockroom@127.0.0.1:15432/stockroom?sslmode=disable"))
	if err != nil {
		return err
	}
	defer pool.Close()
	store := &postgres.Store{Pool: pool}
	if err := store.Ping(startup); err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	if err := store.Migrate(startup); err != nil {
		return err
	}
	if os.Getenv("SEED_DEMO") == "true" {
		if err := store.Seed(startup); err != nil {
			return err
		}
	}
	redisOptions, err := redis.ParseURL(env("REDIS_URL", "redis://127.0.0.1:16379/0"))
	if err != nil {
		return err
	}
	redisOptions.MaxRetries = -1
	redisOptions.DialTimeout = 150 * time.Millisecond
	redisOptions.ReadTimeout = 150 * time.Millisecond
	redisOptions.WriteTimeout = 150 * time.Millisecond
	redisOptions.ContextTimeoutEnabled = true
	cache := redis.NewClient(redisOptions)
	defer cache.Close()
	server := &http.Server{Addr: env("HTTP_ADDR", "127.0.0.1:8081"), Handler: httpapi.Router(store, catalog.Catalog{Source: store, Redis: cache, TTL: 30 * time.Second}), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	slog.Info("stockroom listening", "address", server.Addr)
	select {
	case err := <-errs:
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
