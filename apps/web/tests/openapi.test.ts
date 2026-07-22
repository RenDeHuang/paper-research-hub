// @vitest-environment node

import { existsSync, readFileSync } from "node:fs"
import { spawnSync } from "node:child_process"
import { fileURLToPath } from "node:url"

import ts from "typescript"
import { describe, expect, it } from "vitest"
import { parse } from "yaml"

const requestIDParameterRef = "#/components/parameters/RequestID"
const discoveryResponseRef = "#/components/schemas/DiscoveryResponse"
const journalReferenceRef = "#/components/schemas/JournalReference"
const stringCatalogValueRef = "#/components/schemas/StringCatalogValue"
const taxonomyItemRef = "#/components/schemas/TaxonomyItem"
const taxonomyReferenceRef = "#/components/schemas/TaxonomyReference"
const taxonomySlugRef = "#/components/schemas/TaxonomySlug"
const openAPIPath = fileURLToPath(
  new URL("../../../contracts/openapi.yaml", import.meta.url),
)
const generatedTypesPath = fileURLToPath(
  new URL("../app/types/openapi.generated.ts", import.meta.url),
)
const generatorPath = fileURLToPath(
  new URL("../scripts/generate-openapi-types.mjs", import.meta.url),
)
const catalogTypesPath = fileURLToPath(
  new URL("../app/types/catalog.ts", import.meta.url),
)
const biomedicalTypesPath = fileURLToPath(
  new URL("../app/types/biomedical.ts", import.meta.url),
)
const document = parse(readFileSync(openAPIPath, "utf8")) as unknown

describe("OpenAPI contract", () => {
  it("parses and resolves every local reference", () => {
    expect(isRecord(document)).toBe(true)
    visitReferences(document, (reference) => {
      expect(() => resolveLocalReference(document, reference)).not.toThrow()
    })
  })

  it("reuses the strict Request ID header parameter on every public operation", () => {
    const parameter = getRecord(document, "components", "parameters", "RequestID")
    expect(parameter).toMatchObject({
      name: "X-Request-ID",
      in: "header",
      required: false,
    })

    const schema = resolveSchema(document, parameter.schema)
    expect(schema).toMatchObject({
      type: "string",
      minLength: 1,
      maxLength: 255,
      pattern: "^[A-Za-z0-9._:-]{1,255}$",
    })

    for (const { method, operation, path } of publicOperations(document)) {
      expect(
        operation.parameters,
        `${method.toUpperCase()} ${path} must reference RequestID`,
      ).toContainEqual({
        $ref: requestIDParameterRef,
      })
    }
  })

  it("documents only the implemented GET root discovery contract", () => {
    const rootPath = getRecord(document, "paths", "/")
    expect(Object.keys(rootPath)).toEqual(["get"])

    const operation = getRecord(rootPath, "get")
    expect(operation).toMatchObject({
      operationId: "getDiscovery",
      parameters: [
        {
          $ref: requestIDParameterRef,
        },
      ],
    })

    const responses = getRecord(operation, "responses")
    expect(Object.keys(responses)).toEqual(["200", "400", "405", "default"])
    expect(
      getRecord(responses, "200", "headers", "X-Request-ID"),
    ).toEqual({
      $ref: "#/components/headers/XRequestID",
    })
    expect(responses["400"]).toEqual({
      $ref: "#/components/responses/InvalidRequestIDProblem",
    })
    expect(responses["405"]).toEqual({
      $ref: "#/components/responses/MethodNotAllowedProblem",
    })
    expect(responses.default).toEqual({
      $ref: "#/components/responses/InternalProblem",
    })

    expect(
      getRecord(operation, "responses", "200", "content", "application/json"),
    ).toEqual({
      schema: {
        $ref: discoveryResponseRef,
      },
    })

    expect(
      getRecord(document, "components", "schemas", "DiscoveryResponse"),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: ["service", "status", "api_version", "health"],
      properties: {
        service: {
          type: "string",
          const: "medpaperhub-api",
        },
        status: {
          type: "string",
          const: "ok",
        },
        api_version: {
          type: "string",
          const: "v1",
        },
        health: {
          type: "string",
          const: "/health",
        },
      },
    })
  })

  it("publishes the medpaperhub API health identity", () => {
    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "HealthResponse",
        "properties",
        "service",
      ),
    ).toEqual({
      type: "string",
      const: "medpaperhub-api",
    })
  })

  it("reuses TaxonomySlug and TaxonomyItem without schema drift", () => {
    expect(
      getRecord(document, "components", "schemas", "TaxonomySlug"),
    ).toEqual({
      type: "string",
      minLength: 1,
      maxLength: 120,
      pattern: "^[a-z0-9]+(?:-[a-z0-9]+)*$",
    })

    const taxonomyItem = getRecord(
      document,
      "components",
      "schemas",
      "TaxonomyItem",
    )
    expect(getRecord(taxonomyItem, "properties", "slug")).toEqual({
      $ref: taxonomySlugRef,
    })

    for (const parameterName of ["Slug", "TopicFilter", "MethodFilter"]) {
      expect(
        getRecord(
          document,
          "components",
          "parameters",
          parameterName,
          "schema",
        ),
      ).toEqual({
        $ref: taxonomySlugRef,
      })
    }

    for (const schemaName of ["Topic", "Method"]) {
      expect(
        getRecord(document, "components", "schemas", schemaName),
      ).toEqual({
        $ref: taxonomyItemRef,
      })
    }
  })

  it("declares biomedical paper summary fields in the public contract", () => {
    const paperSummary = getRecord(
      document,
      "components",
      "schemas",
      "PaperSummary",
    )
    expect(paperSummary).toMatchObject({
      type: "object",
      additionalProperties: false,
      required: expect.arrayContaining([
        "citation_source",
        "citation_snapshots",
        "citation_velocity",
        "citation_percentile",
      ]),
    })
    expect(getRecord(paperSummary, "properties")).toMatchObject({
      abstract_snippet: {
        $ref: stringCatalogValueRef,
      },
      citation_analysis_evidence: {
        $ref: "#/components/schemas/CitationAnalysisEvidenceCatalogValue",
      },
      citation_percentile: {
        $ref: "#/components/schemas/CitationPercentileCatalogValue",
      },
      citation_snapshots: {
        $ref: "#/components/schemas/CitationSnapshotsCatalogValue",
      },
      citation_source: {
        $ref: stringCatalogValueRef,
      },
      citation_velocity: {
        $ref: "#/components/schemas/CitationVelocityCatalogValue",
      },
      journal: {
        $ref: journalReferenceRef,
      },
      publication_types: {
        type: "array",
        items: {
          type: "string",
          minLength: 1,
        },
      },
      subjects: {
        type: "array",
        items: {
          $ref: taxonomyReferenceRef,
        },
      },
    })

    expect(
      getRecord(document, "components", "schemas", "JournalReference"),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: ["id", "slug", "title"],
      properties: {
        id: {
          type: "string",
          format: "uuid",
        },
        slug: {
          $ref: taxonomySlugRef,
        },
        title: {
          type: "string",
          minLength: 1,
        },
      },
    })
  })

  it("allows Facts Home scope fields to report missing instead of fabricated JCR values", () => {
    const missingRef = {
      $ref: "#/components/schemas/MissingCatalogValue",
    }
    const yearOrMissing = {
      oneOf: [
        {
          type: "integer",
          minimum: 1900,
          maximum: 3000,
        },
        missingRef,
      ],
    }
    const stringOrMissing = {
      oneOf: [
        {
          type: "string",
          minLength: 1,
        },
        missingRef,
      ],
    }

    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "HomeCoverage",
        "properties",
        "jcr_metric_year",
      ),
    ).toEqual(yearOrMissing)
    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "HomeCoverage",
        "properties",
        "taxonomy_version",
      ),
    ).toEqual(stringOrMissing)
    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "HomeResponse",
        "properties",
        "scope",
        "properties",
        "jcr_metric_year",
      ),
    ).toEqual(yearOrMissing)
    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "HomeResponse",
        "properties",
        "scope",
        "properties",
        "taxonomy_version",
      ),
    ).toEqual(stringOrMissing)
  })

  it("declares every biomedical field emitted by the paper publisher", () => {
    const emittedFields = {
      mesh_headings:
        "#/components/schemas/MeshHeadingsCatalogValue",
      publication_types_state:
        "#/components/schemas/PublicationTypesCatalogValue",
      jcr_assessment:
        "#/components/schemas/BiomedicalEligibilityRevisionCatalogValue",
      article_usage:
        "#/components/schemas/MissingCatalogValue",
      open_fulltext:
        "#/components/schemas/MissingCatalogValue",
    }

    for (const schemaName of ["PaperSummary", "PaperDetail"]) {
      const paper = getRecord(
        document,
        "components",
        "schemas",
        schemaName,
      )
      expect(paper).toMatchObject({
        type: "object",
        additionalProperties: false,
        required: expect.arrayContaining(Object.keys(emittedFields)),
      })

      const properties = getRecord(paper, "properties")
      for (const [field, reference] of Object.entries(emittedFields)) {
        expect(properties[field], `${schemaName}.${field}`).toEqual({
          $ref: reference,
        })
      }
    }
  })

  it("defines the strict verified official link contract", () => {
    expect(
      getRecord(document, "components", "schemas", "OfficialLink"),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: [
        "url",
        "verification_id",
        "link_role",
        "content_channel",
        "verified_at",
        "expires_at",
        "verifier_version",
        "policy_version",
      ],
      properties: {
        url: {
          type: "string",
          format: "uri",
          pattern: "^https://",
        },
        verification_id: {
          type: "string",
          format: "uuid",
        },
        link_role: {
          type: "string",
          enum: [
            "official_article",
            "doi_url",
            "official_preprint",
            "official_proceeding",
          ],
        },
        content_channel: {
          type: "string",
          enum: [
            "journal_published",
            "accepted_early",
            "preprint",
            "conference_proceeding",
          ],
        },
        verified_at: {
          type: "string",
          format: "date-time",
        },
        expires_at: {
          type: "string",
          format: "date-time",
        },
        verifier_version: {
          type: "string",
          minLength: 1,
        },
        policy_version: {
          type: "string",
          minLength: 1,
        },
      },
    })
  })

  it("requires server-provided visibility state on paper payloads", () => {
    for (const schemaName of ["PaperSummary", "PaperDetail"]) {
      const paper = getRecord(
        document,
        "components",
        "schemas",
        schemaName,
      )
      expect(paper).toMatchObject({
        type: "object",
        additionalProperties: false,
        required: expect.arrayContaining([
          "official_link",
          "publicly_visible",
          "analysis_ready",
        ]),
        properties: {
          official_link: {
            $ref: "#/components/schemas/OfficialLink",
          },
          publicly_visible: {
            type: "boolean",
          },
          analysis_ready: {
            type: "boolean",
          },
        },
      })
    }
  })

  it("requires explicit taxonomy readiness on paper payloads", () => {
    for (const schemaName of ["PaperSummary", "PaperDetail"]) {
      const paper = getRecord(
        document,
        "components",
        "schemas",
        schemaName,
      )
      expect(paper).toMatchObject({
        required: expect.arrayContaining([
          "topics_state",
          "methods_state",
        ]),
        properties: {
          topics_state: {
            type: "string",
            enum: ["known", "missing", "not_ready"],
          },
          methods_state: {
            type: "string",
            enum: ["known", "missing", "not_ready"],
          },
        },
      })
    }
  })

  it("constrains public trend scores to normalized ratios", () => {
    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "TrendItem",
        "properties",
        "score",
      ),
    ).toEqual({
      type: "number",
      minimum: 0,
      maximum: 1,
    })
  })

  it("declares citation evidence on summary and detail payloads", () => {
    for (const schemaName of ["PaperSummary", "PaperDetail"]) {
      const paper = getRecord(
        document,
        "components",
        "schemas",
        schemaName,
      )
      expect(paper).toMatchObject({
        type: "object",
        additionalProperties: false,
        required: expect.arrayContaining([
          "citation_source",
          "citation_snapshots",
          "citation_velocity",
          "citation_percentile",
        ]),
        properties: {
          citation_analysis_evidence: {
            $ref: "#/components/schemas/CitationAnalysisEvidenceCatalogValue",
          },
          citation_percentile: {
            $ref: "#/components/schemas/CitationPercentileCatalogValue",
          },
          citation_source: {
            $ref: stringCatalogValueRef,
          },
          citation_snapshots: {
            $ref: "#/components/schemas/CitationSnapshotsCatalogValue",
          },
          citation_velocity: {
            $ref: "#/components/schemas/CitationVelocityCatalogValue",
          },
        },
      })
    }

    expect(
      getRecord(document, "components", "schemas", "CitationSnapshot"),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: [
        "source",
        "observed_at",
        "count",
        "source_record_id",
        "ingestion_job_id",
        "retrieved_at",
        "coverage",
        "definition_version",
        "dataset_version",
      ],
      properties: {
        source: {
          type: "string",
          minLength: 1,
        },
        observed_at: {
          type: "string",
          format: "date-time",
        },
        count: {
          type: "integer",
          minimum: 0,
        },
        source_record_id: {
          type: "string",
          format: "uuid",
        },
        ingestion_job_id: {
          type: "string",
          format: "uuid",
        },
        retrieved_at: {
          type: "string",
          format: "date-time",
        },
        coverage: {
          type: "number",
          minimum: 0,
          maximum: 1,
        },
        definition_version: {
          type: "string",
          minLength: 1,
        },
        dataset_version: {
          type: "string",
          minLength: 1,
        },
      },
    })

    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "CitationSnapshotsCatalogValue",
      ),
    ).toEqual({
      oneOf: [
        {
          type: "object",
          additionalProperties: false,
          required: ["state", "value"],
          properties: {
            state: {
              type: "string",
              const: "known",
            },
            value: {
              type: "array",
              minItems: 1,
              items: {
                $ref: "#/components/schemas/CitationSnapshot",
              },
            },
          },
        },
        {
          $ref: "#/components/schemas/UnknownCatalogValue",
        },
        {
          $ref: "#/components/schemas/MissingCatalogValue",
        },
      ],
    })
  })

  it("models citation analysis values and run evidence without inferred fields", () => {
    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "CitationAnalysisEvidence",
      ),
    ).toMatchObject({
      type: "object",
      additionalProperties: false,
      required: [
        "analysis_run_id",
        "source",
        "as_of",
        "generated_at",
        "formula_version",
        "source_revision",
        "velocity",
        "percentile",
      ],
      properties: {
        analysis_run_id: {
          type: "string",
          format: "uuid",
        },
        source: {
          type: "string",
          minLength: 1,
        },
        as_of: {
          type: "string",
          format: "date-time",
        },
        generated_at: {
          type: "string",
          format: "date-time",
        },
        formula_version: {
          type: "string",
          minLength: 1,
        },
        source_revision: {
          type: "string",
          pattern: "^[0-9a-f]{64}$",
        },
        velocity: {
          $ref: "#/components/schemas/CitationVelocityEvidence",
        },
        percentile: {
          $ref: "#/components/schemas/CitationPercentileEvidence",
        },
      },
    })

    for (const [schemaName, knownSchema] of [
      ["CitationVelocityCatalogValue", {
        type: "number",
      }],
      ["CitationPercentileCatalogValue", {
        type: "number",
        minimum: 0,
        maximum: 100,
      }],
    ] as const) {
      const catalogValue = getRecord(
        document,
        "components",
        "schemas",
        schemaName,
      )
      expect(catalogValue).toMatchObject({
        oneOf: expect.arrayContaining([
          expect.objectContaining({
            type: "object",
            additionalProperties: false,
            required: ["state", "value"],
            properties: {
              state: {
                type: "string",
                const: "known",
              },
              value: knownSchema,
            },
          }),
          {
            $ref: "#/components/schemas/InsufficientEvidenceCatalogValue",
          },
          {
            $ref: "#/components/schemas/UnknownCatalogValue",
          },
          {
            $ref: "#/components/schemas/MissingCatalogValue",
          },
        ]),
      })
    }
  })

  it("extends analysis metadata with run identity and cohort windows", () => {
    expect(
      getRecord(document, "components", "schemas", "AnalysisMetadata"),
    ).toMatchObject({
      type: "object",
      additionalProperties: false,
      required: [
        "coverage_ratio",
        "generated_at",
        "missing_signals",
        "sample_size",
        "sources",
        "window_days",
      ],
      properties: {
        analysis_run_id: {
          type: "string",
          format: "uuid",
        },
        analysis_type: {
          type: "string",
          minLength: 1,
        },
        baseline_window_days: {
          type: "integer",
          minimum: 0,
        },
        cohort_revision: {
          type: "string",
          pattern: "^[0-9a-f]{64}$",
        },
        recent_window_days: {
          type: "integer",
          minimum: 0,
        },
      },
    })
  })

  it("publishes trend and entity estimates with support counts and uncertainty", () => {
    for (const schemaName of ["SubjectTrendEstimate", "EntityMomentumItem"]) {
      const schema = getRecord(document, "components", "schemas", schemaName)
      expect(schema).toMatchObject({
        type: "object",
        additionalProperties: false,
        required: expect.arrayContaining([
          "baseline_count",
          "confidence_interval",
          "estimate",
          "model_family",
          "p_value",
          "adjusted_p_value",
          "recent_count",
          "independent_journal_count",
          "independent_team_count",
        ]),
        properties: {
          adjusted_p_value: {
            $ref: "#/components/schemas/NumberCatalogValue",
          },
          baseline_count: {
            $ref: "#/components/schemas/IntegerCatalogValue",
          },
          confidence_interval: {
            $ref: "#/components/schemas/ConfidenceInterval",
          },
          model_family: {
            type: "string",
            minLength: 1,
          },
          p_value: {
            $ref: "#/components/schemas/NumberCatalogValue",
          },
          recent_count: {
            $ref: "#/components/schemas/IntegerCatalogValue",
          },
        },
      })
    }
  })

  it("adds editorial interpretation metadata without causal framing", () => {
    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "EditorialPatternEstimate",
      ),
    ).toMatchObject({
      type: "object",
      additionalProperties: false,
      required: expect.arrayContaining([
        "adjusted_p_value",
        "confidence_interval",
        "coverage_ratio",
        "field_baseline_count",
        "interpretation_kind",
        "minimum_support_count",
        "p_value",
      ]),
      properties: {
        interpretation_kind: {
          type: "string",
          const: "editorial_pattern",
        },
        p_value: {
          $ref: "#/components/schemas/NumberCatalogValue",
        },
        field_baseline_count: {
          $ref: "#/components/schemas/IntegerCatalogValue",
        },
        minimum_support_count: {
          $ref: "#/components/schemas/IntegerCatalogValue",
        },
      },
    })
  })

  it("restructures research opportunities around rules, estimates and support work IDs", () => {
    const opportunity = getRecord(
      document,
      "components",
      "schemas",
      "ResearchOpportunity",
    )
    expect(opportunity).toMatchObject({
      type: "object",
      additionalProperties: false,
      required: expect.arrayContaining([
        "analysis_run_id",
        "coverage_ratio",
        "estimates",
        "formula_version",
        "generated_at",
        "id",
        "status",
        "supporting_work_ids",
        "target_id",
        "target_kind",
        "title",
        "trigger_rule",
      ]),
      properties: {
        analysis_run_id: {
          type: "string",
          format: "uuid",
        },
        coverage_ratio: {
          $ref: "#/components/schemas/RatioCatalogValue",
        },
        estimates: {
          type: "array",
          minItems: 1,
          items: {
            $ref: "#/components/schemas/ResearchOpportunityEstimate",
          },
        },
        supporting_work_ids: {
          type: "array",
          minItems: 1,
          uniqueItems: true,
          items: {
            type: "string",
            format: "uuid",
          },
        },
        target_kind: {
          type: "string",
          minLength: 1,
        },
        trigger_rule: {
          $ref: "#/components/schemas/ResearchOpportunityTriggerRule",
        },
      },
    })

    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "ResearchOpportunityListResponse",
      ),
    ).toMatchObject({
      type: "object",
      additionalProperties: false,
      required: ["analysis", "items", "pagination"],
      properties: {
        analysis: {
          $ref: "#/components/schemas/AnalysisMetadata",
        },
      },
    })
  })

  it("describes generation-bound Home publication updates", () => {
    expect(
      getRecord(document, "components", "schemas", "HomeResponse"),
    ).toMatchObject({
      type: "object",
      additionalProperties: false,
      required: expect.arrayContaining([
        "publication_updates",
        "snapshot_schema",
      ]),
      properties: {
        publication_updates: {
          $ref: "#/components/schemas/HomePublicationUpdates",
        },
        snapshot_schema: {
          type: "string",
          const: "home-snapshot/v2",
        },
      },
    })

    expect(
      getRecord(document, "components", "schemas", "HomePublicationUpdates"),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: [
        "calendar_date",
        "calendar_timezone",
        "formal_publications_today",
        "recent_acceptances",
        "recent_online_first",
      ],
      properties: {
        calendar_date: {
          type: "string",
          format: "date",
        },
        calendar_timezone: {
          type: "string",
          const: "UTC",
        },
        formal_publications_today: {
          $ref: "#/components/schemas/PublicationUpdateCollection",
        },
        recent_acceptances: {
          $ref: "#/components/schemas/PublicationUpdateCollection",
        },
        recent_online_first: {
          $ref: "#/components/schemas/PublicationUpdateCollection",
        },
      },
    })

    expect(
      getRecord(document, "components", "schemas", "PublicationEventKind"),
    ).toEqual({
      type: "string",
      enum: [
        "print_published",
        "electronic_published",
        "ahead_of_print",
        "accepted",
      ],
    })

    expect(
      getRecord(document, "components", "schemas", "PublicationUpdateEvent"),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: [
        "kind",
        "date",
        "date_precision",
        "publication_status",
        "publication_model",
        "provenance",
      ],
      properties: {
        date: {
          type: "string",
          format: "date",
        },
        date_precision: {
          type: "string",
          const: "day",
        },
        kind: {
          $ref: "#/components/schemas/PublicationEventKind",
        },
        publication_status: {
          type: "string",
          minLength: 1,
        },
        publication_model: {
          type: ["string", "null"],
          minLength: 1,
        },
        provenance: {
          $ref: "#/components/schemas/PublicationEventProvenance",
        },
      },
    })

    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "PublicationEventProvenance",
      ),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: [
        "source",
        "source_record_id",
        "normalized_assertion_id",
        "projection_assertion_id",
        "source_path",
        "status_raw",
      ],
      properties: {
        source: {
          type: "string",
          const: "pubmed",
        },
        source_record_id: {
          type: "string",
          format: "uuid",
        },
        normalized_assertion_id: {
          type: "string",
          format: "uuid",
        },
        projection_assertion_id: {
          type: "string",
          format: "uuid",
        },
        source_path: {
          type: "string",
          minLength: 1,
        },
        status_raw: {
          type: "string",
          minLength: 1,
        },
      },
    })

    expect(
      getRecord(document, "components", "schemas", "PublicationUpdateItem"),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: ["paper", "event"],
      properties: {
        paper: {
          $ref: "#/components/schemas/PaperSummary",
        },
        event: {
          $ref: "#/components/schemas/PublicationUpdateEvent",
        },
      },
    })

    expect(
      getRecord(document, "components", "schemas", "PublicationUpdateCollection"),
    ).toMatchObject({
      type: "object",
      additionalProperties: false,
      required: ["analysis", "items", "pagination"],
      properties: {
        analysis: {
          $ref: "#/components/schemas/AnalysisMetadata",
        },
        items: {
          type: "array",
          items: {
            $ref: "#/components/schemas/PublicationUpdateItem",
          },
        },
        pagination: {
          $ref: "#/components/schemas/SnapshotPagination",
        },
      },
    })
  })

  it("requires snapshot pagination on paper analysis collections", () => {
    expect(
      getRecord(document, "components", "schemas", "PaperAnalysisCollection"),
    ).toMatchObject({
      type: "object",
      additionalProperties: false,
      required: ["analysis", "items", "pagination"],
      properties: {
        pagination: {
          $ref: "#/components/schemas/SnapshotPagination",
        },
      },
    })

    expect(
      getRecord(document, "components", "schemas", "SnapshotPagination"),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: ["limit", "total", "next_cursor", "has_more"],
      properties: {
        limit: {
          type: "integer",
          minimum: 0,
        },
        total: {
          type: "integer",
          minimum: 0,
        },
        next_cursor: {
          type: "null",
          const: null,
        },
        has_more: {
          type: "boolean",
          const: false,
        },
      },
    })
  })
})

describe("generated OpenAPI TypeScript types", () => {
  it("are synchronized with contracts/openapi.yaml", () => {
    const result = spawnSync(process.execPath, [generatorPath, "--check"], {
      encoding: "utf8",
    })

    expect(
      result.status,
      [result.stdout, result.stderr].filter(Boolean).join("\n"),
    ).toBe(0)
  })

  it("contain no explicit any type", () => {
    expect(
      existsSync(generatedTypesPath),
      "apps/web/app/types/openapi.generated.ts must be generated",
    ).toBe(true)

    const sourceFile = ts.createSourceFile(
      generatedTypesPath,
      readFileSync(generatedTypesPath, "utf8"),
      ts.ScriptTarget.Latest,
      true,
      ts.ScriptKind.TS,
    )
    const explicitAnyLocations: string[] = []

    visitTypeScriptNodes(sourceFile, (node) => {
      if (node.kind === ts.SyntaxKind.AnyKeyword) {
        const position = sourceFile.getLineAndCharacterOfPosition(node.getStart())
        explicitAnyLocations.push(`${position.line + 1}:${position.character + 1}`)
      }
    })

    expect(explicitAnyLocations).toEqual([])
  })

  it("keeps biomedical venue evidence narrower than unknown", () => {
    const venueEvidence = getGeneratedComponentSchemaType(
      "BiomedicalEligibilityVenueEvidence",
    )
    const unknownLocations: string[] = []

    visitTypeScriptNodes(venueEvidence.type, (node) => {
      if (node.kind === ts.SyntaxKind.UnknownKeyword) {
        const position = venueEvidence.sourceFile.getLineAndCharacterOfPosition(
          node.getStart(),
        )
        unknownLocations.push(`${position.line + 1}:${position.character + 1}`)
      }
    })

    expect(unknownLocations).toEqual([])
  })

  it("exposes API responses as generated component schema aliases", () => {
    expectComponentSchemaAliases(catalogTypesPath, {
      MethodListResponse: "MethodListResponse",
      PaperDetail: "PaperDetail",
      PaperListResponse: "PaperListResponse",
      PaperSummary: "PaperSummary",
      ProblemDetails: "ProblemDetails",
      ResearchOpportunityListResponse: "ResearchOpportunityListResponse",
      StatsResponse: "StatsResponse",
      TopicListResponse: "TopicListResponse",
      TrendListResponse: "TrendListResponse",
    })
    expectComponentSchemaAliases(biomedicalTypesPath, {
      HomeResponse: "HomeResponse",
      JournalDetailResponse: "JournalDetailResponse",
      JournalListResponse: "JournalListResponse",
      SubjectDetailResponse: "SubjectDetailResponse",
      SubjectListResponse: "SubjectListResponse",
    })
  })
})

function publicOperations(value: unknown) {
  const methods = new Set([
    "delete",
    "get",
    "head",
    "options",
    "patch",
    "post",
    "put",
    "trace",
  ])
  const operations: Array<{
    method: string
    operation: Record<string, unknown>
    path: string
  }> = []

  for (const [path, pathItem] of Object.entries(getRecord(value, "paths"))) {
    if (!isRecord(pathItem)) {
      continue
    }
    for (const [method, operation] of Object.entries(pathItem)) {
      if (methods.has(method) && isRecord(operation)) {
        operations.push({ method, operation, path })
      }
    }
  }

  return operations
}

function visitReferences(
  value: unknown,
  visitor: (reference: string) => void,
) {
  if (Array.isArray(value)) {
    for (const item of value) {
      visitReferences(item, visitor)
    }
    return
  }
  if (!isRecord(value)) {
    return
  }

  if (typeof value.$ref === "string") {
    visitor(value.$ref)
  }
  for (const item of Object.values(value)) {
    visitReferences(item, visitor)
  }
}

function resolveSchema(value: unknown, schema: unknown) {
  if (isRecord(schema) && typeof schema.$ref === "string") {
    return getRecord(resolveLocalReference(value, schema.$ref))
  }
  return getRecord(schema)
}

function resolveLocalReference(value: unknown, reference: string): unknown {
  if (!reference.startsWith("#/")) {
    throw new Error(`unsupported external reference: ${reference}`)
  }

  let current = value
  for (const encodedSegment of reference.slice(2).split("/")) {
    const segment = encodedSegment.replaceAll("~1", "/").replaceAll("~0", "~")
    const record = getRecord(current)
    if (!(segment in record)) {
      throw new Error(`unresolved reference: ${reference}`)
    }
    current = record[segment]
  }

  return current
}

function getRecord(
  value: unknown,
  ...path: string[]
): Record<string, unknown> {
  let current = value
  for (const segment of path) {
    if (!isRecord(current) || !(segment in current)) {
      throw new Error(`missing object path: ${path.join(".")}`)
    }
    current = current[segment]
  }
  if (!isRecord(current)) {
    throw new Error(`value is not an object: ${path.join(".")}`)
  }
  return current
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function visitTypeScriptNodes(
  node: ts.Node,
  visitor: (node: ts.Node) => void,
) {
  visitor(node)
  node.forEachChild((child) => visitTypeScriptNodes(child, visitor))
}

function expectComponentSchemaAliases(
  path: string,
  expectedAliases: Record<string, string>,
) {
  const sourceFile = ts.createSourceFile(
    path,
    readFileSync(path, "utf8"),
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TS,
  )
  const aliases = new Map(
    sourceFile.statements
      .filter(ts.isTypeAliasDeclaration)
      .map((declaration) => [
        declaration.name.text,
        indexedAccessPath(declaration.type),
      ]),
  )

  for (const [aliasName, schemaName] of Object.entries(expectedAliases)) {
    expect(aliases.get(aliasName), aliasName).toEqual([
      "components",
      "schemas",
      schemaName,
    ])
  }
}

function getGeneratedComponentSchemaType(schemaName: string) {
  const sourceFile = ts.createSourceFile(
    generatedTypesPath,
    readFileSync(generatedTypesPath, "utf8"),
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TS,
  )
  const components = sourceFile.statements.find(
    (statement): statement is ts.InterfaceDeclaration =>
      ts.isInterfaceDeclaration(statement)
      && statement.name.text === "components",
  )
  if (components === undefined) {
    throw new Error("generated components interface is missing")
  }

  const schemas = findPropertySignature(components.members, "schemas")
  if (schemas.type === undefined || !ts.isTypeLiteralNode(schemas.type)) {
    throw new Error("generated components.schemas type is missing")
  }

  const schema = findPropertySignature(schemas.type.members, schemaName)
  if (schema.type === undefined) {
    throw new Error(`generated schema type is missing: ${schemaName}`)
  }
  return {
    sourceFile,
    type: schema.type,
  }
}

function findPropertySignature(
  members: ts.NodeArray<ts.TypeElement>,
  name: string,
) {
  const property = members.find(
    (member): member is ts.PropertySignature =>
      ts.isPropertySignature(member)
      && propertyNameText(member.name) === name,
  )
  if (property === undefined) {
    throw new Error(`generated property is missing: ${name}`)
  }
  return property
}

function propertyNameText(name: ts.PropertyName) {
  if (
    ts.isIdentifier(name)
    || ts.isStringLiteral(name)
    || ts.isNumericLiteral(name)
  ) {
    return name.text
  }
  return undefined
}

function indexedAccessPath(node: ts.TypeNode): string[] | undefined {
  if (ts.isTypeReferenceNode(node) && ts.isIdentifier(node.typeName)) {
    return [node.typeName.text]
  }
  if (!ts.isIndexedAccessTypeNode(node)) {
    return undefined
  }

  const objectPath = indexedAccessPath(node.objectType)
  const argument = node.indexType
  if (
    objectPath === undefined
    || !ts.isLiteralTypeNode(argument)
    || !ts.isStringLiteral(argument.literal)
  ) {
    return undefined
  }
  return [...objectPath, argument.literal.text]
}
