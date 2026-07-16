package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const publicCatalogPublisherLockID int64 = 0x50434154414c4f47

var slugSeparatorPattern = regexp.MustCompile(`[^a-z0-9]+`)

type Publisher struct {
	database transactionBeginner
}

type catalogSnapshot struct {
	stats   json.RawMessage
	papers  []publishedPaper
	topics  []publishedTaxonomy
	methods []publishedTaxonomy
	sources []sourceRevisionFact
}

type sourceRevisionFact struct {
	LogicalSource              string `json:"logical_source"`
	EventKey                   string `json:"event_key"`
	RawEventID                 string `json:"raw_event_id"`
	SourceRecordID             string `json:"source_record_id"`
	WorkID                     string `json:"work_id"`
	SourceTime                 string `json:"source_time"`
	TieBreakKey                string `json:"tie_break_key"`
	Position                   int64  `json:"position"`
	ScopePolicyVersion         string `json:"scope_policy_version"`
	ProjectionPolicyVersion    string `json:"projection_policy_version"`
	NormalizationPolicyVersion string `json:"normalization_policy_version"`
	NormalizedPayload          any    `json:"normalized_payload"`
}

type sourceState struct {
	logicalSource              string
	eventKey                   string
	rawEventID                 uuid.UUID
	sourceRecordID             uuid.UUID
	workID                     uuid.UUID
	sourceTime                 time.Time
	tieBreakKey                string
	position                   int64
	scopeStatus                string
	scopePolicyVersion         string
	projectionPolicyVersion    string
	isDeleted                  bool
	hasNormalized              bool
	normalizationPolicyVersion string
	normalizedPayload          any
	hasWorkLink                bool
}

type workSources struct {
	states          []sourceState
	sourceRecordIDs []uuid.UUID
}

type publishedPaper struct {
	ID                 uuid.UUID
	CanonicalKey       string
	Title              string
	SearchText         string
	PublishedAtState   string
	PublishedAt        *time.Time
	PaperTypeState     string
	PaperType          *string
	LifecycleStatus    string
	TopicSlugs         []string
	MethodSlugs        []string
	SourceNames        []string
	HasCodeState       string
	HasCodeValue       *bool
	HasDataState       string
	HasDataValue       *bool
	HasBenchmarkState  string
	HasBenchmarkValue  *bool
	CitationCountState string
	CitationCountValue *int64
	TrendScoreState    string
	TrendScoreValue    *string
	SummaryPayload     json.RawMessage
	DetailPayload      json.RawMessage
}

type publishedTaxonomy struct {
	ID             uuid.UUID
	Slug           string
	Name           string
	PaperCount     int64
	SummaryPayload json.RawMessage
	DetailPayload  json.RawMessage
}

type taxonomyFact struct {
	ID          uuid.UUID
	Name        string
	Description *string
	Slug        string
	PaperIDs    []uuid.UUID
}

type assertionValue struct {
	State string
	Value json.RawMessage
}

type catalogValue struct {
	State string `json:"state"`
	Value any    `json:"value,omitempty"`
}

type sourceProvenancePayload struct {
	Source                     string    `json:"source"`
	EventKey                   string    `json:"event_key"`
	SourceRecordID             uuid.UUID `json:"source_record_id"`
	SourceTime                 string    `json:"source_time"`
	NormalizationPolicyVersion string    `json:"normalization_policy_version"`
	ScopePolicyVersion         string    `json:"scope_policy_version"`
	ProjectionPolicyVersion    string    `json:"projection_policy_version"`
}

type taxonomyReference struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

type authorReference struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	ORCID           *string    `json:"orcid,omitempty"`
	InstitutionID   *uuid.UUID `json:"institution_id,omitempty"`
	InstitutionName *string    `json:"institution_name,omitempty"`
	Position        int        `json:"position"`
	IsCorresponding bool       `json:"is_corresponding"`
}

type curationPayload struct {
	Decision      string          `json:"decision"`
	MatchedRules  json.RawMessage `json:"matched_rules"`
	Evidence      json.RawMessage `json:"evidence"`
	MetricYear    int             `json:"metric_year"`
	PolicyName    string          `json:"policy_name"`
	PolicyVersion int             `json:"policy_version"`
	AssessedAt    string          `json:"assessed_at"`
}

func NewPublisher(database transactionBeginner) (*Publisher, error) {
	if database == nil {
		return nil, errors.New("catalog publisher requires a PostgreSQL database")
	}
	return &Publisher{database: database}, nil
}

func (publisher *Publisher) PublishCurrent(
	ctx context.Context,
	input PublishInput,
) (generation Generation, returnErr error) {
	if err := validatePublishInput(input); err != nil {
		return Generation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Generation{}, err
	}

	tx, err := publisher.database.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.Serializable,
		AccessMode: pgx.ReadWrite,
	})
	if err != nil {
		return Generation{}, fmt.Errorf("begin public catalog publication transaction: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback(context.Background())
		}
	}()

	if err := verifyPublisherTransaction(ctx, tx); err != nil {
		return Generation{}, err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", publicCatalogPublisherLockID); err != nil {
		return Generation{}, fmt.Errorf("lock public catalog publication: %w", err)
	}

	snapshot, sourceRevision, err := buildCurrentSnapshot(ctx, tx, input)
	if err != nil {
		return Generation{}, err
	}

	existing, found, err := existingPublishedGeneration(ctx, tx, sourceRevision)
	if err != nil {
		return Generation{}, err
	}
	if found {
		if err := setCurrentGeneration(ctx, tx, existing.ID); err != nil {
			return Generation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Generation{}, fmt.Errorf("commit reused public catalog generation: %w", err)
		}
		return existing, nil
	}

	generation, err = persistSnapshot(ctx, tx, input, sourceRevision, snapshot)
	if err != nil {
		return Generation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Generation{}, fmt.Errorf("commit public catalog publication: %w", err)
	}
	return generation, nil
}

func validatePublishInput(input PublishInput) error {
	if input.FormulaVersion == "" ||
		input.FormulaVersion != strings.TrimSpace(input.FormulaVersion) {
		return fmt.Errorf("%w: formula version must be non-empty and trimmed", ErrCatalogNotReady)
	}
	if input.GeneratedAt.IsZero() {
		return fmt.Errorf("%w: generated_at is required", ErrCatalogNotReady)
	}
	return nil
}

func verifyPublisherTransaction(ctx context.Context, tx pgx.Tx) error {
	var isolation, readOnly string
	if err := tx.QueryRow(ctx, `
		SELECT
			current_setting('transaction_isolation'),
			current_setting('transaction_read_only')
	`).Scan(&isolation, &readOnly); err != nil {
		return fmt.Errorf("verify public catalog publication transaction: %w", err)
	}
	if isolation != "serializable" || readOnly != "off" {
		return fmt.Errorf(
			"public catalog publication transaction invariant violated: isolation=%q read_only=%q",
			isolation,
			readOnly,
		)
	}
	return nil
}

func buildCurrentSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
) (catalogSnapshot, string, error) {
	states, err := loadCurrentSourceStates(ctx, tx)
	if err != nil {
		return catalogSnapshot{}, "", err
	}

	visible := make(map[uuid.UUID]*workSources)
	revisionSources := make([]sourceRevisionFact, 0)
	for _, state := range states {
		if state.isDeleted {
			continue
		}
		switch state.scopeStatus {
		case "pending":
			return catalogSnapshot{}, "", fmt.Errorf(
				"%w: source %s event %s is pending",
				ErrCatalogNotReady,
				state.logicalSource,
				state.eventKey,
			)
		case "excluded":
			continue
		case "included":
		default:
			return catalogSnapshot{}, "", fmt.Errorf(
				"%w: source %s event %s has invalid scope status %q",
				ErrCatalogNotReady,
				state.logicalSource,
				state.eventKey,
				state.scopeStatus,
			)
		}

		if state.sourceRecordID == uuid.Nil ||
			state.workID == uuid.Nil ||
			!state.hasNormalized ||
			!state.hasWorkLink {
			return catalogSnapshot{}, "", fmt.Errorf(
				"%w: included source %s event %s lacks normalized work provenance",
				ErrCatalogNotReady,
				state.logicalSource,
				state.eventKey,
			)
		}

		entry := visible[state.workID]
		if entry == nil {
			entry = &workSources{}
			visible[state.workID] = entry
		}
		entry.states = append(entry.states, state)
		entry.sourceRecordIDs = append(entry.sourceRecordIDs, state.sourceRecordID)
		revisionSources = append(revisionSources, sourceRevisionFact{
			LogicalSource:              state.logicalSource,
			EventKey:                   state.eventKey,
			RawEventID:                 state.rawEventID.String(),
			SourceRecordID:             state.sourceRecordID.String(),
			WorkID:                     state.workID.String(),
			SourceTime:                 state.sourceTime.UTC().Format(time.RFC3339Nano),
			TieBreakKey:                state.tieBreakKey,
			Position:                   state.position,
			ScopePolicyVersion:         state.scopePolicyVersion,
			ProjectionPolicyVersion:    state.projectionPolicyVersion,
			NormalizationPolicyVersion: state.normalizationPolicyVersion,
			NormalizedPayload:          state.normalizedPayload,
		})
	}
	if len(visible) == 0 {
		return catalogSnapshot{}, "", ErrEmptyDomain
	}

	workIDs := make([]uuid.UUID, 0, len(visible))
	for workID := range visible {
		workIDs = append(workIDs, workID)
	}
	sort.Slice(workIDs, func(i, j int) bool {
		return workIDs[i].String() < workIDs[j].String()
	})
	sort.Slice(revisionSources, func(i, j int) bool {
		left, right := revisionSources[i], revisionSources[j]
		if left.LogicalSource != right.LogicalSource {
			return left.LogicalSource < right.LogicalSource
		}
		return left.EventKey < right.EventKey
	})

	topicFacts := make(map[uuid.UUID]*taxonomyFact)
	methodFacts := make(map[uuid.UUID]*taxonomyFact)
	papers := make([]publishedPaper, 0, len(workIDs))
	for _, workID := range workIDs {
		paper, topics, methods, err := buildPaper(
			ctx,
			tx,
			workID,
			visible[workID],
		)
		if err != nil {
			return catalogSnapshot{}, "", err
		}
		papers = append(papers, paper)
		if err := mergeTaxonomyFacts(topicFacts, topics, workID); err != nil {
			return catalogSnapshot{}, "", err
		}
		if err := mergeTaxonomyFacts(methodFacts, methods, workID); err != nil {
			return catalogSnapshot{}, "", err
		}
	}
	sort.Slice(papers, func(i, j int) bool {
		return papers[i].CanonicalKey < papers[j].CanonicalKey
	})

	topics, err := publishTaxonomies(topicFacts)
	if err != nil {
		return catalogSnapshot{}, "", err
	}
	methods, err := publishTaxonomies(methodFacts)
	if err != nil {
		return catalogSnapshot{}, "", err
	}
	stats, err := buildStats(input, papers, len(topics), len(methods))
	if err != nil {
		return catalogSnapshot{}, "", err
	}

	snapshot := catalogSnapshot{
		stats:   stats,
		papers:  papers,
		topics:  topics,
		methods: methods,
		sources: revisionSources,
	}
	sourceRevision, err := snapshotRevision(input.FormulaVersion, snapshot)
	if err != nil {
		return catalogSnapshot{}, "", err
	}
	return snapshot, sourceRevision, nil
}

func loadCurrentSourceStates(ctx context.Context, tx pgx.Tx) ([]sourceState, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			state.logical_source,
			state.event_key,
			state.raw_event_id,
			COALESCE(state.source_record_uuid::text, ''),
			COALESCE(state.work_id::text, ''),
			state.source_time,
			state.tie_break_key,
			state.position,
			state.scope_status,
			state.scope_policy_version,
			state.projection_policy_version,
			state.is_deleted,
			normalized.raw_event_id IS NOT NULL,
			COALESCE(normalized.normalization_policy_version, ''),
			COALESCE(normalized.normalized_payload::text, ''),
			association.source_record_id IS NOT NULL
		FROM ingestion_source_states AS state
		LEFT JOIN ingestion_normalized_records AS normalized
		  ON normalized.raw_event_id = state.raw_event_id
		 AND normalized.source_record_uuid = state.source_record_uuid
		LEFT JOIN source_record_works AS association
		  ON association.source_record_id = state.source_record_uuid
		 AND association.work_id = state.work_id
		ORDER BY state.logical_source, state.event_key
	`)
	if err != nil {
		return nil, fmt.Errorf("query current ingestion source states: %w", err)
	}
	defer rows.Close()

	states := make([]sourceState, 0)
	for rows.Next() {
		var (
			state             sourceState
			rawEventID        string
			sourceRecordID    string
			workID            string
			normalizedPayload string
		)
		if err := rows.Scan(
			&state.logicalSource,
			&state.eventKey,
			&rawEventID,
			&sourceRecordID,
			&workID,
			&state.sourceTime,
			&state.tieBreakKey,
			&state.position,
			&state.scopeStatus,
			&state.scopePolicyVersion,
			&state.projectionPolicyVersion,
			&state.isDeleted,
			&state.hasNormalized,
			&state.normalizationPolicyVersion,
			&normalizedPayload,
			&state.hasWorkLink,
		); err != nil {
			return nil, fmt.Errorf("scan current ingestion source state: %w", err)
		}
		var err error
		state.rawEventID, err = uuid.Parse(rawEventID)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid raw event ID", ErrCatalogNotReady)
		}
		if sourceRecordID != "" {
			state.sourceRecordID, err = uuid.Parse(sourceRecordID)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid source record ID", ErrCatalogNotReady)
			}
		}
		if workID != "" {
			state.workID, err = uuid.Parse(workID)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid work ID", ErrCatalogNotReady)
			}
		}
		if state.hasNormalized {
			if state.normalizationPolicyVersion == "" || normalizedPayload == "" {
				return nil, fmt.Errorf(
					"%w: normalized source state is structurally incomplete",
					ErrCatalogNotReady,
				)
			}
			if err := json.Unmarshal([]byte(normalizedPayload), &state.normalizedPayload); err != nil {
				return nil, fmt.Errorf(
					"%w: decode normalized source payload: %v",
					ErrCatalogNotReady,
					err,
				)
			}
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current ingestion source states: %w", err)
	}
	return states, nil
}

func buildPaper(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	sources *workSources,
) (publishedPaper, []taxonomyFact, []taxonomyFact, error) {
	var (
		paper       publishedPaper
		venueIDText string
	)
	err := tx.QueryRow(ctx, `
		SELECT
			id,
			canonical_key,
			title,
			status,
			COALESCE(venue_id::text, '')
		FROM works
		WHERE id = $1
	`, workID).Scan(
		&paper.ID,
		&paper.CanonicalKey,
		&paper.Title,
		&paper.LifecycleStatus,
		&venueIDText,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return publishedPaper{}, nil, nil, fmt.Errorf(
			"%w: included work %s does not exist",
			ErrCatalogNotReady,
			workID,
		)
	}
	if err != nil {
		return publishedPaper{}, nil, nil, fmt.Errorf("query included work %s: %w", workID, err)
	}
	if paper.CanonicalKey != strings.TrimSpace(paper.CanonicalKey) ||
		paper.Title != strings.TrimSpace(paper.Title) ||
		!validLifecycleStatus(paper.LifecycleStatus) {
		return publishedPaper{}, nil, nil, fmt.Errorf(
			"%w: included work %s has invalid normalized identity",
			ErrCatalogNotReady,
			workID,
		)
	}

	assertions, err := loadAssertions(ctx, tx, workID, sources.sourceRecordIDs)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	publishedAtValue, publishedAt, err := publishedAtFromAssertion(assertions["published_at"])
	if err != nil {
		return publishedPaper{}, nil, nil, catalogWorkError(workID, "published_at", err)
	}
	paper.PublishedAtState = publishedAtValue.State
	paper.PublishedAt = publishedAt

	paperTypeValue, paperType, err := paperTypeFromAssertion(assertions["paper_type"])
	if err != nil {
		return publishedPaper{}, nil, nil, catalogWorkError(workID, "paper_type", err)
	}
	paper.PaperTypeState = paperTypeValue.State
	paper.PaperType = paperType

	abstractValue, abstract, err := stringFromAssertion(assertions["abstract"], "missing")
	if err != nil {
		return publishedPaper{}, nil, nil, catalogWorkError(workID, "abstract", err)
	}

	hasCode, err := relationValue(
		ctx,
		tx,
		`SELECT EXISTS (
			SELECT 1 FROM work_code_repositories WHERE work_id = $1
		)`,
		workID,
		assertions["code_urls"],
	)
	if err != nil {
		return publishedPaper{}, nil, nil, catalogWorkError(workID, "has_code", err)
	}
	hasData, err := relationValue(
		ctx,
		tx,
		`SELECT EXISTS (
			SELECT 1 FROM work_datasets WHERE work_id = $1
		)`,
		workID,
		assertionValue{},
	)
	if err != nil {
		return publishedPaper{}, nil, nil, catalogWorkError(workID, "has_data", err)
	}
	hasBenchmark, err := relationValue(
		ctx,
		tx,
		`SELECT EXISTS (
			SELECT 1 FROM work_benchmarks WHERE work_id = $1
		)`,
		workID,
		assertionValue{},
	)
	if err != nil {
		return publishedPaper{}, nil, nil, catalogWorkError(workID, "has_benchmark", err)
	}
	paper.HasCodeState, paper.HasCodeValue = boolColumns(hasCode)
	paper.HasDataState, paper.HasDataValue = boolColumns(hasData)
	paper.HasBenchmarkState, paper.HasBenchmarkValue = boolColumns(hasBenchmark)

	citationValue, citationCount, err := loadCitationCount(ctx, tx, workID)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	paper.CitationCountState = citationValue.State
	paper.CitationCountValue = citationCount
	paper.TrendScoreState = "missing"

	topics, err := loadTaxonomyFacts(ctx, tx, "topics", workID, sources.sourceRecordIDs)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	methods, err := loadTaxonomyFacts(ctx, tx, "methods", workID, sources.sourceRecordIDs)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	paper.TopicSlugs = taxonomySlugs(topics)
	paper.MethodSlugs = taxonomySlugs(methods)
	paper.SourceNames = sourceNames(sources.states)

	authors, err := loadAuthors(ctx, tx, workID)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	curation, err := loadCuration(ctx, tx, venueIDText)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	provenance := sourceProvenance(sources.states)

	paperPayload := map[string]any{
		"id":                paper.ID,
		"canonical_key":     paper.CanonicalKey,
		"title":             paper.Title,
		"status":            paper.LifecycleStatus,
		"published_at":      publishedAtValue,
		"type":              paperTypeValue,
		"abstract":          abstractValue,
		"has_code":          hasCode,
		"has_data":          hasData,
		"has_benchmark":     hasBenchmark,
		"citation_count":    citationValue,
		"trend_score":       catalogValue{State: "missing"},
		"topics":            taxonomyReferences(topics),
		"methods":           taxonomyReferences(methods),
		"authors":           authors,
		"curation":          curation,
		"source_provenance": catalogValue{State: "known", Value: provenance},
	}
	payload, err := marshalCatalogPayload(paperPayload)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	paper.SummaryPayload = payload
	paper.DetailPayload = payload

	searchParts := []string{paper.Title, paper.CanonicalKey}
	if abstract != "" {
		searchParts = append(searchParts, abstract)
	}
	for _, author := range authors {
		searchParts = append(searchParts, author.Name)
	}
	for _, topic := range topics {
		searchParts = append(searchParts, topic.Name)
	}
	for _, method := range methods {
		searchParts = append(searchParts, method.Name)
	}
	searchParts = append(searchParts, paper.SourceNames...)
	paper.SearchText = strings.Join(searchParts, " ")
	return paper, topics, methods, nil
}

func loadAssertions(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	sourceRecordIDs []uuid.UUID,
) (map[string]assertionValue, error) {
	rows, err := tx.Query(ctx, `
		SELECT field_name, asserted_value, asserted_at, id
		FROM field_assertions
		WHERE work_id = $1
		  AND source_record_id = ANY($2::uuid[])
		ORDER BY field_name, asserted_at DESC, id
	`, workID, sourceRecordIDs)
	if err != nil {
		return nil, fmt.Errorf("query work %s field assertions: %w", workID, err)
	}
	defer rows.Close()

	type selectedAssertion struct {
		value      assertionValue
		assertedAt time.Time
		canonical  string
	}
	selected := make(map[string]selectedAssertion)
	for rows.Next() {
		var (
			fieldName   string
			raw         []byte
			assertedAt  time.Time
			assertionID uuid.UUID
		)
		if err := rows.Scan(&fieldName, &raw, &assertedAt, &assertionID); err != nil {
			return nil, fmt.Errorf("scan work %s field assertion: %w", workID, err)
		}
		value, canonical, err := decodeAssertion(raw)
		if err != nil {
			return nil, catalogWorkError(workID, fieldName, err)
		}
		existing, exists := selected[fieldName]
		if !exists {
			selected[fieldName] = selectedAssertion{
				value:      value,
				assertedAt: assertedAt,
				canonical:  canonical,
			}
			continue
		}
		if assertedAt.Equal(existing.assertedAt) && canonical != existing.canonical {
			return nil, fmt.Errorf(
				"%w: work %s has conflicting %s assertions at %s",
				ErrCatalogNotReady,
				workID,
				fieldName,
				assertedAt.UTC().Format(time.RFC3339Nano),
			)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work %s field assertions: %w", workID, err)
	}

	result := make(map[string]assertionValue, len(selected))
	for fieldName, assertion := range selected {
		result[fieldName] = assertion.value
	}
	return result, nil
}

func decodeAssertion(raw []byte) (assertionValue, string, error) {
	var decoded struct {
		State string          `json:"state"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return assertionValue{}, "", fmt.Errorf("%w: decode assertion: %v", ErrCatalogNotReady, err)
	}
	if decoded.State != "known" && decoded.State != "unknown" && decoded.State != "missing" {
		return assertionValue{}, "", fmt.Errorf(
			"%w: assertion has invalid state %q",
			ErrCatalogNotReady,
			decoded.State,
		)
	}
	valueIsNull := len(decoded.Value) == 0 || string(decoded.Value) == "null"
	if decoded.State == "known" && valueIsNull {
		return assertionValue{}, "", fmt.Errorf(
			"%w: known assertion lacks a value",
			ErrCatalogNotReady,
		)
	}
	if decoded.State != "known" && !valueIsNull {
		return assertionValue{}, "", fmt.Errorf(
			"%w: %s assertion must not carry a value",
			ErrCatalogNotReady,
			decoded.State,
		)
	}
	var canonicalValue any
	if !valueIsNull {
		if err := json.Unmarshal(decoded.Value, &canonicalValue); err != nil {
			return assertionValue{}, "", fmt.Errorf("%w: invalid assertion value", ErrCatalogNotReady)
		}
	}
	canonical, err := json.Marshal(struct {
		State string `json:"state"`
		Value any    `json:"value,omitempty"`
	}{State: decoded.State, Value: canonicalValue})
	if err != nil {
		return assertionValue{}, "", fmt.Errorf("canonicalize assertion: %w", err)
	}
	return assertionValue{State: decoded.State, Value: decoded.Value}, string(canonical), nil
}

func publishedAtFromAssertion(
	assertion assertionValue,
) (catalogValue, *time.Time, error) {
	if assertion.State == "" {
		return catalogValue{State: "missing"}, nil, nil
	}
	if assertion.State != "known" {
		return catalogValue{State: assertion.State}, nil, nil
	}
	var value string
	if err := json.Unmarshal(assertion.Value, &value); err != nil {
		return catalogValue{}, nil, fmt.Errorf("%w: published_at must be an RFC3339 string", ErrCatalogNotReady)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return catalogValue{}, nil, fmt.Errorf("%w: published_at must be RFC3339", ErrCatalogNotReady)
	}
	parsed = parsed.UTC()
	return catalogValue{
		State: "known",
		Value: parsed.Format(time.RFC3339Nano),
	}, &parsed, nil
}

func paperTypeFromAssertion(
	assertion assertionValue,
) (catalogValue, *string, error) {
	if assertion.State == "" {
		return catalogValue{State: "unknown"}, nil, nil
	}
	if assertion.State != "known" {
		return catalogValue{State: assertion.State}, nil, nil
	}
	var value string
	if err := json.Unmarshal(assertion.Value, &value); err != nil || !validPaperType(value) {
		return catalogValue{}, nil, fmt.Errorf("%w: invalid paper type assertion", ErrCatalogNotReady)
	}
	return catalogValue{State: "known", Value: value}, &value, nil
}

func stringFromAssertion(
	assertion assertionValue,
	defaultState string,
) (catalogValue, string, error) {
	if assertion.State == "" {
		return catalogValue{State: defaultState}, "", nil
	}
	if assertion.State != "known" {
		return catalogValue{State: assertion.State}, "", nil
	}
	var value string
	if err := json.Unmarshal(assertion.Value, &value); err != nil ||
		value == "" ||
		value != strings.TrimSpace(value) {
		return catalogValue{}, "", fmt.Errorf("%w: invalid string assertion", ErrCatalogNotReady)
	}
	return catalogValue{State: "known", Value: value}, value, nil
}

func relationValue(
	ctx context.Context,
	tx pgx.Tx,
	query string,
	workID uuid.UUID,
	assertion assertionValue,
) (catalogValue, error) {
	var exists bool
	if err := tx.QueryRow(ctx, query, workID).Scan(&exists); err != nil {
		return catalogValue{}, err
	}
	if exists {
		return catalogValue{State: "known", Value: true}, nil
	}
	if assertion.State == "" {
		return catalogValue{State: "unknown"}, nil
	}
	if assertion.State != "known" {
		return catalogValue{State: assertion.State}, nil
	}
	var values []string
	if err := json.Unmarshal(assertion.Value, &values); err != nil {
		return catalogValue{}, fmt.Errorf("%w: relation assertion must be a string array", ErrCatalogNotReady)
	}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return catalogValue{}, fmt.Errorf("%w: relation assertion contains an invalid value", ErrCatalogNotReady)
		}
	}
	return catalogValue{State: "known", Value: len(values) > 0}, nil
}

func boolColumns(value catalogValue) (string, *bool) {
	if value.State != "known" {
		return value.State, nil
	}
	typed := value.Value.(bool)
	return value.State, &typed
}

func loadCitationCount(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
) (catalogValue, *int64, error) {
	var value string
	err := tx.QueryRow(ctx, `
		SELECT metric_value::text
		FROM metric_snapshots
		WHERE work_id = $1
		  AND metric_name = 'citation_count'
		ORDER BY observed_at DESC, id
		LIMIT 1
	`, workID).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalogValue{State: "missing"}, nil, nil
	}
	if err != nil {
		return catalogValue{}, nil, fmt.Errorf("query work %s citation count: %w", workID, err)
	}
	var parsed int64
	if _, err := fmt.Sscan(value, &parsed); err != nil ||
		fmt.Sprintf("%d", parsed) != value ||
		parsed < 0 {
		return catalogValue{}, nil, fmt.Errorf(
			"%w: work %s citation count is not a non-negative integer",
			ErrCatalogNotReady,
			workID,
		)
	}
	return catalogValue{State: "known", Value: parsed}, &parsed, nil
}

func loadTaxonomyFacts(
	ctx context.Context,
	tx pgx.Tx,
	kind string,
	workID uuid.UUID,
	sourceRecordIDs []uuid.UUID,
) ([]taxonomyFact, error) {
	var query string
	switch kind {
	case "topics":
		query = `
			SELECT topic.id, topic.name, topic.description
			FROM work_topics AS relation
			JOIN topics AS topic ON topic.id = relation.topic_id
			WHERE relation.work_id = $1
			  AND relation.source_record_id = ANY($2::uuid[])
			ORDER BY topic.id
		`
	case "methods":
		query = `
			SELECT method.id, method.name, method.description
			FROM work_methods AS relation
			JOIN methods AS method ON method.id = relation.method_id
			WHERE relation.work_id = $1
			  AND relation.source_record_id = ANY($2::uuid[])
			ORDER BY method.id
		`
	default:
		return nil, fmt.Errorf("%w: unsupported taxonomy kind %q", ErrCatalogNotReady, kind)
	}
	rows, err := tx.Query(ctx, query, workID, sourceRecordIDs)
	if err != nil {
		return nil, fmt.Errorf("query work %s %s: %w", workID, kind, err)
	}
	defer rows.Close()

	facts := make([]taxonomyFact, 0)
	for rows.Next() {
		var fact taxonomyFact
		if err := rows.Scan(&fact.ID, &fact.Name, &fact.Description); err != nil {
			return nil, fmt.Errorf("scan work %s %s: %w", workID, kind, err)
		}
		if fact.Name == "" || fact.Name != strings.TrimSpace(fact.Name) {
			return nil, fmt.Errorf(
				"%w: taxonomy %s has an invalid name",
				ErrCatalogNotReady,
				fact.ID,
			)
		}
		if fact.Description != nil &&
			(*fact.Description == "" || *fact.Description != strings.TrimSpace(*fact.Description)) {
			return nil, fmt.Errorf(
				"%w: taxonomy %s has an invalid description",
				ErrCatalogNotReady,
				fact.ID,
			)
		}
		fact.Slug = deterministicSlug(fact.Name)
		if !validSlug(fact.Slug) {
			return nil, fmt.Errorf(
				"%w: taxonomy %s cannot produce a valid slug",
				ErrCatalogNotReady,
				fact.ID,
			)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work %s %s: %w", workID, kind, err)
	}
	return facts, nil
}

func deterministicSlug(name string) string {
	lower := strings.ToLower(name)
	return strings.Trim(slugSeparatorPattern.ReplaceAllString(lower, "-"), "-")
}

func mergeTaxonomyFacts(
	target map[uuid.UUID]*taxonomyFact,
	facts []taxonomyFact,
	workID uuid.UUID,
) error {
	for _, fact := range facts {
		existing := target[fact.ID]
		if existing == nil {
			copy := fact
			copy.PaperIDs = []uuid.UUID{workID}
			target[fact.ID] = &copy
			continue
		}
		if existing.Name != fact.Name ||
			existing.Slug != fact.Slug ||
			!equalOptionalString(existing.Description, fact.Description) {
			return fmt.Errorf(
				"%w: taxonomy %s changed within one publication snapshot",
				ErrCatalogNotReady,
				fact.ID,
			)
		}
		existing.PaperIDs = append(existing.PaperIDs, workID)
	}
	return nil
}

func publishTaxonomies(facts map[uuid.UUID]*taxonomyFact) ([]publishedTaxonomy, error) {
	values := make([]*taxonomyFact, 0, len(facts))
	for _, fact := range facts {
		sort.Slice(fact.PaperIDs, func(i, j int) bool {
			return fact.PaperIDs[i].String() < fact.PaperIDs[j].String()
		})
		values = append(values, fact)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Slug != values[j].Slug {
			return values[i].Slug < values[j].Slug
		}
		return values[i].ID.String() < values[j].ID.String()
	})
	for index := 1; index < len(values); index++ {
		if values[index-1].Slug == values[index].Slug &&
			values[index-1].ID != values[index].ID {
			return nil, fmt.Errorf(
				"%w: taxonomy slug %q is ambiguous",
				ErrCatalogNotReady,
				values[index].Slug,
			)
		}
	}

	result := make([]publishedTaxonomy, 0, len(values))
	for _, fact := range values {
		description := catalogValue{State: "missing"}
		if fact.Description != nil {
			description = catalogValue{State: "known", Value: *fact.Description}
		}
		payload, err := marshalCatalogPayload(map[string]any{
			"id":          fact.ID,
			"slug":        fact.Slug,
			"name":        fact.Name,
			"description": description,
			"paper_count": len(fact.PaperIDs),
			"paper_ids":   fact.PaperIDs,
		})
		if err != nil {
			return nil, err
		}
		result = append(result, publishedTaxonomy{
			ID:             fact.ID,
			Slug:           fact.Slug,
			Name:           fact.Name,
			PaperCount:     int64(len(fact.PaperIDs)),
			SummaryPayload: payload,
			DetailPayload:  payload,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PaperCount != result[j].PaperCount {
			return result[i].PaperCount > result[j].PaperCount
		}
		return result[i].Slug < result[j].Slug
	})
	return result, nil
}

func loadAuthors(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
) ([]authorReference, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			author.id,
			author.display_name,
			author.orcid,
			institution.id,
			institution.display_name,
			relation.author_position,
			relation.is_corresponding
		FROM work_authors AS relation
		JOIN authors AS author ON author.id = relation.author_id
		LEFT JOIN institutions AS institution ON institution.id = relation.institution_id
		WHERE relation.work_id = $1
		ORDER BY relation.author_position
	`, workID)
	if err != nil {
		return nil, fmt.Errorf("query work %s authors: %w", workID, err)
	}
	defer rows.Close()

	authors := make([]authorReference, 0)
	for rows.Next() {
		var (
			author          authorReference
			institutionText *string
		)
		if err := rows.Scan(
			&author.ID,
			&author.Name,
			&author.ORCID,
			&institutionText,
			&author.InstitutionName,
			&author.Position,
			&author.IsCorresponding,
		); err != nil {
			return nil, fmt.Errorf("scan work %s author: %w", workID, err)
		}
		if author.Name == "" || author.Name != strings.TrimSpace(author.Name) {
			return nil, fmt.Errorf("%w: work %s has invalid author name", ErrCatalogNotReady, workID)
		}
		if institutionText != nil {
			parsed, err := uuid.Parse(*institutionText)
			if err != nil {
				return nil, fmt.Errorf("%w: work %s has invalid institution ID", ErrCatalogNotReady, workID)
			}
			author.InstitutionID = &parsed
		}
		authors = append(authors, author)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work %s authors: %w", workID, err)
	}
	return authors, nil
}

func loadCuration(
	ctx context.Context,
	tx pgx.Tx,
	venueIDText string,
) (catalogValue, error) {
	if venueIDText == "" {
		return catalogValue{State: "missing"}, nil
	}
	venueID, err := uuid.Parse(venueIDText)
	if err != nil {
		return catalogValue{}, fmt.Errorf("%w: invalid venue ID", ErrCatalogNotReady)
	}
	var (
		decision      string
		matchedRules  []byte
		evidence      []byte
		metricYear    int
		policyName    string
		policyVersion int
		assessedAt    time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT
			assessment.decision,
			assessment.matched_rules,
			assessment.evidence,
			assessment.metric_year,
			policy.policy_name,
			policy.version_number,
			assessment.assessed_at
		FROM venue_policy_assessments AS assessment
		JOIN venue_policy_versions AS policy
		  ON policy.id = assessment.policy_version_id
		WHERE assessment.venue_id = $1
		ORDER BY assessment.assessed_at DESC, assessment.metric_year DESC, assessment.id
		LIMIT 1
	`, venueID).Scan(
		&decision,
		&matchedRules,
		&evidence,
		&metricYear,
		&policyName,
		&policyVersion,
		&assessedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalogValue{State: "missing"}, nil
	}
	if err != nil {
		return catalogValue{}, fmt.Errorf("query venue %s curation: %w", venueID, err)
	}
	if decision == "unknown" {
		return catalogValue{State: "unknown"}, nil
	}
	if decision != "accepted" && decision != "rejected" && decision != "not_applicable" {
		return catalogValue{}, fmt.Errorf(
			"%w: venue %s has invalid curation decision %q",
			ErrCatalogNotReady,
			venueID,
			decision,
		)
	}
	return catalogValue{
		State: "known",
		Value: curationPayload{
			Decision:      decision,
			MatchedRules:  json.RawMessage(matchedRules),
			Evidence:      json.RawMessage(evidence),
			MetricYear:    metricYear,
			PolicyName:    policyName,
			PolicyVersion: policyVersion,
			AssessedAt:    assessedAt.UTC().Format(time.RFC3339Nano),
		},
	}, nil
}

func sourceNames(states []sourceState) []string {
	unique := make(map[string]struct{}, len(states))
	for _, state := range states {
		unique[state.logicalSource] = struct{}{}
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sourceProvenance(states []sourceState) []sourceProvenancePayload {
	values := make([]sourceProvenancePayload, len(states))
	for index, state := range states {
		values[index] = sourceProvenancePayload{
			Source:                     state.logicalSource,
			EventKey:                   state.eventKey,
			SourceRecordID:             state.sourceRecordID,
			SourceTime:                 state.sourceTime.UTC().Format(time.RFC3339Nano),
			NormalizationPolicyVersion: state.normalizationPolicyVersion,
			ScopePolicyVersion:         state.scopePolicyVersion,
			ProjectionPolicyVersion:    state.projectionPolicyVersion,
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Source != values[j].Source {
			return values[i].Source < values[j].Source
		}
		return values[i].EventKey < values[j].EventKey
	})
	return values
}

func taxonomySlugs(facts []taxonomyFact) []string {
	values := make([]string, len(facts))
	for index, fact := range facts {
		values[index] = fact.Slug
	}
	sort.Strings(values)
	return values
}

func taxonomyReferences(facts []taxonomyFact) []taxonomyReference {
	values := make([]taxonomyReference, len(facts))
	for index, fact := range facts {
		values[index] = taxonomyReference{ID: fact.ID, Slug: fact.Slug, Name: fact.Name}
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].Slug < values[j].Slug
	})
	return values
}

func buildStats(
	input PublishInput,
	papers []publishedPaper,
	topicCount int,
	methodCount int,
) (json.RawMessage, error) {
	recentCutoff := input.GeneratedAt.UTC().AddDate(0, 0, -7)
	recentCount := int64(0)
	allPublishedKnown := true
	for _, paper := range papers {
		if paper.PublishedAtState != "known" {
			allPublishedKnown = false
			continue
		}
		if !paper.PublishedAt.Before(recentCutoff) &&
			!paper.PublishedAt.After(input.GeneratedAt.UTC()) {
			recentCount++
		}
	}
	recent := catalogValue{State: "unknown"}
	if allPublishedKnown {
		recent = catalogValue{State: "known", Value: recentCount}
	}

	payload := map[string]any{
		"generated_at":       input.GeneratedAt.UTC().Format(time.RFC3339Nano),
		"formula_version":    input.FormulaVersion,
		"papers_total":       catalogValue{State: "known", Value: int64(len(papers))},
		"papers_last_7_days": recent,
		"topics_total":       catalogValue{State: "known", Value: int64(topicCount)},
		"methods_total":      catalogValue{State: "known", Value: int64(methodCount)},
		"with_code_ratio": boolRatio(papers, func(paper publishedPaper) (string, *bool) {
			return paper.HasCodeState, paper.HasCodeValue
		}),
		"with_data_ratio": boolRatio(papers, func(paper publishedPaper) (string, *bool) {
			return paper.HasDataState, paper.HasDataValue
		}),
		"with_benchmark_ratio": boolRatio(papers, func(paper publishedPaper) (string, *bool) {
			return paper.HasBenchmarkState, paper.HasBenchmarkValue
		}),
	}
	return marshalCatalogPayload(payload)
}

func boolRatio(
	papers []publishedPaper,
	value func(publishedPaper) (string, *bool),
) catalogValue {
	known := 0
	trueCount := 0
	unknown := 0
	missing := 0
	for _, paper := range papers {
		state, current := value(paper)
		switch state {
		case "known":
			if current == nil {
				return catalogValue{State: "unknown"}
			}
			known++
			if *current {
				trueCount++
			}
		case "unknown":
			unknown++
		case "missing":
			missing++
		default:
			return catalogValue{State: "unknown"}
		}
	}
	if known == len(papers) {
		return catalogValue{
			State: "known",
			Value: float64(trueCount) / float64(len(papers)),
		}
	}
	if known == 0 && unknown == 0 && missing == len(papers) {
		return catalogValue{State: "missing"}
	}
	return catalogValue{State: "unknown"}
}

func snapshotRevision(formulaVersion string, snapshot catalogSnapshot) (string, error) {
	material := struct {
		FormulaVersion string               `json:"formula_version"`
		Sources        []sourceRevisionFact `json:"sources"`
		Papers         []publishedPaper     `json:"papers"`
		Topics         []publishedTaxonomy  `json:"topics"`
		Methods        []publishedTaxonomy  `json:"methods"`
	}{
		FormulaVersion: formulaVersion,
		Sources:        snapshot.sources,
		Papers:         snapshot.papers,
		Topics:         snapshot.topics,
		Methods:        snapshot.methods,
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("encode public catalog source revision: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func existingPublishedGeneration(
	ctx context.Context,
	tx pgx.Tx,
	sourceRevision string,
) (Generation, bool, error) {
	var generation Generation
	err := tx.QueryRow(ctx, `
		SELECT
			generation.id,
			generation.source_revision,
			generation.formula_version,
			generation.generated_at,
			publication.published_at
		FROM public_catalog_generations AS generation
		LEFT JOIN public_catalog_publications AS publication
		  ON publication.generation_id = generation.id
		WHERE generation.source_revision = $1
	`, sourceRevision).Scan(
		&generation.ID,
		&generation.SourceRevision,
		&generation.FormulaVersion,
		&generation.GeneratedAt,
		&generation.PublishedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Generation{}, false, nil
	}
	if err != nil {
		return Generation{}, false, fmt.Errorf(
			"query existing public catalog generation: %w",
			err,
		)
	}
	if generation.PublishedAt.IsZero() {
		return Generation{}, false, fmt.Errorf(
			"%w: source revision %s is reserved by an unpublished generation",
			ErrCatalogNotReady,
			sourceRevision,
		)
	}
	generation.GeneratedAt = generation.GeneratedAt.UTC()
	generation.PublishedAt = generation.PublishedAt.UTC()
	return generation, true, nil
}

func persistSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
	sourceRevision string,
	snapshot catalogSnapshot,
) (Generation, error) {
	metadata, err := json.Marshal(map[string]any{
		"papers":  len(snapshot.papers),
		"topics":  len(snapshot.topics),
		"methods": len(snapshot.methods),
	})
	if err != nil {
		return Generation{}, fmt.Errorf("encode public catalog generation metadata: %w", err)
	}

	var generation Generation
	err = tx.QueryRow(ctx, `
		INSERT INTO public_catalog_generations (
			source_revision,
			formula_version,
			generated_at,
			metadata
		) VALUES ($1, $2, $3, $4)
		RETURNING id, source_revision, formula_version, generated_at
	`, sourceRevision, input.FormulaVersion, input.GeneratedAt.UTC(), metadata).Scan(
		&generation.ID,
		&generation.SourceRevision,
		&generation.FormulaVersion,
		&generation.GeneratedAt,
	)
	if err != nil {
		return Generation{}, fmt.Errorf("insert public catalog generation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_stats (generation_id, payload)
		VALUES ($1, $2)
	`, generation.ID, snapshot.stats); err != nil {
		return Generation{}, fmt.Errorf("insert public catalog stats: %w", err)
	}

	for _, taxonomy := range snapshot.topics {
		if err := insertTaxonomy(ctx, tx, "public_catalog_topics", generation.ID, taxonomy); err != nil {
			return Generation{}, err
		}
	}
	for _, taxonomy := range snapshot.methods {
		if err := insertTaxonomy(ctx, tx, "public_catalog_methods", generation.ID, taxonomy); err != nil {
			return Generation{}, err
		}
	}
	for _, paper := range snapshot.papers {
		if _, err := tx.Exec(ctx, `
			INSERT INTO public_catalog_papers (
				generation_id,
				paper_id,
				canonical_key,
				title,
				search_text,
				published_at_state,
				published_at,
				paper_type_state,
				paper_type,
				lifecycle_status,
				topic_slugs,
				method_slugs,
				source_names,
				has_code_state,
				has_code_value,
				has_data_state,
				has_data_value,
				has_benchmark_state,
				has_benchmark_value,
				citation_count_state,
				citation_count_value,
				trend_score_state,
				trend_score_value,
				summary_payload,
				detail_payload
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
				$14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25
			)
		`,
			generation.ID,
			paper.ID,
			paper.CanonicalKey,
			paper.Title,
			paper.SearchText,
			paper.PublishedAtState,
			paper.PublishedAt,
			paper.PaperTypeState,
			paper.PaperType,
			paper.LifecycleStatus,
			paper.TopicSlugs,
			paper.MethodSlugs,
			paper.SourceNames,
			paper.HasCodeState,
			paper.HasCodeValue,
			paper.HasDataState,
			paper.HasDataValue,
			paper.HasBenchmarkState,
			paper.HasBenchmarkValue,
			paper.CitationCountState,
			paper.CitationCountValue,
			paper.TrendScoreState,
			paper.TrendScoreValue,
			paper.SummaryPayload,
			paper.DetailPayload,
		); err != nil {
			return Generation{}, fmt.Errorf("insert public catalog paper %s: %w", paper.ID, err)
		}
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO public_catalog_publications (generation_id, published_at)
		VALUES ($1, clock_timestamp())
		RETURNING published_at
	`, generation.ID).Scan(&generation.PublishedAt)
	if err != nil {
		return Generation{}, fmt.Errorf("publish public catalog generation: %w", err)
	}
	if err := setCurrentGeneration(ctx, tx, generation.ID); err != nil {
		return Generation{}, err
	}
	generation.GeneratedAt = generation.GeneratedAt.UTC()
	generation.PublishedAt = generation.PublishedAt.UTC()
	return generation, nil
}

func insertTaxonomy(
	ctx context.Context,
	tx pgx.Tx,
	table string,
	generationID uuid.UUID,
	taxonomy publishedTaxonomy,
) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (
			generation_id,
			taxonomy_id,
			slug,
			name,
			paper_count,
			summary_payload,
			detail_payload
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, table)
	if _, err := tx.Exec(
		ctx,
		query,
		generationID,
		taxonomy.ID,
		taxonomy.Slug,
		taxonomy.Name,
		taxonomy.PaperCount,
		taxonomy.SummaryPayload,
		taxonomy.DetailPayload,
	); err != nil {
		return fmt.Errorf("insert %s taxonomy %s: %w", table, taxonomy.ID, err)
	}
	return nil
}

func setCurrentGeneration(
	ctx context.Context,
	tx pgx.Tx,
	generationID uuid.UUID,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_current (singleton, generation_id)
		VALUES (true, $1)
		ON CONFLICT (singleton) DO UPDATE
		SET generation_id = EXCLUDED.generation_id
	`, generationID); err != nil {
		return fmt.Errorf("set current public catalog generation: %w", err)
	}
	return nil
}

func marshalCatalogPayload(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode public catalog payload: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func catalogWorkError(workID uuid.UUID, field string, err error) error {
	return fmt.Errorf("%w: work %s field %s", err, workID, field)
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
