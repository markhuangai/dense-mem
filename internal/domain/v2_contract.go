package domain

const (
	V2ContractVersion        = "dense-mem.v2.1"
	V2PredicatePolicyVersion = "open_vocabulary_v1"
	V2ConflictPolicyVersion  = "cross_profile_conflict_v1"
	V2FeatureGate            = "memory_v2"
	V2ToolVisibility         = "dormant"
)

type V2IngestID string
type V2PlacementItemID string
type V2EvidenceID string
type V2ObservationID string
type V2EntityID string
type V2ValueID string
type V2RelationshipID string
type V2HypothesisID string
type V2CommunityID string
type V2MemoryPackID string

type V2SemanticNodeKind string

const (
	V2SemanticNodeEntity V2SemanticNodeKind = "entity"
	V2SemanticNodeValue  V2SemanticNodeKind = "value"
)

type V2EntityKind string

const (
	V2EntityKindPerson       V2EntityKind = "person"
	V2EntityKindOrganization V2EntityKind = "organization"
	V2EntityKindProject      V2EntityKind = "project"
	V2EntityKindProduct      V2EntityKind = "product"
	V2EntityKindPlace        V2EntityKind = "place"
	V2EntityKindDocument     V2EntityKind = "document"
	V2EntityKindConcept      V2EntityKind = "concept"
	V2EntityKindOther        V2EntityKind = "other"
)

type V2ValueType string

const (
	V2ValueTypeString   V2ValueType = "string"
	V2ValueTypeNumber   V2ValueType = "number"
	V2ValueTypeBoolean  V2ValueType = "boolean"
	V2ValueTypeDate     V2ValueType = "date"
	V2ValueTypeDateTime V2ValueType = "date_time"
)

type V2RelationshipTier string

const (
	V2RelationshipTierCandidate       V2RelationshipTier = "candidate"
	V2RelationshipTierFact            V2RelationshipTier = "fact"
	V2RelationshipTierValidatedClaim  V2RelationshipTier = "validated_claim"
	V2RelationshipTierPendingEvidence V2RelationshipTier = "pending_evidence"
	V2RelationshipTierNeedsReview     V2RelationshipTier = "needs_review"
	V2RelationshipTierPredicateReview V2RelationshipTier = "predicate_needs_review"
	V2RelationshipTierIdentityReview  V2RelationshipTier = "identity_needs_review"
)

type V2RelationshipStatus string

const (
	V2RelationshipStatusActive          V2RelationshipStatus = "active"
	V2RelationshipStatusPendingEvidence V2RelationshipStatus = "pending_evidence"
	V2RelationshipStatusNeedsReview     V2RelationshipStatus = "needs_review"
	V2RelationshipStatusRejected        V2RelationshipStatus = "rejected"
	V2RelationshipStatusRetracted       V2RelationshipStatus = "retracted"
	V2RelationshipStatusSuperseded      V2RelationshipStatus = "superseded"
	V2RelationshipStatusQuarantined     V2RelationshipStatus = "quarantined"
	V2RelationshipStatusDisputed        V2RelationshipStatus = "disputed"
)

type V2RelationshipKind string

const (
	V2RelationshipKindState V2RelationshipKind = "state"
	V2RelationshipKindEvent V2RelationshipKind = "event"
)

type V2CurrentCardinality string

const (
	V2CurrentCardinalityOne  V2CurrentCardinality = "one"
	V2CurrentCardinalityMany V2CurrentCardinality = "many"
)

type V2PredicateLifecycleState string

const (
	V2PredicateLifecycleActive     V2PredicateLifecycleState = "active"
	V2PredicateLifecycleDeprecated V2PredicateLifecycleState = "deprecated"
	V2PredicateLifecycleRetired    V2PredicateLifecycleState = "retired"
)

type V2VerificationVerdict string

const (
	V2VerificationEntailed     V2VerificationVerdict = "entailed"
	V2VerificationContradicted V2VerificationVerdict = "contradicted"
	V2VerificationInsufficient V2VerificationVerdict = "insufficient"
)

type V2EntityResolutionAction string

const (
	V2EntityResolutionReuse     V2EntityResolutionAction = "reuse"
	V2EntityResolutionCreate    V2EntityResolutionAction = "create"
	V2EntityResolutionAmbiguous V2EntityResolutionAction = "ambiguous"
)

type V2SupportDecision string

const (
	V2SupportGrant     V2SupportDecision = "grant"
	V2SupportRevoke    V2SupportDecision = "revoke"
	V2SupportReinstate V2SupportDecision = "reinstate"
)

type V2CrossReferenceKind string

const (
	V2CrossReferenceConfirms           V2CrossReferenceKind = "confirms"
	V2CrossReferenceChallenges         V2CrossReferenceKind = "challenges"
	V2CrossReferenceCorrects           V2CrossReferenceKind = "corrects"
	V2CrossReferenceAdoptsEvidenceFrom V2CrossReferenceKind = "adopts_evidence_from"
)

type V2RelationshipConflictStatus string

const (
	V2RelationshipConflictOpen      V2RelationshipConflictStatus = "open"
	V2RelationshipConflictOverdue   V2RelationshipConflictStatus = "overdue"
	V2RelationshipConflictResolved  V2RelationshipConflictStatus = "resolved"
	V2RelationshipConflictDismissed V2RelationshipConflictStatus = "dismissed"
)

type V2RelationshipConflictKind string

const (
	V2RelationshipConflictCrossProfileCurrentState V2RelationshipConflictKind = "cross_profile_current_state"
)

type V2RelationshipConflictPositionDisposition string

const (
	V2RelationshipConflictPositionCandidate         V2RelationshipConflictPositionDisposition = "candidate"
	V2RelationshipConflictPositionPreferred         V2RelationshipConflictPositionDisposition = "preferred"
	V2RelationshipConflictPositionSuppressedCurrent V2RelationshipConflictPositionDisposition = "suppressed_current"
)

type V2RelationshipConflictEventAction string

const (
	V2RelationshipConflictEventOpened              V2RelationshipConflictEventAction = "opened"
	V2RelationshipConflictEventPositionAdded       V2RelationshipConflictEventAction = "position_added"
	V2RelationshipConflictEventMemberAdded         V2RelationshipConflictEventAction = "member_added"
	V2RelationshipConflictEventEvaluated           V2RelationshipConflictEventAction = "evaluated"
	V2RelationshipConflictEventMarkedOverdue       V2RelationshipConflictEventAction = "marked_overdue"
	V2RelationshipConflictEventResolved            V2RelationshipConflictEventAction = "resolved"
	V2RelationshipConflictEventRelationshipUpdated V2RelationshipConflictEventAction = "relationship_updated"
)

type V2HypothesisStatus string

const (
	V2HypothesisProposed   V2HypothesisStatus = "proposed"
	V2HypothesisReinforced V2HypothesisStatus = "reinforced"
	V2HypothesisStale      V2HypothesisStatus = "stale"
	V2HypothesisRejected   V2HypothesisStatus = "rejected"
	V2HypothesisSubmitted  V2HypothesisStatus = "submitted"
)

type V2PlacementRunStatus string

const (
	V2PlacementRunQueued         V2PlacementRunStatus = "queued"
	V2PlacementRunGuarded        V2PlacementRunStatus = "guarded"
	V2PlacementRunQuarantined    V2PlacementRunStatus = "quarantined"
	V2PlacementRunProcessing     V2PlacementRunStatus = "processing"
	V2PlacementRunAwaitingReview V2PlacementRunStatus = "awaiting_review"
	V2PlacementRunCompleted      V2PlacementRunStatus = "completed"
	V2PlacementRunFailed         V2PlacementRunStatus = "failed"
)

type V2SearchProjectionState string

const (
	V2SearchProjectionNotRequired V2SearchProjectionState = "not_required"
	V2SearchProjectionPending     V2SearchProjectionState = "pending"
	V2SearchProjectionCurrent     V2SearchProjectionState = "current"
	V2SearchProjectionFailed      V2SearchProjectionState = "failed"
)

type V2SearchIndexGenerationState string

const (
	V2SearchIndexGenerationBuilding   V2SearchIndexGenerationState = "building"
	V2SearchIndexGenerationActive     V2SearchIndexGenerationState = "active"
	V2SearchIndexGenerationFailed     V2SearchIndexGenerationState = "failed"
	V2SearchIndexGenerationDeprecated V2SearchIndexGenerationState = "deprecated"
	V2SearchIndexGenerationRetired    V2SearchIndexGenerationState = "retired"
)

type V2VectorDistanceMetric string

const (
	V2VectorDistanceCosine V2VectorDistanceMetric = "cosine"
)

type V2VectorIndexStrategy string

const (
	V2VectorIndexExact       V2VectorIndexStrategy = "exact"
	V2VectorIndexVectorHNSW  V2VectorIndexStrategy = "vector_hnsw"
	V2VectorIndexHalfvecHNSW V2VectorIndexStrategy = "halfvec_hnsw"
)

type V2EmbeddingJobStatus string

const (
	V2EmbeddingJobQueued     V2EmbeddingJobStatus = "queued"
	V2EmbeddingJobProcessing V2EmbeddingJobStatus = "processing"
	V2EmbeddingJobCompleted  V2EmbeddingJobStatus = "completed"
	V2EmbeddingJobFailed     V2EmbeddingJobStatus = "failed"
	V2EmbeddingJobStale      V2EmbeddingJobStatus = "stale"
	V2EmbeddingJobCancelled  V2EmbeddingJobStatus = "cancelled"
)

type V2EvidenceItemCategory string

const (
	V2EvidenceProcessed        V2EvidenceItemCategory = "evidence_processed"
	V2EvidenceQuarantined      V2EvidenceItemCategory = "evidence_quarantined"
	V2EvidenceProcessingFailed V2EvidenceItemCategory = "processing_failed"
)

type V2RelationshipOutcomeCategory string

const (
	V2OutcomeRelationshipFact            V2RelationshipOutcomeCategory = "relationship_fact"
	V2OutcomeRelationshipValidatedClaim  V2RelationshipOutcomeCategory = "relationship_validated_claim"
	V2OutcomeRelationshipPendingEvidence V2RelationshipOutcomeCategory = "relationship_pending_evidence"
	V2OutcomeRelationshipNeedsReview     V2RelationshipOutcomeCategory = "relationship_needs_review"
	V2OutcomePredicateNeedsReview        V2RelationshipOutcomeCategory = "predicate_needs_review"
	V2OutcomeRelationshipRejected        V2RelationshipOutcomeCategory = "relationship_rejected"
	V2OutcomeIdentityNeedsReview         V2RelationshipOutcomeCategory = "identity_needs_review"
)

type V2SemanticReviewStatus string

const (
	V2SemanticReviewAccepted        V2SemanticReviewStatus = "accepted"
	V2SemanticReviewReviewRequired  V2SemanticReviewStatus = "review_required"
	V2SemanticReviewQuarantined     V2SemanticReviewStatus = "quarantined"
	V2SemanticReviewRejected        V2SemanticReviewStatus = "rejected"
	V2SemanticReviewRetryable       V2SemanticReviewStatus = "retryable"
	V2SemanticReviewTerminalFailure V2SemanticReviewStatus = "terminal_failure"
)

type V2ResolveAction string

const (
	V2ResolveAcknowledge       V2ResolveAction = "acknowledge"
	V2ResolveSelectEntity      V2ResolveAction = "select_entity"
	V2ResolveConfirmNewEntity  V2ResolveAction = "confirm_new_entity"
	V2ResolveSelectPredicate   V2ResolveAction = "select_predicate"
	V2ResolveAccept            V2ResolveAction = "accept"
	V2ResolveReject            V2ResolveAction = "reject"
	V2ResolveCorrect           V2ResolveAction = "correct"
	V2ResolveReleaseQuarantine V2ResolveAction = "release_quarantine"
	V2ResolveForget            V2ResolveAction = "forget"
)

type V2EntityCorrectionAction string

const (
	V2EntityCorrectionMerge V2EntityCorrectionAction = "merge"
	V2EntityCorrectionSplit V2EntityCorrectionAction = "split"
)

type V2PublicErrorCode string

const (
	V2ErrorInvalidContractVersion V2PublicErrorCode = "invalid_contract_version"
	V2ErrorInvalidInput           V2PublicErrorCode = "invalid_input"
	V2ErrorUnauthorizedScope      V2PublicErrorCode = "unauthorized_scope"
	V2ErrorWrongOwner             V2PublicErrorCode = "wrong_owner"
	V2ErrorConflict               V2PublicErrorCode = "conflict"
	V2ErrorProviderUnavailable    V2PublicErrorCode = "provider_unavailable"
	V2ErrorProviderMalformed      V2PublicErrorCode = "provider_malformed"
	V2ErrorDegraded               V2PublicErrorCode = "degraded"
)

func V2ResolveActions() []string {
	return []string{
		string(V2ResolveAcknowledge),
		string(V2ResolveSelectEntity),
		string(V2ResolveConfirmNewEntity),
		string(V2ResolveSelectPredicate),
		string(V2ResolveAccept),
		string(V2ResolveReject),
		string(V2ResolveCorrect),
		string(V2ResolveReleaseQuarantine),
		string(V2ResolveForget),
	}
}

func V2RelationshipKinds() []string {
	return []string{
		string(V2RelationshipKindState),
		string(V2RelationshipKindEvent),
	}
}

func V2RelationshipTiers() []string {
	return []string{
		string(V2RelationshipTierCandidate),
		string(V2RelationshipTierFact),
		string(V2RelationshipTierValidatedClaim),
		string(V2RelationshipTierPendingEvidence),
		string(V2RelationshipTierNeedsReview),
		string(V2RelationshipTierPredicateReview),
		string(V2RelationshipTierIdentityReview),
	}
}

func V2RelationshipStatuses() []string {
	return []string{
		string(V2RelationshipStatusPendingEvidence),
		string(V2RelationshipStatusActive),
		string(V2RelationshipStatusNeedsReview),
		string(V2RelationshipStatusRejected),
		string(V2RelationshipStatusRetracted),
		string(V2RelationshipStatusSuperseded),
		string(V2RelationshipStatusQuarantined),
		string(V2RelationshipStatusDisputed),
	}
}

func V2CurrentCardinalities() []string {
	return []string{
		string(V2CurrentCardinalityOne),
		string(V2CurrentCardinalityMany),
	}
}

func V2PredicateLifecycleStates() []string {
	return []string{
		string(V2PredicateLifecycleActive),
		string(V2PredicateLifecycleDeprecated),
		string(V2PredicateLifecycleRetired),
	}
}

func V2VerificationVerdicts() []string {
	return []string{
		string(V2VerificationEntailed),
		string(V2VerificationContradicted),
		string(V2VerificationInsufficient),
	}
}

func V2EntityResolutionActions() []string {
	return []string{
		string(V2EntityResolutionReuse),
		string(V2EntityResolutionCreate),
		string(V2EntityResolutionAmbiguous),
	}
}

func V2SupportDecisions() []string {
	return []string{
		string(V2SupportGrant),
		string(V2SupportRevoke),
		string(V2SupportReinstate),
	}
}

func V2CrossReferenceKinds() []string {
	return []string{
		string(V2CrossReferenceConfirms),
		string(V2CrossReferenceChallenges),
		string(V2CrossReferenceCorrects),
		string(V2CrossReferenceAdoptsEvidenceFrom),
	}
}

func V2HypothesisStatuses() []string {
	return []string{
		string(V2HypothesisProposed),
		string(V2HypothesisReinforced),
		string(V2HypothesisStale),
		string(V2HypothesisRejected),
		string(V2HypothesisSubmitted),
	}
}

func V2EntityCorrectionActions() []string {
	return []string{
		string(V2EntityCorrectionMerge),
		string(V2EntityCorrectionSplit),
	}
}

func V2RelationshipOutcomeCategories() []string {
	return []string{
		string(V2OutcomeRelationshipFact),
		string(V2OutcomeRelationshipValidatedClaim),
		string(V2OutcomeRelationshipPendingEvidence),
		string(V2OutcomeRelationshipNeedsReview),
		string(V2OutcomePredicateNeedsReview),
		string(V2OutcomeRelationshipRejected),
		string(V2OutcomeIdentityNeedsReview),
	}
}

func V2SemanticReviewStatuses() []string {
	return []string{
		string(V2SemanticReviewAccepted),
		string(V2SemanticReviewReviewRequired),
		string(V2SemanticReviewQuarantined),
		string(V2SemanticReviewRejected),
		string(V2SemanticReviewRetryable),
		string(V2SemanticReviewTerminalFailure),
	}
}

func V2EvidenceItemCategories() []string {
	return []string{
		string(V2EvidenceProcessed),
		string(V2EvidenceQuarantined),
		string(V2EvidenceProcessingFailed),
	}
}

func V2EntityKinds() []string {
	return []string{
		string(V2EntityKindPerson),
		string(V2EntityKindOrganization),
		string(V2EntityKindProject),
		string(V2EntityKindProduct),
		string(V2EntityKindPlace),
		string(V2EntityKindDocument),
		string(V2EntityKindConcept),
		string(V2EntityKindOther),
	}
}

func V2ValueTypes() []string {
	return []string{
		string(V2ValueTypeString),
		string(V2ValueTypeNumber),
		string(V2ValueTypeBoolean),
		string(V2ValueTypeDate),
		string(V2ValueTypeDateTime),
	}
}

func V2PlacementRunStatuses() []string {
	return []string{
		string(V2PlacementRunQueued),
		string(V2PlacementRunGuarded),
		string(V2PlacementRunQuarantined),
		string(V2PlacementRunProcessing),
		string(V2PlacementRunAwaitingReview),
		string(V2PlacementRunCompleted),
		string(V2PlacementRunFailed),
	}
}

func V2SearchProjectionStates() []string {
	return []string{
		string(V2SearchProjectionNotRequired),
		string(V2SearchProjectionPending),
		string(V2SearchProjectionCurrent),
		string(V2SearchProjectionFailed),
	}
}

func V2SearchIndexGenerationStates() []string {
	return []string{
		string(V2SearchIndexGenerationBuilding),
		string(V2SearchIndexGenerationActive),
		string(V2SearchIndexGenerationFailed),
		string(V2SearchIndexGenerationDeprecated),
		string(V2SearchIndexGenerationRetired),
	}
}

func V2VectorDistanceMetrics() []string {
	return []string{
		string(V2VectorDistanceCosine),
	}
}

func V2VectorIndexStrategies() []string {
	return []string{
		string(V2VectorIndexExact),
		string(V2VectorIndexVectorHNSW),
		string(V2VectorIndexHalfvecHNSW),
	}
}

func V2EmbeddingJobStatuses() []string {
	return []string{
		string(V2EmbeddingJobQueued),
		string(V2EmbeddingJobProcessing),
		string(V2EmbeddingJobCompleted),
		string(V2EmbeddingJobFailed),
		string(V2EmbeddingJobStale),
		string(V2EmbeddingJobCancelled),
	}
}

func V2RelationshipConflictStatuses() []string {
	return []string{
		string(V2RelationshipConflictOpen),
		string(V2RelationshipConflictOverdue),
		string(V2RelationshipConflictResolved),
		string(V2RelationshipConflictDismissed),
	}
}

func V2RelationshipConflictKinds() []string {
	return []string{
		string(V2RelationshipConflictCrossProfileCurrentState),
	}
}

func V2RelationshipConflictPositionDispositions() []string {
	return []string{
		string(V2RelationshipConflictPositionCandidate),
		string(V2RelationshipConflictPositionPreferred),
		string(V2RelationshipConflictPositionSuppressedCurrent),
	}
}
