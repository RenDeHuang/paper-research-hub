package urlverify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrHTTPBodyLimitExceeded = errors.New(
	"official URL HTTP response body exceeds the configured limit",
)

type HTTPObserverConfig struct {
	Timeout      time.Duration
	MaxBodyBytes int64
	MaxRedirects int
	Resolver     HostResolver
	Dialer       ContextDialer
}

func (config HTTPObserverConfig) Validate() error {
	if config.Timeout <= 0 {
		return errors.New("official URL HTTP observer timeout must be positive")
	}
	if config.MaxBodyBytes <= 0 {
		return errors.New(
			"official URL HTTP observer body limit must be positive",
		)
	}
	if config.MaxRedirects < 0 ||
		config.MaxRedirects > MaximumRedirects {
		return fmt.Errorf(
			"official URL HTTP observer redirect bound must be between 0 and %d",
			MaximumRedirects,
		)
	}
	if config.Resolver == nil {
		return errors.New(
			"official URL HTTP observer requires an explicit host resolver",
		)
	}
	if config.Dialer == nil {
		return errors.New(
			"official URL HTTP observer requires an explicit network dialer",
		)
	}
	return nil
}

type HostResolver interface {
	LookupNetIP(
		context.Context,
		string,
		string,
	) ([]netip.Addr, error)
}

type ContextDialer interface {
	DialContext(
		context.Context,
		string,
		string,
	) (net.Conn, error)
}

type HTTPObserver struct {
	transport    *http.Transport
	resolver     HostResolver
	dialer       ContextDialer
	timeout      time.Duration
	maxBodyBytes int64
	maxRedirects int
}

func NewHTTPObserver(
	client *http.Client,
	config HTTPObserverConfig,
) (*HTTPObserver, error) {
	if client == nil {
		return nil, errors.New(
			"official URL HTTP observer requires an explicit HTTP client",
		)
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	var baseTransport *http.Transport
	switch transport := client.Transport.(type) {
	case nil:
		baseTransport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		baseTransport = transport.Clone()
	default:
		return nil, errors.New(
			"official URL HTTP observer requires an *http.Transport",
		)
	}
	baseTransport.Proxy = nil
	baseTransport.DisableKeepAlives = true
	baseTransport.DialContext = nil
	baseTransport.DialTLSContext = nil
	baseTransport.DialTLS = nil
	return &HTTPObserver{
		transport:    baseTransport,
		resolver:     config.Resolver,
		dialer:       config.Dialer,
		timeout:      config.Timeout,
		maxBodyBytes: config.MaxBodyBytes,
		maxRedirects: config.MaxRedirects,
	}, nil
}

func (observer *HTTPObserver) Observe(
	ctx context.Context,
	candidate Candidate,
	hostPolicy HostPolicy,
) (HTTPObservation, error) {
	if ctx == nil {
		return HTTPObservation{}, errors.New(
			"official URL HTTP observer context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return HTTPObservation{}, err
	}
	if observer == nil {
		return HTTPObservation{}, errors.New(
			"official URL HTTP observer is nil",
		)
	}
	if err := candidate.Validate(); err != nil {
		return HTTPObservation{}, err
	}
	if !exactNonEmpty(hostPolicy.PolicyVersion()) {
		return HTTPObservation{}, errors.New(
			"official URL HTTP observer requires an immutable host policy",
		)
	}

	requestContext, cancel := context.WithTimeout(ctx, observer.timeout)
	defer cancel()

	currentURL := candidate.URL
	seen := make(map[string]struct{}, observer.maxRedirects+1)
	redirectsFollowed := 0
	observation := HTTPObservation{FinalURL: candidate.URL}

	for {
		canonical, err := canonicalHTTPURL(currentURL)
		if err != nil {
			return observation, err
		}
		seen[canonical] = struct{}{}

		requestURL, dialAddress, failureCode, err :=
			observer.resolveRequestTarget(
				requestContext,
				currentURL,
				hostPolicy,
			)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return observation, ctxErr
			}
			if errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, context.Canceled) {
				observation.FailureCode = FailureDNSResolution
				return observation, nil
			}
			return observation, err
		}
		if failureCode != FailureNone {
			observation.FailureCode = failureCode
			return observation, nil
		}
		request, err := http.NewRequestWithContext(
			requestContext,
			http.MethodGet,
			requestURL.String(),
			nil,
		)
		if err != nil {
			return observation, fmt.Errorf(
				"build official URL HTTP request: %w",
				err,
			)
		}
		request.Header.Set(
			"Accept",
			"text/html, application/xhtml+xml",
		)
		request.Header.Set(
			"User-Agent",
			"paper-research-hub-official-url-observer/1",
		)
		client, transport := observer.clientForRequest(
			requestURL,
			dialAddress,
		)
		response, err := client.Do(request)
		if err != nil {
			transport.CloseIdleConnections()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return observation, ctxErr
			}
			observation.FailureCode = FailureNetwork
			return observation, nil
		}

		metadata := responseMetadata(response.Header)
		hop := RedirectHop{
			URL:        response.Request.URL.String(),
			StatusCode: response.StatusCode,
			Location:   response.Header.Get("Location"),
			Metadata:   metadata,
		}
		observation.RedirectChain = append(
			observation.RedirectChain,
			hop,
		)
		observation.FinalURL = hop.URL
		observation.HTTPStatus = hop.StatusCode
		observation.ResponseMetadata = metadata

		if !redirectStatus(response.StatusCode) {
			body, readErr := readBoundedResponseBody(
				response.Body,
				observer.maxBodyBytes,
			)
			transport.CloseIdleConnections()
			if readErr != nil {
				if errors.Is(readErr, ErrHTTPBodyLimitExceeded) {
					observation.FailureCode = FailureResponseBodyLimit
				} else {
					observation.FailureCode = FailureNetwork
				}
				return observation, nil
			}
			observation.IdentifierEvidence = extractHTMLMetaIdentifiers(
				metadata.ContentType,
				body,
			)
			return observation, nil
		}
		if closeErr := response.Body.Close(); closeErr != nil {
			transport.CloseIdleConnections()
			observation.FailureCode = FailureNetwork
			return observation, nil
		}
		transport.CloseIdleConnections()
		if !exactNonEmpty(hop.Location) {
			return observation, nil
		}
		current, err := parseAbsoluteHTTPURL(hop.URL)
		if err != nil {
			return observation, err
		}
		if err := ValidateRawPercentEncoding(hop.Location); err != nil {
			return observation, nil
		}
		location, err := url.Parse(hop.Location)
		if err != nil {
			return observation, nil
		}
		nextURL := current.ResolveReference(location)
		nextCanonical, err := canonicalHTTPURL(nextURL.String())
		if err != nil {
			return observation, nil
		}
		if _, loop := seen[nextCanonical]; loop {
			return observation, nil
		}
		if redirectsFollowed >= observer.maxRedirects {
			return observation, nil
		}
		redirectsFollowed++
		currentURL = nextURL.String()
	}
}

func (observer *HTTPObserver) resolveRequestTarget(
	ctx context.Context,
	rawURL string,
	hostPolicy HostPolicy,
) (*url.URL, string, FailureCode, error) {
	parsed, err := parseAbsoluteHTTPURL(rawURL)
	if err != nil {
		return nil, "", FailureNone, err
	}
	if parsed.Scheme != "https" {
		return parsed, "", FailureNonHTTPSFinalURL, nil
	}
	hostname := strings.ToLower(parsed.Hostname())
	if !hostPolicy.Allows(hostname) {
		return parsed, "", FailureHostNotAllowed, nil
	}
	addresses, err := observer.resolver.LookupNetIP(
		ctx,
		"ip",
		hostname,
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil &&
			errors.Is(err, ctxErr) {
			return nil, "", FailureNone, ctxErr
		}
		return parsed, "", FailureDNSResolution, nil
	}
	if len(addresses) == 0 {
		return parsed, "", FailureDNSResolution, nil
	}
	validated := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		normalized := address.Unmap()
		if !safeOfficialURLAddress(normalized) {
			return parsed, "", FailureUnsafeResolvedAddress, nil
		}
		validated = append(validated, normalized)
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return parsed, net.JoinHostPort(validated[0].String(), port),
		FailureNone, nil
}

func safeOfficialURLAddress(address netip.Addr) bool {
	if !address.IsValid() ||
		address.Zone() != "" ||
		!address.IsGlobalUnicast() ||
		address.IsPrivate() ||
		address.IsLoopback() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() ||
		address.IsInterfaceLocalMulticast() ||
		address.IsUnspecified() ||
		address.IsMulticast() {
		return false
	}
	for _, prefix := range blockedOfficialURLPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var blockedOfficialURLPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func (observer *HTTPObserver) clientForRequest(
	requestURL *url.URL,
	dialAddress string,
) (*http.Client, *http.Transport) {
	transport := observer.transport.Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	transport.DialTLSContext = nil
	transport.DialTLS = nil
	expectedAddress := requestURL.Host
	if requestURL.Port() == "" {
		if requestURL.Scheme == "https" {
			expectedAddress = net.JoinHostPort(
				requestURL.Hostname(),
				"443",
			)
		} else {
			expectedAddress = net.JoinHostPort(
				requestURL.Hostname(),
				"80",
			)
		}
	}
	transport.DialContext = func(
		ctx context.Context,
		network string,
		address string,
	) (net.Conn, error) {
		if !strings.EqualFold(address, expectedAddress) {
			return nil, errors.New(
				"official URL transport attempted an unexpected dial target",
			)
		}
		return observer.dialer.DialContext(
			ctx,
			network,
			dialAddress,
		)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   observer.timeout,
		CheckRedirect: func(
			_ *http.Request,
			_ []*http.Request,
		) error {
			return http.ErrUseLastResponse
		},
	}, transport
}

func responseMetadata(header http.Header) ResponseMetadata {
	return ResponseMetadata{
		ContentType:  header.Get("Content-Type"),
		ETag:         header.Get("ETag"),
		LastModified: header.Get("Last-Modified"),
	}
}

func readBoundedResponseBody(
	body io.ReadCloser,
	limit int64,
) ([]byte, error) {
	defer func() { _ = body.Close() }()
	content, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf(
			"read official URL HTTP response body: %w",
			err,
		)
	}
	if int64(len(content)) > limit {
		return nil, ErrHTTPBodyLimitExceeded
	}
	return content, nil
}

func extractHTMLMetaIdentifiers(
	contentType string,
	body []byte,
) []IdentifierEvidence {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil ||
		(mediaType != "text/html" &&
			mediaType != "application/xhtml+xml") {
		return nil
	}
	metaTags := scanHTMLMetaTags(body)
	result := make([]IdentifierEvidence, 0, len(metaTags))
	for _, attributes := range metaTags {
		metaName, hasName := attributes["name"]
		rawValue, hasContent := attributes["content"]
		if !hasName || !hasContent {
			continue
		}
		metaName = strings.ToLower(strings.TrimSpace(metaName))
		scheme, value, accepted := stableIdentifierFromMeta(
			metaName,
			rawValue,
		)
		if !accepted {
			continue
		}
		identifier, normalizeErr := NormalizeStableIdentifier(
			StableIdentifier{Scheme: scheme, Value: value},
		)
		if normalizeErr != nil {
			continue
		}
		sourcePath := fmt.Sprintf(
			`meta[name=%q]@content`,
			metaName,
		)
		evidence := IdentifierEvidence{
			Identifier: identifier,
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: sourcePath,
			RawValue:   rawValue,
		}
		if evidence.Validate() == nil {
			result = append(result, evidence)
		}
	}
	return result
}

func stableIdentifierFromMeta(
	metaName string,
	rawValue string,
) (string, string, bool) {
	switch metaName {
	case "citation_doi", "dc.identifier.doi", "prism.doi":
		return "doi", rawValue, true
	case "citation_pmid":
		return "pmid", rawValue, true
	case "citation_pmcid":
		return "pmcid", rawValue, true
	case "citation_arxiv_id":
		return "arxiv", rawValue, true
	case "dc.identifier":
		return explicitDCIdentifier(rawValue)
	default:
		return "", "", false
	}
}

func explicitDCIdentifier(rawValue string) (string, string, bool) {
	value := strings.TrimSpace(rawValue)
	lower := strings.ToLower(value)
	for _, prefix := range []struct {
		value  string
		scheme string
	}{
		{value: "doi:", scheme: "doi"},
		{value: "https://doi.org/", scheme: "doi"},
		{value: "http://doi.org/", scheme: "doi"},
		{value: "pmid:", scheme: "pmid"},
		{value: "pmcid:", scheme: "pmcid"},
		{value: "arxiv:", scheme: "arxiv"},
	} {
		if strings.HasPrefix(lower, prefix.value) {
			return prefix.scheme, value, true
		}
	}
	return "", "", false
}

func scanHTMLMetaTags(body []byte) []map[string]string {
	result := make([]map[string]string, 0)
	for offset := 0; offset < len(body); {
		relative := bytes.IndexByte(body[offset:], '<')
		if relative < 0 {
			break
		}
		start := offset + relative
		if bytes.HasPrefix(body[start:], []byte("<!--")) {
			end := bytes.Index(body[start+4:], []byte("-->"))
			if end < 0 {
				break
			}
			offset = start + 4 + end + 3
			continue
		}
		end := htmlTagEnd(body, start+1)
		if end < 0 {
			break
		}
		name, attributesStart, closing := htmlTagName(
			body[start+1 : end],
		)
		if closing {
			offset = end + 1
			continue
		}
		switch name {
		case "script", "style", "title", "textarea",
			"xmp", "iframe", "noembed", "noframes":
			closeStart := indexFoldASCII(
				body[end+1:],
				[]byte("</"+name),
			)
			if closeStart < 0 {
				return result
			}
			closeEnd := htmlTagEnd(
				body,
				end+1+closeStart+2+len(name),
			)
			if closeEnd < 0 {
				return result
			}
			offset = closeEnd + 1
			continue
		case "meta":
			result = append(
				result,
				parseHTMLAttributes(body[start+1+attributesStart:end]),
			)
		}
		offset = end + 1
	}
	return result
}

func htmlTagEnd(body []byte, offset int) int {
	var quote byte
	for index := offset; index < len(body); index++ {
		switch body[index] {
		case '\'', '"':
			if quote == 0 {
				quote = body[index]
			} else if quote == body[index] {
				quote = 0
			}
		case '>':
			if quote == 0 {
				return index
			}
		}
	}
	return -1
}

func htmlTagName(tag []byte) (string, int, bool) {
	index := 0
	for index < len(tag) && htmlSpace(tag[index]) {
		index++
	}
	closing := index < len(tag) && tag[index] == '/'
	if closing {
		index++
	}
	start := index
	for index < len(tag) && htmlNameByte(tag[index]) {
		index++
	}
	if start == index {
		return "", len(tag), closing
	}
	return strings.ToLower(string(tag[start:index])), index, closing
}

func parseHTMLAttributes(tag []byte) map[string]string {
	attributes := make(map[string]string)
	index := 0
	for index < len(tag) {
		for index < len(tag) &&
			(htmlSpace(tag[index]) || tag[index] == '/') {
			index++
		}
		nameStart := index
		for index < len(tag) && htmlNameByte(tag[index]) {
			index++
		}
		if nameStart == index {
			index++
			continue
		}
		name := strings.ToLower(string(tag[nameStart:index]))
		for index < len(tag) && htmlSpace(tag[index]) {
			index++
		}
		value := ""
		if index < len(tag) && tag[index] == '=' {
			index++
			for index < len(tag) && htmlSpace(tag[index]) {
				index++
			}
			if index < len(tag) &&
				(tag[index] == '\'' || tag[index] == '"') {
				quote := tag[index]
				index++
				valueStart := index
				for index < len(tag) && tag[index] != quote {
					index++
				}
				value = string(tag[valueStart:index])
				if index < len(tag) {
					index++
				}
			} else {
				valueStart := index
				for index < len(tag) &&
					!htmlSpace(tag[index]) &&
					tag[index] != '>' {
					index++
				}
				value = string(tag[valueStart:index])
			}
		}
		if _, exists := attributes[name]; !exists {
			attributes[name] = html.UnescapeString(value)
		}
	}
	return attributes
}

func htmlSpace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func htmlNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '-' ||
		value == '_' ||
		value == '.' ||
		value == ':'
}

func indexFoldASCII(haystack []byte, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for index := 0; index+len(needle) <= len(haystack); index++ {
		matched := true
		for offset := range needle {
			left := haystack[index+offset]
			right := needle[offset]
			if left >= 'A' && left <= 'Z' {
				left += 'a' - 'A'
			}
			if right >= 'A' && right <= 'Z' {
				right += 'a' - 'A'
			}
			if left != right {
				matched = false
				break
			}
		}
		if matched {
			return index
		}
	}
	return -1
}
