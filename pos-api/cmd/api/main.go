package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AldiandyaIrsyad/pos-api/internal/catalog"
	"github.com/AldiandyaIrsyad/pos-api/internal/reports"
	"github.com/AldiandyaIrsyad/pos-api/internal/sales"
	"github.com/AldiandyaIrsyad/pos-api/internal/web"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is not set (copy .env.example to .env)")
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return err
	}
	// pgxpool defaults MaxConns to max(4, NumCPU), which starves reads and
	// unrelated outlets when a few checkouts wait on lock_timeout.
	if !strings.Contains(dsn, "pool_max_conns") && cfg.MaxConns < 25 {
		cfg.MaxConns = 25
	}
	// A checkout waiting on a busy stock row gives up after 2 s and gets a
	// retryable 503 (see web.ServerError) instead of holding the request open.
	// No query may run longer than 5 s.
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "2s"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5s"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		web.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// ROUTES
	reports.New(pool).Routes(mux)
	catalog.New(pool).Routes(mux)
	sales.New(pool).Routes(mux)

	srv := &http.Server{
		Addr:              addr,
		Handler:           web.Middleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return srv.Shutdown(shutdownCtx)
}
