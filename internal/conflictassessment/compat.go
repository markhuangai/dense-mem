// Package conflictassessment preserves the historical assessor import path.
// The Conflict capability owns the schema and complete-response validation.
package conflictassessment

import native "github.com/markhuangai/dense-mem/internal/conflict/assessment"

const (
	ConflictAssessmentSchemaName            = native.ConflictAssessmentSchemaName
	ConflictAssessmentDecisionSelect        = native.ConflictAssessmentDecisionSelect
	ConflictAssessmentDecisionAbstain       = native.ConflictAssessmentDecisionAbstain
	ConflictAssessmentMaxPositions          = native.ConflictAssessmentMaxPositions
	ConflictAssessmentMaxEvidence           = native.ConflictAssessmentMaxEvidence
	ConflictAssessmentMaxContent            = native.ConflictAssessmentMaxContent
	ConflictAssessmentMaxRationale          = native.ConflictAssessmentMaxRationale
	ConflictAssessmentSystemPrompt          = native.ConflictAssessmentSystemPrompt
	ConflictAssessmentCorrectionInstruction = native.ConflictAssessmentCorrectionInstruction
	ProviderFailureClassHTTPServer          = native.ProviderFailureClassHTTPServer
	ProviderFailureClassProviderUnavailable = native.ProviderFailureClassProviderUnavailable
)

type (
	ConflictAssessmentEvidence = native.ConflictAssessmentEvidence
	ConflictAssessmentPosition = native.ConflictAssessmentPosition
	ConflictAssessmentRequest  = native.ConflictAssessmentRequest
	ConflictAssessmentResponse = native.ConflictAssessmentResponse
	Provider                   = native.Provider
	SemanticAssessmentLimits   = native.SemanticAssessmentLimits
	ProviderError              = native.ProviderError
	MalformedResponseError     = native.MalformedResponseError
	ProviderFailureMetadata    = native.ProviderFailureMetadata
)

var (
	ErrVerifierMalformedResponse = native.ErrVerifierMalformedResponse
	ProviderFailureDetails       = native.ProviderFailureDetails
)

var (
	ConflictAssessmentResponseSchema     = native.ConflictAssessmentResponseSchema
	PrepareConflictAssessmentRequest     = native.PrepareConflictAssessmentRequest
	DecodeConflictAssessmentResponseJSON = native.DecodeConflictAssessmentResponseJSON
	ValidateConflictAssessmentResponse   = native.ValidateConflictAssessmentResponse
	DefaultSemanticAssessmentLimits      = native.DefaultSemanticAssessmentLimits
)
