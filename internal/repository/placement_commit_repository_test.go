package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestPlacementCommitInputValidationCoversRefAndValueObservations(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	fragmentID := uuid.NewString()
	input := normalizeCommitPlacementSemanticInput(CommitPlacementSemanticInput{
		TeamID:           " " + teamID + " ",
		OwnerProfileID:   ownerID,
		IngestID:         ingestID,
		PlacementRunID:   runID,
		PlacementItemID:  itemID,
		WorkerID:         " worker-1 ",
		ExpectedAttempts: 1,
		EntityResolutions: []PlacementEntityResolutionInput{
			{
				MentionRef: " subject ",
				Action:     string(domain.EntityResolutionReuse),
				EntityID:   uuid.NewString(),
			},
			{
				MentionRef:    " object ",
				Action:        string(domain.EntityResolutionCreate),
				EntityKind:    "project",
				CanonicalName: "Dense-Mem",
			},
		},
		RelationshipObservations: []PlacementRelationshipDecisionInput{{
			Ref:          " rel-1 ",
			SubjectRef:   " subject ",
			PredicateKey: "released",
			ObjectValue: &PlacementValueInput{
				Ref:            " release-date ",
				ValueType:      "date",
				CanonicalValue: "2026-07-17",
			},
			Support: &EvidenceSupportInput{
				FragmentID:     fragmentID,
				SourceGroupKey: "conversation:test",
				SpanStart:      0,
				SpanEnd:        10,
			},
		}},
	})

	if err := validateCommitPlacementSemanticInput(input); err != nil {
		t.Fatalf("validateCommitPlacementSemanticInput returned error: %v", err)
	}
	if input.WorkerID != "worker-1" || input.OutcomeKind != "semantic_commit" || input.Status != string(domain.SemanticReviewAccepted) {
		t.Fatalf("normalized commit input = %#v", input)
	}
	if input.RelationshipObservations[0].ObjectValue.Ref != "release-date" || input.RelationshipObservations[0].Support.Authority != "primary" {
		t.Fatalf("normalized relationship = %#v", input.RelationshipObservations[0])
	}
}

func TestRelationshipTransitionIdempotencyKeyPrefersVerificationEvent(t *testing.T) {
	verificationID := uuid.NewString()
	supportDecisionID := uuid.NewString()

	got := relationshipTransitionIdempotencyKey(" "+verificationID+" ", supportDecisionID)
	if got != "verification:"+verificationID+":relationship_transition" {
		t.Fatalf("verification transition idempotency key = %q", got)
	}
	got = relationshipTransitionIdempotencyKey("", " "+supportDecisionID+" ")
	if got != "support_decision:"+supportDecisionID+":relationship_transition" {
		t.Fatalf("support transition idempotency key = %q", got)
	}
	if got = relationshipTransitionIdempotencyKey("", ""); got != "" {
		t.Fatalf("empty transition idempotency key = %q", got)
	}
}

func TestPlacementCommitValidationRejectsBadShapes(t *testing.T) {
	tests := []struct {
		name  string
		input CommitPlacementSemanticInput
	}{
		{
			name: "missing worker",
			input: CommitPlacementSemanticInput{
				TeamID:           uuid.NewString(),
				OwnerProfileID:   uuid.NewString(),
				IngestID:         uuid.NewString(),
				PlacementRunID:   uuid.NewString(),
				PlacementItemID:  uuid.NewString(),
				ExpectedAttempts: 1,
			},
		},
		{
			name: "invalid category",
			input: CommitPlacementSemanticInput{
				TeamID:           uuid.NewString(),
				OwnerProfileID:   uuid.NewString(),
				IngestID:         uuid.NewString(),
				PlacementRunID:   uuid.NewString(),
				PlacementItemID:  uuid.NewString(),
				WorkerID:         "worker-1",
				ExpectedAttempts: 1,
				Category:         "unknown",
			},
		},
		{
			name: "ambiguous object endpoint",
			input: CommitPlacementSemanticInput{
				TeamID:           uuid.NewString(),
				OwnerProfileID:   uuid.NewString(),
				IngestID:         uuid.NewString(),
				PlacementRunID:   uuid.NewString(),
				PlacementItemID:  uuid.NewString(),
				WorkerID:         "worker-1",
				ExpectedAttempts: 1,
				RelationshipObservations: []PlacementRelationshipDecisionInput{{
					SubjectRef:   "subject",
					PredicateKey: "works_on",
					ObjectRef:    "object",
					ObjectValue: &PlacementValueInput{
						ValueType:      "date",
						CanonicalValue: "2026-07-17",
					},
				}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := normalizeCommitPlacementSemanticInput(tt.input)
			if err := validateCommitPlacementSemanticInput(input); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRelationshipDecisionFromPlacementObservationResolvesEntityRefs(t *testing.T) {
	commit := CommitPlacementSemanticInput{
		TeamID:          uuid.NewString(),
		OwnerProfileID:  uuid.NewString(),
		IngestID:        uuid.NewString(),
		PlacementItemID: uuid.NewString(),
	}
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	confidence := 0.9
	decision, err := relationshipDecisionFromPlacementObservation(nil, nil, commit, PlacementRelationshipDecisionInput{
		Ref:               "rel-1",
		SubjectRef:        "subject",
		OriginalPredicate: "works on",
		PredicateKey:      "works_on",
		PredicateVersion:  1,
		ObjectRef:         "object",
		EvidenceVerdict:   string(domain.VerificationEntailed),
		Confidence:        &confidence,
		Support: &EvidenceSupportInput{
			FragmentID:     uuid.NewString(),
			SourceGroupKey: "conversation:mapper",
			SpanStart:      0,
			SpanEnd:        12,
			Authority:      "primary",
		},
	}, map[string]string{
		"subject": subjectID,
		"object":  objectID,
	})
	if err != nil {
		t.Fatalf("relationshipDecisionFromPlacementObservation returned error: %v", err)
	}
	if decision.SubjectEntityID != subjectID || decision.ObjectEntityID != objectID || decision.TeamID != commit.TeamID {
		t.Fatalf("decision = %#v", decision)
	}
	if _, err := relationshipDecisionFromPlacementObservation(nil, nil, commit, PlacementRelationshipDecisionInput{
		Ref:          "rel-1",
		SubjectRef:   "missing",
		PredicateKey: "works_on",
		ObjectRef:    "object",
	}, map[string]string{"object": objectID}); err == nil {
		t.Fatal("expected missing subject error")
	}
	if _, err := relationshipDecisionFromPlacementObservation(nil, nil, commit, PlacementRelationshipDecisionInput{
		Ref:          "rel-1",
		SubjectRef:   "subject",
		PredicateKey: "works_on",
		ObjectRef:    "missing",
	}, map[string]string{"subject": subjectID}); err == nil {
		t.Fatal("expected missing object error")
	}
}

func TestPlacementPreTransactionErrorPaths(t *testing.T) {
	repo := &LedgerRepositoryImpl{}
	if _, err := repo.CommitPlacementSemanticResult(context.Background(), CommitPlacementSemanticInput{}); err == nil {
		t.Fatal("expected invalid semantic commit input error")
	}
	if _, err := repo.CompletePlacementReviewResult(context.Background(), CompletePlacementReviewInput{}); err == nil {
		t.Fatal("expected invalid terminal review input error")
	}
	_, _, err := insertPlacementEntityResolution(context.Background(), nil, CommitPlacementSemanticInput{}, PlacementEntityResolutionInput{
		MentionRef:     "subject",
		Action:         string(domain.EntityResolutionReuse),
		EntityID:       uuid.NewString(),
		VerifierResult: map[string]any{"bad": func() {}},
	})
	if err == nil {
		t.Fatal("expected verifier result marshal error")
	}
	_, err = upsertPlacementValue(context.Background(), nil, CommitPlacementSemanticInput{}, PlacementValueInput{
		ValueType:      "string",
		CanonicalValue: "value",
		Metadata:       map[string]any{"bad": func() {}},
	})
	if err == nil {
		t.Fatal("expected value metadata marshal error")
	}
	_, err = upsertSearchDocumentInTx(context.Background(), nil, UpsertSearchDocumentInput{
		Metadata: map[string]any{"bad": func() {}},
	}, &ActiveSearchContract{}, defaultEmbeddingJobMaxAttempts)
	if err == nil {
		t.Fatal("expected search metadata marshal error")
	}
}

func TestRelationshipDecisionFromPlacementObservationValueErrorPath(t *testing.T) {
	commit := CommitPlacementSemanticInput{
		TeamID:          uuid.NewString(),
		OwnerProfileID:  uuid.NewString(),
		IngestID:        uuid.NewString(),
		PlacementItemID: uuid.NewString(),
	}
	_, err := relationshipDecisionFromPlacementObservation(context.Background(), nil, commit, PlacementRelationshipDecisionInput{
		Ref:          "rel-1",
		SubjectRef:   "subject",
		PredicateKey: "released",
		ObjectValue: &PlacementValueInput{
			ValueType:      "date",
			CanonicalValue: "2026-07-17",
			Metadata:       map[string]any{"bad": func() {}},
		},
		Support: &EvidenceSupportInput{
			FragmentID:     uuid.NewString(),
			SourceGroupKey: "conversation:value",
			SpanStart:      0,
			SpanEnd:        12,
			Authority:      "primary",
		},
	}, map[string]string{"subject": uuid.NewString()})
	if err == nil {
		t.Fatal("expected value upsert error")
	}
}

func TestPlacementValidationBranchCoverage(t *testing.T) {
	if err := validatePlacementEntityResolutionInput(PlacementEntityResolutionInput{
		MentionRef: "ambiguous",
		Action:     string(domain.EntityResolutionAmbiguous),
	}); err != nil {
		t.Fatalf("ambiguous resolution should validate: %v", err)
	}
	if err := validatePlacementEntityResolutionInput(PlacementEntityResolutionInput{
		MentionRef:    "created",
		Action:        string(domain.EntityResolutionCreate),
		EntityKind:    "unsupported",
		CanonicalName: "Name",
	}); err == nil {
		t.Fatal("expected unsupported entity kind error")
	}
	if err := validatePlacementValueInput(PlacementValueInput{ValueType: "date"}); err == nil {
		t.Fatal("expected missing canonical value error")
	}
	if err := validatePlacementValueInput(PlacementValueInput{ValueType: "date", CanonicalValue: "2026-07-17", NormalizationVersion: -1}); err == nil {
		t.Fatal("expected invalid normalization version error")
	}
	validFrom := time.Now().UTC()
	validTo := validFrom.Add(-time.Hour)
	if err := validatePlacementRelationshipDecisionInput(PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "+",
		EvidenceVerdict: string(domain.VerificationInsufficient),
		ValidFrom:       &validFrom,
		ValidTo:         &validTo,
	}); err == nil {
		t.Fatal("expected invalid validity window error")
	}
	highConfidence := 1.1
	if err := validatePlacementRelationshipDecisionInput(PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "+",
		EvidenceVerdict: string(domain.VerificationInsufficient),
		Confidence:      &highConfidence,
	}); err == nil {
		t.Fatal("expected confidence range error")
	}
	if err := validatePlacementRelationshipDecisionInput(PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "?",
		EvidenceVerdict: string(domain.VerificationInsufficient),
	}); err == nil {
		t.Fatal("expected polarity error")
	}
	if err := validatePlacementRelationshipReviewInput(PlacementRelationshipReviewInput{
		Ref:               "relationship-review",
		SubjectRef:        "subject",
		OriginalPredicate: "works on",
		ObjectRef:         "object",
		Polarity:          "?",
		EvidenceVerdict:   string(domain.VerificationInsufficient),
	}); err == nil {
		t.Fatal("expected relationship review polarity error")
	}
	if err := validatePlacementRelationshipDecisionInput(PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "+",
		EvidenceVerdict: string(domain.VerificationInsufficient),
		CorrectionTarget: &PlacementCorrectionTargetInput{
			RelationshipID:  "not-a-uuid",
			ExpectedVersion: 1,
		},
	}); err == nil {
		t.Fatal("expected correction target relationship_id error")
	}
	if err := validatePlacementRelationshipDecisionInput(PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "+",
		EvidenceVerdict: string(domain.VerificationInsufficient),
		CorrectionTarget: &PlacementCorrectionTargetInput{
			RelationshipID:  uuid.NewString(),
			ExpectedVersion: 0,
		},
	}); err == nil {
		t.Fatal("expected correction target version error")
	}
}

func TestPlacementReviewCompletionValidationAndStatusMapping(t *testing.T) {
	input := normalizeCompletePlacementReviewInput(CompletePlacementReviewInput{
		TeamID:           uuid.NewString(),
		OwnerProfileID:   uuid.NewString(),
		IngestID:         uuid.NewString(),
		PlacementRunID:   uuid.NewString(),
		PlacementItemID:  uuid.NewString(),
		WorkerID:         " worker-1 ",
		ExpectedAttempts: 2,
		Status:           string(domain.SemanticReviewQuarantined),
		Payload: map[string]any{
			"reason": "security signal",
		},
	})
	if err := validateCompletePlacementReviewInput(input); err != nil {
		t.Fatalf("validateCompletePlacementReviewInput returned error: %v", err)
	}
	if input.OutcomeKind != "semantic_review_terminal" || input.WorkerID != "worker-1" {
		t.Fatalf("normalized terminal input = %#v", input)
	}
	itemStatus, category, runStatus := terminalPlacementStatuses(input.Status, input.Category)
	if itemStatus != "quarantined" || category != "quarantined" || runStatus != string(domain.PlacementRunQuarantined) {
		t.Fatalf("terminal statuses = %q/%q/%q", itemStatus, category, runStatus)
	}
	itemStatus, category, runStatus = terminalPlacementStatuses(string(domain.SemanticReviewTerminalFailure), "")
	if itemStatus != "failed" || category != "failed" || runStatus != string(domain.PlacementRunFailed) {
		t.Fatalf("failed statuses = %q/%q/%q", itemStatus, category, runStatus)
	}
	itemStatus, category, runStatus = terminalPlacementStatuses(string(domain.SemanticReviewReviewRequired), "fragment_only")
	if itemStatus != "completed" || category != "fragment_only" || runStatus != string(domain.PlacementRunCompleted) {
		t.Fatalf("review statuses = %q/%q/%q", itemStatus, category, runStatus)
	}
	payload := terminalPlacementPayload(input.Payload, input.Status)
	if payload["contract_version"] != domain.ContractVersion || payload["status"] != input.Status || payload["reason"] != "security signal" {
		t.Fatalf("payload = %#v", payload)
	}
	scope := placementCommitScope(input)
	if scope.WorkerID != "worker-1" || scope.ExpectedAttempts != 2 {
		t.Fatalf("scope = %#v", scope)
	}
}

func TestPlacementPayloadAndSearchTextHelpers(t *testing.T) {
	relationshipID := uuid.NewString()
	searchDocumentID := uuid.NewString()
	embeddingJobID := uuid.NewString()
	result := &CommitPlacementSemanticResult{
		EntityResolutionIDs: []string{uuid.NewString()},
		RelationshipResults: []RelationshipDecisionResult{{
			Relationship: &RelationshipRecord{
				RelationshipID: relationshipID,
				OwnerProfileID: "profile-1",
				Status:         string(domain.RelationshipStatusActive),
			},
			ObservationID:  "obs-1",
			ProposalID:     "rel:authority",
			OwnerProfileID: "profile-1",
			Category:       string(domain.OutcomeRelationshipAccepted),
			Reason:         "accepted",
		}},
		SearchDocuments: []SearchDocumentResult{{
			SearchDocumentID: searchDocumentID,
			QueuedJobID:      embeddingJobID,
		}},
	}
	payload := placementCommitPayload(map[string]any{"request_id": "req-1"}, result)
	if payload["contract_version"] != domain.ContractVersion || payload["request_id"] != "req-1" {
		t.Fatalf("payload = %#v", payload)
	}
	if got := payload["relationship_ids"].([]string); len(got) != 1 || got[0] != relationshipID {
		t.Fatalf("relationship ids = %#v", got)
	}
	outcomes := payload["relationship_outcomes"].([]map[string]any)
	if len(outcomes) != 1 || outcomes[0]["owner_profile_id"] != "profile-1" || outcomes[0]["category"] != string(domain.OutcomeRelationshipAccepted) {
		t.Fatalf("relationship outcomes = %#v", outcomes)
	}
	text := relationshipProjectionText(&RelationshipRecord{
		SubjectEntityID: "subject",
		PredicateKey:    "works_on",
		ObjectEntityID:  "object",
		Polarity:        "+",
	}, relationshipProjectionNames{
		SubjectName: "Mark Huang",
		ObjectName:  "Dense-Mem",
	})
	if text != "relationship\nsubject: Mark Huang\npredicate: works on\nobject: Dense-Mem\npolarity: positive" {
		t.Fatalf("search text = %q", text)
	}
}

func TestPlacementSmallHelpers(t *testing.T) {
	value := 7
	if got := intPointerArg(&value); got != 7 {
		t.Fatalf("intPointerArg value = %#v", got)
	}
	if got := intPointerArg(nil); got != nil {
		t.Fatalf("intPointerArg nil = %#v", got)
	}
	scoped := withPlacementDecisionScope(CommitPlacementSemanticInput{
		TeamID:          "team",
		OwnerProfileID:  "owner",
		IngestID:        "ingest",
		PlacementItemID: "item",
	}, ApplyRelationshipDecisionInput{PredicateKey: "works_on"})
	if scoped.TeamID != "team" || scoped.OwnerProfileID != "owner" || scoped.IngestID != "ingest" || scoped.PlacementItemID != "item" {
		t.Fatalf("scoped decision = %#v", scoped)
	}
	for category, want := range map[string]string{
		string(domain.OutcomeRelationshipNeedsReview): string(domain.OutcomeRelationshipNeedsReview),
		string(domain.OutcomePredicateNeedsReview):    string(domain.OutcomePredicateNeedsReview),
		string(domain.OutcomeIdentityNeedsReview):     string(domain.OutcomeIdentityNeedsReview),
	} {
		got := relationshipOutcomeCategory(&RelationshipDecisionResult{
			ReviewTaskID: "review",
			Category:     category,
		})
		if got != want {
			t.Fatalf("review category %q = %q, want %q", category, got, want)
		}
	}
}

func TestPlacementCorrectionTargetRelated(t *testing.T) {
	source := &RelationshipRecord{
		SubjectEntityID: "subject-a",
		PredicateKey:    "uses",
		ObjectEntityID:  "object-a",
	}
	if !placementCorrectionTargetRelated(source, placementCorrectionTargetRecord{
		SubjectEntityID: "subject-a",
		PredicateKey:    "uses",
		ObjectEntityID:  "object-b",
	}) {
		t.Fatal("expected matching subject and predicate to be related")
	}
	if !placementCorrectionTargetRelated(source, placementCorrectionTargetRecord{
		SubjectEntityID: "subject-b",
		PredicateKey:    "uses",
		ObjectEntityID:  "object-a",
	}) {
		t.Fatal("expected matching object and predicate to be related")
	}
	if placementCorrectionTargetRelated(source, placementCorrectionTargetRecord{
		SubjectEntityID: "subject-a",
		PredicateKey:    "released",
		ObjectEntityID:  "object-a",
	}) {
		t.Fatal("different predicates must not be related")
	}
	if placementCorrectionTargetRelated(source, placementCorrectionTargetRecord{
		SubjectEntityID: "subject-b",
		PredicateKey:    "uses",
		ObjectEntityID:  "object-b",
	}) {
		t.Fatal("same predicate without an endpoint overlap must not be related")
	}
}

func TestPlacementErrorsRemainComparable(t *testing.T) {
	if !errors.Is(ErrPlacementLeaseLost, ErrPlacementLeaseLost) {
		t.Fatal("lease lost error is not comparable")
	}
	if !errors.Is(ErrPlacementStaleSource, ErrPlacementStaleSource) {
		t.Fatal("stale source error is not comparable")
	}
}
