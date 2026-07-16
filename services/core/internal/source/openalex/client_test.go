package openalex_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/openalex"
)

func TestClientFetchUsesCursorPaginationAndHardMaxResults(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		cursors []string
		perPage []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/works" {
			t.Errorf("path = %q, want /works", request.URL.Path)
		}
		query := request.URL.Query()
		mu.Lock()
		cursors = append(cursors, query.Get("cursor"))
		perPage = append(perPage, query.Get("per_page"))
		mu.Unlock()

		if query.Get("api_key") != "openalex-test-secret" {
			t.Errorf("api_key = %q, want configured credential", query.Get("api_key"))
		}
		if query.Get("mailto") != "research@example.test" {
			t.Errorf("mailto = %q, want contact email", query.Get("mailto"))
		}
		if query.Get("search") != "LLM agents" {
			t.Errorf("search = %q, want query", query.Get("search"))
		}
		if query.Get("filter") != "from_publication_date:2026-01-01" {
			t.Errorf("filter = %q, want exact filter", query.Get("filter"))
		}
		if request.Header.Get("User-Agent") != "paper-hub-openalex-test/1.0" {
			t.Errorf("User-Agent = %q, want configured value", request.Header.Get("User-Agent"))
		}

		switch query.Get("cursor") {
		case "*":
			writeEnvelope(t, writer, "cursor-2", "W1", "W2")
		case "cursor-2":
			// Return more records than requested to prove the client enforces its
			// own max_results hard limit instead of trusting the server.
			writeEnvelope(t, writer, "cursor-3", "W3", "W4")
		default:
			t.Errorf("unexpected cursor %q", query.Get("cursor"))
			writeEnvelope(t, writer, "", "W999")
		}
	}))
	defer server.Close()

	client := newClient(t, server, func(cfg *openalex.Config) {
		cfg.PerPage = 2
	})
	records, errs := collect(client.Fetch(context.Background(), source.Query{
		Search:     "LLM agents",
		Filter:     "from_publication_date:2026-01-01",
		MaxResults: 3,
	}))
	if len(errs) != 0 {
		t.Fatalf("Fetch() errors = %v", errs)
	}
	if len(records) != 3 {
		t.Fatalf("Fetch() records = %d, want hard max 3", len(records))
	}
	gotIDs := make([]string, 0, len(records))
	for _, record := range records {
		gotIDs = append(gotIDs, record.SourceRecordID)
	}
	if !slices.Equal(gotIDs, []string{"W1", "W2", "W3"}) {
		t.Fatalf("record IDs = %v, want first 3 records", gotIDs)
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(cursors, []string{"*", "cursor-2"}) {
		t.Fatalf("cursors = %v, want cursor deep pagination", cursors)
	}
	if !slices.Equal(perPage, []string{"2", "1"}) {
		t.Fatalf("per_page = %v, want remaining max applied to page size", perPage)
	}
}

func TestNewClientRequiresCurrentOpenAlexCredentialsAndBounds(t *testing.T) {
	t.Parallel()

	valid := validOpenAlexConfig("https://api.openalex.org")
	tests := []struct {
		name   string
		mutate func(*openalex.Config)
		want   string
	}{
		{name: "api key", mutate: func(cfg *openalex.Config) { cfg.APIKey = "" }, want: "API key"},
		{name: "contact", mutate: func(cfg *openalex.Config) { cfg.ContactEmail = "" }, want: "contact"},
		{name: "invalid contact", mutate: func(cfg *openalex.Config) { cfg.ContactEmail = "not-an-email" }, want: "contact"},
		{name: "per page zero", mutate: func(cfg *openalex.Config) { cfg.PerPage = 0 }, want: "per_page"},
		{name: "per page above API max", mutate: func(cfg *openalex.Config) { cfg.PerPage = 101 }, want: "per_page"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid
			tt.mutate(&cfg)
			client, err := openalex.NewClient(http.DefaultClient, cfg, httpclient.Dependencies{})
			if err == nil {
				t.Fatalf("NewClient() = %#v, want error", client)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("NewClient() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestClientFetchRejectsNonPositiveMaxResultsWithoutEmptySuccess(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeEnvelope(t, writer, "", "W1")
	}))
	defer server.Close()

	client := newClient(t, server, nil)
	records, errs := collect(client.Fetch(context.Background(), source.Query{MaxResults: 0}))
	if len(records) != 0 || len(errs) != 1 {
		t.Fatalf("Fetch() = %d records, %d errors; want explicit single error", len(records), len(errs))
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want validation before network", requests.Load())
	}
}

func TestClientFetchYieldsMalformedJSONAsExplicitError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"meta":{"next_cursor":"cursor-2"},"results":[`))
	}))
	defer server.Close()

	client := newClient(t, server, nil)
	records, errs := collect(client.Fetch(context.Background(), source.Query{MaxResults: 10}))
	if len(records) != 0 || len(errs) != 1 {
		t.Fatalf("Fetch() = %d records, %d errors; want malformed response error", len(records), len(errs))
	}
	if !strings.Contains(strings.ToLower(errs[0].Error()), "json") {
		t.Fatalf("Fetch() error = %v, want malformed JSON context", errs[0])
	}
}

func TestClientFetchRejectsMissingResultsEnvelope(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"meta":{"next_cursor":null}}`))
	}))
	defer server.Close()

	client := newClient(t, server, nil)
	records, errs := collect(client.Fetch(context.Background(), source.Query{MaxResults: 10}))
	if len(records) != 0 || len(errs) != 1 {
		t.Fatalf("Fetch() = %d records, %d errors; want invalid envelope error", len(records), len(errs))
	}
	if !strings.Contains(strings.ToLower(errs[0].Error()), "results") {
		t.Fatalf("Fetch() error = %v, want missing results context", errs[0])
	}
}

func TestClientFetchReportsExactMalformedRecordPosition(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"meta":{"next_cursor":null},
			"results":[
				{"id":"https://openalex.org/W1"},
				{"title":"missing required identifier"}
			]
		}`))
	}))
	defer server.Close()

	client := newClient(t, server, nil)
	records, errs := collect(client.Fetch(context.Background(), source.Query{MaxResults: 10}))
	if len(records) != 1 || len(errs) != 1 {
		t.Fatalf("Fetch() = %d records, %d errors; want first record plus exact parse error", len(records), len(errs))
	}
	if !strings.Contains(errs[0].Error(), "result 2") {
		t.Fatalf("Fetch() error = %v, want exact malformed record position 2", errs[0])
	}
}

func TestClientFetchPropagatesHTTPErrorInsteadOfReturningEmptySuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "invalid search", http.StatusBadRequest)
	}))
	defer server.Close()

	client := newClient(t, server, nil)
	records, errs := collect(client.Fetch(context.Background(), source.Query{MaxResults: 10}))
	if len(records) != 0 || len(errs) != 1 {
		t.Fatalf("Fetch() = %d records, %d errors; want explicit HTTP error", len(records), len(errs))
	}
	if !strings.Contains(errs[0].Error(), "400") {
		t.Fatalf("Fetch() error = %v, want HTTP status", errs[0])
	}
	if strings.Contains(errs[0].Error(), "openalex-test-secret") {
		t.Fatalf("Fetch() error leaked API key: %v", errs[0])
	}
}

func TestClientFetchRejectsNonAdvancingCursor(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cursor := request.URL.Query().Get("cursor")
		if cursor == "*" {
			writeEnvelope(t, writer, "stuck", "W1")
			return
		}
		writeEnvelope(t, writer, "stuck", "W2")
	}))
	defer server.Close()

	client := newClient(t, server, func(cfg *openalex.Config) {
		cfg.PerPage = 1
	})
	records, errs := collect(client.Fetch(context.Background(), source.Query{MaxResults: 3}))
	if len(records) != 2 || len(errs) != 1 {
		t.Fatalf("Fetch() = %d records, %d errors; want prior records plus cursor error", len(records), len(errs))
	}
	if !strings.Contains(strings.ToLower(errs[0].Error()), "cursor") {
		t.Fatalf("Fetch() error = %v, want cursor progress error", errs[0])
	}
}

func newClient(
	t *testing.T,
	server *httptest.Server,
	mutate func(*openalex.Config),
) *openalex.Client {
	t.Helper()

	cfg := validOpenAlexConfig(server.URL)
	if mutate != nil {
		mutate(&cfg)
	}
	client, err := openalex.NewClient(server.Client(), cfg, httpclient.Dependencies{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func validOpenAlexConfig(baseURL string) openalex.Config {
	return openalex.Config{
		BaseURL:      baseURL,
		APIKey:       "openalex-test-secret",
		ContactEmail: "research@example.test",
		UserAgent:    "paper-hub-openalex-test/1.0",
		PerPage:      100,
		Timeout:      time.Second,
		RateLimit: httpclient.RateLimit{
			Requests: 1000,
			Interval: time.Second,
		},
		MaxRetries:       2,
		MaxWait:          5 * time.Second,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       100 * time.Millisecond,
		MaxResponseBytes: 1 << 20,
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

func writeEnvelope(t *testing.T, writer http.ResponseWriter, nextCursor string, ids ...string) {
	t.Helper()

	results := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		results = append(results, json.RawMessage(fmt.Sprintf(`{"id":"https://openalex.org/%s"}`, id)))
	}
	var cursor any
	if nextCursor != "" {
		cursor = nextCursor
	}
	payload := struct {
		Meta struct {
			NextCursor any `json:"next_cursor"`
		} `json:"meta"`
		Results []json.RawMessage `json:"results"`
	}{Results: results}
	payload.Meta.NextCursor = cursor

	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		t.Errorf("Encode() error = %v", err)
	}
}

var _ source.Client = (*openalex.Client)(nil)
