package crossref_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/crossref"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
)

func TestFetchWithPageReceiptsRecordsValidatedPagesBeforeYield(t *testing.T) {
	t.Parallel()

	firstPayload := envelopePayload(
		t,
		cursorValue("page 2"),
		item("10.1000/one"),
		item("10.1000/two"),
	)
	secondPayload := envelopePayload(t, noCursor())
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Query().Get("cursor") {
		case "*":
			_, _ = writer.Write(firstPayload)
		case "page 2":
			_, _ = writer.Write(secondPayload)
		default:
			t.Errorf("unexpected cursor %q", request.URL.Query().Get("cursor"))
		}
	}))
	defer server.Close()

	client := newClient(t, server, func(config *crossref.Config) {
		config.BatchSize = 2
	}, httpclient.Dependencies{})
	var (
		receipts       []crossref.PageReceipt
		recordsAtWrite []int
		records        []source.Record
		errs           []error
	)
	sequence := client.FetchWithPageReceipts(
		context.Background(),
		boundedQuery(10),
		func(_ context.Context, receipt crossref.PageReceipt) error {
			receipts = append(receipts, receipt)
			recordsAtWrite = append(recordsAtWrite, len(records))
			return nil
		},
	)
	for record, err := range sequence {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		records = append(records, record)
	}
	if len(errs) != 0 {
		t.Fatalf("FetchWithPageReceipts() errors = %v", errs)
	}
	if got := recordDOIs(records); !slices.Equal(got, []string{
		"10.1000/one",
		"10.1000/two",
	}) {
		t.Fatalf("record DOIs = %v", got)
	}
	if !slices.Equal(recordsAtWrite, []int{0, 2}) {
		t.Fatalf(
			"records visible when receipts persisted = %v, want receipt before each page yield",
			recordsAtWrite,
		)
	}
	if len(receipts) != 2 {
		t.Fatalf("receipts = %d, want 2 including terminal empty page", len(receipts))
	}
	assertPageReceipt(
		t,
		receipts[0],
		1,
		"*",
		"page 2",
		firstPayload,
		2,
	)
	assertPageReceipt(
		t,
		receipts[1],
		2,
		"page 2",
		"",
		secondPayload,
		0,
	)
}

func TestFetchWithPageReceiptsRejectsInvalidPageBeforeReceiptOrYield(t *testing.T) {
	t.Parallel()

	payload := envelopePayload(
		t,
		noCursor(),
		item("10.1000/one"),
		json.RawMessage(`{"title":["missing DOI"]}`),
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	client := newClient(t, server, func(config *crossref.Config) {
		config.BatchSize = 2
	}, httpclient.Dependencies{})
	receipts := 0
	records, errs := collect(client.FetchWithPageReceipts(
		context.Background(),
		boundedQuery(10),
		func(context.Context, crossref.PageReceipt) error {
			receipts++
			return nil
		},
	))
	if len(records) != 0 || len(errs) != 1 {
		t.Fatalf(
			"FetchWithPageReceipts() = %d records, %d errors; want atomic page rejection",
			len(records),
			len(errs),
		)
	}
	if receipts != 0 {
		t.Fatalf("receipts = %d, malformed page must not be acknowledged", receipts)
	}
	if !strings.Contains(strings.ToLower(errs[0].Error()), "item 2") {
		t.Fatalf("error = %v, want exact global item ordinal", errs[0])
	}
}

func TestFetchWithPageReceiptsStopsBeforeYieldWhenReceiptPersistenceFails(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		requests.Add(1)
		writeEnvelope(
			t,
			writer,
			cursorValue("page 2"),
			item("10.1000/one"),
		)
	}))
	defer server.Close()

	client := newClient(t, server, func(config *crossref.Config) {
		config.BatchSize = 1
	}, httpclient.Dependencies{})
	persistErr := errors.New("page receipt unavailable")
	records, errs := collect(client.FetchWithPageReceipts(
		context.Background(),
		boundedQuery(2),
		func(context.Context, crossref.PageReceipt) error {
			return persistErr
		},
	))
	if len(records) != 0 || len(errs) != 1 ||
		!errors.Is(errs[0], persistErr) {
		t.Fatalf(
			"FetchWithPageReceipts() = %d records, errors %v; want persistence failure",
			len(records),
			errs,
		)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want stop after failed first receipt", requests.Load())
	}
}

func assertPageReceipt(
	t *testing.T,
	receipt crossref.PageReceipt,
	ordinal int,
	cursorIn string,
	cursorOut string,
	payload []byte,
	recordCount int,
) {
	t.Helper()

	hash := sha256.Sum256(payload)
	if receipt.Ordinal != ordinal ||
		receipt.CursorIn != cursorIn ||
		receipt.CursorOut != cursorOut ||
		receipt.ContentSHA256 != hex.EncodeToString(hash[:]) ||
		receipt.RecordCount != recordCount {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestNewClientRequiresExplicitValidatedConfiguration(t *testing.T) {
	t.Parallel()

	valid := validConfig("https://api.crossref.test")
	tests := []struct {
		name   string
		mutate func(*crossref.Config)
		want   string
	}{
		{name: "base URL", mutate: func(config *crossref.Config) { config.BaseURL = "" }, want: "base URL"},
		{name: "base URL scheme", mutate: func(config *crossref.Config) { config.BaseURL = "ftp://api.crossref.test" }, want: "base URL"},
		{name: "base URL query", mutate: func(config *crossref.Config) { config.BaseURL += "?token=secret" }, want: "base URL"},
		{name: "contact email", mutate: func(config *crossref.Config) { config.ContactEmail = "" }, want: "contact email"},
		{name: "contact email syntax", mutate: func(config *crossref.Config) { config.ContactEmail = "Crossref User <user@example.test>" }, want: "contact email"},
		{name: "user agent", mutate: func(config *crossref.Config) { config.UserAgent = "" }, want: "User-Agent"},
		{name: "batch size zero", mutate: func(config *crossref.Config) { config.BatchSize = 0 }, want: "batch size"},
		{name: "batch size over API limit", mutate: func(config *crossref.Config) { config.BatchSize = 1001 }, want: "batch size"},
		{name: "timeout", mutate: func(config *crossref.Config) { config.Timeout = 0 }, want: "timeout"},
		{name: "rate requests", mutate: func(config *crossref.Config) { config.RateLimit.Requests = 0 }, want: "rate limit"},
		{name: "rate interval", mutate: func(config *crossref.Config) { config.RateLimit.Interval = 0 }, want: "rate limit"},
		{name: "max retries", mutate: func(config *crossref.Config) { config.MaxRetries = -1 }, want: "retries"},
		{name: "max wait", mutate: func(config *crossref.Config) { config.MaxWait = 0 }, want: "retry wait"},
		{name: "initial backoff", mutate: func(config *crossref.Config) { config.InitialBackoff = 0 }, want: "initial backoff"},
		{name: "max backoff", mutate: func(config *crossref.Config) { config.MaxBackoff = 0 }, want: "max backoff"},
		{name: "backoff order", mutate: func(config *crossref.Config) { config.MaxBackoff = config.InitialBackoff / 2 }, want: "max backoff"},
		{name: "response bytes", mutate: func(config *crossref.Config) { config.MaxResponseBytes = 0 }, want: "body limit"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			test.mutate(&config)
			client, err := crossref.NewClient(http.DefaultClient, config, httpclient.Dependencies{})
			if err == nil {
				t.Fatalf("NewClient() = %#v, want error", client)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("NewClient() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCrossrefCreatedUpdatedStreamsCreatedFilterAndPagination(t *testing.T) {
	t.Parallel()

	const specialCursor = "next cursor/+?=&%"
	var (
		mu       sync.Mutex
		requests []url.Values
		rawQuery []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/works" {
			t.Errorf("path = %q, want /works", request.URL.Path)
		}
		if request.Header.Get("User-Agent") != "paper-hub-crossref-test/1.0" {
			t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		mu.Lock()
		requests = append(requests, request.URL.Query())
		rawQuery = append(rawQuery, request.URL.RawQuery)
		mu.Unlock()

		switch request.URL.Query().Get("cursor") {
		case "*":
			writeEnvelope(t, writer, cursorValue(specialCursor), item("10.1000/one"), item("10.1000/two"))
		case specialCursor:
			writeEnvelope(t, writer, noCursor(), item("10.1000/three"))
		default:
			t.Errorf("unexpected cursor %q", request.URL.Query().Get("cursor"))
			writeEnvelope(t, writer, noCursor())
		}
	}))
	defer server.Close()

	client := newClient(t, server, func(config *crossref.Config) {
		config.BatchSize = 2
	}, httpclient.Dependencies{})
	records, errs := collect(client.Fetch(context.Background(), crossref.Query{
		Stream: crossref.StreamCreated,
		DOIs:   []string{" DOI:10.1000/TWO ", "https://doi.org/10.1000/one", "10.1000/two"},
		ISSNs:  []string{"2049-3630", "0028-0836", "2049-3630"},
		DateWindow: crossref.DateWindow{
			From: time.Date(2026, time.July, 1, 18, 0, 0, 0, time.FixedZone("west", -7*60*60)),
			To:   time.Date(2026, time.July, 16, 23, 59, 0, 0, time.FixedZone("east", 8*60*60)),
		},
		MaxResults: 4,
	}))
	if len(errs) != 0 {
		t.Fatalf("Fetch() errors = %v", errs)
	}
	if got := recordDOIs(records); !slices.Equal(got, []string{
		"10.1000/one",
		"10.1000/two",
		"10.1000/three",
	}) {
		t.Fatalf("record DOIs = %v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	const wantFilter = "doi:10.1000/one,doi:10.1000/two,issn:0028-0836,issn:2049-3630,from-created-date:2026-07-01,until-created-date:2026-07-16"
	for index, values := range requests {
		if got := values.Get("filter"); got != wantFilter {
			t.Errorf("request %d filter = %q, want %q", index+1, got, wantFilter)
		}
		if got := values.Get("mailto"); got != "research@example.test" {
			t.Errorf("request %d mailto = %q", index+1, got)
		}
	}
	if requests[0].Get("cursor") != "*" || requests[0].Get("rows") != "2" {
		t.Fatalf("first request = %v, want cursor=* rows=2", requests[0])
	}
	if requests[1].Get("cursor") != specialCursor || requests[1].Get("rows") != "2" {
		t.Fatalf("second request = %v, want exact decoded cursor and rows=2", requests[1])
	}
	for _, fragment := range []string{"cursor=next+cursor%2F%2B%3F%3D%26%25", "filter=doi%3A10.1000%2Fone"} {
		if !strings.Contains(rawQuery[1], fragment) {
			t.Errorf("second RawQuery = %q, want encoded fragment %q", rawQuery[1], fragment)
		}
	}
}

func TestCrossrefCreatedUpdatedStreamsUpdatedFilter(t *testing.T) {
	t.Parallel()

	var captured url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = request.URL.Query()
		writeEnvelope(t, writer, noCursor())
	}))
	defer server.Close()

	client := newClient(t, server, nil, httpclient.Dependencies{})
	records, errs := collect(client.Fetch(context.Background(), crossref.Query{
		Stream: crossref.StreamUpdated,
		DateWindow: crossref.DateWindow{
			From: time.Date(2026, time.July, 17, 23, 59, 0, 0, time.FixedZone("west", -7*60*60)),
			To:   time.Date(2026, time.July, 18, 0, 1, 0, 0, time.FixedZone("east", 8*60*60)),
		},
		ISSNs:      []string{"0028-0836"},
		MaxResults: 25,
	}))
	if len(records) != 0 || len(errs) != 0 {
		t.Fatalf("Fetch() = %d records, errors %v", len(records), errs)
	}
	const wantFilter = "issn:0028-0836,from-update-date:2026-07-17,until-update-date:2026-07-18"
	if got := captured.Get("filter"); got != wantFilter {
		t.Fatalf("filter = %q, want explicit updated stream %q", got, wantFilter)
	}
	if got := captured.Get("cursor"); got != "*" {
		t.Fatalf("cursor = %q, want initial cursor", got)
	}
	if got := captured.Get("rows"); got != "25" {
		t.Fatalf("rows = %q, want remaining max results", got)
	}
	if got := captured.Get("mailto"); got != "research@example.test" {
		t.Fatalf("mailto = %q", got)
	}
}

func TestFetchRejectsDOIFilterDelimiterInjectionButAllowsDOIPunctuation(t *testing.T) {
	t.Parallel()

	var (
		requests atomic.Int32
		filter   string
		rawQuery string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		filter = request.URL.Query().Get("filter")
		rawQuery = request.URL.RawQuery
		writeEnvelope(t, writer, noCursor())
	}))
	defer server.Close()

	client := newClient(t, server, nil, httpclient.Dependencies{})
	records, errs := collect(client.Fetch(context.Background(), crossref.Query{
		Stream:     crossref.StreamCreated,
		DOIs:       []string{"10.1000/A:B%2CC/D"},
		MaxResults: 1,
	}))
	if len(records) != 0 || len(errs) != 0 {
		t.Fatalf("valid punctuation Fetch() = %d records, errors %v", len(records), errs)
	}
	if filter != "doi:10.1000/a:b%2cc/d" {
		t.Fatalf("filter = %q, want colon and percent preserved inside one DOI token", filter)
	}
	if strings.Count(filter, ",") != 0 {
		t.Fatalf("filter = %q, percent-encoded comma must not create another token", filter)
	}
	if !strings.Contains(rawQuery, "%252c") {
		t.Fatalf("RawQuery = %q, want literal DOI percent encoded exactly once by url.Values", rawQuery)
	}

	records, errs = collect(client.Fetch(context.Background(), crossref.Query{
		Stream:     crossref.StreamCreated,
		DOIs:       []string{"10.1000/safe,issn:0028-0836"},
		MaxResults: 1,
	}))
	if len(records) != 0 || len(errs) != 1 {
		t.Fatalf("injection Fetch() = %d records, %d errors; want validation error", len(records), len(errs))
	}
	if !strings.Contains(strings.ToLower(errs[0].Error()), "filter") ||
		!strings.Contains(strings.ToLower(errs[0].Error()), "comma") {
		t.Fatalf("Fetch() error = %v, want explicit unsafe filter comma", errs[0])
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, injected DOI must be rejected before network", requests.Load())
	}
}

func TestFetchSnapshotsQueryBeforeReturningLazySequence(t *testing.T) {
	t.Parallel()

	var captured url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = request.URL.Query()
		writeEnvelope(t, writer, noCursor())
	}))
	defer server.Close()

	client := newClient(t, server, nil, httpclient.Dependencies{})
	query := crossref.Query{
		Stream: crossref.StreamCreated,
		DOIs:   []string{"10.1000/original"},
		ISSNs:  []string{"0028-0836"},
		DateWindow: crossref.DateWindow{
			From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC),
		},
		MaxResults: 1,
	}
	sequence := client.Fetch(context.Background(), query)
	query.DOIs[0] = "10.1000/mutated"
	query.ISSNs[0] = "2049-3630"

	records, errs := collect(sequence)
	if len(records) != 0 || len(errs) != 0 {
		t.Fatalf("Fetch() = %d records, errors %v", len(records), errs)
	}
	const want = "doi:10.1000/original,issn:0028-0836,from-created-date:2026-07-01,until-created-date:2026-07-02"
	if got := captured.Get("filter"); got != want {
		t.Fatalf("filter = %q, want immutable Fetch snapshot %q", got, want)
	}
}

func TestFetchSnapshotHasNoRaceWithCallerMutation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeEnvelope(t, writer, noCursor())
	}))
	defer server.Close()

	client := newClient(t, server, nil, httpclient.Dependencies{})
	query := crossref.Query{
		Stream:     crossref.StreamCreated,
		DOIs:       []string{"10.1000/original"},
		ISSNs:      []string{"0028-0836"},
		MaxResults: 1,
	}
	sequence := client.Fetch(context.Background(), query)

	start := make(chan struct{})
	mutated := make(chan struct{})
	go func() {
		defer close(mutated)
		close(start)
		for index := range 10_000 {
			if index%2 == 0 {
				query.DOIs[0] = "10.1000/mutated-a"
				query.ISSNs[0] = "2049-3630"
			} else {
				query.DOIs[0] = "10.1000/mutated-b"
				query.ISSNs[0] = "0028-0836"
			}
		}
	}()
	<-start
	records, errs := collect(sequence)
	<-mutated
	if len(records) != 0 || len(errs) != 0 {
		t.Fatalf("Fetch() = %d records, errors %v", len(records), errs)
	}
}

func TestFetchRejectsUnboundedOrMalformedQueryBeforeNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeEnvelope(t, writer, noCursor())
	}))
	defer server.Close()

	client := newClient(t, server, nil, httpclient.Dependencies{})
	tests := []struct {
		name  string
		query crossref.Query
		want  string
	}{
		{
			name:  "missing stream",
			query: crossref.Query{DOIs: []string{"10.1000/a"}, MaxResults: 1},
			want:  "stream",
		},
		{
			name: "unknown stream",
			query: crossref.Query{
				Stream:     crossref.Stream("indexed"),
				DOIs:       []string{"10.1000/a"},
				MaxResults: 1,
			},
			want: "stream",
		},
		{
			name: "nonpositive max",
			query: crossref.Query{
				Stream: crossref.StreamCreated,
				DOIs:   []string{"10.1000/a"},
			},
			want: "max results",
		},
		{
			name: "unbounded",
			query: crossref.Query{
				Stream:     crossref.StreamCreated,
				MaxResults: 1,
			},
			want: "bounded",
		},
		{name: "partial from window", query: crossref.Query{
			Stream:     crossref.StreamCreated,
			DateWindow: crossref.DateWindow{From: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
			MaxResults: 1,
		}, want: "both"},
		{name: "partial to window", query: crossref.Query{
			Stream:     crossref.StreamUpdated,
			DateWindow: crossref.DateWindow{To: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
			MaxResults: 1,
		}, want: "both"},
		{name: "reversed window", query: crossref.Query{
			Stream: crossref.StreamUpdated,
			DateWindow: crossref.DateWindow{
				From: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
				To:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			},
			MaxResults: 1,
		}, want: "must not follow"},
		{name: "invalid DOI", query: crossref.Query{
			Stream: crossref.StreamCreated, DOIs: []string{"not-a-doi"}, MaxResults: 1,
		}, want: "DOI"},
		{name: "invalid ISSN format", query: crossref.Query{
			Stream: crossref.StreamCreated, ISSNs: []string{"Nature"}, MaxResults: 1,
		}, want: "ISSN"},
		{name: "invalid ISSN checksum", query: crossref.Query{
			Stream: crossref.StreamUpdated, ISSNs: []string{"0028-0837"}, MaxResults: 1,
		}, want: "ISSN"},
		{name: "date year zero", query: crossref.Query{
			Stream: crossref.StreamCreated,
			DateWindow: crossref.DateWindow{
				From: time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC),
				To:   time.Date(1, time.January, 2, 0, 0, 0, 0, time.UTC),
			},
			MaxResults: 1,
		}, want: "year"},
		{name: "date year above four digits", query: crossref.Query{
			Stream: crossref.StreamUpdated,
			DateWindow: crossref.DateWindow{
				From: time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC),
				To:   time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
			},
			MaxResults: 1,
		}, want: "year"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			records, errs := collect(client.Fetch(context.Background(), test.query))
			if len(records) != 0 || len(errs) != 1 {
				t.Fatalf("Fetch() = %d records, %d errors; want one explicit error", len(records), len(errs))
			}
			if !strings.Contains(strings.ToLower(errs[0].Error()), strings.ToLower(test.want)) {
				t.Fatalf("Fetch() error = %v, want containing %q", errs[0], test.want)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want validation before network", requests.Load())
	}
}

func TestFetchAcceptsCreatedAndUpdatedDateYearBoundaries(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		filters []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		filters = append(filters, request.URL.Query().Get("filter"))
		mu.Unlock()
		writeEnvelope(t, writer, noCursor())
	}))
	defer server.Close()

	client := newClient(t, server, nil, httpclient.Dependencies{})
	for _, test := range []struct {
		stream crossref.Stream
		window crossref.DateWindow
	}{
		{
			stream: crossref.StreamCreated,
			window: crossref.DateWindow{
				From: time.Date(1, time.January, 2, 0, 0, 0, 0, time.UTC),
				To:   time.Date(1, time.December, 31, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			stream: crossref.StreamUpdated,
			window: crossref.DateWindow{
				From: time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC),
				To:   time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC),
			},
		},
	} {
		records, errs := collect(client.Fetch(context.Background(), crossref.Query{
			Stream:     test.stream,
			DateWindow: test.window,
			MaxResults: 1,
		}))
		if len(records) != 0 || len(errs) != 0 {
			t.Fatalf("Fetch(%s, %v) = %d records, errors %v", test.stream, test.window, len(records), errs)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"from-created-date:0001-01-02,until-created-date:0001-12-31",
		"from-update-date:9999-01-01,until-update-date:9999-12-31",
	}
	if !slices.Equal(filters, want) {
		t.Fatalf("filters = %v, want exact supported year boundaries %v", filters, want)
	}
}

func TestFetchStopsNormallyOnShortOrEmptyPageWithoutCursor(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		items     []json.RawMessage
		wantCount int
	}{
		{name: "short", items: []json.RawMessage{item("10.1000/one")}, wantCount: 1},
		{name: "empty", items: []json.RawMessage{}, wantCount: 0},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writeEnvelope(t, writer, noCursor(), test.items...)
			}))
			defer server.Close()

			client := newClient(t, server, func(config *crossref.Config) {
				config.BatchSize = 2
			}, httpclient.Dependencies{})
			records, errs := collect(client.Fetch(context.Background(), boundedQuery(10)))
			if len(records) != test.wantCount || len(errs) != 0 {
				t.Fatalf("Fetch() = %d records, %d errors", len(records), len(errs))
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1", requests.Load())
			}
		})
	}
}

func TestFetchFailsAtMaxResultsWhenCursorIsNotExhausted(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeEnvelope(t, writer, cursorValue("page 2"), item("10.1000/one"))
	}))
	defer server.Close()

	client := newClient(t, server, func(config *crossref.Config) {
		config.BatchSize = 1
	}, httpclient.Dependencies{})
	records, errs := collect(client.Fetch(context.Background(), boundedQuery(1)))
	if len(records) != 1 || len(errs) != 1 ||
		!errors.Is(errs[0], crossref.ErrResultLimitReached) {
		t.Fatalf(
			"Fetch() = %d records, errors %v; want explicit incomplete-window error",
			len(records),
			errs,
		)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want exactly 1", requests.Load())
	}
}

func TestFetchWithPageReceiptsPreservesNextCursorBeforeResultLimitFailure(
	t *testing.T,
) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writeEnvelope(
			t,
			writer,
			cursorValue("opaque next cursor \t"),
			item("10.1000/one"),
		)
	}))
	defer server.Close()

	client := newClient(t, server, func(config *crossref.Config) {
		config.BatchSize = 1
	}, httpclient.Dependencies{})
	var receipts []crossref.PageReceipt
	records, errs := collect(client.FetchWithPageReceipts(
		context.Background(),
		boundedQuery(1),
		func(_ context.Context, receipt crossref.PageReceipt) error {
			receipts = append(receipts, receipt)
			return nil
		},
	))
	if len(records) != 1 || len(errs) != 1 ||
		!errors.Is(errs[0], crossref.ErrResultLimitReached) {
		t.Fatalf(
			"FetchWithPageReceipts() = %d records, errors %v",
			len(records),
			errs,
		)
	}
	if len(receipts) != 1 ||
		receipts[0].CursorOut != "opaque next cursor \t" {
		t.Fatalf("receipts = %#v, want exact opaque continuation cursor", receipts)
	}
}

func TestFetchRejectsPageThatExceedsRequestedRowsBeforeYieldingIt(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeEnvelope(
			t,
			writer,
			cursorValue("next"),
			item("10.1000/one"),
			item("10.1000/two"),
		)
	}))
	defer server.Close()

	client := newClient(t, server, func(config *crossref.Config) {
		config.BatchSize = 1
	}, httpclient.Dependencies{})
	records, errs := collect(client.Fetch(context.Background(), boundedQuery(3)))
	if len(records) != 0 || len(errs) != 1 {
		t.Fatalf("Fetch() = %d records, %d errors; want protocol error before page yield", len(records), len(errs))
	}
	message := strings.ToLower(errs[0].Error())
	if !strings.Contains(message, "rows") || !strings.Contains(message, "2") {
		t.Fatalf("Fetch() error = %v, want requested rows and returned count", errs[0])
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want one request", requests.Load())
	}
}

func TestFetchRequiresAdvancingCursorOnlyWhenFullPageNeedsContinuation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
		records int
	}{
		{
			name: "missing",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writeEnvelope(t, writer, noCursor(), item("10.1000/one"))
			},
			want:    "missing",
			records: 1,
		},
		{
			name: "blank",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writeEnvelope(t, writer, cursorValue(" \t "), item("10.1000/one"))
			},
			want:    "blank",
			records: 1,
		},
		{
			name: "duplicate",
			handler: func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Query().Get("cursor") == "*" {
					writeEnvelope(t, writer, cursorValue("repeat"), item("10.1000/one"))
					return
				}
				writeEnvelope(t, writer, cursorValue("repeat"), item("10.1000/two"))
			},
			want:    "duplicate",
			records: 2,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(test.handler)
			defer server.Close()
			client := newClient(t, server, func(config *crossref.Config) {
				config.BatchSize = 1
			}, httpclient.Dependencies{})

			records, errs := collect(client.Fetch(context.Background(), boundedQuery(3)))
			if len(records) != test.records || len(errs) != 1 {
				t.Fatalf("Fetch() = %d records, %d errors", len(records), len(errs))
			}
			message := strings.ToLower(errs[0].Error())
			if !strings.Contains(message, "cursor") || !strings.Contains(message, test.want) {
				t.Fatalf("Fetch() error = %v, want cursor and %q", errs[0], test.want)
			}
		})
	}
}

func TestFetchRejectsStrictEnvelopeViolations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "not object", payload: `[]`, want: "object"},
		{name: "multiple JSON values", payload: `{}` + "\n" + `{}`, want: "exactly one"},
		{name: "missing status", payload: `{"message-type":"work-list","message":{"items":[]}}`, want: "status"},
		{name: "bad status", payload: `{"status":"error","message-type":"work-list","message":{"items":[]}}`, want: "status"},
		{name: "missing message type", payload: `{"status":"ok","message":{"items":[]}}`, want: "message-type"},
		{name: "bad message type", payload: `{"status":"ok","message-type":"work","message":{"items":[]}}`, want: "message-type"},
		{name: "missing message", payload: `{"status":"ok","message-type":"work-list"}`, want: "message"},
		{name: "null message", payload: `{"status":"ok","message-type":"work-list","message":null}`, want: "message"},
		{name: "missing items", payload: `{"status":"ok","message-type":"work-list","message":{}}`, want: "items"},
		{name: "null items", payload: `{"status":"ok","message-type":"work-list","message":{"items":null}}`, want: "items"},
		{name: "nonarray items", payload: `{"status":"ok","message-type":"work-list","message":{"items":{}}}`, want: "items"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.payload))
			}))
			defer server.Close()
			client := newClient(t, server, nil, httpclient.Dependencies{})

			records, errs := collect(client.Fetch(context.Background(), boundedQuery(2)))
			if len(records) != 0 || len(errs) != 1 {
				t.Fatalf("Fetch() = %d records, %d errors", len(records), len(errs))
			}
			if !strings.Contains(strings.ToLower(errs[0].Error()), strings.ToLower(test.want)) {
				t.Fatalf("Fetch() error = %v, want containing %q", errs[0], test.want)
			}
		})
	}
}

func TestFetchRejectsDuplicateJSONKeysInEnvelopeAndItemsBeforeYield(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		key     string
	}{
		{
			name: "top-level status",
			payload: `{
				"status":"ok",
				"status":"ok",
				"message-type":"work-list",
				"message":{"items":[]}
			}`,
			key: "status",
		},
		{
			name: "item DOI",
			payload: `{
				"status":"ok",
				"message-type":"work-list",
				"message":{"items":[{
					"DOI":"10.1000/first",
					"DOI":"10.1000/second"
				}]}
			}`,
			key: "DOI",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.payload))
			}))
			defer server.Close()

			client := newClient(t, server, nil, httpclient.Dependencies{})
			records, errs := collect(client.Fetch(context.Background(), boundedQuery(1)))
			if len(records) != 0 || len(errs) != 1 {
				t.Fatalf("Fetch() = %d records, %d errors; want duplicate-key error before yield", len(records), len(errs))
			}
			message := errs[0].Error()
			if !strings.Contains(strings.ToLower(message), "duplicate") ||
				!strings.Contains(message, test.key) {
				t.Fatalf("Fetch() error = %v, want duplicate key %q", errs[0], test.key)
			}
		})
	}
}

func TestFetchYieldsPriorRecordsThenStopsAtGlobalMalformedItemOrdinal(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Query().Get("cursor") {
		case "*":
			writeEnvelope(
				t,
				writer,
				cursorValue("page 2"),
				item("10.1000/one"),
				item("10.1000/two"),
			)
		case "page 2":
			writeEnvelope(
				t,
				writer,
				noCursor(),
				item("10.1000/three"),
				json.RawMessage(`{"title":["missing DOI"]}`),
			)
		default:
			t.Errorf("unexpected cursor %q", request.URL.Query().Get("cursor"))
		}
	}))
	defer server.Close()

	client := newClient(t, server, func(config *crossref.Config) {
		config.BatchSize = 2
	}, httpclient.Dependencies{})
	records, errs := collect(client.Fetch(context.Background(), boundedQuery(10)))
	if len(records) != 3 || len(errs) != 1 {
		t.Fatalf("Fetch() = %d records, %d errors; want three records then one error", len(records), len(errs))
	}
	if !strings.Contains(strings.ToLower(errs[0].Error()), "item 4") {
		t.Fatalf("Fetch() error = %v, want global item ordinal 4", errs[0])
	}
}

func TestFetchInheritsRetryCancellationAndBodyLimit(t *testing.T) {
	t.Parallel()

	t.Run("Retry-After", func(t *testing.T) {
		var attempts atomic.Int32
		var (
			mu     sync.Mutex
			now    = time.Unix(0, 0)
			sleeps []time.Duration
		)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			if attempts.Add(1) == 1 {
				writer.Header().Set("Retry-After", "2")
				http.Error(writer, "retry", http.StatusServiceUnavailable)
				return
			}
			writeEnvelope(t, writer, noCursor())
		}))
		defer server.Close()

		client := newClient(t, server, func(config *crossref.Config) {
			config.MaxRetries = 1
		}, httpclient.Dependencies{
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
		})
		records, errs := collect(client.Fetch(context.Background(), boundedQuery(1)))
		if len(records) != 0 || len(errs) != 0 {
			t.Fatalf("Fetch() = %d records, %d errors", len(records), len(errs))
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts = %d, want 2", attempts.Load())
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(sleeps, []time.Duration{2 * time.Second}) {
			t.Fatalf("sleeps = %v, want Retry-After", sleeps)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "retry", http.StatusServiceUnavailable)
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		client := newClient(t, server, func(config *crossref.Config) {
			config.MaxRetries = 1
		}, httpclient.Dependencies{
			Now: func() time.Time { return time.Unix(0, 0) },
			Sleep: func(context.Context, time.Duration) error {
				cancel()
				return context.Canceled
			},
		})
		records, errs := collect(client.Fetch(ctx, boundedQuery(1)))
		if len(records) != 0 || len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
			t.Fatalf("Fetch() = %d records, errors %v; want context cancellation", len(records), errs)
		}
	})

	t.Run("body limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(strings.Repeat("x", 1024)))
		}))
		defer server.Close()

		client := newClient(t, server, func(config *crossref.Config) {
			config.MaxResponseBytes = 32
		}, httpclient.Dependencies{})
		records, errs := collect(client.Fetch(context.Background(), boundedQuery(1)))
		if len(records) != 0 || len(errs) != 1 || !errors.Is(errs[0], httpclient.ErrResponseTooLarge) {
			t.Fatalf("Fetch() = %d records, errors %v; want ErrResponseTooLarge", len(records), errs)
		}
	})
}

func TestFetchRedactsMailtoFromStatusBodyAndTransportErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport roundTripperFunc
		want      string
	}{
		{
			name: "HTTP status URL",
			transport: func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("bad request")),
				}, nil
			},
			want: "400",
		},
		{
			name: "response body echo",
			transport: func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(
						"contact research@example.test was rejected",
					)),
				}, nil
			},
			want: "[REDACTED]",
		},
		{
			name: "transport echo",
			transport: func(request *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf(
					"dial failed for %s contact=%s",
					request.URL.String(),
					request.URL.Query().Get("mailto"),
				)
			},
			want: "[REDACTED]",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := validConfig("https://api.crossref.test")
			config.MaxRetries = 0
			client, err := crossref.NewClient(
				&http.Client{Transport: test.transport},
				config,
				httpclient.Dependencies{},
			)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			records, errs := collect(client.Fetch(context.Background(), boundedQuery(1)))
			if len(records) != 0 || len(errs) != 1 {
				t.Fatalf("Fetch() = %d records, %d errors", len(records), len(errs))
			}
			message := errs[0].Error()
			if strings.Contains(message, "research@example.test") {
				t.Fatalf("Fetch() error leaked mailto: %v", errs[0])
			}
			if !strings.Contains(message, test.want) {
				t.Fatalf("Fetch() error = %v, want containing %q", errs[0], test.want)
			}
		})
	}
}

func TestFetchClosesResponseBodyOnEveryExitPath(t *testing.T) {
	t.Parallel()

	successPayload := envelopePayload(
		t,
		noCursor(),
		item("10.1000/one"),
	)
	parseFailurePayload := envelopePayload(
		t,
		noCursor(),
		json.RawMessage(`{"title":["missing DOI"]}`),
	)
	earlyStopPayload := envelopePayload(
		t,
		noCursor(),
		item("10.1000/one"),
		item("10.1000/two"),
	)

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		client, closes := newTrackingBodyClient(t, successPayload, nil)
		records, errs := collect(client.Fetch(context.Background(), boundedQuery(2)))
		if len(records) != 1 || len(errs) != 0 {
			t.Fatalf("Fetch() = %d records, errors %v", len(records), errs)
		}
		if got := closes.Load(); got != 1 {
			t.Fatalf("response body Close() calls = %d, want 1", got)
		}
	})

	t.Run("parse failure", func(t *testing.T) {
		t.Parallel()

		client, closes := newTrackingBodyClient(t, parseFailurePayload, nil)
		records, errs := collect(client.Fetch(context.Background(), boundedQuery(1)))
		if len(records) != 0 || len(errs) != 1 {
			t.Fatalf("Fetch() = %d records, errors %v; want parse error", len(records), errs)
		}
		if got := closes.Load(); got != 1 {
			t.Fatalf("response body Close() calls = %d, want 1", got)
		}
	})

	t.Run("body limit", func(t *testing.T) {
		t.Parallel()

		client, closes := newTrackingBodyClient(
			t,
			[]byte(strings.Repeat("x", 128)),
			func(config *crossref.Config) {
				config.MaxResponseBytes = 32
			},
		)
		records, errs := collect(client.Fetch(context.Background(), boundedQuery(1)))
		if len(records) != 0 || len(errs) != 1 ||
			!errors.Is(errs[0], httpclient.ErrResponseTooLarge) {
			t.Fatalf("Fetch() = %d records, errors %v; want body limit error", len(records), errs)
		}
		if got := closes.Load(); got != 1 {
			t.Fatalf("response body Close() calls = %d, want 1", got)
		}
	})

	t.Run("consumer stops after first record", func(t *testing.T) {
		t.Parallel()

		client, closes := newTrackingBodyClient(t, earlyStopPayload, func(config *crossref.Config) {
			config.BatchSize = 2
		})
		yields := 0
		client.Fetch(context.Background(), boundedQuery(2))(func(record source.Record, err error) bool {
			yields++
			if err != nil {
				t.Fatalf("Fetch() yielded unexpected error: %v", err)
			}
			if record.SourceRecordID != "10.1000/one" {
				t.Fatalf("first record DOI = %q, want 10.1000/one", record.SourceRecordID)
			}
			return false
		})
		if yields != 1 {
			t.Fatalf("Fetch() yields = %d, want consumer stop after 1", yields)
		}
		if got := closes.Load(); got != 1 {
			t.Fatalf("response body Close() calls = %d, want 1", got)
		}
	})
}

func TestSingleFetchRequestsCursorPagesSerially(t *testing.T) {
	t.Parallel()

	var (
		active    atomic.Int32
		maxActive atomic.Int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			maximum := maxActive.Load()
			if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		switch request.URL.Query().Get("cursor") {
		case "*":
			writeEnvelope(t, writer, cursorValue("second"), item("10.1000/one"))
		case "second":
			writeEnvelope(t, writer, noCursor())
		default:
			t.Errorf("unexpected cursor")
		}
	}))
	defer server.Close()

	client := newClient(t, server, func(config *crossref.Config) {
		config.BatchSize = 1
	}, httpclient.Dependencies{})
	_, errs := collect(client.Fetch(context.Background(), boundedQuery(2)))
	if len(errs) != 0 {
		t.Fatalf("Fetch() errors = %v", errs)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("max concurrent requests = %d, want serial cursor requests", maxActive.Load())
	}
}

func newClient(
	t *testing.T,
	server *httptest.Server,
	mutate func(*crossref.Config),
	dependencies httpclient.Dependencies,
) *crossref.Client {
	t.Helper()

	config := validConfig(server.URL)
	if mutate != nil {
		mutate(&config)
	}
	client, err := crossref.NewClient(server.Client(), config, dependencies)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func validConfig(baseURL string) crossref.Config {
	return crossref.Config{
		BaseURL:      baseURL,
		ContactEmail: "research@example.test",
		UserAgent:    "paper-hub-crossref-test/1.0",
		BatchSize:    100,
		Timeout:      time.Second,
		RateLimit: httpclient.RateLimit{
			Requests: 1000,
			Interval: time.Second,
		},
		MaxRetries:       2,
		MaxWait:          5 * time.Second,
		InitialBackoff:   time.Millisecond,
		MaxBackoff:       10 * time.Millisecond,
		MaxResponseBytes: 1 << 20,
	}
}

func boundedQuery(maxResults int) crossref.Query {
	return crossref.Query{
		Stream:     crossref.StreamCreated,
		DOIs:       []string{"10.1000/bound"},
		MaxResults: maxResults,
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

func recordDOIs(records []source.Record) []string {
	result := make([]string, 0, len(records))
	for _, record := range records {
		result = append(result, record.SourceRecordID)
	}
	return result
}

func newTrackingBodyClient(
	t *testing.T,
	payload []byte,
	mutate func(*crossref.Config),
) (*crossref.Client, *atomic.Int32) {
	t.Helper()

	var closes atomic.Int32
	transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: &trackingBody{
				Reader: strings.NewReader(string(payload)),
				closes: &closes,
			},
		}, nil
	})
	config := validConfig("https://api.crossref.test")
	config.MaxRetries = 0
	if mutate != nil {
		mutate(&config)
	}
	client, err := crossref.NewClient(
		&http.Client{Transport: transport},
		config,
		httpclient.Dependencies{},
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client, &closes
}

type trackingBody struct {
	io.Reader
	closes *atomic.Int32
}

func (body *trackingBody) Close() error {
	body.closes.Add(1)
	return nil
}

func item(doi string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"DOI":%q}`, doi))
}

type cursorField struct {
	present bool
	value   string
}

func noCursor() cursorField {
	return cursorField{}
}

func cursorValue(value string) cursorField {
	return cursorField{present: true, value: value}
}

func writeEnvelope(
	t *testing.T,
	writer http.ResponseWriter,
	cursor cursorField,
	items ...json.RawMessage,
) {
	t.Helper()

	if items == nil {
		items = make([]json.RawMessage, 0)
	}
	message := map[string]any{
		"items":         items,
		"total-results": 1_000_000,
	}
	if cursor.present {
		message["next-cursor"] = cursor.value
	}
	payload := map[string]any{
		"status":       "ok",
		"message-type": "work-list",
		"message":      message,
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		t.Errorf("Encode() error = %v", err)
	}
}

func envelopePayload(
	t *testing.T,
	cursor cursorField,
	items ...json.RawMessage,
) []byte {
	t.Helper()

	recorder := httptest.NewRecorder()
	writeEnvelope(t, recorder, cursor, items...)
	return append([]byte(nil), recorder.Body.Bytes()...)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
