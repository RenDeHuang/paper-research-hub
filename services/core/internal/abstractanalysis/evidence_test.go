package abstractanalysis

import (
	"strings"
	"testing"
)

func TestValidateEvidenceRequiresEverySnippetToExistInNormalizedAbstract(t *testing.T) {
	t.Parallel()

	result := notReportedResult()
	result.Domain = EvidenceField{
		State: StateSupported,
		Value: "medicine",
		Evidence: []string{
			"Patients with advanced cancer",
			"external validation cohort",
		},
	}
	abstract := "Patients with advanced cancer\nwere evaluated in an " +
		"external\tvalidation cohort."

	if err := ValidateEvidence(abstract, result); err != nil {
		t.Fatalf("ValidateEvidence() error = %v", err)
	}
}

func TestValidateEvidenceNormalizesUnicodeDeterministically(t *testing.T) {
	t.Parallel()

	result := notReportedResult()
	result.CoreMethods = EvidenceField{
		State:    StateSupported,
		Value:    "single-cell RNA sequencing",
		Evidence: []string{"single-cell café analysis"},
	}
	abstract := "We performed single-cell cafe\u0301 analysis."

	if err := ValidateEvidence(abstract, result); err != nil {
		t.Fatalf("ValidateEvidence() error = %v", err)
	}
}

func TestValidateEvidenceRejectsUnsupportedOrMalformedEvidenceAtomically(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Result)
		want   string
	}{
		{
			name: "invented evidence",
			mutate: func(result *Result) {
				result.InnovationPoints = EvidenceField{
					State:    StateSupported,
					Value:    "novel paradigm",
					Evidence: []string{"never stated in the abstract"},
				}
			},
			want: "not found",
		},
		{
			name: "blank evidence",
			mutate: func(result *Result) {
				result.CoreMethods = EvidenceField{
					State:    StateSupported,
					Value:    "sequencing",
					Evidence: []string{" \t "},
				}
			},
			want: "evidence",
		},
		{
			name: "untrimmed evidence",
			mutate: func(result *Result) {
				result.CoreMethods = EvidenceField{
					State:    StateSupported,
					Value:    "sequencing",
					Evidence: []string{" sequencing"},
				}
			},
			want: "trimmed",
		},
		{
			name: "duplicate evidence",
			mutate: func(result *Result) {
				result.CoreMethods = EvidenceField{
					State: StateSupported,
					Value: "sequencing",
					Evidence: []string{
						"sequencing",
						"sequencing",
					},
				}
			},
			want: "duplicate",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := notReportedResult()
			test.mutate(&result)
			err := ValidateEvidence(
				"We performed sequencing in patients.",
				result,
			)
			if err == nil || !strings.Contains(
				strings.ToLower(err.Error()),
				test.want,
			) {
				t.Fatalf(
					"ValidateEvidence() error = %v, want containing %q",
					err,
					test.want,
				)
			}
		})
	}
}

func TestValidateEvidenceRejectsEmptyAbstractAndInvalidFieldShape(t *testing.T) {
	t.Parallel()

	result := notReportedResult()
	if err := ValidateEvidence("", result); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "abstract") {
		t.Fatalf("empty abstract error = %v", err)
	}

	result.Domain = EvidenceField{
		State:    StateSupported,
		Value:    " medicine",
		Evidence: []string{"Patients"},
	}
	if err := ValidateEvidence("Patients were enrolled.", result); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "trimmed") {
		t.Fatalf("invalid field shape error = %v", err)
	}
}

func notReportedResult() Result {
	field := EvidenceField{
		State:    StateNotReported,
		Value:    "",
		Evidence: []string{},
	}
	return Result{
		Domain:               field,
		ResearchProblem:      field,
		ResearchPurpose:      field,
		ResearchObjects:      field,
		ContentType:          field,
		ResearchMode:         field,
		StudyDesign:          field,
		DataOrSamples:        field,
		CoreMethods:          field,
		TechnicalRoute:       field,
		ValidationStrategy:   field,
		MainFindings:         field,
		InnovationPoints:     field,
		ApplicationDirection: field,
		LimitationsReported:  field,
	}
}
