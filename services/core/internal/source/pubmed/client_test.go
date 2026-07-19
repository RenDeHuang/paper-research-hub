package pubmed_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
)

func TestSearchUsesHistoryServerIdentityDateWindowAndStableBatches(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/esearch.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var captured url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/entrez/eutils/esearch.fcgi" {
			t.Errorf("path = %q", request.URL.Path)
		}
		captured = request.URL.Query()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()

	client := newClient(t, server, nil, httpclient.Dependencies{})
	result, err := client.Search(context.Background(), pubmed.SearchQuery{
		Term:         "agents",
		JournalISSNs: []string{"0028-0836"},
		DateType:     pubmed.DateTypeEntrez,
		DateWindow: pubmed.DateWindow{
			From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
		},
		MaxResults: 5,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if result.Count != 5 || result.WebEnv != "NCID_1_123456789_130.14.22.215_9001_1720000000_1" || result.QueryKey != "1" {
		t.Fatalf("Search() = %#v, want count and history identity", result)
	}
	wantBatches := []pubmed.Batch{
		{RetStart: 0, RetMax: 2},
		{RetStart: 2, RetMax: 2},
		{RetStart: 4, RetMax: 1},
	}
	if !slices.Equal(result.Batches, wantBatches) {
		t.Fatalf("Batches = %#v, want %#v", result.Batches, wantBatches)
	}
	for key, want := range map[string]string{
		"db":         "pubmed",
		"retmode":    "json",
		"retmax":     "0",
		"usehistory": "y",
		"tool":       "paper-hub-test",
		"email":      "research@example.test",
		"api_key":    "ncbi-test-secret",
		"datetype":   "edat",
		"mindate":    "2026/07/01",
		"maxdate":    "2026/07/16",
	} {
		if got := captured.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := captured.Get("term"); got != "(agents) AND (0028-0836[issn])" {
		t.Fatalf("term = %q", got)
	}
}

func TestFetchUsesOnlyHistoryServerOrderedRetstartRetmaxBatches(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		requests []url.Values
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		values := request.URL.Query()
		mu.Lock()
		requests = append(requests, values)
		mu.Unlock()

		start, _ := strconv.Atoi(values.Get("retstart"))
		maximum, _ := strconv.Atoi(values.Get("retmax"))
		writer.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(writer, `<?xml version="1.0"?><PubmedArticleSet>`)
		for offset := range maximum {
			pmid := start + offset + 1
			_, _ = fmt.Fprintf(writer, `<PubmedArticle><MedlineCitation><PMID>%d</PMID><Article><ArticleTitle>Record %d</ArticleTitle></Article></MedlineCitation><PubmedData/></PubmedArticle>`, pmid, pmid)
		}
		_, _ = fmt.Fprint(writer, `</PubmedArticleSet>`)
	}))
	defer server.Close()

	client := newClient(t, server, nil, httpclient.Dependencies{})
	records, errs := collect(client.Fetch(context.Background(), pubmed.SearchResult{
		Count:    3,
		WebEnv:   "history-token",
		QueryKey: "7",
		Batches: []pubmed.Batch{
			{RetStart: 0, RetMax: 2},
			{RetStart: 2, RetMax: 1},
		},
	}))
	if len(errs) != 0 {
		t.Fatalf("Fetch() errors = %v", errs)
	}
	if got := recordPMIDs(records); !slices.Equal(got, []string{"1", "2", "3"}) {
		t.Fatalf("ordered PMIDs = %v", got)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for index, values := range requests {
		for _, forbidden := range []string{"id", "term"} {
			if values.Has(forbidden) {
				t.Errorf("request %d unexpectedly used %s=%q", index, forbidden, values.Get(forbidden))
			}
		}
		for key, want := range map[string]string{
			"db":        "pubmed",
			"query_key": "7",
			"WebEnv":    "history-token",
			"retmode":   "xml",
		} {
			if got := values.Get(key); got != want {
				t.Errorf("request %d %s = %q, want %q", index, key, got, want)
			}
		}
	}
}

func TestClientDerivesNCBIRateLimitFromOptionalAPIKey(t *testing.T) {
	for _, tt := range []struct {
		name     string
		apiKey   string
		wantWait time.Duration
	}{
		{name: "without key", wantWait: time.Second / 3},
		{name: "with key", apiKey: "ncbi-test-secret", wantWait: time.Second / 10},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"esearchresult":{"count":"0","querykey":"1","webenv":"empty-history"}}`))
			}))
			defer server.Close()

			clock := &recordingClock{now: time.Unix(0, 0)}
			client := newClient(t, server, func(config *pubmed.Config) {
				config.APIKey = tt.apiKey
			}, httpclient.Dependencies{
				Now: clock.Now,
				Sleep: func(ctx context.Context, delay time.Duration) error {
					return clock.Sleep(ctx, delay)
				},
			})
			query := pubmed.SearchQuery{
				DateType: pubmed.DateTypeEntrez,
				DateWindow: pubmed.DateWindow{
					From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
					To:   time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC),
				},
				MaxResults: 1,
			}
			for range 2 {
				if _, err := client.Search(context.Background(), query); err != nil {
					t.Fatalf("Search() error = %v", err)
				}
			}
			if got := clock.Sleeps(); !slices.Equal(got, []time.Duration{tt.wantWait}) {
				t.Fatalf("rate sleeps = %v, want [%s]", got, tt.wantWait)
			}
		})
	}
}

func TestClientUsesBoundedRetryAndRedactsAPIKey(t *testing.T) {
	t.Parallel()

	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(writer, "rejected ncbi-test-secret", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := newClient(t, server, func(config *pubmed.Config) {
		config.MaxRetries = 1
	}, httpclient.Dependencies{
		Now: func() time.Time { return time.Unix(0, 0) },
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	})
	_, err := client.Search(context.Background(), pubmed.SearchQuery{
		DateType: pubmed.DateTypeEntrez,
		DateWindow: pubmed.DateWindow{
			From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC),
		},
		MaxResults: 1,
	})
	if err == nil {
		t.Fatal("Search() accepted repeated 503")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want initial request plus one bounded retry", attempts)
	}
	if strings.Contains(err.Error(), "ncbi-test-secret") {
		t.Fatalf("Search() error leaked API key: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("Search() error = %v, want explicit redaction", err)
	}
}

func TestClientRejectsTimeoutAndResponseLimitThroughSharedPolicy(t *testing.T) {
	t.Parallel()

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			<-request.Context().Done()
		}))
		defer server.Close()

		client := newClient(t, server, func(config *pubmed.Config) {
			config.Timeout = time.Millisecond
		}, httpclient.Dependencies{})
		_, err := client.Search(context.Background(), validSearchQuery())
		if err == nil {
			t.Fatal("Search() accepted timed out response")
		}
	})

	t.Run("response limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(strings.Repeat("x", 1024)))
		}))
		defer server.Close()

		client := newClient(t, server, func(config *pubmed.Config) {
			config.MaxResponseBytes = 32
		}, httpclient.Dependencies{})
		_, err := client.Search(context.Background(), validSearchQuery())
		if !errors.Is(err, httpclient.ErrResponseTooLarge) {
			t.Fatalf("Search() error = %v, want ErrResponseTooLarge", err)
		}
	})
}

func TestClientCountCoverageUsesExactCanonicalISSNORAndReturnsRawResponseSHA256(t *testing.T) {
	t.Parallel()

	payload := []byte("{\"esearchresult\":{\"count\":\"42\"}}\n")
	var (
		captured url.Values
		requests int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/entrez/eutils/esearch.fcgi" {
			t.Errorf("path = %q, want ESearch", request.URL.Path)
		}
		captured = request.URL.Query()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	client := newClient(t, server, nil, httpclient.Dependencies{})
	result, err := client.CountCoverage(context.Background(), pubmed.CoverageQuery{
		JournalISSNs: []string{
			" 0140-6736 ",
			"0028-0836",
			"0140-6736",
		},
	})
	if err != nil {
		t.Fatalf("CountCoverage() error = %v", err)
	}
	if result.Count != 42 {
		t.Fatalf("Count = %d, want 42", result.Count)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(payload)); result.ResponseSHA256 != want {
		t.Fatalf("ResponseSHA256 = %q, want %q", result.ResponseSHA256, want)
	}
	if requests != 1 {
		t.Fatalf("physical requests = %d, want 1", requests)
	}
	if got := captured.Get("term"); got != "(0028-0836[issn] OR 0140-6736[issn])" {
		t.Fatalf("term = %q, want unique stable exact ISSN OR", got)
	}
	for key, want := range map[string]string{
		"db":      "pubmed",
		"retmode": "json",
		"retmax":  "0",
		"tool":    "paper-hub-test",
		"email":   "research@example.test",
		"api_key": "ncbi-test-secret",
	} {
		if got := captured.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for _, forbidden := range []string{
		"usehistory",
		"datetype",
		"mindate",
		"maxdate",
		"retstart",
		"query_key",
		"WebEnv",
	} {
		if captured.Has(forbidden) {
			t.Errorf("coverage request unexpectedly included %s=%q", forbidden, captured.Get(forbidden))
		}
	}
	if len(captured) != 7 {
		t.Fatalf("coverage query keys = %v, want only exact coverage and identity parameters", captured)
	}
}

func TestClientCountCoverageRejectsMissingOrInvalidISSNsWithoutRequest(t *testing.T) {
	t.Parallel()

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	client := newClient(t, server, nil, httpclient.Dependencies{})

	for _, tt := range []struct {
		name  string
		query pubmed.CoverageQuery
	}{
		{name: "missing"},
		{name: "journal title", query: pubmed.CoverageQuery{JournalISSNs: []string{"Nature"}}},
		{name: "invalid checksum", query: pubmed.CoverageQuery{JournalISSNs: []string{"0028-0837"}}},
		{name: "one invalid among valid", query: pubmed.CoverageQuery{JournalISSNs: []string{"0028-0836", "bad"}}},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got, err := client.CountCoverage(context.Background(), tt.query); err == nil {
				t.Fatalf("CountCoverage() = %#v, want error", got)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("invalid coverage queries made %d requests, want 0", requests)
	}
}

func TestClientCountCoverageStrictlyValidatesCountString(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		payload string
	}{
		{name: "missing", payload: `{"esearchresult":{}}`},
		{name: "number", payload: `{"esearchresult":{"count":1}}`},
		{name: "empty", payload: `{"esearchresult":{"count":""}}`},
		{name: "negative", payload: `{"esearchresult":{"count":"-1"}}`},
		{name: "plus sign", payload: `{"esearchresult":{"count":"+1"}}`},
		{name: "leading whitespace", payload: `{"esearchresult":{"count":" 1"}}`},
		{name: "trailing whitespace", payload: `{"esearchresult":{"count":"1 "}}`},
		{name: "decimal", payload: `{"esearchresult":{"count":"1.0"}}`},
		{name: "unicode digit", payload: `{"esearchresult":{"count":"１"}}`},
		{name: "int64 overflow", payload: `{"esearchresult":{"count":"9223372036854775808"}}`},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(tt.payload))
			}))
			defer server.Close()
			client := newClient(t, server, nil, httpclient.Dependencies{})

			if got, err := client.CountCoverage(context.Background(), pubmed.CoverageQuery{
				JournalISSNs: []string{"0028-0836"},
			}); err == nil {
				t.Fatalf("CountCoverage() = %#v, want strict count error", got)
			}
		})
	}
}

func TestClientCountCoverageRejectsInvalidUTF8DuplicateKeysAndTrailingJSON(t *testing.T) {
	t.Parallel()

	invalidUTF8 := append(
		[]byte(`{"esearchresult":{"count":"1"},"ignored":"`),
		0xff,
	)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	for _, tt := range []struct {
		name    string
		payload []byte
	}{
		{name: "invalid UTF-8", payload: invalidUTF8},
		{name: "duplicate top-level key", payload: []byte(`{"esearchresult":{"count":"1"},"esearchresult":{"count":"2"}}`)},
		{name: "duplicate result key", payload: []byte(`{"esearchresult":{"count":"1","count":"2"}}`)},
		{name: "duplicate ignored nested key", payload: []byte(`{"ignored":{"nested":{"x":1,"x":2}},"esearchresult":{"count":"1"}}`)},
		{name: "second JSON value", payload: []byte(`{"esearchresult":{"count":"1"}} {"second":true}`)},
		{name: "trailing garbage", payload: []byte(`{"esearchresult":{"count":"1"}} garbage`)},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(tt.payload)
			}))
			defer server.Close()
			client := newClient(t, server, nil, httpclient.Dependencies{})

			if got, err := client.CountCoverage(context.Background(), pubmed.CoverageQuery{
				JournalISSNs: []string{"0028-0836"},
			}); err == nil {
				t.Fatalf("CountCoverage() = %#v, want strict JSON error", got)
			}
		})
	}
}

func TestClientCountCoverageReturnsCanceledContextBeforeQueryValidationOrRequest(t *testing.T) {
	t.Parallel()

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	client := newClient(t, server, nil, httpclient.Dependencies{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.CountCoverage(ctx, pubmed.CoverageQuery{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CountCoverage() error = %v, want context.Canceled", err)
	}
	if requests != 0 {
		t.Fatalf("canceled coverage made %d requests, want 0", requests)
	}
}

func TestClientCountCoverageUsesSharedBoundedRetryPolicy(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts int
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				attempts++
				if attempts == 1 {
					http.Error(writer, "retry", status)
					return
				}
				_, _ = writer.Write([]byte(`{"esearchresult":{"count":"1"}}`))
			}))
			defer server.Close()
			client := newClient(t, server, func(config *pubmed.Config) {
				config.MaxRetries = 1
			}, httpclient.Dependencies{
				Now: func() time.Time { return time.Unix(0, 0) },
				Sleep: func(context.Context, time.Duration) error {
					return nil
				},
			})

			result, err := client.CountCoverage(context.Background(), pubmed.CoverageQuery{
				JournalISSNs: []string{"0028-0836"},
			})
			if err != nil {
				t.Fatalf("CountCoverage() error = %v", err)
			}
			if result.Count != 1 || attempts != 2 {
				t.Fatalf("CountCoverage() = %#v after %d attempts, want count 1 after one retry", result, attempts)
			}
		})
	}
}

func TestClientCountCoverageRedactsAPIKeyAndEmailFromErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(
			writer,
			"rejected ncbi-test-secret for research@example.test",
			http.StatusBadRequest,
		)
	}))
	defer server.Close()
	client := newClient(t, server, func(config *pubmed.Config) {
		config.MaxRetries = 0
	}, httpclient.Dependencies{})

	_, err := client.CountCoverage(context.Background(), pubmed.CoverageQuery{
		JournalISSNs: []string{"0028-0836"},
	})
	if err == nil {
		t.Fatal("CountCoverage() accepted HTTP 400")
	}
	for _, secret := range []string{"ncbi-test-secret", "research@example.test"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("CountCoverage() error leaked %q: %v", secret, err)
		}
	}
}

func TestClientCountCoverageDoesNotEchoCredentialsFromProtocolErrors(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		payload string
	}{
		{name: "count", payload: `{"esearchresult":{"count":"ncbi-test-secret research@example.test"}}`},
		{name: "trailing value", payload: `{"esearchresult":{"count":"1"}} "ncbi-test-secret research@example.test"`},
		{name: "duplicate key", payload: `{"ncbi-test-secret":1,"ncbi-test-secret":2,"esearchresult":{"count":"1"}}`},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(tt.payload))
			}))
			defer server.Close()
			client := newClient(t, server, nil, httpclient.Dependencies{})

			_, err := client.CountCoverage(context.Background(), pubmed.CoverageQuery{
				JournalISSNs: []string{"0028-0836"},
			})
			if err == nil {
				t.Fatal("CountCoverage() accepted invalid protocol payload")
			}
			for _, secret := range []string{"ncbi-test-secret", "research@example.test"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("CountCoverage() protocol error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestSearchRejectsDuplicateESearchKeys(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(
			`{"esearchresult":{"count":"1","count":"2","querykey":"1","webenv":"history"}}`,
		))
	}))
	defer server.Close()
	client := newClient(t, server, nil, httpclient.Dependencies{})

	if got, err := client.Search(context.Background(), validSearchQuery()); err == nil {
		t.Fatalf("Search() = %#v, want duplicate-key JSON error", got)
	}
}

func newClient(
	t *testing.T,
	server *httptest.Server,
	mutate func(*pubmed.Config),
	dependencies httpclient.Dependencies,
) *pubmed.Client {
	t.Helper()

	config := pubmed.Config{
		BaseURL:          server.URL,
		Tool:             "paper-hub-test",
		Email:            "research@example.test",
		APIKey:           "ncbi-test-secret",
		UserAgent:        "paper-hub-pubmed-test/1.0",
		BatchSize:        2,
		Timeout:          time.Second,
		MaxRetries:       2,
		MaxWait:          5 * time.Second,
		InitialBackoff:   time.Millisecond,
		MaxBackoff:       10 * time.Millisecond,
		MaxResponseBytes: 1 << 20,
	}
	if mutate != nil {
		mutate(&config)
	}
	client, err := pubmed.NewClient(server.Client(), config, dependencies)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func validSearchQuery() pubmed.SearchQuery {
	return pubmed.SearchQuery{
		DateType: pubmed.DateTypeEntrez,
		DateWindow: pubmed.DateWindow{
			From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC),
		},
		MaxResults: 1,
	}
}

func collect(sequence source.ClientSequence) ([]source.Record, []error) {
	records := make([]source.Record, 0)
	errs := make([]error, 0)
	sequence(func(record source.Record, err error) bool {
		if err != nil {
			errs = append(errs, err)
		} else {
			records = append(records, record)
		}
		return true
	})
	return records, errs
}

func recordPMIDs(records []source.Record) []string {
	result := make([]string, 0, len(records))
	for _, record := range records {
		for _, identifier := range record.Identifiers {
			if identifier.Scheme == source.IdentifierPMID {
				result = append(result, identifier.Value)
			}
		}
	}
	return result
}

type recordingClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (clock *recordingClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *recordingClock) Sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.sleeps = append(clock.sleeps, delay)
	clock.now = clock.now.Add(delay)
	return nil
}

func (clock *recordingClock) Sleeps() []time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return append([]time.Duration(nil), clock.sleeps...)
}
