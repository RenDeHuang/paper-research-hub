package pubmed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Tool             string
	Email            string
	APIKey           string
	UserAgent        string
	BatchSize        int
	Timeout          time.Duration
	MaxRetries       int
	MaxWait          time.Duration
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	MaxResponseBytes int64
}

type Client struct {
	baseURL    *url.URL
	tool       string
	email      string
	apiKey     string
	batchSize  int
	httpClient *httpclient.Client
}

type CoverageResult struct {
	Count          int64
	ResponseSHA256 string
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
	tool := strings.TrimSpace(config.Tool)
	if tool == "" {
		return nil, errors.New("PubMed NCBI tool is required")
	}
	email := strings.TrimSpace(config.Email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return nil, errors.New("PubMed NCBI email must be a valid email address")
	}
	if config.BatchSize < 1 || config.BatchSize > 10_000 {
		return nil, errors.New("PubMed batch size must be between 1 and 10000")
	}

	requestsPerSecond := 3
	if strings.TrimSpace(config.APIKey) != "" {
		requestsPerSecond = 10
	}
	policy, err := httpclient.New(base, httpclient.Config{
		Timeout:                  config.Timeout,
		UserAgent:                config.UserAgent,
		RateLimit:                httpclient.RateLimit{Requests: requestsPerSecond, Interval: time.Second},
		MaxRetries:               config.MaxRetries,
		MaxWait:                  config.MaxWait,
		InitialBackoff:           config.InitialBackoff,
		MaxBackoff:               config.MaxBackoff,
		MaxResponseBytes:         config.MaxResponseBytes,
		SensitiveQueryParameters: []string{"api_key", "email"},
	}, dependencies)
	if err != nil {
		return nil, fmt.Errorf("create PubMed HTTP policy: %w", err)
	}

	return &Client{
		baseURL:    baseURL,
		tool:       tool,
		email:      email,
		apiKey:     strings.TrimSpace(config.APIKey),
		batchSize:  config.BatchSize,
		httpClient: policy,
	}, nil
}

func (client *Client) Search(ctx context.Context, query SearchQuery) (SearchResult, error) {
	if client == nil {
		return SearchResult{}, errors.New("PubMed client is nil")
	}
	if ctx == nil {
		return SearchResult{}, errors.New("PubMed search context is required")
	}

	values, err := query.Values()
	if err != nil {
		return SearchResult{}, err
	}
	values.Set("db", "pubmed")
	values.Set("retmode", "json")
	values.Set("retmax", "0")
	values.Set("usehistory", "y")

	identity := make(url.Values)
	client.addIdentity(identity)
	response, err := client.postForm(ctx, "esearch.fcgi", identity, values)
	if err != nil {
		return SearchResult{}, fmt.Errorf("search PubMed history: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return SearchResult{}, fmt.Errorf("read PubMed ESearch response: %w", err)
	}

	result, err := decodeESearch(payload)
	if err != nil {
		return SearchResult{}, err
	}
	count64, err := parseESearchCount(result.Count)
	if err != nil {
		return SearchResult{}, err
	}
	maxInt := int64(^uint(0) >> 1)
	if count64 > maxInt {
		return SearchResult{}, fmt.Errorf("PubMed ESearch count %q exceeds int range", result.Count)
	}
	count := int(count64)
	if strings.TrimSpace(result.WebEnv) == "" ||
		strings.TrimSpace(result.QueryKey) == "" {
		return SearchResult{}, errors.New("PubMed ESearch response requires WebEnv and QueryKey")
	}
	batches, err := BuildBatches(count, query.MaxResults, client.batchSize)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{
		Count:    count,
		WebEnv:   result.WebEnv,
		QueryKey: result.QueryKey,
		Batches:  batches,
	}, nil
}

func (client *Client) CountCoverage(
	ctx context.Context,
	query CoverageQuery,
) (CoverageResult, error) {
	if client == nil {
		return CoverageResult{}, errors.New("PubMed client is nil")
	}
	if ctx == nil {
		return CoverageResult{}, errors.New("PubMed coverage context is required")
	}
	if err := ctx.Err(); err != nil {
		return CoverageResult{}, err
	}

	values, err := query.values()
	if err != nil {
		return CoverageResult{}, err
	}
	values.Set("db", "pubmed")
	values.Set("retmode", "json")
	values.Set("retmax", "0")
	client.addIdentity(values)

	response, err := client.get(ctx, "esearch.fcgi", values)
	if err != nil {
		return CoverageResult{}, fmt.Errorf("count PubMed coverage: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return CoverageResult{}, fmt.Errorf("read PubMed coverage ESearch response: %w", err)
	}
	responseSHA256 := sha256.Sum256(payload)

	result, err := decodeESearch(payload)
	if err != nil {
		return CoverageResult{}, err
	}
	count, err := parseESearchCount(result.Count)
	if err != nil {
		return CoverageResult{}, err
	}
	return CoverageResult{
		Count:          count,
		ResponseSHA256: hex.EncodeToString(responseSHA256[:]),
	}, nil
}

func (client *Client) Fetch(ctx context.Context, history SearchResult) source.ClientSequence {
	return func(yield func(source.Record, error) bool) {
		if client == nil {
			yield(source.Record{}, errors.New("PubMed client is nil"))
			return
		}
		if ctx == nil {
			yield(source.Record{}, errors.New("PubMed fetch context is required"))
			return
		}
		if err := validateHistory(history); err != nil {
			yield(source.Record{}, err)
			return
		}

		recordPosition := 0
		for batchIndex, batch := range history.Batches {
			if err := ctx.Err(); err != nil {
				yield(source.Record{}, err)
				return
			}
			values := make(url.Values)
			values.Set("db", "pubmed")
			values.Set("query_key", history.QueryKey)
			values.Set("WebEnv", history.WebEnv)
			values.Set("retstart", strconv.Itoa(batch.RetStart))
			values.Set("retmax", strconv.Itoa(batch.RetMax))
			values.Set("rettype", "pubmed")
			values.Set("retmode", "xml")
			client.addIdentity(values)

			response, err := client.get(ctx, "efetch.fcgi", values)
			if err != nil {
				yield(
					source.Record{},
					fmt.Errorf("fetch PubMed history batch %d: %w", batchIndex+1, err),
				)
				return
			}
			payload, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if readErr != nil {
				yield(source.Record{}, fmt.Errorf("read PubMed EFetch response: %w", readErr))
				return
			}
			if closeErr != nil {
				yield(source.Record{}, fmt.Errorf("close PubMed EFetch response: %w", closeErr))
				return
			}

			records, err := Parse(payload)
			if err != nil {
				yield(
					source.Record{},
					fmt.Errorf("parse PubMed history batch %d after record %d: %w", batchIndex+1, recordPosition, err),
				)
				return
			}
			if len(records) != batch.RetMax {
				yield(
					source.Record{},
					fmt.Errorf(
						"PubMed history batch %d returned %d records, want %d",
						batchIndex+1,
						len(records),
						batch.RetMax,
					),
				)
				return
			}
			for _, record := range records {
				recordPosition++
				if !yield(record, nil) {
					return
				}
			}
		}
	}
}

func (client *Client) get(
	ctx context.Context,
	operation string,
	values url.Values,
) (*http.Response, error) {
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/entrez/eutils/" + operation
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create PubMed %s request: %w", operation, err)
	}
	return client.httpClient.Do(request)
}

func (client *Client) postForm(
	ctx context.Context,
	operation string,
	query url.Values,
	form url.Values,
) (*http.Response, error) {
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/entrez/eutils/" + operation
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint.String(),
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("create PubMed %s request: %w", operation, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return client.httpClient.Do(request)
}

func (client *Client) addIdentity(values url.Values) {
	values.Set("tool", client.tool)
	values.Set("email", client.email)
	if client.apiKey != "" {
		values.Set("api_key", client.apiKey)
	}
}

func validateHistory(history SearchResult) error {
	if history.Count < 0 {
		return errors.New("PubMed history count must not be negative")
	}
	if strings.TrimSpace(history.WebEnv) == "" || strings.TrimSpace(history.QueryKey) == "" {
		return errors.New("PubMed history requires WebEnv and QueryKey")
	}
	nextStart := 0
	total := 0
	for index, batch := range history.Batches {
		if batch.RetStart != nextStart || batch.RetMax <= 0 {
			return fmt.Errorf("PubMed history batch %d is not a stable ordered batch", index+1)
		}
		nextStart += batch.RetMax
		total += batch.RetMax
	}
	if total > history.Count {
		return fmt.Errorf("PubMed history batches request %d records beyond count %d", total, history.Count)
	}
	return nil
}

func validateBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, errors.New("PubMed base URL must be an absolute http:// or https:// URL without query or fragment")
	}
	return parsed, nil
}
