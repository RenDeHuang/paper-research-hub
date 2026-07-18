package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

const (
	biomedicalHomeWindowDays   = 7
	biomedicalDetailWindowDays = 30
)

type journalMetricCategoryPayload struct {
	Name     string `json:"name"`
	Quartile string `json:"quartile"`
}

type journalSnapshotProfile struct {
	resource publishedBiomedicalResource
	summary  map[string]any
	curation map[string]any
}

type distributionCounter struct {
	knownPapers int
	counts      map[string]int64
}

func buildBiomedicalSnapshots(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
	papers []publishedPaper,
	curations []curationRevisionFact,
	citationMomentum map[string]any,
) (
	json.RawMessage,
	json.RawMessage,
	json.RawMessage,
	[]publishedBiomedicalResource,
	[]publishedBiomedicalResource,
	error,
) {
	jcrSource, err := loadJCRReceiptSource(ctx, tx, input.JCRImportReceipt)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	sources := biomedicalAnalysisSources(papers, jcrSource)
	curationByVenue, err := uniqueCurationByVenue(curations)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	journalPapers := make(map[uuid.UUID][]publishedPaper)
	subjectPapers := make(map[uuid.UUID][]publishedPaper)
	subjectsByID := make(map[uuid.UUID]taxonomyReference)
	for _, paper := range papers {
		journalPapers[paper.biomedical.Journal.ID] = append(
			journalPapers[paper.biomedical.Journal.ID],
			paper,
		)
		for _, subject := range paper.biomedical.Subjects {
			existing, found := subjectsByID[subject.ID]
			if found && existing != subject {
				return nil, nil, nil, nil, nil, fmt.Errorf(
					"%w: Subject %s identity changed within one Catalog snapshot",
					ErrCatalogNotReady,
					subject.ID,
				)
			}
			subjectsByID[subject.ID] = subject
			subjectPapers[subject.ID] = append(subjectPapers[subject.ID], paper)
		}
	}

	journalProfiles := make(map[uuid.UUID]journalSnapshotProfile, len(journalPapers))
	journals := make([]publishedBiomedicalResource, 0, len(journalPapers))
	for journalID, groupedPapers := range journalPapers {
		curation, found := curationByVenue[journalID]
		if !found {
			return nil, nil, nil, nil, nil, fmt.Errorf(
				"%w: Journal %s lacks one accepted curation snapshot",
				ErrCatalogNotReady,
				journalID,
			)
		}
		profile, err := buildJournalSnapshot(
			ctx,
			tx,
			input,
			journalID,
			groupedPapers,
			curation,
			sources,
		)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		journalProfiles[journalID] = profile
		journals = append(journals, profile.resource)
	}
	sortBiomedicalResources(journals)

	subjects := make([]publishedBiomedicalResource, 0, len(subjectPapers))
	for subjectID, groupedPapers := range subjectPapers {
		resource, err := buildSubjectSnapshot(
			input,
			subjectsByID[subjectID],
			groupedPapers,
			journalProfiles,
			sources,
		)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		subjects = append(subjects, resource)
	}
	sortBiomedicalResources(subjects)

	subjectListMetadata, err := marshalCatalogPayload(map[string]any{
		"analysis": analysisMetadata(
			input,
			biomedicalDetailWindowDays,
			len(subjects),
			catalogValue{State: "known", Value: float64(1)},
			sources,
			[]string{},
		),
		"jcr_metric_year":  input.JCRMetricYear,
		"taxonomy_version": input.SubjectVersion,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	journalListMetadata, err := marshalCatalogPayload(map[string]any{
		"analysis": analysisMetadata(
			input,
			biomedicalDetailWindowDays,
			len(journals),
			catalogValue{State: "known", Value: float64(1)},
			sources,
			[]string{},
		),
		"jcr_metric_year":  input.JCRMetricYear,
		"taxonomy_version": input.SubjectVersion,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	home, err := buildHomeSnapshot(
		input,
		papers,
		journalProfiles,
		sources,
		citationMomentum,
	)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return home, subjectListMetadata, journalListMetadata, subjects, journals, nil
}

func loadJCRReceiptSource(
	ctx context.Context,
	tx pgx.Tx,
	receiptID uuid.UUID,
) (string, error) {
	var source string
	if err := tx.QueryRow(ctx, `
		SELECT source
		FROM jcr_import_receipts
		WHERE id = $1
	`, receiptID).Scan(&source); err != nil {
		return "", fmt.Errorf("query JCR import receipt source: %w", err)
	}
	if source == "" || source != strings.TrimSpace(source) {
		return "", fmt.Errorf(
			"%w: JCR import receipt has invalid source",
			ErrCatalogNotReady,
		)
	}
	return source, nil
}

func uniqueCurationByVenue(
	curations []curationRevisionFact,
) (map[uuid.UUID]curationPayload, error) {
	values := make(map[uuid.UUID]curationPayload)
	for _, fact := range curations {
		if fact.VenueID == uuid.Nil || fact.Curation.Decision != "accepted" {
			return nil, fmt.Errorf(
				"%w: biomedical Catalog curation is not accepted",
				ErrCatalogNotReady,
			)
		}
		existing, found := values[fact.VenueID]
		if found && !equalCuration(existing, fact.Curation) {
			return nil, fmt.Errorf(
				"%w: Journal %s has conflicting curation evidence",
				ErrCatalogNotReady,
				fact.VenueID,
			)
		}
		values[fact.VenueID] = fact.Curation
	}
	return values, nil
}

func equalCuration(left, right curationPayload) bool {
	return left.Decision == right.Decision &&
		string(left.MatchedRules) == string(right.MatchedRules) &&
		string(left.Evidence) == string(right.Evidence) &&
		left.MetricYear == right.MetricYear &&
		left.PolicyName == right.PolicyName &&
		left.PolicyVersion == right.PolicyVersion &&
		left.AssessedAt == right.AssessedAt
}

func buildJournalSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
	journalID uuid.UUID,
	papers []publishedPaper,
	curation curationPayload,
	sources []string,
) (journalSnapshotProfile, error) {
	var (
		title              string
		issnL, issn, eissn *string
	)
	if err := tx.QueryRow(ctx, `
		SELECT display_title, issn_l, issn, eissn
		FROM venues
		WHERE id = $1
		  AND venue_type = 'journal'
	`, journalID).Scan(&title, &issnL, &issn, &eissn); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return journalSnapshotProfile{}, fmt.Errorf(
				"%w: Journal %s is not a current journal Venue",
				ErrCatalogNotReady,
				journalID,
			)
		}
		return journalSnapshotProfile{}, fmt.Errorf(
			"query Journal %s identity: %w",
			journalID,
			err,
		)
	}
	if title == "" || title != strings.TrimSpace(title) {
		return journalSnapshotProfile{}, fmt.Errorf(
			"%w: Journal %s has invalid title",
			ErrCatalogNotReady,
			journalID,
		)
	}
	slug := "journal-" + strings.ReplaceAll(journalID.String(), "-", "")
	for _, paper := range papers {
		if paper.biomedical.Journal.ID != journalID ||
			paper.biomedical.Journal.Slug != slug ||
			paper.biomedical.Journal.Title != title {
			return journalSnapshotProfile{}, fmt.Errorf(
				"%w: Journal %s paper identity is inconsistent",
				ErrCatalogNotReady,
				journalID,
			)
		}
	}

	aliases, err := loadJournalAliases(ctx, tx, journalID)
	if err != nil {
		return journalSnapshotProfile{}, err
	}
	categories, jif, jifText, err := loadJournalMetrics(ctx, tx, input, journalID)
	if err != nil {
		return journalSnapshotProfile{}, err
	}
	curationPayload, err := journalCurationPayload(
		curation,
		categories,
		jifText,
	)
	if err != nil {
		return journalSnapshotProfile{}, err
	}

	issns := distinctNonEmptyStrings(issn, issnL)
	summary := map[string]any{
		"id":               journalID,
		"slug":             slug,
		"title":            title,
		"aliases":          aliases,
		"categories":       categories,
		"eissn":            optionalStringCatalogValue(eissn),
		"issn_l":           optionalStringCatalogValue(issnL),
		"issns":            issns,
		"jcr_metric_year":  input.JCRMetricYear,
		"jif":              jif,
		"paper_count":      catalogValue{State: "known", Value: int64(len(papers))},
		"publisher":        catalogValue{State: "missing"},
		"taxonomy_version": input.SubjectVersion,
	}
	recent, _, err := recentPaperCollection(
		input,
		papers,
		biomedicalDetailWindowDays,
		sources,
	)
	if err != nil {
		return journalSnapshotProfile{}, err
	}
	evidenceGaps := []string{
		"editorial_pattern_analysis_not_published",
		"publisher_identity_not_available",
	}
	detail := clonePayloadMap(summary)
	detail["curation"] = curationPayload
	detail["editorial_patterns"] = map[string]any{
		"analysis": analysisMetadata(
			input,
			biomedicalDetailWindowDays,
			len(papers),
			catalogValue{State: "missing"},
			sources,
			[]string{"journal_pattern_analysis_not_published"},
		),
		"items": []any{},
	}
	detail["evidence_gaps"] = evidenceGaps
	detail["recent_papers"] = recent

	summaryPayload, err := marshalCatalogPayload(summary)
	if err != nil {
		return journalSnapshotProfile{}, err
	}
	detailPayload, err := marshalCatalogPayload(detail)
	if err != nil {
		return journalSnapshotProfile{}, err
	}
	return journalSnapshotProfile{
		resource: publishedBiomedicalResource{
			ID:             journalID,
			Slug:           slug,
			PaperCount:     int64(len(papers)),
			SummaryPayload: summaryPayload,
			DetailPayload:  detailPayload,
		},
		summary:  summary,
		curation: curationPayload,
	}, nil
}

func loadJournalAliases(
	ctx context.Context,
	tx pgx.Tx,
	journalID uuid.UUID,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT alias
		FROM venue_aliases
		WHERE venue_id = $1
		ORDER BY alias
	`, journalID)
	if err != nil {
		return nil, fmt.Errorf("query Journal %s aliases: %w", journalID, err)
	}
	defer rows.Close()

	aliases := make([]string, 0)
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, fmt.Errorf("scan Journal %s alias: %w", journalID, err)
		}
		if alias == "" || alias != strings.TrimSpace(alias) {
			return nil, fmt.Errorf(
				"%w: Journal %s has invalid alias",
				ErrCatalogNotReady,
				journalID,
			)
		}
		aliases = append(aliases, alias)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Journal %s aliases: %w", journalID, err)
	}
	return aliases, nil
}

func loadJournalMetrics(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
	journalID uuid.UUID,
) ([]journalMetricCategoryPayload, catalogValue, string, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			metric.category,
			metric.jif::text,
			metric.quartile,
			metric.metric_status
		FROM jcr_import_receipt_metrics AS receipt_metric
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = receipt_metric.metric_snapshot_id
		WHERE receipt_metric.import_receipt_id = $1
		  AND metric.venue_id = $2
		  AND metric.metric_year = $3
		ORDER BY metric.category
	`, input.JCRImportReceipt, journalID, input.JCRMetricYear)
	if err != nil {
		return nil, catalogValue{}, "", fmt.Errorf(
			"query Journal %s exact JCR metrics: %w",
			journalID,
			err,
		)
	}
	defer rows.Close()

	categories := make([]journalMetricCategoryPayload, 0)
	var canonicalJIF *decimal.Decimal
	for rows.Next() {
		var (
			category, status string
			jif, quartile    *string
		)
		if err := rows.Scan(&category, &jif, &quartile, &status); err != nil {
			return nil, catalogValue{}, "", fmt.Errorf(
				"scan Journal %s exact JCR metric: %w",
				journalID,
				err,
			)
		}
		if category == "" ||
			category != strings.TrimSpace(category) ||
			status != "known" ||
			jif == nil ||
			quartile == nil ||
			*quartile == "" {
			return nil, catalogValue{}, "", fmt.Errorf(
				"%w: Journal %s has incomplete exact JCR Category evidence",
				ErrCatalogNotReady,
				journalID,
			)
		}
		currentJIF, err := decimal.NewFromString(*jif)
		if err != nil || currentJIF.IsNegative() {
			return nil, catalogValue{}, "", fmt.Errorf(
				"%w: Journal %s has invalid exact JIF",
				ErrCatalogNotReady,
				journalID,
			)
		}
		if canonicalJIF == nil {
			canonicalJIF = &currentJIF
		} else if !canonicalJIF.Equal(currentJIF) {
			return nil, catalogValue{}, "", fmt.Errorf(
				"%w: Journal %s has inconsistent exact JIF values across JCR Categories",
				ErrCatalogNotReady,
				journalID,
			)
		}
		categories = append(categories, journalMetricCategoryPayload{
			Name:     category,
			Quartile: *quartile,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, catalogValue{}, "", fmt.Errorf(
			"iterate Journal %s exact JCR metrics: %w",
			journalID,
			err,
		)
	}
	if len(categories) == 0 || canonicalJIF == nil {
		return nil, catalogValue{}, "", fmt.Errorf(
			"%w: Journal %s lacks exact JCR metrics from receipt %s",
			ErrCatalogNotReady,
			journalID,
			input.JCRImportReceipt,
		)
	}
	jifText := canonicalJIF.String()
	return categories,
		catalogValue{State: "known", Value: json.Number(jifText)},
		jifText,
		nil
}

func journalCurationPayload(
	curation curationPayload,
	categories []journalMetricCategoryPayload,
	jif string,
) (map[string]any, error) {
	var matchedRules []string
	if err := json.Unmarshal(curation.MatchedRules, &matchedRules); err != nil {
		return nil, fmt.Errorf(
			"%w: decode accepted Journal matched rules: %v",
			ErrCatalogNotReady,
			err,
		)
	}
	if len(matchedRules) == 0 {
		return nil, fmt.Errorf(
			"%w: accepted Journal curation has no matched rules",
			ErrCatalogNotReady,
		)
	}
	evidence := make([]map[string]string, 0, len(categories)+1)
	for _, category := range categories {
		evidence = append(evidence, map[string]string{
			"label": category.Name,
			"value": category.Quartile,
		})
	}
	evidence = append(evidence, map[string]string{
		"label": "JIF",
		"value": jif,
	})
	return map[string]any{
		"assessed_at":    curation.AssessedAt,
		"decision":       curation.Decision,
		"evidence":       evidence,
		"matched_rules":  matchedRules,
		"metric_year":    curation.MetricYear,
		"policy_name":    curation.PolicyName,
		"policy_version": curation.PolicyVersion,
	}, nil
}

func buildSubjectSnapshot(
	input PublishInput,
	subject taxonomyReference,
	papers []publishedPaper,
	journals map[uuid.UUID]journalSnapshotProfile,
	sources []string,
) (publishedBiomedicalResource, error) {
	if subject.ID == uuid.Nil ||
		!validSlug(subject.Slug) ||
		subject.Name == "" ||
		subject.Name != strings.TrimSpace(subject.Name) {
		return publishedBiomedicalResource{}, fmt.Errorf(
			"%w: invalid biomedical Subject identity",
			ErrCatalogNotReady,
		)
	}
	journalIDs := make(map[uuid.UUID]struct{})
	for _, paper := range papers {
		journalIDs[paper.biomedical.Journal.ID] = struct{}{}
	}
	summary := map[string]any{
		"description":      catalogValue{State: "missing"},
		"id":               subject.ID,
		"jcr_metric_year":  input.JCRMetricYear,
		"journal_count":    catalogValue{State: "known", Value: int64(len(journalIDs))},
		"name":             subject.Name,
		"paper_count":      catalogValue{State: "known", Value: int64(len(papers))},
		"slug":             subject.Slug,
		"taxonomy_version": input.SubjectVersion,
	}

	recentPapers, _, err := papersInWindow(
		input,
		papers,
		biomedicalDetailWindowDays,
	)
	if err != nil {
		return publishedBiomedicalResource{}, err
	}
	activeJournals, err := journalActivityItems(recentPapers, journals)
	if err != nil {
		return publishedBiomedicalResource{}, err
	}
	meshDistribution, err := meshDistributionItems(recentPapers)
	if err != nil {
		return publishedBiomedicalResource{}, err
	}
	publicationTypeDistribution, err := publicationTypeDistributionItems(recentPapers)
	if err != nil {
		return publishedBiomedicalResource{}, err
	}
	recentCollection, _, err := recentPaperCollection(
		input,
		papers,
		biomedicalDetailWindowDays,
		sources,
	)
	if err != nil {
		return publishedBiomedicalResource{}, err
	}

	detail := clonePayloadMap(summary)
	detail["active_journals"] = map[string]any{
		"analysis": analysisMetadata(
			input,
			biomedicalDetailWindowDays,
			len(recentPapers),
			dateCoverageValue(papers),
			sources,
			[]string{"publication_change_baseline_not_published"},
		),
		"items": activeJournals,
	}
	detail["mesh_distribution"] = map[string]any{
		"analysis": analysisMetadata(
			input,
			biomedicalDetailWindowDays,
			meshDistribution.knownPapers,
			ratioCatalogValue(meshDistribution.knownPapers, len(recentPapers)),
			sources,
			[]string{},
		),
		"items": distributionItems(
			meshDistribution,
			meshDistribution.knownPapers,
		),
	}
	detail["publication_type_distribution"] = map[string]any{
		"analysis": analysisMetadata(
			input,
			biomedicalDetailWindowDays,
			publicationTypeDistribution.knownPapers,
			ratioCatalogValue(
				publicationTypeDistribution.knownPapers,
				len(recentPapers),
			),
			sources,
			[]string{},
		),
		"items": distributionItems(
			publicationTypeDistribution,
			publicationTypeDistribution.knownPapers,
		),
	}
	detail["recent_papers"] = recentCollection
	detail["trend_estimates"] = map[string]any{
		"analysis": analysisMetadata(
			input,
			biomedicalDetailWindowDays,
			len(recentPapers),
			catalogValue{State: "missing"},
			sources,
			[]string{"subject_trend_analysis_not_published"},
		),
		"items": []any{},
	}
	detail["evidence_gaps"] = []string{
		"subject_citation_momentum_not_published",
		"independent_team_analysis_not_published",
		"subject_trend_analysis_not_published",
	}

	summaryPayload, err := marshalCatalogPayload(summary)
	if err != nil {
		return publishedBiomedicalResource{}, err
	}
	detailPayload, err := marshalCatalogPayload(detail)
	if err != nil {
		return publishedBiomedicalResource{}, err
	}
	return publishedBiomedicalResource{
		ID:             subject.ID,
		Slug:           subject.Slug,
		PaperCount:     int64(len(papers)),
		SummaryPayload: summaryPayload,
		DetailPayload:  detailPayload,
	}, nil
}

func buildHomeSnapshot(
	input PublishInput,
	papers []publishedPaper,
	journals map[uuid.UUID]journalSnapshotProfile,
	sources []string,
	citationMomentum map[string]any,
) (json.RawMessage, error) {
	latestPapers, _, err := recentPaperCollection(
		input,
		papers,
		biomedicalHomeWindowDays,
		sources,
	)
	if err != nil {
		return nil, err
	}
	recentPapers, _, err := papersInWindow(
		input,
		papers,
		biomedicalDetailWindowDays,
	)
	if err != nil {
		return nil, err
	}
	activeJournals, err := journalActivityItems(recentPapers, journals)
	if err != nil {
		return nil, err
	}
	publicationUpdates, err := buildPublicationUpdates(input, papers)
	if err != nil {
		return nil, err
	}
	knownCitations := 0
	knownMeSH := 0
	knownPublicationTypes := 0
	for _, paper := range papers {
		if paper.CitationCountState == "known" {
			knownCitations++
		}
		if paper.biomedical.MeSHHeadings.State == "known" {
			knownMeSH++
		}
		if paper.biomedical.PublicationTypesState.State == "known" {
			knownPublicationTypes++
		}
	}
	coverageAnalysis := analysisMetadata(
		input,
		biomedicalDetailWindowDays,
		len(papers),
		catalogValue{State: "known", Value: float64(1)},
		sources,
		[]string{
			"citation_snapshots_not_published",
			"open_fulltext_not_published",
		},
	)
	unavailableAnalysis := func(signal string) map[string]any {
		return analysisMetadata(
			input,
			biomedicalDetailWindowDays,
			len(papers),
			catalogValue{State: "missing"},
			sources,
			[]string{signal},
		)
	}
	return marshalCatalogPayload(map[string]any{
		"active_journals": map[string]any{
			"analysis": analysisMetadata(
				input,
				biomedicalDetailWindowDays,
				len(recentPapers),
				dateCoverageValue(papers),
				sources,
				[]string{"publication_change_baseline_not_published"},
			),
			"items": activeJournals,
		},
		"citation_momentum": citationMomentum,
		"coverage": map[string]any{
			"analysis":                        coverageAnalysis,
			"citation_coverage_ratio":         ratioCatalogValue(knownCitations, len(papers)),
			"jcr_metric_year":                 input.JCRMetricYear,
			"mesh_coverage_ratio":             ratioCatalogValue(knownMeSH, len(papers)),
			"publication_type_coverage_ratio": ratioCatalogValue(knownPublicationTypes, len(papers)),
			"taxonomy_version":                input.SubjectVersion,
		},
		"entity_momentum": map[string]any{
			"analysis": unavailableAnalysis("entity_trend_analysis_not_published"),
			"items":    []any{},
		},
		"evidence_gaps": []string{
			"entity_trend_analysis_not_published",
			"journal_pattern_analysis_not_published",
			"open_fulltext_not_published",
			"research_opportunity_analysis_not_published",
			"subject_trend_analysis_not_published",
		},
		"generated_at":        input.GeneratedAt.UTC().Format(time.RFC3339Nano),
		"latest_papers":       latestPapers,
		"publication_updates": publicationUpdates,
		"research_opportunities": map[string]any{
			"analysis": unavailableAnalysis(
				"research_opportunity_analysis_not_published",
			),
			"items": []any{},
		},
		"scope": map[string]any{
			"jcr_metric_year":  input.JCRMetricYear,
			"taxonomy_version": input.SubjectVersion,
		},
		"subject_momentum": map[string]any{
			"analysis": unavailableAnalysis("subject_trend_analysis_not_published"),
			"items":    []any{},
		},
	})
}

type publicationUpdateItem struct {
	paper        json.RawMessage
	eventKind    string
	eventDate    time.Time
	canonicalKey string
	event        map[string]any
}

func buildPublicationUpdates(
	input PublishInput,
	papers []publishedPaper,
) (map[string]any, error) {
	generatedAt := input.GeneratedAt.UTC()
	calendarDate := time.Date(
		generatedAt.Year(),
		generatedAt.Month(),
		generatedAt.Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	)
	recentStart := calendarDate.AddDate(0, 0, -(biomedicalHomeWindowDays - 1))
	formalItems := make([]publicationUpdateItem, 0)
	acceptanceItems := make([]publicationUpdateItem, 0)
	onlineFirstItems := make([]publicationUpdateItem, 0)
	formalKnownPapers := 0
	acceptanceKnownPapers := 0
	onlineFirstKnownPapers := 0

	for _, paper := range papers {
		publication := paper.PublicationState
		if publication == nil {
			continue
		}
		if publication.PrintPublished.State == "known" ||
			publication.ElectronicPublished.State == "known" {
			formalKnownPapers++
		}
		if publication.Accepted.State == "known" {
			acceptanceKnownPapers++
		}
		if publication.AheadOfPrint.State == "known" {
			onlineFirstKnownPapers++
		}

		for _, event := range []struct {
			kind  string
			state publishedPublicationEventState
		}{
			{
				kind:  "print_published",
				state: publication.PrintPublished,
			},
			{
				kind:  "electronic_published",
				state: publication.ElectronicPublished,
			},
		} {
			if event.state.State != "known" {
				continue
			}
			if event.state.Date == nil {
				return nil, fmt.Errorf(
					"%w: paper %s has known %s publication state without date",
					ErrCatalogNotReady,
					paper.ID,
					event.kind,
				)
			}
			if event.state.Date.Equal(calendarDate) {
				item, err := newPublicationUpdateItem(
					paper,
					*publication,
					event.kind,
					event.state,
				)
				if err != nil {
					return nil, err
				}
				formalItems = append(formalItems, item)
			}
		}

		for _, event := range []struct {
			kind   string
			state  publishedPublicationEventState
			target *[]publicationUpdateItem
		}{
			{
				kind:   "accepted",
				state:  publication.Accepted,
				target: &acceptanceItems,
			},
			{
				kind:   "ahead_of_print",
				state:  publication.AheadOfPrint,
				target: &onlineFirstItems,
			},
		} {
			if event.state.State != "known" {
				continue
			}
			if event.state.Date == nil {
				return nil, fmt.Errorf(
					"%w: paper %s has known %s publication state without date",
					ErrCatalogNotReady,
					paper.ID,
					event.kind,
				)
			}
			if event.state.Date.Before(recentStart) ||
				event.state.Date.After(calendarDate) {
				continue
			}
			item, err := newPublicationUpdateItem(
				paper,
				*publication,
				event.kind,
				event.state,
			)
			if err != nil {
				return nil, err
			}
			*event.target = append(*event.target, item)
		}
	}

	sortPublicationUpdateItems(formalItems)
	sortPublicationUpdateItems(acceptanceItems)
	sortPublicationUpdateItems(onlineFirstItems)
	return map[string]any{
		"calendar_date":     calendarDate.Format("2006-01-02"),
		"calendar_timezone": "UTC",
		"formal_publications_today": buildPublicationUpdateCollection(
			input,
			1,
			formalItems,
			formalKnownPapers,
			len(papers),
		),
		"recent_acceptances": buildPublicationUpdateCollection(
			input,
			biomedicalHomeWindowDays,
			acceptanceItems,
			acceptanceKnownPapers,
			len(papers),
		),
		"recent_online_first": buildPublicationUpdateCollection(
			input,
			biomedicalHomeWindowDays,
			onlineFirstItems,
			onlineFirstKnownPapers,
			len(papers),
		),
	}, nil
}

func newPublicationUpdateItem(
	paper publishedPaper,
	publication publishedPublicationState,
	eventKind string,
	eventState publishedPublicationEventState,
) (publicationUpdateItem, error) {
	if eventState.State != "known" ||
		eventState.Date == nil ||
		eventState.Provenance == nil {
		return publicationUpdateItem{}, fmt.Errorf(
			"%w: paper %s publication event %s is not fully known",
			ErrCatalogNotReady,
			paper.ID,
			eventKind,
		)
	}
	return publicationUpdateItem{
		paper:        paper.SummaryPayload,
		eventKind:    eventKind,
		eventDate:    *eventState.Date,
		canonicalKey: paper.CanonicalKey,
		event: map[string]any{
			"kind":               eventKind,
			"date":               eventState.Date.Format("2006-01-02"),
			"date_precision":     "day",
			"publication_status": publication.PublicationStatus,
			"publication_model":  publication.PublicationModel,
			"provenance":         *eventState.Provenance,
		},
	}, nil
}

func sortPublicationUpdateItems(items []publicationUpdateItem) {
	sort.Slice(items, func(i, j int) bool {
		if !items[i].eventDate.Equal(items[j].eventDate) {
			return items[i].eventDate.After(items[j].eventDate)
		}
		if items[i].canonicalKey != items[j].canonicalKey {
			return items[i].canonicalKey < items[j].canonicalKey
		}
		return items[i].eventKind < items[j].eventKind
	})
}

func buildPublicationUpdateCollection(
	input PublishInput,
	windowDays int,
	items []publicationUpdateItem,
	knownStatePapers int,
	eligiblePapers int,
) map[string]any {
	payloadItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		payloadItems = append(payloadItems, map[string]any{
			"paper": item.paper,
			"event": item.event,
		})
	}
	return map[string]any{
		"analysis": analysisMetadata(
			input,
			windowDays,
			len(payloadItems),
			ratioCatalogValue(knownStatePapers, eligiblePapers),
			[]string{"pubmed"},
			[]string{},
		),
		"items": payloadItems,
		"pagination": map[string]any{
			"has_more":    false,
			"limit":       len(payloadItems),
			"next_cursor": nil,
			"total":       len(payloadItems),
		},
	}
}

func analysisMetadata(
	input PublishInput,
	windowDays int,
	sampleSize int,
	coverage catalogValue,
	sources []string,
	missingSignals []string,
) map[string]any {
	return map[string]any{
		"coverage_ratio": coverage,
		"formula_version": catalogValue{
			State: "known",
			Value: input.FormulaVersion,
		},
		"generated_at": catalogValue{
			State: "known",
			Value: input.GeneratedAt.UTC().Format(time.RFC3339Nano),
		},
		"missing_signals": missingSignals,
		"sample_size": catalogValue{
			State: "known",
			Value: int64(sampleSize),
		},
		"sources": catalogValue{
			State: "known",
			Value: sources,
		},
		"window_days": catalogValue{
			State: "known",
			Value: windowDays,
		},
	}
}

func recentPaperCollection(
	input PublishInput,
	papers []publishedPaper,
	windowDays int,
	sources []string,
) (map[string]any, catalogValue, error) {
	recent, knownDates, err := papersInWindow(input, papers, windowDays)
	if err != nil {
		return nil, catalogValue{}, err
	}
	items := make([]json.RawMessage, len(recent))
	for index := range recent {
		items[index] = recent[index].SummaryPayload
	}
	coverage := ratioCatalogValue(knownDates, len(papers))
	return map[string]any{
		"analysis": analysisMetadata(
			input,
			windowDays,
			len(recent),
			coverage,
			sources,
			[]string{},
		),
		"items": items,
		"pagination": map[string]any{
			"has_more":    false,
			"limit":       len(items),
			"next_cursor": nil,
			"total":       len(items),
		},
	}, coverage, nil
}

func papersInWindow(
	input PublishInput,
	papers []publishedPaper,
	windowDays int,
) ([]publishedPaper, int, error) {
	if windowDays < 1 {
		return nil, 0, fmt.Errorf(
			"%w: biomedical analysis window must be positive",
			ErrCatalogNotReady,
		)
	}
	cutoff := input.GeneratedAt.UTC().AddDate(0, 0, -windowDays)
	recent := make([]publishedPaper, 0, len(papers))
	knownDates := 0
	for _, paper := range papers {
		if paper.PublishedAtState != "known" {
			continue
		}
		if paper.PublishedAt == nil {
			return nil, 0, fmt.Errorf(
				"%w: paper %s has known published_at without value",
				ErrCatalogNotReady,
				paper.ID,
			)
		}
		knownDates++
		if paper.PublishedAt.Before(cutoff) ||
			paper.PublishedAt.After(input.GeneratedAt.UTC()) {
			continue
		}
		recent = append(recent, paper)
	}
	sort.Slice(recent, func(i, j int) bool {
		if !recent[i].PublishedAt.Equal(*recent[j].PublishedAt) {
			return recent[i].PublishedAt.After(*recent[j].PublishedAt)
		}
		return recent[i].CanonicalKey < recent[j].CanonicalKey
	})
	return recent, knownDates, nil
}

func journalActivityItems(
	papers []publishedPaper,
	journals map[uuid.UUID]journalSnapshotProfile,
) ([]map[string]any, error) {
	counts := make(map[uuid.UUID]int64)
	for _, paper := range papers {
		counts[paper.biomedical.Journal.ID]++
	}
	type activity struct {
		id    uuid.UUID
		count int64
		title string
	}
	values := make([]activity, 0, len(counts))
	for journalID, count := range counts {
		profile, found := journals[journalID]
		if !found {
			return nil, fmt.Errorf(
				"%w: activity references unpublished Journal %s",
				ErrCatalogNotReady,
				journalID,
			)
		}
		title, _ := profile.summary["title"].(string)
		values = append(values, activity{id: journalID, count: count, title: title})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].count != values[j].count {
			return values[i].count > values[j].count
		}
		if values[i].title != values[j].title {
			return values[i].title < values[j].title
		}
		return values[i].id.String() < values[j].id.String()
	})
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		items = append(items, map[string]any{
			"journal":                  clonePayloadMap(journals[value.id].summary),
			"paper_count":              catalogValue{State: "known", Value: value.count},
			"publication_change_ratio": catalogValue{State: "missing"},
		})
	}
	return items, nil
}

func meshDistributionItems(
	papers []publishedPaper,
) (distributionCounter, error) {
	counter := distributionCounter{counts: make(map[string]int64)}
	for _, paper := range papers {
		switch paper.biomedical.MeSHHeadings.State {
		case "known":
			headings, ok := paper.biomedical.MeSHHeadings.Value.([]meshHeadingPayload)
			if !ok {
				return distributionCounter{}, fmt.Errorf(
					"%w: paper %s has invalid in-memory MeSH payload",
					ErrCatalogNotReady,
					paper.ID,
				)
			}
			counter.knownPapers++
			seen := make(map[string]struct{}, len(headings))
			for _, heading := range headings {
				if heading.Label == "" ||
					heading.Label != strings.TrimSpace(heading.Label) {
					return distributionCounter{}, fmt.Errorf(
						"%w: paper %s has invalid MeSH label",
						ErrCatalogNotReady,
						paper.ID,
					)
				}
				seen[heading.Label] = struct{}{}
			}
			for label := range seen {
				counter.counts[label]++
			}
		case "missing":
		default:
			return distributionCounter{}, fmt.Errorf(
				"%w: paper %s has unsupported MeSH state %q",
				ErrCatalogNotReady,
				paper.ID,
				paper.biomedical.MeSHHeadings.State,
			)
		}
	}
	return counter, nil
}

func publicationTypeDistributionItems(
	papers []publishedPaper,
) (distributionCounter, error) {
	counter := distributionCounter{counts: make(map[string]int64)}
	for _, paper := range papers {
		switch paper.biomedical.PublicationTypesState.State {
		case "known":
			counter.knownPapers++
			seen := make(map[string]struct{}, len(paper.biomedical.PublicationTypes))
			for _, label := range paper.biomedical.PublicationTypes {
				if label == "" || label != strings.TrimSpace(label) {
					return distributionCounter{}, fmt.Errorf(
						"%w: paper %s has invalid Publication Type label",
						ErrCatalogNotReady,
						paper.ID,
					)
				}
				seen[label] = struct{}{}
			}
			for label := range seen {
				counter.counts[label]++
			}
		case "missing":
		default:
			return distributionCounter{}, fmt.Errorf(
				"%w: paper %s has unsupported Publication Type state %q",
				ErrCatalogNotReady,
				paper.ID,
				paper.biomedical.PublicationTypesState.State,
			)
		}
	}
	return counter, nil
}

func distributionItems(
	counter distributionCounter,
	totalPapers int,
) []map[string]any {
	type distribution struct {
		label string
		count int64
	}
	values := make([]distribution, 0, len(counter.counts))
	for label, count := range counter.counts {
		values = append(values, distribution{label: label, count: count})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].count != values[j].count {
			return values[i].count > values[j].count
		}
		return values[i].label < values[j].label
	})
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		items = append(items, map[string]any{
			"count": catalogValue{State: "known", Value: value.count},
			"label": value.label,
			"ratio": ratioCatalogValue(int(value.count), totalPapers),
		})
	}
	return items
}

func biomedicalAnalysisSources(
	papers []publishedPaper,
	jcrSource string,
) []string {
	unique := map[string]struct{}{jcrSource: {}}
	for _, paper := range papers {
		for _, source := range paper.SourceNames {
			unique[source] = struct{}{}
		}
	}
	values := make([]string, 0, len(unique))
	for source := range unique {
		values = append(values, source)
	}
	sort.Strings(values)
	return values
}

func dateCoverageValue(papers []publishedPaper) catalogValue {
	known := 0
	for _, paper := range papers {
		if paper.PublishedAtState == "known" {
			known++
		}
	}
	return ratioCatalogValue(known, len(papers))
}

func ratioCatalogValue(numerator, denominator int) catalogValue {
	if denominator == 0 {
		return catalogValue{State: "missing"}
	}
	return catalogValue{
		State: "known",
		Value: float64(numerator) / float64(denominator),
	}
}

func optionalStringCatalogValue(value *string) catalogValue {
	if value == nil {
		return catalogValue{State: "missing"}
	}
	return catalogValue{State: "known", Value: *value}
}

func distinctNonEmptyStrings(values ...*string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == nil || *value == "" {
			continue
		}
		unique[*value] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func clonePayloadMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func sortBiomedicalResources(resources []publishedBiomedicalResource) {
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].PaperCount != resources[j].PaperCount {
			return resources[i].PaperCount > resources[j].PaperCount
		}
		if resources[i].Slug != resources[j].Slug {
			return resources[i].Slug < resources[j].Slug
		}
		return resources[i].ID.String() < resources[j].ID.String()
	})
}

func bindCatalogGeneration(
	payload json.RawMessage,
	generationID uuid.UUID,
) (json.RawMessage, error) {
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, fmt.Errorf("decode biomedical Home snapshot: %w", err)
	}
	document["catalog_generation"] = generationID.String()
	return marshalCatalogPayload(document)
}
