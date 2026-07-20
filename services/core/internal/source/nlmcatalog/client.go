package nlmcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
)

const (
	nlmCatalogDatabase = "nlmcatalog"
	eSummaryVersion    = "2.0"
)

type Config struct {
	BaseURL          string
	Tool             string
	Email            string
	APIKey           string
	UserAgent        string
	Timeout          time.Duration
	MaxRetries       int
	MaxWait          time.Duration
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	MaxResponseBytes int64
}

type Query struct {
	Title          string
	CandidateISSNs []string
}

type TitleMain struct {
	Title     string
	SortTitle string
}

type Result struct {
	UID                   string
	NLMUniqueID           string
	TitleMainSort         string
	TitleMainList         []TitleMain
	PrintISSN             string
	ElectronicISSN        string
	AllISSNs              []string
	DateRevised           string
	EndYear               string
	CurrentIndexingStatus string
	ESearchSHA256         string
	ESummarySHA256        string
}

type Client struct {
	baseURL    *url.URL
	tool       string
	email      string
	apiKey     string
	httpClient *httpclient.Client
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
		return nil, errors.New("NLM Catalog NCBI tool is required")
	}
	email := strings.TrimSpace(config.Email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return nil, errors.New(
			"NLM Catalog NCBI email must be a valid bare email address",
		)
	}
	apiKey := strings.TrimSpace(config.APIKey)
	requestsPerSecond := 3
	if apiKey != "" {
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
		return nil, fmt.Errorf("create NLM Catalog HTTP policy: %w", err)
	}
	return &Client{
		baseURL:    baseURL,
		tool:       tool,
		email:      email,
		apiKey:     apiKey,
		httpClient: policy,
	}, nil
}

func (client *Client) Resolve(
	ctx context.Context,
	query Query,
) (Result, error) {
	if client == nil {
		return Result{}, errors.New("NLM Catalog client is nil")
	}
	if ctx == nil {
		return Result{}, errors.New("NLM Catalog context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	title, issns, err := validateQuery(query)
	if err != nil {
		return Result{}, err
	}

	searchValues := make(url.Values)
	searchValues.Set("db", nlmCatalogDatabase)
	searchValues.Set("term", exactTitleISSNQuery(title, issns))
	searchValues.Set("retmode", "json")
	searchValues.Set("retmax", "1")
	client.addIdentity(searchValues)

	searchPayload, err := client.get(ctx, "esearch.fcgi", searchValues)
	if err != nil {
		return Result{}, fmt.Errorf("search NLM Catalog identity: %w", err)
	}
	searchSHA256 := sha256Hex(searchPayload)
	search, err := decodeESearch(searchPayload)
	if err != nil {
		return Result{}, fmt.Errorf("decode NLM Catalog ESearch: %w", err)
	}
	if search.Count != 1 || len(search.UIDs) != 1 {
		return Result{}, fmt.Errorf(
			"NLM Catalog ESearch requires exactly one UID; count=%d ids=%d",
			search.Count,
			len(search.UIDs),
		)
	}
	uid := search.UIDs[0]

	summaryValues := make(url.Values)
	summaryValues.Set("db", nlmCatalogDatabase)
	summaryValues.Set("id", uid)
	summaryValues.Set("retmode", "json")
	summaryValues.Set("version", eSummaryVersion)
	client.addIdentity(summaryValues)

	summaryPayload, err := client.get(ctx, "esummary.fcgi", summaryValues)
	if err != nil {
		return Result{}, fmt.Errorf("summarize NLM Catalog identity: %w", err)
	}
	summarySHA256 := sha256Hex(summaryPayload)
	result, err := decodeESummary(summaryPayload, uid)
	if err != nil {
		return Result{}, fmt.Errorf("decode NLM Catalog ESummary: %w", err)
	}
	result.ESearchSHA256 = searchSHA256
	result.ESummarySHA256 = summarySHA256
	if err := result.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate NLM Catalog result: %w", err)
	}
	return result, nil
}

func (result Result) Validate() error {
	if err := validateUID(result.UID); err != nil {
		return fmt.Errorf("uid: %w", err)
	}
	if result.NLMUniqueID != result.UID {
		return fmt.Errorf(
			"nlmuniqueid %q does not match UID %q",
			result.NLMUniqueID,
			result.UID,
		)
	}
	if err := validateRequiredTrimmed(
		"titlemainsort",
		result.TitleMainSort,
	); err != nil {
		return err
	}
	if len(result.TitleMainList) == 0 {
		return errors.New("titlemainlist must be non-empty")
	}
	for index, title := range result.TitleMainList {
		if err := validateRequiredTrimmed(
			fmt.Sprintf("titlemainlist[%d].title", index),
			title.Title,
		); err != nil {
			return err
		}
		if err := validateRequiredTrimmed(
			fmt.Sprintf("titlemainlist[%d].sorttitle", index),
			title.SortTitle,
		); err != nil {
			return err
		}
	}
	if _, err := result.MainTitle(); err != nil {
		return err
	}
	if err := validateAuthorityISSNs(result); err != nil {
		return err
	}
	revised, err := time.Parse("2006-01-02", result.DateRevised)
	if err != nil ||
		revised.Format("2006-01-02") != result.DateRevised {
		return errors.New("daterevised must be a valid YYYY-MM-DD date")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "endyear", value: result.EndYear},
		{name: "currentindexingstatus", value: result.CurrentIndexingStatus},
	} {
		if field.value != strings.TrimSpace(field.value) {
			return fmt.Errorf("%s must be trimmed", field.name)
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "esearch_sha256", value: result.ESearchSHA256},
		{name: "esummary_sha256", value: result.ESummarySHA256},
	} {
		if !validSHA256(field.value) {
			return fmt.Errorf(
				"%s must be 64 lowercase hexadecimal characters",
				field.name,
			)
		}
	}
	return nil
}

func (result Result) MainTitle() (string, error) {
	match := ""
	matches := 0
	for _, title := range result.TitleMainList {
		if title.SortTitle != result.TitleMainSort {
			continue
		}
		match = title.Title
		matches++
	}
	if matches != 1 {
		return "", fmt.Errorf(
			"titlemainlist must contain exactly one entry whose sorttitle equals titlemainsort; got %d",
			matches,
		)
	}
	return match, nil
}

func validateAuthorityISSNs(result Result) error {
	if result.PrintISSN == "" && result.ElectronicISSN == "" {
		return errors.New("NLM Catalog valid ISSN identity must be non-empty")
	}
	expected := make([]string, 0, 2)
	seen := make(map[string]string, 2)
	for _, candidate := range []struct {
		role  venue.ISSNRole
		name  string
		value string
	}{
		{role: venue.ISSNRolePrint, name: "print", value: result.PrintISSN},
		{role: venue.ISSNRoleElectronic, name: "electronic", value: result.ElectronicISSN},
	} {
		if candidate.value == "" {
			continue
		}
		parsed, err := venue.ParseISSN(candidate.role, candidate.value)
		if err != nil {
			return fmt.Errorf("invalid %s ISSN: %w", candidate.name, err)
		}
		if parsed.String() != candidate.value {
			return fmt.Errorf(
				"%s ISSN %q must use canonical form %q",
				candidate.name,
				candidate.value,
				parsed.String(),
			)
		}
		if otherRole, exists := seen[candidate.value]; exists {
			return fmt.Errorf(
				"ISSN %q has conflicting roles %s and %s",
				candidate.value,
				otherRole,
				candidate.name,
			)
		}
		seen[candidate.value] = candidate.name
		expected = append(expected, candidate.value)
	}
	slices.Sort(expected)
	if !slices.Equal(result.AllISSNs, expected) {
		return fmt.Errorf(
			"all ISSNs %v do not match exact role identity %v",
			result.AllISSNs,
			expected,
		)
	}
	return nil
}

func validateQuery(query Query) (string, []string, error) {
	title := query.Title
	if err := validateRequiredTrimmed("NLM Catalog source title", title); err != nil {
		return "", nil, err
	}
	for _, current := range title {
		if current == '"' || unicode.IsControl(current) {
			return "", nil, errors.New(
				"NLM Catalog source title cannot be represented as an exact quoted Title query",
			)
		}
	}
	if len(query.CandidateISSNs) == 0 {
		return "", nil, errors.New(
			"NLM Catalog query requires at least one candidate ISSN",
		)
	}
	issns := make([]string, 0, len(query.CandidateISSNs))
	seen := make(map[string]struct{}, len(query.CandidateISSNs))
	for index, raw := range query.CandidateISSNs {
		parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, raw)
		if err != nil {
			return "", nil, fmt.Errorf(
				"NLM Catalog candidate ISSN %d: %w",
				index+1,
				err,
			)
		}
		value := parsed.String()
		if value != raw {
			return "", nil, fmt.Errorf(
				"NLM Catalog candidate ISSN %d %q must use canonical form %q",
				index+1,
				raw,
				value,
			)
		}
		if _, duplicate := seen[value]; duplicate {
			return "", nil, fmt.Errorf(
				"NLM Catalog candidate ISSN %q is duplicated",
				value,
			)
		}
		seen[value] = struct{}{}
		issns = append(issns, value)
	}
	slices.Sort(issns)
	return title, issns, nil
}

func exactTitleISSNQuery(title string, issns []string) string {
	filters := make([]string, len(issns))
	for index, issn := range issns {
		filters[index] = `"` + issn + `"[ISSN]`
	}
	return `"` + title + `"[Title] AND (` +
		strings.Join(filters, " OR ") +
		")"
}

func (client *Client) get(
	ctx context.Context,
	operation string,
	values url.Values,
) ([]byte, error) {
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") +
		"/entrez/eutils/" +
		operation
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		endpoint.String(),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create NLM Catalog %s request: %w",
			operation,
			err,
		)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf(
			"read NLM Catalog %s response: %w",
			operation,
			readErr,
		)
	}
	if closeErr != nil {
		return nil, fmt.Errorf(
			"close NLM Catalog %s response: %w",
			operation,
			closeErr,
		)
	}
	return payload, nil
}

func (client *Client) addIdentity(values url.Values) {
	values.Set("tool", client.tool)
	values.Set("email", client.email)
	if client.apiKey != "" {
		values.Set("api_key", client.apiKey)
	}
}

type eSearchResult struct {
	Count int
	UIDs  []string
}

func decodeESearch(payload []byte) (eSearchResult, error) {
	var envelope map[string]json.RawMessage
	if err := decodeStrictJSON(payload, &envelope); err != nil {
		return eSearchResult{}, err
	}
	rawResult, exists := envelope["esearchresult"]
	if !exists {
		return eSearchResult{}, errors.New(
			"ESearch JSON requires exact esearchresult field",
		)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawResult, &fields); err != nil || fields == nil {
		return eSearchResult{}, errors.New(
			"ESearch esearchresult must be a JSON object",
		)
	}
	countRaw, err := requiredJSONString(fields, "count")
	if err != nil {
		return eSearchResult{}, err
	}
	count, err := parseASCIIInt(countRaw)
	if err != nil {
		return eSearchResult{}, fmt.Errorf("ESearch count: %w", err)
	}
	rawUIDs, exists := fields["idlist"]
	if !exists {
		return eSearchResult{}, errors.New(
			"ESearch JSON requires exact idlist field",
		)
	}
	var uids []string
	if err := json.Unmarshal(rawUIDs, &uids); err != nil || uids == nil {
		return eSearchResult{}, errors.New(
			"ESearch idlist must be an array of strings",
		)
	}
	for index, uid := range uids {
		if err := validateUID(uid); err != nil {
			return eSearchResult{}, fmt.Errorf(
				"ESearch idlist[%d]: %w",
				index,
				err,
			)
		}
	}
	return eSearchResult{Count: count, UIDs: uids}, nil
}

func decodeESummary(payload []byte, requestedUID string) (Result, error) {
	var envelope map[string]json.RawMessage
	if err := decodeStrictJSON(payload, &envelope); err != nil {
		return Result{}, err
	}
	rawResult, exists := envelope["result"]
	if !exists {
		return Result{}, errors.New(
			"ESummary JSON requires exact result field",
		)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawResult, &fields); err != nil || fields == nil {
		return Result{}, errors.New("ESummary result must be a JSON object")
	}
	rawUIDs, exists := fields["uids"]
	if !exists {
		return Result{}, errors.New(
			"ESummary result requires exact uids field",
		)
	}
	var uids []string
	if err := json.Unmarshal(rawUIDs, &uids); err != nil ||
		len(uids) != 1 ||
		uids[0] != requestedUID {
		return Result{}, fmt.Errorf(
			"ESummary UID list must contain only requested UID %q",
			requestedUID,
		)
	}
	rawItem, exists := fields[requestedUID]
	if !exists {
		return Result{}, fmt.Errorf(
			"ESummary result requires requested UID object %q",
			requestedUID,
		)
	}
	if len(fields) != 2 {
		return Result{}, errors.New(
			"ESummary result contains duplicate or inconsistent UID objects",
		)
	}
	var item map[string]json.RawMessage
	if err := json.Unmarshal(rawItem, &item); err != nil || item == nil {
		return Result{}, errors.New(
			"ESummary requested UID value must be a JSON object",
		)
	}

	uid, err := requiredJSONString(item, "uid")
	if err != nil {
		return Result{}, err
	}
	if uid != requestedUID {
		return Result{}, fmt.Errorf(
			"ESummary item UID %q does not match requested UID %q",
			uid,
			requestedUID,
		)
	}
	nlmUniqueID, err := requiredJSONString(item, "nlmuniqueid")
	if err != nil {
		return Result{}, err
	}
	titleMainSort, err := requiredJSONString(item, "titlemainsort")
	if err != nil {
		return Result{}, err
	}
	titleMainList, err := decodeTitleMainList(item)
	if err != nil {
		return Result{}, err
	}
	printISSN, electronicISSN, allISSNs, err := decodeISSNList(item)
	if err != nil {
		return Result{}, err
	}
	dateRevised, err := requiredJSONString(item, "daterevised")
	if err != nil {
		return Result{}, err
	}
	endYear, err := requiredJSONString(item, "endyear")
	if err != nil {
		return Result{}, err
	}
	currentIndexingStatus, err := requiredJSONString(
		item,
		"currentindexingstatus",
	)
	if err != nil {
		return Result{}, err
	}
	return Result{
		UID:                   uid,
		NLMUniqueID:           nlmUniqueID,
		TitleMainSort:         titleMainSort,
		TitleMainList:         titleMainList,
		PrintISSN:             printISSN,
		ElectronicISSN:        electronicISSN,
		AllISSNs:              allISSNs,
		DateRevised:           dateRevised,
		EndYear:               endYear,
		CurrentIndexingStatus: currentIndexingStatus,
	}, nil
}

func decodeTitleMainList(
	item map[string]json.RawMessage,
) ([]TitleMain, error) {
	rawList, exists := item["titlemainlist"]
	if !exists {
		return nil, errors.New(
			"ESummary item requires exact titlemainlist field",
		)
	}
	var rawTitles []map[string]json.RawMessage
	if err := json.Unmarshal(rawList, &rawTitles); err != nil ||
		len(rawTitles) == 0 {
		return nil, errors.New(
			"ESummary titlemainlist must be a non-empty array of objects",
		)
	}
	titles := make([]TitleMain, len(rawTitles))
	for index, fields := range rawTitles {
		title, err := requiredJSONString(fields, "title")
		if err != nil {
			return nil, fmt.Errorf("titlemainlist[%d]: %w", index, err)
		}
		sortTitle, err := requiredJSONString(fields, "sorttitle")
		if err != nil {
			return nil, fmt.Errorf("titlemainlist[%d]: %w", index, err)
		}
		titles[index] = TitleMain{
			Title:     title,
			SortTitle: sortTitle,
		}
	}
	return titles, nil
}

func decodeISSNList(
	item map[string]json.RawMessage,
) (string, string, []string, error) {
	rawList, exists := item["issnlist"]
	if !exists {
		return "", "", nil, errors.New(
			"ESummary item requires exact issnlist field",
		)
	}
	var rawISSNs []map[string]json.RawMessage
	if err := json.Unmarshal(rawList, &rawISSNs); err != nil {
		return "", "", nil, errors.New(
			"ESummary issnlist must be an array of objects",
		)
	}
	roles := make(map[venue.ISSNRole]string, 2)
	valueRoles := make(map[string]venue.ISSNRole, 2)
	claims := make(map[string]struct{}, len(rawISSNs))
	for index, fields := range rawISSNs {
		raw, err := requiredJSONString(fields, "issn")
		if err != nil {
			return "", "", nil, fmt.Errorf("issnlist[%d]: %w", index, err)
		}
		issnType, err := requiredJSONString(fields, "issntype")
		if err != nil {
			return "", "", nil, fmt.Errorf("issnlist[%d]: %w", index, err)
		}
		validYN, err := requiredJSONString(fields, "validyn")
		if err != nil {
			return "", "", nil, fmt.Errorf("issnlist[%d]: %w", index, err)
		}
		if validYN != "Y" && validYN != "N" {
			return "", "", nil, fmt.Errorf(
				"issnlist[%d] validyn %q must be Y or N",
				index,
				validYN,
			)
		}
		if validYN == "N" {
			continue
		}
		var role venue.ISSNRole
		switch issnType {
		case "Print":
			role = venue.ISSNRolePrint
		case "Electronic":
			role = venue.ISSNRoleElectronic
		default:
			return "", "", nil, fmt.Errorf(
				"issnlist[%d] valid ISSN type %q is not Print or Electronic",
				index,
				issnType,
			)
		}
		parsed, err := venue.ParseISSN(role, raw)
		if err != nil {
			return "", "", nil, fmt.Errorf(
				"issnlist[%d] invalid ISSN: %w",
				index,
				err,
			)
		}
		value := parsed.String()
		if value != raw {
			return "", "", nil, fmt.Errorf(
				"issnlist[%d] ISSN %q must use canonical form %q",
				index,
				raw,
				value,
			)
		}
		claimKey := string(role) + "\x00" + value
		if _, duplicate := claims[claimKey]; duplicate {
			return "", "", nil, fmt.Errorf(
				"issnlist[%d] duplicates ISSN role claim %s=%q",
				index,
				role,
				value,
			)
		}
		claims[claimKey] = struct{}{}
		if current, exists := roles[role]; exists && current != value {
			return "", "", nil, fmt.Errorf(
				"NLM Catalog ISSN role %s conflicts between %q and %q",
				role,
				current,
				value,
			)
		}
		if currentRole, exists := valueRoles[value]; exists &&
			currentRole != role {
			return "", "", nil, fmt.Errorf(
				"NLM Catalog ISSN %q has conflicting roles %s and %s",
				value,
				currentRole,
				role,
			)
		}
		roles[role] = value
		valueRoles[value] = role
	}
	allISSNs := make([]string, 0, len(valueRoles))
	for value := range valueRoles {
		allISSNs = append(allISSNs, value)
	}
	slices.Sort(allISSNs)
	if len(allISSNs) == 0 {
		return "", "", nil, errors.New(
			"NLM Catalog valid ISSN identity must be non-empty",
		)
	}
	return roles[venue.ISSNRolePrint],
		roles[venue.ISSNRoleElectronic],
		allISSNs,
		nil
}

func requiredJSONString(
	fields map[string]json.RawMessage,
	key string,
) (string, error) {
	raw, exists := fields[key]
	if !exists {
		return "", fmt.Errorf("JSON requires exact %s field", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("JSON %s field must be a string", key)
	}
	return value, nil
}

func parseASCIIInt(raw string) (int, error) {
	if raw == "" {
		return 0, errors.New("must be a non-empty ASCII digit string")
	}
	for index := range len(raw) {
		if raw[index] < '0' || raw[index] > '9' {
			return 0, errors.New("must contain only ASCII digits")
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New("exceeds int64")
	}
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return 0, errors.New("exceeds int range")
	}
	return int(value), nil
}

func validateUID(uid string) error {
	value, err := parseASCIIInt(uid)
	if err != nil || value <= 0 || uid[0] == '0' {
		return errors.New("must be a positive canonical ASCII integer")
	}
	return nil
}

func validateRequiredTrimmed(field, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty and trimmed", field)
	}
	return nil
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 ||
		value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
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
			"NLM Catalog base URL must be an absolute http:// or https:// URL without query or fragment",
		)
	}
	return parsed, nil
}
