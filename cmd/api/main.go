package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alyralabs/digitaletude-api/internal/auth"
	"github.com/alyralabs/digitaletude-api/internal/config"
	"github.com/alyralabs/digitaletude-api/internal/httpserver"
	"github.com/alyralabs/digitaletude-api/internal/music"
	"github.com/alyralabs/digitaletude-api/internal/photos"
	"github.com/alyralabs/digitaletude-api/internal/storage"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Supabase's transaction-mode pooler hands out different backend
	// sessions between statements, so pgx's default named/cached prepared
	// statements collide across them (42P05). QueryExecModeExec keeps the
	// extended (type-safe) protocol but never names or caches a statement.
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	poolConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()

	verifier, err := auth.NewVerifier(cfg.SupabaseURL, cfg.AdminUserID)
	if err != nil {
		return err
	}
	st := storage.New(cfg.SupabaseURL, cfg.SupabaseSecretKey)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			httpserver.Err(w, http.StatusServiceUnavailable, "unhealthy", "database unreachable")
			return
		}
		httpserver.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	photos.NewHandler(photos.NewRepo(pool), st).Register(mux, verifier.Middleware)
	music.NewHandler(music.NewRepo(pool), st).Register(mux, verifier.Middleware)

	handler := httpserver.Recover(httpserver.Log(httpserver.CORS(cfg.AllowedOrigin)(mux)))
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// Drain in-flight requests (uploads especially) before exiting.
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
