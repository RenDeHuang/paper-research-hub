package analysis

import (
	"slices"
	"testing"
)

func TestAvailableJournalFeatureTypesRepresentClassificationCoverage(
	t *testing.T,
) {
	t.Parallel()

	actual := availableJournalFeatureTypes(
		[]cohortMeshDescriptor{
			{DescriptorLbl: "Neoplasms"},
			{DescriptorLbl: "Immunotherapy"},
		},
		[]cohortPublicationType{
			{PublicationTypeLbl: "Journal Article"},
		},
		nil,
	)
	expected := []string{"mesh", "publication_type"}
	if !slices.Equal(actual, expected) {
		t.Fatalf(
			"availableJournalFeatureTypes() = %v, want %v",
			actual,
			expected,
		)
	}
}

func TestJournalPatternInputCoverageIsAvailableWithoutSufficientEffect(
	t *testing.T,
) {
	t.Parallel()

	coverage, err := journalPatternInputCoverage(JournalPatternInput{
		CoveredPaperCount:  3,
		EligiblePaperCount: 4,
	})
	if err != nil {
		t.Fatalf("journalPatternInputCoverage() error = %v", err)
	}
	if coverage.Proportion != 0.75 {
		t.Fatalf(
			"journalPatternInputCoverage() proportion = %v, want 0.75",
			coverage.Proportion,
		)
	}
}

func TestJournalPatternPopulationCountsExcludeUnclassifiedPapersFromEffect(
	t *testing.T,
) {
	t.Parallel()

	population, err := journalPatternPopulationCounts(
		10,
		10,
		9,
		100,
		10,
	)
	if err != nil {
		t.Fatalf("journalPatternPopulationCounts() error = %v", err)
	}
	if population.JournalPaperCount != 10 ||
		population.FieldFeaturePaperCount != 1 ||
		population.FieldBaselinePaperCount != 90 {
		t.Fatalf(
			"journalPatternPopulationCounts() = %#v, want journal 10 and disjoint field 1/90",
			population,
		)
	}
	if population.Coverage.Proportion != 1 {
		t.Fatalf(
			"journalPatternPopulationCounts() coverage = %v, want 1",
			population.Coverage.Proportion,
		)
	}
}
