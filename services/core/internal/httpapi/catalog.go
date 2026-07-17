package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/catalog"
)

const catalogGenerationHeader = "X-Catalog-Generation"

type catalogHandlers struct {
	repository *catalog.Repository
}

type paginationResponse struct {
	Limit      int     `json:"limit"`
	Total      int64   `json:"total"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

type facetBucketResponse struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int64  `json:"count"`
}

type facetsResponse struct {
	PaperTypes []facetBucketResponse `json:"paper_types"`
	Topics     []facetBucketResponse `json:"topics"`
	Methods    []facetBucketResponse `json:"methods"`
	Statuses   []facetBucketResponse `json:"statuses"`
	Sources    []facetBucketResponse `json:"sources"`
}

func registerCatalogRoutes(mux *http.ServeMux, repository *catalog.Repository) {
	handlers := catalogHandlers{repository: repository}
	mux.HandleFunc("/api/v1/home", getOnly(handlers.home))
	mux.HandleFunc("/api/v1/subjects", getOnly(handlers.subjects))
	mux.HandleFunc("/api/v1/subjects/{slug}", getOnly(handlers.subject))
	mux.HandleFunc("/api/v1/journals", getOnly(handlers.journals))
	mux.HandleFunc("/api/v1/journals/{slug}", getOnly(handlers.journal))
	mux.HandleFunc("/api/v1/stats", getOnly(handlers.stats))
	mux.HandleFunc("/api/v1/papers", getOnly(handlers.papers))
	mux.HandleFunc("/api/v1/papers/{id}", getOnly(handlers.paper))
	mux.HandleFunc("/api/v1/topics", getOnly(handlers.topics))
	mux.HandleFunc("/api/v1/topics/{slug}", getOnly(handlers.topic))
	mux.HandleFunc("/api/v1/methods", getOnly(handlers.methods))
	mux.HandleFunc("/api/v1/methods/{slug}", getOnly(handlers.method))
	mux.HandleFunc(
		"/api/v1/trends/papers",
		getOnly(handlers.trends(catalog.TrendKindPapers)),
	)
	mux.HandleFunc(
		"/api/v1/trends/topics",
		getOnly(handlers.trends(catalog.TrendKindTopics)),
	)
	mux.HandleFunc(
		"/api/v1/trends/methods",
		getOnly(handlers.trends(catalog.TrendKindMethods)),
	)
	mux.HandleFunc(
		"/api/v1/research-opportunities",
		getOnly(handlers.researchOpportunities),
	)
}

func (handlers catalogHandlers) home(writer http.ResponseWriter, request *http.Request) {
	if !requireNoQuery(writer, request) {
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	document, err := handlers.repository.Home(request.Context())
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	writeDocument(writer, document)
}

func (handlers catalogHandlers) subjects(writer http.ResponseWriter, request *http.Request) {
	handlers.biomedicalList(writer, request, true)
}

func (handlers catalogHandlers) subject(writer http.ResponseWriter, request *http.Request) {
	handlers.biomedicalDetail(writer, request, true)
}

func (handlers catalogHandlers) journals(writer http.ResponseWriter, request *http.Request) {
	handlers.biomedicalList(writer, request, false)
}

func (handlers catalogHandlers) journal(writer http.ResponseWriter, request *http.Request) {
	handlers.biomedicalDetail(writer, request, false)
}

func (handlers catalogHandlers) biomedicalList(
	writer http.ResponseWriter,
	request *http.Request,
	subject bool,
) {
	query, err := parsePageQuery(request)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	var page catalog.BiomedicalPage
	if subject {
		page, err = handlers.repository.Subjects(request.Context(), query)
	} else {
		page, err = handlers.repository.Journals(request.Context(), query)
	}
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	if err := writeBiomedicalPage(writer, page); err != nil {
		writeCatalogError(writer, request, err)
	}
}

func (handlers catalogHandlers) biomedicalDetail(
	writer http.ResponseWriter,
	request *http.Request,
	subject bool,
) {
	query, err := parsePageQuery(request)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	var document catalog.Document
	if subject {
		document, err = handlers.repository.Subject(
			request.Context(),
			request.PathValue("slug"),
			query,
		)
	} else {
		document, err = handlers.repository.Journal(
			request.Context(),
			request.PathValue("slug"),
			query,
		)
	}
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	writeDocument(writer, document)
}

func (handlers catalogHandlers) stats(writer http.ResponseWriter, request *http.Request) {
	if !requireNoQuery(writer, request) {
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	document, err := handlers.repository.Stats(request.Context())
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	writeDocument(writer, document)
}

func (handlers catalogHandlers) papers(writer http.ResponseWriter, request *http.Request) {
	query, err := parsePaperListQuery(request)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	page, err := handlers.repository.Papers(request.Context(), query)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	setGenerationHeader(writer, page.Generation)
	writeJSON(writer, struct {
		Items      []json.RawMessage  `json:"items"`
		Pagination paginationResponse `json:"pagination"`
		Facets     facetsResponse     `json:"facets"`
	}{
		Items:      page.Items,
		Pagination: responsePagination(page.Pagination),
		Facets:     responseFacets(page.Facets),
	})
}

func (handlers catalogHandlers) paper(writer http.ResponseWriter, request *http.Request) {
	if !requireNoQuery(writer, request) {
		return
	}
	paperID, err := uuid.Parse(request.PathValue("id"))
	if err != nil || paperID == uuid.Nil {
		writeCatalogError(writer, request, catalog.ErrNotFound)
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	document, err := handlers.repository.Paper(request.Context(), paperID)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	writeDocument(writer, document)
}

func (handlers catalogHandlers) topics(writer http.ResponseWriter, request *http.Request) {
	query, err := parsePageQuery(request)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	page, err := handlers.repository.Topics(request.Context(), query)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	writeTaxonomyPage(writer, page)
}

func (handlers catalogHandlers) topic(writer http.ResponseWriter, request *http.Request) {
	handlers.taxonomyDetail(writer, request, true)
}

func (handlers catalogHandlers) methods(writer http.ResponseWriter, request *http.Request) {
	query, err := parsePageQuery(request)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	page, err := handlers.repository.Methods(request.Context(), query)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	writeTaxonomyPage(writer, page)
}

func (handlers catalogHandlers) method(writer http.ResponseWriter, request *http.Request) {
	handlers.taxonomyDetail(writer, request, false)
}

func (handlers catalogHandlers) taxonomyDetail(
	writer http.ResponseWriter,
	request *http.Request,
	topic bool,
) {
	if !requireNoQuery(writer, request) {
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	var (
		document catalog.Document
		err      error
	)
	if topic {
		document, err = handlers.repository.Topic(request.Context(), request.PathValue("slug"))
	} else {
		document, err = handlers.repository.Method(request.Context(), request.PathValue("slug"))
	}
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	writeDocument(writer, document)
}

func (handlers catalogHandlers) trends(kind catalog.TrendKind) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		query, err := parseTrendQuery(request)
		if err != nil {
			writeCatalogError(writer, request, err)
			return
		}
		if handlers.repository == nil {
			writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
			return
		}

		page, err := handlers.repository.Trends(request.Context(), kind, query)
		if err != nil {
			writeCatalogError(writer, request, err)
			return
		}
		setGenerationHeader(writer, page.Generation)
		writeJSON(writer, struct {
			GeneratedAt string             `json:"generated_at"`
			WindowDays  int                `json:"window_days"`
			Items       []json.RawMessage  `json:"items"`
			Pagination  paginationResponse `json:"pagination"`
		}{
			GeneratedAt: page.Generation.GeneratedAt.Format(timeFormat),
			WindowDays:  page.WindowDays,
			Items:       page.Items,
			Pagination:  responsePagination(page.Pagination),
		})
	}
}

func (handlers catalogHandlers) researchOpportunities(
	writer http.ResponseWriter,
	request *http.Request,
) {
	query, err := parseOpportunityQuery(request)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	if handlers.repository == nil {
		writeCatalogError(writer, request, catalog.ErrCatalogNotPublished)
		return
	}

	page, err := handlers.repository.ResearchOpportunities(request.Context(), query)
	if err != nil {
		writeCatalogError(writer, request, err)
		return
	}
	setGenerationHeader(writer, page.Generation)
	writeJSON(writer, struct {
		Analysis   json.RawMessage    `json:"analysis"`
		Items      []json.RawMessage  `json:"items"`
		Pagination paginationResponse `json:"pagination"`
	}{
		Analysis:   page.Analysis,
		Items:      page.Items,
		Pagination: responsePagination(page.Pagination),
	})
}

func writeTaxonomyPage(writer http.ResponseWriter, page catalog.TaxonomyPage) {
	setGenerationHeader(writer, page.Generation)
	writeJSON(writer, struct {
		Items      []json.RawMessage  `json:"items"`
		Pagination paginationResponse `json:"pagination"`
	}{
		Items:      page.Items,
		Pagination: responsePagination(page.Pagination),
	})
}

func writeBiomedicalPage(writer http.ResponseWriter, page catalog.BiomedicalPage) error {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(page.Metadata, &response); err != nil {
		return err
	}
	generation, err := json.Marshal(page.Generation.ID.String())
	if err != nil {
		return err
	}
	items, err := json.Marshal(page.Items)
	if err != nil {
		return err
	}
	pagination, err := json.Marshal(responsePagination(page.Pagination))
	if err != nil {
		return err
	}
	response["catalog_generation"] = generation
	response["items"] = items
	response["pagination"] = pagination
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}

	setGenerationHeader(writer, page.Generation)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
	return nil
}

func writeDocument(writer http.ResponseWriter, document catalog.Document) {
	setGenerationHeader(writer, document.Generation)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(document.Payload)
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func setGenerationHeader(writer http.ResponseWriter, generation catalog.Generation) {
	writer.Header().Set(catalogGenerationHeader, generation.ID.String())
}

func responsePagination(value catalog.Pagination) paginationResponse {
	var cursor *string
	if value.NextCursor != "" {
		next := value.NextCursor
		cursor = &next
	}
	return paginationResponse{
		Limit:      value.Limit,
		Total:      value.Total,
		NextCursor: cursor,
		HasMore:    value.HasMore,
	}
}

func responseFacets(value catalog.Facets) facetsResponse {
	return facetsResponse{
		PaperTypes: responseFacetBuckets(value.PaperTypes),
		Topics:     responseFacetBuckets(value.Topics),
		Methods:    responseFacetBuckets(value.Methods),
		Statuses:   responseFacetBuckets(value.Statuses),
		Sources:    responseFacetBuckets(value.Sources),
	}
}

func responseFacetBuckets(values []catalog.FacetBucket) []facetBucketResponse {
	result := make([]facetBucketResponse, len(values))
	for index, value := range values {
		result[index] = facetBucketResponse{
			Key:   value.Key,
			Label: value.Label,
			Count: value.Count,
		}
	}
	return result
}

func writeCatalogError(
	writer http.ResponseWriter,
	request *http.Request,
	err error,
) {
	switch {
	case errors.Is(err, catalog.ErrCatalogNotPublished):
		writeProblem(
			writer,
			request,
			http.StatusServiceUnavailable,
			"urn:paper-hub:problem:catalog-not-published",
			"Service Unavailable",
			"catalog_not_published",
			"The public catalog has not been published.",
		)
	case errors.Is(err, catalog.ErrNotFound):
		writeProblem(
			writer,
			request,
			http.StatusNotFound,
			"urn:paper-hub:problem:resource-not-found",
			"Not Found",
			"resource_not_found",
			"The requested public catalog resource was not found.",
		)
	case errors.Is(err, catalog.ErrInvalidCursor):
		writeProblem(
			writer,
			request,
			http.StatusUnprocessableEntity,
			"urn:paper-hub:problem:invalid-cursor",
			"Unprocessable Content",
			"invalid_cursor",
			"The pagination cursor is malformed or has an invalid signature.",
		)
	case errors.Is(err, catalog.ErrCursorConflict):
		writeProblem(
			writer,
			request,
			http.StatusConflict,
			"urn:paper-hub:problem:cursor-context-mismatch",
			"Conflict",
			"cursor_context_mismatch",
			"The pagination cursor does not belong to the current generation and filters.",
		)
	case errors.Is(err, catalog.ErrInvalidQuery):
		writeProblem(
			writer,
			request,
			http.StatusUnprocessableEntity,
			"urn:paper-hub:problem:validation-error",
			"Unprocessable Content",
			"validation_error",
			"The request parameters are invalid.",
		)
	default:
		writeProblem(
			writer,
			request,
			http.StatusInternalServerError,
			"urn:paper-hub:problem:internal-error",
			"Internal Server Error",
			"internal_error",
			"The server could not complete the request.",
		)
	}
}
