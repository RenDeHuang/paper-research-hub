package urlverify

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

func TestHTTPObserverFollowsRedirectsAndExtractsWhitelistedHTMLMetaEvidence(
	t *testing.T,
) {
	t.Parallel()

	final := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/article" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("ETag", `"observer-v1"`)
		writer.Header().Set(
			"Last-Modified",
			"Sat, 18 Jul 2026 08:00:00 GMT",
		)
		_, _ = writer.Write([]byte(`
			<html><head>
			<meta name="citation_doi" content=" DOI:10.1000/OBSERVER ">
			<meta content="123456" name="citation_pmid">
			<meta property="og:title" content="DOI:10.1000/not-evidence">
			<title>DOI:10.1000/not-evidence</title>
			</head></html>
		`))
	}))
	t.Cleanup(final.Close)
	finalURL := logicalTestServerURL(
		t,
		final,
		"publisher.example.test",
		"/article",
	)
	source := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(
			writer,
			request,
			finalURL,
			http.StatusFound,
		)
	}))
	t.Cleanup(source.Close)

	sourceURL := logicalTestServerURL(
		t,
		source,
		"resolver.example.test",
		"/source",
	)
	resolver := &staticHostResolver{addresses: map[string][]netip.Addr{
		"resolver.example.test": {
			netip.MustParseAddr("93.184.216.34"),
		},
		"publisher.example.test": {
			netip.MustParseAddr("93.184.216.35"),
		},
	}}
	dialer := &mappedRecordingDialer{targetsByPort: map[string]string{
		testServerPort(t, source): source.Listener.Addr().String(),
		testServerPort(t, final):  final.Listener.Addr().String(),
	}}
	observer := newSecurityTestHTTPObserver(
		t,
		final.Client(),
		resolver,
		dialer,
		10,
		64<<10,
	)
	hostPolicy, err := NewHostPolicy("official-url/v1", []string{
		"resolver.example.test",
		"publisher.example.test",
	})
	if err != nil {
		t.Fatalf("NewHostPolicy() error = %v", err)
	}
	candidate := observerCandidate(
		sourceURL,
		"10.1000/observer",
	)
	observation, err := observer.Observe(
		context.Background(),
		candidate,
		hostPolicy,
	)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.FinalURL != finalURL ||
		observation.HTTPStatus != http.StatusOK ||
		len(observation.RedirectChain) != 2 {
		t.Fatalf("observation HTTP evidence = %#v", observation)
	}
	if observation.ResponseMetadata != (ResponseMetadata{
		ContentType:  "text/html; charset=utf-8",
		ETag:         `"observer-v1"`,
		LastModified: "Sat, 18 Jul 2026 08:00:00 GMT",
	}) {
		t.Fatalf("response metadata = %#v", observation.ResponseMetadata)
	}
	if observation.RedirectChain[1].Metadata != observation.ResponseMetadata {
		t.Fatalf(
			"final redirect metadata = %#v",
			observation.RedirectChain[1].Metadata,
		)
	}
	wantEvidence := []IdentifierEvidence{
		{
			Identifier: StableIdentifier{
				Scheme: "doi",
				Value:  "10.1000/observer",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_doi"]@content`,
			RawValue:   " DOI:10.1000/OBSERVER ",
		},
		{
			Identifier: StableIdentifier{
				Scheme: "pmid",
				Value:  "123456",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_pmid"]@content`,
			RawValue:   "123456",
		},
	}
	if fmt.Sprint(observation.IdentifierEvidence) != fmt.Sprint(wantEvidence) {
		t.Fatalf(
			"identifier evidence = %#v, want %#v",
			observation.IdentifierEvidence,
			wantEvidence,
		)
	}

	verification, err := Verify(VerificationInput{
		Candidate:          candidate,
		ExpectedIdentifier: candidate.Identifier,
		Observation:        observation,
		CheckedAt:          candidate.AssertedAt.Add(time.Hour),
		ExpiresAt:          candidate.AssertedAt.Add(25 * time.Hour),
		EvaluatedAt:        candidate.AssertedAt.Add(time.Hour),
		VerifierVersion:    "official-url-verifier/v1",
		Policy: Policy{
			Version:      "official-url/v1",
			MaxRedirects: 10,
		},
	})
	if err != nil {
		t.Fatalf("Verify(observation) error = %v", err)
	}
	if verification.State != VerificationStateVerified {
		t.Fatalf("verification = %#v", verification)
	}
}

func TestHTTPObserverProducesIdentifierMismatchEvidence(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(
			`<meta name="dc.identifier.doi" content="10.1000/different">`,
		))
	}))
	t.Cleanup(server.Close)

	candidate := observerCandidate(
		logicalTestServerURL(
			t,
			server,
			"mismatch.example.test",
			"/",
		),
		"10.1000/expected",
	)
	observation := observeHTTP(t, server, candidate, 10)
	assertObservedFailure(
		t,
		candidate,
		observation,
		FailureIdentifierMismatch,
	)
}

func TestHTTPObserverStopsRedirectLoopWithCompleteObservedChain(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/a":
			http.Redirect(writer, request, "/b", http.StatusFound)
		case "/b":
			http.Redirect(writer, request, "/a", http.StatusTemporaryRedirect)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	baseURL := logicalTestServerURL(
		t,
		server,
		"loop.example.test",
		"",
	)
	candidate := observerCandidate(baseURL+"/a", "10.1000/loop")
	observation := observeHTTP(t, server, candidate, 10)
	if len(observation.RedirectChain) != 2 ||
		observation.RedirectChain[1].URL != baseURL+"/b" {
		t.Fatalf("loop chain = %#v", observation.RedirectChain)
	}
	assertObservedFailure(t, candidate, observation, FailureRedirectLoop)
}

func TestHTTPObserverStopsAtBoundedRedirectLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		index, err := strconv.Atoi(request.URL.Path[1:])
		if err != nil {
			http.NotFound(writer, request)
			return
		}
		http.Redirect(
			writer,
			request,
			fmt.Sprintf("/%d", index+1),
			http.StatusFound,
		)
	}))
	t.Cleanup(server.Close)

	baseURL := logicalTestServerURL(
		t,
		server,
		"limit.example.test",
		"",
	)
	candidate := observerCandidate(baseURL+"/0", "10.1000/limit")
	observation := observeHTTP(t, server, candidate, 10)
	if len(observation.RedirectChain) != 11 ||
		observation.RedirectChain[10].URL != baseURL+"/10" {
		t.Fatalf("bounded chain = %#v", observation.RedirectChain)
	}
	assertObservedFailure(
		t,
		candidate,
		observation,
		FailureRedirectLimitExceeded,
	)
}

func TestHTTPObserverRejectsMalformedRawPercentEncodingInRedirectLocation(
	t *testing.T,
) {
	t.Parallel()

	for _, location := range []string{
		"/final?view=%zz",
		"/final?view=%",
	} {
		location := location
		t.Run(location, func(t *testing.T) {
			t.Parallel()

			var finalRequests atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				if request.URL.Path == "/final" {
					finalRequests.Add(1)
					writer.Header().Set("Content-Type", "text/html")
					_, _ = writer.Write([]byte(
						`<meta name="citation_doi" content="10.1000/percent">`,
					))
					return
				}
				writer.Header().Set("Location", location)
				writer.WriteHeader(http.StatusFound)
			}))
			t.Cleanup(server.Close)

			candidate := observerCandidate(
				logicalTestServerURL(
					t,
					server,
					"percent.example.test",
					"/source",
				),
				"10.1000/percent",
			)
			observation := observeHTTP(t, server, candidate, 10)
			if finalRequests.Load() != 0 {
				t.Fatalf(
					"observer followed malformed redirect location %q",
					location,
				)
			}
			if len(observation.RedirectChain) != 1 ||
				observation.RedirectChain[0].Location != location {
				t.Fatalf(
					"malformed location observation = %#v",
					observation,
				)
			}
			assertObservedFailure(
				t,
				candidate,
				observation,
				FailureInvalidRedirectChain,
			)
		})
	}
}

func TestHTTPObserverDoesNotGuessIdentifierFromTitleOrURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(`
			<title><meta name="citation_doi" content="10.1000/no-guess"></title>
			<link rel="canonical" href="https://example.test/10.1000/no-guess">
		`))
	}))
	t.Cleanup(server.Close)

	candidate := observerCandidate(
		logicalTestServerURL(
			t,
			server,
			"no-guess.example.test",
			"/10.1000/no-guess",
		),
		"10.1000/no-guess",
	)
	observation := observeHTTP(t, server, candidate, 10)
	if len(observation.IdentifierEvidence) != 0 {
		t.Fatalf(
			"guessed identifier evidence = %#v",
			observation.IdentifierEvidence,
		)
	}
	assertObservedFailure(
		t,
		candidate,
		observation,
		FailureMissingStableIdentifier,
	)
}

func TestHTTPObserverEnforcesBodyLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(
			`<meta name="citation_doi" content="10.1000/limit">`,
		))
	}))
	t.Cleanup(server.Close)

	candidate := observerCandidate(
		logicalTestServerURL(
			t,
			server,
			"body-limit.example.test",
			"/",
		),
		"10.1000/limit",
	)
	observation := observeHTTPWithBodyLimit(
		t,
		server,
		candidate,
		10,
		8,
	)
	if observation.FailureCode != FailureResponseBodyLimit {
		t.Fatalf("Observe(body limit) = %#v", observation)
	}
	assertObservedFailure(
		t,
		candidate,
		observation,
		FailureResponseBodyLimit,
	)
}

func TestHTTPObserverEnforcesTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		_ http.ResponseWriter,
		request *http.Request,
	) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)

	candidate := observerCandidate(
		logicalTestServerURL(
			t,
			server,
			"timeout.example.test",
			"/",
		),
		"10.1000/timeout",
	)
	resolver := &staticHostResolver{addresses: map[string][]netip.Addr{
		"timeout.example.test": {
			netip.MustParseAddr("93.184.216.34"),
		},
	}}
	dialer := &mappedRecordingDialer{targetsByPort: map[string]string{
		testServerPort(t, server): server.Listener.Addr().String(),
	}}
	observer, err := NewHTTPObserver(server.Client(), HTTPObserverConfig{
		Timeout:      50 * time.Millisecond,
		MaxBodyBytes: 1024,
		MaxRedirects: 10,
		Resolver:     resolver,
		Dialer:       dialer,
	})
	if err != nil {
		t.Fatalf("NewHTTPObserver() error = %v", err)
	}
	started := time.Now()
	hostPolicy, err := NewHostPolicy(
		"official-url/v1",
		[]string{"timeout.example.test"},
	)
	if err != nil {
		t.Fatalf("NewHostPolicy() error = %v", err)
	}
	observation, err := observer.Observe(
		context.Background(),
		candidate,
		hostPolicy,
	)
	if err != nil {
		t.Fatalf("Observe(timeout) error = %v", err)
	}
	if observation.FailureCode != FailureNetwork {
		t.Fatalf("Observe(timeout) = %#v", observation)
	}
	if time.Since(started) >= time.Second {
		t.Fatalf("Observe(timeout) exceeded deterministic bound: %v", err)
	}
}

func observeHTTP(
	t *testing.T,
	server *httptest.Server,
	candidate Candidate,
	maxRedirects int,
) HTTPObservation {
	return observeHTTPWithBodyLimit(
		t,
		server,
		candidate,
		maxRedirects,
		64<<10,
	)
}

func observeHTTPWithBodyLimit(
	t *testing.T,
	server *httptest.Server,
	candidate Candidate,
	maxRedirects int,
	maxBodyBytes int64,
) HTTPObservation {
	t.Helper()
	parsed, err := url.Parse(candidate.URL)
	if err != nil {
		t.Fatalf("parse candidate URL: %v", err)
	}
	resolver := &staticHostResolver{addresses: map[string][]netip.Addr{
		parsed.Hostname(): {
			netip.MustParseAddr("93.184.216.34"),
		},
	}}
	dialer := &mappedRecordingDialer{targetsByPort: map[string]string{
		testServerPort(t, server): server.Listener.Addr().String(),
	}}
	observer := newSecurityTestHTTPObserver(
		t,
		server.Client(),
		resolver,
		dialer,
		maxRedirects,
		maxBodyBytes,
	)
	hostPolicy, err := NewHostPolicy(
		"official-url/v1",
		[]string{parsed.Hostname()},
	)
	if err != nil {
		t.Fatalf("NewHostPolicy() error = %v", err)
	}
	observation, err := observer.Observe(
		context.Background(),
		candidate,
		hostPolicy,
	)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	return observation
}

func assertObservedFailure(
	t *testing.T,
	candidate Candidate,
	observation HTTPObservation,
	want FailureCode,
) {
	t.Helper()
	checkedAt := candidate.AssertedAt.Add(time.Hour)
	verification, err := Verify(VerificationInput{
		Candidate:          candidate,
		ExpectedIdentifier: candidate.Identifier,
		Observation:        observation,
		CheckedAt:          checkedAt,
		ExpiresAt:          checkedAt.Add(24 * time.Hour),
		EvaluatedAt:        checkedAt,
		VerifierVersion:    "official-url-verifier/v1",
		Policy: Policy{
			Version:      "official-url/v1",
			MaxRedirects: 10,
		},
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.State != VerificationStateFailed ||
		verification.FailureCode != want {
		t.Fatalf("verification = %#v, want failure %q", verification, want)
	}
}

func observerCandidate(rawURL string, doi string) Candidate {
	return Candidate{
		ID:                    "candidate-observer",
		WorkID:                "work-observer",
		ProjectionAssertionID: "projection-observer",
		NormalizedAssertionID: "normalized-observer",
		SourceRecordID:        "source-observer",
		SourcePath:            "$.url_candidates[0].url",
		Channel:               scope.ContentChannelJournalPublished,
		LinkRole:              LinkRoleOfficialArticle,
		URL:                   rawURL,
		ParserVersion:         "observer-fixture/v1",
		Identifier: StableIdentifier{
			Scheme: "doi",
			Value:  doi,
		},
		AssertedAt: time.Date(
			2026,
			time.July,
			18,
			8,
			0,
			0,
			0,
			time.UTC,
		),
	}
}
