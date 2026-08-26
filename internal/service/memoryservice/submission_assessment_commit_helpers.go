package memoryservice

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func assessmentCompatibleCandidateExists(group *assessor.SemanticAssessmentEntityCandidateGroup, kind string) bool {
	if group == nil {
		return false
	}
	for _, candidate := range group.Candidates {
		if candidate.Kind == kind {
			return true
		}
	}
	return false
}

func semanticSupportAuthority(raw string) (string, error) {
	authority := domain.Authority(strings.TrimSpace(raw))
	if authority == "" {
		return string(domain.AuthorityPrimary), nil
	}
	if !authority.IsValid() {
		return "", fmt.Errorf("semantic support authority is unsupported: %q", authority)
	}
	return string(authority), nil
}

func semanticAssessmentPrimarySupport(supports []repository.EvidenceSupportInput) (*repository.EvidenceSupportInput, []repository.EvidenceSupportInput) {
	if len(supports) == 0 {
		return nil, nil
	}
	primary := supports[0]
	return &primary, append([]repository.EvidenceSupportInput(nil), supports[1:]...)
}

func semanticAssessmentObject(
	ref string,
	result assessor.SemanticAssessmentRelationshipSplit,
) (string, *repository.SemanticValueInput, error) {
	if result.ObjectRef != nil {
		return *result.ObjectRef, nil, nil
	}
	if result.ObjectValue == nil {
		return "", nil, errors.New("semantic assessment relationship object is missing")
	}
	display := result.ObjectValue.CanonicalValue
	if result.ObjectValue.Display != nil {
		display = *result.ObjectValue.Display
	}
	unit := ""
	if result.ObjectValue.Unit != nil {
		unit = *result.ObjectValue.Unit
	}
	return "", &repository.SemanticValueInput{
		Ref:            "value:" + ref,
		ValueType:      result.ObjectValue.ValueType,
		CanonicalValue: result.ObjectValue.CanonicalValue,
		Display:        display,
		Unit:           unit,
	}, nil
}

func semanticAssessmentValidity(result assessor.SemanticAssessmentRelationshipSplit) (*time.Time, *time.Time, error) {
	parse := func(value *string) (*time.Time, error) {
		if value == nil {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, *value)
		if err != nil {
			return nil, err
		}
		parsed = parsed.UTC()
		return &parsed, nil
	}
	from, err := parse(result.ValidFrom)
	if err != nil {
		return nil, nil, err
	}
	to, err := parse(result.ValidTo)
	if err != nil {
		return nil, nil, err
	}
	return from, to, nil
}
