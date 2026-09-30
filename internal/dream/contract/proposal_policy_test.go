package contract

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

const (
	policyTeamID     = "00000000-0000-0000-0000-000000000001"
	policySubjectID  = "00000000-0000-0000-0000-000000000002"
	policyObjectID   = "00000000-0000-0000-0000-000000000003"
	policyRunID      = "00000000-0000-0000-0000-000000000004"
	policyEvidenceID = "00000000-0000-0000-0000-000000000005"
	policySourceA    = "00000000-0000-0000-0000-000000000006"
	policySourceB    = "00000000-0000-0000-0000-000000000007"
)

func validPolicyEvidenceProposal() UpsertHypothesisInput {
	return NormalizeUpsertHypothesisInput(UpsertHypothesisInput{
		TeamID: policyTeamID, RunID: policyRunID, Lane: domain.DreamLaneEvidenceDiscovery,
		Statement: "A may use B", SubjectEntityID: policySubjectID,
		PredicateKey: "uses", ObjectEntityID: policyObjectID, ContentHash: "sha256:test",
		SourceEvidenceIDs: []string{policyEvidenceID},
		EvidenceDerivations: []EvidenceDerivationSource{{
			EvidenceID: policyEvidenceID, FragmentID: policyEvidenceID,
			SourceGroupKey: "ingest:test", SpanStart: 0, SpanEnd: 1,
			Quote: "A", Authority: "primary",
		}},
	})
}

func validPolicyGraphProposal() UpsertHypothesisInput {
	input := validPolicyEvidenceProposal()
	input.Lane = domain.DreamLaneGraph
	input.SourceEvidenceIDs = nil
	input.EvidenceDerivations = nil
	input.SourceVersions = map[string]int{policySourceA: 1, policySourceB: 2}
	input.Derivations = []DreamDerivationSource{
		{PremisePosition: 1, RelationshipID: policySourceA, RelationshipVersion: 1,
			SupportID: policyEvidenceID, FragmentID: policyEvidenceID, SourceGroupKey: "ingest:test",
			SpanStart: 0, SpanEnd: 1, Quote: "A", Authority: "primary"},
		{PremisePosition: 2, RelationshipID: policySourceB, RelationshipVersion: 2,
			ObservationID: policyEvidenceID, FragmentID: policyEvidenceID, SourceGroupKey: "ingest:test",
			SpanStart: 0, SpanEnd: 1, Quote: "B", Authority: "primary"},
	}
	return input
}

func TestNormalizeUpsertHypothesisInputPreservesCanonicalIdentityAndCaller(t *testing.T) {
	input := UpsertHypothesisInput{
		TeamID: " " + policyTeamID + " ", RunID: " " + policyRunID + " ",
		Statement: "  A may use B  ", SubjectEntityID: " " + policySubjectID + " ",
		PredicateKey: " USES ", ObjectEntityID: " " + policyObjectID + " ",
		ContentHash: " sha256:test ", SourceEvidenceIDs: []string{" " + policyEvidenceID + " ", policyEvidenceID},
		EvidenceDerivations: []EvidenceDerivationSource{{EvidenceID: " " + policyEvidenceID + " ", Authority: " primary "}},
		Derivations:         []DreamDerivationSource{{RelationshipID: " " + policySourceA + " "}},
	}
	got := NormalizeUpsertHypothesisInput(input)
	require.Equal(t, domain.DreamLaneGraph, got.Lane)
	require.Equal(t, "deterministic", got.GeneratorKind)
	require.Equal(t, "dream-v2", got.GeneratorVersion)
	require.Equal(t, 1, got.PredicateVersion)
	require.Equal(t, []string{policyEvidenceID}, got.SourceEvidenceIDs)
	require.Equal(t, "sha256:99636d4216c34b97f10efea4cf6019930ec578e7047fe720970d08fece15e23c", got.TargetIdentity)
	require.Equal(t, policyEvidenceID, got.EvidenceDerivations[0].EvidenceID)
	require.Equal(t, "primary", got.EvidenceDerivations[0].Authority)
	require.Equal(t, policySourceA, got.Derivations[0].RelationshipID)
	require.Equal(t, " "+policyEvidenceID+" ", input.EvidenceDerivations[0].EvidenceID)
	require.Equal(t, " "+policySourceA+" ", input.Derivations[0].RelationshipID)
}

func TestValidateUpsertHypothesisInputKeepsLanesDistinct(t *testing.T) {
	evidence := validPolicyEvidenceProposal()
	require.NoError(t, ValidateUpsertHypothesisInput(evidence, true))
	graph := validPolicyGraphProposal()
	require.NoError(t, ValidateUpsertHypothesisInput(graph, true))
	seed := graph
	seed.GeneratorKind = "evaluation_seed"
	seed.Derivations = nil
	require.NoError(t, ValidateUpsertHypothesisInput(seed, true))

	for _, tc := range []struct {
		name   string
		mutate func(*UpsertHypothesisInput)
		want   string
	}{
		{"missing evidence", func(x *UpsertHypothesisInput) { x.SourceEvidenceIDs = nil }, "evidence discovery hypotheses require evidence derivations"},
		{"duplicate evidence", func(x *UpsertHypothesisInput) { x.SourceEvidenceIDs = append(x.SourceEvidenceIDs, policyEvidenceID) }, "source_evidence_ids[1] is duplicated"},
		{"invalid authority", func(x *UpsertHypothesisInput) { x.EvidenceDerivations[0].Authority = "derived" }, "evidence_derivations[0].authority is unsupported"},
		{"invalid span", func(x *UpsertHypothesisInput) { x.EvidenceDerivations[0].SpanEnd = 0 }, "evidence_derivations[0] is incomplete"},
		{"wrong target identity", func(x *UpsertHypothesisInput) { x.TargetIdentity = "sha256:wrong" }, "target_identity must match the canonical hypothesis target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validPolicyEvidenceProposal()
			tc.mutate(&input)
			require.ErrorContains(t, ValidateUpsertHypothesisInput(input, true), tc.want)
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*UpsertHypothesisInput)
		want   string
	}{
		{"missing premise", func(x *UpsertHypothesisInput) { x.Derivations[1].PremisePosition = 1 }, "dream derivations must cover both premise positions"},
		{"missing support or observation", func(x *UpsertHypothesisInput) { x.Derivations[0].SupportID = "" }, "derivations[0] must identify exactly one support or observation"},
		{"invalid authority", func(x *UpsertHypothesisInput) { x.Derivations[0].Authority = "derived" }, "derivations[0].authority is unsupported"},
		{"invalid span", func(x *UpsertHypothesisInput) { x.Derivations[0].SpanEnd = 0 }, "derivations[0] has an invalid evidence span"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validPolicyGraphProposal()
			tc.mutate(&input)
			require.ErrorContains(t, ValidateUpsertHypothesisInput(input, true), tc.want)
		})
	}
}
