package memoryservice

import (
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/placementreview"
	"github.com/stretchr/testify/require"
)

func TestValidateProposalRejectsEveryMalformedGraphBoundary(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Mark works on Dense-Mem.", SourceGroup: "source-1"}}
	valid := graphReviewProposal(len(evidence[0].Content))
	require.NoError(t, validateProposal(evidence, valid))

	later := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	earlier := later.Add(-time.Hour)
	tests := []struct {
		name   string
		mutate func(*domain.MemoryProposal)
		text   string
	}{
		{name: "entities required", mutate: func(v *domain.MemoryProposal) { v.Entities = nil }, text: "proposal.entities is required"},
		{name: "relationships required", mutate: func(v *domain.MemoryProposal) { v.Relationships = nil }, text: "proposal.relationships is required"},
		{name: "too many entities", mutate: func(v *domain.MemoryProposal) {
			v.Entities = make([]domain.MemoryEntityProposal, maxProposalEntities+1)
		}, text: "entities exceeds"},
		{name: "too many relationships", mutate: func(v *domain.MemoryProposal) {
			v.Relationships = make([]domain.MemoryRelationshipProposal, maxProposalRelationships+1)
		}, text: "relationships exceeds"},
		{name: "entity fields", mutate: func(v *domain.MemoryProposal) { v.Entities[0].Name = "" }, text: "requires ref, name, and type"},
		{name: "duplicate entity", mutate: func(v *domain.MemoryProposal) { v.Entities = append(v.Entities, v.Entities[0]) }, text: "duplicate entity ref"},
		{name: "relationship fields", mutate: func(v *domain.MemoryProposal) { v.Relationships[0].Predicate = "" }, text: "requires proposal_id and predicate"},
		{name: "unknown subject", mutate: func(v *domain.MemoryProposal) { v.Relationships[0].SubjectRef = "unknown" }, text: "subject_ref is unknown"},
		{name: "no object", mutate: func(v *domain.MemoryProposal) { v.Relationships[0].ObjectRef = "" }, text: "exactly one object_ref or object_value"},
		{name: "two objects", mutate: func(v *domain.MemoryProposal) {
			v.Relationships[0].ObjectValue = &domain.MemoryValueProposal{Type: domain.ValueTypeString, Value: "x"}
		}, text: "exactly one object_ref or object_value"},
		{name: "invalid value", mutate: func(v *domain.MemoryProposal) {
			v.Relationships[0].ObjectRef = ""
			v.Relationships[0].ObjectValue = &domain.MemoryValueProposal{Type: "bad", Value: "x"}
		}, text: "object_value is invalid"},
		{name: "invalid policy", mutate: func(v *domain.MemoryProposal) { v.Relationships[0].PolicyFamily = "bad" }, text: "policy_family is invalid"},
		{name: "invalid polarity", mutate: func(v *domain.MemoryProposal) { v.Relationships[0].Polarity = "bad" }, text: "polarity or modality is invalid"},
		{name: "invalid time", mutate: func(v *domain.MemoryProposal) {
			v.Relationships[0].ValidFrom = &later
			v.Relationships[0].ValidTo = &earlier
		}, text: "valid_to must be after valid_from"},
		{name: "evidence required", mutate: func(v *domain.MemoryProposal) { v.Relationships[0].Evidence = nil }, text: "evidence is required"},
		{name: "evidence index", mutate: func(v *domain.MemoryProposal) { v.Relationships[0].Evidence[0].EvidenceIndex = 3 }, text: "evidence_index 3 is invalid"},
		{name: "evidence span", mutate: func(v *domain.MemoryProposal) { v.Relationships[0].Evidence[0].End = 999 }, text: "evidence span is invalid"},
		{name: "repeated evidence", mutate: func(v *domain.MemoryProposal) {
			v.Relationships[0].Evidence = append(v.Relationships[0].Evidence, v.Relationships[0].Evidence[0])
		}, text: "repeats evidence_index"},
		{name: "duplicate proposal", mutate: func(v *domain.MemoryProposal) { v.Relationships = append(v.Relationships, v.Relationships[0]) }, text: "duplicate proposal_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proposal := graphReviewProposal(len(evidence[0].Content))
			tc.mutate(&proposal)
			require.ErrorContains(t, validateProposal(evidence, proposal), tc.text)
		})
	}
}

func TestValidateProposalUsesUnicodeCodePointOffsets(t *testing.T) {
	content := "马克 demoed Dense-Mem."
	evidence := []domain.MemoryEvidence{{Index: 0, Content: content, SourceGroup: "source-1"}}
	proposal := graphReviewProposal(len([]rune(content)))
	require.NoError(t, validateProposal(evidence, proposal))

	proposal.Relationships[0].Evidence[0].End = len(content)
	require.ErrorContains(t, validateProposal(evidence, proposal), "evidence span is invalid")
}

func TestNormalizeReviewedResultValidatesReviewerAndSortsAtomicRelations(t *testing.T) {
	evidence := []domain.MemoryEvidence{{Index: 0, Content: "Mark works on Dense-Mem.", SourceGroup: "source-1"}}
	proposal := graphReviewProposal(len(evidence[0].Content))
	second := proposal.Relationships[0]
	second.ProposalID = " a-relation "
	second.SubjectRef = " mark "
	second.ObjectRef = " dense-mem "
	second.Predicate = "contributed_to"
	result := placementreview.Result{
		Entities: []placementreview.ReviewedEntity{
			{Proposal: proposal.Entities[0], ResolutionStatus: domain.EntityResolutionCanonical, ResolutionConf: 0.9},
			{Proposal: proposal.Entities[1], ResolutionStatus: domain.EntityResolutionProvisional, ResolutionConf: 0.8},
		},
		Relationships: []placementreview.ReviewedRelationship{
			{Proposal: proposal.Relationships[0], Atomic: true, ExtractConf: 0.9, Rationale: " valid "},
			{Proposal: second, Atomic: true, ExtractConf: 0.8, Rationale: " valid "},
		},
	}

	normalized, err := normalizeReviewedResult(evidence, result)
	require.NoError(t, err)
	require.Equal(t, "a-relation", normalized.Relationships[0].Proposal.ProposalID)
	require.Equal(t, "mark", normalized.Relationships[0].Proposal.SubjectRef)
	require.Equal(t, "dense-mem", normalized.Relationships[0].Proposal.ObjectRef)
	require.Equal(t, "valid", normalized.Relationships[0].Rationale)

	tests := []struct {
		name   string
		mutate func(*placementreview.Result)
		text   string
	}{
		{name: "resolution status", mutate: func(v *placementreview.Result) { v.Entities[0].ResolutionStatus = "bad" }, text: "resolution_status is invalid"},
		{name: "resolution confidence", mutate: func(v *placementreview.Result) { v.Entities[0].ResolutionConf = 2 }, text: "resolution_conf is invalid"},
		{name: "not atomic", mutate: func(v *placementreview.Result) { v.Relationships[0].Atomic = false }, text: "is not atomic"},
		{name: "extract confidence", mutate: func(v *placementreview.Result) { v.Relationships[0].ExtractConf = -1 }, text: "extract_conf is invalid"},
		{name: "invalid proposal", mutate: func(v *placementreview.Result) { v.Relationships[0].Proposal.ObjectRef = "" }, text: "reviewer returned invalid proposal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			copy := result
			copy.Entities = append([]placementreview.ReviewedEntity(nil), result.Entities...)
			copy.Relationships = append([]placementreview.ReviewedRelationship(nil), result.Relationships...)
			tc.mutate(&copy)
			_, callErr := normalizeReviewedResult(evidence, copy)
			require.ErrorContains(t, callErr, tc.text)
		})
	}
}

func graphReviewProposal(contentLength int) domain.MemoryProposal {
	return domain.MemoryProposal{
		Entities: []domain.MemoryEntityProposal{{Ref: "mark", Name: "Mark", Type: "person"}, {Ref: "dense-mem", Name: "Dense-Mem", Type: "project"}},
		Relationships: []domain.MemoryRelationshipProposal{{
			ProposalID: "works-on", SubjectRef: "mark", Predicate: "works_on", ObjectRef: "dense-mem",
			PolicyFamily: domain.AssertionPolicyVersioned, Polarity: domain.PolarityPlus, Modality: domain.ModalityAssertion,
			Evidence: []domain.MemoryEvidenceRef{{EvidenceIndex: 0, Start: 0, End: contentLength}},
		}},
	}
}
