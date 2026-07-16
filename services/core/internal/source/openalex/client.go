package openalex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
)

type Config struct {
	BaseURL          string
	APIKey           string
	ContactEmail     string
	UserAgent        string
	PerPage          int
	Timeout          time.Duration
	RateLimit        httpclient.RateLimit
	MaxRetries       int
	MaxWait          time.Duration
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	MaxResponseBytes int64
}

type Client struct {
	baseURL      *url.URL
	apiKey       string
	contactEmail string
	perPage      int
	httpClient   *httpclient.Client
}

func NewClient(
	base *http.Client,
	config Config,
	dependencies httpclient.Dependencies,
) (*Client, error) {
	baseURL, err := validateBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("OpenAlex API key is required")
	}
	if strings.TrimSpace(config.ContactEmail) == "" {
		return nil, errors.New("OpenAlex contact email is required")
	}
	address, err := mail.ParseAddress(config.ContactEmail)
	if err != nil || address.Address != config.ContactEmail {
		return nil, errors.New("OpenAlex contact email must be a valid email address")
	}
	if config.PerPage < 1 || config.PerPage > 100 {
		return nil, errors.New("OpenAlex per_page must be between 1 and 100")
	}

	policy, err := httpclient.New(base, httpclient.Config{
		Timeout:                  config.Timeout,
		UserAgent:                config.UserAgent,
		RateLimit:                config.RateLimit,
		MaxRetries:               config.MaxRetries,
		MaxWait:                  config.MaxWait,
		InitialBackoff:           config.InitialBackoff,
		MaxBackoff:               config.MaxBackoff,
		MaxResponseBytes:         config.MaxResponseBytes,
		SensitiveQueryParameters: []string{"api_key"},
	}, dependencies)
	if err != nil {
		return nil, fmt.Errorf("create OpenAlex HTTP policy: %w", err)
	}

	return &Client{
		baseURL:      baseURL,
		apiKey:       config.APIKey,
		contactEmail: config.ContactEmail,
		perPage:      config.PerPage,
		httpClient:   policy,
	}, nil
}

func (client *Client) Fetch(ctx context.Context, query source.Query) source.ClientSequence {
	return func(yield func(source.Record, error) bool) {
		if client == nil {
			yield(source.Record{}, errors.New("OpenAlex client is nil"))
			return
		}
		if ctx == nil {
			yield(source.Record{}, errors.New("OpenAlex fetch context is required"))
			return
		}
		if query.MaxResults <= 0 {
			yield(source.Record{}, errors.New("OpenAlex max_results must be positive"))
			return
		}

		cursor := "*"
		seenCursors := map[string]struct{}{cursor: {}}
		emitted := 0

		for emitted < query.MaxResults {
			if err := ctx.Err(); err != nil {
				yield(source.Record{}, err)
				return
			}

			pageSize := min(client.perPage, query.MaxResults-emitted)
			endpoint := client.worksURL(query, cursor, pageSize)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				yield(source.Record{}, fmt.Errorf("create OpenAlex works request: %w", err))
				return
			}
			response, err := client.httpClient.Do(request)
			if err != nil {
				yield(source.Record{}, fmt.Errorf("fetch OpenAlex works page: %w", err))
				return
			}
			payload, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if readErr != nil {
				yield(source.Record{}, fmt.Errorf("read OpenAlex works response: %w", readErr))
				return
			}
			if closeErr != nil {
				yield(source.Record{}, fmt.Errorf("close OpenAlex works response: %w", closeErr))
				return
			}

			page, err := decodePage(payload)
			if err != nil {
				yield(source.Record{}, err)
				return
			}
			if len(page.Results) == 0 &&
				page.NextCursor != nil &&
				*page.NextCursor != "" {
				yield(
					source.Record{},
					fmt.Errorf(
						"OpenAlex protocol error: empty results page requires continuation cursor %q",
						*page.NextCursor,
					),
				)
				return
			}
			pageStart := emitted
			for index, raw := range page.Results {
				if emitted >= query.MaxResults {
					return
				}
				record, err := Parse(raw)
				if err != nil {
					yield(
						source.Record{},
						fmt.Errorf("parse OpenAlex result %d: %w", pageStart+index+1, err),
					)
					return
				}
				emitted++
				if !yield(record, nil) {
					return
				}
			}

			if page.NextCursor == nil || *page.NextCursor == "" {
				return
			}
			nextCursor := *page.NextCursor
			if _, exists := seenCursors[nextCursor]; exists {
				yield(
					source.Record{},
					fmt.Errorf("OpenAlex cursor did not advance from %q", cursor),
				)
				return
			}
			seenCursors[nextCursor] = struct{}{}
			cursor = nextCursor
		}
	}
}

func (client *Client) worksURL(query source.Query, cursor string, perPage int) string {
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/works"
	values := endpoint.Query()
	values.Set("api_key", client.apiKey)
	values.Set("mailto", client.contactEmail)
	values.Set("cursor", cursor)
	values.Set("per_page", strconv.Itoa(perPage))
	if query.Search != "" {
		values.Set("search", query.Search)
	}
	if query.Filter != "" {
		values.Set("filter", query.Filter)
	}
	endpoint.RawQuery = values.Encode()
	return endpoint.String()
}

type page struct {
	NextCursor *string
	Results    []json.RawMessage
}

func decodePage(payload []byte) (page, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return page{}, fmt.Errorf("decode OpenAlex works page JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return page{}, errors.New("OpenAlex works page JSON contains multiple values")
		}
		return page{}, fmt.Errorf("decode trailing OpenAlex works page JSON: %w", err)
	}

	resultsPayload, ok := fields["results"]
	if !ok || bytes.Equal(bytes.TrimSpace(resultsPayload), []byte("null")) {
		return page{}, errors.New("OpenAlex works page JSON requires a results array")
	}
	var results []json.RawMessage
	if err := json.Unmarshal(resultsPayload, &results); err != nil {
		return page{}, fmt.Errorf("decode OpenAlex works page results JSON: %w", err)
	}

	metaPayload, ok := fields["meta"]
	if !ok || bytes.Equal(bytes.TrimSpace(metaPayload), []byte("null")) {
		return page{}, errors.New("OpenAlex works page JSON requires a meta object")
	}
	var metaFields map[string]json.RawMessage
	if err := json.Unmarshal(metaPayload, &metaFields); err != nil {
		return page{}, fmt.Errorf("decode OpenAlex works page meta JSON: %w", err)
	}
	cursorPayload, ok := metaFields["next_cursor"]
	if !ok {
		return page{}, errors.New("OpenAlex works page JSON requires meta.next_cursor")
	}
	var nextCursor *string
	if !bytes.Equal(bytes.TrimSpace(cursorPayload), []byte("null")) {
		var cursor string
		if err := json.Unmarshal(cursorPayload, &cursor); err != nil {
			return page{}, fmt.Errorf("decode OpenAlex works page meta.next_cursor JSON: %w", err)
		}
		nextCursor = &cursor
	}

	return page{
		NextCursor: nextCursor,
		Results:    results,
	}, nil
}

func validateBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, errors.New("OpenAlex base URL must be an absolute http:// or https:// URL without query or fragment")
	}
	return parsed, nil
}
