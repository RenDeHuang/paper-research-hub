package abstractanalysis

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	SchemaName    = "abstract_route"
	SchemaVersion = "abstract-route/v1"
)

type FieldState string

const (
	StateSupported   FieldState = "supported"
	StateNotReported FieldState = "not_reported"
)

type EvidenceField struct {
	State    FieldState `json:"state"`
	Value    string     `json:"value"`
	Evidence []string   `json:"evidence"`
}

type Result struct {
	Domain               EvidenceField `json:"domain"`
	ResearchProblem      EvidenceField `json:"research_problem"`
	ResearchPurpose      EvidenceField `json:"research_purpose"`
	ResearchObjects      EvidenceField `json:"research_objects"`
	ContentType          EvidenceField `json:"content_type"`
	ResearchMode         EvidenceField `json:"research_mode"`
	StudyDesign          EvidenceField `json:"study_design"`
	DataOrSamples        EvidenceField `json:"data_or_samples"`
	CoreMethods          EvidenceField `json:"core_methods"`
	TechnicalRoute       EvidenceField `json:"technical_route"`
	ValidationStrategy   EvidenceField `json:"validation_strategy"`
	MainFindings         EvidenceField `json:"main_findings"`
	InnovationPoints     EvidenceField `json:"innovation_points"`
	ApplicationDirection EvidenceField `json:"application_direction"`
	LimitationsReported  EvidenceField `json:"limitations_reported"`
}

var abstractRouteFieldNames = []string{
	"domain",
	"research_problem",
	"research_purpose",
	"research_objects",
	"content_type",
	"research_mode",
	"study_design",
	"data_or_samples",
	"core_methods",
	"technical_route",
	"validation_strategy",
	"main_findings",
	"innovation_points",
	"application_direction",
	"limitations_reported",
}

var abstractRouteSchemaJSON = buildSchemaJSON()

func FieldNames() []string {
	return slices.Clone(abstractRouteFieldNames)
}

func SchemaJSON() json.RawMessage {
	return slices.Clone(abstractRouteSchemaJSON)
}

func DecodeResult(raw []byte) (Result, error) {
	if err := validateRequiredJSONShape(raw); err != nil {
		return Result{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result Result
	if err := decoder.Decode(&result); err != nil {
		return Result{}, fmt.Errorf("decode abstract route result: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Result{}, err
	}
	if err := result.ValidateShape(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (result Result) ValidateShape() error {
	for _, field := range result.fields() {
		if err := field.Value.ValidateShape(field.Name); err != nil {
			return err
		}
	}
	return nil
}

func (field EvidenceField) ValidateShape(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("abstract route field name is required")
	}
	switch field.State {
	case StateSupported:
		if strings.TrimSpace(field.Value) == "" {
			return fmt.Errorf(
				"abstract route field %s supported value is required",
				name,
			)
		}
		if field.Value != strings.TrimSpace(field.Value) {
			return fmt.Errorf(
				"abstract route field %s value must be trimmed",
				name,
			)
		}
		if len(field.Evidence) == 0 {
			return fmt.Errorf(
				"abstract route field %s supported evidence is required",
				name,
			)
		}
	case StateNotReported:
		if field.Value != "" {
			return fmt.Errorf(
				"abstract route field %s not_reported value must be empty",
				name,
			)
		}
		if len(field.Evidence) != 0 {
			return fmt.Errorf(
				"abstract route field %s not_reported evidence must be empty",
				name,
			)
		}
	default:
		return fmt.Errorf(
			"abstract route field %s has invalid state %q",
			name,
			field.State,
		)
	}
	return nil
}

type namedEvidenceField struct {
	Name  string
	Value EvidenceField
}

func (result Result) fields() []namedEvidenceField {
	return []namedEvidenceField{
		{Name: "domain", Value: result.Domain},
		{Name: "research_problem", Value: result.ResearchProblem},
		{Name: "research_purpose", Value: result.ResearchPurpose},
		{Name: "research_objects", Value: result.ResearchObjects},
		{Name: "content_type", Value: result.ContentType},
		{Name: "research_mode", Value: result.ResearchMode},
		{Name: "study_design", Value: result.StudyDesign},
		{Name: "data_or_samples", Value: result.DataOrSamples},
		{Name: "core_methods", Value: result.CoreMethods},
		{Name: "technical_route", Value: result.TechnicalRoute},
		{Name: "validation_strategy", Value: result.ValidationStrategy},
		{Name: "main_findings", Value: result.MainFindings},
		{Name: "innovation_points", Value: result.InnovationPoints},
		{
			Name:  "application_direction",
			Value: result.ApplicationDirection,
		},
		{Name: "limitations_reported", Value: result.LimitationsReported},
	}
}

func buildSchemaJSON() json.RawMessage {
	notReported := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"state": map[string]any{
				"type": "string",
				"enum": []string{string(StateNotReported)},
			},
			"value": map[string]any{
				"type": "string",
				"enum": []string{""},
			},
			"evidence": map[string]any{
				"type":     "array",
				"maxItems": 0,
				"items": map[string]any{
					"type": "string",
				},
			},
		},
		"required": []string{"state", "value", "evidence"},
	}
	supported := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"state": map[string]any{
				"type": "string",
				"enum": []string{string(StateSupported)},
			},
			"value": map[string]any{
				"type":      "string",
				"minLength": 1,
			},
			"evidence": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
			},
		},
		"required": []string{"state", "value", "evidence"},
	}

	properties := make(map[string]any, len(abstractRouteFieldNames))
	for _, name := range abstractRouteFieldNames {
		properties[name] = map[string]any{
			"$ref": "#/$defs/evidence_field",
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             abstractRouteFieldNames,
		"$defs": map[string]any{
			"evidence_field": map[string]any{
				"anyOf": []any{supported, notReported},
			},
		},
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("marshal abstract route schema: %v", err))
	}
	return encoded
}

func validateRequiredJSONShape(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var root map[string]json.RawMessage
	if err := decoder.Decode(&root); err != nil {
		return fmt.Errorf("decode abstract route JSON object: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	if len(root) != len(abstractRouteFieldNames) {
		return fmt.Errorf(
			"abstract route result has %d root fields, want %d",
			len(root),
			len(abstractRouteFieldNames),
		)
	}
	for _, name := range abstractRouteFieldNames {
		rawField, exists := root[name]
		if !exists {
			return fmt.Errorf(
				"abstract route result is missing required field %s",
				name,
			)
		}
		var field map[string]json.RawMessage
		if err := json.Unmarshal(rawField, &field); err != nil {
			return fmt.Errorf(
				"decode abstract route field %s: %w",
				name,
				err,
			)
		}
		if len(field) != 3 {
			return fmt.Errorf(
				"abstract route field %s must contain state, value, and evidence",
				name,
			)
		}
		for _, key := range []string{"state", "value", "evidence"} {
			if _, exists := field[key]; !exists {
				return fmt.Errorf(
					"abstract route field %s is missing %s",
					name,
					key,
				)
			}
		}
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing abstract route JSON: %w", err)
	}
	return errors.New("abstract route result must contain exactly one JSON value")
}
