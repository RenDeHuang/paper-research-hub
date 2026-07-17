package catalog

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestRankPositiveCitationMomentumUsesMidrankAndStableTieBreak(t *testing.T) {
	t.Parallel()

	fastA := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	fastB := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	slow := uuid.MustParse("00000000-0000-4000-8000-000000000003")
	zero := uuid.MustParse("00000000-0000-4000-8000-000000000004")
	declining := uuid.MustParse("00000000-0000-4000-8000-000000000005")

	results, err := rankPositiveCitationMomentum([]citationMomentumObservation{
		{
			WorkID:        fastA,
			CanonicalKey:  "doi:10.1000/b",
			CitationCount: 30,
			Velocity:      decimal.RequireFromString("2"),
		},
		{
			WorkID:        fastB,
			CanonicalKey:  "doi:10.1000/a",
			CitationCount: 40,
			Velocity:      decimal.RequireFromString("2"),
		},
		{
			WorkID:        slow,
			CanonicalKey:  "doi:10.1000/c",
			CitationCount: 50,
			Velocity:      decimal.RequireFromString("1"),
		},
		{
			WorkID:        zero,
			CanonicalKey:  "doi:10.1000/d",
			CitationCount: 100,
			Velocity:      decimal.Zero,
		},
		{
			WorkID:        declining,
			CanonicalKey:  "doi:10.1000/e",
			CitationCount: 200,
			Velocity:      decimal.RequireFromString("-1"),
		},
	})
	if err != nil {
		t.Fatalf("rankPositiveCitationMomentum() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("result count = %d, want 3 positive-velocity Works", len(results))
	}
	if results[0].WorkID != fastB ||
		results[1].WorkID != fastA ||
		results[2].WorkID != slow {
		t.Fatalf("stable ranking = %#v", results)
	}
	if results[0].Rank != 1 ||
		results[1].Rank != 2 ||
		results[2].Rank != 3 {
		t.Fatalf("ranks = %#v, want 1,2,3", results)
	}
	if !results[0].Score.Equal(decimal.RequireFromString("0.833333333333333333")) ||
		!results[1].Score.Equal(decimal.RequireFromString("0.833333333333333333")) ||
		!results[2].Score.Equal(decimal.RequireFromString("0.333333333333333333")) {
		t.Fatalf("midrank scores = %#v", results)
	}
}

func TestRankPositiveCitationMomentumRejectsDuplicateWork(t *testing.T) {
	t.Parallel()

	workID := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	_, err := rankPositiveCitationMomentum([]citationMomentumObservation{
		{
			WorkID:        workID,
			CanonicalKey:  "doi:10.1000/a",
			CitationCount: 1,
			Velocity:      decimal.RequireFromString("1"),
		},
		{
			WorkID:        workID,
			CanonicalKey:  "doi:10.1000/b",
			CitationCount: 2,
			Velocity:      decimal.RequireFromString("2"),
		},
	})
	if !errors.Is(err, errDuplicateCitationMomentumWork) {
		t.Fatalf(
			"rankPositiveCitationMomentum(duplicate) error = %v, want %v",
			err,
			errDuplicateCitationMomentumWork,
		)
	}
}

func TestRankPositiveCitationMomentumRejectsInvalidIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		observation citationMomentumObservation
	}{
		{
			name: "missing Work ID",
			observation: citationMomentumObservation{
				CanonicalKey:  "doi:10.1000/a",
				CitationCount: 1,
				Velocity:      decimal.RequireFromString("1"),
			},
		},
		{
			name: "untrimmed canonical key",
			observation: citationMomentumObservation{
				WorkID:        uuid.New(),
				CanonicalKey:  " doi:10.1000/a",
				CitationCount: 1,
				Velocity:      decimal.RequireFromString("1"),
			},
		},
		{
			name: "negative citation count",
			observation: citationMomentumObservation{
				WorkID:        uuid.New(),
				CanonicalKey:  "doi:10.1000/a",
				CitationCount: -1,
				Velocity:      decimal.RequireFromString("1"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := rankPositiveCitationMomentum(
				[]citationMomentumObservation{test.observation},
			)
			if !errors.Is(err, errInvalidCitationMomentumObservation) {
				t.Fatalf(
					"rankPositiveCitationMomentum() error = %v, want %v",
					err,
					errInvalidCitationMomentumObservation,
				)
			}
		})
	}
}
