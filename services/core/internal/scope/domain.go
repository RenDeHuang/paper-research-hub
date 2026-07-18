package scope

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

const (
	ResearchDomainRegistryVersion = "research-domains-jcr-subjects/v2"
	maxDomainRegistryBytes        = 4 << 20
	maxDomainRegistryRows         = 20_000
)

var (
	ErrInvalidResearchDomain = errors.New("invalid research domain")
	ErrInvalidDomainRegistry = errors.New("invalid research domain Registry CSV")

	domainRegistryHeader = []string{
		"registry_name",
		"registry_version",
		"domain",
		"display_label",
		"jcr_category",
		"article_level_required",
	}
)

type ResearchDomain string

const (
	ResearchDomainMedicine        ResearchDomain = "medicine"
	ResearchDomainBiology         ResearchDomain = "biology"
	ResearchDomainComputerScience ResearchDomain = "computer_science"
)

func ResearchDomains() []ResearchDomain {
	return []ResearchDomain{
		ResearchDomainMedicine,
		ResearchDomainBiology,
		ResearchDomainComputerScience,
	}
}

func ParseResearchDomain(value string) (ResearchDomain, error) {
	domain := ResearchDomain(value)
	switch domain {
	case ResearchDomainMedicine,
		ResearchDomainBiology,
		ResearchDomainComputerScience:
		return domain, nil
	default:
		return "", fmt.Errorf("%w %q", ErrInvalidResearchDomain, value)
	}
}

type WorkDomainAssertion struct {
	ProjectionAssertionID string
	NormalizedAssertionID string
	SourceRecordID        string
	WorkID                string
	DomainRegistryVersion string
	DomainCategoryRuleID  string
	Domain                ResearchDomain
	SourcePath            string
	AssertedAt            time.Time
}

func (assertion WorkDomainAssertion) Validate() error {
	for field, value := range map[string]string{
		"projection assertion ID": assertion.ProjectionAssertionID,
		"normalized assertion ID": assertion.NormalizedAssertionID,
		"source record ID":        assertion.SourceRecordID,
		"Work ID":                 assertion.WorkID,
		"domain Category rule ID": assertion.DomainCategoryRuleID,
		"source path":             assertion.SourcePath,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("Work domain assertion requires exact %s", field)
		}
	}
	if assertion.DomainRegistryVersion != ResearchDomainRegistryVersion {
		return fmt.Errorf(
			"unsupported domain Registry version %q",
			assertion.DomainRegistryVersion,
		)
	}
	if _, err := ParseResearchDomain(string(assertion.Domain)); err != nil {
		return err
	}
	if assertion.AssertedAt.IsZero() {
		return errors.New("Work domain assertion asserted_at is required")
	}
	return nil
}

type DomainCategoryRule struct {
	domain               ResearchDomain
	displayLabel         string
	jcrCategory          string
	articleLevelRequired bool
}

func (rule DomainCategoryRule) Domain() ResearchDomain {
	return rule.domain
}

func (rule DomainCategoryRule) DisplayLabel() string {
	return rule.displayLabel
}

func (rule DomainCategoryRule) JCRCategory() string {
	return rule.jcrCategory
}

func (rule DomainCategoryRule) ArticleLevelRequired() bool {
	return rule.articleLevelRequired
}

type DomainRegistry struct {
	name       string
	version    string
	fileSHA256 string
	rules      []DomainCategoryRule
}

func (registry DomainRegistry) Name() string {
	return registry.name
}

func (registry DomainRegistry) Version() string {
	return registry.version
}

func (registry DomainRegistry) FileSHA256() string {
	return registry.fileSHA256
}

func (registry DomainRegistry) DomainCount() int {
	if len(registry.rules) == 0 {
		return 0
	}
	domains := make(map[ResearchDomain]struct{}, len(ResearchDomains()))
	for _, rule := range registry.rules {
		domains[rule.domain] = struct{}{}
	}
	return len(domains)
}

func (registry DomainRegistry) RuleCount() int {
	return len(registry.rules)
}

func (registry DomainRegistry) Rules() []DomainCategoryRule {
	return slices.Clone(registry.rules)
}

func (registry DomainRegistry) MatchJCRCategory(
	category string,
) []DomainCategoryRule {
	var result []DomainCategoryRule
	for _, rule := range registry.rules {
		if rule.jcrCategory == category {
			result = append(result, rule)
		}
	}
	return result
}

func (registry DomainRegistry) AutomaticDomainsForJCRCategory(
	category string,
) []ResearchDomain {
	var result []ResearchDomain
	for _, rule := range registry.rules {
		if rule.jcrCategory == category && !rule.articleLevelRequired {
			result = append(result, rule.domain)
		}
	}
	return result
}

func ParseDomainRegistry(source io.Reader) (DomainRegistry, error) {
	contents, err := readExactRegistry(
		source,
		maxDomainRegistryBytes,
		ErrInvalidDomainRegistry,
	)
	if err != nil {
		return DomainRegistry{}, err
	}
	reader := csv.NewReader(bytes.NewReader(contents))
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return DomainRegistry{}, fmt.Errorf("%w: CSV is empty", ErrInvalidDomainRegistry)
	}
	if err != nil {
		return DomainRegistry{}, fmt.Errorf(
			"%w: read CSV header: %w",
			ErrInvalidDomainRegistry,
			err,
		)
	}
	if !slices.Equal(header, domainRegistryHeader) {
		return DomainRegistry{}, fmt.Errorf(
			"%w: exact header must be %q",
			ErrInvalidDomainRegistry,
			strings.Join(domainRegistryHeader, ","),
		)
	}
	reader.FieldsPerRecord = len(domainRegistryHeader)

	var (
		registryName    string
		registryVersion string
		rules           []DomainCategoryRule
	)
	displayLabels := make(map[ResearchDomain]string, len(ResearchDomains()))
	identities := make(map[string]int)
	for rowNumber := 2; ; rowNumber++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return DomainRegistry{}, fmt.Errorf(
				"%w: read CSV row %d: %w",
				ErrInvalidDomainRegistry,
				rowNumber,
				readErr,
			)
		}
		if len(rules) >= maxDomainRegistryRows {
			return DomainRegistry{}, fmt.Errorf(
				"%w: row count exceeds %d",
				ErrInvalidDomainRegistry,
				maxDomainRegistryRows,
			)
		}
		if err := rejectRegistryNUL(
			record,
			domainRegistryHeader,
			rowNumber,
			ErrInvalidDomainRegistry,
		); err != nil {
			return DomainRegistry{}, err
		}
		rowName, err := exactRegistryText(
			record[0],
			"registry_name",
			rowNumber,
			ErrInvalidDomainRegistry,
		)
		if err != nil {
			return DomainRegistry{}, err
		}
		rowVersion, err := exactRegistryText(
			record[1],
			"registry_version",
			rowNumber,
			ErrInvalidDomainRegistry,
		)
		if err != nil {
			return DomainRegistry{}, err
		}
		if rowVersion != ResearchDomainRegistryVersion {
			return DomainRegistry{}, fmt.Errorf(
				"%w: row %d registry_version %q must equal %q",
				ErrInvalidDomainRegistry,
				rowNumber,
				rowVersion,
				ResearchDomainRegistryVersion,
			)
		}
		domain, err := ParseResearchDomain(record[2])
		if err != nil {
			return DomainRegistry{}, fmt.Errorf(
				"%w: row %d domain: %v",
				ErrInvalidDomainRegistry,
				rowNumber,
				err,
			)
		}
		displayLabel, err := exactRegistryText(
			record[3],
			"display_label",
			rowNumber,
			ErrInvalidDomainRegistry,
		)
		if err != nil {
			return DomainRegistry{}, err
		}
		category, err := exactRegistryText(
			record[4],
			"jcr_category",
			rowNumber,
			ErrInvalidDomainRegistry,
		)
		if err != nil {
			return DomainRegistry{}, err
		}
		articleLevelRequired, err := exactRegistryBoolean(
			record[5],
			"article_level_required",
			rowNumber,
			ErrInvalidDomainRegistry,
		)
		if err != nil {
			return DomainRegistry{}, err
		}

		if rowNumber == 2 {
			registryName = rowName
			registryVersion = rowVersion
		} else if rowName != registryName || rowVersion != registryVersion {
			return DomainRegistry{}, fmt.Errorf(
				"%w: all rows must use one exact registry_name and registry_version",
				ErrInvalidDomainRegistry,
			)
		}
		if existing, found := displayLabels[domain]; found && existing != displayLabel {
			return DomainRegistry{}, fmt.Errorf(
				"%w: row %d display_label %q conflicts with %q for domain %q",
				ErrInvalidDomainRegistry,
				rowNumber,
				displayLabel,
				existing,
				domain,
			)
		}
		displayLabels[domain] = displayLabel
		identity := string(domain) + "\x00" + category
		if previous, duplicate := identities[identity]; duplicate {
			return DomainRegistry{}, fmt.Errorf(
				"%w: duplicate domain/category at rows %d and %d",
				ErrInvalidDomainRegistry,
				previous,
				rowNumber,
			)
		}
		identities[identity] = rowNumber
		rules = append(rules, DomainCategoryRule{
			domain:               domain,
			displayLabel:         displayLabel,
			jcrCategory:          category,
			articleLevelRequired: articleLevelRequired,
		})
	}
	if len(displayLabels) != len(ResearchDomains()) {
		return DomainRegistry{}, fmt.Errorf(
			"%w: Registry must contain exactly three top-level domains",
			ErrInvalidDomainRegistry,
		)
	}
	for _, domain := range ResearchDomains() {
		if _, found := displayLabels[domain]; !found {
			return DomainRegistry{}, fmt.Errorf(
				"%w: Registry is missing domain %q",
				ErrInvalidDomainRegistry,
				domain,
			)
		}
	}
	digest := sha256.Sum256(contents)
	return DomainRegistry{
		name:       registryName,
		version:    registryVersion,
		fileSHA256: hex.EncodeToString(digest[:]),
		rules:      rules,
	}, nil
}

func readExactRegistry(
	source io.Reader,
	maxBytes int64,
	sentinel error,
) ([]byte, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: CSV reader is required", sentinel)
	}
	contents, err := io.ReadAll(io.LimitReader(source, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read CSV: %w", sentinel, err)
	}
	if int64(len(contents)) > maxBytes {
		return nil, fmt.Errorf("%w: input exceeds maximum bytes %d", sentinel, maxBytes)
	}
	return contents, nil
}

func rejectRegistryNUL(
	record []string,
	header []string,
	rowNumber int,
	sentinel error,
) error {
	for index, value := range record {
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf(
				"%w: row %d column %q contains NUL",
				sentinel,
				rowNumber,
				header[index],
			)
		}
	}
	return nil
}

func exactRegistryText(
	value string,
	column string,
	rowNumber int,
	sentinel error,
) (string, error) {
	if value == "" {
		return "", fmt.Errorf(
			"%w: row %d %s is required",
			sentinel,
			rowNumber,
			column,
		)
	}
	if value != strings.TrimSpace(value) {
		return "", fmt.Errorf(
			"%w: row %d %s must be exact and whitespace-trimmed",
			sentinel,
			rowNumber,
			column,
		)
	}
	return value, nil
}

func exactRegistryBoolean(
	value string,
	column string,
	rowNumber int,
	sentinel error,
) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf(
			"%w: row %d %s must be exact true or false",
			sentinel,
			rowNumber,
			column,
		)
	}
}
