package workfamily

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRelationAssertionAcceptsOnlyProvenEvidence(t *testing.T) {
	t.Parallel()

	assertedAt := time.Now().UTC().Truncate(time.Microsecond)
	subjectWorkID := uuid.New()
	objectWorkID := uuid.New()
	subjectSourceRecordID := uuid.New()
	objectSourceRecordID := uuid.New()

	tests := []struct {
		name      string
		assertion RelationAssertion
		wantErr   error
	}{
		{
			name: "explicit source relation",
			assertion: RelationAssertion{
				SubjectWorkID:         subjectWorkID,
				ObjectWorkID:          objectWorkID,
				RelationKind:          RelationIsPreprintOf,
				EvidenceKind:          EvidenceExplicitSourceRelation,
				SubjectSourceRecordID: subjectSourceRecordID,
				SubjectSourcePath:     "message.relation.is-preprint-of",
				AssertedAt:            assertedAt,
			},
		},
		{
			name: "stable shared identifier",
			assertion: RelationAssertion{
				SubjectWorkID:         subjectWorkID,
				ObjectWorkID:          objectWorkID,
				RelationKind:          RelationIsVersionOf,
				EvidenceKind:          EvidenceStableSharedIdentifier,
				SubjectSourceRecordID: subjectSourceRecordID,
				SubjectSourcePath:     "record.identifiers.pmid",
				ObjectSourceRecordID:  objectSourceRecordID,
				ObjectSourcePath:      "record.identifiers.pmid",
				IdentifierScheme:      "pmid",
				IdentifierValue:       "41002001",
				AssertedAt:            assertedAt,
			},
		},
		{
			name: "title and author similarity",
			assertion: RelationAssertion{
				SubjectWorkID:         subjectWorkID,
				ObjectWorkID:          objectWorkID,
				RelationKind:          RelationIsVersionOf,
				EvidenceKind:          EvidenceKind("title_author_similarity"),
				SubjectSourceRecordID: subjectSourceRecordID,
				SubjectSourcePath:     "derived.similarity",
				AssertedAt:            assertedAt,
			},
			wantErr: ErrUnsupportedEvidence,
		},
		{
			name: "self loop",
			assertion: RelationAssertion{
				SubjectWorkID:         subjectWorkID,
				ObjectWorkID:          subjectWorkID,
				RelationKind:          RelationIsVersionOf,
				EvidenceKind:          EvidenceExplicitSourceRelation,
				SubjectSourceRecordID: subjectSourceRecordID,
				SubjectSourcePath:     "message.relation.is-version-of",
				AssertedAt:            assertedAt,
			},
			wantErr: ErrRelationSelfLoop,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := test.assertion.Validate()
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestRelationAssertionAcceptsRegisteredStableIdentifierSchemes(
	t *testing.T,
) {
	t.Parallel()

	base := RelationAssertion{
		SubjectWorkID:         uuid.New(),
		ObjectWorkID:          uuid.New(),
		RelationKind:          RelationIsVersionOf,
		EvidenceKind:          EvidenceStableSharedIdentifier,
		SubjectSourceRecordID: uuid.New(),
		SubjectSourcePath:     "record.identifiers",
		ObjectSourceRecordID:  uuid.New(),
		ObjectSourcePath:      "record.identifiers",
		IdentifierValue:       "exact-shared-value",
		AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
	}

	for _, scheme := range []string{
		"doi",
		"arxiv",
		"openreview",
		"semantic_scholar",
		"openalex",
		"pmid",
		"pmcid",
	} {
		scheme := scheme
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()

			assertion := base
			assertion.IdentifierScheme = scheme
			if err := assertion.Validate(); err != nil {
				t.Fatalf("Validate(%q) error = %v", scheme, err)
			}
		})
	}
}

func TestRelationAssertionRejectsUnregisteredStableIdentifierSchemes(
	t *testing.T,
) {
	t.Parallel()

	base := RelationAssertion{
		SubjectWorkID:         uuid.New(),
		ObjectWorkID:          uuid.New(),
		RelationKind:          RelationIsVersionOf,
		EvidenceKind:          EvidenceStableSharedIdentifier,
		SubjectSourceRecordID: uuid.New(),
		SubjectSourcePath:     "record.identifiers",
		ObjectSourceRecordID:  uuid.New(),
		ObjectSourcePath:      "record.identifiers",
		IdentifierValue:       "derived-similarity-value",
		AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
	}

	for _, scheme := range []string{
		"title_author",
		"title",
		"author_similarity",
	} {
		scheme := scheme
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()

			assertion := base
			assertion.IdentifierScheme = scheme
			err := assertion.Validate()
			if err == nil ||
				!strings.Contains(err.Error(), "registered stable identifier") {
				t.Fatalf(
					"Validate(%q) error = %v, want registered stable identifier error",
					scheme,
					err,
				)
			}
		})
	}
}

func TestRelationAssertionRequiresCompleteEvidenceShape(t *testing.T) {
	t.Parallel()

	base := RelationAssertion{
		SubjectWorkID:         uuid.New(),
		ObjectWorkID:          uuid.New(),
		RelationKind:          RelationIsVersionOf,
		EvidenceKind:          EvidenceStableSharedIdentifier,
		SubjectSourceRecordID: uuid.New(),
		SubjectSourcePath:     "record.identifiers.pmid",
		ObjectSourceRecordID:  uuid.New(),
		ObjectSourcePath:      "record.identifiers.pmid",
		IdentifierScheme:      "pmid",
		IdentifierValue:       "41002001",
		AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
	}

	tests := []struct {
		name string
		edit func(*RelationAssertion)
		want string
	}{
		{
			name: "missing object evidence",
			edit: func(value *RelationAssertion) {
				value.ObjectSourceRecordID = uuid.Nil
			},
			want: "object source record",
		},
		{
			name: "missing identifier scheme",
			edit: func(value *RelationAssertion) {
				value.IdentifierScheme = ""
			},
			want: "identifier scheme",
		},
		{
			name: "untrimmed source path",
			edit: func(value *RelationAssertion) {
				value.SubjectSourcePath = " record.identifiers.pmid"
			},
			want: "source path",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			invalid := base
			test.edit(&invalid)
			err := invalid.Validate()
			if err == nil ||
				!strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
