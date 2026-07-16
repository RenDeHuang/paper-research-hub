package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/catalog"
)

const timeFormat = time.RFC3339Nano

func parsePaperListQuery(request *http.Request) (catalog.PaperListQuery, error) {
	values, err := strictQuery(request.URL.Query(), map[string]struct{}{
		"q":              {},
		"published_from": {},
		"published_to":   {},
		"type":           {},
		"topic":          {},
		"method":         {},
		"has_code":       {},
		"has_data":       {},
		"has_benchmark":  {},
		"status":         {},
		"source":         {},
		"sort":           {},
		"limit":          {},
		"cursor":         {},
	})
	if err != nil {
		return catalog.PaperListQuery{}, err
	}

	query := catalog.PaperListQuery{}
	if entries, present := values["q"]; present {
		value := entries[0]
		if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > 300 {
			return catalog.PaperListQuery{}, invalidQuery("q")
		}
		query.Query = value
	}
	if entries, present := values["published_from"]; present {
		value := entries[0]
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return catalog.PaperListQuery{}, invalidQuery("published_from")
		}
		query.PublishedFrom = &parsed
	}
	if entries, present := values["published_to"]; present {
		value := entries[0]
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return catalog.PaperListQuery{}, invalidQuery("published_to")
		}
		query.PublishedTo = &parsed
	}
	query.PaperType = values.Get("type")
	query.Topic = values.Get("topic")
	query.Method = values.Get("method")
	query.Status = values.Get("status")
	query.Source = values.Get("source")
	query.Sort = catalog.PaperSort(values.Get("sort"))

	if entries, present := values["has_code"]; present {
		value := entries[0]
		parsed, err := parseStrictBoolean(value)
		if err != nil {
			return catalog.PaperListQuery{}, invalidQuery("has_code")
		}
		query.HasCode = &parsed
	}
	if entries, present := values["has_data"]; present {
		value := entries[0]
		parsed, err := parseStrictBoolean(value)
		if err != nil {
			return catalog.PaperListQuery{}, invalidQuery("has_data")
		}
		query.HasData = &parsed
	}
	if entries, present := values["has_benchmark"]; present {
		value := entries[0]
		parsed, err := parseStrictBoolean(value)
		if err != nil {
			return catalog.PaperListQuery{}, invalidQuery("has_benchmark")
		}
		query.HasBenchmark = &parsed
	}
	query.Limit, err = parseLimit(values)
	if err != nil {
		return catalog.PaperListQuery{}, err
	}
	query.Cursor, err = parseCursor(values)
	if err != nil {
		return catalog.PaperListQuery{}, err
	}
	return query, nil
}

func parsePageQuery(request *http.Request) (catalog.PageQuery, error) {
	values, err := strictQuery(request.URL.Query(), map[string]struct{}{
		"limit":  {},
		"cursor": {},
	})
	if err != nil {
		return catalog.PageQuery{}, err
	}
	limit, err := parseLimit(values)
	if err != nil {
		return catalog.PageQuery{}, err
	}
	cursor, err := parseCursor(values)
	if err != nil {
		return catalog.PageQuery{}, err
	}
	return catalog.PageQuery{Limit: limit, Cursor: cursor}, nil
}

func parseTrendQuery(request *http.Request) (catalog.TrendQuery, error) {
	values, err := strictQuery(request.URL.Query(), map[string]struct{}{
		"window_days": {},
		"limit":       {},
		"cursor":      {},
	})
	if err != nil {
		return catalog.TrendQuery{}, err
	}
	limit, err := parseLimit(values)
	if err != nil {
		return catalog.TrendQuery{}, err
	}
	cursor, err := parseCursor(values)
	if err != nil {
		return catalog.TrendQuery{}, err
	}
	windowDays := 0
	if entries, present := values["window_days"]; present {
		value := entries[0]
		windowDays, err = parseStrictInteger(value, 1, 365)
		if err != nil {
			return catalog.TrendQuery{}, invalidQuery("window_days")
		}
	}
	return catalog.TrendQuery{
		WindowDays: windowDays,
		Limit:      limit,
		Cursor:     cursor,
	}, nil
}

func parseOpportunityQuery(request *http.Request) (catalog.OpportunityQuery, error) {
	values, err := strictQuery(request.URL.Query(), map[string]struct{}{
		"status": {},
		"limit":  {},
		"cursor": {},
	})
	if err != nil {
		return catalog.OpportunityQuery{}, err
	}
	limit, err := parseLimit(values)
	if err != nil {
		return catalog.OpportunityQuery{}, err
	}
	cursor, err := parseCursor(values)
	if err != nil {
		return catalog.OpportunityQuery{}, err
	}
	return catalog.OpportunityQuery{
		Status: values.Get("status"),
		Limit:  limit,
		Cursor: cursor,
	}, nil
}

func requireNoQuery(writer http.ResponseWriter, request *http.Request) bool {
	if len(request.URL.Query()) == 0 {
		return true
	}
	writeCatalogError(writer, request, invalidQuery("query"))
	return false
}

func strictQuery(values url.Values, allowed map[string]struct{}) (url.Values, error) {
	for key, entries := range values {
		if _, ok := allowed[key]; !ok {
			return nil, invalidQuery(key)
		}
		if len(entries) != 1 {
			return nil, invalidQuery(key)
		}
	}
	return values, nil
}

func parseLimit(values url.Values) (int, error) {
	value, present := values["limit"]
	if !present {
		return 0, nil
	}
	parsed, err := parseStrictInteger(value[0], 1, 100)
	if err != nil {
		return 0, invalidQuery("limit")
	}
	return parsed, nil
}

func parseCursor(values url.Values) (string, error) {
	value, present := values["cursor"]
	if !present {
		return "", nil
	}
	if value[0] == "" || len(value[0]) > 2048 {
		return "", invalidQuery("cursor")
	}
	return value[0], nil
}

func parseStrictInteger(value string, minimum, maximum int) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("empty integer")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("non-decimal integer")
		}
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("integer outside range")
	}
	return parsed, nil
}

func parseStrictBoolean(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("boolean must be true or false")
	}
}

func invalidQuery(parameter string) error {
	return fmt.Errorf("%w: invalid parameter %q", catalog.ErrInvalidQuery, parameter)
}
