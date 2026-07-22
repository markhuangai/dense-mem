package skillpackservice

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestParseLegacyArtifactConvertsToV2MemoryPack(t *testing.T) {
	pack := validLegacySkillPack()
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal legacy pack: %v", err)
	}
	parsed, err := parseArtifactJSON(data)
	if err != nil {
		t.Fatalf("parseArtifactJSON: %v", err)
	}
	if parsed.Name != pack.Name || len(parsed.Items) != 1 {
		t.Fatalf("parsed legacy pack = %+v", parsed)
	}

	artifact, legacy, err := parseV2MemoryPackArtifactJSON(data)
	if err != nil {
		t.Fatalf("parseV2MemoryPackArtifactJSON legacy: %v", err)
	}
	if !legacy {
		t.Fatal("legacy artifact was not marked legacy")
	}
	if artifact.Format != MemoryPackFormat || artifact.LegacySchemaVersion != LegacySchemaVersion || artifact.Name != pack.Name {
		t.Fatalf("converted artifact = %+v", artifact)
	}
	relationship := artifact.Relationships[0]
	if relationship.ItemID != "legacy-rel-1" ||
		relationship.PredicateKey != "uses" ||
		relationship.Status != string(domain.V2RelationshipStatusNeedsReview) ||
		relationship.Subject.DisplayName != "Dense-Mem" ||
		relationship.Object.DisplayName != "PostgreSQL" ||
		len(relationship.SupportFragmentIDs) != 1 {
		t.Fatalf("converted relationship = %+v", relationship)
	}
	if len(artifact.EvidenceFragments) != 1 || artifact.EvidenceFragments[0].FragmentID != "fragment-1" {
		t.Fatalf("converted fragments = %+v", artifact.EvidenceFragments)
	}

	if err := validateExpectedHash("abc", ""); err != nil {
		t.Fatalf("empty expected hash rejected: %v", err)
	}
	if err := validateExpectedHash(strings.Repeat("a", 64), strings.Repeat("b", 64)); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("hash mismatch err = %v", err)
	}
}

func TestParseLegacyArtifactRejectsMalformedInput(t *testing.T) {
	if _, err := parseArtifactJSON(nil); !errors.Is(err, ErrInvalidArtifact) || !strings.Contains(err.Error(), "empty artifact") {
		t.Fatalf("empty artifact err = %v", err)
	}
	if _, err := parseArtifactJSON([]byte(strings.Repeat("x", maxArtifactBytes+1))); !errors.Is(err, ErrInvalidArtifact) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized artifact err = %v", err)
	}
	if _, err := parseArtifactJSON([]byte(`{"schema_version":"dense-mem.skill_pack.v1","name":"bad","items":[],"extra":true}`)); !errors.Is(err, ErrInvalidArtifact) || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field err = %v", err)
	}

	validData, err := json.Marshal(validLegacySkillPack())
	if err != nil {
		t.Fatalf("marshal valid pack: %v", err)
	}
	if _, err := parseArtifactJSON(append(validData, []byte(` {}`)...)); !errors.Is(err, ErrInvalidArtifact) || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("multiple JSON values err = %v", err)
	}

	for _, tc := range []struct {
		name    string
		mutate  func(*SkillPack)
		wantErr string
	}{
		{
			name:    "bad schema",
			mutate:  func(pack *SkillPack) { pack.SchemaVersion = "wrong" },
			wantErr: "schema_version",
		},
		{
			name:    "missing item",
			mutate:  func(pack *SkillPack) { pack.Items = nil },
			wantErr: "items is required",
		},
		{
			name:    "unsupported predicate",
			mutate:  func(pack *SkillPack) { pack.Items[0].Predicate = "unknown" },
			wantErr: "predicate",
		},
		{
			name:    "unsupported source kind",
			mutate:  func(pack *SkillPack) { pack.Items[0].SourceKind = "claim" },
			wantErr: "source_kind",
		},
		{
			name: "duplicate support claim id",
			mutate: func(pack *SkillPack) {
				pack.Support.Claims = append(pack.Support.Claims, pack.Support.Claims[0])
			},
			wantErr: "duplicate claim_id",
		},
		{
			name: "duplicate support fragment id",
			mutate: func(pack *SkillPack) {
				pack.Support.Fragments = append(pack.Support.Fragments, pack.Support.Fragments[0])
			},
			wantErr: "duplicate fragment_id",
		},
		{
			name:    "claim references missing fragment",
			mutate:  func(pack *SkillPack) { pack.Support.Claims[0].SupportedBy = []string{"missing-fragment"} },
			wantErr: "supported_by references missing fragment",
		},
		{
			name:    "item references missing claim",
			mutate:  func(pack *SkillPack) { pack.Items[0].SupportClaimIDs = []string{"missing-claim"} },
			wantErr: "support_claim_ids references missing claim",
		},
		{
			name:    "item support claim triple mismatch",
			mutate:  func(pack *SkillPack) { pack.Support.Claims[0].Object = "Legacy graph" },
			wantErr: "does not match item triple",
		},
		{
			name:    "item references missing fragment",
			mutate:  func(pack *SkillPack) { pack.Items[0].SupportFragmentIDs = []string{"missing-fragment"} },
			wantErr: "support_fragment_ids references missing fragment",
		},
		{
			name:    "duplicate id list value",
			mutate:  func(pack *SkillPack) { pack.Items[0].SupportFragmentIDs = []string{"fragment-1", "fragment-1"} },
			wantErr: "duplicates",
		},
		{
			name:    "invalid support source type",
			mutate:  func(pack *SkillPack) { pack.Support.Fragments[0].SourceType = "webpage" },
			wantErr: "source_type",
		},
		{
			name: "invalid source quality",
			mutate: func(pack *SkillPack) {
				bad := 1.1
				pack.Support.Fragments[0].SourceQuality = &bad
			},
			wantErr: "source_quality",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pack := validLegacySkillPack()
			tc.mutate(&pack)
			data, err := json.Marshal(pack)
			if err != nil {
				t.Fatalf("marshal pack: %v", err)
			}
			_, err = parseArtifactJSON(data)
			if !errors.Is(err, ErrInvalidArtifact) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("parseArtifactJSON err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func validLegacySkillPack() SkillPack {
	quality := 0.95
	return SkillPack{
		SchemaVersion: LegacySchemaVersion,
		Name:          "PostgreSQL Runtime",
		Description:   "Legacy compatibility artifact.",
		Items: []SkillPackItem{{
			Subject:            "Dense-Mem",
			Predicate:          "uses",
			Object:             "PostgreSQL",
			SourceKind:         SourceKindManual,
			SourceID:           "legacy-rel-1",
			SupportClaimIDs:    []string{"claim-1"},
			SupportFragmentIDs: []string{"fragment-1"},
		}},
		Support: &SkillPackSupport{
			Claims: []SkillPackSupportClaim{{
				ClaimID:     "claim-1",
				Subject:     "Dense-Mem",
				Predicate:   "uses",
				Object:      "PostgreSQL",
				SupportedBy: []string{"fragment-1"},
			}},
			Fragments: []SkillPackSupportFragment{{
				FragmentID:    "fragment-1",
				Content:       "Dense-Mem uses PostgreSQL.",
				Source:        "architecture",
				SourceType:    string(domain.SourceTypeDocument),
				Authority:     string(domain.AuthorityPrimary),
				Labels:        []string{"v2"},
				SourceQuality: &quality,
			}},
		},
	}
}
