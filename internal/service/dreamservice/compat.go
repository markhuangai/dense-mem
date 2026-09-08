// Package dreamservice preserves the pre-cutover import path as a bounded
// compatibility facade. Dream policy and contracts live in internal/dream.
package dreamservice

import (
	"log/slog"

	"github.com/markhuangai/dense-mem/internal/assessor"
	dream "github.com/markhuangai/dense-mem/internal/dream"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

type (
	ConfirmationBusyError     = dream.ConfirmationBusyError
	AppConfig                 = dream.AppConfig
	TeamService               = dream.TeamService
	TeamConfigService         = dream.TeamConfigService
	Generator                 = dream.Generator
	EvidenceGenerator         = dream.EvidenceGenerator
	RememberService           = dream.RememberService
	Dependencies              = dream.Dependencies
	Service                   = dream.Service
	RunCycleRequest           = dream.RunCycleRequest
	RunCycleResult            = dream.RunCycleResult
	ListOptions               = dream.ListOptions
	ResolveFeedbackRequest    = dream.ResolveFeedbackRequest
	ResolveFeedbackResult     = dream.ResolveFeedbackResult
	StatusResult              = dream.StatusResult
	EffectiveConfig           = dream.EffectiveConfig
	GenerateRequest           = dream.GenerateRequest
	DreamInput                = dream.DreamInput
	GeneratedDream            = dream.GeneratedDream
	EvidenceGenerationRequest = dream.EvidenceGenerationRequest
	GenerationDiagnostics     = dream.GenerationDiagnostics
	DiagnosticsGenerator      = dream.DiagnosticsGenerator
	DreamPath                 = dream.DreamPath
	DreamPathNode             = dream.DreamPathNode
	DreamPathPremise          = dream.DreamPathPremise
	SeedDream                 = dream.SeedDream
	ControlDependencies       = dream.ControlDependencies
	ControlService            = dream.ControlService
	Scheduler                 = dream.Scheduler
	ProviderGenerator         = dream.ProviderGenerator
	EvidenceProviderGenerator = dream.EvidenceProviderGenerator
	HeuristicGenerator        = dream.HeuristicGenerator
)

const (
	DefaultStartTimeLocal = dream.DefaultStartTimeLocal
	DefaultTimezone       = dream.DefaultTimezone
	DefaultMaxOutputs     = dream.DefaultMaxOutputs
	DreamSortUpdatedAt    = dream.DreamSortUpdatedAt
	DreamSortCreatedAt    = dream.DreamSortCreatedAt
	DreamDirectionAsc     = dream.DreamDirectionAsc
	DreamDirectionDesc    = dream.DreamDirectionDesc
)

var (
	ErrDreamNotFound             = dream.ErrDreamNotFound
	ErrInvalidDreamStatus        = dream.ErrInvalidDreamStatus
	ErrDreamFeedbackInvalidInput = dream.ErrDreamFeedbackInvalidInput
	ErrDreamProviderUnavailable  = dream.ErrDreamProviderUnavailable
	ErrDreamAuthContext          = dream.ErrDreamAuthContext
)

var (
	New                  = dream.New
	NewControl           = dream.NewControl
	NewProviderGenerator = dream.NewProviderGenerator
	NewScheduler         = func(service Service, teams TeamService, logger *slog.Logger) *Scheduler {
		return dream.NewScheduler(service, teams, logger)
	}
	NewHeuristicGenerator   = dream.NewHeuristicGenerator
	EffectiveDreamingConfig = dream.EffectiveDreamingConfig
)

// NewEvidenceProviderGenerator forwards provider construction to the Dream
// owner while keeping the old package usable by external integrations.
func NewEvidenceProviderGenerator(transport modelprovider.StructuredTransport, model string, limits assessor.SemanticAssessmentLimits) *EvidenceProviderGenerator {
	return dream.NewEvidenceProviderGenerator(transport, model, limits)
}
