package venueenrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/nlmcatalog"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
)

type NLMCatalogResolver interface {
	Resolve(
		context.Context,
		nlmcatalog.Query,
	) (nlmcatalog.Result, error)
}

type NLMCatalogResolverFactory func() (NLMCatalogResolver, error)

type NLMCatalogIdentityReceipt struct {
	Domain                  string   `json:"domain"`
	SourceOrder             int      `json:"source_order"`
	SourceJournalName       string   `json:"source_journal_name"`
	NormalizedSourceTitle   string   `json:"normalized_source_title"`
	NLMUID                  string   `json:"nlm_uid"`
	NLMUniqueID             string   `json:"nlm_unique_id"`
	NLMTitle                string   `json:"nlm_title"`
	NLMTitleMainSort        string   `json:"nlm_title_main_sort"`
	DateRevised             string   `json:"date_revised"`
	EndYear                 string   `json:"end_year"`
	CurrentIndexingStatus   string   `json:"current_indexing_status"`
	OriginalPrintISSN       string   `json:"original_print_issn"`
	OriginalElectronicISSN  string   `json:"original_eissn"`
	OriginalISSNs           []string `json:"original_issns"`
	AuthorityPrintISSN      string   `json:"authority_print_issn"`
	AuthorityElectronicISSN string   `json:"authority_eissn"`
	AuthorityISSNs          []string `json:"authority_issns"`
	ESearchSHA256           string   `json:"esearch_sha256"`
	ESummarySHA256          string   `json:"esummary_sha256"`
}

func ReconcileConflictingISSNsWithNLMCatalog(
	ctx context.Context,
	rows []RegistryRow,
	newResolver NLMCatalogResolverFactory,
) ([]RegistryRow, []NLMCatalogIdentityReceipt, error) {
	if ctx == nil {
		return nil, nil, errors.New(
			"NLM Catalog reconciliation context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	reconciled := cloneRegistryRows(rows)
	identities, err := validateResolvedRegistryIdentities(reconciled)
	if err != nil {
		return nil, nil, err
	}
	participants := conflictingResolvedParticipants(identities)
	if len(participants) == 0 {
		return reconciled, []NLMCatalogIdentityReceipt{}, nil
	}
	groups, err := buildConflictTitleGroups(
		reconciled,
		identities,
		participants,
	)
	if err != nil {
		return nil, nil, err
	}
	if newResolver == nil {
		return nil, nil, errors.New(
			"NLM Catalog resolver factory is required for conflicting resolved identities",
		)
	}
	resolver, err := newResolver()
	if err != nil {
		return nil, nil, fmt.Errorf(
			"create NLM Catalog resolver for conflicting identities: %w",
			err,
		)
	}
	if resolver == nil {
		return nil, nil, errors.New(
			"NLM Catalog resolver factory returned nil resolver",
		)
	}

	receipts := make([]NLMCatalogIdentityReceipt, 0, len(participants))
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		result, err := resolver.Resolve(ctx, nlmcatalog.Query{
			Title:          reconciled[group.rowIndexes[0]].SourceJournalName,
			CandidateISSNs: slices.Clone(group.candidateISSNs),
		})
		if err != nil {
			return nil, nil, fmt.Errorf(
				"resolve NLM Catalog identity for %q: %w",
				reconciled[group.rowIndexes[0]].SourceJournalName,
				err,
			)
		}
		if err := result.Validate(); err != nil {
			return nil, nil, fmt.Errorf(
				"validate NLM Catalog identity for %q: %w",
				reconciled[group.rowIndexes[0]].SourceJournalName,
				err,
			)
		}
		if result.TitleMainSort != group.normalizedTitle {
			return nil, nil, fmt.Errorf(
				"NLM Catalog titlemainsort %q does not exactly equal normalized source title %q",
				result.TitleMainSort,
				group.normalizedTitle,
			)
		}
		if !nonEmptySubset(result.AllISSNs, group.candidateISSNs) {
			return nil, nil, fmt.Errorf(
				"NLM Catalog ISSNs %v must be a non-empty subset of Crossref candidate ISSNs %v for %q",
				result.AllISSNs,
				group.candidateISSNs,
				reconciled[group.rowIndexes[0]].SourceJournalName,
			)
		}
		nlmTitle, err := result.MainTitle()
		if err != nil {
			return nil, nil, fmt.Errorf(
				"select exact NLM Catalog main title for %q: %w",
				reconciled[group.rowIndexes[0]].SourceJournalName,
				err,
			)
		}

		for _, rowIndex := range group.rowIndexes {
			original := reconciled[rowIndex]
			corrected := original
			corrected.PrintISSN = result.PrintISSN
			corrected.EISSN = result.ElectronicISSN
			corrected.AllISSNs = slices.Clone(result.AllISSNs)
			if err := corrected.Validate(); err != nil {
				return nil, nil, fmt.Errorf(
					"row %d: NLM-corrected registry row is invalid: %w",
					rowIndex+1,
					err,
				)
			}
			reconciled[rowIndex] = corrected
			receipt := NLMCatalogIdentityReceipt{
				Domain:                  original.Domain,
				SourceOrder:             original.SourceOrder,
				SourceJournalName:       original.SourceJournalName,
				NormalizedSourceTitle:   group.normalizedTitle,
				NLMUID:                  result.UID,
				NLMUniqueID:             result.NLMUniqueID,
				NLMTitle:                nlmTitle,
				NLMTitleMainSort:        result.TitleMainSort,
				DateRevised:             result.DateRevised,
				EndYear:                 result.EndYear,
				CurrentIndexingStatus:   result.CurrentIndexingStatus,
				OriginalPrintISSN:       original.PrintISSN,
				OriginalElectronicISSN:  original.EISSN,
				OriginalISSNs:           slices.Clone(group.candidateISSNs),
				AuthorityPrintISSN:      result.PrintISSN,
				AuthorityElectronicISSN: result.ElectronicISSN,
				AuthorityISSNs:          slices.Clone(result.AllISSNs),
				ESearchSHA256:           result.ESearchSHA256,
				ESummarySHA256:          result.ESummarySHA256,
			}
			if err := receipt.Validate(); err != nil {
				return nil, nil, fmt.Errorf(
					"row %d: invalid NLM Catalog receipt: %w",
					rowIndex+1,
					err,
				)
			}
			receipts = append(receipts, receipt)
		}
	}

	finalIdentities, err := validateResolvedRegistryIdentities(reconciled)
	if err != nil {
		return nil, nil, err
	}
	finalParticipants := conflictingResolvedParticipants(finalIdentities)
	if len(finalParticipants) != 0 {
		first := finalParticipants[0]
		return nil, nil, fmt.Errorf(
			"NLM Catalog reconciliation left a non-identical resolved ISSN intersection at row %d",
			first+1,
		)
	}
	return reconciled, receipts, nil
}

func (receipt NLMCatalogIdentityReceipt) Validate() error {
	if receipt.Domain == "" ||
		receipt.Domain != strings.TrimSpace(receipt.Domain) {
		return errors.New("NLM receipt domain must be non-empty and trimmed")
	}
	if receipt.SourceOrder <= 0 {
		return errors.New("NLM receipt source_order must be positive")
	}
	if receipt.SourceJournalName == "" ||
		receipt.SourceJournalName != strings.TrimSpace(
			receipt.SourceJournalName,
		) {
		return errors.New(
			"NLM receipt source_journal_name must be non-empty and trimmed",
		)
	}
	normalized, err := NormalizeTitle(receipt.SourceJournalName)
	if err != nil || normalized == "" {
		return errors.New(
			"NLM receipt source_journal_name cannot be normalized",
		)
	}
	if receipt.NormalizedSourceTitle != normalized ||
		receipt.NLMTitleMainSort != normalized {
		return errors.New(
			"NLM receipt normalized source title and titlemainsort must exactly match",
		)
	}
	if receipt.NLMUID == "" ||
		receipt.NLMUID != receipt.NLMUniqueID {
		return errors.New(
			"NLM receipt UID and NLM unique ID must be equal and non-empty",
		)
	}
	for _, current := range receipt.NLMUID {
		if current < '0' || current > '9' {
			return errors.New("NLM receipt UID must contain only ASCII digits")
		}
	}
	if receipt.NLMUID[0] == '0' {
		return errors.New("NLM receipt UID must use canonical positive form")
	}
	if receipt.NLMTitle == "" ||
		receipt.NLMTitle != strings.TrimSpace(receipt.NLMTitle) {
		return errors.New("NLM receipt title must be non-empty and trimmed")
	}
	revised, err := time.Parse("2006-01-02", receipt.DateRevised)
	if err != nil ||
		revised.Format("2006-01-02") != receipt.DateRevised {
		return errors.New(
			"NLM receipt date_revised must be a valid YYYY-MM-DD date",
		)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "end_year", value: receipt.EndYear},
		{name: "current_indexing_status", value: receipt.CurrentIndexingStatus},
	} {
		if field.value != strings.TrimSpace(field.value) {
			return fmt.Errorf("NLM receipt %s must be trimmed", field.name)
		}
	}
	if err := validateReceiptISSNSet(
		"original_issns",
		receipt.OriginalISSNs,
	); err != nil {
		return err
	}
	if err := validateReceiptISSNSet(
		"authority_issns",
		receipt.AuthorityISSNs,
	); err != nil {
		return err
	}
	if !nonEmptySubset(receipt.AuthorityISSNs, receipt.OriginalISSNs) {
		return errors.New(
			"NLM receipt authority_issns must be a non-empty subset of original_issns",
		)
	}
	for _, field := range []struct {
		name  string
		role  venue.ISSNRole
		value string
		set   []string
	}{
		{
			name:  "original_print_issn",
			role:  venue.ISSNRolePrint,
			value: receipt.OriginalPrintISSN,
			set:   receipt.OriginalISSNs,
		},
		{
			name:  "original_eissn",
			role:  venue.ISSNRoleElectronic,
			value: receipt.OriginalElectronicISSN,
			set:   receipt.OriginalISSNs,
		},
		{
			name:  "authority_print_issn",
			role:  venue.ISSNRolePrint,
			value: receipt.AuthorityPrintISSN,
			set:   receipt.AuthorityISSNs,
		},
		{
			name:  "authority_eissn",
			role:  venue.ISSNRoleElectronic,
			value: receipt.AuthorityElectronicISSN,
			set:   receipt.AuthorityISSNs,
		},
	} {
		if field.value == "" {
			continue
		}
		parsed, err := venue.ParseISSN(field.role, field.value)
		if err != nil || parsed.String() != field.value {
			return fmt.Errorf("NLM receipt %s is invalid", field.name)
		}
		if !slices.Contains(field.set, field.value) {
			return fmt.Errorf(
				"NLM receipt %s is not present in its exact ISSN set",
				field.name,
			)
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "esearch_sha256", value: receipt.ESearchSHA256},
		{name: "esummary_sha256", value: receipt.ESummarySHA256},
	} {
		if !validNLMReceiptSHA256(field.value) {
			return fmt.Errorf(
				"NLM receipt %s must be 64 lowercase hexadecimal characters",
				field.name,
			)
		}
	}
	return nil
}

type resolvedRegistryIdentity struct {
	rowIndex int
	issns    []string
}

func validateResolvedRegistryIdentities(
	rows []RegistryRow,
) ([]resolvedRegistryIdentity, error) {
	identities := make([]resolvedRegistryIdentity, 0, len(rows))
	for index, row := range rows {
		if err := row.Validate(); err != nil {
			return nil, fmt.Errorf(
				"row %d: invalid registry row before NLM reconciliation: %w",
				index+1,
				err,
			)
		}
		if row.MatchStatus != MatchStatusResolved {
			continue
		}
		issns, err := canonicalIdentityISSNs(row.AllISSNs)
		if err != nil {
			return nil, fmt.Errorf("row %d: all_issns: %w", index+1, err)
		}
		identities = append(identities, resolvedRegistryIdentity{
			rowIndex: index,
			issns:    issns,
		})
	}
	return identities, nil
}

func canonicalIdentityISSNs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("must be non-empty")
	}
	canonical := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, raw)
		if err != nil {
			return nil, fmt.Errorf("invalid ISSN %q", raw)
		}
		value := parsed.String()
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		canonical = append(canonical, value)
	}
	slices.Sort(canonical)
	return canonical, nil
}

func conflictingResolvedParticipants(
	identities []resolvedRegistryIdentity,
) []int {
	participantSet := make(map[int]struct{})
	for left := range identities {
		for right := left + 1; right < len(identities); right++ {
			if slices.Equal(
				identities[left].issns,
				identities[right].issns,
			) {
				continue
			}
			if intersects(
				identities[left].issns,
				identities[right].issns,
			) {
				participantSet[identities[left].rowIndex] = struct{}{}
				participantSet[identities[right].rowIndex] = struct{}{}
			}
		}
	}
	participants := make([]int, 0, len(participantSet))
	for rowIndex := range participantSet {
		participants = append(participants, rowIndex)
	}
	slices.Sort(participants)
	return participants
}

type conflictTitleGroup struct {
	normalizedTitle string
	candidateISSNs  []string
	rowIndexes      []int
}

func buildConflictTitleGroups(
	rows []RegistryRow,
	identities []resolvedRegistryIdentity,
	participants []int,
) ([]conflictTitleGroup, error) {
	identityByRow := make(
		map[int][]string,
		len(identities),
	)
	for _, identity := range identities {
		identityByRow[identity.rowIndex] = identity.issns
	}
	groups := make([]conflictTitleGroup, 0, len(participants))
	groupByTitle := make(map[string]int, len(participants))
	for _, rowIndex := range participants {
		normalized, err := NormalizeTitle(rows[rowIndex].SourceJournalName)
		if err != nil || normalized == "" {
			return nil, fmt.Errorf(
				"row %d: normalize conflicting source title: %w",
				rowIndex+1,
				err,
			)
		}
		candidateISSNs := identityByRow[rowIndex]
		groupIndex, exists := groupByTitle[normalized]
		if !exists {
			groupByTitle[normalized] = len(groups)
			groups = append(groups, conflictTitleGroup{
				normalizedTitle: normalized,
				candidateISSNs:  slices.Clone(candidateISSNs),
				rowIndexes:      []int{rowIndex},
			})
			continue
		}
		group := &groups[groupIndex]
		if !slices.Equal(group.candidateISSNs, candidateISSNs) {
			return nil, fmt.Errorf(
				"conflicting normalized title %q has inconsistent Crossref ISSN candidate sets",
				normalized,
			)
		}
		group.rowIndexes = append(group.rowIndexes, rowIndex)
	}
	return groups, nil
}

func validateReceiptISSNSet(field string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s must be non-empty", field)
	}
	if !slices.IsSorted(values) {
		return fmt.Errorf("%s must use stable sorted order", field)
	}
	for index, raw := range values {
		if index > 0 && raw == values[index-1] {
			return fmt.Errorf("%s contains duplicate ISSN %q", field, raw)
		}
		parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, raw)
		if err != nil || parsed.String() != raw {
			return fmt.Errorf("%s contains invalid canonical ISSN %q", field, raw)
		}
	}
	return nil
}

func nonEmptySubset(subset, superset []string) bool {
	if len(subset) == 0 {
		return false
	}
	for _, value := range subset {
		if !slices.Contains(superset, value) {
			return false
		}
	}
	return true
}

func intersects(left, right []string) bool {
	for _, value := range left {
		if slices.Contains(right, value) {
			return true
		}
	}
	return false
}

func cloneRegistryRows(rows []RegistryRow) []RegistryRow {
	cloned := make([]RegistryRow, len(rows))
	for index, row := range rows {
		row.AllISSNs = slices.Clone(row.AllISSNs)
		row.SecondarySources = slices.Clone(row.SecondarySources)
		cloned[index] = row
	}
	return cloned
}

func validNLMReceiptSHA256(value string) bool {
	if len(value) != sha256.Size*2 ||
		value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
