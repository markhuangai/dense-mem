package memoryservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestV2SemanticPlacementReviewSourceBuildsCurrentEvidenceJob(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	doneItemID := uuid.NewString()
	queuedItemID := uuid.NewString()
	markID := uuid.NewString()
	targetID := uuid.NewString()
	currentContent := "Mark works on Dense-Mem using PostgreSQL."
	worksQuote := "Mark works on Dense-Mem"
	usesQuote := "Dense-Mem using PostgreSQL"
	validFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	validTo := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	validFromText := validFrom.Format(time.RFC3339)
	validToText := validTo.Format(time.RFC3339)
	ledger := &v2ReviewSourceLedgerStub{placement: &repository.V2CreateIngestResult{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		Proposal: map[string]any{
			"entity_hints": []map[string]any{
				{"ref": "person_1", "name": "Mark", "entity_kind": "person", "known_entity_id": markID, "identity_context": map[string]any{"github": "markhuangai"}},
				{"ref": "project_1", "name": "Dense-Mem", "entity_kind": "project"},
				{"ref": "db_1", "name": "PostgreSQL", "entity_kind": "project"},
			},
			"relationship_hints": []map[string]any{
				{
					"proposal_id": "rel:ignored",
					"subject_ref": "person_1",
					"predicate":   "works_on",
					"object_ref":  "project_1",
					"evidence": []map[string]any{{
						"evidence_index": 0,
						"start":          0,
						"end":            4,
					}},
				},
				{
					"proposal_id": "rel:works",
					"subject_ref": "person_1",
					"predicate":   "works_on",
					"object_ref":  "project_1",
					"evidence": []map[string]any{{
						"evidence_index": 1,
						"start":          0,
						"end":            utf8.RuneCountInString(worksQuote),
					}},
				},
				{
					"proposal_id": "rel:uses",
					"subject_ref": "project_1",
					"predicate":   "uses",
					"object_ref":  "db_1",
					"valid_from":  validFrom.Format(time.RFC3339),
					"valid_to":    validTo.Format(time.RFC3339),
					"correction_target": map[string]any{
						"relationship_id":  targetID,
						"expected_version": 4,
					},
					"evidence": []map[string]any{{
						"evidence_index": 1,
						"start":          utf8.RuneCountInString("Mark works on "),
						"end":            utf8.RuneCountInString("Mark works on ") + utf8.RuneCountInString(usesQuote),
					}},
				},
			},
		},
		Evidence: []repository.V2EvidenceFragment{
			{FragmentID: uuid.NewString(), EvidenceIndex: 0, Content: "old evidence", ContentHash: "sha256:old"},
			{FragmentID: uuid.NewString(), EvidenceIndex: 1, Content: currentContent, ContentHash: "sha256:current", SourceRevisionID: uuid.NewString()},
		},
		Items: []repository.V2PlacementItem{
			{PlacementItemID: doneItemID, EvidenceIndex: 0, Status: "completed"},
			{PlacementItemID: queuedItemID, EvidenceIndex: 1, Status: "queued"},
		},
	}}
	catalog := &v2ReviewSourceCatalogStub{
		predicateOptions: []string{"uses", "works_on"},
		entityCandidates: map[string][]repository.V2SemanticReviewEntityCandidate{
			"Mark": {{
				TeamID:        teamID,
				EntityID:      markID,
				EntityKind:    "person",
				CanonicalName: "Mark Huang",
				Status:        "active",
			}},
		},
		predicateCandidates: map[string][]repository.V2SemanticReviewPredicateCandidate{
			"works_on": {{
				PredicateKey:        "works_on",
				Version:             1,
				AllowedSubjectKinds: []string{"person"},
				AllowedObjectKinds:  []string{"project"},
				RelationshipKind:    "state",
				CurrentCardinality:  "many",
				LifecycleState:      "active",
			}},
			"uses": {{
				PredicateKey:        "uses",
				Version:             1,
				AllowedSubjectKinds: []string{"project"},
				AllowedObjectKinds:  []string{"project"},
				RelationshipKind:    "state",
				CurrentCardinality:  "many",
				LifecycleState:      "active",
			}},
		},
	}
	provider := &v2ReviewSourceProposalProviderStub{
		proposal: verifier.V2ProviderProposal{
			PredicateOptions: []string{"uses", "works_on"},
			EntityProposals: []verifier.V2ProviderEntityProposal{
				{
					Ref:           "person_1",
					Name:          "Mark",
					EntityKind:    "person",
					KnownEntityID: &markID,
					IdentityContext: map[string]any{
						"github": "markhuangai",
					},
					Evidence: []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 1, Start: 0, End: utf8.RuneCountInString("Mark")}},
				},
				{
					Ref:        "project_1",
					Name:       "Dense-Mem",
					EntityKind: "project",
					Evidence: []verifier.V2ProviderEvidenceSpan{{
						EvidenceIndex: 1,
						Start:         utf8.RuneCountInString("Mark works on "),
						End:           utf8.RuneCountInString("Mark works on Dense-Mem"),
					}},
				},
				{
					Ref:        "db_1",
					Name:       "PostgreSQL",
					EntityKind: "project",
					Evidence: []verifier.V2ProviderEvidenceSpan{{
						EvidenceIndex: 1,
						Start:         utf8.RuneCountInString("Mark works on Dense-Mem using "),
						End:           utf8.RuneCountInString(currentContent),
					}},
				},
			},
			RelationshipProposals: []verifier.V2ProviderRelationshipProposal{{
				ProposalID:        "rel:uses",
				SubjectRef:        "project_1",
				OriginalPredicate: "uses",
				PredicateCandidates: []string{
					"uses",
				},
				RelationshipKind: "state",
				ObjectRef:        "db_1",
				Polarity:         "-",
				Modality:         "statement",
				ValidFrom:        &validFromText,
				ValidTo:          &validToText,
				Evidence: []verifier.V2ProviderEvidenceSpan{{
					EvidenceIndex: 1,
					Start:         utf8.RuneCountInString("Mark works on "),
					End:           utf8.RuneCountInString("Mark works on ") + utf8.RuneCountInString(usesQuote),
				}},
			}, {
				ProposalID:        "rel:works",
				SubjectRef:        "person_1",
				OriginalPredicate: "works_on",
				PredicateCandidates: []string{
					"works_on",
				},
				RelationshipKind: "state",
				ObjectRef:        "project_1",
				Polarity:         "+",
				Modality:         "statement",
				Evidence: []verifier.V2ProviderEvidenceSpan{{
					EvidenceIndex: 1,
					Start:         0,
					End:           utf8.RuneCountInString(worksQuote),
				}},
			}},
		},
	}
	source := NewV2SemanticPlacementReviewSource(V2SemanticPlacementReviewSourceDependencies{
		Ledger:           ledger,
		Catalog:          catalog,
		ProposalProvider: provider,
		CandidateLimit:   3,
	})

	job, err := source.BuildV2SemanticReviewJob(context.Background(), repository.V2PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		Attempts:       2,
		MaxAttempts:    5,
	})
	if err != nil {
		t.Fatalf("BuildV2SemanticReviewJob returned error: %v", err)
	}

	if ledger.input.TeamID != teamID || ledger.input.OwnerProfileID != ownerID || ledger.input.IngestID != ingestID {
		t.Fatalf("placement load input = %#v", ledger.input)
	}
	if job.PlacementItemID != queuedItemID || job.MaxAttempts != 5 || job.Request.RequestID != "semantic-review:"+queuedItemID {
		t.Fatalf("job scope = %#v", job)
	}
	if len(job.Request.Evidence) != 1 || job.Request.Evidence[0].EvidenceIndex != 1 || job.Request.Evidence[0].Content != currentContent {
		t.Fatalf("evidence = %#v", job.Request.Evidence)
	}
	if len(job.Request.EntityMentions) != 3 {
		t.Fatalf("entity mentions = %#v validation=%#v retryable=%#v", job.Request.EntityMentions, job.ValidationErrors, job.RetryableValidationErrors)
	}
	mentionsByRef := map[string]verifier.V2SemanticEntityMention{}
	for _, mention := range job.Request.EntityMentions {
		mentionsByRef[mention.Ref] = mention
	}
	personMention := mentionsByRef["person_1"]
	if personMention.Ref != "person_1" || personMention.Candidates[0].EntityID != markID {
		t.Fatalf("person mention = %#v", personMention)
	}
	if personMention.IdentityContext["github"] != "markhuangai" {
		t.Fatalf("person identity context = %#v", personMention.IdentityContext)
	}
	if len(job.Request.RelationshipObservations) != 2 {
		t.Fatalf("relationship observations = %#v", job.Request.RelationshipObservations)
	}
	if job.Request.RelationshipObservations[0].Ref != "rel:uses" || job.Request.RelationshipObservations[0].Quote != usesQuote {
		t.Fatalf("uses observation = %#v", job.Request.RelationshipObservations[0])
	}
	if job.Request.RelationshipObservations[0].Polarity != "-" {
		t.Fatalf("uses observation polarity = %q, want -", job.Request.RelationshipObservations[0].Polarity)
	}
	if got := job.Request.RelationshipObservations[0].CorrectionTarget; got == nil || got.RelationshipID != targetID || got.ExpectedVersion != 4 {
		t.Fatalf("correction target = %#v", got)
	}
	if got := job.Request.RelationshipObservations[0].ValidFrom; got == nil || !got.Equal(validFrom) {
		t.Fatalf("valid_from = %#v", got)
	}
	if got := job.Request.RelationshipObservations[0].ValidTo; got == nil || !got.Equal(validTo) {
		t.Fatalf("valid_to = %#v", got)
	}
	if job.Request.RelationshipObservations[1].Ref != "rel:works" || job.Request.RelationshipObservations[1].Quote != worksQuote {
		t.Fatalf("works observation = %#v", job.Request.RelationshipObservations[1])
	}
	prepared, validationErrs := verifier.PrepareV2SemanticReviewRequest(job.Request)
	if len(validationErrs) > 0 {
		t.Fatalf("prepared request validation errors = %#v", validationErrs)
	}
	if len(prepared.RelationshipObservations[0].PredicateCandidates) != 1 || len(prepared.RelationshipObservations[1].PredicateCandidates) != 1 {
		t.Fatalf("prepared predicate allowlists = %#v", prepared.RelationshipObservations)
	}
	if catalog.ensurePredicateCalls != 0 {
		t.Fatalf("provider predicate candidates were durably ensured %d times", catalog.ensurePredicateCalls)
	}
}

func TestV2SemanticPlacementReviewSourceExtractsWhenProposalAbsent(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	content := "Dense-Mem uses PostgreSQL."
	validFromText := "2026-07-01T00:00:00Z"
	validToText := "2026-12-31T00:00:00Z"
	ledger := &v2ReviewSourceLedgerStub{placement: &repository.V2CreateIngestResult{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		Proposal:       map[string]any{},
		Evidence: []repository.V2EvidenceFragment{{
			FragmentID:       uuid.NewString(),
			EvidenceIndex:    0,
			Content:          content,
			ContentHash:      "sha256:current",
			SourceRevisionID: uuid.NewString(),
		}},
		Items: []repository.V2PlacementItem{{
			PlacementItemID: itemID,
			EvidenceIndex:   0,
			Status:          "queued",
		}},
	}}
	catalog := &v2ReviewSourceCatalogStub{
		predicateOptions: []string{"uses", "works_on"},
		entityCandidates: map[string][]repository.V2SemanticReviewEntityCandidate{
			"Dense-Mem": {{
				TeamID:        teamID,
				EntityID:      uuid.NewString(),
				EntityKind:    "project",
				CanonicalName: "Dense-Mem",
				Status:        "active",
			}},
		},
		predicateCandidates: map[string][]repository.V2SemanticReviewPredicateCandidate{
			"uses": {{
				PredicateKey:        "uses",
				Version:             1,
				AllowedSubjectKinds: []string{"project"},
				AllowedObjectKinds:  []string{"project"},
				RelationshipKind:    "state",
				CurrentCardinality:  "many",
				LifecycleState:      "active",
			}},
		},
	}
	provider := &v2ReviewSourceProposalProviderStub{
		proposal: verifier.V2ProviderProposal{
			PredicateOptions: []string{"uses"},
			EntityProposals: []verifier.V2ProviderEntityProposal{{
				Ref:        "project_1",
				Name:       "Dense-Mem",
				EntityKind: "project",
				Evidence:   []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString("Dense-Mem")}},
			}, {
				Ref:        "db_1",
				Name:       "PostgreSQL",
				EntityKind: "project",
				Evidence: []verifier.V2ProviderEvidenceSpan{{
					EvidenceIndex: 0,
					Start:         utf8.RuneCountInString("Dense-Mem uses "),
					End:           utf8.RuneCountInString("Dense-Mem uses PostgreSQL"),
				}},
			}},
			RelationshipProposals: []verifier.V2ProviderRelationshipProposal{{
				ProposalID:        "rel:uses",
				SubjectRef:        "project_1",
				OriginalPredicate: "uses",
				PredicateCandidates: []string{
					"uses",
				},
				RelationshipKind: "state",
				ObjectRef:        "db_1",
				Polarity:         "+",
				Modality:         "statement",
				ValidFrom:        &validFromText,
				ValidTo:          &validToText,
				Evidence: []verifier.V2ProviderEvidenceSpan{{
					EvidenceIndex: 0,
					Start:         0,
					End:           utf8.RuneCountInString(content),
				}},
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
	if len(provider.req.PredicateOptions) != 2 || provider.req.Evidence[0].Content != content {
		t.Fatalf("provider request = %#v", provider.req)
	}
	if len(job.ValidationErrors) != 0 {
		t.Fatalf("validation errors = %#v", job.ValidationErrors)
	}
	if len(job.Request.EntityMentions) != 2 || len(job.Request.RelationshipObservations) != 1 {
		t.Fatalf("request = %#v", job.Request)
	}
	if job.Request.EntityMentions[1].Surface != "PostgreSQL" {
		t.Fatalf("provider entity evidence span was not preserved: %#v", job.Request.EntityMentions[1])
	}
	if job.Request.RelationshipObservations[0].Ref != "rel:uses" || job.Request.RelationshipObservations[0].Quote != content {
		t.Fatalf("relationship observation = %#v", job.Request.RelationshipObservations[0])
	}
	if got := job.Request.RelationshipObservations[0].ValidFrom; got == nil || got.Format(time.RFC3339) != validFromText {
		t.Fatalf("valid_from = %#v", got)
	}
	if got := job.Request.RelationshipObservations[0].ValidTo; got == nil || got.Format(time.RFC3339) != validToText {
		t.Fatalf("valid_to = %#v", got)
	}
	if _, validationErrs := verifier.PrepareV2SemanticReviewRequest(job.Request); len(validationErrs) > 0 {
		t.Fatalf("prepared request validation errors = %#v", validationErrs)
	}
}

func TestV2SemanticPlacementReviewSourceRejectsInvalidValidityWindow(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	content := "Dense-Mem used PostgreSQL."
	ledger := &v2ReviewSourceLedgerStub{placement: &repository.V2CreateIngestResult{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		Proposal: map[string]any{
			"entity_hints": []map[string]any{
				{"ref": "project_1", "name": "Dense-Mem", "entity_kind": "project"},
				{"ref": "db_1", "name": "PostgreSQL", "entity_kind": "project"},
			},
			"relationship_hints": []map[string]any{{
				"proposal_id": "rel:uses",
				"subject_ref": "project_1",
				"predicate":   "uses",
				"object_ref":  "db_1",
				"valid_from":  "2026-12-31T00:00:00Z",
				"valid_to":    "2026-07-01T00:00:00Z",
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
	}}
	provider := &v2ReviewSourceProposalProviderStub{
		proposal: verifier.V2ProviderProposal{
			PredicateOptions: []string{"uses"},
			EntityProposals: []verifier.V2ProviderEntityProposal{{
				Ref:        "project_1",
				Name:       "Dense-Mem",
				EntityKind: "project",
				Evidence:   []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString("Dense-Mem")}},
			}, {
				Ref:        "db_1",
				Name:       "PostgreSQL",
				EntityKind: "project",
				Evidence: []verifier.V2ProviderEvidenceSpan{{
					EvidenceIndex: 0,
					Start:         utf8.RuneCountInString("Dense-Mem uses "),
					End:           utf8.RuneCountInString("Dense-Mem uses PostgreSQL"),
				}},
			}},
			RelationshipProposals: []verifier.V2ProviderRelationshipProposal{{
				ProposalID:        "rel:uses",
				SubjectRef:        "project_1",
				OriginalPredicate: "uses",
				PredicateCandidates: []string{
					"uses",
				},
				RelationshipKind: "state",
				ObjectRef:        "db_1",
				Polarity:         "+",
				Modality:         "statement",
				ValidFrom:        v2SemanticReviewSourceStringPtr("2026-12-31T00:00:00Z"),
				ValidTo:          v2SemanticReviewSourceStringPtr("2026-07-01T00:00:00Z"),
				Evidence:         []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString(content)}},
			}},
		},
	}
	source := NewV2SemanticPlacementReviewSource(V2SemanticPlacementReviewSourceDependencies{
		Ledger:           ledger,
		Catalog:          &v2ReviewSourceCatalogStub{predicateOptions: []string{"uses"}},
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
	if len(job.RetryableValidationErrors) != 1 || job.RetryableValidationErrors[0].Field != "relationship_proposals[0].valid_to" {
		t.Fatalf("retryable validation errors = %#v", job.RetryableValidationErrors)
	}
}

func TestV2SemanticPlacementReviewSourceRequiresProviderWhenProposalAbsent(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
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
			Content:       "Dense-Mem uses PostgreSQL.",
			ContentHash:   "sha256:current",
		}},
		Items: []repository.V2PlacementItem{{
			PlacementItemID: itemID,
			EvidenceIndex:   0,
			Status:          "queued",
		}},
	}}
	source := NewV2SemanticPlacementReviewSource(V2SemanticPlacementReviewSourceDependencies{
		Ledger:  ledger,
		Catalog: &v2ReviewSourceCatalogStub{},
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
	if len(job.ValidationErrors) != 1 || job.ValidationErrors[0].Field != "proposal" {
		t.Fatalf("validation errors = %#v", job.ValidationErrors)
	}
}

func TestV2SemanticPlacementReviewSourceRetriesInvalidProviderProposal(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	content := "Dense-Mem uses PostgreSQL."
	ledger := &v2ReviewSourceLedgerStub{placement: &repository.V2CreateIngestResult{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		Proposal:       map[string]any{},
		Evidence: []repository.V2EvidenceFragment{{
			FragmentID:       uuid.NewString(),
			EvidenceIndex:    0,
			Content:          content,
			ContentHash:      "sha256:current",
			SourceRevisionID: uuid.NewString(),
		}},
		Items: []repository.V2PlacementItem{{
			PlacementItemID: itemID,
			EvidenceIndex:   0,
			Status:          "queued",
		}},
	}}
	catalog := &v2ReviewSourceCatalogStub{
		predicateOptions: []string{"uses"},
		predicateCandidates: map[string][]repository.V2SemanticReviewPredicateCandidate{
			"uses": {{
				PredicateKey:        "uses",
				Version:             1,
				AllowedSubjectKinds: []string{"project"},
				AllowedObjectKinds:  []string{"project"},
				RelationshipKind:    "state",
				CurrentCardinality:  "many",
				LifecycleState:      "active",
			}},
		},
	}
	invalid := verifier.V2ProviderProposal{
		EntityProposals: []verifier.V2ProviderEntityProposal{{
			Ref:        "project_1",
			Name:       "Dense-Mem",
			EntityKind: "project",
			Evidence:   []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 2, Start: 0, End: 1}},
		}},
	}
	valid := verifier.V2ProviderProposal{
		PredicateOptions: []string{"uses"},
		EntityProposals: []verifier.V2ProviderEntityProposal{{
			Ref:        "project_1",
			Name:       "Dense-Mem",
			EntityKind: "project",
			Evidence:   []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString("Dense-Mem")}},
		}, {
			Ref:        "db_1",
			Name:       "PostgreSQL",
			EntityKind: "project",
			Evidence: []verifier.V2ProviderEvidenceSpan{{
				EvidenceIndex: 0,
				Start:         utf8.RuneCountInString("Dense-Mem uses "),
				End:           utf8.RuneCountInString("Dense-Mem uses PostgreSQL"),
			}},
		}},
		RelationshipProposals: []verifier.V2ProviderRelationshipProposal{{
			ProposalID:        "rel:uses",
			SubjectRef:        "project_1",
			OriginalPredicate: "uses",
			PredicateCandidates: []string{
				"uses",
			},
			RelationshipKind: "state",
			ObjectRef:        "db_1",
			Polarity:         "+",
			Modality:         "statement",
			Evidence:         []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString(content)}},
		}},
	}
	provider := &v2ReviewSourceProposalProviderStub{proposals: []verifier.V2ProviderProposal{invalid, valid}}
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
	if len(provider.reqs) != 2 {
		t.Fatalf("provider attempts = %d, requests=%#v", len(provider.reqs), provider.reqs)
	}
	if provider.reqs[0].Attempt != 1 || provider.reqs[1].Attempt != 2 || len(provider.reqs[1].ValidationFeedback) == 0 {
		t.Fatalf("provider retry requests = %#v", provider.reqs)
	}
	if len(job.ValidationErrors) != 0 || len(job.Request.RelationshipObservations) != 1 {
		t.Fatalf("job = %#v", job)
	}
}

func TestV2SemanticPlacementReviewSourceRetriesMalformedProviderProposal(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	content := "Dense-Mem uses PostgreSQL."
	ledger := &v2ReviewSourceLedgerStub{placement: &repository.V2CreateIngestResult{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		Proposal:       map[string]any{},
		Evidence: []repository.V2EvidenceFragment{{
			FragmentID:       uuid.NewString(),
			EvidenceIndex:    0,
			Content:          content,
			ContentHash:      "sha256:current",
			SourceRevisionID: uuid.NewString(),
		}},
		Items: []repository.V2PlacementItem{{
			PlacementItemID: itemID,
			EvidenceIndex:   0,
			Status:          "queued",
		}},
	}}
	catalog := &v2ReviewSourceCatalogStub{
		predicateOptions: []string{"uses"},
		predicateCandidates: map[string][]repository.V2SemanticReviewPredicateCandidate{
			"uses": {{
				PredicateKey:        "uses",
				Version:             1,
				AllowedSubjectKinds: []string{"project"},
				AllowedObjectKinds:  []string{"project"},
				RelationshipKind:    "state",
				CurrentCardinality:  "many",
				LifecycleState:      "active",
			}},
		},
	}
	valid := verifier.V2ProviderProposal{
		PredicateOptions: []string{"uses"},
		EntityProposals: []verifier.V2ProviderEntityProposal{{
			Ref:        "project_1",
			Name:       "Dense-Mem",
			EntityKind: "project",
			Evidence:   []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString("Dense-Mem")}},
		}, {
			Ref:        "db_1",
			Name:       "PostgreSQL",
			EntityKind: "project",
			Evidence: []verifier.V2ProviderEvidenceSpan{{
				EvidenceIndex: 0,
				Start:         utf8.RuneCountInString("Dense-Mem uses "),
				End:           utf8.RuneCountInString("Dense-Mem uses PostgreSQL"),
			}},
		}},
		RelationshipProposals: []verifier.V2ProviderRelationshipProposal{{
			ProposalID:        "rel:uses",
			SubjectRef:        "project_1",
			OriginalPredicate: "uses",
			PredicateCandidates: []string{
				"uses",
			},
			RelationshipKind: "state",
			ObjectRef:        "db_1",
			Polarity:         "+",
			Modality:         "statement",
			Evidence:         []verifier.V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: utf8.RuneCountInString(content)}},
		}},
	}
	provider := &v2ReviewSourceProposalProviderStub{
		errs: []error{
			&verifier.MalformedResponseError{Provider: "stub", Message: "bad structured output"},
			nil,
		},
		proposals: []verifier.V2ProviderProposal{{}, valid},
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
		MaxAttempts:    5,
	})
	if err != nil {
		t.Fatalf("BuildV2SemanticReviewJob returned error: %v", err)
	}
	if len(provider.reqs) != 2 {
		t.Fatalf("provider attempts = %d, requests=%#v", len(provider.reqs), provider.reqs)
	}
	if provider.reqs[1].Attempt != 2 ||
		len(provider.reqs[1].ValidationFeedback) != 1 ||
		!strings.Contains(provider.reqs[1].ValidationFeedback[0], "provider_proposal") ||
		!strings.Contains(provider.reqs[1].ValidationFeedback[0], "malformed structured response") {
		t.Fatalf("provider retry requests = %#v", provider.reqs)
	}
	if len(job.ValidationErrors) != 0 || len(job.Request.RelationshipObservations) != 1 {
		t.Fatalf("job = %#v", job)
	}
}

func TestV2SemanticPlacementReviewSourceReturnsRetryableProviderProposalAtLimit(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
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
			Content:       "Dense-Mem uses PostgreSQL.",
			ContentHash:   "sha256:current",
		}},
		Items: []repository.V2PlacementItem{{
			PlacementItemID: itemID,
			EvidenceIndex:   0,
			Status:          "queued",
		}},
	}}
	provider := &v2ReviewSourceProposalProviderStub{
		err: &verifier.MalformedResponseError{Provider: "stub", Message: "bad structured output"},
	}
	source := NewV2SemanticPlacementReviewSource(V2SemanticPlacementReviewSourceDependencies{
		Ledger:           ledger,
		Catalog:          &v2ReviewSourceCatalogStub{predicateOptions: []string{"uses"}},
		ProposalProvider: provider,
	})

	job, err := source.BuildV2SemanticReviewJob(context.Background(), repository.V2PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		MaxAttempts:    2,
	})
	if err != nil {
		t.Fatalf("BuildV2SemanticReviewJob returned error: %v", err)
	}
	if len(provider.reqs) != 5 || provider.reqs[4].Attempt != 5 {
		t.Fatalf("provider requests = %#v", provider.reqs)
	}
	if len(job.ValidationErrors) != 0 {
		t.Fatalf("terminal validation errors = %#v", job.ValidationErrors)
	}
	if len(job.RetryableValidationErrors) != 1 || job.RetryableValidationErrors[0].Field != "provider_proposal" {
		t.Fatalf("retryable validation errors = %#v", job.RetryableValidationErrors)
	}
	if job.FailureStage != v2SemanticFailureStageExtraction || job.FailureClass != v2SemanticFailureClassMalformedResponse {
		t.Fatalf("failure = %s/%s", job.FailureStage, job.FailureClass)
	}
}

func TestV2SemanticPlacementReviewSourceClassifiesPredicateCatalogLookupFailure(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
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
			Content:       "Dense-Mem uses PostgreSQL.",
			ContentHash:   "sha256:current",
		}},
		Items: []repository.V2PlacementItem{{
			PlacementItemID: itemID,
			EvidenceIndex:   0,
			Status:          "queued",
		}},
	}}
	provider := &v2ReviewSourceProposalProviderStub{}
	source := NewV2SemanticPlacementReviewSource(V2SemanticPlacementReviewSourceDependencies{
		Ledger: ledger,
		Catalog: &v2ReviewSourceCatalogStub{
			predicateOptionsErr: errors.New("postgres unavailable"),
		},
		ProposalProvider: provider,
	})

	job, err := source.BuildV2SemanticReviewJob(context.Background(), repository.V2PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingestID,
		PlacementRunID: runID,
		Status:         "processing",
		MaxAttempts:    2,
	})
	if err != nil {
		t.Fatalf("BuildV2SemanticReviewJob returned error: %v", err)
	}
	if len(provider.reqs) != 0 {
		t.Fatalf("provider requests = %#v", provider.reqs)
	}
	if len(job.RetryableValidationErrors) != 1 || job.RetryableValidationErrors[0].Field != "provider_proposal" {
		t.Fatalf("retryable validation errors = %#v", job.RetryableValidationErrors)
	}
	if job.FailureStage != v2SemanticFailureStagePredicateCatalog || job.FailureClass != v2SemanticFailureClassLookupFailed {
		t.Fatalf("failure = %s/%s", job.FailureStage, job.FailureClass)
	}
}

func TestV2PlacementReviewSourceHelperCoercions(t *testing.T) {
	raw := map[string]any{
		"object_value": map[string]any{
			"type":    "number",
			"value":   42,
			"display": "42%",
			"unit":    "percent",
		},
	}
	value, ok := v2PlacementReviewObjectValue(raw, "rel:score")
	if !ok || value.Ref != "value:rel:score" || value.Type != "number" || value.Value != "42" ||
		value.Display != "42%" || value.Unit != "percent" {
		t.Fatalf("object value = %#v, ok=%v", value, ok)
	}
	value, ok = v2PlacementReviewObjectValue(map[string]any{
		"object_value": map[string]any{
			"ref":   v2ReviewStringer(" value:explicit "),
			"type":  "boolean",
			"value": true,
		},
	}, "rel:flag")
	if !ok || value.Ref != "value:explicit" || value.Value != "true" {
		t.Fatalf("stringer/bool object value = %#v, ok=%v", value, ok)
	}
	if _, ok := v2PlacementReviewObjectValue(map[string]any{"object_value": map[string]any{"type": "date"}}, "rel:missing"); ok {
		t.Fatal("object value accepted missing value")
	}

	maps := v2PlacementReviewObjectArray(map[string]any{
		"relationship_hints": []any{
			map[string]any{"proposal_id": "rel:one"},
			"ignored",
		},
	}, "relationships", "relationship_hints")
	if len(maps) != 1 || maps[0]["proposal_id"] != "rel:one" {
		t.Fatalf("object array = %#v", maps)
	}
	if got := v2PlacementReviewObjectArray(map[string]any{"relationships": []map[string]any{{"proposal_id": "rel:typed"}}}, "relationships"); len(got) != 1 {
		t.Fatalf("typed object array = %#v", got)
	}
	if got := v2PlacementReviewObjectArray(map[string]any{}, "relationships"); got != nil {
		t.Fatalf("missing object array = %#v", got)
	}

	if got := v2ReviewAnyString(float64(12.5)); got != "12.5" {
		t.Fatalf("float string = %q", got)
	}
	if got := v2ReviewAnyString(int64(7)); got != "7" {
		t.Fatalf("int64 string = %q", got)
	}
	if got := v2ReviewAnyString(struct{}{}); got != "" {
		t.Fatalf("unsupported string = %q", got)
	}
	if got, ok := v2ReviewInt(map[string]any{"n": int64(9)}, "n"); !ok || got != 9 {
		t.Fatalf("int64 coercion = %d, ok=%v", got, ok)
	}
	if got, ok := v2ReviewInt(map[string]any{"n": float64(3)}, "n"); !ok || got != 3 {
		t.Fatalf("float coercion = %d, ok=%v", got, ok)
	}
	if _, ok := v2ReviewInt(nil, "n"); ok {
		t.Fatal("nil fields produced int")
	}

	now := time.Date(2026, 7, 19, 22, 0, 0, 123, time.Local)
	parsed, err := v2ReviewOptionalTime(map[string]any{"at": now}, "at")
	if err != nil || parsed == nil || !parsed.Equal(now.UTC()) {
		t.Fatalf("time value = %#v, err=%v", parsed, err)
	}
	parsed, err = v2ReviewOptionalTime(map[string]any{"at": &now}, "at")
	if err != nil || parsed == nil || !parsed.Equal(now.UTC()) {
		t.Fatalf("time pointer = %#v, err=%v", parsed, err)
	}
	parsed, err = v2ReviewOptionalTime(map[string]any{"at": ""}, "at")
	if err != nil || parsed != nil {
		t.Fatalf("empty time = %#v, err=%v", parsed, err)
	}
	if _, err := v2ReviewOptionalTime(map[string]any{"at": 12}, "at"); err == nil {
		t.Fatal("numeric time was accepted")
	}
}
