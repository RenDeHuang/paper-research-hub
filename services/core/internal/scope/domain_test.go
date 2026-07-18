package scope

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestResearchDomainIdentityIsClosedAndExact(t *testing.T) {
	t.Parallel()

	if got, want := ResearchDomains(), []ResearchDomain{
		ResearchDomainMedicine,
		ResearchDomainBiology,
		ResearchDomainComputerScience,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ResearchDomains() = %#v, want %#v", got, want)
	}

	for _, domain := range ResearchDomains() {
		parsed, err := ParseResearchDomain(string(domain))
		if err != nil {
			t.Fatalf("ParseResearchDomain(%q) error = %v", domain, err)
		}
		if parsed != domain {
			t.Fatalf("ParseResearchDomain(%q) = %q", domain, parsed)
		}
	}

	for _, value := range []string{
		"biomedical",
		"computer-science",
		"Computer_Science",
		" computer_science",
		"computer_science ",
		"",
	} {
		if _, err := ParseResearchDomain(value); !errors.Is(err, ErrInvalidResearchDomain) {
			t.Fatalf(
				"ParseResearchDomain(%q) error = %v, want ErrInvalidResearchDomain",
				value,
				err,
			)
		}
	}
}

func TestExactDomainRegistryPreservesExplicitManyDomainCategoryRows(t *testing.T) {
	t.Parallel()

	contents := []byte(
		"registry_name,registry_version,domain,display_label,jcr_category,article_level_required\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,medicine,Medicine,Medicine; General & Internal,false\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,biology,Biology,Biology,false\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,computer_science,Computer Science,Computer Science; Artificial Intelligence,false\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,medicine,Medicine,Multidisciplinary Sciences,true\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,biology,Biology,Multidisciplinary Sciences,true\n",
	)
	registry, err := ParseDomainRegistry(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("ParseDomainRegistry() error = %v", err)
	}
	digest := sha256.Sum256(contents)
	if registry.Name() != "medpaperhub-research-domains" ||
		registry.Version() != ResearchDomainRegistryVersion ||
		registry.FileSHA256() != stringHex(digest[:]) ||
		registry.DomainCount() != 3 ||
		registry.RuleCount() != 5 {
		t.Fatalf("registry metadata = %#v", registry)
	}

	matches := registry.MatchJCRCategory("Multidisciplinary Sciences")
	if got, want := ruleDomains(matches), []ResearchDomain{
		ResearchDomainMedicine,
		ResearchDomainBiology,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit multidisciplinary matches = %#v, want %#v", got, want)
	}
	for _, match := range matches {
		if !match.ArticleLevelRequired() {
			t.Fatalf("multidisciplinary rule = %#v, want article-level requirement", match)
		}
	}
	if automatic := registry.AutomaticDomainsForJCRCategory(
		"Multidisciplinary Sciences",
	); len(automatic) != 0 {
		t.Fatalf(
			"AutomaticDomainsForJCRCategory(multidisciplinary) = %#v, want none",
			automatic,
		)
	}
	if got, want := registry.AutomaticDomainsForJCRCategory(
		"Medicine; General & Internal",
	), []ResearchDomain{ResearchDomainMedicine}; !reflect.DeepEqual(got, want) {
		t.Fatalf("automatic exact domain = %#v, want %#v", got, want)
	}
}

func TestExactDomainRegistryRejectsNormalizationAndImplicitDomains(t *testing.T) {
	t.Parallel()

	validRows := "" +
		"medpaperhub-research-domains,research-domains-jcr-subjects/v2,medicine,Medicine,Medicine; General & Internal,false\n" +
		"medpaperhub-research-domains,research-domains-jcr-subjects/v2,biology,Biology,Biology,false\n" +
		"medpaperhub-research-domains,research-domains-jcr-subjects/v2,computer_science,Computer Science,Computer Science; Artificial Intelligence,false\n"
	valid := "registry_name,registry_version,domain,display_label,jcr_category,article_level_required\n" +
		validRows
	registry, err := ParseDomainRegistry(strings.NewReader(valid))
	if err != nil {
		t.Fatalf("ParseDomainRegistry(valid) error = %v", err)
	}
	for _, category := range []string{
		"medicine; general & internal",
		" Medicine; General & Internal",
		"Medicine; General & Internal ",
		"Medicine General and Internal",
		"Unknown Category",
	} {
		if matches := registry.MatchJCRCategory(category); len(matches) != 0 {
			t.Fatalf("MatchJCRCategory(%q) = %#v, want exact rejection", category, matches)
		}
	}

	tests := []struct {
		name string
		csv  string
		want string
	}{
		{
			name: "missing computer science identity",
			csv: "registry_name,registry_version,domain,display_label,jcr_category,article_level_required\n" +
				"medpaperhub-research-domains,research-domains-jcr-subjects/v2,medicine,Medicine,Medicine,false\n" +
				"medpaperhub-research-domains,research-domains-jcr-subjects/v2,biology,Biology,Biology,false\n",
			want: "exactly three",
		},
		{
			name: "hyphenated database identity",
			csv: strings.Replace(
				valid,
				"computer_science,Computer Science",
				"computer-science,Computer Science",
				1,
			),
			want: "domain",
		},
		{
			name: "category whitespace repair",
			csv: strings.Replace(
				valid,
				"Medicine; General & Internal,false",
				" Medicine; General & Internal,false",
				1,
			),
			want: "jcr_category",
		},
		{
			name: "duplicate domain category row",
			csv: valid +
				"medpaperhub-research-domains,research-domains-jcr-subjects/v2,medicine,Medicine,Medicine; General & Internal,false\n",
			want: "duplicate domain/category",
		},
		{
			name: "conflicting display label",
			csv: valid +
				"medpaperhub-research-domains,research-domains-jcr-subjects/v2,medicine,Medical Science,Clinical Medicine,false\n",
			want: "display_label",
		},
		{
			name: "non exact boolean",
			csv:  strings.Replace(valid, ",false\n", ",FALSE\n", 1),
			want: "article_level_required",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseDomainRegistry(strings.NewReader(test.csv))
			if !errors.Is(err, ErrInvalidDomainRegistry) {
				t.Fatalf("ParseDomainRegistry() error = %v, want invalid registry", err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseDomainRegistry() error = %q, want %q", err, test.want)
			}
		})
	}
}

func ruleDomains(rules []DomainCategoryRule) []ResearchDomain {
	result := make([]ResearchDomain, 0, len(rules))
	for _, rule := range rules {
		result = append(result, rule.Domain())
	}
	return result
}

func stringHex(value []byte) string {
	return hex.EncodeToString(value)
}

func formatTestUUID(value int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", value)
}
