package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const biomedicalSnapshotSort = "paper_count_desc"

type biomedicalResource struct {
	listName       string
	detailName     string
	table          string
	manifestColumn string
	countColumn    string
}

var subjectResource = biomedicalResource{
	listName:       "subjects",
	detailName:     "subject_recent_papers",
	table:          "public_catalog_subjects",
	manifestColumn: "subject_list_payload",
	countColumn:    "subject_count",
}

func (repository *Repository) Subjects(
	ctx context.Context,
	query PageQuery,
) (BiomedicalPage, error) {
	return repository.biomedicalList(ctx, subjectResource, query)
}

func (repository *Repository) Subject(
	ctx context.Context,
	slug string,
	query PageQuery,
) (Document, error) {
	return repository.biomedicalDetail(ctx, subjectResource, slug, query)
}

func (repository *Repository) biomedicalList(
	ctx context.Context,
	resource biomedicalResource,
	query PageQuery,
) (BiomedicalPage, error) {
	limit, err := normalizeLimit(query.Limit)
	if err != nil {
		return BiomedicalPage{}, err
	}
	contextHash, err := filterHash(struct{}{})
	if err != nil {
		return BiomedicalPage{}, err
	}

	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (BiomedicalPage, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return BiomedicalPage{}, err
		}

		manifestSQL := fmt.Sprintf(`
			SELECT %s, %s
			FROM public_catalog_biomedical_manifest
			WHERE generation_id = $1
		`, resource.manifestColumn, resource.countColumn)
		var metadata []byte
		var total int64
		if err := tx.QueryRow(ctx, manifestSQL, generation.ID).Scan(
			&metadata,
			&total,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return BiomedicalPage{}, ErrCatalogNotPublished
			}
			return BiomedicalPage{}, fmt.Errorf(
				"query public Catalog %s manifest: %w",
				resource.listName,
				err,
			)
		}

		var cursor cursorPayload
		if query.Cursor != "" {
			cursor, err = repository.boundCursor(
				query.Cursor,
				generation,
				resource.listName,
				contextHash,
				biomedicalSnapshotSort,
			)
			if err != nil {
				return BiomedicalPage{}, err
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
		listSQL := fmt.Sprintf(`
			SELECT summary_payload, paper_count, slug
			FROM %s
			WHERE generation_id = $1
			  %s
			ORDER BY paper_count DESC, slug
			LIMIT $%d
		`, resource.table, cursorClause, len(arguments))

		rows, err := tx.Query(ctx, listSQL, arguments...)
		if err != nil {
			return BiomedicalPage{}, fmt.Errorf(
				"query public Catalog %s: %w",
				resource.listName,
				err,
			)
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
				return BiomedicalPage{}, fmt.Errorf(
					"scan public Catalog %s: %w",
					resource.listName,
					err,
				)
			}
			value.payload = json.RawMessage(payload)
			values = append(values, value)
		}
		if err := rows.Err(); err != nil {
			return BiomedicalPage{}, fmt.Errorf(
				"iterate public Catalog %s: %w",
				resource.listName,
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
			last := values[len(values)-1]
			nextCursor, err = repository.cursors.Encode(cursorPayload{
				Generation: generation.ID.String(),
				Resource:   resource.listName,
				FilterHash: contextHash,
				Sort:       biomedicalSnapshotSort,
				Value:      fmt.Sprintf("%d", last.paperCount),
				Key:        last.slug,
			})
			if err != nil {
				return BiomedicalPage{}, err
			}
		}

		return BiomedicalPage{
			Generation: generation,
			Metadata:   json.RawMessage(metadata),
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

func (repository *Repository) biomedicalDetail(
	ctx context.Context,
	resource biomedicalResource,
	slug string,
	query PageQuery,
) (Document, error) {
	if !validSlug(slug) {
		return Document{}, ErrNotFound
	}
	limit, err := normalizeLimit(query.Limit)
	if err != nil {
		return Document{}, err
	}
	contextHash, err := filterHash(struct {
		Slug string `json:"slug"`
	}{Slug: slug})
	if err != nil {
		return Document{}, err
	}

	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (Document, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return Document{}, err
		}

		offset := 0
		if query.Cursor != "" {
			cursor, err := repository.boundCursor(
				query.Cursor,
				generation,
				resource.detailName,
				contextHash,
				"snapshot_order",
			)
			if err != nil {
				return Document{}, err
			}
			if cursor.Ordinal < 1 {
				return Document{}, ErrInvalidCursor
			}
			offset = cursor.Ordinal
		}

		detailSQL := fmt.Sprintf(`
			SELECT detail_payload
			FROM %s
			WHERE generation_id = $1 AND slug = $2
		`, resource.table)
		var payload []byte
		if err := tx.QueryRow(ctx, detailSQL, generation.ID, slug).Scan(&payload); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Document{}, ErrNotFound
			}
			return Document{}, fmt.Errorf(
				"query public Catalog %s detail: %w",
				resource.listName,
				err,
			)
		}

		paged, err := repository.pageRecentPapers(
			json.RawMessage(payload),
			generation,
			resource,
			contextHash,
			offset,
			limit,
		)
		if err != nil {
			return Document{}, err
		}
		return Document{
			Generation: generation,
			Payload:    paged,
		}, nil
	})
}

func (repository *Repository) pageRecentPapers(
	payload json.RawMessage,
	generation Generation,
	resource biomedicalResource,
	contextHash string,
	offset int,
	limit int,
) (json.RawMessage, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, fmt.Errorf(
			"decode public Catalog %s detail payload: %w",
			resource.listName,
			err,
		)
	}
	recentPayload, ok := document["recent_papers"]
	if !ok {
		return nil, fmt.Errorf(
			"public Catalog %s detail payload lacks recent_papers",
			resource.listName,
		)
	}
	var recent map[string]json.RawMessage
	if err := json.Unmarshal(recentPayload, &recent); err != nil {
		return nil, fmt.Errorf(
			"decode public Catalog %s recent_papers: %w",
			resource.listName,
			err,
		)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(recent["items"], &items); err != nil {
		return nil, fmt.Errorf(
			"decode public Catalog %s recent_papers items: %w",
			resource.listName,
			err,
		)
	}
	if offset < 0 || offset > len(items) {
		return nil, ErrInvalidCursor
	}

	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	hasMore := end < len(items)
	nextCursor := ""
	var err error
	if hasMore {
		nextCursor, err = repository.cursors.Encode(cursorPayload{
			Generation: generation.ID.String(),
			Resource:   resource.detailName,
			FilterHash: contextHash,
			Sort:       "snapshot_order",
			Ordinal:    end,
		})
		if err != nil {
			return nil, err
		}
	}

	pageItems, err := json.Marshal(items[offset:end])
	if err != nil {
		return nil, fmt.Errorf("encode public Catalog recent_papers page: %w", err)
	}
	pagination, err := json.Marshal(struct {
		Limit      int     `json:"limit"`
		Total      int     `json:"total"`
		NextCursor *string `json:"next_cursor"`
		HasMore    bool    `json:"has_more"`
	}{
		Limit:      limit,
		Total:      len(items),
		NextCursor: optionalCursor(nextCursor),
		HasMore:    hasMore,
	})
	if err != nil {
		return nil, fmt.Errorf("encode public Catalog recent_papers pagination: %w", err)
	}
	recent["items"] = pageItems
	recent["pagination"] = pagination
	recentEncoded, err := json.Marshal(recent)
	if err != nil {
		return nil, fmt.Errorf("encode public Catalog recent_papers: %w", err)
	}
	document["recent_papers"] = recentEncoded
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf(
			"encode public Catalog %s detail payload: %w",
			resource.listName,
			err,
		)
	}
	return json.RawMessage(encoded), nil
}

func optionalCursor(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
