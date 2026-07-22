package contenttruth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/workfamily"
)

type TimelineMember struct {
	WorkID         uuid.UUID
	CanonicalKey   string
	Title          string
	ContentChannel scope.ContentChannel
	VersionKind    VersionKind
	VersionNumber  *int
}

func (member TimelineMember) Validate() error {
	if member.WorkID == uuid.Nil {
		return errors.New("timeline member Work ID is required")
	}
	if err := validateExactText(member.CanonicalKey, "timeline member canonical key"); err != nil {
		return err
	}
	if err := validateExactText(member.Title, "timeline member title"); err != nil {
		return err
	}
	if _, err := scope.ParseContentChannel(string(member.ContentChannel)); err != nil {
		return err
	}
	if !member.VersionKind.valid() {
		return fmt.Errorf("invalid timeline member version kind %q", member.VersionKind)
	}
	if member.VersionNumber != nil && *member.VersionNumber < 1 {
		return errors.New("timeline member version number must be positive")
	}
	return nil
}

type TimelineRelation struct {
	DecisionID    uuid.UUID
	SubjectWorkID uuid.UUID
	ObjectWorkID  uuid.UUID
	Kind          workfamily.RelationKind
	DecidedAt     time.Time
}

func (relation TimelineRelation) Validate() error {
	if relation.DecisionID == uuid.Nil {
		return errors.New("timeline relation decision ID is required")
	}
	if relation.SubjectWorkID == uuid.Nil || relation.ObjectWorkID == uuid.Nil {
		return errors.New("timeline relation endpoints are required")
	}
	if relation.SubjectWorkID == relation.ObjectWorkID {
		return errors.New("timeline relation self-loop is forbidden")
	}
	if !supportedRelationKind(relation.Kind) {
		return fmt.Errorf("invalid timeline relation kind %q", relation.Kind)
	}
	if relation.DecidedAt.IsZero() {
		return errors.New("timeline relation decided_at is required")
	}
	return nil
}

type VersionTimeline struct {
	WorkFamilyID    uuid.UUID
	CanonicalWorkID uuid.UUID
	AsOf            time.Time
	Members         []TimelineMember
	Relations       []TimelineRelation
}

func (timeline VersionTimeline) Validate() error {
	if timeline.WorkFamilyID == uuid.Nil {
		return errors.New("version timeline Work Family ID is required")
	}
	if timeline.CanonicalWorkID == uuid.Nil {
		return errors.New("version timeline canonical Work ID is required")
	}
	if timeline.AsOf.IsZero() {
		return errors.New("version timeline as_of is required")
	}
	if len(timeline.Members) == 0 {
		return errors.New("version timeline requires members")
	}
	members := make(map[uuid.UUID]struct{}, len(timeline.Members))
	canonicalFound := false
	for index, member := range timeline.Members {
		if err := member.Validate(); err != nil {
			return fmt.Errorf("timeline member %d: %w", index, err)
		}
		if _, duplicate := members[member.WorkID]; duplicate {
			return fmt.Errorf("timeline repeats Work %s", member.WorkID)
		}
		members[member.WorkID] = struct{}{}
		if member.WorkID == timeline.CanonicalWorkID {
			canonicalFound = true
		}
	}
	if !canonicalFound {
		return errors.New("version timeline canonical Work must be a member")
	}
	decisions := make(map[uuid.UUID]struct{}, len(timeline.Relations))
	for index, relation := range timeline.Relations {
		if err := relation.Validate(); err != nil {
			return fmt.Errorf("timeline relation %d: %w", index, err)
		}
		if _, duplicate := decisions[relation.DecisionID]; duplicate {
			return fmt.Errorf(
				"timeline repeats relation decision %s",
				relation.DecisionID,
			)
		}
		decisions[relation.DecisionID] = struct{}{}
		if _, ok := members[relation.SubjectWorkID]; !ok {
			return fmt.Errorf(
				"timeline relation subject Work %s is not a member",
				relation.SubjectWorkID,
			)
		}
		if _, ok := members[relation.ObjectWorkID]; !ok {
			return fmt.Errorf(
				"timeline relation object Work %s is not a member",
				relation.ObjectWorkID,
			)
		}
		if relation.DecidedAt.After(timeline.AsOf) {
			return errors.New(
				"timeline relation decision cannot be newer than timeline as_of",
			)
		}
	}
	return nil
}

func (timeline VersionTimeline) Member(workID uuid.UUID) (TimelineMember, bool) {
	for _, member := range timeline.Members {
		if member.WorkID == workID {
			return member, true
		}
	}
	return TimelineMember{}, false
}

func normalizeTimelineText(value string) string {
	return strings.TrimSpace(value)
}
