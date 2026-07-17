package venue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const JournalJIFOrQ1PolicyVersion = "journal-jif-or-q1/v1"

type AssessmentInput struct {
	JCRImportReceiptID string
	MetricYear         int
	PolicyVersion      string
	AssessedAt         time.Time
}

type AssessmentVenue struct {
	Venue   Venue
	Metrics []MetricSnapshot
}

type AssessmentLoad struct {
	JCRImportReceiptID string
	MetricYear         int
	Venues             []AssessmentVenue
}

type Assessment struct {
	Venue              Venue
	JCRImportReceiptID string
	Result             PolicyResult
}

func (assessment Assessment) VenueID() string {
	return assessment.Venue.ID()
}

type AssessmentBatch struct {
	JCRImportReceiptID string
	MetricYear         int
	PolicyVersion      string
	AssessedAt         time.Time
	Assessments        []Assessment
}

type AssessmentSummary struct {
	Total         int
	Accepted      int
	Rejected      int
	Unknown       int
	NotApplicable int
}

type AssessmentStore interface {
	LoadAssessment(
		context.Context,
		string,
		int,
	) (AssessmentLoad, error)
	PersistAssessments(
		context.Context,
		AssessmentBatch,
	) (AssessmentSummary, error)
}

type AssessmentService struct {
	store AssessmentStore
}

func NewAssessmentService(store AssessmentStore) (*AssessmentService, error) {
	if store == nil {
		return nil, errors.New("assessment store is required")
	}
	return &AssessmentService{store: store}, nil
}

func (service *AssessmentService) Assess(
	ctx context.Context,
	input AssessmentInput,
) (AssessmentSummary, error) {
	if err := ctx.Err(); err != nil {
		return AssessmentSummary{}, err
	}
	if service == nil || service.store == nil {
		return AssessmentSummary{}, errors.New("assessment service is not initialized")
	}
	receiptID := strings.TrimSpace(input.JCRImportReceiptID)
	if receiptID == "" {
		return AssessmentSummary{}, errors.New("JCR import receipt is required")
	}
	if receiptID != input.JCRImportReceiptID {
		return AssessmentSummary{}, errors.New("JCR import receipt must be trimmed")
	}
	if input.MetricYear < 1900 || input.MetricYear > 3000 {
		return AssessmentSummary{}, fmt.Errorf(
			"metric year %d is outside 1900..3000",
			input.MetricYear,
		)
	}
	policyVersion := strings.TrimSpace(input.PolicyVersion)
	if policyVersion != JournalJIFOrQ1PolicyVersion {
		return AssessmentSummary{}, fmt.Errorf(
			"unsupported policy version %q; expected %s",
			input.PolicyVersion,
			JournalJIFOrQ1PolicyVersion,
		)
	}
	if input.AssessedAt.IsZero() {
		return AssessmentSummary{}, errors.New("assessed_at is required")
	}

	policy, err := NewJournalPolicy(policyVersion)
	if err != nil {
		return AssessmentSummary{}, err
	}
	loaded, err := service.store.LoadAssessment(
		ctx,
		receiptID,
		input.MetricYear,
	)
	if err != nil {
		return AssessmentSummary{}, fmt.Errorf("load Venue assessment input: %w", err)
	}
	if loaded.JCRImportReceiptID != receiptID {
		return AssessmentSummary{}, fmt.Errorf(
			"loaded JCR import receipt %q does not match requested %q",
			loaded.JCRImportReceiptID,
			receiptID,
		)
	}
	if loaded.MetricYear != input.MetricYear {
		return AssessmentSummary{}, fmt.Errorf(
			"loaded metric year %d does not match requested %d",
			loaded.MetricYear,
			input.MetricYear,
		)
	}

	venues := slices.Clone(loaded.Venues)
	slices.SortFunc(venues, func(left, right AssessmentVenue) int {
		return strings.Compare(left.Venue.ID(), right.Venue.ID())
	})
	assessments := make([]Assessment, 0, len(venues))
	for index, item := range venues {
		if strings.TrimSpace(item.Venue.ID()) == "" {
			return AssessmentSummary{}, fmt.Errorf(
				"assessment Venue row %d has no controlled identity",
				index,
			)
		}
		metrics := slices.Clone(item.Metrics)
		slices.SortFunc(metrics, func(left, right MetricSnapshot) int {
			return strings.Compare(left.Category(), right.Category())
		})
		result, evaluateErr := policy.Evaluate(
			item.Venue,
			input.MetricYear,
			metrics,
			input.AssessedAt,
		)
		if evaluateErr != nil {
			return AssessmentSummary{}, fmt.Errorf(
				"evaluate Venue %q: %w",
				item.Venue.ID(),
				evaluateErr,
			)
		}
		assessments = append(assessments, Assessment{
			Venue:              item.Venue,
			JCRImportReceiptID: receiptID,
			Result:             result,
		})
	}

	summary, err := service.store.PersistAssessments(ctx, AssessmentBatch{
		JCRImportReceiptID: receiptID,
		MetricYear:         input.MetricYear,
		PolicyVersion:      policyVersion,
		AssessedAt:         input.AssessedAt.UTC(),
		Assessments:        assessments,
	})
	if err != nil {
		return AssessmentSummary{}, fmt.Errorf("persist Venue assessments: %w", err)
	}
	expected := summarizeAssessments(assessments)
	if summary != expected {
		return AssessmentSummary{}, fmt.Errorf(
			"persisted Venue assessment summary %#v does not match evaluated %#v",
			summary,
			expected,
		)
	}
	return summary, nil
}

func summarizeAssessments(assessments []Assessment) AssessmentSummary {
	summary := AssessmentSummary{Total: len(assessments)}
	for _, assessment := range assessments {
		switch assessment.Result.Decision() {
		case PolicyDecisionAccepted:
			summary.Accepted++
		case PolicyDecisionRejected:
			summary.Rejected++
		case PolicyDecisionUnknown:
			summary.Unknown++
		case PolicyDecisionNotApplicable:
			summary.NotApplicable++
		}
	}
	return summary
}
