package catalog

import (
	"encoding/json"
	"testing"
	"time"
)

const (
	strictContractUUIDA = "11111111-1111-4111-8111-111111111111"
	strictContractUUIDB = "22222222-2222-4222-8222-222222222222"
	strictContractUUIDC = "33333333-3333-4333-8333-333333333333"
	strictContractUUIDD = "44444444-4444-4444-8444-444444444444"
)

func TestHomeSnapshotStrictContractAcceptsCompleteOpenAPIPayload(t *testing.T) {
	payload := strictHomeContractPayload(t, strictHomeContractFixture())
	if err := validateHomeSnapshotPayload(payload); err != nil {
		t.Fatalf("validateHomeSnapshotPayload() rejected valid OpenAPI payload: %v", err)
	}
}

func TestHomeOfficialLinkStrictContractAcceptsVerifiedPreprintDOIURL(
	t *testing.T,
) {
	payload := strictHomeContractPayload(t, map[string]any{
		"url":              "https://doi.org/10.1000/preprint",
		"verification_id":  strictContractUUIDA,
		"link_role":        "doi_url",
		"content_channel":  "preprint",
		"verified_at":      "2026-07-18T07:30:00Z",
		"expires_at":       "2026-08-18T07:30:00Z",
		"verifier_version": "official-url-verifier/v1",
		"policy_version":   "official-url-policy/v1",
	})
	channel, err := validateHomeOfficialLinkAndChannelAt(
		"PaperSummary.official_link",
		payload,
		time.Time{},
	)
	if err != nil {
		t.Fatalf(
			"validateHomeOfficialLinkAndChannelAt(preprint doi_url) error = %v",
			err,
		)
	}
	if channel != "preprint" {
		t.Fatalf("content channel = %q, want preprint", channel)
	}
}

func TestHomeSnapshotStrictContractRequiresExactOfficialLinkVisibilityFields(
	t *testing.T,
) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "missing official link",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "latest_papers", "items", 0),
					"official_link",
				)
			},
		},
		{
			name: "missing public visibility",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "latest_papers", "items", 0),
					"publicly_visible",
				)
			},
		},
		{
			name: "missing analysis readiness",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "latest_papers", "items", 0),
					"analysis_ready",
				)
			},
		},
		{
			name: "missing topics readiness",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "latest_papers", "items", 0),
					"topics_state",
				)
			},
		},
		{
			name: "missing methods readiness",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "latest_papers", "items", 0),
					"methods_state",
				)
			},
		},
		{
			name: "official link unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"official_link",
				)["extra"] = true
			},
		},
		{
			name: "official link is not HTTPS",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"official_link",
				)["url"] = "http://publisher.example.test/article"
			},
		},
		{
			name: "official link has nil verification id",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"official_link",
				)["verification_id"] =
					"00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "official link role conflicts with channel",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"official_link",
				)["link_role"] = "official_preprint"
			},
		},
		{
			name: "official link expires before verification",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"official_link",
				)["expires_at"] = "2026-07-18T07:59:59Z"
			},
		},
		{
			name: "public visibility is false",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
				)["publicly_visible"] = false
			},
		},
		{
			name: "analysis readiness is not boolean",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
				)["analysis_ready"] = "true"
			},
		},
		{
			name: "invalid topics readiness",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
				)["topics_state"] = "unknown"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := strictHomeContractClone(t)
			test.mutate(candidate)
			if err := validateHomeSnapshotPayload(
				strictHomeContractPayload(t, candidate),
			); err == nil {
				t.Fatalf("validateHomeSnapshotPayload() accepted %s", test.name)
			}
		})
	}
}

func TestHomeSnapshotStrictContractBindsOfficialLinkToGeneratedAt(
	t *testing.T,
) {
	tests := []struct {
		name       string
		verifiedAt string
		expiresAt  string
	}{
		{
			name:       "verified after generated at",
			verifiedAt: "2026-07-18T08:30:00.124Z",
			expiresAt:  "2026-08-18T08:30:00Z",
		},
		{
			name:       "expires at generated at",
			verifiedAt: "2026-07-18T07:30:00Z",
			expiresAt:  "2026-07-18T08:30:00.123Z",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			candidate := strictHomeContractClone(t)
			link := strictContractObject(
				candidate,
				"latest_papers",
				"items",
				0,
				"official_link",
			)
			link["verified_at"] = test.verifiedAt
			link["expires_at"] = test.expiresAt
			if err := validateHomeSnapshotPayload(
				strictHomeContractPayload(t, candidate),
			); err == nil {
				t.Fatalf(
					"validateHomeSnapshotPayload() accepted %s",
					test.name,
				)
			}
		})
	}
}

func TestHomeSnapshotStrictContractRejectsWindowMetadataOutsideV2Declaration(
	t *testing.T,
) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "active journals window is not seven days",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"active_journals",
					"analysis",
					"window_days",
				)["value"] = 30
			},
		},
		{
			name: "citation momentum window is not thirty days",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"citation_momentum",
					"analysis",
					"window_days",
				)["value"] = 7
			},
		},
		{
			name: "coverage window is not thirty days",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"coverage",
					"analysis",
					"window_days",
				)["value"] = 7
			},
		},
		{
			name: "entity momentum window is not seven days",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"entity_momentum",
					"analysis",
					"window_days",
				)["value"] = 30
			},
		},
		{
			name: "latest papers window is not seven days",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"analysis",
					"window_days",
				)["value"] = 1
			},
		},
		{
			name: "formal publications today window is not one day",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"publication_updates",
					"formal_publications_today",
					"analysis",
					"window_days",
				)["value"] = 7
			},
		},
		{
			name: "recent acceptances window is not seven days",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"publication_updates",
					"recent_acceptances",
					"analysis",
					"window_days",
				)["value"] = 30
			},
		},
		{
			name: "recent online first window is not seven days",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"publication_updates",
					"recent_online_first",
					"analysis",
					"window_days",
				)["value"] = 30
			},
		},
		{
			name: "research opportunities invents a publication window",
			mutate: func(home map[string]any) {
				strictContractSet(
					home,
					strictKnown(30),
					"research_opportunities",
					"analysis",
					"window_days",
				)
			},
		},
		{
			name: "subject momentum window is not seven days",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"subject_momentum",
					"analysis",
					"window_days",
				)["value"] = 30
			},
		},
		{
			name: "recent window disagrees with declared window",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"subject_momentum",
					"analysis",
				)["recent_window_days"] = 30
			},
		},
		{
			name: "baseline window is present without recent window",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "subject_momentum", "analysis"),
					"recent_window_days",
				)
			},
		},
		{
			name: "baseline window does not exceed recent window",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"subject_momentum",
					"analysis",
				)["baseline_window_days"] = 7
			},
		},
		{
			name: "analysis identity is incomplete",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "subject_momentum", "analysis"),
					"analysis_type",
				)
			},
		},
		{
			name: "window value has wrong type",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"active_journals",
					"analysis",
					"window_days",
				)["value"] = "7"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := strictHomeContractClone(t)
			test.mutate(candidate)
			if err := validateHomeSnapshotPayload(
				strictHomeContractPayload(t, candidate),
			); err == nil {
				t.Fatalf("validateHomeSnapshotPayload() accepted %s", test.name)
			}
		})
	}
}

func TestHomeSnapshotStrictContractRejectsLooseObjectFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "subject momentum item is missing subject",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "subject_momentum", "items", 0),
					"subject",
				)
			},
		},
		{
			name: "subject momentum subject has wrong type",
			mutate: func(home map[string]any) {
				strictContractObject(home, "subject_momentum", "items", 0)["subject"] =
					"oncology"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := strictHomeContractClone(t)
			test.mutate(candidate)
			if err := validateHomeSnapshotPayload(
				strictHomeContractPayload(t, candidate),
			); err == nil {
				t.Fatalf("validateHomeSnapshotPayload() accepted %s", test.name)
			}
		})
	}
}

func TestHomeSnapshotStrictContractRejectsUnknownAndMissingNestedFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "analysis unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(home, "active_journals", "analysis")["extra"] = true
			},
		},
		{
			name: "analysis missing required sources",
			mutate: func(home map[string]any) {
				delete(strictContractObject(home, "active_journals", "analysis"), "sources")
			},
		},
		{
			name: "analysis missing signal unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"active_journals",
					"analysis",
					"missing_signals",
					1,
				)["extra"] = true
			},
		},
		{
			name: "collection unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(home, "latest_papers")["extra"] = true
			},
		},
		{
			name: "pagination missing required total",
			mutate: func(home map[string]any) {
				delete(strictContractObject(home, "latest_papers", "pagination"), "total")
			},
		},
		{
			name: "pagination unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(home, "latest_papers", "pagination")["extra"] = true
			},
		},
		{
			name: "coverage missing required taxonomy version",
			mutate: func(home map[string]any) {
				delete(strictContractObject(home, "coverage"), "taxonomy_version")
			},
		},
		{
			name: "coverage unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(home, "coverage")["extra"] = true
			},
		},
		{
			name: "journal item missing required paper count",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "active_journals", "items", 0),
					"paper_count",
				)
			},
		},
		{
			name: "journal summary unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"active_journals",
					"items",
					0,
					"journal",
				)["extra"] = true
			},
		},
		{
			name: "journal category missing quartile",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(
						home,
						"active_journals",
						"items",
						0,
						"journal",
						"categories",
						0,
					),
					"quartile",
				)
			},
		},
		{
			name: "entity item missing required label",
			mutate: func(home map[string]any) {
				delete(strictContractObject(home, "entity_momentum", "items", 0), "label")
			},
		},
		{
			name: "entity confidence interval unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"entity_momentum",
					"items",
					0,
					"confidence_interval",
				)["extra"] = true
			},
		},
		{
			name: "subject item missing required model family",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "subject_momentum", "items", 0),
					"model_family",
				)
			},
		},
		{
			name: "subject reference unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"subject_momentum",
					"items",
					0,
					"subject",
				)["extra"] = true
			},
		},
		{
			name: "citation item missing required paper",
			mutate: func(home map[string]any) {
				delete(strictContractObject(home, "citation_momentum", "items", 0), "paper")
			},
		},
		{
			name: "opportunity missing required trigger rule",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "research_opportunities", "items", 0),
					"trigger_rule",
				)
			},
		},
		{
			name: "opportunity estimate unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"research_opportunities",
					"items",
					0,
					"estimates",
					0,
				)["extra"] = true
			},
		},
		{
			name: "publication updates unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(home, "publication_updates")["extra"] = true
			},
		},
		{
			name: "publication item unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"publication_updates",
					"formal_publications_today",
					"items",
					0,
				)["extra"] = true
			},
		},
		{
			name: "publication event unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"publication_updates",
					"formal_publications_today",
					"items",
					0,
					"event",
				)["extra"] = true
			},
		},
		{
			name: "publication provenance missing source path",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(
						home,
						"publication_updates",
						"formal_publications_today",
						"items",
						0,
						"event",
						"provenance",
					),
					"source_path",
				)
			},
		},
		{
			name: "paper missing required citation snapshots",
			mutate: func(home map[string]any) {
				delete(strictContractObject(home, "latest_papers", "items", 0), "citation_snapshots")
			},
		},
		{
			name: "paper unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(home, "latest_papers", "items", 0)["extra"] = true
			},
		},
		{
			name: "citation snapshot missing required dataset version",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(
						home,
						"latest_papers",
						"items",
						0,
						"citation_snapshots",
						"value",
						0,
					),
					"dataset_version",
				)
			},
		},
		{
			name: "citation evidence unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"citation_analysis_evidence",
					"value",
				)["extra"] = true
			},
		},
		{
			name: "mesh qualifier missing required source path",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(
						home,
						"latest_papers",
						"items",
						0,
						"mesh_headings",
						"value",
						0,
						"qualifiers",
						0,
					),
					"source_path",
				)
			},
		},
		{
			name: "eligibility evidence missing required matches",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(
						home,
						"latest_papers",
						"items",
						0,
						"jcr_assessment",
						"value",
						"evidence",
					),
					"matches",
				)
			},
		},
		{
			name: "curation payload unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"curation",
					"value",
				)["extra"] = true
			},
		},
		{
			name: "source provenance missing required event key",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(
						home,
						"latest_papers",
						"items",
						0,
						"source_provenance",
						"value",
						0,
					),
					"event_key",
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := strictHomeContractClone(t)
			test.mutate(candidate)
			if err := validateHomeSnapshotPayload(
				strictHomeContractPayload(t, candidate),
			); err == nil {
				t.Fatalf("validateHomeSnapshotPayload() accepted %s", test.name)
			}
		})
	}
}

func TestHomeSnapshotStrictContractRejectsInvalidCatalogValueStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "known state without value",
			mutate: func(home map[string]any) {
				delete(
					strictContractObject(home, "coverage", "analysis", "coverage_ratio"),
					"value",
				)
			},
		},
		{
			name: "known state with unknown field",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"coverage",
					"analysis",
					"coverage_ratio",
				)["reason"] = "not allowed"
			},
		},
		{
			name: "missing state with value",
			mutate: func(home map[string]any) {
				strictContractSet(
					home,
					map[string]any{"state": "missing", "value": 1},
					"coverage",
					"analysis",
					"coverage_ratio",
				)
			},
		},
		{
			name: "unknown state with value",
			mutate: func(home map[string]any) {
				strictContractSet(
					home,
					map[string]any{"state": "unknown", "value": 1},
					"coverage",
					"analysis",
					"coverage_ratio",
				)
			},
		},
		{
			name: "unsupported state",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"coverage",
					"analysis",
					"coverage_ratio",
				)["state"] = "estimated"
			},
		},
		{
			name: "ratio above maximum",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"coverage",
					"citation_coverage_ratio",
				)["value"] = 1.01
			},
		},
		{
			name: "negative integer",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"coverage",
					"analysis",
					"sample_size",
				)["value"] = -1
			},
		},
		{
			name: "boolean value has wrong type",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"has_code",
				)["value"] = "false"
			},
		},
		{
			name: "paper type value outside enum",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"type",
				)["value"] = "editorial"
			},
		},
		{
			name: "citation snapshots known empty array",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"citation_snapshots",
				)["value"] = []any{}
			},
		},
		{
			name: "citation velocity insufficient evidence missing reason",
			mutate: func(home map[string]any) {
				strictContractSet(
					home,
					map[string]any{"state": "insufficient_evidence"},
					"latest_papers",
					"items",
					0,
					"citation_velocity",
				)
			},
		},
		{
			name: "citation percentile above maximum",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"citation_percentile",
				)["value"] = 101
			},
		},
		{
			name: "publication types rejects unknown state",
			mutate: func(home map[string]any) {
				strictContractSet(
					home,
					map[string]any{"state": "unknown"},
					"latest_papers",
					"items",
					0,
					"publication_types_state",
				)
			},
		},
		{
			name: "mesh headings rejects unknown state",
			mutate: func(home map[string]any) {
				strictContractSet(
					home,
					map[string]any{"state": "unknown"},
					"latest_papers",
					"items",
					0,
					"mesh_headings",
				)
			},
		},
		{
			name: "eligibility assessment rejects missing state",
			mutate: func(home map[string]any) {
				strictContractSet(
					home,
					map[string]any{"state": "missing"},
					"latest_papers",
					"items",
					0,
					"jcr_assessment",
				)
			},
		},
		{
			name: "article usage accepts only missing state",
			mutate: func(home map[string]any) {
				strictContractSet(
					home,
					map[string]any{"state": "known", "value": 1},
					"latest_papers",
					"items",
					0,
					"article_usage",
				)
			},
		},
		{
			name: "source provenance known empty array",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"source_provenance",
				)["value"] = []any{}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := strictHomeContractClone(t)
			test.mutate(candidate)
			if err := validateHomeSnapshotPayload(
				strictHomeContractPayload(t, candidate),
			); err == nil {
				t.Fatalf("validateHomeSnapshotPayload() accepted %s", test.name)
			}
		})
	}
}

func TestHomeSnapshotStrictContractRejectsInvalidUUIDsAndFormats(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "nil catalog generation",
			mutate: func(home map[string]any) {
				home["catalog_generation"] = "00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "nil analysis run id",
			mutate: func(home map[string]any) {
				strictContractObject(home, "active_journals", "analysis")["analysis_run_id"] =
					"00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "nil journal id",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"active_journals",
					"items",
					0,
					"journal",
				)["id"] = "00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "nil paper id",
			mutate: func(home map[string]any) {
				strictContractObject(home, "latest_papers", "items", 0)["id"] =
					"00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "nil citation snapshot source record id",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"citation_snapshots",
					"value",
					0,
				)["source_record_id"] = "00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "nil citation evidence supporting work id",
			mutate: func(home map[string]any) {
				strictContractArray(
					home,
					"latest_papers",
					"items",
					0,
					"citation_analysis_evidence",
					"value",
					"percentile",
					"supporting_work_ids",
				)[0] = "00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "nil author institution id",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"authors",
					0,
				)["institution_id"] = "00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "nil eligibility subject rule id",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"jcr_assessment",
					"value",
					"evidence",
					"matches",
					0,
				)["subject_rule_id"] = "00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "nil opportunity id",
			mutate: func(home map[string]any) {
				strictContractObject(home, "research_opportunities", "items", 0)["id"] =
					"00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "nil publication provenance assertion id",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"publication_updates",
					"formal_publications_today",
					"items",
					0,
					"event",
					"provenance",
				)["normalized_assertion_id"] =
					"00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "invalid top level date time",
			mutate: func(home map[string]any) {
				home["generated_at"] = "2026-02-30T08:30:00Z"
			},
		},
		{
			name: "invalid publication date",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"publication_updates",
					"formal_publications_today",
					"items",
					0,
					"event",
				)["date"] = "2026-02-30"
			},
		},
		{
			name: "invalid paper published date time",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"published_at",
				)["value"] = "2026-07-18"
			},
		},
		{
			name: "invalid citation snapshot observed date time",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"citation_snapshots",
					"value",
					0,
				)["observed_at"] = "not-a-date-time"
			},
		},
		{
			name: "invalid opportunity generated date time",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"research_opportunities",
					"items",
					0,
				)["generated_at"] = "2026-07-18"
			},
		},
		{
			name: "invalid curation assessed date time",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"curation",
					"value",
				)["assessed_at"] = "invalid"
			},
		},
		{
			name: "invalid taxonomy slug",
			mutate: func(home map[string]any) {
				strictContractObject(
					home,
					"latest_papers",
					"items",
					0,
					"topics",
					0,
				)["slug"] = "Not Valid"
			},
		},
		{
			name: "invalid cohort revision hash",
			mutate: func(home map[string]any) {
				strictContractObject(home, "active_journals", "analysis")["cohort_revision"] =
					"not-a-sha256"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := strictHomeContractClone(t)
			test.mutate(candidate)
			if err := validateHomeSnapshotPayload(
				strictHomeContractPayload(t, candidate),
			); err == nil {
				t.Fatalf("validateHomeSnapshotPayload() accepted %s", test.name)
			}
		})
	}
}

func strictHomeContractClone(t *testing.T) map[string]any {
	t.Helper()
	payload := strictHomeContractPayload(t, strictHomeContractFixture())
	var clone map[string]any
	if err := json.Unmarshal(payload, &clone); err != nil {
		t.Fatalf("clone strict Home contract fixture: %v", err)
	}
	return clone
}

func strictHomeContractPayload(t *testing.T, value map[string]any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode strict Home contract fixture: %v", err)
	}
	return payload
}

func strictHomeContractFixture() map[string]any {
	paper := strictHomeContractPaper()
	return map[string]any{
		"active_journals": map[string]any{
			"analysis": strictHomeContractAnalysisForKnownWindow(7),
			"items": []any{
				map[string]any{
					"journal": map[string]any{
						"id":      strictContractUUIDA,
						"slug":    "nature-medicine",
						"title":   "Nature Medicine",
						"aliases": []any{"Nat Med"},
						"categories": []any{
							map[string]any{"name": "Medicine, Research & Experimental", "quartile": "Q1"},
						},
						"eissn":            strictKnown("1546-170X"),
						"issn_l":           strictKnown("1078-8956"),
						"issns":            []any{"1078-8956", "1546-170X"},
						"jcr_metric_year":  2025,
						"jif":              strictKnown(58.7),
						"paper_count":      strictKnown(1),
						"publisher":        strictKnown("Springer Nature"),
						"taxonomy_version": "biomedical-jcr-subjects/v1",
					},
					"paper_count":              strictKnown(1),
					"publication_change_ratio": strictKnown(0.25),
				},
			},
		},
		"catalog_generation": strictContractUUIDA,
		"citation_momentum": map[string]any{
			"analysis": strictHomeContractAnalysisForKnownWindow(30),
			"items": []any{
				map[string]any{
					"citation_delta":    strictKnown(8),
					"citations_per_day": strictKnown(2.5),
					"cohort_percentile": strictKnown(0.95),
					"paper":             paper,
				},
			},
		},
		"coverage": map[string]any{
			"analysis":                        strictHomeContractAnalysisForKnownWindow(30),
			"citation_coverage_ratio":         strictKnown(1),
			"jcr_metric_year":                 2025,
			"mesh_coverage_ratio":             strictKnown(1),
			"publication_type_coverage_ratio": strictKnown(1),
			"taxonomy_version":                "biomedical-jcr-subjects/v1",
		},
		"entity_momentum": map[string]any{
			"analysis": strictHomeContractTrendAnalysis(),
			"items": []any{
				map[string]any{
					"adjusted_p_value":          strictKnown(0.02),
					"baseline_count":            strictKnown(8),
					"confidence_interval":       map[string]any{"lower": 0.2, "upper": 1.4},
					"entity_type":               "method",
					"estimate":                  strictKnown(0.8),
					"independent_journal_count": strictKnown(3),
					"independent_team_count":    strictKnown(4),
					"label":                     "spatial transcriptomics",
					"model_family":              "negative_binomial",
					"p_value":                   strictKnown(0.01),
					"recent_count":              strictKnown(12),
				},
			},
		},
		"evidence_gaps": []any{"article_usage_not_published"},
		"generated_at":  "2026-07-18T08:30:00.123Z",
		"latest_papers": strictHomeContractPaperCollection(paper),
		"publication_updates": map[string]any{
			"calendar_date":     "2026-07-18",
			"calendar_timezone": "UTC",
			"formal_publications_today": strictHomeContractPublicationCollection(
				paper,
				"print_published",
			),
			"recent_acceptances": strictHomeContractPublicationCollection(
				paper,
				"accepted",
			),
			"recent_online_first": strictHomeContractPublicationCollection(
				paper,
				"ahead_of_print",
			),
		},
		"research_opportunities": map[string]any{
			"analysis": strictHomeContractAnalysisForMissingWindow(),
			"items": []any{
				map[string]any{
					"analysis_run_id": strictContractUUIDA,
					"coverage_ratio":  strictKnown(0.9),
					"estimates": []any{
						map[string]any{
							"confidence_interval": map[string]any{"lower": 0.1, "upper": 0.9},
							"metric":              "effect_size",
							"value":               0.5,
						},
					},
					"formula_version":        "opportunity/v1",
					"generated_at":           "2026-07-18T08:30:00Z",
					"id":                     strictContractUUIDB,
					"limitations":            []any{"External validation is absent"},
					"missing_signals":        []any{"prospective_validation"},
					"recommended_next_steps": []any{"Run a multicenter validation study"},
					"status":                 "worth_pursuing",
					"summary":                "A validated translational gap.",
					"supporting_work_ids":    []any{strictContractUUIDC},
					"target_id":              "spatial-transcriptomics",
					"target_kind":            "method",
					"title":                  "Validate spatial biomarkers",
					"trigger_rule": map[string]any{
						"code":    "high-growth-low-validation",
						"version": "v1",
					},
				},
			},
		},
		"snapshot_schema": homeSnapshotSchemaVersion,
		"scope": map[string]any{
			"jcr_metric_year":  2025,
			"taxonomy_version": "biomedical-jcr-subjects/v1",
		},
		"subject_momentum": map[string]any{
			"analysis": strictHomeContractTrendAnalysis(),
			"items": []any{
				map[string]any{
					"adjusted_p_value":          strictKnown(0.02),
					"baseline_count":            strictKnown(8),
					"confidence_interval":       map[string]any{"lower": 0.2, "upper": 1.4},
					"estimate":                  strictKnown(0.8),
					"independent_journal_count": strictKnown(3),
					"independent_team_count":    strictKnown(4),
					"label":                     "rising",
					"model_family":              "negative_binomial",
					"p_value":                   strictKnown(0.01),
					"recent_count":              strictKnown(12),
					"subject": map[string]any{
						"id":   strictContractUUIDA,
						"slug": "oncology",
						"name": "Oncology",
					},
				},
			},
		},
	}
}

func strictHomeContractAnalysisForKnownWindow(windowDays int) map[string]any {
	analysis := strictHomeContractAnalysisForMissingWindow()
	analysis["window_days"] = strictKnown(windowDays)
	return analysis
}

func strictHomeContractAnalysisForMissingWindow() map[string]any {
	return map[string]any{
		"coverage_ratio":  strictKnown(1),
		"formula_version": strictKnown("home/v2"),
		"generated_at":    strictKnown("2026-07-18T08:30:00Z"),
		"missing_signals": []any{
			"article_usage",
			map[string]any{
				"signal":              "open_fulltext",
				"reason":              "not published",
				"affected_entity_ids": []any{strictContractUUIDA},
			},
		},
		"sample_size": strictKnown(1),
		"sources":     strictKnown([]any{"pubmed"}),
		"window_days": strictMissing(),
	}
}

func strictHomeContractTrendAnalysis() map[string]any {
	analysis := strictHomeContractAnalysisForKnownWindow(7)
	analysis["analysis_run_id"] = strictContractUUIDA
	analysis["analysis_type"] = "publication_trends"
	analysis["baseline_window_days"] = 364
	analysis["cohort_revision"] =
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	analysis["recent_window_days"] = 7
	return analysis
}

func strictHomeContractPaperCollection(paper map[string]any) map[string]any {
	return map[string]any{
		"analysis":   strictHomeContractAnalysisForKnownWindow(7),
		"items":      []any{paper},
		"pagination": strictHomeContractPagination(1),
	}
}

func strictHomeContractPublicationCollection(
	paper map[string]any,
	kind string,
) map[string]any {
	windowDays := 7
	if kind == "print_published" || kind == "electronic_published" {
		windowDays = 1
	}
	return map[string]any{
		"analysis": strictHomeContractAnalysisForKnownWindow(windowDays),
		"items": []any{
			map[string]any{
				"paper": paper,
				"event": map[string]any{
					"kind":               kind,
					"date":               "2026-07-18",
					"date_precision":     "day",
					"publication_status": "ppublish",
					"publication_model":  "Print",
					"provenance": map[string]any{
						"source":                  "pubmed",
						"source_record_id":        strictContractUUIDA,
						"normalized_assertion_id": strictContractUUIDB,
						"projection_assertion_id": strictContractUUIDC,
						"source_path":             "PubmedArticle/MedlineCitation/Article/Journal",
						"status_raw":              "ppublish",
					},
				},
			},
		},
		"pagination": strictHomeContractPagination(1),
	}
}

func strictHomeContractPagination(size int) map[string]any {
	return map[string]any{
		"has_more":    false,
		"limit":       size,
		"next_cursor": nil,
		"total":       size,
	}
}

func strictHomeContractPaper() map[string]any {
	return map[string]any{
		"id":               strictContractUUIDA,
		"canonical_key":    "doi:10.1000/strict-home",
		"title":            "Strict Home contract paper",
		"status":           "active",
		"published_at":     strictKnown("2026-07-18T07:30:00Z"),
		"type":             strictKnown("research_article"),
		"abstract":         strictKnown("Abstract"),
		"abstract_snippet": strictKnown("Snippet"),
		"has_code":         strictKnown(true),
		"has_data":         strictKnown(true),
		"has_benchmark":    strictKnown(false),
		"citation_count":   strictKnown(42),
		"citation_source":  strictKnown("openalex"),
		"citation_snapshots": strictKnown([]any{
			map[string]any{
				"source":             "openalex",
				"observed_at":        "2026-07-18T07:00:00Z",
				"count":              42,
				"source_record_id":   strictContractUUIDA,
				"ingestion_job_id":   strictContractUUIDB,
				"retrieved_at":       "2026-07-18T07:05:00Z",
				"coverage":           1,
				"definition_version": "openalex-cited-by/v1",
				"dataset_version":    "2026-07-18",
			},
		}),
		"citation_velocity":   strictKnown(2.5),
		"citation_percentile": strictKnown(95),
		"citation_analysis_evidence": strictKnown(map[string]any{
			"analysis_run_id": strictContractUUIDA,
			"source":          "openalex",
			"as_of":           "2026-07-18T07:00:00Z",
			"generated_at":    "2026-07-18T08:00:00Z",
			"formula_version": "citation/v1",
			"source_revision": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"velocity": map[string]any{
				"state":                "known",
				"window_days":          7,
				"current_snapshot_id":  strictContractUUIDA,
				"baseline_snapshot_id": strictContractUUIDB,
				"elapsed_days":         7,
			},
			"percentile": map[string]any{
				"state":                "known",
				"citation_snapshot_id": strictContractUUIDA,
				"subject_version_id":   strictContractUUIDB,
				"subject_id":           strictContractUUIDC,
				"publication_year":     2026,
				"publication_type_id":  strictContractUUIDD,
				"cohort_key":           "oncology:2026:research-article",
				"cohort_size":          20,
				"minimum_cohort_size":  10,
				"midrank":              19.5,
				"supporting_work_ids":  []any{strictContractUUIDA},
			},
		}),
		"trend_score": strictKnown(0.8),
		"topics": []any{
			map[string]any{
				"id":   strictContractUUIDA,
				"slug": "spatial-transcriptomics",
				"name": "Spatial transcriptomics",
			},
		},
		"topics_state": "known",
		"methods": []any{
			map[string]any{
				"id":   strictContractUUIDB,
				"slug": "single-cell-sequencing",
				"name": "Single-cell sequencing",
			},
		},
		"methods_state": "known",
		"authors": []any{
			map[string]any{
				"id":               strictContractUUIDA,
				"name":             "Ada Researcher",
				"orcid":            "0000-0002-1825-0097",
				"institution_id":   strictContractUUIDB,
				"institution_name": "Example University",
				"position":         1,
				"is_corresponding": true,
			},
		},
		"journal": map[string]any{
			"id":    strictContractUUIDA,
			"slug":  "nature-medicine",
			"title": "Nature Medicine",
		},
		"publication_types":       []any{"Journal Article"},
		"publication_types_state": strictKnown([]any{"Journal Article"}),
		"mesh_headings": strictKnown([]any{
			map[string]any{
				"descriptor_ui":  "D000001",
				"label":          "Calcimycin",
				"is_major_topic": true,
				"source_path":    "MeshHeadingList/MeshHeading",
				"qualifiers": []any{
					map[string]any{
						"qualifier_ui":   "Q000001",
						"label":          "analysis",
						"is_major_topic": false,
						"source_path":    "MeshHeadingList/MeshHeading/QualifierName",
					},
				},
			},
		}),
		"jcr_assessment": strictKnown(map[string]any{
			"id":                  strictContractUUIDA,
			"policy_version":      "biomedical-jcr-q1-jif10/v1",
			"metric_year":         2025,
			"subject_version_id":  strictContractUUIDB,
			"subject_version_key": "biomedical-jcr-subjects/v1",
			"decision":            "accepted",
			"evidence": map[string]any{
				"policy_version":      "biomedical-jcr-q1-jif10/v1",
				"metric_year":         2025,
				"subject_version_id":  strictContractUUIDB,
				"subject_version_key": "biomedical-jcr-subjects/v1",
				"venue": map[string]any{
					"venue_id": strictContractUUIDA,
					"issn_l":   "1078-8956",
					"issn":     "1078-8956",
					"eissn":    "1546-170X",
				},
				"metrics": []any{
					map[string]any{
						"venue_metric_snapshot_id": strictContractUUIDA,
						"venue_id":                 strictContractUUIDA,
						"metric_year":              2025,
						"jcr_category":             "Medicine, Research & Experimental",
					},
				},
				"matches": []any{
					map[string]any{
						"journal_subject_metric_id": strictContractUUIDA,
						"venue_metric_snapshot_id":  strictContractUUIDB,
						"venue_id":                  strictContractUUIDA,
						"metric_year":               2025,
						"subject_version_id":        strictContractUUIDB,
						"subject_id":                strictContractUUIDC,
						"subject_rule_id":           strictContractUUIDD,
						"subject_slug":              "oncology",
						"jcr_category":              "Oncology",
					},
				},
			},
			"assessed_at": "2026-07-18T06:00:00Z",
		}),
		"article_usage": strictMissing(),
		"open_fulltext": strictMissing(),
		"official_link": map[string]any{
			"url":              "https://publisher.example.test/article?view=full",
			"verification_id":  strictContractUUIDD,
			"link_role":        "official_article",
			"content_channel":  "journal_published",
			"verified_at":      "2026-07-18T08:00:00Z",
			"expires_at":       "2026-08-18T08:00:00Z",
			"verifier_version": "official-url-verifier/v1",
			"policy_version":   "official-url/v1",
		},
		"publicly_visible": true,
		"analysis_ready":   false,
		"subjects": []any{
			map[string]any{
				"id":   strictContractUUIDC,
				"slug": "oncology",
				"name": "Oncology",
			},
		},
		"curation": strictKnown(map[string]any{
			"decision":       "accepted",
			"matched_rules":  map[string]any{"q1": true},
			"evidence":       map[string]any{"jif": 58.7},
			"metric_year":    2025,
			"policy_name":    "biomedical",
			"policy_version": 1,
			"assessed_at":    "2026-07-18T06:00:00Z",
		}),
		"source_provenance": strictKnown([]any{
			map[string]any{
				"source":                       "pubmed",
				"event_key":                    "pubmed:123",
				"source_record_id":             strictContractUUIDA,
				"source_time":                  "2026-07-18T05:00:00Z",
				"normalization_policy_version": "normalization/v1",
				"scope_policy_version":         "scope/v1",
				"projection_policy_version":    "projection/v1",
			},
		}),
	}
}

func strictKnown(value any) map[string]any {
	return map[string]any{"state": "known", "value": value}
}

func strictMissing() map[string]any {
	return map[string]any{"state": "missing"}
}

func strictContractObject(root any, path ...any) map[string]any {
	value := strictContractLookup(root, path...)
	object, ok := value.(map[string]any)
	if !ok {
		panic("strict contract path is not an object")
	}
	return object
}

func strictContractArray(root any, path ...any) []any {
	value := strictContractLookup(root, path...)
	array, ok := value.([]any)
	if !ok {
		panic("strict contract path is not an array")
	}
	return array
}

func strictContractSet(root any, value any, path ...any) {
	if len(path) == 0 {
		panic("strict contract set path is empty")
	}
	parent := strictContractLookup(root, path[:len(path)-1]...)
	switch key := path[len(path)-1].(type) {
	case string:
		parent.(map[string]any)[key] = value
	case int:
		parent.([]any)[key] = value
	default:
		panic("strict contract set path key has unsupported type")
	}
}

func strictContractLookup(root any, path ...any) any {
	current := root
	for _, segment := range path {
		switch key := segment.(type) {
		case string:
			current = current.(map[string]any)[key]
		case int:
			current = current.([]any)[key]
		default:
			panic("strict contract path segment has unsupported type")
		}
	}
	return current
}
