package memoryservice

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSemanticPlacementReviewSourceKeepsProviderPredicateCandidatesRequestLocal(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	content := "Dense-Mem has latency of 135ms."
	ledger := &reviewSourceLedgerStub{placement: &repository.CreateIngestResult{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		Proposal:       map[string]any{},
		Evidence: []repository.EvidenceFragment{{
			FragmentID:    uuid.NewString(),
			EvidenceIndex: 0,
			Content:       content,
			ContentHash:   "sha256:current",
		}},
		Items: []repository.PlacementItem{{
			PlacementItemID: itemID,
			EvidenceIndex:   0,
			Status:          "queued",
		}},
	}}
	catalog := &reviewSourceCatalogStub{
		predicateOptions:    []string{"has latency"},
		predicateCandidates: map[string][]repository.SemanticReviewPredicateCandidate{},
	}
	provider := &reviewSourceProposalProviderStub{
		proposal: verifier.ProviderProposal{
			PredicateOptions: []string{"has latency"},
			EntityProposals: []verifier.ProviderEntityProposal{{
				Ref:        "project_1",
				Name:       "Dense-Mem",
				EntityKind: "project",
				Evidence:   []verifier.ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString("Dense-Mem")}},
			}},
			RelationshipProposals: []verifier.ProviderRelationshipProposal{{
				ProposalID:          "rel:latency",
				SubjectRef:          "project_1",
				OriginalPredicate:   "has latency",
				PredicateCandidates: []string{"has latency"},
				RelationshipKind:    "state",
				ObjectValue: &verifier.SemanticValueObservation{
					Ref:     "latency",
					Type:    "number",
					Value:   "135",
					Display: "135ms",
					Unit:    "ms",
				},
				Polarity: "+",
				Modality: "statement",
				Evidence: []verifier.ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString(content)}},
			}},
		},
	}
	source := NewSemanticPlacementReviewSource(SemanticPlacementReviewSourceDependencies{
		Ledger:           ledger,
		Catalog:          catalog,
		ProposalProvider: provider,
	})

	job, err := source.BuildSemanticReviewJob(context.Background(), repository.PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		MaxAttempts:    3,
	})
	if err != nil {
		t.Fatalf("BuildSemanticReviewJob returned error: %v", err)
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
	prepared, validationErrs := verifier.PrepareSemanticReviewRequest(job.Request)
	if len(validationErrs) > 0 {
		t.Fatalf("prepared request validation errors = %#v", validationErrs)
	}
	if len(prepared.RelationshipObservations[0].PredicateCandidates) != 1 {
		t.Fatalf("prepared predicate candidates = %#v", prepared.RelationshipObservations[0].PredicateCandidates)
	}
}

func TestReviewSourceCanonicalPredicateCandidateRejectsAmbiguousAliases(t *testing.T) {
	candidate, matched, ambiguous := reviewSourceCanonicalPredicateCandidate([]repository.SemanticReviewPredicateResolution{
		{
			RequestedPredicate: "depends_on",
			MatchKind:          "alias",
			Candidate: repository.SemanticReviewPredicateCandidate{
				PredicateKey: "uses",
				Version:      1,
			},
		},
		{
			RequestedPredicate: "depends_on",
			MatchKind:          "alias",
			Candidate: repository.SemanticReviewPredicateCandidate{
				PredicateKey: "requires",
				Version:      1,
			},
		},
	})

	if matched || !ambiguous || candidate.PredicateKey != "" {
		t.Fatalf("candidate = %#v, matched = %v, ambiguous = %v", candidate, matched, ambiguous)
	}
}

func TestReviewSourceCorrectionTargetMatchesByObjectRefOrValue(t *testing.T) {
	target := reviewSourceCorrectionTarget{
		SubjectRef:     "entity:a",
		PredicateKey:   "depends_on",
		ObjectRef:      "entity:b",
		ObjectValueKey: "ignored",
	}
	if !reviewSourceCorrectionTargetMatches(reviewSourceCorrectionTarget{
		SubjectRef:   "entity:a",
		PredicateKey: "depends_on",
		ObjectRef:    "entity:b",
	}, target) {
		t.Fatal("object ref target did not match")
	}
	if reviewSourceCorrectionTargetMatches(reviewSourceCorrectionTarget{
		SubjectRef:   "entity:a",
		PredicateKey: "depends_on",
		ObjectRef:    "entity:c",
	}, target) {
		t.Fatal("different object ref matched")
	}
	if reviewSourceCorrectionTargetMatches(reviewSourceCorrectionTarget{
		SubjectRef:   "entity:a",
		PredicateKey: "uses",
		ObjectRef:    "entity:b",
	}, target) {
		t.Fatal("different predicate matched")
	}

	valueTarget := reviewSourceCorrectionTarget{
		SubjectRef:     "entity:a",
		PredicateKey:   "has_latency",
		ObjectValueKey: "number\x00135\x00ms",
	}
	if !reviewSourceCorrectionTargetMatches(reviewSourceCorrectionTarget{
		SubjectRef:     "entity:a",
		PredicateKey:   "has_latency",
		ObjectValueKey: "number\x00135\x00ms",
	}, valueTarget) {
		t.Fatal("object value target did not match")
	}
	if reviewSourceCorrectionTargetMatches(reviewSourceCorrectionTarget{
		SubjectRef:   "entity:a",
		PredicateKey: "has_latency",
	}, valueTarget) {
		t.Fatal("missing object endpoint matched")
	}
}

func TestReviewSourcePredicateHelpersNormalizeFallbackAndBounds(t *testing.T) {
	if got := reviewSourceAllowedKinds("", "other"); len(got) != 1 || got[0] != "other" {
		t.Fatalf("fallback kinds = %#v", got)
	}
	if got := reviewSourcePredicateKey("  Uses / Depends On!!! "); got != "uses_depends_on" {
		t.Fatalf("predicate key = %q", got)
	}
	longKey := reviewSourcePredicateKey(strings.Repeat("a", 70) + "!")
	if len(longKey) != 64 || strings.HasSuffix(longKey, "_") {
		t.Fatalf("bounded predicate key = %q len=%d", longKey, len(longKey))
	}
}
