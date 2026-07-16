package crossref

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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
)

const maxBatchSize = 1000

type Config struct {
	BaseURL          string
	ContactEmail     string
	UserAgent        string
	BatchSize        int
	Timeout          time.Duration
	RateLimit        httpclient.RateLimit
	MaxRetries       int
	MaxWait          time.Duration
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	MaxResponseBytes int64
}

type DateWindow struct {
	From time.Time
	To   time.Time
}

type Query struct {
	DOIs              []string
	ISSNs             []string
	IndexedDateWindow DateWindow
	MaxResults        int
}

type Client struct {
	baseURL      *url.URL
	contactEmail string
	batchSize    int
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
	contactEmail := strings.TrimSpace(config.ContactEmail)
	address, err := mail.ParseAddress(contactEmail)
	if err != nil || address.Address != contactEmail {
		return nil, errors.New("Crossref contact email must be a valid bare email address")
	}
	if config.BatchSize < 1 || config.BatchSize > maxBatchSize {
		return nil, fmt.Errorf("Crossref batch size must be between 1 and %d", maxBatchSize)
	}

	policy, err := httpclient.New(base, httpclient.Config{
		Timeout:          config.Timeout,
		UserAgent:        config.UserAgent,
		RateLimit:        config.RateLimit,
		MaxRetries:       config.MaxRetries,
		MaxWait:          config.MaxWait,
		InitialBackoff:   config.InitialBackoff,
		MaxBackoff:       config.MaxBackoff,
		MaxResponseBytes: config.MaxResponseBytes,
	}, dependencies)
	if err != nil {
		return nil, fmt.Errorf("create Crossref HTTP policy: %w", err)
	}

	return &Client{
		baseURL:      baseURL,
		contactEmail: contactEmail,
		batchSize:    config.BatchSize,
		httpClient:   policy,
	}, nil
}

func (client *Client) Fetch(ctx context.Context, query Query) source.ClientSequence {
	return func(yield func(source.Record, error) bool) {
		if client == nil {
			yield(source.Record{}, errors.New("Crossref client is nil"))
			return
		}
		if ctx == nil {
			yield(source.Record{}, errors.New("Crossref fetch context is required"))
			return
		}

		filter, err := query.filter()
		if err != nil {
			yield(source.Record{}, err)
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

			rows := min(client.batchSize, query.MaxResults-emitted)
			request, err := http.NewRequestWithContext(
				ctx,
				http.MethodGet,
				client.worksURL(filter, cursor, rows),
				nil,
			)
			if err != nil {
				yield(source.Record{}, fmt.Errorf("create Crossref works request: %w", err))
				return
			}
			response, err := client.httpClient.Do(request)
			if err != nil {
				yield(source.Record{}, fmt.Errorf("fetch Crossref works page: %w", err))
				return
			}
			payload, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if readErr != nil {
				yield(source.Record{}, fmt.Errorf("read Crossref works response: %w", readErr))
				return
			}
			if closeErr != nil {
				yield(source.Record{}, fmt.Errorf("close Crossref works response: %w", closeErr))
				return
			}

			page, err := decodePage(payload)
			if err != nil {
				yield(source.Record{}, err)
				return
			}
			if len(page.items) > rows {
				yield(
					source.Record{},
					fmt.Errorf(
						"Crossref protocol error: page returned %d items for requested rows=%d",
						len(page.items),
						rows,
					),
				)
				return
			}
			pageStart := emitted
			for index, raw := range page.items {
				if emitted >= query.MaxResults {
					return
				}
				record, err := Parse(raw)
				if err != nil {
					yield(
						source.Record{},
						fmt.Errorf("parse Crossref item %d: %w", pageStart+index+1, err),
					)
					return
				}
				emitted++
				if !yield(record, nil) {
					return
				}
			}
			if emitted >= query.MaxResults {
				return
			}
			if len(page.items) < rows {
				return
			}

			nextCursor, err := page.requiredNextCursor()
			if err != nil {
				yield(source.Record{}, err)
				return
			}
			if _, exists := seenCursors[nextCursor]; exists {
				yield(
					source.Record{},
					fmt.Errorf("Crossref protocol error: duplicate next-cursor %q", nextCursor),
				)
				return
			}
			seenCursors[nextCursor] = struct{}{}
			cursor = nextCursor
		}
	}
}

func (client *Client) worksURL(filter, cursor string, rows int) string {
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/works"
	values := endpoint.Query()
	values.Set("filter", filter)
	values.Set("cursor", cursor)
	values.Set("rows", strconv.Itoa(rows))
	values.Set("mailto", client.contactEmail)
	endpoint.RawQuery = values.Encode()
	return endpoint.String()
}

func (query Query) filter() (string, error) {
	if query.MaxResults <= 0 {
		return "", errors.New("Crossref max results must be positive")
	}

	dois, err := normalizeDOIs(query.DOIs)
	if err != nil {
		return "", err
	}
	issns, err := normalizeISSNs(query.ISSNs)
	if err != nil {
		return "", err
	}

	hasFrom := !query.IndexedDateWindow.From.IsZero()
	hasTo := !query.IndexedDateWindow.To.IsZero()
	if hasFrom != hasTo {
		return "", errors.New("Crossref indexed date window requires both from and to dates")
	}

	tokens := make([]string, 0, len(dois)+len(issns)+2)
	for _, doi := range dois {
		tokens = append(tokens, "doi:"+doi)
	}
	for _, issn := range issns {
		tokens = append(tokens, "issn:"+issn)
	}
	if hasFrom {
		from := dateOnly(query.IndexedDateWindow.From)
		to := dateOnly(query.IndexedDateWindow.To)
		if from.After(to) {
			return "", errors.New("Crossref indexed date window from date must not follow to date")
		}
		tokens = append(
			tokens,
			"from-index-date:"+from.Format(time.DateOnly),
			"until-index-date:"+to.Format(time.DateOnly),
		)
	}
	if len(tokens) == 0 {
		return "", errors.New("Crossref query must be bounded by DOI, ISSN, or a complete indexed date window")
	}
	return strings.Join(tokens, ","), nil
}

func normalizeDOIs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, raw := range values {
		identifier, err := paper.NewIdentifier(paper.SchemeDOI, raw)
		if err != nil {
			return nil, fmt.Errorf("invalid Crossref DOI at position %d: %w", index+1, err)
		}
		if _, exists := seen[identifier.Value()]; exists {
			continue
		}
		seen[identifier.Value()] = struct{}{}
		result = append(result, identifier.Value())
	}
	sort.Strings(result)
	return result, nil
}

func normalizeISSNs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, raw := range values {
		value := strings.ToUpper(strings.TrimSpace(raw))
		if !validISSN(value) {
			return nil, fmt.Errorf("invalid Crossref ISSN at position %d: %q", index+1, raw)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func validISSN(value string) bool {
	if len(value) != 9 || value[4] != '-' {
		return false
	}
	sum := 0
	digitIndex := 0
	for index, character := range value {
		if index == 4 {
			continue
		}
		digit := 0
		switch {
		case character >= '0' && character <= '9':
			digit = int(character - '0')
		case character == 'X' && index == len(value)-1:
			digit = 10
		default:
			return false
		}
		sum += digit * (8 - digitIndex)
		digitIndex++
	}
	return digitIndex == 8 && sum%11 == 0
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

type page struct {
	items         []json.RawMessage
	nextCursor    json.RawMessage
	cursorPresent bool
}

func decodePage(payload []byte) (page, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var envelope map[string]json.RawMessage
	if err := decoder.Decode(&envelope); err != nil {
		return page{}, fmt.Errorf("Crossref works page JSON must be an object: %w", err)
	}
	if envelope == nil {
		return page{}, errors.New("Crossref works page JSON must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return page{}, errors.New("Crossref works page JSON must contain exactly one object")
		}
		return page{}, fmt.Errorf("decode trailing Crossref works page JSON: %w", err)
	}

	status, err := requiredString(envelope, "status")
	if err != nil {
		return page{}, err
	}
	if status != "ok" {
		return page{}, fmt.Errorf("Crossref works page status = %q, want ok", status)
	}
	messageType, err := requiredString(envelope, "message-type")
	if err != nil {
		return page{}, err
	}
	if messageType != "work-list" {
		return page{}, fmt.Errorf(
			"Crossref works page message-type = %q, want work-list",
			messageType,
		)
	}

	messagePayload, exists := envelope["message"]
	if !exists || bytes.Equal(bytes.TrimSpace(messagePayload), []byte("null")) {
		return page{}, errors.New("Crossref works page requires a message object")
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(messagePayload, &message); err != nil || message == nil {
		if err == nil {
			err = errors.New("not an object")
		}
		return page{}, fmt.Errorf("Crossref works page requires a message object: %w", err)
	}

	itemsPayload, exists := message["items"]
	if !exists || bytes.Equal(bytes.TrimSpace(itemsPayload), []byte("null")) {
		return page{}, errors.New("Crossref works page message requires a non-null items array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(itemsPayload, &items); err != nil || items == nil {
		if err == nil {
			err = errors.New("null array")
		}
		return page{}, fmt.Errorf(
			"Crossref works page message requires a non-null items array: %w",
			err,
		)
	}

	nextCursor, cursorPresent := message["next-cursor"]
	return page{
		items:         items,
		nextCursor:    nextCursor,
		cursorPresent: cursorPresent,
	}, nil
}

func requiredString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, exists := fields[name]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("Crossref works page requires %s", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("Crossref works page requires string %s: %w", name, err)
	}
	return value, nil
}

func (page page) requiredNextCursor() (string, error) {
	if !page.cursorPresent {
		return "", errors.New("Crossref protocol error: missing next-cursor on full page")
	}
	var cursor string
	if err := json.Unmarshal(page.nextCursor, &cursor); err != nil {
		return "", fmt.Errorf("Crossref protocol error: invalid next-cursor: %w", err)
	}
	if strings.TrimSpace(cursor) == "" {
		return "", errors.New("Crossref protocol error: blank next-cursor on full page")
	}
	return cursor, nil
}

func validateBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, errors.New(
			"Crossref base URL must be an absolute http:// or https:// URL without credentials, query, or fragment",
		)
	}
	return parsed, nil
}
