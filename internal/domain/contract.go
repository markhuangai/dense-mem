package domain

const (
	ContractVersion              = "dense-mem.v2.4"
	PredicatePolicyVersion       = "open_vocabulary_v1"
	ConflictPolicyVersion        = "cross_profile_conflict_v1"
	ConflictOverduePolicyVersion = "overdue_conflict_ai_v1"
	FeatureGate                  = "memory"
	ToolVisibility               = "dormant"
)

type IngestID string
type PlacementItemID string
type EvidenceID string
type ObservationID string
type EntityID string
type ValueID string
type RelationshipID string
type HypothesisID string
type CommunityID string
type MemoryPackID string

type SemanticNodeKind string

const (
	SemanticNodeEntity SemanticNodeKind = "entity"
	SemanticNodeValue  SemanticNodeKind = "value"
)

type EntityKind string

const (
	EntityKindPerson       EntityKind = "person"
	EntityKindOrganization EntityKind = "organization"
	EntityKindProject      EntityKind = "project"
	EntityKindProduct      EntityKind = "product"
	EntityKindPlace        EntityKind = "place"
	EntityKindDocument     EntityKind = "document"
	EntityKindConcept      EntityKind = "concept"
	EntityKindOther        EntityKind = "other"
)

type ValueType string

const (
	ValueTypeString   ValueType = "string"
	ValueTypeNumber   ValueType = "number"
	ValueTypeBoolean  ValueType = "boolean"
	ValueTypeDate     ValueType = "date"
	ValueTypeDateTime ValueType = "date_time"
)

type RelationshipStatus string

const (
	RelationshipStatusActive          RelationshipStatus = "active"
	RelationshipStatusPendingEvidence RelationshipStatus = "pending_evidence"
	RelationshipStatusNeedsReview     RelationshipStatus = "needs_review"
	RelationshipStatusRejected        RelationshipStatus = "rejected"
	RelationshipStatusRetracted       RelationshipStatus = "retracted"
	RelationshipStatusSuperseded      RelationshipStatus = "superseded"
	RelationshipStatusQuarantined     RelationshipStatus = "quarantined"
	RelationshipStatusDisputed        RelationshipStatus = "disputed"
)

type RelationshipKind string

const (
	RelationshipKindState RelationshipKind = "state"
	RelationshipKindEvent RelationshipKind = "event"
)

type CurrentCardinality string

const (
	CurrentCardinalityOne  CurrentCardinality = "one"
	CurrentCardinalityMany CurrentCardinality = "many"
)

type PredicateLifecycleState string

const (
	PredicateLifecycleActive     PredicateLifecycleState = "active"
	PredicateLifecycleDeprecated PredicateLifecycleState = "deprecated"
	PredicateLifecycleRetired    PredicateLifecycleState = "retired"
)

type VerificationVerdict string

const (
	VerificationEntailed     VerificationVerdict = "entailed"
	VerificationContradicted VerificationVerdict = "contradicted"
	VerificationInsufficient VerificationVerdict = "insufficient"
)

type EntityResolutionAction string

const (
	EntityResolutionReuse     EntityResolutionAction = "reuse"
	EntityResolutionCreate    EntityResolutionAction = "create"
	EntityResolutionAmbiguous EntityResolutionAction = "ambiguous"
)

type SupportDecision string

const (
	SupportGrant     SupportDecision = "grant"
	SupportRevoke    SupportDecision = "revoke"
	SupportReinstate SupportDecision = "reinstate"
)

type CrossReferenceKind string

const (
	CrossReferenceConfirms           CrossReferenceKind = "confirms"
	CrossReferenceChallenges         CrossReferenceKind = "challenges"
	CrossReferenceCorrects           CrossReferenceKind = "corrects"
	CrossReferenceAdoptsEvidenceFrom CrossReferenceKind = "adopts_evidence_from"
)

type RelationshipConflictStatus string

const (
	RelationshipConflictOpen      RelationshipConflictStatus = "open"
	RelationshipConflictOverdue   RelationshipConflictStatus = "overdue"
	RelationshipConflictResolved  RelationshipConflictStatus = "resolved"
	RelationshipConflictDismissed RelationshipConflictStatus = "dismissed"
)

type RelationshipConflictKind string

const (
	RelationshipConflictCrossProfileCurrentState RelationshipConflictKind = "cross_profile_current_state"
)

type RelationshipConflictPositionDisposition string

const (
	RelationshipConflictPositionCandidate         RelationshipConflictPositionDisposition = "candidate"
	RelationshipConflictPositionPreferred         RelationshipConflictPositionDisposition = "preferred"
	RelationshipConflictPositionSuppressedCurrent RelationshipConflictPositionDisposition = "suppressed_current"
)

type RelationshipConflictEventAction string

const (
	RelationshipConflictEventOpened               RelationshipConflictEventAction = "opened"
	RelationshipConflictEventPositionAdded        RelationshipConflictEventAction = "position_added"
	RelationshipConflictEventMemberAdded          RelationshipConflictEventAction = "member_added"
	RelationshipConflictEventEvaluated            RelationshipConflictEventAction = "evaluated"
	RelationshipConflictEventMarkedOverdue        RelationshipConflictEventAction = "marked_overdue"
	RelationshipConflictEventResolved             RelationshipConflictEventAction = "resolved"
	RelationshipConflictEventRelationshipUpdated  RelationshipConflictEventAction = "relationship_updated"
	RelationshipConflictEventDismissed            RelationshipConflictEventAction = "dismissed"
	RelationshipConflictEventAIAssessmentReserved RelationshipConflictEventAction = "ai_assessment_reserved"
	RelationshipConflictEventAIAssessed           RelationshipConflictEventAction = "ai_assessed"
	RelationshipConflictEventResolutionPending    RelationshipConflictEventAction = "resolution_pending"
	RelationshipConflictEventEvidenceRetracted    RelationshipConflictEventAction = "evidence_retracted"
	RelationshipConflictEventDerivedStaged        RelationshipConflictEventAction = "derived_replacement_staged"
	RelationshipConflictEventDerivedFailed        RelationshipConflictEventAction = "derived_replacement_failed"
)

type HypothesisStatus string

const (
	HypothesisProposed   HypothesisStatus = "proposed"
	HypothesisReinforced HypothesisStatus = "reinforced"
	HypothesisStale      HypothesisStatus = "stale"
	HypothesisRejected   HypothesisStatus = "rejected"
	HypothesisSubmitted  HypothesisStatus = "submitted"
)

type PlacementRunStatus string

const (
	PlacementRunQueued         PlacementRunStatus = "queued"
	PlacementRunGuarded        PlacementRunStatus = "guarded"
	PlacementRunQuarantined    PlacementRunStatus = "quarantined"
	PlacementRunProcessing     PlacementRunStatus = "processing"
	PlacementRunAwaitingReview PlacementRunStatus = "awaiting_review"
	PlacementRunCompleted      PlacementRunStatus = "completed"
	PlacementRunFailed         PlacementRunStatus = "failed"
)

// SubmissionStatus is the public lifecycle for evidence that has not yet
// become canonical memory. It intentionally has no client-review state.
type SubmissionStatus string

const (
	SubmissionQueued      SubmissionStatus = "queued"
	SubmissionProcessing  SubmissionStatus = "processing"
	SubmissionCompleted   SubmissionStatus = "completed"
	SubmissionRejected    SubmissionStatus = "rejected"
	SubmissionQuarantined SubmissionStatus = "quarantined"
	SubmissionFailed      SubmissionStatus = "failed"
)

type SearchProjectionState string

const (
	SearchProjectionNotRequired SearchProjectionState = "not_required"
	SearchProjectionPending     SearchProjectionState = "pending"
	SearchProjectionCurrent     SearchProjectionState = "current"
	SearchProjectionFailed      SearchProjectionState = "failed"
)

type SearchIndexGenerationState string

const (
	SearchIndexGenerationBuilding   SearchIndexGenerationState = "building"
	SearchIndexGenerationActive     SearchIndexGenerationState = "active"
	SearchIndexGenerationFailed     SearchIndexGenerationState = "failed"
	SearchIndexGenerationDeprecated SearchIndexGenerationState = "deprecated"
	SearchIndexGenerationRetired    SearchIndexGenerationState = "retired"
)

type VectorDistanceMetric string

const (
	VectorDistanceCosine VectorDistanceMetric = "cosine"
)

type VectorIndexStrategy string

const (
	VectorIndexExact       VectorIndexStrategy = "exact"
	VectorIndexVectorHNSW  VectorIndexStrategy = "vector_hnsw"
	VectorIndexHalfvecHNSW VectorIndexStrategy = "halfvec_hnsw"
)

type EmbeddingJobStatus string

const (
	EmbeddingJobQueued     EmbeddingJobStatus = "queued"
	EmbeddingJobProcessing EmbeddingJobStatus = "processing"
	EmbeddingJobCompleted  EmbeddingJobStatus = "completed"
	EmbeddingJobFailed     EmbeddingJobStatus = "failed"
	EmbeddingJobStale      EmbeddingJobStatus = "stale"
	EmbeddingJobCancelled  EmbeddingJobStatus = "cancelled"
)

type EvidenceItemCategory string

const (
	EvidenceProcessed        EvidenceItemCategory = "evidence_processed"
	EvidenceQuarantined      EvidenceItemCategory = "evidence_quarantined"
	EvidenceProcessingFailed EvidenceItemCategory = "processing_failed"
)

type RelationshipOutcomeCategory string

const (
	OutcomeRelationshipAccepted        RelationshipOutcomeCategory = "relationship_accepted"
	OutcomeRelationshipPendingEvidence RelationshipOutcomeCategory = "relationship_pending_evidence"
	OutcomeRelationshipNeedsReview     RelationshipOutcomeCategory = "relationship_needs_review"
	OutcomePredicateNeedsReview        RelationshipOutcomeCategory = "predicate_needs_review"
	OutcomeRelationshipRejected        RelationshipOutcomeCategory = "relationship_rejected"
	OutcomeIdentityNeedsReview         RelationshipOutcomeCategory = "identity_needs_review"
)

type SemanticReviewStatus string

const (
	SemanticReviewAccepted        SemanticReviewStatus = "accepted"
	SemanticReviewReviewRequired  SemanticReviewStatus = "review_required"
	SemanticReviewQuarantined     SemanticReviewStatus = "quarantined"
	SemanticReviewRejected        SemanticReviewStatus = "rejected"
	SemanticReviewRetryable       SemanticReviewStatus = "retryable"
	SemanticReviewTerminalFailure SemanticReviewStatus = "terminal_failure"
)

type ResolveAction string

const (
	ResolveAcknowledge       ResolveAction = "acknowledge"
	ResolveSelectEntity      ResolveAction = "select_entity"
	ResolveConfirmNewEntity  ResolveAction = "confirm_new_entity"
	ResolveSelectPredicate   ResolveAction = "select_predicate"
	ResolveAccept            ResolveAction = "accept"
	ResolveReject            ResolveAction = "reject"
	ResolveCorrect           ResolveAction = "correct"
	ResolveReleaseQuarantine ResolveAction = "release_quarantine"
	ResolveForget            ResolveAction = "forget"
)

type EntityCorrectionAction string

const (
	EntityCorrectionMerge EntityCorrectionAction = "merge"
	EntityCorrectionSplit EntityCorrectionAction = "split"
)

type PublicErrorCode string

const (
	ErrorInvalidContractVersion PublicErrorCode = "invalid_contract_version"
	ErrorInvalidInput           PublicErrorCode = "invalid_input"
	ErrorUnauthorizedScope      PublicErrorCode = "unauthorized_scope"
	ErrorWrongOwner             PublicErrorCode = "wrong_owner"
	ErrorConflict               PublicErrorCode = "conflict"
	ErrorProviderUnavailable    PublicErrorCode = "provider_unavailable"
	ErrorProviderMalformed      PublicErrorCode = "provider_malformed"
	ErrorDegraded               PublicErrorCode = "degraded"
)

func ResolveActions() []string {
	return []string{
		string(ResolveAcknowledge),
		string(ResolveSelectEntity),
		string(ResolveConfirmNewEntity),
		string(ResolveSelectPredicate),
		string(ResolveAccept),
		string(ResolveReject),
		string(ResolveCorrect),
		string(ResolveReleaseQuarantine),
		string(ResolveForget),
	}
}

func RelationshipKinds() []string {
	return []string{
		string(RelationshipKindState),
		string(RelationshipKindEvent),
	}
}

func RelationshipStatuses() []string {
	return []string{
		string(RelationshipStatusPendingEvidence),
		string(RelationshipStatusActive),
		string(RelationshipStatusNeedsReview),
		string(RelationshipStatusRejected),
		string(RelationshipStatusRetracted),
		string(RelationshipStatusSuperseded),
		string(RelationshipStatusQuarantined),
		string(RelationshipStatusDisputed),
	}
}

func CurrentCardinalities() []string {
	return []string{
		string(CurrentCardinalityOne),
		string(CurrentCardinalityMany),
	}
}

func PredicateLifecycleStates() []string {
	return []string{
		string(PredicateLifecycleActive),
		string(PredicateLifecycleDeprecated),
		string(PredicateLifecycleRetired),
	}
}

func VerificationVerdicts() []string {
	return []string{
		string(VerificationEntailed),
		string(VerificationContradicted),
		string(VerificationInsufficient),
	}
}

func EntityResolutionActions() []string {
	return []string{
		string(EntityResolutionReuse),
		string(EntityResolutionCreate),
		string(EntityResolutionAmbiguous),
	}
}

func SupportDecisions() []string {
	return []string{
		string(SupportGrant),
		string(SupportRevoke),
		string(SupportReinstate),
	}
}

func CrossReferenceKinds() []string {
	return []string{
		string(CrossReferenceConfirms),
		string(CrossReferenceChallenges),
		string(CrossReferenceCorrects),
		string(CrossReferenceAdoptsEvidenceFrom),
	}
}

func HypothesisStatuses() []string {
	return []string{
		string(HypothesisProposed),
		string(HypothesisReinforced),
		string(HypothesisStale),
		string(HypothesisRejected),
		string(HypothesisSubmitted),
	}
}

func EntityCorrectionActions() []string {
	return []string{
		string(EntityCorrectionMerge),
		string(EntityCorrectionSplit),
	}
}

func RelationshipOutcomeCategories() []string {
	return []string{
		string(OutcomeRelationshipAccepted),
		string(OutcomeRelationshipPendingEvidence),
		string(OutcomeRelationshipNeedsReview),
		string(OutcomePredicateNeedsReview),
		string(OutcomeRelationshipRejected),
		string(OutcomeIdentityNeedsReview),
	}
}

func SemanticReviewStatuses() []string {
	return []string{
		string(SemanticReviewAccepted),
		string(SemanticReviewReviewRequired),
		string(SemanticReviewQuarantined),
		string(SemanticReviewRejected),
		string(SemanticReviewRetryable),
		string(SemanticReviewTerminalFailure),
	}
}

func EvidenceItemCategories() []string {
	return []string{
		string(EvidenceProcessed),
		string(EvidenceQuarantined),
		string(EvidenceProcessingFailed),
	}
}

func EntityKinds() []string {
	return []string{
		string(EntityKindPerson),
		string(EntityKindOrganization),
		string(EntityKindProject),
		string(EntityKindProduct),
		string(EntityKindPlace),
		string(EntityKindDocument),
		string(EntityKindConcept),
		string(EntityKindOther),
	}
}

func ValueTypes() []string {
	return []string{
		string(ValueTypeString),
		string(ValueTypeNumber),
		string(ValueTypeBoolean),
		string(ValueTypeDate),
		string(ValueTypeDateTime),
	}
}

func PlacementRunStatuses() []string {
	return []string{
		string(PlacementRunQueued),
		string(PlacementRunGuarded),
		string(PlacementRunQuarantined),
		string(PlacementRunProcessing),
		string(PlacementRunAwaitingReview),
		string(PlacementRunCompleted),
		string(PlacementRunFailed),
	}
}

func SubmissionStatuses() []string {
	return []string{
		string(SubmissionQueued),
		string(SubmissionProcessing),
		string(SubmissionCompleted),
		string(SubmissionRejected),
		string(SubmissionQuarantined),
		string(SubmissionFailed),
	}
}

func SearchProjectionStates() []string {
	return []string{
		string(SearchProjectionNotRequired),
		string(SearchProjectionPending),
		string(SearchProjectionCurrent),
		string(SearchProjectionFailed),
	}
}

func SearchIndexGenerationStates() []string {
	return []string{
		string(SearchIndexGenerationBuilding),
		string(SearchIndexGenerationActive),
		string(SearchIndexGenerationFailed),
		string(SearchIndexGenerationDeprecated),
		string(SearchIndexGenerationRetired),
	}
}

func VectorDistanceMetrics() []string {
	return []string{
		string(VectorDistanceCosine),
	}
}

func VectorIndexStrategies() []string {
	return []string{
		string(VectorIndexExact),
		string(VectorIndexVectorHNSW),
		string(VectorIndexHalfvecHNSW),
	}
}

func EmbeddingJobStatuses() []string {
	return []string{
		string(EmbeddingJobQueued),
		string(EmbeddingJobProcessing),
		string(EmbeddingJobCompleted),
		string(EmbeddingJobFailed),
		string(EmbeddingJobStale),
		string(EmbeddingJobCancelled),
	}
}

func RelationshipConflictStatuses() []string {
	return []string{
		string(RelationshipConflictOpen),
		string(RelationshipConflictOverdue),
		string(RelationshipConflictResolved),
		string(RelationshipConflictDismissed),
	}
}

func RelationshipConflictKinds() []string {
	return []string{
		string(RelationshipConflictCrossProfileCurrentState),
	}
}

func RelationshipConflictPositionDispositions() []string {
	return []string{
		string(RelationshipConflictPositionCandidate),
		string(RelationshipConflictPositionPreferred),
		string(RelationshipConflictPositionSuppressedCurrent),
	}
}
