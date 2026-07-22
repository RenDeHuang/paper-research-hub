package contenttruth

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/workfamily"
)

func TestVersionTimelineIsAnExplicitGraphNotAnOrderedVersionList(t *testing.T) {
	t.Parallel()

	familyID := uuid.New()
	canonicalWorkID := uuid.New()
	preprintWorkID := uuid.New()
	asOf := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	timeline := VersionTimeline{
		WorkFamilyID:    familyID,
		CanonicalWorkID: canonicalWorkID,
		AsOf:            asOf,
		Members: []TimelineMember{
			{
				WorkID:         canonicalWorkID,
				CanonicalKey:   "doi:10.1000/version-of-record",
				Title:          "Version of record",
				ContentChannel: scope.ContentChannelJournalPublished,
				VersionKind:    VersionKindVersionOfRecord,
			},
			{
				WorkID:         preprintWorkID,
				CanonicalKey:   "arxiv:2607.12345",
				Title:          "Preprint",
				ContentChannel: scope.ContentChannelPreprint,
				VersionKind:    VersionKindPreprint,
			},
		},
		Relations: []TimelineRelation{{
			DecisionID:    uuid.New(),
			SubjectWorkID: preprintWorkID,
			ObjectWorkID:  canonicalWorkID,
			Kind:          workfamily.RelationIsPreprintOf,
			DecidedAt:     asOf.Add(-time.Hour),
		}},
	}
	if err := timeline.Validate(); err != nil {
		t.Fatalf("VersionTimeline.Validate() error = %v", err)
	}

	timeline.Members[0], timeline.Members[1] =
		timeline.Members[1], timeline.Members[0]
	if err := timeline.Validate(); err != nil {
		t.Fatalf(
			"VersionTimeline.Validate() after member reordering error = %v; array order must not encode chronology",
			err,
		)
	}
}

func TestVersionTimelineRejectsUnknownRelationEndpointsAndFutureDecisions(t *testing.T) {
	t.Parallel()

	familyID := uuid.New()
	workID := uuid.New()
	asOf := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	base := VersionTimeline{
		WorkFamilyID:    familyID,
		CanonicalWorkID: workID,
		AsOf:            asOf,
		Members: []TimelineMember{{
			WorkID:         workID,
			CanonicalKey:   "doi:10.1000/only-member",
			Title:          "Only member",
			ContentChannel: scope.ContentChannelJournalPublished,
			VersionKind:    VersionKindVersionOfRecord,
		}},
	}

	t.Run("unknown endpoint", func(t *testing.T) {
		timeline := base
		timeline.Relations = []TimelineRelation{{
			DecisionID:    uuid.New(),
			SubjectWorkID: workID,
			ObjectWorkID:  uuid.New(),
			Kind:          workfamily.RelationIsVersionOf,
			DecidedAt:     asOf.Add(-time.Hour),
		}}
		if err := timeline.Validate(); err == nil {
			t.Fatal("VersionTimeline.Validate() error = nil")
		}
	})

	t.Run("future relation decision", func(t *testing.T) {
		otherWorkID := uuid.New()
		timeline := base
		timeline.Members = append(timeline.Members, TimelineMember{
			WorkID:         otherWorkID,
			CanonicalKey:   "arxiv:2607.00001",
			Title:          "Other member",
			ContentChannel: scope.ContentChannelPreprint,
			VersionKind:    VersionKindPreprint,
		})
		timeline.Relations = []TimelineRelation{{
			DecisionID:    uuid.New(),
			SubjectWorkID: otherWorkID,
			ObjectWorkID:  workID,
			Kind:          workfamily.RelationIsPreprintOf,
			DecidedAt:     asOf.Add(time.Second),
		}}
		if err := timeline.Validate(); err == nil {
			t.Fatal("VersionTimeline.Validate() error = nil")
		}
	})
}
