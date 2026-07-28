package memoryservice

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

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
	for _, match := range matches.Matches {
		candidate := assessmentEntityCandidate(match.Candidate)
		for _, span := range exactTokenSpans(fragment.Content, match.MatchedName) {
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
	if err := s.addKnownEntityHintCandidates(ctx, run, fragment, proposal, evidenceID, groups); err != nil {
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
		if ordered[left].Start != ordered[right].Start {
			return ordered[left].Start < ordered[right].Start
		}
		return ordered[left].End < ordered[right].End
	})
	if len(ordered) > verifier.SemanticAssessmentMaxEntityResults {
		ordered = ordered[:verifier.SemanticAssessmentMaxEntityResults]
		truncated = true
		for i := range ordered {
			ordered[i].CandidateContextTruncated = true
		}
	}
	for _, group := range ordered {
		if group.CandidateContextTruncated {
			truncated = true
			break
		}
	}
	return ordered, truncated, nil
}

func (s *semanticAssessmentPlacementWorkerService) addKnownEntityHintCandidates(
	ctx context.Context,
	run repository.PlacementRun,
	fragment repository.EvidenceFragment,
	proposal map[string]any,
	evidenceID string,
	groups map[string]*verifier.SemanticAssessmentEntityCandidateGroup,
) error {
	hints := placementReviewEntityHints(proposal)
	refs := make([]string, 0, len(hints))
	for ref, hint := range hints {
		if strings.TrimSpace(hint.KnownEntityID) != "" {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	for _, ref := range refs {
		hint := hints[ref]
		candidates, err := s.catalog.ListSemanticReviewEntityCandidates(ctx, repository.SemanticReviewEntityCandidateInput{
			TeamID:         run.TeamID,
			OwnerProfileID: run.OwnerProfileID,
			KnownEntityID:  hint.KnownEntityID,
			Limit:          1,
		})
		if err != nil {
			return fmt.Errorf("revalidate known entity %q: %w", hint.KnownEntityID, err)
		}
		if len(candidates) != 1 || candidates[0].EntityID != hint.KnownEntityID || candidates[0].Status != "active" {
			return deterministicSemanticAssessmentPreflightError(
				"candidate_context_validation",
				"known entity hint is not a current active candidate",
			)
		}
		spans := assessmentHintSpans(fragment, hint)
		if len(spans) == 0 {
			continue
		}
		candidate := assessmentEntityCandidate(candidates[0])
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
	return exactTokenSpans(fragment.Content, hint.Name)
}

func exactTokenSpans(content, surface string) []assessmentTextSpan {
	surface = strings.TrimSpace(surface)
	if surface == "" {
		return nil
	}
	text := []rune(content)
	needle := []rune(surface)
	if len(needle) == 0 || len(needle) > len(text) {
		return nil
	}
	spans := make([]assessmentTextSpan, 0, 1)
	for start := 0; start+len(needle) <= len(text); start++ {
		if !strings.EqualFold(string(text[start:start+len(needle)]), surface) {
			continue
		}
		end := start + len(needle)
		if !assessmentTokenBoundary(text, start, end) {
			continue
		}
		spans = append(spans, assessmentTextSpan{start: start, end: end, surface: string(text[start:end])})
	}
	return spans
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
