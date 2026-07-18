package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/analysis"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublisherPublishesDailyPublicationUpdates(t *testing.T) {
	t.Run("publishes exact generation-bound collections and stable payload", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		generatedAt := time.Date(
			2026,
			time.July,
			17,
			23,
			30,
			0,
			0,
			time.FixedZone("UTC-4", -4*60*60),
		)
		input := catalogCurationInputAt(generatedAt)
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:                "pubmed:catalog-publication-updates",
			logicalSource:           "pubmed",
			canonicalKey:            "doi:10.1000/catalog-publication-updates",
			title:                   "Generation-bound publication updates",
			paperType:               "research_article",
			publishedAt:             time.Date(2026, time.July, 18, 1, 0, 0, 0, time.UTC),
			sourceTime:              time.Date(2026, time.July, 18, 2, 0, 0, 0, time.UTC),
			scopeStatus:             "included",
			includeWorkLink:         true,
			includeWorkID:           true,
			includeNormalized:       true,
			normalizedPayloadSchema: "normalized-record/v3",
			publicationModel:        "Print-Electronic",
			publicationStatus:       "epublish",
			publicationHistory: []publisherPublicationHistoryFixture{
				{
					Status: "accepted",
					Date: publisherPublicationDateFixture{
						Year:      2026,
						Month:     int(time.July),
						Day:       12,
						Precision: "day",
					},
					SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
					Ordinal:    1,
				},
				{
					Status: "accepted",
					Date: publisherPublicationDateFixture{
						Year:      2026,
						Month:     int(time.July),
						Day:       12,
						Precision: "day",
					},
					SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[2]",
					Ordinal:    2,
				},
				{
					Status: "aheadofprint",
					Date: publisherPublicationDateFixture{
						Year:      2026,
						Month:     int(time.July),
						Day:       15,
						Precision: "day",
					},
					SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[3]",
					Ordinal:    3,
				},
				{
					Status: "ppublish",
					Date: publisherPublicationDateFixture{
						Year:      2026,
						Month:     int(time.July),
						Day:       18,
						Precision: "day",
					},
					SourcePath: "/PubmedArticle/MedlineCitation/Article/Journal/JournalIssue/PubDate",
					Ordinal:    4,
				},
				{
					Status: "epublish",
					Date: publisherPublicationDateFixture{
						Year:      2026,
						Month:     int(time.July),
						Day:       18,
						Precision: "day",
					},
					SourcePath: "/PubmedArticle/MedlineCitation/Article/ArticleDate[1]",
					Ordinal:    5,
				},
			},
			noJCRAssessment: true,
		})
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherBiomedicalProjection(t, pool, fixture)

		printDate := publicationUpdateDate(2026, time.July, 18)
		electronicDate := publicationUpdateDate(2026, time.July, 18)
		acceptedDate := publicationUpdateDate(2026, time.July, 12)
		aheadDate := publicationUpdateDate(2026, time.July, 15)
		projectionAssertionID := insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printDate:         &printDate,
				printState:        "known",
				electronicDate:    &electronicDate,
				electronicState:   "known",
				aheadDate:         &aheadDate,
				aheadState:        "known",
				acceptedDate:      &acceptedDate,
				acceptedState:     "known",
				publicationModel:  stringPointer("Print-Electronic"),
				publicationStatus: stringPointer("epublish"),
				events: []publisherPublicationEventFixture{
					{
						kind:       "accepted",
						date:       &acceptedDate,
						precision:  "day",
						statusRaw:  "accepted",
						modelRaw:   stringPointer("Print-Electronic"),
						sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
						ordinal:    1,
					},
					{
						kind:       "accepted",
						date:       &acceptedDate,
						precision:  "day",
						statusRaw:  "accepted",
						modelRaw:   stringPointer("Print-Electronic"),
						sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[2]",
						ordinal:    2,
					},
					{
						kind:       "ahead_of_print",
						date:       &aheadDate,
						precision:  "day",
						statusRaw:  "aheadofprint",
						modelRaw:   stringPointer("Print-Electronic"),
						sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[3]",
						ordinal:    3,
					},
					{
						kind:       "print_published",
						date:       &printDate,
						precision:  "day",
						statusRaw:  "ppublish",
						modelRaw:   stringPointer("Print-Electronic"),
						sourcePath: "/PubmedArticle/MedlineCitation/Article/Journal/JournalIssue/PubDate",
						ordinal:    4,
					},
					{
						kind:       "electronic_published",
						date:       &electronicDate,
						precision:  "day",
						statusRaw:  "epublish",
						modelRaw:   stringPointer("Print-Electronic"),
						sourcePath: "/PubmedArticle/MedlineCitation/Article/ArticleDate[1]",
						ordinal:    5,
					},
				},
			},
		)

		generation, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		)
		if err != nil {
			t.Fatalf("PublishCurrent() error = %v", err)
		}
		repository := mustRepository(t, pool)
		home, err := repository.Home(context.Background())
		if err != nil {
			t.Fatalf("Home() error = %v", err)
		}
		if home.Generation.ID != generation.ID {
			t.Fatalf("Home() generation = %s, want %s", home.Generation.ID, generation.ID)
		}
		assertNestedJSONValue(
			t,
			home.Payload,
			[]string{"publication_updates", "calendar_date"},
			"2026-07-18",
		)
		assertNestedJSONValue(
			t,
			home.Payload,
			[]string{"publication_updates", "calendar_timezone"},
			"UTC",
		)

		formal := publicationUpdateCollection(t, home.Payload, "formal_publications_today")
		accepted := publicationUpdateCollection(t, home.Payload, "recent_acceptances")
		ahead := publicationUpdateCollection(t, home.Payload, "recent_online_first")
		assertPublicationUpdateCollectionMetadata(t, formal, 1, 2, 1)
		assertPublicationUpdateCollectionMetadata(t, accepted, 7, 1, 1)
		assertPublicationUpdateCollectionMetadata(t, ahead, 7, 1, 1)

		formalItems := formal["items"].([]any)
		assertPublicationUpdateEvent(
			t,
			formalItems[0],
			fixture,
			projectionAssertionID,
			"electronic_published",
			"2026-07-18",
			"epublish",
			"/PubmedArticle/MedlineCitation/Article/ArticleDate[1]",
		)
		assertPublicationUpdateEvent(
			t,
			formalItems[1],
			fixture,
			projectionAssertionID,
			"print_published",
			"2026-07-18",
			"ppublish",
			"/PubmedArticle/MedlineCitation/Article/Journal/JournalIssue/PubDate",
		)
		assertPublicationUpdateEvent(
			t,
			accepted["items"].([]any)[0],
			fixture,
			projectionAssertionID,
			"accepted",
			"2026-07-12",
			"accepted",
			"/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
		)
		assertPublicationUpdateEvent(
			t,
			ahead["items"].([]any)[0],
			fixture,
			projectionAssertionID,
			"ahead_of_print",
			"2026-07-15",
			"aheadofprint",
			"/PubmedArticle/PubmedData/History/PubMedPubDate[3]",
		)

		paper, err := repository.Paper(context.Background(), fixture.workID)
		if err != nil {
			t.Fatalf("Paper() error = %v", err)
		}
		var frozenPaper any
		if err := json.Unmarshal(paper.Payload, &frozenPaper); err != nil {
			t.Fatalf("decode frozen Paper payload: %v", err)
		}
		for _, collection := range []map[string]any{formal, accepted, ahead} {
			for _, rawItem := range collection["items"].([]any) {
				item := rawItem.(map[string]any)
				if !reflect.DeepEqual(item["paper"], frozenPaper) {
					t.Fatalf(
						"publication update paper = %#v, want frozen Paper summary %#v",
						item["paper"],
						frozenPaper,
					)
				}
			}
		}

		var stored []byte
		if err := pool.QueryRow(context.Background(), `
			SELECT payload
			FROM public_catalog_home
			WHERE generation_id = $1
		`, generation.ID).Scan(&stored); err != nil {
			t.Fatalf("query stored Home payload: %v", err)
		}
		if !bytes.Equal(home.Payload, stored) {
			t.Fatalf("Home() payload differs from stored generation payload")
		}
		second, err := repository.Home(context.Background())
		if err != nil {
			t.Fatalf("Home() second read error = %v", err)
		}
		if second.Generation.ID != generation.ID ||
			!bytes.Equal(second.Payload, home.Payload) {
			t.Fatalf("Home() payload changed within generation %s", generation.ID)
		}
	})

	t.Run("excludes non-day non-current and ineligible events", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		input := catalogCurationInputAt(
			time.Date(2026, time.July, 18, 23, 30, 0, 0, time.UTC),
		)
		eligibleHistory := []publisherPublicationHistoryFixture{
			publisherPublicationHistoryEntry(
				"ppublish", 2026, time.July, 17, "day",
				"/PubmedArticle/JournalIssue/PubDate[1]", 1,
			),
			publisherPublicationHistoryEntry(
				"ppublish", 2026, time.July, 18, "day",
				"/PubmedArticle/JournalIssue/PubDate[2]", 2,
			),
			publisherPublicationHistoryEntry(
				"epublish", 2026, time.July, 0, "month",
				"/PubmedArticle/ArticleDate[1]", 3,
			),
			publisherPublicationHistoryEntry(
				"aheadofprint", 2026, time.July, 11, "day",
				"/PubmedArticle/PubMedPubDate[1]", 4,
			),
			publisherPublicationHistoryEntry(
				"accepted", 2026, time.July, 19, "day",
				"/PubmedArticle/PubMedPubDate[2]", 5,
			),
		}
		ineligibleHistory := []publisherPublicationHistoryFixture{
			publisherPublicationHistoryEntry(
				"ppublish", 2026, time.July, 18, "day",
				"/PubmedArticle/JournalIssue/PubDate", 1,
			),
		}
		eligible := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"eligible-exclusions",
			"doi:10.1000/eligible-exclusions",
			eligibleHistory...,
		)
		rejectedJCR := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"rejected-jcr",
			"doi:10.1000/rejected-jcr",
			ineligibleHistory...,
		)
		nonBiomedical := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"non-biomedical",
			"doi:10.1000/non-biomedical",
			ineligibleHistory...,
		)
		retracted := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"retracted",
			"doi:10.1000/retracted",
			ineligibleHistory...,
		)
		excluded := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"excluded",
			"doi:10.1000/excluded",
			ineligibleHistory...,
		)
		if _, err := pool.Exec(context.Background(), `
			UPDATE works SET status = 'retracted' WHERE id = $1
		`, retracted.workID); err != nil {
			t.Fatalf("mark retracted Work: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `
			UPDATE ingestion_source_states
			SET scope_status = 'excluded'
			WHERE work_id = $1
		`, excluded.workID); err != nil {
			t.Fatalf("mark excluded source state: %v", err)
		}
		for index, fixture := range []publisherWorkFixture{
			rejectedJCR,
			nonBiomedical,
			retracted,
			excluded,
		} {
			venueID := catalogWorkVenueID(t, pool, fixture.workID)
			if _, err := pool.Exec(context.Background(), `
				UPDATE venues
				SET issn_l = $2
				WHERE id = $1
			`, venueID, deterministicCatalogISSN(1200+index)); err != nil {
				t.Fatalf("prepare ineligible publication update Venue: %v", err)
			}
		}

		preparePublisherAcceptedCuration(t, pool, input, eligible)
		insertPublisherBiomedicalProjection(t, pool, eligible)
		ineligibleProjectionIDs := make(map[uuid.UUID]uuid.UUID)
		for index, fixture := range []publisherWorkFixture{
			rejectedJCR,
			nonBiomedical,
			retracted,
			excluded,
		} {
			ineligibleProjectionIDs[fixture.workID] =
				insertPublisherAlternateProjectionAssertion(
					t,
					pool,
					fixture,
					fmt.Sprintf("projection/ineligible-publication-%d", index+1),
				)
		}

		futureAccepted := publicationUpdateDate(2026, time.July, 19)
		oldAhead := publicationUpdateDate(2026, time.July, 11)
		firstConflict := publicationUpdateDate(2026, time.July, 17)
		secondConflict := publicationUpdateDate(2026, time.July, 18)
		insertPublisherPublicationState(
			t,
			pool,
			eligible,
			publisherPublicationStateFixture{
				printState:        "conflict",
				electronicState:   "missing",
				aheadDate:         &oldAhead,
				aheadState:        "known",
				acceptedDate:      &futureAccepted,
				acceptedState:     "known",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
				events: []publisherPublicationEventFixture{
					{
						kind:       "print_published",
						date:       &firstConflict,
						precision:  "day",
						statusRaw:  "ppublish",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/JournalIssue/PubDate[1]",
						ordinal:    1,
					},
					{
						kind:       "print_published",
						date:       &secondConflict,
						precision:  "day",
						statusRaw:  "ppublish",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/JournalIssue/PubDate[2]",
						ordinal:    2,
					},
					{
						kind:       "electronic_published",
						precision:  "month",
						sourceDate: `{"year":2026,"month":7,"precision":"month"}`,
						statusRaw:  "epublish",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/ArticleDate[1]",
						ordinal:    3,
					},
					{
						kind:       "ahead_of_print",
						date:       &oldAhead,
						precision:  "day",
						statusRaw:  "aheadofprint",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/PubMedPubDate[1]",
						ordinal:    4,
					},
					{
						kind:       "accepted",
						date:       &futureAccepted,
						precision:  "day",
						statusRaw:  "accepted",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/PubMedPubDate[2]",
						ordinal:    5,
					},
				},
			},
		)
		today := publicationUpdateDate(2026, time.July, 18)
		for _, fixture := range []publisherWorkFixture{
			rejectedJCR,
			nonBiomedical,
			retracted,
			excluded,
		} {
			insertPublisherPublicationState(
				t,
				pool,
				fixture,
				publisherPublicationStateFixture{
					projectionAssertionID: ineligibleProjectionIDs[fixture.workID],
					printDate:             &today,
					printState:            "known",
					electronicState:       "missing",
					aheadState:            "missing",
					acceptedState:         "missing",
					events: []publisherPublicationEventFixture{{
						kind:       "print_published",
						date:       &today,
						precision:  "day",
						statusRaw:  "ppublish",
						sourcePath: "/PubmedArticle/JournalIssue/PubDate",
						ordinal:    1,
					}},
				},
			)
		}

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent() error = %v", err)
		}
		home, err := mustRepository(t, pool).Home(context.Background())
		if err != nil {
			t.Fatalf("Home() error = %v", err)
		}
		formal := publicationUpdateCollection(t, home.Payload, "formal_publications_today")
		accepted := publicationUpdateCollection(t, home.Payload, "recent_acceptances")
		ahead := publicationUpdateCollection(t, home.Payload, "recent_online_first")
		assertPublicationUpdateCollectionMetadata(t, formal, 1, 0, 0)
		assertPublicationUpdateCollectionMetadata(t, accepted, 7, 0, 1)
		assertPublicationUpdateCollectionMetadata(t, ahead, 7, 0, 1)
	})

	t.Run("sorts real collection items by date canonical key and event kind", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		input := catalogCurationInputAt(
			time.Date(2026, time.July, 18, 23, 30, 0, 0, time.UTC),
		)
		historyA := []publisherPublicationHistoryFixture{
			publisherPublicationHistoryEntry(
				"ppublish", 2026, time.July, 18, "day",
				"/PubmedArticle/SortingA/Print", 1,
			),
			publisherPublicationHistoryEntry(
				"epublish", 2026, time.July, 18, "day",
				"/PubmedArticle/SortingA/Electronic", 2,
			),
			publisherPublicationHistoryEntry(
				"accepted", 2026, time.July, 16, "day",
				"/PubmedArticle/SortingA/Accepted", 3,
			),
		}
		historyB := []publisherPublicationHistoryFixture{
			publisherPublicationHistoryEntry(
				"ppublish", 2026, time.July, 18, "day",
				"/PubmedArticle/SortingB/Print", 1,
			),
			publisherPublicationHistoryEntry(
				"accepted", 2026, time.July, 16, "day",
				"/PubmedArticle/SortingB/Accepted", 2,
			),
		}
		historyC := []publisherPublicationHistoryFixture{
			publisherPublicationHistoryEntry(
				"ppublish", 2026, time.July, 18, "day",
				"/PubmedArticle/SortingC/Print", 1,
			),
			publisherPublicationHistoryEntry(
				"accepted", 2026, time.July, 17, "day",
				"/PubmedArticle/SortingC/Accepted", 2,
			),
		}
		fixtureA := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"sorting-a",
			"doi:10.1000/publication-sorting-a",
			historyA...,
		)
		fixtureB := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"sorting-b",
			"doi:10.1000/publication-sorting-b",
			historyB...,
		)
		fixtureC := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"sorting-c",
			"doi:10.1000/publication-sorting-c",
			historyC...,
		)
		fixtures := []publisherWorkFixture{fixtureA, fixtureB, fixtureC}
		preparePublisherAcceptedCuration(t, pool, input, fixtures...)
		for _, fixture := range fixtures {
			insertPublisherBiomedicalProjection(t, pool, fixture)
		}

		today := publicationUpdateDate(2026, time.July, 18)
		olderAcceptance := publicationUpdateDate(2026, time.July, 16)
		newerAcceptance := publicationUpdateDate(2026, time.July, 17)
		insertPublisherPublicationState(
			t,
			pool,
			fixtureA,
			publisherPublicationStateFixture{
				printDate:         &today,
				printState:        "known",
				electronicDate:    &today,
				electronicState:   "known",
				aheadState:        "missing",
				acceptedDate:      &olderAcceptance,
				acceptedState:     "known",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
				events: []publisherPublicationEventFixture{
					{
						kind:       "print_published",
						date:       &today,
						precision:  "day",
						statusRaw:  "ppublish",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/SortingA/Print",
						ordinal:    1,
					},
					{
						kind:       "electronic_published",
						date:       &today,
						precision:  "day",
						statusRaw:  "epublish",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/SortingA/Electronic",
						ordinal:    2,
					},
					{
						kind:       "accepted",
						date:       &olderAcceptance,
						precision:  "day",
						statusRaw:  "accepted",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/SortingA/Accepted",
						ordinal:    3,
					},
				},
			},
		)
		insertPublisherPublicationState(
			t,
			pool,
			fixtureB,
			publisherPublicationStateFixture{
				printDate:         &today,
				printState:        "known",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedDate:      &olderAcceptance,
				acceptedState:     "known",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
				events: []publisherPublicationEventFixture{
					{
						kind:       "print_published",
						date:       &today,
						precision:  "day",
						statusRaw:  "ppublish",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/SortingB/Print",
						ordinal:    1,
					},
					{
						kind:       "accepted",
						date:       &olderAcceptance,
						precision:  "day",
						statusRaw:  "accepted",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/SortingB/Accepted",
						ordinal:    2,
					},
				},
			},
		)
		insertPublisherPublicationState(
			t,
			pool,
			fixtureC,
			publisherPublicationStateFixture{
				printDate:         &today,
				printState:        "known",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedDate:      &newerAcceptance,
				acceptedState:     "known",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
				events: []publisherPublicationEventFixture{
					{
						kind:       "print_published",
						date:       &today,
						precision:  "day",
						statusRaw:  "ppublish",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/SortingC/Print",
						ordinal:    1,
					},
					{
						kind:       "accepted",
						date:       &newerAcceptance,
						precision:  "day",
						statusRaw:  "accepted",
						modelRaw:   stringPointer("Electronic"),
						sourcePath: "/PubmedArticle/SortingC/Accepted",
						ordinal:    2,
					},
				},
			},
		)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent() error = %v", err)
		}
		home, err := mustRepository(t, pool).Home(context.Background())
		if err != nil {
			t.Fatalf("Home() error = %v", err)
		}
		assertPublicationUpdateItemOrder(
			t,
			publicationUpdateCollection(
				t,
				home.Payload,
				"recent_acceptances",
			)["items"].([]any),
			[]string{
				"2026-07-17|doi:10.1000/publication-sorting-c|accepted",
				"2026-07-16|doi:10.1000/publication-sorting-a|accepted",
				"2026-07-16|doi:10.1000/publication-sorting-b|accepted",
			},
		)
		assertPublicationUpdateItemOrder(
			t,
			publicationUpdateCollection(
				t,
				home.Payload,
				"formal_publications_today",
			)["items"].([]any),
			[]string{
				"2026-07-18|doi:10.1000/publication-sorting-a|electronic_published",
				"2026-07-18|doi:10.1000/publication-sorting-a|print_published",
				"2026-07-18|doi:10.1000/publication-sorting-b|print_published",
				"2026-07-18|doi:10.1000/publication-sorting-c|print_published",
			},
		)
	})

	t.Run("keeps old v2 work publishable with empty updates", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		input := catalogCurationInput()
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "pubmed:catalog-old-v2-no-publication-state",
			logicalSource:     "pubmed",
			canonicalKey:      "doi:10.1000/catalog-old-v2-no-publication-state",
			title:             "Old v2 publication without projected event state",
			publishedAt:       time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			sourceTime:        time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
			noJCRAssessment:   true,
		})
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherBiomedicalProjection(t, pool, fixture)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent() old v2 error = %v", err)
		}
		home, err := mustRepository(t, pool).Home(context.Background())
		if err != nil {
			t.Fatalf("Home() error = %v", err)
		}
		for _, collectionName := range []string{
			"formal_publications_today",
			"recent_acceptances",
			"recent_online_first",
		} {
			collection := publicationUpdateCollection(t, home.Payload, collectionName)
			windowDays := 7
			if collectionName == "formal_publications_today" {
				windowDays = 1
			}
			assertPublicationUpdateCollectionMetadata(t, collection, windowDays, 0, 0)
		}
	})

	t.Run("preserves missing publication metadata as null", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		input := catalogCurationInput()
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:                   "pubmed:catalog-publication-metadata-missing",
			logicalSource:              "pubmed",
			canonicalKey:               "doi:10.1000/catalog-publication-metadata-missing",
			title:                      "Publication metadata missing",
			publishedAt:                time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			sourceTime:                 time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
			scopeStatus:                "included",
			includeWorkLink:            true,
			includeWorkID:              true,
			includeNormalized:          true,
			normalizedPayloadSchema:    "normalized-record/v3",
			includePublicationMetadata: true,
			publicationHistory: []publisherPublicationHistoryFixture{
				publisherPublicationHistoryEntry(
					"ppublish", 2026, time.July, 17, "day",
					"/PubmedArticle/JournalIssue/PubDate", 1,
				),
			},
			noJCRAssessment: true,
		})
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherBiomedicalProjection(t, pool, fixture)
		printDate := publicationUpdateDate(2026, time.July, 17)
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printDate:       &printDate,
				printState:      "known",
				electronicState: "missing",
				aheadState:      "missing",
				acceptedState:   "missing",
				events: []publisherPublicationEventFixture{{
					kind:       "print_published",
					date:       &printDate,
					precision:  "day",
					statusRaw:  "ppublish",
					sourcePath: "/PubmedArticle/JournalIssue/PubDate",
					ordinal:    1,
				}},
			},
		)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent() missing publication metadata error = %v", err)
		}
		home, err := mustRepository(t, pool).Home(context.Background())
		if err != nil {
			t.Fatalf("Home() error = %v", err)
		}
		formal := publicationUpdateCollection(t, home.Payload, "formal_publications_today")
		event := formal["items"].([]any)[0].(map[string]any)["event"].(map[string]any)
		if event["publication_status"] != "ppublish" ||
			event["publication_model"] != nil {
			t.Fatalf(
				"missing publication metadata event = %#v, want assertion status and null model",
				event,
			)
		}
	})
}

func TestPublisherPublicationUpdatesRespectSingleEligibilityGates(t *testing.T) {
	pool := openCatalogTestPool(t)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 18, 23, 30, 0, 0, time.UTC),
	)
	specs := []struct {
		name        string
		canonical   string
		jcrDecision string
		withSubject bool
	}{
		{
			name:        "accepted-control",
			canonical:   "doi:10.1000/publication-gate-accepted",
			jcrDecision: "accepted",
			withSubject: true,
		},
		{
			name:        "rejected-jcr",
			canonical:   "doi:10.1000/publication-gate-rejected-jcr",
			jcrDecision: "rejected",
			withSubject: true,
		},
		{
			name:        "non-biomedical",
			canonical:   "doi:10.1000/publication-gate-non-biomedical",
			jcrDecision: "accepted",
			withSubject: false,
		},
		{
			name:        "retracted",
			canonical:   "doi:10.1000/publication-gate-retracted",
			jcrDecision: "accepted",
			withSubject: true,
		},
		{
			name:        "excluded",
			canonical:   "doi:10.1000/publication-gate-excluded",
			jcrDecision: "accepted",
			withSubject: true,
		},
	}

	fixtures := make(map[string]publisherWorkFixture, len(specs))
	for _, spec := range specs {
		fixture := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"single-gate-"+spec.name,
			spec.canonical,
			publisherPublicationHistoryEntry(
				"ppublish", 2026, time.July, 18, "day",
				"/PubmedArticle/SingleGate/JournalIssue/PubDate", 1,
			),
		)
		fixtures[spec.name] = fixture
		insertPublisherBiomedicalProjection(t, pool, fixture)
		today := publicationUpdateDate(2026, time.July, 18)
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printDate:         &today,
				printState:        "known",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
				events: []publisherPublicationEventFixture{{
					kind:       "print_published",
					date:       &today,
					precision:  "day",
					statusRaw:  "ppublish",
					modelRaw:   stringPointer("Electronic"),
					sourcePath: "/PubmedArticle/SingleGate/JournalIssue/PubDate",
					ordinal:    1,
				}},
			},
		)
	}

	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	jcrFixtures := make([]catalogJCRVenueFixture, 0, len(specs))
	for index, spec := range specs {
		fixture := fixtures[spec.name]
		venueID := catalogWorkVenueID(t, pool, fixture.workID)
		if _, err := pool.Exec(context.Background(), `
			UPDATE venues
			SET venue_type = 'journal',
			    issn_l = $2
			WHERE id = $1
		`, venueID, deterministicCatalogISSN(1400+index)); err != nil {
			t.Fatalf("prepare %s Venue identity: %v", spec.name, err)
		}
		jcrFixtures = append(jcrFixtures, catalogJCRVenueFixture{
			venueID:     venueID,
			index:       1400 + index,
			withSubject: spec.withSubject,
		})
	}
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, jcrFixtures)
	for _, spec := range specs {
		fixture := fixtures[spec.name]
		insertCatalogVenueAssessment(
			t,
			pool,
			input,
			catalogWorkVenueID(t, pool, fixture.workID),
			input.VenuePolicyName,
			input.VenuePolicyVersion,
			input.JCRMetricYear,
			spec.jcrDecision,
		)
		wantEligibility := biomed.PublicEligibilityDecisionAccepted
		if spec.name == "non-biomedical" {
			wantEligibility = biomed.PublicEligibilityDecisionRejected
		}
		assertCatalogBiomedicalEligibilityDecision(
			t,
			pool,
			fixture.workID,
			input,
			wantEligibility,
		)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE works SET status = 'retracted' WHERE id = $1
	`, fixtures["retracted"].workID); err != nil {
		t.Fatalf("mark single-gate Work retracted: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE ingestion_source_states
		SET scope_status = 'excluded'
		WHERE work_id = $1
	`, fixtures["excluded"].workID); err != nil {
		t.Fatalf("mark single-gate source excluded: %v", err)
	}

	control := fixtures["accepted-control"]
	insertCatalogCitationAnalysisRun(t, pool, input, control.workID)
	controlCohortRevision := catalogBiomedicalCohortRevisionForFixtures(
		t,
		pool,
		input,
		[]publisherWorkFixture{control},
	)
	insertCatalogBiomedicalPublicationFixtures(
		t,
		pool,
		input,
		controlCohortRevision,
		controlCohortRevision,
		input.TrendAnalysisRunID,
		control,
	)

	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	home, err := mustRepository(t, pool).Home(context.Background())
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	formal := publicationUpdateCollection(t, home.Payload, "formal_publications_today")
	assertPublicationUpdateCollectionMetadata(t, formal, 1, 1, 1)
	items := formal["items"].([]any)
	assertPublicationUpdateItemOrder(
		t,
		items,
		[]string{
			"2026-07-18|doi:10.1000/publication-gate-accepted|print_published",
		},
	)
	for _, name := range []string{
		"rejected-jcr",
		"non-biomedical",
		"retracted",
		"excluded",
	} {
		if publicationUpdateItemsContainWork(items, fixtures[name].workID) {
			t.Fatalf("%s Work unexpectedly entered publication updates", name)
		}
	}
}

func TestPublisherPublishesExactBiomedicalAnalysisSnapshots(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:catalog-biomedical-analysis-publication",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/catalog-biomedical-analysis-publication",
		title:             "Catalog biomedical analysis publication",
		methodNames:       []string{"Causal Inference"},
		paperType:         "research_article",
		publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	cohortRevision := preparePublisherAcceptedCurationWithoutBiomedicalRuns(
		t,
		pool,
		input,
		fixture,
	)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)
	insertCatalogBiomedicalPublicationFixtures(
		t,
		pool,
		input,
		cohortRevision,
		cohortRevision,
		input.TrendAnalysisRunID,
		fixture,
	)

	generation, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}

	home, err := mustRepository(t, pool).Home(context.Background())
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"subject_momentum", "analysis", "analysis_run_id"},
		input.TrendAnalysisRunID.String(),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"subject_momentum", "analysis", "cohort_revision"},
		cohortRevision,
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"subject_momentum", "items", "0", "estimate", "value"},
		float64(7.25),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"entity_momentum", "items", "0", "label"},
		"Causal Inference",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"research_opportunities", "analysis", "analysis_run_id"},
		input.OpportunityAnalysisRunID.String(),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"research_opportunities", "analysis", "window_days", "state"},
		"missing",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"research_opportunities", "items", "0", "estimates", "0", "value"},
		float64(0.81),
	)

	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	journal, err := mustRepository(t, pool).Journal(
		context.Background(),
		"journal-"+strings.ReplaceAll(venueID.String(), "-", ""),
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Journal() error = %v", err)
	}
	assertNestedJSONValue(
		t,
		journal.Payload,
		[]string{"editorial_patterns", "analysis", "analysis_run_id"},
		input.JournalAnalysisRunID.String(),
	)
	assertNestedJSONValue(
		t,
		journal.Payload,
		[]string{"editorial_patterns", "items", "0", "estimate", "value"},
		float64(4.5),
	)

	opportunities, err := mustRepository(t, pool).ResearchOpportunities(
		context.Background(),
		OpportunityQuery{Status: "insufficient_evidence", Limit: 20},
	)
	if err != nil {
		t.Fatalf("ResearchOpportunities() error = %v", err)
	}
	if len(opportunities.Items) != 1 {
		t.Fatalf(
			"ResearchOpportunities() item count = %d, want 1",
			len(opportunities.Items),
		)
	}
	assertNestedJSONValue(
		t,
		opportunities.Items[0],
		[]string{"analysis_run_id"},
		input.OpportunityAnalysisRunID.String(),
	)
	assertNestedJSONValue(
		t,
		opportunities.Items[0],
		[]string{"supporting_work_ids", "0"},
		fixture.workID.String(),
	)

	var metadata []byte
	if err := pool.QueryRow(context.Background(), `
		SELECT metadata
		FROM public_catalog_generations
		WHERE id = $1
	`, generation.ID).Scan(&metadata); err != nil {
		t.Fatalf("query published generation metadata: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatalf("decode published generation metadata: %v", err)
	}
	for key, want := range map[string]any{
		"trend_analysis_run_id":       input.TrendAnalysisRunID.String(),
		"journal_analysis_run_id":     input.JournalAnalysisRunID.String(),
		"opportunity_analysis_run_id": input.OpportunityAnalysisRunID.String(),
		"opportunities":               float64(1),
	} {
		if decoded[key] != want {
			t.Fatalf("generation metadata[%q] = %#v, want %#v", key, decoded[key], want)
		}
	}
}

func insertPublisherPublicationUpdateWork(
	t *testing.T,
	pool *pgxpool.Pool,
	name string,
	canonicalKey string,
	publicationHistory ...publisherPublicationHistoryFixture,
) publisherWorkFixture {
	t.Helper()
	return insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                "pubmed:catalog-publication-update-" + name,
		logicalSource:           "pubmed",
		canonicalKey:            canonicalKey,
		title:                   "Publication update " + name,
		paperType:               "research_article",
		publishedAt:             time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		sourceTime:              time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC),
		scopeStatus:             "included",
		includeWorkLink:         true,
		includeWorkID:           true,
		includeNormalized:       true,
		normalizedPayloadSchema: "normalized-record/v3",
		publicationModel:        "Electronic",
		publicationStatus:       "epublish",
		publicationHistory:      publicationHistory,
		noJCRAssessment:         true,
	})
}

func publicationUpdateCollection(
	t *testing.T,
	payload json.RawMessage,
	name string,
) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode Home payload: %v", err)
	}
	updates, ok := document["publication_updates"].(map[string]any)
	if !ok {
		t.Fatalf("publication_updates = %#v, want object", document["publication_updates"])
	}
	collection, ok := updates[name].(map[string]any)
	if !ok {
		t.Fatalf("publication_updates.%s = %#v, want object", name, updates[name])
	}
	if _, ok := collection["items"].([]any); !ok {
		t.Fatalf("publication_updates.%s.items = %#v, want array", name, collection["items"])
	}
	return collection
}

func assertPublicationUpdateCollectionMetadata(
	t *testing.T,
	collection map[string]any,
	windowDays int,
	itemCount int,
	knownStateCount int,
) {
	t.Helper()
	analysis, ok := collection["analysis"].(map[string]any)
	if !ok {
		t.Fatalf("publication update analysis = %#v, want object", collection["analysis"])
	}
	assertDecodedCatalogValue(t, analysis["window_days"], float64(windowDays))
	assertDecodedCatalogValue(t, analysis["sample_size"], float64(itemCount))
	assertDecodedCatalogValue(t, analysis["coverage_ratio"], float64(knownStateCount))
	assertDecodedCatalogValue(t, analysis["sources"], []any{"pubmed"})

	items := collection["items"].([]any)
	if len(items) != itemCount {
		t.Fatalf("publication update item count = %d, want %d", len(items), itemCount)
	}
	pagination, ok := collection["pagination"].(map[string]any)
	if !ok {
		t.Fatalf("publication update pagination = %#v, want object", collection["pagination"])
	}
	for key, want := range map[string]any{
		"has_more":    false,
		"limit":       float64(itemCount),
		"next_cursor": nil,
		"total":       float64(itemCount),
	} {
		if !reflect.DeepEqual(pagination[key], want) {
			t.Fatalf("publication update pagination[%q] = %#v, want %#v", key, pagination[key], want)
		}
	}
}

func assertDecodedCatalogValue(t *testing.T, raw any, want any) {
	t.Helper()
	value, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("catalog value = %#v, want object", raw)
	}
	if value["state"] != "known" || !reflect.DeepEqual(value["value"], want) {
		t.Fatalf("catalog value = %#v, want known %#v", value, want)
	}
}

func assertPublicationUpdateEvent(
	t *testing.T,
	raw any,
	fixture publisherWorkFixture,
	projectionAssertionID uuid.UUID,
	kind string,
	date string,
	statusRaw string,
	sourcePath string,
) {
	t.Helper()
	item, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("publication update item = %#v, want object", raw)
	}
	event, ok := item["event"].(map[string]any)
	if !ok {
		t.Fatalf("publication update event = %#v, want object", item["event"])
	}
	for field, want := range map[string]any{
		"kind":               kind,
		"date":               date,
		"date_precision":     "day",
		"publication_status": statusRaw,
		"publication_model":  "Print-Electronic",
	} {
		if event[field] != want {
			t.Fatalf("publication update event[%q] = %#v, want %#v", field, event[field], want)
		}
	}
	provenance, ok := event["provenance"].(map[string]any)
	if !ok {
		t.Fatalf("publication update provenance = %#v, want object", event["provenance"])
	}
	for field, want := range map[string]any{
		"source":                  "pubmed",
		"source_record_id":        fixture.sourceRecord.String(),
		"normalized_assertion_id": fixture.normalizedAssertionID.String(),
		"projection_assertion_id": projectionAssertionID.String(),
		"source_path":             sourcePath,
		"status_raw":              statusRaw,
	} {
		if provenance[field] != want {
			t.Fatalf("publication update provenance[%q] = %#v, want %#v", field, provenance[field], want)
		}
	}
}

func assertPublicationUpdateItemOrder(
	t *testing.T,
	items []any,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			t.Fatalf("publication update item = %#v, want object", rawItem)
		}
		paper, ok := item["paper"].(map[string]any)
		if !ok {
			t.Fatalf("publication update paper = %#v, want object", item["paper"])
		}
		event, ok := item["event"].(map[string]any)
		if !ok {
			t.Fatalf("publication update event = %#v, want object", item["event"])
		}
		got = append(
			got,
			fmt.Sprintf(
				"%s|%s|%s",
				event["date"],
				paper["canonical_key"],
				event["kind"],
			),
		)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("publication update order = %#v, want %#v", got, want)
	}
}

func publicationUpdateItemsContainWork(items []any, workID uuid.UUID) bool {
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		paper, ok := item["paper"].(map[string]any)
		if ok && paper["id"] == workID.String() {
			return true
		}
	}
	return false
}

func assertCatalogBiomedicalEligibilityDecision(
	t *testing.T,
	pool *pgxpool.Pool,
	workID uuid.UUID,
	input PublishInput,
	want biomed.PublicEligibilityDecision,
) {
	t.Helper()
	store, err := biomed.NewPostgresPublicEligibilityStore(pool)
	if err != nil {
		t.Fatalf("create Catalog biomedical eligibility store: %v", err)
	}
	service, err := biomed.NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("create Catalog biomedical eligibility service: %v", err)
	}
	assessment, err := service.Assess(
		context.Background(),
		biomed.PublicEligibilityInput{
			WorkID:            workID.String(),
			PolicyVersion:     biomed.BiomedicalPublicEligibilityPolicyVersion,
			MetricYear:        input.JCRMetricYear,
			SubjectVersionKey: input.SubjectVersion,
			AssessedAt:        input.GeneratedAt.Add(-30 * time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("assess Catalog biomedical eligibility: %v", err)
	}
	if assessment.Decision != want {
		t.Fatalf(
			"Catalog biomedical eligibility decision = %q, want %q",
			assessment.Decision,
			want,
		)
	}
}

func publicationUpdateDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func stringPointer(value string) *string {
	return &value
}

func TestPublisherRejectsBiomedicalSnapshotOutsideValidatedCohort(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:catalog-biomedical-analysis-cohort-mismatch",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/catalog-biomedical-analysis-cohort-mismatch",
		title:             "Catalog biomedical analysis cohort mismatch",
		publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	cohortRevision := preparePublisherAcceptedCurationWithoutBiomedicalRuns(
		t,
		pool,
		input,
		fixture,
	)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)
	insertCatalogBiomedicalPublicationFixtures(
		t,
		pool,
		input,
		cohortRevision,
		strings.Repeat("f", 64),
		input.TrendAnalysisRunID,
		fixture,
	)

	_, err := mustPublisher(t, pool).PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "cohort revision") {
		t.Fatalf(
			"PublishCurrent(snapshot cohort mismatch) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestSnapshotRevisionIncludesOpportunityRows(t *testing.T) {
	input := catalogCurationInput()
	firstOpportunityID := uuid.MustParse(
		"00000000-0000-0000-0000-000000000801",
	)
	secondOpportunityID := uuid.MustParse(
		"00000000-0000-0000-0000-000000000802",
	)
	first := catalogSnapshot{
		opportunities: []publishedResearchOpportunity{{
			ID:      firstOpportunityID,
			Status:  "insufficient_evidence",
			Ordinal: 1,
			Payload: json.RawMessage(`{"id":"00000000-0000-0000-0000-000000000801"}`),
		}},
	}
	second := first
	second.opportunities = []publishedResearchOpportunity{{
		ID:      secondOpportunityID,
		Status:  "insufficient_evidence",
		Ordinal: 1,
		Payload: json.RawMessage(`{"id":"00000000-0000-0000-0000-000000000802"}`),
	}}

	firstRevision, err := snapshotRevision(input, first)
	if err != nil {
		t.Fatalf("snapshotRevision(first) error = %v", err)
	}
	secondRevision, err := snapshotRevision(input, second)
	if err != nil {
		t.Fatalf("snapshotRevision(second) error = %v", err)
	}
	if firstRevision == secondRevision {
		t.Fatalf(
			"snapshot revisions are equal for distinct opportunity rows: %s",
			firstRevision,
		)
	}
}

func preparePublisherAcceptedCurationWithoutBiomedicalRuns(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	fixtures ...publisherWorkFixture,
) string {
	t.Helper()
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	venueIDs := make([]uuid.UUID, 0, len(fixtures))
	jcrFixtures := make([]catalogJCRVenueFixture, 0, len(fixtures))
	for index, fixture := range fixtures {
		venueID := catalogWorkVenueID(t, pool, fixture.workID)
		if _, err := pool.Exec(context.Background(), `
			UPDATE venues
			SET venue_type = 'journal',
			    issn_l = $2
			WHERE id = $1
		`, venueID, deterministicCatalogISSN(index+900)); err != nil {
			t.Fatalf("prepare biomedical publication Journal identity: %v", err)
		}
		venueIDs = append(venueIDs, venueID)
		jcrFixtures = append(jcrFixtures, catalogJCRVenueFixture{
			venueID:     venueID,
			index:       index,
			withSubject: true,
		})
	}
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, jcrFixtures)
	assessCatalogVenuePolicy(t, pool, input)
	for _, fixture := range fixtures {
		assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	}
	return catalogBiomedicalCohortRevisionForFixtures(t, pool, input, fixtures)
}

func insertCatalogBiomedicalPublicationFixtures(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	runCohortRevision string,
	snapshotCohortRevision string,
	opportunityTrendRunID uuid.UUID,
	fixture publisherWorkFixture,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin biomedical publication fixtures: %v", err)
	}
	defer tx.Rollback(context.Background())

	var (
		subjectVersionID uuid.UUID
		subjectID        uuid.UUID
		subjectSlug      string
		venueID          uuid.UUID
	)
	if err := tx.QueryRow(ctx, `
		SELECT
			eligibility.subject_version_id,
			rule.subject_id,
			subject.slug,
			eligibility.venue_id
		FROM biomedical_publication_eligibility_decisions AS eligibility
		JOIN journal_subject_metrics AS link
		  ON link.id = eligibility.journal_subject_metric_id
		JOIN biomedical_subject_rules AS rule
		  ON rule.id = link.subject_rule_id
		JOIN subjects AS subject
		  ON subject.id = rule.subject_id
		 AND subject.subject_version_id = eligibility.subject_version_id
		WHERE eligibility.work_id = $1
		  AND eligibility.decision = 'accepted'
	`, fixture.workID).Scan(
		&subjectVersionID,
		&subjectID,
		&subjectSlug,
		&venueID,
	); err != nil {
		t.Fatalf("query biomedical publication fixture identities: %v", err)
	}

	asOf := input.GeneratedAt.Add(-3 * time.Minute).UTC()
	startedAt := input.GeneratedAt.Add(-2 * time.Minute).UTC()
	completedAt := input.GeneratedAt.Add(-time.Minute).UTC()
	common := map[string]any{
		"as_of":                      asOf.Format(time.RFC3339Nano),
		"subject_version":            input.SubjectVersion,
		"eligibility_policy_version": input.EligibilityPolicyVersion,
		"jcr_metric_year":            input.JCRMetricYear,
		"jcr_import_receipt":         input.JCRImportReceipt,
		"venue_policy_name":          input.VenuePolicyName,
		"venue_policy_version":       input.VenuePolicyVersion,
		"cohort_revision":            runCohortRevision,
	}
	type runFixture struct {
		id              uuid.UUID
		analysisType    string
		formulaVersion  string
		additionalInput map[string]any
	}
	runs := []runFixture{
		{
			id:             input.TrendAnalysisRunID,
			analysisType:   "publication_trends",
			formulaVersion: analysis.PublicationTrendFormulaVersion,
			additionalInput: map[string]any{
				"recent_window_days":                56,
				"baseline_window_days":              364,
				"minimum_paper_count":               20,
				"minimum_independent_journal_count": 3,
				"minimum_independent_team_count":    3,
				"model_selection_rule":              "fixed_poisson",
				"dispersion_threshold":              0,
			},
		},
		{
			id:             input.JournalAnalysisRunID,
			analysisType:   "journal_editorial_patterns",
			formulaVersion: analysis.JournalPatternFormulaVersion,
			additionalInput: map[string]any{
				"window_days":                  364,
				"minimum_support_count":        10,
				"minimum_field_baseline_count": 40,
			},
		},
		{
			id:             input.OpportunityAnalysisRunID,
			analysisType:   "research_opportunities",
			formulaVersion: analysis.OpportunityFormulaVersion,
			additionalInput: map[string]any{
				"rule_set_version":         analysis.OpportunityRuleSetVersion,
				"citation_analysis_run_id": input.CitationAnalysisRunID,
				"trend_analysis_run_id":    opportunityTrendRunID,
				"journal_analysis_run_id":  input.JournalAnalysisRunID,
			},
		},
	}
	for _, run := range runs {
		payload := make(map[string]any, len(common)+len(run.additionalInput)+1)
		for key, value := range common {
			payload[key] = value
		}
		payload["formula_version"] = run.formulaVersion
		for key, value := range run.additionalInput {
			payload[key] = value
		}
		rawInput, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode %s run input: %v", run.analysisType, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO analysis_runs (
				id,
				analysis_type,
				model_provider,
				model_name,
				prompt_version,
				status,
				input_payload,
				started_at
			) VALUES (
				$1, $2, 'internal', 'deterministic', $3, 'running',
				$4::jsonb, $5
			)
		`,
			run.id,
			run.analysisType,
			run.formulaVersion,
			rawInput,
			startedAt,
		); err != nil {
			t.Fatalf("insert running %s analysis run: %v", run.analysisType, err)
		}
	}

	baselineStart := asOf.Add(-364 * 24 * time.Hour)
	recentStart := asOf.Add(-56 * 24 * time.Hour)
	trendFixtures := []struct {
		entityType string
		entityID   string
		rateRatio  string
	}{
		{entityType: "subject", entityID: subjectSlug, rateRatio: "7.25"},
	}
	var methodName string
	err = tx.QueryRow(ctx, `
		SELECT method.name
		FROM work_methods AS link
		JOIN methods AS method
		  ON method.id = link.method_id
		WHERE link.work_id = $1
		ORDER BY method.name
		LIMIT 1
	`, fixture.workID).Scan(&methodName)
	if err == nil {
		trendFixtures = append(trendFixtures, struct {
			entityType string
			entityID   string
			rateRatio  string
		}{
			entityType: "method",
			entityID:   methodName,
			rateRatio:  "3.75",
		})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("query biomedical publication fixture Method: %v", err)
	}
	for _, trend := range trendFixtures {
		if _, err := tx.Exec(ctx, `
			INSERT INTO publication_trend_snapshots (
				analysis_run_id,
				entity_type,
				entity_id,
				state,
				model,
				model_selection_rule,
				dispersion_threshold,
				predeclared_dispersion_alpha,
				recent_window_start,
				recent_window_end,
				recent_paper_count,
				recent_rate_per_day,
				baseline_window_start,
				baseline_window_end,
				baseline_paper_count,
				baseline_rate_per_day,
				independent_journal_count,
				independent_team_count,
				rate_ratio,
				confidence_level,
				confidence_interval_lower,
				confidence_interval_upper,
				p_value,
				adjusted_p_value,
				cohort_revision,
				formula_version,
				payload,
				evidence,
				generated_at
			) VALUES (
				$1, $2, $3, 'sufficient_evidence', 'poisson',
				'fixed_poisson', 0, 0, $4, $5, 24, 0.5,
				$6, $4, 40, 0.13, 3, 4, $7::numeric, 0.95,
				1.2, 8.5, 0.01, 0.02, $8, $9,
				'{"fixture":"persisted_trend"}',
				'{"fixture":"persisted_trend_evidence"}',
				$10
			)
		`,
			input.TrendAnalysisRunID,
			trend.entityType,
			trend.entityID,
			recentStart,
			asOf,
			baselineStart,
			trend.rateRatio,
			snapshotCohortRevision,
			analysis.PublicationTrendFormulaVersion,
			startedAt,
		); err != nil {
			t.Fatalf("insert %s publication trend snapshot: %v", trend.entityType, err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO journal_pattern_snapshots (
			analysis_run_id,
			pattern_key,
			journal_id,
			subject_version_id,
			subject_id,
			pattern_kind,
			feature_type,
			feature_value,
			state,
			measure,
			journal_feature_paper_count,
			journal_paper_count,
			field_feature_paper_count,
			field_baseline_paper_count,
			journal_exposure,
			field_baseline_exposure,
			covered_paper_count,
			eligible_paper_count,
			coverage,
			effect_value,
			confidence_level,
			confidence_interval_lower,
			confidence_interval_upper,
			p_value,
			adjusted_p_value,
			cohort_revision,
			formula_version,
			payload,
			evidence,
			generated_at
		) VALUES (
			$1, 'method:causal-inference', $2, $3, $4,
			'editorial_pattern', 'method', 'Causal Inference',
			'sufficient_evidence', 'odds_ratio',
			12, 20, 20, 50, NULL, NULL, 20, 20, 1,
			4.5, 0.95, 1.25, 6.5, 0.01, 0.02, $5, $6,
			'{"fixture":"persisted_journal_pattern"}',
			'{"fixture":"persisted_journal_pattern_evidence"}',
			$7
		)
	`,
		input.JournalAnalysisRunID,
		venueID,
		subjectVersionID,
		subjectID,
		snapshotCohortRevision,
		analysis.JournalPatternFormulaVersion,
		startedAt,
	); err != nil {
		t.Fatalf("insert journal pattern snapshot: %v", err)
	}

	opportunityID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO research_opportunity_snapshots (
			id,
			analysis_run_id,
			rule,
			rule_version,
			entity_type,
			entity_id,
			state,
			supporting_work_count,
			covered_paper_count,
			eligible_paper_count,
			coverage,
			confidence_level,
			primary_metric,
			primary_estimate,
			primary_confidence_interval_lower,
			primary_confidence_interval_upper,
			limitations,
			cohort_revision,
			payload,
			evidence,
			generated_at
		) VALUES (
			$1, $2, 'observational_dominance', $3,
			'subject', $4, 'insufficient_evidence',
			1, 1, 2, 0.5, 0.95,
			'observational_share', 0.81, 0.71, 0.91,
			ARRAY['requires_external_validation'], $5,
			'{"fixture":"persisted_opportunity"}',
			'{"fixture":"persisted_opportunity_evidence"}',
			$6
		)
	`,
		opportunityID,
		input.OpportunityAnalysisRunID,
		analysis.OpportunityRuleSetVersion,
		subjectSlug,
		snapshotCohortRevision,
		startedAt,
	); err != nil {
		t.Fatalf("insert research opportunity snapshot: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO research_opportunity_supporting_works (
			analysis_run_id,
			research_opportunity_snapshot_id,
			work_id,
			ordinal
		) VALUES ($1, $2, $3, 1)
	`,
		input.OpportunityAnalysisRunID,
		opportunityID,
		fixture.workID,
	); err != nil {
		t.Fatalf("insert research opportunity supporting Work: %v", err)
	}

	for _, completed := range []struct {
		id            uuid.UUID
		snapshotCount int
	}{
		{id: input.TrendAnalysisRunID, snapshotCount: len(trendFixtures)},
		{id: input.JournalAnalysisRunID, snapshotCount: 1},
		{id: input.OpportunityAnalysisRunID, snapshotCount: 1},
	} {
		rawOutput, err := json.Marshal(map[string]any{
			"cohort_revision": runCohortRevision,
			"snapshot_count":  completed.snapshotCount,
		})
		if err != nil {
			t.Fatalf("encode completed biomedical run output: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE analysis_runs
			SET status = 'succeeded',
			    output_payload = $2::jsonb,
			    completed_at = $3
			WHERE id = $1
		`, completed.id, rawOutput, completedAt); err != nil {
			t.Fatalf("complete biomedical analysis run %s: %v", completed.id, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit biomedical publication fixtures: %v", err)
	}
}
