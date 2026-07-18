package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/abstractanalysis"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/analysis"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/catalog"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/citation"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ingestion"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/openairesponses"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/crossref"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/openalex"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
)

type commandKind string

const (
	commandSyncOpenAlex                commandKind = "sync_openalex"
	commandSyncPubMed                  commandKind = "sync_pubmed"
	commandSyncCrossrefCreated         commandKind = "sync_crossref_created"
	commandSyncCrossrefUpdated         commandKind = "sync_crossref_updated"
	commandImportJCR                   commandKind = "import_jcr"
	commandImportSubjects              commandKind = "import_subjects"
	commandAssessVenues                commandKind = "assess_venues"
	commandAssessBiomedicalEligibility commandKind = "assess_biomedical_eligibility"
	commandAnalyzeCitations            commandKind = "analyze_citations"
	commandAnalyzeAbstractRoutes       commandKind = "analyze_abstract_routes"
	commandAnalyzeTrends               commandKind = "analyze_trends"
	commandAnalyzeJournals             commandKind = "analyze_journals"
	commandAnalyzeOpportunities        commandKind = "analyze_opportunities"
	commandPublishCatalog              commandKind = "publish_catalog"
	maxSyncResults                                 = 1000
	abstractAnalysisRequestTimeout                 = 2 * time.Minute
)

type workerCommand struct {
	Kind                           commandKind
	Query                          string
	Filter                         string
	FromDate                       time.Time
	ToDate                         time.Time
	ISSNs                          []string
	MaxResults                     int
	File                           string
	FormulaVersion                 string
	GeneratedAt                    time.Time
	MetricYear                     int
	PolicyVersion                  string
	AssessedAt                     time.Time
	JCRReceipt                     string
	VenuePolicyName                string
	VenuePolicyVersion             int
	EligibilityPolicyVersion       string
	SubjectVersion                 string
	CitationSource                 string
	CitationAnalysisRunID          string
	TrendAnalysisRunID             string
	JournalAnalysisRunID           string
	OpportunityAnalysisRunID       string
	AsOf                           time.Time
	VelocityWindowDays             int
	MinimumCohortSize              int
	RecentWindowDays               int
	BaselineWindowDays             int
	WindowDays                     int
	MinimumPaperCount              int
	MinimumIndependentJournalCount int
	MinimumIndependentTeamCount    int
	TrendModelSelectionRule        string
	TrendDispersionThreshold       float64
	MinimumSupportCount            int
	MinimumFieldBaselineCount      int
	RuleSetVersion                 string
	PromptVersion                  string
	SchemaVersion                  string
	AnalysisCutoff                 time.Time
	Limit                          int
}

type commandRunner func(
	context.Context,
	config.Config,
	workerCommand,
) (map[string]any, error)

type connectorRunStore interface {
	Start(context.Context, ingestion.ConnectorClaim) (ingestion.ConnectorRun, error)
	RecordPage(
		context.Context,
		ingestion.ConnectorRun,
		ingestion.ConnectorPageReceipt,
	) error
	Succeed(context.Context, ingestion.ConnectorRun) error
	Fail(context.Context, ingestion.ConnectorRun) error
}

type crossrefFetchFunc func(
	context.Context,
	crossref.Query,
	crossref.PageReceiptRecorder,
) source.ClientSequence

type ingestionRunFunc func(
	context.Context,
	ingestion.Job,
	ingestion.EventSequence,
) (ingestion.JobSummary, error)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	os.Exit(realMain(
		ctx,
		os.Args[1:],
		os.Stdout,
		os.Stderr,
		os.LookupEnv,
		runCommand,
	))
}

func realMain(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookup config.LookupEnv,
	runner commandRunner,
) int {
	command, role, err := parseWorkerCommand(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg, err := config.LoadFrom(role, lookup)
	if err != nil {
		fmt.Fprintf(stderr, "load worker configuration: %v\n", err)
		return 1
	}
	result, err := runner(ctx, cfg, command)
	if err != nil {
		fmt.Fprintf(stderr, "run worker command: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(stderr, "encode worker result: %v\n", err)
		return 1
	}
	return 0
}

func parseWorkerCommand(args []string) (workerCommand, config.Role, error) {
	if len(args) < 2 {
		return workerCommand{}, "", errors.New(
			"usage: paper-hub-worker <sync|import|assess|analyze|publish> <source> [flags]",
		)
	}
	if args[0] == "analyze" {
		switch args[1] {
		case "abstract-routes":
			command, err := parseAbstractAnalysisCommand(args[2:])
			return command, config.RoleAbstractAnalysis, err
		case "citations":
			command, err := parseCitationAnalysisCommand(args[2:])
			return command, config.RoleCitationAnalysis, err
		case "trends":
			command, err := parseTrendAnalysisCommand(args[2:])
			return command, config.RoleBiomedicalAnalysis, err
		case "journals":
			command, err := parseJournalAnalysisCommand(args[2:])
			return command, config.RoleBiomedicalAnalysis, err
		case "opportunities":
			command, err := parseOpportunityAnalysisCommand(args[2:])
			return command, config.RoleBiomedicalAnalysis, err
		default:
			return workerCommand{}, "", fmt.Errorf(
				"unsupported analyze target %q; expected abstract-routes, citations, trends, journals, or opportunities",
				args[1],
			)
		}
	}
	if args[0] == "assess" {
		switch args[1] {
		case "venues":
			command, err := parseVenueAssessmentCommand(args[2:])
			return command, config.RoleVenueAssessment, err
		case "biomedical-eligibility":
			command, err := parseBiomedicalEligibilityCommand(args[2:])
			return command, config.RoleVenueAssessment, err
		default:
			return workerCommand{}, "", fmt.Errorf(
				"unsupported assess target %q; expected venues or biomedical-eligibility",
				args[1],
			)
		}
	}
	if args[0] == "publish" {
		if args[1] != "catalog" {
			return workerCommand{}, "", fmt.Errorf(
				"unsupported publish target %q; expected catalog",
				args[1],
			)
		}
		command, err := parseCatalogPublishCommand(args[2:])
		return command, config.RoleCatalogPublish, err
	}
	if args[0] == "import" {
		switch args[1] {
		case "jcr":
			command, err := parseJCRImportCommand(args[2:])
			return command, config.RoleJCRImport, err
		case "subjects":
			command, err := parseSubjectImportCommand(args[2:])
			return command, config.RoleMigrate, err
		default:
			return workerCommand{}, "", fmt.Errorf(
				"unsupported import source %q; expected jcr or subjects",
				args[1],
			)
		}
	}
	if args[0] != "sync" {
		return workerCommand{}, "", errors.New(
			"usage: paper-hub-worker <sync|import|assess|analyze|publish> <source> [flags]",
		)
	}
	switch args[1] {
	case "openalex":
		command, err := parseOpenAlexCommand(args[2:])
		return command, config.RoleOpenAlexSync, err
	case "pubmed":
		command, err := parsePubMedCommand(args[2:])
		return command, config.RolePubMedSync, err
	case "crossref-created":
		command, err := parseCrossrefCommand(
			commandSyncCrossrefCreated,
			args[2:],
		)
		return command, config.RoleCrossrefSync, err
	case "crossref-updated":
		command, err := parseCrossrefCommand(
			commandSyncCrossrefUpdated,
			args[2:],
		)
		return command, config.RoleCrossrefSync, err
	default:
		return workerCommand{}, "", fmt.Errorf(
			"unsupported sync source %q; expected openalex, pubmed, crossref-created, or crossref-updated",
			args[1],
		)
	}
}

func parseAbstractAnalysisCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("analyze abstract-routes", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	var cutoff string
	set.StringVar(
		&command.PromptVersion,
		"prompt-version",
		"",
		"frozen abstract analysis prompt version",
	)
	set.StringVar(
		&command.SchemaVersion,
		"schema-version",
		"",
		"frozen abstract analysis schema version",
	)
	set.StringVar(
		&cutoff,
		"analysis-cutoff",
		"",
		"explicit RFC3339Nano source revision cutoff",
	)
	set.IntVar(
		&command.Limit,
		"limit",
		0,
		"maximum exact Work revisions to analyze",
	)
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf(
			"parse abstract route analysis flags: %w",
			err,
		)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected abstract route analysis arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}

	command.Kind = commandAnalyzeAbstractRoutes
	if command.PromptVersion == "" {
		return workerCommand{}, errors.New(
			"abstract route analysis requires an explicit --prompt-version",
		)
	}
	if command.PromptVersion != strings.TrimSpace(command.PromptVersion) {
		return workerCommand{}, errors.New(
			"abstract route analysis prompt-version must be trimmed",
		)
	}
	if command.PromptVersion != abstractanalysis.PromptVersion {
		return workerCommand{}, fmt.Errorf(
			"abstract route analysis --prompt-version must equal %s",
			abstractanalysis.PromptVersion,
		)
	}
	if command.SchemaVersion == "" {
		return workerCommand{}, errors.New(
			"abstract route analysis requires an explicit --schema-version",
		)
	}
	if command.SchemaVersion != strings.TrimSpace(command.SchemaVersion) {
		return workerCommand{}, errors.New(
			"abstract route analysis schema-version must be trimmed",
		)
	}
	if command.SchemaVersion != abstractanalysis.SchemaVersion {
		return workerCommand{}, fmt.Errorf(
			"abstract route analysis --schema-version must equal %s",
			abstractanalysis.SchemaVersion,
		)
	}
	if cutoff == "" {
		return workerCommand{}, errors.New(
			"abstract route analysis requires an explicit --analysis-cutoff",
		)
	}
	if cutoff != strings.TrimSpace(cutoff) {
		return workerCommand{}, errors.New(
			"abstract route analysis cutoff must be trimmed",
		)
	}
	parsedCutoff, err := time.Parse(time.RFC3339Nano, cutoff)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"abstract route analysis cutoff must use RFC3339Nano: %w",
			err,
		)
	}
	if parsedCutoff.IsZero() {
		return workerCommand{}, errors.New(
			"abstract route analysis cutoff must be non-zero",
		)
	}
	command.AnalysisCutoff = parsedCutoff.UTC()
	if command.Limit < 1 || command.Limit > 1000 {
		return workerCommand{}, errors.New(
			"abstract route analysis limit must be between 1 and 1000",
		)
	}
	return command, nil
}

func parseCitationAnalysisCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("analyze citations", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	var asOf string
	set.StringVar(
		&asOf,
		"as-of",
		"",
		"explicit RFC3339Nano analysis boundary",
	)
	set.StringVar(
		&command.CitationSource,
		"source",
		"",
		"exact citation evidence source",
	)
	set.IntVar(
		&command.VelocityWindowDays,
		"velocity-window-days",
		0,
		"citation velocity boundary window in days",
	)
	set.IntVar(
		&command.MinimumCohortSize,
		"minimum-cohort-size",
		0,
		"minimum exact cohort size for percentile publication",
	)
	set.StringVar(
		&command.FormulaVersion,
		"formula-version",
		"",
		"deterministic citation analysis formula version",
	)
	set.StringVar(
		&command.SubjectVersion,
		"subject-version",
		"",
		"exact biomedical Subject registry version",
	)
	set.StringVar(
		&command.EligibilityPolicyVersion,
		"eligibility-policy-version",
		"",
		"biomedical public eligibility policy version",
	)
	set.IntVar(
		&command.MetricYear,
		"jcr-metric-year",
		0,
		"authorized JCR metric year",
	)
	set.StringVar(
		&command.JCRReceipt,
		"jcr-import-receipt",
		"",
		"authorized JCR import receipt UUID",
	)
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf(
			"parse citation analysis flags: %w",
			err,
		)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected citation analysis arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}

	command.Kind = commandAnalyzeCitations
	if asOf == "" {
		return workerCommand{}, errors.New(
			"citation analysis requires an explicit --as-of",
		)
	}
	if asOf != strings.TrimSpace(asOf) {
		return workerCommand{}, errors.New(
			"citation analysis as-of must be trimmed",
		)
	}
	parsedAsOf, err := time.Parse(time.RFC3339Nano, asOf)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"citation analysis as-of must use RFC3339Nano: %w",
			err,
		)
	}
	command.AsOf = parsedAsOf.UTC()
	if command.CitationSource == "" {
		return workerCommand{}, errors.New(
			"citation analysis requires an explicit --source",
		)
	}
	if command.CitationSource != strings.TrimSpace(command.CitationSource) {
		return workerCommand{}, errors.New(
			"citation analysis source must be trimmed",
		)
	}
	if command.VelocityWindowDays < 1 ||
		command.VelocityWindowDays > 3650 {
		return workerCommand{}, errors.New(
			"citation analysis --velocity-window-days must be between 1 and 3650",
		)
	}
	if command.MinimumCohortSize < 2 ||
		command.MinimumCohortSize > 1_000_000 {
		return workerCommand{}, errors.New(
			"citation analysis --minimum-cohort-size must be between 2 and 1000000",
		)
	}
	if command.FormulaVersion !=
		citation.CitationIntelligenceFormulaVersion {
		return workerCommand{}, fmt.Errorf(
			"citation analysis --formula-version must equal %s",
			citation.CitationIntelligenceFormulaVersion,
		)
	}
	if command.SubjectVersion == "" {
		return workerCommand{}, errors.New(
			"citation analysis requires an explicit --subject-version",
		)
	}
	if command.SubjectVersion != strings.TrimSpace(command.SubjectVersion) {
		return workerCommand{}, errors.New(
			"citation analysis subject-version must be trimmed",
		)
	}
	if command.EligibilityPolicyVersion !=
		biomed.BiomedicalPublicEligibilityPolicyVersion {
		return workerCommand{}, fmt.Errorf(
			"citation analysis --eligibility-policy-version must equal %s",
			biomed.BiomedicalPublicEligibilityPolicyVersion,
		)
	}
	if command.MetricYear < 1900 || command.MetricYear > 3000 {
		return workerCommand{}, errors.New(
			"citation analysis requires --jcr-metric-year between 1900 and 3000",
		)
	}
	if command.JCRReceipt == "" {
		return workerCommand{}, errors.New(
			"citation analysis requires an explicit --jcr-import-receipt",
		)
	}
	if command.JCRReceipt != strings.TrimSpace(command.JCRReceipt) {
		return workerCommand{}, errors.New(
			"citation analysis jcr-import-receipt must be trimmed",
		)
	}
	parsedReceipt, err := uuid.Parse(command.JCRReceipt)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"citation analysis jcr-import-receipt must be a UUID: %w",
			err,
		)
	}
	command.JCRReceipt = parsedReceipt.String()
	return command, nil
}

const (
	biomedicalTrendFormulaVersion    = analysis.PublicationTrendFormulaVersion
	biomedicalJournalFormulaVersion  = analysis.JournalPatternFormulaVersion
	biomedicalOpportunityVersion     = analysis.OpportunityFormulaVersion
	biomedicalOpportunityRuleSet     = analysis.OpportunityRuleSetVersion
	biomedicalJournalMinimumCoverage = 0.8
)

type biomedicalAnalysisScopeFlags struct {
	asOf string
}

func registerBiomedicalAnalysisScopeFlags(
	set *flag.FlagSet,
	command *workerCommand,
) *biomedicalAnalysisScopeFlags {
	flags := &biomedicalAnalysisScopeFlags{}
	set.StringVar(
		&flags.asOf,
		"as-of",
		"",
		"explicit RFC3339Nano analysis boundary",
	)
	set.StringVar(
		&command.SubjectVersion,
		"subject-version",
		"",
		"exact biomedical Subject registry version",
	)
	set.StringVar(
		&command.EligibilityPolicyVersion,
		"eligibility-policy-version",
		"",
		"biomedical public eligibility policy version",
	)
	set.IntVar(
		&command.MetricYear,
		"jcr-metric-year",
		0,
		"authorized JCR metric year",
	)
	set.StringVar(
		&command.JCRReceipt,
		"jcr-import-receipt",
		"",
		"authorized JCR import receipt UUID",
	)
	set.StringVar(
		&command.VenuePolicyName,
		"venue-policy-name",
		"",
		"exact Venue eligibility policy name",
	)
	set.IntVar(
		&command.VenuePolicyVersion,
		"venue-policy-version",
		0,
		"exact Venue eligibility policy version",
	)
	return flags
}

func validateBiomedicalAnalysisScope(
	command workerCommand,
	asOf string,
	target string,
) (workerCommand, error) {
	if asOf == "" {
		return workerCommand{}, fmt.Errorf(
			"%s analysis requires an explicit --as-of",
			target,
		)
	}
	if asOf != strings.TrimSpace(asOf) {
		return workerCommand{}, fmt.Errorf(
			"%s analysis as-of must be trimmed",
			target,
		)
	}
	parsedAsOf, err := time.Parse(time.RFC3339Nano, asOf)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"%s analysis as-of must use RFC3339Nano: %w",
			target,
			err,
		)
	}
	command.AsOf = parsedAsOf.UTC()
	if command.SubjectVersion == "" {
		return workerCommand{}, fmt.Errorf(
			"%s analysis requires an explicit --subject-version",
			target,
		)
	}
	if command.SubjectVersion != strings.TrimSpace(command.SubjectVersion) {
		return workerCommand{}, fmt.Errorf(
			"%s analysis subject-version must be trimmed",
			target,
		)
	}
	if command.EligibilityPolicyVersion !=
		biomed.BiomedicalPublicEligibilityPolicyVersion {
		return workerCommand{}, fmt.Errorf(
			"%s analysis --eligibility-policy-version must equal %s",
			target,
			biomed.BiomedicalPublicEligibilityPolicyVersion,
		)
	}
	if command.MetricYear < 1900 || command.MetricYear > 3000 {
		return workerCommand{}, fmt.Errorf(
			"%s analysis requires --jcr-metric-year between 1900 and 3000",
			target,
		)
	}
	if command.JCRReceipt == "" {
		return workerCommand{}, fmt.Errorf(
			"%s analysis requires an explicit --jcr-import-receipt",
			target,
		)
	}
	if command.JCRReceipt != strings.TrimSpace(command.JCRReceipt) {
		return workerCommand{}, fmt.Errorf(
			"%s analysis jcr-import-receipt must be trimmed",
			target,
		)
	}
	parsedReceipt, err := uuid.Parse(command.JCRReceipt)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"%s analysis jcr-import-receipt must be a UUID: %w",
			target,
			err,
		)
	}
	command.JCRReceipt = parsedReceipt.String()
	if command.VenuePolicyName != venue.JournalAllQ1PolicyName {
		return workerCommand{}, fmt.Errorf(
			"%s analysis --venue-policy-name must equal %s",
			target,
			venue.JournalAllQ1PolicyName,
		)
	}
	if command.VenuePolicyVersion != venue.JournalAllQ1PolicyRevision {
		return workerCommand{}, fmt.Errorf(
			"%s analysis --venue-policy-version must equal version %d",
			target,
			venue.JournalAllQ1PolicyRevision,
		)
	}
	return command, nil
}

func parseTrendAnalysisCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("analyze trends", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	scope := registerBiomedicalAnalysisScopeFlags(set, &command)
	set.IntVar(
		&command.RecentWindowDays,
		"recent-window-days",
		0,
		"recent publication window in days",
	)
	set.IntVar(
		&command.BaselineWindowDays,
		"baseline-window-days",
		0,
		"historical baseline window in days",
	)
	set.IntVar(
		&command.MinimumPaperCount,
		"minimum-paper-count",
		0,
		"minimum paper count for a published estimate",
	)
	set.IntVar(
		&command.MinimumIndependentJournalCount,
		"minimum-independent-journal-count",
		0,
		"minimum independent journal count",
	)
	set.IntVar(
		&command.MinimumIndependentTeamCount,
		"minimum-independent-team-count",
		0,
		"minimum independent team count",
	)
	set.StringVar(
		&command.TrendModelSelectionRule,
		"model-selection",
		"",
		"predeclared publication trend model-selection rule",
	)
	set.Float64Var(
		&command.TrendDispersionThreshold,
		"dispersion-threshold",
		0,
		"predeclared dispersion threshold for model selection",
	)
	set.StringVar(
		&command.FormulaVersion,
		"formula-version",
		"",
		"deterministic publication trend formula version",
	)
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse trend analysis flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected trend analysis arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}
	command.Kind = commandAnalyzeTrends
	if command.RecentWindowDays < 1 || command.RecentWindowDays > 3650 {
		return workerCommand{}, errors.New(
			"trend analysis --recent-window-days must be between 1 and 3650",
		)
	}
	if command.BaselineWindowDays <= command.RecentWindowDays ||
		command.BaselineWindowDays > 3650 {
		return workerCommand{}, errors.New(
			"trend analysis --baseline-window-days must be greater than recent-window-days and at most 3650",
		)
	}
	if command.MinimumPaperCount < 1 {
		return workerCommand{}, errors.New(
			"trend analysis --minimum-paper-count must be at least 1",
		)
	}
	if command.MinimumIndependentJournalCount < 1 {
		return workerCommand{}, errors.New(
			"trend analysis --minimum-independent-journal-count must be at least 1",
		)
	}
	if command.MinimumIndependentTeamCount < 1 {
		return workerCommand{}, errors.New(
			"trend analysis --minimum-independent-team-count must be at least 1",
		)
	}
	switch analysis.TrendModelSelectionRule(command.TrendModelSelectionRule) {
	case analysis.TrendModelSelectionFixedPoisson,
		analysis.TrendModelSelectionFixedNegativeBinomial:
		if command.TrendDispersionThreshold != 0 {
			return workerCommand{}, errors.New(
				"trend analysis --dispersion-threshold must be zero for a fixed model selection",
			)
		}
	case analysis.TrendModelSelectionDispersionThreshold:
		if command.TrendDispersionThreshold <= 0 {
			return workerCommand{}, errors.New(
				"trend analysis --dispersion-threshold must be positive for predeclared_dispersion_threshold",
			)
		}
	default:
		return workerCommand{}, errors.New(
			"trend analysis --model-selection must be fixed_poisson, fixed_negative_binomial, or predeclared_dispersion_threshold",
		)
	}
	if command.FormulaVersion != biomedicalTrendFormulaVersion {
		return workerCommand{}, fmt.Errorf(
			"trend analysis --formula-version must equal %s",
			biomedicalTrendFormulaVersion,
		)
	}
	return validateBiomedicalAnalysisScope(command, scope.asOf, "trend")
}

func parseJournalAnalysisCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("analyze journals", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	scope := registerBiomedicalAnalysisScopeFlags(set, &command)
	set.IntVar(
		&command.WindowDays,
		"window-days",
		0,
		"journal editorial-pattern window in days",
	)
	set.IntVar(
		&command.MinimumSupportCount,
		"minimum-support-count",
		0,
		"minimum within-journal support count",
	)
	set.IntVar(
		&command.MinimumFieldBaselineCount,
		"minimum-field-baseline-count",
		0,
		"minimum field baseline count",
	)
	set.StringVar(
		&command.FormulaVersion,
		"formula-version",
		"",
		"deterministic journal editorial-pattern formula version",
	)
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse journal analysis flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected journal analysis arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}
	command.Kind = commandAnalyzeJournals
	if command.WindowDays < 1 || command.WindowDays > 3650 {
		return workerCommand{}, errors.New(
			"journal analysis --window-days must be between 1 and 3650",
		)
	}
	if command.MinimumSupportCount < 1 {
		return workerCommand{}, errors.New(
			"journal analysis --minimum-support-count must be at least 1",
		)
	}
	if command.MinimumFieldBaselineCount < command.MinimumSupportCount {
		return workerCommand{}, errors.New(
			"journal analysis --minimum-field-baseline-count must be at least minimum-support-count",
		)
	}
	if command.FormulaVersion != biomedicalJournalFormulaVersion {
		return workerCommand{}, fmt.Errorf(
			"journal analysis --formula-version must equal %s",
			biomedicalJournalFormulaVersion,
		)
	}
	return validateBiomedicalAnalysisScope(command, scope.asOf, "journal")
}

func parseOpportunityAnalysisCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("analyze opportunities", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	scope := registerBiomedicalAnalysisScopeFlags(set, &command)
	set.StringVar(
		&command.RuleSetVersion,
		"rule-set-version",
		"",
		"predefined biomedical opportunity rule-set version",
	)
	set.StringVar(
		&command.FormulaVersion,
		"formula-version",
		"",
		"deterministic opportunity formula version",
	)
	set.StringVar(
		&command.CitationAnalysisRunID,
		"citation-analysis-run-id",
		"",
		"exact succeeded citation analysis run UUID",
	)
	set.StringVar(
		&command.TrendAnalysisRunID,
		"trend-analysis-run-id",
		"",
		"exact succeeded trend analysis run UUID",
	)
	set.StringVar(
		&command.JournalAnalysisRunID,
		"journal-analysis-run-id",
		"",
		"exact succeeded journal analysis run UUID",
	)
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf(
			"parse opportunity analysis flags: %w",
			err,
		)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected opportunity analysis arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}
	command.Kind = commandAnalyzeOpportunities
	if command.RuleSetVersion != biomedicalOpportunityRuleSet {
		return workerCommand{}, fmt.Errorf(
			"opportunity analysis --rule-set-version must equal %s",
			biomedicalOpportunityRuleSet,
		)
	}
	if command.FormulaVersion != biomedicalOpportunityVersion {
		return workerCommand{}, fmt.Errorf(
			"opportunity analysis --formula-version must equal %s",
			biomedicalOpportunityVersion,
		)
	}
	var err error
	command.CitationAnalysisRunID, err = parseRequiredRunID(
		command.CitationAnalysisRunID,
		"opportunity analysis",
		"citation-analysis-run-id",
	)
	if err != nil {
		return workerCommand{}, err
	}
	command.TrendAnalysisRunID, err = parseRequiredRunID(
		command.TrendAnalysisRunID,
		"opportunity analysis",
		"trend-analysis-run-id",
	)
	if err != nil {
		return workerCommand{}, err
	}
	command.JournalAnalysisRunID, err = parseRequiredRunID(
		command.JournalAnalysisRunID,
		"opportunity analysis",
		"journal-analysis-run-id",
	)
	if err != nil {
		return workerCommand{}, err
	}
	return validateBiomedicalAnalysisScope(command, scope.asOf, "opportunity")
}

func parseRequiredRunID(value string, contextName string, flagName string) (string, error) {
	if value == "" {
		return "", fmt.Errorf(
			"%s requires an explicit --%s",
			contextName,
			flagName,
		)
	}
	if value != strings.TrimSpace(value) {
		return "", fmt.Errorf("%s %s must be trimmed", contextName, flagName)
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return "", fmt.Errorf(
			"%s %s must be a UUID: %w",
			contextName,
			flagName,
			err,
		)
	}
	return parsed.String(), nil
}

func parseVenueAssessmentCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("assess venues", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	var assessedAt string
	set.IntVar(
		&command.MetricYear,
		"metric-year",
		0,
		"authorized JCR metric year",
	)
	set.StringVar(
		&command.PolicyVersion,
		"policy-version",
		"",
		"Venue policy version",
	)
	set.StringVar(
		&assessedAt,
		"assessed-at",
		"",
		"explicit RFC3339Nano assessment time",
	)
	set.StringVar(
		&command.JCRReceipt,
		"jcr-receipt",
		"",
		"authorized JCR import receipt UUID",
	)
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf(
			"parse Venue assessment flags: %w",
			err,
		)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected Venue assessment arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}

	command.Kind = commandAssessVenues
	if command.MetricYear < 1900 || command.MetricYear > 3000 {
		return workerCommand{}, errors.New(
			"Venue assessment requires --metric-year between 1900 and 3000",
		)
	}
	if command.PolicyVersion != venue.JournalAllQ1PolicyVersion {
		return workerCommand{}, fmt.Errorf(
			"Venue assessment --policy-version must equal %s",
			venue.JournalAllQ1PolicyVersion,
		)
	}
	if assessedAt == "" {
		return workerCommand{}, errors.New(
			"Venue assessment requires an explicit --assessed-at",
		)
	}
	if assessedAt != strings.TrimSpace(assessedAt) {
		return workerCommand{}, errors.New(
			"Venue assessment assessed-at must be trimmed",
		)
	}
	parsedAssessedAt, err := time.Parse(time.RFC3339Nano, assessedAt)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"Venue assessment assessed-at must use RFC3339Nano: %w",
			err,
		)
	}
	if parsedAssessedAt.IsZero() {
		return workerCommand{}, errors.New(
			"Venue assessment assessed-at must be non-zero",
		)
	}
	command.AssessedAt = parsedAssessedAt
	command.JCRReceipt = strings.TrimSpace(command.JCRReceipt)
	if command.JCRReceipt == "" {
		return workerCommand{}, errors.New(
			"Venue assessment requires an explicit --jcr-receipt",
		)
	}
	parsedReceipt, err := uuid.Parse(command.JCRReceipt)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"Venue assessment jcr-receipt must be a UUID: %w",
			err,
		)
	}
	command.JCRReceipt = parsedReceipt.String()
	return command, nil
}

func parseBiomedicalEligibilityCommand(
	args []string,
) (workerCommand, error) {
	set := flag.NewFlagSet(
		"assess biomedical-eligibility",
		flag.ContinueOnError,
	)
	set.SetOutput(io.Discard)
	var command workerCommand
	var assessedAt string
	set.IntVar(
		&command.MetricYear,
		"metric-year",
		0,
		"authorized JCR metric year",
	)
	set.StringVar(
		&command.SubjectVersion,
		"subject-version",
		"",
		"exact biomedical Subject registry version",
	)
	set.StringVar(
		&command.PolicyVersion,
		"policy-version",
		"",
		"biomedical public eligibility policy version",
	)
	set.StringVar(
		&assessedAt,
		"assessed-at",
		"",
		"explicit RFC3339Nano assessment time",
	)
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf(
			"parse biomedical eligibility flags: %w",
			err,
		)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected biomedical eligibility arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}

	command.Kind = commandAssessBiomedicalEligibility
	if command.MetricYear < 1900 || command.MetricYear > 3000 {
		return workerCommand{}, errors.New(
			"biomedical eligibility requires --metric-year between 1900 and 3000",
		)
	}
	if command.SubjectVersion == "" {
		return workerCommand{}, errors.New(
			"biomedical eligibility requires an explicit --subject-version",
		)
	}
	if command.SubjectVersion != strings.TrimSpace(command.SubjectVersion) {
		return workerCommand{}, errors.New(
			"biomedical eligibility subject-version must be trimmed",
		)
	}
	if err := biomed.ValidateLegacyBiomedicalSubjectVersion(
		command.SubjectVersion,
	); err != nil {
		return workerCommand{}, err
	}
	if command.PolicyVersion == "" {
		return workerCommand{}, errors.New(
			"biomedical eligibility requires an explicit --policy-version",
		)
	}
	if command.PolicyVersion != strings.TrimSpace(command.PolicyVersion) {
		return workerCommand{}, errors.New(
			"biomedical eligibility policy-version must be trimmed",
		)
	}
	if command.PolicyVersion !=
		biomed.BiomedicalPublicEligibilityPolicyVersion {
		return workerCommand{}, fmt.Errorf(
			"biomedical eligibility --policy-version must equal %s",
			biomed.BiomedicalPublicEligibilityPolicyVersion,
		)
	}
	if assessedAt == "" {
		return workerCommand{}, errors.New(
			"biomedical eligibility requires an explicit --assessed-at",
		)
	}
	if assessedAt != strings.TrimSpace(assessedAt) {
		return workerCommand{}, errors.New(
			"biomedical eligibility assessed-at must be trimmed",
		)
	}
	parsedAssessedAt, err := time.Parse(time.RFC3339Nano, assessedAt)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"biomedical eligibility assessed-at must use RFC3339Nano: %w",
			err,
		)
	}
	if parsedAssessedAt.IsZero() {
		return workerCommand{}, errors.New(
			"biomedical eligibility assessed-at must be non-zero",
		)
	}
	command.AssessedAt = parsedAssessedAt
	return command, nil
}

func parseCatalogPublishCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("publish catalog", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	var generatedAt string
	set.StringVar(
		&command.FormulaVersion,
		"formula-version",
		"",
		"catalog formula version",
	)
	set.StringVar(
		&generatedAt,
		"generated-at",
		"",
		"explicit RFC3339Nano generation time",
	)
	set.IntVar(
		&command.MetricYear,
		"jcr-metric-year",
		0,
		"authorized JCR metric year",
	)
	set.StringVar(
		&command.VenuePolicyName,
		"venue-policy-name",
		"",
		"Venue policy name",
	)
	set.IntVar(
		&command.VenuePolicyVersion,
		"venue-policy-version",
		0,
		"Venue policy version number",
	)
	set.StringVar(
		&command.EligibilityPolicyVersion,
		"eligibility-policy-version",
		"",
		"biomedical public eligibility policy version",
	)
	set.StringVar(
		&command.SubjectVersion,
		"subject-version",
		"",
		"biomedical Subject registry version",
	)
	set.StringVar(
		&command.JCRReceipt,
		"jcr-import-receipt",
		"",
		"authorized JCR import receipt UUID",
	)
	set.StringVar(
		&command.CitationSource,
		"citation-source",
		"",
		"exact citation evidence source to publish",
	)
	set.StringVar(
		&command.CitationAnalysisRunID,
		"citation-analysis-run-id",
		"",
		"exact succeeded citation analysis run UUID",
	)
	set.StringVar(
		&command.TrendAnalysisRunID,
		"trend-analysis-run-id",
		"",
		"exact succeeded trend analysis run UUID",
	)
	set.StringVar(
		&command.JournalAnalysisRunID,
		"journal-analysis-run-id",
		"",
		"exact succeeded journal analysis run UUID",
	)
	set.StringVar(
		&command.OpportunityAnalysisRunID,
		"opportunity-analysis-run-id",
		"",
		"exact succeeded opportunity analysis run UUID",
	)
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse catalog publish flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected catalog publish arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}

	command.Kind = commandPublishCatalog
	if command.FormulaVersion == "" {
		return workerCommand{}, errors.New(
			"catalog publish requires an explicit --formula-version",
		)
	}
	if command.FormulaVersion != strings.TrimSpace(command.FormulaVersion) {
		return workerCommand{}, errors.New("catalog formula-version must be trimmed")
	}
	if generatedAt == "" {
		return workerCommand{}, errors.New(
			"catalog publish requires an explicit --generated-at",
		)
	}
	if generatedAt != strings.TrimSpace(generatedAt) {
		return workerCommand{}, errors.New("catalog generated-at must be trimmed")
	}
	parsed, err := time.Parse(time.RFC3339Nano, generatedAt)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"catalog generated-at must use RFC3339Nano: %w",
			err,
		)
	}
	if parsed.IsZero() {
		return workerCommand{}, errors.New("catalog generated-at must be non-zero")
	}
	command.GeneratedAt = parsed
	if command.MetricYear < 1900 || command.MetricYear > 3000 {
		return workerCommand{}, errors.New(
			"catalog publish requires --jcr-metric-year between 1900 and 3000",
		)
	}
	if command.VenuePolicyName != venue.JournalAllQ1PolicyName {
		return workerCommand{}, fmt.Errorf(
			"catalog --venue-policy-name must equal %s",
			venue.JournalAllQ1PolicyName,
		)
	}
	if command.VenuePolicyVersion != venue.JournalAllQ1PolicyRevision {
		return workerCommand{}, fmt.Errorf(
			"catalog --venue-policy-version must equal version %d",
			venue.JournalAllQ1PolicyRevision,
		)
	}
	if command.EligibilityPolicyVersion == "" {
		return workerCommand{}, errors.New(
			"catalog publish requires an explicit --eligibility-policy-version",
		)
	}
	if command.EligibilityPolicyVersion !=
		strings.TrimSpace(command.EligibilityPolicyVersion) {
		return workerCommand{}, errors.New(
			"catalog eligibility-policy-version must be trimmed",
		)
	}
	if command.EligibilityPolicyVersion !=
		biomed.BiomedicalPublicEligibilityPolicyVersion {
		return workerCommand{}, fmt.Errorf(
			"catalog --eligibility-policy-version must equal %s",
			biomed.BiomedicalPublicEligibilityPolicyVersion,
		)
	}
	if command.SubjectVersion == "" {
		return workerCommand{}, errors.New(
			"catalog publish requires an explicit --subject-version",
		)
	}
	if command.SubjectVersion != strings.TrimSpace(command.SubjectVersion) {
		return workerCommand{}, errors.New(
			"catalog subject-version must be trimmed",
		)
	}
	command.JCRReceipt = strings.TrimSpace(command.JCRReceipt)
	if command.JCRReceipt == "" {
		return workerCommand{}, errors.New(
			"catalog publish requires an explicit --jcr-import-receipt",
		)
	}
	parsedReceipt, err := uuid.Parse(command.JCRReceipt)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"catalog jcr-import-receipt must be a UUID: %w",
			err,
		)
	}
	command.JCRReceipt = parsedReceipt.String()
	if command.CitationSource == "" {
		return workerCommand{}, errors.New(
			"catalog publish requires an explicit --citation-source",
		)
	}
	if command.CitationSource != strings.TrimSpace(command.CitationSource) {
		return workerCommand{}, errors.New(
			"catalog citation-source must be trimmed",
		)
	}
	command.CitationAnalysisRunID, err = parseRequiredRunID(
		command.CitationAnalysisRunID,
		"catalog publish",
		"citation-analysis-run-id",
	)
	if err != nil {
		return workerCommand{}, err
	}
	command.TrendAnalysisRunID, err = parseRequiredRunID(
		command.TrendAnalysisRunID,
		"catalog publish",
		"trend-analysis-run-id",
	)
	if err != nil {
		return workerCommand{}, err
	}
	command.JournalAnalysisRunID, err = parseRequiredRunID(
		command.JournalAnalysisRunID,
		"catalog publish",
		"journal-analysis-run-id",
	)
	if err != nil {
		return workerCommand{}, err
	}
	command.OpportunityAnalysisRunID, err = parseRequiredRunID(
		command.OpportunityAnalysisRunID,
		"catalog publish",
		"opportunity-analysis-run-id",
	)
	if err != nil {
		return workerCommand{}, err
	}
	return command, nil
}

func parseJCRImportCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("import jcr", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	set.StringVar(&command.File, "file", "", "authorized JCR CSV export")
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse JCR import flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected JCR import arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}
	command.Kind = commandImportJCR
	command.File = strings.TrimSpace(command.File)
	if command.File == "" {
		return workerCommand{}, errors.New("JCR import requires an explicit --file")
	}
	if strings.ContainsAny(command.File, "?#") ||
		!strings.EqualFold(filepath.Ext(command.File), ".csv") {
		return workerCommand{}, errors.New("JCR import file must be an explicit .csv file")
	}
	return command, nil
}

func parseSubjectImportCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("import subjects", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	set.StringVar(&command.File, "file", "", "versioned biomedical Subject CSV")
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse Subject import flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected Subject import arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}
	command.Kind = commandImportSubjects
	command.File = strings.TrimSpace(command.File)
	if command.File == "" {
		return workerCommand{}, errors.New(
			"Subject import requires an explicit --file",
		)
	}
	if strings.ContainsAny(command.File, "?#") ||
		!strings.EqualFold(filepath.Ext(command.File), ".csv") {
		return workerCommand{}, errors.New(
			"Subject import file must be an explicit .csv file",
		)
	}
	return command, nil
}

func parseOpenAlexCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("sync openalex", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	set.StringVar(&command.Query, "query", "", "OpenAlex search query")
	set.StringVar(&command.Filter, "filter", "", "OpenAlex exact filter expression")
	set.IntVar(&command.MaxResults, "max-results", 0, "maximum records")
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse OpenAlex flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf("unexpected OpenAlex arguments: %s", strings.Join(set.Args(), " "))
	}
	command.Kind = commandSyncOpenAlex
	command.Query = strings.TrimSpace(command.Query)
	command.Filter = strings.TrimSpace(command.Filter)
	if command.Query == "" && command.Filter == "" {
		return workerCommand{}, errors.New("OpenAlex sync requires a query or filter")
	}
	if err := validateMaxResults(command.MaxResults); err != nil {
		return workerCommand{}, err
	}
	return command, nil
}

func parsePubMedCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("sync pubmed", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	var fromDate, toDate string
	var issns stringListFlag
	set.StringVar(&command.Query, "query", "", "PubMed Entrez term")
	set.StringVar(&fromDate, "from-date", "", "inclusive YYYY-MM-DD")
	set.StringVar(&toDate, "to-date", "", "inclusive YYYY-MM-DD")
	set.Var(&issns, "issn", "exact journal ISSN; repeatable")
	set.IntVar(&command.MaxResults, "max-results", 0, "maximum records")
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse PubMed flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf("unexpected PubMed arguments: %s", strings.Join(set.Args(), " "))
	}
	if (fromDate == "") != (toDate == "") {
		return workerCommand{}, errors.New("PubMed sync requires both from-date and to-date")
	}
	if fromDate == "" {
		return workerCommand{}, errors.New("PubMed sync requires both from-date and to-date")
	}
	var err error
	command.FromDate, err = parseDateFlag("from-date", fromDate)
	if err != nil {
		return workerCommand{}, err
	}
	command.ToDate, err = parseDateFlag("to-date", toDate)
	if err != nil {
		return workerCommand{}, err
	}
	if command.FromDate.After(command.ToDate) {
		return workerCommand{}, errors.New("PubMed from-date must not follow to-date")
	}
	command.Kind = commandSyncPubMed
	command.Query = strings.TrimSpace(command.Query)
	command.ISSNs = issns.values()
	if command.Query == "" && len(command.ISSNs) == 0 {
		return workerCommand{}, errors.New("PubMed sync requires a query or exact ISSN")
	}
	if err := validateMaxResults(command.MaxResults); err != nil {
		return workerCommand{}, err
	}
	return command, nil
}

func parseCrossrefCommand(
	kind commandKind,
	args []string,
) (workerCommand, error) {
	var target string
	switch kind {
	case commandSyncCrossrefCreated:
		target = "crossref-created"
	case commandSyncCrossrefUpdated:
		target = "crossref-updated"
	default:
		return workerCommand{}, fmt.Errorf(
			"invalid Crossref stream command kind %q",
			kind,
		)
	}
	set := flag.NewFlagSet("sync "+target, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	var fromDate, toDate string
	var issns stringListFlag
	set.StringVar(&fromDate, "from-date", "", "inclusive indexed YYYY-MM-DD")
	set.StringVar(&toDate, "to-date", "", "inclusive indexed YYYY-MM-DD")
	set.Var(&issns, "issn", "exact journal ISSN; repeatable")
	set.IntVar(&command.MaxResults, "max-results", 0, "maximum records")
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse Crossref flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf("unexpected Crossref arguments: %s", strings.Join(set.Args(), " "))
	}
	if fromDate == "" || toDate == "" {
		return workerCommand{}, errors.New("Crossref sync requires both from-date and to-date")
	}
	var err error
	command.FromDate, err = parseDateFlag("from-date", fromDate)
	if err != nil {
		return workerCommand{}, err
	}
	command.ToDate, err = parseDateFlag("to-date", toDate)
	if err != nil {
		return workerCommand{}, err
	}
	if command.FromDate.After(command.ToDate) {
		return workerCommand{}, errors.New("Crossref from-date must not follow to-date")
	}
	command.Kind = kind
	command.ISSNs = issns.values()
	if err := validateMaxResults(command.MaxResults); err != nil {
		return workerCommand{}, err
	}
	return command, nil
}

func validateMaxResults(value int) error {
	if value < 1 || value > maxSyncResults {
		return fmt.Errorf("max-results must be between 1 and %d", maxSyncResults)
	}
	return nil
}

func parseDateFlag(name string, value string) (time.Time, error) {
	parsed, err := time.Parse(time.DateOnly, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must use YYYY-MM-DD: %w", name, err)
	}
	return parsed.UTC(), nil
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if normalized == "" {
		return errors.New("ISSN must not be empty")
	}
	*values = append(*values, normalized)
	return nil
}

func (values stringListFlag) values() []string {
	result := append([]string(nil), values...)
	slices.Sort(result)
	result = slices.Compact(result)
	return result
}

func runCommand(
	ctx context.Context,
	cfg config.Config,
	command workerCommand,
) (map[string]any, error) {
	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("open worker database: %w", err)
	}
	defer pool.Close()
	switch command.Kind {
	case commandAnalyzeAbstractRoutes:
		return runAbstractAnalysis(ctx, pool, cfg, command)
	case commandAnalyzeCitations:
		return runCitationAnalysis(ctx, pool, command)
	case commandAnalyzeTrends:
		return runTrendAnalysis(ctx, pool, command)
	case commandAnalyzeJournals:
		return runJournalAnalysis(ctx, pool, command)
	case commandAnalyzeOpportunities:
		return runOpportunityAnalysis(ctx, pool, command)
	case commandPublishCatalog:
		return runCatalogPublish(ctx, pool, command)
	case commandAssessBiomedicalEligibility:
		return runBiomedicalEligibility(ctx, pool, command)
	case commandAssessVenues:
		return runVenueAssessment(ctx, pool, command)
	case commandImportJCR:
		return runJCRImport(ctx, pool, cfg, command)
	case commandImportSubjects:
		return runSubjectImport(ctx, pool, command)
	default:
		return runSync(ctx, pool, cfg, command)
	}
}

func runAbstractAnalysis(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Config,
	command workerCommand,
) (map[string]any, error) {
	store, err := abstractanalysis.NewPostgresStore(pool)
	if err != nil {
		return nil, fmt.Errorf(
			"create abstract analysis store: %w",
			err,
		)
	}
	client, err := openairesponses.New(
		&http.Client{Timeout: abstractAnalysisRequestTimeout},
		openairesponses.Config{
			BaseURL: cfg.OpenAI.BaseURL,
			Mode:    openairesponses.Mode(cfg.OpenAI.APIMode),
			APIKey:  cfg.OpenAI.APIKey,
			Model:   cfg.OpenAI.Model,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create OpenAI Responses client: %w",
			err,
		)
	}
	service, err := abstractanalysis.NewService(
		store,
		client,
		openairesponses.Mode(cfg.OpenAI.APIMode),
		cfg.OpenAI.Model,
		time.Now,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create abstract route analysis service: %w",
			err,
		)
	}
	summary, err := service.Analyze(ctx, abstractAnalysisInput(command))
	if err != nil {
		return nil, fmt.Errorf("analyze abstract routes: %w", err)
	}
	return abstractAnalysisResult(
		command,
		cfg.OpenAI.APIMode,
		cfg.OpenAI.Model,
		summary,
	), nil
}

func abstractAnalysisInput(
	command workerCommand,
) abstractanalysis.AnalyzeInput {
	return abstractanalysis.AnalyzeInput{
		PromptVersion: command.PromptVersion,
		SchemaVersion: command.SchemaVersion,
		Cutoff:        command.AnalysisCutoff,
		Limit:         command.Limit,
	}
}

func abstractAnalysisResult(
	command workerCommand,
	apiMode string,
	requestedModel string,
	summary abstractanalysis.Summary,
) map[string]any {
	return map[string]any{
		"prompt_version":  command.PromptVersion,
		"schema_version":  command.SchemaVersion,
		"analysis_cutoff": command.AnalysisCutoff.Format(time.RFC3339Nano),
		"api_mode":        apiMode,
		"requested_model": requestedModel,
		"selected":        summary.Selected,
		"succeeded":       summary.Succeeded,
		"failed":          summary.Failed,
	}
}

func runCitationAnalysis(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	service, err := citation.NewPostgresAnalysisService(pool, time.Now)
	if err != nil {
		return nil, fmt.Errorf(
			"create citation analysis service: %w",
			err,
		)
	}
	summary, err := service.Analyze(ctx, citationAnalysisInput(command))
	if err != nil {
		return nil, fmt.Errorf("analyze citations: %w", err)
	}
	return citationAnalysisResult(command, summary), nil
}

func citationAnalysisInput(command workerCommand) citation.AnalysisInput {
	return citation.AnalysisInput{
		AsOf:                     command.AsOf,
		Source:                   command.CitationSource,
		VelocityWindowDays:       command.VelocityWindowDays,
		MinimumCohortSize:        command.MinimumCohortSize,
		FormulaVersion:           command.FormulaVersion,
		SubjectVersion:           command.SubjectVersion,
		EligibilityPolicyVersion: command.EligibilityPolicyVersion,
		JCRMetricYear:            command.MetricYear,
		JCRImportReceipt:         uuid.MustParse(command.JCRReceipt),
	}
}

func citationAnalysisResult(
	command workerCommand,
	summary citation.AnalysisSummary,
) map[string]any {
	return map[string]any{
		"analysis_run_id":         summary.RunID.String(),
		"source":                  command.CitationSource,
		"as_of":                   command.AsOf.UTC().Format(time.RFC3339Nano),
		"velocity_window_days":    command.VelocityWindowDays,
		"minimum_cohort_size":     command.MinimumCohortSize,
		"formula_version":         command.FormulaVersion,
		"subject_version":         command.SubjectVersion,
		"eligibility_policy":      command.EligibilityPolicyVersion,
		"jcr_metric_year":         command.MetricYear,
		"jcr_import_receipt":      command.JCRReceipt,
		"total_works":             summary.TotalWorks,
		"known_citation_counts":   summary.KnownCitationCounts,
		"known_velocities":        summary.KnownVelocities,
		"insufficient_velocity":   summary.InsufficientVelocity,
		"percentile_rows":         summary.PercentileRows,
		"known_percentiles":       summary.KnownPercentiles,
		"insufficient_percentile": summary.InsufficientPercentile,
	}
}

func runTrendAnalysis(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	service, err := analysis.NewPostgresAnalysisService(pool, time.Now)
	if err != nil {
		return nil, fmt.Errorf(
			"create publication trend analysis service: %w",
			err,
		)
	}
	summary, err := service.AnalyzePublicationTrends(
		ctx,
		trendAnalysisInput(command),
	)
	if err != nil {
		return nil, fmt.Errorf("analyze publication trends: %w", err)
	}
	return biomedicalAnalysisResult(command, summary), nil
}

func trendAnalysisInput(command workerCommand) analysis.TrendAnalysisInput {
	return analysis.TrendAnalysisInput{
		AsOf:                     command.AsOf,
		FormulaVersion:           command.FormulaVersion,
		ModelSelectionRule:       analysis.TrendModelSelectionRule(command.TrendModelSelectionRule),
		DispersionThreshold:      command.TrendDispersionThreshold,
		SubjectVersion:           command.SubjectVersion,
		EligibilityPolicyVersion: command.EligibilityPolicyVersion,
		JCRMetricYear:            command.MetricYear,
		JCRImportReceipt:         uuid.MustParse(command.JCRReceipt),
		VenuePolicyName:          command.VenuePolicyName,
		VenuePolicyVersion:       command.VenuePolicyVersion,
		RecentWindowDays:         command.RecentWindowDays,
		BaselineWindowDays:       command.BaselineWindowDays,
		MinimumPaperCount:        command.MinimumPaperCount,
		MinimumIndependentJournalCount: command.
			MinimumIndependentJournalCount,
		MinimumIndependentTeamCount: command.
			MinimumIndependentTeamCount,
	}
}

func runJournalAnalysis(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	service, err := analysis.NewPostgresAnalysisService(pool, time.Now)
	if err != nil {
		return nil, fmt.Errorf(
			"create journal analysis service: %w",
			err,
		)
	}
	summary, err := service.AnalyzeJournalPatterns(
		ctx,
		journalAnalysisInput(command),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"analyze journal editorial patterns: %w",
			err,
		)
	}
	return biomedicalAnalysisResult(command, summary), nil
}

func journalAnalysisInput(
	command workerCommand,
) analysis.JournalPatternAnalysisInput {
	return analysis.JournalPatternAnalysisInput{
		AsOf:                      command.AsOf,
		FormulaVersion:            command.FormulaVersion,
		SubjectVersion:            command.SubjectVersion,
		EligibilityPolicyVersion:  command.EligibilityPolicyVersion,
		JCRMetricYear:             command.MetricYear,
		JCRImportReceipt:          uuid.MustParse(command.JCRReceipt),
		VenuePolicyName:           command.VenuePolicyName,
		VenuePolicyVersion:        command.VenuePolicyVersion,
		WindowDays:                command.WindowDays,
		MinimumSupportCount:       command.MinimumSupportCount,
		MinimumFieldBaselineCount: command.MinimumFieldBaselineCount,
		MinimumCoverage:           biomedicalJournalMinimumCoverage,
	}
}

func runOpportunityAnalysis(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	service, err := analysis.NewPostgresAnalysisService(pool, time.Now)
	if err != nil {
		return nil, fmt.Errorf(
			"create research opportunity analysis service: %w",
			err,
		)
	}
	summary, err := service.AnalyzeResearchOpportunities(
		ctx,
		opportunityAnalysisInput(command),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"analyze research opportunities: %w",
			err,
		)
	}
	return biomedicalAnalysisResult(command, summary), nil
}

func opportunityAnalysisInput(
	command workerCommand,
) analysis.OpportunityAnalysisInput {
	return analysis.OpportunityAnalysisInput{
		AsOf:                     command.AsOf,
		FormulaVersion:           command.FormulaVersion,
		RuleSetVersion:           command.RuleSetVersion,
		SubjectVersion:           command.SubjectVersion,
		EligibilityPolicyVersion: command.EligibilityPolicyVersion,
		JCRMetricYear:            command.MetricYear,
		JCRImportReceipt:         uuid.MustParse(command.JCRReceipt),
		VenuePolicyName:          command.VenuePolicyName,
		VenuePolicyVersion:       command.VenuePolicyVersion,
		CitationAnalysisRunID: uuid.MustParse(
			command.CitationAnalysisRunID,
		),
		TrendAnalysisRunID: uuid.MustParse(
			command.TrendAnalysisRunID,
		),
		JournalAnalysisRunID: uuid.MustParse(
			command.JournalAnalysisRunID,
		),
	}
}

func biomedicalAnalysisResult(
	command workerCommand,
	summary analysis.AnalysisRunSummary,
) map[string]any {
	return map[string]any{
		"analysis_run_id": summary.RunID.String(),
		"as_of": command.AsOf.UTC().Format(
			time.RFC3339Nano,
		),
		"formula_version":            command.FormulaVersion,
		"subject_version":            command.SubjectVersion,
		"eligibility_policy_version": command.EligibilityPolicyVersion,
		"jcr_metric_year":            command.MetricYear,
		"jcr_import_receipt":         command.JCRReceipt,
		"venue_policy_name":          command.VenuePolicyName,
		"venue_policy_version":       command.VenuePolicyVersion,
		"snapshot_count":             summary.SnapshotCount,
		"cohort_revisions":           summary.SourceRevisions,
	}
}

func runBiomedicalEligibility(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	works, err := biomed.NewPostgresPublicEligibilityWorkEnumerator(pool)
	if err != nil {
		return nil, fmt.Errorf(
			"create biomedical eligibility Work enumerator: %w",
			err,
		)
	}
	store, err := biomed.NewPostgresPublicEligibilityStore(pool)
	if err != nil {
		return nil, fmt.Errorf(
			"create biomedical eligibility store: %w",
			err,
		)
	}
	eligibility, err := biomed.NewPublicEligibilityService(store)
	if err != nil {
		return nil, fmt.Errorf(
			"create biomedical eligibility service: %w",
			err,
		)
	}
	batch, err := biomed.NewPublicEligibilityBatchService(
		works,
		eligibility,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create biomedical eligibility batch service: %w",
			err,
		)
	}
	summary, err := batch.AssessAll(
		ctx,
		biomed.PublicEligibilityBatchInput{
			PolicyVersion:     command.PolicyVersion,
			MetricYear:        command.MetricYear,
			SubjectVersionKey: command.SubjectVersion,
			AssessedAt:        command.AssessedAt,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"assess biomedical publication eligibility: %w",
			err,
		)
	}
	return biomedicalEligibilityResult(command, summary), nil
}

func biomedicalEligibilityResult(
	command workerCommand,
	summary biomed.PublicEligibilityBatchSummary,
) map[string]any {
	return map[string]any{
		"metric_year":     command.MetricYear,
		"subject_version": command.SubjectVersion,
		"policy_version":  command.PolicyVersion,
		"assessed_at": command.AssessedAt.UTC().Format(
			time.RFC3339Nano,
		),
		"total":    summary.Total,
		"accepted": summary.Accepted,
		"rejected": summary.Rejected,
		"missing":  summary.Missing,
	}
}

func runVenueAssessment(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	store, err := venue.NewPostgresAssessmentStore(pool)
	if err != nil {
		return nil, fmt.Errorf("create Venue assessment store: %w", err)
	}
	service, err := venue.NewAssessmentService(store)
	if err != nil {
		return nil, fmt.Errorf("create Venue assessment service: %w", err)
	}
	summary, err := service.Assess(ctx, venue.AssessmentInput{
		JCRImportReceiptID: command.JCRReceipt,
		MetricYear:         command.MetricYear,
		PolicyVersion:      command.PolicyVersion,
		AssessedAt:         command.AssessedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("assess Venues: %w", err)
	}
	return map[string]any{
		"jcr_receipt":    command.JCRReceipt,
		"metric_year":    command.MetricYear,
		"policy_version": command.PolicyVersion,
		"assessed_at":    command.AssessedAt.UTC().Format(time.RFC3339Nano),
		"total":          summary.Total,
		"accepted":       summary.Accepted,
		"rejected":       summary.Rejected,
		"unknown":        summary.Unknown,
		"not_applicable": summary.NotApplicable,
	}, nil
}

func runCatalogPublish(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	publisher, err := catalog.NewPublisher(pool)
	if err != nil {
		return nil, fmt.Errorf("create catalog publisher: %w", err)
	}
	generation, err := publisher.PublishCurrent(
		ctx,
		catalogPublishInput(command),
	)
	if err != nil {
		return nil, fmt.Errorf("publish current catalog: %w", err)
	}
	return catalogPublishResult(generation, command), nil
}

func catalogPublishInput(command workerCommand) catalog.PublishInput {
	return catalog.PublishInput{
		FormulaVersion:           command.FormulaVersion,
		GeneratedAt:              command.GeneratedAt,
		JCRMetricYear:            command.MetricYear,
		VenuePolicyName:          command.VenuePolicyName,
		VenuePolicyVersion:       command.VenuePolicyVersion,
		EligibilityPolicyVersion: command.EligibilityPolicyVersion,
		SubjectVersion:           command.SubjectVersion,
		JCRImportReceipt:         uuid.MustParse(command.JCRReceipt),
		CitationSource:           command.CitationSource,
		CitationAnalysisRunID: uuid.MustParse(
			command.CitationAnalysisRunID,
		),
		TrendAnalysisRunID: uuid.MustParse(
			command.TrendAnalysisRunID,
		),
		JournalAnalysisRunID: uuid.MustParse(
			command.JournalAnalysisRunID,
		),
		OpportunityAnalysisRunID: uuid.MustParse(
			command.OpportunityAnalysisRunID,
		),
	}
}

func catalogPublishResult(
	generation catalog.Generation,
	command workerCommand,
) map[string]any {
	result := catalogGenerationResult(generation)
	result["jcr_metric_year"] = command.MetricYear
	result["venue_policy_name"] = command.VenuePolicyName
	result["venue_policy_version"] = command.VenuePolicyVersion
	result["eligibility_policy_version"] =
		command.EligibilityPolicyVersion
	result["subject_version"] = command.SubjectVersion
	result["jcr_import_receipt"] = command.JCRReceipt
	result["citation_source"] = command.CitationSource
	result["citation_analysis_run_id"] = command.CitationAnalysisRunID
	result["trend_analysis_run_id"] = command.TrendAnalysisRunID
	result["journal_analysis_run_id"] = command.JournalAnalysisRunID
	result["opportunity_analysis_run_id"] = command.OpportunityAnalysisRunID
	return result
}

func catalogGenerationResult(generation catalog.Generation) map[string]any {
	return map[string]any{
		"id":              generation.ID.String(),
		"source_revision": generation.SourceRevision,
		"formula_version": generation.FormulaVersion,
		"generated_at":    generation.GeneratedAt.UTC().Format(time.RFC3339Nano),
		"published_at":    generation.PublishedAt.UTC().Format(time.RFC3339Nano),
	}
}

func runSubjectImport(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	file, err := os.Open(command.File)
	if err != nil {
		return nil, fmt.Errorf("open Subject import file: %w", err)
	}
	defer file.Close()

	importer, err := biomed.NewPostgresSubjectImporter(pool, time.Now)
	if err != nil {
		return nil, err
	}
	receipt, err := importer.Import(ctx, file)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"file_sha256":      receipt.FileSHA256(),
		"source":           receipt.Source(),
		"registry_version": receipt.RegistryVersion(),
		"imported_at":      receipt.ImportedAt(),
		"subject_count":    receipt.SubjectCount(),
		"rule_count":       receipt.RuleCount(),
	}, nil
}

func runSync(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Config,
	command workerCommand,
) (map[string]any, error) {
	repository, err := ingestion.NewPostgresRepository(pool)
	if err != nil {
		return nil, err
	}
	scopePolicy := ingestion.NewControlledIdentityScopePolicy(
		"scope/controlled-canonical-identity/v1",
	)
	projectionPolicy := ingestion.NewDeterministicProjectionPolicy(
		"projection/latest-source-revision/v1",
	)
	service, err := ingestion.NewService(
		repository,
		repository,
		repository,
		repository,
		scopePolicy,
		projectionPolicy,
	)
	if err != nil {
		return nil, err
	}
	if command.Kind == commandSyncCrossrefCreated ||
		command.Kind == commandSyncCrossrefUpdated {
		return runCrossrefSync(
			ctx,
			pool,
			cfg,
			command,
			service.Run,
		)
	}
	records, logicalSource, err := fetchRecords(ctx, cfg, command)
	if err != nil {
		return nil, err
	}
	payload := commandPayload(command)
	idempotencyKey, err := commandIdempotencyKey(logicalSource, payload)
	if err != nil {
		return nil, err
	}
	job, err := ingestion.NewJob(
		"sync/"+logicalSource+"/"+idempotencyKey,
		logicalSource,
		idempotencyKey,
		payload,
	)
	if err != nil {
		return nil, err
	}
	summary, err := service.Run(
		ctx,
		job,
		recordEventsForCommand(command, logicalSource, records),
	)
	if err != nil {
		return nil, err
	}
	return syncSummaryResult(summary), nil
}

func runCrossrefSync(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Config,
	command workerCommand,
	runIngestion ingestionRunFunc,
) (map[string]any, error) {
	client, err := crossref.NewClient(
		http.DefaultClient,
		crossref.Config{
			BaseURL:          cfg.Crossref.Request.BaseURL,
			ContactEmail:     cfg.Crossref.ContactEmail,
			UserAgent:        "paper-research-hub/0.1 (+mailto:" + cfg.Crossref.ContactEmail + ")",
			BatchSize:        min(cfg.Crossref.Request.BatchSize, 1000),
			Timeout:          cfg.Crossref.Request.Timeout,
			RateLimit:        httpclient.RateLimit{Requests: 10, Interval: time.Second},
			MaxRetries:       cfg.Crossref.Request.MaxRetries,
			MaxWait:          cfg.Crossref.Request.MaxWait,
			InitialBackoff:   500 * time.Millisecond,
			MaxBackoff:       10 * time.Second,
			MaxResponseBytes: 64 << 20,
		},
		httpclient.Dependencies{},
	)
	if err != nil {
		return nil, err
	}
	payload := commandPayload(command)
	idempotencyKey, err := commandIdempotencyKey(source.Crossref, payload)
	if err != nil {
		return nil, err
	}
	job, err := ingestion.NewJob(
		"sync/"+source.Crossref+"/"+idempotencyKey,
		source.Crossref,
		idempotencyKey,
		payload,
	)
	if err != nil {
		return nil, err
	}
	store, err := ingestion.NewPostgresConnectorRunStore(pool)
	if err != nil {
		return nil, err
	}
	summary, err := executeCrossrefConnectorRun(
		ctx,
		command,
		job,
		store,
		client.FetchWithPageReceipts,
		runIngestion,
	)
	if err != nil {
		return nil, err
	}
	return syncSummaryResult(summary), nil
}

func executeCrossrefConnectorRun(
	ctx context.Context,
	command workerCommand,
	job ingestion.Job,
	store connectorRunStore,
	fetch crossrefFetchFunc,
	runIngestion ingestionRunFunc,
) (ingestion.JobSummary, error) {
	query, err := crossrefQuery(command)
	if err != nil {
		return ingestion.JobSummary{}, err
	}
	claim, err := ingestion.NewConnectorClaim(
		source.Crossref,
		string(query.Stream),
		ingestion.WatermarkTimestamp,
		command.FromDate,
		command.ToDate.AddDate(0, 0, 1),
		job.IdempotencyKey,
	)
	if err != nil {
		return ingestion.JobSummary{}, err
	}
	run, err := store.Start(ctx, claim)
	if err != nil {
		return ingestion.JobSummary{}, err
	}

	records := fetch(
		ctx,
		query,
		func(
			pageContext context.Context,
			page crossref.PageReceipt,
		) error {
			receipt, receiptErr := ingestion.NewConnectorPageReceipt(
				run.ID,
				page.Ordinal,
				page.CursorIn,
				page.CursorOut,
				page.ContentSHA256,
				page.RecordCount,
			)
			if receiptErr != nil {
				return receiptErr
			}
			return store.RecordPage(pageContext, run, receipt)
		},
	)
	fetchFailed := false
	trackedRecords := func(yield func(source.Record, error) bool) {
		for record, recordErr := range records {
			if recordErr != nil {
				fetchFailed = true
			}
			if !yield(record, recordErr) {
				return
			}
		}
	}
	summary, err := runIngestion(
		ctx,
		job,
		recordEventsForCommand(
			command,
			source.Crossref,
			source.ClientSequence(trackedRecords),
		),
	)
	if err != nil {
		stage := "ingestion"
		code := "ingestion_failed"
		if fetchFailed {
			stage = "fetch"
			code = "crossref_fetch_failed"
		}
		return ingestion.JobSummary{}, errors.Join(
			err,
			failConnectorRun(ctx, store, run, stage, code),
		)
	}

	succeeded, err := run.Succeed()
	if err != nil {
		return ingestion.JobSummary{}, errors.Join(
			err,
			failConnectorRun(
				ctx,
				store,
				run,
				"watermark",
				"watermark_transition_failed",
			),
		)
	}
	if err := store.Succeed(ctx, succeeded); err != nil {
		return ingestion.JobSummary{}, errors.Join(
			err,
			failConnectorRun(
				ctx,
				store,
				run,
				"watermark",
				"watermark_advance_failed",
			),
		)
	}
	return summary, nil
}

func failConnectorRun(
	ctx context.Context,
	store connectorRunStore,
	run ingestion.ConnectorRun,
	stage string,
	code string,
) error {
	failed, err := run.Fail(stage, code)
	if err != nil {
		return err
	}
	return store.Fail(context.WithoutCancel(ctx), failed)
}

func syncSummaryResult(summary ingestion.JobSummary) map[string]any {
	return map[string]any{
		"job_id":       summary.JobID,
		"status":       summary.Status,
		"raw_inserted": summary.RawInserted,
		"raw_reused":   summary.RawReused,
		"projected":    summary.Projected,
		"excluded":     summary.Excluded,
		"deleted":      summary.Deleted,
		"unchanged":    summary.Unchanged,
		"failed":       summary.Failed,
	}
}

func runJCRImport(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Config,
	command workerCommand,
) (map[string]any, error) {
	if command.File != cfg.Venues.JCRImportPath {
		return nil, fmt.Errorf(
			"JCR --file %q must exactly match JCR_IMPORT_PATH",
			command.File,
		)
	}
	file, err := os.Open(command.File)
	if err != nil {
		return nil, fmt.Errorf("open JCR import file: %w", err)
	}
	defer file.Close()

	store, err := venue.NewPostgresJCRStore(
		pool,
		venue.PostgresJCRStoreConfig{
			SourceLicense: cfg.Venues.JCRSourceLicense,
		},
	)
	if err != nil {
		return nil, err
	}
	importer, err := venue.NewJCRImporter(store, store, time.Now)
	if err != nil {
		return nil, err
	}
	receipt, err := importer.Import(ctx, file)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"file_sha256":    receipt.FileSHA256(),
		"source":         receipt.Source(),
		"imported_at":    receipt.ImportedAt(),
		"input_rows":     receipt.InputRows(),
		"inserted_rows":  receipt.InsertedRows(),
		"unchanged_rows": receipt.UnchangedRows(),
	}, nil
}

func fetchRecords(
	ctx context.Context,
	cfg config.Config,
	command workerCommand,
) (source.ClientSequence, string, error) {
	switch command.Kind {
	case commandSyncOpenAlex:
		client, err := openalex.NewClient(
			http.DefaultClient,
			openalex.Config{
				BaseURL:          cfg.OpenAlex.Request.BaseURL,
				APIKey:           cfg.OpenAlex.APIKey,
				ContactEmail:     cfg.OpenAlex.ContactEmail,
				UserAgent:        "paper-research-hub/0.1 (+mailto:" + cfg.OpenAlex.ContactEmail + ")",
				PerPage:          min(cfg.OpenAlex.Request.BatchSize, 100),
				Timeout:          cfg.OpenAlex.Request.Timeout,
				RateLimit:        httpclient.RateLimit{Requests: 10, Interval: time.Second},
				MaxRetries:       cfg.OpenAlex.Request.MaxRetries,
				MaxWait:          cfg.OpenAlex.Request.MaxWait,
				InitialBackoff:   500 * time.Millisecond,
				MaxBackoff:       10 * time.Second,
				MaxResponseBytes: 32 << 20,
			},
			httpclient.Dependencies{},
		)
		if err != nil {
			return nil, "", err
		}
		return client.Fetch(ctx, source.Query{
			Search:     command.Query,
			Filter:     command.Filter,
			MaxResults: command.MaxResults,
		}), source.OpenAlex, nil

	case commandSyncPubMed:
		client, err := pubmed.NewClient(
			http.DefaultClient,
			pubmed.Config{
				BaseURL:          cfg.PubMed.Request.BaseURL,
				Tool:             cfg.PubMed.Tool,
				Email:            cfg.PubMed.Email,
				APIKey:           cfg.PubMed.APIKey,
				UserAgent:        "paper-research-hub/0.1 (+mailto:" + cfg.PubMed.Email + ")",
				BatchSize:        min(cfg.PubMed.Request.BatchSize, 10_000),
				Timeout:          cfg.PubMed.Request.Timeout,
				MaxRetries:       cfg.PubMed.Request.MaxRetries,
				MaxWait:          cfg.PubMed.Request.MaxWait,
				InitialBackoff:   500 * time.Millisecond,
				MaxBackoff:       10 * time.Second,
				MaxResponseBytes: 64 << 20,
			},
			httpclient.Dependencies{},
		)
		if err != nil {
			return nil, "", err
		}
		history, err := client.Search(ctx, pubmed.SearchQuery{
			Term:         command.Query,
			JournalISSNs: command.ISSNs,
			DateWindow: pubmed.DateWindow{
				From: command.FromDate,
				To:   command.ToDate,
			},
			MaxResults: command.MaxResults,
		})
		if err != nil {
			return nil, "", err
		}
		return client.Fetch(ctx, history), source.PubMed, nil

	case commandSyncCrossrefCreated, commandSyncCrossrefUpdated:
		client, err := crossref.NewClient(
			http.DefaultClient,
			crossref.Config{
				BaseURL:          cfg.Crossref.Request.BaseURL,
				ContactEmail:     cfg.Crossref.ContactEmail,
				UserAgent:        "paper-research-hub/0.1 (+mailto:" + cfg.Crossref.ContactEmail + ")",
				BatchSize:        min(cfg.Crossref.Request.BatchSize, 1000),
				Timeout:          cfg.Crossref.Request.Timeout,
				RateLimit:        httpclient.RateLimit{Requests: 10, Interval: time.Second},
				MaxRetries:       cfg.Crossref.Request.MaxRetries,
				MaxWait:          cfg.Crossref.Request.MaxWait,
				InitialBackoff:   500 * time.Millisecond,
				MaxBackoff:       10 * time.Second,
				MaxResponseBytes: 64 << 20,
			},
			httpclient.Dependencies{},
		)
		if err != nil {
			return nil, "", err
		}
		query, err := crossrefQuery(command)
		if err != nil {
			return nil, "", err
		}
		return client.Fetch(ctx, query), source.Crossref, nil

	default:
		return nil, "", fmt.Errorf("unsupported worker command kind %q", command.Kind)
	}
}

func crossrefQuery(command workerCommand) (crossref.Query, error) {
	var stream crossref.Stream
	switch command.Kind {
	case commandSyncCrossrefCreated:
		stream = crossref.StreamCreated
	case commandSyncCrossrefUpdated:
		stream = crossref.StreamUpdated
	default:
		return crossref.Query{}, fmt.Errorf(
			"unsupported Crossref command kind %q",
			command.Kind,
		)
	}
	return crossref.Query{
		Stream: stream,
		DateWindow: crossref.DateWindow{
			From: command.FromDate,
			To:   command.ToDate,
		},
		ISSNs:      append([]string(nil), command.ISSNs...),
		MaxResults: command.MaxResults,
	}, nil
}

func recordEvents(
	logicalSource string,
	records source.ClientSequence,
) ingestion.EventSequence {
	return recordEventsWithRevisionTime(
		logicalSource,
		records,
		recordRevisionTime,
	)
}

type recordRevisionTimeFunc func(source.Record) (time.Time, bool)

func recordEventsForCommand(
	command workerCommand,
	logicalSource string,
	records source.ClientSequence,
) ingestion.EventSequence {
	selector := recordRevisionTimeFunc(recordRevisionTime)
	switch command.Kind {
	case commandSyncCrossrefCreated:
		selector = func(record source.Record) (time.Time, bool) {
			return exactRecordTime(record.CreatedAt)
		}
	case commandSyncCrossrefUpdated:
		selector = func(record source.Record) (time.Time, bool) {
			return exactRecordTime(record.UpdatedAt)
		}
	}
	return recordEventsWithRevisionTime(logicalSource, records, selector)
}

func recordEventsWithRevisionTime(
	logicalSource string,
	records source.ClientSequence,
	revisionTime recordRevisionTimeFunc,
) ingestion.EventSequence {
	return func(yield func(ingestion.Event, error) bool) {
		if records == nil {
			yield(nil, errors.New("source record sequence is required"))
			return
		}
		if revisionTime == nil {
			yield(nil, errors.New("record revision-time selector is required"))
			return
		}
		position := int64(0)
		for record, err := range records {
			if err != nil {
				yield(nil, err)
				return
			}
			position++
			if strings.TrimSpace(record.Source) != logicalSource {
				yield(nil, fmt.Errorf(
					"record source %q conflicts with worker source %q",
					record.Source,
					logicalSource,
				))
				return
			}
			sourceTime, ok := revisionTime(record)
			if !ok {
				yield(nil, fmt.Errorf(
					"record %s:%s has no deterministic source revision time",
					logicalSource,
					record.SourceRecordID,
				))
				return
			}
			envelope, err := ingestion.NewEnvelope(
				logicalSource,
				logicalSource+":"+record.SourceRecordID,
				sourceTime,
				record.Raw.SHA256,
				position,
				record,
				record.Raw,
			)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(envelope, nil) {
				return
			}
		}
	}
}

func exactRecordTime(value *time.Time) (time.Time, bool) {
	if value == nil || value.IsZero() {
		return time.Time{}, false
	}
	return value.UTC(), true
}

func recordRevisionTime(record source.Record) (time.Time, bool) {
	for _, value := range []*time.Time{
		record.UpdatedAt,
		record.RevisedAt,
		record.CreatedAt,
		record.ElectronicPublishedAt,
		record.PublishedAt,
	} {
		if value != nil && !value.IsZero() {
			return value.UTC(), true
		}
	}
	return time.Time{}, false
}

func commandPayload(command workerCommand) map[string]any {
	payload := map[string]any{
		"kind":        command.Kind,
		"max_results": command.MaxResults,
	}
	if command.Query != "" {
		payload["query"] = command.Query
	}
	if command.Filter != "" {
		payload["filter"] = command.Filter
	}
	if !command.FromDate.IsZero() {
		payload["from_date"] = command.FromDate.Format(time.DateOnly)
		payload["to_date"] = command.ToDate.Format(time.DateOnly)
	}
	if len(command.ISSNs) > 0 {
		payload["issns"] = append([]string(nil), command.ISSNs...)
	}
	return payload
}

func commandIdempotencyKey(
	logicalSource string,
	payload map[string]any,
) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode worker idempotency payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return logicalSource + ":" + hex.EncodeToString(digest[:]), nil
}

var _ iter.Seq2[ingestion.Event, error] = recordEvents("", nil)
