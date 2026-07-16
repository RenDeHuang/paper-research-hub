package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Role string

const (
	RoleAPI                Role = "api"
	RoleMigrate            Role = "migrate"
	RoleCatalogPublish     Role = "catalog-publish"
	RoleOpenAlexSync       Role = "openalex-sync"
	RolePubMedSync         Role = "pubmed-sync"
	RolePubMedImport       Role = "pubmed-import"
	RoleCrossrefSync       Role = "crossref-sync"
	RolePMCSync            Role = "pmc-sync"
	RoleSpringerNatureSync Role = "springer-nature-sync"
	RoleElsevierSync       Role = "elsevier-sync"
	RoleJCRImport          Role = "jcr-import"
	defaultEnvironment          = "development"
	defaultAPIHost              = "0.0.0.0"
	defaultAPIPort              = 8080
	defaultMaxHeaderBytes       = 1 << 20
)

type LookupEnv func(string) (string, bool)

type Config struct {
	Environment string          `json:"environment"`
	HTTP        HTTPConfig      `json:"http"`
	Database    DatabaseConfig  `json:"database"`
	Catalog     CatalogConfig   `json:"catalog"`
	Worker      WorkerConfig    `json:"worker"`
	OpenAlex    OpenAlexConfig  `json:"openalex"`
	PubMed      PubMedConfig    `json:"pubmed"`
	Crossref    CrossrefConfig  `json:"crossref"`
	PMC         PMCConfig       `json:"pmc"`
	Publishers  PublisherConfig `json:"publishers"`
	Venues      VenueConfig     `json:"venues"`
}

type HTTPConfig struct {
	Host               string        `json:"host"`
	Port               int           `json:"port"`
	ReadHeaderTimeout  time.Duration `json:"read_header_timeout"`
	ReadTimeout        time.Duration `json:"read_timeout"`
	WriteTimeout       time.Duration `json:"write_timeout"`
	IdleTimeout        time.Duration `json:"idle_timeout"`
	ShutdownTimeout    time.Duration `json:"shutdown_timeout"`
	MaxHeaderBytes     int           `json:"max_header_bytes"`
	CORSAllowedOrigins []string      `json:"cors_allowed_origins,omitempty"`
}

func (cfg HTTPConfig) Address() string {
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

type DatabaseConfig struct {
	URL               string        `json:"-"`
	MinConns          int32         `json:"min_conns"`
	MaxConns          int32         `json:"max_conns"`
	ConnectTimeout    time.Duration `json:"connect_timeout"`
	MaxConnLifetime   time.Duration `json:"max_conn_lifetime"`
	MaxConnIdleTime   time.Duration `json:"max_conn_idle_time"`
	HealthCheckPeriod time.Duration `json:"health_check_period"`
}

type CatalogConfig struct {
	CursorSecret string `json:"-"`
}

type WorkerConfig struct {
	Concurrency  int           `json:"concurrency"`
	BatchSize    int           `json:"batch_size"`
	PollInterval time.Duration `json:"poll_interval"`
	MaxRetries   int           `json:"max_retries"`
	MaxWait      time.Duration `json:"max_wait"`
}

type RequestConfig struct {
	BaseURL    string        `json:"base_url"`
	Timeout    time.Duration `json:"timeout"`
	MaxRetries int           `json:"max_retries"`
	MaxWait    time.Duration `json:"max_wait"`
	BatchSize  int           `json:"batch_size"`
}

type OpenAlexConfig struct {
	Request      RequestConfig `json:"request"`
	ContactEmail string        `json:"contact_email,omitempty"`
	APIKey       string        `json:"-"`
}

type PubMedConfig struct {
	Request RequestConfig `json:"request"`
	Tool    string        `json:"tool,omitempty"`
	Email   string        `json:"email,omitempty"`
	APIKey  string        `json:"-"`
}

type CrossrefConfig struct {
	Request      RequestConfig `json:"request"`
	ContactEmail string        `json:"contact_email,omitempty"`
}

type PMCConfig struct {
	Request      RequestConfig `json:"request"`
	ContactEmail string        `json:"contact_email,omitempty"`
}

type PublisherConfig struct {
	SpringerNature PublisherSourceConfig `json:"springer_nature"`
	Elsevier       PublisherSourceConfig `json:"elsevier"`
}

type PublisherSourceConfig struct {
	Request      RequestConfig `json:"request"`
	ContactEmail string        `json:"contact_email,omitempty"`
	APIKey       string        `json:"-"`
}

type VenueConfig struct {
	JCRImportPath    string `json:"jcr_import_path,omitempty"`
	JCRSourceLicense string `json:"jcr_source_license,omitempty"`
}

type RedactedConfig struct {
	Environment string                  `json:"environment"`
	HTTP        HTTPConfig              `json:"http"`
	Database    RedactedDatabaseConfig  `json:"database"`
	Catalog     RedactedCatalogConfig   `json:"catalog"`
	Worker      WorkerConfig            `json:"worker"`
	OpenAlex    RedactedOpenAlexConfig  `json:"openalex"`
	PubMed      RedactedPubMedConfig    `json:"pubmed"`
	Crossref    CrossrefConfig          `json:"crossref"`
	PMC         PMCConfig               `json:"pmc"`
	Publishers  RedactedPublisherConfig `json:"publishers"`
	Venues      VenueConfig             `json:"venues"`
}

type RedactedDatabaseConfig struct {
	URL               string        `json:"url"`
	MinConns          int32         `json:"min_conns"`
	MaxConns          int32         `json:"max_conns"`
	ConnectTimeout    time.Duration `json:"connect_timeout"`
	MaxConnLifetime   time.Duration `json:"max_conn_lifetime"`
	MaxConnIdleTime   time.Duration `json:"max_conn_idle_time"`
	HealthCheckPeriod time.Duration `json:"health_check_period"`
}

type RedactedCatalogConfig struct {
	CursorSecret string `json:"cursor_secret,omitempty"`
}

type RedactedOpenAlexConfig struct {
	Request      RequestConfig `json:"request"`
	ContactEmail string        `json:"contact_email,omitempty"`
	APIKey       string        `json:"api_key,omitempty"`
}

type RedactedPubMedConfig struct {
	Request RequestConfig `json:"request"`
	Tool    string        `json:"tool,omitempty"`
	Email   string        `json:"email,omitempty"`
	APIKey  string        `json:"api_key,omitempty"`
}

type RedactedPublisherConfig struct {
	SpringerNature RedactedPublisherSourceConfig `json:"springer_nature"`
	Elsevier       RedactedPublisherSourceConfig `json:"elsevier"`
}

type RedactedPublisherSourceConfig struct {
	Request      RequestConfig `json:"request"`
	ContactEmail string        `json:"contact_email,omitempty"`
	APIKey       string        `json:"api_key,omitempty"`
}

func Load(role Role) (Config, error) {
	return LoadFrom(role, os.LookupEnv)
}

func LoadFrom(role Role, lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("environment lookup is required")
	}
	if !supportedRole(role) {
		return Config{}, fmt.Errorf("unsupported configuration role %q", role)
	}

	environment, err := readEnum(lookup, "APP_ENV", defaultEnvironment, []string{
		"development", "test", "staging", "production",
	})
	if err != nil {
		return Config{}, err
	}
	databaseURL, err := required(lookup, "DATABASE_URL")
	if err != nil {
		return Config{}, err
	}
	if err := validateDatabaseURL(databaseURL); err != nil {
		return Config{}, err
	}

	httpConfig, err := loadHTTP(lookup)
	if err != nil {
		return Config{}, err
	}
	databaseConfig, err := loadDatabase(lookup, databaseURL)
	if err != nil {
		return Config{}, err
	}
	workerConfig, err := loadWorker(lookup)
	if err != nil {
		return Config{}, err
	}
	openAlexRequest, err := loadRequest(lookup, "OPENALEX", "https://api.openalex.org", 100)
	if err != nil {
		return Config{}, err
	}
	pubMedRequest, err := loadRequest(lookup, "PUBMED", "https://eutils.ncbi.nlm.nih.gov", 1000)
	if err != nil {
		return Config{}, err
	}
	crossrefRequest, err := loadRequest(lookup, "CROSSREF", "https://api.crossref.org", 1000)
	if err != nil {
		return Config{}, err
	}
	pmcRequest, err := loadRequest(lookup, "PMC", "https://www.ncbi.nlm.nih.gov/pmc", 1000)
	if err != nil {
		return Config{}, err
	}
	springerRequest, err := loadRequest(
		lookup,
		"SPRINGER_NATURE",
		"https://api.springernature.com",
		1000,
	)
	if err != nil {
		return Config{}, err
	}
	elsevierRequest, err := loadRequest(lookup, "ELSEVIER", "https://api.elsevier.com", 1000)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Environment: environment,
		HTTP:        httpConfig,
		Database:    databaseConfig,
		Catalog: CatalogConfig{
			CursorSecret: optional(lookup, "CATALOG_CURSOR_SECRET"),
		},
		Worker: workerConfig,
		OpenAlex: OpenAlexConfig{
			Request:      openAlexRequest,
			ContactEmail: optional(lookup, "OPENALEX_CONTACT_EMAIL"),
			APIKey:       optional(lookup, "OPENALEX_API_KEY"),
		},
		PubMed: PubMedConfig{
			Request: pubMedRequest,
			Tool:    optional(lookup, "NCBI_TOOL"),
			Email:   optional(lookup, "NCBI_EMAIL"),
			APIKey:  optional(lookup, "NCBI_API_KEY"),
		},
		Crossref: CrossrefConfig{
			Request:      crossrefRequest,
			ContactEmail: optional(lookup, "CROSSREF_CONTACT_EMAIL"),
		},
		PMC: PMCConfig{
			Request:      pmcRequest,
			ContactEmail: optional(lookup, "PMC_CONTACT_EMAIL"),
		},
		Publishers: PublisherConfig{
			SpringerNature: PublisherSourceConfig{
				Request:      springerRequest,
				ContactEmail: optional(lookup, "SPRINGER_NATURE_CONTACT_EMAIL"),
				APIKey:       optional(lookup, "SPRINGER_NATURE_API_KEY"),
			},
			Elsevier: PublisherSourceConfig{
				Request:      elsevierRequest,
				ContactEmail: optional(lookup, "ELSEVIER_CONTACT_EMAIL"),
				APIKey:       optional(lookup, "ELSEVIER_API_KEY"),
			},
		},
		Venues: VenueConfig{
			JCRImportPath:    optional(lookup, "JCR_IMPORT_PATH"),
			JCRSourceLicense: optional(lookup, "JCR_SOURCE_LICENSE"),
		},
	}

	if cfg.Venues.JCRImportPath != "" && !isExplicitCSV(cfg.Venues.JCRImportPath) {
		return Config{}, errors.New("JCR_IMPORT_PATH must reference an explicit .csv file")
	}
	if err := cfg.validateRole(role); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) validateRole(role Role) error {
	switch role {
	case RoleAPI:
		return validateCatalogCursorSecret(cfg.Catalog.CursorSecret)
	case RoleMigrate, RoleCatalogPublish:
		return nil
	case RoleOpenAlexSync:
		if strings.TrimSpace(cfg.OpenAlex.APIKey) == "" {
			return errors.New("OPENALEX_API_KEY is required")
		}
		return validateEmailRequired("OPENALEX_CONTACT_EMAIL", cfg.OpenAlex.ContactEmail)
	case RolePubMedSync, RolePubMedImport:
		if strings.TrimSpace(cfg.PubMed.Tool) == "" {
			return errors.New("NCBI_TOOL is required")
		}
		return validateEmailRequired("NCBI_EMAIL", cfg.PubMed.Email)
	case RoleCrossrefSync:
		return validateEmailRequired("CROSSREF_CONTACT_EMAIL", cfg.Crossref.ContactEmail)
	case RolePMCSync:
		return validateEmailRequired("PMC_CONTACT_EMAIL", cfg.PMC.ContactEmail)
	case RoleSpringerNatureSync:
		if cfg.Publishers.SpringerNature.APIKey == "" {
			return errors.New("SPRINGER_NATURE_API_KEY is required")
		}
		return validateEmailRequired(
			"SPRINGER_NATURE_CONTACT_EMAIL",
			cfg.Publishers.SpringerNature.ContactEmail,
		)
	case RoleElsevierSync:
		if cfg.Publishers.Elsevier.APIKey == "" {
			return errors.New("ELSEVIER_API_KEY is required")
		}
		return validateEmailRequired("ELSEVIER_CONTACT_EMAIL", cfg.Publishers.Elsevier.ContactEmail)
	case RoleJCRImport:
		if cfg.Venues.JCRImportPath == "" {
			return errors.New("JCR_IMPORT_PATH is required")
		}
		if strings.TrimSpace(cfg.Venues.JCRSourceLicense) == "" {
			return errors.New("JCR_SOURCE_LICENSE is required")
		}
		return nil
	default:
		return fmt.Errorf("unsupported configuration role %q", role)
	}
}

func supportedRole(role Role) bool {
	switch role {
	case RoleAPI,
		RoleMigrate,
		RoleCatalogPublish,
		RoleOpenAlexSync,
		RolePubMedSync,
		RolePubMedImport,
		RoleCrossrefSync,
		RolePMCSync,
		RoleSpringerNatureSync,
		RoleElsevierSync,
		RoleJCRImport:
		return true
	default:
		return false
	}
}

func validateCatalogCursorSecret(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("CATALOG_CURSOR_SECRET is required")
	}
	if value != strings.TrimSpace(value) {
		return errors.New("CATALOG_CURSOR_SECRET must be trimmed")
	}
	if len([]byte(value)) < 32 {
		return errors.New("CATALOG_CURSOR_SECRET must contain at least 32 bytes")
	}
	return nil
}

func loadHTTP(lookup LookupEnv) (HTTPConfig, error) {
	host, err := readString(lookup, "API_HOST", defaultAPIHost)
	if err != nil {
		return HTTPConfig{}, err
	}
	if strings.TrimSpace(host) != host || strings.ContainsAny(host, " \t\r\n/") {
		return HTTPConfig{}, errors.New("API_HOST must be a host name or IP address without a port")
	}
	port, err := readInt(lookup, "API_PORT", defaultAPIPort, 1, 65535)
	if err != nil {
		return HTTPConfig{}, err
	}
	readHeaderTimeout, err := readDuration(lookup, "API_READ_HEADER_TIMEOUT", 5*time.Second, time.Second, 30*time.Second)
	if err != nil {
		return HTTPConfig{}, err
	}
	readTimeout, err := readDuration(lookup, "API_READ_TIMEOUT", 15*time.Second, time.Second, 2*time.Minute)
	if err != nil {
		return HTTPConfig{}, err
	}
	writeTimeout, err := readDuration(lookup, "API_WRITE_TIMEOUT", 30*time.Second, time.Second, 5*time.Minute)
	if err != nil {
		return HTTPConfig{}, err
	}
	idleTimeout, err := readDuration(lookup, "API_IDLE_TIMEOUT", time.Minute, time.Second, 10*time.Minute)
	if err != nil {
		return HTTPConfig{}, err
	}
	shutdownTimeout, err := readDuration(lookup, "API_SHUTDOWN_TIMEOUT", 10*time.Second, time.Second, 2*time.Minute)
	if err != nil {
		return HTTPConfig{}, err
	}
	maxHeaderBytes, err := readInt(lookup, "API_MAX_HEADER_BYTES", defaultMaxHeaderBytes, 1024, 16<<20)
	if err != nil {
		return HTTPConfig{}, err
	}
	corsAllowedOrigins, err := parseCORSAllowedOrigins(optional(lookup, "API_CORS_ALLOWED_ORIGINS"))
	if err != nil {
		return HTTPConfig{}, err
	}
	return HTTPConfig{
		Host:               host,
		Port:               port,
		ReadHeaderTimeout:  readHeaderTimeout,
		ReadTimeout:        readTimeout,
		WriteTimeout:       writeTimeout,
		IdleTimeout:        idleTimeout,
		ShutdownTimeout:    shutdownTimeout,
		MaxHeaderBytes:     maxHeaderBytes,
		CORSAllowedOrigins: corsAllowedOrigins,
	}, nil
}

func parseCORSAllowedOrigins(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin == "" {
			return nil, errors.New("API_CORS_ALLOWED_ORIGINS must not contain an empty origin")
		}
		parsed, err := url.Parse(origin)
		if err != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Hostname() == "" ||
			parsed.User != nil ||
			parsed.Path != "" ||
			parsed.RawQuery != "" ||
			parsed.Fragment != "" {
			return nil, fmt.Errorf(
				"API_CORS_ALLOWED_ORIGINS origin %q must be an exact http:// or https:// origin without credentials, path, query, or fragment",
				origin,
			)
		}
		if _, exists := seen[origin]; exists {
			return nil, fmt.Errorf("API_CORS_ALLOWED_ORIGINS contains duplicate origin %q", origin)
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins, nil
}

func loadDatabase(lookup LookupEnv, databaseURL string) (DatabaseConfig, error) {
	minConns, err := readInt(lookup, "DB_MIN_CONNS", 2, 0, 64)
	if err != nil {
		return DatabaseConfig{}, err
	}
	maxConns, err := readInt(lookup, "DB_MAX_CONNS", 16, 1, 256)
	if err != nil {
		return DatabaseConfig{}, err
	}
	if minConns > maxConns {
		return DatabaseConfig{}, errors.New("DB_MIN_CONNS must not exceed DB_MAX_CONNS")
	}
	connectTimeout, err := readDuration(lookup, "DB_CONNECT_TIMEOUT", 5*time.Second, time.Second, time.Minute)
	if err != nil {
		return DatabaseConfig{}, err
	}
	maxConnLifetime, err := readDuration(lookup, "DB_MAX_CONN_LIFETIME", 30*time.Minute, time.Minute, 24*time.Hour)
	if err != nil {
		return DatabaseConfig{}, err
	}
	maxConnIdleTime, err := readDuration(lookup, "DB_MAX_CONN_IDLE_TIME", 5*time.Minute, 30*time.Second, time.Hour)
	if err != nil {
		return DatabaseConfig{}, err
	}
	healthCheckPeriod, err := readDuration(lookup, "DB_HEALTH_CHECK_PERIOD", 30*time.Second, time.Second, 5*time.Minute)
	if err != nil {
		return DatabaseConfig{}, err
	}
	return DatabaseConfig{
		URL:               databaseURL,
		MinConns:          int32(minConns),
		MaxConns:          int32(maxConns),
		ConnectTimeout:    connectTimeout,
		MaxConnLifetime:   maxConnLifetime,
		MaxConnIdleTime:   maxConnIdleTime,
		HealthCheckPeriod: healthCheckPeriod,
	}, nil
}

func loadWorker(lookup LookupEnv) (WorkerConfig, error) {
	concurrency, err := readInt(lookup, "WORKER_CONCURRENCY", 4, 1, 128)
	if err != nil {
		return WorkerConfig{}, err
	}
	batchSize, err := readInt(lookup, "WORKER_BATCH_SIZE", 100, 1, 10000)
	if err != nil {
		return WorkerConfig{}, err
	}
	pollInterval, err := readDuration(lookup, "WORKER_POLL_INTERVAL", 5*time.Second, 100*time.Millisecond, 10*time.Minute)
	if err != nil {
		return WorkerConfig{}, err
	}
	maxRetries, err := readInt(lookup, "WORKER_MAX_RETRIES", 5, 0, 20)
	if err != nil {
		return WorkerConfig{}, err
	}
	maxWait, err := readDuration(lookup, "WORKER_MAX_WAIT", 5*time.Minute, time.Second, 24*time.Hour)
	if err != nil {
		return WorkerConfig{}, err
	}
	return WorkerConfig{
		Concurrency:  concurrency,
		BatchSize:    batchSize,
		PollInterval: pollInterval,
		MaxRetries:   maxRetries,
		MaxWait:      maxWait,
	}, nil
}

func loadRequest(
	lookup LookupEnv,
	prefix string,
	defaultBaseURL string,
	maximumBatchSize int,
) (RequestConfig, error) {
	baseURLKey := prefix + "_BASE_URL"
	baseURL, err := readString(lookup, baseURLKey, defaultBaseURL)
	if err != nil {
		return RequestConfig{}, err
	}
	if err := validateHTTPURL(baseURLKey, baseURL); err != nil {
		return RequestConfig{}, err
	}
	timeout, err := readDuration(lookup, prefix+"_TIMEOUT", 30*time.Second, time.Second, 5*time.Minute)
	if err != nil {
		return RequestConfig{}, err
	}
	maxRetries, err := readInt(lookup, prefix+"_MAX_RETRIES", 5, 0, 20)
	if err != nil {
		return RequestConfig{}, err
	}
	maxWait, err := readDuration(lookup, prefix+"_MAX_WAIT", time.Minute, time.Second, time.Hour)
	if err != nil {
		return RequestConfig{}, err
	}
	batchSize, err := readInt(lookup, prefix+"_BATCH_SIZE", 100, 1, maximumBatchSize)
	if err != nil {
		return RequestConfig{}, err
	}
	return RequestConfig{
		BaseURL:    baseURL,
		Timeout:    timeout,
		MaxRetries: maxRetries,
		MaxWait:    maxWait,
		BatchSize:  batchSize,
	}, nil
}

func validateDatabaseURL(raw string) error {
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		return errors.New("DATABASE_URL must be a valid PostgreSQL URL using postgres:// or postgresql://")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("DATABASE_URL must be a valid PostgreSQL URL")
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return errors.New("DATABASE_URL must use postgres:// or postgresql://")
	}
	if parsed.Hostname() == "" {
		return errors.New("DATABASE_URL must include a host")
	}
	if strings.Trim(parsed.EscapedPath(), "/") == "" {
		return errors.New("DATABASE_URL must include a database name")
	}
	return nil
}

func validateHTTPURL(key, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return fmt.Errorf("%s must be an absolute http:// or https:// URL", key)
	}
	return nil
}

func validateEmailRequired(key, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", key)
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value {
		return fmt.Errorf("%s must be a valid email address", key)
	}
	at := strings.LastIndexByte(value, '@')
	if at <= 0 || at == len(value)-1 {
		return fmt.Errorf("%s must be a valid email address", key)
	}
	return nil
}

func isExplicitCSV(path string) bool {
	if strings.TrimSpace(path) != path || path == "" || strings.ContainsAny(path, "?#") {
		return false
	}
	parsed, err := url.Parse(path)
	if err != nil || parsed.Scheme != "" {
		return false
	}
	return strings.EqualFold(filepath.Ext(path), ".csv")
}

func required(lookup LookupEnv, key string) (string, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func optional(lookup LookupEnv, key string) string {
	value, ok := lookup(key)
	if !ok {
		return ""
	}
	return value
}

func readString(lookup LookupEnv, key, defaultValue string) (string, error) {
	value, ok := lookup(key)
	if !ok {
		return defaultValue, nil
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}
	return value, nil
}

func readEnum(lookup LookupEnv, key, defaultValue string, allowed []string) (string, error) {
	value, err := readString(lookup, key, defaultValue)
	if err != nil {
		return "", err
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s must be one of %s", key, strings.Join(allowed, ", "))
}

func readInt(lookup LookupEnv, key string, defaultValue, minimum, maximum int) (int, error) {
	raw, ok := lookup(key)
	if !ok {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
	}
	return value, nil
}

func readDuration(
	lookup LookupEnv,
	key string,
	defaultValue time.Duration,
	minimum time.Duration,
	maximum time.Duration,
) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok {
		return defaultValue, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be a duration between %s and %s", key, minimum, maximum)
	}
	return value, nil
}

func (cfg Config) Redacted() RedactedConfig {
	return RedactedConfig{
		Environment: cfg.Environment,
		HTTP:        cfg.HTTP,
		Database: RedactedDatabaseConfig{
			URL:               redactURL(cfg.Database.URL),
			MinConns:          cfg.Database.MinConns,
			MaxConns:          cfg.Database.MaxConns,
			ConnectTimeout:    cfg.Database.ConnectTimeout,
			MaxConnLifetime:   cfg.Database.MaxConnLifetime,
			MaxConnIdleTime:   cfg.Database.MaxConnIdleTime,
			HealthCheckPeriod: cfg.Database.HealthCheckPeriod,
		},
		Catalog: RedactedCatalogConfig{
			CursorSecret: redactedSecret(cfg.Catalog.CursorSecret),
		},
		Worker: cfg.Worker,
		OpenAlex: RedactedOpenAlexConfig{
			Request:      cfg.OpenAlex.Request,
			ContactEmail: cfg.OpenAlex.ContactEmail,
			APIKey:       redactedSecret(cfg.OpenAlex.APIKey),
		},
		PubMed: RedactedPubMedConfig{
			Request: cfg.PubMed.Request,
			Tool:    cfg.PubMed.Tool,
			Email:   cfg.PubMed.Email,
			APIKey:  redactedSecret(cfg.PubMed.APIKey),
		},
		Crossref: cfg.Crossref,
		PMC:      cfg.PMC,
		Publishers: RedactedPublisherConfig{
			SpringerNature: RedactedPublisherSourceConfig{
				Request:      cfg.Publishers.SpringerNature.Request,
				ContactEmail: cfg.Publishers.SpringerNature.ContactEmail,
				APIKey:       redactedSecret(cfg.Publishers.SpringerNature.APIKey),
			},
			Elsevier: RedactedPublisherSourceConfig{
				Request:      cfg.Publishers.Elsevier.Request,
				ContactEmail: cfg.Publishers.Elsevier.ContactEmail,
				APIKey:       redactedSecret(cfg.Publishers.Elsevier.APIKey),
			},
		},
		Venues: cfg.Venues,
	}
}

func (cfg Config) String() string {
	encoded, err := json.Marshal(cfg.Redacted())
	if err != nil {
		return `{"error":"configuration formatting failed"}`
	}
	return string(encoded)
}

func (cfg Config) MarshalJSON() ([]byte, error) {
	return json.Marshal(cfg.Redacted())
}

func redactedSecret(value string) string {
	if value == "" {
		return ""
	}
	return "[REDACTED]"
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[REDACTED]"
	}
	if parsed.User != nil {
		username := parsed.User.Username()
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword(username, "[REDACTED]")
		}
	}
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "password") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "token") ||
			strings.Contains(lower, "key") {
			query.Set(key, "[REDACTED]")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
