package biomed

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
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

func stringHex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = digits[item>>4]
		result[index*2+1] = digits[item&0x0f]
	}
	return string(result)
}
