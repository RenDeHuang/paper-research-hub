package biomed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

func TestParseSubjectRegistryRequiresExactVersionedCSV(t *testing.T) {
	t.Run("preserves reviewed category bytes and file identity", func(t *testing.T) {
		contents := []byte(
			"source,registry_version,slug,display_label,jcr_category\n" +
				"medpaperhub-reviewed-jcr-category-allowlist,biomedical-jcr-subjects/v1,oncology,Oncology,Oncology\n" +
				"medpaperhub-reviewed-jcr-category-allowlist,biomedical-jcr-subjects/v1,genetics-heredity,Genetics & Heredity,Genetics & Heredity\n",
		)
		registry, err := ParseSubjectRegistry(bytes.NewReader(contents))
		if err != nil {
			t.Fatalf("ParseSubjectRegistry() error = %v", err)
		}
		digest := sha256.Sum256(contents)
		if registry.Source() != "medpaperhub-reviewed-jcr-category-allowlist" ||
			registry.Version() != "biomedical-jcr-subjects/v1" ||
			registry.FileSHA256() != stringHex(digest[:]) ||
			registry.SubjectCount() != 2 ||
			registry.RuleCount() != 2 {
			t.Fatalf("registry metadata = %#v", registry)
		}
		rules := registry.Rules()
		if len(rules) != 2 ||
			rules[0].Slug() != "oncology" ||
			rules[0].DisplayLabel() != "Oncology" ||
			rules[0].JCRCategory() != "Oncology" ||
			rules[1].Slug() != "genetics-heredity" ||
			rules[1].DisplayLabel() != "Genetics & Heredity" ||
			rules[1].JCRCategory() != "Genetics & Heredity" {
			t.Fatalf("registry rules = %#v", rules)
		}
	})

	tests := []struct {
		name string
		csv  string
		want string
	}{
		{
			name: "missing explicit version",
			csv: "source,registry_version,slug,display_label,jcr_category\n" +
				"reviewed-source,,oncology,Oncology,Oncology\n",
			want: "registry_version",
		},
		{
			name: "mixed versions",
			csv: "source,registry_version,slug,display_label,jcr_category\n" +
				"reviewed-source,biomedical/v1,oncology,Oncology,Oncology\n" +
				"reviewed-source,biomedical/v2,immunology,Immunology,Immunology\n",
			want: "single registry_version",
		},
		{
			name: "header case changed",
			csv: "source,registry_version,slug,display_label,JCR_Category\n" +
				"reviewed-source,biomedical/v1,oncology,Oncology,Oncology\n",
			want: "exact header",
		},
		{
			name: "header reordered",
			csv: "registry_version,source,slug,display_label,jcr_category\n" +
				"biomedical/v1,reviewed-source,oncology,Oncology,Oncology\n",
			want: "exact header",
		},
		{
			name: "extra header",
			csv: "source,registry_version,slug,display_label,jcr_category,alias\n" +
				"reviewed-source,biomedical/v1,oncology,Oncology,Oncology,cancer\n",
			want: "exact header",
		},
		{
			name: "duplicate slug",
			csv: "source,registry_version,slug,display_label,jcr_category\n" +
				"reviewed-source,biomedical/v1,oncology,Oncology,Oncology\n" +
				"reviewed-source,biomedical/v1,oncology,Cancer Biology,Cancer Biology\n",
			want: "duplicate slug",
		},
		{
			name: "duplicate category in version",
			csv: "source,registry_version,slug,display_label,jcr_category\n" +
				"reviewed-source,biomedical/v1,oncology,Oncology,Oncology\n" +
				"reviewed-source,biomedical/v1,cancer-biology,Cancer Biology,Oncology\n",
			want: "duplicate JCR Category",
		},
		{
			name: "slug is not stable lowercase form",
			csv: "source,registry_version,slug,display_label,jcr_category\n" +
				"reviewed-source,biomedical/v1,Oncology,Oncology,Oncology\n",
			want: "slug",
		},
		{
			name: "category has leading space",
			csv: "source,registry_version,slug,display_label,jcr_category\n" +
				"reviewed-source,biomedical/v1,oncology,Oncology, Oncology\n",
			want: "jcr_category",
		},
		{
			name: "category has trailing space",
			csv: "source,registry_version,slug,display_label,jcr_category\n" +
				"reviewed-source,biomedical/v1,oncology,Oncology,Oncology \n",
			want: "jcr_category",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseSubjectRegistry(strings.NewReader(test.csv))
			if !errors.Is(err, ErrInvalidSubjectRegistry) {
				t.Fatalf("ParseSubjectRegistry() error = %v, want ErrInvalidSubjectRegistry", err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseSubjectRegistry() error = %q, want containing %q", err, test.want)
			}
		})
	}
}

func TestCommittedSubjectRegistryContainsReviewedExactJCRCategories(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve taxonomy_test.go path")
	}
	path := filepath.Join(
		filepath.Dir(currentFile),
		"..",
		"..",
		"..",
		"..",
		"data",
		"subjects",
		"biomedical-jcr-subjects.v1.csv",
	)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed Subject registry: %v", err)
	}
	registry, err := ParseSubjectRegistry(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("ParseSubjectRegistry(committed) error = %v", err)
	}
	if registry.Source() != "medpaperhub-reviewed-jcr-category-allowlist" ||
		registry.Version() != "biomedical-jcr-subjects/v1" {
		t.Fatalf(
			"committed registry source/version = %q/%q",
			registry.Source(),
			registry.Version(),
		)
	}
	got := make([]string, 0, registry.RuleCount())
	for _, rule := range registry.Rules() {
		got = append(got, rule.JCRCategory())
	}
	want := []string{
		"Oncology",
		"Immunology",
		"Neurosciences",
		"Genetics & Heredity",
		"Cell Biology",
		"Biochemistry & Molecular Biology",
		"Pharmacology & Pharmacy",
		"Medical Informatics",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("committed exact JCR Categories = %#v, want %#v", got, want)
	}
}

func TestExactDomainRegistryReconcilesThroughLegacyJCRImportBoundary(t *testing.T) {
	pool := openBiomedScopeTestPool(t)
	importer, err := scope.NewPostgresDomainImporter(
		pool,
		func() time.Time {
			return time.Date(2026, time.July, 18, 6, 0, 0, 0, time.UTC)
		},
	)
	if err != nil {
		t.Fatalf("NewPostgresDomainImporter() error = %v", err)
	}
	registry := "" +
		"registry_name,registry_version,domain,display_label,jcr_category,article_level_required\n" +
		"medpaperhub-research-domains,research-domains-jcr-subjects/v2,medicine,Medicine,\"Medicine, General & Internal\",false\n" +
		"medpaperhub-research-domains,research-domains-jcr-subjects/v2,biology,Biology,Biology,false\n" +
		"medpaperhub-research-domains,research-domains-jcr-subjects/v2,computer_science,Computer Science,\"Computer Science, Artificial Intelligence\",false\n"
	if _, err := importer.Import(
		context.Background(),
		strings.NewReader(registry),
	); err != nil {
		t.Fatalf("Import(domain Registry) error = %v", err)
	}

	ctx := biomedScopeTestContext(t)
	var venueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l)
		VALUES ('journal', 'Domain bridge journal', '2468-1357')
		RETURNING id::text
	`).Scan(&venueID); err != nil {
		t.Fatalf("insert bridge Venue: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, registry_version, edition_year,
			jif_rank, category_journal_count, jif_percentile
		) VALUES (
			$1, 2025, 'Medicine, General & Internal',
			5, 'Q1', 'known', 'authorized-jcr', 'authorized-license',
			'jcr-registry/v2', 2026, 1, 100, 99
		)
	`, venueID); err != nil {
		t.Fatalf("insert bridge Venue metric: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Registry reconciliation: %v", err)
	}
	if _, err := ReconcileJournalSubjectMetrics(ctx, tx); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("ReconcileJournalSubjectMetrics() error = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit Registry reconciliation: %v", err)
	}

	var links int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM journal_domain_metrics AS link
		JOIN domain_category_rules AS rule
		  ON rule.id = link.domain_category_rule_id
		WHERE link.jcr_category = 'Medicine, General & Internal'
		  AND NOT rule.article_level_required
	`).Scan(&links); err != nil {
		t.Fatalf("query bridged domain links: %v", err)
	}
	if links != 1 {
		t.Fatalf("bridged exact domain links = %d, want 1", links)
	}
}

func TestResearchDomainRegistryCannotUseLegacyBiomedicalEligibility(t *testing.T) {
	err := validatePublicEligibilityIdentity(
		"00000000-0000-0000-0000-000000000901",
		BiomedicalPublicEligibilityPolicyVersion,
		2025,
		scope.ResearchDomainRegistryVersion,
	)
	if err == nil || !strings.Contains(err.Error(), "channel admission") {
		t.Fatalf(
			"validatePublicEligibilityIdentity(research domains) error = %v, want channel admission boundary",
			err,
		)
	}
}

func stringHex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = digits[item>>4]
		result[index*2+1] = digits[item&0x0f]
	}
	return string(result)
}
