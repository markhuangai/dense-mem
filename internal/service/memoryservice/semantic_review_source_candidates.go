package memoryservice

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type reviewSourceCorrectionTarget struct {
	Ref            string
	SubjectRef     string
	PredicateKey   string
	ObjectRef      string
	ObjectValueKey string
	Target         verifier.RelationshipCorrectionTarget
}

type reviewSourceConflictContext struct {
	Index          int
	Ref            string
	SubjectRef     string
	PredicateKey   string
	ObjectRef      string
	ObjectValueKey string
	Context        verifier.RelationshipConflictContext
}

func reviewSourceCorrectionTargets(proposal map[string]any) []reviewSourceCorrectionTarget {
	relationships := placementReviewObjectArray(proposal, "relationship_hints", "relationships")
	out := make([]reviewSourceCorrectionTarget, 0, len(relationships))
	for i, raw := range relationships {
		target, ok := placementReviewCorrectionTarget(raw)
		if !ok {
			continue
		}
		objectRef := reviewString(raw, "object_ref")
		if objectRef == "" && raw["object_value"] == nil {
			objectRef = reviewString(raw, "object")
		}
		out = append(out, reviewSourceCorrectionTarget{
			Ref:            reviewFirstNonEmpty(reviewString(raw, "proposal_id"), reviewString(raw, "ref"), reviewString(raw, "legacy_id"), fmt.Sprintf("relationship:%d", i)),
			SubjectRef:     reviewFirstNonEmpty(reviewString(raw, "subject_ref"), reviewString(raw, "subject")),
			PredicateKey:   reviewSourcePredicateKey(reviewString(raw, "predicate")),
			ObjectRef:      objectRef,
			ObjectValueKey: reviewSourceObjectValueKey(raw),
			Target:         target,
		})
	}
	return out
}

func reviewSourceConflictContexts(proposal map[string]any) []reviewSourceConflictContext {
	relationships := placementReviewObjectArray(proposal, "relationship_hints", "relationships")
	out := make([]reviewSourceConflictContext, 0, len(relationships))
	for i, raw := range relationships {
		context, ok := placementReviewConflictContext(raw)
		if !ok {
			continue
		}
		objectRef := reviewString(raw, "object_ref")
		if objectRef == "" && raw["object_value"] == nil {
			objectRef = reviewString(raw, "object")
		}
		out = append(out, reviewSourceConflictContext{
			Index:          i,
			Ref:            reviewFirstNonEmpty(reviewString(raw, "proposal_id"), reviewString(raw, "ref"), reviewString(raw, "legacy_id"), fmt.Sprintf("relationship:%d", i)),
			SubjectRef:     reviewFirstNonEmpty(reviewString(raw, "subject_ref"), reviewString(raw, "subject")),
			PredicateKey:   reviewSourcePredicateKey(reviewString(raw, "predicate")),
			ObjectRef:      objectRef,
			ObjectValueKey: reviewSourceObjectValueKey(raw),
			Context:        context,
		})
	}
	return out
}

func reviewSourceProposalWithTrustedCorrectionTargets(
	proposal map[string]any,
	targets []reviewSourceCorrectionTarget,
) map[string]any {
	if len(targets) == 0 {
		return proposal
	}
	relationships := placementReviewObjectArray(proposal, "relationship_hints", "relationships")
	used := map[int]struct{}{}
	for _, raw := range relationships {
		if _, ok := placementReviewCorrectionTarget(raw); ok {
			continue
		}
		index, ok := reviewSourceMatchCorrectionTarget(raw, targets, used, len(relationships))
		if !ok {
			continue
		}
		used[index] = struct{}{}
		target := targets[index].Target
		raw["correction_target"] = map[string]any{
			"relationship_id":  target.RelationshipID,
			"expected_version": target.ExpectedVersion,
		}
	}
	return proposal
}

func reviewSourceProposalWithTrustedConflictContexts(
	proposal map[string]any,
	contexts []reviewSourceConflictContext,
) (map[string]any, []verifier.SemanticValidationError) {
	relationships := placementReviewObjectArray(proposal, "relationship_hints", "relationships")
	used := map[int]struct{}{}
	for _, raw := range relationships {
		delete(raw, "conflict_context")
		if len(contexts) == 0 {
			continue
		}
		index, ok := reviewSourceMatchConflictContext(raw, contexts, used, len(relationships))
		if !ok {
			continue
		}
		used[index] = struct{}{}
		context := contexts[index].Context
		raw["conflict_context"] = map[string]any{
			"conflict_id":      context.ConflictID,
			"expected_version": context.ExpectedVersion,
		}
	}
	if len(used) == len(contexts) {
		return proposal, nil
	}
	errors := make([]verifier.SemanticValidationError, 0, len(contexts)-len(used))
	for i, context := range contexts {
		if _, ok := used[i]; ok {
			continue
		}
		errors = append(errors, verifier.SemanticValidationError{
			Field:   fmt.Sprintf("relationship_hints[%d].conflict_context", context.Index),
			Message: "could not be reattached after provider proposal rewrite",
		})
	}
	return proposal, errors
}

func reviewSourceMatchConflictContext(
	raw map[string]any,
	contexts []reviewSourceConflictContext,
	used map[int]struct{},
	relationshipCount int,
) (int, bool) {
	ref := reviewFirstNonEmpty(reviewString(raw, "proposal_id"), reviewString(raw, "ref"), reviewString(raw, "legacy_id"))
	if ref != "" {
		for i, context := range contexts {
			if _, exists := used[i]; exists {
				continue
			}
			if context.Ref == ref {
				return i, true
			}
		}
	}
	probe := reviewSourceConflictContext{
		SubjectRef:     reviewFirstNonEmpty(reviewString(raw, "subject_ref"), reviewString(raw, "subject")),
		PredicateKey:   reviewSourcePredicateKey(reviewString(raw, "predicate")),
		ObjectRef:      reviewFirstNonEmpty(reviewString(raw, "object_ref"), reviewString(raw, "object")),
		ObjectValueKey: reviewSourceObjectValueKey(raw),
	}
	matches := make([]int, 0, 1)
	for i, context := range contexts {
		if _, exists := used[i]; exists {
			continue
		}
		if reviewSourceConflictContextMatches(probe, context) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return 0, false
}

func reviewSourceConflictContextMatches(probe reviewSourceConflictContext, context reviewSourceConflictContext) bool {
	if probe.SubjectRef == "" || context.SubjectRef == "" || probe.SubjectRef != context.SubjectRef {
		return false
	}
	if probe.PredicateKey == "" || context.PredicateKey == "" || probe.PredicateKey != context.PredicateKey {
		return false
	}
	if probe.ObjectRef != "" || context.ObjectRef != "" {
		return probe.ObjectRef != "" && probe.ObjectRef == context.ObjectRef
	}
	return probe.ObjectValueKey != "" && probe.ObjectValueKey == context.ObjectValueKey
}

func reviewSourceMatchCorrectionTarget(
	raw map[string]any,
	targets []reviewSourceCorrectionTarget,
	used map[int]struct{},
	relationshipCount int,
) (int, bool) {
	ref := reviewFirstNonEmpty(reviewString(raw, "proposal_id"), reviewString(raw, "ref"), reviewString(raw, "legacy_id"))
	if ref != "" {
		for i, target := range targets {
			if _, exists := used[i]; exists {
				continue
			}
			if target.Ref == ref {
				return i, true
			}
		}
	}
	probe := reviewSourceCorrectionTarget{
		SubjectRef:     reviewFirstNonEmpty(reviewString(raw, "subject_ref"), reviewString(raw, "subject")),
		PredicateKey:   reviewSourcePredicateKey(reviewString(raw, "predicate")),
		ObjectRef:      reviewFirstNonEmpty(reviewString(raw, "object_ref"), reviewString(raw, "object")),
		ObjectValueKey: reviewSourceObjectValueKey(raw),
	}
	matches := make([]int, 0, 1)
	for i, target := range targets {
		if _, exists := used[i]; exists {
			continue
		}
		if reviewSourceCorrectionTargetMatches(probe, target) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	if relationshipCount == 1 && len(targets)-len(used) == 1 {
		for i := range targets {
			if _, exists := used[i]; !exists {
				return i, true
			}
		}
	}
	return 0, false
}

func reviewSourceCorrectionTargetMatches(probe reviewSourceCorrectionTarget, target reviewSourceCorrectionTarget) bool {
	if probe.SubjectRef == "" || target.SubjectRef == "" || probe.SubjectRef != target.SubjectRef {
		return false
	}
	if probe.PredicateKey == "" || target.PredicateKey == "" || probe.PredicateKey != target.PredicateKey {
		return false
	}
	if probe.ObjectRef != "" || target.ObjectRef != "" {
		return probe.ObjectRef != "" && probe.ObjectRef == target.ObjectRef
	}
	return probe.ObjectValueKey != "" && probe.ObjectValueKey == target.ObjectValueKey
}

func reviewSourceObjectValueKey(raw map[string]any) string {
	value, ok := placementReviewObjectValue(raw, "match")
	if !ok {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(value.Type),
		strings.TrimSpace(value.Value),
		strings.TrimSpace(value.Unit),
	}, "\x00")
}

func reviewSourceRequestLocalPredicateCandidate(
	predicate string,
	relationship placementReviewRelationshipSpec,
	kindByRef map[string]string,
) repository.SemanticReviewPredicateCandidate {
	return repository.SemanticReviewPredicateCandidate{
		PredicateKey:        reviewSourcePredicateKey(predicate),
		Version:             1,
		AllowedSubjectKinds: reviewSourceAllowedKinds(kindByRef[relationship.SubjectRef], string(domain.EntityKindOther)),
		AllowedObjectKinds:  reviewSourceAllowedKinds(reviewSourceRelationshipObjectKind(relationship, kindByRef), string(domain.EntityKindOther)),
		RelationshipKind:    relationship.RelationshipKind,
		CurrentCardinality:  string(domain.CurrentCardinalityMany),
		LifecycleState:      string(domain.PredicateLifecycleActive),
	}
}

func reviewSourcePredicateLabels(relationship placementReviewRelationshipSpec) []string {
	labels := make([]string, 0, len(relationship.PredicateCandidates)+1)
	seen := map[string]struct{}{}
	appendLabel := func(label string) {
		label = strings.TrimSpace(label)
		if label == "" {
			return
		}
		if _, exists := seen[label]; exists {
			return
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	appendLabel(relationship.Predicate)
	for _, candidate := range relationship.PredicateCandidates {
		appendLabel(candidate)
	}
	return labels
}

func reviewSourcePredicateResolutionsByLabel(
	resolutions []repository.SemanticReviewPredicateResolution,
) map[string][]repository.SemanticReviewPredicateResolution {
	out := make(map[string][]repository.SemanticReviewPredicateResolution)
	for _, resolution := range resolutions {
		label := strings.TrimSpace(resolution.RequestedPredicate)
		if label != "" {
			out[label] = append(out[label], resolution)
		}
	}
	return out
}

func reviewSourceCanonicalPredicateCandidate(
	resolutions []repository.SemanticReviewPredicateResolution,
) (repository.SemanticReviewPredicateCandidate, bool, bool) {
	for _, resolution := range resolutions {
		if resolution.MatchKind == "key" {
			return resolution.Candidate, true, false
		}
	}
	byKey := map[string]repository.SemanticReviewPredicateCandidate{}
	for _, resolution := range resolutions {
		key := strings.TrimSpace(resolution.Candidate.PredicateKey)
		if key != "" {
			byKey[key] = resolution.Candidate
		}
	}
	if len(byKey) == 0 {
		return repository.SemanticReviewPredicateCandidate{}, false, false
	}
	if len(byKey) > 1 {
		return repository.SemanticReviewPredicateCandidate{}, false, true
	}
	for _, candidate := range byKey {
		return candidate, true, false
	}
	return repository.SemanticReviewPredicateCandidate{}, false, false
}

func reviewSourceAllowedKinds(value string, fallback string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return []string{value}
}

func reviewSourcePredicateKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	out := make([]rune, 0, len(value))
	lastUnderscore := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, r)
			lastUnderscore = false
			continue
		}
		if len(out) == 0 || lastUnderscore {
			continue
		}
		out = append(out, '_')
		lastUnderscore = true
	}
	for len(out) > 0 && out[len(out)-1] == '_' {
		out = out[:len(out)-1]
	}
	if len(out) > 64 {
		out = out[:64]
		for len(out) > 0 && out[len(out)-1] == '_' {
			out = out[:len(out)-1]
		}
	}
	return string(out)
}
