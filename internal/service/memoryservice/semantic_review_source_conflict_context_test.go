package memoryservice

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestV2SemanticPlacementReviewSourceRejectsStaleConflictContextBeforeProvider(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	conflictID := uuid.NewString()
	content := "Dense-Mem uses GraphDB."
	ledger := &reviewSourceLedgerStub{
		conflictContextErr: repository.ErrV2ConflictContextStale,
		placement: &repository.V2CreateIngestResult{
			TeamID:         teamID,
			OwnerProfileID: ownerID,
			IngestID:       ingestID,
			PlacementRunID: runID,
			Status:         "processing",
			Proposal: map[string]any{
				"entity_hints": []map[string]any{
					{"ref": "project_1", "name": "Dense-Mem", "entity_kind": "project"},
					{"ref": "db_1", "name": "GraphDB", "entity_kind": "project"},
				},
				"relationship_hints": []map[string]any{{
					"proposal_id": "rel:uses",
					"subject_ref": "project_1",
					"predicate":   "uses",
					"object_ref":  "db_1",
					"conflict_context": map[string]any{
						"conflict_id":      conflictID,
						"expected_version": 2,
					},
					"evidence": []map[string]any{{
						"evidence_index": 0,
						"start":          0,
						"end":            utf8.RuneCountInString(content),
					}},
				}},
			},
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
		},
	}
	provider := &reviewSourceProposalProviderStub{}
	source := NewSemanticPlacementReviewSource(SemanticPlacementReviewSourceDependencies{
		Ledger:           ledger,
		Catalog:          &reviewSourceCatalogStub{},
		ProposalProvider: provider,
	})

	job, err := source.BuildSemanticReviewJob(context.Background(), repository.V2PlacementRun{
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
	if len(job.ValidationErrors) != 1 || job.ValidationErrors[0].Field != "relationship_hints[0].conflict_context" {
		t.Fatalf("validation errors = %#v", job.ValidationErrors)
	}
	if !strings.Contains(job.ValidationErrors[0].Message, "stale") {
		t.Fatalf("validation error message = %q", job.ValidationErrors[0].Message)
	}
	if len(provider.reqs) != 0 {
		t.Fatalf("provider was called %#v", provider.reqs)
	}
}

func TestV2SemanticPlacementReviewSourceRejectsMalformedConflictContextBeforeProvider(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	content := "Dense-Mem uses GraphDB."
	ledger := &reviewSourceLedgerStub{
		placement: &repository.V2CreateIngestResult{
			TeamID:         teamID,
			OwnerProfileID: ownerID,
			IngestID:       ingestID,
			PlacementRunID: runID,
			Status:         "processing",
			Proposal: map[string]any{
				"relationship_hints": []map[string]any{{
					"proposal_id": "rel:uses",
					"subject_ref": "project_1",
					"predicate":   "uses",
					"object_ref":  "db_1",
					"conflict_context": map[string]any{
						"conflict_id": "",
					},
					"evidence": []map[string]any{{
						"evidence_index": 0,
						"start":          0,
						"end":            utf8.RuneCountInString(content),
					}},
				}},
			},
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
		},
	}
	provider := &reviewSourceProposalProviderStub{}
	source := NewSemanticPlacementReviewSource(SemanticPlacementReviewSourceDependencies{
		Ledger:           ledger,
		Catalog:          &reviewSourceCatalogStub{},
		ProposalProvider: provider,
	})

	job, err := source.BuildSemanticReviewJob(context.Background(), repository.V2PlacementRun{
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
	if len(job.ValidationErrors) != 1 || job.ValidationErrors[0].Field != "relationship_hints[0].conflict_context" {
		t.Fatalf("validation errors = %#v", job.ValidationErrors)
	}
	if len(provider.reqs) != 0 {
		t.Fatalf("provider was called %#v", provider.reqs)
	}
}
