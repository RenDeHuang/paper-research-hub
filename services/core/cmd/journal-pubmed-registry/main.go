package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venueenrich"
)

const (
	journalPubMedRegistryRows                = 2032
	journalPubMedRegistryReportSchemaVersion = "journal-pubmed-registry-report/v1"
	journalPubMedRegistryUserAgent           = "paper-research-hub-journal-pubmed-registry/1.0"
	officialCrossrefBaseURL                  = "https://api.crossref.org"
	officialPubMedBaseURL                    = "https://eutils.ncbi.nlm.nih.gov"

	pubMedRegistryTimeout          = 30 * time.Second
	pubMedRegistryMaxRetries       = 3
	pubMedRegistryMaxWait          = 30 * time.Second
	pubMedRegistryInitialBackoff   = 500 * time.Millisecond
	pubMedRegistryMaxBackoff       = 8 * time.Second
	pubMedRegistryMaxResponseBytes = 4 << 20

	crossrefRegistryTimeout          = 45 * time.Second
	crossrefRegistryMaxRetries       = 3
	crossrefRegistryMaxWait          = 45 * time.Second
	crossrefRegistryInitialBackoff   = 500 * time.Millisecond
	crossrefRegistryMaxBackoff       = 8 * time.Second
	crossrefRegistryMaxResponseBytes = 32 << 20

	registryPublishOutput = "output_csv"
	registryPublishReport = "report_json"

	registryCaseProbePattern = ".journal-registry-case-probe-*"
)

var registryCSVHeader = []string{
	"domain",
	"source_order",
	"source_journal_name",
	"impact_factor",
	"jcr_value",
	"cass_value",
	"issn_l",
	"print_issn",
	"eissn",
	"all_issns",
	"crossref_publisher",
	"resolution_status",
	"pubmed_supported",
	"pubmed_record_count",
	"pubmed_checked_at",
	"source_url",
	"verification_status",
}

type envLookup func(string) (string, bool)

type registryCommand struct {
	Inputs               []string
	CacheDir             string
	OutputPath           string
	ReportPath           string
	AuditPubMedCoverage  bool
	NCBITool             string
	NCBIEmail            string
	NCBIAPIKey           string
	CrossrefContactEmail string
}

type registryResult struct {
	Rows          int
	PubMedUnknown int
}

type registryRun func(
	context.Context,
	registryCommand,
) (registryResult, error)

type fetchCrossrefFunc func(
	context.Context,
	*http.Client,
	venueenrich.CrossrefCatalogConfig,
	httpclient.Dependencies,
) (venueenrich.CrossrefCatalog, error)

type matchCrossrefFunc func(
	[]venueenrich.SourceRow,
	venueenrich.CrossrefCatalog,
) ([]venueenrich.RegistryRow, error)

type newPubMedCounterFunc func(
	*http.Client,
	pubmed.Config,
	httpclient.Dependencies,
) (venueenrich.PubMedCoverageCounter, error)

type registryDependencies struct {
	httpClient       *http.Client
	now              func() time.Time
	httpDependencies httpclient.Dependencies
	crossrefBaseURL  string
	pubMedBaseURL    string
	fetchCrossref    fetchCrossrefFunc
	matchCrossref    matchCrossrefFunc
	newPubMedCounter newPubMedCounterFunc
	fileOps          registryFileOps
}

type registryRunner struct {
	dependencies registryDependencies
}

type registryInputReceipt struct {
	Ordinal int    `json:"ordinal"`
	Path    string `json:"path"`
	Rows    int    `json:"rows"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
}

type registryCrossrefReceipt struct {
	ManifestVersion string `json:"manifest_version"`
	Source          string `json:"source"`
	FetchedAt       string `json:"fetched_at"`
	Hash            string `json:"hash"`
	Bytes           int64  `json:"bytes"`
	Records         int64  `json:"records"`
	Replayed        bool   `json:"replayed"`
	Resumed         bool   `json:"resumed"`
}

type registryMatchCounts struct {
	Resolved   int `json:"resolved"`
	Ambiguous  int `json:"ambiguous"`
	Unresolved int `json:"unresolved"`
}

type registryProbeCounts struct {
	Eligible  int `json:"eligible"`
	Attempted int `json:"attempted"`
}

type registrySupportCounts struct {
	Yes     int `json:"yes"`
	No      int `json:"no"`
	Unknown int `json:"unknown"`
}

type registryCounts struct {
	InputRows  int                   `json:"input_rows"`
	OutputRows int                   `json:"output_rows"`
	Match      registryMatchCounts   `json:"match"`
	Probes     registryProbeCounts   `json:"probes"`
	PubMed     registrySupportCounts `json:"pubmed"`
}

type registryDomainCounts struct {
	Domain string         `json:"domain"`
	Counts registryCounts `json:"counts"`
}

type registryOutputReceipt struct {
	Path   string `json:"path"`
	Rows   int    `json:"rows"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type registryReport struct {
	SchemaVersion   string                           `json:"schema_version"`
	GeneratedAt     string                           `json:"generated_at"`
	Inputs          []registryInputReceipt           `json:"inputs"`
	CrossrefCatalog registryCrossrefReceipt          `json:"crossref_catalog"`
	Counts          registryCounts                   `json:"counts"`
	ByDomain        []registryDomainCounts           `json:"by_domain"`
	Probes          []venueenrich.PubMedProbeReceipt `json:"probes"`
	OutputCSV       registryOutputReceipt            `json:"output_csv"`
}

type registryFileOps struct {
	before  func(string) error
	link    func(string, string) error
	remove  func(string) error
	syncDir func(string) error
}

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	runner := registryRunner{dependencies: defaultRegistryDependencies()}
	os.Exit(realMain(
		ctx,
		os.Args[1:],
		os.Stdout,
		os.Stderr,
		os.LookupEnv,
		runner.run,
	))
}

func realMain(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookup envLookup,
	run registryRun,
) int {
	command, err := parseRegistryCommand(args)
	if err != nil {
		fmt.Fprintf(
			stderr,
			"parse journal PubMed registry arguments: %v\n",
			err,
		)
		return 2
	}
	command, err = loadRegistrySourceConfig(command, lookup)
	if err != nil {
		fmt.Fprintf(
			stderr,
			"load journal PubMed registry configuration: %v\n",
			err,
		)
		return 1
	}
	if run == nil {
		fmt.Fprintln(stderr, "run journal PubMed registry: runner is required")
		return 1
	}
	result, err := run(ctx, command)
	if err != nil {
		message := redactRegistrySecret(err.Error(), command.NCBIAPIKey)
		fmt.Fprintf(stderr, "run journal PubMed registry: %s\n", message)
		return 1
	}
	fmt.Fprintf(
		stdout,
		"generated journal PubMed registry rows=%d unknown=%d\n",
		result.Rows,
		result.PubMedUnknown,
	)
	return 0
}

func parseRegistryCommand(args []string) (registryCommand, error) {
	set := flag.NewFlagSet("journal-pubmed-registry", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command registryCommand
	var inputs repeatedStringFlag
	set.Var(&inputs, "input", "source CSV; repeat exactly three times")
	set.StringVar(&command.CacheDir, "cache-dir", "", "Crossref catalog cache directory")
	set.StringVar(&command.OutputPath, "output", "", "output registry CSV")
	set.StringVar(&command.ReportPath, "report", "", "output report JSON")
	set.BoolVar(
		&command.AuditPubMedCoverage,
		"audit-pubmed-coverage",
		false,
		"probe historical PubMed coverage for resolved journals",
	)
	if err := set.Parse(args); err != nil {
		return registryCommand{}, err
	}
	if set.NArg() != 0 {
		return registryCommand{}, fmt.Errorf(
			"unexpected arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}
	if len(inputs) != 3 {
		return registryCommand{}, fmt.Errorf(
			"--input must be provided exactly 3 times; got %d",
			len(inputs),
		)
	}
	command.Inputs = slices.Clone(inputs)
	for index, path := range command.Inputs {
		if err := validateExplicitPath(
			fmt.Sprintf("--input occurrence %d", index+1),
			path,
		); err != nil {
			return registryCommand{}, err
		}
	}
	for name, path := range map[string]string{
		"--cache-dir": command.CacheDir,
		"--output":    command.OutputPath,
		"--report":    command.ReportPath,
	} {
		if err := validateExplicitPath(name, path); err != nil {
			return registryCommand{}, err
		}
	}
	if command.OutputPath == command.ReportPath {
		return registryCommand{}, errors.New(
			"--output and --report must reference different paths",
		)
	}
	return command, nil
}

func loadRegistrySourceConfig(
	command registryCommand,
	lookup envLookup,
) (registryCommand, error) {
	if lookup == nil {
		return registryCommand{}, errors.New("environment lookup is required")
	}
	crossrefEmail := ""
	if command.AuditPubMedCoverage {
		tool, err := requiredTrimmedEnvironment(lookup, "NCBI_TOOL")
		if err != nil {
			return registryCommand{}, err
		}
		email, err := requiredBareEmailEnvironment(lookup, "NCBI_EMAIL")
		if err != nil {
			return registryCommand{}, err
		}
		apiKey := ""
		if raw, ok := lookup("NCBI_API_KEY"); ok {
			apiKey = strings.TrimSpace(raw)
		}
		command.NCBITool = tool
		command.NCBIEmail = email
		command.NCBIAPIKey = apiKey
		crossrefEmail = email
	}
	if raw, ok := lookup("CROSSREF_CONTACT_EMAIL"); ok && raw != "" {
		var err error
		crossrefEmail, err = validateBareEmail(
			"CROSSREF_CONTACT_EMAIL",
			raw,
		)
		if err != nil {
			return registryCommand{}, err
		}
	}
	command.CrossrefContactEmail = crossrefEmail
	return command, nil
}

func defaultRegistryDependencies() registryDependencies {
	fileOps := defaultRegistryFileOps()
	return registryDependencies{
		httpClient:      &http.Client{},
		now:             time.Now,
		crossrefBaseURL: officialCrossrefBaseURL,
		pubMedBaseURL:   officialPubMedBaseURL,
		fetchCrossref:   venueenrich.FetchCrossrefCatalog,
		matchCrossref:   venueenrich.MatchCrossrefCatalog,
		newPubMedCounter: func(
			base *http.Client,
			config pubmed.Config,
			dependencies httpclient.Dependencies,
		) (venueenrich.PubMedCoverageCounter, error) {
			return pubmed.NewClient(base, config, dependencies)
		},
		fileOps: fileOps,
	}
}

func (runner registryRunner) run(
	ctx context.Context,
	command registryCommand,
) (registryResult, error) {
	if ctx == nil {
		return registryResult{}, errors.New("registry context is required")
	}
	if err := ctx.Err(); err != nil {
		return registryResult{}, err
	}
	if len(command.Inputs) != 3 {
		return registryResult{}, errors.New(
			"registry run requires exactly three input files",
		)
	}
	if err := validateDistinctRegistryPaths(
		command.OutputPath,
		command.ReportPath,
	); err != nil {
		return registryResult{}, err
	}
	if err := validateRegistryAuditIdentity(command); err != nil {
		return registryResult{}, err
	}
	dependencies, err := runner.dependencies.validated(
		command.AuditPubMedCoverage,
	)
	if err != nil {
		return registryResult{}, err
	}

	sourceRows, inputs, err := loadRegistryInputs(command.Inputs, os.ReadFile)
	if err != nil {
		return registryResult{}, err
	}
	if len(sourceRows) != journalPubMedRegistryRows {
		return registryResult{}, fmt.Errorf(
			"source logical row count = %d, require exactly %d",
			len(sourceRows),
			journalPubMedRegistryRows,
		)
	}
	if err := ctx.Err(); err != nil {
		return registryResult{}, err
	}

	httpDependencies := dependencies.httpDependencies
	if httpDependencies.Now == nil {
		httpDependencies.Now = dependencies.now
	}
	catalog, err := dependencies.fetchCrossref(
		ctx,
		dependencies.httpClient,
		crossrefCatalogConfig(command, dependencies.crossrefBaseURL),
		httpDependencies,
	)
	if err != nil {
		return registryResult{}, fmt.Errorf("fetch Crossref catalog: %w", err)
	}
	rows, err := dependencies.matchCrossref(sourceRows, catalog)
	if err != nil {
		return registryResult{}, fmt.Errorf("match Crossref catalog: %w", err)
	}
	if len(rows) != len(sourceRows) {
		return registryResult{}, fmt.Errorf(
			"Crossref match rows = %d, source rows = %d",
			len(rows),
			len(sourceRows),
		)
	}

	var probes []venueenrich.PubMedProbeReceipt
	if command.AuditPubMedCoverage {
		counter, err := dependencies.newPubMedCounter(
			dependencies.httpClient,
			pubMedClientConfig(command, dependencies.pubMedBaseURL),
			httpDependencies,
		)
		if err != nil {
			return registryResult{}, fmt.Errorf(
				"create PubMed coverage client: %w",
				err,
			)
		}
		rows, probes, err = venueenrich.ProbePubMedCoverage(
			ctx,
			rows,
			counter,
			dependencies.now,
		)
		if err != nil {
			return registryResult{}, fmt.Errorf(
				"probe PubMed coverage: %w",
				err,
			)
		}
	} else {
		rows, probes, err = overlaySkippedPubMedCoverage(rows)
		if err != nil {
			return registryResult{}, err
		}
	}
	if len(rows) != journalPubMedRegistryRows ||
		len(probes) != journalPubMedRegistryRows {
		return registryResult{}, fmt.Errorf(
			"PubMed probe reconciliation failed: rows=%d probes=%d require=%d",
			len(rows),
			len(probes),
			journalPubMedRegistryRows,
		)
	}

	csvBytes, err := encodeRegistryCSV(rows, probes)
	if err != nil {
		return registryResult{}, err
	}
	output := registryOutputReceipt{
		Path:   command.OutputPath,
		Rows:   len(rows),
		Bytes:  int64(len(csvBytes)),
		SHA256: sha256Hex(csvBytes),
	}
	generatedAt := dependencies.now().UTC()
	if generatedAt.IsZero() {
		return registryResult{}, errors.New(
			"registry clock returned a zero generated_at",
		)
	}
	report, err := buildRegistryReport(
		generatedAt,
		inputs,
		catalog,
		rows,
		probes,
		output,
	)
	if err != nil {
		return registryResult{}, err
	}
	reportBytes, err := encodeRegistryReport(report)
	if err != nil {
		return registryResult{}, err
	}
	if err := reconcileRegistryArtifacts(csvBytes, reportBytes); err != nil {
		return registryResult{}, err
	}
	if err := publishRegistryArtifacts(
		command.OutputPath,
		csvBytes,
		command.ReportPath,
		reportBytes,
		dependencies.fileOps,
	); err != nil {
		return registryResult{}, fmt.Errorf("publish registry artifacts: %w", err)
	}
	return registryResult{
		Rows:          report.Counts.OutputRows,
		PubMedUnknown: report.Counts.PubMed.Unknown,
	}, nil
}

func validateRegistryAuditIdentity(command registryCommand) error {
	if !command.AuditPubMedCoverage {
		return nil
	}
	if _, err := validateRequiredTrimmed(
		"NCBI_TOOL",
		command.NCBITool,
	); err != nil {
		return err
	}
	if _, err := validateRequiredBareEmail(
		"NCBI_EMAIL",
		command.NCBIEmail,
	); err != nil {
		return err
	}
	return nil
}

func (dependencies registryDependencies) validated(
	auditPubMedCoverage bool,
) (registryDependencies, error) {
	if dependencies.httpClient == nil {
		return registryDependencies{}, errors.New(
			"registry base HTTP client is required",
		)
	}
	if dependencies.now == nil {
		return registryDependencies{}, errors.New("registry clock is required")
	}
	if strings.TrimSpace(dependencies.crossrefBaseURL) == "" {
		return registryDependencies{}, errors.New(
			"registry Crossref base URL is required",
		)
	}
	if dependencies.fetchCrossref == nil ||
		dependencies.matchCrossref == nil {
		return registryDependencies{}, errors.New(
			"registry Crossref source dependencies are required",
		)
	}
	if auditPubMedCoverage {
		if strings.TrimSpace(dependencies.pubMedBaseURL) == "" {
			return registryDependencies{}, errors.New(
				"registry PubMed base URL is required for coverage audit",
			)
		}
		if dependencies.newPubMedCounter == nil {
			return registryDependencies{}, errors.New(
				"registry PubMed counter dependency is required for coverage audit",
			)
		}
	}
	defaultOps := defaultRegistryFileOps()
	if dependencies.fileOps.before == nil {
		dependencies.fileOps.before = defaultOps.before
	}
	if dependencies.fileOps.link == nil {
		dependencies.fileOps.link = defaultOps.link
	}
	if dependencies.fileOps.remove == nil {
		dependencies.fileOps.remove = defaultOps.remove
	}
	if dependencies.fileOps.syncDir == nil {
		dependencies.fileOps.syncDir = defaultOps.syncDir
	}
	return dependencies, nil
}

func overlaySkippedPubMedCoverage(
	rows []venueenrich.RegistryRow,
) ([]venueenrich.RegistryRow, []venueenrich.PubMedProbeReceipt, error) {
	overlaid := make([]venueenrich.RegistryRow, len(rows))
	probes := make([]venueenrich.PubMedProbeReceipt, len(rows))
	for index, row := range rows {
		if err := row.Validate(); err != nil {
			return nil, nil, fmt.Errorf(
				"row %d: invalid registry row before skipped PubMed coverage audit: %w",
				index+1,
				err,
			)
		}
		issns, err := canonicalRegistryISSNs(row.AllISSNs)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"row %d: canonicalize PubMed coverage ISSNs: %w",
				index+1,
				err,
			)
		}
		if row.MatchStatus == venueenrich.MatchStatusResolved &&
			len(issns) == 0 {
			return nil, nil, fmt.Errorf(
				"row %d: resolved registry row requires at least one valid ISSN",
				index+1,
			)
		}
		row.AllISSNs = issns
		row.PubMedSupported = venueenrich.SupportStatusUnknown
		row.PubMedRecordCount = 0
		if err := row.Validate(); err != nil {
			return nil, nil, fmt.Errorf(
				"row %d: invalid skipped PubMed coverage result: %w",
				index+1,
				err,
			)
		}
		overlaid[index] = row
		probes[index] = venueenrich.PubMedProbeReceipt{
			Domain:      row.Domain,
			SourceOrder: row.SourceOrder,
			ISSNs:       slices.Clone(row.AllISSNs),
			Status:      venueenrich.SupportStatusUnknown,
		}
	}
	return overlaid, probes, nil
}

func canonicalRegistryISSNs(values []string) ([]string, error) {
	canonical := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, raw := range values {
		parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, raw)
		if err != nil {
			return nil, fmt.Errorf("all_issns[%d] %q: %w", index, raw, err)
		}
		value := parsed.String()
		if value != raw {
			return nil, fmt.Errorf(
				"all_issns[%d] %q must use canonical form %q",
				index,
				raw,
				value,
			)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		canonical = append(canonical, value)
	}
	slices.Sort(canonical)
	return canonical, nil
}

func crossrefCatalogConfig(
	command registryCommand,
	baseURL string,
) venueenrich.CrossrefCatalogConfig {
	return venueenrich.CrossrefCatalogConfig{
		BaseURL:      baseURL,
		CacheDir:     command.CacheDir,
		ContactEmail: command.CrossrefContactEmail,
		UserAgent:    journalPubMedRegistryUserAgent,
		Timeout:      crossrefRegistryTimeout,
		RateLimit: httpclient.RateLimit{
			Requests: 5,
			Interval: time.Second,
		},
		MaxRetries:       crossrefRegistryMaxRetries,
		MaxWait:          crossrefRegistryMaxWait,
		InitialBackoff:   crossrefRegistryInitialBackoff,
		MaxBackoff:       crossrefRegistryMaxBackoff,
		MaxResponseBytes: crossrefRegistryMaxResponseBytes,
	}
}

func pubMedClientConfig(
	command registryCommand,
	baseURL string,
) pubmed.Config {
	return pubmed.Config{
		BaseURL:          baseURL,
		Tool:             command.NCBITool,
		Email:            command.NCBIEmail,
		APIKey:           command.NCBIAPIKey,
		UserAgent:        journalPubMedRegistryUserAgent,
		BatchSize:        1000,
		Timeout:          pubMedRegistryTimeout,
		MaxRetries:       pubMedRegistryMaxRetries,
		MaxWait:          pubMedRegistryMaxWait,
		InitialBackoff:   pubMedRegistryInitialBackoff,
		MaxBackoff:       pubMedRegistryMaxBackoff,
		MaxResponseBytes: pubMedRegistryMaxResponseBytes,
	}
}

func loadRegistryInputs(
	paths []string,
	readFile func(string) ([]byte, error),
) ([]venueenrich.SourceRow, []registryInputReceipt, error) {
	if len(paths) != 3 {
		return nil, nil, errors.New(
			"registry input metadata requires exactly three paths",
		)
	}
	if readFile == nil {
		return nil, nil, errors.New("registry input reader is required")
	}
	receipts := make([]registryInputReceipt, len(paths))
	payloads := make([]venueenrich.SourcePayload, len(paths))
	for index, path := range paths {
		payload, err := readFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"input %d %q: read raw bytes: %w",
				index+1,
				path,
				err,
			)
		}
		payloads[index] = venueenrich.SourcePayload{
			Path: path,
			Data: payload,
		}
		receipts[index] = registryInputReceipt{
			Ordinal: index + 1,
			Path:    path,
			Bytes:   int64(len(payload)),
			SHA256:  sha256Hex(payload),
		}
	}
	rows, rowCounts, err := venueenrich.LoadSourcePayloads(payloads)
	if err != nil {
		return nil, nil, fmt.Errorf("load three immutable source payloads: %w", err)
	}
	if len(rowCounts) != len(receipts) {
		return nil, nil, errors.New(
			"input logical row counts do not match immutable payload count",
		)
	}
	totalRows := 0
	for index, rowCount := range rowCounts {
		receipts[index].Rows = rowCount
		totalRows += rowCount
	}
	if totalRows != len(rows) {
		return nil, nil, errors.New(
			"input logical row counts do not reconcile with immutable payloads",
		)
	}
	return rows, receipts, nil
}

func encodeRegistryCSV(
	rows []venueenrich.RegistryRow,
	probes []venueenrich.PubMedProbeReceipt,
) ([]byte, error) {
	if len(rows) != len(probes) {
		return nil, fmt.Errorf(
			"CSV rows=%d and probes=%d must match",
			len(rows),
			len(probes),
		)
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(registryCSVHeader); err != nil {
		return nil, fmt.Errorf("encode registry CSV header: %w", err)
	}
	for index, row := range rows {
		if err := row.Validate(); err != nil {
			return nil, fmt.Errorf(
				"encode registry CSV row %d: invalid row: %w",
				index+1,
				err,
			)
		}
		probe := probes[index]
		if err := validateProbeForRow(row, probe); err != nil {
			return nil, fmt.Errorf(
				"encode registry CSV row %d: %w",
				index+1,
				err,
			)
		}
		allISSNs, err := compactISSNJSON(row.AllISSNs)
		if err != nil {
			return nil, fmt.Errorf(
				"encode registry CSV row %d all_issns: %w",
				index+1,
				err,
			)
		}
		record := []string{
			row.Domain,
			strconv.Itoa(row.SourceOrder),
			row.SourceJournalName,
			row.ImpactFactor,
			row.JCRValue,
			row.CASSValue,
			row.ISSNL,
			row.PrintISSN,
			row.EISSN,
			allISSNs,
			row.CrossrefPublisher,
			string(row.MatchStatus),
			string(row.PubMedSupported),
			strconv.FormatInt(row.PubMedRecordCount, 10),
			probe.CheckedAt,
			row.SourceURL,
			venueenrich.VerificationStatusPendingClarivate,
		}
		if err := writer.Write(record); err != nil {
			return nil, fmt.Errorf(
				"encode registry CSV row %d: %w",
				index+1,
				err,
			)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("flush registry CSV: %w", err)
	}
	payload := buffer.Bytes()
	if err := validateRegistryCSVBytes(payload, len(rows)); err != nil {
		return nil, err
	}
	return slices.Clone(payload), nil
}

func validateRegistryCSVBytes(payload []byte, expectedRows int) error {
	reader := csv.NewReader(bytes.NewReader(payload))
	records, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("validate encoded registry CSV: %w", err)
	}
	if len(records) != expectedRows+1 {
		return fmt.Errorf(
			"encoded registry CSV records=%d, require header plus %d rows",
			len(records),
			expectedRows,
		)
	}
	if !slices.Equal(records[0], registryCSVHeader) {
		return errors.New("encoded registry CSV header changed")
	}
	for index, record := range records[1:] {
		if len(record) != len(registryCSVHeader) {
			return fmt.Errorf(
				"encoded registry CSV row %d fields=%d, require=%d",
				index+1,
				len(record),
				len(registryCSVHeader),
			)
		}
	}
	return nil
}

func compactISSNJSON(values []string) (string, error) {
	canonical := slices.Clone(values)
	if canonical == nil {
		canonical = []string{}
	}
	if !slices.IsSorted(canonical) {
		return "", errors.New("ISSNs must use stable sorted order")
	}
	for index := 1; index < len(canonical); index++ {
		if canonical[index] == canonical[index-1] {
			return "", fmt.Errorf("duplicate ISSN %q", canonical[index])
		}
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func buildRegistryReport(
	generatedAt time.Time,
	inputs []registryInputReceipt,
	catalog venueenrich.CrossrefCatalog,
	rows []venueenrich.RegistryRow,
	probes []venueenrich.PubMedProbeReceipt,
	output registryOutputReceipt,
) (registryReport, error) {
	counts, byDomain, err := countRegistryRows(rows, probes)
	if err != nil {
		return registryReport{}, err
	}
	report := registryReport{
		SchemaVersion: journalPubMedRegistryReportSchemaVersion,
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339Nano),
		Inputs:        slices.Clone(inputs),
		CrossrefCatalog: registryCrossrefReceipt{
			ManifestVersion: catalog.Manifest.SchemaVersion,
			Source:          catalog.Manifest.SourceURL,
			FetchedAt: catalog.Manifest.FetchedAt.UTC().Format(
				time.RFC3339Nano,
			),
			Hash:     catalog.Manifest.CatalogSHA256,
			Bytes:    catalog.Manifest.CatalogBytes,
			Records:  catalog.Manifest.RecordCount,
			Replayed: catalog.Replayed,
			Resumed:  catalog.Resumed,
		},
		Counts:    counts,
		ByDomain:  byDomain,
		Probes:    slices.Clone(probes),
		OutputCSV: output,
	}
	if err := validateRegistryReport(report); err != nil {
		return registryReport{}, err
	}
	return report, nil
}

func countRegistryRows(
	rows []venueenrich.RegistryRow,
	probes []venueenrich.PubMedProbeReceipt,
) (registryCounts, []registryDomainCounts, error) {
	if len(rows) != len(probes) {
		return registryCounts{}, nil, errors.New(
			"report rows and probes must have equal length",
		)
	}
	counts := registryCounts{
		InputRows:  len(rows),
		OutputRows: len(rows),
	}
	byDomain := make([]registryDomainCounts, 0)
	domainIndexes := make(map[string]int)
	for index, row := range rows {
		if err := validateProbeForRow(row, probes[index]); err != nil {
			return registryCounts{}, nil, fmt.Errorf(
				"report row %d: %w",
				index+1,
				err,
			)
		}
		domainIndex, exists := domainIndexes[row.Domain]
		if !exists {
			domainIndex = len(byDomain)
			domainIndexes[row.Domain] = domainIndex
			byDomain = append(byDomain, registryDomainCounts{
				Domain: row.Domain,
			})
		}
		domain := &byDomain[domainIndex].Counts
		domain.InputRows++
		domain.OutputRows++
		incrementRegistryCounts(&counts, row, probes[index])
		incrementRegistryCounts(domain, row, probes[index])
	}
	if err := validateRegistryCounts(counts, byDomain); err != nil {
		return registryCounts{}, nil, err
	}
	return counts, byDomain, nil
}

func incrementRegistryCounts(
	counts *registryCounts,
	row venueenrich.RegistryRow,
	probe venueenrich.PubMedProbeReceipt,
) {
	switch row.MatchStatus {
	case venueenrich.MatchStatusResolved:
		counts.Match.Resolved++
		counts.Probes.Eligible++
	case venueenrich.MatchStatusAmbiguous:
		counts.Match.Ambiguous++
	case venueenrich.MatchStatusUnresolved:
		counts.Match.Unresolved++
	}
	if probe.Attempted {
		counts.Probes.Attempted++
	}
	switch row.PubMedSupported {
	case venueenrich.SupportStatusYes:
		counts.PubMed.Yes++
	case venueenrich.SupportStatusNo:
		counts.PubMed.No++
	case venueenrich.SupportStatusUnknown:
		counts.PubMed.Unknown++
	}
}

func validateRegistryReport(report registryReport) error {
	if report.SchemaVersion != journalPubMedRegistryReportSchemaVersion {
		return errors.New("registry report schema_version changed")
	}
	if _, err := time.Parse(time.RFC3339Nano, report.GeneratedAt); err != nil {
		return fmt.Errorf("registry report generated_at: %w", err)
	}
	if len(report.Inputs) != 3 {
		return errors.New("registry report requires exactly three inputs")
	}
	inputRows := 0
	for index, input := range report.Inputs {
		if input.Ordinal != index+1 ||
			strings.TrimSpace(input.Path) == "" ||
			input.Rows < 0 ||
			input.Bytes < 0 ||
			!validSHA256(input.SHA256) {
			return fmt.Errorf(
				"registry report input %d is invalid",
				index+1,
			)
		}
		inputRows += input.Rows
	}
	if inputRows != report.Counts.InputRows {
		return fmt.Errorf(
			"registry report input rows=%d counts.input_rows=%d",
			inputRows,
			report.Counts.InputRows,
		)
	}
	if strings.TrimSpace(report.CrossrefCatalog.ManifestVersion) == "" ||
		strings.TrimSpace(report.CrossrefCatalog.Source) == "" ||
		!validSHA256(report.CrossrefCatalog.Hash) ||
		report.CrossrefCatalog.Bytes < 0 ||
		report.CrossrefCatalog.Records < 0 {
		return errors.New("registry report Crossref catalog receipt is invalid")
	}
	if _, err := time.Parse(
		time.RFC3339Nano,
		report.CrossrefCatalog.FetchedAt,
	); err != nil {
		return fmt.Errorf("registry report Crossref fetched_at: %w", err)
	}
	if err := validateRegistryCounts(
		report.Counts,
		report.ByDomain,
	); err != nil {
		return err
	}
	if len(report.Probes) != report.Counts.OutputRows {
		return fmt.Errorf(
			"registry report probes=%d output_rows=%d",
			len(report.Probes),
			report.Counts.OutputRows,
		)
	}
	var attempted int
	var support registrySupportCounts
	for index, probe := range report.Probes {
		if err := validateReportProbe(probe); err != nil {
			return fmt.Errorf(
				"registry report probe %d: %w",
				index+1,
				err,
			)
		}
		if probe.Attempted {
			attempted++
		}
		switch probe.Status {
		case venueenrich.SupportStatusYes:
			support.Yes++
		case venueenrich.SupportStatusNo:
			support.No++
		case venueenrich.SupportStatusUnknown:
			support.Unknown++
		}
	}
	if attempted != report.Counts.Probes.Attempted ||
		support != report.Counts.PubMed {
		return errors.New(
			"registry report probe receipts do not reconcile with counts",
		)
	}
	if strings.TrimSpace(report.OutputCSV.Path) == "" ||
		report.OutputCSV.Rows != report.Counts.OutputRows ||
		report.OutputCSV.Bytes < 0 ||
		!validSHA256(report.OutputCSV.SHA256) {
		return errors.New("registry report output_csv receipt is invalid")
	}
	return nil
}

func validateRegistryCounts(
	counts registryCounts,
	byDomain []registryDomainCounts,
) error {
	if counts.InputRows != journalPubMedRegistryRows ||
		counts.OutputRows != journalPubMedRegistryRows {
		return fmt.Errorf(
			"registry input_rows and output_rows must each require exactly %d",
			journalPubMedRegistryRows,
		)
	}
	if counts.InputRows != counts.OutputRows {
		return errors.New("registry input_rows and output_rows must match")
	}
	if counts.Match.Resolved+
		counts.Match.Ambiguous+
		counts.Match.Unresolved != counts.InputRows {
		return errors.New("registry match counts do not reconcile")
	}
	if counts.Probes.Eligible != counts.Match.Resolved ||
		(counts.Probes.Attempted != 0 &&
			counts.Probes.Attempted != counts.Probes.Eligible) {
		return errors.New("registry probe counts do not reconcile")
	}
	if counts.PubMed.Yes+
		counts.PubMed.No+
		counts.PubMed.Unknown != counts.OutputRows {
		return errors.New("registry PubMed counts do not reconcile")
	}
	skippedCoverageAudit := counts.Probes.Attempted == 0
	if skippedCoverageAudit &&
		(counts.PubMed.Yes != 0 ||
			counts.PubMed.No != 0 ||
			counts.PubMed.Unknown != counts.OutputRows) {
		return errors.New(
			"registry skipped probe counts require all PubMed statuses unknown",
		)
	}
	var aggregate registryCounts
	for _, domain := range byDomain {
		if strings.TrimSpace(domain.Domain) == "" {
			return errors.New("registry by_domain requires non-empty domain")
		}
		domainCounts := domain.Counts
		expectedAttempts := domainCounts.Probes.Eligible
		if skippedCoverageAudit {
			expectedAttempts = 0
		}
		if domainCounts.InputRows != domainCounts.OutputRows ||
			domainCounts.Match.Resolved+
				domainCounts.Match.Ambiguous+
				domainCounts.Match.Unresolved != domainCounts.InputRows ||
			domainCounts.Probes.Eligible != domainCounts.Match.Resolved ||
			domainCounts.Probes.Attempted != expectedAttempts ||
			domainCounts.PubMed.Yes+
				domainCounts.PubMed.No+
				domainCounts.PubMed.Unknown != domainCounts.OutputRows {
			return fmt.Errorf(
				"registry by_domain %q counts do not reconcile",
				domain.Domain,
			)
		}
		if skippedCoverageAudit &&
			(domainCounts.PubMed.Yes != 0 ||
				domainCounts.PubMed.No != 0 ||
				domainCounts.PubMed.Unknown != domainCounts.OutputRows) {
			return fmt.Errorf(
				"registry by_domain %q skipped probes require all PubMed statuses unknown",
				domain.Domain,
			)
		}
		aggregate.InputRows += domainCounts.InputRows
		aggregate.OutputRows += domainCounts.OutputRows
		aggregate.Match.Resolved += domainCounts.Match.Resolved
		aggregate.Match.Ambiguous += domainCounts.Match.Ambiguous
		aggregate.Match.Unresolved += domainCounts.Match.Unresolved
		aggregate.Probes.Eligible += domainCounts.Probes.Eligible
		aggregate.Probes.Attempted += domainCounts.Probes.Attempted
		aggregate.PubMed.Yes += domainCounts.PubMed.Yes
		aggregate.PubMed.No += domainCounts.PubMed.No
		aggregate.PubMed.Unknown += domainCounts.PubMed.Unknown
	}
	if aggregate != counts {
		return errors.New("registry by_domain totals do not match top-level counts")
	}
	return nil
}

func validateReportProbe(probe venueenrich.PubMedProbeReceipt) error {
	if probe.Domain == "" || probe.Domain != strings.TrimSpace(probe.Domain) {
		return errors.New("probe domain must be non-empty and trimmed")
	}
	if probe.SourceOrder <= 0 {
		return errors.New("probe source_order must be positive")
	}
	if !probe.Status.Valid() {
		return errors.New("probe status is invalid")
	}
	if probe.RecordCount < 0 {
		return errors.New("probe record_count must not be negative")
	}
	if !slices.IsSorted(probe.ISSNs) {
		return errors.New("probe ISSNs must use stable sorted order")
	}
	for index, raw := range probe.ISSNs {
		if index > 0 && raw == probe.ISSNs[index-1] {
			return fmt.Errorf("probe ISSNs contain duplicate %q", raw)
		}
		parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, raw)
		if err != nil || parsed.String() != raw {
			return fmt.Errorf("probe ISSN %q is invalid", raw)
		}
	}
	if probe.Attempted {
		if len(probe.ISSNs) == 0 {
			return errors.New("attempted probe requires at least one ISSN")
		}
		parsed, err := time.Parse(time.RFC3339Nano, probe.CheckedAt)
		if err != nil || parsed.Location() != time.UTC {
			return errors.New(
				"attempted probe requires UTC RFC3339Nano checked_at",
			)
		}
	} else {
		if probe.CheckedAt != "" ||
			probe.ResponseSHA256 != "" ||
			probe.Error != "" ||
			probe.Status != venueenrich.SupportStatusUnknown ||
			probe.RecordCount != 0 {
			return errors.New("unattempted probe receipt is contradictory")
		}
		return nil
	}
	if probe.Error != "" {
		if probe.Error != strings.TrimSpace(probe.Error) ||
			strings.ContainsAny(probe.Error, "\r\n\t") ||
			probe.Status != venueenrich.SupportStatusUnknown ||
			probe.RecordCount != 0 ||
			probe.ResponseSHA256 != "" {
			return errors.New("failed probe receipt is contradictory")
		}
		return nil
	}
	if !validSHA256(probe.ResponseSHA256) {
		return errors.New("successful probe response_sha256 is invalid")
	}
	switch probe.Status {
	case venueenrich.SupportStatusYes:
		if probe.RecordCount <= 0 {
			return errors.New("yes probe requires a positive record_count")
		}
	case venueenrich.SupportStatusNo:
		if probe.RecordCount != 0 {
			return errors.New("no probe requires zero record_count")
		}
	default:
		return errors.New("attempted unknown probe requires an error")
	}
	return nil
}

func encodeRegistryReport(report registryReport) ([]byte, error) {
	if err := validateRegistryReport(report); err != nil {
		return nil, err
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode registry report: %w", err)
	}
	payload = append(payload, '\n')
	var decoded registryReport
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("validate encoded registry report: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New(
			"validate encoded registry report: trailing JSON value",
		)
	}
	if err := validateRegistryReport(decoded); err != nil {
		return nil, fmt.Errorf("validate encoded registry report: %w", err)
	}
	return payload, nil
}

func reconcileRegistryArtifacts(csvBytes, reportBytes []byte) error {
	if err := validateRegistryCSVBytes(
		csvBytes,
		journalPubMedRegistryRows,
	); err != nil {
		return err
	}
	var report registryReport
	decoder := json.NewDecoder(bytes.NewReader(reportBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return fmt.Errorf("reconcile registry report: %w", err)
	}
	if err := validateRegistryReport(report); err != nil {
		return fmt.Errorf("reconcile registry report: %w", err)
	}
	if report.OutputCSV.Rows != journalPubMedRegistryRows ||
		report.OutputCSV.Bytes != int64(len(csvBytes)) ||
		report.OutputCSV.SHA256 != sha256Hex(csvBytes) {
		return errors.New(
			"registry output CSV count/hash reconciliation failed",
		)
	}
	return nil
}

func validateProbeForRow(
	row venueenrich.RegistryRow,
	probe venueenrich.PubMedProbeReceipt,
) error {
	if err := validateReportProbe(probe); err != nil {
		return fmt.Errorf("invalid PubMed probe receipt: %w", err)
	}
	if probe.Domain != row.Domain ||
		probe.SourceOrder != row.SourceOrder ||
		!slices.Equal(probe.ISSNs, row.AllISSNs) ||
		probe.Status != row.PubMedSupported ||
		probe.RecordCount != row.PubMedRecordCount {
		return errors.New("PubMed probe receipt does not match registry row")
	}
	if probe.Attempted {
		parsed, err := time.Parse(time.RFC3339Nano, probe.CheckedAt)
		if err != nil || parsed.Location() != time.UTC {
			return errors.New(
				"attempted PubMed probe requires UTC RFC3339Nano checked_at",
			)
		}
	} else if probe.CheckedAt != "" ||
		probe.ResponseSHA256 != "" ||
		probe.Error != "" {
		return errors.New(
			"unattempted PubMed probe requires empty checked_at, response_sha256, and error",
		)
	}
	if row.MatchStatus != venueenrich.MatchStatusResolved &&
		probe.Attempted {
		return errors.New(
			"only resolved rows may have an attempted PubMed probe",
		)
	}
	if probe.ResponseSHA256 != "" && !validSHA256(probe.ResponseSHA256) {
		return errors.New("PubMed probe response_sha256 is invalid")
	}
	if probe.Error != "" {
		if !probe.Attempted ||
			probe.Status != venueenrich.SupportStatusUnknown ||
			probe.RecordCount != 0 ||
			probe.ResponseSHA256 != "" {
			return errors.New("failed PubMed probe receipt is contradictory")
		}
	}
	if probe.Status == venueenrich.SupportStatusYes &&
		(!probe.Attempted || probe.RecordCount <= 0 ||
			probe.ResponseSHA256 == "") {
		return errors.New("positive PubMed probe receipt is incomplete")
	}
	if probe.Status == venueenrich.SupportStatusNo &&
		(!probe.Attempted || probe.RecordCount != 0 ||
			probe.ResponseSHA256 == "") {
		return errors.New("negative PubMed probe receipt is incomplete")
	}
	return nil
}

func publishRegistryArtifacts(
	outputPath string,
	outputPayload []byte,
	reportPath string,
	reportPayload []byte,
	ops registryFileOps,
) (returnErr error) {
	if err := validateDistinctRegistryPaths(outputPath, reportPath); err != nil {
		return err
	}
	for _, path := range []string{outputPath, reportPath} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("refuse to replace existing final %q", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect final %q: %w", path, err)
		}
	}

	outputTemp, err := writeRegistryTemp(outputPath, outputPayload)
	if err != nil {
		return err
	}
	reportTemp, err := writeRegistryTemp(reportPath, reportPayload)
	if err != nil {
		return errors.Join(
			err,
			cleanupRegistryPath(
				ops.remove,
				outputTemp,
				"output CSV temporary file",
			),
		)
	}
	outputPublished := false
	reportPublished := false
	var outputPublishedInfo os.FileInfo
	var reportPublishedInfo os.FileInfo
	defer func() {
		if returnErr == nil {
			return
		}
		cleanupErr := errors.Join(
			cleanupRegistryPath(
				ops.remove,
				outputTemp,
				"output CSV temporary file",
			),
			cleanupRegistryPath(
				ops.remove,
				reportTemp,
				"report temporary file",
			),
		)
		if reportPublished {
			cleanupErr = errors.Join(
				cleanupErr,
				cleanupPublishedRegistryPath(
					ops.remove,
					reportPath,
					reportPublishedInfo,
					"published report",
				),
			)
		}
		if outputPublished {
			cleanupErr = errors.Join(
				cleanupErr,
				cleanupPublishedRegistryPath(
					ops.remove,
					outputPath,
					outputPublishedInfo,
					"published output CSV",
				),
			)
		}
		returnErr = errors.Join(returnErr, cleanupErr)
	}()

	if err := ops.before(registryPublishOutput); err != nil {
		return err
	}
	outputTempInfo, err := os.Lstat(outputTemp)
	if err != nil {
		return fmt.Errorf("record output CSV temporary identity: %w", err)
	}
	if err := ops.link(outputTemp, outputPath); err != nil {
		return fmt.Errorf(
			"publish output CSV without replacing final: %w",
			err,
		)
	}
	outputFinalInfo, err := os.Lstat(outputPath)
	if err != nil {
		return fmt.Errorf("verify output CSV link identity: %w", err)
	}
	if !os.SameFile(outputTempInfo, outputFinalInfo) {
		return errors.New(
			"verify output CSV link identity: final identity does not match temporary file",
		)
	}
	outputPublished = true
	outputPublishedInfo = outputTempInfo
	if err := ops.remove(outputTemp); err != nil {
		return fmt.Errorf("unlink published output CSV temporary file: %w", err)
	}
	outputTemp = ""
	if err := ops.syncDir(filepath.Dir(outputPath)); err != nil {
		return fmt.Errorf("sync output CSV directory: %w", err)
	}

	if err := ops.before(registryPublishReport); err != nil {
		return err
	}
	reportTempInfo, err := os.Lstat(reportTemp)
	if err != nil {
		return fmt.Errorf("record report temporary identity: %w", err)
	}
	if err := ops.link(reportTemp, reportPath); err != nil {
		return fmt.Errorf(
			"publish report without replacing final: %w",
			err,
		)
	}
	reportFinalInfo, err := os.Lstat(reportPath)
	if err != nil {
		return fmt.Errorf("verify report link identity: %w", err)
	}
	if !os.SameFile(reportTempInfo, reportFinalInfo) {
		return errors.New(
			"verify report link identity: final identity does not match temporary file",
		)
	}
	reportPublished = true
	reportPublishedInfo = reportTempInfo
	if err := ops.remove(reportTemp); err != nil {
		return fmt.Errorf("unlink published report temporary file: %w", err)
	}
	reportTemp = ""
	if err := ops.syncDir(filepath.Dir(reportPath)); err != nil {
		return fmt.Errorf("sync report directory: %w", err)
	}
	return nil
}

type registryPathIdentity struct {
	path      string
	canonical string
	lstat     os.FileInfo
	stat      os.FileInfo
	lstatErr  error
	statErr   error
}

func validateDistinctRegistryPaths(outputPath, reportPath string) error {
	output, err := inspectRegistryPathIdentity(outputPath)
	if err != nil {
		return fmt.Errorf("inspect output path: %w", err)
	}
	report, err := inspectRegistryPathIdentity(reportPath)
	if err != nil {
		return fmt.Errorf("inspect report path: %w", err)
	}
	if output.canonical == report.canonical {
		return fmt.Errorf(
			"output and report resolve to the same final path %q",
			output.canonical,
		)
	}
	if output.lstatErr == nil &&
		report.lstatErr == nil &&
		os.SameFile(output.lstat, report.lstat) {
		return errors.New(
			"output and report are the same final inode via Lstat",
		)
	}
	if output.statErr == nil &&
		report.statErr == nil &&
		os.SameFile(output.stat, report.stat) {
		return errors.New(
			"output and report are the same final inode via Stat",
		)
	}
	if errors.Is(output.lstatErr, os.ErrNotExist) &&
		errors.Is(report.lstatErr, os.ErrNotExist) &&
		output.canonical != report.canonical &&
		strings.EqualFold(output.canonical, report.canonical) {
		aliases, probeErr := probeRegistryCaseAlias(
			output.canonical,
			report.canonical,
		)
		if probeErr != nil {
			return fmt.Errorf(
				"probe registry final path case sensitivity: %w",
				probeErr,
			)
		}
		if aliases {
			return errors.New(
				"output and report resolve to the same final path on a case-insensitive filesystem",
			)
		}
	}
	return nil
}

func inspectRegistryPathIdentity(path string) (registryPathIdentity, error) {
	if err := validateExplicitPath("registry final path", path); err != nil {
		return registryPathIdentity{}, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return registryPathIdentity{}, fmt.Errorf(
			"absolute path %q: %w",
			path,
			err,
		)
	}
	cleaned := filepath.Clean(absolute)
	canonical, err := canonicalRegistryPath(cleaned)
	if err != nil {
		return registryPathIdentity{}, err
	}
	lstat, lstatErr := os.Lstat(cleaned)
	stat, statErr := os.Stat(cleaned)
	if lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist) {
		return registryPathIdentity{}, fmt.Errorf(
			"Lstat %q: %w",
			cleaned,
			lstatErr,
		)
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return registryPathIdentity{}, fmt.Errorf(
			"Stat %q: %w",
			cleaned,
			statErr,
		)
	}
	return registryPathIdentity{
		path:      cleaned,
		canonical: canonical,
		lstat:     lstat,
		stat:      stat,
		lstatErr:  lstatErr,
		statErr:   statErr,
	}, nil
}

func canonicalRegistryPath(path string) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0, 4)
	for {
		_, err := os.Lstat(current)
		switch {
		case err == nil:
			resolved, evalErr := filepath.EvalSymlinks(current)
			if evalErr == nil {
				resolvedAbsolute, absoluteErr := filepath.Abs(resolved)
				if absoluteErr != nil {
					return "", fmt.Errorf(
						"absolute resolved path %q: %w",
						resolved,
						absoluteErr,
					)
				}
				for index := len(missing) - 1; index >= 0; index-- {
					resolvedAbsolute = filepath.Join(
						resolvedAbsolute,
						missing[index],
					)
				}
				return filepath.Clean(resolvedAbsolute), nil
			}
			if !errors.Is(evalErr, os.ErrNotExist) {
				return "", fmt.Errorf(
					"evaluate symlinks %q: %w",
					current,
					evalErr,
				)
			}
			// A dangling symlink cannot be resolved further. Keep the
			// verifiable prefix and let the eventual temp creation report
			// the unusable parent if this path is selected for publishing.
			for index := len(missing) - 1; index >= 0; index-- {
				current = filepath.Join(current, missing[index])
			}
			return filepath.Clean(current), nil
		case errors.Is(err, os.ErrNotExist):
			parent := filepath.Dir(current)
			missing = append(missing, filepath.Base(current))
			if parent == current {
				return filepath.Clean(current), nil
			}
			current = parent
		default:
			return "", fmt.Errorf("inspect path %q: %w", current, err)
		}
	}
}

func probeRegistryCaseAlias(
	firstPath string,
	secondPath string,
) (aliases bool, returnErr error) {
	probeParent, err := registryCaseProbeParent(firstPath, secondPath)
	if err != nil {
		return false, err
	}
	probeDirectory, err := os.MkdirTemp(
		probeParent,
		registryCaseProbePattern,
	)
	if err != nil {
		return false, fmt.Errorf(
			"create case-sensitivity probe directory in %q: %w",
			probeParent,
			err,
		)
	}
	defer func() {
		if err := os.RemoveAll(probeDirectory); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf(
					"remove case-sensitivity probe directory %q: %w",
					probeDirectory,
					err,
				),
			)
		}
	}()

	firstRelative, err := filepath.Rel(probeParent, firstPath)
	if err != nil || registryRelativePathEscapes(firstRelative) {
		return false, fmt.Errorf(
			"resolve first case-sensitivity probe path %q from %q",
			firstPath,
			probeParent,
		)
	}
	secondRelative, err := filepath.Rel(probeParent, secondPath)
	if err != nil || registryRelativePathEscapes(secondRelative) {
		return false, fmt.Errorf(
			"resolve second case-sensitivity probe path %q from %q",
			secondPath,
			probeParent,
		)
	}
	firstProbePath := filepath.Join(probeDirectory, firstRelative)
	if err := os.MkdirAll(filepath.Dir(firstProbePath), 0o700); err != nil {
		return false, fmt.Errorf(
			"create case-sensitivity probe parent for %q: %w",
			firstProbePath,
			err,
		)
	}
	first, err := os.OpenFile(
		firstProbePath,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return false, fmt.Errorf(
			"create case-sensitivity probe %q: %w",
			firstProbePath,
			err,
		)
	}
	if err := first.Close(); err != nil {
		return false, fmt.Errorf(
			"close case-sensitivity probe %q: %w",
			firstProbePath,
			err,
		)
	}
	firstInfo, err := os.Lstat(firstProbePath)
	if err != nil {
		return false, fmt.Errorf(
			"Lstat case-sensitivity probe %q: %w",
			firstProbePath,
			err,
		)
	}
	secondProbePath := filepath.Join(probeDirectory, secondRelative)
	secondInfo, err := os.Lstat(secondProbePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"Lstat case-sensitivity probe alias %q: %w",
			secondProbePath,
			err,
		)
	}
	if !os.SameFile(firstInfo, secondInfo) {
		return false, errors.New(
			"case-sensitivity probe aliases resolved to different inodes",
		)
	}
	return true, nil
}

func registryCaseProbeParent(firstPath, secondPath string) (string, error) {
	current := filepath.Dir(firstPath)
	for {
		relative, err := filepath.Rel(current, secondPath)
		if err == nil && !registryRelativePathEscapes(relative) {
			return nearestExistingRegistryDirectory(current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf(
				"registry case-alias paths %q and %q have no common parent",
				firstPath,
				secondPath,
			)
		}
		current = parent
	}
}

func registryRelativePathEscapes(path string) bool {
	return path == ".." ||
		strings.HasPrefix(path, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(path)
}

func nearestExistingRegistryDirectory(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		info, err := os.Stat(current)
		switch {
		case err == nil:
			if !info.IsDir() {
				return "", fmt.Errorf(
					"case-sensitivity probe parent %q is not a directory",
					current,
				)
			}
			return current, nil
		case errors.Is(err, os.ErrNotExist):
			parent := filepath.Dir(current)
			if parent == current {
				return "", fmt.Errorf(
					"no existing directory for case-sensitivity probe path %q",
					path,
				)
			}
			current = parent
		default:
			return "", fmt.Errorf(
				"inspect case-sensitivity probe parent %q: %w",
				current,
				err,
			)
		}
	}
}

func writeRegistryTemp(path string, payload []byte) (returnPath string, returnErr error) {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(
		directory,
		"."+filepath.Base(path)+".tmp-*",
	)
	if err != nil {
		return "", fmt.Errorf(
			"create temporary file for %q: %w",
			path,
			err,
		)
	}
	tempPath := temp.Name()
	tempClosed := false
	defer func() {
		if returnErr != nil {
			var closeErr error
			if !tempClosed {
				closeErr = temp.Close()
				if errors.Is(closeErr, os.ErrClosed) {
					closeErr = nil
				}
			}
			removeErr := ignoreNotExist(os.Remove(tempPath))
			if closeErr != nil {
				closeErr = fmt.Errorf(
					"close temporary file during cleanup for %q: %w",
					path,
					closeErr,
				)
			}
			if removeErr != nil {
				removeErr = fmt.Errorf(
					"remove temporary file during cleanup for %q: %w",
					path,
					removeErr,
				)
			}
			returnErr = errors.Join(returnErr, closeErr, removeErr)
		}
	}()
	if _, err := temp.Write(payload); err != nil {
		return "", fmt.Errorf("write temporary file for %q: %w", path, err)
	}
	if err := temp.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary file for %q: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close temporary file for %q: %w", path, err)
	}
	tempClosed = true
	return tempPath, nil
}

func defaultRegistryFileOps() registryFileOps {
	return registryFileOps{
		before: func(string) error { return nil },
		link:   os.Link,
		remove: os.Remove,
		syncDir: func(path string) error {
			directory, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("open directory for fsync: %w", err)
			}
			syncErr := directory.Sync()
			closeErr := directory.Close()
			if syncErr != nil {
				syncErr = fmt.Errorf("fsync directory: %w", syncErr)
			}
			if closeErr != nil {
				closeErr = fmt.Errorf("close fsync directory: %w", closeErr)
			}
			return errors.Join(syncErr, closeErr)
		},
	}
}

func cleanupRegistryPath(
	remove func(string) error,
	path string,
	description string,
) error {
	if path == "" {
		return nil
	}
	if err := ignoreNotExist(remove(path)); err != nil {
		return fmt.Errorf("remove %s %q: %w", description, path, err)
	}
	return nil
}

func cleanupPublishedRegistryPath(
	remove func(string) error,
	path string,
	expected os.FileInfo,
	description string,
) error {
	// This is intentionally best-effort: without a portable directory-scoped
	// conditional unlink primitive, Lstat followed by Remove cannot eliminate
	// an adversarial replacement race. It protects normal no-replace publishes
	// and preserves a final whose inode changed before rollback.
	if expected == nil {
		return fmt.Errorf(
			"preserve %s %q: published inode identity is unavailable",
			description,
			path,
		)
	}
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf(
			"inspect %s %q before rollback: %w",
			description,
			path,
			err,
		)
	}
	if !os.SameFile(current, expected) {
		return fmt.Errorf(
			"preserve %s %q: final was replaced before rollback",
			description,
			path,
		)
	}
	return cleanupRegistryPath(remove, path, description)
}

func validateExplicitPath(name, value string) error {
	if strings.TrimSpace(value) == "" ||
		value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be explicit and non-empty", name)
	}
	return nil
}

func requiredTrimmedEnvironment(
	lookup envLookup,
	key string,
) (string, error) {
	value, ok := lookup(key)
	if !ok {
		return "", fmt.Errorf("%s is required and must be trimmed", key)
	}
	return validateRequiredTrimmed(key, value)
}

func requiredBareEmailEnvironment(
	lookup envLookup,
	key string,
) (string, error) {
	value, ok := lookup(key)
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	return validateRequiredBareEmail(key, value)
}

func validateRequiredTrimmed(key, value string) (string, error) {
	if strings.TrimSpace(value) == "" ||
		value != strings.TrimSpace(value) {
		return "", fmt.Errorf("%s is required and must be trimmed", key)
	}
	return value, nil
}

func validateRequiredBareEmail(key, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return validateBareEmail(key, value)
}

func validateBareEmail(key, value string) (string, error) {
	if value != strings.TrimSpace(value) {
		return "", fmt.Errorf("%s must be a valid bare email address", key)
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value {
		return "", fmt.Errorf("%s must be a valid bare email address", key)
	}
	return value, nil
}

func redactRegistrySecret(message, secret string) string {
	if secret == "" {
		return message
	}
	redacted := strings.ReplaceAll(message, secret, "[REDACTED]")
	redacted = strings.ReplaceAll(
		redacted,
		url.QueryEscape(secret),
		"[REDACTED]",
	)
	return redacted
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 ||
		value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func ignoreNotExist(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type repeatedStringFlag []string

func (flagValues *repeatedStringFlag) String() string {
	return strings.Join(*flagValues, ",")
}

func (flagValues *repeatedStringFlag) Set(value string) error {
	*flagValues = append(*flagValues, value)
	return nil
}
