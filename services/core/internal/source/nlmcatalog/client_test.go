package nlmcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
)

const (
	nlmTestSearchUID = "7502536"
	nlmTestHashOne   = "1111111111111111111111111111111111111111111111111111111111111111"
)

func TestValidateBaseURLRejectsUserinfo(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"https://user:password@example.test",
		"https://user@example.test",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			parsed, err := validateBaseURL(raw)
			if err == nil {
				t.Fatalf(
					"validateBaseURL(%q) = %#v, nil; want userinfo rejection",
					raw,
					parsed,
				)
			}
			if parsed != nil {
				t.Fatalf(
					"validateBaseURL(%q) returned URL after rejection: %#v",
					raw,
					parsed,
				)
			}
		})
	}
}

func TestClientResolveUsesTitleAndStableISSNIntersectionQuery(t *testing.T) {
	t.Parallel()

	searchPayload := []byte(
		`{"esearchresult":{"count":"1","retmax":"1","retstart":"0","idlist":["7502536"]}}`,
	)
	summaryPayload := nlmSummaryPayload(
		nlmTestSearchUID,
		"british journal of pharmacology",
		"British journal of pharmacology.",
		[]string{
			`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
			`{"issn":"1476-5381","issntype":"Electronic","validyn":"Y"}`,
		},
	)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		call := calls.Add(1)
		if request.Method != http.MethodGet {
			t.Errorf("request %d method = %s, want GET", call, request.Method)
		}
		query := request.URL.Query()
		for key, want := range map[string]string{
			"db":      "nlmcatalog",
			"retmode": "json",
			"tool":    "paper-hub",
			"email":   "research@example.test",
			"api_key": "nlm-secret",
		} {
			if got := query.Get(key); got != want {
				t.Errorf("request %d %s = %q, want %q", call, key, got, want)
			}
		}
		switch request.URL.Path {
		case "/entrez/eutils/esearch.fcgi":
			wantTerm := `"British Journal of Pharmacology"[Title] AND (` +
				`"0007-1188"[ISSN] OR "1476-5381"[ISSN])`
			if got := query.Get("term"); got != wantTerm {
				t.Errorf("ESearch term = %q, want %q", got, wantTerm)
			}
			if got := query.Get("retmax"); got != "1" {
				t.Errorf("ESearch retmax = %q, want 1", got)
			}
			if strings.Contains(query.Get("term"), `[Title]`) &&
				!strings.Contains(query.Get("term"), `[ISSN]`) {
				t.Errorf("client issued forbidden pure-title query %q", query.Get("term"))
			}
			_, _ = writer.Write(searchPayload)
		case "/entrez/eutils/esummary.fcgi":
			if got := query.Get("id"); got != nlmTestSearchUID {
				t.Errorf("ESummary id = %q, want %q", got, nlmTestSearchUID)
			}
			if got := query.Get("version"); got != "2.0" {
				t.Errorf("ESummary version = %q, want 2.0", got)
			}
			_, _ = writer.Write(summaryPayload)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := newNLMTestClient(t, server.URL, "nlm-secret", httpclient.Dependencies{})
	result, err := client.Resolve(context.Background(), Query{
		Title:          "British Journal of Pharmacology",
		CandidateISSNs: []string{"1476-5381", "0007-1188"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP calls = %d, want ESearch + ESummary", calls.Load())
	}
	if result.UID != nlmTestSearchUID ||
		result.NLMUniqueID != nlmTestSearchUID ||
		result.TitleMainSort != "british journal of pharmacology" ||
		result.DateRevised != "2026-05-08" ||
		result.EndYear != "9999" ||
		result.CurrentIndexingStatus != "Y" {
		t.Fatalf("Resolve() result identity = %#v", result)
	}
	if result.PrintISSN != "0007-1188" ||
		result.ElectronicISSN != "1476-5381" ||
		!slices.Equal(result.AllISSNs, []string{"0007-1188", "1476-5381"}) {
		t.Fatalf("Resolve() ISSNs = %#v", result)
	}
	if len(result.TitleMainList) != 1 ||
		result.TitleMainList[0].Title != "British journal of pharmacology." ||
		result.TitleMainList[0].SortTitle != "british journal of pharmacology" {
		t.Fatalf("Resolve() titlemainlist = %#v", result.TitleMainList)
	}
	if result.ESearchSHA256 != sha256String(searchPayload) ||
		result.ESummarySHA256 != sha256String(summaryPayload) {
		t.Fatalf(
			"Resolve() hashes = %q/%q",
			result.ESearchSHA256,
			result.ESummarySHA256,
		)
	}
}

func TestClientResolveRequiresExactlyOneESearchUID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "zero hits",
			payload: `{"esearchresult":{"count":"0","idlist":[]}}`,
		},
		{
			name: "multiple hits",
			payload: `{"esearchresult":{"count":"2",` +
				`"idlist":["7502536","101668984"]}}`,
		},
		{
			name:    "one count without UID",
			payload: `{"esearchresult":{"count":"1","idlist":[]}}`,
		},
		{
			name:    "one count with duplicate UID",
			payload: `{"esearchresult":{"count":"1","idlist":["7502536","7502536"]}}`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				calls.Add(1)
				_, _ = writer.Write([]byte(test.payload))
			}))
			defer server.Close()

			client := newNLMTestClient(t, server.URL, "", httpclient.Dependencies{})
			_, err := client.Resolve(context.Background(), Query{
				Title:          "British Journal of Pharmacology",
				CandidateISSNs: []string{"0007-1188", "1476-5381"},
			})
			if err == nil || !strings.Contains(err.Error(), "exactly one") {
				t.Fatalf("Resolve() error = %v, want exactly-one failure", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("HTTP calls = %d, want ESearch only", calls.Load())
			}
		})
	}
}

func TestClientResolveStrictlyRejectsMalformedESearchJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "duplicate key",
			payload: `{"esearchresult":{"count":"1","count":"1","idlist":["7502536"]}}`,
		},
		{
			name:    "trailing value",
			payload: `{"esearchresult":{"count":"1","idlist":["7502536"]}} {}`,
		},
		{
			name:    "numeric count",
			payload: `{"esearchresult":{"count":1,"idlist":["7502536"]}}`,
		},
		{
			name:    "numeric UID",
			payload: `{"esearchresult":{"count":"1","idlist":[7502536]}}`,
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
				_, _ = writer.Write([]byte(test.payload))
			}))
			defer server.Close()

			client := newNLMTestClient(t, server.URL, "", httpclient.Dependencies{})
			if _, err := client.Resolve(context.Background(), Query{
				Title:          "British Journal of Pharmacology",
				CandidateISSNs: []string{"0007-1188", "1476-5381"},
			}); err == nil {
				t.Fatal("Resolve() error = nil, want strict ESearch JSON failure")
			}
		})
	}
}

func TestClientResolveRejectsInvalidSummaryIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary string
		want    string
	}{
		{
			name: "inconsistent UID list",
			summary: strings.Replace(
				string(nlmSummaryPayload(
					nlmTestSearchUID,
					"british journal of pharmacology",
					"British journal of pharmacology.",
					[]string{
						`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
					},
				)),
				`"uids":["7502536"]`,
				`"uids":["101668984"]`,
				1,
			),
			want: "UID",
		},
		{
			name: "inconsistent NLM unique ID",
			summary: strings.Replace(
				string(nlmSummaryPayload(
					nlmTestSearchUID,
					"british journal of pharmacology",
					"British journal of pharmacology.",
					[]string{
						`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
					},
				)),
				`"nlmuniqueid":"7502536"`,
				`"nlmuniqueid":"101668984"`,
				1,
			),
			want: "nlmuniqueid",
		},
		{
			name: "invalid ISSN",
			summary: string(nlmSummaryPayload(
				nlmTestSearchUID,
				"british journal of pharmacology",
				"British journal of pharmacology.",
				[]string{
					`{"issn":"0007-1189","issntype":"Print","validyn":"Y"}`,
				},
			)),
			want: "ISSN",
		},
		{
			name: "non canonical ISSN",
			summary: string(nlmSummaryPayload(
				nlmTestSearchUID,
				"british journal of pharmacology",
				"British journal of pharmacology.",
				[]string{
					`{"issn":"3141-592x","issntype":"Electronic","validyn":"Y"}`,
				},
			)),
			want: "canonical",
		},
		{
			name: "conflicting print role",
			summary: string(nlmSummaryPayload(
				nlmTestSearchUID,
				"british journal of pharmacology",
				"British journal of pharmacology.",
				[]string{
					`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
					`{"issn":"1476-5381","issntype":"Print","validyn":"Y"}`,
				},
			)),
			want: "role",
		},
		{
			name: "same ISSN in two roles",
			summary: string(nlmSummaryPayload(
				nlmTestSearchUID,
				"british journal of pharmacology",
				"British journal of pharmacology.",
				[]string{
					`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
					`{"issn":"0007-1188","issntype":"Electronic","validyn":"Y"}`,
				},
			)),
			want: "role",
		},
		{
			name: "unsupported valid role",
			summary: string(nlmSummaryPayload(
				nlmTestSearchUID,
				"british journal of pharmacology",
				"British journal of pharmacology.",
				[]string{
					`{"issn":"0007-1188","issntype":"Linking","validyn":"Y"}`,
				},
			)),
			want: "type",
		},
		{
			name: "empty valid identity",
			summary: string(nlmSummaryPayload(
				nlmTestSearchUID,
				"british journal of pharmacology",
				"British journal of pharmacology.",
				[]string{
					`{"issn":"0007-1188","issntype":"Print","validyn":"N"}`,
				},
			)),
			want: "non-empty",
		},
		{
			name: "invalid date revised",
			summary: strings.Replace(
				string(nlmSummaryPayload(
					nlmTestSearchUID,
					"british journal of pharmacology",
					"British journal of pharmacology.",
					[]string{
						`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
					},
				)),
				`"daterevised":"2026-05-08"`,
				`"daterevised":"2026-02-30"`,
				1,
			),
			want: "daterevised",
		},
		{
			name: "titlemainlist does not identify titlemainsort",
			summary: strings.Replace(
				string(nlmSummaryPayload(
					nlmTestSearchUID,
					"british journal of pharmacology",
					"British journal of pharmacology.",
					[]string{
						`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
					},
				)),
				`"sorttitle":"british journal of pharmacology"`,
				`"sorttitle":"different title"`,
				1,
			),
			want: "titlemainlist",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				switch calls.Add(1) {
				case 1:
					_, _ = writer.Write([]byte(
						`{"esearchresult":{"count":"1","idlist":["7502536"]}}`,
					))
				case 2:
					_, _ = writer.Write([]byte(test.summary))
				default:
					t.Errorf("unexpected request %s", request.URL)
				}
			}))
			defer server.Close()

			client := newNLMTestClient(t, server.URL, "", httpclient.Dependencies{})
			_, err := client.Resolve(context.Background(), Query{
				Title:          "British Journal of Pharmacology",
				CandidateISSNs: []string{"0007-1188", "1476-5381"},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestClientResolveStrictlyRejectsMalformedESummaryJSON(t *testing.T) {
	t.Parallel()

	valid := string(nlmSummaryPayload(
		nlmTestSearchUID,
		"british journal of pharmacology",
		"British journal of pharmacology.",
		[]string{
			`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
		},
	))
	tests := []struct {
		name    string
		payload string
	}{
		{
			name: "duplicate key inside dynamic UID object",
			payload: strings.Replace(
				valid,
				`"uid":"7502536",`,
				`"uid":"7502536","uid":"7502536",`,
				1,
			),
		},
		{
			name:    "trailing second JSON value",
			payload: valid + ` {}`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				switch calls.Add(1) {
				case 1:
					_, _ = writer.Write([]byte(
						`{"esearchresult":{"count":"1","idlist":["7502536"]}}`,
					))
				case 2:
					_, _ = writer.Write([]byte(test.payload))
				}
			}))
			defer server.Close()

			client := newNLMTestClient(t, server.URL, "", httpclient.Dependencies{})
			_, err := client.Resolve(context.Background(), Query{
				Title:          "British Journal of Pharmacology",
				CandidateISSNs: []string{"0007-1188", "1476-5381"},
			})
			if err == nil || !strings.Contains(err.Error(), "ESummary") {
				t.Fatalf("Resolve() error = %v, want strict ESummary JSON failure", err)
			}
		})
	}
}

func TestClientResolveFailsClosedOnNetworkError(t *testing.T) {
	t.Parallel()

	client := newNLMTestClient(
		t,
		"http://127.0.0.1:1",
		"",
		httpclient.Dependencies{},
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := client.Resolve(ctx, Query{
		Title:          "British Journal of Pharmacology",
		CandidateISSNs: []string{"0007-1188", "1476-5381"},
	}); err == nil {
		t.Fatal("Resolve() error = nil, want network failure")
	}
}

func TestClientResolveUsesHTTPPolicyRetriesAndRedactsAPIKey(t *testing.T) {
	t.Parallel()

	apiKey := "nlm+/secret"
	email := "research@example.test"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		calls.Add(1)
		http.Error(
			writer,
			"upstream echoed raw email "+email+
				", escaped email "+url.QueryEscape(email)+
				", raw API key "+apiKey+
				", and escaped API key "+url.QueryEscape(apiKey),
			http.StatusServiceUnavailable,
		)
	}))
	defer server.Close()

	client := newNLMTestClientWithConfig(
		t,
		server.URL,
		apiKey,
		2,
		httpclient.Dependencies{
			Now: func() time.Time { return time.Unix(0, 0) },
			Sleep: func(context.Context, time.Duration) error {
				return nil
			},
		},
	)
	_, err := client.Resolve(context.Background(), Query{
		Title:          "British Journal of Pharmacology",
		CandidateISSNs: []string{"0007-1188", "1476-5381"},
	})
	if err == nil {
		t.Fatal("Resolve() error = nil, want bounded HTTP failure")
	}
	if calls.Load() != 3 {
		t.Fatalf("HTTP calls = %d, want initial + 2 retries", calls.Load())
	}
	message := err.Error()
	for _, secret := range []string{
		email,
		url.QueryEscape(email),
		apiKey,
		url.QueryEscape(apiKey),
	} {
		if strings.Contains(message, secret) {
			t.Fatalf("Resolve() error leaked NCBI identity %q: %s", secret, message)
		}
	}
	if !strings.Contains(message, "[REDACTED]") {
		t.Fatalf("Resolve() error = %q, want redaction marker", message)
	}
}

func TestClientResolveClosesUpstreamResponseBodies(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		searchBody := &recordingBody{
			reader: strings.NewReader(
				`{"esearchresult":{"count":"1","idlist":["7502536"]}}`,
			),
		}
		summaryBody := &recordingBody{
			reader: strings.NewReader(string(nlmSummaryPayload(
				nlmTestSearchUID,
				"british journal of pharmacology",
				"British journal of pharmacology.",
				[]string{
					`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
				},
			))),
		}
		bodies := []*recordingBody{searchBody, summaryBody}
		var calls atomic.Int32
		client := newNLMTestClientWithHTTPClient(
			t,
			&http.Client{Transport: roundTripFunc(func(
				request *http.Request,
			) (*http.Response, error) {
				index := int(calls.Add(1)) - 1
				if index >= len(bodies) {
					return nil, errors.New("unexpected NLM request")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       bodies[index],
					Request:    request,
				}, nil
			})},
			"https://eutils.example.test",
			"",
			0,
			noSleepNLMDependencies(),
		)
		if _, err := client.Resolve(context.Background(), Query{
			Title:          "British Journal of Pharmacology",
			CandidateISSNs: []string{"0007-1188"},
		}); err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		for index, body := range bodies {
			if !body.closed.Load() {
				t.Fatalf("upstream response body %d was not closed", index+1)
			}
		}
	})

	t.Run("JSON parse failure", func(t *testing.T) {
		body := &recordingBody{reader: strings.NewReader(`not JSON`)}
		client := newNLMTestClientWithHTTPClient(
			t,
			&http.Client{Transport: roundTripFunc(func(
				request *http.Request,
			) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       body,
					Request:    request,
				}, nil
			})},
			"https://eutils.example.test",
			"",
			0,
			noSleepNLMDependencies(),
		)
		if _, err := client.Resolve(context.Background(), Query{
			Title:          "British Journal of Pharmacology",
			CandidateISSNs: []string{"0007-1188"},
		}); err == nil {
			t.Fatal("Resolve() error = nil, want JSON parse failure")
		}
		if !body.closed.Load() {
			t.Fatal("upstream response body was not closed after JSON parse failure")
		}
	})

	t.Run("read failure", func(t *testing.T) {
		body := &recordingBody{
			reader: readerFunc(func([]byte) (int, error) {
				return 0, errors.New("injected response read failure")
			}),
		}
		client := newNLMTestClientWithHTTPClient(
			t,
			&http.Client{Transport: roundTripFunc(func(
				request *http.Request,
			) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       body,
					Request:    request,
				}, nil
			})},
			"https://eutils.example.test",
			"",
			0,
			noSleepNLMDependencies(),
		)
		if _, err := client.Resolve(context.Background(), Query{
			Title:          "British Journal of Pharmacology",
			CandidateISSNs: []string{"0007-1188"},
		}); err == nil || !strings.Contains(err.Error(), "read failure") {
			t.Fatalf("Resolve() error = %v, want response read failure", err)
		}
		if !body.closed.Load() {
			t.Fatal("upstream response body was not closed after read failure")
		}
	})
}

func TestClientResolveUsesNCBIRateLimitByAPIKeyPresence(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		delay time.Duration
	}{
		{name: "without key", delay: time.Second / 3},
		{name: "with key", key: "nlm-key", delay: time.Second / 10},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var sleeps []time.Duration
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				switch calls.Add(1) {
				case 1:
					_, _ = writer.Write([]byte(
						`{"esearchresult":{"count":"1","idlist":["7502536"]}}`,
					))
				case 2:
					_, _ = writer.Write(nlmSummaryPayload(
						nlmTestSearchUID,
						"british journal of pharmacology",
						"British journal of pharmacology.",
						[]string{
							`{"issn":"0007-1188","issntype":"Print","validyn":"Y"}`,
						},
					))
				}
			}))
			defer server.Close()

			client := newNLMTestClient(
				t,
				server.URL,
				test.key,
				httpclient.Dependencies{
					Now: func() time.Time { return time.Unix(0, 0) },
					Sleep: func(_ context.Context, delay time.Duration) error {
						sleeps = append(sleeps, delay)
						return nil
					},
				},
			)
			if _, err := client.Resolve(context.Background(), Query{
				Title:          "British Journal of Pharmacology",
				CandidateISSNs: []string{"0007-1188"},
			}); err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if !slices.Equal(sleeps, []time.Duration{test.delay}) {
				t.Fatalf("rate-limit sleeps = %v, want [%s]", sleeps, test.delay)
			}
		})
	}
}

func newNLMTestClient(
	t *testing.T,
	baseURL string,
	apiKey string,
	dependencies httpclient.Dependencies,
) *Client {
	t.Helper()
	return newNLMTestClientWithConfig(t, baseURL, apiKey, 0, dependencies)
}

func newNLMTestClientWithConfig(
	t *testing.T,
	baseURL string,
	apiKey string,
	maxRetries int,
	dependencies httpclient.Dependencies,
) *Client {
	t.Helper()

	return newNLMTestClientWithHTTPClient(
		t,
		&http.Client{},
		baseURL,
		apiKey,
		maxRetries,
		dependencies,
	)
}

func newNLMTestClientWithHTTPClient(
	t *testing.T,
	base *http.Client,
	baseURL string,
	apiKey string,
	maxRetries int,
	dependencies httpclient.Dependencies,
) *Client {
	t.Helper()

	client, err := NewClient(base, Config{
		BaseURL:          baseURL,
		Tool:             "paper-hub",
		Email:            "research@example.test",
		APIKey:           apiKey,
		UserAgent:        "paper-hub-nlm-test/1.0",
		Timeout:          2 * time.Second,
		MaxRetries:       maxRetries,
		MaxWait:          5 * time.Second,
		InitialBackoff:   time.Millisecond,
		MaxBackoff:       time.Millisecond,
		MaxResponseBytes: 1 << 20,
	}, dependencies)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func noSleepNLMDependencies() httpclient.Dependencies {
	return httpclient.Dependencies{
		Now: func() time.Time { return time.Unix(0, 0) },
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return function(request)
}

type readerFunc func([]byte) (int, error)

func (function readerFunc) Read(buffer []byte) (int, error) {
	return function(buffer)
}

type recordingBody struct {
	reader io.Reader
	closed atomic.Bool
}

func (body *recordingBody) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

func (body *recordingBody) Close() error {
	body.closed.Store(true)
	return nil
}

func nlmSummaryPayload(
	uid string,
	titleMainSort string,
	title string,
	issns []string,
) []byte {
	return []byte(fmt.Sprintf(
		`{"result":{"uids":[%q],%q:{`+
			`"uid":%q,`+
			`"titlemainsort":%q,`+
			`"titlemainlist":[{"title":%q,"sorttitle":%q,"aiid":""}],`+
			`"issnlist":[%s],`+
			`"nlmuniqueid":%q,`+
			`"daterevised":"2026-05-08",`+
			`"endyear":"9999",`+
			`"currentindexingstatus":"Y"`+
			`}}}`,
		uid,
		uid,
		uid,
		titleMainSort,
		title,
		titleMainSort,
		strings.Join(issns, ","),
		uid,
	))
}

func sha256String(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
