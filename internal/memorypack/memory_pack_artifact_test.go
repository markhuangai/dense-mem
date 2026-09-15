package memorypack

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

func TestMemoryPackArtifactCanonicalizationNormalizesAndHashesWithoutContentHash(t *testing.T) {
	artifact := validMemoryPackArtifact()
	artifact.Format = " " + MemoryPackFormat + " "
	artifact.Name = "  Team memories  "
	artifact.Description = "  exported  "
	artifact.ContentSHA256 = "stale"
	artifact.Relationships[0].SupportEvidenceIDs = []string{"evidence-1", " evidence-1 ", ""}
	artifact.Relationships[0].Subject.DisplayName = "  Dense-Mem  "

	canonical, hash, err := canonicalMemoryPackArtifact(artifact)
	if err != nil {
		t.Fatalf("canonical artifact: %v", err)
	}
	if len(canonical) == 0 || len(hash) != 64 {
		t.Fatalf("canonical result = %d bytes, hash %q", len(canonical), hash)
	}
	if strings.Contains(string(canonical), "stale") {
		t.Fatalf("canonical bytes retained content hash: %s", canonical)
	}

	marshaled, err := marshalMemoryPackArtifact(artifact)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	if !strings.Contains(string(marshaled), `"content_sha256":"stale"`) {
		t.Fatalf("marshal bytes omitted supplied content hash: %s", marshaled)
	}

	normalized := normalizeMemoryPackArtifact(artifact)
	if normalized.Format != MemoryPackFormat || normalized.Name != "Team memories" || normalized.Description != "exported" {
		t.Fatalf("normalized artifact = %#v", normalized)
	}
	if len(normalized.Relationships[0].SupportEvidenceIDs) != 1 || normalized.Relationships[0].SupportEvidenceIDs[0] != "evidence-1" {
		t.Fatalf("normalized support IDs = %#v", normalized.Relationships[0].SupportEvidenceIDs)
	}
}

func TestMemoryPackArtifactValidationRejectsMalformedInputs(t *testing.T) {
	base := validMemoryPackArtifact()
	tests := []struct {
		name   string
		mutate func(*MemoryPackArtifact)
		want   string
	}{
		{name: "format", mutate: func(a *MemoryPackArtifact) { a.Format = "other" }, want: "format must be"},
		{name: "name", mutate: func(a *MemoryPackArtifact) { a.Name = " " }, want: "name is required"},
		{name: "name too long", mutate: func(a *MemoryPackArtifact) { a.Name = strings.Repeat("x", maxMemoryPackNameBytes+1) }, want: "name exceeds"},
		{name: "description too long", mutate: func(a *MemoryPackArtifact) { a.Description = strings.Repeat("x", maxMemoryPackDescriptionBytes+1) }, want: "description exceeds"},
		{name: "relationships missing", mutate: func(a *MemoryPackArtifact) { a.Relationships = nil }, want: "relationships is required"},
		{name: "duplicate relationship", mutate: func(a *MemoryPackArtifact) { a.Relationships = append(a.Relationships, a.Relationships[0]) }, want: "duplicate item_id"},
		{name: "evidence ID missing", mutate: func(a *MemoryPackArtifact) { a.Evidence[0].EvidenceID = " " }, want: "evidence_id is required"},
		{name: "evidence content missing", mutate: func(a *MemoryPackArtifact) { a.Evidence[0].Content = " " }, want: "content is required"},
		{name: "duplicate evidence", mutate: func(a *MemoryPackArtifact) { a.Evidence = append(a.Evidence, a.Evidence[0]) }, want: "duplicate evidence_id"},
		{name: "support relationship missing", mutate: func(a *MemoryPackArtifact) { a.EvidenceSupports[0].RelationshipItemID = "other" }, want: "relationship item"},
		{name: "support evidence missing", mutate: func(a *MemoryPackArtifact) { a.EvidenceSupports[0].EvidenceID = "other" }, want: "evidence \"other\" is missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			artifact := cloneMemoryPackArtifact(base)
			tt.mutate(&artifact)
			err := validateMemoryPackArtifact(artifact)
			if !errors.Is(err, ErrInvalidArtifact) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMemoryPackRelationshipValidationHonorsEntityNameOption(t *testing.T) {
	item := validMemoryPackArtifact().Relationships[0]
	item.Subject.DisplayName = ""
	if err := validateMemoryPackRelationship(item); err == nil || !strings.Contains(err.Error(), "subject display_name") {
		t.Fatalf("missing subject name error = %v", err)
	}
	if err := validateMemoryPackRelationshipWithOptions(item, false); err != nil {
		t.Fatalf("name-free relationship validation: %v", err)
	}

	item = validMemoryPackArtifact().Relationships[0]
	item.Object = MemoryPackEndpoint{Ref: "object", Kind: "value", ValueType: string(domain.ValueTypeNumber), Value: "42.5"}
	if err := validateMemoryPackRelationship(item); err != nil {
		t.Fatalf("numeric value relationship validation: %v", err)
	}
	item.Object.Value = "nan"
	if err := validateMemoryPackRelationship(item); err == nil || !strings.Contains(err.Error(), "must be finite") {
		t.Fatalf("invalid numeric value error = %v", err)
	}
}

func TestMemoryPackRelationshipFromTraceAndCanonicalValues(t *testing.T) {
	entity := memoryPackRelationshipFromTrace(&tracecontract.RelationshipTraceRecord{
		SemanticGroupKey: "group-key",
		SubjectEntityID:  "subject-id",
		SubjectName:      "Subject",
		ObjectEntityID:   "object-id",
		ObjectEntityName: "Object",
		PredicateKey:     "uses",
		PredicateVersion: 2,
		OwnerProfileID:   "profile-id",
		Version:          3,
	})
	if entity.ItemID == "" || !strings.HasPrefix(entity.ItemID, "rel_") || entity.Object.Kind != "entity" {
		t.Fatalf("entity relationship = %#v", entity)
	}
	value := memoryPackRelationshipFromTrace(&tracecontract.RelationshipTraceRecord{
		RelationshipID: "relationship-id", ObjectValueID: "value-id", ObjectValueType: string(domain.ValueTypeBoolean), ObjectValue: "true",
	})
	if value.ItemID != "relationship-id" || value.Object.Kind != "value" || value.Object.ValueType != string(domain.ValueTypeBoolean) {
		t.Fatalf("value relationship = %#v", value)
	}

	values := []struct {
		typeName string
		value    string
		want     any
	}{
		{string(domain.ValueTypeString), "text", "text"},
		{string(domain.ValueTypeDate), "2026-01-01", "2026-01-01"},
		{string(domain.ValueTypeDateTime), "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"},
		{string(domain.ValueTypeNumber), "42.5", float64(42.5)},
		{string(domain.ValueTypeBoolean), "true", true},
	}
	for _, tt := range values {
		got, err := memoryPackCanonicalValue(MemoryPackEndpoint{ValueType: tt.typeName, Value: tt.value})
		if err != nil || got != tt.want {
			t.Fatalf("canonical %s = %#v, %v; want %#v", tt.typeName, got, err, tt.want)
		}
	}
	for _, tt := range []struct {
		typeName string
		value    string
		want     string
	}{
		{string(domain.ValueTypeNumber), "bad", "must be a number"},
		{string(domain.ValueTypeNumber), "NaN", "must be finite"},
		{string(domain.ValueTypeNumber), "Inf", "must be finite"},
		{string(domain.ValueTypeBoolean), "maybe", "must be a boolean"},
		{"unsupported", "x", "unsupported"},
	} {
		if tt.value == "NaN" || tt.value == "Inf" {
			value := math.NaN()
			if tt.value == "Inf" {
				value = math.Inf(1)
			}
			_ = value
		}
		_, err := memoryPackCanonicalValue(MemoryPackEndpoint{ValueType: tt.typeName, Value: tt.value})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("canonical invalid %s/%s error = %v, want %q", tt.typeName, tt.value, err, tt.want)
		}
	}
}

func TestMemoryPackHelpersNormalizeAndCopyValues(t *testing.T) {
	ids := MemoryPackSortedEvidenceIDs(map[string]MemoryPackEvidence{"b": {}, "a": {}})
	if strings.Join(ids, ",") != "a,b" {
		t.Fatalf("sorted evidence IDs = %#v", ids)
	}
	hash := memoryPackShortHash("same")
	if hash != memoryPackShortHash("same") || len(hash) != 16 {
		t.Fatalf("short hash is not stable and bounded")
	}
	for input, want := range map[string]string{
		" Team memories! ": "team-memories.memory-pack.json",
		"!!!":              "memory-pack.memory-pack.json",
		"one--two":         "one-two.memory-pack.json",
	} {
		if got := skillPackFilename(input); got != want {
			t.Errorf("skillPackFilename(%q) = %q, want %q", input, got, want)
		}
	}
	if got := uniqueStrings([]string{" a ", "", "a", "b"}); strings.Join(got, ",") != "a,b" {
		t.Fatalf("unique strings = %#v", got)
	}
	parsed := uuid.NewString()
	if canonicalMemoryPackRelationshipID(" "+parsed+" ") != parsed || canonicalMemoryPackRelationshipID("raw") != "raw" {
		t.Fatalf("canonical relationship IDs changed unexpectedly")
	}
	item := validMemoryPackArtifact().Relationships[0]
	omitMemoryPackEntityNames(&item)
	if item.Subject.DisplayName != "" || item.Object.DisplayName != "" {
		t.Fatalf("entity names were not omitted: %#v", item)
	}
	value := validMemoryPackArtifact().Relationships[0]
	value.Object = MemoryPackEndpoint{Kind: "value", DisplayName: "ignored"}
	omitMemoryPackEntityNames(&value)
	if value.Object.DisplayName != "ignored" {
		t.Fatalf("value display name was omitted")
	}
	copyMap := MemoryPackCopyMap(map[string]any{"key": "value"})
	copyMap["key"] = "changed"
	if MemoryPackCopyMap(nil) != nil || MemoryPackCopyMap(map[string]any{}) != nil {
		t.Fatalf("empty maps should copy to nil")
	}
	if copyMap["key"] != "changed" {
		t.Fatalf("copy map is not writable")
	}
	if got := MemoryPackSupportOmissions(false, []byte("artifact")); len(got) != 1 {
		t.Fatalf("support omission = %#v", got)
	}
	if MemoryPackSupportOmissions(true, []byte("artifact")) != nil || MemoryPackSupportOmissions(false, nil) != nil {
		t.Fatalf("unexpected support omission")
	}
	if !*boolPtr(true) {
		t.Fatal("boolPtr returned false")
	}

	teamID, ownerID := uuid.New(), uuid.New()
	if _, err := memoryPackActor(context.Background()); !errors.Is(err, ErrMemoryPackAuthContext) {
		t.Fatalf("missing actor error = %v", err)
	}
	actor, err := memoryPackActor(requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: ownerID}))
	if err != nil || actor.TeamID != teamID || actor.OwnerID != ownerID {
		t.Fatalf("actor = %#v, error = %v", actor, err)
	}
}

func validMemoryPackArtifact() MemoryPackArtifact {
	return MemoryPackArtifact{
		Format: MemoryPackFormat,
		PackID: "pack-1",
		Name:   "Pack",
		Source: MemoryPackSource{TeamID: "team-1", ExportedBy: "profile-1"},
		Relationships: []MemoryPackRelationship{{
			ItemID: "item-1", PredicateKey: "uses", PredicateVersion: 1,
			Subject: MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Subject"},
			Object:  MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "Object"},
		}},
		Evidence:         []MemoryPackEvidence{{EvidenceID: "evidence-1", Content: "support"}},
		EvidenceSupports: []MemoryPackEvidenceSupport{{RelationshipItemID: "item-1", EvidenceID: "evidence-1"}},
	}
}

func cloneMemoryPackArtifact(value MemoryPackArtifact) MemoryPackArtifact {
	copy := value
	copy.Relationships = append([]MemoryPackRelationship(nil), value.Relationships...)
	copy.Evidence = append([]MemoryPackEvidence(nil), value.Evidence...)
	copy.EvidenceSupports = append([]MemoryPackEvidenceSupport(nil), value.EvidenceSupports...)
	return copy
}
