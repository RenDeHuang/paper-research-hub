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
	rename  func(string, string) error
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
	crossrefEmail := email
	if raw, ok := lookup("CROSSREF_CONTACT_EMAIL"); ok &&
		strings.TrimSpace(raw) != "" {
		crossrefEmail, err = validateBareEmail(
			"CROSSREF_CONTACT_EMAIL",
			raw,
		)
		if err != nil {
			return registryCommand{}, err
		}
	}
	command.NCBITool = tool
	command.NCBIEmail = email
	command.NCBIAPIKey = apiKey
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
	dependencies, err := runner.dependencies.validated()
	if err != nil {
		return registryResult{}, err
	}

	sourceRows, inputs, err := loadRegistryInputs(command.Inputs)
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

	counter, err := dependencies.newPubMedCounter(
		dependencies.httpClient,
		pubMedClientConfig(command, dependencies.pubMedBaseURL),
		httpDependencies,
	)
	if err != nil {
		return registryResult{}, fmt.Errorf("create PubMed coverage client: %w", err)
	}
	rows, probes, err := venueenrich.ProbePubMedCoverage(
		ctx,
		rows,
		counter,
		dependencies.now,
	)
	if err != nil {
		return registryResult{}, fmt.Errorf("probe PubMed coverage: %w", err)
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

func (dependencies registryDependencies) validated() (registryDependencies, error) {
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
	if strings.TrimSpace(dependencies.pubMedBaseURL) == "" {
		return registryDependencies{}, errors.New(
			"registry PubMed base URL is required",
		)
	}
	if dependencies.fetchCrossref == nil ||
		dependencies.matchCrossref == nil ||
		dependencies.newPubMedCounter == nil {
		return registryDependencies{}, errors.New(
			"registry source dependencies are required",
		)
	}
	defaultOps := defaultRegistryFileOps()
	if dependencies.fileOps.before == nil {
		dependencies.fileOps.before = defaultOps.before
	}
	if dependencies.fileOps.rename == nil {
		dependencies.fileOps.rename = defaultOps.rename
	}
	if dependencies.fileOps.remove == nil {
		dependencies.fileOps.remove = defaultOps.remove
	}
	if dependencies.fileOps.syncDir == nil {
		dependencies.fileOps.syncDir = defaultOps.syncDir
	}
	return dependencies, nil
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
) ([]venueenrich.SourceRow, []registryInputReceipt, error) {
	if len(paths) != 3 {
		return nil, nil, errors.New(
			"registry input metadata requires exactly three paths",
		)
	}
	receipts := make([]registryInputReceipt, len(paths))
	originalPayloads := make([][]byte, len(paths))
	for index, path := range paths {
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"input %d %q: read raw bytes: %w",
				index+1,
				path,
				err,
			)
		}
		fileRows, err := venueenrich.LoadSourceFiles([]string{path})
		if err != nil {
			return nil, nil, fmt.Errorf(
				"input %d %q: load logical rows: %w",
				index+1,
				path,
				err,
			)
		}
		originalPayloads[index] = payload
		receipts[index] = registryInputReceipt{
			Ordinal: index + 1,
			Path:    path,
			Rows:    len(fileRows),
			Bytes:   int64(len(payload)),
			SHA256:  sha256Hex(payload),
		}
	}
	rows, err := venueenrich.LoadSourceFiles(paths)
	if err != nil {
		return nil, nil, fmt.Errorf("load three source files: %w", err)
	}
	totalRows := 0
	for _, receipt := range receipts {
		totalRows += receipt.Rows
	}
	if totalRows != len(rows) {
		return nil, nil, fmt.Errorf(
			"input logical row reconciliation failed: per-file=%d combined=%d",
			totalRows,
			len(rows),
		)
	}
	for index, path := range paths {
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"input %d %q: re-read raw bytes: %w",
				index+1,
				path,
				err,
			)
		}
		if !bytes.Equal(payload, originalPayloads[index]) {
			return nil, nil, fmt.Errorf(
				"input %d %q changed while registry inputs were loaded",
				index+1,
				path,
			)
		}
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
		counts.Probes.Attempted > counts.Probes.Eligible {
		return errors.New("registry probe counts do not reconcile")
	}
	if counts.PubMed.Yes+
		counts.PubMed.No+
		counts.PubMed.Unknown != counts.OutputRows {
		return errors.New("registry PubMed counts do not reconcile")
	}
	var aggregate registryCounts
	for _, domain := range byDomain {
		if strings.TrimSpace(domain.Domain) == "" {
			return errors.New("registry by_domain requires non-empty domain")
		}
		domainCounts := domain.Counts
		if domainCounts.InputRows != domainCounts.OutputRows ||
			domainCounts.Match.Resolved+
				domainCounts.Match.Ambiguous+
				domainCounts.Match.Unresolved != domainCounts.InputRows ||
			domainCounts.Probes.Eligible != domainCounts.Match.Resolved ||
			domainCounts.Probes.Attempted > domainCounts.Probes.Eligible ||
			domainCounts.PubMed.Yes+
				domainCounts.PubMed.No+
				domainCounts.PubMed.Unknown != domainCounts.OutputRows {
			return fmt.Errorf(
				"registry by_domain %q counts do not reconcile",
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
	if outputPath == reportPath {
		return errors.New("registry output and report paths must differ")
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
		_ = ops.remove(outputTemp)
		return err
	}
	outputPublished := false
	reportPublished := false
	defer func() {
		if returnErr == nil {
			return
		}
		cleanupErr := errors.Join(
			ignoreNotExist(ops.remove(outputTemp)),
			ignoreNotExist(ops.remove(reportTemp)),
		)
		if reportPublished {
			cleanupErr = errors.Join(
				cleanupErr,
				ignoreNotExist(ops.remove(reportPath)),
			)
		}
		if outputPublished {
			cleanupErr = errors.Join(
				cleanupErr,
				ignoreNotExist(ops.remove(outputPath)),
			)
		}
		returnErr = errors.Join(returnErr, cleanupErr)
	}()

	if err := ops.before(registryPublishOutput); err != nil {
		return err
	}
	if err := ops.rename(outputTemp, outputPath); err != nil {
		return fmt.Errorf("rename output CSV temporary file: %w", err)
	}
	outputPublished = true
	outputTemp = ""
	if err := ops.syncDir(filepath.Dir(outputPath)); err != nil {
		return fmt.Errorf("sync output CSV directory: %w", err)
	}

	if err := ops.before(registryPublishReport); err != nil {
		return err
	}
	if err := ops.rename(reportTemp, reportPath); err != nil {
		return fmt.Errorf("rename report temporary file: %w", err)
	}
	reportPublished = true
	reportTemp = ""
	if err := ops.syncDir(filepath.Dir(reportPath)); err != nil {
		return fmt.Errorf("sync report directory: %w", err)
	}
	return nil
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
	defer func() {
		if returnErr != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
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
	return tempPath, nil
}

func defaultRegistryFileOps() registryFileOps {
	return registryFileOps{
		before: func(string) error { return nil },
		rename: os.Rename,
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
	if !ok ||
		strings.TrimSpace(value) == "" ||
		value != strings.TrimSpace(value) {
		return "", fmt.Errorf("%s is required and must be trimmed", key)
	}
	return value, nil
}

func requiredBareEmailEnvironment(
	lookup envLookup,
	key string,
) (string, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
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
