package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/httpapi"
)

type httpServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

type databasePool interface {
	Close()
}

type databaseOpener func(context.Context, config.DatabaseConfig) (databasePool, error)

func main() {
	cfg, err := config.Load(config.RoleAPI)
	if err != nil {
		log.Printf("load API configuration: %v", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := newHTTPServer(cfg.HTTP, httpapi.NewServer(httpapi.Dependencies{}))

	if err := runApplication(
		ctx,
		cfg,
		server,
		func(ctx context.Context, databaseConfig config.DatabaseConfig) (databasePool, error) {
			return database.Open(ctx, databaseConfig)
		},
	); err != nil {
		log.Printf("api server: %v", err)
		os.Exit(1)
	}
}

func runApplication(
	ctx context.Context,
	cfg config.Config,
	server httpServer,
	openDatabase databaseOpener,
) error {
	pool, err := openDatabase(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}
	defer pool.Close()
	return run(ctx, server, cfg.HTTP.ShutdownTimeout)
}

func newHTTPServer(cfg config.HTTPConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Address(),
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
}

func run(ctx context.Context, server httpServer, shutdownTimeout time.Duration) error {
	if ctx.Err() != nil {
		return nil
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErrors:
		return normalizeServeError(err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}

		return normalizeServeError(<-serveErrors)
	}
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("serve HTTP: %w", err)
}
