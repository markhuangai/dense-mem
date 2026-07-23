package memoryservice

import (
	"context"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestV2SemanticPlacementReviewSourceKeepsProviderPredicateCandidatesRequestLocal(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	content := "Dense-Mem has latency of 135ms."
	ledger := &v2ReviewSourceLedgerStub{placement: &repository.V2CreateIngestResult{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		Proposal:       map[string]any{},
		Evidence: []repository.V2EvidenceFragment{{
			FragmentID:    uuid.NewString(),
			EvidenceIndex: 0,
			Content:       content,
			ContentHash:   "sha256:current",
		}},
		Items: []repository.V2PlacementItem{{
			PlacementItemID: itemID,
			EvidenceIndex:   0,
			Status:          "queued",
		}},
	}}
	catalog := &v2ReviewSourceCatalogStub{
		predicateOptions:    []string{"has latency"},
		predicateCandidates: map[string][]repository.V2SemanticReviewPredicateCandidate{},
	}
	provider := &v2ReviewSourceProposalProviderStub{
		proposal: verifier.V2ProviderProposal{
			PredicateOptions: []string{"has latency"},
			EntityProposals: []verifier.V2ProviderEntityProposal{{
				Ref:        "project_1",
				Name:       "Dense-Mem",
				EntityKind: "project",
				Evidence:   []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString("Dense-Mem")}},
			}},
			RelationshipProposals: []verifier.V2ProviderRelationshipProposal{{
				ProposalID:          "rel:latency",
				SubjectRef:          "project_1",
				OriginalPredicate:   "has latency",
				PredicateCandidates: []string{"has latency"},
				RelationshipKind:    "state",
				ObjectValue: &verifier.V2SemanticValueObservation{
					Ref:     "latency",
					Type:    "number",
					Value:   "135",
					Display: "135ms",
					Unit:    "ms",
				},
				Polarity: "+",
				Modality: "statement",
				Evidence: []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString(content)}},
			}},
		},
	}
	source := NewV2SemanticPlacementReviewSource(V2SemanticPlacementReviewSourceDependencies{
		Ledger:           ledger,
		Catalog:          catalog,
		ProposalProvider: provider,
	})

	job, err := source.BuildV2SemanticReviewJob(context.Background(), repository.V2PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		MaxAttempts:    3,
	})
	if err != nil {
		t.Fatalf("BuildV2SemanticReviewJob returned error: %v", err)
	}
	if catalog.ensurePredicateCalls != 0 {
		t.Fatalf("provider predicate candidates were durably ensured %d times", catalog.ensurePredicateCalls)
	}
	if len(job.Request.RelationshipObservations) != 1 {
		t.Fatalf("relationship observations = %#v", job.Request.RelationshipObservations)
	}
	candidates := job.Request.RelationshipObservations[0].PredicateCandidates
	if len(candidates) != 1 || candidates[0].PredicateKey != "has_latency" || candidates[0].RelationshipKind != "state" {
		t.Fatalf("request-local predicate candidates = %#v", candidates)
	}
	prepared, validationErrs := verifier.PrepareV2SemanticReviewRequest(job.Request)
	if len(validationErrs) > 0 {
		t.Fatalf("prepared request validation errors = %#v", validationErrs)
	}
	if len(prepared.RelationshipObservations[0].PredicateCandidates) != 1 {
		t.Fatalf("prepared predicate candidates = %#v", prepared.RelationshipObservations[0].PredicateCandidates)
	}
}

func TestV2ReviewSourceCanonicalPredicateCandidateRejectsAmbiguousAliases(t *testing.T) {
	candidate, matched, ambiguous := v2ReviewSourceCanonicalPredicateCandidate([]repository.V2SemanticReviewPredicateResolution{
		{
			RequestedPredicate: "depends_on",
			MatchKind:          "alias",
			Candidate: repository.V2SemanticReviewPredicateCandidate{
				PredicateKey: "uses",
				Version:      1,
			},
		},
		{
			RequestedPredicate: "depends_on",
			MatchKind:          "alias",
			Candidate: repository.V2SemanticReviewPredicateCandidate{
				PredicateKey: "requires",
				Version:      1,
			},
		},
	})

	if matched || !ambiguous || candidate.PredicateKey != "" {
		t.Fatalf("candidate = %#v, matched = %v, ambiguous = %v", candidate, matched, ambiguous)
	}
}
