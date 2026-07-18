package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/analysis"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/citation"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
)

const publicCatalogPublisherLockID int64 = 0x50434154414c4f47

var slugSeparatorPattern = regexp.MustCompile(`[^a-z0-9]+`)

type Publisher struct {
	database transactionBeginner
}

type catalogSnapshot struct {
	stats               json.RawMessage
	home                json.RawMessage
	subjectListMetadata json.RawMessage
	journalListMetadata json.RawMessage
	papers              []publishedPaper
	topics              []publishedTaxonomy
	methods             []publishedTaxonomy
	subjects            []publishedBiomedicalResource
	journals            []publishedBiomedicalResource
	trends              []publishedTrend
	opportunities       []publishedResearchOpportunity
	sources             []sourceRevisionFact
	curations           []curationRevisionFact
}

type sourceRevisionFact struct {
	LogicalSource              string `json:"logical_source"`
	EventKey                   string `json:"event_key"`
	NormalizedAssertionID      string `json:"normalized_assertion_id"`
	RawEventID                 string `json:"raw_event_id"`
	SourceRecordID             string `json:"source_record_id"`
	WorkID                     string `json:"work_id"`
	SourceTime                 string `json:"source_time"`
	TieBreakKey                string `json:"tie_break_key"`
	Position                   int64  `json:"position"`
	ScopePolicyVersion         string `json:"scope_policy_version"`
	ProjectionPolicyVersion    string `json:"projection_policy_version"`
	NormalizationPolicyVersion string `json:"normalization_policy_version"`
	NormalizedPayloadSchema    string `json:"normalized_payload_schema"`
	NormalizedPayload          any    `json:"normalized_payload"`
}

type sourceState struct {
	logicalSource              string
	eventKey                   string
	normalizedAssertionID      uuid.UUID
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
	normalizedPayloadSchema    string
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
	CitationTrend      citationTrendEvidence
	PublicationState   *publishedPublicationState
	biomedical         biomedicalPaperPayload
}

type publishedPublicationState struct {
	ProjectionAssertionID uuid.UUID
	NormalizedAssertionID uuid.UUID
	SourceRecordID        uuid.UUID
	Source                string
	PrintPublished        publishedPublicationEventState
	ElectronicPublished   publishedPublicationEventState
	AheadOfPrint          publishedPublicationEventState
	Accepted              publishedPublicationEventState
	PublicationModel      *string
	PublicationStatus     *string
}

type publishedPublicationEventState struct {
	State            string
	Date             *time.Time
	PublicationModel *string
	Provenance       *publishedPublicationEventProvenance
}

type publishedPublicationEventProvenance struct {
	Source                string    `json:"source"`
	SourceRecordID        uuid.UUID `json:"source_record_id"`
	NormalizedAssertionID uuid.UUID `json:"normalized_assertion_id"`
	ProjectionAssertionID uuid.UUID `json:"projection_assertion_id"`
	SourcePath            string    `json:"source_path"`
	StatusRaw             string    `json:"status_raw"`
}

type normalizedPublicationDateV3 struct {
	Year      int    `json:"year"`
	Month     int    `json:"month,omitempty"`
	Day       int    `json:"day,omitempty"`
	Precision string `json:"precision"`
}

type normalizedPublicationHistoryEntryV3 struct {
	Status     string                      `json:"status"`
	Date       normalizedPublicationDateV3 `json:"date"`
	SourcePath string                      `json:"source_path"`
	Ordinal    int                         `json:"ordinal"`
}

type normalizedPublicationPayloadV3 struct {
	Source             string                                `json:"source"`
	SourceRecordID     string                                `json:"source_record_id"`
	PublicationModel   string                                `json:"publication_model"`
	PublicationStatus  string                                `json:"publication_status"`
	PublicationHistory []normalizedPublicationHistoryEntryV3 `json:"publication_history"`
}

type publicationEventAssertionEvidence struct {
	ProjectionAssertionID uuid.UUID
	NormalizedAssertionID uuid.UUID
	SourceRecordID        uuid.UUID
	WorkID                uuid.UUID
	EventKind             string
	EventDate             *time.Time
	DatePrecision         string
	SourceDate            json.RawMessage
	StatusRaw             string
	PublicationModelRaw   *string
	SourcePath            string
	Ordinal               int
}

type publicationEvidenceSeed struct {
	state    publishedPublicationState
	stored   map[string]publishedPublicationEventState
	expected []publicationEventAssertionEvidence
}

type publishedBiomedicalResource struct {
	ID             uuid.UUID
	Slug           string
	PaperCount     int64
	SummaryPayload json.RawMessage
	DetailPayload  json.RawMessage
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
	State  string `json:"state"`
	Value  any    `json:"value,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type citationSnapshotPayload struct {
	Source            string    `json:"source"`
	ObservedAt        string    `json:"observed_at"`
	Count             int64     `json:"count"`
	SourceRecordID    uuid.UUID `json:"source_record_id"`
	IngestionJobID    uuid.UUID `json:"ingestion_job_id"`
	RetrievedAt       string    `json:"retrieved_at"`
	Coverage          float64   `json:"coverage"`
	DefinitionVersion string    `json:"definition_version"`
	DatasetVersion    string    `json:"dataset_version"`
}

type citationAnalysisRun struct {
	ID                       uuid.UUID
	Source                   string
	AsOf                     time.Time
	GeneratedAt              time.Time
	VelocityWindowDays       int
	MinimumCohortSize        int
	FormulaVersion           string
	SubjectVersion           string
	EligibilityPolicyVersion string
	JCRMetricYear            int
	JCRImportReceipt         uuid.UUID
	VenuePolicyName          string
	VenuePolicyVersion       int
}

type citationVelocityKnownEvidence struct {
	State              string    `json:"state"`
	WindowDays         int       `json:"window_days"`
	CurrentSnapshotID  uuid.UUID `json:"current_snapshot_id"`
	BaselineSnapshotID uuid.UUID `json:"baseline_snapshot_id"`
	ElapsedDays        float64   `json:"elapsed_days"`
}

type citationVelocityInsufficientEvidence struct {
	State      string   `json:"state"`
	WindowDays int      `json:"window_days"`
	Reason     string   `json:"reason"`
	Missing    []string `json:"missing"`
}

type citationPercentileKnownEvidence struct {
	State              string      `json:"state"`
	CitationSnapshotID uuid.UUID   `json:"citation_snapshot_id"`
	SubjectVersionID   uuid.UUID   `json:"subject_version_id"`
	SubjectID          uuid.UUID   `json:"subject_id"`
	PublicationYear    int         `json:"publication_year"`
	PublicationTypeID  uuid.UUID   `json:"publication_type_id"`
	CohortKey          string      `json:"cohort_key"`
	CohortSize         int         `json:"cohort_size"`
	MinimumCohortSize  int         `json:"minimum_cohort_size"`
	Midrank            json.Number `json:"midrank"`
	SupportingWorkIDs  []uuid.UUID `json:"supporting_work_ids"`
}

type citationPercentileInsufficientEvidence struct {
	State              string      `json:"state"`
	CitationSnapshotID uuid.UUID   `json:"citation_snapshot_id"`
	SubjectVersionID   uuid.UUID   `json:"subject_version_id"`
	SubjectID          uuid.UUID   `json:"subject_id"`
	PublicationYear    int         `json:"publication_year"`
	PublicationTypeID  uuid.UUID   `json:"publication_type_id"`
	CohortKey          string      `json:"cohort_key"`
	CohortSize         int         `json:"cohort_size"`
	MinimumCohortSize  int         `json:"minimum_cohort_size"`
	Reason             string      `json:"reason"`
	SupportingWorkIDs  []uuid.UUID `json:"supporting_work_ids"`
}

type citationAnalysisEvidencePayload struct {
	AnalysisRunID  uuid.UUID `json:"analysis_run_id"`
	Source         string    `json:"source"`
	AsOf           string    `json:"as_of"`
	GeneratedAt    string    `json:"generated_at"`
	FormulaVersion string    `json:"formula_version"`
	SourceRevision string    `json:"source_revision"`
	Velocity       any       `json:"velocity"`
	Percentile     any       `json:"percentile"`
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

type journalReference struct {
	ID    uuid.UUID `json:"id"`
	Slug  string    `json:"slug"`
	Title string    `json:"title"`
}

type meshQualifierPayload struct {
	QualifierUI string `json:"qualifier_ui"`
	Label       string `json:"label"`
	MajorTopic  bool   `json:"is_major_topic"`
	SourcePath  string `json:"source_path"`
}

type meshHeadingPayload struct {
	DescriptorUI string                 `json:"descriptor_ui"`
	Label        string                 `json:"label"`
	MajorTopic   bool                   `json:"is_major_topic"`
	SourcePath   string                 `json:"source_path"`
	Qualifiers   []meshQualifierPayload `json:"qualifiers"`
}

type biomedicalPaperPayload struct {
	Journal               journalReference
	Subjects              []taxonomyReference
	MeSHHeadings          catalogValue
	PublicationTypes      []string
	PublicationTypesState catalogValue
	JCRAssessment         catalogValue
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

type curationRevisionFact struct {
	WorkID      uuid.UUID                     `json:"work_id"`
	VenueID     uuid.UUID                     `json:"venue_id"`
	Curation    curationPayload               `json:"curation"`
	Eligibility biomedicalEligibilityRevision `json:"biomedical_eligibility"`
}

type jcrAssessmentCategoryEvidence struct {
	Category             string  `json:"category"`
	RegistryVersion      string  `json:"registry_version"`
	EditionYear          *int    `json:"edition_year"`
	JIF                  *string `json:"jif"`
	JIFRank              *int    `json:"jif_rank"`
	CategoryJournalCount *int    `json:"category_journal_count"`
	JIFPercentile        *string `json:"jif_percentile"`
	Quartile             string  `json:"quartile,omitempty"`
	Status               string  `json:"status"`
	SourceName           string  `json:"source_name"`
}

type jcrAssessmentEvidence struct {
	JCRImportReceiptID string                          `json:"jcr_import_receipt_id"`
	PolicyVersion      string                          `json:"policy_version"`
	MetricYear         int                             `json:"metric_year"`
	VenueType          string                          `json:"venue_type"`
	Reason             string                          `json:"reason,omitempty"`
	Categories         []jcrAssessmentCategoryEvidence `json:"categories"`
}

type biomedicalEligibilityRevision struct {
	ID                uuid.UUID       `json:"id"`
	PolicyVersion     string          `json:"policy_version"`
	MetricYear        int             `json:"metric_year"`
	SubjectVersionID  uuid.UUID       `json:"subject_version_id"`
	SubjectVersionKey string          `json:"subject_version_key"`
	Decision          string          `json:"decision"`
	Evidence          json.RawMessage `json:"evidence"`
	AssessedAt        string          `json:"assessed_at"`
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
	if input.JCRMetricYear < 1900 || input.JCRMetricYear > 3000 {
		return fmt.Errorf(
			"%w: JCR metric year must be between 1900 and 3000",
			ErrCatalogNotReady,
		)
	}
	if input.VenuePolicyName != venue.JournalAllQ1PolicyName {
		return fmt.Errorf(
			"%w: Venue policy name must equal %s",
			ErrCatalogNotReady,
			venue.JournalAllQ1PolicyName,
		)
	}
	if input.VenuePolicyVersion != venue.JournalAllQ1PolicyRevision {
		return fmt.Errorf(
			"%w: Venue policy version must equal version %d",
			ErrCatalogNotReady,
			venue.JournalAllQ1PolicyRevision,
		)
	}
	if input.EligibilityPolicyVersion !=
		biomed.BiomedicalPublicEligibilityPolicyVersion {
		return fmt.Errorf(
			"%w: unsupported biomedical eligibility policy version %q; expected %s",
			ErrCatalogNotReady,
			input.EligibilityPolicyVersion,
			biomed.BiomedicalPublicEligibilityPolicyVersion,
		)
	}
	if input.SubjectVersion == "" ||
		input.SubjectVersion != strings.TrimSpace(input.SubjectVersion) {
		return fmt.Errorf(
			"%w: Subject version must be non-empty and trimmed",
			ErrCatalogNotReady,
		)
	}
	if input.JCRImportReceipt == uuid.Nil {
		return fmt.Errorf(
			"%w: JCR import receipt is required",
			ErrCatalogNotReady,
		)
	}
	if input.CitationSource == "" ||
		input.CitationSource != strings.TrimSpace(input.CitationSource) {
		return fmt.Errorf(
			"%w: citation source must be non-empty and trimmed",
			ErrCatalogNotReady,
		)
	}
	if input.CitationAnalysisRunID == uuid.Nil {
		return fmt.Errorf(
			"%w: citation analysis run is required",
			ErrCatalogNotReady,
		)
	}
	if input.TrendAnalysisRunID == uuid.Nil {
		return fmt.Errorf(
			"%w: trend analysis run is required",
			ErrCatalogNotReady,
		)
	}
	if input.JournalAnalysisRunID == uuid.Nil {
		return fmt.Errorf(
			"%w: journal analysis run is required",
			ErrCatalogNotReady,
		)
	}
	if input.OpportunityAnalysisRunID == uuid.Nil {
		return fmt.Errorf(
			"%w: opportunity analysis run is required",
			ErrCatalogNotReady,
		)
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

func validateCitationAnalysisRun(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
) (citationAnalysisRun, error) {
	var (
		run           citationAnalysisRun
		analysisType  string
		modelProvider string
		modelName     string
		promptVersion string
		status        string
		rawInput      []byte
		rawOutput     []byte
		startedAt     time.Time
		completedAt   time.Time
	)
	err := tx.QueryRow(ctx, `
		SELECT
			id,
			analysis_type,
			model_provider,
			model_name,
			prompt_version,
			status,
			input_payload,
			output_payload,
			started_at,
			completed_at
		FROM analysis_runs
		WHERE id = $1
	`, input.CitationAnalysisRunID).Scan(
		&run.ID,
		&analysisType,
		&modelProvider,
		&modelName,
		&promptVersion,
		&status,
		&rawInput,
		&rawOutput,
		&startedAt,
		&completedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return citationAnalysisRun{}, fmt.Errorf(
			"%w: citation analysis run %s does not exist",
			ErrCatalogNotReady,
			input.CitationAnalysisRunID,
		)
	}
	if err != nil {
		return citationAnalysisRun{}, fmt.Errorf(
			"query citation analysis run %s: %w",
			input.CitationAnalysisRunID,
			err,
		)
	}
	if analysisType != citation.CitationIntelligenceAnalysisType ||
		modelProvider != "internal" ||
		modelName != "deterministic" ||
		promptVersion != citation.CitationIntelligenceFormulaVersion ||
		status != "succeeded" ||
		len(rawOutput) == 0 ||
		startedAt.IsZero() ||
		completedAt.IsZero() ||
		completedAt.After(input.GeneratedAt) {
		return citationAnalysisRun{}, fmt.Errorf(
			"%w: citation analysis run %s is not an exact completed deterministic citation_intelligence run available at generated_at",
			ErrCatalogNotReady,
			input.CitationAnalysisRunID,
		)
	}

	var payload struct {
		Source                   string    `json:"source"`
		AsOf                     string    `json:"as_of"`
		VelocityWindowDays       int       `json:"velocity_window_days"`
		MinimumCohortSize        int       `json:"minimum_cohort_size"`
		FormulaVersion           string    `json:"formula_version"`
		SubjectVersion           string    `json:"subject_version"`
		EligibilityPolicyVersion string    `json:"eligibility_policy_version"`
		JCRMetricYear            int       `json:"jcr_metric_year"`
		JCRImportReceipt         uuid.UUID `json:"jcr_import_receipt"`
		VenuePolicyName          string    `json:"venue_policy_name"`
		VenuePolicyVersion       int       `json:"venue_policy_version"`
	}
	if err := decodeStrictJSONObject(rawInput, &payload); err != nil {
		return citationAnalysisRun{}, fmt.Errorf(
			"%w: decode exact citation analysis run %s input: %v",
			ErrCatalogNotReady,
			input.CitationAnalysisRunID,
			err,
		)
	}
	asOf, err := time.Parse(time.RFC3339Nano, payload.AsOf)
	if err != nil {
		return citationAnalysisRun{}, fmt.Errorf(
			"%w: citation analysis run %s has invalid as_of",
			ErrCatalogNotReady,
			input.CitationAnalysisRunID,
		)
	}
	if payload.Source != input.CitationSource ||
		payload.FormulaVersion != citation.CitationIntelligenceFormulaVersion ||
		payload.SubjectVersion != input.SubjectVersion ||
		payload.EligibilityPolicyVersion !=
			input.EligibilityPolicyVersion ||
		payload.JCRMetricYear != input.JCRMetricYear ||
		payload.JCRImportReceipt != input.JCRImportReceipt ||
		payload.VenuePolicyName != input.VenuePolicyName ||
		payload.VenuePolicyVersion != input.VenuePolicyVersion ||
		payload.VelocityWindowDays < 1 ||
		payload.VelocityWindowDays > 3650 ||
		payload.MinimumCohortSize < 2 ||
		asOf.After(input.GeneratedAt) {
		return citationAnalysisRun{}, fmt.Errorf(
			"%w: citation analysis run %s scope does not match this catalog publication",
			ErrCatalogNotReady,
			input.CitationAnalysisRunID,
		)
	}
	run.Source = payload.Source
	run.AsOf = asOf.UTC()
	run.GeneratedAt = completedAt.UTC()
	run.VelocityWindowDays = payload.VelocityWindowDays
	run.MinimumCohortSize = payload.MinimumCohortSize
	run.FormulaVersion = payload.FormulaVersion
	run.SubjectVersion = payload.SubjectVersion
	run.EligibilityPolicyVersion = payload.EligibilityPolicyVersion
	run.JCRMetricYear = payload.JCRMetricYear
	run.JCRImportReceipt = payload.JCRImportReceipt
	run.VenuePolicyName = payload.VenuePolicyName
	run.VenuePolicyVersion = payload.VenuePolicyVersion
	return run, nil
}

func buildCurrentSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
) (catalogSnapshot, string, error) {
	if err := validateCurationReferences(ctx, tx, input); err != nil {
		return catalogSnapshot{}, "", err
	}
	analysisRun, err := validateCitationAnalysisRun(ctx, tx, input)
	if err != nil {
		return catalogSnapshot{}, "", err
	}
	states, err := loadCurrentSourceStates(ctx, tx)
	if err != nil {
		return catalogSnapshot{}, "", err
	}

	visible := make(map[uuid.UUID]*workSources)
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
	eligibleWorkIDs := make([]uuid.UUID, 0, len(workIDs))
	curationsByWork := make(map[uuid.UUID]catalogValue, len(workIDs))
	eligibilityByWork := make(
		map[uuid.UUID]biomedicalEligibilityRevision,
		len(workIDs),
	)
	curationFacts := make([]curationRevisionFact, 0, len(workIDs))
	revisionSources := make([]sourceRevisionFact, 0)
	for _, workID := range workIDs {
		curation, accepted, curationErr := loadAcceptedCuration(
			ctx,
			tx,
			workID,
			input,
		)
		if curationErr != nil {
			return catalogSnapshot{}, "", curationErr
		}
		if !accepted {
			continue
		}
		eligibleWorkIDs = append(eligibleWorkIDs, workID)
		curationsByWork[workID] = catalogValue{
			State: "known",
			Value: curation.Curation,
		}
		eligibilityByWork[workID] = curation.Eligibility
		curationFacts = append(curationFacts, curation)
		for _, state := range visible[workID].states {
			revisionSources = append(revisionSources, sourceRevisionFact{
				LogicalSource:              state.logicalSource,
				EventKey:                   state.eventKey,
				NormalizedAssertionID:      state.normalizedAssertionID.String(),
				RawEventID:                 state.rawEventID.String(),
				SourceRecordID:             state.sourceRecordID.String(),
				WorkID:                     state.workID.String(),
				SourceTime:                 state.sourceTime.UTC().Format(time.RFC3339Nano),
				TieBreakKey:                state.tieBreakKey,
				Position:                   state.position,
				ScopePolicyVersion:         state.scopePolicyVersion,
				ProjectionPolicyVersion:    state.projectionPolicyVersion,
				NormalizationPolicyVersion: state.normalizationPolicyVersion,
				NormalizedPayloadSchema:    state.normalizedPayloadSchema,
				NormalizedPayload:          state.normalizedPayload,
			})
		}
	}
	if len(eligibleWorkIDs) == 0 {
		return catalogSnapshot{}, "", ErrEmptyDomain
	}
	cohortRevision, err := biomedicalCohortRevision(
		eligibleWorkIDs,
		visible,
		curationFacts,
	)
	if err != nil {
		return catalogSnapshot{}, "", err
	}
	biomedicalRuns, err := validateBiomedicalAnalysisRuns(
		ctx,
		tx,
		input,
		cohortRevision,
	)
	if err != nil {
		return catalogSnapshot{}, "", err
	}
	sort.Slice(revisionSources, func(i, j int) bool {
		left, right := revisionSources[i], revisionSources[j]
		if left.LogicalSource != right.LogicalSource {
			return left.LogicalSource < right.LogicalSource
		}
		return left.EventKey < right.EventKey
	})

	publicationStatesByWork, err := loadCurrentPublicationStates(
		ctx,
		tx,
		eligibleWorkIDs,
	)
	if err != nil {
		return catalogSnapshot{}, "", err
	}
	topicFacts := make(map[uuid.UUID]*taxonomyFact)
	methodFacts := make(map[uuid.UUID]*taxonomyFact)
	papers := make([]publishedPaper, 0, len(eligibleWorkIDs))
	for _, workID := range eligibleWorkIDs {
		paper, topics, methods, err := buildPaper(
			ctx,
			tx,
			workID,
			visible[workID],
			curationsByWork[workID],
			eligibilityByWork[workID],
			input.JCRImportReceipt,
			analysisRun,
			publicationStatesByWork[workID],
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
	papers, trends, citationMomentum, err := buildCitationMomentum(
		analysisRun,
		papers,
	)
	if err != nil {
		return catalogSnapshot{}, "", err
	}

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
	home,
		subjectListMetadata,
		journalListMetadata,
		subjects,
		journals,
		err := buildBiomedicalSnapshots(
		ctx,
		tx,
		input,
		papers,
		curationFacts,
		citationMomentum,
	)
	if err != nil {
		return catalogSnapshot{}, "", err
	}
	home,
		subjects,
		journals,
		opportunities,
		err := publishBiomedicalAnalysisSnapshots(
		ctx,
		tx,
		biomedicalRuns,
		papers,
		home,
		subjects,
		journals,
	)
	if err != nil {
		return catalogSnapshot{}, "", err
	}

	snapshot := catalogSnapshot{
		stats:               stats,
		home:                home,
		subjectListMetadata: subjectListMetadata,
		journalListMetadata: journalListMetadata,
		papers:              papers,
		topics:              topics,
		methods:             methods,
		subjects:            subjects,
		journals:            journals,
		trends:              trends,
		opportunities:       opportunities,
		sources:             revisionSources,
		curations:           curationFacts,
	}
	sourceRevision, err := snapshotRevision(input, snapshot)
	if err != nil {
		return catalogSnapshot{}, "", err
	}
	return snapshot, sourceRevision, nil
}

func biomedicalCohortRevision(
	workIDs []uuid.UUID,
	visible map[uuid.UUID]*workSources,
	curations []curationRevisionFact,
) (string, error) {
	curationByWork := make(map[uuid.UUID]curationRevisionFact, len(curations))
	for _, curation := range curations {
		if curation.WorkID == uuid.Nil ||
			curation.VenueID == uuid.Nil ||
			curation.Eligibility.ID == uuid.Nil {
			return "", fmt.Errorf(
				"%w: biomedical cohort contains incomplete curation evidence",
				ErrCatalogNotReady,
			)
		}
		if _, duplicate := curationByWork[curation.WorkID]; duplicate {
			return "", fmt.Errorf(
				"%w: biomedical cohort contains duplicate curation for Work %s",
				ErrCatalogNotReady,
				curation.WorkID,
			)
		}
		curationByWork[curation.WorkID] = curation
	}

	facts := make([]analysis.CohortWorkRevisionFact, 0, len(workIDs))
	for _, workID := range workIDs {
		curation, found := curationByWork[workID]
		if !found {
			return "", fmt.Errorf(
				"%w: biomedical cohort Work %s lacks curation evidence",
				ErrCatalogNotReady,
				workID,
			)
		}
		sources := visible[workID]
		if sources == nil || len(sources.states) == 0 {
			return "", fmt.Errorf(
				"%w: biomedical cohort Work %s lacks current source evidence",
				ErrCatalogNotReady,
				workID,
			)
		}
		assertionIDs := make([]uuid.UUID, 0, len(sources.states))
		for _, state := range sources.states {
			if state.normalizedAssertionID == uuid.Nil {
				return "", fmt.Errorf(
					"%w: biomedical cohort Work %s has a missing normalized assertion",
					ErrCatalogNotReady,
					workID,
				)
			}
			assertionIDs = append(assertionIDs, state.normalizedAssertionID)
		}
		facts = append(facts, analysis.CohortWorkRevisionFact{
			WorkID:                workID,
			VenueID:               curation.VenueID,
			EligibilityDecisionID: curation.Eligibility.ID,
			SourceAssertionIDs:    assertionIDs,
		})
	}
	revision, err := analysis.ComputeCohortRevision(facts)
	if err != nil {
		return "", fmt.Errorf(
			"%w: compute biomedical cohort revision: %v",
			ErrCatalogNotReady,
			err,
		)
	}
	return revision, nil
}

func loadCurrentSourceStates(ctx context.Context, tx pgx.Tx) ([]sourceState, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			state.logical_source,
			state.event_key,
			COALESCE(state.normalized_assertion_id::text, ''),
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
			normalized.id IS NOT NULL,
			COALESCE(normalized.normalization_policy_version, ''),
			COALESCE(normalized.payload_schema_version, ''),
			COALESCE(normalized.normalized_payload::text, ''),
			association.source_record_id IS NOT NULL
		FROM ingestion_source_states AS state
		LEFT JOIN ingestion_normalized_records AS normalized
		  ON normalized.id = state.normalized_assertion_id
		 AND normalized.raw_event_id = state.raw_event_id
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
			normalizedID      string
			rawEventID        string
			sourceRecordID    string
			workID            string
			normalizedPayload string
		)
		if err := rows.Scan(
			&state.logicalSource,
			&state.eventKey,
			&normalizedID,
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
			&state.normalizedPayloadSchema,
			&normalizedPayload,
			&state.hasWorkLink,
		); err != nil {
			return nil, fmt.Errorf("scan current ingestion source state: %w", err)
		}
		var err error
		if normalizedID != "" {
			state.normalizedAssertionID, err = uuid.Parse(normalizedID)
			if err != nil {
				return nil, fmt.Errorf(
					"%w: invalid normalized assertion ID",
					ErrCatalogNotReady,
				)
			}
		}
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
			if state.normalizedAssertionID == uuid.Nil ||
				state.normalizationPolicyVersion == "" ||
				state.normalizedPayloadSchema == "" ||
				normalizedPayload == "" {
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
	curation catalogValue,
	eligibility biomedicalEligibilityRevision,
	jcrImportReceipt uuid.UUID,
	analysisRun citationAnalysisRun,
	publicationState *publishedPublicationState,
) (publishedPaper, []taxonomyFact, []taxonomyFact, error) {
	var paper publishedPaper
	err := tx.QueryRow(ctx, `
		SELECT
			id,
			canonical_key,
			title,
			status
		FROM works
		WHERE id = $1
	`, workID).Scan(
		&paper.ID,
		&paper.CanonicalKey,
		&paper.Title,
		&paper.LifecycleStatus,
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

	citationValue,
		citationCount,
		citationSnapshots,
		citationVelocity,
		citationPercentile,
		citationAnalysisEvidence,
		err := loadCitationAnalysisEvidence(
		ctx,
		tx,
		workID,
		analysisRun,
	)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	paper.CitationCountState = citationValue.State
	paper.CitationCountValue = citationCount
	paper.TrendScoreState = "missing"
	paper.CitationTrend, err = loadCitationTrendEvidence(
		ctx,
		tx,
		workID,
		analysisRun,
	)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}

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
	provenance := sourceProvenance(sources.states)
	biomedical, err := loadBiomedicalPaperPayload(
		ctx,
		tx,
		workID,
		eligibility,
		jcrImportReceipt,
	)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	paper.biomedical = biomedical

	paperPayload := map[string]any{
		"id":                         paper.ID,
		"canonical_key":              paper.CanonicalKey,
		"title":                      paper.Title,
		"status":                     paper.LifecycleStatus,
		"published_at":               publishedAtValue,
		"type":                       paperTypeValue,
		"abstract":                   abstractValue,
		"has_code":                   hasCode,
		"has_data":                   hasData,
		"has_benchmark":              hasBenchmark,
		"citation_source":            catalogValue{State: "known", Value: analysisRun.Source},
		"citation_count":             citationValue,
		"trend_score":                catalogValue{State: "missing"},
		"topics":                     taxonomyReferences(topics),
		"methods":                    taxonomyReferences(methods),
		"authors":                    authors,
		"curation":                   curation,
		"journal":                    biomedical.Journal,
		"subjects":                   biomedical.Subjects,
		"mesh_headings":              biomedical.MeSHHeadings,
		"publication_types":          biomedical.PublicationTypes,
		"publication_types_state":    biomedical.PublicationTypesState,
		"jcr_assessment":             biomedical.JCRAssessment,
		"citation_snapshots":         citationSnapshots,
		"citation_velocity":          citationVelocity,
		"citation_percentile":        citationPercentile,
		"citation_analysis_evidence": citationAnalysisEvidence,
		"article_usage":              catalogValue{State: "missing"},
		"open_fulltext":              catalogValue{State: "missing"},
		"source_provenance":          catalogValue{State: "known", Value: provenance},
	}
	payload, err := marshalCatalogPayload(paperPayload)
	if err != nil {
		return publishedPaper{}, nil, nil, err
	}
	paper.SummaryPayload = payload
	paper.DetailPayload = payload
	paper.PublicationState = publicationState

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
	searchParts = append(searchParts, biomedical.Journal.Title)
	for _, subject := range biomedical.Subjects {
		searchParts = append(searchParts, subject.Name)
	}
	if biomedical.MeSHHeadings.State == "known" {
		headings, ok := biomedical.MeSHHeadings.Value.([]meshHeadingPayload)
		if !ok {
			return publishedPaper{}, nil, nil, fmt.Errorf(
				"%w: work %s has invalid in-memory MeSH payload",
				ErrCatalogNotReady,
				workID,
			)
		}
		for _, heading := range headings {
			searchParts = append(searchParts, heading.Label)
			for _, qualifier := range heading.Qualifiers {
				searchParts = append(searchParts, qualifier.Label)
			}
		}
	}
	searchParts = append(searchParts, biomedical.PublicationTypes...)
	searchParts = append(searchParts, paper.SourceNames...)
	paper.SearchText = strings.Join(searchParts, " ")
	return paper, topics, methods, nil
}

func loadCurrentPublicationStates(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
) (map[uuid.UUID]*publishedPublicationState, error) {
	statesByWork := make(
		map[uuid.UUID]*publishedPublicationState,
		len(workIDs),
	)
	if len(workIDs) == 0 {
		return statesByWork, nil
	}

	rows, err := tx.Query(ctx, `
		WITH requested AS (
			SELECT work_id, ordinal
			FROM unnest($1::uuid[]) WITH ORDINALITY AS input(work_id, ordinal)
		),
		exact_winners AS (
			SELECT
				winner.work_id,
				projection.id AS projection_assertion_id,
				normalized.id AS normalized_assertion_id,
				source_record.id AS source_record_id,
				source_record.source,
				source_record.source_record_id AS source_record_external_id,
				normalized.payload_schema_version,
				normalized.normalized_payload::text AS normalized_payload
			FROM work_projection_states AS winner
			JOIN ingestion_projection_assertions AS projection
			  ON projection.work_id = winner.work_id
			 AND projection.raw_event_id = winner.raw_event_id
			 AND projection.normalized_assertion_id =
			     winner.normalized_assertion_id
			 AND projection.source_record_uuid = winner.source_record_uuid
			 AND projection.scope_policy_version =
			     winner.scope_policy_version
			 AND projection.projection_policy_version =
			     winner.projection_policy_version
			JOIN ingestion_normalized_records AS normalized
			  ON normalized.id = projection.normalized_assertion_id
			 AND normalized.raw_event_id = projection.raw_event_id
			 AND normalized.source_record_uuid =
			     projection.source_record_uuid
			JOIN source_records AS source_record
			  ON source_record.id = winner.source_record_uuid
			JOIN ingestion_raw_events AS raw
			  ON raw.id = winner.raw_event_id
			 AND raw.logical_source = source_record.source
			 AND raw.source_record_id = source_record.source_record_id
			JOIN ingestion_source_states AS source_state
			  ON source_state.logical_source = raw.logical_source
			 AND source_state.event_key = raw.event_key
			 AND source_state.raw_event_id = winner.raw_event_id
			 AND source_state.normalized_assertion_id =
			     winner.normalized_assertion_id
			 AND source_state.source_record_uuid =
			     winner.source_record_uuid
			 AND source_state.work_id = winner.work_id
			 AND source_state.source_time = winner.source_time
			 AND source_state.tie_break_key = winner.tie_break_key
			 AND source_state.position = winner.position
			 AND source_state.scope_policy_version =
			     winner.scope_policy_version
			 AND source_state.projection_policy_version =
			     winner.projection_policy_version
			 AND source_state.scope_status = 'included'
			 AND NOT source_state.is_deleted
			JOIN source_record_works AS association
			  ON association.source_record_id = winner.source_record_uuid
			 AND association.work_id = winner.work_id
			WHERE winner.work_id = ANY($1::uuid[])
		)
		SELECT
			requested.work_id,
			winner.projection_assertion_id,
			winner.normalized_assertion_id,
			winner.source_record_id,
			winner.source,
			winner.source_record_external_id,
			winner.payload_schema_version,
			winner.normalized_payload,
			publication.work_id IS NOT NULL,
			publication.projection_assertion_id,
			publication.normalized_assertion_id,
			publication.source_record_id,
			publication.print_published_on,
			publication.print_published_state,
			publication.electronic_published_on,
			publication.electronic_published_state,
			publication.ahead_of_print_on,
			publication.ahead_of_print_state,
			publication.accepted_on,
			publication.accepted_state,
			publication.publication_model_raw,
			publication.publication_status_raw
		FROM requested
		LEFT JOIN exact_winners AS winner
		  ON winner.work_id = requested.work_id
		LEFT JOIN work_publication_states AS publication
		  ON publication.work_id = requested.work_id
		ORDER BY requested.ordinal
	`, workIDs)
	if err != nil {
		return nil, fmt.Errorf("query bulk current publication evidence: %w", err)
	}
	defer rows.Close()

	seedsByWork := make(map[uuid.UUID]publicationEvidenceSeed, len(workIDs))
	seen := make(map[uuid.UUID]struct{}, len(workIDs))
	for rows.Next() {
		var (
			workID                 uuid.UUID
			winnerProjectionID     pgtype.UUID
			winnerNormalizedID     pgtype.UUID
			winnerSourceRecordID   pgtype.UUID
			winnerSource           pgtype.Text
			sourceRecordExternalID pgtype.Text
			normalizedSchema       pgtype.Text
			normalizedPayload      pgtype.Text
			publicationStateExists bool
			stateProjectionID      pgtype.UUID
			stateNormalizedID      pgtype.UUID
			stateSourceRecordID    pgtype.UUID
			printDate              pgtype.Date
			printState             pgtype.Text
			electronicDate         pgtype.Date
			electronicState        pgtype.Text
			aheadDate              pgtype.Date
			aheadState             pgtype.Text
			acceptedDate           pgtype.Date
			acceptedState          pgtype.Text
			publicationModel       pgtype.Text
			publicationStatus      pgtype.Text
		)
		if err := rows.Scan(
			&workID,
			&winnerProjectionID,
			&winnerNormalizedID,
			&winnerSourceRecordID,
			&winnerSource,
			&sourceRecordExternalID,
			&normalizedSchema,
			&normalizedPayload,
			&publicationStateExists,
			&stateProjectionID,
			&stateNormalizedID,
			&stateSourceRecordID,
			&printDate,
			&printState,
			&electronicDate,
			&electronicState,
			&aheadDate,
			&aheadState,
			&acceptedDate,
			&acceptedState,
			&publicationModel,
			&publicationStatus,
		); err != nil {
			return nil, fmt.Errorf(
				"scan bulk current publication evidence: %w",
				err,
			)
		}
		if _, duplicate := seen[workID]; duplicate {
			return nil, fmt.Errorf(
				"%w: work %s has multiple exact current normalized winners",
				ErrCatalogNotReady,
				workID,
			)
		}
		seen[workID] = struct{}{}

		exactWinner := winnerProjectionID.Valid &&
			winnerNormalizedID.Valid &&
			winnerSourceRecordID.Valid &&
			winnerSource.Valid &&
			sourceRecordExternalID.Valid &&
			normalizedSchema.Valid &&
			normalizedPayload.Valid
		if !exactWinner {
			if publicationStateExists {
				return nil, fmt.Errorf(
					"%w: work %s publication state has incomplete normalized provenance",
					ErrCatalogNotReady,
					workID,
				)
			}
			return nil, fmt.Errorf(
				"%w: work %s without publication state lacks exact current normalized winner provenance",
				ErrCatalogNotReady,
				workID,
			)
		}
		if !publicationStateExists {
			if normalizedSchema.String == "normalized-record/v2" {
				statesByWork[workID] = nil
				continue
			}
			return nil, fmt.Errorf(
				"%w: work %s current normalized schema %q requires an exact publication state",
				ErrCatalogNotReady,
				workID,
				normalizedSchema.String,
			)
		}
		if !stateProjectionID.Valid ||
			!stateNormalizedID.Valid ||
			!stateSourceRecordID.Valid ||
			!printState.Valid ||
			!electronicState.Valid ||
			!aheadState.Valid ||
			!acceptedState.Valid {
			return nil, fmt.Errorf(
				"%w: work %s publication state has incomplete normalized provenance",
				ErrCatalogNotReady,
				workID,
			)
		}

		state := publishedPublicationState{
			ProjectionAssertionID: pgUUIDValue(stateProjectionID),
			NormalizedAssertionID: pgUUIDValue(stateNormalizedID),
			SourceRecordID:        pgUUIDValue(stateSourceRecordID),
			Source:                winnerSource.String,
			PublicationModel:      optionalPGText(publicationModel),
			PublicationStatus:     optionalPGText(publicationStatus),
		}
		if state.ProjectionAssertionID != pgUUIDValue(winnerProjectionID) ||
			state.NormalizedAssertionID != pgUUIDValue(winnerNormalizedID) ||
			state.SourceRecordID != pgUUIDValue(winnerSourceRecordID) {
			return nil, fmt.Errorf(
				"%w: work %s publication state does not match the exact current work projection winner",
				ErrCatalogNotReady,
				workID,
			)
		}

		var payload normalizedPublicationPayloadV3
		if err := json.Unmarshal(
			[]byte(normalizedPayload.String),
			&payload,
		); err != nil {
			return nil, fmt.Errorf(
				"%w: work %s has invalid normalized publication payload: %v",
				ErrCatalogNotReady,
				workID,
				err,
			)
		}
		normalizedModel, err := normalizedPublicationRaw(
			payload.PublicationModel,
			"publication model",
		)
		if err != nil {
			return nil, catalogPublicationEvidenceError(workID, err)
		}
		normalizedStatus, err := normalizedPublicationRaw(
			payload.PublicationStatus,
			"publication status",
		)
		if err != nil {
			return nil, catalogPublicationEvidenceError(workID, err)
		}
		if state.Source != "pubmed" ||
			normalizedSchema.String != "normalized-record/v4" ||
			payload.Source != state.Source ||
			payload.SourceRecordID != sourceRecordExternalID.String ||
			!equalOptionalString(state.PublicationModel, normalizedModel) ||
			!equalOptionalString(state.PublicationStatus, normalizedStatus) {
			return nil, fmt.Errorf(
				"%w: work %s publication state does not match its exact normalized PubMed assertion",
				ErrCatalogNotReady,
				workID,
			)
		}
		expected, err := expectedPublicationEventAssertions(
			workID,
			state,
			payload,
		)
		if err != nil {
			return nil, err
		}
		seedsByWork[workID] = publicationEvidenceSeed{
			state: state,
			stored: map[string]publishedPublicationEventState{
				"print_published": {
					State: printState.String,
					Date:  pgDatePointer(printDate),
				},
				"electronic_published": {
					State: electronicState.String,
					Date:  pgDatePointer(electronicDate),
				},
				"ahead_of_print": {
					State: aheadState.String,
					Date:  pgDatePointer(aheadDate),
				},
				"accepted": {
					State: acceptedState.String,
					Date:  pgDatePointer(acceptedDate),
				},
			},
			expected: expected,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate bulk current publication evidence: %w",
			err,
		)
	}
	if len(seen) != len(workIDs) {
		return nil, fmt.Errorf(
			"%w: bulk publication evidence returned %d Works, expected %d",
			ErrCatalogNotReady,
			len(seen),
			len(workIDs),
		)
	}

	assertionsByProjection, err := loadPublicationEventAssertionSets(
		ctx,
		tx,
		workIDs,
	)
	if err != nil {
		return nil, err
	}
	for _, workID := range workIDs {
		seed, found := seedsByWork[workID]
		if !found {
			continue
		}
		key := publicationAssertionSetKey{
			workID:                workID,
			projectionAssertionID: seed.state.ProjectionAssertionID,
		}
		assertions := assertionsByProjection[key]
		if err := validatePublicationEventAssertionSet(
			workID,
			seed.state,
			seed.expected,
			assertions,
		); err != nil {
			return nil, err
		}
		computed, err := publicationEventStatesFromAssertions(
			workID,
			seed.state,
			assertions,
		)
		if err != nil {
			return nil, err
		}
		for _, eventKind := range publicationEventKinds() {
			storedState := seed.stored[eventKind]
			computedState := computed[eventKind]
			if publicationEventStateEqual(storedState, computedState) {
				continue
			}
			if storedState.State == "known" {
				return nil, fmt.Errorf(
					"%w: work %s known publication event %s lacks matching exact immutable event provenance",
					ErrCatalogNotReady,
					workID,
					eventKind,
				)
			}
			return nil, fmt.Errorf(
				"%w: work %s publication state %s does not match exact immutable event assertions",
				ErrCatalogNotReady,
				workID,
				eventKind,
			)
		}
		seed.state.PrintPublished = computed["print_published"]
		seed.state.ElectronicPublished = computed["electronic_published"]
		seed.state.AheadOfPrint = computed["ahead_of_print"]
		seed.state.Accepted = computed["accepted"]
		state := seed.state
		statesByWork[workID] = &state
	}
	return statesByWork, nil
}

type publicationAssertionSetKey struct {
	workID                uuid.UUID
	projectionAssertionID uuid.UUID
}

func loadPublicationEventAssertionSets(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
) (map[publicationAssertionSetKey][]publicationEventAssertionEvidence, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			work_id,
			event_kind,
			event_date,
			date_precision,
			source_date,
			status_raw,
			publication_model_raw,
			source_path,
			ordinal
		FROM work_publication_event_assertions
		WHERE work_id = ANY($1::uuid[])
		ORDER BY work_id, projection_assertion_id, ordinal
	`, workIDs)
	if err != nil {
		return nil, fmt.Errorf(
			"query bulk publication event assertion sets: %w",
			err,
		)
	}
	defer rows.Close()

	assertionsByProjection := make(
		map[publicationAssertionSetKey][]publicationEventAssertionEvidence,
	)
	for rows.Next() {
		var assertion publicationEventAssertionEvidence
		if err := rows.Scan(
			&assertion.ProjectionAssertionID,
			&assertion.NormalizedAssertionID,
			&assertion.SourceRecordID,
			&assertion.WorkID,
			&assertion.EventKind,
			&assertion.EventDate,
			&assertion.DatePrecision,
			&assertion.SourceDate,
			&assertion.StatusRaw,
			&assertion.PublicationModelRaw,
			&assertion.SourcePath,
			&assertion.Ordinal,
		); err != nil {
			return nil, fmt.Errorf(
				"scan bulk publication event assertion set: %w",
				err,
			)
		}
		key := publicationAssertionSetKey{
			workID:                assertion.WorkID,
			projectionAssertionID: assertion.ProjectionAssertionID,
		}
		assertionsByProjection[key] = append(
			assertionsByProjection[key],
			assertion,
		)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate bulk publication event assertion sets: %w",
			err,
		)
	}
	return assertionsByProjection, nil
}

func expectedPublicationEventAssertions(
	workID uuid.UUID,
	state publishedPublicationState,
	payload normalizedPublicationPayloadV3,
) ([]publicationEventAssertionEvidence, error) {
	expected := make(
		[]publicationEventAssertionEvidence,
		0,
		len(payload.PublicationHistory),
	)
	seenOrdinals := make(map[int]struct{}, len(payload.PublicationHistory))
	for _, entry := range payload.PublicationHistory {
		if entry.Status == "" || entry.Status != strings.TrimSpace(entry.Status) {
			return nil, catalogPublicationEvidenceError(
				workID,
				fmt.Errorf(
					"publication history ordinal %d status must be non-empty and trimmed",
					entry.Ordinal,
				),
			)
		}
		if entry.Ordinal < 1 {
			return nil, catalogPublicationEvidenceError(
				workID,
				fmt.Errorf(
					"publication history ordinal must be positive, got %d",
					entry.Ordinal,
				),
			)
		}
		if entry.SourcePath == "" ||
			entry.SourcePath != strings.TrimSpace(entry.SourcePath) {
			return nil, catalogPublicationEvidenceError(
				workID,
				fmt.Errorf(
					"publication history ordinal %d source path must be non-empty and trimmed",
					entry.Ordinal,
				),
			)
		}
		eventDate, err := normalizedPublicationEventDate(entry.Date)
		if err != nil {
			return nil, catalogPublicationEvidenceError(
				workID,
				fmt.Errorf("publication history ordinal %d: %w", entry.Ordinal, err),
			)
		}
		sourceDate, err := json.Marshal(entry.Date)
		if err != nil {
			return nil, fmt.Errorf(
				"encode work %s normalized publication history ordinal %d source date: %w",
				workID,
				entry.Ordinal,
				err,
			)
		}
		eventKind := ""
		switch entry.Status {
		case "accepted":
			eventKind = "accepted"
		case "aheadofprint":
			eventKind = "ahead_of_print"
		case "ppublish":
			eventKind = "print_published"
		case "epublish":
			if state.PublicationStatus == nil {
				continue
			}
			switch *state.PublicationStatus {
			case "epublish", "ppublish":
				eventKind = "electronic_published"
			default:
				continue
			}
		default:
			continue
		}
		if _, duplicate := seenOrdinals[entry.Ordinal]; duplicate {
			return nil, catalogPublicationEvidenceError(
				workID,
				fmt.Errorf(
					"publication event assertion set has duplicate expected ordinal %d",
					entry.Ordinal,
				),
			)
		}
		seenOrdinals[entry.Ordinal] = struct{}{}
		expected = append(expected, publicationEventAssertionEvidence{
			ProjectionAssertionID: state.ProjectionAssertionID,
			NormalizedAssertionID: state.NormalizedAssertionID,
			SourceRecordID:        state.SourceRecordID,
			WorkID:                workID,
			EventKind:             eventKind,
			EventDate:             eventDate,
			DatePrecision:         entry.Date.Precision,
			SourceDate:            sourceDate,
			StatusRaw:             entry.Status,
			PublicationModelRaw:   copyOptionalString(state.PublicationModel),
			SourcePath:            entry.SourcePath,
			Ordinal:               entry.Ordinal,
		})
	}
	return expected, nil
}

func validatePublicationEventAssertionSet(
	workID uuid.UUID,
	state publishedPublicationState,
	expected []publicationEventAssertionEvidence,
	persisted []publicationEventAssertionEvidence,
) error {
	expectedByOrdinal := make(
		map[int]publicationEventAssertionEvidence,
		len(expected),
	)
	for _, assertion := range expected {
		expectedByOrdinal[assertion.Ordinal] = assertion
	}
	for _, assertion := range persisted {
		expectedAssertion, found := expectedByOrdinal[assertion.Ordinal]
		if !found {
			return catalogPublicationAssertionSetError(
				workID,
				fmt.Sprintf("unexpected ordinal %d", assertion.Ordinal),
			)
		}
		sourceDateMatches, err := publicationSourceDatesEqual(
			assertion.SourceDate,
			expectedAssertion.SourceDate,
		)
		if err != nil {
			return fmt.Errorf(
				"compare work %s publication event assertion ordinal %d source date: %w",
				workID,
				assertion.Ordinal,
				err,
			)
		}
		if assertion.ProjectionAssertionID != state.ProjectionAssertionID ||
			assertion.NormalizedAssertionID != state.NormalizedAssertionID ||
			assertion.SourceRecordID != state.SourceRecordID ||
			assertion.WorkID != workID ||
			assertion.EventKind != expectedAssertion.EventKind ||
			!publicationEventDatesEqual(
				assertion.EventDate,
				expectedAssertion.EventDate,
			) ||
			assertion.DatePrecision != expectedAssertion.DatePrecision ||
			!sourceDateMatches ||
			assertion.StatusRaw != expectedAssertion.StatusRaw ||
			!equalOptionalString(
				assertion.PublicationModelRaw,
				expectedAssertion.PublicationModelRaw,
			) ||
			assertion.SourcePath != expectedAssertion.SourcePath {
			return catalogPublicationAssertionSetError(
				workID,
				fmt.Sprintf("ordinal %d fields differ", assertion.Ordinal),
			)
		}
		delete(expectedByOrdinal, assertion.Ordinal)
	}
	if len(persisted) != len(expected) || len(expectedByOrdinal) != 0 {
		return catalogPublicationAssertionSetError(
			workID,
			fmt.Sprintf(
				"persisted %d assertions, expected %d",
				len(persisted),
				len(expected),
			),
		)
	}
	return nil
}

func publicationEventStatesFromAssertions(
	workID uuid.UUID,
	state publishedPublicationState,
	assertions []publicationEventAssertionEvidence,
) (map[string]publishedPublicationEventState, error) {
	type eventDateEvidence struct {
		date             time.Time
		publicationModel *string
		provenance       publishedPublicationEventProvenance
	}
	dayEvidence := map[string]map[string]eventDateEvidence{
		"print_published":      {},
		"electronic_published": {},
		"ahead_of_print":       {},
		"accepted":             {},
	}
	for _, assertion := range assertions {
		evidenceByDate, supported := dayEvidence[assertion.EventKind]
		if !supported {
			return nil, fmt.Errorf(
				"%w: work %s publication event assertion is inconsistent with exact provenance",
				ErrCatalogNotReady,
				workID,
			)
		}
		if assertion.DatePrecision != "day" {
			continue
		}
		if assertion.EventDate == nil {
			return nil, fmt.Errorf(
				"%w: work %s day-precision publication event has no date",
				ErrCatalogNotReady,
				workID,
			)
		}
		date := normalizedPublicationDate(assertion.EventDate)
		dateKey := date.Format("2006-01-02")
		if _, duplicateDate := evidenceByDate[dateKey]; duplicateDate {
			continue
		}
		evidenceByDate[dateKey] = eventDateEvidence{
			date:             *date,
			publicationModel: copyOptionalString(assertion.PublicationModelRaw),
			provenance: publishedPublicationEventProvenance{
				Source:                state.Source,
				SourceRecordID:        state.SourceRecordID,
				NormalizedAssertionID: state.NormalizedAssertionID,
				ProjectionAssertionID: state.ProjectionAssertionID,
				SourcePath:            assertion.SourcePath,
				StatusRaw:             assertion.StatusRaw,
			},
		}
	}

	result := make(map[string]publishedPublicationEventState, len(dayEvidence))
	for eventKind, evidenceByDate := range dayEvidence {
		switch len(evidenceByDate) {
		case 0:
			result[eventKind] = publishedPublicationEventState{State: "missing"}
		case 1:
			for _, evidence := range evidenceByDate {
				date := evidence.date
				provenance := evidence.provenance
				result[eventKind] = publishedPublicationEventState{
					State:            "known",
					Date:             &date,
					PublicationModel: copyOptionalString(evidence.publicationModel),
					Provenance:       &provenance,
				}
			}
		default:
			result[eventKind] = publishedPublicationEventState{State: "conflict"}
		}
	}
	return result, nil
}

func normalizedPublicationEventDate(
	value normalizedPublicationDateV3,
) (*time.Time, error) {
	if value.Year <= 0 {
		return nil, fmt.Errorf("publication date year %d is invalid", value.Year)
	}
	switch value.Precision {
	case "year":
		return nil, nil
	case "month":
		if value.Month < int(time.January) || value.Month > int(time.December) {
			return nil, fmt.Errorf(
				"publication date month %d is invalid",
				value.Month,
			)
		}
		return nil, nil
	case "day":
		if value.Month < int(time.January) || value.Month > int(time.December) {
			return nil, fmt.Errorf(
				"publication date month %d is invalid",
				value.Month,
			)
		}
		date := time.Date(
			value.Year,
			time.Month(value.Month),
			value.Day,
			0,
			0,
			0,
			0,
			time.UTC,
		)
		if date.Year() != value.Year ||
			int(date.Month()) != value.Month ||
			date.Day() != value.Day {
			return nil, fmt.Errorf(
				"publication date %04d-%02d-%02d is invalid",
				value.Year,
				value.Month,
				value.Day,
			)
		}
		return &date, nil
	default:
		return nil, fmt.Errorf(
			"publication date precision %q is unsupported",
			value.Precision,
		)
	}
}

func normalizedPublicationRaw(value string, field string) (*string, error) {
	if value == "" {
		return nil, nil
	}
	if value != strings.TrimSpace(value) {
		return nil, fmt.Errorf("%s must be trimmed", field)
	}
	return stringPointerCopy(value), nil
}

func publicationSourceDatesEqual(
	left json.RawMessage,
	right json.RawMessage,
) (bool, error) {
	var leftValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false, err
	}
	var rightValue any
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false, err
	}
	return reflect.DeepEqual(leftValue, rightValue), nil
}

func publicationEventDatesEqual(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return normalizedPublicationDate(left).Equal(*normalizedPublicationDate(right))
}

func publicationEventKinds() []string {
	return []string{
		"print_published",
		"electronic_published",
		"ahead_of_print",
		"accepted",
	}
}

func catalogPublicationEvidenceError(workID uuid.UUID, err error) error {
	return fmt.Errorf(
		"%w: work %s normalized publication evidence is invalid: %v",
		ErrCatalogNotReady,
		workID,
		err,
	)
}

func catalogPublicationAssertionSetError(workID uuid.UUID, detail string) error {
	return fmt.Errorf(
		"%w: work %s publication event assertion set conflicts with exact normalized history: %s",
		ErrCatalogNotReady,
		workID,
		detail,
	)
}

func pgUUIDValue(value pgtype.UUID) uuid.UUID {
	return uuid.UUID(value.Bytes)
}

func pgDatePointer(value pgtype.Date) *time.Time {
	if !value.Valid {
		return nil
	}
	return normalizedPublicationDate(&value.Time)
}

func optionalPGText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return stringPointerCopy(value.String)
}

func stringPointerCopy(value string) *string {
	copied := value
	return &copied
}

func normalizedPublicationDate(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := time.Date(
		value.Year(),
		value.Month(),
		value.Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	)
	return &normalized
}

func publicationEventStateEqual(
	left publishedPublicationEventState,
	right publishedPublicationEventState,
) bool {
	if left.State != right.State {
		return false
	}
	if left.Date == nil || right.Date == nil {
		return left.Date == nil && right.Date == nil
	}
	return left.Date.Equal(*right.Date)
}

func loadBiomedicalPaperPayload(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	eligibility biomedicalEligibilityRevision,
	jcrImportReceipt uuid.UUID,
) (biomedicalPaperPayload, error) {
	if eligibility.ID == uuid.Nil ||
		eligibility.Decision != "accepted" ||
		eligibility.SubjectVersionID == uuid.Nil {
		return biomedicalPaperPayload{}, fmt.Errorf(
			"%w: work %s lacks accepted persisted biomedical eligibility",
			ErrCatalogNotReady,
			workID,
		)
	}

	var journal journalReference
	if err := tx.QueryRow(ctx, `
		SELECT venue.id, venue.display_title
		FROM works AS work
		JOIN venues AS venue
		  ON venue.id = work.venue_id
		WHERE work.id = $1
		  AND venue.id = (
			SELECT decision.venue_id
			FROM biomedical_publication_eligibility_decisions AS decision
			WHERE decision.id = $2
		  )
	`, workID, eligibility.ID).Scan(&journal.ID, &journal.Title); err != nil {
		return biomedicalPaperPayload{}, fmt.Errorf(
			"query work %s biomedical journal identity: %w",
			workID,
			err,
		)
	}
	if journal.Title == "" || journal.Title != strings.TrimSpace(journal.Title) {
		return biomedicalPaperPayload{}, fmt.Errorf(
			"%w: work %s journal has invalid display title",
			ErrCatalogNotReady,
			workID,
		)
	}
	journal.Slug = "journal-" + strings.ReplaceAll(journal.ID.String(), "-", "")

	subjects, err := loadEligibilitySubjects(
		ctx,
		tx,
		eligibility,
		jcrImportReceipt,
	)
	if err != nil {
		return biomedicalPaperPayload{}, fmt.Errorf(
			"load work %s biomedical Subjects: %w",
			workID,
			err,
		)
	}
	meshHeadings, publicationTypes, publicationTypesState, err :=
		loadCurrentBiomedicalSemantics(ctx, tx, workID)
	if err != nil {
		return biomedicalPaperPayload{}, err
	}

	return biomedicalPaperPayload{
		Journal:               journal,
		Subjects:              subjects,
		MeSHHeadings:          meshHeadings,
		PublicationTypes:      publicationTypes,
		PublicationTypesState: publicationTypesState,
		JCRAssessment: catalogValue{
			State: "known",
			Value: eligibility,
		},
	}, nil
}

func loadEligibilitySubjects(
	ctx context.Context,
	tx pgx.Tx,
	eligibility biomedicalEligibilityRevision,
	jcrImportReceipt uuid.UUID,
) ([]taxonomyReference, error) {
	var evidence struct {
		Matches []struct {
			JournalSubjectMetricID string `json:"journal_subject_metric_id"`
			VenueMetricSnapshotID  string `json:"venue_metric_snapshot_id"`
			VenueID                string `json:"venue_id"`
			MetricYear             int    `json:"metric_year"`
			SubjectVersionID       string `json:"subject_version_id"`
			SubjectID              string `json:"subject_id"`
			SubjectRuleID          string `json:"subject_rule_id"`
			SubjectSlug            string `json:"subject_slug"`
			JCRCategory            string `json:"jcr_category"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(eligibility.Evidence, &evidence); err != nil {
		return nil, fmt.Errorf(
			"%w: decode biomedical eligibility evidence: %v",
			ErrCatalogNotReady,
			err,
		)
	}
	if len(evidence.Matches) == 0 {
		return nil, fmt.Errorf(
			"%w: accepted biomedical eligibility has no Subject matches",
			ErrCatalogNotReady,
		)
	}

	seen := make(map[uuid.UUID]struct{}, len(evidence.Matches))
	subjects := make([]taxonomyReference, 0, len(evidence.Matches))
	for _, match := range evidence.Matches {
		subjectID, err := uuid.Parse(match.SubjectID)
		linkID, linkErr := uuid.Parse(match.JournalSubjectMetricID)
		metricID, metricErr := uuid.Parse(match.VenueMetricSnapshotID)
		venueID, venueErr := uuid.Parse(match.VenueID)
		subjectVersionID, versionErr := uuid.Parse(match.SubjectVersionID)
		subjectRuleID, ruleErr := uuid.Parse(match.SubjectRuleID)
		if err != nil ||
			linkErr != nil ||
			metricErr != nil ||
			venueErr != nil ||
			versionErr != nil ||
			ruleErr != nil ||
			subjectID == uuid.Nil ||
			linkID == uuid.Nil ||
			metricID == uuid.Nil ||
			venueID == uuid.Nil ||
			subjectVersionID != eligibility.SubjectVersionID ||
			subjectRuleID == uuid.Nil ||
			match.MetricYear != eligibility.MetricYear ||
			match.JCRCategory == "" ||
			match.JCRCategory != strings.TrimSpace(match.JCRCategory) ||
			!validSlug(match.SubjectSlug) {
			return nil, fmt.Errorf(
				"%w: accepted biomedical eligibility has invalid Subject identity",
				ErrCatalogNotReady,
			)
		}
		if _, duplicate := seen[subjectID]; duplicate {
			continue
		}
		seen[subjectID] = struct{}{}

		var subject taxonomyReference
		if err := tx.QueryRow(ctx, `
			SELECT subject.id, subject.slug, subject.display_label
			FROM jcr_import_receipt_metrics AS receipt_metric
			JOIN venue_metric_snapshots AS metric
			  ON metric.id = receipt_metric.metric_snapshot_id
			JOIN journal_subject_metrics AS link
			  ON link.id = $2
			 AND link.venue_metric_snapshot_id = metric.id
			JOIN biomedical_subject_rules AS rule
			  ON rule.id = link.subject_rule_id
			JOIN subjects AS subject
			  ON subject.id = rule.subject_id
			 AND subject.subject_version_id = rule.subject_version_id
			WHERE receipt_metric.import_receipt_id = $1
			  AND metric.id = $3
			  AND metric.venue_id = $4
			  AND metric.metric_year = $5
			  AND metric.category = $6
			  AND link.subject_rule_id = $7
			  AND link.jcr_category = metric.category
			  AND rule.subject_version_id = $8
			  AND rule.jcr_category = metric.category
			  AND subject.id = $9
		`,
			jcrImportReceipt,
			linkID,
			metricID,
			venueID,
			match.MetricYear,
			match.JCRCategory,
			subjectRuleID,
			subjectVersionID,
			subjectID,
		).Scan(
			&subject.ID,
			&subject.Slug,
			&subject.Name,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf(
					"%w: accepted biomedical eligibility Subject evidence does not belong to JCR receipt %s",
					ErrCatalogNotReady,
					jcrImportReceipt,
				)
			}
			return nil, fmt.Errorf(
				"query exact biomedical Subject %s: %w",
				subjectID,
				err,
			)
		}
		if subject.Slug != match.SubjectSlug ||
			subject.Name == "" ||
			subject.Name != strings.TrimSpace(subject.Name) {
			return nil, fmt.Errorf(
				"%w: persisted biomedical Subject evidence changed",
				ErrCatalogNotReady,
			)
		}
		subjects = append(subjects, subject)
	}
	sort.Slice(subjects, func(i, j int) bool {
		if subjects[i].Slug != subjects[j].Slug {
			return subjects[i].Slug < subjects[j].Slug
		}
		return subjects[i].ID.String() < subjects[j].ID.String()
	})
	return subjects, nil
}

func loadCurrentBiomedicalSemantics(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
) (catalogValue, []string, catalogValue, error) {
	var projectionAssertionID uuid.UUID
	var logicalSource string
	err := tx.QueryRow(ctx, `
		SELECT assertion.id, source_record.source
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.work_id = state.work_id
		 AND assertion.raw_event_id = state.raw_event_id
		 AND assertion.normalized_assertion_id = state.normalized_assertion_id
		 AND assertion.source_record_uuid = state.source_record_uuid
		 AND assertion.scope_policy_version = state.scope_policy_version
		 AND assertion.projection_policy_version = state.projection_policy_version
		JOIN source_records AS source_record
		  ON source_record.id = state.source_record_uuid
		WHERE state.work_id = $1
	`, workID).Scan(&projectionAssertionID, &logicalSource)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalogValue{State: "missing"},
			[]string{},
			catalogValue{State: "missing"},
			nil
	}
	if err != nil {
		return catalogValue{}, nil, catalogValue{}, fmt.Errorf(
			"query work %s current biomedical projection: %w",
			workID,
			err,
		)
	}
	if logicalSource != "pubmed" {
		return catalogValue{State: "missing"},
			[]string{},
			catalogValue{State: "missing"},
			nil
	}

	headings, err := loadMeSHHeadings(ctx, tx, workID, projectionAssertionID)
	if err != nil {
		return catalogValue{}, nil, catalogValue{}, err
	}
	publicationTypes, err := loadPublicationTypes(
		ctx,
		tx,
		workID,
		projectionAssertionID,
	)
	if err != nil {
		return catalogValue{}, nil, catalogValue{}, err
	}
	return catalogValue{State: "known", Value: headings},
		publicationTypes,
		catalogValue{State: "known", Value: publicationTypes},
		nil
}

func loadMeSHHeadings(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	projectionAssertionID uuid.UUID,
) ([]meshHeadingPayload, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			heading.id,
			descriptor.descriptor_ui,
			heading.descriptor_label,
			heading.is_major_topic,
			heading.source_path,
			qualifier.qualifier_ui,
			assertion.qualifier_label,
			assertion.is_major_topic,
			assertion.source_path
		FROM work_mesh_headings AS heading
		JOIN mesh_descriptors AS descriptor
		  ON descriptor.id = heading.descriptor_id
		LEFT JOIN work_mesh_qualifiers AS assertion
		  ON assertion.work_mesh_heading_id = heading.id
		 AND assertion.projection_assertion_id = heading.projection_assertion_id
		 AND assertion.source_record_id = heading.source_record_id
		 AND assertion.work_id = heading.work_id
		LEFT JOIN mesh_qualifiers AS qualifier
		  ON qualifier.id = assertion.qualifier_id
		WHERE heading.work_id = $1
		  AND heading.projection_assertion_id = $2
		ORDER BY descriptor.descriptor_ui, qualifier.qualifier_ui NULLS LAST
	`, workID, projectionAssertionID)
	if err != nil {
		return nil, fmt.Errorf("query work %s current MeSH headings: %w", workID, err)
	}
	defer rows.Close()

	headings := make([]meshHeadingPayload, 0)
	headingIndex := make(map[uuid.UUID]int)
	for rows.Next() {
		var (
			headingID                       uuid.UUID
			descriptorUI, label, sourcePath string
			majorTopic                      bool
			qualifierUI, qualifierLabel     *string
			qualifierSourcePath             *string
			qualifierMajorTopic             *bool
		)
		if err := rows.Scan(
			&headingID,
			&descriptorUI,
			&label,
			&majorTopic,
			&sourcePath,
			&qualifierUI,
			&qualifierLabel,
			&qualifierMajorTopic,
			&qualifierSourcePath,
		); err != nil {
			return nil, fmt.Errorf("scan work %s current MeSH heading: %w", workID, err)
		}
		index, exists := headingIndex[headingID]
		if !exists {
			if descriptorUI == "" || label == "" || sourcePath == "" {
				return nil, fmt.Errorf(
					"%w: work %s has incomplete MeSH heading evidence",
					ErrCatalogNotReady,
					workID,
				)
			}
			index = len(headings)
			headingIndex[headingID] = index
			headings = append(headings, meshHeadingPayload{
				DescriptorUI: descriptorUI,
				Label:        label,
				MajorTopic:   majorTopic,
				SourcePath:   sourcePath,
				Qualifiers:   []meshQualifierPayload{},
			})
		}
		if qualifierUI == nil {
			continue
		}
		if qualifierLabel == nil ||
			qualifierMajorTopic == nil ||
			qualifierSourcePath == nil ||
			*qualifierUI == "" ||
			*qualifierLabel == "" ||
			*qualifierSourcePath == "" {
			return nil, fmt.Errorf(
				"%w: work %s has incomplete MeSH qualifier evidence",
				ErrCatalogNotReady,
				workID,
			)
		}
		headings[index].Qualifiers = append(
			headings[index].Qualifiers,
			meshQualifierPayload{
				QualifierUI: *qualifierUI,
				Label:       *qualifierLabel,
				MajorTopic:  *qualifierMajorTopic,
				SourcePath:  *qualifierSourcePath,
			},
		)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work %s current MeSH headings: %w", workID, err)
	}
	return headings, nil
}

func loadPublicationTypes(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	projectionAssertionID uuid.UUID,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			publication_type.publication_type_ui,
			assertion.publication_type_label
		FROM work_publication_types AS assertion
		JOIN publication_types AS publication_type
		  ON publication_type.id = assertion.publication_type_id
		WHERE assertion.work_id = $1
		  AND assertion.projection_assertion_id = $2
		ORDER BY publication_type.publication_type_ui
	`, workID, projectionAssertionID)
	if err != nil {
		return nil, fmt.Errorf("query work %s current Publication Types: %w", workID, err)
	}
	defer rows.Close()

	values := make([]string, 0)
	for rows.Next() {
		var ui, label string
		if err := rows.Scan(&ui, &label); err != nil {
			return nil, fmt.Errorf("scan work %s current Publication Type: %w", workID, err)
		}
		if ui == "" || label == "" || label != strings.TrimSpace(label) {
			return nil, fmt.Errorf(
				"%w: work %s has incomplete Publication Type evidence",
				ErrCatalogNotReady,
				workID,
			)
		}
		values = append(values, label)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work %s current Publication Types: %w", workID, err)
	}
	return values, nil
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

func loadCitationAnalysisEvidence(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	run citationAnalysisRun,
) (
	catalogValue,
	*int64,
	catalogValue,
	catalogValue,
	catalogValue,
	catalogValue,
	error,
) {
	var (
		asOf, generatedAt                     time.Time
		windowDays                            int
		countState, velocityState             string
		currentSnapshotID, baselineSnapshotID pgtype.UUID
		countValue                            pgtype.Int8
		velocityValue                         pgtype.Text
		sourceRevision, formulaVersion        string
		rawWorkEvidence                       []byte
	)
	err := tx.QueryRow(ctx, `
		SELECT
			as_of,
			velocity_window_days,
			citation_count_state,
			current_snapshot_id,
			citation_count,
			citation_velocity_state,
			baseline_snapshot_id,
			citation_velocity::text,
			source_revision,
			formula_version,
			evidence,
			generated_at
		FROM citation_analysis_work_snapshots
		WHERE analysis_run_id = $1
		  AND work_id = $2
		  AND source = $3
	`, run.ID, workID, run.Source).Scan(
		&asOf,
		&windowDays,
		&countState,
		&currentSnapshotID,
		&countValue,
		&velocityState,
		&baselineSnapshotID,
		&velocityValue,
		&sourceRevision,
		&formulaVersion,
		&rawWorkEvidence,
		&generatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalogValue{},
			nil,
			catalogValue{},
			catalogValue{},
			catalogValue{},
			catalogValue{},
			fmt.Errorf(
				"%w: citation analysis run %s has no materialized Work %s",
				ErrCatalogNotReady,
				run.ID,
				workID,
			)
	}
	if err != nil {
		return catalogValue{},
			nil,
			catalogValue{},
			catalogValue{},
			catalogValue{},
			catalogValue{},
			fmt.Errorf(
				"query Work %s citation analysis run %s: %w",
				workID,
				run.ID,
				err,
			)
	}
	if !asOf.Equal(run.AsOf) ||
		windowDays < 1 ||
		formulaVersion != run.FormulaVersion ||
		generatedAt.IsZero() ||
		sourceRevision == "" ||
		!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sourceRevision) {
		return catalogValue{},
			nil,
			catalogValue{},
			catalogValue{},
			catalogValue{},
			catalogValue{},
			fmt.Errorf(
				"%w: Work %s citation analysis metadata conflicts with run %s",
				ErrCatalogNotReady,
				workID,
				run.ID,
			)
	}

	var workEvidence struct {
		Source                string          `json:"source"`
		AsOf                  string          `json:"as_of"`
		VelocityWindowDays    int             `json:"velocity_window_days"`
		CurrentSnapshotID     *uuid.UUID      `json:"current_snapshot_id"`
		BaselineSnapshotID    *uuid.UUID      `json:"baseline_snapshot_id"`
		ElapsedHours          *json.Number    `json:"elapsed_hours"`
		MissingSignals        []string        `json:"missing_signals"`
		SupportingSnapshotIDs []uuid.UUID     `json:"supporting_snapshot_ids"`
		Scope                 json.RawMessage `json:"scope"`
	}
	if err := decodeStrictJSONObject(rawWorkEvidence, &workEvidence); err != nil {
		return catalogValue{},
			nil,
			catalogValue{},
			catalogValue{},
			catalogValue{},
			catalogValue{},
			fmt.Errorf(
				"%w: decode Work %s citation analysis evidence: %v",
				ErrCatalogNotReady,
				workID,
				err,
			)
	}
	if workEvidence.Source != run.Source ||
		workEvidence.AsOf != run.AsOf.Format(time.RFC3339Nano) ||
		workEvidence.VelocityWindowDays != windowDays ||
		len(workEvidence.Scope) == 0 {
		return catalogValue{},
			nil,
			catalogValue{},
			catalogValue{},
			catalogValue{},
			catalogValue{},
			fmt.Errorf(
				"%w: Work %s citation analysis evidence conflicts with run %s",
				ErrCatalogNotReady,
				workID,
				run.ID,
			)
	}

	snapshots, err := loadCitationSnapshotsByID(
		ctx,
		tx,
		workID,
		run.Source,
		workEvidence.SupportingSnapshotIDs,
	)
	if err != nil {
		return catalogValue{},
			nil,
			catalogValue{},
			catalogValue{},
			catalogValue{},
			catalogValue{},
			err
	}
	snapshotsValue := catalogValue{State: "missing"}
	if len(snapshots) != 0 {
		snapshotsValue = catalogValue{
			State: "known",
			Value: snapshots,
		}
	}

	var count *int64
	countCatalogValue := catalogValue{State: countState}
	switch countState {
	case "known":
		if !countValue.Valid ||
			countValue.Int64 < 0 ||
			!currentSnapshotID.Valid {
			return catalogValue{},
				nil,
				catalogValue{},
				catalogValue{},
				catalogValue{},
				catalogValue{},
				fmt.Errorf(
					"%w: Work %s has invalid materialized citation count",
					ErrCatalogNotReady,
					workID,
				)
		}
		value := countValue.Int64
		count = &value
		countCatalogValue.Value = value
	case "missing":
		if countValue.Valid || currentSnapshotID.Valid {
			return catalogValue{},
				nil,
				catalogValue{},
				catalogValue{},
				catalogValue{},
				catalogValue{},
				fmt.Errorf(
					"%w: Work %s missing citation count has stored value",
					ErrCatalogNotReady,
					workID,
				)
		}
	default:
		return catalogValue{},
			nil,
			catalogValue{},
			catalogValue{},
			catalogValue{},
			catalogValue{},
			fmt.Errorf(
				"%w: Work %s has invalid citation count state %q",
				ErrCatalogNotReady,
				workID,
				countState,
			)
	}

	var velocityCatalogValue catalogValue
	var velocityEvidence any
	switch velocityState {
	case "known":
		if !velocityValue.Valid ||
			!currentSnapshotID.Valid ||
			!baselineSnapshotID.Valid ||
			workEvidence.ElapsedHours == nil {
			return catalogValue{},
				nil,
				catalogValue{},
				catalogValue{},
				catalogValue{},
				catalogValue{},
				fmt.Errorf(
					"%w: Work %s has incomplete known citation velocity",
					ErrCatalogNotReady,
					workID,
				)
		}
		velocityNumber, err := strictJSONNumber(velocityValue.String)
		if err != nil {
			return catalogValue{},
				nil,
				catalogValue{},
				catalogValue{},
				catalogValue{},
				catalogValue{},
				fmt.Errorf(
					"%w: Work %s has invalid citation velocity: %v",
					ErrCatalogNotReady,
					workID,
					err,
				)
		}
		elapsedHours, err := decimal.NewFromString(
			workEvidence.ElapsedHours.String(),
		)
		if err != nil || !elapsedHours.IsPositive() {
			return catalogValue{},
				nil,
				catalogValue{},
				catalogValue{},
				catalogValue{},
				catalogValue{},
				fmt.Errorf(
					"%w: Work %s has invalid citation velocity elapsed time",
					ErrCatalogNotReady,
					workID,
				)
		}
		elapsedDays := elapsedHours.Div(decimal.NewFromInt(24))
		velocityCatalogValue = catalogValue{
			State: "known",
			Value: velocityNumber,
		}
		velocityEvidence = citationVelocityKnownEvidence{
			State:              "known",
			WindowDays:         windowDays,
			CurrentSnapshotID:  uuid.UUID(currentSnapshotID.Bytes),
			BaselineSnapshotID: uuid.UUID(baselineSnapshotID.Bytes),
			ElapsedDays:        decimalFloat64(elapsedDays),
		}
	case "insufficient_evidence":
		if velocityValue.Valid {
			return catalogValue{},
				nil,
				catalogValue{},
				catalogValue{},
				catalogValue{},
				catalogValue{},
				fmt.Errorf(
					"%w: Work %s insufficient velocity has a numeric value",
					ErrCatalogNotReady,
					workID,
				)
		}
		missing := append([]string(nil), workEvidence.MissingSignals...)
		if len(missing) == 0 {
			missing = []string{"citation_velocity_boundary"}
		}
		reason := "citation velocity requires exact same-source boundary snapshots"
		velocityCatalogValue = catalogValue{
			State:  "insufficient_evidence",
			Reason: reason,
		}
		velocityEvidence = citationVelocityInsufficientEvidence{
			State:      "insufficient_evidence",
			WindowDays: windowDays,
			Reason:     reason,
			Missing:    missing,
		}
	default:
		return catalogValue{},
			nil,
			catalogValue{},
			catalogValue{},
			catalogValue{},
			catalogValue{},
			fmt.Errorf(
				"%w: Work %s has invalid citation velocity state %q",
				ErrCatalogNotReady,
				workID,
				velocityState,
			)
	}

	percentileCatalogValue,
		percentileEvidence,
		hasUniquePercentile,
		err := loadCitationPercentile(
		ctx,
		tx,
		run,
		workID,
	)
	if err != nil {
		return catalogValue{},
			nil,
			catalogValue{},
			catalogValue{},
			catalogValue{},
			catalogValue{},
			err
	}
	analysisEvidence := catalogValue{State: "missing"}
	if hasUniquePercentile {
		analysisEvidence = catalogValue{
			State: "known",
			Value: citationAnalysisEvidencePayload{
				AnalysisRunID:  run.ID,
				Source:         run.Source,
				AsOf:           run.AsOf.Format(time.RFC3339Nano),
				GeneratedAt:    generatedAt.UTC().Format(time.RFC3339Nano),
				FormulaVersion: formulaVersion,
				SourceRevision: sourceRevision,
				Velocity:       velocityEvidence,
				Percentile:     percentileEvidence,
			},
		}
	}
	return countCatalogValue,
		count,
		snapshotsValue,
		velocityCatalogValue,
		percentileCatalogValue,
		analysisEvidence,
		nil
}

func loadCitationSnapshotsByID(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	source string,
	snapshotIDs []uuid.UUID,
) ([]citationSnapshotPayload, error) {
	if len(snapshotIDs) == 0 {
		return []citationSnapshotPayload{}, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT
			id,
			source,
			observed_at,
			count,
			source_record_id,
			ingestion_job_id,
			retrieved_at,
			coverage::double precision,
			definition_version,
			dataset_version
		FROM citation_snapshots
		WHERE work_id = $1
		  AND source = $2
		  AND id = ANY($3::uuid[])
		ORDER BY observed_at, id
	`, workID, source, snapshotIDs)
	if err != nil {
		return nil, fmt.Errorf(
			"query Work %s materialized citation snapshots: %w",
			workID,
			err,
		)
	}
	defer rows.Close()
	snapshots := make([]citationSnapshotPayload, 0, len(snapshotIDs))
	seen := make(map[uuid.UUID]struct{}, len(snapshotIDs))
	for rows.Next() {
		var (
			id                      uuid.UUID
			snapshot                citationSnapshotPayload
			observedAt, retrievedAt time.Time
		)
		if err := rows.Scan(
			&id,
			&snapshot.Source,
			&observedAt,
			&snapshot.Count,
			&snapshot.SourceRecordID,
			&snapshot.IngestionJobID,
			&retrievedAt,
			&snapshot.Coverage,
			&snapshot.DefinitionVersion,
			&snapshot.DatasetVersion,
		); err != nil {
			return nil, fmt.Errorf(
				"scan Work %s materialized citation snapshot: %w",
				workID,
				err,
			)
		}
		if snapshot.Count < 0 ||
			snapshot.Coverage < 0 ||
			snapshot.Coverage > 1 ||
			snapshot.Source != source {
			return nil, fmt.Errorf(
				"%w: Work %s has invalid materialized citation snapshot",
				ErrCatalogNotReady,
				workID,
			)
		}
		seen[id] = struct{}{}
		snapshot.ObservedAt = observedAt.UTC().Format(time.RFC3339Nano)
		snapshot.RetrievedAt = retrievedAt.UTC().Format(time.RFC3339Nano)
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate Work %s materialized citation snapshots: %w",
			workID,
			err,
		)
	}
	if len(seen) != len(snapshotIDs) {
		return nil, fmt.Errorf(
			"%w: Work %s materialized citation evidence references missing snapshots",
			ErrCatalogNotReady,
			workID,
		)
	}
	return snapshots, nil
}

func loadCitationPercentile(
	ctx context.Context,
	tx pgx.Tx,
	run citationAnalysisRun,
	workID uuid.UUID,
) (catalogValue, any, bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			subject_version_id,
			subject_id,
			publication_year,
			publication_type_id,
			citation_snapshot_id,
			citation_count,
			cohort_key,
			cohort_size,
			minimum_cohort_size,
			percentile_state,
			midrank::text,
			citation_percentile::text,
			source_revision,
			formula_version,
			evidence
		FROM citation_analysis_percentiles
		WHERE analysis_run_id = $1
		  AND work_id = $2
		  AND source = $3
		ORDER BY subject_id, publication_type_id
	`, run.ID, workID, run.Source)
	if err != nil {
		return catalogValue{}, nil, false, fmt.Errorf(
			"query Work %s citation percentiles from run %s: %w",
			workID,
			run.ID,
			err,
		)
	}
	defer rows.Close()

	type percentileRow struct {
		subjectVersionID  uuid.UUID
		subjectID         uuid.UUID
		publicationYear   int
		publicationTypeID uuid.UUID
		snapshotID        uuid.UUID
		citationCount     int64
		cohortKey         string
		cohortSize        int
		minimumSize       int
		state             string
		midrank           pgtype.Text
		percentile        pgtype.Text
		sourceRevision    string
		formulaVersion    string
		supportingWorkIDs []uuid.UUID
	}
	values := make([]percentileRow, 0, 2)
	for rows.Next() {
		var (
			value       percentileRow
			rawEvidence []byte
		)
		if err := rows.Scan(
			&value.subjectVersionID,
			&value.subjectID,
			&value.publicationYear,
			&value.publicationTypeID,
			&value.snapshotID,
			&value.citationCount,
			&value.cohortKey,
			&value.cohortSize,
			&value.minimumSize,
			&value.state,
			&value.midrank,
			&value.percentile,
			&value.sourceRevision,
			&value.formulaVersion,
			&rawEvidence,
		); err != nil {
			return catalogValue{}, nil, false, fmt.Errorf(
				"scan Work %s citation percentile: %w",
				workID,
				err,
			)
		}
		var evidence struct {
			Cohort             json.RawMessage `json:"cohort"`
			CohortKey          string          `json:"cohort_key"`
			MinimumCohortSize  int             `json:"minimum_cohort_size"`
			SupportingWorkIDs  []uuid.UUID     `json:"supporting_work_ids"`
			CitationSnapshotID uuid.UUID       `json:"citation_snapshot_id"`
			Scope              json.RawMessage `json:"scope"`
		}
		if err := decodeStrictJSONObject(rawEvidence, &evidence); err != nil {
			return catalogValue{}, nil, false, fmt.Errorf(
				"%w: decode Work %s citation percentile evidence: %v",
				ErrCatalogNotReady,
				workID,
				err,
			)
		}
		if value.subjectVersionID == uuid.Nil ||
			value.subjectID == uuid.Nil ||
			value.publicationTypeID == uuid.Nil ||
			value.snapshotID == uuid.Nil ||
			value.publicationYear < 1900 ||
			value.publicationYear > 3000 ||
			value.citationCount < 0 ||
			value.cohortKey == "" ||
			value.cohortSize < 1 ||
			value.minimumSize != run.MinimumCohortSize ||
			value.formulaVersion != run.FormulaVersion ||
			!regexp.MustCompile(`^[0-9a-f]{64}$`).
				MatchString(value.sourceRevision) ||
			evidence.CohortKey != value.cohortKey ||
			evidence.MinimumCohortSize != value.minimumSize ||
			evidence.CitationSnapshotID != value.snapshotID ||
			len(evidence.SupportingWorkIDs) != value.cohortSize ||
			len(evidence.Cohort) == 0 ||
			len(evidence.Scope) == 0 {
			return catalogValue{}, nil, false, fmt.Errorf(
				"%w: Work %s has invalid materialized citation percentile evidence",
				ErrCatalogNotReady,
				workID,
			)
		}
		value.supportingWorkIDs = evidence.SupportingWorkIDs
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return catalogValue{}, nil, false, fmt.Errorf(
			"iterate Work %s citation percentiles: %w",
			workID,
			err,
		)
	}
	if len(values) == 0 {
		return catalogValue{State: "missing"}, nil, false, nil
	}
	if len(values) > 1 {
		return catalogValue{State: "unknown"}, nil, false, nil
	}

	value := values[0]
	switch value.state {
	case "known":
		if !value.midrank.Valid || !value.percentile.Valid {
			return catalogValue{}, nil, false, fmt.Errorf(
				"%w: Work %s known citation percentile is incomplete",
				ErrCatalogNotReady,
				workID,
			)
		}
		midrank, err := strictJSONNumber(value.midrank.String)
		if err != nil {
			return catalogValue{}, nil, false, fmt.Errorf(
				"%w: Work %s has invalid percentile midrank",
				ErrCatalogNotReady,
				workID,
			)
		}
		percentile, err := strictJSONNumber(value.percentile.String)
		if err != nil {
			return catalogValue{}, nil, false, fmt.Errorf(
				"%w: Work %s has invalid citation percentile",
				ErrCatalogNotReady,
				workID,
			)
		}
		return catalogValue{
				State: "known",
				Value: percentile,
			},
			citationPercentileKnownEvidence{
				State:              "known",
				CitationSnapshotID: value.snapshotID,
				SubjectVersionID:   value.subjectVersionID,
				SubjectID:          value.subjectID,
				PublicationYear:    value.publicationYear,
				PublicationTypeID:  value.publicationTypeID,
				CohortKey:          value.cohortKey,
				CohortSize:         value.cohortSize,
				MinimumCohortSize:  value.minimumSize,
				Midrank:            midrank,
				SupportingWorkIDs:  value.supportingWorkIDs,
			},
			true,
			nil
	case "insufficient_evidence":
		if value.midrank.Valid || value.percentile.Valid {
			return catalogValue{}, nil, false, fmt.Errorf(
				"%w: Work %s insufficient percentile has numeric values",
				ErrCatalogNotReady,
				workID,
			)
		}
		reason := fmt.Sprintf(
			"citation percentile cohort size %d is below minimum %d",
			value.cohortSize,
			value.minimumSize,
		)
		return catalogValue{
				State:  "insufficient_evidence",
				Reason: reason,
			},
			citationPercentileInsufficientEvidence{
				State:              "insufficient_evidence",
				CitationSnapshotID: value.snapshotID,
				SubjectVersionID:   value.subjectVersionID,
				SubjectID:          value.subjectID,
				PublicationYear:    value.publicationYear,
				PublicationTypeID:  value.publicationTypeID,
				CohortKey:          value.cohortKey,
				CohortSize:         value.cohortSize,
				MinimumCohortSize:  value.minimumSize,
				Reason:             reason,
				SupportingWorkIDs:  value.supportingWorkIDs,
			},
			true,
			nil
	default:
		return catalogValue{}, nil, false, fmt.Errorf(
			"%w: Work %s has invalid citation percentile state %q",
			ErrCatalogNotReady,
			workID,
			value.state,
		)
	}
}

func decodeStrictJSONObject(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON value has trailing content")
		}
		return err
	}
	return nil
}

func strictJSONNumber(value string) (json.Number, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", errors.New("number must be non-empty and trimmed")
	}
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return "", err
	}
	return json.Number(parsed.String()), nil
}

func decimalFloat64(value decimal.Decimal) float64 {
	return value.InexactFloat64()
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

func validateCurationReferences(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
) error {
	var receiptExists, subjectVersionExists bool
	if err := tx.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1
				FROM jcr_import_receipts
				WHERE id = $1
			),
			EXISTS (
				SELECT 1
				FROM subject_versions
				WHERE version_key = $2
			)
	`,
		input.JCRImportReceipt,
		input.SubjectVersion,
	).Scan(&receiptExists, &subjectVersionExists); err != nil {
		return fmt.Errorf("validate Catalog curation references: %w", err)
	}
	if !receiptExists {
		return fmt.Errorf(
			"%w: JCR import receipt %s does not exist",
			ErrCatalogNotReady,
			input.JCRImportReceipt,
		)
	}
	if !subjectVersionExists {
		return fmt.Errorf(
			"%w: Subject version %q does not exist",
			ErrCatalogNotReady,
			input.SubjectVersion,
		)
	}
	return nil
}

func loadAcceptedCuration(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	input PublishInput,
) (curationRevisionFact, bool, error) {
	var (
		fact                        curationRevisionFact
		decision                    string
		matchedRules                []byte
		evidence                    []byte
		metricYear                  int
		policyName                  string
		policyVersion               int
		assessedAt                  time.Time
		eligibilityID               uuid.UUID
		eligibilityPolicyVersion    string
		eligibilityDecision         string
		eligibilityEvidence         []byte
		eligibilityAssessedAt       time.Time
		eligibilitySubjectVersionID uuid.UUID
		subjectVersionKey           string
	)
	err := tx.QueryRow(ctx, `
		SELECT
			work.id,
			venue.id,
			assessment.decision,
			assessment.matched_rules,
			assessment.evidence,
			assessment.metric_year,
			policy.policy_name,
			policy.version_number,
			assessment.assessed_at,
			eligibility.id,
			eligibility.policy_version,
			eligibility.decision,
			eligibility.evidence,
			eligibility.assessed_at,
			subject_version.id,
			subject_version.version_key
		FROM works AS work
		JOIN venues AS venue
		  ON venue.id = work.venue_id
		JOIN venue_policy_versions AS policy
		  ON policy.policy_name = $2
		 AND policy.version_number = $3
		JOIN venue_policy_assessments AS assessment
		  ON assessment.venue_id = venue.id
		 AND assessment.policy_version_id = policy.id
		 AND assessment.metric_year = $4
		JOIN subject_versions AS subject_version
		  ON subject_version.version_key = $6
		JOIN biomedical_publication_eligibility_decisions AS eligibility
		  ON eligibility.work_id = work.id
		 AND eligibility.policy_version = $7
		 AND eligibility.metric_year = $4
		 AND eligibility.subject_version_id = subject_version.id
		 AND eligibility.decision = 'accepted'
		 AND eligibility.venue_id = venue.id
		WHERE work.id = $1
		  AND work.status = 'active'
		  AND venue.venue_type = 'journal'
		  AND COALESCE(venue.issn_l, venue.issn, venue.eissn) IS NOT NULL
		  AND assessment.decision = 'accepted'
		  AND assessment.evidence ->> 'jcr_import_receipt_id' = ($5::uuid)::text
		  AND EXISTS (
				SELECT 1
				FROM jcr_import_receipt_metrics AS receipt_metric
				JOIN venue_metric_snapshots AS metric
				  ON metric.id = receipt_metric.metric_snapshot_id
				JOIN journal_subject_metrics AS journal_subject
				  ON journal_subject.venue_metric_snapshot_id = metric.id
				JOIN biomedical_subject_rules AS subject_rule
				  ON subject_rule.id = journal_subject.subject_rule_id
				WHERE receipt_metric.import_receipt_id = $5::uuid
				  AND journal_subject.id =
				      eligibility.journal_subject_metric_id
				  AND metric.venue_id = venue.id
				  AND metric.metric_year = $4
				  AND subject_rule.subject_version_id =
				      subject_version.id
				  AND journal_subject.jcr_category = metric.category
				  AND subject_rule.jcr_category = metric.category
		  )
	`,
		workID,
		input.VenuePolicyName,
		input.VenuePolicyVersion,
		input.JCRMetricYear,
		input.JCRImportReceipt,
		input.SubjectVersion,
		input.EligibilityPolicyVersion,
	).Scan(
		&fact.WorkID,
		&fact.VenueID,
		&decision,
		&matchedRules,
		&evidence,
		&metricYear,
		&policyName,
		&policyVersion,
		&assessedAt,
		&eligibilityID,
		&eligibilityPolicyVersion,
		&eligibilityDecision,
		&eligibilityEvidence,
		&eligibilityAssessedAt,
		&eligibilitySubjectVersionID,
		&subjectVersionKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return curationRevisionFact{}, false, nil
	}
	if err != nil {
		return curationRevisionFact{}, false, fmt.Errorf(
			"query Work %s accepted curation: %w",
			workID,
			err,
		)
	}
	fact.Curation = curationPayload{
		Decision:      decision,
		MatchedRules:  json.RawMessage(matchedRules),
		Evidence:      json.RawMessage(evidence),
		MetricYear:    metricYear,
		PolicyName:    policyName,
		PolicyVersion: policyVersion,
		AssessedAt:    assessedAt.UTC().Format(time.RFC3339Nano),
	}
	fact.Eligibility = biomedicalEligibilityRevision{
		ID:                eligibilityID,
		PolicyVersion:     eligibilityPolicyVersion,
		MetricYear:        input.JCRMetricYear,
		SubjectVersionID:  eligibilitySubjectVersionID,
		SubjectVersionKey: subjectVersionKey,
		Decision:          eligibilityDecision,
		Evidence:          json.RawMessage(eligibilityEvidence),
		AssessedAt:        eligibilityAssessedAt.UTC().Format(time.RFC3339Nano),
	}
	if err := validateExactJCRAdmission(
		ctx,
		tx,
		input,
		fact.VenueID,
	); err != nil {
		return curationRevisionFact{}, false, err
	}
	if err := validateExactJCRAssessmentEvidence(
		ctx,
		tx,
		input,
		fact.VenueID,
		fact.Curation,
	); err != nil {
		return curationRevisionFact{}, false, err
	}
	return fact, true, nil
}

func validateExactJCRAdmission(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
	venueID uuid.UUID,
) error {
	var matchedQ1 bool
	if err := tx.QueryRow(ctx, `
		SELECT
			COALESCE(bool_or(
				metric.metric_status = 'known'
				AND metric.registry_version = 'jcr-registry/v2'
				AND metric.quartile = 'Q1'
			), false)
		FROM jcr_import_receipt_metrics AS receipt_metric
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = receipt_metric.metric_snapshot_id
		WHERE receipt_metric.import_receipt_id = $1
		  AND metric.venue_id = $2
		  AND metric.metric_year = $3
	`,
		input.JCRImportReceipt,
		venueID,
		input.JCRMetricYear,
	).Scan(&matchedQ1); err != nil {
		return fmt.Errorf(
			"validate Journal %s exact JCR admission evidence: %w",
			venueID,
			err,
		)
	}
	if !matchedQ1 {
		return fmt.Errorf(
			"%w: Journal %s does not satisfy JCR Q1 in receipt %s for metric year %d",
			ErrCatalogNotReady,
			venueID,
			input.JCRImportReceipt,
			input.JCRMetricYear,
		)
	}
	return nil
}

func validateExactJCRAssessmentEvidence(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
	venueID uuid.UUID,
	curation curationPayload,
) error {
	rows, err := tx.Query(ctx, `
		SELECT
			metric.category,
			metric.registry_version,
			metric.edition_year,
			metric.jif::text,
			metric.jif_rank,
			metric.category_journal_count,
			metric.jif_percentile::text,
			COALESCE(metric.quartile, ''),
			metric.metric_status,
			metric.source_name,
			metric.metric_status = 'known'
				AND metric.registry_version = 'jcr-registry/v2'
				AND metric.quartile = 'Q1'
		FROM jcr_import_receipt_metrics AS receipt_metric
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = receipt_metric.metric_snapshot_id
		WHERE receipt_metric.import_receipt_id = $1
		  AND metric.venue_id = $2
		  AND metric.metric_year = $3
		ORDER BY metric.category
	`, input.JCRImportReceipt, venueID, input.JCRMetricYear)
	if err != nil {
		return fmt.Errorf(
			"query Journal %s exact JCR assessment evidence: %w",
			venueID,
			err,
		)
	}
	defer rows.Close()

	expectedCategories := make([]jcrAssessmentCategoryEvidence, 0)
	matchedQ1 := false
	for rows.Next() {
		var (
			category     jcrAssessmentCategoryEvidence
			rowMatchedQ1 bool
		)
		if err := rows.Scan(
			&category.Category,
			&category.RegistryVersion,
			&category.EditionYear,
			&category.JIF,
			&category.JIFRank,
			&category.CategoryJournalCount,
			&category.JIFPercentile,
			&category.Quartile,
			&category.Status,
			&category.SourceName,
			&rowMatchedQ1,
		); err != nil {
			return fmt.Errorf(
				"scan Journal %s exact JCR assessment evidence: %w",
				venueID,
				err,
			)
		}
		expectedCategories = append(expectedCategories, category)
		matchedQ1 = matchedQ1 || rowMatchedQ1
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf(
			"iterate Journal %s exact JCR assessment evidence: %w",
			venueID,
			err,
		)
	}

	expectedRules := make([]string, 0, 1)
	if matchedQ1 {
		expectedRules = append(expectedRules, "jcr_q1")
	}
	var actualRules []string
	if err := json.Unmarshal(curation.MatchedRules, &actualRules); err != nil {
		return fmt.Errorf(
			"%w: Journal %s has invalid accepted assessment matched rules: %v",
			ErrCatalogNotReady,
			venueID,
			err,
		)
	}
	if !equalStrings(actualRules, expectedRules) {
		return fmt.Errorf(
			"%w: Journal %s assessment matched rules do not match exact JCR receipt",
			ErrCatalogNotReady,
			venueID,
		)
	}

	var actual jcrAssessmentEvidence
	if err := json.Unmarshal(curation.Evidence, &actual); err != nil {
		return fmt.Errorf(
			"%w: Journal %s has invalid accepted assessment evidence: %v",
			ErrCatalogNotReady,
			venueID,
			err,
		)
	}
	expectedPolicyVersion := fmt.Sprintf(
		"%s/v%d",
		input.VenuePolicyName,
		input.VenuePolicyVersion,
	)
	if actual.JCRImportReceiptID != input.JCRImportReceipt.String() ||
		actual.PolicyVersion != expectedPolicyVersion ||
		actual.MetricYear != input.JCRMetricYear ||
		actual.VenueType != "journal" ||
		actual.Reason != "" ||
		!equalJCRAssessmentCategories(actual.Categories, expectedCategories) {
		return fmt.Errorf(
			"%w: Journal %s assessment evidence does not match exact JCR receipt",
			ErrCatalogNotReady,
			venueID,
		)
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalJCRAssessmentCategories(
	left, right []jcrAssessmentCategoryEvidence,
) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Category != right[index].Category ||
			left[index].RegistryVersion != right[index].RegistryVersion ||
			!equalOptionalInt(left[index].EditionYear, right[index].EditionYear) ||
			left[index].Quartile != right[index].Quartile ||
			left[index].Status != right[index].Status ||
			left[index].SourceName != right[index].SourceName ||
			!equalOptionalString(left[index].JIF, right[index].JIF) ||
			!equalOptionalInt(left[index].JIFRank, right[index].JIFRank) ||
			!equalOptionalInt(
				left[index].CategoryJournalCount,
				right[index].CategoryJournalCount,
			) ||
			!equalOptionalString(
				left[index].JIFPercentile,
				right[index].JIFPercentile,
			) {
			return false
		}
	}
	return true
}

func equalOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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

func snapshotRevision(input PublishInput, snapshot catalogSnapshot) (string, error) {
	material := struct {
		FormulaVersion           string                         `json:"formula_version"`
		JCRMetricYear            int                            `json:"jcr_metric_year"`
		VenuePolicyName          string                         `json:"venue_policy_name"`
		VenuePolicyVersion       int                            `json:"venue_policy_version"`
		EligibilityPolicyVersion string                         `json:"eligibility_policy_version"`
		SubjectVersion           string                         `json:"subject_version"`
		JCRImportReceipt         uuid.UUID                      `json:"jcr_import_receipt"`
		CitationSource           string                         `json:"citation_source"`
		CitationAnalysisRunID    uuid.UUID                      `json:"citation_analysis_run_id"`
		TrendAnalysisRunID       uuid.UUID                      `json:"trend_analysis_run_id"`
		JournalAnalysisRunID     uuid.UUID                      `json:"journal_analysis_run_id"`
		OpportunityAnalysisRunID uuid.UUID                      `json:"opportunity_analysis_run_id"`
		Sources                  []sourceRevisionFact           `json:"sources"`
		Curations                []curationRevisionFact         `json:"curations"`
		Papers                   []publishedPaper               `json:"papers"`
		Topics                   []publishedTaxonomy            `json:"topics"`
		Methods                  []publishedTaxonomy            `json:"methods"`
		Home                     json.RawMessage                `json:"home"`
		SubjectListMetadata      json.RawMessage                `json:"subject_list_metadata"`
		JournalListMetadata      json.RawMessage                `json:"journal_list_metadata"`
		Subjects                 []publishedBiomedicalResource  `json:"subjects"`
		Journals                 []publishedBiomedicalResource  `json:"journals"`
		Trends                   []publishedTrend               `json:"trends"`
		Opportunities            []publishedResearchOpportunity `json:"opportunities"`
	}{
		FormulaVersion:           input.FormulaVersion,
		JCRMetricYear:            input.JCRMetricYear,
		VenuePolicyName:          input.VenuePolicyName,
		VenuePolicyVersion:       input.VenuePolicyVersion,
		EligibilityPolicyVersion: input.EligibilityPolicyVersion,
		SubjectVersion:           input.SubjectVersion,
		JCRImportReceipt:         input.JCRImportReceipt,
		CitationSource:           input.CitationSource,
		CitationAnalysisRunID:    input.CitationAnalysisRunID,
		TrendAnalysisRunID:       input.TrendAnalysisRunID,
		JournalAnalysisRunID:     input.JournalAnalysisRunID,
		OpportunityAnalysisRunID: input.OpportunityAnalysisRunID,
		Sources:                  snapshot.sources,
		Curations:                snapshot.curations,
		Papers:                   snapshot.papers,
		Topics:                   snapshot.topics,
		Methods:                  snapshot.methods,
		Home:                     snapshot.home,
		SubjectListMetadata:      snapshot.subjectListMetadata,
		JournalListMetadata:      snapshot.journalListMetadata,
		Subjects:                 snapshot.subjects,
		Journals:                 snapshot.journals,
		Trends:                   snapshot.trends,
		Opportunities:            snapshot.opportunities,
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
		"papers":                      len(snapshot.papers),
		"topics":                      len(snapshot.topics),
		"methods":                     len(snapshot.methods),
		"subjects":                    len(snapshot.subjects),
		"journals":                    len(snapshot.journals),
		"trends":                      len(snapshot.trends),
		"opportunities":               len(snapshot.opportunities),
		"jcr_metric_year":             input.JCRMetricYear,
		"venue_policy_name":           input.VenuePolicyName,
		"venue_policy_version":        input.VenuePolicyVersion,
		"eligibility_policy_version":  input.EligibilityPolicyVersion,
		"subject_version":             input.SubjectVersion,
		"jcr_import_receipt":          input.JCRImportReceipt,
		"citation_source":             input.CitationSource,
		"citation_analysis_run_id":    input.CitationAnalysisRunID,
		"trend_analysis_run_id":       input.TrendAnalysisRunID,
		"journal_analysis_run_id":     input.JournalAnalysisRunID,
		"opportunity_analysis_run_id": input.OpportunityAnalysisRunID,
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
	home, err := bindCatalogGeneration(snapshot.home, generation.ID)
	if err != nil {
		return Generation{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_home (generation_id, payload)
		VALUES ($1, $2)
	`, generation.ID, home); err != nil {
		return Generation{}, fmt.Errorf("insert public catalog Home snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_biomedical_manifest (
			generation_id,
			subject_count,
			subject_list_payload,
			journal_count,
			journal_list_payload
		) VALUES ($1, $2, $3, $4, $5)
	`,
		generation.ID,
		len(snapshot.subjects),
		snapshot.subjectListMetadata,
		len(snapshot.journals),
		snapshot.journalListMetadata,
	); err != nil {
		return Generation{}, fmt.Errorf(
			"insert public catalog biomedical manifest: %w",
			err,
		)
	}
	for _, subject := range snapshot.subjects {
		if err := insertBiomedicalResource(
			ctx,
			tx,
			"public_catalog_subjects",
			"subject_id",
			generation.ID,
			subject,
		); err != nil {
			return Generation{}, err
		}
	}
	for _, journal := range snapshot.journals {
		if err := insertBiomedicalResource(
			ctx,
			tx,
			"public_catalog_journals",
			"journal_id",
			generation.ID,
			journal,
		); err != nil {
			return Generation{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_biomedical_coverage (generation_id)
		VALUES ($1)
	`, generation.ID); err != nil {
		return Generation{}, fmt.Errorf(
			"insert public catalog biomedical coverage marker: %w",
			err,
		)
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
	for _, trend := range snapshot.trends {
		if _, err := tx.Exec(ctx, `
			INSERT INTO public_catalog_trends (
				generation_id,
				trend_kind,
				window_days,
				rank,
				subject_id,
				score,
				payload
			) VALUES ($1, $2, $3, $4, $5, $6::numeric, $7)
		`,
			generation.ID,
			trend.Kind,
			trend.WindowDays,
			trend.Rank,
			trend.SubjectID,
			trend.Score,
			trend.Payload,
		); err != nil {
			return Generation{}, fmt.Errorf(
				"insert public catalog %s trend rank %d: %w",
				trend.Kind,
				trend.Rank,
				err,
			)
		}
	}
	for _, opportunity := range snapshot.opportunities {
		if _, err := tx.Exec(ctx, `
			INSERT INTO public_catalog_research_opportunities (
				generation_id,
				opportunity_id,
				status,
				ordinal,
				payload
			) VALUES ($1, $2, $3, $4, $5)
		`,
			generation.ID,
			opportunity.ID,
			opportunity.Status,
			opportunity.Ordinal,
			opportunity.Payload,
		); err != nil {
			return Generation{}, fmt.Errorf(
				"insert public catalog research opportunity %s: %w",
				opportunity.ID,
				err,
			)
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

func insertBiomedicalResource(
	ctx context.Context,
	tx pgx.Tx,
	table string,
	idColumn string,
	generationID uuid.UUID,
	resource publishedBiomedicalResource,
) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (
			generation_id,
			%s,
			slug,
			paper_count,
			summary_payload,
			detail_payload
		) VALUES ($1, $2, $3, $4, $5, $6)
	`, table, idColumn)
	if _, err := tx.Exec(
		ctx,
		query,
		generationID,
		resource.ID,
		resource.Slug,
		resource.PaperCount,
		resource.SummaryPayload,
		resource.DetailPayload,
	); err != nil {
		return fmt.Errorf(
			"insert %s biomedical resource %s: %w",
			table,
			resource.ID,
			err,
		)
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

func copyOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
