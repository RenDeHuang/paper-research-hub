package venueenrich

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
)

func TestCrossrefCatalogFetchesTwoPagesAndRecordsHashes(t *testing.T) {
	t.Parallel()

	pageOne := readCrossrefCatalogFixture(t, "crossref-journals-page-1.json")
	pageTwo := readCrossrefCatalogFixture(t, "crossref-journals-page-2.json")
	cacheDir := t.TempDir()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/journals" {
			t.Errorf("request = %s %s, want GET /journals", request.Method, request.URL.Path)
		}
		if got := request.URL.Query().Get("rows"); got != "1000" {
			t.Errorf("rows = %q, want 1000", got)
		}
		if got := request.URL.Query().Get("mailto"); got != "catalog@example.test" {
			t.Errorf("mailto = %q, want configured contact", got)
		}
		if got := request.Header.Get("User-Agent"); got != "paper-hub-catalog-test/1.0" {
			t.Errorf("User-Agent = %q", got)
		}
		if _, err := os.Stat(filepath.Join(
			cacheDir,
			crossrefCatalogFinalDirectoryName,
		)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("final catalog became visible during live fetch: %v", err)
		}
		switch cursor := request.URL.Query().Get("cursor"); cursor {
		case "*":
			_, _ = writer.Write(pageOne)
		case "page-2-cursor":
			_, _ = writer.Write(pageTwo)
		default:
			t.Errorf("unexpected cursor %q", cursor)
		}
	}))
	defer server.Close()

	fetchedAt := time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC)
	config := validCrossrefCatalogConfig(server.URL, cacheDir)
	result, err := FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{Now: func() time.Time { return fetchedAt }},
	)
	if err != nil {
		t.Fatalf("FetchCrossrefCatalog() error = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if result.Replayed || result.Resumed {
		t.Fatalf("fresh result replayed=%t resumed=%t", result.Replayed, result.Resumed)
	}
	if got := journalTitles(result.Journals); !slices.Equal(got, []string{
		"Journal of Reproducible Metadata",
		"Transactions on Exact Matching",
		"Archives of Bounded Acquisition",
	}) {
		t.Fatalf("journal titles = %v", got)
	}
	first := result.Journals[0]
	if !slices.Equal(first.ISSNs, []string{"0028-0836", "2049-3630"}) ||
		first.PrintISSN != "0028-0836" ||
		first.ElectronicISSN != "2049-3630" ||
		first.Publisher != "Deterministic Publishing Society" ||
		first.TotalDOIs != 25 {
		t.Fatalf("first journal = %#v", first)
	}
	if !bytes.Contains(first.Raw, []byte(`"future-field"`)) {
		t.Fatalf("first raw journal lost unknown source field: %s", first.Raw)
	}

	catalogBytes, err := os.ReadFile(result.CatalogPath)
	if err != nil {
		t.Fatalf("ReadFile(catalog) error = %v", err)
	}
	if bytes.Count(catalogBytes, []byte{'\n'}) != 3 ||
		!bytes.HasSuffix(catalogBytes, []byte{'\n'}) {
		t.Fatalf("catalog JSONL framing = %q", catalogBytes)
	}
	catalogHash := sha256.Sum256(catalogBytes)
	if result.Manifest.SchemaVersion != CrossrefCatalogSchemaVersion ||
		result.Manifest.SourceURL != server.URL+"/journals" ||
		!result.Manifest.FetchedAt.Equal(fetchedAt) ||
		result.Manifest.Rows != CrossrefCatalogRows ||
		result.Manifest.RecordCount != 3 ||
		result.Manifest.TotalResults == nil ||
		*result.Manifest.TotalResults != 3 ||
		result.Manifest.CatalogBytes != int64(len(catalogBytes)) ||
		result.Manifest.CatalogSHA256 != hex.EncodeToString(catalogHash[:]) ||
		!result.Manifest.Complete {
		t.Fatalf("manifest = %#v", result.Manifest)
	}
	if len(result.Manifest.Pages) != 2 {
		t.Fatalf("page receipts = %d, want 2", len(result.Manifest.Pages))
	}
	assertCrossrefCatalogReceipt(
		t,
		filepath.Dir(result.ManifestPath),
		result.Manifest.Pages[0],
		1,
		"*",
		"page-2-cursor",
		2,
		pageOne,
	)
	assertCrossrefCatalogReceipt(
		t,
		filepath.Dir(result.ManifestPath),
		result.Manifest.Pages[1],
		2,
		"page-2-cursor",
		"terminal-cursor",
		1,
		pageTwo,
	)

	manifestBytes, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	var persisted CrossrefCatalogManifest
	if err := json.Unmarshal(manifestBytes, &persisted); err != nil {
		t.Fatalf("Unmarshal(manifest) error = %v", err)
	}
	if !persisted.Complete || persisted.CatalogSHA256 != result.Manifest.CatalogSHA256 {
		t.Fatalf("persisted manifest = %#v", persisted)
	}
	if filepath.Dir(result.CatalogPath) != filepath.Join(
		config.CacheDir,
		crossrefCatalogFinalDirectoryName,
	) {
		t.Fatalf("catalog path = %q, want atomically published final directory", result.CatalogPath)
	}
	if _, err := os.Stat(filepath.Join(
		config.CacheDir,
		crossrefCatalogPartialDirectoryName,
	)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified partial cache remains after publish: %v", err)
	}
	assertNoCrossrefCatalogTempFiles(t, config.CacheDir)
}

func TestCrossrefCatalogRejectsDuplicateCursor(t *testing.T) {
	t.Parallel()

	payload := crossrefJournalEnvelope(
		t,
		2,
		"*",
		validCrossrefJournalItem("Loop Journal", "0028-0836"),
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	config := validCrossrefCatalogConfig(server.URL, t.TempDir())
	_, err := FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{},
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		t.Fatalf("FetchCrossrefCatalog() error = %v, want duplicate cursor", err)
	}
	assertCrossrefCatalogIncomplete(t, config.CacheDir)
}

func TestCrossrefCatalogRejectsMalformedEnvelopeItemsAndFraming(t *testing.T) {
	t.Parallel()

	validItem := validCrossrefJournalItem("Valid Journal", "0028-0836")
	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{
			name: "status",
			payload: []byte(`{
				"status":"error","message-type":"journal-list",
				"message":{"total-results":0,"next-cursor":"","items":[]}
			}`),
			want: "status",
		},
		{
			name: "message type",
			payload: []byte(`{
				"status":"ok","message-type":"work-list",
				"message":{"total-results":0,"next-cursor":"","items":[]}
			}`),
			want: "message-type",
		},
		{
			name: "message object",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list",
				"message-version":"1.0.0","message":[]
			}`),
			want: "message object",
		},
		{
			name: "items array",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{"total-results":1,"next-cursor":"next","items":{}}
			}`),
			want: "items array",
		},
		{
			name: "missing next cursor",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{"total-results":0,"items":[]}
			}`),
			want: "next-cursor",
		},
		{
			name: "duplicate key",
			payload: []byte(`{
				"status":"ok","status":"ok","message-type":"journal-list",
				"message":{"total-results":0,"next-cursor":"","items":[]}
			}`),
			want: "duplicate",
		},
		{
			name: "multiple JSON values",
			payload: append(
				crossrefJournalEnvelope(t, 1, "terminal", validItem),
				[]byte(` {}`)...,
			),
			want: "exactly one",
		},
		{
			name: "invalid UTF-8",
			payload: append(
				[]byte(`{"status":"ok","message-type":"journal-list","message":{"total-results":0,"next-cursor":"","items":[]},"bad":"`),
				0xff, '"', '}',
			),
			want: "UTF-8",
		},
		{
			name: "unknown envelope field",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","unexpected":true,
				"message":{"total-results":0,"next-cursor":"","items":[]}
			}`),
			want: "unexpected",
		},
		{
			name: "unknown message field",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{
					"total-results":0,"items-per-page":0,"next-cursor":"",
					"query":{"start-index":0,"search-terms":null},"items":[],
					"unexpected":true
				}
			}`),
			want: "unexpected",
		},
		{
			name: "missing message version",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list",
				"message":{
					"total-results":0,"items-per-page":0,"next-cursor":"",
					"query":{"start-index":0,"search-terms":null},"items":[]
				}
			}`),
			want: "message-version",
		},
		{
			name: "unsupported message version",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"2.0.0",
				"message":{
					"total-results":0,"items-per-page":0,"next-cursor":"",
					"query":{"start-index":0,"search-terms":null},"items":[]
				}
			}`),
			want: "1.0.0",
		},
		{
			name: "missing items per page",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{
					"total-results":0,"next-cursor":"",
					"query":{"start-index":0,"search-terms":null},"items":[]
				}
			}`),
			want: "items-per-page",
		},
		{
			name: "items per page mismatch",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{
					"total-results":0,"items-per-page":1,"next-cursor":"",
					"query":{"start-index":0,"search-terms":null},"items":[]
				}
			}`),
			want: "actual items",
		},
		{
			name: "missing query",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{
					"total-results":0,"items-per-page":0,"next-cursor":"","items":[]
				}
			}`),
			want: "query",
		},
		{
			name: "query start index type",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{
					"total-results":0,"items-per-page":0,"next-cursor":"",
					"query":{"start-index":"0","search-terms":null},"items":[]
				}
			}`),
			want: "start-index",
		},
		{
			name: "query start index value",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{
					"total-results":0,"items-per-page":0,"next-cursor":"",
					"query":{"start-index":1,"search-terms":null},"items":[]
				}
			}`),
			want: "want 0",
		},
		{
			name: "query search terms",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{
					"total-results":0,"items-per-page":0,"next-cursor":"",
					"query":{"start-index":0,"search-terms":"journal"},"items":[]
				}
			}`),
			want: "search-terms",
		},
		{
			name: "query unexpected field",
			payload: []byte(`{
				"status":"ok","message-type":"journal-list","message-version":"1.0.0",
				"message":{
					"total-results":0,"items-per-page":0,"next-cursor":"",
					"query":{"start-index":0,"search-terms":null,"cursor":"unexpected"},
					"items":[]
				}
			}`),
			want: "unexpected",
		},
		{
			name: "malformed title",
			payload: crossrefJournalEnvelope(
				t,
				1,
				"terminal",
				json.RawMessage(`{
					"title":["not a string"],
					"ISSN":["0028-0836"],
					"issn-type":[{"value":"0028-0836","type":"print"}],
					"publisher":"Publisher",
					"counts":{"total-dois":1}
				}`),
			),
			want: "title",
		},
		{
			name: "malformed ISSN",
			payload: crossrefJournalEnvelope(
				t,
				1,
				"terminal",
				validCrossrefJournalItem("Bad ISSN Journal", "not-an-issn"),
			),
			want: "ISSN",
		},
		{
			name: "malformed issn type",
			payload: crossrefJournalEnvelope(
				t,
				1,
				"terminal",
				json.RawMessage(`{
					"title":"Bad Type Journal",
					"ISSN":["0028-0836"],
					"issn-type":[{"value":"0028-0836","type":"online"}],
					"publisher":"Publisher",
					"counts":{"total-dois":1}
				}`),
			),
			want: "issn-type",
		},
		{
			name: "malformed publisher",
			payload: crossrefJournalEnvelope(
				t,
				1,
				"terminal",
				json.RawMessage(`{
					"title":"Bad Publisher Journal",
					"ISSN":["0028-0836"],
					"issn-type":[{"value":"0028-0836","type":"print"}],
					"publisher":null,
					"counts":{"total-dois":1}
				}`),
			),
			want: "publisher",
		},
		{
			name: "malformed counts",
			payload: crossrefJournalEnvelope(
				t,
				1,
				"terminal",
				json.RawMessage(`{
					"title":"Bad Count Journal",
					"ISSN":["0028-0836"],
					"issn-type":[{"value":"0028-0836","type":"print"}],
					"publisher":"Publisher",
					"counts":{"total-dois":1.5}
				}`),
			),
			want: "total-dois",
		},
		{
			name:    "truncated JSON",
			payload: []byte(`{"status":"ok","message-type":"journal-list","message":`),
			want:    "JSON",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				_, _ = writer.Write(test.payload)
			}))
			defer server.Close()

			config := validCrossrefCatalogConfig(server.URL, t.TempDir())
			_, err := FetchCrossrefCatalog(
				context.Background(),
				server.Client(),
				config,
				httpclient.Dependencies{},
			)
			if err == nil ||
				!strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("FetchCrossrefCatalog() error = %v, want containing %q", err, test.want)
			}
			assertCrossrefCatalogIncomplete(t, config.CacheDir)
		})
	}
}

func TestCrossrefCatalogAcceptsObservedJournalISSNVariants(t *testing.T) {
	t.Parallel()

	payload := crossrefJournalEnvelope(
		t,
		1,
		"terminal",
		json.RawMessage(`{
			"title":"Observed Crossref Variant",
			"ISSN":["1617-7061","1617-7061","1521-3979"],
			"issn-type":[
				{"value":"1617-7061","type":"print"},
				{"value":"1617-7061","type":"electronic"},
				{"value":"1521-3979","type":null}
			],
			"publisher":"Observed Publisher",
			"counts":{"total-dois":7}
		}`),
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	config := validCrossrefCatalogConfig(server.URL, t.TempDir())
	result, err := FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{},
	)
	if err != nil {
		t.Fatalf("FetchCrossrefCatalog() error = %v", err)
	}
	if len(result.Journals) != 1 {
		t.Fatalf("journals = %d, want 1", len(result.Journals))
	}
	journal := result.Journals[0]
	if !slices.Equal(journal.ISSNs, []string{"1617-7061", "1521-3979"}) ||
		journal.PrintISSN != "1617-7061" ||
		journal.ElectronicISSN != "1617-7061" ||
		!slices.Equal(journal.ISSNTypes, []CrossrefJournalISSNType{
			{Value: "1617-7061", Type: "print"},
			{Value: "1617-7061", Type: "electronic"},
		}) {
		t.Fatalf("journal ISSN parsing = %#v", journal)
	}
	if !bytes.Contains(journal.Raw, []byte(`"type":null`)) {
		t.Fatalf("raw journal lost unclassified issn-type: %s", journal.Raw)
	}
}

func TestCrossrefCatalogAcceptsObservedEmptyISSNArrays(t *testing.T) {
	t.Parallel()

	payload := crossrefJournalEnvelope(
		t,
		1,
		"terminal",
		json.RawMessage(`{
			"title":"Observed Journal Without ISSNs",
			"ISSN":[],
			"issn-type":[],
			"publisher":"Observed Publisher",
			"counts":{"total-dois":0}
		}`),
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	config := validCrossrefCatalogConfig(server.URL, t.TempDir())
	result, err := FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{},
	)
	if err != nil {
		t.Fatalf("FetchCrossrefCatalog() error = %v", err)
	}
	if len(result.Journals) != 1 ||
		len(result.Journals[0].ISSNs) != 0 ||
		len(result.Journals[0].ISSNTypes) != 0 {
		t.Fatalf("journal = %#v, want parsed empty ISSN arrays", result.Journals)
	}
}

func TestCrossrefCatalogAcceptsEmptyTerminalCatalog(t *testing.T) {
	t.Parallel()

	payload := crossrefJournalEnvelope(t, 0, "")
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	config := validCrossrefCatalogConfig(server.URL, t.TempDir())
	result, err := FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{},
	)
	if err != nil {
		t.Fatalf("FetchCrossrefCatalog() error = %v", err)
	}
	if len(result.Journals) != 0 ||
		!result.Manifest.Complete ||
		result.Manifest.TotalResults == nil ||
		*result.Manifest.TotalResults != 0 ||
		len(result.Manifest.Pages) != 1 ||
		result.Manifest.Pages[0].RecordCount != 0 {
		t.Fatalf("empty terminal catalog result = %#v", result)
	}
}

func TestCrossrefCatalogRejectsProtocolTruncationAndOversizedPages(t *testing.T) {
	t.Parallel()

	item := validCrossrefJournalItem("Protocol Journal", "0028-0836")
	oversized := make([]json.RawMessage, CrossrefCatalogRows+1)
	for index := range oversized {
		oversized[index] = item
	}
	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{
			name:    "empty page with continuation",
			payload: crossrefJournalEnvelope(t, 1, "continue", nil),
			want:    "empty",
		},
		{
			name:    "early EOF",
			payload: crossrefJournalEnvelope(t, 2, "", item),
			want:    "early EOF",
		},
		{
			name:    "items over rows",
			payload: crossrefJournalEnvelope(t, int64(len(oversized)), "continue", oversized...),
			want:    "rows",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				_, _ = writer.Write(test.payload)
			}))
			defer server.Close()

			config := validCrossrefCatalogConfig(server.URL, t.TempDir())
			config.MaxResponseBytes = int64(len(test.payload) + 1024)
			_, err := FetchCrossrefCatalog(
				context.Background(),
				server.Client(),
				config,
				httpclient.Dependencies{},
			)
			if err == nil ||
				!strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("FetchCrossrefCatalog() error = %v, want containing %q", err, test.want)
			}
			assertCrossrefCatalogIncomplete(t, config.CacheDir)
		})
	}
}

func TestCrossrefCatalogRequiresAndAppliesBoundedHTTPPolicy(t *testing.T) {
	t.Parallel()

	valid := validCrossrefCatalogConfig("https://api.crossref.test", t.TempDir())
	tests := []struct {
		name   string
		mutate func(*CrossrefCatalogConfig)
		want   string
	}{
		{name: "base URL", mutate: func(config *CrossrefCatalogConfig) { config.BaseURL = "" }, want: "base URL"},
		{name: "cache directory", mutate: func(config *CrossrefCatalogConfig) { config.CacheDir = "" }, want: "cache"},
		{name: "contact email", mutate: func(config *CrossrefCatalogConfig) { config.ContactEmail = "Display <x@example.test>" }, want: "email"},
		{name: "user agent", mutate: func(config *CrossrefCatalogConfig) { config.UserAgent = "" }, want: "User-Agent"},
		{name: "timeout", mutate: func(config *CrossrefCatalogConfig) { config.Timeout = 0 }, want: "timeout"},
		{name: "rate requests", mutate: func(config *CrossrefCatalogConfig) { config.RateLimit.Requests = 0 }, want: "rate"},
		{name: "rate interval", mutate: func(config *CrossrefCatalogConfig) { config.RateLimit.Interval = 0 }, want: "rate"},
		{name: "retries", mutate: func(config *CrossrefCatalogConfig) { config.MaxRetries = -1 }, want: "retries"},
		{name: "max wait", mutate: func(config *CrossrefCatalogConfig) { config.MaxWait = 0 }, want: "wait"},
		{name: "initial backoff", mutate: func(config *CrossrefCatalogConfig) { config.InitialBackoff = 0 }, want: "backoff"},
		{name: "max backoff", mutate: func(config *CrossrefCatalogConfig) { config.MaxBackoff = 0 }, want: "backoff"},
		{name: "response bytes", mutate: func(config *CrossrefCatalogConfig) { config.MaxResponseBytes = 0 }, want: "limit"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			config.CacheDir = t.TempDir()
			test.mutate(&config)
			_, err := FetchCrossrefCatalog(
				context.Background(),
				http.DefaultClient,
				config,
				httpclient.Dependencies{},
			)
			if err == nil ||
				!strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("FetchCrossrefCatalog() error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("rate limit", func(t *testing.T) {
		pageOne := readCrossrefCatalogFixture(t, "crossref-journals-page-1.json")
		pageTwo := readCrossrefCatalogFixture(t, "crossref-journals-page-2.json")
		server := httptest.NewServer(http.HandlerFunc(func(
			writer http.ResponseWriter,
			request *http.Request,
		) {
			if request.URL.Query().Get("cursor") == "*" {
				_, _ = writer.Write(pageOne)
				return
			}
			_, _ = writer.Write(pageTwo)
		}))
		defer server.Close()

		var (
			mu     sync.Mutex
			now    = time.Unix(0, 0)
			sleeps []time.Duration
		)
		config := validCrossrefCatalogConfig(server.URL, t.TempDir())
		config.RateLimit = httpclient.RateLimit{Requests: 1, Interval: time.Second}
		_, err := FetchCrossrefCatalog(
			context.Background(),
			server.Client(),
			config,
			httpclient.Dependencies{
				Now: func() time.Time {
					mu.Lock()
					defer mu.Unlock()
					return now
				},
				Sleep: func(ctx context.Context, delay time.Duration) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					mu.Lock()
					sleeps = append(sleeps, delay)
					now = now.Add(delay)
					mu.Unlock()
					return nil
				},
			},
		)
		if err != nil {
			t.Fatalf("FetchCrossrefCatalog() error = %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(sleeps, []time.Duration{time.Second}) {
			t.Fatalf("rate-limit sleeps = %v, want [1s]", sleeps)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		config := validCrossrefCatalogConfig("https://api.crossref.test", t.TempDir())
		config.Timeout = 10 * time.Millisecond
		_, err := FetchCrossrefCatalog(
			context.Background(),
			&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			})},
			config,
			httpclient.Dependencies{},
		)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "deadline") {
			t.Fatalf("FetchCrossrefCatalog() error = %v, want timeout", err)
		}
	})

	t.Run("retry wait budget", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			writer http.ResponseWriter,
			_ *http.Request,
		) {
			writer.Header().Set("Retry-After", "10")
			http.Error(writer, "retry", http.StatusServiceUnavailable)
		}))
		defer server.Close()

		config := validCrossrefCatalogConfig(server.URL, t.TempDir())
		config.MaxRetries = 1
		config.MaxWait = time.Second
		_, err := FetchCrossrefCatalog(
			context.Background(),
			server.Client(),
			config,
			httpclient.Dependencies{Now: func() time.Time { return time.Unix(0, 0) }},
		)
		if !errors.Is(err, httpclient.ErrRetryBudgetExceeded) {
			t.Fatalf("FetchCrossrefCatalog() error = %v, want ErrRetryBudgetExceeded", err)
		}
	})

	t.Run("response body limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			writer http.ResponseWriter,
			_ *http.Request,
		) {
			_, _ = writer.Write([]byte(strings.Repeat("x", 1024)))
		}))
		defer server.Close()

		config := validCrossrefCatalogConfig(server.URL, t.TempDir())
		config.MaxResponseBytes = 32
		_, err := FetchCrossrefCatalog(
			context.Background(),
			server.Client(),
			config,
			httpclient.Dependencies{},
		)
		if !errors.Is(err, httpclient.ErrResponseTooLarge) {
			t.Fatalf("FetchCrossrefCatalog() error = %v, want ErrResponseTooLarge", err)
		}
	})

	t.Run("optional and sensitive mailto", func(t *testing.T) {
		payload := crossrefJournalEnvelope(
			t,
			1,
			"terminal",
			validCrossrefJournalItem("Optional Mailto Journal", "0028-0836"),
		)
		var sawMailto atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(
			writer http.ResponseWriter,
			request *http.Request,
		) {
			sawMailto.Store(request.URL.Query().Has("mailto"))
			_, _ = writer.Write(payload)
		}))
		config := validCrossrefCatalogConfig(server.URL, t.TempDir())
		config.ContactEmail = ""
		if _, err := FetchCrossrefCatalog(
			context.Background(),
			server.Client(),
			config,
			httpclient.Dependencies{},
		); err != nil {
			server.Close()
			t.Fatalf("FetchCrossrefCatalog(optional mailto) error = %v", err)
		}
		server.Close()
		if sawMailto.Load() {
			t.Fatal("request unexpectedly included optional mailto")
		}

		config = validCrossrefCatalogConfig("https://api.crossref.test", t.TempDir())
		_, err := FetchCrossrefCatalog(
			context.Background(),
			&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("dial %s failed", request.URL.String())
			})},
			config,
			httpclient.Dependencies{},
		)
		if err == nil {
			t.Fatal("FetchCrossrefCatalog(sensitive mailto) error = nil")
		}
		if strings.Contains(err.Error(), config.ContactEmail) ||
			!strings.Contains(err.Error(), "[REDACTED]") {
			t.Fatalf("FetchCrossrefCatalog() leaked mailto: %v", err)
		}
	})
}

func TestCrossrefCatalogReplaysOnlyVerifiedCompleteCache(t *testing.T) {
	t.Parallel()

	pageOne := readCrossrefCatalogFixture(t, "crossref-journals-page-1.json")
	pageTwo := readCrossrefCatalogFixture(t, "crossref-journals-page-2.json")
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Query().Get("cursor") == "*" {
			_, _ = writer.Write(pageOne)
			return
		}
		_, _ = writer.Write(pageTwo)
	}))
	defer server.Close()

	config := validCrossrefCatalogConfig(server.URL, t.TempDir())
	first, err := FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{},
	)
	if err != nil {
		t.Fatalf("initial FetchCrossrefCatalog() error = %v", err)
	}

	var networkCalls atomic.Int32
	replayed, err := FetchCrossrefCatalog(
		context.Background(),
		&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			networkCalls.Add(1)
			return nil, errors.New("network must not be used for verified replay")
		})},
		config,
		httpclient.Dependencies{},
	)
	if err != nil {
		t.Fatalf("replay FetchCrossrefCatalog() error = %v", err)
	}
	if !replayed.Replayed || replayed.Resumed || networkCalls.Load() != 0 {
		t.Fatalf(
			"replay flags/calls = replayed:%t resumed:%t calls:%d",
			replayed.Replayed,
			replayed.Resumed,
			networkCalls.Load(),
		)
	}
	if replayed.Manifest.CatalogSHA256 != first.Manifest.CatalogSHA256 ||
		!slices.Equal(journalTitles(replayed.Journals), journalTitles(first.Journals)) {
		t.Fatalf("replayed result differs from fetched result")
	}

	manifestBytes, err := os.ReadFile(first.ManifestPath)
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	invalidSchema := bytes.Replace(
		manifestBytes,
		[]byte(CrossrefCatalogSchemaVersion),
		[]byte("crossref-journal-catalog/v0"),
		1,
	)
	if err := os.WriteFile(first.ManifestPath, invalidSchema, 0o600); err != nil {
		t.Fatalf("WriteFile(invalid schema manifest) error = %v", err)
	}
	_, err = FetchCrossrefCatalog(
		context.Background(),
		&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			networkCalls.Add(1)
			return nil, errors.New("invalid schema must fail before network")
		})},
		config,
		httpclient.Dependencies{},
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "schema_version") {
		t.Fatalf("invalid schema replay error = %v", err)
	}
	if networkCalls.Load() != 0 {
		t.Fatalf("network calls after invalid schema = %d, want 0", networkCalls.Load())
	}
	if err := os.WriteFile(first.ManifestPath, manifestBytes, 0o600); err != nil {
		t.Fatalf("restore manifest error = %v", err)
	}

	pagePath := filepath.Join(
		filepath.Dir(first.ManifestPath),
		first.Manifest.Pages[0].PageFile,
	)
	pageBytes, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("ReadFile(raw page) error = %v", err)
	}
	if err := os.WriteFile(pagePath, append(pageBytes, ' '), 0o600); err != nil {
		t.Fatalf("WriteFile(tampered raw page) error = %v", err)
	}
	_, err = FetchCrossrefCatalog(
		context.Background(),
		&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			networkCalls.Add(1)
			return nil, errors.New("unverified page receipt must fail before network")
		})},
		config,
		httpclient.Dependencies{},
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "page sha") {
		t.Fatalf("tampered raw page replay error = %v, want page SHA verification failure", err)
	}
	if networkCalls.Load() != 0 {
		t.Fatalf("network calls after tampered raw page = %d, want 0", networkCalls.Load())
	}
	if err := os.WriteFile(pagePath, pageBytes, 0o600); err != nil {
		t.Fatalf("restore raw page error = %v", err)
	}

	catalog, err := os.OpenFile(first.CatalogPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile(catalog) error = %v", err)
	}
	if _, err := catalog.WriteString(`{"tampered":true}` + "\n"); err != nil {
		_ = catalog.Close()
		t.Fatalf("tamper catalog: %v", err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatalf("close tampered catalog: %v", err)
	}

	_, err = FetchCrossrefCatalog(
		context.Background(),
		&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			networkCalls.Add(1)
			return nil, errors.New("unverified cache must fail before network")
		})},
		config,
		httpclient.Dependencies{},
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "hash") {
		t.Fatalf("tampered replay error = %v, want hash verification failure", err)
	}
	if networkCalls.Load() != 0 {
		t.Fatalf("network calls after tampered cache = %d, want 0", networkCalls.Load())
	}
}

func TestCrossrefCatalogResumesOnlyVerifiedIncompleteCache(t *testing.T) {
	t.Parallel()

	pageOne := readCrossrefCatalogFixture(t, "crossref-journals-page-1.json")
	pageTwo := readCrossrefCatalogFixture(t, "crossref-journals-page-2.json")
	var failSecond atomic.Bool
	failSecond.Store(true)
	var (
		mu      sync.Mutex
		cursors []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		cursor := request.URL.Query().Get("cursor")
		mu.Lock()
		cursors = append(cursors, cursor)
		mu.Unlock()
		switch cursor {
		case "*":
			_, _ = writer.Write(pageOne)
		case "page-2-cursor":
			if failSecond.Load() {
				http.Error(writer, "interrupted", http.StatusInternalServerError)
				return
			}
			_, _ = writer.Write(pageTwo)
		default:
			t.Errorf("unexpected cursor %q", cursor)
		}
	}))
	defer server.Close()

	config := validCrossrefCatalogConfig(server.URL, t.TempDir())
	config.MaxRetries = 0
	_, err := FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{},
	)
	if err == nil {
		t.Fatal("interrupted FetchCrossrefCatalog() error = nil")
	}
	manifest := readCrossrefCatalogManifest(t, config.CacheDir)
	if manifest.Complete || manifest.RecordCount != 2 ||
		len(manifest.Pages) != 1 ||
		manifest.Pages[0].CursorOut != "page-2-cursor" {
		t.Fatalf("interrupted manifest = %#v", manifest)
	}
	if _, err := os.Stat(filepath.Join(
		config.CacheDir,
		crossrefCatalogFinalDirectoryName,
	)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed live run polluted final catalog: %v", err)
	}
	partialPagePath := filepath.Join(
		config.CacheDir,
		crossrefCatalogPartialDirectoryName,
		manifest.Pages[0].PageFile,
	)
	partialPage, err := os.ReadFile(partialPagePath)
	if err != nil {
		t.Fatalf("ReadFile(partial raw page) error = %v", err)
	}
	if err := os.WriteFile(
		partialPagePath,
		append(partialPage, '\n'),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(tampered partial page) error = %v", err)
	}
	mu.Lock()
	requestCountBeforeVerify := len(cursors)
	mu.Unlock()
	_, err = FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{},
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "page sha") {
		t.Fatalf("tampered partial resume error = %v, want page SHA verification failure", err)
	}
	mu.Lock()
	requestCountAfterVerify := len(cursors)
	mu.Unlock()
	if requestCountAfterVerify != requestCountBeforeVerify {
		t.Fatalf("unverified partial cache triggered network request")
	}
	if err := os.WriteFile(partialPagePath, partialPage, 0o600); err != nil {
		t.Fatalf("restore partial raw page error = %v", err)
	}

	failSecond.Store(false)
	resumed, err := FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{},
	)
	if err != nil {
		t.Fatalf("resumed FetchCrossrefCatalog() error = %v", err)
	}
	if resumed.Replayed || !resumed.Resumed || !resumed.Manifest.Complete {
		t.Fatalf(
			"resumed flags/manifest = replayed:%t resumed:%t complete:%t",
			resumed.Replayed,
			resumed.Resumed,
			resumed.Manifest.Complete,
		)
	}
	mu.Lock()
	gotCursors := append([]string(nil), cursors...)
	mu.Unlock()
	if !slices.Equal(gotCursors, []string{"*", "page-2-cursor", "page-2-cursor"}) {
		t.Fatalf("request cursors = %v, want resume from last verified cursor", gotCursors)
	}
}

func TestCrossrefCatalogFailureNeverLeavesCompleteManifest(t *testing.T) {
	t.Parallel()

	payload := crossrefJournalEnvelope(
		t,
		2,
		"continue",
		validCrossrefJournalItem("Only Journal", "0028-0836"),
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Query().Get("cursor") == "*" {
			_, _ = writer.Write(payload)
			return
		}
		http.Error(writer, "interrupted", http.StatusInternalServerError)
	}))
	defer server.Close()

	config := validCrossrefCatalogConfig(server.URL, t.TempDir())
	_, err := FetchCrossrefCatalog(
		context.Background(),
		server.Client(),
		config,
		httpclient.Dependencies{},
	)
	if err == nil {
		t.Fatal("FetchCrossrefCatalog() error = nil, want interruption")
	}
	manifest := readCrossrefCatalogManifest(t, config.CacheDir)
	if manifest.Complete {
		t.Fatalf("failed fetch left complete manifest: %#v", manifest)
	}
	finalDir := filepath.Join(config.CacheDir, crossrefCatalogFinalDirectoryName)
	if _, err := os.Stat(finalDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed fetch published final catalog directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		config.CacheDir,
		CrossrefCatalogJSONLName,
	)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed fetch polluted top-level final JSONL: %v", err)
	}
	assertNoCrossrefCatalogTempFiles(t, config.CacheDir)
}

func validCrossrefCatalogConfig(baseURL, cacheDir string) CrossrefCatalogConfig {
	return CrossrefCatalogConfig{
		BaseURL:          baseURL,
		CacheDir:         cacheDir,
		ContactEmail:     "catalog@example.test",
		UserAgent:        "paper-hub-catalog-test/1.0",
		Timeout:          time.Second,
		RateLimit:        httpclient.RateLimit{Requests: 1000, Interval: time.Second},
		MaxRetries:       0,
		MaxWait:          time.Second,
		InitialBackoff:   time.Millisecond,
		MaxBackoff:       time.Second,
		MaxResponseBytes: 4 << 20,
	}
}

func readCrossrefCatalogFixture(t *testing.T, name string) []byte {
	t.Helper()

	payload, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", name, err)
	}
	return payload
}

func validCrossrefJournalItem(title, issn string) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"title":     title,
		"ISSN":      []string{issn},
		"issn-type": []map[string]string{{"value": issn, "type": "print"}},
		"publisher": "Publisher",
		"counts":    map[string]int64{"total-dois": 1},
	})
	if err != nil {
		panic(err)
	}
	return payload
}

func crossrefJournalEnvelope(
	t *testing.T,
	totalResults int64,
	nextCursor string,
	items ...json.RawMessage,
) []byte {
	t.Helper()

	if len(items) == 1 && items[0] == nil {
		items = []json.RawMessage{}
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	payload, err := json.Marshal(map[string]any{
		"status":          "ok",
		"message-type":    "journal-list",
		"message-version": "1.0.0",
		"message": map[string]any{
			"total-results":  totalResults,
			"items-per-page": len(items),
			"next-cursor":    nextCursor,
			"query": map[string]any{
				"start-index":  0,
				"search-terms": nil,
			},
			"items": items,
		},
	})
	if err != nil {
		t.Fatalf("Marshal(envelope) error = %v", err)
	}
	return payload
}

func assertCrossrefCatalogReceipt(
	t *testing.T,
	generationDir string,
	receipt CrossrefCatalogPageReceipt,
	ordinal int,
	cursorIn string,
	cursorOut string,
	recordCount int,
	payload []byte,
) {
	t.Helper()

	hash := sha256.Sum256(payload)
	if receipt.Ordinal != ordinal ||
		receipt.CursorIn != cursorIn ||
		receipt.CursorOut != cursorOut ||
		receipt.RecordCount != recordCount ||
		receipt.PageFile != filepath.Join(
			crossrefCatalogPagesDirectoryName,
			fmt.Sprintf("page-%06d.json", ordinal),
		) ||
		receipt.PageBytes != int64(len(payload)) ||
		receipt.PageSHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("receipt = %#v", receipt)
	}
	rawPage, err := os.ReadFile(filepath.Join(generationDir, receipt.PageFile))
	if err != nil {
		t.Fatalf("ReadFile(receipt page) error = %v", err)
	}
	if !bytes.Equal(rawPage, payload) {
		t.Fatalf("receipt raw page differs from HTTP response")
	}
}

func assertCrossrefCatalogIncomplete(t *testing.T, cacheDir string) {
	t.Helper()

	manifest := readCrossrefCatalogManifest(t, cacheDir)
	if manifest.Complete {
		t.Fatalf("manifest complete = true after failure: %#v", manifest)
	}
}

func readCrossrefCatalogManifest(t *testing.T, cacheDir string) CrossrefCatalogManifest {
	t.Helper()

	finalPath := filepath.Join(
		cacheDir,
		crossrefCatalogFinalDirectoryName,
		CrossrefCatalogManifestName,
	)
	partialPath := filepath.Join(
		cacheDir,
		crossrefCatalogPartialDirectoryName,
		CrossrefCatalogManifestName,
	)
	path := finalPath
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		path = partialPath
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	var manifest CrossrefCatalogManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatalf("Unmarshal(manifest) error = %v", err)
	}
	return manifest
}

func assertNoCrossrefCatalogTempFiles(t *testing.T, cacheDir string) {
	t.Helper()

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("ReadDir(cache) error = %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary cache file remains: %s", entry.Name())
		}
	}
}

func journalTitles(journals []CrossrefJournal) []string {
	titles := make([]string, len(journals))
	for index, journal := range journals {
		titles[index] = journal.Title
	}
	return titles
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
