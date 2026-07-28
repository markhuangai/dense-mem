package skillpackservice

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestMemoryPackExportInspectAndImportStagesRemember(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	sourceOwnerID := uuid.New()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	semantic := &semanticReaderStub{traces: map[string]*repository.RelationshipTraceResult{
		"rel-uses-postgres": {
			Relationship: &repository.RelationshipTraceRecord{
				TeamID:           teamID.String(),
				RelationshipID:   "rel-uses-postgres",
				OwnerProfileID:   sourceOwnerID.String(),
				SemanticGroupKey: "dense-mem:uses:postgres",
				SubjectEntityID:  "entity-dense-mem",
				SubjectName:      "Dense-Mem",
				PredicateKey:     "uses",
				PredicateVersion: 1,
				ObjectEntityID:   "entity-postgres",
				ObjectEntityName: "PostgreSQL",
				Status:           string(domain.RelationshipStatusActive),
				Polarity:         "positive",
				ScopeKey:         "default",
				SupportCount:     1,
				SourceGroupCount: 1,
				Version:          3,
			},
			EvidenceSupports: []repository.RelationshipEvidenceSupportRecord{{
				FragmentID: "fragment-1",
				Quote:      "Dense-Mem uses PostgreSQL.",
				SpanStart:  0,
				SpanEnd:    27,
				Metadata:   map[string]any{"support": "primary"},
			}},
			EvidenceFragments: []repository.TraceEvidenceFragment{{
				FragmentID:       "fragment-1",
				Content:          "Dense-Mem uses PostgreSQL with pgvector as the durable authority.",
				ContentHash:      "fragment-hash",
				SourceType:       "conversation",
				Authority:        "primary",
				SourceRef:        "wiki:Data-Model-And-Storage",
				SourceKey:        "wiki:data-model",
				SourceRevisionID: "rev-1",
				Labels:           []string{"canonical", "wiki"},
				Metadata:         map[string]any{"page": "Data-Model-And-Storage"},
			}},
		},
	}}
	remember := &rememberStub{result: &memoryservice.RememberResult{
		IngestID:          "ingest-canonical",
		ProcessingState:   string(domain.PlacementRunQueued),
		CheckAfterSeconds: 15,
		StatusTool:        "get_memory_placement",
	}}
	ledger := newLedgerStub()
	svc := NewMemoryPackService(MemoryPackDependencies{
		Semantic: semantic,
		Remember: remember,
		Ledger:   ledger,
		Now:      func() time.Time { return now },
	})
	ctx := authenticatedMemoryPackContext(teamID, ownerID, uuid.New())

	exported, err := svc.Export(ctx, ExportRequest{
		Name:            "Dense-Mem PostgreSQL pack",
		RelationshipIDs: []string{"rel-uses-postgres"},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exported.SHA256 == "" || exported.Artifact.ContentSHA256 != exported.SHA256 {
		t.Fatalf("exported hash = %q artifact hash = %q", exported.SHA256, exported.Artifact.ContentSHA256)
	}
	if !strings.Contains(exported.CanonicalJSON, `"content_sha256":"`+exported.SHA256+`"`) {
		t.Fatalf("canonical_json did not include content_sha256: %s", exported.CanonicalJSON)
	}

	inspected, err := svc.Inspect(ctx, InspectRequest{
		ArtifactJSON:   exported.CanonicalJSON,
		ExpectedSHA256: exported.SHA256,
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspected.ArtifactHash != exported.SHA256 || inspected.ItemCount != 1 || inspected.Items[0].Status != "ready" {
		t.Fatalf("inspect result = %#v", inspected)
	}

	imported, err := svc.Import(ctx, ImportRequest{
		ArtifactJSON:   exported.CanonicalJSON,
		ExpectedSHA256: exported.SHA256,
		Mode:           ModeReview,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported.Status != domain.SkillPackImportStatusApplied || imported.IngestID != "ingest-canonical" || imported.AppliedCount != 1 {
		t.Fatalf("import result = %#v", imported)
	}
	if remember.calls != 1 {
		t.Fatalf("Remember calls = %d, want 1", remember.calls)
	}
	if len(remember.reqs) != 1 || len(remember.reqs[0].Evidence) != 1 || len(remember.reqs[0].RelationshipHints) != 1 {
		t.Fatalf("remember request = %#v", remember.reqs)
	}
	req := remember.reqs[0]
	if req.ContractVersion != domain.ContractVersion || req.IdempotencyKey != "memory-pack:"+imported.ImportID {
		t.Fatalf("remember request identity = %#v", req)
	}
	metadata := req.Evidence[0].Metadata
	if metadata["source_owner_profile_id"] != sourceOwnerID.String() {
		t.Fatalf("source owner metadata = %#v", metadata)
	}
	if metadata["source_author_is_provenance_only"] != true || metadata["trusted_mode_forces_status"] != false {
		t.Fatalf("trust boundary metadata = %#v", metadata)
	}
	hint := req.RelationshipHints[0]
	if hint["source_relationship_id"] != "rel-uses-postgres" {
		t.Fatalf("relationship hint = %#v", hint)
	}
	hintEvidence, ok := hint["evidence"].([]map[string]any)
	if !ok || len(hintEvidence) != 1 || hintEvidence[0]["evidence_index"] != 0 {
		t.Fatalf("relationship hint evidence = %#v", hint["evidence"])
	}

	created := ledger.imports[ledgerKey(teamID.String(), imported.ImportID)]
	if created.TeamID != teamID.String() || created.OwnerProfileID != ownerID.String() {
		t.Fatalf("ledger import = %#v", created)
	}
	changes := ledger.changes[ledgerKey(teamID.String(), imported.ImportID)]
	if len(changes) != 1 || changes[0].EntityType != "v2_ingest" {
		t.Fatalf("ledger changes = %#v", changes)
	}

	retried, err := svc.Import(ctx, ImportRequest{
		ArtifactJSON:   exported.CanonicalJSON,
		ExpectedSHA256: exported.SHA256,
		Mode:           ModeReview,
	})
	if err != nil {
		t.Fatalf("Import retry: %v", err)
	}
	if retried.ImportID != imported.ImportID || retried.IngestID != imported.IngestID {
		t.Fatalf("retry import = %#v want import_id=%s ingest_id=%s", retried, imported.ImportID, imported.IngestID)
	}
	if remember.calls != 1 {
		t.Fatalf("Remember calls after retry = %d, want 1", remember.calls)
	}
}

func TestMemoryPackFindCandidatesFromSemanticGraph(t *testing.T) {
	teamID := uuid.New()
	semantic := &semanticReaderStub{graph: &repository.SemanticGraphSnapshot{
		Nodes: []repository.SemanticGraphNode{
			{Key: "entity:dense-mem", Title: "Dense-Mem"},
			{Key: "value:postgres", Title: "PostgreSQL"},
		},
		Edges: []repository.SemanticGraphEdge{{
			RelationshipID:   "relationship-canonical",
			Source:           "entity:dense-mem",
			Target:           "value:postgres",
			Relationship:     "uses",
			SupportCount:     2,
			SourceGroupCount: 1,
		}},
	}}
	svc := NewMemoryPackService(MemoryPackDependencies{Semantic: semantic})

	result, err := svc.FindCandidates(authenticatedMemoryPackContext(teamID, uuid.New(), uuid.New()), FindCandidatesRequest{
		Query: "postgres",
		Limit: 3,
	})
	if err != nil {
		t.Fatalf("FindCandidates: %v", err)
	}
	if semantic.graphInput.TeamID != teamID.String() || semantic.graphInput.Query != "postgres" || semantic.graphInput.Limit != 3 {
		t.Fatalf("semantic graph input = %#v", semantic.graphInput)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %#v", result.Candidates)
	}
	got := result.Candidates[0]
	if got.RelationshipID != "relationship-canonical" || got.Subject != "Dense-Mem" || got.Object != "PostgreSQL" {
		t.Fatalf("candidate = %#v", got)
	}
}

func TestMemoryPackExportValueRelationshipWithoutSupport(t *testing.T) {
	teamID := uuid.New()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	semantic := &semanticReaderStub{traces: map[string]*repository.RelationshipTraceResult{
		"rel-released": {
			Relationship: &repository.RelationshipTraceRecord{
				TeamID:           teamID.String(),
				RelationshipID:   "rel-released",
				SemanticGroupKey: "dense-mem:released:date",
				SubjectEntityID:  "entity-dense-mem",
				SubjectName:      "Dense-Mem",
				PredicateKey:     "released",
				PredicateVersion: 1,
				ObjectValueID:    "value-date",
				ObjectValue:      "2026-07-17",
				ObjectValueType:  "date",
				Status:           string(domain.RelationshipStatusActive),
				Version:          1,
			},
			EvidenceSupports: []repository.RelationshipEvidenceSupportRecord{{
				FragmentID: "fragment-ignored",
				Quote:      "ignored because support is disabled",
			}},
		},
	}}
	includeSupport := false
	svc := NewMemoryPackService(MemoryPackDependencies{
		Semantic: semantic,
		Now:      func() time.Time { return now },
	})

	result, err := svc.Export(authenticatedMemoryPackContext(teamID, uuid.New(), uuid.New()), ExportRequest{
		Name:            "Release pack",
		RelationshipIDs: []string{"rel-released"},
		IncludeSupport:  &includeSupport,
	})
	if err != nil {
		t.Fatalf("Export value relationship: %v", err)
	}
	if len(result.Omissions) != 1 || len(result.Artifact.EvidenceFragments) != 0 {
		t.Fatalf("export omissions/fragments = %#v/%#v", result.Omissions, result.Artifact.EvidenceFragments)
	}
	item := result.Artifact.Relationships[0]
	if item.Object.Kind != "value" || item.Object.ValueType != "date" || item.Object.Value != "2026-07-17" {
		t.Fatalf("value object = %#v", item.Object)
	}
}

func TestMemoryPackInspectAcceptsLegacyArtifactAsReviewOnly(t *testing.T) {
	svc := NewMemoryPackService(MemoryPackDependencies{})
	ctx := authenticatedMemoryPackContext(uuid.New(), uuid.New(), uuid.New())

	result, err := svc.Inspect(ctx, InspectRequest{ArtifactJSON: `{
		"schema_version": "dense-mem.memory_pack.v1",
		"name": "Legacy skill pack",
		"items": [{
			"subject": "Dense-Mem",
			"predicate": "has_skill",
			"object": "PostgreSQL import",
			"source_kind": "manual"
		}]
	}`})
	if err != nil {
		t.Fatalf("Inspect legacy: %v", err)
	}
	if result.Format != MemoryPackFormat || result.ItemCount != 1 {
		t.Fatalf("legacy inspect result = %#v", result)
	}
	if result.Items[0].Status != "needs_review" || len(result.DecisionsRequired) != 1 {
		t.Fatalf("legacy item should require review: %#v", result.Items[0])
	}
}

func TestMemoryPackArtifactValidationRejectsMalformedArtifacts(t *testing.T) {
	validRelationship := MemoryPackRelationship{
		ItemID:           "item-1",
		Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
		PredicateKey:     "uses",
		PredicateVersion: 1,
		Object:           MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
	}
	validArtifact := MemoryPackArtifact{
		Format:        MemoryPackFormat,
		Name:          "Valid pack",
		Relationships: []MemoryPackRelationship{validRelationship},
	}
	longName := strings.Repeat("x", 257)
	longDescription := strings.Repeat("x", 1025)

	tests := []struct {
		name     string
		mutate   func(MemoryPackArtifact) MemoryPackArtifact
		wantText string
	}{
		{name: "wrong format", mutate: func(a MemoryPackArtifact) MemoryPackArtifact { a.Format = "wrong"; return a }, wantText: "format"},
		{name: "missing name", mutate: func(a MemoryPackArtifact) MemoryPackArtifact { a.Name = ""; return a }, wantText: "name is required"},
		{name: "long name", mutate: func(a MemoryPackArtifact) MemoryPackArtifact { a.Name = longName; return a }, wantText: "name exceeds"},
		{name: "long description", mutate: func(a MemoryPackArtifact) MemoryPackArtifact { a.Description = longDescription; return a }, wantText: "description exceeds"},
		{name: "no relationships", mutate: func(a MemoryPackArtifact) MemoryPackArtifact { a.Relationships = nil; return a }, wantText: "relationships is required"},
		{name: "missing item id", mutate: func(a MemoryPackArtifact) MemoryPackArtifact { a.Relationships[0].ItemID = ""; return a }, wantText: "item_id"},
		{name: "missing predicate", mutate: func(a MemoryPackArtifact) MemoryPackArtifact { a.Relationships[0].PredicateKey = ""; return a }, wantText: "predicate_key"},
		{name: "bad predicate version", mutate: func(a MemoryPackArtifact) MemoryPackArtifact { a.Relationships[0].PredicateVersion = 0; return a }, wantText: "predicate_version"},
		{name: "missing subject", mutate: func(a MemoryPackArtifact) MemoryPackArtifact {
			a.Relationships[0].Subject.DisplayName = ""
			return a
		}, wantText: "subject ref"},
		{name: "missing object ref", mutate: func(a MemoryPackArtifact) MemoryPackArtifact { a.Relationships[0].Object.Ref = ""; return a }, wantText: "object ref"},
		{name: "missing value payload", mutate: func(a MemoryPackArtifact) MemoryPackArtifact {
			a.Relationships[0].Object = MemoryPackEndpoint{Ref: "object", Kind: "value", ValueType: "text"}
			return a
		}, wantText: "object value"},
		{name: "missing object display", mutate: func(a MemoryPackArtifact) MemoryPackArtifact {
			a.Relationships[0].Object.DisplayName = ""
			return a
		}, wantText: "object display_name"},
		{name: "duplicate relationship", mutate: func(a MemoryPackArtifact) MemoryPackArtifact {
			a.Relationships = append(a.Relationships, a.Relationships[0])
			return a
		}, wantText: "duplicate item_id"},
		{name: "missing fragment id", mutate: func(a MemoryPackArtifact) MemoryPackArtifact {
			a.EvidenceFragments = []MemoryPackEvidenceFragment{{Content: "evidence"}}
			return a
		}, wantText: "fragment_id"},
		{name: "empty fragment content", mutate: func(a MemoryPackArtifact) MemoryPackArtifact {
			a.EvidenceFragments = []MemoryPackEvidenceFragment{{FragmentID: "fragment-1"}}
			return a
		}, wantText: "content is required"},
		{name: "duplicate fragment", mutate: func(a MemoryPackArtifact) MemoryPackArtifact {
			a.EvidenceFragments = []MemoryPackEvidenceFragment{
				{FragmentID: "fragment-1", Content: "evidence"},
				{FragmentID: "fragment-1", Content: "evidence"},
			}
			return a
		}, wantText: "duplicate fragment_id"},
		{name: "support missing relationship", mutate: func(a MemoryPackArtifact) MemoryPackArtifact {
			a.EvidenceFragments = []MemoryPackEvidenceFragment{{FragmentID: "fragment-1", Content: "evidence"}}
			a.EvidenceSupports = []MemoryPackEvidenceSupport{{RelationshipItemID: "missing", FragmentID: "fragment-1"}}
			return a
		}, wantText: "relationship item"},
		{name: "support missing fragment", mutate: func(a MemoryPackArtifact) MemoryPackArtifact {
			a.EvidenceFragments = []MemoryPackEvidenceFragment{{FragmentID: "fragment-1", Content: "evidence"}}
			a.EvidenceSupports = []MemoryPackEvidenceSupport{{RelationshipItemID: "item-1", FragmentID: "missing"}}
			return a
		}, wantText: "fragment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMemoryPackArtifact(tc.mutate(cloneTestArtifact(validArtifact)))
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("validate error = %v, want %q", err, tc.wantText)
			}
		})
	}
}

func TestMemoryPackLoadRejectsHashMismatches(t *testing.T) {
	artifact := MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-hash",
		Name:      "Hash pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "uses",
			PredicateVersion: 1,
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
		}},
	}
	data := testArtifactJSON(t, artifact)
	svc := NewMemoryPackService(MemoryPackDependencies{})

	_, err := svc.(*memoryPackService).loadArtifact(context.Background(), data, "", strings.Repeat("0", 64))
	if err == nil || !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("expected hash mismatch err, got %v", err)
	}

	artifact.ContentSHA256 = strings.Repeat("f", 64)
	badEmbedded, err := marshalMemoryPackArtifact(artifact)
	if err != nil {
		t.Fatalf("marshal bad embedded hash: %v", err)
	}
	_, err = svc.(*memoryPackService).loadArtifact(context.Background(), string(badEmbedded), "", "")
	if err == nil || !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("expected embedded hash mismatch err, got %v", err)
	}
}

func TestMemoryPackInspectLoadsArtifactFromURL(t *testing.T) {
	artifact := MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-url",
		Name:      "URL pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "uses",
			PredicateVersion: 1,
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
		}},
	}
	data := testArtifactJSON(t, artifact)
	_, hash, err := canonicalMemoryPackArtifact(artifact)
	if err != nil {
		t.Fatalf("canonical artifact: %v", err)
	}
	client := artifactHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://example.com/pack.json" || req.Header.Get("Accept") != "application/json" {
			t.Fatalf("unexpected request url=%s accept=%s", req.URL.String(), req.Header.Get("Accept"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(data))}, nil
	})
	result, err := NewMemoryPackService(MemoryPackDependencies{HTTPClient: client}).Inspect(
		authenticatedMemoryPackContext(uuid.New(), uuid.New(), uuid.New()),
		InspectRequest{URL: "https://example.com/pack.json", ExpectedSHA256: hash},
	)
	if err != nil {
		t.Fatalf("Inspect URL: %v", err)
	}
	if result.SourceURL != "https://example.com/pack.json" || result.ArtifactHash != hash {
		t.Fatalf("url inspect result = %#v", result)
	}
}

func TestMemoryPackServiceValidationErrors(t *testing.T) {
	ctx := authenticatedMemoryPackContext(uuid.New(), uuid.New(), uuid.New())
	artifactJSON := testArtifactJSON(t, MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-errors",
		Name:      "Error pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "uses",
			PredicateVersion: 1,
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
		}},
	})
	inactiveSemantic := &semanticReaderStub{traces: map[string]*repository.RelationshipTraceResult{
		"inactive": {Relationship: &repository.RelationshipTraceRecord{
			RelationshipID: "inactive",
			SubjectName:    "Dense-Mem",
			PredicateKey:   "uses",
			ObjectEntityID: "entity-postgres",
			Status:         string(domain.RelationshipStatusNeedsReview),
		}},
	}}
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{name: "find missing semantic", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{}).FindCandidates(ctx, FindCandidatesRequest{Query: "postgres"})
			return err
		}, want: "semantic reader"},
		{name: "find missing query", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{Semantic: &semanticReaderStub{}}).FindCandidates(ctx, FindCandidatesRequest{})
			return err
		}, want: "query is required"},
		{name: "export missing name", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{Semantic: &semanticReaderStub{}}).Export(ctx, ExportRequest{RelationshipIDs: []string{"relationship"}})
			return err
		}, want: "name is required"},
		{name: "export missing relationship ids", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{Semantic: &semanticReaderStub{}}).Export(ctx, ExportRequest{Name: "Pack"})
			return err
		}, want: "relationship_ids"},
		{name: "export relationship not found", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{Semantic: &semanticReaderStub{}}).Export(ctx, ExportRequest{Name: "Pack", RelationshipIDs: []string{"missing"}})
			return err
		}, want: "not found"},
		{name: "export inactive relationship", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{Semantic: inactiveSemantic}).Export(ctx, ExportRequest{Name: "Pack", RelationshipIDs: []string{"inactive"}})
			return err
		}, want: "not active"},
		{name: "import invalid mode", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{}).Import(ctx, ImportRequest{Mode: "force"})
			return err
		}, want: "mode must be review or trusted"},
		{name: "import missing remember", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{}).Import(ctx, ImportRequest{ArtifactJSON: artifactJSON, Mode: ModeReview})
			return err
		}, want: "remember service"},
		{name: "import missing ledger", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{Remember: &rememberStub{}}).Import(ctx, ImportRequest{ArtifactJSON: artifactJSON, Mode: ModeReview})
			return err
		}, want: "import ledger"},
		{name: "rollback missing ledger", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{}).Rollback(ctx, RollbackRequest{ImportID: "import-canonical"})
			return err
		}, want: "import ledger"},
		{name: "rollback missing import id", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{Ledger: newLedgerStub()}).Rollback(ctx, RollbackRequest{})
			return err
		}, want: "import_id is required"},
		{name: "rollback missing import", run: func() error {
			_, err := NewMemoryPackService(MemoryPackDependencies{Ledger: newLedgerStub()}).Rollback(ctx, RollbackRequest{ImportID: "missing"})
			return err
		}, want: "no rows"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMemoryPackParseRejectsMalformedJSON(t *testing.T) {
	valid := []byte(testArtifactJSON(t, MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-parse",
		Name:      "Parse pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "uses",
			PredicateVersion: 1,
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
		}},
	}))
	tests := [][]byte{
		nil,
		[]byte("{"),
		[]byte(`{"format":"dense-mem.memory-pack.v1"}`),
		append(valid, []byte("\n{}")...),
	}
	for _, data := range tests {
		if _, _, err := parseMemoryPackArtifactJSON(data); err == nil {
			t.Fatalf("parseMemoryPackArtifactJSON(%q) succeeded, want error", string(data))
		}
	}
}

func TestMemoryPackImportReviewOnlyAndRecoverableFailures(t *testing.T) {
	artifactJSON := testArtifactJSON(t, MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-review-only",
		Name:      "Review only pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "uses",
			PredicateVersion: 1,
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
		}},
	})
	ctx := authenticatedMemoryPackContext(uuid.New(), uuid.New(), uuid.New())

	reviewOnlyRemember := &rememberStub{}
	reviewOnly, err := NewMemoryPackService(MemoryPackDependencies{
		Remember: reviewOnlyRemember,
		Ledger:   newLedgerStub(),
	}).Import(ctx, ImportRequest{
		ArtifactJSON: artifactJSON,
		Mode:         ModeReview,
		ConflictDecisions: []ImportItemDecision{{
			ItemID: "item-1",
			Action: DecisionSkip,
		}},
	})
	if err != nil {
		t.Fatalf("Import review-only: %v", err)
	}
	if reviewOnly.Status != domain.SkillPackImportStatusNeedsReview || reviewOnly.SkippedCount != 1 || reviewOnlyRemember.calls != 0 {
		t.Fatalf("review-only result = %#v calls=%d", reviewOnly, reviewOnlyRemember.calls)
	}

	rememberFailed, err := NewMemoryPackService(MemoryPackDependencies{
		Remember: &rememberStub{err: errors.New("provider unavailable")},
		Ledger:   newLedgerStub(),
	}).Import(ctx, ImportRequest{ArtifactJSON: artifactJSON, Mode: ModeReview})
	if err != nil {
		t.Fatalf("Import remember failure: %v", err)
	}
	if rememberFailed.Status != domain.SkillPackImportStatusFailed || !strings.Contains(rememberFailed.Error, "provider unavailable") {
		t.Fatalf("remember failure result = %#v", rememberFailed)
	}

	ledger := newLedgerStub()
	ledger.appendErr = errors.New("append failed")
	changeLedgerFailed, err := NewMemoryPackService(MemoryPackDependencies{
		Remember: &rememberStub{},
		Ledger:   ledger,
	}).Import(ctx, ImportRequest{ArtifactJSON: artifactJSON, Mode: ModeTrusted})
	if err != nil {
		t.Fatalf("Import change ledger failure: %v", err)
	}
	if changeLedgerFailed.Status != "change_ledger_failed" || changeLedgerFailed.IngestID == "" {
		t.Fatalf("change ledger failure result = %#v", changeLedgerFailed)
	}

	statusLedger := newLedgerStub()
	statusLedger.updateErr = errors.New("update failed")
	statusUpdateFailed, err := NewMemoryPackService(MemoryPackDependencies{
		Remember: &rememberStub{},
		Ledger:   statusLedger,
	}).Import(ctx, ImportRequest{ArtifactJSON: artifactJSON, Mode: ModeReview})
	if err != nil {
		t.Fatalf("Import status update failure: %v", err)
	}
	if statusUpdateFailed.Status != "status_update_failed" || !strings.Contains(statusUpdateFailed.Error, "update failed") {
		t.Fatalf("status update failure result = %#v", statusUpdateFailed)
	}
}

func TestMemoryPackImportUsesLegacySourceAndValueHints(t *testing.T) {
	ctx := authenticatedMemoryPackContext(uuid.New(), uuid.New(), uuid.New())
	remember := &rememberStub{}
	legacyResult, err := NewMemoryPackService(MemoryPackDependencies{
		Remember: remember,
		Ledger:   newLedgerStub(),
	}).Import(ctx, ImportRequest{
		ArtifactJSON: `{
			"schema_version": "dense-mem.memory_pack.v1",
			"name": "Legacy import pack",
			"items": [{
				"subject": "Dense-Mem",
				"predicate": "has_skill",
				"object": "PostgreSQL import",
				"source_kind": "manual"
			}]
		}`,
		Mode: ModeReview,
	})
	if err != nil {
		t.Fatalf("Import legacy source: %v", err)
	}
	if legacyResult.Status != domain.SkillPackImportStatusApplied || remember.reqs[0].Evidence[0].Source != "legacy" {
		t.Fatalf("legacy import result=%#v request=%#v", legacyResult, remember.reqs)
	}

	valueRemember := &rememberStub{}
	valueArtifact := testArtifactJSON(t, MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-value",
		Name:      "Value pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "released",
			PredicateVersion: 1,
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "value", ValueType: "date", Value: "2026-07-17"},
		}},
	})
	valueResult, err := NewMemoryPackService(MemoryPackDependencies{
		Remember: valueRemember,
		Ledger:   newLedgerStub(),
	}).Import(ctx, ImportRequest{ArtifactJSON: valueArtifact, Mode: ModeTrusted})
	if err != nil {
		t.Fatalf("Import value hint: %v", err)
	}
	hint := valueRemember.reqs[0].RelationshipHints[0]
	objectValue, ok := hint["object_value"].(map[string]any)
	if valueResult.Status != domain.SkillPackImportStatusApplied || !ok || objectValue["type"] != "date" {
		t.Fatalf("value import result=%#v hint=%#v", valueResult, hint)
	}
	if valueRemember.reqs[0].Evidence[0].Authority != "authoritative" || !strings.HasPrefix(valueRemember.reqs[0].Evidence[0].Source, "memory_pack:") {
		t.Fatalf("value evidence = %#v", valueRemember.reqs[0].Evidence[0])
	}
}

func TestMemoryPackImportRejectsUnknownSelection(t *testing.T) {
	artifact := MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-selection",
		Name:      "Selection pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "uses",
			PredicateVersion: 1,
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
		}},
	}
	data, _, err := canonicalMemoryPackArtifact(artifact)
	if err != nil {
		t.Fatalf("canonical artifact: %v", err)
	}
	svc := NewMemoryPackService(MemoryPackDependencies{Remember: &rememberStub{}, Ledger: newLedgerStub()})
	_, err = svc.Import(authenticatedMemoryPackContext(uuid.New(), uuid.New(), uuid.New()), ImportRequest{
		ArtifactJSON:    string(data),
		Mode:            ModeReview,
		SelectedItemIDs: []string{"missing-item"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing-item") {
		t.Fatalf("Import err = %v, want unknown selected item error", err)
	}
}

func TestMemoryPackImportRejectsInvalidConflictDecision(t *testing.T) {
	artifactJSON := testArtifactJSON(t, MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-invalid-decision",
		Name:      "Invalid decision pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "uses",
			PredicateVersion: 1,
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
		}},
	})
	_, err := NewMemoryPackService(MemoryPackDependencies{Remember: &rememberStub{}, Ledger: newLedgerStub()}).Import(
		authenticatedMemoryPackContext(uuid.New(), uuid.New(), uuid.New()),
		ImportRequest{
			ArtifactJSON: artifactJSON,
			Mode:         ModeReview,
			ConflictDecisions: []ImportItemDecision{{
				ItemID: "item-1",
				Action: "force",
			}},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported conflict decision action") {
		t.Fatalf("Import err = %v, want invalid decision action error", err)
	}
}

func TestMemoryPackRollbackBlocksCrossOwnerAndSemanticEffects(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	otherOwnerID := uuid.New()
	ledger := newLedgerStub()
	ledger.imports[ledgerKey(teamID.String(), "import-semantic")] = domain.SkillPackImport{
		ImportID:       "import-semantic",
		TeamID:         teamID.String(),
		OwnerProfileID: ownerID.String(),
		Status:         domain.SkillPackImportStatusApplied,
	}
	ledger.changes[ledgerKey(teamID.String(), "import-semantic")] = []domain.SkillPackImportChange{{
		ImportID:   "import-semantic",
		TeamID:     teamID.String(),
		EntityType: "relationship",
		EntityID:   "relationship-1",
		Action:     domain.SkillPackChangeActionCreated,
	}}
	ledger.imports[ledgerKey(teamID.String(), "import-staged")] = domain.SkillPackImport{
		ImportID:       "import-staged",
		TeamID:         teamID.String(),
		OwnerProfileID: ownerID.String(),
		Status:         domain.SkillPackImportStatusApplied,
	}
	ledger.changes[ledgerKey(teamID.String(), "import-staged")] = []domain.SkillPackImportChange{{
		ImportID:   "import-staged",
		TeamID:     teamID.String(),
		EntityType: "v2_ingest",
		EntityID:   "ingest-canonical",
		Action:     domain.SkillPackChangeActionLinked,
	}}
	svc := NewMemoryPackService(MemoryPackDependencies{Ledger: ledger})

	_, err := svc.Rollback(authenticatedMemoryPackContext(teamID, otherOwnerID, uuid.New()), RollbackRequest{
		ImportID: "import-semantic",
		DryRun:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "owner mismatch") {
		t.Fatalf("cross-owner rollback err = %v, want owner mismatch", err)
	}

	blocked, err := svc.Rollback(authenticatedMemoryPackContext(teamID, ownerID, uuid.New()), RollbackRequest{
		ImportID: "import-semantic",
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("semantic rollback: %v", err)
	}
	if blocked.Status != "blocked" || len(blocked.Conflicts) != 1 {
		t.Fatalf("blocked rollback = %#v", blocked)
	}

	safe, err := svc.Rollback(authenticatedMemoryPackContext(teamID, ownerID, uuid.New()), RollbackRequest{
		ImportID: "import-staged",
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("staged dry-run rollback: %v", err)
	}
	if safe.Status != "safe" || !safe.DryRun || ledger.imports[ledgerKey(teamID.String(), "import-staged")].Status != domain.SkillPackImportStatusApplied {
		t.Fatalf("safe rollback = %#v record = %#v", safe, ledger.imports[ledgerKey(teamID.String(), "import-staged")])
	}
	if safe.ImpactToken == "" {
		t.Fatalf("safe rollback missing impact token: %#v", safe)
	}

	rolledBack, err := svc.Rollback(authenticatedMemoryPackContext(teamID, ownerID, uuid.New()), RollbackRequest{
		ImportID:    "import-staged",
		Confirm:     true,
		ImpactToken: safe.ImpactToken,
	})
	if err != nil {
		t.Fatalf("staged confirmed rollback: %v", err)
	}
	if rolledBack.Status != domain.SkillPackImportStatusRolledBack || rolledBack.DryRun || rolledBack.RevertedCount != 1 {
		t.Fatalf("confirmed rollback = %#v", rolledBack)
	}
	if ledger.imports[ledgerKey(teamID.String(), "import-staged")].Status != domain.SkillPackImportStatusRolledBack {
		t.Fatalf("ledger status = %#v", ledger.imports[ledgerKey(teamID.String(), "import-staged")])
	}
}

func TestMemoryPackAuthContextRequired(t *testing.T) {
	svc := NewMemoryPackService(MemoryPackDependencies{})
	_, err := svc.Inspect(context.Background(), InspectRequest{ArtifactJSON: "{}"})
	if !errors.Is(err, ErrMemoryPackAuthContext) {
		t.Fatalf("Inspect err = %v, want ErrMemoryPackAuthContext", err)
	}
}
