// Command admin runs the local-only admin API: the write endpoints for
// photos, music, and posts, with no auth. It only ever binds 127.0.0.1 and
// is never deployed — the owner runs it locally, pointed (via a local .env)
// at the same production Supabase Postgres/Storage the public site reads
// from, and drives it with the separate digitaletude-admin frontend.
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

	"github.com/alyralabs/digitaletude-api/internal/config"
	"github.com/alyralabs/digitaletude-api/internal/httpserver"
	"github.com/alyralabs/digitaletude-api/internal/music"
	"github.com/alyralabs/digitaletude-api/internal/photos"
	"github.com/alyralabs/digitaletude-api/internal/posts"
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

	// Pooler safety: QueryExecModeExec avoids named prepared statements
	// colliding across Supabase's transaction-mode pooler's rotating
	// backend sessions (42P05).
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

	st := storage.New(cfg.SupabaseURL, cfg.SupabaseSecretKey)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			httpserver.Err(w, http.StatusServiceUnavailable, "unhealthy", "database unreachable")
			return
		}
		httpserver.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Photos and music have no draft/published split — the "admin" pages for
	// them list everything via the same read routes the public site uses, so
	// this server registers both. Posts' admin list/get are already separate
	// admin-only routes (they include drafts), so no public registration is
	// needed there.
	photosHandler := photos.NewHandler(photos.NewRepo(pool), st)
	photosHandler.RegisterPublic(mux)
	photosHandler.RegisterAdmin(mux)

	musicHandler := music.NewHandler(music.NewRepo(pool), st)
	musicHandler.RegisterPublic(mux)
	musicHandler.RegisterAdmin(mux)

	posts.NewHandler(posts.NewRepo(pool), st).RegisterAdmin(mux)

	handler := httpserver.Recover(httpserver.Log(httpserver.CORS(cfg.AllowedOrigin)(mux)))
	srv := &http.Server{
		Addr:              "127.0.0.1:" + cfg.AdminPort,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("admin server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
