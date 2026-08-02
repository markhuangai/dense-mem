package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func resolveSubmissionPlacementPredicateCandidate(
	ctx context.Context,
	tx *gorm.DB,
	decision ApplyRelationshipDecisionInput,
	candidate PlacementPredicateCandidateInput,
) (ApplyRelationshipDecisionInput, error) {
	canonicalKey := canonicalGeneratedPredicateKey(candidate.PredicateKey)
	canonicalOriginal := canonicalGeneratedPredicateKey(decision.OriginalPredicate)
	if !placementNovelPredicateSafe(candidate.PredicateKey, canonicalKey, decision.OriginalPredicate) {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate candidate %q is not a safe canonical key",
			errPlacementPredicateReview,
			candidate.PredicateKey,
		)
	}
	if err := tx.WithContext(ctx).Exec(
		`SELECT pg_advisory_xact_lock(hashtext(?))`,
		decision.TeamID+":"+canonicalKey,
	).Error; err != nil {
		return ApplyRelationshipDecisionInput{}, err
	}
	matches, err := loadPlacementPredicateMatches(
		ctx,
		tx,
		decision.TeamID,
		canonicalKey,
		decision.OriginalPredicate,
		canonicalOriginal,
	)
	if err != nil {
		return ApplyRelationshipDecisionInput{}, err
	}
	subjectKind, objectKind, err := loadPlacementPredicateEndpointKinds(ctx, tx, decision)
	if err != nil {
		return ApplyRelationshipDecisionInput{}, err
	}
	if len(matches) == 1 {
		match := matches[0]
		if match.LifecycleState == string(domain.PredicateLifecycleActive) &&
			match.RelationshipKind == candidate.RelationshipKind &&
			placementPredicateKindAllowed(match.AllowedSubjectKinds, subjectKind) &&
			placementPredicateKindAllowed(match.AllowedObjectKinds, objectKind) {
			decision.PredicateKey = match.PredicateKey
			decision.PredicateVersion = match.Version
			return decision, nil
		}
	}
	registrationKey := canonicalKey
	collision := len(matches) > 0
	if collision {
		registrationKey = collisionGeneratedPredicateKey(canonicalKey, candidate.RelationshipKind, decision.OriginalPredicate)
		if err := tx.WithContext(ctx).Exec(
			`SELECT pg_advisory_xact_lock(hashtext(?))`,
			decision.TeamID+":"+registrationKey,
		).Error; err != nil {
			return ApplyRelationshipDecisionInput{}, err
		}
	}
	input := EnsureSemanticPredicateCandidateInput{
		TeamID:           decision.TeamID,
		OwnerProfileID:   decision.OwnerProfileID,
		Predicate:        decision.OriginalPredicate,
		RelationshipKind: candidate.RelationshipKind,
		SubjectKind:      subjectKind,
		ObjectKind:       objectKind,
		Origin:           "submission_assessor",
		Metadata: map[string]any{
			"source":                           "submission_assessment",
			"assessment_id":                    decision.SubmissionAssessmentID,
			"assessment_policy":                decision.AssessmentPolicyVersion,
			"predicate_policy_version":         domain.PredicatePolicyVersion,
			"assessor_predicate_candidate_key": candidate.PredicateKey,
		},
	}
	resolved, err := insertTeamPredicateCandidate(ctx, tx, input, registrationKey, collision)
	if err != nil {
		return ApplyRelationshipDecisionInput{}, err
	}
	resolved, err = ensureTeamPredicateCandidateKinds(ctx, tx, input, *resolved)
	if err != nil {
		return ApplyRelationshipDecisionInput{}, err
	}
	if resolved.LifecycleState != string(domain.PredicateLifecycleActive) {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q lifecycle is %q",
			errPlacementPredicateReview,
			resolved.PredicateKey,
			resolved.LifecycleState,
		)
	}
	if resolved.RelationshipKind != candidate.RelationshipKind {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q relationship_kind is %q, candidate requested %q",
			errPlacementPredicateReview,
			resolved.PredicateKey,
			resolved.RelationshipKind,
			candidate.RelationshipKind,
		)
	}
	decision.PredicateKey = resolved.PredicateKey
	decision.PredicateVersion = resolved.Version
	return decision, nil
}
