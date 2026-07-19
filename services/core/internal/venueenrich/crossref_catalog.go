package venueenrich

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
)

const (
	CrossrefCatalogRows          = 1000
	CrossrefCatalogSchemaVersion = "crossref-journal-catalog/v1"
	CrossrefCatalogJSONLName     = "crossref-journals.v1.jsonl"
	CrossrefCatalogManifestName  = "crossref-journals.v1.manifest.json"

	maxCrossrefCatalogManifestBytes = 8 << 20
)

type CrossrefCatalogConfig struct {
	BaseURL          string
	CacheDir         string
	ContactEmail     string
	UserAgent        string
	Timeout          time.Duration
	RateLimit        httpclient.RateLimit
	MaxRetries       int
	MaxWait          time.Duration
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	MaxResponseBytes int64
}

type CrossrefJournalISSNType struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

type CrossrefJournal struct {
	Title          string
	ISSNs          []string
	ISSNTypes      []CrossrefJournalISSNType
	PrintISSN      string
	ElectronicISSN string
	Publisher      string
	TotalDOIs      int64
	Raw            json.RawMessage
}

type CrossrefCatalogPageReceipt struct {
	Ordinal     int    `json:"ordinal"`
	CursorIn    string `json:"cursor_in"`
	CursorOut   string `json:"cursor_out"`
	RecordCount int    `json:"record_count"`
	PageSHA256  string `json:"page_sha256"`
}

type CrossrefCatalogManifest struct {
	SchemaVersion string                       `json:"schema_version"`
	SourceURL     string                       `json:"source_url"`
	FetchedAt     time.Time                    `json:"fetched_at"`
	Rows          int                          `json:"rows"`
	Pages         []CrossrefCatalogPageReceipt `json:"pages"`
	CatalogSHA256 string                       `json:"catalog_sha256"`
	CatalogBytes  int64                        `json:"catalog_bytes"`
	RecordCount   int64                        `json:"record_count"`
	TotalResults  *int64                       `json:"total_results"`
	Complete      bool                         `json:"complete"`
}

type CrossrefCatalog struct {
	Journals     []CrossrefJournal
	Manifest     CrossrefCatalogManifest
	CatalogPath  string
	ManifestPath string
	Replayed     bool
	Resumed      bool
}

type crossrefCatalogCache struct {
	manifest CrossrefCatalogManifest
	journals []CrossrefJournal
	hasher   hash.Hash
	exists   bool
}

type crossrefJournalPage struct {
	totalResults int64
	nextCursor   string
	journals     []CrossrefJournal
}

func FetchCrossrefCatalog(
	ctx context.Context,
	base *http.Client,
	config CrossrefCatalogConfig,
	dependencies httpclient.Dependencies,
) (CrossrefCatalog, error) {
	if ctx == nil {
		return CrossrefCatalog{}, errors.New("Crossref catalog context is required")
	}

	endpoint, contactEmail, err := validateCrossrefCatalogConfig(config)
	if err != nil {
		return CrossrefCatalog{}, err
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
		SensitiveQueryParameters: []string{"mailto"},
	}, dependencies)
	if err != nil {
		return CrossrefCatalog{}, fmt.Errorf(
			"create Crossref catalog bounded HTTP policy: %w",
			err,
		)
	}
	if err := os.MkdirAll(config.CacheDir, 0o700); err != nil {
		return CrossrefCatalog{}, fmt.Errorf(
			"create Crossref catalog cache directory: %w",
			err,
		)
	}

	catalogPath := filepath.Join(config.CacheDir, CrossrefCatalogJSONLName)
	manifestPath := filepath.Join(config.CacheDir, CrossrefCatalogManifestName)
	cache, err := loadCrossrefCatalogCache(
		catalogPath,
		manifestPath,
		endpoint.String(),
		config.MaxResponseBytes,
	)
	if err != nil {
		return CrossrefCatalog{}, err
	}
	if cache.exists && cache.manifest.Complete {
		return crossrefCatalogResult(
			cache,
			catalogPath,
			manifestPath,
			true,
			false,
		), nil
	}

	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	resumed := cache.exists
	if !cache.exists {
		cache, err = initializeCrossrefCatalogCache(
			catalogPath,
			manifestPath,
			endpoint.String(),
			now().UTC(),
		)
		if err != nil {
			return CrossrefCatalog{}, err
		}
	}
	if cache.manifest.TotalResults != nil &&
		cache.manifest.RecordCount == *cache.manifest.TotalResults {
		cache.manifest.Complete = true
		if err := writeCrossrefCatalogManifestAtomic(
			manifestPath,
			cache.manifest,
		); err != nil {
			return CrossrefCatalog{}, err
		}
		return crossrefCatalogResult(
			cache,
			catalogPath,
			manifestPath,
			false,
			resumed,
		), nil
	}

	cursor, seenCursors, err := crossrefCatalogResumeCursor(cache.manifest)
	if err != nil {
		return CrossrefCatalog{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return CrossrefCatalog{}, err
		}

		requestURL := crossrefCatalogPageURL(
			endpoint,
			cursor,
			contactEmail,
		)
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			requestURL,
			nil,
		)
		if err != nil {
			return CrossrefCatalog{}, fmt.Errorf(
				"create Crossref journals request: %w",
				err,
			)
		}
		response, err := policy.Do(request)
		if err != nil {
			return CrossrefCatalog{}, fmt.Errorf(
				"fetch Crossref journals page: %w",
				err,
			)
		}
		payload, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil {
			return CrossrefCatalog{}, fmt.Errorf(
				"read Crossref journals response: %w",
				readErr,
			)
		}
		if closeErr != nil {
			return CrossrefCatalog{}, fmt.Errorf(
				"close Crossref journals response: %w",
				closeErr,
			)
		}

		page, err := decodeCrossrefJournalPage(payload)
		if err != nil {
			return CrossrefCatalog{}, err
		}
		if len(page.journals) > CrossrefCatalogRows {
			return CrossrefCatalog{}, fmt.Errorf(
				"Crossref journal catalog protocol error: page returned %d items for rows=%d",
				len(page.journals),
				CrossrefCatalogRows,
			)
		}
		if cache.manifest.TotalResults == nil {
			total := page.totalResults
			cache.manifest.TotalResults = &total
		} else if page.totalResults != *cache.manifest.TotalResults {
			return CrossrefCatalog{}, fmt.Errorf(
				"Crossref journal catalog total-results changed from %d to %d",
				*cache.manifest.TotalResults,
				page.totalResults,
			)
		}

		nextRecordCount := cache.manifest.RecordCount + int64(len(page.journals))
		if len(page.journals) == 0 {
			if page.nextCursor != "" {
				return CrossrefCatalog{}, errors.New(
					"Crossref journal catalog protocol error: empty page has a continuation cursor",
				)
			}
			if nextRecordCount != *cache.manifest.TotalResults {
				return CrossrefCatalog{}, fmt.Errorf(
					"Crossref journal catalog early EOF at %d of %d records",
					cache.manifest.RecordCount,
					*cache.manifest.TotalResults,
				)
			}
		}
		if nextRecordCount > *cache.manifest.TotalResults {
			return CrossrefCatalog{}, fmt.Errorf(
				"Crossref journal catalog page exceeds total-results: %d records would exceed %d",
				nextRecordCount,
				*cache.manifest.TotalResults,
			)
		}
		if page.nextCursor != "" {
			if _, duplicate := seenCursors[page.nextCursor]; duplicate {
				return CrossrefCatalog{}, fmt.Errorf(
					"Crossref journal catalog protocol error: duplicate cursor %q",
					page.nextCursor,
				)
			}
		}
		if nextRecordCount < *cache.manifest.TotalResults &&
			page.nextCursor == "" {
			return CrossrefCatalog{}, fmt.Errorf(
				"Crossref journal catalog early EOF at %d of %d records: missing next-cursor",
				nextRecordCount,
				*cache.manifest.TotalResults,
			)
		}

		pageJSONL := crossrefCatalogPageJSONL(page.journals)
		if err := appendCrossrefCatalogPage(catalogPath, pageJSONL); err != nil {
			return CrossrefCatalog{}, err
		}
		if _, err := cache.hasher.Write(pageJSONL); err != nil {
			return CrossrefCatalog{}, fmt.Errorf(
				"hash Crossref journal catalog page: %w",
				err,
			)
		}
		pageHash := sha256.Sum256(payload)
		cache.manifest.Pages = append(
			cache.manifest.Pages,
			CrossrefCatalogPageReceipt{
				Ordinal:     len(cache.manifest.Pages) + 1,
				CursorIn:    cursor,
				CursorOut:   page.nextCursor,
				RecordCount: len(page.journals),
				PageSHA256:  hex.EncodeToString(pageHash[:]),
			},
		)
		cache.manifest.RecordCount = nextRecordCount
		cache.manifest.CatalogBytes += int64(len(pageJSONL))
		cache.manifest.CatalogSHA256 = hex.EncodeToString(cache.hasher.Sum(nil))
		cache.journals = append(cache.journals, page.journals...)
		if err := writeCrossrefCatalogManifestAtomic(
			manifestPath,
			cache.manifest,
		); err != nil {
			return CrossrefCatalog{}, err
		}

		if nextRecordCount == *cache.manifest.TotalResults {
			cache.manifest.Complete = true
			if err := writeCrossrefCatalogManifestAtomic(
				manifestPath,
				cache.manifest,
			); err != nil {
				return CrossrefCatalog{}, err
			}
			return crossrefCatalogResult(
				cache,
				catalogPath,
				manifestPath,
				false,
				resumed,
			), nil
		}

		seenCursors[page.nextCursor] = struct{}{}
		cursor = page.nextCursor
	}
}

func crossrefCatalogResult(
	cache crossrefCatalogCache,
	catalogPath string,
	manifestPath string,
	replayed bool,
	resumed bool,
) CrossrefCatalog {
	return CrossrefCatalog{
		Journals:     cache.journals,
		Manifest:     cache.manifest,
		CatalogPath:  catalogPath,
		ManifestPath: manifestPath,
		Replayed:     replayed,
		Resumed:      resumed,
	}
}

func validateCrossrefCatalogConfig(
	config CrossrefCatalogConfig,
) (*url.URL, string, error) {
	parsed, err := url.Parse(config.BaseURL)
	if err != nil ||
		parsed == nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, "", errors.New(
			"Crossref catalog base URL must be an absolute HTTP(S) URL without credentials, query, or fragment",
		)
	}
	if strings.TrimSpace(config.CacheDir) == "" ||
		config.CacheDir != strings.TrimSpace(config.CacheDir) {
		return nil, "", errors.New(
			"Crossref catalog cache directory must be explicit and trimmed",
		)
	}

	contactEmail := strings.TrimSpace(config.ContactEmail)
	if contactEmail != "" {
		address, parseErr := mail.ParseAddress(contactEmail)
		if parseErr != nil || address.Address != contactEmail {
			return nil, "", errors.New(
				"Crossref catalog contact email must be empty or a valid bare email address",
			)
		}
	}

	endpoint := *parsed
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/journals"
	return &endpoint, contactEmail, nil
}

func crossrefCatalogPageURL(
	endpoint *url.URL,
	cursor string,
	contactEmail string,
) string {
	pageURL := *endpoint
	query := pageURL.Query()
	query.Set("rows", strconv.Itoa(CrossrefCatalogRows))
	query.Set("cursor", cursor)
	if contactEmail != "" {
		query.Set("mailto", contactEmail)
	}
	pageURL.RawQuery = query.Encode()
	return pageURL.String()
}

func initializeCrossrefCatalogCache(
	catalogPath string,
	manifestPath string,
	sourceURL string,
	fetchedAt time.Time,
) (crossrefCatalogCache, error) {
	if fetchedAt.IsZero() {
		return crossrefCatalogCache{}, errors.New(
			"Crossref catalog fetched_at must not be zero",
		)
	}
	if err := writeFileAtomic(catalogPath, nil); err != nil {
		return crossrefCatalogCache{}, fmt.Errorf(
			"initialize Crossref catalog JSONL: %w",
			err,
		)
	}

	emptyHash := sha256.Sum256(nil)
	manifest := CrossrefCatalogManifest{
		SchemaVersion: CrossrefCatalogSchemaVersion,
		SourceURL:     sourceURL,
		FetchedAt:     fetchedAt.UTC(),
		Rows:          CrossrefCatalogRows,
		Pages:         []CrossrefCatalogPageReceipt{},
		CatalogSHA256: hex.EncodeToString(emptyHash[:]),
		CatalogBytes:  0,
		RecordCount:   0,
		TotalResults:  nil,
		Complete:      false,
	}
	if err := writeCrossrefCatalogManifestAtomic(manifestPath, manifest); err != nil {
		_ = os.Remove(catalogPath)
		return crossrefCatalogCache{}, err
	}
	return crossrefCatalogCache{
		manifest: manifest,
		journals: []CrossrefJournal{},
		hasher:   sha256.New(),
		exists:   true,
	}, nil
}

func loadCrossrefCatalogCache(
	catalogPath string,
	manifestPath string,
	sourceURL string,
	maxRecordBytes int64,
) (crossrefCatalogCache, error) {
	catalogExists, err := fileExists(catalogPath)
	if err != nil {
		return crossrefCatalogCache{}, err
	}
	manifestExists, err := fileExists(manifestPath)
	if err != nil {
		return crossrefCatalogCache{}, err
	}
	if !catalogExists && !manifestExists {
		return crossrefCatalogCache{}, nil
	}
	if catalogExists != manifestExists {
		return crossrefCatalogCache{}, errors.New(
			"Crossref catalog cache is unverified: catalog JSONL and manifest must both exist",
		)
	}

	manifest, err := loadCrossrefCatalogManifestFile(manifestPath)
	if err != nil {
		return crossrefCatalogCache{}, err
	}
	if err := validateCrossrefCatalogManifest(manifest, sourceURL); err != nil {
		return crossrefCatalogCache{}, err
	}
	journals, hasher, err := readAndVerifyCrossrefCatalogJSONL(
		catalogPath,
		manifest,
		maxRecordBytes,
	)
	if err != nil {
		return crossrefCatalogCache{}, err
	}
	return crossrefCatalogCache{
		manifest: manifest,
		journals: journals,
		hasher:   hasher,
		exists:   true,
	}, nil
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("inspect Crossref catalog cache file %s: %w", path, err)
	}
}

func loadCrossrefCatalogManifestFile(
	path string,
) (CrossrefCatalogManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return CrossrefCatalogManifest{}, fmt.Errorf(
			"open Crossref catalog manifest: %w",
			err,
		)
	}
	defer file.Close()

	payload, err := io.ReadAll(io.LimitReader(
		file,
		maxCrossrefCatalogManifestBytes+1,
	))
	if err != nil {
		return CrossrefCatalogManifest{}, fmt.Errorf(
			"read Crossref catalog manifest: %w",
			err,
		)
	}
	if len(payload) > maxCrossrefCatalogManifestBytes {
		return CrossrefCatalogManifest{}, errors.New(
			"Crossref catalog manifest exceeds size limit",
		)
	}
	if err := validateSingleUniqueJSONValue(
		payload,
		"Crossref catalog manifest",
	); err != nil {
		return CrossrefCatalogManifest{}, fmt.Errorf(
			"validate Crossref catalog manifest JSON: %w",
			err,
		)
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var manifest CrossrefCatalogManifest
	if err := decoder.Decode(&manifest); err != nil {
		return CrossrefCatalogManifest{}, fmt.Errorf(
			"decode Crossref catalog manifest: %w",
			err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return CrossrefCatalogManifest{}, errors.New(
				"Crossref catalog manifest must contain exactly one object",
			)
		}
		return CrossrefCatalogManifest{}, fmt.Errorf(
			"decode trailing Crossref catalog manifest JSON: %w",
			err,
		)
	}
	return manifest, nil
}

func validateCrossrefCatalogManifest(
	manifest CrossrefCatalogManifest,
	sourceURL string,
) error {
	if manifest.SchemaVersion != CrossrefCatalogSchemaVersion {
		return fmt.Errorf(
			"Crossref catalog manifest schema_version = %q, want %q",
			manifest.SchemaVersion,
			CrossrefCatalogSchemaVersion,
		)
	}
	if manifest.SourceURL != sourceURL {
		return fmt.Errorf(
			"Crossref catalog manifest source_url = %q, want %q",
			manifest.SourceURL,
			sourceURL,
		)
	}
	if manifest.FetchedAt.IsZero() {
		return errors.New(
			"Crossref catalog manifest fetched_at must not be zero",
		)
	}
	if _, offset := manifest.FetchedAt.Zone(); offset != 0 {
		return errors.New(
			"Crossref catalog manifest fetched_at must use UTC",
		)
	}
	if manifest.Rows != CrossrefCatalogRows {
		return fmt.Errorf(
			"Crossref catalog manifest rows = %d, want %d",
			manifest.Rows,
			CrossrefCatalogRows,
		)
	}
	if manifest.CatalogBytes < 0 || manifest.RecordCount < 0 {
		return errors.New(
			"Crossref catalog manifest byte and record counts must not be negative",
		)
	}
	if err := validateLowerSHA256(
		manifest.CatalogSHA256,
		"Crossref catalog manifest catalog_sha256",
	); err != nil {
		return err
	}

	var receiptRecords int64
	expectedCursor := "*"
	seenCursors := map[string]struct{}{expectedCursor: {}}
	for index, receipt := range manifest.Pages {
		if receipt.Ordinal != index+1 {
			return fmt.Errorf(
				"Crossref catalog manifest page %d ordinal = %d",
				index+1,
				receipt.Ordinal,
			)
		}
		if receipt.CursorIn != expectedCursor {
			return fmt.Errorf(
				"Crossref catalog manifest page %d cursor_in = %q, want %q",
				index+1,
				receipt.CursorIn,
				expectedCursor,
			)
		}
		if receipt.RecordCount < 0 ||
			receipt.RecordCount > CrossrefCatalogRows {
			return fmt.Errorf(
				"Crossref catalog manifest page %d record_count = %d",
				index+1,
				receipt.RecordCount,
			)
		}
		if err := validateLowerSHA256(
			receipt.PageSHA256,
			fmt.Sprintf(
				"Crossref catalog manifest page %d page_sha256",
				index+1,
			),
		); err != nil {
			return err
		}
		receiptRecords += int64(receipt.RecordCount)
		if receipt.CursorOut != "" {
			if _, duplicate := seenCursors[receipt.CursorOut]; duplicate {
				return fmt.Errorf(
					"Crossref catalog manifest contains duplicate cursor %q",
					receipt.CursorOut,
				)
			}
			seenCursors[receipt.CursorOut] = struct{}{}
		}
		expectedCursor = receipt.CursorOut
	}
	if receiptRecords != manifest.RecordCount {
		return fmt.Errorf(
			"Crossref catalog manifest page record sum = %d, want record_count %d",
			receiptRecords,
			manifest.RecordCount,
		)
	}

	if len(manifest.Pages) == 0 {
		if manifest.RecordCount != 0 ||
			manifest.CatalogBytes != 0 ||
			manifest.TotalResults != nil ||
			manifest.Complete {
			return errors.New(
				"Crossref catalog manifest without pages must be an empty incomplete checkpoint",
			)
		}
		return nil
	}
	if manifest.TotalResults == nil || *manifest.TotalResults < 0 {
		return errors.New(
			"Crossref catalog manifest with pages requires non-negative total_results",
		)
	}
	if manifest.RecordCount > *manifest.TotalResults {
		return errors.New(
			"Crossref catalog manifest record_count exceeds total_results",
		)
	}
	if manifest.Complete {
		if manifest.RecordCount != *manifest.TotalResults {
			return errors.New(
				"complete Crossref catalog manifest requires record_count equal total_results",
			)
		}
		return nil
	}
	if manifest.RecordCount < *manifest.TotalResults &&
		manifest.Pages[len(manifest.Pages)-1].CursorOut == "" {
		return errors.New(
			"incomplete Crossref catalog manifest requires a continuation cursor",
		)
	}
	return nil
}

func validateLowerSHA256(value string, field string) error {
	decoded, err := hex.DecodeString(value)
	if err != nil ||
		len(decoded) != sha256.Size ||
		value != strings.ToLower(value) {
		return fmt.Errorf("%s must be a lowercase SHA-256 hex digest", field)
	}
	return nil
}

func readAndVerifyCrossrefCatalogJSONL(
	path string,
	manifest CrossrefCatalogManifest,
	maxRecordBytes int64,
) ([]CrossrefJournal, hash.Hash, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"open Crossref catalog JSONL: %w",
			err,
		)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf(
			"stat Crossref catalog JSONL: %w",
			err,
		)
	}
	if info.Size() != manifest.CatalogBytes {
		return nil, nil, fmt.Errorf(
			"Crossref catalog hash/size verification failed: JSONL bytes = %d, manifest = %d",
			info.Size(),
			manifest.CatalogBytes,
		)
	}
	if info.Size() > 0 {
		if _, err := file.Seek(-1, io.SeekEnd); err != nil {
			return nil, nil, fmt.Errorf(
				"seek Crossref catalog JSONL trailer: %w",
				err,
			)
		}
		var trailer [1]byte
		if _, err := io.ReadFull(file, trailer[:]); err != nil {
			return nil, nil, fmt.Errorf(
				"read Crossref catalog JSONL trailer: %w",
				err,
			)
		}
		if trailer[0] != '\n' {
			return nil, nil, errors.New(
				"Crossref catalog JSONL is truncated: final record lacks newline",
			)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, nil, fmt.Errorf(
				"rewind Crossref catalog JSONL: %w",
				err,
			)
		}
	}

	hasher := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(file, hasher))
	maxToken := maxScannerToken(maxRecordBytes)
	scanner.Buffer(make([]byte, 64*1024), maxToken)
	journals := make([]CrossrefJournal, 0, manifest.RecordCount)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, nil, fmt.Errorf(
				"Crossref catalog JSONL record %d is blank",
				len(journals)+1,
			)
		}
		journal, err := decodeCrossrefJournal(line)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"validate cached Crossref journal %d: %w",
				len(journals)+1,
				err,
			)
		}
		journals = append(journals, journal)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf(
			"read Crossref catalog JSONL: %w",
			err,
		)
	}
	if int64(len(journals)) != manifest.RecordCount {
		return nil, nil, fmt.Errorf(
			"Crossref catalog JSONL record count = %d, manifest = %d",
			len(journals),
			manifest.RecordCount,
		)
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != manifest.CatalogSHA256 {
		return nil, nil, fmt.Errorf(
			"Crossref catalog hash verification failed: JSONL SHA-256 = %s, manifest = %s",
			actualHash,
			manifest.CatalogSHA256,
		)
	}
	return journals, hasher, nil
}

func maxScannerToken(maxRecordBytes int64) int {
	maxInt := int(^uint(0) >> 1)
	if maxRecordBytes >= int64(maxInt) {
		return maxInt
	}
	return int(maxRecordBytes) + 1
}

func crossrefCatalogResumeCursor(
	manifest CrossrefCatalogManifest,
) (string, map[string]struct{}, error) {
	cursor := "*"
	seen := map[string]struct{}{cursor: {}}
	for _, receipt := range manifest.Pages {
		if receipt.CursorIn != cursor {
			return "", nil, fmt.Errorf(
				"Crossref catalog resume cursor chain breaks at page %d",
				receipt.Ordinal,
			)
		}
		if receipt.CursorOut != "" {
			if _, duplicate := seen[receipt.CursorOut]; duplicate {
				return "", nil, fmt.Errorf(
					"Crossref catalog resume contains duplicate cursor %q",
					receipt.CursorOut,
				)
			}
			seen[receipt.CursorOut] = struct{}{}
		}
		cursor = receipt.CursorOut
	}
	if manifest.RecordCount > 0 &&
		manifest.TotalResults != nil &&
		manifest.RecordCount < *manifest.TotalResults &&
		cursor == "" {
		return "", nil, errors.New(
			"Crossref catalog resume requires a verified continuation cursor",
		)
	}
	return cursor, seen, nil
}

func decodeCrossrefJournalPage(
	payload []byte,
) (crossrefJournalPage, error) {
	if err := validateSingleUniqueJSONValue(
		payload,
		"Crossref journal page",
	); err != nil {
		return crossrefJournalPage{}, fmt.Errorf(
			"validate Crossref journal page JSON: %w",
			err,
		)
	}

	envelope, err := decodeJSONObject(payload, "Crossref journal page envelope")
	if err != nil {
		return crossrefJournalPage{}, err
	}
	if err := rejectUnexpectedJSONFields(
		envelope,
		"Crossref journal page envelope",
		"status",
		"message-type",
		"message-version",
		"message",
	); err != nil {
		return crossrefJournalPage{}, err
	}
	status, err := requiredJSONString(
		envelope,
		"status",
		"Crossref journal page",
	)
	if err != nil {
		return crossrefJournalPage{}, err
	}
	if status != "ok" {
		return crossrefJournalPage{}, fmt.Errorf(
			"Crossref journal page status = %q, want ok",
			status,
		)
	}
	messageType, err := requiredJSONString(
		envelope,
		"message-type",
		"Crossref journal page",
	)
	if err != nil {
		return crossrefJournalPage{}, err
	}
	if messageType != "journal-list" {
		return crossrefJournalPage{}, fmt.Errorf(
			"Crossref journal page message-type = %q, want journal-list",
			messageType,
		)
	}
	if rawVersion, exists := envelope["message-version"]; exists {
		version, err := decodeJSONString(
			rawVersion,
			"Crossref journal page message-version",
		)
		if err != nil {
			return crossrefJournalPage{}, err
		}
		if strings.TrimSpace(version) == "" {
			return crossrefJournalPage{}, errors.New(
				"Crossref journal page message-version must not be blank",
			)
		}
	}

	messageRaw, exists := envelope["message"]
	if !exists || isJSONNull(messageRaw) {
		return crossrefJournalPage{}, errors.New(
			"Crossref journal page requires a message object",
		)
	}
	message, err := decodeJSONObject(
		messageRaw,
		"Crossref journal page message object",
	)
	if err != nil {
		return crossrefJournalPage{}, errors.New(
			"Crossref journal page requires a message object",
		)
	}
	if err := rejectUnexpectedJSONFields(
		message,
		"Crossref journal page message object",
		"total-results",
		"items-per-page",
		"next-cursor",
		"query",
		"items",
	); err != nil {
		return crossrefJournalPage{}, err
	}

	totalResults, err := requiredJSONInt64(
		message,
		"total-results",
		"Crossref journal page message",
	)
	if err != nil {
		return crossrefJournalPage{}, err
	}
	if totalResults < 0 {
		return crossrefJournalPage{}, errors.New(
			"Crossref journal page total-results must not be negative",
		)
	}
	nextCursor, err := requiredJSONString(
		message,
		"next-cursor",
		"Crossref journal page message",
	)
	if err != nil {
		return crossrefJournalPage{}, err
	}
	if nextCursor != strings.TrimSpace(nextCursor) {
		return crossrefJournalPage{}, errors.New(
			"Crossref journal page next-cursor must be trimmed",
		)
	}

	itemsRaw, exists := message["items"]
	if !exists || isJSONNull(itemsRaw) {
		return crossrefJournalPage{}, errors.New(
			"Crossref journal page message requires a non-null items array",
		)
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(itemsRaw, &rawItems); err != nil ||
		rawItems == nil {
		return crossrefJournalPage{}, errors.New(
			"Crossref journal page message requires a non-null items array",
		)
	}
	if rawItemsPerPage, exists := message["items-per-page"]; exists {
		itemsPerPage, err := decodeJSONInt64(
			rawItemsPerPage,
			"Crossref journal page items-per-page",
		)
		if err != nil {
			return crossrefJournalPage{}, err
		}
		if itemsPerPage != int64(len(rawItems)) {
			return crossrefJournalPage{}, fmt.Errorf(
				"Crossref journal page items-per-page = %d, actual items = %d",
				itemsPerPage,
				len(rawItems),
			)
		}
	}
	if rawQuery, exists := message["query"]; exists &&
		!isJSONNull(rawQuery) {
		if _, err := decodeJSONObject(
			rawQuery,
			"Crossref journal page query",
		); err != nil {
			return crossrefJournalPage{}, err
		}
	}

	journals := make([]CrossrefJournal, len(rawItems))
	for index, rawItem := range rawItems {
		journal, err := decodeCrossrefJournal(rawItem)
		if err != nil {
			return crossrefJournalPage{}, fmt.Errorf(
				"parse Crossref journal item %d: %w",
				index+1,
				err,
			)
		}
		journals[index] = journal
	}
	return crossrefJournalPage{
		totalResults: totalResults,
		nextCursor:   nextCursor,
		journals:     journals,
	}, nil
}

func decodeCrossrefJournal(raw []byte) (CrossrefJournal, error) {
	if err := validateSingleUniqueJSONValue(
		raw,
		"Crossref journal item",
	); err != nil {
		return CrossrefJournal{}, fmt.Errorf(
			"validate Crossref journal item JSON: %w",
			err,
		)
	}
	fields, err := decodeJSONObject(raw, "Crossref journal item")
	if err != nil {
		return CrossrefJournal{}, err
	}

	title, err := requiredJSONString(
		fields,
		"title",
		"Crossref journal item",
	)
	if err != nil {
		return CrossrefJournal{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return CrossrefJournal{}, errors.New(
			"Crossref journal item title must not be blank",
		)
	}
	publisher, err := requiredJSONString(
		fields,
		"publisher",
		"Crossref journal item",
	)
	if err != nil {
		return CrossrefJournal{}, err
	}
	publisher = strings.TrimSpace(publisher)
	if publisher == "" {
		return CrossrefJournal{}, errors.New(
			"Crossref journal item publisher must not be blank",
		)
	}

	rawISSNs, exists := fields["ISSN"]
	if !exists || isJSONNull(rawISSNs) {
		return CrossrefJournal{}, errors.New(
			"Crossref journal item requires a non-null ISSN array",
		)
	}
	var sourceISSNs []string
	if err := json.Unmarshal(rawISSNs, &sourceISSNs); err != nil ||
		sourceISSNs == nil {
		return CrossrefJournal{}, errors.New(
			"Crossref journal item requires a non-null string ISSN array",
		)
	}
	issns := make([]string, 0, len(sourceISSNs))
	issnSet := make(map[string]struct{}, len(sourceISSNs))
	for index, sourceISSN := range sourceISSNs {
		if !validCrossrefISSNSyntax(sourceISSN) {
			return CrossrefJournal{}, fmt.Errorf(
				"Crossref journal item ISSN[%d] is invalid: %q",
				index,
				sourceISSN,
			)
		}
		if _, duplicate := issnSet[sourceISSN]; duplicate {
			continue
		}
		issnSet[sourceISSN] = struct{}{}
		issns = append(issns, sourceISSN)
	}

	rawISSNTypes, exists := fields["issn-type"]
	if !exists || isJSONNull(rawISSNTypes) {
		return CrossrefJournal{}, errors.New(
			"Crossref journal item requires a non-null issn-type array",
		)
	}
	var sourceISSNTypes []json.RawMessage
	if err := json.Unmarshal(rawISSNTypes, &sourceISSNTypes); err != nil ||
		sourceISSNTypes == nil {
		return CrossrefJournal{}, errors.New(
			"Crossref journal item requires a non-null issn-type array",
		)
	}
	issnTypes := make([]CrossrefJournalISSNType, 0, len(sourceISSNTypes))
	var printISSN, electronicISSN string
	for index, rawISSNType := range sourceISSNTypes {
		typeFields, err := decodeJSONObject(
			rawISSNType,
			fmt.Sprintf("Crossref journal item issn-type[%d]", index),
		)
		if err != nil {
			return CrossrefJournal{}, err
		}
		if err := rejectUnexpectedJSONFields(
			typeFields,
			fmt.Sprintf("Crossref journal item issn-type[%d]", index),
			"value",
			"type",
		); err != nil {
			return CrossrefJournal{}, err
		}
		value, err := requiredJSONString(
			typeFields,
			"value",
			fmt.Sprintf("Crossref journal item issn-type[%d]", index),
		)
		if err != nil {
			return CrossrefJournal{}, err
		}
		if _, exists := issnSet[value]; !exists {
			return CrossrefJournal{}, fmt.Errorf(
				"Crossref journal item issn-type[%d] value %q is absent from ISSN",
				index,
				value,
			)
		}
		rawKind, exists := typeFields["type"]
		if !exists {
			return CrossrefJournal{}, fmt.Errorf(
				"Crossref journal item issn-type[%d] requires type",
				index,
			)
		}
		if isJSONNull(rawKind) {
			continue
		}
		kind, err := decodeJSONString(
			rawKind,
			fmt.Sprintf("Crossref journal item issn-type[%d] type", index),
		)
		if err != nil {
			return CrossrefJournal{}, err
		}
		switch kind {
		case "print":
			if printISSN == "" {
				printISSN = value
			}
		case "electronic":
			if electronicISSN == "" {
				electronicISSN = value
			}
		default:
			return CrossrefJournal{}, fmt.Errorf(
				"Crossref journal item issn-type[%d] type = %q, want print or electronic",
				index,
				kind,
			)
		}
		issnTypes = append(issnTypes, CrossrefJournalISSNType{
			Value: value,
			Type:  kind,
		})
	}

	rawCounts, exists := fields["counts"]
	if !exists || isJSONNull(rawCounts) {
		return CrossrefJournal{}, errors.New(
			"Crossref journal item requires a counts object",
		)
	}
	counts, err := decodeJSONObject(
		rawCounts,
		"Crossref journal item counts",
	)
	if err != nil {
		return CrossrefJournal{}, errors.New(
			"Crossref journal item requires a counts object",
		)
	}
	totalDOIs, err := requiredJSONInt64(
		counts,
		"total-dois",
		"Crossref journal item counts",
	)
	if err != nil {
		return CrossrefJournal{}, err
	}
	if totalDOIs < 0 {
		return CrossrefJournal{}, errors.New(
			"Crossref journal item counts.total-dois must not be negative",
		)
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return CrossrefJournal{}, fmt.Errorf(
			"compact Crossref journal item JSON: %w",
			err,
		)
	}
	return CrossrefJournal{
		Title:          title,
		ISSNs:          issns,
		ISSNTypes:      issnTypes,
		PrintISSN:      printISSN,
		ElectronicISSN: electronicISSN,
		Publisher:      publisher,
		TotalDOIs:      totalDOIs,
		Raw:            append(json.RawMessage(nil), compact.Bytes()...),
	}, nil
}

func validCrossrefISSNSyntax(value string) bool {
	if value != strings.ToUpper(strings.TrimSpace(value)) ||
		len(value) != 9 ||
		value[4] != '-' {
		return false
	}
	for index, character := range value {
		if index == 4 {
			continue
		}
		if character >= '0' && character <= '9' {
			continue
		}
		if index == len(value)-1 && character == 'X' {
			continue
		}
		return false
	}
	return true
}

func decodeJSONObject(
	payload []byte,
	name string,
) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	return fields, nil
}

func rejectUnexpectedJSONFields(
	fields map[string]json.RawMessage,
	path string,
	allowed ...string,
) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := allowedSet[field]; !ok {
			return fmt.Errorf("%s contains unexpected field %q", path, field)
		}
	}
	return nil
}

func requiredJSONString(
	fields map[string]json.RawMessage,
	name string,
	path string,
) (string, error) {
	raw, exists := fields[name]
	if !exists || isJSONNull(raw) {
		return "", fmt.Errorf("%s requires %s", path, name)
	}
	return decodeJSONString(raw, path+" "+name)
}

func decodeJSONString(raw []byte, path string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string: %w", path, err)
	}
	return value, nil
}

func requiredJSONInt64(
	fields map[string]json.RawMessage,
	name string,
	path string,
) (int64, error) {
	raw, exists := fields[name]
	if !exists || isJSONNull(raw) {
		return 0, fmt.Errorf("%s requires %s", path, name)
	}
	return decodeJSONInt64(raw, path+" "+name)
}

func decodeJSONInt64(raw []byte, path string) (int64, error) {
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", path, err)
	}
	return value, nil
}

func isJSONNull(raw []byte) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func crossrefCatalogPageJSONL(journals []CrossrefJournal) []byte {
	var buffer bytes.Buffer
	for _, journal := range journals {
		buffer.Write(journal.Raw)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes()
}

func appendCrossrefCatalogPage(path string, payload []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open Crossref catalog JSONL for append: %w", err)
	}
	_, writeErr := file.Write(payload)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append Crossref catalog JSONL page: %w", writeErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync Crossref catalog JSONL page: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Crossref catalog JSONL page: %w", closeErr)
	}
	return nil
}

func writeCrossrefCatalogManifestAtomic(
	path string,
	manifest CrossrefCatalogManifest,
) error {
	payload, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode Crossref catalog manifest: %w", err)
	}
	payload = append(payload, '\n')
	if err := writeFileAtomic(path, payload); err != nil {
		return fmt.Errorf("write Crossref catalog manifest atomically: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, payload []byte) (returnErr error) {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(
		directory,
		"."+filepath.Base(path)+".tmp-*",
	)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if returnErr != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := temp.Write(payload); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("rename temporary file: %w", err)
	}
	return nil
}

type duplicateJSONKeyError struct {
	path string
	key  string
}

func (err *duplicateJSONKeyError) Error() string {
	return fmt.Sprintf(
		"duplicate JSON object key %q at %s",
		err.key,
		err.path,
	)
}

func validateSingleUniqueJSONValue(payload []byte, name string) error {
	if !utf8.Valid(payload) {
		return fmt.Errorf("%s contains invalid UTF-8", name)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := validateUniqueJSONValue(decoder, "$"); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode trailing JSON token: %w", err)
		}
		return fmt.Errorf(
			"JSON must contain exactly one value; found trailing token %v",
			token,
		)
	}
	return nil
}

func validateUniqueJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode JSON token at %s: %w", path, err)
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf(
					"decode JSON object key at %s: %w",
					path,
					err,
				)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf(
					"JSON object key at %s is not a string",
					path,
				)
			}
			if _, duplicate := seen[key]; duplicate {
				return &duplicateJSONKeyError{path: path, key: key}
			}
			seen[key] = struct{}{}
			if err := validateUniqueJSONValue(
				decoder,
				jsonObjectPath(path, key),
			); err != nil {
				return err
			}
		}
		return consumeUniqueJSONDelimiter(decoder, '}', path)
	case '[':
		index := 0
		for decoder.More() {
			if err := validateUniqueJSONValue(
				decoder,
				path+"["+strconv.Itoa(index)+"]",
			); err != nil {
				return err
			}
			index++
		}
		return consumeUniqueJSONDelimiter(decoder, ']', path)
	default:
		return fmt.Errorf(
			"unexpected JSON delimiter %q at %s",
			delimiter,
			path,
		)
	}
}

func consumeUniqueJSONDelimiter(
	decoder *json.Decoder,
	want json.Delim,
	path string,
) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf(
			"decode closing JSON delimiter at %s: %w",
			path,
			err,
		)
	}
	got, ok := token.(json.Delim)
	if !ok || got != want {
		return fmt.Errorf(
			"closing JSON delimiter at %s = %v, want %q",
			path,
			token,
			want,
		)
	}
	return nil
}

func jsonObjectPath(parent string, key string) string {
	encoded, _ := json.Marshal(key)
	return parent + "[" + string(encoded) + "]"
}
