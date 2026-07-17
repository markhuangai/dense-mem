package domain

const (
	V2ContractVersion = "dense-mem.v2.1"
	V2FeatureGate     = "memory_v2"
	V2ToolVisibility  = "dormant"
)

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

type V2PlacementRunStatus string

const (
	V2PlacementRunQueued     V2PlacementRunStatus = "queued"
	V2PlacementRunProcessing V2PlacementRunStatus = "processing"
	V2PlacementRunCompleted  V2PlacementRunStatus = "completed"
	V2PlacementRunFailed     V2PlacementRunStatus = "failed"
)

type V2SearchProjectionState string

const (
	V2SearchProjectionPending V2SearchProjectionState = "pending"
	V2SearchProjectionCurrent V2SearchProjectionState = "current"
	V2SearchProjectionStale   V2SearchProjectionState = "stale"
	V2SearchProjectionFailed  V2SearchProjectionState = "failed"
)

type V2EvidenceItemCategory string

const (
	V2EvidenceProcessed   V2EvidenceItemCategory = "evidence_processed"
	V2EvidenceNeedsReview V2EvidenceItemCategory = "evidence_needs_review"
	V2EvidenceQuarantined V2EvidenceItemCategory = "evidence_quarantined"
)

type V2RelationshipOutcomeCategory string

const (
	V2OutcomeRelationshipFact            V2RelationshipOutcomeCategory = "relationship_fact"
	V2OutcomeRelationshipValidatedClaim  V2RelationshipOutcomeCategory = "relationship_validated_claim"
	V2OutcomeRelationshipPendingEvidence V2RelationshipOutcomeCategory = "relationship_pending_evidence"
	V2OutcomeRelationshipNeedsReview     V2RelationshipOutcomeCategory = "relationship_needs_review"
	V2OutcomePredicateNeedsReview        V2RelationshipOutcomeCategory = "predicate_needs_review"
	V2OutcomeIdentityNeedsReview         V2RelationshipOutcomeCategory = "identity_needs_review"
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
		string(V2OutcomeIdentityNeedsReview),
	}
}

func V2EvidenceItemCategories() []string {
	return []string{
		string(V2EvidenceProcessed),
		string(V2EvidenceNeedsReview),
		string(V2EvidenceQuarantined),
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
		string(V2PlacementRunProcessing),
		string(V2PlacementRunCompleted),
		string(V2PlacementRunFailed),
	}
}

func V2SearchProjectionStates() []string {
	return []string{
		string(V2SearchProjectionPending),
		string(V2SearchProjectionCurrent),
		string(V2SearchProjectionStale),
		string(V2SearchProjectionFailed),
	}
}
