package venueenrich

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
)

type PubMedCoverageCounter interface {
	CountCoverage(
		context.Context,
		pubmed.CoverageQuery,
	) (pubmed.CoverageResult, error)
}

type PubMedProbeReceipt struct {
	Domain         string        `json:"domain"`
	SourceOrder    int           `json:"source_order"`
	ISSNs          []string      `json:"issns"`
	Attempted      bool          `json:"attempted"`
	CheckedAt      string        `json:"checked_at"`
	Status         SupportStatus `json:"status"`
	RecordCount    int64         `json:"record_count"`
	ResponseSHA256 string        `json:"response_sha256"`
	Error          string        `json:"error"`
}

func ProbePubMedCoverage(
	ctx context.Context,
	rows []RegistryRow,
	counter PubMedCoverageCounter,
	now func() time.Time,
) ([]RegistryRow, []PubMedProbeReceipt, error) {
	if ctx == nil {
		return nil, nil, errors.New("PubMed coverage context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	probedRows := make([]RegistryRow, len(rows))
	for index, row := range rows {
		if err := row.Validate(); err != nil {
			return nil, nil, fmt.Errorf(
				"row %d: invalid registry row before PubMed probe: %w",
				index+1,
				err,
			)
		}
		row.AllISSNs = canonicalProbeISSNs(row.AllISSNs)
		if row.MatchStatus == MatchStatusResolved && len(row.AllISSNs) == 0 {
			return nil, nil, fmt.Errorf(
				"row %d: resolved registry row requires at least one valid ISSN",
				index+1,
			)
		}
		probedRows[index] = row
	}

	receipts := make([]PubMedProbeReceipt, len(probedRows))
	for index := range probedRows {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		row := probedRows[index]
		row.PubMedSupported = SupportStatusUnknown
		row.PubMedRecordCount = 0
		receipt := PubMedProbeReceipt{
			Domain:      row.Domain,
			SourceOrder: row.SourceOrder,
			ISSNs:       slices.Clone(row.AllISSNs),
			Status:      SupportStatusUnknown,
		}
		if row.MatchStatus != MatchStatusResolved {
			if err := row.Validate(); err != nil {
				return nil, nil, fmt.Errorf(
					"row %d: invalid unprobed PubMed result: %w",
					index+1,
					err,
				)
			}
			probedRows[index] = row
			receipts[index] = receipt
			continue
		}
		if counter == nil {
			return nil, nil, errors.New("PubMed coverage counter is required")
		}
		if now == nil {
			return nil, nil, errors.New("PubMed coverage clock is required")
		}

		checkedAt := now().UTC()
		if checkedAt.IsZero() {
			return nil, nil, errors.New(
				"PubMed coverage clock returned a zero time",
			)
		}
		receipt.Attempted = true
		receipt.CheckedAt = checkedAt.Format(time.RFC3339Nano)

		result, err := counter.CountCoverage(ctx, pubmed.CoverageQuery{
			JournalISSNs: slices.Clone(row.AllISSNs),
		})
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, nil, contextErr
		}
		if err == nil {
			err = validatePubMedCoverageResult(result)
		}
		if err != nil {
			receipt.Error = stableProbeError(err)
			if err := row.Validate(); err != nil {
				return nil, nil, fmt.Errorf(
					"row %d: invalid failed PubMed result: %w",
					index+1,
					err,
				)
			}
			probedRows[index] = row
			receipts[index] = receipt
			continue
		}

		receipt.RecordCount = result.Count
		receipt.ResponseSHA256 = result.ResponseSHA256
		if result.Count > 0 {
			row.PubMedSupported = SupportStatusYes
			row.PubMedRecordCount = result.Count
			receipt.Status = SupportStatusYes
		} else {
			row.PubMedSupported = SupportStatusNo
			receipt.Status = SupportStatusNo
		}
		if err := row.Validate(); err != nil {
			return nil, nil, fmt.Errorf(
				"row %d: invalid successful PubMed result: %w",
				index+1,
				err,
			)
		}
		probedRows[index] = row
		receipts[index] = receipt
	}
	return probedRows, receipts, nil
}

func canonicalProbeISSNs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, raw)
		if err != nil {
			// RegistryRow.Validate locates invalid values before this helper runs.
			continue
		}
		value := parsed.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validatePubMedCoverageResult(result pubmed.CoverageResult) error {
	if result.Count < 0 {
		return errors.New("PubMed coverage record_count must not be negative")
	}
	if len(result.ResponseSHA256) != hex.EncodedLen(sha256Size) ||
		result.ResponseSHA256 != strings.ToLower(result.ResponseSHA256) {
		return errors.New(
			"PubMed coverage response_sha256 must be 64 lowercase hexadecimal characters",
		)
	}
	decoded, err := hex.DecodeString(result.ResponseSHA256)
	if err != nil || len(decoded) != sha256Size {
		return errors.New(
			"PubMed coverage response_sha256 must be 64 lowercase hexadecimal characters",
		)
	}
	return nil
}

const sha256Size = 32

func stableProbeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	replacer := strings.NewReplacer(
		"\r\n", " ",
		"\r", " ",
		"\n", " ",
		"\t", " ",
	)
	return strings.Join(strings.Fields(replacer.Replace(message)), " ")
}
