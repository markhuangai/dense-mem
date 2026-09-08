package repository

import dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"

type DreamRepository = dreamcontract.DreamRepository
type ScheduledDreamRepository = dreamcontract.ScheduledDreamRepository
type DreamCycleClaimInput = dreamcontract.DreamCycleClaimInput
type DreamCycleRecoveryClaimInput = dreamcontract.DreamCycleRecoveryClaimInput
type DreamCycleCompleteInput = dreamcontract.DreamCycleCompleteInput
type DreamCycleRun = dreamcontract.DreamCycleRun
type DreamInputListInput = dreamcontract.DreamInputListInput
type DreamInput = dreamcontract.DreamInput
type DreamEvidence = dreamcontract.DreamEvidence
type DreamTargetPredicate = dreamcontract.DreamTargetPredicate
type DreamTargetCandidate = dreamcontract.DreamTargetCandidate
type DreamPathEvaluationInput = dreamcontract.DreamPathEvaluationInput
type DreamPathEvaluationRecordInput = dreamcontract.DreamPathEvaluationRecordInput
type DreamGenerationPersistInput = dreamcontract.DreamGenerationPersistInput
type DreamGenerationPersistResult = dreamcontract.DreamGenerationPersistResult
type UpsertHypothesisInput = dreamcontract.UpsertHypothesisInput
type EvidenceDerivationSource = dreamcontract.EvidenceDerivationSource
type DreamDerivationSource = dreamcontract.DreamDerivationSource
type HypothesisRecord = dreamcontract.HypothesisRecord
type ListHypothesesInput = dreamcontract.ListHypothesesInput
type EvidenceTarget = dreamcontract.EvidenceTarget
type EvidenceContext = dreamcontract.EvidenceContext
type EvidenceDiscoverySelectionInput = dreamcontract.EvidenceDiscoverySelectionInput
type EvidenceDiscoveryAttempt = dreamcontract.EvidenceDiscoveryAttempt
type EvidenceDiscoveryAttemptValidationInput = dreamcontract.EvidenceDiscoveryAttemptValidationInput
type EvidenceDiscoveryTargetInput = dreamcontract.EvidenceDiscoveryTargetInput
type EvidenceNode = dreamcontract.EvidenceNode
type EvidenceDiscoveryEvaluationInput = dreamcontract.EvidenceDiscoveryEvaluationInput
type EvidenceDiscoveryRunTotals = dreamcontract.EvidenceDiscoveryRunTotals
type GetHypothesisInput = dreamcontract.GetHypothesisInput
type RecallHypothesesInput = dreamcontract.RecallHypothesesInput
type UpdateHypothesisStatusInput = dreamcontract.UpdateHypothesisStatusInput
type SubmitHypothesisInput = dreamcontract.SubmitHypothesisInput
type DreamControlRepository = dreamcontract.DreamControlRepository
type EvidenceDiscoveryRepository = dreamcontract.EvidenceDiscoveryRepository
type EvidenceDiscoveryInputValidator = dreamcontract.EvidenceDiscoveryInputValidator
type EvidenceDiscoveryDuplicateError = dreamcontract.EvidenceDiscoveryDuplicateError

var (
	ErrDreamCycleAlreadyClaimed            = dreamcontract.ErrDreamCycleAlreadyClaimed
	ErrDreamHypothesisNotFound             = dreamcontract.ErrDreamHypothesisNotFound
	ErrDreamHypothesisIDInvalid            = dreamcontract.ErrDreamHypothesisIDInvalid
	ErrDreamSourceStale                    = dreamcontract.ErrDreamSourceStale
	ErrDreamExactRelationshipExists        = dreamcontract.ErrDreamExactRelationshipExists
	ErrDreamExactHypothesisExists          = dreamcontract.ErrDreamExactHypothesisExists
	ErrDreamCycleLeaseLost                 = dreamcontract.ErrDreamCycleLeaseLost
	ErrDreamConfirmationBusy               = dreamcontract.ErrDreamConfirmationBusy
	ErrEvidenceDiscoveryBusy               = dreamcontract.ErrEvidenceDiscoveryBusy
	ErrEvidenceDiscoveryAttemptNotReserved = dreamcontract.ErrEvidenceDiscoveryAttemptNotReserved
)
