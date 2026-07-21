package venueenrich

import (
	"errors"
	"fmt"
	"strings"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
)

type MatchStatus string

const (
	MatchStatusResolved   MatchStatus = "resolved"
	MatchStatusAmbiguous  MatchStatus = "ambiguous"
	MatchStatusUnresolved MatchStatus = "unresolved"
)

func (status MatchStatus) Valid() bool {
	switch status {
	case MatchStatusResolved, MatchStatusAmbiguous, MatchStatusUnresolved:
		return true
	default:
		return false
	}
}

type SupportStatus string

const (
	SupportStatusYes     SupportStatus = "yes"
	SupportStatusNo      SupportStatus = "no"
	SupportStatusUnknown SupportStatus = "unknown"
)

func (status SupportStatus) Valid() bool {
	switch status {
	case SupportStatusYes, SupportStatusNo, SupportStatusUnknown:
		return true
	default:
		return false
	}
}

type SourceRow struct {
	Domain            string
	SourceOrder       int
	SourceJournalName string
	ImpactFactor      string
	JCRValue          string
	CASSValue         string
	SourceURL         string
}

func (row SourceRow) Validate() error {
	if err := validateRequiredTrimmed("domain", row.Domain); err != nil {
		return err
	}
	if row.SourceOrder <= 0 {
		return errors.New("source_order must be positive")
	}
	if err := validateRequiredTrimmed(
		"source_journal_name",
		row.SourceJournalName,
	); err != nil {
		return err
	}
	return validateRequiredTrimmed("source_url", row.SourceURL)
}

type RegistryRow struct {
	SourceRow

	ISSNL     string
	PrintISSN string
	EISSN     string
	AllISSNs  []string

	CrossrefTitle          string
	CrossrefPublisher      string
	CrossrefTotalDOIs      int64
	CrossrefSupported      SupportStatus
	OpenAlexSourceID       string
	OpenAlexSupported      SupportStatus
	PubMedSupported        SupportStatus
	PubMedRecordCount      int64
	PrimaryDiscoverySource string
	SecondarySources       []string
	MatchMethod            string
	MatchStatus            MatchStatus
	VerificationStatus     string
}

func (row RegistryRow) Validate() error {
	if err := row.SourceRow.Validate(); err != nil {
		return fmt.Errorf("source row: %w", err)
	}

	for _, candidate := range []struct {
		field string
		role  venue.ISSNRole
		value string
	}{
		{field: "issn_l", role: venue.ISSNRoleLinking, value: row.ISSNL},
		{field: "print_issn", role: venue.ISSNRolePrint, value: row.PrintISSN},
		{field: "eissn", role: venue.ISSNRoleElectronic, value: row.EISSN},
	} {
		if err := validateOptionalISSN(
			candidate.field,
			candidate.role,
			candidate.value,
		); err != nil {
			return err
		}
	}
	for index, raw := range row.AllISSNs {
		if err := validateRequiredGenericISSN(
			fmt.Sprintf("all_issns[%d]", index),
			raw,
		); err != nil {
			return err
		}
	}
	if !row.MatchStatus.Valid() {
		return fmt.Errorf("invalid match_status %q", row.MatchStatus)
	}
	for _, candidate := range []struct {
		field  string
		status SupportStatus
	}{
		{field: "crossref_supported", status: row.CrossrefSupported},
		{field: "openalex_supported", status: row.OpenAlexSupported},
		{field: "pubmed_supported", status: row.PubMedSupported},
	} {
		if !candidate.status.Valid() {
			return fmt.Errorf("invalid %s %q", candidate.field, candidate.status)
		}
	}
	if err := validateCrossrefEvidence(row); err != nil {
		return err
	}
	if err := validateOpenAlexEvidence(row); err != nil {
		return err
	}
	return validatePubMedEvidence(row)
}

func validateRequiredTrimmed(field, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty and trimmed", field)
	}
	return nil
}

func validateOptionalTrimmed(field, value string) error {
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be trimmed", field)
	}
	return nil
}

func validateOptionalISSN(
	field string,
	role venue.ISSNRole,
	raw string,
) error {
	if raw == "" {
		return nil
	}
	parsed, err := venue.ParseISSN(role, raw)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if parsed.String() != raw {
		return fmt.Errorf("%s must use canonical ISSN form %q", field, parsed.String())
	}
	return nil
}

func validateRequiredGenericISSN(field, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, raw)
	if err != nil {
		return fmt.Errorf("%s: invalid ISSN %q", field, raw)
	}
	if parsed.String() != raw {
		return fmt.Errorf("%s must use canonical ISSN form %q", field, parsed.String())
	}
	return nil
}

func validateCrossrefEvidence(row RegistryRow) error {
	if row.CrossrefTotalDOIs < 0 {
		return errors.New("crossref_total_dois must not be negative")
	}
	if row.CrossrefSupported == SupportStatusYes {
		if err := validateRequiredTrimmed(
			"crossref_title",
			row.CrossrefTitle,
		); err != nil {
			return err
		}
		return validateOptionalTrimmed(
			"crossref_publisher",
			row.CrossrefPublisher,
		)
	}
	if row.CrossrefTitle != "" ||
		row.CrossrefPublisher != "" ||
		row.CrossrefTotalDOIs != 0 {
		return fmt.Errorf(
			"crossref_supported %q requires empty catalog fields and zero crossref_total_dois",
			row.CrossrefSupported,
		)
	}
	return nil
}

func validateOpenAlexEvidence(row RegistryRow) error {
	if row.OpenAlexSupported == SupportStatusYes {
		return validateRequiredTrimmed(
			"openalex_source_id",
			row.OpenAlexSourceID,
		)
	}
	if row.OpenAlexSourceID != "" {
		return fmt.Errorf(
			"openalex_supported %q requires an empty openalex_source_id",
			row.OpenAlexSupported,
		)
	}
	return nil
}

func validatePubMedEvidence(row RegistryRow) error {
	if row.PubMedRecordCount < 0 {
		return errors.New("pubmed_record_count must not be negative")
	}
	switch row.PubMedSupported {
	case SupportStatusYes:
		if row.PubMedRecordCount == 0 {
			return errors.New("pubmed_record_count must be positive when pubmed_supported is yes")
		}
	case SupportStatusNo, SupportStatusUnknown:
		if row.PubMedRecordCount != 0 {
			return fmt.Errorf(
				"pubmed_record_count must be zero when pubmed_supported is %s",
				row.PubMedSupported,
			)
		}
	}
	return nil
}
