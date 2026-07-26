package memoryservice

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type v2ReviewSourceCorrectionTarget struct {
	Ref            string
	SubjectRef     string
	PredicateKey   string
	ObjectRef      string
	ObjectValueKey string
	Target         verifier.V2RelationshipCorrectionTarget
}

type v2ReviewSourceConflictContext struct {
	Index          int
	Ref            string
	SubjectRef     string
	PredicateKey   string
	ObjectRef      string
	ObjectValueKey string
	Context        verifier.V2RelationshipConflictContext
}

func v2ReviewSourceCorrectionTargets(proposal map[string]any) []v2ReviewSourceCorrectionTarget {
	relationships := v2PlacementReviewObjectArray(proposal, "relationship_hints", "relationships")
	out := make([]v2ReviewSourceCorrectionTarget, 0, len(relationships))
	for i, raw := range relationships {
		target, ok := v2PlacementReviewCorrectionTarget(raw)
		if !ok {
			continue
		}
		objectRef := v2ReviewString(raw, "object_ref")
		if objectRef == "" && raw["object_value"] == nil {
			objectRef = v2ReviewString(raw, "object")
		}
		out = append(out, v2ReviewSourceCorrectionTarget{
			Ref:            v2ReviewFirstNonEmpty(v2ReviewString(raw, "proposal_id"), v2ReviewString(raw, "ref"), v2ReviewString(raw, "legacy_id"), fmt.Sprintf("relationship:%d", i)),
			SubjectRef:     v2ReviewFirstNonEmpty(v2ReviewString(raw, "subject_ref"), v2ReviewString(raw, "subject")),
			PredicateKey:   v2ReviewSourcePredicateKey(v2ReviewString(raw, "predicate")),
			ObjectRef:      objectRef,
			ObjectValueKey: v2ReviewSourceObjectValueKey(raw),
			Target:         target,
		})
	}
	return out
}

func v2ReviewSourceConflictContexts(proposal map[string]any) []v2ReviewSourceConflictContext {
	relationships := v2PlacementReviewObjectArray(proposal, "relationship_hints", "relationships")
	out := make([]v2ReviewSourceConflictContext, 0, len(relationships))
	for i, raw := range relationships {
		context, ok := v2PlacementReviewConflictContext(raw)
		if !ok {
			continue
		}
		objectRef := v2ReviewString(raw, "object_ref")
		if objectRef == "" && raw["object_value"] == nil {
			objectRef = v2ReviewString(raw, "object")
		}
		out = append(out, v2ReviewSourceConflictContext{
			Index:          i,
			Ref:            v2ReviewFirstNonEmpty(v2ReviewString(raw, "proposal_id"), v2ReviewString(raw, "ref"), v2ReviewString(raw, "legacy_id"), fmt.Sprintf("relationship:%d", i)),
			SubjectRef:     v2ReviewFirstNonEmpty(v2ReviewString(raw, "subject_ref"), v2ReviewString(raw, "subject")),
			PredicateKey:   v2ReviewSourcePredicateKey(v2ReviewString(raw, "predicate")),
			ObjectRef:      objectRef,
			ObjectValueKey: v2ReviewSourceObjectValueKey(raw),
			Context:        context,
		})
	}
	return out
}

func v2ReviewSourceProposalWithTrustedCorrectionTargets(
	proposal map[string]any,
	targets []v2ReviewSourceCorrectionTarget,
) map[string]any {
	if len(targets) == 0 {
		return proposal
	}
	relationships := v2PlacementReviewObjectArray(proposal, "relationship_hints", "relationships")
	used := map[int]struct{}{}
	for _, raw := range relationships {
		if _, ok := v2PlacementReviewCorrectionTarget(raw); ok {
			continue
		}
		index, ok := v2ReviewSourceMatchCorrectionTarget(raw, targets, used, len(relationships))
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

func v2ReviewSourceProposalWithTrustedConflictContexts(
	proposal map[string]any,
	contexts []v2ReviewSourceConflictContext,
) (map[string]any, []verifier.V2SemanticValidationError) {
	relationships := v2PlacementReviewObjectArray(proposal, "relationship_hints", "relationships")
	used := map[int]struct{}{}
	for _, raw := range relationships {
		delete(raw, "conflict_context")
		if len(contexts) == 0 {
			continue
		}
		index, ok := v2ReviewSourceMatchConflictContext(raw, contexts, used, len(relationships))
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
	errors := make([]verifier.V2SemanticValidationError, 0, len(contexts)-len(used))
	for i, context := range contexts {
		if _, ok := used[i]; ok {
			continue
		}
		errors = append(errors, verifier.V2SemanticValidationError{
			Field:   fmt.Sprintf("relationship_hints[%d].conflict_context", context.Index),
			Message: "could not be reattached after provider proposal rewrite",
		})
	}
	return proposal, errors
}

func v2ReviewSourceMatchConflictContext(
	raw map[string]any,
	contexts []v2ReviewSourceConflictContext,
	used map[int]struct{},
	relationshipCount int,
) (int, bool) {
	ref := v2ReviewFirstNonEmpty(v2ReviewString(raw, "proposal_id"), v2ReviewString(raw, "ref"), v2ReviewString(raw, "legacy_id"))
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
	probe := v2ReviewSourceConflictContext{
		SubjectRef:     v2ReviewFirstNonEmpty(v2ReviewString(raw, "subject_ref"), v2ReviewString(raw, "subject")),
		PredicateKey:   v2ReviewSourcePredicateKey(v2ReviewString(raw, "predicate")),
		ObjectRef:      v2ReviewFirstNonEmpty(v2ReviewString(raw, "object_ref"), v2ReviewString(raw, "object")),
		ObjectValueKey: v2ReviewSourceObjectValueKey(raw),
	}
	matches := make([]int, 0, 1)
	for i, context := range contexts {
		if _, exists := used[i]; exists {
			continue
		}
		if v2ReviewSourceConflictContextMatches(probe, context) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return 0, false
}

func v2ReviewSourceConflictContextMatches(probe v2ReviewSourceConflictContext, context v2ReviewSourceConflictContext) bool {
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

func v2ReviewSourceMatchCorrectionTarget(
	raw map[string]any,
	targets []v2ReviewSourceCorrectionTarget,
	used map[int]struct{},
	relationshipCount int,
) (int, bool) {
	ref := v2ReviewFirstNonEmpty(v2ReviewString(raw, "proposal_id"), v2ReviewString(raw, "ref"), v2ReviewString(raw, "legacy_id"))
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
	probe := v2ReviewSourceCorrectionTarget{
		SubjectRef:     v2ReviewFirstNonEmpty(v2ReviewString(raw, "subject_ref"), v2ReviewString(raw, "subject")),
		PredicateKey:   v2ReviewSourcePredicateKey(v2ReviewString(raw, "predicate")),
		ObjectRef:      v2ReviewFirstNonEmpty(v2ReviewString(raw, "object_ref"), v2ReviewString(raw, "object")),
		ObjectValueKey: v2ReviewSourceObjectValueKey(raw),
	}
	matches := make([]int, 0, 1)
	for i, target := range targets {
		if _, exists := used[i]; exists {
			continue
		}
		if v2ReviewSourceCorrectionTargetMatches(probe, target) {
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

func v2ReviewSourceCorrectionTargetMatches(probe v2ReviewSourceCorrectionTarget, target v2ReviewSourceCorrectionTarget) bool {
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

func v2ReviewSourceObjectValueKey(raw map[string]any) string {
	value, ok := v2PlacementReviewObjectValue(raw, "match")
	if !ok {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(value.Type),
		strings.TrimSpace(value.Value),
		strings.TrimSpace(value.Unit),
	}, "\x00")
}

func v2ReviewSourceRequestLocalPredicateCandidate(
	predicate string,
	relationship v2PlacementReviewRelationshipSpec,
	kindByRef map[string]string,
) repository.V2SemanticReviewPredicateCandidate {
	return repository.V2SemanticReviewPredicateCandidate{
		PredicateKey:        v2ReviewSourcePredicateKey(predicate),
		Version:             1,
		AllowedSubjectKinds: v2ReviewSourceAllowedKinds(kindByRef[relationship.SubjectRef], string(domain.V2EntityKindOther)),
		AllowedObjectKinds:  v2ReviewSourceAllowedKinds(v2ReviewSourceRelationshipObjectKind(relationship, kindByRef), string(domain.V2EntityKindOther)),
		RelationshipKind:    relationship.RelationshipKind,
		CurrentCardinality:  string(domain.V2CurrentCardinalityMany),
		LifecycleState:      string(domain.V2PredicateLifecycleActive),
	}
}

func v2ReviewSourcePredicateLabels(relationship v2PlacementReviewRelationshipSpec) []string {
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

func v2ReviewSourcePredicateResolutionsByLabel(
	resolutions []repository.V2SemanticReviewPredicateResolution,
) map[string][]repository.V2SemanticReviewPredicateResolution {
	out := make(map[string][]repository.V2SemanticReviewPredicateResolution)
	for _, resolution := range resolutions {
		label := strings.TrimSpace(resolution.RequestedPredicate)
		if label != "" {
			out[label] = append(out[label], resolution)
		}
	}
	return out
}

func v2ReviewSourceCanonicalPredicateCandidate(
	resolutions []repository.V2SemanticReviewPredicateResolution,
) (repository.V2SemanticReviewPredicateCandidate, bool, bool) {
	for _, resolution := range resolutions {
		if resolution.MatchKind == "key" {
			return resolution.Candidate, true, false
		}
	}
	byKey := map[string]repository.V2SemanticReviewPredicateCandidate{}
	for _, resolution := range resolutions {
		key := strings.TrimSpace(resolution.Candidate.PredicateKey)
		if key != "" {
			byKey[key] = resolution.Candidate
		}
	}
	if len(byKey) == 0 {
		return repository.V2SemanticReviewPredicateCandidate{}, false, false
	}
	if len(byKey) > 1 {
		return repository.V2SemanticReviewPredicateCandidate{}, false, true
	}
	for _, candidate := range byKey {
		return candidate, true, false
	}
	return repository.V2SemanticReviewPredicateCandidate{}, false, false
}

func v2ReviewSourceAllowedKinds(value string, fallback string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return []string{value}
}

func v2ReviewSourcePredicateKey(value string) string {
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
