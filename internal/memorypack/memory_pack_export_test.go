package memorypack

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

// The v2.4 files are byte-for-byte artifact locks. Keep them in the
// capability package so a format change has to update the owning tests.
//
//go:embed testdata/v2.4/*.json
var memoryPackV24Fixtures embed.FS

type exportSemanticStub struct {
	record *tracecontract.RelationshipTraceRecord
	result *tracecontract.RelationshipTraceResult
	err    error
	input  tracecontract.Input
}

type fixtureTrace struct {
	Relationship      *tracecontract.RelationshipTraceRecord            `json:"relationship"`
	EvidenceSupports  []tracecontract.RelationshipEvidenceSupportRecord `json:"evidence_supports"`
	EvidenceFragments []tracecontract.TraceEvidenceFragment             `json:"evidence_fragments"`
}

type memoryPackV24FixtureManifest struct {
	Now             string                     `json:"now"`
	TeamID          string                     `json:"team_id"`
	OwnerProfileID  string                     `json:"owner_profile_id"`
	Name            string                     `json:"name"`
	Description     string                     `json:"description"`
	RelationshipIDs []string                   `json:"relationship_ids"`
	Traces          map[string]fixtureTrace    `json:"traces"`
	Cases           []memoryPackV24FixtureCase `json:"cases"`
}

type memoryPackV24FixtureCase struct {
	Name               string   `json:"name"`
	IncludeEvidence    *bool    `json:"include_evidence"`
	IncludeEntityNames *bool    `json:"include_entity_names"`
	ArtifactFile       string   `json:"artifact_file"`
	SHA256             string   `json:"sha256"`
	Filename           string   `json:"filename"`
	ItemCount          int      `json:"item_count"`
	EvidenceCount      int      `json:"evidence_count"`
	SupportCount       int      `json:"support_count"`
	Omissions          []string `json:"omissions"`
}

type fixtureSemanticReader struct {
	results map[string]*tracecontract.RelationshipTraceResult
	inputs  []tracecontract.Input
}

func (s *fixtureSemanticReader) TraceRelationship(_ context.Context, input tracecontract.Input) (*tracecontract.RelationshipTraceResult, error) {
	s.inputs = append(s.inputs, input)
	result, ok := s.results[input.RelationshipID]
	if !ok {
		return nil, tracecontract.ErrRelationshipNotFound
	}
	return result, nil
}

func (s *exportSemanticStub) TraceRelationship(_ context.Context, input tracecontract.Input) (*tracecontract.RelationshipTraceResult, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &tracecontract.RelationshipTraceResult{Relationship: s.record}, nil
}

func TestMemoryPackV24FixturesMatchEveryExportOption(t *testing.T) {
	manifestBytes, err := memoryPackV24Fixtures.ReadFile("testdata/v2.4/manifest.json")
	if err != nil {
		t.Fatalf("read v2.4 manifest: %v", err)
	}
	var manifest memoryPackV24FixtureManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode v2.4 manifest: %v", err)
	}
	now, err := time.Parse(time.RFC3339Nano, manifest.Now)
	if err != nil {
		t.Fatalf("parse fixture time: %v", err)
	}
	teamID, err := uuid.Parse(manifest.TeamID)
	if err != nil {
		t.Fatalf("parse fixture team ID: %v", err)
	}
	ownerID, err := uuid.Parse(manifest.OwnerProfileID)
	if err != nil {
		t.Fatalf("parse fixture owner ID: %v", err)
	}
	results := make(map[string]*tracecontract.RelationshipTraceResult, len(manifest.Traces))
	for relationshipID, fixture := range manifest.Traces {
		if fixture.Relationship == nil {
			t.Fatalf("fixture trace %q has no relationship", relationshipID)
		}
		results[relationshipID] = &tracecontract.RelationshipTraceResult{
			Relationship:      fixture.Relationship,
			EvidenceSupports:  fixture.EvidenceSupports,
			EvidenceFragments: fixture.EvidenceFragments,
		}
	}

	for _, fixtureCase := range manifest.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			artifactBytes, err := memoryPackV24Fixtures.ReadFile("testdata/v2.4/" + fixtureCase.ArtifactFile)
			if err != nil {
				t.Fatalf("read expected artifact: %v", err)
			}
			expectedJSON := strings.TrimSuffix(string(artifactBytes), "\n")
			var expectedArtifact MemoryPackArtifact
			if err := json.Unmarshal([]byte(expectedJSON), &expectedArtifact); err != nil {
				t.Fatalf("decode expected artifact: %v", err)
			}
			for i := range expectedArtifact.Relationships {
				if expectedArtifact.Relationships[i].SupportEvidenceIDs == nil {
					expectedArtifact.Relationships[i].SupportEvidenceIDs = []string{}
				}
			}

			reader := &fixtureSemanticReader{results: results}
			svc := NewMemoryPackService(MemoryPackDependencies{
				Semantic: reader,
				Now:      func() time.Time { return now },
			})
			ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: ownerID})
			result, err := svc.Export(ctx, ExportRequest{
				Name:               manifest.Name,
				Description:        manifest.Description,
				RelationshipIDs:    manifest.RelationshipIDs,
				IncludeSupport:     fixtureCase.IncludeEvidence,
				IncludeEntityNames: fixtureCase.IncludeEntityNames,
			})
			if err != nil {
				t.Fatalf("Export: %v", err)
			}
			if result.CanonicalJSON != expectedJSON {
				t.Fatalf("canonical artifact mismatch:\n got: %s\nwant: %s", result.CanonicalJSON, expectedJSON)
			}
			if result.SHA256 != fixtureCase.SHA256 || result.SHA256 != expectedArtifact.ContentSHA256 {
				t.Fatalf("artifact hash = %q, want manifest %q and artifact %q", result.SHA256, fixtureCase.SHA256, expectedArtifact.ContentSHA256)
			}
			if !reflect.DeepEqual(result.Artifact, expectedArtifact) {
				t.Fatalf("artifact projection = %#v, want %#v", result.Artifact, expectedArtifact)
			}
			if result.Filename != fixtureCase.Filename || result.ItemCount != fixtureCase.ItemCount ||
				len(result.Artifact.Evidence) != fixtureCase.EvidenceCount ||
				len(result.Artifact.EvidenceSupports) != fixtureCase.SupportCount ||
				!reflect.DeepEqual(append([]string(nil), result.Omissions...), append([]string(nil), fixtureCase.Omissions...)) {
				t.Fatalf("result metadata = filename %q, items %d, evidence %d, supports %d, omissions %#v; want filename %q, items %d, evidence %d, supports %d, omissions %#v",
					result.Filename, result.ItemCount, len(result.Artifact.Evidence), len(result.Artifact.EvidenceSupports), result.Omissions,
					fixtureCase.Filename, fixtureCase.ItemCount, fixtureCase.EvidenceCount, fixtureCase.SupportCount, fixtureCase.Omissions)
			}
			if len(reader.inputs) != 2 {
				t.Fatalf("trace calls = %d, want 2 after relationship ID normalization", len(reader.inputs))
			}
			wantIncludeEvidence := fixtureCase.IncludeEvidence == nil || *fixtureCase.IncludeEvidence
			for _, input := range reader.inputs {
				if input.TeamID != manifest.TeamID || input.MaxEvents != maxTraceEvents || input.MaxFragmentContentRunes != maxFragmentContentRunes ||
					input.IncludeEvidenceContent == nil || *input.IncludeEvidenceContent != wantIncludeEvidence {
					t.Fatalf("trace input = %#v, want team %q, include_evidence %t, bounds %d/%d", input, manifest.TeamID, wantIncludeEvidence, maxTraceEvents, maxFragmentContentRunes)
				}
			}
		})
	}
}

func TestMemoryPackExportOnlyProducesCanonicalV24Artifact(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	relationshipID := uuid.NewString()
	svc := NewMemoryPackService(MemoryPackDependencies{
		Semantic: &exportSemanticStub{record: &tracecontract.RelationshipTraceRecord{
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
	active := &tracecontract.RelationshipTraceRecord{RelationshipID: "rel-1", Status: string(domain.RelationshipStatusActive)}
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
		{name: "trace relationship not found", svc: NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{err: tracecontract.ErrRelationshipNotFound}}), ctx: ctx, req: ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}, want: "trace relationship not found"},
		{name: "relationship missing", svc: NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{result: &tracecontract.RelationshipTraceResult{}}}), ctx: ctx, req: ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}, want: "not found"},
		{name: "relationship inactive", svc: NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{record: &tracecontract.RelationshipTraceRecord{RelationshipID: "rel-1", Status: string(domain.RelationshipStatusSuperseded)}}}), ctx: ctx, req: ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}, want: "not active"},
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
			if tc.name == "relationship missing" && !errors.Is(err, tracecontract.ErrRelationshipNotFound) {
				t.Fatalf("Export error = %v, want tracecontract.ErrRelationshipNotFound", err)
			}
		})
	}
}

func TestMemoryPackExportIncludesSupportAndNormalizesIDs(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	relationshipID := uuid.NewString()
	trace := &tracecontract.RelationshipTraceResult{
		Relationship: &tracecontract.RelationshipTraceRecord{
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
		EvidenceSupports: []tracecontract.RelationshipEvidenceSupportRecord{
			{FragmentID: "evidence-1", Quote: "Dense-Mem uses 42", SpanStart: 0, SpanEnd: 17, Metadata: map[string]any{"source": "test"}},
			{FragmentID: "evidence-1"},
		},
		EvidenceFragments: []tracecontract.TraceEvidenceFragment{
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
