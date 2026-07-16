package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const testDatabaseURL = "postgres://paper_user:database-password@localhost:5432/paper_hub?sslmode=disable"
const testCatalogCursorSecret = "test-catalog-cursor-secret-32-bytes"

func TestLoadForAPIRequiresDatabaseAndCatalogCursorSecret(t *testing.T) {
	cfg, err := LoadFrom(RoleAPI, envMap(
		"DATABASE_URL", testDatabaseURL,
	))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.Database.URL != testDatabaseURL {
		t.Errorf("Database.URL = %q, want supplied URL", cfg.Database.URL)
	}
	if cfg.HTTP.Address() != "0.0.0.0:8080" {
		t.Errorf("HTTP.Address() = %q, want %q", cfg.HTTP.Address(), "0.0.0.0:8080")
	}
	if cfg.Catalog.CursorSecret != testCatalogCursorSecret {
		t.Fatal("API configuration did not preserve CATALOG_CURSOR_SECRET")
	}
	if cfg.OpenAlex.ContactEmail != "" || cfg.PubMed.Email != "" ||
		cfg.Crossref.ContactEmail != "" || cfg.Publishers.SpringerNature.APIKey != "" {
		t.Fatal("API configuration unexpectedly required or populated ingestion credentials")
	}
}

func TestLoadForAPIParsesExplicitCORSAllowedOrigins(t *testing.T) {
	cfg, err := LoadFrom(RoleAPI, envMap(
		"DATABASE_URL", testDatabaseURL,
		"API_CORS_ALLOWED_ORIGINS", "http://localhost:3000,https://papers.example.test",
	))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	want := []string{"http://localhost:3000", "https://papers.example.test"}
	if len(cfg.HTTP.CORSAllowedOrigins) != len(want) {
		t.Fatalf("HTTP.CORSAllowedOrigins = %#v, want %#v", cfg.HTTP.CORSAllowedOrigins, want)
	}
	for index := range want {
		if cfg.HTTP.CORSAllowedOrigins[index] != want[index] {
			t.Fatalf("HTTP.CORSAllowedOrigins = %#v, want %#v", cfg.HTTP.CORSAllowedOrigins, want)
		}
	}
}

func TestLoadForAPIRejectsInvalidCORSAllowedOrigin(t *testing.T) {
	tests := []string{
		"*",
		"localhost:3000",
		"http://localhost:3000/path",
		"https://user@example.test",
	}

	for _, origin := range tests {
		t.Run(origin, func(t *testing.T) {
			_, err := LoadFrom(RoleAPI, envMap(
				"DATABASE_URL", testDatabaseURL,
				"API_CORS_ALLOWED_ORIGINS", origin,
			))
			if err == nil || !strings.Contains(err.Error(), "API_CORS_ALLOWED_ORIGINS") {
				t.Fatalf("LoadFrom() error = %v, want invalid CORS origin error", err)
			}
		})
	}
}

func TestLoadForAPIRejectsInvalidCatalogCursorSecretWithoutLeakingIt(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		present bool
		want    string
	}{
		{
			name: "missing",
			want: "CATALOG_CURSOR_SECRET is required",
		},
		{
			name:    "blank",
			secret:  " \t ",
			present: true,
			want:    "CATALOG_CURSOR_SECRET is required",
		},
		{
			name:    "short",
			secret:  strings.Repeat("s", 31),
			present: true,
			want:    "CATALOG_CURSOR_SECRET must contain at least 32 bytes",
		},
		{
			name:    "not trimmed",
			secret:  " " + strings.Repeat("s", 32),
			present: true,
			want:    "CATALOG_CURSOR_SECRET must be trimmed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := map[string]string{"DATABASE_URL": testDatabaseURL}
			if tt.present {
				values["CATALOG_CURSOR_SECRET"] = tt.secret
			}

			_, err := LoadFrom(RoleAPI, mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadFrom() error = %v, want containing %q", err, tt.want)
			}
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Fatalf("LoadFrom() error leaked CATALOG_CURSOR_SECRET: %v", err)
			}
		})
	}
}

func TestLoadForAPIMeasuresCatalogCursorSecretInBytes(t *testing.T) {
	secret := strings.Repeat("密", 11)
	cfg, err := LoadFrom(RoleAPI, mapLookup(map[string]string{
		"DATABASE_URL":          testDatabaseURL,
		"CATALOG_CURSOR_SECRET": secret,
	}))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Catalog.CursorSecret != secret {
		t.Fatal("Catalog.CursorSecret did not preserve the supplied UTF-8 bytes")
	}
}

func TestLoadForMigrateDoesNotRequireOpenAlexCredentials(t *testing.T) {
	cfg, err := LoadFrom(RoleMigrate, envMap(
		"DATABASE_URL", testDatabaseURL,
	))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.OpenAlex.APIKey != "" || cfg.OpenAlex.ContactEmail != "" {
		t.Fatalf("migrate configuration unexpectedly populated OpenAlex credentials: %+v", cfg.Redacted())
	}
}

func TestLoadForCatalogPublishRequiresOnlyDatabase(t *testing.T) {
	cfg, err := LoadFrom(RoleCatalogPublish, mapLookup(map[string]string{
		"DATABASE_URL": testDatabaseURL,
	}))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Database.URL != testDatabaseURL {
		t.Errorf("Database.URL = %q, want supplied URL", cfg.Database.URL)
	}
	if cfg.Catalog.CursorSecret != "" {
		t.Fatal("catalog publish unexpectedly required CATALOG_CURSOR_SECRET")
	}
	if cfg.OpenAlex.APIKey != "" ||
		cfg.PubMed.APIKey != "" ||
		cfg.Publishers.SpringerNature.APIKey != "" ||
		cfg.Publishers.Elsevier.APIKey != "" {
		t.Fatalf("catalog publish unexpectedly populated source credentials: %+v", cfg.Redacted())
	}
}

func TestLoadRequiresPostgreSQLDatabaseURLWithoutLeakingIt(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "missing", url: "", want: "DATABASE_URL is required"},
		{name: "wrong scheme", url: "mysql://root:mysql-secret@localhost/papers", want: "postgres:// or postgresql://"},
		{name: "relative", url: "postgres:paper-secret", want: "valid PostgreSQL URL"},
		{name: "missing host", url: "postgres://paper:paper-secret@/papers", want: "host"},
		{name: "missing database", url: "postgres://paper:paper-secret@localhost", want: "database name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFrom(RoleAPI, envMap("DATABASE_URL", tt.url))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadFrom() error = %v, want containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "paper-secret") || strings.Contains(err.Error(), "mysql-secret") {
				t.Fatalf("LoadFrom() error leaked a database secret: %v", err)
			}
		})
	}
}

func TestLoadValidatesCredentialsOnlyForSelectedRole(t *testing.T) {
	tests := []struct {
		role Role
		env  map[string]string
		want string
	}{
		{role: RoleOpenAlexSync, want: "OPENALEX_API_KEY"},
		{
			role: RoleOpenAlexSync,
			env:  map[string]string{"OPENALEX_API_KEY": "openalex-secret"},
			want: "OPENALEX_CONTACT_EMAIL",
		},
		{role: RolePubMedSync, want: "NCBI_TOOL"},
		{role: RolePubMedImport, env: map[string]string{"NCBI_TOOL": "paper-hub"}, want: "NCBI_EMAIL"},
		{role: RoleCrossrefSync, want: "CROSSREF_CONTACT_EMAIL"},
		{role: RolePMCSync, want: "PMC_CONTACT_EMAIL"},
		{role: RoleSpringerNatureSync, want: "SPRINGER_NATURE_API_KEY"},
		{
			role: RoleSpringerNatureSync,
			env:  map[string]string{"SPRINGER_NATURE_API_KEY": "springer-secret"},
			want: "SPRINGER_NATURE_CONTACT_EMAIL",
		},
		{role: RoleElsevierSync, want: "ELSEVIER_API_KEY"},
		{
			role: RoleElsevierSync,
			env:  map[string]string{"ELSEVIER_API_KEY": "elsevier-secret"},
			want: "ELSEVIER_CONTACT_EMAIL",
		},
		{role: RoleJCRImport, want: "JCR_IMPORT_PATH"},
		{
			role: RoleJCRImport,
			env:  map[string]string{"JCR_IMPORT_PATH": "/authorized/jcr-export.csv"},
			want: "JCR_SOURCE_LICENSE",
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			values := map[string]string{"DATABASE_URL": testDatabaseURL}
			for key, value := range tt.env {
				values[key] = value
			}

			_, err := LoadFrom(tt.role, mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadFrom(%q) error = %v, want containing %q", tt.role, err, tt.want)
			}
			if strings.Contains(err.Error(), "openalex-secret") ||
				strings.Contains(err.Error(), "springer-secret") ||
				strings.Contains(err.Error(), "elsevier-secret") {
				t.Fatalf("LoadFrom() error leaked an API key: %v", err)
			}
		})
	}
}

func TestLoadAcceptsRoleSpecificCredentials(t *testing.T) {
	tests := []struct {
		role Role
		env  map[string]string
	}{
		{
			role: RoleOpenAlexSync,
			env: map[string]string{
				"OPENALEX_API_KEY":       "openalex-secret",
				"OPENALEX_CONTACT_EMAIL": "openalex@example.test",
			},
		},
		{
			role: RolePubMedSync,
			env: map[string]string{
				"NCBI_TOOL":    "paper-hub",
				"NCBI_EMAIL":   "pubmed@example.test",
				"NCBI_API_KEY": "optional-ncbi-secret",
			},
		},
		{
			role: RolePubMedImport,
			env: map[string]string{
				"NCBI_TOOL":  "paper-hub",
				"NCBI_EMAIL": "pubmed@example.test",
			},
		},
		{role: RoleCrossrefSync, env: map[string]string{"CROSSREF_CONTACT_EMAIL": "crossref@example.test"}},
		{role: RolePMCSync, env: map[string]string{"PMC_CONTACT_EMAIL": "pmc@example.test"}},
		{
			role: RoleSpringerNatureSync,
			env: map[string]string{
				"SPRINGER_NATURE_API_KEY":       "springer-secret",
				"SPRINGER_NATURE_CONTACT_EMAIL": "springer@example.test",
			},
		},
		{
			role: RoleElsevierSync,
			env: map[string]string{
				"ELSEVIER_API_KEY":       "elsevier-secret",
				"ELSEVIER_CONTACT_EMAIL": "elsevier@example.test",
			},
		},
		{
			role: RoleJCRImport,
			env: map[string]string{
				"JCR_IMPORT_PATH":    "/authorized/jcr-export.csv",
				"JCR_SOURCE_LICENSE": "institutional-jcr-license",
			},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			values := map[string]string{"DATABASE_URL": testDatabaseURL}
			for key, value := range tt.env {
				values[key] = value
			}
			if _, err := LoadFrom(tt.role, mapLookup(values)); err != nil {
				t.Fatalf("LoadFrom(%q) error = %v", tt.role, err)
			}
		})
	}
}

func TestLoadRejectsInvalidEmailForSelectedRole(t *testing.T) {
	tests := []struct {
		role     Role
		emailKey string
		extra    map[string]string
	}{
		{
			role:     RoleOpenAlexSync,
			emailKey: "OPENALEX_CONTACT_EMAIL",
			extra:    map[string]string{"OPENALEX_API_KEY": "secret"},
		},
		{role: RolePubMedSync, emailKey: "NCBI_EMAIL", extra: map[string]string{"NCBI_TOOL": "paper-hub"}},
		{role: RoleCrossrefSync, emailKey: "CROSSREF_CONTACT_EMAIL"},
		{role: RolePMCSync, emailKey: "PMC_CONTACT_EMAIL"},
		{
			role:     RoleSpringerNatureSync,
			emailKey: "SPRINGER_NATURE_CONTACT_EMAIL",
			extra:    map[string]string{"SPRINGER_NATURE_API_KEY": "secret"},
		},
		{
			role:     RoleElsevierSync,
			emailKey: "ELSEVIER_CONTACT_EMAIL",
			extra:    map[string]string{"ELSEVIER_API_KEY": "secret"},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			values := map[string]string{
				"DATABASE_URL": testDatabaseURL,
				tt.emailKey:    "not-an-email",
			}
			for key, value := range tt.extra {
				values[key] = value
			}
			_, err := LoadFrom(tt.role, mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), tt.emailKey+" must be a valid email address") {
				t.Fatalf("LoadFrom() error = %v, want invalid email error for %s", err, tt.emailKey)
			}
		})
	}
}

func TestOpenAlexSyncRejectsBlankAPIKey(t *testing.T) {
	_, err := LoadFrom(RoleOpenAlexSync, envMap(
		"DATABASE_URL", testDatabaseURL,
		"OPENALEX_API_KEY", " \t ",
		"OPENALEX_CONTACT_EMAIL", "openalex@example.test",
	))
	if err == nil || !strings.Contains(err.Error(), "OPENALEX_API_KEY is required") {
		t.Fatalf("LoadFrom() error = %v, want blank OpenAlex API key rejection", err)
	}
}

func TestLoadRejectsUnknownRole(t *testing.T) {
	_, err := LoadFrom(Role("unknown-command"), envMap("DATABASE_URL", testDatabaseURL))
	if err == nil || !strings.Contains(err.Error(), "unsupported configuration role") {
		t.Fatalf("LoadFrom() error = %v, want unsupported role", err)
	}
}

func TestLoadRejectsValuesOutsideStrictBounds(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "API_PORT", value: "0"},
		{key: "API_READ_HEADER_TIMEOUT", value: "500ms"},
		{key: "API_READ_TIMEOUT", value: "10m"},
		{key: "API_MAX_HEADER_BYTES", value: "1023"},
		{key: "DB_MIN_CONNS", value: "65"},
		{key: "DB_MAX_CONNS", value: "257"},
		{key: "DB_CONNECT_TIMEOUT", value: "500ms"},
		{key: "DB_MAX_CONN_LIFETIME", value: "25h"},
		{key: "DB_MAX_CONN_IDLE_TIME", value: "10s"},
		{key: "DB_HEALTH_CHECK_PERIOD", value: "500ms"},
		{key: "WORKER_CONCURRENCY", value: "0"},
		{key: "WORKER_BATCH_SIZE", value: "10001"},
		{key: "WORKER_POLL_INTERVAL", value: "50ms"},
		{key: "WORKER_MAX_RETRIES", value: "21"},
		{key: "WORKER_MAX_WAIT", value: "25h"},
		{key: "OPENALEX_TIMEOUT", value: "500ms"},
		{key: "OPENALEX_BATCH_SIZE", value: "101"},
		{key: "PUBMED_BATCH_SIZE", value: "1001"},
		{key: "CROSSREF_MAX_RETRIES", value: "21"},
		{key: "PMC_MAX_WAIT", value: "500ms"},
		{key: "SPRINGER_NATURE_TIMEOUT", value: "6m"},
		{key: "ELSEVIER_BATCH_SIZE", value: "0"},
	}

	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			_, err := LoadFrom(RoleAPI, envMap(
				"DATABASE_URL", testDatabaseURL,
				tt.key, tt.value,
			))
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("LoadFrom() error = %v, want strict bound error for %s", err, tt.key)
			}
		})
	}
}

func TestLoadRejectsMinimumConnectionsAboveMaximum(t *testing.T) {
	_, err := LoadFrom(RoleAPI, envMap(
		"DATABASE_URL", testDatabaseURL,
		"DB_MIN_CONNS", "17",
		"DB_MAX_CONNS", "16",
	))
	if err == nil || !strings.Contains(err.Error(), "DB_MIN_CONNS must not exceed DB_MAX_CONNS") {
		t.Fatalf("LoadFrom() error = %v, want connection ordering error", err)
	}
}

func TestLoadParsesExplicitBoundedConfiguration(t *testing.T) {
	cfg, err := LoadFrom(RoleAPI, envMap(
		"DATABASE_URL", testDatabaseURL,
		"APP_ENV", "production",
		"API_HOST", "127.0.0.1",
		"API_PORT", "9090",
		"API_SHUTDOWN_TIMEOUT", "20s",
		"DB_MIN_CONNS", "3",
		"DB_MAX_CONNS", "24",
		"DB_CONNECT_TIMEOUT", "7s",
		"WORKER_CONCURRENCY", "8",
		"WORKER_BATCH_SIZE", "250",
		"WORKER_MAX_RETRIES", "7",
		"WORKER_MAX_WAIT", "30m",
		"OPENALEX_TIMEOUT", "45s",
	))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.Environment != "production" || cfg.HTTP.Address() != "127.0.0.1:9090" {
		t.Fatalf("unexpected application/HTTP config: %+v", cfg.Redacted())
	}
	if cfg.HTTP.ShutdownTimeout != 20*time.Second ||
		cfg.Database.MinConns != 3 ||
		cfg.Database.MaxConns != 24 ||
		cfg.Database.ConnectTimeout != 7*time.Second ||
		cfg.Worker.Concurrency != 8 ||
		cfg.Worker.BatchSize != 250 ||
		cfg.Worker.MaxRetries != 7 ||
		cfg.Worker.MaxWait != 30*time.Minute ||
		cfg.OpenAlex.Request.Timeout != 45*time.Second {
		t.Fatalf("explicit configuration was not preserved: %+v", cfg.Redacted())
	}
}

func TestJCRImportPathMustBeExplicitCSV(t *testing.T) {
	tests := []string{
		"openalex://venue-metrics",
		"/authorized/jcr-export.tsv",
		"/authorized/jcr-export.csv?fallback=openalex",
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			_, err := LoadFrom(RoleJCRImport, envMap(
				"DATABASE_URL", testDatabaseURL,
				"JCR_IMPORT_PATH", path,
			))
			if err == nil || !strings.Contains(err.Error(), "JCR_IMPORT_PATH must reference an explicit .csv file") {
				t.Fatalf("LoadFrom() error = %v, want explicit CSV error", err)
			}
		})
	}

	cfg, err := LoadFrom(RoleAPI, envMap("DATABASE_URL", testDatabaseURL))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Venues.JCRImportPath != "" {
		t.Fatalf("JCRImportPath = %q, want no implicit fallback", cfg.Venues.JCRImportPath)
	}
}

func TestFormattedAndJSONConfigurationRedactSecrets(t *testing.T) {
	cfg, err := LoadFrom(RolePubMedSync, envMap(
		"DATABASE_URL", testDatabaseURL,
		"NCBI_TOOL", "paper-hub",
		"NCBI_EMAIL", "pubmed@example.test",
		"NCBI_API_KEY", "ncbi-api-secret",
		"OPENALEX_API_KEY", "openalex-api-secret",
		"SPRINGER_NATURE_API_KEY", "springer-api-secret",
		"ELSEVIER_API_KEY", "elsevier-api-secret",
		"CATALOG_CURSOR_SECRET", "catalog-cursor-secret-that-must-not-leak",
	))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	formatted := cfg.String()
	goFormatted := fmt.Sprintf("%+v", cfg)
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, output := range []string{formatted, goFormatted, string(encoded)} {
		for _, secret := range []string{
			"database-password",
			"ncbi-api-secret",
			"openalex-api-secret",
			"springer-api-secret",
			"elsevier-api-secret",
			"catalog-cursor-secret-that-must-not-leak",
		} {
			if strings.Contains(output, secret) {
				t.Fatalf("formatted config leaked %q: %s", secret, output)
			}
		}
	}
	if !strings.Contains(formatted, "postgres://paper_user:%5BREDACTED%5D@localhost:5432/paper_hub") {
		t.Fatalf("String() = %s, want redacted database URL", formatted)
	}
	if !strings.Contains(formatted, `"cursor_secret":"[REDACTED]"`) {
		t.Fatalf("String() = %s, want explicitly redacted catalog cursor secret", formatted)
	}
}

func envMap(pairs ...string) LookupEnv {
	if len(pairs)%2 != 0 {
		panic("envMap requires key/value pairs")
	}
	values := make(map[string]string, len(pairs)/2+1)
	values["CATALOG_CURSOR_SECRET"] = testCatalogCursorSecret
	for index := 0; index < len(pairs); index += 2 {
		values[pairs[index]] = pairs[index+1]
	}
	return mapLookup(values)
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
