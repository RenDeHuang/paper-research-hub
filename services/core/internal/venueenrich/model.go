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
		if raw == "" {
			return fmt.Errorf("all_issns[%d] must not be empty", index)
		}
		if err := validateOptionalISSN(
			fmt.Sprintf("all_issns[%d]", index),
			venue.ISSNRoleLinking,
			raw,
		); err != nil {
			return err
		}
	}
	if !row.MatchStatus.Valid() {
		return fmt.Errorf("invalid match_status %q", row.MatchStatus)
	}
	return nil
}

func validateRequiredTrimmed(field, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty and trimmed", field)
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
