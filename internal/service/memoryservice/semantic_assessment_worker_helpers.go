package memoryservice

import (
	"strconv"
	"strings"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func attachSemanticAssessmentTrustedRelationshipContext(
	observation *repository.PlacementRelationshipDecisionInput,
	context semanticAssessmentTrustedRelationshipContext,
) {
	if observation == nil {
		return
	}
	if context.correctionTarget != nil {
		target := *context.correctionTarget
		observation.CorrectionTarget = &target
	}
	if context.conflictContext != nil {
		conflict := *context.conflictContext
		observation.ConflictContext = &conflict
	}
}

func attachSemanticAssessmentTrustedRelationshipContextToReview(
	review *repository.PlacementRelationshipReviewInput,
	context semanticAssessmentTrustedRelationshipContext,
) {
	if review == nil {
		return
	}
	if context.correctionTarget != nil {
		target := *context.correctionTarget
		review.CorrectionTarget = &target
	}
	if context.conflictContext != nil {
		conflict := *context.conflictContext
		review.ConflictContext = &conflict
	}
}

func assessmentGroupsBySpan(groups []verifier.SemanticAssessmentEntityCandidateGroup) map[string]*verifier.SemanticAssessmentEntityCandidateGroup {
	result := make(map[string]*verifier.SemanticAssessmentEntityCandidateGroup, len(groups))
	for index := range groups {
		group := &groups[index]
		result[assessmentCandidateGroupKey(group.EvidenceID, group.Start, group.End)] = group
	}
	return result
}

func assessmentPredicatesByKeyVersion(options []verifier.SemanticAssessmentPredicateOption) map[string]verifier.SemanticAssessmentPredicateOption {
	result := make(map[string]verifier.SemanticAssessmentPredicateOption, len(options))
	for _, option := range options {
		result[assessmentPredicateKey(option.PredicateKey, option.Version)] = option
	}
	return result
}

func assessmentPredicateKey(key string, version int) string {
	return strings.TrimSpace(key) + ":" + strconv.Itoa(version)
}

func assessmentPolicyVersion(policy repository.AutoWriteConfidencePolicy) string {
	version := strings.TrimSpace(policy.Version)
	if version == "" {
		version = repository.AssessmentPolicyVersion
	}
	return version + ":config-" + strconv.FormatInt(policy.ConfigVersion, 10)
}
