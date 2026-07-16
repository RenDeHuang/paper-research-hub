package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type normalizedPaperQuery struct {
	Query         string    `json:"q,omitempty"`
	PublishedFrom time.Time `json:"published_from,omitempty"`
	PublishedTo   time.Time `json:"published_to,omitempty"`
	PaperType     string    `json:"paper_type,omitempty"`
	Topic         string    `json:"topic,omitempty"`
	Method        string    `json:"method,omitempty"`
	HasCode       *bool     `json:"has_code,omitempty"`
	HasData       *bool     `json:"has_data,omitempty"`
	HasBenchmark  *bool     `json:"has_benchmark,omitempty"`
	Status        string    `json:"status,omitempty"`
	Source        string    `json:"source,omitempty"`
	Sort          PaperSort `json:"sort"`
}

type paperRow struct {
	payload       json.RawMessage
	canonicalKey  string
	state         string
	value         string
	relevanceRank string
}

func (repository *Repository) Papers(
	ctx context.Context,
	query PaperListQuery,
) (PaperPage, error) {
	normalized, limit, err := normalizePaperQuery(query)
	if err != nil {
		return PaperPage{}, err
	}
	contextHash, err := filterHash(normalized)
	if err != nil {
		return PaperPage{}, err
	}

	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (PaperPage, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return PaperPage{}, err
		}

		var cursor cursorPayload
		if query.Cursor != "" {
			cursor, err = repository.boundCursor(
				query.Cursor,
				generation,
				"papers",
				contextHash,
				string(normalized.Sort),
			)
			if err != nil {
				return PaperPage{}, err
			}
		}

		baseWhere, baseArguments := paperWhere(generation, normalized)
		listWhere := baseWhere
		listArguments := append([]any(nil), baseArguments...)
		if query.Cursor != "" {
			cursorWhere, cursorArguments, err := paperCursorWhere(
				normalized.Sort,
				cursor,
				len(listArguments),
			)
			if err != nil {
				return PaperPage{}, err
			}
			listWhere += "\n AND " + cursorWhere
			listArguments = append(listArguments, cursorArguments...)
		}

		listArguments = append(listArguments, limit+1)
		orderBy, rankExpression := paperOrder(normalized.Sort, normalized.Query, baseArguments)
		listSQL := fmt.Sprintf(`
			SELECT
				p.summary_payload,
				p.canonical_key,
				%s AS sort_state,
				%s AS sort_value,
				%s AS relevance_rank
			FROM public_catalog_papers AS p
			WHERE %s
			ORDER BY %s
			LIMIT $%d
		`,
			paperStateExpression(normalized.Sort),
			paperValueExpression(normalized.Sort, rankExpression),
			rankExpression,
			listWhere,
			orderBy,
			len(listArguments),
		)

		rows, err := tx.Query(ctx, listSQL, listArguments...)
		if err != nil {
			return PaperPage{}, fmt.Errorf("query public catalog papers: %w", err)
		}
		defer rows.Close()

		values := make([]paperRow, 0, limit+1)
		for rows.Next() {
			var payload []byte
			var row paperRow
			if err := rows.Scan(
				&payload,
				&row.canonicalKey,
				&row.state,
				&row.value,
				&row.relevanceRank,
			); err != nil {
				return PaperPage{}, fmt.Errorf("scan public catalog paper: %w", err)
			}
			row.payload = json.RawMessage(payload)
			values = append(values, row)
		}
		if err := rows.Err(); err != nil {
			return PaperPage{}, fmt.Errorf("iterate public catalog papers: %w", err)
		}

		var total int64
		if err := tx.QueryRow(
			ctx,
			"SELECT count(*) FROM public_catalog_papers AS p WHERE "+baseWhere,
			baseArguments...,
		).Scan(&total); err != nil {
			return PaperPage{}, fmt.Errorf("count public catalog papers: %w", err)
		}

		facets, err := queryPaperFacets(ctx, tx, baseWhere, baseArguments)
		if err != nil {
			return PaperPage{}, err
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
			last := values[len(values)-1]
			value := last.value
			if normalized.Sort == PaperSortRelevance {
				value = last.relevanceRank
			}
			nextCursor, err = repository.cursors.Encode(cursorPayload{
				Generation: generation.ID.String(),
				Resource:   "papers",
				FilterHash: contextHash,
				Sort:       string(normalized.Sort),
				State:      last.state,
				Value:      value,
				Key:        last.canonicalKey,
			})
			if err != nil {
				return PaperPage{}, err
			}
		}

		return PaperPage{
			Generation: generation,
			Items:      items,
			Pagination: Pagination{
				Limit:      limit,
				Total:      total,
				NextCursor: nextCursor,
				HasMore:    hasMore,
			},
			Facets: facets,
		}, nil
	})
}

func normalizePaperQuery(query PaperListQuery) (normalizedPaperQuery, int, error) {
	limit, err := normalizeLimit(query.Limit)
	if err != nil {
		return normalizedPaperQuery{}, 0, err
	}
	if query.Query != strings.TrimSpace(query.Query) || len(query.Query) > 300 {
		return normalizedPaperQuery{}, 0, fmt.Errorf(
			"%w: q must be trimmed and contain at most 300 characters",
			ErrInvalidQuery,
		)
	}
	if query.Query != "" && len([]rune(query.Query)) > 300 {
		return normalizedPaperQuery{}, 0, fmt.Errorf("%w: q is too long", ErrInvalidQuery)
	}
	if query.PublishedFrom != nil &&
		query.PublishedTo != nil &&
		query.PublishedFrom.After(*query.PublishedTo) {
		return normalizedPaperQuery{}, 0, fmt.Errorf(
			"%w: published_from must not be after published_to",
			ErrInvalidQuery,
		)
	}
	if query.PaperType != "" && !validPaperType(query.PaperType) {
		return normalizedPaperQuery{}, 0, fmt.Errorf("%w: invalid paper type", ErrInvalidQuery)
	}
	if query.Topic != "" && !validSlug(query.Topic) {
		return normalizedPaperQuery{}, 0, fmt.Errorf("%w: invalid topic slug", ErrInvalidQuery)
	}
	if query.Method != "" && !validSlug(query.Method) {
		return normalizedPaperQuery{}, 0, fmt.Errorf("%w: invalid method slug", ErrInvalidQuery)
	}
	if query.Status != "" && !validLifecycleStatus(query.Status) {
		return normalizedPaperQuery{}, 0, fmt.Errorf("%w: invalid lifecycle status", ErrInvalidQuery)
	}
	if query.Source != "" && !validSource(query.Source) {
		return normalizedPaperQuery{}, 0, fmt.Errorf("%w: invalid source", ErrInvalidQuery)
	}

	sortName := query.Sort
	if sortName == "" {
		sortName = PaperSortPublishedAtDesc
	}
	switch sortName {
	case PaperSortPublishedAtDesc, PaperSortCitationsDesc, PaperSortTrendDesc:
	case PaperSortRelevance:
		if query.Query == "" {
			return normalizedPaperQuery{}, 0, fmt.Errorf(
				"%w: relevance sort requires q",
				ErrInvalidQuery,
			)
		}
	default:
		return normalizedPaperQuery{}, 0, fmt.Errorf("%w: invalid paper sort", ErrInvalidQuery)
	}

	normalized := normalizedPaperQuery{
		Query:        query.Query,
		PaperType:    query.PaperType,
		Topic:        query.Topic,
		Method:       query.Method,
		HasCode:      query.HasCode,
		HasData:      query.HasData,
		HasBenchmark: query.HasBenchmark,
		Status:       query.Status,
		Source:       query.Source,
		Sort:         sortName,
	}
	if query.PublishedFrom != nil {
		normalized.PublishedFrom = query.PublishedFrom.UTC()
	}
	if query.PublishedTo != nil {
		normalized.PublishedTo = query.PublishedTo.UTC()
	}
	return normalized, limit, nil
}

func paperWhere(
	generation Generation,
	query normalizedPaperQuery,
) (string, []any) {
	clauses := []string{"p.generation_id = $1"}
	arguments := []any{generation.ID}
	add := func(clause string, value any) {
		arguments = append(arguments, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(arguments)))
	}

	if query.Query != "" {
		add(
			"p.search_document @@ websearch_to_tsquery('english'::regconfig, $%d)",
			query.Query,
		)
	}
	if !query.PublishedFrom.IsZero() {
		add("p.published_at >= $%d", query.PublishedFrom)
	}
	if !query.PublishedTo.IsZero() {
		add("p.published_at <= $%d", query.PublishedTo)
	}
	if query.PaperType != "" {
		add("p.paper_type_state = 'known' AND p.paper_type = $%d", query.PaperType)
	}
	if query.Topic != "" {
		add("$%d = ANY(p.topic_slugs)", query.Topic)
	}
	if query.Method != "" {
		add("$%d = ANY(p.method_slugs)", query.Method)
	}
	if query.HasCode != nil {
		add("p.has_code_state = 'known' AND p.has_code_value = $%d", *query.HasCode)
	}
	if query.HasData != nil {
		add("p.has_data_state = 'known' AND p.has_data_value = $%d", *query.HasData)
	}
	if query.HasBenchmark != nil {
		add(
			"p.has_benchmark_state = 'known' AND p.has_benchmark_value = $%d",
			*query.HasBenchmark,
		)
	}
	if query.Status != "" {
		add("p.lifecycle_status = $%d", query.Status)
	}
	if query.Source != "" {
		add("$%d = ANY(p.source_names)", query.Source)
	}
	return strings.Join(clauses, "\n AND "), arguments
}

func paperOrder(sortName PaperSort, query string, arguments []any) (string, string) {
	switch sortName {
	case PaperSortCitationsDesc:
		return `
			CASE p.citation_count_state
				WHEN 'known' THEN 0
				WHEN 'unknown' THEN 1
				ELSE 2
			END,
			p.citation_count_value DESC NULLS LAST,
			p.canonical_key
		`, "0::numeric"
	case PaperSortTrendDesc:
		return `
			CASE p.trend_score_state
				WHEN 'known' THEN 0
				WHEN 'unknown' THEN 1
				ELSE 2
			END,
			p.trend_score_value DESC NULLS LAST,
			p.canonical_key
		`, "0::numeric"
	case PaperSortRelevance:
		queryArgument := 0
		for index, argument := range arguments {
			if text, ok := argument.(string); ok && text == query {
				queryArgument = index + 1
				break
			}
		}
		expression := fmt.Sprintf(
			"ts_rank(p.search_document, websearch_to_tsquery('english'::regconfig, $%d))",
			queryArgument,
		)
		return expression + " DESC, p.canonical_key", expression
	default:
		return `
			CASE p.published_at_state
				WHEN 'known' THEN 0
				WHEN 'unknown' THEN 1
				ELSE 2
			END,
			p.published_at DESC NULLS LAST,
			p.canonical_key
		`, "0::numeric"
	}
}

func paperStateExpression(sortName PaperSort) string {
	switch sortName {
	case PaperSortCitationsDesc:
		return "p.citation_count_state"
	case PaperSortTrendDesc:
		return "p.trend_score_state"
	case PaperSortRelevance:
		return "'known'::text"
	default:
		return "p.published_at_state"
	}
}

func paperValueExpression(sortName PaperSort, rankExpression string) string {
	switch sortName {
	case PaperSortCitationsDesc:
		return "COALESCE(p.citation_count_value::text, '')"
	case PaperSortTrendDesc:
		return "COALESCE(p.trend_score_value::text, '')"
	case PaperSortRelevance:
		return "(" + rankExpression + ")::text"
	default:
		return "COALESCE(to_char(p.published_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'), '')"
	}
}

func paperCursorWhere(
	sortName PaperSort,
	cursor cursorPayload,
	offset int,
) (string, []any, error) {
	if cursor.Key == "" {
		return "", nil, ErrInvalidCursor
	}
	if sortName == PaperSortRelevance {
		if _, err := strconv.ParseFloat(cursor.Value, 64); err != nil {
			return "", nil, ErrInvalidCursor
		}
		return fmt.Sprintf(`
			(
				ts_rank(
					p.search_document,
					websearch_to_tsquery('english'::regconfig, $2)
				) < $%d::real
				OR (
					ts_rank(
						p.search_document,
						websearch_to_tsquery('english'::regconfig, $2)
					) = $%d::real
					AND p.canonical_key > $%d
				)
			)
		`, offset+1, offset+1, offset+2), []any{cursor.Value, cursor.Key}, nil
	}

	stateRank, err := normalizedStateRank(cursor.State)
	if err != nil {
		return "", nil, err
	}
	switch sortName {
	case PaperSortCitationsDesc:
		return numericCursorWhere(
			"p.citation_count_state",
			"p.citation_count_value",
			stateRank,
			cursor,
			offset,
			"bigint",
		)
	case PaperSortTrendDesc:
		return numericCursorWhere(
			"p.trend_score_state",
			"p.trend_score_value",
			stateRank,
			cursor,
			offset,
			"numeric",
		)
	default:
		arguments := []any{stateRank, cursor.Value, cursor.Key}
		if cursor.State == "known" {
			if _, err := time.Parse(time.RFC3339Nano, cursor.Value); err != nil {
				return "", nil, ErrInvalidCursor
			}
		} else if cursor.Value != "" {
			return "", nil, ErrInvalidCursor
		}
		return fmt.Sprintf(`
			(
				CASE p.published_at_state
					WHEN 'known' THEN 0
					WHEN 'unknown' THEN 1
					ELSE 2
				END > $%d
				OR (
					CASE p.published_at_state
						WHEN 'known' THEN 0
						WHEN 'unknown' THEN 1
						ELSE 2
					END = $%d
					AND (
						($%d = 0 AND (
							p.published_at < $%d::timestamptz
							OR (
								p.published_at = $%d::timestamptz
								AND p.canonical_key > $%d
							)
						))
						OR ($%d <> 0 AND p.canonical_key > $%d)
					)
				)
			)
		`,
			offset+1,
			offset+1,
			offset+1,
			offset+2,
			offset+2,
			offset+3,
			offset+1,
			offset+3,
		), arguments, nil
	}
}

func numericCursorWhere(
	stateColumn string,
	valueColumn string,
	stateRank int,
	cursor cursorPayload,
	offset int,
	cast string,
) (string, []any, error) {
	if cursor.State == "known" {
		if _, err := strconv.ParseFloat(cursor.Value, 64); err != nil {
			return "", nil, ErrInvalidCursor
		}
	} else if cursor.Value != "" {
		return "", nil, ErrInvalidCursor
	}
	return fmt.Sprintf(`
		(
			CASE %s
				WHEN 'known' THEN 0
				WHEN 'unknown' THEN 1
				ELSE 2
			END > $%d
			OR (
				CASE %s
					WHEN 'known' THEN 0
					WHEN 'unknown' THEN 1
					ELSE 2
				END = $%d
				AND (
					($%d = 0 AND (
						%s < $%d::%s
						OR (%s = $%d::%s AND p.canonical_key > $%d)
					))
					OR ($%d <> 0 AND p.canonical_key > $%d)
				)
			)
		)
	`,
		stateColumn,
		offset+1,
		stateColumn,
		offset+1,
		offset+1,
		valueColumn,
		offset+2,
		cast,
		valueColumn,
		offset+2,
		cast,
		offset+3,
		offset+1,
		offset+3,
	), []any{stateRank, cursor.Value, cursor.Key}, nil
}

func queryPaperFacets(
	ctx context.Context,
	tx pgx.Tx,
	where string,
	arguments []any,
) (Facets, error) {
	var facets Facets
	var err error

	facets.PaperTypes, err = queryFacet(
		ctx,
		tx,
		`SELECT p.paper_type, p.paper_type, count(*)
		 FROM public_catalog_papers AS p
		 WHERE `+where+` AND p.paper_type_state = 'known'
		 GROUP BY p.paper_type
		 ORDER BY count(*) DESC, p.paper_type`,
		arguments,
	)
	if err != nil {
		return Facets{}, err
	}
	facets.Statuses, err = queryFacet(
		ctx,
		tx,
		`SELECT p.lifecycle_status, p.lifecycle_status, count(*)
		 FROM public_catalog_papers AS p
		 WHERE `+where+`
		 GROUP BY p.lifecycle_status
		 ORDER BY count(*) DESC, p.lifecycle_status`,
		arguments,
	)
	if err != nil {
		return Facets{}, err
	}
	facets.Topics, err = queryArrayFacet(
		ctx,
		tx,
		where,
		arguments,
		"topic_slugs",
	)
	if err != nil {
		return Facets{}, err
	}
	facets.Methods, err = queryArrayFacet(
		ctx,
		tx,
		where,
		arguments,
		"method_slugs",
	)
	if err != nil {
		return Facets{}, err
	}
	facets.Sources, err = queryArrayFacet(
		ctx,
		tx,
		where,
		arguments,
		"source_names",
	)
	if err != nil {
		return Facets{}, err
	}
	return facets, nil
}

func queryArrayFacet(
	ctx context.Context,
	tx pgx.Tx,
	where string,
	arguments []any,
	column string,
) ([]FacetBucket, error) {
	query := fmt.Sprintf(`
		SELECT value, value, count(*)
		FROM (
			SELECT unnest(p.%s) AS value
			FROM public_catalog_papers AS p
			WHERE %s
		) AS values
		GROUP BY value
		ORDER BY count(*) DESC, value
	`, column, where)
	return queryFacet(ctx, tx, query, arguments)
}

func queryFacet(
	ctx context.Context,
	tx pgx.Tx,
	query string,
	arguments []any,
) ([]FacetBucket, error) {
	rows, err := tx.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query public catalog facets: %w", err)
	}
	defer rows.Close()

	result := make([]FacetBucket, 0)
	for rows.Next() {
		var bucket FacetBucket
		if err := rows.Scan(&bucket.Key, &bucket.Label, &bucket.Count); err != nil {
			return nil, fmt.Errorf("scan public catalog facet: %w", err)
		}
		result = append(result, bucket)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate public catalog facets: %w", err)
	}
	return result, nil
}

func validPaperType(value string) bool {
	switch value {
	case "research_article", "review", "preprint", "dataset", "benchmark":
		return true
	default:
		return false
	}
}

func validLifecycleStatus(value string) bool {
	switch value {
	case "active", "withdrawn", "retracted", "rejected", "superseded":
		return true
	default:
		return false
	}
}

func validSource(value string) bool {
	switch value {
	case "crossref",
		"arxiv",
		"openreview",
		"s2",
		"pubmed",
		"pmc",
		"openalex",
		"springer_nature",
		"elsevier",
		"manual":
		return true
	default:
		return false
	}
}

var _ = errors.Is
