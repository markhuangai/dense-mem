package skillpackservice

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

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

func TestFindCandidatesListErrorBranches(t *testing.T) {
	factErrSvc := New(Dependencies{FactList: fakeFactList{err: errors.New("fact list failed")}})
	if _, err := factErrSvc.FindCandidates(context.Background(), "team-1", FindCandidatesRequest{Query: "skill pack"}); err == nil || !strings.Contains(err.Error(), "fact list failed") {
		t.Fatalf("fact list err = %v, want fact list failed", err)
	}

	claimErrSvc := New(Dependencies{
		FactList:  fakeFactList{},
		ClaimList: fakeClaimList{err: errors.New("claim list failed")},
	})
	if _, err := claimErrSvc.FindCandidates(context.Background(), "team-1", FindCandidatesRequest{Query: "skill pack"}); err == nil || !strings.Contains(err.Error(), "claim list failed") {
		t.Fatalf("claim list err = %v, want claim list failed", err)
	}

	empty, err := newGraphOps(nil).findCandidates(context.Background(), "team-1", "   ", 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty graph candidates = %+v err=%v, want none", empty, err)
	}
}

func TestExportAndInspectDependencyErrorBranches(t *testing.T) {
	factGetSvc := New(Dependencies{FactGet: fakeFactGet{err: errors.New("fact get failed")}})
	if _, err := factGetSvc.Export(context.Background(), "team-1", ExportRequest{Name: "Pack", FactIDs: []string{"fact-1"}}); err == nil || !strings.Contains(err.Error(), "fact get failed") {
		t.Fatalf("fact get err = %v, want fact get failed", err)
	}

	claimGetSvc := New(Dependencies{ClaimGet: fakeClaimGet{err: errors.New("claim get failed")}})
	if _, err := claimGetSvc.Export(context.Background(), "team-1", ExportRequest{Name: "Pack", ClaimIDs: []string{"claim-1"}}); err == nil || !strings.Contains(err.Error(), "claim get failed") {
		t.Fatalf("claim get err = %v, want claim get failed", err)
	}

	item := SkillPackItem{Subject: "assistant", Predicate: "has_skill", Object: "testing", SourceKind: SourceKindManual}
	factListSvc := New(Dependencies{FactList: fakeFactList{err: errors.New("fact inspect failed")}}).(*service)
	if _, err := factListSvc.inspectItem(context.Background(), "team-1", 0, item); err == nil || !strings.Contains(err.Error(), "fact inspect failed") {
		t.Fatalf("fact inspect err = %v, want fact inspect failed", err)
	}

	claimListSvc := New(Dependencies{ClaimList: fakeClaimList{err: errors.New("claim inspect failed")}}).(*service)
	if _, err := claimListSvc.inspectItem(context.Background(), "team-1", 0, item); err == nil || !strings.Contains(err.Error(), "claim inspect failed") {
		t.Fatalf("claim inspect err = %v, want claim inspect failed", err)
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
	if _, err := fetchArtifact(context.Background(), &fakeHTTPClient{status: http.StatusOK, readErr: errors.New("read failed")}, "https://example.com/pack.json"); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("read err = %v, want read failed", err)
	}
	if err := validateArtifactURL("://bad-url"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("bad URL err = %v, want ErrUnsafeURL", err)
	}
	if err := validateArtifactURL("https:///missing-host"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("missing host err = %v, want ErrUnsafeURL", err)
	}
	if err := rejectUnsafeHost(context.Background(), "localhost"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("localhost err = %v, want ErrUnsafeURL", err)
	}
	client := defaultHTTPClient()
	if client == nil {
		t.Fatal("defaultHTTPClient returned nil")
	}
	redirectReq, _ := http.NewRequest(http.MethodGet, "https://example.com/next.json", nil)
	if err := client.CheckRedirect(redirectReq, []*http.Request{&http.Request{}, &http.Request{}, &http.Request{}}); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("redirect count err = %v, want too many redirects", err)
	}
	unsafeRedirect, _ := http.NewRequest(http.MethodGet, "http://example.com/next.json", nil)
	if err := client.CheckRedirect(unsafeRedirect, nil); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("unsafe redirect err = %v, want ErrUnsafeURL", err)
	}
	transport := client.Transport.(*http.Transport)
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("unsafe dial err = %v, want ErrUnsafeURL", err)
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "missing-port"); err == nil {
		t.Fatal("bad dial address should fail")
	}
}

func TestSafeDialAddressUsesResolvedSafeIP(t *testing.T) {
	dialAddress, err := safeDialAddressFromResolved("tcp", "443", []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}})
	if err != nil {
		t.Fatalf("safe dial address: %v", err)
	}
	if dialAddress != "203.0.113.10:443" {
		t.Fatalf("dial address = %q, want vetted IP address", dialAddress)
	}
	if _, err := safeDialAddressFromResolved("tcp", "443", []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("unsafe resolved IP err = %v, want ErrUnsafeURL", err)
	}
	if _, err := safeDialAddressFromResolved("tcp4", "443", []net.IPAddr{{IP: net.ParseIP("2001:db8::1")}}); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("network mismatch err = %v, want ErrUnsafeURL", err)
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
	if !isUnsafeIP(nil) {
		t.Fatal("nil IP should be unsafe")
	}
	if !isUnsafeIP(net.ParseIP("224.0.0.1")) {
		t.Fatal("multicast address should be unsafe")
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

func TestInspectReportsSupersededMatchesAndRecommendations(t *testing.T) {
	item := packWithItem(SourceKindFact).Items[0]
	decider := &fakeConflictDecider{result: ConflictDecisionResult{
		Action:     DecisionSkip,
		Confidence: 0.92,
		Rationale:  "matches superseded local knowledge",
		Model:      "test-decider",
	}}
	svc := New(Dependencies{
		FactList: fakeFactList{facts: []*domain.Fact{{
			FactID:    "fact-old",
			Subject:   item.Subject,
			Predicate: item.Predicate,
			Object:    item.Object,
			Status:    domain.FactStatusSuperseded,
		}}},
		ConflictDecider: decider,
	})

	res, err := svc.Inspect(context.Background(), "team-1", InspectRequest{
		Artifact:           packWithItem(SourceKindFact),
		RecommendDecisions: true,
	})
	if err != nil {
		t.Fatalf("Inspect superseded: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Status != "matches_superseded_fact" || len(res.Items[0].SupersededMatches) != 1 {
		t.Fatalf("items = %+v, want superseded match", res.Items)
	}
	if len(res.DecisionsRequired) != 1 || res.DecisionsRequired[0].Recommendation == nil || res.DecisionsRequired[0].Recommendation.Action != DecisionSkip {
		t.Fatalf("decisions = %+v, want skip recommendation", res.DecisionsRequired)
	}
	if decider.calls != 1 || len(decider.requests[0].Inspection.SupersededMatches) != 1 {
		t.Fatalf("decider calls/requests = %d/%+v, want superseded evidence", decider.calls, decider.requests)
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

func TestImportSkipsDuplicateInspectionResultsBeforeMutation(t *testing.T) {
	item := packWithItem(SourceKindFact).Items[0]
	claimCreate := &fakeClaimCreate{}
	svc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{},
		ClaimCreate:    claimCreate,
		FactList: fakeFactList{facts: []*domain.Fact{{
			FactID:    "fact-existing",
			Subject:   item.Subject,
			Predicate: item.Predicate,
			Object:    item.Object,
			Status:    domain.FactStatusActive,
		}}},
		Ledger: &fakeLedger{},
	})

	res, err := svc.Import(context.Background(), "team-1", ImportRequest{
		Artifact: packWithItem(SourceKindFact),
		Mode:     ModeTrusted,
	})
	if err != nil {
		t.Fatalf("Import duplicate fact: %v", err)
	}
	if res.AppliedCount != 0 || res.SkippedCount != 1 || res.Items[0].Status != "skipped" {
		t.Fatalf("result = %+v, want duplicate item skipped", res)
	}
	if claimCreate.calls != 0 {
		t.Fatalf("claimCreate calls = %d, want 0", claimCreate.calls)
	}
}

func TestReviewImportSkipsDuplicateFragmentMutationAndLedger(t *testing.T) {
	graph := &recordingGraph{}
	ledger := &fakeLedger{}
	svc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{duplicate: true},
		ClaimCreate:    &fakeClaimCreate{},
		Graph:          graph,
		Ledger:         ledger,
	})

	res, err := svc.Import(context.Background(), "team-1", ImportRequest{
		Artifact:      packWithItem(SourceKindManual),
		Mode:          ModeReview,
		SelectedItems: []int{-1},
	})
	if err != nil {
		t.Fatalf("Import duplicate fragment: %v", err)
	}
	if res.SkippedCount != 1 || res.Status != domain.SkillPackImportStatusNeedsReview {
		t.Fatalf("result = %+v, want skipped duplicate-fragment-only import", res)
	}
	if graph.writeCount != 0 {
		t.Fatalf("graph writes = %d, want no duplicate fragment tag", graph.writeCount)
	}
	if len(ledger.changes) != 0 {
		t.Fatalf("ledger changes len = %d, want no created fragment change", len(ledger.changes))
	}
}

func TestImportReturnsRecoverableResultWhenItemLedgerAppendFails(t *testing.T) {
	graph := &recordingGraph{}
	ledger := &fakeLedger{appendErr: errors.New("append failed"), appendErrAfter: 2}
	svc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{},
		ClaimCreate:    &fakeClaimCreate{},
		Graph:          graph,
		Ledger:         ledger,
	})

	res, err := svc.Import(context.Background(), "team-1", ImportRequest{
		Artifact: packWithItem(SourceKindManual),
		Mode:     ModeReview,
	})
	if err != nil {
		t.Fatalf("Import err = %v, want recoverable result", err)
	}
	if res == nil || res.ImportID == "" || res.Status != domain.SkillPackImportStatusFailed || !strings.Contains(res.Error, "item 0: append failed") {
		t.Fatalf("result = %+v, want failed status with rollback id", res)
	}
	if ledger.updateCalls != 1 || ledger.status != domain.SkillPackImportStatusFailed {
		t.Fatalf("ledger update calls/status = %d/%q, want one failed update", ledger.updateCalls, ledger.status)
	}
	if !graphWriteContains(graph, "DETACH DELETE c") {
		t.Fatalf("graph writes = %q, want created claim cleanup before abort", graph.writeQueries)
	}
}

func TestImportAndRollbackDependencyErrorBranches(t *testing.T) {
	ctx := context.Background()
	pack := packWithItem(SourceKindManual)

	loadErrSvc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{},
		ClaimCreate:    &fakeClaimCreate{},
		Ledger:         &fakeLedger{},
	})
	if _, err := loadErrSvc.Import(ctx, "team-1", ImportRequest{Mode: ModeReview}); err == nil || !strings.Contains(err.Error(), "artifact_json") {
		t.Fatalf("load err = %v, want missing artifact input", err)
	}

	createErrSvc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{},
		ClaimCreate:    &fakeClaimCreate{},
		Ledger:         &fakeLedger{createErr: errors.New("create import failed")},
	})
	if _, err := createErrSvc.Import(ctx, "team-1", ImportRequest{Artifact: pack, Mode: ModeReview}); err == nil || !strings.Contains(err.Error(), "create import failed") {
		t.Fatalf("create import err = %v, want create import failed", err)
	}

	fragmentErrSvc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{err: errors.New("fragment failed")},
		ClaimCreate:    &fakeClaimCreate{},
		Ledger:         &fakeLedger{},
	})
	fragmentErrRes, err := fragmentErrSvc.Import(ctx, "team-1", ImportRequest{Artifact: pack, Mode: ModeReview})
	if err != nil {
		t.Fatalf("fragment err = %v, want recoverable result", err)
	}
	if fragmentErrRes == nil || fragmentErrRes.ImportID == "" || fragmentErrRes.Status != domain.SkillPackImportStatusFailed || !strings.Contains(fragmentErrRes.Error, "fragment failed") {
		t.Fatalf("fragment result = %+v, want failed result with import id", fragmentErrRes)
	}

	tagFragmentErrSvc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{},
		ClaimCreate:    &fakeClaimCreate{},
		Graph:          &recordingGraph{writeErr: errors.New("tag fragment failed")},
		Ledger:         &fakeLedger{},
	})
	tagFragmentErrRes, err := tagFragmentErrSvc.Import(ctx, "team-1", ImportRequest{Artifact: pack, Mode: ModeReview})
	if err != nil {
		t.Fatalf("tag fragment err = %v, want recoverable result", err)
	}
	if tagFragmentErrRes == nil || tagFragmentErrRes.ImportID == "" || tagFragmentErrRes.Status != domain.SkillPackImportStatusFailed || !strings.Contains(tagFragmentErrRes.Error, "tag fragment failed") {
		t.Fatalf("tag fragment result = %+v, want failed result with import id", tagFragmentErrRes)
	}

	updateErrSvc := New(Dependencies{
		FragmentCreate: &fakeFragmentCreate{},
		ClaimCreate:    &fakeClaimCreate{},
		Ledger:         &fakeLedger{updateErr: errors.New("update failed")},
	})
	updateErrRes, err := updateErrSvc.Import(ctx, "team-1", ImportRequest{Artifact: pack, Mode: ModeReview})
	if err != nil {
		t.Fatalf("update err = %v, want recoverable result", err)
	}
	if updateErrRes == nil || updateErrRes.ImportID == "" || updateErrRes.Status != "status_update_failed" || !strings.Contains(updateErrRes.Error, "update failed") {
		t.Fatalf("update result = %+v, want rollback id and status update error", updateErrRes)
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
		ledger := &fakeLedger{}
		svc := New(Dependencies{
			FragmentCreate: &fakeFragmentCreate{},
			ClaimCreate:    &fakeClaimCreate{},
			Graph:          &recordingGraph{},
			Ledger:         ledger,
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact: packWithItem(SourceKindFact),
			Mode:     ModeTrusted,
		})
		if err != nil {
			t.Fatalf("Import err = %v, want recoverable result", err)
		}
		if res == nil || res.ImportID == "" || res.Status != domain.SkillPackImportStatusFailed || !strings.Contains(res.Error, "fact promote service is required") {
			t.Fatalf("result = %+v, want failed status with rollback id", res)
		}
		if ledger.updateCalls != 1 || ledger.status != domain.SkillPackImportStatusFailed {
			t.Fatalf("ledger update calls/status = %d/%q, want one failed update", ledger.updateCalls, ledger.status)
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

	t.Run("demote source fact to claim", func(t *testing.T) {
		ledger := &fakeLedger{}
		svc := New(Dependencies{
			FragmentCreate: &fakeFragmentCreate{},
			ClaimCreate:    &fakeClaimCreate{},
			FactList:       fakeFactList{facts: []*domain.Fact{conflictingFact}},
			FactPromote:    fakeFactPromote{err: errors.New("promote should not be called")},
			Graph:          &recordingGraph{},
			Ledger:         ledger,
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact: packWithItem(SourceKindFact),
			Mode:     ModeTrusted,
			ConflictDecisions: []ConflictDecision{{
				Index:  0,
				Action: DecisionDemoteToClaim,
			}},
		})
		if err != nil {
			t.Fatalf("Import demote: %v", err)
		}
		if res.Items[0].Status != "validated" || res.Items[0].FactID != "" || res.Items[0].ClaimID == "" {
			t.Fatalf("item = %+v, want validated claim without fact", res.Items[0])
		}
		if len(ledger.changes) != 2 {
			t.Fatalf("changes len = %d, want fragment+claim", len(ledger.changes))
		}
	})

	t.Run("demote invalid for manual conflict", func(t *testing.T) {
		claimCreate := &fakeClaimCreate{}
		svc := New(Dependencies{
			FragmentCreate: &fakeFragmentCreate{},
			ClaimCreate:    claimCreate,
			FactList:       fakeFactList{facts: []*domain.Fact{conflictingFact}},
			Ledger:         &fakeLedger{},
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact: packWithItem(SourceKindManual),
			Mode:     ModeTrusted,
			ConflictDecisions: []ConflictDecision{{
				Index:  0,
				Action: DecisionDemoteToClaim,
			}},
		})
		if err != nil {
			t.Fatalf("Import invalid demote: %v", err)
		}
		if res.Status != domain.SkillPackImportStatusNeedsReview || len(res.DecisionsRequired) != 1 || res.DecisionsRequired[0].Reason != "invalid conflict decision" {
			t.Fatalf("result = %+v, want invalid decision review", res)
		}
		if claimCreate.calls != 0 {
			t.Fatalf("claimCreate calls = %d, want no writes", claimCreate.calls)
		}
	})

	t.Run("auto decide applies high confidence decision", func(t *testing.T) {
		decider := &fakeConflictDecider{result: ConflictDecisionResult{
			Action:     DecisionDemoteToClaim,
			Confidence: 0.95,
			Rationale:  "preserve remote fact as claim",
			Model:      "test-decider",
		}}
		svc := New(Dependencies{
			FragmentCreate:  &fakeFragmentCreate{},
			ClaimCreate:     &fakeClaimCreate{},
			FactList:        fakeFactList{facts: []*domain.Fact{conflictingFact}},
			FactPromote:     fakeFactPromote{err: errors.New("promote should not be called")},
			ConflictDecider: decider,
			Graph:           &recordingGraph{},
			Ledger:          &fakeLedger{},
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact:            packWithItem(SourceKindFact),
			Mode:                ModeTrusted,
			AutoDecideConflicts: true,
		})
		if err != nil {
			t.Fatalf("Import auto decide: %v", err)
		}
		if decider.calls != 1 {
			t.Fatalf("decider calls = %d, want 1", decider.calls)
		}
		if res.Status != domain.SkillPackImportStatusApplied || res.Items[0].Decision != DecisionDemoteToClaim || res.Items[0].Status != "validated" {
			t.Fatalf("result = %+v, want demoted applied import", res)
		}
	})

	t.Run("auto decide falls back on low confidence", func(t *testing.T) {
		claimCreate := &fakeClaimCreate{}
		decider := &fakeConflictDecider{result: ConflictDecisionResult{
			Action:     DecisionSupersedeLocal,
			Confidence: 0.40,
			Rationale:  "uncertain",
		}}
		svc := New(Dependencies{
			FragmentCreate:  &fakeFragmentCreate{},
			ClaimCreate:     claimCreate,
			FactList:        fakeFactList{facts: []*domain.Fact{conflictingFact}},
			ConflictDecider: decider,
			Ledger:          &fakeLedger{},
		})
		res, err := svc.Import(context.Background(), "team-1", ImportRequest{
			Artifact:            packWithItem(SourceKindManual),
			Mode:                ModeTrusted,
			AutoDecideConflicts: true,
		})
		if err != nil {
			t.Fatalf("Import low confidence auto decide: %v", err)
		}
		if res.Status != domain.SkillPackImportStatusNeedsReview || len(res.DecisionsRequired) != 1 {
			t.Fatalf("result = %+v, want needs_review", res)
		}
		if res.DecisionsRequired[0].Recommendation == nil || !strings.Contains(res.DecisionsRequired[0].Recommendation.Error, "below auto-decision threshold") {
			t.Fatalf("recommendation = %+v, want threshold error", res.DecisionsRequired[0].Recommendation)
		}
		if claimCreate.calls != 0 {
			t.Fatalf("claimCreate calls = %d, want no writes", claimCreate.calls)
		}
	})
}
