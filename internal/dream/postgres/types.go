package postgres

import dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"

type DreamCycleRun = dreamcontract.DreamCycleRun

type EvidenceDerivationSource = dreamcontract.EvidenceDerivationSource
type DreamDerivationSource = dreamcontract.DreamDerivationSource
type HypothesisRecord = dreamcontract.HypothesisRecord
type ListHypothesesInput = dreamcontract.ListHypothesesInput
type GetHypothesisInput = dreamcontract.GetHypothesisInput

type DreamControlRepository = dreamcontract.DreamControlRepository
type DreamDiagnosticRepository = dreamcontract.DreamDiagnosticRepository

var (
	ErrDreamHypothesisNotFound  = dreamcontract.ErrDreamHypothesisNotFound
	ErrDreamHypothesisIDInvalid = dreamcontract.ErrDreamHypothesisIDInvalid
)
