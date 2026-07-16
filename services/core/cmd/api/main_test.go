package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/httpapi"
)

func TestNewHTTPServerUsesSharedConfiguration(t *testing.T) {
	t.Parallel()

	handlerCalled := false
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		handlerCalled = true
	})
	httpConfig := config.HTTPConfig{
		Host:              "127.0.0.1",
		Port:              9090,
		ReadHeaderTimeout: 7 * time.Second,
		ReadTimeout:       17 * time.Second,
		WriteTimeout:      31 * time.Second,
		IdleTimeout:       71 * time.Second,
		MaxHeaderBytes:    2 << 20,
	}
	server := newHTTPServer(httpConfig, handler)

	if server.Addr != "127.0.0.1:9090" {
		t.Errorf("Addr = %q, want %q", server.Addr, "127.0.0.1:9090")
	}
	server.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !handlerCalled {
		t.Error("configured Handler did not call the supplied handler")
	}
	if server.ReadHeaderTimeout != httpConfig.ReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %s, want %s", server.ReadHeaderTimeout, httpConfig.ReadHeaderTimeout)
	}
	if server.ReadTimeout != httpConfig.ReadTimeout {
		t.Errorf("ReadTimeout = %s, want %s", server.ReadTimeout, httpConfig.ReadTimeout)
	}
	if server.WriteTimeout != httpConfig.WriteTimeout {
		t.Errorf("WriteTimeout = %s, want %s", server.WriteTimeout, httpConfig.WriteTimeout)
	}
	if server.IdleTimeout != httpConfig.IdleTimeout {
		t.Errorf("IdleTimeout = %s, want %s", server.IdleTimeout, httpConfig.IdleTimeout)
	}
	if server.MaxHeaderBytes != httpConfig.MaxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, httpConfig.MaxHeaderBytes)
	}
}

func TestRunApplicationCreatesCatalogRepositoryFromDatabasePool(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		HTTP: config.HTTPConfig{
			Host:               "127.0.0.1",
			Port:               9090,
			ShutdownTimeout:    time.Second,
			CORSAllowedOrigins: []string{"https://papers.example.test"},
		},
		Database: config.DatabaseConfig{
			URL:      "postgres://paper:secret@localhost/papers",
			MaxConns: 9,
		},
		Catalog: config.CatalogConfig{
			CursorSecret: "api-catalog-cursor-secret-32-bytes",
		},
	}
	pool := &stubDatabasePool{}
	var opened config.DatabaseConfig
	var serverConfig config.HTTPConfig
	var dependencies httpapi.Dependencies
	server := &stubHTTPServer{
		listenAndServe: func() error { return http.ErrServerClosed },
		shutdown: func(context.Context) error {
			t.Fatal("Shutdown called after clean server exit")
			return nil
		},
	}

	err := runApplication(
		context.Background(),
		cfg,
		func(_ context.Context, databaseConfig config.DatabaseConfig) (databasePool, error) {
			opened = databaseConfig
			return pool, nil
		},
		func(cfg config.HTTPConfig, received httpapi.Dependencies) httpServer {
			serverConfig = cfg
			dependencies = received
			return server
		},
	)
	if err != nil {
		t.Fatalf("runApplication() error = %v", err)
	}
	if opened.URL != cfg.Database.URL || opened.MaxConns != cfg.Database.MaxConns {
		t.Fatalf("opened database config = %+v, want shared config", opened)
	}
	if !pool.closed {
		t.Fatal("database pool was not closed")
	}
	if dependencies.Catalog == nil {
		t.Fatal("HTTP dependencies did not receive a catalog repository")
	}
	if len(dependencies.CORSAllowedOrigins) != 1 ||
		dependencies.CORSAllowedOrigins[0] != "https://papers.example.test" {
		t.Fatalf(
			"HTTP dependencies CORS origins = %#v, want configured origin",
			dependencies.CORSAllowedOrigins,
		)
	}
	if serverConfig.Address() != cfg.HTTP.Address() {
		t.Fatalf("HTTP server config address = %q, want %q", serverConfig.Address(), cfg.HTTP.Address())
	}
}

func TestRunApplicationReturnsDatabaseOpenErrorWithoutStartingHTTP(t *testing.T) {
	t.Parallel()

	openError := errors.New("database unavailable")
	server := &stubHTTPServer{
		listenAndServe: func() error {
			t.Fatal("HTTP server started after database open failure")
			return nil
		},
		shutdown: func(context.Context) error {
			t.Fatal("Shutdown called after database open failure")
			return nil
		},
	}
	err := runApplication(
		context.Background(),
		config.Config{},
		func(context.Context, config.DatabaseConfig) (databasePool, error) {
			return nil, openError
		},
		func(config.HTTPConfig, httpapi.Dependencies) httpServer {
			t.Fatal("HTTP server constructed after database open failure")
			return server
		},
	)
	if !errors.Is(err, openError) {
		t.Fatalf("runApplication() error = %v, want wrapped %v", err, openError)
	}
}

func TestRunApplicationReturnsCatalogRepositoryErrorWithoutStartingHTTP(t *testing.T) {
	t.Parallel()

	pool := &stubDatabasePool{}
	server := &stubHTTPServer{
		listenAndServe: func() error {
			t.Fatal("HTTP server started after catalog repository failure")
			return nil
		},
		shutdown: func(context.Context) error {
			t.Fatal("Shutdown called after catalog repository failure")
			return nil
		},
	}
	err := runApplication(
		context.Background(),
		config.Config{
			Catalog: config.CatalogConfig{CursorSecret: "too-short"},
		},
		func(context.Context, config.DatabaseConfig) (databasePool, error) {
			return pool, nil
		},
		func(config.HTTPConfig, httpapi.Dependencies) httpServer {
			t.Fatal("HTTP server constructed after catalog repository failure")
			return server
		},
	)
	if err == nil || !strings.Contains(err.Error(), "create catalog repository") {
		t.Fatalf("runApplication() error = %v, want catalog repository error", err)
	}
	if !pool.closed {
		t.Fatal("database pool was not closed after catalog repository failure")
	}
}

func TestRunReturnsUnexpectedListenError(t *testing.T) {
	t.Parallel()

	listenError := errors.New("listen failed")
	server := &stubHTTPServer{
		listenAndServe: func() error {
			return listenError
		},
		shutdown: func(context.Context) error {
			t.Fatal("Shutdown called after ListenAndServe failed")
			return nil
		},
	}

	err := run(context.Background(), server, time.Second)
	if !errors.Is(err, listenError) {
		t.Fatalf("run error = %v, want wrapped %v", err, listenError)
	}
}

func TestRunTreatsErrServerClosedAsCleanExit(t *testing.T) {
	t.Parallel()

	server := &stubHTTPServer{
		listenAndServe: func() error {
			return http.ErrServerClosed
		},
		shutdown: func(context.Context) error {
			t.Fatal("Shutdown called after server already closed")
			return nil
		},
	}

	if err := run(context.Background(), server, time.Second); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
}

func TestRunShutsDownWithDeadlineWhenContextIsCancelled(t *testing.T) {
	t.Parallel()

	listening := make(chan struct{})
	stopped := make(chan struct{})
	shutdownContext := make(chan context.Context, 1)
	server := &stubHTTPServer{
		listenAndServe: func() error {
			close(listening)
			<-stopped
			return http.ErrServerClosed
		},
		shutdown: func(ctx context.Context) error {
			shutdownContext <- ctx
			close(stopped)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, server, 10*time.Second)
	}()

	<-listening
	cancel()

	select {
	case ctx := <-shutdownContext:
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("Shutdown context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > 10*time.Second {
			t.Fatalf("Shutdown deadline remaining = %s, want within (0, %s]", remaining, 10*time.Second)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown was not called")
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("run error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not return after shutdown")
	}
}

func TestRunReturnsShutdownError(t *testing.T) {
	t.Parallel()

	listening := make(chan struct{})
	stopped := make(chan struct{})
	shutdownError := errors.New("shutdown failed")
	server := &stubHTTPServer{
		listenAndServe: func() error {
			close(listening)
			<-stopped
			return http.ErrServerClosed
		},
		shutdown: func(context.Context) error {
			close(stopped)
			return shutdownError
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, server, time.Second)
	}()

	<-listening
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, shutdownError) {
			t.Fatalf("run error = %v, want wrapped %v", err, shutdownError)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not return after shutdown failure")
	}
}

type stubHTTPServer struct {
	listenAndServe func() error
	shutdown       func(context.Context) error
}

type stubDatabasePool struct {
	closed bool
}

func (pool *stubDatabasePool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errors.New("unexpected transaction")
}

func (pool *stubDatabasePool) Close() {
	pool.closed = true
}

func (server *stubHTTPServer) ListenAndServe() error {
	return server.listenAndServe()
}

func (server *stubHTTPServer) Shutdown(ctx context.Context) error {
	return server.shutdown(ctx)
}
