package skillpackservice

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestImportRebuildsSelectedSupportLineage(t *testing.T) {
	fragmentCreate := &fakeFragmentCreate{}
	claimCreate := &fakeClaimCreate{}
	ledger := &fakeLedger{}
	graph := &recordingGraph{}
	pack := &SkillPack{
		SchemaVersion: SchemaVersion,
		Name:          "Supported pack",
		Items: []SkillPackItem{{
			Subject:            "assistant",
			Predicate:          "has_skill",
			Object:             "writes useful blog outlines",
			SourceKind:         SourceKindValidatedClaim,
			SourceID:           "claim-source",
			SupportClaimIDs:    []string{"claim-source", "claim-source-2"},
			SupportFragmentIDs: []string{"fragment-source"},
		}},
		Support: &SkillPackSupport{
			Claims: []SkillPackSupportClaim{{
				ClaimID:     "claim-source",
				Subject:     "assistant",
				Predicate:   "has_skill",
				Object:      "writes useful blog outlines",
				SupportedBy: []string{"fragment-source"},
			}, {
				ClaimID:     "claim-source-2",
				Subject:     "assistant",
				Predicate:   "has_skill",
				Object:      "writes useful blog outlines",
				SupportedBy: []string{"fragment-source-2"},
			}},
			Fragments: []SkillPackSupportFragment{{
				FragmentID:    "fragment-source",
				Content:       "Good blog outlines define audience, thesis, supporting claims, and success checks.",
				Source:        "conversation",
				SourceType:    string(domain.SourceTypeConversation),
				Authority:     string(domain.AuthorityPrimary),
				SourceQuality: 0.93,
			}, {
				FragmentID:    "fragment-source-2",
				Content:       "Strong outlines turn each supporting claim into a section with an acceptance check.",
				Source:        "conversation",
				SourceType:    string(domain.SourceTypeConversation),
				Authority:     string(domain.AuthorityPrimary),
				SourceQuality: 0.93,
			}},
		},
	}
	svc := New(Dependencies{
		FragmentCreate: fragmentCreate,
		ClaimCreate:    claimCreate,
		Graph:          graph,
		Ledger:         ledger,
	})

	res, err := svc.Import(context.Background(), "team-1", ImportRequest{
		Artifact: pack,
		Mode:     ModeReview,
	})
	if err != nil {
		t.Fatalf("Import supported pack: %v", err)
	}
	if res.Status != domain.SkillPackImportStatusApplied || res.Items[0].Status != "imported" {
		t.Fatalf("result = %+v, want applied imported", res)
	}
	if fragmentCreate.calls != 3 {
		t.Fatalf("fragmentCreate calls = %d, want artifact fragment plus two support fragments", fragmentCreate.calls)
	}
	if claimCreate.calls != 1 || len(claimCreate.claims) != 1 {
		t.Fatalf("claimCreate calls/claims = %d/%d, want one claim", claimCreate.calls, len(claimCreate.claims))
	}
	createdClaim := claimCreate.claims[0]
	if len(createdClaim.SupportedBy) != 2 ||
		!slices.Contains(createdClaim.SupportedBy, "fragment-2") ||
		!slices.Contains(createdClaim.SupportedBy, "fragment-3") {
		t.Fatalf("created claim supported_by = %+v, want recreated support fragments", createdClaim.SupportedBy)
	}
	if createdClaim.Speaker != "skill_pack" || createdClaim.ExtractConf != 0.8 || !strings.HasPrefix(createdClaim.IdempotencyKey, "skill-pack:claim:") {
		t.Fatalf("created claim = %+v, want import defaults and support claim idempotency", createdClaim)
	}
	if len(ledger.changes) != 4 {
		t.Fatalf("ledger changes = %d, want artifact fragment + two support fragments + claim", len(ledger.changes))
	}
}

func TestSupportHelpersNormalizeAndDedupe(t *testing.T) {
	claimIDs := supportFragmentIDsForClaim(&domain.Claim{
		SupportedBy: []string{" fragment-a ", "fragment-a", ""},
		Evidence: []domain.Evidence{
			{FragmentID: "fragment-b"},
			{FragmentID: "fragment-b"},
			{FragmentID: ""},
		},
	})
	if !slices.Equal(claimIDs, []string{"fragment-a", "fragment-b"}) {
		t.Fatalf("claim support ids = %+v, want deduped claim and evidence fragments", claimIDs)
	}
	factIDs := supportFragmentIDsForFact(&domain.Fact{
		Evidence: []domain.Evidence{
			{FragmentID: "fragment-c"},
			{FragmentID: "fragment-c"},
			{FragmentID: ""},
		},
	})
	if !slices.Equal(factIDs, []string{"fragment-c"}) {
		t.Fatalf("fact support ids = %+v, want deduped evidence fragments", factIDs)
	}
	missing := missingSupportFragments([]string{"fragment-a", "fragment-b"}, map[string]SkillPackSupportFragment{
		"fragment-a": {FragmentID: "fragment-a"},
	})
	if !slices.Equal(missing, []string{"fragment-b"}) {
		t.Fatalf("missing fragments = %+v, want fragment-b", missing)
	}
	labels := appendImportLabel([]string{" a ", "a", "", "b"})
	if !slices.Equal(labels, []string{"a", "b", "skill_pack_import"}) {
		t.Fatalf("labels = %+v, want trimmed dedupe plus import label", labels)
	}
	fullLabels := []string{}
	for i := 0; i < 20; i++ {
		fullLabels = append(fullLabels, "label"+string(rune('a'+i)))
	}
	if got := appendImportLabel(fullLabels); len(got) != 20 || slices.Contains(got, "skill_pack_import") {
		t.Fatalf("full labels = %+v, want no extra import label", got)
	}
}

func TestGraphRowStateHelpersDecodeTypes(t *testing.T) {
	row := map[string]any{
		"strings":       []string{"a", "b"},
		"mixed":         []any{"c", 12, "d", ""},
		"float32":       float32(1.25),
		"int":           int(2),
		"int64":         int64(3),
		"metadata_json": `{"topic":"testing"}`,
		"bad_json":      "{",
	}
	if !slices.Equal(stringSliceState(row, "strings"), []string{"a", "b"}) {
		t.Fatalf("stringSliceState strings = %+v", stringSliceState(row, "strings"))
	}
	if !slices.Equal(stringSliceState(row, "mixed"), []string{"c", "d"}) {
		t.Fatalf("stringSliceState mixed = %+v", stringSliceState(row, "mixed"))
	}
	if stringSliceState(row, "missing") != nil || stringSliceState(map[string]any{"x": 1}, "x") != nil {
		t.Fatal("stringSliceState should return nil for missing or unsupported values")
	}
	if floatState(row, "float32") != 1.25 || floatState(row, "int") != 2 || floatState(row, "int64") != 3 || floatState(row, "missing") != 0 {
		t.Fatalf("floatState decoded unexpected values")
	}
	decoded := optionalMapState(row, "bad_json", "metadata_json")
	if decoded["topic"] != "testing" {
		t.Fatalf("optionalMapState = %+v, want decoded metadata_json", decoded)
	}
	if optionalMapState(row, "bad_json", "missing") != nil {
		t.Fatal("optionalMapState should return nil when no map value decodes")
	}
}

func TestSupportValidationRejectsMalformedFields(t *testing.T) {
	claimCases := []struct {
		name  string
		claim SkillPackSupportClaim
		want  string
	}{
		{name: "missing claim id", claim: SkillPackSupportClaim{Subject: "assistant", Predicate: "has_skill", Object: "testing"}, want: "claim_id"},
		{name: "long claim id", claim: SkillPackSupportClaim{ClaimID: strings.Repeat("c", 129), Subject: "assistant", Predicate: "has_skill", Object: "testing"}, want: "claim_id exceeds"},
		{name: "bad triple", claim: SkillPackSupportClaim{ClaimID: "claim-1", Predicate: "has_skill", Object: "testing"}, want: "subject"},
		{name: "bad support id", claim: SkillPackSupportClaim{ClaimID: "claim-1", Subject: "assistant", Predicate: "has_skill", Object: "testing", SupportedBy: []string{""}}, want: "supported_by"},
	}
	for _, tc := range claimCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSupportClaim(tc.claim); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateSupportClaim err = %v, want %q", err, tc.want)
			}
		})
	}

	fragmentCases := []struct {
		name     string
		fragment SkillPackSupportFragment
		want     string
	}{
		{name: "missing id", fragment: SkillPackSupportFragment{Content: "content"}, want: "fragment_id"},
		{name: "long id", fragment: SkillPackSupportFragment{FragmentID: strings.Repeat("f", 129), Content: "content"}, want: "fragment_id exceeds"},
		{name: "missing content", fragment: SkillPackSupportFragment{FragmentID: "fragment-1"}, want: "content"},
		{name: "long content", fragment: SkillPackSupportFragment{FragmentID: "fragment-1", Content: strings.Repeat("x", 8193)}, want: "content exceeds"},
		{name: "long source", fragment: SkillPackSupportFragment{FragmentID: "fragment-1", Content: "content", Source: strings.Repeat("s", 257)}, want: "source exceeds"},
		{name: "bad source type", fragment: SkillPackSupportFragment{FragmentID: "fragment-1", Content: "content", SourceType: "bad"}, want: "source_type"},
		{name: "bad authority", fragment: SkillPackSupportFragment{FragmentID: "fragment-1", Content: "content", Authority: "bad"}, want: "authority"},
		{name: "too many labels", fragment: SkillPackSupportFragment{FragmentID: "fragment-1", Content: "content", Labels: make([]string, 21)}, want: "labels exceeds"},
		{name: "empty label", fragment: SkillPackSupportFragment{FragmentID: "fragment-1", Content: "content", Labels: []string{""}}, want: "labels[0]"},
		{name: "long label", fragment: SkillPackSupportFragment{FragmentID: "fragment-1", Content: "content", Labels: []string{strings.Repeat("l", 65)}}, want: "labels[0] exceeds"},
		{name: "bad quality", fragment: SkillPackSupportFragment{FragmentID: "fragment-1", Content: "content", SourceQuality: 2}, want: "source_quality"},
	}
	for _, tc := range fragmentCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSupportFragment(tc.fragment); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateSupportFragment err = %v, want %q", err, tc.want)
			}
		})
	}
}
