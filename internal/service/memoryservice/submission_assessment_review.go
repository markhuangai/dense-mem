package memoryservice

import (
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

var errSubmissionAssessmentStaleInput = errors.New("submission assessment exact input is stale")

type submissionAssessmentNoSupportedMemoryError struct {
	RelationshipResults []repository.SubmissionRelationshipResultInput
}

type NoSupportedMemoryError = submissionAssessmentNoSupportedMemoryError

func (e *submissionAssessmentNoSupportedMemoryError) Error() string {
	return "submission assessment contains no supported memory"
}

func isRememberStaleInputError(err error) bool {
	return errors.Is(err, errSubmissionAssessmentStaleInput) ||
		errors.Is(err, repository.ErrSourceRevisionConflict) ||
		errors.Is(err, repository.ErrEvidenceLifecycleConflict) ||
		errors.Is(err, repository.ErrConflictContextStale) ||
		errors.Is(err, repository.ErrRememberExactReferenceStale) ||
		errors.Is(err, repository.ErrCorrectionTargetStale) ||
		errors.Is(err, repository.ErrSemanticStaleSource)
}

// IsRememberStaleInputError exposes the narrow stale-input classification to
// the request-scoped Remember processor.
func IsRememberStaleInputError(err error) bool {
	return isRememberStaleInputError(err)
}

func submissionAssessmentSupports(
	plan submissionAssessmentPlan,
	assessmentID string,
	spans []assessor.SemanticAssessmentEvidenceSpan,
) ([]repository.EvidenceSupportInput, error) {
	if len(spans) == 0 {
		return nil, errors.New("submission assessor relationship has no evidence span")
	}
	supports := make([]repository.EvidenceSupportInput, 0, len(spans))
	for _, span := range spans {
		item, ok := plan.itemsByEvidenceID[span.EvidenceID]
		if !ok {
			return nil, errors.New("submission assessor evidence span is outside the run")
		}
		quote, err := assessor.SemanticEvidenceSpan(item.Fragment.Content, span.Start, span.End)
		if err != nil {
			return nil, err
		}
		authority, err := semanticSupportAuthority(item.Fragment.Authority)
		if err != nil {
			return nil, err
		}
		supports = append(supports, repository.EvidenceSupportInput{
			FragmentID:       item.Fragment.FragmentID,
			SourceGroupKey:   fmt.Sprintf("semantic_assessment:%s:%s:%d:%d", assessmentID, span.EvidenceID, span.Start, span.End),
			SourceID:         item.Fragment.SourceID,
			SourceRevisionID: item.Fragment.SourceRevisionID,
			SpanStart:        span.Start,
			SpanEnd:          span.End,
			Quote:            quote,
			Authority:        authority,
			Metadata: map[string]any{
				"semantic_contract": domain.ContractVersion,
				"assessment_id":     assessmentID,
				"evidence_id":       span.EvidenceID,
			},
		})
	}
	return supports, nil
}

func submissionAssessmentItemForFragment(plan submissionAssessmentPlan, fragmentID string) (submissionAssessmentItem, bool) {
	for _, item := range plan.Items {
		if item.Fragment.FragmentID == fragmentID {
			return item, true
		}
	}
	return submissionAssessmentItem{}, false
}

// repairSubmissionAssessmentResponse records entity results that the assessor
// explicitly left ambiguous. It never invents a grounding, identity action, or
// candidate; those are provider-owned semantic decisions.
func repairSubmissionAssessmentResponse(
	plan *submissionAssessmentPlan,
	response *assessor.SemanticAssessmentResponse,
) map[string]struct{} {
	unsupported := make(map[string]struct{})
	if plan == nil || response == nil {
		return unsupported
	}
	for index := range response.EntityResults {
		result := &response.EntityResults[index]
		if _, ok := plan.entityTargetsByRef[result.Ref]; !ok {
			continue
		}
		if result.Action == string(domain.EntityResolutionAmbiguous) ||
			result.GroundingRef == nil || strings.TrimSpace(*result.GroundingRef) == "" {
			unsupported[result.Ref] = struct{}{}
		}
	}
	return unsupported
}

func unsupportedEntityResult(result assessor.SemanticAssessmentRelationshipSplit, unsupported map[string]struct{}) bool {
	if _, found := unsupported[result.SubjectRef]; found {
		return true
	}
	if result.ObjectRef != nil {
		_, found := unsupported[*result.ObjectRef]
		return found
	}
	return false
}
