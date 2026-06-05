package skillpackservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
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
	if res.Artifact.ExportedAt == nil {
		t.Fatal("exported_at is required on exported artifacts")
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

func TestExportAllowsMoreThanOneHundredFacts(t *testing.T) {
	facts := make(map[string]*domain.Fact)
	factIDs := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		factID := fmt.Sprintf("fact-%03d", i)
		factIDs = append(factIDs, factID)
		facts[factID] = &domain.Fact{
			FactID:    factID,
			Subject:   "assistant",
			Predicate: "has_skill",
			Object:    fmt.Sprintf("skill %03d", i),
			Status:    domain.FactStatusActive,
		}
	}
	svc := New(Dependencies{FactGet: fakeFactGet{facts: facts}})

	res, err := svc.Export(context.Background(), "team-1", ExportRequest{
		Name:    "Large pack",
		FactIDs: factIDs,
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.ItemCount != len(factIDs) {
		t.Fatalf("item_count = %d, want %d", res.ItemCount, len(factIDs))
	}
	if len(res.Artifact.Items) != len(factIDs) {
		t.Fatalf("items len = %d, want %d", len(res.Artifact.Items), len(factIDs))
	}
}

func TestExportIncludesSelectedFactSupportGraph(t *testing.T) {
	fact := &domain.Fact{
		FactID:              "fact-1",
		Subject:             "assistant",
		Predicate:           "has_skill",
		Object:              "writes useful blog outlines",
		Status:              domain.FactStatusActive,
		PromotedFromClaimID: "claim-1",
	}
	claim := &domain.Claim{
		ClaimID:        "claim-1",
		Subject:        fact.Subject,
		Predicate:      fact.Predicate,
		Object:         fact.Object,
		Status:         domain.StatusSuperseded,
		Modality:       domain.ModalityAssertion,
		Polarity:       domain.PolarityPlus,
		Speaker:        "user",
		ExtractConf:    0.91,
		ResolutionConf: 0.92,
		SupportedBy:    []string{"fragment-1"},
	}
	graph := &recordingGraph{supportFragments: map[string]SkillPackSupportFragment{
		"fragment-1": {
			FragmentID:    "fragment-1",
			Content:       "A blog outline should define audience, thesis, claims, and success checks.",
			Source:        "conversation",
			SourceType:    "conversation",
			Authority:     string(domain.AuthorityPrimary),
			SourceQuality: optionalFloat64(0.95),
		},
	}}
	svc := New(Dependencies{
		FactGet:  fakeFactGet{facts: map[string]*domain.Fact{"fact-1": fact}},
		ClaimGet: fakeClaimGet{claims: map[string]*domain.Claim{"claim-1": claim}},
		Graph:    graph,
	})

	res, err := svc.Export(context.Background(), "team-1", ExportRequest{
		Name:    "Blog writing",
		FactIDs: []string{"fact-1"},
	})
	if err != nil {
		t.Fatalf("Export with support: %v", err)
	}
	if res.Filename != "blog-writing.skill-pack.json" || res.ContentType != "application/json" {
		t.Fatalf("file metadata = %q/%q, want blog-writing.skill-pack.json/application/json", res.Filename, res.ContentType)
	}
	if len(res.Artifact.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(res.Artifact.Items))
	}
	item := res.Artifact.Items[0]
	if item.SourceID != "fact-1" || len(item.SupportClaimIDs) != 1 || item.SupportClaimIDs[0] != "claim-1" {
		t.Fatalf("item support = %+v, want source fact and support claim", item)
	}
	if res.Artifact.Support == nil || len(res.Artifact.Support.Claims) != 1 || len(res.Artifact.Support.Fragments) != 1 {
		t.Fatalf("support = %+v, want one claim and one fragment", res.Artifact.Support)
	}
	if res.Artifact.Support.Fragments[0].Content == "" ||
		strings.Contains(res.CanonicalJSON, "team_id") ||
		strings.Contains(res.CanonicalJSON, "extract_conf") ||
		strings.Contains(res.CanonicalJSON, "resolution_conf") ||
		strings.Contains(res.CanonicalJSON, "extraction_model") ||
		strings.Contains(res.CanonicalJSON, "extraction_version") {
		t.Fatalf("canonical support JSON = %s", res.CanonicalJSON)
	}
}

func TestExportRejectsUnsupportedPredicatesAndDefaultsManualSource(t *testing.T) {
	badFactSvc := New(Dependencies{
		FactGet: fakeFactGet{facts: map[string]*domain.Fact{"fact-1": &domain.Fact{
			FactID:    "fact-1",
			Subject:   "assistant",
			Predicate: "bad",
			Object:    "testing",
			Status:    domain.FactStatusActive,
		}}},
	})
	if _, err := badFactSvc.Export(context.Background(), "team-1", ExportRequest{Name: "Pack", FactIDs: []string{"fact-1"}}); err == nil || !strings.Contains(err.Error(), "predicate") {
		t.Fatalf("bad fact predicate err = %v, want predicate error", err)
	}

	badClaimSvc := New(Dependencies{
		ClaimGet: fakeClaimGet{claims: map[string]*domain.Claim{"claim-1": &domain.Claim{
			ClaimID:   "claim-1",
			Subject:   "assistant",
			Predicate: "bad",
			Object:    "testing",
			Status:    domain.StatusValidated,
		}}},
	})
	if _, err := badClaimSvc.Export(context.Background(), "team-1", ExportRequest{Name: "Pack", ClaimIDs: []string{"claim-1"}}); err == nil || !strings.Contains(err.Error(), "predicate") {
		t.Fatalf("bad claim predicate err = %v, want predicate error", err)
	}

	res, err := New(Dependencies{}).Export(context.Background(), "team-1", ExportRequest{
		Name: "Pack",
		ManualItems: []SkillPackItem{{
			Subject:   "assistant",
			Predicate: "has_skill",
			Object:    "defaults manual source kind",
		}},
	})
	if err != nil {
		t.Fatalf("Export manual default source: %v", err)
	}
	if got := res.Artifact.Items[0].SourceKind; got != SourceKindManual {
		t.Fatalf("manual source_kind = %q, want %q", got, SourceKindManual)
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

func TestInspectReportsDuplicateFacts(t *testing.T) {
	svc := New(Dependencies{
		FactList: fakeFactList{facts: []*domain.Fact{{
			FactID:    "fact-local",
			Subject:   "assistant",
			Predicate: "uses",
			Object:    "pnpm",
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
	if got := res.Items[0].Status; got != "duplicate_fact" {
		t.Fatalf("status = %q, want duplicate_fact", got)
	}
	if len(res.Items[0].MatchingFacts) != 1 {
		t.Fatalf("matching_facts len = %d, want 1", len(res.Items[0].MatchingFacts))
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

func TestRollbackRevertsCreatedAndUpdatedEntities(t *testing.T) {
	importID := "import-1"
	ledger := &fakeLedger{changes: []domain.SkillPackImportChange{
		{
			ImportID:    importID,
			TeamID:      "team-1",
			EntityType:  "fragment",
			EntityID:    "fragment-1",
			Action:      domain.SkillPackChangeActionCreated,
			AfterState:  map[string]any{"fragment_id": "fragment-1", "import_id": importID},
			BeforeState: nil,
		},
		{
			ImportID:   importID,
			TeamID:     "team-1",
			EntityType: "claim",
			EntityID:   "claim-1",
			Action:     domain.SkillPackChangeActionUpdated,
			BeforeState: map[string]any{
				"claim_id":           "claim-1",
				"status":             string(domain.StatusCandidate),
				"entailment_verdict": string(domain.VerdictInsufficient),
			},
			AfterState: map[string]any{
				"claim_id":           "claim-1",
				"status":             string(domain.StatusValidated),
				"entailment_verdict": string(domain.VerdictEntailed),
				"import_id":          importID,
			},
		},
	}}
	graph := &recordingGraph{states: map[string]map[string]any{
		"fragment-1": {"fragment_id": "fragment-1", "import_id": importID},
		"claim-1": {
			"claim_id":           "claim-1",
			"status":             string(domain.StatusValidated),
			"entailment_verdict": string(domain.VerdictEntailed),
			"import_id":          importID,
		},
	}}
	svc := New(Dependencies{Ledger: ledger, Graph: graph})

	res, err := svc.Rollback(context.Background(), "team-1", RollbackRequest{ImportID: importID})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if res.Status != domain.SkillPackImportStatusRolledBack || res.RevertedCount != 2 {
		t.Fatalf("rollback = %+v, want rolled_back count 2", res)
	}
	if graph.writeCount != 2 {
		t.Fatalf("writeCount = %d, want delete+restore", graph.writeCount)
	}

	graph.states["claim-1"]["status"] = string(domain.StatusDisputed)
	ledger.status = domain.SkillPackImportStatusApplied
	blocked, err := svc.Rollback(context.Background(), "team-1", RollbackRequest{ImportID: importID})
	if err != nil {
		t.Fatalf("Rollback drift: %v", err)
	}
	if blocked.Status != "blocked" || len(blocked.Conflicts) == 0 {
		t.Fatalf("blocked rollback = %+v, want conflicts", blocked)
	}
}

func TestRollbackErrorAndFactSupersedeBranches(t *testing.T) {
	svc := New(Dependencies{})
	if _, err := svc.Rollback(context.Background(), "team-1", RollbackRequest{}); err == nil {
		t.Fatal("missing import_id should fail")
	}
	if _, err := svc.Rollback(context.Background(), "team-1", RollbackRequest{ImportID: "import-1"}); err == nil {
		t.Fatal("missing ledger should fail")
	}

	rolledLedger := &fakeLedger{status: domain.SkillPackImportStatusRolledBack}
	svc = New(Dependencies{Ledger: rolledLedger, Graph: &recordingGraph{}})
	if _, err := svc.Rollback(context.Background(), "team-1", RollbackRequest{ImportID: "import-1"}); err == nil || !strings.Contains(err.Error(), "already rolled back") {
		t.Fatalf("already rolled back err = %v", err)
	}

	readErrGraph := &recordingGraph{readErr: errors.New("graph unavailable")}
	ledger := &fakeLedger{changes: []domain.SkillPackImportChange{{
		ImportID:   "import-1",
		TeamID:     "team-1",
		EntityType: "claim",
		EntityID:   "claim-1",
		Action:     domain.SkillPackChangeActionCreated,
		AfterState: map[string]any{"claim_id": "claim-1"},
	}}}
	svc = New(Dependencies{Ledger: ledger, Graph: readErrGraph})
	blocked, err := svc.Rollback(context.Background(), "team-1", RollbackRequest{ImportID: "import-1"})
	if err != nil {
		t.Fatalf("rollback read error: %v", err)
	}
	if blocked.Status != "blocked" || len(blocked.Conflicts) != 1 {
		t.Fatalf("blocked = %+v, want read conflict", blocked)
	}

	factLedger := &fakeLedger{changes: []domain.SkillPackImportChange{{
		ImportID:    "import-1",
		TeamID:      "team-1",
		EntityType:  "fact",
		EntityID:    "fact-1",
		Action:      domain.SkillPackChangeActionSuperseded,
		BeforeState: map[string]any{"fact_id": "fact-1", "status": string(domain.FactStatusActive)},
		AfterState:  map[string]any{"fact_id": "fact-1", "status": string(domain.FactStatusSuperseded)},
	}}}
	factGraph := &recordingGraph{states: map[string]map[string]any{
		"fact-1": {"fact_id": "fact-1", "status": string(domain.FactStatusSuperseded)},
	}}
	svc = New(Dependencies{Ledger: factLedger, Graph: factGraph})
	res, err := svc.Rollback(context.Background(), "team-1", RollbackRequest{ImportID: "import-1"})
	if err != nil {
		t.Fatalf("fact rollback: %v", err)
	}
	if res.Status != domain.SkillPackImportStatusRolledBack || factGraph.txCount != 1 {
		t.Fatalf("fact rollback = %+v txCount=%d, want rolled_back tx", res, factGraph.txCount)
	}
}

func TestRollbackBlocksWhenImportedEntityIsMissing(t *testing.T) {
	ledger := &fakeLedger{changes: []domain.SkillPackImportChange{{
		ImportID:   "import-1",
		TeamID:     "team-1",
		EntityType: "claim",
		EntityID:   "claim-1",
		Action:     domain.SkillPackChangeActionCreated,
		AfterState: map[string]any{"claim_id": "claim-1"},
	}}}
	svc := New(Dependencies{Ledger: ledger, Graph: &recordingGraph{}})

	res, err := svc.Rollback(context.Background(), "team-1", RollbackRequest{ImportID: "import-1"})
	if err != nil {
		t.Fatalf("Rollback missing entity: %v", err)
	}
	if res.Status != "blocked" || len(res.Conflicts) != 1 || !strings.Contains(res.Conflicts[0], "is missing") {
		t.Fatalf("rollback = %+v, want missing entity conflict", res)
	}
}

func TestRollbackActionWriteErrorBranches(t *testing.T) {
	tests := []struct {
		name   string
		change domain.SkillPackImportChange
		graph  *recordingGraph
		want   string
	}{
		{
			name: "delete created entity fails",
			change: domain.SkillPackImportChange{
				ImportID:   "import-1",
				TeamID:     "team-1",
				EntityType: "fragment",
				EntityID:   "fragment-1",
				Action:     domain.SkillPackChangeActionCreated,
				AfterState: map[string]any{
					"fragment_id": "fragment-1",
					"import_id":   "import-1",
				},
			},
			graph: &recordingGraph{
				states:   map[string]map[string]any{"fragment-1": {"fragment_id": "fragment-1", "import_id": "import-1"}},
				writeErr: errors.New("delete failed"),
			},
			want: "delete failed",
		},
		{
			name: "restore updated claim fails",
			change: domain.SkillPackImportChange{
				ImportID:   "import-1",
				TeamID:     "team-1",
				EntityType: "claim",
				EntityID:   "claim-1",
				Action:     domain.SkillPackChangeActionUpdated,
				BeforeState: map[string]any{
					"claim_id":           "claim-1",
					"status":             string(domain.StatusCandidate),
					"entailment_verdict": string(domain.VerdictInsufficient),
				},
				AfterState: map[string]any{
					"claim_id":           "claim-1",
					"status":             string(domain.StatusValidated),
					"entailment_verdict": string(domain.VerdictEntailed),
					"import_id":          "import-1",
				},
			},
			graph: &recordingGraph{
				states:   map[string]map[string]any{"claim-1": {"claim_id": "claim-1", "status": string(domain.StatusValidated), "entailment_verdict": string(domain.VerdictEntailed), "import_id": "import-1"}},
				writeErr: errors.New("restore claim failed"),
			},
			want: "restore claim failed",
		},
		{
			name: "restore superseded fact fails",
			change: domain.SkillPackImportChange{
				ImportID:    "import-1",
				TeamID:      "team-1",
				EntityType:  "fact",
				EntityID:    "fact-1",
				Action:      domain.SkillPackChangeActionSuperseded,
				BeforeState: map[string]any{"fact_id": "fact-1", "status": string(domain.FactStatusActive)},
				AfterState:  map[string]any{"fact_id": "fact-1", "status": string(domain.FactStatusSuperseded)},
			},
			graph: &recordingGraph{
				states: map[string]map[string]any{"fact-1": {"fact_id": "fact-1", "status": string(domain.FactStatusSuperseded)}},
				txErr:  errors.New("restore fact failed"),
			},
			want: "restore fact failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Dependencies{
				Ledger: &fakeLedger{changes: []domain.SkillPackImportChange{tc.change}},
				Graph:  tc.graph,
			})
			_, err := svc.Rollback(context.Background(), "team-1", RollbackRequest{ImportID: "import-1"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("rollback err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGraphOpsQueryHelpersAndState(t *testing.T) {
	graph := &recordingGraph{states: map[string]map[string]any{
		"claim-1": {"claim_id": "claim-1", "status": "validated"},
	}}
	ops := newGraphOps(graph)
	ctx := context.Background()
	if err := ops.tagFragment(ctx, "team-1", "fragment-1", "import-1", "hash"); err != nil {
		t.Fatalf("tagFragment: %v", err)
	}
	if err := ops.tagClaim(ctx, "team-1", "claim-1", "import-1", "hash", SourceKindManual); err != nil {
		t.Fatalf("tagClaim: %v", err)
	}
	if err := ops.trustClaim(ctx, "team-1", "claim-1", "import-1", "hash", SourceKindManual); err != nil {
		t.Fatalf("trustClaim: %v", err)
	}
	if err := ops.trustExistingClaim(ctx, "team-1", "claim-1"); err != nil {
		t.Fatalf("trustExistingClaim: %v", err)
	}
	if err := ops.tagFact(ctx, "team-1", "fact-1", "import-1", "hash", SourceKindFact); err != nil {
		t.Fatalf("tagFact: %v", err)
	}
	if err := ops.deleteEntity(ctx, "team-1", "claim", "claim-1"); err != nil {
		t.Fatalf("deleteEntity: %v", err)
	}
	if err := ops.restoreClaim(ctx, "team-1", "claim-1", "import-1", map[string]any{
		"status":             string(domain.StatusCandidate),
		"entailment_verdict": string(domain.VerdictInsufficient),
	}); err != nil {
		t.Fatalf("restoreClaim: %v", err)
	}
	restoreQuery := graph.writeQueries[len(graph.writeQueries)-1]
	if strings.Contains(restoreQuery, "REMOVE c.import_id") {
		t.Fatalf("restoreClaim query removes import metadata: %s", restoreQuery)
	}
	state, err := ops.currentState(ctx, "team-1", "claim", "claim-1")
	if err != nil {
		t.Fatalf("currentState: %v", err)
	}
	if state["status"] != "validated" {
		t.Fatalf("state = %v", state)
	}
	if _, _, err := deleteEntityQuery("bad", "id"); err == nil {
		t.Fatal("deleteEntityQuery bad type should fail")
	}
	if _, _, err := currentStateQuery("bad", "id"); err == nil {
		t.Fatal("currentStateQuery bad type should fail")
	}
	for _, entityType := range []string{"fragment", "claim", "fact"} {
		if _, params, err := deleteEntityQuery(entityType, "entity-1"); err != nil || params["entityId"] != "entity-1" {
			t.Fatalf("deleteEntityQuery(%s) params=%v err=%v", entityType, params, err)
		}
		if _, params, err := currentStateQuery(entityType, "entity-1"); err != nil || params["entityId"] != "entity-1" {
			t.Fatalf("currentStateQuery(%s) params=%v err=%v", entityType, params, err)
		}
	}
	nilOps := newGraphOps(nil)
	if err := nilOps.trustClaim(ctx, "team-1", "claim-1", "import-1", "hash", SourceKindManual); err == nil {
		t.Fatal("trustClaim without graph should fail")
	}
	if err := nilOps.trustExistingClaim(ctx, "team-1", "claim-1"); err == nil {
		t.Fatal("trustExistingClaim without graph should fail")
	}
	if err := nilOps.supersedeFacts(ctx, "team-1", nil, "claim-1", "import-1"); err != nil {
		t.Fatalf("supersedeFacts empty without graph: %v", err)
	}
	if err := nilOps.supersedeFacts(ctx, "team-1", []string{"fact-1"}, "claim-1", "import-1"); err == nil {
		t.Fatal("supersedeFacts without graph should fail")
	}
	if err := nilOps.deleteEntity(ctx, "team-1", "claim", "claim-1"); err == nil {
		t.Fatal("deleteEntity without graph should fail")
	}
	if err := nilOps.restoreClaim(ctx, "team-1", "claim-1", "import-1", nil); err == nil {
		t.Fatal("restoreClaim without graph should fail")
	}
	if err := nilOps.restoreFact(ctx, "team-1", "fact-1", "import-1", nil); err == nil {
		t.Fatal("restoreFact without graph should fail")
	}
	if _, err := nilOps.currentState(ctx, "team-1", "claim", "claim-1"); err == nil {
		t.Fatal("currentState without graph should fail")
	}
}

func TestServiceValidationAndInspectBranches(t *testing.T) {
	svc := New(Dependencies{})
	if _, err := svc.Export(context.Background(), "team-1", ExportRequest{}); err == nil {
		t.Fatal("export missing name should fail")
	}
	if _, err := svc.Export(context.Background(), "team-1", ExportRequest{Name: "Pack", FactIDs: []string{"fact-1"}}); err == nil || !strings.Contains(err.Error(), "fact get service is required") {
		t.Fatalf("fact get err = %v", err)
	}
	if _, err := svc.Export(context.Background(), "team-1", ExportRequest{Name: "Pack", ClaimIDs: []string{"claim-1"}}); err == nil || !strings.Contains(err.Error(), "claim get service is required") {
		t.Fatalf("claim get err = %v", err)
	}

	unvalidated := &domain.Claim{ClaimID: "claim-1", Subject: "assistant", Predicate: "has_skill", Object: "testing", Status: domain.StatusCandidate}
	svc = New(Dependencies{ClaimGet: fakeClaimGet{claims: map[string]*domain.Claim{"claim-1": unvalidated}}})
	if _, err := svc.Export(context.Background(), "team-1", ExportRequest{Name: "Pack", ClaimIDs: []string{"claim-1"}}); err == nil || !strings.Contains(err.Error(), "must be validated") {
		t.Fatalf("unvalidated claim err = %v", err)
	}

	svc = New(Dependencies{})
	if _, err := svc.Import(context.Background(), "team-1", ImportRequest{Mode: "bad"}); err == nil {
		t.Fatal("invalid mode should fail")
	}
	if _, err := svc.Import(context.Background(), "team-1", ImportRequest{Mode: ModeTrusted, URL: "https://example.com/pack.json"}); err == nil || !strings.Contains(err.Error(), "trusted URL imports require expected_sha256") {
		t.Fatalf("trusted URL err = %v", err)
	}
	if _, err := svc.Import(context.Background(), "team-1", ImportRequest{Mode: ModeReview}); err == nil || !strings.Contains(err.Error(), "import ledger is required") {
		t.Fatalf("missing ledger err = %v", err)
	}
	svc = New(Dependencies{Ledger: &fakeLedger{}})
	if _, err := svc.Import(context.Background(), "team-1", ImportRequest{Mode: ModeReview}); err == nil || !strings.Contains(err.Error(), "fragment and claim services are required") {
		t.Fatalf("missing write services err = %v", err)
	}

	svc = New(Dependencies{ClaimList: fakeClaimList{claims: []*domain.Claim{{
		ClaimID:   "claim-1",
		Subject:   "assistant",
		Predicate: "has_skill",
		Object:    "testing",
		Status:    domain.StatusCandidate,
	}}}})
	item := SkillPackItem{Subject: "assistant", Predicate: "has_skill", Object: "testing", SourceKind: SourceKindManual}
	inspected, err := svc.(*service).inspectItem(context.Background(), "team-1", 0, item)
	if err != nil {
		t.Fatalf("inspectItem: %v", err)
	}
	if inspected.Status != "already_claimed" {
		t.Fatalf("inspect status = %q, want already_claimed", inspected.Status)
	}
	unsupported, err := svc.(*service).inspectItem(context.Background(), "team-1", 0, SkillPackItem{Subject: "assistant", Predicate: "bad", Object: "testing", SourceKind: SourceKindManual})
	if err != nil {
		t.Fatalf("unsupported inspectItem: %v", err)
	}
	if unsupported.Status != "unsupported_predicate" {
		t.Fatalf("unsupported status = %q", unsupported.Status)
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
	if !stateMatches(map[string]any{"verified_at": now.Format(time.RFC3339Nano)}, map[string]any{"verified_at": now}) {
		t.Fatal("graph timestamp string should match ledger timestamp")
	}
	if stateMatches(map[string]any{"verified_at": now.Add(time.Second)}, map[string]any{"verified_at": now.Format(time.RFC3339Nano)}) {
		t.Fatal("different timestamps should fail")
	}
	if stateMatches(map[string]any{"verified_at": "not-time"}, map[string]any{"verified_at": now}) {
		t.Fatal("invalid timestamp should fail")
	}
	if stateMatches(map[string]any{}, map[string]any{"status": "candidate"}) {
		t.Fatal("missing current state key should fail")
	}
	if stateValueMatches(now, 123) {
		t.Fatal("time current value should not match non-string expected value")
	}
	if stateValueMatches(123, now) {
		t.Fatal("non-string current value should not match expected time")
	}
	if stateValueMatches(now, "not-time") {
		t.Fatal("time current value should not match unparsable expected time")
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

func TestRequiredDecisionAndLedgerHelperBranches(t *testing.T) {
	inspection := &InspectResult{DecisionsRequired: []ConflictPrompt{
		{Index: 0, Reason: "conflict", FactIDs: []string{"fact-1"}, AllowedActions: []string{DecisionImportAnyway, DecisionSkip, DecisionSupersedeLocal}},
		{Index: 1, Reason: "unselected conflict", FactIDs: []string{"fact-2"}, AllowedActions: []string{DecisionSkip}},
	}}

	missing := requiredDecisions(inspection, map[int]bool{0: true, 1: false}, nil)
	if len(missing) != 1 || missing[0].Index != 0 {
		t.Fatalf("missing decisions = %+v, want only selected prompt 0", missing)
	}
	invalid := requiredDecisions(inspection, map[int]bool{0: true}, map[int]string{0: "delete_local"})
	if len(invalid) != 1 || invalid[0].Reason != "invalid conflict decision" {
		t.Fatalf("invalid decisions = %+v, want invalid reason", invalid)
	}
	accepted := requiredDecisions(inspection, map[int]bool{0: true}, map[int]string{0: DecisionImportAnyway})
	if len(accepted) != 0 {
		t.Fatalf("accepted decisions = %+v, want none missing", accepted)
	}

	allSelected := selectedIndexSet(nil, 2)
	if !allSelected[0] || !allSelected[1] {
		t.Fatalf("allSelected = %+v, want both indexes selected", allSelected)
	}
	someSelected := selectedIndexSet([]int{-1, 1, 99}, 2)
	if someSelected[0] || !someSelected[1] || len(someSelected) != 1 {
		t.Fatalf("someSelected = %+v, want only index 1", someSelected)
	}
	decisions := conflictDecisionMap([]ConflictDecision{{Index: 2, Action: DecisionSkip}})
	if decisions[2] != DecisionSkip {
		t.Fatalf("decisions = %+v, want index 2 skip", decisions)
	}

	svc := New(Dependencies{}).(*service)
	if err := svc.appendChange(context.Background(), "team-1", "import-1", "claim", "claim-1", domain.SkillPackChangeActionCreated, nil, nil); err != nil {
		t.Fatalf("appendChange without ledger: %v", err)
	}

	verifiedAt := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	state := claimLedgerState(&domain.Claim{
		ClaimID:              "claim-1",
		Subject:              "assistant",
		Predicate:            "has_skill",
		Object:               "trusted import",
		Status:               domain.StatusValidated,
		EntailmentVerdict:    domain.VerdictEntailed,
		VerifierModel:        "skill_pack.source_trust",
		LastVerifierResponse: "trusted source",
		VerifiedAt:           &verifiedAt,
	}, "")
	if state["verifier_model"] != "skill_pack.source_trust" ||
		state["last_verifier_response"] != "trusted source" ||
		state["verified_at"] != verifiedAt.Format(time.RFC3339Nano) {
		t.Fatalf("claim state = %v, want verifier fields", state)
	}
	if _, ok := state["import_id"]; ok {
		t.Fatal("empty import id should not be recorded")
	}
}

func TestSkillPackSmallHelpers(t *testing.T) {
	if New(Dependencies{HistoryDays: 2}).(*service).retain != 48*time.Hour {
		t.Fatal("HistoryDays should configure import retention")
	}
	if tripleMatches("skill pack", "assistant", "has_skill", "skill pack testing") != true {
		t.Fatal("triple should match all query terms")
	}
	if tripleMatches("missing", "assistant", "has_skill", "skill pack testing") {
		t.Fatal("triple should not match missing term")
	}
	if clampLimit(0, 20, 100) != 20 || clampLimit(200, 20, 100) != 100 || clampLimit(5, 20, 100) != 5 {
		t.Fatal("clampLimit returned unexpected values")
	}
	if confidenceFor(ModeTrusted, SourceKindFact) != 0.98 || confidenceFor(ModeTrusted, SourceKindManual) != 0.9 || confidenceFor(ModeReview, SourceKindManual) != 0.8 {
		t.Fatal("confidenceFor returned unexpected values")
	}
	if sourceQuality(ModeTrusted) != 1.0 || sourceQuality(ModeReview) != 0.8 {
		t.Fatal("sourceQuality returned unexpected values")
	}
	if importAuthority(ModeTrusted) != "authoritative" || importAuthority(ModeReview) != "secondary" {
		t.Fatal("importAuthority returned unexpected values")
	}
	if importSource(SkillPack{Name: "Pack"}, "") != "skill_pack:Pack" || importSource(SkillPack{Name: "Pack"}, "https://example.com/p.json") != "https://example.com/p.json" {
		t.Fatal("importSource returned unexpected value")
	}
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	fact := &domain.Fact{
		FactID:          "fact-1",
		Subject:         "assistant",
		Predicate:       "has_skill",
		Object:          "testing",
		Status:          domain.FactStatusSuperseded,
		ValidTo:         &now,
		RecordedTo:      &now,
		RetractedAt:     &now,
		LastConfirmedAt: &now,
	}
	state := factLedgerStateWithImport(fact, "import-1")
	if state["import_id"] != "import-1" || state["status"] != string(domain.FactStatusSuperseded) {
		t.Fatalf("fact state = %v", state)
	}
	if factLedgerState(nil) != nil || claimSummary(nil).ClaimID != "" || factSummary(nil).FactID != "" {
		t.Fatal("nil summaries/states returned unexpected values")
	}
	if nullableStringState(map[string]any{"x": ""}, "x") != nil {
		t.Fatal("empty nullableStringState should be nil")
	}
	if nullableStringState(map[string]any{"x": "value"}, "x") != "value" {
		t.Fatal("nullableStringState should keep non-empty strings")
	}
	if nullableTimeState(map[string]any{"x": now}, "x") != now {
		t.Fatal("nullableTimeState should keep time.Time values")
	}
	if nullableTimeState(map[string]any{"x": now.Format(time.RFC3339Nano)}, "x") == nil {
		t.Fatal("nullableTimeState should parse RFC3339Nano")
	}
	if nullableTimeState(map[string]any{}, "x") != nil {
		t.Fatal("nullableTimeState should ignore missing values")
	}
	if nullableTimeState(map[string]any{"x": nil}, "x") != nil {
		t.Fatal("nullableTimeState should ignore nil values")
	}
	if nullableTimeState(map[string]any{"x": ""}, "x") != nil {
		t.Fatal("nullableTimeState should ignore empty strings")
	}
	if nullableTimeState(map[string]any{"x": "not-time"}, "x") != nil {
		t.Fatal("nullableTimeState should ignore unparsable strings")
	}
	if nullableTimeState(map[string]any{"x": 123}, "x") != nil {
		t.Fatal("nullableTimeState should ignore unsupported values")
	}
	content := fragmentContent(SkillPack{
		Name:        "Long",
		Description: strings.Repeat("d", 9000),
		Items: []SkillPackItem{{
			Subject:    "assistant",
			Predicate:  "has_skill",
			Object:     "testing",
			SourceKind: SourceKindManual,
		}},
	}, "hash")
	if len(content) != 8192 {
		t.Fatalf("fragmentContent len = %d, want capped 8192", len(content))
	}
}
