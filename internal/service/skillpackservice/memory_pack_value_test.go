package skillpackservice

import (
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestMemoryPackCanonicalValue(t *testing.T) {
	tests := []struct {
		name     string
		endpoint MemoryPackEndpoint
		want     any
		wantErr  string
	}{
		{name: "string", endpoint: MemoryPackEndpoint{ValueType: string(domain.ValueTypeString), Value: "Dense-Mem"}, want: "Dense-Mem"},
		{name: "date", endpoint: MemoryPackEndpoint{ValueType: string(domain.ValueTypeDate), Value: "2026-08-03"}, want: "2026-08-03"},
		{name: "date time", endpoint: MemoryPackEndpoint{ValueType: string(domain.ValueTypeDateTime), Value: "2026-08-03T12:00:00Z"}, want: "2026-08-03T12:00:00Z"},
		{name: "number", endpoint: MemoryPackEndpoint{ValueType: string(domain.ValueTypeNumber), Value: "20.5"}, want: float64(20.5)},
		{name: "boolean", endpoint: MemoryPackEndpoint{ValueType: string(domain.ValueTypeBoolean), Value: "true"}, want: true},
		{name: "invalid number", endpoint: MemoryPackEndpoint{ValueType: string(domain.ValueTypeNumber), Value: "twenty"}, wantErr: "must be a number"},
		{name: "not a number", endpoint: MemoryPackEndpoint{ValueType: string(domain.ValueTypeNumber), Value: "NaN"}, wantErr: "must be finite"},
		{name: "infinite number", endpoint: MemoryPackEndpoint{ValueType: string(domain.ValueTypeNumber), Value: "+Inf"}, wantErr: "must be finite"},
		{name: "invalid boolean", endpoint: MemoryPackEndpoint{ValueType: string(domain.ValueTypeBoolean), Value: "truthy"}, wantErr: "must be a boolean"},
		{name: "unsupported type", endpoint: MemoryPackEndpoint{ValueType: "json", Value: "{}"}, wantErr: "is unsupported"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := memoryPackCanonicalValue(test.endpoint)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("memoryPackCanonicalValue: %v", err)
			}
			if got != test.want {
				t.Fatalf("value = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestMemoryPackArtifactRejectsRelationshipValuesOutsidePublicBounds(t *testing.T) {
	valid := MemoryPackArtifact{
		Format: MemoryPackFormat,
		Name:   "value validation",
		Relationships: []MemoryPackRelationship{{
			ItemID:           "value-item",
			PredicateKey:     "costs",
			PredicateVersion: 1,
			Subject:          MemoryPackEndpoint{Ref: "widget", Kind: "entity", DisplayName: "Widget"},
			Object:           MemoryPackEndpoint{Ref: "cost", Kind: "value", ValueType: string(domain.ValueTypeNumber), Value: "20"},
		}},
	}
	tests := []struct {
		name    string
		mutate  func(*MemoryPackArtifact)
		wantErr string
	}{
		{
			name: "non-finite number",
			mutate: func(artifact *MemoryPackArtifact) {
				artifact.Relationships[0].Object.Value = "NaN"
			},
			wantErr: "must be finite",
		},
		{
			name: "long predicate",
			mutate: func(artifact *MemoryPackArtifact) {
				artifact.Relationships[0].PredicateKey = strings.Repeat("p", 129)
			},
			wantErr: "predicate_key exceeds 128 characters",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := cloneTestArtifact(valid)
			test.mutate(&artifact)
			err := validateMemoryPackArtifact(artifact)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateMemoryPackArtifact error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestMemoryPackRelationshipHintRejectsInvalidValueCanonicalization(t *testing.T) {
	_, err := MemoryPackRelationshipHint(
		MemoryPackArtifact{Name: "value validation"},
		MemoryPackRelationship{
			ItemID:       "value-item",
			Subject:      MemoryPackEndpoint{Kind: "entity", DisplayName: "Widget"},
			PredicateKey: "costs",
			Object:       MemoryPackEndpoint{Kind: "value", ValueType: string(domain.ValueTypeNumber), Value: "twenty"},
		},
		0,
	)
	if err == nil || !strings.Contains(err.Error(), "must be a number") {
		t.Fatalf("MemoryPackRelationshipHint error = %v", err)
	}
}
