package pubmed

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var issnPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{3}[0-9X]$`)

type DateType string

const (
	DateTypePublication  DateType = "publication"
	DateTypeEntrez       DateType = "entrez"
	DateTypeModification DateType = "modification"
)

type DateWindow struct {
	From time.Time
	To   time.Time
}

type SearchQuery struct {
	Term         string
	JournalISSNs []string
	DateType     DateType
	DateWindow   DateWindow
	MaxResults   int
}

type CoverageQuery struct {
	JournalISSNs []string
}

type Batch struct {
	RetStart int
	RetMax   int
}

type SearchResult struct {
	Count    int
	WebEnv   string
	QueryKey string
	Batches  []Batch
}

func (query SearchQuery) Values() (url.Values, error) {
	dateType, err := query.DateType.entrezValue()
	if err != nil {
		return nil, err
	}
	if query.DateWindow.From.IsZero() || query.DateWindow.To.IsZero() {
		return nil, errors.New("PubMed Entrez date window requires both from and to dates")
	}
	from := dateOnly(query.DateWindow.From)
	to := dateOnly(query.DateWindow.To)
	if from.After(to) {
		return nil, errors.New("PubMed Entrez date window from date must not follow to date")
	}
	if query.MaxResults <= 0 {
		return nil, errors.New("PubMed max results must be positive")
	}

	issns, err := normalizedISSNs(query.JournalISSNs)
	if err != nil {
		return nil, err
	}

	values := make(url.Values)
	values.Set("datetype", dateType)
	values.Set("mindate", from.Format("2006/01/02"))
	values.Set("maxdate", to.Format("2006/01/02"))

	term := strings.TrimSpace(query.Term)
	if len(issns) > 0 {
		journalFilter := exactISSNOR(issns)
		if term == "" {
			term = journalFilter
		} else {
			term = "(" + term + ") AND " + journalFilter
		}
	}
	if term != "" {
		values.Set("term", term)
	}
	return values, nil
}

func (query CoverageQuery) values() (url.Values, error) {
	issns, err := normalizedISSNs(query.JournalISSNs)
	if err != nil {
		return nil, err
	}
	if len(issns) == 0 {
		return nil, errors.New("PubMed coverage requires at least one journal ISSN")
	}
	values := make(url.Values)
	values.Set("term", exactISSNOR(issns))
	return values, nil
}

func BuildBatches(count, maxResults, batchSize int) ([]Batch, error) {
	if count < 0 {
		return nil, errors.New("PubMed count must not be negative")
	}
	if maxResults <= 0 {
		return nil, errors.New("PubMed max results must be positive")
	}
	if batchSize <= 0 {
		return nil, errors.New("PubMed batch size must be positive")
	}

	total := min(count, maxResults)
	batchCount := total / batchSize
	if total%batchSize != 0 {
		batchCount++
	}
	batches := make([]Batch, 0, batchCount)
	for start := 0; start < total; {
		retMax := min(batchSize, total-start)
		batches = append(batches, Batch{
			RetStart: start,
			RetMax:   retMax,
		})
		start += retMax
	}
	return batches, nil
}

func normalizedISSNs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, raw := range values {
		value := strings.ToUpper(strings.TrimSpace(raw))
		if !validISSN(value) {
			return nil, fmt.Errorf("invalid journal ISSN at position %d: %q", index+1, raw)
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

func exactISSNOR(issns []string) string {
	filters := make([]string, len(issns))
	for index, issn := range issns {
		filters[index] = issn + "[issn]"
	}
	return "(" + strings.Join(filters, " OR ") + ")"
}

func (dateType DateType) entrezValue() (string, error) {
	switch dateType {
	case DateTypePublication:
		return "pdat", nil
	case DateTypeEntrez:
		return "edat", nil
	case DateTypeModification:
		return "mdat", nil
	default:
		return "", fmt.Errorf("invalid PubMed date type %q", dateType)
	}
}

func validISSN(value string) bool {
	if !issnPattern.MatchString(value) {
		return false
	}
	sum := 0
	digitIndex := 0
	for _, character := range value {
		if character == '-' {
			continue
		}
		digit := 10
		if character != 'X' {
			digit = int(character - '0')
		}
		sum += digit * (8 - digitIndex)
		digitIndex++
	}
	return sum%11 == 0
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
