package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestV2PlacementCommitInputValidationCoversRefAndValueObservations(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	itemID := uuid.NewString()
	fragmentID := uuid.NewString()
	input := normalizeV2CommitPlacementSemanticInput(V2CommitPlacementSemanticInput{
		TeamID:           " " + teamID + " ",
		OwnerProfileID:   ownerID,
		IngestID:         ingestID,
		PlacementRunID:   runID,
		PlacementItemID:  itemID,
		WorkerID:         " worker-1 ",
		ExpectedAttempts: 1,
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{
				MentionRef: " subject ",
				Action:     string(domain.V2EntityResolutionReuse),
				EntityID:   uuid.NewString(),
			},
			{
				MentionRef:    " object ",
				Action:        string(domain.V2EntityResolutionCreate),
				EntityKind:    "project",
				CanonicalName: "Dense-Mem",
			},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
			Ref:          " rel-1 ",
			SubjectRef:   " subject ",
			PredicateKey: "released",
			ObjectValue: &V2PlacementValueInput{
				Ref:            " release-date ",
				ValueType:      "date",
				CanonicalValue: "2026-07-17",
			},
			Support: &V2EvidenceSupportInput{
				FragmentID:     fragmentID,
				SourceGroupKey: "conversation:test",
				SpanStart:      0,
				SpanEnd:        10,
			},
		}},
	})

	if err := validateV2CommitPlacementSemanticInput(input); err != nil {
		t.Fatalf("validateV2CommitPlacementSemanticInput returned error: %v", err)
	}
	if input.WorkerID != "worker-1" || input.OutcomeKind != "semantic_commit" || input.Status != string(domain.V2SemanticReviewAccepted) {
		t.Fatalf("normalized commit input = %#v", input)
	}
	if input.RelationshipObservations[0].ObjectValue.Ref != "release-date" || input.RelationshipObservations[0].Support.Authority != "primary" {
		t.Fatalf("normalized relationship = %#v", input.RelationshipObservations[0])
	}
}

func TestV2RelationshipTransitionIdempotencyKeyPrefersVerificationEvent(t *testing.T) {
	verificationID := uuid.NewString()
	supportDecisionID := uuid.NewString()

	got := v2RelationshipTransitionIdempotencyKey(" "+verificationID+" ", supportDecisionID)
	if got != "verification:"+verificationID+":relationship_transition" {
		t.Fatalf("verification transition idempotency key = %q", got)
	}
	got = v2RelationshipTransitionIdempotencyKey("", " "+supportDecisionID+" ")
	if got != "support_decision:"+supportDecisionID+":relationship_transition" {
		t.Fatalf("support transition idempotency key = %q", got)
	}
	if got = v2RelationshipTransitionIdempotencyKey("", ""); got != "" {
		t.Fatalf("empty transition idempotency key = %q", got)
	}
}

func TestV2PlacementCommitValidationRejectsBadShapes(t *testing.T) {
	tests := []struct {
		name  string
		input V2CommitPlacementSemanticInput
	}{
		{
			name: "missing worker",
			input: V2CommitPlacementSemanticInput{
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
			input: V2CommitPlacementSemanticInput{
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
			input: V2CommitPlacementSemanticInput{
				TeamID:           uuid.NewString(),
				OwnerProfileID:   uuid.NewString(),
				IngestID:         uuid.NewString(),
				PlacementRunID:   uuid.NewString(),
				PlacementItemID:  uuid.NewString(),
				WorkerID:         "worker-1",
				ExpectedAttempts: 1,
				RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
					SubjectRef:   "subject",
					PredicateKey: "works_on",
					ObjectRef:    "object",
					ObjectValue: &V2PlacementValueInput{
						ValueType:      "date",
						CanonicalValue: "2026-07-17",
					},
				}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := normalizeV2CommitPlacementSemanticInput(tt.input)
			if err := validateV2CommitPlacementSemanticInput(input); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestV2RelationshipDecisionFromPlacementObservationResolvesEntityRefs(t *testing.T) {
	commit := V2CommitPlacementSemanticInput{
		TeamID:          uuid.NewString(),
		OwnerProfileID:  uuid.NewString(),
		IngestID:        uuid.NewString(),
		PlacementItemID: uuid.NewString(),
	}
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	confidence := 0.9
	decision, err := v2RelationshipDecisionFromPlacementObservation(nil, nil, commit, V2PlacementRelationshipDecisionInput{
		Ref:               "rel-1",
		SubjectRef:        "subject",
		OriginalPredicate: "works on",
		PredicateKey:      "works_on",
		PredicateVersion:  1,
		ObjectRef:         "object",
		EvidenceVerdict:   string(domain.V2VerificationEntailed),
		Confidence:        &confidence,
		Support: &V2EvidenceSupportInput{
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
		t.Fatalf("v2RelationshipDecisionFromPlacementObservation returned error: %v", err)
	}
	if decision.SubjectEntityID != subjectID || decision.ObjectEntityID != objectID || decision.TeamID != commit.TeamID {
		t.Fatalf("decision = %#v", decision)
	}
	if _, err := v2RelationshipDecisionFromPlacementObservation(nil, nil, commit, V2PlacementRelationshipDecisionInput{
		Ref:          "rel-1",
		SubjectRef:   "missing",
		PredicateKey: "works_on",
		ObjectRef:    "object",
	}, map[string]string{"object": objectID}); err == nil {
		t.Fatal("expected missing subject error")
	}
	if _, err := v2RelationshipDecisionFromPlacementObservation(nil, nil, commit, V2PlacementRelationshipDecisionInput{
		Ref:          "rel-1",
		SubjectRef:   "subject",
		PredicateKey: "works_on",
		ObjectRef:    "missing",
	}, map[string]string{"subject": subjectID}); err == nil {
		t.Fatal("expected missing object error")
	}
}

func TestV2PlacementPreTransactionErrorPaths(t *testing.T) {
	repo := &V2LedgerRepositoryImpl{}
	if _, err := repo.CommitPlacementSemanticResult(context.Background(), V2CommitPlacementSemanticInput{}); err == nil {
		t.Fatal("expected invalid semantic commit input error")
	}
	if _, err := repo.CompletePlacementReviewResult(context.Background(), V2CompletePlacementReviewInput{}); err == nil {
		t.Fatal("expected invalid terminal review input error")
	}
	_, _, err := insertV2PlacementEntityResolution(context.Background(), nil, V2CommitPlacementSemanticInput{}, V2PlacementEntityResolutionInput{
		MentionRef:     "subject",
		Action:         string(domain.V2EntityResolutionReuse),
		EntityID:       uuid.NewString(),
		VerifierResult: map[string]any{"bad": func() {}},
	})
	if err == nil {
		t.Fatal("expected verifier result marshal error")
	}
	_, err = upsertV2PlacementValue(context.Background(), nil, V2CommitPlacementSemanticInput{}, V2PlacementValueInput{
		ValueType:      "string",
		CanonicalValue: "value",
		Metadata:       map[string]any{"bad": func() {}},
	})
	if err == nil {
		t.Fatal("expected value metadata marshal error")
	}
	_, err = upsertV2SearchDocumentInTx(context.Background(), nil, V2UpsertSearchDocumentInput{
		Metadata: map[string]any{"bad": func() {}},
	}, &V2ActiveSearchContract{}, defaultV2EmbeddingJobMaxAttempts)
	if err == nil {
		t.Fatal("expected search metadata marshal error")
	}
}

func TestV2RelationshipDecisionFromPlacementObservationValueErrorPath(t *testing.T) {
	commit := V2CommitPlacementSemanticInput{
		TeamID:          uuid.NewString(),
		OwnerProfileID:  uuid.NewString(),
		IngestID:        uuid.NewString(),
		PlacementItemID: uuid.NewString(),
	}
	_, err := v2RelationshipDecisionFromPlacementObservation(context.Background(), nil, commit, V2PlacementRelationshipDecisionInput{
		Ref:          "rel-1",
		SubjectRef:   "subject",
		PredicateKey: "released",
		ObjectValue: &V2PlacementValueInput{
			ValueType:      "date",
			CanonicalValue: "2026-07-17",
			Metadata:       map[string]any{"bad": func() {}},
		},
		Support: &V2EvidenceSupportInput{
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

func TestV2PlacementValidationBranchCoverage(t *testing.T) {
	if err := validateV2PlacementEntityResolutionInput(V2PlacementEntityResolutionInput{
		MentionRef: "ambiguous",
		Action:     string(domain.V2EntityResolutionAmbiguous),
	}); err != nil {
		t.Fatalf("ambiguous resolution should validate: %v", err)
	}
	if err := validateV2PlacementEntityResolutionInput(V2PlacementEntityResolutionInput{
		MentionRef:    "created",
		Action:        string(domain.V2EntityResolutionCreate),
		EntityKind:    "unsupported",
		CanonicalName: "Name",
	}); err == nil {
		t.Fatal("expected unsupported entity kind error")
	}
	if err := validateV2PlacementValueInput(V2PlacementValueInput{ValueType: "date"}); err == nil {
		t.Fatal("expected missing canonical value error")
	}
	if err := validateV2PlacementValueInput(V2PlacementValueInput{ValueType: "date", CanonicalValue: "2026-07-17", NormalizationVersion: -1}); err == nil {
		t.Fatal("expected invalid normalization version error")
	}
	validFrom := time.Now().UTC()
	validTo := validFrom.Add(-time.Hour)
	if err := validateV2PlacementRelationshipDecisionInput(V2PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "+",
		EvidenceVerdict: string(domain.V2VerificationInsufficient),
		ValidFrom:       &validFrom,
		ValidTo:         &validTo,
	}); err == nil {
		t.Fatal("expected invalid validity window error")
	}
	highConfidence := 1.1
	if err := validateV2PlacementRelationshipDecisionInput(V2PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "+",
		EvidenceVerdict: string(domain.V2VerificationInsufficient),
		Confidence:      &highConfidence,
	}); err == nil {
		t.Fatal("expected confidence range error")
	}
	if err := validateV2PlacementRelationshipDecisionInput(V2PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "?",
		EvidenceVerdict: string(domain.V2VerificationInsufficient),
	}); err == nil {
		t.Fatal("expected polarity error")
	}
	if err := validateV2PlacementRelationshipReviewInput(V2PlacementRelationshipReviewInput{
		Ref:               "relationship-review",
		SubjectRef:        "subject",
		OriginalPredicate: "works on",
		ObjectRef:         "object",
		Polarity:          "?",
		EvidenceVerdict:   string(domain.V2VerificationInsufficient),
	}); err == nil {
		t.Fatal("expected relationship review polarity error")
	}
	if err := validateV2PlacementRelationshipDecisionInput(V2PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "+",
		EvidenceVerdict: string(domain.V2VerificationInsufficient),
		CorrectionTarget: &V2PlacementCorrectionTargetInput{
			RelationshipID:  "not-a-uuid",
			ExpectedVersion: 1,
		},
	}); err == nil {
		t.Fatal("expected correction target relationship_id error")
	}
	if err := validateV2PlacementRelationshipDecisionInput(V2PlacementRelationshipDecisionInput{
		SubjectRef:      "subject",
		PredicateKey:    "works_on",
		ObjectRef:       "object",
		Polarity:        "+",
		EvidenceVerdict: string(domain.V2VerificationInsufficient),
		CorrectionTarget: &V2PlacementCorrectionTargetInput{
			RelationshipID:  uuid.NewString(),
			ExpectedVersion: 0,
		},
	}); err == nil {
		t.Fatal("expected correction target version error")
	}
}

func TestV2PlacementReviewCompletionValidationAndStatusMapping(t *testing.T) {
	input := normalizeV2CompletePlacementReviewInput(V2CompletePlacementReviewInput{
		TeamID:           uuid.NewString(),
		OwnerProfileID:   uuid.NewString(),
		IngestID:         uuid.NewString(),
		PlacementRunID:   uuid.NewString(),
		PlacementItemID:  uuid.NewString(),
		WorkerID:         " worker-1 ",
		ExpectedAttempts: 2,
		Status:           string(domain.V2SemanticReviewQuarantined),
		Payload: map[string]any{
			"reason": "security signal",
		},
	})
	if err := validateV2CompletePlacementReviewInput(input); err != nil {
		t.Fatalf("validateV2CompletePlacementReviewInput returned error: %v", err)
	}
	if input.OutcomeKind != "semantic_review_terminal" || input.WorkerID != "worker-1" {
		t.Fatalf("normalized terminal input = %#v", input)
	}
	itemStatus, category, runStatus := v2TerminalPlacementStatuses(input.Status, input.Category)
	if itemStatus != "quarantined" || category != "quarantined" || runStatus != string(domain.V2PlacementRunQuarantined) {
		t.Fatalf("terminal statuses = %q/%q/%q", itemStatus, category, runStatus)
	}
	itemStatus, category, runStatus = v2TerminalPlacementStatuses(string(domain.V2SemanticReviewTerminalFailure), "")
	if itemStatus != "failed" || category != "failed" || runStatus != string(domain.V2PlacementRunFailed) {
		t.Fatalf("failed statuses = %q/%q/%q", itemStatus, category, runStatus)
	}
	itemStatus, category, runStatus = v2TerminalPlacementStatuses(string(domain.V2SemanticReviewReviewRequired), "fragment_only")
	if itemStatus != "completed" || category != "fragment_only" || runStatus != string(domain.V2PlacementRunCompleted) {
		t.Fatalf("review statuses = %q/%q/%q", itemStatus, category, runStatus)
	}
	payload := v2TerminalPlacementPayload(input.Payload, input.Status)
	if payload["contract_version"] != domain.V2ContractVersion || payload["status"] != input.Status || payload["reason"] != "security signal" {
		t.Fatalf("payload = %#v", payload)
	}
	scope := v2PlacementCommitScope(input)
	if scope.WorkerID != "worker-1" || scope.ExpectedAttempts != 2 {
		t.Fatalf("scope = %#v", scope)
	}
}

func TestV2PlacementPayloadAndSearchTextHelpers(t *testing.T) {
	relationshipID := uuid.NewString()
	searchDocumentID := uuid.NewString()
	embeddingJobID := uuid.NewString()
	result := &V2CommitPlacementSemanticResult{
		EntityResolutionIDs: []string{uuid.NewString()},
		RelationshipResults: []V2RelationshipDecisionResult{{
			Relationship: &V2RelationshipRecord{
				RelationshipID: relationshipID,
				OwnerProfileID: "profile-1",
				Tier:           string(domain.V2RelationshipTierFact),
				Status:         string(domain.V2RelationshipStatusActive),
			},
			ObservationID:  "obs-1",
			ProposalID:     "rel:authority",
			OwnerProfileID: "profile-1",
			Category:       string(domain.V2OutcomeRelationshipFact),
			Reason:         "accepted",
		}},
		SearchDocuments: []V2SearchDocumentResult{{
			SearchDocumentID: searchDocumentID,
			QueuedJobID:      embeddingJobID,
		}},
	}
	payload := v2PlacementCommitPayload(map[string]any{"request_id": "req-1"}, result)
	if payload["contract_version"] != domain.V2ContractVersion || payload["request_id"] != "req-1" {
		t.Fatalf("payload = %#v", payload)
	}
	if got := payload["relationship_ids"].([]string); len(got) != 1 || got[0] != relationshipID {
		t.Fatalf("relationship ids = %#v", got)
	}
	outcomes := payload["relationship_outcomes"].([]map[string]any)
	if len(outcomes) != 1 || outcomes[0]["owner_profile_id"] != "profile-1" || outcomes[0]["category"] != string(domain.V2OutcomeRelationshipFact) {
		t.Fatalf("relationship outcomes = %#v", outcomes)
	}
	text := v2PlacementRelationshipSearchText(&V2RelationshipRecord{
		SubjectEntityID:  "subject",
		PredicateKey:     "works_on",
		ObjectEntityID:   "object",
		SemanticGroupKey: "group",
	})
	if text != "relationship works_on subject object  group" {
		t.Fatalf("search text = %q", text)
	}
}

func TestV2PlacementSmallHelpers(t *testing.T) {
	value := 7
	if got := v2IntPointerArg(&value); got != 7 {
		t.Fatalf("v2IntPointerArg value = %#v", got)
	}
	if got := v2IntPointerArg(nil); got != nil {
		t.Fatalf("v2IntPointerArg nil = %#v", got)
	}
	scoped := withV2PlacementDecisionScope(V2CommitPlacementSemanticInput{
		TeamID:          "team",
		OwnerProfileID:  "owner",
		IngestID:        "ingest",
		PlacementItemID: "item",
	}, V2ApplyRelationshipDecisionInput{PredicateKey: "works_on"})
	if scoped.TeamID != "team" || scoped.OwnerProfileID != "owner" || scoped.IngestID != "ingest" || scoped.PlacementItemID != "item" {
		t.Fatalf("scoped decision = %#v", scoped)
	}
	for category, want := range map[string]string{
		string(domain.V2OutcomeRelationshipNeedsReview): string(domain.V2OutcomeRelationshipNeedsReview),
		string(domain.V2OutcomePredicateNeedsReview):    string(domain.V2OutcomePredicateNeedsReview),
		string(domain.V2OutcomeIdentityNeedsReview):     string(domain.V2OutcomeIdentityNeedsReview),
	} {
		got := v2RelationshipOutcomeCategory(&V2RelationshipDecisionResult{
			ReviewTaskID: "review",
			Category:     category,
		})
		if got != want {
			t.Fatalf("review category %q = %q, want %q", category, got, want)
		}
	}
}

func TestV2PlacementCorrectionTargetRelated(t *testing.T) {
	source := &V2RelationshipRecord{
		SubjectEntityID: "subject-a",
		PredicateKey:    "uses",
		ObjectEntityID:  "object-a",
	}
	if !v2PlacementCorrectionTargetRelated(source, v2PlacementCorrectionTargetRecord{
		SubjectEntityID: "subject-a",
		PredicateKey:    "uses",
		ObjectEntityID:  "object-b",
	}) {
		t.Fatal("expected matching subject and predicate to be related")
	}
	if !v2PlacementCorrectionTargetRelated(source, v2PlacementCorrectionTargetRecord{
		SubjectEntityID: "subject-b",
		PredicateKey:    "uses",
		ObjectEntityID:  "object-a",
	}) {
		t.Fatal("expected matching object and predicate to be related")
	}
	if v2PlacementCorrectionTargetRelated(source, v2PlacementCorrectionTargetRecord{
		SubjectEntityID: "subject-a",
		PredicateKey:    "released",
		ObjectEntityID:  "object-a",
	}) {
		t.Fatal("different predicates must not be related")
	}
	if v2PlacementCorrectionTargetRelated(source, v2PlacementCorrectionTargetRecord{
		SubjectEntityID: "subject-b",
		PredicateKey:    "uses",
		ObjectEntityID:  "object-b",
	}) {
		t.Fatal("same predicate without an endpoint overlap must not be related")
	}
}

func TestV2PlacementErrorsRemainComparable(t *testing.T) {
	if !errors.Is(ErrV2PlacementLeaseLost, ErrV2PlacementLeaseLost) {
		t.Fatal("lease lost error is not comparable")
	}
	if !errors.Is(ErrV2PlacementStaleSource, ErrV2PlacementStaleSource) {
		t.Fatal("stale source error is not comparable")
	}
}
