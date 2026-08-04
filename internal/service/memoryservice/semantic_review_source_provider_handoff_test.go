package memoryservice

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSemanticPlacementReviewSourceForwardsFlatRelationshipHintsToProvider(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	content := "Dense-Mem uses PostgreSQL."
	provider := &reviewSourceProposalProviderStub{
		proposal: verifier.ProviderProposal{
			EntityProposals: []verifier.ProviderEntityProposal{
				{
					Ref:        "dense-mem",
					Name:       "Dense-Mem",
					EntityKind: "project",
					Evidence:   []verifier.ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: 9}},
				},
				{
					Ref:        "postgresql",
					Name:       "PostgreSQL",
					EntityKind: "product",
					Evidence:   []verifier.ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 15, End: 25}},
				},
			},
			RelationshipProposals: []verifier.ProviderRelationshipProposal{{
				ProposalID:          "uses-postgresql",
				SubjectRef:          "dense-mem",
				OriginalPredicate:   "uses",
				PredicateCandidates: []string{"uses"},
				RelationshipKind:    "state",
				ObjectRef:           "postgresql",
				Polarity:            "+",
				Modality:            "statement",
				Evidence:            []verifier.ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: 25}},
			}},
		},
	}
	source := NewSemanticPlacementReviewSource(SemanticPlacementReviewSourceDependencies{
		Catalog:          &reviewSourceCatalogStub{predicateOptions: []string{"uses"}},
		ProposalProvider: provider,
	})
	flatRelationship := map[string]any{
		"ref": "uses-postgresql",
		"subject": map[string]any{
			"name": "Dense-Mem", "entity_kind": "project",
			"span": map[string]any{"evidence_index": 0, "start": 0, "end": 9},
		},
		"predicate": map[string]any{
			"proposed_key": "uses", "surface": "uses",
			"span": map[string]any{"evidence_index": 0, "start": 10, "end": 14},
		},
		"object": map[string]any{"entity": map[string]any{
			"name": "PostgreSQL", "entity_kind": "product",
			"span": map[string]any{"evidence_index": 0, "start": 15, "end": 25},
		}},
		"polarity": "+",
		"modality": "statement",
		"supports": []any{map[string]any{"evidence_index": 0, "start": 0, "end": 25}},
	}

	proposal, validationErrors, retryable, _, err := source.(*semanticPlacementReviewSource).placementReviewProviderProposal(
		context.Background(),
		repository.PlacementRun{TeamID: teamID, OwnerProfileID: ownerID},
		verifier.SemanticReviewEvidence{EvidenceID: "evidence:0", FragmentID: "fragment:0", EvidenceIndex: 0, Content: content},
		map[string]any{"relationships": []any{flatRelationship}},
	)
	if err != nil || retryable || len(validationErrors) != 0 || proposal == nil {
		t.Fatalf("provider proposal = %#v, validation=%#v retryable=%v err=%v", proposal, validationErrors, retryable, err)
	}
	if len(provider.req.RelationshipHints) != 1 || len(provider.req.PredicateOptions) != 1 || provider.req.PredicateOptions[0] != "uses" {
		t.Fatalf("provider request = %#v", provider.req)
	}
	got := provider.req.RelationshipHints[0]
	if !reflect.DeepEqual(got, flatRelationship) {
		t.Fatalf("flat relationship hint = %#v, want %#v", got, flatRelationship)
	}
}
