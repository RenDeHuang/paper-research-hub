package urlverify

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestHTTPObserverResolvesEveryHopAndDialsOnlyValidatedIPs(
	t *testing.T,
) {
	t.Parallel()

	finalServer := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(
			`<meta name="citation_doi" content="10.1000/safe-dial">`,
		))
	}))
	t.Cleanup(finalServer.Close)
	finalURL := logicalTestServerURL(
		t,
		finalServer,
		"publisher.example.test",
		"/article",
	)
	sourceServer := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(writer, request, finalURL, http.StatusFound)
	}))
	t.Cleanup(sourceServer.Close)
	sourceURL := logicalTestServerURL(
		t,
		sourceServer,
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
		testServerPort(t, sourceServer): sourceServer.Listener.Addr().String(),
		testServerPort(t, finalServer):  finalServer.Listener.Addr().String(),
	}}
	observer := newSecurityTestHTTPObserver(
		t,
		sourceServer.Client(),
		resolver,
		dialer,
		MaximumRedirects,
		64<<10,
	)
	policy, err := NewHostPolicy("official-url/v1", []string{
		"resolver.example.test",
		"publisher.example.test",
	})
	if err != nil {
		t.Fatalf("NewHostPolicy() error = %v", err)
	}
	candidate := observerCandidate(sourceURL, "10.1000/safe-dial")
	observation, err := observer.Observe(
		context.Background(),
		candidate,
		policy,
	)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.FailureCode != FailureNone ||
		observation.FinalURL != finalURL ||
		len(observation.RedirectChain) != 2 {
		t.Fatalf("observation = %#v", observation)
	}
	if !slices.Equal(
		resolver.Calls(),
		[]string{
			"resolver.example.test",
			"publisher.example.test",
		},
	) {
		t.Fatalf("resolver calls = %#v", resolver.Calls())
	}
	wantDialAddresses := []string{
		net.JoinHostPort(
			"93.184.216.34",
			testServerPort(t, sourceServer),
		),
		net.JoinHostPort(
			"93.184.216.35",
			testServerPort(t, finalServer),
		),
	}
	if !slices.Equal(dialer.Calls(), wantDialAddresses) {
		t.Fatalf(
			"dial addresses = %#v, want validated IPs %#v",
			dialer.Calls(),
			wantDialAddresses,
		)
	}
}

func TestHTTPObserverRejectsUnregisteredAndUnsafeHostsBeforeDial(
	t *testing.T,
) {
	t.Parallel()

	candidate := observerCandidate(
		"https://blocked.example.test/article",
		"10.1000/blocked",
	)
	tests := []struct {
		name        string
		policyHosts []string
		addresses   []netip.Addr
		want        FailureCode
	}{
		{
			name: "host absent from registry policy",
			want: FailureHostNotAllowed,
		},
		{
			name:        "one unsafe address rejects the full DNS answer",
			policyHosts: []string{"blocked.example.test"},
			addresses: []netip.Addr{
				netip.MustParseAddr("93.184.216.34"),
				netip.MustParseAddr("10.0.0.1"),
			},
			want: FailureUnsafeResolvedAddress,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolver := &staticHostResolver{addresses: map[string][]netip.Addr{
				"blocked.example.test": test.addresses,
			}}
			dialer := &mappedRecordingDialer{}
			observer := newSecurityTestHTTPObserver(
				t,
				&http.Client{Transport: http.DefaultTransport},
				resolver,
				dialer,
				MaximumRedirects,
				64<<10,
			)
			policy, err := NewHostPolicy(
				"official-url/v1",
				test.policyHosts,
			)
			if err != nil {
				t.Fatalf("NewHostPolicy() error = %v", err)
			}
			observation, err := observer.Observe(
				context.Background(),
				candidate,
				policy,
			)
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if observation.FailureCode != test.want {
				t.Fatalf(
					"FailureCode = %q, want %q",
					observation.FailureCode,
					test.want,
				)
			}
			if len(dialer.Calls()) != 0 {
				t.Fatalf("unsafe observer dialed %#v", dialer.Calls())
			}
		})
	}
}

func TestHTTPObserverRejectsEveryNonPublicAddressClass(t *testing.T) {
	t.Parallel()

	for _, rawAddress := range []string{
		"127.0.0.1",
		"10.0.0.1",
		"100.64.0.1",
		"169.254.1.1",
		"192.0.0.1",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"0.0.0.0",
		"224.0.0.1",
		"::1",
		"fc00::1",
		"fe80::1",
		"::",
		"ff02::1",
		"::ffff:10.0.0.1",
	} {
		rawAddress := rawAddress
		t.Run(rawAddress, func(t *testing.T) {
			t.Parallel()

			resolver := &staticHostResolver{addresses: map[string][]netip.Addr{
				"unsafe.example.test": {
					netip.MustParseAddr(rawAddress),
				},
			}}
			dialer := &mappedRecordingDialer{}
			observer := newSecurityTestHTTPObserver(
				t,
				&http.Client{Transport: http.DefaultTransport},
				resolver,
				dialer,
				MaximumRedirects,
				64<<10,
			)
			policy, err := NewHostPolicy(
				"official-url/v1",
				[]string{"unsafe.example.test"},
			)
			if err != nil {
				t.Fatalf("NewHostPolicy() error = %v", err)
			}
			observation, err := observer.Observe(
				context.Background(),
				observerCandidate(
					"https://unsafe.example.test/article",
					"10.1000/unsafe",
				),
				policy,
			)
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if observation.FailureCode != FailureUnsafeResolvedAddress {
				t.Fatalf("observation = %#v", observation)
			}
			if len(dialer.Calls()) != 0 {
				t.Fatalf("unsafe address dialed %#v", dialer.Calls())
			}
		})
	}
}

func TestHTTPObserverRejectsHTTPAtEveryRequestHopBeforeDNS(t *testing.T) {
	t.Parallel()

	resolver := &staticHostResolver{addresses: map[string][]netip.Addr{
		"publisher.example.test": {
			netip.MustParseAddr("93.184.216.34"),
		},
	}}
	observer := newSecurityTestHTTPObserver(
		t,
		&http.Client{Transport: http.DefaultTransport},
		resolver,
		&mappedRecordingDialer{},
		MaximumRedirects,
		64<<10,
	)
	policy, err := NewHostPolicy(
		"official-url/v1",
		[]string{"publisher.example.test"},
	)
	if err != nil {
		t.Fatalf("NewHostPolicy() error = %v", err)
	}
	_, _, failure, err := observer.resolveRequestTarget(
		context.Background(),
		"http://publisher.example.test/article",
		policy,
	)
	if err != nil {
		t.Fatalf("resolveRequestTarget() error = %v", err)
	}
	if failure != FailureNonHTTPSFinalURL {
		t.Fatalf(
			"resolveRequestTarget() failure = %q, want %q",
			failure,
			FailureNonHTTPSFinalURL,
		)
	}
	if len(resolver.Calls()) != 0 {
		t.Fatalf("HTTP downgrade resolved DNS hosts %#v", resolver.Calls())
	}
}

func TestHTTPObserverPersistsInternalDNSTimeoutAsFailure(t *testing.T) {
	t.Parallel()

	observer := newSecurityTestHTTPObserver(
		t,
		&http.Client{Transport: http.DefaultTransport},
		blockingHostResolver{},
		&mappedRecordingDialer{},
		MaximumRedirects,
		64<<10,
	)
	observer.timeout = 10 * time.Millisecond
	policy, err := NewHostPolicy(
		"official-url/v1",
		[]string{"timeout.example.test"},
	)
	if err != nil {
		t.Fatalf("NewHostPolicy() error = %v", err)
	}
	observation, err := observer.Observe(
		context.Background(),
		observerCandidate(
			"https://timeout.example.test/article",
			"10.1000/dns-timeout",
		),
		policy,
	)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.FailureCode != FailureDNSResolution {
		t.Fatalf(
			"FailureCode = %q, want %q",
			observation.FailureCode,
			FailureDNSResolution,
		)
	}
}

func TestHTTPObserverClassifiesDNSAndNetworkFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		resolverErr error
		dialerErr   error
		want        FailureCode
	}{
		{
			name:        "DNS resolution",
			resolverErr: errors.New("resolver unavailable"),
			want:        FailureDNSResolution,
		},
		{
			name:      "network dial",
			dialerErr: errors.New("dial unavailable"),
			want:      FailureNetwork,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolver := &staticHostResolver{
				addresses: map[string][]netip.Addr{
					"network.example.test": {
						netip.MustParseAddr("93.184.216.34"),
					},
				},
				err: test.resolverErr,
			}
			dialer := &mappedRecordingDialer{err: test.dialerErr}
			observer := newSecurityTestHTTPObserver(
				t,
				&http.Client{Transport: http.DefaultTransport},
				resolver,
				dialer,
				MaximumRedirects,
				64<<10,
			)
			policy, err := NewHostPolicy(
				"official-url/v1",
				[]string{"network.example.test"},
			)
			if err != nil {
				t.Fatalf("NewHostPolicy() error = %v", err)
			}
			observation, err := observer.Observe(
				context.Background(),
				observerCandidate(
					"https://network.example.test/article",
					"10.1000/network",
				),
				policy,
			)
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if observation.FailureCode != test.want {
				t.Fatalf(
					"FailureCode = %q, want %q",
					observation.FailureCode,
					test.want,
				)
			}
		})
	}
}

type staticHostResolver struct {
	mu        sync.Mutex
	addresses map[string][]netip.Addr
	err       error
	calls     []string
}

type blockingHostResolver struct{}

func (blockingHostResolver) LookupNetIP(
	ctx context.Context,
	_ string,
	_ string,
) ([]netip.Addr, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (resolver *staticHostResolver) LookupNetIP(
	_ context.Context,
	network string,
	host string,
) ([]netip.Addr, error) {
	if network != "ip" {
		return nil, errors.New("unexpected resolver network")
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls = append(resolver.calls, host)
	if resolver.err != nil {
		return nil, resolver.err
	}
	return slices.Clone(resolver.addresses[host]), nil
}

func (resolver *staticHostResolver) Calls() []string {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return slices.Clone(resolver.calls)
}

type mappedRecordingDialer struct {
	mu            sync.Mutex
	targetsByPort map[string]string
	err           error
	calls         []string
}

func (dialer *mappedRecordingDialer) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	dialer.mu.Lock()
	dialer.calls = append(dialer.calls, address)
	dialerErr := dialer.err
	targets := dialer.targetsByPort
	dialer.mu.Unlock()
	if dialerErr != nil {
		return nil, dialerErr
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	target := targets[port]
	if target == "" {
		return nil, errors.New("missing mapped dial target")
	}
	var systemDialer net.Dialer
	return systemDialer.DialContext(ctx, network, target)
}

func (dialer *mappedRecordingDialer) Calls() []string {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	return slices.Clone(dialer.calls)
}

func newSecurityTestHTTPObserver(
	t *testing.T,
	client *http.Client,
	resolver HostResolver,
	dialer ContextDialer,
	maxRedirects int,
	maxBodyBytes int64,
) *HTTPObserver {
	t.Helper()
	testClient := *client
	if transport, ok := client.Transport.(*http.Transport); ok {
		testTransport := transport.Clone()
		if testTransport.TLSClientConfig != nil {
			testTransport.TLSClientConfig =
				testTransport.TLSClientConfig.Clone()
			testTransport.TLSClientConfig.InsecureSkipVerify = true
		}
		testClient.Transport = testTransport
	}
	observer, err := NewHTTPObserver(&testClient, HTTPObserverConfig{
		Timeout:      2 * time.Second,
		MaxBodyBytes: maxBodyBytes,
		MaxRedirects: maxRedirects,
		Resolver:     resolver,
		Dialer:       dialer,
	})
	if err != nil {
		t.Fatalf("NewHTTPObserver() error = %v", err)
	}
	return observer
}

func logicalTestServerURL(
	t *testing.T,
	server *httptest.Server,
	hostname string,
	path string,
) string {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	parsed.Host = net.JoinHostPort(hostname, parsed.Port())
	parsed.Path = path
	return parsed.String()
}

func testServerPort(t *testing.T, server *httptest.Server) string {
	t.Helper()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("parse test server address: %v", err)
	}
	return port
}
