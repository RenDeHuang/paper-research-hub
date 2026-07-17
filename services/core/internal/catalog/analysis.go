package catalog

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (repository *Repository) Trends(
	ctx context.Context,
	kind TrendKind,
	query TrendQuery,
) (TrendPage, error) {
	if !kind.Valid() {
		return TrendPage{}, fmt.Errorf("%w: invalid trend kind", ErrInvalidQuery)
	}
	windowDays := query.WindowDays
	if windowDays == 0 {
		windowDays = 30
	}
	if windowDays < 1 || windowDays > 365 {
		return TrendPage{}, fmt.Errorf("%w: window_days must be between 1 and 365", ErrInvalidQuery)
	}
	limit, err := normalizeLimit(query.Limit)
	if err != nil {
		return TrendPage{}, err
	}
	contextHash, err := filterHash(struct {
		WindowDays int `json:"window_days"`
	}{WindowDays: windowDays})
	if err != nil {
		return TrendPage{}, err
	}
	resource := "trends:" + string(kind)

	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (TrendPage, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return TrendPage{}, err
		}

		var cursor cursorPayload
		if query.Cursor != "" {
			cursor, err = repository.boundCursor(
				query.Cursor,
				generation,
				resource,
				contextHash,
				"rank_asc",
			)
			if err != nil {
				return TrendPage{}, err
			}
			if cursor.Ordinal < 1 {
				return TrendPage{}, ErrInvalidCursor
			}
		}

		arguments := []any{generation.ID, string(kind), windowDays}
		cursorClause := ""
		if query.Cursor != "" {
			arguments = append(arguments, cursor.Ordinal)
			cursorClause = "AND rank > $4"
		}
		arguments = append(arguments, limit+1)

		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT payload, rank
			FROM public_catalog_trends
			WHERE generation_id = $1
			  AND trend_kind = $2
			  AND window_days = $3
			  %s
			ORDER BY rank
			LIMIT $%d
		`, cursorClause, len(arguments)), arguments...)
		if err != nil {
			return TrendPage{}, fmt.Errorf("query public catalog %s trends: %w", kind, err)
		}
		defer rows.Close()

		type trendRow struct {
			payload json.RawMessage
			rank    int
		}
		values := make([]trendRow, 0, limit+1)
		for rows.Next() {
			var payload []byte
			var row trendRow
			if err := rows.Scan(&payload, &row.rank); err != nil {
				return TrendPage{}, fmt.Errorf("scan public catalog %s trend: %w", kind, err)
			}
			row.payload = json.RawMessage(payload)
			values = append(values, row)
		}
		if err := rows.Err(); err != nil {
			return TrendPage{}, fmt.Errorf("iterate public catalog %s trends: %w", kind, err)
		}

		var total int64
		if err := tx.QueryRow(ctx, `
			SELECT count(*)
			FROM public_catalog_trends
			WHERE generation_id = $1
			  AND trend_kind = $2
			  AND window_days = $3
		`, generation.ID, string(kind), windowDays).Scan(&total); err != nil {
			return TrendPage{}, fmt.Errorf("count public catalog %s trends: %w", kind, err)
		}

		hasMore := len(values) > limit
		if hasMore {
			values = values[:limit]
		}
		items := make([]json.RawMessage, len(values))
		for index := range values {
			items[index] = values[index].payload
		}

		nextCursor := ""
		if hasMore {
			nextCursor, err = repository.cursors.Encode(cursorPayload{
				Generation: generation.ID.String(),
				Resource:   resource,
				FilterHash: contextHash,
				Sort:       "rank_asc",
				Ordinal:    values[len(values)-1].rank,
			})
			if err != nil {
				return TrendPage{}, err
			}
		}
		return TrendPage{
			Generation: generation,
			WindowDays: windowDays,
			Items:      items,
			Pagination: Pagination{
				Limit:      limit,
				Total:      total,
				NextCursor: nextCursor,
				HasMore:    hasMore,
			},
		}, nil
	})
}

func (kind TrendKind) Valid() bool {
	switch kind {
	case TrendKindPapers, TrendKindTopics, TrendKindMethods:
		return true
	default:
		return false
	}
}

func (repository *Repository) ResearchOpportunities(
	ctx context.Context,
	query OpportunityQuery,
) (OpportunityPage, error) {
	if query.Status != "" && !validOpportunityStatus(query.Status) {
		return OpportunityPage{}, fmt.Errorf("%w: invalid opportunity status", ErrInvalidQuery)
	}
	limit, err := normalizeLimit(query.Limit)
	if err != nil {
		return OpportunityPage{}, err
	}
	contextHash, err := filterHash(struct {
		Status string `json:"status,omitempty"`
	}{Status: query.Status})
	if err != nil {
		return OpportunityPage{}, err
	}

	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (OpportunityPage, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return OpportunityPage{}, err
		}
		var analysisPayload []byte
		if err := tx.QueryRow(ctx, `
			SELECT payload -> 'research_opportunities' -> 'analysis'
			FROM public_catalog_home
			WHERE generation_id = $1
		`, generation.ID).Scan(&analysisPayload); err != nil {
			return OpportunityPage{}, fmt.Errorf(
				"query public catalog research opportunity analysis metadata: %w",
				err,
			)
		}
		if len(analysisPayload) == 0 ||
			!json.Valid(analysisPayload) ||
			string(analysisPayload) == "null" {
			return OpportunityPage{}, fmt.Errorf(
				"public catalog research opportunity analysis metadata is invalid",
			)
		}

		var cursor cursorPayload
		if query.Cursor != "" {
			cursor, err = repository.boundCursor(
				query.Cursor,
				generation,
				"research-opportunities",
				contextHash,
				"ordinal_asc",
			)
			if err != nil {
				return OpportunityPage{}, err
			}
			if cursor.Ordinal < 1 {
				return OpportunityPage{}, ErrInvalidCursor
			}
		}

		arguments := []any{generation.ID}
		clauses := []string{"generation_id = $1"}
		if query.Status != "" {
			arguments = append(arguments, query.Status)
			clauses = append(clauses, fmt.Sprintf("status = $%d", len(arguments)))
		}
		baseWhere := joinAnd(clauses)
		if query.Cursor != "" {
			arguments = append(arguments, cursor.Ordinal)
			clauses = append(clauses, fmt.Sprintf("ordinal > $%d", len(arguments)))
		}
		arguments = append(arguments, limit+1)

		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT payload, ordinal
			FROM public_catalog_research_opportunities
			WHERE %s
			ORDER BY ordinal
			LIMIT $%d
		`, joinAnd(clauses), len(arguments)), arguments...)
		if err != nil {
			return OpportunityPage{}, fmt.Errorf("query public catalog research opportunities: %w", err)
		}
		defer rows.Close()

		type opportunityRow struct {
			payload json.RawMessage
			ordinal int
		}
		values := make([]opportunityRow, 0, limit+1)
		for rows.Next() {
			var payload []byte
			var row opportunityRow
			if err := rows.Scan(&payload, &row.ordinal); err != nil {
				return OpportunityPage{}, fmt.Errorf(
					"scan public catalog research opportunity: %w",
					err,
				)
			}
			row.payload = json.RawMessage(payload)
			values = append(values, row)
		}
		if err := rows.Err(); err != nil {
			return OpportunityPage{}, fmt.Errorf(
				"iterate public catalog research opportunities: %w",
				err,
			)
		}

		var total int64
		countArguments := arguments[:len(arguments)-1]
		if query.Cursor != "" {
			countArguments = countArguments[:len(countArguments)-1]
		}
		if err := tx.QueryRow(
			ctx,
			"SELECT count(*) FROM public_catalog_research_opportunities WHERE "+baseWhere,
			countArguments...,
		).Scan(&total); err != nil {
			return OpportunityPage{}, fmt.Errorf(
				"count public catalog research opportunities: %w",
				err,
			)
		}

		hasMore := len(values) > limit
		if hasMore {
			values = values[:limit]
		}
		items := make([]json.RawMessage, len(values))
		for index := range values {
			items[index] = values[index].payload
		}

		nextCursor := ""
		if hasMore {
			nextCursor, err = repository.cursors.Encode(cursorPayload{
				Generation: generation.ID.String(),
				Resource:   "research-opportunities",
				FilterHash: contextHash,
				Sort:       "ordinal_asc",
				Ordinal:    values[len(values)-1].ordinal,
			})
			if err != nil {
				return OpportunityPage{}, err
			}
		}
		return OpportunityPage{
			Generation: generation,
			Analysis:   json.RawMessage(analysisPayload),
			Items:      items,
			Pagination: Pagination{
				Limit:      limit,
				Total:      total,
				NextCursor: nextCursor,
				HasMore:    hasMore,
			},
		}, nil
	})
}

func validOpportunityStatus(value string) bool {
	switch value {
	case "worth_pursuing",
		"proceed_with_caution",
		"not_recommended_now",
		"insufficient_evidence":
		return true
	default:
		return false
	}
}

func joinAnd(clauses []string) string {
	result := ""
	for index, clause := range clauses {
		if index != 0 {
			result += " AND "
		}
		result += clause
	}
	return result
}
