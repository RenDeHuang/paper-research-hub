package httpclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrRetryBudgetExceeded = errors.New("HTTP retry wait budget exceeded")
	ErrResponseTooLarge    = errors.New("HTTP response body exceeds configured limit")
)

type RateLimit struct {
	Requests int
	Interval time.Duration
}

type Config struct {
	Timeout                  time.Duration
	UserAgent                string
	RateLimit                RateLimit
	MaxRetries               int
	MaxWait                  time.Duration
	InitialBackoff           time.Duration
	MaxBackoff               time.Duration
	MaxResponseBytes         int64
	SensitiveQueryParameters []string
	SensitiveHeaders         []string
}

type Dependencies struct {
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

type Client struct {
	httpClient       *http.Client
	config           Config
	now              func() time.Time
	sleep            func(context.Context, time.Duration) error
	requestInterval  time.Duration
	sensitiveQueries map[string]struct{}
	sensitiveHeaders map[string]struct{}

	rateTurn    chan struct{}
	nextRequest time.Time
}

func New(base *http.Client, config Config, dependencies Dependencies) (*Client, error) {
	if base == nil {
		return nil, errors.New("base HTTP client is required")
	}
	if config.Timeout <= 0 {
		return nil, errors.New("HTTP timeout must be explicit and positive")
	}
	if strings.TrimSpace(config.UserAgent) == "" {
		return nil, errors.New("HTTP User-Agent must be explicit")
	}
	if config.RateLimit.Requests <= 0 {
		return nil, errors.New("HTTP rate limit requests must be positive")
	}
	if config.RateLimit.Interval <= 0 {
		return nil, errors.New("HTTP rate limit interval must be positive")
	}
	requestInterval := config.RateLimit.Interval / time.Duration(config.RateLimit.Requests)
	if requestInterval <= 0 {
		return nil, errors.New("HTTP rate limit interval per request must be positive")
	}
	if config.MaxRetries < 0 {
		return nil, errors.New("HTTP max retries must not be negative")
	}
	if config.MaxWait <= 0 {
		return nil, errors.New("HTTP max retry wait must be explicit and positive")
	}
	if config.InitialBackoff <= 0 {
		return nil, errors.New("HTTP initial backoff must be explicit and positive")
	}
	if config.MaxBackoff <= 0 {
		return nil, errors.New("HTTP max backoff must be explicit and positive")
	}
	if config.MaxBackoff < config.InitialBackoff {
		return nil, errors.New("HTTP max backoff must not be less than initial backoff")
	}
	if config.MaxResponseBytes <= 0 {
		return nil, errors.New("HTTP response body limit must be explicit and positive")
	}

	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	sleep := dependencies.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	clonedHTTPClient := *base
	clonedHTTPClient.Timeout = config.Timeout

	return &Client{
		httpClient:      &clonedHTTPClient,
		config:          config,
		now:             now,
		sleep:           sleep,
		requestInterval: requestInterval,
		rateTurn:        make(chan struct{}, 1),
		sensitiveQueries: normalizedSet(
			append(
				[]string{"api_key", "apikey", "access_token", "token", "key"},
				config.SensitiveQueryParameters...,
			),
		),
		sensitiveHeaders: normalizedSet(
			append(
				[]string{"Authorization", "Proxy-Authorization", "X-API-Key", "Api-Key"},
				config.SensitiveHeaders...,
			),
		),
	}, nil
}

func (client *Client) Do(request *http.Request) (*http.Response, error) {
	return client.do(request, nil, nil)
}

func (client *Client) DoValidated(
	request *http.Request,
	validate func([]byte) error,
	retryableValidationError func(error) bool,
) (*http.Response, error) {
	return client.do(request, validate, retryableValidationError)
}

func (client *Client) do(
	request *http.Request,
	validate func([]byte) error,
	retryableValidationError func(error) bool,
) (*http.Response, error) {
	if client == nil {
		return nil, errors.New("HTTP policy client is nil")
	}
	if request == nil {
		return nil, errors.New("HTTP request is required")
	}
	if request.Context() == nil {
		return nil, errors.New("HTTP request context is required")
	}

	secrets := client.requestSecrets(request)
	waited := time.Duration(0)

	for attempt := 0; ; attempt++ {
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		if err := client.waitForRateLimit(request.Context()); err != nil {
			return nil, err
		}

		attemptRequest, err := cloneRequest(request, attempt)
		if err != nil {
			return nil, client.redactedError(err, request, secrets)
		}
		attemptRequest.Header.Set("User-Agent", client.config.UserAgent)

		response, err := client.httpClient.Do(attemptRequest)
		if err != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			if contextError := request.Context().Err(); contextError != nil {
				return nil, contextError
			}
			return nil, client.redactedError(
				fmt.Errorf("HTTP %s request failed: %w", request.Method, err),
				request,
				secrets,
			)
		}

		body, err := readBounded(response.Body, client.config.MaxResponseBytes)
		closeErr := response.Body.Close()
		if err != nil {
			if contextError := request.Context().Err(); contextError != nil {
				return nil, contextError
			}
			if errors.Is(err, context.Canceled) {
				return nil, context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, context.DeadlineExceeded
			}
			if errors.Is(err, ErrResponseTooLarge) {
				return nil, fmt.Errorf(
					"%w: HTTP %s %s",
					ErrResponseTooLarge,
					request.Method,
					client.redactedURL(request.URL),
				)
			}
			return nil, client.redactedError(
				fmt.Errorf(
					"HTTP %s %s: %w",
					request.Method,
					client.redactedURL(request.URL),
					err,
				),
				request,
				secrets,
			)
		}
		if closeErr != nil {
			return nil, client.redactedError(
				fmt.Errorf("close HTTP response body: %w", closeErr),
				request,
				secrets,
			)
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))

		var retryErr error
		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
			if validate == nil {
				return response, nil
			}
			validationErr := validate(body)
			if validationErr == nil {
				return response, nil
			}
			if retryableValidationError == nil ||
				!retryableValidationError(validationErr) ||
				attempt >= client.config.MaxRetries {
				return nil, client.redactedError(validationErr, request, secrets)
			}
			retryErr = client.redactedError(validationErr, request, secrets)
		} else {
			statusErr := client.statusError(request, response.StatusCode, body, secrets)
			if !retryableStatus(response.StatusCode) || attempt >= client.config.MaxRetries {
				return nil, statusErr
			}
			retryErr = statusErr
		}

		delay := client.retryDelay(response, attempt)
		remaining := client.config.MaxWait - waited
		if delay > remaining {
			return nil, fmt.Errorf(
				"%w: next delay %s exceeds remaining budget %s: %v",
				ErrRetryBudgetExceeded,
				delay,
				remaining,
				retryErr,
			)
		}
		if err := client.sleep(request.Context(), delay); err != nil {
			if contextError := request.Context().Err(); contextError != nil {
				return nil, contextError
			}
			return nil, err
		}
		waited += delay
	}
}

func (client *Client) waitForRateLimit(ctx context.Context) error {
	select {
	case client.rateTurn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() {
		<-client.rateTurn
	}()

	if err := ctx.Err(); err != nil {
		return err
	}
	now := client.now()
	scheduled := now
	if client.nextRequest.After(scheduled) {
		scheduled = client.nextRequest
	}

	delay := scheduled.Sub(now)
	if delay > 0 {
		if err := client.sleep(ctx, delay); err != nil {
			return err
		}
	}
	client.nextRequest = scheduled.Add(client.requestInterval)
	return nil
}

func (client *Client) retryDelay(response *http.Response, retryIndex int) time.Duration {
	if delay, ok := parseRetryAfter(response.Header.Get("Retry-After"), client.now()); ok {
		return delay
	}

	delay := client.config.InitialBackoff
	for index := 0; index < retryIndex; index++ {
		if delay >= client.config.MaxBackoff {
			return client.config.MaxBackoff
		}
		if delay > client.config.MaxBackoff/2 {
			return client.config.MaxBackoff
		}
		delay *= 2
	}
	if delay > client.config.MaxBackoff {
		return client.config.MaxBackoff
	}
	return delay
}

func (client *Client) statusError(
	request *http.Request,
	statusCode int,
	body []byte,
	secrets []string,
) error {
	const maxErrorBodyBytes = 4 << 10
	if len(body) > maxErrorBodyBytes {
		body = body[:maxErrorBodyBytes]
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	message = redactSecrets(message, secrets)

	return fmt.Errorf(
		"HTTP %s %s returned %d: %s",
		request.Method,
		client.redactedURL(request.URL),
		statusCode,
		message,
	)
}

func (client *Client) redactedError(
	err error,
	request *http.Request,
	secrets []string,
) error {
	if err == nil {
		return nil
	}
	message := redactSecrets(err.Error(), secrets)
	if request != nil && request.URL != nil {
		message = strings.ReplaceAll(message, request.URL.String(), client.redactedURL(request.URL))
	}
	return errors.New(message)
}

func (client *Client) redactedURL(original *url.URL) string {
	if original == nil {
		return ""
	}
	cloned := *original
	query := cloned.Query()
	for key := range query {
		if _, sensitive := client.sensitiveQueries[strings.ToLower(key)]; sensitive {
			query.Set(key, "[REDACTED]")
		}
	}
	cloned.RawQuery = query.Encode()
	return cloned.String()
}

func (client *Client) requestSecrets(request *http.Request) []string {
	values := make([]string, 0)
	if request.URL != nil {
		query := request.URL.Query()
		keys := make([]string, 0, len(query))
		for key := range query {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, sensitive := client.sensitiveQueries[strings.ToLower(key)]; !sensitive {
				continue
			}
			values = append(values, query[key]...)
		}
	}
	headerKeys := make([]string, 0, len(request.Header))
	for key := range request.Header {
		headerKeys = append(headerKeys, key)
	}
	sort.Strings(headerKeys)
	for _, key := range headerKeys {
		if _, sensitive := client.sensitiveHeaders[strings.ToLower(key)]; !sensitive {
			continue
		}
		headerValues := request.Header.Values(key)
		values = append(values, headerValues...)
		if strings.EqualFold(key, "Authorization") ||
			strings.EqualFold(key, "Proxy-Authorization") {
			for _, value := range headerValues {
				if credential := authorizationCredential(value); credential != "" {
					values = append(values, credential)
				}
			}
		}
	}
	return normalizeSecrets(values)
}

func authorizationCredential(value string) string {
	trimmed := strings.TrimSpace(value)
	separator := strings.IndexAny(trimmed, " \t\r\n")
	if separator < 0 {
		return ""
	}
	return strings.TrimSpace(trimmed[separator:])
}

func cloneRequest(request *http.Request, attempt int) (*http.Request, error) {
	cloned := request.Clone(request.Context())
	if request.Body == nil || request.Body == http.NoBody {
		return cloned, nil
	}
	if attempt == 0 {
		cloned.Body = request.Body
		return cloned, nil
	}
	if request.GetBody == nil {
		return nil, errors.New("HTTP request body cannot be replayed for retry")
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, fmt.Errorf("recreate HTTP request body for retry: %w", err)
	}
	cloned.Body = body
	return cloned, nil
}

func readBounded(body io.Reader, maximum int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read HTTP response body: %w", err)
	}
	if int64(len(payload)) > maximum {
		return nil, ErrResponseTooLarge
	}
	return payload, nil
}

func retryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		(statusCode >= http.StatusInternalServerError && statusCode <= 599)
}

func parseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		const maximumDuration = time.Duration(1<<63 - 1)
		if seconds > int64(maximumDuration/time.Second) {
			return maximumDuration, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	at, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := at.Sub(now)
	if delay < 0 {
		return 0, true
	}
	return delay, true
}

func redactSecrets(message string, secrets []string) string {
	redacted := message
	for _, secret := range normalizeSecrets(secrets) {
		redacted = strings.ReplaceAll(redacted, secret, "[REDACTED]")
		redacted = strings.ReplaceAll(redacted, url.QueryEscape(secret), "[REDACTED]")
	}
	return redacted
}

func normalizeSecrets(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	secrets := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, exists := unique[value]; exists {
			continue
		}
		unique[value] = struct{}{}
		secrets = append(secrets, value)
	}
	sort.SliceStable(secrets, func(left, right int) bool {
		return len(secrets[left]) > len(secrets[right])
	})
	return secrets
}

func normalizedSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized != "" {
			result[normalized] = struct{}{}
		}
	}
	return result
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
