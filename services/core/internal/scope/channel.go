package scope

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	PreprintRegistryVersion   = "preprint-sources/v1"
	ConferenceRegistryVersion = "conference-venues/v1"
	maxChannelRegistryBytes   = 4 << 20
	maxChannelRegistryRows    = 20_000
)

var (
	ErrInvalidContentChannel     = errors.New("invalid content channel")
	ErrInvalidPreprintRegistry   = errors.New("invalid preprint source Registry CSV")
	ErrInvalidConferenceRegistry = errors.New(
		"invalid conference Registry CSV",
	)

	registryKeyPattern  = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)
	registryHostPattern = regexp.MustCompile(
		`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`,
	)
	preprintRegistryHeader = []string{
		"registry_name",
		"registry_version",
		"source_key",
		"display_name",
		"identifier_scheme",
		"official_host",
		"allowed_domain",
		"lifecycle",
	}
	conferenceRegistryHeader = []string{
		"registry_name",
		"registry_version",
		"provider",
		"series_key",
		"series_name",
		"event_key",
		"event_name",
		"event_year",
		"identifier_scheme",
		"identifier_value",
		"official_host",
		"allowed_domain",
		"lifecycle",
		"reviewed",
	}
)

type ContentChannel string

const (
	ContentChannelJournalPublished     ContentChannel = "journal_published"
	ContentChannelAcceptedEarly        ContentChannel = "accepted_early"
	ContentChannelPreprint             ContentChannel = "preprint"
	ContentChannelConferenceProceeding ContentChannel = "conference_proceeding"
)

func ContentChannels() []ContentChannel {
	return []ContentChannel{
		ContentChannelJournalPublished,
		ContentChannelAcceptedEarly,
		ContentChannelPreprint,
		ContentChannelConferenceProceeding,
	}
}

func ParseContentChannel(value string) (ContentChannel, error) {
	channel := ContentChannel(value)
	switch channel {
	case ContentChannelJournalPublished,
		ContentChannelAcceptedEarly,
		ContentChannelPreprint,
		ContentChannelConferenceProceeding:
		return channel, nil
	default:
		return "", fmt.Errorf("%w %q", ErrInvalidContentChannel, value)
	}
}

type RegistryLifecycle string

const (
	RegistryLifecycleActive  RegistryLifecycle = "active"
	RegistryLifecycleRetired RegistryLifecycle = "retired"
)

func parseRegistryLifecycle(value string) (RegistryLifecycle, error) {
	lifecycle := RegistryLifecycle(value)
	switch lifecycle {
	case RegistryLifecycleActive, RegistryLifecycleRetired:
		return lifecycle, nil
	default:
		return "", fmt.Errorf("invalid Registry lifecycle %q", value)
	}
}

type TrustedPreprintSource struct {
	sourceKey        string
	displayName      string
	identifierScheme string
	officialHost     string
	allowedDomains   []ResearchDomain
	lifecycle        RegistryLifecycle
}

func (source TrustedPreprintSource) SourceKey() string {
	return source.sourceKey
}

func (source TrustedPreprintSource) DisplayName() string {
	return source.displayName
}

func (source TrustedPreprintSource) IdentifierScheme() string {
	return source.identifierScheme
}

func (source TrustedPreprintSource) OfficialHost() string {
	return source.officialHost
}

func (source TrustedPreprintSource) AllowedDomains() []ResearchDomain {
	return slices.Clone(source.allowedDomains)
}

func (source TrustedPreprintSource) Lifecycle() RegistryLifecycle {
	return source.lifecycle
}

func (source TrustedPreprintSource) Channel() ContentChannel {
	return ContentChannelPreprint
}

func (source TrustedPreprintSource) MatchesOfficialURL(value *url.URL) bool {
	return exactOfficialHostMatches(value, source.officialHost)
}

func (source TrustedPreprintSource) allows(domain ResearchDomain) bool {
	return slices.Contains(source.allowedDomains, domain)
}

type PreprintRegistry struct {
	name       string
	version    string
	fileSHA256 string
	ruleCount  int
	sources    []TrustedPreprintSource
}

func (registry PreprintRegistry) Name() string {
	return registry.name
}

func (registry PreprintRegistry) Version() string {
	return registry.version
}

func (registry PreprintRegistry) FileSHA256() string {
	return registry.fileSHA256
}

func (registry PreprintRegistry) SourceCount() int {
	return len(registry.sources)
}

func (registry PreprintRegistry) RuleCount() int {
	return registry.ruleCount
}

func (registry PreprintRegistry) Sources() []TrustedPreprintSource {
	result := make([]TrustedPreprintSource, len(registry.sources))
	for index, source := range registry.sources {
		result[index] = source
		result[index].allowedDomains = slices.Clone(source.allowedDomains)
	}
	return result
}

func (registry PreprintRegistry) Match(
	sourceKey string,
	domain ResearchDomain,
) (TrustedPreprintSource, bool) {
	if _, err := ParseResearchDomain(string(domain)); err != nil {
		return TrustedPreprintSource{}, false
	}
	for _, source := range registry.sources {
		if source.sourceKey == sourceKey &&
			source.lifecycle == RegistryLifecycleActive &&
			source.allows(domain) {
			source.allowedDomains = slices.Clone(source.allowedDomains)
			return source, true
		}
	}
	return TrustedPreprintSource{}, false
}

func ParsePreprintRegistry(source io.Reader) (PreprintRegistry, error) {
	contents, err := readExactRegistry(
		source,
		maxChannelRegistryBytes,
		ErrInvalidPreprintRegistry,
	)
	if err != nil {
		return PreprintRegistry{}, err
	}
	reader := csv.NewReader(bytes.NewReader(contents))
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return PreprintRegistry{}, fmt.Errorf(
			"%w: CSV is empty",
			ErrInvalidPreprintRegistry,
		)
	}
	if err != nil {
		return PreprintRegistry{}, fmt.Errorf(
			"%w: read CSV header: %w",
			ErrInvalidPreprintRegistry,
			err,
		)
	}
	if !slices.Equal(header, preprintRegistryHeader) {
		return PreprintRegistry{}, fmt.Errorf(
			"%w: exact header must be %q",
			ErrInvalidPreprintRegistry,
			strings.Join(preprintRegistryHeader, ","),
		)
	}
	reader.FieldsPerRecord = len(preprintRegistryHeader)

	var registryName, registryVersion string
	sourceIndexes := make(map[string]int)
	rules := make(map[string]int)
	var sources []TrustedPreprintSource
	ruleCount := 0
	for rowNumber := 2; ; rowNumber++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: read CSV row %d: %w",
				ErrInvalidPreprintRegistry,
				rowNumber,
				readErr,
			)
		}
		if ruleCount >= maxChannelRegistryRows {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: row count exceeds %d",
				ErrInvalidPreprintRegistry,
				maxChannelRegistryRows,
			)
		}
		if err := rejectRegistryNUL(
			record,
			preprintRegistryHeader,
			rowNumber,
			ErrInvalidPreprintRegistry,
		); err != nil {
			return PreprintRegistry{}, err
		}
		values := make([]string, 7)
		for index := 0; index < 7; index++ {
			values[index], err = exactRegistryText(
				record[index],
				preprintRegistryHeader[index],
				rowNumber,
				ErrInvalidPreprintRegistry,
			)
			if err != nil {
				return PreprintRegistry{}, err
			}
		}
		rowName, rowVersion := values[0], values[1]
		if rowVersion != PreprintRegistryVersion {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: row %d registry_version %q must equal %q",
				ErrInvalidPreprintRegistry,
				rowNumber,
				rowVersion,
				PreprintRegistryVersion,
			)
		}
		sourceKey, displayName := values[2], values[3]
		identifierScheme, officialHost := values[4], values[5]
		if !registryKeyPattern.MatchString(sourceKey) {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: row %d source_key must be a stable exact identifier",
				ErrInvalidPreprintRegistry,
				rowNumber,
			)
		}
		if !registryKeyPattern.MatchString(identifierScheme) {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: row %d identifier_scheme must be exact",
				ErrInvalidPreprintRegistry,
				rowNumber,
			)
		}
		if err := validateRegistryHost(officialHost); err != nil {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: row %d official_host: %v",
				ErrInvalidPreprintRegistry,
				rowNumber,
				err,
			)
		}
		domain, err := ParseResearchDomain(values[6])
		if err != nil {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: row %d allowed_domain: %v",
				ErrInvalidPreprintRegistry,
				rowNumber,
				err,
			)
		}
		lifecycle, err := parseRegistryLifecycle(record[7])
		if err != nil {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: row %d lifecycle: %v",
				ErrInvalidPreprintRegistry,
				rowNumber,
				err,
			)
		}
		if rowNumber == 2 {
			registryName, registryVersion = rowName, rowVersion
		} else if rowName != registryName || rowVersion != registryVersion {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: all rows must use one exact registry_name and registry_version",
				ErrInvalidPreprintRegistry,
			)
		}
		ruleKey := sourceKey + "\x00" + string(domain)
		if previous, duplicate := rules[ruleKey]; duplicate {
			return PreprintRegistry{}, fmt.Errorf(
				"%w: duplicate source/domain at rows %d and %d",
				ErrInvalidPreprintRegistry,
				previous,
				rowNumber,
			)
		}
		rules[ruleKey] = rowNumber
		if index, found := sourceIndexes[sourceKey]; found {
			existing := &sources[index]
			if existing.displayName != displayName ||
				existing.identifierScheme != identifierScheme ||
				existing.officialHost != officialHost ||
				existing.lifecycle != lifecycle {
				return PreprintRegistry{}, fmt.Errorf(
					"%w: row %d metadata conflicts for source_key %q",
					ErrInvalidPreprintRegistry,
					rowNumber,
					sourceKey,
				)
			}
			existing.allowedDomains = append(existing.allowedDomains, domain)
		} else {
			sourceIndexes[sourceKey] = len(sources)
			sources = append(sources, TrustedPreprintSource{
				sourceKey:        sourceKey,
				displayName:      displayName,
				identifierScheme: identifierScheme,
				officialHost:     officialHost,
				allowedDomains:   []ResearchDomain{domain},
				lifecycle:        lifecycle,
			})
		}
		ruleCount++
	}
	if len(sources) == 0 {
		return PreprintRegistry{}, fmt.Errorf(
			"%w: CSV requires at least one source",
			ErrInvalidPreprintRegistry,
		)
	}
	digest := sha256.Sum256(contents)
	return PreprintRegistry{
		name:       registryName,
		version:    registryVersion,
		fileSHA256: hex.EncodeToString(digest[:]),
		ruleCount:  ruleCount,
		sources:    sources,
	}, nil
}

type ConferenceEntry struct {
	provider         string
	seriesKey        string
	seriesName       string
	eventKey         string
	eventName        string
	eventYear        int
	identifierScheme string
	identifierValue  string
	officialHost     string
	allowedDomains   []ResearchDomain
	lifecycle        RegistryLifecycle
	reviewed         bool
}

func (entry ConferenceEntry) Provider() string {
	return entry.provider
}

func (entry ConferenceEntry) SeriesKey() string {
	return entry.seriesKey
}

func (entry ConferenceEntry) SeriesName() string {
	return entry.seriesName
}

func (entry ConferenceEntry) EventKey() string {
	return entry.eventKey
}

func (entry ConferenceEntry) EventName() string {
	return entry.eventName
}

func (entry ConferenceEntry) EventYear() int {
	return entry.eventYear
}

func (entry ConferenceEntry) IdentifierScheme() string {
	return entry.identifierScheme
}

func (entry ConferenceEntry) IdentifierValue() string {
	return entry.identifierValue
}

func (entry ConferenceEntry) OfficialHost() string {
	return entry.officialHost
}

func (entry ConferenceEntry) AllowedDomains() []ResearchDomain {
	return slices.Clone(entry.allowedDomains)
}

func (entry ConferenceEntry) Lifecycle() RegistryLifecycle {
	return entry.lifecycle
}

func (entry ConferenceEntry) Reviewed() bool {
	return entry.reviewed
}

func (entry ConferenceEntry) Channel() ContentChannel {
	return ContentChannelConferenceProceeding
}

func (entry ConferenceEntry) MatchesOfficialURL(value *url.URL) bool {
	return exactOfficialHostMatches(value, entry.officialHost)
}

func (entry ConferenceEntry) allows(domain ResearchDomain) bool {
	return slices.Contains(entry.allowedDomains, domain)
}

type ConferenceRegistry struct {
	name       string
	version    string
	fileSHA256 string
	ruleCount  int
	entries    []ConferenceEntry
}

func (registry ConferenceRegistry) Name() string {
	return registry.name
}

func (registry ConferenceRegistry) Version() string {
	return registry.version
}

func (registry ConferenceRegistry) FileSHA256() string {
	return registry.fileSHA256
}

func (registry ConferenceRegistry) SeriesCount() int {
	series := make(map[string]struct{})
	for _, entry := range registry.entries {
		series[entry.provider+"\x00"+entry.seriesKey] = struct{}{}
	}
	return len(series)
}

func (registry ConferenceRegistry) EventCount() int {
	return len(registry.entries)
}

func (registry ConferenceRegistry) RuleCount() int {
	return registry.ruleCount
}

func (registry ConferenceRegistry) Entries() []ConferenceEntry {
	result := make([]ConferenceEntry, len(registry.entries))
	for index, entry := range registry.entries {
		result[index] = entry
		result[index].allowedDomains = slices.Clone(entry.allowedDomains)
	}
	return result
}

func (registry ConferenceRegistry) Match(
	provider string,
	seriesKey string,
	eventKey string,
	domain ResearchDomain,
) (ConferenceEntry, bool) {
	if _, err := ParseResearchDomain(string(domain)); err != nil {
		return ConferenceEntry{}, false
	}
	for _, entry := range registry.entries {
		if entry.provider == provider &&
			entry.seriesKey == seriesKey &&
			entry.eventKey == eventKey &&
			entry.lifecycle == RegistryLifecycleActive &&
			entry.allows(domain) {
			entry.allowedDomains = slices.Clone(entry.allowedDomains)
			return entry, true
		}
	}
	return ConferenceEntry{}, false
}

func ParseConferenceRegistry(source io.Reader) (ConferenceRegistry, error) {
	contents, err := readExactRegistry(
		source,
		maxChannelRegistryBytes,
		ErrInvalidConferenceRegistry,
	)
	if err != nil {
		return ConferenceRegistry{}, err
	}
	reader := csv.NewReader(bytes.NewReader(contents))
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return ConferenceRegistry{}, fmt.Errorf(
			"%w: CSV is empty",
			ErrInvalidConferenceRegistry,
		)
	}
	if err != nil {
		return ConferenceRegistry{}, fmt.Errorf(
			"%w: read CSV header: %w",
			ErrInvalidConferenceRegistry,
			err,
		)
	}
	if !slices.Equal(header, conferenceRegistryHeader) {
		return ConferenceRegistry{}, fmt.Errorf(
			"%w: exact header must be %q",
			ErrInvalidConferenceRegistry,
			strings.Join(conferenceRegistryHeader, ","),
		)
	}
	reader.FieldsPerRecord = len(conferenceRegistryHeader)

	var registryName, registryVersion string
	entryIndexes := make(map[string]int)
	rules := make(map[string]int)
	var entries []ConferenceEntry
	ruleCount := 0
	for rowNumber := 2; ; rowNumber++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: read CSV row %d: %w",
				ErrInvalidConferenceRegistry,
				rowNumber,
				readErr,
			)
		}
		if ruleCount >= maxChannelRegistryRows {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: row count exceeds %d",
				ErrInvalidConferenceRegistry,
				maxChannelRegistryRows,
			)
		}
		if err := rejectRegistryNUL(
			record,
			conferenceRegistryHeader,
			rowNumber,
			ErrInvalidConferenceRegistry,
		); err != nil {
			return ConferenceRegistry{}, err
		}
		values := make([]string, 13)
		for index := 0; index < 13; index++ {
			if index == 12 {
				continue
			}
			values[index], err = exactRegistryText(
				record[index],
				conferenceRegistryHeader[index],
				rowNumber,
				ErrInvalidConferenceRegistry,
			)
			if err != nil {
				return ConferenceRegistry{}, err
			}
		}
		rowName, rowVersion := values[0], values[1]
		if rowVersion != ConferenceRegistryVersion {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: row %d registry_version %q must equal %q",
				ErrInvalidConferenceRegistry,
				rowNumber,
				rowVersion,
				ConferenceRegistryVersion,
			)
		}
		provider, seriesKey, seriesName := values[2], values[3], values[4]
		eventKey, eventName := values[5], values[6]
		eventYear, err := strconv.Atoi(values[7])
		if err != nil || eventYear < 1900 || eventYear > 3000 {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: row %d event_year must be an exact year",
				ErrInvalidConferenceRegistry,
				rowNumber,
			)
		}
		identifierScheme, identifierValue := values[8], values[9]
		officialHost := values[10]
		if provider != "ieee" &&
			provider != "acm" &&
			provider != "reviewed_allowlist" {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: row %d provider %q is not authorized",
				ErrInvalidConferenceRegistry,
				rowNumber,
				provider,
			)
		}
		for column, value := range map[string]string{
			"series_key":        seriesKey,
			"event_key":         eventKey,
			"identifier_scheme": identifierScheme,
		} {
			if !registryKeyPattern.MatchString(value) {
				return ConferenceRegistry{}, fmt.Errorf(
					"%w: row %d %s must be a stable exact identifier",
					ErrInvalidConferenceRegistry,
					rowNumber,
					column,
				)
			}
		}
		if err := validateRegistryHost(officialHost); err != nil {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: row %d official_host: %v",
				ErrInvalidConferenceRegistry,
				rowNumber,
				err,
			)
		}
		domain, err := ParseResearchDomain(values[11])
		if err != nil {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: row %d allowed_domain: %v",
				ErrInvalidConferenceRegistry,
				rowNumber,
				err,
			)
		}
		lifecycle, err := parseRegistryLifecycle(record[12])
		if err != nil {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: row %d lifecycle: %v",
				ErrInvalidConferenceRegistry,
				rowNumber,
				err,
			)
		}
		reviewed, err := exactRegistryBoolean(
			record[13],
			"reviewed",
			rowNumber,
			ErrInvalidConferenceRegistry,
		)
		if err != nil {
			return ConferenceRegistry{}, err
		}
		if provider == "reviewed_allowlist" && !reviewed {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: row %d reviewed_allowlist provider must be reviewed",
				ErrInvalidConferenceRegistry,
				rowNumber,
			)
		}
		if provider != "reviewed_allowlist" && reviewed {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: row %d IEEE/ACM entries must use provider authority, not reviewed allowlist",
				ErrInvalidConferenceRegistry,
				rowNumber,
			)
		}
		if rowNumber == 2 {
			registryName, registryVersion = rowName, rowVersion
		} else if rowName != registryName || rowVersion != registryVersion {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: all rows must use one exact registry_name and registry_version",
				ErrInvalidConferenceRegistry,
			)
		}
		entryKey := provider + "\x00" + seriesKey + "\x00" + eventKey
		ruleKey := entryKey + "\x00" + string(domain)
		if previous, duplicate := rules[ruleKey]; duplicate {
			return ConferenceRegistry{}, fmt.Errorf(
				"%w: duplicate conference/domain at rows %d and %d",
				ErrInvalidConferenceRegistry,
				previous,
				rowNumber,
			)
		}
		rules[ruleKey] = rowNumber
		if index, found := entryIndexes[entryKey]; found {
			existing := &entries[index]
			if existing.seriesName != seriesName ||
				existing.eventName != eventName ||
				existing.eventYear != eventYear ||
				existing.identifierScheme != identifierScheme ||
				existing.identifierValue != identifierValue ||
				existing.officialHost != officialHost ||
				existing.lifecycle != lifecycle ||
				existing.reviewed != reviewed {
				return ConferenceRegistry{}, fmt.Errorf(
					"%w: row %d metadata conflicts for event %q",
					ErrInvalidConferenceRegistry,
					rowNumber,
					eventKey,
				)
			}
			existing.allowedDomains = append(existing.allowedDomains, domain)
		} else {
			entryIndexes[entryKey] = len(entries)
			entries = append(entries, ConferenceEntry{
				provider:         provider,
				seriesKey:        seriesKey,
				seriesName:       seriesName,
				eventKey:         eventKey,
				eventName:        eventName,
				eventYear:        eventYear,
				identifierScheme: identifierScheme,
				identifierValue:  identifierValue,
				officialHost:     officialHost,
				allowedDomains:   []ResearchDomain{domain},
				lifecycle:        lifecycle,
				reviewed:         reviewed,
			})
		}
		ruleCount++
	}
	if len(entries) == 0 {
		return ConferenceRegistry{}, fmt.Errorf(
			"%w: CSV requires at least one conference event",
			ErrInvalidConferenceRegistry,
		)
	}
	digest := sha256.Sum256(contents)
	return ConferenceRegistry{
		name:       registryName,
		version:    registryVersion,
		fileSHA256: hex.EncodeToString(digest[:]),
		ruleCount:  ruleCount,
		entries:    entries,
	}, nil
}

func validateRegistryHost(host string) error {
	if !registryHostPattern.MatchString(host) ||
		strings.ContainsAny(host, "/:@") ||
		host != strings.ToLower(host) ||
		!strings.Contains(host, ".") {
		return errors.New("must be an exact lowercase host without scheme or path")
	}
	return nil
}

func exactOfficialHostMatches(value *url.URL, host string) bool {
	if value == nil || value.Scheme != "https" || value.User != nil {
		return false
	}
	return value.Hostname() == host && value.Port() == ""
}
