package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewHTTPServerConfiguresFiniteHTTPBoundaries(t *testing.T) {
	t.Parallel()

	handlerCalled := false
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		handlerCalled = true
	})
	server := newHTTPServer(":9090", handler)

	if server.Addr != ":9090" {
		t.Errorf("Addr = %q, want %q", server.Addr, ":9090")
	}
	server.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !handlerCalled {
		t.Error("configured Handler did not call the supplied handler")
	}
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %s, want %s", server.ReadHeaderTimeout, 5*time.Second)
	}
	if server.ReadTimeout != 15*time.Second {
		t.Errorf("ReadTimeout = %s, want %s", server.ReadTimeout, 15*time.Second)
	}
	if server.WriteTimeout != 30*time.Second {
		t.Errorf("WriteTimeout = %s, want %s", server.WriteTimeout, 30*time.Second)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %s, want %s", server.IdleTimeout, 60*time.Second)
	}
	if server.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, 1<<20)
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

	err := run(context.Background(), server)
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

	if err := run(context.Background(), server); err != nil {
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
		result <- run(ctx, server)
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
		result <- run(ctx, server)
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

func (server *stubHTTPServer) ListenAndServe() error {
	return server.listenAndServe()
}

func (server *stubHTTPServer) Shutdown(ctx context.Context) error {
	return server.shutdown(ctx)
}
