package skillpackservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func TestExportProducesMinimalCanonicalArtifactAndHash(t *testing.T) {
	fact := &domain.Fact{
		FactID:    "fact-1",
		Subject:   "assistant",
		Predicate: "has_skill",
		Object:    "writes behavior-first React tests",
		Status:    domain.FactStatusActive,
	}
	claim := &domain.Claim{
		ClaimID:   "claim-1",
		Subject:   "assistant",
		Predicate: "uses",
		Object:    "pnpm for workspace package scripts",
		Status:    domain.StatusValidated,
	}
	svc := New(Dependencies{
		FactGet:  fakeFactGet{facts: map[string]*domain.Fact{"fact-1": fact}},
		ClaimGet: fakeClaimGet{claims: map[string]*domain.Claim{"claim-1": claim}},
	})

	res, err := svc.Export(context.Background(), "team-1", ExportRequest{
		Name:     "React testing",
		FactIDs:  []string{"fact-1"},
		ClaimIDs: []string{"claim-1"},
		ManualItems: []SkillPackItem{{
			Subject:    "assistant",
			Predicate:  "knows",
			Object:     "project-specific test helper names",
			SourceKind: SourceKindManual,
		}},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if res.Artifact.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %q", res.Artifact.SchemaVersion)
	}
	if len(res.Artifact.Items) != 3 {
		t.Fatalf("items len = %d, want 3", len(res.Artifact.Items))
	}
	if strings.Contains(res.CanonicalJSON, "team_id") ||
		strings.Contains(res.CanonicalJSON, "valid_from") ||
		strings.Contains(res.CanonicalJSON, "labels") {
		t.Fatalf("canonical JSON contains stripped fields: %s", res.CanonicalJSON)
	}
	sum := sha256.Sum256([]byte(res.CanonicalJSON))
	if got := res.SHA256; got != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %s, want hash of canonical JSON", got)
	}
}

func TestInspectReportsFactConflictsAndDecisionPrompt(t *testing.T) {
	svc := New(Dependencies{
		FactList: fakeFactList{facts: []*domain.Fact{{
			FactID:    "fact-local",
			Subject:   "assistant",
			Predicate: "uses",
			Object:    "npm",
			Status:    domain.FactStatusActive,
		}}},
	})
	pack := &SkillPack{
		SchemaVersion: SchemaVersion,
		Name:          "Package manager",
		Items: []SkillPackItem{{
			Subject:    "assistant",
			Predicate:  "uses",
			Object:     "pnpm",
			SourceKind: SourceKindFact,
		}},
	}

	res, err := svc.Inspect(context.Background(), "team-1", InspectRequest{Artifact: pack})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := res.Items[0].Status; got != "conflicts_with_fact" {
		t.Fatalf("status = %q, want conflicts_with_fact", got)
	}
	if len(res.DecisionsRequired) != 1 {
		t.Fatalf("decisions_required len = %d, want 1", len(res.DecisionsRequired))
	}
	if got := res.DecisionsRequired[0].FactIDs[0]; got != "fact-local" {
		t.Fatalf("conflict fact id = %q", got)
	}
}

func TestTrustedImportReturnsNeedsReviewBeforeWritesWhenConflictDecisionMissing(t *testing.T) {
	fragmentCreate := &fakeFragmentCreate{}
	claimCreate := &fakeClaimCreate{}
	svc := New(Dependencies{
		FragmentCreate: fragmentCreate,
		ClaimCreate:    claimCreate,
		Ledger:         &fakeLedger{},
		FactList: fakeFactList{facts: []*domain.Fact{{
			FactID:    "fact-local",
			Subject:   "assistant",
			Predicate: "uses",
			Object:    "npm",
			Status:    domain.FactStatusActive,
		}}},
	})
	pack := &SkillPack{
		SchemaVersion: SchemaVersion,
		Name:          "Package manager",
		Items: []SkillPackItem{{
			Subject:    "assistant",
			Predicate:  "uses",
			Object:     "pnpm",
			SourceKind: SourceKindFact,
		}},
	}

	res, err := svc.Import(context.Background(), "team-1", ImportRequest{
		Artifact: pack,
		Mode:     ModeTrusted,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Status != domain.SkillPackImportStatusNeedsReview {
		t.Fatalf("status = %q, want needs_review", res.Status)
	}
	if len(res.DecisionsRequired) != 1 {
		t.Fatalf("decisions_required len = %d, want 1", len(res.DecisionsRequired))
	}
	if fragmentCreate.calls != 0 || claimCreate.calls != 0 {
		t.Fatalf("writes happened before conflict decision: fragments=%d claims=%d", fragmentCreate.calls, claimCreate.calls)
	}
}

func TestInspectRejectsHashMismatch(t *testing.T) {
	svc := New(Dependencies{})
	pack := &SkillPack{
		SchemaVersion: SchemaVersion,
		Name:          "Hash test",
		Items: []SkillPackItem{{
			Subject:    "assistant",
			Predicate:  "has_skill",
			Object:     "checks artifact integrity",
			SourceKind: SourceKindManual,
		}},
	}

	_, err := svc.Inspect(context.Background(), "team-1", InspectRequest{
		Artifact:       pack,
		ExpectedSHA256: strings.Repeat("0", 64),
	})
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("Inspect err = %v, want ErrHashMismatch", err)
	}
}

func TestURLSafetyRejectsNonHTTPSAndLocalIPs(t *testing.T) {
	if err := validateArtifactURL("http://example.com/pack.json"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("validateArtifactURL http err = %v, want ErrUnsafeURL", err)
	}
	if !isUnsafeIP(net.ParseIP("127.0.0.1")) {
		t.Fatal("127.0.0.1 should be unsafe")
	}
	if !isUnsafeIP(net.ParseIP("169.254.169.254")) {
		t.Fatal("link-local metadata address should be unsafe")
	}
}

func TestFindCandidatesDoesNotReuseScopedParams(t *testing.T) {
	graph := &mutatingScopedGraph{}
	svc := New(Dependencies{Graph: graph})

	res, err := svc.FindCandidates(context.Background(), "team-1", FindCandidatesRequest{
		Query: "skill pack",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("FindCandidates: %v", err)
	}
	if graph.reads != 2 {
		t.Fatalf("graph reads = %d, want 2", graph.reads)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1", len(res.Candidates))
	}
	if res.Candidates[0].ID != "claim-1" {
		t.Fatalf("candidate id = %q, want claim-1", res.Candidates[0].ID)
	}
}

func TestReviewImportWritesFragmentClaimAndLedger(t *testing.T) {
	fragmentCreate := &fakeFragmentCreate{}
	claimCreate := &fakeClaimCreate{}
	ledger := &fakeLedger{}
	svc := New(Dependencies{
		FragmentCreate: fragmentCreate,
		ClaimCreate:    claimCreate,
		Ledger:         ledger,
	})
	pack := &SkillPack{
		SchemaVersion: SchemaVersion,
		Name:          "Review import",
		Items: []SkillPackItem{{
			Subject:    "assistant",
			Predicate:  "has_skill",
			Object:     "imports skill packs in review mode",
			SourceKind: SourceKindManual,
		}},
	}

	res, err := svc.Import(context.Background(), "team-1", ImportRequest{
		Artifact: pack,
		Mode:     ModeReview,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Status != domain.SkillPackImportStatusApplied {
		t.Fatalf("status = %q, want applied", res.Status)
	}
	if res.ImportID == "" {
		t.Fatal("import_id should be returned")
	}
	if fragmentCreate.calls != 1 || claimCreate.calls != 1 {
		t.Fatalf("writes = fragments:%d claims:%d, want 1/1", fragmentCreate.calls, claimCreate.calls)
	}
	if len(ledger.imports) != 1 {
		t.Fatalf("ledger imports len = %d, want 1", len(ledger.imports))
	}
	if len(ledger.changes) != 2 {
		t.Fatalf("ledger changes len = %d, want fragment+claim changes", len(ledger.changes))
	}
	if ledger.status != domain.SkillPackImportStatusApplied {
		t.Fatalf("ledger status = %q, want applied", ledger.status)
	}
}

func TestStateMatchesDetectsRollbackDrift(t *testing.T) {
	if !stateMatches(map[string]any{"status": "candidate"}, map[string]any{"status": "candidate"}) {
		t.Fatal("matching states should pass")
	}
	if stateMatches(map[string]any{"status": "validated"}, map[string]any{"status": "candidate"}) {
		t.Fatal("changed states should fail")
	}
}

func TestStateMatchesComparesLedgerTimeStringsWithGraphTimes(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 34, 56, 789, time.UTC)
	if !stateMatches(map[string]any{"verified_at": now}, map[string]any{"verified_at": now.Format(time.RFC3339Nano)}) {
		t.Fatal("ledger timestamp string should match graph timestamp")
	}
	if stateMatches(map[string]any{"verified_at": now.Add(time.Second)}, map[string]any{"verified_at": now.Format(time.RFC3339Nano)}) {
		t.Fatal("different timestamps should fail")
	}
}

func TestClaimLedgerStateOmitsEmptyOptionalVerifierFields(t *testing.T) {
	state := claimLedgerState(&domain.Claim{
		ClaimID:           "claim-1",
		Subject:           "assistant",
		Predicate:         "has_skill",
		Object:            "answering with project context",
		Status:            domain.StatusCandidate,
		EntailmentVerdict: domain.VerdictInsufficient,
	}, "import-1")

	if _, ok := state["verifier_model"]; ok {
		t.Fatal("empty verifier_model should not be recorded")
	}
	if _, ok := state["last_verifier_response"]; ok {
		t.Fatal("empty last_verifier_response should not be recorded")
	}
	if state["import_id"] != "import-1" {
		t.Fatalf("import_id = %v, want import-1", state["import_id"])
	}
}

type fakeFactGet struct {
	facts map[string]*domain.Fact
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

func (f fakeFactGet) Get(_ context.Context, _ string, factID string) (*domain.Fact, error) {
	return f.facts[factID], nil
}

type fakeClaimGet struct {
	claims map[string]*domain.Claim
}

func (f fakeClaimGet) Get(_ context.Context, _ string, claimID string) (*domain.Claim, error) {
	return f.claims[claimID], nil
}

type fakeFactList struct {
	facts []*domain.Fact
}

func (f fakeFactList) List(_ context.Context, _ string, filters factservice.FactListFilters, _ int, _ string) ([]*domain.Fact, string, error) {
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

type fakeFragmentCreate struct {
	calls int
}

func (f *fakeFragmentCreate) Create(_ context.Context, profileID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	f.calls++
	return &fragmentservice.CreateResult{
		Fragment: &domain.Fragment{
			FragmentID:  "fragment-1",
			ProfileID:   profileID,
			Content:     req.Content,
			ContentHash: "hash",
			CreatedAt:   time.Now().UTC(),
		},
	}, nil
}

type fakeClaimCreate struct {
	calls int
}

func (f *fakeClaimCreate) Create(_ context.Context, profileID string, claim *domain.Claim) (*claimservice.CreateResult, error) {
	f.calls++
	claim.ClaimID = "claim-1"
	claim.ProfileID = profileID
	claim.Status = domain.StatusCandidate
	claim.EntailmentVerdict = domain.VerdictInsufficient
	return &claimservice.CreateResult{Claim: claim}, nil
}

type fakeLedger struct {
	imports []domain.SkillPackImport
	changes []domain.SkillPackImportChange
	status  string
}

func (f *fakeLedger) CreateImport(_ context.Context, record domain.SkillPackImport) error {
	f.imports = append(f.imports, record)
	f.status = record.Status
	return nil
}
func (f *fakeLedger) UpdateImportStatus(_ context.Context, _, _, status string, _, _ int, _ map[string]any) error {
	f.status = status
	return nil
}
func (f *fakeLedger) MarkRolledBack(context.Context, string, string) error {
	f.status = domain.SkillPackImportStatusRolledBack
	return nil
}
func (f *fakeLedger) GetImport(context.Context, string, string) (*domain.SkillPackImport, error) {
	return &domain.SkillPackImport{Status: domain.SkillPackImportStatusApplied}, nil
}
func (f *fakeLedger) AppendChange(_ context.Context, change domain.SkillPackImportChange) error {
	f.changes = append(f.changes, change)
	return nil
}
func (f *fakeLedger) ListChanges(context.Context, string, string) ([]domain.SkillPackImportChange, error) {
	return f.changes, nil
}
