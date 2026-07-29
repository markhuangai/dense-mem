package memoryservice

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func (s *semanticAssessmentPlacementWorkerService) prefetchEntityCandidates(
	ctx context.Context,
	run repository.PlacementRun,
	fragment repository.EvidenceFragment,
	proposal map[string]any,
	evidenceID string,
) ([]verifier.SemanticAssessmentEntityCandidateGroup, bool, error) {
	matches, err := s.catalog.ListSemanticAssessmentEntityMatches(ctx, repository.SemanticAssessmentEntityMatchInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		EvidenceText:   fragment.Content,
		Limit:          1000,
	})
	if err != nil {
		return nil, false, fmt.Errorf("load exact entity candidates: %w", err)
	}
	groups := map[string]*verifier.SemanticAssessmentEntityCandidateGroup{}
	truncated := matches.Truncated
	textIndex := newAssessmentTextIndex(fragment.Content)
	spansByMatchedName := make(map[string][]assessmentTextSpan)
	for _, match := range matches.Matches {
		candidate := assessmentEntityCandidate(match.Candidate)
		spans, exists := spansByMatchedName[match.MatchedName]
		if !exists {
			spans = textIndex.exactTokenSpans(match.MatchedName)
			spansByMatchedName[match.MatchedName] = spans
		}
		for _, span := range spans {
			key := assessmentCandidateGroupKey(evidenceID, span.start, span.end)
			group := groups[key]
			if group == nil {
				group = &verifier.SemanticAssessmentEntityCandidateGroup{
					Surface:    span.surface,
					EvidenceID: evidenceID,
					Start:      span.start,
					End:        span.end,
				}
				groups[key] = group
			}
			addAssessmentEntityCandidate(group, candidate)
		}
	}
	knownGroupKeys := map[string]struct{}{}
	if err := s.addKnownEntityHintCandidates(ctx, run, fragment, proposal, evidenceID, textIndex, groups, knownGroupKeys); err != nil {
		return nil, false, err
	}
	ordered := make([]verifier.SemanticAssessmentEntityCandidateGroup, 0, len(groups))
	for _, group := range groups {
		if truncated {
			group.CandidateContextTruncated = true
		}
		sort.Slice(group.Candidates, func(left, right int) bool {
			return group.Candidates[left].EntityID < group.Candidates[right].EntityID
		})
		ordered = append(ordered, *group)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return assessmentCandidateGroupLess(ordered[left], ordered[right])
	})
	if len(ordered) > verifier.SemanticAssessmentMaxEntityResults {
		sort.SliceStable(ordered, func(left, right int) bool {
			leftKey := assessmentCandidateGroupKey(ordered[left].EvidenceID, ordered[left].Start, ordered[left].End)
			rightKey := assessmentCandidateGroupKey(ordered[right].EvidenceID, ordered[right].Start, ordered[right].End)
			_, leftKnown := knownGroupKeys[leftKey]
			_, rightKnown := knownGroupKeys[rightKey]
			return leftKnown && !rightKnown
		})
		ordered = ordered[:verifier.SemanticAssessmentMaxEntityResults]
		truncated = true
		for i := range ordered {
			ordered[i].CandidateContextTruncated = true
		}
		sort.Slice(ordered, func(left, right int) bool {
			return assessmentCandidateGroupLess(ordered[left], ordered[right])
		})
	}
	for _, group := range ordered {
		if group.CandidateContextTruncated {
			truncated = true
			break
		}
	}
	return ordered, truncated, nil
}

func assessmentCandidateGroupLess(left, right verifier.SemanticAssessmentEntityCandidateGroup) bool {
	if left.EvidenceID != right.EvidenceID {
		return left.EvidenceID < right.EvidenceID
	}
	if left.Start != right.Start {
		return left.Start < right.Start
	}
	return left.End < right.End
}

func (s *semanticAssessmentPlacementWorkerService) addKnownEntityHintCandidates(
	ctx context.Context,
	run repository.PlacementRun,
	fragment repository.EvidenceFragment,
	proposal map[string]any,
	evidenceID string,
	textIndex assessmentTextIndex,
	groups map[string]*verifier.SemanticAssessmentEntityCandidateGroup,
	knownGroupKeys map[string]struct{},
) error {
	hints := placementReviewEntityHints(proposal)
	refs := make([]string, 0, len(hints))
	for ref, hint := range hints {
		if strings.TrimSpace(hint.KnownEntityID) != "" {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	type knownEntityHint struct {
		entityID string
		spans    []assessmentTextSpan
	}
	pending := make([]knownEntityHint, 0, len(refs))
	knownIDs := make([]string, 0, len(refs))
	seenIDs := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		hint := hints[ref]
		spans := assessmentHintSpansWithIndex(fragment, hint, textIndex)
		if len(spans) == 0 {
			continue
		}
		pending = append(pending, knownEntityHint{entityID: hint.KnownEntityID, spans: spans})
		if _, exists := seenIDs[hint.KnownEntityID]; !exists {
			seenIDs[hint.KnownEntityID] = struct{}{}
			knownIDs = append(knownIDs, hint.KnownEntityID)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	candidates, err := s.catalog.ListSemanticAssessmentKnownEntities(ctx, repository.SemanticAssessmentKnownEntityInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		EntityIDs:      knownIDs,
	})
	if err != nil {
		return fmt.Errorf("revalidate known entities: %w", err)
	}
	candidatesByID := make(map[string]repository.SemanticReviewEntityCandidate, len(candidates))
	for _, candidate := range candidates {
		candidatesByID[candidate.EntityID] = candidate
	}
	for _, item := range pending {
		candidate, exists := candidatesByID[item.entityID]
		if !exists || candidate.Status != "active" {
			continue
		}
		for _, span := range item.spans {
			key := assessmentCandidateGroupKey(evidenceID, span.start, span.end)
			knownGroupKeys[key] = struct{}{}
			group := groups[key]
			if group == nil {
				group = &verifier.SemanticAssessmentEntityCandidateGroup{
					Surface:    span.surface,
					EvidenceID: evidenceID,
					Start:      span.start,
					End:        span.end,
				}
				groups[key] = group
			}
			addAssessmentEntityCandidate(group, assessmentEntityCandidate(candidate))
		}
	}
	return nil
}

func assessmentEntityCandidate(candidate repository.SemanticReviewEntityCandidate) verifier.SemanticAssessmentEntityCandidate {
	context := map[string]any{}
	for key, value := range candidate.IdentityContext {
		context[key] = value
	}
	return verifier.SemanticAssessmentEntityCandidate{
		EntityID:        candidate.EntityID,
		CanonicalName:   candidate.CanonicalName,
		Kind:            candidate.EntityKind,
		IdentityContext: context,
	}
}

func addAssessmentEntityCandidate(
	group *verifier.SemanticAssessmentEntityCandidateGroup,
	candidate verifier.SemanticAssessmentEntityCandidate,
) {
	if group == nil || candidate.EntityID == "" {
		return
	}
	for _, existing := range group.Candidates {
		if existing.EntityID == candidate.EntityID {
			return
		}
	}
	if len(group.Candidates) >= verifier.SemanticAssessmentMaxEntityCandidatesPerSurface {
		group.CandidateContextTruncated = true
		return
	}
	group.Candidates = append(group.Candidates, candidate)
}

type assessmentTextSpan struct {
	start   int
	end     int
	surface string
}

func assessmentHintSpans(fragment repository.EvidenceFragment, hint placementReviewEntityHint) []assessmentTextSpan {
	return assessmentHintSpansWithIndex(fragment, hint, newAssessmentTextIndex(fragment.Content))
}

func assessmentHintSpansWithIndex(
	fragment repository.EvidenceFragment,
	hint placementReviewEntityHint,
	textIndex assessmentTextIndex,
) []assessmentTextSpan {
	if len(hint.Evidence) > 0 {
		for _, evidence := range hint.Evidence {
			if evidence.evidenceIndex != fragment.EvidenceIndex {
				continue
			}
			surface, err := verifier.SemanticEvidenceSpan(fragment.Content, evidence.start, evidence.end)
			if err == nil && strings.TrimSpace(surface) != "" {
				return []assessmentTextSpan{{start: evidence.start, end: evidence.end, surface: surface}}
			}
		}
		return nil
	}
	return textIndex.exactTokenSpans(hint.Name)
}

type assessmentTextIndex struct {
	content     string
	runes       []rune
	byteOffsets []int
}

func newAssessmentTextIndex(content string) assessmentTextIndex {
	runes := []rune(content)
	byteOffsets := make([]int, 0, len(runes)+1)
	for offset := range content {
		byteOffsets = append(byteOffsets, offset)
	}
	byteOffsets = append(byteOffsets, len(content))
	return assessmentTextIndex{content: content, runes: runes, byteOffsets: byteOffsets}
}

func (index assessmentTextIndex) exactTokenSpans(surface string) []assessmentTextSpan {
	surface = strings.TrimSpace(surface)
	if surface == "" {
		return nil
	}
	needleLength := utf8.RuneCountInString(surface)
	if needleLength == 0 || needleLength > len(index.runes) {
		return nil
	}
	spans := make([]assessmentTextSpan, 0, 1)
	for start := 0; start+needleLength <= len(index.runes); start++ {
		end := start + needleLength
		candidate := index.content[index.byteOffsets[start]:index.byteOffsets[end]]
		if !strings.EqualFold(candidate, surface) {
			continue
		}
		if !assessmentTokenBoundary(index.runes, start, end) {
			continue
		}
		spans = append(spans, assessmentTextSpan{start: start, end: end, surface: candidate})
	}
	return spans
}

func exactTokenSpans(content, surface string) []assessmentTextSpan {
	return newAssessmentTextIndex(content).exactTokenSpans(surface)
}

func assessmentTokenBoundary(text []rune, start, end int) bool {
	if start > 0 && assessmentTokenRune(text[start-1]) {
		return false
	}
	return end >= len(text) || !assessmentTokenRune(text[end])
}

func assessmentTokenRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsNumber(value) || value == '_'
}

func assessmentCandidateGroupKey(evidenceID string, start, end int) string {
	return evidenceID + ":" + strconv.Itoa(start) + ":" + strconv.Itoa(end)
}
