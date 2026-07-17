package citation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Snapshot struct {
	WorkID            uuid.UUID
	Source            string
	ObservedAt        time.Time
	Count             int64
	SourceRecordID    uuid.UUID
	IngestionJobID    uuid.UUID
	RetrievedAt       time.Time
	Coverage          float64
	DefinitionVersion string
	DatasetVersion    string
}

func (snapshot Snapshot) Validate() error {
	if snapshot.WorkID == uuid.Nil {
		return errors.New("work_id is required")
	}
	if err := validateControlledText("source", snapshot.Source); err != nil {
		return err
	}
	if snapshot.ObservedAt.IsZero() {
		return errors.New("observed_at is required")
	}
	if snapshot.Count < 0 {
		return errors.New("count must not be negative")
	}
	if snapshot.SourceRecordID == uuid.Nil {
		return errors.New("source_record_id is required")
	}
	if snapshot.IngestionJobID == uuid.Nil {
		return errors.New("ingestion_job_id is required")
	}
	if snapshot.RetrievedAt.IsZero() {
		return errors.New("retrieved_at is required")
	}
	if snapshot.Coverage < 0 || snapshot.Coverage > 1 {
		return errors.New("coverage must be between zero and one")
	}
	if err := validateControlledText(
		"definition_version",
		snapshot.DefinitionVersion,
	); err != nil {
		return err
	}
	return validateControlledText("dataset_version", snapshot.DatasetVersion)
}

type CitationEdge struct {
	CitingWorkID      uuid.UUID
	CitedWorkID       uuid.UUID
	CitingIdentifier  string
	CitedIdentifier   string
	Source            string
	SourceRecordID    uuid.UUID
	IngestionJobID    uuid.UUID
	RetrievedAt       time.Time
	DatasetVersion    string
	DefinitionVersion string
}

func (edge CitationEdge) Validate() error {
	if edge.CitingWorkID == uuid.Nil && edge.CitedWorkID == uuid.Nil {
		return errors.New("at least one citing_work_id or cited_work_id is required")
	}
	if err := validateControlledText(
		"citing_identifier",
		edge.CitingIdentifier,
	); err != nil {
		return err
	}
	if err := validateControlledText(
		"cited_identifier",
		edge.CitedIdentifier,
	); err != nil {
		return err
	}
	if edge.CitingIdentifier == edge.CitedIdentifier {
		return errors.New("self citation edge identifiers are not allowed")
	}
	if edge.CitingWorkID != uuid.Nil &&
		edge.CitingWorkID == edge.CitedWorkID {
		return errors.New("self citation edge Work IDs are not allowed")
	}
	if err := validateControlledText("source", edge.Source); err != nil {
		return err
	}
	if edge.SourceRecordID == uuid.Nil {
		return errors.New("source_record_id is required")
	}
	if edge.IngestionJobID == uuid.Nil {
		return errors.New("ingestion_job_id is required")
	}
	if edge.RetrievedAt.IsZero() {
		return errors.New("retrieved_at is required")
	}
	if err := validateControlledText(
		"dataset_version",
		edge.DatasetVersion,
	); err != nil {
		return err
	}
	return validateControlledText(
		"definition_version",
		edge.DefinitionVersion,
	)
}

func validateControlledText(field string, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty and trimmed", field)
	}
	return nil
}
