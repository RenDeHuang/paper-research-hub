package abstractanalysis

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAbstractRouteSchemaIsStrictVersionedAndRequiresEveryField(t *testing.T) {
	t.Parallel()

	if SchemaVersion != "abstract-route/v1" {
		t.Fatalf("SchemaVersion = %q", SchemaVersion)
	}
	if SchemaName != "abstract_route" {
		t.Fatalf("SchemaName = %q", SchemaName)
	}

	var schema map[string]any
	if err := json.Unmarshal(SchemaJSON(), &schema); err != nil {
		t.Fatalf("SchemaJSON() is invalid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("root type = %#v, want object", schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Fatalf(
			"root additionalProperties = %#v, want false",
			schema["additionalProperties"],
		)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("root properties = %#v, want object", schema["properties"])
	}
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("root required = %#v, want array", schema["required"])
	}
	wantFields := FieldNames()
	if len(properties) != len(wantFields) ||
		len(required) != len(wantFields) {
		t.Fatalf(
			"schema properties/required = %d/%d, want %d",
			len(properties),
			len(required),
			len(wantFields),
		)
	}
	for _, name := range wantFields {
		if _, exists := properties[name]; !exists {
			t.Errorf("schema is missing property %q", name)
		}
		if !containsJSONStrings(required, name) {
			t.Errorf("schema required is missing %q", name)
		}
	}
}

func TestDecodeResultRejectsUnknownRootAndFieldProperties(t *testing.T) {
	t.Parallel()

	valid := supportedResultJSON()
	if _, err := DecodeResult([]byte(valid)); err != nil {
		t.Fatalf("DecodeResult(valid) error = %v", err)
	}

	for _, test := range []struct {
		name string
		raw  string
	}{
		{
			name: "unknown root field",
			raw: strings.TrimSuffix(valid, "}") +
				`,"unexpected":{"state":"not_reported","value":"","evidence":[]}}`,
		},
		{
			name: "unknown evidence field property",
			raw: strings.Replace(
				valid,
				`"domain":{"state":"supported","value":"medicine","evidence":["Patients"]}`,
				`"domain":{"state":"supported","value":"medicine","evidence":["Patients"],"confidence":1}`,
				1,
			),
		},
		{
			name: "trailing JSON",
			raw:  valid + `{}`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := DecodeResult([]byte(test.raw)); err == nil {
				t.Fatalf("DecodeResult() accepted %s", test.name)
			}
		})
	}
}

func TestResultStateShapesAreExact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		field EvidenceField
		want  string
	}{
		{
			name: "supported empty value",
			field: EvidenceField{
				State:    StateSupported,
				Evidence: []string{"Patients"},
			},
			want: "value",
		},
		{
			name: "supported empty evidence",
			field: EvidenceField{
				State: StateSupported,
				Value: "medicine",
			},
			want: "evidence",
		},
		{
			name: "not reported value",
			field: EvidenceField{
				State: StateNotReported,
				Value: "medicine",
			},
			want: "value",
		},
		{
			name: "not reported evidence",
			field: EvidenceField{
				State:    StateNotReported,
				Evidence: []string{"Patients"},
			},
			want: "evidence",
		},
		{
			name:  "unknown state",
			field: EvidenceField{State: "guessed"},
			want:  "state",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := test.field.ValidateShape("domain")
			if err == nil || !strings.Contains(
				strings.ToLower(err.Error()),
				test.want,
			) {
				t.Fatalf(
					"ValidateShape() error = %v, want containing %q",
					err,
					test.want,
				)
			}
		})
	}
}

func containsJSONStrings(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func supportedResultJSON() string {
	fields := make([]string, 0, len(FieldNames()))
	for index, name := range FieldNames() {
		if index == 0 {
			fields = append(
				fields,
				`"`+name+`":{"state":"supported","value":"medicine","evidence":["Patients"]}`,
			)
			continue
		}
		fields = append(
			fields,
			`"`+name+`":{"state":"not_reported","value":"","evidence":[]}`,
		)
	}
	return "{" + strings.Join(fields, ",") + "}"
}
