package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	defaultPageLimit = 20
	maximumPageLimit = 100
)

var taxonomySlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type transactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type Repository struct {
	database transactionBeginner
	cursors  cursorCodec
}

func NewRepository(database transactionBeginner, cursorSecret []byte) (*Repository, error) {
	if database == nil {
		return nil, errors.New("catalog repository requires a PostgreSQL database")
	}
	cursors, err := newCursorCodec(cursorSecret)
	if err != nil {
		return nil, err
	}
	return &Repository{
		database: database,
		cursors:  cursors,
	}, nil
}

func (repository *Repository) Stats(ctx context.Context) (Document, error) {
	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (Document, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return Document{}, err
		}

		var payload []byte
		if err := tx.QueryRow(ctx, `
			SELECT payload
			FROM public_catalog_stats
			WHERE generation_id = $1
		`, generation.ID).Scan(&payload); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Document{}, ErrCatalogNotPublished
			}
			return Document{}, fmt.Errorf("query public catalog stats: %w", err)
		}
		return Document{
			Generation: generation,
			Payload:    json.RawMessage(payload),
		}, nil
	})
}

func (repository *Repository) Paper(
	ctx context.Context,
	paperID uuid.UUID,
) (Document, error) {
	if paperID == uuid.Nil {
		return Document{}, ErrNotFound
	}
	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (Document, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return Document{}, err
		}
		return queryDocument(
			ctx,
			tx,
			generation,
			`SELECT detail_payload
			 FROM public_catalog_papers
			 WHERE generation_id = $1 AND paper_id = $2`,
			paperID,
		)
	})
}

func (repository *Repository) Topic(ctx context.Context, slug string) (Document, error) {
	if !validSlug(slug) {
		return Document{}, ErrNotFound
	}
	return repository.taxonomyDetail(ctx, "public_catalog_topics", slug)
}

func (repository *Repository) Method(ctx context.Context, slug string) (Document, error) {
	if !validSlug(slug) {
		return Document{}, ErrNotFound
	}
	return repository.taxonomyDetail(ctx, "public_catalog_methods", slug)
}

func (repository *Repository) taxonomyDetail(
	ctx context.Context,
	table string,
	slug string,
) (Document, error) {
	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (Document, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return Document{}, err
		}
		query := fmt.Sprintf(`
			SELECT detail_payload
			FROM %s
			WHERE generation_id = $1 AND slug = $2
		`, table)
		return queryDocument(ctx, tx, generation, query, slug)
	})
}

func (repository *Repository) Topics(
	ctx context.Context,
	query PageQuery,
) (TaxonomyPage, error) {
	return repository.taxonomyList(ctx, "topics", "public_catalog_topics", query)
}

func (repository *Repository) Methods(
	ctx context.Context,
	query PageQuery,
) (TaxonomyPage, error) {
	return repository.taxonomyList(ctx, "methods", "public_catalog_methods", query)
}

func (repository *Repository) taxonomyList(
	ctx context.Context,
	resource string,
	table string,
	query PageQuery,
) (TaxonomyPage, error) {
	limit, err := normalizeLimit(query.Limit)
	if err != nil {
		return TaxonomyPage{}, err
	}
	contextHash, err := filterHash(struct{}{})
	if err != nil {
		return TaxonomyPage{}, err
	}

	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (TaxonomyPage, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return TaxonomyPage{}, err
		}

		var cursor cursorPayload
		if query.Cursor != "" {
			cursor, err = repository.boundCursor(
				query.Cursor,
				generation,
				resource,
				contextHash,
				"paper_count_desc",
			)
			if err != nil {
				return TaxonomyPage{}, err
			}
		}

		arguments := []any{generation.ID}
		cursorClause := ""
		if query.Cursor != "" {
			arguments = append(arguments, cursor.Value, cursor.Key)
			cursorClause = `AND (
				paper_count < $2::bigint
				OR (paper_count = $2::bigint AND slug > $3)
			)`
		}
		arguments = append(arguments, limit+1)
		querySQL := fmt.Sprintf(`
			SELECT summary_payload, paper_count, slug
			FROM %s
			WHERE generation_id = $1
			  %s
			ORDER BY paper_count DESC, slug
			LIMIT $%d
		`, table, cursorClause, len(arguments))

		rows, err := tx.Query(ctx, querySQL, arguments...)
		if err != nil {
			return TaxonomyPage{}, fmt.Errorf("query public catalog %s: %w", resource, err)
		}
		defer rows.Close()

		type rowValue struct {
			payload    json.RawMessage
			paperCount int64
			slug       string
		}
		values := make([]rowValue, 0, limit+1)
		for rows.Next() {
			var payload []byte
			var value rowValue
			if err := rows.Scan(&payload, &value.paperCount, &value.slug); err != nil {
				return TaxonomyPage{}, fmt.Errorf("scan public catalog %s: %w", resource, err)
			}
			value.payload = json.RawMessage(payload)
			values = append(values, value)
		}
		if err := rows.Err(); err != nil {
			return TaxonomyPage{}, fmt.Errorf("iterate public catalog %s: %w", resource, err)
		}

		var total int64
		countSQL := fmt.Sprintf(`SELECT count(*) FROM %s WHERE generation_id = $1`, table)
		if err := tx.QueryRow(ctx, countSQL, generation.ID).Scan(&total); err != nil {
			return TaxonomyPage{}, fmt.Errorf("count public catalog %s: %w", resource, err)
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
			nextCursor, err = repository.cursors.Encode(cursorPayload{
				Generation: generation.ID.String(),
				Resource:   resource,
				FilterHash: contextHash,
				Sort:       "paper_count_desc",
				Value:      fmt.Sprintf("%d", last.paperCount),
				Key:        last.slug,
			})
			if err != nil {
				return TaxonomyPage{}, err
			}
		}

		return TaxonomyPage{
			Generation: generation,
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

func (repository *Repository) boundCursor(
	value string,
	generation Generation,
	resource string,
	contextHash string,
	sortName string,
) (cursorPayload, error) {
	cursor, err := repository.cursors.Decode(value)
	if err != nil {
		return cursorPayload{}, err
	}
	if cursor.Generation != generation.ID.String() ||
		cursor.Resource != resource ||
		cursor.FilterHash != contextHash ||
		cursor.Sort != sortName {
		return cursorPayload{}, ErrCursorConflict
	}
	return cursor, nil
}

func queryDocument(
	ctx context.Context,
	tx pgx.Tx,
	generation Generation,
	query string,
	arguments ...any,
) (Document, error) {
	allArguments := append([]any{generation.ID}, arguments...)
	var payload []byte
	if err := tx.QueryRow(ctx, query, allArguments...).Scan(&payload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Document{}, ErrNotFound
		}
		return Document{}, fmt.Errorf("query public catalog document: %w", err)
	}
	return Document{
		Generation: generation,
		Payload:    json.RawMessage(payload),
	}, nil
}

func currentGeneration(ctx context.Context, tx pgx.Tx) (Generation, error) {
	var generation Generation
	err := tx.QueryRow(ctx, `
		SELECT
			generation.id,
			generation.source_revision,
			generation.formula_version,
			generation.generated_at,
			publication.published_at
		FROM public_catalog_current AS current
		JOIN public_catalog_publications AS publication
		  ON publication.generation_id = current.generation_id
		JOIN public_catalog_generations AS generation
		  ON generation.id = current.generation_id
		WHERE current.singleton
	`).Scan(
		&generation.ID,
		&generation.SourceRevision,
		&generation.FormulaVersion,
		&generation.GeneratedAt,
		&generation.PublishedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Generation{}, ErrCatalogNotPublished
	}
	if err != nil {
		return Generation{}, fmt.Errorf("query current public catalog generation: %w", err)
	}
	return generation, nil
}

func readTransaction[T any](
	ctx context.Context,
	database transactionBeginner,
	read func(pgx.Tx) (T, error),
) (result T, returnErr error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}

	tx, err := database.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return result, fmt.Errorf("begin public catalog read transaction: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback(context.Background())
		}
	}()

	var isolation, readOnly string
	if err := tx.QueryRow(ctx, `
		SELECT
			current_setting('transaction_isolation'),
			current_setting('transaction_read_only')
	`).Scan(&isolation, &readOnly); err != nil {
		return result, fmt.Errorf("verify public catalog read transaction: %w", err)
	}
	if isolation != "repeatable read" || readOnly != "on" {
		return result, fmt.Errorf(
			"public catalog transaction invariant violated: isolation=%q read_only=%q",
			isolation,
			readOnly,
		)
	}

	result, err = read(tx)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit public catalog read transaction: %w", err)
	}
	return result, nil
}

func normalizeLimit(value int) (int, error) {
	if value == 0 {
		return defaultPageLimit, nil
	}
	if value < 1 || value > maximumPageLimit {
		return 0, fmt.Errorf(
			"%w: limit must be between 1 and %d",
			ErrInvalidQuery,
			maximumPageLimit,
		)
	}
	return value, nil
}

func validSlug(value string) bool {
	return len(value) <= 120 && taxonomySlugPattern.MatchString(value)
}

func normalizedStateRank(state string) (int, error) {
	switch state {
	case "known":
		return 0, nil
	case "unknown":
		return 1, nil
	case "missing":
		return 2, nil
	default:
		return 0, ErrInvalidCursor
	}
}

func normalizedStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := make([]string, len(values))
	for index := range values {
		result[index] = strings.TrimSpace(values[index])
	}
	return result
}
