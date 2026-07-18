package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublisherBuildsAndAtomicallyPublishesNormalizedCurrentState(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-visible",
		canonicalKey:      "doi:10.1000/publisher-visible",
		title:             "Evidence-Grounded Agents",
		topicNames:        []string{"Agent Systems"},
		methodNames:       []string{"Causal Inference"},
		withCode:          true,
		withData:          true,
		withBenchmark:     true,
		paperType:         "research_article",
		jcrDecision:       "accepted",
		jcrMatchedRules:   `["jif_at_least_10"]`,
		jcrEvidence:       `{"venue_type":"journal","jif":12.4,"quartiles":["Q1"]}`,
		publishedAt:       time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	publisher := mustPublisher(t, pool)
	generatedAt := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	input := catalogCurationInputAt(generatedAt)
	preparePublisherAcceptedCuration(t, pool, input, fixture)

	generation, err := publisher.PublishCurrent(context.Background(), input)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	if generation.ID == uuid.Nil ||
		generation.GeneratedAt != generatedAt ||
		generation.FormulaVersion != input.FormulaVersion {
		t.Fatalf("published generation = %#v, want requested immutable generation", generation)
	}
	if len(generation.SourceRevision) != 64 {
		t.Fatalf("source revision = %q, want SHA-256", generation.SourceRevision)
	}

	repository := mustRepository(t, pool)
	page, err := repository.Papers(context.Background(), PaperListQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Papers() after publish error = %v", err)
	}
	assertPaperIDs(t, page.Items, fixture.workID)

	detail, err := repository.Paper(context.Background(), fixture.workID)
	if err != nil {
		t.Fatalf("Paper() after publish error = %v", err)
	}
	assertNestedJSONValue(t, detail.Payload, []string{"type", "state"}, "known")
	assertNestedJSONValue(
		t,
		detail.Payload,
		[]string{"type", "value"},
		"research_article",
	)
	assertNestedJSONValue(t, detail.Payload, []string{"has_code", "value"}, true)
	assertNestedJSONValue(t, detail.Payload, []string{"has_data", "value"}, true)
	assertNestedJSONValue(t, detail.Payload, []string{"has_benchmark", "value"}, true)
	assertNestedJSONValue(t, detail.Payload, []string{"curation", "state"}, "known")
	assertNestedJSONValue(
		t,
		detail.Payload,
		[]string{"curation", "value", "decision"},
		"accepted",
	)
	assertJSONArrayLength(t, detail.Payload, "source_provenance", 1)

	topics, err := repository.Topics(context.Background(), PageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Topics() after publish error = %v", err)
	}
	if len(topics.Items) != 1 {
		t.Fatalf("topic count = %d, want 1", len(topics.Items))
	}
	assertJSONField(t, topics.Items[0], "slug", "agent-systems")

	methods, err := repository.Methods(context.Background(), PageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Methods() after publish error = %v", err)
	}
	if len(methods.Items) != 1 {
		t.Fatalf("method count = %d, want 1", len(methods.Items))
	}
	assertJSONField(t, methods.Items[0], "slug", "causal-inference")

	stats, err := repository.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() after publish error = %v", err)
	}
	assertNestedJSONValue(t, stats.Payload, []string{"papers_total", "value"}, float64(1))
	assertNestedJSONValue(t, stats.Payload, []string{"with_code_ratio", "value"}, float64(1))
}

func TestPublisherRejectsPublicationEventProvenanceMismatch(t *testing.T) {
	t.Run("publication state does not match exact work winner", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"winner-mismatch",
			"doi:10.1000/publication-winner-mismatch",
		)
		input := catalogCurationInput()
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherBiomedicalProjection(t, pool, fixture)
		alternateProjectionID := insertPublisherAlternateProjectionAssertion(
			t,
			pool,
			fixture,
			"projection/publication-decoy-v2",
		)
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				projectionAssertionID: alternateProjectionID,
				printState:            "missing",
				electronicState:       "missing",
				aheadState:            "missing",
				acceptedState:         "missing",
			},
		)

		_, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		)
		if !errors.Is(err, ErrCatalogNotReady) ||
			!strings.Contains(err.Error(), "publication state") {
			t.Fatalf(
				"PublishCurrent(publication winner mismatch) error = %v, want ErrCatalogNotReady",
				err,
			)
		}
		assertNoCatalogWrites(t, pool)
	})

	t.Run("known state lacks exact immutable event assertion", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"event-provenance-missing",
			"doi:10.1000/publication-event-provenance-missing",
			publisherPublicationHistoryEntry(
				"accepted", 2026, time.July, 17, "day",
				"/PubmedArticle/PubmedData/History/PubMedPubDate[1]", 1,
			),
		)
		input := catalogCurationInput()
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherBiomedicalProjection(t, pool, fixture)
		acceptedDate := publicationUpdateDate(2026, time.July, 17)
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedDate:      &acceptedDate,
				acceptedState:     "known",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
			},
		)

		_, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		)
		if !errors.Is(err, ErrCatalogNotReady) ||
			!strings.Contains(err.Error(), "publication event") {
			t.Fatalf(
				"PublishCurrent(missing exact publication event) error = %v, want ErrCatalogNotReady",
				err,
			)
		}
		assertNoCatalogWrites(t, pool)
	})

	t.Run("event assertion publication model differs from exact state", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"event-model-mismatch",
			"doi:10.1000/publication-event-model-mismatch",
			publisherPublicationHistoryEntry(
				"ppublish", 2026, time.July, 17, "day",
				"/PubmedArticle/JournalIssue/PubDate", 1,
			),
		)
		input := catalogCurationInput()
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherBiomedicalProjection(t, pool, fixture)
		printDate := publicationUpdateDate(2026, time.July, 17)
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printDate:         &printDate,
				printState:        "known",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
				events: []publisherPublicationEventFixture{{
					kind:       "print_published",
					date:       &printDate,
					precision:  "day",
					statusRaw:  "ppublish",
					modelRaw:   stringPointer("Decoy"),
					sourcePath: "/PubmedArticle/JournalIssue/PubDate",
					ordinal:    1,
				}},
			},
		)

		_, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		)
		if !errors.Is(err, ErrCatalogNotReady) ||
			!strings.Contains(err.Error(), "publication event assertion") {
			t.Fatalf(
				"PublishCurrent(publication event model mismatch) error = %v, want ErrCatalogNotReady",
				err,
			)
		}
		assertNoCatalogWrites(t, pool)
	})
}

func TestPublisherRejectsPublicationAssertionSetMismatchWithNormalizedHistory(
	t *testing.T,
) {
	normalizedDate := publisherPublicationDateFixture{
		Year:      2026,
		Month:     int(time.July),
		Day:       17,
		Precision: "day",
	}
	persistedDate := publicationUpdateDate(2026, time.July, 17)
	baseHistory := publisherPublicationHistoryFixture{
		Status:     "ppublish",
		Date:       normalizedDate,
		SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
		Ordinal:    1,
	}
	baseEvent := publisherPublicationEventFixture{
		kind:       "print_published",
		date:       &persistedDate,
		precision:  "day",
		statusRaw:  "ppublish",
		modelRaw:   stringPointer("Electronic"),
		sourcePath: baseHistory.SourcePath,
		ordinal:    1,
	}
	baseState := publisherPublicationStateFixture{
		printDate:         &persistedDate,
		printState:        "known",
		electronicState:   "missing",
		aheadState:        "missing",
		acceptedState:     "missing",
		publicationModel:  stringPointer("Electronic"),
		publicationStatus: stringPointer("epublish"),
	}

	tests := []struct {
		name              string
		history           []publisherPublicationHistoryFixture
		events            []publisherPublicationEventFixture
		state             publisherPublicationStateFixture
		publicationStatus string
	}{
		{
			name:    "event kind",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.kind = "accepted"
				return event
			}()},
			state: func() publisherPublicationStateFixture {
				state := baseState
				state.printDate = nil
				state.printState = "missing"
				state.acceptedDate = &persistedDate
				state.acceptedState = "known"
				return state
			}(),
			publicationStatus: "epublish",
		},
		{
			name:    "event date",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				changed := publicationUpdateDate(2026, time.July, 18)
				event.date = &changed
				event.sourceDate = `{"year":2026,"month":7,"day":17,"precision":"day"}`
				return event
			}()},
			state: func() publisherPublicationStateFixture {
				state := baseState
				changed := publicationUpdateDate(2026, time.July, 18)
				state.printDate = &changed
				return state
			}(),
			publicationStatus: "epublish",
		},
		{
			name:    "date precision",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.date = nil
				event.precision = "month"
				event.sourceDate = `{"year":2026,"month":7,"day":17,"precision":"day"}`
				return event
			}()},
			state: func() publisherPublicationStateFixture {
				state := baseState
				state.printDate = nil
				state.printState = "missing"
				return state
			}(),
			publicationStatus: "epublish",
		},
		{
			name:    "source date",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.sourceDate = `{"year":2026,"month":7,"day":18,"precision":"day"}`
				return event
			}()},
			state:             baseState,
			publicationStatus: "epublish",
		},
		{
			name:    "status",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.statusRaw = "accepted"
				return event
			}()},
			state:             baseState,
			publicationStatus: "epublish",
		},
		{
			name:    "source path",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.sourcePath = "/PubmedArticle/Decoy/PubMedPubDate[1]"
				return event
			}()},
			state:             baseState,
			publicationStatus: "epublish",
		},
		{
			name:    "ordinal",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.ordinal = 2
				return event
			}()},
			state:             baseState,
			publicationStatus: "epublish",
		},
		{
			name: "missing partial assertion",
			history: []publisherPublicationHistoryFixture{{
				Status: "aheadofprint",
				Date: publisherPublicationDateFixture{
					Year:      2026,
					Month:     int(time.July),
					Precision: "month",
				},
				SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				Ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
			},
			publicationStatus: "epublish",
		},
		{
			name:    "extra assertion for empty history",
			history: []publisherPublicationHistoryFixture{},
			events: []publisherPublicationEventFixture{{
				kind:       "print_published",
				precision:  "month",
				sourceDate: `{"year":2026,"month":7,"precision":"month"}`,
				statusRaw:  "ppublish",
				modelRaw:   stringPointer("Electronic"),
				sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
			},
			publicationStatus: "epublish",
		},
		{
			name: "unknown status does not generate assertion",
			history: []publisherPublicationHistoryFixture{{
				Status: "received",
				Date: publisherPublicationDateFixture{
					Year:      2026,
					Month:     int(time.July),
					Precision: "month",
				},
				SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				Ordinal:    1,
			}},
			events: []publisherPublicationEventFixture{{
				kind:       "accepted",
				precision:  "month",
				sourceDate: `{"year":2026,"month":7,"precision":"month"}`,
				statusRaw:  "received",
				modelRaw:   stringPointer("Electronic"),
				sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
			},
			publicationStatus: "epublish",
		},
		{
			name: "epublish requires top level epublish or ppublish",
			history: []publisherPublicationHistoryFixture{{
				Status: "epublish",
				Date: publisherPublicationDateFixture{
					Year:      2026,
					Month:     int(time.July),
					Precision: "month",
				},
				SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				Ordinal:    1,
			}},
			events: []publisherPublicationEventFixture{{
				kind:       "electronic_published",
				precision:  "month",
				sourceDate: `{"year":2026,"month":7,"precision":"month"}`,
				statusRaw:  "epublish",
				modelRaw:   stringPointer("Electronic"),
				sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("accepted"),
			},
			publicationStatus: "accepted",
		},
		{
			name: "epublish is recognized for top level ppublish",
			history: []publisherPublicationHistoryFixture{{
				Status: "epublish",
				Date: publisherPublicationDateFixture{
					Year:      2026,
					Month:     int(time.July),
					Precision: "month",
				},
				SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				Ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("ppublish"),
			},
			publicationStatus: "ppublish",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := openCatalogTestPool(t)
			fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
				eventKey:                "pubmed:publication-history-mismatch-" + strings.ReplaceAll(test.name, " ", "-"),
				logicalSource:           "pubmed",
				canonicalKey:            "doi:10.1000/publication-history-mismatch-" + strings.ReplaceAll(test.name, " ", "-"),
				title:                   "Publication history mismatch " + test.name,
				paperType:               "research_article",
				publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
				sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
				scopeStatus:             "included",
				includeWorkLink:         true,
				includeWorkID:           true,
				includeNormalized:       true,
				normalizedPayloadSchema: "normalized-record/v3",
				publicationModel:        "Electronic",
				publicationStatus:       test.publicationStatus,
				publicationHistory:      test.history,
				noJCRAssessment:         true,
			})
			input := catalogCurationInput()
			preparePublisherAcceptedCuration(t, pool, input, fixture)
			insertPublisherBiomedicalProjection(t, pool, fixture)
			state := test.state
			state.events = test.events
			insertPublisherPublicationState(t, pool, fixture, state)

			_, err := mustPublisher(t, pool).PublishCurrent(
				context.Background(),
				input,
			)
			if !errors.Is(err, ErrCatalogNotReady) ||
				!strings.Contains(err.Error(), "publication event assertion") {
				t.Fatalf(
					"PublishCurrent(%s) error = %v, want publication assertion set ErrCatalogNotReady",
					test.name,
					err,
				)
			}
			assertNoCatalogWrites(t, pool)
		})
	}
}

func TestPublisherPreloadsPublicationEvidenceForMultipleWorks(t *testing.T) {
	pool := openCatalogTestPool(t)
	eventDate := publicationUpdateDate(2026, time.July, 17)
	event := publisherPublicationEventFixture{
		kind:       "print_published",
		date:       &eventDate,
		precision:  "day",
		statusRaw:  "ppublish",
		modelRaw:   stringPointer("Electronic"),
		sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
		ordinal:    1,
	}
	v3 := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                "pubmed:catalog-bulk-publication-v3",
		logicalSource:           "pubmed",
		canonicalKey:            "doi:10.1000/catalog-bulk-publication-v3",
		title:                   "Bulk publication evidence v3",
		paperType:               "research_article",
		publishedAt:             eventDate,
		sourceTime:              eventDate.Add(time.Hour),
		scopeStatus:             "included",
		includeWorkLink:         true,
		includeWorkID:           true,
		includeNormalized:       true,
		normalizedPayloadSchema: "normalized-record/v3",
		publicationModel:        "Electronic",
		publicationStatus:       "epublish",
		publicationHistory: publisherPublicationHistoryFromEvents(
			t,
			[]publisherPublicationEventFixture{event},
		),
		noJCRAssessment: true,
	})
	v2 := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:catalog-bulk-publication-v2",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/catalog-bulk-publication-v2",
		title:             "Bulk publication evidence v2",
		paperType:         "research_article",
		publishedAt:       eventDate.Add(-time.Hour),
		sourceTime:        eventDate.Add(2 * time.Hour),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInput()
	preparePublisherAcceptedCuration(t, pool, input, v3, v2)
	insertPublisherBiomedicalProjection(t, pool, v3)
	insertPublisherBiomedicalProjection(t, pool, v2)
	insertPublisherPublicationState(
		t,
		pool,
		v3,
		publisherPublicationStateFixture{
			printDate:         &eventDate,
			printState:        "known",
			electronicState:   "missing",
			aheadState:        "missing",
			acceptedState:     "missing",
			publicationModel:  stringPointer("Electronic"),
			publicationStatus: stringPointer("epublish"),
			events:            []publisherPublicationEventFixture{event},
		},
	)

	counter := &publicationEvidenceQueryCounter{}
	publisher, err := NewPublisher(publicationEvidenceCountingDatabase{
		database: pool,
		counter:  counter,
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	if _, err := publisher.PublishCurrent(context.Background(), input); err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	if got := counter.count.Load(); got != 2 {
		t.Fatalf(
			"publication evidence SQL statements = %d, want exactly 2 bulk preload statements for two Works",
			got,
		)
	}
}

func TestPublisherRejectsV3WinnerWithoutPublicationState(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                "pubmed:catalog-v3-no-publication-state",
		logicalSource:           "pubmed",
		canonicalKey:            "doi:10.1000/catalog-v3-no-publication-state",
		title:                   "V3 winner without publication state",
		paperType:               "research_article",
		publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
		scopeStatus:             "included",
		includeWorkLink:         true,
		includeWorkID:           true,
		includeNormalized:       true,
		normalizedPayloadSchema: "normalized-record/v3",
		publicationModel:        "Electronic",
		publicationStatus:       "epublish",
		noJCRAssessment:         true,
	})
	input := catalogCurationInput()
	preparePublisherAcceptedCuration(t, pool, input, fixture)
	insertPublisherBiomedicalProjection(t, pool, fixture)

	_, err := mustPublisher(t, pool).PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "publication state") {
		t.Fatalf(
			"PublishCurrent(v3 winner without publication state) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsV1WinnerWithoutPublicationState(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                "pubmed:catalog-v1-no-publication-state",
		logicalSource:           "pubmed",
		canonicalKey:            "doi:10.1000/catalog-v1-no-publication-state",
		title:                   "V1 winner without publication state",
		paperType:               "research_article",
		publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
		scopeStatus:             "included",
		includeWorkLink:         true,
		includeWorkID:           true,
		includeNormalized:       true,
		normalizedPayloadSchema: "normalized-record/v1",
		noJCRAssessment:         true,
	})
	input := catalogCurationInput()
	preparePublisherAcceptedCuration(t, pool, input, fixture)
	insertPublisherBiomedicalProjection(t, pool, fixture)

	_, err := mustPublisher(t, pool).PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "publication state") {
		t.Fatalf(
			"PublishCurrent(v1 winner without publication state) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherPersistsBiomedicalCoverageMarkerBeforePublication(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-biomedical-coverage-marker",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-biomedical-coverage-marker",
		title:             "Biomedical coverage marker",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCuration(t, pool, input, fixture)

	generation, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	var markers int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM public_catalog_biomedical_coverage
		WHERE generation_id = $1
	`, generation.ID).Scan(&markers); err != nil {
		t.Fatalf("query biomedical coverage marker: %v", err)
	}
	if markers != 1 {
		t.Fatalf(
			"biomedical coverage markers = %d, want exactly one for the published generation",
			markers,
		)
	}
}

func TestPublisherUsesOnlyTheExplicitCitationSource(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-citation-source",
		canonicalKey:      "doi:10.1000/publisher-citation-source",
		title:             "Source-specific citation evidence",
		publishedAt:       time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
	)
	input.CitationAnalysisRunID = uuid.New()
	preparePublisherAcceptedCurationReferences(t, pool, input, fixture)
	insertPublisherCitationSnapshot(t, pool, fixture, 42)
	insertCatalogCitationAnalysisRun(
		t,
		pool,
		input,
		fixture.workID,
	)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO metric_snapshots (
			work_id,
			metric_name,
			metric_value,
			observed_at,
			source
		) VALUES (
			$1,
			'citation_count',
			999,
			'2026-07-17T08:00:00Z',
			'legacy-cross-source'
		)
	`, fixture.workID); err != nil {
		t.Fatalf("insert legacy ambiguous citation metric: %v", err)
	}

	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf("PublishCurrent(openalex) error = %v", err)
	}
	document, err := mustRepository(t, pool).Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper(openalex citation source) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_source", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_source", "value"},
		"openalex",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_count", "value"},
		float64(42),
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_snapshots", "value", "0", "source"},
		"openalex",
	)

	crossrefInput := input
	crossrefInput.CitationSource = "crossref"
	crossrefInput.GeneratedAt = input.GeneratedAt.Add(time.Hour)
	crossrefInput.CitationAnalysisRunID = uuid.New()
	crossrefInput.TrendAnalysisRunID = uuid.New()
	crossrefInput.JournalAnalysisRunID = uuid.New()
	crossrefInput.OpportunityAnalysisRunID = uuid.New()
	insertCatalogCitationAnalysisRun(
		t,
		pool,
		crossrefInput,
		fixture.workID,
	)
	insertCatalogBiomedicalAnalysisRuns(
		t,
		pool,
		crossrefInput,
		catalogBiomedicalCohortRevisionForFixtures(
			t,
			pool,
			crossrefInput,
			[]publisherWorkFixture{fixture},
		),
		crossrefInput.TrendAnalysisRunID,
	)
	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		crossrefInput,
	); err != nil {
		t.Fatalf("PublishCurrent(crossref) error = %v", err)
	}
	crossrefDocument, err := mustRepository(t, pool).Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper(crossref citation source) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		crossrefDocument.Payload,
		[]string{"citation_source", "value"},
		"crossref",
	)
	assertNestedJSONValue(
		t,
		crossrefDocument.Payload,
		[]string{"citation_count", "state"},
		"missing",
	)
	assertNestedJSONValue(
		t,
		crossrefDocument.Payload,
		[]string{"citation_snapshots", "state"},
		"missing",
	)
}

func TestPublisherRejectsCitationVelocityWindowThatDiffersFromAnalysisRun(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:citation-window-mismatch",
		canonicalKey:      "doi:10.1000/citation-window-mismatch",
		title:             "Citation window mismatch",
		publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.June, 15, 10, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	insertPublisherCitationSnapshot(t, pool, fixture, 10)
	insertPublisherAdditionalCitationSnapshot(
		t,
		pool,
		fixture,
		time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC),
		42,
	)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCurationReferences(t, pool, input, fixture)
	insertCatalogCitationAnalysisRunWithOptions(
		t,
		pool,
		input,
		catalogCitationAnalysisFixtureOptions{
			runVelocityWindowDays:          31,
			materializedVelocityWindowDays: 30,
			runMinimumCohortSize:           20,
		},
		fixture.workID,
	)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "citation trend metadata conflicts") {
		t.Fatalf(
			"PublishCurrent(citation velocity window mismatch) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsCitationPercentileMinimumThatDiffersFromAnalysisRun(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:citation-cohort-minimum-mismatch",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/citation-cohort-minimum-mismatch",
		title:             "Citation cohort minimum mismatch",
		paperType:         "research_article",
		publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	insertPublisherBiomedicalProjection(t, pool, fixture)
	insertPublisherAdditionalCitationSnapshot(
		t,
		pool,
		fixture,
		time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC),
		42,
	)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCurationReferences(t, pool, input, fixture)
	materializedMinimum := 21
	insertCatalogCitationAnalysisRunWithOptions(
		t,
		pool,
		input,
		catalogCitationAnalysisFixtureOptions{
			runVelocityWindowDays:          30,
			materializedVelocityWindowDays: 30,
			runMinimumCohortSize:           20,
			materializedMinimumCohortSize:  &materializedMinimum,
		},
		fixture.workID,
	)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "invalid materialized citation percentile evidence") {
		t.Fatalf(
			"PublishCurrent(citation cohort minimum mismatch) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherPublishesOnlyPositiveCitationMomentumFromBoundAnalysisRun(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	generatedAt := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	baselineAt := time.Date(2026, time.June, 15, 10, 0, 0, 0, time.UTC)
	currentAt := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)
	fixtures := []publisherWorkFixture{
		insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "openalex:citation-momentum-fast",
			canonicalKey:      "doi:10.1000/citation-momentum-fast",
			title:             "Fast citation momentum",
			publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
			sourceTime:        baselineAt,
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
		}),
		insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "openalex:citation-momentum-slow",
			canonicalKey:      "doi:10.1000/citation-momentum-slow",
			title:             "Slow citation momentum",
			publishedAt:       time.Date(2026, time.July, 16, 7, 0, 0, 0, time.UTC),
			sourceTime:        baselineAt,
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
		}),
		insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "openalex:citation-momentum-flat",
			canonicalKey:      "doi:10.1000/citation-momentum-flat",
			title:             "Flat citation momentum",
			publishedAt:       time.Date(2026, time.July, 16, 6, 0, 0, 0, time.UTC),
			sourceTime:        baselineAt,
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
		}),
	}
	for index, counts := range [][2]int64{
		{10, 42},
		{20, 30},
		{50, 50},
	} {
		insertPublisherCitationSnapshot(t, pool, fixtures[index], counts[0])
		insertPublisherAdditionalCitationSnapshot(
			t,
			pool,
			fixtures[index],
			currentAt,
			counts[1],
		)
	}

	input := catalogCurationInputAt(generatedAt)
	preparePublisherAcceptedCuration(t, pool, input, fixtures...)
	generation, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}

	repository := mustRepository(t, pool)
	trends, err := repository.Trends(
		context.Background(),
		TrendKindPapers,
		TrendQuery{WindowDays: 30, Limit: 20},
	)
	if err != nil {
		t.Fatalf("Trends(papers) error = %v", err)
	}
	if trends.Generation.ID != generation.ID {
		t.Fatalf(
			"Trends() generation = %s, want %s",
			trends.Generation.ID,
			generation.ID,
		)
	}
	if len(trends.Items) != 2 {
		t.Fatalf(
			"positive citation momentum count = %d, want 2; items=%s",
			len(trends.Items),
			trends.Items,
		)
	}
	assertNestedJSONValue(
		t,
		trends.Items[0],
		[]string{"paper", "id"},
		fixtures[0].workID.String(),
	)
	assertNestedJSONValue(t, trends.Items[0], []string{"rank"}, float64(1))
	assertNestedJSONValue(t, trends.Items[0], []string{"score"}, float64(1))
	assertNestedJSONValue(
		t,
		trends.Items[0],
		[]string{"ranking", "formula_version"},
		"citation-intelligence/v1",
	)
	assertNestedJSONValue(
		t,
		trends.Items[0],
		[]string{"ranking", "window_days"},
		float64(30),
	)
	assertNestedJSONValue(
		t,
		trends.Items[1],
		[]string{"paper", "id"},
		fixtures[1].workID.String(),
	)
	assertNestedJSONValue(t, trends.Items[1], []string{"score"}, float64(0.5))

	fast, err := repository.Paper(context.Background(), fixtures[0].workID)
	if err != nil {
		t.Fatalf("Paper(fast) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		fast.Payload,
		[]string{"trend_score", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		fast.Payload,
		[]string{"trend_score", "value"},
		float64(1),
	)
	flat, err := repository.Paper(context.Background(), fixtures[2].workID)
	if err != nil {
		t.Fatalf("Paper(flat) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		flat.Payload,
		[]string{"trend_score", "state"},
		"missing",
	)

	home, err := repository.Home(context.Background())
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "analysis", "formula_version", "value"},
		"citation-intelligence/v1",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "analysis", "sources", "value", "0"},
		"openalex",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "analysis", "coverage_ratio", "value"},
		float64(1),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "items", "0", "citation_delta", "value"},
		float64(32),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "items", "0", "cohort_percentile", "value"},
		float64(1),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "items", "1", "citation_delta", "value"},
		float64(10),
	)
}

func TestPublisherPublishesBiomedicalPayloadFromBoundProjection(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-biomedical",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-biomedical",
		title:             "Prospective oncology cohort",
		paperType:         "research_article",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCuration(t, pool, input, fixture)
	insertPublisherBiomedicalProjection(t, pool, fixture)

	generation, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	repository := mustRepository(t, pool)
	document, err := repository.Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper() error = %v", err)
	}

	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"journal", "title"},
		"Publisher Journal pubmed:publisher-biomedical",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"subjects", "0", "slug"},
		"oncology",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "descriptor_ui"},
		"D008175",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "label"},
		"Lung Neoplasms",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "is_major_topic"},
		true,
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "source_path"},
		"/PubmedArticle/MeshHeading[1]/DescriptorName",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "qualifiers", "0", "qualifier_ui"},
		"Q000401",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "qualifiers", "0", "label"},
		"therapy",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "qualifiers", "0", "is_major_topic"},
		false,
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "qualifiers", "0", "source_path"},
		"/PubmedArticle/MeshHeading[1]/QualifierName[1]",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"publication_types", "0"},
		"Randomized Controlled Trial",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"publication_types_state", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"jcr_assessment", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"jcr_assessment", "value", "decision"},
		"accepted",
	)
	for _, field := range []string{
		"citation_snapshots",
		"citation_percentile",
		"article_usage",
		"open_fulltext",
	} {
		assertNestedJSONValue(
			t,
			document.Payload,
			[]string{field, "state"},
			"missing",
		)
	}
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_velocity", "state"},
		"insufficient_evidence",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_velocity", "reason"},
		"citation velocity requires exact same-source boundary snapshots",
	)

	home, err := repository.Home(context.Background())
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	if home.Generation.ID != generation.ID {
		t.Fatalf(
			"Home() generation = %s, want %s",
			home.Generation.ID,
			generation.ID,
		)
	}
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"catalog_generation"},
		generation.ID.String(),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"latest_papers", "items", "0", "title"},
		"Prospective oncology cohort",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"coverage", "mesh_coverage_ratio", "value"},
		float64(1),
	)

	subjects, err := repository.Subjects(
		context.Background(),
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Subjects() error = %v", err)
	}
	if subjects.Generation.ID != generation.ID {
		t.Fatalf(
			"Subjects() generation = %s, want %s",
			subjects.Generation.ID,
			generation.ID,
		)
	}
	if len(subjects.Items) != 1 {
		t.Fatalf("Subjects() items = %d, want 1", len(subjects.Items))
	}
	assertNestedJSONValue(
		t,
		subjects.Items[0],
		[]string{"slug"},
		"oncology",
	)
	subject, err := repository.Subject(
		context.Background(),
		"oncology",
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Subject(oncology) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		subject.Payload,
		[]string{"mesh_distribution", "items", "0", "label"},
		"Lung Neoplasms",
	)
	assertNestedJSONValue(
		t,
		subject.Payload,
		[]string{"publication_type_distribution", "items", "0", "label"},
		"Randomized Controlled Trial",
	)

	journals, err := repository.Journals(
		context.Background(),
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Journals() error = %v", err)
	}
	if journals.Generation.ID != generation.ID {
		t.Fatalf(
			"Journals() generation = %s, want %s",
			journals.Generation.ID,
			generation.ID,
		)
	}
	if len(journals.Items) != 1 {
		t.Fatalf("Journals() items = %d, want 1", len(journals.Items))
	}
	assertNestedJSONValue(
		t,
		journals.Items[0],
		[]string{"jif", "value"},
		float64(12),
	)
	assertNestedJSONValue(
		t,
		journals.Items[0],
		[]string{"categories", "0", "name"},
		"Oncology",
	)
	journalSlug := "journal-" + strings.ReplaceAll(
		catalogWorkVenueID(t, pool, fixture.workID).String(),
		"-",
		"",
	)
	journal, err := repository.Journal(
		context.Background(),
		journalSlug,
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Journal(%s) error = %v", journalSlug, err)
	}
	assertNestedJSONValue(
		t,
		journal.Payload,
		[]string{"curation", "decision"},
		"accepted",
	)
	assertNestedJSONValue(
		t,
		journal.Payload,
		[]string{"recent_papers", "items", "0", "title"},
		"Prospective oncology cohort",
	)

	searchPage, err := repository.Papers(
		context.Background(),
		PaperListQuery{
			Query: "Lung Neoplasms",
			Sort:  PaperSortRelevance,
			Limit: 20,
		},
	)
	if err != nil {
		t.Fatalf("Papers(MeSH search) error = %v", err)
	}
	assertPaperIDs(t, searchPage.Items, fixture.workID)
}

func TestPublisherRejectsInconsistentExactJIFAcrossJCRCategories(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-inconsistent-jif",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-inconsistent-jif",
		title:             "Inconsistent JIF evidence",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues
		SET venue_type = 'journal',
		    issn_l = $2
		WHERE id = $1
	`, venueID, deterministicCatalogISSN(1400)); err != nil {
		t.Fatalf("prepare inconsistent-JIF Venue: %v", err)
	}
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(
		t,
		pool,
		input,
		subjectRuleID,
		[]catalogJCRVenueFixture{{
			venueID:     venueID,
			index:       1400,
			withSubject: true,
			extraMetrics: []catalogJCRMetricFixture{{
				category: "Immunology",
				jif:      "15",
				quartile: "Q2",
			}},
		}},
	)
	assessCatalogVenuePolicy(t, pool, input)
	assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)
	insertCatalogBiomedicalAnalysisRuns(
		t,
		pool,
		input,
		catalogBiomedicalCohortRevisionForFixtures(
			t,
			pool,
			input,
			[]publisherWorkFixture{fixture},
		),
		input.TrendAnalysisRunID,
	)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "inconsistent exact JIF") {
		t.Fatalf(
			"PublishCurrent(inconsistent exact JIF) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsForgedAcceptedAssessmentBelowJCRAdmissionThreshold(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-forged-accepted",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-forged-accepted",
		title:             "Forged accepted JCR evidence",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues
		SET venue_type = 'journal',
		    issn_l = $2
		WHERE id = $1
	`, venueID, deterministicCatalogISSN(1450)); err != nil {
		t.Fatalf("prepare forged-assessment Venue: %v", err)
	}
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(
		t,
		pool,
		input,
		subjectRuleID,
		[]catalogJCRVenueFixture{{
			venueID:     venueID,
			index:       1450,
			withSubject: true,
			primaryMetric: &catalogJCRMetricFixture{
				category: "Oncology",
				jif:      "9.999",
				quartile: "Q2",
			},
		}},
	)
	insertCatalogVenueAssessment(
		t,
		pool,
		input,
		venueID,
		input.VenuePolicyName,
		input.VenuePolicyVersion,
		input.JCRMetricYear,
		"accepted",
	)
	assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "does not satisfy JCR Q1") {
		t.Fatalf(
			"PublishCurrent(forged accepted assessment) error = %v, want exact JCR admission rejection",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsAcceptedAssessmentWhoseEvidenceDiffersFromExactReceipt(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-mismatched-assessment-evidence",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-mismatched-assessment-evidence",
		title:             "Mismatched accepted JCR evidence",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues
		SET venue_type = 'journal',
		    issn_l = $2
		WHERE id = $1
	`, venueID, deterministicCatalogISSN(1460)); err != nil {
		t.Fatalf("prepare mismatched-assessment Venue: %v", err)
	}
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(
		t,
		pool,
		input,
		subjectRuleID,
		[]catalogJCRVenueFixture{{
			venueID:     venueID,
			index:       1460,
			withSubject: true,
		}},
	)
	insertCatalogVenueAssessmentPayload(
		t,
		pool,
		input,
		venueID,
		input.VenuePolicyName,
		input.VenuePolicyVersion,
		input.JCRMetricYear,
		"accepted",
		`["jcr_q1"]`,
		fmt.Sprintf(`{
			"jcr_import_receipt_id":%q,
			"policy_version":"journal-all-q1/v2",
			"metric_year":%d,
			"venue_type":"journal",
			"categories":[{
				"category":"Oncology",
				"jif":"99",
				"quartile":"Q4",
				"status":"known",
				"source_name":"authorized-jcr"
			}]
		}`, input.JCRImportReceipt.String(), input.JCRMetricYear),
	)
	assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "assessment evidence does not match exact JCR receipt") {
		t.Fatalf(
			"PublishCurrent(mismatched accepted evidence) error = %v, want exact receipt mismatch",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsEligibilitySubjectEvidenceFromAnotherJCRReceipt(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-cross-receipt-subject",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-cross-receipt-subject",
		title:             "Cross-receipt Subject evidence",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues
		SET venue_type = 'journal',
		    issn_l = $2
		WHERE id = $1
	`, venueID, deterministicCatalogISSN(1470)); err != nil {
		t.Fatalf("prepare cross-receipt Venue: %v", err)
	}
	_, subjectRules := insertCatalogSubjectVersionCategories(
		t,
		pool,
		input.SubjectVersion,
		map[string]string{
			"immunology": "Immunology",
			"oncology":   "Oncology",
		},
	)
	primaryMetricID := uuid.MustParse(
		"00000000-0000-4000-8000-000000000101",
	)
	primaryLinkID := uuid.MustParse(
		"00000000-0000-4000-8000-000000000102",
	)
	insertCatalogJCRBundle(
		t,
		pool,
		input,
		subjectRules["Oncology"],
		[]catalogJCRVenueFixture{{
			venueID:         venueID,
			index:           1470,
			withSubject:     true,
			primaryMetricID: &primaryMetricID,
			subjectLinkID:   &primaryLinkID,
		}},
	)
	insertCatalogCrossReceiptSubjectEvidence(
		t,
		pool,
		venueID,
		input.JCRMetricYear,
		subjectRules["Immunology"],
	)
	assessCatalogVenuePolicy(t, pool, input)
	assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)
	insertCatalogBiomedicalAnalysisRuns(
		t,
		pool,
		input,
		catalogBiomedicalCohortRevisionForFixtures(
			t,
			pool,
			input,
			[]publisherWorkFixture{fixture},
		),
		input.TrendAnalysisRunID,
	)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "Subject evidence does not belong to JCR receipt") {
		t.Fatalf(
			"PublishCurrent(cross-receipt Subject) error = %v, want exact receipt rejection",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func insertCatalogCrossReceiptSubjectEvidence(
	t *testing.T,
	pool *pgxpool.Pool,
	venueID uuid.UUID,
	metricYear int,
	subjectRuleID uuid.UUID,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cross-receipt Subject fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	receiptID := uuid.MustParse("ffffffff-ffff-4fff-8fff-fffffffffff1")
	metricID := uuid.MustParse("ffffffff-ffff-4fff-8fff-fffffffffff2")
	linkID := uuid.MustParse("ffffffff-ffff-4fff-8fff-fffffffffff3")
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipts (
			id,
			file_sha256,
			source,
			imported_at,
			input_rows,
			inserted_rows,
			unchanged_rows
		) VALUES (
			$1,
			$2,
			'authorized-jcr-secondary',
			$3,
			1,
			1,
			0
		)
	`,
		receiptID,
		strings.Repeat("c", 64),
		time.Date(2026, time.July, 17, 8, 31, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("insert secondary JCR receipt: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO venue_metric_snapshots (
			id,
			venue_id,
			metric_year,
			category,
			jif,
			quartile,
			metric_status,
			source_name,
			source_license,
			captured_at,
			jcr_import_receipt_id
		) VALUES (
			$1,
			$2,
			$3,
			'Immunology',
			12,
			'Q1',
			'known',
			'authorized-jcr-secondary',
			'institution-authorized',
			$4,
			$5
		)
	`,
		metricID,
		venueID,
		metricYear,
		time.Date(2026, time.July, 17, 8, 31, 0, 0, time.UTC),
		receiptID,
	); err != nil {
		t.Fatalf("insert secondary JCR metric: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipt_metrics (
			import_receipt_id,
			metric_snapshot_id
		) VALUES ($1, $2)
	`, receiptID, metricID); err != nil {
		t.Fatalf("link secondary JCR receipt metric: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO journal_subject_metrics (
			id,
			venue_metric_snapshot_id,
			subject_rule_id,
			jcr_category
		) VALUES ($1, $2, $3, 'Immunology')
	`, linkID, metricID, subjectRuleID); err != nil {
		t.Fatalf("insert secondary Subject metric link: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit cross-receipt Subject fixture: %v", err)
	}
}

func TestPublisherLoadsNormalizedPayloadByBoundAssertionID(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-normalized-binding",
		canonicalKey:      "doi:10.1000/publisher-normalized-binding",
		title:             "Bound normalized payload",
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
	})
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO ingestion_normalized_records (
			raw_event_id, source_record_uuid, normalization_policy_version,
			payload_schema_version, normalized_payload
		) VALUES (
			$1, $2, 'normalize/v1', 'normalized-record/v3',
			'{
				"source":"openalex",
				"source_record_id":"openalex:publisher-normalized-binding",
				"canonical_key":"doi:10.1000/publisher-normalized-binding",
				"title":"Unbound decoy payload"
			}'
		)
	`, fixture.rawEventID, fixture.sourceRecord); err != nil {
		t.Fatalf("insert unbound normalized assertion: %v", err)
	}

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin normalized binding read: %v", err)
	}
	defer tx.Rollback(context.Background())
	states, err := loadCurrentSourceStates(context.Background(), tx)
	if err != nil {
		t.Fatalf("loadCurrentSourceStates() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("current source states = %d, want one bound assertion", len(states))
	}
	payload, ok := states[0].normalizedPayload.(map[string]any)
	if !ok || payload["title"] != "Bound normalized payload" {
		t.Fatalf(
			"bound normalized payload = %#v, want the source-state assertion",
			states[0].normalizedPayload,
		)
	}
}

func TestPublisherPreservesKnownUnknownAndMissingWithoutInventingValues(t *testing.T) {
	pool := openCatalogTestPool(t)
	known := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-known",
		canonicalKey:      "doi:10.1000/publisher-known",
		title:             "Known Curation",
		paperType:         "review",
		jcrDecision:       "accepted",
		jcrMatchedRules:   `["jcr_q1"]`,
		jcrEvidence:       `{"venue_type":"journal","quartiles":["Q1"]}`,
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		publishedAt:       time.Date(2026, time.July, 14, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 14, 9, 0, 0, 0, time.UTC),
	})
	unknown := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-unknown",
		canonicalKey:      "doi:10.1000/publisher-unknown",
		title:             "Unknown Curation",
		jcrDecision:       "unknown",
		jcrMatchedRules:   `[]`,
		jcrEvidence:       `{"venue_type":"journal","reason":"metric_unknown"}`,
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC),
	})
	missing := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-missing",
		canonicalKey:      "doi:10.1000/publisher-missing",
		title:             "Missing Curation",
		noJCRAssessment:   true,
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 14, 11, 0, 0, 0, time.UTC),
	})

	publisher := mustPublisher(t, pool)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCuration(t, pool, input, known, unknown, missing)
	if _, err := publisher.PublishCurrent(context.Background(), input); err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	repository := mustRepository(t, pool)

	for _, test := range []struct {
		name string
		id   uuid.UUID
	}{
		{"known", known.workID},
		{"unknown", unknown.workID},
		{"missing", missing.workID},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, err := repository.Paper(context.Background(), test.id)
			if err != nil {
				t.Fatalf("Paper(%s) error = %v", test.id, err)
			}
			assertNestedJSONValue(
				t,
				document.Payload,
				[]string{"curation", "state"},
				"known",
			)
		})
	}

	missingDocument, err := repository.Paper(context.Background(), missing.workID)
	if err != nil {
		t.Fatalf("Paper(missing evidence) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		missingDocument.Payload,
		[]string{"published_at", "state"},
		"missing",
	)
	assertNestedJSONValue(
		t,
		missingDocument.Payload,
		[]string{"has_code", "state"},
		"unknown",
	)
}

func TestPublisherReturnsEmptyDomainWithoutWritingGeneration(t *testing.T) {
	pool := openCatalogTestPool(t)
	insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-excluded",
		canonicalKey:      "doi:10.1000/publisher-excluded",
		title:             "Excluded Work",
		scopeStatus:       "excluded",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
	})
	publisher := mustPublisher(t, pool)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherEmptyCurationReferences(t, pool, input)

	_, err := publisher.PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrEmptyDomain) {
		t.Fatalf("PublishCurrent() error = %v, want ErrEmptyDomain", err)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherReturnsNotReadyForPendingOrBrokenIncludedStateWithoutWrites(t *testing.T) {
	tests := []struct {
		name    string
		options publisherWorkOptions
	}{
		{
			name: "pending current source state",
			options: publisherWorkOptions{
				eventKey:          "openalex:publisher-pending",
				canonicalKey:      "doi:10.1000/publisher-pending",
				title:             "Pending Work",
				scopeStatus:       "pending",
				includeWorkLink:   true,
				includeWorkID:     true,
				includeNormalized: true,
				sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "included state without work ID",
			options: publisherWorkOptions{
				eventKey:          "openalex:publisher-no-work",
				canonicalKey:      "doi:10.1000/publisher-no-work",
				title:             "No Work Link",
				scopeStatus:       "included",
				includeWorkLink:   false,
				includeWorkID:     false,
				includeNormalized: true,
				sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := openCatalogTestPool(t)
			insertPublisherVisibleWork(t, pool, test.options)
			publisher := mustPublisher(t, pool)
			input := catalogCurationInputAt(
				time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
			)
			preparePublisherEmptyCurationReferences(t, pool, input)

			_, err := publisher.PublishCurrent(context.Background(), input)
			if !errors.Is(err, ErrCatalogNotReady) {
				t.Fatalf("PublishCurrent() error = %v, want ErrCatalogNotReady", err)
			}
			assertNoCatalogWrites(t, pool)
		})
	}
}

func TestPublisherRejectsDeterministicSlugCollisionsWithoutPartialPublication(t *testing.T) {
	pool := openCatalogTestPool(t)
	var fixtures []publisherWorkFixture
	for index, topic := range []string{"Agent Systems", "Agent-Systems"} {
		fixtures = append(fixtures, insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          fmt.Sprintf("openalex:publisher-collision-%d", index),
			canonicalKey:      fmt.Sprintf("doi:10.1000/publisher-collision-%d", index),
			title:             fmt.Sprintf("Collision Work %d", index),
			topicNames:        []string{topic},
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
			sourceTime:        time.Date(2026, time.July, 15, 9+index, 0, 0, 0, time.UTC),
		}))
	}
	publisher := mustPublisher(t, pool)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCuration(t, pool, input, fixtures...)

	_, err := publisher.PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrCatalogNotReady) {
		t.Fatalf("PublishCurrent() error = %v, want ErrCatalogNotReady", err)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherReusesOnlyIdenticalImmutableGeneration(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-retry",
		canonicalKey:      "doi:10.1000/publisher-retry",
		title:             "Retry Work",
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
	})
	publisher := mustPublisher(t, pool)
	firstInput := catalogCurationInputAt(
		time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCuration(t, pool, firstInput, fixture)

	first, err := publisher.PublishCurrent(context.Background(), firstInput)
	if err != nil {
		t.Fatalf("PublishCurrent(first) error = %v", err)
	}
	second, err := publisher.PublishCurrent(context.Background(), firstInput)
	if err != nil {
		t.Fatalf("PublishCurrent(second) error = %v", err)
	}
	if second.ID != first.ID || second.SourceRevision != first.SourceRevision {
		t.Fatalf("retry generation = %#v, want original %#v", second, first)
	}

	var generations, publications int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM public_catalog_generations),
			(SELECT count(*) FROM public_catalog_publications)
	`).Scan(&generations, &publications); err != nil {
		t.Fatalf("count idempotent publication rows: %v", err)
	}
	if generations != 1 || publications != 1 {
		t.Fatalf(
			"idempotent publication rows = generations %d publications %d, want 1/1",
			generations,
			publications,
		)
	}

	changedInput := firstInput
	changedInput.GeneratedAt = time.Date(2026, time.July, 15, 13, 0, 0, 0, time.UTC)
	changed, err := publisher.PublishCurrent(context.Background(), changedInput)
	if err != nil {
		t.Fatalf("PublishCurrent(changed generated_at) error = %v", err)
	}
	if changed.ID == first.ID || changed.SourceRevision == first.SourceRevision {
		t.Fatalf(
			"changed generated_at generation = %#v, want a new immutable snapshot after %#v",
			changed,
			first,
		)
	}

	home, err := mustRepository(t, pool).Home(context.Background())
	if err != nil {
		t.Fatalf("Home(after changed generated_at) error = %v", err)
	}
	if home.Generation.ID != changed.ID {
		t.Fatalf(
			"Home() generation = %s, want changed generation %s",
			home.Generation.ID,
			changed.ID,
		)
	}
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"generated_at"},
		changedInput.GeneratedAt.Format(time.RFC3339Nano),
	)

	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM public_catalog_generations),
			(SELECT count(*) FROM public_catalog_publications)
	`).Scan(&generations, &publications); err != nil {
		t.Fatalf("count changed publication rows: %v", err)
	}
	if generations != 2 || publications != 2 {
		t.Fatalf(
			"changed publication rows = generations %d publications %d, want 2/2",
			generations,
			publications,
		)
	}
}

type publisherWorkOptions struct {
	eventKey                   string
	logicalSource              string
	canonicalKey               string
	title                      string
	topicNames                 []string
	methodNames                []string
	withCode                   bool
	withData                   bool
	withBenchmark              bool
	paperType                  string
	jcrDecision                string
	jcrMatchedRules            string
	jcrEvidence                string
	noJCRAssessment            bool
	publishedAt                time.Time
	sourceTime                 time.Time
	scopeStatus                string
	includeWorkLink            bool
	includeWorkID              bool
	includeNormalized          bool
	normalizedPayloadSchema    string
	includePublicationMetadata bool
	publicationModel           string
	publicationStatus          string
	publicationHistory         []publisherPublicationHistoryFixture
}

type publisherPublicationDateFixture struct {
	Year      int    `json:"year"`
	Month     int    `json:"month,omitempty"`
	Day       int    `json:"day,omitempty"`
	Precision string `json:"precision"`
}

type publisherPublicationHistoryFixture struct {
	Status     string                          `json:"status"`
	Date       publisherPublicationDateFixture `json:"date"`
	SourcePath string                          `json:"source_path"`
	Ordinal    int                             `json:"ordinal"`
}

func publisherPublicationHistoryEntry(
	status string,
	year int,
	month time.Month,
	day int,
	precision string,
	sourcePath string,
	ordinal int,
) publisherPublicationHistoryFixture {
	return publisherPublicationHistoryFixture{
		Status: status,
		Date: publisherPublicationDateFixture{
			Year:      year,
			Month:     int(month),
			Day:       day,
			Precision: precision,
		},
		SourcePath: sourcePath,
		Ordinal:    ordinal,
	}
}

type publisherWorkFixture struct {
	workID                uuid.UUID
	sourceRecord          uuid.UUID
	rawEventID            uuid.UUID
	normalizedAssertionID uuid.UUID
}

func insertPublisherVisibleWork(
	t *testing.T,
	pool *pgxpool.Pool,
	options publisherWorkOptions,
) publisherWorkFixture {
	t.Helper()

	if options.eventKey == "" {
		options.eventKey = "openalex:" + uuid.NewString()
	}
	if options.logicalSource == "" {
		options.logicalSource = "openalex"
	}
	if options.canonicalKey == "" {
		options.canonicalKey = "openalex:W" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if options.title == "" {
		options.title = "Publisher Work"
	}
	if options.sourceTime.IsZero() {
		options.sourceTime = time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC)
	}
	if options.scopeStatus == "" {
		options.scopeStatus = "included"
	}
	if options.normalizedPayloadSchema == "" {
		options.normalizedPayloadSchema = "normalized-record/v2"
	}

	fixture := publisherWorkFixture{
		workID:       uuid.New(),
		sourceRecord: uuid.New(),
		rawEventID:   uuid.New(),
	}
	jobID := uuid.New()
	venueID := uuid.New()
	ctx := context.Background()
	hash := sha256.Sum256([]byte(options.eventKey))
	contentHash := hex.EncodeToString(hash[:])

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publisher fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (
			id, source, job_type, idempotency_key, status, payload, max_attempts,
			batch_key, stage
		) VALUES (
			$1, $3, 'sync', $2, 'succeeded', '{}', 1, $2, 'project'
		)
	`, jobID, "publisher:"+options.eventKey, options.logicalSource); err != nil {
		t.Fatalf("insert publisher ingestion job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_raw_events (
			id, job_id, logical_source, event_key, event_kind, source_record_id,
			source_time, tie_break_key, position, content_hash, raw_format, raw_payload
		) VALUES (
			$1, $2, $8, $3, 'upsert', $4, $5, 'publisher-fixture', 1,
			$6, 'json', $7
		)
	`,
		fixture.rawEventID,
		jobID,
		options.eventKey,
		options.eventKey,
		options.sourceTime,
		contentHash,
		[]byte(`{"fixture":"publisher"}`),
		options.logicalSource,
	); err != nil {
		t.Fatalf("insert publisher raw event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_records (
			id, source, source_record_id, source_identity, source_time,
			content_hash, raw_payload
		) VALUES (
			$1, $6, $2, $3::jsonb, $4, $5, '{"fixture":"publisher"}'
		)
	`,
		fixture.sourceRecord,
		options.eventKey,
		fmt.Sprintf(`{"canonical_key":%q}`, options.canonicalKey),
		options.sourceTime,
		contentHash,
		options.logicalSource,
	); err != nil {
		t.Fatalf("insert publisher source record: %v", err)
	}
	if options.includeNormalized {
		normalizedPayloadMap := map[string]any{
			"source":           options.logicalSource,
			"source_record_id": options.eventKey,
			"canonical_key":    options.canonicalKey,
			"title":            options.title,
		}
		if options.includePublicationMetadata || options.publicationModel != "" {
			normalizedPayloadMap["publication_model"] = options.publicationModel
		}
		if options.includePublicationMetadata || options.publicationStatus != "" {
			normalizedPayloadMap["publication_status"] = options.publicationStatus
		}
		if options.normalizedPayloadSchema == "normalized-record/v3" {
			history := options.publicationHistory
			if history == nil {
				history = []publisherPublicationHistoryFixture{}
			}
			normalizedPayloadMap["publication_history"] = history
		}
		normalizedPayload, err := json.Marshal(normalizedPayloadMap)
		if err != nil {
			t.Fatalf("encode publisher normalized payload: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO ingestion_normalized_records (
				raw_event_id, source_record_uuid, normalization_policy_version,
				payload_schema_version, normalized_payload
			) VALUES ($1, $2, 'normalize/v1', $3, $4::jsonb)
			RETURNING id
		`,
			fixture.rawEventID,
			fixture.sourceRecord,
			options.normalizedPayloadSchema,
			normalizedPayload,
		).Scan(
			&fixture.normalizedAssertionID,
		); err != nil {
			t.Fatalf("insert publisher normalized record: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO venues (
			id, venue_type, display_title, source_scheme, source_identifier
		) VALUES ($1, 'journal', $2, 'openalex', $3)
	`,
		venueID,
		"Publisher Journal "+options.eventKey,
		"V"+strings.ReplaceAll(fixture.workID.String(), "-", ""),
	); err != nil {
		t.Fatalf("insert publisher venue: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO works (
			id, canonical_key, status, title, abstract, published_at, venue_id
		) VALUES (
			$1, $2, 'active', $3, 'Source-backed abstract',
			NULLIF($4, '0001-01-01 00:00:00+00'::timestamptz), $5
		)
	`,
		fixture.workID,
		options.canonicalKey,
		options.title,
		options.publishedAt,
		venueID,
	); err != nil {
		t.Fatalf("insert publisher work: %v", err)
	}
	if options.includeWorkLink {
		if _, err := tx.Exec(ctx, `
			INSERT INTO source_record_works (source_record_id, work_id)
			VALUES ($1, $2)
		`, fixture.sourceRecord, fixture.workID); err != nil {
			t.Fatalf("insert publisher source/work association: %v", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_source_states (
			logical_source, event_key, raw_event_id, source_record_uuid, work_id,
			source_time, tie_break_key, position, scope_status,
			scope_policy_version, projection_policy_version,
			normalized_assertion_id, is_deleted
		) VALUES (
			$8, $1, $2, $3, NULLIF($4, '')::uuid, $5,
			'publisher-fixture', 1, $6, 'scope/v1', 'projection/v1',
			NULLIF($7, '')::uuid, false
		)
	`,
		options.eventKey,
		fixture.rawEventID,
		fixture.sourceRecord,
		optionalUUID(options.includeWorkID, fixture.workID),
		options.sourceTime,
		options.scopeStatus,
		optionalUUID(options.includeNormalized, fixture.normalizedAssertionID),
		options.logicalSource,
	); err != nil {
		t.Fatalf("insert publisher source state: %v", err)
	}
	if options.includeWorkLink &&
		options.includeWorkID &&
		options.includeNormalized {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ingestion_projection_assertions (
				raw_event_id,
				normalized_assertion_id,
				source_record_uuid,
				work_id,
				job_id,
				scope_policy_version,
				projection_policy_version,
				record_payload
			) VALUES (
				$1, $2, $3, $4, $5,
				'scope/v1', 'projection/v1', '{"fixture":"publisher-winner"}'
			)
		`,
			fixture.rawEventID,
			fixture.normalizedAssertionID,
			fixture.sourceRecord,
			fixture.workID,
			jobID,
		); err != nil {
			t.Fatalf("insert publisher projection winner assertion: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_projection_states (
				work_id,
				raw_event_id,
				normalized_assertion_id,
				source_record_uuid,
				source_time,
				tie_break_key,
				position,
				scope_policy_version,
				projection_policy_version
			)
			SELECT
				$1,
				raw_event_id,
				normalized_assertion_id,
				source_record_uuid,
				source_time,
				tie_break_key,
				position,
				scope_policy_version,
				projection_policy_version
			FROM ingestion_source_states
			WHERE work_id = $1
		`, fixture.workID); err != nil {
			t.Fatalf("insert publisher projection winner state: %v", err)
		}
	}

	if options.includeWorkLink {
		assertions := map[string]map[string]any{
			"published_at": {
				"state": stateForTime(options.publishedAt),
				"value": optionalTimeValue(options.publishedAt),
			},
			"abstract": {
				"state": "known",
				"value": "Source-backed abstract",
			},
			"code_urls": {
				"state": boolState(options.withCode),
				"value": optionalStringArray(
					options.withCode,
					"https://github.com/example/project",
				),
			},
		}
		if options.paperType != "" {
			assertions["paper_type"] = map[string]any{
				"state": "known",
				"value": options.paperType,
			}
		}
		for name, value := range assertions {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("encode publisher assertion %s: %v", name, err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO field_assertions (
					work_id, source_record_id, field_name, asserted_value, asserted_at
				) VALUES ($1, $2, $3, $4, $5)
			`,
				fixture.workID,
				fixture.sourceRecord,
				name,
				encoded,
				options.sourceTime,
			); err != nil {
				t.Fatalf("insert publisher assertion %s: %v", name, err)
			}
		}
	}

	authorID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO authors (id, display_name) VALUES ($1, 'Ada Lovelace')
	`, authorID); err != nil {
		t.Fatalf("insert publisher author: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_authors (work_id, author_id, author_position)
		VALUES ($1, $2, 1)
	`, fixture.workID, authorID); err != nil {
		t.Fatalf("associate publisher author: %v", err)
	}
	for _, name := range options.topicNames {
		topicID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO topics (id, name) VALUES ($1, $2)
		`, topicID, name); err != nil {
			t.Fatalf("insert publisher topic %q: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_topics (work_id, topic_id, source_record_id)
			VALUES ($1, $2, $3)
		`, fixture.workID, topicID, fixture.sourceRecord); err != nil {
			t.Fatalf("associate publisher topic %q: %v", name, err)
		}
	}
	for _, name := range options.methodNames {
		methodID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO methods (id, name) VALUES ($1, $2)
		`, methodID, name); err != nil {
			t.Fatalf("insert publisher method %q: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_methods (work_id, method_id, source_record_id)
			VALUES ($1, $2, $3)
		`, fixture.workID, methodID, fixture.sourceRecord); err != nil {
			t.Fatalf("associate publisher method %q: %v", name, err)
		}
	}
	if options.withCode {
		repositoryID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO code_repositories (
				id, canonical_url, host, owner_name, repository_name
			) VALUES ($1, $2, 'github.com', 'example', $3)
		`,
			repositoryID,
			"https://github.com/example/"+fixture.workID.String(),
			fixture.workID.String(),
		); err != nil {
			t.Fatalf("insert publisher code repository: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_code_repositories (work_id, repository_id, relation_type)
			VALUES ($1, $2, 'official')
		`, fixture.workID, repositoryID); err != nil {
			t.Fatalf("associate publisher code repository: %v", err)
		}
	}
	if options.withData {
		datasetID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO datasets (id, name) VALUES ($1, $2)
		`, datasetID, "Dataset "+fixture.workID.String()); err != nil {
			t.Fatalf("insert publisher dataset: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_datasets (work_id, dataset_id, relation_type)
			VALUES ($1, $2, 'used')
		`, fixture.workID, datasetID); err != nil {
			t.Fatalf("associate publisher dataset: %v", err)
		}
	}
	if options.withBenchmark {
		benchmarkID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO benchmarks (id, name) VALUES ($1, $2)
		`, benchmarkID, "Benchmark "+fixture.workID.String()); err != nil {
			t.Fatalf("insert publisher benchmark: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_benchmarks (work_id, benchmark_id, relation_type)
			VALUES ($1, $2, 'evaluated')
		`, fixture.workID, benchmarkID); err != nil {
			t.Fatalf("associate publisher benchmark: %v", err)
		}
	}

	if !options.noJCRAssessment {
		policyID := uuid.New()
		decision := options.jcrDecision
		if decision == "" {
			decision = "accepted"
		}
		matchedRules := options.jcrMatchedRules
		if matchedRules == "" {
			matchedRules = `["jcr_q1"]`
		}
		evidence := options.jcrEvidence
		if evidence == "" {
			evidence = `{"venue_type":"journal","quartiles":["Q1"]}`
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO venue_policy_versions (
				id, policy_name, version_number, definition, effective_at
			) VALUES ($1, $2, 1, '{"kind":"publisher-fixture"}', $3)
		`,
			policyID,
			"publisher-policy-"+fixture.workID.String(),
			options.sourceTime,
		); err != nil {
			t.Fatalf("insert publisher JCR policy: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO venue_policy_assessments (
				venue_id, policy_version_id, metric_year, decision,
				matched_rules, evidence, assessed_at
			) VALUES ($1, $2, 2026, $3, $4::jsonb, $5::jsonb, $6)
		`,
			venueID,
			policyID,
			decision,
			matchedRules,
			evidence,
			options.sourceTime,
		); err != nil {
			t.Fatalf("insert publisher JCR assessment: %v", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publisher fixture: %v", err)
	}
	return fixture
}

type publisherPublicationEventFixture struct {
	kind       string
	date       *time.Time
	precision  string
	sourceDate string
	statusRaw  string
	modelRaw   *string
	sourcePath string
	ordinal    int
}

func publisherPublicationHistoryFromEvents(
	t *testing.T,
	events []publisherPublicationEventFixture,
) []publisherPublicationHistoryFixture {
	t.Helper()
	history := make([]publisherPublicationHistoryFixture, 0, len(events))
	for _, event := range events {
		var date publisherPublicationDateFixture
		if event.sourceDate != "" {
			if err := json.Unmarshal([]byte(event.sourceDate), &date); err != nil {
				t.Fatalf(
					"decode publication history source date ordinal %d: %v",
					event.ordinal,
					err,
				)
			}
		} else {
			if event.date == nil {
				t.Fatalf(
					"publication history event ordinal %d lacks a source date",
					event.ordinal,
				)
			}
			date = publisherPublicationDateFixture{
				Year:      event.date.Year(),
				Month:     int(event.date.Month()),
				Day:       event.date.Day(),
				Precision: event.precision,
			}
		}
		history = append(history, publisherPublicationHistoryFixture{
			Status:     event.statusRaw,
			Date:       date,
			SourcePath: event.sourcePath,
			Ordinal:    event.ordinal,
		})
	}
	return history
}

type publisherPublicationStateFixture struct {
	projectionAssertionID uuid.UUID
	printDate             *time.Time
	printState            string
	electronicDate        *time.Time
	electronicState       string
	aheadDate             *time.Time
	aheadState            string
	acceptedDate          *time.Time
	acceptedState         string
	publicationModel      *string
	publicationStatus     *string
	events                []publisherPublicationEventFixture
}

type publicationEvidenceQueryCounter struct {
	count atomic.Int64
}

func (counter *publicationEvidenceQueryCounter) record(sql string) {
	if strings.Contains(sql, "work_publication_states") ||
		strings.Contains(sql, "work_publication_event_assertions") {
		counter.count.Add(1)
	}
}

type publicationEvidenceCountingDatabase struct {
	database *pgxpool.Pool
	counter  *publicationEvidenceQueryCounter
}

func (database publicationEvidenceCountingDatabase) BeginTx(
	ctx context.Context,
	options pgx.TxOptions,
) (pgx.Tx, error) {
	tx, err := database.database.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return publicationEvidenceCountingTx{
		Tx:      tx,
		counter: database.counter,
	}, nil
}

type publicationEvidenceCountingTx struct {
	pgx.Tx
	counter *publicationEvidenceQueryCounter
}

func (tx publicationEvidenceCountingTx) Query(
	ctx context.Context,
	sql string,
	args ...any,
) (pgx.Rows, error) {
	tx.counter.record(sql)
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx publicationEvidenceCountingTx) QueryRow(
	ctx context.Context,
	sql string,
	args ...any,
) pgx.Row {
	tx.counter.record(sql)
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func insertPublisherPublicationState(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
	state publisherPublicationStateFixture,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	if state.projectionAssertionID == uuid.Nil {
		state.projectionAssertionID = publisherProjectionAssertionID(t, pool, fixture.workID)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publisher publication state fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	for _, event := range state.events {
		sourceDate := event.sourceDate
		if sourceDate == "" {
			if event.date == nil {
				t.Fatalf("publication event %s lacks source date", event.kind)
			}
			encoded, err := json.Marshal(map[string]any{
				"year":      event.date.Year(),
				"month":     int(event.date.Month()),
				"day":       event.date.Day(),
				"precision": event.precision,
			})
			if err != nil {
				t.Fatalf("encode publication event source date: %v", err)
			}
			sourceDate = string(encoded)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_publication_event_assertions (
				projection_assertion_id,
				normalized_assertion_id,
				source_record_id,
				work_id,
				event_kind,
				event_date,
				date_precision,
				source_date,
				status_raw,
				publication_model_raw,
				source_path,
				ordinal
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8::jsonb, $9, $10, $11, $12
			)
		`,
			state.projectionAssertionID,
			fixture.normalizedAssertionID,
			fixture.sourceRecord,
			fixture.workID,
			event.kind,
			event.date,
			event.precision,
			sourceDate,
			event.statusRaw,
			event.modelRaw,
			event.sourcePath,
			event.ordinal,
		); err != nil {
			t.Fatalf("insert publisher publication event %s: %v", event.kind, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_publication_states (
			work_id,
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			print_published_on,
			print_published_state,
			electronic_published_on,
			electronic_published_state,
			ahead_of_print_on,
			ahead_of_print_state,
			accepted_on,
			accepted_state,
			publication_model_raw,
			publication_status_raw
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13, $14
		)
	`,
		fixture.workID,
		state.projectionAssertionID,
		fixture.normalizedAssertionID,
		fixture.sourceRecord,
		state.printDate,
		state.printState,
		state.electronicDate,
		state.electronicState,
		state.aheadDate,
		state.aheadState,
		state.acceptedDate,
		state.acceptedState,
		state.publicationModel,
		state.publicationStatus,
	); err != nil {
		t.Fatalf("insert publisher publication state: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publisher publication state fixture: %v", err)
	}
	return state.projectionAssertionID
}

func publisherProjectionAssertionID(
	t *testing.T,
	pool *pgxpool.Pool,
	workID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var projectionAssertionID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT assertion.id
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.work_id = state.work_id
		 AND assertion.raw_event_id = state.raw_event_id
		 AND assertion.normalized_assertion_id = state.normalized_assertion_id
		 AND assertion.source_record_uuid = state.source_record_uuid
		 AND assertion.scope_policy_version = state.scope_policy_version
		 AND assertion.projection_policy_version = state.projection_policy_version
		WHERE state.work_id = $1
	`, workID).Scan(&projectionAssertionID); err != nil {
		t.Fatalf("query publisher projection assertion: %v", err)
	}
	return projectionAssertionID
}

func insertPublisherAlternateProjectionAssertion(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
	projectionPolicyVersion string,
) uuid.UUID {
	t.Helper()
	var jobID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT job_id
		FROM ingestion_raw_events
		WHERE id = $1
	`, fixture.rawEventID).Scan(&jobID); err != nil {
		t.Fatalf("query alternate publication projection job: %v", err)
	}
	projectionAssertionID := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO ingestion_projection_assertions (
			id,
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			work_id,
			job_id,
			scope_policy_version,
			projection_policy_version,
			record_payload
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			'scope/v1', $7, '{"fixture":"publication-decoy"}'
		)
	`,
		projectionAssertionID,
		fixture.rawEventID,
		fixture.normalizedAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		jobID,
		projectionPolicyVersion,
	); err != nil {
		t.Fatalf("insert alternate publication projection assertion: %v", err)
	}
	return projectionAssertionID
}

func mustPublisher(t *testing.T, pool *pgxpool.Pool) *Publisher {
	t.Helper()

	publisher, err := NewPublisher(pool)
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	return publisher
}

func assertNoCatalogWrites(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	var generations, publications, current int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM public_catalog_generations),
			(SELECT count(*) FROM public_catalog_publications),
			(SELECT count(*) FROM public_catalog_current)
	`).Scan(&generations, &publications, &current); err != nil {
		t.Fatalf("count public catalog writes: %v", err)
	}
	if generations != 0 || publications != 0 || current != 0 {
		t.Fatalf(
			"catalog writes = generations %d publications %d current %d, want 0/0/0",
			generations,
			publications,
			current,
		)
	}
}

func assertNestedJSONValue(
	t *testing.T,
	payload json.RawMessage,
	path []string,
	want any,
) {
	t.Helper()

	var current any
	if err := json.Unmarshal(payload, &current); err != nil {
		t.Fatalf("decode JSON payload: %v", err)
	}
	for _, segment := range path {
		if object, ok := current.(map[string]any); ok {
			current, ok = object[segment]
			if !ok {
				t.Fatalf("JSON path %v is missing segment %q in %s", path, segment, payload)
			}
			continue
		}
		items, ok := current.([]any)
		if !ok {
			t.Fatalf("JSON path %v reached non-container %#v", path, current)
		}
		index := -1
		if _, err := fmt.Sscan(segment, &index); err != nil ||
			index < 0 ||
			index >= len(items) {
			t.Fatalf("JSON path %v has invalid array index %q", path, segment)
		}
		current = items[index]
	}
	if fmt.Sprint(current) != fmt.Sprint(want) {
		t.Fatalf("JSON path %v = %#v, want %#v; payload=%s", path, current, want, payload)
	}
}

func insertPublisherBiomedicalProjection(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publisher biomedical fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	var jobID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT job_id
		FROM ingestion_raw_events
		WHERE id = $1
	`, fixture.rawEventID).Scan(&jobID); err != nil {
		t.Fatalf("query publisher biomedical job: %v", err)
	}
	var projectionAssertionID uuid.UUID
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO ingestion_projection_assertions (
				id,
				raw_event_id,
				normalized_assertion_id,
				source_record_uuid,
				work_id,
				job_id,
				scope_policy_version,
				projection_policy_version,
				record_payload
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				'scope/v1', 'projection/v1', '{"fixture":"biomedical"}'
			)
			ON CONFLICT (
				normalized_assertion_id,
				scope_policy_version,
				projection_policy_version
			) DO NOTHING
			RETURNING id
		)
		SELECT id FROM inserted
		UNION ALL
		SELECT id
		FROM ingestion_projection_assertions
		WHERE normalized_assertion_id = $3
		  AND scope_policy_version = 'scope/v1'
		  AND projection_policy_version = 'projection/v1'
		LIMIT 1
	`,
		uuid.New(),
		fixture.rawEventID,
		fixture.normalizedAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		jobID,
	).Scan(&projectionAssertionID); err != nil {
		t.Fatalf("insert publisher biomedical projection assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_projection_states (
			work_id,
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			source_time,
			tie_break_key,
			position,
			scope_policy_version,
			projection_policy_version
		)
		SELECT
			$1,
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			source_time,
			tie_break_key,
			position,
			scope_policy_version,
			projection_policy_version
		FROM ingestion_source_states
		WHERE work_id = $1
		ON CONFLICT (work_id) DO NOTHING
	`, fixture.workID); err != nil {
		t.Fatalf("insert publisher biomedical winner state: %v", err)
	}

	var descriptorID uuid.UUID
	var qualifierID uuid.UUID
	var publicationTypeID uuid.UUID
	headingID := uuid.New()
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO mesh_descriptors (id, descriptor_ui)
			VALUES ($1, 'D008175')
			ON CONFLICT (descriptor_ui) DO NOTHING
			RETURNING id
		)
		SELECT id FROM inserted
		UNION ALL
		SELECT id FROM mesh_descriptors WHERE descriptor_ui = 'D008175'
		LIMIT 1
	`, uuid.New()).Scan(&descriptorID); err != nil {
		t.Fatalf("insert publisher MeSH descriptor: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO mesh_qualifiers (id, qualifier_ui)
			VALUES ($1, 'Q000401')
			ON CONFLICT (qualifier_ui) DO NOTHING
			RETURNING id
		)
		SELECT id FROM inserted
		UNION ALL
		SELECT id FROM mesh_qualifiers WHERE qualifier_ui = 'Q000401'
		LIMIT 1
	`, uuid.New()).Scan(&qualifierID); err != nil {
		t.Fatalf("insert publisher MeSH qualifier: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO publication_types (id, publication_type_ui)
			VALUES ($1, 'D016449')
			ON CONFLICT (publication_type_ui) DO NOTHING
			RETURNING id
		)
		SELECT id FROM inserted
		UNION ALL
		SELECT id
		FROM publication_types
		WHERE publication_type_ui = 'D016449'
		LIMIT 1
	`, uuid.New()).Scan(&publicationTypeID); err != nil {
		t.Fatalf("insert publisher Publication Type: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_mesh_headings (
			id,
			projection_assertion_id,
			source_record_id,
			work_id,
			descriptor_id,
			source_path,
			descriptor_label,
			is_major_topic
		) VALUES (
			$1, $2, $3, $4, $5,
			'/PubmedArticle/MeshHeading[1]/DescriptorName',
			'Lung Neoplasms',
			true
		)
	`,
		headingID,
		projectionAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		descriptorID,
	); err != nil {
		t.Fatalf("insert publisher MeSH heading: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_mesh_qualifiers (
			projection_assertion_id,
			source_record_id,
			work_id,
			work_mesh_heading_id,
			qualifier_id,
			source_path,
			qualifier_label,
			is_major_topic
		) VALUES (
			$1, $2, $3, $4, $5,
			'/PubmedArticle/MeshHeading[1]/QualifierName[1]',
			'therapy',
			false
		)
	`,
		projectionAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		headingID,
		qualifierID,
	); err != nil {
		t.Fatalf("insert publisher MeSH qualifier assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_publication_types (
			projection_assertion_id,
			source_record_id,
			work_id,
			publication_type_id,
			source_path,
			publication_type_label
		) VALUES (
			$1, $2, $3, $4,
			'/PubmedArticle/PublicationType[1]',
			'Randomized Controlled Trial'
		)
	`,
		projectionAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		publicationTypeID,
	); err != nil {
		t.Fatalf("insert publisher Publication Type assertion: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publisher biomedical fixture: %v", err)
	}
}

func insertPublisherCitationSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
	count int64,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publisher citation fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	var jobID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT job_id
		FROM ingestion_raw_events
		WHERE id = $1
	`, fixture.rawEventID).Scan(&jobID); err != nil {
		t.Fatalf("query publisher citation job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			work_id,
			job_id,
			scope_policy_version,
			projection_policy_version,
			record_payload
		) VALUES (
			$1, $2, $3, $4, $5,
			'scope/v1', 'projection/v1', '{"fixture":"citation"}'
		)
		ON CONFLICT (
			normalized_assertion_id,
			scope_policy_version,
			projection_policy_version
		) DO NOTHING
	`, fixture.rawEventID, fixture.normalizedAssertionID, fixture.sourceRecord, fixture.workID, jobID); err != nil {
		t.Fatalf("insert publisher citation projection assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO citation_snapshots (
			work_id,
			source,
			observed_at,
			count,
			source_record_id,
			ingestion_job_id,
			retrieved_at,
			coverage,
			definition_version,
			dataset_version
		)
		SELECT
			$1,
			source,
			source_time,
			$2,
			id,
			$3,
			retrieved_at,
			1,
			'openalex-cited-by-count/v1',
			'openalex-source-record/' || id::text
		FROM source_records
		WHERE id = $4
	`, fixture.workID, count, jobID, fixture.sourceRecord); err != nil {
		t.Fatalf("insert publisher citation snapshot: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publisher citation fixture: %v", err)
	}
}

func insertPublisherAdditionalCitationSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
	observedAt time.Time,
	count int64,
) {
	t.Helper()
	ctx := context.Background()
	sourceRecordID := uuid.New()
	rawEventID := uuid.New()
	jobID := uuid.New()
	eventKey := "openalex:citation:" + uuid.NewString()
	sum := sha256.Sum256([]byte(eventKey))
	contentHash := hex.EncodeToString(sum[:])
	retrievedAt := observedAt.Add(time.Hour)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin additional publisher citation fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (
			id, source, job_type, idempotency_key, status, payload, max_attempts,
			batch_key, stage
		) VALUES (
			$1, 'openalex', 'sync', $2, 'succeeded', '{}', 1, $2, 'project'
		)
	`, jobID, "publisher:"+eventKey); err != nil {
		t.Fatalf("insert additional citation ingestion job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_raw_events (
			id, job_id, logical_source, event_key, event_kind, source_record_id,
			source_time, tie_break_key, position, content_hash, raw_format, raw_payload
		) VALUES (
			$1, $2, 'openalex', $3, 'upsert', $3, $4,
			'publisher-citation-fixture', 1, $5, 'json', $6
		)
	`, rawEventID, jobID, eventKey, observedAt, contentHash, []byte(`{"fixture":"citation"}`)); err != nil {
		t.Fatalf("insert additional citation raw event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_records (
			id, source, source_record_id, source_identity, source_time,
			content_hash, raw_payload, retrieved_at
		) VALUES (
			$1, 'openalex', $2, $3::jsonb, $4, $5,
			'{"fixture":"citation"}', $6
		)
	`,
		sourceRecordID,
		eventKey,
		fmt.Sprintf(`{"canonical_key":%q}`, fixture.workID.String()),
		observedAt,
		contentHash,
		retrievedAt,
	); err != nil {
		t.Fatalf("insert additional citation source record: %v", err)
	}
	var normalizedAssertionID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id,
			source_record_uuid,
			normalization_policy_version,
			payload_schema_version,
			normalized_payload
		) VALUES (
			$1, $2, 'normalize/v1', 'normalized-record/v2',
			'{"fixture":"citation"}'
		)
		RETURNING id
	`, rawEventID, sourceRecordID).Scan(&normalizedAssertionID); err != nil {
		t.Fatalf("insert additional citation normalized record: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, sourceRecordID, fixture.workID); err != nil {
		t.Fatalf("associate additional citation source record: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			work_id,
			job_id,
			scope_policy_version,
			projection_policy_version,
			record_payload
		) VALUES (
			$1, $2, $3, $4, $5,
			'scope/v1', 'projection/v1', '{"fixture":"citation"}'
		)
	`,
		rawEventID,
		normalizedAssertionID,
		sourceRecordID,
		fixture.workID,
		jobID,
	); err != nil {
		t.Fatalf("insert additional citation projection assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO citation_snapshots (
			work_id,
			source,
			observed_at,
			count,
			source_record_id,
			ingestion_job_id,
			retrieved_at,
			coverage,
			definition_version,
			dataset_version
		) VALUES (
			$1, 'openalex', $2, $3, $4, $5, $6, 1,
			'openalex-cited-by-count/v1',
			$7
		)
	`,
		fixture.workID,
		observedAt,
		count,
		sourceRecordID,
		jobID,
		retrievedAt,
		"openalex-source-record/"+sourceRecordID.String(),
	); err != nil {
		t.Fatalf("insert additional publisher citation snapshot: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit additional publisher citation fixture: %v", err)
	}
}

func assertJSONArrayLength(
	t *testing.T,
	payload json.RawMessage,
	field string,
	want int,
) {
	t.Helper()

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode JSON payload: %v", err)
	}
	value, ok := decoded[field].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want DataValue object", field, decoded[field])
	}
	items, ok := value["value"].([]any)
	if !ok {
		t.Fatalf("%s.value = %#v, want array", field, value["value"])
	}
	if len(items) != want {
		t.Fatalf("%s.value length = %d, want %d", field, len(items), want)
	}
}

func optionalUUID(include bool, value uuid.UUID) string {
	if include {
		return value.String()
	}
	return ""
}

func stateForTime(value time.Time) string {
	if value.IsZero() {
		return "missing"
	}
	return "known"
}

func optionalTimeValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.Format(time.RFC3339Nano)
}

func boolState(value bool) string {
	if value {
		return "known"
	}
	return "unknown"
}

func optionalStringArray(include bool, value string) []string {
	if !include {
		return nil
	}
	return []string{value}
}
