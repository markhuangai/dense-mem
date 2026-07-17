package skillpackservice

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

type v2SemanticReaderStub struct {
	graph      *repository.V2SemanticGraphSnapshot
	graphInput repository.V2SemanticGraphQuery
	traces     map[string]*repository.V2RelationshipTraceResult
	traceInput []repository.V2TraceRelationshipInput
}

func (s *v2SemanticReaderStub) SemanticGraph(_ context.Context, input repository.V2SemanticGraphQuery) (*repository.V2SemanticGraphSnapshot, error) {
	s.graphInput = input
	if s.graph == nil {
		return &repository.V2SemanticGraphSnapshot{}, nil
	}
	return s.graph, nil
}

func (s *v2SemanticReaderStub) TraceRelationship(_ context.Context, input repository.V2TraceRelationshipInput) (*repository.V2RelationshipTraceResult, error) {
	s.traceInput = append(s.traceInput, input)
	if trace, ok := s.traces[input.RelationshipID]; ok {
		return trace, nil
	}
	return &repository.V2RelationshipTraceResult{}, nil
}

type v2RememberStub struct {
	calls  int
	reqs   []memoryservice.V2RememberRequest
	result *memoryservice.V2RememberResult
	err    error
}

func (s *v2RememberStub) RememberV2(_ context.Context, req memoryservice.V2RememberRequest) (*memoryservice.V2RememberResult, error) {
	s.calls++
	s.reqs = append(s.reqs, req)
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &memoryservice.V2RememberResult{
		IngestID:   "ingest-v2",
		Status:     string(domain.V2PlacementRunQueued),
		StatusTool: "get_memory_placement",
		Items: []memoryservice.V2RememberItemResult{{
			ItemID:        "placement-item-1",
			EvidenceIndex: 0,
			Category:      string(domain.V2EvidenceNeedsReview),
			SearchState:   string(domain.V2SearchProjectionPending),
		}},
		SearchState: string(domain.V2SearchProjectionPending),
	}, nil
}

func (s *v2RememberStub) GetMemoryPlacementV2(_ context.Context, req memoryservice.V2GetMemoryPlacementRequest) (*memoryservice.V2RememberResult, error) {
	return &memoryservice.V2RememberResult{IngestID: req.IngestID, Status: string(domain.V2PlacementRunQueued)}, nil
}

type v2LedgerStub struct {
	imports   map[string]domain.SkillPackImport
	changes   map[string][]domain.SkillPackImportChange
	createErr error
	updateErr error
	appendErr error
	listErr   error
	markErr   error
}

type v2ArtifactHTTPClientFunc func(*http.Request) (*http.Response, error)

func (fn v2ArtifactHTTPClientFunc) Do(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func newV2LedgerStub() *v2LedgerStub {
	return &v2LedgerStub{
		imports: map[string]domain.SkillPackImport{},
		changes: map[string][]domain.SkillPackImportChange{},
	}
}

func (s *v2LedgerStub) CreateImport(_ context.Context, record domain.SkillPackImport) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.imports[ledgerKey(record.TeamID, record.ImportID)] = record
	return nil
}

func (s *v2LedgerStub) UpdateImportStatus(_ context.Context, teamID, importID, status string, appliedCount, skippedCount int, summary map[string]any) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	key := ledgerKey(teamID, importID)
	record, ok := s.imports[key]
	if !ok {
		return sql.ErrNoRows
	}
	record.Status = status
	record.AppliedCount = appliedCount
	record.SkippedCount = skippedCount
	record.Summary = summary
	if ingestID, _ := summary["ingest_id"].(string); ingestID != "" {
		record.IngestID = ingestID
	}
	s.imports[key] = record
	return nil
}

func (s *v2LedgerStub) MarkRolledBack(_ context.Context, teamID, importID string) error {
	if s.markErr != nil {
		return s.markErr
	}
	key := ledgerKey(teamID, importID)
	record, ok := s.imports[key]
	if !ok {
		return sql.ErrNoRows
	}
	record.Status = domain.SkillPackImportStatusRolledBack
	now := time.Now().UTC()
	record.RolledBackAt = &now
	s.imports[key] = record
	return nil
}

func (s *v2LedgerStub) GetImport(_ context.Context, teamID, importID string) (*domain.SkillPackImport, error) {
	record, ok := s.imports[ledgerKey(teamID, importID)]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &record, nil
}

func (s *v2LedgerStub) AppendChange(_ context.Context, change domain.SkillPackImportChange) error {
	if s.appendErr != nil {
		return s.appendErr
	}
	key := ledgerKey(change.TeamID, change.ImportID)
	s.changes[key] = append(s.changes[key], change)
	return nil
}

func (s *v2LedgerStub) ListChanges(_ context.Context, teamID, importID string) ([]domain.SkillPackImportChange, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	changes := s.changes[ledgerKey(teamID, importID)]
	return append([]domain.SkillPackImportChange(nil), changes...), nil
}

func ledgerKey(teamID, importID string) string {
	return teamID + "/" + importID
}

func authenticatedV2MemoryPackContext(teamID, profileID, keyID uuid.UUID) context.Context {
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamID:      teamID,
		TeamName:    "team",
		ProfileID:   profileID,
		ProfileName: "profile",
	})
	return requestctx.WithActorCredential(ctx, requestctx.ActorCredential{
		KeyID:      keyID,
		AuthMethod: "api_key",
		Role:       "member",
	})
}

var _ V2SemanticReader = (*v2SemanticReaderStub)(nil)
var _ memoryservice.V2RememberService = (*v2RememberStub)(nil)
var _ ImportLedger = (*v2LedgerStub)(nil)

func testV2ArtifactJSON(t *testing.T, artifact V2MemoryPackArtifact) string {
	t.Helper()
	_, hash, err := canonicalV2MemoryPackArtifact(artifact)
	if err != nil {
		t.Fatalf("canonical artifact: %v", err)
	}
	artifact.ContentSHA256 = hash
	data, err := marshalV2MemoryPackArtifact(artifact)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	return string(data)
}

func cloneV2TestArtifact(artifact V2MemoryPackArtifact) V2MemoryPackArtifact {
	artifact.Relationships = append([]V2MemoryPackRelationship(nil), artifact.Relationships...)
	artifact.EvidenceFragments = append([]V2MemoryPackEvidenceFragment(nil), artifact.EvidenceFragments...)
	artifact.EvidenceSupports = append([]V2MemoryPackEvidenceSupport(nil), artifact.EvidenceSupports...)
	return artifact
}
