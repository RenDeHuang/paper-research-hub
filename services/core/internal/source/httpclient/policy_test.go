package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
)

func TestNewRequiresExplicitBoundedPolicy(t *testing.T) {
	t.Parallel()

	valid := validConfig()
	tests := []struct {
		name   string
		mutate func(*httpclient.Config)
	}{
		{name: "timeout", mutate: func(cfg *httpclient.Config) { cfg.Timeout = 0 }},
		{name: "user agent", mutate: func(cfg *httpclient.Config) { cfg.UserAgent = "" }},
		{name: "rate requests", mutate: func(cfg *httpclient.Config) { cfg.RateLimit.Requests = 0 }},
		{name: "rate interval", mutate: func(cfg *httpclient.Config) { cfg.RateLimit.Interval = 0 }},
		{name: "negative retries", mutate: func(cfg *httpclient.Config) { cfg.MaxRetries = -1 }},
		{name: "max wait", mutate: func(cfg *httpclient.Config) { cfg.MaxWait = 0 }},
		{name: "initial backoff", mutate: func(cfg *httpclient.Config) { cfg.InitialBackoff = 0 }},
		{name: "max backoff", mutate: func(cfg *httpclient.Config) { cfg.MaxBackoff = 0 }},
		{name: "backoff ordering", mutate: func(cfg *httpclient.Config) { cfg.MaxBackoff = cfg.InitialBackoff / 2 }},
		{name: "response limit", mutate: func(cfg *httpclient.Config) { cfg.MaxResponseBytes = 0 }},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid
			tt.mutate(&cfg)
			if client, err := httpclient.New(http.DefaultClient, cfg, httpclient.Dependencies{}); err == nil {
				t.Fatalf("New() = %#v, want explicit policy validation error", client)
			}
		})
	}
}

func TestClientSetsUserAgentAndAppliesInjectedRateLimit(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if got := request.Header.Get("User-Agent"); got != "paper-hub-test/1.0" {
			t.Errorf("User-Agent = %q, want configured value", got)
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	clock := newFakeClock()
	cfg := validConfig()
	cfg.RateLimit = httpclient.RateLimit{Requests: 2, Interval: time.Second}
	client, err := httpclient.New(server.Client(), cfg, clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for range 2 {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatalf("NewRequestWithContext() error = %v", err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		_ = response.Body.Close()
	}

	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if got := clock.sleeps(); len(got) != 1 || got[0] != 500*time.Millisecond {
		t.Fatalf("rate-limit sleeps = %v, want [500ms]", got)
	}
}

func TestCanceledConcurrentRateWaitsDoNotConsumeFutureSlots(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	clock := newCancelingRateClock()
	cfg := validConfig()
	cfg.RateLimit = httpclient.RateLimit{Requests: 1, Interval: time.Second}
	client, err := httpclient.New(server.Client(), cfg, clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first, err := doGet(client, server.URL)
	if err != nil {
		t.Fatalf("first Do() error = %v", err)
	}
	_ = first.Body.Close()

	const canceledRequests = 8
	start := make(chan struct{})
	errs := make(chan error, canceledRequests)
	var waitGroup sync.WaitGroup
	for id := range canceledRequests {
		baseContext := context.WithValue(context.Background(), cancellationRequestKey{}, id)
		ctx, cancel := context.WithCancel(baseContext)
		clock.registerCancel(id, cancel)
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			if requestErr != nil {
				errs <- requestErr
				return
			}
			_, requestErr = client.Do(request)
			errs <- requestErr
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Do() error = %v, want context.Canceled", err)
		}
	}

	final, err := doGet(client, server.URL)
	if err != nil {
		t.Fatalf("final Do() error = %v", err)
	}
	_ = final.Body.Close()

	if requests.Load() != 2 {
		t.Fatalf("HTTP requests = %d, want only initial and final valid requests", requests.Load())
	}
	if got := clock.sleeps(); len(got) != 1 || got[0] != time.Second {
		t.Fatalf("successful rate-limit sleeps = %v, want one 1s slot", got)
	}
}

func TestRateLimitQueueCancellationDoesNotWaitForActiveSleeperOrConsumeSlots(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	clock := newBlockingRateClock()
	cfg := validConfig()
	cfg.RateLimit = httpclient.RateLimit{Requests: 1, Interval: time.Second}
	client, err := httpclient.New(server.Client(), cfg, clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first, err := doGet(client, server.URL)
	if err != nil {
		t.Fatalf("first Do() error = %v", err)
	}
	_ = first.Body.Close()

	activeResult := make(chan error, 1)
	go func() {
		response, requestErr := doGet(client, server.URL)
		if response != nil {
			_ = response.Body.Close()
		}
		activeResult <- requestErr
	}()

	select {
	case <-clock.sleepStarted:
	case <-time.After(time.Second):
		t.Fatal("active rate-limit sleep did not start")
	}

	const canceledRequests = 8
	canceledResults := make(chan error, canceledRequests)
	checked := make([]<-chan struct{}, 0, canceledRequests)
	cancels := make([]context.CancelFunc, 0, canceledRequests)
	var canceledWaitGroup sync.WaitGroup
	for range canceledRequests {
		baseContext, cancel := context.WithCancel(context.Background())
		observed := &observedContext{
			Context: baseContext,
			checked: make(chan struct{}),
		}
		checked = append(checked, observed.checked)
		cancels = append(cancels, cancel)

		request, requestErr := http.NewRequestWithContext(
			observed,
			http.MethodGet,
			server.URL,
			nil,
		)
		if requestErr != nil {
			t.Fatalf("NewRequestWithContext() error = %v", requestErr)
		}
		canceledWaitGroup.Add(1)
		go func() {
			defer canceledWaitGroup.Done()
			_, requestErr := client.Do(request)
			canceledResults <- requestErr
		}()
	}

	for _, requestChecked := range checked {
		select {
		case <-requestChecked:
		case <-time.After(time.Second):
			close(clock.releaseFirstSleep)
			t.Fatal("canceled request did not enter Do")
		}
	}
	for _, cancel := range cancels {
		cancel()
	}

	allCanceledReturned := make(chan struct{})
	go func() {
		canceledWaitGroup.Wait()
		close(allCanceledReturned)
	}()

	select {
	case <-allCanceledReturned:
	case <-time.After(500 * time.Millisecond):
		close(clock.releaseFirstSleep)
		<-allCanceledReturned
		t.Fatal("canceled requests remained queued behind active rate-limit sleep")
	}
	for range canceledRequests {
		if err := <-canceledResults; !errors.Is(err, context.Canceled) {
			close(clock.releaseFirstSleep)
			t.Fatalf("canceled Do() error = %v, want context.Canceled", err)
		}
	}

	close(clock.releaseFirstSleep)
	if err := <-activeResult; err != nil {
		t.Fatalf("active Do() error = %v", err)
	}

	final, err := doGet(client, server.URL)
	if err != nil {
		t.Fatalf("final Do() error = %v", err)
	}
	_ = final.Body.Close()

	if requests.Load() != 3 {
		t.Fatalf("HTTP requests = %d, want initial, active, and final valid requests", requests.Load())
	}
	if got := clock.sleeps(); len(got) != 2 ||
		got[0] != time.Second ||
		got[1] != time.Second {
		t.Fatalf("successful rate-limit sleeps = %v, want [1s 1s]", got)
	}
}

func TestClientRetries429UsingBoundedRetryAfter(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.Header().Set("Retry-After", "2")
			http.Error(writer, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	clock := newFakeClock()
	cfg := validConfig()
	cfg.RateLimit = httpclient.RateLimit{Requests: 1, Interval: time.Nanosecond}
	client, err := httpclient.New(server.Client(), cfg, clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response, err := doGet(client, server.URL+"?api_key=retry-secret")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = response.Body.Close()

	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	if !containsDuration(clock.sleeps(), 2*time.Second) {
		t.Fatalf("sleeps = %v, want Retry-After delay 2s", clock.sleeps())
	}
}

func TestClientRetries503UsingBoundedRetryAfter(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.Header().Set("Retry-After", "2")
			http.Error(writer, "maintenance", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	clock := newFakeClock()
	cfg := validConfig()
	cfg.RateLimit = httpclient.RateLimit{Requests: 1, Interval: time.Nanosecond}
	client, err := httpclient.New(server.Client(), cfg, clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response, err := doGet(client, server.URL)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = response.Body.Close()

	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	if !containsDuration(clock.sleeps(), 2*time.Second) {
		t.Fatalf("sleeps = %v, want 503 Retry-After delay 2s", clock.sleeps())
	}
}

func TestClientRejects503RetryAfterBeyondWaitBudget(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Retry-After", "120")
		http.Error(writer, "maintenance", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	clock := newFakeClock()
	cfg := validConfig()
	cfg.MaxWait = 5 * time.Second
	client, err := httpclient.New(server.Client(), cfg, clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = doGet(client, server.URL)
	if !errors.Is(err, httpclient.ErrRetryBudgetExceeded) {
		t.Fatalf("Do() error = %v, want ErrRetryBudgetExceeded", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want no retry beyond 503 wait budget", attempts.Load())
	}
}

func TestClientRejectsRetryAfterBeyondWaitBudget(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Retry-After", "120")
		http.Error(writer, "budget exceeded", http.StatusTooManyRequests)
	}))
	defer server.Close()

	clock := newFakeClock()
	cfg := validConfig()
	cfg.MaxWait = 5 * time.Second
	client, err := httpclient.New(server.Client(), cfg, clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = doGet(client, server.URL+"?api_key=budget-secret")
	if !errors.Is(err, httpclient.ErrRetryBudgetExceeded) {
		t.Fatalf("Do() error = %v, want ErrRetryBudgetExceeded", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want no retry beyond budget", attempts.Load())
	}
	if containsDuration(clock.sleeps(), 120*time.Second) {
		t.Fatalf("client slept past retry budget: %v", clock.sleeps())
	}
	if strings.Contains(err.Error(), "budget-secret") {
		t.Fatalf("error leaked api_key: %v", err)
	}
}

func TestClientRejectsOverflowingRetryAfterAsBeyondWaitBudget(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Retry-After", "9223372036854775807")
		http.Error(writer, "unrepresentable delay", http.StatusTooManyRequests)
	}))
	defer server.Close()

	clock := newFakeClock()
	cfg := validConfig()
	cfg.MaxWait = 5 * time.Second
	client, err := httpclient.New(server.Client(), cfg, clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = doGet(client, server.URL)
	if !errors.Is(err, httpclient.ErrRetryBudgetExceeded) {
		t.Fatalf("Do() error = %v, want ErrRetryBudgetExceeded", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want no retry for overflowing Retry-After", attempts.Load())
	}
	for _, delay := range clock.sleeps() {
		if delay < 0 {
			t.Fatalf("client attempted a negative sleep after overflow: %v", clock.sleeps())
		}
	}
}

func TestClientRetries5xxWithBoundedExponentialBackoff(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempt := attempts.Add(1)
		if attempt <= 2 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	clock := newFakeClock()
	cfg := validConfig()
	cfg.InitialBackoff = 100 * time.Millisecond
	cfg.MaxBackoff = 150 * time.Millisecond
	cfg.RateLimit = httpclient.RateLimit{Requests: 1, Interval: time.Nanosecond}
	client, err := httpclient.New(server.Client(), cfg, clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response, err := doGet(client, server.URL)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = response.Body.Close()

	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
	sleeps := clock.sleepsAtLeast(time.Millisecond)
	if len(sleeps) != 2 || sleeps[0] != 100*time.Millisecond || sleeps[1] != 150*time.Millisecond {
		t.Fatalf("retry sleeps = %v, want [100ms 150ms]", sleeps)
	}
}

func TestClientDoesNotRetryNonRetryable4xx(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(writer, "invalid query", http.StatusBadRequest)
	}))
	defer server.Close()

	client, err := httpclient.New(server.Client(), validConfig(), httpclient.Dependencies{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = doGet(client, server.URL)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("Do() error = %v, want explicit 400 error", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestClientHonorsContextCancellationDuringRetryWait(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	clock := newFakeClock()
	clock.beforeSleep = cancel
	client, err := httpclient.New(server.Client(), validConfig(), clock.dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	_, err = client.Do(request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context.Canceled", err)
	}
}

func TestClientRejectsResponsesAboveConfiguredBodyLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", 33)))
	}))
	defer server.Close()

	cfg := validConfig()
	cfg.MaxResponseBytes = 32
	client, err := httpclient.New(server.Client(), cfg, httpclient.Dependencies{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = doGet(client, server.URL)
	if !errors.Is(err, httpclient.ErrResponseTooLarge) {
		t.Fatalf("Do() error = %v, want ErrResponseTooLarge", err)
	}
}

func TestClientRedactsSensitiveQueryHeaderAndResponseValuesFromErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(
			writer,
			fmt.Sprintf(
				"query=%s header=%s",
				request.URL.Query().Get("api_key"),
				request.Header.Get("X-API-Key"),
			),
			http.StatusBadRequest,
		)
	}))
	defer server.Close()

	client, err := httpclient.New(server.Client(), validConfig(), httpclient.Dependencies{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		server.URL+"?api_key=query-secret&search=agents",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	request.Header.Set("X-API-Key", "header-secret")

	_, err = client.Do(request)
	if err == nil {
		t.Fatal("Do() error = nil, want 400 error")
	}
	for _, secret := range []string{"query-secret", "header-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v, want visible redaction marker", err)
	}
}

func TestClientRedactsOverlappingQueryAndHeaderSecretsLongestFirst(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(
			writer,
			fmt.Sprintf(
				"query=%s header=%s repeated=%s",
				request.URL.Query().Get("api_key"),
				request.Header.Get("X-API-Key"),
				request.URL.Query().Get("token"),
			),
			http.StatusBadRequest,
		)
	}))
	defer server.Close()

	client, err := httpclient.New(server.Client(), validConfig(), httpclient.Dependencies{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		server.URL+"?api_key=abcdef&token=abcdef",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	request.Header.Set("X-API-Key", "abc")

	_, err = client.Do(request)
	if err == nil {
		t.Fatal("Do() error = nil, want 400 error")
	}
	for _, leaked := range []string{"abcdef", "abc", "def"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error retained overlapping secret fragment %q: %v", leaked, err)
		}
	}
	if strings.Count(err.Error(), "[REDACTED]") < 3 {
		t.Fatalf("error = %v, want query/header/error values visibly redacted", err)
	}
}

func TestClientRedactsTransportErrors(t *testing.T) {
	t.Parallel()

	base := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf(
			"dial failed for %s authorization=%s",
			request.URL.String(),
			request.Header.Get("Authorization"),
		)
	})}
	client, err := httpclient.New(base, validConfig(), httpclient.Dependencies{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"https://example.test/works?api_key=transport-query-secret",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer transport-header-secret")

	_, err = client.Do(request)
	if err == nil {
		t.Fatal("Do() error = nil, want transport error")
	}
	for _, secret := range []string{"transport-query-secret", "transport-header-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("transport error leaked %q: %v", secret, err)
		}
	}
}

func TestClientRedactsAuthorizationCredentialsWhenErrorContainsOnlyToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(
			writer,
			"auth=authorization-token-secret proxy=proxy-token-secret",
			http.StatusBadRequest,
		)
	}))
	defer server.Close()

	client, err := httpclient.New(server.Client(), validConfig(), httpclient.Dependencies{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		server.URL,
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer authorization-token-secret")
	request.Header.Set("Proxy-Authorization", "Basic proxy-token-secret")

	_, err = client.Do(request)
	if err == nil {
		t.Fatal("Do() error = nil, want 400 error")
	}
	for _, secret := range []string{
		"authorization-token-secret",
		"proxy-token-secret",
	} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked credential-only secret %q: %v", secret, err)
		}
	}
}

func validConfig() httpclient.Config {
	return httpclient.Config{
		Timeout:          time.Second,
		UserAgent:        "paper-hub-test/1.0",
		RateLimit:        httpclient.RateLimit{Requests: 1000, Interval: time.Second},
		MaxRetries:       2,
		MaxWait:          5 * time.Second,
		InitialBackoff:   100 * time.Millisecond,
		MaxBackoff:       time.Second,
		MaxResponseBytes: 1 << 20,
		SensitiveQueryParameters: []string{
			"api_key",
		},
		SensitiveHeaders: []string{
			"Authorization",
			"X-API-Key",
		},
	}
}

func doGet(client *httpclient.Client, target string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}

func containsDuration(values []time.Duration, want time.Duration) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type fakeClock struct {
	mu          sync.Mutex
	now         time.Time
	recorded    []time.Duration
	beforeSleep func()
}

type cancellationRequestKey struct{}

type observedContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

func (ctx *observedContext) Err() error {
	ctx.once.Do(func() {
		close(ctx.checked)
	})
	return ctx.Context.Err()
}

type blockingRateClock struct {
	mu                sync.Mutex
	now               time.Time
	recorded          []time.Duration
	firstSleep        bool
	sleepStarted      chan struct{}
	releaseFirstSleep chan struct{}
}

func newBlockingRateClock() *blockingRateClock {
	return &blockingRateClock{
		now:               time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
		sleepStarted:      make(chan struct{}),
		releaseFirstSleep: make(chan struct{}),
	}
}

func (clock *blockingRateClock) dependencies() httpclient.Dependencies {
	return httpclient.Dependencies{
		Now: func() time.Time {
			clock.mu.Lock()
			defer clock.mu.Unlock()
			return clock.now
		},
		Sleep: func(ctx context.Context, delay time.Duration) error {
			clock.mu.Lock()
			block := !clock.firstSleep
			if block {
				clock.firstSleep = true
			}
			clock.mu.Unlock()

			if block {
				close(clock.sleepStarted)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-clock.releaseFirstSleep:
				}
			} else {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
			}

			clock.mu.Lock()
			defer clock.mu.Unlock()
			clock.recorded = append(clock.recorded, delay)
			clock.now = clock.now.Add(delay)
			return nil
		},
	}
}

func (clock *blockingRateClock) sleeps() []time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return append([]time.Duration(nil), clock.recorded...)
}

type cancelingRateClock struct {
	mu       sync.Mutex
	now      time.Time
	recorded []time.Duration
	cancels  map[int]context.CancelFunc
}

func newCancelingRateClock() *cancelingRateClock {
	return &cancelingRateClock{
		now:     time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
		cancels: make(map[int]context.CancelFunc),
	}
}

func (clock *cancelingRateClock) registerCancel(id int, cancel context.CancelFunc) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.cancels[id] = cancel
}

func (clock *cancelingRateClock) dependencies() httpclient.Dependencies {
	return httpclient.Dependencies{
		Now: func() time.Time {
			clock.mu.Lock()
			defer clock.mu.Unlock()
			return clock.now
		},
		Sleep: func(ctx context.Context, delay time.Duration) error {
			if id, ok := ctx.Value(cancellationRequestKey{}).(int); ok {
				clock.mu.Lock()
				cancel := clock.cancels[id]
				clock.mu.Unlock()
				cancel()
				<-ctx.Done()
				return ctx.Err()
			}
			clock.mu.Lock()
			defer clock.mu.Unlock()
			clock.recorded = append(clock.recorded, delay)
			clock.now = clock.now.Add(delay)
			return nil
		},
	}
}

func (clock *cancelingRateClock) sleeps() []time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return append([]time.Duration(nil), clock.recorded...)
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)}
}

func (clock *fakeClock) dependencies() httpclient.Dependencies {
	return httpclient.Dependencies{
		Now: clock.Now,
		Sleep: func(ctx context.Context, delay time.Duration) error {
			if clock.beforeSleep != nil {
				clock.beforeSleep()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			clock.mu.Lock()
			defer clock.mu.Unlock()
			clock.recorded = append(clock.recorded, delay)
			clock.now = clock.now.Add(delay)
			return nil
		},
	}
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) sleeps() []time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return append([]time.Duration(nil), clock.recorded...)
}

func (clock *fakeClock) sleepsAtLeast(minimum time.Duration) []time.Duration {
	all := clock.sleeps()
	filtered := make([]time.Duration, 0, len(all))
	for _, delay := range all {
		if delay >= minimum {
			filtered = append(filtered, delay)
		}
	}
	return filtered
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
