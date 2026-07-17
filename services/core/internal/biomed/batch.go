package biomed

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PublicEligibilityBatchInput struct {
	PolicyVersion     string
	MetricYear        int
	SubjectVersionKey string
	AssessedAt        time.Time
}

func (input PublicEligibilityBatchInput) validate() error {
	if err := validatePublicEligibilityIdentity(
		"batch",
		input.PolicyVersion,
		input.MetricYear,
		input.SubjectVersionKey,
	); err != nil {
		return err
	}
	if input.AssessedAt.IsZero() {
		return errors.New("biomedical public eligibility assessed_at is required")
	}
	return nil
}

type PublicEligibilityBatchSummary struct {
	Total    int
	Accepted int
	Rejected int
	Missing  int
}

type PublicEligibilityWorkEnumerator interface {
	WorkIDs(context.Context) iter.Seq2[string, error]
}

type PostgresPublicEligibilityWorkEnumerator struct {
	pool *pgxpool.Pool
}

var _ PublicEligibilityWorkEnumerator = (*PostgresPublicEligibilityWorkEnumerator)(nil)

func NewPostgresPublicEligibilityWorkEnumerator(
	pool *pgxpool.Pool,
) (*PostgresPublicEligibilityWorkEnumerator, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &PostgresPublicEligibilityWorkEnumerator{pool: pool}, nil
}

func (source *PostgresPublicEligibilityWorkEnumerator) WorkIDs(
	ctx context.Context,
) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		if ctx == nil {
			yield("", errors.New("public eligibility context is required"))
			return
		}
		if err := ctx.Err(); err != nil {
			yield("", err)
			return
		}
		if source == nil || source.pool == nil {
			yield(
				"",
				errors.New(
					"PostgresPublicEligibilityWorkEnumerator is not initialized",
				),
			)
			return
		}

		rows, err := source.pool.Query(ctx, `
			SELECT id::text
			FROM works
			ORDER BY id
		`)
		if err != nil {
			yield("", fmt.Errorf("enumerate Work IDs: %w", err))
			return
		}

		var workIDs []string
		for rows.Next() {
			var workID string
			if err := rows.Scan(&workID); err != nil {
				rows.Close()
				yield("", fmt.Errorf("scan Work ID: %w", err))
				return
			}
			if strings.TrimSpace(workID) == "" ||
				workID != strings.TrimSpace(workID) {
				rows.Close()
				yield("", errors.New("enumerated Work ID is not exact"))
				return
			}
			workIDs = append(workIDs, workID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			yield("", fmt.Errorf("iterate Work IDs: %w", err))
			return
		}
		rows.Close()

		for _, workID := range workIDs {
			if !yield(workID, nil) {
				return
			}
		}
	}
}

type PublicEligibilityBatchService struct {
	works       PublicEligibilityWorkEnumerator
	eligibility *PublicEligibilityService
}

func NewPublicEligibilityBatchService(
	works PublicEligibilityWorkEnumerator,
	eligibility *PublicEligibilityService,
) (*PublicEligibilityBatchService, error) {
	if interfaceIsNil(works) {
		return nil, errors.New("public eligibility Work enumerator is required")
	}
	if eligibility == nil {
		return nil, errors.New("public eligibility service is required")
	}
	return &PublicEligibilityBatchService{
		works:       works,
		eligibility: eligibility,
	}, nil
}

func (service *PublicEligibilityBatchService) AssessAll(
	ctx context.Context,
	input PublicEligibilityBatchInput,
) (PublicEligibilityBatchSummary, error) {
	if ctx == nil {
		return PublicEligibilityBatchSummary{}, errors.New(
			"public eligibility context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return PublicEligibilityBatchSummary{}, err
	}
	if service == nil ||
		interfaceIsNil(service.works) ||
		service.eligibility == nil {
		return PublicEligibilityBatchSummary{}, errors.New(
			"public eligibility batch service is not initialized",
		)
	}
	if err := input.validate(); err != nil {
		return PublicEligibilityBatchSummary{}, err
	}

	workIDs := service.works.WorkIDs(ctx)
	if workIDs == nil {
		return PublicEligibilityBatchSummary{}, errors.New(
			"public eligibility Work enumerator returned no sequence",
		)
	}
	var summary PublicEligibilityBatchSummary
	for workID, enumerateErr := range workIDs {
		if enumerateErr != nil {
			return PublicEligibilityBatchSummary{}, fmt.Errorf(
				"enumerate biomedical public eligibility Works: %w",
				enumerateErr,
			)
		}
		assessment, err := service.eligibility.Assess(
			ctx,
			PublicEligibilityInput{
				WorkID:            workID,
				PolicyVersion:     input.PolicyVersion,
				MetricYear:        input.MetricYear,
				SubjectVersionKey: input.SubjectVersionKey,
				AssessedAt:        input.AssessedAt,
			},
		)
		if err != nil {
			return PublicEligibilityBatchSummary{}, fmt.Errorf(
				"assess biomedical public eligibility for Work %q: %w",
				workID,
				err,
			)
		}
		summary.Total++
		switch assessment.Decision {
		case PublicEligibilityDecisionAccepted:
			summary.Accepted++
		case PublicEligibilityDecisionRejected:
			summary.Rejected++
		case PublicEligibilityDecisionMissing:
			summary.Missing++
		default:
			return PublicEligibilityBatchSummary{}, fmt.Errorf(
				"Work %q returned invalid public eligibility decision %q",
				workID,
				assessment.Decision,
			)
		}
	}
	return summary, nil
}
