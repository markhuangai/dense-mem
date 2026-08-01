package dreamservice

import (
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func dreamGeneratorInputs(inputs []repository.DreamInput) []DreamInput {
	out := make([]DreamInput, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, DreamInput{
			Type:      dreamSourceType(input),
			ID:        input.RelationshipID,
			Subject:   dreamDisplay(input.SubjectName, input.SubjectEntityID),
			Predicate: input.PredicateKey,
			Object:    dreamDisplay(input.ObjectName, firstNonEmpty(input.ObjectEntityID, input.ObjectValueID)),
			Status:    input.Status,
		})
	}
	return out
}

func dreamProposalsFromCandidates(inputs []repository.DreamInput, maxOutputs int) []repository.UpsertHypothesisInput {
	out := make([]repository.UpsertHypothesisInput, 0, maxOutputs)
	for _, input := range inputs {
		if input.Status != "pending_evidence" {
			continue
		}
		proposal := dreamProposalFromInput(input, fmt.Sprintf(
			"%s may %s %s.",
			dreamDisplay(input.SubjectName, input.SubjectEntityID),
			strings.ReplaceAll(input.PredicateKey, "_", " "),
			dreamDisplay(input.ObjectName, firstNonEmpty(input.ObjectEntityID, input.ObjectValueID)),
		))
		out = append(out, proposal)
		if len(out) >= maxOutputs {
			break
		}
	}
	return out
}

func dreamProposalsFromGenerated(
	generated []GeneratedDream,
	inputs map[string]repository.DreamInput,
	maxOutputs int,
	generatorModel string,
) ([]repository.UpsertHypothesisInput, int) {
	out := make([]repository.UpsertHypothesisInput, 0, maxOutputs)
	rejected := 0
	allowed := buildDreamEndpointAllowlist(inputs)
	for _, item := range generated {
		statement := strings.TrimSpace(item.Hypothesis)
		if statement == "" {
			rejected++
			continue
		}
		sources, ok := dreamInputsFromRefs(item.SourceRefs, inputs)
		if !ok {
			rejected++
			continue
		}
		proposal := dreamProposalFromSources(sources, statement)
		proposal.Rationale = strings.TrimSpace(item.Rationale)
		if proposal.Rationale == "" {
			proposal.Rationale = "Provider proposed an edge-shaped hypothesis from eligible Relationship inputs."
		}
		proposal.Likelihood = optionalProbability(item.Likelihood)
		proposal.Confidence = optionalProbability(item.Confidence)
		proposal.Payload["what_if"] = strings.TrimSpace(item.WhatIf)
		proposal.Payload["possible_outcome"] = strings.TrimSpace(item.PossibleOutcome)
		proposal.GeneratorKind = "provider"
		proposal.GeneratorVersion = firstNonEmpty(generatorModel, "dream-v2.provider")
		if !applyGeneratedTarget(&proposal, item, allowed) {
			rejected++
			continue
		}
		proposal.ContentHash = hypothesisContentHash(proposal)
		out = append(out, proposal)
		if len(out) >= maxOutputs {
			break
		}
	}
	return out, rejected
}

func dreamProposalsFromSeeds(
	seeds []SeedDream,
	inputs map[string]repository.DreamInput,
	maxOutputs int,
) []repository.UpsertHypothesisInput {
	out := make([]repository.UpsertHypothesisInput, 0, maxOutputs)
	for _, seed := range seeds {
		statement := strings.TrimSpace(seed.Hypothesis)
		if statement == "" {
			continue
		}
		sourceInput, ok := firstDreamSeedSource(seed.SourceRefs, inputs)
		if !ok {
			continue
		}
		proposal := dreamProposalFromInput(sourceInput, statement)
		proposal.Rationale = strings.TrimSpace(seed.Rationale)
		proposal.Likelihood = optionalProbability(seed.Likelihood)
		proposal.Confidence = optionalProbability(seed.Confidence)
		proposal.Payload["what_if"] = strings.TrimSpace(seed.WhatIf)
		proposal.Payload["possible_outcome"] = strings.TrimSpace(seed.PossibleOutcome)
		proposal.GeneratorKind = "evaluation_seed"
		proposal.GeneratorVersion = "evaluation-seed-v1"
		out = append(out, proposal)
		if len(out) >= maxOutputs {
			break
		}
	}
	return out
}

func dreamProposalFromInput(input repository.DreamInput, statement string) repository.UpsertHypothesisInput {
	return dreamProposalFromSources([]repository.DreamInput{input}, statement)
}

func dreamProposalFromSources(sources []repository.DreamInput, statement string) repository.UpsertHypothesisInput {
	input := sources[0]
	sourceRefs := make([]map[string]any, 0, len(sources))
	sourceVersions := make(map[string]int, len(sources))
	sourceOwnerProfileIDs := make([]string, 0, len(sources))
	seenOwners := map[string]struct{}{}
	sourceStatuses := make([]string, 0, len(sources))
	for _, source := range sources {
		sourceRefs = append(sourceRefs, map[string]any{
			"type": dreamSourceType(source),
			"id":   source.RelationshipID,
		})
		sourceVersions[source.RelationshipID] = source.Version
		if source.OwnerProfileID != "" {
			if _, ok := seenOwners[source.OwnerProfileID]; !ok {
				seenOwners[source.OwnerProfileID] = struct{}{}
				sourceOwnerProfileIDs = append(sourceOwnerProfileIDs, source.OwnerProfileID)
			}
		}
		sourceStatuses = append(sourceStatuses, source.Status)
	}
	input = preferredDreamTargetSource(sources)
	payload := map[string]any{
		"source_statuses": sourceStatuses,
	}
	proposal := repository.UpsertHypothesisInput{
		Statement:             strings.TrimSpace(statement),
		Rationale:             "Eligible pending relationship needs independent evidence before semantic commitment.",
		SubjectEntityID:       input.SubjectEntityID,
		PredicateKey:          input.PredicateKey,
		PredicateVersion:      input.PredicateVersion,
		ObjectEntityID:        input.ObjectEntityID,
		ObjectValueID:         input.ObjectValueID,
		SourceRefs:            sourceRefs,
		SourceVersions:        sourceVersions,
		SourceOwnerProfileIDs: sourceOwnerProfileIDs,
		GeneratorKind:         "deterministic",
		GeneratorVersion:      "dream-v2.candidate-safe",
		Payload:               payload,
	}
	proposal.ContentHash = hypothesisContentHash(proposal)
	return proposal
}

func dreamSourceType(input repository.DreamInput) string {
	switch input.Status {
	case "pending_evidence":
		return "candidate_relationship"
	default:
		return "relationship"
	}
}

func preferredDreamTargetSource(sources []repository.DreamInput) repository.DreamInput {
	for _, source := range sources {
		if source.Status == "pending_evidence" {
			return source
		}
	}
	return sources[0]
}

func dreamInputsFromRefs(
	refs []domain.DreamSourceRef,
	inputs map[string]repository.DreamInput,
) ([]repository.DreamInput, bool) {
	out := make([]repository.DreamInput, 0, len(refs))
	seen := map[string]struct{}{}
	for _, ref := range refs {
		switch ref.Type {
		case "relationship", "candidate_relationship":
		default:
			return nil, false
		}
		input, ok := inputs[ref.ID]
		if !ok {
			return nil, false
		}
		if _, ok := seen[input.RelationshipID]; ok {
			continue
		}
		seen[input.RelationshipID] = struct{}{}
		out = append(out, input)
	}
	return out, len(out) > 0
}

type dreamEndpointSet struct {
	entities map[string]struct{}
	values   map[string]struct{}
}

func buildDreamEndpointAllowlist(inputs map[string]repository.DreamInput) dreamEndpointSet {
	allowed := dreamEndpointSet{
		entities: map[string]struct{}{},
		values:   map[string]struct{}{},
	}
	for _, input := range inputs {
		if input.SubjectEntityID != "" {
			allowed.entities[input.SubjectEntityID] = struct{}{}
		}
		if input.ObjectEntityID != "" {
			allowed.entities[input.ObjectEntityID] = struct{}{}
		}
		if input.ObjectValueID != "" {
			allowed.values[input.ObjectValueID] = struct{}{}
		}
	}
	return allowed
}

func applyGeneratedTarget(
	proposal *repository.UpsertHypothesisInput,
	item GeneratedDream,
	allowed dreamEndpointSet,
) bool {
	if item.SubjectEntityID != "" {
		if _, ok := allowed.entities[item.SubjectEntityID]; !ok {
			return false
		}
		proposal.SubjectEntityID = strings.TrimSpace(item.SubjectEntityID)
	}
	if item.PredicateKey != "" {
		proposal.PredicateKey = strings.TrimSpace(item.PredicateKey)
	}
	if item.PredicateVersion > 0 {
		proposal.PredicateVersion = item.PredicateVersion
	}
	if item.ObjectEntityID != "" || item.ObjectValueID != "" {
		if (item.ObjectEntityID == "") == (item.ObjectValueID == "") {
			return false
		}
		if item.ObjectEntityID != "" {
			if _, ok := allowed.entities[item.ObjectEntityID]; !ok {
				return false
			}
			proposal.ObjectEntityID = strings.TrimSpace(item.ObjectEntityID)
			proposal.ObjectValueID = ""
		}
		if item.ObjectValueID != "" {
			if _, ok := allowed.values[item.ObjectValueID]; !ok {
				return false
			}
			proposal.ObjectEntityID = ""
			proposal.ObjectValueID = strings.TrimSpace(item.ObjectValueID)
		}
	}
	return proposal.SubjectEntityID != "" &&
		proposal.PredicateKey != "" &&
		(proposal.ObjectEntityID != "") != (proposal.ObjectValueID != "")
}

func firstDreamSeedSource(refs []domain.DreamSourceRef, inputs map[string]repository.DreamInput) (repository.DreamInput, bool) {
	for _, ref := range refs {
		switch ref.Type {
		case "relationship", "candidate_relationship":
			if input, ok := inputs[ref.ID]; ok {
				return input, true
			}
		}
	}
	return repository.DreamInput{}, false
}
