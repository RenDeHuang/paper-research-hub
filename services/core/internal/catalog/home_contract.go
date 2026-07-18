package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const homeSnapshotSchemaVersion = "home-snapshot/v2"

var (
	homeTaxonomySlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	homeSHA256Pattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type homeValueValidator func(string, json.RawMessage) error

type homeAnalysisWindowDeclaration struct {
	known bool
	days  int64
}

func validateHomeSnapshotPayload(payload json.RawMessage) error {
	home, err := decodeHomeObject(
		"HomeResponse",
		payload,
		[]string{
			"active_journals",
			"catalog_generation",
			"citation_momentum",
			"coverage",
			"entity_momentum",
			"evidence_gaps",
			"generated_at",
			"latest_papers",
			"publication_updates",
			"research_opportunities",
			"snapshot_schema",
			"scope",
			"subject_momentum",
		},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeCollection(
		"HomeResponse.active_journals",
		home["active_journals"],
		homeAnalysisWindowDeclaration{known: true, days: 7},
		false,
		validateHomeJournalActivityItem,
	); err != nil {
		return err
	}
	if err := validateHomeUUID(
		"HomeResponse.catalog_generation",
		home["catalog_generation"],
	); err != nil {
		return err
	}
	if err := validateHomeCollection(
		"HomeResponse.citation_momentum",
		home["citation_momentum"],
		homeAnalysisWindowDeclaration{known: true, days: 30},
		false,
		validateHomeCitationMomentumItem,
	); err != nil {
		return err
	}
	if err := validateHomeCoverage(
		"HomeResponse.coverage",
		home["coverage"],
	); err != nil {
		return err
	}
	if err := validateHomeCollection(
		"HomeResponse.entity_momentum",
		home["entity_momentum"],
		homeAnalysisWindowDeclaration{known: true, days: 7},
		false,
		validateHomeEntityMomentumItem,
	); err != nil {
		return err
	}
	if err := validateHomeStringArray(
		"HomeResponse.evidence_gaps",
		home["evidence_gaps"],
		0,
		1,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeDateTime(
		"HomeResponse.generated_at",
		home["generated_at"],
	); err != nil {
		return err
	}
	if err := validateHomeCollection(
		"HomeResponse.latest_papers",
		home["latest_papers"],
		homeAnalysisWindowDeclaration{known: true, days: 7},
		true,
		validateHomePaperSummary,
	); err != nil {
		return err
	}
	if err := validateHomePublicationUpdates(
		"HomeResponse.publication_updates",
		home["publication_updates"],
	); err != nil {
		return err
	}
	if err := validateHomeCollection(
		"HomeResponse.research_opportunities",
		home["research_opportunities"],
		homeAnalysisWindowDeclaration{},
		false,
		validateHomeResearchOpportunity,
	); err != nil {
		return err
	}
	if err := validateHomeStringConst(
		"HomeResponse.snapshot_schema",
		home["snapshot_schema"],
		homeSnapshotSchemaVersion,
	); err != nil {
		return err
	}
	if err := validateHomeScope("HomeResponse.scope", home["scope"]); err != nil {
		return err
	}
	if err := validateHomeCollection(
		"HomeResponse.subject_momentum",
		home["subject_momentum"],
		homeAnalysisWindowDeclaration{known: true, days: 7},
		false,
		validateHomeSubjectTrendEstimate,
	); err != nil {
		return err
	}
	return nil
}

func validateHomeCollection(
	path string,
	raw json.RawMessage,
	window homeAnalysisWindowDeclaration,
	withPagination bool,
	itemValidator homeValueValidator,
) error {
	required := []string{"analysis", "items"}
	if withPagination {
		required = append(required, "pagination")
	}
	collection, err := decodeHomeObject(path, raw, required, nil)
	if err != nil {
		return err
	}
	if err := validateHomeAnalysisMetadata(
		path+".analysis",
		collection["analysis"],
		window,
	); err != nil {
		return err
	}
	items, err := decodeHomeArray(path+".items", collection["items"], 0)
	if err != nil {
		return err
	}
	for index, item := range items {
		if err := itemValidator(
			fmt.Sprintf("%s.items[%d]", path, index),
			item,
		); err != nil {
			return err
		}
	}
	if withPagination {
		if err := validateHomeSnapshotPagination(
			path+".pagination",
			collection["pagination"],
			len(items),
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeAnalysisMetadata(
	path string,
	raw json.RawMessage,
	window homeAnalysisWindowDeclaration,
) error {
	analysis, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"coverage_ratio",
			"generated_at",
			"missing_signals",
			"sample_size",
			"sources",
			"window_days",
		},
		[]string{
			"analysis_run_id",
			"analysis_type",
			"baseline_window_days",
			"cohort_revision",
			"formula_version",
			"recent_window_days",
		},
	)
	if err != nil {
		return err
	}
	if rawValue, ok := analysis["analysis_run_id"]; ok {
		if err := validateHomeUUID(path+".analysis_run_id", rawValue); err != nil {
			return err
		}
	}
	if rawValue, ok := analysis["analysis_type"]; ok {
		if err := validateHomeString(path+".analysis_type", rawValue, 1, 0); err != nil {
			return err
		}
	}
	if rawValue, ok := analysis["baseline_window_days"]; ok {
		if err := validateHomeInteger(
			path+".baseline_window_days",
			rawValue,
			0,
		); err != nil {
			return err
		}
	}
	if rawValue, ok := analysis["cohort_revision"]; ok {
		if err := validateHomePatternString(
			path+".cohort_revision",
			rawValue,
			homeSHA256Pattern,
		); err != nil {
			return err
		}
	}
	if err := validateHomeCatalogValue(
		path+".coverage_ratio",
		analysis["coverage_ratio"],
		validateHomeRatio,
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if rawValue, ok := analysis["formula_version"]; ok {
		if err := validateHomeCatalogValue(
			path+".formula_version",
			rawValue,
			func(valuePath string, value json.RawMessage) error {
				return validateHomeString(valuePath, value, 0, 0)
			},
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	if err := validateHomeCatalogValue(
		path+".generated_at",
		analysis["generated_at"],
		func(valuePath string, value json.RawMessage) error {
			return validateHomeString(valuePath, value, 0, 0)
		},
		true,
		true,
		false,
	); err != nil {
		return err
	}
	missingSignals, err := decodeHomeArray(
		path+".missing_signals",
		analysis["missing_signals"],
		0,
	)
	if err != nil {
		return err
	}
	for index, missingSignal := range missingSignals {
		itemPath := fmt.Sprintf("%s.missing_signals[%d]", path, index)
		switch firstNonSpaceByte(missingSignal) {
		case '"':
			if err := validateHomeString(itemPath, missingSignal, 1, 0); err != nil {
				return err
			}
		case '{':
			if err := validateHomeMissingSignal(itemPath, missingSignal); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s must be a non-empty string or MissingSignal", itemPath)
		}
	}
	if rawValue, ok := analysis["recent_window_days"]; ok {
		if err := validateHomeInteger(
			path+".recent_window_days",
			rawValue,
			0,
		); err != nil {
			return err
		}
	}
	if err := validateHomeCatalogValue(
		path+".sample_size",
		analysis["sample_size"],
		func(valuePath string, value json.RawMessage) error {
			return validateHomeInteger(valuePath, value, 0)
		},
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".sources",
		analysis["sources"],
		func(valuePath string, value json.RawMessage) error {
			return validateHomeStringArray(valuePath, value, 0, 1, false)
		},
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".window_days",
		analysis["window_days"],
		func(valuePath string, value json.RawMessage) error {
			return validateHomeInteger(valuePath, value, 0)
		},
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeAnalysisWindowDeclaration(
		path+".window_days",
		analysis["window_days"],
		window,
	); err != nil {
		return err
	}
	return validateHomeAnalysisMetadataCoherence(path, analysis, window)
}

func validateHomeAnalysisWindowDeclaration(
	path string,
	raw json.RawMessage,
	window homeAnalysisWindowDeclaration,
) error {
	state, err := decodeHomeUnionState(path, raw)
	if err != nil {
		return err
	}
	if !window.known {
		if state != "missing" {
			return fmt.Errorf(
				"%s.state = %q, want missing for %s",
				path,
				state,
				homeSnapshotSchemaVersion,
			)
		}
		return nil
	}
	if state != "known" {
		return fmt.Errorf(
			"%s.state = %q, want known for %s",
			path,
			state,
			homeSnapshotSchemaVersion,
		)
	}
	value, err := decodeHomeObject(path, raw, []string{"state", "value"}, nil)
	if err != nil {
		return err
	}
	days, err := decodeHomeInteger(path+".value", value["value"])
	if err != nil {
		return err
	}
	if days != window.days {
		return fmt.Errorf(
			"%s.value = %d, want %d for %s",
			path,
			days,
			window.days,
			homeSnapshotSchemaVersion,
		)
	}
	return nil
}

func validateHomeAnalysisMetadataCoherence(
	path string,
	analysis map[string]json.RawMessage,
	window homeAnalysisWindowDeclaration,
) error {
	identityFields := []string{
		"analysis_run_id",
		"analysis_type",
		"cohort_revision",
	}
	identityFieldCount := 0
	for _, field := range identityFields {
		if _, ok := analysis[field]; ok {
			identityFieldCount++
		}
	}
	if identityFieldCount != 0 && identityFieldCount != len(identityFields) {
		return fmt.Errorf(
			"%s must provide analysis_run_id, analysis_type, and cohort_revision together",
			path,
		)
	}

	recentRaw, hasRecent := analysis["recent_window_days"]
	baselineRaw, hasBaseline := analysis["baseline_window_days"]
	if hasRecent != hasBaseline {
		return fmt.Errorf(
			"%s must provide recent_window_days and baseline_window_days together",
			path,
		)
	}
	if !hasRecent {
		return nil
	}
	if !window.known {
		return fmt.Errorf(
			"%s cannot declare recent or baseline windows when window_days is missing",
			path,
		)
	}
	recentDays, err := decodeHomeInteger(path+".recent_window_days", recentRaw)
	if err != nil {
		return err
	}
	if recentDays != window.days {
		return fmt.Errorf(
			"%s.recent_window_days = %d, want declared window %d",
			path,
			recentDays,
			window.days,
		)
	}
	baselineDays, err := decodeHomeInteger(path+".baseline_window_days", baselineRaw)
	if err != nil {
		return err
	}
	if baselineDays <= recentDays {
		return fmt.Errorf(
			"%s.baseline_window_days must exceed recent_window_days",
			path,
		)
	}
	return nil
}

func validateHomeMissingSignal(path string, raw json.RawMessage) error {
	signal, err := decodeHomeObject(
		path,
		raw,
		[]string{"signal"},
		[]string{"reason", "affected_entity_ids"},
	)
	if err != nil {
		return err
	}
	if err := validateHomeString(path+".signal", signal["signal"], 1, 0); err != nil {
		return err
	}
	if rawValue, ok := signal["reason"]; ok {
		if err := validateHomeString(path+".reason", rawValue, 1, 0); err != nil {
			return err
		}
	}
	if rawValue, ok := signal["affected_entity_ids"]; ok {
		if err := validateHomeUUIDArray(
			path+".affected_entity_ids",
			rawValue,
			0,
			false,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeSnapshotPagination(
	path string,
	raw json.RawMessage,
	itemCount int,
) error {
	pagination, err := decodeHomeObject(
		path,
		raw,
		[]string{"limit", "total", "next_cursor", "has_more"},
		nil,
	)
	if err != nil {
		return err
	}
	limit, err := decodeHomeInteger(path+".limit", pagination["limit"])
	if err != nil {
		return err
	}
	if limit < 0 {
		return fmt.Errorf("%s.limit must be >= 0", path)
	}
	total, err := decodeHomeInteger(path+".total", pagination["total"])
	if err != nil {
		return err
	}
	if total < 0 {
		return fmt.Errorf("%s.total must be >= 0", path)
	}
	if !bytes.Equal(bytes.TrimSpace(pagination["next_cursor"]), []byte("null")) {
		return fmt.Errorf("%s.next_cursor must be null", path)
	}
	hasMore, err := decodeHomeBoolean(path+".has_more", pagination["has_more"])
	if err != nil {
		return err
	}
	if hasMore {
		return fmt.Errorf("%s.has_more must be false", path)
	}
	if limit != int64(itemCount) || total != int64(itemCount) {
		return fmt.Errorf(
			"%s must describe its complete immutable item set: limit=%d total=%d items=%d",
			path,
			limit,
			total,
			itemCount,
		)
	}
	return nil
}

func validateHomeCoverage(path string, raw json.RawMessage) error {
	coverage, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"analysis",
			"citation_coverage_ratio",
			"jcr_metric_year",
			"mesh_coverage_ratio",
			"publication_type_coverage_ratio",
			"taxonomy_version",
		},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeAnalysisMetadata(
		path+".analysis",
		coverage["analysis"],
		homeAnalysisWindowDeclaration{known: true, days: 30},
	); err != nil {
		return err
	}
	for _, field := range []string{
		"citation_coverage_ratio",
		"mesh_coverage_ratio",
		"publication_type_coverage_ratio",
	} {
		if err := validateHomeCatalogValue(
			path+"."+field,
			coverage[field],
			validateHomeRatio,
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	if err := validateHomeYear(path+".jcr_metric_year", coverage["jcr_metric_year"]); err != nil {
		return err
	}
	return validateHomeString(path+".taxonomy_version", coverage["taxonomy_version"], 1, 0)
}

func validateHomeScope(path string, raw json.RawMessage) error {
	scope, err := decodeHomeObject(
		path,
		raw,
		[]string{"jcr_metric_year", "taxonomy_version"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeYear(path+".jcr_metric_year", scope["jcr_metric_year"]); err != nil {
		return err
	}
	return validateHomeString(path+".taxonomy_version", scope["taxonomy_version"], 1, 0)
}

func validateHomeJournalActivityItem(path string, raw json.RawMessage) error {
	item, err := decodeHomeObject(
		path,
		raw,
		[]string{"journal", "paper_count", "publication_change_ratio"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeJournalSummary(path+".journal", item["journal"]); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".paper_count",
		item["paper_count"],
		func(valuePath string, value json.RawMessage) error {
			return validateHomeInteger(valuePath, value, 0)
		},
		true,
		true,
		false,
	); err != nil {
		return err
	}
	return validateHomeCatalogValue(
		path+".publication_change_ratio",
		item["publication_change_ratio"],
		validateHomeNumber,
		true,
		true,
		false,
	)
}

func validateHomeJournalSummary(path string, raw json.RawMessage) error {
	journal, err := decodeHomeObject(
		path,
		raw,
		[]string{"id", "slug", "title", "jcr_metric_year", "jif"},
		[]string{
			"aliases",
			"categories",
			"eissn",
			"issn_l",
			"issns",
			"paper_count",
			"publisher",
			"taxonomy_version",
		},
	)
	if err != nil {
		return err
	}
	if err := validateHomeUUID(path+".id", journal["id"]); err != nil {
		return err
	}
	if err := validateHomeTaxonomySlug(path+".slug", journal["slug"]); err != nil {
		return err
	}
	if err := validateHomeString(path+".title", journal["title"], 1, 0); err != nil {
		return err
	}
	if rawValue, ok := journal["aliases"]; ok {
		if err := validateHomeStringArray(path+".aliases", rawValue, 0, 1, false); err != nil {
			return err
		}
	}
	if rawValue, ok := journal["categories"]; ok {
		categories, err := decodeHomeArray(path+".categories", rawValue, 0)
		if err != nil {
			return err
		}
		for index, category := range categories {
			if err := validateHomeJournalMetricCategory(
				fmt.Sprintf("%s.categories[%d]", path, index),
				category,
			); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"eissn", "issn_l", "publisher"} {
		if rawValue, ok := journal[field]; ok {
			if err := validateHomeCatalogValue(
				path+"."+field,
				rawValue,
				func(valuePath string, value json.RawMessage) error {
					return validateHomeString(valuePath, value, 0, 0)
				},
				true,
				true,
				false,
			); err != nil {
				return err
			}
		}
	}
	if rawValue, ok := journal["issns"]; ok {
		if err := validateHomeStringArray(path+".issns", rawValue, 0, 1, false); err != nil {
			return err
		}
	}
	if err := validateHomeYear(path+".jcr_metric_year", journal["jcr_metric_year"]); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".jif",
		journal["jif"],
		validateHomeNumber,
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if rawValue, ok := journal["paper_count"]; ok {
		if err := validateHomeCatalogValue(
			path+".paper_count",
			rawValue,
			func(valuePath string, value json.RawMessage) error {
				return validateHomeInteger(valuePath, value, 0)
			},
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	if rawValue, ok := journal["taxonomy_version"]; ok {
		if err := validateHomeString(path+".taxonomy_version", rawValue, 1, 0); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeJournalMetricCategory(path string, raw json.RawMessage) error {
	category, err := decodeHomeObject(
		path,
		raw,
		[]string{"name", "quartile"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeString(path+".name", category["name"], 1, 0); err != nil {
		return err
	}
	return validateHomeString(path+".quartile", category["quartile"], 1, 0)
}

func validateHomeCitationMomentumItem(path string, raw json.RawMessage) error {
	item, err := decodeHomeObject(
		path,
		raw,
		[]string{"citation_delta", "citations_per_day", "cohort_percentile", "paper"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".citation_delta",
		item["citation_delta"],
		func(valuePath string, value json.RawMessage) error {
			return validateHomeInteger(valuePath, value, 0)
		},
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".citations_per_day",
		item["citations_per_day"],
		validateHomeNumber,
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".cohort_percentile",
		item["cohort_percentile"],
		validateHomeRatio,
		true,
		true,
		false,
	); err != nil {
		return err
	}
	return validateHomePaperSummary(path+".paper", item["paper"])
}

func validateHomeEntityMomentumItem(path string, raw json.RawMessage) error {
	item, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"adjusted_p_value",
			"baseline_count",
			"confidence_interval",
			"entity_type",
			"estimate",
			"model_family",
			"p_value",
			"recent_count",
			"label",
			"independent_journal_count",
			"independent_team_count",
		},
		nil,
	)
	if err != nil {
		return err
	}
	for _, field := range []string{"adjusted_p_value", "estimate", "p_value"} {
		if err := validateHomeCatalogValue(
			path+"."+field,
			item[field],
			validateHomeNumber,
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	for _, field := range []string{
		"baseline_count",
		"recent_count",
		"independent_journal_count",
		"independent_team_count",
	} {
		if err := validateHomeCatalogValue(
			path+"."+field,
			item[field],
			func(valuePath string, value json.RawMessage) error {
				return validateHomeInteger(valuePath, value, 0)
			},
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	if err := validateHomeConfidenceInterval(
		path+".confidence_interval",
		item["confidence_interval"],
	); err != nil {
		return err
	}
	if err := validateHomeStringEnum(
		path+".entity_type",
		item["entity_type"],
		"disease",
		"method",
		"publication_type",
		"study_design",
		"target",
	); err != nil {
		return err
	}
	if err := validateHomeString(path+".label", item["label"], 1, 0); err != nil {
		return err
	}
	return validateHomeString(path+".model_family", item["model_family"], 1, 0)
}

func validateHomeSubjectTrendEstimate(path string, raw json.RawMessage) error {
	item, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"adjusted_p_value",
			"baseline_count",
			"confidence_interval",
			"estimate",
			"model_family",
			"p_value",
			"recent_count",
			"independent_journal_count",
			"independent_team_count",
			"subject",
		},
		[]string{"label"},
	)
	if err != nil {
		return err
	}
	for _, field := range []string{"adjusted_p_value", "estimate", "p_value"} {
		if err := validateHomeCatalogValue(
			path+"."+field,
			item[field],
			validateHomeNumber,
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	for _, field := range []string{
		"baseline_count",
		"recent_count",
		"independent_journal_count",
		"independent_team_count",
	} {
		if err := validateHomeCatalogValue(
			path+"."+field,
			item[field],
			func(valuePath string, value json.RawMessage) error {
				return validateHomeInteger(valuePath, value, 0)
			},
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	if err := validateHomeConfidenceInterval(
		path+".confidence_interval",
		item["confidence_interval"],
	); err != nil {
		return err
	}
	if err := validateHomeString(path+".model_family", item["model_family"], 1, 0); err != nil {
		return err
	}
	if rawValue, ok := item["label"]; ok {
		if err := validateHomeString(path+".label", rawValue, 1, 0); err != nil {
			return err
		}
	}
	if err := validateHomeTaxonomyReference(path+".subject", item["subject"]); err != nil {
		return err
	}
	return nil
}

func validateHomeConfidenceInterval(path string, raw json.RawMessage) error {
	interval, err := decodeHomeObject(
		path,
		raw,
		[]string{"lower", "upper"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeNumber(path+".lower", interval["lower"]); err != nil {
		return err
	}
	return validateHomeNumber(path+".upper", interval["upper"])
}

func validateHomePublicationUpdates(path string, raw json.RawMessage) error {
	updates, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"calendar_date",
			"calendar_timezone",
			"formal_publications_today",
			"recent_acceptances",
			"recent_online_first",
		},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeDate(path+".calendar_date", updates["calendar_date"]); err != nil {
		return err
	}
	if err := validateHomeStringConst(
		path+".calendar_timezone",
		updates["calendar_timezone"],
		"UTC",
	); err != nil {
		return err
	}
	for _, collection := range []struct {
		field  string
		window homeAnalysisWindowDeclaration
	}{
		{
			field:  "formal_publications_today",
			window: homeAnalysisWindowDeclaration{known: true, days: 1},
		},
		{
			field:  "recent_acceptances",
			window: homeAnalysisWindowDeclaration{known: true, days: 7},
		},
		{
			field:  "recent_online_first",
			window: homeAnalysisWindowDeclaration{known: true, days: 7},
		},
	} {
		if err := validateHomeCollection(
			path+"."+collection.field,
			updates[collection.field],
			collection.window,
			true,
			validateHomePublicationUpdateItem,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHomePublicationUpdateItem(path string, raw json.RawMessage) error {
	item, err := decodeHomeObject(
		path,
		raw,
		[]string{"paper", "event"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomePaperSummary(path+".paper", item["paper"]); err != nil {
		return err
	}
	return validateHomePublicationUpdateEvent(path+".event", item["event"])
}

func validateHomePublicationUpdateEvent(path string, raw json.RawMessage) error {
	event, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"kind",
			"date",
			"date_precision",
			"publication_status",
			"publication_model",
			"provenance",
		},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeStringEnum(
		path+".kind",
		event["kind"],
		"print_published",
		"electronic_published",
		"ahead_of_print",
		"accepted",
	); err != nil {
		return err
	}
	if err := validateHomeDate(path+".date", event["date"]); err != nil {
		return err
	}
	if err := validateHomeStringConst(
		path+".date_precision",
		event["date_precision"],
		"day",
	); err != nil {
		return err
	}
	if err := validateHomeString(
		path+".publication_status",
		event["publication_status"],
		1,
		0,
	); err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(event["publication_model"]), []byte("null")) {
		if err := validateHomeString(
			path+".publication_model",
			event["publication_model"],
			1,
			0,
		); err != nil {
			return err
		}
	}
	return validateHomePublicationEventProvenance(
		path+".provenance",
		event["provenance"],
	)
}

func validateHomePublicationEventProvenance(path string, raw json.RawMessage) error {
	provenance, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"source",
			"source_record_id",
			"normalized_assertion_id",
			"projection_assertion_id",
			"source_path",
			"status_raw",
		},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeStringConst(path+".source", provenance["source"], "pubmed"); err != nil {
		return err
	}
	for _, field := range []string{
		"source_record_id",
		"normalized_assertion_id",
		"projection_assertion_id",
	} {
		if err := validateHomeUUID(path+"."+field, provenance[field]); err != nil {
			return err
		}
	}
	if err := validateHomeString(path+".source_path", provenance["source_path"], 1, 0); err != nil {
		return err
	}
	return validateHomeString(path+".status_raw", provenance["status_raw"], 1, 0)
}

func validateHomePaperSummary(path string, raw json.RawMessage) error {
	paper, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"id",
			"title",
			"status",
			"published_at",
			"type",
			"has_code",
			"has_data",
			"has_benchmark",
			"citation_source",
			"citation_snapshots",
			"citation_velocity",
			"citation_percentile",
			"mesh_headings",
			"publication_types_state",
			"jcr_assessment",
			"article_usage",
			"open_fulltext",
		},
		[]string{
			"canonical_key",
			"abstract",
			"abstract_snippet",
			"citation_count",
			"citation_analysis_evidence",
			"trend_score",
			"topics",
			"methods",
			"authors",
			"journal",
			"publication_types",
			"subjects",
			"curation",
			"source_provenance",
		},
	)
	if err != nil {
		return err
	}
	if err := validateHomeUUID(path+".id", paper["id"]); err != nil {
		return err
	}
	if rawValue, ok := paper["canonical_key"]; ok {
		if err := validateHomeString(path+".canonical_key", rawValue, 1, 0); err != nil {
			return err
		}
	}
	if err := validateHomeString(path+".title", paper["title"], 1, 0); err != nil {
		return err
	}
	if err := validateHomeStringEnum(
		path+".status",
		paper["status"],
		"active",
		"withdrawn",
		"retracted",
		"rejected",
		"superseded",
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".published_at",
		paper["published_at"],
		validateHomeDateTime,
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".type",
		paper["type"],
		func(valuePath string, value json.RawMessage) error {
			return validateHomeStringEnum(
				valuePath,
				value,
				"research_article",
				"review",
				"preprint",
				"dataset",
				"benchmark",
			)
		},
		true,
		true,
		false,
	); err != nil {
		return err
	}
	for _, field := range []string{"abstract", "abstract_snippet"} {
		if rawValue, ok := paper[field]; ok {
			if err := validateHomeCatalogValue(
				path+"."+field,
				rawValue,
				func(valuePath string, value json.RawMessage) error {
					return validateHomeString(valuePath, value, 0, 0)
				},
				true,
				true,
				false,
			); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"has_code", "has_data", "has_benchmark"} {
		if err := validateHomeCatalogValue(
			path+"."+field,
			paper[field],
			validateHomeBoolean,
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	if rawValue, ok := paper["citation_count"]; ok {
		if err := validateHomeCatalogValue(
			path+".citation_count",
			rawValue,
			func(valuePath string, value json.RawMessage) error {
				return validateHomeInteger(valuePath, value, 0)
			},
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	if err := validateHomeCatalogValue(
		path+".citation_source",
		paper["citation_source"],
		func(valuePath string, value json.RawMessage) error {
			return validateHomeString(valuePath, value, 0, 0)
		},
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".citation_snapshots",
		paper["citation_snapshots"],
		validateHomeCitationSnapshots,
		true,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".citation_velocity",
		paper["citation_velocity"],
		validateHomeNumber,
		true,
		true,
		true,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".citation_percentile",
		paper["citation_percentile"],
		validateHomePercentile,
		true,
		true,
		true,
	); err != nil {
		return err
	}
	if rawValue, ok := paper["citation_analysis_evidence"]; ok {
		if err := validateHomeCatalogValue(
			path+".citation_analysis_evidence",
			rawValue,
			validateHomeCitationAnalysisEvidence,
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	if rawValue, ok := paper["trend_score"]; ok {
		if err := validateHomeCatalogValue(
			path+".trend_score",
			rawValue,
			validateHomeNumber,
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	for _, field := range []string{"topics", "methods", "subjects"} {
		if rawValue, ok := paper[field]; ok {
			items, err := decodeHomeArray(path+"."+field, rawValue, 0)
			if err != nil {
				return err
			}
			for index, item := range items {
				if err := validateHomeTaxonomyReference(
					fmt.Sprintf("%s.%s[%d]", path, field, index),
					item,
				); err != nil {
					return err
				}
			}
		}
	}
	if rawValue, ok := paper["authors"]; ok {
		authors, err := decodeHomeArray(path+".authors", rawValue, 0)
		if err != nil {
			return err
		}
		for index, author := range authors {
			if err := validateHomeAuthorReference(
				fmt.Sprintf("%s.authors[%d]", path, index),
				author,
			); err != nil {
				return err
			}
		}
	}
	if rawValue, ok := paper["journal"]; ok {
		if err := validateHomeJournalReference(path+".journal", rawValue); err != nil {
			return err
		}
	}
	if rawValue, ok := paper["publication_types"]; ok {
		if err := validateHomeStringArray(
			path+".publication_types",
			rawValue,
			0,
			1,
			false,
		); err != nil {
			return err
		}
	}
	if err := validateHomeCatalogValue(
		path+".publication_types_state",
		paper["publication_types_state"],
		func(valuePath string, value json.RawMessage) error {
			return validateHomeStringArray(valuePath, value, 0, 1, false)
		},
		false,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".mesh_headings",
		paper["mesh_headings"],
		validateHomeMeshHeadings,
		false,
		true,
		false,
	); err != nil {
		return err
	}
	if err := validateHomeCatalogValue(
		path+".jcr_assessment",
		paper["jcr_assessment"],
		validateHomeBiomedicalEligibilityRevision,
		false,
		false,
		false,
	); err != nil {
		return err
	}
	for _, field := range []string{"article_usage", "open_fulltext"} {
		if err := validateHomeMissingCatalogValue(path+"."+field, paper[field]); err != nil {
			return err
		}
	}
	if rawValue, ok := paper["curation"]; ok {
		if err := validateHomeCatalogValue(
			path+".curation",
			rawValue,
			validateHomeCurationPayload,
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	if rawValue, ok := paper["source_provenance"]; ok {
		if err := validateHomeCatalogValue(
			path+".source_provenance",
			rawValue,
			validateHomeSourceProvenanceArray,
			true,
			true,
			false,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeCitationSnapshots(path string, raw json.RawMessage) error {
	snapshots, err := decodeHomeArray(path, raw, 1)
	if err != nil {
		return err
	}
	for index, snapshot := range snapshots {
		if err := validateHomeCitationSnapshot(
			fmt.Sprintf("%s[%d]", path, index),
			snapshot,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeCitationSnapshot(path string, raw json.RawMessage) error {
	snapshot, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"source",
			"observed_at",
			"count",
			"source_record_id",
			"ingestion_job_id",
			"retrieved_at",
			"coverage",
			"definition_version",
			"dataset_version",
		},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeString(path+".source", snapshot["source"], 1, 0); err != nil {
		return err
	}
	if err := validateHomeDateTime(path+".observed_at", snapshot["observed_at"]); err != nil {
		return err
	}
	if err := validateHomeInteger(path+".count", snapshot["count"], 0); err != nil {
		return err
	}
	for _, field := range []string{"source_record_id", "ingestion_job_id"} {
		if err := validateHomeUUID(path+"."+field, snapshot[field]); err != nil {
			return err
		}
	}
	if err := validateHomeDateTime(path+".retrieved_at", snapshot["retrieved_at"]); err != nil {
		return err
	}
	if err := validateHomeRatio(path+".coverage", snapshot["coverage"]); err != nil {
		return err
	}
	for _, field := range []string{"definition_version", "dataset_version"} {
		if err := validateHomeString(path+"."+field, snapshot[field], 1, 0); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeCitationAnalysisEvidence(path string, raw json.RawMessage) error {
	evidence, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"analysis_run_id",
			"source",
			"as_of",
			"generated_at",
			"formula_version",
			"source_revision",
			"velocity",
			"percentile",
		},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeUUID(path+".analysis_run_id", evidence["analysis_run_id"]); err != nil {
		return err
	}
	if err := validateHomeString(path+".source", evidence["source"], 1, 0); err != nil {
		return err
	}
	for _, field := range []string{"as_of", "generated_at"} {
		if err := validateHomeDateTime(path+"."+field, evidence[field]); err != nil {
			return err
		}
	}
	if err := validateHomeString(path+".formula_version", evidence["formula_version"], 1, 0); err != nil {
		return err
	}
	if err := validateHomePatternString(
		path+".source_revision",
		evidence["source_revision"],
		homeSHA256Pattern,
	); err != nil {
		return err
	}
	if err := validateHomeCitationVelocityEvidence(
		path+".velocity",
		evidence["velocity"],
	); err != nil {
		return err
	}
	return validateHomeCitationPercentileEvidence(
		path+".percentile",
		evidence["percentile"],
	)
}

func validateHomeCitationVelocityEvidence(path string, raw json.RawMessage) error {
	state, err := decodeHomeUnionState(path, raw)
	if err != nil {
		return err
	}
	switch state {
	case "known":
		evidence, err := decodeHomeObject(
			path,
			raw,
			[]string{
				"state",
				"window_days",
				"current_snapshot_id",
				"baseline_snapshot_id",
				"elapsed_days",
			},
			nil,
		)
		if err != nil {
			return err
		}
		if err := validateHomeInteger(
			path+".window_days",
			evidence["window_days"],
			1,
			3650,
		); err != nil {
			return err
		}
		for _, field := range []string{"current_snapshot_id", "baseline_snapshot_id"} {
			if err := validateHomeUUID(path+"."+field, evidence[field]); err != nil {
				return err
			}
		}
		return validateHomeExclusivePositiveNumber(
			path+".elapsed_days",
			evidence["elapsed_days"],
		)
	case "insufficient_evidence":
		evidence, err := decodeHomeObject(
			path,
			raw,
			[]string{"state", "window_days", "reason", "missing"},
			nil,
		)
		if err != nil {
			return err
		}
		if err := validateHomeInteger(
			path+".window_days",
			evidence["window_days"],
			1,
			3650,
		); err != nil {
			return err
		}
		if err := validateHomeString(path+".reason", evidence["reason"], 1, 0); err != nil {
			return err
		}
		return validateHomeStringArray(path+".missing", evidence["missing"], 1, 1, false)
	default:
		return fmt.Errorf("%s.state = %q is unsupported", path, state)
	}
}

func validateHomeCitationPercentileEvidence(path string, raw json.RawMessage) error {
	state, err := decodeHomeUnionState(path, raw)
	if err != nil {
		return err
	}
	var required []string
	switch state {
	case "known":
		required = []string{
			"state",
			"citation_snapshot_id",
			"subject_version_id",
			"subject_id",
			"publication_year",
			"publication_type_id",
			"cohort_key",
			"cohort_size",
			"minimum_cohort_size",
			"midrank",
			"supporting_work_ids",
		}
	case "insufficient_evidence":
		required = []string{
			"state",
			"citation_snapshot_id",
			"subject_version_id",
			"subject_id",
			"publication_year",
			"publication_type_id",
			"cohort_key",
			"cohort_size",
			"minimum_cohort_size",
			"reason",
			"supporting_work_ids",
		}
	default:
		return fmt.Errorf("%s.state = %q is unsupported", path, state)
	}
	evidence, err := decodeHomeObject(path, raw, required, nil)
	if err != nil {
		return err
	}
	for _, field := range []string{
		"citation_snapshot_id",
		"subject_version_id",
		"subject_id",
		"publication_type_id",
	} {
		if err := validateHomeUUID(path+"."+field, evidence[field]); err != nil {
			return err
		}
	}
	if err := validateHomeYear(path+".publication_year", evidence["publication_year"]); err != nil {
		return err
	}
	if err := validateHomeString(path+".cohort_key", evidence["cohort_key"], 1, 0); err != nil {
		return err
	}
	if err := validateHomeInteger(path+".cohort_size", evidence["cohort_size"], 1); err != nil {
		return err
	}
	if err := validateHomeInteger(
		path+".minimum_cohort_size",
		evidence["minimum_cohort_size"],
		2,
	); err != nil {
		return err
	}
	if state == "known" {
		if err := validateHomeAtLeastOneNumber(path+".midrank", evidence["midrank"]); err != nil {
			return err
		}
	} else if err := validateHomeString(path+".reason", evidence["reason"], 1, 0); err != nil {
		return err
	}
	return validateHomeUUIDArray(
		path+".supporting_work_ids",
		evidence["supporting_work_ids"],
		1,
		false,
	)
}

func validateHomeTaxonomyReference(path string, raw json.RawMessage) error {
	reference, err := decodeHomeObject(
		path,
		raw,
		[]string{"id", "slug", "name"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeUUID(path+".id", reference["id"]); err != nil {
		return err
	}
	if err := validateHomeTaxonomySlug(path+".slug", reference["slug"]); err != nil {
		return err
	}
	return validateHomeString(path+".name", reference["name"], 1, 0)
}

func validateHomeAuthorReference(path string, raw json.RawMessage) error {
	author, err := decodeHomeObject(
		path,
		raw,
		[]string{"id", "name", "position", "is_corresponding"},
		[]string{"orcid", "institution_id", "institution_name"},
	)
	if err != nil {
		return err
	}
	if err := validateHomeUUID(path+".id", author["id"]); err != nil {
		return err
	}
	if err := validateHomeString(path+".name", author["name"], 1, 0); err != nil {
		return err
	}
	if rawValue, ok := author["orcid"]; ok {
		if err := validateHomeString(path+".orcid", rawValue, 1, 0); err != nil {
			return err
		}
	}
	if rawValue, ok := author["institution_id"]; ok {
		if err := validateHomeUUID(path+".institution_id", rawValue); err != nil {
			return err
		}
	}
	if rawValue, ok := author["institution_name"]; ok {
		if err := validateHomeString(path+".institution_name", rawValue, 1, 0); err != nil {
			return err
		}
	}
	if err := validateHomeInteger(path+".position", author["position"], 1); err != nil {
		return err
	}
	return validateHomeBoolean(path+".is_corresponding", author["is_corresponding"])
}

func validateHomeJournalReference(path string, raw json.RawMessage) error {
	journal, err := decodeHomeObject(
		path,
		raw,
		[]string{"id", "slug", "title"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeUUID(path+".id", journal["id"]); err != nil {
		return err
	}
	if err := validateHomeTaxonomySlug(path+".slug", journal["slug"]); err != nil {
		return err
	}
	return validateHomeString(path+".title", journal["title"], 1, 0)
}

func validateHomeMeshHeadings(path string, raw json.RawMessage) error {
	headings, err := decodeHomeArray(path, raw, 0)
	if err != nil {
		return err
	}
	for index, heading := range headings {
		if err := validateHomeMeshHeading(
			fmt.Sprintf("%s[%d]", path, index),
			heading,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeMeshHeading(path string, raw json.RawMessage) error {
	heading, err := decodeHomeObject(
		path,
		raw,
		[]string{"descriptor_ui", "label", "is_major_topic", "source_path", "qualifiers"},
		nil,
	)
	if err != nil {
		return err
	}
	for _, field := range []string{"descriptor_ui", "label", "source_path"} {
		if err := validateHomeString(path+"."+field, heading[field], 1, 0); err != nil {
			return err
		}
	}
	if err := validateHomeBoolean(path+".is_major_topic", heading["is_major_topic"]); err != nil {
		return err
	}
	qualifiers, err := decodeHomeArray(path+".qualifiers", heading["qualifiers"], 0)
	if err != nil {
		return err
	}
	for index, qualifier := range qualifiers {
		if err := validateHomeMeshQualifier(
			fmt.Sprintf("%s.qualifiers[%d]", path, index),
			qualifier,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeMeshQualifier(path string, raw json.RawMessage) error {
	qualifier, err := decodeHomeObject(
		path,
		raw,
		[]string{"qualifier_ui", "label", "is_major_topic", "source_path"},
		nil,
	)
	if err != nil {
		return err
	}
	for _, field := range []string{"qualifier_ui", "label", "source_path"} {
		if err := validateHomeString(path+"."+field, qualifier[field], 1, 0); err != nil {
			return err
		}
	}
	return validateHomeBoolean(path+".is_major_topic", qualifier["is_major_topic"])
}

func validateHomeBiomedicalEligibilityRevision(path string, raw json.RawMessage) error {
	revision, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"id",
			"policy_version",
			"metric_year",
			"subject_version_id",
			"subject_version_key",
			"decision",
			"evidence",
			"assessed_at",
		},
		nil,
	)
	if err != nil {
		return err
	}
	for _, field := range []string{"id", "subject_version_id"} {
		if err := validateHomeUUID(path+"."+field, revision[field]); err != nil {
			return err
		}
	}
	for _, field := range []string{"policy_version", "subject_version_key"} {
		if err := validateHomeString(path+"."+field, revision[field], 1, 0); err != nil {
			return err
		}
	}
	if err := validateHomeYear(path+".metric_year", revision["metric_year"]); err != nil {
		return err
	}
	if err := validateHomeStringConst(path+".decision", revision["decision"], "accepted"); err != nil {
		return err
	}
	if err := validateHomeBiomedicalEligibilityEvidence(
		path+".evidence",
		revision["evidence"],
	); err != nil {
		return err
	}
	return validateHomeDateTime(path+".assessed_at", revision["assessed_at"])
}

func validateHomeBiomedicalEligibilityEvidence(path string, raw json.RawMessage) error {
	evidence, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"policy_version",
			"metric_year",
			"subject_version_id",
			"subject_version_key",
			"venue",
			"metrics",
			"matches",
		},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeString(path+".policy_version", evidence["policy_version"], 1, 0); err != nil {
		return err
	}
	if err := validateHomeYear(path+".metric_year", evidence["metric_year"]); err != nil {
		return err
	}
	if err := validateHomeUUID(path+".subject_version_id", evidence["subject_version_id"]); err != nil {
		return err
	}
	if err := validateHomeString(
		path+".subject_version_key",
		evidence["subject_version_key"],
		1,
		0,
	); err != nil {
		return err
	}
	if err := validateHomeBiomedicalEligibilityVenue(path+".venue", evidence["venue"]); err != nil {
		return err
	}
	metrics, err := decodeHomeArray(path+".metrics", evidence["metrics"], 1)
	if err != nil {
		return err
	}
	for index, metric := range metrics {
		if err := validateHomeBiomedicalEligibilityMetric(
			fmt.Sprintf("%s.metrics[%d]", path, index),
			metric,
		); err != nil {
			return err
		}
	}
	matches, err := decodeHomeArray(path+".matches", evidence["matches"], 1)
	if err != nil {
		return err
	}
	for index, match := range matches {
		if err := validateHomeBiomedicalEligibilitySubject(
			fmt.Sprintf("%s.matches[%d]", path, index),
			match,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeBiomedicalEligibilityVenue(path string, raw json.RawMessage) error {
	venue, err := decodeHomeObject(
		path,
		raw,
		[]string{"venue_id"},
		[]string{"issn_l", "issn", "eissn"},
	)
	if err != nil {
		return err
	}
	if err := validateHomeUUID(path+".venue_id", venue["venue_id"]); err != nil {
		return err
	}
	hasIdentifier := false
	for _, field := range []string{"issn_l", "issn", "eissn"} {
		if rawValue, ok := venue[field]; ok {
			hasIdentifier = true
			if err := validateHomeString(path+"."+field, rawValue, 1, 0); err != nil {
				return err
			}
		}
	}
	if !hasIdentifier {
		return fmt.Errorf("%s must contain issn_l, issn, or eissn", path)
	}
	return nil
}

func validateHomeBiomedicalEligibilityMetric(path string, raw json.RawMessage) error {
	metric, err := decodeHomeObject(
		path,
		raw,
		[]string{"venue_metric_snapshot_id", "venue_id", "metric_year", "jcr_category"},
		nil,
	)
	if err != nil {
		return err
	}
	for _, field := range []string{"venue_metric_snapshot_id", "venue_id"} {
		if err := validateHomeUUID(path+"."+field, metric[field]); err != nil {
			return err
		}
	}
	if err := validateHomeYear(path+".metric_year", metric["metric_year"]); err != nil {
		return err
	}
	return validateHomeString(path+".jcr_category", metric["jcr_category"], 1, 0)
}

func validateHomeBiomedicalEligibilitySubject(path string, raw json.RawMessage) error {
	subject, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"journal_subject_metric_id",
			"venue_metric_snapshot_id",
			"venue_id",
			"metric_year",
			"subject_version_id",
			"subject_id",
			"subject_rule_id",
			"subject_slug",
			"jcr_category",
		},
		nil,
	)
	if err != nil {
		return err
	}
	for _, field := range []string{
		"journal_subject_metric_id",
		"venue_metric_snapshot_id",
		"venue_id",
		"subject_version_id",
		"subject_id",
		"subject_rule_id",
	} {
		if err := validateHomeUUID(path+"."+field, subject[field]); err != nil {
			return err
		}
	}
	if err := validateHomeYear(path+".metric_year", subject["metric_year"]); err != nil {
		return err
	}
	if err := validateHomeTaxonomySlug(path+".subject_slug", subject["subject_slug"]); err != nil {
		return err
	}
	return validateHomeString(path+".jcr_category", subject["jcr_category"], 1, 0)
}

func validateHomeCurationPayload(path string, raw json.RawMessage) error {
	payload, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"decision",
			"matched_rules",
			"evidence",
			"metric_year",
			"policy_name",
			"policy_version",
			"assessed_at",
		},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeStringEnum(
		path+".decision",
		payload["decision"],
		"accepted",
		"rejected",
		"not_applicable",
	); err != nil {
		return err
	}
	if err := validateHomeJSONValue(path+".matched_rules", payload["matched_rules"]); err != nil {
		return err
	}
	if err := validateHomeJSONValue(path+".evidence", payload["evidence"]); err != nil {
		return err
	}
	if err := validateHomeInteger(path+".metric_year", payload["metric_year"], 1); err != nil {
		return err
	}
	if err := validateHomeString(path+".policy_name", payload["policy_name"], 1, 0); err != nil {
		return err
	}
	if err := validateHomeInteger(path+".policy_version", payload["policy_version"], 1); err != nil {
		return err
	}
	return validateHomeDateTime(path+".assessed_at", payload["assessed_at"])
}

func validateHomeSourceProvenanceArray(path string, raw json.RawMessage) error {
	items, err := decodeHomeArray(path, raw, 1)
	if err != nil {
		return err
	}
	for index, item := range items {
		if err := validateHomeSourceProvenance(
			fmt.Sprintf("%s[%d]", path, index),
			item,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHomeSourceProvenance(path string, raw json.RawMessage) error {
	provenance, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"source",
			"event_key",
			"source_record_id",
			"source_time",
			"normalization_policy_version",
			"scope_policy_version",
			"projection_policy_version",
		},
		nil,
	)
	if err != nil {
		return err
	}
	for _, field := range []string{
		"source",
		"event_key",
		"normalization_policy_version",
		"scope_policy_version",
		"projection_policy_version",
	} {
		if err := validateHomeString(path+"."+field, provenance[field], 1, 0); err != nil {
			return err
		}
	}
	if err := validateHomeUUID(path+".source_record_id", provenance["source_record_id"]); err != nil {
		return err
	}
	return validateHomeDateTime(path+".source_time", provenance["source_time"])
}

func validateHomeResearchOpportunity(path string, raw json.RawMessage) error {
	opportunity, err := decodeHomeObject(
		path,
		raw,
		[]string{
			"analysis_run_id",
			"coverage_ratio",
			"estimates",
			"formula_version",
			"generated_at",
			"id",
			"status",
			"supporting_work_ids",
			"target_id",
			"target_kind",
			"title",
			"trigger_rule",
		},
		[]string{
			"limitations",
			"missing_signals",
			"recommended_next_steps",
			"summary",
		},
	)
	if err != nil {
		return err
	}
	for _, field := range []string{"analysis_run_id", "id"} {
		if err := validateHomeUUID(path+"."+field, opportunity[field]); err != nil {
			return err
		}
	}
	if err := validateHomeCatalogValue(
		path+".coverage_ratio",
		opportunity["coverage_ratio"],
		validateHomeRatio,
		true,
		true,
		false,
	); err != nil {
		return err
	}
	estimates, err := decodeHomeArray(path+".estimates", opportunity["estimates"], 1)
	if err != nil {
		return err
	}
	for index, estimate := range estimates {
		if err := validateHomeResearchOpportunityEstimate(
			fmt.Sprintf("%s.estimates[%d]", path, index),
			estimate,
		); err != nil {
			return err
		}
	}
	if err := validateHomeString(path+".formula_version", opportunity["formula_version"], 1, 0); err != nil {
		return err
	}
	if err := validateHomeDateTime(path+".generated_at", opportunity["generated_at"]); err != nil {
		return err
	}
	for _, field := range []string{
		"limitations",
		"missing_signals",
		"recommended_next_steps",
	} {
		if rawValue, ok := opportunity[field]; ok {
			if err := validateHomeStringArray(path+"."+field, rawValue, 0, 1, false); err != nil {
				return err
			}
		}
	}
	if rawValue, ok := opportunity["summary"]; ok {
		if err := validateHomeString(path+".summary", rawValue, 1, 0); err != nil {
			return err
		}
	}
	if err := validateHomeUUIDArray(
		path+".supporting_work_ids",
		opportunity["supporting_work_ids"],
		1,
		true,
	); err != nil {
		return err
	}
	if err := validateHomeStringEnum(
		path+".status",
		opportunity["status"],
		"worth_pursuing",
		"proceed_with_caution",
		"not_recommended_now",
		"insufficient_evidence",
	); err != nil {
		return err
	}
	for _, field := range []string{"target_id", "target_kind", "title"} {
		if err := validateHomeString(path+"."+field, opportunity[field], 1, 0); err != nil {
			return err
		}
	}
	return validateHomeResearchOpportunityTriggerRule(
		path+".trigger_rule",
		opportunity["trigger_rule"],
	)
}

func validateHomeResearchOpportunityEstimate(path string, raw json.RawMessage) error {
	estimate, err := decodeHomeObject(
		path,
		raw,
		[]string{"confidence_interval", "metric", "value"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeConfidenceInterval(
		path+".confidence_interval",
		estimate["confidence_interval"],
	); err != nil {
		return err
	}
	if err := validateHomeString(path+".metric", estimate["metric"], 1, 0); err != nil {
		return err
	}
	return validateHomeNumber(path+".value", estimate["value"])
}

func validateHomeResearchOpportunityTriggerRule(path string, raw json.RawMessage) error {
	rule, err := decodeHomeObject(
		path,
		raw,
		[]string{"code", "version"},
		nil,
	)
	if err != nil {
		return err
	}
	if err := validateHomeString(path+".code", rule["code"], 1, 0); err != nil {
		return err
	}
	return validateHomeString(path+".version", rule["version"], 1, 0)
}

func validateHomeCatalogValue(
	path string,
	raw json.RawMessage,
	knownValidator homeValueValidator,
	allowUnknown bool,
	allowMissing bool,
	allowInsufficientEvidence bool,
) error {
	state, err := decodeHomeUnionState(path, raw)
	if err != nil {
		return err
	}
	switch state {
	case "known":
		value, err := decodeHomeObject(path, raw, []string{"state", "value"}, nil)
		if err != nil {
			return err
		}
		return knownValidator(path+".value", value["value"])
	case "unknown":
		if !allowUnknown {
			return fmt.Errorf("%s.state = unknown is unsupported", path)
		}
		_, err := decodeHomeObject(path, raw, []string{"state"}, nil)
		return err
	case "missing":
		if !allowMissing {
			return fmt.Errorf("%s.state = missing is unsupported", path)
		}
		_, err := decodeHomeObject(path, raw, []string{"state"}, nil)
		return err
	case "insufficient_evidence":
		if !allowInsufficientEvidence {
			return fmt.Errorf("%s.state = insufficient_evidence is unsupported", path)
		}
		value, err := decodeHomeObject(path, raw, []string{"state", "reason"}, nil)
		if err != nil {
			return err
		}
		return validateHomeString(path+".reason", value["reason"], 1, 0)
	default:
		return fmt.Errorf("%s.state = %q is unsupported", path, state)
	}
}

func validateHomeMissingCatalogValue(path string, raw json.RawMessage) error {
	state, err := decodeHomeUnionState(path, raw)
	if err != nil {
		return err
	}
	if state != "missing" {
		return fmt.Errorf("%s.state = %q, want missing", path, state)
	}
	_, err = decodeHomeObject(path, raw, []string{"state"}, nil)
	return err
}

func decodeHomeUnionState(path string, raw json.RawMessage) (string, error) {
	if firstNonSpaceByte(raw) != '{' {
		return "", fmt.Errorf("%s must be an object", path)
	}
	var value map[string]json.RawMessage
	if err := strictHomeDecode(raw, &value); err != nil {
		return "", fmt.Errorf("decode %s: %w", path, err)
	}
	state, ok := value["state"]
	if !ok {
		return "", fmt.Errorf("%s is missing required field %q", path, "state")
	}
	return decodeHomeString(path+".state", state)
}

func decodeHomeObject(
	path string,
	raw json.RawMessage,
	required []string,
	optional []string,
) (map[string]json.RawMessage, error) {
	if firstNonSpaceByte(raw) != '{' {
		return nil, fmt.Errorf("%s must be an object", path)
	}
	var object map[string]json.RawMessage
	if err := strictHomeDecode(raw, &object); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be an object", path)
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, field := range required {
		allowed[field] = struct{}{}
		if _, ok := object[field]; !ok {
			return nil, fmt.Errorf("%s is missing required field %q", path, field)
		}
	}
	for _, field := range optional {
		allowed[field] = struct{}{}
	}
	for field := range object {
		if _, ok := allowed[field]; !ok {
			return nil, fmt.Errorf("%s contains unknown field %q", path, field)
		}
	}
	return object, nil
}

func decodeHomeArray(
	path string,
	raw json.RawMessage,
	minItems int,
) ([]json.RawMessage, error) {
	if firstNonSpaceByte(raw) != '[' {
		return nil, fmt.Errorf("%s must be an array", path)
	}
	var items []json.RawMessage
	if err := strictHomeDecode(raw, &items); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if len(items) < minItems {
		return nil, fmt.Errorf("%s must contain at least %d item(s)", path, minItems)
	}
	return items, nil
}

func strictHomeDecode(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func firstNonSpaceByte(raw json.RawMessage) byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

func validateHomeJSONValue(path string, raw json.RawMessage) error {
	var value any
	if err := strictHomeDecode(raw, &value); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func validateHomeString(
	path string,
	raw json.RawMessage,
	minLength int,
	maxLength int,
) error {
	value, err := decodeHomeString(path, raw)
	if err != nil {
		return err
	}
	length := utf8.RuneCountInString(value)
	if length < minLength {
		return fmt.Errorf("%s must contain at least %d character(s)", path, minLength)
	}
	if maxLength > 0 && length > maxLength {
		return fmt.Errorf("%s must contain at most %d character(s)", path, maxLength)
	}
	return nil
}

func decodeHomeString(path string, raw json.RawMessage) (string, error) {
	var value string
	if err := strictHomeDecode(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string: %w", path, err)
	}
	return value, nil
}

func validateHomeStringConst(path string, raw json.RawMessage, want string) error {
	value, err := decodeHomeString(path, raw)
	if err != nil {
		return err
	}
	if value != want {
		return fmt.Errorf("%s = %q, want %q", path, value, want)
	}
	return nil
}

func validateHomeStringEnum(
	path string,
	raw json.RawMessage,
	allowed ...string,
) error {
	value, err := decodeHomeString(path, raw)
	if err != nil {
		return err
	}
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("%s = %q is outside the allowed enum", path, value)
}

func validateHomePatternString(
	path string,
	raw json.RawMessage,
	pattern *regexp.Regexp,
) error {
	value, err := decodeHomeString(path, raw)
	if err != nil {
		return err
	}
	if !pattern.MatchString(value) {
		return fmt.Errorf("%s = %q does not match %s", path, value, pattern)
	}
	return nil
}

func validateHomeTaxonomySlug(path string, raw json.RawMessage) error {
	if err := validateHomeString(path, raw, 1, 120); err != nil {
		return err
	}
	return validateHomePatternString(path, raw, homeTaxonomySlugPattern)
}

func validateHomeStringArray(
	path string,
	raw json.RawMessage,
	minItems int,
	itemMinLength int,
	unique bool,
) error {
	items, err := decodeHomeArray(path, raw, minItems)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		value, err := decodeHomeString(itemPath, item)
		if err != nil {
			return err
		}
		if utf8.RuneCountInString(value) < itemMinLength {
			return fmt.Errorf(
				"%s must contain at least %d character(s)",
				itemPath,
				itemMinLength,
			)
		}
		if unique {
			if _, exists := seen[value]; exists {
				return fmt.Errorf("%s contains duplicate value %q", path, value)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func validateHomeBoolean(path string, raw json.RawMessage) error {
	_, err := decodeHomeBoolean(path, raw)
	return err
}

func decodeHomeBoolean(path string, raw json.RawMessage) (bool, error) {
	var value bool
	if err := strictHomeDecode(raw, &value); err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", path, err)
	}
	return value, nil
}

func validateHomeInteger(
	path string,
	raw json.RawMessage,
	minimum int64,
	maximum ...int64,
) error {
	value, err := decodeHomeInteger(path, raw)
	if err != nil {
		return err
	}
	if value < minimum {
		return fmt.Errorf("%s must be >= %d", path, minimum)
	}
	if len(maximum) > 1 {
		panic("validateHomeInteger accepts at most one maximum")
	}
	if len(maximum) == 1 && value > maximum[0] {
		return fmt.Errorf("%s must be <= %d", path, maximum[0])
	}
	return nil
}

func decodeHomeInteger(path string, raw json.RawMessage) (int64, error) {
	number, err := decodeHomeNumber(path, raw)
	if err != nil {
		return 0, err
	}
	if math.Trunc(number) != number {
		return 0, fmt.Errorf("%s must be an integer", path)
	}
	if number < math.MinInt64 || number > math.MaxInt64 {
		return 0, fmt.Errorf("%s is outside the supported integer range", path)
	}
	return int64(number), nil
}

func validateHomeNumber(path string, raw json.RawMessage) error {
	_, err := decodeHomeNumber(path, raw)
	return err
}

func validateHomeRatio(path string, raw json.RawMessage) error {
	value, err := decodeHomeNumber(path, raw)
	if err != nil {
		return err
	}
	if value < 0 || value > 1 {
		return fmt.Errorf("%s must be between 0 and 1", path)
	}
	return nil
}

func validateHomePercentile(path string, raw json.RawMessage) error {
	value, err := decodeHomeNumber(path, raw)
	if err != nil {
		return err
	}
	if value < 0 || value > 100 {
		return fmt.Errorf("%s must be between 0 and 100", path)
	}
	return nil
}

func validateHomeExclusivePositiveNumber(path string, raw json.RawMessage) error {
	value, err := decodeHomeNumber(path, raw)
	if err != nil {
		return err
	}
	if value <= 0 {
		return fmt.Errorf("%s must be > 0", path)
	}
	return nil
}

func validateHomeAtLeastOneNumber(path string, raw json.RawMessage) error {
	value, err := decodeHomeNumber(path, raw)
	if err != nil {
		return err
	}
	if value < 1 {
		return fmt.Errorf("%s must be >= 1", path)
	}
	return nil
}

func decodeHomeNumber(path string, raw json.RawMessage) (float64, error) {
	first := firstNonSpaceByte(raw)
	if first != '-' && (first < '0' || first > '9') {
		return 0, fmt.Errorf("%s must be a number", path)
	}
	var number json.Number
	if err := strictHomeDecode(raw, &number); err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", path, err)
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("%s must be a finite JSON number", path)
	}
	return value, nil
}

func validateHomeYear(path string, raw json.RawMessage) error {
	return validateHomeInteger(path, raw, 1900, 3000)
}

func validateHomeDate(path string, raw json.RawMessage) error {
	value, err := decodeHomeString(path, raw)
	if err != nil {
		return err
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return fmt.Errorf("%s must be an RFC 3339 full-date: %w", path, err)
	}
	return nil
}

func validateHomeDateTime(path string, raw json.RawMessage) error {
	value, err := decodeHomeString(path, raw)
	if err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("%s must be an RFC 3339 date-time: %w", path, err)
	}
	return nil
}

func validateHomeUUID(path string, raw json.RawMessage) error {
	value, err := decodeHomeString(path, raw)
	if err != nil {
		return err
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil {
		return fmt.Errorf("%s must be a non-Nil UUID", path)
	}
	return nil
}

func validateHomeUUIDArray(
	path string,
	raw json.RawMessage,
	minItems int,
	unique bool,
) error {
	items, err := decodeHomeArray(path, raw, minItems)
	if err != nil {
		return err
	}
	seen := make(map[uuid.UUID]struct{}, len(items))
	for index, item := range items {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		value, err := decodeHomeString(itemPath, item)
		if err != nil {
			return err
		}
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil {
			return fmt.Errorf("%s must be a non-Nil UUID", itemPath)
		}
		if unique {
			if _, exists := seen[parsed]; exists {
				return fmt.Errorf("%s contains duplicate UUID %s", path, parsed)
			}
			seen[parsed] = struct{}{}
		}
	}
	return nil
}
