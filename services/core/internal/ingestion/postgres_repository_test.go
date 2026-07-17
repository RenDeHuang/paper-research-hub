package ingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
)

var (
	postgresRepositoryContainerOnce sync.Once
	postgresRepositoryContainer     *postgres.PostgresContainer
	postgresRepositoryDatabaseURL   string
	postgresRepositoryContainerErr  error
)

func TestPostgresRepositoryPersistsPubMedBiomedicalSemantics(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	eventTime := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	envelope := pubMedBiomedicalRepositoryEnvelope(
		t,
		"76543210",
		"10.1000/pubmed.biomedical",
		eventTime,
	)
	if envelope.Record.Source != source.PubMed ||
		envelope.Record.SourceRecordID != "76543210" ||
		envelope.Record.Title != "Biomedical repository paper default" ||
		len(envelope.Record.AbstractSections) != 2 ||
		len(envelope.Record.MeSHHeadings) != 2 ||
		len(envelope.Record.PublicationTypes) != 3 ||
		len(envelope.Record.Relations) != 2 ||
		envelope.Record.Venue == nil ||
		envelope.Record.Venue.DisplayName != "Journal of Biomedical Evidence" ||
		envelope.Record.PublishedAt == nil {
		t.Fatalf(
			"PubMed parser-derived fixture = %#v, want complete biomedical record",
			envelope.Record,
		)
	}
	if len(envelope.Record.Keywords) != 1 ||
		envelope.Record.Keywords[0].DisplayName != "immune checkpoint blockade" {
		t.Fatalf(
			"supplemented generic keyword = %#v, want one non-XML normalized payload keyword",
			envelope.Record.Keywords,
		)
	}

	payloadCopy := recordPayload(envelope.Record)
	payloadBeforeMutation, err := json.Marshal(payloadCopy)
	if err != nil {
		t.Fatalf("marshal persisted payload before source mutation: %v", err)
	}
	envelope.Record.MeSHHeadings[0].Qualifiers[0].Name = "mutated qualifier"
	*envelope.Record.Keywords[0].Score = 0.01
	payloadAfterMutation, err := json.Marshal(payloadCopy)
	if err != nil {
		t.Fatalf("marshal persisted payload after source mutation: %v", err)
	}
	if !bytes.Equal(payloadAfterMutation, payloadBeforeMutation) {
		t.Fatal("persisted record payload shares nested biomedical state with source.Record")
	}
	envelope = pubMedBiomedicalRepositoryEnvelope(
		t,
		"76543210",
		"10.1000/pubmed.biomedical",
		eventTime,
	)

	job := startPubMedRepositoryJob(t, repository, "biomedical-semantics")
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/pubmed-v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	var normalizedJSON []byte
	if err := pool.QueryRow(ctx, `
		SELECT normalized_payload
		FROM ingestion_normalized_records
		WHERE raw_event_id = $1
	`, raw.ID).Scan(&normalizedJSON); err != nil {
		t.Fatalf("query normalized PubMed payload: %v", err)
	}
	var normalizedPayload struct {
		Identifiers      []source.Identifier      `json:"identifiers"`
		AbstractSections []source.AbstractSection `json:"abstract_sections"`
		MeSHHeadings     []source.MeSHHeading     `json:"mesh_headings"`
		PublicationTypes []source.PublicationType `json:"publication_types"`
		Relations        []source.Relation        `json:"relations"`
		Keywords         []source.Keyword         `json:"keywords"`
	}
	if err := json.Unmarshal(normalizedJSON, &normalizedPayload); err != nil {
		t.Fatalf("decode normalized PubMed payload: %v", err)
	}
	assertJSONOmitsKeys(t, normalizedJSON, "tree_number", "tree_numbers")
	if len(normalizedPayload.AbstractSections) != 2 ||
		normalizedPayload.AbstractSections[0].Label != "BACKGROUND" ||
		normalizedPayload.AbstractSections[1].NLMCategory != "METHODS" {
		t.Errorf(
			"normalized abstract sections = %#v, want structured PubMed abstract",
			normalizedPayload.AbstractSections,
		)
	}
	if len(normalizedPayload.MeSHHeadings) != 2 ||
		normalizedPayload.MeSHHeadings[0].Descriptor.UI != "D009369" ||
		!normalizedPayload.MeSHHeadings[0].Descriptor.MajorTopic ||
		len(normalizedPayload.MeSHHeadings[0].Qualifiers) != 2 ||
		normalizedPayload.MeSHHeadings[0].Qualifiers[0].UI != "Q000628" ||
		!normalizedPayload.MeSHHeadings[0].Qualifiers[0].MajorTopic {
		t.Errorf(
			"normalized MeSH headings = %#v, want descriptors, qualifiers, and major flags",
			normalizedPayload.MeSHHeadings,
		)
	}
	if len(normalizedPayload.PublicationTypes) != 3 ||
		normalizedPayload.PublicationTypes[0].Name != "Journal Article" ||
		normalizedPayload.PublicationTypes[1].Name != "Randomized Controlled Trial" ||
		normalizedPayload.PublicationTypes[2].Name != "Meta-Analysis" {
		t.Errorf(
			"normalized publication types = %#v, want exact PubMed types",
			normalizedPayload.PublicationTypes,
		)
	}
	if len(normalizedPayload.Relations) != 2 ||
		normalizedPayload.Relations[0].Type != "CommentIn" ||
		normalizedPayload.Relations[1].Type != "ErratumIn" {
		t.Errorf(
			"normalized relations = %#v, want comment and correction relations",
			normalizedPayload.Relations,
		)
	}
	if len(normalizedPayload.Keywords) != 1 ||
		normalizedPayload.Keywords[0].DisplayName != "immune checkpoint blockade" ||
		normalizedPayload.Keywords[0].Score == nil ||
		*normalizedPayload.Keywords[0].Score != 0.97 {
		t.Errorf("normalized keywords = %#v, want scored keyword", normalizedPayload.Keywords)
	}
	if len(normalizedPayload.Identifiers) != 3 ||
		normalizedPayload.Identifiers[0] != (source.Identifier{
			Scheme: source.IdentifierPMID,
			Value:  "76543210",
		}) ||
		normalizedPayload.Identifiers[1] != (source.Identifier{
			Scheme: source.IdentifierDOI,
			Value:  "10.1000/pubmed.biomedical",
		}) ||
		normalizedPayload.Identifiers[2] != (source.Identifier{
			Scheme: source.IdentifierPMCID,
			Value:  "PMC76543210",
		}) {
		t.Errorf(
			"normalized identifiers = %#v, want PMID, PMCID, and DOI",
			normalizedPayload.Identifiers,
		)
	}

	candidate, err := NewProjectionCandidate(
		normalized,
		source.ScopeDecision{
			Status: source.ScopeIncluded,
			Reason: "controlled_identity_present",
		},
	)
	if err != nil {
		t.Fatalf("NewProjectionCandidate() error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/pubmed-v1",
		"projection/pubmed-v1",
	); err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/pubmed-v1",
		"projection/pubmed-v1",
	); err != nil {
		t.Fatalf("Project(replay) error = %v", err)
	}

	var projectionJSON []byte
	var projectionPayloadMatchesNormalized bool
	if err := pool.QueryRow(ctx, `
		SELECT
			assertion.record_payload,
			assertion.record_payload = normalized.normalized_payload
		FROM ingestion_projection_assertions AS assertion
		JOIN ingestion_normalized_records AS normalized
		  ON normalized.raw_event_id = assertion.raw_event_id
		WHERE assertion.raw_event_id = $1
		  AND assertion.scope_policy_version = 'scope/pubmed-v1'
		  AND assertion.projection_policy_version = 'projection/pubmed-v1'
	`, raw.ID).Scan(
		&projectionJSON,
		&projectionPayloadMatchesNormalized,
	); err != nil {
		t.Fatalf("query immutable projection payload: %v", err)
	}
	if !projectionPayloadMatchesNormalized {
		t.Fatal("projection assertion payload differs from immutable normalized payload")
	}
	assertJSONOmitsKeys(t, projectionJSON, "tree_number", "tree_numbers")

	var publicTreeTable, searchPathTreeTable *string
	if err := pool.QueryRow(ctx, `
		SELECT
			to_regclass('public.mesh_descriptor_tree_numbers')::text,
			to_regclass('mesh_descriptor_tree_numbers')::text
	`).Scan(&publicTreeTable, &searchPathTreeTable); err != nil {
		t.Fatalf("query forbidden Tree Number table: %v", err)
	}
	if publicTreeTable != nil || searchPathTreeTable != nil {
		t.Fatalf(
			"Tree Number table = public %v/search_path %v, want absent",
			publicTreeTable,
			searchPathTreeTable,
		)
	}

	var headingCount int
	var headings string
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*),
			COALESCE(string_agg(
				descriptor.descriptor_ui || '|' ||
				heading.source_path || '|' ||
				heading.descriptor_label || '|' ||
				heading.is_major_topic::text,
				E'\n'
				ORDER BY heading.source_path
			), '')
		FROM work_mesh_headings AS heading
		JOIN mesh_descriptors AS descriptor
		  ON descriptor.id = heading.descriptor_id
	`).Scan(&headingCount, &headings); err != nil {
		t.Fatalf("query persisted MeSH headings: %v", err)
	}
	wantHeadings := strings.Join([]string{
		"D009369|/PubmedArticle/MedlineCitation/MeshHeadingList/MeshHeading[1]/DescriptorName|Neoplasms|true",
		"D001943|/PubmedArticle/MedlineCitation/MeshHeadingList/MeshHeading[2]/DescriptorName|Breast Neoplasms|false",
	}, "\n")
	if headingCount != 2 || headings != wantHeadings {
		t.Fatalf("persisted MeSH headings = %d/%q, want 2/%q", headingCount, headings, wantHeadings)
	}

	var qualifierCount int
	var qualifiers string
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*),
			COALESCE(string_agg(
				qualifier.qualifier_ui || '|' ||
				assertion.source_path || '|' ||
				assertion.qualifier_label || '|' ||
				assertion.is_major_topic::text,
				E'\n'
				ORDER BY assertion.source_path
			), '')
		FROM work_mesh_qualifiers AS assertion
		JOIN mesh_qualifiers AS qualifier
		  ON qualifier.id = assertion.qualifier_id
	`).Scan(&qualifierCount, &qualifiers); err != nil {
		t.Fatalf("query persisted MeSH qualifiers: %v", err)
	}
	wantQualifiers := strings.Join([]string{
		"Q000628|/PubmedArticle/MedlineCitation/MeshHeadingList/MeshHeading[1]/QualifierName[1]|therapy|true",
		"Q000235|/PubmedArticle/MedlineCitation/MeshHeadingList/MeshHeading[1]/QualifierName[2]|genetics|false",
		"Q000503|/PubmedArticle/MedlineCitation/MeshHeadingList/MeshHeading[2]/QualifierName[1]|pathology|true",
	}, "\n")
	if qualifierCount != 3 || qualifiers != wantQualifiers {
		t.Fatalf(
			"persisted MeSH qualifiers = %d/%q, want 3/%q",
			qualifierCount,
			qualifiers,
			wantQualifiers,
		)
	}

	var publicationTypeCount int
	var publicationTypes string
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*),
			COALESCE(string_agg(
				publication_type.publication_type_ui || '|' ||
				assertion.source_path || '|' ||
				assertion.publication_type_label,
				E'\n'
				ORDER BY assertion.source_path
			), '')
		FROM work_publication_types AS assertion
		JOIN publication_types AS publication_type
		  ON publication_type.id = assertion.publication_type_id
	`).Scan(&publicationTypeCount, &publicationTypes); err != nil {
		t.Fatalf("query persisted Publication Types: %v", err)
	}
	wantPublicationTypes := strings.Join([]string{
		"D016428|/PubmedArticle/MedlineCitation/Article/PublicationTypeList/PublicationType[1]|Journal Article",
		"D016449|/PubmedArticle/MedlineCitation/Article/PublicationTypeList/PublicationType[2]|Randomized Controlled Trial",
		"D017418|/PubmedArticle/MedlineCitation/Article/PublicationTypeList/PublicationType[3]|Meta-Analysis",
	}, "\n")
	if publicationTypeCount != 3 || publicationTypes != wantPublicationTypes {
		t.Fatalf(
			"persisted Publication Types = %d/%q, want 3/%q",
			publicationTypeCount,
			publicationTypes,
			wantPublicationTypes,
		)
	}

	var genericPaperTypes int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM field_assertions
		WHERE field_name = 'paper_type'
	`).Scan(&genericPaperTypes); err != nil {
		t.Fatalf("count generic paper type assertions: %v", err)
	}
	if genericPaperTypes != 0 {
		t.Fatalf("generic paper type assertions = %d, want 0", genericPaperTypes)
	}
}

func TestPostgresRepositoryRejectsProjectionCandidateBiomedicalMutations(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*source.Record)
	}{
		{
			name: "descriptor UI",
			mutate: func(record *source.Record) {
				record.MeSHHeadings[0].Descriptor.UI = "D999999"
			},
		},
		{
			name: "descriptor label",
			mutate: func(record *source.Record) {
				record.MeSHHeadings[0].Descriptor.Name = "Mutated Neoplasms"
			},
		},
		{
			name: "major topic",
			mutate: func(record *source.Record) {
				record.MeSHHeadings[0].Descriptor.MajorTopic =
					!record.MeSHHeadings[0].Descriptor.MajorTopic
			},
		},
		{
			name: "heading order",
			mutate: func(record *source.Record) {
				record.MeSHHeadings[0], record.MeSHHeadings[1] =
					record.MeSHHeadings[1], record.MeSHHeadings[0]
			},
		},
		{
			name: "publication type",
			mutate: func(record *source.Record) {
				record.PublicationTypes[0].Name = "Mutated Publication Type"
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			pool := openIngestionTestPool(t)
			repository := mustPostgresRepository(t, pool)
			ctx := context.Background()
			suffix := strings.ToLower(strings.ReplaceAll(testCase.name, " ", "-"))
			envelope := pubMedBiomedicalRepositoryEnvelope(
				t,
				"76543250",
				"10.1000/pubmed.candidate-mutation."+suffix,
				time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
			)
			job := startPubMedRepositoryJob(t, repository, "candidate-mutation-"+suffix)
			raw, err := repository.PersistRaw(ctx, job.ID, envelope)
			if err != nil {
				t.Fatalf("PersistRaw() error = %v", err)
			}
			normalized, err := repository.Normalize(
				ctx,
				job.ID,
				raw,
				"normalization/pubmed-v1",
			)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			candidate, err := NewProjectionCandidate(
				normalized,
				source.ScopeDecision{
					Status: source.ScopeIncluded,
					Reason: "controlled_identity_present",
				},
			)
			if err != nil {
				t.Fatalf("NewProjectionCandidate() error = %v", err)
			}
			testCase.mutate(&candidate.Record)

			_, err = repository.Project(
				ctx,
				job.ID,
				candidate,
				"scope/pubmed-v1",
				"projection/pubmed-v1",
			)
			if err == nil ||
				!strings.Contains(
					err.Error(),
					"projection candidate differs from immutable normalized payload",
				) {
				t.Fatalf(
					"Project(mutated candidate) error = %v, want immutable normalized payload rejection",
					err,
				)
			}

			var projectionRows, semanticRows, canonicalRows, workRows int
			if err := pool.QueryRow(ctx, `
				SELECT
					(SELECT count(*) FROM ingestion_projection_assertions),
					(
						(SELECT count(*) FROM work_mesh_headings) +
						(SELECT count(*) FROM work_mesh_qualifiers) +
						(SELECT count(*) FROM work_publication_types)
					),
					(
						(SELECT count(*) FROM mesh_descriptors) +
						(SELECT count(*) FROM mesh_qualifiers) +
						(SELECT count(*) FROM publication_types)
					),
					(SELECT count(*) FROM works)
			`).Scan(
				&projectionRows,
				&semanticRows,
				&canonicalRows,
				&workRows,
			); err != nil {
				t.Fatalf("query mutated candidate rollback state: %v", err)
			}
			if projectionRows != 0 ||
				semanticRows != 0 ||
				canonicalRows != 0 ||
				workRows != 0 {
				t.Fatalf(
					"mutated candidate rollback = projection %d semantic %d canonical %d work %d, want all zero",
					projectionRows,
					semanticRows,
					canonicalRows,
					workRows,
				)
			}
		})
	}
}

func TestPostgresRepositoryRejectsInvalidPubMedBiomedicalSemanticsAtomically(t *testing.T) {
	testCases := []struct {
		name      string
		mutate    func(*source.Record)
		wantError string
	}{
		{
			name: "blank descriptor UI",
			mutate: func(record *source.Record) {
				record.MeSHHeadings[0].Descriptor.UI = "   "
			},
			wantError: "MeSH descriptor 1 UI is required",
		},
		{
			name: "blank descriptor label",
			mutate: func(record *source.Record) {
				record.MeSHHeadings[0].Descriptor.Name = "   "
			},
			wantError: "MeSH descriptor 1 source label is required",
		},
		{
			name: "blank qualifier UI",
			mutate: func(record *source.Record) {
				record.MeSHHeadings[0].Qualifiers[0].UI = "   "
			},
			wantError: "MeSH qualifier 1.1 UI is required",
		},
		{
			name: "blank qualifier label",
			mutate: func(record *source.Record) {
				record.MeSHHeadings[0].Qualifiers[0].Name = "   "
			},
			wantError: "MeSH qualifier 1.1 source label is required",
		},
		{
			name: "blank publication type UI",
			mutate: func(record *source.Record) {
				record.PublicationTypes[0].UI = "   "
			},
			wantError: "Publication Type 1 UI is required",
		},
		{
			name: "blank publication type label",
			mutate: func(record *source.Record) {
				record.PublicationTypes[0].Name = "   "
			},
			wantError: "Publication Type 1 source label is required",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			pool := openIngestionTestPool(t)
			repository := mustPostgresRepository(t, pool)
			ctx := context.Background()
			suffix := strings.ToLower(strings.ReplaceAll(testCase.name, " ", "-"))
			envelope := pubMedBiomedicalRepositoryEnvelope(
				t,
				"76543300",
				"10.1000/pubmed.invalid."+suffix,
				time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
			)
			testCase.mutate(&envelope.Record)
			if err := envelope.Validate(); err != nil {
				t.Fatalf("mutated envelope validation error = %v", err)
			}

			job := startPubMedRepositoryJob(t, repository, "invalid-"+suffix)
			raw, err := repository.PersistRaw(ctx, job.ID, envelope)
			if err != nil {
				t.Fatalf("PersistRaw() error = %v", err)
			}
			normalized, err := repository.Normalize(
				ctx,
				job.ID,
				raw,
				"normalization/pubmed-v1",
			)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			candidate, err := NewProjectionCandidate(
				normalized,
				source.ScopeDecision{
					Status: source.ScopeIncluded,
					Reason: "controlled_identity_present",
				},
			)
			if err != nil {
				t.Fatalf("NewProjectionCandidate() error = %v", err)
			}
			_, err = repository.Project(
				ctx,
				job.ID,
				candidate,
				"scope/pubmed-v1",
				"projection/pubmed-v1",
			)
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("Project() error = %v, want %q", err, testCase.wantError)
			}

			var projectionRows, canonicalRows, semanticRows, workRows int
			if err := pool.QueryRow(ctx, `
				SELECT
					(SELECT count(*) FROM ingestion_projection_assertions),
					(
						(SELECT count(*) FROM mesh_descriptors) +
						(SELECT count(*) FROM mesh_qualifiers) +
						(SELECT count(*) FROM publication_types)
					),
					(
						(SELECT count(*) FROM work_mesh_headings) +
						(SELECT count(*) FROM work_mesh_qualifiers) +
						(SELECT count(*) FROM work_publication_types)
					),
					(SELECT count(*) FROM works)
			`).Scan(
				&projectionRows,
				&canonicalRows,
				&semanticRows,
				&workRows,
			); err != nil {
				t.Fatalf("query rollback state: %v", err)
			}
			if projectionRows != 0 ||
				canonicalRows != 0 ||
				semanticRows != 0 ||
				workRows != 0 {
				t.Fatalf(
					"rollback state = projections %d canonical %d semantics %d works %d, want all zero",
					projectionRows,
					canonicalRows,
					semanticRows,
					workRows,
				)
			}
		})
	}
}

func TestPostgresRepositoryKeepsPubMedSemanticAssertionsForNonWinningEvents(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	baseTime := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)
	older := pubMedBiomedicalRepositoryEnvelope(
		t,
		"76543400",
		"10.1000/pubmed.semantic-history",
		baseTime,
	)
	older = pubMedEnvelopeWithDescriptorLabel(
		t,
		older,
		"Historical Neoplasms",
		"historical",
	)
	newer := pubMedBiomedicalRepositoryEnvelope(
		t,
		"76543400",
		"10.1000/pubmed.semantic-history",
		baseTime.Add(time.Hour),
	)
	newer = pubMedEnvelopeWithDescriptorLabel(
		t,
		newer,
		"Current Neoplasms",
		"current",
	)

	for index, envelope := range []Envelope{newer, older} {
		job := startPubMedRepositoryJob(
			t,
			repository,
			fmt.Sprintf("semantic-history-%d", index),
		)
		raw, err := repository.PersistRaw(ctx, job.ID, envelope)
		if err != nil {
			t.Fatalf("PersistRaw(%d) error = %v", index, err)
		}
		normalized, err := repository.Normalize(
			ctx,
			job.ID,
			raw,
			"normalization/pubmed-v1",
		)
		if err != nil {
			t.Fatalf("Normalize(%d) error = %v", index, err)
		}
		candidate, err := NewProjectionCandidate(
			normalized,
			source.ScopeDecision{
				Status: source.ScopeIncluded,
				Reason: "controlled_identity_present",
			},
		)
		if err != nil {
			t.Fatalf("NewProjectionCandidate(%d) error = %v", index, err)
		}
		result, err := repository.Project(
			ctx,
			job.ID,
			candidate,
			"scope/pubmed-v1",
			"projection/pubmed-v1",
		)
		if err != nil {
			t.Fatalf("Project(%d) error = %v", index, err)
		}
		if index == 1 && result.Status != ProjectionStatusUnchanged {
			t.Fatalf("older semantic projection status = %q, want unchanged", result.Status)
		}
	}

	var assertionCount int
	var retainedLabels string
	if err := pool.QueryRow(ctx, `
		SELECT
			count(DISTINCT assertion.id),
			string_agg(
				heading.descriptor_label,
				'|' ORDER BY heading.descriptor_label
			)
		FROM ingestion_projection_assertions AS assertion
		JOIN work_mesh_headings AS heading
		  ON heading.projection_assertion_id = assertion.id
		JOIN mesh_descriptors AS descriptor
		  ON descriptor.id = heading.descriptor_id
		WHERE descriptor.descriptor_ui = 'D009369'
	`).Scan(&assertionCount, &retainedLabels); err != nil {
		t.Fatalf("query retained semantic assertions: %v", err)
	}
	if assertionCount != 2 ||
		retainedLabels != "Current Neoplasms|Historical Neoplasms" {
		t.Fatalf(
			"retained semantics = %d/%q, want both historical and current assertions",
			assertionCount,
			retainedLabels,
		)
	}

	var currentLabel string
	if err := pool.QueryRow(ctx, `
		SELECT heading.descriptor_label
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.raw_event_id = state.raw_event_id
		 AND assertion.source_record_uuid = state.source_record_uuid
		 AND assertion.work_id = state.work_id
		 AND assertion.scope_policy_version = state.scope_policy_version
		 AND assertion.projection_policy_version = state.projection_policy_version
		JOIN work_mesh_headings AS heading
		  ON heading.projection_assertion_id = assertion.id
		JOIN mesh_descriptors AS descriptor
		  ON descriptor.id = heading.descriptor_id
		WHERE descriptor.descriptor_ui = 'D009369'
	`).Scan(&currentLabel); err != nil {
		t.Fatalf("query current semantic assertion: %v", err)
	}
	if currentLabel != "Current Neoplasms" {
		t.Fatalf("current semantic label = %q, want current winner", currentLabel)
	}
}

func TestPostgresRepositoryBindsCurrentPubMedSemanticsToCompletePolicyTuple(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	envelope := pubMedBiomedicalRepositoryEnvelope(
		t,
		"76543500",
		"10.1000/pubmed.semantic-policies",
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	job := startPubMedRepositoryJob(t, repository, "semantic-policies")
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(
		ctx,
		job.ID,
		raw,
		"normalization/pubmed-v1",
	)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	decision := source.ScopeDecision{
		Status: source.ScopeIncluded,
		Reason: "controlled_identity_present",
	}
	candidate, err := NewProjectionCandidate(normalized, decision)
	if err != nil {
		t.Fatalf("NewProjectionCandidate() error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/pubmed-v1",
		"projection/pubmed-v1",
	); err != nil {
		t.Fatalf("Project(first policy) error = %v", err)
	}

	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/pubmed-v2",
		"projection/pubmed-v2",
	); err != nil {
		t.Fatalf("Project(second policy) error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/pubmed-v2",
		"projection/pubmed-v2",
	); err != nil {
		t.Fatalf("Project(second policy replay) error = %v", err)
	}

	var (
		assertionCount                 int
		distinctProjectionAssertionIDs int
		distinctSemanticFingerprints   int
		canonicalDescriptorCount       int
	)
	if err := pool.QueryRow(ctx, `
		WITH policy_assertions AS (
			SELECT
				assertion.id,
				md5(
					COALESCE((
						SELECT string_agg(
							descriptor.descriptor_ui || '|' ||
							heading.source_path || '|' ||
							heading.descriptor_label || '|' ||
							heading.is_major_topic::text,
							E'\n' ORDER BY heading.source_path
						)
						FROM work_mesh_headings AS heading
						JOIN mesh_descriptors AS descriptor
						  ON descriptor.id = heading.descriptor_id
						WHERE heading.projection_assertion_id = assertion.id
					), '') || E'\n--qualifiers--\n' ||
					COALESCE((
						SELECT string_agg(
							qualifier.qualifier_ui || '|' ||
							semantic.source_path || '|' ||
							semantic.qualifier_label || '|' ||
							semantic.is_major_topic::text,
							E'\n' ORDER BY semantic.source_path
						)
						FROM work_mesh_qualifiers AS semantic
						JOIN mesh_qualifiers AS qualifier
						  ON qualifier.id = semantic.qualifier_id
						WHERE semantic.projection_assertion_id = assertion.id
					), '') || E'\n--publication-types--\n' ||
					COALESCE((
						SELECT string_agg(
							publication_type.publication_type_ui || '|' ||
							semantic.source_path || '|' ||
							semantic.publication_type_label,
							E'\n' ORDER BY semantic.source_path
						)
						FROM work_publication_types AS semantic
						JOIN publication_types AS publication_type
						  ON publication_type.id = semantic.publication_type_id
						WHERE semantic.projection_assertion_id = assertion.id
					), '')
				) AS semantic_fingerprint
			FROM ingestion_projection_assertions AS assertion
			WHERE assertion.raw_event_id = $1
		)
		SELECT
			count(*),
			count(DISTINCT id),
			count(DISTINCT semantic_fingerprint),
			(
				SELECT count(*)
				FROM mesh_descriptors
				WHERE descriptor_ui = 'D009369'
			)
		FROM policy_assertions
	`, raw.ID).Scan(
		&assertionCount,
		&distinctProjectionAssertionIDs,
		&distinctSemanticFingerprints,
		&canonicalDescriptorCount,
	); err != nil {
		t.Fatalf("query policy assertion counts: %v", err)
	}
	if assertionCount != 2 ||
		distinctProjectionAssertionIDs != 2 ||
		distinctSemanticFingerprints != 1 ||
		canonicalDescriptorCount != 1 {
		t.Fatalf(
			"policy assertions/IDs/semantic fingerprints/canonical descriptors = %d/%d/%d/%d, want 2/2/1/1",
			assertionCount,
			distinctProjectionAssertionIDs,
			distinctSemanticFingerprints,
			canonicalDescriptorCount,
		)
	}

	var sourceRecordOnlyMatches int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.source_record_uuid = state.source_record_uuid
		 AND assertion.work_id = state.work_id
		JOIN work_mesh_headings AS heading
		  ON heading.projection_assertion_id = assertion.id
		JOIN mesh_descriptors AS descriptor
		  ON descriptor.id = heading.descriptor_id
		WHERE descriptor.descriptor_ui = 'D009369'
	`).Scan(&sourceRecordOnlyMatches); err != nil {
		t.Fatalf("query source-record-only semantic matches: %v", err)
	}
	if sourceRecordOnlyMatches != 2 {
		t.Fatalf(
			"source-record-only semantic matches = %d, want ambiguous 2",
			sourceRecordOnlyMatches,
		)
	}

	var completeTupleMatches int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.raw_event_id = state.raw_event_id
		 AND assertion.source_record_uuid = state.source_record_uuid
		 AND assertion.work_id = state.work_id
		 AND assertion.scope_policy_version = state.scope_policy_version
		 AND assertion.projection_policy_version = state.projection_policy_version
	`).Scan(&completeTupleMatches); err != nil {
		t.Fatalf("query complete winner tuple matches: %v", err)
	}
	if completeTupleMatches != 1 {
		t.Fatalf("complete winner tuple matches = %d, want exactly 1", completeTupleMatches)
	}

	var currentLabel, currentQualifierLabel, currentPublicationTypeLabel string
	if err := pool.QueryRow(ctx, `
		SELECT
			heading.descriptor_label,
			qualifier.qualifier_label,
			publication_type.publication_type_label
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.raw_event_id = state.raw_event_id
		 AND assertion.source_record_uuid = state.source_record_uuid
		 AND assertion.work_id = state.work_id
		 AND assertion.scope_policy_version = state.scope_policy_version
		 AND assertion.projection_policy_version = state.projection_policy_version
		JOIN work_mesh_headings AS heading
		  ON heading.projection_assertion_id = assertion.id
		JOIN mesh_descriptors AS descriptor
		  ON descriptor.id = heading.descriptor_id
		JOIN work_mesh_qualifiers AS qualifier
		  ON qualifier.work_mesh_heading_id = heading.id
		JOIN mesh_qualifiers AS canonical_qualifier
		  ON canonical_qualifier.id = qualifier.qualifier_id
		JOIN work_publication_types AS publication_type
		  ON publication_type.projection_assertion_id = assertion.id
		JOIN publication_types AS canonical_publication_type
		  ON canonical_publication_type.id = publication_type.publication_type_id
		WHERE descriptor.descriptor_ui = 'D009369'
		  AND canonical_qualifier.qualifier_ui = 'Q000628'
		  AND canonical_publication_type.publication_type_ui = 'D016428'
	`).Scan(
		&currentLabel,
		&currentQualifierLabel,
		&currentPublicationTypeLabel,
	); err != nil {
		t.Fatalf("query complete-tuple current semantics: %v", err)
	}
	if currentLabel != "Neoplasms" ||
		currentQualifierLabel != "therapy" ||
		currentPublicationTypeLabel != "Journal Article" {
		t.Fatalf(
			"current tuple semantics = %q/%q/%q, want immutable normalized labels",
			currentLabel,
			currentQualifierLabel,
			currentPublicationTypeLabel,
		)
	}

	var currentScopePolicy, currentProjectionPolicy string
	if err := pool.QueryRow(ctx, `
		SELECT scope_policy_version, projection_policy_version
		FROM work_projection_states
	`).Scan(&currentScopePolicy, &currentProjectionPolicy); err != nil {
		t.Fatalf("query current policy tuple: %v", err)
	}
	if currentScopePolicy != "scope/pubmed-v2" ||
		currentProjectionPolicy != "projection/pubmed-v2" {
		t.Fatalf(
			"current policy tuple = %q/%q, want scope/pubmed-v2/projection/pubmed-v2",
			currentScopePolicy,
			currentProjectionPolicy,
		)
	}
}

func TestPostgresRepositoryPersistsRawSnapshotsIdempotently(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	job := startRepositoryJob(t, repository, "raw-idempotency")
	envelope := repositoryEnvelope(t, "W100", "10.1000/raw-idempotency", time.Now().UTC())

	first, err := repository.PersistRaw(context.Background(), job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw(first) error = %v", err)
	}
	second, err := repository.PersistRaw(context.Background(), job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw(second) error = %v", err)
	}

	if first.ID != second.ID ||
		first.Disposition != RawDispositionInserted ||
		second.Disposition != RawDispositionReused {
		t.Fatalf("raw dispositions = %#v then %#v", first, second)
	}

	var rawEvents int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM ingestion_raw_events
		WHERE logical_source = $1
		  AND event_key = $2
		  AND content_hash = $3
	`, envelope.LogicalSource, envelope.EventKey, envelope.Raw.SHA256).Scan(&rawEvents); err != nil {
		t.Fatalf("count raw events: %v", err)
	}
	if rawEvents != 1 {
		t.Fatalf("raw event count = %d, want 1", rawEvents)
	}
}

func TestPostgresRepositoryAllowsNewRunAfterSameKeyFinishes(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	pending, err := NewJob(
		"sync/openalex/repeatable",
		source.OpenAlex,
		"test:repeatable",
		map[string]any{"query": "agent"},
	)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}

	first, err := repository.Start(context.Background(), pending)
	if err != nil {
		t.Fatalf("Start(first) error = %v", err)
	}
	finished, err := first.Succeed()
	if err != nil {
		t.Fatalf("Succeed() error = %v", err)
	}
	if err := repository.Complete(context.Background(), finished); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	second, err := repository.Start(context.Background(), pending)
	if err != nil {
		t.Fatalf("Start(second) error = %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("second job ID = %q, want a new run", second.ID)
	}
}

func TestPostgresRepositoryRejectsConcurrentRunWithSameKey(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	pending, err := NewJob(
		"sync/openalex/concurrent",
		source.OpenAlex,
		"test:concurrent",
		map[string]any{"query": "agent"},
	)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	first, err := repository.Start(context.Background(), pending)
	if err != nil {
		t.Fatalf("Start(first) error = %v", err)
	}

	_, err = repository.Start(context.Background(), pending)
	var conflict *ErrIdempotencyConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("Start(second) error = %v, want ErrIdempotencyConflict", err)
	}
	if conflict.ExistingJobID != first.ID {
		t.Fatalf("conflict existing job ID = %q, want %q", conflict.ExistingJobID, first.ID)
	}
}

func TestPostgresRepositoryStartSucceedsWhenPriorJobFinishesBeforeActiveCheck(t *testing.T) {
	for _, testCase := range postgresRepositoryTerminalCases() {
		t.Run(testCase.name, func(t *testing.T) {
			pool := openIngestionTestPool(t)
			repository := mustPostgresRepository(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			pending, err := NewJob(
				"sync/openalex/terminal-before-check/"+testCase.name,
				source.OpenAlex,
				"test:terminal-before-check:"+testCase.name,
				map[string]any{"query": "agent"},
			)
			if err != nil {
				t.Fatalf("NewJob() error = %v", err)
			}
			first, err := repository.Start(ctx, pending)
			if err != nil {
				t.Fatalf("Start(first) error = %v", err)
			}
			terminal, err := testCase.terminal(first)
			if err != nil {
				t.Fatalf("%s terminal transition error = %v", testCase.name, err)
			}

			lockConnection, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatalf("acquire idempotency lock connection: %v", err)
			}
			defer lockConnection.Release()
			if _, err := lockConnection.Exec(ctx, `
				SELECT pg_advisory_lock(hashtextextended($1, $2))
			`, pending.IdempotencyKey, postgresJobIdempotencyAdvisorySeed); err != nil {
				t.Fatalf("acquire idempotency advisory barrier: %v", err)
			}
			var unlockOnce sync.Once
			unlock := func() {
				unlockOnce.Do(func() {
					if _, unlockErr := lockConnection.Exec(
						context.Background(),
						`SELECT pg_advisory_unlock(hashtextextended($1, $2))`,
						pending.IdempotencyKey,
						postgresJobIdempotencyAdvisorySeed,
					); unlockErr != nil {
						t.Errorf("release idempotency advisory barrier: %v", unlockErr)
					}
				})
			}
			defer unlock()

			startLockTrace := newPostgresQueryBarrierTracer(func(sql string) bool {
				return strings.Contains(sql, "pg_advisory_xact_lock") &&
					strings.Contains(sql, "hashtextextended")
			}, nil)
			startPool := openTracedIngestionTestPool(t, pool, startLockTrace)
			startRepository := mustPostgresRepository(t, startPool)
			startResult := make(chan postgresRepositoryStartResult, 1)
			go func() {
				job, startErr := startRepository.Start(ctx, pending)
				startResult <- postgresRepositoryStartResult{job: job, err: startErr}
			}()

			waitForPostgresTraceSignal(
				t,
				ctx,
				startLockTrace.queryStarted,
				"new Start to reach the idempotency advisory lock",
			)
			if err := testCase.persist(ctx, repository, terminal); err != nil {
				t.Fatalf("%s prior job error = %v", testCase.name, err)
			}

			var persistedStatus JobStatus
			if err := pool.QueryRow(ctx, `
				SELECT status
				FROM ingestion_jobs
				WHERE id = $1
			`, first.ID).Scan(&persistedStatus); err != nil {
				t.Fatalf("query prior job status: %v", err)
			}
			if persistedStatus != testCase.status {
				t.Fatalf(
					"prior job status before releasing Start = %q, want %q",
					persistedStatus,
					testCase.status,
				)
			}

			unlock()
			result := waitForPostgresStartResult(t, ctx, startResult)
			if result.err != nil {
				t.Fatalf("Start(after %s) error = %v", testCase.name, result.err)
			}
			if result.job.ID == first.ID {
				t.Fatalf("new Start job ID = %q, want a new run", result.job.ID)
			}
		})
	}
}

func TestPostgresRepositoryStartLocksActiveJobBeforeTerminalUpdate(t *testing.T) {
	for _, testCase := range postgresRepositoryTerminalCases() {
		t.Run(testCase.name, func(t *testing.T) {
			pool := openIngestionTestPool(t)
			repository := mustPostgresRepository(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			pending, err := NewJob(
				"sync/openalex/start-before-terminal/"+testCase.name,
				source.OpenAlex,
				"test:start-before-terminal:"+testCase.name,
				map[string]any{"query": "agent"},
			)
			if err != nil {
				t.Fatalf("NewJob() error = %v", err)
			}
			first, err := repository.Start(ctx, pending)
			if err != nil {
				t.Fatalf("Start(first) error = %v", err)
			}
			terminal, err := testCase.terminal(first)
			if err != nil {
				t.Fatalf("%s terminal transition error = %v", testCase.name, err)
			}

			releaseActiveRow := make(chan struct{})
			var releaseActiveRowOnce sync.Once
			releaseStart := func() {
				releaseActiveRowOnce.Do(func() {
					close(releaseActiveRow)
				})
			}
			defer releaseStart()
			startRowTrace := newPostgresQueryBarrierTracer(func(sql string) bool {
				return strings.Contains(sql, "FROM ingestion_jobs") &&
					strings.Contains(sql, "status IN ('pending', 'running')") &&
					strings.Contains(sql, "FOR UPDATE")
			}, releaseActiveRow)
			startPool := openTracedIngestionTestPool(t, pool, startRowTrace)
			startRepository := mustPostgresRepository(t, startPool)
			startResult := make(chan postgresRepositoryStartResult, 1)
			go func() {
				job, startErr := startRepository.Start(ctx, pending)
				startResult <- postgresRepositoryStartResult{job: job, err: startErr}
			}()

			waitForPostgresTraceSignal(
				t,
				ctx,
				startRowTrace.queryEnded,
				"second Start to select and lock the active job row",
			)

			finishTrace := newPostgresQueryBarrierTracer(func(sql string) bool {
				return strings.Contains(sql, "UPDATE ingestion_jobs") &&
					strings.Contains(sql, "finished_at = now()") &&
					strings.Contains(sql, "status = $2")
			}, nil)
			finishPool := openTracedIngestionTestPool(t, pool, finishTrace)
			finishRepository := mustPostgresRepository(t, finishPool)
			finishResult := make(chan error, 1)
			go func() {
				finishResult <- testCase.persist(ctx, finishRepository, terminal)
			}()
			waitForPostgresTraceSignal(
				t,
				ctx,
				finishTrace.queryStarted,
				testCase.name+" update to enter PostgreSQL while Start holds the row lock",
			)

			releaseStart()
			result := waitForPostgresStartResult(t, ctx, startResult)
			var conflict *ErrIdempotencyConflict
			if !errors.As(result.err, &conflict) {
				t.Fatalf(
					"Start(second) error = %v, want ErrIdempotencyConflict",
					result.err,
				)
			}
			if conflict.ExistingJobID != first.ID {
				t.Fatalf(
					"conflict existing job ID = %q, want %q",
					conflict.ExistingJobID,
					first.ID,
				)
			}

			select {
			case finishErr := <-finishResult:
				if finishErr != nil {
					t.Fatalf("%s serialized update error = %v", testCase.name, finishErr)
				}
			case <-ctx.Done():
				t.Fatalf("wait for serialized %s update: %v", testCase.name, ctx.Err())
			}

			var persistedStatus JobStatus
			if err := pool.QueryRow(ctx, `
				SELECT status
				FROM ingestion_jobs
				WHERE id = $1
			`, first.ID).Scan(&persistedStatus); err != nil {
				t.Fatalf("query serialized terminal status: %v", err)
			}
			if persistedStatus != testCase.status {
				t.Fatalf(
					"serialized terminal status = %q, want %q",
					persistedStatus,
					testCase.status,
				)
			}
		})
	}
}

func TestPostgresRepositoryRejectsRawWritesAfterJobFinishesAtomically(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	job := startRepositoryJob(t, repository, "raw-after-finish")
	finished, err := job.Succeed()
	if err != nil {
		t.Fatalf("Succeed() error = %v", err)
	}
	if err := repository.Complete(context.Background(), finished); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	envelope := repositoryEnvelope(
		t,
		"W100-finished",
		"10.1000/raw-after-finish",
		time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC),
	)

	if _, err := repository.PersistRaw(
		context.Background(),
		job.ID,
		envelope,
	); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("PersistRaw() error = %v, want terminal job rejection", err)
	}

	var rawEvents, rawInserted int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM ingestion_raw_events WHERE job_id = $1),
			(SELECT raw_inserted FROM ingestion_jobs WHERE id = $1)
	`, job.ID).Scan(&rawEvents, &rawInserted); err != nil {
		t.Fatalf("query terminal raw persistence: %v", err)
	}
	if rawEvents != 0 || rawInserted != 0 {
		t.Fatalf(
			"terminal raw persistence = events %d counter %d, want 0/0",
			rawEvents,
			rawInserted,
		)
	}
}

func TestPostgresRepositoryConvergesIndependentSourcesOnSharedDOI(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	doi := "10.1000/shared-work"
	publishedAt := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)

	openAlex := repositoryEnvelope(t, "W101", doi, publishedAt.Add(2*time.Hour))
	pubMed := repositoryEnvelope(t, "12345678", doi, publishedAt.Add(3*time.Hour))
	pubMed.LogicalSource = source.PubMed
	pubMed.EventKey = "pubmed:12345678"
	pubMed.Record.Source = source.PubMed
	pubMed.Record.SourceRecordID = "12345678"
	pubMed.Record.Identifiers = []source.Identifier{
		{Scheme: source.IdentifierDOI, Value: doi},
		source.Identifier{Scheme: source.IdentifierPMID, Value: "12345678"},
	}
	pubMed.Record.Raw = openAlex.Record.Raw
	pubMed.Raw = openAlex.Raw

	for index, envelope := range []Envelope{openAlex, pubMed} {
		job := startRepositoryJob(t, repository, fmt.Sprintf("shared-doi-%d", index))
		raw, err := repository.PersistRaw(ctx, job.ID, envelope)
		if err != nil {
			t.Fatalf("PersistRaw(%d) error = %v", index, err)
		}
		normalized, err := repository.Normalize(ctx, job.ID, raw, "scope/v1")
		if err != nil {
			t.Fatalf("Normalize(%d) error = %v", index, err)
		}
		candidate, err := NewProjectionCandidate(
			normalized,
			source.ScopeDecision{
				Status: source.ScopeIncluded,
				Reason: "controlled_identity_present",
			},
		)
		if err != nil {
			t.Fatalf("NewProjectionCandidate(%d) error = %v", index, err)
		}
		if _, err := repository.Project(
			ctx,
			job.ID,
			candidate,
			"scope/v1",
			"projection/v1",
		); err != nil {
			t.Fatalf("Project(%d) error = %v", index, err)
		}
	}

	var works, sourceRecords, identifiers int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM works WHERE canonical_key = $1),
			(SELECT count(*)
			 FROM source_record_works
			 WHERE work_id = (SELECT id FROM works WHERE canonical_key = $1)),
			(SELECT count(*) FROM external_identifiers WHERE work_id = (
				SELECT id FROM works WHERE canonical_key = $1
			))
	`, "doi:"+doi).Scan(&works, &sourceRecords, &identifiers); err != nil {
		t.Fatalf("query converged work: %v", err)
	}
	if works != 1 || sourceRecords != 2 || identifiers != 3 {
		t.Fatalf(
			"converged counts = works %d source records %d identifiers %d, want 1/2/3",
			works,
			sourceRecords,
			identifiers,
		)
	}
}

func TestPostgresRepositoryResolvesVenueISSNAcrossStoredRoles(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()

	var existingVenueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO venues (
			venue_type,
			display_title,
			eissn
		) VALUES (
			'journal',
			'Existing JCR Venue',
			'1234-567X'
		)
		RETURNING id::text
	`).Scan(&existingVenueID); err != nil {
		t.Fatalf("insert existing Venue: %v", err)
	}

	job := startRepositoryJob(t, repository, "venue-cross-role")
	envelope := repositoryEnvelope(
		t,
		"W106",
		"10.1000/venue-cross-role",
		time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC),
	)
	envelope.Record.Venue = &source.Venue{
		DisplayName: "Source Venue",
		Type:        "journal",
		ISSN:        []string{"1234-567X"},
		ISSNDetails: []source.VenueISSN{
			{Value: "1234-567X", Type: "Print"},
		},
	}
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	candidate, err := NewProjectionCandidate(normalized, source.ScopeDecision{
		Status: source.ScopeIncluded,
		Reason: "controlled_identity_present",
	})
	if err != nil {
		t.Fatalf("NewProjectionCandidate() error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/v1",
		"projection/v1",
	); err != nil {
		t.Fatalf("Project() error = %v", err)
	}

	var workVenueID string
	if err := pool.QueryRow(ctx, `
		SELECT venue_id::text
		FROM works
		WHERE canonical_key = 'doi:10.1000/venue-cross-role'
	`).Scan(&workVenueID); err != nil {
		t.Fatalf("query projected work Venue: %v", err)
	}
	if workVenueID != existingVenueID {
		t.Fatalf("work Venue ID = %q, want existing JCR Venue %q", workVenueID, existingVenueID)
	}
	var venueCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM venues
		WHERE '1234-567X' IN (issn_l, issn, eissn)
	`).Scan(&venueCount); err != nil {
		t.Fatalf("count exact ISSN Venues: %v", err)
	}
	if venueCount != 1 {
		t.Fatalf("exact ISSN Venue count = %d, want 1", venueCount)
	}
}

func TestPostgresRepositoryRejectsAmbiguousVenueISSNAcrossStoredRoles(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()

	for _, statement := range []string{
		`INSERT INTO venues (
			venue_type,
			display_title,
			issn_l
		) VALUES (
			'journal',
			'Ambiguous Linking Venue',
			'0028-0836'
		)`,
		`INSERT INTO venues (
			venue_type,
			display_title,
			eissn
		) VALUES (
			'journal',
			'Ambiguous Electronic Venue',
			'0028-0836'
		)`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("insert ambiguous Venue: %v", err)
		}
	}

	job := startRepositoryJob(t, repository, "venue-ambiguous-cross-role")
	envelope := repositoryEnvelope(
		t,
		"W108",
		"10.1000/venue-ambiguous-cross-role",
		time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC),
	)
	envelope.Record.Venue = &source.Venue{
		DisplayName: "Ambiguous Source Venue",
		Type:        "journal",
		ISSN:        []string{"0028-0836"},
	}
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	candidate, err := NewProjectionCandidate(normalized, source.ScopeDecision{
		Status: source.ScopeIncluded,
		Reason: "controlled_identity_present",
	})
	if err != nil {
		t.Fatalf("NewProjectionCandidate() error = %v", err)
	}

	_, err = repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/v1",
		"projection/v1",
	)
	if err == nil || !strings.Contains(err.Error(), "multiple venues") {
		t.Fatalf("Project() error = %v, want ambiguous Venue rejection", err)
	}
}

func TestPostgresRepositoryScopesVenueSourceIdentifierByScheme(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()

	var crossrefVenueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO venues (
			venue_type,
			display_title,
			source_scheme,
			source_identifier
		) VALUES (
			'journal',
			'Crossref Venue',
			'crossref',
			'S123456'
		)
		RETURNING id::text
	`).Scan(&crossrefVenueID); err != nil {
		t.Fatalf("insert Crossref Venue: %v", err)
	}

	job := startRepositoryJob(t, repository, "venue-source-scheme")
	envelope := repositoryEnvelope(
		t,
		"W107",
		"10.1000/venue-source-scheme",
		time.Date(2026, time.July, 16, 13, 0, 0, 0, time.UTC),
	)
	envelope.Record.Venue = &source.Venue{
		OpenAlexID:  "S123456",
		DisplayName: "OpenAlex Venue",
		Type:        "journal",
	}
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	candidate, err := NewProjectionCandidate(normalized, source.ScopeDecision{
		Status: source.ScopeIncluded,
		Reason: "controlled_identity_present",
	})
	if err != nil {
		t.Fatalf("NewProjectionCandidate() error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/v1",
		"projection/v1",
	); err != nil {
		t.Fatalf("Project() error = %v", err)
	}

	var workVenueID string
	if err := pool.QueryRow(ctx, `
		SELECT venue_id::text
		FROM works
		WHERE canonical_key = 'doi:10.1000/venue-source-scheme'
	`).Scan(&workVenueID); err != nil {
		t.Fatalf("query projected work Venue: %v", err)
	}
	if workVenueID == crossrefVenueID {
		t.Fatalf("OpenAlex Venue incorrectly reused Crossref Venue %q", crossrefVenueID)
	}
}

func TestPostgresRepositoryRejectsConflictingProjectionAssertionReplay(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	job := startRepositoryJob(t, repository, "projection-replay-conflict")
	envelope := repositoryEnvelope(
		t,
		"W101999",
		"10.1000/projection-replay-conflict",
		time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC),
	)
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	decision := source.ScopeDecision{
		Status: source.ScopeIncluded,
		Reason: "controlled_identity_present",
	}
	first, err := NewProjectionCandidate(normalized, decision)
	if err != nil {
		t.Fatalf("NewProjectionCandidate(first) error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		first,
		"scope/v1",
		"projection/v1",
	); err != nil {
		t.Fatalf("Project(first) error = %v", err)
	}

	conflictingRecord := normalized.Clone()
	conflictingRecord.Record.Title = "Conflicting replay title"
	second, err := NewProjectionCandidate(conflictingRecord, decision)
	if err != nil {
		t.Fatalf("NewProjectionCandidate(second) error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		second,
		"scope/v1",
		"projection/v1",
	); err == nil ||
		!strings.Contains(
			err.Error(),
			"projection candidate differs from immutable normalized payload",
		) {
		t.Fatalf(
			"Project(conflicting replay) error = %v, want immutable normalized payload conflict",
			err,
		)
	}
}

func TestPostgresRepositoryAcceptsSemanticallyEquivalentNormalizationReplay(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	job := startRepositoryJob(t, repository, "normalization-semantic-replay")
	envelope := repositoryEnvelope(
		t,
		"W101996",
		"10.1000/normalization-semantic-replay",
		time.Date(2026, time.July, 16, 11, 0, 0, 0, time.UTC),
	)
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	if _, err := repository.Normalize(
		ctx,
		job.ID,
		raw,
		"normalization/v1",
	); err != nil {
		t.Fatalf("Normalize(first) error = %v", err)
	}
	if _, err := repository.Normalize(
		ctx,
		job.ID,
		raw,
		"normalization/v1",
	); err != nil {
		t.Fatalf("Normalize(semantic replay) error = %v", err)
	}
}

func TestPostgresRepositoryAcceptsSemanticallyEquivalentScopeDecisionReplay(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	job := startRepositoryJob(t, repository, "scope-semantic-replay")
	envelope := repositoryEnvelope(
		t,
		"W101995",
		"10.1000/scope-semantic-replay",
		time.Date(2026, time.July, 16, 11, 15, 0, 0, time.UTC),
	)
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	decision := source.ScopeDecision{
		Status: source.ScopeExcluded,
		Reason: "fixture_out_of_scope",
		Evidence: []source.FieldEvidence{
			{Field: "title", SourcePath: "$.title"},
		},
	}
	if _, err := repository.Exclude(
		ctx,
		job.ID,
		normalized,
		decision,
		"scope/v1",
	); err != nil {
		t.Fatalf("Exclude(first) error = %v", err)
	}
	if _, err := repository.Exclude(
		ctx,
		job.ID,
		normalized,
		decision,
		"scope/v1",
	); err != nil {
		t.Fatalf("Exclude(semantic replay) error = %v", err)
	}
}

func TestPostgresRepositoryAcceptsSemanticallyEquivalentProjectionAssertionReplay(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	job := startRepositoryJob(t, repository, "projection-semantic-replay")
	envelope := repositoryEnvelope(
		t,
		"W101994",
		"10.1000/projection-semantic-replay",
		time.Date(2026, time.July, 16, 11, 30, 0, 0, time.UTC),
	)
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	candidate, err := NewProjectionCandidate(
		normalized,
		source.ScopeDecision{
			Status: source.ScopeIncluded,
			Reason: "controlled_identity_present",
		},
	)
	if err != nil {
		t.Fatalf("NewProjectionCandidate() error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/v1",
		"projection/v1",
	); err != nil {
		t.Fatalf("Project(first) error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/v1",
		"projection/v1",
	); err != nil {
		t.Fatalf("Project(semantic replay) error = %v", err)
	}
}

func TestPostgresRepositoryReplaysSameRawSnapshotUnderExplicitPolicyVersions(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	job := startRepositoryJob(t, repository, "projection-policy-replay")
	envelope := repositoryEnvelope(
		t,
		"W101998",
		"10.1000/projection-policy-replay",
		time.Date(2026, time.July, 16, 10, 30, 0, 0, time.UTC),
	)
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	decision := source.ScopeDecision{
		Status: source.ScopeIncluded,
		Reason: "controlled_identity_present",
	}
	first, err := NewProjectionCandidate(normalized, decision)
	if err != nil {
		t.Fatalf("NewProjectionCandidate(first) error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		first,
		"scope/v1",
		"projection/v1",
	); err != nil {
		t.Fatalf("Project(first) error = %v", err)
	}

	second := first.Clone()
	result, err := repository.Project(
		ctx,
		job.ID,
		second,
		"scope/v2",
		"projection/v2",
	)
	if err != nil {
		t.Fatalf("Project(policy replay) error = %v", err)
	}
	if result.Status != ProjectionStatusProjected {
		t.Errorf("policy replay status = %q, want projected", result.Status)
	}

	var (
		sourceScopePolicy, sourceProjectionPolicy string
		winnerScopePolicy, winnerProjectionPolicy string
		title                                     string
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			source_state.scope_policy_version,
			source_state.projection_policy_version,
			winner_state.scope_policy_version,
			winner_state.projection_policy_version,
			work.title
		FROM works AS work
		JOIN ingestion_source_states AS source_state
		  ON source_state.work_id = work.id
		JOIN work_projection_states AS winner_state
		  ON winner_state.work_id = work.id
		WHERE work.canonical_key = 'doi:10.1000/projection-policy-replay'
	`).Scan(
		&sourceScopePolicy,
		&sourceProjectionPolicy,
		&winnerScopePolicy,
		&winnerProjectionPolicy,
		&title,
	); err != nil {
		t.Fatalf("query replayed projection state: %v", err)
	}
	if sourceScopePolicy != "scope/v2" ||
		sourceProjectionPolicy != "projection/v2" ||
		winnerScopePolicy != "scope/v2" ||
		winnerProjectionPolicy != "projection/v2" ||
		title != normalized.Record.Title {
		t.Fatalf(
			"replayed projection = source %q/%q winner %q/%q title %q",
			sourceScopePolicy,
			sourceProjectionPolicy,
			winnerScopePolicy,
			winnerProjectionPolicy,
			title,
		)
	}
}

func TestApplyWinningProjectionRequiresMatchingScopeAndProjectionPolicies(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	job := startRepositoryJob(t, repository, "winner-policy-binding")
	envelope := repositoryEnvelope(
		t,
		"W101997",
		"10.1000/winner-policy-binding",
		time.Date(2026, time.July, 16, 10, 45, 0, 0, time.UTC),
	)
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	candidate, err := NewProjectionCandidate(
		normalized,
		source.ScopeDecision{
			Status: source.ScopeIncluded,
			Reason: "controlled_identity_present",
		},
	)
	if err != nil {
		t.Fatalf("NewProjectionCandidate() error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/v1",
		"projection/v1",
	); err != nil {
		t.Fatalf("Project() error = %v", err)
	}

	var workID string
	if err := pool.QueryRow(ctx, `
		UPDATE ingestion_source_states AS state
		SET scope_policy_version = 'scope/unmatched'
		FROM works AS work
		WHERE state.work_id = work.id
		  AND work.canonical_key = 'doi:10.1000/winner-policy-binding'
		RETURNING work.id::text
	`).Scan(&workID); err != nil {
		t.Fatalf("create mismatched source policy state: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin winner recomputation: %v", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, err := applyWinningProjection(ctx, tx, workID); err != nil {
		t.Fatalf("applyWinningProjection() error = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit winner recomputation: %v", err)
	}

	var winnerStates int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM work_projection_states
		WHERE work_id = $1
	`, workID).Scan(&winnerStates); err != nil {
		t.Fatalf("count mismatched winner states: %v", err)
	}
	if winnerStates != 0 {
		t.Fatalf("winner state count = %d, want 0 without exact policy assertion", winnerStates)
	}
}

func TestPostgresRepositoryDoesNotRegressCurrentSourceProjection(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	newerTime := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	newer := repositoryEnvelope(t, "W102", "10.1000/non-regression", newerTime)
	newer = envelopeWithTitle(t, newer, "Newer title")
	older := repositoryEnvelope(t, "W102", "10.1000/non-regression", newerTime.Add(-time.Hour))
	older = envelopeWithTitle(t, older, "Older title")
	older.TieBreakKey = "older"

	for index, envelope := range []Envelope{newer, older} {
		job := startRepositoryJob(t, repository, fmt.Sprintf("non-regression-%d", index))
		raw, err := repository.PersistRaw(ctx, job.ID, envelope)
		if err != nil {
			t.Fatalf("PersistRaw(%d) error = %v", index, err)
		}
		normalized, err := repository.Normalize(ctx, job.ID, raw, "scope/v1")
		if err != nil {
			t.Fatalf("Normalize(%d) error = %v", index, err)
		}
		candidate, err := NewProjectionCandidate(
			normalized,
			source.ScopeDecision{
				Status: source.ScopeIncluded,
				Reason: "controlled_identity_present",
			},
		)
		if err != nil {
			t.Fatalf("NewProjectionCandidate(%d) error = %v", index, err)
		}
		result, err := repository.Project(
			ctx,
			job.ID,
			candidate,
			"scope/v1",
			"projection/v1",
		)
		if err != nil {
			t.Fatalf("Project(%d) error = %v", index, err)
		}
		if index == 1 && result.Status != ProjectionStatusUnchanged {
			t.Fatalf("older projection status = %q, want unchanged", result.Status)
		}
	}

	var title string
	if err := pool.QueryRow(ctx, `
		SELECT title FROM works WHERE canonical_key = 'doi:10.1000/non-regression'
	`).Scan(&title); err != nil {
		t.Fatalf("query projected title: %v", err)
	}
	if title != "Newer title" {
		t.Fatalf("projected title = %q, want newer title", title)
	}
}

func TestPostgresRepositoryDeletionPreservesRawHistory(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	eventTime := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	envelope := repositoryEnvelope(t, "W103", "10.1000/delete-history", eventTime)
	job := startRepositoryJob(t, repository, "delete-upsert")
	raw, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw() error = %v", err)
	}
	normalized, err := repository.Normalize(ctx, job.ID, raw, "scope/v1")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	candidate, err := NewProjectionCandidate(
		normalized,
		source.ScopeDecision{
			Status: source.ScopeIncluded,
			Reason: "controlled_identity_present",
		},
	)
	if err != nil {
		t.Fatalf("NewProjectionCandidate() error = %v", err)
	}
	if _, err := repository.Project(
		ctx,
		job.ID,
		candidate,
		"scope/v1",
		"projection/v1",
	); err != nil {
		t.Fatalf("Project() error = %v", err)
	}

	deletionRaw, err := source.NewRawRecord([]byte(`{"delete":"W103"}`))
	if err != nil {
		t.Fatalf("NewRawRecord(deletion) error = %v", err)
	}
	deletion, err := NewDeletionEnvelope(
		source.OpenAlex,
		envelope.EventKey,
		eventTime.Add(time.Hour),
		"delete-1",
		2,
		deletionRaw,
	)
	if err != nil {
		t.Fatalf("NewDeletionEnvelope() error = %v", err)
	}
	deleteJob := startRepositoryJob(t, repository, "delete-event")
	persistedDeletion, err := repository.PersistDeletion(ctx, deleteJob.ID, deletion)
	if err != nil {
		t.Fatalf("PersistDeletion() error = %v", err)
	}
	result, err := repository.ApplyDeletion(
		ctx,
		deleteJob.ID,
		persistedDeletion,
		"scope/v1",
		"projection/v1",
	)
	if err != nil {
		t.Fatalf("ApplyDeletion() error = %v", err)
	}
	if result.Status != ProjectionStatusDeleted {
		t.Fatalf("deletion status = %q, want deleted", result.Status)
	}

	var rawEvents, sourceRecords int
	var deleted bool
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM ingestion_raw_events WHERE event_key = $1),
			(SELECT count(*) FROM source_records WHERE source = $2 AND source_record_id = $3),
			(SELECT is_deleted FROM ingestion_source_states WHERE logical_source = $2 AND event_key = $1)
	`, envelope.EventKey, source.OpenAlex, "W103").Scan(
		&rawEvents,
		&sourceRecords,
		&deleted,
	); err != nil {
		t.Fatalf("query deletion state: %v", err)
	}
	if rawEvents != 2 || sourceRecords != 1 || !deleted {
		t.Fatalf(
			"deletion state = raw %d source records %d deleted %v, want 2/1/true",
			rawEvents,
			sourceRecords,
			deleted,
		)
	}
}

func TestPostgresRepositoryProjectAndDeletionUseConsistentLockOrder(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	doi := "10.1000/project-deletion-lock-order"
	baseTime := time.Date(2026, time.July, 16, 7, 0, 0, 0, time.UTC)

	openAlex := repositoryEnvelope(t, "W103103", doi, baseTime.Add(2*time.Hour))
	openAlex = envelopeWithTitle(t, openAlex, "OpenAlex current winner")
	pubMed := repositoryEnvelope(t, "10310310", doi, baseTime.Add(time.Hour))
	pubMed.LogicalSource = source.PubMed
	pubMed.EventKey = "pubmed:10310310"
	pubMed.Record.Source = source.PubMed
	pubMed.Record.SourceRecordID = "10310310"
	pubMed.Record.Identifiers = []source.Identifier{
		{Scheme: source.IdentifierDOI, Value: doi},
		{Scheme: source.IdentifierPMID, Value: "10310310"},
	}
	pubMed.Record.Title = "PubMed fallback winner"
	pubMed.Record.Raw = openAlex.Record.Raw
	pubMed.Raw = openAlex.Raw

	var replayCandidate ProjectionCandidate
	var replayJob Job
	for index, envelope := range []Envelope{pubMed, openAlex} {
		job := startRepositoryJob(t, repository, fmt.Sprintf("lock-order-project-%d", index))
		raw, err := repository.PersistRaw(ctx, job.ID, envelope)
		if err != nil {
			t.Fatalf("PersistRaw(%d) error = %v", index, err)
		}
		normalized, err := repository.Normalize(ctx, job.ID, raw, "normalization/v1")
		if err != nil {
			t.Fatalf("Normalize(%d) error = %v", index, err)
		}
		candidate, err := NewProjectionCandidate(
			normalized,
			source.ScopeDecision{
				Status: source.ScopeIncluded,
				Reason: "controlled_identity_present",
			},
		)
		if err != nil {
			t.Fatalf("NewProjectionCandidate(%d) error = %v", index, err)
		}
		if _, err := repository.Project(
			ctx,
			job.ID,
			candidate,
			"scope/v1",
			"projection/v1",
		); err != nil {
			t.Fatalf("Project(%d) error = %v", index, err)
		}
		if envelope.LogicalSource == source.OpenAlex {
			replayCandidate = candidate
			replayJob = job
		}
	}
	deletionRaw, err := source.NewRawRecord([]byte(`{"delete":"W103103"}`))
	if err != nil {
		t.Fatalf("NewRawRecord(deletion) error = %v", err)
	}
	deletionEnvelope, err := NewDeletionEnvelope(
		source.OpenAlex,
		openAlex.EventKey,
		baseTime.Add(3*time.Hour),
		"delete-lock-order",
		2,
		deletionRaw,
	)
	if err != nil {
		t.Fatalf("NewDeletionEnvelope() error = %v", err)
	}
	deletionJob := startRepositoryJob(t, repository, "lock-order-deletion")
	persistedDeletion, err := repository.PersistDeletion(
		ctx,
		deletionJob.ID,
		deletionEnvelope,
	)
	if err != nil {
		t.Fatalf("PersistDeletion() error = %v", err)
	}

	const gateKey int64 = 701603103
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION pause_lock_order_project() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			IF current_setting('application_name') = 'ingestion-lock-order-project'
			   AND NEW.projection_policy_version = 'projection/v2' THEN
				PERFORM pg_advisory_xact_lock(701603103);
			END IF;
			RETURN NEW;
		END;
		$$;

		CREATE TRIGGER pause_lock_order_project
		AFTER INSERT ON ingestion_projection_assertions
		FOR EACH ROW
		EXECUTE FUNCTION pause_lock_order_project();
	`); err != nil {
		t.Fatalf("install lock-order test trigger: %v", err)
	}

	gateConnection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire lock-order gate connection: %v", err)
	}
	var unlockGateOnce sync.Once
	unlockGate := func() {
		unlockGateOnce.Do(func() {
			if _, unlockErr := gateConnection.Exec(
				context.Background(),
				"SELECT pg_advisory_unlock($1)",
				gateKey,
			); unlockErr != nil {
				t.Errorf("release lock-order gate: %v", unlockErr)
			}
		})
	}
	defer func() {
		unlockGate()
		gateConnection.Release()
	}()
	if _, err := gateConnection.Exec(
		ctx,
		"SELECT pg_advisory_lock($1)",
		gateKey,
	); err != nil {
		t.Fatalf("acquire lock-order gate: %v", err)
	}

	projectPool := openNamedIngestionTestPool(t, pool, "ingestion-lock-order-project")
	deletionPool := openNamedIngestionTestPool(t, pool, "ingestion-lock-order-deletion")
	projectRepository := mustPostgresRepository(t, projectPool)
	deletionRepository := mustPostgresRepository(t, deletionPool)
	operationContext, cancelOperations := context.WithTimeout(ctx, 10*time.Second)
	defer cancelOperations()
	projectDone := make(chan error, 1)
	deletionDone := make(chan error, 1)

	go func() {
		_, projectErr := projectRepository.Project(
			operationContext,
			replayJob.ID,
			replayCandidate,
			"scope/v2",
			"projection/v2",
		)
		projectDone <- projectErr
	}()
	waitForPostgresApplicationLock(
		t,
		pool,
		"ingestion-lock-order-project",
		5*time.Second,
	)

	go func() {
		_, deletionErr := deletionRepository.ApplyDeletion(
			operationContext,
			deletionJob.ID,
			persistedDeletion,
			"scope/v2",
			"projection/v2",
		)
		deletionDone <- deletionErr
	}()
	waitForPostgresApplicationLock(
		t,
		pool,
		"ingestion-lock-order-deletion",
		5*time.Second,
	)

	unlockGate()
	projectErr := <-projectDone
	deletionErr := <-deletionDone
	if projectErr != nil || deletionErr != nil {
		t.Fatalf(
			"concurrent Project/ApplyDeletion errors = project %v deletion %v",
			projectErr,
			deletionErr,
		)
	}
}

func TestPostgresRepositoryPersistsCitationMetricsAndNeverRevivesRetractedWork(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	baseTime := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC)

	retracted := repositoryEnvelope(t, "W104", "10.1000/terminal-status", baseTime)
	retractedValue := true
	citations := 17
	retracted.Record.Retracted = &retractedValue
	retracted.Record.CitedByCount = &citations
	retracted = envelopeWithTitle(t, retracted, "Retracted source assertion")

	active := repositoryEnvelope(t, "W104", "10.1000/terminal-status", baseTime.Add(time.Hour))
	activeValue := false
	active.Record.Retracted = &activeValue
	active = envelopeWithTitle(t, active, "Later active source assertion")

	for index, envelope := range []Envelope{retracted, active} {
		job := startRepositoryJob(t, repository, fmt.Sprintf("terminal-status-%d", index))
		raw, err := repository.PersistRaw(ctx, job.ID, envelope)
		if err != nil {
			t.Fatalf("PersistRaw(%d) error = %v", index, err)
		}
		normalized, err := repository.Normalize(ctx, job.ID, raw, "scope/v1")
		if err != nil {
			t.Fatalf("Normalize(%d) error = %v", index, err)
		}
		candidate, err := NewProjectionCandidate(
			normalized,
			source.ScopeDecision{
				Status: source.ScopeIncluded,
				Reason: "controlled_identity_present",
			},
		)
		if err != nil {
			t.Fatalf("NewProjectionCandidate(%d) error = %v", index, err)
		}
		if _, err := repository.Project(
			ctx,
			job.ID,
			candidate,
			"scope/v1",
			"projection/v1",
		); err != nil {
			t.Fatalf("Project(%d) error = %v", index, err)
		}
	}

	var status string
	var metricValue int
	if err := pool.QueryRow(ctx, `
		SELECT
			work.status,
			metric.metric_value::integer
		FROM works AS work
		JOIN metric_snapshots AS metric
		  ON metric.work_id = work.id
		 AND metric.metric_name = 'citation_count'
		WHERE work.canonical_key = 'doi:10.1000/terminal-status'
		ORDER BY metric.observed_at
		LIMIT 1
	`).Scan(&status, &metricValue); err != nil {
		t.Fatalf("query terminal work metric: %v", err)
	}
	if status != "retracted" || metricValue != citations {
		t.Fatalf(
			"terminal work = status %q metric %d, want retracted/%d",
			status,
			metricValue,
			citations,
		)
	}
}

func mustPostgresRepository(
	t *testing.T,
	pool *pgxpool.Pool,
) *PostgresRepository {
	t.Helper()
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatalf("NewPostgresRepository() error = %v", err)
	}
	return repository
}

func startRepositoryJob(
	t *testing.T,
	repository *PostgresRepository,
	suffix string,
) Job {
	t.Helper()
	job, err := NewJob(
		"sync/openalex/"+suffix,
		source.OpenAlex,
		"test:"+suffix,
		map[string]any{"test": suffix},
	)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	started, err := repository.Start(context.Background(), job)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return started
}

func startPubMedRepositoryJob(
	t *testing.T,
	repository *PostgresRepository,
	suffix string,
) Job {
	t.Helper()
	job, err := NewJob(
		"sync/pubmed/"+suffix,
		source.PubMed,
		"test:pubmed:"+suffix,
		map[string]any{"test": suffix},
	)
	if err != nil {
		t.Fatalf("NewJob(PubMed) error = %v", err)
	}
	started, err := repository.Start(context.Background(), job)
	if err != nil {
		t.Fatalf("Start(PubMed) error = %v", err)
	}
	return started
}

func assertJSONOmitsKeys(t *testing.T, payload []byte, keys ...string) {
	t.Helper()
	forbidden := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		forbidden[key] = struct{}{}
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode JSON key assertion payload: %v", err)
	}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, nested := range typed {
				if _, found := forbidden[key]; found {
					t.Fatalf("JSON payload contains forbidden key %q", key)
				}
				walk(nested)
			}
		case []any:
			for _, nested := range typed {
				walk(nested)
			}
		}
	}
	walk(decoded)
}

func pubMedBiomedicalRepositoryEnvelope(
	t *testing.T,
	pmid string,
	doi string,
	sourceTime time.Time,
) Envelope {
	t.Helper()
	return pubMedBiomedicalRepositoryEnvelopeFromXML(
		t,
		pmid,
		doi,
		sourceTime,
		"Neoplasms",
		"default",
	)
}

func pubMedBiomedicalRepositoryEnvelopeFromXML(
	t *testing.T,
	pmid string,
	doi string,
	sourceTime time.Time,
	descriptorLabel string,
	titleMarker string,
) Envelope {
	t.Helper()
	record, err := pubmed.ParseRecord([]byte(fmt.Sprintf(`
<PubmedArticle Status="MEDLINE">
  <MedlineCitation Status="MEDLINE" Owner="NLM">
    <PMID Version="1">%s</PMID>
    <DateCompleted>
      <Year>2026</Year><Month>07</Month><Day>16</Day>
    </DateCompleted>
    <DateRevised>
      <Year>2026</Year><Month>07</Month><Day>17</Day>
    </DateRevised>
    <Article PubModel="Print-Electronic">
      <Journal>
        <ISSN IssnType="Print">0028-0836</ISSN>
        <ISSN IssnType="Electronic">2049-3630</ISSN>
        <JournalIssue CitedMedium="Internet">
          <PubDate>
            <Year>2026</Year><Month>07</Month><Day>15</Day>
          </PubDate>
        </JournalIssue>
        <Title>Journal of Biomedical Evidence</Title>
        <ISOAbbreviation>J Biomed Evid</ISOAbbreviation>
      </Journal>
      <ArticleTitle>Biomedical repository paper %s</ArticleTitle>
      <Abstract>
        <AbstractText Label="BACKGROUND" NlmCategory="BACKGROUND">Tumor response was evaluated.</AbstractText>
        <AbstractText Label="METHODS" NlmCategory="METHODS">The randomized trial improved survival.</AbstractText>
      </Abstract>
      <PublicationTypeList>
        <PublicationType UI="D016428">Journal Article</PublicationType>
        <PublicationType UI="D016449">Randomized Controlled Trial</PublicationType>
        <PublicationType UI="D017418">Meta-Analysis</PublicationType>
      </PublicationTypeList>
      <ArticleDate DateType="Electronic">
        <Year>2026</Year><Month>07</Month><Day>14</Day>
      </ArticleDate>
      <ELocationID EIdType="doi" ValidYN="Y">%s</ELocationID>
    </Article>
    <MedlineJournalInfo>
      <Country>United States</Country>
      <MedlineTA>J Biomed Evid</MedlineTA>
      <NlmUniqueID>7654321</NlmUniqueID>
      <ISSNLinking>0028-0836</ISSNLinking>
    </MedlineJournalInfo>
    <CommentsCorrectionsList>
      <CommentsCorrections RefType="CommentIn">
        <RefSource>Commented on by</RefSource><PMID>76543211</PMID>
      </CommentsCorrections>
      <CommentsCorrections RefType="ErratumIn">
        <RefSource>Corrected by</RefSource><PMID>76543212</PMID>
      </CommentsCorrections>
    </CommentsCorrectionsList>
    <MeshHeadingList>
      <MeshHeading>
        <DescriptorName UI="D009369" MajorTopicYN="Y">%s</DescriptorName>
        <QualifierName UI="Q000628" MajorTopicYN="Y">therapy</QualifierName>
        <QualifierName UI="Q000235" MajorTopicYN="N">genetics</QualifierName>
      </MeshHeading>
      <MeshHeading>
        <DescriptorName UI="D001943" MajorTopicYN="N">Breast Neoplasms</DescriptorName>
        <QualifierName UI="Q000503" MajorTopicYN="Y">pathology</QualifierName>
      </MeshHeading>
    </MeshHeadingList>
  </MedlineCitation>
  <PubmedData>
    <ArticleIdList>
      <ArticleId IdType="pubmed">%s</ArticleId>
      <ArticleId IdType="pmc">PMC%s</ArticleId>
      <ArticleId IdType="doi">%s</ArticleId>
    </ArticleIdList>
  </PubmedData>
</PubmedArticle>`,
		pmid,
		titleMarker,
		doi,
		descriptorLabel,
		pmid,
		pmid,
		doi,
	)))
	if err != nil {
		t.Fatalf("pubmed.ParseRecord() error = %v", err)
	}
	keywordScore := 0.97
	// PubMed ParseRecord does not parse KeywordList. This keyword is deliberate
	// generic normalized-payload data and has no PubMed semantic source_path.
	record.Keywords = []source.Keyword{
		{
			DisplayName: "immune checkpoint blockade",
			Score:       &keywordScore,
		},
	}
	envelope, err := NewEnvelope(
		source.PubMed,
		"pubmed:"+pmid,
		sourceTime,
		"pubmed-record-1",
		1,
		record,
		record.Raw,
	)
	if err != nil {
		t.Fatalf("NewEnvelope(PubMed) error = %v", err)
	}
	return envelope
}

func pubMedEnvelopeWithDescriptorLabel(
	t *testing.T,
	envelope Envelope,
	label string,
	rawMarker string,
) Envelope {
	t.Helper()
	return pubMedBiomedicalRepositoryEnvelopeFromXML(
		t,
		envelope.Record.SourceRecordID,
		envelope.Record.Identity.Value(),
		envelope.SourceTime,
		label,
		rawMarker,
	)
}

func repositoryEnvelope(
	t *testing.T,
	openAlexID string,
	doi string,
	sourceTime time.Time,
) Envelope {
	t.Helper()
	raw, err := source.NewRawRecord([]byte(fmt.Sprintf(
		`{"doi":%q,"id":%q,"title":"Repository paper"}`,
		doi,
		openAlexID,
	)))
	if err != nil {
		t.Fatalf("NewRawRecord() error = %v", err)
	}
	identity, err := paper.NewIdentifier(paper.SchemeDOI, doi)
	if err != nil {
		t.Fatalf("NewIdentifier(DOI) error = %v", err)
	}
	publishedAt := sourceTime.Add(-24 * time.Hour)
	record := source.Record{
		Source:         source.OpenAlex,
		SourceRecordID: openAlexID,
		Identity:       identity,
		Identifiers: []source.Identifier{
			{Scheme: source.IdentifierDOI, Value: doi},
			{Scheme: source.IdentifierOpenAlex, Value: openAlexID},
		},
		Raw:         raw,
		Title:       "Repository paper",
		Abstract:    "A deterministic repository integration fixture.",
		PublishedAt: &publishedAt,
		Scope: source.ScopeDecision{
			Status: source.ScopePending,
			Reason: source.ScopeReasonAwaitingDeterministicEvaluation,
		},
	}
	envelope, err := NewEnvelope(
		source.OpenAlex,
		"openalex:"+openAlexID,
		sourceTime,
		"record-1",
		1,
		record,
		raw,
	)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	return envelope
}

func envelopeWithTitle(t *testing.T, envelope Envelope, title string) Envelope {
	t.Helper()
	raw, err := source.NewRawRecord([]byte(fmt.Sprintf(
		`{"doi":%q,"id":%q,"title":%q}`,
		envelope.Record.Identity.Value(),
		envelope.Record.SourceRecordID,
		title,
	)))
	if err != nil {
		t.Fatalf("NewRawRecord(title) error = %v", err)
	}
	envelope.Record.Title = title
	envelope.Record.Raw = raw
	envelope.Raw = raw
	if err := envelope.Validate(); err != nil {
		t.Fatalf("title envelope validation error = %v", err)
	}
	return envelope
}

type postgresRepositoryTerminalCase struct {
	name     string
	status   JobStatus
	terminal func(Job) (Job, error)
	persist  func(context.Context, *PostgresRepository, Job) error
}

func postgresRepositoryTerminalCases() []postgresRepositoryTerminalCase {
	return []postgresRepositoryTerminalCase{
		{
			name:     "complete",
			status:   JobStatusSucceeded,
			terminal: Job.Succeed,
			persist: func(
				ctx context.Context,
				repository *PostgresRepository,
				job Job,
			) error {
				return repository.Complete(ctx, job)
			},
		},
		{
			name:     "fail",
			status:   JobStatusFailed,
			terminal: Job.Fail,
			persist: func(
				ctx context.Context,
				repository *PostgresRepository,
				job Job,
			) error {
				return repository.Fail(
					ctx,
					job,
					errors.New("deterministic terminal race fixture"),
				)
			},
		},
	}
}

type postgresRepositoryStartResult struct {
	job Job
	err error
}

type postgresQueryBarrierContextKey struct{}

type postgresQueryBarrierTracer struct {
	match           func(string) bool
	releaseQueryEnd <-chan struct{}
	queryStarted    chan struct{}
	queryEnded      chan struct{}
	startOnce       sync.Once
	endOnce         sync.Once
}

func newPostgresQueryBarrierTracer(
	match func(string) bool,
	releaseQueryEnd <-chan struct{},
) *postgresQueryBarrierTracer {
	return &postgresQueryBarrierTracer{
		match:           match,
		releaseQueryEnd: releaseQueryEnd,
		queryStarted:    make(chan struct{}),
		queryEnded:      make(chan struct{}),
	}
}

func (tracer *postgresQueryBarrierTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if tracer == nil || tracer.match == nil || !tracer.match(data.SQL) {
		return ctx
	}
	tracer.startOnce.Do(func() {
		close(tracer.queryStarted)
	})
	return context.WithValue(ctx, postgresQueryBarrierContextKey{}, tracer)
}

func (tracer *postgresQueryBarrierTracer) TraceQueryEnd(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceQueryEndData,
) {
	if ctx.Value(postgresQueryBarrierContextKey{}) != tracer {
		return
	}
	tracer.endOnce.Do(func() {
		close(tracer.queryEnded)
	})
	if tracer.releaseQueryEnd == nil {
		return
	}
	select {
	case <-tracer.releaseQueryEnd:
	case <-ctx.Done():
	}
}

func openTracedIngestionTestPool(
	t *testing.T,
	pool *pgxpool.Pool,
	tracer pgx.QueryTracer,
) *pgxpool.Pool {
	t.Helper()
	config := pool.Config()
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1
	tracedPool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open traced PostgreSQL pool: %v", err)
	}
	t.Cleanup(tracedPool.Close)
	return tracedPool
}

func waitForPostgresTraceSignal(
	t *testing.T,
	ctx context.Context,
	signal <-chan struct{},
	description string,
) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("wait for %s: %v", description, ctx.Err())
	}
}

func waitForPostgresStartResult(
	t *testing.T,
	ctx context.Context,
	results <-chan postgresRepositoryStartResult,
) postgresRepositoryStartResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-ctx.Done():
		t.Fatalf("wait for ingestion Start result: %v", ctx.Err())
		return postgresRepositoryStartResult{}
	}
}

func openNamedIngestionTestPool(
	t *testing.T,
	pool *pgxpool.Pool,
	applicationName string,
) *pgxpool.Pool {
	t.Helper()
	config := pool.Config()
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	namedPool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open named PostgreSQL pool %q: %v", applicationName, err)
	}
	t.Cleanup(namedPool.Close)
	return namedPool
}

func waitForPostgresApplicationLock(
	t *testing.T,
	pool *pgxpool.Pool,
	applicationName string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var waiting bool
		if err := pool.QueryRow(context.Background(), `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE application_name = $1
				  AND wait_event_type = 'Lock'
			)
		`, applicationName).Scan(&waiting); err != nil {
			t.Fatalf("inspect PostgreSQL lock wait for %q: %v", applicationName, err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("application %q did not reach a PostgreSQL lock wait", applicationName)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func openIngestionTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	postgresRepositoryContainerOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		postgresRepositoryContainer, postgresRepositoryContainerErr = postgres.Run(
			ctx,
			"postgres:18-alpine",
			postgres.WithDatabase("paper_hub"),
			postgres.WithUsername("paper_hub"),
			postgres.WithPassword("paper_hub"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(time.Minute),
			),
		)
		if postgresRepositoryContainerErr != nil {
			return
		}
		postgresRepositoryDatabaseURL, postgresRepositoryContainerErr =
			postgresRepositoryContainer.ConnectionString(ctx, "sslmode=disable")
	})
	if postgresRepositoryContainerErr != nil {
		t.Fatalf("start PostgreSQL test container: %v", postgresRepositoryContainerErr)
	}

	admin, err := pgxpool.New(context.Background(), postgresRepositoryDatabaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL admin pool: %v", err)
	}
	t.Cleanup(admin.Close)

	schema := "ingestion_test_" + strings.ReplaceAll(
		strings.ToLower(t.Name()),
		"/",
		"_",
	)
	schema = strings.NewReplacer(" ", "_", "-", "_").Replace(schema)
	if len(schema) > 55 {
		schema = schema[:55]
	}
	if _, err := admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		t.Fatalf("drop test schema: %v", err)
	}
	if _, err := admin.Exec(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}

	config, err := pgxpool.ParseConfig(postgresRepositoryDatabaseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL config: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open schema PostgreSQL pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	if err := database.Up(context.Background(), pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return pool
}

func TestMain(m *testing.M) {
	code := m.Run()
	if postgresRepositoryContainer != nil {
		_ = testcontainers.TerminateContainer(postgresRepositoryContainer)
	}
	os.Exit(code)
}
