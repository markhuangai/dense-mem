package skillpackservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
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

func TestFindCandidatesFallsBackToFactAndClaimLists(t *testing.T) {
	svc := New(Dependencies{
		FactList: fakeFactList{facts: []*domain.Fact{{
			FactID:        "fact-1",
			Subject:       "assistant",
			Predicate:     "has_skill",
			Object:        "skill pack regression testing",
			Status:        domain.FactStatusActive,
			RecordedAt:    time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
			RecordedTo:    nil,
			SourceQuality: 0.9,
		}}},
		ClaimList: fakeClaimList{claims: []*domain.Claim{{
			ClaimID:    "claim-1",
			Subject:    "assistant",
			Predicate:  "uses",
			Object:     "portable memory artifacts",
			Status:     domain.StatusValidated,
			RecordedAt: time.Date(2026, 5, 31, 13, 0, 0, 0, time.UTC),
		}}},
	})

	res, err := svc.FindCandidates(context.Background(), "team-1", FindCandidatesRequest{
		Query: "skill pack",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("FindCandidates: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1", len(res.Candidates))
	}
	if res.Candidates[0].ID != "fact-1" || res.Candidates[0].Item.SourceKind != SourceKindFact {
		t.Fatalf("candidate = %+v, want fact-1 source_fact", res.Candidates[0])
	}

	claimRes, err := svc.FindCandidates(context.Background(), "team-1", FindCandidatesRequest{
		Query: "portable memory",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("FindCandidates claim fallback: %v", err)
	}
	if len(claimRes.Candidates) != 1 {
		t.Fatalf("claim candidates len = %d, want 1", len(claimRes.Candidates))
	}
	if claimRes.Candidates[0].ID != "claim-1" || claimRes.Candidates[0].Item.SourceKind != SourceKindValidatedClaim {
		t.Fatalf("candidate = %+v, want claim-1 source_validated_claim", claimRes.Candidates[0])
	}

	if _, err := svc.FindCandidates(context.Background(), "team-1", FindCandidatesRequest{}); err == nil {
		t.Fatal("empty query should fail")
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

func TestLoadArtifactBranches(t *testing.T) {
	ctx := context.Background()
	pack := packWithItem(SourceKindManual)
	canonical, hash, err := canonicalArtifact(*pack)
	if err != nil {
		t.Fatalf("canonicalArtifact: %v", err)
	}

	svc := New(Dependencies{}).(*service)
	loaded, gotHash, canonicalJSON, sourceURL, err := svc.loadArtifact(ctx, nil, string(canonical), "", "")
	if err != nil {
		t.Fatalf("load artifact JSON: %v", err)
	}
	if loaded.Name != pack.Name || gotHash != hash || canonicalJSON == "" || sourceURL != "" {
		t.Fatalf("loaded=%+v hash=%q canonical=%q sourceURL=%q", loaded, gotHash, canonicalJSON, sourceURL)
	}

	urlClient := &fakeHTTPClient{status: http.StatusOK, body: string(canonical)}
	svc = New(Dependencies{HTTPClient: urlClient}).(*service)
	_, gotHash, _, sourceURL, err = svc.loadArtifact(ctx, nil, "", "https://93.184.216.34/pack.json", hash)
	if err != nil {
		t.Fatalf("load artifact URL: %v", err)
	}
	if gotHash != hash || sourceURL != "https://93.184.216.34/pack.json" {
		t.Fatalf("hash=%q sourceURL=%q, want %q and URL", gotHash, sourceURL, hash)
	}

	if _, _, _, _, err := svc.loadArtifact(ctx, nil, "", "", ""); err == nil {
		t.Fatal("missing artifact inputs should fail")
	}
	if _, _, _, _, err := svc.loadArtifact(ctx, nil, "{", "", ""); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("bad JSON err = %v, want ErrInvalidArtifact", err)
	}
}

func TestParseArtifactJSONValidationBranches(t *testing.T) {
	valid := `{"schema_version":"dense-mem.skill_pack.v1","name":"Pack","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`
	pack, err := parseArtifactJSON([]byte(valid))
	if err != nil {
		t.Fatalf("parse valid artifact: %v", err)
	}
	if pack.Name != "Pack" || len(pack.Items) != 1 {
		t.Fatalf("pack = %+v", pack)
	}

	cases := []struct {
		name string
		data string
	}{
		{name: "empty", data: ""},
		{name: "unknown field", data: `{"schema_version":"dense-mem.skill_pack.v1","name":"Pack","extra":true,"items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`},
		{name: "multiple values", data: valid + ` {}`},
		{name: "bad schema", data: `{"schema_version":"wrong","name":"Pack","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`},
		{name: "missing name", data: `{"schema_version":"dense-mem.skill_pack.v1","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`},
		{name: "missing items", data: `{"schema_version":"dense-mem.skill_pack.v1","name":"Pack","items":[]}`},
		{name: "bad predicate", data: `{"schema_version":"dense-mem.skill_pack.v1","name":"Pack","items":[{"subject":"assistant","predicate":"bad","object":"testing","source_kind":"manual"}]}`},
		{name: "bad source", data: `{"schema_version":"dense-mem.skill_pack.v1","name":"Pack","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"bad"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseArtifactJSON([]byte(tc.data)); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("parse err = %v, want ErrInvalidArtifact", err)
			}
		})
	}

	if _, err := parseArtifactJSON([]byte(strings.Repeat("x", maxArtifactBytes+1))); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("oversized parse err = %v, want ErrInvalidArtifact", err)
	}
	if err := validateItem(SkillPackItem{Subject: strings.Repeat("x", 257), Predicate: "has_skill", Object: "x", SourceKind: SourceKindManual}); err == nil {
		t.Fatal("long subject should fail")
	}
	if err := validateItem(SkillPackItem{Subject: "assistant", Predicate: "has_skill", Object: strings.Repeat("x", 1025), SourceKind: SourceKindManual}); err == nil {
		t.Fatal("long object should fail")
	}
	if err := validateItem(SkillPackItem{Predicate: "has_skill", Object: "x", SourceKind: SourceKindManual}); err == nil {
		t.Fatal("blank subject should fail")
	}
	if err := validateItem(SkillPackItem{Subject: "assistant", Object: "x", SourceKind: SourceKindManual}); err == nil {
		t.Fatal("blank predicate should fail")
	}
	if err := validateItem(SkillPackItem{Subject: "assistant", Predicate: "has_skill", SourceKind: SourceKindManual}); err == nil {
		t.Fatal("blank object should fail")
	}
	longDescriptionPack := SkillPack{
		SchemaVersion: SchemaVersion,
		Name:          "Pack",
		Description:   strings.Repeat("x", 1025),
		Items: []SkillPackItem{{
			Subject:    "assistant",
			Predicate:  "has_skill",
			Object:     "testing",
			SourceKind: SourceKindManual,
		}},
	}
	if err := validatePack(longDescriptionPack); err == nil {
		t.Fatal("long description should fail")
	}
	longNamePack := longDescriptionPack
	longNamePack.Name = strings.Repeat("x", 257)
	longNamePack.Description = ""
	if err := validatePack(longNamePack); err == nil {
		t.Fatal("long name should fail")
	}
	tooManyItemsPack := longNamePack
	tooManyItemsPack.Name = "Pack"
	tooManyItemsPack.Items = make([]SkillPackItem, maxPackItems+1)
	for i := range tooManyItemsPack.Items {
		tooManyItemsPack.Items[i] = SkillPackItem{Subject: "assistant", Predicate: "has_skill", Object: "testing", SourceKind: SourceKindManual}
	}
	if err := validatePack(tooManyItemsPack); err == nil {
		t.Fatal("too many items should fail")
	}
	if err := validateExpectedHash("abc", "not-64"); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("invalid expected hash err = %v, want ErrInvalidArtifact", err)
	}
}

func TestFetchArtifactBranches(t *testing.T) {
	successClient := &fakeHTTPClient{status: http.StatusOK, body: `{"ok":true}`}
	data, err := fetchArtifact(context.Background(), successClient, "https://example.com/pack.json")
	if err != nil {
		t.Fatalf("fetch success: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("data = %s", data)
	}
	if successClient.lastAccept != "application/json" {
		t.Fatalf("Accept = %q, want application/json", successClient.lastAccept)
	}

	if _, err := fetchArtifact(context.Background(), &fakeHTTPClient{status: http.StatusNotFound, body: "missing"}, "https://example.com/pack.json"); err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("status err = %v, want status 404", err)
	}
	if _, err := fetchArtifact(context.Background(), &fakeHTTPClient{err: errors.New("network down")}, "https://example.com/pack.json"); err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("network err = %v, want network down", err)
	}
	if _, err := fetchArtifact(context.Background(), &fakeHTTPClient{status: http.StatusOK, body: strings.Repeat("x", maxArtifactBytes+1)}, "https://example.com/pack.json"); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("oversize err = %v, want ErrInvalidArtifact", err)
	}
	if err := validateArtifactURL("https:///missing-host"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("missing host err = %v, want ErrUnsafeURL", err)
	}
	if err := rejectUnsafeHost(context.Background(), "localhost"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("localhost err = %v, want ErrUnsafeURL", err)
	}
	if defaultHTTPClient() == nil {
		t.Fatal("defaultHTTPClient returned nil")
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

func TestFindCandidatesGraphFactFastPathAndReadError(t *testing.T) {
	graph := &factCandidateGraph{}
	svc := New(Dependencies{Graph: graph})
	res, err := svc.FindCandidates(context.Background(), "team-1", FindCandidatesRequest{
		Query: "skill pack",
		Limit: 1,
	})
	if err != nil {
		t.Fatalf("FindCandidates graph fact: %v", err)
	}
	if graph.reads != 1 {
		t.Fatalf("graph reads = %d, want fact fast path only", graph.reads)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Type != "fact" || res.Candidates[0].Item.SourceKind != SourceKindFact {
		t.Fatalf("candidates = %+v, want one graph fact candidate", res.Candidates)
	}

	readErrSvc := New(Dependencies{Graph: &recordingGraph{readErr: errors.New("graph read failed")}})
	if _, err := readErrSvc.FindCandidates(context.Background(), "team-1", FindCandidatesRequest{Query: "skill pack"}); err == nil || !strings.Contains(err.Error(), "graph read failed") {
		t.Fatalf("graph read err = %v, want graph read failed", err)
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

func TestReviewImportSkipsUnselectedItems(t *testing.T) {
	claimCreate := &fakeClaimCreate{}
	ledger := &fakeLedger{}
	svc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{},
		ClaimCreate:    claimCreate,
		Ledger:         ledger,
	})
	pack := &SkillPack{
		SchemaVersion: SchemaVersion,
		Name:          "Partial import",
		Items: []SkillPackItem{
			{
				Subject:    "assistant",
				Predicate:  "has_skill",
				Object:     "skips this item",
				SourceKind: SourceKindManual,
			},
			{
				Subject:    "assistant",
				Predicate:  "uses",
				Object:     "imports selected items",
				SourceKind: SourceKindManual,
			},
		},
	}

	res, err := svc.Import(context.Background(), "team-1", ImportRequest{
		Artifact:      pack,
		Mode:          ModeReview,
		SelectedItems: []int{1},
	})
	if err != nil {
		t.Fatalf("Import selected items: %v", err)
	}
	if res.AppliedCount != 1 || res.SkippedCount != 1 || res.Status != domain.SkillPackImportStatusApplied {
		t.Fatalf("result = %+v, want one applied and one skipped", res)
	}
	if len(res.Items) != 1 || res.Items[0].Index != 1 {
		t.Fatalf("items = %+v, want only selected index 1", res.Items)
	}
	if claimCreate.calls != 1 {
		t.Fatalf("claimCreate calls = %d, want 1", claimCreate.calls)
	}
	if len(ledger.changes) != 2 {
		t.Fatalf("ledger changes len = %d, want fragment+selected claim", len(ledger.changes))
	}
}

func TestImportAndRollbackDependencyErrorBranches(t *testing.T) {
	ctx := context.Background()
	pack := packWithItem(SourceKindManual)

	fragmentErrSvc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{err: errors.New("fragment failed")},
		ClaimCreate:    &fakeClaimCreate{},
		Ledger:         &fakeLedger{},
	})
	if _, err := fragmentErrSvc.Import(ctx, "team-1", ImportRequest{Artifact: pack, Mode: ModeReview}); err == nil || !strings.Contains(err.Error(), "fragment failed") {
		t.Fatalf("fragment err = %v, want fragment failed", err)
	}

	tagFragmentErrSvc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{},
		ClaimCreate:    &fakeClaimCreate{},
		Graph:          &recordingGraph{writeErr: errors.New("tag fragment failed")},
		Ledger:         &fakeLedger{},
	})
	if _, err := tagFragmentErrSvc.Import(ctx, "team-1", ImportRequest{Artifact: pack, Mode: ModeReview}); err == nil || !strings.Contains(err.Error(), "tag fragment failed") {
		t.Fatalf("tag fragment err = %v, want tag fragment failed", err)
	}

	updateErrSvc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{},
		ClaimCreate:    &fakeClaimCreate{},
		Ledger:         &fakeLedger{updateErr: errors.New("update failed")},
	})
	if _, err := updateErrSvc.Import(ctx, "team-1", ImportRequest{Artifact: pack, Mode: ModeReview}); err == nil || !strings.Contains(err.Error(), "update failed") {
		t.Fatalf("update err = %v, want update failed", err)
	}

	getErrSvc := New(Dependencies{Ledger: &fakeLedger{getErr: errors.New("get failed")}})
	if _, err := getErrSvc.Rollback(ctx, "team-1", RollbackRequest{ImportID: "import-1"}); err == nil || !strings.Contains(err.Error(), "get failed") {
		t.Fatalf("get err = %v, want get failed", err)
	}

	listErrSvc := New(Dependencies{Ledger: &fakeLedger{listErr: errors.New("list failed")}})
	if _, err := listErrSvc.Rollback(ctx, "team-1", RollbackRequest{ImportID: "import-1"}); err == nil || !strings.Contains(err.Error(), "list failed") {
		t.Fatalf("list err = %v, want list failed", err)
	}

	markErrSvc := New(Dependencies{Ledger: &fakeLedger{markErr: errors.New("mark failed")}, Graph: &recordingGraph{}})
	if _, err := markErrSvc.Rollback(ctx, "team-1", RollbackRequest{ImportID: "import-1"}); err == nil || !strings.Contains(err.Error(), "mark failed") {
		t.Fatalf("mark err = %v, want mark failed", err)
	}
}

func TestTrustedImportValidatesAndPromotes(t *testing.T) {
	t.Run("manual trusted validates claim", func(t *testing.T) {
		ledger := &fakeLedger{}
		graph := &recordingGraph{}
		svc := New(Dependencies{
			FragmentCreate: &fakeFragmentCreate{},
			ClaimCreate:    &fakeClaimCreate{},
			Graph:          graph,
			Ledger:         ledger,
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact: packWithItem(SourceKindManual),
			Mode:     ModeTrusted,
		})
		if err != nil {
			t.Fatalf("Import trusted manual: %v", err)
		}
		if res.Items[0].Status != "validated" || ledger.status != domain.SkillPackImportStatusApplied {
			t.Fatalf("res = %+v ledger status = %q", res, ledger.status)
		}
		if len(ledger.changes) != 2 {
			t.Fatalf("changes len = %d, want fragment+claim", len(ledger.changes))
		}
		if graph.writeCount < 2 {
			t.Fatalf("graph writes = %d, want tag fragment and trust claim", graph.writeCount)
		}
	})

	t.Run("source fact promotes fact", func(t *testing.T) {
		ledger := &fakeLedger{}
		graph := &recordingGraph{}
		svc := New(Dependencies{
			FragmentCreate: &fakeFragmentCreate{},
			ClaimCreate:    &fakeClaimCreate{},
			FactPromote:    fakeFactPromote{},
			Graph:          graph,
			Ledger:         ledger,
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact: packWithItem(SourceKindFact),
			Mode:     ModeTrusted,
		})
		if err != nil {
			t.Fatalf("Import trusted fact: %v", err)
		}
		if res.Items[0].Status != "promoted" || res.Items[0].FactID != "fact-1" {
			t.Fatalf("item = %+v, want promoted fact-1", res.Items[0])
		}
		if len(ledger.changes) != 3 {
			t.Fatalf("changes len = %d, want fragment+claim+fact", len(ledger.changes))
		}
	})

	t.Run("source fact requires promoter", func(t *testing.T) {
		svc := New(Dependencies{
			FragmentCreate: &fakeFragmentCreate{},
			ClaimCreate:    &fakeClaimCreate{},
			Graph:          &recordingGraph{},
			Ledger:         &fakeLedger{},
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact: packWithItem(SourceKindFact),
			Mode:     ModeTrusted,
		})
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if res.Items[0].Status != "error" || !strings.Contains(res.Items[0].Error, "fact promote service is required") {
			t.Fatalf("item = %+v, want fact promote service error", res.Items[0])
		}
	})
}

func TestTrustedImportConflictDecisions(t *testing.T) {
	conflictingFact := &domain.Fact{
		FactID:    "fact-local",
		Subject:   "assistant",
		Predicate: "has_skill",
		Object:    "old behavior",
		Status:    domain.FactStatusActive,
	}

	t.Run("skip selected conflict", func(t *testing.T) {
		ledger := &fakeLedger{}
		claimCreate := &fakeClaimCreate{}
		svc := New(Dependencies{
			FragmentCreate: &fakeFragmentCreate{},
			ClaimCreate:    claimCreate,
			FactList:       fakeFactList{facts: []*domain.Fact{conflictingFact}},
			Graph:          &recordingGraph{},
			Ledger:         ledger,
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact: packWithItem(SourceKindManual),
			Mode:     ModeTrusted,
			ConflictDecisions: []ConflictDecision{{
				Index:  0,
				Action: DecisionSkip,
			}},
		})
		if err != nil {
			t.Fatalf("Import skip: %v", err)
		}
		if res.Status != domain.SkillPackImportStatusNeedsReview || res.SkippedCount != 1 {
			t.Fatalf("result = %+v, want needs_review skipped", res)
		}
		if claimCreate.calls != 0 {
			t.Fatalf("claimCreate calls = %d, want 0", claimCreate.calls)
		}
	})

	t.Run("supersede local conflict", func(t *testing.T) {
		ledger := &fakeLedger{}
		graph := &recordingGraph{}
		svc := New(Dependencies{
			FragmentCreate: &fakeFragmentCreate{},
			ClaimCreate:    &fakeClaimCreate{},
			FactList:       fakeFactList{facts: []*domain.Fact{conflictingFact}},
			FactGet:        fakeFactGet{facts: map[string]*domain.Fact{"fact-local": conflictingFact}},
			Graph:          graph,
			Ledger:         ledger,
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact: packWithItem(SourceKindManual),
			Mode:     ModeTrusted,
			ConflictDecisions: []ConflictDecision{{
				Index:  0,
				Action: DecisionSupersedeLocal,
			}},
		})
		if err != nil {
			t.Fatalf("Import supersede: %v", err)
		}
		if res.Items[0].Status != "validated" {
			t.Fatalf("item = %+v, want validated", res.Items[0])
		}
		if len(ledger.changes) != 3 {
			t.Fatalf("changes len = %d, want fragment+fact+claim", len(ledger.changes))
		}
		if graph.txCount != 1 {
			t.Fatalf("txCount = %d, want supersede tx", graph.txCount)
		}
	})
}

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
	duplicateSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{duplicate: true, claimID: "claim-dup"},
		ClaimGet:    fakeClaimGet{claims: map[string]*domain.Claim{"claim-dup": existingClaim}},
		Graph:       &recordingGraph{},
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

	appendErrSvc := New(Dependencies{
		ClaimCreate: &fakeClaimCreate{},
		Ledger:      &fakeLedger{appendErr: errors.New("append failed")},
	}).(*service)
	appendErr := appendErrSvc.importItem(ctx, "team-1", "import-1", "hash", "fragment-1", ModeReview, item, inspected, "")
	if appendErr.Status != "error" || !strings.Contains(appendErr.Error, "append failed") {
		t.Fatalf("appendErr = %+v, want ledger append error", appendErr)
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

type fakeFactGet struct {
	facts map[string]*domain.Fact
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
	lastAccept string
}

func (f *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	f.lastAccept = req.Header.Get("Accept")
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
	}, nil
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
	states     map[string]map[string]any
	writeCount int
	writeErr   error
	txCount    int
	readErr    error
}

func (g *recordingGraph) ScopedRead(_ context.Context, _ string, _ string, params map[string]any) (neo4jdriver.ResultSummary, []map[string]any, error) {
	if g.readErr != nil {
		return nil, nil, g.readErr
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

func (g *recordingGraph) ScopedWrite(_ context.Context, _ string, _ string, _ map[string]any) (neo4jdriver.ResultSummary, error) {
	g.writeCount++
	return nil, g.writeErr
}

func (g *recordingGraph) ScopedWriteTx(_ context.Context, _ string, _ func(neo4jdriver.ManagedTransaction) error) error {
	g.txCount++
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

type fakeClaimList struct {
	claims []*domain.Claim
}

func (f fakeClaimList) List(_ context.Context, _ string, limit, offset int) ([]*domain.Claim, int, error) {
	return f.claims, len(f.claims), nil
}

type fakeFragmentCreate struct {
	calls int
	err   error
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
	imports   []domain.SkillPackImport
	changes   []domain.SkillPackImportChange
	status    string
	updateErr error
	markErr   error
	getErr    error
	listErr   error
	appendErr error
}

func (f *fakeLedger) CreateImport(_ context.Context, record domain.SkillPackImport) error {
	f.imports = append(f.imports, record)
	f.status = record.Status
	return nil
}
func (f *fakeLedger) UpdateImportStatus(_ context.Context, _, _, status string, _, _ int, _ map[string]any) error {
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
	if f.appendErr != nil {
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
