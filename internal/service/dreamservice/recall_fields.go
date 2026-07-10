package dreamservice

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/recallident"
)

func dreamRecallFields(d *domain.Dream) (string, []string) {
	if d == nil {
		return "", nil
	}
	parts := []string{
		d.Hypothesis,
		d.WhatIf,
		d.PossibleOutcome,
		d.Rationale,
		d.CycleRunID,
		d.GeneratorModel,
	}
	for _, ref := range d.SourceRefs {
		parts = append(parts, ref.Type, ref.ID)
	}
	return recallident.BuildRecallText(parts...)
}
