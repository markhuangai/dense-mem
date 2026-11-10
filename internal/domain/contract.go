package domain

const (
	ContractVersion         = "dense-mem.v2.8.0"
	PreviousContractVersion = "dense-mem.v2.6.2"
	FeatureGate             = "memory"
	ToolVisibility          = "active"
)

// ContractVersionCompatible reports versions whose terminal Remember shape
// and replay semantics are intentionally preserved by the current server.
func ContractVersionCompatible(version string) bool {
	return version == ContractVersion
}

// AcceptedContractVersions contains only the supported Evidence-only contract.
func AcceptedContractVersions() []string {
	return []string{ContractVersion}
}

const (
	SubmissionErrorMessagePolicyRejected  = "submission was rejected by semantic policy"
	SubmissionErrorMessageInternalFailure = "Dense-Mem could not complete the submission"
)

type IngestID string
type EvidenceID string
type MemoryPackID string

type SearchProjectionState string

const (
	SearchProjectionNotRequired SearchProjectionState = "not_required"
	SearchProjectionPending     SearchProjectionState = "pending"
	SearchProjectionCurrent     SearchProjectionState = "current"
	SearchProjectionFailed      SearchProjectionState = "failed"
)

// CombineSearchProjectionStates returns the most degraded state observed across
// two search branches while preserving the canonical precedence order.
func CombineSearchProjectionStates(left, right string) string {
	if left == string(SearchProjectionFailed) || right == string(SearchProjectionFailed) {
		return string(SearchProjectionFailed)
	}
	if left == string(SearchProjectionPending) || right == string(SearchProjectionPending) {
		return string(SearchProjectionPending)
	}
	if left == string(SearchProjectionCurrent) || right == string(SearchProjectionCurrent) {
		return string(SearchProjectionCurrent)
	}
	if left == string(SearchProjectionNotRequired) || right == string(SearchProjectionNotRequired) {
		return string(SearchProjectionNotRequired)
	}
	if left == "" {
		return right
	}
	return left
}

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
	MaxEmbeddingDimensions     = 16000
	MaxEmbeddingBatchDocuments = 256
)

const (
	VectorIndexExact       VectorIndexStrategy = "exact"
	VectorIndexVectorHNSW  VectorIndexStrategy = "vector_hnsw"
	VectorIndexHalfvecHNSW VectorIndexStrategy = "halfvec_hnsw"
	VectorIndexBinaryHNSW  VectorIndexStrategy = "binary_hnsw"
)

type EvidenceItemCategory string

const (
	EvidenceProcessed        EvidenceItemCategory = "evidence_processed"
	EvidenceQuarantined      EvidenceItemCategory = "evidence_quarantined"
	EvidenceProcessingFailed EvidenceItemCategory = "processing_failed"
)

type SemanticReviewStatus string

const (
	SemanticReviewAccepted        SemanticReviewStatus = "accepted"
	SemanticReviewQuarantined     SemanticReviewStatus = "quarantined"
	SemanticReviewRejected        SemanticReviewStatus = "rejected"
	SemanticReviewRetryable       SemanticReviewStatus = "retryable"
	SemanticReviewTerminalFailure SemanticReviewStatus = "terminal_failure"
	SemanticReviewSuperseded      SemanticReviewStatus = "superseded"
)

type PublicErrorCode string

const (
	ErrorInvalidInput        PublicErrorCode = "invalid_input"
	ErrorUnauthorizedScope   PublicErrorCode = "unauthorized_scope"
	ErrorWrongOwner          PublicErrorCode = "wrong_owner"
	ErrorConflict            PublicErrorCode = "conflict"
	ErrorProviderUnavailable PublicErrorCode = "provider_unavailable"
	ErrorProviderMalformed   PublicErrorCode = "provider_malformed"
	ErrorDegraded            PublicErrorCode = "degraded"
)

func SemanticReviewStatuses() []string {
	return []string{
		string(SemanticReviewAccepted),
		string(SemanticReviewQuarantined),
		string(SemanticReviewRejected),
		string(SemanticReviewRetryable),
		string(SemanticReviewTerminalFailure),
		string(SemanticReviewSuperseded),
	}
}

func EvidenceItemCategories() []string {
	return []string{
		string(EvidenceProcessed),
		string(EvidenceQuarantined),
		string(EvidenceProcessingFailed),
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
		string(VectorIndexBinaryHNSW),
	}
}

// EmbeddingFailureClass is the server-owned recovery policy for a failed
// provider or input operation. Unknown failures are intentionally classified
// as permanent by the embedding adapter.
type EmbeddingFailureClass string

const (
	EmbeddingFailureTransient      EmbeddingFailureClass = "transient"
	EmbeddingFailureProviderAction EmbeddingFailureClass = "provider_action_required"
	EmbeddingFailurePermanent      EmbeddingFailureClass = "permanent"
)

type EmbeddingFailureCode string

const (
	EmbeddingFailureProviderRateLimited      EmbeddingFailureCode = "provider_rate_limited"
	EmbeddingFailureProviderTimeout          EmbeddingFailureCode = "provider_timeout"
	EmbeddingFailureProviderNetworkError     EmbeddingFailureCode = "provider_network_error"
	EmbeddingFailureProviderServerError      EmbeddingFailureCode = "provider_server_error"
	EmbeddingFailureProviderQuotaExhausted   EmbeddingFailureCode = "provider_quota_exhausted"
	EmbeddingFailureProviderAuthentication   EmbeddingFailureCode = "provider_authentication_failed"
	EmbeddingFailureProviderPermissionDenied EmbeddingFailureCode = "provider_permission_denied"
	EmbeddingFailureProviderContractRejected EmbeddingFailureCode = "provider_contract_rejected"
	EmbeddingFailureProviderResponseInvalid  EmbeddingFailureCode = "provider_response_invalid"
	EmbeddingFailureInputRejected            EmbeddingFailureCode = "embedding_input_rejected"
	EmbeddingFailureContractMismatch         EmbeddingFailureCode = "embedding_contract_mismatch"
	EmbeddingFailureUnknown                  EmbeddingFailureCode = "unknown_embedding_failure"
)

func EmbeddingFailureClasses() []string {
	return []string{
		string(EmbeddingFailureTransient),
		string(EmbeddingFailureProviderAction),
		string(EmbeddingFailurePermanent),
	}
}

func EmbeddingFailureCodes() []string {
	return []string{
		string(EmbeddingFailureProviderRateLimited),
		string(EmbeddingFailureProviderTimeout),
		string(EmbeddingFailureProviderNetworkError),
		string(EmbeddingFailureProviderServerError),
		string(EmbeddingFailureProviderQuotaExhausted),
		string(EmbeddingFailureProviderAuthentication),
		string(EmbeddingFailureProviderPermissionDenied),
		string(EmbeddingFailureProviderContractRejected),
		string(EmbeddingFailureProviderResponseInvalid),
		string(EmbeddingFailureInputRejected),
		string(EmbeddingFailureContractMismatch),
		string(EmbeddingFailureUnknown),
	}
}

func EmbeddingFailureCodeValid(code string) bool {
	for _, candidate := range EmbeddingFailureCodes() {
		if code == candidate {
			return true
		}
	}
	return false
}

func EmbeddingFailureContractValid(class, code string) bool {
	switch EmbeddingFailureClass(class) {
	case EmbeddingFailureTransient:
		return code == string(EmbeddingFailureProviderRateLimited) ||
			code == string(EmbeddingFailureProviderTimeout) ||
			code == string(EmbeddingFailureProviderNetworkError) ||
			code == string(EmbeddingFailureProviderServerError)
	case EmbeddingFailureProviderAction:
		return code == string(EmbeddingFailureProviderQuotaExhausted) ||
			code == string(EmbeddingFailureProviderAuthentication) ||
			code == string(EmbeddingFailureProviderPermissionDenied) ||
			code == string(EmbeddingFailureProviderContractRejected) ||
			code == string(EmbeddingFailureProviderResponseInvalid)
	case EmbeddingFailurePermanent:
		return code == string(EmbeddingFailureInputRejected) ||
			code == string(EmbeddingFailureContractMismatch) ||
			code == string(EmbeddingFailureUnknown)
	default:
		return false
	}
}

func EmbeddingFailureMessage(code string) string {
	switch code {
	case string(EmbeddingFailureProviderRateLimited):
		return "embedding provider rate limited"
	case string(EmbeddingFailureProviderTimeout):
		return "embedding provider timed out"
	case string(EmbeddingFailureProviderNetworkError):
		return "embedding provider network failure"
	case string(EmbeddingFailureProviderServerError):
		return "embedding provider server failure"
	case string(EmbeddingFailureProviderQuotaExhausted):
		return "embedding provider quota exhausted"
	case string(EmbeddingFailureProviderAuthentication):
		return "embedding provider authentication failed"
	case string(EmbeddingFailureProviderPermissionDenied):
		return "embedding provider permission denied"
	case string(EmbeddingFailureProviderContractRejected):
		return "embedding provider contract rejected"
	case string(EmbeddingFailureProviderResponseInvalid):
		return "embedding provider response invalid"
	case string(EmbeddingFailureInputRejected):
		return "embedding input rejected"
	case string(EmbeddingFailureContractMismatch):
		return "embedding contract mismatch"
	default:
		return "embedding processing failed"
	}
}

type EmbeddingReconciliationRunStatus string

const (
	EmbeddingReconciliationReserved  EmbeddingReconciliationRunStatus = "reserved"
	EmbeddingReconciliationRunning   EmbeddingReconciliationRunStatus = "running"
	EmbeddingReconciliationCompleted EmbeddingReconciliationRunStatus = "completed"
	EmbeddingReconciliationDeferred  EmbeddingReconciliationRunStatus = "deferred"
	EmbeddingReconciliationFailed    EmbeddingReconciliationRunStatus = "failed"
	EmbeddingReconciliationAmbiguous EmbeddingReconciliationRunStatus = "ambiguous"
)
