// Command api runs the transaction execution API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/decogzz/spin-coding-challenge/internal/config"
	"gitlab.com/decogzz/spin-coding-challenge/internal/httpapi"
	"gitlab.com/decogzz/spin-coding-challenge/internal/postgres"
	"gitlab.com/decogzz/spin-coding-challenge/internal/provider"
	"gitlab.com/decogzz/spin-coding-challenge/internal/requestid"
	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := slog.New(requestid.LogHandler{
		Handler: slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}),
	})
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := connectDatabase(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	svc := transaction.NewService(
		postgres.NewRepository(pool),
		provider.NewClient(cfg.Provider, logger),
		logger,
	)
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewHandler(svc, pool, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      writeTimeout(cfg.Provider),
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", slog.String("addr", cfg.HTTPAddr))
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down; draining in-flight requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// connectDatabase fails fast at startup instead of on the first request.
// Pool size and other settings come from the URL (e.g. ?pool_max_conns=20).
func connectDatabase(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("configure database pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

// writeTimeout must outlast the slowest possible provider call (every attempt
// timing out plus the backoff between them), or the response would be cut off.
func writeTimeout(p provider.Config) time.Duration {
	attempts := time.Duration(p.MaxAttempts)
	return attempts*(p.AttemptTimeout+p.MaxBackoff) + 5*time.Second
}
