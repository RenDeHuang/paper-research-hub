package analysis

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	confidenceLevel95 = 0.95
	normalQuantile95  = 1.959963984540054
)

type AnalysisStatus string

const (
	AnalysisStatusSufficientEvidence   AnalysisStatus = "sufficient_evidence"
	AnalysisStatusInsufficientEvidence AnalysisStatus = "insufficient_evidence"
)

type ConfidenceInterval struct {
	Level float64
	Lower float64
	Upper float64
}

type StatisticalEstimate struct {
	Measure            string
	Value              float64
	ConfidenceInterval ConfidenceInterval
	PValue             float64
	AdjustedPValue     float64
}

type Coverage struct {
	Covered    int
	Eligible   int
	Proportion float64
}

type InsufficientEvidence struct {
	Status  AnalysisStatus
	Reasons []string
}

type InvalidInputError struct {
	Field  string
	Reason string
}

func (err *InvalidInputError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("invalid analysis input %q: %s", err.Field, err.Reason)
}

type InsufficientEvidenceError struct {
	Status   AnalysisStatus
	Analysis string
	Reasons  []string
}

func (err *InsufficientEvidenceError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"insufficient evidence for %s: %s",
		err.Analysis,
		strings.Join(err.Reasons, ", "),
	)
}

func newInsufficientEvidence(
	analysis string,
	reasons []string,
) (*InsufficientEvidence, *InsufficientEvidenceError) {
	normalized := normalizedStrings(reasons)
	evidence := &InsufficientEvidence{
		Status:  AnalysisStatusInsufficientEvidence,
		Reasons: normalized,
	}
	return evidence, &InsufficientEvidenceError{
		Status:   AnalysisStatusInsufficientEvidence,
		Analysis: analysis,
		Reasons:  append([]string(nil), normalized...),
	}
}

func calculateCoverage(covered, eligible int) (Coverage, error) {
	if eligible <= 0 {
		return Coverage{}, &InvalidInputError{
			Field:  "eligible_paper_count",
			Reason: "must be positive",
		}
	}
	if covered < 0 || covered > eligible {
		return Coverage{}, &InvalidInputError{
			Field:  "covered_paper_count",
			Reason: "must be between zero and eligible paper count",
		}
	}
	return Coverage{
		Covered:    covered,
		Eligible:   eligible,
		Proportion: float64(covered) / float64(eligible),
	}, nil
}

func ratioEstimate(
	measure string,
	ratio, standardError float64,
) (StatisticalEstimate, error) {
	if !positiveFinite(ratio) {
		return StatisticalEstimate{}, &InvalidInputError{
			Field:  measure,
			Reason: "ratio must be positive and finite",
		}
	}
	if !positiveFinite(standardError) {
		return StatisticalEstimate{}, &InvalidInputError{
			Field:  measure + "_standard_error",
			Reason: "must be positive and finite",
		}
	}

	logRatio := math.Log(ratio)
	lower := math.Exp(logRatio - normalQuantile95*standardError)
	upper := math.Exp(logRatio + normalQuantile95*standardError)
	pValue := math.Erfc(math.Abs(logRatio/standardError) / math.Sqrt2)
	return StatisticalEstimate{
		Measure: measure,
		Value:   ratio,
		ConfidenceInterval: ConfidenceInterval{
			Level: confidenceLevel95,
			Lower: lower,
			Upper: upper,
		},
		PValue:         pValue,
		AdjustedPValue: pValue,
	}, nil
}

func benjaminiHochberg(pValues map[string]float64) map[string]float64 {
	type rankedPValue struct {
		id     string
		pValue float64
	}
	ranked := make([]rankedPValue, 0, len(pValues))
	for id, pValue := range pValues {
		ranked = append(ranked, rankedPValue{id: id, pValue: pValue})
	}
	sort.Slice(ranked, func(left, right int) bool {
		if ranked[left].pValue != ranked[right].pValue {
			return ranked[left].pValue < ranked[right].pValue
		}
		return ranked[left].id < ranked[right].id
	})

	adjusted := make(map[string]float64, len(ranked))
	runningMinimum := 1.0
	total := float64(len(ranked))
	for index := len(ranked) - 1; index >= 0; index-- {
		rank := float64(index + 1)
		candidate := ranked[index].pValue * total / rank
		if candidate > 1 {
			candidate = 1
		}
		if candidate < runningMinimum {
			runningMinimum = candidate
		}
		adjusted[ranked[index].id] = runningMinimum
	}
	return adjusted
}

func normalizedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		seen[normalized] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validateTrimmed(field, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return &InvalidInputError{
			Field:  field,
			Reason: "must be non-empty and trimmed",
		}
	}
	return nil
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func nonNegativeFinite(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
