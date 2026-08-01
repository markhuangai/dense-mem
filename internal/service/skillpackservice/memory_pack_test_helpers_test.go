package skillpackservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

type semanticReaderStub struct {
	graph      *repository.SemanticGraphSnapshot
	graphInput repository.SemanticGraphQuery
	traces     map[string]*repository.RelationshipTraceResult
	traceInput []repository.TraceRelationshipInput
}

func (s *semanticReaderStub) SemanticGraph(_ context.Context, input repository.SemanticGraphQuery) (*repository.SemanticGraphSnapshot, error) {
	s.graphInput = input
	if s.graph == nil {
		return &repository.SemanticGraphSnapshot{}, nil
	}
	return s.graph, nil
}

func (s *semanticReaderStub) TraceRelationship(_ context.Context, input repository.TraceRelationshipInput) (*repository.RelationshipTraceResult, error) {
	s.traceInput = append(s.traceInput, input)
	if trace, ok := s.traces[input.RelationshipID]; ok {
		return trace, nil
	}
	return &repository.RelationshipTraceResult{}, nil
}

type rememberStub struct {
	calls      int
	reqs       []memoryservice.RememberRequest
	result     *memoryservice.RememberResult
	err        error
	status     *memoryservice.SubmissionStatusResult
	statusErr  error
	statusReqs []memoryservice.GetSubmissionStatusRequest
}

func (s *rememberStub) Remember(_ context.Context, req memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	s.calls++
	s.reqs = append(s.reqs, req)
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &memoryservice.RememberResult{
		SubmissionID:    "submission-canonical",
		ProcessingState: string(domain.SubmissionQueued),
		StatusTool:      "get_submission_status",
	}, nil
}

func (s *rememberStub) GetSubmissionStatus(_ context.Context, req memoryservice.GetSubmissionStatusRequest) (*memoryservice.SubmissionStatusResult, error) {
	s.statusReqs = append(s.statusReqs, req)
	if s.statusErr != nil {
		return nil, s.statusErr
	}
	if s.status != nil {
		return s.status, nil
	}
	return &memoryservice.SubmissionStatusResult{SubmissionID: req.SubmissionID, ProcessingState: string(domain.SubmissionQueued)}, nil
}

type ledgerStub struct {
	imports   map[string]domain.SkillPackImport
	changes   map[string][]domain.SkillPackImportChange
	createErr error
	updateErr error
	appendErr error
	listErr   error
	markErr   error
}

type artifactHTTPClientFunc func(*http.Request) (*http.Response, error)

func (fn artifactHTTPClientFunc) Do(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func newLedgerStub() *ledgerStub {
	return &ledgerStub{
		imports: map[string]domain.SkillPackImport{},
		changes: map[string][]domain.SkillPackImportChange{},
	}
}

func (s *ledgerStub) CreateImport(_ context.Context, record domain.SkillPackImport) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.imports[ledgerKey(record.TeamID, record.ImportID)] = record
	return nil
}

func (s *ledgerStub) UpdateImportStatus(_ context.Context, teamID, importID, status string, appliedCount, skippedCount int, summary map[string]any) error {
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
	if submissionID, _ := summary["submission_id"].(string); submissionID != "" {
		record.SubmissionID = submissionID
	}
	s.imports[key] = record
	return nil
}

func (s *ledgerStub) MarkRolledBack(_ context.Context, teamID, importID string) error {
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

func (s *ledgerStub) GetImport(_ context.Context, teamID, importID string) (*domain.SkillPackImport, error) {
	record, ok := s.imports[ledgerKey(teamID, importID)]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &record, nil
}

func (s *ledgerStub) AppendChange(_ context.Context, change domain.SkillPackImportChange) error {
	if s.appendErr != nil {
		return s.appendErr
	}
	key := ledgerKey(change.TeamID, change.ImportID)
	s.changes[key] = append(s.changes[key], change)
	return nil
}

func (s *ledgerStub) ListChanges(_ context.Context, teamID, importID string) ([]domain.SkillPackImportChange, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	changes := s.changes[ledgerKey(teamID, importID)]
	return append([]domain.SkillPackImportChange(nil), changes...), nil
}

func ledgerKey(teamID, importID string) string {
	return teamID + "/" + importID
}

func authenticatedMemoryPackContext(teamID, profileID, keyID uuid.UUID) context.Context {
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

var _ MemoryPackSemanticReader = (*semanticReaderStub)(nil)
var _ memoryservice.RememberService = (*rememberStub)(nil)
var _ ImportLedger = (*ledgerStub)(nil)

func testArtifactJSON(t *testing.T, artifact MemoryPackArtifact) string {
	t.Helper()
	_, hash, err := canonicalMemoryPackArtifact(artifact)
	if err != nil {
		t.Fatalf("canonical artifact: %v", err)
	}
	artifact.ContentSHA256 = hash
	data, err := marshalMemoryPackArtifact(artifact)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	return string(data)
}

func testArtifactV23JSON(t *testing.T, artifact memoryPackArtifactV23) (string, string) {
	t.Helper()
	_, hash, err := canonicalMemoryPackArtifactV23(artifact)
	if err != nil {
		t.Fatalf("canonical v2.3 artifact: %v", err)
	}
	artifact.ContentSHA256 = hash
	data, err := json.Marshal(normalizeMemoryPackArtifactV23(artifact))
	if err != nil {
		t.Fatalf("marshal v2.3 artifact: %v", err)
	}
	return string(data), hash
}

func cloneTestArtifact(artifact MemoryPackArtifact) MemoryPackArtifact {
	artifact.Relationships = append([]MemoryPackRelationship(nil), artifact.Relationships...)
	artifact.Evidence = append([]MemoryPackEvidence(nil), artifact.Evidence...)
	artifact.EvidenceSupports = append([]MemoryPackEvidenceSupport(nil), artifact.EvidenceSupports...)
	return artifact
}
