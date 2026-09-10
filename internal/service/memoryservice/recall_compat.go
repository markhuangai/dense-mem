// Package memoryservice keeps the pre-cutover Recall names available to
// transitional callers. Recall policy and contracts live in internal/recall.
package memoryservice

import recall "github.com/markhuangai/dense-mem/internal/recall"

type (
	RecallService                     = recall.RecallService
	RecallDependencies                = recall.RecallDependencies
	RecallSearchRepository            = recall.RecallSearchRepository
	RecallHypothesisRepository        = recall.RecallHypothesisRepository
	RecallCommunityRepository         = recall.RecallCommunityRepository
	RecallCommunitySnapshotRepository = recall.RecallCommunitySnapshotRepository
	RecallCommunityCoverageRepository = recall.RecallCommunityCoverageRepository
	RecallCommunityRunRepository      = recall.RecallCommunityRunRepository
	RecallCommunityConfigProvider     = recall.RecallCommunityConfigProvider
	RecallRequest                     = recall.RecallRequest
	RecallResult                      = recall.RecallResult
	RecallSuggestedAction             = recall.RecallSuggestedAction
	RecallResultItem                  = recall.RecallResultItem
	RecallDiscoveryPath               = recall.RecallDiscoveryPath
	RecallCommunity                   = recall.RecallCommunity
	RecallConflictSummary             = recall.RecallConflictSummary
	RecallRelationshipHandle          = recall.RecallRelationshipHandle
	RelatedRelationshipSummary        = recall.RelatedRelationshipSummary
	EntityHandle                      = recall.EntityHandle
	SemanticObject                    = recall.SemanticObject
	RelatedHypothesisSummary          = recall.RelatedHypothesisSummary
	RecallDegradationResult           = recall.RecallDegradationResult
	RecallSearchStates                = recall.RecallSearchStates
	RecallConflictPosition            = recall.RecallConflictPosition
	RecallConflictSupporter           = recall.RecallConflictSupporter
)

var (
	ErrRecallAuthContext            = recall.ErrRecallAuthContext
	ErrRecallRepositoryTeamMismatch = recall.ErrRecallRepositoryTeamMismatch
	NewRecallService                = recall.NewRecallService
)
