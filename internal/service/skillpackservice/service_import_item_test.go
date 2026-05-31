package skillpackservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestImportItemErrorAndDuplicateBranches(t *testing.T) {
	ctx := context.Background()
	item := SkillPackItem{
		Subject:    "assistant",
		Predicate:  "has_skill",
		Object:     "tests import item branches",
		SourceKind: SourceKindManual,
	}
	inspected := InspectItem{Index: 0, Item: item}

	createErrSvc := New(Dependencies{ClaimCreate: &fakeClaimCreate{err: errors.New("claim create failed")}}).(*service)
	createErr := createErrSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeReview, item, inspected, "")
	if createErr.Status != "error" || !strings.Contains(createErr.Error, "claim create failed") {
		t.Fatalf("createErr = %+v, want claim create error", createErr)
	}

	graphErr := errors.New("graph write failed")
	reviewGraphErrSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		Graph:       &recordingGraph{writeErr: graphErr},
		Ledger:      &fakeLedger{},
	}).(*service)
	reviewGraphErr := reviewGraphErrSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeReview, item, inspected, "")
	if reviewGraphErr.Status != "error" || !strings.Contains(reviewGraphErr.Error, "graph write failed") {
		t.Fatalf("reviewGraphErr = %+v, want graph write error", reviewGraphErr)
	}

	trustedGraphErrSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		Graph:       &recordingGraph{writeErr: graphErr},
		Ledger:      &fakeLedger{},
	}).(*service)
	trustedGraphErr := trustedGraphErrSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeTrusted, item, inspected, "")
	if trustedGraphErr.Status != "error" || !strings.Contains(trustedGraphErr.Error, "graph write failed") {
		t.Fatalf("trustedGraphErr = %+v, want graph write error", trustedGraphErr)
	}

	promoteErrItem := item
	promoteErrItem.SourceKind = SourceKindFact
	promoteErrSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		FactPromote: fakeFactPromote{err: errors.New("promote failed")},
		Graph:       &recordingGraph{},
		Ledger:      &fakeLedger{},
	}).(*service)
	promoteErr := promoteErrSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeTrusted, promoteErrItem, inspected, "")
	if promoteErr.Status != "error" || !strings.Contains(promoteErr.Error, "promote failed") {
		t.Fatalf("promoteErr = %+v, want promote error", promoteErr)
	}

	finalClaim := &domain.Claim{
		ClaimID:              "claim-1",
		Subject:              item.Subject,
		Predicate:            item.Predicate,
		Object:               item.Object,
		Status:               domain.StatusValidated,
		EntailmentVerdict:    domain.VerdictEntailed,
		VerifierModel:        "skill_pack.source_trust",
		LastVerifierResponse: "trusted source",
	}
	ledger := &fakeLedger{}
	finalClaimSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		ClaimGet:    fakeClaimGet{claims: map[string]*domain.Claim{"claim-1": finalClaim}},
		Graph:       &recordingGraph{},
		Ledger:      ledger,
	}).(*service)
	validated := finalClaimSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeTrusted, item, inspected, "")
	if validated.Status != "validated" || len(ledger.changes) != 1 {
		t.Fatalf("validated = %+v changes=%d, want one claim change", validated, len(ledger.changes))
	}
	if ledger.changes[0].AfterState["verifier_model"] != "skill_pack.source_trust" {
		t.Fatalf("after state = %v, want final claim verifier fields", ledger.changes[0].AfterState)
	}

	existingClaim := &domain.Claim{
		ClaimID:           "claim-dup",
		Subject:           item.Subject,
		Predicate:         item.Predicate,
		Object:            item.Object,
		Status:            domain.StatusCandidate,
		EntailmentVerdict: domain.VerdictInsufficient,
	}
	duplicateLedger := &fakeLedger{}
	duplicateGraph := &recordingGraph{}
	duplicateSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{duplicate: true, claimID: "claim-dup"},
		ClaimGet:    fakeClaimGet{claims: map[string]*domain.Claim{"claim-dup": existingClaim}},
		Graph:       duplicateGraph,
		Ledger:      duplicateLedger,
	}).(*service)
	duplicate := duplicateSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeTrusted, item, inspected, "")
	if duplicate.Status != "validated" || len(duplicateLedger.changes) != 1 {
		t.Fatalf("duplicate = %+v changes=%d, want updated existing claim", duplicate, len(duplicateLedger.changes))
	}
	if duplicateLedger.changes[0].Action != domain.SkillPackChangeActionUpdated ||
		duplicateLedger.changes[0].BeforeState["status"] != string(domain.StatusCandidate) {
		t.Fatalf("duplicate change = %+v, want updated candidate claim", duplicateLedger.changes[0])
	}
	if _, ok := duplicateLedger.changes[0].AfterState["import_id"]; ok {
		t.Fatalf("duplicate after state = %v, want no new import provenance", duplicateLedger.changes[0].AfterState)
	}
	if len(duplicateGraph.writeQueries) != 1 || strings.Contains(duplicateGraph.writeQueries[0], "import_id") {
		t.Fatalf("duplicate graph writes = %q, want trust without import tag", duplicateGraph.writeQueries)
	}

	duplicateFactItem := item
	duplicateFactItem.SourceKind = SourceKindFact
	duplicateFactLedger := &fakeLedger{}
	duplicateFactSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{duplicate: true, claimID: "claim-dup"},
		ClaimGet:    fakeClaimGet{claims: map[string]*domain.Claim{"claim-dup": existingClaim}},
		FactPromote: fakeFactPromote{},
		Graph:       &recordingGraph{},
		Ledger:      duplicateFactLedger,
	}).(*service)
	promoted := duplicateFactSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeTrusted, duplicateFactItem, inspected, "")
	if promoted.Status != "promoted" || promoted.FactID == "" || len(duplicateFactLedger.changes) != 2 {
		t.Fatalf("promoted = %+v changes=%d, want updated claim and created fact", promoted, len(duplicateFactLedger.changes))
	}
	if duplicateFactLedger.changes[0].Action != domain.SkillPackChangeActionUpdated {
		t.Fatalf("first duplicate fact change = %+v, want updated claim", duplicateFactLedger.changes[0])
	}
	if _, ok := duplicateFactLedger.changes[0].AfterState["import_id"]; ok {
		t.Fatalf("duplicate fact after state = %v, want no new claim import provenance", duplicateFactLedger.changes[0].AfterState)
	}

	appendErrSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		Ledger:      &fakeLedger{appendErr: errors.New("append failed")},
	}).(*service)
	appendErr := appendErrSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeReview, item, inspected, "")
	if appendErr.Status != "error" || !strings.Contains(appendErr.Error, "append failed") {
		t.Fatalf("appendErr = %+v, want ledger append error", appendErr)
	}
}

func TestImportItemPromotionAndSupersedeErrorBranches(t *testing.T) {
	ctx := context.Background()
	factItem := SkillPackItem{
		Subject:    "assistant",
		Predicate:  "has_skill",
		Object:     "tests import item branches",
		SourceKind: SourceKindFact,
	}
	inspected := InspectItem{Index: 0, Item: factItem}

	tagFactErrSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		FactPromote: fakeFactPromote{},
		Graph:       &recordingGraph{writeErr: errors.New("tag fact failed"), writeErrAfter: 2},
		Ledger:      &fakeLedger{},
	}).(*service)
	tagFactErr := tagFactErrSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeTrusted, factItem, inspected, "")
	if tagFactErr.Status != "error" || !strings.Contains(tagFactErr.Error, "tag fact failed") {
		t.Fatalf("tagFactErr = %+v, want tag fact error", tagFactErr)
	}

	factAppendErrSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		FactPromote: fakeFactPromote{},
		Graph:       &recordingGraph{},
		Ledger:      &fakeLedger{appendErr: errors.New("fact append failed"), appendErrAfter: 2},
	}).(*service)
	factAppendErr := factAppendErrSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeTrusted, factItem, inspected, "")
	if factAppendErr.Status != "error" || !strings.Contains(factAppendErr.Error, "fact append failed") {
		t.Fatalf("factAppendErr = %+v, want fact append error", factAppendErr)
	}

	manualItem := SkillPackItem{
		Subject:    "assistant",
		Predicate:  "has_skill",
		Object:     "tests supersede errors",
		SourceKind: SourceKindManual,
	}
	supersedeInspected := InspectItem{
		Index: 0,
		Item:  manualItem,
		ConflictingFacts: []FactSummary{{
			FactID:    "fact-local",
			Subject:   manualItem.Subject,
			Predicate: manualItem.Predicate,
			Object:    "old behavior",
			Status:    string(domain.FactStatusActive),
		}},
	}
	supersedeErrSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		FactGet: fakeFactGet{facts: map[string]*domain.Fact{"fact-local": &domain.Fact{
			FactID:    "fact-local",
			Subject:   manualItem.Subject,
			Predicate: manualItem.Predicate,
			Object:    "old behavior",
			Status:    domain.FactStatusActive,
		}}},
		Graph:  &recordingGraph{txErr: errors.New("supersede failed")},
		Ledger: &fakeLedger{},
	}).(*service)
	supersedeErr := supersedeErrSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeTrusted, manualItem, supersedeInspected, DecisionSupersedeLocal)
	if supersedeErr.Status != "error" || !strings.Contains(supersedeErr.Error, "supersede failed") {
		t.Fatalf("supersedeErr = %+v, want supersede error", supersedeErr)
	}
}

func TestImportItemReviewDuplicateAndFactFinalClaimBranches(t *testing.T) {
	ctx := context.Background()
	item := SkillPackItem{
		Subject:    "assistant",
		Predicate:  "has_skill",
		Object:     "tests duplicate review branches",
		SourceKind: SourceKindManual,
	}
	inspected := InspectItem{Index: 0, Item: item}

	reviewDuplicateLedger := &fakeLedger{}
	reviewDuplicateGraph := &recordingGraph{}
	reviewDuplicateSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{duplicate: true, claimID: "claim-dup"},
		Graph:       reviewDuplicateGraph,
		Ledger:      reviewDuplicateLedger,
	}).(*service)
	reviewDuplicate := reviewDuplicateSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeReview, item, inspected, "")
	if reviewDuplicate.Status != "imported" || len(reviewDuplicateLedger.changes) != 0 {
		t.Fatalf("reviewDuplicate = %+v changes=%d, want imported without claim change", reviewDuplicate, len(reviewDuplicateLedger.changes))
	}
	if reviewDuplicateGraph.writeCount != 0 {
		t.Fatalf("graph writes = %d, want no duplicate claim tag", reviewDuplicateGraph.writeCount)
	}

	factItem := item
	factItem.SourceKind = SourceKindFact
	finalClaim := &domain.Claim{
		ClaimID:              "claim-1",
		Subject:              factItem.Subject,
		Predicate:            factItem.Predicate,
		Object:               factItem.Object,
		Status:               domain.StatusSuperseded,
		EntailmentVerdict:    domain.VerdictEntailed,
		VerifierModel:        "skill_pack.source_trust",
		LastVerifierResponse: "trusted source",
	}
	factLedger := &fakeLedger{}
	factSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		ClaimGet:    fakeClaimGet{claims: map[string]*domain.Claim{"claim-1": finalClaim}},
		FactPromote: fakeFactPromote{},
		Graph:       &recordingGraph{},
		Ledger:      factLedger,
	}).(*service)
	promoted := factSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeTrusted, factItem, inspected, "")
	if promoted.Status != "promoted" || len(factLedger.changes) != 2 {
		t.Fatalf("promoted = %+v changes=%d, want promoted with claim and fact changes", promoted, len(factLedger.changes))
	}
	if factLedger.changes[0].AfterState["verifier_model"] != "skill_pack.source_trust" {
		t.Fatalf("after state = %v, want final claim verifier fields", factLedger.changes[0].AfterState)
	}
}
