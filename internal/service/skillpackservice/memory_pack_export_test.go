package skillpackservice

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type exportSemanticStub struct {
	record *repository.RelationshipTraceRecord
	result *repository.RelationshipTraceResult
	err    error
	input  repository.TraceRelationshipInput
}

func (s *exportSemanticStub) TraceRelationship(_ context.Context, input repository.TraceRelationshipInput) (*repository.RelationshipTraceResult, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &repository.RelationshipTraceResult{Relationship: s.record}, nil
}

func TestMemoryPackExportOnlyProducesCanonicalV24Artifact(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	relationshipID := uuid.NewString()
	svc := NewMemoryPackService(MemoryPackDependencies{
		Semantic: &exportSemanticStub{record: &repository.RelationshipTraceRecord{
			RelationshipID:   relationshipID,
			OwnerProfileID:   profileID.String(),
			SubjectEntityID:  uuid.NewString(),
			SubjectName:      "Dense-Mem",
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectEntityID:   uuid.NewString(),
			ObjectEntityName: "PostgreSQL",
			Status:           string(domain.RelationshipStatusActive),
			Polarity:         "+",
			Version:          1,
		}},
	})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID})
	includeSupport := false
	result, err := svc.Export(ctx, ExportRequest{Name: "database choices", RelationshipIDs: []string{relationshipID}, IncludeSupport: &includeSupport})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.Artifact.Format != MemoryPackFormat || result.SHA256 == "" {
		t.Fatalf("artifact = %#v", result.Artifact)
	}
	if !strings.Contains(result.CanonicalJSON, `"format":"`+MemoryPackFormat+`"`) {
		t.Fatalf("canonical artifact missing current format: %s", result.CanonicalJSON)
	}
	if strings.Contains(result.CanonicalJSON, "ingest_id") || strings.Contains(result.CanonicalJSON, "legacy_schema_version") {
		t.Fatalf("canonical artifact contains removed import/legacy fields: %s", result.CanonicalJSON)
	}
	if len(result.Omissions) != 1 {
		t.Fatalf("omissions = %#v, want support omission", result.Omissions)
	}
	if got := svc.(*memoryPackService).deps.Semantic.(*exportSemanticStub).input.TeamID; got != teamID.String() {
		t.Fatalf("trace team_id = %q, want %q", got, teamID)
	}
}

func TestMemoryPackArtifactRejectsNonFiniteValue(t *testing.T) {
	err := validateMemoryPackArtifact(MemoryPackArtifact{
		Format: MemoryPackFormat,
		Name:   "invalid",
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item",
			PredicateKey:     "costs",
			PredicateVersion: 1,
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Widget"},
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "value", ValueType: string(domain.ValueTypeNumber), Value: "NaN"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "must be finite") {
		t.Fatalf("validateMemoryPackArtifact error = %v", err)
	}
}

func TestMemoryPackExportRejectsMissingInputsAndUnavailableRelationships(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID})
	active := &repository.RelationshipTraceRecord{RelationshipID: "rel-1", Status: string(domain.RelationshipStatusActive)}
	base := NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{record: active}})
	noActor := context.Background()
	cases := []struct {
		name string
		svc  MemoryPackService
		ctx  context.Context
		req  ExportRequest
		want string
	}{
		{name: "actor required", svc: base, ctx: noActor, req: ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}, want: "authenticated actor"},
		{name: "semantic reader required", svc: NewMemoryPackService(MemoryPackDependencies{}), ctx: ctx, req: ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}, want: "semantic reader"},
		{name: "name required", svc: base, ctx: ctx, req: ExportRequest{RelationshipIDs: []string{"rel-1"}}, want: "name is required"},
		{name: "relationship ids required", svc: base, ctx: ctx, req: ExportRequest{Name: "pack"}, want: "relationship_ids is required"},
		{name: "trace failure", svc: NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{err: errors.New("trace failed")}}), ctx: ctx, req: ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}, want: "trace failed"},
		{name: "trace relationship not found", svc: NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{err: repository.ErrTraceRelationshipNotFound}}), ctx: ctx, req: ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}, want: "trace relationship not found"},
		{name: "relationship missing", svc: NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{result: &repository.RelationshipTraceResult{}}}), ctx: ctx, req: ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}, want: "not found"},
		{name: "relationship inactive", svc: NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{record: &repository.RelationshipTraceRecord{RelationshipID: "rel-1", Status: string(domain.RelationshipStatusSuperseded)}}}), ctx: ctx, req: ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}, want: "not active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.svc.Export(tc.ctx, tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Export error = %v, want substring %q", err, tc.want)
			}
			if tc.name == "relationship inactive" && !errors.Is(err, ErrMemoryPackRelationshipNotActive) {
				t.Fatalf("Export error = %v, want ErrMemoryPackRelationshipNotActive", err)
			}
			if tc.name == "relationship missing" && !errors.Is(err, repository.ErrTraceRelationshipNotFound) {
				t.Fatalf("Export error = %v, want repository.ErrTraceRelationshipNotFound", err)
			}
		})
	}
}

func TestMemoryPackExportIncludesSupportAndNormalizesIDs(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	relationshipID := uuid.NewString()
	trace := &repository.RelationshipTraceResult{
		Relationship: &repository.RelationshipTraceRecord{
			RelationshipID:   relationshipID,
			OwnerProfileID:   profileID.String(),
			SemanticGroupKey: "group-1",
			SubjectEntityID:  "subject-id",
			SubjectName:      "Dense-Mem",
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectValueID:    "value-id",
			ObjectValueType:  string(domain.ValueTypeNumber),
			ObjectValue:      "42",
			Status:           string(domain.RelationshipStatusActive),
		},
		EvidenceSupports: []repository.RelationshipEvidenceSupportRecord{
			{FragmentID: "evidence-1", Quote: "Dense-Mem uses 42", SpanStart: 0, SpanEnd: 17, Metadata: map[string]any{"source": "test"}},
			{FragmentID: "evidence-1"},
		},
		EvidenceFragments: []repository.TraceEvidenceFragment{
			{FragmentID: "evidence-1", Content: "Dense-Mem uses 42", ContentHash: "hash", SourceType: "manual", Authority: "primary", SourceRef: "ref", SourceKey: "source", SourceRevisionID: "rev", Labels: []string{"one"}, Metadata: map[string]any{"key": "value"}},
			{FragmentID: ""},
		},
	}
	includeSupport := true
	svc := NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{result: trace}, Now: func() time.Time { return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC) }})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID})
	result, err := svc.Export(ctx, ExportRequest{Name: "  Numeric choices  ", RelationshipIDs: []string{relationshipID, relationshipID}, IncludeSupport: &includeSupport})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.ItemCount != 1 || len(result.Artifact.Evidence) != 1 || len(result.Artifact.EvidenceSupports) != 2 {
		t.Fatalf("export counts = item %d evidence %d supports %d", result.ItemCount, len(result.Artifact.Evidence), len(result.Artifact.EvidenceSupports))
	}
	if got := result.Artifact.Relationships[0].Object.Value; got != "42" {
		t.Fatalf("value object = %q", got)
	}
	if len(result.Artifact.Relationships[0].SupportEvidenceIDs) != 1 || result.Omissions != nil {
		t.Fatalf("support projection = %#v omissions %#v", result.Artifact.Relationships[0].SupportEvidenceIDs, result.Omissions)
	}
}

func TestMemoryPackArtifactValidationBranches(t *testing.T) {
	valid := func() MemoryPackArtifact {
		return MemoryPackArtifact{
			Format: MemoryPackFormat,
			Name:   "pack",
			Relationships: []MemoryPackRelationship{{
				ItemID: "item-1", PredicateKey: "uses", PredicateVersion: 1,
				Subject: MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Subject"},
				Object:  MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "Object"},
			}},
			Evidence: []MemoryPackEvidence{{EvidenceID: "evidence-1", Content: "content"}},
		}
	}
	cases := []struct {
		name   string
		mutate func(*MemoryPackArtifact)
		want   string
	}{
		{"format", func(a *MemoryPackArtifact) { a.Format = "old" }, "format must be"},
		{"name", func(a *MemoryPackArtifact) { a.Name = "  " }, "name is required"},
		{"long name", func(a *MemoryPackArtifact) { a.Name = strings.Repeat("n", 257) }, "name exceeds"},
		{"long description", func(a *MemoryPackArtifact) { a.Description = strings.Repeat("d", 1025) }, "description exceeds"},
		{"relationships", func(a *MemoryPackArtifact) { a.Relationships = nil }, "relationships is required"},
		{"relationship item id", func(a *MemoryPackArtifact) { a.Relationships[0].ItemID = "" }, "item_id is required"},
		{"predicate", func(a *MemoryPackArtifact) { a.Relationships[0].PredicateKey = "" }, "predicate_key is required"},
		{"predicate length", func(a *MemoryPackArtifact) { a.Relationships[0].PredicateKey = strings.Repeat("p", 129) }, "predicate_key exceeds"},
		{"predicate version", func(a *MemoryPackArtifact) { a.Relationships[0].PredicateVersion = 0 }, "predicate_version"},
		{"subject", func(a *MemoryPackArtifact) { a.Relationships[0].Subject.DisplayName = "" }, "subject ref and display_name"},
		{"object ref", func(a *MemoryPackArtifact) { a.Relationships[0].Object.Ref = "" }, "object ref"},
		{"object value", func(a *MemoryPackArtifact) {
			a.Relationships[0].Object = MemoryPackEndpoint{Ref: "value", Kind: "value"}
		}, "object value and value_type"},
		{"object value type", func(a *MemoryPackArtifact) {
			a.Relationships[0].Object = MemoryPackEndpoint{Ref: "value", Kind: "value", Value: "1", ValueType: "unsupported"}
		}, "unsupported"},
		{"evidence id", func(a *MemoryPackArtifact) { a.Evidence[0].EvidenceID = "" }, "evidence_id is required"},
		{"evidence content", func(a *MemoryPackArtifact) { a.Evidence[0].Content = "  " }, "content is required"},
		{"duplicate evidence", func(a *MemoryPackArtifact) { a.Evidence = append(a.Evidence, a.Evidence[0]) }, "duplicate evidence_id"},
		{"duplicate item", func(a *MemoryPackArtifact) { a.Relationships = append(a.Relationships, a.Relationships[0]) }, "duplicate item_id"},
		{"support relationship", func(a *MemoryPackArtifact) {
			a.EvidenceSupports = []MemoryPackEvidenceSupport{{RelationshipItemID: "missing", EvidenceID: "evidence-1"}}
		}, "relationship item"},
		{"support evidence", func(a *MemoryPackArtifact) {
			a.EvidenceSupports = []MemoryPackEvidenceSupport{{RelationshipItemID: "item-1", EvidenceID: "missing"}}
		}, "evidence \"missing\" is missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact := valid()
			tc.mutate(&artifact)
			err := validateMemoryPackArtifact(artifact)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestMemoryPackHelpersAndCanonicalValues(t *testing.T) {
	if _, err := memoryPackActor(context.Background()); !errors.Is(err, ErrMemoryPackAuthContext) {
		t.Fatalf("memoryPackActor error = %v", err)
	}
	values := map[string]MemoryPackEvidence{"b": {}, "a": {}}
	if got := MemoryPackSortedEvidenceIDs(values); !sort.StringsAreSorted(got) || strings.Join(got, ",") != "a,b" {
		t.Fatalf("sorted IDs = %#v", got)
	}
	if got := skillPackFilename("  My Pack!  "); got != "my-pack.memory-pack.json" {
		t.Fatalf("filename = %q", got)
	}
	if got := skillPackFilename("!!!"); got != "memory-pack.memory-pack.json" {
		t.Fatalf("empty filename = %q", got)
	}
	if got := uniqueStrings([]string{" a ", "a", "", "b"}); strings.Join(got, ",") != "a,b" {
		t.Fatalf("unique strings = %#v", got)
	}
	copy := MemoryPackCopyMap(map[string]any{"key": "value"})
	copy["key"] = "changed"
	if MemoryPackCopyMap(nil) != nil || copy["key"] != "changed" {
		t.Fatalf("copy map = %#v", copy)
	}
	if MemoryPackSupportOmissions(true, []byte("x")) != nil || len(MemoryPackSupportOmissions(false, []byte("x"))) != 1 || MemoryPackSupportOmissions(false, nil) != nil {
		t.Fatalf("support omissions did not preserve include semantics")
	}
	for _, tc := range []struct {
		typ  string
		text string
		want any
	}{
		{string(domain.ValueTypeString), "hello", "hello"},
		{string(domain.ValueTypeDate), "2026-01-01", "2026-01-01"},
		{string(domain.ValueTypeDateTime), "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"},
		{string(domain.ValueTypeNumber), "12.5", 12.5},
		{string(domain.ValueTypeBoolean), "true", true},
	} {
		got, err := memoryPackCanonicalValue(MemoryPackEndpoint{ValueType: tc.typ, Value: tc.text})
		if err != nil || got != tc.want {
			t.Fatalf("canonical %s = %#v, %v; want %#v", tc.typ, got, err, tc.want)
		}
	}
	for _, text := range []string{"NaN", "Inf"} {
		if _, err := memoryPackCanonicalValue(MemoryPackEndpoint{ValueType: string(domain.ValueTypeNumber), Value: text}); err == nil || !strings.Contains(err.Error(), "finite") {
			t.Fatalf("canonical non-finite %q error = %v", text, err)
		}
	}
	if _, err := memoryPackCanonicalValue(MemoryPackEndpoint{ValueType: string(domain.ValueTypeBoolean), Value: "maybe"}); err == nil || !strings.Contains(err.Error(), "boolean") {
		t.Fatalf("canonical boolean error = %v", err)
	}
	if _, err := memoryPackCanonicalValue(MemoryPackEndpoint{ValueType: string(domain.ValueTypeNumber), Value: "nope"}); err == nil || !strings.Contains(err.Error(), "number") {
		t.Fatalf("canonical number error = %v", err)
	}
	if _, err := memoryPackCanonicalValue(MemoryPackEndpoint{ValueType: "object", Value: "x"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("canonical unsupported error = %v", err)
	}
}
