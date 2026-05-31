package skillpackservice

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type fakeFactGet struct {
	facts map[string]*domain.Fact
	err   error
}

func packWithItem(sourceKind string) *SkillPack {
	return &SkillPack{
		SchemaVersion: SchemaVersion,
		Name:          "Test pack",
		Items: []SkillPackItem{{
			Subject:    "assistant",
			Predicate:  "has_skill",
			Object:     "tests skill packs",
			SourceKind: sourceKind,
		}},
	}
}

type fakeHTTPClient struct {
	status     int
	body       string
	err        error
	readErr    error
	lastAccept string
}

func (f *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	f.lastAccept = req.Header.Get("Accept")
	if f.err != nil {
		return nil, f.err
	}
	body := io.NopCloser(strings.NewReader(f.body))
	if f.readErr != nil {
		body = io.NopCloser(errorReader{err: f.readErr})
	}
	return &http.Response{StatusCode: f.status, Body: body}, nil
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type mutatingScopedGraph struct {
	reads int
}

func (g *mutatingScopedGraph) ScopedRead(_ context.Context, _ string, _ string, params map[string]any) (neo4jdriver.ResultSummary, []map[string]any, error) {
	if _, exists := params["profileId"]; exists {
		return nil, nil, errors.New("profileId parameter was reused")
	}
	params["profileId"] = "injected-by-scope"
	g.reads++
	if g.reads == 1 {
		return nil, nil, nil
	}
	return nil, []map[string]any{{
		"id":        "claim-1",
		"type":      "claim",
		"subject":   "assistant",
		"predicate": "has_skill",
		"object":    "skill pack regression testing",
	}}, nil
}

func (g *mutatingScopedGraph) ScopedWrite(_ context.Context, _ string, _ string, _ map[string]any) (neo4jdriver.ResultSummary, error) {
	return nil, nil
}

func (g *mutatingScopedGraph) ScopedWriteTx(_ context.Context, _ string, _ func(neo4jdriver.ManagedTransaction) error) error {
	return nil
}

type factCandidateGraph struct {
	reads int
}

func (g *factCandidateGraph) ScopedRead(_ context.Context, _ string, _ string, _ map[string]any) (neo4jdriver.ResultSummary, []map[string]any, error) {
	g.reads++
	return nil, []map[string]any{{
		"id":        "fact-1",
		"type":      "fact",
		"subject":   "assistant",
		"predicate": "has_skill",
		"object":    "skill pack regression testing",
	}}, nil
}

func (g *factCandidateGraph) ScopedWrite(_ context.Context, _ string, _ string, _ map[string]any) (neo4jdriver.ResultSummary, error) {
	return nil, nil
}

func (g *factCandidateGraph) ScopedWriteTx(_ context.Context, _ string, _ func(neo4jdriver.ManagedTransaction) error) error {
	return nil
}

type recordingGraph struct {
	states        map[string]map[string]any
	promotedFacts map[string]string
	writeCount    int
	writeErr      error
	writeErrAfter int
	writeQueries  []string
	writeParams   []map[string]any
	txCount       int
	txErr         error
	readErr       error
}

func (g *recordingGraph) ScopedRead(_ context.Context, _ string, _ string, params map[string]any) (neo4jdriver.ResultSummary, []map[string]any, error) {
	if g.readErr != nil {
		return nil, nil, g.readErr
	}
	if claimID, _ := params["claimId"].(string); claimID != "" {
		if factID := g.promotedFacts[claimID]; factID != "" {
			return nil, []map[string]any{{"fact_id": factID}}, nil
		}
		return nil, nil, nil
	}
	entityID, _ := params["entityId"].(string)
	if entityID == "" {
		return nil, nil, nil
	}
	state := g.states[entityID]
	if state == nil {
		return nil, nil, nil
	}
	return nil, []map[string]any{{"state": state}}, nil
}

func (g *recordingGraph) ScopedWrite(_ context.Context, _ string, query string, params map[string]any) (neo4jdriver.ResultSummary, error) {
	g.writeCount++
	g.writeQueries = append(g.writeQueries, query)
	g.writeParams = append(g.writeParams, params)
	if g.writeErr != nil && (g.writeErrAfter == 0 || g.writeCount >= g.writeErrAfter) {
		return nil, g.writeErr
	}
	return nil, nil
}

func (g *recordingGraph) ScopedWriteTx(_ context.Context, _ string, _ func(neo4jdriver.ManagedTransaction) error) error {
	g.txCount++
	return g.txErr
}

func graphWriteContains(g *recordingGraph, fragment string) bool {
	for _, query := range g.writeQueries {
		if strings.Contains(query, fragment) {
			return true
		}
	}
	return false
}

func (f fakeFactGet) Get(_ context.Context, _ string, factID string) (*domain.Fact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.facts[factID], nil
}

type fakeClaimGet struct {
	claims map[string]*domain.Claim
	err    error
}

func (f fakeClaimGet) Get(_ context.Context, _ string, claimID string) (*domain.Claim, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.claims[claimID], nil
}

type fakeFactList struct {
	facts []*domain.Fact
	err   error
}

func (f fakeFactList) List(_ context.Context, _ string, filters factservice.FactListFilters, _ int, _ string) ([]*domain.Fact, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	out := []*domain.Fact{}
	for _, fact := range f.facts {
		if filters.Subject != "" && fact.Subject != filters.Subject {
			continue
		}
		if filters.Predicate != "" && fact.Predicate != filters.Predicate {
			continue
		}
		if filters.Status != "" && fact.Status != filters.Status {
			continue
		}
		out = append(out, fact)
	}
	return out, "", nil
}

type fakeClaimList struct {
	claims []*domain.Claim
	err    error
}

func (f fakeClaimList) List(_ context.Context, _ string, limit, offset int) ([]*domain.Claim, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.claims, len(f.claims), nil
}

type fakeFragmentCreate struct {
	calls     int
	duplicate bool
	err       error
}

func (f *fakeFragmentCreate) Create(_ context.Context, profileID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &fragmentservice.CreateResult{
		Fragment: &domain.Fragment{
			FragmentID:  "fragment-1",
			ProfileID:   profileID,
			Content:     req.Content,
			ContentHash: "hash",
			CreatedAt:   time.Now().UTC(),
		},
		Duplicate: f.duplicate,
	}, nil
}

type fakeClaimCreate struct {
	calls     int
	duplicate bool
	claimID   string
	err       error
}

func (f *fakeClaimCreate) Create(_ context.Context, profileID string, claim *domain.Claim) (*claimservice.CreateResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	claimID := f.claimID
	if claimID == "" {
		claimID = "claim-1"
	}
	claim.ClaimID = claimID
	claim.ProfileID = profileID
	claim.Status = domain.StatusCandidate
	claim.EntailmentVerdict = domain.VerdictInsufficient
	res := &claimservice.CreateResult{Claim: claim, Duplicate: f.duplicate}
	if f.duplicate {
		res.DuplicateOf = claimID
	}
	return res, nil
}

type fakeFactPromote struct {
	err error
}

func (f fakeFactPromote) Promote(_ context.Context, profileID, claimID string) (*domain.Fact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &domain.Fact{
		FactID:              "fact-1",
		ProfileID:           profileID,
		Subject:             "assistant",
		Predicate:           "has_skill",
		Object:              "tests skill packs",
		Status:              domain.FactStatusActive,
		PromotedFromClaimID: claimID,
		RecordedAt:          time.Now().UTC(),
	}, nil
}

type fakeLedger struct {
	imports        []domain.SkillPackImport
	changes        []domain.SkillPackImportChange
	status         string
	createErr      error
	updateErr      error
	markErr        error
	getErr         error
	listErr        error
	appendErr      error
	appendErrAfter int
	appendCalls    int
	updateCalls    int
}

func (f *fakeLedger) CreateImport(_ context.Context, record domain.SkillPackImport) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.imports = append(f.imports, record)
	f.status = record.Status
	return nil
}

func (f *fakeLedger) UpdateImportStatus(_ context.Context, _, _, status string, _, _ int, _ map[string]any) error {
	f.updateCalls++
	if f.updateErr != nil {
		return f.updateErr
	}
	f.status = status
	return nil
}

func (f *fakeLedger) MarkRolledBack(context.Context, string, string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.status = domain.SkillPackImportStatusRolledBack
	return nil
}

func (f *fakeLedger) GetImport(context.Context, string, string) (*domain.SkillPackImport, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	status := f.status
	if status == "" || status == domain.SkillPackImportStatusInspecting {
		status = domain.SkillPackImportStatusApplied
	}
	return &domain.SkillPackImport{Status: status}, nil
}

func (f *fakeLedger) AppendChange(_ context.Context, change domain.SkillPackImportChange) error {
	f.appendCalls++
	if f.appendErr != nil && (f.appendErrAfter == 0 || f.appendCalls >= f.appendErrAfter) {
		return f.appendErr
	}
	f.changes = append(f.changes, change)
	return nil
}

func (f *fakeLedger) ListChanges(context.Context, string, string) ([]domain.SkillPackImportChange, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.changes, nil
}
