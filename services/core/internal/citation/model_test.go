package citation

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSnapshotValidateRequiresExactSourceSpecificProvenance(t *testing.T) {
	t.Parallel()

	valid := Snapshot{
		WorkID:            uuid.New(),
		Source:            "openalex",
		ObservedAt:        time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		Count:             42,
		SourceRecordID:    uuid.New(),
		IngestionJobID:    uuid.New(),
		RetrievedAt:       time.Date(2026, time.July, 17, 8, 1, 0, 0, time.UTC),
		Coverage:          1,
		DefinitionVersion: "openalex-cited-by-count/v1",
		DatasetVersion:    "openalex-api/2026-07-17",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Snapshot.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   string
	}{
		{
			name: "negative count",
			mutate: func(snapshot *Snapshot) {
				snapshot.Count = -1
			},
			want: "count",
		},
		{
			name: "missing work",
			mutate: func(snapshot *Snapshot) {
				snapshot.WorkID = uuid.Nil
			},
			want: "work_id",
		},
		{
			name: "blank source",
			mutate: func(snapshot *Snapshot) {
				snapshot.Source = ""
			},
			want: "source",
		},
		{
			name: "untrimmed source",
			mutate: func(snapshot *Snapshot) {
				snapshot.Source = " openalex"
			},
			want: "source",
		},
		{
			name: "missing observation time",
			mutate: func(snapshot *Snapshot) {
				snapshot.ObservedAt = time.Time{}
			},
			want: "observed_at",
		},
		{
			name: "missing retrieval time",
			mutate: func(snapshot *Snapshot) {
				snapshot.RetrievedAt = time.Time{}
			},
			want: "retrieved_at",
		},
		{
			name: "coverage below zero",
			mutate: func(snapshot *Snapshot) {
				snapshot.Coverage = -0.01
			},
			want: "coverage",
		},
		{
			name: "coverage above one",
			mutate: func(snapshot *Snapshot) {
				snapshot.Coverage = 1.01
			},
			want: "coverage",
		},
		{
			name: "missing source record",
			mutate: func(snapshot *Snapshot) {
				snapshot.SourceRecordID = uuid.Nil
			},
			want: "source_record_id",
		},
		{
			name: "missing ingestion job",
			mutate: func(snapshot *Snapshot) {
				snapshot.IngestionJobID = uuid.Nil
			},
			want: "ingestion_job_id",
		},
		{
			name: "blank definition",
			mutate: func(snapshot *Snapshot) {
				snapshot.DefinitionVersion = ""
			},
			want: "definition_version",
		},
		{
			name: "untrimmed definition",
			mutate: func(snapshot *Snapshot) {
				snapshot.DefinitionVersion = " openalex-cited-by-count/v1"
			},
			want: "definition_version",
		},
		{
			name: "blank dataset version",
			mutate: func(snapshot *Snapshot) {
				snapshot.DatasetVersion = ""
			},
			want: "dataset_version",
		},
		{
			name: "untrimmed dataset version",
			mutate: func(snapshot *Snapshot) {
				snapshot.DatasetVersion = "openalex-api/2026-07-17 "
			},
			want: "dataset_version",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := valid
			test.mutate(&candidate)
			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"Snapshot.Validate() error = %v, want field %q",
					err,
					test.want,
				)
			}
		})
	}
}

func TestCitationEdgeValidatePreservesControlledIdentifiersAndProvenance(
	t *testing.T,
) {
	t.Parallel()

	edge := CitationEdge{
		CitingWorkID:      uuid.New(),
		CitedWorkID:       uuid.New(),
		CitingIdentifier:  "pmid:123",
		CitedIdentifier:   "doi:10.1000/example",
		Source:            "europepmc",
		SourceRecordID:    uuid.New(),
		IngestionJobID:    uuid.New(),
		RetrievedAt:       time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		DatasetVersion:    "europepmc/2026-07-17",
		DefinitionVersion: "citation-edge/v1",
	}
	if err := edge.Validate(); err != nil {
		t.Fatalf("valid CitationEdge.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*CitationEdge)
		want   string
	}{
		{
			name: "blank cited identifier",
			mutate: func(candidate *CitationEdge) {
				candidate.CitedIdentifier = ""
			},
			want: "cited_identifier",
		},
		{
			name: "untrimmed citing identifier",
			mutate: func(candidate *CitationEdge) {
				candidate.CitingIdentifier = " pmid:123"
			},
			want: "citing_identifier",
		},
		{
			name: "same controlled identifier",
			mutate: func(candidate *CitationEdge) {
				candidate.CitedIdentifier = candidate.CitingIdentifier
			},
			want: "self",
		},
		{
			name: "same resolved work",
			mutate: func(candidate *CitationEdge) {
				candidate.CitedWorkID = candidate.CitingWorkID
			},
			want: "self",
		},
		{
			name: "no resolved work",
			mutate: func(candidate *CitationEdge) {
				candidate.CitingWorkID = uuid.Nil
				candidate.CitedWorkID = uuid.Nil
			},
			want: "work_id",
		},
		{
			name: "blank source",
			mutate: func(candidate *CitationEdge) {
				candidate.Source = ""
			},
			want: "source",
		},
		{
			name: "missing source record",
			mutate: func(candidate *CitationEdge) {
				candidate.SourceRecordID = uuid.Nil
			},
			want: "source_record_id",
		},
		{
			name: "missing ingestion job",
			mutate: func(candidate *CitationEdge) {
				candidate.IngestionJobID = uuid.Nil
			},
			want: "ingestion_job_id",
		},
		{
			name: "missing retrieval time",
			mutate: func(candidate *CitationEdge) {
				candidate.RetrievedAt = time.Time{}
			},
			want: "retrieved_at",
		},
		{
			name: "blank dataset version",
			mutate: func(candidate *CitationEdge) {
				candidate.DatasetVersion = ""
			},
			want: "dataset_version",
		},
		{
			name: "blank definition version",
			mutate: func(candidate *CitationEdge) {
				candidate.DefinitionVersion = ""
			},
			want: "definition_version",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := edge
			test.mutate(&candidate)
			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"CitationEdge.Validate() error = %v, want %q",
					err,
					test.want,
				)
			}
		})
	}
}
