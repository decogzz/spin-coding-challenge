// Command providermock runs a mock of the external transaction provider.
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

	"gitlab.com/decogzz/spin-coding-challenge/internal/providermock"
	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("provider mock stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := envOr("PROVIDER_MOCK_ADDR", ":8081")
	initialBalance, err := transaction.ParseMoney(envOr("PROVIDER_MOCK_INITIAL_BALANCE", "10000.00"))
	if err != nil {
		return err
	}
	slowDelay, err := time.ParseDuration(envOr("PROVIDER_MOCK_SLOW_DELAY", "5s"))
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           providermock.NewServer(providermock.Config{InitialBalance: initialBalance, SlowDelay: slowDelay}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("provider mock listening", slog.String("addr", addr))
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
