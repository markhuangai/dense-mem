package service

import remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"

var (
	ErrIdempotencyConflict    = remembercontract.ErrIdempotencyConflict
	ErrSourceRevisionConflict = remembercontract.ErrSourceRevisionConflict
	ErrTeamInactive           = remembercontract.ErrTeamInactive
)

type (
	RememberValidationIssue      = remembercontract.RememberValidationIssue
	RememberValidationError      = remembercontract.RememberValidationError
	EvidenceInput                = remembercontract.EvidenceInput
	SecurityEventDraft           = remembercontract.SecurityEventDraft
	SecuritySignalInput          = remembercontract.SecuritySignalInput
	EvidenceFragment             = remembercontract.EvidenceFragment
	SecurityRejectionAuditor     = remembercontract.SecurityRejectionAuditor
	SecurityRejectionAuditInput  = remembercontract.SecurityRejectionAuditInput
	SecurityRejectionAuditSignal = remembercontract.SecurityRejectionAuditSignal
)
